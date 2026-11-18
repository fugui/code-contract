package main

import (
	"os/exec"
	"strings"
	"testing"
)

// T-M13-10: 数据面 Agent 物理防腐红线依赖核验
// 断言 cmd/agent, pkg/crosrpc, pkg/agentproto, pkg/ebpf, pkg/replay 零 gorm、零 pgx、零 dynamicpb、零 internal/server 依赖
func TestAgentDependencies_NoGormNoDynamicPBNoServer(t *testing.T) {
	t.Run("CSGMock_NoEBPF", func(t *testing.T) {
		c := exec.Command("go", "list", "-deps", "./pkg/csgmock")
		c.Dir = "../.."
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("go list ./pkg/csgmock failed: %v\n%s", err, string(out))
		}
		if strings.Contains(string(out), "code-contract/pkg/ebpf") {
			t.Fatalf("FORBIDDEN dependency detected in pkg/csgmock: code-contract/pkg/ebpf")
		}
	})

	t.Run("RPCMon_NoProtobufRegistry", func(t *testing.T) {
		c := exec.Command("go", "list", "-deps", "./pkg/rpcmon")
		c.Dir = "../.."
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("go list ./pkg/rpcmon failed: %v\n%s", err, string(out))
		}
		if strings.Contains(string(out), "google.golang.org/protobuf") {
			t.Fatalf("FORBIDDEN dependency detected in pkg/rpcmon: google.golang.org/protobuf")
		}
	})

	t.Run("CSGDlink_PureStdlib", func(t *testing.T) {
		c := exec.Command("go", "list", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "-deps", "./pkg/csgdlink")
		c.Dir = "../.."
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("go list ./pkg/csgdlink failed: %v\n%s", err, string(out))
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && line != "code-contract/pkg/csgdlink" {
				t.Fatalf("FORBIDDEN non-stdlib dependency in pkg/csgdlink: %q", line)
			}
		}
	})

	cmd := exec.Command("go", "list", "-deps", "./cmd/agent", "./pkg/crosrpc", "./pkg/agentproto", "./pkg/ebpf", "./pkg/replay", "./pkg/csgdlink", "./pkg/rpcmon", "./pkg/csgmock")
	cmd.Dir = "../.." // code-contract root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps failed: %v, out: %s", err, string(out))
	}

	forbiddenKeywords := []string{
		"gorm.io",
		"pgx",
		"dynamicpb",
		"protoregistry",
		"code-contract/internal/server",
		"code-contract/models",
		"github.com/go-git/go-git",
		"github.com/go-git/go-billy",
		"github.com/bufbuild/protocompile",
		"sqlite",
	}

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		for _, kw := range forbiddenKeywords {
			if strings.Contains(trimmed, kw) {
				t.Errorf("Agent component forbidden dependency detected: %s (matched rule: %s)", trimmed, kw)
			}
		}
	}
}
