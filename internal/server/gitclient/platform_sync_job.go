package gitclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"code-common/backend/gitsync"
	"code-contract/config"
	"code-contract/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var (
	// ErrSharedWriteDisabled 表示服务端关闭了 storage.shared_write_enabled，禁止触发 FETCH_MISSING 网络拉取。
	ErrSharedWriteDisabled = errors.New("服务端未开启 storage.shared_write_enabled，禁止执行缺失缓存网络拉取，请使用「仅同步已有缓存」")
)

// SyncJobErrorItem 记录批处理同步任务中单仓失败原因。
type SyncJobErrorItem struct {
	GitRepoID uint64 `json:"git_repo_id"`
	RepoName  string `json:"repo_name"`
	Error     string `json:"error"`
}

// SyncJobProgressEvent 为 SSE 推送的 proto_sync_job.progress 事件载荷。
type SyncJobProgressEvent struct {
	JobID                 uint64             `json:"job_id"`
	Scope                 string             `json:"scope"`
	CachePolicy           string             `json:"cache_policy"`
	Status                string             `json:"status"`
	TotalRepos            int                `json:"total_repos"`
	ProcessedRepos        int                `json:"processed_repos"`
	HasProtoCount         int                `json:"has_proto_count"`
	NoProtoCount          int                `json:"no_proto_count"`
	NoCacheCount          int                `json:"no_cache_count"`
	FailedCount           int                `json:"failed_count"`
	SkippedUnchangedCount int                `json:"skipped_unchanged_count"`
	ErrorSummary          []SyncJobErrorItem `json:"error_summary,omitempty"`
	StartedAt             time.Time          `json:"started_at"`
	FinishedAt            *time.Time         `json:"finished_at,omitempty"`
}

// SyncJobBroadcaster 推送批处理同步进度到 SSEBroker。
type SyncJobBroadcaster func(evt SyncJobProgressEvent)

// SyncAllRequest 描述全量/局部双按钮异步同步请求。
type SyncAllRequest struct {
	Scope          string   `json:"scope"`        // ALL | DEPARTMENT | SERVICE_GROUP | FAILED_ONLY | SELECTED
	CachePolicy    string   `json:"cache_policy"` // CACHE_ONLY | FETCH_MISSING
	DepartmentID   uint64   `json:"department_id"`
	ServiceGroup   string   `json:"service_group"`
	RepoIDs        []uint64 `json:"repo_ids"`
	ForceRecompile bool     `json:"force_recompile"`
	OperatorID     uint     `json:"-"`
	TriggeredBy    string   `json:"-"`
	WaitSync       bool     `json:"sync"` // 仅测试或显式等待完成时设为 true
}

// SyncSummaryDTO 汇总平台活跃仓、6 态数量与最近同步任务。
type SyncSummaryDTO struct {
	PlatformActiveTotal int64                `json:"platform_active_total"`
	AlignedTotal        int64                `json:"aligned_total"`
	PendingCount        int64                `json:"pending_count"`
	HasProtoCount       int64                `json:"has_proto_count"`
	NoProtoCount        int64                `json:"no_proto_count"`
	NoLocalCacheCount   int64                `json:"no_local_cache_count"`
	ExcludedCount       int64                `json:"excluded_count"`
	SyncFailedCount     int64                `json:"sync_failed_count"`
	SharedWriteEnabled  bool                 `json:"shared_write_enabled"`
	ServiceGroups       []string             `json:"service_groups"`
	LatestJob           *models.ProtoSyncJob `json:"latest_job,omitempty"`
}

