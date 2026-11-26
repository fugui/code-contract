package ebpf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
	"code-contract/pkg/csgdlink"
	"code-contract/pkg/replay"
)

// ProxyMode EgressProxy 运行模式
type ProxyMode int32

const (
	// ModePassthrough 待命透传模式（默认）：零应用层协议解析，零额外缓冲区申请，停录不断连
	ModePassthrough ProxyMode = 0
	// ModeCapturing 会话录制模式：分帧采集入信封流，有序排空
	ModeCapturing ProxyMode = 1
	// ModeReplayMock 回放 Mock 接管模式：命中 Mock 下游端点主动阻断真实后端连接 (0 字节外泄)
	ModeReplayMock ProxyMode = 2
)

func (m ProxyMode) String() string {
	switch m {
	case ModePassthrough:
		return "PASSTHROUGH"
	case ModeCapturing:
		return "CAPTURING"
	case ModeReplayMock:
		return "REPLAY_MOCK"
	default:
		return "UNKNOWN"
	}
}

// NatLookupFunc 查找客户端临时端口对应的原始目的地
type NatLookupFunc func(clientPort uint16) (processID uint64, pid uint32, origIP net.IP, origPort uint16, ok bool)

// NatAddrLookupFunc 按客户端源 IP 与临时端口查找原始目的地（支持多网卡显式 bind 非 127.0.0.1 源 IP）
type NatAddrLookupFunc func(clientIP net.IP, clientPort uint16) (processID uint64, pid uint32, origIP net.IP, origPort uint16, ok bool)

// NatMissDiagFunc 在 NAT 未命中时返回内核态/Map 诊断摘要（仅用于排障日志，严禁输出业务 payload）
type NatMissDiagFunc func(clientIP net.IP, clientPort uint16) string

// FrameSinkFunc 接收代理采集到的出站 CROS RPC 信封帧
type FrameSinkFunc func(envelopeBytes []byte)

var proxyBufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 32768)
		return &b
	},
}

// EgressStats 出站代理累计统计快照
type EgressStats struct {
	CapturedFrames  int64
	ReqFrames       int64
	RspFrames       int64
	MngFrames       int64
	TruncatedFrames int64
	DlinkEvents     int64
	ActiveConns     int64
	NatMiss         int64
	DialFail        int64
	MockHit         int64
	MockMiss        int64
}

// EgressProxy 透明 TCP 出站代理（三态隔离与零泄漏不断连机制）
type EgressProxy struct {
	mu                sync.RWMutex
	listener          net.Listener
	proxyPort         uint16
	natLookup         NatLookupFunc
	natAddrLookup     NatAddrLookupFunc
	natMissDiag       NatMissDiagFunc
	frameSink         FrameSinkFunc
	activeConns       sync.Map // net.Conn -> bool
	connSeqGen        uint32
	closed            chan struct{}
	statCaptured      int64
	statDlinkEvents   int64
	statReqFrames     atomic.Int64
	statRspFrames     atomic.Int64
	statMngFrames     atomic.Int64
	statTruncated     atomic.Int64
	statActiveConns   atomic.Int64
	statNatMiss       atomic.Int64
	statDialFail      atomic.Int64
	statMockHit       atomic.Int64
	statMockMiss      atomic.Int64
	verbose           atomic.Bool
	natMissLogMu      sync.Mutex
	natMissLogged     int64
	natMissSuppressed int64
	lastNatMissLogAt  time.Time
	mode              int32 // ProxyMode (atomic)
	activeSessionID   atomic.Uint64
	mockMatcher       *replay.ScopedMockMatcher
	mockEndpoints     map[string]bool // "IP:Port" -> true
	csgMatchFn        func(origIP net.IP, origPort uint16) bool
	csgServeFn        func(conn net.Conn)
	draining          int32 // atomic flag: 1 = graceful draining
}

