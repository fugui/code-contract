package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"code-contract/internal/agent/csg"
	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
	"code-contract/pkg/ebpf"
	"code-contract/pkg/replay"
	"code-contract/pkg/transport"

	"github.com/gorilla/websocket"
)

// AgentServerConfig Agent 服务器启动参数
type AgentServerConfig struct {
	ListenAddr            string
	AllowIPs              []string
	MaxSessionBufferBytes int64
	GracePeriod           time.Duration
	StatsInterval         time.Duration
	ClaimCode             string
	ClaimCodeFile         string
	AgentVersion          string
	ProcRoot              string
	Verbose               bool
	LogFunc               func(format string, args ...interface{})
	CapabilityChecker     ebpf.CapabilityChecker
	CSGAddrs              []string
	CSGMonitorAddr        string
}

// AgentServer code-contract-agent HTTP REST + WebSocket 二进制数据面服务端
type AgentServer struct {
	mu                sync.Mutex
	cfg               AgentServerConfig
	allowPrefixes     []netip.Prefix
	claimCode         string
	bootID            string
	startedAt         time.Time
	buffer            *BoundedSessionBuffer
	ebpfMgr           *ebpf.Manager
	drtDriver         *transport.DRTUprobeDriver
	replayExec        *replay.ReplayExecutor
	procScanner       *ebpf.ProcScanner
	activeLeaseType   string
	activeRefID       uint64
	lastContactAt     time.Time
	wsActive          int32
	wsSentFrames      atomic.Int64
	httpServer        *http.Server
	listener          net.Listener
	watchdogStop      chan struct{}
	statsStop         chan struct{}
	windowMu          sync.Mutex
	windowBreakdown   map[frameBreakdownKey]int64
	upgrader          websocket.Upgrader
	armedRevision     uint64
	armedDigest       string
	armedProcesses    []agentproto.ProcessMonitorSpec
	latestDiags       []agentproto.ProcessDiagStatus
	activeCSGSim      *csg.SimController
	activeDone        chan struct{}
	transportRegistry *transport.Registry
}

// NewAgentServer 构造 Agent 服务端实例
func NewAgentServer(cfg AgentServerConfig) (*AgentServer, error) {
	if cfg.AgentVersion == "" {
		cfg.AgentVersion = "1.0.0"
	}
	if cfg.GracePeriod <= 0 {
		cfg.GracePeriod = 5 * time.Minute
	}
	if cfg.StatsInterval <= 0 {
		cfg.StatsInterval = 5 * time.Second
	}
	if cfg.MaxSessionBufferBytes <= 0 {
		cfg.MaxSessionBufferBytes = DefaultMaxSessionBufferBytes
	}

	// 1. 生成一次性认领码 (16 字节十六进制)
	claimCode := cfg.ClaimCode
	if claimCode == "" {
		randomBytes := make([]byte, 16)
		if _, err := rand.Read(randomBytes); err != nil {
			return nil, fmt.Errorf("生成 ClaimCode 失败: %w", err)
		}
		claimCode = hex.EncodeToString(randomBytes)
	}

	// 若配置了认领码输出路径，写入受保护文件
	if cfg.ClaimCodeFile != "" {
		_ = os.MkdirAll(filepath.Dir(cfg.ClaimCodeFile), 0755)
		_ = os.WriteFile(cfg.ClaimCodeFile, []byte(claimCode), 0600)
	}

	// 2. 生成实例重启 BootID
	bootRandom := make([]byte, 16)
	_, _ = rand.Read(bootRandom)
	bootID := hex.EncodeToString(bootRandom)

	// 3. 解析 IP 白名单
	var prefixes []netip.Prefix
	for _, ipStr := range cfg.AllowIPs {
		ipStr = strings.TrimSpace(ipStr)
		if ipStr == "" {
			continue
		}
		if !strings.Contains(ipStr, "/") {
			// 单 IP
			addr, err := netip.ParseAddr(ipStr)
			if err == nil {
				if addr.Is4() {
					prefixes = append(prefixes, netip.PrefixFrom(addr, 32))
				} else {
					prefixes = append(prefixes, netip.PrefixFrom(addr, 128))
				}
			}
		} else {
			// CIDR 网段
			pfx, err := netip.ParsePrefix(ipStr)
			if err == nil {
				prefixes = append(prefixes, pfx)
			}
		}
	}

	buf := NewBoundedSessionBuffer(cfg.MaxSessionBufferBytes)

	s := &AgentServer{
		cfg:             cfg,
		allowPrefixes:   prefixes,
		claimCode:       claimCode,
		bootID:          bootID,
		startedAt:       time.Now(),
		buffer:          buf,
		procScanner:     ebpf.NewProcScanner(cfg.ProcRoot),
		replayExec:      replay.NewReplayExecutor(),
		lastContactAt:   time.Now(),
		watchdogStop:    make(chan struct{}),
		statsStop:       make(chan struct{}),
		windowBreakdown: make(map[frameBreakdownKey]int64),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	}

	// 创建 eBPF 管理器
	ebpfConfig := ebpf.ManagerConfig{
		ProcRoot:          cfg.ProcRoot,
		MaxFramerBudget:   int(cfg.MaxSessionBufferBytes),
		Verbose:           cfg.Verbose,
		CapabilityChecker: cfg.CapabilityChecker,
		FrameSink: func(env []byte) {
			s.recordFrameEnvelope(env)
			s.buffer.Push(env)
		},
		LifecycleEventSink: func(eventType uint8, eventData []byte) {
			// 封装为 WebSocket 信封帧直接入缓冲
			env := agentproto.EnvelopeHeader{
				Magic:        agentproto.EnvelopeMagic,
				ProtoVer:     agentproto.EnvelopeProtoVer,
				EventType:    eventType,
				CapturedAtNs: time.Now().UnixNano(),
			}
			s.buffer.Push(agentproto.MarshalEnvelope(env, eventData))
		},
	}
	s.ebpfMgr = ebpf.NewManager(ebpfConfig)

	s.transportRegistry = transport.NewRegistry()
	_ = s.transportRegistry.Register(transport.NewTCPDriver(s.ebpfMgr))
	s.drtDriver = transport.NewDRTUprobeDriver(s.ebpfMgr, transport.DRTUprobeConfig{
		ProcRoot:      s.cfg.ProcRoot,
		PayloadOffset: 0,
		Verbose:       s.cfg.Verbose,
	})
	_ = s.transportRegistry.Register(s.drtDriver)

	return s, nil
}

