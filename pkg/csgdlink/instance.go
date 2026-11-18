package csgdlink

import (
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Instance mirrors CSG_SERVICE_INST_INFO_S (248 bytes, network byte order).
type Instance struct {
	ClientID uint64 `json:"client_id"`
	Version  uint64 `json:"version"`
	ProcID   uint32 `json:"proc_id"`
	OsNodeID uint32 `json:"os_node_id"`
	Name32   string `json:"name32,omitempty"`
	Addr     string `json:"addr"`
	Name64   string `json:"name64"`
}

// ParseInstance decodes a 248-byte CSG_SERVICE_INST_INFO_S record.
func ParseInstance(b []byte) (Instance, error) {
	if len(b) < ServiceInstLen {
		return Instance{}, fmt.Errorf("csgdlink: short service instance: %d", len(b))
	}
	return Instance{
		ClientID: binary.BigEndian.Uint64(b[0:8]),
		Version:  binary.BigEndian.Uint64(b[8:16]),
		ProcID:   binary.BigEndian.Uint32(b[16:20]),
		OsNodeID: binary.BigEndian.Uint32(b[20:24]),
		Name32:   CString(b[24:56]),
		Addr:     CString(b[56:184]),
		Name64:   CString(b[184:248]),
	}, nil
}

// EncodeInstance serializes an Instance into 248 big-endian bytes.
func EncodeInstance(inst Instance) []byte {
	b := make([]byte, ServiceInstLen)
	binary.BigEndian.PutUint64(b[0:8], inst.ClientID)
	binary.BigEndian.PutUint64(b[8:16], inst.Version)
	binary.BigEndian.PutUint32(b[16:20], inst.ProcID)
	binary.BigEndian.PutUint32(b[20:24], inst.OsNodeID)
	copy(b[24:56], PaddedString(inst.Name32, ServiceName32Len))
	copy(b[56:184], PaddedString(inst.Addr, ServiceAddrLen))
	copy(b[184:248], PaddedString(inst.Name64, ServiceNameLen))
	return b
}

// SubRequest mirrors CSG_SUB_SVC_MSG_S (80 bytes).
type SubRequest struct {
	ClientID uint64
	Name     string
	Trans    [7]byte
}

// EncodeSubRequest encodes a SubRequest into 80 bytes.
func EncodeSubRequest(req SubRequest) []byte {
	b := make([]byte, SubPayloadLen)
	binary.BigEndian.PutUint64(b[0:8], req.ClientID)
	copy(b[8:72], PaddedString(req.Name, ServiceNameLen))
	copy(b[72:79], req.Trans[:])
	return b
}

// DecodeSubRequest decodes an 80-byte CSG_SUB_SVC_MSG_S message.
func DecodeSubRequest(b []byte) (SubRequest, error) {
	if len(b) < SubPayloadLen {
		return SubRequest{}, fmt.Errorf("csgdlink: short SUB payload: %d", len(b))
	}
	var req SubRequest
	req.ClientID = binary.BigEndian.Uint64(b[0:8])
	req.Name = CString(b[8:72])
	copy(req.Trans[:], b[72:79])
	return req, nil
}

// PubRequest mirrors CSG_PUB_SVC_MSG_S (256 bytes).
type PubRequest struct {
	Instance Instance
	Scope    byte
}

// EncodePubRequest encodes a PubRequest into 256 bytes.
func EncodePubRequest(req PubRequest) []byte {
	b := make([]byte, PubPayloadLen)
	copy(b[:ServiceInstLen], EncodeInstance(req.Instance))
	b[ServiceInstLen] = req.Scope
	return b
}

// DecodePubRequest decodes a 256-byte (or 248-byte) publish request.
func DecodePubRequest(b []byte) (PubRequest, error) {
	inst, err := ParseInstance(b)
	if err != nil {
		return PubRequest{}, err
	}
	var req PubRequest
	req.Instance = inst
	if len(b) > ServiceInstLen {
		req.Scope = b[ServiceInstLen]
	}
	return req, nil
}

// UnsubRequest mirrors CSG_UNSUB_SVC_MSG_S (72 bytes).
type UnsubRequest struct {
	ClientID uint64
	Name     string
}

// EncodeUnsubRequest encodes an UnsubRequest into 72 bytes.
func EncodeUnsubRequest(req UnsubRequest) []byte {
	b := make([]byte, UnsubPayloadLen)
	binary.BigEndian.PutUint64(b[0:8], req.ClientID)
	copy(b[8:72], PaddedString(req.Name, ServiceNameLen))
	return b
}

// TransMaskForAddr returns the 7-byte CSG_SUB_TRANS_TYPE_E array for a service URI.
func TransMaskForAddr(addr string) [7]byte {
	var mask [7]byte
	switch {
	case strings.Contains(addr, "dophi_mesh://"):
		mask[4] = 1
	case strings.Contains(addr, "dms://"):
		mask[2] = 1
	case strings.Contains(addr, "dpuc://"):
		mask[3] = 1
	case strings.Contains(addr, "domain://"):
		mask[5] = 1
	default:
		mask[1] = 1
	}
	return mask
}

// MergeTransMask ORs src transport selection flags into dst.
func MergeTransMask(dst *[7]byte, src [7]byte) {
	for i := range dst {
		dst[i] |= src[i]
	}
}

// IsTCPAddr reports whether addr denotes a plain TCP service endpoint.
func IsTCPAddr(addr string) bool {
	return strings.HasPrefix(strings.TrimSpace(addr), "tcp://")
}

// IsMeshAddr reports whether addr denotes a dophi_mesh service endpoint.
func IsMeshAddr(addr string) bool {
	return strings.HasPrefix(strings.TrimSpace(addr), "dophi_mesh://")
}

// RewriteMeshAddrToTCP rewrites a dophi_mesh:// URI prefix to tcp:// while preserving IP and port.
func RewriteMeshAddrToTCP(addr string) string {
	trimmed := strings.TrimSpace(addr)
	if strings.HasPrefix(trimmed, "dophi_mesh://") {
		return "tcp://" + strings.TrimPrefix(trimmed, "dophi_mesh://")
	}
	return trimmed
}

// ParseTCPTarget extracts host:port from tcp://ipv4:<ip>:<port>[:<instID>] or dophi_mesh://ipv4:<ip>:<port>.
func ParseTCPTarget(addr string) (string, bool) {
	s := strings.TrimSpace(addr)
	switch {
	case strings.HasPrefix(s, "tcp://"):
		s = strings.TrimPrefix(s, "tcp://")
	case strings.HasPrefix(s, "dophi_mesh://"):
		s = strings.TrimPrefix(s, "dophi_mesh://")
	default:
		return "", false
	}
	s = strings.TrimPrefix(s, "ipv4:")
	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return "", false
	}
	ip := net.ParseIP(parts[0])
	if ip == nil {
		return "", false
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil || port <= 0 || port > 65535 {
		return "", false
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port)), true
}

// ParseAddrIPPort extracts IP string and uint16 port from a CSG service URI.
func ParseAddrIPPort(addr string) (string, uint16, bool) {
	hp, ok := ParseTCPTarget(addr)
	if !ok {
		return "", 0, false
	}
	host, portStr, err := net.SplitHostPort(hp)
	if err != nil {
		return "", 0, false
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 || p > 65535 {
		return "", 0, false
	}
	return host, uint16(p), true
}
