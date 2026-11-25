package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"code-contract/config"
	"code-contract/internal/server/discovery"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/datatypes"
)

func TestImportBlueprint_DelegatesToEnroll_NoFakeModule(t *testing.T) {
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
		CSGEndpoints: []agentproto.CSGEndpointInfo{
			{ListenIP: "127.0.0.1", ListenPort: 31806, Source: "CLI_FLAG"},
		},
		Services: []agentproto.CSGServiceInfo{
			{ServiceName: "BillingService-0", Addr: "tcp://ipv4:127.0.0.1:21001", Transport: "TCP", Port: 21001, ServerPID: 4001, ServerProc: "billing_svr", IsLocal: true},
		},
		Subscriptions: []agentproto.CSGSubscription{
			{ClientPID: 4002, ClientProc: "order_cli", ClientAddr: "127.0.0.1", ServiceName: "BillingService-0", IsLocal: true},
		},
	}
	discProcs := []agentproto.DiscoveredProcess{
		{PID: 4001, ListenPort: 21001, ExeName: "/opt/bin/billing_svr", Cmdline: "/opt/bin/billing_svr --port=21001", Transports: []string{"TCP"}},
		{PID: 4002, ListenPort: 0, ExeName: "/opt/bin/order_cli", Cmdline: "/opt/bin/order_cli --mode=cli", Transports: []string{"TCP"}},
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

	adminUser := createTestUserInDB(t, db, "imp_v2_admin", config.AdminRole)
	normalUser := createTestUserInDB(t, db, "imp_v2_user", "developer")
	adminToken := generateTestJWT(t, adminUser)
	normalToken := generateTestJWT(t, normalUser)

	agent := models.Agent{
		Name:                "agent-imp-v2",
		Endpoint:            agentSrv.URL,
		Scope:               models.ScopeShared,
		Status:              models.AgentStatusOnline,
		ExpectedArmedDigest: "keep-digest-v2",
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// 1. M31: 废弃路由 POST /api/agents/:id/csg/import-blueprint 已下线，返回 404
	for _, tok := range []string{normalToken, adminToken} {
		reqDead := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/agents/%d/csg/import-blueprint", agent.ID), bytes.NewReader([]byte(`{"import_dependencies":true}`)))
		reqDead.Header.Set("Authorization", "Bearer "+tok)
		reqDead.Header.Set("Content-Type", "application/json")
		wDead := httptest.NewRecorder()
		r.ServeHTTP(wDead, reqDead)
		if wDead.Code != http.StatusNotFound {
			t.Fatalf("import-blueprint 路由应已下线返回 404，实际 %d", wDead.Code)
		}
	}

	// 2. Enroll v2 绝不生成任何 CSG_DISCOVERED 功能组或 MOD_* 假模块
	var fakeFGCount, fakeModCount int64
	_ = db.Model(&models.FunctionalGroup{}).Where("code = ?", "CSG_DISCOVERED").Count(&fakeFGCount).Error
	_ = db.Model(&models.Module{}).Where("code LIKE 'MOD\\_%'").Count(&fakeModCount).Error
	if fakeFGCount != 0 || fakeModCount != 0 {
		t.Fatalf("v2 绝不创建假模块，实际 fakeFG=%d fakeMod=%d", fakeFGCount, fakeModCount)
	}

	// 4. arm-all 缺省跳过 DISCOVERY 挂载，仅布防 MANUAL 挂载
	pgrp := models.ArchProcessGroup{Code: "PGRP_ARM_SKIP", Name: "布防跳过组"}
	_ = db.Create(&pgrp).Error
	apMan := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-manual-arm", Name: "人工进程", DefaultCmdPattern: "manual_bin"}
	_ = db.Create(&apMan).Error
	procMan := models.Process{
		AgentID: agent.ID, ArchProcessID: apMan.ID, EffectiveListenPort: 29999,
		ExtraDownstreams: datatypes.JSON("[]"), EnrollSource: models.EnrollSourceManual,
		ProbeDesiredState: models.ProbeDesiredStateDisarmed, ProbeStatus: models.ProbeStatusDisarmed,
	}
	_ = db.Create(&procMan).Error

	reqArmDefault := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/agents/%d/probes/arm-all", agent.ID), bytes.NewReader([]byte(`{}`)))
	reqArmDefault.Header.Set("Authorization", "Bearer "+adminToken)
	reqArmDefault.Header.Set("Content-Type", "application/json")
	wArmDefault := httptest.NewRecorder()
	r.ServeHTTP(wArmDefault, reqArmDefault)
	if wArmDefault.Code != http.StatusOK {
		t.Fatalf("arm-all 失败: %d %s", wArmDefault.Code, wArmDefault.Body.String())
	}

	var refreshedMan models.Process
	_ = db.First(&refreshedMan, procMan.ID).Error
	if refreshedMan.ProbeDesiredState != models.ProbeDesiredStateArmed {
		t.Fatalf("arm-all 应当布防 MANUAL 挂载，实际: %s", refreshedMan.ProbeDesiredState)
	}
	var discProcsCount int64
	_ = db.Model(&models.Process{}).Where("agent_id = ? AND enroll_source = ? AND probe_desired_state = ?", agent.ID, models.EnrollSourceDiscovery, models.ProbeDesiredStateArmed).Count(&discProcsCount).Error
	if discProcsCount != 0 {
		t.Fatalf("arm-all 缺省应跳过 DISCOVERY 挂载，实际仍有 %d 条被布防", discProcsCount)
	}
}

func TestDiscoveryEnroll_HTTP_NormalUserPrivateGuardAndPromote(t *testing.T) {
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
			{ServiceName: "PrivSvc", Addr: "tcp://ipv4:10.1.0.8:23001", Transport: "TCP", Port: 23001, ServerPID: 5001, ServerProc: "priv_worker", IsLocal: true},
		},
	}
	discProcs := []agentproto.DiscoveredProcess{
		{PID: 5001, ListenPort: 23001, ExeName: "/opt/bin/priv_worker", Cmdline: "/opt/bin/priv_worker --env=dev", Transports: []string{"TCP"}},
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

	adminUser := createTestUserInDB(t, db, "m30b_admin", config.AdminRole)
	aliceUser := createTestUserInDB(t, db, "m30b_alice", "developer")
	bobUser := createTestUserInDB(t, db, "m30b_bob", "developer")
	adminToken := generateTestJWT(t, adminUser)
	aliceToken := generateTestJWT(t, aliceUser)
	bobToken := generateTestJWT(t, bobUser)

	privAgent := models.Agent{Name: "alice-priv-http", Endpoint: agentSrv.URL, Scope: models.ScopePrivate, OwnerID: &aliceUser.ID, Status: models.AgentStatusOnline}
	if err := db.Create(&privAgent).Error; err != nil {
		t.Fatalf("create privAgent: %v", err)
	}

	// 1. Alice 获取 live discovery 后直接调用 POST /api/discovery/enroll -> 200
	liveReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/live?agent_id=%d", privAgent.ID), nil)
	liveReq.Header.Set("Authorization", "Bearer "+aliceToken)
	liveW := httptest.NewRecorder()
	r.ServeHTTP(liveW, liveReq)
	if liveW.Code != http.StatusOK {
		t.Fatalf("GET /api/discovery/live 失败: %d %s", liveW.Code, liveW.Body.String())
	}
	var liveBody struct {
		Processes []struct {
			ProcessKey string `json:"process_key"`
		} `json:"processes"`
	}
	_ = json.Unmarshal(liveW.Body.Bytes(), &liveBody)
	if len(liveBody.Processes) != 1 {
		t.Fatalf("期望 1 个待纳管进程，实际: %+v", liveBody)
	}

	// 2. Bob 试图纳管 Alice PRIVATE Agent -> 403
	enrollPayload, _ := json.Marshal(map[string]interface{}{
		"agent_id": privAgent.ID,
		"items":    []map[string]interface{}{{"process_key": liveBody.Processes[0].ProcessKey}},
	})
	bobEnrollReq := httptest.NewRequest(http.MethodPost, "/api/discovery/enroll", bytes.NewReader(enrollPayload))
	bobEnrollReq.Header.Set("Authorization", "Bearer "+bobToken)
	bobEnrollReq.Header.Set("Content-Type", "application/json")
	bobEnrollW := httptest.NewRecorder()
	r.ServeHTTP(bobEnrollW, bobEnrollReq)
	if bobEnrollW.Code != http.StatusForbidden {
		t.Fatalf("Bob 访问 Alice PRIVATE enroll 期望 403，实际 %d", bobEnrollW.Code)
	}

	// 3. Alice 纳管自身 PRIVATE Agent -> 200 且生成 PROVISIONAL
	aliceEnrollReq := httptest.NewRequest(http.MethodPost, "/api/discovery/enroll", bytes.NewReader(enrollPayload))
	aliceEnrollReq.Header.Set("Authorization", "Bearer "+aliceToken)
	aliceEnrollReq.Header.Set("Content-Type", "application/json")
	aliceEnrollW := httptest.NewRecorder()
	r.ServeHTTP(aliceEnrollW, aliceEnrollReq)
	if aliceEnrollW.Code != http.StatusOK {
		t.Fatalf("Alice Enroll 期望 200，实际 %d %s", aliceEnrollW.Code, aliceEnrollW.Body.String())
	}
	var enrollResp struct {
		Enrolled []struct {
			ArchProcessID uint64 `json:"arch_process_id"`
			Lifecycle     string `json:"lifecycle"`
		} `json:"enrolled"`
	}
	_ = json.Unmarshal(aliceEnrollW.Body.Bytes(), &enrollResp)
	if len(enrollResp.Enrolled) != 1 || enrollResp.Enrolled[0].Lifecycle != models.ArchProcLifecycleProvisional {
		t.Fatalf("期望生成 PROVISIONAL 进程，实际: %+v", enrollResp)
	}
	provAPID := enrollResp.Enrolled[0].ArchProcessID

	// 4. Alice 5s 内重复提交触发 429
	rateReq := httptest.NewRequest(http.MethodPost, "/api/discovery/enroll", bytes.NewReader(enrollPayload))
	rateReq.Header.Set("Authorization", "Bearer "+aliceToken)
	rateReq.Header.Set("Content-Type", "application/json")
	rateW := httptest.NewRecorder()
	r.ServeHTTP(rateW, rateReq)
	if rateW.Code != http.StatusTooManyRequests {
		t.Fatalf("5s 内重复 Enroll 期望 429，实际 %d %s", rateW.Code, rateW.Body.String())
	}

	// 5. Bob 调用 GET /api/arch/processes/:id 看 Alice PROVISIONAL -> 404；ListArchProcesses 也不可见
	bobGetAP := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/arch/processes/%d", provAPID), nil)
	bobGetAP.Header.Set("Authorization", "Bearer "+bobToken)
	bobGetW := httptest.NewRecorder()
	r.ServeHTTP(bobGetW, bobGetAP)
	if bobGetW.Code != http.StatusNotFound {
		t.Fatalf("Bob 读取他人 PRIVATE PROVISIONAL 期望 404，实际 %d", bobGetW.Code)
	}

	// 6. 普通用户调用 promote -> 403；管理员调用 promote -> 200；再调一次 -> 409
	alicePromote := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/arch-processes/%d/promote", provAPID), bytes.NewReader([]byte(`{}`)))
	alicePromote.Header.Set("Authorization", "Bearer "+aliceToken)
	alicePromoteW := httptest.NewRecorder()
	r.ServeHTTP(alicePromoteW, alicePromote)
	if alicePromoteW.Code != http.StatusForbidden {
		t.Fatalf("普通用户 promote 期望 403，实际 %d", alicePromoteW.Code)
	}

	adminPromote := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/arch-processes/%d/promote", provAPID), bytes.NewReader([]byte(`{}`)))
	adminPromote.Header.Set("Authorization", "Bearer "+adminToken)
	adminPromoteW := httptest.NewRecorder()
	r.ServeHTTP(adminPromoteW, adminPromote)
	if adminPromoteW.Code != http.StatusOK {
		t.Fatalf("管理员 promote 期望 200，实际 %d %s", adminPromoteW.Code, adminPromoteW.Body.String())
	}

	dupPromote := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/arch-processes/%d/promote", provAPID), bytes.NewReader([]byte(`{}`)))
	dupPromote.Header.Set("Authorization", "Bearer "+adminToken)
	dupPromoteW := httptest.NewRecorder()
	r.ServeHTTP(dupPromoteW, dupPromote)
	if dupPromoteW.Code != http.StatusConflict {
		t.Fatalf("重复 promote 已 CONFIRMED 期望 409，实际 %d", dupPromoteW.Code)
	}
}

