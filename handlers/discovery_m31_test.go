package handlers_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"code-contract/config"
	"code-contract/handlers"
	"code-contract/internal/server/dashboard"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/discovery"
	"code-contract/internal/server/hub"
	"code-contract/internal/server/recorder"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"gorm.io/datatypes"
)

func TestAutoDownstreams_MergesActiveTCPEdges_SkipsMesh(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}

	_, db := setupRouterAndDB(t)

	var armedReqMu sync.Mutex
	var lastArmReq agentproto.ArmStandbyReq
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/agent/v1/monitors/arm" {
			var body agentproto.ArmStandbyReq
			_ = json.NewDecoder(req.Body).Decode(&body)
			armedReqMu.Lock()
			lastArmReq = body
			armedReqMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(agentproto.ArmStandbyResp{
				Revision:    body.Revision,
				ArmedDigest: agentproto.ComputeArmedDigest(body.Processes),
			})
			return
		}
		http.NotFound(w, req)
	}))
	defer agentSrv.Close()

	agent := models.Agent{
		Name: "auto-ds-agent", Endpoint: agentSrv.URL, Scope: models.ScopeShared, Status: models.AgentStatusOnline, MountsRevision: 1,
	}
	_ = db.Create(&agent).Error
	pgrp := models.ArchProcessGroup{Code: "PGRP_ADS", Name: "AutoDS组"}
	_ = db.Create(&pgrp).Error
	apArmed := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-armed", Name: "ArmedProc"}
	apDisarmed := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-disarmed", Name: "DisarmedProc"}
	_ = db.Create(&apArmed).Error
	_ = db.Create(&apDisarmed).Error

	procArmed := models.Process{
		AgentID: agent.ID, ArchProcessID: apArmed.ID, EffectiveListenPort: 26001,
		ExtraDownstreams: datatypes.JSON("[]"), ProbeDesiredState: models.ProbeDesiredStateArmed,
	}
	procDisarmed := models.Process{
		AgentID: agent.ID, ArchProcessID: apDisarmed.ID, EffectiveListenPort: 26002,
		ExtraDownstreams: datatypes.JSON("[]"), ProbeDesiredState: models.ProbeDesiredStateDisarmed,
	}
	_ = db.Create(&procArmed).Error
	_ = db.Create(&procDisarmed).Error

	// 写入 3 条订阅边：1 条 ACTIVE TCP、1 条 ACTIVE dophi_mesh://、1 条 STALE TCP
	_ = db.Create(&models.ProcessDependency{
		ConsumerProcessID: procArmed.ID, ServiceName: "ds.v1.ActiveTCP",
		ProviderKind: models.ProviderKindLocalUnenrolled, ResolvedEndpoint: "tcp://ipv4:10.1.2.3:28001", Status: models.PlacementStatusActive,
	}).Error
	_ = db.Create(&models.ProcessDependency{
		ConsumerProcessID: procArmed.ID, ServiceName: "ds.v1.ActiveMesh",
		ProviderKind: models.ProviderKindLocalUnenrolled, ResolvedEndpoint: "dophi_mesh://ipv4:10.1.2.4:28002", Status: models.PlacementStatusActive,
	}).Error
	_ = db.Create(&models.ProcessDependency{
		ConsumerProcessID: procArmed.ID, ServiceName: "ds.v1.StaleTCP",
		ProviderKind: models.ProviderKindOffline, ResolvedEndpoint: "tcp://ipv4:10.1.2.5:28003", Status: models.PlacementStatusStale,
	}).Error
	_ = db.Create(&models.ProcessDependency{
		ConsumerProcessID: procDisarmed.ID, ServiceName: "ds.v1.DisarmedTCP",
		ProviderKind: models.ProviderKindLocalUnenrolled, ResolvedEndpoint: "tcp://ipv4:10.1.2.6:28004", Status: models.PlacementStatusActive,
	}).Error

	hasEndpoint := func(eps []agentproto.DownstreamEndpoint, ip string, port uint16) bool {
		for _, ep := range eps {
			if ep.IP == ip && ep.Port == port {
				return true
			}
		}
		return false
	}

	// 1. 缺省 discovery.auto_downstreams=false：绝不并入订阅边
	dsOff, err := topology.CompileProcessDownstreams(db, procArmed.ID, nil, nil)
	if err != nil {
		t.Fatalf("CompileProcessDownstreams off: %v", err)
	}
	if hasEndpoint(dsOff, "10.1.2.3", 28001) {
		t.Fatalf("expected 10.1.2.3:28001 excluded when auto_downstreams=false, got %+v", dsOff)
	}

	// 2. 开启 discovery.auto_downstreams=true
	if err := db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "discovery.auto_downstreams").Update("setting_val", "true").Error; err != nil {
		t.Fatalf("enable discovery.auto_downstreams: %v", err)
	}

	dsArmed, err := topology.CompileProcessDownstreams(db, procArmed.ID, nil, nil)
	if err != nil {
		t.Fatalf("CompileProcessDownstreams on: %v", err)
	}
	if !hasEndpoint(dsArmed, "10.1.2.3", 28001) {
		t.Fatalf("expected ACTIVE TCP 10.1.2.3:28001 merged for ARMED proc, got %+v", dsArmed)
	}
	if hasEndpoint(dsArmed, "10.1.2.4", 28002) {
		t.Fatalf("dophi_mesh:// 10.1.2.4:28002 must be skipped, got %+v", dsArmed)
	}
	if hasEndpoint(dsArmed, "10.1.2.5", 28003) {
		t.Fatalf("STALE edge 10.1.2.5:28003 must be skipped, got %+v", dsArmed)
	}

	// DISARMED 进程不自动并入
	dsDisarmed, err := topology.CompileProcessDownstreams(db, procDisarmed.ID, nil, nil)
	if err != nil {
		t.Fatalf("CompileProcessDownstreams disarmed: %v", err)
	}
	if hasEndpoint(dsDisarmed, "10.1.2.6", 28004) {
		t.Fatalf("DISARMED proc must not merge auto_downstreams, got %+v", dsDisarmed)
	}

	// 3. 验证 hub.Manager.AutoRearmAgent 同步刷新 ExpectedArmedDigest 并下发 10.1.2.3:28001
	hubMgr := hub.NewManager(db, &config.AppConfig)
	if _, err := hubMgr.AutoRearmAgent(context.Background(), agent.ID); err != nil {
		t.Fatalf("AutoRearmAgent failed: %v", err)
	}
	armedReqMu.Lock()
	armReq := lastArmReq
	armedReqMu.Unlock()
	if len(armReq.Processes) != 1 || !hasEndpoint(armReq.Processes[0].Downstreams, "10.1.2.3", 28001) {
		t.Fatalf("AutoRearmAgent expected 10.1.2.3:28001 in ArmStandbyReq, got %+v", armReq.Processes)
	}
}

