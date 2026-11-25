package gitclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"code-common/backend/testdb"
	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/models"

	"gorm.io/datatypes"
)

func TestMRGate_LiveImpactExcludesPrivateEnvironments(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过数据库隔离测试")
	}
	t.Setenv("CONTRACT_CRYPTO_KEY", "m32-mr-impact-crypto-key-32bytes")

	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB 失败: %v", err)
	}
	if err := db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "outbound_trusted_cidrs").
		Update("setting_val", "127.0.0.1/32").Error; err != nil {
		t.Fatalf("设置 outbound_trusted_cidrs 失败: %v", err)
	}

	var (
		mu       sync.Mutex
		comments []string
	)
	mockGitLab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/notes") {
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Body string `json:"body"`
			}
			_ = json.Unmarshal(body, &req)
			mu.Lock()
			comments = append(comments, req.Body)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id": 7701, "body": "ok"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mockGitLab.Close()

	// 1. 构造架构、2 个 SHARED 环境 + 1 个 PRIVATE 环境放置与订阅边
	pgrp := models.ArchProcessGroup{Code: "PGRP_MR", Name: "MR 进程组"}
	_ = db.Create(&pgrp).Error
	apProv1 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-mr-prov1", Name: "MR发布1", Lifecycle: models.ArchProcLifecycleConfirmed}
	apProv2 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-mr-prov2", Name: "MR发布2", Lifecycle: models.ArchProcLifecycleConfirmed}
	apCons1 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-mr-cons1", Name: "MR消费1", Lifecycle: models.ArchProcLifecycleConfirmed}
	apCons2 := models.ArchProcess{GroupID: pgrp.ID, Code: "ap-mr-cons2", Name: "MR消费2", Lifecycle: models.ArchProcLifecycleConfirmed}
	apPriv := models.ArchProcess{GroupID: pgrp.ID, Code: "prv-mr-secret", Name: "MR私有发布", Lifecycle: models.ArchProcLifecycleProvisional}
	_ = db.Create(&apProv1).Error
	_ = db.Create(&apProv2).Error
	_ = db.Create(&apCons1).Error
	_ = db.Create(&apCons2).Error
	_ = db.Create(&apPriv).Error

	sharedA := models.Agent{Name: "mr-shared-sit", Endpoint: "http://10.60.1.1:19001", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	sharedB := models.Agent{Name: "mr-shared-uat", Endpoint: "http://10.60.2.1:19001", Scope: models.ScopeShared, Status: models.AgentStatusOnline}
	privA := models.Agent{Name: "mr-private-devbox", Endpoint: "http://10.60.9.1:19001", Scope: models.ScopePrivate, Status: models.AgentStatusOnline}
	_ = db.Create(&sharedA).Error
	_ = db.Create(&sharedB).Error
	_ = db.Create(&privA).Error

	mkProc := func(agentID, apID uint64, port int) models.Process {
		p := models.Process{
			AgentID: agentID, ArchProcessID: apID, EffectiveListenPort: port,
			ExtraDownstreams: datatypes.JSON("[]"), EnrollSource: models.EnrollSourceDiscovery,
			ProbeDesiredState: models.ProbeDesiredStateDisarmed, ProbeStatus: models.ProbeStatusDisarmed,
		}
		_ = db.Create(&p).Error
		return p
	}
	pSharedProv1 := mkProc(sharedA.ID, apProv1.ID, 32001)
	pSharedProv2 := mkProc(sharedB.ID, apProv2.ID, 32002)
	pSharedCons1 := mkProc(sharedA.ID, apCons1.ID, 32011)
	pSharedCons2 := mkProc(sharedB.ID, apCons2.ID, 32012)
	pPrivProv := mkProc(privA.ID, apPriv.ID, 32091)
	pPrivCons := mkProc(privA.ID, apPriv.ID, 32092)

	svc := models.ModuleService{
		ServiceName:      "PayImpl-PayService",
		ProtoServiceName: "fin.pay.v1.PayService",
		Source:           models.ServiceSourceCSGDiscovered,
		DiffIgnorePaths:  datatypes.JSON("[]"),
	}
	if err := db.Create(&svc).Error; err != nil {
		t.Fatalf("create ModuleService: %v", err)
	}

	now := time.Now().UTC()
	mkPl := func(procID uint64, status string) {
		_ = db.Create(&models.ServicePlacement{
			ModuleServiceID: svc.ID, ProcessID: procID, RawServiceName: svc.ServiceName,
			Endpoints: datatypes.JSON(`["tcp://10.60.1.1:32001"]`), ObservedTransport: models.ObservedTransportTCP,
			InstanceCount: 1, Status: status, FirstSeenAt: now, LastSeenAt: now,
		}).Error
	}
	// SHARED 2 个环境 2 个 ACTIVE 发布进程 + 1 个 RETIRED 噪声
	mkPl(pSharedProv1.ID, models.PlacementStatusActive)
	mkPl(pSharedProv2.ID, models.PlacementStatusActive)
	mkPl(pSharedCons1.ID, models.PlacementStatusRetired)
	// PRIVATE 发布不得计入
	mkPl(pPrivProv.ID, models.PlacementStatusActive)

	mkSub := func(procID uint64, status string) {
		_ = db.Create(&models.ProcessDependency{
			ConsumerProcessID: procID, ServiceName: svc.ServiceName, ServiceID: models.Uint64Ptr(svc.ID),
			ProviderKind: models.ProviderKindLocalEnrolled, Source: models.ProcDepSourceCSGSub,
			Status: status, FirstSeenAt: now, LastSeenAt: now,
		}).Error
	}
	// SHARED 2 个消费进程订阅 + PRIVATE 订阅（不得计入）
	mkSub(pSharedCons1.ID, models.PlacementStatusActive)
	mkSub(pSharedCons2.ID, models.PlacementStatusActive)
	mkSub(pPrivCons.ID, models.PlacementStatusActive)

	// 2. 初始化 GitRepo + Bundle + 基线 Schema，并执行含破坏性变更的 MR 检查
	repo := models.GitRepo{
		Name: "mr-impact-repo", Provider: models.GitProviderGitLab,
		RepoURL: mockGitLab.URL + "/fin/mr-impact.git", APIBaseURL: mockGitLab.URL + "/api/v4",
		DefaultBranch: "main", AuthType: models.GitAuthToken, SyncMode: models.GitSyncWebhook,
	}
	_ = repo.SetCredentials("token")
	_ = db.Create(&repo).Error
	bundle := models.GitProtoBundle{
		GitRepoID: repo.ID, BundleName: "pay", TargetSchemaName: "pay-impact-schema",
		Category: models.ProtoCategoryBusiness, SubdirPath: "proto", IncludeDirsJSON: datatypes.JSON(`["."]`),
	}
	_ = db.Create(&bundle).Error

	reg, err := decoder.NewRegistry(db)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	baselineProto := `syntax = "proto3";
package fin.pay.v1;
service PayService { rpc CreatePayment (PaymentReq) returns (PaymentRsp); }
message PaymentReq { int64 order_id = 1; }
message PaymentRsp { int64 pay_id = 1; }
`
	baseSchema, _, _, err := reg.CompileAndSaveSchema(context.Background(), decoder.CompileProtoBundleInput{
		Name: bundle.TargetSchemaName, Category: bundle.Category, VersionTag: "v1",
		Files: map[string]string{"pay.proto": baselineProto}, ProtoIncludeDirs: []string{"."},
		AutoActivate: true, SourceType: models.ProtoSourceGitSync,
	})
	if err != nil {
		t.Fatalf("CompileAndSaveSchema: %v", err)
	}
	_ = db.Model(&bundle).Update("last_schema_id", baseSchema.ID).Error

	breakingProto := `syntax = "proto3";
package fin.pay.v1;
service PayService { rpc CreatePayment (PaymentReq) returns (PaymentRsp); }
message PaymentReq { string order_id = 1; }
message PaymentRsp { int64 pay_id = 1; }
`
	runner := NewMRGateRunner(db, reg, nil)
	run, err := runner.CreateAndExecuteBundleRun(context.Background(), MRExecuteInput{
		Repo: &repo, Bundle: &bundle, MRIID: 88, EventPhase: models.MREventPhaseGate,
		MRTitle: "breaking change", SourceBranch: "feat/pay", TargetBranch: "main", HeadCommitSHA: "abc12345",
		PatchFiles: []MRPatchFile{{ChangeType: MRChangeModified, NewPath: "proto/pay.proto", Content: breakingProto}},
	})
	if err != nil {
		t.Fatalf("CreateAndExecuteBundleRun: %v", err)
	}
	if run.Status != models.MRCheckStatusFailed {
		t.Fatalf("破坏性变更期望 FAILED，实际: %s", run.Status)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(comments) != 1 {
		t.Fatalf("期望生成 1 条 MR 评论，实际 %d", len(comments))
	}
	body := comments[0]
	if !strings.Contains(body, "权威 `SHARED` 环境") && !strings.Contains(body, "SHARED") {
		t.Fatalf("MR 评论应包含权威 SHARED 环境影响面摘要，实际:\n%s", body)
	}
	if !strings.Contains(body, "fin.pay.v1.PayService") {
		t.Fatalf("MR 评论应包含受影响服务 fin.pay.v1.PayService，实际:\n%s", body)
	}
	if !strings.Contains(body, "该服务当前在 2 个环境由 2 个物理进程发布、被 2 个消费方进程订阅") {
		t.Fatalf("MR 评论应精确统计 2 个环境、2 个发布进程、2 个消费进程（排除 PRIVATE/RETIRED），实际:\n%s", body)
	}
	if strings.Contains(body, "mr-private-devbox") || strings.Contains(body, "MR私有发布") || strings.Contains(body, "3 个环境") {
		t.Fatalf("MR 评论绝不能泄露或计入 PRIVATE 环境信息:\n%s", body)
	}
}
