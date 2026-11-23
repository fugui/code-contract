// Package rpcmon implements the pure-Go RPCMonitorService client/server
// across both plain CROS TCP and dophi_mesh (128B HELLO + 40B MESH_MSG_DATA_HEAD_S)
// transports, with zero protobuf-registry or external C++ probe dependencies.
package rpcmon

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"

	"code-contract/pkg/crosrpc"
)

const (
	// MeshHelloMagic is MESH_SECCOMM_MAGIC_NUM.
	MeshHelloMagic uint16 = 0xEFBC
	// MeshHelloLen is sizeof(MESH_SEC_HELLO_MSG_INFO_S).
	MeshHelloLen = 128
	// MeshAuthNameLen is MESH_AUTH_NAME_LEN.
	MeshAuthNameLen = 96

	// MeshDataMagic is MESH_PKT_MAGIC_NUM for a single-message data frame.
	MeshDataMagic uint32 = 0x2019AAAA
	// MeshBatchDataMagic is the batch data frame magic (unsupported in pure-Go client).
	MeshBatchDataMagic uint32 = 0x2019BBBB
	// MeshDataHeadLen is sizeof(MESH_MSG_DATA_HEAD_S).
	MeshDataHeadLen = 40

	// MeshRoleClient is MESH_UCM_ROLE_CLIENT.
	MeshRoleClient byte = 0
	// MeshRoleServer is MESH_UCM_ROLE_SERVER.
	MeshRoleServer byte = 1

	// MeshHelloTypeSec is MESH_HELLO_MSG_SEC_TYPE.
	MeshHelloTypeSec byte = 1
	// MeshHelloVer00 is MESH_HELLO_MSG_VER_00.
	MeshHelloVer00 byte = 0

	// MeshDataHeadVer00 is MESH_MSG_HEAD_VER_00.
	MeshDataHeadVer00 byte = 0
	// MeshCheckNone is MESH_SOCK_CHECK_TYPE_NONE.
	MeshCheckNone uint16 = 0
	// MeshInvalidMsgID is MESH_INVALID_MSG_ID for single-message frames.
	MeshInvalidMsgID uint32 = 0xFFFFFFFF
	// MeshInvalidMsgNum is the fixed uiMsgNum for single-message frames.
	MeshInvalidMsgNum uint32 = 0xFFFFFFFF
)

// MeshHello mirrors MESH_SEC_HELLO_MSG_INFO_S (128 bytes, big-endian).
type MeshHello struct {
	Magic          uint16
	MsgLen         uint16
	Role           byte
	Type           byte
	Version        byte
	Reserved       byte
	Name           string
	AuthLevel      uint32
	AuthorizedRet  uint32
	SecNegoTimeout uint32
	InterFd        uint32
	Reserved2      [6]byte
	SecOpt         byte
	SspAuthOK      byte
}

// EncodeMeshHello builds a fixed 128-byte big-endian HELLO frame.
func EncodeMeshHello(h MeshHello) []byte {
	if h.Magic == 0 {
		h.Magic = MeshHelloMagic
	}
	if h.MsgLen == 0 {
		h.MsgLen = MeshHelloLen
	}
	if h.Type == 0 {
		h.Type = MeshHelloTypeSec
	}
	b := make([]byte, MeshHelloLen)
	binary.BigEndian.PutUint16(b[0:], h.Magic)
	binary.BigEndian.PutUint16(b[2:], h.MsgLen)
	b[4] = h.Role
	b[5] = h.Type
	b[6] = h.Version
	b[7] = h.Reserved
	nameBytes := []byte(h.Name)
	if len(nameBytes) >= MeshAuthNameLen {
		nameBytes = nameBytes[:MeshAuthNameLen-1]
	}
	copy(b[8:8+MeshAuthNameLen], nameBytes)
	binary.BigEndian.PutUint32(b[104:], h.AuthLevel)
	binary.BigEndian.PutUint32(b[108:], h.AuthorizedRet)
	binary.BigEndian.PutUint32(b[112:], h.SecNegoTimeout)
	binary.BigEndian.PutUint32(b[116:], h.InterFd)
	copy(b[120:126], h.Reserved2[:])
	b[126] = h.SecOpt
	b[127] = h.SspAuthOK
	return b
}

// DecodeMeshHello parses a 128-byte HELLO frame, truncating Name at the first NUL byte.
func DecodeMeshHello(b []byte) (MeshHello, error) {
	if len(b) < MeshHelloLen {
		return MeshHello{}, fmt.Errorf("rpcmon: short mesh hello %d", len(b))
	}
	var h MeshHello
	h.Magic = binary.BigEndian.Uint16(b[0:])
	h.MsgLen = binary.BigEndian.Uint16(b[2:])
	h.Role = b[4]
	h.Type = b[5]
	h.Version = b[6]
	h.Reserved = b[7]
	h.Name = firstNULCString(b[8 : 8+MeshAuthNameLen])
	h.AuthLevel = binary.BigEndian.Uint32(b[104:])
	h.AuthorizedRet = binary.BigEndian.Uint32(b[108:])
	h.SecNegoTimeout = binary.BigEndian.Uint32(b[112:])
	h.InterFd = binary.BigEndian.Uint32(b[116:])
	copy(h.Reserved2[:], b[120:126])
	h.SecOpt = b[126]
	h.SspAuthOK = b[127]
	return h, nil
}

