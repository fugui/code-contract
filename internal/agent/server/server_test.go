package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
	"code-contract/pkg/transport"
)

// TestAgentZeroSchemaAndZeroDBDeps 静态架构断言: 数据面零 gorm、零 dynamicpb/protoregistry、零 os/exec
func TestAgentZeroSchemaAndZeroDBDeps(t *testing.T) {
	// 1. 执行 go list -deps 断言数据面依赖闭包
	cmd := exec.Command("go", "list", "-deps", "./pkg/ebpf", "./pkg/replay", "./internal/agent/...")
	cmd.Dir = "../../../" // code-contract root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps failed: %v, output: %s", err, string(out))
	}

	forbiddenDeps := []string{
		"gorm.io",
		"code-common/backend/gormdb",
		"google.golang.org/protobuf/types/dynamicpb",
		"google.golang.org/protobuf/reflect/protoregistry",
	}

	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		for _, forbidden := range forbiddenDeps {
			if strings.Contains(trimmed, forbidden) {
				t.Fatalf("数据面架构红线违背: 引入了禁止的包 %s (命中 %s)", forbidden, trimmed)
			}
		}
	}

	// 2. 扫描 AST 确保源码中没有使用 os/exec (排除测试文件)
	rootDirs := []string{"../../../pkg/ebpf", "../../../pkg/replay", "../../../internal/agent"}
	for _, rootDir := range rootDirs {
		err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				fset := token.NewFileSet()
				node, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
				if err != nil {
					return err
				}
				for _, imp := range node.Imports {
					importPath := strings.Trim(imp.Path.Value, `"`)
					if importPath == "os/exec" {
						t.Errorf("源码文件 %s 违规引入了 os/exec 外部命令拼接", path)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("扫描目录 %s AST 失败: %v", rootDir, err)
		}
	}
}

// TestAgentIPAllowlistAndClaimCode 验证源 IP 白名单过滤与 ClaimCode 校验
func TestAgentIPAllowlistAndClaimCode(t *testing.T) {
	cfg := AgentServerConfig{
		ListenAddr: "127.0.0.1:0",
		AllowIPs:   []string{"127.0.0.1/32"},
		ClaimCode:  "my_secure_claim_code_12345",
	}

	srv, err := NewAgentServer(cfg)
	if err != nil {
		t.Fatalf("NewAgentServer failed: %v", err)
	}

	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	handler := srv.ipAllowlistMiddleware(mux)

	// 1. 模拟非白名单 IP (附带伪造的 X-Forwarded-For: 127.0.0.1)
	reqForbidden := httptest.NewRequest(http.MethodGet, "/agent/v1/handshake", nil)
	reqForbidden.RemoteAddr = "10.20.30.40:54321" // 非白名单 IP
	reqForbidden.Header.Set("X-Forwarded-For", "127.0.0.1")
	recForbidden := httptest.NewRecorder()
	handler.ServeHTTP(recForbidden, reqForbidden)

	if recForbidden.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for non-whitelisted IP, got %d", recForbidden.Code)
	}

	// 2. 白名单 IP 携带错误的 ClaimCode
	reqBadClaim := httptest.NewRequest(http.MethodGet, "/agent/v1/handshake", nil)
	reqBadClaim.RemoteAddr = "127.0.0.1:54321"
	reqBadClaim.Header.Set(agentproto.HeaderClaimCode, "wrong_claim_code")
	recBadClaim := httptest.NewRecorder()
	handler.ServeHTTP(recBadClaim, reqBadClaim)

	if recBadClaim.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for wrong ClaimCode, got %d", recBadClaim.Code)
	}

	// 3. 白名单 IP 携带正确的 ClaimCode
	reqOK := httptest.NewRequest(http.MethodGet, "/agent/v1/handshake", nil)
	reqOK.RemoteAddr = "127.0.0.1:54321"
	reqOK.Header.Set(agentproto.HeaderClaimCode, "my_secure_claim_code_12345")
	recOK := httptest.NewRecorder()
	handler.ServeHTTP(recOK, reqOK)

	if recOK.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for valid ClaimCode, got %d", recOK.Code)
	}

	var handshakeResp agentproto.HandshakeResp
	if err := json.Unmarshal(recOK.Body.Bytes(), &handshakeResp); err != nil {
		t.Fatalf("unmarshal HandshakeResp failed: %v", err)
	}
	if handshakeResp.ProtocolVersion != "1.0" {
		t.Errorf("expected ProtocolVersion 1.0, got %s", handshakeResp.ProtocolVersion)
	}
	if handshakeResp.BootID == "" {
		t.Errorf("expected non-empty BootID")
	}
}

