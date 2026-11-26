package transport

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"code-contract/pkg/agentproto"
)

var (
	// ErrDriverAlreadyRegistered 驱动重复注册错误
	ErrDriverAlreadyRegistered = errors.New("transport: driver already registered")
	// ErrDriverNotFound 驱动未找到错误
	ErrDriverNotFound = errors.New("transport: driver not found")
)

// Registry 线程安全传输驱动注册表与生命周期分发器 (ADR-22 v2.0)
type Registry struct {
	mu      sync.RWMutex
	drivers map[string]Driver
	ordered []string
}

// NewRegistry 创建空注册表
func NewRegistry() *Registry {
	return &Registry{
		drivers: make(map[string]Driver),
	}
}

// Register 注册传输驱动
func (r *Registry) Register(d Driver) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	info := d.Info()
	if _, exists := r.drivers[info.Name]; exists {
		return fmt.Errorf("%w: %s", ErrDriverAlreadyRegistered, info.Name)
	}
	r.drivers[info.Name] = d
	r.ordered = append(r.ordered, info.Name)
	return nil
}

// Get 根据名称获取驱动
func (r *Registry) Get(name string) (Driver, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.drivers[name]
	return d, ok
}

// List 返回所有已注册的驱动列表（按注册顺序）
func (r *Registry) List() []Driver {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Driver, 0, len(r.ordered))
	for _, name := range r.ordered {
		out = append(out, r.drivers[name])
	}
	return out
}

// Capabilities 返回各驱动实时就绪状态，供 HandshakeResp 上报
func (r *Registry) Capabilities() []agentproto.TransportDriverCapability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	caps := make([]agentproto.TransportDriverCapability, 0, len(r.ordered))
	for _, name := range r.ordered {
		d := r.drivers[name]
		info := d.Info()
		avail, detail := d.IsAvailable()
		caps = append(caps, agentproto.TransportDriverCapability{
			Name:          info.Name,
			Version:       info.Version,
			Transports:    info.Transports,
			TransportCode: info.TransportCode,
			Capabilities:  info.Capabilities,
			Available:     avail,
			Detail:        detail,
		})
	}
	return caps
}

// StartCapture 将采集规格分发给可用驱动，单驱动非致命不可用时跳过而不拖垮可用驱动
func (r *Registry) StartCapture(ctx context.Context, spec CaptureSpec) error {
	drivers := r.List()

	var firstErr error
	for _, d := range drivers {
		avail, _ := d.IsAvailable()
		if !avail {
			continue
		}
		if err := d.StartCapture(ctx, spec); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// StopCapture 停止指定会话/任务全部驱动的采集
func (r *Registry) StopCapture(ctx context.Context, sessionOrTaskID uint64) error {
	drivers := r.List()

	var firstErr error
	for _, d := range drivers {
		if err := d.StopCapture(ctx, sessionOrTaskID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// RemoveTarget 从所有驱动中移除指定进程
func (r *Registry) RemoveTarget(sessionOrTaskID uint64, processID uint64) error {
	drivers := r.List()

	var firstErr error
	for _, d := range drivers {
		if err := d.RemoveTarget(sessionOrTaskID, processID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ResetAll 幂等重置所有已注册驱动（清理活跃状态但不永久关闭，支持 PurgeAll 后继续复用）
func (r *Registry) ResetAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var firstErr error
	for _, name := range r.ordered {
		d := r.drivers[name]
		var err error
		if rd, ok := d.(ResettableDriver); ok {
			err = rd.Reset()
		} else {
			err = d.Close()
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// CloseAll 幂等关闭所有已注册驱动（进程退出永久清理）
func (r *Registry) CloseAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var firstErr error
	for _, name := range r.ordered {
		d := r.drivers[name]
		if err := d.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
