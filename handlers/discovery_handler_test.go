package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"
	"code-contract/config"
	"code-contract/handlers"
	"code-contract/internal/server/discovery"
	"code-contract/models"
	"code-contract/pkg/agentproto"
)

func TestDiscoveryAPI_PrivateAgentLiveAndSnapshot_AntiIDOR403(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}

	r, db := setupRouterAndDB(t)

	topo := agentproto.CSGTopologyReport{
		DiscoveryMode: "CROSTCP_RPCMON",
		CSGEndpoints: []agentproto.CSGEndpointInfo{
			{PID: 100, ExeName: "csg_server", ListenIP: "127.0.0.1", ListenPort: 31806, Source: "CLI_FLAG"},
		},
		Services: []agentproto.CSGServiceInfo{
			{ServiceName: "PrivSvc-0", Addr: "tcp://ipv4:127.0.0.1:21001", Transport: "TCP", ServerPID: 5001, ServerProc: "priv_svr", IsLocal: true},
		},
	}
	procs := []agentproto.DiscoveredProcess{
		{PID: 5001, ListenPort: 21001, ExeName: "priv_svr", Cmdline: "/opt/priv_svr --port=21001 --token=privtok"},
	}

	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/agent/v1/csg/topology":
			_ = json.NewEncoder(w).Encode(topo)
		case "/agent/v1/processes/discover":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": procs})
		default:
			http.NotFound(w, req)
		}
	}))
	defer agentSrv.Close()

	adminUser := createTestUserInDB(t, db, "disc_admin", config.AdminRole)
	alice := createTestUserInDB(t, db, "disc_alice", "developer")
	bob := createTestUserInDB(t, db, "disc_bob", "developer")
	adminToken := generateTestJWT(t, adminUser)
	aliceToken := generateTestJWT(t, alice)
	bobToken := generateTestJWT(t, bob)

	privAgent := models.Agent{
		Name:     "alice-private-agent",
		Endpoint: agentSrv.URL,
		Scope:    models.ScopePrivate,
		OwnerID:  &alice.ID,
		Status:   models.AgentStatusOnline,
	}
	if err := db.Create(&privAgent).Error; err != nil {
		t.Fatalf("create privAgent failed: %v", err)
	}

	agentSrv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/agent/v1/csg/topology":
			_ = json.NewEncoder(w).Encode(topo)
		case "/agent/v1/processes/discover":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": procs})
		default:
			http.NotFound(w, req)
		}
	}))
	defer agentSrv2.Close()

	sharedAgent := models.Agent{
		Name:     "shared-agent",
		Endpoint: agentSrv2.URL,
		Scope:    models.ScopeShared,
		Status:   models.AgentStatusOnline,
	}
	if err := db.Create(&sharedAgent).Error; err != nil {
		t.Fatalf("create sharedAgent failed: %v", err)
	}

	// 1. Alice (owner) can access her PRIVATE agent live discovery & create snapshot
	reqLiveAlice := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/live?agent_id=%d", privAgent.ID), nil)
	reqLiveAlice.Header.Set("Authorization", "Bearer "+aliceToken)
	wLiveAlice := httptest.NewRecorder()
	r.ServeHTTP(wLiveAlice, reqLiveAlice)
	if wLiveAlice.Code != http.StatusOK {
		t.Fatalf("owner live status = %d body=%s", wLiveAlice.Code, wLiveAlice.Body.String())
	}

	createBody, _ := json.Marshal(map[string]interface{}{
		"agent_id": privAgent.ID,
		"label":    "alice-snap",
	})
	reqSnapAlice := httptest.NewRequest(http.MethodPost, "/api/discovery/snapshots", bytes.NewReader(createBody))
	reqSnapAlice.Header.Set("Authorization", "Bearer "+aliceToken)
	reqSnapAlice.Header.Set("Content-Type", "application/json")
	wSnapAlice := httptest.NewRecorder()
	r.ServeHTTP(wSnapAlice, reqSnapAlice)
	if wSnapAlice.Code != http.StatusCreated {
		t.Fatalf("owner create snapshot status = %d body=%s", wSnapAlice.Code, wSnapAlice.Body.String())
	}
	var aliceSnap discovery.DiscoverySnapshotDTO
	_ = json.Unmarshal(wSnapAlice.Body.Bytes(), &aliceSnap)

	// Bob creates a snapshot on SHARED agent for cross-env diff test
	sharedBody, _ := json.Marshal(map[string]interface{}{
		"agent_id": sharedAgent.ID,
		"label":    "shared-snap",
	})
	reqSnapShared := httptest.NewRequest(http.MethodPost, "/api/discovery/snapshots", bytes.NewReader(sharedBody))
	reqSnapShared.Header.Set("Authorization", "Bearer "+bobToken)
	reqSnapShared.Header.Set("Content-Type", "application/json")
	wSnapShared := httptest.NewRecorder()
	r.ServeHTTP(wSnapShared, reqSnapShared)
	if wSnapShared.Code != http.StatusCreated {
		t.Fatalf("shared create snapshot status = %d body=%s", wSnapShared.Code, wSnapShared.Body.String())
	}
	var sharedSnap discovery.DiscoverySnapshotDTO
	_ = json.Unmarshal(wSnapShared.Body.Bytes(), &sharedSnap)

	// 2. Bob (non-owner, non-admin) receives 403 across ALL endpoints on Alice's PRIVATE agent & snapshots
	antiIDORRequests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, fmt.Sprintf("/api/discovery/live?agent_id=%d", privAgent.ID), ""},
		{http.MethodGet, fmt.Sprintf("/api/discovery/snapshots?agent_id=%d", privAgent.ID), ""},
		{http.MethodPost, "/api/discovery/snapshots", string(createBody)},
		{http.MethodPatch, fmt.Sprintf("/api/discovery/snapshots/%d", aliceSnap.ID), `{"is_pinned":true}`},
		{http.MethodDelete, fmt.Sprintf("/api/discovery/snapshots/%d", aliceSnap.ID), ""},
		{http.MethodGet, fmt.Sprintf("/api/discovery/snapshots/diff?left_snapshot_id=%d&right_snapshot_id=%d", aliceSnap.ID, sharedSnap.ID), ""},
		{http.MethodGet, fmt.Sprintf("/api/discovery/snapshots/diff?left_snapshot_id=%d&live_agent_id=%d", sharedSnap.ID, privAgent.ID), ""},
	}
	for _, tc := range antiIDORRequests {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+bobToken)
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("Anti-IDOR expected 403 for %s %s, got %d body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}

	// 3. contract_admin can access Alice's PRIVATE live & snapshots (200)
	reqLiveAdmin := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/live?agent_id=%d", privAgent.ID), nil)
	reqLiveAdmin.Header.Set("Authorization", "Bearer "+adminToken)
	wLiveAdmin := httptest.NewRecorder()
	r.ServeHTTP(wLiveAdmin, reqLiveAdmin)
	if wLiveAdmin.Code != http.StatusOK {
		t.Fatalf("admin live on private agent status = %d body=%s", wLiveAdmin.Code, wLiveAdmin.Body.String())
	}

	reqDiffAdmin := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/snapshots/diff?left_snapshot_id=%d&right_snapshot_id=%d", aliceSnap.ID, sharedSnap.ID), nil)
	reqDiffAdmin.Header.Set("Authorization", "Bearer "+adminToken)
	wDiffAdmin := httptest.NewRecorder()
	r.ServeHTTP(wDiffAdmin, reqDiffAdmin)
	if wDiffAdmin.Code != http.StatusOK {
		t.Fatalf("admin diff on private+shared status = %d body=%s", wDiffAdmin.Code, wDiffAdmin.Body.String())
	}
}

