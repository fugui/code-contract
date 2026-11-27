package gitclient

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"code-common/backend/gitsync"
	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/models"

	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

// SyncScheduler 提供定时增量同步、并发 Worker 池、按 Git 服务器限速、singleflight 去重与 LOCAL_CACHE_ONLY 变更检测 (ADR-26 §2.7)。
type SyncScheduler struct {
	db             *gorm.DB
	reg            *decoder.Registry
	broadcaster    EventBroadcaster
	jobBroadcaster SyncJobBroadcaster
	cfg            *config.Config

	sfGroup     singleflight.Group
	sem         chan struct{}
	hostMu      sync.Mutex
	hostLastHit map[string]time.Time
	hostMinGap  time.Duration
}

// SetJobBroadcaster 注册批处理同步任务进度 SSE 广播器。
func (s *SyncScheduler) SetJobBroadcaster(b SyncJobBroadcaster) {
	if s == nil {
		return
	}
	s.jobBroadcaster = b
}

// NewSyncScheduler 创建同步调度器。
func NewSyncScheduler(db *gorm.DB, reg *decoder.Registry, broadcaster EventBroadcaster, cfg *config.Config) *SyncScheduler {
	if cfg == nil {
		cfg = &config.AppConfig
	}
	conc := cfg.ProtoCollect.SyncConcurrency
	if conc <= 0 {
		conc = 4
	}
	return &SyncScheduler{
		db:          db,
		reg:         reg,
		broadcaster: broadcaster,
		cfg:         cfg,
		sem:         make(chan struct{}, conc),
		hostLastHit: make(map[string]time.Time),
		hostMinGap:  100 * time.Millisecond,
	}
}

// TriggerRepoSync 通过 singleflight + worker 池并发信号量 + 按 host 限速执行单仓同步。
func (s *SyncScheduler) TriggerRepoSync(ctx context.Context, repoID uint64, opts PipelineOptions) (*PipelineRunResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("SyncScheduler 未初始化")
	}
	key := fmt.Sprintf("repo-%d-%s-%s-%v", repoID, strings.TrimSpace(opts.Branch), strings.TrimSpace(opts.CachePolicy), opts.ForceRecompile)
	val, err, _ := s.sfGroup.Do(key, func() (any, error) {
		select {
		case s.sem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-s.sem }()

		var repo models.GitRepo
		if err := s.db.WithContext(ctx).First(&repo, repoID).Error; err != nil {
			return nil, err
		}
		s.throttleByHost(ctx, repo.RepoURL)

		if opts.CodesDir == "" {
			opts.CodesDir = s.cfg.Storage.GetCodesDir()
		}
		if opts.ProtosDir == "" {
			opts.ProtosDir = s.cfg.Storage.GetProtosDir()
		}
		if !opts.SharedWriteEnabled {
			opts.SharedWriteEnabled = s.cfg.Storage.SharedWriteEnabled
		}
		opts.TriggerBlastRadius = true

		// 统一走共享缓存四段式流水线
		return RunFourStagePipeline(ctx, s.db, s.reg, s.broadcaster, repoID, opts)
	})
	if err != nil {
		return nil, err
	}
	res, _ := val.(*PipelineRunResult)
	return res, nil
}

// BatchTriggerSync 并发调度批量仓库同步（自动分批限流，避免与 code-shield 高峰争抢写锁）。
func (s *SyncScheduler) BatchTriggerSync(ctx context.Context, repoIDs []uint64, opts PipelineOptions) map[uint64]error {
	errs := make(map[uint64]error)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, rid := range repoIDs {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()
			if _, err := s.TriggerRepoSync(ctx, id, opts); err != nil {
				mu.Lock()
				errs[id] = err
				mu.Unlock()
			}
		}(rid)
	}
	wg.Wait()
	return errs
}

// Start 启动定时增量调度与目录树对账后台协程。
func (s *SyncScheduler) Start(ctx context.Context) {
	if s == nil || s.db == nil {
		return
	}
	interval := parseCronIntervalFallback(s.cfg.ProtoCollect.SyncCron)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		treeTicker := time.NewTicker(15 * time.Minute)
		defer treeTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runPollCycle(ctx)
			case <-treeTicker.C:
				mgr := NewProtoTreeManager(s.cfg.Storage.GetProtosDir())
				if rebuilt, pruned, err := mgr.ReconcileAll(ctx, s.db); err != nil {
					log.Printf("[ProtoTreeReconciler] 巡检失败: %v", err)
				} else if rebuilt > 0 || pruned > 0 {
					log.Printf("[ProtoTreeReconciler] 巡检完成: 重建 %d 仓目录树, 清理 %d 个孤儿条目", rebuilt, pruned)
				}
			}
		}
	}()
}

