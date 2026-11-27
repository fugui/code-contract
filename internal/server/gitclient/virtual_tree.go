package gitclient

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"code-contract/internal/server/decoder"
	"code-contract/internal/server/protodoc"
	"code-contract/models"

	"gorm.io/gorm"
)

// ErrMRNormalizeFailed 表示 MR 虚拟树在 Normalizer 清洗或语法隔离门禁阶段失败（绝不允许 Override）。
var ErrMRNormalizeFailed = errors.New("MR 虚拟树 Normalizer 清洗或 Protobuf 语法门禁失败")

// MRNormalizeReport 描述 MR 虚拟树在 Normalizer 阶段的清洗结果与失败诊断。
type MRNormalizeReport struct {
	NormalizedTree map[string]string
	FilteredFiles  []string
	Diagnostics    []NormalizeDiagnostic
	Version        string
}

// NormalizeAndValidateMRTree 对 MR 虚拟树应用与全量入库同源的 pg 白名单过滤、Stage B Normalizer 与语法门禁校验。
// - 当 repo.SourceMode == PLATFORM_SHARED（或 changedFiles 落在非白名单路径）时，剔除不符合严格小写 pg/ 目录的文件；
// - 对所有受变更且位于白名单内的文件（以及基线变更集合）执行 Normalizer + ParseProtoSyntaxOnly 语法校验；
// - 任意语法解析失败或 Normalizer 致命诊断均返回 ErrMRNormalizeFailed（上层标记 normalize_failed=true，禁止 Override）。
func NormalizeAndValidateMRTree(
	ctx context.Context,
	repo *models.GitRepo,
	bundle *models.GitProtoBundle,
	virtualTree map[string]string,
	changedFiles []string,
	norm Normalizer,
	auxConfig []byte,
) (*MRNormalizeReport, error) {
	if norm == nil {
		norm = NewPassthroughNormalizer()
	}
	report := &MRNormalizeReport{
		NormalizedTree: make(map[string]string, len(virtualTree)),
		Version:        norm.Version(),
	}

	subdir := "."
	if bundle != nil {
		if cleanSub, err := SanitizeSubdirPath(bundle.SubdirPath); err == nil && cleanSub != "" {
			subdir = cleanSub
		}
	}
	isPGSubdir := subdir == "pg" || strings.HasPrefix(subdir, "pg/") || strings.HasSuffix(subdir, "/pg") || strings.Contains(subdir, "/pg/")
	applyPgWhitelist := false
	if repo != nil && repo.SourceMode == models.GitSourceModePlatformShared {
		if len(repo.CollectGlobs()) > 0 || isPGSubdir {
			applyPgWhitelist = true
		} else if subdir == "." && (repo.ProtoStatus == models.GitRepoProtoStatusHasProto || repo.LastCollectedCommit != "") {
			applyPgWhitelist = true
		}
	}
	collectGlobs := []string(nil)
	excludeGlobs := []string(nil)
	if repo != nil {
		collectGlobs = repo.CollectGlobs()
		excludeGlobs = repo.ExcludeGlobs()
	}
	if len(collectGlobs) == 0 {
		collectGlobs = []string{"pg/**/*.proto", "**/pg/**/*.proto"}
	}

	rawBytes := make(map[string][]byte, len(virtualTree))
	keyOrder := make([]string, 0, len(virtualTree))
	for p := range virtualTree {
		keyOrder = append(keyOrder, p)
	}
	sort.Strings(keyOrder)

	for _, relPath := range keyOrder {
		content := virtualTree[relPath]
		repoRelPath := relPath
		if subdir != "." {
			repoRelPath = strings.TrimPrefix(subdir+"/"+strings.TrimPrefix(relPath, "/"), "./")
		}
		if applyPgWhitelist {
			inPg := (matchCollectGlobsStrict(collectGlobs, repoRelPath) || matchCollectGlobsStrict(collectGlobs, relPath)) &&
				!matchAnyDoublestar(excludeGlobs, repoRelPath)
			if !inPg {
				report.FilteredFiles = append(report.FilteredFiles, relPath)
				continue
			}
		}
		rawBytes[relPath] = []byte(content)
	}

	if len(rawBytes) == 0 {
		return report, nil
	}

	var repoVal models.GitRepo
	if repo != nil {
		repoVal = *repo
	}
	normOut, err := norm.Normalize(ctx, NormalizeInput{
		Repo:          repoVal,
		Files:         rawBytes,
		AuxConfigJSON: auxConfig,
	})
	if err != nil {
		return report, fmt.Errorf("%w: %v", ErrMRNormalizeFailed, err)
	}
	if strings.TrimSpace(normOut.NormalizerVersion) != "" {
		report.Version = normOut.NormalizerVersion
	}

	var diagErrors []string
	changedSet := make(map[string]struct{}, len(changedFiles))
	for _, f := range changedFiles {
		changedSet[f] = struct{}{}
	}

	// 将标准化结果回填至 NormalizedTree，并对变更文件强制执行语法隔离门禁
	for _, relPath := range keyOrder {
		if _, filtered := rawBytes[relPath]; !filtered {
			continue
		}
		cleanedBytes := rawBytes[relPath]
		if nf, ok := normOut.Files[relPath]; ok {
			if len(nf.Diagnostics) > 0 {
				report.Diagnostics = append(report.Diagnostics, nf.Diagnostics...)
				for _, d := range nf.Diagnostics {
					if strings.EqualFold(d.Severity, "ERROR") || d.RuleID == "PARSE_SYNTAX_ERROR" {
						diagErrors = append(diagErrors, fmt.Sprintf("%s [%s]: %s", d.RepoFilePath, d.RuleID, d.Message))
					}
				}
			}
			if len(nf.CleanContent) > 0 {
				cleanedBytes = nf.CleanContent
			}
		}
		// 检查受变更文件与新增/修改文件的语法门禁
		if _, isChanged := changedSet[relPath]; isChanged || len(changedSet) == 0 {
			gateRes := ParseProtoSyntaxOnly(relPath, cleanedBytes)
			if !gateRes.Passed {
				report.Diagnostics = append(report.Diagnostics, gateRes.Diagnostics...)
				errMsg := gateRes.ErrorSummary
				if strings.TrimSpace(errMsg) == "" {
					errMsg = "Proto 语法门禁解析失败"
				}
				diagErrors = append(diagErrors, fmt.Sprintf("%s: %s", relPath, errMsg))
			}
		}
		report.NormalizedTree[relPath] = string(cleanedBytes)
	}

	if len(diagErrors) > 0 {
		sort.Strings(diagErrors)
		return report, fmt.Errorf("%w: %s", ErrMRNormalizeFailed, strings.Join(diagErrors, "; "))
	}
	return report, nil
}

