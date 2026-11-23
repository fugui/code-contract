package rpcmon

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"code-contract/pkg/crosrpc"
)

const (
	// ServiceName is the published CROS service name for RPCMonitorService.
	ServiceName              = "RPCMonitorService-RPCMonitorService"
	OpGetRpcPortInfo         = "GetRpcPortInfo"
	OpGetRpcServerNumInfo    = "GetRpcServerNumInfo"
	OpGetRpcServerDetailInfo = "GetRpcServerDetailInfo"
	OpGetRpcClientNumInfo    = "GetRpcClientNumInfo"
	OpGetRpcClientDetailInfo = "GetRpcClientDetailInfo"

	ReqHeadV05Len = 256
	RspHeadV04Len = 200
)

// BuildCrosReqFrame encodes a standard "CSE.TCP" V05 CROS RPC request frame.
func BuildCrosReqFrame(sno uint64, serviceName, operation string, payload []byte) []byte {
	var magic [7]byte
	copy(magic[:], crosrpc.MagicPrefix)
	comm := crosrpc.CommHead{
		Magic:          magic,
		TransportProto: crosrpc.TransportTCP,
		SNO:            sno,
		Length:         uint32(ReqHeadV05Len + len(payload)),
		HeadLen:        ReqHeadV05Len,
		MsgType:        crosrpc.MsgTypeReq,
	}

	frame := make([]byte, crosrpc.CommHeadLen+ReqHeadV05Len+len(payload))
	copy(frame[:crosrpc.CommHeadLen], comm.Marshal())

	req := frame[crosrpc.CommHeadLen : crosrpc.CommHeadLen+ReqHeadV05Len]
	binary.BigEndian.PutUint16(req[0:2], 5)
	binary.BigEndian.PutUint16(req[2:4], ReqHeadV05Len)
	copyFixedCString(req[8:72], serviceName)
	copyFixedCString(req[72:136], operation)
	req[136] = crosrpc.ReqTypeSync
	binary.BigEndian.PutUint16(req[138:140], 1)

	if len(payload) > 0 {
		copy(frame[crosrpc.CommHeadLen+ReqHeadV05Len:], payload)
	}
	return frame
}

// BuildCrosRspFrame encodes a standard "CSE.TCP" V04 CROS RPC response frame.
func BuildCrosRspFrame(sno uint64, serviceName, operation string, status uint64, payload []byte) []byte {
	var magic [7]byte
	copy(magic[:], crosrpc.MagicPrefix)
	comm := crosrpc.CommHead{
		Magic:          magic,
		TransportProto: crosrpc.TransportTCP,
		SNO:            sno,
		Length:         uint32(RspHeadV04Len + len(payload)),
		HeadLen:        RspHeadV04Len,
		MsgType:        crosrpc.MsgTypeRsp,
	}

	frame := make([]byte, crosrpc.CommHeadLen+RspHeadV04Len+len(payload))
	copy(frame[:crosrpc.CommHeadLen], comm.Marshal())

	rsp := frame[crosrpc.CommHeadLen : crosrpc.CommHeadLen+RspHeadV04Len]
	binary.BigEndian.PutUint16(rsp[0:2], 4)
	binary.BigEndian.PutUint16(rsp[2:4], RspHeadV04Len)
	binary.BigEndian.PutUint32(rsp[4:8], uint32(status))
	copyFixedCString(rsp[56:120], serviceName)
	copyFixedCString(rsp[120:184], operation)
	binary.BigEndian.PutUint64(rsp[184:192], status)

	if len(payload) > 0 {
		copy(frame[crosrpc.CommHeadLen+RspHeadV04Len:], payload)
	}
	return frame
}

func copyFixedCString(dst []byte, s string) {
	if len(dst) == 0 {
		return
	}
	copy(dst[:len(dst)-1], []byte(s))
}

// CrosTCPBackend queries a remote RPCMonitorService over standard CROS RPC TCP.
type CrosTCPBackend struct {
	addr    string
	timeout time.Duration
	mu      sync.Mutex
	conn    net.Conn
	framer  *crosrpc.StreamFramer
	seq     atomic.Uint64
}

