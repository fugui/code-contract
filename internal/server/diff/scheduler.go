package diff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	commonAudit "code-common/backend/audit"
	"code-contract/config"
	"code-contract/internal/server/callgraph"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/hub"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var (
	ErrTaskNotFound    = errors.New("404 Not Found: 重放任务不存在")
	ErrTaskAlreadyDone = errors.New("409 Conflict: 重放任务已结束")
	ErrRecordNotReplay = errors.New("400 Bad Request: 选定记录包含不可重放或已被截断的报文")
	ErrAgentOffline    = errors.New("503 Service Unavailable: 目标 Agent 处于离线状态")
	ErrEnvBusy         = errors.New("503 Service Unavailable: GATED_ENV_BUSY: 测试环境排他租约等待超时")
)

// CreateReplayTaskReq 创建并下发重放任务请求
type CreateReplayTaskReq struct {
	Name              string   `json:"name" binding:"required"`
	SessionID         uint64   `json:"session_id" binding:"required"`
	SourceProcessID   uint64   `json:"source_process_id" binding:"required"`
	TargetProcessID   uint64   `json:"target_process_id" binding:"required"`
	SelectedRecordIDs []uint64 `json:"selected_record_ids" binding:"required"`
	ExtraIgnorePaths  []string `json:"extra_ignore_paths"`
	TimeoutMs         int      `json:"timeout_ms"`
	TriggerSource     string   `json:"trigger_source"`
	CSGStrategy       string   `json:"csg_strategy"`
	CSGAddr           string   `json:"csg_addr"`
}

// ReplayTaskListQuery 任务列表筛选
type ReplayTaskListQuery struct {
	SessionID       uint64 `form:"session_id"`
	GoldenSuiteID   uint64 `form:"golden_suite_id"`
	TargetProcessID uint64 `form:"target_process_id"`
	AgentID         uint64 `form:"agent_id"`
	LifecycleState  string `form:"lifecycle_state"`
	ResultStatus    string `form:"result_status"`
	Page            int    `form:"page"`
	PageSize        int    `form:"page_size"`
}

// ReplayItemListQuery 单任务请求明细筛选
type ReplayItemListQuery struct {
	RecordID     uint64 `form:"record_id"`
	ResultStatus string `form:"result_status"`
	ServiceName  string `form:"service_name"`
	Operation    string `form:"operation"`
	Page         int    `form:"page"`
	PageSize     int    `form:"page_size"`
}

// ReplayScheduler 跨 Agent 同构离线重放调度与双层回归断言中枢
type ReplayScheduler struct {
	db         *gorm.DB
	dec        *decoder.Registry
	httpClient *http.Client
}

// NewReplayScheduler 构造调度器
func NewReplayScheduler(db *gorm.DB, dec *decoder.Registry) *ReplayScheduler {
	return &ReplayScheduler{
		db:  db,
		dec: dec,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// CreateAndExecuteTask 创建重放任务、校验同构性、获取排他租约并下发 Agent 执行
func (s *ReplayScheduler) CreateAndExecuteTask(
	ctx context.Context,
	req CreateReplayTaskReq,
	userID uint,
	isAdmin bool,
) (*models.ReplayTask, error) {
	if len(req.SelectedRecordIDs) == 0 {
		return nil, errors.New("400 Bad Request: selected_record_ids 不能为空")
	}
	strategy, sErr := parseCSGStrategyOrErr(req.CSGStrategy)
	if sErr != nil {
		return nil, sErr
	}

	// 1. 加载 Session 并校验 Anti-IDOR
	var session models.RecordingSession
	if err := s.db.WithContext(ctx).Where("id = ?", req.SessionID).First(&session).Error; err != nil {
		return nil, errors.New("404 Not Found: 录制会话不存在")
	}
	if session.Scope == models.ScopePrivate && session.OwnerID != userID && !isAdmin {
		return nil, ErrForbiddenPrivateSession
	}

	// 2. 加载目标进程与目标 Agent
	var targetProc models.Process
	if err := s.db.WithContext(ctx).Preload("Agent").Preload("ArchProcess").Where("id = ?", req.TargetProcessID).First(&targetProc).Error; err != nil {
		return nil, errors.New("404 Not Found: 目标进程不存在")
	}
	if targetProc.Agent == nil {
		return nil, errors.New("400 Bad Request: 目标进程未关联 Agent")
	}
	if targetProc.Agent.Scope == models.ScopePrivate && (targetProc.Agent.OwnerID == nil || *targetProc.Agent.OwnerID != userID) && !isAdmin {
		return nil, ErrForbiddenPrivateAgent
	}

	// 3. 加载源进程（优先从 session.ProcessSnapshots 获取，解耦源进程被卸载后依然可重放）
	var sourceProc models.Process
	_ = s.db.WithContext(ctx).Preload("ArchProcess").Where("id = ?", req.SourceProcessID).First(&sourceProc).Error

	var sourceSnap *models.ProcessSnapshotItem
	if len(session.ProcessSnapshots) > 0 {
		var snapshots []models.ProcessSnapshotItem
		if err := json.Unmarshal(session.ProcessSnapshots, &snapshots); err == nil {
			for i := range snapshots {
				if snapshots[i].ProcessID == req.SourceProcessID {
					sourceSnap = &snapshots[i]
					break
				}
			}
		}
	}
	if sourceSnap == nil && sourceProc.ID > 0 {
		sourceProc.FillFromArchProcess()
		sourceSnap = &models.ProcessSnapshotItem{
			ProcessID:     sourceProc.ID,
			ArchProcessID: sourceProc.ArchProcessID,
			Code:          sourceProc.Code,
			Name:          sourceProc.Name,
			ListenPort:    sourceProc.EffectiveListenPort,
		}
	}
	if sourceSnap == nil {
		return nil, errors.New("404 Not Found: 源进程不存在且未在录制快照中固化")
	}

	// 4. 加载选定流量记录并校验可重放性
	var records []models.TrafficRecord
	if err := s.db.WithContext(ctx).Where("id IN ? AND session_id = ?", req.SelectedRecordIDs, req.SessionID).
		Find(&records).Error; err != nil {
		return nil, fmt.Errorf("查询选定记录失败: %w", err)
	}
	if len(records) != len(req.SelectedRecordIDs) {
		return nil, errors.New("400 Bad Request: 部分选定记录不存在或不属于该录制批次")
	}

	for _, r := range records {
		if !r.IsReplayable || len(r.RawReqPacket) == 0 || r.IsTruncated {
			return nil, ErrRecordNotReplay
		}
	}

	// 5. 跨 Agent 同构进程模块兼容性校验 (F-12.1, ADR-09)
	var provideServices []models.ModuleService
	_ = s.db.WithContext(ctx).Where("module_id IS NOT NULL").Find(&provideServices).Error
	provideMap := make(map[string]uint64)
	for _, svc := range provideServices {
		if svc.ModuleID == nil || *svc.ModuleID == 0 {
			continue
		}
		k := strings.TrimSpace(svc.ServiceName)
		provideMap[k] = *svc.ModuleID
	}

	archProcModules := make(map[uint64][]uint64)
	var allMods []models.Module
	_ = s.db.WithContext(ctx).Where("arch_process_id IS NOT NULL").Find(&allMods).Error
	for _, m := range allMods {
		if m.ArchProcessID != nil {
			archProcModules[*m.ArchProcessID] = append(archProcModules[*m.ArchProcessID], m.ID)
		}
	}

	if err := ValidateReplayProcessCompatibilityWithStrategy(
		userID, isAdmin, &session, records, sourceSnap, &targetProc, targetProc.Agent, archProcModules, provideMap, strategy,
	); err != nil {
		return nil, err
	}

	// 6. 创建初始 ReplayTask
	now := time.Now()
	taskNo := fmt.Sprintf("REP_%s_%d", time.Now().Format("20060102150405"), time.Now().UnixNano()%1000)
	selBytes, _ := json.Marshal(req.SelectedRecordIDs)
	ignoreBytes, _ := json.Marshal(req.ExtraIgnorePaths)
	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 30000
	}

	srcPID := req.SourceProcessID
	tgtPID := req.TargetProcessID
	var srcPIDPtr *uint64
	if srcPID > 0 {
		srcPIDPtr = &srcPID
	}
	var tgtPIDPtr *uint64
	if tgtPID > 0 {
		tgtPIDPtr = &tgtPID
	}
	archPID := targetProc.ArchProcessID
	if archPID == 0 && sourceSnap != nil {
		archPID = sourceSnap.ArchProcessID
	}

	triggerSource := req.TriggerSource
	if triggerSource == "" {
		triggerSource = models.TriggerSourceManual
	}
	sessID := req.SessionID

	task := &models.ReplayTask{
		TaskNo:            taskNo,
		Name:              req.Name,
		TriggerSource:     triggerSource,
		OwnerID:           userID,
		SessionID:         &sessID,
		ArchProcessID:     archPID,
		SourceProcessID:   srcPIDPtr,
		TargetProcessID:   tgtPIDPtr,
		AgentID:           targetProc.AgentID,
		SelectedRecordIDs: datatypes.JSON(selBytes),
		ExtraIgnorePaths:  datatypes.JSON(ignoreBytes),
		TimeoutMs:         timeoutMs,
		CSGStrategy:       string(strategy),
		LifecycleState:    "ARMED",
		TotalRequests:     len(records),
		CreatedBy:         userID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.db.WithContext(ctx).Create(task).Error; err != nil {
		return nil, fmt.Errorf("创建重放任务失败: %w", err)
	}

	// 7. 锁定目标 Agent 的 REPLAY 排他租约
	leaseTTL := time.Duration(timeoutMs/1000+120) * time.Second
	if err := hub.AcquireAgentLeases(ctx, s.db, []uint64{targetProc.AgentID}, models.AgentLeaseReplay, task.ID, userID, leaseTTL); err != nil {
		s.db.WithContext(ctx).Model(task).Updates(map[string]interface{}{
			"lifecycle_state": "FAILED",
			"error_message":   err.Error(),
		})
		return nil, err
	}

	// 8. 组装 ScopedMocks 与下发执行 (全策略同步下发 /execute 预检以即时暴露 409/422 冲突，随后异步轮询)
	procCopy := targetProc
	execReq := s.buildSessionExecReq(task, &procCopy, records, req.CSGAddr)
	if pErr := postExecuteReplayToAgent(ctx, s.httpClient, procCopy.Agent, execReq); pErr != nil {
		_ = hub.ReleaseAgentLeases(context.Background(), s.db, []uint64{task.AgentID}, models.AgentLeaseReplay, task.ID)
		s.markTaskFailed(task.ID, pErr.Error())
		return nil, pErr
	}
	go s.pollAndEvaluateSessionReplay(task, &procCopy, records, req.ExtraIgnorePaths, true)

	commonAudit.Push(&models.SysAuditLog{
		CreatedAt:  time.Now(),
		Service:    config.ServiceName,
		Module:     config.ModuleKey,
		Action:     "execute_replay",
		Level:      "P2",
		Summary:    fmt.Sprintf("发起跨进程同构重放: task=%s, records=%d, targetProc=%s", task.Name, len(records), targetProc.Name),
		TargetType: "replay_task",
		TargetID:   fmt.Sprintf("%d", task.ID),
		UserID:     userID,
		Username:   fmt.Sprintf("user_%d", userID),
		StatusCode: 201,
	})

	return task, nil
}

func (s *ReplayScheduler) pollAndEvaluateSessionReplay(
	task *models.ReplayTask,
	targetProc *models.Process,
	records []models.TrafficRecord,
	extraIgnorePaths []string,
	_ bool,
) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(task.TimeoutMs+10000)*time.Millisecond)
	defer cancel()
	s.pollAndEvaluateSessionReplayWithCtx(ctx, task, targetProc, records, extraIgnorePaths)
}

