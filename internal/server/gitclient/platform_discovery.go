package gitclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"code-common/backend/gitsync"
	"code-contract/config"
	"code-contract/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// PlatformDiscoverFilter 定义从 code-bench repositories 批量发现代码仓的查询参数。
type PlatformDiscoverFilter struct {
	DepartmentID uint64 `json:"department_id"`
	ServiceGroup string `json:"service_group"`
	Search       string `json:"search"`
	ProbeProtos  bool   `json:"probe_protos"`
	Page         int    `json:"page"`
	PageSize     int    `json:"page_size"`
}

// CandidateBundlePreview 描述每个 pg 目录自动生成的候选 Bundle。
type CandidateBundlePreview struct {
	BundleName       string `json:"bundle_name"`
	TargetSchemaName string `json:"target_schema_name"`
	Category         string `json:"category"`
	SubdirPath       string `json:"subdir_path"`
}

// PlatformRepoPreviewDTO 平台仓发现预览项。
type PlatformRepoPreviewDTO struct {
	PlatformRepoID        uint64                   `json:"platform_repo_id"`
	Name                  string                   `json:"name"`
	URL                   string                   `json:"url"`
	HTTPURL               string                   `json:"http_url"`
	Branch                string                   `json:"branch"`
	DepartmentID          uint                     `json:"department_id"`
	DepartmentName        string                   `json:"department_name"`
	ServiceGroup          string                   `json:"service_group"`
	SubsystemSlug         string                   `json:"subsystem_slug"`
	OwnerID               uint                     `json:"owner_id"`
	AlreadyImported       bool                     `json:"already_imported"`
	GitRepoID             *uint64                  `json:"git_repo_id,omitempty"`
	LocalCachePresent     bool                     `json:"local_cache_present"`
	ResolvedBranch        string                   `json:"resolved_branch,omitempty"`
	PGDirs                []string                 `json:"pg_dirs"`
	WhitelistProtoCount   int                      `json:"whitelist_proto_count"`
	OutsideWhitelistCount int                      `json:"outside_whitelist_count"`
	CandidateBundles      []CandidateBundlePreview `json:"candidate_bundles"`
	ProbeError            string                   `json:"probe_error,omitempty"`
}

// PlatformDiscoverResult 平台仓批量发现分页结果。
type PlatformDiscoverResult struct {
	Items    []PlatformRepoPreviewDTO `json:"items"`
	Total    int64                    `json:"total"`
	Page     int                      `json:"page"`
	PageSize int                      `json:"page_size"`
}

