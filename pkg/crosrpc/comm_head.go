package crosrpc

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// CommHead CROS RPC 24 字节定长公共消息头 (RPC_COMM_HEAD_S)
type CommHead struct {
	Magic          [7]byte `json:"magic"`
	TransportProto uint8   `json:"transport_proto"`
	SNO            uint64  `json:"sno"`
	Length         uint32  `json:"length"`
	HeadLen        uint16  `json:"head_len"`
	MsgType        uint8   `json:"msg_type"`
	IsPacked       uint8   `json:"is_packed"`
}

// ParseCommHead 从原始 24 字节大端网络序反序列化公共消息头
func ParseCommHead(raw []byte) (CommHead, error) {
	if len(raw) < CommHeadLen {
		return CommHead{}, ErrBufferTooShort
	}
	var h CommHead
	copy(h.Magic[:], raw[0:7])
	h.TransportProto = raw[7]
	h.SNO = binary.BigEndian.Uint64(raw[8:16])
	h.Length = binary.BigEndian.Uint32(raw[16:20])
	h.HeadLen = binary.BigEndian.Uint16(raw[20:22])
	h.MsgType = raw[22]
	h.IsPacked = raw[23]
	return h, nil
}

// Marshal 将公共消息头序列化为 24 字节网络大端序切片
func (h CommHead) Marshal() []byte {
	buf := make([]byte, CommHeadLen)
	copy(buf[0:7], h.Magic[:])
	buf[7] = h.TransportProto
	binary.BigEndian.PutUint64(buf[8:16], h.SNO)
	binary.BigEndian.PutUint32(buf[16:20], h.Length)
	binary.BigEndian.PutUint16(buf[20:22], h.HeadLen)
	buf[22] = h.MsgType
	buf[23] = h.IsPacked
	return buf
}

// ValidateCommHead 按照 DOPHI CROS RPC 规范全面校验公共消息头合法性边界
func ValidateCommHead(h CommHead) error {
	// 1. 魔术字校验
	if !bytes.Equal(h.Magic[:], []byte(MagicPrefix)) {
		return ErrInvalidMagic
	}

	// 2. 传输协议限定在合法范围 [0, TransportCustomize (7)]
	if h.TransportProto > TransportCustomize {
		return ErrInvalidTransport
	}

	// 3. 消息类型合法范围 [2, MsgTypeButt)
	if h.MsgType < MsgTypeReq || h.MsgType >= MsgTypeButt {
		return ErrInvalidMsgType
	}

	// 4. 最小会话头下界检查
	switch {
	case h.MsgType == MsgTypeReq:
		if h.HeadLen < ReqHeadMinLen {
			return ErrHeadLenTooSmall
		}
	case h.MsgType == MsgTypeRsp || h.MsgType == MsgTypeRspSeg:
		if h.HeadLen < RspHeadMinLen {
			return ErrHeadLenTooSmall
		}
	case h.MsgType == MsgTypeMng || h.MsgType == MsgTypeMngReq || h.MsgType == MsgTypeUUIDReq:
		if h.HeadLen < MngHeadMinLen {
			return ErrHeadLenTooSmall
		}
	}

	// 5. 总长度与头长度关系校验: HeadLen <= Length
	if uint32(h.HeadLen) > h.Length {
		return ErrHeadLenGreaterThanLength
	}

	// 6. 最大载荷上界防溢出校验: Length <= MaxPayloadLen + HeadLen
	if uint64(h.Length) > uint64(MaxPayloadLen)+uint64(h.HeadLen) {
		return ErrLengthTooLarge
	}

	return nil
}

// TransportProtoName 返回 CROS RPC 传输层协议全名
func TransportProtoName(proto uint8) string {
	switch proto {
	case TransportDefault:
		return "RPC_TRANSPORT_DEFAULT"
	case TransportTCP:
		return "RPC_TRANSPORT_TCP"
	case TransportDMS:
		return "RPC_TRANSPORT_DMS"
	case TransportUC:
		return "RPC_TRANSPORT_UC"
	case TransportMesh:
		return "RPC_TRANSPORT_MESH"
	case TransportDomain:
		return "RPC_TRANSPORT_DOMAIN"
	case TransportFRPC:
		return "RPC_TRANSPORT_FRPC"
	case TransportCustomize:
		return "RPC_TRANSPORT_CUSTOMIZE"
	default:
		return fmt.Sprintf("RPC_TRANSPORT_UNKNOWN(%d)", proto)
	}
}

// TransportProtoShortLabel 返回 CROS RPC 传输层协议短标识（如 "TCP(1)", "MESH(4)"）
func TransportProtoShortLabel(proto uint8) string {
	switch proto {
	case TransportDefault:
		return "DEFAULT(0)"
	case TransportTCP:
		return "TCP(1)"
	case TransportDMS:
		return "DMS(2)"
	case TransportUC:
		return "UC(3)"
	case TransportMesh:
		return "MESH(4)"
	case TransportDomain:
		return "DOMAIN(5)"
	case TransportFRPC:
		return "FRPC(6)"
	case TransportCustomize:
		return "CUSTOMIZE(7)"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", proto)
	}
}
