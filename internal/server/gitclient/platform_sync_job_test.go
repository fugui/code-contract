package gitclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"code-common/backend/gitsync"
	commonModels "code-common/backend/models"
	"code-contract/config"
	"code-contract/models"
)

func copyDirRecursive(t *testing.T, src, dst string) {
	t.Helper()
	if err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}); err != nil {
		t.Fatalf("copyDirRecursive %s -> %s failed: %v", src, dst, err)
	}
}

func TestRunSyncAllJob_500ReposScale_AndCacheOnlyNoFetch(t *testing.T) {
	db, reg := setupGitClientTestDB(t)
	ctx := context.Background()

	oldCfg := config.AppConfig
	codesDir := filepath.Join(t.TempDir(), "codes")
	protosDir := filepath.Join(t.TempDir(), "protos")
	config.AppConfig.Storage.CodesDir = codesDir
	config.AppConfig.Storage.ProtosDir = protosDir
	config.AppConfig.Storage.SharedWriteEnabled = false
	config.AppConfig.ProtoCollect.SyncConcurrency = 8
	defer func() {
		config.AppConfig = oldCfg
	}()

	dep := commonModels.Department{Name: "核心研发部"}
	if err := db.Create(&dep).Error; err != nil {
		t.Fatalf("create department: %v", err)
	}
	owner := commonModels.User{Username: "sync500_owner", Name: "sync500_owner", Email: "sync500@example.com", Password: "hashed_password"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	// 1. 准备两类模板缓存：含小写 pg/ 的仓库缓存 vs 仅含非白名单 (PG/ 与 src/pg_extra/) 的缓存
	pgBare := createBareGitRepoWithCommits(t, "main", []map[string]string{
		{"pg/svc.proto": "syntax = \"proto3\";\npackage scale.v1;\nmessage Ping { string id = 1; }\n"},
	})
	noPGBare := createBareGitRepoWithCommits(t, "main", []map[string]string{
		{"PG/upper.proto": "syntax = \"proto3\";\npackage ignored.v1;\nmessage Ignored { string id = 1; }\n"},
		{"src/pg_extra/misc.proto": "syntax = \"proto3\";\npackage ignored.v2;\nmessage Misc { string id = 1; }\n"},
		{"README.md": "# no proto\n"},
	})
	tmplCodes := filepath.Join(t.TempDir(), "tmpl-codes")
	pgCacheSrc := filepath.Join(tmplCodes, "tmpl-pg")
	noPGCacheSrc := filepath.Join(tmplCodes, "tmpl-nopg")
	if _, _, err := gitsync.FetchBranchToRef(ctx, tmplCodes, pgCacheSrc, pgBare, "main", "refs/contract/heads"); err != nil {
		t.Fatalf("seed pg cache template: %v", err)
	}
	if _, _, err := gitsync.FetchBranchToRef(ctx, tmplCodes, noPGCacheSrc, noPGBare, "main", "refs/contract/heads"); err != nil {
		t.Fatalf("seed no-pg cache template: %v", err)
	}

	// 2. 插入 500 条平台 Repository（20 含 pg/ 已缓存，30 无 pg/ 已缓存，450 无本地缓存且 URL 不可达）
	const (
		totalRepos = 500
		pgCount    = 20
		noPGCount  = 30
	)
	plats := make([]commonModels.Repository, 0, totalRepos)
	for i := 0; i < totalRepos; i++ {
		repoURL := fmt.Sprintf("http://127.0.0.1:1/scale/repo-%03d.git", i)
		group := "group-no-cache"
		if i < pgCount {
			repoURL = fmt.Sprintf("http://127.0.0.1:1/scale/pg-repo-%03d.git", i)
			group = "group-has-pg"
		} else if i < pgCount+noPGCount {
			repoURL = fmt.Sprintf("http://127.0.0.1:1/scale/nopg-repo-%03d.git", i)
			group = "group-no-pg"
		}
		plats = append(plats, commonModels.Repository{
			DepartmentID: dep.ID,
			Name:         fmt.Sprintf("scale-repo-%03d", i),
			URL:          repoURL,
			HTTPURL:      repoURL,
			OwnerID:      owner.ID,
			Branch:       "main",
			ServiceGroup: group,
			IsActive:     true,
		})

		if i < pgCount+noPGCount {
			dst, rErr := gitsync.ResolveRepoLocalPath(codesDir, repoURL)
			if rErr != nil {
				t.Fatalf("ResolveRepoLocalPath: %v", rErr)
			}
			if i < pgCount {
				copyDirRecursive(t, pgCacheSrc, dst)
			} else {
				copyDirRecursive(t, noPGCacheSrc, dst)
			}
		}
	}
	if err := db.CreateInBatches(&plats, 100).Error; err != nil {
		t.Fatalf("CreateInBatches 500 repositories failed: %v", err)
	}

	// 验证 FETCH_MISSING 在 SharedWriteEnabled=false 时必须被阻断
	sched := NewSyncScheduler(db, reg, nil, &config.AppConfig)
	var (
		mu         sync.Mutex
		progressEv []SyncJobProgressEvent
	)
	sched.SetJobBroadcaster(func(evt SyncJobProgressEvent) {
		mu.Lock()
		progressEv = append(progressEv, evt)
		mu.Unlock()
	})

	if _, err := RunSyncAllJob(ctx, db, sched, &config.AppConfig, SyncAllRequest{
		Scope:       models.SyncJobScopeAll,
		CachePolicy: models.SyncCachePolicyFetchMissing,
		WaitSync:    true,
	}); !errors.Is(err, ErrSharedWriteDisabled) {
		t.Fatalf("expected ErrSharedWriteDisabled when SharedWriteEnabled=false, got %v", err)
	}

	// 3. 执行 500 仓 CACHE_ONLY 批处理扫描（零网络拉取）
	job, err := RunSyncAllJob(ctx, db, sched, &config.AppConfig, SyncAllRequest{
		Scope:       models.SyncJobScopeAll,
		CachePolicy: models.SyncCachePolicyCacheOnly,
		WaitSync:    true,
		TriggeredBy: "scale-test",
	})
	if err != nil {
		t.Fatalf("RunSyncAllJob 500 repos CACHE_ONLY failed: %v", err)
	}
	if job.Status != models.SyncJobStatusCompleted ||
		job.TotalRepos != totalRepos ||
		job.ProcessedRepos != totalRepos ||
		job.HasProtoCount != pgCount ||
		job.NoProtoCount != noPGCount ||
		job.NoCacheCount != (totalRepos-pgCount-noPGCount) ||
		job.FailedCount != 0 {
		t.Fatalf("unexpected 500-repo job metrics: %+v", job)
	}

	mu.Lock()
	evCount := len(progressEv)
	mu.Unlock()
	if evCount == 0 {
		t.Fatalf("expected SSE proto_sync_job.progress events, got 0")
	}

	// 验证无 pg/ 的 30 个仓静默归档为 NO_PROTO 且零空 Bundle、零空目录
	var noPGRepoIDs []uint64
	if err := db.Model(&models.GitRepo{}).
		Where("proto_status = ?", models.GitRepoProtoStatusNoProto).
		Pluck("id", &noPGRepoIDs).Error; err != nil {
		t.Fatalf("pluck NO_PROTO repo ids: %v", err)
	}
	if len(noPGRepoIDs) != noPGCount {
		t.Fatalf("expected %d NO_PROTO repos, got %d", noPGCount, len(noPGRepoIDs))
	}
	var noPGBundleCnt int64
	_ = db.Model(&models.GitProtoBundle{}).Where("git_repo_id IN ?", noPGRepoIDs).Count(&noPGBundleCnt).Error
	if noPGBundleCnt != 0 {
		t.Fatalf("NO_PROTO repos must not generate empty Bundles, got %d", noPGBundleCnt)
	}
	noPGRepoDir := filepath.Join(protosDir, TreeKindRaw, "group-no-pg")
	if _, statErr := os.Stat(noPGRepoDir); !os.IsNotExist(statErr) {
		t.Fatalf("NO_PROTO repos must not materialize empty protos dir %s", noPGRepoDir)
	}

	// 验证 SyncSummaryDTO 统计一致
	summary, sErr := GetSyncSummary(ctx, db, false)
	if sErr != nil {
		t.Fatalf("GetSyncSummary failed: %v", sErr)
	}
	if summary.PlatformActiveTotal != totalRepos ||
		summary.AlignedTotal != totalRepos ||
		summary.HasProtoCount != pgCount ||
		summary.NoProtoCount != noPGCount ||
		summary.NoLocalCacheCount != (totalRepos-pgCount-noPGCount) {
		t.Fatalf("unexpected SyncSummary: %+v", summary)
	}

	// 4. 二次 CACHE_ONLY 增量跑必须跳过未变化 commit 的仓
	job2, err := RunSyncAllJob(ctx, db, sched, &config.AppConfig, SyncAllRequest{
		Scope:        models.SyncJobScopeServiceGroup,
		ServiceGroup: "group-has-pg",
		CachePolicy:  models.SyncCachePolicyCacheOnly,
		WaitSync:     true,
	})
	if err != nil {
		t.Fatalf("second incremental RunSyncAllJob failed: %v", err)
	}
	if job2.TotalRepos != pgCount || job2.SkippedUnchangedCount != pgCount || job2.HasProtoCount != pgCount {
		t.Fatalf("expected all %d HAS_PROTO repos skipped unchanged on 2nd run, got %+v", pgCount, job2)
	}

	// 5. 开启 SharedWriteEnabled 并为 1 个原 NO_LOCAL_CACHE 仓替换为本地真实 bare 仓，验证 FETCH_MISSING 成功拉取并转为 HAS_PROTO
	realBare := createBareGitRepoWithCommits(t, "main", []map[string]string{
		{"pg/new.proto": "syntax = \"proto3\";\npackage recovered.v1;\nmessage RecRsp { bool ok = 1; }\n"},
	})
	var targetNoCache models.GitRepo
	if err := db.Where("proto_status = ?", models.GitRepoProtoStatusNoLocalCache).Order("id ASC").First(&targetNoCache).Error; err != nil {
		t.Fatalf("find NO_LOCAL_CACHE repo: %v", err)
	}
	if err := db.Model(&commonModels.Repository{}).Where("id = ?", targetNoCache.PlatformRepoID).
		Updates(map[string]any{"url": realBare, "http_url": realBare}).Error; err != nil {
		t.Fatalf("update platform repo URL: %v", err)
	}
	config.AppConfig.Storage.SharedWriteEnabled = true
	job3, err := RunSyncAllJob(ctx, db, sched, &config.AppConfig, SyncAllRequest{
		Scope:       models.SyncJobScopeSelected,
		RepoIDs:     []uint64{targetNoCache.ID},
		CachePolicy: models.SyncCachePolicyFetchMissing,
		WaitSync:    true,
	})
	if err != nil {
		t.Fatalf("FETCH_MISSING selected job failed: %v", err)
	}
	if job3.TotalRepos != 1 || job3.HasProtoCount != 1 || job3.FailedCount != 0 {
		t.Fatalf("expected selected FETCH_MISSING job to recover 1 HAS_PROTO repo, got %+v", job3)
	}
}