// AlignPlatformRepositories 以 code-bench 公共 repositories 表为唯一真值源，零勾选自动对齐 contract_git_repos。
func AlignPlatformRepositories(ctx context.Context, db *gorm.DB) (created int, updated int, deactivated int, err error) {
	if db == nil {
		return 0, 0, 0, errors.New("db 不能为空")
	}

	var plats []models.Repository
	if err := db.WithContext(ctx).Order("id ASC").Find(&plats).Error; err != nil {
		return 0, 0, 0, err
	}

	var existingRepos []models.GitRepo
	if err := db.WithContext(ctx).Order("id ASC").Find(&existingRepos).Error; err != nil {
		return 0, 0, 0, err
	}

	byPlatID := make(map[uint64]models.GitRepo, len(existingRepos))
	usedNames := make(map[string]struct{}, len(existingRepos))
	for _, gr := range existingRepos {
		byPlatID[gr.PlatformRepoID] = gr
		usedNames[gr.Name] = struct{}{}
	}

	platByID := make(map[uint64]models.Repository, len(plats))
	for _, p := range plats {
		pid := uint64(p.ID)
		platByID[pid] = p

		effectiveURL := strings.TrimSpace(p.URL)
		if effectiveURL == "" {
			effectiveURL = strings.TrimSpace(p.HTTPURL)
		}
		branch := gitsync.ResolveBranchDefault(p.Branch)
		subSlug := NormalizeSlug(p.ServiceGroup, nil, "sub:"+p.ServiceGroup, true)

		existing, exists := byPlatID[pid]
		if !exists {
			if !p.IsActive {
				continue
			}
			repoName := strings.TrimSpace(p.Name)
			if repoName == "" {
				repoName = fmt.Sprintf("repo-%d", pid)
			}
			if _, taken := usedNames[repoName]; taken {
				repoName = fmt.Sprintf("%s-%d", repoName, pid)
			}
			if _, taken := usedNames[repoName]; taken {
				repoName = fmt.Sprintf("plat-repo-%d", pid)
			}
			usedNames[repoName] = struct{}{}

			newRepo := models.GitRepo{
				PlatformRepoID:  pid,
				SourceMode:      models.GitSourceModePlatformShared,
				SyncStrategy:    models.GitSyncStrategyLocalCacheOnly,
				ProtoStatus:     models.GitRepoProtoStatusPending,
				SyncEnabled:     true,
				SubsystemSlug:   subSlug,
				Name:            repoName,
				Provider:        models.GitProviderGitLab,
				RepoURL:         effectiveURL,
				DefaultBranch:   branch,
				AuthType:        models.GitAuthNone,
				SyncMode:        models.GitSyncPoll,
				PollIntervalSec: 1800,
				AutoCompile:     true,
				AutoActivate:    false,
				LintOnSync:      true,
				LastSyncStatus:  models.GitSyncStatusIdle,
			}
			if cErr := db.WithContext(ctx).Create(&newRepo).Error; cErr != nil {
				return created, updated, deactivated, cErr
			}
			created++
			continue
		}

		if !p.IsActive {
			if existing.SyncEnabled || existing.ProtoStatus != models.GitRepoProtoStatusExcluded {
				if uErr := db.WithContext(ctx).Model(&existing).Updates(map[string]any{
					"sync_enabled": false,
					"proto_status": models.GitRepoProtoStatusExcluded,
				}).Error; uErr != nil {
					return created, updated, deactivated, uErr
				}
				deactivated++
			}
			continue
		}

		updates := make(map[string]any)
		if existing.SourceMode != models.GitSourceModePlatformShared {
			updates["source_mode"] = models.GitSourceModePlatformShared
		}
		if effectiveURL != "" && existing.RepoURL != effectiveURL {
			updates["repo_url"] = effectiveURL
		}
		if branch != "" && existing.DefaultBranch != branch {
			updates["default_branch"] = branch
		}
		if subSlug != "" && existing.SubsystemSlug != subSlug {
			updates["subsystem_slug"] = subSlug
		}
		if len(updates) > 0 {
			if uErr := db.WithContext(ctx).Model(&existing).Updates(updates).Error; uErr != nil {
				return created, updated, deactivated, uErr
			}
			updated++
		}
	}

	// 若平台表中记录已被物理移除，则自动停用对应契约仓（豁免 R4 系统共享基座虚拟仓）
	for _, gr := range existingRepos {
		if gr.PlatformRepoID == SystemSharedBasePlatformRepoID {
			continue
		}
		if _, ok := platByID[gr.PlatformRepoID]; !ok {
			if gr.SyncEnabled || gr.ProtoStatus != models.GitRepoProtoStatusExcluded {
				_ = db.WithContext(ctx).Model(&gr).Updates(map[string]any{
					"sync_enabled": false,
					"proto_status": models.GitRepoProtoStatusExcluded,
				}).Error
				deactivated++
			}
		}
	}

	return created, updated, deactivated, nil
}

