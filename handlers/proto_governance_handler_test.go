package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"
	"code-contract/config"
	"code-contract/handlers"
	"code-contract/internal/server/gitclient"
	"code-contract/models"
)

func TestProtoExportGovernance_API_Collisions_DryRun_ApplyAndAudit(t *testing.T) {
	t.Setenv("CONTRACT_CRYPTO_KEY", "test-contract-crypto-key-m38-32bytes!")
	r, db := setupProtoDocRouterAndDB(t)
	handlers.InitGlobalSyncScheduler(nil)
	defer handlers.InitGlobalSyncScheduler(nil)

	var auditLogs []commonModels.SysAuditLog
	_ = commonAudit.Close(context.Background())
	commonAudit.Init(db, commonAudit.InitOptions{
		QueueCap:    1000,
		BatchSize:   1,
		FlushPeriod: 10 * time.Millisecond,
	})
	defer func() { _ = commonAudit.Close(context.Background()) }()

	oldCodes := config.AppConfig.Storage.CodesDir
	oldProtos := config.AppConfig.Storage.ProtosDir
	oldShared := config.AppConfig.Storage.SharedWriteEnabled
	codesDir := filepath.Join(t.TempDir(), "codes")
	protosDir := filepath.Join(t.TempDir(), "protos")
	config.AppConfig.Storage.CodesDir = codesDir
	config.AppConfig.Storage.ProtosDir = protosDir
	config.AppConfig.Storage.SharedWriteEnabled = true
	defer func() {
		config.AppConfig.Storage.CodesDir = oldCodes
		config.AppConfig.Storage.ProtosDir = oldProtos
		config.AppConfig.Storage.SharedWriteEnabled = oldShared
	}()

	adminUser := createTestUserInDB(t, db, "gov_admin", config.AdminRole)
	viewerUser := createTestUserInDB(t, db, "gov_viewer", "viewer")
	adminJWT := generateTestJWT(t, adminUser)
	viewerJWT := generateTestJWT(t, viewerUser)

	dep := commonModels.Department{Name: "治理中心"}
	if err := db.Create(&dep).Error; err != nil {
		t.Fatalf("create department: %v", err)
	}

	bareAlpha := initTestBareRepoForHandler(t, map[string]string{
		// Deliberately missing syntax header + Windows CRLF + bad package hyphen to exercise Stage B rules
		"pg/legacy/alpha.proto": "#include \"legacy.h\"\r\npackage alpha-core/v1;\r\nmessage AlphaReq { string id = 1; }\r\n",
	})
	bareBeta := initTestBareRepoForHandler(t, map[string]string{
		"pg/beta.proto":     "syntax = \"proto3\";\npackage beta.v1;\nmessage BetaRsp { bool ok = 1; }\n",
		"pg/beta_svc.proto": "package beta.v1;\nimport \"pg/beta.proto\";\nuService BetaService {\n  interface Check(BetaRsp) return (BetaRsp) (timeout:1000);\n}\n",
	})

	platAlpha := commonModels.Repository{
		DepartmentID: dep.ID,
		Name:         "repo-alpha",
		URL:          bareAlpha,
		HTTPURL:      bareAlpha,
		OwnerID:      adminUser.ID,
		Branch:       "main",
		ServiceGroup: "legacy-dept",
		IsActive:     true,
	}
	platBeta := commonModels.Repository{
		DepartmentID: dep.ID,
		Name:         "repo-beta",
		URL:          bareBeta,
		HTTPURL:      bareBeta,
		OwnerID:      adminUser.ID,
		Branch:       "main",
		ServiceGroup: "pay-dept",
		IsActive:     true,
	}
	if err := db.Create(&platAlpha).Error; err != nil {
		t.Fatalf("create platAlpha: %v", err)
	}
	if err := db.Create(&platBeta).Error; err != nil {
		t.Fatalf("create platBeta: %v", err)
	}

	// Sync both repos first
	{
		body := []byte(`{"scope":"ALL","cache_policy":"FETCH_MISSING","sync":true}`)
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/sync-all", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("sync-all expected 202, got %d: %s", w.Code, w.Body.String())
		}
	}

	var repoAlpha, repoBeta models.GitRepo
	_ = db.Where("platform_repo_id = ?", platAlpha.ID).First(&repoAlpha).Error
	_ = db.Where("platform_repo_id = ?", platBeta.ID).First(&repoBeta).Error

	// 1. GET /api/proto-governance/config (Auth allowed)
	{
		req := httptest.NewRequest(http.MethodGet, "/api/proto-governance/config", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "NORM-005") {
			t.Fatalf("GET /proto-governance/config expected 200 with NORM-005, got %d: %s", w.Code, w.Body.String())
		}
	}

	// 2. POST /api/git-repos/:id/governance/dry-run (Auth allowed, zero side effect)
	{
		drPayload := []byte(`{
			"export_target_subsystem": "trade",
			"export_target_repo_dir": "alpha-clean",
			"strip_prefixes": ["pg/legacy/"]
		}`)
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/"+strconv.FormatUint(repoAlpha.ID, 10)+"/governance/dry-run", bytes.NewReader(drPayload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("POST /governance/dry-run expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var drRes gitclient.RepoGovernanceDryRunResult
		if err := json.Unmarshal(w.Body.Bytes(), &drRes); err != nil || drRes.AfterTargetDir != "clean/trade/alpha-clean" {
			t.Fatalf("unexpected dry-run result: err=%v body=%s", err, w.Body.String())
		}
		if len(drRes.Files) != 1 || drRes.Files[0].CleanPath != "alpha.proto" || !drRes.Files[0].CleanSyntaxOK {
			t.Fatalf("expected mapped clean path alpha.proto passing syntax check, got %+v", drRes.Files)
		}
		// Ensure dry-run did NOT mutate DB
		var checkAlpha models.GitRepo
		_ = db.First(&checkAlpha, repoAlpha.ID).Error
		if checkAlpha.ExportTargetSubsystem != "" {
			t.Fatalf("dry-run must not persist export_target_subsystem")
		}
	}

	// 3. PUT /api/git-repos/:id/governance (RBAC + valid update + zero-copy clean/ relocation)
	govPayload := []byte(`{
		"export_target_subsystem": "trade",
		"export_target_repo_dir": "alpha-clean",
		"strip_prefixes": ["pg/legacy/"]
	}`)
	{
		req := httptest.NewRequest(http.MethodPut, "/api/git-repos/"+strconv.FormatUint(repoAlpha.ID, 10)+"/governance", bytes.NewReader(govPayload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("viewer PUT /governance expected 403, got %d", w.Code)
		}
	}
	{
		badRegex := []byte(`{"regex_rewrites":[{"id":"bad","pattern":"[unclosed"}]}`)
		req := httptest.NewRequest(http.MethodPut, "/api/git-repos/"+strconv.FormatUint(repoAlpha.ID, 10)+"/governance", bytes.NewReader(badRegex))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid regex expected 400, got %d: %s", w.Code, w.Body.String())
		}
	}
	{
		req := httptest.NewRequest(http.MethodPut, "/api/git-repos/"+strconv.FormatUint(repoAlpha.ID, 10)+"/governance", bytes.NewReader(govPayload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("admin PUT /governance expected 200, got %d: %s", w.Code, w.Body.String())
		}
		// Relocated symlink must exist under clean/trade/alpha-clean; raw/ stays under raw/legacy-dept/repo-alpha
		if _, err := os.Stat(filepath.Join(protosDir, "clean", "trade", "alpha-clean")); err != nil {
			t.Fatalf("expected relocated clean symlink: %v", err)
		}
		if _, err := os.Stat(filepath.Join(protosDir, "raw", "legacy-dept", "repo-alpha", "pg", "legacy", "alpha.proto")); err != nil {
			t.Fatalf("expected raw path untouched: %v", err)
		}
	}

	// 4. Colliding repo-beta to trade/alpha-clean must return 409 Conflict
	{
		colPayload := []byte(`{"export_target_subsystem":"trade","export_target_repo_dir":"alpha-clean"}`)
		req := httptest.NewRequest(http.MethodPut, "/api/git-repos/"+strconv.FormatUint(repoBeta.ID, 10)+"/governance", bytes.NewReader(colPayload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "EXPORT_TARGET_CONFLICT") {
			t.Fatalf("expected 409 EXPORT_TARGET_CONFLICT, got %d: %s", w.Code, w.Body.String())
		}
	}

	// 5. Global proto-governance Dry-Run, PUT config, and apply-and-renormalize
	globalDoc := gitclient.DefaultGovernanceConfig()
	globalDoc.Global.DefaultSyntax = "proto3"
	globalDoc.Global.RegexRewrites = []gitclient.RegexRewriteRule{
		{
			ID:          "rename-legacy-msg",
			Pattern:     `AlphaReq`,
			Replacement: "AlphaCleanReq",
		},
	}
	globalDoc.Repos[repoAlpha.Name] = gitclient.RepoGovernanceSpec{
		PlatformRepoID:        repoAlpha.PlatformRepoID,
		ExportTargetSubsystem: "trade",
		ExportTargetRepoDir:   "alpha-clean",
		StripPrefixes:         []string{"pg/legacy/"},
	}
	docBytes, _ := json.Marshal(globalDoc)

	{
		req := httptest.NewRequest(http.MethodPost, "/api/proto-governance/dry-run", bytes.NewReader(docBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"schema_valid":true`) {
			t.Fatalf("POST /proto-governance/dry-run expected 200 valid, got %d: %s", w.Code, w.Body.String())
		}
	}
	{
		req := httptest.NewRequest(http.MethodPut, "/api/proto-governance/config", bytes.NewReader(docBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"revision":1`) {
			t.Fatalf("PUT /proto-governance/config expected 200 rev=1, got %d: %s", w.Code, w.Body.String())
		}
	}
	{
		applyPayload, _ := json.Marshal(map[string]any{
			"repo_ids":        []uint64{repoAlpha.ID},
			"force_recompile": true,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/proto-governance/apply-and-renormalize", bytes.NewReader(applyPayload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"succeeded_count":1`) {
			t.Fatalf("POST /apply-and-renormalize expected 200 succeeded=1, got %d: %s", w.Code, w.Body.String())
		}
		// Check re-normalized file content uses proto2 & mapped path clean/trade/alpha-clean/alpha.proto
		cleanData, rErr := os.ReadFile(filepath.Join(protosDir, "clean", "trade", "alpha-clean", "alpha.proto"))
		if rErr != nil {
			t.Fatalf("read relocated mapped alpha.proto: %v", rErr)
		}
		if !strings.Contains(string(cleanData), `syntax = "proto3";`) || !strings.Contains(string(cleanData), "AlphaCleanReq") {
			t.Fatalf("expected updated global regex_rewrites & proto3 in clean alpha.proto, got:\n%s", string(cleanData))
		}
	}

	// Verify raw/ keeps pg/ while clean/ strips /pg by default for beta
	if _, err := os.Stat(filepath.Join(protosDir, "raw", "pay-dept", "repo-beta", "pg", "beta_svc.proto")); err != nil {
		t.Fatalf("expected raw/beta_svc.proto with pg/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(protosDir, "clean", "pay-dept", "repo-beta", "beta_svc.proto")); err != nil {
		t.Fatalf("expected clean/beta_svc.proto flattened without /pg: %v", err)
	}
	if _, err := os.Stat(filepath.Join(protosDir, "clean", "pay-dept", "repo-beta", "pg", "beta_svc.proto")); !os.IsNotExist(err) {
		t.Fatalf("clean/ must not contain standalone pg/ dir when strip_pg_segment=true")
	}

	// Verify GET /api/proto-files?is_uservice_source=true returns beta_svc.proto with clean_rel_path
	{
		req := httptest.NewRequest(http.MethodGet, "/api/proto-files?is_uservice_source=true", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"clean_rel_path":"beta_svc.proto"`) || !strings.Contains(w.Body.String(), `"is_uservice_source":true`) {
			t.Fatalf("GET /proto-files?is_uservice_source=true unexpected: %d %s", w.Code, w.Body.String())
		}
	}

	// Verify P1 audit logs persisted
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = db.Where("action LIKE ?", "contract_git:%governance%").Find(&auditLogs).Error
		if len(auditLogs) >= 3 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(auditLogs) < 3 {
		t.Fatalf("expected >= 3 governance audit logs, got %d", len(auditLogs))
	}
}
