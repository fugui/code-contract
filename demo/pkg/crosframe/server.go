package crosframe

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"code-contract/pkg/crosrpc"
	"code-contract/pkg/csgdlink"
)

// HandlerFunc 处理接收到的 CROS RPC 请求并返回业务状态码与应答载荷
type HandlerFunc func(ctx context.Context, packet *crosrpc.ParsedPacket, reqPayload []byte) (statusCode uint32, rspPayload []byte, err error)

type publishedService struct {
	serviceName string
	instAddr    string
}

// Server 基于 DOPHI CROS RPC 规范的 TCP 业务服务容器
type Server struct {
	addr        string
	csgAddr     string
	listener    net.Listener
	handlers    map[string]map[string]HandlerFunc
	mu          sync.RWMutex
	running     int32
	ctx         context.Context
	cancel      context.CancelFunc
	activeWg    sync.WaitGroup
	activeConns map[net.Conn]struct{}
	csgClient   *csgdlink.Client
	published   []publishedService
	LogPrefix   string
}

// NewServer 实例化 CROS RPC 服务端
func NewServer(addr string, logPrefix string) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		addr:        addr,
		handlers:    make(map[string]map[string]HandlerFunc),
		activeConns: make(map[net.Conn]struct{}),
		ctx:         ctx,
		cancel:      cancel,
		LogPrefix:   logPrefix,
	}
}

// SetCSGAddr 配置 CSG 控制面 dlink 端点，非空时将在 Start 自动执行 PUB 并在 Stop 自动 UNPUB
func (s *Server) SetCSGAddr(csgAddr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.csgAddr = strings.TrimSpace(csgAddr)
}

// CSGAddr 返回当前配置的 CSG 控制面端点
func (s *Server) CSGAddr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.csgAddr
}

// Register 注册业务 Handler，绑定 (serviceName, operation)
func (s *Server) Register(serviceName, operation string, h HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.handlers[serviceName]; !ok {
		s.handlers[serviceName] = make(map[string]HandlerFunc)
	}
	s.handlers[serviceName][operation] = h
}

// Start 启动 TCP 监听并异步分发处理连接；若配置了 CSGAddr 则同步向 CSG 注册发布所有服务
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("crosframe.Server: listen %s failed: %w", s.addr, err)
	}
	s.listener = ln
	atomic.StoreInt32(&s.running, 1)

	s.activeWg.Add(1)
	go s.acceptLoop()

	if err := s.publishRegisteredServices(); err != nil {
		_ = s.Stop()
		return err
	}

	if s.LogPrefix != "" {
		log.Printf("[%s] CROS RPC TCP Server started on %s", s.LogPrefix, ln.Addr().String())
	}
	return nil
}

func (s *Server) publishRegisteredServices() error {
	s.mu.RLock()
	csgAddr := s.csgAddr
	svcNames := make([]string, 0, len(s.handlers))
	for svc := range s.handlers {
		svcNames = append(svcNames, svc)
	}
	s.mu.RUnlock()

	if csgAddr == "" || len(svcNames) == 0 || s.listener == nil {
		return nil
	}
	sort.Strings(svcNames)

	advertiseAddr := s.listener.Addr().String()
	if host, port, err := net.SplitHostPort(advertiseAddr); err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" {
			advertiseAddr = net.JoinHostPort("127.0.0.1", port)
		}
	}
	instAddr := "tcp://ipv4:" + advertiseAddr

	cli, err := csgdlink.Dial(csgAddr, 3*time.Second)
	if err != nil {
		return fmt.Errorf("crosframe.Server: dial CSG %s failed: %w", csgAddr, err)
	}
	if _, err := cli.Register(3 * time.Second); err != nil {
		_ = cli.Close()
		return fmt.Errorf("crosframe.Server: CSG register failed: %w", err)
	}
	if _, err := cli.ClientID(3 * time.Second); err != nil {
		_ = cli.Close()
		return fmt.Errorf("crosframe.Server: CSG client_id failed: %w", err)
	}

	pubs := make([]publishedService, 0, len(svcNames))
	for _, svc := range svcNames {
		if err := cli.Pub(svc, instAddr, 3*time.Second); err != nil {
			for _, prev := range pubs {
				_ = cli.Unpub(prev.serviceName, prev.instAddr, 2*time.Second)
			}
			_ = cli.Close()
			return fmt.Errorf("crosframe.Server: CSG pub %s failed: %w", svc, err)
		}
		pubs = append(pubs, publishedService{serviceName: svc, instAddr: instAddr})
		if s.LogPrefix != "" {
			log.Printf("[%s] Published to CSG (%s): %s -> %s", s.LogPrefix, csgAddr, svc, instAddr)
		}
	}

	s.mu.Lock()
	s.csgClient = cli
	s.published = pubs
	s.mu.Unlock()
	return nil
}

