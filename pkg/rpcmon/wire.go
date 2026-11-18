package rpcmon

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

var (
	ErrTruncatedWire = errors.New("rpcmon: truncated protobuf wire message")
	ErrInvalidWire   = errors.New("rpcmon: invalid protobuf wire type")
)

// EncodeUInt32Type encodes BasicTypeMsg.UInt32Type { required uint32 value = 1; }.
func EncodeUInt32Type(val uint32) []byte {
	var b []byte
	b = appendTag(b, 1, wireVarint)
	b = binary.AppendUvarint(b, uint64(val))
	return b
}

// DecodeUInt32Type decodes BasicTypeMsg.UInt32Type.
func DecodeUInt32Type(data []byte) (uint32, error) {
	var val uint32
	for len(data) > 0 {
		num, wt, n, err := consumeTag(data)
		if err != nil {
			return 0, err
		}
		data = data[n:]
		switch {
		case num == 1 && wt == wireVarint:
			v, m, err := consumeVarint(data)
			if err != nil {
				return 0, err
			}
			val = uint32(v)
			data = data[m:]
		default:
			skip, err := skipField(data, wt)
			if err != nil {
				return 0, err
			}
			data = data[skip:]
		}
	}
	return val, nil
}

// EncodePortInfoResponse encodes swp.rpc.GetRpcPortInfoResponse.
func EncodePortInfoResponse(ports []PortUsage, ts int64) []byte {
	var b []byte
	for _, p := range ports {
		var sub []byte
		sub = appendStringField(sub, 1, p.IPAddr)
		sub = appendVarintField(sub, 2, uint64(p.Used))
		sub = appendVarintField(sub, 3, uint64(p.Total))
		b = appendBytesField(b, 1, sub)
	}
	b = appendSFixed64Field(b, 2, ts)
	return b
}

// DecodePortInfoResponse decodes swp.rpc.GetRpcPortInfoResponse.
func DecodePortInfoResponse(data []byte) ([]PortUsage, int64, error) {
	var ports []PortUsage
	var ts int64
	for len(data) > 0 {
		num, wt, n, err := consumeTag(data)
		if err != nil {
			return nil, 0, err
		}
		data = data[n:]
		switch {
		case num == 1 && wt == wireBytes:
			raw, m, err := consumeBytes(data)
			if err != nil {
				return nil, 0, err
			}
			item, err := decodePortUsage(raw)
			if err != nil {
				return nil, 0, err
			}
			ports = append(ports, item)
			data = data[m:]
		case num == 2 && wt == wireFixed64:
			v, m, err := consumeFixed64(data)
			if err != nil {
				return nil, 0, err
			}
			ts = int64(v)
			data = data[m:]
		default:
			skip, err := skipField(data, wt)
			if err != nil {
				return nil, 0, err
			}
			data = data[skip:]
		}
	}
	return ports, ts, nil
}

func decodePortUsage(data []byte) (PortUsage, error) {
	var out PortUsage
	for len(data) > 0 {
		num, wt, n, err := consumeTag(data)
		if err != nil {
			return PortUsage{}, err
		}
		data = data[n:]
		switch {
		case num == 1 && wt == wireBytes:
			raw, m, err := consumeBytes(data)
			if err != nil {
				return PortUsage{}, err
			}
			out.IPAddr = string(raw)
			data = data[m:]
		case num == 2 && wt == wireVarint:
			v, m, err := consumeVarint(data)
			if err != nil {
				return PortUsage{}, err
			}
			out.Used = uint32(v)
			data = data[m:]
		case num == 3 && wt == wireVarint:
			v, m, err := consumeVarint(data)
			if err != nil {
				return PortUsage{}, err
			}
			out.Total = uint32(v)
			data = data[m:]
		default:
			skip, err := skipField(data, wt)
			if err != nil {
				return PortUsage{}, err
			}
			data = data[skip:]
		}
	}
	return out, nil
}

