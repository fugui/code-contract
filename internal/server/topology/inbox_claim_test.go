package topology

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"code-contract/models"
)

func (e *inboxEnv) op(u *models.User, admin bool) Operator {
	return Operator{UserID: u.ID, IsAdmin: admin}
}

func TestClaimServices_RBAC_ModuleOwnerSucceeds_OtherOwner403_NormalUser403(t *testing.T) {
	e := newInboxEnv(t)
	ctx := context.Background()

	mk := func(name string) models.ModuleService {
		s := e.newUnclaimedSvc(t, name)
		e.place(t, s.ID, e.proc1.ID, models.PlacementStatusActive)
		return s
	}
	sOwner, sSub, sAdmin, sOther := mk("rbac.v1.OwnerSvc"), mk("rbac.v1.SubSvc"), mk("rbac.v1.AdminSvc"), mk("rbac.v1.OtherSvc")

	// 普通用户（非 Module/Subsystem 负责人）403
	if _, err := ClaimServices(ctx, e.db, e.op(e.otherUser, false), []ClaimItem{{ServiceID: sOther.ID, ModuleID: e.modA.ID}}); !errors.Is(err, ErrClaimForbidden) {
		t.Fatalf("普通用户认领期望 ErrClaimForbidden，实际: %v", err)
	}
	var reloaded models.ModuleService
	if err := e.db.First(&reloaded, sOther.ID).Error; err != nil || reloaded.IsClaimed() {
		t.Fatalf("越权认领不得改变服务归属: %+v err=%v", reloaded, err)
	}

	// Module 负责人认领本人模块 -> 成功，并回填 claimed_by/claimed_at
	res, err := ClaimServices(ctx, e.db, e.op(e.modOwner, false), []ClaimItem{{ServiceID: sOwner.ID, ModuleID: e.modA.ID}})
	if err != nil {
		t.Fatalf("模块负责人认领失败: %v", err)
	}
	if res.ClaimedCount != 1 {
		t.Fatalf("ClaimedCount 期望 1，实际 %d", res.ClaimedCount)
	}
	reloaded = models.ModuleService{}
	_ = e.db.First(&reloaded, sOwner.ID).Error
	if !reloaded.IsClaimed() || reloaded.ModuleIDVal() != e.modA.ID || reloaded.ClaimedBy != e.modOwner.ID || reloaded.ClaimedAt == nil {
		t.Fatalf("认领结果不符: %+v", reloaded)
	}

	// 他人模块：模块负责人认领非本人负责（subOwner 负责子系统而 libMod 无负责人 -> 子系统负责人可）
	// modOwner 尝试认领进 libMod（无 owner，属 subOwner 子系统）-> 403
	if _, err := ClaimServices(ctx, e.db, e.op(e.modOwner, false), []ClaimItem{{ServiceID: sSub.ID, ModuleID: e.libMod.ID}}); !errors.Is(err, ErrClaimForbidden) {
		t.Fatalf("他人负责模块认领期望 ErrClaimForbidden，实际: %v", err)
	}

	// 混合批次：任一目标模块越权则整批 403 且零写入
	if _, err := ClaimServices(ctx, e.db, e.op(e.modOwner, false), []ClaimItem{
		{ServiceID: sSub.ID, ModuleID: e.modB.ID},
		{ServiceID: sOther.ID, ModuleID: e.libMod.ID},
	}); !errors.Is(err, ErrClaimForbidden) {
		t.Fatalf("混合批次期望 ErrClaimForbidden，实际: %v", err)
	}
	reloaded = models.ModuleService{}
	_ = e.db.First(&reloaded, sSub.ID).Error
	if reloaded.IsClaimed() {
		t.Fatalf("混合批次越权应整体回滚，sSub 不应被认领")
	}

	// Subsystem 负责人认领子系统下任意模块 -> 成功
	if _, err := ClaimServices(ctx, e.db, e.op(e.subOwner, false), []ClaimItem{{ServiceID: sSub.ID, ModuleID: e.modB.ID}}); err != nil {
		t.Fatalf("子系统负责人认领失败: %v", err)
	}
	// 管理员 -> 成功
	if _, err := ClaimServices(ctx, e.db, e.op(e.adminUser, true), []ClaimItem{{ServiceID: sAdmin.ID, ModuleID: e.modA.ID}}); err != nil {
		t.Fatalf("管理员认领失败: %v", err)
	}

	// 已认领服务再次认领 -> 冲突
	if _, err := ClaimServices(ctx, e.db, e.op(e.adminUser, true), []ClaimItem{{ServiceID: sAdmin.ID, ModuleID: e.modB.ID}}); !errors.Is(err, ErrClaimAlreadyClaimed) {
		t.Fatalf("重复认领期望 ErrClaimAlreadyClaimed，实际: %v", err)
	}
	// 空批次 / 不存在
	if _, err := ClaimServices(ctx, e.db, e.op(e.adminUser, true), nil); !errors.Is(err, ErrClaimEmptyItems) {
		t.Fatalf("空批次期望 ErrClaimEmptyItems，实际: %v", err)
	}
	if _, err := ClaimServices(ctx, e.db, e.op(e.adminUser, true), []ClaimItem{{ServiceID: 99999999, ModuleID: e.modA.ID}}); !errors.Is(err, ErrModuleServiceNotFound) {
		t.Fatalf("不存在服务期望 ErrModuleServiceNotFound，实际: %v", err)
	}
	if _, err := ClaimServices(ctx, e.db, e.op(e.adminUser, true), []ClaimItem{{ServiceID: sOther.ID, ModuleID: 99999999}}); !errors.Is(err, ErrModuleNotFound) {
		t.Fatalf("不存在模块期望 ErrModuleNotFound，实际: %v", err)
	}
}

