package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"code-common/backend/testdb"
	"code-contract/config"
	"code-contract/handlers"
	"code-contract/internal/server/noise"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/datatypes"
)

func TestNoiseRulesEndpoints_AndTrafficFiltering(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB failed: %v", err)
	}
	r, _ := setupGoldenSuiteTestServer(t, db)
	normalUser, userToken := createTestUser(t, db, "noise_user", "member")
	_, adminToken := createTestUser(t, db, "noise_admin", config.AdminRole)

	// 1. 普通用户禁止追加全局噪音规则 (403)
	appendBody, _ := json.Marshal(map[string]string{"rule": "*LogService*"})
	reqForbid := httptest.NewRequest(http.MethodPost, "/api/admin/noise-rules/append", bytes.NewReader(appendBody))
	reqForbid.Header.Set("Authorization", "Bearer "+userToken)
	reqForbid.Header.Set("Content-Type", "application/json")
	wForbid := httptest.NewRecorder()
	r.ServeHTTP(wForbid, reqForbid)
	if wForbid.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin append, got %d", wForbid.Code)
	}

	// 2. 管理员追加全局规则成功 (200)
	reqAppend := httptest.NewRequest(http.MethodPost, "/api/admin/noise-rules/append", bytes.NewReader(appendBody))
	reqAppend.Header.Set("Authorization", "Bearer "+adminToken)
	reqAppend.Header.Set("Content-Type", "application/json")
	wAppend := httptest.NewRecorder()
	r.ServeHTTP(wAppend, reqAppend)
	if wAppend.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin append, got %d body=%s", wAppend.Code, wAppend.Body.String())
	}

	// 3. 普通用户可读合并配置 (GET /api/noise-rules)
	reqGet := httptest.NewRequest(http.MethodGet, "/api/noise-rules", nil)
	reqGet.Header.Set("Authorization", "Bearer "+userToken)
	wGet := httptest.NewRecorder()
	r.ServeHTTP(wGet, reqGet)
	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET /api/noise-rules, got %d", wGet.Code)
	}
	var cfg noise.EffectiveNoiseConfigDTO
	if err := json.Unmarshal(wGet.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode config failed: %v", err)
	}
	if len(cfg.EffectiveRules) != 1 || cfg.EffectiveRules[0] != "*LogService*" {
		t.Fatalf("unexpected effective rules: %v", cfg.EffectiveRules)
	}

	// 4. 构造测试会话和流量记录（含噪音 + 异常破格噪音 + 业务）
	sess := models.RecordingSession{
		SessionNo:          "REC_NOISE_TEST",
		Name:               "Noise Filter Test",
		Scope:              models.ScopePrivate,
		OwnerID:            normalUser.ID,
		Status:             models.SessionStatusCompleted,
		LeasedAgentIDs:     datatypes.JSON("[]"),
		TargetProcessIDs:   datatypes.JSON("[]"),
		ProcessSnapshots:   datatypes.JSON("[]"),
		EndpointSnapshots:  datatypes.JSON("[]"),
		CSGSnapshots:       datatypes.JSON("[]"),
		ProcessDiagnostics: datatypes.JSON("[]"),
		NoiseExcludeRules:  datatypes.JSON("[]"),
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatalf("create session failed: %v", err)
	}

	tBase := int64(1700000000000000000)
	lat := int64(1000)
	recs := []models.TrafficRecord{
		{
			SessionID: sess.ID, SeqNo: 1, AgentID: 1, ProcessID: 1, ConnKey: "c1",
			Direction: "INGRESS", ExchangeType: "UNARY", SrcAddr: "1.1.1.1:100", DstAddr: "1.1.1.2:8080",
			ReqCapturedAtNs: tBase, LatencyUs: &lat, ServiceName: "biz.OrderSvc", Operation: "Create",
			TraceID: "tr1", SpanID: "sp1", FatherSpanID: "", StatusCode: models.U64Numeric(0), CreatedAt: time.Now(),
		},
		{
			SessionID: sess.ID, SeqNo: 2, AgentID: 1, ProcessID: 1, ConnKey: "c1",
			Direction: "EGRESS", ExchangeType: "ONEWAY", SrcAddr: "1.1.1.2:4001", DstAddr: "1.1.1.3:9000",
			ReqCapturedAtNs: tBase + 100, LatencyUs: &lat, ServiceName: "infra.LogService", Operation: "Write",
			TraceID: "tr1", SpanID: "sp2", FatherSpanID: "sp1", StatusCode: models.U64Numeric(0), CreatedAt: time.Now(),
		},
		{
			SessionID: sess.ID, SeqNo: 3, AgentID: 1, ProcessID: 1, ConnKey: "c2",
			Direction: "EGRESS", ExchangeType: "ONEWAY", SrcAddr: "1.1.1.2:4002", DstAddr: "1.1.1.4:9001",
			ReqCapturedAtNs: tBase + 20_000_000, LatencyUs: &lat, ServiceName: "infra.LogService", Operation: "Write",
			TraceID: "tr2", SpanID: "sp3", FatherSpanID: "", StatusCode: models.U64Numeric(0), CreatedAt: time.Now(),
		},
		{
			SessionID: sess.ID, SeqNo: 4, AgentID: 1, ProcessID: 1, ConnKey: "c3",
			Direction: "EGRESS", ExchangeType: "UNARY", SrcAddr: "1.1.1.2:4003", DstAddr: "1.1.1.4:9001",
			ReqCapturedAtNs: tBase + 40_000_000, LatencyUs: &lat, ServiceName: "infra.LogService", Operation: "FlushErr",
			TraceID: "tr3", SpanID: "sp4", FatherSpanID: "", StatusCode: models.U64Numeric(500), CreatedAt: time.Now(),
		},
	}
	for i := range recs {
		if err := db.Create(&recs[i]).Error; err != nil {
			t.Fatalf("create traffic record failed: %v", err)
		}
	}

	// 5. 默认 hide_noise=true：事务树隐藏纯噪音根(sp3)、折叠子树(sp2)、破格展示错误噪音(sp4)
	reqTx := httptest.NewRequest(http.MethodGet, "/api/recording-sessions/"+strconv.FormatUint(sess.ID, 10)+"/transactions", nil)
	reqTx.Header.Set("Authorization", "Bearer "+userToken)
	wTx := httptest.NewRecorder()
	r.ServeHTTP(wTx, reqTx)
	if wTx.Code != http.StatusOK {
		t.Fatalf("transactions failed: %d body=%s", wTx.Code, wTx.Body.String())
	}
	var txResp struct {
		Total                 int `json:"total"`
		HiddenNoiseRootCount  int `json:"hidden_noise_root_count"`
		FoldedNoiseChildCount int `json:"folded_noise_child_count"`
		HiddenNoiseCount      int `json:"hidden_noise_count"`
	}
	if err := json.Unmarshal(wTx.Body.Bytes(), &txResp); err != nil {
		t.Fatalf("decode transactions failed: %v", err)
	}
	if txResp.Total != 2 || txResp.HiddenNoiseRootCount != 1 || txResp.FoldedNoiseChildCount != 1 || txResp.HiddenNoiseCount != 2 {
		t.Fatalf("unexpected transaction noise stats: %+v", txResp)
	}

	// 6. 平铺列表默认隐藏非异常噪音 (total=2: OrderSvc + LogService error)，并报告 hidden_noise_count=2
	reqFlat := httptest.NewRequest(http.MethodGet, "/api/traffic-records?session_id="+strconv.FormatUint(sess.ID, 10), nil)
	reqFlat.Header.Set("Authorization", "Bearer "+userToken)
	wFlat := httptest.NewRecorder()
	r.ServeHTTP(wFlat, reqFlat)
	if wFlat.Code != http.StatusOK {
		t.Fatalf("list traffic failed: %d body=%s", wFlat.Code, wFlat.Body.String())
	}
	var flatResp struct {
		Total            int `json:"total"`
		HiddenNoiseCount int `json:"hidden_noise_count"`
	}
	if err := json.Unmarshal(wFlat.Body.Bytes(), &flatResp); err != nil {
		t.Fatalf("decode flat failed: %v", err)
	}
	if flatResp.Total != 2 || flatResp.HiddenNoiseCount != 2 {
		t.Fatalf("expected flat total=2 hidden_noise_count=2, got %+v", flatResp)
	}

	// 7. 关闭 hide_noise=false 时全部 4 条可见
	reqFlatAll := httptest.NewRequest(http.MethodGet, "/api/traffic-records?session_id="+strconv.FormatUint(sess.ID, 10)+"&hide_noise=false", nil)
	reqFlatAll.Header.Set("Authorization", "Bearer "+userToken)
	wFlatAll := httptest.NewRecorder()
	r.ServeHTTP(wFlatAll, reqFlatAll)
	var flatAllResp struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal(wFlatAll.Body.Bytes(), &flatAllResp)
	if flatAllResp.Total != 4 {
		t.Fatalf("expected flat total=4 when hide_noise=false, got %d", flatAllResp.Total)
	}
}

