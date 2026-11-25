package discovery

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"code-common/backend/testdb"
	"code-contract/config"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func enableDiscoveryV2(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Model(&models.ModuleSetting{}).
		Where("setting_key = ?", "discovery.v2_enabled").
		Update("setting_val", "true").Error; err != nil {
		t.Fatalf("开启 discovery.v2_enabled 失败: %v", err)
	}
}

func setupEnrollBaseDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB 失败: %v", err)
	}
	enableDiscoveryV2(t, db)
	ResetEnrollRateLimiter()
	return db
}

func TestEnroll_Idempotent_MultiHost1Identity2Placements_CrossEnvIsolated_AndMultiImplMatch(t *testing.T) {
	db := setupEnrollBaseDB(t)

	// 1. 构建两套 SHARED 环境 (Agent1 & Agent2)，以及 MultiImpl + Exclusive 既有服务
	agent1 := models.Agent{Name: "env-sit-1", Endpoint: "10.0.1.1:8090", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	agent2 := models.Agent{Name: "env-uat-2", Endpoint: "10.0.2.1:8090", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	if err := db.Create(&agent1).Error; err != nil {
		t.Fatalf("create agent1: %v", err)
	}
	if err := db.Create(&agent2).Error; err != nil {
		t.Fatalf("create agent2: %v", err)
	}

	sub := models.Subsystem{Code: "SUB_E", Name: "E子系统"}
	_ = db.Create(&sub).Error
	fg := models.FunctionalGroup{SubsystemID: sub.ID, Code: "FG_E", Name: "E功能组", Level: 1, Path: "/1/"}
	_ = db.Create(&fg).Error
	pgrp := models.ArchProcessGroup{Code: "PGRP_E", Name: "E进程组"}
	_ = db.Create(&pgrp).Error

	fpHostA, _, _ := ComputeIdentityFingerprint("/opt/bin/host_a", "/opt/bin/host_a --role=primary", "host_a")
	apHostA := models.ArchProcess{
		GroupID:             pgrp.ID,
		Code:                "host-a",
		Name:                "Host A",
		DefaultCmdPattern:   "host_a --role=primary",
		IdentityFingerprint: fpHostA,
		FingerprintVersion:  CurrentFingerprintVersion,
		Lifecycle:           models.ArchProcLifecycleConfirmed,
	}
	_ = db.Create(&apHostA).Error

	libMod := models.Module{SubsystemID: sub.ID, GroupID: fg.ID, Code: "LIB_E", Name: "契约库", ModuleKind: models.ModuleKindContractLib}
	_ = db.Create(&libMod).Error
	modA := models.Module{SubsystemID: sub.ID, GroupID: fg.ID, Code: "MOD_A", Name: "模块A", ArchProcessID: &apHostA.ID, ModuleKind: models.ModuleKindBusiness}
	_ = db.Create(&modA).Error
	sc := models.ServiceContract{ServiceName: "trade.v1.SharedPay", DefinerModuleID: libMod.ID, ProvideMode: models.ContractProvideModeMultiImpl}
	_ = db.Create(&sc).Error
	msMultiA := models.ModuleService{
		ModuleID:    models.Uint64Ptr(modA.ID),
		ContractID:  &sc.ID,
		ServiceName: "trade.v1.SharedPay",
		ImplKey:     "impl_a",
		Source:      models.ServiceSourceManual,
	}
	_ = db.Create(&msMultiA).Error

	// 独占已认领服务
	msExcl := models.ModuleService{
		ModuleID:    models.Uint64Ptr(modA.ID),
		ServiceName: "trade.v1.ClaimedExcl",
		Source:      models.ServiceSourceManual,
	}
	_ = db.Create(&msExcl).Error

	// Agent1 上：host_a 与 host_b 均发布同一服务 "common.v1.DualHostSvc"，host_a 还发布 SharedPay + ClaimedExcl 并订阅 DownstreamOff
	report1 := &agentproto.CSGTopologyReport{
		DiscoveryMode: "CROSTCP_RPCMON",
		Services: []agentproto.CSGServiceInfo{
			{ServiceName: "common.v1.DualHostSvc", Addr: "tcp://ipv4:10.0.1.1:21001", Transport: "TCP", ServerPID: 101, ServerProc: "host_a", IsLocal: true},
			{ServiceName: "trade.v1.SharedPay", Addr: "tcp://ipv4:10.0.1.1:21001", Transport: "TCP", ServerPID: 101, ServerProc: "host_a", IsLocal: true},
			{ServiceName: "trade.v1.ClaimedExcl", Addr: "tcp://ipv4:10.0.1.1:21001", Transport: "TCP", ServerPID: 101, ServerProc: "host_a", IsLocal: true},
			{ServiceName: "common.v1.DualHostSvc", Addr: "dophi_mesh://ipv4:10.0.1.1:21002", Transport: "MESH", ServerPID: 102, ServerProc: "host_b", IsLocal: true},
		},
		Subscriptions: []agentproto.CSGSubscription{
			{ClientPID: 101, ClientProc: "host_a", ServiceName: "common.v1.DualHostSvc", IsLocal: true},
			{ClientPID: 101, ClientProc: "host_a", ServiceName: "missing.v1.OfflineSvc", IsLocal: true},
		},
	}
	disc1 := []agentproto.DiscoveredProcess{
		{PID: 101, ListenPort: 21001, ExeName: "/opt/bin/host_a", Cmdline: "/opt/bin/host_a --role=primary", Transports: []string{"TCP"}},
		{PID: 102, ListenPort: 0, ExeName: "/opt/bin/host_b", Cmdline: "/opt/bin/host_b --role=secondary", Transports: []string{"MESH"}},
	}

	// Agent2 上：host_a 在不同路径发布同名服务（验证跨环境放置互不覆盖）
	report2 := &agentproto.CSGTopologyReport{
		DiscoveryMode: "CROSTCP_RPCMON",
		Services: []agentproto.CSGServiceInfo{
			{ServiceName: "common.v1.DualHostSvc", Addr: "tcp://ipv4:10.0.2.1:31001", Transport: "TCP", ServerPID: 201, ServerProc: "host_a", IsLocal: true},
		},
	}
	disc2 := []agentproto.DiscoveredProcess{
		{PID: 201, ListenPort: 31001, ExeName: "/srv/uat/host_a", Cmdline: "/srv/uat/host_a --role=primary --port=31001", Transports: []string{"TCP"}},
	}

	fetcher := &routeByAgentFetcher{
		reports: map[uint64]*agentproto.CSGTopologyReport{agent1.ID: report1, agent2.ID: report2},
		procs:   map[uint64][]agentproto.DiscoveredProcess{agent1.ID: disc1, agent2.ID: disc2},
	}
	liveSvc := NewLiveDiscoveryService(db, fetcher)

	live1, err := liveSvc.GetLiveDiscovery(context.Background(), agent1.ID, "", false, 1, true)
	if err != nil {
		t.Fatalf("live1 failed: %v", err)
	}
	items1 := make([]EnrollItemInput, 0, len(live1.Processes))
	for _, p := range live1.Processes {
		items1 = append(items1, EnrollItemInput{ProcessKey: p.ProcessKey})
	}

	res1, err := Enroll(context.Background(), db, liveSvc, EnrollInput{
		AgentID: agent1.ID, Items: items1, OperatorID: 1, IsAdmin: true,
	})
	if err != nil {
		t.Fatalf("首次 Enroll agent1 失败: %v", err)
	}
	if res1.PlacementsCount != 4 || res1.DependenciesCount != 2 {
		t.Fatalf("期望 4 条放置与 2 条依赖，实际: %+v", res1)
	}

	// 重复提交完全幂等
	res1Repeat, err := Enroll(context.Background(), db, liveSvc, EnrollInput{
		AgentID: agent1.ID, Items: items1, OperatorID: 1, IsAdmin: true, BypassRateLimit: true,
	})
	if err != nil {
		t.Fatalf("重复 Enroll agent1 失败: %v", err)
	}
	for _, e := range res1Repeat.Enrolled {
		if e.Result != "MATCHED" || e.ServicesCreated != 0 {
			t.Fatalf("重复 Enroll 应为 MATCHED 且 ServicesCreated=0，实际: %+v", e)
		}
	}

	// 校验 common.v1.DualHostSvc 仅 1 条身份行 + 2 条放置行
	var dualSvcs []models.ModuleService
	if err := db.Where("service_name = ?", "common.v1.DualHostSvc").Find(&dualSvcs).Error; err != nil {
		t.Fatalf("查询 DualHostSvc 失败: %v", err)
	}
	if len(dualSvcs) != 1 || dualSvcs[0].ModuleID != nil || dualSvcs[0].Source != models.ServiceSourceCSGDiscovered {
		t.Fatalf("同环境双宿主同名服务应仅建 1 条未认领身份，实际: %+v", dualSvcs)
	}
	var dualPlacements []models.ServicePlacement
	_ = db.Where("module_service_id = ?", dualSvcs[0].ID).Find(&dualPlacements).Error
	if len(dualPlacements) != 2 {
		t.Fatalf("同环境双宿主应有 2 条 ServicePlacement，实际 %d", len(dualPlacements))
	}

	// 校验 MULTI_IMPL 精确挂载至 msMultiA（没有额外新建未认领行）
	var multiSvcs []models.ModuleService
	_ = db.Where("service_name = ?", "trade.v1.SharedPay").Find(&multiSvcs).Error
	if len(multiSvcs) != 1 || multiSvcs[0].ID != msMultiA.ID {
		t.Fatalf("MULTI_IMPL 应匹配已有实现行 %d，实际: %+v", msMultiA.ID, multiSvcs)
	}
	var multiPlc models.ServicePlacement
	if err := db.Where("module_service_id = ? AND agent_id = ?", msMultiA.ID, agent1.ID).First(&multiPlc).Error; err != nil {
		t.Fatalf("MULTI_IMPL 放置未落库: %v", err)
	}

	// 校验独占已认领服务直接挂放置
	var exclPlc models.ServicePlacement
	if err := db.Where("module_service_id = ? AND agent_id = ?", msExcl.ID, agent1.ID).First(&exclPlc).Error; err != nil {
		t.Fatalf("独占已认领服务放置未落库: %v", err)
	}

	// 校验订阅边 provider_kind
	var deps []models.ProcessDependency
	_ = db.Where("agent_id = ?", agent1.ID).Order("service_name ASC").Find(&deps).Error
	if len(deps) != 2 {
		t.Fatalf("期望 2 条 ProcessDependency，实际 %d", len(deps))
	}
	if deps[0].ServiceName != "common.v1.DualHostSvc" || deps[0].ProviderKind != models.ProviderKindLocalEnrolled || deps[0].ProviderProcessID == nil {
		t.Fatalf("DualHostSvc 订阅边应解析为 LOCAL_ENROLLED，实际: %+v", deps[0])
	}
	if deps[1].ServiceName != "missing.v1.OfflineSvc" || deps[1].ProviderKind != models.ProviderKindOffline {
		t.Fatalf("OfflineSvc 订阅边应解析为 OFFLINE，实际: %+v", deps[1])
	}

	// Agent2 Enroll 跨环境互不覆盖
	live2, err := liveSvc.GetLiveDiscovery(context.Background(), agent2.ID, "", false, 1, true)
	if err != nil {
		t.Fatalf("live2 failed: %v", err)
	}
	if _, err := Enroll(context.Background(), db, liveSvc, EnrollInput{
		AgentID:    agent2.ID,
		Items:      []EnrollItemInput{{ProcessKey: live2.Processes[0].ProcessKey}},
		OperatorID: 1, IsAdmin: true, BypassRateLimit: true,
	}); err != nil {
		t.Fatalf("Enroll agent2 失败: %v", err)
	}

	var totalDualPlc int64
	_ = db.Model(&models.ServicePlacement{}).Where("module_service_id = ?", dualSvcs[0].ID).Count(&totalDualPlc).Error
	if totalDualPlc != 3 {
		t.Fatalf("跨环境后 DualHostSvc 放置总数应为 3（agent1 2 条 + agent2 1 条），实际 %d", totalDualPlc)
	}
	var env1Plc models.ServicePlacement
	_ = db.Where("module_service_id = ? AND agent_id = ?", dualSvcs[0].ID, agent1.ID).First(&env1Plc).Error
	if string(env1Plc.Endpoints) == "" || env1Plc.ArchProcessID == 0 {
		t.Fatalf("agent1 放置数据被异常破坏: %+v", env1Plc)
	}
}

func TestEnroll_NeverOverwritesManualFields_DisarmedNoDigestChange_AndArmAllSkipsDiscovered(t *testing.T) {
	db := setupEnrollBaseDB(t)

	pgrp := models.ArchProcessGroup{Code: "PGRP_MAN", Name: "人工进程组"}
	_ = db.Create(&pgrp).Error

	fpMan, _, _ := ComputeIdentityFingerprint("/opt/manual/srv", "/opt/manual/srv --cfg=prod", "manual_srv")
	apMan := models.ArchProcess{
		GroupID:             pgrp.ID,
		Code:                "manual-core",
		Name:                "人工核心名称",
		DefaultCmdPattern:   "manual_srv --cfg=prod",
		DeclaredRole:        models.ArchProcRoleServer,
		IdentityFingerprint: fpMan,
		FingerprintVersion:  CurrentFingerprintVersion,
		Lifecycle:           models.ArchProcLifecycleConfirmed,
		Origin:              models.ArchProcOriginManual,
		Description:         "不可被覆盖的人工说明",
	}
	_ = db.Create(&apMan).Error

	holderUserID := uint(99)
	agent := models.Agent{
		Name:                "agent-no-overwrite",
		Endpoint:            "10.0.3.1:8090",
		Scope:               models.ScopeShared,
		Status:              models.AgentStatusOnline,
		ExpectedArmedDigest: "digest-keep-unchanged",
		ArmedDigest:         "digest-keep-unchanged",
		LeaseType:           "RECORDING",
		LeaseHolderID:       &holderUserID, // 他人持有租约期间依然允许无副作用纳管
	}
	_ = db.Create(&agent).Error

	procManual := models.Process{
		AgentID:             agent.ID,
		ArchProcessID:       apMan.ID,
		EffectiveListenPort: 22001,
		OverrideCmdPattern:  "manual_srv --cfg=prod",
		ExtraDownstreams:    datatypes.JSON("[]"),
		EnrollSource:        models.EnrollSourceManual,
		ProbeDesiredState:   models.ProbeDesiredStateArmed,
		ProbeStatus:         models.ProbeStatusReady,
	}
	_ = db.Create(&procManual).Error

	report := &agentproto.CSGTopologyReport{
		DiscoveryMode: "CROSTCP_RPCMON",
		Services: []agentproto.CSGServiceInfo{
			{ServiceName: "man.v1.Svc-1", Addr: "tcp://ipv4:10.0.3.1:22001", Transport: "TCP", ServerPID: 301, ServerProc: "manual_srv", IsLocal: true},
			{ServiceName: "new.v1.DiscSvc-2", Addr: "dophi_mesh://ipv4:10.0.3.1:22002", Transport: "MESH", ServerPID: 302, ServerProc: "new_disc", IsLocal: true},
		},
	}
	disc := []agentproto.DiscoveredProcess{
		{PID: 301, ListenPort: 22001, ExeName: "/opt/manual/srv", Cmdline: "/opt/manual/srv --cfg=prod", Transports: []string{"TCP"}},
		{PID: 302, ListenPort: 0, ExeName: "/opt/bin/new_disc", Cmdline: "/opt/bin/new_disc --flag=1", Transports: []string{"MESH"}},
	}
	fetcher := &routeByAgentFetcher{
		reports: map[uint64]*agentproto.CSGTopologyReport{agent.ID: report},
		procs:   map[uint64][]agentproto.DiscoveredProcess{agent.ID: disc},
	}
	liveSvc := NewLiveDiscoveryService(db, fetcher)
	live, err := liveSvc.GetLiveDiscovery(context.Background(), agent.ID, "", false, 2, false)
	if err != nil {
		t.Fatalf("GetLiveDiscovery 失败: %v", err)
	}

	items := make([]EnrollItemInput, 0, len(live.Processes))
	for _, p := range live.Processes {
		items = append(items, EnrollItemInput{ProcessKey: p.ProcessKey})
	}
	if _, err := Enroll(context.Background(), db, liveSvc, EnrollInput{
		AgentID: agent.ID, Items: items, OperatorID: 2, IsAdmin: false,
	}); err != nil {
		t.Fatalf("租约期间普通用户 Enroll 应成功: %v", err)
	}

	// 1. 验证人工 ArchProcess 字段零覆盖
	var apCheck models.ArchProcess
	_ = db.First(&apCheck, apMan.ID).Error
	if apCheck.Name != "人工核心名称" || apCheck.GroupID != pgrp.ID || apCheck.DeclaredRole != models.ArchProcRoleServer || apCheck.Description != "不可被覆盖的人工说明" {
		t.Fatalf("人工 ArchProcess 字段被意外覆盖: %+v", apCheck)
	}

	// 2. 验证既有 ARMED 挂载未被改写为 DISARMED，但 observed_* 已刷新
	var procManCheck models.Process
	_ = db.First(&procManCheck, procManual.ID).Error
	if procManCheck.ProbeDesiredState != models.ProbeDesiredStateArmed || procManCheck.EnrollSource != models.EnrollSourceManual || procManCheck.LastObservedAt == nil {
		t.Fatalf("人工挂载布防态被篡改或未刷新观测时间: %+v", procManCheck)
	}

	// 3. 验证新纳管进程为 DISCOVERY + DISARMED，且 Agent Digest 零变动
	var procDisc models.Process
	if err := db.Where("agent_id = ? AND id != ?", agent.ID, procManual.ID).First(&procDisc).Error; err != nil {
		t.Fatalf("未找到新纳管 Process: %v", err)
	}
	if procDisc.EnrollSource != models.EnrollSourceDiscovery || procDisc.ProbeDesiredState != models.ProbeDesiredStateDisarmed || procDisc.ProbeStatus != models.ProbeStatusDisarmed {
		t.Fatalf("发现纳管进程必须为 DISCOVERY + DISARMED，实际: %+v", procDisc)
	}
	var agentCheck models.Agent
	_ = db.First(&agentCheck, agent.ID).Error
	if agentCheck.ExpectedArmedDigest != "digest-keep-unchanged" || agentCheck.ArmedDigest != "digest-keep-unchanged" {
		t.Fatalf("Enroll 绝不允许修改 Agent ArmedDigest: %+v", agentCheck)
	}
}

func TestEnroll_SharedBatchLimit422_Ambiguous422_LowConfidence422(t *testing.T) {
	db := setupEnrollBaseDB(t)

	// 1. 特性开关关闭时返 ErrDiscoveryV2Disabled (404)
	_ = db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "discovery.v2_enabled").Update("setting_val", "false").Error
	agent := models.Agent{Name: "agent-guard", Endpoint: "10.0.4.1:8090", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	_ = db.Create(&agent).Error
	if _, err := Enroll(context.Background(), db, NewLiveDiscoveryService(db, nil), EnrollInput{
		AgentID: agent.ID, Items: []EnrollItemInput{{ProcessKey: "x"}}, OperatorID: 1,
	}); !errors.Is(err, ErrDiscoveryV2Disabled) {
		t.Fatalf("v2_enabled=false 期望 ErrDiscoveryV2Disabled，实际: %v", err)
	}
	enableDiscoveryV2(t, db)

	// 2. SHARED 批量上限 422 ENROLL_BATCH_LIMIT
	_ = db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "discovery.enroll_batch_limit_shared").Update("setting_val", "2").Error
	_, err := Enroll(context.Background(), db, NewLiveDiscoveryService(db, nil), EnrollInput{
		AgentID:    agent.ID,
		Items:      []EnrollItemInput{{ProcessKey: "a"}, {ProcessKey: "b"}, {ProcessKey: "c"}},
		OperatorID: 1,
	})
	if !errors.Is(err, ErrEnrollBatchLimit) {
		t.Fatalf("超过批量上限期望 ErrEnrollBatchLimit，实际: %v", err)
	}
	_ = db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "discovery.enroll_batch_limit_shared").Update("setting_val", "50").Error

	// 3. 歧义与低置信 422 拦截及重提交成功
	pgrp := models.ArchProcessGroup{Code: "PGRP_AMB", Name: "歧义测试组"}
	_ = db.Create(&pgrp).Error
	fpAmb, _, _ := ComputeIdentityFingerprint("/opt/bin/amb", "/opt/bin/amb --x=1", "amb")
	ap1 := models.ArchProcess{GroupID: pgrp.ID, Code: "amb-1", Name: "歧义1", IdentityFingerprint: fpAmb, FingerprintVersion: CurrentFingerprintVersion, Lifecycle: models.ArchProcLifecycleConfirmed}
	ap2 := models.ArchProcess{GroupID: pgrp.ID, Code: "amb-2", Name: "歧义2", IdentityFingerprint: fpAmb, FingerprintVersion: CurrentFingerprintVersion, Lifecycle: models.ArchProcLifecycleConfirmed}
	_ = db.Create(&ap1).Error
	_ = db.Create(&ap2).Error

	report := &agentproto.CSGTopologyReport{
		DiscoveryMode: "PROC_ONLY",
		Services: []agentproto.CSGServiceInfo{
			{ServiceName: "low.v1.Svc-1", Addr: "tcp://ipv4:10.0.4.1:24001", Transport: "TCP", ServerPID: 402, ServerProc: "low_proc-402", IsLocal: true},
		},
	}
	disc := []agentproto.DiscoveredProcess{
		{PID: 401, ListenPort: 0, ExeName: "/opt/bin/amb", Cmdline: "/opt/bin/amb --x=1", Transports: []string{"TCP"}},
	}
	liveSvc := NewLiveDiscoveryService(db, &routeByAgentFetcher{
		reports: map[uint64]*agentproto.CSGTopologyReport{agent.ID: report},
		procs:   map[uint64][]agentproto.DiscoveredProcess{agent.ID: disc},
	})
	live, err := liveSvc.GetLiveDiscovery(context.Background(), agent.ID, "", false, 1, true)
	if err != nil {
		t.Fatalf("live failed: %v", err)
	}

	var ambKey, lowKey string
	for _, p := range live.Processes {
		if p.ExeName == "amb" || p.ProcName == "amb" {
			ambKey = p.ProcessKey
		} else if p.ProcName == "low_proc" {
			lowKey = p.ProcessKey
		}
	}

	// 歧义未选候选 ➔ 422 IDENTITY_AMBIGUOUS
	_, err = Enroll(context.Background(), db, liveSvc, EnrollInput{
		AgentID: agent.ID, Items: []EnrollItemInput{{ProcessKey: ambKey}}, OperatorID: 1, BypassRateLimit: true,
	})
	var valErr *EnrollValidationError
	if !errors.As(err, &valErr) || valErr.Code != "IDENTITY_AMBIGUOUS" {
		t.Fatalf("多候选应返回 IDENTITY_AMBIGUOUS，实际: %v", err)
	}

	// 低置信未确认 ➔ 422 LOW_CONFIDENCE_CONFIRM_REQUIRED
	_, err = Enroll(context.Background(), db, liveSvc, EnrollInput{
		AgentID: agent.ID, Items: []EnrollItemInput{{ProcessKey: lowKey}}, OperatorID: 1, BypassRateLimit: true,
	})
	if !errors.As(err, &valErr) || valErr.Code != "LOW_CONFIDENCE_CONFIRM_REQUIRED" {
		t.Fatalf("低置信未确认应返回 LOW_CONFIDENCE_CONFIRM_REQUIRED，实际: %v", err)
	}

	// 传入 force_arch_process_id 与 confirm_low_confidence=true ➔ 成功 200
	resOK, err := Enroll(context.Background(), db, liveSvc, EnrollInput{
		AgentID: agent.ID,
		Items: []EnrollItemInput{
			{ProcessKey: ambKey, ForceArchProcessID: &ap2.ID},
			{ProcessKey: lowKey, ConfirmLowConfidence: true},
		},
		OperatorID:      1,
		BypassRateLimit: true,
	})
	if err != nil {
		t.Fatalf("显式消歧与低置信确认后 Enroll 应成功: %v", err)
	}
	if len(resOK.Enrolled) != 2 {
		t.Fatalf("期望返回 2 条已纳管记录，实际: %+v", resOK)
	}
}

