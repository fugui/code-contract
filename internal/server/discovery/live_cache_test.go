package discovery

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code-common/backend/testdb"
	"code-contract/config"
	"code-contract/internal/server/hub"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/datatypes"
)

type mockDiscoveryFetcher struct {
	mu          sync.Mutex
	topoCalls   atomic.Int32
	procCalls   atomic.Int32
	delay       time.Duration
	topoReport  *agentproto.CSGTopologyReport
	topoErr     error
	discovered  []agentproto.DiscoveredProcess
	discoverErr error
}

func (m *mockDiscoveryFetcher) FetchAgentCSGTopology(ctx context.Context, agentID uint64, csgAddr string, currentUserID uint, isAdmin bool) (*agentproto.CSGTopologyReport, error) {
	m.topoCalls.Add(1)
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.topoErr != nil {
		return nil, m.topoErr
	}
	return m.topoReport, nil
}

func (m *mockDiscoveryFetcher) FetchAgentDiscoveredProcesses(ctx context.Context, agentID uint64, currentUserID uint, isAdmin bool) ([]agentproto.DiscoveredProcess, error) {
	m.procCalls.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.discoverErr != nil {
		return nil, m.discoverErr
	}
	return m.discovered, nil
}

func TestAggregateProcessCentric_MultiPIDFold_PureClient_AndRemoteLDCSeparation(t *testing.T) {
	report := &agentproto.CSGTopologyReport{
		DiscoveryMode: "CROSTCP_RPCMON",
		CSGEndpoints: []agentproto.CSGEndpointInfo{
			{PID: 100, ExeName: "csg_server", ListenIP: "127.0.0.1", ListenPort: 31806, Source: "CLI_FLAG"},
		},
		Services: []agentproto.CSGServiceInfo{
			{
				ServiceName: "OrderSvc-0-1001",
				Addr:        "tcp://ipv4:10.0.0.1:21001",
				Transport:   "TCP",
				HostIP:      "10.0.0.1",
				Port:        21001,
				ServerPID:   1001,
				ServerProc:  "order_svr",
				IsLocal:     true,
			},
			{
				ServiceName: "OrderSvc-0-1002",
				Addr:        "tcp://ipv4:10.0.0.1:21002",
				Transport:   "TCP",
				HostIP:      "10.0.0.1",
				Port:        21002,
				ServerPID:   1002,
				ServerProc:  "order_svr",
				IsLocal:     true,
			},
			{
				ServiceName: "RemoteAuthSvc-RemoteAuthSvc",
				Addr:        "dophi_mesh://ipv4:10.0.9.9:32001",
				Transport:   "MESH",
				HostIP:      "10.0.9.9",
				Port:        32001,
				ServerPID:   9001,
				ServerProc:  "remote_auth_svr",
				OsNodeID:    2,
				IsLocal:     false,
				IsRemoteLDC: true,
			},
		},
		Subscriptions: []agentproto.CSGSubscription{
			{
				ClientPID:   1001,
				ClientProc:  "order_svr",
				ClientAddr:  "10.0.0.1",
				ServiceName: "RemoteAuthSvc-RemoteAuthSvc",
				IsLocal:     true,
			},
			{
				ClientPID:   2001,
				ClientProc:  "pure_cli_app",
				ClientAddr:  "10.0.0.1",
				ServiceName: "OrderSvc-0-1001",
				IsLocal:     true,
			},
		},
	}

	discovered := []agentproto.DiscoveredProcess{
		{PID: 1001, ListenPort: 21001, ExeName: "order_svr", Cmdline: "/opt/bin/order_svr --port=21001 --password=s3cr3t"},
		{PID: 1002, ListenPort: 21002, ExeName: "order_svr", Cmdline: "/opt/bin/order_svr --port=21002 --password=s3cr3t"},
		{PID: 2001, ListenPort: 0, ExeName: "pure_cli_app", Cmdline: "/opt/bin/pure_cli_app --worker=1", IsPureClient: true},
	}

	resp, err := AggregateProcessCentric(nil, 1, report, discovered)
	if err != nil {
		t.Fatalf("AggregateProcessCentric failed: %v", err)
	}

	// 1. Local processes: order_svr (folded 2 PIDs) + pure_cli_app (pure client)
	if len(resp.Processes) != 2 {
		t.Fatalf("expected 2 local process groups, got %d: %+v", len(resp.Processes), resp.Processes)
	}

	var orderGrp, cliGrp *DiscoveryProcessGroupDTO
	for i := range resp.Processes {
		switch resp.Processes[i].ProcName {
		case "order_svr":
			orderGrp = &resp.Processes[i]
		case "pure_cli_app":
			cliGrp = &resp.Processes[i]
		}
	}
	if orderGrp == nil {
		t.Fatalf("missing order_svr group")
	}
	if len(orderGrp.PIDs) != 2 || orderGrp.PIDs[0] != 1001 || orderGrp.PIDs[1] != 1002 {
		t.Fatalf("expected folded PIDs [1001, 1002], got %v", orderGrp.PIDs)
	}
	if orderGrp.ObservedRole != "DUAL" {
		t.Fatalf("expected order_svr role DUAL, got %s", orderGrp.ObservedRole)
	}
	if len(orderGrp.Services) != 1 || orderGrp.Services[0].ServiceName != "OrderSvc-0" || orderGrp.Services[0].InstanceCount != 2 {
		t.Fatalf("expected normalized folded service OrderSvc-0 with instance_count=2, got %+v", orderGrp.Services)
	}
	if orderGrp.Cmdline == "" || containsAny(orderGrp.Cmdline, "s3cr3t") {
		t.Fatalf("expected redacted cmdline, got %q", orderGrp.Cmdline)
	}

	if cliGrp == nil {
		t.Fatalf("expected pure_cli_app first-class group")
	}
	if cliGrp.ObservedRole != "CLIENT_ONLY" || len(cliGrp.Services) != 0 {
		t.Fatalf("expected pure client CLIENT_ONLY with 0 services, got role=%s services=%d", cliGrp.ObservedRole, len(cliGrp.Services))
	}
	if !reflect.DeepEqual(cliGrp.ObservedTransports, []string{"TCP"}) {
		t.Fatalf("expected pure client observed_transports [TCP] inferred from subscription, got %v", cliGrp.ObservedTransports)
	}
	if len(cliGrp.Subscriptions) != 1 || cliGrp.Subscriptions[0].ProviderKind != "LOCAL_UNENROLLED" {
		t.Fatalf("expected pure client subscription resolved as LOCAL_UNENROLLED, got %+v", cliGrp.Subscriptions)
	}

	// 2. Remote LDC processes separated into remote_ldc_processes
	if len(resp.RemoteLDCProcesses) != 1 {
		t.Fatalf("expected 1 remote LDC process, got %d", len(resp.RemoteLDCProcesses))
	}
	if !resp.RemoteLDCProcesses[0].IsRemoteLDC || resp.RemoteLDCProcesses[0].ProcName != "remote_auth_svr" {
		t.Fatalf("unexpected remote LDC process: %+v", resp.RemoteLDCProcesses[0])
	}
	if len(orderGrp.Subscriptions) != 1 || orderGrp.Subscriptions[0].ProviderKind != "REMOTE_LDC" {
		t.Fatalf("expected order_svr subscription to RemoteAuthSvc marked REMOTE_LDC, got %+v", orderGrp.Subscriptions)
	}
}

