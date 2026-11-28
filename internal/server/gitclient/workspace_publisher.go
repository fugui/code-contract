package gitclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"
	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/protodoc"
	"code-contract/internal/server/topology"
	"code-contract/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const workspacePublisherCompilerVersion = "protocompile-v0.14.1-adr29"

var (
	// ErrReconcileStale 表示全池协调后输入文件发生变化或尚无有效收敛结果，拒绝发布。
	ErrReconcileStale = errors.New("全池收敛结果已过期或不存在，请先重新执行阶段 ③ 全池收敛")
	// ErrPublishRunNotFound 表示未找到指定的阶段 ④ 发布记录。
	ErrPublishRunNotFound = errors.New("未找到指定的发布批次记录")
	// ErrInvalidBreakingReason 表示放行破坏性变更时未提供足够长度的原因。
	ErrInvalidBreakingReason = errors.New("强制放行破坏性变更必须填写不少于 8 个字符的原因 (breaking_reason)")
	// ErrPublishRunNotPreviewReady 表示当前发布批次非 PREVIEW_READY 状态，不可执行预览确认生效。
	ErrPublishRunNotPreviewReady = errors.New("仅处于 PREVIEW_READY 状态的发布批次支持确认生效")
)

// PublishWorkspaceOptions 控制阶段 ④ 单仓拓扑编译发布行为。
type PublishWorkspaceOptions struct {
	BundleIDs      []uint64
	Subsystem      string
	ForceRecompile bool
	PublishMode    string // 为空时读取 cfg.ProtoPipeline.EffectivePublishMode()
	TriggeredBy    string
	OperatorID     uint
	AlreadyLocked  bool
	ProgressFn     func(PublishProgressEvent)
}

// PublishProgressEvent 为 SSE proto_publish.progress 事件载荷。
type PublishProgressEvent struct {
	RunID                uint64 `json:"run_id"`
	Stage                string `json:"stage"`
	Status               string `json:"status"`
	Message              string `json:"message"`
	TotalBundles         int    `json:"total_bundles"`
	ProcessedBundles     int    `json:"processed_bundles"`
	PublishedCount       int    `json:"published_count"`
	SkippedCount         int    `json:"skipped_count"`
	BreakingBlockedCount int    `json:"breaking_blocked_count"`
	ConflictBlockedCount int    `json:"conflict_blocked_count"`
	PendingUpstreamCount int    `json:"pending_upstream_count"`
	FailedCount          int    `json:"failed_count"`
}

// PublishBreakingItem 描述阶段 ④ 被破坏性门禁局部挂起的单仓详情。
type PublishBreakingItem struct {
	BundleID           uint64                          `json:"bundle_id"`
	BundleName         string                          `json:"bundle_name"`
	TargetSchemaName   string                          `json:"target_schema_name"`
	SchemaID           uint64                          `json:"schema_id"`
	Version            int                             `json:"version"`
	BreakingRules      []decoder.BreakingRuleViolation `json:"breaking_rules"`
	AffectedDependents []decoder.AffectedDependent     `json:"affected_dependents,omitempty"`
}

// PublishConflictRiskItem 描述涉及未人工裁决内容不等价冲突的发布项。
type PublishConflictRiskItem struct {
	BundleID         uint64   `json:"bundle_id"`
	BundleName       string   `json:"bundle_name"`
	TargetSchemaName string   `json:"target_schema_name"`
	LogicalPaths     []string `json:"logical_paths"`
	Blocked          bool     `json:"blocked"`
}

// PublishBundleResultItem 记录单个 Bundle 在阶段 ④ 中的处置结论。
type PublishBundleResultItem struct {
	BundleID         uint64   `json:"bundle_id"`
	GitRepoID        uint64   `json:"git_repo_id"`
	RepoName         string   `json:"repo_name"`
	SubsystemSlug    string   `json:"subsystem_slug"`
	BundleName       string   `json:"bundle_name"`
	TargetSchemaName string   `json:"target_schema_name"`
	Category         string   `json:"category"`
	Layer            int      `json:"layer"`
	Status           string   `json:"status"`
	SchemaID         *uint64  `json:"schema_id,omitempty"`
	ActiveSchemaID   *uint64  `json:"active_schema_id,omitempty"`
	AutoResolvedRisk bool     `json:"auto_resolved_risk,omitempty"`
	BlockedByBundles []string `json:"blocked_by_bundles,omitempty"`
	Reason           string   `json:"reason,omitempty"`
}

// PublishRunDetails 存储于 ProtoPublishRun.DetailsJSON 中的结构化报告。
type PublishRunDetails struct {
	PublishMode          string                    `json:"publish_mode"`
	PoolReconcileRunID   uint64                    `json:"pool_reconcile_run_id"`
	InputFingerprint     string                    `json:"input_fingerprint"`
	BlockOnConflict      bool                      `json:"block_on_unresolved_conflict"`
	EstimatedSeconds     int                       `json:"estimated_seconds"`
	AutoDeactivatedIDs   []uint64                  `json:"auto_deactivated_archive_ids,omitempty"`
	BreakingAuditCount   int                       `json:"breaking_audit_count,omitempty"`
	BreakingItems        []PublishBreakingItem     `json:"breaking_items,omitempty"`
	ConflictRisks        []PublishConflictRiskItem `json:"conflict_risks,omitempty"`
	PendingUpstreamItems []PublishBundleResultItem `json:"pending_upstream_items,omitempty"`
	BundleResults        []PublishBundleResultItem `json:"bundle_results"`
	LayerBatches         [][]uint64                `json:"layer_batches,omitempty"`
}

// ApproveBreakingResult 返回放行破坏性变更及下游补发结果。
type ApproveBreakingResult struct {
	ApprovedSchemas    []models.ProtoSchema    `json:"approved_schemas"`
	DownstreamRun      *models.ProtoPublishRun `json:"downstream_publish_run,omitempty"`
	AutoDeactivatedIDs []uint64                `json:"auto_deactivated_archive_ids,omitempty"`
}

