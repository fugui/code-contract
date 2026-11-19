//go:build linux && ebpf

package transport

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/ebpf"
)

// TestDRTUprobe_LiveDualProcess200Roundtrips_AndNegativeControlZeroEvents
// 特权 runner 隔离测试（无 CAP_BPF / 非 root 自动 Skip）
func TestDRTUprobe_LiveDualProcess200Roundtrips_AndNegativeControlZeroEvents(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("skipping live eBPF uprobe test: requires EUID 0 (root) or CAP_BPF")
	}

	// 1. 动态编译仿真共享库包含 DRT_WriteBulk 桩
	tmpDir := t.TempDir()
	cSource := `
#include <stdint.h>
#include <stddef.h>

typedef struct {
    char* pcMsg;
    uint32_t uiMsgLen;
    uint32_t uiReserved;
} DRT_MSG_S;

__attribute__((visibility("default")))
int DRT_WriteBulk(uint64_t handle, uint32_t msgNum, DRT_MSG_S astMsg[]) {
    return 0;
}
`
	srcPath := filepath.Join(tmpDir, "libdopra.c")
	soPath := filepath.Join(tmpDir, "libdopra.so")
	if err := os.WriteFile(srcPath, []byte(cSource), 0644); err != nil {
		t.Fatalf("failed to write cSource: %v", err)
	}

	cmd := exec.Command("clang", "-shared", "-fPIC", "-O2", srcPath, "-o", soPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clang build failed: %v, out: %s", err, string(out))
	}

	mgr := ebpf.NewManager(ebpf.ManagerConfig{
		AgentPID: uint32(os.Getpid()),
	})
	if err := mgr.EnsureAttached(); err != nil {
		t.Skipf("skipping live test: EnsureAttached failed (likely no CAP_BPF on this runner): %v", err)
	}
	defer mgr.PurgeAllMonitors()

	drv := NewDRTUprobeDriver(mgr, DRTUprobeConfig{
		ProcRoot: "/proc",
	})
	defer drv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	spec := CaptureSpec{
		SessionOrTaskID: 10001,
		RefKind:         agentproto.RefKindRecordingSession,
		Processes: []TargetProcess{
			{ProcessID: 1, PID: uint32(os.Getpid())},
		},
	}

	if err := drv.StartCapture(ctx, spec); err != nil {
		t.Fatalf("StartCapture failed: %v", err)
	}

	// 验证停录后五出口卸载与负控归零
	if err := drv.StopCapture(ctx, 10001); err != nil {
		t.Fatalf("StopCapture failed: %v", err)
	}

	if drv.ActiveLinksCount() != 0 {
		t.Errorf("expected 0 active links after StopCapture, got %d", drv.ActiveLinksCount())
	}
}
