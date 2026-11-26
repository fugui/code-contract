package gitclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"code-common/backend/gitsync"
	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/models"
)

var (
	// ErrMaxFilesPerRepoExceeded 表示单仓命中的白名单 .proto 文件数超过安全上限。
	ErrMaxFilesPerRepoExceeded = errors.New("单仓采集 .proto 文件数量超过 max_files_per_repo 上限")
)

const lfsPointerPrefix = "version https://git-lfs.github.com/spec/v1"

// CollectConfig 控制 Stage A pg 目录白名单采集参数。
type CollectConfig struct {
	CodesDir           string
	SharedWriteEnabled bool
	CollectGlobs       []string
	ExcludeGlobs       []string
	MaxFileBytes       int64
	MaxFilesPerRepo    int
	KnownRawBlobSHAs   map[string]string // repo_file_path -> raw_blob_sha
}

// CollectedFile 表示 Stage A 采集出的单个原始 .proto 文件。
type CollectedFile struct {
	RepoFilePath      string `json:"repo_file_path"`
	PGDir             string `json:"pg_dir"`
	RawBlobSHA        string `json:"raw_blob_sha"`
	Content           []byte `json:"-"`
	SizeBytes         int64  `json:"size_bytes"`
	IncrementalReused bool   `json:"incremental_reused"`
	Status            string `json:"status"` // COLLECTED | QUARANTINED
	StatusReason      string `json:"status_reason,omitempty"`
}

// CollectResult 表示单仓 Stage A 采集结果。
type CollectResult struct {
	RepoID                uint64              `json:"repo_id"`
	LocalPath             string              `json:"local_path"`
	EffectiveSyncStrategy string              `json:"effective_sync_strategy"`
	ResolvedBranch        string              `json:"resolved_branch"`
	FullRef               string              `json:"full_ref"`
	CommitMeta            *gitsync.CommitMeta `json:"commit_meta,omitempty"`
	Files                 []CollectedFile     `json:"files"`
	OutsideWhitelistCount int                 `json:"outside_whitelist_count"`
	SkippedSymlinks       []string            `json:"skipped_symlinks,omitempty"`
	SkippedSubmodules     []string            `json:"skipped_submodules,omitempty"`
}

// ResolveEffectiveRepoLocalPath 解析仓库在宿主机共享缓存或隔离目录中的物理路径，以及实际采集策略。
func ResolveEffectiveRepoLocalPath(repo *models.GitRepo, codesDir string, sharedWriteEnabled bool) (localPath string, strategy string, err error) {
	if repo == nil {
		return "", "", errors.New("GitRepo 不能为空")
	}
	if strings.TrimSpace(codesDir) == "" {
		codesDir = config.AppConfig.Storage.GetCodesDir()
	}

	sourceMode := repo.SourceMode
	if sourceMode == "" {
		sourceMode = models.GitSourceModeExternalStandalone
	}
	strategy = repo.SyncStrategy
	if strategy == "" {
		strategy = models.GitSyncStrategyFetchAndRead
	}

	if sourceMode == models.GitSourceModePlatformShared {
		p, pErr := gitsync.ResolveRepoLocalPath(codesDir, repo.RepoURL)
		if pErr != nil {
			return "", "", pErr
		}
		if !sharedWriteEnabled {
			strategy = models.GitSyncStrategyLocalCacheOnly
		}
		return p, strategy, nil
	}

	// EXTERNAL_STANDALONE 统一收敛至 <codes_dir>/_external/<git_repo_id>/
	absCodes, err := filepath.Abs(filepath.Clean(codesDir))
	if err != nil {
		return "", "", err
	}
	localPath = filepath.Join(absCodes, "_external", strconv.FormatUint(repo.ID, 10))
	return localPath, models.GitSyncStrategyFetchAndRead, nil
}

