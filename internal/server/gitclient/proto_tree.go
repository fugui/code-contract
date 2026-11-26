package gitclient

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/models"

	"gorm.io/gorm"
)

const (
	TreeKindRaw   = "raw"
	TreeKindClean = "clean"

	UnassignedSubsystemSlug = "_unassigned"
	retainedVersionsPerRepo = 2
)

// ProtoMetaManifest 写入每个 raw/clean 版本目录的 .proto-meta.json。
type ProtoMetaManifest struct {
	GitRepoID         uint64            `json:"git_repo_id"`
	Kind              string            `json:"kind"` // raw | clean
	SubsystemSlug     string            `json:"subsystem_slug"`
	RepoSlug          string            `json:"repo_slug"`
	Branch            string            `json:"branch"`
	CommitSHA         string            `json:"commit_sha"`
	NormalizerVersion string            `json:"normalizer_version,omitempty"`
	GeneratedAt       time.Time         `json:"generated_at"`
	Files             map[string]string `json:"files"` // repo_file_path -> blob/content sha
	QuarantinedFiles  []string          `json:"quarantined_files,omitempty"`
}

// NormalizeReportEntry 描述单个文件在 .normalize-report.json 中的摘要。
type NormalizeReportEntry struct {
	RepoFilePath   string                `json:"repo_file_path"`
	LogicalPath    string                `json:"logical_path,omitempty"`
	PipelineStatus string                `json:"pipeline_status"`
	StatusReason   string                `json:"status_reason,omitempty"`
	IsExternalCopy bool                  `json:"is_external_copy,omitempty"`
	LinePreserving bool                  `json:"line_preserving"`
	LineMap        map[int]int           `json:"line_map,omitempty"`
	Diagnostics    []NormalizeDiagnostic `json:"diagnostics,omitempty"`
}

// NormalizeReportManifest 写入每个 clean 版本目录的 .normalize-report.json。
type NormalizeReportManifest struct {
	GitRepoID         uint64                 `json:"git_repo_id"`
	CommitSHA         string                 `json:"commit_sha"`
	NormalizerVersion string                 `json:"normalizer_version"`
	GeneratedAt       time.Time              `json:"generated_at"`
	Entries           []NormalizeReportEntry `json:"entries"`
}

