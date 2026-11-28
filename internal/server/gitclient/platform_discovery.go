package gitclient

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"code-contract/models"

	"gorm.io/gorm"
)

// CandidateBundlePreview 描述每个 pg 目录自动生成的候选 Bundle。
type CandidateBundlePreview struct {
	BundleName       string `json:"bundle_name"`
	TargetSchemaName string `json:"target_schema_name"`
	Category         string `json:"category"`
	SubdirPath       string `json:"subdir_path"`
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

// BaselineReportRepoItem 单仓现网基线统计。
type BaselineReportRepoItem struct {
	GitRepoID        uint64 `json:"git_repo_id"`
	RepoName         string `json:"repo_name"`
	SubsystemSlug    string `json:"subsystem_slug"`
	SourceMode       string `json:"source_mode"`
	ProtoStatus      string `json:"proto_status"`
	WhitelistFiles   int    `json:"whitelist_files"`
	NormalizedFiles  int    `json:"normalized_files"`
	QuarantinedFiles int    `json:"quarantined_files"`
	ExternalCopies   int    `json:"external_copies"`
	BundleCount      int    `json:"bundle_count"`
	CompiledBundles  int    `json:"compiled_bundles"`
	LastSyncStatus   string `json:"last_sync_status"`
}

// BaselineReportDTO ADR-26 / ADR-27 现网基线报告。
type BaselineReportDTO struct {
	TotalRepos        int                      `json:"total_repos"`
	HasProtoRepos     int                      `json:"has_proto_repos"`
	NoProtoRepos      int                      `json:"no_proto_repos"`
	NoCacheRepos      int                      `json:"no_cache_repos"`
	ExcludedRepos     int                      `json:"excluded_repos"`
	FailedRepos       int                      `json:"failed_repos"`
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
		switch r.ProtoStatus {
		case models.GitRepoProtoStatusHasProto:
			report.HasProtoRepos++
		case models.GitRepoProtoStatusNoProto:
			report.NoProtoRepos++
		case models.GitRepoProtoStatusNoLocalCache:
			report.NoCacheRepos++
		case models.GitRepoProtoStatusExcluded:
			report.ExcludedRepos++
		case models.GitRepoProtoStatusSyncFailed:
			report.FailedRepos++
		}
		item := &BaselineReportRepoItem{
			GitRepoID:      r.ID,
			RepoName:       r.Name,
			SubsystemSlug:  r.SubsystemSlug,
			SourceMode:     r.SourceMode,
			ProtoStatus:    r.ProtoStatus,
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
	case upper == "":
		return "UNKNOWN"
	case strings.Contains(upper, "LFS_POINTER"):
		return "LFS_POINTER"
	case strings.Contains(upper, "FILE_QUARANTINED"):
		return "FILE_QUARANTINED"
	case strings.Contains(upper, "INTRA_REPO_PATH_CONFLICT"):
		return "INTRA_REPO_PATH_CONFLICT"
	case strings.Contains(upper, "SIZE") || strings.Contains(upper, "上限"):
		return "FILE_TOO_LARGE"
	case strings.Contains(upper, "FILE DOES NOT EXIST") || strings.Contains(upper, "NOT FOUND") || strings.Contains(upper, "COULD NOT RESOLVE"):
		return "MISSING_IMPORT"
	case strings.Contains(upper, "PARSE_FAILED") || strings.Contains(upper, "SYNTAX"):
		return "PARSE_FAILED"
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
	if err := db.WithContext(ctx).Where("platform_repo_id > 0 AND sync_enabled = true").Order("id ASC").Find(&repos).Error; err != nil {
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
		if r.PlatformRepoID == 0 {
			continue
		}
		plat, ok := platMap[r.PlatformRepoID]
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
