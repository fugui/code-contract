package csg_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"code-contract/internal/agent/csg"
	"code-contract/pkg/agentproto"
	"code-contract/pkg/csgdlink"
	"code-contract/pkg/csgmock"
	"code-contract/pkg/rpcmon"
)

func TestLocateCSGEndpoints_MultiTenantPriority(t *testing.T) {
	procRoot := t.TempDir()
	netDir := filepath.Join(procRoot, "net")
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatalf("mkdir proc/net failed: %v", err)
	}

	// Build fake /proc/net/tcp:
	// - LISTEN on 0.0.0.0:31806 (hex 00000000:7C3E) st=0A
	// - ESTABLISHED to 10.29.92.80:31808 (hex 505C1D0A:7C40) st=01
	// - LISTEN outside range on 31999 (7CFF) -> ignored
	fakeTCP := strings.Join([]string{
		"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode",
		"   0: 00000000:7C3E 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 10001 1 00000000 100 0 0 10 0",
		"   1: 0100007F:9999 505C1D0A:7C40 01 00000000:00000000 00:00000000 00000000  1000        0 10002 1 00000000 20 4 30 10 -1",
		"   2: 00000000:7CFF 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 10003 1 00000000 100 0 0 10 0",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(netDir, "tcp"), []byte(fakeTCP), 0o644); err != nil {
		t.Fatalf("write fake /proc/net/tcp failed: %v", err)
	}

	// Map socket:[10001] -> pid 4100 (csg_server)
	fdDir := filepath.Join(procRoot, "4100", "fd")
	if err := os.MkdirAll(fdDir, 0o755); err != nil {
		t.Fatalf("mkdir pid fd failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(procRoot, "4100", "comm"), []byte("csg_server\n"), 0o644); err != nil {
		t.Fatalf("write comm failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(procRoot, "4100", "cmdline"), []byte("/opt/dopra/csg_server\x00--port=31806\x00"), 0o644); err != nil {
		t.Fatalf("write cmdline failed: %v", err)
	}
	if err := os.Symlink("socket:[10001]", filepath.Join(fdDir, "5")); err != nil {
		t.Fatalf("symlink socket inode failed: %v", err)
	}

	eps, err := csg.LocateCSGEndpoints(csg.LocateOptions{
		CLICSGAddrs: []string{"127.0.0.1:31801"},
		EnvCSGAddr:  "ipv4:10.29.92.81:31802",
		ProcRoot:    procRoot,
		PortMin:     31800,
		PortMax:     31900,
	})
	if err != nil {
		t.Fatalf("LocateCSGEndpoints failed: %v", err)
	}

	if len(eps) != 4 {
		t.Fatalf("expected 4 endpoints, got %d: %+v", len(eps), eps)
	}
	if eps[0].Source != "CLI_FLAG" || eps[0].Addr() != "127.0.0.1:31801" {
		t.Fatalf("priority[0] mismatch: %+v", eps[0])
	}
	if eps[1].Source != "ENV" || eps[1].Addr() != "10.29.92.81:31802" {
		t.Fatalf("priority[1] mismatch: %+v", eps[1])
	}
	if eps[2].Source != "PROC_ESTABLISHED" || eps[2].Addr() != "10.29.92.80:31808" {
		t.Fatalf("priority[2] mismatch: %+v", eps[2])
	}
	if eps[3].Source != "PROC_LISTEN" || eps[3].Addr() != "127.0.0.1:31806" || eps[3].PID != 4100 || eps[3].ExeName != "csg_server" {
		t.Fatalf("priority[3] mismatch: %+v", eps[3])
	}
}

type scaleSnapshotProvider struct {
	snap rpcmon.Snapshot
}

func (p *scaleSnapshotProvider) Snapshot() rpcmon.Snapshot { return p.snap }

func TestDiscoverAndFuse_709Servers724Clients_RemoteLDCFiltered(t *testing.T) {
	// Spin up an in-process MockCSGServer (dlink) + a large-scale CrosTCP rpcmon server
	// simulating 709 local+remote servers, 724 clients, pure clients (listen_port==0),
	// dophi_mesh:// instances, cross-board remote LDC IPs, and transient rpcmon_probe entries.
	mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mockCSG.Start(); err != nil {
		t.Fatalf("mockCSG.Start failed: %v", err)
	}
	defer mockCSG.Close()

	var snap rpcmon.Snapshot
	snap.Ports = []rpcmon.PortUsage{{IPAddr: "127.0.0.1", Used: 700, Total: 16384}}

	// Build 709 servers:
	// - [0..698] local tcp://ipv4:127.0.0.1:<20000+i> (699 servers)
	// - [699] local dophi_mesh://ipv4:127.0.0.1:28000 (1 mesh server with no TCP listen socket)
	// - [700..708] remote LDC tcp://ipv4:10.200.99.88:<21000+i> (9 cross-board servers -> filtered!)
	for i := 0; i < 709; i++ {
		svcName := fmt.Sprintf("BizSvc%03d-BizSvc%03d", i, i)
		pid := uint32(5000 + i)
		switch {
		case i < 699:
			addr := fmt.Sprintf("tcp://ipv4:127.0.0.1:%d", 20000+i)
			snap.Servers = append(snap.Servers, rpcmon.ServerEntry{
				ProcName:   fmt.Sprintf("biz_proc_%03d", i),
				Pid:        pid,
				ServerName: svcName,
				ServerAddr: addr,
			})
			if i < 3 {
				// Publish a few to mockCSG so Sub->Unsub enrichment populates OsNodeID & Version!
				mockCSG.SeedInstance(csgdlink.Instance{
					ClientID: uint64(100 + i),
					Version:  99,
					ProcID:   pid,
					OsNodeID: 12,
					Name32:   "Compat" + strconv.Itoa(i),
					Addr:     addr,
					Name64:   svcName,
				})
			}
		case i == 699:
			addr := "dophi_mesh://ipv4:127.0.0.1:28000"
			snap.Servers = append(snap.Servers, rpcmon.ServerEntry{
				ProcName:   "mesh_worker",
				Pid:        pid,
				ServerName: svcName,
				ServerAddr: addr,
			})
			mockCSG.SeedInstance(csgdlink.Instance{
				ClientID: 888,
				Version:  5,
				ProcID:   pid,
				OsNodeID: 3,
				Name32:   "MeshCompat",
				Addr:     addr,
				Name64:   svcName,
			})
		default:
			snap.Servers = append(snap.Servers, rpcmon.ServerEntry{
				ProcName:   "remote_ldc_proc",
				Pid:        pid,
				ServerName: svcName,
				ServerAddr: fmt.Sprintf("tcp://ipv4:10.200.99.88:%d", 21000+i),
			})
		}
	}

	// Build 724 clients:
	// - [0..718] local clients (including pure clients with listen_port==0)
	// - [719..720] transient rpcmon_probe entries -> filtered as transient!
	// - [721..723] remote LDC clients (10.200.99.88) -> filtered as remote LDC!
	for i := 0; i < 724; i++ {
		svcName := fmt.Sprintf("BizSvc%03d-BizSvc%03d", i%3, i%3)
		switch {
		case i == 719 || i == 720:
			snap.Clients = append(snap.Clients, rpcmon.ClientEntry{
				ProcName:   "rpcmon_probe",
				Pid:        uint32(9000 + i),
				ClientAddr: "127.0.0.1",
				ServerName: rpcmon.ServiceName,
			})
		case i >= 721:
			snap.Clients = append(snap.Clients, rpcmon.ClientEntry{
				ProcName:   "remote_client",
				Pid:        uint32(9000 + i),
				ClientAddr: "10.200.99.88",
				ServerName: svcName,
			})
		default:
			snap.Clients = append(snap.Clients, rpcmon.ClientEntry{
				ProcName:   fmt.Sprintf("pure_cli_%03d", i),
				Pid:        uint32(7000 + i),
				ClientAddr: "127.0.0.1",
				ServerName: svcName,
			})
		}
	}

	monLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen mon failed: %v", err)
	}
	defer monLn.Close()
	go func() {
		_ = rpcmon.NewServer(&scaleSnapshotProvider{snap: snap}).Serve(monLn)
	}()

	// Local L1 process list intentionally only has a couple TCP listen processes
	// (no pure clients, no mesh_worker). DiscoverAndFuse must backfill all of them!
	l1Procs := []agentproto.DiscoveredProcess{
		{PID: 5000, ListenPort: 20000, ExeName: "biz_proc_000", Cmdline: "/app/biz_proc_000"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	report, fusedProcs, err := csg.DiscoverAndFuse(ctx, csg.DiscovererConfig{
		MonitorAddr: monLn.Addr().String(),
		CSGAddr:     mockCSG.Addr(),
		LocalIPs:    []string{"127.0.0.1"},
		LocalPIDs:   l1Procs,
		SubEnrich:   true,
	})
	if err != nil {
		t.Fatalf("DiscoverAndFuse failed: %v", err)
	}

	if report.DiscoveryMode != "CROSTCP_RPCMON" {
		t.Fatalf("DiscoveryMode = %q, want CROSTCP_RPCMON", report.DiscoveryMode)
	}
	if len(report.Services) != 700 {
		t.Fatalf("expected 700 local services (9 remote LDC filtered), got %d", len(report.Services))
	}
	// Remote LDC filtered count: 9 servers + 3 clients = 12
	if report.FilteredRemoteCount != 12 {
		t.Fatalf("FilteredRemoteCount = %d, want 12", report.FilteredRemoteCount)
	}
	if report.FilteredTransientCnt != 2 {
		t.Fatalf("FilteredTransientCnt = %d, want 2", report.FilteredTransientCnt)
	}
	if len(report.Subscriptions) != 719 {
		t.Fatalf("expected 719 local subscriptions, got %d", len(report.Subscriptions))
	}

	// Verify dlink Sub->Unsub enrichment happened for BizSvc000-BizSvc000 and left zero residual mock session!
	var found0, foundMesh bool
	for _, s := range report.Services {
		if s.ServiceName == "BizSvc000-BizSvc000" {
			found0 = true
			if !s.ResolvedBySub || s.OsNodeID != 12 || s.Version != 99 {
				t.Fatalf("expected BizSvc000 to be enriched via dlink Sub, got %+v", s)
			}
		}
		if s.ServiceName == "BizSvc699-BizSvc699" {
			foundMesh = true
			if s.Transport != "MESH" || s.Port != 28000 || !s.ResolvedBySub {
				t.Fatalf("expected BizSvc699 MESH service enriched, got %+v", s)
			}
		}
		if strings.Contains(s.HostIP, "10.200.99.88") {
			t.Fatalf("remote LDC service leaked into report.Services: %+v", s)
		}
	}
	if !found0 || !foundMesh {
		t.Fatalf("missing expected services found0=%v foundMesh=%v", found0, foundMesh)
	}

	// Verify fused L1+L2 process list backfilled pure client (listen_port==0) and dophi_mesh worker.
	procByPID := make(map[uint32]agentproto.DiscoveredProcess, len(fusedProcs))
	for _, p := range fusedProcs {
		procByPID[p.PID] = p
	}
	if pMesh, ok := procByPID[5699]; !ok || len(pMesh.ServiceNames) != 1 || pMesh.IsPureClient {
		t.Fatalf("mesh_worker process (PID 5699) not properly backfilled: ok=%v proc=%+v", ok, pMesh)
	}
	if pCli, ok := procByPID[7000]; !ok || !pCli.IsPureClient || pCli.ListenPort != 0 {
		t.Fatalf("pure_cli_000 (PID 7000) not marked IsPureClient with ListenPort=0: ok=%v proc=%+v", ok, pCli)
	}

	// Ensure MockCSGServer has zero dangling subscriptions after Sub->Unsub enrichment!
	if leftovers := mockCSG.Snapshot().Clients; len(leftovers) != 0 {
		t.Fatalf("expected 0 leftover dlink subscriptions after DiscoverAndFuse, got %+v", leftovers)
	}
}

func TestDiscoverAndFuse_DeduplicatesMultiPortLocalPID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	report, fused, err := csg.DiscoverAndFuse(ctx, csg.DiscovererConfig{
		Locate:   csg.LocateOptions{ProcRoot: t.TempDir()},
		LocalIPs: []string{"127.0.0.1"},
		LocalPIDs: []agentproto.DiscoveredProcess{
			{PID: 4242, ExeName: "multi-port-svc", ListenPort: 31806},
			{PID: 4242, ExeName: "multi-port-svc", ListenPort: 31901, ServiceNames: []string{"ExtraSvc"}},
		},
	})
	if err != nil {
		t.Fatalf("DiscoverAndFuse failed: %v", err)
	}
	if report == nil {
		t.Fatal("expected non-nil report")
	}
	if len(fused) != 1 {
		t.Fatalf("expected 1 deduplicated PID entry, got %d: %+v", len(fused), fused)
	}
	if fused[0].ListenPort != 31806 || len(fused[0].ServiceNames) != 1 || fused[0].ServiceNames[0] != "ExtraSvc" {
		t.Fatalf("unexpected merged process fields: %+v", fused[0])
	}
}

func TestDiscoverAndFuse_BareIPRemoteLDCFilteredAndSubEnrichBackfillsAddr(t *testing.T) {
	mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mockCSG.Start(); err != nil {
		t.Fatalf("mockCSG.Start failed: %v", err)
	}
	defer mockCSG.Close()

	mockCSG.SeedInstance(csgdlink.Instance{
		ClientID: 11,
		Version:  7,
		ProcID:   6001,
		OsNodeID: 1,
		Name32:   "LocalTCP",
		Addr:     "tcp://ipv4:127.0.0.1:28101",
		Name64:   "LocalTCP-LocalTCP",
	})
	mockCSG.SeedInstance(csgdlink.Instance{
		ClientID: 12,
		Version:  8,
		ProcID:   6002,
		OsNodeID: 2,
		Name32:   "LocalMesh",
		Addr:     "dophi_mesh://ipv4:127.0.0.1:28102",
		Name64:   "LocalMesh-LocalMesh",
	})

	snap := rpcmon.Snapshot{
		Ports: []rpcmon.PortUsage{{IPAddr: "127.0.0.1", Used: 2, Total: 1200}},
		Servers: []rpcmon.ServerEntry{
			{ProcName: "local_tcp_svr", Pid: 6001, ServerName: "LocalTCP-LocalTCP", ServerAddr: "127.0.0.1"},
			{ProcName: "local_mesh_svr", Pid: 6002, ServerName: "LocalMesh-LocalMesh", ServerAddr: "ipv4:127.0.0.1"},
			{ProcName: "remote_ldc_svr", Pid: 900001, ServerName: "RemoteLDC-RemoteLDC", ServerAddr: "10.1.126.150"},
		},
		Clients: []rpcmon.ClientEntry{
			{ProcName: "local_cli", Pid: 6003, ClientAddr: "127.0.0.1", ServerName: "LocalTCP-LocalTCP"},
			{ProcName: "remote_cli", Pid: 900002, ClientAddr: "ipv4:10.1.126.150", ServerName: "LocalTCP-LocalTCP"},
		},
	}

	monLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen mon failed: %v", err)
	}
	defer monLn.Close()
	go func() {
		_ = rpcmon.NewServer(&scaleSnapshotProvider{snap: snap}).Serve(monLn)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	report, fusedProcs, err := csg.DiscoverAndFuse(ctx, csg.DiscovererConfig{
		MonitorAddr: monLn.Addr().String(),
		CSGAddr:     mockCSG.Addr(),
		LocalIPs:    []string{"127.0.0.1"},
		SubEnrich:   true,
	})
	if err != nil {
		t.Fatalf("DiscoverAndFuse failed: %v", err)
	}
	if len(report.Services) != 2 {
		t.Fatalf("expected 2 local services (1 bare-IP remote LDC filtered), got %d: %+v", len(report.Services), report.Services)
	}
	if len(report.Subscriptions) != 1 {
		t.Fatalf("expected 1 local subscription (1 remote LDC client filtered), got %d: %+v", len(report.Subscriptions), report.Subscriptions)
	}
	if report.FilteredRemoteCount != 2 {
		t.Fatalf("expected FilteredRemoteCount=2, got %d", report.FilteredRemoteCount)
	}

	byName := make(map[string]agentproto.CSGServiceInfo, len(report.Services))
	for _, s := range report.Services {
		byName[s.ServiceName] = s
	}
	if tcpSvc := byName["LocalTCP-LocalTCP"]; tcpSvc.Addr != "tcp://ipv4:127.0.0.1:28101" || tcpSvc.Transport != "TCP" || tcpSvc.Port != 28101 || !tcpSvc.ResolvedBySub {
		t.Fatalf("expected LocalTCP bare IP enriched to full tcp URI & port, got %+v", tcpSvc)
	}
	if meshSvc := byName["LocalMesh-LocalMesh"]; meshSvc.Addr != "dophi_mesh://ipv4:127.0.0.1:28102" || meshSvc.Transport != "MESH" || meshSvc.Port != 28102 || !meshSvc.ResolvedBySub {
		t.Fatalf("expected LocalMesh bare IP enriched to full dophi_mesh URI & port, got %+v", meshSvc)
	}

	procByPID := make(map[uint32]agentproto.DiscoveredProcess, len(fusedProcs))
	for _, p := range fusedProcs {
		procByPID[p.PID] = p
	}
	if _, leaked := procByPID[900001]; leaked {
		t.Fatalf("remote bare-IP server leaked into fusedProcs: %+v", procByPID[900001])
	}
	if pTCP := procByPID[6001]; pTCP.ListenPort != 28101 {
		t.Fatalf("expected local_tcp_svr ListenPort=28101 backfilled from SubEnrich, got %+v", pTCP)
	}
	if pMesh := procByPID[6002]; pMesh.ListenPort != 0 {
		t.Fatalf("expected local_mesh_svr ListenPort=0 (non-TCP mesh port must not populate TCP ListenPort), got %+v", pMesh)
	}
}

func TestDiscoverAndFuse_MeshOnlyMonitor_SurfacesExplicitErrorWithoutProbe(t *testing.T) {
	mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mockCSG.Start(); err != nil {
		t.Fatalf("mockCSG.Start failed: %v", err)
	}
	defer mockCSG.Close()

	// Real-world scenario: CSG publishes RPCMonitorService ONLY via dophi_mesh:// (no tcp://).
	mockCSG.SeedInstance(csgdlink.Instance{
		ClientID: 1,
		ProcID:   4001,
		Addr:     "dophi_mesh://ipv4:127.0.0.1:16000",
		Name64:   rpcmon.ServiceName,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	report, _, err := csg.DiscoverAndFuse(ctx, csg.DiscovererConfig{
		Locate:            csg.LocateOptions{ProcRoot: t.TempDir()},
		CSGAddr:           mockCSG.Addr(),
		AutoDiscoverProbe: false,
		LocalIPs:          []string{"127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("DiscoverAndFuse returned error: %v", err)
	}
	if report.DiscoveryMode != "PROC_ONLY" {
		t.Fatalf("DiscoveryMode = %q, want PROC_ONLY", report.DiscoveryMode)
	}
	if len(report.Errors) == 0 {
		t.Fatalf("expected non-empty report.Errors explaining mesh-only RPCMonitorService degradation")
	}
	joined := strings.Join(report.Errors, " | ")
	if !strings.Contains(joined, "dophi_mesh://") || !strings.Contains(joined, "--csg-probe-bin") {
		t.Fatalf("expected error to mention dophi_mesh:// and --csg-probe-bin, got: %s", joined)
	}
}

func TestDiscoverAndFuse_MeshOnlyMonitor_AutoDiscoversNativeProbeAndFusesMeshProc(t *testing.T) {
	mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mockCSG.Start(); err != nil {
		t.Fatalf("mockCSG.Start failed: %v", err)
	}
	defer mockCSG.Close()

	mockCSG.SeedInstance(csgdlink.Instance{
		ClientID: 1,
		ProcID:   4001,
		Addr:     "dophi_mesh://ipv4:127.0.0.1:16000",
		Name64:   rpcmon.ServiceName,
	})
	mockCSG.SeedInstance(csgdlink.Instance{
		ClientID: 2,
		ProcID:   7788,
		OsNodeID: 9,
		Version:  12,
		Addr:     "dophi_mesh://ipv4:127.0.0.1:16000",
		Name64:   "OptlMeshSvc-OptlMeshSvc",
	})

	// Build fake /proc/7788 so L2 fusion can enrich cmdline for pure dophi_mesh process.
	procRoot := t.TempDir()
	pidDir := filepath.Join(procRoot, "7788")
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatalf("mkdir fake pid dir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "comm"), []byte("optl_mesh_svr\n"), 0o644); err != nil {
		t.Fatalf("write comm failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "cmdline"), []byte("/opt/sw/bin/optl_mesh_svr\x00--role=optl\x00"), 0o644); err != nil {
		t.Fatalf("write cmdline failed: %v", err)
	}

	probeDir := t.TempDir()
	probeBin := filepath.Join(probeDir, rpcmon.DefaultProbeBinaryName)
	script := `#!/bin/sh
cat <<'JSON'
{
  "portInfo": [{"ipAddr": "127.0.0.1", "uiPortUsed": 12, "uiPortTotal": 1200}],
  "servers": [{"procName": "optl_mesh_svr", "pid": 7788, "serverName": "OptlMeshSvc-OptlMeshSvc", "serverAddr": "127.0.0.1"}],
  "clients": [{"procName": "optl_mesh_cli", "pid": 7789, "clientAddr": "127.0.0.1", "serverName": "OptlMeshSvc-OptlMeshSvc"}],
  "serverNum": 1,
  "clientNum": 1,
  "timestamp": 1794961000
}
JSON
`
	if err := os.WriteFile(probeBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rpcmon_probe failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	report, fusedProcs, err := csg.DiscoverAndFuse(ctx, csg.DiscovererConfig{
		Locate:              csg.LocateOptions{ProcRoot: procRoot},
		CSGAddr:             mockCSG.Addr(),
		ProbeAllowedDirs:    []string{probeDir},
		AutoDiscoverProbe:   true,
		AutoProbeSearchDirs: []string{probeDir},
		LocalIPs:            []string{"127.0.0.1"},
		SubEnrich:           true,
	})
	if err != nil {
		t.Fatalf("DiscoverAndFuse failed: %v", err)
	}
	if report.DiscoveryMode != "NATIVE_PROBE" {
		t.Fatalf("DiscoveryMode = %q, want NATIVE_PROBE (errors=%v)", report.DiscoveryMode, report.Errors)
	}
	if len(report.Services) != 1 || report.Services[0].Transport != "MESH" || report.Services[0].Addr != "dophi_mesh://ipv4:127.0.0.1:16000" {
		t.Fatalf("unexpected mesh service: %+v", report.Services)
	}
	if len(report.Subscriptions) != 1 || report.Subscriptions[0].ClientProc != "optl_mesh_cli" {
		t.Fatalf("unexpected subscriptions: %+v", report.Subscriptions)
	}

	var foundMeshProc bool
	for _, p := range fusedProcs {
		if p.PID == 7788 {
			foundMeshProc = true
			if p.ListenPort != 0 {
				t.Fatalf("expected mesh proc ListenPort=0, got %d", p.ListenPort)
			}
			if !strings.Contains(p.Cmdline, "optl_mesh_svr --role=optl") {
				t.Fatalf("expected cmdline backfilled from /proc, got %q", p.Cmdline)
			}
			if len(p.Transports) != 1 || p.Transports[0] != "MESH" {
				t.Fatalf("expected Transports=[MESH], got %+v", p.Transports)
			}
		}
	}
	if !foundMeshProc {
		t.Fatalf("expected PID 7788 backfilled in fusedProcs: %+v", fusedProcs)
	}
}
