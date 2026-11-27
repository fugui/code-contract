package gitclient

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"
)

const pipelineAdvisoryLockKey = "contract_proto_pipeline"

var (
	// ErrPipelineLocked 表示全局四阶段流水线互斥锁当前已被另一项作业持有。
	ErrPipelineLocked = errors.New("全局 Proto 流水线互斥锁已被占用")
	// ErrPipelineLockTimeout 表示等待全局流水线互斥锁超时。
	ErrPipelineLockTimeout = errors.New("等待全局 Proto 流水线互斥锁超时")
)

// PipelineLockHolder 描述当前持有全局流水线互斥锁的作业信息。
type PipelineLockHolder struct {
	JobType    string    `json:"job_type"` // PIPELINE_RUN | SYNC_ALL | RECONCILE | PUBLISH | REPO_SYNC | MR_MERGE | RENORMALIZE
	JobID      uint64    `json:"job_id,omitempty"`
	Holder     string    `json:"holder"`
	AcquiredAt time.Time `json:"acquired_at"`
}

// PipelineLockError 携带当前持锁摘要的结构化锁冲突错误。
type PipelineLockError struct {
	Cause  error
	Holder *PipelineLockHolder
}

func (e *PipelineLockError) Error() string {
	if e.Holder != nil && e.Holder.JobType != "" {
		if e.Holder.JobID > 0 {
			return fmt.Sprintf("%s（当前运行中作业: %s #%d，触发人: %s）", e.Cause.Error(), e.Holder.JobType, e.Holder.JobID, e.Holder.Holder)
		}
		return fmt.Sprintf("%s（当前运行中作业: %s，触发人: %s）", e.Cause.Error(), e.Holder.JobType, e.Holder.Holder)
	}
	return e.Cause.Error()
}

func (e *PipelineLockError) Unwrap() error { return e.Cause }

var (
	activePipelineLockMu     sync.Mutex
	activePipelineLockHolder *PipelineLockHolder
)

// PipelineLock 代表绑定在独占物理 *sql.Conn 上的 PostgreSQL 会话级咨询锁。
type PipelineLock struct {
	conn   *sql.Conn
	holder PipelineLockHolder
	once   sync.Once
}

// Release 解锁并归还独占数据库连接。
func (l *PipelineLock) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		activePipelineLockMu.Lock()
		if activePipelineLockHolder != nil &&
			activePipelineLockHolder.JobType == l.holder.JobType &&
			activePipelineLockHolder.AcquiredAt.Equal(l.holder.AcquiredAt) {
			activePipelineLockHolder = nil
		}
		activePipelineLockMu.Unlock()

		if l.conn != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = l.conn.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext($1))`, pipelineAdvisoryLockKey)
			cancel()
			_ = l.conn.Close()
		}
	})
}

// TryAcquirePipelineLock 非阻塞尝试获取全局流水线会话级咨询锁。
func TryAcquirePipelineLock(ctx context.Context, db *gorm.DB, holder PipelineLockHolder) (*PipelineLock, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, err
	}

	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, pipelineAdvisoryLockKey).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !acquired {
		_ = conn.Close()
		cur := GetActivePipelineLockHolder()
		return nil, &PipelineLockError{
			Cause:  ErrPipelineLocked,
			Holder: cur,
		}
	}

	if holder.AcquiredAt.IsZero() {
		holder.AcquiredAt = time.Now().UTC()
	}
	activePipelineLockMu.Lock()
	copyHolder := holder
	activePipelineLockHolder = &copyHolder
	activePipelineLockMu.Unlock()

	return &PipelineLock{
		conn:   conn,
		holder: holder,
	}, nil
}

// AcquirePipelineLockWithTimeout 在指定超时时间内轮询等待全局流水线互斥锁（供单仓同步与 MR 合入落库使用）。
func AcquirePipelineLockWithTimeout(ctx context.Context, db *gorm.DB, holder PipelineLockHolder, timeout time.Duration) (*PipelineLock, error) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timeout)
	for {
		lk, err := TryAcquirePipelineLock(ctx, db, holder)
		if err == nil {
			return lk, nil
		}
		if !errors.Is(err, ErrPipelineLocked) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, &PipelineLockError{
				Cause:  ErrPipelineLockTimeout,
				Holder: GetActivePipelineLockHolder(),
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// GetActivePipelineLockHolder 返回本进程当前已知的锁持有作业信息（未持有则返回 nil）。
func GetActivePipelineLockHolder() *PipelineLockHolder {
	activePipelineLockMu.Lock()
	defer activePipelineLockMu.Unlock()
	if activePipelineLockHolder == nil {
		return nil
	}
	cp := *activePipelineLockHolder
	return &cp
}

// SetActivePipelineLockJobID 在获取锁创建持久化任务记录后补充更新其 JobID。
func SetActivePipelineLockJobID(jobID uint64) {
	activePipelineLockMu.Lock()
	defer activePipelineLockMu.Unlock()
	if activePipelineLockHolder != nil {
		activePipelineLockHolder.JobID = jobID
	}
}