// NewEgressProxy 创建出站代理，默认为 ModePassthrough 模式
func NewEgressProxy(natLookup NatLookupFunc, frameSink FrameSinkFunc) (*EgressProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("绑定本地出站代理端口失败: %w", err)
	}

	p := &EgressProxy{
		listener:      ln,
		proxyPort:     uint16(ln.Addr().(*net.TCPAddr).Port),
		natLookup:     natLookup,
		frameSink:     frameSink,
		closed:        make(chan struct{}),
		mode:          int32(ModePassthrough),
		mockEndpoints: make(map[string]bool),
	}

	go p.acceptLoop()
	return p, nil
}

// ProxyPort 获取当前监听端口
func (p *EgressProxy) ProxyPort() uint16 {
	return p.proxyPort
}

// SetVerbose 设置是否输出详细诊断日志（不打印业务 payload）
func (p *EgressProxy) SetVerbose(v bool) {
	if p == nil {
		return
	}
	p.verbose.Store(v)
}

// SetNatAddrLookup 注入带客户端源 IP 的 NAT 反查回调（优先于旧版仅端口回调）
func (p *EgressProxy) SetNatAddrLookup(fn NatAddrLookupFunc) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.natAddrLookup = fn
}

// SetNatMissDiag 注入 NAT 未命中时的内核 Map 诊断摘要回调
func (p *EgressProxy) SetNatMissDiag(fn NatMissDiagFunc) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.natMissDiag = fn
}

func (p *EgressProxy) shouldLogNatMiss() (bool, int64) {
	p.natMissLogMu.Lock()
	defer p.natMissLogMu.Unlock()

	if p.natMissLogged < 5 {
		p.natMissLogged++
		p.lastNatMissLogAt = time.Now()
		return true, 0
	}
	minInterval := 5 * time.Second
	if p.verbose.Load() {
		minInterval = 1 * time.Second
	}
	now := time.Now()
	if now.Sub(p.lastNatMissLogAt) < minInterval {
		p.natMissSuppressed++
		return false, 0
	}
	suppressed := p.natMissSuppressed
	p.natMissSuppressed = 0
	p.natMissLogged++
	p.lastNatMissLogAt = now
	return true, suppressed
}

// Stats 返回出站代理累计统计快照
func (p *EgressProxy) Stats() EgressStats {
	if p == nil {
		return EgressStats{}
	}
	return EgressStats{
		CapturedFrames:  atomic.LoadInt64(&p.statCaptured),
		ReqFrames:       p.statReqFrames.Load(),
		RspFrames:       p.statRspFrames.Load(),
		MngFrames:       p.statMngFrames.Load(),
		TruncatedFrames: p.statTruncated.Load(),
		DlinkEvents:     atomic.LoadInt64(&p.statDlinkEvents),
		ActiveConns:     p.statActiveConns.Load(),
		NatMiss:         p.statNatMiss.Load(),
		DialFail:        p.statDialFail.Load(),
		MockHit:         p.statMockHit.Load(),
		MockMiss:        p.statMockMiss.Load(),
	}
}

// Mode 获取当前代理模式
func (p *EgressProxy) Mode() ProxyMode {
	return ProxyMode(atomic.LoadInt32(&p.mode))
}

// SetMode 设置当前代理模式
func (p *EgressProxy) SetMode(mode ProxyMode) {
	if mode == ModeCapturing && p.activeSessionID.Load() == 0 {
		p.activeSessionID.Store(1)
	}
	atomic.StoreInt32(&p.mode, int32(mode))
}

// SetMockMatcher 注入回放 Mock 匹配器及 Mock 下游端点清单 (ADR-09)
func (p *EgressProxy) SetMockMatcher(matcher *replay.ScopedMockMatcher, mockEndpoints []agentproto.DownstreamEndpoint) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.mockMatcher = matcher
	p.mockEndpoints = make(map[string]bool)
	for _, ep := range mockEndpoints {
		p.mockEndpoints[fmt.Sprintf("%s:%d", ep.IP, ep.Port)] = true
	}
}

// StartCapture 开启录制并原子注入 sessionID (F-09, ADR-11)
func (p *EgressProxy) StartCapture(sessionID uint64) {
	p.activeSessionID.Store(sessionID)
	p.SetMode(ModeCapturing)
}

