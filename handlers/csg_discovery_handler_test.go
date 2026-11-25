package handlers_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"code-contract/config"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"code-contract/pkg/agentproto"
)

func TestCSGImportBlueprint_IdempotentAdminOnlyLayerGuardAndAudit(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}

	r, db := setupRouterAndDB(t)

	var topoMu sync.RWMutex
	liveTopo := agentproto.CSGTopologyReport{
		DiscoveryMode: "CROSTCP_RPCMON",
		CSGEndpoints: []agentproto.CSGEndpointInfo{
			{ListenIP: "127.0.0.1", ListenPort: 31806, Source: "CLI_FLAG"},
		},
		Services: []agentproto.CSGServiceInfo{
			{
				ServiceName: "BillingService-BillingService",
				Addr:        "tcp://ipv4:127.0.0.1:21001",
				Transport:   "TCP",
				HostIP:      "127.0.0.1",
				Port:        21001,
				ServerPID:   4001,
				ServerProc:  "billing_svr",
				IsLocal:     true,
			},
			{
				ServiceName: "LedgerService-LedgerService",
				Addr:        "dophi_mesh://ipv4:127.0.0.1:21002",
				Transport:   "MESH",
				HostIP:      "127.0.0.1",
				Port:        21002,
				ServerPID:   4002,
				ServerProc:  "ledger_svr",
				IsLocal:     true,
			},
		},
		Subscriptions: []agentproto.CSGSubscription{
			{
				ClientPID:   4001,
				ClientProc:  "billing_svr",
				ClientAddr:  "127.0.0.1",
				ServiceName: "LedgerService-LedgerService",
				IsLocal:     true,
			},
		},
	}

	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/agent/v1/csg/topology":
			topoMu.RLock()
			defer topoMu.RUnlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(liveTopo)
		default:
			http.NotFound(w, req)
		}
	}))
	defer agentSrv.Close()

	adminUser := createTestUserInDB(t, db, "csg_admin", config.AdminRole)
	normalUser := createTestUserInDB(t, db, "csg_user", "operator")
	adminToken := generateTestJWT(t, adminUser)
	normalToken := generateTestJWT(t, normalUser)

	agent := models.Agent{
		Name:      "csg-bp-agent",
		Endpoint:  agentSrv.URL,
		ClaimCode: "claim-123",
		Status:    models.AgentStatusOnline,
		Scope:     models.ScopeShared,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent failed: %v", err)
	}

	subsys := models.Subsystem{Code: "finance", Name: "Finance", LayerLevel: 20}
	if err := db.Create(&subsys).Error; err != nil {
		t.Fatalf("create subsystem failed: %v", err)
	}

	// 1. Non-admin can query GET /api/agents/:id/csg/topology on SHARED agent
	reqGet := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/agents/%d/csg/topology", agent.ID), nil)
	reqGet.Header.Set("Authorization", "Bearer "+normalToken)
	wGet := httptest.NewRecorder()
	r.ServeHTTP(wGet, reqGet)
	if wGet.Code != http.StatusOK {
		t.Fatalf("GET csg topology status = %d body=%s", wGet.Code, wGet.Body.String())
	}

	// 2. M31: POST /api/agents/:id/csg/import-blueprint 路由已下线，返回 404 Not Found
	importBody := map[string]interface{}{
		"subsystem_id":        subsys.ID,
		"import_dependencies": true,
	}
	rawBody, _ := json.Marshal(importBody)
	for _, tok := range []string{normalToken, adminToken} {
		reqDead := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/agents/%d/csg/import-blueprint", agent.ID), bytes.NewReader(rawBody))
		reqDead.Header.Set("Authorization", "Bearer "+tok)
		reqDead.Header.Set("Content-Type", "application/json")
		wDead := httptest.NewRecorder()
		r.ServeHTTP(wDead, reqDead)
		if wDead.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for decommissioned import-blueprint, got %d body=%s", wDead.Code, wDead.Body.String())
		}
	}

	// 3. 保留核心领域函数 topology.ImportCSGBlueprint 幂等性与跨层守卫回归验证
	topoMu.RLock()
	liveCopy1 := liveTopo
	topoMu.RUnlock()
	resp1, err := topology.ImportCSGBlueprint(db, topology.CSGBlueprintImportInput{
		AgentID:            agent.ID,
		SubsystemID:        subsys.ID,
		ImportDependencies: true,
		OperatorID:         adminUser.ID,
		Live:               &liveCopy1,
	})
	if err != nil {
		t.Fatalf("first topology.ImportCSGBlueprint failed: %v", err)
	}
	if resp1.CreatedServices != 2 || resp1.CreatedDependencies != 1 {
		t.Fatalf("unexpected import resp1: %+v", resp1)
	}

	resp2, err := topology.ImportCSGBlueprint(db, topology.CSGBlueprintImportInput{
		AgentID:            agent.ID,
		SubsystemID:        subsys.ID,
		ImportDependencies: true,
		OperatorID:         adminUser.ID,
		Live:               &liveCopy1,
	})
	if err != nil {
		t.Fatalf("repeat topology.ImportCSGBlueprint failed: %v", err)
	}
	if resp2.CreatedServices != 0 || resp2.SkippedServices != 2 {
		t.Fatalf("expected repeat import idempotent skip, got %+v", resp2)
	}

	// 4. Layer violation guard (ErrLayerViolation) rolls back when lower-layer module tries to depend on higher-layer provider
	lowSub := models.Subsystem{Code: "low_infra", Name: "LowInfra", LayerLevel: 10}
	if err := db.Create(&lowSub).Error; err != nil {
		t.Fatalf("create lowSub failed: %v", err)
	}
	topoMu.Lock()
	liveTopo.Services = []agentproto.CSGServiceInfo{
		{
			ServiceName: "LowInfraSvc-LowInfraSvc",
			Addr:        "tcp://ipv4:127.0.0.1:21099",
			Transport:   "TCP",
			HostIP:      "127.0.0.1",
			Port:        21099,
			ServerPID:   4099,
			ServerProc:  "low_infra_svr",
			IsLocal:     true,
		},
	}
	liveTopo.Subscriptions = []agentproto.CSGSubscription{
		{
			ClientPID:   4099,
			ClientProc:  "low_infra_svr",
			ClientAddr:  "127.0.0.1",
			ServiceName: "BillingService-BillingService", // L10 -> L20 violates layer guard!
			IsLocal:     true,
		},
	}
	liveCopyLow := liveTopo
	topoMu.Unlock()

	var layerErr *topology.LayerViolationError
	if _, err := topology.ImportCSGBlueprint(db, topology.CSGBlueprintImportInput{
		AgentID:            agent.ID,
		SubsystemID:        lowSub.ID,
		ImportDependencies: true,
		OperatorID:         adminUser.ID,
		Live:               &liveCopyLow,
	}); !errors.As(err, &layerErr) || layerErr.Code != topology.ErrCodeLayerViolation {
		t.Fatalf("expected ErrCodeLayerViolation on L10->L20 import, got %v", err)
	}
	var leakedCount int64
	db.Model(&models.ModuleService{}).Where("service_name = ?", "LowInfraSvc-LowInfraSvc").Count(&leakedCount)
	if leakedCount != 0 {
		t.Fatalf("expected rollback on layer guard, found %d leaked services", leakedCount)
	}
}