const (
	MRChangeAdded    = "ADDED"
	MRChangeModified = "MODIFIED"
	MRChangeRenamed  = "RENAMED"
	MRChangeDeleted  = "DELETED"
)

// MRPatchFile MR/PR 单文件变更补丁项
type MRPatchFile struct {
	ChangeType string `json:"change_type"` // ADDED | MODIFIED | RENAMED | DELETED
	Path       string `json:"path,omitempty"`
	OldPath    string `json:"old_path,omitempty"`
	NewPath    string `json:"new_path,omitempty"`
	Content    string `json:"content,omitempty"`
}

func (p MRPatchFile) effectiveNewPath() string {
	if strings.TrimSpace(p.NewPath) != "" {
		return strings.TrimSpace(p.NewPath)
	}
	return strings.TrimSpace(p.Path)
}

func (p MRPatchFile) effectiveOldPath() string {
	if strings.TrimSpace(p.OldPath) != "" {
		return strings.TrimSpace(p.OldPath)
	}
	return strings.TrimSpace(p.Path)
}

// MatchBundleForRepoPath 判断仓库级路径是否归属于指定 Monorepo Bundle，并返回剥离 SubdirPath 后的相对路径
func MatchBundleForRepoPath(bundle *models.GitProtoBundle, rawRepoPath string) (string, bool, error) {
	if bundle == nil {
		return "", false, errors.New("GitProtoBundle 不能为空")
	}
	trimmed := strings.TrimSpace(rawRepoPath)
	if trimmed == "" || !strings.HasSuffix(trimmed, ".proto") {
		return "", false, nil
	}
	repoRel, err := decoder.SanitizeProtoRelPath(trimmed, true)
	if err != nil {
		return "", false, err
	}
	subdir, err := SanitizeSubdirPath(bundle.SubdirPath)
	if err != nil {
		return "", false, err
	}

	relPath := repoRel
	if subdir != "." {
		prefix := subdir + "/"
		if !strings.HasPrefix(repoRel, prefix) {
			return "", false, nil
		}
		relPath = strings.TrimPrefix(repoRel, prefix)
	}
	cleanRel, err := decoder.SanitizeProtoRelPath(relPath, true)
	if err != nil {
		return "", false, err
	}

	includeGlobs := bundle.FileGlobs()
	if len(includeGlobs) > 0 && !matchAnyProtoGlob(includeGlobs, cleanRel) {
		return "", false, nil
	}
	excludeGlobs := bundle.ExcludeGlobs()
	if len(excludeGlobs) > 0 && matchAnyProtoGlob(excludeGlobs, cleanRel) {
		return "", false, nil
	}
	return cleanRel, true, nil
}