func (s *ReplayScheduler) buildSessionExecReq(
	task *models.ReplayTask,
	targetProc *models.Process,
	records []models.TrafficRecord,
	csgAddr string,
) agentproto.ExecuteReplayReq {
	// 1. 组装输入帧与下游出站 Mocks
	var ingressRequests [][]byte
	var ingressSvcs []string
	for _, r := range records {
		ingressRequests = append(ingressRequests, r.RawReqPacket)
		ingressSvcs = append(ingressSvcs, r.ServiceName)
	}

	// 检索同批次属于 source_process_id 的 Egress 流量用作 ScopedMock 与历史端点提取
	var egressRecords []models.TrafficRecord
	egressQuery := s.db.Where("session_id = ? AND direction = 'EGRESS' AND is_truncated = false AND exchange_type != 'REQ_ONLY'", task.SessionID)
	if task.SourceProcessID != nil && *task.SourceProcessID > 0 {
		egressQuery = egressQuery.Where("process_id = ?", *task.SourceProcessID)
	}
	_ = egressQuery.Limit(500).Find(&egressRecords).Error

	var egressMocks [][]byte
	var historyEgressEndpoints []agentproto.DownstreamEndpoint
	seenHistoryEP := make(map[string]bool)

	for _, eg := range egressRecords {
		mockData := eg.RawRspPacket
		if len(mockData) == 0 {
			mockData = eg.RawReqPacket
		}
		if len(mockData) > 0 {
			egressMocks = append(egressMocks, mockData)
		}
		if eg.DstAddr != "" {
			host, portStr, err := net.SplitHostPort(strings.TrimSpace(eg.DstAddr))
			if err == nil {
				port, err := strconv.Atoi(portStr)
				if err == nil && port > 0 && port <= 65535 {
					key := fmt.Sprintf("%s:%d", host, port)
					if !seenHistoryEP[key] {
						seenHistoryEP[key] = true
						historyEgressEndpoints = append(historyEgressEndpoints, agentproto.DownstreamEndpoint{
							IP:   host,
							Port: uint16(port),
						})
					}
				}
			}
		}
	}

	downstreams, _ := topology.CompileProcessDownstreams(s.db, targetProc.ID, nil, historyEgressEndpoints)
	targetProc.FillFromArchProcess()
	var targetPIDs []uint32
	if len(targetProc.ObservedPIDs) > 0 {
		_ = json.Unmarshal(targetProc.ObservedPIDs, &targetPIDs)
	}
	var obsTransports []string
	if len(targetProc.ObservedTransports) > 0 {
		_ = json.Unmarshal(targetProc.ObservedTransports, &obsTransports)
	}
	spec := agentproto.ProcessMonitorSpec{
		ProcessID:   targetProc.ID,
		ListenPort:  uint16(targetProc.EffectiveListenPort),
		CmdPattern:  targetProc.CmdPattern,
		TargetPIDs:  targetPIDs,
		Transports:  obsTransports,
		Downstreams: downstreams,
		CSGAddr:     strings.TrimSpace(csgAddr),
	}

	// 组装新版成对 ScopedMock 与 IngressItems (支持 DstTag 多实现隔离)
	var allMods []models.Module
	_ = s.db.Preload("ArchProcess").Where("arch_process_id IS NOT NULL").Find(&allMods).Error
	modArchCodes := make(map[uint64]string, len(allMods))
	for _, m := range allMods {
		if m.ArchProcess != nil && m.ArchProcess.Code != "" {
			modArchCodes[m.ID] = m.ArchProcess.Code
		}
	}

	var ingressItems []agentproto.ReplayIngressItem
	scopedEgressMocks := make(map[uint64][]agentproto.MockRuleDownstream)
	for _, r := range records {
		ingressItems = append(ingressItems, agentproto.ReplayIngressItem{
			RecordID: r.ID,
			RawReq:   r.RawReqPacket,
		})

		var matchedEgress []models.TrafficRecord
		for _, eg := range egressRecords {
			if r.TraceID != "" && r.TraceID != "0" && eg.TraceID == r.TraceID {
				matchedEgress = append(matchedEgress, eg)
			} else if eg.ReqCapturedAtNs >= r.ReqCapturedAtNs {
				rEnd := r.ReqCapturedAtNs + 500_000_000
				if r.RspCapturedAtNs != nil {
					rEnd = *r.RspCapturedAtNs + 10_000_000
				}
				if eg.ReqCapturedAtNs <= rEnd {
					matchedEgress = append(matchedEgress, eg)
				}
			}
		}
		if len(matchedEgress) > 0 {
			scopedEgressMocks[r.ID] = callgraph.BuildScopedMockRulesWithDstTags(matchedEgress, modArchCodes)
		}
	}
	globalFallbackMocks := callgraph.BuildScopedMockRulesWithDstTags(egressRecords, modArchCodes)

	strat := agentproto.CSGReplayStrategy(task.CSGStrategy)
	csgSnaps := loadSessionCSGSnapshots(s.db, task.SessionID)
	targetSvcs := collectReplayTargetServices(s.db, strat, task.ArchProcessID, ingressSvcs, egressRecords, scopedEgressMocks, csgSnaps)

	return agentproto.ExecuteReplayReq{
		TaskID:              task.ID,
		ProcessSpec:         spec,
		TimeoutMs:           task.TimeoutMs,
		CSGStrategy:         strat,
		CSGAddr:             strings.TrimSpace(csgAddr),
		CSGSnapshots:        csgSnaps,
		TargetServices:      targetSvcs,
		IngressRequests:     ingressRequests,
		EgressMocks:         egressMocks,
		IngressItems:        ingressItems,
		ScopedEgressMocks:   scopedEgressMocks,
		GlobalFallbackMocks: globalFallbackMocks,
	}
}

