package rpcmon_test

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code-contract/pkg/crosrpc"
	"code-contract/pkg/rpcmon"
)

type staticProvider struct {
	snap rpcmon.Snapshot
}

func (p staticProvider) Snapshot() rpcmon.Snapshot { return p.snap }

func TestWireCodec_PortServerClientRoundtrip_ZeroProtoregistry(t *testing.T) {
	now := time.Unix(1794960981, 0)
	snap := rpcmon.Snapshot{
		Ports: []rpcmon.PortUsage{
			{IPAddr: "127.0.0.1", Used: 2, Total: 16384},
		},
		Servers: []rpcmon.ServerEntry{
			{ProcName: "procA", Pid: 1001, ServerName: "Order-Order", ServerAddr: "tcp://ipv4:127.0.0.1:18500"},
			{ProcName: "procB", Pid: 1002, ServerName: "Pay-Pay", ServerAddr: "dophi_mesh://ipv4:127.0.0.1:18501"},
		},
		Clients: []rpcmon.ClientEntry{
			{ProcName: "callerA", Pid: 2001, ClientAddr: "127.0.0.1", ServerName: "Order-Order"},
		},
	}

	rawPort := rpcmon.EncodePortInfoResponse(snap.Ports, now.Unix())
	ports, ts, err := rpcmon.DecodePortInfoResponse(rawPort)
	if err != nil {
		t.Fatalf("DecodePortInfoResponse failed: %v", err)
	}
	if ts != now.Unix() || len(ports) != 1 || ports[0] != snap.Ports[0] {
		t.Fatalf("port roundtrip mismatch: ts=%d ports=%+v", ts, ports)
	}

	rawNum := rpcmon.EncodeUInt32Type(snap.ServerNum())
	num, err := rpcmon.DecodeUInt32Type(rawNum)
	if err != nil || num != 2 {
		t.Fatalf("uint32 roundtrip mismatch: num=%d err=%v", num, err)
	}

	rawSrv := rpcmon.EncodeServerDetailResponse(snap.Servers, now.Unix())
	srvs, ts, err := rpcmon.DecodeServerDetailResponse(rawSrv)
	if err != nil || ts != now.Unix() || len(srvs) != 2 || srvs[1] != snap.Servers[1] {
		t.Fatalf("server detail roundtrip mismatch: ts=%d srvs=%+v err=%v", ts, srvs, err)
	}

	rawCli := rpcmon.EncodeClientDetailResponse(snap.Clients, now.Unix())
	clis, ts, err := rpcmon.DecodeClientDetailResponse(rawCli)
	if err != nil || ts != now.Unix() || len(clis) != 1 || clis[0] != snap.Clients[0] {
		t.Fatalf("client detail roundtrip mismatch: ts=%d clis=%+v err=%v", ts, clis, err)
	}
}

