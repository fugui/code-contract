package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"code-contract/internal/server/hub"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/datatypes"
)

type mockMonitorSyncer struct {
	syncCalls int
	lastReq   agentproto.SyncMonitorReq
	diags     []agentproto.ProcessDiagStatus
	err       error
	startedID uint64
}

func (m *mockMonitorSyncer) SyncMonitors(ctx context.Context, agt *models.Agent, req agentproto.SyncMonitorReq) ([]agentproto.ProcessDiagStatus, error) {
	m.syncCalls++
	m.lastReq = req
	if m.err != nil {
		return nil, m.err
	}
	if len(m.diags) > 0 {
		return m.diags, nil
	}
	out := make([]agentproto.ProcessDiagStatus, 0, len(req.Processes))
	for _, p := range req.Processes {
		var resolved uint32
		if len(p.TargetPIDs) > 0 {
			resolved = p.TargetPIDs[0]
		}
		out = append(out, agentproto.ProcessDiagStatus{
			ProcessID:    p.ProcessID,
			Status:       "MONITORING",
			ResolvedPID:  resolved,
			ResolvedPIDs: append([]uint32(nil), p.TargetPIDs...),
		})
	}
	return out, nil
}

func (m *mockMonitorSyncer) AttachRecordingSession(ctx context.Context, session *models.RecordingSession, agt *models.Agent) {
	m.startedID = session.ID
}

