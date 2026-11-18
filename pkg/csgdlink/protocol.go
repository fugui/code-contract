// Package csgdlink implements the pure Go stdlib DOPRA dlink control-plane
// protocol and non-intrusive passive frame observer for CSG.
package csgdlink

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	// DlinkMagic is the 4-byte ASCII magic "DLNK" (0x444C4E4B).
	DlinkMagic uint32 = 0x444C4E4B
	// DlinkHeadLen is sizeof(DDM_DLINK_MSG_HEAD_S) in bytes.
	DlinkHeadLen = 32
	// DlinkMaxBody caps a single dlink frame payload to DDM_DLINK_MAX_MSG_BUFF.
	DlinkMaxBody = 32000
	// LinkID is the CSG dlink channel identifier accepted by live CSG servers.
	LinkID uint32 = 0x020A0000
)

// dlink message types (usType).
const (
	MsgTypeRequest uint16 = 0
	MsgTypeReply   uint16 = 1
	MsgTypeNotify  uint16 = 2
	MsgTypeFault   uint16 = 3
)

// dlink system message subtypes (0xFF00+).
const (
	SubTypeRegister         uint16 = 0xFF00
	SubTypeRegisterAck      uint16 = 0xFF01
	SubTypeUnregister       uint16 = 0xFF02
	SubTypeUnregisterAck    uint16 = 0xFF03
	SubTypeBatchRegister    uint16 = 0xFF04
	SubTypeBatchRegisterAck uint16 = 0xFF05
	SubTypeFaultNotify      uint16 = 0xFF06
)

// CSG control message subtypes (1..8 requests, 1001..1008 replies).
const (
	SubTypeClientID        uint16 = 1
	SubTypePub             uint16 = 2
	SubTypeSub             uint16 = 3
	SubTypeUnpub           uint16 = 4
	SubTypeUnsub           uint16 = 5
	SubTypeSessionIDGet    uint16 = 6
	SubTypeGetBindAddr     uint16 = 7
	SubTypeFreeBindAddr    uint16 = 8
	SubTypeClientIDAck     uint16 = 1001
	SubTypePubAck          uint16 = 1002
	SubTypeSubAck          uint16 = 1003
	SubTypeUnpubAck        uint16 = 1004
	SubTypeUnsubAck        uint16 = 1005
	SubTypeSessionIDAck    uint16 = 1006
	SubTypeGetBindAddrAck  uint16 = 1007
	SubTypeFreeBindAddrAck uint16 = 1008
)

// CSG push notification subtypes delivered with MsgTypeNotify.
const (
	NotifySubTypePubUpdate uint16 = 1
	NotifySubTypeSuberQuit uint16 = 2
)

// CSG wire payload sizes.
const (
	ServiceName32Len = 32
	ServiceNameLen   = 64
	ServiceAddrLen   = 128
	ServiceInstLen   = 248
	PubPayloadLen    = 256
	SubPayloadLen    = 80
	UnsubPayloadLen  = 72
)

var (
	ErrShortFrame = errors.New("csgdlink: short frame")
	ErrBadMagic   = errors.New("csgdlink: bad magic")
	magicBytes    = []byte{'D', 'L', 'N', 'K'}
)

// Frame models the 32-byte big-endian DDM_DLINK_MSG_HEAD_S header.
type Frame struct {
	Magic     uint32
	Type      uint16
	HeadLen   uint16
	Size      uint16
	SubType   uint16
	CorrID    uint32
	LinkID    uint32
	Reserved  uint32
	TimeStamp uint64
}

// IsDlinkPrefix reports whether b begins with the 4-byte "DLNK" magic prefix.
func IsDlinkPrefix(b []byte) bool {
	return len(b) >= len(magicBytes) && bytes.Equal(b[:4], magicBytes)
}

// EncodeHead encodes a big-endian 32-byte dlink request header.
func EncodeHead(subType uint16, size uint16, corrID uint32) []byte {
	f := NewRequestFrame(subType, nil, corrID)
	f.Size = size
	return MarshalHead(f)
}

// MarshalHead serializes f into a 32-byte big-endian slice.
func MarshalHead(f Frame) []byte {
	if f.Magic == 0 {
		f.Magic = DlinkMagic
	}
	if f.HeadLen == 0 {
		f.HeadLen = DlinkHeadLen
	}
	b := make([]byte, DlinkHeadLen)
	binary.BigEndian.PutUint32(b[0:], f.Magic)
	binary.BigEndian.PutUint16(b[4:], f.Type)
	binary.BigEndian.PutUint16(b[6:], f.HeadLen)
	binary.BigEndian.PutUint16(b[8:], f.Size)
	binary.BigEndian.PutUint16(b[10:], f.SubType)
	binary.BigEndian.PutUint32(b[12:], f.CorrID)
	binary.BigEndian.PutUint32(b[16:], f.LinkID)
	binary.BigEndian.PutUint32(b[20:], f.Reserved)
	binary.BigEndian.PutUint64(b[24:], f.TimeStamp)
	return b
}

