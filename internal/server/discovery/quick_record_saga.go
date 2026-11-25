package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"code-contract/internal/server/hub"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var (
	// ErrQuickRecordTargetsEmpty 表示未指定任何待直录的进程键
	ErrQuickRecordTargetsEmpty = errors.New("process_keys 不能为空")
	// ErrQuickRecordRequestIDEmpty 表示缺少幂等键 request_id
	ErrQuickRecordRequestIDEmpty = errors.New("request_id 不能为空")
)

// AgentMonitorSyncer 抽象 Agent 录制下发与流挂载动作（生产环境由 Recorder Orchestrator 实现，单测可注入 Mock）
type AgentMonitorSyncer interface {
	SyncMonitors(ctx context.Context, agt *models.Agent, req agentproto.SyncMonitorReq) ([]agentproto.ProcessDiagStatus, error)
	AttachRecordingSession(ctx context.Context, session *models.RecordingSession, agt *models.Agent)
}

// HTTPMonitorSyncer 默认基于 HTTP 的 Agent 监控下发实现（不挂载 WS 流，供无 Orchestrator 场景兜底）
type HTTPMonitorSyncer struct {
	Client *http.Client
}

func (s *HTTPMonitorSyncer) SyncMonitors(ctx context.Context, agt *models.Agent, req agentproto.SyncMonitorReq) ([]agentproto.ProcessDiagStatus, error) {
	if agt == nil {
		return nil, ErrAgentNotFound
	}
	client := s.Client
	if client == nil {
		client = &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(r *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	body, _ := json.Marshal(req)
	url := strings.TrimRight(agt.Endpoint, "/") + "/agent/v1/monitors/sync"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if agt.ClaimCode != "" {
		httpReq.Header.Set(agentproto.HeaderClaimCode, agt.ClaimCode)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent monitors/sync returned status %d", resp.StatusCode)
	}
	var out struct {
		Diagnostics []agentproto.ProcessDiagStatus `json:"diagnostics"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Diagnostics, nil
}

func (s *HTTPMonitorSyncer) AttachRecordingSession(_ context.Context, _ *models.RecordingSession, _ *models.Agent) {
}

// CoverageWarningDTO 直录覆盖度告警提示
type CoverageWarningDTO struct {
	Code        string `json:"code"` // NEED_RECONNECT | LEGACY_AGENT_SINGLE_PID | MESH_REQUIRES_DRT_UPROBE
	ProcessID   uint64 `json:"process_id"`
	ProcessName string `json:"process_name"`
	Transport   string `json:"transport,omitempty"`
	Message     string `json:"message"`
}

// QuickRecordInput 一键直录 4 步 Saga 入参
type QuickRecordInput struct {
	AgentID              uint64
	ProcessKeys          []string
	Name                 string
	Scope                string
	DurationSec          int
	MaxFrames            int
	MaxFrameBytes        int
	CSGAddr              string
	RequestID            string
	IdentityOverrides    map[string]uint64
	ConfirmLowConfidence bool
	OperatorID           uint
	IsAdmin              bool
	beforeStep3Hook      func() error
}

// QuickRecordResult 一键直录 Saga 返回结果
type QuickRecordResult struct {
	Session          *models.RecordingSession `json:"session"`
	CoverageWarnings []CoverageWarningDTO     `json:"coverage_warnings"`
	EnrolledCount    int                      `json:"enrolled_count"`
	TargetDrivers    []string                 `json:"target_drivers"`
	IdempotentHit    bool                     `json:"idempotent_hit"`
}

type quickRecordIdemKey struct {
	userID    uint
	requestID string
}

type quickRecordIdemEntry struct {
	sessionID        uint64
	coverageWarnings []CoverageWarningDTO
	enrolledCount    int
	targetDrivers    []string
	createdAt        time.Time
}

var (
	quickRecordIdemMu sync.Mutex
	quickRecordIdem   = make(map[quickRecordIdemKey]quickRecordIdemEntry)
)

// ResetQuickRecordIdempotencyCache 清空直录幂等缓存（供测试隔离使用）
func ResetQuickRecordIdempotencyCache() {
	quickRecordIdemMu.Lock()
	quickRecordIdem = make(map[quickRecordIdemKey]quickRecordIdemEntry)
	quickRecordIdemMu.Unlock()
}

// ExecuteQuickRecordSaga 按 4 步 Saga（独立 Enroll -> 抢租约 -> 创建会话 -> 下发同步采集）执行一键直录
func ExecuteQuickRecordSaga(
	ctx context.Context,
	db *gorm.DB,
	liveSvc *LiveDiscoveryService,
	syncer AgentMonitorSyncer,
	in QuickRecordInput,
) (*QuickRecordResult, error) {
	if db == nil {
		return nil, errors.New("discovery: nil db")
	}
	if !IsV2Enabled(db) {
		return nil, ErrDiscoveryV2Disabled
	}

	var agent models.Agent
	if err := db.First(&agent, in.AgentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAgentNotFound
		}
		return nil, err
	}
	if err := ScopeEnrollGuard(&agent, in.OperatorID, in.IsAdmin); err != nil {
		return nil, err
	}

	reqID := strings.TrimSpace(in.RequestID)
	if reqID == "" {
		return nil, ErrQuickRecordRequestIDEmpty
	}

	// 清理与去重 process_keys
	seenKeys := make(map[string]struct{}, len(in.ProcessKeys))
	cleanKeys := make([]string, 0, len(in.ProcessKeys))
	for _, k := range in.ProcessKeys {
		trimmed := strings.TrimSpace(k)
		if trimmed == "" {
			continue
		}
		if _, ok := seenKeys[trimmed]; ok {
			continue
		}
		seenKeys[trimmed] = struct{}{}
		cleanKeys = append(cleanKeys, trimmed)
	}
	if len(cleanKeys) == 0 {
		return nil, ErrQuickRecordTargetsEmpty
	}

	// 0. 幂等键 (user_id, request_id) 检查：命中直接返回既有会话与覆盖度告警
	idemKey := quickRecordIdemKey{userID: in.OperatorID, requestID: reqID}
	quickRecordIdemMu.Lock()
	cached, hit := quickRecordIdem[idemKey]
	quickRecordIdemMu.Unlock()
	if hit {
		var existingSess models.RecordingSession
		if err := db.WithContext(ctx).First(&existingSess, cached.sessionID).Error; err == nil {
			return &QuickRecordResult{
				Session:          &existingSess,
				CoverageWarnings: append([]CoverageWarningDTO(nil), cached.coverageWarnings...),
				EnrolledCount:    cached.enrolledCount,
				TargetDrivers:    append([]string(nil), cached.targetDrivers...),
				IdempotentHit:    true,
			}, nil
		}
	}

	if liveSvc == nil {
		return nil, errors.New("discovery: nil liveSvc")
	}
	if syncer == nil {
		syncer = &HTTPMonitorSyncer{}
	}

	// 先读取实况用于过滤本机进程、组装 CSG 快照及判断多 PID
	liveResp, err := liveSvc.GetLiveDiscovery(ctx, in.AgentID, in.CSGAddr, false, in.OperatorID, in.IsAdmin)
	if err != nil {
		return nil, err
	}
	liveByKey := make(map[string]DiscoveryProcessGroupDTO, len(liveResp.Processes))
	for _, p := range liveResp.Processes {
		liveByKey[p.ProcessKey] = p
	}

	enrollItems := make([]EnrollItemInput, 0, len(cleanKeys))
	for _, pk := range cleanKeys {
		lp, ok := liveByKey[pk]
		if !ok || lp.IsRemoteLDC {
			return nil, fmt.Errorf("%w: %s", ErrEnrollTargetMissing, pk)
		}
		item := EnrollItemInput{
			ProcessKey:           pk,
			ConfirmLowConfidence: in.ConfirmLowConfidence,
		}
		if apID, hasOverride := in.IdentityOverrides[pk]; hasOverride && apID > 0 {
			item.ForceArchProcessID = &apID
		}
		enrollItems = append(enrollItems, item)
	}

	// Step 1: 独立事务执行 Enroll（保持 DISARMED；422 歧义/低置信终止并保留已提交部分）
	enrollRes, err := Enroll(ctx, db, liveSvc, EnrollInput{
		AgentID:         in.AgentID,
		CSGAddr:         in.CSGAddr,
		Items:           enrollItems,
		OperatorID:      in.OperatorID,
		IsAdmin:         in.IsAdmin,
		BypassRateLimit: true,
	})
	if err != nil {
		return nil, err
	}

	enrolledProcIDs := make([]uint64, 0, len(enrollRes.Enrolled))
	for _, e := range enrollRes.Enrolled {
		enrolledProcIDs = append(enrolledProcIDs, e.ProcessID)
	}
	var procs []models.Process
	if err := db.WithContext(ctx).Preload("ArchProcess.Modules").Where("id IN ?", enrolledProcIDs).Find(&procs).Error; err != nil {
		return nil, fmt.Errorf("查询已纳管进程失败: %w", err)
	}
	if len(procs) == 0 {
		return nil, errors.New("未找到可录制的已纳管进程")
	}

	// Step 2: 抢占目标 Agent 排他租约（冲突直接返回 409 LEASE_CONFLICT）
	if agent.Status != models.AgentStatusOnline {
		return nil, fmt.Errorf("400 Bad Request: 目标 Agent [%s] 处于离线状态", agent.Name)
	}
	maxDur := in.DurationSec
	if maxDur <= 0 {
		maxDur = 1800
	}
	maxFrames := in.MaxFrames
	if maxFrames <= 0 {
		maxFrames = 100000
	}
	maxBytes := in.MaxFrameBytes
	if maxBytes <= 0 {
		maxBytes = 4194304
	}
	leaseTTL := time.Duration(maxDur+300) * time.Second
	if err := hub.AcquireAgentLeases(ctx, db, []uint64{agent.ID}, models.AgentLeaseRecording, 0, in.OperatorID, leaseTTL); err != nil {
		return nil, err
	}

	// Step 3: 创建 RecordingSession 并绑定 lease_ref_id（失败立即释放 Step 2 租约）
	releaseTempLease := func() {
		_ = hub.ReleaseAgentLeases(context.Background(), db, []uint64{agent.ID}, models.AgentLeaseRecording, 0)
	}

	if in.beforeStep3Hook != nil {
		if hookErr := in.beforeStep3Hook(); hookErr != nil {
			releaseTempLease()
			return nil, hookErr
		}
	}

	scope := strings.ToUpper(strings.TrimSpace(in.Scope))
	if scope != models.ScopeShared && scope != models.ScopePrivate {
		scope = agent.Scope
		if scope != models.ScopeShared && scope != models.ScopePrivate {
			scope = models.ScopePrivate
		}
	}

	sessName := strings.TrimSpace(in.Name)
	if sessName == "" {
		sessName = fmt.Sprintf("直录-%s-%s", agent.Name, time.Now().Format("150405"))
	}

	procSnapshots := make([]models.ProcessSnapshotItem, 0, len(procs))
	for i := range procs {
		procs[i].FillFromArchProcess()
		var modIDs []uint64
		if procs[i].ArchProcess != nil {
			for _, m := range procs[i].ArchProcess.Modules {
				modIDs = append(modIDs, m.ID)
			}
		}
		procSnapshots = append(procSnapshots, models.ProcessSnapshotItem{
			ProcessID:     procs[i].ID,
			ArchProcessID: procs[i].ArchProcessID,
			Code:          procs[i].Code,
			Name:          procs[i].Name,
			ModuleIDs:     modIDs,
			ListenPort:    procs[i].EffectiveListenPort,
		})
	}
	procSnapBytes, _ := json.Marshal(procSnapshots)
	epSnapshots, _ := topology.CompileProcessEndpointSnapshots(ctx, db, []uint64{agent.ID}, enrolledProcIDs)
	epSnapBytes, _ := json.Marshal(epSnapshots)

	// 基于 liveResp 构建会话级 CSGSnapshots 供 Stage 1 归因优先级 ① 使用
	var csgSnaps []agentproto.CSGSnapshotRecord
	nowStr := time.Now().UTC().Format(time.RFC3339)
	for _, pk := range cleanKeys {
		if lp, ok := liveByKey[pk]; ok {
			for _, svc := range lp.Services {
				for _, ep := range svc.Endpoints {
					host, port := parseEndpointHostPort(ep)
					csgSnaps = append(csgSnaps, agentproto.CSGSnapshotRecord{
						ServiceName: svc.ServiceName,
						Addr:        ep,
						Transport:   svc.Transport,
						HostIP:      host,
						Port:        port,
						Source:      "QUICK_RECORD",
						ObservedAt:  nowStr,
					})
				}
			}
		}
	}
	csgSnapBytes, _ := json.Marshal(csgSnaps)
	if len(csgSnaps) == 0 {
		csgSnapBytes = []byte("[]")
	}

	leasedBytes, _ := json.Marshal([]uint64{agent.ID})
	targetProcBytes, _ := json.Marshal(enrolledProcIDs)
	now := time.Now()
	session := &models.RecordingSession{
		SessionNo:          fmt.Sprintf("QREC_%s_%d", now.Format("20060102150405"), now.UnixNano()%10000),
		Name:               sessName,
		Scope:              scope,
		OwnerID:            in.OperatorID,
		Status:             models.SessionStatusPending,
		LeasedAgentIDs:     datatypes.JSON(leasedBytes),
		TargetProcessIDs:   datatypes.JSON(targetProcBytes),
		ProcessSnapshots:   datatypes.JSON(procSnapBytes),
		EndpointSnapshots:  datatypes.JSON(epSnapBytes),
		CSGSnapshots:       datatypes.JSON(csgSnapBytes),
		ProcessDiagnostics: datatypes.JSON([]byte("[]")),
		MaxDurationSec:     maxDur,
		MaxFrames:          maxFrames,
		MaxFrameBytes:      maxBytes,
		CreatedBy:          in.OperatorID,
		CreatedAt:          now,
		UpdatedAt:          now,
		Description:        fmt.Sprintf("Discovery 一键直录 (request_id=%s)", reqID),
	}

	if err := db.WithContext(ctx).Create(session).Error; err != nil {
		releaseTempLease()
		return nil, fmt.Errorf("创建直录会话失败: %w", err)
	}
	if err := db.WithContext(ctx).Model(&models.Agent{}).
		Where("id = ? AND lease_type = ? AND lease_holder_id = ?", agent.ID, models.AgentLeaseRecording, in.OperatorID).
		Update("lease_ref_id", session.ID).Error; err != nil {
		releaseTempLease()
		return nil, fmt.Errorf("绑定会话租约引用失败: %w", err)
	}

	// Step 4: 组装携带 target_pids[] 的监控规格并下发 Agent（失败置 FAILED 并释放租约）
	driverSet := make(map[string]struct{})
	specs := make([]agentproto.ProcessMonitorSpec, 0, len(procs))
	targetPIDsByProc := make(map[uint64][]uint32, len(procs))
	transportsByProc := make(map[uint64][]string, len(procs))

	for i := range procs {
		p := &procs[i]
		p.FillFromArchProcess()

		var obsPIDs []uint32
		if len(p.ObservedPIDs) > 0 {
			_ = json.Unmarshal(p.ObservedPIDs, &obsPIDs)
		}
		targetPIDsByProc[p.ID] = obsPIDs

		var obsTransports []string
		if len(p.ObservedTransports) > 0 {
			_ = json.Unmarshal(p.ObservedTransports, &obsTransports)
		}
		transportsByProc[p.ID] = obsTransports
		for _, tr := range obsTransports {
			switch strings.ToUpper(strings.TrimSpace(tr)) {
			case "TCP":
				driverSet["builtin-tcp"] = struct{}{}
			case "MESH", "DOPHI_MESH":
				driverSet["builtin-drt-uprobe"] = struct{}{}
			}
		}
		if len(obsTransports) == 0 && p.EffectiveListenPort > 0 {
			driverSet["builtin-tcp"] = struct{}{}
		}

		downstreams, _ := topology.CompileProcessDownstreams(db, p.ID, enrolledProcIDs, nil)
		specs = append(specs, agentproto.ProcessMonitorSpec{
			ProcessID:   p.ID,
			ListenPort:  uint16(p.EffectiveListenPort),
			CmdPattern:  p.EffectiveCmdPattern(),
			TargetPIDs:  obsPIDs,
			Downstreams: downstreams,
			CSGAddr:     strings.TrimSpace(in.CSGAddr),
		})
	}

	syncReq := agentproto.SyncMonitorReq{
		SessionID:      session.ID,
		MaxFrameBytes:  maxBytes,
		MaxBufferBytes: 64 * 1024 * 1024,
		Processes:      specs,
	}

	diags, syncErr := syncer.SyncMonitors(ctx, &agent, syncReq)
	if syncErr != nil {
		_ = db.WithContext(ctx).Model(session).Updates(map[string]interface{}{
			"status":      models.SessionStatusFailed,
			"stop_reason": syncErr.Error(),
		}).Error
		_ = hub.ReleaseAgentLeases(context.Background(), db, []uint64{agent.ID}, models.AgentLeaseRecording, session.ID)
		return nil, fmt.Errorf("下发 Agent 采集规格失败: %w", syncErr)
	}

	diagBytes, _ := json.Marshal(diags)
	startedAt := time.Now()
	session.Status = models.SessionStatusRecording
	session.StartedAt = &startedAt
	session.ProcessDiagnostics = datatypes.JSON(diagBytes)
	if err := db.WithContext(ctx).Model(session).Updates(map[string]interface{}{
		"status":              models.SessionStatusRecording,
		"started_at":          session.StartedAt,
		"process_diagnostics": session.ProcessDiagnostics,
	}).Error; err != nil {
		_ = hub.ReleaseAgentLeases(context.Background(), db, []uint64{agent.ID}, models.AgentLeaseRecording, session.ID)
		return nil, fmt.Errorf("更新直录会话状态失败: %w", err)
	}

	syncer.AttachRecordingSession(ctx, session, &agent)

	// 构建覆盖度告警
	diagByProc := make(map[uint64]agentproto.ProcessDiagStatus, len(diags))
	for _, d := range diags {
		diagByProc[d.ProcessID] = d
	}
	warnings := make([]CoverageWarningDTO, 0)
	for i := range procs {
		p := &procs[i]
		trList := transportsByProc[p.ID]
		hasTCP := len(trList) == 0 && p.EffectiveListenPort > 0
		for _, tr := range trList {
			if strings.EqualFold(tr, "TCP") {
				hasTCP = true
			}
		}
		diag := diagByProc[p.ID]
		if hasTCP && (p.ProbeDesiredState != models.ProbeDesiredStateArmed || diag.NeedReconnect) {
			warnings = append(warnings, CoverageWarningDTO{
				Code:        "NEED_RECONNECT",
				ProcessID:   p.ID,
				ProcessName: p.Name,
				Transport:   "TCP",
				Message:     fmt.Sprintf("进程 [%s] 未常驻布防或存在存量直连：入站已完整覆盖，出向仅覆盖录制开始后新建连接；需完整出向采集请由负责人常驻布防或重启连接", p.Name),
			})
		}
		if len(targetPIDsByProc[p.ID]) > 1 && len(diag.ResolvedPIDs) <= 1 {
			warnings = append(warnings, CoverageWarningDTO{
				Code:        "LEGACY_AGENT_SINGLE_PID",
				ProcessID:   p.ID,
				ProcessName: p.Name,
				Message:     fmt.Sprintf("进程 [%s] 下发了 %d 个实例 PID，但 Agent 仅回报 %d 个已挂载 PID（可能为旧版 Agent，已回退单 PID 采集）", p.Name, len(targetPIDsByProc[p.ID]), len(diag.ResolvedPIDs)),
			})
		}
	}

	drivers := make([]string, 0, len(driverSet))
	for d := range driverSet {
		drivers = append(drivers, d)
	}

	quickRecordIdemMu.Lock()
	quickRecordIdem[idemKey] = quickRecordIdemEntry{
		sessionID:        session.ID,
		coverageWarnings: append([]CoverageWarningDTO(nil), warnings...),
		enrolledCount:    len(enrollRes.Enrolled),
		targetDrivers:    append([]string(nil), drivers...),
		createdAt:        time.Now(),
	}
	quickRecordIdemMu.Unlock()

	return &QuickRecordResult{
		Session:          session,
		CoverageWarnings: warnings,
		EnrolledCount:    len(enrollRes.Enrolled),
		TargetDrivers:    drivers,
		IdempotentHit:    false,
	}, nil
}

func parseEndpointHostPort(ep string) (string, uint16) {
	trimmed := strings.TrimSpace(ep)
	trimmed = strings.TrimPrefix(trimmed, "tcp://ipv4:")
	trimmed = strings.TrimPrefix(trimmed, "dophi_mesh://ipv4:")
	trimmed = strings.TrimPrefix(trimmed, "tcp://")
	trimmed = strings.TrimPrefix(trimmed, "dophi_mesh://")
	idx := strings.LastIndex(trimmed, ":")
	if idx <= 0 || idx == len(trimmed)-1 {
		return trimmed, 0
	}
	host := trimmed[:idx]
	var port int
	_, _ = fmt.Sscanf(trimmed[idx+1:], "%d", &port)
	if port < 0 || port > 65535 {
		port = 0
	}
	return host, uint16(port)
}