func TestClaimServices_InvariantI4_PlacementMismatch422_BackfillsModuleProc(t *testing.T) {
	e := newInboxEnv(t)
	ctx := context.Background()
	admin := e.op(e.adminUser, true)

	// 1. 多宿主（ap1 + ap2 均 ACTIVE）-> 422
	multi := e.newUnclaimedSvc(t, "i4.v1.MultiHost")
	e.place(t, multi.ID, e.proc1.ID, models.PlacementStatusActive)
	e.place(t, multi.ID, e.proc2.ID, models.PlacementStatusActive)
	_, err := ClaimServices(ctx, e.db, admin, []ClaimItem{{ServiceID: multi.ID, ModuleID: e.modA.ID}})
	var mismatch *ClaimPlacementMismatchError
	if !errors.As(err, &mismatch) || mismatch.Code != "CLAIM_PLACEMENT_MISMATCH" {
		t.Fatalf("多宿主认领期望 CLAIM_PLACEMENT_MISMATCH，实际: %v", err)
	}
	var modA models.Module
	_ = e.db.First(&modA, e.modA.ID).Error
	if modA.ArchProcessID != nil {
		t.Fatalf("422 失败不得回填 Module.arch_process_id，实际: %v", *modA.ArchProcessID)
	}

	// 2. 单宿主 ap1，但目标模块 modC 已绑 ap2 -> 422
	s1 := e.newUnclaimedSvc(t, "i4.v1.SingleAp1")
	e.place(t, s1.ID, e.proc1.ID, models.PlacementStatusActive)
	if _, err := ClaimServices(ctx, e.db, admin, []ClaimItem{{ServiceID: s1.ID, ModuleID: e.modC.ID}}); !errors.As(err, &mismatch) {
		t.Fatalf("模块已绑他进程期望 422，实际: %v", err)
	}

	// 3. 单宿主 ap1 + modB 未绑 -> 成功并回填 modB.arch_process_id = ap1
	res, err := ClaimServices(ctx, e.db, admin, []ClaimItem{{ServiceID: s1.ID, ModuleID: e.modB.ID}})
	if err != nil {
		t.Fatalf("单宿主认领失败: %v", err)
	}
	if res.AttributionJobID == nil {
		t.Fatalf("认领成功后应自动入队 REATTRIBUTE 作业")
	}
	var job models.AttributionJob
	if err := e.db.First(&job, *res.AttributionJobID).Error; err != nil {
		t.Fatalf("读取归因作业失败: %v", err)
	}
	if job.JobType != models.AttrJobTypeReattribute || job.Status != models.AttrJobStatusPending || job.CreatedBy != e.adminUser.ID {
		t.Fatalf("归因作业字段不符: %+v", job)
	}
	var scope struct {
		ServiceNames []string `json:"service_names"`
	}
	_ = json.Unmarshal(job.ScopeJSON, &scope)
	if len(scope.ServiceNames) != 1 || scope.ServiceNames[0] != "i4.v1.SingleAp1" {
		t.Fatalf("作业 scope_json.service_names 不符: %s", string(job.ScopeJSON))
	}
	var modB models.Module
	_ = e.db.First(&modB, e.modB.ID).Error
	if modB.ArchProcessID == nil || *modB.ArchProcessID != e.ap1.ID {
		t.Fatalf("认领应同事务回填 modB.arch_process_id=ap1，实际: %v", modB.ArchProcessID)
	}

	// 4. 1 进程 : N 模块：同进程 ap1 的另一个服务认领进另一个模块 modA（均无返工）
	s2 := e.newUnclaimedSvc(t, "i4.v1.SingleAp1Other")
	e.place(t, s2.ID, e.proc1.ID, models.PlacementStatusActive)
	if _, err := ClaimServices(ctx, e.db, admin, []ClaimItem{
		{ServiceID: s2.ID, ModuleID: e.modA.ID},
	}); err != nil {
		t.Fatalf("同进程服务认领进另一模块失败: %v", err)
	}
	var apMods int64
	e.db.Model(&models.Module{}).Where("arch_process_id = ?", e.ap1.ID).Count(&apMods)
	if apMods != 2 {
		t.Fatalf("ap1 应承载 2 个业务模块，实际 %d", apMods)
	}

	// 5. 批次内跨进程：同一模块先后认领 ap1 与 ap2 服务 -> 后者 422，整批回滚
	s3 := e.newUnclaimedSvc(t, "i4.v1.BatchAp1")
	e.place(t, s3.ID, e.proc1.ID, models.PlacementStatusActive)
	s4 := e.newUnclaimedSvc(t, "i4.v1.BatchAp2")
	e.place(t, s4.ID, e.proc2.ID, models.PlacementStatusActive)
	if _, err := ClaimServices(ctx, e.db, admin, []ClaimItem{
		{ServiceID: s3.ID, ModuleID: e.modB.ID},
		{ServiceID: s4.ID, ModuleID: e.modB.ID},
	}); !errors.As(err, &mismatch) {
		t.Fatalf("同模块跨进程批次期望 422，实际: %v", err)
	}
	var r3 models.ModuleService
	_ = e.db.First(&r3, s3.ID).Error
	if r3.IsClaimed() {
		t.Fatalf("批次失败应整体回滚")
	}

	// 6. 仅 PRIVATE / STALE 放置不计入权威环境：ap1 ACTIVE(SHARED) + PRIVATE 放置在 apPriv + ap2 STALE -> 仍可认领
	s5 := e.newUnclaimedSvc(t, "i4.v1.PrivNoise")
	e.place(t, s5.ID, e.proc1.ID, models.PlacementStatusActive)
	e.place(t, s5.ID, e.procPriv.ID, models.PlacementStatusActive)
	e.place(t, s5.ID, e.proc2.ID, models.PlacementStatusStale)
	if _, err := ClaimServices(ctx, e.db, admin, []ClaimItem{{ServiceID: s5.ID, ModuleID: e.modB.ID}}); err != nil {
		t.Fatalf("PRIVATE/STALE 放置不应阻断认领: %v", err)
	}
}