func TestCrosTCPBackend_StandardCrosFrameOverLoopback(t *testing.T) {
	prov := staticProvider{
		snap: rpcmon.Snapshot{
			Ports: []rpcmon.PortUsage{{IPAddr: "127.0.0.1", Used: 1, Total: 16384}},
			Servers: []rpcmon.ServerEntry{
				{ProcName: "svc1", Pid: 709, ServerName: "HelloService-HelloService", ServerAddr: "tcp://ipv4:127.0.0.1:18550"},
			},
			Clients: []rpcmon.ClientEntry{
				{ProcName: "cli1", Pid: 724, ClientAddr: "127.0.0.1", ServerName: "HelloService-HelloService"},
			},
		},
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer ln.Close()

	srv := rpcmon.NewServer(prov)
	go func() {
		_ = srv.Serve(ln)
	}()

	// Verify wire format uses standard CROS RPC "CSE.TCP" magic and parses cleanly with crosrpc.ParsePacket.
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial raw failed: %v", err)
	}
	reqFrame := rpcmon.BuildCrosReqFrame(1, rpcmon.ServiceName, rpcmon.OpGetRpcServerNumInfo, nil)
	if _, err := conn.Write(reqFrame); err != nil {
		_ = conn.Close()
		t.Fatalf("write raw req failed: %v", err)
	}
	buf := make([]byte, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(buf)
	_ = conn.Close()
	if err != nil {
		t.Fatalf("read raw rsp failed: %v", err)
	}
	parsed, err := crosrpc.ParsePacket(buf[:n])
	if err != nil {
		t.Fatalf("crosrpc.ParsePacket rejected rpcmon response: %v", err)
	}
	if parsed.Comm.MsgType != crosrpc.MsgTypeRsp || parsed.Rsp == nil || parsed.Rsp.Operation != rpcmon.OpGetRpcServerNumInfo {
		t.Fatalf("unexpected parsed CROS response: %+v", parsed)
	}

	backend, err := rpcmon.NewCrosTCP(ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("NewCrosTCP failed: %v", err)
	}
	defer backend.Close()

	rep, err := rpcmon.Fetch(context.Background(), backend, "all")
	if err != nil {
		t.Fatalf("rpcmon.Fetch failed: %v", err)
	}
	if rep.ServerNum != 1 || rep.ClientNum != 1 || len(rep.Ports) != 1 || len(rep.Servers) != 1 || len(rep.Clients) != 1 {
		t.Fatalf("unexpected report: %+v", rep)
	}
}

func TestMeshProtocol_HelloAndDataHeadRoundTrip_FirstNulTruncation(t *testing.T) {
	in := rpcmon.MeshHello{
		Role:           rpcmon.MeshRoleClient,
		Type:           rpcmon.MeshHelloTypeSec,
		Name:           "[P:CodeContractAgent:0]",
		SecNegoTimeout: 5000,
		InterFd:        42,
	}
	b := rpcmon.EncodeMeshHello(in)
	if len(b) != rpcmon.MeshHelloLen {
		t.Fatalf("hello len = %d, want %d", len(b), rpcmon.MeshHelloLen)
	}
	// Inject dirty stack bytes after first NUL in Name[96] (offsets 8..103)
	copy(b[8+len(in.Name)+1:], []byte("DIRTY_STACK_RESIDUE"))

	out, err := rpcmon.DecodeMeshHello(b)
	if err != nil {
		t.Fatal(err)
	}
	if out.Magic != rpcmon.MeshHelloMagic || out.MsgLen != rpcmon.MeshHelloLen {
		t.Fatalf("magic/len = %#x/%d", out.Magic, out.MsgLen)
	}
	if out.Name != in.Name {
		t.Fatalf("expected NUL-truncated Name=%q, got %q", in.Name, out.Name)
	}
	if out.Role != rpcmon.MeshRoleClient || out.Type != rpcmon.MeshHelloTypeSec || out.SecNegoTimeout != 5000 || out.InterFd != 42 {
		t.Fatalf("unexpected hello fields: %+v", out)
	}

	dhIn := rpcmon.MeshDataHead{
		MessageSize: 300,
		CheckType:   rpcmon.MeshCheckNone,
		SrcIPv4:     0x0A1D5C50,
		SrcPort:     40000,
		Timestamp:   123456789,
	}
	dhBytes := rpcmon.EncodeMeshDataHead(dhIn)
	if len(dhBytes) != rpcmon.MeshDataHeadLen {
		t.Fatalf("data head len = %d, want %d", len(dhBytes), rpcmon.MeshDataHeadLen)
	}
	dhOut, err := rpcmon.DecodeMeshDataHead(dhBytes)
	if err != nil {
		t.Fatal(err)
	}
	if dhOut.Magic != rpcmon.MeshDataMagic || dhOut.MsgNum != rpcmon.MeshInvalidMsgNum || dhOut.MsgID != rpcmon.MeshInvalidMsgID {
		t.Fatalf("magic/num/id = %#x/%#x/%#x", dhOut.Magic, dhOut.MsgNum, dhOut.MsgID)
	}
	if dhOut.HeadSize != rpcmon.MeshDataHeadLen || dhOut.MessageSize != 300 || dhOut.SrcIPv4 != dhIn.SrcIPv4 || dhOut.SrcPort != dhIn.SrcPort {
		t.Fatalf("unexpected data head: %+v", dhOut)
	}
}

type countingAcceptListener struct {
	net.Listener
	accepts atomic.Int32
}

func (l *countingAcceptListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepts.Add(1)
	}
	return c, err
}

