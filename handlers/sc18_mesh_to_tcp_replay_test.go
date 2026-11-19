package handlers_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"code-common/backend/testdb"
	"code-contract/config"
	"code-contract/handlers"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/diff"
	"code-contract/internal/server/recorder"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
	"code-contract/pkg/csgdlink"
	"code-contract/pkg/csgmock"
	"code-contract/pkg/replay"
	"code-contract/pkg/transport"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"gorm.io/datatypes"
)

func buildSC18ProtoDescriptor(t *testing.T) []byte {
	t.Helper()
	fds := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name:    proto.String("mesh_order_v1.proto"),
				Package: proto.String("order.v1"),
				Syntax:  proto.String("proto3"),
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("OrderReq"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:     proto.String("id"),
								JsonName: proto.String("id"),
								Number:   proto.Int32(1),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
							},
						},
					},
					{
						Name: proto.String("OrderRsp"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:     proto.String("ok"),
								JsonName: proto.String("ok"),
								Number:   proto.Int32(1),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
							},
						},
					},
				},
				Service: []*descriptorpb.ServiceDescriptorProto{
					{
						Name: proto.String("OrderService"),
						Method: []*descriptorpb.MethodDescriptorProto{
							{
								Name:       proto.String("CreateOrder"),
								InputType:  proto.String(".order.v1.OrderReq"),
								OutputType: proto.String(".order.v1.OrderRsp"),
							},
						},
					},
				},
			},
		},
	}
	raw, err := proto.Marshal(fds)
	if err != nil {
		t.Fatalf("marshal FileDescriptorSet failed: %v", err)
	}
	return raw
}

func buildTestCrosMeshReqFrame(sno uint64, reqType uint8, svc, op string, payload []byte) []byte {
	pkt := buildTestCrosReqFrame(sno, reqType, svc, op, payload)
	pkt[7] = crosrpc.TransportMesh // 4
	return pkt
}

func buildTestCrosMeshRspFrame(sno uint64, msgType uint8, msgNo uint16, statusCode uint32, svc, op string, payload []byte) []byte {
	pkt := buildTestCrosRspFrame(sno, msgType, msgNo, statusCode, svc, op, payload)
	pkt[7] = crosrpc.TransportMesh // 4
	return pkt
}

