package gitclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"code-common/backend/gitsync"
	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/linter"
	"code-contract/internal/server/protodoc"
	"code-contract/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// FileDependencyGraph 表示全池 NORMALIZED 文件级有向无环依赖图与 Bundle 投影。
type FileDependencyGraph struct {
	FileBundle   map[string]uint64              // logical_path -> bundle_id
	ForwardEdges map[string]map[string]struct{} // file -> imported files
	ReverseEdges map[string]map[string]struct{} // imported file -> dependent files
}

// BuildFileDependencyGraph 从全部活跃且非外部副本的 NORMALIZED 文件构造文件级 DAG。
func BuildFileDependencyGraph(files []models.ProtoFile) FileDependencyGraph {
	g := FileDependencyGraph{
		FileBundle:   make(map[string]uint64, len(files)),
		ForwardEdges: make(map[string]map[string]struct{}, len(files)),
		ReverseEdges: make(map[string]map[string]struct{}, len(files)),
	}
	for _, f := range files {
		if f.IsDeleted || f.PipelineStatus != models.ProtoPipelineNormalized || f.IsExternalCopy {
			continue
		}
		if f.FilePath == "" {
			continue
		}
		g.FileBundle[f.FilePath] = f.BundleID
		var imps []string
		if len(f.ImportsJSON) > 0 {
			_ = json.Unmarshal(f.ImportsJSON, &imps)
		}
		if g.ForwardEdges[f.FilePath] == nil {
			g.ForwardEdges[f.FilePath] = make(map[string]struct{})
		}
		for _, imp := range imps {
			cleaned := strings.Trim(strings.TrimSpace(imp), "/")
			if cleaned == "" || strings.HasPrefix(cleaned, "google/protobuf/") {
				continue
			}
			g.ForwardEdges[f.FilePath][cleaned] = struct{}{}
			if g.ReverseEdges[cleaned] == nil {
				g.ReverseEdges[cleaned] = make(map[string]struct{})
			}
			g.ReverseEdges[cleaned][f.FilePath] = struct{}{}
		}
	}
	return g
}

// ComputeBundleSCCs 将文件级依赖图投影到 Bundle 级，使用 Tarjan 强连通分量算法缩点分组。
// 返回拓扑有序（被依赖的 SCC 在前）的 Bundle ID 分组列表。
func ComputeBundleSCCs(bundleIDs []uint64, g FileDependencyGraph) [][]uint64 {
	bundleSet := make(map[uint64]struct{}, len(bundleIDs))
	adj := make(map[uint64]map[uint64]struct{}, len(bundleIDs))
	for _, id := range bundleIDs {
		bundleSet[id] = struct{}{}
		adj[id] = make(map[uint64]struct{})
	}
	for fromFile, toFiles := range g.ForwardEdges {
		fromBundle, ok := g.FileBundle[fromFile]
		if !ok {
			continue
		}
		if _, inSet := bundleSet[fromBundle]; !inSet {
			continue
		}
		for toFile := range toFiles {
			toBundle, ok := g.FileBundle[toFile]
			if !ok || toBundle == fromBundle {
				continue
			}
			if _, inSet := bundleSet[toBundle]; !inSet {
				continue
			}
			adj[fromBundle][toBundle] = struct{}{}
		}
	}

	sortedIDs := append([]uint64(nil), bundleIDs...)
	sort.Slice(sortedIDs, func(i, j int) bool { return sortedIDs[i] < sortedIDs[j] })

	var (
		index   = 0
		indices = make(map[uint64]int, len(sortedIDs))
		lowlink = make(map[uint64]int, len(sortedIDs))
		onStack = make(map[uint64]bool, len(sortedIDs))
		stack   = make([]uint64, 0, len(sortedIDs))
		sccs    [][]uint64
	)

	var strongConnect func(v uint64)
	strongConnect = func(v uint64) {
		index++
		indices[v] = index
		lowlink[v] = index
		stack = append(stack, v)
		onStack[v] = true

		neighbors := make([]uint64, 0, len(adj[v]))
		for w := range adj[v] {
			neighbors = append(neighbors, w)
		}
		sort.Slice(neighbors, func(i, j int) bool { return neighbors[i] < neighbors[j] })

		for _, w := range neighbors {
			if indices[w] == 0 {
				strongConnect(w)
				if lowlink[w] < lowlink[v] {
					lowlink[v] = lowlink[w]
				}
			} else if onStack[w] {
				if indices[w] < lowlink[v] {
					lowlink[v] = indices[w]
				}
			}
		}

		if lowlink[v] == indices[v] {
			var comp []uint64
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp = append(comp, w)
				if w == v {
					break
				}
			}
			sort.Slice(comp, func(i, j int) bool { return comp[i] < comp[j] })
			sccs = append(sccs, comp)
		}
	}

	for _, id := range sortedIDs {
		if indices[id] == 0 {
			strongConnect(id)
		}
	}
	return sccs
}

// ComputeBlastRadius 在文件级 DAG 上从变更文件反向求可达集，并映射到受影响的 Bundle ID 集合（按依赖序排列）。
func ComputeBlastRadius(ctx context.Context, db *gorm.DB, changedLogicalPaths []string, excludeBundleIDs ...uint64) ([]uint64, error) {
	if db == nil || len(changedLogicalPaths) == 0 {
		return nil, nil
	}
	var allFiles []models.ProtoFile
	if err := db.WithContext(ctx).
		Where("is_deleted = false AND pipeline_status = ? AND is_external_copy = false", models.ProtoPipelineNormalized).
		Find(&allFiles).Error; err != nil {
		return nil, err
	}
	g := BuildFileDependencyGraph(allFiles)

	visitedFiles := make(map[string]struct{}, len(changedLogicalPaths))
	queue := make([]string, 0, len(changedLogicalPaths))
	for _, p := range changedLogicalPaths {
		cleaned := strings.Trim(strings.TrimSpace(p), "/")
		if cleaned == "" {
			continue
		}
		if _, seen := visitedFiles[cleaned]; !seen {
			visitedFiles[cleaned] = struct{}{}
			queue = append(queue, cleaned)
		}
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for dependent := range g.ReverseEdges[cur] {
			if _, seen := visitedFiles[dependent]; !seen {
				visitedFiles[dependent] = struct{}{}
				queue = append(queue, dependent)
			}
		}
	}

	excludeSet := make(map[uint64]struct{}, len(excludeBundleIDs))
	for _, id := range excludeBundleIDs {
		excludeSet[id] = struct{}{}
	}
	affectedBundleSet := make(map[uint64]struct{})
	for fPath := range visitedFiles {
		if bID, ok := g.FileBundle[fPath]; ok && bID > 0 {
			if _, ex := excludeSet[bID]; !ex {
				affectedBundleSet[bID] = struct{}{}
			}
		}
	}
	if len(affectedBundleSet) == 0 {
		return nil, nil
	}
	bundleIDs := make([]uint64, 0, len(affectedBundleSet))
	for bID := range affectedBundleSet {
		bundleIDs = append(bundleIDs, bID)
	}
	sccs := ComputeBundleSCCs(bundleIDs, g)
	ordered := make([]uint64, 0, len(bundleIDs))
	for _, scc := range sccs {
		ordered = append(ordered, scc...)
	}
	return ordered, nil
}

// PipelineOptions 控制四段式流水线运行。
type PipelineOptions struct {
	Branch                string
	OperatorID            uint
	CachePolicy           string // CACHE_ONLY | FETCH_MISSING
	SkipIfCommitUnchanged bool
	ForceRecompile        bool
	ForceRenormalize      bool
	SkipCompile           bool // ADR-29: 为 true 时仅运行 Stage A+B 采集清洗落库，不触发单仓盲编
	AlreadyLocked         bool // ADR-29: 由外部（如 RunSyncAllJob / RunFullProtoPipeline）已持有全局锁
	Normalizer            Normalizer
	AuxConfigJSON         []byte
	CodesDir              string
	ProtosDir             string
	SharedWriteEnabled    bool
	TriggerBlastRadius    bool
}