// PublishWorkspaceBundles 执行阶段 ④ 单仓拓扑编译、破坏性门禁、挂起传播、原子激活与文档重建 (ADR-29 §2.3.3)。
func PublishWorkspaceBundles(
	ctx context.Context,
	db *gorm.DB,
	reg *decoder.Registry,
	cfg *config.Config,
	opts PublishWorkspaceOptions,
) (*models.ProtoPublishRun, *PublishRunDetails, error) {
	if db == nil {
		return nil, nil, errors.New("db 不能为空")
	}
	if reg == nil {
		return nil, nil, errors.New("decoder.Registry 未初始化")
	}
	if cfg == nil {
		cfg = &config.AppConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if !opts.AlreadyLocked {
		lk, lErr := TryAcquirePipelineLock(ctx, db, PipelineLockHolder{
			JobType: "PUBLISH",
			Holder:  strings.TrimSpace(opts.TriggeredBy),
		})
		if lErr != nil {
			return nil, nil, lErr
		}
		defer lk.Release()
	}

	startedAt := time.Now().UTC()
	publishMode := strings.ToUpper(strings.TrimSpace(opts.PublishMode))
	if publishMode != models.PublishModePreview {
		publishMode = cfg.ProtoPipeline.EffectivePublishMode()
	}

	// 1. 校验最近一次成功收敛记录与实时输入指纹
	var latestReconcile models.ProtoPoolReconcileRun
	_ = db.WithContext(ctx).
		Where("status = ?", models.PoolReconcileStatusCompleted).
		Order("id DESC").
		Limit(1).
		Find(&latestReconcile).Error

	liveFP, fpErr := ComputePoolInputFingerprint(ctx, db)
	if fpErr != nil {
		return nil, nil, fpErr
	}

	if latestReconcile.ID == 0 || latestReconcile.InputFingerprint == "" || latestReconcile.InputFingerprint != liveFP {
		fin := time.Now().UTC()
		rejected := models.ProtoPublishRun{
			PoolReconcileRunID: latestReconcile.ID,
			PublishMode:        publishMode,
			Status:             models.PublishRunStatusRejectedStale,
			ErrorMessage:       ErrReconcileStale.Error(),
			TriggeredBy:        strings.TrimSpace(opts.TriggeredBy),
			DurationMs:         fin.Sub(startedAt).Milliseconds(),
			StartedAt:          startedAt,
			FinishedAt:         &fin,
		}
		_ = db.WithContext(ctx).Create(&rejected).Error
		return &rejected, nil, ErrReconcileStale
	}

	// 解析治理配置（含 block_on_unresolved_conflict 与最新收敛报告）
	blockOnConflict := false
	if snap, gErr := LoadActiveGovernanceConfig(ctx, db); gErr == nil && snap != nil {
		blockOnConflict = snap.Doc.Global.EffectivePoolReconcile().EffectiveBlockOnUnresolvedConflict()
	}
	var reconcileReport PoolReconcileReport
	if len(latestReconcile.ReportJSON) > 0 {
		_ = json.Unmarshal(latestReconcile.ReportJSON, &reconcileReport)
	}

	run := models.ProtoPublishRun{
		PoolReconcileRunID: latestReconcile.ID,
		PublishMode:        publishMode,
		Status:             models.PublishRunStatusRunning,
		TriggeredBy:        strings.TrimSpace(opts.TriggeredBy),
		StartedAt:          startedAt,
	}
	if err := db.WithContext(ctx).Create(&run).Error; err != nil {
		return nil, nil, err
	}
	if !opts.AlreadyLocked {
		SetActivePipelineLockJobID(run.ID)
	}

	emit := func(stage, msg string, processed int) {
		if opts.ProgressFn == nil {
			return
		}
		opts.ProgressFn(PublishProgressEvent{
			RunID:                run.ID,
			Stage:                stage,
			Status:               run.Status,
			Message:              msg,
			TotalBundles:         run.TotalBundles,
			ProcessedBundles:     processed,
			PublishedCount:       run.PublishedCount,
			SkippedCount:         run.SkippedCount,
			BreakingBlockedCount: run.BreakingBlockedCount,
			ConflictBlockedCount: run.ConflictBlockedCount,
			PendingUpstreamCount: run.PendingUpstreamCount,
			FailedCount:          run.FailedCount,
		})
	}

	failRun := func(cause error) (*models.ProtoPublishRun, *PublishRunDetails, error) {
		fin := time.Now().UTC()
		run.Status = models.PublishRunStatusFailed
		run.ErrorMessage = cause.Error()
		run.DurationMs = fin.Sub(startedAt).Milliseconds()
		run.FinishedAt = &fin
		_ = db.WithContext(ctx).Save(&run).Error
		emit("FAILED", cause.Error(), 0)
		return &run, nil, cause
	}

	// 2. 加载参与全池图与发布的仓库、Bundle、NORMALIZED 非外部副本文件与 owners
	var repos []models.GitRepo
	if err := db.WithContext(ctx).
		Where("sync_enabled = true OR platform_repo_id = ?", SystemSharedBasePlatformRepoID).
		Order("id ASC").
		Find(&repos).Error; err != nil {
		return failRun(err)
	}
	repoByID := make(map[uint64]models.GitRepo, len(repos))
	repoIDs := make([]uint64, 0, len(repos))
	for _, r := range repos {
		repoByID[r.ID] = r
		repoIDs = append(repoIDs, r.ID)
	}

	var allBundles []models.GitProtoBundle
	if len(repoIDs) > 0 {
		if err := db.WithContext(ctx).Where("git_repo_id IN ?", repoIDs).Order("id ASC").Find(&allBundles).Error; err != nil {
			return failRun(err)
		}
	}
	bundleByID := make(map[uint64]models.GitProtoBundle, len(allBundles))
	for _, b := range allBundles {
		bundleByID[b.ID] = b
	}

	var allFiles []models.ProtoFile
	if len(repoIDs) > 0 {
		if err := db.WithContext(ctx).
			Where("git_repo_id IN ? AND is_deleted = false AND pipeline_status = ? AND is_external_copy = false",
				repoIDs, models.ProtoPipelineNormalized).
			Order("id ASC").
			Find(&allFiles).Error; err != nil {
			return failRun(err)
		}
	}

	verIDs := make([]uint64, 0, len(allFiles))
	for _, f := range allFiles {
		if f.LatestVersionID != nil && *f.LatestVersionID > 0 {
			verIDs = append(verIDs, *f.LatestVersionID)
		}
	}
	verByFile := make(map[uint64]models.ProtoFileVersion, len(allFiles))
	if len(verIDs) > 0 {
		var vers []models.ProtoFileVersion
		if err := db.WithContext(ctx).Where("id IN ?", verIDs).Find(&vers).Error; err != nil {
			return failRun(err)
		}
		for _, v := range vers {
			verByFile[v.ProtoFileID] = v
		}
	}
	var missingVerFileIDs []uint64
	for _, f := range allFiles {
		if _, ok := verByFile[f.ID]; !ok {
			missingVerFileIDs = append(missingVerFileIDs, f.ID)
		}
	}
	if len(missingVerFileIDs) > 0 {
		var fallbackVers []models.ProtoFileVersion
		if err := db.WithContext(ctx).
			Where("proto_file_id IN ?", missingVerFileIDs).
			Order("proto_file_id ASC, version DESC, id DESC").
			Find(&fallbackVers).Error; err != nil {
			return failRun(err)
		}
		for _, v := range fallbackVers {
			if _, ok := verByFile[v.ProtoFileID]; !ok {
				verByFile[v.ProtoFileID] = v
			}
		}
	}

	var owners []models.ProtoImportOwner
	_ = db.WithContext(ctx).Find(&owners).Error
	ownerByPath := make(map[string]models.ProtoImportOwner, len(owners))
	for _, ow := range owners {
		ownerByPath[ow.LogicalPath] = ow
	}

	filesByBundle := make(map[uint64][]models.ProtoFile)
	fileByLogical := make(map[string]models.ProtoFile, len(allFiles))
	for _, f := range allFiles {
		filesByBundle[f.BundleID] = append(filesByBundle[f.BundleID], f)
		if cur, exists := fileByLogical[f.FilePath]; !exists {
			fileByLogical[f.FilePath] = f
		} else if ow, hasOwner := ownerByPath[f.FilePath]; hasOwner && ow.OwnerBundleID == f.BundleID {
			fileByLogical[f.FilePath] = f
		} else {
			_ = cur
		}
	}

	// 构建全池文件级 DAG，并与 import_owners 保持 100% 同源归属
	graph := BuildFileDependencyGraph(allFiles)
	for lp, ow := range ownerByPath {
		if ow.OwnerBundleID > 0 {
			graph.FileBundle[lp] = ow.OwnerBundleID
		}
	}

	// 确定目标发布 Bundle 集合
	filterBundleSet := make(map[uint64]struct{}, len(opts.BundleIDs))
	for _, id := range opts.BundleIDs {
		if id > 0 {
			filterBundleSet[id] = struct{}{}
		}
	}
	subsysFilter := strings.TrimSpace(opts.Subsystem)

	targetBundleIDs := make([]uint64, 0, len(allBundles))
	for _, b := range allBundles {
		if len(filesByBundle[b.ID]) == 0 {
			continue
		}
		if len(filterBundleSet) > 0 {
			if _, ok := filterBundleSet[b.ID]; !ok {
				continue
			}
		}
		if subsysFilter != "" {
			r := repoByID[b.GitRepoID]
			if !strings.EqualFold(r.SubsystemSlug, subsysFilter) && !strings.EqualFold(r.ExportTargetSubsystem, subsysFilter) {
				continue
			}
		}
		targetBundleIDs = append(targetBundleIDs, b.ID)
	}
	sort.Slice(targetBundleIDs, func(i, j int) bool { return targetBundleIDs[i] < targetBundleIDs[j] })

	// 识别未人工裁决且内容不等价的冲突组关联 Bundle
	bundleConflictPaths := make(map[uint64][]string)
	for _, c := range reconcileReport.Conflicts {
		if c.ContentIdentical {
			continue
		}
		ow, hasOwner := ownerByPath[c.LogicalPath]
		isManualOrBase := hasOwner && (ow.Source == models.ImportOwnerSourceManual || ow.Source == models.ImportOwnerSourceBaseLib)
		if isManualOrBase || c.HasManualOwner {
			continue
		}
		// 胜出方 Bundle 及依赖该路径的下游 Bundle 记入冲突风险
		winBID := c.WinnerBundleID
		if winBID == 0 && hasOwner {
			winBID = ow.OwnerBundleID
		}
		if winBID > 0 {
			bundleConflictPaths[winBID] = appendUniqueStr(bundleConflictPaths[winBID], c.LogicalPath)
		}
		for depFile := range graph.ReverseEdges[c.LogicalPath] {
			if depBID := graph.FileBundle[depFile]; depBID > 0 {
				bundleConflictPaths[depBID] = appendUniqueStr(bundleConflictPaths[depBID], c.LogicalPath)
			}
		}
	}

	// 3. 按 SCC 拓扑分层（BASE_LIB 优先，依赖在被依赖之前）
	sccs := ComputeBundleSCCs(targetBundleIDs, graph)
	bundleToSCC := make(map[uint64]int, len(targetBundleIDs))
	for idx, scc := range sccs {
		for _, bid := range scc {
			bundleToSCC[bid] = idx
		}
	}

	sccDeps := make([]map[int]struct{}, len(sccs))
	bundleDirectDeps := make(map[uint64]map[uint64]struct{}, len(targetBundleIDs))
	for i := range sccs {
		sccDeps[i] = make(map[int]struct{})
	}
	for _, bid := range targetBundleIDs {
		bundleDirectDeps[bid] = make(map[uint64]struct{})
		for _, f := range filesByBundle[bid] {
			for depFile := range graph.ForwardEdges[f.FilePath] {
				depBID, ok := graph.FileBundle[depFile]
				if !ok || depBID == 0 || depBID == bid {
					continue
				}
				bundleDirectDeps[bid][depBID] = struct{}{}
				if depSCC, inTarget := bundleToSCC[depBID]; inTarget {
					curSCC := bundleToSCC[bid]
					if depSCC != curSCC {
						sccDeps[curSCC][depSCC] = struct{}{}
					}
				}
			}
		}
	}

	// 计算每个 SCC 的拓扑层深（BASE_LIB SCC 锚定层 0，其余至少为 max(depLayer)+1）
	sccLayer := make([]int, len(sccs))
	maxLayer := 0
	for i, scc := range sccs {
		hasBase := false
		for _, bid := range scc {
			if bundleByID[bid].Category == models.ProtoCategoryBaseLib {
				hasBase = true
				break
			}
		}
		l := 0
		for depSCC := range sccDeps[i] {
			if sccLayer[depSCC]+1 > l {
				l = sccLayer[depSCC] + 1
			}
		}
		if !hasBase && len(sccDeps[i]) == 0 {
			// 非 BASE_LIB 无依赖层置于 layer 1，确保全局 BASE_LIB (layer 0) 最先处理
			l = 1
		}
		sccLayer[i] = l
		if l > maxLayer {
			maxLayer = l
		}
	}

	layerSCCIndices := make([][]int, maxLayer+1)
	for i, l := range sccLayer {
		layerSCCIndices[l] = append(layerSCCIndices[l], i)
	}

	// 加载当前所有生效 Schema 以比对 compile_input_sha256
	var activeSchemas []models.ProtoSchema
	_ = db.WithContext(ctx).
		Select("id, name, version, source_type, compile_input_sha256, is_active").
		Where("is_active = true").
		Find(&activeSchemas).Error
	activeByName := make(map[string]models.ProtoSchema, len(activeSchemas))
	for _, as := range activeSchemas {
		activeByName[as.Name] = as
	}

	run.TotalBundles = len(targetBundleIDs)
	_ = db.WithContext(ctx).Model(&run).Update("total_bundles", run.TotalBundles).Error
	emit("PREPARING", fmt.Sprintf("拓扑分层完成：共 %d 个契约包，%d 个拓扑层", len(targetBundleIDs), len(layerSCCIndices)), 0)

	// 记录全部 Bundle 状态，便于下游判定上游是否挂起/失败
	bundleStatus := make(map[uint64]string, len(allBundles))
	for _, b := range allBundles {
		if b.LastPublishStatus != "" {
			bundleStatus[b.ID] = b.LastPublishStatus
		} else {
			bundleStatus[b.ID] = models.BundlePublishStatusPending
		}
	}

	details := PublishRunDetails{
		PublishMode:        publishMode,
		PoolReconcileRunID: latestReconcile.ID,
		InputFingerprint:   liveFP,
		BlockOnConflict:    blockOnConflict,
		EstimatedSeconds:   maxInt(1, (len(targetBundleIDs)+3)/4),
		BundleResults:      make([]PublishBundleResultItem, 0, len(targetBundleIDs)),
	}

	conc := cfg.ProtoPipeline.CompileConcurrency
	if conc <= 0 {
		conc = 4
	}
	processedCount := 0
	anyActivated := false
	var autoDeactivatedAll []uint64
	resultByBundle := make(map[uint64]*PublishBundleResultItem, len(targetBundleIDs))

	// 4. 逐拓扑层推进编译、破坏性检查与原子激活
	for layerIdx, sccIdxList := range layerSCCIndices {
		if len(sccIdxList) == 0 {
			continue
		}
		layerBundleIDs := make([]uint64, 0)
		for _, si := range sccIdxList {
			layerBundleIDs = append(layerBundleIDs, sccs[si]...)
		}

		type compileTask struct {
			bundle       models.GitProtoBundle
			repo         models.GitRepo
			filesMap     map[string]string
			inputSHA256  string
			conflictRisk bool
		}
		var compileTasks []compileTask

		for _, bid := range layerBundleIDs {
			b := bundleByID[bid]
			repo := repoByID[b.GitRepoID]
			item := &PublishBundleResultItem{
				BundleID:         b.ID,
				GitRepoID:        b.GitRepoID,
				RepoName:         repo.Name,
				SubsystemSlug:    repo.SubsystemSlug,
				BundleName:       b.BundleName,
				TargetSchemaName: b.TargetSchemaName,
				Category:         b.Category,
				Layer:            layerIdx,
			}
			if curAct, hasAct := activeByName[b.TargetSchemaName]; hasAct {
				idCopy := curAct.ID
				item.ActiveSchemaID = &idCopy
			}
			resultByBundle[bid] = item

			// 4.1 检查未人工裁决的内容不等价冲突
			if conflictPaths := bundleConflictPaths[bid]; len(conflictPaths) > 0 {
				details.ConflictRisks = append(details.ConflictRisks, PublishConflictRiskItem{
					BundleID:         b.ID,
					BundleName:       b.BundleName,
					TargetSchemaName: b.TargetSchemaName,
					LogicalPaths:     conflictPaths,
					Blocked:          blockOnConflict,
				})
				if blockOnConflict {
					item.Status = models.BundlePublishStatusBlockedConflict
					item.Reason = fmt.Sprintf("存在未人工裁决的内容不等价冲突: %s", strings.Join(conflictPaths, ", "))
					bundleStatus[bid] = models.BundlePublishStatusBlockedConflict
					continue
				}
				item.AutoResolvedRisk = true
			}

			// 4.2 检查传递上游（仅依赖 direct deps 即可，因为前面层已传播 PENDING_UPSTREAM；破坏性变更不再阻塞上游/下游）
			var blockingUpstreamNames []string
			for depBID := range bundleDirectDeps[bid] {
				st := bundleStatus[depBID]
				if st == models.BundlePublishStatusBlockedConflict ||
					st == models.BundlePublishStatusFailed ||
					st == models.BundlePublishStatusPendingUpstream {
					if ub, ok := bundleByID[depBID]; ok {
						blockingUpstreamNames = append(blockingUpstreamNames, ub.BundleName)
					} else {
						blockingUpstreamNames = append(blockingUpstreamNames, fmt.Sprintf("bundle#%d", depBID))
					}
				}
			}
			if len(blockingUpstreamNames) > 0 {
				sort.Strings(blockingUpstreamNames)
				item.Status = models.BundlePublishStatusPendingUpstream
				item.BlockedByBundles = blockingUpstreamNames
				item.Reason = fmt.Sprintf("等待上游契约包就绪: %s", strings.Join(blockingUpstreamNames, ", "))
				bundleStatus[bid] = models.BundlePublishStatusPendingUpstream
				continue
			}

			// 4.3 构建编译文件集并计算 compile_input_sha256
			bFiles := filesByBundle[bid]
			filesMap := make(map[string]string, len(bFiles))
			for _, f := range bFiles {
				ver := verByFile[f.ID]
				filesMap[f.FilePath] = ver.ContentText
			}
			compileFP := computeBundleCompileInputSHA256(b, bFiles, fileByLogical, graph)

			if !opts.ForceRecompile {
				if curAct, hasAct := activeByName[b.TargetSchemaName]; hasAct &&
					curAct.CompileInputSHA256 != "" &&
					curAct.CompileInputSHA256 == compileFP &&
					models.IsAllowedGitProtoSource(curAct.SourceType) {
					item.Status = models.BundlePublishStatusSkippedUnchanged
					scID := curAct.ID
					item.SchemaID = &scID
					bundleStatus[bid] = models.BundlePublishStatusSkippedUnchanged
					if b.PendingBreakingSchemaID != nil {
						_ = db.WithContext(ctx).Model(&models.GitProtoBundle{}).Where("id = ?", b.ID).
							Update("pending_breaking_schema_id", nil).Error
					}
					continue
				}
			}

			compileTasks = append(compileTasks, compileTask{
				bundle:       b,
				repo:         repo,
				filesMap:     filesMap,
				inputSHA256:  compileFP,
				conflictRisk: item.AutoResolvedRisk,
			})
		}

		// 4.4 有界并发执行本层单仓编译 + 破坏性检查
		type compiledCandidate struct {
			bundleID uint64
			schema   *models.ProtoSchema
		}
		var (
			mu          sync.Mutex
			wg          sync.WaitGroup
			sem         = make(chan struct{}, conc)
			layerStaged = make([]compiledCandidate, 0, len(compileTasks))
		)
		for _, task := range compileTasks {
			wg.Add(1)
			go func(tk compileTask) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				repoIDCopy := tk.repo.ID
				bundleIDCopy := tk.bundle.ID
				runIDCopy := run.ID
				schema, _, _, compErr := reg.CompileAndSaveSchema(ctx, decoder.CompileProtoBundleInput{
					Name:               tk.bundle.TargetSchemaName,
					Category:           tk.bundle.Category,
					VersionTag:         shortCommitTag(tk.repo.LastSyncCommit),
					Description:        fmt.Sprintf("阶段 ④ 拓扑编译发布 #%d", run.ID),
					Files:              tk.filesMap,
					ProtoIncludeDirs:   tk.bundle.IncludeDirs(),
					EntryPoints:        tk.bundle.EntryFiles(),
					AutoActivate:       false,
					SourceType:         models.ProtoSourceGitPipeline,
					GitRepoID:          &repoIDCopy,
					SourceBundleID:     &bundleIDCopy,
					SourceCommitSHA:    tk.repo.LastSyncCommit,
					CompileInputSHA256: tk.inputSHA256,
					PublishRunID:       &runIDCopy,
					OperatorID:         opts.OperatorID,
					SourceProvider:     decoder.NewDBProtoSourceProvider(db),
				})

				mu.Lock()
				defer mu.Unlock()
				item := resultByBundle[tk.bundle.ID]
				if compErr != nil || schema == nil {
					item.Status = models.BundlePublishStatusFailed
					if compErr != nil {
						item.Reason = compErr.Error()
					} else {
						item.Reason = "编译产出为空"
					}
					bundleStatus[tk.bundle.ID] = models.BundlePublishStatusFailed
					return
				}

				scID := schema.ID
				item.SchemaID = &scID

				// 更新文件版本关联
				verUpdateIDs := make([]uint64, 0, len(filesByBundle[tk.bundle.ID]))
				for _, f := range filesByBundle[tk.bundle.ID] {
					if f.LatestVersionID != nil && *f.LatestVersionID > 0 {
						verUpdateIDs = append(verUpdateIDs, *f.LatestVersionID)
					}
				}
				if len(verUpdateIDs) > 0 {
					_ = db.WithContext(ctx).Model(&models.ProtoFileVersion{}).
						Where("id IN ?", verUpdateIDs).
						Update("schema_id", scID).Error
				}

				// 破坏性变更仅在 PREVIEW 阶段采集预告，不再阻断编译与入库上线（统一归口至规范与 MR 门禁审计）
				if publishMode == models.PublishModePreview {
					if violations, bErr := reg.DetectBreakingChangesAgainstActive(tk.bundle.TargetSchemaName, schema.DescriptorBytes); bErr == nil && len(violations) > 0 {
						affected := decoder.FindAffectedDependents(db, violations)
						details.BreakingItems = append(details.BreakingItems, PublishBreakingItem{
							BundleID:           tk.bundle.ID,
							BundleName:         tk.bundle.BundleName,
							TargetSchemaName:   tk.bundle.TargetSchemaName,
							SchemaID:           schema.ID,
							Version:            schema.Version,
							BreakingRules:      violations,
							AffectedDependents: affected,
						})
					}
				}

				layerStaged = append(layerStaged, compiledCandidate{
					bundleID: tk.bundle.ID,
					schema:   schema,
				})
			}(task)
		}
		wg.Wait()

		// 4.5 SCC 组一致性检查（len(scc) > 1 同进同退）
		stagedByBundle := make(map[uint64]*models.ProtoSchema, len(layerStaged))
		for _, sc := range layerStaged {
			stagedByBundle[sc.bundleID] = sc.schema
		}
		for _, si := range sccIdxList {
			scc := sccs[si]
			if len(scc) <= 1 {
				continue
			}
			sccHasBlock := false
			var blockNames []string
			for _, bid := range scc {
				st := bundleStatus[bid]
				if st == models.BundlePublishStatusBlockedConflict ||
					st == models.BundlePublishStatusFailed ||
					st == models.BundlePublishStatusPendingUpstream {
					sccHasBlock = true
					blockNames = append(blockNames, bundleByID[bid].BundleName)
				}
			}
			if sccHasBlock {
				sort.Strings(blockNames)
				for _, bid := range scc {
					if _, inStaged := stagedByBundle[bid]; inStaged {
						delete(stagedByBundle, bid)
						item := resultByBundle[bid]
						item.Status = models.BundlePublishStatusPendingUpstream
						item.BlockedByBundles = blockNames
						item.Reason = fmt.Sprintf("同强连通依赖组内存在阻塞成员 (%s)，整组撤出待激活批次", strings.Join(blockNames, ", "))
						bundleStatus[bid] = models.BundlePublishStatusPendingUpstream
					}
				}
			}
		}

		// 4.6 本层批量激活或 PREVIEW 暂存
		if len(stagedByBundle) > 0 {
			stagedIDs := make([]uint64, 0, len(stagedByBundle))
			stagedBundleOrder := make([]uint64, 0, len(stagedByBundle))
			bundleMetaMap := make(map[uint64]decoder.BreakingBundleMeta, len(stagedByBundle))
			for bid := range stagedByBundle {
				stagedBundleOrder = append(stagedBundleOrder, bid)
			}
			sort.Slice(stagedBundleOrder, func(i, j int) bool { return stagedBundleOrder[i] < stagedBundleOrder[j] })
			for _, bid := range stagedBundleOrder {
				sc := stagedByBundle[bid]
				stagedIDs = append(stagedIDs, sc.ID)
				b := bundleByID[bid]
				repoIDCopy := b.GitRepoID
				bundleIDCopy := b.ID
				bundleMetaMap[sc.ID] = decoder.BreakingBundleMeta{
					GitRepoID:  &repoIDCopy,
					BundleID:   &bundleIDCopy,
					BundleName: b.BundleName,
				}
			}

			if publishMode == models.PublishModePreview {
				details.LayerBatches = append(details.LayerBatches, stagedIDs)
				for _, bid := range stagedBundleOrder {
					sc := stagedByBundle[bid]
					item := resultByBundle[bid]
					item.Status = models.BundlePublishStatusPending
					item.Reason = "PREVIEW 模式已编译通过，等待管理员确认生效"
					bundleStatus[bid] = models.BundlePublishStatusPending
					_ = db.WithContext(ctx).Model(&models.GitProtoBundle{}).Where("id = ?", bid).Updates(map[string]any{
						"last_schema_id":             sc.ID,
						"pending_breaking_schema_id": nil,
					}).Error
				}
			} else {
				if preDeact := deactivateCoveredArchivedSchemas(ctx, db, reg, stagedIDs, opts.TriggeredBy); len(preDeact) > 0 {
					autoDeactivatedAll = appendUniqueUint64(autoDeactivatedAll, preDeact...)
				}
				observedBySchema := make(map[uint64]decoder.SchemaBreakingObservation)
				runIDCopy := run.ID
				auditCtx := &decoder.BreakingAuditContext{
					Source:         models.BreakingSourcePipelinePublish,
					PublishRunID:   &runIDCopy,
					TriggeredBy:    opts.TriggeredBy,
					BundleBySchema: bundleMetaMap,
					OnObserved: func(obs decoder.SchemaBreakingObservation) {
						observedBySchema[obs.ToSchemaID] = obs
					},
				}
				activated, _, actErr := reg.BatchActivate(stagedIDs, decoder.BatchActivateOptions{
					SkipReload:      true,
					AllowLargeBatch: true,
					BreakingAudit:   auditCtx,
				})
				if actErr != nil {
					// 回退为逐个激活，隔离冲突/残缺 Bundle
					for _, bid := range stagedBundleOrder {
						sc := stagedByBundle[bid]
						_, actOne, _, oneErr := reg.ActivateSchemaVersion(sc.ID, decoder.BatchActivateOptions{
							SkipReload:    true,
							BreakingAudit: auditCtx,
						})
						item := resultByBundle[bid]
						if oneErr != nil {
							item.Status = models.BundlePublishStatusFailed
							item.Reason = fmt.Sprintf("激活失败: %v", oneErr)
							bundleStatus[bid] = models.BundlePublishStatusFailed
							continue
						}
						anyActivated = true
						item.Status = models.BundlePublishStatusPublished
						item.ActiveSchemaID = &actOne.ID
						if obs, ok := observedBySchema[actOne.ID]; ok {
							item.Reason = fmt.Sprintf("已发布生效（检出 %d 项破坏性变更，已归档至规范与 MR 门禁审计）", len(obs.BreakingRules))
							details.BreakingAuditCount++
							b := bundleByID[bid]
							details.BreakingItems = append(details.BreakingItems, PublishBreakingItem{
								BundleID:           b.ID,
								BundleName:         b.BundleName,
								TargetSchemaName:   b.TargetSchemaName,
								SchemaID:           actOne.ID,
								Version:            actOne.Version,
								BreakingRules:      obs.BreakingRules,
								AffectedDependents: obs.AffectedDependents,
							})
						}
						bundleStatus[bid] = models.BundlePublishStatusPublished
						activeByName[actOne.Name] = *actOne
						_, _ = protodoc.RebuildForSchema(db, actOne)
						_ = db.WithContext(ctx).Model(&models.GitProtoBundle{}).Where("id = ?", bid).Updates(map[string]any{
							"last_schema_id":             actOne.ID,
							"pending_breaking_schema_id": nil,
						}).Error
					}
				} else {
					anyActivated = true
					actByID := make(map[uint64]models.ProtoSchema, len(activated))
					for i := range activated {
						actByID[activated[i].ID] = activated[i]
					}
					for _, bid := range stagedBundleOrder {
						sc := stagedByBundle[bid]
						act := actByID[sc.ID]
						if act.ID == 0 {
							act = *sc
							act.IsActive = true
						}
						item := resultByBundle[bid]
						item.Status = models.BundlePublishStatusPublished
						item.ActiveSchemaID = &act.ID
						if obs, ok := observedBySchema[act.ID]; ok {
							item.Reason = fmt.Sprintf("已发布生效（检出 %d 项破坏性变更，已归档至规范与 MR 门禁审计）", len(obs.BreakingRules))
							details.BreakingAuditCount++
							b := bundleByID[bid]
							details.BreakingItems = append(details.BreakingItems, PublishBreakingItem{
								BundleID:           b.ID,
								BundleName:         b.BundleName,
								TargetSchemaName:   b.TargetSchemaName,
								SchemaID:           act.ID,
								Version:            act.Version,
								BreakingRules:      obs.BreakingRules,
								AffectedDependents: obs.AffectedDependents,
							})
						}
						bundleStatus[bid] = models.BundlePublishStatusPublished
						activeByName[act.Name] = act
						_, _ = protodoc.RebuildForSchema(db, &act)
						_ = db.WithContext(ctx).Model(&models.GitProtoBundle{}).Where("id = ?", bid).Updates(map[string]any{
							"last_schema_id":             act.ID,
							"pending_breaking_schema_id": nil,
						}).Error
					}
				}
			}
		}

		// 回写本层各 Bundle 发布状态与统计
		for _, bid := range layerBundleIDs {
			processedCount++
			item := resultByBundle[bid]
			if item == nil {
				continue
			}
			switch item.Status {
			case models.BundlePublishStatusPublished:
				run.PublishedCount++
			case models.BundlePublishStatusSkippedUnchanged:
				run.SkippedCount++
			case models.BundlePublishStatusBlockedBreaking:
				run.BreakingBlockedCount++
			case models.BundlePublishStatusBlockedConflict:
				run.ConflictBlockedCount++
			case models.BundlePublishStatusPendingUpstream:
				run.PendingUpstreamCount++
				details.PendingUpstreamItems = append(details.PendingUpstreamItems, *item)
			case models.BundlePublishStatusFailed:
				run.FailedCount++
			}
			_ = db.WithContext(ctx).Model(&models.GitProtoBundle{}).Where("id = ?", bid).Updates(map[string]any{
				"last_publish_status": item.Status,
				"last_publish_run_id": run.ID,
			}).Error
		}
		emit("PUBLISHING", fmt.Sprintf("拓扑层 %d/%d 完成", layerIdx+1, len(layerSCCIndices)), processedCount)
	}

	// 5. 收尾：单次 Registry.Reload + 归档覆盖自动停用 + 异步统一目录撮合
	if anyActivated {
		_ = reg.Reload()
		if postDeact := deactivateCoveredArchivedSchemas(ctx, db, reg, nil, opts.TriggeredBy); len(postDeact) > 0 {
			autoDeactivatedAll = appendUniqueUint64(autoDeactivatedAll, postDeact...)
		}
		details.AutoDeactivatedIDs = autoDeactivatedAll
		go func() {
			corrCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if _, cErr := topology.CorrelateCatalogServices(corrCtx, db, nil); cErr != nil {
				log.Printf("[WorkspacePublisher] 异步刷新统一目录撮合失败: %v", cErr)
			}
		}()
	}

	for _, bid := range targetBundleIDs {
		if item := resultByBundle[bid]; item != nil {
			details.BundleResults = append(details.BundleResults, *item)
		}
	}

	fin := time.Now().UTC()
	run.FanoutCount = run.PublishedCount + run.BreakingBlockedCount + run.FailedCount
	switch {
	case publishMode == models.PublishModePreview && run.FailedCount == 0 && run.BreakingBlockedCount == 0 && run.ConflictBlockedCount == 0:
		run.Status = models.PublishRunStatusPreviewReady
	case run.BreakingBlockedCount > 0 || run.ConflictBlockedCount > 0 || run.PendingUpstreamCount > 0 || run.FailedCount > 0:
		if run.PublishedCount == 0 && run.SkippedCount == 0 && run.FailedCount > 0 {
			run.Status = models.PublishRunStatusFailed
		} else {
			run.Status = models.PublishRunStatusPartialBlocked
		}
	default:
		run.Status = models.PublishRunStatusCompleted
	}
	detailsBytes, _ := json.Marshal(details)
	run.DetailsJSON = datatypes.JSON(detailsBytes)
	run.DurationMs = fin.Sub(startedAt).Milliseconds()
	run.FinishedAt = &fin

	if err := db.WithContext(ctx).Save(&run).Error; err != nil {
		return nil, &details, err
	}
	emit("COMPLETED", fmt.Sprintf("阶段 ④ 发布完成（生效 %d，跳过 %d，破坏性审计 %d，等待上游 %d，失败 %d）",
		run.PublishedCount, run.SkippedCount, details.BreakingAuditCount, run.PendingUpstreamCount, run.FailedCount), processedCount)

	return &run, &details, nil
}