func (s *ReplayScheduler) pollAndEvaluateSessionReplayWithCtx(
	ctx context.Context,
	task *models.ReplayTask,
	targetProc *models.Process,
	records []models.TrafficRecord,
	extraIgnorePaths []string,
) {
	defer func() {
		// 释放租约
		_ = hub.ReleaseAgentLeases(context.Background(), s.db, []uint64{task.AgentID}, models.AgentLeaseReplay, task.ID)
	}()

	startedAt := time.Now()
	s.db.Model(&models.ReplayTask{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"lifecycle_state": "INJECTING",
		"started_at":      &startedAt,
	})

	// 2. 轮询拉取结果
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	var result *agentproto.ReplayTaskResultResp
	pollURL := fmt.Sprintf("%s/agent/v1/replays/%d/result", targetProc.Agent.Endpoint, task.ID)

	for {
		select {
		case <-ctx.Done():
			s.markTaskFailed(task.ID, "等待重放执行超时")
			return
		case <-ticker.C:
			pReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
			if targetProc.Agent.ClaimCode != "" {
				pReq.Header.Set(agentproto.HeaderClaimCode, targetProc.Agent.ClaimCode)
			}
			pResp, pErr := s.httpClient.Do(pReq)
			if pErr == nil && pResp.StatusCode == http.StatusOK {
				var res agentproto.ReplayTaskResultResp
				if decErr := json.NewDecoder(pResp.Body).Decode(&res); decErr == nil {
					_ = pResp.Body.Close()
					result = &res
					goto EVALUATE
				}
				_ = pResp.Body.Close()
			}
		}
	}

EVALUATE:
	// 3. 执行每条请求的双层 Diff 断言并落盘 contract_replay_items (ADR-31 D2 Fail-Closed)
	var itemStatuses []string
	var mockHitCount, mockMissCount int
	if result != nil {
		mockHitCount = result.MockHitCount
		mockMissCount = result.MockMissCount
	}

	detailMap := extractReplayItemDetails(result)

	for i, rec := range records {
		d, hasDetail := detailMap[rec.ID]
		var detailPtr *agentproto.ReplayItemDetail
		if hasDetail {
			detailPtr = &d
		}
		resultFallback := ""
		if result != nil {
			resultFallback = result.Status
		}

		eval := evaluateReplayItem(
			s.dec,
			rec.ServiceName,
			rec.Operation,
			rec.ExchangeType,
			rec.RawReqPacket,
			rec.RawRspPacket,
			detailPtr,
			hasDetail,
			resultFallback,
			extraIgnorePaths,
		)

		itemStatuses = append(itemStatuses, eval.ResultStatus)
		diffBytes, _ := json.Marshal(eval.FieldDiffs)
		mockTraceBytes, _ := json.Marshal(eval.MockTrace)
		recID := rec.ID
		replayItem := models.ReplayItem{
			TaskID:          task.ID,
			RecordID:        &recID,
			SeqNo:           int64(i + 1),
			ServiceName:     rec.ServiceName,
			Operation:       rec.Operation,
			ResultStatus:    eval.ResultStatus,
			LatencyUs:       eval.LatencyUs,
			ActualStatus:    models.U64Numeric(eval.ActualStatus),
			BaselineStatus:  rec.StatusCode,
			FailedLayer:     eval.FailedLayer,
			FieldDiffs:      datatypes.JSON(diffBytes),
			ActualPayload:   eval.ActualPayload,
			RawActualPacket: eval.RawActualPacket,
			MockTrace:       datatypes.JSON(mockTraceBytes),
			ErrorMessage:    eval.ErrorMessage,
			CreatedAt:       time.Now(),
		}
		if createErr := s.db.Create(&replayItem).Error; createErr == nil {
			hub.GetGlobalSSEBroker().BroadcastReplayItemDone(task.ID, replayItem.ID, recID, replayItem.ResultStatus, replayItem.LatencyUs)
		}
	}

	// 4. 聚合最终状态并持久化 (带 CANCELLED 条件守卫)
	finalStatus := AggregateReplayStatus(itemStatuses)
	finishedAt := time.Now()
	passedCount := 0
	for _, st := range itemStatuses {
		if st == models.ReplayResultPass {
			passedCount++
		}
	}

	var observedCalls []agentproto.ObservedEgressCall
	if result != nil && len(result.ObservedEgressCalls) > 0 {
		observedCalls = result.ObservedEgressCalls
	}
	observedBytes, _ := json.Marshal(observedCalls)
	var mockEvents []agentproto.MockTraceEvent
	if result != nil && len(result.MockEvents) > 0 {
		mockEvents = result.MockEvents
	}
	mockEventsBytes, _ := json.Marshal(mockEvents)
	sandboxJSON, violations, _ := verifyObservedWithSandbox(ctx, s.db, task.ArchProcessID, observedCalls, result)
	violationCount := len(violations)

	updates := map[string]interface{}{
		"lifecycle_state":       "COMPLETED",
		"result_status":         finalStatus,
		"passed_requests":       passedCount,
		"mock_hit_count":        mockHitCount,
		"mock_miss_count":       mockMissCount,
		"mock_events":           datatypes.JSON(mockEventsBytes),
		"violation_count":       violationCount,
		"observed_egress_calls": datatypes.JSON(observedBytes),
		"finished_at":           &finishedAt,
	}
	if len(sandboxJSON) > 0 {
		updates["csg_sandbox_report"] = datatypes.JSON(sandboxJSON)
	}
	txRes := s.db.Model(&models.ReplayTask{}).Where("id = ? AND lifecycle_state != ?", task.ID, "CANCELLED").Updates(updates)
	if txRes.RowsAffected > 0 {
		hub.GetGlobalSSEBroker().BroadcastReplayCompleted(task.ID, "COMPLETED", finalStatus, len(records), passedCount, mockHitCount, mockMissCount)
	}
}

