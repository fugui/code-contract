package server

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/csgdlink"
	"code-contract/pkg/csgmock"
	"code-contract/pkg/rpcmon"
)

func TestAgentServer_HandshakeDiscoverAndCSGTopologyRoutes(t *testing.T) {
	mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mockCSG.Start(); err != nil {
		t.Fatalf("mockCSG.Start failed: %v", err)
	}
	defer mockCSG.Close()
	if err := mockCSG.StartMonitor("127.0.0.1:0"); err != nil {
		t.Fatalf("mockCSG.StartMonitor failed: %v", err)
	}

	mockCSG.SeedInstance(csgdlink.Instance{
		ClientID: 1,
		ProcID:   8801,
		OsNodeID: 5,
		Addr:     "tcp://ipv4:127.0.0.1:28801",
		Name64:   "OrderService-OrderService",
	})

	subClient, err := csgdlink.Dial(mockCSG.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("sub dial failed: %v", err)
	}
	defer subClient.Close()
	_, _ = subClient.Register(2 * time.Second)
	_, _ = subClient.ClientID(2 * time.Second)
	if _, err := subClient.Sub("OrderService-OrderService", 2*time.Second); err != nil {
		t.Fatalf("sub failed: %v", err)
	}

	procRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procRoot, "net"), 0o755); err != nil {
		t.Fatalf("mkdir proc/net failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(procRoot, "net", "tcp"), []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"), 0o644); err != nil {
		t.Fatalf("write proc/net/tcp failed: %v", err)
	}

	srv, err := NewAgentServer(AgentServerConfig{
		ListenAddr:     "127.0.0.1:0",
		AllowIPs:       []string{"127.0.0.1/32"},
		ProcRoot:       procRoot,
		CSGAddrs:       []string{mockCSG.Addr()},
		CSGMonitorAddr: mockCSG.MonitorAddr(),
	})
	if err != nil {
		t.Fatalf("NewAgentServer failed: %v", err)
	}

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	// 1. Handshake returns csg_endpoints
	reqHS := httptest.NewRequest(http.MethodGet, "/agent/v1/handshake", nil)
	recHS := httptest.NewRecorder()
	mux.ServeHTTP(recHS, reqHS)
	if recHS.Code != http.StatusOK {
		t.Fatalf("handshake code = %d body=%s", recHS.Code, recHS.Body.String())
	}
	var hs agentproto.HandshakeResp
	if err := json.Unmarshal(recHS.Body.Bytes(), &hs); err != nil {
		t.Fatalf("decode handshake failed: %v", err)
	}
	if len(hs.CSGEndpoints) != 1 || hs.CSGEndpoints[0].Addr() != mockCSG.Addr() {
		t.Fatalf("expected handshake csg_endpoints [%s], got %+v", mockCSG.Addr(), hs.CSGEndpoints)
	}

	// 2. GET /agent/v1/csg/topology returns CROSTCP_RPCMON report
	reqTopo := httptest.NewRequest(http.MethodGet, "/agent/v1/csg/topology", nil)
	recTopo := httptest.NewRecorder()
	mux.ServeHTTP(recTopo, reqTopo)
	if recTopo.Code != http.StatusOK {
		t.Fatalf("topology code = %d body=%s", recTopo.Code, recTopo.Body.String())
	}
	var topo agentproto.CSGTopologyReport
	if err := json.Unmarshal(recTopo.Body.Bytes(), &topo); err != nil {
		t.Fatalf("decode topology failed: %v", err)
	}
	if topo.DiscoveryMode != "CROSTCP_RPCMON" || len(topo.Services) != 1 || len(topo.Subscriptions) != 1 {
		t.Fatalf("unexpected topology report: %+v", topo)
	}

	// 3. GET /agent/v1/processes/discover includes backfilled CSG server + pure client
	reqDisc := httptest.NewRequest(http.MethodGet, "/agent/v1/processes/discover", nil)
	recDisc := httptest.NewRecorder()
	mux.ServeHTTP(recDisc, reqDisc)
	if recDisc.Code != http.StatusOK {
		t.Fatalf("discover code = %d body=%s", recDisc.Code, recDisc.Body.String())
	}
	var discResp struct {
		Items []agentproto.DiscoveredProcess `json:"items"`
	}
	if err := json.Unmarshal(recDisc.Body.Bytes(), &discResp); err != nil {
		t.Fatalf("decode discover failed: %v", err)
	}
	if len(discResp.Items) < 2 {
		t.Fatalf("expected >=2 fused processes (server + pure client), got %+v", discResp.Items)
	}
}