// CollectRepoStageA 执行 Stage A 字节级白名单采集（两层锁、私有 ref、仅增量读取变更 blob）。
func CollectRepoStageA(ctx context.Context, repo *models.GitRepo, branch string, cfg CollectConfig) (*CollectResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if repo == nil {
		return nil, errors.New("GitRepo 不能为空")
	}
	if strings.TrimSpace(cfg.CodesDir) == "" {
		cfg.CodesDir = config.AppConfig.Storage.GetCodesDir()
	}
	if len(cfg.CollectGlobs) == 0 {
		if repoGlobs := repo.CollectGlobs(); len(repoGlobs) > 0 {
			cfg.CollectGlobs = repoGlobs
		} else {
			cfg.CollectGlobs = config.DefaultCollectGlobs()
		}
	}
	if len(cfg.ExcludeGlobs) == 0 {
		if repoEx := repo.ExcludeGlobs(); len(repoEx) > 0 {
			cfg.ExcludeGlobs = repoEx
		} else {
			cfg.ExcludeGlobs = config.DefaultExcludeGlobs()
		}
	}
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = 2 * 1024 * 1024
	}
	if cfg.MaxFilesPerRepo <= 0 {
		cfg.MaxFilesPerRepo = 5000
	}

	localPath, strategy, err := ResolveEffectiveRepoLocalPath(repo, cfg.CodesDir, cfg.SharedWriteEnabled)
	if err != nil {
		return nil, err
	}

	targetBranch := strings.TrimSpace(branch)
	if targetBranch == "" {
		targetBranch = strings.TrimSpace(repo.DefaultBranch)
	}
	targetBranch = gitsync.ResolveBranchDefault(targetBranch)

	var (
		resolvedBranch string
		fullRef        string
	)
	if strategy == models.GitSyncStrategyLocalCacheOnly {
		// 仅读取 code-shield 维护的 refs/remotes/origin/<branch>，瞬时失败自动重试
		var resolveErr error
		for attempt := 0; attempt < 2; attempt++ {
			resolvedBranch, fullRef, resolveErr = gitsync.ResolveLocalRef(ctx, localPath, "refs/remotes/origin", targetBranch)
			if resolveErr == nil {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
		if resolveErr != nil {
			return nil, resolveErr
		}
	} else {
		extraEnv, cleanupEnv, envErr := buildExternalGitAuthEnv(repo)
		if envErr != nil {
			return nil, envErr
		}
		defer cleanupEnv()

		resolvedBranch, fullRef, err = gitsync.FetchBranchToRef(
			ctx, cfg.CodesDir, localPath, repo.RepoURL, targetBranch, "refs/contract/heads", extraEnv...,
		)
		if err != nil {
			return nil, err
		}
	}

	// LOCK_SH 持锁范围严格限于 Git 对象只读操作（ls-tree / cat-file / log），读完立即释放
	shLock, err := gitsync.AcquireRepoLock(ctx, cfg.CodesDir, localPath, gitsync.LockShared)
	if err != nil {
		return nil, err
	}
	released := false
	releaseOnce := func() {
		if !released {
			released = true
			_ = shLock.Release()
		}
	}
	defer releaseOnce()

	meta, err := gitsync.ReadCommitMeta(ctx, localPath, fullRef)
	if err != nil {
		return nil, err
	}

	entries, err := listWhitelistCandidateEntries(ctx, localPath, fullRef, cfg.CollectGlobs)
	if err != nil {
		return nil, err
	}

	res := &CollectResult{
		RepoID:                repo.ID,
		LocalPath:             localPath,
		EffectiveSyncStrategy: strategy,
		ResolvedBranch:        resolvedBranch,
		FullRef:               fullRef,
		CommitMeta:            meta,
		Files:                 make([]CollectedFile, 0),
	}

	type pendingBlob struct {
		fileIdx int
		sha     string
	}
	var toFetch []pendingBlob
	var fetchSHAs []string

	for _, entry := range entries {
		if entry.Mode == "120000" {
			res.SkippedSymlinks = append(res.SkippedSymlinks, entry.Path)
			continue
		}
		if entry.Mode == "160000" {
			res.SkippedSubmodules = append(res.SkippedSubmodules, entry.Path)
			continue
		}
		if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") {
			continue
		}
		if !strings.HasSuffix(entry.Path, ".proto") {
			continue
		}
		cleanedPath, sErr := decoder.SanitizeProtoRelPath(entry.Path, true)
		if sErr != nil {
			return nil, sErr
		}
		if !matchCollectGlobsStrict(cfg.CollectGlobs, cleanedPath) {
			res.OutsideWhitelistCount++
			continue
		}
		if matchAnyDoublestar(cfg.ExcludeGlobs, cleanedPath) {
			continue
		}

		pgDir, _ := ExtractStrictPGDir(cleanedPath)
		cf := CollectedFile{
			RepoFilePath: cleanedPath,
			PGDir:        pgDir,
			RawBlobSHA:   entry.SHA,
			Status:       models.ProtoPipelineCollected,
		}
		if knownSHA, ok := cfg.KnownRawBlobSHAs[cleanedPath]; ok && knownSHA != "" && knownSHA == entry.SHA {
			cf.IncrementalReused = true
			res.Files = append(res.Files, cf)
			continue
		}
		idx := len(res.Files)
		res.Files = append(res.Files, cf)
		toFetch = append(toFetch, pendingBlob{fileIdx: idx, sha: entry.SHA})
		fetchSHAs = append(fetchSHAs, entry.SHA)
	}

	if len(res.Files) > cfg.MaxFilesPerRepo {
		return nil, fmt.Errorf("%w: 仓库 [%s] 命中 %d 个文件（上限 %d）",
			ErrMaxFilesPerRepoExceeded, repo.Name, len(res.Files), cfg.MaxFilesPerRepo)
	}

	if len(fetchSHAs) > 0 {
		blobs, blobErrs, bErr := gitsync.ReadBlobsBatch(ctx, localPath, fetchSHAs, cfg.MaxFileBytes)
		if bErr != nil {
			return nil, bErr
		}
		for _, pb := range toFetch {
			if errMsg, hasErr := blobErrs[pb.sha]; hasErr {
				res.Files[pb.fileIdx].Status = models.ProtoPipelineQuarantined
				res.Files[pb.fileIdx].StatusReason = errMsg.Error()
				continue
			}
			rawBytes := blobs[pb.sha]
			res.Files[pb.fileIdx].Content = rawBytes
			res.Files[pb.fileIdx].SizeBytes = int64(len(rawBytes))
			if isGitLFSPointer(rawBytes) {
				res.Files[pb.fileIdx].Status = models.ProtoPipelineQuarantined
				res.Files[pb.fileIdx].StatusReason = "LFS_POINTER"
			}
		}
	}

	// 立即释放 LOCK_SH，后续 Stage B/C/D 均在锁外执行
	releaseOnce()

	sort.Slice(res.Files, func(i, j int) bool {
		return res.Files[i].RepoFilePath < res.Files[j].RepoFilePath
	})
	return res, nil
}