func (s *ReplayScheduler) markTaskFailed(taskID uint64, msg string) {
	finishedAt := time.Now()
	s.db.Model(&models.ReplayTask{}).Where("id = ? AND lifecycle_state != ?", taskID, "CANCELLED").Updates(map[string]interface{}{
		"lifecycle_state": "FAILED",
		"result_status":   models.ReplayResultError,
		"error_message":   msg,
		"finished_at":     &finishedAt,
	})
}

// CancelTask 取消正在执行的重放任务
func (s *ReplayScheduler) CancelTask(
	ctx context.Context,
	taskID uint64,
	userID uint,
	isAdmin bool,
) (*models.ReplayTask, error) {
	var task models.ReplayTask
	if err := s.db.WithContext(ctx).Preload("Agent").Where("id = ?", taskID).First(&task).Error; err != nil {
		return nil, ErrTaskNotFound
	}
	if task.OwnerID != userID && !isAdmin {
		return nil, errors.New("403 Forbidden: 无权取消他人重放任务")
	}
	if task.LifecycleState == "COMPLETED" || task.LifecycleState == "CANCELLED" || task.LifecycleState == "FAILED" {
		return nil, ErrTaskAlreadyDone
	}

	if task.Agent != nil {
		url := fmt.Sprintf("%s/agent/v1/replays/%d/cancel", task.Agent.Endpoint, task.ID)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		if task.Agent.ClaimCode != "" {
			req.Header.Set(agentproto.HeaderClaimCode, task.Agent.ClaimCode)
		}
		resp, err := s.httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}

	_ = hub.ReleaseAgentLeases(ctx, s.db, []uint64{task.AgentID}, models.AgentLeaseReplay, task.ID)
	finishedAt := time.Now()
	task.LifecycleState = "CANCELLED"
	task.FinishedAt = &finishedAt
	s.db.WithContext(ctx).Model(&task).Updates(map[string]interface{}{
		"lifecycle_state": "CANCELLED",
		"finished_at":     &finishedAt,
	})

	return &task, nil
}

// RediffTask 离线重算双层 Diff (F-14.4, ADR-31 P1-4)
func (s *ReplayScheduler) RediffTask(
	ctx context.Context,
	taskID uint64,
	extraIgnorePaths []string,
	userID uint,
	isAdmin bool,
) (*models.ReplayTask, error) {
	var task models.ReplayTask
	if err := s.db.WithContext(ctx).Preload("GoldenSuite").Where("id = ?", taskID).First(&task).Error; err != nil {
		return nil, ErrTaskNotFound
	}
	if task.OwnerID != userID && !isAdmin {
		return nil, errors.New("403 Forbidden: 无权操作他人重放任务")
	}

	var items []models.ReplayItem
	if err := s.db.WithContext(ctx).Where("task_id = ?", task.ID).Order("seq_no ASC").Find(&items).Error; err != nil {
		return nil, err
	}

	// 合并基础忽略路径：若未传入则沿用任务既有规则，同时叠加套件规则
	effectiveTaskIgnore := extraIgnorePaths
	if len(effectiveTaskIgnore) == 0 && len(task.ExtraIgnorePaths) > 0 {
		_ = json.Unmarshal(task.ExtraIgnorePaths, &effectiveTaskIgnore)
	}
	var suiteIgnorePaths []string
	if task.GoldenSuite != nil && len(task.GoldenSuite.ExtraIgnorePaths) > 0 {
		_ = json.Unmarshal(task.GoldenSuite.ExtraIgnorePaths, &suiteIgnorePaths)
	}
	baseIgnorePaths := appendUniqueIgnorePaths(suiteIgnorePaths, effectiveTaskIgnore)

	var itemStatuses []string
	passedCount := 0

	for _, item := range items {
		// 严格保留既有执行期异常状态（TIMEOUT / MOCK_MISS / ERROR），绝不洗白
		if item.ResultStatus == models.ReplayResultTimeout ||
			item.ResultStatus == models.ReplayResultMockMiss ||
			item.ResultStatus == models.ReplayResultError {
			itemStatuses = append(itemStatuses, item.ResultStatus)
			continue
		}

		// 查询关联 Baseline 记录或黄金用例自包含快照
		var baseReq, baseRsp []byte
		var exchangeType string
		var itemIgnorePaths []string
		itemIgnorePaths = append(itemIgnorePaths, baseIgnorePaths...)

		if item.RecordID != nil {
			var baseRec models.TrafficRecord
			if err := s.db.WithContext(ctx).Where("id = ?", *item.RecordID).First(&baseRec).Error; err == nil {
				baseReq = baseRec.RawReqPacket
				baseRsp = baseRec.RawRspPacket
				exchangeType = baseRec.ExchangeType
			}
		} else if item.SuiteItemID != nil {
			var suiteItem models.GoldenSuiteItem
			if err := s.db.WithContext(ctx).Where("id = ?", *item.SuiteItemID).First(&suiteItem).Error; err == nil {
				baseReq = suiteItem.RawReqPacket
				baseRsp = suiteItem.RawRspPacket
				if len(suiteItem.MaskRulesJSON) > 0 {
					var masks []models.MaskRule
					if json.Unmarshal(suiteItem.MaskRulesJSON, &masks) == nil {
						var maskPaths []string
						for _, mr := range masks {
							p := strings.TrimSpace(mr.Path)
							if p != "" {
								maskPaths = append(maskPaths, p)
								if strings.HasPrefix(p, "rsp.") {
									maskPaths = append(maskPaths, strings.TrimPrefix(p, "rsp."))
								}
							}
						}
						itemIgnorePaths = appendUniqueIgnorePaths(itemIgnorePaths, maskPaths)
					}
				}
			}
		}

		if exchangeType == models.ExchangeOneway || isOnewayRequestFrame(baseReq) {
			itemStatuses = append(itemStatuses, models.ReplayResultPass)
			passedCount++
			continue
		}

		if len(item.RawActualPacket) == 0 {
			itemStatuses = append(itemStatuses, models.ReplayResultError)
			s.db.WithContext(ctx).Model(&models.ReplayItem{}).Where("id = ?", item.ID).Updates(map[string]interface{}{
				"result_status": models.ReplayResultError,
				"failed_layer":  1,
				"error_message": "缺少实际应答切片，无法重算 Diff (Fail-Closed)",
			})
			continue
		}

		itemStatus := models.ReplayResultPass
		diffRes, fieldDiffs, failedLayer, errMsg := diffHeadersAndPayloads(
			s.dec, item.ServiceName, item.Operation, baseRsp, item.RawActualPacket, itemIgnorePaths,
		)
		if failedLayer > 0 {
			itemStatus = models.ReplayResultFailed
		} else {
			passedCount++
		}
		itemStatuses = append(itemStatuses, itemStatus)

		actualPayload := decodeActualPayloadJSON(s.dec, item.ServiceName, item.Operation, item.RawActualPacket)
		diffBytes, _ := json.Marshal(fieldDiffs)
		upd := map[string]interface{}{
			"result_status": itemStatus,
			"failed_layer":  failedLayer,
			"field_diffs":   datatypes.JSON(diffBytes),
			"error_message": errMsg,
		}
		if len(actualPayload) > 0 {
			upd["actual_payload"] = actualPayload
		}
		if diffRes != nil {
			upd["actual_status"] = models.U64Numeric(diffRes.ActualStatus)
		}
		s.db.WithContext(ctx).Model(&models.ReplayItem{}).Where("id = ?", item.ID).Updates(upd)
	}

	finalStatus := AggregateReplayStatus(itemStatuses)
	ignBytes, _ := json.Marshal(effectiveTaskIgnore)
	task.ResultStatus = finalStatus
	task.PassedRequests = passedCount
	task.ExtraIgnorePaths = datatypes.JSON(ignBytes)

	s.db.WithContext(ctx).Model(&task).Updates(map[string]interface{}{
		"result_status":      finalStatus,
		"passed_requests":    passedCount,
		"extra_ignore_paths": task.ExtraIgnorePaths,
	})

	return &task, nil
}

