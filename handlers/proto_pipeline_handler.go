package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"
	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/gitclient"
	"code-contract/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	globalSyncScheduler   *gitclient.SyncScheduler
	globalSyncSchedulerMu sync.RWMutex
)

// MakeSSEEventBroadcaster 将全局 SSEBroker 适配为 gitclient.EventBroadcaster。
func MakeSSEEventBroadcaster(broker *SSEBroker) gitclient.EventBroadcaster {
	if broker == nil {
		return nil
	}
	return func(event gitclient.GitSyncEventPayload) {
		broker.BroadcastGitSyncCompleted(event)
	}
}

// MakeSyncJobProgressBroadcaster 将全局 SSEBroker 适配为 gitclient.SyncJobBroadcaster。
func MakeSyncJobProgressBroadcaster(broker *SSEBroker) gitclient.SyncJobBroadcaster {
	if broker == nil {
		return nil
	}
	return func(evt gitclient.SyncJobProgressEvent) {
		broker.BroadcastProtoSyncJobProgress(evt)
	}
}

// InitGlobalSyncScheduler 注册全局 ADR-26 SyncScheduler 单例。
func InitGlobalSyncScheduler(sched *gitclient.SyncScheduler) {
	globalSyncSchedulerMu.Lock()
	defer globalSyncSchedulerMu.Unlock()
	globalSyncScheduler = sched
}

// GetGlobalSyncScheduler 读取已初始化的全局 SyncScheduler。
func GetGlobalSyncScheduler() *gitclient.SyncScheduler {
	globalSyncSchedulerMu.RLock()
	defer globalSyncSchedulerMu.RUnlock()
	return globalSyncScheduler
}

// EnsureSyncScheduler 获取或按需懒初始化全局 SyncScheduler。
func EnsureSyncScheduler(db *gorm.DB, reg *decoder.Registry, broker *SSEBroker) *gitclient.SyncScheduler {
	if sched := GetGlobalSyncScheduler(); sched != nil {
		if broker != nil {
			sched.SetJobBroadcaster(MakeSyncJobProgressBroadcaster(broker))
		}
		return sched
	}
	globalSyncSchedulerMu.Lock()
	defer globalSyncSchedulerMu.Unlock()
	if globalSyncScheduler == nil {
		globalSyncScheduler = gitclient.NewSyncScheduler(db, reg, MakeSSEEventBroadcaster(broker), &config.AppConfig)
	}
	if broker != nil {
		globalSyncScheduler.SetJobBroadcaster(MakeSyncJobProgressBroadcaster(broker))
	}
	return globalSyncScheduler
}