// ProbeRepoPGStructure 在只读共享锁下快速探测本地缓存中的全小写 pg 目录与白名单统计（用于平台仓发现预览）。
func ProbeRepoPGStructure(ctx context.Context, codesDir, repoURL, branch string) (pgDirs []string, whitelistCount int, outsideCount int, resolvedBranch string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	localPath, err := gitsync.ResolveRepoLocalPath(codesDir, repoURL)
	if err != nil {
		return nil, 0, 0, "", err
	}
	if _, statErr := os.Stat(filepath.Join(localPath, ".git")); statErr != nil {
		return nil, 0, 0, "", fmt.Errorf("本地缓存尚无该仓库: %w", statErr)
	}

	lk, err := gitsync.AcquireRepoLock(ctx, codesDir, localPath, gitsync.LockShared)
	if err != nil {
		return nil, 0, 0, "", err
	}
	defer func() { _ = lk.Release() }()

	resolved, fullRef, err := gitsync.ResolveLocalRef(ctx, localPath, "refs/remotes/origin", gitsync.ResolveBranchDefault(branch))
	if err != nil {
		resolved, fullRef, err = gitsync.ResolveLocalRef(ctx, localPath, "refs/contract/heads", gitsync.ResolveBranchDefault(branch))
		if err != nil {
			return nil, 0, 0, "", err
		}
	}

	allEntries, err := gitsync.ListTreeEntries(ctx, localPath, fullRef, true, false)
	if err != nil {
		return nil, 0, 0, "", err
	}

	pgDirSet := make(map[string]struct{})
	defaultGlobs := config.DefaultCollectGlobs()
	defaultEx := config.DefaultExcludeGlobs()
	for _, e := range allEntries {
		if e.Type != "blob" || (e.Mode != "100644" && e.Mode != "100755") {
			continue
		}
		if !strings.HasSuffix(e.Path, ".proto") {
			continue
		}
		cleaned, sErr := decoder.SanitizeProtoRelPath(e.Path, true)
		if sErr != nil {
			continue
		}
		if matchCollectGlobsStrict(defaultGlobs, cleaned) && !matchAnyDoublestar(defaultEx, cleaned) {
			whitelistCount++
			if pgDir, ok := ExtractStrictPGDir(cleaned); ok {
				pgDirSet[pgDir] = struct{}{}
			}
		} else {
			outsideCount++
		}
	}
	pgDirs = make([]string, 0, len(pgDirSet))
	for d := range pgDirSet {
		pgDirs = append(pgDirs, d)
	}
	sort.Strings(pgDirs)
	return pgDirs, whitelistCount, outsideCount, resolved, nil
}