// NormalizeSlug 按 ADR-26 §2.5.3 规范化子系统或仓库目录名（仅保留 [A-Za-z0-9._-]）。
func NormalizeSlug(raw string, used map[string]string, stableKey string, isSubsystem bool) string {
	trimmed := strings.TrimSpace(raw)
	if isSubsystem && trimmed == "" {
		return UnassignedSubsystemSlug
	}

	var b strings.Builder
	prevDash := false
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		isSafe := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-'
		if isSafe {
			b.WriteByte(c)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")

	shortHash := func(seed string) string {
		sum := sha1.Sum([]byte(seed))
		return hex.EncodeToString(sum[:])[:6]
	}

	isReserved := func(s string) bool {
		return s == "" || s == "." || s == ".." || s == UnassignedSubsystemSlug || s == ".versions" || s == ".tmp"
	}

	if isReserved(slug) {
		if slug == "" || slug == "." || slug == ".." {
			slug = "item-" + shortHash(stableKey)
		} else {
			slug = slug + "-" + shortHash(stableKey)
		}
	}

	if used != nil {
		if owner, exists := used[slug]; exists && owner != stableKey {
			slug = slug + "-" + shortHash(stableKey)
		}
		used[slug] = stableKey
	}
	return slug
}

// ProtoTreeManager 管理 <protos_dir>/{raw,clean}/<subsystem>/<repo> 只读聚合目录树与原子软链切换。
type ProtoTreeManager struct {
	ProtosDir string
}

// NewProtoTreeManager 创建目录树管理器。
func NewProtoTreeManager(protosDir string) *ProtoTreeManager {
	dir := strings.TrimSpace(protosDir)
	if dir == "" {
		dir = config.AppConfig.Storage.GetProtosDir()
	}
	return &ProtoTreeManager{ProtosDir: filepath.Clean(dir)}
}

func (m *ProtoTreeManager) ensureBaseDirs() error {
	for _, sub := range []string{
		m.ProtosDir,
		filepath.Join(m.ProtosDir, TreeKindRaw),
		filepath.Join(m.ProtosDir, TreeKindClean),
		filepath.Join(m.ProtosDir, ".versions", TreeKindRaw),
		filepath.Join(m.ProtosDir, ".versions", TreeKindClean),
		filepath.Join(m.ProtosDir, ".tmp"),
	} {
		if err := os.MkdirAll(sub, 0755); err != nil {
			return err
		}
	}
	return nil
}

// StageVersionDir 先在 .tmp/ 构造完整的版本目录并验证（暂不切换正式软链）。
func (m *ProtoTreeManager) StageVersionDir(
	kind string,
	repoID uint64,
	subsystemSlug string,
	repoSlug string,
	meta ProtoMetaManifest,
	files map[string][]byte,
	normReport *NormalizeReportManifest,
) (stagedDir string, versionName string, err error) {
	if kind != TreeKindRaw && kind != TreeKindClean {
		return "", "", fmt.Errorf("非法的目录树类型: %q", kind)
	}
	if err := m.ensureBaseDirs(); err != nil {
		return "", "", err
	}

	commit12 := strings.TrimSpace(meta.CommitSHA)
	if len(commit12) > 12 {
		commit12 = commit12[:12]
	}
	if commit12 == "" {
		commit12 = "000000000000"
	}
	ts := time.Now().UTC().Format("20060102T150405.000000000")
	if kind == TreeKindClean && strings.TrimSpace(meta.NormalizerVersion) != "" {
		normSafe := NormalizeSlug(meta.NormalizerVersion, nil, meta.NormalizerVersion, false)
		versionName = fmt.Sprintf("%s_%s_%s", commit12, normSafe, ts)
	} else {
		versionName = fmt.Sprintf("%s_%s", commit12, ts)
	}

	tmpDir, err := os.MkdirTemp(filepath.Join(m.ProtosDir, ".tmp"), fmt.Sprintf("%s-%d-", kind, repoID))
	if err != nil {
		return "", "", err
	}

	cleanupOnErr := true
	defer func() {
		if cleanupOnErr {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	meta.GitRepoID = repoID
	meta.Kind = kind
	meta.SubsystemSlug = subsystemSlug
	meta.RepoSlug = repoSlug
	if meta.GeneratedAt.IsZero() {
		meta.GeneratedAt = time.Now().UTC()
	}
	if meta.Files == nil {
		meta.Files = map[string]string{}
	}

	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".proto-meta.json"), metaBytes, 0444); err != nil {
		return "", "", err
	}

	if kind == TreeKindClean && normReport != nil {
		repBytes, err := json.MarshalIndent(normReport, "", "  ")
		if err != nil {
			return "", "", err
		}
		if err := os.WriteFile(filepath.Join(tmpDir, ".normalize-report.json"), repBytes, 0444); err != nil {
			return "", "", err
		}
	}

	relPaths := make([]string, 0, len(files))
	for p := range files {
		relPaths = append(relPaths, p)
	}
	sort.Strings(relPaths)

	for _, relPath := range relPaths {
		cleaned, sErr := decoder.SanitizeProtoRelPath(relPath, true)
		if sErr != nil {
			return "", "", sErr
		}
		dest := filepath.Join(tmpDir, filepath.FromSlash(cleaned))
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return "", "", err
		}
		if err := os.WriteFile(dest, files[relPath], 0444); err != nil {
			return "", "", err
		}
	}

	cleanupOnErr = false
	return tmpDir, versionName, nil
}

// CommitAndSwitchVersion 将已校验的 .tmp 目录 rename 到 .versions/<kind>/<repoID>/<ver>，并原子切换符号链接。
func (m *ProtoTreeManager) CommitAndSwitchVersion(
	kind string,
	repoID uint64,
	subsystemSlug string,
	repoSlug string,
	stagedDir string,
	versionName string,
) (string, error) {
	if subsystemSlug == "" {
		subsystemSlug = UnassignedSubsystemSlug
	}
	if repoSlug == "" {
		repoSlug = fmt.Sprintf("repo-%d", repoID)
	}
	repoVerRoot := filepath.Join(m.ProtosDir, ".versions", kind, strconv.FormatUint(repoID, 10))
	if err := os.MkdirAll(repoVerRoot, 0755); err != nil {
		return "", err
	}
	finalVerDir := filepath.Join(repoVerRoot, versionName)
	if err := os.Rename(stagedDir, finalVerDir); err != nil {
		return "", fmt.Errorf("迁移版本目录到 %s 失败: %w", finalVerDir, err)
	}

	subDir := filepath.Join(m.ProtosDir, kind, subsystemSlug)
	if err := os.MkdirAll(subDir, 0755); err != nil {
		return "", err
	}

	relTarget := filepath.ToSlash(filepath.Join("..", "..", ".versions", kind, strconv.FormatUint(repoID, 10), versionName))
	finalLink := filepath.Join(subDir, repoSlug)
	tmpLink := filepath.Join(subDir, fmt.Sprintf(".tmp-%s-%d", repoSlug, time.Now().UnixNano()))
	_ = os.Remove(tmpLink)
	if err := os.Symlink(relTarget, tmpLink); err != nil {
		return "", fmt.Errorf("创建临时符号链接失败: %w", err)
	}
	if err := os.Rename(tmpLink, finalLink); err != nil {
		_ = os.Remove(tmpLink)
		return "", fmt.Errorf("原子替换符号链接失败: %w", err)
	}

	// 清理该仓库在其他子系统/旧 slug 下的残留符号链接（零拷贝搬迁）
	m.cleanupStaleRepoSymlinks(kind, repoID, subsystemSlug, repoSlug)
	// 每仓仅保留最近 2 个版本目录
	m.pruneRepoVersions(repoVerRoot, versionName, retainedVersionsPerRepo)
	return finalLink, nil
}

