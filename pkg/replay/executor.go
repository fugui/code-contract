package replay

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
)

// ReplayExecutor 离线隔离重放执行器
type ReplayExecutor struct {
	mu           sync.Mutex
	activeTasks  map[uint64]context.CancelFunc
	taskResults  map[uint64]*agentproto.ReplayTaskResultResp
	mockListener net.Listener
	mockPort     uint16
	dialAddr     string
}

// NewReplayExecutor 创建重放执行器
func NewReplayExecutor() *ReplayExecutor {
	return &ReplayExecutor{
		activeTasks: make(map[uint64]context.CancelFunc),
		taskResults: make(map[uint64]*agentproto.ReplayTaskResultResp),
	}
}

// MockPort 获取当前 Mock 监听端口
func (e *ReplayExecutor) MockPort() uint16 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mockPort
}

// SetTargetDialOverride 设置入站重放连接目标覆盖地址 (如 CLIENT_INJECT 动态解析到的 ip:port)
func (e *ReplayExecutor) SetTargetDialOverride(addr string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.dialAddr = addr
}

// EnsureMockListener 在重放启动前预绑定本地 MockServer 监听端口供 CSG PUB_STUB 注册使用
func (e *ReplayExecutor) EnsureMockListener() (uint16, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.mockListener != nil && e.mockPort > 0 {
		return e.mockPort, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("预绑定 MockServer 监听失败: %w", err)
	}
	e.mockListener = ln
	e.mockPort = uint16(ln.Addr().(*net.TCPAddr).Port)
	return e.mockPort, nil
}

