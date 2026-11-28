package gitclient

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code-common/backend/gitsync"
	"code-common/backend/models"
	"code-contract/config"
	"code-contract/internal/server/decoder"
	contractModels "code-contract/models"

	"gorm.io/datatypes"
)

func createBareGitRepoWithCommits(t *testing.T, branch string, commits []map[string]string) string {
	t.Helper()
	if branch == "" {
		branch = "main"
	}
	base := t.TempDir()
	bareDir := filepath.Join(base, "remote.git")
	workDir := filepath.Join(base, "work")

	runGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=ContractBot",
			"GIT_AUTHOR_EMAIL=bot@example.com",
			"GIT_COMMITTER_NAME=ContractBot",
			"GIT_COMMITTER_EMAIL=bot@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	if err := os.MkdirAll(bareDir, 0755); err != nil {
		t.Fatalf("mkdir bare: %v", err)
	}
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}
	runGit(bareDir, "init", "--bare")
	runGit(workDir, "init", "-b", branch)

	prevFiles := make(map[string]struct{})
	for idx, files := range commits {
		for old := range prevFiles {
			if _, keep := files[old]; !keep {
				_ = os.Remove(filepath.Join(workDir, filepath.FromSlash(old)))
			}
		}
		prevFiles = make(map[string]struct{}, len(files))
		for rel, content := range files {
			prevFiles[rel] = struct{}{}
			abs := filepath.Join(workDir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
				t.Fatalf("mkdir file parent: %v", err)
			}
			if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
				t.Fatalf("write file %s: %v", rel, err)
			}
		}
		runGit(workDir, "add", "-A")
		runGit(workDir, "commit", "--allow-empty", "-m", "commit-"+string(rune('A'+idx)))
	}
	runGit(workDir, "remote", "add", "origin", bareDir)
	runGit(workDir, "push", "-u", "origin", branch)
	return bareDir
}

