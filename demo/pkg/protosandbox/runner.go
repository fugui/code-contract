package protosandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"code-common/backend/gitsync"
	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/gitclient"
	"code-contract/internal/server/protodoc"
	"code-contract/models"

	"gorm.io/gorm"
)

// UpOptions 控制沙箱初始化与一键联动流程。
type UpOptions struct {
	ScaleCount     int
	RunSync        bool
	ActivateGroupA bool
}

// UpResult 汇总沙箱初始化与同步激活输出。
type UpResult struct {
	ReposInitialized int                            `json:"repos_initialized"`
	SharedBaseDir    string                         `json:"shared_base_dir"`
	SeedSummary      *SeedSummary                   `json:"seed_summary,omitempty"`
	SyncJob          *models.ProtoSyncJob           `json:"sync_job,omitempty"`
	ReconcileRun     *models.ProtoPoolReconcileRun  `json:"reconcile_run,omitempty"`
	ReconcileReport  *gitclient.PoolReconcileReport `json:"reconcile_report,omitempty"`
	ActivatedSchemas []string                       `json:"activated_schemas,omitempty"`
	Warnings         []string                       `json:"warnings,omitempty"`
}

// SandboxStatus 描述沙箱当前运行状态。
type SandboxStatus struct {
	TotalRepos        int            `json:"total_repos"`
	LocalCachesReady  int            `json:"local_caches_ready"`
	ProtoStatusCounts map[string]int `json:"proto_status_counts"`
	CompiledSchemas   int            `json:"compiled_schemas"`
	ActiveSchemas     int            `json:"active_schemas"`
	DocSymbols        int64          `json:"doc_symbols"`
	MRCheckRuns       int64          `json:"mr_check_runs"`
}

// Up 一键生成本地 Git 缓存、播种平台数据，并按需执行同步、全池协调与 Group A 描述集激活。
func Up(ctx context.Context, db *gorm.DB, cfg *config.Config, opts UpOptions) (*UpResult, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if cfg == nil {
		cfg = &config.AppConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}

	codesDir := cfg.Storage.GetCodesDir()
	baseDir, err := WriteSharedBaseDir(codesDir)
	if err != nil {
		return nil, err
	}

	fixtures := StandardFixtures()
	if opts.ScaleCount > 0 {
		fixtures = append(fixtures, BuildScaleFixtures(opts.ScaleCount)...)
	}

	for _, fx := range fixtures {
		if _, _, gErr := InitSandboxGitRepo(codesDir, fx); gErr != nil {
			return nil, fmt.Errorf("初始化本地沙箱仓库 %s 失败: %w", fx.Name, gErr)
		}
	}

	seedSum, err := SeedSandboxDB(ctx, db, fixtures, SeedOptions{IncludeScaleRepos: opts.ScaleCount > 0})
	if err != nil {
		return nil, err
	}

	res := &UpResult{
		ReposInitialized: len(fixtures),
		SharedBaseDir:    baseDir,
		SeedSummary:      seedSum,
		Warnings:         append([]string(nil), seedSum.Warnings...),
	}

	if opts.RunSync || opts.ActivateGroupA {
		job, reconRun, reconReport, syncErr := syncSandboxBaseline(ctx, db, cfg)
		if syncErr != nil {
			res.Warnings = append(res.Warnings, syncErr.Error())
		}
		res.SyncJob = job
		res.ReconcileRun = reconRun
		res.ReconcileReport = reconReport
		if opts.ActivateGroupA {
			activated, actWarnings := activateGroupASchemas(ctx, db)
			res.ActivatedSchemas = activated
			res.Warnings = append(res.Warnings, actWarnings...)
		}
	}

	return res, nil
}

// Down 彻底卸载所有沙箱记录与本地沙箱缓存。
func Down(ctx context.Context, db *gorm.DB, cfg *config.Config) (*CleanSummary, error) {
	if cfg == nil {
		cfg = &config.AppConfig
	}
	return CleanSandboxDB(ctx, db, cfg.Storage.GetCodesDir(), cfg.Storage.GetProtosDir())
}

// Reset 先清理后重建沙箱。
func Reset(ctx context.Context, db *gorm.DB, cfg *config.Config, opts UpOptions) (*UpResult, error) {
	if _, err := Down(ctx, db, cfg); err != nil {
		return nil, err
	}
	return Up(ctx, db, cfg, opts)
}