// TransportRegistry 获取传输驱动注册表
func (s *AgentServer) TransportRegistry() *transport.Registry {
	return s.transportRegistry
}

// ClaimCode 获取当前认领码
func (s *AgentServer) ClaimCode() string {
	return s.claimCode
}

// BootID 获取当前实例 BootID
func (s *AgentServer) BootID() string {
	return s.bootID
}

// Buffer 获取会话缓冲
func (s *AgentServer) Buffer() *BoundedSessionBuffer {
	return s.buffer
}

// EbpfManager 获取 eBPF 管理器
func (s *AgentServer) EbpfManager() *ebpf.Manager {
	return s.ebpfMgr
}

// ActiveLease 获取当前持有的租约
func (s *AgentServer) ActiveLease() (leaseType string, refID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeLeaseType, s.activeRefID
}

// SetGracePeriod 用于测试动态修改看门狗超时时间
func (s *AgentServer) SetGracePeriod(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.GracePeriod = d
}

// Start 启动 HTTP + WebSocket 监听服务
func (s *AgentServer) Start() error {
	addr := s.cfg.ListenAddr
	if addr == "" {
		addr = "127.0.0.1:0"
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", addr, err)
	}
	s.listener = ln

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	// 注入 IP 白名单拦截
	handler := s.ipAllowlistMiddleware(mux)

	s.httpServer = &http.Server{
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// 启动失联自洁看门狗与 5 秒滚动统计汇报
	go s.watchdogLoop()
	go s.statsLoop()

	go func() {
		_ = s.httpServer.Serve(ln)
	}()

	return nil
}

// Addr 返回实际监听的地址
func (s *AgentServer) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr()
	}
	return nil
}