func TestDiscoveryPhysical_FilterScopeAndPagination(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}
	discovery.ResetEnrollRateLimiter()

	r, db := setupRouterAndDB(t)
	_ = db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "discovery.v2_enabled").Update("setting_val", "true").Error

	aliceUser := createTestUserInDB(t, db, "phys_alice", "developer")
	bobUser := createTestUserInDB(t, db, "phys_bob", "developer")
	aliceToken := generateTestJWT(t, aliceUser)
	bobToken := generateTestJWT(t, bobUser)

	sharedAgent := models.Agent{Name: "phys-shared", Endpoint: "10.10.1.1:8090", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	alicePrivAgent := models.Agent{Name: "phys-alice-priv", Endpoint: "10.10.1.2:8090", Scope: models.ScopePrivate, OwnerID: &aliceUser.ID, Status: models.AgentStatusOnline}
	_ = db.Create(&sharedAgent).Error
	_ = db.Create(&alicePrivAgent).Error

	pgrp := models.ArchProcessGroup{Code: "PGRP_PHYS_Q", Name: "物理查询组"}
	_ = db.Create(&pgrp).Error
	apShared := models.ArchProcess{GroupID: pgrp.ID, Code: "shared-svc-proc", Name: "公共进程", Lifecycle: models.ArchProcLifecycleConfirmed}
	apPriv := models.ArchProcess{GroupID: pgrp.ID, Code: fmt.Sprintf("prv%d-priv-worker", alicePrivAgent.ID), Name: "私有试用进程", Lifecycle: models.ArchProcLifecycleProvisional, Origin: models.ArchProcOriginDiscovery, OriginAgentID: &alicePrivAgent.ID}
	_ = db.Create(&apShared).Error
	_ = db.Create(&apPriv).Error

	procShared := models.Process{AgentID: sharedAgent.ID, ArchProcessID: apShared.ID, ExtraDownstreams: datatypes.JSON("[]"), EnrollSource: models.EnrollSourceDiscovery, ProbeDesiredState: models.ProbeDesiredStateDisarmed, ProbeStatus: models.ProbeStatusDisarmed}
	procPriv := models.Process{AgentID: alicePrivAgent.ID, ArchProcessID: apPriv.ID, ExtraDownstreams: datatypes.JSON("[]"), EnrollSource: models.EnrollSourceDiscovery, ProbeDesiredState: models.ProbeDesiredStateDisarmed, ProbeStatus: models.ProbeStatusDisarmed}
	_ = db.Create(&procShared).Error
	_ = db.Create(&procPriv).Error

	svcShared := models.ModuleService{ModuleID: nil, ServiceName: "shared.v1.AlphaSvc", Source: models.ServiceSourceCSGDiscovered}
	svcPriv := models.ModuleService{ModuleID: nil, ServiceName: "priv.v1.SecretSvc", Source: models.ServiceSourceCSGDiscovered}
	_ = db.Create(&svcShared).Error
	_ = db.Create(&svcPriv).Error

	_ = db.Create(&models.ServicePlacement{ModuleServiceID: svcShared.ID, ProcessID: procShared.ID, ObservedTransport: models.ObservedTransportTCP, Status: models.PlacementStatusActive}).Error
	_ = db.Create(&models.ServicePlacement{ModuleServiceID: svcPriv.ID, ProcessID: procPriv.ID, ObservedTransport: models.ObservedTransportMESH, Status: models.PlacementStatusActive}).Error

	// 1. Bob 查询默认物理资产 -> 仅见 SHARED，绝不见 Alice PRIVATE
	reqBob := httptest.NewRequest(http.MethodGet, "/api/discovery/physical", nil)
	reqBob.Header.Set("Authorization", "Bearer "+bobToken)
	wBob := httptest.NewRecorder()
	r.ServeHTTP(wBob, reqBob)
	if wBob.Code != http.StatusOK {
		t.Fatalf("GET /api/discovery/physical 失败: %d %s", wBob.Code, wBob.Body.String())
	}
	var respBob struct {
		Processes  []map[string]interface{} `json:"processes"`
		Placements []map[string]interface{} `json:"placements"`
	}
	_ = json.Unmarshal(wBob.Body.Bytes(), &respBob)
	if len(respBob.Processes) != 1 || len(respBob.Placements) != 1 {
		t.Fatalf("Bob 默认视图应仅含 1 条 SHARED 进程与放置，实际: %+v", respBob)
	}

	// 2. Bob 指定 Alice PRIVATE agent_id -> 403
	reqBob403 := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/physical?agent_id=%d", alicePrivAgent.ID), nil)
	reqBob403.Header.Set("Authorization", "Bearer "+bobToken)
	wBob403 := httptest.NewRecorder()
	r.ServeHTTP(wBob403, reqBob403)
	if wBob403.Code != http.StatusForbidden {
		t.Fatalf("Bob 指定 Alice PRIVATE agent_id 期望 403，实际 %d", wBob403.Code)
	}

	// 3. Alice 指定自身 PRIVATE agent_id + include_provisional=true -> 200 且看到私有试用进程
	reqAlice := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/physical?agent_id=%d&include_provisional=true", alicePrivAgent.ID), nil)
	reqAlice.Header.Set("Authorization", "Bearer "+aliceToken)
	wAlice := httptest.NewRecorder()
	r.ServeHTTP(wAlice, reqAlice)
	if wAlice.Code != http.StatusOK {
		t.Fatalf("Alice 查询自有 PRIVATE 期望 200，实际 %d %s", wAlice.Code, wAlice.Body.String())
	}
	var respAlice struct {
		Processes []map[string]interface{} `json:"processes"`
	}
	_ = json.Unmarshal(wAlice.Body.Bytes(), &respAlice)
	if len(respAlice.Processes) != 1 {
		t.Fatalf("Alice 期望看到 1 条 PRIVATE PROVISIONAL 进程，实际: %+v", respAlice)
	}
}