// TestAgentIPAllowlistWithProductionControlPlane 验证包含现网主控 IP (10.1.64.178) 白名单准入与隔离
func TestAgentIPAllowlistWithProductionControlPlane(t *testing.T) {
	cfg := AgentServerConfig{
		ListenAddr: "127.0.0.1:0",
		AllowIPs:   []string{"127.0.0.1", "10.1.64.178"},
		ClaimCode:  "prod_claim_code_98765",
	}

	srv, err := NewAgentServer(cfg)
	if err != nil {
		t.Fatalf("NewAgentServer failed: %v", err)
	}

	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	handler := srv.ipAllowlistMiddleware(mux)

	// 1. 模拟来自现网主控 IP (10.1.64.178) 携带合法 ClaimCode 成功握手
	reqProd := httptest.NewRequest(http.MethodGet, "/agent/v1/handshake", nil)
	reqProd.RemoteAddr = "10.1.64.178:54321"
	reqProd.Header.Set(agentproto.HeaderClaimCode, "prod_claim_code_98765")
	recProd := httptest.NewRecorder()
	handler.ServeHTTP(recProd, reqProd)

	if recProd.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for 10.1.64.178, got %d (body: %s)", recProd.Code, recProd.Body.String())
	}

	// 2. 模拟本地 127.0.0.1 同样受允许
	reqLocal := httptest.NewRequest(http.MethodGet, "/agent/v1/handshake", nil)
	reqLocal.RemoteAddr = "127.0.0.1:54322"
	reqLocal.Header.Set(agentproto.HeaderClaimCode, "prod_claim_code_98765")
	recLocal := httptest.NewRecorder()
	handler.ServeHTTP(recLocal, reqLocal)

	if recLocal.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for 127.0.0.1, got %d", recLocal.Code)
	}

	// 3. 模拟非白名单 IP (如 10.20.30.40) 请求被拦截
	reqForbidden := httptest.NewRequest(http.MethodGet, "/agent/v1/handshake", nil)
	reqForbidden.RemoteAddr = "10.20.30.40:54321"
	recForbidden := httptest.NewRecorder()
	handler.ServeHTTP(recForbidden, reqForbidden)

	if recForbidden.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for non-whitelisted IP, got %d", recForbidden.Code)
	}
}

// TestBoundedSessionBufferBackpressureAndGracePurge 验证有界缓冲背压丢帧与失联看门狗自洁
func TestBoundedSessionBufferBackpressureAndGracePurge(t *testing.T) {
	// 1. 测试 1MB 缓冲上限与非阻塞丢帧
	maxBytes := int64(1024 * 1024) // 1MB
	buf := NewBoundedSessionBuffer(maxBytes)

	// 生成 256KB 的大帧
	frameChunk := make([]byte, 256*1024)

	// 灌入 4 帧 = 1MB 刚好填满
	for i := 0; i < 4; i++ {
		ok := buf.Push(frameChunk)
		if !ok {
			t.Fatalf("frame %d should be pushed", i)
		}
	}

	// 再灌入 4 帧 (共 2MB) -> 触发背压非阻塞丢弃
	for i := 0; i < 4; i++ {
		ok := buf.Push(frameChunk)
		if ok {
			t.Errorf("frame %d should have been dropped due to backpressure", i)
		}
	}

	used, dropped, total := buf.Stats()
	if used > maxBytes {
		t.Errorf("used bytes %d exceeded max buffer %d", used, maxBytes)
	}
	if dropped != 4 {
		t.Errorf("expected exactly 4 dropped frames, got %d", dropped)
	}
	if total != 4 {
		t.Errorf("expected exactly 4 captured frames, got %d", total)
	}

	// 2. 测试失联看门狗宽限期自洁
	cfg := AgentServerConfig{
		ListenAddr:  "127.0.0.1:0",
		GracePeriod: 200 * time.Millisecond,
	}
	srv, err := NewAgentServer(cfg)
	if err != nil {
		t.Fatalf("NewAgentServer failed: %v", err)
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start server failed: %v", err)
	}
	defer srv.Shutdown(context.Background())

	// 锁定一个模拟租约
	srv.mu.Lock()
	srv.activeLeaseType = "RECORDING"
	srv.activeRefID = 8888
	srv.lastContactAt = time.Now()
	srv.mu.Unlock()

	// 等待超过 200ms 宽限期且无 WebSocket 活跃连接
	time.Sleep(350 * time.Millisecond)

	leaseType, refID := srv.ActiveLease()
	if leaseType != "" || refID != 0 {
		t.Errorf("expected lease cleared after watchdog grace period, got %s, %d", leaseType, refID)
	}

	// 3. 测试心跳双向对账：下发 X-Expected-Lease-Type: "" 时立即自洁
	srv.mu.Lock()
	srv.activeLeaseType = "RECORDING"
	srv.activeRefID = 9999
	srv.mu.Unlock()

	reqReconcile := httptest.NewRequest(http.MethodGet, "/agent/v1/handshake", nil)
	reqReconcile.RemoteAddr = "127.0.0.1:12345"
	reqReconcile.Header.Set(agentproto.HeaderExpectedLeaseType, "") // 主控期望无租约
	recReconcile := httptest.NewRecorder()

	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	mux.ServeHTTP(recReconcile, reqReconcile)

	leaseTypeAfter, refIDAfter := srv.ActiveLease()
	if leaseTypeAfter != "" || refIDAfter != 0 {
		t.Errorf("expected lease cleared after empty expected lease heartbeat, got %s, %d", leaseTypeAfter, refIDAfter)
	}
}

