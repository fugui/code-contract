package server

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
	"code-contract/pkg/ebpf"
	"code-contract/pkg/transport"
)

const maxWindowBreakdownKeys = 64

type frameBreakdownKey struct {
	pid     uint32
	channel string
	target  string
}

type statsSnapshot struct {
	ingress      ebpf.IngressStats
	egress       ebpf.EgressStats
	drt          transport.DRTUprobeStats
	rbDrops      int64
	redirected   int64
	bufUsedBytes int64
	bufDropped   int64
	bufTotal     int64
	wsSent       int64
}

func (s *AgentServer) logf(format string, args ...interface{}) {
	if s != nil && s.cfg.LogFunc != nil {
		s.cfg.LogFunc(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (s *AgentServer) recordFrameEnvelope(envBytes []byte) {
	if s == nil || !s.cfg.Verbose || len(envBytes) < agentproto.EnvelopeHeaderLen {
		return
	}
	h, payload, err := agentproto.UnmarshalEnvelope(envBytes)
	if err != nil {
		return
	}
	s.recordFramePayload(h, payload)
}

func (s *AgentServer) recordFramePayload(h agentproto.EnvelopeHeader, payload []byte) {
	if s == nil || !s.cfg.Verbose || h.EventType != agentproto.EventTrafficFrame {
		return
	}

	channel := "TCP-IN"
	if h.TransportProto == crosrpc.TransportMesh {
		channel = "MESH"
	} else if h.Direction == agentproto.DirectionEgress {
		channel = "TCP-OUT"
	}

	target := extractFrameTargetLabel(payload)

	s.windowMu.Lock()
	defer s.windowMu.Unlock()

	if s.windowBreakdown == nil {
		s.windowBreakdown = make(map[frameBreakdownKey]int64)
	}
	key := frameBreakdownKey{
		pid:     h.CapturedPID,
		channel: channel,
		target:  target,
	}
	if _, exists := s.windowBreakdown[key]; !exists && len(s.windowBreakdown) >= maxWindowBreakdownKeys {
		key = frameBreakdownKey{
			pid:     0,
			channel: channel,
			target:  "<other>",
		}
	}
	s.windowBreakdown[key]++
}

func extractFrameTargetLabel(payload []byte) string {
	if len(payload) < crosrpc.CommHeadLen {
		return "<UNKNOWN>"
	}
	if parsed, err := crosrpc.ParsePacket(payload); err == nil && parsed != nil {
		if parsed.Req != nil && parsed.Req.ServiceName != "" {
			if parsed.Req.Operation != "" {
				return parsed.Req.ServiceName + "." + parsed.Req.Operation
			}
			return parsed.Req.ServiceName
		}
		if parsed.Rsp != nil && parsed.Rsp.ServiceName != "" {
			if parsed.Rsp.Operation != "" {
				return parsed.Rsp.ServiceName + "." + parsed.Rsp.Operation
			}
			return parsed.Rsp.ServiceName
		}
		if parsed.Comm.MsgType >= crosrpc.MsgTypeMng {
			return "<MNG>"
		}
	}
	if comm, err := crosrpc.ParseCommHead(payload); err == nil && comm.MsgType >= crosrpc.MsgTypeMng {
		return "<MNG>"
	}
	return "<UNKNOWN>"
}

func (s *AgentServer) captureStatsSnapshot() statsSnapshot {
	var snap statsSnapshot
	if s.ebpfMgr != nil {
		snap.ingress = s.ebpfMgr.IngressStats()
		snap.egress = s.ebpfMgr.EgressStats()
		_, snap.rbDrops, snap.redirected = s.ebpfMgr.GetStats()
	}
	if s.drtDriver != nil {
		snap.drt = s.drtDriver.Stats()
	}
	if s.buffer != nil {
		snap.bufUsedBytes, snap.bufDropped, snap.bufTotal = s.buffer.Stats()
	}
	snap.wsSent = s.wsSentFrames.Load()
	return snap
}

func deltaNonNeg(cur, prev int64) int64 {
	if cur >= prev {
		return cur - prev
	}
	return cur
}

func hasWindowActivity(cur, prev statsSnapshot) bool {
	return deltaNonNeg(cur.ingress.CapturedFrames, prev.ingress.CapturedFrames) > 0 ||
		deltaNonNeg(cur.ingress.GapCount, prev.ingress.GapCount) > 0 ||
		deltaNonNeg(cur.egress.CapturedFrames, prev.egress.CapturedFrames) > 0 ||
		deltaNonNeg(cur.egress.DlinkEvents, prev.egress.DlinkEvents) > 0 ||
		deltaNonNeg(cur.egress.NatMiss, prev.egress.NatMiss) > 0 ||
		deltaNonNeg(cur.egress.DialFail, prev.egress.DialFail) > 0 ||
		deltaNonNeg(cur.redirected, prev.redirected) > 0 ||
		deltaNonNeg(cur.drt.RawEvents, prev.drt.RawEvents) > 0 ||
		deltaNonNeg(cur.drt.ValidFrames, prev.drt.ValidFrames) > 0 ||
		deltaNonNeg(cur.drt.DroppedEvents, prev.drt.DroppedEvents) > 0 ||
		deltaNonNeg(cur.bufDropped, prev.bufDropped) > 0 ||
		deltaNonNeg(cur.wsSent, prev.wsSent) > 0 ||
		deltaNonNeg(cur.rbDrops, prev.rbDrops) > 0
}

func formatStats5sLine(sessionID uint64, cur, prev statsSnapshot, wsConnected bool) string {
	wsState := "未连接"
	if wsConnected {
		wsState = "已连接"
	}
	return fmt.Sprintf(
		"[Agent][Stats 5s] Session=%d | TCP-In: +%d帧(累计%d, REQ:+%d RSP:+%d gap:+%d) | TCP-Out: +%d帧(累计%d, REQ:+%d RSP:+%d conns:%d redir:+%d natMiss:+%d dialFail:+%d dlink:+%d) | Mesh: +%d事件/+%d帧(累计%d, REQ:+%d RSP配对:+%d 孤立:+%d 截断:+%d 丢弃:+%d links:%d) | Buffer: 积压%dKB 丢帧+%d | WS(%s): +%d帧(累计%d) | RingBuf丢包:+%d",
		sessionID,
		deltaNonNeg(cur.ingress.CapturedFrames, prev.ingress.CapturedFrames),
		cur.ingress.CapturedFrames,
		deltaNonNeg(cur.ingress.ReqFrames, prev.ingress.ReqFrames),
		deltaNonNeg(cur.ingress.RspFrames, prev.ingress.RspFrames),
		deltaNonNeg(cur.ingress.GapCount, prev.ingress.GapCount),
		deltaNonNeg(cur.egress.CapturedFrames, prev.egress.CapturedFrames),
		cur.egress.CapturedFrames,
		deltaNonNeg(cur.egress.ReqFrames, prev.egress.ReqFrames),
		deltaNonNeg(cur.egress.RspFrames, prev.egress.RspFrames),
		cur.egress.ActiveConns,
		deltaNonNeg(cur.redirected, prev.redirected),
		deltaNonNeg(cur.egress.NatMiss, prev.egress.NatMiss),
		deltaNonNeg(cur.egress.DialFail, prev.egress.DialFail),
		deltaNonNeg(cur.egress.DlinkEvents, prev.egress.DlinkEvents),
		deltaNonNeg(cur.drt.RawEvents, prev.drt.RawEvents),
		deltaNonNeg(cur.drt.ValidFrames, prev.drt.ValidFrames),
		cur.drt.ValidFrames,
		deltaNonNeg(cur.drt.ReqFrames, prev.drt.ReqFrames),
		deltaNonNeg(cur.drt.RspPaired, prev.drt.RspPaired),
		deltaNonNeg(cur.drt.RspOrphan, prev.drt.RspOrphan),
		deltaNonNeg(cur.drt.TruncatedFrames, prev.drt.TruncatedFrames),
		deltaNonNeg(cur.drt.DroppedEvents, prev.drt.DroppedEvents),
		cur.drt.ActiveLinks,
		cur.bufUsedBytes/1024,
		deltaNonNeg(cur.bufDropped, prev.bufDropped),
		wsState,
		deltaNonNeg(cur.wsSent, prev.wsSent),
		cur.wsSent,
		deltaNonNeg(cur.rbDrops, prev.rbDrops),
	)
}

func formatBreakdownDetailLine(counts map[frameBreakdownKey]int64) string {
	type item struct {
		key   frameBreakdownKey
		count int64
	}
	items := make([]item, 0, len(counts))
	for k, v := range counts {
		if v > 0 {
			items = append(items, item{key: k, count: v})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		if items[i].key.pid != items[j].key.pid {
			return items[i].key.pid < items[j].key.pid
		}
		if items[i].key.channel != items[j].key.channel {
			return items[i].key.channel < items[j].key.channel
		}
		return items[i].key.target < items[j].key.target
	})
	if len(items) > 8 {
		items = items[:8]
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		pidStr := fmt.Sprintf("%d", it.key.pid)
		if it.key.pid == 0 {
			pidStr = "*"
		}
		parts = append(parts, fmt.Sprintf("PID=%s [%s] %s:+%d", pidStr, it.key.channel, it.key.target, it.count))
	}
	return "[Agent][Stats 5s][Detail] " + strings.Join(parts, ", ")
}

func (s *AgentServer) drainWindowBreakdown() map[frameBreakdownKey]int64 {
	s.windowMu.Lock()
	defer s.windowMu.Unlock()
	if len(s.windowBreakdown) == 0 {
		return nil
	}
	cp := s.windowBreakdown
	s.windowBreakdown = make(map[frameBreakdownKey]int64)
	return cp
}

func (s *AgentServer) statsLoop() {
	interval := s.cfg.StatsInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var (
		hasPrev     bool
		prevSession uint64
		prev        statsSnapshot
		idleTicks   int
	)

	for {
		select {
		case <-s.statsStop:
			return
		case <-ticker.C:
			leaseType, refID := s.ActiveLease()
			if leaseType != "RECORDING" || refID == 0 {
				hasPrev = false
				prevSession = 0
				idleTicks = 0
				s.drainWindowBreakdown()
				continue
			}

			cur := s.captureStatsSnapshot()
			if !hasPrev || prevSession != refID {
				prev = statsSnapshot{}
				prevSession = refID
				hasPrev = true
				idleTicks = 0
			}

			active := hasWindowActivity(cur, prev)
			if !active {
				idleTicks++
				if !s.cfg.Verbose && idleTicks < 3 {
					prev = cur
					s.drainWindowBreakdown()
					continue
				}
			}
			idleTicks = 0

			wsConnected := atomic.LoadInt32(&s.wsActive) > 0
			s.logf("%s", formatStats5sLine(refID, cur, prev, wsConnected))

			if s.cfg.Verbose {
				if counts := s.drainWindowBreakdown(); len(counts) > 0 {
					s.logf("%s", formatBreakdownDetailLine(counts))
				}
			}
			prev = cur
		}
	}
}
