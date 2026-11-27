package gitclient

import (
	"context"
	"errors"
	"strings"
	"testing"

	"code-contract/models"
)

func TestStripPGPathSegment_AndPoolStripCollisionGuard(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"pg/A.proto", "A.proto"},
		{"wsttp/pg/B.proto", "wsttp/B.proto"},
		{"a/b/pg/sub/C.proto", "a/b/sub/C.proto"},
		{"ltcomm/ltdd/pg/GtPosMsg.proto", "ltcomm/ltdd/GtPosMsg.proto"},
		// Protected non-exact segment names and filename pg.proto:
		{"extpg/Foo.proto", "extpg/Foo.proto"},
		{"intpg/Bar.proto", "intpg/Bar.proto"},
		{"pggen/Baz.proto", "pggen/Baz.proto"},
		{"pg_common/Q.proto", "pg_common/Q.proto"},
		{"mypg/R.proto", "mypg/R.proto"},
		{"a/pg.proto", "a/pg.proto"},
		{"pg.proto", "pg.proto"},
		// WKT untouched by NormalizeLogicalPath:
		{"google/protobuf/empty.proto", "google/protobuf/empty.proto"},
	}

	for _, tc := range cases {
		got := NormalizeLogicalPath(tc.in, true)
		if got != tc.want {
			t.Errorf("NormalizeLogicalPath(%q, true) = %q, want %q", tc.in, got, tc.want)
		}
		// Idempotency
		if got2 := NormalizeLogicalPath(got, true); got2 != got {
			t.Errorf("NormalizeLogicalPath not idempotent: %q -> %q -> %q", tc.in, got, got2)
		}
	}

	if got := NormalizeLogicalPath("wsttp/pg/B.proto", false); got != "wsttp/pg/B.proto" {
		t.Errorf("NormalizeLogicalPath with stripPG=false changed path: %q", got)
	}

	// Intra-repo collision when both a/pg/X.proto and a/X.proto exist
	_, cols, err := MapAndValidateIntraRepoPathsWithPG(
		[]string{"a/pg/X.proto", "a/X.proto"},
		nil,
		nil,
		true,
	)
	if !errors.Is(err, ErrIntraRepoPathConflict) || len(cols) != 1 || cols[0].MappedPath != "a/X.proto" {
		t.Fatalf("expected intra-repo collision on a/X.proto, got err=%v cols=%+v", err, cols)
	}

	// Order check: strip_prefixes -> path_rewrites -> StripPGPathSegment
	mapped, err := ApplyIntraRepoPathMappingWithPG("legacy/src/pg/Foo.proto", []string{"legacy/"}, map[string]string{"src/": "svc/"}, true)
	if err != nil || mapped != "svc/Foo.proto" {
		t.Fatalf("expected svc/Foo.proto, got %q (err=%v)", mapped, err)
	}

	// Pool-level post-strip collision guard:
	// 1. Pre-existing collision in both pre and post is NOT a new strip collision
	preExisting := CheckPoolStripPGCollisions([]PoolPathCandidate{
		{RepoID: 1, RepoName: "r1", PreStripPath: "a/Same.proto", PostStripPath: "a/Same.proto"},
		{RepoID: 2, RepoName: "r2", PreStripPath: "a/Same.proto", PostStripPath: "a/Same.proto"},
	})
	if len(preExisting) != 0 {
		t.Fatalf("expected 0 NEW strip collisions for pre-existing collision, got %+v", preExisting)
	}
	// 2. Collision introduced only because /pg was stripped MUST be caught
	newStripCols := CheckPoolStripPGCollisions([]PoolPathCandidate{
		{RepoID: 1, RepoName: "r1", PreStripPath: "a/pg/X.proto", PostStripPath: "a/X.proto"},
		{RepoID: 2, RepoName: "r2", PreStripPath: "a/X.proto", PostStripPath: "a/X.proto"},
	})
	if len(newStripCols) != 1 || newStripCols[0].PostStripPath != "a/X.proto" {
		t.Fatalf("expected 1 new strip collision on a/X.proto, got %+v", newStripCols)
	}
}

func TestRuleNormalizerV2_PackageDedup_QuotesSemicolon_AndSingleRepoEscapeHatch(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultGovernanceConfig()
	cfg.Global.DefaultSyntax = "auto"
	cfg.Repos["keep-clean-pg-repo"] = RepoGovernanceSpec{
		DisableStripPGSegment: true,
	}
	norm := NewRuleBasedNormalizer(&cfg)

	repo := models.GitRepo{
		ID:             11,
		Name:           "keep-clean-pg-repo",
		PlatformRepoID: 111,
		SourceMode:     models.GitSourceModePlatformShared,
	}
	raw := "package acme.v1;\npackage acme.v1;\nimport 'ws/wsttp/pg/Msg.proto'\nmessage Foo { optional string bar = 1; }\n"
	out, err := norm.Normalize(ctx, NormalizeInput{
		Repo:          repo,
		Files:         map[string][]byte{"ws/pg/Foo.proto": []byte(raw)},
		AuxConfigJSON: BuildEffectiveRepoAuxJSON(repo, &cfg),
	})
	if err != nil {
		t.Fatalf("Normalize failed: %v", err)
	}

	res := out.Files["ws/pg/Foo.proto"]
	clean := string(res.CleanContent)

	// Package dedup: only 1 active package declaration
	if strings.Count(clean, "\npackage acme.v1;") != 1 {
		t.Fatalf("expected exactly 1 active package acme.v1, got:\n%s", clean)
	}
	if !strings.Contains(clean, "// dedup") {
		t.Fatalf("expected // dedup comment on duplicate package, got:\n%s", clean)
	}
	// Single-quote + missing semicolon fixed + global /pg coordinate aligned in import!
	if !strings.Contains(clean, `import "ws/wsttp/Msg.proto";`) {
		t.Fatalf("expected fixed import \"ws/wsttp/Msg.proto\";, got:\n%s", clean)
	}
	// Escape hatch disable_strip_pg_segment keeps clean_rel_path with pg/, while global coordinate stays without /pg!
	if res.CleanRelPath != "ws/pg/Foo.proto" {
		t.Fatalf("expected CleanRelPath to keep ws/pg/Foo.proto via escape hatch, got %q", res.CleanRelPath)
	}
	// Auto syntax fallback for proto3 when no uService/required/default
	if !strings.Contains(clean, `syntax = "proto3";`) {
		t.Fatalf("expected auto fallback to proto3, got:\n%s", clean)
	}

	// R0 subsystem and repo pattern exclusion check
	preset := ProductionUServicePresetConfig()
	if !ShouldExcludeRepoByR0("3M", "3m-mirror", preset.Global) {
		t.Fatalf("expected subsystem 3M to be excluded by R0")
	}
	if ShouldExcludeRepoByR0("ws", "wsttp-repo", preset.Global) {
		t.Fatalf("expected normal subsystem ws not to be excluded by R0")
	}
}