func TestDiscoveryAPI_SharedSnapshotPinDeletePermission_AndAuditP2(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}

	r, db := setupRouterAndDB(t)

	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/agent/v1/csg/topology":
			_ = json.NewEncoder(w).Encode(agentproto.CSGTopologyReport{
				DiscoveryMode: "CROSTCP_RPCMON",
				Services: []agentproto.CSGServiceInfo{
					{ServiceName: "SharedSvc-0", Transport: "TCP", ServerPID: 6001, ServerProc: "shared_svr", IsLocal: true},
				},
			})
		case "/agent/v1/processes/discover":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"items": []agentproto.DiscoveredProcess{{PID: 6001, ExeName: "shared_svr", Cmdline: "/opt/shared_svr"}},
			})
		default:
			http.NotFound(w, req)
		}
	}))
	defer agentSrv.Close()

	adminUser := createTestUserInDB(t, db, "perm_admin", config.AdminRole)
	alice := createTestUserInDB(t, db, "perm_alice", "developer")
	bob := createTestUserInDB(t, db, "perm_bob", "developer")
	adminToken := generateTestJWT(t, adminUser)
	aliceToken := generateTestJWT(t, alice)
	bobToken := generateTestJWT(t, bob)

	sharedAgent := models.Agent{
		Name:     "perm-shared-agent",
		Endpoint: agentSrv.URL,
		Scope:    models.ScopeShared,
		Status:   models.AgentStatusOnline,
	}
	if err := db.Create(&sharedAgent).Error; err != nil {
		t.Fatalf("create sharedAgent failed: %v", err)
	}

	// Alice creates snapshot on SHARED agent
	body, _ := json.Marshal(map[string]interface{}{
		"agent_id":  sharedAgent.ID,
		"label":     "alice-shared-snap",
		"is_pinned": false,
	})
	reqCreate := httptest.NewRequest(http.MethodPost, "/api/discovery/snapshots", bytes.NewReader(body))
	reqCreate.Header.Set("Authorization", "Bearer "+aliceToken)
	reqCreate.Header.Set("Content-Type", "application/json")
	wCreate := httptest.NewRecorder()
	r.ServeHTTP(wCreate, reqCreate)
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("create snapshot status = %d body=%s", wCreate.Code, wCreate.Body.String())
	}
	var snap discovery.DiscoverySnapshotDTO
	_ = json.Unmarshal(wCreate.Body.Bytes(), &snap)

	// Bob (non-creator, non-admin) cannot pin/update or delete Alice's SHARED snapshot (403)
	reqBobPatch := httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/discovery/snapshots/%d", snap.ID), strings.NewReader(`{"is_pinned":true}`))
	reqBobPatch.Header.Set("Authorization", "Bearer "+bobToken)
	reqBobPatch.Header.Set("Content-Type", "application/json")
	wBobPatch := httptest.NewRecorder()
	r.ServeHTTP(wBobPatch, reqBobPatch)
	if wBobPatch.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-creator PATCH on SHARED snapshot, got %d", wBobPatch.Code)
	}

	reqBobDel := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/discovery/snapshots/%d", snap.ID), nil)
	reqBobDel.Header.Set("Authorization", "Bearer "+bobToken)
	wBobDel := httptest.NewRecorder()
	r.ServeHTTP(wBobDel, reqBobDel)
	if wBobDel.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-creator DELETE on SHARED snapshot, got %d", wBobDel.Code)
	}

	// Alice pins her snapshot -> 200; deleting pinned snapshot -> 409; unpin -> 200; admin deletes -> 200
	reqAlicePin := httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/discovery/snapshots/%d", snap.ID), strings.NewReader(`{"is_pinned":true}`))
	reqAlicePin.Header.Set("Authorization", "Bearer "+aliceToken)
	reqAlicePin.Header.Set("Content-Type", "application/json")
	wAlicePin := httptest.NewRecorder()
	r.ServeHTTP(wAlicePin, reqAlicePin)
	if wAlicePin.Code != http.StatusOK {
		t.Fatalf("expected 200 pinning own snapshot, got %d body=%s", wAlicePin.Code, wAlicePin.Body.String())
	}

	reqDelPinned := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/discovery/snapshots/%d", snap.ID), nil)
	reqDelPinned.Header.Set("Authorization", "Bearer "+aliceToken)
	wDelPinned := httptest.NewRecorder()
	r.ServeHTTP(wDelPinned, reqDelPinned)
	if wDelPinned.Code != http.StatusConflict {
		t.Fatalf("expected 409 deleting pinned snapshot, got %d body=%s", wDelPinned.Code, wDelPinned.Body.String())
	}

	reqUnpin := httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/discovery/snapshots/%d", snap.ID), strings.NewReader(`{"is_pinned":false,"label":"ready-to-delete"}`))
	reqUnpin.Header.Set("Authorization", "Bearer "+adminToken)
	reqUnpin.Header.Set("Content-Type", "application/json")
	wUnpin := httptest.NewRecorder()
	r.ServeHTTP(wUnpin, reqUnpin)
	if wUnpin.Code != http.StatusOK {
		t.Fatalf("expected 200 unpinning by admin, got %d", wUnpin.Code)
	}

	reqDelAdmin := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/discovery/snapshots/%d", snap.ID), nil)
	reqDelAdmin.Header.Set("Authorization", "Bearer "+adminToken)
	wDelAdmin := httptest.NewRecorder()
	r.ServeHTTP(wDelAdmin, reqDelAdmin)
	if wDelAdmin.Code != http.StatusOK {
		t.Fatalf("expected 200 deleting unpinned snapshot by admin, got %d", wDelAdmin.Code)
	}

	// Verify P2 audit logs persisted
	if err := commonAudit.Close(context.Background()); err != nil {
		t.Fatalf("close audit failed: %v", err)
	}
	var auditLogs []commonModels.SysAuditLog
	if err := db.Where("target_type = ?", "discovery_snapshot").Find(&auditLogs).Error; err != nil {
		t.Fatalf("query audit logs failed: %v", err)
	}
	if len(auditLogs) < 3 {
		t.Fatalf("expected >= 3 discovery_snapshot P2 audit logs, got %d", len(auditLogs))
	}
	for _, al := range auditLogs {
		if al.Level != commonModels.AuditLevelP2 {
			t.Fatalf("expected AuditLevelP2 for %s, got %s", al.Action, al.Level)
		}
	}
}

