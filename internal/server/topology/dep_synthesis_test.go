package topology

import (
	"context"
	"errors"
	"testing"
	"time"

	"code-contract/models"
	"gorm.io/datatypes"
)

// seedSession 创建录制会话
func (e *inboxEnv) seedSession(t *testing.T, no, scope string) models.RecordingSession {
	t.Helper()
	s := models.RecordingSession{
		SessionNo: no, Name: no, Scope: scope, OwnerID: e.modOwner.ID, Status: models.SessionStatusCompleted,
		LeasedAgentIDs: datatypes.JSON("[]"), TargetProcessIDs: datatypes.JSON("[]"),
	}
	mustCreate(t, e.db, &s)
	return s
}

// seedEgress 写入一条出站流量；src/dst 模块可空
func (e *inboxEnv) seedTraffic(t *testing.T, sess models.RecordingSession, seq int64, procID uint64, dir, svcName string, src, dst *uint64) {
	t.Helper()
	now := time.Now().UTC()
	rec := models.TrafficRecord{
		CreatedAt: now, SessionID: sess.ID, SeqNo: seq, AgentID: e.sharedAgent.ID, ProcessID: procID,
		CapturedPID: uint32(2000 + seq), ConnKey: "conn-m32", Direction: dir, ExchangeType: models.ExchangeUnary,
		SrcAddr: "10.50.1.1:50001", DstAddr: "10.50.1.1:31001", ReqCapturedAtNs: now.UnixNano() + seq,
		TransportProto: 1, SNO: models.U64Numeric(1000 + seq), ServiceName: svcName, Operation: "Op",
		SrcModuleID: src, DstModuleID: dst, IsReplayable: true,
	}
	if src != nil {
		rec.SrcAttributionMode = AttributionTraceSpan
	}
	mustCreate(t, e.db, &rec)
}