// FilterAffectedBundles 筛选 MR 变更涉及的 Monorepo Bundles（按 BASE_LIB -> BUSINESS -> GATEWAY 排序）
func FilterAffectedBundles(bundles []models.GitProtoBundle, patchFiles []MRPatchFile) ([]models.GitProtoBundle, error) {
	if len(bundles) == 0 {
		return nil, nil
	}
	// 先校验所有补丁中的 .proto 路径不含路径穿越
	for _, pf := range patchFiles {
		for _, candidate := range []string{pf.Path, pf.OldPath, pf.NewPath} {
			c := strings.TrimSpace(candidate)
			if c == "" || !strings.HasSuffix(c, ".proto") {
				continue
			}
			if _, err := decoder.SanitizeProtoRelPath(c, true); err != nil {
				return nil, err
			}
		}
	}

	if len(patchFiles) == 0 {
		out := make([]models.GitProtoBundle, len(bundles))
		copy(out, bundles)
		sortBundlesByCategory(out)
		return out, nil
	}

	matched := make([]models.GitProtoBundle, 0, len(bundles))
	for _, b := range bundles {
		bCopy := b
		hit := false
		for _, pf := range patchFiles {
			for _, candidate := range []string{pf.effectiveOldPath(), pf.effectiveNewPath()} {
				if candidate == "" {
					continue
				}
				_, ok, err := MatchBundleForRepoPath(&bCopy, candidate)
				if err != nil {
					return nil, err
				}
				if ok {
					hit = true
					break
				}
			}
			if hit {
				break
			}
		}
		if hit {
			matched = append(matched, bCopy)
		}
	}
	sortBundlesByCategory(matched)
	return matched, nil
}

