package crosframe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"code-contract/pkg/crosrpc"
	"code-contract/pkg/csgdlink"
)

// Client 遵循 DOPHI CROS RPC 规范的 TCP 客户端，支持直连与 CSG dlink 动态服务发现
type Client struct {
	targetAddr  string
	csgAddr     string
	serviceName string
	timeout     time.Duration
	snoSeq      uint64
	mu          sync.Mutex
	conn        net.Conn
	framer      *crosrpc.StreamFramer
	presenceCli *csgdlink.Client
	resolved    string
}

// NewClient 实例化直连静态目标地址的 CROS RPC 客户端
func NewClient(targetAddr string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		targetAddr: strings.TrimSpace(targetAddr),
		timeout:    timeout,
		snoSeq:     1000,
	}
}

// NewCSGClient 实例化基于 CSG dlink 动态服务发现的 CROS RPC 客户端；
// 启动时建立持久化订阅会话以便 RPCMonitorService 观测拓扑关系，实际拨号按需通过 CSG SUB 解析最新实例地址。
func NewCSGClient(csgAddr, serviceName, fallbackAddr string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	c := &Client{
		targetAddr:  strings.TrimSpace(fallbackAddr),
		csgAddr:     strings.TrimSpace(csgAddr),
		serviceName: strings.TrimSpace(serviceName),
		timeout:     timeout,
		snoSeq:      1000,
	}
	if c.csgAddr != "" && c.serviceName != "" {
		c.initCSGPresence()
	}
	return c
}

func (c *Client) initCSGPresence() {
	cli, err := csgdlink.Dial(c.csgAddr, c.timeout)
	if err != nil {
		log.Printf("[crosframe.Client] CSG presence dial %s (%s) skipped: %v", c.csgAddr, c.serviceName, err)
		return
	}
	if _, err := cli.Register(c.timeout); err != nil {
		_ = cli.Close()
		log.Printf("[crosframe.Client] CSG presence register (%s) failed: %v", c.serviceName, err)
		return
	}
	if _, err := cli.ClientID(c.timeout); err != nil {
		_ = cli.Close()
		log.Printf("[crosframe.Client] CSG presence client_id (%s) failed: %v", c.serviceName, err)
		return
	}
	insts, err := cli.Sub(c.serviceName, c.timeout)
	if err != nil {
		_ = cli.Close()
		log.Printf("[crosframe.Client] CSG presence sub (%s) failed: %v", c.serviceName, err)
		return
	}
	c.presenceCli = cli
	for _, inst := range insts {
		if csgdlink.IsTCPAddr(inst.Addr) {
			if hp, ok := csgdlink.ParseTCPTarget(inst.Addr); ok {
				c.resolved = hp
				break
			}
		}
	}
}

// CSGAddr 返回配置的 CSG 控制面端点
func (c *Client) CSGAddr() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.csgAddr
}

// ResolvedAddr 返回最近一次通过 CSG 解析或直连的目标地址
func (c *Client) ResolvedAddr() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resolved != "" {
		return c.resolved
	}
	return c.targetAddr
}

// NextSNO 获取下一个自增 SNO
func (c *Client) NextSNO() uint64 {
	return atomic.AddUint64(&c.snoSeq, 1)
}

func (c *Client) resolveViaCSG() (string, error) {
	hp, err := csgdlink.ResolveServiceTCP(c.csgAddr, c.serviceName, c.timeout)
	if err != nil {
		return "", fmt.Errorf("crosframe.Client: %w", err)
	}
	return hp, nil
}

