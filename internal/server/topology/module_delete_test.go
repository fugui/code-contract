package topology

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"code-common/backend/testdb"
	"code-contract/config"
	"code-contract/models"
	"gorm.io/datatypes"
)

func TestDeleteModuleTx_DepReferenced409_RollbackUnclaimed_AndMergeMultiImpl(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB 失败: %v", err)
	}

	sub, err := CreateSubsystem(db, SubsystemInput{Code: "DEL_SUB", Name: "删除测试子系统"})
	if err != nil {
		t.Fatalf("CreateSubsystem 失败: %v", err)
	}
	fg, err := CreateFunctionalGroup(db, FunctionalGroupInput{SubsystemID: sub.ID, Code: "DEL_FG", Name: "删除测试功能组"})
	if err != nil {
		t.Fatalf("CreateFunctionalGroup 失败: %v", err)
	}
	pgrp := models.ArchProcessGroup{Code: "PGRP_DEL", Name: "删除测试进程组"}
	if err := db.Create(&pgrp).Error; err != nil {
		t.Fatalf("创建 ArchProcessGroup 失败: %v", err)
	}
	ap1 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-del-1", Name: "ap-del-1"}
	ap2 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-del-2", Name: "ap-del-2"}
	if err := db.Create(&ap1).Error; err != nil {
		t.Fatalf("创建 ap1 失败: %v", err)
	}
	if err := db.Create(&ap2).Error; err != nil {
		t.Fatalf("创建 ap2 失败: %v", err)
	}

	agent := models.Agent{Name: "agent-del-tx", Endpoint: "192.168.56.102:19001", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("创建 Agent 失败: %v", err)
	}
	proc1 := models.Process{
		AgentID: agent.ID, ArchProcessID: ap1.ID, EffectiveListenPort: 21001,
		ExtraDownstreams: datatypes.JSON("[]"), EnrollSource: models.EnrollSourceDiscovery, ProbeDesiredState: models.ProbeDesiredStateDisarmed, ProbeStatus: models.ProbeStatusDisarmed,
	}
	proc2 := models.Process{
		AgentID: agent.ID, ArchProcessID: ap2.ID, EffectiveListenPort: 21002,
		ExtraDownstreams: datatypes.JSON("[]"), EnrollSource: models.EnrollSourceDiscovery, ProbeDesiredState: models.ProbeDesiredStateDisarmed, ProbeStatus: models.ProbeStatusDisarmed,
	}
	if err := db.Create(&proc1).Error; err != nil {
		t.Fatalf("创建 proc1 失败: %v", err)
	}
	if err := db.Create(&proc2).Error; err != nil {
		t.Fatalf("创建 proc2 失败: %v", err)
	}

	// 路径 1: I6 依赖守卫 -> 返回 409 DEPENDENCY_REFERENCED
	modProvider, err := CreateModule(db, ModuleInput{GroupID: fg.ID, Code: "MOD_PROV", Name: "被依赖模块", ArchProcessID: &ap1.ID})
	if err != nil {
		t.Fatalf("CreateModule modProvider 失败: %v", err)
	}
	modConsumer, err := CreateModule(db, ModuleInput{GroupID: fg.ID, Code: "MOD_CONS", Name: "消费模块", ArchProcessID: &ap2.ID})
	if err != nil {
		t.Fatalf("CreateModule modConsumer 失败: %v", err)
	}
	svcRef, err := CreateModuleService(db, ModuleServiceInput{ModuleID: modProvider.ID, ServiceName: "del.v1.RefService"})
	if err != nil {
		t.Fatalf("CreateModuleService svcRef 失败: %v", err)
	}
	depRef, err := CreateModuleDependency(db, ModuleDependencyInput{ConsumerModuleID: modConsumer.ID, ServiceID: &svcRef.ID})
	if err != nil {
		t.Fatalf("CreateModuleDependency 失败: %v", err)
	}

	_, err = DeleteModule(db, modProvider.ID)
	var depErr *DependencyReferencedError
	if !errors.As(err, &depErr) {
		t.Fatalf("I6 期望返回 *DependencyReferencedError，实际: %v", err)
	}
	if len(depErr.ReferencingModules) == 0 || depErr.ReferencingModules[0].ModuleID != modConsumer.ID {
		t.Fatalf("DependencyReferencedError 应列出消费模块 %d，实际: %+v", modConsumer.ID, depErr.ReferencingModules)
	}

	// 移除依赖引用后继续测试路径 2（I7 回退未认领 + 删纯手工服务）
	if _, err := DeleteModuleDependency(db, depRef.ID); err != nil {
		t.Fatalf("DeleteModuleDependency 失败: %v", err)
	}

	now := time.Now().UTC()
	// svcRef 绑定 1 条放置 -> 应原地回退为未认领 (source=MIGRATED, module_id=NULL)
	spRef := models.ServicePlacement{
		ModuleServiceID: svcRef.ID, ProcessID: proc1.ID, RawServiceName: "del.v1.RefService",
		Endpoints: datatypes.JSON(`["tcp://192.168.56.102:21001"]`), ObservedTransport: models.ObservedTransportTCP,
		InstanceCount: 1, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now,
	}
	if err := db.Create(&spRef).Error; err != nil {
		t.Fatalf("创建 spRef 失败: %v", err)
	}
	// 无放置的纯手工服务 -> 应被物理删除
	svcManualOnly, err := CreateModuleService(db, ModuleServiceInput{ModuleID: modProvider.ID, ServiceName: "del.v1.ManualOnly"})
	if err != nil {
		t.Fatalf("CreateModuleService svcManualOnly 失败: %v", err)
	}

	if _, err := DeleteModule(db, modProvider.ID); err != nil {
		t.Fatalf("路径 2 DeleteModule 失败: %v", err)
	}

	var modCheck models.Module
	if err := db.First(&modCheck, modProvider.ID).Error; err == nil {
		t.Fatalf("模块 %d 应当已被删除", modProvider.ID)
	}
	var manualCheck models.ModuleService
	if err := db.First(&manualCheck, svcManualOnly.ID).Error; err == nil {
		t.Fatalf("无放置的纯手工服务 %d 应当已被删除", svcManualOnly.ID)
	}
	var rolledBackSvc models.ModuleService
	if err := db.First(&rolledBackSvc, svcRef.ID).Error; err != nil {
		t.Fatalf("有放置的服务 %d 应当回退为未认领保留: %v", svcRef.ID, err)
	}
	if rolledBackSvc.ModuleID != nil || rolledBackSvc.Source != models.ServiceSourceMigrated || rolledBackSvc.ClaimedAt != nil || rolledBackSvc.ClaimedBy != 0 {
		t.Fatalf("服务回退字段不正确: %+v", rolledBackSvc)
	}
	var spCount int64
	_ = db.Model(&models.ServicePlacement{}).Where("module_service_id = ?", svcRef.ID).Count(&spCount).Error
	if spCount != 1 {
		t.Fatalf("服务放置应当完整保留，实际数量 %d", spCount)
	}

	// 路径 3: I7 MULTI_IMPL 合并到既有同名未认领身份
	libMod, err := CreateModule(db, ModuleInput{GroupID: fg.ID, Code: "LIB_MERGE", Name: "合并契约库", ModuleKind: models.ModuleKindContractLib})
	if err != nil {
		t.Fatalf("CreateModule libMod 失败: %v", err)
	}
	contract, err := CreateServiceContract(db, ServiceContractInput{DefinerModuleID: libMod.ID, ServiceName: "del.v1.MultiMerge"})
	if err != nil {
		t.Fatalf("CreateServiceContract 失败: %v", err)
	}
	modImpl, err := CreateModule(db, ModuleInput{GroupID: fg.ID, Code: "MOD_IMPL", Name: "实现模块", ArchProcessID: &ap2.ID})
	if err != nil {
		t.Fatalf("CreateModule modImpl 失败: %v", err)
	}

	// 既有未认领身份（在 proc1 有放置 + ignore_paths=["$.ts"]）
	existingUnclaimed := models.ModuleService{
		ModuleID:        nil,
		ServiceName:     "del.v1.MultiMerge",
		Source:          models.ServiceSourceCSGDiscovered,
		DiffIgnorePaths: datatypes.JSON(`["$.ts"]`),
	}
	if err := db.Create(&existingUnclaimed).Error; err != nil {
		t.Fatalf("创建 existingUnclaimed 失败: %v", err)
	}
	if err := db.Create(&models.ServicePlacement{
		ModuleServiceID: existingUnclaimed.ID, ProcessID: proc1.ID, RawServiceName: "del.v1.MultiMerge",
		Endpoints: datatypes.JSON(`["tcp://192.168.56.102:21001"]`), ObservedTransport: models.ObservedTransportTCP,
		InstanceCount: 1, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now,
	}).Error; err != nil {
		t.Fatalf("创建 existingUnclaimed 放置失败: %v", err)
	}

	// modImpl 下的多实现行（在 proc2 有放置 + ignore_paths=["$.trace_id"]，且被 ProcessDependency 引用）
	implSvc, err := CreateModuleService(db, ModuleServiceInput{
		ModuleID:        modImpl.ID,
		ContractID:      &contract.ID,
		ImplKey:         "impl_b",
		ServiceName:     "del.v1.MultiMerge",
		DiffIgnorePaths: []string{"$.trace_id"},
	})
	if err != nil {
		t.Fatalf("CreateModuleService implSvc 失败: %v", err)
	}
	if err := db.Create(&models.ServicePlacement{
		ModuleServiceID: implSvc.ID, ProcessID: proc2.ID, RawServiceName: "del.v1.MultiMerge",
		Endpoints: datatypes.JSON(`["tcp://192.168.56.102:21002"]`), ObservedTransport: models.ObservedTransportTCP,
		InstanceCount: 1, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now,
	}).Error; err != nil {
		t.Fatalf("创建 implSvc 放置失败: %v", err)
	}
	pdep := models.ProcessDependency{
		ConsumerProcessID: proc1.ID,
		ServiceName:       "del.v1.MultiMerge",
		ServiceID:         &implSvc.ID,
		ProviderKind:      models.ProviderKindLocalEnrolled,
		Source:            models.ProcDepSourceCSGSub,
		Status:            models.PlacementStatusActive,
		FirstSeenAt:       now,
		LastSeenAt:        now,
	}
	if err := db.Create(&pdep).Error; err != nil {
		t.Fatalf("创建 ProcessDependency 失败: %v", err)
	}

	if _, err := DeleteModule(db, modImpl.ID); err != nil {
		t.Fatalf("路径 3 DeleteModule 合并失败: %v", err)
	}

	// implSvc 应当已被合并删除，放置合并至 existingUnclaimed (共 2 条)
	var implCheck models.ModuleService
	if err := db.First(&implCheck, implSvc.ID).Error; err == nil {
		t.Fatalf("被合并的实现服务 %d 应当已被删除", implSvc.ID)
	}
	var mergedSPCount int64
	_ = db.Model(&models.ServicePlacement{}).Where("module_service_id = ?", existingUnclaimed.ID).Count(&mergedSPCount).Error
	if mergedSPCount != 2 {
		t.Fatalf("合并后未认领身份放置数期望为 2，实际为 %d", mergedSPCount)
	}
	var reloadedUnclaimed models.ModuleService
	if err := db.First(&reloadedUnclaimed, existingUnclaimed.ID).Error; err != nil {
		t.Fatalf("回查 existingUnclaimed 失败: %v", err)
	}
	var ignorePaths []string
	if err := json.Unmarshal(reloadedUnclaimed.DiffIgnorePaths, &ignorePaths); err != nil {
		t.Fatalf("解析合并后的 DiffIgnorePaths 失败: %v", err)
	}
	if len(ignorePaths) != 2 {
		t.Fatalf("期望并集合并 2 条 ignore_paths，实际: %v", ignorePaths)
	}
	var reloadedDep models.ProcessDependency
	if err := db.First(&reloadedDep, pdep.ID).Error; err != nil {
		t.Fatalf("回查 ProcessDependency 失败: %v", err)
	}
	if reloadedDep.ServiceID == nil || *reloadedDep.ServiceID != existingUnclaimed.ID {
		t.Fatalf("ProcessDependency.ServiceID 应重指向 %d，实际: %v", existingUnclaimed.ID, reloadedDep.ServiceID)
	}
}