func (s *SyncScheduler) runPollCycle(ctx context.Context) {
	if _, _, _, err := AlignPlatformRepositories(ctx, s.db); err != nil {
		log.Printf("[SyncScheduler] 对齐平台 repositories 失败: %v", err)
	}

	var repos []models.GitRepo
	if err := s.db.WithContext(ctx).
		Where("sync_enabled = true").
		Order("id ASC").
		Find(&repos).Error; err != nil {
		return
	}
	var toSync []uint64
	codesDir := s.cfg.Storage.GetCodesDir()
	for _, r := range repos {
		// 定时巡检默认仅同步已有缓存（CACHE_ONLY），避免缺失/无权限仓引发拉取风暴
		changed, err := s.hasLocalCacheCommitChanged(ctx, &r, codesDir)
		if err != nil {
			if errors.Is(err, ErrLocalCacheMissing) && r.ProtoStatus != models.GitRepoProtoStatusNoLocalCache {
				_ = s.db.Model(&r).Update("proto_status", models.GitRepoProtoStatusNoLocalCache).Error
			}
			continue
		}
		if !changed && r.ProtoStatus != models.GitRepoProtoStatusPending && r.ProtoStatus != models.GitRepoProtoStatusNoLocalCache {
			continue
		}
		toSync = append(toSync, r.ID)
	}
	if len(toSync) > 0 {
		s.BatchTriggerSync(ctx, toSync, PipelineOptions{
			CodesDir:              codesDir,
			ProtosDir:             s.cfg.Storage.GetProtosDir(),
			SharedWriteEnabled:    s.cfg.Storage.SharedWriteEnabled,
			CachePolicy:           models.SyncCachePolicyCacheOnly,
			SkipIfCommitUnchanged: true,
			TriggerBlastRadius:    true,
		})
	}
}

func (s *SyncScheduler) hasLocalCacheCommitChanged(ctx context.Context, repo *models.GitRepo, codesDir string) (bool, error) {
	localPath, _, err := ResolveEffectiveRepoLocalPathWithPolicy(repo, codesDir, models.SyncCachePolicyCacheOnly, s.cfg.Storage.SharedWriteEnabled)
	if err != nil {
		return false, err
	}
	if _, statErr := os.Stat(filepath.Join(localPath, ".git")); statErr != nil {
		return false, fmt.Errorf("%w: %v", ErrLocalCacheMissing, statErr)
	}
	lk, err := gitsync.AcquireRepoLock(ctx, codesDir, localPath, gitsync.LockShared)
	if err != nil {
		return false, err
	}
	defer func() { _ = lk.Release() }()

	_, fullRef, err := gitsync.ResolveLocalRef(ctx, localPath, "refs/remotes/origin", gitsync.ResolveBranchDefault(repo.DefaultBranch))
	if err != nil {
		_, fullRef, err = gitsync.ResolveLocalRef(ctx, localPath, "refs/contract/heads", gitsync.ResolveBranchDefault(repo.DefaultBranch))
		if err != nil {
			return false, fmt.Errorf("%w: %v", ErrLocalCacheMissing, err)
		}
	}
	meta, err := gitsync.ReadCommitMeta(ctx, localPath, fullRef)
	if err != nil {
		return false, err
	}
	return meta.CommitSHA != "" && meta.CommitSHA != repo.LastCollectedCommit, nil
}

func (s *SyncScheduler) throttleByHost(ctx context.Context, rawURL string) {
	host := extractGitHost(rawURL)
	if host == "" {
		return
	}
	s.hostMu.Lock()
	last := s.hostLastHit[host]
	now := time.Now()
	wait := time.Duration(0)
	if !last.IsZero() && now.Sub(last) < s.hostMinGap {
		wait = s.hostMinGap - now.Sub(last)
	}
	s.hostLastHit[host] = now.Add(wait)
	s.hostMu.Unlock()

	if wait > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

func extractGitHost(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	if !strings.Contains(trimmed, "://") && strings.Contains(trimmed, "@") && strings.Contains(trimmed, ":") {
		at := strings.IndexByte(trimmed, '@')
		col := strings.IndexByte(trimmed, ':')
		if col > at+1 {
			return strings.ToLower(trimmed[at+1 : col])
		}
	}
	if u, err := url.Parse(trimmed); err == nil {
		return strings.ToLower(u.Host)
	}
	return ""
}

func parseCronIntervalFallback(cronExpr string) time.Duration {
	// 支持常见 */N * * * * 形式，其余回退 30 分钟
	fields := strings.Fields(strings.TrimSpace(cronExpr))
	if len(fields) >= 5 && strings.HasPrefix(fields[0], "*/") {
		if mins, err := strconv.Atoi(strings.TrimPrefix(fields[0], "*/")); err == nil && mins > 0 {
			return time.Duration(mins) * time.Minute
		}
	}
	return 30 * time.Minute
}