func TestDepSynthesis_MultiModuleConsumerReturnsCandidates_AndStage2Prefill(t *testing.T) {
	e := newInboxEnv(t)
	ctx := context.Background()

	// 提供方：modA 认领服务 S（ap1 放置）；同时 modA 绑定 ap1
	e.db.Model(&models.Module{}).Where("id = ?", e.modA.ID).Update("arch_process_id", e.ap1.ID)
	svc := e.newClaimedSvc(t, "dep.v1.Provider", e.modA.ID)
	e.place(t, svc.ID, e.proc1.ID, models.PlacementStatusActive)

	// 消费方进程 ap2 承载 modC + modC2（多模块）
	modC2 := models.Module{SubsystemID: e.sub.ID, GroupID: e.fg.ID, Code: "M32_MOD_C2", Name: "模块C2", ArchProcessID: &e.ap2.ID}
	mustCreate(t, e.db, &modC2)
	e.subscribe(t, e.proc2.ID, svc.ID, svc.ServiceName, models.PlacementStatusActive)

	// Stage 2 流量：modC2 4 条、modC 1 条（SHARED 会话）；modC 另有 10 条 PRIVATE 会话流量（不得影响推荐）
	shared := e.seedSession(t, "DS_SHARED", models.ScopeShared)
	priv := e.seedSession(t, "DS_PRIV", models.ScopePrivate)
	for i := int64(1); i <= 4; i++ {
		e.seedTraffic(t, shared, i, e.proc2.ID, models.TrafficDirEgress, svc.ServiceName, &modC2.ID, &e.modA.ID)
	}
	e.seedTraffic(t, shared, 5, e.proc2.ID, models.TrafficDirEgress, svc.ServiceName, &e.modC.ID, &e.modA.ID)
	for i := int64(1); i <= 10; i++ {
		e.seedTraffic(t, priv, i, e.proc2.ID, models.TrafficDirEgress, svc.ServiceName, &e.modC.ID, &e.modA.ID)
	}

	// 噪声：PRIVATE agent 订阅边、RETIRED 订阅边、未认领服务订阅边 均不得产生建议
	privSvc := e.newClaimedSvc(t, "dep.v1.PrivProv", e.modB.ID)
	e.subscribe(t, e.procPriv.ID, privSvc.ID, privSvc.ServiceName, models.PlacementStatusActive)
	retSvc := e.newClaimedSvc(t, "dep.v1.RetiredProv", e.modB.ID)
	e.subscribe(t, e.proc2.ID, retSvc.ID, retSvc.ServiceName, models.PlacementStatusRetired)
	unclSvc := e.newUnclaimedSvc(t, "dep.v1.Unclaimed")
	e.subscribe(t, e.proc2.ID, unclSvc.ID, unclSvc.ServiceName, models.PlacementStatusActive)

	sugs, err := SynthesizeDependencySuggestions(ctx, e.db)
	if err != nil {
		t.Fatalf("SynthesizeDependencySuggestions 失败: %v", err)
	}
	if len(sugs) != 1 {
		t.Fatalf("应仅有 1 条建议（PRIVATE/RETIRED/未认领均排除），实际 %d: %+v", len(sugs), sugs)
	}
	s := sugs[0]
	if s.ConsumerArchProcessID != e.ap2.ID || s.ServiceID != svc.ID || s.ProviderModuleID != e.modA.ID {
		t.Fatalf("建议主键不符: %+v", s)
	}
	if len(s.CandidateConsumerModules) != 2 {
		t.Fatalf("多模块消费方应列出全部候选 (2)，实际 %d", len(s.CandidateConsumerModules))
	}
	if s.RecommendedConsumerModuleID == nil || *s.RecommendedConsumerModuleID != modC2.ID || s.RecommendationSource != RecommendSourceStage2Traffic {
		t.Fatalf("应按 SHARED Stage 2 归因推荐 modC2: %+v", s)
	}

	// 单模块消费方 (ap1 仅 modA... 构造 ap3 单模块) -> SINGLE_MODULE 推荐
	ap3 := models.ArchProcess{GroupID: e.pgrp.ID, Code: "ap-m32-3", Name: "M32 进程3", Lifecycle: models.ArchProcLifecycleConfirmed}
	mustCreate(t, e.db, &ap3)
	proc3 := models.Process{AgentID: e.sharedAgent.ID, ArchProcessID: ap3.ID, EffectiveListenPort: 31009, ExtraDownstreams: datatypes.JSON("[]"),
		EnrollSource: models.EnrollSourceDiscovery, ProbeDesiredState: models.ProbeDesiredStateDisarmed, ProbeStatus: models.ProbeStatusDisarmed}
	mustCreate(t, e.db, &proc3)
	modSolo := models.Module{SubsystemID: e.sub.ID, GroupID: e.fg.ID, Code: "M32_SOLO", Name: "独占模块", ArchProcessID: &ap3.ID}
	mustCreate(t, e.db, &modSolo)
	e.subscribe(t, proc3.ID, svc.ID, svc.ServiceName, models.PlacementStatusActive)

	sugs, err = SynthesizeDependencySuggestions(ctx, e.db)
	if err != nil || len(sugs) != 2 {
		t.Fatalf("应有 2 条建议: %d err=%v", len(sugs), err)
	}
	var solo *DepSuggestionDTO
	for i := range sugs {
		if sugs[i].ConsumerArchProcessID == ap3.ID {
			solo = &sugs[i]
		}
	}
	if solo == nil || solo.RecommendedConsumerModuleID == nil || *solo.RecommendedConsumerModuleID != modSolo.ID || solo.RecommendationSource != RecommendSourceSingleModule {
		t.Fatalf("单模块消费方应推荐唯一模块: %+v", solo)
	}

	// 无业务模块的消费进程不产生建议：删除 modSolo 的进程绑定
	e.db.Model(&models.Module{}).Where("id = ?", modSolo.ID).Update("arch_process_id", nil)
	sugs, _ = SynthesizeDependencySuggestions(ctx, e.db)
	if len(sugs) != 1 {
		t.Fatalf("消费进程无承载模块时不应产生建议，实际 %d", len(sugs))
	}
}

