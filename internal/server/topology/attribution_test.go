package topology

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	"code-common/backend/testdb"
	"code-contract/config"
	"code-contract/internal/server/decoder"
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

func buildStage1DescriptorBytes(t *testing.T, pkgName, svcName, methodName string) []byte {
	t.Helper()
	fileProto := &descriptorpb.FileDescriptorProto{
		Syntax:  proto.String("proto3"),
		Name:    proto.String("stage1/test_svc.proto"),
		Package: proto.String(pkgName),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String(svcName + "Req"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   proto.String("order_id"),
						Number: proto.Int32(1),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_UINT64.Enum(),
					},
				},
			},
			{
				Name: proto.String(svcName + "Rsp"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   proto.String("ok"),
						Number: proto.Int32(1),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(),
					},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String(svcName),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String(methodName),
						InputType:  proto.String("." + pkgName + "." + svcName + "Req"),
						OutputType: proto.String("." + pkgName + "." + svcName + "Rsp"),
					},
				},
			},
		},
	}
	fds := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fileProto}}
	b, err := proto.Marshal(fds)
	if err != nil {
		t.Fatalf("marshal descriptor failed: %v", err)
	}
	return b
}

func buildTestReqFrame(t *testing.T, serviceName, opName string, pbPayload []byte) []byte {
	t.Helper()
	sh := make([]byte, 160)
	binary.BigEndian.PutUint16(sh[0:2], 1)
	binary.BigEndian.PutUint16(sh[2:4], 160)
	copy(sh[8:72], []byte(serviceName))
	copy(sh[72:136], []byte(opName))
	sh[136] = crosrpc.ReqTypeSync

	ch := crosrpc.CommHead{
		TransportProto: crosrpc.TransportTCP,
		SNO:            99001,
		Length:         uint32(160 + len(pbPayload)),
		HeadLen:        160,
		MsgType:        crosrpc.MsgTypeReq,
		IsPacked:       0,
	}
	copy(ch.Magic[:], []byte(crosrpc.MagicPrefix))

	var out []byte
	out = append(out, ch.Marshal()...)
	out = append(out, sh...)
	out = append(out, pbPayload...)
	return out
}

