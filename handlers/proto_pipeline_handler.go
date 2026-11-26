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
		return sched
	}
	globalSyncSchedulerMu.Lock()
	defer globalSyncSchedulerMu.Unlock()
	if globalSyncScheduler == nil {
		globalSyncScheduler = gitclient.NewSyncScheduler(db, reg, MakeSSEEventBroadcaster(broker), &config.AppConfig)
	}
	return globalSyncScheduler
}

// DiscoverPlatformReposHandler 从公共 repositories 发现平台代码仓并探测全小写 pg/ 目录 (GET /api/git-repos/platform-discover)。
func DiscoverPlatformReposHandler(c *gin.Context) {
	var depID uint64
	if raw := strings.TrimSpace(c.Query("department_id")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "非法的 department_id 参数"})
			return
		}
		depID = parsed
	}
	page, pageSize := parseStandardPagination(c)
	probeProtos := true
	if rawProbe := strings.TrimSpace(c.Query("probe_protos")); rawProbe == "false" || rawProbe == "0" {
		probeProtos = false
	}

	res, err := gitclient.DiscoverPlatformRepos(c.Request.Context(), models.DB, config.AppConfig.Storage.GetCodesDir(), gitclient.PlatformDiscoverFilter{
		DepartmentID: depID,
		ServiceGroup: strings.TrimSpace(c.Query("service_group")),
		Search:       strings.TrimSpace(c.Query("search")),
		ProbeProtos:  probeProtos,
		Page:         page,
		PageSize:     pageSize,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// ImportPlatformReposHandler 批量将平台代码仓以 PLATFORM_SHARED 模式纳管并创建候选 Bundle (POST /api/git-repos/platform-import)。
func ImportPlatformReposHandler(c *gin.Context) {
	var req struct {
		Items []gitclient.PlatformRepoImportItem `json:"items"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "items 不能为空"})
		return
	}

	uid, opName := currentOperatorName(c)
	created, err := gitclient.BatchImportPlatformRepos(
		c.Request.Context(),
		models.DB,
		config.AppConfig.Storage.GetCodesDir(),
		uid,
		req.Items,
	)
	if err != nil {
		errMsg := err.Error()
		switch {
		case strings.Contains(errMsg, "已纳管"),
			strings.Contains(errMsg, "duplicate key"),
			strings.Contains(errMsg, "UNIQUE constraint"):
			c.JSON(http.StatusConflict, gin.H{"error": errMsg})
		case strings.Contains(errMsg, "未找到"):
			c.JSON(http.StatusNotFound, gin.H{"error": errMsg})
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
		}
		return
	}

	recordAudit(c, "contract_git:platform_import", commonModels.AuditLevelP2,
		fmt.Sprintf("管理员 [%s] 批量导入 %d 个平台共享代码仓", opName, len(created)),
		"git_repo", 0, "platform-import", nil, gin.H{"count": len(created), "items": created})

	c.JSON(http.StatusCreated, gin.H{
		"imported_count": len(created),
		"items":          created,
	})
}

// LinkPlatformRepoHandler 将已有 EXTERNAL_STANDALONE 仓库关联升级为 PLATFORM_SHARED 并清理冗余凭据 (POST /api/git-repos/:id/link-platform)。
func LinkPlatformRepoHandler(c *gin.Context) {
	repoID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || repoID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的 git_repo ID"})
		return
	}

	var req struct {
		PlatformRepoID uint64 `json:"platform_repo_id"`
		SyncStrategy   string `json:"sync_strategy"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	var before models.GitRepo
	_ = models.DB.First(&before, repoID).Error

	updated, err := gitclient.LinkLegacyRepoToPlatform(c.Request.Context(), models.DB, repoID, req.PlatformRepoID, req.SyncStrategy)
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound) || strings.Contains(err.Error(), "未找到"):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "已关联") || strings.Contains(err.Error(), "UNIQUE"):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		return
	}

	var bundles []models.GitProtoBundle
	_ = models.DB.Where("git_repo_id = ?", updated.ID).Order("id ASC").Find(&bundles).Error
	_, opName := currentOperatorName(c)

	recordAudit(c, "contract_git:link_platform", commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 将仓库 [%s] 关联至平台仓 #%d 并清理历史独立凭据", opName, updated.Name, derefUint64Ptr(updated.PlatformRepoID)),
		"git_repo", updated.ID, updated.Name,
		map[string]any{"source_mode": before.SourceMode, "platform_repo_id": before.PlatformRepoID, "auth_type": before.AuthType},
		map[string]any{"source_mode": updated.SourceMode, "platform_repo_id": updated.PlatformRepoID, "auth_type": updated.AuthType},
	)

	c.JSON(http.StatusOK, toGitRepoResponseDTO(*updated, bundles))
}