// ApproveBreakingSchemas 放行阶段 ④ 被破坏性变更挂起的草稿版本，重建文档并自动为其 PENDING_UPSTREAM 下游补发增量发布。
func ApproveBreakingSchemas(
	ctx context.Context,
	db *gorm.DB,
	reg *decoder.Registry,
	cfg *config.Config,
	runID uint64,
	schemaIDs []uint64,
	breakingReason string,
	operatorName string,
	operatorID uint,
) (*ApproveBreakingResult, error) {
	if db == nil || reg == nil {
		return nil, errors.New("db 或 registry 未初始化")
	}
	if cfg == nil {
		cfg = &config.AppConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reason := strings.TrimSpace(breakingReason)
	if len(reason) < 8 {
		return nil, ErrInvalidBreakingReason
	}
	if len(schemaIDs) == 0 {
		return nil, errors.New("schema_ids 不能为空")
	}

	var pubRun models.ProtoPublishRun
	if err := db.WithContext(ctx).First(&pubRun, runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPublishRunNotFound
		}
		return nil, err
	}

	lk, lErr := TryAcquirePipelineLock(ctx, db, PipelineLockHolder{
		JobType: "APPROVE_BREAKING",
		JobID:   runID,
		Holder:  strings.TrimSpace(operatorName),
	})
	if lErr != nil {
		return nil, lErr
	}
	defer lk.Release()

	preDeactivated := deactivateCoveredArchivedSchemas(ctx, db, reg, schemaIDs, operatorName)
	activated, _, err := reg.BatchActivate(schemaIDs, decoder.BatchActivateOptions{
		AllowBreaking:   true,
		BreakingReason:  reason,
		AllowLargeBatch: true,
	})
	if err != nil {
		return nil, err
	}

	approvedBundleIDs := make([]uint64, 0, len(activated))
	for i := range activated {
		_, _ = protodoc.RebuildForSchema(db, &activated[i])
		if activated[i].SourceBundleID != nil && *activated[i].SourceBundleID > 0 {
			bid := *activated[i].SourceBundleID
			approvedBundleIDs = append(approvedBundleIDs, bid)
			_ = db.WithContext(ctx).Model(&models.GitProtoBundle{}).Where("id = ?", bid).Updates(map[string]any{
				"last_schema_id":             activated[i].ID,
				"last_publish_status":        models.BundlePublishStatusPublished,
				"pending_breaking_schema_id": nil,
			}).Error
		}
	}

	autoDeactivated := appendUniqueUint64(preDeactivated, deactivateCoveredArchivedSchemas(ctx, db, reg, nil, operatorName)...)

	// 收集等待这些上游的 PENDING_UPSTREAM 下游 Bundle 自动补发增量发布
	var pendingBundles []models.GitProtoBundle
	_ = db.WithContext(ctx).
		Where("last_publish_status = ?", models.BundlePublishStatusPendingUpstream).
		Order("id ASC").
		Find(&pendingBundles).Error
	downstreamIDs := make([]uint64, 0, len(pendingBundles))
	approvedSet := make(map[uint64]struct{}, len(approvedBundleIDs))
	for _, id := range approvedBundleIDs {
		approvedSet[id] = struct{}{}
	}
	for _, pb := range pendingBundles {
		if _, isSelf := approvedSet[pb.ID]; !isSelf {
			downstreamIDs = append(downstreamIDs, pb.ID)
		}
	}

	var downstreamRun *models.ProtoPublishRun
	if len(downstreamIDs) > 0 {
		dRun, _, dErr := PublishWorkspaceBundles(ctx, db, reg, cfg, PublishWorkspaceOptions{
			BundleIDs:     downstreamIDs,
			PublishMode:   models.PublishModeAuto,
			TriggeredBy:   operatorName,
			OperatorID:    operatorID,
			AlreadyLocked: true,
		})
		if dErr == nil {
			downstreamRun = dRun
		} else {
			log.Printf("[ApproveBreaking] 自动补发下游失败: %v", dErr)
		}
	} else {
		go func() {
			corrCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			_, _ = topology.CorrelateCatalogServices(corrCtx, db, nil)
		}()
	}

	return &ApproveBreakingResult{
		ApprovedSchemas:    activated,
		DownstreamRun:      downstreamRun,
		AutoDeactivatedIDs: autoDeactivated,
	}, nil
}

