package transport_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
	"code-contract/pkg/transport"
)

type mockDriver struct {
	name          string
	available     bool
	detail        string
	captureCalled atomic.Int32
	stopCalled    atomic.Int32
	removeCalled  atomic.Int32
	closeCalled   atomic.Int32
}

func (m *mockDriver) Info() transport.DriverInfo {
	return transport.DriverInfo{
		Name:          m.name,
		Version:       "1.0.0",
		Transports:    []string{"mock"},
		TransportCode: 1,
		Capabilities:  agentproto.CapCapture,
	}
}

func (m *mockDriver) IsAvailable() (bool, string) {
	return m.available, m.detail
}

func (m *mockDriver) StartCapture(ctx context.Context, spec transport.CaptureSpec) error {
	m.captureCalled.Add(1)
	return nil
}

func (m *mockDriver) StopCapture(ctx context.Context, sessionOrTaskID uint64) error {
	m.stopCalled.Add(1)
	return nil
}

func (m *mockDriver) RemoveTarget(sessionOrTaskID uint64, processID uint64) error {
	m.removeCalled.Add(1)
	return nil
}

func (m *mockDriver) Close() error {
	m.closeCalled.Add(1)
	return nil
}

func TestRegistry_MultiDriverDispatch_AndFiveExitIdempotentClose(t *testing.T) {
	reg := transport.NewRegistry()

	d1 := &mockDriver{name: "driver-1", available: true}
	d2 := &mockDriver{name: "driver-2", available: false, detail: "missing capability"}

	if err := reg.Register(d1); err != nil {
		t.Fatalf("register d1 failed: %v", err)
	}
	if err := reg.Register(d2); err != nil {
		t.Fatalf("register d2 failed: %v", err)
	}

	// Duplicate registration error
	if err := reg.Register(d1); !errors.Is(err, transport.ErrDriverAlreadyRegistered) {
		t.Fatalf("expected ErrDriverAlreadyRegistered, got: %v", err)
	}

	// Capabilities report
	caps := reg.Capabilities()
	if len(caps) != 2 {
		t.Fatalf("expected 2 capabilities, got %d", len(caps))
	}
	if !caps[0].Available || caps[1].Available {
		t.Errorf("capabilities availability mismatch: %+v", caps)
	}

	// StartCapture skips unavailable d2 without error
	spec := transport.CaptureSpec{
		SessionOrTaskID: 1001,
		RefKind:         agentproto.RefKindRecordingSession,
	}
	if err := reg.StartCapture(context.Background(), spec); err != nil {
		t.Fatalf("StartCapture failed: %v", err)
	}
	if d1.captureCalled.Load() != 1 {
		t.Errorf("expected d1.captureCalled == 1, got %d", d1.captureCalled.Load())
	}
	if d2.captureCalled.Load() != 0 {
		t.Errorf("expected d2.captureCalled == 0 (unavailable), got %d", d2.captureCalled.Load())
	}

	// StopCapture & RemoveTarget dispatches to all
	_ = reg.RemoveTarget(1001, 10)
	_ = reg.StopCapture(context.Background(), 1001)
	if d1.removeCalled.Load() != 1 || d1.stopCalled.Load() != 1 {
		t.Errorf("d1 remove/stop mismatch")
	}

	// CloseAll idempotent
	if err := reg.CloseAll(); err != nil {
		t.Fatalf("CloseAll failed: %v", err)
	}
	if err := reg.CloseAll(); err != nil {
		t.Fatalf("CloseAll second call failed: %v", err)
	}
	if d1.closeCalled.Load() != 2 || d2.closeCalled.Load() != 2 {
		t.Errorf("close calls mismatch")
	}
}

func TestTCPDriver_ZeroRegressionWrapping(t *testing.T) {
	driver := transport.NewTCPDriver(nil)
	info := driver.Info()
	if info.Name != "builtin-tcp" || info.TransportCode != crosrpc.TransportTCP {
		t.Fatalf("unexpected TCPDriver info: %+v", info)
	}
	avail, detail := driver.IsAvailable()
	if avail {
		t.Fatalf("expected nil manager to be unavailable")
	}
	if detail == "" {
		t.Fatalf("expected non-empty detail")
	}

	_ = driver.StartCapture(context.Background(), transport.CaptureSpec{SessionOrTaskID: 1})
	_ = driver.StopCapture(context.Background(), 1)
	_ = driver.RemoveTarget(1, 2)
	_ = driver.Close()
}
