package gitclient

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code-contract/config"
	"code-contract/models"

	"gorm.io/datatypes"
)

func TestRuleBasedNormalizer_All5Rules_Overrides_LineMap_AndIdempotency(t *testing.T) {
	ctx := context.Background()
	defCfg := DefaultGovernanceConfig()
	defCfg.Global.ImportRewrites = map[string]string{
		"legacy/common_types.proto": "common/v1/types.proto",
	}
	defCfg.Global.RegexRewrites = []RegexRewriteRule{
		{
			ID:          "strip-custom-opt",
			Pattern:     `^\s*option\s+\(dophi_legacy\)\s*=.*;\s*$`,
			Replacement: "",
		},
	}

	repo := models.GitRepo{
		ID:             101,
		Name:           "legacy-order-repo",
		PlatformRepoID: 1001,
		SourceMode:     models.GitSourceModePlatformShared,
		RepoNormalizeConfigJSON: datatypes.JSON([]byte(`{
			"regex_rewrites": [
				{"id": "fix-deprecated-opt", "pattern": "\\[deprecated_dophi=true\\]", "replacement": "[deprecated = true]"}
			],
			"import_rewrites": {
				"order_local.proto": "order/v1/local.proto"
			},
			"exempt_files": ["pg/exempt/raw_legacy.proto"],
			"quarantined_files": ["pg/broken/quarantine_me.proto"]
		}`)),
	}

	norm := NewRuleBasedNormalizer(&defCfg)
	if norm.Version() != "rule-normalizer/v2" {
		t.Fatalf("unexpected normalizer version: %s", norm.Version())
	}

	rawProto := "\xEF\xBB\xBF#include \"dophi_macros.h\"\r\n" +
		"DOPHI_DECLARE_MESSAGE(CreateOrderReq)\r\n" +
		"// 保留业务注释\r\n" +
		"package acme-order/v1;\r\n" +
		"import \"legacy\\common_types.proto\";\r\n" +
		"import \"./order_local.proto\";\r\n" +
		"option (dophi_legacy) = \"ignore\";\r\n" +
		"message CreateOrderReq {\r\n" +
		"  string id = 1 [deprecated_dophi=true];\r\n" +
		"}\r\n"

	auxA := BuildEffectiveRepoAuxJSON(repo, &defCfg)
	out, err := norm.Normalize(ctx, NormalizeInput{
		Repo: repo,
		Files: map[string][]byte{
			"pg/order/create.proto":         []byte(rawProto),
			"pg/exempt/raw_legacy.proto":    []byte("syntax = \"proto3\";\r\npackage raw.v1;\r\nmessage Raw { string x = 1; }\r\n"),
			"pg/broken/quarantine_me.proto": []byte("syntax = \"proto3\";\npackage bad.v1;\nmessage B { string x = 1; }\n"),
		},
		AuxConfigJSON: auxA,
	})
	if err != nil {
		t.Fatalf("Normalize failed: %v", err)
	}

	fClean := out.Files["pg/order/create.proto"]
	cleanStr := string(fClean.CleanContent)
	if strings.HasPrefix(cleanStr, "\xEF\xBB\xBF") || strings.Contains(cleanStr, "\r") {
		t.Fatalf("NORM-001 failed: BOM or CR still present: %q", cleanStr)
	}
	if !strings.Contains(cleanStr, `syntax = "proto3";`) {
		t.Fatalf("NORM-002 failed: missing syntax header: %s", cleanStr)
	}
	if !strings.Contains(cleanStr, `package acme_order.v1;`) {
		t.Fatalf("NORM-003 failed: package not normalized: %s", cleanStr)
	}
	if strings.Contains(cleanStr, "#include") || strings.Contains(cleanStr, "DOPHI_DECLARE_MESSAGE") || strings.Contains(cleanStr, "dophi_legacy") {
		t.Fatalf("NORM-004/regex failed: nonstandard directive leaked: %s", cleanStr)
	}
	if !strings.Contains(cleanStr, "// 保留业务注释") {
		t.Fatalf("comment must be preserved: %s", cleanStr)
	}
	if !strings.Contains(cleanStr, `import "common/v1/types.proto";`) || !strings.Contains(cleanStr, `import "order/v1/local.proto";`) {
		t.Fatalf("NORM-005 failed: imports not rewritten: %s", cleanStr)
	}
	if !strings.Contains(cleanStr, "[deprecated = true]") {
		t.Fatalf("Layer 2 repo regex_rewrite not applied: %s", cleanStr)
	}
	if fClean.LinePreserving || len(fClean.LineMap) == 0 {
		t.Fatalf("expected non-line-preserving output with populated LineMap, got preserving=%v map=%v", fClean.LinePreserving, fClean.LineMap)
	}
	// Verify syntax-only parse succeeds after normalization!
	gate := ParseProtoSyntaxOnly("pg/order/create.proto", fClean.CleanContent)
	if !gate.Passed {
		t.Fatalf("expected ParseProtoSyntaxOnly to pass after normalization, got: %s\nclean:\n%s", gate.ErrorSummary, cleanStr)
	}

	// Idempotency check
	out2, err2 := norm.Normalize(ctx, NormalizeInput{
		Repo:          repo,
		Files:         map[string][]byte{"pg/order/create.proto": fClean.CleanContent},
		AuxConfigJSON: auxA,
	})
	if err2 != nil {
		t.Fatalf("2nd Normalize failed: %v", err2)
	}
	if string(out2.Files["pg/order/create.proto"].CleanContent) != cleanStr {
		t.Fatalf("Normalizer not idempotent:\nfirst:\n%s\nsecond:\n%s", cleanStr, string(out2.Files["pg/order/create.proto"].CleanContent))
	}

	// Exempt file stays untouched (keeps \r\n)
	exRes := out.Files["pg/exempt/raw_legacy.proto"]
	if !strings.Contains(string(exRes.CleanContent), "\r\n") {
		t.Fatalf("exempt file must stay untouched")
	}

	// Quarantined file emits ERROR diagnostic FILE_QUARANTINED
	qRes := out.Files["pg/broken/quarantine_me.proto"]
	foundQ := false
	for _, d := range qRes.Diagnostics {
		if d.Severity == "ERROR" && d.RuleID == "FILE_QUARANTINED" {
			foundQ = true
		}
	}
	if !foundQ {
		t.Fatalf("expected FILE_QUARANTINED ERROR diagnostic, got %+v", qRes.Diagnostics)
	}

	// EffectiveNormalizerVersion must change when config changes
	defCfg2 := defCfg
	defCfg2.Global.DefaultSyntax = "proto2"
	ver1 := EffectiveNormalizerVersion(norm, BuildEffectiveRepoAuxJSON(repo, &defCfg))
	ver2 := EffectiveNormalizerVersion(norm, BuildEffectiveRepoAuxJSON(repo, &defCfg2))
	if ver1 == ver2 || !strings.HasPrefix(ver1, "rule-normalizer/v2+") {
		t.Fatalf("expected distinct version keys prefixed with rule-normalizer/v2+, got %s vs %s", ver1, ver2)
	}
}