func TestSC19_DiscoveryLiveCache_OfflineFallback_RedactedSnapshot_AndDualModeDiff(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}

	r, db := setupRouterAndDB(t)

	// Build a 500-process / 2000-service scale fixture to validate SC-19 P95 performance gates
	const procCount = 500
	const svcCount = 2000

	var topoMu sync.RWMutex
	var agentCalls atomic.Int32

	largeProcs := make([]agentproto.DiscoveredProcess, 0, procCount)
	largeSvcs := make([]agentproto.CSGServiceInfo, 0, svcCount)
	largeSubs := make([]agentproto.CSGSubscription, 0, procCount)

	for i := 0; i < procCount; i++ {
		pid := uint32(10000 + i)
		procName := fmt.Sprintf("svc_proc_%03d", i)
		largeProcs = append(largeProcs, agentproto.DiscoveredProcess{
			PID:        pid,
			ListenPort: uint16(20000 + i),
			ExeName:    procName,
			Cmdline:    fmt.Sprintf("/opt/bin/%s --port=%d --password=secret_%d --token=tok_%d", procName, 20000+i, i, i),
			Transports: []string{"TCP"},
		})
	}
	for j := 0; j < svcCount; j++ {
		pIdx := j % procCount
		pid := uint32(10000 + pIdx)
		tr := "TCP"
		addr := fmt.Sprintf("tcp://ipv4:10.0.0.1:%d", 20000+pIdx)
		if j%4 == 0 {
			tr = "MESH"
			addr = fmt.Sprintf("dophi_mesh://ipv4:10.0.0.1:%d", 30000+pIdx)
		}
		largeSvcs = append(largeSvcs, agentproto.CSGServiceInfo{
			ServiceName: fmt.Sprintf("ScaleService_%04d-0-%d", j, pid),
			Addr:        addr,
			Transport:   tr,
			HostIP:      "10.0.0.1",
			Port:        uint16(20000 + pIdx),
			ServerPID:   pid,
			ServerProc:  fmt.Sprintf("svc_proc_%03d", pIdx),
			IsLocal:     true,
		})
	}
	for i := 0; i < procCount; i++ {
		targetIdx := (i + 1) % svcCount
		largeSubs = append(largeSubs, agentproto.CSGSubscription{
			ClientPID:   uint32(10000 + i),
			ClientProc:  fmt.Sprintf("svc_proc_%03d", i),
			ClientAddr:  "10.0.0.1",
			ServiceName: largeSvcs[targetIdx].ServiceName,
			IsLocal:     true,
		})
	}

	currentTopo := agentproto.CSGTopologyReport{
		DiscoveryMode: "CROSTCP_RPCMON",
		CSGEndpoints: []agentproto.CSGEndpointInfo{
			{PID: 100, ExeName: "csg_server", ListenIP: "127.0.0.1", ListenPort: 31806, Source: "CLI_FLAG"},
		},
		Services:      largeSvcs,
		Subscriptions: largeSubs,
		CapturedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		agentCalls.Add(1)
		topoMu.RLock()
		defer topoMu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/agent/v1/csg/topology":
			_ = json.NewEncoder(w).Encode(currentTopo)
		case "/agent/v1/processes/discover":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": largeProcs})
		default:
			http.NotFound(w, req)
		}
	}))
	defer agentSrv.Close()

	user := createTestUserInDB(t, db, "sc19_user", "developer")
	token := generateTestJWT(t, user)

	agent := models.Agent{
		Name:     "sc19-scale-agent",
		Endpoint: agentSrv.URL,
		Scope:    models.ScopeShared,
		Status:   models.AgentStatusOnline,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent failed: %v", err)
	}

	// 1. Cold GET /api/discovery/live (500 procs / 2000 services) <= 2s
	tStartCold := time.Now()
	reqCold := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/live?agent_id=%d&csg_addr=127.0.0.1:31806", agent.ID), nil)
	reqCold.Header.Set("Authorization", "Bearer "+token)
	wCold := httptest.NewRecorder()
	r.ServeHTTP(wCold, reqCold)
	coldDur := time.Since(tStartCold)
	if wCold.Code != http.StatusOK {
		t.Fatalf("cold live status = %d body=%s", wCold.Code, wCold.Body.String())
	}
	if coldDur > 2*time.Second {
		t.Fatalf("SC-19 performance budget exceeded for uncached 500p/2000s: %v > 2s", coldDur)
	}
	if strings.Contains(wCold.Body.String(), "secret_0") || strings.Contains(wCold.Body.String(), "tok_0") {
		t.Fatalf("expected live response cmdline secrets redacted")
	}

	var liveResp discovery.DiscoveryLiveRespDTO
	_ = json.Unmarshal(wCold.Body.Bytes(), &liveResp)
	if len(liveResp.Processes) != procCount || liveResp.Summary.TotalServices != svcCount {
		t.Fatalf("unexpected scale counts: procs=%d svcs=%d", len(liveResp.Processes), liveResp.Summary.TotalServices)
	}

	// 2. Warm TTL cache GET /api/discovery/live <= 200ms and 0 extra RPC calls
	callsAfterCold := agentCalls.Load()
	tStartWarm := time.Now()
	reqWarm := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/live?agent_id=%d&csg_addr=127.0.0.1:31806", agent.ID), nil)
	reqWarm.Header.Set("Authorization", "Bearer "+token)
	wWarm := httptest.NewRecorder()
	r.ServeHTTP(wWarm, reqWarm)
	warmDur := time.Since(tStartWarm)
	if wWarm.Code != http.StatusOK {
		t.Fatalf("warm live status = %d", wWarm.Code)
	}
	if warmDur > 200*time.Millisecond {
		t.Fatalf("SC-19 performance budget exceeded for cached 500p/2000s: %v > 200ms", warmDur)
	}
	if agentCalls.Load() != callsAfterCold {
		t.Fatalf("expected TTL cache hit without hitting agent HTTP server")
	}

	// 3. Save redacted snapshot T1
	snap1Body, _ := json.Marshal(map[string]interface{}{
		"agent_id": agent.ID,
		"csg_addr": "127.0.0.1:31806",
		"label":    "T1-scale",
	})
	reqSnap1 := httptest.NewRequest(http.MethodPost, "/api/discovery/snapshots", bytes.NewReader(snap1Body))
	reqSnap1.Header.Set("Authorization", "Bearer "+token)
	reqSnap1.Header.Set("Content-Type", "application/json")
	wSnap1 := httptest.NewRecorder()
	r.ServeHTTP(wSnap1, reqSnap1)
	if wSnap1.Code != http.StatusCreated {
		t.Fatalf("save T1 snapshot status = %d body=%s", wSnap1.Code, wSnap1.Body.String())
	}
	var snap1 discovery.DiscoverySnapshotDTO
	_ = json.Unmarshal(wSnap1.Body.Bytes(), &snap1)

	// Verify DB payload is redacted
	var dbSnap models.DiscoverySnapshot
	if err := db.First(&dbSnap, snap1.ID).Error; err != nil {
		t.Fatalf("query saved snapshot failed: %v", err)
	}
	if strings.Contains(string(dbSnap.PayloadJSON), "secret_0") || strings.Contains(string(dbSnap.PayloadJSON), "tok_0") {
		t.Fatalf("persisted snapshot leaked unredacted secret")
	}

	// 4. List snapshots pagination excludes payload_json
	reqList := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/snapshots?agent_id=%d&page=1&page_size=15", agent.ID), nil)
	reqList.Header.Set("Authorization", "Bearer "+token)
	wList := httptest.NewRecorder()
	r.ServeHTTP(wList, reqList)
	if wList.Code != http.StatusOK || strings.Contains(wList.Body.String(), "payload_json") {
		t.Fatalf("unexpected list snapshots response: code=%d body=%s", wList.Code, wList.Body.String())
	}

	// 5. Mutate live topology (add new service & change transport on ScaleService_0001 TCP->MESH), refresh & diff T1 vs live
	topoMu.Lock()
	currentTopo.Services[1].Transport = "MESH"
	currentTopo.Services = append(currentTopo.Services, agentproto.CSGServiceInfo{
		ServiceName: "AddedInT2Svc-0-10000",
		Addr:        "tcp://ipv4:10.0.0.1:29999",
		Transport:   "TCP",
		ServerPID:   10000,
		ServerProc:  "svc_proc_000",
		IsLocal:     true,
	})
	topoMu.Unlock()

	reqRefresh := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/live?agent_id=%d&csg_addr=127.0.0.1:31806&refresh=true", agent.ID), nil)
	reqRefresh.Header.Set("Authorization", "Bearer "+token)
	wRefresh := httptest.NewRecorder()
	r.ServeHTTP(wRefresh, reqRefresh)
	if wRefresh.Code != http.StatusOK {
		t.Fatalf("refresh live status = %d", wRefresh.Code)
	}

	reqDiff := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/snapshots/diff?left_snapshot_id=%d&live_agent_id=%d&csg_addr=127.0.0.1:31806", snap1.ID, agent.ID), nil)
	reqDiff.Header.Set("Authorization", "Bearer "+token)
	wDiff := httptest.NewRecorder()
	r.ServeHTTP(wDiff, reqDiff)
	if wDiff.Code != http.StatusOK {
		t.Fatalf("diff status = %d body=%s", wDiff.Code, wDiff.Body.String())
	}
	var diffResp discovery.DiscoverySnapshotDiffDTO
	_ = json.Unmarshal(wDiff.Body.Bytes(), &diffResp)
	if diffResp.Summary.AddedService < 1 || diffResp.Summary.TransportChanged < 1 {
		t.Fatalf("expected AddedService >= 1 and TransportChanged >= 1, got %+v", diffResp.Summary)
	}

	// 6. Agent goes OFFLINE -> GET /api/discovery/live falls back to snapshot #1 with stale=true
	if err := db.Model(&models.Agent{}).Where("id = ?", agent.ID).Update("status", models.AgentStatusOffline).Error; err != nil {
		t.Fatalf("set agent offline failed: %v", err)
	}
	handlers.GetDiscoveryService().ResetCache()

	reqOffline := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/live?agent_id=%d&csg_addr=127.0.0.1:31806", agent.ID), nil)
	reqOffline.Header.Set("Authorization", "Bearer "+token)
	wOffline := httptest.NewRecorder()
	r.ServeHTTP(wOffline, reqOffline)
	if wOffline.Code != http.StatusOK {
		t.Fatalf("offline fallback status = %d body=%s", wOffline.Code, wOffline.Body.String())
	}
	var offlineResp discovery.DiscoveryLiveRespDTO
	_ = json.Unmarshal(wOffline.Body.Bytes(), &offlineResp)
	if !offlineResp.Stale || offlineResp.SnapshotID == nil || *offlineResp.SnapshotID != snap1.ID {
		t.Fatalf("expected stale=true fallback to snap #%d, got stale=%v snap=%v", snap1.ID, offlineResp.Stale, offlineResp.SnapshotID)
	}
}