func TestSC20_PhysicalLogicalDecoupling_ZeroFakeModules_ZeroModuleTopology(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}
	discovery.ResetEnrollRateLimiter()

	r, db := setupRouterAndDB(t)
	_ = db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "discovery.v2_enabled").Update("setting_val", "true").Error

	// 构造 50 个并发物理进程与端到端调用边
	const procCount = 50
	svcs := make([]agentproto.CSGServiceInfo, 0, procCount)
	subs := make([]agentproto.CSGSubscription, 0, procCount-1)
	disc := make([]agentproto.DiscoveredProcess, 0, procCount)
	for i := 0; i < procCount; i++ {
		pid := uint32(7000 + i)
		port := uint16(25000 + i)
		pname := fmt.Sprintf("sc20_worker_%02d", i)
		sname := fmt.Sprintf("sc20.v1.WorkerSvc%02d", i)
		svcs = append(svcs, agentproto.CSGServiceInfo{
			ServiceName: sname, Addr: fmt.Sprintf("tcp://ipv4:10.50.0.1:%d", port),
			Transport: "TCP", Port: port, ServerPID: pid, ServerProc: pname, IsLocal: true,
		})
		disc = append(disc, agentproto.DiscoveredProcess{
			PID: pid, ListenPort: port, ExeName: "/opt/sc20/" + pname, Cmdline: "/opt/sc20/" + pname + " --tier=prod", Transports: []string{"TCP"},
		})
		if i > 0 {
			subs = append(subs, agentproto.CSGSubscription{
				ClientPID: pid, ClientProc: pname, ServiceName: "sc20.v1.WorkerSvc00", IsLocal: true,
			})
		}
	}
	liveTopo := agentproto.CSGTopologyReport{DiscoveryMode: "CROSTCP_RPCMON", Services: svcs, Subscriptions: subs}
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/agent/v1/csg/topology":
			_ = json.NewEncoder(w).Encode(liveTopo)
		case "/agent/v1/processes/discover":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": disc})
		default:
			http.NotFound(w, req)
		}
	}))
	defer agentSrv.Close()

	adminUser := createTestUserInDB(t, db, "sc20_admin", config.AdminRole)
	normalUser := createTestUserInDB(t, db, "sc20_dev", "developer")
	adminToken := generateTestJWT(t, adminUser)
	devToken := generateTestJWT(t, normalUser)

	agent := models.Agent{
		Name: "sc20-shared-agent", Endpoint: agentSrv.URL, Scope: models.ScopeShared, Status: models.AgentStatusOnline,
		ExpectedArmedDigest: "sc20-digest-immutable",
	}
	_ = db.Create(&agent).Error

	// 1. 普通开发者获取 live 并一次性批量 Enroll 50 个进程（SLA <= 3s，0 假模块，0 Armed 修改）
	liveReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/discovery/live?agent_id=%d", agent.ID), nil)
	liveReq.Header.Set("Authorization", "Bearer "+devToken)
	liveW := httptest.NewRecorder()
	r.ServeHTTP(liveW, liveReq)
	if liveW.Code != http.StatusOK {
		t.Fatalf("live 50 失败: %d %s", liveW.Code, liveW.Body.String())
	}
	var liveBody struct {
		Processes []struct {
			ProcessKey string `json:"process_key"`
		} `json:"processes"`
	}
	_ = json.Unmarshal(liveW.Body.Bytes(), &liveBody)
	if len(liveBody.Processes) != procCount {
		t.Fatalf("期望 50 个发现进程，实际 %d", len(liveBody.Processes))
	}
	items := make([]map[string]interface{}, 0, procCount)
	for _, p := range liveBody.Processes {
		items = append(items, map[string]interface{}{"process_key": p.ProcessKey})
	}
	enrollBody, _ := json.Marshal(map[string]interface{}{"agent_id": agent.ID, "items": items})

	start := time.Now()
	enrollReq := httptest.NewRequest(http.MethodPost, "/api/discovery/enroll", bytes.NewReader(enrollBody))
	enrollReq.Header.Set("Authorization", "Bearer "+devToken)
	enrollReq.Header.Set("Content-Type", "application/json")
	enrollW := httptest.NewRecorder()
	r.ServeHTTP(enrollW, enrollReq)
	elapsed := time.Since(start)
	if enrollW.Code != http.StatusOK {
		t.Fatalf("SC20 Enroll 50 失败: %d %s", enrollW.Code, enrollW.Body.String())
	}
	if elapsed > 3*time.Second {
		t.Fatalf("SC20 50 进程 Enroll SLA 超出 3s 上限，实际耗时: %v", elapsed)
	}

	var modCount int64
	_ = db.Model(&models.Module{}).Count(&modCount).Error
	if modCount != 0 {
		t.Fatalf("SC20 要求逻辑模块数为 0，实际: %d", modCount)
	}
	var agentAfter models.Agent
	_ = db.First(&agentAfter, agent.ID).Error
	if agentAfter.ExpectedArmedDigest != "sc20-digest-immutable" {
		t.Fatalf("Agent ExpectedArmedDigest 不得变化: %s", agentAfter.ExpectedArmedDigest)
	}
	var armedCount int64
	_ = db.Model(&models.Process{}).Where("agent_id = ? AND probe_desired_state = ?", agent.ID, models.ProbeDesiredStateArmed).Count(&armedCount).Error
	if armedCount != 0 {
		t.Fatalf("所有新纳管挂载必须为 DISARMED，实际 armed=%d", armedCount)
	}

	// 2. 0 个 Module 下的物理拓扑直接输出 PROCESS 节点与 PROCESS->PROCESS 边
	topoReq := httptest.NewRequest(http.MethodGet, "/api/arch/topology/graph?view=PHYSICAL", nil)
	topoReq.Header.Set("Authorization", "Bearer "+devToken)
	topoW := httptest.NewRecorder()
	r.ServeHTTP(topoW, topoReq)
	if topoW.Code != http.StatusOK {
		t.Fatalf("物理拓扑失败: %d %s", topoW.Code, topoW.Body.String())
	}
	var topoBody struct {
		Nodes []map[string]interface{} `json:"nodes"`
		Edges []map[string]interface{} `json:"edges"`
	}
	_ = json.Unmarshal(topoW.Body.Bytes(), &topoBody)
	if len(topoBody.Edges) < procCount-1 {
		t.Fatalf("0 模块下物理拓扑至少应渲染 %d 条进程边，实际 %d", procCount-1, len(topoBody.Edges))
	}

	// 3. 存量 CSG 占位模块迁移 API (Dry-Run -> Execute -> Rollback；非管理员返回 403)
	sub := models.Subsystem{Code: "SUB_SC20", Name: "SC20"}
	_ = db.Create(&sub).Error
	fg := models.FunctionalGroup{SubsystemID: sub.ID, Code: "CSG_DISCOVERED", Name: "占位组", Level: 1, Path: "/1/"}
	_ = db.Create(&fg).Error
	fakeMod := models.Module{SubsystemID: sub.ID, GroupID: fg.ID, Code: "MOD_legacy_sc20", Name: "历史占位", ModuleKind: models.ModuleKindBusiness}
	_ = db.Create(&fakeMod).Error

	devMigReq := httptest.NewRequest(http.MethodPost, "/api/admin/migrations/csg-placeholder-modules", bytes.NewReader([]byte(`{"dry_run":true}`)))
	devMigReq.Header.Set("Authorization", "Bearer "+devToken)
	devMigW := httptest.NewRecorder()
	r.ServeHTTP(devMigW, devMigReq)
	if devMigW.Code != http.StatusForbidden {
		t.Fatalf("非管理员调用迁移期望 403，实际 %d", devMigW.Code)
	}

	execMigReq := httptest.NewRequest(http.MethodPost, "/api/admin/migrations/csg-placeholder-modules", bytes.NewReader([]byte(`{"dry_run":false,"action":"EXECUTE"}`)))
	execMigReq.Header.Set("Authorization", "Bearer "+adminToken)
	execMigReq.Header.Set("Content-Type", "application/json")
	execMigW := httptest.NewRecorder()
	r.ServeHTTP(execMigW, execMigReq)
	if execMigW.Code != http.StatusOK {
		t.Fatalf("管理员执行迁移失败: %d %s", execMigW.Code, execMigW.Body.String())
	}
	var migResp struct {
		ClearedModules int    `json:"cleared_modules"`
		BackupBatchID  string `json:"backup_batch_id"`
	}
	_ = json.Unmarshal(execMigW.Body.Bytes(), &migResp)
	if migResp.ClearedModules != 1 || migResp.BackupBatchID == "" {
		t.Fatalf("期望清理 1 个历史占位模块，实际: %+v", migResp)
	}
	var remainingFake int64
	_ = db.Model(&models.Module{}).Where("id = ?", fakeMod.ID).Count(&remainingFake).Error
	if remainingFake != 0 {
		t.Fatalf("执行迁移后假模块应为 0")
	}

	rbReq := httptest.NewRequest(http.MethodPost, "/api/admin/migrations/csg-placeholder-modules", bytes.NewReader([]byte(fmt.Sprintf(`{"dry_run":false,"action":"ROLLBACK","batch_id":%q}`, migResp.BackupBatchID))))
	rbReq.Header.Set("Authorization", "Bearer "+adminToken)
	rbReq.Header.Set("Content-Type", "application/json")
	rbW := httptest.NewRecorder()
	r.ServeHTTP(rbW, rbReq)
	if rbW.Code != http.StatusOK {
		t.Fatalf("回滚失败: %d %s", rbW.Code, rbW.Body.String())
	}
	_ = db.Model(&models.Module{}).Where("id = ?", fakeMod.ID).Count(&remainingFake).Error
	if remainingFake != 1 {
		t.Fatalf("回滚后假模块应恢复")
	}
}