// listWhitelistCandidateEntries 遵循 ADR-26 §2.3.2 按前缀列举，无法推导前缀时退化为全量 ls-tree -z -r。
func listWhitelistCandidateEntries(ctx context.Context, localPath, fullRef string, collectGlobs []string) ([]gitsync.TreeEntry, error) {
	prefixes, canUsePrefixes, err := derivePGPrefixes(ctx, localPath, fullRef, collectGlobs)
	if err != nil {
		return nil, err
	}
	if !canUsePrefixes {
		return gitsync.ListTreeEntries(ctx, localPath, fullRef, true, false)
	}
	if len(prefixes) == 0 {
		return nil, nil
	}
	return gitsync.ListTreeEntries(ctx, localPath, fullRef, true, false, prefixes...)
}

func derivePGPrefixes(ctx context.Context, localPath, fullRef string, collectGlobs []string) ([]string, bool, error) {
	if !areAllGlobsDefaultPGShape(collectGlobs) {
		return nil, false, nil
	}

	topDirsEntries, err := gitsync.ListTreeEntries(ctx, localPath, fullRef, false, true)
	if err != nil {
		return nil, false, err
	}
	var prefixes []string
	var candidateParents []string
	for _, e := range topDirsEntries {
		if e.Type != "tree" {
			continue
		}
		if e.Path == "pg" {
			prefixes = append(prefixes, "pg")
		} else {
			candidateParents = append(candidateParents, e.Path+"/")
		}
	}

	if len(candidateParents) > 0 {
		subEntries, subErr := gitsync.ListTreeEntries(ctx, localPath, fullRef, false, true, candidateParents...)
		if subErr != nil {
			return nil, false, subErr
		}
		for _, se := range subEntries {
			if se.Type != "tree" {
				continue
			}
			// 严格匹配一级子目录下的全小写 pg 目录 (<dir>/pg)
			if strings.Count(se.Path, "/") == 1 && strings.HasSuffix(se.Path, "/pg") {
				prefixes = append(prefixes, se.Path)
			}
		}
	}

	sort.Strings(prefixes)
	return prefixes, true, nil
}

func areAllGlobsDefaultPGShape(globs []string) bool {
	if len(globs) == 0 {
		return false
	}
	for _, g := range globs {
		t := strings.TrimSpace(g)
		if t != "pg/**/*.proto" && t != "*/pg/**/*.proto" {
			return false
		}
	}
	return true
}

// ExtractStrictPGDir 识别路径所属的严格全小写 pg 目录（"pg" 或 "<dir>/pg"）。
func ExtractStrictPGDir(repoRelPath string) (string, bool) {
	segs := strings.Split(strings.Trim(repoRelPath, "/"), "/")
	if len(segs) >= 2 && segs[0] == "pg" {
		return "pg", true
	}
	if len(segs) >= 3 && segs[1] == "pg" {
		return segs[0] + "/pg", true
	}
	return "", false
}

