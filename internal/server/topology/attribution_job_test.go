package topology

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"code-contract/models"
	"gorm.io/datatypes"
)

type memAttributionBroadcaster struct {
	mu     sync.Mutex
	events []AttributionJobEventDTO
}

func (b *memAttributionBroadcaster) Broadcast(eventType string, data any) {
	if eventType != "attribution_job.updated" {
		return
	}
	raw, _ := json.Marshal(data)
	var dto AttributionJobEventDTO
	if err := json.Unmarshal(raw, &dto); err == nil {
		b.mu.Lock()
		b.events = append(b.events, dto)
		b.mu.Unlock()
	}
}

func (b *memAttributionBroadcaster) snapshot() []AttributionJobEventDTO {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]AttributionJobEventDTO, len(b.events))
	copy(out, b.events)
	return out
}

func TestAttributionJob_BatchCursor_Cancel_ResumeAfterRestart_AndSSE(t *testing.T) {
	e := newInboxEnv(t)
	ctx := context.Background()

	// ap1 承载 modA，ap2 承载 modC；modA 依赖 svcC(ap2/modC)
	e.db.Model(&models.Module{}).Where("id = ?", e.modA.ID).Update("arch_process_id", e.ap1.ID)
	svcC := e.newClaimedSvc(t, "ajob.v1.TargetService", e.modC.ID)
	e.place(t, svcC.ID, e.proc2.ID, models.PlacementStatusActive)
	svcOther := e.newClaimedSvc(t, "ajob.v1.UnrelatedService", e.modC.ID)

	// 准备 4 个录制会话：S1/S2/S4 含目标服务待回补流量，S3 仅含无关服务（不计入目标会话）
	s1 := e.seedSession(t, "AJOB_S1", models.ScopeShared)
	s2 := e.seedSession(t, "AJOB_S2", models.ScopeShared)
	s3 := e.seedSession(t, "AJOB_S3", models.ScopeShared)
	s4 := e.seedSession(t, "AJOB_S4", models.ScopeShared)

	// S1/S2/S4 各插入 1 条 EGRESS（proc1 -> ap1 仅 modA）和 1 条 INGRESS（proc2 -> svcC），初始 src/dst_module_id 为 NULL
	e.seedTraffic(t, s1, 1, e.proc1.ID, models.TrafficDirEgress, svcC.ServiceName, nil, nil)
	e.seedTraffic(t, s1, 2, e.proc2.ID, models.TrafficDirIngress, svcC.ServiceName, nil, nil)
	e.seedTraffic(t, s2, 1, e.proc1.ID, models.TrafficDirEgress, svcC.ServiceName, nil, nil)
	e.seedTraffic(t, s3, 1, e.proc1.ID, models.TrafficDirEgress, svcOther.ServiceName, nil, nil)
	e.seedTraffic(t, s4, 1, e.proc1.ID, models.TrafficDirEgress, svcC.ServiceName, nil, nil)

	// 记录 SSE attribution_job.updated 广播事件
	broker := &memAttributionBroadcaster{}

	// 1. 创建作业 1：处理完第 1 个会话 (S1) 后主动取消 -> 验证安全退出且不覆盖 CANCELLED
	scopeBytes, _ := json.Marshal(map[string]interface{}{
		"service_names": []string{svcC.ServiceName},
		"trigger_type":  "SERVICE_CLAIM",
		"target_id":     svcC.ID,
	})
	job1 := models.AttributionJob{
		JobType:   models.AttrJobTypeReattribute,
		ScopeJSON: datatypes.JSON(scopeBytes),
		Status:    models.AttrJobStatusPending,
		CreatedBy: e.modOwner.ID,
	}
	mustCreate(t, e.db, &job1)

	var batchHits int
	worker1 := NewAttributionJobWorker(e.db, nil, broker, AttributionWorkerOptions{
		BatchInterval: 5 * time.Millisecond,
		PollInterval:  20 * time.Millisecond,
		OnBatchDone: func(jobID, sessionID uint64) {
			if jobID == job1.ID {
				batchHits++
				if batchHits == 1 {
					// 第 1 批 (S1) 结束后立即取消
					if _, cErr := CancelAttributionJob(ctx, e.db, e.op(e.modOwner, false), job1.ID); cErr != nil {
						t.Errorf("批间取消任务失败: %v", cErr)
					}
				}
			}
		},
	})

	if err := worker1.RunJob(ctx, job1.ID); err != nil {
		t.Fatalf("worker1 RunJob 失败: %v", err)
	}

	dto1, err := GetAttributionJobDTO(ctx, e.db, job1.ID)
	if err != nil {
		t.Fatalf("GetAttributionJobDTO 失败: %v", err)
	}
	if dto1.Status != models.AttrJobStatusCancelled {
		t.Fatalf("取消后状态期望 CANCELLED，实际: %+v", dto1)
	}
	if dto1.Processed != 1 || dto1.Total != 3 || dto1.CursorSessionID != s1.ID {
		t.Fatalf("取消断点游标不符 (应停在 s1=%d, processed=1, total=3): %+v", s1.ID, dto1)
	}
	// 终态再次取消 -> ErrAttributionJobTerminal (409)
	if _, err := CancelAttributionJob(ctx, e.db, e.op(e.adminUser, true), job1.ID); !errors.Is(err, ErrAttributionJobTerminal) {
		t.Fatalf("终态取消期望 ErrAttributionJobTerminal，实际: %v", err)
	}

	// 验证 S1 流量已完成回补，而 S2/S4 仍未回补
	var s1Egress, s2Egress models.TrafficRecord
	_ = e.db.Where("session_id = ? AND seq_no = 1", s1.ID).First(&s1Egress).Error
	_ = e.db.Where("session_id = ? AND seq_no = 1", s2.ID).First(&s2Egress).Error
	if s1Egress.SrcModuleID == nil || *s1Egress.SrcModuleID != e.modA.ID || s1Egress.DstModuleID == nil || *s1Egress.DstModuleID != e.modC.ID {
		t.Fatalf("S1 EGRESS 应已回补 src=modA dst=modC，实际: %+v", s1Egress)
	}
	if s2Egress.SrcModuleID != nil || s2Egress.DstModuleID != nil {
		t.Fatalf("取消后 S2 不得被处理，实际: %+v", s2Egress)
	}

	// 2. 模拟服务崩溃重启遗留 RUNNING 状态（cursor=s1.ID），验证 RecoverRunningJobs + 续跑完成 S2/S4
	if err := e.db.Model(&models.AttributionJob{}).Where("id = ?", job1.ID).Update("status", models.AttrJobStatusRunning).Error; err != nil {
		t.Fatalf("模拟遗留 RUNNING 失败: %v", err)
	}
	// 普通他人无权取消任务 -> 403
	if _, err := CancelAttributionJob(ctx, e.db, e.op(e.otherUser, false), job1.ID); !errors.Is(err, ErrAttributionJobForbidden) {
		t.Fatalf("非创建人非管理员取消期望 ErrAttributionJobForbidden，实际: %v", err)
	}

	worker2 := NewAttributionJobWorker(e.db, nil, broker, AttributionWorkerOptions{
		BatchInterval: 2 * time.Millisecond,
		PollInterval:  10 * time.Millisecond,
	})
	if err := worker2.RecoverRunningJobs(ctx); err != nil {
		t.Fatalf("RecoverRunningJobs 失败: %v", err)
	}
	var recovered models.AttributionJob
	_ = e.db.First(&recovered, job1.ID).Error
	if recovered.Status != models.AttrJobStatusPending || recovered.CursorSessionID != s1.ID {
		t.Fatalf("RecoverRunningJobs 应置为 PENDING 且保留游标 s1=%d: %+v", s1.ID, recovered)
	}

	processedAny, err := worker2.ProcessNextPending(ctx)
	if err != nil || !processedAny {
		t.Fatalf("续跑 ProcessNextPending 失败: processed=%v err=%v", processedAny, err)
	}

	dtoAfter, err := GetAttributionJobDTO(ctx, e.db, job1.ID)
	if err != nil {
		t.Fatalf("GetAttributionJobDTO(after) 失败: %v", err)
	}
	if dtoAfter.Status != models.AttrJobStatusDone || dtoAfter.Processed != 3 || dtoAfter.Total != 3 || dtoAfter.CursorSessionID != s4.ID {
		t.Fatalf("断点续跑完成后期望 DONE (3/3, cursor=s4=%d): %+v", s4.ID, dtoAfter)
	}
	if dtoAfter.MatchedCount < 3 {
		t.Fatalf("MatchedCount 应 >= 3: %+v", dtoAfter)
	}

	// 验证 S2 和 S4 均完成回补，S3 未受影响
	var s4Egress models.TrafficRecord
	_ = e.db.Where("session_id = ? AND seq_no = 1", s2.ID).First(&s2Egress).Error
	_ = e.db.Where("session_id = ? AND seq_no = 1", s4.ID).First(&s4Egress).Error
	if s2Egress.DstModuleID == nil || *s2Egress.DstModuleID != e.modC.ID || s4Egress.DstModuleID == nil || *s4Egress.DstModuleID != e.modC.ID {
		t.Fatalf("续跑后 S2/S4 应均已回补: s2=%+v s4=%+v", s2Egress, s4Egress)
	}

	// 3. 验证 SSE 事件至少收到中间进度与最终 DONE 事件
	events := broker.snapshot()
	if len(events) < 3 {
		t.Fatalf("期望收到 >= 3 条 attribution_job.updated SSE 事件，实际 %d: %+v", len(events), events)
	}
	last := events[len(events)-1]
	if last.JobID != job1.ID || last.Status != models.AttrJobStatusDone || last.Processed != 3 || last.Total != 3 {
		t.Fatalf("最终 SSE 事件不符: %+v", last)
	}
}