func TestLiveDiscovery_SingleflightTTLCache_RefreshRateLimit_AndOfflineFallbackSnapshot(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB failed: %v", err)
	}

	agent := models.Agent{
		Name:     "live-cache-agent",
		Endpoint: "http://127.0.0.1:19091",
		Scope:    models.ScopeShared,
		Status:   models.AgentStatusOnline,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent failed: %v", err)
	}

	fetcher := &mockDiscoveryFetcher{
		delay: 40 * time.Millisecond,
		topoReport: &agentproto.CSGTopologyReport{
			DiscoveryMode: "CROSTCP_RPCMON",
			CSGEndpoints: []agentproto.CSGEndpointInfo{
				{PID: 100, ExeName: "csg_server", ListenIP: "127.0.0.1", ListenPort: 31806, Source: "CLI_FLAG"},
			},
			Services: []agentproto.CSGServiceInfo{
				{ServiceName: "PaySvc-PaySvc", Addr: "tcp://ipv4:127.0.0.1:22001", Transport: "TCP", ServerPID: 3001, ServerProc: "pay_svr", IsLocal: true},
			},
			CapturedAt: time.Now().UTC().Format(time.RFC3339),
		},
		discovered: []agentproto.DiscoveredProcess{
			{PID: 3001, ListenPort: 22001, ExeName: "pay_svr", Cmdline: "/opt/pay_svr --token=abc123"},
		},
	}

	svc := NewLiveDiscoveryService(db, fetcher)

	// 1. Concurrent uncached requests collapse via singleflight into 1 RPC
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := svc.GetLiveDiscovery(context.Background(), agent.ID, "127.0.0.1:31806", false, 1, false)
			if err != nil {
				errs <- err
				return
			}
			if res.Stale || len(res.Processes) != 1 {
				errs <- errors.New("unexpected live response")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent live discovery error: %v", err)
	}
	if got := fetcher.topoCalls.Load(); got != 1 {
		t.Fatalf("expected 1 singleflight FetchAgentCSGTopology call, got %d", got)
	}

	// 2. Subsequent non-refresh call hits 5s TTL cache (still 1 RPC total)
	cached, err := svc.GetLiveDiscovery(context.Background(), agent.ID, "127.0.0.1:31806", false, 2, false)
	if err != nil {
		t.Fatalf("cached GetLiveDiscovery failed: %v", err)
	}
	if cached.Stale || fetcher.topoCalls.Load() != 1 {
		t.Fatalf("expected TTL cache hit without extra RPC, calls=%d", fetcher.topoCalls.Load())
	}

	// 3. First manual refresh succeeds; immediate second refresh within 3s is rate limited (429)
	if _, err := svc.GetLiveDiscovery(context.Background(), agent.ID, "127.0.0.1:31806", true, 2, false); err != nil {
		t.Fatalf("first manual refresh failed: %v", err)
	}
	if fetcher.topoCalls.Load() != 2 {
		t.Fatalf("expected 2 RPC calls after manual refresh, got %d", fetcher.topoCalls.Load())
	}
	if _, err := svc.GetLiveDiscovery(context.Background(), agent.ID, "127.0.0.1:31806", true, 2, false); !errors.Is(err, ErrRefreshRateLimited) {
		t.Fatalf("expected ErrRefreshRateLimited on immediate second refresh, got %v", err)
	}

	// 4. Save a snapshot, then mark agent OFFLINE -> fallback returns stale=true snapshot
	saveRes, err := SaveSnapshot(db, SaveSnapshotInput{
		AgentID:     agent.ID,
		CSGAddr:     "127.0.0.1:31806",
		TriggerMode: models.DiscoveryTriggerManual,
		Label:       "before-offline",
		CreatedBy:   2,
		Live:        cached,
	})
	if err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	if err := db.Model(&models.Agent{}).Where("id = ?", agent.ID).Update("status", models.AgentStatusOffline).Error; err != nil {
		t.Fatalf("set agent offline failed: %v", err)
	}
	fetcher.mu.Lock()
	fetcher.topoErr = hub.ErrAgentOffline
	fetcher.mu.Unlock()

	svc.ResetCache()
	fallback, err := svc.GetLiveDiscovery(context.Background(), agent.ID, "127.0.0.1:31806", false, 2, false)
	if err != nil {
		t.Fatalf("expected offline fallback snapshot, got err: %v", err)
	}
	if !fallback.Stale || fallback.SnapshotID == nil || *fallback.SnapshotID != saveRes.Snapshot.ID {
		t.Fatalf("expected stale=true fallback to snapshot #%d, got stale=%v snapshot_id=%v", saveRes.Snapshot.ID, fallback.Stale, fallback.SnapshotID)
	}
}

