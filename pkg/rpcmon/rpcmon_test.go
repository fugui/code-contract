package rpcmon_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
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

func TestNativeBackend_ArgAllowlistAndJsonPrefixSkip(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "mock_probe.sh")
	script := `#!/bin/sh
echo "[INFO] loading dopra env..."
echo "source env.sh done"
cat <<'JSON'
{
  "portInfo": [{"ipAddr": "10.29.92.80", "uiPortUsed": 3, "uiPortTotal": 16384}],
  "servers": [{"procName": "srv_a", "pid": 111, "serverName": "SvcA-SvcA", "serverAddr": "tcp://ipv4:10.29.92.80:33000"}],
  "clients": [{"procName": "cli_a", "pid": 222, "clientAddr": "10.29.92.80", "serverName": "SvcA-SvcA"}],
  "serverNum": 1,
  "clientNum": 1,
  "timestamp": 1794960999
}
JSON
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write mock probe script failed: %v", err)
	}

	// Symlink binary path must be rejected.
	linkPath := filepath.Join(dir, "symlink_probe.sh")
	if err := os.Symlink(scriptPath, linkPath); err != nil {
		t.Fatalf("symlink failed: %v", err)
	}
	badLinkBackend := rpcmon.NewNative(rpcmon.NativeConfig{
		ProbeBin: linkPath,
		CSGAddr:  "ipv4:10.29.92.80:31806",
	})
	if _, err := rpcmon.Fetch(context.Background(), badLinkBackend, "all"); err == nil {
		t.Fatalf("expected symlink probe path to be rejected")
	}

	// Shell injection / invalid CSGAddr must be rejected.
	for _, badAddr := range []string{"ipv4:10.29.92.80:31806;id", "ipv4:10.29.92.80:99999", "10.29.92.80:31806"} {
		badAddrBackend := rpcmon.NewNative(rpcmon.NativeConfig{
			ProbeBin: scriptPath,
			CSGAddr:  badAddr,
		})
		if _, err := rpcmon.Fetch(context.Background(), badAddrBackend, "all"); err == nil {
			t.Fatalf("expected invalid CSGAddr %q to be rejected", badAddr)
		}
	}

	// Valid binary + allowlisted CSGAddr skips pre-'{' DOPRA logs and parses JSON.
	goodBackend := rpcmon.NewNative(rpcmon.NativeConfig{
		ProbeBin: scriptPath,
		CSGAddr:  "ipv4:10.29.92.80:31806",
	})
	rep, err := rpcmon.Fetch(context.Background(), goodBackend, "all")
	if err != nil {
		t.Fatalf("good native probe fetch failed: %v", err)
	}
	if rep.ServerNum != 1 || rep.ClientNum != 1 || len(rep.Servers) != 1 || !strings.HasPrefix(rep.Servers[0].ServerAddr, "tcp://") {
		t.Fatalf("unexpected native probe report: %+v", rep)
	}
}