func TestAgentServer_HandshakeReportsTransportDrivers(t *testing.T) {
	cfg := AgentServerConfig{
		ListenAddr: "127.0.0.1:0",
	}
	srv, err := NewAgentServer(cfg)
	if err != nil {
		t.Fatalf("NewAgentServer failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/agent/v1/handshake", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var resp agentproto.HandshakeResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal HandshakeResp: %v", err)
	}

	if len(resp.TransportDrivers) == 0 {
		t.Fatalf("expected non-empty TransportDrivers in HandshakeResp, got 0")
	}

	foundTCP := false
	foundDRT := false
	for _, td := range resp.TransportDrivers {
		if td.Name == "builtin-tcp" && td.TransportCode == 1 {
			foundTCP = true
			if td.Capabilities == 0 {
				t.Errorf("expected non-zero capabilities for builtin-tcp")
			}
		}
		if td.Name == "builtin-drt-uprobe" && td.TransportCode == 4 {
			foundDRT = true
			if td.Capabilities == 0 {
				t.Errorf("expected non-zero capabilities for builtin-drt-uprobe")
			}
		}
	}
	if !foundTCP {
		t.Errorf("expected to find builtin-tcp driver in HandshakeResp, got %+v", resp.TransportDrivers)
	}
	if !foundDRT {
		t.Errorf("expected to find builtin-drt-uprobe driver in HandshakeResp, got %+v", resp.TransportDrivers)
	}
}

func TestAgentServer_PureMeshReplaySkipsUnrelatedTCPNeedReconnectAndKeepsExplicitDownstreamGuard(t *testing.T) {
	procRoot := t.TempDir()
	tcpContent := strings.Join([]string{
		"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode",
		"   0: 0100007F:C001 0100007F:1538 01 00000000:00000000 00:00000000 00000000  1000        0 30001", // 127.0.0.1:49153 -> 127.0.0.1:5432
	}, "\n") + "\n"
	_ = os.MkdirAll(filepath.Join(procRoot, "net"), 0755)
	_ = os.WriteFile(filepath.Join(procRoot, "net", "tcp"), []byte(tcpContent), 0644)

	pDir := filepath.Join(procRoot, "401")
	_ = os.MkdirAll(filepath.Join(pDir, "fd"), 0755)
	_ = os.WriteFile(filepath.Join(pDir, "cmdline"), []byte("/opt/bin/mesh_worker\x00--mesh"), 0644)
	_ = os.WriteFile(filepath.Join(pDir, "comm"), []byte("mesh_worker\n"), 0644)
	_ = os.Symlink("socket:[30001]", filepath.Join(pDir, "fd", "3"))

	srv, err := NewAgentServer(AgentServerConfig{
		ListenAddr: "127.0.0.1:0",
		ProcRoot:   procRoot,
		CSGAddrs:   []string{"127.0.0.1:31806"},
	})
	if err != nil {
		t.Fatalf("NewAgentServer failed: %v", err)
	}
	defer srv.PurgeAll()

	// 1. 验证 enrichSpecsWithCSGAddr 自动注入默认 CSG 端点
	enriched := srv.enrichSpecsWithCSGAddr([]agentproto.ProcessMonitorSpec{{ProcessID: 1}})
	if len(enriched) != 1 || enriched[0].CSGAddr != "127.0.0.1:31806" {
		t.Fatalf("expected CSGAddr enriched to 127.0.0.1:31806, got %+v", enriched)
	}

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	// 2. 纯 MESH 进程无显式 TCP 下游：携带无关 TCP 长连接 (5432) 不应误报 409 NEED_RECONNECT
	reqBody1, _ := json.Marshal(agentproto.ExecuteReplayReq{
		TaskID: 501,
		ProcessSpec: agentproto.ProcessMonitorSpec{
			ProcessID:  1,
			ListenPort: 0,
			CmdPattern: "mesh_worker",
			TargetPIDs: []uint32{401},
			Transports: []string{"MESH"},
		},
		TimeoutMs: 500,
	})
	req1 := httptest.NewRequest(http.MethodPost, "/agent/v1/replays/execute", bytes.NewReader(reqBody1))
	req1.RemoteAddr = "127.0.0.1:12345"
	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for pure mesh replay without explicit downstreams, got %d (body: %s)", rec1.Code, rec1.Body.String())
	}

	// 释放 TaskID 501 租约后再测试显式下游拦截
	srv.PurgeAll()

	// 3. 若显式指定了指向 127.0.0.1:5432 的 Mock 下游端点，仍必须守住零外泄 409 拦截
	reqBody2, _ := json.Marshal(agentproto.ExecuteReplayReq{
		TaskID: 502,
		ProcessSpec: agentproto.ProcessMonitorSpec{
			ProcessID:   1,
			ListenPort:  0,
			CmdPattern:  "mesh_worker",
			TargetPIDs:  []uint32{401},
			Transports:  []string{"MESH"},
			Downstreams: []agentproto.DownstreamEndpoint{{IP: "127.0.0.1", Port: 5432}},
		},
		TimeoutMs: 500,
	})
	req2 := httptest.NewRequest(http.MethodPost, "/agent/v1/replays/execute", bytes.NewReader(reqBody2))
	req2.RemoteAddr = "127.0.0.1:12345"
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict || !strings.Contains(rec2.Body.String(), "NEED_RECONNECT") {
		t.Fatalf("expected 409 NEED_RECONNECT when explicit downstream matches live socket, got %d (body: %s)", rec2.Code, rec2.Body.String())
	}
}