func TestQuickRecordSaga_StepFailureCompensation_IdempotencyKey_AndNeedReconnectWarning(t *testing.T) {
	db := setupEnrollBaseDB(t)
	ResetQuickRecordIdempotencyCache()

	// 1. 特性开关关闭时应返回 ErrDiscoveryV2Disabled
	_ = db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "discovery.v2_enabled").Update("setting_val", "false").Error
	agent := models.Agent{Name: "qr-agent", Endpoint: "http://10.10.1.1:19001", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	fetcher := &routeByAgentFetcher{
		reports: map[uint64]*agentproto.CSGTopologyReport{
			agent.ID: {
				DiscoveryMode: "CROSTCP_RPCMON",
				Services: []agentproto.CSGServiceInfo{
					{ServiceName: "order.v1.OrderSvc-1", Addr: "tcp://ipv4:10.10.1.1:21001", Transport: "TCP", HostIP: "10.10.1.1", Port: 21001, ServerPID: 2001, ServerProc: "order_svr", IsLocal: true},
					{ServiceName: "order.v1.OrderSvc-2", Addr: "tcp://ipv4:10.10.1.1:21002", Transport: "TCP", HostIP: "10.10.1.1", Port: 21002, ServerPID: 2002, ServerProc: "order_svr", IsLocal: true},
				},
			},
		},
		procs: map[uint64][]agentproto.DiscoveredProcess{
			agent.ID: {
				{PID: 2001, ListenPort: 21001, ExeName: "/opt/bin/order_svr", Cmdline: "/opt/bin/order_svr --port=21001", Transports: []string{"TCP"}},
				{PID: 2002, ListenPort: 21002, ExeName: "/opt/bin/order_svr", Cmdline: "/opt/bin/order_svr --port=21002", Transports: []string{"TCP"}},
			},
		},
	}
	liveSvc := NewLiveDiscoveryService(db, fetcher)

	live, err := liveSvc.GetLiveDiscovery(context.Background(), agent.ID, "", false, 10, false)
	if err != nil {
		t.Fatalf("GetLiveDiscovery: %v", err)
	}
	if len(live.Processes) != 1 || len(live.Processes[0].PIDs) != 2 {
		t.Fatalf("expected 1 folded multi-PID process, got %+v", live.Processes)
	}
	procKey := live.Processes[0].ProcessKey

	syncer := &mockMonitorSyncer{}
	if _, err := ExecuteQuickRecordSaga(context.Background(), db, liveSvc, syncer, QuickRecordInput{
		AgentID:     agent.ID,
		ProcessKeys: []string{procKey},
		RequestID:   "req-disabled",
		OperatorID:  10,
	}); !errors.Is(err, ErrDiscoveryV2Disabled) {
		t.Fatalf("expected ErrDiscoveryV2Disabled, got %v", err)
	}

	enableDiscoveryV2(t, db)

	// 2. Step 1 失败：歧义身份（同指纹 2 个 ArchProcess）-> 422 IDENTITY_AMBIGUOUS，不占租约
	pgrp := models.ArchProcessGroup{Code: "PGRP_QR", Name: "直录组"}
	_ = db.Create(&pgrp).Error
	fp, _, _ := ComputeIdentityFingerprint("/opt/bin/order_svr", "/opt/bin/order_svr --port=21001", "order_svr")
	ap1 := models.ArchProcess{GroupID: pgrp.ID, Code: "order-amb-1", Name: "Order 1", IdentityFingerprint: fp, FingerprintVersion: CurrentFingerprintVersion, Lifecycle: models.ArchProcLifecycleConfirmed}
	ap2 := models.ArchProcess{GroupID: pgrp.ID, Code: "order-amb-2", Name: "Order 2", IdentityFingerprint: fp, FingerprintVersion: CurrentFingerprintVersion, Lifecycle: models.ArchProcLifecycleConfirmed}
	_ = db.Create(&ap1).Error
	_ = db.Create(&ap2).Error
	liveSvc.ResetCache()

	_, err = ExecuteQuickRecordSaga(context.Background(), db, liveSvc, syncer, QuickRecordInput{
		AgentID:     agent.ID,
		ProcessKeys: []string{procKey},
		RequestID:   "req-ambiguous",
		OperatorID:  10,
	})
	var valErr *EnrollValidationError
	if !errors.As(err, &valErr) || valErr.Code != "IDENTITY_AMBIGUOUS" {
		t.Fatalf("expected IDENTITY_AMBIGUOUS, got %v", err)
	}
	var refreshedAgent models.Agent
	_ = db.First(&refreshedAgent, agent.ID).Error
	if refreshedAgent.LeaseType != "" {
		t.Fatalf("Step 1 失败不应占用租约，实际 lease=%q", refreshedAgent.LeaseType)
	}

	// 清理歧义候选，保留唯一身份以便继续验证 Step 2/3/4
	_ = db.Delete(&ap2).Error
	liveSvc.ResetCache()

	// 3. Step 2 失败：Agent 已被他人占用 -> 409 LEASE_CONFLICT，Step 1 独立纳管保留且无副作用
	otherHolder := uint(99)
	exp := time.Now().Add(5 * time.Minute)
	_ = db.Model(&models.Agent{}).Where("id = ?", agent.ID).Updates(map[string]interface{}{
		"lease_type":       models.AgentLeaseRecording,
		"lease_ref_id":     uint64(888),
		"lease_holder_id":  otherHolder,
		"lease_expires_at": exp,
	}).Error

	_, err = ExecuteQuickRecordSaga(context.Background(), db, liveSvc, syncer, QuickRecordInput{
		AgentID:     agent.ID,
		ProcessKeys: []string{procKey},
		RequestID:   "req-lease-conflict",
		OperatorID:  10,
	})
	var leaseErr *hub.LeaseConflictError
	if !errors.As(err, &leaseErr) {
		t.Fatalf("expected LeaseConflictError, got %v", err)
	}
	var enrolledProc models.Process
	if err := db.Where("agent_id = ? AND arch_process_id = ?", agent.ID, ap1.ID).First(&enrolledProc).Error; err != nil {
		t.Fatalf("Step 1 独立事务 Enroll 应在 Step 2 租约冲突后保留 DISARMED 挂载: %v", err)
	}
	if enrolledProc.ProbeDesiredState != models.ProbeDesiredStateDisarmed {
		t.Fatalf("Enroll 挂载必须保持 DISARMED，实际 %s", enrolledProc.ProbeDesiredState)
	}

	// 释放他人租约
	_ = hub.ReleaseAgentLeases(context.Background(), db, []uint64{agent.ID}, models.AgentLeaseRecording, 888)

	// 4. Step 3 失败补偿：建会话失败必须回滚释放 Step 2 获取的 Agent 租约
	_, err = ExecuteQuickRecordSaga(context.Background(), db, liveSvc, syncer, QuickRecordInput{
		AgentID:     agent.ID,
		ProcessKeys: []string{procKey},
		RequestID:   "req-step3-fail",
		OperatorID:  10,
		beforeStep3Hook: func() error {
			return errors.New("simulated step 3 failure")
		},
	})
	if err == nil {
		t.Fatalf("expected Step 3 failure")
	}
	_ = db.First(&refreshedAgent, agent.ID).Error
	if refreshedAgent.LeaseType != "" {
		t.Fatalf("Step 3 失败补偿必须释放 Agent 租约，实际 lease=%q", refreshedAgent.LeaseType)
	}

	// 5. Step 4 失败补偿：Agent 下发失败 -> 会话置 FAILED 且释放 Agent 租约
	syncerFail := &mockMonitorSyncer{err: errors.New("agent unreachable")}
	_, err = ExecuteQuickRecordSaga(context.Background(), db, liveSvc, syncerFail, QuickRecordInput{
		AgentID:     agent.ID,
		ProcessKeys: []string{procKey},
		RequestID:   "req-step4-fail",
		OperatorID:  10,
	})
	if err == nil {
		t.Fatalf("expected Step 4 failure")
	}
	_ = db.First(&refreshedAgent, agent.ID).Error
	if refreshedAgent.LeaseType != "" {
		t.Fatalf("Step 4 失败补偿必须释放 Agent 租约，实际 lease=%q", refreshedAgent.LeaseType)
	}
	var failedSess models.RecordingSession
	if err := db.Order("id DESC").First(&failedSess).Error; err != nil || failedSess.Status != models.SessionStatusFailed {
		t.Fatalf("Step 4 失败应将会话标记为 FAILED，实际 err=%v status=%s", err, failedSess.Status)
	}

	// 6. 全链路成功：携带 target_pids=[2001, 2002]、输出 NEED_RECONNECT 覆盖度警告、request_id 幂等去重
	res1, err := ExecuteQuickRecordSaga(context.Background(), db, liveSvc, syncer, QuickRecordInput{
		AgentID:     agent.ID,
		ProcessKeys: []string{procKey},
		RequestID:   "req-success-1",
		OperatorID:  10,
	})
	if err != nil {
		t.Fatalf("ExecuteQuickRecordSaga success failed: %v", err)
	}
	if res1.IdempotentHit {
		t.Fatalf("first call should not be idempotent hit")
	}
	if res1.Session.Status != models.SessionStatusRecording {
		t.Fatalf("expected RECORDING status, got %s", res1.Session.Status)
	}
	if syncer.syncCalls != 1 || len(syncer.lastReq.Processes) != 1 {
		t.Fatalf("expected 1 Agent sync call with 1 process, got calls=%d procs=%d", syncer.syncCalls, len(syncer.lastReq.Processes))
	}
	gotPIDs := syncer.lastReq.Processes[0].TargetPIDs
	if len(gotPIDs) != 2 || gotPIDs[0] != 2001 || gotPIDs[1] != 2002 {
		t.Fatalf("expected TargetPIDs=[2001, 2002], got %v", gotPIDs)
	}
	hasNeedReconnect := false
	for _, w := range res1.CoverageWarnings {
		if w.Code == "NEED_RECONNECT" {
			hasNeedReconnect = true
		}
	}
	if !hasNeedReconnect {
		t.Fatalf("expected NEED_RECONNECT coverage warning for non-armed TCP process, got %+v", res1.CoverageWarnings)
	}

	// 同 (user_id, request_id) 重复提交 -> 直接命中幂等返回同一 Session，不触发二次 sync 或 409
	res2, err := ExecuteQuickRecordSaga(context.Background(), db, liveSvc, syncer, QuickRecordInput{
		AgentID:     agent.ID,
		ProcessKeys: []string{procKey},
		RequestID:   "req-success-1",
		OperatorID:  10,
	})
	if err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	if !res2.IdempotentHit || res2.Session.ID != res1.Session.ID {
		t.Fatalf("expected idempotent hit returning session %d, got hit=%v id=%d", res1.Session.ID, res2.IdempotentHit, res2.Session.ID)
	}
	if syncer.syncCalls != 1 {
		t.Fatalf("idempotent retry must not invoke Agent sync again, calls=%d", syncer.syncCalls)
	}
}