// WriteAndSwitchRepoTree 一步写入版本目录并原子切换软链（适合单阶段测试或对账重建）。
func (m *ProtoTreeManager) WriteAndSwitchRepoTree(
	kind string,
	repoID uint64,
	subsystemSlug string,
	repoSlug string,
	meta ProtoMetaManifest,
	files map[string][]byte,
	normReport *NormalizeReportManifest,
) (string, error) {
	staged, verName, err := m.StageVersionDir(kind, repoID, subsystemSlug, repoSlug, meta, files, normReport)
	if err != nil {
		return "", err
	}
	linkPath, err := m.CommitAndSwitchVersion(kind, repoID, subsystemSlug, repoSlug, staged, verName)
	if err != nil {
		_ = os.RemoveAll(staged)
		return "", err
	}
	return linkPath, nil
}

func (m *ProtoTreeManager) cleanupStaleRepoSymlinks(kind string, repoID uint64, keepSubSlug, keepRepoSlug string) {
	kindRoot := filepath.Join(m.ProtosDir, kind)
	needle := fmt.Sprintf("/.versions/%s/%d/", kind, repoID)
	subDirs, err := os.ReadDir(kindRoot)
	if err != nil {
		return
	}
	for _, sd := range subDirs {
		if !sd.IsDir() {
			continue
		}
		subPath := filepath.Join(kindRoot, sd.Name())
		entries, err := os.ReadDir(subPath)
		if err != nil {
			continue
		}
		for _, ent := range entries {
			if sd.Name() == keepSubSlug && ent.Name() == keepRepoSlug {
				continue
			}
			full := filepath.Join(subPath, ent.Name())
			target, rErr := os.Readlink(full)
			if rErr == nil && strings.Contains(filepath.ToSlash(target), needle) {
				_ = os.Remove(full)
			}
		}
		// 若子系统目录已空且非目标目录，顺手清理空目录
		if sd.Name() != keepSubSlug {
			if rem, rErr := os.ReadDir(subPath); rErr == nil && len(rem) == 0 {
				_ = os.Remove(subPath)
			}
		}
	}
}

func (m *ProtoTreeManager) pruneRepoVersions(repoVerRoot, activeVerName string, keep int) {
	entries, err := os.ReadDir(repoVerRoot)
	if err != nil || len(entries) <= keep {
		return
	}
	type verEntry struct {
		name    string
		modTime time.Time
	}
	var list []verEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, iErr := e.Info()
		mt := time.Time{}
		if iErr == nil {
			mt = info.ModTime()
		}
		list = append(list, verEntry{name: e.Name(), modTime: mt})
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].modTime.Equal(list[j].modTime) {
			return list[i].modTime.After(list[j].modTime)
		}
		return list[i].name > list[j].name
	})
	kept := 0
	for _, item := range list {
		if item.name == activeVerName || kept < keep {
			kept++
			continue
		}
		_ = os.RemoveAll(filepath.Join(repoVerRoot, item.name))
	}
}