// Shutdown 优雅关闭服务
func (s *AgentServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	select {
	case <-s.watchdogStop:
	default:
		close(s.watchdogStop)
	}
	select {
	case <-s.statsStop:
	default:
		close(s.statsStop)
	}
	s.mu.Unlock()

	s.PurgeAll()
	s.buffer.Close()

	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

// PurgeAll 清空所有监控、取消重放、卸载 CSG 仿真桩并释放本地排他租约
func (s *AgentServer) PurgeAll() {
	s.mu.Lock()
	if s.ebpfMgr != nil {
		s.ebpfMgr.PurgeAllMonitors()
	}
	if s.transportRegistry != nil {
		_ = s.transportRegistry.CloseAll()
	}
	if s.replayExec != nil && s.activeRefID > 0 {
		s.replayExec.Cancel(s.activeRefID)
	}
	sim := s.activeCSGSim
	s.activeCSGSim = nil
	s.activeLeaseType = ""
	s.activeRefID = 0
	s.activeDone = nil
	s.buffer.Reset()
	s.mu.Unlock()

	s.finishCSGSim(sim, 0)
}

// IP 白名单中间件 (严格忽略可被伪造的 X-Forwarded-For，仅取 TCP RemoteAddr)
func (s *AgentServer) ipAllowlistMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.allowPrefixes) > 0 {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			addr, err := netip.ParseAddr(host)
			if err != nil {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			allowed := false
			for _, pfx := range s.allowPrefixes {
				if pfx.Contains(addr) {
					allowed = true
					break
				}
			}

			if !allowed {
				http.Error(w, "Forbidden: IP not in allowlist", http.StatusForbidden)
				return
			}
		}

		s.touchContact()
		next.ServeHTTP(w, r)
	})
}

func (s *AgentServer) touchContact() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastContactAt = time.Now()
}

func (s *AgentServer) watchdogLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-s.watchdogStop:
			return
		case <-ticker.C:
			s.mu.Lock()
			hasLease := s.activeLeaseType != ""
			isDisconnected := atomic.LoadInt32(&s.wsActive) == 0
			timeoutExceeded := time.Since(s.lastContactAt) > s.cfg.GracePeriod
			s.mu.Unlock()

			if hasLease && isDisconnected && timeoutExceeded {
				s.PurgeAll()
			}
		}
	}
}

func (s *AgentServer) registerRoutes(mux *http.ServeMux) {
	// 1. 握手与心跳双向对账
	mux.HandleFunc("GET /agent/v1/handshake", s.handleHandshake)
	mux.HandleFunc("GET /agent/v1/health", s.handleHandshake)

	// 2. 候选进程与 CSG 拓扑探查
	mux.HandleFunc("GET /agent/v1/processes/discover", s.handleDiscoverProcesses)
	mux.HandleFunc("GET /agent/v1/csg/topology", s.handleCSGTopology)

	// 3. 待命预挂载与状态同步
	mux.HandleFunc("POST /agent/v1/monitors/arm", s.handleArmStandby)
	mux.HandleFunc("POST /agent/v1/monitors/sync", s.handleSyncMonitors)
	mux.HandleFunc("PUT /agent/v1/monitors/sync", s.handleSyncMonitors)

	// 4. 单独移除进程监控
	mux.HandleFunc("DELETE /agent/v1/monitors/{sessionId}/processes/{processId}", s.handleRemoveProcessMonitor)

	// 5. 停止录制会话
	mux.HandleFunc("POST /agent/v1/monitors/{sessionId}/stop", s.handleStopMonitor)

	// 6. 执行离线重放
	mux.HandleFunc("POST /agent/v1/replays/execute", s.handleExecuteReplay)

	// 7. 查询重放结果
	mux.HandleFunc("GET /agent/v1/replays/{taskId}/result", s.handleGetReplayResult)

	// 8. 取消重放
	mux.HandleFunc("POST /agent/v1/replays/{taskId}/cancel", s.handleCancelReplay)

	// 9. 强制清理与复位
	mux.HandleFunc("POST /agent/v1/purge", s.handlePurge)

	// 10. WebSocket 二进制流
	mux.HandleFunc("GET /agent/v1/stream", s.handleStream)
}