func sortBundlesByCategory(bundles []models.GitProtoBundle) {
	catOrder := func(cat string) int {
		switch cat {
		case models.ProtoCategoryBaseLib:
			return 0
		case models.ProtoCategoryBusiness:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(bundles, func(i, j int) bool {
		ci, cj := catOrder(bundles[i].Category), catOrder(bundles[j].Category)
		if ci != cj {
			return ci < cj
		}
		return bundles[i].ID < bundles[j].ID
	})
}

// BuildMRVirtualTree 构建「DB 目标分支活跃基线 + MR 变更补丁」的完整内存虚拟树（含 ADDED/MODIFIED/RENAMED/DELETED 语义）
func BuildMRVirtualTree(
	db *gorm.DB,
	bundle *models.GitProtoBundle,
	patchFiles []MRPatchFile,
) (map[string]string, []string, error) {
	if db == nil {
		return nil, nil, errors.New("db 不能为空")
	}
	if bundle == nil {
		return nil, nil, errors.New("bundle 不能为空")
	}

	virtualTree := make(map[string]string)

	// 1. 加载目标分支在 DB 中的活跃基线文件 (is_deleted = false)
	type baselineRow struct {
		FilePath    string
		ContentText string
	}
	var rows []baselineRow
	if err := db.Table("contract_proto_files AS f").
		Select("f.file_path AS file_path, v.content_text AS content_text").
		Joins("JOIN contract_proto_file_versions AS v ON v.id = f.latest_version_id").
		Where("f.bundle_id = ? AND f.is_deleted = false", bundle.ID).
		Order("f.file_path ASC").
		Find(&rows).Error; err != nil {
		return nil, nil, fmt.Errorf("读取 Bundle 基线文件失败: %w", err)
	}
	for _, r := range rows {
		cleaned, err := decoder.SanitizeProtoRelPath(r.FilePath, true)
		if err != nil {
			continue
		}
		virtualTree[cleaned] = r.ContentText
	}

	// 若 contract_proto_files 尚未落库但 Bundle 已关联既有 ProtoSchema（或同名活跃 Schema），从描述符重建基线文件兜底
	if len(virtualTree) == 0 {
		var baseSchema models.ProtoSchema
		var err error
		if bundle.LastSchemaID != nil && *bundle.LastSchemaID > 0 {
			err = db.First(&baseSchema, *bundle.LastSchemaID).Error
		} else if bundle.TargetSchemaName != "" {
			err = db.Where("name = ? AND is_active = true", bundle.TargetSchemaName).First(&baseSchema).Error
		}
		if err == nil && baseSchema.ID > 0 {
			protodoc.EnsureSymbolsForSchema(db, &baseSchema)
			var syms []models.ProtoDocSymbol
			if sErr := db.Where("schema_id = ?", baseSchema.ID).Find(&syms).Error; sErr == nil {
				if reconstructed, rErr := protodoc.ReconstructProtoSources(&baseSchema, syms); rErr == nil {
					for _, rf := range reconstructed {
						if cleaned, cErr := decoder.SanitizeProtoRelPath(rf.FilePath, true); cErr == nil {
							virtualTree[cleaned] = rf.Content
						}
					}
				}
			}
		}
	}

	// 2. 叠加 MR 变更补丁（ADDED / MODIFIED / RENAMED / DELETED）
	changedSet := make(map[string]struct{})
	for _, pf := range patchFiles {
		changeType := strings.ToUpper(strings.TrimSpace(pf.ChangeType))
		if changeType == "" {
			changeType = MRChangeModified
		}

		switch changeType {
		case MRChangeDeleted:
			targetRaw := pf.effectiveOldPath()
			if targetRaw == "" {
				targetRaw = pf.effectiveNewPath()
			}
			relPath, matched, err := MatchBundleForRepoPath(bundle, targetRaw)
			if err != nil {
				return nil, nil, err
			}
			if !matched {
				// 兼容传入已经是 Bundle 相对路径的情况
				if relTry, rErr := decoder.SanitizeProtoRelPath(targetRaw, true); rErr == nil {
					if _, exists := virtualTree[relTry]; exists {
						relPath = relTry
						matched = true
					}
				}
			}
			if matched {
				delete(virtualTree, relPath)
				changedSet[relPath] = struct{}{}
			}

		case MRChangeRenamed:
			oldRaw := pf.effectiveOldPath()
			if oldRaw != "" {
				oldRel, oldMatched, err := MatchBundleForRepoPath(bundle, oldRaw)
				if err != nil {
					return nil, nil, err
				}
				if !oldMatched {
					if relTry, rErr := decoder.SanitizeProtoRelPath(oldRaw, true); rErr == nil {
						if _, exists := virtualTree[relTry]; exists {
							oldRel = relTry
							oldMatched = true
						}
					}
				}
				if oldMatched {
					delete(virtualTree, oldRel)
					changedSet[oldRel] = struct{}{}
				}
			}

			newRaw := pf.effectiveNewPath()
			if newRaw != "" {
				newRel, newMatched, err := MatchBundleForRepoPath(bundle, newRaw)
				if err != nil {
					return nil, nil, err
				}
				if newMatched {
					virtualTree[newRel] = pf.Content
					changedSet[newRel] = struct{}{}
				}
			}

		default: // ADDED | MODIFIED
			newRaw := pf.effectiveNewPath()
			if newRaw == "" {
				continue
			}
			newRel, matched, err := MatchBundleForRepoPath(bundle, newRaw)
			if err != nil {
				return nil, nil, err
			}
			if !matched {
				// 若补丁本身已是相对于 Bundle 根目录的合法路径且 Bundle SubdirPath="."，MatchBundleForRepoPath 已覆盖；
				// 若 SubdirPath != "." 但调用方直接提供了已在 virtualTree 内的相对路径，也允许匹配
				if relTry, rErr := decoder.SanitizeProtoRelPath(newRaw, true); rErr == nil {
					if _, exists := virtualTree[relTry]; exists {
						newRel = relTry
						matched = true
					}
				}
			}
			if matched {
				virtualTree[newRel] = pf.Content
				changedSet[newRel] = struct{}{}
			}
		}
	}

	var totalBytes int64
	for _, content := range virtualTree {
		totalBytes += int64(len(content))
		if totalBytes > int64(decoder.MaxDescriptorFileSize) {
			return nil, nil, fmt.Errorf("MR 虚拟树 .proto 总大小超过 16MB (%d Bytes) 上限", decoder.MaxDescriptorFileSize)
		}
	}

	changedFiles := make([]string, 0, len(changedSet))
	for p := range changedSet {
		changedFiles = append(changedFiles, p)
	}
	sort.Strings(changedFiles)
	return virtualTree, changedFiles, nil
}