func (s *Server) unpublishFromCSG() {
	s.mu.Lock()
	cli := s.csgClient
	pubs := append([]publishedService(nil), s.published...)
	s.csgClient = nil
	s.published = nil
	s.mu.Unlock()

	if cli == nil {
		return
	}
	for _, p := range pubs {
		_ = cli.Unpub(p.serviceName, p.instAddr, 2*time.Second)
	}
	_ = cli.Close()
}

// Addr 返回监听地址
func (s *Server) Addr() net.Addr {
	if s.listener != nil {
		return s.listener.Addr()
	}
	return nil
}

func (s *Server) acceptLoop() {
	defer s.activeWg.Done()
	for atomic.LoadInt32(&s.running) == 1 {
		conn, err := s.listener.Accept()
		if err != nil {
			if atomic.LoadInt32(&s.running) == 0 {
				return
			}
			log.Printf("[%s] accept error: %v", s.LogPrefix, err)
			continue
		}

		s.activeWg.Add(1)
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.activeWg.Done()
	s.mu.Lock()
	if s.activeConns != nil {
		s.activeConns[conn] = struct{}{}
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.activeConns, conn)
		s.mu.Unlock()
		_ = conn.Close()
	}()

	framer := crosrpc.NewStreamFramer(4 * 1024 * 1024)
	readBuf := make([]byte, 8192)

	for atomic.LoadInt32(&s.running) == 1 {
		n, err := conn.Read(readBuf)
		if err != nil {
			if err != io.EOF && atomic.LoadInt32(&s.running) == 1 {
				// 客户端断开连接或网络故障
			}
			return
		}

		frames, err := framer.Feed(readBuf[:n])
		if err != nil {
			log.Printf("[%s] framer feed error: %v", s.LogPrefix, err)
			return
		}

		for _, frame := range frames {
			s.dispatchFrame(conn, frame.Data)
		}
	}
}

func (s *Server) dispatchFrame(conn net.Conn, frameData []byte) {
	packet, err := crosrpc.ParsePacket(frameData)
	if err != nil {
		log.Printf("[%s] ParsePacket failed: %v", s.LogPrefix, err)
		return
	}

	if packet.Comm.MsgType != crosrpc.MsgTypeReq || packet.Req == nil {
		// 仅响应 REQ 报文
		return
	}

	serviceName := packet.Req.ServiceName
	operation := packet.Req.Operation

	s.mu.RLock()
	var handler HandlerFunc
	if ops, ok := s.handlers[serviceName]; ok {
		handler = ops[operation]
	}
	s.mu.RUnlock()

	var reqPayload []byte
	if packet.PayloadOffset < len(frameData) {
		reqPayload = frameData[packet.PayloadOffset:]
	}

	var statusCode uint32
	var rspPayload []byte
	if handler == nil {
		statusCode = 404 // 接口未找到
	} else {
		code, out, err := handler(s.ctx, packet, reqPayload)
		if err != nil {
			log.Printf("[%s] handler error for (%s, %s): %v", s.LogPrefix, serviceName, operation, err)
			statusCode = 500
		} else {
			statusCode = code
			rspPayload = out
		}
	}

	// 拼装 RSP 报文帧回传
	rspFrame := BuildRspPacket(packet.Comm.SNO, serviceName, operation, statusCode, packet.Trace, rspPayload)
	if _, err := conn.Write(rspFrame); err != nil {
		log.Printf("[%s] conn.Write response error: %v", s.LogPrefix, err)
	}
}

// Stop 停止监听与连接处理，并从 CSG 注销已发布服务
func (s *Server) Stop() error {
	if !atomic.CompareAndSwapInt32(&s.running, 1, 0) {
		return nil
	}
	s.unpublishFromCSG()
	s.cancel()
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.activeConns))
	for c := range s.activeConns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	s.activeWg.Wait()
	return nil
}