// ConfirmPreviewPublishRun 在 PREVIEW 模式下对 PREVIEW_READY 发布批次执行二次确认生效。
func ConfirmPreviewPublishRun(
	ctx context.Context,
	db *gorm.DB,
	reg *decoder.Registry,
	cfg *config.Config,
	runID uint64,
	operatorName string,
	operatorID uint,
) (*models.ProtoPublishRun, *PublishRunDetails, error) {
	if db == nil || reg == nil {
		return nil, nil, errors.New("db 或 registry 未初始化")
	}
	if cfg == nil {
		cfg = &config.AppConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}

	lk, lErr := TryAcquirePipelineLock(ctx, db, PipelineLockHolder{
		JobType: "CONFIRM_PREVIEW",
		JobID:   runID,
		Holder:  strings.TrimSpace(operatorName),
	})
	if lErr != nil {
		return nil, nil, lErr
	}
	defer lk.Release()

	var pubRun models.ProtoPublishRun
	if err := db.WithContext(ctx).First(&pubRun, runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrPublishRunNotFound
		}
		return nil, nil, err
	}
	if pubRun.Status != models.PublishRunStatusPreviewReady {
		return nil, nil, ErrPublishRunNotPreviewReady
	}

	var details PublishRunDetails
	if len(pubRun.DetailsJSON) > 0 {
		_ = json.Unmarshal(pubRun.DetailsJSON, &details)
	}

	bundleMetaBySchema := make(map[uint64]decoder.BreakingBundleMeta, len(details.BundleResults))
	for _, br := range details.BundleResults {
		if br.SchemaID != nil && *br.SchemaID > 0 {
			repoIDCopy := br.GitRepoID
			bundleIDCopy := br.BundleID
			bundleMetaBySchema[*br.SchemaID] = decoder.BreakingBundleMeta{
				GitRepoID:  &repoIDCopy,
				BundleID:   &bundleIDCopy,
				BundleName: br.BundleName,
			}
		}
	}
	observedBySchema := make(map[uint64]decoder.SchemaBreakingObservation)
	pubRunIDCopy := pubRun.ID
	auditCtx := &decoder.BreakingAuditContext{
		Source:         models.BreakingSourcePipelinePublish,
		PublishRunID:   &pubRunIDCopy,
		TriggeredBy:    operatorName,
		BundleBySchema: bundleMetaBySchema,
		OnObserved: func(obs decoder.SchemaBreakingObservation) {
			observedBySchema[obs.ToSchemaID] = obs
		},
	}

	publishedCnt := 0
	var deactIDs []uint64
	for _, layerIDs := range details.LayerBatches {
		if len(layerIDs) == 0 {
			continue
		}
		if pre := deactivateCoveredArchivedSchemas(ctx, db, reg, layerIDs, operatorName); len(pre) > 0 {
			deactIDs = appendUniqueUint64(deactIDs, pre...)
		}
		activated, _, err := reg.BatchActivate(layerIDs, decoder.BatchActivateOptions{
			SkipReload:      true,
			AllowLargeBatch: true,
			BreakingAudit:   auditCtx,
		})
		if err != nil {
			return nil, &details, err
		}
		for i := range activated {
			publishedCnt++
			_, _ = protodoc.RebuildForSchema(db, &activated[i])
			if activated[i].SourceBundleID != nil && *activated[i].SourceBundleID > 0 {
				_ = db.WithContext(ctx).Model(&models.GitProtoBundle{}).Where("id = ?", *activated[i].SourceBundleID).Updates(map[string]any{
					"last_schema_id":             activated[i].ID,
					"last_publish_status":        models.BundlePublishStatusPublished,
					"last_publish_run_id":        pubRun.ID,
					"pending_breaking_schema_id": nil,
				}).Error
			}
		}
	}

	if publishedCnt > 0 {
		_ = reg.Reload()
		if post := deactivateCoveredArchivedSchemas(ctx, db, reg, nil, operatorName); len(post) > 0 {
			deactIDs = appendUniqueUint64(deactIDs, post...)
		}
		details.AutoDeactivatedIDs = appendUniqueUint64(details.AutoDeactivatedIDs, deactIDs...)
		go func() {
			corrCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			_, _ = topology.CorrelateCatalogServices(corrCtx, db, nil)
		}()
	}

	if len(observedBySchema) > 0 {
		details.BreakingAuditCount += len(observedBySchema)
		existingItems := make(map[uint64]struct{}, len(details.BreakingItems))
		for _, bi := range details.BreakingItems {
			existingItems[bi.SchemaID] = struct{}{}
		}
		for sid, obs := range observedBySchema {
			if _, ok := existingItems[sid]; ok {
				continue
			}
			var bundleIDVal uint64
			if obs.BundleID != nil {
				bundleIDVal = *obs.BundleID
			}
			details.BreakingItems = append(details.BreakingItems, PublishBreakingItem{
				BundleID:           bundleIDVal,
				BundleName:         obs.BundleName,
				TargetSchemaName:   obs.SchemaName,
				SchemaID:           obs.ToSchemaID,
				Version:            obs.ToVersion,
				BreakingRules:      obs.BreakingRules,
				AffectedDependents: obs.AffectedDependents,
			})
		}
	}

	for i := range details.BundleResults {
		if details.BundleResults[i].Status == models.BundlePublishStatusPending && details.BundleResults[i].SchemaID != nil {
			details.BundleResults[i].Status = models.BundlePublishStatusPublished
			details.BundleResults[i].ActiveSchemaID = details.BundleResults[i].SchemaID
			if obs, ok := observedBySchema[*details.BundleResults[i].SchemaID]; ok {
				details.BundleResults[i].Reason = fmt.Sprintf("已确认发布生效（检出 %d 项破坏性变更，已归档至规范与 MR 门禁审计）", len(obs.BreakingRules))
			} else {
				details.BundleResults[i].Reason = "已确认发布生效"
			}
		}
	}
	fin := time.Now().UTC()
	pubRun.PublishedCount += publishedCnt
	pubRun.Status = models.PublishRunStatusCompleted
	pubRun.FinishedAt = &fin
	detailsBytes, _ := json.Marshal(details)
	pubRun.DetailsJSON = datatypes.JSON(detailsBytes)
	if err := db.WithContext(ctx).Save(&pubRun).Error; err != nil {
		return nil, &details, err
	}
	return &pubRun, &details, nil
}

