package protosandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code-contract/config"
	"code-contract/internal/server/gitclient"
	"code-contract/models"
)

func buildTestGovernanceDoc(codesDir string) gitclient.GovernanceConfigDoc {
	doc := gitclient.DefaultGovernanceConfig()
	doc.Global.SharedBaseSources = []gitclient.SharedBaseSourceSpec{
		{PathHint: filepath.Join(codesDir, filepath.FromSlash(SandboxSharedBaseRel)), Priority: 1},
	}
	doc.Global.ExcludeSubsystems = []string{"SBX-3M"}
	doc.Global.ExcludeGlobs = append(doc.Global.ExcludeGlobs,
		"**/alpha/**", "**/beta/**", "**/m1/**", "**/m2/**", "**/m3/**",
	)
	doc.Repos["sandbox-inventory-svc"] = gitclient.RepoGovernanceSpec{
		StripPrefixes: []string{"server/pg/proto/"},
	}
	return doc
}

func TestSandboxFixturesGenerateAndCollectStageA(t *testing.T) {
	ctx := context.Background()
	codesDir := filepath.Join(t.TempDir(), "codes")

	baseDir, err := WriteSharedBaseDir(codesDir)
	if err != nil {
		t.Fatalf("WriteSharedBaseDir failed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(baseDir, "ltcommon", "pg", "OptionsExtensions.proto")); statErr != nil {
		t.Fatalf("missing OptionsExtensions.proto in %s: %v", baseDir, statErr)
	}

	fixtures := StandardFixtures()
	if len(fixtures) != 14 {
		t.Fatalf("expected 14 standard fixtures, got %d", len(fixtures))
	}

	doc := buildTestGovernanceDoc(codesDir)
	collectedCounts := make(map[string]int)

	for i, fx := range fixtures {
		localPath, sha, initErr := InitSandboxGitRepo(codesDir, fx)
		if initErr != nil {
			t.Fatalf("InitSandboxGitRepo(%s) failed: %v", fx.Name, initErr)
		}
		if fx.SkipLocalCache {
			if localPath != "" || sha != "" {
				t.Fatalf("expected SkipLocalCache repo %s to return empty path/sha", fx.Name)
			}
		} else if sha == "" {
			t.Fatalf("expected non-empty commit SHA for %s", fx.Name)
		}

		repo := models.GitRepo{
			ID:             uint64(i + 1),
			PlatformRepoID: uint64(i + 1),
			Name:           fx.Name,
			RepoURL:        fx.RepoURL(),
			DefaultBranch:  fx.Branch,
			SourceMode:     models.GitSourceModePlatformShared,
			SyncStrategy:   models.GitSyncStrategyLocalCacheOnly,
			SubsystemSlug:  gitclient.NormalizeSlug(fx.ServiceGroup, nil, "sub:"+fx.ServiceGroup, true),
		}

		res, cErr := gitclient.CollectRepoStageA(ctx, &repo, "main", gitclient.CollectConfig{
			CodesDir:           codesDir,
			SharedWriteEnabled: false,
			CachePolicy:        models.SyncCachePolicyCacheOnly,
			CollectGlobs:       doc.Global.CollectGlobs,
			ExcludeGlobs:       doc.Global.ExcludeGlobs,
		})
		if fx.Name == "sandbox-missing-cache" {
			if cErr == nil || !strings.Contains(cErr.Error(), gitclient.ErrLocalCacheMissing.Error()) {
				t.Fatalf("expected ErrLocalCacheMissing for %s, got %v", fx.Name, cErr)
			}
			continue
		}
		if cErr != nil {
			t.Fatalf("CollectRepoStageA(%s) failed: %v", fx.Name, cErr)
		}
		collectedCounts[fx.Name] = len(res.Files)
	}

	if collectedCounts["sandbox-no-proto-app"] != 0 {
		t.Fatalf("expected 0 files for sandbox-no-proto-app (uppercase PG/ ignored), got %d", collectedCounts["sandbox-no-proto-app"])
	}
	if collectedCounts["sandbox-inventory-svc"] != 1 {
		t.Fatalf("expected 1 file for sandbox-inventory-svc (*/pg/**/*.proto), got %d", collectedCounts["sandbox-inventory-svc"])
	}
	if collectedCounts["sandbox-fab-optics"] != 5 {
		t.Fatalf("expected 5 files for sandbox-fab-optics (alpha & m1 excluded by R0), got %d", collectedCounts["sandbox-fab-optics"])
	}
	if collectedCounts["sandbox-fab-motion"] != 3 {
		t.Fatalf("expected 3 files for sandbox-fab-motion, got %d", collectedCounts["sandbox-fab-motion"])
	}
}

func TestSandboxStageBV2AndPoolReconcile(t *testing.T) {
	ctx := context.Background()
	codesDir := filepath.Join(t.TempDir(), "codes")

	if _, err := WriteSharedBaseDir(codesDir); err != nil {
		t.Fatalf("WriteSharedBaseDir failed: %v", err)
	}
	fixtures := StandardFixtures()
	doc := buildTestGovernanceDoc(codesDir)
	norm := gitclient.NewRuleBasedNormalizer(&doc)

	baseIdx, err := gitclient.BuildSharedBaseMemoryIndex(ctx, doc.Global.SharedBaseSources, codesDir, &doc)
	if err != nil {
		t.Fatalf("BuildSharedBaseMemoryIndex failed: %v", err)
	}

	var poolInputs []gitclient.PoolFileInput
	var nextFileID uint64 = 1

	for i, fx := range fixtures {
		if fx.SkipLocalCache {
			continue
		}
		if _, _, initErr := InitSandboxGitRepo(codesDir, fx); initErr != nil {
			t.Fatalf("InitSandboxGitRepo(%s) failed: %v", fx.Name, initErr)
		}
		if gitclient.ShouldExcludeRepoByR0(fx.ServiceGroup, fx.Name, doc.Global) {
			continue
		}

		subSlug := gitclient.NormalizeSlug(fx.ServiceGroup, nil, "sub:"+fx.ServiceGroup, true)
		repo := models.GitRepo{
			ID:             uint64(i + 1),
			PlatformRepoID: uint64(i + 1),
			Name:           fx.Name,
			RepoURL:        fx.RepoURL(),
			DefaultBranch:  fx.Branch,
			SourceMode:     models.GitSourceModePlatformShared,
			SyncStrategy:   models.GitSyncStrategyLocalCacheOnly,
			SubsystemSlug:  subSlug,
		}
		if len(fx.StripPrefixes) > 0 {
			repo.StripPrefixesJSON = []byte(`["server/pg/proto/"]`)
		}

		res, cErr := gitclient.CollectRepoStageA(ctx, &repo, "main", gitclient.CollectConfig{
			CodesDir:           codesDir,
			SharedWriteEnabled: false,
			CachePolicy:        models.SyncCachePolicyCacheOnly,
			CollectGlobs:       doc.Global.CollectGlobs,
			ExcludeGlobs:       doc.Global.ExcludeGlobs,
		})
		if cErr != nil || len(res.Files) == 0 {
			continue
		}

		rawFiles := make(map[string][]byte, len(res.Files))
		for _, cf := range res.Files {
			rawFiles[cf.RepoFilePath] = cf.Content
		}
		aux := gitclient.BuildEffectiveRepoAuxJSON(repo, &doc)
		normOut, nErr := norm.Normalize(ctx, gitclient.NormalizeInput{
			Repo:          repo,
			Files:         rawFiles,
			AuxConfigJSON: aux,
		})
		if nErr != nil {
			t.Fatalf("Normalize(%s) failed: %v", fx.Name, nErr)
		}

		cat := models.ProtoCategoryBusiness
		if fx.Name == "sandbox-common-base" {
			cat = models.ProtoCategoryBaseLib
		}

		for _, cf := range res.Files {
			nf := normOut.Files[cf.RepoFilePath]
			gate := gitclient.ParseProtoSyntaxOnly(nf.CleanRelPath, nf.CleanContent)
			status := models.ProtoPipelineNormalized
			reason := ""
			if !gate.Passed {
				status = models.ProtoPipelineQuarantined
				reason = gate.ErrorSummary
			}
			sum := sha256.Sum256(nf.CleanContent)
			poolInputs = append(poolInputs, gitclient.PoolFileInput{
				ProtoFileID:        nextFileID,
				GitRepoID:          repo.ID,
				RepoName:           repo.Name,
				SubsystemSlug:      subSlug,
				BundleID:           repo.ID * 10,
				Category:           cat,
				RepoFilePath:       cf.RepoFilePath,
				CleanRelPath:       nf.CleanRelPath,
				LogicalPath:        nf.CleanRelPath,
				HasExplicitMapping: len(fx.StripPrefixes) > 0,
				Content:            string(nf.CleanContent),
				RawContent:         string(cf.Content),
				ContentSHA256:      hex.EncodeToString(sum[:]),
				RawBlobSHA:         cf.RawBlobSHA,
				PackageName:        gate.PackageName,
				Imports:            gate.Imports,
				Symbols:            gate.Symbols,
				LineMap:            nf.LineMap,
				IsUServiceSource:   nf.IsUServiceSource,
				PipelineStatus:     status,
				StatusReason:       reason,
			})
			nextFileID++
		}
	}

	out, err := gitclient.ReconcilePoolInMemory(ctx, poolInputs, gitclient.PoolReconcileOptions{
		ClosureOnly:        true,
		EnableR7AutoFix:    true,
		MaxR7Rounds:        12,
		EnableR8Quarantine: true,
		MaxR8Rounds:        150,
		StripPGSegment:     true,
		GovernanceConfig:   &doc,
		BaseIndex:          baseIdx,
		CodesDir:           codesDir,
	})
	if err != nil {
		t.Fatalf("ReconcilePoolInMemory failed: %v", err)
	}

	byRepoAndRaw := make(map[string]*gitclient.PoolFileState, len(out.Files))
	for _, st := range out.Files {
		byRepoAndRaw[st.RepoName+"::"+st.RepoFilePath] = st
	}

	// 1. StageMotionImpl.proto (无 Service 后缀): 必须被识别为 uService 并转换、注入 VoidType 依赖、展开裸 import
	motionImpl := byRepoAndRaw["sandbox-fab-motion::motion/pg/StageMotionImpl.proto"]
	if motionImpl == nil || !motionImpl.IsUServiceSource {
		t.Fatalf("expected StageMotionImpl.proto to be detected as uService source")
	}
	if !strings.Contains(motionImpl.Content, `syntax = "proto2";`) ||
		!strings.Contains(motionImpl.Content, "service StageMotionService") ||
		!strings.Contains(motionImpl.Content, `import "sw/BasicTypeMsg.proto";`) ||
		!strings.Contains(motionImpl.Content, `import "motion/StageMotionMsg.proto";`) {
		t.Fatalf("unexpected converted StageMotionImpl.proto content:\n%s", motionImpl.Content)
	}

	// 2. StageMotionTC.proto 应保留在池中，但不在服务依赖闭包内
	motionTC := byRepoAndRaw["sandbox-fab-motion::motion/pg/StageMotionTC.proto"]
	if motionTC == nil || motionTC.InServiceClosure {
		t.Fatalf("expected StageMotionTC.proto to have InServiceClosure=false, got %+v", motionTC)
	}

	// 3. 去 /pg 边界保护: extpg/OpticsCoeff.proto 与 pg.proto 不得误剥离
	if st := byRepoAndRaw["sandbox-fab-optics::pg/extpg/OpticsCoeff.proto"]; st == nil || st.CleanRelPath != "extpg/OpticsCoeff.proto" {
		t.Fatalf("expected extpg/OpticsCoeff.proto preserved, got %+v", st)
	}
	if st := byRepoAndRaw["sandbox-fab-optics::pg/pg.proto"]; st == nil || st.CleanRelPath != "pg.proto" {
		t.Fatalf("expected pg.proto preserved, got %+v", st)
	}

	// 4. CorruptSyntax.proto 应在 Stage B 语法门禁落入 QUARANTINED
	corrupt := byRepoAndRaw["sandbox-fab-broken::pg/broken/CorruptSyntax.proto"]
	if corrupt == nil || corrupt.PipelineStatus != models.ProtoPipelineQuarantined {
		t.Fatalf("expected CorruptSyntax.proto QUARANTINED, got %+v", corrupt)
	}

	// 5. R4 应从共享基座目录收割 OptionsExtensions.proto 与 BasicTypeMsg.proto
	harvested := make(map[string]bool)
	for _, h := range out.Report.Harvested {
		harvested[h.LogicalPath] = true
	}
	if !harvested["ltcommon/OptionsExtensions.proto"] || !harvested["sw/BasicTypeMsg.proto"] {
		t.Fatalf("expected R4 harvested OptionsExtensions.proto & BasicTypeMsg.proto, got %+v", out.Report.Harvested)
	}

	// 6. R3 应同时产出不等价冲突 (symbol_diff 非空) 与等价冲突 (content_identical=true)
	var foundDivergent, foundIdentical bool
	for _, c := range out.Report.Conflicts {
		if strings.HasSuffix(c.LogicalPath, "SharedDiagMsg.proto") {
			foundDivergent = true
			if c.ContentIdentical || len(c.SymbolDiff) == 0 {
				t.Fatalf("expected SharedDiagMsg.proto divergent conflict with symbol_diff, got %+v", c)
			}
		}
		if strings.HasSuffix(c.LogicalPath, "IdenticalUtilMsg.proto") {
			foundIdentical = true
			if !c.ContentIdentical {
				t.Fatalf("expected IdenticalUtilMsg.proto content_identical=true, got %+v", c)
			}
		}
	}
	if !foundDivergent || !foundIdentical {
		t.Fatalf("expected both SharedDiagMsg.proto and IdenticalUtilMsg.proto conflicts, got %+v", out.Report.Conflicts)
	}
}

func TestSandboxMutatorAndRollback(t *testing.T) {
	codesDir := filepath.Join(t.TempDir(), "codes")
	var noProtoFx RepoFixture
	for _, fx := range StandardFixtures() {
		if fx.Name == "sandbox-no-proto-app" {
			noProtoFx = fx
			break
		}
	}
	localPath, v1SHA, err := InitSandboxGitRepo(codesDir, noProtoFx)
	if err != nil {
		t.Fatalf("InitSandboxGitRepo failed: %v", err)
	}

	v2SHA, err := CommitAndAdvanceOrigin(localPath, "main", map[string][]byte{
		"pg/notify_service.proto": []byte("syntax = \"proto3\";\npackage tools.notify;\nmessage Ping { string id = 1; }\n"),
	}, "feat: add pg proto")
	if err != nil {
		t.Fatalf("CommitAndAdvanceOrigin failed: %v", err)
	}
	if v2SHA == "" || v2SHA == v1SHA {
		t.Fatalf("expected commit SHA to advance (%s -> %s)", v1SHA, v2SHA)
	}

	rolledSHA, err := RollbackRepoToTag(localPath, "main", SandboxTagV1)
	if err != nil {
		t.Fatalf("RollbackRepoToTag failed: %v", err)
	}
	if rolledSHA != v1SHA {
		t.Fatalf("expected rollback SHA %s, got %s", v1SHA, rolledSHA)
	}
	if _, statErr := os.Stat(filepath.Join(localPath, "pg", "notify_service.proto")); !os.IsNotExist(statErr) {
		t.Fatalf("expected added pg/notify_service.proto removed after rollback")
	}
}

func TestBuildScaleFixturesDistribution(t *testing.T) {
	items := BuildScaleFixtures(100)
	if len(items) != 100 {
		t.Fatalf("expected 100 scale fixtures, got %d", len(items))
	}
	_ = config.AppConfig
}
