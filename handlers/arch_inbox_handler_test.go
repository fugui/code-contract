package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"code-contract/config"
	"code-contract/handlers"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/gitclient"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"gorm.io/datatypes"
)

func TestSC22_DiscoveryInbox_1ProcNModules_DepSynthesis_Backfill_AndMRImpact(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}
	t.Setenv("CONTRACT_CRYPTO_KEY", "m32-sc22-crypto-key-32bytes-test")

	r, db := setupRouterAndDB(t)
	_ = db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "outbound_trusted_cidrs").
		Update("setting_val", "127.0.0.1/32").Error

	admin := createTestUserInDB(t, db, "sc22_admin", config.AdminRole)
	modOwner := createTestUserInDB(t, db, "sc22_owner", "developer")
	otherUser := createTestUserInDB(t, db, "sc22_other", "developer")

	adminTok := generateTestJWT(t, admin)
	ownerTok := generateTestJWT(t, modOwner)
	otherTok := generateTestJWT(t, otherUser)

	// 1. 构造架构：1 个核心进程 ap1 未来将承载 2 个模块 mod1/mod2；ap2 承载消费方模块 modCons；ap3 承载多宿主另一方
	sub := models.Subsystem{Code: "SC22_SUB", Name: "SC22子系统"}
	_ = db.Create(&sub).Error
	fg := models.FunctionalGroup{SubsystemID: sub.ID, Code: "SC22_FG", Name: "SC22功能组", Level: 1, Path: "/1/"}
	_ = db.Create(&fg).Error
	pgrp := models.ArchProcessGroup{Code: "PGRP_SC22", Name: "SC22进程组"}
	_ = db.Create(&pgrp).Error

	ap1 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-sc22-core", Name: "核心复合进程", Lifecycle: models.ArchProcLifecycleConfirmed}
	ap2 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-sc22-edge", Name: "边缘网关进程", Lifecycle: models.ArchProcLifecycleConfirmed}
	apPriv := models.ArchProcess{GroupID: pgrp.ID, Code: "prv-sc22", Name: "私有实验进程", Lifecycle: models.ArchProcLifecycleProvisional}
	_ = db.Create(&ap1).Error
	_ = db.Create(&ap2).Error
	_ = db.Create(&apPriv).Error

	mod1 := models.Module{SubsystemID: sub.ID, GroupID: fg.ID, Code: "SC22_M1", Name: "订单模块", OwnerID: &modOwner.ID, LayerLevel: 2}
	mod2 := models.Module{SubsystemID: sub.ID, GroupID: fg.ID, Code: "SC22_M2", Name: "支付模块", OwnerID: &modOwner.ID, LayerLevel: 2}
	modCons := models.Module{SubsystemID: sub.ID, GroupID: fg.ID, Code: "SC22_CONS", Name: "网关模块", ArchProcessID: &ap2.ID, OwnerID: &modOwner.ID, LayerLevel: 5}
	libMod := models.Module{SubsystemID: sub.ID, GroupID: fg.ID, Code: "SC22_LIB", Name: "共享接口库", ModuleKind: models.ModuleKindContractLib}
	_ = db.Create(&mod1).Error
	_ = db.Create(&mod2).Error
	_ = db.Create(&modCons).Error
	_ = db.Create(&libMod).Error

	sharedAgent := models.Agent{Name: "sc22-shared", Endpoint: "http://10.70.1.1:19001", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	privAgent := models.Agent{Name: "sc22-private", Endpoint: "http://10.70.9.1:19001", Scope: models.ScopePrivate, OwnerID: &otherUser.ID, Status: models.AgentStatusOnline}
	_ = db.Create(&sharedAgent).Error
	_ = db.Create(&privAgent).Error

	mkProc := func(agID, apID uint64, port int) models.Process {
		p := models.Process{
			AgentID: agID, ArchProcessID: apID, EffectiveListenPort: port,
			ExtraDownstreams: datatypes.JSON("[]"), EnrollSource: models.EnrollSourceDiscovery,
			ProbeDesiredState: models.ProbeDesiredStateDisarmed, ProbeStatus: models.ProbeStatusDisarmed,
		}
		_ = db.Create(&p).Error
		return p
	}
	p1 := mkProc(sharedAgent.ID, ap1.ID, 33001)
	p2 := mkProc(sharedAgent.ID, ap2.ID, 33002)
	pPriv := mkProc(privAgent.ID, apPriv.ID, 33009)

	// 2. 创建未认领服务：svc1/svc2 同属 ap1（验证 1 进程 : N 模块），svcMulti 横跨 ap1+ap2（验证 MULTI_HOST 拆分），svcPriv 仅私有环境
	svc1 := models.ModuleService{ServiceName: "sc22.v1.OrderSvc", ProtoServiceName: "sc22.v1.OrderSvc", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	svc2 := models.ModuleService{ServiceName: "sc22.v1.PaySvc", ProtoServiceName: "sc22.v1.PaySvc", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	svcMulti := models.ModuleService{ServiceName: "sc22.v1.NotifySvc", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	svcPriv := models.ModuleService{ServiceName: "sc22.v1.PrivateOnlySvc", Source: models.ServiceSourceCSGDiscovered, DiffIgnorePaths: datatypes.JSON("[]")}
	_ = db.Create(&svc1).Error
	_ = db.Create(&svc2).Error
	_ = db.Create(&svcMulti).Error
	_ = db.Create(&svcPriv).Error

	now := time.Now().UTC()
	mkPl := func(svcID, procID uint64) {
		_ = db.Create(&models.ServicePlacement{
			ModuleServiceID: svcID, ProcessID: procID, RawServiceName: "raw",
			Endpoints: datatypes.JSON(`["tcp://10.70.1.1:33001"]`), ObservedTransport: models.ObservedTransportTCP,
			InstanceCount: 1, Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now,
		}).Error
	}
	mkPl(svc1.ID, p1.ID)
	mkPl(svc2.ID, p1.ID)
	mkPl(svcMulti.ID, p1.ID)
	mkPl(svcMulti.ID, p2.ID)
	mkPl(svcPriv.ID, pPriv.ID)

	// ap2 订阅 svc1；priv 订阅 svc1
	_ = db.Create(&models.ProcessDependency{
		ConsumerProcessID: p2.ID, ServiceName: svc1.ServiceName, ServiceID: &svc1.ID,
		ProviderKind: models.ProviderKindLocalEnrolled, Source: models.ProcDepSourceCSGSub,
		Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now,
	}).Error
	_ = db.Create(&models.ProcessDependency{
		ConsumerProcessID: pPriv.ID, ServiceName: svc1.ServiceName, ServiceID: &svc1.ID,
		ProviderKind: models.ProviderKindLocalEnrolled, Source: models.ProcDepSourceCSGSub,
		Status: models.PlacementStatusActive, FirstSeenAt: now, LastSeenAt: now,
	}).Error

	// 历史录制流量（用于验证认领后 REATTRIBUTE 归因回补）
	sess := models.RecordingSession{
		SessionNo: "SC22_S1", Name: "SC22历史录制", Scope: models.ScopeShared, OwnerID: admin.ID,
		Status: models.SessionStatusCompleted, LeasedAgentIDs: datatypes.JSON("[]"), TargetProcessIDs: datatypes.JSON("[]"),
	}
	_ = db.Create(&sess).Error
	_ = db.Create(&models.TrafficRecord{
		CreatedAt: now, SessionID: sess.ID, SeqNo: 1, AgentID: sharedAgent.ID, ProcessID: p2.ID,
		CapturedPID: 101, ConnKey: "c1", Direction: models.TrafficDirEgress, ExchangeType: models.ExchangeUnary,
		SrcAddr: "10.70.1.1:40001", DstAddr: "10.70.1.1:33001", ReqCapturedAtNs: now.UnixNano(),
		TransportProto: 1, SNO: 1, ServiceName: svc1.ServiceName, Operation: "Create", IsReplayable: true,
	}).Error

	postJSON := func(path, token string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// Step A: GET /api/arch/inbox 仅返回 SHARED 权威环境分组，不含 PrivateOnlySvc
	reqInbox := httptest.NewRequest(http.MethodGet, "/api/arch/inbox", nil)
	reqInbox.Header.Set("Authorization", "Bearer "+otherTok)
	wInbox := httptest.NewRecorder()
	r.ServeHTTP(wInbox, reqInbox)
	if wInbox.Code != http.StatusOK {
		t.Fatalf("GET /api/arch/inbox 期望 200，实际 %d body=%s", wInbox.Code, wInbox.Body.String())
	}
	if strings.Contains(wInbox.Body.String(), "PrivateOnlySvc") {
		t.Fatalf("收件箱不得泄露 PRIVATE 服务: %s", wInbox.Body.String())
	}
	var inboxDTO topology.ArchInboxResponseDTO
	_ = json.Unmarshal(wInbox.Body.Bytes(), &inboxDTO)
	if inboxDTO.Summary.UnclaimedCount != 2 || inboxDTO.Summary.MultiHostCount != 1 {
		t.Fatalf("收件箱计数不符: %+v", inboxDTO.Summary)
	}

	// Step B: 1 进程 : N 模块认领（普通用户 403 -> 模块负责人 200 -> 违反 I4 422）
	if w := postJSON("/api/arch/services/claim", otherTok, map[string]any{
		"items": []map[string]any{{"service_id": svc1.ID, "module_id": mod1.ID}},
	}); w.Code != http.StatusForbidden {
		t.Fatalf("非负责人认领期望 403，实际 %d body=%s", w.Code, w.Body.String())
	}
	if w := postJSON("/api/arch/services/claim", ownerTok, map[string]any{
		"items": []map[string]any{{"service_id": svc1.ID, "module_id": modCons.ID}},
	}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("违反 I4 认领期望 422，实际 %d body=%s", w.Code, w.Body.String())
	}

	wClaim := postJSON("/api/arch/services/claim", ownerTok, map[string]any{
		"items": []map[string]any{
			{"service_id": svc1.ID, "module_id": mod1.ID},
			{"service_id": svc2.ID, "module_id": mod2.ID},
		},
	})
	if wClaim.Code != http.StatusOK {
		t.Fatalf("模块负责人批量认领 1进程:2模块 期望 200，实际 %d body=%s", wClaim.Code, wClaim.Body.String())
	}
	var claimResp topology.ClaimResult
	_ = json.Unmarshal(wClaim.Body.Bytes(), &claimResp)
	if claimResp.ClaimedCount != 2 || claimResp.AttributionJobID == nil {
		t.Fatalf("认领响应不符: %+v", claimResp)
	}

	// Step C: 执行归因回补 Worker 并查询 GET /api/attribution-jobs/:id 验证历史流量回补完成
	worker := topology.NewAttributionJobWorker(db, nil, handlers.GetGlobalSSEBroker(), topology.AttributionWorkerOptions{BatchInterval: time.Millisecond})
	if err := worker.RunJob(context.Background(), *claimResp.AttributionJobID); err != nil {
		t.Fatalf("RunJob 失败: %v", err)
	}
	reqJob := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/attribution-jobs/%d", *claimResp.AttributionJobID), nil)
	reqJob.Header.Set("Authorization", "Bearer "+ownerTok)
	wJob := httptest.NewRecorder()
	r.ServeHTTP(wJob, reqJob)
	if wJob.Code != http.StatusOK {
		t.Fatalf("GET attribution-job 期望 200，实际 %d", wJob.Code)
	}
	var rec models.TrafficRecord
	_ = db.Where("session_id = ? AND seq_no = 1", sess.ID).First(&rec).Error
	if rec.SrcModuleID == nil || *rec.SrcModuleID != modCons.ID || rec.DstModuleID == nil || *rec.DstModuleID != mod1.ID {
		t.Fatalf("历史流量应被回补 src=modCons dst=mod1，实际: %+v", rec)
	}

	// Step D: 多宿主拆分（普通用户 403，管理员 200）
	splitBody := map[string]any{
		"source_service_id": svcMulti.ID,
		"definer_module_id": libMod.ID,
		"implementations": []map[string]any{
			{"arch_process_id": ap1.ID, "module_id": mod1.ID, "impl_key": "core"},
			{"arch_process_id": ap2.ID, "module_id": modCons.ID, "impl_key": "edge"},
		},
	}
	if w := postJSON("/api/arch/services/split-multi-impl", ownerTok, splitBody); w.Code != http.StatusForbidden {
		t.Fatalf("非管理员拆分多宿主期望 403，实际 %d", w.Code)
	}
	if w := postJSON("/api/arch/services/split-multi-impl", adminTok, splitBody); w.Code != http.StatusOK {
		t.Fatalf("管理员拆分多宿主期望 200，实际 %d body=%s", w.Code, w.Body.String())
	}

	// Step E: 依赖反向审批（批准 ap2 -> svc1）+ Unclaim 被引用拦截 409 DEPENDENCY_REFERENCED
	if w := postJSON("/api/arch/dependency-suggestions/decide", adminTok, map[string]any{
		"consumer_arch_process_id": ap2.ID,
		"service_id":               svc1.ID,
		"decision":                 "APPROVE",
		"consumer_module_id":       modCons.ID,
	}); w.Code != http.StatusOK {
		t.Fatalf("依赖批准期望 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	wUnclaim := postJSON("/api/arch/services/unclaim", ownerTok, map[string]any{"service_id": svc1.ID})
	if wUnclaim.Code != http.StatusConflict || !strings.Contains(wUnclaim.Body.String(), "DEPENDENCY_REFERENCED") {
		t.Fatalf("Unclaim I6 期望 409 DEPENDENCY_REFERENCED，实际 %d body=%s", wUnclaim.Code, wUnclaim.Body.String())
	}
	// 取消认领 svc2（无依赖引用）-> 200 UNCLAIMED
	wUnclaim2 := postJSON("/api/arch/services/unclaim", ownerTok, map[string]any{"service_id": svc2.ID})
	if wUnclaim2.Code != http.StatusOK || !strings.Contains(wUnclaim2.Body.String(), "UNCLAIMED") {
		t.Fatalf("Unclaim svc2 期望 200 UNCLAIMED，实际 %d body=%s", wUnclaim2.Code, wUnclaim2.Body.String())
	}

	// Step F: MR 门禁影响面仅统计 SHARED（1 环境 1 发布进程 1 消费进程），排除 PRIVATE
	var (
		mu      sync.Mutex
		mrNotes []string
	)
	mockGit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/notes") {
			raw, _ := io.ReadAll(r.Body)
			var payload struct {
				Body string `json:"body"`
			}
			_ = json.Unmarshal(raw, &payload)
			mu.Lock()
			mrNotes = append(mrNotes, payload.Body)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id": 1001}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mockGit.Close()

	repo := models.GitRepo{
		Name: "sc22-repo", Provider: models.GitProviderGitLab,
		RepoURL: mockGit.URL + "/sc22/repo.git", APIBaseURL: mockGit.URL + "/api/v4",
		DefaultBranch: "main", AuthType: models.GitAuthToken, SyncMode: models.GitSyncWebhook,
		PlatformRepoID: 1, SourceMode: models.GitSourceModePlatformShared,
	}
	_ = repo.SetCredentials("tok")
	_ = db.Create(&repo).Error
	bundle := models.GitProtoBundle{
		GitRepoID: repo.ID, BundleName: "sc22", TargetSchemaName: "sc22-schema",
		Category: models.ProtoCategoryBusiness, SubdirPath: "proto", IncludeDirsJSON: datatypes.JSON(`["."]`),
	}
	_ = db.Create(&bundle).Error

	reg, _ := decoder.NewRegistry(db)
	v1 := `syntax = "proto3"; package sc22.v1;
service OrderSvc { rpc Create (Req) returns (Rsp); }
message Req { int64 id = 1; }
message Rsp { bool ok = 1; }`
	v2 := `syntax = "proto3"; package sc22.v1;
service OrderSvc { rpc Create (Req) returns (Rsp); }
message Req { string id = 1; }
message Rsp { bool ok = 1; }`
	sc, _, _, err := reg.CompileAndSaveSchema(context.Background(), decoder.CompileProtoBundleInput{
		Name: bundle.TargetSchemaName, Category: bundle.Category, VersionTag: "v1",
		Files: map[string]string{"order.proto": v1}, ProtoIncludeDirs: []string{"."}, AutoActivate: true,
	})
	if err != nil {
		t.Fatalf("CompileAndSaveSchema: %v", err)
	}
	_ = db.Model(&bundle).Update("last_schema_id", sc.ID).Error

	runner := gitclient.NewMRGateRunner(db, reg, nil)
	if _, err := runner.CreateAndExecuteBundleRun(context.Background(), gitclient.MRExecuteInput{
		Repo: &repo, Bundle: &bundle, MRIID: 22, EventPhase: models.MREventPhaseGate,
		MRTitle: "sc22 break", SourceBranch: "feat", TargetBranch: "main", HeadCommitSHA: "sc22sha",
		PatchFiles: []gitclient.MRPatchFile{{ChangeType: gitclient.MRChangeModified, NewPath: "proto/order.proto", Content: v2}},
	}); err != nil {
		t.Fatalf("MRGateRunner: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(mrNotes) != 1 || !strings.Contains(mrNotes[0], "该服务当前在 1 个环境由 1 个物理进程发布、被 1 个消费方进程订阅") {
		t.Fatalf("SC-22 MR 影响面应排除 PRIVATE 为 1/1/1，实际: %v", mrNotes)
	}
}