// GetTask 获取单个任务
func (s *ReplayScheduler) GetTask(ctx context.Context, taskID uint64, userID uint, isAdmin bool) (*models.ReplayTask, error) {
	var task models.ReplayTask
	if err := s.db.WithContext(ctx).Preload("Session").Preload("GoldenSuite").Preload("TargetProcess").Where("id = ?", taskID).First(&task).Error; err != nil {
		return nil, ErrTaskNotFound
	}
	if task.OwnerID != userID && !isAdmin {
		return nil, errors.New("403 Forbidden")
	}
	return &task, nil
}

// ListTasks 分页获取任务
func (s *ReplayScheduler) ListTasks(ctx context.Context, query ReplayTaskListQuery, userID uint, isAdmin bool) ([]models.ReplayTask, int64, error) {
	db := s.db.WithContext(ctx).Model(&models.ReplayTask{})
	if !isAdmin {
		db = db.Where("owner_id = ?", userID)
	}
	if query.SessionID > 0 {
		db = db.Where("session_id = ?", query.SessionID)
	}
	if query.GoldenSuiteID > 0 {
		db = db.Where("golden_suite_id = ?", query.GoldenSuiteID)
	}
	if query.TargetProcessID > 0 {
		db = db.Where("target_process_id = ?", query.TargetProcessID)
	}
	if query.AgentID > 0 {
		db = db.Where("agent_id = ?", query.AgentID)
	}
	if query.LifecycleState != "" {
		db = db.Where("lifecycle_state = ?", query.LifecycleState)
	}
	if query.ResultStatus != "" {
		db = db.Where("result_status = ?", query.ResultStatus)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page := query.Page
	if page <= 0 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 25
	}

	items := make([]models.ReplayTask, 0)
	if err := db.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ListItems 分页获取重放条目 (含任务归属 Anti-IDOR 校验与 record_id 筛选)
func (s *ReplayScheduler) ListItems(ctx context.Context, taskID uint64, query ReplayItemListQuery, userID uint, isAdmin bool) ([]models.ReplayItem, int64, error) {
	var task models.ReplayTask
	if err := s.db.WithContext(ctx).Select("id", "owner_id").Where("id = ?", taskID).First(&task).Error; err != nil {
		return nil, 0, ErrTaskNotFound
	}
	if task.OwnerID != userID && !isAdmin {
		return nil, 0, errors.New("403 Forbidden: 无权查看他人重放任务明细")
	}

	db := s.db.WithContext(ctx).Model(&models.ReplayItem{}).Where("task_id = ?", taskID)
	if query.RecordID > 0 {
		db = db.Where("record_id = ?", query.RecordID)
	}
	if query.ResultStatus != "" {
		db = db.Where("result_status = ?", query.ResultStatus)
	}
	if query.ServiceName != "" {
		db = db.Where("service_name = ?", query.ServiceName)
	}
	if query.Operation != "" {
		db = db.Where("operation = ?", query.Operation)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page := query.Page
	if page <= 0 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 25
	}

	items := make([]models.ReplayItem, 0)
	if err := db.Order("seq_no ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// GetItem 获取单条重放条目
func (s *ReplayScheduler) GetItem(ctx context.Context, taskID, itemID uint64, userID uint, isAdmin bool) (*models.ReplayItem, error) {
	var item models.ReplayItem
	if err := s.db.WithContext(ctx).Where("task_id = ? AND id = ?", taskID, itemID).First(&item).Error; err != nil {
		return nil, errors.New("404 Not Found: 重放条目不存在")
	}
	var task models.ReplayTask
	if err := s.db.WithContext(ctx).Select("owner_id").Where("id = ?", taskID).First(&task).Error; err != nil {
		return nil, ErrTaskNotFound
	}
	if task.OwnerID != userID && !isAdmin {
		return nil, errors.New("403 Forbidden")
	}
	return &item, nil
}

// ExecuteGoldenSuite 触发黄金契约套件脱机回归执行 (F-25, ADR-14)
func (s *ReplayScheduler) ExecuteGoldenSuite(
	ctx context.Context,
	suiteID uint64,
	envAgentID uint64,
	triggerSource string,
	userID uint,
	isAdmin bool,
	timeoutMs int,
) (*models.ReplayTask, error) {
	return s.ExecuteGoldenSuiteWithCSG(ctx, suiteID, envAgentID, triggerSource, userID, isAdmin, timeoutMs, "", "")
}

// ExecuteGoldenSuiteWithCSG 支持指定 CSG 仿真策略与端点触发黄金契约套件脱机回归 (ADR-21)
func (s *ReplayScheduler) ExecuteGoldenSuiteWithCSG(
	ctx context.Context,
	suiteID uint64,
	envAgentID uint64,
	triggerSource string,
	userID uint,
	isAdmin bool,
	timeoutMs int,
	csgStrategy string,
	csgAddr string,
) (*models.ReplayTask, error) {
	strategy, sErr := parseCSGStrategyOrErr(csgStrategy)
	if sErr != nil {
		return nil, sErr
	}
	// 1. 加载套件及用例
	var suite models.GoldenSuite
	if err := s.db.WithContext(ctx).Preload("ArchProcess").Preload("Items", func(db *gorm.DB) *gorm.DB {
		return db.Order("order_no ASC")
	}).Where("id = ?", suiteID).First(&suite).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("404 Not Found: 黄金契约套件不存在")
		}
		return nil, fmt.Errorf("查询黄金套件失败: %w", err)
	}

	if len(suite.Items) == 0 {
		return nil, errors.New("400 Bad Request: 黄金套件中无可用用例")
	}

	// 2. 加载目标 Agent 并进行 Anti-IDOR 校验
	var agent models.Agent
	if err := s.db.WithContext(ctx).Where("id = ?", envAgentID).First(&agent).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("404 Not Found: 目标环境 Agent 不存在")
		}
		return nil, fmt.Errorf("查询目标 Agent 失败: %w", err)
	}
	if agent.Scope == models.ScopePrivate && (agent.OwnerID == nil || *agent.OwnerID != userID) && !isAdmin {
		return nil, ErrForbiddenPrivateAgent
	}

	// 3. 查找该 Agent 上绑定的与 suite.ArchProcessID 匹配的目标进程
	var targetProc models.Process
	if err := s.db.WithContext(ctx).Preload("ArchProcess").
		Where("agent_id = ? AND arch_process_id = ?", envAgentID, suite.ArchProcessID).
		First(&targetProc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("400 Bad Request: 目标环境 Agent 未挂载该黄金套件对应的标准架构进程")
		}
		return nil, fmt.Errorf("查询目标进程失败: %w", err)
	}
	targetProc.Agent = &agent

	// 4. 检查进程就绪状态
	if targetProc.ProbeStatus == models.ProbeStatusDisarmed || targetProc.NeedReconnect {
		return nil, errors.New("409 Conflict: 目标进程探针未就绪 (NEED_RECONNECT)")
	}

	if timeoutMs <= 0 {
		timeoutMs = 30000
	}
	if triggerSource == "" {
		triggerSource = models.TriggerSourceGoldenSuite
	}

	// 5. 创建 ReplayTask
	now := time.Now()
	taskNo := fmt.Sprintf("REP_GS_%s_%d", now.Format("20060102150405"), now.UnixNano()%1000)
	tgtPID := targetProc.ID

	task := &models.ReplayTask{
		TaskNo:            taskNo,
		Name:              fmt.Sprintf("[%s] %s 脱机回归", suite.Code, suite.Name),
		TriggerSource:     triggerSource,
		OwnerID:           userID,
		SessionID:         nil,
		GoldenSuiteID:     &suite.ID,
		ArchProcessID:     suite.ArchProcessID,
		TargetProcessID:   &tgtPID,
		AgentID:           envAgentID,
		SelectedRecordIDs: datatypes.JSON([]byte("[]")),
		ExtraIgnorePaths:  suite.ExtraIgnorePaths,
		TimeoutMs:         timeoutMs,
		CSGStrategy:       string(strategy),
		LifecycleState:    "ARMED",
		TotalRequests:     len(suite.Items),
		CreatedBy:         userID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := s.db.WithContext(ctx).Create(task).Error; err != nil {
		return nil, fmt.Errorf("创建脱机回归任务失败: %w", err)
	}

	// 6. 获取目标 Agent 的排他租约
	leaseTTL := time.Duration(timeoutMs/1000+120) * time.Second
	if err := hub.AcquireAgentLeases(ctx, s.db, []uint64{envAgentID}, models.AgentLeaseReplay, task.ID, userID, leaseTTL); err != nil {
		s.db.WithContext(ctx).Model(task).Updates(map[string]interface{}{
			"lifecycle_state": "FAILED",
			"error_message":   err.Error(),
		})
		return nil, err
	}

	// 7. 执行重放与回归断言 (全策略同步发送预检请求以即时暴露 409/422 冲突)
	procCopy := targetProc
	suiteCopy := suite
	execReq := s.buildSuiteExecReq(task, &procCopy, suite.Items, csgAddr)
	if pErr := postExecuteReplayToAgent(ctx, s.httpClient, procCopy.Agent, execReq); pErr != nil {
		_ = hub.ReleaseAgentLeases(context.Background(), s.db, []uint64{task.AgentID}, models.AgentLeaseReplay, task.ID)
		s.markTaskFailed(task.ID, pErr.Error())
		return nil, pErr
	}
	go s.pollGoldenSuiteAsync(task, &procCopy, &suiteCopy, suite.Items)

	commonAudit.Push(&models.SysAuditLog{
		CreatedAt:  time.Now(),
		Service:    config.ServiceName,
		Module:     config.ModuleKey,
		Action:     "execute_golden_suite",
		Level:      "P2",
		Summary:    fmt.Sprintf("发起黄金契约套件脱机回归: suite=%s, items=%d, targetProc=%s", suite.Code, len(suite.Items), targetProc.Name),
		TargetType: "golden_suite",
		TargetID:   fmt.Sprintf("%d", suite.ID),
		UserID:     userID,
		Username:   fmt.Sprintf("user_%d", userID),
		StatusCode: 201,
	})

	return task, nil
}

