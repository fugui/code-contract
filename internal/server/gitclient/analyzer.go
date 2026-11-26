package gitclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"code-contract/internal/server/decoder"
	"code-contract/models"

	"gorm.io/gorm"
)

// AnalyzedInputFile 是 Stage C 输入的单个 NORMALIZED 文件信息。
type AnalyzedInputFile struct {
	RepoFilePath  string
	PGDir         string
	PackageName   string
	Imports       []string
	Symbols       []ParsedProtoSymbol
	ContentSHA256 string
}

// AnalyzedFileResult 是 Stage C 推导后的逻辑路径与归一化结果。
type AnalyzedFileResult struct {
	RepoFilePath string
	LogicalPath  string
	BundleID     uint64
}

// InferLogicalPathsAndIncludeDirs 按 ADR-26 §2.4.3 推断每份 NORMALIZED 文件的逻辑导入路径与 Bundle include_dirs：
// 1. Bundle 显式非 "." 的 include_dirs 优先；
// 2. 其次扫描仓内 import "..." 与 RepoFilePath 做后缀匹配反推 proto_root；
// 3. 缺省以所属全小写 pg 目录（或 Bundle SubdirPath）为根。
func InferLogicalPathsAndIncludeDirs(
	bundle models.GitProtoBundle,
	files []AnalyzedInputFile,
	allRepoImports []string,
) (map[string]string, []string) {
	explicitIncludes := make([]string, 0)
	for _, d := range bundle.IncludeDirs() {
		cleaned := strings.Trim(strings.TrimSpace(d), "/")
		if cleaned != "" && cleaned != "." {
			explicitIncludes = append(explicitIncludes, cleaned)
		}
	}
	sort.Slice(explicitIncludes, func(i, j int) bool {
		if len(explicitIncludes[i]) != len(explicitIncludes[j]) {
			return len(explicitIncludes[i]) > len(explicitIncludes[j])
		}
		return explicitIncludes[i] < explicitIncludes[j]
	})

	subdir, err := SanitizeSubdirPath(bundle.SubdirPath)
	if err != nil {
		subdir = "."
	}

	// 若未显式配置 include_dirs，结合仓内 import "..." 后缀反推候选 proto_root
	inferredRoots := make(map[string]int)
	for _, f := range files {
		for _, imp := range f.Imports {
			impClean := strings.Trim(strings.TrimSpace(imp), "/")
			if impClean == "" || strings.HasPrefix(impClean, "google/protobuf/") {
				continue
			}
			for _, cand := range files {
				suffix := "/" + impClean
				if strings.HasSuffix(cand.RepoFilePath, suffix) {
					root := strings.TrimSuffix(cand.RepoFilePath, suffix)
					if root != "" {
						inferredRoots[root]++
					}
				}
			}
		}
		for _, imp := range allRepoImports {
			impClean := strings.Trim(strings.TrimSpace(imp), "/")
			if impClean == "" || strings.HasPrefix(impClean, "google/protobuf/") {
				continue
			}
			suffix := "/" + impClean
			if strings.HasSuffix(f.RepoFilePath, suffix) {
				root := strings.TrimSuffix(f.RepoFilePath, suffix)
				if root != "" {
					inferredRoots[root]++
				}
			}
		}
	}

	bestRoot := ""
	bestScore := 0
	for root, score := range inferredRoots {
		if score > bestScore || (score == bestScore && len(root) > len(bestRoot)) {
			bestRoot = root
			bestScore = score
		}
	}

	logicalByRepoPath := make(map[string]string, len(files))
	includeSet := make(map[string]struct{})
	for _, f := range files {
		rp := f.RepoFilePath
		logical := rp

		matchedExplicit := false
		for _, inc := range explicitIncludes {
			// 兼容 inc 既可能是仓内全路径也可能是相对 Bundle SubdirPath 的路径
			candidates := []string{inc}
			if subdir != "." {
				candidates = append([]string{subdir + "/" + inc}, candidates...)
			}
			for _, fullInc := range candidates {
				prefix := strings.Trim(fullInc, "/") + "/"
				if strings.HasPrefix(rp, prefix) {
					logical = strings.TrimPrefix(rp, prefix)
					includeSet[strings.Trim(fullInc, "/")] = struct{}{}
					matchedExplicit = true
					break
				}
			}
			if matchedExplicit {
				break
			}
		}

		if !matchedExplicit {
			if bestRoot != "" && strings.HasPrefix(rp, bestRoot+"/") {
				logical = strings.TrimPrefix(rp, bestRoot+"/")
				includeSet[bestRoot] = struct{}{}
			} else if subdir != "." && strings.HasPrefix(rp, subdir+"/") {
				logical = strings.TrimPrefix(rp, subdir+"/")
				includeSet[subdir] = struct{}{}
			} else if pgDir, ok := ExtractStrictPGDir(rp); ok && strings.HasPrefix(rp, pgDir+"/") {
				logical = strings.TrimPrefix(rp, pgDir+"/")
				includeSet[pgDir] = struct{}{}
			}
		}

		if cleaned, cErr := decoder.SanitizeProtoRelPath(logical, true); cErr == nil {
			logical = cleaned
		}
		logicalByRepoPath[rp] = logical
	}

	includeList := make([]string, 0, len(includeSet))
	for inc := range includeSet {
		includeList = append(includeList, inc)
	}
	sort.Strings(includeList)
	if len(includeList) == 0 {
		includeList = []string{"."}
	}
	return logicalByRepoPath, includeList
}

