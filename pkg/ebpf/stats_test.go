package ebpf

import (
	"encoding/binary"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
)

func buildTestCrosFrame(proto, msgType uint8, sno uint64, body []byte) []byte {
	headLen := crosrpc.ReqHeadMinLen
	if msgType == crosrpc.MsgTypeRsp || msgType == crosrpc.MsgTypeRspSeg {
		headLen = crosrpc.RspHeadMinLen
	}
	totalBodyLen := uint32(headLen + len(body))
	pkt := make([]byte, crosrpc.CommHeadLen+int(totalBodyLen))
	copy(pkt[0:7], []byte(crosrpc.MagicPrefix))
	pkt[7] = proto
	binary.BigEndian.PutUint64(pkt[8:16], sno)
	binary.BigEndian.PutUint32(pkt[16:20], totalBodyLen)
	binary.BigEndian.PutUint16(pkt[20:22], uint16(headLen))
	pkt[22] = msgType
	pkt[23] = 1
	copy(pkt[crosrpc.CommHeadLen+headLen:], body)
	return pkt
}

func TestGapAwareIngressReader_Stats(t *testing.T) {
	pool := NewGlobalFramerBudgetPool(4 * 1024 * 1024)
	reader := NewGapAwareIngressReader(pool, 4*1024*1024)

	req := buildTestCrosFrame(crosrpc.TransportTCP, crosrpc.MsgTypeReq, 101, []byte("req-body"))
	rsp := buildTestCrosFrame(crosrpc.TransportTCP, crosrpc.MsgTypeRsp, 101, []byte("rsp-body"))

	_, err := reader.FeedChunk(IngressChunk{
		SessionID: 1, ProcessID: 10, PID: 1001, Direction: 1, StreamOff: 0, Data: req,
	})
	if err != nil {
		t.Fatalf("FeedChunk req failed: %v", err)
	}

	// 制造一次 StreamOff Gap 后送入 RSP
	_, err = reader.FeedChunk(IngressChunk{
		SessionID: 1, ProcessID: 10, PID: 1001, Direction: 1, StreamOff: uint64(len(req)) + 64, Data: rsp,
	})
	if err != nil {
		t.Fatalf("FeedChunk rsp failed: %v", err)
	}

	st := reader.Stats()
	if st.CapturedFrames != 2 || st.ReqFrames != 1 || st.RspFrames != 1 || st.GapCount != 1 {
		t.Fatalf("unexpected IngressStats: %+v", st)
	}
}

func TestEgressProxy_Stats_CaptureAndNatMiss(t *testing.T) {
	// 启动真实回声下游用于测试 EgressProxy CAPTURING 转发
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen backend failed: %v", err)
	}
	defer backendLn.Close()
	backendAddr := backendLn.Addr().(*net.TCPAddr)

	rspPkt := buildTestCrosFrame(crosrpc.TransportTCP, crosrpc.MsgTypeRsp, 202, []byte("backend-rsp"))
	go func() {
		for {
			conn, aErr := backendLn.Accept()
			if aErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				if n, rErr := c.Read(buf); rErr == nil && n > 0 {
					_, _ = c.Write(rspPkt)
				}
			}(conn)
		}
	}()

	var hitNat atomicBoolSwitch
	proxy, err := NewEgressProxy(func(clientPort uint16) (uint64, uint32, net.IP, uint16, bool) {
		if !hitNat.get() {
			return 0, 0, nil, 0, false
		}
		return 10, 2001, net.IPv4(127, 0, 0, 1), uint16(backendAddr.Port), true
	}, func(env []byte) {})
	if err != nil {
		t.Fatalf("NewEgressProxy failed: %v", err)
	}
	defer proxy.Close()
	proxy.SetVerbose(true)
	proxy.StartCapture(99)

	proxyAddr := net.JoinHostPort("127.0.0.1", itoaPort(proxy.ProxyPort()))

	// 1. 触发一次 NAT 未命中
	cMiss, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy miss failed: %v", err)
	}
	_ = cMiss.Close()

	// 等待服务端处理 NAT 未命中
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if proxy.Stats().NatMiss >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if proxy.Stats().NatMiss < 1 {
		t.Fatalf("expected NatMiss >= 1, got %+v", proxy.Stats())
	}

	// 2. 命中 NAT 并完成一次 REQ + RSP 透传采集
	hitNat.set(true)
	cHit, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy hit failed: %v", err)
	}
	reqPkt := buildTestCrosFrame(crosrpc.TransportTCP, crosrpc.MsgTypeReq, 202, []byte("client-req"))
	if _, err := cHit.Write(reqPkt); err != nil {
		t.Fatalf("write req failed: %v", err)
	}
	buf := make([]byte, 4096)
	_ = cHit.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := cHit.Read(buf); err != nil {
		t.Fatalf("read rsp failed: %v", err)
	}
	_ = cHit.Close()

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := proxy.Stats()
		if st.CapturedFrames >= 2 && st.ReqFrames >= 1 && st.RspFrames >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := proxy.Stats()
	if st.CapturedFrames != 2 || st.ReqFrames != 1 || st.RspFrames != 1 {
		t.Fatalf("unexpected EgressStats: %+v", st)
	}

	var _ = agentproto.DirectionEgress
}

type atomicBoolSwitch struct {
	v atomic.Bool
}

func (a *atomicBoolSwitch) set(v bool) {
	a.v.Store(v)
}

func (a *atomicBoolSwitch) get() bool {
	return a.v.Load()
}

func itoaPort(p uint16) string {
	return strconv.Itoa(int(p))
}
