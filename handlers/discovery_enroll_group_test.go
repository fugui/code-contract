package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"code-contract/config"
	"code-contract/internal/server/discovery"
	"code-contract/models"
	"code-contract/pkg/agentproto"
)

func TestDiscoveryEnroll_TargetGroupSelection(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}
	discovery.ResetEnrollRateLimiter()

	r, db := setupRouterAndDB(t)
	if err := db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "discovery.v2_enabled").Update("setting_val", "true").Error; err != nil {
		t.Fatalf("开启 discovery.v2_enabled 失败: %v", err)
	}

	liveTopo := agentproto.CSGTopologyReport{
		DiscoveryMode: "CROSTCP_RPCMON",
		Services: []agentproto.CSGServiceInfo{
			{ServiceName: "OrderSvc-0", Addr: "tcp://ipv4:127.0.0.1:24001", Transport: "TCP", Port: 24001, ServerPID: 6001, ServerProc: "order_svr", IsLocal: true},
			{ServiceName: "LegacySvc-0", Addr: "tcp://ipv4:127.0.0.1:24002", Transport: "TCP", Port: 24002, ServerPID: 6002, ServerProc: "legacy_svr", IsLocal: true},
			{ServiceName: "AutoSvc-0", Addr: "tcp://ipv4:127.0.0.1:24003", Transport: "TCP", Port: 24003, ServerPID: 6003, ServerProc: "auto_svr", IsLocal: true},
		},
	}
	discProcs := []agentproto.DiscoveredProcess{
		{PID: 6001, ListenPort: 24001, ExeName: "/opt/bin/order_svr", Cmdline: "/opt/bin/order_svr --port=24001", Transports: []string{"TCP"}},
		{PID: 6002, ListenPort: 24002, ExeName: "/opt/bin/legacy_svr", Cmdline: "/opt/bin/legacy_svr --port=24002", Transports: []string{"TCP"}},
		{PID: 6003, ListenPort: 24003, ExeName: "/opt/bin/auto_svr", Cmdline: "/opt/bin/auto_svr --port=24003", Transports: []string{"TCP"}},
	}

	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/agent/v1/csg/topology":
			_ = json.NewEncoder(w).Encode(liveTopo)
		case "/agent/v1/processes/discover":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": discProcs})
		default:
			http.NotFound(w, req)
		}
	}))
	defer agentSrv.Close()

	adminUser := createTestUserInDB(t, db, "enr_grp_admin", config.AdminRole)
	adminToken := generateTestJWT(t, adminUser)

	agent := models.Agent{
		Name:     "agent-enroll-group",
		Endpoint: agentSrv.URL,
		Scope:    models.ScopeShared,
		Status:   models.AgentStatusOnline,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// 预先创建两个业务进程组及一个既有标准进程
	grpBiz := models.ArchProcessGroup{Code: "BIZ_ORDER", Name: "订单业务组", SortOrder: 1}
	grpLegacy := models.ArchProcessGroup{Code: "BIZ_LEGACY", Name: "存量业务组", SortOrder: 2}
	if err := db.Create(&grpBiz).Error; err != nil {
		t.Fatalf("create grpBiz: %v", err)
	}
	if err := db.Create(&grpLegacy).Error; err != nil {
		t.Fatalf("create grpLegacy: %v", err)
	}

	existingFP, _, _ := discovery.ComputeIdentityFingerprint("/opt/bin/legacy_svr", "/opt/bin/legacy_svr --port=24002", "legacy_svr")
	existingAP := models.ArchProcess{
		GroupID:             grpLegacy.ID,
		Code:                "legacy-svr",
		Name:                "存量服务进程",
		DefaultListenPort:   24002,
		DefaultCmdPattern:   "/opt/bin/legacy_svr --port=24002",
		IdentityFingerprint: existingFP,
		FingerprintVersion:  discovery.CurrentFingerprintVersion,
		Lifecycle:           models.ArchProcLifecycleConfirmed,
		Origin:              models.ArchProcOriginManual,
	}
	if err := db.Create(&existingAP).Error; err != nil {
		t.Fatalf("create existingAP: %v", err)
	}

	// 获取 live discovery process_key
	liveReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/live?agent_id=%d", agent.ID), nil)
	liveReq.Header.Set("Authorization", "Bearer "+adminToken)
	liveW := httptest.NewRecorder()
	r.ServeHTTP(liveW, liveReq)
	if liveW.Code != http.StatusOK {
		t.Fatalf("GET /api/discovery/live 失败: %d %s", liveW.Code, liveW.Body.String())
	}
	var liveBody struct {
		Processes []struct {
			ProcessKey string `json:"process_key"`
			ProcName   string `json:"proc_name"`
		} `json:"processes"`
	}
	_ = json.Unmarshal(liveW.Body.Bytes(), &liveBody)

	keyByName := make(map[string]string)
	for _, p := range liveBody.Processes {
		keyByName[filepath.Base(p.ProcName)] = p.ProcessKey
	}
	if keyByName["order_svr"] == "" || keyByName["legacy_svr"] == "" || keyByName["auto_svr"] == "" {
		t.Fatalf("live discovery 进程键缺失: %+v", liveBody.Processes)
	}

	// 1. 不存在目标组 -> 404
	badPayload, _ := json.Marshal(map[string]interface{}{
		"agent_id":        agent.ID,
		"target_group_id": 999999,
		"items":           []map[string]interface{}{{"process_key": keyByName["order_svr"]}},
	})
	badReq := httptest.NewRequest(http.MethodPost, "/api/discovery/enroll", bytes.NewReader(badPayload))
	badReq.Header.Set("Authorization", "Bearer "+adminToken)
	badReq.Header.Set("Content-Type", "application/json")
	badW := httptest.NewRecorder()
	r.ServeHTTP(badW, badReq)
	if badW.Code != http.StatusNotFound {
		t.Fatalf("target_group_id 不存在期望 404，实际 %d: %s", badW.Code, badW.Body.String())
	}

	discovery.ResetEnrollRateLimiter()

	// 2. 指定 target_group_id = grpBiz.ID：新建 order_svr 落入 grpBiz；既有 legacy_svr 保持 grpLegacy 不被篡改
	okPayload, _ := json.Marshal(map[string]interface{}{
		"agent_id":        agent.ID,
		"target_group_id": grpBiz.ID,
		"items": []map[string]interface{}{
			{"process_key": keyByName["order_svr"]},
			{"process_key": keyByName["legacy_svr"]},
		},
	})
	okReq := httptest.NewRequest(http.MethodPost, "/api/discovery/enroll", bytes.NewReader(okPayload))
	okReq.Header.Set("Authorization", "Bearer "+adminToken)
	okReq.Header.Set("Content-Type", "application/json")
	okW := httptest.NewRecorder()
	r.ServeHTTP(okW, okReq)
	if okW.Code != http.StatusOK {
		t.Fatalf("带 target_group_id 纳管失败: %d %s", okW.Code, okW.Body.String())
	}

	var okResp struct {
		Enrolled []struct {
			ProcessKey    string `json:"process_key"`
			ArchProcessID uint64 `json:"arch_process_id"`
			GroupID       uint64 `json:"group_id"`
			Result        string `json:"result"`
		} `json:"enrolled"`
	}
	_ = json.Unmarshal(okW.Body.Bytes(), &okResp)
	for _, it := range okResp.Enrolled {
		switch it.ProcessKey {
		case keyByName["order_svr"]:
			if it.GroupID != grpBiz.ID || it.Result != "CREATED_CONFIRMED" {
				t.Fatalf("新建 order_svr 应落入 grpBiz(%d)，实际: %+v", grpBiz.ID, it)
			}
		case keyByName["legacy_svr"]:
			if it.GroupID != grpLegacy.ID || it.Result != "MATCHED" {
				t.Fatalf("既有 legacy_svr 应保留原 grpLegacy(%d)，实际: %+v", grpLegacy.ID, it)
			}
		}
	}

	discovery.ResetEnrollRateLimiter()

	// 3. 不传 target_group_id：新建 auto_svr 落入默认 PGRP_CSG_AUTO
	defPayload, _ := json.Marshal(map[string]interface{}{
		"agent_id": agent.ID,
		"items":    []map[string]interface{}{{"process_key": keyByName["auto_svr"]}},
	})
	defReq := httptest.NewRequest(http.MethodPost, "/api/discovery/enroll", bytes.NewReader(defPayload))
	defReq.Header.Set("Authorization", "Bearer "+adminToken)
	defReq.Header.Set("Content-Type", "application/json")
	defW := httptest.NewRecorder()
	r.ServeHTTP(defW, defReq)
	if defW.Code != http.StatusOK {
		t.Fatalf("默认纳管失败: %d %s", defW.Code, defW.Body.String())
	}
	var autoGrp models.ArchProcessGroup
	if err := db.Where("code = ?", "PGRP_CSG_AUTO").First(&autoGrp).Error; err != nil {
		t.Fatalf("未找到默认 PGRP_CSG_AUTO 组: %v", err)
	}
	var defResp struct {
		Enrolled []struct {
			GroupID uint64 `json:"group_id"`
		} `json:"enrolled"`
	}
	_ = json.Unmarshal(defW.Body.Bytes(), &defResp)
	if len(defResp.Enrolled) != 1 || defResp.Enrolled[0].GroupID != autoGrp.ID {
		t.Fatalf("默认纳管应落入 PGRP_CSG_AUTO(%d)，实际: %+v", autoGrp.ID, defResp)
	}
}