func (s *AgentServer) handleHandshake(w http.ResponseWriter, r *http.Request) {
	// 校验 ClaimCode
	clientClaim := r.Header.Get(agentproto.HeaderClaimCode)
	if clientClaim != "" && clientClaim != s.claimCode {
		http.Error(w, "Forbidden: Invalid ClaimCode", http.StatusForbidden)
		return
	}

	// 检查主控期望租约
	expectedType := r.Header.Get(agentproto.HeaderExpectedLeaseType)
	expectedRefStr := r.Header.Get(agentproto.HeaderExpectedLeaseRef)
	var expectedRef uint64
	if expectedRefStr != "" {
		expectedRef, _ = strconv.ParseUint(expectedRefStr, 10, 64)
	}

	s.mu.Lock()
	// 若主控判定租约已过期清空 (expectedType == "" 或 "NONE")，而 Agent 仍持有租约，立即触发自洁
	if (expectedType == "" || expectedType == "NONE") && s.activeLeaseType != "" {
		s.logf("[Agent] [Handshake] 租约对账触发自洁: expected=(%q,%d) actual=(%s,%d)", expectedType, expectedRef, s.activeLeaseType, s.activeRefID)
		s.mu.Unlock()
		s.PurgeAll()
		s.mu.Lock()
	} else if expectedType != "" && (expectedType != s.activeLeaseType || expectedRef != s.activeRefID) {
		// 租约失调自愈
		s.logf("[Agent] [Handshake] 租约失调触发自洁: expected=(%q,%d) actual=(%s,%d)", expectedType, expectedRef, s.activeLeaseType, s.activeRefID)
		s.mu.Unlock()
		s.PurgeAll()
		s.mu.Lock()
	}

	hasCaps, _, capDetail := s.ebpfMgr.CheckPrivileges()
	uptime := uint64(time.Since(s.startedAt).Seconds())

	armedDigest := s.armedDigest
	armedCount := len(s.armedProcesses)
	s.mu.Unlock()

	csgEps := s.locateCSGEndpoints()

	var transDrivers []agentproto.TransportDriverCapability
	if s.transportRegistry != nil {
		transDrivers = s.transportRegistry.Capabilities()
	}

	resp := agentproto.HandshakeResp{
		AgentVersion:      s.cfg.AgentVersion,
		ProtocolVersion:   agentproto.ProtocolVersion,
		BootID:            s.bootID,
		StartedAt:         s.startedAt,
		KernelVersion:     "Linux",
		HasEbpfCap:        hasCaps,
		CapDetail:         capDetail,
		ActiveLeaseType:   s.activeLeaseType,
		ActiveRefID:       s.activeRefID,
		UptimeSec:         uptime,
		ArmedProcessCount: armedCount,
		ArmedDigest:       armedDigest,
		CSGEndpoints:      csgEps,
		TransportDrivers:  transDrivers,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *AgentServer) handleDiscoverProcesses(w http.ResponseWriter, r *http.Request) {
	procs, err := s.procScanner.ScanListenProcesses()
	if err != nil {
		http.Error(w, fmt.Sprintf("扫描进程失败: %v", err), http.StatusInternalServerError)
		return
	}
	if procs == nil {
		procs = []agentproto.DiscoveredProcess{}
	}
	l1Count := len(procs)

	explicitCSG := r.URL.Query().Get("csg_addr")
	_, fused, fuseErr := s.discoverCSGAndFuse(r.Context(), explicitCSG, procs, true)
	if fuseErr == nil && len(fused) > 0 {
		procs = fused
	}
	if s.cfg.Verbose {
		s.logf("[Agent][Discover] L1扫描TCP进程=%d, CSG端点=%q, 融合后总进程=%d, err=%v", l1Count, s.resolveEffectiveCSGAddr(explicitCSG), len(procs), fuseErr)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"items": procs,
	})
}