func makeMinimalAgentCrosFrame(sno uint64) []byte {
	pkt := make([]byte, crosrpc.CommHeadLen)
	copy(pkt[0:7], []byte(crosrpc.MagicPrefix))
	pkt[7] = crosrpc.TransportMesh
	binary.BigEndian.PutUint64(pkt[8:16], sno)
	binary.BigEndian.PutUint32(pkt[16:20], 0)
	binary.BigEndian.PutUint16(pkt[20:22], 0)
	pkt[22] = crosrpc.MsgTypeReq
	return pkt
}

func TestAgentServer_StopRecordingLeaseRaceDoesNotPurgeOrBrickDrivers(t *testing.T) {
	srv, err := NewAgentServer(AgentServerConfig{
		ListenAddr: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("NewAgentServer failed: %v", err)
	}
	defer srv.Shutdown(context.Background())

	// 模拟处于 ARMED 待命并正在录制 SessionID=777
	srv.mu.Lock()
	srv.armedDigest = "digest-armed-777"
	srv.armedProcesses = []agentproto.ProcessMonitorSpec{{ProcessID: 1, ListenPort: 8001}}
	srv.activeLeaseType = "RECORDING"
	srv.activeRefID = 777
	srv.mu.Unlock()

	if err := srv.transportRegistry.StartCapture(context.Background(), transport.CaptureSpec{
		SessionOrTaskID: 777,
		RefKind:         agentproto.RefKindRecordingSession,
	}); err != nil {
		t.Fatalf("StartCapture 777 failed: %v", err)
	}

	// 模拟 2 帧在途流量先入缓冲
	for i := uint64(1); i <= 2; i++ {
		env := agentproto.EnvelopeHeader{
			Magic:           agentproto.EnvelopeMagic,
			ProtoVer:        agentproto.EnvelopeProtoVer,
			EventType:       agentproto.EventTrafficFrame,
			SessionOrTaskID: 777,
			CapturedAtNs:    time.Now().UnixNano(),
		}
		srv.buffer.Push(agentproto.MarshalEnvelope(env, makeMinimalAgentCrosFrame(i)))
	}

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	// 1. 触发停录 /stop
	stopReq := httptest.NewRequest(http.MethodPost, "/agent/v1/monitors/777/stop", nil)
	stopReq.RemoteAddr = "127.0.0.1:12345"
	stopRec := httptest.NewRecorder()
	mux.ServeHTTP(stopRec, stopReq)
	if stopRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /stop, got %d", stopRec.Code)
	}

	// 验证缓冲中 EventSessionDrained 严格排在 2 条流量帧之后
	items := srv.buffer.Drain()
	if len(items) != 3 {
		t.Fatalf("expected 3 frames in buffer (2 traffic + 1 drained), got %d", len(items))
	}
	lastHdr, _, err := agentproto.UnmarshalEnvelope(items[2])
	if err != nil || lastHdr.EventType != agentproto.EventSessionDrained {
		t.Fatalf("expected last frame to be EventSessionDrained, got %+v (err=%v)", lastHdr, err)
	}

	// 2. 紧接着模拟主控在释放 DB 租约前发起的携带 RECORDING/777 的心跳握手（竞态窗口）
	hsReq := httptest.NewRequest(http.MethodGet, "/agent/v1/handshake", nil)
	hsReq.RemoteAddr = "127.0.0.1:12345"
	hsReq.Header.Set(agentproto.HeaderExpectedLeaseType, "RECORDING")
	hsReq.Header.Set(agentproto.HeaderExpectedLeaseRef, "777")
	hsRec := httptest.NewRecorder()
	mux.ServeHTTP(hsRec, hsReq)
	if hsRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /handshake, got %d", hsRec.Code)
	}

	var hsResp agentproto.HandshakeResp
	if err := json.Unmarshal(hsRec.Body.Bytes(), &hsResp); err != nil {
		t.Fatalf("unmarshal HandshakeResp failed: %v", err)
	}
	if hsResp.ArmedDigest != "digest-armed-777" {
		t.Fatalf("expected ArmedDigest preserved after stop+handshake race, got %q", hsResp.ArmedDigest)
	}
	if hsResp.ActiveLeaseType != "" || hsResp.ActiveRefID != 0 {
		t.Fatalf("expected empty active lease, got (%q,%d)", hsResp.ActiveLeaseType, hsResp.ActiveRefID)
	}

	// 3. 确认传输驱动未被永久关闭，后续 SessionID=778 录制可正常启动
	if err := srv.transportRegistry.StartCapture(context.Background(), transport.CaptureSpec{
		SessionOrTaskID: 778,
		RefKind:         agentproto.RefKindRecordingSession,
	}); err != nil {
		t.Fatalf("subsequent StartCapture 778 failed (driver bricked?): %v", err)
	}
}

