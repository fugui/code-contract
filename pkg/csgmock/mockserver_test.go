package csgmock_test

import (
	"context"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"code-contract/pkg/crosrpc"
	"code-contract/pkg/csgdlink"
	"code-contract/pkg/csgmock"
	"code-contract/pkg/rpcmon"
)

func TestMockCSGServer_E2E_PubSubCrosRPC_AndRpcmonAll(t *testing.T) {
	mock := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mock.Start(); err != nil {
		t.Fatalf("mock.Start failed: %v", err)
	}
	defer mock.Close()

	if err := mock.StartMonitor("127.0.0.1:0"); err != nil {
		t.Fatalf("mock.StartMonitor failed: %v", err)
	}

	// Start real CROS RPC business data-plane listener on loopback.
	bizLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen biz failed: %v", err)
	}
	defer bizLn.Close()

	go func() {
		conn, err := bizLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		pkt, err := crosrpc.ParsePacket(buf[:n])
		if err != nil || pkt.Req == nil {
			return
		}
		rsp := rpcmon.BuildCrosRspFrame(pkt.Comm.SNO, pkt.Req.ServiceName, pkt.Req.Operation, 0, []byte("ok"))
		_, _ = conn.Write(rsp)
	}()

	// Publisher publishes service to MockCSGServer.
	pubClient, err := csgdlink.Dial(mock.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("pub dial failed: %v", err)
	}
	defer pubClient.Close()
	if _, err := pubClient.Register(2 * time.Second); err != nil {
		t.Fatalf("pub register failed: %v", err)
	}
	if _, err := pubClient.ClientID(2 * time.Second); err != nil {
		t.Fatalf("pub clientID failed: %v", err)
	}
	svcAddr := "tcp://ipv4:" + bizLn.Addr().String()
	if err := pubClient.Pub("OrderService-OrderService", svcAddr, 2*time.Second); err != nil {
		t.Fatalf("pub failed: %v", err)
	}

	// Subscriber discovers via SUB.
	subClient, err := csgdlink.Dial(mock.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("sub dial failed: %v", err)
	}
	defer subClient.Close()
	if _, err := subClient.Register(2 * time.Second); err != nil {
		t.Fatalf("sub register failed: %v", err)
	}
	if _, err := subClient.ClientID(2 * time.Second); err != nil {
		t.Fatalf("sub clientID failed: %v", err)
	}
	insts, err := subClient.Sub("OrderService-OrderService", 2*time.Second)
	if err != nil {
		t.Fatalf("sub failed: %v", err)
	}
	if len(insts) != 1 || insts[0].Addr != svcAddr {
		t.Fatalf("expected 1 instance with addr %s, got %+v", svcAddr, insts)
	}

	// Direct CROS RPC call against discovered endpoint.
	targetHostPort, ok := csgdlink.ParseTCPTarget(insts[0].Addr)
	if !ok {
		t.Fatalf("ParseTCPTarget failed for %q", insts[0].Addr)
	}
	dataConn, err := net.DialTimeout("tcp", targetHostPort, 2*time.Second)
	if err != nil {
		t.Fatalf("dial biz failed: %v", err)
	}
	defer dataConn.Close()
	reqBytes := rpcmon.BuildCrosReqFrame(99, "OrderService-OrderService", "CreateOrder", []byte("req"))
	if _, err := dataConn.Write(reqBytes); err != nil {
		t.Fatalf("write biz req failed: %v", err)
	}
	rspBuf := make([]byte, 4096)
	_ = dataConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	rn, err := dataConn.Read(rspBuf)
	if err != nil {
		t.Fatalf("read biz rsp failed: %v", err)
	}
	rspPkt, err := crosrpc.ParsePacket(rspBuf[:rn])
	if err != nil || rspPkt.Rsp == nil || string(rspBuf[rspPkt.PayloadOffset:rn]) != "ok" {
		t.Fatalf("unexpected biz rsp: %+v err=%v", rspPkt, err)
	}

	// Monitor query via CrosTCP rpcmon.
	monBackend, err := rpcmon.NewCrosTCP(mock.MonitorAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("NewCrosTCP failed: %v", err)
	}
	defer monBackend.Close()

	rep, err := rpcmon.Fetch(context.Background(), monBackend, "all")
	if err != nil {
		t.Fatalf("rpcmon.Fetch failed: %v", err)
	}
	if rep.ServerNum != 1 || rep.ClientNum != 1 || len(rep.Ports) != 1 {
		t.Fatalf("unexpected monitor report: %+v", rep)
	}
	if rep.Ports[0].IPAddr != "127.0.0.1" || rep.Ports[0].Used != 1 {
		t.Fatalf("unexpected port usage: %+v", rep.Ports)
	}

	// Verify RPCMonitorService is resolvable via dlink Sub without inflating Snapshot().ServerNum().
	monLookupCli, err := csgdlink.Dial(mock.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("monLookupCli dial failed: %v", err)
	}
	defer monLookupCli.Close()
	_, _ = monLookupCli.Register(2 * time.Second)
	_, _ = monLookupCli.ClientID(2 * time.Second)
	monInsts, err := monLookupCli.Sub(rpcmon.ServiceName, 2*time.Second)
	_ = monLookupCli.Unsub(rpcmon.ServiceName, 2*time.Second)
	if err != nil {
		t.Fatalf("sub RPCMonitorService failed: %v", err)
	}
	if len(monInsts) != 1 || monInsts[0].Addr != "tcp://ipv4:"+mock.MonitorAddr() {
		t.Fatalf("expected RPCMonitorService addr tcp://ipv4:%s, got %+v", mock.MonitorAddr(), monInsts)
	}
	if mock.Snapshot().ServerNum() != 1 {
		t.Fatalf("expected ServerNum to remain 1 after transient monitor lookup, got %d", mock.Snapshot().ServerNum())
	}
}