func (s *AgentServer) handleSyncMonitors(w http.ResponseWriter, r *http.Request) {
	var req agentproto.SyncMonitorReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("[Agent] [HTTP 400] 录制/重放监控下发 JSON 解析失败: %v", err)
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	log.Printf("[Agent] [HTTP] 收到录制/重放监控下发请求: SessionID=%d, 进程数=%d (RemoteAddr: %s)", req.SessionID, len(req.Processes), r.RemoteAddr)

	s.mu.Lock()
	// SingleTenantGuard: 租约互斥检查
	if s.activeLeaseType != "" && (s.activeLeaseType != "RECORDING" || s.activeRefID != req.SessionID) {
		s.mu.Unlock()
		log.Printf("[Agent] [HTTP 409] 租约冲突: 当前已被 %s (RefID=%d) 占用，拒绝 SessionID=%d", s.activeLeaseType, s.activeRefID, req.SessionID)
		http.Error(w, "Conflict: Agent leased by other task", http.StatusConflict)
		return
	}
	s.activeLeaseType = "RECORDING"
	s.activeRefID = req.SessionID
	s.mu.Unlock()

	diags, err := s.ebpfMgr.SyncMonitors(req.SessionID, req.Processes)
	if err != nil {
		log.Printf("[Agent] [❌ 下发监控失败 (HTTP 503)]: %v (SessionID=%d)", err, req.SessionID)
		http.Error(w, fmt.Sprintf("下发监控失败: %v", err), http.StatusServiceUnavailable)
		return
	}

	if s.transportRegistry != nil {
		pidMap := make(map[uint64]uint32)
		pidsMap := make(map[uint64][]uint32)
		for _, d := range diags {
			pidMap[d.ProcessID] = d.ResolvedPID
			if len(d.ResolvedPIDs) > 0 {
				pidsMap[d.ProcessID] = d.ResolvedPIDs
			}
		}
		var targetProcs []transport.TargetProcess
		for _, p := range req.Processes {
			pids := pidsMap[p.ProcessID]
			if len(pids) == 0 && len(p.TargetPIDs) > 0 {
				pids = p.TargetPIDs
			}
			mainPID := pidMap[p.ProcessID]
			if mainPID == 0 && len(pids) > 0 {
				mainPID = pids[0]
			}
			targetProcs = append(targetProcs, transport.TargetProcess{
				ProcessID:   p.ProcessID,
				PID:         mainPID,
				PIDs:        pids,
				ListenPort:  p.ListenPort,
				CmdPattern:  p.CmdPattern,
				Downstreams: p.Downstreams,
			})
		}
		sink := transport.FrameSinkFunc(func(h agentproto.EnvelopeHeader, payload []byte) error {
			s.recordFramePayload(h, payload)
			s.buffer.Push(agentproto.MarshalEnvelope(h, payload))
			return nil
		})
		if capErr := s.transportRegistry.StartCapture(r.Context(), transport.CaptureSpec{
			SessionOrTaskID: req.SessionID,
			RefKind:         agentproto.RefKindRecordingSession,
			Processes:       targetProcs,
			Sink:            sink,
		}); capErr != nil {
			s.logf("[Agent] [⚠️ transportRegistry.StartCapture 警告]: SessionID=%d, err=%v", req.SessionID, capErr)
		}
	}

	s.logDiagDetails("SyncMonitors", diags, req.Processes)
	s.logf("[Agent] [✓ 监控下发成功]: SessionID=%d, 诊断进程数=%d", req.SessionID, len(diags))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"diagnostics": diags,
	})
}

func (s *AgentServer) handleArmStandby(w http.ResponseWriter, r *http.Request) {
	clientClaim := r.Header.Get(agentproto.HeaderClaimCode)
	if clientClaim != "" && clientClaim != s.claimCode {
		log.Printf("[Agent] [HTTP 403] 待命预挂载认领码不匹配 (RemoteAddr: %s)", r.RemoteAddr)
		http.Error(w, "Forbidden: Invalid ClaimCode", http.StatusForbidden)
		return
	}

	var req agentproto.ArmStandbyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("[Agent] [HTTP 400] 待命预挂载 JSON 解析失败: %v", err)
		http.Error(w, "Invalid JSON payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("[Agent] [HTTP] 收到待命预挂载请求: Revision=%d, 进程数=%d (RemoteAddr: %s)", req.Revision, len(req.Processes), r.RemoteAddr)

	diags, err := s.ebpfMgr.ArmStandby(req.Processes)
	if err != nil {
		log.Printf("[Agent] [❌ 待命预挂载被拒绝 (HTTP 503)]: %v (进程数=%d)", err, len(req.Processes))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":       err.Error(),
			"diagnostics": diags,
		})
		return
	}

	digest := agentproto.ComputeArmedDigest(req.Processes)

	s.mu.Lock()
	s.armedRevision = req.Revision
	s.armedDigest = digest
	s.armedProcesses = req.Processes
	s.latestDiags = diags
	s.mu.Unlock()

	s.logDiagDetails("ArmStandby", diags, req.Processes)
	log.Printf("[Agent] [✓ 待命预挂载生效 (ARMED)]: Revision=%d, 摘要=%s, 就绪进程数=%d", req.Revision, digest, len(diags))

	resp := agentproto.ArmStandbyResp{
		Revision:    req.Revision,
		ArmedDigest: digest,
		Diagnostics: diags,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *AgentServer) handleRemoveProcessMonitor(w http.ResponseWriter, r *http.Request) {
	procIDStr := r.PathValue("processId")
	procID, err := strconv.ParseUint(procIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid processId", http.StatusBadRequest)
		return
	}

	rem, err := s.ebpfMgr.RemoveProcessMonitor(procID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if s.transportRegistry != nil {
		sessID, _ := strconv.ParseUint(r.PathValue("sessionId"), 10, 64)
		_ = s.transportRegistry.RemoveTarget(sessID, procID)
	}

	if rem == 0 {
		s.mu.Lock()
		s.activeLeaseType = ""
		s.activeRefID = 0
		s.mu.Unlock()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"removed":             true,
		"remaining_processes": rem,
	})
}

