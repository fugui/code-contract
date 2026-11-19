package csg_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"code-contract/internal/agent/csg"
	"code-contract/pkg/agentproto"
	"code-contract/pkg/csgdlink"
	"code-contract/pkg/csgmock"
)

func TestSimController_ClientInject_DynamicPortNoRestart(t *testing.T) {
	mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mockCSG.Start(); err != nil {
		t.Fatalf("mockCSG.Start failed: %v", err)
	}
	defer mockCSG.Close()

	mockCSG.SeedInstance(csgdlink.Instance{
		ClientID: 11,
		ProcID:   6001,
		Addr:     "tcp://ipv4:127.0.0.1:38888",
		Name64:   "TradeSvc-TradeSvc",
	})

	ctrl := csg.NewSimController()
	out, err := ctrl.Prepare(context.Background(), csg.SimPrepareInput{
		Strategy:       agentproto.CSGStrategyClientInject,
		CSGAddr:        mockCSG.Addr(),
		ListenPort:     20001, // stale SSOT port
		TargetServices: []string{"TradeSvc-TradeSvc"},
		Timeout:        2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Prepare CLIENT_INJECT failed: %v", err)
	}
	defer func() { _ = ctrl.Cleanup() }()

	if out.TargetDialAddr != "127.0.0.1:38888" {
		t.Fatalf("TargetDialAddr = %q, want 127.0.0.1:38888", out.TargetDialAddr)
	}
	if err := ctrl.Cleanup(); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	// Zero GET_BIND_ADDR / FREE_BIND_ADDR must be sent, and no lingering SUB remains.
	for _, st := range mockCSG.ObservedControlSubtypes() {
		if st == csgdlink.SubTypeGetBindAddr || st == csgdlink.SubTypeFreeBindAddr {
			t.Fatalf("FORBIDDEN bind addr call observed: subtype=%d", st)
		}
	}
	if len(mockCSG.Snapshot().Clients) != 0 {
		t.Fatalf("expected 0 dangling CSG subscriptions after CLIENT_INJECT, got %+v", mockCSG.Snapshot().Clients)
	}
}

func TestSimController_ClientInject_PureMeshReturns422(t *testing.T) {
	mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mockCSG.Start(); err != nil {
		t.Fatalf("mockCSG.Start failed: %v", err)
	}
	defer mockCSG.Close()

	mockCSG.SeedInstance(csgdlink.Instance{
		ClientID: 12,
		ProcID:   6002,
		Addr:     "dophi_mesh://ipv4:127.0.0.1:39001",
		Name64:   "MeshOnlySvc-MeshOnlySvc",
	})

	ctrl := csg.NewSimController()
	_, err := ctrl.Prepare(context.Background(), csg.SimPrepareInput{
		Strategy:       agentproto.CSGStrategyClientInject,
		CSGAddr:        mockCSG.Addr(),
		TargetServices: []string{"MeshOnlySvc-MeshOnlySvc"},
		Timeout:        2 * time.Second,
	})
	if !errors.Is(err, csg.ErrDataPlaneMeshUnsupported) {
		t.Fatalf("expected ErrDataPlaneMeshUnsupported (422), got %v", err)
	}
	rep := ctrl.Report()
	if rep == nil || rep.ErrorCode != csg.ErrCodeDataPlaneMeshUnsupported {
		t.Fatalf("expected error_code %s, got %+v", csg.ErrCodeDataPlaneMeshUnsupported, rep)
	}
}

func TestSimController_PubStub_ConflictReturns409_AbsenceZeroBindAddrAnd5ExitCleanup(t *testing.T) {
	t.Run("ConflictReturns409WithoutCallingPub", func(t *testing.T) {
		mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
		if err := mockCSG.Start(); err != nil {
			t.Fatalf("mockCSG.Start failed: %v", err)
		}
		defer mockCSG.Close()

		mockCSG.SeedInstance(csgdlink.Instance{
			ClientID: 21,
			ProcID:   7001,
			Addr:     "tcp://ipv4:127.0.0.1:28001",
			Name64:   "DownstreamSvc-DownstreamSvc",
		})

		ctrl := csg.NewSimController()
		_, err := ctrl.Prepare(context.Background(), csg.SimPrepareInput{
			Strategy:       agentproto.CSGStrategyPubStub,
			CSGAddr:        mockCSG.Addr(),
			MockDataPort:   18999,
			TargetServices: []string{"DownstreamSvc-DownstreamSvc"},
			Timeout:        2 * time.Second,
		})
		if !errors.Is(err, csg.ErrServiceAlreadyPublished) {
			t.Fatalf("expected ErrServiceAlreadyPublished (409), got %v", err)
		}
		for _, st := range mockCSG.ObservedControlSubtypes() {
			if st == csgdlink.SubTypePub || st == csgdlink.SubTypeGetBindAddr || st == csgdlink.SubTypeFreeBindAddr {
				t.Fatalf("unexpected mutation frame %d after conflict 409", st)
			}
		}
	})

	t.Run("AbsencePublishesAndCleansUpIdempotently", func(t *testing.T) {
		mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
		if err := mockCSG.Start(); err != nil {
			t.Fatalf("mockCSG.Start failed: %v", err)
		}
		defer mockCSG.Close()

		ctrl := csg.NewSimController()
		_, err := ctrl.Prepare(context.Background(), csg.SimPrepareInput{
			Strategy:       agentproto.CSGStrategyPubStub,
			CSGAddr:        mockCSG.Addr(),
			MockDataPort:   18999,
			TargetServices: []string{"DownstreamSvc-DownstreamSvc"},
			Timeout:        2 * time.Second,
		})
		if err != nil {
			t.Fatalf("Prepare PUB_STUB failed: %v", err)
		}

		snap := mockCSG.Snapshot()
		if len(snap.Servers) != 1 || snap.Servers[0].ServerAddr != "tcp://ipv4:127.0.0.1:18999" {
			t.Fatalf("expected published stub on 18999, got %+v", snap.Servers)
		}
		for _, st := range mockCSG.ObservedControlSubtypes() {
			if st == csgdlink.SubTypeGetBindAddr || st == csgdlink.SubTypeFreeBindAddr {
				t.Fatalf("FORBIDDEN bind addr call observed in PUB_STUB: subtype=%d", st)
			}
		}

		// Idempotent cleanup across repeated exits (complete/cancel/purge/watchdog/signal)
		if err := ctrl.Cleanup(); err != nil {
			t.Fatalf("first Cleanup failed: %v", err)
		}
		if err := ctrl.Cleanup(); err != nil {
			t.Fatalf("second idempotent Cleanup failed: %v", err)
		}

		if len(mockCSG.Snapshot().Servers) != 0 {
			t.Fatalf("expected stub Unpub on Cleanup, remaining servers: %+v", mockCSG.Snapshot().Servers)
		}
		rep := ctrl.Report()
		if !rep.CleanupCompleted || len(rep.UnpublishedStubs) != 1 {
			t.Fatalf("unexpected PUB_STUB cleanup report: %+v", rep)
		}
	})
}

func TestSimController_MockServer_ZeroLeakAndSandboxReport(t *testing.T) {
	// Sentinel listener representing a real CSG server that MUST receive 0 connections.
	realCSG, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen sentinel realCSG failed: %v", err)
	}
	defer realCSG.Close()

	var realAccepted atomic.Int32
	go func() {
		for {
			c, aErr := realCSG.Accept()
			if aErr != nil {
				return
			}
			realAccepted.Add(1)
			_ = c.Close()
		}
	}()

	ctrl := csg.NewSimController()
	out, err := ctrl.Prepare(context.Background(), csg.SimPrepareInput{
		Strategy:     agentproto.CSGStrategyMockServer,
		CSGAddr:      realCSG.Addr().String(),
		MockDataPort: 19100,
		Snapshots: []agentproto.CSGSnapshotRecord{
			{
				ServiceName: "DownstreamSvc-DownstreamSvc",
				Addr:        "dophi_mesh://ipv4:10.0.0.9:33001",
				Transport:   "MESH",
			},
		},
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Prepare MOCK_SERVER failed: %v", err)
	}
	if out.MockCSGServer == nil {
		t.Fatalf("expected non-nil MockCSGServer")
	}

	// Route an SUT dlink connection into the in-process MockCSGServer via pipe (simulating EgressProxy mux)
	serverSide, clientSide := net.Pipe()
	go out.MockCSGServer.ServeConn(serverSide)

	dlinkCli := csgdlink.NewClient(clientSide)
	if _, err := dlinkCli.Register(2 * time.Second); err != nil {
		t.Fatalf("mock sandbox Register failed: %v", err)
	}
	if _, err := dlinkCli.ClientID(2 * time.Second); err != nil {
		t.Fatalf("mock sandbox ClientID failed: %v", err)
	}
	insts, err := dlinkCli.Sub("DownstreamSvc-DownstreamSvc", 2*time.Second)
	if err != nil {
		t.Fatalf("mock sandbox Sub failed: %v", err)
	}
	_ = dlinkCli.Close()

	if len(insts) != 1 || insts[0].Addr != "tcp://ipv4:10.0.0.9:33001" {
		t.Fatalf("expected Mesh->TCP rewritten addr in sandbox, got %+v", insts)
	}

	if err := ctrl.Cleanup(); err != nil {
		t.Fatalf("Cleanup MOCK_SERVER failed: %v", err)
	}
	if realAccepted.Load() != 0 {
		t.Fatalf("LEAK: sentinel realCSG received %d connections in MOCK_SERVER mode", realAccepted.Load())
	}
	rep := ctrl.Report()
	if !rep.CleanupCompleted || rep.RealCSGLeakCount != 0 || rep.MeshRewrites == 0 {
		t.Fatalf("unexpected sandbox report: %+v", rep)
	}
	if len(rep.SandboxSubscribedSvcs) != 1 || rep.SandboxSubscribedSvcs[0] != "DownstreamSvc-DownstreamSvc" {
		t.Fatalf("expected SandboxSubscribedSvcs [DownstreamSvc-DownstreamSvc], got %+v", rep.SandboxSubscribedSvcs)
	}
}

func TestSimController_MockServer_AllowsMeshToTCPRewriteWithout422(t *testing.T) {
	ctrl := csg.NewSimController()
	out, err := ctrl.Prepare(context.Background(), csg.SimPrepareInput{
		Strategy:     agentproto.CSGStrategyMockServer,
		CSGAddr:      "127.0.0.1:49999",
		MockDataPort: 19101,
		Snapshots: []agentproto.CSGSnapshotRecord{
			{
				ServiceName: "PureMeshSvc-PureMeshSvc",
				Addr:        "dophi_mesh://ipv4:192.168.1.50:41001",
				Transport:   "MESH",
			},
		},
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Prepare MOCK_SERVER failed: %v", err)
	}
	defer ctrl.Cleanup()

	serverSide, clientSide := net.Pipe()
	go out.MockCSGServer.ServeConn(serverSide)

	dlinkCli := csgdlink.NewClient(clientSide)
	_, _ = dlinkCli.Register(2 * time.Second)
	_, _ = dlinkCli.ClientID(2 * time.Second)

	var meshTrans [7]byte
	meshTrans[4] = 1 // ONLY MESH (Trans[1] == 0)

	insts, err := dlinkCli.SubWithTrans("PureMeshSvc-PureMeshSvc", meshTrans, 2*time.Second)
	if err != nil {
		t.Fatalf("SubWithTrans pure mesh failed: %v", err)
	}
	_ = dlinkCli.Close()

	if len(insts) != 1 || insts[0].Addr != "tcp://ipv4:192.168.1.50:41001" {
		t.Fatalf("expected Mesh->TCP rewritten address, got %+v", insts)
	}

	rep := ctrl.Report()
	if rep == nil {
		t.Fatalf("expected non-nil report")
	}
	if rep.ErrorCode != "" {
		t.Fatalf("expected ErrorCode to be empty, got %s: %s", rep.ErrorCode, rep.ErrorMessage)
	}
	if rep.MeshRewrites != 1 {
		t.Fatalf("expected MeshRewrites == 1, got %d", rep.MeshRewrites)
	}
}
