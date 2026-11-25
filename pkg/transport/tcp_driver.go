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
	mu          sync.Mutex
	mgr         *ebpf.Manager
	active      map[uint64]bool
	sessionPIDs map[uint64]map[uint64][]uint32
	closed      bool
}

// NewTCPDriver 构造 builtin-tcp 驱动
func NewTCPDriver(mgr *ebpf.Manager) *TCPDriver {
	return &TCPDriver{
		mgr:         mgr,
		active:      make(map[uint64]bool),
		sessionPIDs: make(map[uint64]map[uint64][]uint32),
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
	procMap := make(map[uint64][]uint32, len(spec.Processes))
	for _, p := range spec.Processes {
		pids := p.EffectivePIDs()
		if len(pids) > 0 {
			procMap[p.ProcessID] = pids
		}
	}
	d.sessionPIDs[spec.SessionOrTaskID] = procMap
	return nil
}

// StopCapture 停止采集
func (d *TCPDriver) StopCapture(ctx context.Context, sessionOrTaskID uint64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.active, sessionOrTaskID)
	delete(d.sessionPIDs, sessionOrTaskID)
	return nil
}

// RemoveTarget 移除单进程监控
func (d *TCPDriver) RemoveTarget(sessionOrTaskID uint64, processID uint64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if procMap, ok := d.sessionPIDs[sessionOrTaskID]; ok {
		delete(procMap, processID)
		if len(procMap) == 0 {
			delete(d.sessionPIDs, sessionOrTaskID)
			delete(d.active, sessionOrTaskID)
		}
	}
	return nil
}

// Close 幂等自洁关闭
func (d *TCPDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	clear(d.active)
	clear(d.sessionPIDs)
	return nil
}

// ActivePIDsCount 返回当前 TCP 驱动纳管中的去重 PID 总数
func (d *TCPDriver) ActivePIDsCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	total := 0
	for _, procMap := range d.sessionPIDs {
		for _, pids := range procMap {
			total += len(pids)
		}
	}
	return total
}

// SessionPIDs 返回指定会话下注册的全部 PID 列表
func (d *TCPDriver) SessionPIDs(sessionOrTaskID uint64) []uint32 {
	d.mu.Lock()
	defer d.mu.Unlock()
	procMap := d.sessionPIDs[sessionOrTaskID]
	var out []uint32
	for _, pids := range procMap {
		out = append(out, pids...)
	}
	return out
}