func (s *AgentServer) handleStopMonitor(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log.Printf("[Agent] [HTTP] 收到停录请求: ActiveSessionID=%d (RemoteAddr: %s)", s.activeRefID, r.RemoteAddr)

	_, dropped, total := s.buffer.Stats()
	bypassed, _, _ := s.ebpfMgr.GetStats()

	// 发送 EVENT_SESSION_DRAINED 终结信号
	drainedEnv := agentproto.EnvelopeHeader{
		Magic:           agentproto.EnvelopeMagic,
		ProtoVer:        agentproto.EnvelopeProtoVer,
		EventType:       agentproto.EventSessionDrained,
		SessionOrTaskID: s.activeRefID,
		CapturedAtNs:    time.Now().UnixNano(),
	}
	s.buffer.Push(agentproto.MarshalEnvelope(drainedEnv, nil))

	// 平滑停止录制：排空在途数据并解绑大流量 Tracepoint，但保留长连接与待命状态 (停录不断连)
	s.ebpfMgr.StopCapture()
	oldRefID := s.activeRefID
	s.activeLeaseType = ""
	s.activeRefID = 0

	if s.transportRegistry != nil {
		_ = s.transportRegistry.StopCapture(r.Context(), oldRefID)
	}

	finalSnap := s.captureStatsSnapshot()
	s.logf("[Agent] [✓ 停录完成]: SessionID=%d, 捕获总帧数=%d, 丢帧=%d, TCP-In=%d, TCP-Out=%d(dlink=%d), Mesh=%d(截断=%d,丢弃=%d), WS已发=%d, 恢复 ARMED 待命状态",
		oldRefID, total, dropped, finalSnap.ingress.CapturedFrames, finalSnap.egress.CapturedFrames, finalSnap.egress.DlinkEvents,
		finalSnap.drt.ValidFrames, finalSnap.drt.TruncatedFrames, finalSnap.drt.DroppedEvents, finalSnap.wsSent)

	resp := agentproto.StopMonitorResp{
		CapturedFrames:    total,
		DroppedFrames:     dropped,
		BypassedSelfCount: bypassed,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *AgentServer) handleExecuteReplay(w http.ResponseWriter, r *http.Request) {
	var req agentproto.ExecuteReplayReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.activeLeaseType != "" && (s.activeLeaseType != "REPLAY" || s.activeRefID != req.TaskID) {
		s.mu.Unlock()
		http.Error(w, "Conflict: Agent leased by other task", http.StatusConflict)
		return
	}
	s.mu.Unlock()

	// 0. 可选 CSG 三模渐进式仿真前置筹备 (CLIENT_INJECT / PUB_STUB / MOCK_SERVER)
	simCtrl, simOut, effectivePort, handled := s.prepareReplayCSG(r.Context(), w, &req)
	if handled {
		return
	}

	// 1. 拦截检查：若目标进程处于 NEED_RECONNECT 或 WAITING_PROC / NOT_FOUND，直接以 HTTP 409 拦截并卸载本次 CSG 仿真资源
	pid, err := s.procScanner.FindPIDByPortAndCmd(effectivePort, req.ProcessSpec.CmdPattern)
	if err != nil || pid == 0 {
		s.finishCSGSim(simCtrl, 0)
		s.replayExec.CloseMockListener()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":        "目标进程未运行，禁止盲目发起回放",
			"status":       "WAITING_PROC",
			"diag_message": fmt.Sprintf("未在端口 %d 检测到符合特征 [%s] 的进程", effectivePort, req.ProcessSpec.CmdPattern),
		})
		return
	}

	var proxyPort uint16
	if s.ebpfMgr != nil && s.ebpfMgr.EgressProxy() != nil {
		proxyPort = s.ebpfMgr.EgressProxy().ProxyPort()
	}
	csgAddr := s.resolveEffectiveCSGAddr(req.CSGAddr)
	if csgAddr == "" {
		csgAddr = s.resolveEffectiveCSGAddr(req.ProcessSpec.CSGAddr)
	}
	var csgPort uint16
	if _, p, ok := csg.ParseCSGAddr(csgAddr); ok {
		csgPort = p
	}
	estConns, needRec, _ := s.procScanner.DiagnoseEstablishedConns(pid, req.ProcessSpec.Downstreams, effectivePort, proxyPort, csgPort)
	if needRec {
		s.finishCSGSim(simCtrl, 0)
		s.replayExec.CloseMockListener()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":        fmt.Sprintf("检测到 %d 条存量长连接未重启，禁止盲目发起回放 (A-01)", estConns),
			"status":       "NEED_RECONNECT",
			"diag_message": fmt.Sprintf("存量连接数 %d > 0，需重启业务进程生效", estConns),
		})
		return
	}

	s.mu.Lock()
	if s.activeLeaseType != "" && (s.activeLeaseType != "REPLAY" || s.activeRefID != req.TaskID) {
		s.mu.Unlock()
		s.finishCSGSim(simCtrl, 0)
		s.replayExec.CloseMockListener()
		http.Error(w, "Conflict: Agent leased by other task", http.StatusConflict)
		return
	}
	doneCh := make(chan struct{})
	s.activeLeaseType = "REPLAY"
	s.activeRefID = req.TaskID
	s.activeCSGSim = simCtrl
	s.activeDone = doneCh
	s.mu.Unlock()

	if simOut != nil && simOut.TargetDialAddr != "" {
		s.replayExec.SetTargetDialOverride(simOut.TargetDialAddr)
	} else {
		s.replayExec.SetTargetDialOverride("")
	}
	if simOut != nil && simOut.MockCSGServer != nil && s.ebpfMgr != nil && s.ebpfMgr.EgressProxy() != nil {
		csgIPStr, csgPortU16, hasCSGAddr := csg.ParseCSGAddr(csgAddr)
		mockCSG := simOut.MockCSGServer
		s.ebpfMgr.EgressProxy().SetCSGHandler(
			func(origIP net.IP, origPort uint16) bool {
				if origPort >= csg.DefaultCSGPortMin && origPort <= csg.DefaultCSGPortMax {
					return true
				}
				return hasCSGAddr && origIP != nil && origIP.String() == csgIPStr && origPort == csgPortU16
			},
			func(conn net.Conn) {
				mockCSG.ServeConn(conn)
			},
		)
	}

	// 2. 将 EgressProxy 切换为 REPLAY_MOCK 模式并注入 Mock 数据 (ADR-12, M13)
	matcher := replay.NewScopedMockMatcher()
	for _, mng := range req.MngMocks {
		if len(mng) > 22 {
			matcher.AddMngMock(mng[22], mng)
		}
	}
	for recID, rules := range req.ScopedEgressMocks {
		for _, rule := range rules {
			_ = matcher.RegisterScopedDownstreamRule(recID, rule)
		}
	}
	for _, rule := range req.GlobalFallbackMocks {
		_ = matcher.RegisterGlobalFallbackRule(rule)
	}
	for _, egMock := range req.EgressMocks {
		if len(egMock) >= 24 {
			parsed, err := crosrpc.ParsePacket(egMock)
			if err == nil {
				svc, op := "", ""
				if parsed.Req != nil {
					svc = parsed.Req.ServiceName
					op = parsed.Req.Operation
				} else if parsed.Rsp != nil {
					svc = parsed.Rsp.ServiceName
					op = parsed.Rsp.Operation
				}
				if svc != "" {
					payload := egMock[parsed.PayloadOffset:]
					matcher.AddGlobalFallback(svc, op, payload, [][]byte{egMock})
				}
			}
		}
	}
	if s.ebpfMgr != nil {
		mockEndpoints := append([]agentproto.DownstreamEndpoint(nil), req.ProcessSpec.Downstreams...)
		for _, snap := range req.CSGSnapshots {
			if snap.HostIP != "" && snap.Port > 0 {
				mockEndpoints = append(mockEndpoints, agentproto.DownstreamEndpoint{
					IP:   snap.HostIP,
					Port: snap.Port,
				})
			}
		}
		if s.replayExec != nil && s.replayExec.MockPort() > 0 {
			mockEndpoints = append(mockEndpoints, agentproto.DownstreamEndpoint{
				IP:   "127.0.0.1",
				Port: s.replayExec.MockPort(),
			})
		}
		s.ebpfMgr.SetReplayMock(req.TaskID, matcher, mockEndpoints)
	}

	// 异步执行重放 (与 EgressProxy 共享同一 matcher 实例，退出时幂等卸载 CSG 桩并释放 REPLAY 租约)
	go func() {
		defer func() {
			s.finishCSGSim(simCtrl, req.TaskID)
			s.mu.Lock()
			if s.activeLeaseType == "REPLAY" && s.activeRefID == req.TaskID {
				s.activeLeaseType = ""
				s.activeRefID = 0
				s.activeCSGSim = nil
				s.activeDone = nil
			}
			s.mu.Unlock()
			close(doneCh)
		}()
		_, _ = s.replayExec.ExecuteWithMatcher(context.Background(), req, matcher)
	}()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ARMED",
		"diagnostics": agentproto.ProcessDiagStatus{
			ProcessID:        req.ProcessSpec.ProcessID,
			Status:           "MONITORING",
			ResolvedPID:      pid,
			EstablishedConns: estConns,
			NeedReconnect:    needRec,
		},
	})
}