// SyncAllGitReposHandler 对齐 code-bench repositories 并启动持久化双按钮批处理同步作业 (POST /api/git-repos/sync-all)。
func SyncAllGitReposHandler(c *gin.Context) {
	var req gitclient.SyncAllRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法: " + err.Error()})
		return
	}
	uid, opName := currentOperatorName(c)
	req.OperatorID = uid
	req.TriggeredBy = opName

	decReg, _ := GetDecoderRegistry()
	broker := GetGlobalSSEBroker()
	sched := EnsureSyncScheduler(models.DB, decReg, broker)

	job, err := gitclient.RunSyncAllJob(c.Request.Context(), models.DB, sched, &config.AppConfig, req)
	if err != nil {
		if handlePipelineLockOrStaleError(c, err) {
			return
		}
		if errors.Is(err, gitclient.ErrSharedWriteDisabled) {
			c.JSON(http.StatusBadRequest, gin.H{
				"err_code": "SHARED_WRITE_DISABLED",
				"error":    err.Error(),
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	recordAudit(c, "contract_git:sync_all_repos", commonModels.AuditLevelP2,
		fmt.Sprintf("管理员 [%s] 发起全量平台仓同步任务 #%d (%s / %s，共 %d 仓)", opName, job.ID, job.Scope, job.CachePolicy, job.TotalRepos),
		"proto_sync_job", job.ID, fmt.Sprintf("job-%d", job.ID), nil, job)

	c.JSON(http.StatusAccepted, job)
}

// GetGitReposSyncSummaryHandler 返回 500+ 平台仓 6 态统计汇总与最近任务状态 (GET /api/git-repos/sync-summary)。
func GetGitReposSyncSummaryHandler(c *gin.Context) {
	if strings.EqualFold(strings.TrimSpace(c.Query("align")), "true") {
		_, _, _, _ = gitclient.AlignPlatformRepositories(c.Request.Context(), models.DB)
	}
	summary, err := gitclient.GetSyncSummary(c.Request.Context(), models.DB, config.AppConfig.Storage.SharedWriteEnabled)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, summary)
}

// GetProtoSyncJobHandler 查询持久化异步批处理任务进度与失败仓明细 (GET /api/git-repos/sync-jobs/:jobId)。
func GetProtoSyncJobHandler(c *gin.Context) {
	jobID, err := strconv.ParseUint(c.Param("jobId"), 10, 64)
	if err != nil || jobID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的 jobId 参数"})
		return
	}
	var job models.ProtoSyncJob
	if err := models.DB.First(&job, jobID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "未找到指定的同步任务"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var errItems []gitclient.SyncJobErrorItem
	if len(job.ErrorSummaryJSON) > 0 {
		_ = json.Unmarshal(job.ErrorSummaryJSON, &errItems)
	}
	c.JSON(http.StatusOK, gin.H{
		"job":           job,
		"error_summary": errItems,
	})
}

// BatchSyncGitReposHandler 经 SyncScheduler 并发限速派发批量四段式流水线同步 (POST /api/git-repos/batch-sync)。
func BatchSyncGitReposHandler(c *gin.Context) {
	var req struct {
		RepoIDs        []uint64 `json:"repo_ids"`
		Branch         string   `json:"branch"`
		ForceRecompile bool     `json:"force_recompile"`
		Sync           bool     `json:"sync"` // 可选：仅测试/同步调用时等待完成
	}
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	repoIDs := req.RepoIDs
	if len(repoIDs) == 0 {
		var rows []models.GitRepo
		if err := models.DB.Order("id ASC").Find(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		for _, r := range rows {
			repoIDs = append(repoIDs, r.ID)
		}
	}

	uid, opName := currentOperatorName(c)
	decReg, _ := GetDecoderRegistry()
	db := models.DB
	broker := GetGlobalSSEBroker()
	sched := EnsureSyncScheduler(db, decReg, broker)

	opts := gitclient.PipelineOptions{
		Branch:             strings.TrimSpace(req.Branch),
		OperatorID:         uid,
		ForceRecompile:     req.ForceRecompile,
		TriggerBlastRadius: true,
	}

	if req.Sync {
		errMap := sched.BatchTriggerSync(c.Request.Context(), repoIDs, opts)
		errsOut := make(map[string]string, len(errMap))
		for k, v := range errMap {
			if v != nil {
				errsOut[strconv.FormatUint(k, 10)] = v.Error()
			}
		}
		recordAudit(c, "contract_git:batch_sync", commonModels.AuditLevelP2,
			fmt.Sprintf("管理员 [%s] 触发批量四段式契约同步 (%d 仓)", opName, len(repoIDs)),
			"git_repo", 0, "batch-sync", nil, gin.H{"repo_ids": repoIDs})
		c.JSON(http.StatusAccepted, gin.H{
			"status":   "completed",
			"repo_ids": repoIDs,
			"errors":   errsOut,
		})
		return
	}

	go func(ids []uint64, o gitclient.PipelineOptions) {
		bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		_ = sched.BatchTriggerSync(bgCtx, ids, o)
	}(append([]uint64(nil), repoIDs...), opts)

	recordAudit(c, "contract_git:batch_sync", commonModels.AuditLevelP2,
		fmt.Sprintf("管理员 [%s] 派发批量四段式契约同步 (%d 仓)", opName, len(repoIDs)),
		"git_repo", 0, "batch-sync", nil, gin.H{"repo_ids": repoIDs})

	c.JSON(http.StatusAccepted, gin.H{
		"status":   models.GitSyncStatusRunning,
		"repo_ids": repoIDs,
	})
}

// ReNormalizeGitRepoHandler 基于 DB raw 快照免网络重跑 Stage B~D (POST /api/git-repos/:id/re-normalize)。
func ReNormalizeGitRepoHandler(c *gin.Context) {
	repoID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || repoID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的 git_repo ID"})
		return
	}
	var req struct {
		Force bool `json:"force"`
	}
	_ = c.ShouldBindJSON(&req)

	uid, opName := currentOperatorName(c)
	decReg, _ := GetDecoderRegistry()
	res, err := gitclient.ReNormalizeRepo(
		c.Request.Context(),
		models.DB,
		decReg,
		MakeSSEEventBroadcaster(GetGlobalSSEBroker()),
		repoID,
		gitclient.PipelineOptions{
			OperatorID:         uid,
			ForceRenormalize:   true,
			ForceRecompile:     req.Force,
			TriggerBlastRadius: true,
		},
	)
	if err != nil {
		if handlePipelineLockOrStaleError(c, err) {
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gitclient.ErrGitRepoNotFound.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	recordAudit(c, "contract_git:re_normalize", commonModels.AuditLevelP2,
		fmt.Sprintf("管理员 [%s] 对仓库 [%s] 免网络重跑 Stage B~D 清理清洗", opName, res.RepoName),
		"git_repo", repoID, res.RepoName, nil, res)

	c.JSON(http.StatusOK, res)
}

// GetGitReposBaselineReportHandler 输出全平台共享仓采集与基线试编译报告 (GET /api/git-repos/baseline-report)。
func GetGitReposBaselineReportHandler(c *gin.Context) {
	report, err := gitclient.GenerateBaselineReport(c.Request.Context(), models.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, report)
}

// GetGitReposArchSuggestionsHandler 输出平台仓与架构 Subsystem/Module 推荐绑定清单 (GET /api/git-repos/arch-suggestions)。
func GetGitReposArchSuggestionsHandler(c *gin.Context) {
	items, err := gitclient.SuggestArchAssignments(c.Request.Context(), models.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// ProtoTreeRepoGroup 按子系统/仓库视图组织的文件树分面节点。
type ProtoTreeRepoGroup struct {
	SubsystemSlug       string         `json:"subsystem_slug"`
	RawSubsystemSlug    string         `json:"raw_subsystem_slug,omitempty"`
	ExportTargetRepoDir string         `json:"export_target_repo_dir,omitempty"`
	GitRepoID           uint64         `json:"git_repo_id"`
	GitRepoName         string         `json:"git_repo_name"`
	SourceMode          string         `json:"source_mode"`
	SyncStrategy        string         `json:"sync_strategy"`
	IsStale             bool           `json:"is_stale"`
	LastSyncAt          *time.Time     `json:"last_sync_at,omitempty"`
	TotalCount          int            `json:"total_count"`
	NormalizedCount     int            `json:"normalized_count"`
	QuarantinedCount    int            `json:"quarantined_count"`
	Files               []ProtoFileDTO `json:"files"`
}

// ProtoTreePackageGroup 按 Proto package 逻辑域视图组织的文件树分面节点。
type ProtoTreePackageGroup struct {
	PackageName      string         `json:"package_name"`
	TotalCount       int            `json:"total_count"`
	NormalizedCount  int            `json:"normalized_count"`
	QuarantinedCount int            `json:"quarantined_count"`
	Files            []ProtoFileDTO `json:"files"`
}

// GetProtoFilesTreeHandler 返回双视图树（subsystem | package）只读清单、摘要统计与按需懒加载节点 (GET /api/proto-files/tree)。
func GetProtoFilesTreeHandler(c *gin.Context) {
	view := strings.ToLower(strings.TrimSpace(c.DefaultQuery("view", "subsystem")))
	if view != "subsystem" && view != "package" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的 view 参数，仅允许 subsystem 或 package"})
		return
	}

	summaryQ := strings.TrimSpace(c.Query("summary_only"))
	summaryOnly := strings.EqualFold(summaryQ, "true") || summaryQ == "1"
	subsystemFilter := strings.TrimSpace(c.Query("subsystem"))
	expandPkg := strings.TrimSpace(c.Query("expand_package"))

	expandRepoID, okExpand := parseOptionalUint64Query(c, "expand_repo_id")
	if !okExpand {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的 expand_repo_id 参数"})
		return
	}

	var repos []models.GitRepo
	_ = models.DB.Order("id ASC").Find(&repos).Error
	repoByID := make(map[uint64]models.GitRepo, len(repos))
	disabledRepoIDs := make([]uint64, 0)
	enabledSubRepoIDs := make([]uint64, 0)
	for _, r := range repos {
		repoByID[r.ID] = r
		if !r.SyncEnabled {
			disabledRepoIDs = append(disabledRepoIDs, r.ID)
			continue
		}
		if subsystemFilter != "" {
			effSub, _ := gitclient.ResolveEffectiveExportTarget(r, nil)
			if strings.EqualFold(effSub, subsystemFilter) || strings.EqualFold(r.SubsystemSlug, subsystemFilter) {
				enabledSubRepoIDs = append(enabledSubRepoIDs, r.ID)
			}
		}
	}

	q := models.DB.Model(&models.ProtoFile{})
	if strings.TrimSpace(c.Query("include_deleted")) != "true" {
		q = q.Where("is_deleted = false")
	}
	if len(disabledRepoIDs) > 0 {
		q = q.Where("git_repo_id IS NULL OR git_repo_id NOT IN ?", disabledRepoIDs)
	}
	if subsystemFilter != "" {
		if len(enabledSubRepoIDs) == 0 {
			c.JSON(http.StatusOK, gin.H{
				"view":         view,
				"total":        0,
				"summary_only": summaryOnly,
				"groups":       []any{},
			})
			return
		}
		q = q.Where("git_repo_id IN ?", enabledSubRepoIDs)
	}
	if pStatus := strings.ToUpper(strings.TrimSpace(c.Query("pipeline_status"))); pStatus != "" {
		q = q.Where("pipeline_status = ?", pStatus)
	}
	if incExt := strings.TrimSpace(c.Query("include_external_copy")); incExt == "false" {
		q = q.Where("is_external_copy = false")
	}
	if closureRaw := strings.TrimSpace(c.Query("in_service_closure")); closureRaw != "" {
		if b, bErr := strconv.ParseBool(closureRaw); bErr == nil {
			q = q.Where("in_service_closure = ?", b)
		}
	}
	if usvcRaw := strings.TrimSpace(c.Query("is_uservice_source")); usvcRaw != "" {
		if b, bErr := strconv.ParseBool(usvcRaw); bErr == nil {
			q = q.Where("is_uservice_source = ?", b)
		}
	}
	if repoID, ok := parseOptionalUint64Query(c, "git_repo_id"); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的 git_repo_id 参数"})
		return
	} else if repoID != nil {
		q = q.Where("git_repo_id = ?", *repoID)
	}
	if search := strings.TrimSpace(c.Query("search")); search != "" {
		like := "%" + search + "%"
		q = q.Where("file_path ILIKE ? OR repo_file_path ILIKE ? OR package_name ILIKE ? OR target_schema_name ILIKE ?",
			like, like, like, like)
	}

	var files []models.ProtoFile
	if err := q.Order("file_path ASC, id ASC").Find(&files).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	repoNames, bundleNames, schemaByID, latestVerSchemaByFile, activeSchemaByName := resolveProtoFileRelations(models.DB, files)
	dtos := make([]ProtoFileDTO, 0, len(files))
	for _, f := range files {
		dtos = append(dtos, buildGitProtoFileDTO(f, repoNames, bundleNames, schemaByID, latestVerSchemaByFile, activeSchemaByName))
	}

	if view == "package" {
		pkgMap := make(map[string][]ProtoFileDTO)
		for _, d := range dtos {
			pkg := strings.TrimSpace(d.PackageName)
			if pkg == "" {
				pkg = "(default)"
			}
			pkgMap[pkg] = append(pkgMap[pkg], d)
		}
		pkgs := make([]string, 0, len(pkgMap))
		for k := range pkgMap {
			pkgs = append(pkgs, k)
		}
		sort.Strings(pkgs)
		groups := make([]ProtoTreePackageGroup, 0, len(pkgs))
		for _, pkg := range pkgs {
			filesInPkg := pkgMap[pkg]
			sort.Slice(filesInPkg, func(i, j int) bool {
				return filesInPkg[i].FilePath < filesInPkg[j].FilePath
			})
			normCnt, quarCnt := 0, 0
			for _, f := range filesInPkg {
				switch f.PipelineStatus {
				case models.ProtoPipelineNormalized:
					normCnt++
				case models.ProtoPipelineQuarantined:
					quarCnt++
				}
			}
			outFiles := filesInPkg
			if summaryOnly && (expandPkg == "" || expandPkg != pkg) {
				outFiles = []ProtoFileDTO{}
			}
			groups = append(groups, ProtoTreePackageGroup{
				PackageName:      pkg,
				TotalCount:       len(filesInPkg),
				NormalizedCount:  normCnt,
				QuarantinedCount: quarCnt,
				Files:            outFiles,
			})
		}
		c.JSON(http.StatusOK, gin.H{
			"view":         view,
			"total":        len(dtos),
			"summary_only": summaryOnly,
			"groups":       groups,
		})
		return
	}

	repoGroupsMap := make(map[uint64]*ProtoTreeRepoGroup)
	for _, d := range dtos {
		var rid uint64
		if d.GitRepoID != nil {
			rid = *d.GitRepoID
		}
		grp, exists := repoGroupsMap[rid]
		if !exists {
			r := repoByID[rid]
			effSub, effRepoDir := gitclient.ResolveEffectiveExportTarget(r, nil)
			rawSub := r.SubsystemSlug
			if strings.TrimSpace(rawSub) == "" {
				rawSub = gitclient.UnassignedSubsystemSlug
			}
			srcMode := r.SourceMode
			if srcMode == "" {
				srcMode = models.GitSourceModePlatformShared
			}
			syncStrat := r.SyncStrategy
			if syncStrat == "" {
				syncStrat = models.GitSyncStrategyLocalCacheOnly
			}
			repoName := r.Name
			if repoName == "" {
				repoName = d.GitRepoName
			}
			grp = &ProtoTreeRepoGroup{
				SubsystemSlug:       effSub,
				RawSubsystemSlug:    rawSub,
				ExportTargetRepoDir: effRepoDir,
				GitRepoID:           rid,
				GitRepoName:         repoName,
				SourceMode:          srcMode,
				SyncStrategy:        syncStrat,
				IsStale:             computeRepoIsStale(r),
				LastSyncAt:          r.LastSyncAt,
				Files:               []ProtoFileDTO{},
			}
			repoGroupsMap[rid] = grp
		}
		grp.TotalCount++
		switch d.PipelineStatus {
		case models.ProtoPipelineNormalized:
			grp.NormalizedCount++
		case models.ProtoPipelineQuarantined:
			grp.QuarantinedCount++
		}
		if !summaryOnly || (expandRepoID != nil && *expandRepoID == rid) {
			grp.Files = append(grp.Files, d)
		}
	}
	groups := make([]ProtoTreeRepoGroup, 0, len(repoGroupsMap))
	for _, grp := range repoGroupsMap {
		sort.Slice(grp.Files, func(i, j int) bool {
			return grp.Files[i].RepoFilePath < grp.Files[j].RepoFilePath
		})
		groups = append(groups, *grp)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].SubsystemSlug != groups[j].SubsystemSlug {
			return groups[i].SubsystemSlug < groups[j].SubsystemSlug
		}
		return groups[i].GitRepoName < groups[j].GitRepoName
	})

	c.JSON(http.StatusOK, gin.H{
		"view":         view,
		"total":        len(dtos),
		"summary_only": summaryOnly,
		"groups":       groups,
	})
}

// ListProtoImportOwnersHandler 分页返回跨仓导入路径权威归属表 (GET /api/proto-import-owners)。
func ListProtoImportOwnersHandler(c *gin.Context) {
	page, pageSize := parseStandardPagination(c)
	q := models.DB.Model(&models.ProtoImportOwner{})
	if src := strings.ToUpper(strings.TrimSpace(c.Query("source"))); src != "" {
		q = q.Where("source = ?", src)
	}
	if search := strings.TrimSpace(c.Query("search")); search != "" {
		q = q.Where("logical_path ILIKE ?", "%"+search+"%")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var items []models.ProtoImportOwner
	if err := q.Order("logical_path ASC, id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// UpsertProtoImportOwnerHandler 新增或人工覆盖指定逻辑路径的权威提供方 (PUT/POST /api/proto-import-owners)。
func UpsertProtoImportOwnerHandler(c *gin.Context) {
	var req struct {
		LogicalPath    string `json:"logical_path"`
		OwnerGitRepoID uint64 `json:"owner_git_repo_id"`
		OwnerBundleID  uint64 `json:"owner_bundle_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	cleanedPath, sErr := decoder.SanitizeProtoRelPath(req.LogicalPath, true)
	if sErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": sErr.Error()})
		return
	}

	_, opName := currentOperatorName(c)
	var before models.ProtoImportOwner
	_ = models.DB.Where("logical_path = ?", cleanedPath).Limit(1).Find(&before).Error

	owners, err := gitclient.ResolveProtoConflict(c.Request.Context(), models.DB, opName, gitclient.ResolveConflictInput{
		Kind:           "IMPORT",
		LogicalPath:    cleanedPath,
		OwnerGitRepoID: req.OwnerGitRepoID,
		OwnerBundleID:  req.OwnerBundleID,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var owner models.ProtoImportOwner
	if len(owners) > 0 {
		owner = owners[0]
	}
	recordAudit(c, "contract_proto:upsert_import_owner", commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 裁决导入路径 [%s] 权威归属为 Bundle #%d", opName, cleanedPath, req.OwnerBundleID),
		"proto_import_owner", owner.ID, cleanedPath, before, owner)

	c.JSON(http.StatusOK, owner)
}

// DeleteProtoImportOwnerHandler 删除指定权威导入归属记录 (DELETE /api/proto-import-owners/:id)。
func DeleteProtoImportOwnerHandler(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的 import_owner ID"})
		return
	}
	var existing models.ProtoImportOwner
	if err := models.DB.First(&existing, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "权威归属记录不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := models.DB.Delete(&existing).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_, opName := currentOperatorName(c)
	recordAudit(c, "contract_proto:delete_import_owner", commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 删除导入路径 [%s] 的权威归属记录", opName, existing.LogicalPath),
		"proto_import_owner", existing.ID, existing.LogicalPath, existing, nil)

	c.JSON(http.StatusOK, gin.H{"deleted": true, "id": existing.ID})
}

// ListProtoConflictsHandler 返回跨仓导入路径歧义与符号冲突报告 (GET /api/proto-conflicts)。
func ListProtoConflictsHandler(c *gin.Context) {
	report, err := gitclient.DetectConflictsAndMarkExternalCopies(c.Request.Context(), models.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, report)
}

// ResolveProtoConflictHandler 管理员仲裁落定导入或符号冲突的权威提供方 (POST /api/proto-conflicts/resolve)。
func ResolveProtoConflictHandler(c *gin.Context) {
	var req gitclient.ResolveConflictInput
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	_, opName := currentOperatorName(c)
	owners, err := gitclient.ResolveProtoConflict(c.Request.Context(), models.DB, opName, req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var firstID uint64
	if len(owners) > 0 {
		firstID = owners[0].ID
	}
	targetName := req.LogicalPath
	if targetName == "" {
		targetName = req.SymbolFullName
	}
	recordAudit(c, "contract_proto:resolve_conflict", commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 裁决冲突 [%s] (%s) 权威提供方为 Bundle #%d", opName, req.Kind, targetName, req.OwnerBundleID),
		"proto_conflict", firstID, targetName, nil, owners)

	c.JSON(http.StatusOK, gin.H{
		"resolved": true,
		"owners":   owners,
	})
}

// HandleGitSyncWebhook 接收 Push Webhook 常量时间验签并经 SyncScheduler 触发增量同步 (POST /api/webhooks/git-sync/:repoId)。
func HandleGitSyncWebhook(c *gin.Context) {
	commonAudit.Skip(c)

	repoID, err := strconv.ParseUint(c.Param("repoId"), 10, 64)
	if err != nil || repoID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 repoId"})
		return
	}

	rawBody, err := io.ReadAll(io.LimitReader(c.Request.Body, maxWebhookBodyBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取 Webhook 请求体失败"})
		return
	}
	if int64(len(rawBody)) > maxWebhookBodyBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Webhook 载荷体积超限"})
		return
	}

	var repo models.GitRepo
	if err := models.DB.First(&repo, repoID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gitclient.ErrGitRepoNotFound.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	secret, err := repo.DecryptWebhookSecret()
	if err != nil || strings.TrimSpace(secret) == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "该仓库尚未配置 Webhook Secret"})
		return
	}
	if !verifyWebhookConstantTimeSignature(repo.Provider, secret, c.Request.Header, rawBody) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Webhook 签名校验失败"})
		return
	}

	var bodyPayload struct {
		Branch string `json:"branch"`
		Ref    string `json:"ref"`
	}
	if len(rawBody) > 0 {
		_ = json.Unmarshal(rawBody, &bodyPayload)
	}
	branch := strings.TrimSpace(bodyPayload.Branch)
	if branch == "" && strings.HasPrefix(strings.TrimSpace(bodyPayload.Ref), "refs/heads/") {
		branch = strings.TrimPrefix(strings.TrimSpace(bodyPayload.Ref), "refs/heads/")
	}
	if branch == "" {
		branch = repo.DefaultBranch
	}

	decReg, _ := GetDecoderRegistry()
	sched := EnsureSyncScheduler(models.DB, decReg, GetGlobalSSEBroker())
	opts := gitclient.PipelineOptions{
		Branch:             branch,
		TriggerBlastRadius: true,
	}

	if strings.EqualFold(c.Query("sync"), "true") {
		res, syncErr := sched.TriggerRepoSync(c.Request.Context(), repo.ID, opts)
		if syncErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": syncErr.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{
			"status":  "accepted",
			"repo_id": repo.ID,
			"branch":  branch,
			"result":  res,
		})
		return
	}

	go func(id uint64, o gitclient.PipelineOptions) {
		bgCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, _ = sched.TriggerRepoSync(bgCtx, id, o)
	}(repo.ID, opts)

	c.JSON(http.StatusAccepted, gin.H{
		"status":  "accepted",
		"repo_id": repo.ID,
		"branch":  branch,
	})
}
