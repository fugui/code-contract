package gitclient

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/models"

	"gorm.io/gorm"
)

// FullPipelineRunRequest 控制一键四阶段可视化流水线编排任务。
type FullPipelineRunRequest struct {
	FromStage         string                           `json:"from_stage"` // COLLECT_NORMALIZE | RECONCILE | PUBLISH
	CachePolicy       string                           `json:"cache_policy"`
	ForceRecompile    bool                             `json:"force_recompile"`
	TriggerSource     string                           `json:"-"`
	TriggeredBy       string                           `json:"-"`
	OperatorID        uint                             `json:"-"`
	WaitSync          bool                             `json:"sync"`
	ProgressFn        func(PipelineProgressEvent)      `json:"-"`
	PublishProgressFn func(PublishProgressEvent)       `json:"-"`
	ReconcileProgFn   func(PoolReconcileProgressEvent) `json:"-"`
}

// PipelineProgressEvent 为 SSE proto_pipeline.progress 事件载荷。
type PipelineProgressEvent struct {
	PipelineRunID  uint64     `json:"pipeline_run_id"`
	FromStage      string     `json:"from_stage"`
	CurrentStage   string     `json:"current_stage"`
	Status         string     `json:"status"`
	TriggerSource  string     `json:"trigger_source"`
	SyncJobID      *uint64    `json:"sync_job_id,omitempty"`
	ReconcileRunID *uint64    `json:"reconcile_run_id,omitempty"`
	PublishRunID   *uint64    `json:"publish_run_id,omitempty"`
	Message        string     `json:"message"`
	ErrorMessage   string     `json:"error_message,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

// ArchivedExternalSchemaDTO 描述历史外部来源（UPLOAD_PB / ONLINE_COMPILE）的对账状态。
type ArchivedExternalSchemaDTO struct {
	SchemaID        uint64    `json:"schema_id"`
	Name            string    `json:"name"`
	Version         int       `json:"version"`
	SourceType      string    `json:"source_type"`
	Category        string    `json:"category"`
	IsActive        bool      `json:"is_active"`
	CreatedAt       time.Time `json:"created_at"`
	TotalServices   int       `json:"total_services"`
	CoveredServices int       `json:"covered_services"`
	CoverageRatio   float64   `json:"coverage_ratio"`
	UncoveredNames  []string  `json:"uncovered_names,omitempty"`
}

// PipelineOverviewDTO 聚合四阶段实时状态、全局锁态、Stale 漂移标记与归档摘要。
type PipelineOverviewDTO struct {
	LockHolder             *PipelineLockHolder      `json:"lock_holder,omitempty"`
	LatestPipelineRun      *models.ProtoPipelineRun `json:"latest_pipeline_run,omitempty"`
	PublishMode            string                   `json:"publish_mode"`
	FanoutWarnThreshold    int                      `json:"fanout_warn_threshold"`
	BlockOnConflict        bool                     `json:"block_on_unresolved_conflict"`
	GovernanceRevision     int                      `json:"governance_revision"`
	GovernanceConfigSHA256 string                   `json:"governance_config_sha256"`
	NormalizeStale         bool                     `json:"normalize_stale"`
	NormalizeStaleReasons  []string                 `json:"normalize_stale_reasons,omitempty"`
	ReconcileStale         bool                     `json:"reconcile_stale"`
	ReconcileStaleReasons  []string                 `json:"reconcile_stale_reasons,omitempty"`
	PublishStale           bool                     `json:"publish_stale"`
	PublishStaleReasons    []string                 `json:"publish_stale_reasons,omitempty"`
	Stage1                 PipelineStage1Summary    `json:"stage1"`
	Stage2                 PipelineStage2Summary    `json:"stage2"`
	Stage3                 PipelineStage3Summary    `json:"stage3"`
	Stage4                 PipelineStage4Summary    `json:"stage4"`
}

type PipelineStage1Summary struct {
	PlatformActiveTotal int64                `json:"platform_active_total"`
	HasProtoCount       int64                `json:"has_proto_count"`
	NoProtoCount        int64                `json:"no_proto_count"`
	NoLocalCacheCount   int64                `json:"no_local_cache_count"`
	ExcludedCount       int64                `json:"excluded_count"`
	SyncFailedCount     int64                `json:"sync_failed_count"`
	SharedWriteEnabled  bool                 `json:"shared_write_enabled"`
	LatestSyncAt        *time.Time           `json:"latest_sync_at,omitempty"`
	LatestSyncJob       *models.ProtoSyncJob `json:"latest_sync_job,omitempty"`
}

type PipelineStage2Summary struct {
	TotalFiles       int64 `json:"total_files"`
	NormalizedCount  int64 `json:"normalized_count"`
	QuarantinedCount int64 `json:"quarantined_count"`
	UServiceCount    int64 `json:"uservice_count"`
	UnassignedRepos  int64 `json:"unassigned_repos"`
}

type PipelineStage3Summary struct {
	LatestRun               *models.ProtoPoolReconcileRun `json:"latest_run,omitempty"`
	ConflictCount           int                           `json:"conflict_count"`
	UnresolvedConflictCount int                           `json:"unresolved_conflict_count"`
	AutoFixCount            int                           `json:"auto_fix_count"`
	QuarantinedCount        int                           `json:"quarantined_count"`
	HarvestedBaseCount      int                           `json:"harvested_base_count"`
}

type PipelineStage4Summary struct {
	LatestRun            *models.ProtoPublishRun `json:"latest_run,omitempty"`
	ActiveSchemaCount    int64                   `json:"active_schema_count"`
	ActiveServiceCount   int64                   `json:"active_service_count"`
	ActiveOperationCount int64                   `json:"active_operation_count"`
	BreakingBlockedCount int64                   `json:"breaking_blocked_count"`
	PendingUpstreamCount int64                   `json:"pending_upstream_count"`
	ConflictBlockedCount int64                   `json:"conflict_blocked_count"`
	ArchivedActiveCount  int64                   `json:"archived_active_count"`
}

// RunFullProtoPipeline 持全局流水线锁按序执行 ① 采集清洗 -> ③ 全池收敛 -> ④ 拓扑编译发布 (ADR-29 §2.3.4)。
func RunFullProtoPipeline(
	ctx context.Context,
	db *gorm.DB,
	sched *SyncScheduler,
	reg *decoder.Registry,
	cfg *config.Config,
	req FullPipelineRunRequest,
) (*models.ProtoPipelineRun, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if cfg == nil {
		cfg = &config.AppConfig
	}
	if sched == nil {
		sched = NewSyncScheduler(db, reg, nil, cfg)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	fromStage := strings.ToUpper(strings.TrimSpace(req.FromStage))
	switch fromStage {
	case models.PipelineStageReconcile, models.PipelineStagePublish:
	default:
		fromStage = models.PipelineStageCollectNormalize
	}
	triggerSource := strings.ToUpper(strings.TrimSpace(req.TriggerSource))
	switch triggerSource {
	case models.PipelineTriggerMRMerge, models.PipelineTriggerSchedule:
	default:
		triggerSource = models.PipelineTriggerManual
	}
	policy := strings.ToUpper(strings.TrimSpace(req.CachePolicy))
	if policy != models.SyncCachePolicyFetchMissing {
		policy = models.SyncCachePolicyCacheOnly
	}
	if policy == models.SyncCachePolicyFetchMissing && !cfg.Storage.SharedWriteEnabled {
		return nil, ErrSharedWriteDisabled
	}

	lk, lErr := TryAcquirePipelineLock(ctx, db, PipelineLockHolder{
		JobType: "PIPELINE_RUN",
		Holder:  strings.TrimSpace(req.TriggeredBy),
	})
	if lErr != nil {
		return nil, lErr
	}

	startedAt := time.Now().UTC()
	pipeRun := models.ProtoPipelineRun{
		FromStage:     fromStage,
		CurrentStage:  fromStage,
		Status:        models.PipelineRunStatusRunning,
		TriggerSource: triggerSource,
		TriggeredBy:   strings.TrimSpace(req.TriggeredBy),
		StartedAt:     startedAt,
	}
	if err := db.WithContext(ctx).Create(&pipeRun).Error; err != nil {
		lk.Release()
		return nil, err
	}
	SetActivePipelineLockJobID(pipeRun.ID)

	emit := func(msg string) {
		if req.ProgressFn == nil {
			return
		}
		req.ProgressFn(PipelineProgressEvent{
			PipelineRunID:  pipeRun.ID,
			FromStage:      pipeRun.FromStage,
			CurrentStage:   pipeRun.CurrentStage,
			Status:         pipeRun.Status,
			TriggerSource:  pipeRun.TriggerSource,
			SyncJobID:      pipeRun.SyncJobID,
			ReconcileRunID: pipeRun.ReconcileRunID,
			PublishRunID:   pipeRun.PublishRunID,
			Message:        msg,
			ErrorMessage:   pipeRun.ErrorMessage,
			StartedAt:      pipeRun.StartedAt,
			FinishedAt:     pipeRun.FinishedAt,
		})
	}
	emit("一键四阶段流水线已启动")

	execute := func(execCtx context.Context) error {
		defer lk.Release()

		failPipe := func(cause error) error {
			fin := time.Now().UTC()
			pipeRun.Status = models.PipelineRunStatusFailed
			pipeRun.ErrorMessage = cause.Error()
			pipeRun.FinishedAt = &fin
			_ = db.WithContext(execCtx).Save(&pipeRun).Error
			emit("流水线执行失败: " + cause.Error())
			return cause
		}

		// 阶段 ① + ②：采集与清洗 (skip_compile = true)
		if fromStage == models.PipelineStageCollectNormalize {
			pipeRun.CurrentStage = models.PipelineStageCollectNormalize
			_ = db.WithContext(execCtx).Model(&pipeRun).Update("current_stage", pipeRun.CurrentStage).Error
			emit("阶段 ①/② 正在扫描缓存并清洗规整...")

			skipComp := true
			syncJob, sErr := RunSyncAllJob(execCtx, db, sched, cfg, SyncAllRequest{
				Scope:          models.SyncJobScopeAll,
				CachePolicy:    policy,
				ForceRecompile: req.ForceRecompile,
				SkipCompile:    &skipComp,
				OperatorID:     req.OperatorID,
				TriggeredBy:    req.TriggeredBy,
				WaitSync:       true,
				AlreadyLocked:  true,
			})
			if sErr != nil {
				return failPipe(sErr)
			}
			if syncJob != nil {
				idCopy := syncJob.ID
				pipeRun.SyncJobID = &idCopy
				_ = db.WithContext(execCtx).Model(&pipeRun).Update("sync_job_id", idCopy).Error
			}
		}

		// 阶段 ③：全池 Stage C+ 收敛与导出 workspace/
		if fromStage == models.PipelineStageCollectNormalize || fromStage == models.PipelineStageReconcile {
			pipeRun.CurrentStage = models.PipelineStageReconcile
			_ = db.WithContext(execCtx).Model(&pipeRun).Update("current_stage", pipeRun.CurrentStage).Error
			emit("阶段 ③ 正在执行全池依赖收敛、R3 仲裁与 R7/R8 编译自愈...")

			recOpts := PoolReconcileOptions{
				CodesDir:      cfg.Storage.GetCodesDir(),
				ProtosDir:     cfg.Storage.GetProtosDir(),
				TriggeredBy:   req.TriggeredBy,
				AlreadyLocked: true,
				ProgressFn:    req.ReconcileProgFn,
			}
			recRun, _, rErr := RunPoolReconcile(execCtx, db, recOpts)
			if rErr != nil {
				return failPipe(rErr)
			}
			if recRun != nil {
				idCopy := recRun.ID
				pipeRun.ReconcileRunID = &idCopy
				_ = db.WithContext(execCtx).Model(&pipeRun).Update("reconcile_run_id", idCopy).Error
			}
		}

		// 阶段 ④：单仓拓扑编译与分级发布
		pipeRun.CurrentStage = models.PipelineStagePublish
		_ = db.WithContext(execCtx).Model(&pipeRun).Update("current_stage", pipeRun.CurrentStage).Error
		emit("阶段 ④ 正在执行单仓拓扑编译发布与接口文档重建...")

		pubRun, _, pErr := PublishWorkspaceBundles(execCtx, db, reg, cfg, PublishWorkspaceOptions{
			ForceRecompile: req.ForceRecompile,
			TriggeredBy:    req.TriggeredBy,
			OperatorID:     req.OperatorID,
			AlreadyLocked:  true,
			ProgressFn:     req.PublishProgressFn,
		})
		if pErr != nil {
			if pubRun != nil && pubRun.ID > 0 {
				idCopy := pubRun.ID
				pipeRun.PublishRunID = &idCopy
			}
			return failPipe(pErr)
		}
		if pubRun != nil {
			idCopy := pubRun.ID
			pipeRun.PublishRunID = &idCopy
		}

		fin := time.Now().UTC()
		if pubRun != nil && (pubRun.Status == models.PublishRunStatusPartialBlocked || pubRun.Status == models.PublishRunStatusPreviewReady) {
			if pubRun.Status == models.PublishRunStatusPartialBlocked {
				pipeRun.Status = models.PipelineRunStatusPartialBlocked
			} else {
				pipeRun.Status = models.PipelineRunStatusCompleted
			}
		} else {
			pipeRun.Status = models.PipelineRunStatusCompleted
		}
		pipeRun.FinishedAt = &fin
		if err := db.WithContext(execCtx).Save(&pipeRun).Error; err != nil {
			return err
		}
		emit("四阶段契约流水线执行完成")
		return nil
	}

	if req.WaitSync {
		if err := execute(ctx); err != nil {
			return &pipeRun, err
		}
		return &pipeRun, nil
	}

	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		_ = execute(bgCtx)
	}()
	return &pipeRun, nil
}

// BuildPipelineOverview 聚合四阶段实时状态、锁信息与 Stale 漂移判定 (ADR-29 §2.2.1)。
func BuildPipelineOverview(ctx context.Context, db *gorm.DB, cfg *config.Config) (*PipelineOverviewDTO, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if cfg == nil {
		cfg = &config.AppConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}

	syncSum, err := GetSyncSummary(ctx, db, cfg.Storage.SharedWriteEnabled)
	if err != nil {
		return nil, err
	}

	out := &PipelineOverviewDTO{
		LockHolder:          GetActivePipelineLockHolder(),
		PublishMode:         cfg.ProtoPipeline.EffectivePublishMode(),
		FanoutWarnThreshold: cfg.ProtoPipeline.FanoutWarnThreshold,
		Stage1: PipelineStage1Summary{
			PlatformActiveTotal: syncSum.PlatformActiveTotal,
			HasProtoCount:       syncSum.HasProtoCount,
			NoProtoCount:        syncSum.NoProtoCount,
			NoLocalCacheCount:   syncSum.NoLocalCacheCount,
			ExcludedCount:       syncSum.ExcludedCount,
			SyncFailedCount:     syncSum.SyncFailedCount,
			SharedWriteEnabled:  cfg.Storage.SharedWriteEnabled,
			LatestSyncJob:       syncSum.LatestJob,
		},
	}
	if syncSum.LatestJob != nil {
		out.Stage1.LatestSyncAt = syncSum.LatestJob.FinishedAt
		if out.Stage1.LatestSyncAt == nil {
			t := syncSum.LatestJob.StartedAt
			out.Stage1.LatestSyncAt = &t
		}
	}

	var latestPipe models.ProtoPipelineRun
	if err := db.WithContext(ctx).Order("id DESC").Limit(1).Find(&latestPipe).Error; err == nil && latestPipe.ID > 0 {
		out.LatestPipelineRun = &latestPipe
	}

	snap, gErr := LoadActiveGovernanceConfig(ctx, db)
	if gErr == nil && snap != nil {
		out.GovernanceRevision = snap.Doc.Revision
		out.GovernanceConfigSHA256 = snap.ConfigSHA256
		out.BlockOnConflict = snap.Doc.Global.EffectivePoolReconcile().EffectiveBlockOnUnresolvedConflict()
	}

	// Stage 2 统计
	_ = db.WithContext(ctx).Model(&models.ProtoFile{}).Where("is_deleted = false").Count(&out.Stage2.TotalFiles).Error
	_ = db.WithContext(ctx).Model(&models.ProtoFile{}).Where("is_deleted = false AND pipeline_status = ?", models.ProtoPipelineNormalized).Count(&out.Stage2.NormalizedCount).Error
	_ = db.WithContext(ctx).Model(&models.ProtoFile{}).Where("is_deleted = false AND pipeline_status = ?", models.ProtoPipelineQuarantined).Count(&out.Stage2.QuarantinedCount).Error
	_ = db.WithContext(ctx).Model(&models.ProtoFile{}).Where("is_deleted = false AND is_uservice_source = true").Count(&out.Stage2.UServiceCount).Error
	_ = db.WithContext(ctx).Model(&models.GitRepo{}).
		Where("sync_enabled = true AND proto_status = ? AND (subsystem_slug = '' OR subsystem_slug = ?) AND export_target_subsystem = ''",
			models.GitRepoProtoStatusHasProto, UnassignedSubsystemSlug).
		Count(&out.Stage2.UnassignedRepos).Error

	// Stage 3 统计
	var latestReconcile models.ProtoPoolReconcileRun
	if err := db.WithContext(ctx).
		Where("status = ?", models.PoolReconcileStatusCompleted).
		Order("id DESC").
		Limit(1).
		Find(&latestReconcile).Error; err == nil && latestReconcile.ID > 0 {
		out.Stage3.LatestRun = &latestReconcile
		out.Stage3.ConflictCount = latestReconcile.ConflictCount
		out.Stage3.AutoFixCount = latestReconcile.AutoFixCount
		out.Stage3.QuarantinedCount = latestReconcile.QuarantinedCount
		out.Stage3.HarvestedBaseCount = latestReconcile.HarvestedBaseCount
	}

	// 统计未人工裁决的内容不等价冲突数
	var owners []models.ProtoImportOwner
	_ = db.WithContext(ctx).Find(&owners).Error
	manualOwnerUpdatedAt := time.Time{}
	manualPathSet := make(map[string]struct{})
	for _, ow := range owners {
		if ow.Source == models.ImportOwnerSourceManual || ow.Source == models.ImportOwnerSourceBaseLib {
			manualPathSet[ow.LogicalPath] = struct{}{}
			if ow.Source == models.ImportOwnerSourceManual && ow.UpdatedAt.After(manualOwnerUpdatedAt) {
				manualOwnerUpdatedAt = ow.UpdatedAt
			}
		}
	}
	if latestReconcile.ID > 0 && len(latestReconcile.ReportJSON) > 0 {
		var rep PoolReconcileReport
		if err := jsonUnmarshalSafe(latestReconcile.ReportJSON, &rep); err == nil {
			for _, c := range rep.Conflicts {
				if c.ContentIdentical {
					continue
				}
				if _, resolved := manualPathSet[c.LogicalPath]; !resolved && !c.HasManualOwner {
					out.Stage3.UnresolvedConflictCount++
				}
			}
		}
	}

	// Stage 4 统计
	var latestPub models.ProtoPublishRun
	if err := db.WithContext(ctx).Order("id DESC").Limit(1).Find(&latestPub).Error; err == nil && latestPub.ID > 0 {
		out.Stage4.LatestRun = &latestPub
	}
	_ = db.WithContext(ctx).Model(&models.ProtoSchema{}).Where("is_active = true").Count(&out.Stage4.ActiveSchemaCount).Error
	_ = db.WithContext(ctx).Model(&models.ProtoOperation{}).Where("is_active = true").Distinct("service_name").Count(&out.Stage4.ActiveServiceCount).Error
	_ = db.WithContext(ctx).Model(&models.ProtoOperation{}).Where("is_active = true").Count(&out.Stage4.ActiveOperationCount).Error
	_ = db.WithContext(ctx).Model(&models.GitProtoBundle{}).Where("last_publish_status = ?", models.BundlePublishStatusBlockedBreaking).Count(&out.Stage4.BreakingBlockedCount).Error
	_ = db.WithContext(ctx).Model(&models.GitProtoBundle{}).Where("last_publish_status = ?", models.BundlePublishStatusPendingUpstream).Count(&out.Stage4.PendingUpstreamCount).Error
	_ = db.WithContext(ctx).Model(&models.GitProtoBundle{}).Where("last_publish_status = ?", models.BundlePublishStatusBlockedConflict).Count(&out.Stage4.ConflictBlockedCount).Error
	_ = db.WithContext(ctx).Model(&models.ProtoSchema{}).
		Where("is_active = true AND source_type IN ?", []string{models.ProtoSourceUploadPB, models.ProtoSourceOnlineCompile}).
		Count(&out.Stage4.ArchivedActiveCount).Error

	// Stale 漂移判定（ADR-29 §2.2.1 5 条规则）
	// 规则 1 & 2: 规则配置变更或存在采集后未按当前规则版本清洗的文件 -> normalize_stale
	if out.GovernanceConfigSHA256 != "" {
		if latestReconcile.ID > 0 && latestReconcile.ConfigSHA256 != "" && latestReconcile.ConfigSHA256 != out.GovernanceConfigSHA256 {
			out.NormalizeStale = true
			out.NormalizeStaleReasons = append(out.NormalizeStaleReasons, "全局清洗规则已变更，建议免网络重洗")
			out.ReconcileStale = true
			out.ReconcileStaleReasons = append(out.ReconcileStaleReasons, "规则配置已更新，待重新收敛")
		}
		var unnormalizedCnt int64
		_ = db.WithContext(ctx).Model(&models.ProtoFile{}).
			Where("is_deleted = false AND pipeline_status = ?", models.ProtoPipelineCollected).
			Count(&unnormalizedCnt).Error
		if unnormalizedCnt > 0 {
			out.NormalizeStale = true
			out.NormalizeStaleReasons = append(out.NormalizeStaleReasons, fmt.Sprintf("存在 %d 个已采集待清洗文件", unnormalizedCnt))
		}
	}
	if snap != nil && out.Stage2.TotalFiles > 0 {
		var mismatchNormCnt int64
		_ = db.WithContext(ctx).Model(&models.ProtoFile{}).
			Where("is_deleted = false AND normalizer_version <> '' AND normalizer_version NOT LIKE ?", "%"+snap.ConfigSHA256[:minInt(12, len(snap.ConfigSHA256))]+"%").
			Count(&mismatchNormCnt).Error
		if mismatchNormCnt > 0 && !out.NormalizeStale {
			out.NormalizeStale = true
			out.NormalizeStaleReasons = append(out.NormalizeStaleReasons, "规则版本与部分文件清洗版本不一致，建议免网络重洗")
		}
	}

	// 规则 3: 阶段 ③ 人工仲裁记录晚于最近一次收敛完成时间 -> reconcile_stale
	if latestReconcile.ID > 0 && latestReconcile.FinishedAt != nil && manualOwnerUpdatedAt.After(*latestReconcile.FinishedAt) {
		out.ReconcileStale = true
		out.ReconcileStaleReasons = append(out.ReconcileStaleReasons, "人工冲突仲裁已更新，待重新收敛")
	}

	// 规则 4: 实时全池输入指纹 != 最近一次收敛记录的 input_fingerprint -> reconcile_stale & publish_stale
	if out.Stage2.TotalFiles > 0 {
		if latestReconcile.ID == 0 {
			out.ReconcileStale = true
			out.ReconcileStaleReasons = append(out.ReconcileStaleReasons, "尚未执行全池依赖收敛")
		} else {
			liveFP, fErr := ComputePoolInputFingerprint(ctx, db)
			if fErr == nil && latestReconcile.InputFingerprint != "" && liveFP != latestReconcile.InputFingerprint {
				out.ReconcileStale = true
				out.ReconcileStaleReasons = append(out.ReconcileStaleReasons, "源码或清洗结果已变化，待重新收敛")
				out.PublishStale = true
				out.PublishStaleReasons = append(out.PublishStaleReasons, "源码已变化，需重新收敛后发布")
			}
		}
	}

	// 规则 5: 存在更新的 PoolReconcileRun，或存在 PENDING_UPSTREAM / PENDING 待发布 Bundle -> publish_stale
	if latestReconcile.ID > 0 {
		if latestPub.ID == 0 || latestReconcile.ID > latestPub.PoolReconcileRunID || latestPub.Status == models.PublishRunStatusRejectedStale {
			out.PublishStale = true
			out.PublishStaleReasons = append(out.PublishStaleReasons, "有新的全池收敛结果待发布")
		}
	}
	if out.Stage4.PendingUpstreamCount > 0 {
		out.PublishStale = true
		out.PublishStaleReasons = append(out.PublishStaleReasons, fmt.Sprintf("有 %d 个下游契约包等待上游就绪后补发", out.Stage4.PendingUpstreamCount))
	}
	if latestPub.ID > 0 && latestPub.Status == models.PublishRunStatusPreviewReady {
		out.PublishStale = true
		out.PublishStaleReasons = append(out.PublishStaleReasons, "PREVIEW 模式编译结果待确认生效")
	}

	return out, nil
}

// ListArchivedExternalSchemas 返回历史归档外部来源（UPLOAD_PB / ONLINE_COMPILE）的对账清单与 Git 服务覆盖率 (ADR-29 §2.5.5)。
func ListArchivedExternalSchemas(ctx context.Context, db *gorm.DB) ([]ArchivedExternalSchemaDTO, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var archived []models.ProtoSchema
	if err := db.WithContext(ctx).
		Select("id, name, version, source_type, category, is_active, created_at").
		Where("source_type IN ?", []string{models.ProtoSourceUploadPB, models.ProtoSourceOnlineCompile}).
		Order("is_active DESC, id DESC").
		Find(&archived).Error; err != nil {
		return nil, err
	}
	if len(archived) == 0 {
		return []ArchivedExternalSchemaDTO{}, nil
	}

	var gitActiveServices []string
	_ = db.WithContext(ctx).
		Table("contract_proto_operations AS op").
		Joins("JOIN contract_proto_schemas AS s ON s.id = op.schema_id").
		Where("op.is_active = true AND s.is_active = true AND s.source_type IN ?",
			[]string{models.ProtoSourceGitSync, models.ProtoSourceGitMRAuto, models.ProtoSourceGitPipeline}).
		Distinct("op.service_name").
		Pluck("op.service_name", &gitActiveServices).Error
	gitSvcSet := make(map[string]struct{}, len(gitActiveServices))
	for _, s := range gitActiveServices {
		gitSvcSet[s] = struct{}{}
	}

	out := make([]ArchivedExternalSchemaDTO, 0, len(archived))
	for _, sc := range archived {
		var svcs []string
		_ = db.WithContext(ctx).Model(&models.ProtoOperation{}).
			Where("schema_id = ?", sc.ID).
			Distinct("service_name").
			Pluck("service_name", &svcs).Error
		sort.Strings(svcs)
		covered := 0
		var uncovered []string
		for _, s := range svcs {
			if _, ok := gitSvcSet[s]; ok {
				covered++
			} else {
				uncovered = append(uncovered, s)
			}
		}
		ratio := 0.0
		if len(svcs) > 0 {
			ratio = float64(covered) / float64(len(svcs))
		}
		out = append(out, ArchivedExternalSchemaDTO{
			SchemaID:        sc.ID,
			Name:            sc.Name,
			Version:         sc.Version,
			SourceType:      sc.SourceType,
			Category:        sc.Category,
			IsActive:        sc.IsActive,
			CreatedAt:       sc.CreatedAt,
			TotalServices:   len(svcs),
			CoveredServices: covered,
			CoverageRatio:   ratio,
			UncoveredNames:  uncovered,
		})
	}
	return out, nil
}

func jsonUnmarshalSafe(raw []byte, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return jsonUnmarshal(raw, dst)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