// ExecuteGoldenSuiteSync 同步阻塞式执行黄金契约套件回归，支持排他租约有界排队 (ADR-14, M17, F-27)
func (s *ReplayScheduler) ExecuteGoldenSuiteSync(
	ctx context.Context,
	suiteID uint64,
	envAgentID uint64,
	triggerSource string,
	userID uint,
	isAdmin bool,
	timeoutMs int,
	waitLeaseSec int,
) (*models.ReplayTask, error) {
	return s.ExecuteGoldenSuiteSyncWithCSG(ctx, suiteID, envAgentID, triggerSource, userID, isAdmin, timeoutMs, waitLeaseSec, "", "")
}

// ExecuteGoldenSuiteSyncWithCSG 支持指定 CSG 仿真策略同步执行门禁黄金套件回归
func (s *ReplayScheduler) ExecuteGoldenSuiteSyncWithCSG(
	ctx context.Context,
	suiteID uint64,
	envAgentID uint64,
	triggerSource string,
	userID uint,
	isAdmin bool,
	timeoutMs int,
	waitLeaseSec int,
	csgStrategy string,
	csgAddr string,
) (*models.ReplayTask, error) {
	strategy, sErr := parseCSGStrategyOrErr(csgStrategy)
	if sErr != nil {
		return nil, sErr
	}
	// 1. 加载套件及用例
	var suite models.GoldenSuite
	if err := s.db.WithContext(ctx).Preload("ArchProcess").Preload("Items", func(db *gorm.DB) *gorm.DB {
		return db.Order("order_no ASC")
	}).Where("id = ?", suiteID).First(&suite).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("404 Not Found: 黄金契约套件不存在")
		}
		return nil, fmt.Errorf("查询黄金套件失败: %w", err)
	}

	if len(suite.Items) == 0 {
		return nil, errors.New("400 Bad Request: 黄金套件中无可用用例")
	}

	// 2. 加载目标 Agent 并进行 Anti-IDOR 校验
	var agent models.Agent
	if err := s.db.WithContext(ctx).Where("id = ?", envAgentID).First(&agent).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("404 Not Found: 目标环境 Agent 不存在")
		}
		return nil, fmt.Errorf("查询目标 Agent 失败: %w", err)
	}
	if agent.Scope == models.ScopePrivate && (agent.OwnerID == nil || *agent.OwnerID != userID) && !isAdmin {
		return nil, ErrForbiddenPrivateAgent
	}
	if agent.Status != models.AgentStatusOnline {
		return nil, ErrAgentOffline
	}

	// 3. 查找该 Agent 上绑定的与 suite.ArchProcessID 匹配的目标进程
	var targetProc models.Process
	if err := s.db.WithContext(ctx).Preload("ArchProcess").
		Where("agent_id = ? AND arch_process_id = ?", envAgentID, suite.ArchProcessID).
		First(&targetProc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("400 Bad Request: 目标环境 Agent 未挂载该黄金套件对应的标准架构进程")
		}
		return nil, fmt.Errorf("查询目标进程失败: %w", err)
	}
	targetProc.Agent = &agent

	// 4. 检查进程就绪状态
	if targetProc.ProbeStatus == models.ProbeStatusDisarmed || targetProc.NeedReconnect {
		return nil, errors.New("503 Service Unavailable: GATED_ENV_NOT_READY: 目标进程探针未就绪 (NEED_RECONNECT)")
	}

	if timeoutMs <= 0 {
		timeoutMs = 30000
	}
	if triggerSource == "" {
		triggerSource = models.TriggerSourceCIGate
	}

	// 5. 创建 ReplayTask
	now := time.Now()
	taskNo := fmt.Sprintf("REP_GATE_%s_%d", now.Format("20060102150405"), now.UnixNano()%1000)
	tgtPID := targetProc.ID

	task := &models.ReplayTask{
		TaskNo:            taskNo,
		Name:              fmt.Sprintf("[CI/CD Gate] %s 脱机回归", suite.Name),
		TriggerSource:     triggerSource,
		OwnerID:           userID,
		SessionID:         nil,
		GoldenSuiteID:     &suite.ID,
		ArchProcessID:     suite.ArchProcessID,
		TargetProcessID:   &tgtPID,
		AgentID:           envAgentID,
		SelectedRecordIDs: datatypes.JSON([]byte("[]")),
		ExtraIgnorePaths:  suite.ExtraIgnorePaths,
		TimeoutMs:         timeoutMs,
		CSGStrategy:       string(strategy),
		LifecycleState:    "ARMED",
		TotalRequests:     len(suite.Items),
		CreatedBy:         userID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := s.db.WithContext(ctx).Create(task).Error; err != nil {
		return nil, fmt.Errorf("创建门禁回归任务失败: %w", err)
	}

	// 6. 排他锁有界排队申请 (wait_lease_seconds)
	if waitLeaseSec < 0 {
		waitLeaseSec = 0
	}
	if waitLeaseSec > 300 {
		waitLeaseSec = 300
	}
	leaseTTL := time.Duration(timeoutMs/1000+120) * time.Second
	deadline := time.Now().Add(time.Duration(waitLeaseSec) * time.Second)
	backoff := 50 * time.Millisecond

	for {
		leaseErr := hub.AcquireAgentLeases(ctx, s.db, []uint64{envAgentID}, models.AgentLeaseReplay, task.ID, userID, leaseTTL)
		if leaseErr == nil {
			break
		}
		var conflictErr *hub.LeaseConflictError
		if !errors.As(leaseErr, &conflictErr) {
			s.markTaskFailed(task.ID, leaseErr.Error())
			return nil, leaseErr
		}
		if time.Now().After(deadline) {
			s.markTaskFailed(task.ID, "等待 Agent 排他租约超时")
			return nil, ErrEnvBusy
		}
		select {
		case <-ctx.Done():
			s.markTaskFailed(task.ID, "等待租约上下文取消")
			return nil, ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
			if backoff > 500*time.Millisecond {
				backoff = 500 * time.Millisecond
			}
		}
	}

	// 7. 同步执行重放与回归断言
	procCopy := targetProc
	suiteCopy := suite
	err := s.runGoldenSuiteReplay(ctx, task, &procCopy, &suiteCopy, suite.Items, csgAddr)
	if err != nil {
		return nil, err
	}

	return task, nil
}