func TestSplitMultiImpl_MigratesPlacementsAndDeletesUnclaimedIdentity(t *testing.T) {
	e := newInboxEnv(t)
	ctx := context.Background()
	admin := e.op(e.adminUser, true)

	svc := e.newUnclaimedSvc(t, "split.v1.SyncService")
	e.place(t, svc.ID, e.proc1.ID, models.PlacementStatusActive)
	e.place(t, svc.ID, e.proc2.ID, models.PlacementStatusActive)

	// 非 CONTRACT_LIB 定义方 -> 409
	if _, err := SplitMultiImpl(ctx, e.db, admin, SplitMultiImplInput{
		SourceServiceID: svc.ID, DefinerModuleID: e.modA.ID,
		Implementations: []SplitImplInput{{ArchProcessID: e.ap1.ID, ModuleID: e.modA.ID, ImplKey: "a"}, {ArchProcessID: e.ap2.ID, ModuleID: e.modC.ID, ImplKey: "c"}},
	}); !errors.Is(err, ErrDefinerNotContractLib) {
		t.Fatalf("非接口库定义方期望 ErrDefinerNotContractLib，实际: %v", err)
	}
	// 实现少于 2 个 / 进程重复 / 未覆盖权威进程 -> 400 类错误
	if _, err := SplitMultiImpl(ctx, e.db, admin, SplitMultiImplInput{
		SourceServiceID: svc.ID, DefinerModuleID: e.libMod.ID,
		Implementations: []SplitImplInput{{ArchProcessID: e.ap1.ID, ModuleID: e.modA.ID, ImplKey: "a"}},
	}); !errors.Is(err, ErrSplitInvalidInput) {
		t.Fatalf("单实现期望 ErrSplitInvalidInput，实际: %v", err)
	}
	if _, err := SplitMultiImpl(ctx, e.db, admin, SplitMultiImplInput{
		SourceServiceID: svc.ID, DefinerModuleID: e.libMod.ID,
		Implementations: []SplitImplInput{{ArchProcessID: e.ap1.ID, ModuleID: e.modA.ID, ImplKey: "a"}, {ArchProcessID: e.ap1.ID, ModuleID: e.modB.ID, ImplKey: "b"}},
	}); !errors.Is(err, ErrSplitInvalidInput) {
		t.Fatalf("同进程重复实现期望 ErrSplitInvalidInput，实际: %v", err)
	}
	// 目标模块已绑他进程 -> 422 CLAIM_PLACEMENT_MISMATCH（modC 绑 ap2，却分配给 ap1）
	var mismatch *ClaimPlacementMismatchError
	if _, err := SplitMultiImpl(ctx, e.db, admin, SplitMultiImplInput{
		SourceServiceID: svc.ID, DefinerModuleID: e.libMod.ID,
		Implementations: []SplitImplInput{{ArchProcessID: e.ap1.ID, ModuleID: e.modC.ID, ImplKey: "c"}, {ArchProcessID: e.ap2.ID, ModuleID: e.modA.ID, ImplKey: "a"}},
	}); !errors.As(err, &mismatch) {
		t.Fatalf("模块进程不一致期望 422，实际: %v", err)
	}
	// 失败不得有任何落库
	var cnt int64
	e.db.Model(&models.ServiceContract{}).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("校验失败不得创建契约，实际 %d", cnt)
	}

	// 成功拆分：ap1 -> modA(impl a)，ap2 -> modC(impl c)
	res, err := SplitMultiImpl(ctx, e.db, admin, SplitMultiImplInput{
		SourceServiceID: svc.ID, DefinerModuleID: e.libMod.ID,
		Implementations: []SplitImplInput{{ArchProcessID: e.ap1.ID, ModuleID: e.modA.ID, ImplKey: "a"}, {ArchProcessID: e.ap2.ID, ModuleID: e.modC.ID, ImplKey: "c"}},
	})
	if err != nil {
		t.Fatalf("SplitMultiImpl 失败: %v", err)
	}
	if res.Contract == nil || res.Contract.ServiceName != "split.v1.SyncService" || res.Contract.DefinerModuleID != e.libMod.ID {
		t.Fatalf("契约不符: %+v", res.Contract)
	}
	if len(res.Implementations) != 2 || res.AttributionJobID == nil {
		t.Fatalf("应生成 2 条实现行并入队归因作业: %+v", res)
	}
	for _, impl := range res.Implementations {
		if impl.ContractID == nil || *impl.ContractID != res.Contract.ID || impl.IsCustomServiceName || impl.ServiceName != "split.v1.SyncService" || !impl.IsClaimed() {
			t.Fatalf("实现行字段不符: %+v", impl)
		}
		var pls []models.ServicePlacement
		e.db.Where("module_service_id = ?", impl.ID).Find(&pls)
		if len(pls) != 1 {
			t.Fatalf("每个实现应迁入 1 条放置，实际 %d", len(pls))
		}
		var m models.Module
		_ = e.db.First(&m, impl.ModuleIDVal()).Error
		if m.ArchProcessID == nil || pls[0].ArchProcessID != *m.ArchProcessID {
			t.Fatalf("放置进程应与实现模块进程一致: pl=%d mod=%v", pls[0].ArchProcessID, m.ArchProcessID)
		}
	}
	// 原未认领身份行已删除，且无放置丢失
	var remain int64
	e.db.Model(&models.ModuleService{}).Where("id = ?", svc.ID).Count(&remain)
	if remain != 0 {
		t.Fatalf("原未认领身份行应被删除")
	}
	e.db.Model(&models.ServicePlacement{}).Count(&remain)
	if remain != 2 {
		t.Fatalf("放置总数应保持 2（零丢失），实际 %d", remain)
	}
	var modA models.Module
	_ = e.db.First(&modA, e.modA.ID).Error
	if modA.ArchProcessID == nil || *modA.ArchProcessID != e.ap1.ID {
		t.Fatalf("modA 应被回填 ap1")
	}

	// 残留非权威放置（PRIVATE）时保留未认领身份
	svc2 := e.newUnclaimedSvc(t, "split.v1.KeepIdentity")
	e.place(t, svc2.ID, e.proc1.ID, models.PlacementStatusActive)
	e.place(t, svc2.ID, e.proc2.ID, models.PlacementStatusActive)
	e.place(t, svc2.ID, e.procPriv.ID, models.PlacementStatusActive)
	modD := models.Module{SubsystemID: e.sub.ID, GroupID: e.fg.ID, Code: "M32_MOD_D", Name: "模块D"}
	modE := models.Module{SubsystemID: e.sub.ID, GroupID: e.fg.ID, Code: "M32_MOD_E", Name: "模块E"}
	mustCreate(t, e.db, &modD)
	mustCreate(t, e.db, &modE)
	if _, err := SplitMultiImpl(ctx, e.db, admin, SplitMultiImplInput{
		SourceServiceID: svc2.ID, DefinerModuleID: e.libMod.ID,
		Implementations: []SplitImplInput{{ArchProcessID: e.ap1.ID, ModuleID: modD.ID, ImplKey: "d"}, {ArchProcessID: e.ap2.ID, ModuleID: modE.ID, ImplKey: "e"}},
	}); err != nil {
		t.Fatalf("存在残留私有放置的拆分失败: %v", err)
	}
	e.db.Model(&models.ModuleService{}).Where("id = ?", svc2.ID).Count(&remain)
	if remain != 1 {
		t.Fatalf("残留 PRIVATE 放置时应保留未认领身份行")
	}

	// 权威进程未被覆盖 -> 400
	svc3 := e.newUnclaimedSvc(t, "split.v1.Uncovered")
	e.place(t, svc3.ID, e.proc1.ID, models.PlacementStatusActive)
	e.place(t, svc3.ID, e.proc2.ID, models.PlacementStatusActive)
	if _, err := SplitMultiImpl(ctx, e.db, admin, SplitMultiImplInput{
		SourceServiceID: svc3.ID, DefinerModuleID: e.libMod.ID,
		Implementations: []SplitImplInput{{ArchProcessID: e.ap1.ID, ModuleID: modD.ID, ImplKey: "d"}, {ArchProcessID: 987654, ModuleID: modE.ID, ImplKey: "e"}},
	}); !errors.Is(err, ErrSplitInvalidInput) {
		t.Fatalf("未覆盖权威进程期望 ErrSplitInvalidInput，实际: %v", err)
	}
}