func buildSC21Descriptor(t *testing.T) []byte {
	t.Helper()
	fileProto := &descriptorpb.FileDescriptorProto{
		Syntax:  proto.String("proto3"),
		Name:    proto.String("sc21/billing.proto"),
		Package: proto.String("sc21.v1"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("OrderReq"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("amount"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_UINT64.Enum()},
				},
			},
			{
				Name: proto.String("OrderRsp"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("ok"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum()},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("OrderService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{Name: proto.String("Create"), InputType: proto.String(".sc21.v1.OrderReq"), OutputType: proto.String(".sc21.v1.OrderRsp")},
				},
			},
			{
				Name: proto.String("BillingService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{Name: proto.String("Charge"), InputType: proto.String(".sc21.v1.OrderReq"), OutputType: proto.String(".sc21.v1.OrderRsp")},
				},
			},
		},
	}
	fds := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fileProto}}
	b, _ := proto.Marshal(fds)
	return b
}

func TestSC21_ZeroModelingQuickRecord_Stage1Attribution_Correlate_AndRadar(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}
	discovery.ResetEnrollRateLimiter()
	discovery.ResetQuickRecordIdempotencyCache()

	r, db := setupRouterAndDB(t)
	if err := db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "discovery.v2_enabled").Update("setting_val", "true").Error; err != nil {
		t.Fatalf("enable v2: %v", err)
	}

	adminUser := createTestUserInDB(t, db, "sc21_admin", config.AdminRole)
	user1 := createTestUserInDB(t, db, "sc21_dev1", "developer")
	user2 := createTestUserInDB(t, db, "sc21_dev2", "developer")
	adminToken := generateTestJWT(t, adminUser)
	token1 := generateTestJWT(t, user1)
	token2 := generateTestJWT(t, user2)

	// 确认系统中初始 0 个逻辑模块
	var initModCnt int64
	_ = db.Model(&models.Module{}).Count(&initModCnt).Error
	if initModCnt != 0 {
		t.Fatalf("expected 0 initial logical modules, got %d", initModCnt)
	}

	var syncMu sync.Mutex
	var capturedSync agentproto.SyncMonitorReq

	liveTopo := agentproto.CSGTopologyReport{
		DiscoveryMode: "CROSTCP_RPCMON",
		Services: []agentproto.CSGServiceInfo{
			{ServiceName: "sc21.v1.OrderService", Addr: "tcp://ipv4:10.50.1.1:27001", Transport: "TCP", HostIP: "10.50.1.1", Port: 27001, ServerPID: 5101, ServerProc: "order_svr", IsLocal: true},
			{ServiceName: "sc21.v1.OrderService", Addr: "tcp://ipv4:10.50.1.1:27002", Transport: "TCP", HostIP: "10.50.1.1", Port: 27002, ServerPID: 5102, ServerProc: "order_svr", IsLocal: true},
			{ServiceName: "BillingImpl-BillingService", Addr: "dophi_mesh://ipv4:10.50.1.1:27003", Transport: "MESH", HostIP: "10.50.1.1", Port: 27003, ServerPID: 5201, ServerProc: "billing_svr", IsLocal: true},
			{ServiceName: "sc21.v1.SharedHostSvc", Addr: "tcp://ipv4:10.50.1.1:27001", Transport: "TCP", HostIP: "10.50.1.1", Port: 27001, ServerPID: 5101, ServerProc: "order_svr", IsLocal: true},
			{ServiceName: "sc21.v1.SharedHostSvc", Addr: "dophi_mesh://ipv4:10.50.1.1:27003", Transport: "MESH", HostIP: "10.50.1.1", Port: 27003, ServerPID: 5201, ServerProc: "billing_svr", IsLocal: true},
		},
		Subscriptions: []agentproto.CSGSubscription{
			{ClientPID: 5101, ClientProc: "order_svr", ServiceName: "BillingImpl-BillingService", IsLocal: true},
		},
	}
	discProcs := []agentproto.DiscoveredProcess{
		{PID: 5101, ListenPort: 27001, ExeName: "/opt/bin/order_svr", Cmdline: "/opt/bin/order_svr --port=27001", Transports: []string{"TCP"}},
		{PID: 5102, ListenPort: 27002, ExeName: "/opt/bin/order_svr", Cmdline: "/opt/bin/order_svr --port=27002", Transports: []string{"TCP"}},
		{PID: 5201, ListenPort: 0, ExeName: "/opt/bin/billing_svr", Cmdline: "/opt/bin/billing_svr --mesh", Transports: []string{"MESH"}},
	}

	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/agent/v1/csg/topology":
			_ = json.NewEncoder(w).Encode(liveTopo)
		case "/agent/v1/processes/discover":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": discProcs})
		case "/agent/v1/monitors/sync":
			var body agentproto.SyncMonitorReq
			_ = json.NewDecoder(req.Body).Decode(&body)
			syncMu.Lock()
			capturedSync = body
			syncMu.Unlock()
			var diags []agentproto.ProcessDiagStatus
			for _, p := range body.Processes {
				var pid uint32
				if len(p.TargetPIDs) > 0 {
					pid = p.TargetPIDs[0]
				}
				diags = append(diags, agentproto.ProcessDiagStatus{
					ProcessID: p.ProcessID, Status: "MONITORING", ResolvedPID: pid, ResolvedPIDs: p.TargetPIDs,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"diagnostics": diags})
		default:
			http.NotFound(w, req)
		}
	}))
	defer agentSrv.Close()

	sharedAgent := models.Agent{Name: "sc21-shared", Endpoint: agentSrv.URL, Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	privAgent := models.Agent{Name: "sc21-priv", Endpoint: "http://127.0.0.1:19999", Scope: models.ScopePrivate, OwnerID: &user2.ID, Status: models.AgentStatusOnline}
	if err := db.Create(&sharedAgent).Error; err != nil {
		t.Fatalf("create sharedAgent: %v", err)
	}
	if err := db.Create(&privAgent).Error; err != nil {
		t.Fatalf("create privAgent: %v", err)
	}

	// 1. 验证废弃路由 POST /api/agents/:id/csg/import-blueprint 已正式下线返回 404
	reqDead := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/agents/%d/csg/import-blueprint", sharedAgent.ID), bytes.NewReader([]byte(`{}`)))
	reqDead.Header.Set("Authorization", "Bearer "+adminToken)
	reqDead.Header.Set("Content-Type", "application/json")
	wDead := httptest.NewRecorder()
	r.ServeHTTP(wDead, reqDead)
	if wDead.Code != http.StatusNotFound {
		t.Fatalf("import-blueprint route must be decommissioned (404), got %d", wDead.Code)
	}

	// 初始化 Orchestrator 供 quick-record 调用
	decReg, err := handlers.GetDecoderRegistry()
	if err != nil {
		t.Fatalf("GetDecoderRegistry: %v", err)
	}
	orch := recorder.NewOrchestrator(db, recorder.NewExchangeAssembler(db, nil, nil, nil, decReg, nil))
	handlers.InitGlobalOrchestrator(orch)
	defer handlers.ResetGlobalOrchestrator(nil)

	// 2. 获取 live 进程键，发起一键直录 POST /api/discovery/quick-record
	reqLive := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/live?agent_id=%d", sharedAgent.ID), nil)
	reqLive.Header.Set("Authorization", "Bearer "+token1)
	wLive := httptest.NewRecorder()
	r.ServeHTTP(wLive, reqLive)
	if wLive.Code != http.StatusOK {
		t.Fatalf("GET /api/discovery/live want 200, got %d body=%s", wLive.Code, wLive.Body.String())
	}
	var liveDTO discovery.DiscoveryLiveRespDTO
	_ = json.Unmarshal(wLive.Body.Bytes(), &liveDTO)
	if len(liveDTO.Processes) != 2 {
		t.Fatalf("expected 2 process groups (order_svr folded 2 PIDs + billing_svr), got %d", len(liveDTO.Processes))
	}
	keys := []string{liveDTO.Processes[0].ProcessKey, liveDTO.Processes[1].ProcessKey}

	qrBody, _ := json.Marshal(map[string]interface{}{
		"agent_id":     sharedAgent.ID,
		"process_keys": keys,
		"request_id":   "sc21-req-1",
	})
	reqQR := httptest.NewRequest(http.MethodPost, "/api/discovery/quick-record", bytes.NewReader(qrBody))
	reqQR.Header.Set("Authorization", "Bearer "+token1)
	reqQR.Header.Set("Content-Type", "application/json")
	wQR := httptest.NewRecorder()
	r.ServeHTTP(wQR, reqQR)
	if wQR.Code != http.StatusCreated {
		t.Fatalf("POST /api/discovery/quick-record want 201, got %d body=%s", wQR.Code, wQR.Body.String())
	}
	var qrResp discovery.QuickRecordResult
	_ = json.Unmarshal(wQR.Body.Bytes(), &qrResp)
	if qrResp.Session == nil || qrResp.Session.Status != models.SessionStatusRecording {
		t.Fatalf("expected RECORDING session, got %+v", qrResp)
	}

	syncMu.Lock()
	syncSnapshot := capturedSync
	syncMu.Unlock()
	foundMultiPID := false
	for _, p := range syncSnapshot.Processes {
		if len(p.TargetPIDs) == 2 && p.TargetPIDs[0] == 5101 && p.TargetPIDs[1] == 5102 {
			foundMultiPID = true
		}
	}
	if !foundMultiPID {
		t.Fatalf("expected TargetPIDs=[5101, 5102] sent to Agent, got %+v", syncSnapshot.Processes)
	}

	// 验证幂等重放 & 并发 409 LEASE_CONFLICT
	reqQRRetry := httptest.NewRequest(http.MethodPost, "/api/discovery/quick-record", bytes.NewReader(qrBody))
	reqQRRetry.Header.Set("Authorization", "Bearer "+token1)
	reqQRRetry.Header.Set("Content-Type", "application/json")
	wQRRetry := httptest.NewRecorder()
	r.ServeHTTP(wQRRetry, reqQRRetry)
	if wQRRetry.Code != http.StatusOK {
		t.Fatalf("idempotent retry want 200, got %d body=%s", wQRRetry.Code, wQRRetry.Body.String())
	}

	qrConflictBody, _ := json.Marshal(map[string]interface{}{
		"agent_id":     sharedAgent.ID,
		"process_keys": keys,
		"request_id":   "sc21-req-user2",
	})
	reqQRConf := httptest.NewRequest(http.MethodPost, "/api/discovery/quick-record", bytes.NewReader(qrConflictBody))
	reqQRConf.Header.Set("Authorization", "Bearer "+token2)
	reqQRConf.Header.Set("Content-Type", "application/json")
	wQRConf := httptest.NewRecorder()
	r.ServeHTTP(wQRConf, reqQRConf)
	if wQRConf.Code != http.StatusConflict {
		t.Fatalf("concurrent quick-record by user2 want 409 LEASE_CONFLICT, got %d body=%s", wQRConf.Code, wQRConf.Body.String())
	}

	// 3. 上传并激活 .pb，触发 POST /api/catalog/correlate 与 confirm-proto
	descBytes := buildSC21Descriptor(t)
	if _, _, _, err := decReg.UploadSchema(decoder.UploadSchemaInput{
		Name: "sc21_pb", Category: models.ProtoCategoryBusiness, VersionTag: "v1.0.0", FileName: "sc21.pb", RawBytes: descBytes, AutoActivate: true,
	}); err != nil {
		t.Fatalf("UploadSchema: %v", err)
	}

	reqCorr := httptest.NewRequest(http.MethodPost, "/api/catalog/correlate", bytes.NewReader([]byte(`{}`)))
	reqCorr.Header.Set("Authorization", "Bearer "+token1)
	reqCorr.Header.Set("Content-Type", "application/json")
	wCorr := httptest.NewRecorder()
	r.ServeHTTP(wCorr, reqCorr)
	if wCorr.Code != http.StatusOK {
		t.Fatalf("POST /api/catalog/correlate want 200, got %d body=%s", wCorr.Code, wCorr.Body.String())
	}
	var corrRes topology.CatalogCorrelateResult
	_ = json.Unmarshal(wCorr.Body.Bytes(), &corrRes)
	if corrRes.ExactMatched < 1 || corrRes.SuggestedCount < 1 {
		t.Fatalf("expected >=1 EXACT and >=1 SUGGESTED, got %+v", corrRes)
	}

	// 4. 注入流量并执行 Stage 1 归因（验证正常解析 + 多宿主 AMBIGUOUS + .pb 解码）
	now := time.Now().UTC()
	pm := &models.PartitionManager{}
	_, _ = pm.EnsureDailyPartition(db, now)

	var allProcs []models.Process
	if err := db.Preload("ArchProcess").Where("agent_id = ?", sharedAgent.ID).Find(&allProcs).Error; err != nil {
		t.Fatalf("query sharedAgent processes: %v", err)
	}
	var procOrder, procBilling models.Process
	for _, p := range allProcs {
		code := ""
		if p.ArchProcess != nil {
			code = p.ArchProcess.Code
		}
		if p.EffectiveListenPort == 27001 || code == "order_svr" {
			procOrder = p
		} else {
			procBilling = p
		}
	}
	if procOrder.ID == 0 || procBilling.ID == 0 {
		t.Fatalf("expected both procOrder and procBilling enrolled, got %+v", allProcs)
	}

	var fds descriptorpb.FileDescriptorSet
	_ = proto.Unmarshal(descBytes, &fds)
	files, _ := protodesc.NewFiles(&fds)
	reqDesc, _ := files.FindDescriptorByName("sc21.v1.OrderReq")
	reqMD := reqDesc.(protoreflect.MessageDescriptor)
	reqMsg := dynamicpb.NewMessage(reqMD)
	reqMsg.Set(reqMD.Fields().ByName("amount"), protoreflect.ValueOfUint64(500))
	pbBytes, _ := proto.Marshal(reqMsg)

	sh := make([]byte, 160)
	binary.BigEndian.PutUint16(sh[0:2], 1)
	binary.BigEndian.PutUint16(sh[2:4], 160)
	copy(sh[8:72], []byte("sc21.v1.OrderService"))
	copy(sh[72:136], []byte("Create"))
	sh[136] = crosrpc.ReqTypeSync
	ch := crosrpc.CommHead{TransportProto: crosrpc.TransportTCP, SNO: 7771, Length: uint32(160 + len(pbBytes)), HeadLen: 160, MsgType: crosrpc.MsgTypeReq}
	copy(ch.Magic[:], []byte(crosrpc.MagicPrefix))
	rawReq := append(append(ch.Marshal(), sh...), pbBytes...)

	trDecoded := models.TrafficRecord{
		CreatedAt: now, SessionID: qrResp.Session.ID, SeqNo: 1, AgentID: sharedAgent.ID, ProcessID: procBilling.ID,
		CapturedPID: 5201, ConnKey: "c1", Direction: models.TrafficDirEgress, ExchangeType: models.ExchangeUnary,
		SrcAddr: "10.50.1.1:50001", DstAddr: "10.50.1.1:27001", ReqCapturedAtNs: now.UnixNano(),
		ServiceName: "sc21.v1.OrderService", Operation: "Create", RawReqPacket: rawReq,
		DecodeStatus: decoder.DecodeStatusNoSchemaRawHex, IsReplayable: true,
	}
	trAmbig := models.TrafficRecord{
		CreatedAt: now, SessionID: qrResp.Session.ID, SeqNo: 2, AgentID: sharedAgent.ID, ProcessID: procOrder.ID,
		CapturedPID: 5101, ConnKey: "c2", Direction: models.TrafficDirEgress, ExchangeType: models.ExchangeUnary,
		SrcAddr: "10.50.1.1:50002", DstAddr: "10.99.99.99:9999", ReqCapturedAtNs: now.UnixNano() + 1,
		ServiceName: "sc21.v1.SharedHostSvc", Operation: "Ping", IsReplayable: true,
	}
	if err := db.Create(&trDecoded).Error; err != nil {
		t.Fatalf("create trDecoded: %v", err)
	}
	if err := db.Create(&trAmbig).Error; err != nil {
		t.Fatalf("create trAmbig: %v", err)
	}

	if err := topology.OfflineBatchAttributionWithDecoder(context.Background(), db, qrResp.Session.ID, decReg); err != nil {
		t.Fatalf("Stage 1 attribution failed: %v", err)
	}
	var gotDecoded, gotAmbig models.TrafficRecord
	_ = db.Where("session_id = ? AND seq_no = 1", qrResp.Session.ID).First(&gotDecoded).Error
	_ = db.Where("session_id = ? AND seq_no = 2", qrResp.Session.ID).First(&gotAmbig).Error
	if gotDecoded.DstArchProcessID == nil || *gotDecoded.DstArchProcessID != procOrder.ArchProcessID {
		t.Fatalf("expected OrderService DstArchProcessID=%d, got %v", procOrder.ArchProcessID, gotDecoded.DstArchProcessID)
	}
	if gotDecoded.DecodeStatus != decoder.DecodeStatusDecodedJSON {
		t.Fatalf("expected DECODED_JSON, got %s", gotDecoded.DecodeStatus)
	}
	if gotAmbig.DstArchProcessID != nil {
		t.Fatalf("AMBIGUOUS record must keep DstArchProcessID=nil, got %d", *gotAmbig.DstArchProcessID)
	}

	// 5. 验证 POST /api/discovery/simulation-plan
	planBody, _ := json.Marshal(map[string]interface{}{
		"agent_id":        sharedAgent.ID,
		"arch_process_id": procOrder.ArchProcessID,
	})
	reqPlan := httptest.NewRequest(http.MethodPost, "/api/discovery/simulation-plan", bytes.NewReader(planBody))
	reqPlan.Header.Set("Authorization", "Bearer "+token1)
	reqPlan.Header.Set("Content-Type", "application/json")
	wPlan := httptest.NewRecorder()
	r.ServeHTTP(wPlan, reqPlan)
	if wPlan.Code != http.StatusOK {
		t.Fatalf("POST /api/discovery/simulation-plan want 200, got %d body=%s", wPlan.Code, wPlan.Body.String())
	}
	var planRes discovery.SimulationPlanResult
	_ = json.Unmarshal(wPlan.Body.Bytes(), &planRes)
	if planRes.RecommendedStrategy != string(agentproto.CSGStrategyMockServer) {
		t.Fatalf("expected MOCK_SERVER for order_svr subscribing to MESH billing_svr, got %s", planRes.RecommendedStrategy)
	}

	// 6. 写入 PRIVATE 噪音并验证统一契约目录与 /api/dashboard/overview 五态雷达排除 PRIVATE
	pgrpPriv := models.ArchProcessGroup{Code: "PGRP_SC21_PRIV", Name: "SC21Priv"}
	if err := db.Create(&pgrpPriv).Error; err != nil {
		t.Fatalf("create pgrpPriv: %v", err)
	}
	apPriv := models.ArchProcess{GroupID: pgrpPriv.ID, Code: "prv-sc21", Name: "PrivProc", Lifecycle: models.ArchProcLifecycleProvisional, OriginAgentID: &privAgent.ID}
	if err := db.Create(&apPriv).Error; err != nil {
		t.Fatalf("create apPriv: %v", err)
	}
	procPriv := models.Process{AgentID: privAgent.ID, ArchProcessID: apPriv.ID, EffectiveListenPort: 27999, ExtraDownstreams: datatypes.JSON("[]")}
	if err := db.Create(&procPriv).Error; err != nil {
		t.Fatalf("create procPriv: %v", err)
	}
	svcPriv := models.ModuleService{ServiceName: "sc21.v1.PrivateOnlyShadow", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	if err := db.Create(&svcPriv).Error; err != nil {
		t.Fatalf("create svcPriv: %v", err)
	}
	if err := db.Create(&models.ServicePlacement{ModuleServiceID: svcPriv.ID, ProcessID: procPriv.ID, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now}).Error; err != nil {
		t.Fatalf("create priv ServicePlacement: %v", err)
	}

	reqCat := httptest.NewRequest(http.MethodGet, "/api/catalog/services", nil)
	reqCat.Header.Set("Authorization", "Bearer "+token1)
	wCat := httptest.NewRecorder()
	r.ServeHTTP(wCat, reqCat)
	if wCat.Code != http.StatusOK {
		t.Fatalf("GET /api/catalog/services want 200, got %d", wCat.Code)
	}
	var catResp topology.CatalogServicesResponseDTO
	_ = json.Unmarshal(wCat.Body.Bytes(), &catResp)
	for _, it := range catResp.Items {
		if it.ServiceName == "sc21.v1.PrivateOnlyShadow" {
			t.Fatalf("PRIVATE service leaked into user1 catalog")
		}
	}

	reqDash := httptest.NewRequest(http.MethodGet, "/api/dashboard/overview", nil)
	reqDash.Header.Set("Authorization", "Bearer "+token1)
	wDash := httptest.NewRecorder()
	r.ServeHTTP(wDash, reqDash)
	if wDash.Code != http.StatusOK {
		t.Fatalf("GET /api/dashboard/overview want 200, got %d", wDash.Code)
	}
	var dashDTO dashboard.DashboardOverviewDTO
	_ = json.Unmarshal(wDash.Body.Bytes(), &dashDTO)
	if dashDTO.FiveStateRadar == nil || !dashDTO.FiveStateRadar.AuthorityScopeOnly {
		t.Fatalf("expected FiveStateRadar with AuthorityScopeOnly=true, got %+v", dashDTO.FiveStateRadar)
	}
	for _, s := range dashDTO.FiveStateRadar.ShadowLiveServices {
		if s == "sc21.v1.PrivateOnlyShadow" {
			t.Fatalf("PRIVATE service leaked into FiveStateRadar")
		}
	}
}
