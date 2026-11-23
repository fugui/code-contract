package rpcmon

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"code-contract/pkg/crosrpc"
)

const (
	defaultMeshClientAuthName = "[P:CodeContractAgent:0]"
	maxMeshHelloLen           = 4096
)

type meshFrameKind int

const (
	meshFrameHello meshFrameKind = iota
	meshFrameData
)

type meshFrame struct {
	kind    meshFrameKind
	head    MeshDataHead
	payload []byte
}

// CrosMeshBackend queries a remote RPCMonitorService over the dophi_mesh data-plane
// protocol (4-message 128B HELLO handshake + 40B MESH_MSG_DATA_HEAD_S + CROS ucProtocol=4),
// reusing a single handshaked TCP connection across operations.
type CrosMeshBackend struct {
	target    string
	timeout   time.Duration
	authName  string
	mu        sync.Mutex
	conn      net.Conn
	reader    *bufio.Reader
	localIPv4 uint32
	localPort uint16
	seq       atomic.Uint64
}

// NewCrosMesh dials addr (supporting "dophi_mesh://ipv4:ip:port[:instID]", "ipv4:ip:port",
// or "ip:port"), performs the 4-message HELLO handshake, and returns a reusable Backend.
func NewCrosMesh(addr string, timeout time.Duration) (Backend, error) {
	target, err := parseMeshDialTarget(addr)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	b := &CrosMeshBackend{
		target:   target,
		timeout:  timeout,
		authName: defaultMeshClientAuthName,
	}
	if err := b.connectLocked(context.Background()); err != nil {
		return nil, err
	}
	return b, nil
}

// Close closes the underlying mesh TCP connection.
func (b *CrosMeshBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeLocked()
}

func (b *CrosMeshBackend) closeLocked() error {
	if b.conn == nil {
		return nil
	}
	err := b.conn.Close()
	b.conn = nil
	b.reader = nil
	return err
}

func (b *CrosMeshBackend) connectLocked(ctx context.Context) error {
	dialTimeout := b.timeout
	if d, ok := ctx.Deadline(); ok {
		if rem := time.Until(d); rem > 0 && rem < dialTimeout {
			dialTimeout = rem
		}
	}
	conn, err := net.DialTimeout("tcp", b.target, dialTimeout)
	if err != nil {
		return fmt.Errorf("rpcmon: dial mesh %s failed: %w", b.target, err)
	}

	deadline := time.Now().Add(b.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return err
	}

	var localIPv4 uint32
	var localPort uint16
	if tcpAddr, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		if ip4 := tcpAddr.IP.To4(); ip4 != nil {
			localIPv4 = binary.BigEndian.Uint32(ip4)
		}
		localPort = uint16(tcpAddr.Port)
	}

	reader := bufio.NewReader(conn)
	if err := handshakeMeshClient(conn, reader, b.authName, localPort); err != nil {
		_ = conn.Close()
		return fmt.Errorf("rpcmon: mesh handshake %s failed: %w", b.target, err)
	}
	_ = conn.SetDeadline(time.Time{})

	b.conn = conn
	b.reader = reader
	b.localIPv4 = localIPv4
	b.localPort = localPort
	return nil
}

func handshakeMeshClient(conn net.Conn, reader *bufio.Reader, name string, localPort uint16) error {
	f, err := readMeshFrame(reader)
	if err != nil {
		return err
	}
	if f.kind != meshFrameHello {
		return fmt.Errorf("expected server hello, got frame kind %d", f.kind)
	}
	h, err := DecodeMeshHello(f.payload)
	if err != nil {
		return err
	}
	if h.Magic != MeshHelloMagic || h.Role != MeshRoleServer || h.Type != MeshHelloTypeSec {
		return fmt.Errorf("unexpected server hello magic=%#x role=%d type=%d", h.Magic, h.Role, h.Type)
	}
	if h.SecOpt != 0 {
		return fmt.Errorf("dophi_mesh endpoint requires SSP/SECLINK encryption (sec_opt=%d), unsupported in pure-Go mode", h.SecOpt)
	}
	if err := sendClientHello(conn, name, localPort); err != nil {
		return err
	}

	f, err = readMeshFrame(reader)
	if err != nil {
		return err
	}
	if f.kind != meshFrameHello {
		return fmt.Errorf("expected server authorize hello, got frame kind %d", f.kind)
	}
	authHello, err := DecodeMeshHello(f.payload)
	if err != nil {
		return err
	}
	if authHello.SecOpt != 0 {
		return fmt.Errorf("dophi_mesh authorize requires SSP/SECLINK encryption (sec_opt=%d), unsupported in pure-Go mode", authHello.SecOpt)
	}
	return sendClientHello(conn, name, localPort)
}