func pushMoreCommitToBareRepo(t *testing.T, bareDir, branch string, files map[string]string) {
	t.Helper()
	workDir := filepath.Join(t.TempDir(), "work-push")
	runGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=ContractBot",
			"GIT_AUTHOR_EMAIL=bot@example.com",
			"GIT_COMMITTER_NAME=ContractBot",
			"GIT_COMMITTER_EMAIL=bot@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
		return strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "clone", "--branch", branch, bareDir, workDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone failed: %v\n%s", err, string(out))
	}
	// 移除旧 .proto 后全量按给定集合覆写
	_ = filepath.WalkDir(workDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".proto") {
			_ = os.Remove(p)
		}
		return nil
	})
	for rel, content := range files {
		abs := filepath.Join(workDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	runGit(workDir, "add", "-A")
	runGit(workDir, "commit", "--allow-empty", "-m", "update commit")
	runGit(workDir, "push", "origin", branch)
}

func TestCollector_PgWhitelistStrictLowercase_IncrementalBlob_AndLFSQuarantine(t *testing.T) {
	db, reg := setupGitClientTestDB(t)
	ctx := context.Background()
	codesDir := filepath.Join(t.TempDir(), "codes")
	protosDir := filepath.Join(t.TempDir(), "protos")

	bareDir := createBareGitRepoWithCommits(t, "main", []map[string]string{
		{
			"pg/common/base.proto":             "syntax = \"proto3\";\npackage acme.base.v1;\nmessage BaseID { string id = 1; }\n",
			"submod/pg/order.proto":            "syntax = \"proto3\";\npackage acme.order.v1;\nimport \"common/base.proto\";\nmessage OrderReq { acme.base.v1.BaseID id = 1; }\n",
			"platform/core/sub/pg/deep.proto":  "syntax = \"proto3\";\npackage acme.deep.v1;\nmessage DeepMsg { string id = 1; }\n",
			"platform/core/test/pg/skip.proto": "syntax = \"proto3\";\npackage ignored;\nmessage SkipTest { string x = 1; }\n",
			"PG/ignored.proto":                 "syntax = \"proto3\";\npackage ignored;\nmessage Ignored1 { string x = 1; }\n",
			"Pg/ignored2.proto":                "syntax = \"proto3\";\npackage ignored;\nmessage Ignored2 { string x = 1; }\n",
			"src/pg_extra/ignored3.proto":      "syntax = \"proto3\";\npackage ignored;\nmessage Ignored3 { string x = 1; }\n",
			"platform/core/PG/ignored4.proto":  "syntax = \"proto3\";\npackage ignored;\nmessage Ignored4 { string x = 1; }\n",
			"pg/lfs_stub.proto":                "version https://git-lfs.github.com/spec/v1\noid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\nsize 12345\n",
			"pg/oversize.proto":                "syntax = \"proto3\";\npackage acme.big;\nmessage Big { string data = 1; }\n" + strings.Repeat("// pad\n", 40),
		},
	})

	repo := contractModels.GitRepo{
		Name:           "collect-whitelist-repo",
		Provider:       contractModels.GitProviderGeneric,
		RepoURL:        bareDir,
		DefaultBranch:  "main",
		AuthType:       contractModels.GitAuthNone,
		PlatformRepoID: 1,
		SourceMode:     contractModels.GitSourceModePlatformShared,
		SyncStrategy:   contractModels.GitSyncStrategyFetchAndRead,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Stage A 直接测试：验证严格小写 pg/ 白名单（含任意深层 pg 目录）、exclude_globs 过滤、LFS 指针与超限大小隔离
	collect1, err := CollectRepoStageA(ctx, &repo, "main", CollectConfig{
		CodesDir:           codesDir,
		SharedWriteEnabled: true,
		MaxFileBytes:       150, // 让 oversize.proto (>150B) 触发超大隔离，而 lfs_stub.proto (130B) 被读取后识别为 LFS_POINTER
		MaxFilesPerRepo:    100,
	})
	if err != nil {
		t.Fatalf("CollectRepoStageA failed: %v", err)
	}
	if len(collect1.Files) != 5 {
		t.Fatalf("expected exactly 5 whitelist .proto files under pg/, submod/pg/ & platform/core/sub/pg/, got %d", len(collect1.Files))
	}

	// 验证 PLATFORM_SHARED 只读探测：含 **/pg/**/*.proto 时自动降级全树 ls-tree -z -r，识别深层 pg 目录并统计白名单外文件
	platShared := contractModels.GitRepo{
		Name:           "collect-probe-shared",
		Provider:       contractModels.GitProviderGeneric,
		RepoURL:        bareDir,
		DefaultBranch:  "main",
		AuthType:       contractModels.GitAuthNone,
		PlatformRepoID: 1,
		SourceMode:     contractModels.GitSourceModePlatformShared,
		SyncStrategy:   contractModels.GitSyncStrategyFetchAndRead,
	}
	if _, pErr := CollectRepoStageA(ctx, &platShared, "main", CollectConfig{
		CodesDir:           codesDir,
		SharedWriteEnabled: true,
		MaxFileBytes:       2 * 1024 * 1024,
		MaxFilesPerRepo:    100,
	}); pErr != nil {
		t.Fatalf("CollectRepoStageA platShared failed: %v", pErr)
	}
	pgDirs, wlCnt, outCnt, _, probeErr := ProbeRepoPGStructure(ctx, codesDir, bareDir, "main")
	if probeErr != nil {
		t.Fatalf("ProbeRepoPGStructure failed: %v", probeErr)
	}
	if wlCnt != 5 || outCnt != 5 || len(pgDirs) != 3 {
		t.Fatalf("expected whitelist=5 outside=5 pgDirs=3, got wl=%d out=%d pgDirs=%v", wlCnt, outCnt, pgDirs)
	}

	byPath := make(map[string]CollectedFile, len(collect1.Files))
	for _, f := range collect1.Files {
		byPath[f.RepoFilePath] = f
	}
	for _, banned := range []string{
		"PG/ignored.proto",
		"Pg/ignored2.proto",
		"src/pg_extra/ignored3.proto",
		"platform/core/PG/ignored4.proto",
		"platform/core/test/pg/skip.proto",
	} {
		if _, exists := byPath[banned]; exists {
			t.Fatalf("strict lowercase pg whitelist leaked %s", banned)
		}
	}
	if got := byPath["platform/core/sub/pg/deep.proto"]; got.Status != contractModels.ProtoPipelineCollected || got.PGDir != "platform/core/sub/pg" {
		t.Fatalf("expected platform/core/sub/pg/deep.proto COLLECTED with PGDir=platform/core/sub/pg, got %+v", got)
	}
	if got := byPath["pg/lfs_stub.proto"]; got.Status != contractModels.ProtoPipelineQuarantined || got.StatusReason != "LFS_POINTER" {
		t.Fatalf("expected pg/lfs_stub.proto QUARANTINED(LFS_POINTER), got status=%s reason=%s", got.Status, got.StatusReason)
	}
	if got := byPath["pg/oversize.proto"]; got.Status != contractModels.ProtoPipelineQuarantined || !strings.Contains(got.StatusReason, "超过上限") {
		t.Fatalf("expected pg/oversize.proto QUARANTINED(超过上限), got status=%s reason=%s", got.Status, got.StatusReason)
	}
	if got := byPath["pg/common/base.proto"]; got.Status != contractModels.ProtoPipelineCollected || got.IncrementalReused {
		t.Fatalf("expected pg/common/base.proto COLLECTED fresh, got %+v", got)
	}

	// 第二次采集传入 KnownRawBlobSHAs：验证增量 blob 跳过
	known := map[string]string{
		"pg/common/base.proto":  byPath["pg/common/base.proto"].RawBlobSHA,
		"submod/pg/order.proto": byPath["submod/pg/order.proto"].RawBlobSHA,
	}
	collect2, err := CollectRepoStageA(ctx, &repo, "main", CollectConfig{
		CodesDir:           codesDir,
		SharedWriteEnabled: true,
		MaxFileBytes:       2 * 1024 * 1024,
		MaxFilesPerRepo:    100,
		KnownRawBlobSHAs:   known,
	})
	if err != nil {
		t.Fatalf("CollectRepoStageA #2 failed: %v", err)
	}
	for _, f := range collect2.Files {
		if f.RepoFilePath == "pg/common/base.proto" || f.RepoFilePath == "submod/pg/order.proto" {
			if !f.IncrementalReused {
				t.Fatalf("expected %s IncrementalReused=true on unchanged blob SHA", f.RepoFilePath)
			}
		}
	}

	// 运行四段式流水线入库，然后删除 submod/pg/order.proto 并再同步一次，验证软删
	res1, err := RunFourStagePipeline(ctx, db, reg, nil, repo.ID, PipelineOptions{
		Branch:             "main",
		CodesDir:           codesDir,
		ProtosDir:          protosDir,
		SharedWriteEnabled: true,
		TriggerBlastRadius: false,
	})
	if err != nil {
		t.Fatalf("RunFourStagePipeline #1 failed: %v", err)
	}
	if res1.NormalizedCount < 2 {
		t.Fatalf("expected >= 2 normalized files, got %d", res1.NormalizedCount)
	}

	pushMoreCommitToBareRepo(t, bareDir, "main", map[string]string{
		"pg/common/base.proto": "syntax = \"proto3\";\npackage acme.base.v1;\nmessage BaseID { string id = 1; }\n",
	})
	if _, err := RunFourStagePipeline(ctx, db, reg, nil, repo.ID, PipelineOptions{
		Branch:             "main",
		CodesDir:           codesDir,
		ProtosDir:          protosDir,
		SharedWriteEnabled: true,
	}); err != nil {
		t.Fatalf("RunFourStagePipeline #2 failed: %v", err)
	}

	var deletedRow contractModels.ProtoFile
	if err := db.Where("git_repo_id = ? AND repo_file_path = ?", repo.ID, "submod/pg/order.proto").First(&deletedRow).Error; err != nil {
		t.Fatalf("query deleted proto_file: %v", err)
	}
	if !deletedRow.IsDeleted {
		t.Fatalf("expected submod/pg/order.proto is_deleted=true after removal from commit")
	}
}

func TestNormalizer_PassthroughAndSyntaxGateQuarantine(t *testing.T) {
	db, reg := setupGitClientTestDB(t)
	ctx := context.Background()
	codesDir := filepath.Join(t.TempDir(), "codes")
	protosDir := filepath.Join(t.TempDir(), "protos")

	norm := NewPassthroughNormalizer()
	if norm.Version() != "passthrough-0" {
		t.Fatalf("expected PassthroughNormalizer version passthrough-0, got %s", norm.Version())
	}

	bareDir := createBareGitRepoWithCommits(t, "main", []map[string]string{
		{
			"pg/ok/health.proto":  "syntax = \"proto3\";\npackage acme.health.v1;\nservice Health { rpc Check(Ping) returns (Pong); }\nmessage Ping {}\nmessage Pong { string ok = 1; }\n",
			"pg/bad/broken.proto": "syntax = \"proto3\";\npackage acme.bad;\nmessage Broken {\n  string missing_semi = 1\n}\n",
		},
	})

	repo := contractModels.GitRepo{
		Name:           "norm-syntax-gate-repo",
		Provider:       contractModels.GitProviderGeneric,
		RepoURL:        bareDir,
		DefaultBranch:  "main",
		AuthType:       contractModels.GitAuthNone,
		PlatformRepoID: 2,
		SourceMode:     contractModels.GitSourceModePlatformShared,
		SyncStrategy:   contractModels.GitSyncStrategyFetchAndRead,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatalf("create repo: %v", err)
	}

	res, err := RunFourStagePipeline(ctx, db, reg, nil, repo.ID, PipelineOptions{
		Branch:             "main",
		CodesDir:           codesDir,
		ProtosDir:          protosDir,
		SharedWriteEnabled: true,
		Normalizer:         norm,
	})
	if err != nil {
		t.Fatalf("RunFourStagePipeline failed: %v", err)
	}
	if res.NormalizedCount != 1 {
		t.Fatalf("expected 1 normalized file, got %d", res.NormalizedCount)
	}

	var okFile, badFile contractModels.ProtoFile
	if err := db.Where("git_repo_id = ? AND repo_file_path = ?", repo.ID, "pg/ok/health.proto").First(&okFile).Error; err != nil {
		t.Fatalf("query ok file: %v", err)
	}
	if okFile.PipelineStatus != contractModels.ProtoPipelineNormalized || okFile.NormalizerVersion != "passthrough-0" {
		t.Fatalf("unexpected okFile state: status=%s normVer=%s", okFile.PipelineStatus, okFile.NormalizerVersion)
	}

	if err := db.Where("git_repo_id = ? AND repo_file_path = ?", repo.ID, "pg/bad/broken.proto").First(&badFile).Error; err != nil {
		t.Fatalf("query bad file: %v", err)
	}
	if badFile.PipelineStatus != contractModels.ProtoPipelineQuarantined || !strings.Contains(badFile.StatusReason, "PARSE_FAILED") {
		t.Fatalf("expected broken.proto QUARANTINED(PARSE_FAILED), got status=%s reason=%s", badFile.PipelineStatus, badFile.StatusReason)
	}

	// 验证 Tier-2 DBProtoSourceProvider 自动跳过未清洗或隔离文件，仅提供 NORMALIZED 文件
	provider := decoder.NewDBProtoSourceProvider(db)
	if entry, pErr := provider.ResolveProtoSource(ctx, "bad/broken.proto"); pErr != nil || entry != nil {
		t.Fatalf("expected QUARANTINED(PARSE_FAILED) file to be excluded from DBProtoSourceProvider, got entry=%+v err=%v", entry, pErr)
	}
	if entryOk, okErr := provider.ResolveProtoSource(ctx, "ok/health.proto"); okErr != nil || entryOk == nil {
		t.Fatalf("expected ok/health.proto resolved by DBProtoSourceProvider, got entry=%+v err=%v", entryOk, okErr)
	}
}

func TestProtoTree_AtomicSymlinkAndReconcile(t *testing.T) {
	db, reg := setupGitClientTestDB(t)
	ctx := context.Background()
	codesDir := filepath.Join(t.TempDir(), "codes")
	protosDir := filepath.Join(t.TempDir(), "protos")

	bareDir := createBareGitRepoWithCommits(t, "main", []map[string]string{
		{
			"pg/svc/alpha.proto": "syntax = \"proto3\";\npackage acme.alpha;\nmessage AlphaReq { string id = 1; }\n",
		},
	})
	repo := contractModels.GitRepo{
		Name:           "tree-reconcile-repo",
		Provider:       contractModels.GitProviderGeneric,
		RepoURL:        bareDir,
		DefaultBranch:  "main",
		AuthType:       contractModels.GitAuthNone,
		PlatformRepoID: 3,
		SourceMode:     contractModels.GitSourceModePlatformShared,
		SyncStrategy:   contractModels.GitSyncStrategyFetchAndRead,
		SubsystemSlug:  "billing-core",
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatalf("create repo: %v", err)
	}

	if _, err := RunFourStagePipeline(ctx, db, reg, nil, repo.ID, PipelineOptions{
		Branch:             "main",
		CodesDir:           codesDir,
		ProtosDir:          protosDir,
		SharedWriteEnabled: true,
	}); err != nil {
		t.Fatalf("RunFourStagePipeline failed: %v", err)
	}

	subSlug := NormalizeSlug("billing-core", nil, "sub:billing-core", true)
	repoSlug := NormalizeSlug(repo.Name, nil, "repo:1", false)
	cleanLink := filepath.Join(protosDir, TreeKindClean, subSlug, repoSlug)
	rawLink := filepath.Join(protosDir, TreeKindRaw, subSlug, repoSlug)

	for _, lk := range []string{cleanLink, rawLink} {
		target, lErr := os.Readlink(lk)
		if lErr != nil || !strings.Contains(target, ".versions/") {
			t.Fatalf("expected symlink at %s -> .versions/*, got target=%q err=%v", lk, target, lErr)
		}
		if _, err := os.Stat(filepath.Join(lk, ".proto-meta.json")); err != nil {
			t.Fatalf("expected .proto-meta.json inside %s: %v", lk, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cleanLink, ".normalize-report.json")); err != nil {
		t.Fatalf("expected .normalize-report.json in clean tree: %v", err)
	}

	// 破坏 clean 符号链接，并制造孤儿软链与残留 .tmp 目录
	_ = os.Remove(cleanLink)
	orphanDir := filepath.Join(protosDir, TreeKindClean, "ghost-sub")
	_ = os.MkdirAll(orphanDir, 0755)
	_ = os.Symlink("../../.versions/clean/9999/v1", filepath.Join(orphanDir, "ghost-repo"))
	staleTmp := filepath.Join(protosDir, ".tmp", "stale-aborted")
	_ = os.MkdirAll(staleTmp, 0755)
	past := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(staleTmp, past, past)

	mgr := NewProtoTreeManager(protosDir)
	rebuilt, pruned, err := mgr.ReconcileAll(ctx, db)
	if err != nil {
		t.Fatalf("ReconcileAll failed: %v", err)
	}
	if rebuilt < 1 || pruned < 1 {
		t.Fatalf("expected rebuilt >= 1 and pruned >= 1, got rebuilt=%d pruned=%d", rebuilt, pruned)
	}
	if _, err := os.Readlink(cleanLink); err != nil {
		t.Fatalf("expected clean symlink rebuilt by ReconcileAll: %v", err)
	}
	if _, err := os.Stat(filepath.Join(orphanDir, "ghost-repo")); !os.IsNotExist(err) {
		t.Fatalf("expected orphan symlink to be pruned")
	}
}

func TestAnalyzer_ImportOwners_AmbiguousError_AndExternalCopy(t *testing.T) {
	db, reg := setupGitClientTestDB(t)
	ctx := context.Background()
	codesDir := filepath.Join(t.TempDir(), "codes")
	protosDir := filepath.Join(t.TempDir(), "protos")

	// 场景 A: 两个均声明为 BASE_LIB 的仓库各自提供不同内容的同逻辑路径 shared/types.proto -> IMPORT_AMBIGUOUS
	bareA := createBareGitRepoWithCommits(t, "main", []map[string]string{
		{"pg/shared/types.proto": "syntax = \"proto3\";\npackage acme.shared;\nmessage Money { int64 cents = 1; }\n"},
	})
	bareB := createBareGitRepoWithCommits(t, "main", []map[string]string{
		{"pg/shared/types.proto": "syntax = \"proto3\";\npackage acme.shared;\nmessage Money { string amount = 1; }\n"},
	})
	// 场景 B: Repo C 为 BUSINESS 且拷贝了一份与 Repo A 完全相同的 pg/shared/alias_copy.proto（同时 Repo A 也有同一文件），验证 is_external_copy=true
	repoA := contractModels.GitRepo{Name: "owner-a", Provider: contractModels.GitProviderGeneric, RepoURL: bareA, DefaultBranch: "main", AuthType: contractModels.GitAuthNone, PlatformRepoID: 4, SourceMode: contractModels.GitSourceModePlatformShared, SyncStrategy: contractModels.GitSyncStrategyFetchAndRead}
	repoB := contractModels.GitRepo{Name: "owner-b", Provider: contractModels.GitProviderGeneric, RepoURL: bareB, DefaultBranch: "main", AuthType: contractModels.GitAuthNone, PlatformRepoID: 5, SourceMode: contractModels.GitSourceModePlatformShared, SyncStrategy: contractModels.GitSyncStrategyFetchAndRead}
	if err := db.Create(&repoA).Error; err != nil {
		t.Fatalf("create repoA: %v", err)
	}
	if err := db.Create(&repoB).Error; err != nil {
		t.Fatalf("create repoB: %v", err)
	}
	bundleA := contractModels.GitProtoBundle{GitRepoID: repoA.ID, BundleName: "bundle-a", TargetSchemaName: "schema-owner-a", Category: contractModels.ProtoCategoryBaseLib, SubdirPath: "pg", IncludeDirsJSON: datatypes.JSON([]byte(`["."]`)), FileGlobsJSON: datatypes.JSON([]byte(`["**/*.proto"]`))}
	bundleB := contractModels.GitProtoBundle{GitRepoID: repoB.ID, BundleName: "bundle-b", TargetSchemaName: "schema-owner-b", Category: contractModels.ProtoCategoryBaseLib, SubdirPath: "pg", IncludeDirsJSON: datatypes.JSON([]byte(`["."]`)), FileGlobsJSON: datatypes.JSON([]byte(`["**/*.proto"]`))}
	if err := db.Create(&bundleA).Error; err != nil {
		t.Fatalf("create bundleA: %v", err)
	}
	if err := db.Create(&bundleB).Error; err != nil {
		t.Fatalf("create bundleB: %v", err)
	}

	if _, err := RunFourStagePipeline(ctx, db, reg, nil, repoA.ID, PipelineOptions{Branch: "main", CodesDir: codesDir, ProtosDir: protosDir, SharedWriteEnabled: true}); err != nil {
		t.Fatalf("sync repoA: %v", err)
	}
	if _, err := RunFourStagePipeline(ctx, db, reg, nil, repoB.ID, PipelineOptions{Branch: "main", CodesDir: codesDir, ProtosDir: protosDir, SharedWriteEnabled: true}); err != nil {
		t.Fatalf("sync repoB: %v", err)
	}

	// 未指认 owner 前，两个 BASE_LIB 均提供 shared/types.proto，DBProtoSourceProvider 必须返回 *decoder.ImportAmbiguousError
	provider := decoder.NewDBProtoSourceProvider(db)
	_, resolveErr := provider.ResolveProtoSource(ctx, "shared/types.proto")
	var ambErr *decoder.ImportAmbiguousError
	if !errors.As(resolveErr, &ambErr) {
		t.Fatalf("expected *decoder.ImportAmbiguousError before arbitration, got err=%v", resolveErr)
	}

	// 检查冲突报告列表 & 管理员落定权威 Owner 为 bundleA
	report, err := DetectConflictsAndMarkExternalCopies(ctx, db)
	if err != nil {
		t.Fatalf("DetectConflicts: %v", err)
	}
	if len(report.ImportAmbiguities) == 0 {
		t.Fatalf("expected >= 1 import ambiguity for shared/types.proto")
	}

	owners, err := ResolveProtoConflict(ctx, db, "contract_admin", ResolveConflictInput{
		Kind:           "IMPORT",
		LogicalPath:    "shared/types.proto",
		OwnerGitRepoID: repoA.ID,
		OwnerBundleID:  bundleA.ID,
	})
	if err != nil {
		t.Fatalf("ResolveProtoConflict failed: %v", err)
	}
	if len(owners) != 1 || owners[0].Source != contractModels.ImportOwnerSourceManual {
		t.Fatalf("expected MANUAL owner row, got %+v", owners)
	}

	// 非权威方 bundleB 的文件必须被标记为 is_external_copy=true，且 Provider 不再报歧义
	var copyFile contractModels.ProtoFile
	if err := db.Where("bundle_id = ? AND file_path = ?", bundleB.ID, "shared/types.proto").First(&copyFile).Error; err != nil {
		t.Fatalf("query copyFile: %v", err)
	}
	if !copyFile.IsExternalCopy {
		t.Fatalf("expected non-owner file to be marked is_external_copy=true")
	}
	entry, err := provider.ResolveProtoSource(ctx, "shared/types.proto")
	if err != nil || entry == nil || !strings.Contains(string(entry.Content), "int64 cents") {
		t.Fatalf("expected resolved source from owner-a after arbitration, got err=%v entry=%+v", err, entry)
	}
}

func TestCompiler_CycleBundleSCC_BlastRadius_ReNormalize_AndDeterminism(t *testing.T) {
	db, reg := setupGitClientTestDB(t)
	ctx := context.Background()
	codesDir := filepath.Join(t.TempDir(), "codes")
	protosDir := filepath.Join(t.TempDir(), "protos")

	// Bundle 1 (pg/svc_a) 导入 cyc/b1.proto，Bundle 2 (pg/svc_b) 导入 cyc/a2.proto -> Bundle 级环状强连通分量 {1, 2}，而文件级 DAG 无环！
	// Bundle 3 (pg/downstream) 导入 cyc/a1.proto -> 依赖下游，验证 blast radius 增量重编与决定性！
	bareDir := createBareGitRepoWithCommits(t, "main", []map[string]string{
		{
			"pg/svc_a/cyc/a1.proto":        "syntax = \"proto3\";\npackage acme.cyc;\nimport \"cyc/b1.proto\";\nmessage A1 { B1 b = 1; }\n",
			"pg/svc_a/cyc/a2.proto":        "syntax = \"proto3\";\npackage acme.cyc;\nmessage A2 { string tag = 1; }\n",
			"pg/svc_b/cyc/b1.proto":        "syntax = \"proto3\";\npackage acme.cyc;\nimport \"cyc/a2.proto\";\nmessage B1 { A2 a = 1; }\n",
			"pg/downstream/consumer.proto": "syntax = \"proto3\";\npackage acme.consumer;\nimport \"cyc/a1.proto\";\nmessage ConsReq { acme.cyc.A1 a = 1; }\n",
		},
	})

	repo := contractModels.GitRepo{
		Name:           "scc-blast-repo",
		Provider:       contractModels.GitProviderGeneric,
		RepoURL:        bareDir,
		DefaultBranch:  "main",
		AuthType:       contractModels.GitAuthNone,
		PlatformRepoID: 6,
		SourceMode:     contractModels.GitSourceModePlatformShared,
		SyncStrategy:   contractModels.GitSyncStrategyFetchAndRead,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatalf("create repo: %v", err)
	}
	b1 := contractModels.GitProtoBundle{GitRepoID: repo.ID, BundleName: "svc-a", TargetSchemaName: "schema-svc-a", Category: contractModels.ProtoCategoryBaseLib, SubdirPath: "pg/svc_a", IncludeDirsJSON: datatypes.JSON([]byte(`["."]`)), FileGlobsJSON: datatypes.JSON([]byte(`["**/*.proto"]`))}
	b2 := contractModels.GitProtoBundle{GitRepoID: repo.ID, BundleName: "svc-b", TargetSchemaName: "schema-svc-b", Category: contractModels.ProtoCategoryBaseLib, SubdirPath: "pg/svc_b", IncludeDirsJSON: datatypes.JSON([]byte(`["."]`)), FileGlobsJSON: datatypes.JSON([]byte(`["**/*.proto"]`))}
	b3 := contractModels.GitProtoBundle{GitRepoID: repo.ID, BundleName: "downstream", TargetSchemaName: "schema-downstream", Category: contractModels.ProtoCategoryBusiness, SubdirPath: "pg/downstream", IncludeDirsJSON: datatypes.JSON([]byte(`["."]`)), FileGlobsJSON: datatypes.JSON([]byte(`["**/*.proto"]`))}
	for _, bp := range []*contractModels.GitProtoBundle{&b1, &b2, &b3} {
		if err := db.Create(bp).Error; err != nil {
			t.Fatalf("create bundle: %v", err)
		}
	}

	// 验证 ComputeBundleSCCs 将 {b1, b2} 合并为同一 SCC
	sccs := ComputeBundleSCCs([]uint64{b1.ID, b2.ID, b3.ID}, FileDependencyGraph{
		FileBundle: map[string]uint64{
			"cyc/a1.proto":   b1.ID,
			"cyc/a2.proto":   b1.ID,
			"cyc/b1.proto":   b2.ID,
			"consumer.proto": b3.ID,
		},
		ForwardEdges: map[string]map[string]struct{}{
			"cyc/a1.proto":   {"cyc/b1.proto": {}},
			"cyc/b1.proto":   {"cyc/a2.proto": {}},
			"consumer.proto": {"cyc/a1.proto": {}},
		},
	})
	foundCycleSCC := false
	for _, scc := range sccs {
		if len(scc) == 2 {
			foundCycleSCC = true
		}
	}
	if !foundCycleSCC {
		t.Fatalf("expected ComputeBundleSCCs to coalesce cyclic bundles {b1, b2}, got %+v", sccs)
	}

	res, err := RunFourStagePipeline(ctx, db, reg, nil, repo.ID, PipelineOptions{
		Branch:             "main",
		CodesDir:           codesDir,
		ProtosDir:          protosDir,
		SharedWriteEnabled: true,
	})
	if err != nil {
		t.Fatalf("RunFourStagePipeline failed: %v", err)
	}
	if len(res.BundleResults) != 3 {
		t.Fatalf("expected 3 compiled bundles, got %d", len(res.BundleResults))
	}
	for _, br := range res.BundleResults {
		if br.CompileError != "" || br.CompiledSchemaID == nil || *br.CompiledSchemaID == 0 {
			t.Fatalf("expected bundle %s compiled cleanly, got %+v", br.BundleName, br)
		}
	}

	// 另建下游仓库消费 cyc/a1.proto 并验证 ReNormalizeRepo 与 BlastRadius
	downBare := createBareGitRepoWithCommits(t, "main", []map[string]string{
		{"pg/ext_consumer.proto": "syntax = \"proto3\";\npackage acme.ext;\nimport \"cyc/a1.proto\";\nmessage ExtReq { acme.cyc.A1 a = 1; }\n"},
	})
	extRepo := contractModels.GitRepo{Name: "ext-consumer-repo", Provider: contractModels.GitProviderGeneric, RepoURL: downBare, DefaultBranch: "main", AuthType: contractModels.GitAuthNone, PlatformRepoID: 7, SourceMode: contractModels.GitSourceModePlatformShared, SyncStrategy: contractModels.GitSyncStrategyFetchAndRead}
	if err := db.Create(&extRepo).Error; err != nil {
		t.Fatalf("create extRepo: %v", err)
	}
	if _, err := RunFourStagePipeline(ctx, db, reg, nil, extRepo.ID, PipelineOptions{
		Branch:             "main",
		CodesDir:           codesDir,
		ProtosDir:          protosDir,
		SharedWriteEnabled: true,
	}); err != nil {
		t.Fatalf("sync extRepo failed: %v", err)
	}

	// 推送 upstream 新提交变更 cyc/a1.proto 并触发影响面下游重编，再验证 ReNormalizeRepo 无网络重跑且确定性
	pushMoreCommitToBareRepo(t, bareDir, "main", map[string]string{
		"pg/svc_a/cyc/a1.proto":        "syntax = \"proto3\";\npackage acme.cyc;\nimport \"cyc/b1.proto\";\nmessage A1 { B1 b = 1; string trace_id = 2; }\n",
		"pg/svc_a/cyc/a2.proto":        "syntax = \"proto3\";\npackage acme.cyc;\nmessage A2 { string tag = 1; }\n",
		"pg/svc_b/cyc/b1.proto":        "syntax = \"proto3\";\npackage acme.cyc;\nimport \"cyc/a2.proto\";\nmessage B1 { A2 a = 1; }\n",
		"pg/downstream/consumer.proto": "syntax = \"proto3\";\npackage acme.consumer;\nimport \"cyc/a1.proto\";\nmessage ConsReq { acme.cyc.A1 a = 1; }\n",
	})
	blastRes, err := RunFourStagePipeline(ctx, db, reg, nil, repo.ID, PipelineOptions{
		Branch:             "main",
		CodesDir:           codesDir,
		ProtosDir:          protosDir,
		SharedWriteEnabled: true,
		TriggerBlastRadius: true,
	})
	if err != nil {
		t.Fatalf("RunFourStagePipeline blast update failed: %v", err)
	}
	if len(blastRes.BlastRadiusBundleIDs) == 0 {
		t.Fatalf("expected downstream BlastRadiusBundleIDs triggered after upstream proto mutation")
	}

	reRes1, err := ReNormalizeRepo(ctx, db, reg, nil, repo.ID, PipelineOptions{
		CodesDir:           codesDir,
		ProtosDir:          protosDir,
		TriggerBlastRadius: false,
	})
	if err != nil {
		t.Fatalf("ReNormalizeRepo #1 failed: %v", err)
	}

	reRes2, err := ReNormalizeRepo(ctx, db, reg, nil, repo.ID, PipelineOptions{
		CodesDir:           codesDir,
		ProtosDir:          protosDir,
		TriggerBlastRadius: false,
	})
	if err != nil {
		t.Fatalf("ReNormalizeRepo #2 failed: %v", err)
	}
	if reRes1.NormalizedCount != reRes2.NormalizedCount {
		t.Fatalf("expected deterministic NormalizedCount: %d vs %d", reRes1.NormalizedCount, reRes2.NormalizedCount)
	}
}

func TestMRGate_NormalizerConsistency(t *testing.T) {
	db, reg := setupGitClientTestDB(t)
	ctx := context.Background()

	repo := contractModels.GitRepo{
		Name:           "mr-norm-repo",
		Provider:       contractModels.GitProviderGitLab,
		RepoURL:        "https://git.example.com/team/mr-norm",
		DefaultBranch:  "main",
		AuthType:       contractModels.GitAuthNone,
		PlatformRepoID: 8,
		SourceMode:     contractModels.GitSourceModePlatformShared,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatalf("create repo: %v", err)
	}
	bundle := contractModels.GitProtoBundle{
		GitRepoID:        repo.ID,
		BundleName:       "order-pg",
		TargetSchemaName: "mr-norm-order",
		Category:         contractModels.ProtoCategoryBusiness,
		SubdirPath:       "pg/order",
		IncludeDirsJSON:  datatypes.JSON([]byte(`["."]`)),
		FileGlobsJSON:    datatypes.JSON([]byte(`["**/*.proto"]`)),
	}
	if err := db.Create(&bundle).Error; err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	run := contractModels.MRCheckRun{
		GitRepoID:     repo.ID,
		BundleID:      &bundle.ID,
		MRIID:         42,
		EventPhase:    contractModels.MREventPhaseGate,
		MRTitle:       "feat: syntax broken proto in pg/",
		SourceBranch:  "feat/bad-proto",
		TargetBranch:  "main",
		HeadCommitSHA: "deadbeef1234",
		Status:        contractModels.MRCheckStatusPending,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}

	runner := NewMRGateRunner(db, reg, nil)
	checked, err := runner.ExecuteCheckRun(ctx, run.ID, []MRPatchFile{
		{
			ChangeType: MRChangeAdded,
			NewPath:    "pg/order/broken.proto",
			Content:    "syntax = \"proto3\";\npackage acme.order;\nmessage Bad {\n  int32 oops = 1\n}\n",
		},
	}, false)
	if err != nil {
		t.Fatalf("ExecuteCheckRun error: %v", err)
	}
	if checked.Status != contractModels.MRCheckStatusFailed || !checked.NormalizeFailed || checked.CompilePassed {
		t.Fatalf("expected FAILED + NormalizeFailed=true + CompilePassed=false, got status=%s normFail=%v compile=%v",
			checked.Status, checked.NormalizeFailed, checked.CompilePassed)
	}
	if !strings.Contains(checked.CompileErrorText, "NORMALIZE_FAILED") {
		t.Fatalf("expected NORMALIZE_FAILED prefix in compile_error_text, got %q", checked.CompileErrorText)
	}

	if _, _, ovErr := runner.ApplyAdminOverride(ctx, checked.ID, 1, "admin", "attempt override normalize error"); !errors.Is(ovErr, ErrMROverrideForbiddenCompileFailed) {
		t.Fatalf("expected ErrMROverrideForbiddenCompileFailed, got %v", ovErr)
	}
}

func TestSyncScheduler_SingleflightAndLocalCacheOnly(t *testing.T) {
	db, reg := setupGitClientTestDB(t)
	ctx := context.Background()
	codesDir := filepath.Join(t.TempDir(), "codes")
	protosDir := filepath.Join(t.TempDir(), "protos")

	// 准备预克隆共享缓存（模拟 code-shield 维护好的 refs/remotes/origin/main），同时配置一个不可达远端 URL 验证 LOCAL_CACHE_ONLY 绝不触发网络 fetch
	srcBare := createBareGitRepoWithCommits(t, "main", []map[string]string{
		{"pg/cache/item.proto": "syntax = \"proto3\";\npackage acme.cache;\nmessage CacheItem { string key = 1; }\n"},
	})
	platURL := "https://unreachable.invalid/group/shared-cache.git"
	localCachePath, err := gitsync.ResolveRepoLocalPath(codesDir, platURL)
	if err != nil {
		t.Fatalf("ResolveRepoLocalPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(localCachePath), 0755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if out, cErr := exec.Command("git", "clone", "--branch", "main", srcBare, localCachePath).CombinedOutput(); cErr != nil {
		t.Fatalf("seed local cache clone failed: %v\n%s", cErr, string(out))
	}
	// 设置 origin URL 为 platURL 验证一致性检查
	if out, rErr := exec.Command("git", "-C", localCachePath, "remote", "set-url", "origin", platURL).CombinedOutput(); rErr != nil {
		t.Fatalf("set origin url: %v\n%s", rErr, string(out))
	}

	platRepo := models.Repository{
		Name:         "shared-cache",
		URL:          platURL,
		Branch:       "main",
		ServiceGroup: "cache-sub",
		IsActive:     true,
	}
	if err := db.Create(&platRepo).Error; err != nil {
		t.Fatalf("create platRepo: %v", err)
	}
	platID := uint64(platRepo.ID)
	gitRepo := contractModels.GitRepo{
		Name:           "shared-cache",
		Provider:       contractModels.GitProviderGitLab,
		RepoURL:        platURL,
		DefaultBranch:  "main",
		AuthType:       contractModels.GitAuthNone,
		PlatformRepoID: platID,
		SourceMode:     contractModels.GitSourceModePlatformShared,
		SyncStrategy:   contractModels.GitSyncStrategyLocalCacheOnly,
		SubsystemSlug:  "cache-sub",
	}
	if err := db.Create(&gitRepo).Error; err != nil {
		t.Fatalf("create gitRepo: %v", err)
	}

	cfg := config.AppConfig
	cfg.Storage.CodesDir = codesDir
	cfg.Storage.ProtosDir = protosDir
	cfg.Storage.SharedWriteEnabled = false // 默认只读隔离模式 -> 强制 LOCAL_CACHE_ONLY
	cfg.ProtoCollect.SyncConcurrency = 2

	sched := NewSyncScheduler(db, reg, nil, &cfg)

	var wg sync.WaitGroup
	errs := make([]error, 4)
	results := make([]*PipelineRunResult, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = sched.TriggerRepoSync(ctx, gitRepo.ID, PipelineOptions{Branch: "main"})
		}(i)
	}
	wg.Wait()

	for i := 0; i < 4; i++ {
		if errs[i] != nil {
			t.Fatalf("concurrent TriggerRepoSync[%d] failed: %v", i, errs[i])
		}
		if results[i] == nil || results[i].NormalizedCount != 1 {
			t.Fatalf("expected NormalizedCount=1 via LOCAL_CACHE_ONLY, got %+v", results[i])
		}
	}
}

func TestExtractStrictPGDirAndDefaultGlobShape(t *testing.T) {
	cases := []struct {
		path    string
		wantDir string
		wantOK  bool
	}{
		{path: "pg/a.proto", wantDir: "pg", wantOK: true},
		{path: "submod/pg/order.proto", wantDir: "submod/pg", wantOK: true},
		{path: "platform/core/sub/pg/deep.proto", wantDir: "platform/core/sub/pg", wantOK: true},
		{path: "platform/core/pg/sub/pg/nested.proto", wantDir: "platform/core/pg", wantOK: true},
		{path: "pg.proto", wantDir: "", wantOK: false},
		{path: "platform/pg.proto", wantDir: "", wantOK: false},
		{path: "platform/PG/a.proto", wantDir: "", wantOK: false},
		{path: "platform/Pg/a.proto", wantDir: "", wantOK: false},
		{path: "platform/pg_extra/a.proto", wantDir: "", wantOK: false},
		{path: "", wantDir: "", wantOK: false},
	}
	for _, tc := range cases {
		gotDir, gotOK := ExtractStrictPGDir(tc.path)
		if gotDir != tc.wantDir || gotOK != tc.wantOK {
			t.Fatalf("ExtractStrictPGDir(%q) = (%q, %v), want (%q, %v)",
				tc.path, gotDir, gotOK, tc.wantDir, tc.wantOK)
		}
	}

	if !areAllGlobsDefaultPGShape([]string{"pg/**/*.proto", "*/pg/**/*.proto"}) {
		t.Fatalf("expected 0/1-level pg globs to use prefix fast path")
	}
	if areAllGlobsDefaultPGShape([]string{"pg/**/*.proto", "*/pg/**/*.proto", "**/pg/**/*.proto"}) {
		t.Fatalf("expected **/pg/**/*.proto to fall back to full tree traversal")
	}
	if areAllGlobsDefaultPGShape(nil) {
		t.Fatalf("expected empty globs not to match default pg shape")
	}
}
