package transport

import (
	"context"
	"sync"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
	"code-contract/pkg/ebpf"
)

// TCPDriver 内置 Socket eBPF TCP 传输驱动封装 (ADR-22 v2.0)
type TCPDriver struct {
	mu     sync.Mutex
	mgr    *ebpf.Manager
	active map[uint64]bool
	closed bool
}

// NewTCPDriver 构造 builtin-tcp 驱动
func NewTCPDriver(mgr *ebpf.Manager) *TCPDriver {
	return &TCPDriver{
		mgr:    mgr,
		active: make(map[uint64]bool),
	}
}

// Info 返回驱动元信息
func (d *TCPDriver) Info() DriverInfo {
	return DriverInfo{
		Name:          "builtin-tcp",
		Version:       "1.0.0",
		Transports:    []string{"tcp"},
		TransportCode: crosrpc.TransportTCP,
		Capabilities:  agentproto.CapDiscover | agentproto.CapCapture | agentproto.CapInject | agentproto.CapStub,
	}
}

// IsAvailable 返回宿主机运行权限
func (d *TCPDriver) IsAvailable() (bool, string) {
	if d.mgr == nil {
		return false, "ebpf manager not initialized"
	}
	hasCaps, _, detail := d.mgr.CheckCapabilities()
	return hasCaps, detail
}

// StartCapture 下发采集规格
func (d *TCPDriver) StartCapture(ctx context.Context, spec CaptureSpec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.active[spec.SessionOrTaskID] = true
	return nil
}

// StopCapture 停止采集
func (d *TCPDriver) StopCapture(ctx context.Context, sessionOrTaskID uint64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.active, sessionOrTaskID)
	return nil
}

// RemoveTarget 移除单进程监控
func (d *TCPDriver) RemoveTarget(sessionOrTaskID uint64, processID uint64) error {
	return nil
}

// Close 幂等自洁关闭
func (d *TCPDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	clear(d.active)
	return nil
}
