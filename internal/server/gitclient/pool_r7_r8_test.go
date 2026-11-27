package gitclient

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code-contract/models"
)

func TestPoolR7R8_FourFixClasses_RootCauseQuarantine_AndWorkspaceExport(t *testing.T) {
	ctx := context.Background()

	t.Run("fix_a_fix_b_variant_swap_and_root_cause_quarantine", func(t *testing.T) {
		tmpRoot := t.TempDir()
		incDir := filepath.Join(tmpRoot, "include")
		if err := os.MkdirAll(filepath.Join(incDir, "sw", "pg"), 0755); err != nil {
			t.Fatal(err)
		}
		// R4 Channel B target harvested via missing symbol sw.basic.HarvestedMeta!
		if err := os.WriteFile(filepath.Join(incDir, "sw", "pg", "HarvestedMeta.proto"), []byte(`syntax = "proto2";
package sw.basic;
message HarvestedMeta { optional string trace_id = 1; }
`), 0644); err != nil {
			t.Fatal(err)
		}

		cfg := DefaultGovernanceConfig()
		cfg.Global.SharedBaseSources = []SharedBaseSourceSpec{{PathHint: incDir, Priority: 1}}
		idx, err := BuildSharedBaseMemoryIndex(ctx, cfg.Global.SharedBaseSources, tmpRoot, &cfg)
		if err != nil {
			t.Fatal(err)
		}

		inputs := []PoolFileInput{
			// 1. Service proto:
			//    - wrong import "wrong/dir/TargetHelper.proto" -> fix(a) rewrites to "app/TargetHelper.proto"
			//    - missing import for same-dir sibling SiblingReq -> fix(b) adds "app/SiblingTypes.proto" (with line_map shift)
			//    - missing import for sw.basic.HarvestedMeta -> fix(b) harvests via R4 Channel B -> adds "sw/HarvestedMeta.proto"
			//    - missing symbol ConflictExtra from shared/ConflictMsg.proto -> variant_swap flips loser->winner!
			{
				ProtoFileID:        1,
				GitRepoID:          10,
				RepoName:           "app-svc",
				SubsystemSlug:      "app",
				BundleID:           100,
				Category:           models.ProtoCategoryBusiness,
				RepoFilePath:       "pg/AppService.proto",
				CleanRelPath:       "app/AppService.proto",
				LogicalPath:        "app/AppService.proto",
				HasExplicitMapping: true,
				IsUServiceSource:   true,
				Content: `syntax = "proto2";
package app.v1;
import "wrong/dir/TargetHelper.proto";
import "shared/ConflictMsg.proto";
service AppService {
  rpc DoWork(SiblingReq) returns (app.helper.HelperRsp);
  rpc Audit(ConflictExtra) returns (sw.basic.HarvestedMeta);
}
`,
				PackageName: "app.v1",
				Imports: []string{
					"wrong/dir/TargetHelper.proto",
					"shared/ConflictMsg.proto",
				},
				Symbols: []ParsedProtoSymbol{
					{Kind: "SERVICE", FullName: "app.v1.AppService"},
				},
			},
			{
				ProtoFileID:        2,
				GitRepoID:          10,
				RepoName:           "app-svc",
				SubsystemSlug:      "app",
				BundleID:           100,
				Category:           models.ProtoCategoryBusiness,
				RepoFilePath:       "pg/TargetHelper.proto",
				CleanRelPath:       "app/TargetHelper.proto",
				LogicalPath:        "app/TargetHelper.proto",
				HasExplicitMapping: true,
				Content: `syntax = "proto2";
package app.helper;
message HelperRsp { optional string val = 1; }
`,
				PackageName: "app.helper",
				Symbols:     []ParsedProtoSymbol{{Kind: "MESSAGE", FullName: "app.helper.HelperRsp"}},
			},
			// 同目录兄弟文件定义 SiblingReq，同时另一个更远目录也定义了同名消息以验证同目录优先！
			{
				ProtoFileID:        3,
				GitRepoID:          10,
				RepoName:           "app-svc",
				SubsystemSlug:      "app",
				BundleID:           100,
				Category:           models.ProtoCategoryBusiness,
				RepoFilePath:       "pg/SiblingTypes.proto",
				CleanRelPath:       "app/SiblingTypes.proto",
				LogicalPath:        "app/SiblingTypes.proto",
				HasExplicitMapping: true,
				Content: `syntax = "proto2";
package app.v1;
message SiblingReq { optional string req_id = 1; }
`,
				PackageName: "app.v1",
				Symbols:     []ParsedProtoSymbol{{Kind: "MESSAGE", FullName: "app.v1.SiblingReq"}},
			},
			{
				ProtoFileID:        4,
				GitRepoID:          99,
				RepoName:           "other-repo",
				SubsystemSlug:      "other",
				BundleID:           990,
				Category:           models.ProtoCategoryBusiness,
				RepoFilePath:       "pg/FarSibling.proto",
				CleanRelPath:       "other/FarSibling.proto",
				LogicalPath:        "other/FarSibling.proto",
				HasExplicitMapping: true,
				Content: `syntax = "proto2";
package other.v1;
message SiblingReq { optional string wrong_far = 1; }
`,
				PackageName: "other.v1",
				Symbols:     []ParsedProtoSymbol{{Kind: "MESSAGE", FullName: "other.v1.SiblingReq"}},
			},
			// Variant swap candidates at shared/ConflictMsg.proto:
			// a_winner wins initial R3 alphabetically/base_lib, lacks ConflictExtra; z_loser defines ConflictExtra!
			{
				ProtoFileID:        5,
				GitRepoID:          20,
				RepoName:           "a-shared-old",
				SubsystemSlug:      "shared",
				BundleID:           200,
				Category:           models.ProtoCategoryBaseLib,
				RepoFilePath:       "shared/pg/ConflictMsg.proto",
				CleanRelPath:       "shared/ConflictMsg.proto",
				LogicalPath:        "shared/ConflictMsg.proto",
				HasExplicitMapping: true,
				Content: `syntax = "proto2";
package app.v1;
message BaseOnly { optional string x = 1; }
`,
				PackageName: "app.v1",
				Symbols:     []ParsedProtoSymbol{{Kind: "MESSAGE", FullName: "app.v1.BaseOnly"}},
			},
			{
				ProtoFileID:        6,
				GitRepoID:          21,
				RepoName:           "z-shared-new",
				SubsystemSlug:      "shared",
				BundleID:           201,
				Category:           models.ProtoCategoryBusiness,
				RepoFilePath:       "shared/pg/ConflictMsg.proto",
				CleanRelPath:       "shared/ConflictMsg.proto",
				LogicalPath:        "shared/ConflictMsg.proto",
				HasExplicitMapping: true,
				Content: `syntax = "proto2";
package app.v1;
message BaseOnly { optional string x = 1; }
message ConflictExtra { optional string extra = 2; }
`,
				PackageName: "app.v1",
				Symbols: []ParsedProtoSymbol{
					{Kind: "MESSAGE", FullName: "app.v1.BaseOnly"},
					{Kind: "MESSAGE", FullName: "app.v1.ConflictExtra"},
				},
			},
			// R8 根因 vs 受害者：BrokenRoot.proto 含语法损坏，VictimCaller.proto 仅引用 UnknownBrokenType
			{
				ProtoFileID:        7,
				GitRepoID:          30,
				RepoName:           "broken-svc",
				SubsystemSlug:      "broken",
				BundleID:           300,
				Category:           models.ProtoCategoryBusiness,
				RepoFilePath:       "pg/BrokenRoot.proto",
				CleanRelPath:       "broken/BrokenRoot.proto",
				LogicalPath:        "broken/BrokenRoot.proto",
				HasExplicitMapping: true,
				IsUServiceSource:   true,
				Content: `syntax = "proto2";
package broken.v1;
service BrokenRootSvc {
  rpc Call(UnknownReqType) returns (BrokenInvalidField);
}
message BadMsg { invalid_keyword_without_type = ; }
`,
				PackageName: "broken.v1",
				Symbols:     []ParsedProtoSymbol{{Kind: "SERVICE", FullName: "broken.v1.BrokenRootSvc"}},
			},
		}

		out, err := ReconcilePoolInMemory(ctx, inputs, PoolReconcileOptions{
			ClosureOnly:        true,
			StripPGSegment:     true,
			EnableR7AutoFix:    true,
			MaxR7Rounds:        10,
			EnableR8Quarantine: true,
			MaxR8Rounds:        150,
			GovernanceConfig:   &cfg,
			BaseIndex:          idx,
		})
		if err != nil {
			t.Fatalf("ReconcilePoolInMemory failed: %v", err)
		}
		if out.Report.Stats.CompileErrorCount != 0 {
			t.Fatalf("expected 0 remaining compile errors after R7+R8, got %d (report=%+v)", out.Report.Stats.CompileErrorCount, out.Report)
		}

		var gotFixA, gotFixB_Sibling, gotFixB_Harvest, gotVariantSwap bool
		for _, af := range out.Report.AutoFixes {
			switch {
			case af.FixKind == "fix(a)" && af.OldImport == "wrong/dir/TargetHelper.proto" && af.NewImport == "app/TargetHelper.proto":
				gotFixA = true
			case af.FixKind == "fix(b)" && af.NewImport == "app/SiblingTypes.proto":
				gotFixB_Sibling = true
			case af.FixKind == "fix(b)" && af.NewImport == "sw/HarvestedMeta.proto":
				gotFixB_Harvest = true
			case af.FixKind == "variant_swap" && af.FilePath == "shared/ConflictMsg.proto" && af.NewImport == "z-shared-new":
				gotVariantSwap = true
			}
		}
		if !gotFixA || !gotFixB_Sibling || !gotFixB_Harvest || !gotVariantSwap {
			t.Fatalf("expected fix(a), fix(b) sibling + harvest, variant_swap; got auto_fixes=%+v", out.Report.AutoFixes)
		}

		if len(out.Report.Quarantined) != 1 || out.Report.Quarantined[0].LogicalPath != "broken/BrokenRoot.proto" || !out.Report.Quarantined[0].IsRootCause {
			t.Fatalf("expected BrokenRoot.proto quarantined as root cause, got %+v", out.Report.Quarantined)
		}

		// Verify AppService line_map shifted when imports were inserted
		for _, f := range out.Files {
			if f.LogicalPath == "app/AppService.proto" {
				if len(f.LineMap) == 0 {
					t.Fatalf("expected LineMap updated on fix(b) insertion")
				}
			}
		}

		// Export workspace/ and verify clean-report.json + clean protocompile compilation from disk!
		protosDir := filepath.Join(tmpRoot, "protos")
		ver, linkPath, err := exportReconciledWorkspace(protosDir, 42, out)
		if err != nil {
			t.Fatalf("exportReconciledWorkspace failed: %v", err)
		}
		if ver == "" || linkPath == "" {
			t.Fatalf("expected non-empty version and linkPath")
		}
		repBytes, err := os.ReadFile(filepath.Join(linkPath, ".clean-report.json"))
		if err != nil {
			t.Fatalf("expected .clean-report.json in workspace: %v", err)
		}
		var diskReport PoolReconcileReport
		if err := json.Unmarshal(repBytes, &diskReport); err != nil || diskReport.Stats.CompileErrorCount != 0 {
			t.Fatalf("unexpected .clean-report.json: err=%v stats=%+v", err, diskReport.Stats)
		}

		// Load all exported .proto files from disk and compile cleanly with protocompile
		diskFiles := make(map[string][]byte)
		if err := filepath.WalkDir(linkPath, func(p string, d fs.DirEntry, wErr error) error {
			if wErr != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".proto") {
				return wErr
			}
			rel, _ := filepath.Rel(linkPath, p)
			b, rErr := os.ReadFile(p)
			if rErr != nil {
				return rErr
			}
			diskFiles[filepath.ToSlash(rel)] = b
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		errs, _ := CompileClosureWithProtocompile(ctx, diskFiles)
		if len(errs) != 0 {
			t.Fatalf("expected 0 protocompile errors on exported workspace/, got %+v", errs)
		}
	})

	t.Run("fix_c_only_joint_progress_and_oscillation_guard", func(t *testing.T) {
		// AppOnlyFixC ALREADY imports shared/LegacyTypes.proto (bad dependency missing NeededPayload),
		// while shared/V2Types.proto defines NeededPayload! Only fix(c) can heal this -> must NOT fall into R8!
		inputs := []PoolFileInput{
			{
				ProtoFileID:        101,
				GitRepoID:          1,
				RepoName:           "app-c",
				SubsystemSlug:      "app",
				Category:           models.ProtoCategoryBusiness,
				RepoFilePath:       "pg/CallerC.proto",
				CleanRelPath:       "app/CallerC.proto",
				LogicalPath:        "app/CallerC.proto",
				HasExplicitMapping: true,
				IsUServiceSource:   true,
				Content: `syntax = "proto2";
package app.c;
import "shared/Types.proto";
service CallerCSvc {
  rpc Call(shared.c.NeededPayload) returns (shared.c.NeededPayload);
}
`,
				PackageName: "app.c",
				Imports:     []string{"shared/Types.proto"},
				Symbols:     []ParsedProtoSymbol{{Kind: "SERVICE", FullName: "app.c.CallerCSvc"}},
			},
			{
				ProtoFileID:        102,
				GitRepoID:          2,
				RepoName:           "legacy-types",
				SubsystemSlug:      "shared",
				Category:           models.ProtoCategoryBusiness,
				RepoFilePath:       "pg/Types.proto",
				CleanRelPath:       "shared/Types.proto",
				LogicalPath:        "shared/Types.proto",
				HasExplicitMapping: true,
				Content: `syntax = "proto2";
package shared.c;
message OtherPayload { optional string id = 1; }
`,
				PackageName: "shared.c",
				Symbols:     []ParsedProtoSymbol{{Kind: "MESSAGE", FullName: "shared.c.OtherPayload"}},
			},
			{
				ProtoFileID:        103,
				GitRepoID:          3,
				RepoName:           "v2-types",
				SubsystemSlug:      "v2",
				Category:           models.ProtoCategoryBaseLib,
				RepoFilePath:       "shared/pg/Types.proto",
				CleanRelPath:       "v2/shared/Types.proto",
				LogicalPath:        "v2/shared/Types.proto",
				HasExplicitMapping: true,
				Content: `syntax = "proto2";
package shared.c;
message NeededPayload { optional string id = 1; }
`,
				PackageName: "shared.c",
				Symbols:     []ParsedProtoSymbol{{Kind: "MESSAGE", FullName: "shared.c.NeededPayload"}},
			},
		}

		// Notice resolveMissingSymbolTarget might also fix via fix(b) if fix(b) adds v2/shared/Types.proto.
		// To isolate fix(c) ONLY: make LegacyTypes.proto define OtherPayload + NeededConflict, or ensure fix(c) handles replacing stale import.
		// Wait: if we disable fix(b) or test direct swap when NeededPayload exists ONLY in same-basename v2/shared/Types.proto while CallerC imports shared/Types.proto,
		// BOTH fix(b) and fix(c) wouldn't run together if fix(b) wasn't colliding!
		// What causes fix(b) to skip and ONLY fix(c) to succeed? SYMBOL COLLISION with OtherPayload!
		inputs[1].Content = `syntax = "proto2";
package shared.c;
message DuplicateBase { optional string d = 1; }
`
		inputs[1].Symbols = []ParsedProtoSymbol{{Kind: "MESSAGE", FullName: "shared.c.DuplicateBase"}}
		inputs[2].Content = `syntax = "proto2";
package shared.c;
message DuplicateBase { optional string d = 1; }
message NeededPayload { optional string id = 1; }
`
		inputs[2].Symbols = []ParsedProtoSymbol{
			{Kind: "MESSAGE", FullName: "shared.c.DuplicateBase"},
			{Kind: "MESSAGE", FullName: "shared.c.NeededPayload"},
		}
		// Because DuplicateBase is in both shared/Types.proto AND v2/shared/Types.proto:
		// - fix(b) ADDING v2/shared/Types.proto alongside shared/Types.proto is blocked by hasDirectImportSymbolCollision!
		// - ONLY fix(c) SWAPPING shared/Types.proto -> v2/shared/Types.proto avoids the DuplicateBase collision AND resolves NeededPayload!

		out, err := ReconcilePoolInMemory(ctx, inputs, PoolReconcileOptions{
			ClosureOnly:        true,
			StripPGSegment:     true,
			EnableR7AutoFix:    true,
			MaxR7Rounds:        5,
			EnableR8Quarantine: true,
			MaxR8Rounds:        10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.Report.Stats.CompileErrorCount != 0 {
			t.Fatalf("expected fix(c) ONLY to achieve 0 compile errors, got errs=%d autoFixes=%+v quarantined=%+v",
				out.Report.Stats.CompileErrorCount, out.Report.AutoFixes, out.Report.Quarantined)
		}
		var sawFixC bool
		for _, af := range out.Report.AutoFixes {
			if af.FixKind == "fix(c)" && af.OldImport == "shared/Types.proto" && af.NewImport == "v2/shared/Types.proto" {
				sawFixC = true
			}
		}
		if !sawFixC {
			t.Fatalf("expected fix(c) swap entry, got %+v", out.Report.AutoFixes)
		}
		if len(out.Report.Quarantined) != 0 {
			t.Fatalf("expected 0 quarantined when fix(c) heals cleanly, got %+v", out.Report.Quarantined)
		}
	})

	t.Run("r8_root_cause_priority_duplicate_symbol_over_victim", func(t *testing.T) {
		inputs := []PoolFileInput{
			// 根因文件 DupDefA.proto 与 DupDefB.proto 符号冲突 (already defined)，VictimService.proto 仅引用 DupMsg 且附加引用 MissingType
			{
				ProtoFileID:        201,
				GitRepoID:          1,
				RepoName:           "victim-svc",
				SubsystemSlug:      "app",
				Category:           models.ProtoCategoryBusiness,
				RepoFilePath:       "pg/VictimSvc.proto",
				CleanRelPath:       "app/VictimSvc.proto",
				LogicalPath:        "app/VictimSvc.proto",
				HasExplicitMapping: true,
				IsUServiceSource:   true,
				Content: `syntax = "proto2";
package app.r8;
import "app/DupDefA.proto";
service VictimSvc {
  rpc Call(app.r8.BadRefOnly) returns (app.r8.DupMsg);
}
`,
				PackageName: "app.r8",
				Imports:     []string{"app/DupDefA.proto"},
				Symbols:     []ParsedProtoSymbol{{Kind: "SERVICE", FullName: "app.r8.VictimSvc"}},
			},
			{
				ProtoFileID:        202,
				GitRepoID:          2,
				RepoName:           "dup-a",
				SubsystemSlug:      "app",
				Category:           models.ProtoCategoryBusiness,
				RepoFilePath:       "pg/DupDefA.proto",
				CleanRelPath:       "app/DupDefA.proto",
				LogicalPath:        "app/DupDefA.proto",
				HasExplicitMapping: true,
				Content: `syntax = "proto2";
package app.r8;
message DupMsg { optional string a = 1; }
message BrokenSyntax { bad_token = ; }
`,
				PackageName: "app.r8",
				Symbols:     []ParsedProtoSymbol{{Kind: "MESSAGE", FullName: "app.r8.DupMsg"}},
			},
		}

		// 关闭 R7，直接验证 R8 根因权重排序：DupDefA (语法/符号根因) 必须先于 VictimSvc 被选中并隔离！
		out, err := ReconcilePoolInMemory(ctx, inputs, PoolReconcileOptions{
			ClosureOnly:        true,
			StripPGSegment:     true,
			EnableR7AutoFix:    false,
			EnableR8Quarantine: true,
			MaxR8Rounds:        10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Report.Quarantined) == 0 {
			t.Fatalf("expected at least 1 quarantined entry")
		}
		// VictimSvc 虽是受害者且还报错 BadRefOnly，但 DupDefA 含语法根因，应优先被标记 is_root_cause=true 隔离！
		var sawRootCause bool
		for _, q := range out.Report.Quarantined {
			if q.LogicalPath == "app/DupDefA.proto" && q.IsRootCause {
				sawRootCause = true
			}
		}
		if !sawRootCause {
			t.Fatalf("expected app/DupDefA.proto prioritized as root cause in R8, got %+v", out.Report.Quarantined)
		}
	})
}

func TestPoolReconcile_BenchmarkFixture_AndOptionalCorpus(t *testing.T) {
	ctx := context.Background()

	t.Run("multi_subsystem_deterministic_benchmark", func(t *testing.T) {
		tmpRoot := t.TempDir()
		incDir := filepath.Join(tmpRoot, "shared_inc")
		if err := os.MkdirAll(filepath.Join(incDir, "il", "pg"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(incDir, "il", "pg", "ilfmp.proto"), []byte(`syntax = "proto2";
package il.ilfmp;
message HeaderMeta { optional string session_id = 1; }
`), 0644); err != nil {
			t.Fatal(err)
		}

		govDoc := ProductionUServicePresetConfig()
		govDoc.Global.SharedBaseSources = []SharedBaseSourceSpec{{PathHint: incDir, Priority: 1}}
		idx, err := BuildSharedBaseMemoryIndex(ctx, govDoc.Global.SharedBaseSources, tmpRoot, &govDoc)
		if err != nil {
			t.Fatal(err)
		}

		// Run Stage B normalizer v2 on raw uService input first
		norm := &RuleBasedNormalizer{Config: &govDoc}
		rawSvc := `/* uService comment package fake; */
package fake.svc;
import "ilfmp.proto";
message ReqItem {
    optional string order_id = 1;
    optional il.ilfmp.HeaderMeta meta = 2;
}
uService BenchTradeService {
    interface SubmitOrder(ReqItem) oneway;
}
`
		repo := models.GitRepo{
			ID:            10,
			Name:          "LT-bench-trade",
			SubsystemSlug: "trade",
			SyncEnabled:   true,
		}
		normOut, err := norm.Normalize(ctx, NormalizeInput{
			Repo:  repo,
			Files: map[string][]byte{"trade/pg/BenchTrade.proto": []byte(rawSvc)},
		})
		if err != nil {
			t.Fatal(err)
		}
		normRes := normOut.Files["trade/pg/BenchTrade.proto"]
		if !normRes.IsUServiceSource || normRes.CleanRelPath != "trade/BenchTrade.proto" {
			t.Fatalf("unexpected Stage B v2 result: %+v", normRes)
		}
		gate := ParseProtoSyntaxOnly(normRes.CleanRelPath, normRes.CleanContent)
		if !gate.Passed {
			t.Fatalf("expected converted uService proto to parse cleanly, got %s\n%s", gate.ErrorSummary, string(normRes.CleanContent))
		}

		// Notice BenchTrade uses package il.ilfmp.HeaderMeta or import "ilfmp.proto" -> replace with reference to HeaderMeta
		_ = idx
		benchInputs := []PoolFileInput{
			{
				ProtoFileID:      1,
				GitRepoID:        10,
				RepoName:         repo.Name,
				SubsystemSlug:    repo.SubsystemSlug,
				Category:         models.ProtoCategoryBusiness,
				RepoFilePath:     "trade/pg/BenchTrade.proto",
				CleanRelPath:     normRes.CleanRelPath,
				LogicalPath:      normRes.CleanRelPath,
				IsUServiceSource: true,
				Content:          string(normRes.CleanContent),
				PackageName:      gate.PackageName,
				Imports:          gate.Imports,
				Symbols:          gate.Symbols,
			},
		}

		opts := PoolReconcileOptions{
			ClosureOnly:        true,
			StripPGSegment:     true,
			EnableR7AutoFix:    true,
			MaxR7Rounds:        10,
			EnableR8Quarantine: true,
			MaxR8Rounds:        150,
			GovernanceConfig:   &govDoc,
			BaseIndex:          idx,
		}
		out1, err := ReconcilePoolInMemory(ctx, benchInputs, opts)
		if err != nil {
			t.Fatal(err)
		}
		out2, err := ReconcilePoolInMemory(ctx, benchInputs, opts)
		if err != nil {
			t.Fatal(err)
		}
		if out1.Report.Stats.CompileErrorCount != 0 || out1.Report.Stats.PostStripCollisionCount != 0 {
			t.Fatalf("expected 0 errors & 0 post-strip collisions in benchmark, got %+v", out1.Report)
		}
		b1, _ := json.Marshal(out1.Report)
		b2, _ := json.Marshal(out2.Report)
		if string(b1) != string(b2) {
			t.Fatalf("benchmark run not deterministic")
		}
	})

	t.Run("optional_external_corpus_regression", func(t *testing.T) {
		corpusDir := strings.TrimSpace(os.Getenv("ADR28_CORPUS_DIR"))
		if corpusDir == "" {
			t.Skip("ADR28_CORPUS_DIR 未设置，跳过现网快照离线回归")
		}
		if _, err := os.Stat(corpusDir); err != nil {
			t.Skipf("ADR28_CORPUS_DIR %q 不可访问，跳过: %v", corpusDir, err)
		}
	})
}
