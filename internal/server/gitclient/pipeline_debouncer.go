package gitclient

import (
	"context"
	"log"
	"sync"
	"time"

	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/models"

	"gorm.io/gorm"
)

// PipelineDebouncer 对 MR 合入触发的增量流水线按仓库 ID 进行去抖合并 (ADR-29 §2.3.2 / §6.4)。
type PipelineDebouncer struct {
	mu       sync.Mutex
	timers   map[uint64]*time.Timer
	debounce time.Duration
	onFire   func(repoID uint64)
}

// NewPipelineDebouncer 创建 MR 合入增量流水线去抖器。
func NewPipelineDebouncer(debounce time.Duration, onFire func(repoID uint64)) *PipelineDebouncer {
	if debounce <= 0 {
		debounce = 300 * time.Second
	}
	return &PipelineDebouncer{
		timers:   make(map[uint64]*time.Timer),
		debounce: debounce,
		onFire:   onFire,
	}
}

// EnqueueMRMerge 按 repoID 合并去抖窗口内的多次 MR 合入事件，窗口结束触发一次 from_stage=RECONCILE 增量流水线。
func (d *PipelineDebouncer) EnqueueMRMerge(repoID uint64) {
	if d == nil || repoID == 0 || d.onFire == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, ok := d.timers[repoID]; ok && existing != nil {
		existing.Stop()
	}
	d.timers[repoID] = time.AfterFunc(d.debounce, func() {
		d.mu.Lock()
		delete(d.timers, repoID)
		d.mu.Unlock()
		d.onFire(repoID)
	})
}

// PendingCount 返回当前去抖队列中等待触发的仓库数（用于测试与可观测）。
func (d *PipelineDebouncer) PendingCount() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.timers)
}

// StopAll 停止所有未触发的去抖定时器（进程优雅退出或测试清理时调用）。
func (d *PipelineDebouncer) StopAll() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, t := range d.timers {
		if t != nil {
			t.Stop()
		}
		delete(d.timers, id)
	}
}

var (
	globalPipelineDebouncerMu sync.RWMutex
	globalPipelineDebouncer   *PipelineDebouncer
)

// InitGlobalPipelineDebouncer 初始化全局 MR 合入增量流水线去抖调度器。
func InitGlobalPipelineDebouncer(db *gorm.DB, sched *SyncScheduler, reg *decoder.Registry, cfg *config.Config, progressFn func(PipelineProgressEvent)) *PipelineDebouncer {
	if cfg == nil {
		cfg = &config.AppConfig
	}
	sec := cfg.ProtoPipeline.MRMergeDebounceSec
	if sec <= 0 {
		sec = 300
	}
	d := NewPipelineDebouncer(time.Duration(sec)*time.Second, func(repoID uint64) {
		if db == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		_, err := RunFullProtoPipeline(ctx, db, sched, reg, cfg, FullPipelineRunRequest{
			FromStage:     models.PipelineStageReconcile,
			TriggerSource: models.PipelineTriggerMRMerge,
			TriggeredBy:   "mr-merge-debouncer",
			WaitSync:      true,
			ProgressFn:    progressFn,
		})
		if err != nil {
			log.Printf("[PipelineDebouncer] 仓库 #%d MR 合入增量流水线触发失败: %v", repoID, err)
		}
	})
	globalPipelineDebouncerMu.Lock()
	if globalPipelineDebouncer != nil {
		globalPipelineDebouncer.StopAll()
	}
	globalPipelineDebouncer = d
	globalPipelineDebouncerMu.Unlock()
	return d
}

// SetGlobalPipelineDebouncer 允许测试注入自定义去抖器。
func SetGlobalPipelineDebouncer(d *PipelineDebouncer) {
	globalPipelineDebouncerMu.Lock()
	globalPipelineDebouncer = d
	globalPipelineDebouncerMu.Unlock()
}

// GetGlobalPipelineDebouncer 返回全局去抖器。
func GetGlobalPipelineDebouncer() *PipelineDebouncer {
	globalPipelineDebouncerMu.RLock()
	defer globalPipelineDebouncerMu.RUnlock()
	return globalPipelineDebouncer
}

// EnqueueGlobalMRMergePipeline 将指定仓库的 MR 合入事件投递至全局去抖调度器。
func EnqueueGlobalMRMergePipeline(repoID uint64) bool {
	d := GetGlobalPipelineDebouncer()
	if d == nil {
		return false
	}
	d.EnqueueMRMerge(repoID)
	return true
}