func TestCrosMeshBackend_HandshakeAndAll5OpsReuseConn(t *testing.T) {
	prov := staticProvider{
		snap: rpcmon.Snapshot{
			Ports: []rpcmon.PortUsage{{IPAddr: "127.0.0.1", Used: 2, Total: 1200}},
			Servers: []rpcmon.ServerEntry{
				{ProcName: "mesh_svr", Pid: 709, ServerName: "MeshSvc-MeshSvc", ServerAddr: "127.0.0.1"},
			},
			Clients: []rpcmon.ClientEntry{
				{ProcName: "mesh_cli", Pid: 724, ClientAddr: "127.0.0.1", ServerName: "MeshSvc-MeshSvc"},
			},
		},
	}

	rawLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	ln := &countingAcceptListener{Listener: rawLn}
	defer ln.Close()

	srv := rpcmon.NewServer(prov)
	go func() {
		_ = srv.ServeMesh(ln)
	}()

	backend, err := rpcmon.NewCrosMesh("dophi_mesh://ipv4:"+ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("NewCrosMesh failed: %v", err)
	}
	defer backend.Close()

	rep, err := rpcmon.Fetch(context.Background(), backend, "all")
	if err != nil {
		t.Fatalf("rpcmon.Fetch over CrosMesh failed: %v", err)
	}
	if rep.ServerNum != 1 || rep.ClientNum != 1 || len(rep.Ports) != 1 || len(rep.Servers) != 1 || len(rep.Clients) != 1 {
		t.Fatalf("unexpected mesh report: %+v", rep)
	}
	if rep.Servers[0].ServerName != "MeshSvc-MeshSvc" || rep.Clients[0].ProcName != "mesh_cli" {
		t.Fatalf("unexpected mesh entries: %+v", rep)
	}
	if got := ln.accepts.Load(); got != 1 {
		t.Fatalf("expected single reused connection across all 5 ops, got %d accepts", got)
	}
}

func TestCrosMeshBackend_RejectsEncryptedSecOptAndBatchFrame(t *testing.T) {
	// 1. Encrypted SecOpt != 0 must fail handshake explicitly.
	encLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen enc failed: %v", err)
	}
	defer encLn.Close()
	go func() {
		conn, aErr := encLn.Accept()
		if aErr != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write(rpcmon.EncodeMeshHello(rpcmon.MeshHello{
			Role:   rpcmon.MeshRoleServer,
			Type:   rpcmon.MeshHelloTypeSec,
			Name:   "[P:EncryptedServer:0]",
			SecOpt: 1,
		}))
	}()

	if _, err := rpcmon.NewCrosMesh("dophi_mesh://ipv4:"+encLn.Addr().String(), 2*time.Second); err == nil || !strings.Contains(err.Error(), "SSP/SECLINK") {
		t.Fatalf("expected SSP/SECLINK rejection error, got: %v", err)
	}

	// 2. Batch data frame magic (0x2019BBBB) must return explicit unsupported error.
	batchLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen batch failed: %v", err)
	}
	defer batchLn.Close()
	go func() {
		conn, aErr := batchLn.Accept()
		if aErr != nil {
			return
		}
		defer conn.Close()
		hello := rpcmon.EncodeMeshHello(rpcmon.MeshHello{
			Role: rpcmon.MeshRoleServer,
			Type: rpcmon.MeshHelloTypeSec,
			Name: "[P:BatchServer:0]",
		})
		r := bufio.NewReader(conn)
		_, _ = conn.Write(hello)
		buf := make([]byte, rpcmon.MeshHelloLen)
		_, _ = r.Read(buf)
		_, _ = conn.Write(hello)
		_, _ = r.Read(buf)
		// Wait for client request head, then respond with batch magic 0x2019BBBB
		reqPeek := make([]byte, 40)
		_, _ = r.Read(reqPeek)
		_, _ = conn.Write([]byte{0x20, 0x19, 0xBB, 0xBB})
	}()

	backend, err := rpcmon.NewCrosMesh(batchLn.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("NewCrosMesh batch server dial failed: %v", err)
	}
	defer backend.Close()
	if _, err := backend.ServerNum(context.Background()); err == nil || !strings.Contains(err.Error(), "0x2019BBBB") {
		t.Fatalf("expected 0x2019BBBB unsupported batch frame error, got: %v", err)
	}
}