// CloseMockListener 释放尚未进入 ExecuteWithMatcher 的预绑定 MockListener
func (e *ReplayExecutor) CloseMockListener() {
	e.mu.Lock()
	ln := e.mockListener
	e.mockListener = nil
	e.mockPort = 0
	e.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

// AttachCSGSandboxReport 将 CSG 仿真与 5 出口自洁报告附加到已完成的重放任务结果上
func (e *ReplayExecutor) AttachCSGSandboxReport(taskID uint64, report *agentproto.CSGSandboxReport, errCode string) {
	if report == nil && errCode == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	res, ok := e.taskResults[taskID]
	if !ok || res == nil {
		return
	}
	res.CSGSandboxReport = report
	if errCode != "" {
		res.ErrorCode = errCode
	}
}

// Execute 执行重放任务
func (e *ReplayExecutor) Execute(ctx context.Context, req agentproto.ExecuteReplayReq) (*agentproto.ReplayTaskResultResp, error) {
	return e.ExecuteWithMatcher(ctx, req, nil)
}

// ExecuteWithMatcher 执行重放任务，支持传入外部共享的 Mock 匹配器 (如与 EgressProxy 共享，ADR-12, M13)
func (e *ReplayExecutor) ExecuteWithMatcher(ctx context.Context, req agentproto.ExecuteReplayReq, existingMatcher *ScopedMockMatcher) (*agentproto.ReplayTaskResultResp, error) {
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	e.mu.Lock()
	e.activeTasks[req.TaskID] = cancel
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		delete(e.activeTasks, req.TaskID)
		e.mu.Unlock()
	}()

	timeout := 30 * time.Second
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}

	// 1. 初始化 Mock 匹配器
	matcher := existingMatcher
	if matcher == nil {
		matcher = NewScopedMockMatcher()
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
		for i, eg := range req.EgressMocks {
			parsed, err := crosrpc.ParsePacket(eg)
			if err == nil {
				recID := uint64(i + 1)
				svc, op := "", ""
				if parsed.Req != nil {
					svc = parsed.Req.ServiceName
					op = parsed.Req.Operation
				} else if parsed.Rsp != nil {
					svc = parsed.Rsp.ServiceName
					op = parsed.Rsp.Operation
				}
				if svc != "" {
					var payload []byte
					if len(eg) > parsed.PayloadOffset {
						payload = eg[parsed.PayloadOffset:]
					}
					matcher.AddScopedMock(recID, svc, op, payload, [][]byte{eg})
					matcher.AddGlobalFallback(svc, op, payload, [][]byte{eg})
				}
			}
		}
	}

	if len(req.ProcessSpec.Downstreams) > 0 {
		epTags := make(map[string]string)
		for _, ds := range req.ProcessSpec.Downstreams {
			if ds.DstTag != "" {
				addr := net.JoinHostPort(ds.IP, strconv.Itoa(int(ds.Port)))
				epTags[addr] = ds.DstTag
				if ds.IP != "127.0.0.1" {
					epTags[net.JoinHostPort("127.0.0.1", strconv.Itoa(int(ds.Port)))] = ds.DstTag
				}
			}
		}
		matcher.SetEndpointTags(epTags)
	}

	// 2. 启动或复用预绑定的本地 MockServer
	e.mu.Lock()
	mockLn := e.mockListener
	e.mu.Unlock()
	if mockLn == nil {
		var lErr error
		mockLn, lErr = net.Listen("tcp", "127.0.0.1:0")
		if lErr != nil {
			return nil, fmt.Errorf("启动 MockServer 失败: %w", lErr)
		}
		e.mu.Lock()
		e.mockListener = mockLn
		e.mockPort = uint16(mockLn.Addr().(*net.TCPAddr).Port)
		e.mu.Unlock()
	}
	matcher.AddStubEndpoint(mockLn.Addr().String())
	if port := e.MockPort(); port > 0 {
		matcher.AddStubEndpoint(net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	}
	defer func() {
		_ = mockLn.Close()
		e.mu.Lock()
		if e.mockListener == mockLn {
			e.mockListener = nil
			e.mockPort = 0
		}
		e.mu.Unlock()
	}()

	// 异步监听出站 Mock 连接
	go func() {
		for {
			conn, err := mockLn.Accept()
			if err != nil {
				return
			}
			go e.handleMockConn(taskCtx, conn, matcher)
		}
	}()

	// 3. 连接目标业务进程
	targetAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(req.ProcessSpec.ListenPort)))
	e.mu.Lock()
	overrideAddr := e.dialAddr
	e.mu.Unlock()
	if overrideAddr != "" {
		targetAddr = overrideAddr
	}
	dialer := net.Dialer{Timeout: 3 * time.Second}
	targetConn, err := dialer.DialContext(taskCtx, "tcp", targetAddr)
	if err != nil {
		res := &agentproto.ReplayTaskResultResp{
			TaskID:  req.TaskID,
			Status:  "ERROR",
			Details: fmt.Sprintf("连接目标进程端口 %d 失败: %v", req.ProcessSpec.ListenPort, err),
		}
		e.saveResult(req.TaskID, res)
		return res, nil
	}
	defer targetConn.Close()

	// 发送前置 MNG 消息
	for _, mng := range req.MngMocks {
		mngToSend := NormalizeFrameTransportForTCP(mng)
		if _, err := targetConn.Write(mngToSend); err != nil {
			res := &agentproto.ReplayTaskResultResp{
				TaskID:  req.TaskID,
				Status:  "ERROR",
				Details: fmt.Sprintf("写入 MNG 消息失败: %v", err),
			}
			e.saveResult(req.TaskID, res)
			return res, nil
		}
	}

	type reqItem struct {
		recordID uint64
		rawReq   []byte
	}
	var items []reqItem
	if len(req.IngressItems) > 0 {
		items = make([]reqItem, len(req.IngressItems))
		for i, it := range req.IngressItems {
			items[i] = reqItem{recordID: it.RecordID, rawReq: it.RawReq}
		}
	} else {
		items = make([]reqItem, len(req.IngressRequests))
		for i, r := range req.IngressRequests {
			items[i] = reqItem{recordID: uint64(i + 1), rawReq: r}
		}
	}

	total := len(items)
	passed := 0
	framer := crosrpc.NewStreamFramer(crosrpc.MaxPayloadLen)

	// 4. 逐条注入入站请求并等待应答
	for i, item := range items {
		select {
		case <-taskCtx.Done():
			res := &agentproto.ReplayTaskResultResp{
				TaskID:        req.TaskID,
				Status:        "CANCELLED",
				TotalRequests: total,
				Details:       "任务已被手动取消",
			}
			e.saveResult(req.TaskID, res)
			return res, nil
		default:
		}

		matcher.SetActiveRecordID(item.recordID)

		reqToSend := NormalizeFrameTransportForTCP(item.rawReq)
		if _, err := targetConn.Write(reqToSend); err != nil {
			res := &agentproto.ReplayTaskResultResp{
				TaskID:         req.TaskID,
				Status:         "ERROR",
				TotalRequests:  total,
				PassedRequests: passed,
				MockHitCount:   matcher.MockHitCount(),
				MockMissCount:  matcher.MockMissCount(),
				Details:        fmt.Sprintf("请求 #%d (RecordID=%d) 写入失败: %v", i+1, item.recordID, err),
			}
			e.saveResult(req.TaskID, res)
			return res, nil
		}

		// 检查是否为 Oneway 请求
		var reqType uint8
		if len(item.rawReq) > 24+136 {
			reqType = item.rawReq[24+136]
		}
		if reqType == crosrpc.ReqTypeOneway {
			passed++
			continue
		}

		expectedSNO := binary.BigEndian.Uint64(item.rawReq[8:16])
		_ = targetConn.SetReadDeadline(time.Now().Add(timeout))

		var gotRsp bool
		buf := make([]byte, 65536)
		for !gotRsp {
			n, err := targetConn.Read(buf)
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
					break
				}
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					res := &agentproto.ReplayTaskResultResp{
						TaskID:         req.TaskID,
						Status:         "TIMEOUT",
						TotalRequests:  total,
						PassedRequests: passed,
						MockHitCount:   matcher.MockHitCount(),
						MockMissCount:  matcher.MockMissCount(),
						Details:        fmt.Sprintf("请求 #%d 等待响应超时 (%v)", i+1, timeout),
					}
					e.saveResult(req.TaskID, res)
					return res, nil
				}
				break
			}

			frames, fErr := framer.Feed(buf[:n])
			if fErr != nil {
				continue
			}
			for _, frame := range frames {
				frameData := frame.Data
				if len(frameData) >= 16 {
					sno := binary.BigEndian.Uint64(frameData[8:16])
					if sno == expectedSNO {
						msgType := frameData[22]
						if msgType == crosrpc.MsgTypeRsp {
							gotRsp = true
							passed++
							break
						}
					}
				}
			}
		}
	}

	status := "PASS"
	if matcher.MockMissCount() > 0 {
		status = "MOCK_MISS"
	} else if passed < total {
		status = "FAILED"
	}

	result := &agentproto.ReplayTaskResultResp{
		TaskID:              req.TaskID,
		Status:              status,
		TotalRequests:       total,
		PassedRequests:      passed,
		MockHitCount:        matcher.MockHitCount(),
		MockMissCount:       matcher.MockMissCount(),
		ObservedEgressCalls: matcher.ObservedEgressCalls(),
		Details:             fmt.Sprintf("重放完成: 总计 %d, 成功 %d, Mock命中 %d, Mock未命中 %d", total, passed, matcher.MockHitCount(), matcher.MockMissCount()),
	}
	e.saveResult(req.TaskID, result)
	return result, nil
}