// ActiveSessionID 获取当前录制会话 ID
func (p *EgressProxy) ActiveSessionID() uint64 {
	return p.activeSessionID.Load()
}

// DrainAndSetPassthrough 会话停止时等待排空在途帧并平滑切回透传待命模式 (停录不断连)
func (p *EgressProxy) DrainAndSetPassthrough(drainDuration time.Duration) {
	if drainDuration <= 0 {
		drainDuration = 200 * time.Millisecond
	}
	time.Sleep(drainDuration)
	p.SetMode(ModePassthrough)
	p.activeSessionID.Store(0)
}

// isMockTarget 判断目标端点是否属于被 Mock 拦截的服务
func (p *EgressProxy) isMockTarget(ip net.IP, port uint16) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	key := fmt.Sprintf("%s:%d", ip.String(), port)
	if p.mockEndpoints[key] {
		return true
	}
	// 兼容 127.0.0.1 本地回环
	keyLocal := fmt.Sprintf("127.0.0.1:%d", port)
	return p.mockEndpoints[keyLocal]
}

// Close 强制关闭代理服务及现有连接
func (p *EgressProxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	select {
	case <-p.closed:
		return nil
	default:
		close(p.closed)
	}

	err := p.listener.Close()
	p.activeConns.Range(func(key, value interface{}) bool {
		if c, ok := key.(io.Closer); ok {
			_ = c.Close()
		}
		return true
	})
	return err
}

// GracefulDrain 优雅排空：关闭监听器禁止新连接，存量连接允许在超时内优雅自然退出
func (p *EgressProxy) GracefulDrain(timeout time.Duration) error {
	if !atomic.CompareAndSwapInt32(&p.draining, 0, 1) {
		return nil
	}

	p.mu.Lock()
	select {
	case <-p.closed:
		p.mu.Unlock()
		return nil
	default:
		close(p.closed)
	}
	err := p.listener.Close()
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		hasActive := false
		p.activeConns.Range(func(_, _ interface{}) bool {
			hasActive = true
			return false
		})

		if !hasActive {
			break
		}

		select {
		case <-ctx.Done():
			// 超时后强制释放
			p.activeConns.Range(func(key, _ interface{}) bool {
				if c, ok := key.(io.Closer); ok {
					_ = c.Close()
				}
				return true
			})
			return nil
		case <-ticker.C:
		}
	}

	return err
}

func (p *EgressProxy) acceptLoop() {
	for {
		clientConn, err := p.listener.Accept()
		if err != nil {
			select {
			case <-p.closed:
				return
			default:
				time.Sleep(10 * time.Millisecond)
				continue
			}
		}

		go p.handleConnection(clientConn)
	}
}

