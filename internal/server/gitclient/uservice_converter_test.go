package gitclient

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"code-contract/models"
)

func TestUServiceConverter_SyntaxCommentAware_ImportsAndMetadata(t *testing.T) {
	ctx := context.Background()
	defCfg := DefaultGovernanceConfig()
	defCfg.Global.DefaultSyntax = "auto"
	norm := NewRuleBasedNormalizer(&defCfg)

	if norm.Version() != "rule-normalizer/v2" {
		t.Fatalf("expected normalizer version rule-normalizer/v2, got %s", norm.Version())
	}

	raw := `// 文件注释中提到 uService FakeInLineComment { interface IgnoreMe(Req); }
/*
   块注释中提到
   uService FakeInBlockComment {
       interface IgnoreBlock(Req) return (Rep);
   }
*/
package ws.wsttp;

import 'sw/pg/CommonMsg.proto'
import "ws/wsttp/pg/WSTTPMsg.proto";

uService PSRSMCImpl
{
    import "ws/wsttp/pg/ExtraInside.proto";
    import 'ws/wsttp/pg/WSTTPMsg.proto'
    interface QueryStatus(QueryReq) return (QueryRep) (timeout:5000);
    interface MultiLineCall(
        LongReq
    ) return (
        LongRep
    ) (timeout:3000);
    interface FireEvent(EventMsg) (event);
    interface NotifyOnly(NotifyMsg);
    interface UseVoid(VoidType) return (VoidType);
    unrecognized_directive foo_bar;
}
`
	repo := models.GitRepo{
		ID:             1,
		Name:           "wsttp-repo",
		PlatformRepoID: 101,
		SourceMode:     models.GitSourceModePlatformShared,
	}
	aux := BuildEffectiveRepoAuxJSON(repo, &defCfg)
	out, err := norm.Normalize(ctx, NormalizeInput{
		Repo: repo,
		Files: map[string][]byte{
			"pg/PSRSMCImpl.proto": []byte(raw),
		},
		AuxConfigJSON: aux,
	})
	if err != nil {
		t.Fatalf("Normalize failed: %v", err)
	}

	res := out.Files["pg/PSRSMCImpl.proto"]
	if !res.IsUServiceSource {
		t.Fatalf("expected PSRSMCImpl.proto (without Service in filename) to be identified as uService source")
	}
	if res.CleanRelPath != "PSRSMCImpl.proto" {
		t.Fatalf("expected flattened CleanRelPath PSRSMCImpl.proto, got %q", res.CleanRelPath)
	}
	clean := string(res.CleanContent)

	// 1. auto default_syntax for uService -> proto2
	if !strings.Contains(clean, `syntax = "proto2";`) {
		t.Fatalf("expected syntax = \"proto2\"; for uService, got:\n%s", clean)
	}
	// 2. Comments containing uService must remain intact and NOT converted to service
	if !strings.Contains(clean, "uService FakeInLineComment") || !strings.Contains(clean, "uService FakeInBlockComment") {
		t.Fatalf("comments were unexpectedly modified:\n%s", clean)
	}
	// 3. uService -> service
	if !strings.Contains(clean, "service PSRSMCImpl") {
		t.Fatalf("expected service PSRSMCImpl, got:\n%s", clean)
	}
	// 4. interface -> rpc & metadata comments
	if !strings.Contains(clean, "rpc QueryStatus(QueryReq) returns (QueryRep); // @uservice: timeout:5000") {
		t.Fatalf("expected converted QueryStatus rpc with @uservice comment, got:\n%s", clean)
	}
	if !strings.Contains(clean, "rpc MultiLineCall(LongReq) returns (LongRep); // @uservice: timeout:3000") {
		t.Fatalf("expected converted multiline interface, got:\n%s", clean)
	}
	if !strings.Contains(clean, "rpc FireEvent(EventMsg) returns (google.protobuf.Empty); // @uservice: event") {
		t.Fatalf("expected oneway event rpc with Empty, got:\n%s", clean)
	}
	if !strings.Contains(clean, "rpc NotifyOnly(NotifyMsg) returns (google.protobuf.Empty);") {
		t.Fatalf("expected oneway rpc with Empty, got:\n%s", clean)
	}
	// 5. Hoisted block imports, deduplicated, single-quoted/semicolon-fixed, and /pg stripped!
	if strings.Count(clean, `import "ws/wsttp/WSTTPMsg.proto";`) != 1 {
		t.Fatalf("expected deduplicated single import ws/wsttp/WSTTPMsg.proto, got:\n%s", clean)
	}
	for _, expectedImp := range []string{
		`import "sw/CommonMsg.proto";`,
		`import "ws/wsttp/ExtraInside.proto";`,
		`import "google/protobuf/empty.proto";`,
		`import "sw/BasicTypeMsg.proto";`,
	} {
		if !strings.Contains(clean, expectedImp) {
			t.Fatalf("expected %s in cleaned proto:\n%s", expectedImp, clean)
		}
	}
	// 6. Unrecognized statement protected as // UNCONVERTED + WARNING diag
	if !strings.Contains(clean, "// UNCONVERTED: unrecognized_directive foo_bar;") {
		t.Fatalf("expected UNCONVERTED comment protection, got:\n%s", clean)
	}
	hasWarn := false
	for _, d := range res.Diagnostics {
		if d.Severity == "WARNING" && d.RuleID == NormRuleUServiceConvert {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Fatalf("expected WARNING diagnostic from NORM-006, got %+v", res.Diagnostics)
	}

	// 7. Must pass ParseProtoSyntaxOnly!
	gate := ParseProtoSyntaxOnly("PSRSMCImpl.proto", res.CleanContent)
	if !gate.Passed {
		t.Fatalf("expected ParseProtoSyntaxOnly to pass, got %s\nclean:\n%s", gate.ErrorSummary, clean)
	}

	// 8. Verify line_map accuracy: rpc QueryStatus line maps back to original raw line of interface QueryStatus
	rawLines := strings.Split(raw, "\n")
	rawQueryLine := 0
	for idx, l := range rawLines {
		if strings.Contains(l, "interface QueryStatus") {
			rawQueryLine = idx + 1
			break
		}
	}
	cleanLines := strings.Split(clean, "\n")
	cleanQueryLine := 0
	for idx, l := range cleanLines {
		if strings.Contains(l, "rpc QueryStatus") {
			cleanQueryLine = idx + 1
			break
		}
	}
	if rawQueryLine == 0 || cleanQueryLine == 0 || res.LineMap[cleanQueryLine] != rawQueryLine {
		t.Fatalf("expected line_map[%d] == %d, got %d (lineMap=%v)", cleanQueryLine, rawQueryLine, res.LineMap[cleanQueryLine], res.LineMap)
	}

	// 9. Message-only file named *Service.proto must NOT be flagged as IsUServiceSource
	msgOnlyOut, err := norm.Normalize(ctx, NormalizeInput{
		Repo: repo,
		Files: map[string][]byte{
			"pg/OrderService.proto": []byte("package order.v1;\nmessage OrderServiceMsg { required string id = 1; }\n"),
		},
		AuxConfigJSON: aux,
	})
	if err != nil {
		t.Fatalf("Normalize msgOnly failed: %v", err)
	}
	msgRes := msgOnlyOut.Files["pg/OrderService.proto"]
	if msgRes.IsUServiceSource {
		t.Fatalf("OrderService.proto without uService declaration must have IsUServiceSource=false")
	}
	if !strings.Contains(string(msgRes.CleanContent), `syntax = "proto2";`) {
		t.Fatalf("expected auto syntax=proto2 due to required modifier, got:\n%s", string(msgRes.CleanContent))
	}
}

func TestUServiceConverter_ImplicitImportsDisabledStripPG(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultGovernanceConfig()
	stripOff := false
	cfg.Global.StripPGSegment = &stripOff
	norm := NewRuleBasedNormalizer(&cfg)

	repo := models.GitRepo{ID: 2, Name: "r", PlatformRepoID: 102, SourceMode: models.GitSourceModePlatformShared}
	raw := "uService DemoSvc {\n  interface Ping(VoidType) return (VoidType);\n}\n"
	out, err := norm.Normalize(ctx, NormalizeInput{
		Repo:          repo,
		Files:         map[string][]byte{"pg/DemoSvc.proto": []byte(raw)},
		AuxConfigJSON: BuildEffectiveRepoAuxJSON(repo, &cfg),
	})
	if err != nil {
		t.Fatalf("Normalize failed: %v", err)
	}
	clean := string(out.Files["pg/DemoSvc.proto"].CleanContent)
	if !strings.Contains(clean, `import "sw/pg/BasicTypeMsg.proto";`) {
		t.Fatalf("when strip_pg_segment=false, implicit VoidType import must stay sw/pg/BasicTypeMsg.proto, got:\n%s", clean)
	}
	if out.Files["pg/DemoSvc.proto"].CleanRelPath != "pg/DemoSvc.proto" {
		t.Fatalf("when strip_pg_segment=false, CleanRelPath must keep pg/, got %q", out.Files["pg/DemoSvc.proto"].CleanRelPath)
	}
}

func TestUServiceConverter_309SampleShapes_AndLineMap(t *testing.T) {
	ctx := context.Background()
	cfg := ProductionUServicePresetConfig()
	norm := NewRuleBasedNormalizer(&cfg)
	repo := models.GitRepo{ID: 99, Name: "corpus-sim", PlatformRepoID: 999, SourceMode: models.GitSourceModePlatformShared}
	aux := BuildEffectiveRepoAuxJSON(repo, &cfg)

	files := make(map[string][]byte, 309)
	for i := 0; i < 309; i++ {
		var body string
		switch i % 5 {
		case 0:
			// Standard uService with inside import + timeout + VoidType
			body = fmt.Sprintf("package svc.p%d;\nuService Svc%d {\n  import \"common/pg/Types%d.proto\";\n  interface Call%d(VoidType) return (Rep%d) (timeout:1000);\n}\n", i, i, i, i, i)
		case 1:
			// Non-*Service filename + multiline interface + single quotes without semicolon
			body = fmt.Sprintf("/* uService Fake {} */\nimport 'sub/pg/Sub%d.proto'\nuService Impl%d\n{\n  interface Run%d(\n    Req%d\n  ) return (\n    Rep%d\n  ) (timeout:2500);\n}\n", i, i, i, i, i)
		case 2:
			// Oneway event + duplicate package
			body = fmt.Sprintf("package dup.p%d;\npackage dup.p%d;\nuService EvtSvc%d {\n  interface OnEvt%d(EvtReq%d) (event);\n  interface OneWay%d(OneReq%d);\n}\n", i, i, i, i, i, i, i)
		case 3:
			// C preprocessor + BOM + CRLF + required field in message alongside uService
			body = fmt.Sprintf("\xEF\xBB\xBF#ifndef H_%d\r\n#define H_%d\r\npackage mix.p%d;\r\nmessage Req%d { required int32 id = 1 [default = 1]; }\r\nuService MixSvc%d {\r\n  interface Do%d(Req%d) return (VoidType);\r\n}\r\n#endif\r\n", i, i, i, i, i, i, i)
		case 4:
			// Weak/public block import + inline block comment
			body = fmt.Sprintf("package pub.p%d;\nuService PubSvc%d {\n  import public \"base/pg/Base%d.proto\";\n  /* inline */ interface Get%d(GetReq%d) return (GetRep%d);\n}\n", i, i, i, i, i, i)
		}
		pathKey := fmt.Sprintf("sub%d/pg/SvcImpl%d.proto", i%10, i)
		if i%20 == 1 {
			pathKey = fmt.Sprintf("sub%d/pg/ReticleStagePhyT%d.proto", i%10, i)
		}
		files[pathKey] = []byte(body)
	}

	out, err := norm.Normalize(ctx, NormalizeInput{
		Repo:          repo,
		Files:         files,
		AuxConfigJSON: aux,
	})
	if err != nil {
		t.Fatalf("Normalize 309 samples failed: %v", err)
	}
	if len(out.Files) != 309 {
		t.Fatalf("expected 309 output files, got %d", len(out.Files))
	}

	passed := 0
	for rp, res := range out.Files {
		if !res.IsUServiceSource {
			t.Fatalf("expected %s IsUServiceSource=true", rp)
		}
		if strings.Contains(res.CleanRelPath, "/pg/") || strings.HasPrefix(res.CleanRelPath, "pg/") {
			t.Fatalf("expected /pg stripped from CleanRelPath, got %q", res.CleanRelPath)
		}
		gate := ParseProtoSyntaxOnly(res.CleanRelPath, res.CleanContent)
		if !gate.Passed {
			t.Fatalf("sample %s failed ParseProtoSyntaxOnly: %s\nclean:\n%s", rp, gate.ErrorSummary, string(res.CleanContent))
		}
		passed++
	}
	if passed != 309 {
		t.Fatalf("expected 309/309 syntax pass, got %d", passed)
	}
}
