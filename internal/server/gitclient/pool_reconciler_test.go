package gitclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonModels "code-common/backend/models"
	"code-contract/models"
)

func testSHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestPoolReconcile_R2Vote_R1R3R4Closure_AndDeterminism(t *testing.T) {
	ctx := context.Background()
	tmpRoot := t.TempDir()
	incDir := filepath.Join(tmpRoot, "include")
	if err := os.MkdirAll(filepath.Join(incDir, "sw", "pg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incDir, "sw", "pg", "BasicTypeMsg.proto"), []byte(`syntax = "proto2";
package sw.basic;
message VoidType { optional int32 ok = 1; }
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultGovernanceConfig()
	cfg.Global.SharedBaseSources = []SharedBaseSourceSpec{{PathHint: incDir, Priority: 1}}
	idx, err := BuildSharedBaseMemoryIndex(ctx, cfg.Global.SharedBaseSources, tmpRoot, &cfg)
	if err != nil {
		t.Fatalf("BuildSharedBaseMemoryIndex failed: %v", err)
	}

	// Construct multi-repo pool testing:
	// 1. Non-heuristic directory (mconst/pg/MConstMsg.proto) voted to ws/mconst/MConstMsg.proto via unique import!
	// 2. Bare filename import "LocalHelper.proto" inside track/ths/pg/TrackService.proto -> track/ths/LocalHelper.proto!
	// 3. R3 7-level disambiguation on shared/DupMsg.proto with different symbols -> winner + is_external_copy + symbol_diff!
	// 4. Non-closure *TC.proto file retained in pool with InServiceClosure = false!
	baseInputs := []PoolFileInput{
		{
			ProtoFileID:      1,
			GitRepoID:        10,
			RepoName:         "LT-track-svc",
			SubsystemSlug:    "track",
			BundleID:         100,
			Category:         models.ProtoCategoryBusiness,
			RepoFilePath:     "ths/pg/TrackService.proto",
			IsUServiceSource: true,
			Content: `syntax = "proto2";
package track.ths;
import "LocalHelper.proto";
import "ws/mconst/MConstMsg.proto";
import "shared/DupMsg.proto";
import "sw/BasicTypeMsg.proto";
service TrackService {
  rpc Query(LocalHelper) returns (sw.basic.VoidType);
}
`,
			PackageName: "track.ths",
			Imports: []string{
				"LocalHelper.proto",
				"ws/mconst/MConstMsg.proto",
				"shared/DupMsg.proto",
				"sw/BasicTypeMsg.proto",
			},
			Symbols: []ParsedProtoSymbol{
				{Kind: "SERVICE", FullName: "track.ths.TrackService"},
			},
		},
		{
			ProtoFileID:   2,
			GitRepoID:     10,
			RepoName:      "LT-track-svc",
			SubsystemSlug: "track",
			BundleID:      100,
			Category:      models.ProtoCategoryBusiness,
			RepoFilePath:  "ths/pg/LocalHelper.proto",
			Content: `syntax = "proto2";
package track.ths;
message LocalHelper { optional string id = 1; }
`,
			PackageName: "track.ths",
			Symbols: []ParsedProtoSymbol{
				{Kind: "MESSAGE", FullName: "track.ths.LocalHelper"},
			},
		},
		{
			ProtoFileID:   3,
			GitRepoID:     20,
			RepoName:      "ws-mconst",
			SubsystemSlug: "ws",
			BundleID:      200,
			Category:      models.ProtoCategoryBusiness,
			RepoFilePath:  "mconst/pg/MConstMsg.proto",
			Content: `syntax = "proto2";
package ws.mconst;
message MConstVal { optional int32 code = 1; }
`,
			PackageName: "ws.mconst",
			Symbols: []ParsedProtoSymbol{
				{Kind: "MESSAGE", FullName: "ws.mconst.MConstVal"},
			},
		},
		// Non-closure test case proto
		{
			ProtoFileID:   4,
			GitRepoID:     10,
			RepoName:      "LT-track-svc",
			SubsystemSlug: "track",
			BundleID:      100,
			Category:      models.ProtoCategoryBusiness,
			RepoFilePath:  "ths/pg/TrackTC.proto",
			Content: `syntax = "proto2";
package track.tc;
message TrackTC { optional string tc = 1; }
`,
			PackageName: "track.tc",
			Symbols: []ParsedProtoSymbol{
				{Kind: "MESSAGE", FullName: "track.tc.TrackTC"},
			},
		},
		// Conflict candidates for shared/DupMsg.proto (different content & unique symbols!)
		{
			ProtoFileID:   5,
			GitRepoID:     30,
			RepoName:      "shared-core",
			SubsystemSlug: "shared",
			BundleID:      300,
			Category:      models.ProtoCategoryBaseLib,
			RepoFilePath:  "shared/pg/DupMsg.proto",
			Content: `syntax = "proto2";
package shared.dup;
message DupPayload { optional string a = 1; }
message WinnerExtra { optional string w = 2; }
`,
			PackageName: "shared.dup",
			Symbols: []ParsedProtoSymbol{
				{Kind: "MESSAGE", FullName: "shared.dup.DupPayload"},
				{Kind: "MESSAGE", FullName: "shared.dup.WinnerExtra"},
			},
		},
		{
			ProtoFileID:   6,
			GitRepoID:     40,
			RepoName:      "legacy-dup",
			SubsystemSlug: "shared",
			BundleID:      400,
			Category:      models.ProtoCategoryBusiness,
			RepoFilePath:  "shared/pg/DupMsg.proto",
			Content: `syntax = "proto2";
package shared.dup;
message DupPayload { optional string a = 1; }
message LegacyExtra { optional string l = 2; }
`,
			PackageName: "shared.dup",
			Symbols: []ParsedProtoSymbol{
				{Kind: "MESSAGE", FullName: "shared.dup.DupPayload"},
				{Kind: "MESSAGE", FullName: "shared.dup.LegacyExtra"},
			},
		},
	}

	opts := PoolReconcileOptions{
		StripPGSegment:     true,
		ClosureOnly:        true,
		EnableR7AutoFix:    false,
		EnableR8Quarantine: false,
		GovernanceConfig:   &cfg,
		BaseIndex:          idx,
	}

	out1, err := ReconcilePoolInMemory(ctx, baseInputs, opts)
	if err != nil {
		t.Fatalf("ReconcilePoolInMemory failed: %v", err)
	}

	// Verify R2 vote mapped mconst/pg/MConstMsg.proto -> ws/mconst/MConstMsg.proto!
	byKey := make(map[string]*PoolFileState)
	for _, f := range out1.Files {
		byKey[f.RepoName+":"+f.RepoFilePath] = f
	}
	if got := byKey["ws-mconst:mconst/pg/MConstMsg.proto"].LogicalPath; got != "ws/mconst/MConstMsg.proto" {
		t.Fatalf("expected R2 vote logical path ws/mconst/MConstMsg.proto, got %q", got)
	}

	// Verify bare filename import expansion in TrackService.proto
	trackSvc := byKey["LT-track-svc:ths/pg/TrackService.proto"]
	if !strings.Contains(trackSvc.Content, `import "track/ths/LocalHelper.proto";`) &&
		!strings.Contains(trackSvc.Content, `import "ths/LocalHelper.proto";`) {
		t.Fatalf("expected bare import LocalHelper.proto to be expanded, got:\n%s", trackSvc.Content)
	}
	if out1.Report.Stats.BareImportExpandedCount != 1 {
		t.Fatalf("expected BareImportExpandedCount == 1, got %d", out1.Report.Stats.BareImportExpandedCount)
	}

	// Verify closure: TrackService, LocalHelper, MConstMsg, winner DupMsg, BasicTypeMsg in closure; TrackTC NOT in closure!
	if !byKey["LT-track-svc:ths/pg/TrackService.proto"].InServiceClosure ||
		!byKey["LT-track-svc:ths/pg/LocalHelper.proto"].InServiceClosure ||
		!byKey["ws-mconst:mconst/pg/MConstMsg.proto"].InServiceClosure {
		t.Fatalf("expected TrackService, LocalHelper, MConstMsg in service closure")
	}
	if byKey["LT-track-svc:ths/pg/TrackTC.proto"].InServiceClosure {
		t.Fatalf("expected non-closure TrackTC.proto to have InServiceClosure=false")
	}

	// Verify R4 harvested BasicTypeMsg.proto into closure as BASE_LIB
	var harvestedFound bool
	for _, f := range out1.Files {
		if f.LogicalPath == "sw/BasicTypeMsg.proto" {
			harvestedFound = true
			if !f.IsHarvestedBase || f.Category != models.ProtoCategoryBaseLib || !f.InServiceClosure {
				t.Fatalf("expected BasicTypeMsg harvested as BASE_LIB in closure, got %+v", f)
			}
		}
	}
	if !harvestedFound {
		t.Fatalf("expected BasicTypeMsg.proto harvested via R4")
	}

	// Verify R3 conflict disambiguation (BASE_LIB shared-core wins over legacy-dup) & symbol_diff
	if byKey["shared-core:shared/pg/DupMsg.proto"].IsExternalCopy {
		t.Fatalf("expected BASE_LIB winner not to be IsExternalCopy")
	}
	if !byKey["legacy-dup:shared/pg/DupMsg.proto"].IsExternalCopy {
		t.Fatalf("expected losing duplicate to be marked IsExternalCopy=true")
	}
	if len(out1.Report.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict entry, got %+v", out1.Report.Conflicts)
	}
	cEntry := out1.Report.Conflicts[0]
	if cEntry.ContentIdentical {
		t.Fatalf("expected ContentIdentical=false for differing DupMsg.proto copies")
	}
	if len(cEntry.SymbolDiff) == 0 {
		t.Fatalf("expected populated SymbolDiff in conflict report, got empty")
	}

	// Verify zero new post-strip collisions
	if out1.Report.Stats.PostStripCollisionCount != 0 {
		t.Fatalf("expected 0 post-strip collisions, got %+v", out1.Report.PostStripCollisions)
	}

	// Determinism check: reverse input slice and run 5 times -> byte-for-byte identical report JSON!
	baseJSON, _ := json.Marshal(out1.Report)
	reversed := make([]PoolFileInput, len(baseInputs))
	for i := range baseInputs {
		reversed[i] = baseInputs[len(baseInputs)-1-i]
	}
	for round := 0; round < 5; round++ {
		outN, err := ReconcilePoolInMemory(ctx, reversed, opts)
		if err != nil {
			t.Fatalf("round %d ReconcilePoolInMemory failed: %v", round, err)
		}
		nJSON, _ := json.Marshal(outN.Report)
		if string(nJSON) != string(baseJSON) {
			t.Fatalf("non-deterministic report on round %d:\nwant: %s\ngot:  %s", round, string(baseJSON), string(nJSON))
		}
	}
}

func TestRunPoolReconcile_DBPersistenceAndPreserveSystemBaseRepo(t *testing.T) {
	db, _ := setupGitClientTestDB(t)
	ctx := context.Background()
	tmpRoot := t.TempDir()
	incDir := filepath.Join(tmpRoot, "include")
	if err := os.MkdirAll(filepath.Join(incDir, "sw", "pg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incDir, "sw", "pg", "BasicTypeMsg.proto"), []byte(`syntax = "proto2";
package sw.basic;
message VoidType { optional int32 ok = 1; }
`), 0644); err != nil {
		t.Fatal(err)
	}

	govDoc := DefaultGovernanceConfig()
	govDoc.Global.SharedBaseSources = []SharedBaseSourceSpec{{PathHint: incDir, Priority: 1}}

	repo := models.GitRepo{
		Name:           "LT-db-svc",
		PlatformRepoID: 101,
		SourceMode:     models.GitSourceModePlatformShared,
		SyncStrategy:   models.GitSyncStrategyLocalCacheOnly,
		Provider:       models.GitProviderGitLab,
		RepoURL:        "http://mock/lt-db-svc.git",
		DefaultBranch:  "main",
		SubsystemSlug:  "track",
		SyncEnabled:    true,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatalf("create repo failed: %v", err)
	}
	bundle := models.GitProtoBundle{
		GitRepoID:        repo.ID,
		BundleName:       "track-svc",
		Category:         models.ProtoCategoryBusiness,
		TargetSchemaName: "track_svc",
		SubdirPath:       "ths/pg",
		AutoCompile:      true,
	}
	if err := db.Create(&bundle).Error; err != nil {
		t.Fatalf("create bundle failed: %v", err)
	}

	svcContent := `syntax = "proto2";
package track.ths;
import "sw/BasicTypeMsg.proto";
service TrackService {
  rpc Ping(sw.basic.VoidType) returns (sw.basic.VoidType);
}
`
	pfSvc := models.ProtoFile{
		GitRepoID:         repo.ID,
		BundleID:          bundle.ID,
		TargetSchemaName:  bundle.TargetSchemaName,
		Category:          models.ProtoCategoryBusiness,
		FilePath:          "track/ths/TrackService.proto",
		RepoFilePath:      "ths/pg/TrackService.proto",
		CleanRelPath:      "track/ths/TrackService.proto",
		PackageName:       "track.ths",
		NormalizerVersion: ruleNormalizerBaseVersion,
		PipelineStatus:    models.ProtoPipelineNormalized,
		IsUServiceSource:  true,
		ImportsJSON:       []byte(`["sw/BasicTypeMsg.proto"]`),
		SymbolsJSON:       []byte(`[{"kind":"SERVICE","full_name":"track.ths.TrackService"}]`),
		ContentSHA256:     testSHA256Hex(svcContent),
		LineCount:         6,
		LatestVersionNum:  1,
		LatestCommitSHA:   "c1",
	}
	if err := db.Create(&pfSvc).Error; err != nil {
		t.Fatalf("create pfSvc failed: %v", err)
	}
	verSvc := models.ProtoFileVersion{
		ProtoFileID:    pfSvc.ID,
		Version:        1,
		VersionTag:     "v1",
		SourceType:     models.ProtoSourceGitSync,
		CommitSHA:      "c1",
		ContentText:    svcContent,
		RawContentText: svcContent,
		ContentSHA256:  pfSvc.ContentSHA256,
		LineCount:      6,
		CommittedAt:    time.Now().UTC(),
	}
	if err := db.Create(&verSvc).Error; err != nil {
		t.Fatalf("create verSvc failed: %v", err)
	}
	if err := db.Model(&pfSvc).Update("latest_version_id", verSvc.ID).Error; err != nil {
		t.Fatal(err)
	}

	pfTC := models.ProtoFile{
		GitRepoID:         repo.ID,
		BundleID:          bundle.ID,
		TargetSchemaName:  bundle.TargetSchemaName,
		Category:          models.ProtoCategoryBusiness,
		FilePath:          "track/ths/TrackTC.proto",
		RepoFilePath:      "ths/pg/TrackTC.proto",
		CleanRelPath:      "track/ths/TrackTC.proto",
		PackageName:       "track.tc",
		NormalizerVersion: ruleNormalizerBaseVersion,
		PipelineStatus:    models.ProtoPipelineNormalized,
		ContentSHA256:     testSHA256Hex("message TrackTC{}"),
		LineCount:         1,
		LatestVersionNum:  1,
		LatestCommitSHA:   "c1",
	}
	if err := db.Create(&pfTC).Error; err != nil {
		t.Fatalf("create pfTC failed: %v", err)
	}

	run, report, err := RunPoolReconcile(ctx, db, PoolReconcileOptions{
		TriggeredBy:      "test-admin",
		ClosureOnly:      true,
		StripPGSegment:   true,
		CodesDir:         tmpRoot,
		ProtosDir:        filepath.Join(tmpRoot, "protos"),
		GovernanceConfig: &govDoc,
	})
	if err != nil {
		t.Fatalf("RunPoolReconcile failed: %v", err)
	}
	if run == nil || run.ID == 0 || run.Status != models.PoolReconcileStatusCompleted {
		t.Fatalf("expected COMPLETED run, got %+v", run)
	}
	if report.Stats.HarvestedBaseCount != 1 || report.Stats.ClosureFileCount != 2 {
		t.Fatalf("unexpected stats: %+v", report.Stats)
	}

	// Verify DB row flags updated
	var gotSvc, gotTC, gotHarvest models.ProtoFile
	if err := db.First(&gotSvc, pfSvc.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&gotTC, pfTC.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !gotSvc.InServiceClosure || gotTC.InServiceClosure {
		t.Fatalf("unexpected closure flags: svc=%v tc=%v", gotSvc.InServiceClosure, gotTC.InServiceClosure)
	}
	if err := db.Where("file_path = ? AND is_harvested_base = true", "sw/BasicTypeMsg.proto").First(&gotHarvest).Error; err != nil {
		t.Fatalf("expected harvested base persisted in DB: %v", err)
	}
	if gotHarvest.LatestVersionID == nil || !gotHarvest.InServiceClosure || gotHarvest.Category != models.ProtoCategoryBaseLib {
		t.Fatalf("unexpected harvested base DB record: %+v", gotHarvest)
	}

	// Verify system shared base repo survives AlignPlatformRepositories when omitted from repositories table!
	dep := commonModels.Department{Name: "测试部门"}
	if err := db.Create(&dep).Error; err != nil {
		t.Fatal(err)
	}
	owner := commonModels.User{Username: "pool_owner", Name: "pool_owner", Email: "pool_owner@example.com", Password: "hashed_password"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	platRepo := commonModels.Repository{
		ID:           uint(repo.PlatformRepoID),
		DepartmentID: dep.ID,
		Name:         repo.Name,
		URL:          repo.RepoURL,
		HTTPURL:      repo.RepoURL,
		OwnerID:      owner.ID,
		Branch:       "main",
		ServiceGroup: repo.SubsystemSlug,
		IsActive:     true,
	}
	if err := db.Create(&platRepo).Error; err != nil {
		t.Fatal(err)
	}
	_, _, _, err = AlignPlatformRepositories(ctx, db)
	if err != nil {
		t.Fatalf("AlignPlatformRepositories failed: %v", err)
	}
	var sysRepo models.GitRepo
	if err := db.Where("platform_repo_id = ?", SystemSharedBasePlatformRepoID).First(&sysRepo).Error; err != nil {
		t.Fatalf("expected system shared base repo to exist: %v", err)
	}
	if !sysRepo.SyncEnabled {
		t.Fatalf("expected system shared base repo SyncEnabled=true even after AlignPlatformRepositories")
	}
}