func TestAggregateProcessCentric_FiltersUnrelatedHostProcessesAndInfersClientTransports(t *testing.T) {
	discovered := []agentproto.DiscoveredProcess{
		// 宿主机无关监听进程（0 服务、0 订阅、未纳管）
		{PID: 1, ListenPort: 111, ExeName: "systemd", Cmdline: "/usr/lib/systemd/systemd --switched-root"},
		{PID: 1287, ListenPort: 22, ExeName: "sshd", Cmdline: "sshd: /usr/sbin/sshd -D"},
		{PID: 1320, ListenPort: 5432, ExeName: "postgres", Cmdline: "/usr/lib/postgresql/16/bin/postgres -D /var/lib/postgresql/16/main"},
		{PID: 23000, ListenPort: 18090, ExeName: "code-contract-agent", Cmdline: "/opt/code-contract/code-contract-agent --listen=:18090"},
		{PID: 23010, ListenPort: 31806, ExeName: "csgserver", Cmdline: "/opt/bin/csgserver --addr=127.0.0.1:31806"},
		// CSG 业务进程
		{PID: 24002, ListenPort: 25002, ExeName: "tcp_svr", Cmdline: "/opt/bin/tcp_svr --port=25002"},
		{PID: 24003, ListenPort: 0, ExeName: "mesh_svr", Cmdline: "/opt/bin/mesh_svr --mesh"},
		{PID: 24004, ListenPort: 0, ExeName: "cli_tcp", Cmdline: "/opt/bin/cli_tcp", IsPureClient: true},
		{PID: 24005, ListenPort: 0, ExeName: "cli_mesh", Cmdline: "/opt/bin/cli_mesh", IsPureClient: true},
		{PID: 24006, ListenPort: 0, ExeName: "cli_offline", Cmdline: "/opt/bin/cli_offline", IsPureClient: true},
	}

	baseServices := []agentproto.CSGServiceInfo{
		{ServiceName: "biz.v1.TcpSvc", Addr: "tcp://ipv4:127.0.0.1:25002", Transport: "TCP", Port: 25002, ServerPID: 24002, ServerProc: "tcp_svr", IsLocal: true},
		{ServiceName: "biz.v1.MeshSvc", Addr: "dophi_mesh://ipv4:127.0.0.1:25003", Transport: "MESH", Port: 25003, ServerPID: 24003, ServerProc: "mesh_svr", IsLocal: true},
	}
	baseSubs := []agentproto.CSGSubscription{
		{ClientPID: 24004, ClientProc: "cli_tcp", ServiceName: "biz.v1.TcpSvc", IsLocal: true},
		{ClientPID: 24005, ClientProc: "cli_mesh", ServiceName: "biz.v1.MeshSvc", IsLocal: true},
		{ClientPID: 24006, ClientProc: "cli_offline", ServiceName: "biz.v1.MissingSvc", IsLocal: true},
	}

	// 1. CROSTCP_RPCMON / CROSMESH_RPCMON 模式：过滤无关宿主机进程，保留业务进程并推导纯客户端制式
	for _, mode := range []string{"CROSTCP_RPCMON", "CROSMESH_RPCMON"} {
		report := &agentproto.CSGTopologyReport{
			DiscoveryMode: mode,
			Services:      baseServices,
			Subscriptions: baseSubs,
		}
		resp, err := AggregateProcessCentric(nil, 1, report, discovered)
		if err != nil {
			t.Fatalf("mode=%s AggregateProcessCentric failed: %v", mode, err)
		}
		if len(resp.Processes) != 5 {
			t.Fatalf("mode=%s expected 5 processes (2 servers + 3 pure clients), got %d", mode, len(resp.Processes))
		}
		if resp.Summary.ProcessCount != 5 {
			t.Fatalf("mode=%s expected summary.process_count=5, got %d", mode, resp.Summary.ProcessCount)
		}
		if resp.Summary.ClientOnlyCount != 3 {
			t.Fatalf("mode=%s expected summary.client_only_count=3, got %d", mode, resp.Summary.ClientOnlyCount)
		}

		byName := make(map[string]DiscoveryProcessGroupDTO, len(resp.Processes))
		for _, p := range resp.Processes {
			byName[p.ProcName] = p
		}
		for _, noise := range []string{"systemd", "sshd", "postgres", "code-contract-agent", "csgserver"} {
			if _, exists := byName[noise]; exists {
				t.Fatalf("mode=%s expected host noise process %q to be filtered out", mode, noise)
			}
		}
		cliTCP, ok := byName["cli_tcp"]
		if !ok {
			t.Fatalf("mode=%s expected cli_tcp to be present, got keys=%v", mode, byName)
		}
		if !reflect.DeepEqual(cliTCP.ObservedTransports, []string{"TCP"}) {
			t.Fatalf("mode=%s expected cli_tcp observed_transports [TCP], got %v", mode, cliTCP.ObservedTransports)
		}
		cliMesh, ok := byName["cli_mesh"]
		if !ok {
			t.Fatalf("mode=%s expected cli_mesh to be present", mode)
		}
		if !reflect.DeepEqual(cliMesh.ObservedTransports, []string{"MESH"}) {
			t.Fatalf("mode=%s expected cli_mesh observed_transports [MESH], got %v", mode, cliMesh.ObservedTransports)
		}
		cliOffline, ok := byName["cli_offline"]
		if !ok {
			t.Fatalf("mode=%s expected cli_offline to be present", mode)
		}
		if cliOffline.ObservedTransports == nil || len(cliOffline.ObservedTransports) != 0 {
			t.Fatalf("mode=%s expected cli_offline observed_transports empty non-nil slice, got %#v", mode, cliOffline.ObservedTransports)
		}
	}

	// 2. PROC_ONLY 及空 DiscoveryMode 降级模式：完整保留 /proc 扫描进程作为兜底
	for _, mode := range []string{"PROC_ONLY", ""} {
		report := &agentproto.CSGTopologyReport{
			DiscoveryMode: mode,
		}
		resp, err := AggregateProcessCentric(nil, 1, report, discovered)
		if err != nil {
			t.Fatalf("fallback mode=%q failed: %v", mode, err)
		}
		if len(resp.Processes) != len(discovered) {
			t.Fatalf("fallback mode=%q expected all %d /proc processes retained, got %d", mode, len(discovered), len(resp.Processes))
		}
		if resp.Summary.ProcessCount != len(discovered) {
			t.Fatalf("fallback mode=%q expected summary.process_count=%d, got %d", mode, len(discovered), resp.Summary.ProcessCount)
		}
	}
}

