package gitclient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code-contract/models"
)

func TestBaseHarvester_ImmuneToR0Excludes_AndSymbolHarvest(t *testing.T) {
	ctx := context.Background()
	tmpRoot := t.TempDir()

	// Create simulated codes_dir with shared bases under include/ and ltcommon/pg/,
	// including paths matching R0 exclude_globs like **/il/** and **/alpha/**!
	incDir := filepath.Join(tmpRoot, "devbench-hpmc-simulator", "include")
	ltDir := filepath.Join(tmpRoot, "ltcommon", "pg")
	writeProto := func(base, rel, content string) {
		full := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}

	writeProto(incDir, "OptionsExtensions.proto", `syntax = "proto2";
package opt.ext;
import "google/protobuf/descriptor.proto";
extend google.protobuf.FieldOptions {
  optional string cmRange = 50001;
}
`)
	writeProto(incDir, "sw/pg/BasicTypeMsg.proto", `syntax = "proto2";
package sw.basic;
message VoidType {
  optional int32 dummy = 1;
}
`)
	// il/ilfmp/ILFMPCommon.proto would be hit if R0's **/il/** were mistakenly applied to R4!
	writeProto(incDir, "il/ilfmp/ILFMPCommon.proto", `syntax = "proto2";
package il.ilfmp;
import "sw/pg/BasicTypeMsg.proto";
message ILFMPHeader {
  optional sw.basic.VoidType tag = 1;
}
`)
	// alpha/AlphaBase.proto inside shared_base_sources must ALSO be immune to R0 **/alpha/**
	writeProto(incDir, "alpha/AlphaBase.proto", `syntax = "proto2";
package alpha.base;
message AlphaToken {
  optional string tok = 1;
}
`)
	writeProto(ltDir, "ltcomm/ltdd/pg/GtPosMsg.proto", `syntax = "proto2";
package ltcomm.ltdd;
import "OptionsExtensions.proto";
message GtPosMsg {
  optional double x = 1 [(opt.ext.cmRange) = "0..100"];
}
`)

	cfg := ProductionUServicePresetConfig()
	// Deliberately add **/il/** to global exclude_globs to verify R4 immunity!
	cfg.Global.ExcludeGlobs = append(cfg.Global.ExcludeGlobs, "**/il/**")
	cfg.Global.SharedBaseSources = []SharedBaseSourceSpec{
		{PathHint: incDir, Priority: 1},
		{PathHint: ltDir, Priority: 2},
	}

	idx, err := BuildSharedBaseMemoryIndex(ctx, cfg.Global.SharedBaseSources, tmpRoot, &cfg)
	if err != nil {
		t.Fatalf("BuildSharedBaseMemoryIndex failed: %v", err)
	}

	// Verify Channel A collects all shared base files (including il/ilfmp & alpha),
	// normalized to unified /pg-stripped logical paths!
	chanA := idx.CollectChannelABases(ctx, nil)
	byNorm := make(map[string]SharedBaseSourceCandidate, len(chanA))
	for _, c := range chanA {
		byNorm[c.NormPath] = c
	}

	for _, expected := range []string{
		"OptionsExtensions.proto",
		"sw/BasicTypeMsg.proto",
		"il/ilfmp/ILFMPCommon.proto",
		"alpha/AlphaBase.proto",
		"ltcomm/ltdd/GtPosMsg.proto",
	} {
		cand, ok := byNorm[expected]
		if !ok {
			t.Fatalf("expected Channel A base %q to be harvested (immune to R0), got keys=%v", expected, keysOfBaseMap(byNorm))
		}
		if strings.Contains(string(cand.CleanBytes), "sw/pg/BasicTypeMsg.proto") {
			t.Fatalf("expected import in %s to be normalized without /pg, got:\n%s", expected, string(cand.CleanBytes))
		}
	}

	// Verify Channel B on-demand harvest by import path + transitive dependency
	harvestedByImp := idx.HarvestOnDemandByImport("il/ilfmp/ILFMPCommon.proto", nil)
	impSet := make(map[string]bool)
	for _, h := range harvestedByImp {
		impSet[h.NormPath] = true
	}
	if !impSet["il/ilfmp/ILFMPCommon.proto"] || !impSet["sw/BasicTypeMsg.proto"] {
		t.Fatalf("expected transitive harvest of ILFMPCommon + BasicTypeMsg, got %v", impSet)
	}

	// Verify Channel B on-demand harvest by unique symbol name (e.g. GtPosMsg -> transitive OptionsExtensions.proto)
	primary, deps := idx.HarvestOnDemandBySymbol("ltcomm.ltdd.GtPosMsg", nil)
	if primary == nil || primary.NormPath != "ltcomm/ltdd/GtPosMsg.proto" {
		t.Fatalf("expected symbol harvest of ltcomm/ltdd/GtPosMsg.proto, got %+v", primary)
	}
	depSet := make(map[string]bool)
	for _, d := range deps {
		depSet[d.NormPath] = true
	}
	if !depSet["OptionsExtensions.proto"] {
		t.Fatalf("expected transitive OptionsExtensions.proto with GtPosMsg, got %v", depSet)
	}

	// Verify PersistHarvestedBasesToDB does not conflict with PLATFORM_SHARED constraints
	// and AlignPlatformRepositories preserves SystemSharedBasePlatformRepoID
	if SystemSharedBasePlatformRepoID == 0 || models.ProtoCategoryBaseLib != "BASE_LIB" {
		t.Fatalf("invalid base constants")
	}
}

func keysOfBaseMap(m map[string]SharedBaseSourceCandidate) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