// RunSyncAllJob 对齐平台仓库并启动持久化批处理同步作业（CACHE_ONLY / FETCH_MISSING）。
func RunSyncAllJob(
	ctx context.Context,
	db *gorm.DB,
	sched *SyncScheduler,
	cfg *config.Config,
	req SyncAllRequest,
) (*models.ProtoSyncJob, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if cfg == nil {
		cfg = &config.AppConfig
	}
	if sched == nil {
		sched = NewSyncScheduler(db, nil, nil, cfg)
	}

	scope := strings.ToUpper(strings.TrimSpace(req.Scope))
	switch scope {
	case models.SyncJobScopeDepartment, models.SyncJobScopeServiceGroup, models.SyncJobScopeFailedOnly, models.SyncJobScopeSelected:
	default:
		scope = models.SyncJobScopeAll
	}

	policy := strings.ToUpper(strings.TrimSpace(req.CachePolicy))
	if policy != models.SyncCachePolicyFetchMissing {
		policy = models.SyncCachePolicyCacheOnly
	}
	if policy == models.SyncCachePolicyFetchMissing && !cfg.Storage.SharedWriteEnabled {
		return nil, ErrSharedWriteDisabled
	}

	if _, _, _, alignErr := AlignPlatformRepositories(ctx, db); alignErr != nil {
		return nil, fmt.Errorf("对齐平台代码仓失败: %w", alignErr)
	}

	q := db.WithContext(ctx).Model(&models.GitRepo{}).
		Joins("JOIN repositories r ON r.id = contract_git_repos.platform_repo_id").
		Where("r.is_active = true AND contract_git_repos.sync_enabled = true")

	switch scope {
	case models.SyncJobScopeDepartment:
		if req.DepartmentID > 0 {
			q = q.Where("r.department_id = ?", req.DepartmentID)
		}
	case models.SyncJobScopeServiceGroup:
		if sg := strings.TrimSpace(req.ServiceGroup); sg != "" {
			q = q.Where("r.service_group = ?", sg)
		}
	case models.SyncJobScopeFailedOnly:
		q = q.Where("(contract_git_repos.proto_status = ? OR contract_git_repos.last_sync_status = ?)",
			models.GitRepoProtoStatusSyncFailed, models.GitSyncStatusFailed)
	case models.SyncJobScopeSelected:
		if len(req.RepoIDs) > 0 {
			q = q.Where("contract_git_repos.id IN ?", req.RepoIDs)
		} else {
			q = q.Where("1 = 0")
		}
	}

	var targets []models.GitRepo
	if err := q.Order("contract_git_repos.id ASC").Find(&targets).Error; err != nil {
		return nil, err
	}

	var govDoc GovernanceConfigDoc
	if snap, gErr := LoadActiveGovernanceConfig(ctx, db); gErr == nil && snap != nil {
		govDoc = snap.Doc
	} else {
		govDoc = DefaultGovernanceConfig()
	}

	var filteredTargets []models.GitRepo
	if len(govDoc.Global.ExcludeSubsystems) > 0 || len(govDoc.Global.ExcludeRepoPatterns) > 0 {
		var plats []models.Repository
		_ = db.WithContext(ctx).Select("id, service_group").Find(&plats).Error
		platSub := make(map[uint64]string, len(plats))
		for _, p := range plats {
			platSub[uint64(p.ID)] = p.ServiceGroup
		}
		treeMgr := NewProtoTreeManager(cfg.Storage.GetProtosDir())
		for _, r := range targets {
			if r.PlatformRepoID == SystemSharedBasePlatformRepoID {
				filteredTargets = append(filteredTargets, r)
				continue
			}
			sub := platSub[r.PlatformRepoID]
			if sub == "" {
				sub = r.SubsystemSlug
			}
			if ShouldExcludeRepoByR0(sub, r.Name, govDoc.Global) {
				_ = db.WithContext(ctx).Model(&r).Updates(map[string]any{
					"sync_enabled": false,
					"proto_status": models.GitRepoProtoStatusExcluded,
				}).Error
				treeMgr.RemoveRepoTree(TreeKindClean, r.ID)
				continue
			}
			filteredTargets = append(filteredTargets, r)
		}
		targets = filteredTargets
	}

	filterBytes, _ := json.Marshal(map[string]any{
		"department_id":   req.DepartmentID,
		"service_group":   strings.TrimSpace(req.ServiceGroup),
		"repo_ids":        req.RepoIDs,
		"force_recompile": req.ForceRecompile,
	})
	now := time.Now().UTC()
	job := models.ProtoSyncJob{
		Scope:            scope,
		CachePolicy:      policy,
		FilterJSON:       datatypes.JSON(filterBytes),
		Status:           models.SyncJobStatusRunning,
		TotalRepos:       len(targets),
		ErrorSummaryJSON: datatypes.JSON([]byte("[]")),
		TriggeredBy:      strings.TrimSpace(req.TriggeredBy),
		StartedAt:        now,
	}
	if len(targets) == 0 {
		job.Status = models.SyncJobStatusCompleted
		job.FinishedAt = &now
	}
	if err := db.WithContext(ctx).Create(&job).Error; err != nil {
		return nil, err
	}

	emitProgress := func(j models.ProtoSyncJob, errItems []SyncJobErrorItem) {
		if sched.jobBroadcaster == nil {
			return
		}
		sched.jobBroadcaster(SyncJobProgressEvent{
			JobID:                 j.ID,
			Scope:                 j.Scope,
			CachePolicy:           j.CachePolicy,
			Status:                j.Status,
			TotalRepos:            j.TotalRepos,
			ProcessedRepos:        j.ProcessedRepos,
			HasProtoCount:         j.HasProtoCount,
			NoProtoCount:          j.NoProtoCount,
			NoCacheCount:          j.NoCacheCount,
			FailedCount:           j.FailedCount,
			SkippedUnchangedCount: j.SkippedUnchangedCount,
			ErrorSummary:          errItems,
			StartedAt:             j.StartedAt,
			FinishedAt:            j.FinishedAt,
		})
	}

	if len(targets) == 0 {
		emitProgress(job, nil)
		return &job, nil
	}

	runJob := func(runCtx context.Context) {
		conc := cfg.ProtoCollect.SyncConcurrency
		if conc <= 0 {
			conc = 4
		}
		if conc > len(targets) {
			conc = len(targets)
		}

		ch := make(chan models.GitRepo, len(targets))
		for _, t := range targets {
			ch <- t
		}
		close(ch)

		var (
			mu       sync.Mutex
			wg       sync.WaitGroup
			errItems []SyncJobErrorItem
		)
		flushJob := func(final bool) {
			errBytes, _ := json.Marshal(errItems)
			updates := map[string]any{
				"processed_repos":         job.ProcessedRepos,
				"has_proto_count":         job.HasProtoCount,
				"no_proto_count":          job.NoProtoCount,
				"no_cache_count":          job.NoCacheCount,
				"failed_count":            job.FailedCount,
				"skipped_unchanged_count": job.SkippedUnchangedCount,
				"error_summary_json":      datatypes.JSON(errBytes),
			}
			if final {
				fin := time.Now().UTC()
				job.FinishedAt = &fin
				switch {
				case job.FailedCount == 0:
					job.Status = models.SyncJobStatusCompleted
				case job.FailedCount < job.TotalRepos:
					job.Status = models.SyncJobStatusPartialFailed
				default:
					job.Status = models.SyncJobStatusFailed
				}
				updates["status"] = job.Status
				updates["finished_at"] = fin
			}
			if err := db.WithContext(runCtx).Model(&models.ProtoSyncJob{}).Where("id = ?", job.ID).Updates(updates).Error; err != nil {
				log.Printf("[SyncAllJob#%d] 更新进度失败: %v", job.ID, err)
			}
			snapshot := job
			errCopy := append([]SyncJobErrorItem(nil), errItems...)
			emitProgress(snapshot, errCopy)
		}

		worker := func() {
			defer wg.Done()
			for repo := range ch {
				if runCtx.Err() != nil {
					return
				}
				res, sErr := sched.TriggerRepoSync(runCtx, repo.ID, PipelineOptions{
					OperatorID:            req.OperatorID,
					CachePolicy:           policy,
					SkipIfCommitUnchanged: !req.ForceRecompile,
					ForceRecompile:        req.ForceRecompile,
					CodesDir:              cfg.Storage.GetCodesDir(),
					ProtosDir:             cfg.Storage.GetProtosDir(),
					SharedWriteEnabled:    cfg.Storage.SharedWriteEnabled,
					TriggerBlastRadius:    true,
				})

				mu.Lock()
				job.ProcessedRepos++
				if sErr != nil {
					job.FailedCount++
					errItems = append(errItems, SyncJobErrorItem{
						GitRepoID: repo.ID,
						RepoName:  repo.Name,
						Error:     sErr.Error(),
					})
				} else if res != nil {
					if res.SkippedUnchanged {
						job.SkippedUnchangedCount++
					}
					switch res.ProtoStatus {
					case models.GitRepoProtoStatusNoLocalCache:
						job.NoCacheCount++
					case models.GitRepoProtoStatusNoProto:
						job.NoProtoCount++
					case models.GitRepoProtoStatusHasProto:
						job.HasProtoCount++
					default:
						if repo.ProtoStatus == models.GitRepoProtoStatusNoProto {
							job.NoProtoCount++
						} else if repo.ProtoStatus == models.GitRepoProtoStatusHasProto {
							job.HasProtoCount++
						}
					}
				}
				shouldFlush := job.ProcessedRepos == job.TotalRepos || job.ProcessedRepos%5 == 0
				if shouldFlush && job.ProcessedRepos < job.TotalRepos {
					flushJob(false)
				}
				mu.Unlock()
			}
		}

		wg.Add(conc)
		for i := 0; i < conc; i++ {
			go worker()
		}
		wg.Wait()

		mu.Lock()
		flushJob(true)
		mu.Unlock()
	}

	if req.WaitSync {
		runJob(ctx)
		_ = db.WithContext(ctx).First(&job, job.ID).Error
		return &job, nil
	}

	go runJob(context.Background())
	return &job, nil
}

