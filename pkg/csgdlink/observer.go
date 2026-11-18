package csgdlink

import (
	"encoding/binary"
	"sync"
)

// ObservedEvent represents a non-intrusively decoded dlink control frame event.
type ObservedEvent struct {
	Direction   string     `json:"direction"`
	MsgType     uint16     `json:"msg_type"`
	SubType     uint16     `json:"sub_type"`
	SubTypeName string     `json:"sub_type_name"`
	CorrID      uint32     `json:"corr_id"`
	ServiceName string     `json:"service_name,omitempty"`
	Instances   []Instance `json:"instances,omitempty"`
	Timestamp   uint64     `json:"timestamp"`
}

// Observer passively reassembles dlink frames per stream direction without modifying wire bytes.
type Observer struct {
	mu      sync.Mutex
	streams map[string]*obsBuffer
}

type obsBuffer struct {
	buf    []byte
	broken bool
}

// NewObserver constructs a stateful passive dlink Observer.
func NewObserver() *Observer {
	return &Observer{
		streams: make(map[string]*obsBuffer),
	}
}

// Feed appends observed wire bytes for a direction ("client->csg" or "csg->client")
// and returns zero or more decoded control events.
func (o *Observer) Feed(direction string, chunk []byte) []ObservedEvent {
	if len(chunk) == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	st := o.streams[direction]
	if st == nil {
		st = &obsBuffer{}
		o.streams[direction] = st
	}
	if st.broken {
		return nil
	}

	st.buf = append(st.buf, chunk...)
	var out []ObservedEvent

	for len(st.buf) >= DlinkHeadLen {
		f, err := DecodeHead(st.buf[:DlinkHeadLen])
		if err != nil || f.Magic != DlinkMagic || int(f.Size) > DlinkMaxBody {
			// Malformed stream for non-dlink data: stop observing this direction cleanly.
			st.broken = true
			st.buf = nil
			return out
		}
		totalLen := DlinkHeadLen + int(f.Size)
		if len(st.buf) < totalLen {
			break
		}
		body := st.buf[DlinkHeadLen:totalLen]
		if ev, ok := decodeObservedEvent(direction, f, body); ok {
			out = append(out, ev)
		}
		// Compact consumed frame.
		if totalLen == len(st.buf) {
			st.buf = st.buf[:0]
		} else {
			copy(st.buf, st.buf[totalLen:])
			st.buf = st.buf[:len(st.buf)-totalLen]
		}
	}
	return out
}

func decodeObservedEvent(direction string, f Frame, body []byte) (ObservedEvent, bool) {
	ev := ObservedEvent{
		Direction:   direction,
		MsgType:     f.Type,
		SubType:     f.SubType,
		SubTypeName: SubTypeName(f.Type, f.SubType),
		CorrID:      f.CorrID,
		Timestamp:   f.TimeStamp,
	}

	if f.Type == MsgTypeNotify {
		if len(body) >= ServiceInstLen {
			if inst, err := ParseInstance(body); err == nil {
				ev.ServiceName = inst.Name64
				ev.Instances = append(ev.Instances, inst)
			}
		}
		return ev, true
	}

	switch f.SubType {
	case SubTypePub, SubTypeUnpub:
		if len(body) >= ServiceInstLen {
			if inst, err := ParseInstance(body); err == nil {
				ev.ServiceName = inst.Name64
				ev.Instances = append(ev.Instances, inst)
			}
		}
		return ev, true
	case SubTypeSub:
		if req, err := DecodeSubRequest(body); err == nil {
			ev.ServiceName = req.Name
		}
		return ev, true
	case SubTypeUnsub:
		if len(body) >= UnsubPayloadLen {
			ev.ServiceName = CString(body[8:72])
		}
		return ev, true
	case SubTypeSubAck:
		if len(body) >= 16 {
			count := binary.BigEndian.Uint32(body[0:4])
			rst := binary.BigEndian.Uint32(body[4:8])
			if rst == 0 {
				off := 16
				for i := uint32(0); i < count && off+ServiceInstLen <= len(body); i++ {
					if inst, err := ParseInstance(body[off : off+ServiceInstLen]); err == nil {
						if ev.ServiceName == "" {
							ev.ServiceName = inst.Name64
						}
						ev.Instances = append(ev.Instances, inst)
					}
					off += ServiceInstLen
				}
			}
		}
		return ev, true
	case SubTypePubAck, SubTypeUnpubAck, SubTypeUnsubAck:
		return ev, true
	default:
		return ObservedEvent{}, false
	}
}