// PipelineRunResult 为四段式流水线的完整产出摘要。
type PipelineRunResult struct {
	RepoID               uint64             `json:"repo_id"`
	RepoName             string             `json:"repo_name"`
	Branch               string             `json:"branch"`
	CommitSHA            string             `json:"commit_sha"`
	ProtoStatus          string             `json:"proto_status"`
	SkippedUnchanged     bool               `json:"skipped_unchanged"`
	NormalizerVersion    string             `json:"normalizer_version"`
	CollectedCount       int                `json:"collected_count"`
	NormalizedCount      int                `json:"normalized_count"`
	QuarantinedCount     int                `json:"quarantined_count"`
	ExternalCopyCount    int                `json:"external_copy_count"`
	OutsideWhitelist     int                `json:"outside_whitelist_count"`
	BundleResults        []SyncBundleResult `json:"bundle_results"`
	BlastRadiusBundleIDs []uint64           `json:"blast_radius_bundle_ids,omitempty"`
	BreakingAlertCount   int                `json:"breaking_alert_count"`
}

// RunFourStagePipeline 执行 ADR-26 「Stage A 采集 -> Stage B 清理整顿 -> Stage C 解析 -> Stage D 分仓编译」四段式流水线。
func RunFourStagePipeline(
	ctx context.Context,
	db *gorm.DB,
	reg *decoder.Registry,
	broadcaster EventBroadcaster,
	repoID uint64,
	opts PipelineOptions,
) (*PipelineRunResult, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if !opts.AlreadyLocked {
		lk, lErr := AcquirePipelineLockWithTimeout(ctx, db, PipelineLockHolder{
			JobType: "REPO_SYNC",
			JobID:   repoID,
			Holder:  fmt.Sprintf("user-%d", opts.OperatorID),
		}, 10*time.Minute)
		if lErr != nil {
			return nil, lErr
		}
		defer lk.Release()
	}
	var repo models.GitRepo
	if err := db.WithContext(ctx).First(&repo, repoID).Error; err != nil {
		return nil, err
	}
	// PLATFORM_SHARED 实时引用公共 repositories 的 URL/分支/子系统
	var platRepo *models.Repository
	if repo.PlatformRepoID > 0 {
		var pr models.Repository
		if err := db.WithContext(ctx).First(&pr, repo.PlatformRepoID).Error; err == nil {
			platRepo = &pr
			effectiveURL := strings.TrimSpace(pr.URL)
			if effectiveURL == "" {
				effectiveURL = strings.TrimSpace(pr.HTTPURL)
			}
			if effectiveURL != "" {
				repo.RepoURL = effectiveURL
			}
			if strings.TrimSpace(pr.Branch) != "" {
				repo.DefaultBranch = pr.Branch
			}
		}
	}

	var govDoc *GovernanceConfigDoc
	if snap, gErr := LoadActiveGovernanceConfig(ctx, db); gErr == nil && snap != nil {
		d := snap.Doc
		govDoc = &d
	}
	norm := opts.Normalizer
	if norm == nil {
		if len(opts.AuxConfigJSON) == 0 {
			opts.AuxConfigJSON = BuildEffectiveRepoAuxJSON(repo, govDoc)
		}
		norm = NewRuleBasedNormalizer(govDoc)
	}
	normVer := EffectiveNormalizerVersion(norm, opts.AuxConfigJSON)

	// 读取现有记录以支持 Stage A blob 增量跳过 & Stage B (raw_blob_sha, normalizer_version) 增量跳过
	var existingFiles []models.ProtoFile
	if err := db.WithContext(ctx).Where("git_repo_id = ? AND is_deleted = false", repo.ID).Find(&existingFiles).Error; err != nil {
		return nil, err
	}
	existingByRepoPath := make(map[string]models.ProtoFile, len(existingFiles))
	knownSHAs := make(map[string]string, len(existingFiles))
	verIDs := make([]uint64, 0, len(existingFiles))
	for _, ef := range existingFiles {
		rp := ef.RepoFilePath
		if rp == "" {
			rp = ef.FilePath
		}
		existingByRepoPath[rp] = ef
		if ef.RawBlobSHA != "" {
			knownSHAs[rp] = ef.RawBlobSHA
		}
		if ef.LatestVersionID != nil && *ef.LatestVersionID > 0 {
			verIDs = append(verIDs, *ef.LatestVersionID)
		}
	}
	existingVerByFile := make(map[uint64]models.ProtoFileVersion, len(verIDs))
	if len(verIDs) > 0 {
		var vers []models.ProtoFileVersion
		if err := db.WithContext(ctx).Where("id IN ?", verIDs).Find(&vers).Error; err == nil {
			for _, v := range vers {
				existingVerByFile[v.ProtoFileID] = v
			}
		}
	}

	codesDir := opts.CodesDir
	if strings.TrimSpace(codesDir) == "" {
		codesDir = config.AppConfig.Storage.GetCodesDir()
	}
	protosDir := opts.ProtosDir
	if strings.TrimSpace(protosDir) == "" {
		protosDir = config.AppConfig.Storage.GetProtosDir()
	}

	skipIfUnchanged := opts.SkipIfCommitUnchanged && !opts.ForceRecompile && !opts.ForceRenormalize &&
		repo.ProtoStatus != "" && repo.ProtoStatus != models.GitRepoProtoStatusPending &&
		repo.ProtoStatus != models.GitRepoProtoStatusNoLocalCache && repo.ProtoStatus != models.GitRepoProtoStatusSyncFailed

	var globalCollectGlobs, globalExcludeGlobs []string
	if govDoc != nil {
		globalCollectGlobs = govDoc.Global.CollectGlobs
		globalExcludeGlobs = govDoc.Global.ExcludeGlobs
	}

	// Stage A: pg 白名单采集 + 增量 blob 读取
	collectRes, err := CollectRepoStageA(ctx, &repo, opts.Branch, CollectConfig{
		CodesDir:              codesDir,
		SharedWriteEnabled:    opts.SharedWriteEnabled,
		CachePolicy:           opts.CachePolicy,
		KnownCommitSHA:        repo.LastCollectedCommit,
		SkipIfCommitUnchanged: skipIfUnchanged,
		CollectGlobs:          globalCollectGlobs,
		ExcludeGlobs:          globalExcludeGlobs,
		MaxFileBytes:          config.AppConfig.ProtoCollect.MaxFileBytes,
		MaxFilesPerRepo:       config.AppConfig.ProtoCollect.MaxFilesPerRepo,
		KnownRawBlobSHAs:      knownSHAs,
	})
	if err != nil {
		now := time.Now().UTC()
		if errors.Is(err, ErrLocalCacheMissing) {
			_ = db.Model(&repo).Updates(map[string]any{
				"proto_status":     models.GitRepoProtoStatusNoLocalCache,
				"last_sync_status": models.GitSyncStatusIdle,
				"last_sync_error":  "",
			}).Error
			return &PipelineRunResult{
				RepoID:      repo.ID,
				RepoName:    repo.Name,
				Branch:      opts.Branch,
				ProtoStatus: models.GitRepoProtoStatusNoLocalCache,
			}, nil
		}
		_ = db.Model(&repo).Updates(map[string]any{
			"proto_status":     models.GitRepoProtoStatusSyncFailed,
			"last_sync_at":     &now,
			"last_sync_status": models.GitSyncStatusFailed,
			"last_sync_error":  err.Error(),
		}).Error
		return nil, err
	}

	if collectRes.SkippedUnchanged {
		commitSHA := ""
		if collectRes.CommitMeta != nil {
			commitSHA = collectRes.CommitMeta.CommitSHA
		}
		return &PipelineRunResult{
			RepoID:           repo.ID,
			RepoName:         repo.Name,
			Branch:           collectRes.ResolvedBranch,
			CommitSHA:        commitSHA,
			ProtoStatus:      repo.ProtoStatus,
			SkippedUnchanged: true,
		}, nil
	}

	// 500+ 仓静默分层：未命中任何全小写 pg/ 或白名单文件数为 0 -> 标 NO_PROTO，不建空 Bundle 与空目录
	if len(collectRes.Files) == 0 {
		commitSHA := ""
		var commitTime *time.Time
		if collectRes.CommitMeta != nil {
			commitSHA = collectRes.CommitMeta.CommitSHA
			if !collectRes.CommitMeta.CommittedAt.IsZero() {
				ct := collectRes.CommitMeta.CommittedAt
				commitTime = &ct
			}
		}
		now := time.Now().UTC()
		if len(existingFiles) > 0 {
			_ = db.Model(&models.ProtoFile{}).
				Where("git_repo_id = ? AND is_deleted = false", repo.ID).
				Update("is_deleted", true).Error
		}
		updates := map[string]any{
			"proto_status":          models.GitRepoProtoStatusNoProto,
			"resolved_branch":       collectRes.ResolvedBranch,
			"last_collected_commit": commitSHA,
			"last_sync_commit":      commitSHA,
			"last_sync_at":          &now,
			"last_sync_status":      models.GitSyncStatusSuccess,
			"last_sync_error":       "",
		}
		if collectRes.EffectiveSyncStrategy != models.GitSyncStrategyLocalCacheOnly {
			updates["last_fetched_at"] = &now
		}
		if commitTime != nil {
			updates["ref_commit_time"] = commitTime
		}
		_ = db.Model(&repo).Updates(updates).Error
		treeMgr := NewProtoTreeManager(protosDir)
		treeMgr.RemoveRepoTree(TreeKindRaw, repo.ID)
		treeMgr.RemoveRepoTree(TreeKindClean, repo.ID)
		return &PipelineRunResult{
			RepoID:           repo.ID,
			RepoName:         repo.Name,
			Branch:           collectRes.ResolvedBranch,
			CommitSHA:        commitSHA,
			ProtoStatus:      models.GitRepoProtoStatusNoProto,
			OutsideWhitelist: collectRes.OutsideWhitelistCount,
		}, nil
	}

	// 补齐增量复用的 raw 字节
	rawFilesMap := make(map[string][]byte, len(collectRes.Files))
	rawBlobSHAMap := make(map[string]string, len(collectRes.Files))
	for i := range collectRes.Files {
		cf := &collectRes.Files[i]
		rawBlobSHAMap[cf.RepoFilePath] = cf.RawBlobSHA
		if cf.IncrementalReused {
			if ef, ok := existingByRepoPath[cf.RepoFilePath]; ok {
				if ev, vok := existingVerByFile[ef.ID]; vok {
					rawText := ev.RawContentText
					if rawText == "" {
						rawText = ev.ContentText
					}
					cf.Content = []byte(rawText)
				}
			}
		}
		rawFilesMap[cf.RepoFilePath] = cf.Content
	}

	return executeStagesBToD(ctx, db, reg, broadcaster, &repo, platRepo, collectRes, rawFilesMap, rawBlobSHAMap, existingByRepoPath, existingVerByFile, norm, normVer, protosDir, opts)
}