func (p *EgressProxy) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	clientAddr, ok := clientConn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return
	}
	clientIP := clientAddr.IP
	clientPort := uint16(clientAddr.Port)

	// 查 NAT 还原原始目的地址
	var (
		processID uint64
		pid       uint32
		origIP    net.IP
		origPort  uint16
	)

	p.mu.RLock()
	addrLookup := p.natAddrLookup
	legacyLookup := p.natLookup
	missDiag := p.natMissDiag
	p.mu.RUnlock()

	if addrLookup != nil {
		processID, pid, origIP, origPort, ok = addrLookup(clientIP, clientPort)
	} else if legacyLookup != nil {
		processID, pid, origIP, origPort, ok = legacyLookup(clientPort)
	}
	if !ok || origPort == 0 {
		totalMiss := p.statNatMiss.Add(1)
		if shouldLog, suppressed := p.shouldLogNatMiss(); shouldLog {
			var diagStr string
			if missDiag != nil {
				diagStr = missDiag(clientIP, clientPort)
			}
			localAddrStr := ""
			if clientConn.LocalAddr() != nil {
				localAddrStr = clientConn.LocalAddr().String()
			}
			reason := "lookup_miss"
			if ok && origPort == 0 {
				reason = "orig_port_zero"
			}
			if diagStr != "" {
				log.Printf("[EgressProxy] NAT 未命中，关闭连接 (client=%s:%d local=%s mode=%s reason=%s total=%d suppressed=%d) %s",
					clientIP.String(), clientPort, localAddrStr, p.Mode().String(), reason, totalMiss, suppressed, diagStr)
			} else {
				log.Printf("[EgressProxy] NAT 未命中，关闭连接 (client=%s:%d local=%s mode=%s reason=%s total=%d suppressed=%d)",
					clientIP.String(), clientPort, localAddrStr, p.Mode().String(), reason, totalMiss, suppressed)
			}
		}
		return // 未在 NAT 表找到合法条目，静默关闭
	}

	connSeq := atomic.AddUint32(&p.connSeqGen, 1)

	// 检查当前模式是否为 REPLAY_MOCK
	if p.Mode() == ModeReplayMock {
		if p.tryHandleCSGConn(clientConn, origIP, origPort) {
			return
		}
		if p.isMockTarget(origIP, origPort) {
			// 核心红线：回放 Mock 接管时，绝对禁止向真实 targetConn 发起任何 Dial！
			// 确保 0 字节外泄到真实网络，直接将请求导入用户态 MockMatcher 并执行头部自愈
			p.activeConns.Store(clientConn, true)
			p.statActiveConns.Add(1)
			defer func() {
				p.activeConns.Delete(clientConn)
				p.statActiveConns.Add(-1)
			}()
			if p.verbose.Load() {
				log.Printf("[EgressProxy] Mock 接管连接: pid=%d %d -> %s:%d (connSeq=%d)", pid, clientPort, origIP, origPort, connSeq)
			}

			p.handleMockConnection(clientConn, origIP, origPort)
			return
		}
	}

	// PASSTHROUGH 或 CAPTURING 模式：连接真实下游服务
	targetAddr := net.JoinHostPort(origIP.String(), strconv.Itoa(int(origPort)))
	targetConn, err := net.DialTimeout("tcp", targetAddr, 3*time.Second)
	if err != nil {
		p.statDialFail.Add(1)
		if p.verbose.Load() {
			log.Printf("[EgressProxy] 连接下游失败: pid=%d target=%s err=%v", pid, targetAddr, err)
		}
		if p.Mode() == ModeReplayMock {
			p.activeConns.Store(clientConn, true)
			p.statActiveConns.Add(1)
			defer func() {
				p.activeConns.Delete(clientConn)
				p.statActiveConns.Add(-1)
			}()
			p.handleMockConnection(clientConn, origIP, origPort)
		}
		return
	}
	defer targetConn.Close()

	p.activeConns.Store(clientConn, true)
	p.activeConns.Store(targetConn, true)
	p.statActiveConns.Add(1)
	if p.verbose.Load() {
		log.Printf("[EgressProxy] 建立代理连接: pid=%d :%d -> %s (mode=%s connSeq=%d)", pid, clientPort, targetAddr, p.Mode().String(), connSeq)
	}
	defer func() {
		p.activeConns.Delete(clientConn)
		p.activeConns.Delete(targetConn)
		p.statActiveConns.Add(-1)
		if p.verbose.Load() {
			log.Printf("[EgressProxy] 关闭代理连接: pid=%d :%d -> %s (connSeq=%d)", pid, clientPort, targetAddr, connSeq)
		}
	}()

	var wg sync.WaitGroup
	dlinkState := newDlinkConnState()
	wg.Add(2)

	// 1. Client -> Target (出站请求)
	go func() {
		defer wg.Done()
		defer targetConn.Close()
		p.relayBidirectional(clientConn, targetConn, processID, pid, clientAddr.IP, origIP, clientPort, origPort, connSeq, true, dlinkState)
	}()

	// 2. Target -> Client (出站应答)
	go func() {
		defer wg.Done()
		p.relayBidirectional(targetConn, clientConn, processID, pid, clientAddr.IP, origIP, clientPort, origPort, connSeq, false, dlinkState)
		if p.Mode() != ModeReplayMock {
			_ = clientConn.Close()
		}
	}()

	wg.Wait()
}

type dlinkConnState struct {
	mu      sync.Mutex
	decided bool
	isDlink bool
	obs     *csgdlink.Observer
}

