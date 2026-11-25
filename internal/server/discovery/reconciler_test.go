package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/datatypes"
)

type dynamicAgentFetcher struct {
	report *agentproto.CSGTopologyReport
	procs  []agentproto.DiscoveredProcess
	err    error
}

func (f *dynamicAgentFetcher) FetchAgentCSGTopology(ctx context.Context, agentID uint64, csgAddr string, currentUserID uint, isAdmin bool) (*agentproto.CSGTopologyReport, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.report, nil
}

func (f *dynamicAgentFetcher) FetchAgentDiscoveredProcesses(ctx context.Context, agentID uint64, currentUserID uint, isAdmin bool) ([]agentproto.DiscoveredProcess, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.procs, nil
}

func TestPhysicalReconciler_DecayStaleRetired_OfflineFreeze_AndGCNeverDeletesClaimed(t *testing.T) {
	db := setupEnrollBaseDB(t)

	_ = db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "discovery.stale_miss_threshold").Update("setting_val", "3").Error
	_ = db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "discovery.retire_days").Update("setting_val", "7").Error
	_ = db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "discovery.gc_days").Update("setting_val", "30").Error

	agent := models.Agent{Name: "reconcile-agent", Endpoint: "10.9.0.1:8090", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	sub := models.Subsystem{Code: "SUB_R", Name: "R子系统"}
	_ = db.Create(&sub).Error
	fg := models.FunctionalGroup{SubsystemID: sub.ID, Code: "FG_R", Name: "R组", Level: 1, Path: "/1/"}
	_ = db.Create(&fg).Error
	mod := models.Module{SubsystemID: sub.ID, GroupID: fg.ID, Code: "MOD_R", Name: "核心模块", ModuleKind: models.ModuleKindBusiness}
	_ = db.Create(&mod).Error

	// 已认领服务：即使放置彻底退役也绝不允许 GC
	claimedSvc := models.ModuleService{ModuleID: models.Uint64Ptr(mod.ID), ServiceName: "claimed.v1.CoreSvc", Source: models.ServiceSourceManual}
	_ = db.Create(&claimedSvc).Error

	// 有 diff_ignore_paths 的未认领服务：受保护不被 GC
	ignoredSvc := models.ModuleService{
		ModuleID:        nil,
		ServiceName:     "ignored.v1.ProtectSvc",
		Source:          models.ServiceSourceCSGDiscovered,
		DiffIgnorePaths: datatypes.JSON(`["$.trace_id"]`),
	}
	_ = db.Create(&ignoredSvc).Error

	fetcher := &dynamicAgentFetcher{
		report: &agentproto.CSGTopologyReport{
			DiscoveryMode: "CROSTCP_RPCMON",
			Services: []agentproto.CSGServiceInfo{
				{ServiceName: "ephem.v1.TempSvc", Addr: "tcp://ipv4:10.9.0.1:21001", Transport: "TCP", ServerPID: 701, ServerProc: "worker_r", IsLocal: true},
				{ServiceName: "claimed.v1.CoreSvc", Addr: "tcp://ipv4:10.9.0.1:21001", Transport: "TCP", ServerPID: 701, ServerProc: "worker_r", IsLocal: true},
				{ServiceName: "ignored.v1.ProtectSvc", Addr: "tcp://ipv4:10.9.0.1:21001", Transport: "TCP", ServerPID: 701, ServerProc: "worker_r", IsLocal: true},
			},
			Subscriptions: []agentproto.CSGSubscription{
				{ClientPID: 701, ClientProc: "worker_r", ServiceName: "temp.v1.SubSvc", IsLocal: true},
				{ClientPID: 701, ClientProc: "worker_r", ServiceName: "perm.v1.KeepSub", IsLocal: true},
			},
		},
		procs: []agentproto.DiscoveredProcess{
			{PID: 701, ListenPort: 21001, ExeName: "/opt/bin/worker_r", Cmdline: "/opt/bin/worker_r --role=all", Transports: []string{"TCP"}},
		},
	}

	liveSvc := NewLiveDiscoveryService(db, fetcher)
	live, err := liveSvc.GetLiveDiscovery(context.Background(), agent.ID, "", false, 1, true)
	if err != nil {
		t.Fatalf("live failed: %v", err)
	}
	enrollRes, err := Enroll(context.Background(), db, liveSvc, EnrollInput{
		AgentID: agent.ID, Items: []EnrollItemInput{{ProcessKey: live.Processes[0].ProcessKey}}, OperatorID: 1, IsAdmin: true,
	})
	if err != nil {
		t.Fatalf("enroll failed: %v", err)
	}
	procID := enrollRes.Enrolled[0].ProcessID

	// 校验已纳管 Process 为 DISARMED，后续对账绝不篡改 ProbeDesiredState / ProbeStatus
	rec := NewPhysicalReconciler(db, liveSvc)

	// 1. 停止发布 TempSvc/CoreSvc/ProtectSvc 且停止订阅 SubSvc，保留 KeepSub
	fetcher.report = &agentproto.CSGTopologyReport{
		DiscoveryMode: "CROSTCP_RPCMON",
		Subscriptions: []agentproto.CSGSubscription{
			{ClientPID: 701, ClientProc: "worker_r", ServiceName: "perm.v1.KeepSub", IsLocal: true},
		},
	}

	// 前 2 次对账：MissCount=1, 2，状态仍为 ACTIVE
	for round := 1; round <= 2; round++ {
		if err := rec.ReconcileAgent(context.Background(), agent.ID); err != nil {
			t.Fatalf("round %d reconcile failed: %v", round, err)
		}
		var plcs []models.ServicePlacement
		_ = db.Where("process_id = ?", procID).Find(&plcs).Error
		for _, p := range plcs {
			if p.MissCount != round || p.Status != models.PlacementStatusActive {
				t.Fatalf("round %d 期望 miss=%d ACTIVE，实际: miss=%d status=%s", round, round, p.MissCount, p.Status)
			}
		}
	}

	// 第 3 次对账：达到 stale_miss_threshold=3 -> 转为 STALE
	if err := rec.ReconcileAgent(context.Background(), agent.ID); err != nil {
		t.Fatalf("round 3 reconcile failed: %v", err)
	}
	var tempPlc models.ServicePlacement
	var ephemSvc models.ModuleService
	_ = db.Where("service_name = ?", "ephem.v1.TempSvc").First(&ephemSvc).Error
	_ = db.Where("module_service_id = ? AND process_id = ?", ephemSvc.ID, procID).First(&tempPlc).Error
	if tempPlc.Status != models.PlacementStatusStale || tempPlc.MissCount != 3 {
		t.Fatalf("第 3 次未见应转为 STALE(miss=3)，实际: %+v", tempPlc)
	}
	var subDep models.ProcessDependency
	_ = db.Where("consumer_process_id = ? AND service_name = ?", procID, "temp.v1.SubSvc").First(&subDep).Error
	if subDep.Status != models.PlacementStatusStale || subDep.MissCount != 3 {
		t.Fatalf("第 3 次未见订阅边应转为 STALE，实际: %+v", subDep)
	}

	// 2. 离线冻结：Agent=OFFLINE 或 fetcher 报错时严禁推进 miss_count / 状态
	_ = db.Model(&models.Agent{}).Where("id = ?", agent.ID).Update("status", models.AgentStatusOffline).Error
	_ = rec.ReconcileAgent(context.Background(), agent.ID)
	_ = db.First(&tempPlc, tempPlc.ID).Error
	if tempPlc.MissCount != 3 || tempPlc.Status != models.PlacementStatusStale {
		t.Fatalf("Agent OFFLINE 期间 miss_count 不得推进，实际: %+v", tempPlc)
	}

	_ = db.Model(&models.Agent{}).Where("id = ?", agent.ID).Update("status", models.AgentStatusOnline).Error
	fetcher.err = errors.New("simulated timeout")
	_ = rec.ReconcileAgent(context.Background(), agent.ID)
	_ = db.First(&tempPlc, tempPlc.ID).Error
	if tempPlc.MissCount != 3 || tempPlc.Status != models.PlacementStatusStale {
		t.Fatalf("探测报错期间 miss_count 不得推进，实际: %+v", tempPlc)
	}
	fetcher.err = nil

	// 3. 恢复 SubSvc 上线 -> 重现复活为 ACTIVE, miss=0
	fetcher.report.Subscriptions = append(fetcher.report.Subscriptions, agentproto.CSGSubscription{
		ClientPID: 701, ClientProc: "worker_r", ServiceName: "temp.v1.SubSvc", IsLocal: true,
	})
	if err := rec.ReconcileAgent(context.Background(), agent.ID); err != nil {
		t.Fatalf("revive reconcile failed: %v", err)
	}
	_ = db.Where("consumer_process_id = ? AND service_name = ?", procID, "temp.v1.SubSvc").First(&subDep).Error
	if subDep.Status != models.PlacementStatusActive || subDep.MissCount != 0 {
		t.Fatalf("重新观测到应立即复活为 ACTIVE(miss=0)，实际: %+v", subDep)
	}

	// 确认 Process ProbeDesiredState 仍为 DISARMED 未被对账器篡改
	var procCheck models.Process
	_ = db.First(&procCheck, procID).Error
	if procCheck.ProbeDesiredState != models.ProbeDesiredStateDisarmed {
		t.Fatalf("PhysicalReconciler 不得篡改 ProbeDesiredState，实际: %s", procCheck.ProbeDesiredState)
	}

	// 4. STALE 超过 retire_days=7 -> 自动转为 RETIRED
	eightDaysAgo := time.Now().Add(-8 * 24 * time.Hour)
	_ = db.Model(&models.ServicePlacement{}).Where("process_id = ?", procID).Update("last_seen_at", eightDaysAgo).Error
	if err := rec.ReconcileAgent(context.Background(), agent.ID); err != nil {
		t.Fatalf("retire reconcile failed: %v", err)
	}
	_ = db.First(&tempPlc, tempPlc.ID).Error
	if tempPlc.Status != models.PlacementStatusRetired {
		t.Fatalf("超过 7 天应转为 RETIRED，实际: %s", tempPlc.Status)
	}

	// 5. 日 GC 测试：超 30 天未认领且无 diff_ignore_paths 的 RETIRED 服务被清理；已认领与 diff_ignore_paths 永不删；孤立 PROVISIONAL 超 30 天被清理
	thirtyFiveDaysAgo := time.Now().Add(-35 * 24 * time.Hour)
	_ = db.Model(&models.ServicePlacement{}).Where("process_id = ?", procID).Updates(map[string]interface{}{
		"status":     models.PlacementStatusRetired,
		"updated_at": thirtyFiveDaysAgo,
	}).Error
	_ = db.Model(&models.ModuleService{}).Where("id IN ?", []uint64{ephemSvc.ID, claimedSvc.ID, ignoredSvc.ID}).Update("updated_at", thirtyFiveDaysAgo).Error

	pgrp := models.ArchProcessGroup{Code: "PGRP_GC", Name: "GC测试组"}
	_ = db.Create(&pgrp).Error
	orphanProv := models.ArchProcess{
		GroupID: pgrp.ID, Code: "prv99-orphan", Name: "孤立试用", Lifecycle: models.ArchProcLifecycleProvisional, Origin: models.ArchProcOriginDiscovery,
	}
	_ = db.Create(&orphanProv).Error
	_ = db.Model(&models.ArchProcess{}).Where("id = ?", orphanProv.ID).Update("updated_at", thirtyFiveDaysAgo).Error

	gcRes, err := rec.RunGCOnce(context.Background())
	if err != nil {
		t.Fatalf("RunGCOnce 失败: %v", err)
	}
	if gcRes.DeletedServices != 1 || gcRes.DeletedProvisionalProcesses != 1 {
		t.Fatalf("期望清理 1 条未认领服务与 1 条孤立试用进程，实际: %+v", gcRes)
	}

	var cnt int64
	_ = db.Model(&models.ModuleService{}).Where("id = ?", ephemSvc.ID).Count(&cnt).Error
	if cnt != 0 {
		t.Fatalf("ephem.v1.TempSvc 应已被 GC")
	}
	_ = db.Model(&models.ModuleService{}).Where("id = ?", claimedSvc.ID).Count(&cnt).Error
	if cnt != 1 {
		t.Fatalf("已认领服务 claimed.v1.CoreSvc 永不允许被 GC")
	}
	_ = db.Model(&models.ModuleService{}).Where("id = ?", ignoredSvc.ID).Count(&cnt).Error
	if cnt != 1 {
		t.Fatalf("带 diff_ignore_paths 的服务绝不允许被 GC")
	}
	_ = db.Model(&models.ArchProcess{}).Where("id = ?", orphanProv.ID).Count(&cnt).Error
	if cnt != 0 {
		t.Fatalf("超 30 天无挂载 PROVISIONAL 进程应被 GC")
	}
}