// EncodeServerDetailResponse encodes swp.rpc.GetRpcServerDetailInfoResponse.
func EncodeServerDetailResponse(servers []ServerEntry, ts int64) []byte {
	var b []byte
	for _, s := range servers {
		var sub []byte
		sub = appendStringField(sub, 1, s.ProcName)
		sub = appendVarintField(sub, 2, uint64(s.Pid))
		sub = appendStringField(sub, 3, s.ServerName)
		sub = appendStringField(sub, 4, s.ServerAddr)
		b = appendBytesField(b, 1, sub)
	}
	b = appendSFixed64Field(b, 2, ts)
	return b
}

// DecodeServerDetailResponse decodes swp.rpc.GetRpcServerDetailInfoResponse.
func DecodeServerDetailResponse(data []byte) ([]ServerEntry, int64, error) {
	var servers []ServerEntry
	var ts int64
	for len(data) > 0 {
		num, wt, n, err := consumeTag(data)
		if err != nil {
			return nil, 0, err
		}
		data = data[n:]
		switch {
		case num == 1 && wt == wireBytes:
			raw, m, err := consumeBytes(data)
			if err != nil {
				return nil, 0, err
			}
			item, err := decodeServerEntry(raw)
			if err != nil {
				return nil, 0, err
			}
			servers = append(servers, item)
			data = data[m:]
		case num == 2 && wt == wireFixed64:
			v, m, err := consumeFixed64(data)
			if err != nil {
				return nil, 0, err
			}
			ts = int64(v)
			data = data[m:]
		default:
			skip, err := skipField(data, wt)
			if err != nil {
				return nil, 0, err
			}
			data = data[skip:]
		}
	}
	return servers, ts, nil
}

func decodeServerEntry(data []byte) (ServerEntry, error) {
	var out ServerEntry
	for len(data) > 0 {
		num, wt, n, err := consumeTag(data)
		if err != nil {
			return ServerEntry{}, err
		}
		data = data[n:]
		switch {
		case num == 1 && wt == wireBytes:
			raw, m, err := consumeBytes(data)
			if err != nil {
				return ServerEntry{}, err
			}
			out.ProcName = string(raw)
			data = data[m:]
		case num == 2 && wt == wireVarint:
			v, m, err := consumeVarint(data)
			if err != nil {
				return ServerEntry{}, err
			}
			out.Pid = uint32(v)
			data = data[m:]
		case num == 3 && wt == wireBytes:
			raw, m, err := consumeBytes(data)
			if err != nil {
				return ServerEntry{}, err
			}
			out.ServerName = string(raw)
			data = data[m:]
		case num == 4 && wt == wireBytes:
			raw, m, err := consumeBytes(data)
			if err != nil {
				return ServerEntry{}, err
			}
			out.ServerAddr = string(raw)
			data = data[m:]
		default:
			skip, err := skipField(data, wt)
			if err != nil {
				return ServerEntry{}, err
			}
			data = data[skip:]
		}
	}
	return out, nil
}

// EncodeClientDetailResponse encodes swp.rpc.GetRpcClientDetailInfoResponse.
func EncodeClientDetailResponse(clients []ClientEntry, ts int64) []byte {
	var b []byte
	for _, c := range clients {
		var sub []byte
		sub = appendStringField(sub, 1, c.ProcName)
		sub = appendVarintField(sub, 2, uint64(c.Pid))
		sub = appendStringField(sub, 3, c.ClientAddr)
		sub = appendStringField(sub, 4, c.ServerName)
		b = appendBytesField(b, 1, sub)
	}
	b = appendSFixed64Field(b, 2, ts)
	return b
}

// DecodeClientDetailResponse decodes swp.rpc.GetRpcClientDetailInfoResponse.
func DecodeClientDetailResponse(data []byte) ([]ClientEntry, int64, error) {
	var clients []ClientEntry
	var ts int64
	for len(data) > 0 {
		num, wt, n, err := consumeTag(data)
		if err != nil {
			return nil, 0, err
		}
		data = data[n:]
		switch {
		case num == 1 && wt == wireBytes:
			raw, m, err := consumeBytes(data)
			if err != nil {
				return nil, 0, err
			}
			item, err := decodeClientEntry(raw)
			if err != nil {
				return nil, 0, err
			}
			clients = append(clients, item)
			data = data[m:]
		case num == 2 && wt == wireFixed64:
			v, m, err := consumeFixed64(data)
			if err != nil {
				return nil, 0, err
			}
			ts = int64(v)
			data = data[m:]
		default:
			skip, err := skipField(data, wt)
			if err != nil {
				return nil, 0, err
			}
			data = data[skip:]
		}
	}
	return clients, ts, nil
}

