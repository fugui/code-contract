package rpcmon

import (
	"bufio"
	"encoding/binary"
	"errors"
	"net"
	"time"

	"code-contract/pkg/crosrpc"
)

// Server exposes RPCMonitorService over standard CROS RPC TCP or dophi_mesh backed by a Provider.
type Server struct {
	provider Provider
}

// NewServer constructs an RPCMonitorService server wrapping prov.
func NewServer(prov Provider) *Server {
	return &Server{provider: prov}
}

// Serve accepts incoming connections on ln and serves RPCMonitorService requests over CROS TCP.
func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.ServeConn(conn)
	}
}

// ServeMesh accepts incoming connections on ln and serves RPCMonitorService requests over dophi_mesh.
func (s *Server) ServeMesh(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.ServeMeshConn(conn)
	}
}

// ServeConn serves RPCMonitorService CROS RPC frames on a single TCP connection.
func (s *Server) ServeConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	framer := crosrpc.NewStreamFramer(crosrpc.DefaultMaxCaptureBytes)
	buf := make([]byte, 16*1024)

	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		frames, err := framer.Feed(buf[:n])
		if err != nil {
			return
		}
		for _, f := range frames {
			pkt, err := crosrpc.ParsePacket(f.Data)
			if err != nil || pkt.Req == nil {
				continue
			}
			rsp := s.handlePacketProto(pkt, crosrpc.TransportTCP)
			if _, err := conn.Write(rsp); err != nil {
				return
			}
		}
	}
}

// ServeMeshConn completes the 4-message 128B HELLO handshake and serves CROS-over-Mesh frames.
func (s *Server) ServeMeshConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)

	var localIPv4 uint32
	var localPort uint16
	if tcpAddr, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		if ip4 := tcpAddr.IP.To4(); ip4 != nil {
			localIPv4 = binary.BigEndian.Uint32(ip4)
		}
		localPort = uint16(tcpAddr.Port)
	}

	// 1. S -> C hello, C -> S hello
	hello1 := EncodeMeshHello(MeshHello{
		Role:           MeshRoleServer,
		Type:           MeshHelloTypeSec,
		Version:        MeshHelloVer00,
		Name:           "[P:MockDophiServer:0]",
		SecNegoTimeout: 5000,
		InterFd:        uint32(localPort),
	})
	if err := writeAllBytes(conn, hello1); err != nil {
		return
	}
	f, err := readMeshFrame(reader)
	if err != nil || f.kind != meshFrameHello {
		return
	}

	// 2. S -> C authorize hello, C -> S authorize hello
	if err := writeAllBytes(conn, hello1); err != nil {
		return
	}
	f, err = readMeshFrame(reader)
	if err != nil || f.kind != meshFrameHello {
		return
	}

	for {
		f, err := readMeshFrame(reader)
		if err != nil {
			return
		}
		if f.kind != meshFrameData {
			continue
		}
		pkt, err := crosrpc.ParsePacket(f.payload)
		if err != nil || pkt.Req == nil {
			continue
		}
		crosRsp := s.handlePacketProto(pkt, crosrpc.TransportMesh)
		dataHead := EncodeMeshDataHead(MeshDataHead{
			MessageSize: uint32(len(crosRsp)),
			CheckType:   MeshCheckNone,
			SrcIPv4:     localIPv4,
			SrcPort:     localPort,
			Timestamp:   uint64(time.Now().UnixNano()),
		})
		if err := writeAllBytes(conn, dataHead, crosRsp); err != nil {
			return
		}
	}
}

func (s *Server) handlePacketProto(pkt *crosrpc.ParsedPacket, transportProto uint8) []byte {
	req := pkt.Req
	buildRsp := BuildCrosRspFrame
	if transportProto == crosrpc.TransportMesh {
		buildRsp = BuildCrosMeshRspFrame
	}

	if req.ServiceName != "" &&
		req.ServiceName != ServiceName &&
		req.ServiceName != "RPCMonitorService" &&
		req.ServiceName != "swp.rpc.RPCMonitorService" {
		return buildRsp(pkt.Comm.SNO, req.ServiceName, req.Operation, 1, nil)
	}

	snap := s.provider.Snapshot()
	now := time.Now().Unix()

	switch req.Operation {
	case OpGetRpcPortInfo:
		return buildRsp(pkt.Comm.SNO, req.ServiceName, req.Operation, 0, EncodePortInfoResponse(snap.Ports, now))
	case OpGetRpcServerNumInfo:
		return buildRsp(pkt.Comm.SNO, req.ServiceName, req.Operation, 0, EncodeUInt32Type(snap.ServerNum()))
	case OpGetRpcServerDetailInfo:
		return buildRsp(pkt.Comm.SNO, req.ServiceName, req.Operation, 0, EncodeServerDetailResponse(snap.Servers, now))
	case OpGetRpcClientNumInfo:
		return buildRsp(pkt.Comm.SNO, req.ServiceName, req.Operation, 0, EncodeUInt32Type(snap.ClientNum()))
	case OpGetRpcClientDetailInfo:
		return buildRsp(pkt.Comm.SNO, req.ServiceName, req.Operation, 0, EncodeClientDetailResponse(snap.Clients, now))
	default:
		return buildRsp(pkt.Comm.SNO, req.ServiceName, req.Operation, 2, nil)
	}
}