func sendClientHello(conn net.Conn, name string, localPort uint16) error {
	b := EncodeMeshHello(MeshHello{
		Role:           MeshRoleClient,
		Type:           MeshHelloTypeSec,
		Version:        MeshHelloVer00,
		Name:           name,
		AuthLevel:      0,
		AuthorizedRet:  0,
		SecNegoTimeout: 5000,
		InterFd:        uint32(localPort),
		SecOpt:         0,
		SspAuthOK:      0,
	})
	return writeAllBytes(conn, b)
}

func readMeshFrame(reader *bufio.Reader) (meshFrame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(reader, hdr[:]); err != nil {
		return meshFrame{}, err
	}
	if binary.BigEndian.Uint16(hdr[0:2]) == MeshHelloMagic {
		msgLen := int(binary.BigEndian.Uint16(hdr[2:4]))
		if msgLen < MeshHelloLen || msgLen > maxMeshHelloLen {
			return meshFrame{}, fmt.Errorf("rpcmon: bad mesh hello len %d", msgLen)
		}
		buf := make([]byte, msgLen)
		copy(buf, hdr[:])
		if _, err := io.ReadFull(reader, buf[4:]); err != nil {
			return meshFrame{}, err
		}
		return meshFrame{kind: meshFrameHello, payload: buf}, nil
	}

	magic := binary.BigEndian.Uint32(hdr[:])
	if magic == MeshBatchDataMagic {
		return meshFrame{}, fmt.Errorf("rpcmon: unsupported dophi_mesh batch frame (0x2019BBBB)")
	}
	if magic != MeshDataMagic {
		return meshFrame{}, fmt.Errorf("rpcmon: unknown mesh frame magic %#x", magic)
	}

	headBuf := make([]byte, MeshDataHeadLen)
	copy(headBuf, hdr[:])
	if _, err := io.ReadFull(reader, headBuf[4:]); err != nil {
		return meshFrame{}, err
	}
	dh, err := DecodeMeshDataHead(headBuf)
	if err != nil {
		return meshFrame{}, err
	}
	if dh.HeadSize != MeshDataHeadLen {
		return meshFrame{}, fmt.Errorf("rpcmon: unexpected mesh data head size %d", dh.HeadSize)
	}
	if int(dh.MessageSize) > crosrpc.DefaultMaxCaptureBytes {
		return meshFrame{}, fmt.Errorf("rpcmon: mesh frame size %d exceeds max limit %d", dh.MessageSize, crosrpc.DefaultMaxCaptureBytes)
	}
	payload := make([]byte, dh.MessageSize)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return meshFrame{}, err
	}
	return meshFrame{kind: meshFrameData, head: dh, payload: payload}, nil
}

func writeAllBytes(w io.Writer, parts ...[]byte) error {
	for _, p := range parts {
		for len(p) > 0 {
			n, err := w.Write(p)
			if err != nil {
				return err
			}
			p = p[n:]
		}
	}
	return nil
}

