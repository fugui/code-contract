package models

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"code-common/backend/testdb"
	"code-contract/config"
	"gorm.io/datatypes"
)

func TestM30bSchema_CheckI1_UnclaimedInvariants_AndPlacementDerivation(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ServiceName)

	// 1. 两次 SetupWithDB 验证幂等
	if err := SetupWithDB(db); err != nil {
		t.Fatalf("首次 SetupWithDB 失败: %v", err)
	}
	if err := SetupWithDB(db); err != nil {
		t.Fatalf("二次幂等 SetupWithDB 失败: %v", err)
	}

	// 2. 验证 39 张核心业务表 + contract_settings = 40 表全部注册且存在
	bizTables := BusinessTableNames()
	if len(bizTables) != 39 {
		t.Fatalf("BusinessTableNames 期望 39 张核心业务表，实际为 %d: %v", len(bizTables), bizTables)
	}

	allModels := []tabler{
		Subsystem{}, FunctionalGroup{}, ArchProcessGroup{}, ArchProcess{},
		Module{}, ServiceContract{}, ModuleService{}, ModuleDependency{},
		Agent{}, Process{}, ProtoSchema{}, ProtoOperation{},
		RecordingSession{}, TrafficRecord{}, ReplayTask{}, ReplayItem{},
		GoldenSuite{}, GoldenSuiteItem{}, TopologyLayout{},
		GitRepo{}, GitProtoBundle{}, ProtoFile{}, ProtoFileVersion{}, ProtoDocSymbol{}, ProtoImportOwner{},
		ProtoSyncJob{}, ProtoGovernanceConfig{},
		CheckRule{}, VocabularyTerm{}, CheckReport{}, CheckViolation{}, MRCheckRun{}, Notification{},
		DiscoverySnapshot{}, ServicePlacement{}, ProcessDependency{}, DependencyDecision{}, AttributionJob{}, MigBackup{},
		ModuleSetting{},
	}
	for _, m := range allModels {
		tbl := m.TableName()
		if !strings.HasPrefix(tbl, config.TablePrefix) {
			t.Fatalf("模型 %s 表名 %q 缺少前缀 %q", reflect.TypeOf(m).Name(), tbl, config.TablePrefix)
		}
		if !db.Migrator().HasTable(tbl) {
			t.Fatalf("数据库中缺少表 %s", tbl)
		}
	}

	// 3. 验证增量列存在，且 contract_module_services 严禁新增 arch_process_id 列
	if db.Migrator().HasColumn(&ModuleService{}, "arch_process_id") {
		t.Fatalf("红线违例：contract_module_services 严禁新增 arch_process_id 列")
	}
	for _, col := range []string{"declared_role", "identity_fingerprint", "fingerprint_version", "lifecycle", "origin", "origin_agent_id"} {
		if !db.Migrator().HasColumn(&ArchProcess{}, col) {
			t.Fatalf("contract_arch_processes 缺少列 %s", col)
		}
	}
	for _, col := range []string{"enroll_source", "observed_role", "observed_transports", "observed_pids", "last_observed_at"} {
		if !db.Migrator().HasColumn(&Process{}, col) {
			t.Fatalf("contract_processes 缺少列 %s", col)
		}
	}
	for _, col := range []string{"source", "proto_match_confidence", "proto_suggested_name", "claimed_at", "claimed_by"} {
		if !db.Migrator().HasColumn(&ModuleService{}, col) {
			t.Fatalf("contract_module_services 缺少列 %s", col)
		}
	}
	for _, col := range []string{"src_arch_process_id", "dst_arch_process_id"} {
		if !db.Migrator().HasColumn(&TrafficRecord{}, col) {
			t.Fatalf("contract_traffic_records 缺少列 %s", col)
		}
	}

	// 验证 uk_contract_mod_svc_name_excl 保持存在
	var idxCnt int64
	if err := db.Raw(`SELECT count(*) FROM pg_indexes WHERE tablename = 'contract_module_services' AND indexname = 'uk_contract_mod_svc_name_excl'`).Scan(&idxCnt).Error; err != nil {
		t.Fatalf("查询索引失败: %v", err)
	}
	if idxCnt != 1 {
		t.Fatalf("期望保留 uk_contract_mod_svc_name_excl，实际 count=%d", idxCnt)
	}

	// 4. 构造基础实体以校验 CHECK I1、FK RESTRICT 与 ServicePlacement 派生一致性
	sub := Subsystem{Code: "SUB_M30B", Name: "M30b子系统"}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatalf("创建 Subsystem 失败: %v", err)
	}
	fg := FunctionalGroup{SubsystemID: sub.ID, Code: "FG_M30B", Name: "M30b功能组", Level: 1, Path: "/1/"}
	if err := db.Create(&fg).Error; err != nil {
		t.Fatalf("创建 FunctionalGroup 失败: %v", err)
	}
	pgrp := ArchProcessGroup{Code: "PGRP_M30B", Name: "M30b进程组"}
	if err := db.Create(&pgrp).Error; err != nil {
		t.Fatalf("创建 ArchProcessGroup 失败: %v", err)
	}
	ap := ArchProcess{GroupID: pgrp.ID, Code: "ap-m30b", Name: "ap-m30b"}
	if err := db.Create(&ap).Error; err != nil {
		t.Fatalf("创建 ArchProcess 失败: %v", err)
	}
	mod := Module{SubsystemID: sub.ID, GroupID: fg.ID, Code: "MOD_M30B", Name: "M30b模块", ModuleKind: ModuleKindBusiness}
	if err := db.Create(&mod).Error; err != nil {
		t.Fatalf("创建 Module 失败: %v", err)
	}
	defMod := Module{SubsystemID: sub.ID, GroupID: fg.ID, Code: "LIB_M30B", Name: "M30b契约库", ModuleKind: ModuleKindContractLib}
	if err := db.Create(&defMod).Error; err != nil {
		t.Fatalf("创建契约库 Module 失败: %v", err)
	}
	sc := ServiceContract{ServiceName: "m30b.v1.MultiSvc", DefinerModuleID: defMod.ID, ProvideMode: ContractProvideModeMultiImpl}
	if err := db.Create(&sc).Error; err != nil {
		t.Fatalf("创建 ServiceContract 失败: %v", err)
	}
	agent := Agent{Name: "agent-m30b", Endpoint: "192.168.56.101:19001", Scope: ScopeShared, Status: AgentStatusOnline}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("创建 Agent 失败: %v", err)
	}
	proc := Process{
		AgentID:             agent.ID,
		ArchProcessID:       ap.ID,
		EffectiveListenPort: 20001,
		ExtraDownstreams:    datatypes.JSON("[]"),
		EnrollSource:        EnrollSourceDiscovery,
		ProbeDesiredState:   ProbeDesiredStateDisarmed,
		ProbeStatus:         ProbeStatusDisarmed,
	}
	if err := db.Create(&proc).Error; err != nil {
		t.Fatalf("创建 Process 失败: %v", err)
	}

	// 4a. CHECK I1: 未认领 (module_id IS NULL) + source='MANUAL' 必须被 DB 拒绝
	if err := db.Exec(`INSERT INTO contract_module_services (module_id, service_name, source, diff_ignore_paths, created_by, created_at, updated_at) VALUES (NULL, 'svc.bad.manual', 'MANUAL', '[]', 0, now(), now())`).Error; err == nil {
		t.Fatalf("未认领服务 source=MANUAL 应当触发 CHECK I1 违例报错")
	}
	// 4b. CHECK I1: 未认领 + contract_id IS NOT NULL 必须被 DB 拒绝
	if err := db.Exec(`INSERT INTO contract_module_services (module_id, contract_id, impl_key, service_name, source, diff_ignore_paths, created_by, created_at, updated_at) VALUES (NULL, ?, 'k1', 'svc.bad.contract', 'CSG_DISCOVERED', '[]', 0, now(), now())`, sc.ID).Error; err == nil {
		t.Fatalf("未认领服务携带 contract_id 应当触发 CHECK I1 违例报错")
	}
	// 4c. CHECK I1: 未认领 + impl_key <> '' 必须被 DB 拒绝
	if err := db.Exec(`INSERT INTO contract_module_services (module_id, impl_key, service_name, source, diff_ignore_paths, created_by, created_at, updated_at) VALUES (NULL, 'k1', 'svc.bad.impl', 'CSG_DISCOVERED', '[]', 0, now(), now())`).Error; err == nil {
		t.Fatalf("未认领服务携带 impl_key 应当触发 CHECK I1 违例报错")
	}

	// 4d. 合法未认领服务写入成功
	unclaimed := ModuleService{
		ModuleID:        nil,
		ServiceName:     "svc.good.discovered",
		Source:          ServiceSourceCSGDiscovered,
		DiffIgnorePaths: datatypes.JSON("[]"),
	}
	if err := db.Create(&unclaimed).Error; err != nil {
		t.Fatalf("合法未认领服务写入失败: %v", err)
	}

	// 4e. FK RESTRICT 阻断裸删 Module
	claimed := ModuleService{
		ModuleID:        Uint64Ptr(mod.ID),
		ServiceName:     "svc.claimed.test",
		Source:          ServiceSourceManual,
		DiffIgnorePaths: datatypes.JSON("[]"),
	}
	if err := db.Create(&claimed).Error; err != nil {
		t.Fatalf("创建已认领服务失败: %v", err)
	}
	if err := db.Exec(`DELETE FROM contract_modules WHERE id = ?`, mod.ID).Error; err == nil {
		t.Fatalf("存在关联 ModuleService 时裸删 contract_modules 应当被 FK RESTRICT 阻断")
	}

	// 4f. ServicePlacement 冗余列强制派生一致性校验
	now := time.Now().UTC()
	sp := ServicePlacement{
		ModuleServiceID:   unclaimed.ID,
		ProcessID:         proc.ID,
		AgentID:           999999, // 故意传入错误值验证 BeforeSave 强制派生
		ArchProcessID:     888888, // 故意传入错误值验证 BeforeSave 强制派生
		RawServiceName:    "svc.good.discovered-1234",
		Endpoints:         datatypes.JSON(`["tcp://192.168.56.101:20001"]`),
		ObservedTransport: ObservedTransportTCP,
		InstanceCount:     1,
		Status:            PlacementStatusActive,
		FirstSeenAt:       now,
		LastSeenAt:        now,
	}
	if err := db.Create(&sp).Error; err != nil {
		t.Fatalf("创建 ServicePlacement 失败: %v", err)
	}
	var reloadedSP ServicePlacement
	if err := db.First(&reloadedSP, sp.ID).Error; err != nil {
		t.Fatalf("回查 ServicePlacement 失败: %v", err)
	}
	if reloadedSP.AgentID != agent.ID || reloadedSP.ArchProcessID != ap.ID {
		t.Fatalf("ServicePlacement 冗余列未从 Process 派生: got (agent=%d, ap=%d), want (agent=%d, ap=%d)",
			reloadedSP.AgentID, reloadedSP.ArchProcessID, agent.ID, ap.ID)
	}

	// 5. 验证 10 项 discovery.* 默认配置全部落库且 discovery.v2_enabled = "false"
	expectedSettings := map[string]string{
		"discovery.v2_enabled":                "false",
		"discovery.enroll_batch_limit_shared": "50",
		"discovery.reconcile_interval_sec":    "600",
		"discovery.stale_miss_threshold":      "3",
		"discovery.stale_hours":               "24",
		"discovery.retire_days":               "7",
		"discovery.gc_days":                   "30",
		"discovery.snapshot_retention_count":  "50",
		"discovery.snapshot_pin_limit":        "20",
		"discovery.auto_downstreams":          "false",
	}
	for k, wantVal := range expectedSettings {
		var s ModuleSetting
		if err := db.Where("setting_key = ?", k).First(&s).Error; err != nil {
			t.Fatalf("缺少默认配置项 %s: %v", k, err)
		}
		if s.SettingVal != wantVal {
			t.Fatalf("配置项 %s 默认值期望 %q，实际 %q", k, wantVal, s.SettingVal)
		}
	}
}