func TestMockCSGServer_PidSuffixMatchAndMeshRewritePreservesAddr(t *testing.T) {
	mock := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mock.Start(); err != nil {
		t.Fatalf("mock.Start failed: %v", err)
	}
	defer mock.Close()

	// Verify Mesh-only SUB hook fires when subscriber asks ONLY for dophi_mesh (Trans[4]==1 && Trans[1]==0)
	var meshHookMu sync.Mutex
	var meshOnlySvcs []string
	mock.SetMeshOnlySubHook(func(serviceName string) {
		meshHookMu.Lock()
		defer meshHookMu.Unlock()
		meshOnlySvcs = append(meshOnlySvcs, serviceName)
	})

	pubClient, err := csgdlink.Dial(mock.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("pub dial failed: %v", err)
	}
	defer pubClient.Close()
	_, _ = pubClient.Register(2 * time.Second)
	_, _ = pubClient.ClientID(2 * time.Second)

	// Published with PID suffix "-21000" and dophi_mesh:// address
	if err := pubClient.Pub("InventorySvc-1-21000", "dophi_mesh://ipv4:127.0.0.1:19001", 2*time.Second); err != nil {
		t.Fatalf("pub failed: %v", err)
	}

	subClient, err := csgdlink.Dial(mock.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("sub dial failed: %v", err)
	}
	defer subClient.Close()
	_, _ = subClient.Register(2 * time.Second)
	cid, _ := subClient.ClientID(2 * time.Second)

	// Normal Sub strips/normalizes PID suffix when exact match misses
	insts, err := subClient.Sub("InventorySvc-1", 2*time.Second)
	if err != nil {
		t.Fatalf("sub failed: %v", err)
	}
	if len(insts) != 1 || insts[0].Addr != "dophi_mesh://ipv4:127.0.0.1:19001" {
		t.Fatalf("expected 1 instance dophi_mesh://ipv4:127.0.0.1:19001 via PID-suffix match, got %+v", insts)
	}

	// Enable Mesh -> TCP rewrite preserving IP:port, then re-SUB.
	mock.EnableMeshToTCPRewrite(true)
	insts, err = subClient.Sub("InventorySvc-1", 2*time.Second)
	if err != nil || len(insts) != 1 || insts[0].Addr != "tcp://ipv4:127.0.0.1:19001" {
		t.Fatalf("expected Mesh rewrite to tcp://ipv4:127.0.0.1:19001, got %+v err=%v", insts, err)
	}

	// Verify raw dlink SUB_ACK has uiSubLen == len(matched)*248 and Mesh-only SUB triggers hook.
	rawConn, err := net.DialTimeout("tcp", mock.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("raw dial failed: %v", err)
	}
	defer rawConn.Close()
	meshOnlyReq := csgdlink.SubRequest{
		ClientID: cid,
		Name:     "InventorySvc-1",
	}
	meshOnlyReq.Trans[4] = 1 // ONLY MESH
	body := csgdlink.EncodeSubRequest(meshOnlyReq)
	if err := csgdlink.WriteFrame(rawConn, csgdlink.NewRequestFrame(csgdlink.SubTypeSub, body, 55), body); err != nil {
		t.Fatalf("write mesh-only sub failed: %v", err)
	}
	ackFrame, ackBody, err := csgdlink.ReadFrame(rawConn)
	if err != nil {
		t.Fatalf("read sub ack failed: %v", err)
	}
	if ackFrame.SubType != csgdlink.SubTypeSubAck || len(ackBody) < 16 {
		t.Fatalf("unexpected ack frame: %+v len=%d", ackFrame, len(ackBody))
	}
	count := binary.BigEndian.Uint32(ackBody[0:4])
	uiSubLen := binary.BigEndian.Uint32(ackBody[8:12])
	if count != 1 || uiSubLen != count*csgdlink.ServiceInstLen {
		t.Fatalf("expected uiSubLen=%d, got count=%d uiSubLen=%d", count*csgdlink.ServiceInstLen, count, uiSubLen)
	}

	meshHookMu.Lock()
	defer meshHookMu.Unlock()
	if len(meshOnlySvcs) != 1 || meshOnlySvcs[0] != "InventorySvc-1" {
		t.Fatalf("expected MeshOnlySub hook to capture InventorySvc-1, got %+v", meshOnlySvcs)
	}
}