func TestSimulationPlan_RecommendsStrategyBySubState(t *testing.T) {
	db := setupEnrollBaseDB(t)

	ownerID := uint(11)
	otherID := uint(22)
	sharedAgent := models.Agent{Name: "sim-shared", Endpoint: "http://10.20.1.1:19001", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	privAgent := models.Agent{Name: "sim-priv", Endpoint: "http://10.20.1.2:19001", Scope: models.ScopePrivate, OwnerID: &ownerID, Status: models.AgentStatusOnline}
	_ = db.Create(&sharedAgent).Error
	_ = db.Create(&privAgent).Error

	pgrp := models.ArchProcessGroup{Code: "PGRP_SIM", Name: "仿真推荐组"}
	_ = db.Create(&pgrp).Error
	apMesh := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-mesh-cli", Name: "Mesh消费者"}
	apOffline := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-off-cli", Name: "离线下游消费者"}
	apTCP := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-tcp-cli", Name: "在线TCP消费者"}
	apPriv := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-priv", Name: "私有进程"}
	_ = db.Create(&apMesh).Error
	_ = db.Create(&apOffline).Error
	_ = db.Create(&apTCP).Error
	_ = db.Create(&apPriv).Error

	procMesh := models.Process{AgentID: sharedAgent.ID, ArchProcessID: apMesh.ID, EffectiveListenPort: 22001, ExtraDownstreams: datatypes.JSON("[]"), EnrollSource: models.EnrollSourceDiscovery, ProbeDesiredState: models.ProbeDesiredStateDisarmed}
	procOffline := models.Process{AgentID: sharedAgent.ID, ArchProcessID: apOffline.ID, EffectiveListenPort: 22002, ExtraDownstreams: datatypes.JSON("[]"), EnrollSource: models.EnrollSourceDiscovery, ProbeDesiredState: models.ProbeDesiredStateDisarmed}
	procTCP := models.Process{AgentID: sharedAgent.ID, ArchProcessID: apTCP.ID, EffectiveListenPort: 22003, ExtraDownstreams: datatypes.JSON("[]"), EnrollSource: models.EnrollSourceDiscovery, ProbeDesiredState: models.ProbeDesiredStateDisarmed}
	procPriv := models.Process{AgentID: privAgent.ID, ArchProcessID: apPriv.ID, EffectiveListenPort: 22004, ExtraDownstreams: datatypes.JSON("[]"), EnrollSource: models.EnrollSourceDiscovery, ProbeDesiredState: models.ProbeDesiredStateDisarmed}
	_ = db.Create(&procMesh).Error
	_ = db.Create(&procOffline).Error
	_ = db.Create(&procTCP).Error
	_ = db.Create(&procPriv).Error

	// 写入 ProcessDependency 场景
	_ = db.Create(&models.ProcessDependency{
		ConsumerProcessID: procMesh.ID, ServiceName: "mesh.v1.PaySvc",
		ProviderKind: models.ProviderKindLocalEnrolled, ResolvedEndpoint: "dophi_mesh://ipv4:10.20.1.1:23001", Status: models.PlacementStatusActive,
	}).Error
	_ = db.Create(&models.ProcessDependency{
		ConsumerProcessID: procOffline.ID, ServiceName: "off.v1.AbsentSvc",
		ProviderKind: models.ProviderKindOffline, ResolvedEndpoint: "", Status: models.PlacementStatusActive,
	}).Error
	_ = db.Create(&models.ProcessDependency{
		ConsumerProcessID: procTCP.ID, ServiceName: "tcp.v1.UserSvc",
		ProviderKind: models.ProviderKindLocalEnrolled, ResolvedEndpoint: "tcp://ipv4:10.20.1.1:23002", Status: models.PlacementStatusActive,
	}).Error

	liveSvc := NewLiveDiscoveryService(db, &routeByAgentFetcher{
		reports: map[uint64]*agentproto.CSGTopologyReport{
			sharedAgent.ID: {
				DiscoveryMode: "CROSTCP_RPCMON",
				Services: []agentproto.CSGServiceInfo{
					{ServiceName: "tcp.v1.LocalSvc", Addr: "tcp://ipv4:10.20.1.1:22003", Transport: "TCP", Port: 22003, ServerPID: 3003, IsLocal: true},
				},
			},
			privAgent.ID: {DiscoveryMode: "CROSTCP_RPCMON"},
		},
		procs: map[uint64][]agentproto.DiscoveredProcess{
			sharedAgent.ID: {
				{PID: 3001, ListenPort: 22001, ExeName: "/opt/bin/ap_mesh", Cmdline: "/opt/bin/ap_mesh"},
				{PID: 3002, ListenPort: 22002, ExeName: "/opt/bin/ap_off", Cmdline: "/opt/bin/ap_off"},
				{PID: 3003, ListenPort: 22003, ExeName: "/opt/bin/ap_tcp", Cmdline: "/opt/bin/ap_tcp"},
			},
			privAgent.ID: {
				{PID: 3004, ListenPort: 22004, ExeName: "/opt/bin/ap_priv", Cmdline: "/opt/bin/ap_priv"},
			},
		},
	})

	// 1. 纯 Mesh 下游 -> MOCK_SERVER
	planMesh, err := BuildSimulationPlan(context.Background(), db, liveSvc, SimulationPlanInput{
		AgentID: sharedAgent.ID, ArchProcessID: apMesh.ID, UserID: otherID,
	})
	if err != nil {
		t.Fatalf("BuildSimulationPlan mesh failed: %v", err)
	}
	if planMesh.RecommendedStrategy != string(agentproto.CSGStrategyMockServer) {
		t.Fatalf("expected MOCK_SERVER for mesh downstream, got %s", planMesh.RecommendedStrategy)
	}

	// 2. 全部下游缺席 -> PUB_STUB
	planPub, err := BuildSimulationPlan(context.Background(), db, liveSvc, SimulationPlanInput{
		AgentID: sharedAgent.ID, ArchProcessID: apOffline.ID, UserID: otherID,
	})
	if err != nil {
		t.Fatalf("BuildSimulationPlan offline failed: %v", err)
	}
	if planPub.RecommendedStrategy != string(agentproto.CSGStrategyPubStub) {
		t.Fatalf("expected PUB_STUB for offline downstreams, got %s", planPub.RecommendedStrategy)
	}

	// 3. 在线 TCP 动态端口 -> CLIENT_INJECT
	planTCP, err := BuildSimulationPlan(context.Background(), db, liveSvc, SimulationPlanInput{
		AgentID: sharedAgent.ID, ArchProcessID: apTCP.ID, UserID: otherID,
	})
	if err != nil {
		t.Fatalf("BuildSimulationPlan tcp failed: %v", err)
	}
	if planTCP.RecommendedStrategy != string(agentproto.CSGStrategyClientInject) {
		t.Fatalf("expected CLIENT_INJECT for online TCP downstream, got %s", planTCP.RecommendedStrategy)
	}

	// 4. PRIVATE Agent 越权拦截
	if _, err := BuildSimulationPlan(context.Background(), db, liveSvc, SimulationPlanInput{
		AgentID: privAgent.ID, ArchProcessID: apPriv.ID, UserID: otherID,
	}); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("expected ErrAgentForbidden for other user on PRIVATE agent, got %v", err)
	}
}
