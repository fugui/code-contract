package rpcmon

import (
	"errors"
	"net"
	"time"

	"code-contract/pkg/crosrpc"
)

// Server exposes RPCMonitorService over standard CROS RPC TCP backed by a Provider.
type Server struct {
	provider Provider
}

// NewServer constructs an RPCMonitorService server wrapping prov.
func NewServer(prov Provider) *Server {
	return &Server{provider: prov}
}

// Serve accepts incoming connections on ln and serves RPCMonitorService requests.
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

// ServeConn serves RPCMonitorService CROS RPC frames on a single connection.
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
			rsp := s.handlePacket(pkt)
			if _, err := conn.Write(rsp); err != nil {
				return
			}
		}
	}
}

func (s *Server) handlePacket(pkt *crosrpc.ParsedPacket) []byte {
	req := pkt.Req
	if req.ServiceName != "" &&
		req.ServiceName != ServiceName &&
		req.ServiceName != "RPCMonitorService" &&
		req.ServiceName != "swp.rpc.RPCMonitorService" {
		return BuildCrosRspFrame(pkt.Comm.SNO, req.ServiceName, req.Operation, 1, nil)
	}

	snap := s.provider.Snapshot()
	now := time.Now().Unix()

	switch req.Operation {
	case OpGetRpcPortInfo:
		return BuildCrosRspFrame(pkt.Comm.SNO, req.ServiceName, req.Operation, 0, EncodePortInfoResponse(snap.Ports, now))
	case OpGetRpcServerNumInfo:
		return BuildCrosRspFrame(pkt.Comm.SNO, req.ServiceName, req.Operation, 0, EncodeUInt32Type(snap.ServerNum()))
	case OpGetRpcServerDetailInfo:
		return BuildCrosRspFrame(pkt.Comm.SNO, req.ServiceName, req.Operation, 0, EncodeServerDetailResponse(snap.Servers, now))
	case OpGetRpcClientNumInfo:
		return BuildCrosRspFrame(pkt.Comm.SNO, req.ServiceName, req.Operation, 0, EncodeUInt32Type(snap.ClientNum()))
	case OpGetRpcClientDetailInfo:
		return BuildCrosRspFrame(pkt.Comm.SNO, req.ServiceName, req.Operation, 0, EncodeClientDetailResponse(snap.Clients, now))
	default:
		return BuildCrosRspFrame(pkt.Comm.SNO, req.ServiceName, req.Operation, 2, nil)
	}
}