// Status 查询当前沙箱仓库、缓存、编译描述集与活体文档状态。
func Status(ctx context.Context, db *gorm.DB, cfg *config.Config) (*SandboxStatus, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if cfg == nil {
		cfg = &config.AppConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}

	codesDir := cfg.Storage.GetCodesDir()
	var gitRepos []models.GitRepo
	if err := db.WithContext(ctx).Where("name LIKE ?", "sandbox-%").Find(&gitRepos).Error; err != nil {
		return nil, err
	}

	st := &SandboxStatus{
		TotalRepos:        len(gitRepos),
		ProtoStatusCounts: map[string]int{},
	}
	gitRepoIDs := make([]uint64, 0, len(gitRepos))
	for _, r := range gitRepos {
		gitRepoIDs = append(gitRepoIDs, r.ID)
		st.ProtoStatusCounts[r.ProtoStatus]++
		if lp, err := gitsync.ResolveRepoLocalPath(codesDir, r.RepoURL); err == nil {
			if info, statErr := os.Stat(filepath.Join(lp, ".git")); statErr == nil && info.IsDir() {
				st.LocalCachesReady++
			}
		}
	}

	if len(gitRepoIDs) > 0 {
		var schemas []models.ProtoSchema
		_ = db.WithContext(ctx).Select("id, is_active").Where("git_repo_id IN ?", gitRepoIDs).Find(&schemas).Error
		schemaIDs := make([]uint64, 0, len(schemas))
		for _, s := range schemas {
			st.CompiledSchemas++
			if s.IsActive {
				st.ActiveSchemas++
			}
			schemaIDs = append(schemaIDs, s.ID)
		}
		if len(schemaIDs) > 0 {
			_ = db.WithContext(ctx).Model(&models.ProtoDocSymbol{}).Where("schema_id IN ?", schemaIDs).Count(&st.DocSymbols).Error
		}
		_ = db.WithContext(ctx).Model(&models.MRCheckRun{}).Where("git_repo_id IN ?", gitRepoIDs).Count(&st.MRCheckRuns).Error
	}
	return st, nil
}

func syncSelectedSandboxRepos(ctx context.Context, db *gorm.DB, cfg *config.Config, repoIDs []uint64) (*models.ProtoSyncJob, error) {
	if len(repoIDs) == 0 {
		return nil, nil
	}
	reg, _ := decoder.NewRegistry(db)
	sched := gitclient.NewSyncScheduler(db, reg, nil, cfg)
	return gitclient.RunSyncAllJob(ctx, db, sched, cfg, gitclient.SyncAllRequest{
		Scope:       models.SyncJobScopeSelected,
		CachePolicy: models.SyncCachePolicyCacheOnly,
		RepoIDs:     repoIDs,
		TriggeredBy: "proto-sandbox",
		WaitSync:    true,
	})
}

func syncAndActivateGroupABaseline(ctx context.Context, db *gorm.DB, cfg *config.Config) (*models.ProtoSyncJob, *models.ProtoPoolReconcileRun, error) {
	job, run, _, err := syncSandboxBaseline(ctx, db, cfg)
	if err != nil {
		return job, run, err
	}
	_, _ = activateGroupASchemas(ctx, db)
	return job, run, nil
}

func syncSandboxBaseline(
	ctx context.Context,
	db *gorm.DB,
	cfg *config.Config,
) (*models.ProtoSyncJob, *models.ProtoPoolReconcileRun, *gitclient.PoolReconcileReport, error) {
	var sandboxRepos []models.GitRepo
	if err := db.WithContext(ctx).Where("name LIKE ?", "sandbox-%").Order("id ASC").Find(&sandboxRepos).Error; err != nil {
		return nil, nil, nil, err
	}
	repoIDs := make([]uint64, 0, len(sandboxRepos))
	for _, r := range sandboxRepos {
		repoIDs = append(repoIDs, r.ID)
	}

	job, err := syncSelectedSandboxRepos(ctx, db, cfg, repoIDs)
	if err != nil {
		return nil, nil, nil, err
	}

	// 将 sandbox-common-base 标记为 BASE_LIB，sandbox-order-gateway 标记为 GATEWAY
	markSandboxBundleCategory(ctx, db, "sandbox-common-base", models.ProtoCategoryBaseLib)
	markSandboxBundleCategory(ctx, db, "sandbox-order-gateway", models.ProtoCategoryGateway)

	reconRun, reconReport, reconErr := gitclient.RunPoolReconcile(ctx, db, gitclient.PoolReconcileOptions{
		CodesDir:    cfg.Storage.GetCodesDir(),
		ProtosDir:   cfg.Storage.GetProtosDir(),
		TriggeredBy: "proto-sandbox",
	})
	if reconErr != nil {
		return job, reconRun, reconReport, reconErr
	}

	// 全池协调后再执行一次二次同步以确保 Stage D 编译使用协调后完整的跨仓坐标并生成最新 Schema
	_, _ = syncSelectedSandboxRepos(ctx, db, cfg, repoIDs)
	markSandboxBundleCategory(ctx, db, "sandbox-common-base", models.ProtoCategoryBaseLib)
	markSandboxBundleCategory(ctx, db, "sandbox-order-gateway", models.ProtoCategoryGateway)

	return job, reconRun, reconReport, nil
}

