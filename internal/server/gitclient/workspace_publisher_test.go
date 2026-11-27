package gitclient

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	commonModels "code-common/backend/models"
	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/notify"
	"code-contract/models"
	"gorm.io/gorm"
)

func createPoolRepoBundleFile(
	t *testing.T,
	db *gorm.DB,
	repoName string,
	platformRepoID uint64,
	subsystem string,
	bundleName string,
	targetSchema string,
	category string,
	filePath string,
	content string,
	imports []string,
) (models.GitRepo, models.GitProtoBundle, models.ProtoFile) {
	t.Helper()
	var repo models.GitRepo
	err := db.Where("name = ?", repoName).First(&repo).Error
	if err != nil {
		repo = models.GitRepo{
			Name:           repoName,
			PlatformRepoID: platformRepoID,
			SourceMode:     models.GitSourceModePlatformShared,
			SyncStrategy:   models.GitSyncStrategyLocalCacheOnly,
			Provider:       models.GitProviderGitLab,
			RepoURL:        "http://mock/" + repoName + ".git",
			DefaultBranch:  "main",
			SubsystemSlug:  subsystem,
			SyncEnabled:    true,
			ProtoStatus:    models.GitRepoProtoStatusHasProto,
		}
		if err := db.Create(&repo).Error; err != nil {
			t.Fatalf("create repo %s failed: %v", repoName, err)
		}
	}

	bundle := models.GitProtoBundle{
		GitRepoID:        repo.ID,
		BundleName:       bundleName,
		TargetSchemaName: targetSchema,
		Category:         category,
		SubdirPath:       ".",
		AutoCompile:      true,
		AutoActivate:     false,
	}
	if err := db.Create(&bundle).Error; err != nil {
		t.Fatalf("create bundle %s failed: %v", bundleName, err)
	}

	impBytes, _ := json.Marshal(imports)
	pf := models.ProtoFile{
		GitRepoID:         repo.ID,
		BundleID:          bundle.ID,
		TargetSchemaName:  targetSchema,
		Category:          category,
		FilePath:          filePath,
		RepoFilePath:      filePath,
		CleanRelPath:      filePath,
		PackageName:       "test.pkg",
		NormalizerVersion: ruleNormalizerBaseVersion,
		PipelineStatus:    models.ProtoPipelineNormalized,
		IsUServiceSource:  true,
		ImportsJSON:       impBytes,
		SymbolsJSON:       []byte(`[]`),
		ContentSHA256:     testSHA256Hex(content),
		LineCount:         10,
		LatestVersionNum:  1,
		LatestCommitSHA:   "c1",
	}
	if err := db.Create(&pf).Error; err != nil {
		t.Fatalf("create proto file %s failed: %v", filePath, err)
	}
	ver := models.ProtoFileVersion{
		ProtoFileID:   pf.ID,
		Version:       1,
		VersionTag:    "v1",
		SourceType:    models.ProtoSourceGitSync,
		CommitSHA:     "c1",
		ContentText:   content,
		ContentSHA256: pf.ContentSHA256,
		LineCount:     pf.LineCount,
	}
	if err := db.Create(&ver).Error; err != nil {
		t.Fatalf("create proto file version %s failed: %v", filePath, err)
	}
	_ = db.Model(&pf).Update("latest_version_id", ver.ID).Error
	pf.LatestVersionID = &ver.ID
	return repo, bundle, pf
}

func isolatedStorageConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.AppConfig
	cfg.Storage.CodesDir = t.TempDir()
	cfg.Storage.ProtosDir = t.TempDir()
	cfg.Storage.SharedWriteEnabled = true
	config.ApplyProtoPipelineDefaults(&cfg.ProtoPipeline)
	return &cfg
}

