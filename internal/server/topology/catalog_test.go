package topology

import (
	"context"
	"testing"
	"time"

	"code-common/backend/testdb"
	"code-contract/config"
	"code-contract/models"
	"gorm.io/datatypes"
)

func TestCatalogCorrelate_ExactAuto_HeuristicAndFingerprintSuggestOnly(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB failed: %v", err)
	}

	// 1. 构造激活态 ProtoSchema 与 ProtoOperation 字典
	schema := models.ProtoSchema{
		Name:            "catalog_correlate",
		Version:         1,
		Category:        models.ProtoCategoryBusiness,
		FileName:        "catalog.pb",
		FileSize:        128,
		DescriptorBytes: []byte{0x0a, 0x00},
		IsActive:        true,
		ServiceCount:    5,
	}
	if err := db.Create(&schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}

	ops := []models.ProtoOperation{
		// EXACT 命中
		{SchemaID: schema.ID, ServiceName: "com.acme.ExactSvc", ProtoPackage: "com.acme", ProtoService: "ExactSvc", Operation: "Get", IsActive: true},
		// HEURISTIC 唯一命中: <Class>-<Svr> => OrderServiceImpl-OrderService -> com.acme.OrderService
		{SchemaID: schema.ID, ServiceName: "com.acme.OrderService", ProtoPackage: "com.acme", ProtoService: "OrderService", Operation: "Create", IsActive: true},
		// HEURISTIC 歧义: 两个包都有 DupSvc
		{SchemaID: schema.ID, ServiceName: "com.a.DupSvc", ProtoPackage: "com.a", ProtoService: "DupSvc", Operation: "CallA", IsActive: true},
		{SchemaID: schema.ID, ServiceName: "com.b.DupSvc", ProtoPackage: "com.b", ProtoService: "DupSvc", Operation: "CallB", IsActive: true},
		// FINGERPRINT 命中 (5 个方法，录制命中 >= 4 个即 >= 80%)
		{SchemaID: schema.ID, ServiceName: "com.acme.PaymentService", ProtoPackage: "com.acme", ProtoService: "PaymentService", Operation: "Pay", IsActive: true},
		{SchemaID: schema.ID, ServiceName: "com.acme.PaymentService", ProtoPackage: "com.acme", ProtoService: "PaymentService", Operation: "Refund", IsActive: true},
		{SchemaID: schema.ID, ServiceName: "com.acme.PaymentService", ProtoPackage: "com.acme", ProtoService: "PaymentService", Operation: "Query", IsActive: true},
		{SchemaID: schema.ID, ServiceName: "com.acme.PaymentService", ProtoPackage: "com.acme", ProtoService: "PaymentService", Operation: "Cancel", IsActive: true},
		{SchemaID: schema.ID, ServiceName: "com.acme.PaymentService", ProtoPackage: "com.acme", ProtoService: "PaymentService", Operation: "Settle", IsActive: true},
		// FINGERPRINT 不足 80% (5 个方法，仅录制 2 个 = 40%)
		{SchemaID: schema.ID, ServiceName: "com.acme.InventoryService", ProtoPackage: "com.acme", ProtoService: "InventoryService", Operation: "Reserve", IsActive: true},
		{SchemaID: schema.ID, ServiceName: "com.acme.InventoryService", ProtoPackage: "com.acme", ProtoService: "InventoryService", Operation: "Release", IsActive: true},
		{SchemaID: schema.ID, ServiceName: "com.acme.InventoryService", ProtoPackage: "com.acme", ProtoService: "InventoryService", Operation: "Commit", IsActive: true},
		{SchemaID: schema.ID, ServiceName: "com.acme.InventoryService", ProtoPackage: "com.acme", ProtoService: "InventoryService", Operation: "Check", IsActive: true},
		{SchemaID: schema.ID, ServiceName: "com.acme.InventoryService", ProtoPackage: "com.acme", ProtoService: "InventoryService", Operation: "List", IsActive: true},
	}
	for i := range ops {
		if err := db.Create(&ops[i]).Error; err != nil {
			t.Fatalf("create proto op: %v", err)
		}
	}

	// 2. 创建 5 个待撮合的未认领 ModuleService
	svcExact := models.ModuleService{ServiceName: "com.acme.ExactSvc", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	svcHeur := models.ModuleService{ServiceName: "OrderServiceImpl-OrderService", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	svcAmb := models.ModuleService{ServiceName: "DupImpl-DupSvc", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	svcFPPass := models.ModuleService{ServiceName: "PayRuntime64B", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	svcFPFail := models.ModuleService{ServiceName: "InvRuntime64B", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	for _, s := range []*models.ModuleService{&svcExact, &svcHeur, &svcAmb, &svcFPPass, &svcFPFail} {
		if err := db.Create(s).Error; err != nil {
			t.Fatalf("create module service %s: %v", s.ServiceName, err)
		}
	}

	// 3. 为 PayRuntime64B 与 InvRuntime64B 注入录制流量操作集合
	now := time.Now().UTC()
	pm := &models.PartitionManager{}
	if _, err := pm.EnsureDailyPartition(db, now); err != nil {
		t.Fatalf("EnsureDailyPartition: %v", err)
	}
	sess := models.RecordingSession{
		SessionNo: "CORR_01", Name: "CorrelateTest", Scope: models.ScopeShared, OwnerID: 1,
		Status: models.SessionStatusCompleted, LeasedAgentIDs: datatypes.JSON("[]"), TargetProcessIDs: datatypes.JSON("[]"),
	}
	_ = db.Create(&sess).Error

	insertTrafficOp := func(seq int64, svcName, opName string) {
		tr := models.TrafficRecord{
			CreatedAt: now, SessionID: sess.ID, SeqNo: seq, AgentID: 1, ProcessID: 1, CapturedPID: 100,
			ConnKey: "c1", Direction: models.TrafficDirIngress, ExchangeType: models.ExchangeUnary,
			SrcAddr: "127.0.0.1:1000", DstAddr: "127.0.0.1:2000", ReqCapturedAtNs: now.UnixNano() + seq,
			ServiceName: svcName, Operation: opName, IsReplayable: true,
		}
		if err := db.Create(&tr).Error; err != nil {
			t.Fatalf("insert traffic record: %v", err)
		}
	}
	insertTrafficOp(1, "PayRuntime64B", "Pay")
	insertTrafficOp(2, "PayRuntime64B", "Refund")
	insertTrafficOp(3, "PayRuntime64B", "Query")
	insertTrafficOp(4, "PayRuntime64B", "Cancel") // 4/5 = 80% -> 命中 FINGERPRINT
	insertTrafficOp(5, "InvRuntime64B", "Reserve")
	insertTrafficOp(6, "InvRuntime64B", "Release") // 2/5 = 40% -> 不命中

	// 4. 执行三级撮合
	corrRes, err := CorrelateCatalogServices(context.Background(), db, nil)
	if err != nil {
		t.Fatalf("CorrelateCatalogServices failed: %v", err)
	}
	if corrRes.ExactMatched != 1 || corrRes.SuggestedCount != 2 {
		t.Fatalf("unexpected correlate counts: %+v", corrRes)
	}

	// 验证落库策略：仅 EXACT 自动写 proto_service_name，HEURISTIC/FINGERPRINT 仅写 proto_suggested_name
	reload := func(id uint64) models.ModuleService {
		var m models.ModuleService
		_ = db.First(&m, id).Error
		return m
	}

	gotExact := reload(svcExact.ID)
	if gotExact.ProtoServiceName != "com.acme.ExactSvc" || gotExact.ProtoMatchConfidence != models.ProtoMatchConfExact {
		t.Fatalf("EXACT mismatch: %+v", gotExact)
	}

	gotHeur := reload(svcHeur.ID)
	if gotHeur.ProtoServiceName != "" || gotHeur.ProtoSuggestedName != "com.acme.OrderService" || gotHeur.ProtoMatchConfidence != models.ProtoMatchConfHeuristic {
		t.Fatalf("HEURISTIC must only write proto_suggested_name, got: %+v", gotHeur)
	}

	gotAmb := reload(svcAmb.ID)
	if gotAmb.ProtoServiceName != "" || gotAmb.ProtoSuggestedName != "" {
		t.Fatalf("ambiguous heuristic must not suggest, got: %+v", gotAmb)
	}

	gotFPPass := reload(svcFPPass.ID)
	if gotFPPass.ProtoServiceName != "" || gotFPPass.ProtoSuggestedName != "com.acme.PaymentService" || gotFPPass.ProtoMatchConfidence != models.ProtoMatchConfFingerprint {
		t.Fatalf("FINGERPRINT >= 80%% must only write proto_suggested_name, got: %+v", gotFPPass)
	}

	gotFPFail := reload(svcFPFail.ID)
	if gotFPFail.ProtoServiceName != "" || gotFPFail.ProtoSuggestedName != "" {
		t.Fatalf("FINGERPRINT < 80%% must stay unmatched, got: %+v", gotFPFail)
	}
}

func TestFiveStateRadar_AuthoritySharedOnly_ExcludesPrivate(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB failed: %v", err)
	}

	privOwner := uint(88)
	sharedAgent := models.Agent{Name: "radar-shared", Endpoint: "http://10.30.1.1:19001", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	privAgent := models.Agent{Name: "radar-priv", Endpoint: "http://10.30.2.1:19001", Scope: models.ScopePrivate, OwnerID: &privOwner, Status: models.AgentStatusOnline}
	_ = db.Create(&sharedAgent).Error
	_ = db.Create(&privAgent).Error

	sub := models.Subsystem{Code: "SUB_R", Name: "雷达子系统"}
	_ = db.Create(&sub).Error
	fg := models.FunctionalGroup{SubsystemID: sub.ID, Code: "FG_R", Name: "雷达组", Level: 1, Path: "/1/"}
	_ = db.Create(&fg).Error
	pgrp := models.ArchProcessGroup{Code: "PGRP_R", Name: "雷达进程组"}
	_ = db.Create(&pgrp).Error

	ap1 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-r1", Name: "R1", Lifecycle: models.ArchProcLifecycleConfirmed}
	ap2 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-r2", Name: "R2", Lifecycle: models.ArchProcLifecycleConfirmed}
	apProv := models.ArchProcess{GroupID: pgrp.ID, Code: "prv-r", Name: "PrivR", Lifecycle: models.ArchProcLifecycleProvisional, OriginAgentID: &privAgent.ID}
	_ = db.Create(&ap1).Error
	_ = db.Create(&ap2).Error
	_ = db.Create(&apProv).Error

	mod1 := models.Module{SubsystemID: sub.ID, GroupID: fg.ID, Code: "MOD_R1", Name: "模块R1", ArchProcessID: &ap1.ID}
	_ = db.Create(&mod1).Error

	proc1 := models.Process{AgentID: sharedAgent.ID, ArchProcessID: ap1.ID, EffectiveListenPort: 24001, ExtraDownstreams: datatypes.JSON("[]")}
	proc2 := models.Process{AgentID: sharedAgent.ID, ArchProcessID: ap2.ID, EffectiveListenPort: 24002, ExtraDownstreams: datatypes.JSON("[]")}
	procPriv := models.Process{AgentID: privAgent.ID, ArchProcessID: apProv.ID, EffectiveListenPort: 24003, ExtraDownstreams: datatypes.JSON("[]")}
	_ = db.Create(&proc1).Error
	_ = db.Create(&proc2).Error
	_ = db.Create(&procPriv).Error

	// 激活 .pb：包含 UnclaimedOrphan 对应服务 与 PhantomProto 纸面僵尸服务
	schema := models.ProtoSchema{Name: "radar_pb", Version: 1, Category: models.ProtoCategoryBusiness, FileName: "radar.pb", FileSize: 128, DescriptorBytes: []byte{0x0a, 0x00}, IsActive: true, ServiceCount: 3}
	if err := db.Create(&schema).Error; err != nil {
		t.Fatalf("create radar schema: %v", err)
	}
	_ = db.Create(&models.ProtoOperation{SchemaID: schema.ID, ServiceName: "radar.v1.OrphanProtoSvc", ProtoPackage: "radar.v1", ProtoService: "OrphanProtoSvc", Operation: "Ping", IsActive: true}).Error
	_ = db.Create(&models.ProtoOperation{SchemaID: schema.ID, ServiceName: "radar.v1.PhantomSvc", ProtoPackage: "radar.v1", ProtoService: "PhantomSvc", Operation: "Ghost", IsActive: true}).Error
	_ = db.Create(&models.ProtoOperation{SchemaID: schema.ID, ServiceName: "radar.v1.PrivOnlySubscribed", ProtoPackage: "radar.v1", ProtoService: "PrivOnlySubscribed", Operation: "Call", IsActive: true}).Error

	now := time.Now().UTC()

	// 1. SHADOW_LIVE: 权威 SHARED 有 ACTIVE 放置，无 proto 映射
	svcShadow := models.ModuleService{ServiceName: "radar.v1.ShadowLive", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	// 2. UNCLAIMED_ORPHAN: 权威 SHARED 有 ACTIVE 放置 + 已确认 proto 映射，但 module_id IS NULL
	svcOrphan := models.ModuleService{ServiceName: "radar.v1.OrphanProtoSvc", ProtoServiceName: "radar.v1.OrphanProtoSvc", ProtoMatchConfidence: models.ProtoMatchConfExact, Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	// 4. VANISHED_CLAIMED: 已认领，但权威 SHARED 放置全部 RETIRED
	svcVanished := models.ModuleService{ModuleID: models.Uint64Ptr(mod1.ID), ServiceName: "radar.v1.VanishedClaimed", ProtoServiceName: "radar.v1.OrphanProtoSvc", Source: models.ServiceSourceManual, DiffIgnorePaths: datatypes.JSON("[]")}
	// 5. PLACEMENT_DRIFT: 已认领进 mod1 (ap1)，但在权威 SHARED 出现 ap2 的 ACTIVE 放置
	svcDrift := models.ModuleService{ModuleID: models.Uint64Ptr(mod1.ID), ServiceName: "radar.v1.PlacementDrift", ProtoServiceName: "radar.v1.OrphanProtoSvc", Source: models.ServiceSourceManual, DiffIgnorePaths: datatypes.JSON("[]")}
	// 噪音: 仅在 PRIVATE 环境放置的未认领服务（绝不能进入五态雷达计数，且对他人不可见）
	svcPrivNoise := models.ModuleService{ServiceName: "radar.v1.PrivOnlySubscribed", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}

	for _, s := range []*models.ModuleService{&svcShadow, &svcOrphan, &svcVanished, &svcDrift, &svcPrivNoise} {
		_ = db.Create(s).Error
	}

	_ = db.Create(&models.ServicePlacement{ModuleServiceID: svcShadow.ID, ProcessID: proc1.ID, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now}).Error
	_ = db.Create(&models.ServicePlacement{ModuleServiceID: svcOrphan.ID, ProcessID: proc1.ID, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now}).Error
	_ = db.Create(&models.ServicePlacement{ModuleServiceID: svcVanished.ID, ProcessID: proc1.ID, Status: models.PlacementStatusRetired, FirstSeenAt: now, LastSeenAt: now}).Error
	_ = db.Create(&models.ServicePlacement{ModuleServiceID: svcDrift.ID, ProcessID: proc2.ID, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now}).Error
	// PRIVATE 放置噪音（SHADOW_LIVE 噪音，且 PRIVATE 订阅了一个 PHANTOM 服务 PrivOnlySubscribed -> 该服务在权威环境仍应为 PHANTOM_PROTO）
	_ = db.Create(&models.ServicePlacement{ModuleServiceID: svcPrivNoise.ID, ProcessID: procPriv.ID, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now}).Error
	_ = db.Create(&models.ProcessDependency{ConsumerProcessID: procPriv.ID, ServiceName: "radar.v1.PrivOnlySubscribed", ProviderKind: models.ProviderKindLocalEnrolled, Status: models.PlacementStatusActive}).Error

	radar, err := ComputeFiveStateRadar(context.Background(), db)
	if err != nil {
		t.Fatalf("ComputeFiveStateRadar failed: %v", err)
	}
	if radar.ShadowLiveCount != 1 {
		t.Fatalf("ShadowLiveCount want 1 (excluding PRIVATE), got %d", radar.ShadowLiveCount)
	}
	if radar.UnclaimedOrphanCount != 1 {
		t.Fatalf("UnclaimedOrphanCount want 1, got %d", radar.UnclaimedOrphanCount)
	}
	// PhantomSvc + PrivOnlySubscribed (因仅在 PRIVATE 存在，在权威环境属于纸面僵尸) = 2
	if radar.PhantomProtoCount != 2 {
		t.Fatalf("PhantomProtoCount want 2 (PhantomSvc + PrivOnlySubscribed), got %d", radar.PhantomProtoCount)
	}
	if radar.VanishedClaimedCount != 1 {
		t.Fatalf("VanishedClaimedCount want 1, got %d", radar.VanishedClaimedCount)
	}
	if radar.PlacementDriftCount != 1 {
		t.Fatalf("PlacementDriftCount want 1, got %d", radar.PlacementDriftCount)
	}

	// 验证 V3 可见性：其他普通用户在目录列表中看不到纯 PRIVATE 的 svcPrivNoise
	listOther, err := ListUnifiedCatalogServices(context.Background(), db, CatalogServicesQuery{Page: 1, PageSize: 50}, 999, false)
	if err != nil {
		t.Fatalf("ListUnifiedCatalogServices failed: %v", err)
	}
	for _, it := range listOther.Items {
		if it.ServiceName == "radar.v1.PrivOnlySubscribed" && it.ID == svcPrivNoise.ID {
			t.Fatalf("pure PRIVATE service must not be visible to other users in catalog")
		}
	}
}