func (s *ReplayScheduler) pollGoldenSuiteAsync(
	task *models.ReplayTask,
	targetProc *models.Process,
	suite *models.GoldenSuite,
	items []models.GoldenSuiteItem,
) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(task.TimeoutMs+10000)*time.Millisecond)
	defer cancel()
	_ = s.pollAndEvaluateSuiteReplay(ctx, task, targetProc, suite, items)
}

func (s *ReplayScheduler) executeGoldenSuiteAsync(
	task *models.ReplayTask,
	targetProc *models.Process,
	suite *models.GoldenSuite,
	items []models.GoldenSuiteItem,
	csgAddr string,
) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(task.TimeoutMs+10000)*time.Millisecond)
	defer cancel()
	_ = s.runGoldenSuiteReplay(ctx, task, targetProc, suite, items, csgAddr)
}

func (s *ReplayScheduler) buildSuiteExecReq(
	task *models.ReplayTask,
	targetProc *models.Process,
	items []models.GoldenSuiteItem,
	csgAddr string,
) agentproto.ExecuteReplayReq {
	var ingressRequests [][]byte
	var ingressSvcs []string
	var ingressItems []agentproto.ReplayIngressItem
	scopedEgressMocks := make(map[uint64][]agentproto.MockRuleDownstream)
	var historyEgressEndpoints []agentproto.DownstreamEndpoint
	seenHistoryEP := make(map[string]bool)
	var egressMocks [][]byte

	for _, item := range items {
		ingressRequests = append(ingressRequests, item.RawReqPacket)
		ingressSvcs = append(ingressSvcs, item.ServiceName)
		ingressItems = append(ingressItems, agentproto.ReplayIngressItem{
			RecordID: item.ID,
			RawReq:   item.RawReqPacket,
		})

		if len(item.ScopedMocks) > 0 {
			var mocks []agentproto.MockRuleDownstream
			if err := json.Unmarshal(item.ScopedMocks, &mocks); err == nil && len(mocks) > 0 {
				scopedEgressMocks[item.ID] = mocks
				for _, m := range mocks {
					if len(m.RspPackets) > 0 {
						for _, rp := range m.RspPackets {
							egressMocks = append(egressMocks, rp)
						}
					} else if len(m.ReqPacket) > 0 {
						egressMocks = append(egressMocks, m.ReqPacket)
					}
				}
			}
		}

		if len(item.EgressEndpoints) > 0 {
			var eps []agentproto.DownstreamEndpoint
			if err := json.Unmarshal(item.EgressEndpoints, &eps); err == nil {
				for _, ep := range eps {
					key := fmt.Sprintf("%s:%d", ep.IP, ep.Port)
					if !seenHistoryEP[key] {
						seenHistoryEP[key] = true
						historyEgressEndpoints = append(historyEgressEndpoints, ep)
					}
				}
			}
		}
	}

	downstreams, _ := topology.CompileProcessDownstreams(s.db, targetProc.ID, nil, historyEgressEndpoints)
	targetProc.FillFromArchProcess()
	var targetPIDs []uint32
	if len(targetProc.ObservedPIDs) > 0 {
		_ = json.Unmarshal(targetProc.ObservedPIDs, &targetPIDs)
	}
	var obsTransports []string
	if len(targetProc.ObservedTransports) > 0 {
		_ = json.Unmarshal(targetProc.ObservedTransports, &obsTransports)
	}
	spec := agentproto.ProcessMonitorSpec{
		ProcessID:   targetProc.ID,
		ListenPort:  uint16(targetProc.EffectiveListenPort),
		CmdPattern:  targetProc.CmdPattern,
		TargetPIDs:  targetPIDs,
		Transports:  obsTransports,
		Downstreams: downstreams,
		CSGAddr:     strings.TrimSpace(csgAddr),
	}

	strat := agentproto.CSGReplayStrategy(task.CSGStrategy)
	csgSnaps := mergeSuiteItemCSGSnapshots(items)
	targetSvcs := collectReplayTargetServices(s.db, strat, task.ArchProcessID, ingressSvcs, nil, scopedEgressMocks, csgSnaps)

	return agentproto.ExecuteReplayReq{
		TaskID:            task.ID,
		ProcessSpec:       spec,
		TimeoutMs:         task.TimeoutMs,
		CSGStrategy:       strat,
		CSGAddr:           strings.TrimSpace(csgAddr),
		CSGSnapshots:      csgSnaps,
		TargetServices:    targetSvcs,
		IngressRequests:   ingressRequests,
		EgressMocks:       egressMocks,
		IngressItems:      ingressItems,
		ScopedEgressMocks: scopedEgressMocks,
	}
}

func (s *ReplayScheduler) runGoldenSuiteReplay(
	ctx context.Context,
	task *models.ReplayTask,
	targetProc *models.Process,
	suite *models.GoldenSuite,
	items []models.GoldenSuiteItem,
	csgAddr string,
) error {
	execReq := s.buildSuiteExecReq(task, targetProc, items, csgAddr)
	if err := postExecuteReplayToAgent(ctx, s.httpClient, targetProc.Agent, execReq); err != nil {
		_ = hub.ReleaseAgentLeases(context.Background(), s.db, []uint64{task.AgentID}, models.AgentLeaseReplay, task.ID)
		s.markTaskFailed(task.ID, err.Error())
		return err
	}
	return s.pollAndEvaluateSuiteReplay(ctx, task, targetProc, suite, items)
}