func TestUnclaim_InvariantI6_DepReferenced409_AndI7Merge(t *testing.T) {
	e := newInboxEnv(t)
	ctx := context.Background()
	admin := e.op(e.adminUser, true)

	// I6：被 ModuleDependency 引用 -> 409
	provSvc := e.newClaimedSvc(t, "unc.v1.Referenced", e.modA.ID)
	e.place(t, provSvc.ID, e.proc1.ID, models.PlacementStatusActive)
	dep := models.ModuleDependency{ConsumerModuleID: e.modC.ID, ServiceID: &provSvc.ID, ProviderModuleID: models.Uint64Ptr(e.modA.ID)}
	mustCreate(t, e.db, &dep)

	_, err := UnclaimService(ctx, e.db, admin, provSvc.ID)
	var depErr *DependencyReferencedError
	if !errors.As(err, &depErr) || depErr.Code != "DEPENDENCY_REFERENCED" || len(depErr.ReferencingModules) != 1 || depErr.ReferencingModules[0].ModuleID != e.modC.ID {
		t.Fatalf("I6 期望 DependencyReferencedError 列出消费模块，实际: %v", err)
	}

	// RBAC：非负责人 403；负责人 OK（先移除依赖）
	if err := e.db.Delete(&dep).Error; err != nil {
		t.Fatalf("删除依赖失败: %v", err)
	}
	if _, err := UnclaimService(ctx, e.db, e.op(e.otherUser, false), provSvc.ID); !errors.Is(err, ErrClaimForbidden) {
		t.Fatalf("非负责人取消认领期望 ErrClaimForbidden，实际: %v", err)
	}

	// I7 原地回退：存在放置 -> 清空归属，source=MIGRATED
	res, err := UnclaimService(ctx, e.db, e.op(e.modOwner, false), provSvc.ID)
	if err != nil {
		t.Fatalf("取消认领失败: %v", err)
	}
	if res.Action != UnclaimActionUnclaimed || res.ServiceID != provSvc.ID {
		t.Fatalf("期望 UNCLAIMED，实际: %+v", res)
	}
	var back models.ModuleService
	_ = e.db.First(&back, provSvc.ID).Error
	if back.IsClaimed() || back.ContractID != nil || back.ImplKey != "" || back.Source != models.ServiceSourceMigrated || back.ClaimedAt != nil || back.ClaimedBy != 0 {
		t.Fatalf("I7 回退字段不符: %+v", back)
	}

	// I7 合并：MULTI_IMPL 实现行回退时，已存在同名未认领身份 -> 放置并入既有行，回退行删除
	contract := models.ServiceContract{ServiceName: "unc.v1.MultiSvc", DefinerModuleID: e.libMod.ID, ProvideMode: models.ContractProvideModeMultiImpl}
	mustCreate(t, e.db, &contract)
	impl := models.ModuleService{ModuleID: models.Uint64Ptr(e.modB.ID), ContractID: &contract.ID, ImplKey: "b", ServiceName: "unc.v1.MultiSvc", Source: models.ServiceSourceCSGDiscovered}
	mustCreate(t, e.db, &impl)
	e.place(t, impl.ID, e.proc1.ID, models.PlacementStatusActive)
	existing := e.newUnclaimedSvc(t, "unc.v1.MultiSvc")
	e.place(t, existing.ID, e.proc2.ID, models.PlacementStatusActive)

	res, err = UnclaimService(ctx, e.db, admin, impl.ID)
	if err != nil {
		t.Fatalf("合并取消认领失败: %v", err)
	}
	if res.Action != UnclaimActionMerged || res.ServiceID != existing.ID {
		t.Fatalf("期望 MERGED 到既有身份 %d，实际: %+v", existing.ID, res)
	}
	var plCnt, implCnt int64
	e.db.Model(&models.ServicePlacement{}).Where("module_service_id = ?", existing.ID).Count(&plCnt)
	e.db.Model(&models.ModuleService{}).Where("id = ?", impl.ID).Count(&implCnt)
	if plCnt != 2 || implCnt != 0 {
		t.Fatalf("合并后既有身份应持有 2 条放置且回退行删除: pl=%d impl=%d", plCnt, implCnt)
	}

	// 无放置纯手工服务 -> DELETED
	manual := e.newClaimedSvc(t, "unc.v1.ManualOnly", e.modA.ID)
	res, err = UnclaimService(ctx, e.db, admin, manual.ID)
	if err != nil || res.Action != UnclaimActionDeleted {
		t.Fatalf("期望 DELETED，实际: %+v err=%v", res, err)
	}
	// 未认领服务再取消 -> 冲突；不存在 -> 404
	if _, err := UnclaimService(ctx, e.db, admin, existing.ID); !errors.Is(err, ErrUnclaimNotClaimed) {
		t.Fatalf("未认领服务取消期望 ErrUnclaimNotClaimed，实际: %v", err)
	}
	if _, err := UnclaimService(ctx, e.db, admin, 99999999); !errors.Is(err, ErrModuleServiceNotFound) {
		t.Fatalf("不存在服务期望 ErrModuleServiceNotFound，实际: %v", err)
	}
}