func TestRecordingSession_SyncExcludeRulesAndFilteredCount(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB failed: %v", err)
	}
	r, _ := setupGoldenSuiteTestServer(t, db)
	_, adminToken := createTestUser(t, db, "rec_noise_admin", config.AdminRole)
	_, _ = noise.AppendGlobalNoiseRule(t.Context(), db, "*MonitorService*", "admin")

	var capturedExclude []string
	mockAgent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == http.MethodPut && req.URL.Path == "/agent/v1/monitors/sync":
			var syncReq agentproto.SyncMonitorReq
			_ = json.NewDecoder(req.Body).Decode(&syncReq)
			capturedExclude = append([]string(nil), syncReq.ExcludeRules...)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"diagnostics": []agentproto.ProcessDiagStatus{},
			})
		case req.Method == http.MethodPost && len(req.URL.Path) > 0:
			_ = json.NewEncoder(w).Encode(agentproto.StopMonitorResp{
				CapturedFrames:      12,
				DroppedFrames:       0,
				FilteredNoiseFrames: 345,
			})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer mockAgent.Close()

	_, orch, _ := handlers.InitRecorderAndDiff(db, handlers.GlobalDecoderRegistry)
	defer func() {
		if orch != nil && orch.Assembler() != nil {
			orch.Assembler().Close()
		}
	}()

	agt := models.Agent{
		Name: "noise-agent", Endpoint: mockAgent.URL,
		Scope: models.ScopeShared, Status: models.AgentStatusOnline, ClaimCode: "c1",
	}
	if err := db.Create(&agt).Error; err != nil {
		t.Fatalf("create agent failed: %v", err)
	}
	apGrp := models.ArchProcessGroup{Code: "NOISE_GRP", Name: "NoiseGrp"}
	if err := db.Create(&apGrp).Error; err != nil {
		t.Fatalf("create arch process group failed: %v", err)
	}
	ap := models.ArchProcess{
		GroupID: apGrp.ID, Code: "noise-proc-1", Name: "noise-proc-1",
		DefaultListenPort: 9001, DefaultCmdPattern: "noise-proc-1",
	}
	if err := db.Create(&ap).Error; err != nil {
		t.Fatalf("create arch process failed: %v", err)
	}
	proc := models.Process{
		AgentID: agt.ID, ArchProcessID: ap.ID, EffectiveListenPort: 9001,
		ExtraDownstreams: datatypes.JSON("[]"),
	}
	if err := db.Create(&proc).Error; err != nil {
		t.Fatalf("create process failed: %v", err)
	}

	startPayload, _ := json.Marshal(map[string]any{
		"name":               "Rec With Noise Filter",
		"scope":              "PRIVATE",
		"target_process_ids": []uint64{proc.ID},
	})
	reqStart := httptest.NewRequest(http.MethodPost, "/api/recording-sessions", bytes.NewReader(startPayload))
	reqStart.Header.Set("Authorization", "Bearer "+adminToken)
	reqStart.Header.Set("Content-Type", "application/json")
	wStart := httptest.NewRecorder()
	r.ServeHTTP(wStart, reqStart)
	if wStart.Code != http.StatusCreated {
		t.Fatalf("start session failed: %d body=%s", wStart.Code, wStart.Body.String())
	}
	var started models.RecordingSession
	_ = json.Unmarshal(wStart.Body.Bytes(), &started)
	if !started.NoiseFilterEnabled {
		t.Fatalf("expected NoiseFilterEnabled=true by default")
	}
	if len(capturedExclude) != 1 || capturedExclude[0] != "*MonitorService*" {
		t.Fatalf("expected SyncMonitorReq.ExcludeRules=[*MonitorService*], got %v", capturedExclude)
	}

	reqStop := httptest.NewRequest(http.MethodPost, "/api/recording-sessions/"+strconv.FormatUint(started.ID, 10)+"/stop", nil)
	reqStop.Header.Set("Authorization", "Bearer "+adminToken)
	wStop := httptest.NewRecorder()
	r.ServeHTTP(wStop, reqStop)
	if wStop.Code != http.StatusOK {
		t.Fatalf("stop session failed: %d body=%s", wStop.Code, wStop.Body.String())
	}
	var stopped models.RecordingSession
	_ = json.Unmarshal(wStop.Body.Bytes(), &stopped)
	if stopped.FilteredNoiseFrames != 345 {
		t.Fatalf("expected FilteredNoiseFrames=345 persisted on stop, got %d", stopped.FilteredNoiseFrames)
	}
}
