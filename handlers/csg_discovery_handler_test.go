package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"
	"code-contract/config"
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

	// 2. Non-admin is forbidden (403) on POST /api/agents/:id/csg/import-blueprint
	importBody := map[string]interface{}{
		"subsystem_id":        subsys.ID,
		"import_dependencies": true,
	}
	rawBody, _ := json.Marshal(importBody)
	reqForbidden := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/agents/%d/csg/import-blueprint", agent.ID), bytes.NewReader(rawBody))
	reqForbidden.Header.Set("Authorization", "Bearer "+normalToken)
	reqForbidden.Header.Set("Content-Type", "application/json")
	wForbidden := httptest.NewRecorder()
	r.ServeHTTP(wForbidden, reqForbidden)
	if wForbidden.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin blueprint import, got %d body=%s", wForbidden.Code, wForbidden.Body.String())
	}

	// 3. First idempotent import by admin creates processes, modules, services, dependencies
	reqImport1 := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/agents/%d/csg/import-blueprint", agent.ID), bytes.NewReader(rawBody))
	reqImport1.Header.Set("Authorization", "Bearer "+adminToken)
	reqImport1.Header.Set("Content-Type", "application/json")
	wImport1 := httptest.NewRecorder()
	r.ServeHTTP(wImport1, reqImport1)
	if wImport1.Code != http.StatusOK {
		t.Fatalf("first import status = %d body=%s", wImport1.Code, wImport1.Body.String())
	}
	var resp1 map[string]interface{}
	if err := json.Unmarshal(wImport1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("decode import resp1 failed: %v", err)
	}
	if resp1["created_services"] != float64(2) || resp1["created_dependencies"] != float64(1) {
		t.Fatalf("unexpected import resp1: %+v", resp1)
	}

	// 4. Repeat import is completely idempotent (0 newly created, skipped instead)
	reqImport2 := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/agents/%d/csg/import-blueprint", agent.ID), bytes.NewReader(rawBody))
	reqImport2.Header.Set("Authorization", "Bearer "+adminToken)
	reqImport2.Header.Set("Content-Type", "application/json")
	wImport2 := httptest.NewRecorder()
	r.ServeHTTP(wImport2, reqImport2)
	if wImport2.Code != http.StatusOK {
		t.Fatalf("repeat import status = %d body=%s", wImport2.Code, wImport2.Body.String())
	}
	var resp2 map[string]interface{}
	_ = json.Unmarshal(wImport2.Body.Bytes(), &resp2)
	if resp2["created_services"] != float64(0) || resp2["skipped_services"] != float64(2) {
		t.Fatalf("expected repeat import idempotent skip, got %+v", resp2)
	}

	// 5. Layer violation guard (409 ERR_LAYER_VIOLATION) rolls back when lower-layer module tries to depend on higher-layer provider!
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
	topoMu.Unlock()

	lowBody, _ := json.Marshal(map[string]interface{}{
		"subsystem_id":        lowSub.ID,
		"import_dependencies": true,
	})
	reqLayerFail := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/agents/%d/csg/import-blueprint", agent.ID), bytes.NewReader(lowBody))
	reqLayerFail.Header.Set("Authorization", "Bearer "+adminToken)
	reqLayerFail.Header.Set("Content-Type", "application/json")
	wLayerFail := httptest.NewRecorder()
	r.ServeHTTP(wLayerFail, reqLayerFail)
	if wLayerFail.Code != http.StatusConflict {
		t.Fatalf("expected 409 layer violation on L10->L20 import, got %d body=%s", wLayerFail.Code, wLayerFail.Body.String())
	}
	var leakedCount int64
	db.Model(&models.ModuleService{}).Where("service_name = ?", "LowInfraSvc-LowInfraSvc").Count(&leakedCount)
	if leakedCount != 0 {
		t.Fatalf("expected rollback on 409 layer guard, found %d leaked services", leakedCount)
	}

	// 6. Flush audit logger and verify SysAuditLog persistence
	if err := commonAudit.Close(context.Background()); err != nil {
		t.Fatalf("close audit failed: %v", err)
	}
	var auditCount int64
	if err := db.Model(&commonModels.SysAuditLog{}).
		Where("target_type = ? AND action = ?", "csg_blueprint", "csg_import_blueprint").
		Count(&auditCount).Error; err != nil {
		t.Fatalf("query audit log failed: %v", err)
	}
	if auditCount == 0 {
		t.Fatalf("expected SysAuditLog record for csg_blueprint/csg_import_blueprint")
	}
}