func TestStage1Attribution_FourTierPriority_AndAmbiguousNeverGuesses(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB failed: %v", err)
	}

	privOwner := uint(77)
	agent1 := models.Agent{Name: "s1-shared-1", Endpoint: "http://10.1.0.1:19001", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	agent2 := models.Agent{Name: "s1-shared-2", Endpoint: "http://10.2.0.1:19001", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	agentPriv := models.Agent{Name: "s1-priv", Endpoint: "http://10.9.0.1:19001", Scope: models.ScopePrivate, OwnerID: &privOwner, Status: models.AgentStatusOnline}
	_ = db.Create(&agent1).Error
	_ = db.Create(&agent2).Error
	_ = db.Create(&agentPriv).Error

	pgrp := models.ArchProcessGroup{Code: "PGRP_S1", Name: "Stage1组"}
	_ = db.Create(&pgrp).Error
	apCaller := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-caller", Name: "Caller", Lifecycle: models.ArchProcLifecycleConfirmed}
	apTier1 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-tier1", Name: "Tier1", Lifecycle: models.ArchProcLifecycleConfirmed}
	apTier2 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-tier2", Name: "Tier2", Lifecycle: models.ArchProcLifecycleConfirmed}
	apTier3 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-tier3", Name: "Tier3", Lifecycle: models.ArchProcLifecycleConfirmed}
	apMulti1 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-multi1", Name: "Multi1", Lifecycle: models.ArchProcLifecycleConfirmed}
	apMulti2 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-multi2", Name: "Multi2", Lifecycle: models.ArchProcLifecycleConfirmed}
	apPriv := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-priv", Name: "Priv", Lifecycle: models.ArchProcLifecycleProvisional, OriginAgentID: &agentPriv.ID}
	for _, ap := range []*models.ArchProcess{&apCaller, &apTier1, &apTier2, &apTier3, &apMulti1, &apMulti2, &apPriv} {
		if err := db.Create(ap).Error; err != nil {
			t.Fatalf("create arch process %s: %v", ap.Code, err)
		}
	}

	procCaller := models.Process{AgentID: agent1.ID, ArchProcessID: apCaller.ID, EffectiveListenPort: 20000, ExtraDownstreams: datatypes.JSON("[]")}
	procTier1 := models.Process{AgentID: agent1.ID, ArchProcessID: apTier1.ID, EffectiveListenPort: 21001, ExtraDownstreams: datatypes.JSON("[]")}
	procTier2 := models.Process{AgentID: agent2.ID, ArchProcessID: apTier2.ID, EffectiveListenPort: 21002, ExtraDownstreams: datatypes.JSON("[]")}
	procTier3 := models.Process{AgentID: agent1.ID, ArchProcessID: apTier3.ID, EffectiveListenPort: 21003, ExtraDownstreams: datatypes.JSON("[]")}
	procMulti1 := models.Process{AgentID: agent1.ID, ArchProcessID: apMulti1.ID, EffectiveListenPort: 21005, ExtraDownstreams: datatypes.JSON("[]")}
	procMulti2 := models.Process{AgentID: agent2.ID, ArchProcessID: apMulti2.ID, EffectiveListenPort: 21006, ExtraDownstreams: datatypes.JSON("[]")}
	procPriv := models.Process{AgentID: agentPriv.ID, ArchProcessID: apPriv.ID, EffectiveListenPort: 21007, ExtraDownstreams: datatypes.JSON("[]")}
	for _, p := range []*models.Process{&procCaller, &procTier1, &procTier2, &procTier3, &procMulti1, &procMulti2, &procPriv} {
		if err := db.Create(p).Error; err != nil {
			t.Fatalf("create process: %v", err)
		}
	}

	// 全部使用未认领服务身份（0 逻辑模块）验证 Stage 1 归因
	svcTier1 := models.ModuleService{ServiceName: "SnapSvc-SnapSvc", ProtoServiceName: "stage1.v1.SnapSvc", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	svcTier2 := models.ModuleService{ServiceName: "stage1.v1.EndpointSvc", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	svcTier3 := models.ModuleService{ServiceName: "stage1.v1.SharedUniqueSvc", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	svcTier4 := models.ModuleService{ServiceName: "stage1.v1.AmbiguousSvc", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	for _, s := range []*models.ModuleService{&svcTier1, &svcTier2, &svcTier3, &svcTier4} {
		if err := db.Create(s).Error; err != nil {
			t.Fatalf("create module service %s: %v", s.ServiceName, err)
		}
	}

	now := time.Now().UTC()
	placements := []models.ServicePlacement{
		// svcTier1 在 procTier1 与 procMulti2 上均有放置（多宿主，靠会话 CSGSnapshots Tier 1 消歧）
		{ModuleServiceID: svcTier1.ID, ProcessID: procTier1.ID, Endpoints: datatypes.JSON(`["tcp://10.1.0.1:21001"]`), ObservedTransport: models.ObservedTransportTCP, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now},
		{ModuleServiceID: svcTier1.ID, ProcessID: procMulti2.ID, Endpoints: datatypes.JSON(`["tcp://10.2.0.1:21006"]`), ObservedTransport: models.ObservedTransportTCP, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now},
		// svcTier2 在 procTier2 与 procMulti1 上均有放置（多宿主，靠对端 DstAddr 匹配 ServicePlacement.endpoints Tier 2 消歧）
		{ModuleServiceID: svcTier2.ID, ProcessID: procTier2.ID, Endpoints: datatypes.JSON(`["tcp://10.2.0.1:21002"]`), ObservedTransport: models.ObservedTransportTCP, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now},
		{ModuleServiceID: svcTier2.ID, ProcessID: procMulti1.ID, Endpoints: datatypes.JSON(`["tcp://10.1.0.1:21005"]`), ObservedTransport: models.ObservedTransportTCP, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now},
		// svcTier3 在 SHARED 恰有 1 个 ACTIVE 放置（procTier3），另在 PRIVATE 有 1 个（procPriv，应被忽略）
		{ModuleServiceID: svcTier3.ID, ProcessID: procTier3.ID, Endpoints: datatypes.JSON(`["tcp://10.1.0.1:21003"]`), ObservedTransport: models.ObservedTransportTCP, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now},
		{ModuleServiceID: svcTier3.ID, ProcessID: procPriv.ID, Endpoints: datatypes.JSON(`["tcp://10.9.0.1:21007"]`), ObservedTransport: models.ObservedTransportTCP, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now},
		// svcTier4 在 SHARED 有 2 个不同 ArchProcess 放置，且无快照/端点命中 -> 必须标为 AMBIGUOUS 不猜绑
		{ModuleServiceID: svcTier4.ID, ProcessID: procMulti1.ID, Endpoints: datatypes.JSON(`["tcp://10.1.0.1:21005"]`), ObservedTransport: models.ObservedTransportTCP, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now},
		{ModuleServiceID: svcTier4.ID, ProcessID: procMulti2.ID, Endpoints: datatypes.JSON(`["tcp://10.2.0.1:21006"]`), ObservedTransport: models.ObservedTransportTCP, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now},
	}
	for i := range placements {
		if err := db.Create(&placements[i]).Error; err != nil {
			t.Fatalf("create placement: %v", err)
		}
	}

	// 上传并激活 stage1.v1.SnapSvc .pb 契约以验证 Stage 1 同步解码
	decReg, err := decoder.NewRegistry(db)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	descBytes := buildStage1DescriptorBytes(t, "stage1.v1", "SnapSvc", "CreateOrder")
	if _, _, _, err := decReg.UploadSchema(decoder.UploadSchemaInput{
		Name:         "stage1_snap",
		Category:     models.ProtoCategoryBusiness,
		VersionTag:   "v1.0.0",
		FileName:     "stage1.pb",
		RawBytes:     descBytes,
		AutoActivate: true,
	}); err != nil {
		t.Fatalf("UploadSchema: %v", err)
	}
	_ = decReg.Reload()

	// 构造真实 protobuf 请求帧字节
	var fds descriptorpb.FileDescriptorSet
	_ = proto.Unmarshal(descBytes, &fds)
	files, _ := protodesc.NewFiles(&fds)
	reqDesc, _ := files.FindDescriptorByName("stage1.v1.SnapSvcReq")
	reqMD := reqDesc.(protoreflect.MessageDescriptor)
	reqMsg := dynamicpb.NewMessage(reqMD)
	reqMsg.Set(reqMD.Fields().ByName("order_id"), protoreflect.ValueOfUint64(888123))
	pbBytes, _ := proto.Marshal(reqMsg)
	rawReq := buildTestReqFrame(t, "SnapSvc-SnapSvc", "CreateOrder", pbBytes)

	// 创建录制会话（携带 Tier 1 CSGSnapshots）
	csgSnaps, _ := json.Marshal([]agentproto.CSGSnapshotRecord{
		{
			ServiceName: "SnapSvc-SnapSvc",
			Addr:        "tcp://ipv4:10.1.0.1:21001",
			Transport:   "TCP",
			HostIP:      "10.1.0.1",
			Port:        21001,
			Source:      "QUICK_RECORD",
		},
	})
	sess := models.RecordingSession{
		SessionNo:        "S1_TEST_01",
		Name:             "Stage1Test",
		Scope:            models.ScopeShared,
		OwnerID:          1,
		Status:           models.SessionStatusRecording,
		LeasedAgentIDs:   datatypes.JSON("[]"),
		TargetProcessIDs: datatypes.JSON("[]"),
		CSGSnapshots:     datatypes.JSON(csgSnaps),
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}

	// 创建流量分区并写入 5 条测试记录
	pm := &models.PartitionManager{}
	if _, err := pm.EnsureDailyPartition(db, now); err != nil {
		t.Fatalf("EnsureDailyPartition: %v", err)
	}
	recBase := func(seq int64, procID uint64, dir, svc, op, srcAddr, dstAddr string, raw []byte) models.TrafficRecord {
		return models.TrafficRecord{
			CreatedAt:       now,
			SessionID:       sess.ID,
			SeqNo:           seq,
			AgentID:         agent1.ID,
			ProcessID:       procID,
			CapturedPID:     1000 + uint32(seq),
			ConnKey:         "conn-s1",
			Direction:       dir,
			ExchangeType:    models.ExchangeUnary,
			SrcAddr:         srcAddr,
			DstAddr:         dstAddr,
			ReqCapturedAtNs: now.UnixNano() + seq,
			TransportProto:  crosrpc.TransportTCP,
			SNO:             models.U64Numeric(100 + seq),
			ServiceName:     svc,
			Operation:       op,
			RawReqPacket:    raw,
			DecodeStatus:    decoder.DecodeStatusNoSchemaRawHex,
			IsReplayable:    true,
		}
	}

	recs := []models.TrafficRecord{
		recBase(1, procCaller.ID, models.TrafficDirEgress, "SnapSvc-SnapSvc", "CreateOrder", "10.1.0.1:50001", "10.1.0.1:21001", rawReq),
		recBase(2, procCaller.ID, models.TrafficDirEgress, "stage1.v1.EndpointSvc", "Ping", "10.1.0.1:50002", "10.2.0.1:21002", nil),
		recBase(3, procCaller.ID, models.TrafficDirEgress, "stage1.v1.SharedUniqueSvc", "Check", "10.1.0.1:50003", "10.88.88.88:9999", nil),
		recBase(4, procCaller.ID, models.TrafficDirEgress, "stage1.v1.AmbiguousSvc", "Do", "10.1.0.1:50004", "10.99.99.99:9999", nil),
		recBase(5, procTier1.ID, models.TrafficDirIngress, "SnapSvc-SnapSvc", "CreateOrder", "10.1.0.1:50001", "10.1.0.1:21001", nil),
	}
	for i := range recs {
		if err := db.Create(&recs[i]).Error; err != nil {
			t.Fatalf("create traffic record %d: %v", i+1, err)
		}
	}

	// 执行 Stage 1 + 解码
	if err := OfflineBatchAttributionWithDecoder(context.Background(), db, sess.ID, decReg); err != nil {
		t.Fatalf("OfflineBatchAttributionWithDecoder failed: %v", err)
	}

	var out []models.TrafficRecord
	if err := db.Where("session_id = ?", sess.ID).Order("seq_no ASC").Find(&out).Error; err != nil {
		t.Fatalf("reload records failed: %v", err)
	}
	if len(out) != 5 {
		t.Fatalf("expected 5 records, got %d", len(out))
	}

	// Record 1: Tier 1 (CSGSnapshots) + .pb 解码成功
	if out[0].SrcArchProcessID == nil || *out[0].SrcArchProcessID != apCaller.ID {
		t.Fatalf("rec1 SrcArchProcessID want %d, got %v", apCaller.ID, out[0].SrcArchProcessID)
	}
	if out[0].DstArchProcessID == nil || *out[0].DstArchProcessID != apTier1.ID {
		t.Fatalf("rec1 DstArchProcessID (Tier 1) want %d, got %v", apTier1.ID, out[0].DstArchProcessID)
	}
	if out[0].DecodeStatus != decoder.DecodeStatusDecodedJSON && out[0].DecodeStatus != decoder.DecodeStatusSuccess {
		t.Fatalf("rec1 expected DecodeStatus=%s, got %s", decoder.DecodeStatusDecodedJSON, out[0].DecodeStatus)
	}

	// Record 2: Tier 2 (ServicePlacement.endpoints)
	if out[1].DstArchProcessID == nil || *out[1].DstArchProcessID != apTier2.ID {
		t.Fatalf("rec2 DstArchProcessID (Tier 2) want %d, got %v", apTier2.ID, out[1].DstArchProcessID)
	}

	// Record 3: Tier 3 (权威 SHARED 唯一放置，忽略 PRIVATE)
	if out[2].DstArchProcessID == nil || *out[2].DstArchProcessID != apTier3.ID {
		t.Fatalf("rec3 DstArchProcessID (Tier 3) want %d, got %v", apTier3.ID, out[2].DstArchProcessID)
	}

	// Record 4: Tier 4 (多宿主无唯一匹配 -> 必须为 nil 且写入 AMBIGUOUS 候选)
	if out[3].DstArchProcessID != nil {
		t.Fatalf("rec4 DstArchProcessID must be nil when AMBIGUOUS, got %d", *out[3].DstArchProcessID)
	}
	var hdrMap map[string]interface{}
	_ = json.Unmarshal(out[3].HeadersJSON, &hdrMap)
	metaMap, _ := hdrMap["meta"].(map[string]interface{})
	if metaMap["peer_attribution_mode"] != "AMBIGUOUS" {
		t.Fatalf("rec4 expected peer_attribution_mode=AMBIGUOUS, got %+v", hdrMap)
	}
	cands, _ := metaMap["peer_candidate_arch_process_ids"].([]interface{})
	if len(cands) != 2 {
		t.Fatalf("rec4 expected 2 candidate arch_process_ids, got %+v", cands)
	}

	// Record 5: INGRESS 本地端写入 DstArchProcessID
	if out[4].DstArchProcessID == nil || *out[4].DstArchProcessID != apTier1.ID {
		t.Fatalf("rec5 INGRESS DstArchProcessID want %d, got %v", apTier1.ID, out[4].DstArchProcessID)
	}
}