func TestDepSynthesis_DecideApproveRunsLayerAndCycleGuard_IgnoreSuppressesNextQuery(t *testing.T) {
	e := newInboxEnv(t)
	ctx := context.Background()
	admin := e.op(e.adminUser, true)

	e.db.Model(&models.Module{}).Where("id = ?", e.modA.ID).Update("arch_process_id", e.ap1.ID)
	svc := e.newClaimedSvc(t, "dec.v1.Provider", e.modA.ID)
	e.place(t, svc.ID, e.proc1.ID, models.PlacementStatusActive)
	modC2 := models.Module{SubsystemID: e.sub.ID, GroupID: e.fg.ID, Code: "M32_DEC_C2", Name: "模块C2", ArchProcessID: &e.ap2.ID}
	mustCreate(t, e.db, &modC2)
	e.subscribe(t, e.proc2.ID, svc.ID, svc.ServiceName, models.PlacementStatusActive)

	// 参数校验
	if _, err := DecideDependencySuggestion(ctx, e.db, admin, DecideInput{ConsumerArchProcessID: e.ap2.ID, ServiceID: svc.ID, Decision: "NOPE"}); !errors.Is(err, ErrDecideInvalidInput) {
		t.Fatalf("非法 decision 期望 ErrDecideInvalidInput，实际: %v", err)
	}
	// 多模块消费方 APPROVE 未指定 consumer_module_id -> 400
	if _, err := DecideDependencySuggestion(ctx, e.db, admin, DecideInput{ConsumerArchProcessID: e.ap2.ID, ServiceID: svc.ID, Decision: DecisionApprove}); !errors.Is(err, ErrDecideConsumerModuleRequired) {
		t.Fatalf("多模块 APPROVE 缺 consumer_module_id 期望 ErrDecideConsumerModuleRequired，实际: %v", err)
	}
	// consumer_module_id 不属于消费进程 -> 400
	if _, err := DecideDependencySuggestion(ctx, e.db, admin, DecideInput{ConsumerArchProcessID: e.ap2.ID, ServiceID: svc.ID, Decision: DecisionApprove, ConsumerModuleID: e.modB.ID}); !errors.Is(err, ErrDecideConsumerModuleNotInProcess) {
		t.Fatalf("越进程 consumer_module_id 期望 ErrDecideConsumerModuleNotInProcess，实际: %v", err)
	}
	if _, err := DecideDependencySuggestion(ctx, e.db, admin, DecideInput{ConsumerArchProcessID: 987654, ServiceID: svc.ID, Decision: DecisionIgnore}); !errors.Is(err, ErrArchProcessNotFound) {
		t.Fatalf("不存在进程期望 ErrArchProcessNotFound，实际: %v", err)
	}

	// 越层 409：consumer(modC 层级 1) < provider(modA 层级 5)
	e.db.Model(&models.Module{}).Where("id = ?", e.modC.ID).Update("layer_level", 1)
	e.db.Model(&models.Module{}).Where("id = ?", e.modA.ID).Update("layer_level", 5)
	_, err := DecideDependencySuggestion(ctx, e.db, admin, DecideInput{ConsumerArchProcessID: e.ap2.ID, ServiceID: svc.ID, Decision: DecisionApprove, ConsumerModuleID: e.modC.ID})
	var layerErr *LayerViolationError
	if !errors.As(err, &layerErr) || layerErr.Code != ErrCodeLayerViolation {
		t.Fatalf("越层批准期望 ERR_LAYER_VIOLATION，实际: %v", err)
	}
	var depCnt int64
	e.db.Model(&models.ModuleDependency{}).Count(&depCnt)
	if depCnt != 0 {
		t.Fatalf("越层批准失败不得落库 ModuleDependency，实际 %d", depCnt)
	}
	e.db.Model(&models.Module{}).Where("id IN ?", []uint64{e.modC.ID, e.modA.ID}).Update("layer_level", 0)

	// 成环 409：modA 已依赖 modC2 的服务，批准 modC2 -> modA 的服务 将成环
	c2Svc := e.newClaimedSvc(t, "dec.v1.C2Provider", modC2.ID)
	mustCreate(t, e.db, &models.ModuleDependency{ConsumerModuleID: e.modA.ID, ServiceID: &c2Svc.ID, ProviderModuleID: models.Uint64Ptr(modC2.ID)})
	_, err = DecideDependencySuggestion(ctx, e.db, admin, DecideInput{ConsumerArchProcessID: e.ap2.ID, ServiceID: svc.ID, Decision: DecisionApprove, ConsumerModuleID: modC2.ID})
	var cycErr *CyclicDependencyError
	if !errors.As(err, &cycErr) || cycErr.Code != ErrCodeCyclicDependency {
		t.Fatalf("成环批准期望 ERR_CYCLIC_DEPENDENCY，实际: %v", err)
	}

	// 合法批准 modC -> svc：创建 ModuleDependency，建议仅剩 modC2 候选
	res, err := DecideDependencySuggestion(ctx, e.db, admin, DecideInput{ConsumerArchProcessID: e.ap2.ID, ServiceID: svc.ID, Decision: DecisionApprove, ConsumerModuleID: e.modC.ID})
	if err != nil || res.ModuleDependencyID == nil || res.Decision != "APPROVED" {
		t.Fatalf("合法批准失败: %+v err=%v", res, err)
	}
	var md models.ModuleDependency
	if err := e.db.First(&md, *res.ModuleDependencyID).Error; err != nil || md.ConsumerModuleID != e.modC.ID || md.ServiceID == nil || *md.ServiceID != svc.ID || md.ProviderModuleID == nil || *md.ProviderModuleID != e.modA.ID {
		t.Fatalf("ModuleDependency 不符: %+v err=%v", md, err)
	}
	sugs, _ := SynthesizeDependencySuggestions(ctx, e.db)
	if len(sugs) != 1 || len(sugs[0].CandidateConsumerModules) != 1 || sugs[0].CandidateConsumerModules[0].ModuleID != modC2.ID {
		t.Fatalf("批准后候选应剩 modC2: %+v", sugs)
	}
	// 批准幂等：重复批准返回既有依赖
	res2, err := DecideDependencySuggestion(ctx, e.db, admin, DecideInput{ConsumerArchProcessID: e.ap2.ID, ServiceID: svc.ID, Decision: DecisionApprove, ConsumerModuleID: e.modC.ID})
	if err != nil || res2.ModuleDependencyID == nil || *res2.ModuleDependencyID != *res.ModuleDependencyID {
		t.Fatalf("重复批准应幂等: %+v err=%v", res2, err)
	}

	// IGNORE modC2 -> 落表 #34 且不再复现
	ign, err := DecideDependencySuggestion(ctx, e.db, admin, DecideInput{ConsumerArchProcessID: e.ap2.ID, ServiceID: svc.ID, Decision: DecisionIgnore, ConsumerModuleID: modC2.ID, Rationale: "测试用途"})
	if err != nil || ign.Decision != models.DepDecisionIgnored {
		t.Fatalf("IGNORE 失败: %+v err=%v", ign, err)
	}
	var dd models.DependencyDecision
	if err := e.db.Where("consumer_module_id = ? AND service_id = ?", modC2.ID, svc.ID).First(&dd).Error; err != nil || dd.Decision != models.DepDecisionIgnored || dd.Reason != "测试用途" || dd.DecidedBy != e.adminUser.ID {
		t.Fatalf("DependencyDecision 不符: %+v err=%v", dd, err)
	}
	sugs, _ = SynthesizeDependencySuggestions(ctx, e.db)
	if len(sugs) != 0 {
		t.Fatalf("IGNORE 后不应再推荐: %+v", sugs)
	}
	// Upsert：改标 VIOLATION，仍唯一一行
	if _, err := DecideDependencySuggestion(ctx, e.db, admin, DecideInput{ConsumerArchProcessID: e.ap2.ID, ServiceID: svc.ID, Decision: DecisionViolation, ConsumerModuleID: modC2.ID}); err != nil {
		t.Fatalf("VIOLATION 失败: %v", err)
	}
	var ddCnt int64
	e.db.Model(&models.DependencyDecision{}).Where("service_id = ?", svc.ID).Count(&ddCnt)
	_ = e.db.Where("consumer_module_id = ? AND service_id = ?", modC2.ID, svc.ID).First(&dd).Error
	if ddCnt != 1 || dd.Decision != models.DepDecisionViolation {
		t.Fatalf("应 Upsert 为 VIOLATION 且唯一: cnt=%d %+v", ddCnt, dd)
	}

	// 进程级 IGNORE（不指定 consumer_module_id）对进程下全部模块生效
	svcB := e.newClaimedSvc(t, "dec.v1.ProviderB", e.modA.ID)
	e.place(t, svcB.ID, e.proc1.ID, models.PlacementStatusActive)
	e.subscribe(t, e.proc2.ID, svcB.ID, svcB.ServiceName, models.PlacementStatusActive)
	sugs, _ = SynthesizeDependencySuggestions(ctx, e.db)
	if len(sugs) != 1 || sugs[0].ServiceID != svcB.ID {
		t.Fatalf("应出现 svcB 新建议: %+v", sugs)
	}
	if _, err := DecideDependencySuggestion(ctx, e.db, admin, DecideInput{ConsumerArchProcessID: e.ap2.ID, ServiceID: svcB.ID, Decision: DecisionIgnore}); err != nil {
		t.Fatalf("进程级 IGNORE 失败: %v", err)
	}
	e.db.Model(&models.DependencyDecision{}).Where("service_id = ?", svcB.ID).Count(&ddCnt)
	if ddCnt != 2 {
		t.Fatalf("进程级 IGNORE 应覆盖 ap2 全部 2 个模块，实际 %d 行", ddCnt)
	}
	sugs, _ = SynthesizeDependencySuggestions(ctx, e.db)
	if len(sugs) != 0 {
		t.Fatalf("进程级 IGNORE 后不应再推荐: %+v", sugs)
	}
}