func appendUniqueUint64(dst []uint64, vals ...uint64) []uint64 {
	seen := make(map[uint64]struct{}, len(dst)+len(vals))
	for _, v := range dst {
		seen[v] = struct{}{}
	}
	for _, v := range vals {
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			dst = append(dst, v)
		}
	}
	return dst
}

// deactivateCoveredArchivedSchemas 自动停用服务全集已被生效（或即将批量激活的）Git 来源 Schema 覆盖的 UPLOAD_PB / ONLINE_COMPILE 归档资产 (ADR-29 §2.5.5)。
func deactivateCoveredArchivedSchemas(ctx context.Context, db *gorm.DB, reg *decoder.Registry, extraGitSchemaIDs []uint64, operatorName string) []uint64 {
	var archivedActive []models.ProtoSchema
	if err := db.WithContext(ctx).
		Select("id, name, version, source_type, is_active").
		Where("is_active = true AND source_type IN ?", []string{models.ProtoSourceUploadPB, models.ProtoSourceOnlineCompile}).
		Order("id ASC").
		Find(&archivedActive).Error; err != nil || len(archivedActive) == 0 {
		return nil
	}

	var gitActiveOps []models.ProtoOperation
	if err := db.WithContext(ctx).
		Table("contract_proto_operations AS op").
		Select("DISTINCT op.service_name").
		Joins("JOIN contract_proto_schemas AS s ON s.id = op.schema_id").
		Where("op.is_active = true AND s.is_active = true AND s.source_type IN ?",
			[]string{models.ProtoSourceGitSync, models.ProtoSourceGitMRAuto, models.ProtoSourceGitPipeline}).
		Scan(&gitActiveOps).Error; err != nil {
		return nil
	}
	gitServices := make(map[string]struct{}, len(gitActiveOps))
	for _, op := range gitActiveOps {
		if op.ServiceName != "" {
			gitServices[op.ServiceName] = struct{}{}
		}
	}
	if len(extraGitSchemaIDs) > 0 {
		var stagedServices []string
		_ = db.WithContext(ctx).Model(&models.ProtoOperation{}).
			Where("schema_id IN ?", extraGitSchemaIDs).
			Distinct("service_name").
			Pluck("service_name", &stagedServices).Error
		for _, svc := range stagedServices {
			if svc != "" {
				gitServices[svc] = struct{}{}
			}
		}
	}

	var deactivatedIDs []uint64
	for _, arch := range archivedActive {
		var svcs []string
		_ = db.WithContext(ctx).Model(&models.ProtoOperation{}).
			Where("schema_id = ?", arch.ID).
			Distinct("service_name").
			Pluck("service_name", &svcs).Error
		if len(svcs) == 0 {
			continue
		}
		allCovered := true
		for _, svc := range svcs {
			if _, ok := gitServices[svc]; !ok {
				allCovered = false
				break
			}
		}
		if !allCovered {
			continue
		}
		if _, _, dErr := reg.DeactivateSchemaVersion(arch.ID); dErr == nil {
			deactivatedIDs = append(deactivatedIDs, arch.ID)
			afterBytes, _ := json.Marshal(map[string]any{"covered_services": svcs})
			targetIDStr := fmt.Sprintf("%d", arch.ID)
			commonAudit.Push(&commonModels.SysAuditLog{
				Module:     config.ModuleKey,
				Action:     "contract_proto_pipeline:archive_auto_deactivate",
				Level:      commonModels.AuditLevelP2,
				Username:   operatorName,
				Summary:    fmt.Sprintf("历史归档描述集 [%s v%d] 的全部服务 (%d 个) 已被 Git 来源生效版本覆盖，自动停用", arch.Name, arch.Version, len(svcs)),
				TargetType: "proto_schema",
				TargetID:   targetIDStr,
				TargetName: arch.Name,
				AfterData:  string(afterBytes),
			})
		}
	}
	return deactivatedIDs
}