func decodeClientEntry(data []byte) (ClientEntry, error) {
	var out ClientEntry
	for len(data) > 0 {
		num, wt, n, err := consumeTag(data)
		if err != nil {
			return ClientEntry{}, err
		}
		data = data[n:]
		switch {
		case num == 1 && wt == wireBytes:
			raw, m, err := consumeBytes(data)
			if err != nil {
				return ClientEntry{}, err
			}
			out.ProcName = string(raw)
			data = data[m:]
		case num == 2 && wt == wireVarint:
			v, m, err := consumeVarint(data)
			if err != nil {
				return ClientEntry{}, err
			}
			out.Pid = uint32(v)
			data = data[m:]
		case num == 3 && wt == wireBytes:
			raw, m, err := consumeBytes(data)
			if err != nil {
				return ClientEntry{}, err
			}
			out.ClientAddr = string(raw)
			data = data[m:]
		case num == 4 && wt == wireBytes:
			raw, m, err := consumeBytes(data)
			if err != nil {
				return ClientEntry{}, err
			}
			out.ServerName = string(raw)
			data = data[m:]
		default:
			skip, err := skipField(data, wt)
			if err != nil {
				return ClientEntry{}, err
			}
			data = data[skip:]
		}
	}
	return out, nil
}

func appendTag(b []byte, fieldNum uint32, wireType uint8) []byte {
	return binary.AppendUvarint(b, uint64(fieldNum)<<3|uint64(wireType))
}

func appendVarintField(b []byte, fieldNum uint32, v uint64) []byte {
	b = appendTag(b, fieldNum, wireVarint)
	return binary.AppendUvarint(b, v)
}

func appendSFixed64Field(b []byte, fieldNum uint32, v int64) []byte {
	b = appendTag(b, fieldNum, wireFixed64)
	return binary.LittleEndian.AppendUint64(b, uint64(v))
}

func appendBytesField(b []byte, fieldNum uint32, raw []byte) []byte {
	b = appendTag(b, fieldNum, wireBytes)
	b = binary.AppendUvarint(b, uint64(len(raw)))
	return append(b, raw...)
}

func appendStringField(b []byte, fieldNum uint32, s string) []byte {
	b = appendTag(b, fieldNum, wireBytes)
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

func consumeTag(data []byte) (uint32, uint8, int, error) {
	v, n := binary.Uvarint(data)
	if n <= 0 {
		return 0, 0, 0, ErrTruncatedWire
	}
	return uint32(v >> 3), uint8(v & 0x7), n, nil
}

func consumeVarint(data []byte) (uint64, int, error) {
	v, n := binary.Uvarint(data)
	if n <= 0 {
		return 0, 0, ErrTruncatedWire
	}
	return v, n, nil
}

func consumeFixed64(data []byte) (uint64, int, error) {
	if len(data) < 8 {
		return 0, 0, ErrTruncatedWire
	}
	return binary.LittleEndian.Uint64(data[:8]), 8, nil
}

func consumeBytes(data []byte) ([]byte, int, error) {
	l, n := binary.Uvarint(data)
	if n <= 0 {
		return nil, 0, ErrTruncatedWire
	}
	end := uint64(n) + l
	if end > uint64(len(data)) {
		return nil, 0, ErrTruncatedWire
	}
	return data[n:int(end)], int(end), nil
}

func skipField(data []byte, wt uint8) (int, error) {
	switch wt {
	case wireVarint:
		_, n, err := consumeVarint(data)
		return n, err
	case wireFixed64:
		if len(data) < 8 {
			return 0, ErrTruncatedWire
		}
		return 8, nil
	case wireBytes:
		_, n, err := consumeBytes(data)
		return n, err
	case wireFixed32:
		if len(data) < 4 {
			return 0, ErrTruncatedWire
		}
		return 4, nil
	default:
		return 0, fmt.Errorf("%w: %d", ErrInvalidWire, wt)
	}
}