// SyncImportOwnersForRepo 在 DB 事务内按 MANUAL > BASE_LIB > 全池唯一 AUTO 维护 contract_proto_import_owners。
func SyncImportOwnersForRepo(ctx context.Context, tx *gorm.DB, repoID uint64) error {
	if tx == nil {
		return errors.New("tx 不能为空")
	}

	var repoFiles []models.ProtoFile
	if err := tx.WithContext(ctx).
		Where("git_repo_id = ? AND is_deleted = false AND pipeline_status = ? AND is_external_copy = false",
			repoID, models.ProtoPipelineNormalized).
		Find(&repoFiles).Error; err != nil {
		return err
	}
	if len(repoFiles) == 0 {
		return nil
	}

	logicalSet := make(map[string]struct{}, len(repoFiles))
	for _, rf := range repoFiles {
		if rf.FilePath != "" {
			logicalSet[rf.FilePath] = struct{}{}
		}
	}
	logicalPaths := make([]string, 0, len(logicalSet))
	for lp := range logicalSet {
		logicalPaths = append(logicalPaths, lp)
	}
	sort.Strings(logicalPaths)

	for _, lp := range logicalPaths {
		var existingOwner models.ProtoImportOwner
		err := tx.WithContext(ctx).Where("logical_path = ?", lp).Limit(1).Find(&existingOwner).Error
		if err != nil {
			return err
		}
		if existingOwner.ID > 0 && existingOwner.Source == models.ImportOwnerSourceManual {
			// MANUAL 人工指认优先级最高，永不自动覆盖
			continue
		}

		var candidates []models.ProtoFile
		if err := tx.WithContext(ctx).
			Where("file_path = ? AND is_deleted = false AND pipeline_status = ? AND is_external_copy = false",
				lp, models.ProtoPipelineNormalized).
			Order("id ASC").
			Find(&candidates).Error; err != nil {
			return err
		}

		if len(candidates) == 0 {
			if existingOwner.ID > 0 && existingOwner.Source != models.ImportOwnerSourceManual {
				_ = tx.WithContext(ctx).Delete(&existingOwner).Error
			}
			continue
		}

		var baseLibCands []models.ProtoFile
		for _, c := range candidates {
			if c.Category == models.ProtoCategoryBaseLib {
				baseLibCands = append(baseLibCands, c)
			}
		}

		var chosen *models.ProtoFile
		var source string
		switch {
		case len(baseLibCands) == 1:
			chosen = &baseLibCands[0]
			source = models.ImportOwnerSourceBaseLib
		case len(candidates) == 1:
			chosen = &candidates[0]
			source = models.ImportOwnerSourceAuto
		default:
			// 多提供方且无唯一 BASE_LIB/MANUAL：清除旧 AUTO 登记，等待 Stage D 报 IMPORT_AMBIGUOUS 或人工仲裁
			if existingOwner.ID > 0 && existingOwner.Source != models.ImportOwnerSourceManual {
				_ = tx.WithContext(ctx).Delete(&existingOwner).Error
			}
			continue
		}

		if existingOwner.ID == 0 {
			if err := tx.WithContext(ctx).Create(&models.ProtoImportOwner{
				LogicalPath:    lp,
				OwnerGitRepoID: chosen.GitRepoID,
				OwnerBundleID:  chosen.BundleID,
				Source:         source,
				UpdatedBy:      "system",
			}).Error; err != nil {
				return err
			}
		} else if existingOwner.OwnerBundleID != chosen.BundleID || existingOwner.Source != source {
			if err := tx.WithContext(ctx).Model(&existingOwner).Updates(map[string]any{
				"owner_git_repo_id": chosen.GitRepoID,
				"owner_bundle_id":   chosen.BundleID,
				"source":            source,
				"updated_by":        "system",
			}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// ImportAmbiguityItem 描述多仓提供同一逻辑导入路径的歧义项。
type ImportAmbiguityItem struct {
	LogicalPath string                       `json:"logical_path"`
	Candidates  []decoder.ImportCandidateDTO `json:"candidates"`
}

// SymbolConflictCandidate 描述同一全限定符号在不同仓库中的定义。
type SymbolConflictCandidate struct {
	ProtoFileID   uint64 `json:"proto_file_id"`
	GitRepoID     uint64 `json:"git_repo_id"`
	RepoName      string `json:"repo_name"`
	BundleID      uint64 `json:"bundle_id"`
	BundleName    string `json:"bundle_name"`
	Category      string `json:"category"`
	LogicalPath   string `json:"logical_path"`
	RepoFilePath  string `json:"repo_file_path"`
	ContentSHA256 string `json:"content_sha256"`
	IsExternal    bool   `json:"is_external_copy"`
}

// SymbolConflictItem 描述全限定符号跨仓内容冲突项。
type SymbolConflictItem struct {
	SymbolKind     string                    `json:"symbol_kind"`
	SymbolFullName string                    `json:"symbol_full_name"`
	ContentMatches bool                      `json:"content_matches"`
	Candidates     []SymbolConflictCandidate `json:"candidates"`
}

// ProtoConflictsReportDTO 汇总全部导入路径歧义与符号冲突，供仲裁面板展示。
type ProtoConflictsReportDTO struct {
	ImportAmbiguities []ImportAmbiguityItem `json:"import_ambiguities"`
	SymbolConflicts   []SymbolConflictItem  `json:"symbol_conflicts"`
}

// DetectConflictsAndMarkExternalCopies 执行全池符号冲突预检：
// 1. 若逻辑路径已有权威提供方（MANUAL 或唯一 BASE_LIB），同内容哈希的非权威副本自动标记 is_external_copy=true；
// 2. 列出无权威裁决的导入歧义与内容不一致的符号冲突，供架构师/负责人仲裁。
func DetectConflictsAndMarkExternalCopies(ctx context.Context, db *gorm.DB) (*ProtoConflictsReportDTO, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}

	var files []models.ProtoFile
	if err := db.WithContext(ctx).
		Where("is_deleted = false AND pipeline_status = ?", models.ProtoPipelineNormalized).
		Order("id ASC").
		Find(&files).Error; err != nil {
		return nil, err
	}

	var owners []models.ProtoImportOwner
	if err := db.WithContext(ctx).Find(&owners).Error; err != nil {
		return nil, err
	}
	ownerByPath := make(map[string]models.ProtoImportOwner, len(owners))
	for _, o := range owners {
		ownerByPath[o.LogicalPath] = o
	}

	var repos []models.GitRepo
	_ = db.WithContext(ctx).Select("id, name").Find(&repos).Error
	repoNameByID := make(map[uint64]string, len(repos))
	for _, r := range repos {
		repoNameByID[r.ID] = r.Name
	}
	var bundles []models.GitProtoBundle
	_ = db.WithContext(ctx).Select("id, bundle_name, category").Find(&bundles).Error
	bundleNameByID := make(map[uint64]string, len(bundles))
	for _, b := range bundles {
		bundleNameByID[b.ID] = b.BundleName
	}

	filesByPath := make(map[string][]models.ProtoFile)
	for _, f := range files {
		filesByPath[f.FilePath] = append(filesByPath[f.FilePath], f)
	}

	// 1. 处理有权威提供方且内容哈希一致的副本 -> 自动标记 is_external_copy=true
	externalUpdates := make(map[uint64]bool)
	var importAmbiguities []ImportAmbiguityItem

	allPaths := make([]string, 0, len(filesByPath))
	for lp := range filesByPath {
		allPaths = append(allPaths, lp)
	}
	sort.Strings(allPaths)

	for _, lp := range allPaths {
		group := filesByPath[lp]
		if len(group) <= 1 {
			continue
		}
		var authFile *models.ProtoFile
		if ow, ok := ownerByPath[lp]; ok && (ow.Source == models.ImportOwnerSourceManual || ow.Source == models.ImportOwnerSourceBaseLib) {
			for i := range group {
				if group[i].BundleID == ow.OwnerBundleID {
					authFile = &group[i]
					break
				}
			}
		} else {
			var baseCands []int
			for i := range group {
				if group[i].Category == models.ProtoCategoryBaseLib {
					baseCands = append(baseCands, i)
				}
			}
			if len(baseCands) == 1 {
				authFile = &group[baseCands[0]]
			}
		}

		if authFile != nil {
			externalUpdates[authFile.ID] = false
			for i := range group {
				if group[i].ID == authFile.ID {
					continue
				}
				if group[i].ContentSHA256 != "" && group[i].ContentSHA256 == authFile.ContentSHA256 {
					externalUpdates[group[i].ID] = true
				}
			}
			continue
		}

		// 无唯一权威提供方 -> 记录 IMPORT_AMBIGUOUS 歧义项
		cands := make([]decoder.ImportCandidateDTO, 0, len(group))
		for _, g := range group {
			cands = append(cands, decoder.ImportCandidateDTO{
				GitRepoID:    g.GitRepoID,
				RepoName:     repoNameByID[g.GitRepoID],
				BundleID:     g.BundleID,
				BundleName:   bundleNameByID[g.BundleID],
				Category:     g.Category,
				RepoFilePath: g.RepoFilePath,
				LogicalPath:  g.FilePath,
			})
		}
		importAmbiguities = append(importAmbiguities, ImportAmbiguityItem{
			LogicalPath: lp,
			Candidates:  cands,
		})
	}

	if len(externalUpdates) > 0 {
		for fid, isExt := range externalUpdates {
			if err := db.WithContext(ctx).Model(&models.ProtoFile{}).Where("id = ?", fid).
				Update("is_external_copy", isExt).Error; err != nil {
				return nil, err
			}
		}
		// 刷新内存中的 IsExternalCopy 标记
		for i := range files {
			if isExt, ok := externalUpdates[files[i].ID]; ok {
				files[i].IsExternalCopy = isExt
			}
		}
	}

	// 2. 全限定符号冲突预检 (SERVICE / MESSAGE / ENUM / EXTENSION)
	type symEntry struct {
		kind string
		file models.ProtoFile
	}
	bySymbol := make(map[string][]symEntry)
	for _, f := range files {
		if len(f.SymbolsJSON) == 0 {
			continue
		}
		var syms []ParsedProtoSymbol
		if err := json.Unmarshal(f.SymbolsJSON, &syms); err != nil {
			continue
		}
		for _, s := range syms {
			if s.FullName == "" {
				continue
			}
			bySymbol[s.FullName] = append(bySymbol[s.FullName], symEntry{
				kind: s.Kind,
				file: f,
			})
		}
	}

	symNames := make([]string, 0, len(bySymbol))
	for sn := range bySymbol {
		symNames = append(symNames, sn)
	}
	sort.Strings(symNames)

	var symbolConflicts []SymbolConflictItem
	for _, sn := range symNames {
		entries := bySymbol[sn]
		if len(entries) <= 1 {
			continue
		}
		// 统计不同仓库/Bundle 中的有效（非外部副本）候选
		activeCands := make([]SymbolConflictCandidate, 0, len(entries))
		hashes := make(map[string]struct{})
		distinctBundles := make(map[uint64]struct{})
		for _, e := range entries {
			if e.file.IsExternalCopy {
				continue
			}
			distinctBundles[e.file.BundleID] = struct{}{}
			hashes[e.file.ContentSHA256] = struct{}{}
			activeCands = append(activeCands, SymbolConflictCandidate{
				ProtoFileID:   e.file.ID,
				GitRepoID:     e.file.GitRepoID,
				RepoName:      repoNameByID[e.file.GitRepoID],
				BundleID:      e.file.BundleID,
				BundleName:    bundleNameByID[e.file.BundleID],
				Category:      e.file.Category,
				LogicalPath:   e.file.FilePath,
				RepoFilePath:  e.file.RepoFilePath,
				ContentSHA256: e.file.ContentSHA256,
				IsExternal:    e.file.IsExternalCopy,
			})
		}
		if len(distinctBundles) <= 1 {
			continue
		}
		symbolConflicts = append(symbolConflicts, SymbolConflictItem{
			SymbolKind:     entries[0].kind,
			SymbolFullName: sn,
			ContentMatches: len(hashes) == 1,
			Candidates:     activeCands,
		})
	}

	if importAmbiguities == nil {
		importAmbiguities = []ImportAmbiguityItem{}
	}
	if symbolConflicts == nil {
		symbolConflicts = []SymbolConflictItem{}
	}
	return &ProtoConflictsReportDTO{
		ImportAmbiguities: importAmbiguities,
		SymbolConflicts:   symbolConflicts,
	}, nil
}

// ResolveConflictInput 为管理员指认权威提供方的请求载荷。
type ResolveConflictInput struct {
	Kind           string `json:"kind"` // IMPORT | SYMBOL
	LogicalPath    string `json:"logical_path"`
	SymbolFullName string `json:"symbol_full_name"`
	OwnerGitRepoID uint64 `json:"owner_git_repo_id"`
	OwnerBundleID  uint64 `json:"owner_bundle_id"`
}

// ResolveProtoConflict 落定权威提供方（MANUAL）并将其他非权威副本标记为 is_external_copy=true。
func ResolveProtoConflict(ctx context.Context, db *gorm.DB, operator string, in ResolveConflictInput) ([]models.ProtoImportOwner, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if in.OwnerGitRepoID == 0 || in.OwnerBundleID == 0 {
		return nil, errors.New("必须指定有效的 owner_git_repo_id 与 owner_bundle_id")
	}
	var bundle models.GitProtoBundle
	if err := db.WithContext(ctx).Where("id = ? AND git_repo_id = ?", in.OwnerBundleID, in.OwnerGitRepoID).First(&bundle).Error; err != nil {
		return nil, fmt.Errorf("权威 Bundle 不存在或与仓库不匹配: %w", err)
	}

	kind := strings.ToUpper(strings.TrimSpace(in.Kind))
	if kind == "" {
		kind = "IMPORT"
	}

	var targetPaths []string
	if lp := strings.TrimSpace(in.LogicalPath); lp != "" {
		targetPaths = append(targetPaths, lp)
	} else if kind == "SYMBOL" && strings.TrimSpace(in.SymbolFullName) != "" {
		var ownerFiles []models.ProtoFile
		if err := db.WithContext(ctx).
			Where("bundle_id = ? AND is_deleted = false AND pipeline_status = ?", in.OwnerBundleID, models.ProtoPipelineNormalized).
			Find(&ownerFiles).Error; err != nil {
			return nil, err
		}
		for _, f := range ownerFiles {
			var syms []ParsedProtoSymbol
			if json.Unmarshal(f.SymbolsJSON, &syms) == nil {
				for _, s := range syms {
					if s.FullName == strings.TrimSpace(in.SymbolFullName) {
						targetPaths = append(targetPaths, f.FilePath)
						break
					}
				}
			}
		}
	}

	if len(targetPaths) == 0 {
		return nil, errors.New("未匹配到需要裁决的逻辑路径或符号定义")
	}

	var updatedOwners []models.ProtoImportOwner
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, lp := range targetPaths {
			var ow models.ProtoImportOwner
			if err := tx.Where("logical_path = ?", lp).Limit(1).Find(&ow).Error; err != nil {
				return err
			}
			if ow.ID == 0 {
				ow = models.ProtoImportOwner{
					LogicalPath:    lp,
					OwnerGitRepoID: in.OwnerGitRepoID,
					OwnerBundleID:  in.OwnerBundleID,
					Source:         models.ImportOwnerSourceManual,
					UpdatedBy:      operator,
				}
				if err := tx.Create(&ow).Error; err != nil {
					return err
				}
			} else {
				if err := tx.Model(&ow).Updates(map[string]any{
					"owner_git_repo_id": in.OwnerGitRepoID,
					"owner_bundle_id":   in.OwnerBundleID,
					"source":            models.ImportOwnerSourceManual,
					"updated_by":        operator,
				}).Error; err != nil {
					return err
				}
			}
			updatedOwners = append(updatedOwners, ow)

			// 将非权威的同路径/同符号候选文件标为 is_external_copy=true，确保后续编译与激活零冲突
			if err := tx.Model(&models.ProtoFile{}).
				Where("file_path = ? AND bundle_id <> ? AND is_deleted = false", lp, in.OwnerBundleID).
				Update("is_external_copy", true).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.ProtoFile{}).
				Where("file_path = ? AND bundle_id = ? AND is_deleted = false", lp, in.OwnerBundleID).
				Update("is_external_copy", false).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	_, _ = DetectConflictsAndMarkExternalCopies(ctx, db)
	return updatedOwners, nil
}