func computeBundleCompileInputSHA256(
	b models.GitProtoBundle,
	directFiles []models.ProtoFile,
	fileByLogical map[string]models.ProtoFile,
	graph FileDependencyGraph,
) string {
	visited := make(map[string]models.ProtoFile)
	queue := make([]string, 0, len(directFiles))
	for _, f := range directFiles {
		visited[f.FilePath] = f
		queue = append(queue, f.FilePath)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for dep := range graph.ForwardEdges[cur] {
			if _, seen := visited[dep]; seen {
				continue
			}
			if df, ok := fileByLogical[dep]; ok {
				visited[dep] = df
				queue = append(queue, dep)
			}
		}
	}

	paths := make([]string, 0, len(visited))
	for p := range visited {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	entries := append([]string(nil), b.EntryFiles()...)
	sort.Strings(entries)
	incDirs := append([]string(nil), b.IncludeDirs()...)
	sort.Strings(incDirs)

	h := sha256.New()
	_, _ = fmt.Fprintf(h, "compiler=%s|schema=%s|cat=%s|entries=%s|inc=%s\n",
		workspacePublisherCompilerVersion, b.TargetSchemaName, b.Category,
		strings.Join(entries, ","), strings.Join(incDirs, ","))
	for _, p := range paths {
		f := visited[p]
		_, _ = fmt.Fprintf(h, "%s|%s|%s\n", f.FilePath, f.ContentSHA256, f.NormalizerVersion)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func appendUniqueStr(slice []string, val string) []string {
	for _, existing := range slice {
		if existing == val {
			return slice
		}
	}
	return append(slice, val)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