func newDlinkConnState() *dlinkConnState {
	return &dlinkConnState{obs: csgdlink.NewObserver()}
}

func (s *dlinkConnState) detectOrCheck(chunk []byte) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isDlink {
		return true
	}
	if csgdlink.IsDlinkPrefix(chunk) {
		s.isDlink = true
		s.decided = true
		return true
	}
	if !s.decided && len(chunk) >= 4 {
		s.decided = true
	}
	return false
}

// StatDlinkEvents 获取录制期间旁路识别并发射的 dlink 0x08 事件数
func (p *EgressProxy) StatDlinkEvents() int64 {
	return atomic.LoadInt64(&p.statDlinkEvents)
}

// relayBidirectional 双向转发与按需抓包循环 (停录不断连与写前魔术字守卫核心实现)
func (p *EgressProxy) relayBidirectional(
	src, dst net.Conn,
	processID uint64,
	pid uint32,
	clientIP, origIP net.IP,
	clientPort, origPort uint16,
	connSeq uint32,
	isClientToTarget bool,
	dlinkState *dlinkConnState,
) {
	bufPtr := proxyBufPool.Get().(*[]byte)
	defer proxyBufPool.Put(bufPtr)
	buf := *bufPtr

	var framer *crosrpc.StreamFramer
	magicPrefixBytes := []byte(crosrpc.MagicPrefix)

	for {
		select {
		case <-p.closed:
			return
		default:
		}

		n, err := src.Read(buf)
		if n > 0 {
			curMode := p.Mode()

			// 核心防御：REPLAY_MOCK 模式防外泄与自愈处理
			if curMode == ModeReplayMock {
				isMockTarget := p.isMockTarget(origIP, origPort)
				hasCrosMagic := isClientToTarget && bytes.Contains(buf[:n], magicPrefixBytes)

				if isMockTarget || hasCrosMagic {
					if !isClientToTarget {
						// 真实下游发来的数据，在 Mock 接管时直接丢弃并切断 targetConn
						_ = src.Close()
						return
					}

					// 物理切断向真实下游的连接，严守 0 字节外泄
					_ = dst.Close()

					p.mu.RLock()
					matcher := p.mockMatcher
					p.mu.RUnlock()

					if matcher != nil {
						if framer == nil {
							framer = crosrpc.NewStreamFramer(crosrpc.DefaultMaxCaptureBytes)
						}
						origAddr := net.JoinHostPort(origIP.String(), strconv.Itoa(int(origPort)))
						frames, fErr := framer.Feed(buf[:n])
						if fErr == nil && len(frames) > 0 {
							for _, f := range frames {
								matchRes, mErr := matcher.MatchAndHealWithDst(f.Data, origAddr)
								if mErr == nil && matchRes != nil && len(matchRes.ResponseFrames) > 0 {
									p.statMockHit.Add(1)
									if p.verbose.Load() {
										log.Printf("[EgressProxy][REPLAY_MOCK] target=%s hit=true rspFrames=%d", origAddr, len(matchRes.ResponseFrames))
									}
									for _, rsp := range matchRes.ResponseFrames {
										if _, wErr := src.Write(rsp); wErr != nil {
											return
										}
									}
								} else {
									p.statMockMiss.Add(1)
									if p.verbose.Load() {
										log.Printf("[EgressProxy][REPLAY_MOCK] target=%s hit=false err=%v", origAddr, mErr)
									}
								}
							}
						}
					}
					// 已接管出站请求，切勿向 targetConn 写入，继续在 clientConn 循环中响应后续请求
					continue
				}
			}

			// 透明转发真实数据
			if _, wErr := dst.Write(buf[:n]); wErr != nil {
				return
			}

			// 若处于 CAPTURING 录制模式且已注入有效会话：
			// 先判定是否为 CSG "DLNK" 控制面流；若为 "DLNK" 则仅走无侵入 csgdlink.Observer 发射 0x08，
			// 绝不喂给 crosrpc.StreamFramer 避免脏字节误判。
			if curMode == ModeCapturing {
				sessID := p.activeSessionID.Load()
				if sessID > 0 {
					if dlinkState.detectOrCheck(buf[:n]) {
						p.observeDlinkChunk(dlinkState, sessID, processID, pid, clientIP, origIP, clientPort, origPort, connSeq, isClientToTarget, buf[:n])
						continue
					}
					if framer == nil {
						framer = crosrpc.NewStreamFramer(crosrpc.DefaultMaxCaptureBytes)
					}
					arrivalNs := time.Now().UnixNano()
					frames := framer.WriteAt(buf[:n], arrivalNs)
					if len(frames) > 0 && p.frameSink != nil {
						for _, f := range frames {
							flags := uint8(0)
							if f.IsTruncated {
								flags |= agentproto.FlagIsTruncated
								p.statTruncated.Add(1)
							}
							if comm, cErr := crosrpc.ParseCommHead(f.Data); cErr == nil {
								switch {
								case comm.MsgType == crosrpc.MsgTypeReq:
									p.statReqFrames.Add(1)
								case comm.MsgType == crosrpc.MsgTypeRsp || comm.MsgType == crosrpc.MsgTypeRspSeg:
									p.statRspFrames.Add(1)
								case comm.MsgType >= crosrpc.MsgTypeMng:
									p.statMngFrames.Add(1)
								}
							}
							header := agentproto.EnvelopeHeader{
								Magic:           agentproto.EnvelopeMagic,
								ProtoVer:        agentproto.EnvelopeProtoVer,
								EventType:       agentproto.EventTrafficFrame,
								SessionOrTaskID: sessID,
								ProcessID:       processID,
								CapturedPID:     pid,
								Direction:       agentproto.DirectionEgress, // 恒定为 DirectionEgress (2)
								Flags:           flags,
								CapturedAtNs:    f.CapturedAtNs,
								SrcPort:         clientPort,
								DstPort:         origPort,
								ConnSeq:         connSeq,
							}
							header.SetSrcIP(clientIP)
							header.SetDstIP(origIP)

							env := agentproto.MarshalEnvelope(header, f.Data)
							p.frameSink(env)
							atomic.AddInt64(&p.statCaptured, 1)
						}
					}
				}
			} else {
				// PASSTHROUGH 待命模式：释放 framer 缓存，零额外内存开销
				if framer != nil {
					framer = nil
				}
			}
		}

		if err != nil {
			return
		}
	}
}