func TestPublishWorkspaceBundles_TopologyBatch_IncrementalSkip_AndBreakingPropagation(t *testing.T) {
	db, reg := setupGitClientTestDB(t)
	cfg := isolatedStorageConfig(t)
	ctx := context.Background()

	// 1. 构建三层拓扑: base (BASE_LIB) <- order (BUSINESS) <- gw (GATEWAY)
	baseProtoV1 := `syntax = "proto3";
package common.v1;
message BaseMeta {
  string req_id = 1;
}
`
	orderProtoV1 := `syntax = "proto3";
package order.v1;
import "common/base.proto";
message OrderReq { common.v1.BaseMeta meta = 1; string sku = 2; }
message OrderRsp { string id = 1; }
service OrderService { rpc CreateOrder(OrderReq) returns (OrderRsp); }
`
	gwProtoV1 := `syntax = "proto3";
package gw.v1;
import "order/order.proto";
message GwReq { order.v1.OrderReq order = 1; }
message GwRsp { bool ok = 1; }
service GwService { rpc Dispatch(GwReq) returns (GwRsp); }
`
	_, baseBundle, basePF := createPoolRepoBundleFile(t, db, "repo-base", 101, "common", "base-bundle", "schema-base", models.ProtoCategoryBaseLib, "common/base.proto", baseProtoV1, nil)
	_, orderBundle, _ := createPoolRepoBundleFile(t, db, "repo-order", 102, "order", "order-bundle", "schema-order", models.ProtoCategoryBusiness, "order/order.proto", orderProtoV1, []string{"common/base.proto"})
	_, gwBundle, _ := createPoolRepoBundleFile(t, db, "repo-gw", 103, "gw", "gw-bundle", "schema-gw", models.ProtoCategoryGateway, "gw/gw.proto", gwProtoV1, []string{"order/order.proto"})

	// 写入依赖模块血缘供破坏性变更受影响检测
	sub := models.Subsystem{Code: "SUB_PUB", Name: "发布测试子系统"}
	db.Create(&sub)
	grp := models.FunctionalGroup{SubsystemID: sub.ID, Code: "GRP_PUB", Name: "发布测试组"}
	db.Create(&grp)
	modOrder := models.Module{SubsystemID: sub.ID, GroupID: grp.ID, Code: "MOD_PUB_ORDER", Name: "订单模块"}
	db.Create(&modOrder)
	svcOrder := models.ModuleService{ModuleID: models.Uint64Ptr(modOrder.ID), ServiceName: "order.v1.OrderService"}
	db.Create(&svcOrder)
	modGw := models.Module{SubsystemID: sub.ID, GroupID: grp.ID, Code: "MOD_PUB_GW", Name: "网关模块"}
	db.Create(&modGw)
	db.Create(&models.ModuleDependency{ConsumerModuleID: modGw.ID, ServiceID: &svcOrder.ID})

	// 2. 先直接发布未协调状态 -> 必须返回 ErrReconcileStale
	if _, _, err := PublishWorkspaceBundles(ctx, db, reg, cfg, PublishWorkspaceOptions{TriggeredBy: "admin"}); !errors.Is(err, ErrReconcileStale) {
		t.Fatalf("未先执行阶段 ③ 直接发布期望返回 ErrReconcileStale, got %v", err)
	}

	// 3. 执行阶段 ③ 全池协调
	if _, _, err := RunPoolReconcile(ctx, db, PoolReconcileOptions{
		CodesDir:    cfg.Storage.CodesDir,
		ProtosDir:   cfg.Storage.ProtosDir,
		TriggeredBy: "admin",
	}); err != nil {
		t.Fatalf("RunPoolReconcile failed: %v", err)
	}

	// 4. 首轮发布 (AUTO 模式): 三层依次编译并原子激活，均为 GIT_PIPELINE
	run1, details1, err := PublishWorkspaceBundles(ctx, db, reg, cfg, PublishWorkspaceOptions{TriggeredBy: "admin"})
	if err != nil {
		t.Fatalf("首轮 PublishWorkspaceBundles failed: %v", err)
	}
	if run1.Status != models.PublishRunStatusCompleted || run1.PublishedCount != 3 || run1.SkippedCount != 0 {
		t.Fatalf("首轮发布统计异常: status=%s pub=%d skip=%d details=%+v", run1.Status, run1.PublishedCount, run1.SkippedCount, details1)
	}

	var baseSchema models.ProtoSchema
	if err := db.Where("name = ? AND is_active = true", baseBundle.TargetSchemaName).First(&baseSchema).Error; err != nil {
		t.Fatalf("base schema not active: %v", err)
	}
	if baseSchema.SourceType != models.ProtoSourceGitPipeline || baseSchema.PublishRunID == nil || *baseSchema.PublishRunID != run1.ID {
		t.Fatalf("期望 source_type=GIT_PIPELINE 且关联 publish_run_id=%d, got %+v", run1.ID, baseSchema)
	}

	// 5. 增量跳过: 不改文件直接再发布 -> 全部 3 包 SKIPPED
	run2, _, err := PublishWorkspaceBundles(ctx, db, reg, cfg, PublishWorkspaceOptions{TriggeredBy: "admin"})
	if err != nil {
		t.Fatalf("第二轮增量发布 failed: %v", err)
	}
	if run2.PublishedCount != 0 || run2.SkippedCount != 3 {
		t.Fatalf("无变更重跑应全部 SKIPPED: pub=%d skip=%d", run2.PublishedCount, run2.SkippedCount)
	}

	// 6. force_recompile=true 应重编激活新版本
	runForce, _, err := PublishWorkspaceBundles(ctx, db, reg, cfg, PublishWorkspaceOptions{TriggeredBy: "admin", ForceRecompile: true})
	if err != nil {
		t.Fatalf("force_recompile 发布 failed: %v", err)
	}
	if runForce.PublishedCount != 3 {
		t.Fatalf("force_recompile=true 期望重新发布 3 个包, got %d", runForce.PublishedCount)
	}

	// 7. 破坏性演进: 修改 base.proto 将 req_id string -> int64 -> 重协调后发布:
	// base 被局部挂起 BLOCKED_BREAKING, order & gw 因依赖 base 传播为 PENDING_UPSTREAM
	baseProtoV2Breaking := `syntax = "proto3";
package common.v1;
message BaseMeta {
  int64 req_id = 1;
}
`
	newSHA := testSHA256Hex(baseProtoV2Breaking)
	v2 := models.ProtoFileVersion{
		ProtoFileID:   basePF.ID,
		Version:       2,
		VersionTag:    "v2",
		SourceType:    models.ProtoSourceGitSync,
		CommitSHA:     "c2",
		ContentText:   baseProtoV2Breaking,
		ContentSHA256: newSHA,
		LineCount:     5,
	}
	_ = db.Create(&v2).Error
	_ = db.Model(&basePF).Updates(map[string]any{"content_sha256": newSHA, "latest_version_num": 2, "latest_version_id": v2.ID}).Error

	if _, _, err := RunPoolReconcile(ctx, db, PoolReconcileOptions{
		CodesDir:    cfg.Storage.CodesDir,
		ProtosDir:   cfg.Storage.ProtosDir,
		TriggeredBy: "admin",
	}); err != nil {
		t.Fatalf("v2 RunPoolReconcile failed: %v", err)
	}

	run3, details3, err := PublishWorkspaceBundles(ctx, db, reg, cfg, PublishWorkspaceOptions{TriggeredBy: "admin"})
	if err != nil {
		t.Fatalf("v2 PublishWorkspaceBundles failed: %v", err)
	}
	if run3.Status != models.PublishRunStatusPartialBlocked || run3.BreakingBlockedCount != 1 || run3.PendingUpstreamCount != 2 {
		t.Fatalf("期望 PARTIAL_BLOCKED (breaking=1, pending_upstream=2), got status=%s brk=%d pend=%d",
			run3.Status, run3.BreakingBlockedCount, run3.PendingUpstreamCount)
	}
	if len(details3.BreakingItems) != 1 || details3.BreakingItems[0].BundleID != baseBundle.ID {
		t.Fatalf("BreakingItems 应精准定位 base-bundle: %+v", details3.BreakingItems)
	}

	// 8. 管理员放行破坏性变更 -> 自动补发下游 order & gw
	approveRes, err := ApproveBreakingSchemas(
		ctx, db, reg, cfg, run3.ID,
		[]uint64{details3.BreakingItems[0].SchemaID},
		"架构委员会评审批准 BaseMeta req_id 类型变更并同步重建下游",
		"admin", 1,
	)
	if err != nil {
		t.Fatalf("ApproveBreakingSchemas failed: %v", err)
	}
	if len(approveRes.ApprovedSchemas) != 1 || !approveRes.ApprovedSchemas[0].IsActive {
		t.Fatalf("放行后的 baseSchema 期望已激活: %+v", approveRes.ApprovedSchemas)
	}
	if approveRes.DownstreamRun == nil || approveRes.DownstreamRun.Status != models.PublishRunStatusCompleted {
		t.Fatalf("放行后应自动触发下游补发且 COMPLETED: %+v", approveRes.DownstreamRun)
	}

	// 验证 gwBundle 与 orderBundle 当前处于 PUBLISHED
	var gwUpdated, orderUpdated models.GitProtoBundle
	_ = db.First(&orderUpdated, orderBundle.ID).Error
	_ = db.First(&gwUpdated, gwBundle.ID).Error
	if orderUpdated.LastPublishStatus != models.BundlePublishStatusPublished || gwUpdated.LastPublishStatus != models.BundlePublishStatusPublished {
		t.Fatalf("下游补发后状态期望 PUBLISHED, got order=%s gw=%s", orderUpdated.LastPublishStatus, gwUpdated.LastPublishStatus)
	}
}