func (s *AgentServer) handleGetReplayResult(w http.ResponseWriter, r *http.Request) {
	taskIDStr := r.PathValue("taskId")
	taskID, err := strconv.ParseUint(taskIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid taskId", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	var waitCh chan struct{}
	if s.activeLeaseType == "REPLAY" && s.activeRefID == taskID {
		waitCh = s.activeDone
	}
	s.mu.Unlock()
	if waitCh != nil {
		select {
		case <-waitCh:
		case <-r.Context().Done():
			http.Error(w, "Request cancelled", http.StatusRequestTimeout)
			return
		case <-time.After(150 * time.Millisecond):
			http.Error(w, "Task finalizing", http.StatusNotFound)
			return
		}
	}

	res, ok := s.replayExec.GetResult(taskID)
	if !ok {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (s *AgentServer) handleCancelReplay(w http.ResponseWriter, r *http.Request) {
	taskIDStr := r.PathValue("taskId")
	taskID, _ := strconv.ParseUint(taskIDStr, 10, 64)

	s.replayExec.Cancel(taskID)
	s.mu.Lock()
	var sim *csg.SimController
	if s.activeRefID == taskID {
		s.activeLeaseType = ""
		s.activeRefID = 0
		sim = s.activeCSGSim
		s.activeCSGSim = nil
	}
	s.mu.Unlock()
	s.finishCSGSim(sim, taskID)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"cancelled": true,
	})
}

func (s *AgentServer) handlePurge(w http.ResponseWriter, r *http.Request) {
	log.Printf("[Agent] [HTTP] 收到强制清理与复位请求 (Purge, RemoteAddr: %s)", r.RemoteAddr)
	s.PurgeAll()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"purged": true,
	})
}