// MeshDataHead mirrors MESH_MSG_DATA_HEAD_S (40 bytes, big-endian).
type MeshDataHead struct {
	Magic       uint32
	MsgNum      uint32
	Timestamp   uint64
	Version     byte
	TraceLen    byte
	HeadSize    uint16
	MessageSize uint32
	CheckType   uint16
	CheckValue  uint16
	SrcIPv4     uint32
	SrcPort     uint16
	SecHeadLen  byte
	MacLen      byte
	MsgID       uint32
}

// EncodeMeshDataHead builds a fixed 40-byte big-endian MESH_MSG_DATA_HEAD_S header.
func EncodeMeshDataHead(h MeshDataHead) []byte {
	if h.Magic == 0 {
		h.Magic = MeshDataMagic
	}
	if h.HeadSize == 0 {
		h.HeadSize = MeshDataHeadLen
	}
	if h.MsgNum == 0 {
		h.MsgNum = MeshInvalidMsgNum
	}
	if h.MsgID == 0 {
		h.MsgID = MeshInvalidMsgID
	}
	h.Version = MeshDataHeadVer00
	h.TraceLen = 0
	h.SecHeadLen = 0
	h.MacLen = 0

	b := make([]byte, MeshDataHeadLen)
	binary.BigEndian.PutUint32(b[0:], h.Magic)
	binary.BigEndian.PutUint32(b[4:], h.MsgNum)
	binary.BigEndian.PutUint64(b[8:], h.Timestamp)
	b[16] = h.Version
	b[17] = h.TraceLen
	binary.BigEndian.PutUint16(b[18:], h.HeadSize)
	binary.BigEndian.PutUint32(b[20:], h.MessageSize)
	binary.BigEndian.PutUint16(b[24:], h.CheckType)
	binary.BigEndian.PutUint16(b[26:], h.CheckValue)
	binary.BigEndian.PutUint32(b[28:], h.SrcIPv4)
	binary.BigEndian.PutUint16(b[32:], h.SrcPort)
	b[34] = h.SecHeadLen
	b[35] = h.MacLen
	binary.BigEndian.PutUint32(b[36:], h.MsgID)
	return b
}

// DecodeMeshDataHead parses a 40-byte MESH_MSG_DATA_HEAD_S header.
func DecodeMeshDataHead(b []byte) (MeshDataHead, error) {
	if len(b) < MeshDataHeadLen {
		return MeshDataHead{}, fmt.Errorf("rpcmon: short mesh data head %d", len(b))
	}
	var h MeshDataHead
	h.Magic = binary.BigEndian.Uint32(b[0:])
	h.MsgNum = binary.BigEndian.Uint32(b[4:])
	h.Timestamp = binary.BigEndian.Uint64(b[8:])
	h.Version = b[16]
	h.TraceLen = b[17]
	h.HeadSize = binary.BigEndian.Uint16(b[18:])
	h.MessageSize = binary.BigEndian.Uint32(b[20:])
	h.CheckType = binary.BigEndian.Uint16(b[24:])
	h.CheckValue = binary.BigEndian.Uint16(b[26:])
	h.SrcIPv4 = binary.BigEndian.Uint32(b[28:])
	h.SrcPort = binary.BigEndian.Uint16(b[32:])
	h.SecHeadLen = b[34]
	h.MacLen = b[35]
	h.MsgID = binary.BigEndian.Uint32(b[36:])
	return h, nil
}

// BuildCrosMeshReqFrame encodes a CROS RPC V05 request frame with TransportProto=4 (MESH).
func BuildCrosMeshReqFrame(sno uint64, serviceName, operation string, timeoutMs uint32, payload []byte) []byte {
	var magic [7]byte
	copy(magic[:], crosrpc.MagicPrefix)
	comm := crosrpc.CommHead{
		Magic:          magic,
		TransportProto: crosrpc.TransportMesh,
		SNO:            sno,
		Length:         uint32(ReqHeadV05Len + len(payload)),
		HeadLen:        ReqHeadV05Len,
		MsgType:        crosrpc.MsgTypeReq,
		IsPacked:       1,
	}

	frame := make([]byte, crosrpc.CommHeadLen+ReqHeadV05Len+len(payload))
	copy(frame[:crosrpc.CommHeadLen], comm.Marshal())

	req := frame[crosrpc.CommHeadLen : crosrpc.CommHeadLen+ReqHeadV05Len]
	binary.BigEndian.PutUint16(req[0:2], 5)
	binary.BigEndian.PutUint16(req[2:4], ReqHeadV05Len)
	copyFixedCString(req[8:72], serviceName)
	copyFixedCString(req[72:136], operation)
	req[136] = crosrpc.ReqTypeSync
	binary.BigEndian.PutUint32(req[244:248], timeoutMs)
	binary.BigEndian.PutUint64(req[248:256], uint64(time.Now().UnixNano()))

	if len(payload) > 0 {
		copy(frame[crosrpc.CommHeadLen+ReqHeadV05Len:], payload)
	}
	return frame
}

// BuildCrosMeshRspFrame encodes a CROS RPC V04 response frame with TransportProto=4 (MESH).
func BuildCrosMeshRspFrame(sno uint64, serviceName, operation string, status uint64, payload []byte) []byte {
	frame := BuildCrosRspFrame(sno, serviceName, operation, status, payload)
	frame[7] = crosrpc.TransportMesh
	frame[23] = 1
	return frame
}

func firstNULCString(b []byte) string {
	if idx := bytes.IndexByte(b, 0); idx >= 0 {
		b = b[:idx]
	}
	return string(b)
}