func derefUint64Ptr(p *uint64) uint64 {
	if p == nil {
		return 0
	}
	return *p
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
	SubsystemSlug string         `json:"subsystem_slug"`
	GitRepoID     uint64         `json:"git_repo_id"`
	GitRepoName   string         `json:"git_repo_name"`
	SourceMode    string         `json:"source_mode"`
	SyncStrategy  string         `json:"sync_strategy"`
	IsStale       bool           `json:"is_stale"`
	LastSyncAt    *time.Time     `json:"last_sync_at,omitempty"`
	Files         []ProtoFileDTO `json:"files"`
}

// ProtoTreePackageGroup 按 Proto package 逻辑域视图组织的文件树分面节点。
type ProtoTreePackageGroup struct {
	PackageName string         `json:"package_name"`
	Files       []ProtoFileDTO `json:"files"`
}

// GetProtoFilesTreeHandler 返回双视图树（subsystem | package）只读清单与统计 (GET /api/proto-files/tree)。
func GetProtoFilesTreeHandler(c *gin.Context) {
	view := strings.ToLower(strings.TrimSpace(c.DefaultQuery("view", "subsystem")))
	if view != "subsystem" && view != "package" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的 view 参数，仅允许 subsystem 或 package"})
		return
	}

	q := models.DB.Model(&models.ProtoFile{})
	if strings.TrimSpace(c.Query("include_deleted")) != "true" {
		q = q.Where("is_deleted = false")
	}
	if pStatus := strings.ToUpper(strings.TrimSpace(c.Query("pipeline_status"))); pStatus != "" {
		q = q.Where("pipeline_status = ?", pStatus)
	}
	if incExt := strings.TrimSpace(c.Query("include_external_copy")); incExt == "false" {
		q = q.Where("is_external_copy = false")
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

	var repos []models.GitRepo
	_ = models.DB.Order("id ASC").Find(&repos).Error
	repoByID := make(map[uint64]models.GitRepo, len(repos))
	for _, r := range repos {
		repoByID[r.ID] = r
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
			groups = append(groups, ProtoTreePackageGroup{
				PackageName: pkg,
				Files:       filesInPkg,
			})
		}
		c.JSON(http.StatusOK, gin.H{
			"view":   view,
			"total":  len(dtos),
			"groups": groups,
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
			subSlug := r.SubsystemSlug
			if strings.TrimSpace(subSlug) == "" {
				subSlug = gitclient.UnassignedSubsystemSlug
			}
			srcMode := r.SourceMode
			if srcMode == "" {
				srcMode = models.GitSourceModeExternalStandalone
			}
			syncStrat := r.SyncStrategy
			if syncStrat == "" {
				syncStrat = models.GitSyncStrategyFetchAndRead
			}
			repoName := r.Name
			if repoName == "" {
				repoName = d.GitRepoName
			}
			grp = &ProtoTreeRepoGroup{
				SubsystemSlug: subSlug,
				GitRepoID:     rid,
				GitRepoName:   repoName,
				SourceMode:    srcMode,
				SyncStrategy:  syncStrat,
				IsStale:       computeRepoIsStale(r),
				LastSyncAt:    r.LastSyncAt,
			}
			repoGroupsMap[rid] = grp
		}
		grp.Files = append(grp.Files, d)
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
		"view":   view,
		"total":  len(dtos),
		"groups": groups,
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