func (s *AgentServer) handleStream(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[Agent] [HTTP] WebSocket 协议升级失败: %v", err)
		return
	}
	defer conn.Close()

	s.logf("[Agent] [HTTP] 管控端已建立流量数据流 WebSocket 传输信道 (RemoteAddr: %s)", r.RemoteAddr)

	atomic.StoreInt32(&s.wsActive, 1)
	defer atomic.StoreInt32(&s.wsActive, 0)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var connSent int64
	var closeReason error
	defer func() {
		s.logf("[Agent] [HTTP] WebSocket 流量信道已断开 (RemoteAddr: %s, 本次推送=%d帧, 原因=%v)", r.RemoteAddr, connSent, closeReason)
	}()

	// 异步读取客户端消息 (维持握手探活)
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
			s.touchContact()
		}
	}()

	for {
		item, ok := s.buffer.Pop(ctx)
		if !ok || ctx.Err() != nil {
			closeReason = ctx.Err()
			return
		}

		if err := conn.WriteMessage(websocket.BinaryMessage, item); err != nil {
			closeReason = err
			return
		}
		connSent++
		s.wsSentFrames.Add(1)
	}
}

func (s *AgentServer) logDiagDetails(stage string, diags []agentproto.ProcessDiagStatus, specs []agentproto.ProcessMonitorSpec) {
	specByID := make(map[uint64]agentproto.ProcessMonitorSpec, len(specs))
	for _, sp := range specs {
		specByID[sp.ProcessID] = sp
	}
	for _, d := range diags {
		sp := specByID[d.ProcessID]
		if s.cfg.Verbose {
			s.logf("[Agent][%s] ProcessID=%d Status=%s PIDs=%v ListenPort=%d Cmd=%q Conns=%d NeedReconnect=%v Msg=%q",
				stage, d.ProcessID, d.Status, d.ResolvedPIDs, sp.ListenPort, sp.CmdPattern, d.EstablishedConns, d.NeedReconnect, d.Message)
		} else if d.Status == "NOT_FOUND" || d.NeedReconnect {
			s.logf("[Agent][%s][⚠️] ProcessID=%d Status=%s ListenPort=%d NeedReconnect=%v Msg=%q",
				stage, d.ProcessID, d.Status, sp.ListenPort, d.NeedReconnect, d.Message)
		}
	}
}

// Ensure unused check
var _ = io.EOF