func matchCollectGlobsStrict(globs []string, relPath string) bool {
	if len(globs) == 0 {
		return false
	}
	return matchAnyDoublestar(globs, relPath)
}

func matchAnyDoublestar(globs []string, relPath string) bool {
	for _, g := range globs {
		trimmed := strings.TrimSpace(g)
		if trimmed == "" {
			continue
		}
		if matchDoublestar(trimmed, relPath) {
			return true
		}
	}
	return false
}

// matchDoublestar 实现严格区分大小写的标准 POSIX doublestar (**) 路径匹配。
func matchDoublestar(pattern, relPath string) bool {
	patSegs := strings.Split(strings.Trim(pattern, "/"), "/")
	pathSegs := strings.Split(strings.Trim(relPath, "/"), "/")
	return matchSegs(patSegs, pathSegs)
}

func matchSegs(patSegs, pathSegs []string) bool {
	for len(patSegs) > 0 {
		p := patSegs[0]
		if p == "**" {
			// 折叠连续 **
			for len(patSegs) > 1 && patSegs[1] == "**" {
				patSegs = patSegs[1:]
			}
			if len(patSegs) == 1 {
				return true
			}
			for i := 0; i <= len(pathSegs); i++ {
				if matchSegs(patSegs[1:], pathSegs[i:]) {
					return true
				}
			}
			return false
		}
		if len(pathSegs) == 0 {
			return false
		}
		ok, err := path.Match(p, pathSegs[0])
		if err != nil || !ok {
			return false
		}
		patSegs = patSegs[1:]
		pathSegs = pathSegs[1:]
	}
	return len(pathSegs) == 0
}

func isGitLFSPointer(content []byte) bool {
	trimmed := bytes.TrimSpace(content)
	return bytes.HasPrefix(trimmed, []byte(lfsPointerPrefix))
}

// buildExternalGitAuthEnv 为 EXTERNAL_STANDALONE 仓库在独立临时目录生成一次性 GIT_ASKPASS / SSH key 环境变量。
func buildExternalGitAuthEnv(repo *models.GitRepo) ([]string, func(), error) {
	noop := func() {}
	if repo == nil || repo.AuthType == "" || repo.AuthType == models.GitAuthNone {
		return nil, noop, nil
	}
	creds, err := repo.DecryptCredentials()
	if err != nil {
		return nil, noop, fmt.Errorf("解密 Git 凭据失败: %w", err)
	}
	if strings.TrimSpace(creds) == "" {
		return nil, noop, nil
	}

	tmpDir, err := os.MkdirTemp("", "cc-gitauth-*")
	if err != nil {
		return nil, noop, err
	}
	cleanup := func() {
		_ = os.RemoveAll(tmpDir)
	}

	if repo.AuthType == models.GitAuthSSHKey {
		keyPath := filepath.Join(tmpDir, "id_key")
		if wErr := os.WriteFile(keyPath, []byte(creds+"\n"), 0600); wErr != nil {
			cleanup()
			return nil, noop, wErr
		}
		sshCmd := fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o BatchMode=yes -o ConnectTimeout=15", keyPath)
		return []string{"GIT_SSH_COMMAND=" + sshCmd}, cleanup, nil
	}

	// TOKEN 模式通过 GIT_ASKPASS 脚本注入密码，避免密码出现在命令行参数或 .git/config 中
	askpassPath := filepath.Join(tmpDir, "askpass.sh")
	username := "oauth2"
	if repo.Provider == models.GitProviderGitHub {
		username = "x-access-token"
	}
	script := "#!/bin/sh\ncase \"$1\" in\n  *Username*|*username*) printf '%s\\n' \"$CC_GIT_USER\" ;;\n  *) printf '%s\\n' \"$CC_GIT_PASS\" ;;\nesac\n"
	if wErr := os.WriteFile(askpassPath, []byte(script), 0700); wErr != nil {
		cleanup()
		return nil, noop, wErr
	}
	return []string{
		"GIT_ASKPASS=" + askpassPath,
		"CC_GIT_USER=" + username,
		"CC_GIT_PASS=" + creds,
	}, cleanup, nil
}
