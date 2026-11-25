package discovery

import (
	"context"
	"testing"
	"time"

	"code-contract/models"
	"gorm.io/datatypes"
)

func TestMigrateCSGPlaceholders_DryRun_BlockedList_Execute_Idempotent_AndRollback(t *testing.T) {
	db := setupEnrollBaseDB(t)

	pm := &models.PartitionManager{}
	if _, err := pm.EnsureDailyPartition(db, time.Now().UTC()); err != nil {
		t.Fatalf("EnsureDailyPartition 失败: %v", err)
	}

	sub := models.Subsystem{Code: "SUB_MIG", Name: "迁移测试子系统"}
	_ = db.Create(&sub).Error
	csgFG := models.FunctionalGroup{SubsystemID: sub.ID, Code: "CSG_DISCOVERED", Name: "CSG 自动发现占位组", Level: 1, Path: "/1/"}
	_ = db.Create(&csgFG).Error
	bizFG := models.FunctionalGroup{SubsystemID: sub.ID, Code: "BIZ_CORE", Name: "核心业务组", Level: 1, Path: "/2/"}
	_ = db.Create(&bizFG).Error

	pgrp := models.ArchProcessGroup{Code: "PGRP_MIG", Name: "迁移进程组"}
	_ = db.Create(&pgrp).Error
	apSafe := models.ArchProcess{GroupID: pgrp.ID, Code: "safe-worker", Name: "安全迁移进程"}
	apBlocked := models.ArchProcess{GroupID: pgrp.ID, Code: "blocked-worker", Name: "受阻进程"}
	_ = db.Create(&apSafe).Error
	_ = db.Create(&apBlocked).Error

	agent := models.Agent{Name: "mig-shared-agent", Endpoint: "10.30.0.1:8090", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	_ = db.Create(&agent).Error
	procSafe := models.Process{AgentID: agent.ID, ArchProcessID: apSafe.ID, ExtraDownstreams: datatypes.JSON("[]"), EnrollSource: models.EnrollSourceDiscovery, ProbeDesiredState: models.ProbeDesiredStateDisarmed, ProbeStatus: models.ProbeStatusDisarmed}
	_ = db.Create(&procSafe).Error

	// 真实人工业务模块
	realMod := models.Module{SubsystemID: sub.ID, GroupID: bizFG.ID, Code: "REAL_CALLER", Name: "人工调用方", ModuleKind: models.ModuleKindBusiness}
	_ = db.Create(&realMod).Error

	// 1. 安全可清理占位假模块 MOD_safe_worker
	safeFakeMod := models.Module{SubsystemID: sub.ID, GroupID: csgFG.ID, Code: "MOD_safe_worker", Name: "安全占位模块", ArchProcessID: &apSafe.ID, ModuleKind: models.ModuleKindBusiness}
	_ = db.Create(&safeFakeMod).Error
	safeSvc := models.ModuleService{ModuleID: models.Uint64Ptr(safeFakeMod.ID), ServiceName: "mig.v1.SafeSvc", Source: models.ServiceSourceManual}
	_ = db.Create(&safeSvc).Error

	// 2. 受阻占位假模块 MOD_blocked_worker（被手工 ModuleDependency 引用）
	blockedFakeMod := models.Module{SubsystemID: sub.ID, GroupID: csgFG.ID, Code: "MOD_blocked_worker", Name: "受阻占位模块", ArchProcessID: &apBlocked.ID, ModuleKind: models.ModuleKindBusiness}
	_ = db.Create(&blockedFakeMod).Error
	blockedSvc := models.ModuleService{ModuleID: models.Uint64Ptr(blockedFakeMod.ID), ServiceName: "mig.v1.BlockedSvc", Source: models.ServiceSourceManual}
	_ = db.Create(&blockedSvc).Error
	_ = db.Create(&models.ModuleDependency{
		ConsumerModuleID: realMod.ID,
		ServiceID:        &blockedSvc.ID,
		ProviderModuleID: &blockedFakeMod.ID,
		Description:      "人工依赖引用阻止删除",
	}).Error

	// 插入流量记录验证 MIGRATION AttributionJob 自动回填 src/dst_arch_process_id
	sess := models.RecordingSession{
		SessionNo:        "REC-MIG-001",
		Name:             "迁移回填测试",
		Scope:            models.ScopeShared,
		OwnerID:          1,
		Status:           "PUBLISHED",
		TargetProcessIDs: datatypes.JSON("[1]"),
	}
	_ = db.Create(&sess).Error
	now := time.Now().UTC()
	tr := models.TrafficRecord{
		CreatedAt:    now,
		SessionID:    sess.ID,
		SeqNo:        1,
		AgentID:      agent.ID,
		ProcessID:    procSafe.ID,
		CapturedPID:  1234,
		ConnKey:      "127.0.0.1:1000->127.0.0.1:2000",
		Direction:    "EGRESS",
		ExchangeType: "UNARY",
		SrcAddr:      "127.0.0.1:1000",
		DstAddr:      "127.0.0.1:2000",
		SNO:          1,
		ServiceName:  "mig.v1.SafeSvc",
		SrcModuleID:  &safeFakeMod.ID,
		DstModuleID:  &safeFakeMod.ID,
	}
	if err := db.Create(&tr).Error; err != nil {
		t.Fatalf("insert traffic_record: %v", err)
	}

	// Step A: Dry-Run — 识别 blocked 与 safe，零写库零删除
	dryRes, err := MigrateCSGPlaceholderModules(context.Background(), db, MigratePlaceholdersInput{
		DryRun: true, OperatorID: 1,
	})
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if dryRes.Mode != "DRY_RUN" || dryRes.ClearedModules != 1 || dryRes.MigratedServices != 1 || len(dryRes.BlockedItems) != 1 || dryRes.BlockedItems[0].ModuleID != blockedFakeMod.ID {
		t.Fatalf("DryRun 结果不符合预期: %+v", dryRes)
	}
	var modCnt int64
	_ = db.Model(&models.Module{}).Where("id = ?", safeFakeMod.ID).Count(&modCnt).Error
	if modCnt != 1 {
		t.Fatalf("DryRun 严禁真实删除模块")
	}

	// Step B: Execute — 清理安全占位模块、回退服务为未认领 MIGRATED、生成 STALE 放置、写入备份、回填流量并标记 DONE
	execRes, err := MigrateCSGPlaceholderModules(context.Background(), db, MigratePlaceholdersInput{
		DryRun: false, Action: "EXECUTE", OperatorID: 1,
	})
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if execRes.Mode != "EXECUTE" || execRes.ClearedModules != 1 || execRes.MigratedServices != 1 || execRes.BackupBatchID == "" || execRes.RewrittenRecords < 1 || len(execRes.BlockedItems) != 1 {
		t.Fatalf("Execute 结果不符合预期: %+v", execRes)
	}

	_ = db.Model(&models.Module{}).Where("id = ?", safeFakeMod.ID).Count(&modCnt).Error
	if modCnt != 0 {
		t.Fatalf("安全占位模块应已被删除")
	}
	_ = db.Model(&models.Module{}).Where("id = ?", blockedFakeMod.ID).Count(&modCnt).Error
	if modCnt != 1 {
		t.Fatalf("受阻占位模块必须保留")
	}

	var refreshedSafeSvc models.ModuleService
	_ = db.First(&refreshedSafeSvc, safeSvc.ID).Error
	if refreshedSafeSvc.ModuleID != nil || refreshedSafeSvc.Source != models.ServiceSourceMigrated {
		t.Fatalf("安全服务应解绑为 module_id=NULL & source=MIGRATED，实际: %+v", refreshedSafeSvc)
	}
	var stalePlc models.ServicePlacement
	if err := db.Where("module_service_id = ? AND process_id = ?", safeSvc.ID, procSafe.ID).First(&stalePlc).Error; err != nil {
		t.Fatalf("应补齐历史关联 STALE 放置: %v", err)
	}
	if stalePlc.Status != models.PlacementStatusStale {
		t.Fatalf("迁移补齐放置应为 STALE，实际: %s", stalePlc.Status)
	}

	var updatedTR models.TrafficRecord
	_ = db.Where("session_id = ? AND seq_no = 1", sess.ID).First(&updatedTR).Error
	if updatedTR.SrcArchProcessID == nil || *updatedTR.SrcArchProcessID != apSafe.ID || updatedTR.DstArchProcessID == nil || *updatedTR.DstArchProcessID != apSafe.ID {
		t.Fatalf("MIGRATION 任务应回填 src/dst_arch_process_id=%d，实际 src=%v dst=%v", apSafe.ID, updatedTR.SrcArchProcessID, updatedTR.DstArchProcessID)
	}
	if execRes.AttributionJobID != nil {
		var ajob models.AttributionJob
		_ = db.First(&ajob, *execRes.AttributionJobID).Error
		if ajob.JobType != models.AttrJobTypeMigration || ajob.Status != models.AttrJobStatusDone {
			t.Fatalf("AttributionJob 应为 MIGRATION+DONE，实际: %+v", ajob)
		}
	}

	// Step C: 幂等重跑 — 0 新增清理，受阻仍为 1
	idemRes, err := MigrateCSGPlaceholderModules(context.Background(), db, MigratePlaceholdersInput{
		DryRun: false, Action: "EXECUTE", OperatorID: 1,
	})
	if err != nil {
		t.Fatalf("idempotent execute failed: %v", err)
	}
	if idemRes.ClearedModules != 0 || idemRes.MigratedServices != 0 || len(idemRes.BlockedItems) != 1 {
		t.Fatalf("幂等重跑期望 ClearedModules=0, MigratedServices=0，实际: %+v", idemRes)
	}

	// Step D: Rollback — 恢复安全占位模块与服务归属，并置备份状态为 ROLLED_BACK
	rbRes, err := MigrateCSGPlaceholderModules(context.Background(), db, MigratePlaceholdersInput{
		DryRun: false, Action: "ROLLBACK", BatchID: execRes.BackupBatchID, OperatorID: 1,
	})
	if err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	if rbRes.Mode != "ROLLBACK" || rbRes.ClearedModules != 1 || rbRes.MigratedServices != 1 {
		t.Fatalf("Rollback 结果不符合预期: %+v", rbRes)
	}

	var restoredMod models.Module
	if err := db.First(&restoredMod, safeFakeMod.ID).Error; err != nil {
		t.Fatalf("回滚后安全占位模块应恢复 (id=%d): %v", safeFakeMod.ID, err)
	}
	_ = db.First(&refreshedSafeSvc, safeSvc.ID).Error
	if refreshedSafeSvc.ModuleID == nil || *refreshedSafeSvc.ModuleID != safeFakeMod.ID {
		t.Fatalf("回滚后服务应重新绑定回模块 %d，实际: %+v", safeFakeMod.ID, refreshedSafeSvc)
	}
	var bkCnt int64
	_ = db.Model(&models.MigBackup{}).Where("migration_key = ? AND migrated_state = ?", execRes.BackupBatchID, models.MigBackupStateRolledBack).Count(&bkCnt).Error
	if bkCnt == 0 {
		t.Fatalf("回滚后 MigBackup 必须标记为 ROLLED_BACK")
	}
}