func (b *CrosMeshBackend) call(ctx context.Context, op string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.conn == nil {
		if err := b.connectLocked(ctx); err != nil {
			return nil, err
		}
	}

	deadline := time.Now().Add(b.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := b.conn.SetDeadline(deadline); err != nil {
		_ = b.closeLocked()
		return nil, err
	}
	defer func() {
		if b.conn != nil {
			_ = b.conn.SetDeadline(time.Time{})
		}
	}()

	timeoutMs := uint32(time.Until(deadline) / time.Millisecond)
	if timeoutMs == 0 {
		timeoutMs = uint32(b.timeout / time.Millisecond)
	}

	sno := b.seq.Add(1)
	crosReq := BuildCrosMeshReqFrame(sno, ServiceName, op, timeoutMs, nil)
	dataHead := EncodeMeshDataHead(MeshDataHead{
		MessageSize: uint32(len(crosReq)),
		CheckType:   MeshCheckNone,
		SrcIPv4:     b.localIPv4,
		SrcPort:     b.localPort,
		Timestamp:   uint64(time.Now().UnixNano()),
	})
	if err := writeAllBytes(b.conn, dataHead, crosReq); err != nil {
		_ = b.closeLocked()
		return nil, err
	}

	for {
		if err := ctx.Err(); err != nil {
			_ = b.closeLocked()
			return nil, err
		}
		f, err := readMeshFrame(b.reader)
		if err != nil {
			_ = b.closeLocked()
			return nil, err
		}
		if f.kind != meshFrameData {
			continue
		}
		pkt, err := crosrpc.ParsePacket(f.payload)
		if err != nil {
			_ = b.closeLocked()
			return nil, fmt.Errorf("rpcmon: invalid CROS mesh response frame: %w", err)
		}
		if pkt.Comm.TransportProto != crosrpc.TransportMesh || (pkt.Comm.MsgType != crosrpc.MsgTypeRsp && pkt.Comm.MsgType != crosrpc.MsgTypeRspSeg) {
			continue
		}
		if pkt.Comm.SNO != sno {
			continue
		}
		if pkt.Rsp != nil {
			var status32 uint32
			if len(f.payload) >= crosrpc.CommHeadLen+8 {
				status32 = binary.BigEndian.Uint32(f.payload[crosrpc.CommHeadLen+4 : crosrpc.CommHeadLen+8])
			}
			if pkt.Rsp.StatusCode != 0 || status32 != 0 {
				return nil, fmt.Errorf("rpcmon: %s returned status=%d/%d", op, status32, pkt.Rsp.StatusCode)
			}
		}
		return append([]byte(nil), f.payload[pkt.PayloadOffset:]...), nil
	}
}

// PortInfo calls RPCMonitorService.GetRpcPortInfo over dophi_mesh.
func (b *CrosMeshBackend) PortInfo(ctx context.Context) ([]PortUsage, int64, error) {
	raw, err := b.call(ctx, OpGetRpcPortInfo)
	if err != nil {
		return nil, 0, err
	}
	return DecodePortInfoResponse(raw)
}

// ServerNum calls RPCMonitorService.GetRpcServerNumInfo over dophi_mesh.
func (b *CrosMeshBackend) ServerNum(ctx context.Context) (uint32, error) {
	raw, err := b.call(ctx, OpGetRpcServerNumInfo)
	if err != nil {
		return 0, err
	}
	return DecodeUInt32Type(raw)
}

// ServerDetail calls RPCMonitorService.GetRpcServerDetailInfo over dophi_mesh.
func (b *CrosMeshBackend) ServerDetail(ctx context.Context) ([]ServerEntry, int64, error) {
	raw, err := b.call(ctx, OpGetRpcServerDetailInfo)
	if err != nil {
		return nil, 0, err
	}
	return DecodeServerDetailResponse(raw)
}

// ClientNum calls RPCMonitorService.GetRpcClientNumInfo over dophi_mesh.
func (b *CrosMeshBackend) ClientNum(ctx context.Context) (uint32, error) {
	raw, err := b.call(ctx, OpGetRpcClientNumInfo)
	if err != nil {
		return 0, err
	}
	return DecodeUInt32Type(raw)
}

// ClientDetail calls RPCMonitorService.GetRpcClientDetailInfo over dophi_mesh.
func (b *CrosMeshBackend) ClientDetail(ctx context.Context) ([]ClientEntry, int64, error) {
	raw, err := b.call(ctx, OpGetRpcClientDetailInfo)
	if err != nil {
		return nil, 0, err
	}
	return DecodeClientDetailResponse(raw)
}

func parseMeshDialTarget(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("rpcmon: empty mesh address")
	}
	s = strings.TrimPrefix(s, "dophi_mesh://")
	return normalizeHostPortTarget(s)
}

func normalizeHostPortTarget(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "tcp://")
	s = strings.TrimPrefix(s, "ipv4:")
	parts := strings.Split(s, ":")
	if len(parts) >= 2 {
		ip := net.ParseIP(parts[0])
		if ip == nil || ip.To4() == nil {
			return "", fmt.Errorf("rpcmon: invalid IPv4 address in %q", raw)
		}
		port, err := strconv.Atoi(parts[1])
		if err != nil || port <= 0 || port > 65535 {
			return "", fmt.Errorf("rpcmon: invalid TCP port in %q", raw)
		}
		return net.JoinHostPort(ip.To4().String(), strconv.Itoa(port)), nil
	}
	return "", fmt.Errorf("rpcmon: invalid host:port target %q", raw)
}