func TestVisibility_PrivateProvisionalHiddenFromOthersAndGlobal_SharedAutoPromotes(t *testing.T) {
	db := setupEnrollBaseDB(t)

	aliceID := uint(11)
	bobID := uint(12)
	privAgent := models.Agent{Name: "alice-priv", Endpoint: "10.0.5.1:8090", Scope: models.ScopePrivate, OwnerID: &aliceID, Status: models.AgentStatusOnline}
	sharedAgent := models.Agent{Name: "team-shared", Endpoint: "10.0.5.2:8090", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	_ = db.Create(&privAgent).Error
	_ = db.Create(&sharedAgent).Error

	privReport := &agentproto.CSGTopologyReport{
		DiscoveryMode: "CROSTCP_RPCMON",
		Services: []agentproto.CSGServiceInfo{
			{ServiceName: "priv.v1.OrderSvc-501", Addr: "tcp://ipv4:10.0.5.1:25001", Transport: "TCP", ServerPID: 501, ServerProc: "OrderCore", IsLocal: true},
		},
	}
	privDisc := []agentproto.DiscoveredProcess{
		{PID: 501, ListenPort: 25001, ExeName: "/opt/bin/order_core", Cmdline: "/opt/bin/order_core --mode=v1", Transports: []string{"TCP"}},
	}
	sharedReport := &agentproto.CSGTopologyReport{
		DiscoveryMode: "CROSTCP_RPCMON",
		Services: []agentproto.CSGServiceInfo{
			{ServiceName: "priv.v1.OrderSvc-601", Addr: "tcp://ipv4:10.0.5.2:26001", Transport: "TCP", ServerPID: 601, ServerProc: "OrderCore", IsLocal: true},
		},
	}
	sharedDisc := []agentproto.DiscoveredProcess{
		{PID: 601, ListenPort: 26001, ExeName: "/srv/bin/order_core", Cmdline: "/srv/bin/order_core --mode=v1 --port=26001", Transports: []string{"TCP"}},
	}

	liveSvc := NewLiveDiscoveryService(db, &routeByAgentFetcher{
		reports: map[uint64]*agentproto.CSGTopologyReport{privAgent.ID: privReport, sharedAgent.ID: sharedReport},
		procs:   map[uint64][]agentproto.DiscoveredProcess{privAgent.ID: privDisc, sharedAgent.ID: sharedDisc},
	})

	// 1. Alice 在 PRIVATE Agent 纳管 -> 生成 PROVISIONAL，编码 prv<agent_id>-<norm>
	livePriv, err := liveSvc.GetLiveDiscovery(context.Background(), privAgent.ID, "", false, aliceID, false)
	if err != nil {
		t.Fatalf("livePriv 失败: %v", err)
	}
	resPriv, err := Enroll(context.Background(), db, liveSvc, EnrollInput{
		AgentID: privAgent.ID, Items: []EnrollItemInput{{ProcessKey: livePriv.Processes[0].ProcessKey}}, OperatorID: aliceID,
	})
	if err != nil {
		t.Fatalf("PRIVATE Enroll 失败: %v", err)
	}
	apID := resPriv.Enrolled[0].ArchProcessID
	var ap models.ArchProcess
	_ = db.First(&ap, apID).Error
	expectedPrefix := fmt.Sprintf("prv%d-", privAgent.ID)
	if ap.Lifecycle != models.ArchProcLifecycleProvisional || ap.Origin != models.ArchProcOriginDiscovery || ap.OriginAgentID == nil || *ap.OriginAgentID != privAgent.ID {
		t.Fatalf("PRIVATE 新建进程必须为 PROVISIONAL 且带 origin_agent_id=%d，实际: %+v", privAgent.ID, ap)
	}
	if len(ap.Code) <= len(expectedPrefix) || ap.Code[:len(expectedPrefix)] != expectedPrefix {
		t.Fatalf("PROVISIONAL 编码应以 %q 开头防止公共编码泄露，实际: %q", expectedPrefix, ap.Code)
	}

	// 2. 可见性隔离 V2：Alice 与 Admin 可见，Bob 不可见
	agentMap := map[uint64]*models.Agent{privAgent.ID: &privAgent, sharedAgent.ID: &sharedAgent}
	if !IsArchProcessVisibleToUser(ap, agentMap, aliceID, false) {
		t.Fatalf("PRIVATE PROVISIONAL 应对创建人 Alice 可见")
	}
	if !IsArchProcessVisibleToUser(ap, agentMap, bobID, true) {
		t.Fatalf("PRIVATE PROVISIONAL 应对管理员可见")
	}
	if IsArchProcessVisibleToUser(ap, agentMap, bobID, false) {
		t.Fatalf("PRIVATE PROVISIONAL 严禁对他人 Bob 可见")
	}

	// 3. 在 SHARED 环境纳管同指纹进程 ➔ 自动提升为 CONFIRMED 并去除 prv<id>- 前缀
	liveShared, err := liveSvc.GetLiveDiscovery(context.Background(), sharedAgent.ID, "", false, bobID, false)
	if err != nil {
		t.Fatalf("liveShared 失败: %v", err)
	}
	// Note: liveShared by non-owner Bob shouldn't have seen Alice's PROVISIONAL in live view, yet Enroll matches fingerprint in DB and promotes!
	resShared, err := Enroll(context.Background(), db, liveSvc, EnrollInput{
		AgentID: sharedAgent.ID, Items: []EnrollItemInput{{ProcessKey: liveShared.Processes[0].ProcessKey}}, OperatorID: bobID,
	})
	if err != nil {
		t.Fatalf("SHARED Enroll 自动提升失败: %v", err)
	}
	if resShared.Enrolled[0].ArchProcessID != apID {
		t.Fatalf("SHARED 同指纹应复用并提升原 PROVISIONAL 进程 %d，实际 %d", apID, resShared.Enrolled[0].ArchProcessID)
	}
	var promoted models.ArchProcess
	_ = db.First(&promoted, apID).Error
	if promoted.Lifecycle != models.ArchProcLifecycleConfirmed {
		t.Fatalf("SHARED 纳管后 lifecycle 应自动提升为 CONFIRMED，实际: %s", promoted.Lifecycle)
	}
	if len(promoted.Code) >= len(expectedPrefix) && promoted.Code[:len(expectedPrefix)] == expectedPrefix {
		t.Fatalf("提升后编码应规范化去除 %q 前缀，实际: %q", expectedPrefix, promoted.Code)
	}
	if !IsArchProcessVisibleToUser(promoted, agentMap, bobID, false) {
		t.Fatalf("提升为 CONFIRMED 后应对全员可见")
	}
}

type routeByAgentFetcher struct {
	reports map[uint64]*agentproto.CSGTopologyReport
	procs   map[uint64][]agentproto.DiscoveredProcess
}

func (f *routeByAgentFetcher) FetchAgentCSGTopology(ctx context.Context, agentID uint64, csgAddr string, currentUserID uint, isAdmin bool) (*agentproto.CSGTopologyReport, error) {
	if r, ok := f.reports[agentID]; ok {
		return r, nil
	}
	return &agentproto.CSGTopologyReport{}, nil
}

func (f *routeByAgentFetcher) FetchAgentDiscoveredProcesses(ctx context.Context, agentID uint64, currentUserID uint, isAdmin bool) ([]agentproto.DiscoveredProcess, error) {
	if p, ok := f.procs[agentID]; ok {
		return p, nil
	}
	return nil, nil
}