// ReNormalizeRepo 直接基于 DB 留存的 RawContentText 重跑 Stage B~D（规则升级后无需重新 git fetch）。
func ReNormalizeRepo(
	ctx context.Context,
	db *gorm.DB,
	reg *decoder.Registry,
	broadcaster EventBroadcaster,
	repoID uint64,
	opts PipelineOptions,
) (*PipelineRunResult, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if !opts.AlreadyLocked {
		lk, lErr := TryAcquirePipelineLock(ctx, db, PipelineLockHolder{
			JobType: "RENORMALIZE",
			JobID:   repoID,
			Holder:  fmt.Sprintf("user-%d", opts.OperatorID),
		})
		if lErr != nil {
			return nil, lErr
		}
		defer lk.Release()
	}
	var repo models.GitRepo
	if err := db.WithContext(ctx).First(&repo, repoID).Error; err != nil {
		return nil, err
	}
	var platRepo *models.Repository
	if repo.PlatformRepoID > 0 {
		var pr models.Repository
		if err := db.WithContext(ctx).First(&pr, repo.PlatformRepoID).Error; err == nil {
			platRepo = &pr
		}
	}
	var existingFiles []models.ProtoFile
	if err := db.WithContext(ctx).Where("git_repo_id = ? AND is_deleted = false", repo.ID).Find(&existingFiles).Error; err != nil {
		return nil, err
	}
	if len(existingFiles) == 0 {
		return &PipelineRunResult{
			RepoID:   repo.ID,
			RepoName: repo.Name,
			Branch:   repo.ResolvedBranch,
		}, nil
	}

	verIDs := make([]uint64, 0, len(existingFiles))
	for _, ef := range existingFiles {
		if ef.LatestVersionID != nil && *ef.LatestVersionID > 0 {
			verIDs = append(verIDs, *ef.LatestVersionID)
		}
	}
	existingVerByFile := make(map[uint64]models.ProtoFileVersion, len(verIDs))
	if len(verIDs) > 0 {
		var vers []models.ProtoFileVersion
		if err := db.WithContext(ctx).Where("id IN ?", verIDs).Find(&vers).Error; err != nil {
			return nil, err
		}
		for _, v := range vers {
			existingVerByFile[v.ProtoFileID] = v
		}
	}

	existingByRepoPath := make(map[string]models.ProtoFile, len(existingFiles))
	rawFilesMap := make(map[string][]byte, len(existingFiles))
	rawBlobSHAMap := make(map[string]string, len(existingFiles))
	collectedFiles := make([]CollectedFile, 0, len(existingFiles))
	for _, ef := range existingFiles {
		rp := ef.RepoFilePath
		if rp == "" {
			rp = ef.FilePath
		}
		existingByRepoPath[rp] = ef
		ev := existingVerByFile[ef.ID]
		rawText := ev.RawContentText
		if rawText == "" {
			rawText = ev.ContentText
		}
		rawBytes := []byte(rawText)
		rawFilesMap[rp] = rawBytes
		rawBlobSHAMap[rp] = ef.RawBlobSHA
		pgDir, _ := ExtractStrictPGDir(rp)
		status := models.ProtoPipelineCollected
		reason := ""
		if isGitLFSPointer(rawBytes) {
			status = models.ProtoPipelineQuarantined
			reason = "LFS_POINTER"
		}
		collectedFiles = append(collectedFiles, CollectedFile{
			RepoFilePath: rp,
			PGDir:        pgDir,
			RawBlobSHA:   ef.RawBlobSHA,
			Content:      rawBytes,
			SizeBytes:    int64(len(rawBytes)),
			Status:       status,
			StatusReason: reason,
		})
	}

	commitSHA := repo.LastSyncCommit
	if commitSHA == "" {
		commitSHA = repo.LastCollectedCommit
	}
	branch := repo.ResolvedBranch
	if branch == "" {
		branch = repo.DefaultBranch
	}
	collectRes := &CollectResult{
		RepoID:                repo.ID,
		EffectiveSyncStrategy: repo.SyncStrategy,
		ResolvedBranch:        branch,
		CommitMeta:            &gitsync.CommitMeta{CommitSHA: commitSHA, CommittedAt: time.Now().UTC()},
		Files:                 collectedFiles,
	}

	var govDoc *GovernanceConfigDoc
	if snap, gErr := LoadActiveGovernanceConfig(ctx, db); gErr == nil && snap != nil {
		d := snap.Doc
		govDoc = &d
	}
	norm := opts.Normalizer
	if norm == nil {
		if len(opts.AuxConfigJSON) == 0 {
			opts.AuxConfigJSON = BuildEffectiveRepoAuxJSON(repo, govDoc)
		}
		norm = NewRuleBasedNormalizer(govDoc)
	}
	normVer := EffectiveNormalizerVersion(norm, opts.AuxConfigJSON)
	protosDir := opts.ProtosDir
	if strings.TrimSpace(protosDir) == "" {
		protosDir = config.AppConfig.Storage.GetProtosDir()
	}
	opts.ForceRenormalize = true
	return executeStagesBToD(ctx, db, reg, broadcaster, &repo, platRepo, collectRes, rawFilesMap, rawBlobSHAMap, existingByRepoPath, existingVerByFile, norm, normVer, protosDir, opts)
}