func (e *ReplayExecutor) handleMockConn(ctx context.Context, conn net.Conn, matcher *ScopedMockMatcher) {
	defer conn.Close()
	dstAddr := ""
	if conn.LocalAddr() != nil {
		dstAddr = conn.LocalAddr().String()
	}
	framer := crosrpc.NewStreamFramer(crosrpc.MaxPayloadLen)
	buf := make([]byte, 32768)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			return
		}

		frames, err := framer.Feed(buf[:n])
		if err != nil {
			continue
		}

		for _, frame := range frames {
			matchRes, mErr := matcher.MatchAndHealWithDst(frame.Data, dstAddr)
			if mErr != nil || matchRes == nil {
				continue
			}

			if matchRes.Result == MatchMiss {
				// MOCK_MISS 保持静默，严禁向客户端写回任何字节，等待目标进程自身超时
				continue
			}

			for _, rspFrame := range matchRes.ResponseFrames {
				if _, wErr := conn.Write(rspFrame); wErr != nil {
					return
				}
			}
		}
	}
}

// Cancel 取消任务
func (e *ReplayExecutor) Cancel(taskID uint64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	cancel, ok := e.activeTasks[taskID]
	if ok {
		cancel()
		delete(e.activeTasks, taskID)
		return true
	}
	return false
}

// GetResult 获取任务结果
func (e *ReplayExecutor) GetResult(taskID uint64) (*agentproto.ReplayTaskResultResp, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	res, ok := e.taskResults[taskID]
	return res, ok
}

func (e *ReplayExecutor) saveResult(taskID uint64, res *agentproto.ReplayTaskResultResp) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.taskResults[taskID] = res
}