func (c *Client) getOrCreateConn() (net.Conn, bool, error) {
	if c.conn != nil {
		return c.conn, true, nil
	}
	dialAddr := c.targetAddr
	if c.csgAddr != "" && c.serviceName != "" {
		addr, err := c.resolveViaCSG()
		if err != nil {
			return nil, false, err
		}
		dialAddr = addr
	}
	if dialAddr == "" {
		return nil, false, fmt.Errorf("crosframe.Client: empty target address for service %s", c.serviceName)
	}
	conn, err := net.DialTimeout("tcp", dialAddr, c.timeout)
	if err != nil {
		return nil, false, fmt.Errorf("crosframe.Client: connect %s failed: %w", dialAddr, err)
	}
	c.conn = conn
	c.resolved = dialAddr
	c.framer = crosrpc.NewStreamFramer(4 * 1024 * 1024)
	return c.conn, false, nil
}

// Invoke 发起同步 CROS RPC 调用，写入请求帧并等待应答返回；
// 若复用长连接因对端重启/退网而断开，将在 CSG 模式下自动重新解析并重试一次。
func (c *Client) Invoke(ctx context.Context, serviceName, operation string, trace *crosrpc.TraceKeyExt, reqPayload []byte) (*crosrpc.ParsedPacket, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for attempt := 0; attempt < 2; attempt++ {
		conn, reused, err := c.getOrCreateConn()
		if err != nil {
			return nil, nil, err
		}
		pkt, rspPayload, retryable, err := c.invokeOnce(ctx, conn, serviceName, operation, trace, reqPayload)
		if err == nil {
			return pkt, rspPayload, nil
		}
		if reused && retryable && attempt == 0 && ctx.Err() == nil {
			continue
		}
		return nil, nil, err
	}
	return nil, nil, fmt.Errorf("crosframe.Client: invoke %s.%s failed", serviceName, operation)
}

func (c *Client) invokeOnce(ctx context.Context, conn net.Conn, serviceName, operation string, trace *crosrpc.TraceKeyExt, reqPayload []byte) (*crosrpc.ParsedPacket, []byte, bool, error) {
	sno := c.NextSNO()
	reqFrame := BuildReqPacket(sno, serviceName, operation, trace, reqPayload)

	deadline := time.Now().Add(c.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write(reqFrame); err != nil {
		_ = conn.Close()
		c.conn = nil
		return nil, nil, true, fmt.Errorf("crosframe.Client: write request failed: %w", err)
	}

	readBuf := make([]byte, 8192)
	for {
		select {
		case <-ctx.Done():
			return nil, nil, false, ctx.Err()
		default:
		}

		n, err := conn.Read(readBuf)
		if err != nil {
			_ = conn.Close()
			c.conn = nil
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return nil, nil, false, fmt.Errorf("crosframe.Client: read response timeout: %w", err)
			}
			if err == io.EOF {
				return nil, nil, true, fmt.Errorf("crosframe.Client: server closed connection")
			}
			return nil, nil, true, fmt.Errorf("crosframe.Client: read response failed: %w", err)
		}

		frames, err := c.framer.Feed(readBuf[:n])
		if err != nil {
			_ = conn.Close()
			c.conn = nil
			return nil, nil, false, fmt.Errorf("crosframe.Client: framer feed error: %w", err)
		}

		for _, frame := range frames {
			pkt, err := crosrpc.ParsePacket(frame.Data)
			if err != nil {
				continue
			}
			if pkt.Comm.MsgType == crosrpc.MsgTypeRsp || pkt.Comm.MsgType == crosrpc.MsgTypeRspSeg {
				if pkt.Comm.SNO == sno {
					var rspPayload []byte
					if pkt.PayloadOffset < len(frame.Data) {
						rspPayload = frame.Data[pkt.PayloadOffset:]
					}
					return pkt, rspPayload, false, nil
				}
			}
		}
	}
}

// Close 关闭底层数据面连接与 CSG 订阅会话
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var firstErr error
	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			firstErr = err
		}
		c.conn = nil
	}
	if c.presenceCli != nil {
		if c.serviceName != "" {
			_ = c.presenceCli.Unsub(c.serviceName, 2*time.Second)
		}
		if err := c.presenceCli.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		c.presenceCli = nil
	}
	return firstErr
}