func executeStagesBToD(
	ctx context.Context,
	db *gorm.DB,
	reg *decoder.Registry,
	broadcaster EventBroadcaster,
	repo *models.GitRepo,
	platRepo *models.Repository,
	collectRes *CollectResult,
	rawFilesMap map[string][]byte,
	rawBlobSHAMap map[string]string,
	existingByRepoPath map[string]models.ProtoFile,
	existingVerByFile map[uint64]models.ProtoFileVersion,
	norm Normalizer,
	normVer string,
	protosDir string,
	opts PipelineOptions,
) (*PipelineRunResult, error) {
	subName := ""
	if platRepo != nil {
		subName = platRepo.ServiceGroup
	} else if repo.SubsystemSlug != "" && repo.SubsystemSlug != UnassignedSubsystemSlug {
		subName = repo.SubsystemSlug
	}
	subSlug := NormalizeSlug(subName, nil, "sub:"+subName, true)
	repoSlug := NormalizeSlug(repo.Name, nil, fmt.Sprintf("repo:%d", repo.ID), false)
	treeMgr := NewProtoTreeManager(protosDir)

	commitSHA := ""
	var commitMeta gitsync.CommitMeta
	if collectRes.CommitMeta != nil {
		commitMeta = *collectRes.CommitMeta
		commitSHA = commitMeta.CommitSHA
	}
	if commitMeta.CommittedAt.IsZero() {
		commitMeta.CommittedAt = time.Now().UTC()
	}

	// Stage raw version dir in .tmp (不立即切换软链，等待 DB 事务提交成功)
	stagedRawDir, rawVerName, err := treeMgr.StageVersionDir(TreeKindRaw, repo.ID, subSlug, repoSlug, ProtoMetaManifest{
		Branch:    collectRes.ResolvedBranch,
		CommitSHA: commitSHA,
		Files:     rawBlobSHAMap,
	}, rawFilesMap, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if stagedRawDir != "" {
			_ = osRemoveAllSafe(stagedRawDir)
		}
	}()

	// Stage B: 清理整顿 + 增量键 (raw_blob_sha, normalizer_version) + 后置仅解析语法门禁
	var activeGovDoc *GovernanceConfigDoc
	if rn, ok := norm.(*RuleBasedNormalizer); ok && rn != nil && rn.Config != nil {
		activeGovDoc = rn.Config
	} else if snap, gErr := LoadActiveGovernanceConfig(ctx, db); gErr == nil && snap != nil {
		d := snap.Doc
		activeGovDoc = &d
	}
	effectiveRules := (&RuleBasedNormalizer{Config: activeGovDoc}).resolveRepoRules(*repo)
	globalStripPG := effectiveRules.stripPGSegment
	cleanStripPG := effectiveRules.stripPGSegment && !effectiveRules.disableStripPG

	filesForNorm := make(map[string][]byte)
	reusedNormFiles := make(map[string]models.ProtoFile)
	for _, cf := range collectRes.Files {
		if cf.Status == models.ProtoPipelineQuarantined {
			continue
		}
		if !opts.ForceRenormalize {
			if ef, ok := existingByRepoPath[cf.RepoFilePath]; ok &&
				ef.RawBlobSHA != "" && ef.RawBlobSHA == cf.RawBlobSHA &&
				ef.NormalizerVersion == normVer &&
				ef.PipelineStatus != models.ProtoPipelineCollected {
				reusedNormFiles[cf.RepoFilePath] = ef
				continue
			}
		}
		filesForNorm[cf.RepoFilePath] = cf.Content
	}

	var normOut NormalizeOutput
	if len(filesForNorm) > 0 {
		var nErr error
		normOut, nErr = norm.Normalize(ctx, NormalizeInput{
			Repo:          *repo,
			Files:         filesForNorm,
			AuxConfigJSON: opts.AuxConfigJSON,
		})
		if nErr != nil {
			return nil, fmt.Errorf("Stage B 清理整顿执行失败: %w", nErr)
		}
	}

	type processedFile struct {
		CollectedFile
		MappedRelPath    string
		IsUServiceSource bool
		CleanContent     []byte
		CleanSHA256      string
		LineCount        int
		LineMapJSON      datatypes.JSON
		PipelineStatus   string
		StatusReason     string
		PackageName      string
		Imports          []string
		Symbols          []ParsedProtoSymbol
		BundleID         uint64
		LogicalPath      string
		Diagnostics      []NormalizeDiagnostic
		LinePreserving   bool
	}

	rawPathsForMap := make([]string, 0, len(collectRes.Files))
	for _, cf := range collectRes.Files {
		rawPathsForMap = append(rawPathsForMap, cf.RepoFilePath)
	}
	mappedPaths, pathCols, _ := MapAndValidateIntraRepoPathsWithPG(rawPathsForMap, repo.StripPrefixes(), repo.PathRewrites(), cleanStripPG)
	collidingSourcePaths := make(map[string]string)
	for _, col := range pathCols {
		for _, src := range col.SourcePaths {
			collidingSourcePaths[src] = col.MappedPath
		}
	}

	processed := make(map[string]*processedFile, len(collectRes.Files))
	allRepoImports := make([]string, 0)

	for _, cf := range collectRes.Files {
		mp := mappedPaths[cf.RepoFilePath]
		if mp == "" {
			mp = cf.RepoFilePath
		}
		pf := &processedFile{
			CollectedFile:  cf,
			MappedRelPath:  mp,
			PipelineStatus: cf.Status,
			StatusReason:   cf.StatusReason,
			LinePreserving: true,
		}
		if cf.Status == models.ProtoPipelineQuarantined {
			sum := sha256.Sum256(cf.Content)
			pf.CleanContent = cf.Content
			pf.CleanSHA256 = hex.EncodeToString(sum[:])
			pf.LineCount = countNormalizedLines(string(cf.Content))
			processed[cf.RepoFilePath] = pf
			continue
		}

		if reused, ok := reusedNormFiles[cf.RepoFilePath]; ok {
			ev := existingVerByFile[reused.ID]
			if reused.CleanRelPath != "" {
				pf.MappedRelPath = reused.CleanRelPath
			}
			pf.IsUServiceSource = reused.IsUServiceSource
			pf.CleanContent = []byte(ev.ContentText)
			pf.CleanSHA256 = reused.ContentSHA256
			pf.LineCount = reused.LineCount
			pf.LineMapJSON = reused.LineMapJSON
			pf.PipelineStatus = reused.PipelineStatus
			pf.StatusReason = reused.StatusReason
			pf.PackageName = reused.PackageName
			if len(reused.ImportsJSON) > 0 {
				_ = json.Unmarshal(reused.ImportsJSON, &pf.Imports)
			}
			if len(reused.SymbolsJSON) > 0 {
				_ = json.Unmarshal(reused.SymbolsJSON, &pf.Symbols)
			}
			allRepoImports = append(allRepoImports, pf.Imports...)
			processed[cf.RepoFilePath] = pf
			continue
		}

		nf, ok := normOut.Files[cf.RepoFilePath]
		if !ok {
			sum := sha256.Sum256(cf.Content)
			nf = NormalizedFileResult{
				RepoFilePath:   cf.RepoFilePath,
				CleanRelPath:   pf.MappedRelPath,
				CleanContent:   cf.Content,
				CleanSHA256:    hex.EncodeToString(sum[:]),
				LineCount:      countNormalizedLines(string(cf.Content)),
				LinePreserving: true,
			}
		}
		if nf.CleanRelPath != "" {
			pf.MappedRelPath = nf.CleanRelPath
		}
		pf.IsUServiceSource = nf.IsUServiceSource
		pf.CleanContent = nf.CleanContent
		pf.CleanSHA256 = nf.CleanSHA256
		pf.LineCount = nf.LineCount
		pf.LinePreserving = nf.LinePreserving
		pf.Diagnostics = append(pf.Diagnostics, nf.Diagnostics...)
		if !nf.LinePreserving && len(nf.LineMap) > 0 {
			if b, mErr := json.Marshal(nf.LineMap); mErr == nil {
				pf.LineMapJSON = datatypes.JSON(b)
			}
		}

		if colTarget, isCol := collidingSourcePaths[cf.RepoFilePath]; isCol {
			pf.PipelineStatus = models.ProtoPipelineQuarantined
			pf.StatusReason = fmt.Sprintf("INTRA_REPO_PATH_CONFLICT: 裁剪重写后路径 %s 存在多源文件碰撞", colTarget)
			processed[cf.RepoFilePath] = pf
			continue
		}

		var normErrMsg string
		for _, d := range nf.Diagnostics {
			if d.Severity == "ERROR" {
				normErrMsg = d.RuleID + ": " + d.Message
				break
			}
		}
		if normErrMsg != "" {
			pf.PipelineStatus = models.ProtoPipelineQuarantined
			pf.StatusReason = normErrMsg
		} else {
			// Stage B 后置语法门禁（仅解析不链接）
			gate := ParseProtoSyntaxOnly(cf.RepoFilePath, nf.CleanContent)
			if !gate.Passed {
				pf.PipelineStatus = models.ProtoPipelineQuarantined
				pf.StatusReason = "PARSE_FAILED: " + gate.ErrorSummary
				pf.Diagnostics = append(pf.Diagnostics, gate.Diagnostics...)
			} else {
				pf.PipelineStatus = models.ProtoPipelineNormalized
				pf.StatusReason = ""
				pf.PackageName = gate.PackageName
				pf.Imports = gate.Imports
				pf.Symbols = gate.Symbols
				allRepoImports = append(allRepoImports, gate.Imports...)
			}
		}
		processed[cf.RepoFilePath] = pf
	}

	// 加载或自动补齐 Bundles
	var bundles []models.GitProtoBundle
	if err := db.WithContext(ctx).Where("git_repo_id = ?", repo.ID).
		Order("CASE WHEN category = 'BASE_LIB' THEN 0 WHEN category = 'BUSINESS' THEN 1 ELSE 2 END, id ASC").
		Find(&bundles).Error; err != nil {
		return nil, err
	}
	if len(bundles) == 0 {
		// 若尚未配置 Bundle，自动按 pg 目录建 Bundle
		pgDirsSet := make(map[string]struct{})
		for _, cf := range collectRes.Files {
			if cf.PGDir != "" {
				pgDirsSet[cf.PGDir] = struct{}{}
			}
		}
		if len(pgDirsSet) == 0 {
			pgDirsSet["pg"] = struct{}{}
		}
		var pgList []string
		for d := range pgDirsSet {
			pgList = append(pgList, d)
		}
		sort.Strings(pgList)
		for _, cb := range buildCandidateBundles(repoSlug, pgList) {
			bm := models.GitProtoBundle{
				GitRepoID:        repo.ID,
				BundleName:       cb.BundleName,
				TargetSchemaName: cb.TargetSchemaName,
				Category:         cb.Category,
				SubdirPath:       cb.SubdirPath,
				IncludeDirsJSON:  datatypes.JSON([]byte(`["."]`)),
				FileGlobsJSON:    datatypes.JSON([]byte(`["**/*.proto"]`)),
				ExcludeGlobsJSON: datatypes.JSON([]byte(`[]`)),
				AutoCompile:      true,
				AutoActivate:     false,
				MRAutoPublish:    models.MRAutoPublishDisabled,
				LintOnSync:       true,
			}
			if cErr := db.WithContext(ctx).Create(&bm).Error; cErr == nil {
				bundles = append(bundles, bm)
			}
		}
	}

	// Stage C: 分配文件至 Bundle 并推断逻辑导入路径与 include_dirs
	bundleFiles := make(map[uint64][]*processedFile, len(bundles))
	for _, pf := range processed {
		var matchedBundle *models.GitProtoBundle
		bestSubdirLen := -1
		for i := range bundles {
			b := &bundles[i]
			sub, _ := SanitizeSubdirPath(b.SubdirPath)
			if sub == "." {
				if bestSubdirLen < 0 {
					matchedBundle = b
					bestSubdirLen = 0
				}
				continue
			}
			if strings.HasPrefix(pf.RepoFilePath, sub+"/") && len(sub) > bestSubdirLen {
				matchedBundle = b
				bestSubdirLen = len(sub)
			}
		}
		if matchedBundle == nil && len(bundles) > 0 {
			matchedBundle = &bundles[0]
		}
		if matchedBundle != nil {
			pf.BundleID = matchedBundle.ID
			bundleFiles[matchedBundle.ID] = append(bundleFiles[matchedBundle.ID], pf)
		}
	}

	inferredIncByBundle := make(map[uint64][]string, len(bundles))
	hasIntraMapping := len(repo.StripPrefixes()) > 0 || len(repo.PathRewrites()) > 0
	for _, b := range bundles {
		pfs := bundleFiles[b.ID]
		if len(pfs) == 0 {
			continue
		}
		subClean, _ := SanitizeSubdirPath(b.SubdirPath)
		useMappedForLogical := hasIntraMapping
		if useMappedForLogical {
			for _, inc := range b.IncludeDirs() {
				trimmedInc := strings.Trim(strings.TrimSpace(inc), "/")
				if trimmedInc != "" && trimmedInc != "." && trimmedInc != subClean && trimmedInc != "pg" {
					useMappedForLogical = false
					break
				}
			}
		}

		inFiles := make([]AnalyzedInputFile, 0, len(pfs))
		for _, pf := range pfs {
			candidatePath := pf.RepoFilePath
			pgDir := pf.PGDir
			if useMappedForLogical && pf.MappedRelPath != "" {
				candidatePath = pf.MappedRelPath
				pgDir = ""
			}
			inFiles = append(inFiles, AnalyzedInputFile{
				RepoFilePath:  candidatePath,
				PGDir:         pgDir,
				PackageName:   pf.PackageName,
				Imports:       pf.Imports,
				Symbols:       pf.Symbols,
				ContentSHA256: pf.CleanSHA256,
			})
		}
		logicalMap, incDirs := InferLogicalPathsAndIncludeDirs(b, inFiles, allRepoImports)
		inferredIncByBundle[b.ID] = incDirs
		for _, pf := range pfs {
			lookupKey := pf.RepoFilePath
			if useMappedForLogical && pf.MappedRelPath != "" {
				lookupKey = pf.MappedRelPath
			}
			if lp, ok := logicalMap[lookupKey]; ok && lp != "" {
				pf.LogicalPath = NormalizeLogicalPath(lp, globalStripPG)
			} else if useMappedForLogical && pf.MappedRelPath != "" {
				pf.LogicalPath = NormalizeLogicalPath(pf.MappedRelPath, globalStripPG)
			} else {
				pf.LogicalPath = NormalizeLogicalPath(pf.RepoFilePath, globalStripPG)
			}
		}
	}

	// 准备 clean 版本目录（写入 .tmp，待 DB 提交后按方案 A 目标目录原子切换）
	cleanSubSlug, cleanRepoSlug := ResolveEffectiveExportTarget(*repo, nil)
	cleanFilesMap := make(map[string][]byte)
	cleanHashes := make(map[string]string)
	var quarantinedPaths []string
	var reportEntries []NormalizeReportEntry
	var normalizedCnt, quarantinedCnt int

	sortedRepoPaths := make([]string, 0, len(processed))
	for rp := range processed {
		sortedRepoPaths = append(sortedRepoPaths, rp)
	}
	sort.Strings(sortedRepoPaths)

	for _, rp := range sortedRepoPaths {
		pf := processed[rp]
		cleanKey := pf.MappedRelPath
		if cleanKey == "" {
			cleanKey = rp
		}
		if pf.PipelineStatus == models.ProtoPipelineQuarantined {
			quarantinedCnt++
			quarantinedPaths = append(quarantinedPaths, rp)
		} else {
			normalizedCnt++
			cleanFilesMap[cleanKey] = pf.CleanContent
			cleanHashes[cleanKey] = pf.CleanSHA256
		}
		var lm map[int]int
		if len(pf.LineMapJSON) > 0 {
			_ = json.Unmarshal(pf.LineMapJSON, &lm)
		}
		reportEntries = append(reportEntries, NormalizeReportEntry{
			RepoFilePath:     rp,
			CleanRelPath:     cleanKey,
			LogicalPath:      pf.LogicalPath,
			PipelineStatus:   pf.PipelineStatus,
			StatusReason:     pf.StatusReason,
			IsUServiceSource: pf.IsUServiceSource,
			LinePreserving:   pf.LinePreserving,
			LineMap:          lm,
			Diagnostics:      pf.Diagnostics,
		})
	}

	stagedCleanDir, cleanVerName, err := treeMgr.StageVersionDir(TreeKindClean, repo.ID, cleanSubSlug, cleanRepoSlug, ProtoMetaManifest{
		Branch:            collectRes.ResolvedBranch,
		CommitSHA:         commitSHA,
		NormalizerVersion: normVer,
		Files:             cleanHashes,
		QuarantinedFiles:  quarantinedPaths,
	}, cleanFilesMap, &NormalizeReportManifest{
		GitRepoID:         repo.ID,
		CommitSHA:         commitSHA,
		NormalizerVersion: normVer,
		GeneratedAt:       time.Now().UTC(),
		Entries:           reportEntries,
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		if stagedCleanDir != "" {
			_ = osRemoveAllSafe(stagedCleanDir)
		}
	}()

	// DB 事务：写入 ProtoFile / ProtoFileVersion、软删缺失文件、同步 ImportOwners
	bundleByID := make(map[uint64]models.GitProtoBundle, len(bundles))
	for _, b := range bundles {
		bundleByID[b.ID] = b
	}
	changedLogicalSet := make(map[string]struct{})
	touchedVerByBundle := make(map[uint64][]uint64)
	bundleStats := make(map[uint64]*SyncBundleResult, len(bundles))
	for _, b := range bundles {
		bundleStats[b.ID] = &SyncBundleResult{
			BundleID:         b.ID,
			BundleName:       b.BundleName,
			TargetSchemaName: b.TargetSchemaName,
			Category:         b.Category,
			Branch:           collectRes.ResolvedBranch,
			CommitSHA:        commitSHA,
		}
	}

	txErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if lockErr := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", fmt.Sprintf("contract_git_repo_pipeline_%d", repo.ID)).Error; lockErr != nil {
			return lockErr
		}

		var curFiles []models.ProtoFile
		if err := tx.Where("git_repo_id = ? AND is_deleted = false", repo.ID).Find(&curFiles).Error; err != nil {
			return err
		}
		curByRepoPath := make(map[string]*models.ProtoFile, len(curFiles))
		for i := range curFiles {
			key := curFiles[i].RepoFilePath
			if key == "" {
				key = curFiles[i].FilePath
			}
			curByRepoPath[key] = &curFiles[i]
		}

		now := time.Now().UTC()
		versionTag := shortCommitTag(commitSHA)

		for _, rp := range sortedRepoPaths {
			pf := processed[rp]
			b, hasB := bundleByID[pf.BundleID]
			if !hasB {
				continue
			}
			st := bundleStats[b.ID]
			if st != nil {
				st.TotalRemoteFiles++
			}

			importsJSONBytes, _ := json.Marshal(nonNilStringsSlice(pf.Imports))
			symbolsJSONBytes, _ := json.Marshal(pf.Symbols)

			cur := curByRepoPath[rp]
			if cur == nil {
				newPF := models.ProtoFile{
					GitRepoID:         repo.ID,
					BundleID:          b.ID,
					TargetSchemaName:  b.TargetSchemaName,
					Category:          b.Category,
					FilePath:          pf.LogicalPath,
					RepoFilePath:      rp,
					CleanRelPath:      pf.MappedRelPath,
					PackageName:       pf.PackageName,
					RawBlobSHA:        pf.RawBlobSHA,
					NormalizerVersion: normVer,
					PipelineStatus:    pf.PipelineStatus,
					StatusReason:      pf.StatusReason,
					LineMapJSON:       pf.LineMapJSON,
					IsExternalCopy:    false,
					IsUServiceSource:  pf.IsUServiceSource,
					ImportsJSON:       datatypes.JSON(importsJSONBytes),
					SymbolsJSON:       datatypes.JSON(symbolsJSONBytes),
					ContentSHA256:     pf.CleanSHA256,
					LineCount:         pf.LineCount,
					LatestVersionNum:  1,
					LatestCommitSHA:   commitSHA,
					IsDeleted:         false,
				}
				if err := tx.Create(&newPF).Error; err != nil {
					return err
				}
				ver := models.ProtoFileVersion{
					ProtoFileID:    newPF.ID,
					Version:        1,
					VersionTag:     versionTag,
					SourceType:     models.ProtoSourceGitSync,
					CommitSHA:      commitSHA,
					CommitMessage:  commitMeta.CommitMessage,
					AuthorName:     commitMeta.AuthorName,
					AuthorEmail:    commitMeta.AuthorEmail,
					RawContentText: string(pf.Content),
					ContentText:    string(pf.CleanContent),
					ContentSHA256:  pf.CleanSHA256,
					LineCount:      pf.LineCount,
					CommittedAt:    commitMeta.CommittedAt,
				}
				if err := tx.Create(&ver).Error; err != nil {
					return err
				}
				if err := tx.Model(&newPF).Update("latest_version_id", ver.ID).Error; err != nil {
					return err
				}
				touchedVerByBundle[b.ID] = append(touchedVerByBundle[b.ID], ver.ID)
				changedLogicalSet[pf.LogicalPath] = struct{}{}
				if st != nil {
					st.AddedFiles++
				}
				continue
			}

			contentChanged := cur.ContentSHA256 != pf.CleanSHA256 || cur.RawBlobSHA != pf.RawBlobSHA || cur.NormalizerVersion != normVer
			if !contentChanged {
				updates := map[string]any{
					"file_path":          pf.LogicalPath,
					"clean_rel_path":     pf.MappedRelPath,
					"is_uservice_source": pf.IsUServiceSource,
					"package_name":       pf.PackageName,
					"pipeline_status":    pf.PipelineStatus,
					"status_reason":      pf.StatusReason,
					"line_map_json":      pf.LineMapJSON,
					"imports_json":       datatypes.JSON(importsJSONBytes),
					"symbols_json":       datatypes.JSON(symbolsJSONBytes),
				}
				if err := tx.Model(cur).Updates(updates).Error; err != nil {
					return err
				}
				if st != nil {
					st.UnchangedFiles++
				}
				continue
			}

			var maxVer int
			if err := tx.Model(&models.ProtoFileVersion{}).
				Where("proto_file_id = ?", cur.ID).
				Select("COALESCE(MAX(version), 0)").
				Scan(&maxVer).Error; err != nil {
				return err
			}
			nextVer := maxVer + 1
			ver := models.ProtoFileVersion{
				ProtoFileID:    cur.ID,
				Version:        nextVer,
				VersionTag:     versionTag,
				SourceType:     models.ProtoSourceGitSync,
				CommitSHA:      commitSHA,
				CommitMessage:  commitMeta.CommitMessage,
				AuthorName:     commitMeta.AuthorName,
				AuthorEmail:    commitMeta.AuthorEmail,
				RawContentText: string(pf.Content),
				ContentText:    string(pf.CleanContent),
				ContentSHA256:  pf.CleanSHA256,
				LineCount:      pf.LineCount,
				CommittedAt:    commitMeta.CommittedAt,
			}
			if err := tx.Create(&ver).Error; err != nil {
				return err
			}
			if err := tx.Model(cur).Updates(map[string]any{
				"bundle_id":          b.ID,
				"target_schema_name": b.TargetSchemaName,
				"category":           b.Category,
				"file_path":          pf.LogicalPath,
				"repo_file_path":     rp,
				"clean_rel_path":     pf.MappedRelPath,
				"is_uservice_source": pf.IsUServiceSource,
				"package_name":       pf.PackageName,
				"raw_blob_sha":       pf.RawBlobSHA,
				"normalizer_version": normVer,
				"pipeline_status":    pf.PipelineStatus,
				"status_reason":      pf.StatusReason,
				"line_map_json":      pf.LineMapJSON,
				"imports_json":       datatypes.JSON(importsJSONBytes),
				"symbols_json":       datatypes.JSON(symbolsJSONBytes),
				"content_sha256":     pf.CleanSHA256,
				"line_count":         pf.LineCount,
				"latest_version_id":  ver.ID,
				"latest_version_num": nextVer,
				"latest_commit_sha":  commitSHA,
			}).Error; err != nil {
				return err
			}
			touchedVerByBundle[b.ID] = append(touchedVerByBundle[b.ID], ver.ID)
			changedLogicalSet[pf.LogicalPath] = struct{}{}
			if st != nil {
				st.ModifiedFiles++
			}
		}

		// 软删已移除或移出白名单的文件
		for rp, cur := range curByRepoPath {
			if _, ok := processed[rp]; !ok {
				if err := tx.Model(cur).Update("is_deleted", true).Error; err != nil {
					return err
				}
				changedLogicalSet[cur.FilePath] = struct{}{}
				if st := bundleStats[cur.BundleID]; st != nil {
					st.DeletedFiles++
				}
			}
		}

		// 更新各 Bundle 推断出的 include_dirs_json（仅当既有配置为默认 ["."] 时更新）与同步时间
		for _, b := range bundles {
			bUpdates := map[string]any{
				"last_sync_commit": commitSHA,
				"last_sync_at":     &now,
			}
			curIncs := b.IncludeDirs()
			if len(curIncs) == 1 && curIncs[0] == "." {
				if inf := inferredIncByBundle[b.ID]; len(inf) > 0 {
					if infJSON, jErr := json.Marshal(inf); jErr == nil {
						bUpdates["include_dirs_json"] = datatypes.JSON(infJSON)
					}
				}
			}
			if err := tx.Model(&models.GitProtoBundle{}).Where("id = ?", b.ID).Updates(bUpdates).Error; err != nil {
				return err
			}
		}

		if err := SyncImportOwnersForRepo(ctx, tx, repo.ID); err != nil {
			return err
		}

		repoUpdates := map[string]any{
			"proto_status":          models.GitRepoProtoStatusHasProto,
			"subsystem_slug":        subSlug,
			"resolved_branch":       collectRes.ResolvedBranch,
			"last_collected_commit": commitSHA,
			"last_sync_commit":      commitSHA,
			"last_sync_at":          &now,
			"last_sync_status":      models.GitSyncStatusSuccess,
			"last_sync_error":       "",
		}
		if collectRes.EffectiveSyncStrategy != models.GitSyncStrategyLocalCacheOnly {
			repoUpdates["last_fetched_at"] = &now
		}
		if !commitMeta.CommittedAt.IsZero() {
			ct := commitMeta.CommittedAt
			repoUpdates["ref_commit_time"] = &ct
		}
		return tx.Model(repo).Updates(repoUpdates).Error
	})
	if txErr != nil {
		return nil, txErr
	}

	// DB 事务提交成功后，原子切换 raw 与 clean 目录符号链接 (ADR-26 §2.5.1)
	if _, err := treeMgr.CommitAndSwitchVersion(TreeKindRaw, repo.ID, subSlug, repoSlug, stagedRawDir, rawVerName); err != nil {
		log.Printf("[ProtoTree] 原子切换 raw 链接警告 (repo#%d): %v", repo.ID, err)
	} else {
		stagedRawDir = ""
	}
	if !repo.SyncEnabled {
		treeMgr.RemoveRepoTree(TreeKindClean, repo.ID)
	} else if _, err := treeMgr.CommitAndSwitchVersion(TreeKindClean, repo.ID, cleanSubSlug, cleanRepoSlug, stagedCleanDir, cleanVerName); err != nil {
		log.Printf("[ProtoTree] 原子切换 clean 链接警告 (repo#%d): %v", repo.ID, err)
	} else {
		stagedCleanDir = ""
	}

	// Stage C 后置：跨仓符号冲突预检与同哈希外部副本自动标记
	_, _ = DetectConflictsAndMarkExternalCopies(ctx, db)

	// 读取最新 DB 状态（含外部副本标记），进入 Stage D 按 Bundle 分仓编译
	var refreshedFiles []models.ProtoFile
	_ = db.WithContext(ctx).Where("git_repo_id = ? AND is_deleted = false", repo.ID).Find(&refreshedFiles).Error
	extCopyCount := 0
	refreshedByBundle := make(map[uint64][]models.ProtoFile)
	for _, rf := range refreshedFiles {
		if rf.IsExternalCopy {
			extCopyCount++
		}
		refreshedByBundle[rf.BundleID] = append(refreshedByBundle[rf.BundleID], rf)
	}

	// ADR-29 §2.3.1 & §2.3.2:
	// 1. sync-all (opts.SkipCompile=true) 跳过单仓盲编，统一交由阶段 ④ 拓扑编译发布；
	// 2. 单仓同步 / Webhook 编译仅作为预览，强制 AutoActivate = false，绝不直接激活进入生效池。
	bundleResults := make([]SyncBundleResult, 0, len(bundles))
	for _, b := range bundles {
		st := bundleStats[b.ID]
		if st == nil {
			continue
		}
		bundleFilesList := refreshedByBundle[b.ID]
		compileFiles := make(map[string]string)
		var lintSources []linter.SourceInput

		for _, rf := range bundleFilesList {
			if rf.PipelineStatus != models.ProtoPipelineNormalized || rf.IsExternalCopy {
				continue
			}
			pf := processed[rf.RepoFilePath]
			contentStr := ""
			if pf != nil {
				contentStr = string(pf.CleanContent)
			} else if ev, ok := existingVerByFile[rf.ID]; ok {
				contentStr = ev.ContentText
			}
			compileFiles[rf.FilePath] = contentStr
			lintSources = append(lintSources, linter.SourceInput{
				ProtoFile: rf.FilePath,
				Content:   contentStr,
				Category:  b.Category,
			})
		}

		shouldCompile := !opts.SkipCompile && b.AutoCompile && len(compileFiles) > 0 &&
			(opts.ForceRecompile || opts.ForceRenormalize || b.LastSchemaID == nil || st.AddedFiles > 0 || st.ModifiedFiles > 0 || st.DeletedFiles > 0)

		if !shouldCompile || reg == nil {
			st.CompileSkipped = true
			st.CompiledSchemaID = b.LastSchemaID
		} else {
			repoIDCopy := repo.ID
			bundleIDCopy := b.ID
			schema, _, _, compErr := reg.CompileAndSaveSchema(ctx, decoder.CompileProtoBundleInput{
				Name:             b.TargetSchemaName,
				Category:         b.Category,
				VersionTag:       shortCommitTag(commitSHA),
				Description:      strings.TrimSpace(commitMeta.CommitMessage),
				Files:            compileFiles,
				ProtoIncludeDirs: []string{"."},
				EntryPoints:      b.EntryFiles(),
				AutoActivate:     false,
				SourceType:       models.ProtoSourceGitSync,
				GitRepoID:        &repoIDCopy,
				SourceBundleID:   &bundleIDCopy,
				SourceCommitSHA:  commitSHA,
				OperatorID:       opts.OperatorID,
				SourceProvider:   decoder.NewDBProtoSourceProvider(db),
			})
			if compErr != nil {
				st.CompileError = compErr.Error()
			} else if schema != nil {
				scID := schema.ID
				st.CompiledSchemaID = &scID
				if vers := touchedVerByBundle[b.ID]; len(vers) > 0 {
					_ = db.Model(&models.ProtoFileVersion{}).Where("id IN ?", vers).Update("schema_id", scID).Error
				}
				_ = db.Model(&models.GitProtoBundle{}).Where("id = ?", b.ID).Update("last_schema_id", scID).Error
				if schema.IsActive {
					_, _ = protodoc.RebuildForSchema(db, schema)
				}
			}
		}

		if b.LintOnSync && len(lintSources) > 0 {
			var scPtr *uint64
			if st.CompiledSchemaID != nil {
				scPtr = st.CompiledSchemaID
			} else if b.LastSchemaID != nil {
				scPtr = b.LastSchemaID
			}
			repoIDCopy := repo.ID
			bundleIDCopy := b.ID
			if report, lintErr := linter.RunAndPersistScan(ctx, linter.ScanRequest{
				DB:                db,
				TriggerSource:     models.CheckTriggerGitSync,
				TriggeredBy:       opts.OperatorID,
				GitRepoID:         &repoIDCopy,
				BundleID:          &bundleIDCopy,
				SchemaID:          scPtr,
				Sources:           lintSources,
				IncludeDuplicates: false,
				IncludeLLM:        false,
			}); lintErr == nil && report != nil {
				rID := report.ID
				st.LintReportID = &rID
			}
		}

		if broadcaster != nil {
			bIDCopy := b.ID
			evtStatus := models.GitSyncStatusSuccess
			msg := fmt.Sprintf("Bundle [%s] 四段式同步完成 (+%d ~%d -%d)", b.BundleName, st.AddedFiles, st.ModifiedFiles, st.DeletedFiles)
			if st.CompileError != "" {
				evtStatus = models.GitSyncStatusFailed
				msg = st.CompileError
			}
			broadcaster(GitSyncEventPayload{
				RepoID:           repo.ID,
				RepoName:         repo.Name,
				BundleID:         &bIDCopy,
				BundleName:       b.BundleName,
				TargetSchemaName: b.TargetSchemaName,
				Branch:           collectRes.ResolvedBranch,
				CommitSHA:        commitSHA,
				AddedFiles:       st.AddedFiles,
				ModifiedFiles:    st.ModifiedFiles,
				DeletedFiles:     st.DeletedFiles,
				CompiledSchemaID: st.CompiledSchemaID,
				LintReportID:     st.LintReportID,
				Status:           evtStatus,
				Message:          msg,
			})
		}

		bundleResults = append(bundleResults, *st)
	}

	out := &PipelineRunResult{
		RepoID:            repo.ID,
		RepoName:          repo.Name,
		Branch:            collectRes.ResolvedBranch,
		CommitSHA:         commitSHA,
		ProtoStatus:       models.GitRepoProtoStatusHasProto,
		NormalizerVersion: normVer,
		CollectedCount:    len(collectRes.Files),
		NormalizedCount:   normalizedCnt,
		QuarantinedCount:  quarantinedCnt,
		ExternalCopyCount: extCopyCount,
		OutsideWhitelist:  collectRes.OutsideWhitelistCount,
		BundleResults:     bundleResults,
	}

	// Stage D 联动：单仓变更后计算影响面并对下游受影响 Bundle 局部重编 + F-26 破坏性检查
	if !opts.SkipCompile && opts.TriggerBlastRadius && len(changedLogicalSet) > 0 && reg != nil {
		changedList := make([]string, 0, len(changedLogicalSet))
		for lp := range changedLogicalSet {
			changedList = append(changedList, lp)
		}
		selfBundleIDs := make([]uint64, 0, len(bundles))
		for _, b := range bundles {
			selfBundleIDs = append(selfBundleIDs, b.ID)
		}
		if blastIDs, bErr := ComputeBlastRadius(ctx, db, changedList, selfBundleIDs...); bErr == nil && len(blastIDs) > 0 {
			out.BlastRadiusBundleIDs = blastIDs
			out.BreakingAlertCount = recompileBlastRadiusBundles(ctx, db, reg, blastIDs, opts.OperatorID)
		}
	}

	return out, nil
}