func TestMockCSGServer_CleanOnConnDrop_AndZeroRealLeak(t *testing.T) {
	mock := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mock.Start(); err != nil {
		t.Fatalf("mock.Start failed: %v", err)
	}
	defer mock.Close()

	pubClient, err := csgdlink.Dial(mock.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	_, _ = pubClient.Register(2 * time.Second)
	_, _ = pubClient.ClientID(2 * time.Second)
	if err := pubClient.Pub("TempSvc-TempSvc", "tcp://ipv4:127.0.0.1:19999", 2*time.Second); err != nil {
		t.Fatalf("pub failed: %v", err)
	}
	if snap := mock.Snapshot(); len(snap.Servers) != 1 {
		t.Fatalf("expected 1 server while connected, got %d", len(snap.Servers))
	}

	// Abruptly close publisher connection without UNPUB.
	_ = pubClient.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := mock.Snapshot()
		if len(snap.Servers) == 0 && len(snap.Clients) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected MockCSGServer to clean up reg & sessions on conn drop, got %+v", mock.Snapshot())
}

func TestMockCSGServer_MeshOnlySub_ReceivesRewrittenTCPAndStubInstance(t *testing.T) {
	mock := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mock.Start(); err != nil {
		t.Fatalf("mock.Start failed: %v", err)
	}
	defer mock.Close()

	// 1. Seed a pure dophi_mesh instance
	mock.SeedInstance(csgdlink.Instance{
		ClientID: 10,
		ProcID:   1001,
		Addr:     "dophi_mesh://ipv4:10.0.0.1:28001",
		Name64:   "MeshSvc-MeshSvc",
	})

	// 2. Publish a local stub directly on TCP
	mock.SeedInstance(csgdlink.Instance{
		ClientID: 20,
		ProcID:   2001,
		Addr:     "tcp://ipv4:127.0.0.1:39001",
		Name64:   "StubSvc-StubSvc",
	})

	mock.EnableMeshToTCPRewrite(true)

	subClient, err := csgdlink.Dial(mock.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("subClient dial failed: %v", err)
	}
	defer subClient.Close()
	_, _ = subClient.Register(2 * time.Second)
	_, _ = subClient.ClientID(2 * time.Second)

	var meshTrans [7]byte
	meshTrans[4] = 1 // Only MESH

	// Pure Mesh SUB for MeshSvc
	insts1, err := subClient.SubWithTrans("MeshSvc-MeshSvc", meshTrans, 2*time.Second)
	if err != nil {
		t.Fatalf("SubWithTrans for MeshSvc failed: %v", err)
	}
	if len(insts1) != 1 {
		t.Fatalf("expected 1 instance for MeshSvc, got %d", len(insts1))
	}
	if insts1[0].Addr != "tcp://ipv4:10.0.0.1:28001" {
		t.Fatalf("expected rewritten tcp addr, got %s", insts1[0].Addr)
	}

	// Pure Mesh SUB for StubSvc (published as tcp://)
	insts2, err := subClient.SubWithTrans("StubSvc-StubSvc", meshTrans, 2*time.Second)
	if err != nil {
		t.Fatalf("SubWithTrans for StubSvc failed: %v", err)
	}
	if len(insts2) != 1 {
		t.Fatalf("expected 1 instance for StubSvc, got %d", len(insts2))
	}
	if insts2[0].Addr != "tcp://ipv4:127.0.0.1:39001" {
		t.Fatalf("expected tcp stub addr, got %s", insts2[0].Addr)
	}
}
