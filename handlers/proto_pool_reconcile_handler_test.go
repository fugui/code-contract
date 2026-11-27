package handlers_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"
	"code-contract/config"
	"code-contract/handlers"
	"code-contract/models"
)

func testHandlerSHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestPoolReconcileAPI_RBAC_SSE_AndPromoteAutoFix(t *testing.T) {
	t.Setenv("CONTRACT_CRYPTO_KEY", "test-contract-crypto-key-m42-32bytes!")
	r, db := setupProtoDocRouterAndDB(t)

	_ = commonAudit.Close(context.Background())
	commonAudit.Init(db, commonAudit.InitOptions{
		QueueCap:    1000,
		BatchSize:   1,
		FlushPeriod: 10 * time.Millisecond,
	})
	defer func() { _ = commonAudit.Close(context.Background()) }()

	oldCfg := config.AppConfig
	tmpRoot := t.TempDir()
	config.AppConfig.Storage.CodesDir = filepath.Join(tmpRoot, "codes")
	config.AppConfig.Storage.ProtosDir = filepath.Join(tmpRoot, "protos")
	defer func() {
		config.AppConfig = oldCfg
	}()

	normalUser := createTestUserInDB(t, db, "pool_api_user", "member")
	adminUser := createTestUserInDB(t, db, "pool_api_admin", config.AdminRole)
	normalJWT := generateTestJWT(t, normalUser)
	adminJWT := generateTestJWT(t, adminUser)

	// 1. Seed repositories, bundles and proto files for fix(a) & variant_swap
	repo1 := models.GitRepo{
		Name:             "app-reconcile-api",
		PlatformRepoID:   801,
		SourceMode:       models.GitSourceModePlatformShared,
		SyncStrategy:     models.GitSyncStrategyLocalCacheOnly,
		Provider:         models.GitProviderGitLab,
		RepoURL:          "http://mock/app-reconcile-api.git",
		DefaultBranch:    "main",
		SubsystemSlug:    "app",
		PathRewritesJSON: []byte(`{"pg/DemoApiSvc.proto":"app/DemoApiSvc.proto","pg/HelperMsg.proto":"app/HelperMsg.proto"}`),
		ProtoStatus:      models.GitRepoProtoStatusHasProto,
		SyncEnabled:      true,
	}
	if err := db.Create(&repo1).Error; err != nil {
		t.Fatal(err)
	}
	bundle1 := models.GitProtoBundle{
		GitRepoID:        repo1.ID,
		BundleName:       "app-main",
		Category:         models.ProtoCategoryBusiness,
		TargetSchemaName: "app_main",
		SubdirPath:       "pg",
		AutoCompile:      true,
	}
	if err := db.Create(&bundle1).Error; err != nil {
		t.Fatal(err)
	}

	repo2 := models.GitRepo{
		Name:           "alt-variant-repo",
		PlatformRepoID: 802,
		SourceMode:     models.GitSourceModePlatformShared,
		SyncStrategy:   models.GitSyncStrategyLocalCacheOnly,
		Provider:       models.GitProviderGitLab,
		RepoURL:        "http://mock/alt-variant.git",
		DefaultBranch:  "main",
		SubsystemSlug:  "shared",
		ProtoStatus:    models.GitRepoProtoStatusHasProto,
		SyncEnabled:    true,
	}
	if err := db.Create(&repo2).Error; err != nil {
		t.Fatal(err)
	}
	bundle2 := models.GitProtoBundle{
		GitRepoID:        repo2.ID,
		BundleName:       "shared-alt",
		Category:         models.ProtoCategoryBaseLib,
		TargetSchemaName: "shared_alt",
		SubdirPath:       "shared/pg",
		AutoCompile:      true,
	}
	if err := db.Create(&bundle2).Error; err != nil {
		t.Fatal(err)
	}

	svcSrc := `syntax = "proto2";
package app.api;
import "wrong/dir/HelperMsg.proto";
service DemoApiSvc {
  rpc Call(app.api.HelperMsg) returns (app.api.HelperMsg);
}
`
	helperSrc := `syntax = "proto2";
package app.api;
message HelperMsg { optional string ok = 1; }
`
	seedFile := func(repoID, bundleID uint64, schema, filePath, repoPath, cleanPath, pkg, content, imps, syms string, isUSvc bool) uint64 {
		pf := models.ProtoFile{
			GitRepoID:         repoID,
			BundleID:          bundleID,
			TargetSchemaName:  schema,
			Category:          models.ProtoCategoryBusiness,
			FilePath:          filePath,
			RepoFilePath:      repoPath,
			CleanRelPath:      cleanPath,
			PackageName:       pkg,
			NormalizerVersion: "rule-normalizer/v2",
			PipelineStatus:    models.ProtoPipelineNormalized,
			IsUServiceSource:  isUSvc,
			ImportsJSON:       []byte(imps),
			SymbolsJSON:       []byte(syms),
			ContentSHA256:     testHandlerSHA256Hex(content),
			LineCount:         4,
			LatestVersionNum:  1,
			LatestCommitSHA:   "c1",
		}
		if err := db.Create(&pf).Error; err != nil {
			t.Fatal(err)
		}
		ver := models.ProtoFileVersion{
			ProtoFileID:    pf.ID,
			Version:        1,
			VersionTag:     "v1",
			SourceType:     models.ProtoSourceGitSync,
			CommitSHA:      "c1",
			ContentText:    content,
			RawContentText: content,
			ContentSHA256:  pf.ContentSHA256,
			LineCount:      4,
			CommittedAt:    time.Now().UTC(),
		}
		if err := db.Create(&ver).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&pf).Update("latest_version_id", ver.ID).Error; err != nil {
			t.Fatal(err)
		}
		return pf.ID
	}

	_ = seedFile(repo1.ID, bundle1.ID, bundle1.TargetSchemaName,
		"app/DemoApiSvc.proto", "pg/DemoApiSvc.proto", "app/DemoApiSvc.proto", "app.api",
		svcSrc, `["wrong/dir/HelperMsg.proto"]`, `[{"kind":"SERVICE","full_name":"app.api.DemoApiSvc"}]`, true)
	_ = seedFile(repo1.ID, bundle1.ID, bundle1.TargetSchemaName,
		"app/HelperMsg.proto", "pg/HelperMsg.proto", "app/HelperMsg.proto", "app.api",
		helperSrc, `[]`, `[{"kind":"MESSAGE","full_name":"app.api.HelperMsg"}]`, false)

	// 2. Non-admin RBAC -> 403
	{
		req := httptest.NewRequest(http.MethodPost, "/api/proto-governance/reconcile-pool", strings.NewReader(`{"sync":true}`))
		req.Header.Set("Authorization", "Bearer "+normalJWT)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for non-admin reconcile-pool, got %d %s", w.Code, w.Body.String())
		}
	}
	{
		req := httptest.NewRequest(http.MethodPost, "/api/proto-governance/auto-fixes/promote", strings.NewReader(`{"fixes":[{"file_path":"a.proto","fix_kind":"fix(a)","old_import":"x","new_import":"y"}]}`))
		req.Header.Set("Authorization", "Bearer "+normalJWT)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for non-admin promote, got %d %s", w.Code, w.Body.String())
		}
	}

	// 3. Before any run exists, /reconcile-runs/latest returns {"run": null, "report": null}
	{
		req := httptest.NewRequest(http.MethodGet, "/api/proto-governance/reconcile-runs/latest", nil)
		req.Header.Set("Authorization", "Bearer "+normalJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"run":null`) {
			t.Fatalf("expected empty latest run, got %d %s", w.Code, w.Body.String())
		}
	}

	// 4. Subscribe SSE & Admin POST /reconcile-pool (sync=true)
	subID, sseCh := handlers.GetGlobalSSEBroker().Subscribe()
	defer handlers.GetGlobalSSEBroker().Unsubscribe(subID)

	var runID uint64
	{
		req := httptest.NewRequest(http.MethodPost, "/api/proto-governance/reconcile-pool", strings.NewReader(`{"sync":true,"closure_only":true,"enable_r7_auto_fix":true}`))
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 on sync reconcile-pool, got %d %s", w.Code, w.Body.String())
		}
		var resp struct {
			Run    models.ProtoPoolReconcileRun `json:"run"`
			Report struct {
				Stats struct {
					CompileErrorCount int `json:"compile_error_count"`
					AutoFixCount      int `json:"auto_fix_count"`
					ClosureFileCount  int `json:"closure_file_count"`
				} `json:"stats"`
				AutoFixes []struct {
					FixKind   string `json:"fix_kind"`
					OldImport string `json:"old_import"`
					NewImport string `json:"new_import"`
				} `json:"auto_fixes"`
			} `json:"report"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Run.ID == 0 || resp.Report.Stats.CompileErrorCount != 0 || resp.Report.Stats.AutoFixCount < 1 {
			t.Fatalf("unexpected sync reconcile response: %+v", resp)
		}
		runID = resp.Run.ID
		// Verify workspace symlink + .clean-report.json exported!
		wsReport := filepath.Join(config.AppConfig.Storage.ProtosDir, "workspace", ".clean-report.json")
		if _, err := os.Stat(wsReport); err != nil {
			t.Fatalf("expected workspace .clean-report.json at %s: %v", wsReport, err)
		}
	}

	// Verify SSE received proto_pool_reconcile.progress
	sawSSE := false
	for i := 0; i < 4; i++ {
		select {
		case evt := <-sseCh:
			if evt.Event == "proto_pool_reconcile.progress" {
				sawSSE = true
			}
		default:
		}
	}
	if !sawSSE {
		t.Fatalf("expected SSE event proto_pool_reconcile.progress")
	}

	// 5. GET /reconcile-runs/latest & /:id & 404
	{
		req := httptest.NewRequest(http.MethodGet, "/api/proto-governance/reconcile-runs/latest", nil)
		req.Header.Set("Authorization", "Bearer "+normalJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"auto_fixes"`) {
			t.Fatalf("GET /latest unexpected: %d %s", w.Code, w.Body.String())
		}

		req2 := httptest.NewRequest(http.MethodGet, "/api/proto-governance/reconcile-runs/999999", nil)
		req2.Header.Set("Authorization", "Bearer "+normalJWT)
		w2 := httptest.NewRecorder()
		r.ServeHTTP(w2, req2)
		if w2.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for unknown run id, got %d", w2.Code)
		}
	}

	// 6. POST /api/proto-governance/auto-fixes/promote (fix(a) + variant_swap) + audit verify
	promoteBody, _ := json.Marshal(map[string]any{
		"run_id": runID,
		"fixes": []map[string]any{
			{
				"file_path":  "app/DemoApiSvc.proto",
				"fix_kind":   "fix(a)",
				"old_import": "wrong/dir/HelperMsg.proto",
				"new_import": "app/HelperMsg.proto",
			},
			{
				"file_path":  "shared/AltCommon.proto",
				"fix_kind":   "variant_swap",
				"new_import": "alt-variant-repo",
				"repo_id":    repo2.ID,
				"bundle_id":  bundle2.ID,
			},
		},
	})
	{
		req := httptest.NewRequest(http.MethodPost, "/api/proto-governance/auto-fixes/promote", bytes.NewReader(promoteBody))
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 on promote auto-fixes, got %d %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"promoted_import_rewrites":1`) || !strings.Contains(w.Body.String(), `"promoted_manual_owners":1`) {
			t.Fatalf("unexpected promote counts: %s", w.Body.String())
		}
	}

	// Verify persisted governance config & manual import owner & P1 audit log
	var owner models.ProtoImportOwner
	if err := db.Where("logical_path = ?", "shared/AltCommon.proto").First(&owner).Error; err != nil {
		t.Fatalf("expected ProtoImportOwner created by promote: %v", err)
	}
	if owner.Source != models.ImportOwnerSourceManual || owner.OwnerBundleID != bundle2.ID {
		t.Fatalf("unexpected owner record: %+v", owner)
	}

	var auditCnt int64
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = db.Model(&commonModels.SysAuditLog{}).Where("action = ?", "contract_git:promote_auto_fixes").Count(&auditCnt).Error
		if auditCnt >= 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if auditCnt < 1 {
		t.Fatalf("expected P1 audit log contract_git:promote_auto_fixes")
	}

	// 7. Verify GET /api/proto-files?in_service_closure=true&is_uservice_source=true filters
	{
		req := httptest.NewRequest(http.MethodGet, "/api/proto-files?in_service_closure=true&is_uservice_source=true", nil)
		req.Header.Set("Authorization", "Bearer "+normalJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "DemoApiSvc.proto") || strings.Contains(w.Body.String(), "HelperMsg.proto") {
			t.Fatalf("GET /proto-files closure+uservice filter unexpected: %d %s", w.Code, w.Body.String())
		}
	}
}