func markSandboxBundleCategory(ctx context.Context, db *gorm.DB, repoName, category string) {
	var repo models.GitRepo
	if err := db.WithContext(ctx).Where("name = ?", repoName).First(&repo).Error; err != nil {
		return
	}
	_ = db.WithContext(ctx).Model(&models.GitProtoBundle{}).Where("git_repo_id = ?", repo.ID).Update("category", category).Error
	_ = db.WithContext(ctx).Model(&models.ProtoFile{}).Where("git_repo_id = ?", repo.ID).Update("category", category).Error
	_ = db.WithContext(ctx).Model(&models.ProtoSchema{}).Where("git_repo_id = ?", repo.ID).Update("category", category).Error
}

func activateGroupASchemas(ctx context.Context, db *gorm.DB) ([]string, []string) {
	groupANames := []string{
		"sandbox-common-base",
		"sandbox-account-svc",
		"sandbox-inventory-svc",
		"sandbox-order-gateway",
		"sandbox-payment-alipay",
		"sandbox-payment-wechat",
	}
	var repos []models.GitRepo
	if err := db.WithContext(ctx).Where("name IN ?", groupANames).Find(&repos).Error; err != nil {
		return nil, []string{err.Error()}
	}
	repoByName := make(map[string]models.GitRepo, len(repos))
	for _, r := range repos {
		repoByName[r.Name] = r
	}

	var orderedSchemaIDs []uint64
	for _, name := range groupANames {
		r, ok := repoByName[name]
		if !ok {
			continue
		}
		var b models.GitProtoBundle
		if err := db.WithContext(ctx).Where("git_repo_id = ?", r.ID).Order("id ASC").First(&b).Error; err == nil && b.LastSchemaID != nil && *b.LastSchemaID > 0 {
			orderedSchemaIDs = append(orderedSchemaIDs, *b.LastSchemaID)
			continue
		}
		var sc models.ProtoSchema
		if err := db.WithContext(ctx).Where("git_repo_id = ?", r.ID).Order("version DESC, id DESC").First(&sc).Error; err == nil && sc.ID > 0 {
			orderedSchemaIDs = append(orderedSchemaIDs, sc.ID)
		}
	}
	if len(orderedSchemaIDs) == 0 {
		return nil, []string{"Group A 尚未编译出可激活的 ProtoSchema"}
	}

	reg, err := decoder.NewRegistry(db)
	if err != nil {
		return nil, []string{fmt.Sprintf("初始化 DecoderRegistry 失败: %v", err)}
	}

	var activatedNames []string
	var warnings []string

	// 先尝试批量激活（按依赖顺序）；若遇手工上传 demo.pb 的服务名冲突，逐个兜底激活未冲突的 Schema
	schemas, _, batchErr := reg.BatchActivate(orderedSchemaIDs, decoder.BatchActivateOptions{
		AllowBreaking:  true,
		BreakingReason: "proto-sandbox initial group A activation",
	})
	if batchErr == nil {
		for i := range schemas {
			_, _ = protodoc.RebuildForSchema(db, &schemas[i])
			activatedNames = append(activatedNames, schemas[i].Name)
		}
		return activatedNames, nil
	}

	warnings = append(warnings, fmt.Sprintf("批量激活遇到冲突 (%v)，切换为逐个安全激活", batchErr))
	for _, sid := range orderedSchemaIDs {
		_, actSchema, _, actErr := reg.ActivateSchemaVersion(sid, decoder.BatchActivateOptions{
			AllowBreaking:  true,
			BreakingReason: "proto-sandbox initial group A activation",
		})
		if actErr != nil {
			warnings = append(warnings, fmt.Sprintf("跳过激活 schema#%d: %v", sid, actErr))
			continue
		}
		if actSchema != nil {
			_, _ = protodoc.RebuildForSchema(db, actSchema)
			activatedNames = append(activatedNames, actSchema.Name)
		}
	}
	return activatedNames, warnings
}