// TestSC18_EndToEnd_MeshCaptureToMOCKServerTCPReplay_ZeroLeak
// 核心验收 SC-18：
// builtin-drt-uprobe 采集 ucProtocol=4 ➔ 主控落盘 transport_proto=4 + .pb 解码 + 四段解剖
// ➔ MOCK_SERVER (tcp://) 跨传输重放 ucProtocol (4->1) 归一化 ➔ 双层 Diff 100% PASS ➔ 五出口卸载零残留
func TestSC18_EndToEnd_MeshCaptureToMOCKServerTCPReplay_ZeroLeak(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}

	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB 失败: %v", err)
	}
	r, _ := setupGoldenSuiteTestServer(t, db)
	adminUser, adminToken := createTestUser(t, db, "admin_sc18_user", config.AdminRole)

	// 1. 基础拓扑与 Schema 资产准备
	subsys := models.Subsystem{Code: "SUB_SC18", Name: "SC18Subsys"}
	db.Create(&subsys)
	fg := models.FunctionalGroup{SubsystemID: subsys.ID, Code: "FG_SC18", Name: "SC18FG"}
	db.Create(&fg)
	grp := models.ArchProcessGroup{Code: "GRP_SC18", Name: "SC18Grp"}
	db.Create(&grp)
	archProc := models.ArchProcess{
		Code:              "order-service-proc",
		Name:              "OrderServiceImpl-OrderService",
		GroupID:           grp.ID,
		DefaultListenPort: 8099,
	}
	db.Create(&archProc)
	mod := models.Module{
		SubsystemID:   subsys.ID,
		GroupID:       fg.ID,
		Code:          "MOD_ORDER_SUT",
		Name:          "订单核心服务",
		ArchProcessID: &archProc.ID,
	}
	db.Create(&mod)

	reg, regErr := handlers.GetDecoderRegistry()
	if regErr != nil {
		t.Fatalf("GetDecoderRegistry failed: %v", regErr)
	}
	if _, _, _, err := reg.UploadSchema(decoder.UploadSchemaInput{
		Name:         "order_sc18",
		VersionTag:   "v1.0.0",
		FileName:     "order_v1.pb",
		RawBytes:     buildSC18ProtoDescriptor(t),
		AutoActivate: true,
		OperatorID:   adminUser.ID,
	}); err != nil {
		t.Fatalf("UploadSchema failed: %v", err)
	}

	agent := &models.Agent{
		Name:            "sc18-mesh-agent",
		Endpoint:        "http://127.0.0.1:8092",
		Scope:           "SHARED",
		Status:          "ONLINE",
		HasEbpfCap:      true,
		MountsRevision:  1,
		ProtocolVersion: "v1",
		AgentVersion:    "1.0.0",
	}
	if err := db.Create(agent).Error; err != nil {
		t.Fatalf("create agent failed: %v", err)
	}

	session := &models.RecordingSession{
		SessionNo:        "REC-SC18-001",
		Name:             "SC18 Mesh Capture Session",
		Scope:            "SHARED",
		OwnerID:          adminUser.ID,
		Status:           "COMPLETED",
		TargetProcessIDs: datatypes.JSON([]byte(fmt.Sprintf("[%d]", archProc.ID))),
		MaxFrames:        100,
	}
	if err := db.Create(session).Error; err != nil {
		t.Fatalf("create recording session failed: %v", err)
	}

	// 2. 模拟 dophi_mesh 数据面写侧旁路采集与双向相关性归一化 (builtin-drt-uprobe)
	asmCfg := recorder.AssemblerConfig{
		WorkerShards:    1,
		BatchSize:       10,
		FlushInterval:   50 * time.Millisecond,
		DefaultTimeout:  5 * time.Second,
		MaxPendingLimit: 1000,
	}
	assembler := recorder.NewExchangeAssembler(db, nil, nil, nil, reg, &asmCfg)
	defer assembler.Close()

	drv := transport.NewDRTUprobeDriver(nil, transport.DRTUprobeConfig{
		ProcRoot:      "/proc",
		PayloadOffset: 0,
		MaxInflight:   1000,
		InflightTTL:   30 * time.Second,
	})
	defer drv.Close()

	sno := uint64(88001)
	reqPB := []byte{0x0a, 0x0b, 'O', 'R', 'D', '_', 'M', 'E', 'S', 'H', '_', '1', '8'} // field 1 = "ORD_MESH_18"
	rspPB := []byte{0x0a, 0x04, 't', 'r', 'u', 'e'}                                    // field 1 = "true"

	reqPkt := buildTestCrosMeshReqFrame(sno, crosrpc.ReqTypeSync, "order.v1.OrderService", "CreateOrder", reqPB)
	rspPkt := buildTestCrosMeshRspFrame(sno, crosrpc.MsgTypeRsp, 1, 0, "order.v1.OrderService", "CreateOrder", rspPB)

	sink := transport.FrameSinkFunc(func(h agentproto.EnvelopeHeader, payload []byte) error {
		assembler.Feed(agent.ID, h, payload)
		return nil
	})

	reqEvt := transport.DRTUprobeEvent{
		SessionID:   session.ID,
		ProcessID:   archProc.ID,
		TimestampNs: uint64(time.Now().UnixNano()),
		Handle:      0x9000,
		PID:         12345,
		DataLen:     uint32(len(reqPkt)),
		Data:        reqPkt,
	}
	if err := drv.ProcessEvent(reqEvt, sink); err != nil {
		t.Fatalf("drv.ProcessEvent req failed: %v", err)
	}

	rspEvt := transport.DRTUprobeEvent{
		SessionID:   session.ID,
		ProcessID:   archProc.ID,
		TimestampNs: uint64(time.Now().UnixNano()),
		Handle:      0x9000,
		PID:         23456, // 服务端 PID
		DataLen:     uint32(len(rspPkt)),
		Data:        rspPkt,
	}
	if err := drv.ProcessEvent(rspEvt, sink); err != nil {
		t.Fatalf("drv.ProcessEvent rsp failed: %v", err)
	}

	// 触发排空落盘
	assembler.Flush()
	time.Sleep(100 * time.Millisecond)

	// 3. 校验数据库落盘：transport_proto=4 + .pb 解码完整落盘
	var rec models.TrafficRecord
	if err := db.Where("session_id = ? AND sno = ?", session.ID, sno).First(&rec).Error; err != nil {
		t.Fatalf("find recorded traffic record failed: %v", err)
	}

	if rec.TransportProto != 4 {
		t.Fatalf("expected rec.TransportProto = 4 (Mesh), got %d", rec.TransportProto)
	}
	if rec.ExchangeType != models.ExchangeUnary {
		t.Fatalf("expected rec.ExchangeType = %s, got %s", models.ExchangeUnary, rec.ExchangeType)
	}
	if rec.DecodeStatus != decoder.DecodeStatusDecodedJSON && rec.DecodeStatus != decoder.DecodeStatusSuccess {
		t.Fatalf("expected rec.DecodeStatus = DECODED_JSON or SUCCESS, got %s", rec.DecodeStatus)
	}
	if !strings.Contains(string(rec.PayloadJSON), "ORD_MESH_18") {
		t.Fatalf("expected PayloadJSON to contain ORD_MESH_18, got %s", string(rec.PayloadJSON))
	}
	if !strings.Contains(string(rec.PayloadJSON), "true") {
		t.Fatalf("expected PayloadJSON to contain true, got %s", string(rec.PayloadJSON))
	}

	// 4. 四段解剖 API 校验 (GET /api/traffic-records/:id/anatomy)
	reqAnatomy := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/traffic-records/%d/anatomy", rec.ID), nil)
	reqAnatomy.Header.Set("Authorization", "Bearer "+adminToken)
	wAnatomy := httptest.NewRecorder()
	r.ServeHTTP(wAnatomy, reqAnatomy)

	if wAnatomy.Code != http.StatusOK {
		t.Fatalf("anatomy API returned %d: %s", wAnatomy.Code, wAnatomy.Body.String())
	}
	var anatomyResp map[string]interface{}
	if err := json.Unmarshal(wAnatomy.Body.Bytes(), &anatomyResp); err != nil {
		t.Fatalf("unmarshal anatomyResp failed: %v", err)
	}
	reqSide, ok := anatomyResp["reqSide"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing reqSide in anatomy response: %+v", anatomyResp)
	}
	commHead, ok := reqSide["commHead"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing commHead in reqSide: %+v", reqSide)
	}
	transProtoNum := int(commHead["transportProtocol"].(float64))
	transProtoName := commHead["transportName"].(string)
	if transProtoNum != 4 {
		t.Errorf("anatomy transportProtocol expected 4, got %d", transProtoNum)
	}
	if !strings.Contains(transProtoName, "MESH") {
		t.Errorf("anatomy transportName expected to contain MESH, got %q", transProtoName)
	}

	// 5. 跨传输重放收敛至 tcp:// (MOCK_SERVER 模式)
	// 启动 SUT 本地回环真实 TCP 监听
	sutLn, sutAddr, sutPort := startSC17LoopbackSUTServer(t, rspPB)
	defer sutLn.Close()

	// 启动沙箱 MockCSGServer
	mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mockCSG.Start(); err != nil {
		t.Fatalf("start mock csg failed: %v", err)
	}
	defer mockCSG.Close()

	// 启用 dophi_mesh:// ➔ tcp:// 自动改写放行
	mockCSG.EnableMeshToTCPRewrite(true)

	// 注册服务实例 (模拟 dophi_mesh 服务发布)
	mockCSG.SeedInstance(csgdlink.Instance{
		ProcID:   uint32(os.Getpid()),
		Name32:   "order.v1.OrderService",
		Addr:     fmt.Sprintf("dophi_mesh://ipv4:127.0.0.1:%d", sutPort),
		Name64:   "order.v1.OrderService",
		OsNodeID: 1,
	})

	// 校验通过 CSG Dlink 客户端以纯 dophi_mesh 协议订阅 (Trans[4]=1, Trans[1]=0)
	dlinkClient, err := csgdlink.Dial(mockCSG.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial mock csg failed: %v", err)
	}
	defer dlinkClient.Close()

	var meshTrans [7]byte
	meshTrans[4] = 1 // dophi_mesh 独占订阅
	subInsts, err := dlinkClient.SubWithTrans("order.v1.OrderService", meshTrans, 2*time.Second)
	if err != nil {
		t.Fatalf("sub with meshTrans failed: %v", err)
	}
	if len(subInsts) == 0 {
		t.Fatalf("expected non-empty subInsts")
	}
	if !csgdlink.IsTCPAddr(subInsts[0].Addr) {
		t.Fatalf("expected rewritten addr to be TCP, got %s", subInsts[0].Addr)
	}
	if mockCSG.MeshRewriteCount() == 0 {
		t.Fatalf("expected MeshRewriteCount > 0, got %d", mockCSG.MeshRewriteCount())
	}

	// 6. 执行跨传输回放与双层 Diff 100% PASS 验证
	// 验证 HealResponseFrames / ReplayExecutor 在发往 TCP SUT 时将 ucProtocol 从 4 归一化为 1
	healedReqFrame := replay.NormalizeFrameTransportForTCP(rec.RawReqPacket)
	if healedReqFrame[7] != crosrpc.TransportTCP {
		t.Fatalf("expected healedReqFrame[7] = 1 (TCP), got %d", healedReqFrame[7])
	}
	// 原落盘报文切片保持 4 不可变
	if rec.RawReqPacket[7] != crosrpc.TransportMesh {
		t.Fatalf("original RawReqPacket[7] mutated! expected 4, got %d", rec.RawReqPacket[7])
	}

	// 发送重放请求并接收真实 TCP SUT 应答
	tcpConn, err := net.Dial("tcp", sutAddr)
	if err != nil {
		t.Fatalf("dial sut failed: %v", err)
	}
	defer tcpConn.Close()

	if _, err := tcpConn.Write(healedReqFrame); err != nil {
		t.Fatalf("write healedReqFrame failed: %v", err)
	}

	rspBuf := make([]byte, 8192)
	_ = tcpConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := tcpConn.Read(rspBuf)
	if err != nil {
		t.Fatalf("read sut response failed: %v", err)
	}
	actualRspPacket := rspBuf[:n]

	// 运行双层 Diff 引擎比对
	l1Res, _, err := diff.DiffLayer1Header(rec.RawRspPacket, actualRspPacket)
	if err != nil {
		t.Fatalf("DiffLayer1Header failed: %v", err)
	}
	if l1Res.Status != models.ReplayResultPass {
		t.Fatalf("expected Layer 1 PASS, got %s: %s", l1Res.Status, l1Res.FailureReason)
	}

	op := reg.LookupOperation("order.v1.OrderService", "CreateOrder")
	if op == nil {
		t.Fatalf("LookupOperation order.v1.OrderService/CreateOrder failed")
	}

	parsedExp, errExp := crosrpc.ParsePacket(rec.RawRspPacket)
	if errExp != nil {
		t.Fatalf("ParsePacket rec.RawRspPacket failed: %v", errExp)
	}
	parsedAct, errAct := crosrpc.ParsePacket(actualRspPacket)
	if errAct != nil {
		t.Fatalf("ParsePacket actualRspPacket failed: %v", errAct)
	}

	expPayload := rec.RawRspPacket[parsedExp.PayloadOffset : parsedExp.PayloadOffset+parsedExp.PayloadLen]
	actPayload := actualRspPacket[parsedAct.PayloadOffset : parsedAct.PayloadOffset+parsedAct.PayloadLen]

	fieldDiffs, err := diff.DiffLayer2ProtoSemantic(expPayload, actPayload, op, nil)
	if err != nil {
		t.Fatalf("DiffLayer2ProtoSemantic failed: %v", err)
	}
	if len(fieldDiffs) != 0 {
		t.Fatalf("expected 0 proto field diffs (100%% PASS), got %d diffs: %+v", len(fieldDiffs), fieldDiffs)
	}

	// 7. 五出口卸载零残留断言
	if err := drv.Close(); err != nil {
		t.Fatalf("drv.Close failed: %v", err)
	}
	if drv.ActiveLinksCount() != 0 {
		t.Errorf("expected 0 active links after drv.Close(), got %d", drv.ActiveLinksCount())
	}

	t.Logf("SC-18 dophi_mesh 端到端采集、落盘、TCP 跨传输重放归一化与 100%% Diff PASS 验证全部通过！")
}