func (s *ReplayScheduler) pollAndEvaluateSuiteReplay(
	ctx context.Context,
	task *models.ReplayTask,
	targetProc *models.Process,
	suite *models.GoldenSuite,
	items []models.GoldenSuiteItem,
) error {
	defer func() {
		_ = hub.ReleaseAgentLeases(context.Background(), s.db, []uint64{task.AgentID}, models.AgentLeaseReplay, task.ID)
	}()

	startedAt := time.Now()
	s.db.Model(&models.ReplayTask{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"lifecycle_state": "INJECTING",
		"started_at":      &startedAt,
	})

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	var result *agentproto.ReplayTaskResultResp
	pollURL := fmt.Sprintf("%s/agent/v1/replays/%d/result", targetProc.Agent.Endpoint, task.ID)

	for {
		select {
		case <-ctx.Done():
			s.markTaskFailed(task.ID, "等待重放执行超时")
			return ctx.Err()
		case <-ticker.C:
			pReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
			if targetProc.Agent.ClaimCode != "" {
				pReq.Header.Set(agentproto.HeaderClaimCode, targetProc.Agent.ClaimCode)
			}
			pResp, pErr := s.httpClient.Do(pReq)
			if pErr == nil && pResp.StatusCode == http.StatusOK {
				var res agentproto.ReplayTaskResultResp
				if decErr := json.NewDecoder(pResp.Body).Decode(&res); decErr == nil {
					_ = pResp.Body.Close()
					result = &res
					goto EVALUATE_SUITE
				}
				_ = pResp.Body.Close()
			}
		}
	}

EVALUATE_SUITE:
	var itemStatuses []string
	var mockHitCount, mockMissCount int
	if result != nil {
		mockHitCount = result.MockHitCount
		mockMissCount = result.MockMissCount
	}

	detailMap := extractReplayItemDetails(result)

	var suiteIgnorePaths []string
	if len(suite.ExtraIgnorePaths) > 0 {
		_ = json.Unmarshal(suite.ExtraIgnorePaths, &suiteIgnorePaths)
	}

	for i, item := range items {
		d, hasDetail := detailMap[item.ID]
		var detailPtr *agentproto.ReplayItemDetail
		if hasDetail {
			detailPtr = &d
		}
		resultFallback := ""
		if result != nil {
			resultFallback = result.Status
		}

		combinedIgnore := append([]string{}, suiteIgnorePaths...)
		if len(item.MaskRulesJSON) > 0 {
			var itemMaskRules []models.MaskRule
			if json.Unmarshal(item.MaskRulesJSON, &itemMaskRules) == nil {
				var maskPaths []string
				for _, mr := range itemMaskRules {
					p := strings.TrimSpace(mr.Path)
					if p != "" {
						maskPaths = append(maskPaths, p)
						if strings.HasPrefix(p, "rsp.") {
							maskPaths = append(maskPaths, strings.TrimPrefix(p, "rsp."))
						}
					}
				}
				combinedIgnore = appendUniqueIgnorePaths(combinedIgnore, maskPaths)
			}
		}

		eval := evaluateReplayItem(
			s.dec,
			item.ServiceName,
			item.Operation,
			"",
			item.RawReqPacket,
			item.RawRspPacket,
			detailPtr,
			hasDetail,
			resultFallback,
			combinedIgnore,
		)

		itemStatuses = append(itemStatuses, eval.ResultStatus)
		diffBytes, _ := json.Marshal(eval.FieldDiffs)
		mockTraceBytes, _ := json.Marshal(eval.MockTrace)
		curItemID := item.ID
		replayItem := models.ReplayItem{
			TaskID:          task.ID,
			SuiteItemID:     &curItemID,
			RecordID:        nil,
			SeqNo:           int64(i + 1),
			ServiceName:     item.ServiceName,
			Operation:       item.Operation,
			ResultStatus:    eval.ResultStatus,
			LatencyUs:       eval.LatencyUs,
			ActualStatus:    models.U64Numeric(eval.ActualStatus),
			BaselineStatus:  item.BaselineStatus,
			FailedLayer:     eval.FailedLayer,
			FieldDiffs:      datatypes.JSON(diffBytes),
			ActualPayload:   eval.ActualPayload,
			RawActualPacket: eval.RawActualPacket,
			MockTrace:       datatypes.JSON(mockTraceBytes),
			ErrorMessage:    eval.ErrorMessage,
			CreatedAt:       time.Now(),
		}
		if createErr := s.db.Create(&replayItem).Error; createErr == nil {
			hub.GetGlobalSSEBroker().BroadcastReplayItemDone(task.ID, replayItem.ID, curItemID, replayItem.ResultStatus, replayItem.LatencyUs)
		}
	}

	finalStatus := AggregateReplayStatus(itemStatuses)
	finishedAt := time.Now()
	passedCount := 0
	for _, st := range itemStatuses {
		if st == models.ReplayResultPass {
			passedCount++
		}
	}

	var observedCalls []agentproto.ObservedEgressCall
	if result != nil && len(result.ObservedEgressCalls) > 0 {
		observedCalls = result.ObservedEgressCalls
	}
	observedBytes, _ := json.Marshal(observedCalls)
	var mockEvents []agentproto.MockTraceEvent
	if result != nil && len(result.MockEvents) > 0 {
		mockEvents = result.MockEvents
	}
	mockEventsBytes, _ := json.Marshal(mockEvents)
	sandboxJSON, violations, _ := verifyObservedWithSandbox(ctx, s.db, task.ArchProcessID, observedCalls, result)
	violationCount := len(violations)

	task.LifecycleState = "COMPLETED"
	task.ResultStatus = finalStatus
	task.FinishedAt = &finishedAt
	task.TotalRequests = len(items)
	task.PassedRequests = passedCount
	task.MockHitCount = mockHitCount
	task.MockMissCount = mockMissCount
	task.MockEvents = datatypes.JSON(mockEventsBytes)
	task.ViolationCount = violationCount
	task.ObservedEgressCalls = datatypes.JSON(observedBytes)
	if len(sandboxJSON) > 0 {
		task.CSGSandboxReport = datatypes.JSON(sandboxJSON)
	}

	updates := map[string]interface{}{
		"lifecycle_state":       "COMPLETED",
		"result_status":         finalStatus,
		"finished_at":           &finishedAt,
		"total_requests":        len(items),
		"passed_requests":       passedCount,
		"mock_hit_count":        mockHitCount,
		"mock_miss_count":       mockMissCount,
		"mock_events":           datatypes.JSON(mockEventsBytes),
		"violation_count":       violationCount,
		"observed_egress_calls": datatypes.JSON(observedBytes),
	}
	if len(sandboxJSON) > 0 {
		updates["csg_sandbox_report"] = datatypes.JSON(sandboxJSON)
	}
	txRes := s.db.Model(&models.ReplayTask{}).Where("id = ? AND lifecycle_state != ?", task.ID, "CANCELLED").Updates(updates)
	if txRes.RowsAffected > 0 {
		hub.GetGlobalSSEBroker().BroadcastReplayCompleted(task.ID, "COMPLETED", finalStatus, len(items), passedCount, mockHitCount, mockMissCount)
	}

	return nil
}