func TestAgentServer_PurgeAllClearsArmedDigestAndKeepsDriversReusable(t *testing.T) {
	srv, err := NewAgentServer(AgentServerConfig{
		ListenAddr: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("NewAgentServer failed: %v", err)
	}

	srv.mu.Lock()
	srv.armedRevision = 3
	srv.armedDigest = "digest-armed-888"
	srv.armedProcesses = []agentproto.ProcessMonitorSpec{{ProcessID: 1, ListenPort: 8001}}
	srv.activeLeaseType = "RECORDING"
	srv.activeRefID = 888
	srv.mu.Unlock()

	srv.PurgeAll()

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	hsReq := httptest.NewRequest(http.MethodGet, "/agent/v1/handshake", nil)
	hsReq.RemoteAddr = "127.0.0.1:12345"
	hsRec := httptest.NewRecorder()
	mux.ServeHTTP(hsRec, hsReq)
	if hsRec.Code != http.StatusOK {
		t.Fatalf("handshake failed: %d", hsRec.Code)
	}

	var hsResp agentproto.HandshakeResp
	if err := json.Unmarshal(hsRec.Body.Bytes(), &hsResp); err != nil {
		t.Fatalf("unmarshal HandshakeResp failed: %v", err)
	}
	if hsResp.ArmedDigest != "" || hsResp.ArmedProcessCount != 0 {
		t.Fatalf("expected ArmedDigest and ArmedProcessCount cleared after PurgeAll so AutoRearm triggers, got digest=%q count=%d", hsResp.ArmedDigest, hsResp.ArmedProcessCount)
	}

	// PurgeAll 重置驱动后，仍可复用 StartCapture
	if err := srv.transportRegistry.StartCapture(context.Background(), transport.CaptureSpec{
		SessionOrTaskID: 889,
		RefKind:         agentproto.RefKindRecordingSession,
	}); err != nil {
		t.Fatalf("StartCapture after PurgeAll failed: %v", err)
	}

	// Shutdown 进程退出时才会永久关闭驱动
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
	if err := srv.drtDriver.StartCapture(context.Background(), transport.CaptureSpec{SessionOrTaskID: 890}); err == nil {
		t.Fatalf("expected StartCapture to fail after Shutdown permanently closes driver")
	}
}
