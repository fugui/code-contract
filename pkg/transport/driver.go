package transport

import (
	"context"

	"code-contract/pkg/agentproto"
)

// DriverInfo 传输驱动基础元信息 (ADR-22 v2.0)
type DriverInfo struct {
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Transports    []string `json:"transports"`
	TransportCode uint8    `json:"transport_code"`
	Capabilities  uint32   `json:"capabilities"`
}

// TargetProcess 描述单个被监控的目标进程与下游配置
type TargetProcess struct {
	ProcessID   uint64                          `json:"process_id"`
	PID         uint32                          `json:"pid"`
	ListenPort  uint16                          `json:"listen_port"`
	CmdPattern  string                          `json:"cmd_pattern"`
	ExeName     string                          `json:"exe_name"`
	Downstreams []agentproto.DownstreamEndpoint `json:"downstreams,omitempty"`
}

// CaptureSpec 流量采集下发规格
type CaptureSpec struct {
	SessionOrTaskID uint64          `json:"session_or_task_id"`
	RefKind         uint8           `json:"ref_kind"`
	Processes       []TargetProcess `json:"processes"`
	Sink            FrameSink       `json:"-"`
}

// FrameSink 采集到的二进制 CROS RPC 流量帧回调信宿
type FrameSink interface {
	PushFrame(h agentproto.EnvelopeHeader, payload []byte) error
}

// FrameSinkFunc 函数式适配器
type FrameSinkFunc func(h agentproto.EnvelopeHeader, payload []byte) error

// PushFrame 实现 FrameSink 接口
func (f FrameSinkFunc) PushFrame(h agentproto.EnvelopeHeader, payload []byte) error {
	return f(h, payload)
}

// Driver 纯 Go 可插拔传输驱动核心契约 (ADR-22 v2.0)
type Driver interface {
	// Info 返回驱动元信息与支持的传输协议
	Info() DriverInfo

	// IsAvailable 探测当前宿主机运行环境是否满足该驱动条件，不可用时返回具体说明
	IsAvailable() (bool, string)

	// StartCapture 下发采集任务并为目标进程挂载探针
	StartCapture(ctx context.Context, spec CaptureSpec) error

	// StopCapture 停止指定会话/任务的采集并卸载相关探针
	StopCapture(ctx context.Context, sessionOrTaskID uint64) error

	// RemoveTarget 单独移除某个进程的监控
	RemoveTarget(sessionOrTaskID uint64, processID uint64) error

	// Close 释放驱动内部持有的一切句柄与资源（五出口自洁）
	Close() error
}