// ReconcileAll 比对磁盘 .proto-meta.json 与 DB 状态，不一致时从 DB 重建 clean/raw 子树，并清理未纳管孤儿目录与链接。
func (m *ProtoTreeManager) ReconcileAll(ctx context.Context, db *gorm.DB) (rebuilt int, prunedOrphans int, err error) {
	if db == nil {
		return 0, 0, nil
	}
	if err := m.ensureBaseDirs(); err != nil {
		return 0, 0, err
	}

	var repos []models.GitRepo
	if err := db.WithContext(ctx).Order("id ASC").Find(&repos).Error; err != nil {
		return 0, 0, err
	}
	repoByID := make(map[uint64]models.GitRepo, len(repos))
	platMap := loadPlatformRepoMetaMap(db, repos)
	usedSubs := make(map[string]string)
	usedRepoSlugs := make(map[string]map[string]string)

	for _, r := range repos {
		repoByID[r.ID] = r
		rawSub := ""
		if r.PlatformRepoID != nil {
			if pr, ok := platMap[*r.PlatformRepoID]; ok {
				rawSub = pr.ServiceGroup
			}
		}
		subSlug := r.SubsystemSlug
		if subSlug == "" || rawSub != "" {
			subSlug = NormalizeSlug(rawSub, usedSubs, fmt.Sprintf("sub:%s", rawSub), true)
		}
		if usedRepoSlugs[subSlug] == nil {
			usedRepoSlugs[subSlug] = make(map[string]string)
		}
		repoSlug := NormalizeSlug(r.Name, usedRepoSlugs[subSlug], fmt.Sprintf("repo:%d", r.ID), false)

		var pfiles []models.ProtoFile
		if fErr := db.WithContext(ctx).Where("git_repo_id = ? AND is_deleted = false", r.ID).Find(&pfiles).Error; fErr != nil || len(pfiles) == 0 {
			continue
		}

		// 检查 clean 链接及 .proto-meta.json 是否与 DB 一致
		cleanLink := filepath.Join(m.ProtosDir, TreeKindClean, subSlug, repoSlug)
		if !m.isCleanTreeInSync(cleanLink, r.LastSyncCommit, pfiles) {
			if rebuildErr := m.rebuildRepoTreesFromDB(ctx, db, r, subSlug, repoSlug, pfiles); rebuildErr != nil {
				log.Printf("[ProtoTreeReconciler] 重建仓库 #%d (%s) 目录树失败: %v", r.ID, r.Name, rebuildErr)
			} else {
				rebuilt++
			}
		}
	}

	// 全局孤儿回收：删除不在 repoByID 中的版本目录与失效/孤儿软链
	for _, kind := range []string{TreeKindRaw, TreeKindClean} {
		verKindDir := filepath.Join(m.ProtosDir, ".versions", kind)
		if idDirs, rErr := os.ReadDir(verKindDir); rErr == nil {
			for _, idDir := range idDirs {
				if !idDir.IsDir() {
					continue
				}
				rid, pErr := strconv.ParseUint(idDir.Name(), 10, 64)
				if pErr != nil {
					_ = os.RemoveAll(filepath.Join(verKindDir, idDir.Name()))
					prunedOrphans++
					continue
				}
				if _, ok := repoByID[rid]; !ok {
					_ = os.RemoveAll(filepath.Join(verKindDir, idDir.Name()))
					prunedOrphans++
				}
			}
		}

		kindDir := filepath.Join(m.ProtosDir, kind)
		if subDirs, rErr := os.ReadDir(kindDir); rErr == nil {
			for _, sd := range subDirs {
				if !sd.IsDir() {
					continue
				}
				sdPath := filepath.Join(kindDir, sd.Name())
				entries, _ := os.ReadDir(sdPath)
				for _, ent := range entries {
					full := filepath.Join(sdPath, ent.Name())
					resolved, statErr := filepath.EvalSymlinks(full)
					if statErr != nil || !strings.HasPrefix(resolved, m.ProtosDir) {
						_ = os.Remove(full)
						prunedOrphans++
					}
				}
				if rem, _ := os.ReadDir(sdPath); len(rem) == 0 {
					_ = os.Remove(sdPath)
				}
			}
		}
	}

	return rebuilt, prunedOrphans, nil
}

func (m *ProtoTreeManager) isCleanTreeInSync(cleanLink, expectedCommit string, pfiles []models.ProtoFile) bool {
	if expectedCommit == "" {
		return true
	}
	metaBytes, err := os.ReadFile(filepath.Join(cleanLink, ".proto-meta.json"))
	if err != nil {
		return false
	}
	var meta ProtoMetaManifest
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return false
	}
	if meta.CommitSHA != expectedCommit {
		return false
	}
	for _, pf := range pfiles {
		if pf.PipelineStatus != models.ProtoPipelineNormalized {
			continue
		}
		rel := pf.RepoFilePath
		if rel == "" {
			rel = pf.FilePath
		}
		if meta.Files[rel] == "" {
			return false
		}
	}
	return true
}

