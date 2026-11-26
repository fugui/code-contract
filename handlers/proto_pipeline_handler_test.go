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

	// 1. 发现平台代码仓（已登录用户可读）
	{
		req := httptest.NewRequest(http.MethodGet, "/api/git-repos/platform-discover?probe_protos=false", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/git-repos/platform-discover expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "order-core-svc") {
			t.Fatalf("expected order-core-svc in discover list: %s", w.Body.String())
		}
	}

	// 2. 批量导入平台仓（RBAC: 普通用户 403，管理员 201，且默认 auto_activate=false）
	importPayload, _ := json.Marshal(map[string]any{
		"items": []map[string]any{
			{
				"platform_repo_id": platRepo1.ID,
				"sync_strategy":    "FETCH_AND_READ",
				"bundles": []map[string]any{
					{
						"bundle_name":        "common-pg",
						"target_schema_name": "adr26-common-schema",
						"category":           "BASE_LIB",
						"subdir_path":        "pg/common",
					},
					{
						"bundle_name":        "order-pg",
						"target_schema_name": "adr26-order-schema",
						"category":           "BUSINESS",
						"subdir_path":        "pg/order",
					},
				},
			},
			{
				"platform_repo_id": platRepo2.ID,
				"sync_strategy":    "FETCH_AND_READ",
				"bundles": []map[string]any{
					{
						"bundle_name":        "legacy-common-pg",
						"target_schema_name": "adr26-legacy-common-schema",
						"category":           "BASE_LIB",
						"subdir_path":        "pg/common",
					},
				},
			},
		},
	})
	{
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/platform-import", bytes.NewReader(importPayload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("non-admin POST /platform-import expected 403, got %d", w.Code)
		}
	}

	var importedRepoID1, importedRepoID2 uint64
	var ownerBundleID1 uint64
	{
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/platform-import", bytes.NewReader(importPayload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("admin POST /platform-import expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			ImportedCount int                         `json:"imported_count"`
			Items         []models.GitRepoRedactedDTO `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.ImportedCount != 2 {
			t.Fatalf("unexpected platform-import response: err=%v body=%s", err, w.Body.String())
		}
		importedRepoID1 = resp.Items[0].ID
		importedRepoID2 = resp.Items[1].ID
		ownerBundleID1 = resp.Items[0].Bundles[0].ID
		if resp.Items[0].AutoActivate {
			t.Fatalf("expected AutoActivate=false by default on platform-import")
		}
	}

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
	{
		req := httptest.NewRequest(http.MethodGet, "/api/proto-conflicts", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "base.proto") {
			t.Fatalf("GET /proto-conflicts expected ambiguity/conflict for base.proto: %d %s", w.Code, w.Body.String())
		}
	}
	resolvePayload, _ := json.Marshal(map[string]any{
		"kind":              "IMPORT",
		"logical_path":      "base.proto",
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

	// 6. 存量孤儿仓一键关联平台仓 (/api/git-repos/:id/link-platform)
	platLinkRepo := commonModels.Repository{
		DepartmentID: dep.ID,
		Name:         "legacy-orphan-plat",
		URL:          "https://git.internal.local/team/legacy-orphan.git",
		OwnerID:      adminUser.ID,
		Branch:       "main",
		ServiceGroup: "pay-sub",
		IsActive:     true,
	}
	if err := db.Create(&platLinkRepo).Error; err != nil {
		t.Fatalf("create platLinkRepo: %v", err)
	}
	legacyRepo := models.GitRepo{
		Name:          "legacy-orphan-git",
		Provider:      models.GitProviderGitLab,
		RepoURL:       "https://git.internal.local/team/legacy-orphan.git",
		DefaultBranch: "main",
		AuthType:      models.GitAuthToken,
		SourceMode:    models.GitSourceModeExternalStandalone,
	}
	_ = legacyRepo.SetCredentials("glpat-legacy-secret")
	if err := db.Create(&legacyRepo).Error; err != nil {
		t.Fatalf("create legacyRepo: %v", err)
	}
	{
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/"+strconv.FormatUint(legacyRepo.ID, 10)+"/link-platform", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("POST /link-platform expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var refreshed models.GitRepo
		if err := db.First(&refreshed, legacyRepo.ID).Error; err != nil {
			t.Fatalf("reload legacyRepo: %v", err)
		}
		if refreshed.SourceMode != models.GitSourceModePlatformShared || refreshed.CredentialsEnc != "" {
			t.Fatalf("expected PLATFORM_SHARED and cleared credentials_enc, got mode=%s encLen=%d",
				refreshed.SourceMode, len(refreshed.CredentialsEnc))
		}
	}

	// 7. Git Push Webhook 常量时间验签与 SyncScheduler 触发
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