// handleMockConnection 在回放 Mock 模式下接管客户端长连接 (真实后端 0 连接 0 字节外泄)
func (p *EgressProxy) handleMockConnection(clientConn net.Conn, origIP net.IP, origPort uint16) {
	bufPtr := proxyBufPool.Get().(*[]byte)
	defer proxyBufPool.Put(bufPtr)
	buf := *bufPtr

	framer := crosrpc.NewStreamFramer(crosrpc.DefaultMaxCaptureBytes)

	for {
		select {
		case <-p.closed:
			return
		default:
		}

		n, err := clientConn.Read(buf)
		if n > 0 {
			frames, fErr := framer.Feed(buf[:n])
			if fErr == nil && len(frames) > 0 {
				p.mu.RLock()
				matcher := p.mockMatcher
				p.mu.RUnlock()

				if matcher != nil {
					origAddr := net.JoinHostPort(origIP.String(), strconv.Itoa(int(origPort)))
					for _, f := range frames {
						matchRes, mErr := matcher.MatchAndHealWithDst(f.Data, origAddr)
						if mErr == nil && matchRes != nil && len(matchRes.ResponseFrames) > 0 {
							p.statMockHit.Add(1)
							if p.verbose.Load() {
								log.Printf("[EgressProxy][REPLAY_MOCK] target=%s hit=true rspFrames=%d", origAddr, len(matchRes.ResponseFrames))
							}
							for _, rsp := range matchRes.ResponseFrames {
								if _, wErr := clientConn.Write(rsp); wErr != nil {
									return
								}
							}
						} else {
							p.statMockMiss.Add(1)
							if p.verbose.Load() {
								log.Printf("[EgressProxy][REPLAY_MOCK] target=%s hit=false err=%v", origAddr, mErr)
							}
						}
					}
				}
			}
		}

		if err != nil {
			return
		}
	}
}