func recompileBlastRadiusBundles(ctx context.Context, db *gorm.DB, reg *decoder.Registry, bundleIDs []uint64, operatorID uint) int {
	breakingAlerts := 0
	for _, bID := range bundleIDs {
		var b models.GitProtoBundle
		if err := db.WithContext(ctx).First(&b, bID).Error; err != nil || !b.AutoCompile {
			continue
		}
		type fileVerRow struct {
			FilePath    string
			ContentText string
		}
		var rows []fileVerRow
		if err := db.WithContext(ctx).
			Table("contract_proto_files AS f").
			Select("f.file_path AS file_path, v.content_text AS content_text").
			Joins("JOIN contract_proto_file_versions AS v ON v.id = f.latest_version_id").
			Where("f.bundle_id = ? AND f.is_deleted = false AND f.pipeline_status = ? AND f.is_external_copy = false",
				b.ID, models.ProtoPipelineNormalized).
			Find(&rows).Error; err != nil || len(rows) == 0 {
			continue
		}
		filesMap := make(map[string]string, len(rows))
		for _, r := range rows {
			filesMap[r.FilePath] = r.ContentText
		}
		repoIDCopy := b.GitRepoID
		bundleIDCopy := b.ID
		schema, _, _, compErr := reg.CompileAndSaveSchema(ctx, decoder.CompileProtoBundleInput{
			Name:             b.TargetSchemaName,
			Category:         b.Category,
			VersionTag:       "blast-radius",
			Description:      "上游变更触发影响面增量重编",
			Files:            filesMap,
			ProtoIncludeDirs: []string{"."},
			EntryPoints:      b.EntryFiles(),
			AutoActivate:     false,
			SourceType:       models.ProtoSourceGitSync,
			GitRepoID:        &repoIDCopy,
			SourceBundleID:   &bundleIDCopy,
			SourceCommitSHA:  b.LastSyncCommit,
			OperatorID:       operatorID,
			SourceProvider:   decoder.NewDBProtoSourceProvider(db),
		})
		if compErr != nil {
			var bErr *decoder.SchemaBreakingError
			if errors.As(compErr, &bErr) {
				breakingAlerts += len(bErr.BreakingRules)
			}
			continue
		}
		if schema != nil {
			_ = db.Model(&b).Update("last_schema_id", schema.ID).Error
			_, _ = protodoc.RebuildForSchema(db, schema)
		}
	}
	return breakingAlerts
}

func nonNilStringsSlice(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func osRemoveAllSafe(dir string) error {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" || trimmed == "/" || !strings.Contains(filepath.Clean(trimmed), string(filepath.Separator)+".tmp") {
		return nil
	}
	return os.RemoveAll(trimmed)
}