// DecodeHead decodes a 32-byte big-endian DDM_DLINK_MSG_HEAD_S header.
func DecodeHead(b []byte) (Frame, error) {
	if len(b) < DlinkHeadLen {
		return Frame{}, ErrShortFrame
	}
	return Frame{
		Magic:     binary.BigEndian.Uint32(b[0:]),
		Type:      binary.BigEndian.Uint16(b[4:]),
		HeadLen:   binary.BigEndian.Uint16(b[6:]),
		Size:      binary.BigEndian.Uint16(b[8:]),
		SubType:   binary.BigEndian.Uint16(b[10:]),
		CorrID:    binary.BigEndian.Uint32(b[12:]),
		LinkID:    binary.BigEndian.Uint32(b[16:]),
		Reserved:  binary.BigEndian.Uint32(b[20:]),
		TimeStamp: binary.BigEndian.Uint64(b[24:]),
	}, nil
}

// ReadFrame reads a complete dlink header + payload from r.
func ReadFrame(r io.Reader) (Frame, []byte, error) {
	head := make([]byte, DlinkHeadLen)
	if _, err := io.ReadFull(r, head); err != nil {
		return Frame{}, nil, err
	}
	f, err := DecodeHead(head)
	if err != nil {
		return Frame{}, nil, err
	}
	if f.Magic != DlinkMagic {
		return Frame{}, nil, ErrBadMagic
	}
	if f.Size == 0 {
		return f, nil, nil
	}
	if int(f.Size) > DlinkMaxBody {
		return Frame{}, nil, fmt.Errorf("csgdlink: frame body %d exceeds max %d", f.Size, DlinkMaxBody)
	}
	body := make([]byte, f.Size)
	if _, err := io.ReadFull(r, body); err != nil {
		return Frame{}, nil, err
	}
	return f, body, nil
}

// WriteFrame writes a complete dlink frame to w.
func WriteFrame(w io.Writer, f Frame, body []byte) error {
	if len(body) > DlinkMaxBody {
		return fmt.Errorf("csgdlink: payload %d exceeds max %d", len(body), DlinkMaxBody)
	}
	if f.Magic == 0 {
		f.Magic = DlinkMagic
	}
	f.HeadLen = DlinkHeadLen
	f.Size = uint16(len(body))
	if f.TimeStamp == 0 {
		f.TimeStamp = uint64(time.Now().UnixNano())
	}
	head := MarshalHead(f)
	if _, err := w.Write(head); err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	_, err := w.Write(body)
	return err
}

// NewRequestFrame constructs a request Frame header.
func NewRequestFrame(subType uint16, body []byte, corrID uint32) Frame {
	return Frame{
		Magic:   DlinkMagic,
		Type:    MsgTypeRequest,
		HeadLen: DlinkHeadLen,
		Size:    uint16(len(body)),
		SubType: subType,
		CorrID:  corrID,
		LinkID:  LinkID,
	}
}

// PaddedString encodes s into a fixed-width NUL-padded buffer of size n.
func PaddedString(s string, n int) []byte {
	b := make([]byte, n)
	copy(b, []byte(s))
	return b
}

// CString extracts a string truncated strictly at the first NUL byte.
func CString(b []byte) string {
	if idx := bytes.IndexByte(b, 0); idx >= 0 {
		return string(b[:idx])
	}
	return string(b)
}

// SubTypeName returns a stable human-readable name for known dlink subtypes.
func SubTypeName(msgType, subType uint16) string {
	if msgType == MsgTypeNotify {
		switch subType {
		case NotifySubTypePubUpdate:
			return "NOTIFY_PUB_UPDATE"
		case NotifySubTypeSuberQuit:
			return "NOTIFY_SUBER_QUIT"
		default:
			return fmt.Sprintf("NOTIFY_0x%04X", subType)
		}
	}
	switch subType {
	case SubTypeRegister:
		return "REGISTER"
	case SubTypeRegisterAck:
		return "REGISTER_ACK"
	case SubTypeUnregister:
		return "UNREGISTER"
	case SubTypeUnregisterAck:
		return "UNREGISTER_ACK"
	case SubTypeBatchRegister:
		return "BATCH_REGISTER"
	case SubTypeBatchRegisterAck:
		return "BATCH_REGISTER_ACK"
	case SubTypeFaultNotify:
		return "FAULT_NOTIFY"
	case SubTypeClientID:
		return "CLIENT_ID"
	case SubTypeClientIDAck:
		return "CLIENT_ID_ACK"
	case SubTypePub:
		return "PUB"
	case SubTypePubAck:
		return "PUB_ACK"
	case SubTypeSub:
		return "SUB"
	case SubTypeSubAck:
		return "SUB_ACK"
	case SubTypeUnpub:
		return "UNPUB"
	case SubTypeUnpubAck:
		return "UNPUB_ACK"
	case SubTypeUnsub:
		return "UNSUB"
	case SubTypeUnsubAck:
		return "UNSUB_ACK"
	case SubTypeSessionIDGet:
		return "SESSION_ID_GET"
	case SubTypeSessionIDAck:
		return "SESSION_ID_ACK"
	case SubTypeGetBindAddr:
		return "GET_BIND_ADDR"
	case SubTypeGetBindAddrAck:
		return "GET_BIND_ADDR_ACK"
	case SubTypeFreeBindAddr:
		return "FREE_BIND_ADDR"
	case SubTypeFreeBindAddrAck:
		return "FREE_BIND_ADDR_ACK"
	default:
		return fmt.Sprintf("SUBTYPE_0x%04X", subType)
	}
}