// GetSyncSummary 汇总 500+ 平台仓 6 态分布与最近一次同步任务状态。
func GetSyncSummary(ctx context.Context, db *gorm.DB, sharedWriteEnabled bool) (*SyncSummaryDTO, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}

	summary := &SyncSummaryDTO{
		SharedWriteEnabled: sharedWriteEnabled,
		ServiceGroups:      []string{},
	}

	_ = db.WithContext(ctx).Model(&models.Repository{}).Where("is_active = true").Count(&summary.PlatformActiveTotal).Error
	_ = db.WithContext(ctx).Model(&models.GitRepo{}).Count(&summary.AlignedTotal).Error

	type statusCount struct {
		ProtoStatus string
		Cnt         int64
	}
	var counts []statusCount
	if err := db.WithContext(ctx).Model(&models.GitRepo{}).
		Select("proto_status, COUNT(*) AS cnt").
		Group("proto_status").
		Scan(&counts).Error; err != nil {
		return nil, err
	}
	for _, sc := range counts {
		switch sc.ProtoStatus {
		case models.GitRepoProtoStatusPending:
			summary.PendingCount += sc.Cnt
		case models.GitRepoProtoStatusHasProto:
			summary.HasProtoCount += sc.Cnt
		case models.GitRepoProtoStatusNoProto:
			summary.NoProtoCount += sc.Cnt
		case models.GitRepoProtoStatusNoLocalCache:
			summary.NoLocalCacheCount += sc.Cnt
		case models.GitRepoProtoStatusExcluded:
			summary.ExcludedCount += sc.Cnt
		case models.GitRepoProtoStatusSyncFailed:
			summary.SyncFailedCount += sc.Cnt
		}
	}

	var groups []string
	if err := db.WithContext(ctx).Model(&models.Repository{}).
		Where("is_active = true AND service_group <> ''").
		Distinct("service_group").
		Pluck("service_group", &groups).Error; err == nil {
		sort.Strings(groups)
		summary.ServiceGroups = groups
	}

	var latest models.ProtoSyncJob
	if err := db.WithContext(ctx).Order("id DESC").Limit(1).Find(&latest).Error; err == nil && latest.ID > 0 {
		summary.LatestJob = &latest
	}
	return summary, nil
}