// NewCrosTCP opens a standard CROS RPC TCP session to an RPCMonitorService listener.
func NewCrosTCP(addr string, timeout time.Duration) (Backend, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	dialAddr := addr
	if normalized, err := normalizeHostPortTarget(addr); err == nil {
		dialAddr = normalized
	}
	conn, err := net.DialTimeout("tcp", dialAddr, timeout)
	if err != nil {
		return nil, err
	}
	return &CrosTCPBackend{
		addr:    dialAddr,
		timeout: timeout,
		conn:    conn,
		framer:  crosrpc.NewStreamFramer(crosrpc.DefaultMaxCaptureBytes),
	}, nil
}

// Close closes the underlying TCP connection.
func (b *CrosTCPBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return nil
	}
	err := b.conn.Close()
	b.conn = nil
	return err
}

func (b *CrosTCPBackend) call(ctx context.Context, op string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.conn == nil {
		conn, err := net.DialTimeout("tcp", b.addr, b.timeout)
		if err != nil {
			return nil, err
		}
		b.conn = conn
		b.framer = crosrpc.NewStreamFramer(crosrpc.DefaultMaxCaptureBytes)
	}

	deadline := time.Now().Add(b.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := b.conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	defer func() {
		if b.conn != nil {
			_ = b.conn.SetDeadline(time.Time{})
		}
	}()

	sno := b.seq.Add(1)
	req := BuildCrosReqFrame(sno, ServiceName, op, nil)
	if _, err := b.conn.Write(req); err != nil {
		_ = b.conn.Close()
		b.conn = nil
		return nil, err
	}

	buf := make([]byte, 16*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := b.conn.Read(buf)
		if err != nil {
			_ = b.conn.Close()
			b.conn = nil
			return nil, err
		}
		frames, err := b.framer.Feed(buf[:n])
		if err != nil {
			return nil, err
		}
		for _, f := range frames {
			pkt, err := crosrpc.ParsePacket(f.Data)
			if err != nil {
				return nil, fmt.Errorf("rpcmon: invalid CROS response frame: %w", err)
			}
			if pkt.Comm.SNO != sno {
				continue
			}
			if pkt.Rsp != nil && pkt.Rsp.StatusCode != 0 {
				return nil, fmt.Errorf("rpcmon: %s failed with status=%d", op, pkt.Rsp.StatusCode)
			}
			return append([]byte(nil), f.Data[pkt.PayloadOffset:]...), nil
		}
	}
}

// PortInfo calls RPCMonitorService.GetRpcPortInfo.
func (b *CrosTCPBackend) PortInfo(ctx context.Context) ([]PortUsage, int64, error) {
	raw, err := b.call(ctx, OpGetRpcPortInfo)
	if err != nil {
		return nil, 0, err
	}
	return DecodePortInfoResponse(raw)
}

// ServerNum calls RPCMonitorService.GetRpcServerNumInfo.
func (b *CrosTCPBackend) ServerNum(ctx context.Context) (uint32, error) {
	raw, err := b.call(ctx, OpGetRpcServerNumInfo)
	if err != nil {
		return 0, err
	}
	return DecodeUInt32Type(raw)
}

// ServerDetail calls RPCMonitorService.GetRpcServerDetailInfo.
func (b *CrosTCPBackend) ServerDetail(ctx context.Context) ([]ServerEntry, int64, error) {
	raw, err := b.call(ctx, OpGetRpcServerDetailInfo)
	if err != nil {
		return nil, 0, err
	}
	return DecodeServerDetailResponse(raw)
}

// ClientNum calls RPCMonitorService.GetRpcClientNumInfo.
func (b *CrosTCPBackend) ClientNum(ctx context.Context) (uint32, error) {
	raw, err := b.call(ctx, OpGetRpcClientNumInfo)
	if err != nil {
		return 0, err
	}
	return DecodeUInt32Type(raw)
}

// ClientDetail calls RPCMonitorService.GetRpcClientDetailInfo.
func (b *CrosTCPBackend) ClientDetail(ctx context.Context) ([]ClientEntry, int64, error) {
	raw, err := b.call(ctx, OpGetRpcClientDetailInfo)
	if err != nil {
		return nil, 0, err
	}
	return DecodeClientDetailResponse(raw)
}