func (m *ProtoTreeManager) rebuildRepoTreesFromDB(
	ctx context.Context,
	db *gorm.DB,
	repo models.GitRepo,
	subSlug string,
	repoSlug string,
	pfiles []models.ProtoFile,
) error {
	verIDs := make([]uint64, 0, len(pfiles))
	for _, pf := range pfiles {
		if pf.LatestVersionID != nil && *pf.LatestVersionID > 0 {
			verIDs = append(verIDs, *pf.LatestVersionID)
		}
	}
	verByFile := make(map[uint64]models.ProtoFileVersion, len(verIDs))
	if len(verIDs) > 0 {
		var vers []models.ProtoFileVersion
		if err := db.WithContext(ctx).Where("id IN ?", verIDs).Find(&vers).Error; err != nil {
			return err
		}
		for _, v := range vers {
			verByFile[v.ProtoFileID] = v
		}
	}

	rawFiles := make(map[string][]byte)
	rawHashes := make(map[string]string)
	cleanFiles := make(map[string][]byte)
	cleanHashes := make(map[string]string)
	var quarantined []string
	var reportEntries []NormalizeReportEntry
	normVer := "passthrough-0"

	for _, pf := range pfiles {
		rel := pf.RepoFilePath
		if rel == "" {
			rel = pf.FilePath
		}
		v, ok := verByFile[pf.ID]
		if !ok {
			continue
		}
		if pf.NormalizerVersion != "" {
			normVer = pf.NormalizerVersion
		}
		rawText := v.RawContentText
		if rawText == "" {
			rawText = v.ContentText
		}
		rawFiles[rel] = []byte(rawText)
		rawHashes[rel] = pf.RawBlobSHA

		if pf.PipelineStatus == models.ProtoPipelineQuarantined {
			quarantined = append(quarantined, rel)
		} else {
			cleanFiles[rel] = []byte(v.ContentText)
			cleanHashes[rel] = pf.ContentSHA256
		}
		var lm map[int]int
		if len(pf.LineMapJSON) > 0 {
			_ = json.Unmarshal(pf.LineMapJSON, &lm)
		}
		reportEntries = append(reportEntries, NormalizeReportEntry{
			RepoFilePath:   rel,
			LogicalPath:    pf.FilePath,
			PipelineStatus: pf.PipelineStatus,
			StatusReason:   pf.StatusReason,
			IsExternalCopy: pf.IsExternalCopy,
			LinePreserving: len(lm) == 0,
			LineMap:        lm,
		})
	}

	commit := repo.LastSyncCommit
	if commit == "" {
		commit = repo.LastCollectedCommit
	}
	branch := repo.ResolvedBranch
	if branch == "" {
		branch = repo.DefaultBranch
	}

	if _, err := m.WriteAndSwitchRepoTree(TreeKindRaw, repo.ID, subSlug, repoSlug, ProtoMetaManifest{
		Branch:    branch,
		CommitSHA: commit,
		Files:     rawHashes,
	}, rawFiles, nil); err != nil {
		return err
	}

	if _, err := m.WriteAndSwitchRepoTree(TreeKindClean, repo.ID, subSlug, repoSlug, ProtoMetaManifest{
		Branch:            branch,
		CommitSHA:         commit,
		NormalizerVersion: normVer,
		Files:             cleanHashes,
		QuarantinedFiles:  quarantined,
	}, cleanFiles, &NormalizeReportManifest{
		GitRepoID:         repo.ID,
		CommitSHA:         commit,
		NormalizerVersion: normVer,
		GeneratedAt:       time.Now().UTC(),
		Entries:           reportEntries,
	}); err != nil {
		return err
	}
	return nil
}

func loadPlatformRepoMetaMap(db *gorm.DB, repos []models.GitRepo) map[uint64]models.Repository {
	out := make(map[uint64]models.Repository)
	if db == nil {
		return out
	}
	var ids []uint
	for _, r := range repos {
		if r.PlatformRepoID != nil && *r.PlatformRepoID > 0 {
			ids = append(ids, uint(*r.PlatformRepoID))
		}
	}
	if len(ids) == 0 {
		return out
	}
	var plats []models.Repository
	if err := db.Where("id IN ?", ids).Find(&plats).Error; err == nil {
		for _, p := range plats {
			out[uint64(p.ID)] = p
		}
	}
	return out
}