func TestClaimServices_InboxGroupsUnclaimedMultiHostDrift_OnlySharedAuthority(t *testing.T) {
	e := newInboxEnv(t)
	ctx := context.Background()

	// 未认领单宿主 (ap1)
	u1 := e.newUnclaimedSvc(t, "inbox.v1.Single")
	e.place(t, u1.ID, e.proc1.ID, models.PlacementStatusActive)
	// 多宿主 (ap1 + ap2)
	m1 := e.newUnclaimedSvc(t, "inbox.v1.Multi")
	e.place(t, m1.ID, e.proc1.ID, models.PlacementStatusActive)
	e.place(t, m1.ID, e.proc2.ID, models.PlacementStatusActive)
	// 仅 PRIVATE 放置：绝不出现
	pv := e.newUnclaimedSvc(t, "inbox.v1.PrivOnly")
	e.place(t, pv.ID, e.procPriv.ID, models.PlacementStatusActive)
	// STALE 放置：不出现
	st := e.newUnclaimedSvc(t, "inbox.v1.StaleOnly")
	e.place(t, st.ID, e.proc1.ID, models.PlacementStatusStale)
	// 放置漂移：已认领 modC(ap2)，但 ACTIVE 放置在 ap1
	dr := e.newClaimedSvc(t, "inbox.v1.Drift", e.modC.ID)
	e.place(t, dr.ID, e.proc1.ID, models.PlacementStatusActive)

	resp, err := BuildArchInbox(ctx, e.db, InboxQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("BuildArchInbox 失败: %v", err)
	}
	if resp.Summary.UnclaimedCount != 1 || resp.Summary.MultiHostCount != 1 || resp.Summary.PlacementDriftCount != 1 {
		t.Fatalf("summary 不符: %+v", resp.Summary)
	}
	if resp.Summary.TotalPending != resp.Summary.UnclaimedCount+resp.Summary.MultiHostCount+resp.Summary.PlacementDriftCount+resp.Summary.DepSuggestionCount {
		t.Fatalf("total_pending 应为各项之和: %+v", resp.Summary)
	}
	names := map[string][]string{} // ap code -> "kind:name"
	for _, g := range resp.ProcessGroups {
		for _, it := range g.Items {
			names[g.ArchProcessCode] = append(names[g.ArchProcessCode], it.Kind+":"+it.ServiceName)
			if it.ServiceName == "inbox.v1.PrivOnly" || it.ServiceName == "inbox.v1.StaleOnly" {
				t.Fatalf("PRIVATE/STALE 服务不得出现在收件箱: %+v", it)
			}
			for _, pl := range it.Placements {
				if pl.AgentName == e.privAgent.Name {
					t.Fatalf("PRIVATE 环境放置不得披露")
				}
			}
		}
	}
	// ap1 组：Single、Multi、Drift；ap2 组：Multi
	if len(names["ap-m32-1"]) != 3 || len(names["ap-m32-2"]) != 1 {
		t.Fatalf("分组不符: %+v", names)
	}

	// kind 过滤 + arch_process_id 过滤 + search
	multiOnly, err := BuildArchInbox(ctx, e.db, InboxQuery{Kind: InboxKindMultiHost, Page: 1, PageSize: 50})
	if err != nil || len(multiOnly.ProcessGroups) != 2 {
		t.Fatalf("MULTI_HOST 过滤应覆盖 2 个进程分组: %+v err=%v", multiOnly, err)
	}
	ap2Only, err := BuildArchInbox(ctx, e.db, InboxQuery{ArchProcessID: e.ap2.ID, Page: 1, PageSize: 50})
	if err != nil || len(ap2Only.ProcessGroups) != 1 || ap2Only.ProcessGroups[0].ArchProcessCode != "ap-m32-2" {
		t.Fatalf("arch_process_id 过滤不符: %+v err=%v", ap2Only, err)
	}
	searched, err := BuildArchInbox(ctx, e.db, InboxQuery{Search: "single", Page: 1, PageSize: 50})
	if err != nil || len(searched.ProcessGroups) != 1 || len(searched.ProcessGroups[0].Items) != 1 {
		t.Fatalf("search 过滤不符: %+v err=%v", searched, err)
	}
	// 分页：按进程分组分页
	page2, err := BuildArchInbox(ctx, e.db, InboxQuery{Page: 2, PageSize: 1})
	if err != nil || page2.Total != 2 || len(page2.ProcessGroups) != 1 || page2.ProcessGroups[0].ArchProcessCode != "ap-m32-2" {
		t.Fatalf("分页不符: %+v err=%v", page2, err)
	}
}