type staticRPCMonProvider struct {
	snap rpcmon.Snapshot
}

func (p staticRPCMonProvider) Snapshot() rpcmon.Snapshot { return p.snap }

func TestAgentServer_CSGTopology_AutoCrosMeshDiscovery(t *testing.T) {
	mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mockCSG.Start(); err != nil {
		t.Fatalf("mockCSG.Start failed: %v", err)
	}
	defer mockCSG.Close()

	meshLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen mesh failed: %v", err)
	}
	defer meshLn.Close()

	snap := rpcmon.Snapshot{
		Ports: []rpcmon.PortUsage{{IPAddr: "127.0.0.1", Used: 4, Total: 1200}},
		Servers: []rpcmon.ServerEntry{
			{ProcName: "mesh_biz_svr", Pid: 8810, ServerName: "MeshBiz-MeshBiz", ServerAddr: "127.0.0.1"},
		},
		Clients: []rpcmon.ClientEntry{
			{ProcName: "mesh_biz_cli", Pid: 8811, ClientAddr: "127.0.0.1", ServerName: "MeshBiz-MeshBiz"},
		},
	}
	go func() {
		_ = rpcmon.NewServer(staticRPCMonProvider{snap: snap}).ServeMesh(meshLn)
	}()

	mockCSG.SeedInstance(csgdlink.Instance{
		ClientID: 1,
		ProcID:   4001,
		Addr:     "dophi_mesh://ipv4:" + meshLn.Addr().String(),
		Name64:   rpcmon.ServiceName,
	})
	mockCSG.SeedInstance(csgdlink.Instance{
		ClientID: 2,
		ProcID:   8810,
		Addr:     "dophi_mesh://ipv4:127.0.0.1:17205",
		Name64:   "MeshBiz-MeshBiz",
	})

	procRoot := t.TempDir()
	_ = os.MkdirAll(filepath.Join(procRoot, "net"), 0o755)
	_ = os.WriteFile(filepath.Join(procRoot, "net", "tcp"), []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"), 0o644)

	srv, err := NewAgentServer(AgentServerConfig{
		ListenAddr: "127.0.0.1:0",
		AllowIPs:   []string{"127.0.0.1/32"},
		ProcRoot:   procRoot,
		CSGAddrs:   []string{mockCSG.Addr()},
	})
	if err != nil {
		t.Fatalf("NewAgentServer failed: %v", err)
	}

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	reqTopo := httptest.NewRequest(http.MethodGet, "/agent/v1/csg/topology", nil)
	recTopo := httptest.NewRecorder()
	mux.ServeHTTP(recTopo, reqTopo)
	if recTopo.Code != http.StatusOK {
		t.Fatalf("topology code = %d body=%s", recTopo.Code, recTopo.Body.String())
	}

	var topo agentproto.CSGTopologyReport
	if err := json.Unmarshal(recTopo.Body.Bytes(), &topo); err != nil {
		t.Fatalf("decode topology failed: %v", err)
	}
	if topo.DiscoveryMode != "CROSMESH_RPCMON" || len(topo.Services) != 1 || len(topo.Subscriptions) != 1 {
		t.Fatalf("unexpected mesh topology report: %+v", topo)
	}
	if topo.Services[0].Transport != "MESH" || topo.Services[0].Addr != "dophi_mesh://ipv4:127.0.0.1:17205" {
		t.Fatalf("unexpected service entry: %+v", topo.Services[0])
	}
}
