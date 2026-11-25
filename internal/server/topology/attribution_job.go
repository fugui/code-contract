package topology

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"code-contract/internal/server/decoder"
	"code-contract/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// EventBroadcaster 解耦 SSE 广播接口，避免 topology -> hub 循环依赖
type EventBroadcaster interface {
	Broadcast(eventType string, data any)
}

var (
	ErrAttributionJobNotFound  = errors.New("归因回补作业不存在")
	ErrAttributionJobForbidden = errors.New("无权操作他人的归因回补作业")
	ErrAttributionJobTerminal  = errors.New("归因回补作业已处于终态，无法取消")
)

type attributionJobScope struct {
	ServiceNames    []string `json:"service_names"`
	TriggerType     string   `json:"trigger_type,omitempty"`
	TargetID        uint64   `json:"target_id,omitempty"`
	MatchedCount    int64    `json:"matched_count,omitempty"`
	UnresolvedCount int64    `json:"unresolved_count,omitempty"`
	ConflictCount   int64    `json:"conflict_count,omitempty"`
}

// AttributionJobDTO 阶段 2 历史归因回补作业进度 DTO
type AttributionJobDTO struct {
	ID              uint64    `json:"id"`
	JobType         string    `json:"job_type"`
	Status          string    `json:"status"`
	Processed       int64     `json:"processed"`
	Total           int64     `json:"total"`
	CursorSessionID uint64    `json:"cursor_session_id"`
	MatchedCount    int64     `json:"matched_count"`
	UnresolvedCount int64     `json:"unresolved_count"`
	ConflictCount   int64     `json:"conflict_count"`
	Error           string    `json:"error"`
	CreatedBy       uint      `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ServiceNames    []string  `json:"service_names"`
}

// AttributionJobEventDTO SSE 事件 attribution_job.updated 载荷
type AttributionJobEventDTO struct {
	JobID           uint64 `json:"job_id"`
	TriggerType     string `json:"trigger_type"`
	TargetID        uint64 `json:"target_id"`
	Status          string `json:"status"`
	Processed       int64  `json:"processed"`
	Total           int64  `json:"total"`
	CursorSessionID uint64 `json:"cursor_session_id"`
	MatchedCount    int64  `json:"matched_count"`
	UnresolvedCount int64  `json:"unresolved_count"`
	ConflictCount   int64  `json:"conflict_count"`
	LastError       string `json:"last_error,omitempty"`
}

func parseAttributionJobScope(raw datatypes.JSON) attributionJobScope {
	var s attributionJobScope
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &s)
	}
	if s.ServiceNames == nil {
		s.ServiceNames = []string{}
	}
	return s
}

func toAttributionJobDTO(job *models.AttributionJob) *AttributionJobDTO {
	scope := parseAttributionJobScope(job.ScopeJSON)
	return &AttributionJobDTO{
		ID:              job.ID,
		JobType:         job.JobType,
		Status:          job.Status,
		Processed:       job.Processed,
		Total:           job.Total,
		CursorSessionID: job.CursorSessionID,
		MatchedCount:    scope.MatchedCount,
		UnresolvedCount: scope.UnresolvedCount,
		ConflictCount:   scope.ConflictCount,
		Error:           job.Error,
		CreatedBy:       job.CreatedBy,
		CreatedAt:       job.CreatedAt,
		UpdatedAt:       job.UpdatedAt,
		ServiceNames:    scope.ServiceNames,
	}
}

func isTerminalAttrJobStatus(status string) bool {
	return status == models.AttrJobStatusDone ||
		status == models.AttrJobStatusFailed ||
		status == models.AttrJobStatusCancelled
}

// GetAttributionJobDTO 查询单个归因回补作业的实时进度
func GetAttributionJobDTO(ctx context.Context, db *gorm.DB, jobID uint64) (*AttributionJobDTO, error) {
	if db == nil {
		return nil, errors.New("nil db")
	}
	var job models.AttributionJob
	if err := db.WithContext(ctx).First(&job, jobID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAttributionJobNotFound
		}
		return nil, err
	}
	return toAttributionJobDTO(&job), nil
}

// CancelAttributionJob 创建人或管理员请求安全断点取消 REATTRIBUTE 回补任务
func CancelAttributionJob(ctx context.Context, db *gorm.DB, op Operator, jobID uint64) (*AttributionJobDTO, error) {
	if db == nil {
		return nil, errors.New("nil db")
	}
	var job models.AttributionJob
	if err := db.WithContext(ctx).First(&job, jobID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAttributionJobNotFound
		}
		return nil, err
	}
	if !op.IsAdmin && (op.UserID == 0 || job.CreatedBy != op.UserID) {
		return nil, ErrAttributionJobForbidden
	}
	if isTerminalAttrJobStatus(job.Status) {
		return nil, ErrAttributionJobTerminal
	}

	upd := db.WithContext(ctx).Model(&models.AttributionJob{}).
		Where("id = ? AND status IN ?", job.ID, []string{models.AttrJobStatusPending, models.AttrJobStatusRunning}).
		Update("status", models.AttrJobStatusCancelled)
	if upd.Error != nil {
		return nil, upd.Error
	}
	if upd.RowsAffected == 0 {
		// 并发下刚好流转至终态
		if err := db.WithContext(ctx).First(&job, jobID).Error; err != nil {
			return nil, err
		}
		if isTerminalAttrJobStatus(job.Status) {
			return nil, ErrAttributionJobTerminal
		}
	}

	if err := db.WithContext(ctx).First(&job, jobID).Error; err != nil {
		return nil, err
	}
	return toAttributionJobDTO(&job), nil
}

// AttributionWorkerOptions 归因回补 Worker 运行参数
type AttributionWorkerOptions struct {
	BatchInterval time.Duration
	PollInterval  time.Duration
	OnBatchDone   func(jobID, sessionID uint64)
}

// AttributionJobWorker 可续跑、可取消的历史归因回补后台 Worker
type AttributionJobWorker struct {
	db            *gorm.DB
	dec           *decoder.Registry
	broadcaster   EventBroadcaster
	batchInterval time.Duration
	pollInterval  time.Duration
	onBatchDone   func(jobID, sessionID uint64)
	notifyCh      chan struct{}
	runMu         sync.Mutex
}

// NewAttributionJobWorker 创建归因回补 Worker 实例
func NewAttributionJobWorker(db *gorm.DB, dec *decoder.Registry, broadcaster EventBroadcaster, opts ...AttributionWorkerOptions) *AttributionJobWorker {
	batchInt := 50 * time.Millisecond
	pollInt := 2 * time.Second
	var onBatch func(jobID, sessionID uint64)
	if len(opts) > 0 {
		if opts[0].BatchInterval > 0 {
			batchInt = opts[0].BatchInterval
		}
		if opts[0].PollInterval > 0 {
			pollInt = opts[0].PollInterval
		}
		onBatch = opts[0].OnBatchDone
	}
	return &AttributionJobWorker{
		db:            db,
		dec:           dec,
		broadcaster:   broadcaster,
		batchInterval: batchInt,
		pollInterval:  pollInt,
		onBatchDone:   onBatch,
		notifyCh:      make(chan struct{}, 1),
	}
}

// Notify 唤醒后台 Worker 立即检查新入队的 PENDING 作业
func (w *AttributionJobWorker) Notify() {
	if w == nil || w.notifyCh == nil {
		return
	}
	select {
	case w.notifyCh <- struct{}{}:
	default:
	}
}

// RecoverRunningJobs 启动恢复：将上次进程中断遗留的 RUNNING REATTRIBUTE 作业重置为 PENDING 以从 cursor_session_id 续跑
func (w *AttributionJobWorker) RecoverRunningJobs(ctx context.Context) error {
	if w == nil || w.db == nil {
		return nil
	}
	return w.db.WithContext(ctx).Model(&models.AttributionJob{}).
		Where("job_type = ? AND status = ?", models.AttrJobTypeReattribute, models.AttrJobStatusRunning).
		Update("status", models.AttrJobStatusPending).Error
}

// Start 启动后台限速消费循环，直至 ctx 取消
func (w *AttributionJobWorker) Start(ctx context.Context) {
	if w == nil || w.db == nil {
		return
	}
	_ = w.RecoverRunningJobs(ctx)

	go func() {
		ticker := time.NewTicker(w.pollInterval)
		defer ticker.Stop()

		for {
			for {
				if ctx.Err() != nil {
					return
				}
				didWork, err := w.ProcessNextPending(ctx)
				if err != nil || !didWork {
					break
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-w.notifyCh:
			case <-ticker.C:
			}
		}
	}()
}

// ProcessNextPending 取一条最早的 PENDING REATTRIBUTE 作业并执行
func (w *AttributionJobWorker) ProcessNextPending(ctx context.Context) (bool, error) {
	if w == nil || w.db == nil {
		return false, nil
	}
	var job models.AttributionJob
	err := w.db.WithContext(ctx).
		Where("job_type = ? AND status = ?", models.AttrJobTypeReattribute, models.AttrJobStatusPending).
		Order("id ASC").
		First(&job).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, w.RunJob(ctx, job.ID)
}

// RunJob 按 RecordingSession 游标分批执行指定归因回补作业，支持取消与断点续跑
func (w *AttributionJobWorker) RunJob(ctx context.Context, jobID uint64) error {
	if w == nil || w.db == nil {
		return nil
	}
	w.runMu.Lock()
	defer w.runMu.Unlock()

	var job models.AttributionJob
	if err := w.db.WithContext(ctx).First(&job, jobID).Error; err != nil {
		return err
	}
	if isTerminalAttrJobStatus(job.Status) {
		return nil
	}

	scope := parseAttributionJobScope(job.ScopeJSON)
	queryNames := expandAttributionServiceNames(scope.ServiceNames)

	// 命中 idx_contract_tr_session_svc 查询含目标服务的所有有序 session_id
	var allSessions []uint64
	if len(queryNames) > 0 {
		if err := w.db.WithContext(ctx).
			Table("contract_traffic_records").
			Distinct("session_id").
			Where("service_name IN ?", queryNames).
			Order("session_id ASC").
			Pluck("session_id", &allSessions).Error; err != nil {
			return err
		}
	}

	total := job.Total
	if total == 0 {
		total = int64(len(allSessions))
	}

	// 原子抢占运行态
	claimRes := w.db.WithContext(ctx).Model(&models.AttributionJob{}).
		Where("id = ? AND status IN ?", job.ID, []string{models.AttrJobStatusPending, models.AttrJobStatusRunning}).
		Updates(map[string]interface{}{
			"status": models.AttrJobStatusRunning,
			"total":  total,
		})
	if claimRes.Error != nil {
		return claimRes.Error
	}
	if claimRes.RowsAffected == 0 {
		return nil
	}
	job.Status = models.AttrJobStatusRunning
	job.Total = total

	pendingSessions := make([]uint64, 0, len(allSessions))
	for _, sid := range allSessions {
		if sid > job.CursorSessionID {
			pendingSessions = append(pendingSessions, sid)
		}
	}

	w.emitProgress(&job, &scope)

	for _, sid := range pendingSessions {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if cancelled, err := w.isJobCancelled(ctx, job.ID); err != nil {
			return err
		} else if cancelled {
			return nil
		}

		if err := OfflineBatchAttributionWithDecoder(ctx, w.db, sid, w.dec); err != nil {
			errMsg := err.Error()
			_ = w.db.WithContext(ctx).Model(&models.AttributionJob{}).
				Where("id = ? AND status = ?", job.ID, models.AttrJobStatusRunning).
				Updates(map[string]interface{}{
					"status": models.AttrJobStatusFailed,
					"error":  errMsg,
				}).Error
			job.Status = models.AttrJobStatusFailed
			job.Error = errMsg
			w.emitProgress(&job, &scope)
			return err
		}

		matched, unresolved, conflict := w.countSessionAttributionStats(ctx, sid, queryNames)
		scope.MatchedCount += matched
		scope.UnresolvedCount += unresolved
		scope.ConflictCount += conflict

		scopeBytes, _ := json.Marshal(scope)
		job.CursorSessionID = sid
		job.Processed++
		job.ScopeJSON = datatypes.JSON(scopeBytes)

		upd := w.db.WithContext(ctx).Model(&models.AttributionJob{}).
			Where("id = ? AND status = ?", job.ID, models.AttrJobStatusRunning).
			Updates(map[string]interface{}{
				"cursor_session_id": job.CursorSessionID,
				"processed":         job.Processed,
				"scope_json":        job.ScopeJSON,
			})
		if upd.Error != nil {
			return upd.Error
		}
		if upd.RowsAffected == 0 {
			// 若并发被取消，立即安全退出
			return nil
		}

		if w.onBatchDone != nil {
			w.onBatchDone(job.ID, sid)
		}

		// 若批间钩子或外部触发取消，携带最新状态推送并安全退出
		if cancelled, _ := w.isJobCancelled(ctx, job.ID); cancelled {
			job.Status = models.AttrJobStatusCancelled
			w.emitProgress(&job, &scope)
			return nil
		}
		w.emitProgress(&job, &scope)

		if w.batchInterval > 0 {
			timer := time.NewTimer(w.batchInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}

	// 全部会话处理完毕，仅在仍为 RUNNING 时流转至 DONE
	fin := w.db.WithContext(ctx).Model(&models.AttributionJob{}).
		Where("id = ? AND status = ?", job.ID, models.AttrJobStatusRunning).
		Update("status", models.AttrJobStatusDone)
	if fin.Error != nil {
		return fin.Error
	}
	if fin.RowsAffected > 0 {
		job.Status = models.AttrJobStatusDone
		w.emitProgress(&job, &scope)
	}
	return nil
}

func (w *AttributionJobWorker) isJobCancelled(ctx context.Context, jobID uint64) (bool, error) {
	var status string
	if err := w.db.WithContext(ctx).Model(&models.AttributionJob{}).
		Select("status").
		Where("id = ?", jobID).
		Scan(&status).Error; err != nil {
		return false, err
	}
	return status == models.AttrJobStatusCancelled, nil
}

func (w *AttributionJobWorker) countSessionAttributionStats(ctx context.Context, sessionID uint64, queryNames []string) (matched, unresolved, conflict int64) {
	if len(queryNames) == 0 {
		return 0, 0, 0
	}
	type statRow struct {
		SrcModuleID        *uint64
		DstModuleID        *uint64
		SrcAttributionMode string
	}
	var rows []statRow
	if err := w.db.WithContext(ctx).Model(&models.TrafficRecord{}).
		Select("src_module_id, dst_module_id, src_attribution_mode").
		Where("session_id = ? AND service_name IN ?", sessionID, queryNames).
		Find(&rows).Error; err != nil {
		return 0, 0, 0
	}
	for _, r := range rows {
		if r.SrcAttributionMode == AttributionProcessLevel {
			conflict++
		}
		if r.DstModuleID != nil || r.SrcModuleID != nil {
			matched++
		} else {
			unresolved++
		}
	}
	return matched, unresolved, conflict
}

func (w *AttributionJobWorker) emitProgress(job *models.AttributionJob, scope *attributionJobScope) {
	if w == nil || w.broadcaster == nil || job == nil || scope == nil {
		return
	}
	w.broadcaster.Broadcast("attribution_job.updated", AttributionJobEventDTO{
		JobID:           job.ID,
		TriggerType:     scope.TriggerType,
		TargetID:        scope.TargetID,
		Status:          job.Status,
		Processed:       job.Processed,
		Total:           job.Total,
		CursorSessionID: job.CursorSessionID,
		MatchedCount:    scope.MatchedCount,
		UnresolvedCount: scope.UnresolvedCount,
		ConflictCount:   scope.ConflictCount,
		LastError:       job.Error,
	})
}

func expandAttributionServiceNames(names []string) []string {
	seen := make(map[string]struct{}, len(names)*2)
	out := make([]string, 0, len(names)*2)
	add := func(s string) {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			return
		}
		if _, ok := seen[trimmed]; !ok {
			seen[trimmed] = struct{}{}
			out = append(out, trimmed)
		}
	}
	for _, n := range names {
		add(n)
		if stripped := StripCSGPIDSuffix(n); stripped != "" {
			add(stripped)
		}
	}
	return out
}
