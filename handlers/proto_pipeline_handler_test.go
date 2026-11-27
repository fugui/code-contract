package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	commonModels "code-common/backend/models"
	"code-contract/config"
	"code-contract/handlers"
	"code-contract/models"
)

func initTestBareRepoForHandler(t *testing.T, files map[string]string) string {
	t.Helper()
	base := t.TempDir()
	bareDir := filepath.Join(base, "remote.git")
	workDir := filepath.Join(base, "work")

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=ContractBot",
			"GIT_AUTHOR_EMAIL=bot@example.com",
			"GIT_COMMITTER_NAME=ContractBot",
			"GIT_COMMITTER_EMAIL=bot@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
	}

	if err := os.MkdirAll(bareDir, 0755); err != nil {
		t.Fatalf("mkdir bare: %v", err)
	}
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}
	runGit(bareDir, "init", "--bare")
	runGit(workDir, "init", "-b", "main")

	for rel, body := range files {
		abs := filepath.Join(workDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(body), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	runGit(workDir, "add", "-A")
	runGit(workDir, "commit", "-m", "init proto tree")
	runGit(workDir, "remote", "add", "origin", bareDir)
	runGit(workDir, "push", "-u", "origin", "main")
	return bareDir
}

func TestADR26PlatformImport_DualView_RawCleanContent_Conflicts_WebhookAndRBAC(t *testing.T) {
	t.Setenv("CONTRACT_CRYPTO_KEY", "test-contract-crypto-key-m36-32bytes!")
	r, db := setupProtoDocRouterAndDB(t)
	handlers.InitGlobalSyncScheduler(nil)
	defer handlers.InitGlobalSyncScheduler(nil)

	oldCodes := config.AppConfig.Storage.CodesDir
	oldProtos := config.AppConfig.Storage.ProtosDir
	oldSharedWrite := config.AppConfig.Storage.SharedWriteEnabled
	config.AppConfig.Storage.CodesDir = filepath.Join(t.TempDir(), "codes")
	config.AppConfig.Storage.ProtosDir = filepath.Join(t.TempDir(), "protos")
	config.AppConfig.Storage.SharedWriteEnabled = true
	defer func() {
		config.AppConfig.Storage.CodesDir = oldCodes
		config.AppConfig.Storage.ProtosDir = oldProtos
		config.AppConfig.Storage.SharedWriteEnabled = oldSharedWrite
	}()

	adminUser := createTestUserInDB(t, db, "adr26_admin", config.AdminRole)
	viewerUser := createTestUserInDB(t, db, "adr26_viewer", "viewer")
	adminJWT := generateTestJWT(t, adminUser)
	viewerJWT := generateTestJWT(t, viewerUser)

	dep := commonModels.Department{Name: "平台基础部门"}
	if err := db.Create(&dep).Error; err != nil {
		t.Fatalf("create department failed: %v", err)
	}

	barePlat := initTestBareRepoForHandler(t, map[string]string{
		"pg/common/base.proto": "syntax = \"proto3\";\npackage acme.common.v1;\nmessage CommonMeta { string req_id = 1; }\n",
		"pg/order/order.proto": "syntax = \"proto3\";\npackage acme.order.v1;\nimport \"common/base.proto\";\nservice OrderSvc { rpc Create(OrderReq) returns (OrderRsp); }\nmessage OrderReq { acme.common.v1.CommonMeta meta = 1; }\nmessage OrderRsp { int32 code = 1; }\n",
		"PG/ignored.proto":     "syntax = \"proto3\";\npackage ignored;\nmessage Ignored { string a = 1; }\n",
	})
	bareConflict := initTestBareRepoForHandler(t, map[string]string{
		"pg/common/base.proto": "syntax = \"proto3\";\npackage acme.common.v1;\nmessage CommonMeta { int64 req_seq = 1; }\n",
	})

	platRepo1 := commonModels.Repository{
		DepartmentID: dep.ID,
		Name:         "order-core-svc",
		URL:          barePlat,
		HTTPURL:      barePlat,
		OwnerID:      adminUser.ID,
		Branch:       "main",
		ServiceGroup: "trade-sub",
		IsActive:     true,
	}
	platRepo2 := commonModels.Repository{
		DepartmentID: dep.ID,
		Name:         "legacy-common-copy",
		URL:          bareConflict,
		HTTPURL:      bareConflict,
		OwnerID:      adminUser.ID,
		Branch:       "main",
		ServiceGroup: "legacy-sub",
		IsActive:     true,
	}
	if err := db.Create(&platRepo1).Error; err != nil {
		t.Fatalf("create platRepo1: %v", err)
	}
	if err := db.Create(&platRepo2).Error; err != nil {
		t.Fatalf("create platRepo2: %v", err)
	}

	// 1. 验证已下线端点的 404 退场契约
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/git-repos/platform-discover"},
		{http.MethodPost, "/api/git-repos/platform-import"},
		{http.MethodPost, "/api/git-repos"},
		{http.MethodDelete, "/api/git-repos/1"},
		{http.MethodPost, "/api/git-repos/1/link-platform"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("retired endpoint %s %s expected 404, got %d: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}

	// 2. 验证 POST /api/git-repos/sync-all RBAC、SharedWriteEnabled 门禁、双模对齐及作业持久化
	{
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/sync-all", bytes.NewReader([]byte(`{"scope":"ALL","cache_policy":"CACHE_ONLY","sync":true}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("non-admin POST /sync-all expected 403, got %d", w.Code)
		}
	}
	{
		config.AppConfig.Storage.SharedWriteEnabled = false
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/sync-all", bytes.NewReader([]byte(`{"scope":"ALL","cache_policy":"FETCH_MISSING","sync":true}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		config.AppConfig.Storage.SharedWriteEnabled = true
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "SHARED_WRITE_DISABLED") {
			t.Fatalf("FETCH_MISSING with SharedWriteEnabled=false expected 400 SHARED_WRITE_DISABLED, got %d: %s", w.Code, w.Body.String())
		}
	}

	var syncJobID uint64
	{
		syncAllPayload, _ := json.Marshal(map[string]any{
			"scope":        "ALL",
			"cache_policy": "FETCH_MISSING",
			"sync":         true,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/sync-all", bytes.NewReader(syncAllPayload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("admin POST /sync-all expected 202, got %d: %s", w.Code, w.Body.String())
		}
		var jobResp models.ProtoSyncJob
		if err := json.Unmarshal(w.Body.Bytes(), &jobResp); err != nil {
			t.Fatalf("decode sync-all job failed: %v body=%s", err, w.Body.String())
		}
		if jobResp.ID == 0 || jobResp.TotalRepos != 2 || jobResp.HasProtoCount != 2 || jobResp.FailedCount != 0 {
			t.Fatalf("unexpected sync-all job stats: %+v", jobResp)
		}
		syncJobID = jobResp.ID
	}

	// 验证 GET /sync-summary & GET /sync-jobs/:jobId
	{
		req := httptest.NewRequest(http.MethodGet, "/api/git-repos/sync-summary", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "has_proto_count") || !strings.Contains(w.Body.String(), "trade-sub") {
			t.Fatalf("GET /sync-summary unexpected: %d %s", w.Code, w.Body.String())
		}

		jReq := httptest.NewRequest(http.MethodGet, "/api/git-repos/sync-jobs/"+strconv.FormatUint(syncJobID, 10), nil)
		jReq.Header.Set("Authorization", "Bearer "+viewerJWT)
		jw := httptest.NewRecorder()
		r.ServeHTTP(jw, jReq)
		if jw.Code != http.StatusOK || !strings.Contains(jw.Body.String(), models.SyncJobStatusCompleted) {
			t.Fatalf("GET /sync-jobs/:jobId unexpected: %d %s", jw.Code, jw.Body.String())
		}
	}

	var repo1, repo2 models.GitRepo
	if err := db.Where("platform_repo_id = ?", platRepo1.ID).First(&repo1).Error; err != nil {
		t.Fatalf("aligned repo1 not found: %v", err)
	}
	if err := db.Where("platform_repo_id = ?", platRepo2.ID).First(&repo2).Error; err != nil {
		t.Fatalf("aligned repo2 not found: %v", err)
	}
	importedRepoID1 := repo1.ID
	importedRepoID2 := repo2.ID
	if repo1.AutoActivate {
		t.Fatalf("expected AutoActivate=false by default on aligned platform repo")
	}
	var commonBundle1 models.GitProtoBundle
	if err := db.Where("git_repo_id = ? AND subdir_path = ?", repo1.ID, "pg").Order("id ASC").First(&commonBundle1).Error; err != nil {
		t.Fatalf("auto candidate bundle pg not created for repo1: %v", err)
	}
	ownerBundleID1 := commonBundle1.ID

	// 3. 验证批量同步 (?sync=true)、免网络重跑 re-normalize、基线报告与架构建议
	{
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/batch-sync", bytes.NewReader([]byte(`{"sync":true}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("viewer POST /batch-sync expected 403, got %d", w.Code)
		}
	}
	{
		body, _ := json.Marshal(map[string]any{
			"repo_ids": []uint64{importedRepoID1, importedRepoID2},
			"branch":   "main",
			"sync":     true,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/batch-sync", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("admin POST /batch-sync expected 202, got %d: %s", w.Code, w.Body.String())
		}
		var bsResp struct {
			Errors map[string]string `json:"errors"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &bsResp); err != nil || len(bsResp.Errors) > 0 {
			t.Fatalf("admin POST /batch-sync returned errors: err=%v body=%s", err, w.Body.String())
		}
	}
	{
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/"+strconv.FormatUint(importedRepoID1, 10)+"/re-normalize", bytes.NewReader([]byte(`{"force":true}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("POST /re-normalize expected 200, got %d: %s", w.Code, w.Body.String())
		}
	}
	{
		req := httptest.NewRequest(http.MethodGet, "/api/git-repos/baseline-report", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "normalized_files") {
			t.Fatalf("GET /baseline-report unexpected: %d %s", w.Code, w.Body.String())
		}
	}
	{
		req := httptest.NewRequest(http.MethodGet, "/api/git-repos/arch-suggestions", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /arch-suggestions expected 200, got %d: %s", w.Code, w.Body.String())
		}
	}

	// 4. 验证双视图 /api/proto-files/tree (subsystem & package) 与 /api/proto-files/:id/content 的 raw_content/pipeline_status
	var firstFileID uint64
	{
		for _, view := range []string{"subsystem", "package"} {
			req := httptest.NewRequest(http.MethodGet, "/api/proto-files/tree?view="+view, nil)
			req.Header.Set("Authorization", "Bearer "+viewerJWT)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("GET /proto-files/tree?view=%s expected 200, got %d: %s", view, w.Code, w.Body.String())
			}
			var parsed struct {
				View   string `json:"view"`
				Total  int    `json:"total"`
				Groups []struct {
					Files []struct {
						ID             uint64 `json:"id"`
						PipelineStatus string `json:"pipeline_status"`
					} `json:"files"`
				} `json:"groups"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil || parsed.Total < 2 || len(parsed.Groups) == 0 {
				t.Fatalf("unexpected tree response for view=%s: err=%v body=%s", view, err, w.Body.String())
			}
			if firstFileID == 0 && len(parsed.Groups[0].Files) > 0 {
				firstFileID = parsed.Groups[0].Files[0].ID
			}
		}

		badReq := httptest.NewRequest(http.MethodGet, "/api/proto-files/tree?view=invalid", nil)
		badReq.Header.Set("Authorization", "Bearer "+viewerJWT)
		bw := httptest.NewRecorder()
		r.ServeHTTP(bw, badReq)
		if bw.Code != http.StatusBadRequest {
			t.Fatalf("GET /proto-files/tree?view=invalid expected 400, got %d", bw.Code)
		}
	}
	{
		req := httptest.NewRequest(http.MethodGet, "/api/proto-files/"+strconv.FormatUint(firstFileID, 10)+"/content", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /proto-files/:id/content expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var contentResp struct {
			Content    string `json:"content"`
			RawContent string `json:"raw_content"`
			File       struct {
				PipelineStatus    string `json:"pipeline_status"`
				NormalizerVersion string `json:"normalizer_version"`
			} `json:"file"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &contentResp); err != nil {
			t.Fatalf("unmarshal content response failed: %v", err)
		}
		if contentResp.RawContent == "" || contentResp.Content == "" || contentResp.File.PipelineStatus != models.ProtoPipelineNormalized {
			t.Fatalf("expected content + raw_content + NORMALIZED status, got %+v", contentResp)
		}
	}

	// 5. 冲突发现、仲裁落定 MANUAL、导入归属表 CRUD 及 RBAC 验证
	conflictLogicalPath := "common/base.proto"
	{
		req := httptest.NewRequest(http.MethodGet, "/api/proto-conflicts", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "base.proto") {
			t.Fatalf("GET /proto-conflicts expected ambiguity/conflict for base.proto: %d %s", w.Code, w.Body.String())
		}
		var confResp struct {
			Items []struct {
				LogicalPath string `json:"logical_path"`
			} `json:"items"`
		}
		if json.Unmarshal(w.Body.Bytes(), &confResp) == nil && len(confResp.Items) > 0 && confResp.Items[0].LogicalPath != "" {
			conflictLogicalPath = confResp.Items[0].LogicalPath
		}
	}
	resolvePayload, _ := json.Marshal(map[string]any{
		"kind":              "IMPORT",
		"logical_path":      conflictLogicalPath,
		"owner_git_repo_id": importedRepoID1,
		"owner_bundle_id":   ownerBundleID1,
	})
	{
		req := httptest.NewRequest(http.MethodPost, "/api/proto-conflicts/resolve", bytes.NewReader(resolvePayload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("viewer POST /proto-conflicts/resolve expected 403, got %d", w.Code)
		}
	}
	{
		req := httptest.NewRequest(http.MethodPost, "/api/proto-conflicts/resolve", bytes.NewReader(resolvePayload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("admin POST /proto-conflicts/resolve expected 200, got %d: %s", w.Code, w.Body.String())
		}
	}
	var ownerID uint64
	{
		req := httptest.NewRequest(http.MethodGet, "/api/proto-import-owners?source=MANUAL", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /proto-import-owners expected 200, got %d", w.Code)
		}
		var listResp struct {
			Items []models.ProtoImportOwner `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil || len(listResp.Items) == 0 {
			t.Fatalf("expected >= 1 MANUAL import owner, got %s", w.Body.String())
		}
		ownerID = listResp.Items[0].ID

		delReq := httptest.NewRequest(http.MethodDelete, "/api/proto-import-owners/"+strconv.FormatUint(ownerID, 10), nil)
		delReq.Header.Set("Authorization", "Bearer "+adminJWT)
		dw := httptest.NewRecorder()
		r.ServeHTTP(dw, delReq)
		if dw.Code != http.StatusOK {
			t.Fatalf("DELETE /proto-import-owners/:id expected 200, got %d: %s", dw.Code, dw.Body.String())
		}
	}

	// 6. Git Push Webhook 常量时间验签与 SyncScheduler 触发
	var syncedRepo models.GitRepo
	if err := db.First(&syncedRepo, importedRepoID1).Error; err != nil {
		t.Fatalf("load importedRepoID1: %v", err)
	}
	const whSecret = "sync-webhook-secret-adr26"
	if err := syncedRepo.SetWebhookSecret(whSecret); err != nil {
		t.Fatalf("SetWebhookSecret: %v", err)
	}
	if err := db.Model(&syncedRepo).Update("webhook_secret_enc", syncedRepo.WebhookSecretEnc).Error; err != nil {
		t.Fatalf("save webhook secret: %v", err)
	}

	whURL := "/api/webhooks/git-sync/" + strconv.FormatUint(importedRepoID1, 10) + "?sync=true"
	{
		badReq := httptest.NewRequest(http.MethodPost, whURL, bytes.NewReader([]byte(`{"ref":"refs/heads/main"}`)))
		badReq.Header.Set("Content-Type", "application/json")
		badReq.Header.Set("X-Gitlab-Token", "wrong-secret")
		bw := httptest.NewRecorder()
		r.ServeHTTP(bw, badReq)
		if bw.Code != http.StatusUnauthorized {
			t.Fatalf("invalid webhook token expected 401, got %d", bw.Code)
		}
	}
	{
		okReq := httptest.NewRequest(http.MethodPost, whURL, bytes.NewReader([]byte(`{"ref":"refs/heads/main"}`)))
		okReq.Header.Set("Content-Type", "application/json")
		okReq.Header.Set("X-Gitlab-Token", whSecret)
		ow := httptest.NewRecorder()
		r.ServeHTTP(ow, okReq)
		if ow.Code != http.StatusAccepted {
			t.Fatalf("valid webhook expected 202, got %d: %s", ow.Code, ow.Body.String())
		}
	}
}

func TestProtoFilesTree_LazySummaryAndExpand(t *testing.T) {
	r, db := setupProtoDocRouterAndDB(t)
	handlers.InitGlobalSyncScheduler(nil)
	defer handlers.InitGlobalSyncScheduler(nil)

	oldCodes := config.AppConfig.Storage.CodesDir
	oldProtos := config.AppConfig.Storage.ProtosDir
	oldShared := config.AppConfig.Storage.SharedWriteEnabled
	config.AppConfig.Storage.CodesDir = filepath.Join(t.TempDir(), "codes")
	config.AppConfig.Storage.ProtosDir = filepath.Join(t.TempDir(), "protos")
	config.AppConfig.Storage.SharedWriteEnabled = true
	defer func() {
		config.AppConfig.Storage.CodesDir = oldCodes
		config.AppConfig.Storage.ProtosDir = oldProtos
		config.AppConfig.Storage.SharedWriteEnabled = oldShared
	}()

	adminUser := createTestUserInDB(t, db, "lazy_admin", config.AdminRole)
	viewerUser := createTestUserInDB(t, db, "lazy_viewer", "viewer")
	adminJWT := generateTestJWT(t, adminUser)
	viewerJWT := generateTestJWT(t, viewerUser)

	dep := commonModels.Department{Name: "懒加载测试部"}
	_ = db.Create(&dep).Error

	bare1 := initTestBareRepoForHandler(t, map[string]string{
		"pg/order/a.proto": "syntax = \"proto3\";\npackage acme.order.v1;\nmessage OrderA { string id = 1; }\n",
	})
	bare2 := initTestBareRepoForHandler(t, map[string]string{
		"pg/pay/b.proto": "syntax = \"proto3\";\npackage acme.pay.v1;\nmessage PayB { int64 amt = 1; }\n",
	})
	bareExcluded := initTestBareRepoForHandler(t, map[string]string{
		"pg/hidden/c.proto": "syntax = \"proto3\";\npackage acme.hidden.v1;\nmessage HiddenC { string id = 1; }\n",
	})

	p1 := commonModels.Repository{DepartmentID: dep.ID, Name: "lazy-order", URL: bare1, OwnerID: adminUser.ID, Branch: "main", ServiceGroup: "trade", IsActive: true}
	p2 := commonModels.Repository{DepartmentID: dep.ID, Name: "lazy-pay", URL: bare2, OwnerID: adminUser.ID, Branch: "main", ServiceGroup: "finance", IsActive: true}
	p3 := commonModels.Repository{DepartmentID: dep.ID, Name: "lazy-hidden", URL: bareExcluded, OwnerID: adminUser.ID, Branch: "main", ServiceGroup: "hidden-sub", IsActive: true}
	_ = db.Create(&p1).Error
	_ = db.Create(&p2).Error
	_ = db.Create(&p3).Error

	// Sync all
	syncReq := httptest.NewRequest(http.MethodPost, "/api/git-repos/sync-all", bytes.NewReader([]byte(`{"scope":"ALL","cache_policy":"FETCH_MISSING","sync":true}`)))
	syncReq.Header.Set("Content-Type", "application/json")
	syncReq.Header.Set("Authorization", "Bearer "+adminJWT)
	sw := httptest.NewRecorder()
	r.ServeHTTP(sw, syncReq)
	if sw.Code != http.StatusAccepted {
		t.Fatalf("sync-all failed: %d %s", sw.Code, sw.Body.String())
	}

	var r1, r3 models.GitRepo
	_ = db.Where("platform_repo_id = ?", p1.ID).First(&r1).Error
	_ = db.Where("platform_repo_id = ?", p3.ID).First(&r3).Error

	// Disable p3 via governance -> EXCLUDED
	govReq := httptest.NewRequest(http.MethodPut, "/api/git-repos/"+strconv.FormatUint(r3.ID, 10)+"/governance", bytes.NewReader([]byte(`{"sync_enabled":false}`)))
	govReq.Header.Set("Content-Type", "application/json")
	govReq.Header.Set("Authorization", "Bearer "+adminJWT)
	gw := httptest.NewRecorder()
	r.ServeHTTP(gw, govReq)
	if gw.Code != http.StatusOK {
		t.Fatalf("disable repo failed: %d %s", gw.Code, gw.Body.String())
	}

	// 1. summary_only=true returns counts without hydrating files[], and filters out EXCLUDED repo
	{
		req := httptest.NewRequest(http.MethodGet, "/api/proto-files/tree?view=subsystem&summary_only=true", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("summary tree failed: %d %s", w.Code, w.Body.String())
		}
		var resp struct {
			SummaryOnly bool `json:"summary_only"`
			Total       int  `json:"total"`
			Groups      []struct {
				GitRepoID       uint64 `json:"git_repo_id"`
				GitRepoName     string `json:"git_repo_name"`
				TotalCount      int    `json:"total_count"`
				NormalizedCount int    `json:"normalized_count"`
				Files           []any  `json:"files"`
			} `json:"groups"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode summary tree: %v", err)
		}
		if !resp.SummaryOnly || resp.Total != 2 || len(resp.Groups) != 2 {
			t.Fatalf("expected 2 enabled repos in summary tree, got %+v", resp)
		}
		for _, g := range resp.Groups {
			if len(g.Files) != 0 || g.TotalCount != 1 || g.NormalizedCount != 1 {
				t.Fatalf("expected unexpanded group to have 0 files and total_count=1, got %+v", g)
			}
		}
	}

	// 2. summary_only=true&expand_repo_id=<r1.ID> hydrates ONLY r1
	{
		req := httptest.NewRequest(http.MethodGet, "/api/proto-files/tree?view=subsystem&summary_only=true&expand_repo_id="+strconv.FormatUint(r1.ID, 10), nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expand_repo_id failed: %d %s", w.Code, w.Body.String())
		}
		var resp struct {
			Groups []struct {
				GitRepoID uint64 `json:"git_repo_id"`
				Files     []any  `json:"files"`
			} `json:"groups"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		for _, g := range resp.Groups {
			if g.GitRepoID == r1.ID && len(g.Files) != 1 {
				t.Fatalf("expected expanded repo %d to have 1 file", r1.ID)
			}
			if g.GitRepoID != r1.ID && len(g.Files) != 0 {
				t.Fatalf("expected collapsed repo %d to have 0 files", g.GitRepoID)
			}
		}
	}

	// 3. view=package&summary_only=true&expand_package=acme.pay.v1
	{
		req := httptest.NewRequest(http.MethodGet, "/api/proto-files/tree?view=package&summary_only=true&expand_package=acme.pay.v1", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expand_package failed: %d %s", w.Code, w.Body.String())
		}
		var resp struct {
			Groups []struct {
				PackageName string `json:"package_name"`
				TotalCount  int    `json:"total_count"`
				Files       []any  `json:"files"`
			} `json:"groups"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		for _, g := range resp.Groups {
			if g.PackageName == "acme.pay.v1" && len(g.Files) != 1 {
				t.Fatalf("expected expanded package acme.pay.v1 to have 1 file")
			}
			if g.PackageName != "acme.pay.v1" && len(g.Files) != 0 {
				t.Fatalf("expected collapsed package %s to have 0 files", g.PackageName)
			}
		}
	}
}