func TestSchemeAExportRelocation_IntraRepoPathMapping_AndCollisions(t *testing.T) {
	db, reg := setupGitClientTestDB(t)
	ctx := context.Background()

	oldCfg := config.AppConfig
	codesDir := filepath.Join(t.TempDir(), "codes")
	protosDir := filepath.Join(t.TempDir(), "protos")
	config.AppConfig.Storage.CodesDir = codesDir
	config.AppConfig.Storage.ProtosDir = protosDir
	config.AppConfig.Storage.SharedWriteEnabled = true
	defer func() {
		config.AppConfig = oldCfg
	}()

	bareA := createBareGitRepoWithCommits(t, "main", []map[string]string{
		{"pg/server/order.proto": "syntax = \"proto3\";\npackage trade.order;\nmessage OrderMsg { string id = 1; }\n"},
	})
	bareB := createBareGitRepoWithCommits(t, "main", []map[string]string{
		{"pg/pay.proto": "syntax = \"proto3\";\npackage fin.pay;\nmessage PayMsg { int64 fen = 1; }\n"},
	})

	repoA := models.GitRepo{
		Name:           "repo-a",
		Provider:       models.GitProviderGeneric,
		RepoURL:        bareA,
		DefaultBranch:  "main",
		AuthType:       models.GitAuthNone,
		PlatformRepoID: 201,
		SourceMode:     models.GitSourceModePlatformShared,
		SyncStrategy:   models.GitSyncStrategyFetchAndRead,
		SubsystemSlug:  "legacy-sub",
		SyncEnabled:    true,
	}
	repoB := models.GitRepo{
		Name:           "repo-b",
		Provider:       models.GitProviderGeneric,
		RepoURL:        bareB,
		DefaultBranch:  "main",
		AuthType:       models.GitAuthNone,
		PlatformRepoID: 202,
		SourceMode:     models.GitSourceModePlatformShared,
		SyncStrategy:   models.GitSyncStrategyFetchAndRead,
		SubsystemSlug:  "fin-sub",
		SyncEnabled:    true,
	}
	if err := db.Create(&repoA).Error; err != nil {
		t.Fatalf("create repoA: %v", err)
	}
	if err := db.Create(&repoB).Error; err != nil {
		t.Fatalf("create repoB: %v", err)
	}

	if _, err := RunFourStagePipeline(ctx, db, reg, nil, repoA.ID, PipelineOptions{
		Branch:             "main",
		CodesDir:           codesDir,
		ProtosDir:          protosDir,
		SharedWriteEnabled: true,
	}); err != nil {
		t.Fatalf("RunFourStagePipeline repoA: %v", err)
	}

	// Verify initial raw/ keeps pg/ and clean/ flattens pg/ under legacy-sub/repo-a
	rawPath := filepath.Join(protosDir, TreeKindRaw, "legacy-sub", "repo-a", "pg", "server", "order.proto")
	cleanOldPath := filepath.Join(protosDir, TreeKindClean, "legacy-sub", "repo-a", "server", "order.proto")
	if _, err := os.Stat(rawPath); err != nil {
		t.Fatalf("expected raw file at %s: %v", rawPath, err)
	}
	if _, err := os.Stat(cleanOldPath); err != nil {
		t.Fatalf("expected initial clean file at %s: %v", cleanOldPath, err)
	}

	// Update Scheme A target export dir + intra-repo strip_prefixes on repoA and run network-free re-normalize
	stripJSON, _ := json.Marshal([]string{"pg/server/"})
	if err := db.Model(&repoA).Updates(map[string]any{
		"export_target_subsystem": "trade",
		"export_target_repo_dir":  "order-service",
		"strip_prefixes_json":     datatypes.JSON(stripJSON),
	}).Error; err != nil {
		t.Fatalf("update repoA export config: %v", err)
	}

	if _, err := ReNormalizeRepo(ctx, db, reg, nil, repoA.ID, PipelineOptions{
		ProtosDir:        protosDir,
		ForceRenormalize: true,
	}); err != nil {
		t.Fatalf("ReNormalizeRepo failed: %v", err)
	}

	// raw/ must remain untouched at raw/legacy-sub/repo-a/pg/server/order.proto!
	if _, err := os.Stat(rawPath); err != nil {
		t.Fatalf("raw/ path must remain byte/path untouched at %s: %v", rawPath, err)
	}
	// old clean/ link must be cleaned up, and new clean/ link must contain stripped path order.proto!
	if _, err := os.Stat(cleanOldPath); !os.IsNotExist(err) {
		t.Fatalf("old clean symlink %s must be removed after relocation", cleanOldPath)
	}
	cleanNewPath := filepath.Join(protosDir, TreeKindClean, "trade", "order-service", "order.proto")
	if _, err := os.Stat(cleanNewPath); err != nil {
		t.Fatalf("expected relocated mapped clean file at %s: %v", cleanNewPath, err)
	}

	// Verify DB LogicalPath updated from strip_prefixes
	var pf models.ProtoFile
	if err := db.Where("git_repo_id = ? AND is_deleted = false", repoA.ID).First(&pf).Error; err != nil {
		t.Fatalf("query ProtoFile: %v", err)
	}
	if pf.FilePath != "order.proto" || pf.RepoFilePath != "pg/server/order.proto" {
		t.Fatalf("expected FilePath=order.proto RepoFilePath=pg/server/order.proto, got %q / %q", pf.FilePath, pf.RepoFilePath)
	}

	// Global 409 collision check if repoB also tries to export to trade/order-service
	cols, cErr := CheckGlobalExportTargetCollisions(ctx, db, map[uint64]*RepoGovernanceSpec{
		repoB.ID: {
			ExportTargetSubsystem: "trade",
			ExportTargetRepoDir:   "order-service",
		},
	})
	if !errors.Is(cErr, ErrExportTargetConflict) || len(cols) != 1 {
		t.Fatalf("expected ErrExportTargetConflict with 1 collision, got err=%v cols=%+v", cErr, cols)
	}

	// Intra-repo collision check when two files map onto same target
	_, iCols, iErr := MapAndValidateIntraRepoPaths(
		[]string{"pg/a/common.proto", "pg/b/common.proto"},
		[]string{"pg/a/", "pg/b/"},
		nil,
	)
	if !errors.Is(iErr, ErrIntraRepoPathConflict) || len(iCols) != 1 || iCols[0].MappedPath != "common.proto" {
		t.Fatalf("expected ErrIntraRepoPathConflict on common.proto, got err=%v cols=%+v", iErr, iCols)
	}
}