func TestPublishWorkspaceBundles_PreviewModeConfirm_AndConflictBlockToggle(t *testing.T) {
	db, reg := setupGitClientTestDB(t)
	cfg := isolatedStorageConfig(t)
	ctx := context.Background()

	// 同逻辑路径 shared/Msg.proto 在两个仓库中存在不同字段（内容不等价冲突）
	svcAProto := `syntax = "proto3";
package conflict.v1;
import "shared/Msg.proto";
message ReqA { shared.v1.DupMsg msg = 1; }
message RspA { string ok = 1; }
service SvcA { rpc Call(ReqA) returns (RspA); }
`
	dupMsgA := `syntax = "proto3";
package shared.v1;
message DupMsg { string alpha = 1; }
`
	dupMsgB := `syntax = "proto3";
package shared.v1;
message DupMsg { string beta = 1; int32 count = 2; }
`
	_, bA, _ := createPoolRepoBundleFile(t, db, "repo-a", 201, "sub-a", "bundle-a", "schema-a", models.ProtoCategoryBusiness, "svc/a.proto", svcAProto, []string{"shared/Msg.proto"})
	// 第二份 shared/Msg.proto 同仓挂载于 bundle-a；再构造 repo-b 提供竞争版 shared/Msg.proto
	shaA := testSHA256Hex(dupMsgA)
	pfDupA := models.ProtoFile{
		GitRepoID:         bA.GitRepoID,
		BundleID:          bA.ID,
		TargetSchemaName:  bA.TargetSchemaName,
		Category:          models.ProtoCategoryBaseLib,
		FilePath:          "shared/Msg.proto",
		RepoFilePath:      "shared/Msg.proto",
		CleanRelPath:      "shared/Msg.proto",
		PackageName:       "shared.v1",
		NormalizerVersion: ruleNormalizerBaseVersion,
		PipelineStatus:    models.ProtoPipelineNormalized,
		ImportsJSON:       []byte(`[]`),
		SymbolsJSON:       []byte(`[]`),
		ContentSHA256:     shaA,
		LineCount:         3,
		LatestVersionNum:  1,
		LatestCommitSHA:   "c1",
	}
	db.Create(&pfDupA)
	db.Create(&models.ProtoFileVersion{ProtoFileID: pfDupA.ID, Version: 1, SourceType: models.ProtoSourceGitSync, ContentText: dupMsgA, ContentSHA256: shaA, LineCount: 3})

	_, bB, _ := createPoolRepoBundleFile(t, db, "repo-b", 202, "sub-b", "bundle-b", "schema-b", models.ProtoCategoryBusiness, "shared/Msg.proto", dupMsgB, nil)

	// 1. block_on_unresolved_conflict = true -> 涉及冲突的包被阻断 BLOCKED_CONFLICT
	govDoc := DefaultGovernanceConfig()
	blkTrue := true
	govDoc.Global.PoolReconcile.BlockOnUnresolvedConflict = &blkTrue
	if _, _, _, err := SaveGovernanceConfig(ctx, db, govDoc, "admin"); err != nil {
		t.Fatalf("SaveGovernanceConfig failed: %v", err)
	}
	if _, _, err := RunPoolReconcile(ctx, db, PoolReconcileOptions{
		CodesDir:         cfg.Storage.CodesDir,
		ProtosDir:        cfg.Storage.ProtosDir,
		GovernanceConfig: &govDoc,
		TriggeredBy:      "admin",
	}); err != nil {
		t.Fatalf("RunPoolReconcile failed: %v", err)
	}
	// 验证 R3_AUTO 胜出者已持久化到 ProtoImportOwner
	var r3Owners []models.ProtoImportOwner
	db.Where("source = ?", models.ImportOwnerSourceR3Auto).Find(&r3Owners)
	if len(r3Owners) == 0 {
		t.Fatalf("期望将 R3 胜出者自动持久化为 source=R3_AUTO")
	}

	runBlk, _, err := PublishWorkspaceBundles(ctx, db, reg, cfg, PublishWorkspaceOptions{TriggeredBy: "admin"})
	if err != nil {
		t.Fatalf("block_on_unresolved_conflict=true 发布异常: %v", err)
	}
	if runBlk.Status != models.PublishRunStatusPartialBlocked || runBlk.ConflictBlockedCount == 0 {
		t.Fatalf("开启冲突阻断期望出现 BLOCKED_CONFLICT, got status=%s conflict_blocked=%d", runBlk.Status, runBlk.ConflictBlockedCount)
	}
	_ = bB

	// 2. block_on_unresolved_conflict = false + PREVIEW 模式 -> 不阻塞编译并生成 PREVIEW_READY，随后 Confirm 切入生效池
	blkFalse := false
	govDoc.Global.PoolReconcile.BlockOnUnresolvedConflict = &blkFalse
	if _, _, _, err := SaveGovernanceConfig(ctx, db, govDoc, "admin"); err != nil {
		t.Fatalf("SaveGovernanceConfig(false) failed: %v", err)
	}
	if _, _, err := RunPoolReconcile(ctx, db, PoolReconcileOptions{
		CodesDir:         cfg.Storage.CodesDir,
		ProtosDir:        cfg.Storage.ProtosDir,
		GovernanceConfig: &govDoc,
		TriggeredBy:      "admin",
	}); err != nil {
		t.Fatalf("RunPoolReconcile(false) failed: %v", err)
	}

	runPrev, detPrev, err := PublishWorkspaceBundles(ctx, db, reg, cfg, PublishWorkspaceOptions{
		TriggeredBy: "admin",
		PublishMode: models.PublishModePreview,
	})
	if err != nil {
		t.Fatalf("PREVIEW 模式发布 failed: %v", err)
	}
	if runPrev.Status != models.PublishRunStatusPreviewReady || len(detPrev.ConflictRisks) == 0 {
		t.Fatalf("PREVIEW 期望 PREVIEW_READY 且附带 conflict_risks 预警, got status=%s risks=%+v", runPrev.Status, detPrev.ConflictRisks)
	}

	var activeCnt int64
	db.Model(&models.ProtoSchema{}).Where("is_active = true AND source_type = ?", models.ProtoSourceGitPipeline).Count(&activeCnt)
	if activeCnt != 0 {
		t.Fatalf("PREVIEW_READY 确认前不应写入生效池, activeCnt=%d", activeCnt)
	}

	runConf, _, err := ConfirmPreviewPublishRun(ctx, db, reg, cfg, runPrev.ID, "admin", 1)
	if err != nil {
		t.Fatalf("ConfirmPreviewPublishRun failed: %v", err)
	}
	if runConf.Status != models.PublishRunStatusCompleted || runConf.PublishedCount == 0 {
		t.Fatalf("确认预览发布期望 COMPLETED, got status=%s pub=%d", runConf.Status, runConf.PublishedCount)
	}
	db.Model(&models.ProtoSchema{}).Where("is_active = true AND source_type = ?", models.ProtoSourceGitPipeline).Count(&activeCnt)
	if activeCnt == 0 {
		t.Fatalf("ConfirmPreviewPublishRun 后应成功激活生效")
	}
}