func TestAggregateProcessCentric_RetainsEnrolledProcessWithoutCSGActivity(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB failed: %v", err)
	}

	agent := models.Agent{
		Name:     "filter-test-agent",
		Endpoint: "http://127.0.0.1:19099",
		Scope:    models.ScopeShared,
		Status:   models.AgentStatusOnline,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent failed: %v", err)
	}

	pgrp := models.ArchProcessGroup{Code: "PGRP_FILTER", Name: "过滤测试进程组"}
	if err := db.Create(&pgrp).Error; err != nil {
		t.Fatalf("create arch process group failed: %v", err)
	}

	fpEnrolled, _, _ := ComputeIdentityFingerprint("/opt/bin/enrolled_idle", "/opt/bin/enrolled_idle --daemon", "enrolled_idle")
	apEnrolled := models.ArchProcess{
		GroupID:             pgrp.ID,
		Code:                "enrolled-idle",
		Name:                "已纳管无CSG进程",
		DefaultCmdPattern:   "enrolled_idle --daemon",
		IdentityFingerprint: fpEnrolled,
		FingerprintVersion:  CurrentFingerprintVersion,
		Lifecycle:           models.ArchProcLifecycleConfirmed,
	}
	if err := db.Create(&apEnrolled).Error; err != nil {
		t.Fatalf("create arch process failed: %v", err)
	}

	procEnrolled := models.Process{
		AgentID:             agent.ID,
		ArchProcessID:       apEnrolled.ID,
		EffectiveListenPort: 25001,
		OverrideCmdPattern:  "enrolled_idle --daemon",
		ExtraDownstreams:    datatypes.JSON("[]"),
		EnrollSource:        models.EnrollSourceDiscovery,
		ProbeDesiredState:   models.ProbeDesiredStateDisarmed,
		ProbeStatus:         models.ProbeStatusDisarmed,
	}
	if err := db.Create(&procEnrolled).Error; err != nil {
		t.Fatalf("create enrolled process failed: %v", err)
	}

	discovered := []agentproto.DiscoveredProcess{
		{PID: 1, ListenPort: 111, ExeName: "systemd", Cmdline: "/usr/lib/systemd/systemd --switched-root"},
		{PID: 24001, ListenPort: 25001, ExeName: "enrolled_idle", Cmdline: "/opt/bin/enrolled_idle --daemon"},
	}
	report := &agentproto.CSGTopologyReport{
		DiscoveryMode: "CROSTCP_RPCMON",
	}

	resp, err := AggregateProcessCentricForUser(db, agent.ID, report, discovered, 1, true)
	if err != nil {
		t.Fatalf("AggregateProcessCentricForUser failed: %v", err)
	}
	if len(resp.Processes) != 1 || resp.Processes[0].ProcName != "enrolled_idle" {
		t.Fatalf("expected enrolled_idle to be retained and systemd filtered, got %+v", resp.Processes)
	}
	if resp.Summary.ProcessCount != 1 || resp.Summary.ClientOnlyCount != 1 {
		t.Fatalf("expected summary process_count=1 client_only_count=1, got %+v", resp.Summary)
	}
}

func containsAny(s string, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