// SetCSGHandler injects an in-process CSG dlink interceptor for MOCK_SERVER replay mode.
// When matchFn matches the NAT original destination, connections are routed directly
// to serveFn without dialing the real CSG control-plane endpoint.
func (p *EgressProxy) SetCSGHandler(matchFn func(origIP net.IP, origPort uint16) bool, serveFn func(conn net.Conn)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.csgMatchFn = matchFn
	p.csgServeFn = serveFn
}

func (p *EgressProxy) tryHandleCSGConn(clientConn net.Conn, origIP net.IP, origPort uint16) bool {
	if p.Mode() != ModeReplayMock {
		return false
	}
	p.mu.RLock()
	matchFn := p.csgMatchFn
	serveFn := p.csgServeFn
	p.mu.RUnlock()
	if matchFn == nil || serveFn == nil {
		return false
	}
	if !matchFn(origIP, origPort) {
		return false
	}
	serveFn(clientConn)
	return true
}

// StatCaptured 获取累计捕获帧数
func (p *EgressProxy) StatCaptured() int64 {
	return atomic.LoadInt64(&p.statCaptured)
}

func (p *EgressProxy) observeDlinkChunk(
	state *dlinkConnState,
	sessID, processID uint64,
	pid uint32,
	clientIP, origIP net.IP,
	clientPort, origPort uint16,
	connSeq uint32,
	isClientToTarget bool,
	chunk []byte,
) {
	if state == nil || state.obs == nil || p.frameSink == nil {
		return
	}
	dir := "csg->client"
	if isClientToTarget {
		dir = "client->csg"
	}
	events := state.obs.Feed(dir, chunk)
	if len(events) == 0 {
		return
	}
	nowStr := time.Now().UTC().Format(time.RFC3339)
	for _, ev := range events {
		obs := agentproto.CSGObservation{
			SessionID:   sessID,
			Direction:   ev.Direction,
			SubTypeName: ev.SubTypeName,
			ServiceName: ev.ServiceName,
			ObservedAt:  nowStr,
		}
		for _, inst := range ev.Instances {
			hostIP, port, _ := csgdlink.ParseAddrIPPort(inst.Addr)
			obs.Snapshots = append(obs.Snapshots, agentproto.CSGSnapshotRecord{
				ServiceName: inst.Name64,
				Addr:        inst.Addr,
				Transport:   detectCSGTransport(inst.Addr),
				HostIP:      hostIP,
				Port:        port,
				Source:      "DLINK_OBSERVER",
				ObservedAt:  nowStr,
			})
		}
		payload, err := json.Marshal(obs)
		if err != nil {
			continue
		}
		header := agentproto.EnvelopeHeader{
			Magic:           agentproto.EnvelopeMagic,
			ProtoVer:        agentproto.EnvelopeProtoVer,
			EventType:       agentproto.EventCSGObservation,
			SessionOrTaskID: sessID,
			ProcessID:       processID,
			CapturedPID:     pid,
			Direction:       agentproto.DirectionEgress,
			CapturedAtNs:    time.Now().UnixNano(),
			SrcPort:         clientPort,
			DstPort:         origPort,
			ConnSeq:         connSeq,
		}
		header.SetSrcIP(clientIP)
		header.SetDstIP(origIP)
		p.frameSink(agentproto.MarshalEnvelope(header, payload))
		atomic.AddInt64(&p.statDlinkEvents, 1)
		if p.verbose.Load() {
			log.Printf("[EgressProxy][DLNK] pid=%d dir=%s subtype=%s svc=%s instances=%d", pid, ev.Direction, ev.SubTypeName, ev.ServiceName, len(ev.Instances))
		}
	}
}

func detectCSGTransport(addr string) string {
	switch {
	case csgdlink.IsMeshAddr(addr):
		return "MESH"
	case strings.Contains(addr, "dms://"):
		return "DMS"
	case strings.Contains(addr, "dpuc://"):
		return "UC"
	case strings.Contains(addr, "domain://"):
		return "DOMAIN"
	default:
		return "TCP"
	}
}