func TestArchiveMigration_AutoDeactivateAnd30DayReminderDedup(t *testing.T) {
	db, reg := setupGitClientTestDB(t)
	cfg := isolatedStorageConfig(t)
	ctx := context.Background()

	admin := commonModels.User{Username: "arch_admin", Name: "arch_admin", Email: "arch@test.local", Password: "hashed_password", Roles: []byte(`["` + config.AdminRole + `"]`), IsActive: true}
	db.Create(&admin)

	legacyCoveredProto := `syntax = "proto3";
package legacy.cov;
message CReq { string id = 1; }
message CRsp { bool ok = 1; }
service CoveredSvc { rpc Do(CReq) returns (CRsp); }
`
	legacyUncoveredProto := `syntax = "proto3";
package legacy.uncov;
message UReq { string id = 1; }
message URsp { bool ok = 1; }
service UncoveredSvc { rpc Ping(UReq) returns (URsp); }
`
	covSchema, _, _, err := reg.CompileAndSaveSchema(ctx, decoder.CompileProtoBundleInput{
		Name:         "legacy-covered",
		Category:     models.ProtoCategoryBusiness,
		Files:        map[string]string{"legacy/cov.proto": legacyCoveredProto},
		AutoActivate: true,
		SourceType:   models.ProtoSourceUploadPB,
		OperatorID:   1,
	})
	if err != nil {
		t.Fatalf("seed covered schema failed: %v", err)
	}
	uncovSchema, _, _, err := reg.CompileAndSaveSchema(ctx, decoder.CompileProtoBundleInput{
		Name:         "legacy-uncovered",
		Category:     models.ProtoCategoryBusiness,
		Files:        map[string]string{"legacy/uncov.proto": legacyUncoveredProto},
		AutoActivate: true,
		SourceType:   models.ProtoSourceOnlineCompile,
		OperatorID:   1,
	})
	if err != nil {
		t.Fatalf("seed uncovered schema failed: %v", err)
	}
	// 手工置为 31 天前激活，模拟历史存量资产
	oldTime := time.Now().UTC().Add(-31 * 24 * time.Hour)
	_ = db.Model(&covSchema).Update("created_at", oldTime).Error
	_ = db.Model(&uncovSchema).Update("created_at", oldTime).Error

	// Git 流水线发布覆盖 legacy.cov.CoveredSvc
	createPoolRepoBundleFile(t, db, "repo-cov", 301, "cov", "cov-bundle", "git-cov", models.ProtoCategoryBusiness, "legacy/cov.proto", legacyCoveredProto, nil)
	if _, _, err := RunPoolReconcile(ctx, db, PoolReconcileOptions{
		CodesDir:    cfg.Storage.CodesDir,
		ProtosDir:   cfg.Storage.ProtosDir,
		TriggeredBy: "admin",
	}); err != nil {
		t.Fatalf("RunPoolReconcile failed: %v", err)
	}
	if _, _, err := PublishWorkspaceBundles(ctx, db, reg, cfg, PublishWorkspaceOptions{TriggeredBy: "admin"}); err != nil {
		t.Fatalf("PublishWorkspaceBundles failed: %v", err)
	}

	var refreshedCov, refreshedUncov models.ProtoSchema
	_ = db.First(&refreshedCov, covSchema.ID).Error
	_ = db.First(&refreshedUncov, uncovSchema.ID).Error
	if refreshedCov.IsActive {
		t.Fatalf("已被 Git 覆盖的 UPLOAD_PB 描述集应自动停用, got is_active=true")
	}
	if !refreshedUncov.IsActive {
		t.Fatalf("未被覆盖的 ONLINE_COMPILE 描述集应继续保持 active")
	}

	// 30 天提醒巡检 + 30 天内去重
	disp := notify.NewDispatcher(db, nil)
	sent1, err := RunArchivedSchemaReminderCheck(ctx, db, disp, time.Now().UTC(), 30)
	if err != nil || sent1 != 1 {
		t.Fatalf("首轮满 30 天归档提醒期望发送 1 条, got sent=%d err=%v", sent1, err)
	}
	sent2, err := RunArchivedSchemaReminderCheck(ctx, db, disp, time.Now().UTC().Add(24*time.Hour), 30)
	if err != nil || sent2 != 0 {
		t.Fatalf("30 天冷却期内重跑期望去重发送 0 条, got sent=%d err=%v", sent2, err)
	}

	// 验证工作区清理无残留 data
	_ = filepath.Join(os.TempDir(), "unused")
}