// DiscoverPlatformRepos 从平台公共 repositories 表按部门/子系统发现代码仓并探测 pg 白名单目录。
func DiscoverPlatformRepos(ctx context.Context, db *gorm.DB, codesDir string, filter PlatformDiscoverFilter) (*PlatformDiscoverResult, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if strings.TrimSpace(codesDir) == "" {
		codesDir = config.AppConfig.Storage.GetCodesDir()
	}
	page := filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 25
	}

	q := db.WithContext(ctx).Model(&models.Repository{}).Preload("Department").Where("is_active = true")
	if filter.DepartmentID > 0 {
		q = q.Where("department_id = ?", filter.DepartmentID)
	}
	if sg := strings.TrimSpace(filter.ServiceGroup); sg != "" {
		q = q.Where("service_group = ?", sg)
	}
	if kw := strings.TrimSpace(filter.Search); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name ILIKE ? OR url ILIKE ? OR service_group ILIKE ?", like, like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, err
	}

	var plats []models.Repository
	if err := q.Order("service_group ASC, name ASC, id ASC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&plats).Error; err != nil {
		return nil, err
	}

	platIDs := make([]uint64, 0, len(plats))
	for _, p := range plats {
		platIDs = append(platIDs, uint64(p.ID))
	}
	importedByPlatID := make(map[uint64]uint64)
	if len(platIDs) > 0 {
		var existing []models.GitRepo
		if err := db.Select("id, platform_repo_id").Where("platform_repo_id IN ?", platIDs).Find(&existing).Error; err == nil {
			for _, g := range existing {
				if g.PlatformRepoID != nil {
					importedByPlatID[*g.PlatformRepoID] = g.ID
				}
			}
		}
	}

	items := make([]PlatformRepoPreviewDTO, 0, len(plats))
	for _, p := range plats {
		effectiveURL := strings.TrimSpace(p.URL)
		if effectiveURL == "" {
			effectiveURL = strings.TrimSpace(p.HTTPURL)
		}
		subSlug := NormalizeSlug(p.ServiceGroup, nil, "sub:"+p.ServiceGroup, true)
		repoSlug := NormalizeSlug(p.Name, nil, fmt.Sprintf("plat:%d", p.ID), false)

		dto := PlatformRepoPreviewDTO{
			PlatformRepoID: uint64(p.ID),
			Name:           p.Name,
			URL:            p.URL,
			HTTPURL:        p.HTTPURL,
			Branch:         gitsync.ResolveBranchDefault(p.Branch),
			DepartmentID:   p.DepartmentID,
			DepartmentName: p.Department.Name,
			ServiceGroup:   p.ServiceGroup,
			SubsystemSlug:  subSlug,
			OwnerID:        p.OwnerID,
			PGDirs:         []string{},
		}
		if gid, ok := importedByPlatID[uint64(p.ID)]; ok {
			dto.AlreadyImported = true
			gidCopy := gid
			dto.GitRepoID = &gidCopy
		}

		if filter.ProbeProtos && effectiveURL != "" {
			pgDirs, wlCnt, outCnt, resolvedBr, pErr := ProbeRepoPGStructure(ctx, codesDir, effectiveURL, p.Branch)
			if pErr != nil {
				dto.ProbeError = pErr.Error()
			} else {
				dto.LocalCachePresent = true
				dto.ResolvedBranch = resolvedBr
				dto.PGDirs = pgDirs
				dto.WhitelistProtoCount = wlCnt
				dto.OutsideWhitelistCount = outCnt
				dto.CandidateBundles = buildCandidateBundles(repoSlug, pgDirs)
			}
		}
		if len(dto.CandidateBundles) == 0 {
			dto.CandidateBundles = buildCandidateBundles(repoSlug, []string{"pg"})
		}
		items = append(items, dto)
	}

	return &PlatformDiscoverResult{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func buildCandidateBundles(repoSlug string, pgDirs []string) []CandidateBundlePreview {
	out := make([]CandidateBundlePreview, 0, len(pgDirs))
	for _, dir := range pgDirs {
		cleanDir := strings.Trim(dir, "/")
		if cleanDir == "" {
			cleanDir = "pg"
		}
		dirTag := NormalizeSlug(strings.ReplaceAll(cleanDir, "/", "-"), nil, cleanDir, false)
		bName := dirTag
		if len(pgDirs) == 1 && cleanDir == "pg" {
			bName = "default"
		}
		targetSchema := repoSlug + "-" + dirTag
		if len(pgDirs) == 1 && cleanDir == "pg" {
			targetSchema = repoSlug
		}
		out = append(out, CandidateBundlePreview{
			BundleName:       bName,
			TargetSchemaName: targetSchema,
			Category:         models.ProtoCategoryBusiness,
			SubdirPath:       cleanDir,
		})
	}
	return out
}

// PlatformBundleImportSpec 定义导入平台仓时的单个 Bundle 配置（支持前端合并或拆分）。
type PlatformBundleImportSpec struct {
	BundleName       string   `json:"bundle_name"`
	TargetSchemaName string   `json:"target_schema_name"`
	Category         string   `json:"category"`
	SubdirPath       string   `json:"subdir_path"`
	IncludeDirs      []string `json:"include_dirs,omitempty"`
}

// PlatformRepoImportItem 定义批量导入单个平台代码仓的请求项。
type PlatformRepoImportItem struct {
	PlatformRepoID uint64                     `json:"platform_repo_id"`
	SyncStrategy   string                     `json:"sync_strategy,omitempty"`
	CollectGlobs   []string                   `json:"collect_globs,omitempty"`
	ExcludeGlobs   []string                   `json:"exclude_globs,omitempty"`
	Bundles        []PlatformBundleImportSpec `json:"bundles,omitempty"`
}

// BatchImportPlatformRepos 批量导入平台代码仓，默认按 pg 目录建 Bundle 且强制 auto_activate=false。
func BatchImportPlatformRepos(
	ctx context.Context,
	db *gorm.DB,
	codesDir string,
	operatorID uint,
	items []PlatformRepoImportItem,
) ([]models.GitRepoRedactedDTO, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if len(items) == 0 {
		return nil, errors.New("导入列表 items 不能为空")
	}
	if strings.TrimSpace(codesDir) == "" {
		codesDir = config.AppConfig.Storage.GetCodesDir()
	}

	var created []models.GitRepoRedactedDTO
	txErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range items {
			if item.PlatformRepoID == 0 {
				return errors.New("platform_repo_id 必须大于 0")
			}
			var plat models.Repository
			if err := tx.First(&plat, item.PlatformRepoID).Error; err != nil {
				return fmt.Errorf("未找到平台代码仓 #%d: %w", item.PlatformRepoID, err)
			}
			var existCnt int64
			if err := tx.Model(&models.GitRepo{}).Where("platform_repo_id = ?", item.PlatformRepoID).Count(&existCnt).Error; err != nil {
				return err
			}
			if existCnt > 0 {
				return fmt.Errorf("平台代码仓 [%s] (#%d) 已纳管", plat.Name, plat.ID)
			}

			effectiveURL := strings.TrimSpace(plat.URL)
			if effectiveURL == "" {
				effectiveURL = strings.TrimSpace(plat.HTTPURL)
			}
			subSlug := NormalizeSlug(plat.ServiceGroup, nil, "sub:"+plat.ServiceGroup, true)
			repoSlug := NormalizeSlug(plat.Name, nil, fmt.Sprintf("plat:%d", plat.ID), false)

			strategy := strings.ToUpper(strings.TrimSpace(item.SyncStrategy))
			if strategy != models.GitSyncStrategyLocalCacheOnly {
				strategy = models.GitSyncStrategyFetchAndRead
			}

			var collectJSON, excludeJSON datatypes.JSON
			if len(item.CollectGlobs) > 0 {
				b, _ := json.Marshal(item.CollectGlobs)
				collectJSON = datatypes.JSON(b)
			}
			if len(item.ExcludeGlobs) > 0 {
				b, _ := json.Marshal(item.ExcludeGlobs)
				excludeJSON = datatypes.JSON(b)
			}

			platID := uint64(plat.ID)
			repoName := plat.Name
			var nameDup int64
			if err := tx.Model(&models.GitRepo{}).Where("name = ?", repoName).Count(&nameDup).Error; err == nil && nameDup > 0 {
				repoName = fmt.Sprintf("%s-%d", plat.Name, plat.ID)
			}

			repo := models.GitRepo{
				PlatformRepoID:   &platID,
				SourceMode:       models.GitSourceModePlatformShared,
				SyncStrategy:     strategy,
				SubsystemSlug:    subSlug,
				CollectGlobsJSON: collectJSON,
				ExcludeGlobsJSON: excludeJSON,
				Name:             repoName,
				Provider:         models.GitProviderGitLab,
				RepoURL:          effectiveURL,
				DefaultBranch:    gitsync.ResolveBranchDefault(plat.Branch),
				AuthType:         models.GitAuthNone,
				SyncMode:         models.GitSyncPoll,
				PollIntervalSec:  1800,
				AutoCompile:      true,
				AutoActivate:     false, // ADR-26 Phase 2 强制默认仅试编译、不自动激活
				LintOnSync:       true,
				LastSyncStatus:   models.GitSyncStatusIdle,
				CreatedBy:        operatorID,
			}
			if err := tx.Create(&repo).Error; err != nil {
				return err
			}

			bundleSpecs := item.Bundles
			if len(bundleSpecs) == 0 {
				pgDirs, _, _, _, pErr := ProbeRepoPGStructure(ctx, codesDir, effectiveURL, plat.Branch)
				if pErr != nil || len(pgDirs) == 0 {
					pgDirs = []string{"pg"}
				}
				for _, cb := range buildCandidateBundles(repoSlug, pgDirs) {
					bundleSpecs = append(bundleSpecs, PlatformBundleImportSpec{
						BundleName:       cb.BundleName,
						TargetSchemaName: cb.TargetSchemaName,
						Category:         cb.Category,
						SubdirPath:       cb.SubdirPath,
					})
				}
			}

			var savedBundles []models.GitProtoBundle
			for _, bs := range bundleSpecs {
				subdir, sErr := SanitizeSubdirPath(bs.SubdirPath)
				if sErr != nil {
					return sErr
				}
				cat := strings.ToUpper(strings.TrimSpace(bs.Category))
				if cat != models.ProtoCategoryBaseLib && cat != models.ProtoCategoryGateway {
					cat = models.ProtoCategoryBusiness
				}
				incDirs := bs.IncludeDirs
				if len(incDirs) == 0 {
					incDirs = []string{"."}
				}
				incJSON, _ := json.Marshal(incDirs)
				bName := strings.TrimSpace(bs.BundleName)
				if bName == "" {
					bName = NormalizeSlug(subdir, nil, subdir, false)
				}
				targetSchema := strings.TrimSpace(bs.TargetSchemaName)
				if targetSchema == "" {
					targetSchema = repoSlug + "-" + bName
				}
				bModel := models.GitProtoBundle{
					GitRepoID:        repo.ID,
					BundleName:       bName,
					TargetSchemaName: targetSchema,
					Category:         cat,
					SubdirPath:       subdir,
					IncludeDirsJSON:  datatypes.JSON(incJSON),
					EntryFilesJSON:   datatypes.JSON([]byte(`[]`)),
					FileGlobsJSON:    datatypes.JSON([]byte(`["**/*.proto"]`)),
					ExcludeGlobsJSON: datatypes.JSON([]byte(`[]`)),
					AutoCompile:      true,
					AutoActivate:     false,
					MRAutoPublish:    models.MRAutoPublishDisabled,
					LintOnSync:       true,
				}
				if err := tx.Create(&bModel).Error; err != nil {
					return err
				}
				savedBundles = append(savedBundles, bModel)
			}
			created = append(created, repo.ToRedactedDTO(savedBundles...))
		}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	return created, nil
}

// LinkLegacyRepoToPlatform 将既有 EXTERNAL_STANDALONE 仓库关联到平台 repositories 并清除冗余凭据（ADR-26 §4.6）。
func LinkLegacyRepoToPlatform(
	ctx context.Context,
	db *gorm.DB,
	repoID uint64,
	platformRepoID uint64,
	syncStrategy string,
) (*models.GitRepo, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	var repo models.GitRepo
	if err := db.WithContext(ctx).First(&repo, repoID).Error; err != nil {
		return nil, err
	}

	var matchedPlat *models.Repository
	if platformRepoID > 0 {
		var p models.Repository
		if err := db.WithContext(ctx).First(&p, platformRepoID).Error; err != nil {
			return nil, fmt.Errorf("未找到平台代码仓 #%d: %w", platformRepoID, err)
		}
		matchedPlat = &p
	} else {
		normTarget := gitsync.NormalizeGitURL(repo.RepoURL)
		var plats []models.Repository
		if err := db.WithContext(ctx).Find(&plats).Error; err != nil {
			return nil, err
		}
		for i := range plats {
			if gitsync.NormalizeGitURL(plats[i].URL) == normTarget ||
				(plats[i].HTTPURL != "" && gitsync.NormalizeGitURL(plats[i].HTTPURL) == normTarget) {
				matchedPlat = &plats[i]
				break
			}
		}
		if matchedPlat == nil {
			return nil, fmt.Errorf("未在平台 repositories 中找到与 %q 匹配的仓库", repo.RepoURL)
		}
	}

	var existingOwnerCnt int64
	if err := db.WithContext(ctx).Model(&models.GitRepo{}).
		Where("platform_repo_id = ? AND id <> ?", matchedPlat.ID, repo.ID).
		Count(&existingOwnerCnt).Error; err != nil {
		return nil, err
	}
	if existingOwnerCnt > 0 {
		return nil, fmt.Errorf("平台代码仓 #%d 已被其他 GitRepo 关联", matchedPlat.ID)
	}

	strategy := strings.ToUpper(strings.TrimSpace(syncStrategy))
	if strategy != models.GitSyncStrategyLocalCacheOnly {
		strategy = models.GitSyncStrategyFetchAndRead
	}
	platID := uint64(matchedPlat.ID)
	subSlug := NormalizeSlug(matchedPlat.ServiceGroup, nil, "sub:"+matchedPlat.ServiceGroup, true)
	effectiveURL := strings.TrimSpace(matchedPlat.URL)
	if effectiveURL == "" {
		effectiveURL = strings.TrimSpace(matchedPlat.HTTPURL)
	}

	updates := map[string]any{
		"platform_repo_id":   &platID,
		"source_mode":        models.GitSourceModePlatformShared,
		"sync_strategy":      strategy,
		"subsystem_slug":     subSlug,
		"repo_url":           effectiveURL,
		"default_branch":     gitsync.ResolveBranchDefault(matchedPlat.Branch),
		"auth_type":          models.GitAuthNone,
		"credentials_enc":    "",
		"credential_hint":    "",
		"webhook_secret_enc": "",
	}
	if err := db.WithContext(ctx).Model(&repo).Updates(updates).Error; err != nil {
		return nil, err
	}
	if err := db.WithContext(ctx).First(&repo, repo.ID).Error; err != nil {
		return nil, err
	}
	return &repo, nil
}

// BaselineReportRepoItem 单仓现网基线统计。
type BaselineReportRepoItem struct {
	GitRepoID        uint64 `json:"git_repo_id"`
	RepoName         string `json:"repo_name"`
	SubsystemSlug    string `json:"subsystem_slug"`
	SourceMode       string `json:"source_mode"`
	WhitelistFiles   int    `json:"whitelist_files"`
	NormalizedFiles  int    `json:"normalized_files"`
	QuarantinedFiles int    `json:"quarantined_files"`
	ExternalCopies   int    `json:"external_copies"`
	BundleCount      int    `json:"bundle_count"`
	CompiledBundles  int    `json:"compiled_bundles"`
	LastSyncStatus   string `json:"last_sync_status"`
}

// BaselineReportDTO ADR-26 Phase 2 现网基线报告。
type BaselineReportDTO struct {
	TotalRepos        int                      `json:"total_repos"`
	WhitelistFiles    int                      `json:"whitelist_files"`
	CollectedFiles    int                      `json:"collected_files"`
	NormalizedFiles   int                      `json:"normalized_files"`
	QuarantinedFiles  int                      `json:"quarantined_files"`
	ExternalCopyFiles int                      `json:"external_copy_files"`
	SyntaxPassRate    float64                  `json:"syntax_pass_rate"`
	TotalBundles      int                      `json:"total_bundles"`
	CompiledBundles   int                      `json:"compiled_bundles"`
	CompilePassRate   float64                  `json:"compile_pass_rate"`
	ErrorDistribution map[string]int           `json:"error_distribution"`
	Repos             []BaselineReportRepoItem `json:"repos"`
}

// GenerateBaselineReport 生成现网白名单文件数、语法通过率、试编译通过率与错误类型分布报告。
func GenerateBaselineReport(ctx context.Context, db *gorm.DB) (*BaselineReportDTO, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	var repos []models.GitRepo
	if err := db.WithContext(ctx).Order("id ASC").Find(&repos).Error; err != nil {
		return nil, err
	}
	var bundles []models.GitProtoBundle
	if err := db.WithContext(ctx).Find(&bundles).Error; err != nil {
		return nil, err
	}
	var files []models.ProtoFile
	if err := db.WithContext(ctx).Where("is_deleted = false").Find(&files).Error; err != nil {
		return nil, err
	}

	report := &BaselineReportDTO{
		TotalRepos:        len(repos),
		TotalBundles:      len(bundles),
		ErrorDistribution: make(map[string]int),
		Repos:             make([]BaselineReportRepoItem, 0, len(repos)),
	}

	repoItems := make(map[uint64]*BaselineReportRepoItem, len(repos))
	for _, r := range repos {
		item := &BaselineReportRepoItem{
			GitRepoID:      r.ID,
			RepoName:       r.Name,
			SubsystemSlug:  r.SubsystemSlug,
			SourceMode:     r.SourceMode,
			LastSyncStatus: r.LastSyncStatus,
		}
		repoItems[r.ID] = item
	}
	for _, b := range bundles {
		if it := repoItems[b.GitRepoID]; it != nil {
			it.BundleCount++
			if b.LastSchemaID != nil && *b.LastSchemaID > 0 {
				it.CompiledBundles++
				report.CompiledBundles++
			}
		}
	}

	for _, f := range files {
		report.WhitelistFiles++
		if f.IsExternalCopy {
			report.ExternalCopyFiles++
		}
		it := repoItems[f.GitRepoID]
		if it != nil {
			it.WhitelistFiles++
			if f.IsExternalCopy {
				it.ExternalCopies++
			}
		}
		switch f.PipelineStatus {
		case models.ProtoPipelineCollected:
			report.CollectedFiles++
		case models.ProtoPipelineQuarantined:
			report.QuarantinedFiles++
			if it != nil {
				it.QuarantinedFiles++
			}
			errKey := classifyPipelineError(f.StatusReason)
			report.ErrorDistribution[errKey]++
		default:
			report.NormalizedFiles++
			if it != nil {
				it.NormalizedFiles++
			}
		}
	}

	if report.WhitelistFiles > 0 {
		report.SyntaxPassRate = float64(report.NormalizedFiles) * 100.0 / float64(report.WhitelistFiles)
	}
	if report.TotalBundles > 0 {
		report.CompilePassRate = float64(report.CompiledBundles) * 100.0 / float64(report.TotalBundles)
	}
	for _, r := range repos {
		if it := repoItems[r.ID]; it != nil {
			report.Repos = append(report.Repos, *it)
		}
	}
	return report, nil
}

func classifyPipelineError(reason string) string {
	upper := strings.ToUpper(strings.TrimSpace(reason))
	switch {
	case strings.Contains(upper, "LFS_POINTER"):
		return "LFS_POINTER"
	case strings.Contains(upper, "PARSE_FAILED") || strings.Contains(upper, "SYNTAX") || strings.Contains(upper, "L"):
		return "PARSE_FAILED"
	case strings.Contains(upper, "SIZE") || strings.Contains(upper, "上限"):
		return "FILE_TOO_LARGE"
	case upper == "":
		return "UNKNOWN"
	default:
		return "OTHER"
	}
}

// ArchAssignmentSuggestionDTO 结合平台 service_group / owner 推荐架构 Subsystem / Module 归属。
type ArchAssignmentSuggestionDTO struct {
	GitRepoID              uint64  `json:"git_repo_id"`
	RepoName               string  `json:"repo_name"`
	ServiceGroup           string  `json:"service_group"`
	PlatformOwnerID        uint    `json:"platform_owner_id"`
	RecommendedSubsystemID *uint64 `json:"recommended_subsystem_id,omitempty"`
	RecommendedSubsystem   string  `json:"recommended_subsystem,omitempty"`
	RecommendedModuleID    *uint64 `json:"recommended_module_id,omitempty"`
	RecommendedModule      string  `json:"recommended_module,omitempty"`
	Confidence             string  `json:"confidence"` // HIGH | MEDIUM | LOW
	Reason                 string  `json:"reason"`
}

// SuggestArchAssignments 结合 platform repositories 的 service_group 与 owner 推荐架构 Subsystem / Module 归属（ADR-26 Phase 4）。
func SuggestArchAssignments(ctx context.Context, db *gorm.DB) ([]ArchAssignmentSuggestionDTO, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	var repos []models.GitRepo
	if err := db.WithContext(ctx).Where("platform_repo_id IS NOT NULL").Order("id ASC").Find(&repos).Error; err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return []ArchAssignmentSuggestionDTO{}, nil
	}
	platMap := loadPlatformRepoMetaMap(db, repos)

	var subsystems []models.Subsystem
	_ = db.WithContext(ctx).Find(&subsystems).Error
	var modules []models.Module
	_ = db.WithContext(ctx).Find(&modules).Error

	out := make([]ArchAssignmentSuggestionDTO, 0, len(repos))
	for _, r := range repos {
		if r.PlatformRepoID == nil {
			continue
		}
		plat, ok := platMap[*r.PlatformRepoID]
		if !ok {
			continue
		}
		item := ArchAssignmentSuggestionDTO{
			GitRepoID:       r.ID,
			RepoName:        r.Name,
			ServiceGroup:    plat.ServiceGroup,
			PlatformOwnerID: plat.OwnerID,
			Confidence:      "LOW",
			Reason:          "暂无同名或同负责人架构匹配，建议新建或人工指定子系统/模块",
		}

		sgLower := strings.ToLower(strings.TrimSpace(plat.ServiceGroup))
		repoLower := strings.ToLower(strings.TrimSpace(plat.Name))
		for _, sub := range subsystems {
			if sgLower != "" && (strings.ToLower(sub.Name) == sgLower || strings.ToLower(sub.Code) == sgLower) {
				sid := sub.ID
				item.RecommendedSubsystemID = &sid
				item.RecommendedSubsystem = sub.Name
				item.Confidence = "HIGH"
				item.Reason = fmt.Sprintf("子系统名/编码匹配 service_group=%q", plat.ServiceGroup)
				break
			}
		}
		for _, mod := range modules {
			if strings.ToLower(mod.Code) == repoLower || strings.ToLower(mod.Name) == repoLower {
				mid := mod.ID
				item.RecommendedModuleID = &mid
				item.RecommendedModule = mod.Name
				item.Confidence = "HIGH"
				item.Reason = fmt.Sprintf("模块编码/名称精确匹配仓库 %q", plat.Name)
				break
			}
			if item.RecommendedModuleID == nil && plat.OwnerID > 0 && mod.OwnerID != nil && *mod.OwnerID == plat.OwnerID {
				mid := mod.ID
				item.RecommendedModuleID = &mid
				item.RecommendedModule = mod.Name
				if item.Confidence != "HIGH" {
					item.Confidence = "MEDIUM"
					item.Reason = fmt.Sprintf("模块负责人匹配仓库 owner_id=%d", plat.OwnerID)
				}
			}
		}
		out = append(out, item)
	}
	return out, nil
}
