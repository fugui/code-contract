package server

import (
	"fmt"
	"sync"
	"sync/atomic"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
)

const maxTrackedDroppedSNOs = 8192

type sessionNoiseFilter struct {
	matcher       *agentproto.NoiseMatcher
	filteredCount atomic.Int64
	mu            sync.Mutex
	droppedSNOs   map[string]struct{}
}

func newSessionNoiseFilter(rules []string) *sessionNoiseFilter {
	return &sessionNoiseFilter{
		matcher:     agentproto.NewNoiseMatcher(rules),
		droppedSNOs: make(map[string]struct{}),
	}
}

func (f *sessionNoiseFilter) Empty() bool {
	return f == nil || f.matcher.Empty()
}

func (f *sessionNoiseFilter) FilteredCount() int64 {
	if f == nil {
		return 0
	}
	return f.filteredCount.Load()
}

func noiseConnSNOKey(h agentproto.EnvelopeHeader, sno uint64) string {
	epA := fmt.Sprintf("%d.%d.%d.%d:%d", h.SrcIPv4[0], h.SrcIPv4[1], h.SrcIPv4[2], h.SrcIPv4[3], h.SrcPort)
	epB := fmt.Sprintf("%d.%d.%d.%d:%d", h.DstIPv4[0], h.DstIPv4[1], h.DstIPv4[2], h.DstIPv4[3], h.DstPort)
	if epA > epB {
		epA, epB = epB, epA
	}
	return fmt.Sprintf("%d:%d:%d:%s<->%s:%d", h.ProcessID, h.Direction, h.ConnSeq, epA, epB, sno)
}

func (f *sessionNoiseFilter) markDroppedSNO(key string) {
	f.mu.Lock()
	if len(f.droppedSNOs) >= maxTrackedDroppedSNOs {
		clear(f.droppedSNOs)
	}
	f.droppedSNOs[key] = struct{}{}
	f.mu.Unlock()
}

func (f *sessionNoiseFilter) hasDroppedSNO(key string, remove bool) bool {
	f.mu.Lock()
	_, ok := f.droppedSNOs[key]
	if ok && remove {
		delete(f.droppedSNOs, key)
	}
	f.mu.Unlock()
	return ok
}

// ShouldDrop 判断单帧二进制流量信封是否命中源头丢弃规则；绝不拦截控制信封与 MNG 帧
func (f *sessionNoiseFilter) ShouldDrop(h agentproto.EnvelopeHeader, payload []byte) bool {
	if f == nil || f.matcher.Empty() {
		return false
	}
	if h.EventType != agentproto.EventTrafficFrame {
		return false
	}
	comm, err := crosrpc.ParseCommHead(payload)
	if err != nil {
		return false
	}

	switch comm.MsgType {
	case crosrpc.MsgTypeMng, crosrpc.MsgTypeMngReq, crosrpc.MsgTypeUUIDReq,
		crosrpc.MsgTypeDirectChanConnReq, crosrpc.MsgTypeDirectChanConnRsp:
		return false

	case crosrpc.MsgTypeReq:
		if len(payload) <= crosrpc.CommHeadLen {
			return false
		}
		reqHead, parseErr := crosrpc.ParseReqHead(payload[crosrpc.CommHeadLen:])
		if parseErr != nil {
			return false
		}
		if f.matcher.Match(reqHead.ServiceName, reqHead.Operation) {
			f.markDroppedSNO(noiseConnSNOKey(h, comm.SNO))
			f.filteredCount.Add(1)
			return true
		}
		return false

	case crosrpc.MsgTypeRsp, crosrpc.MsgTypeRspSeg:
		key := noiseConnSNOKey(h, comm.SNO)
		if f.hasDroppedSNO(key, comm.MsgType == crosrpc.MsgTypeRsp) {
			f.filteredCount.Add(1)
			return true
		}
		if len(payload) <= crosrpc.CommHeadLen {
			return false
		}
		rspHead, parseErr := crosrpc.ParseRspHead(payload[crosrpc.CommHeadLen:], int(comm.HeadLen))
		if parseErr == nil && f.matcher.Match(rspHead.ServiceName, rspHead.Operation) {
			f.filteredCount.Add(1)
			return true
		}
		return false

	default:
		return false
	}
}