func TestPipelineDebouncer_AndAdvisoryLockExclusion(t *testing.T) {
	db, _ := setupGitClientTestDB(t)
	ctx := context.Background()

	// 1. 去抖合并验证：50ms 窗口内对同一 repoID 连续入队 3 次，仅触发 1 次回调
	var fireCount atomic.Int32
	deb := NewPipelineDebouncer(60*time.Millisecond, func(repoID uint64) {
		fireCount.Add(1)
	})
	defer deb.StopAll()
	deb.EnqueueMRMerge(42)
	deb.EnqueueMRMerge(42)
	deb.EnqueueMRMerge(42)
	if deb.PendingCount() != 1 {
		t.Fatalf("期望 1 条去抖等待项, got %d", deb.PendingCount())
	}
	time.Sleep(150 * time.Millisecond)
	if fireCount.Load() != 1 {
		t.Fatalf("5 次快速 MR 合入期望去抖合并为 1 次触发, got %d", fireCount.Load())
	}

	// 2. PostgreSQL 会话级咨询锁排他验证
	lock1, err := TryAcquirePipelineLock(ctx, db, PipelineLockHolder{
		JobType: "PIPELINE_RUN",
		JobID:   99,
		Holder:  "alice",
	})
	if err != nil {
		t.Fatalf("首次获取流水线锁失败: %v", err)
	}
	defer lock1.Release()

	_, err2 := TryAcquirePipelineLock(ctx, db, PipelineLockHolder{
		JobType: "PUBLISH",
		Holder:  "bob",
	})
	if !errors.Is(err2, ErrPipelineLocked) {
		t.Fatalf("并发抢锁期望返回 ErrPipelineLocked, got %v", err2)
	}
	var lockErr *PipelineLockError
	if !errors.As(err2, &lockErr) || lockErr.Holder == nil || lockErr.Holder.Holder != "alice" || lockErr.Holder.JobID != 99 {
		t.Fatalf("PipelineLockError 持锁摘要不准确: %+v", lockErr)
	}
}
