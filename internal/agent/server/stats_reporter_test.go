package server

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
	"code-contract/pkg/transport"
)

func buildTestReqPacketForStats(proto uint8, sno uint64, svc, op, payload string) []byte {
	headLen := crosrpc.ReqHeadMinLen
	bodyLen := uint32(headLen + len(payload))
	pkt := make([]byte, crosrpc.CommHeadLen+int(bodyLen))
	copy(pkt[0:7], []byte(crosrpc.MagicPrefix))
	pkt[7] = proto
	binary.BigEndian.PutUint64(pkt[8:16], sno)
	binary.BigEndian.PutUint32(pkt[16:20], bodyLen)
	binary.BigEndian.PutUint16(pkt[20:22], uint16(headLen))
	pkt[22] = crosrpc.MsgTypeReq
	pkt[23] = 1

	// ReqHead
	binary.BigEndian.PutUint16(pkt[24:26], 1)
	binary.BigEndian.PutUint16(pkt[26:28], uint16(headLen))
	copy(pkt[24+8:24+72], []byte(svc))
	copy(pkt[24+72:24+136], []byte(op))
	pkt[24+136] = crosrpc.ReqTypeSync
	copy(pkt[24+headLen:], []byte(payload))
	return pkt
}

func TestStatsReporter_5sRollingAndVerboseDetailNoPayload(t *testing.T) {
	var mu sync.Mutex
	var logs []string

	srv, err := NewAgentServer(AgentServerConfig{
		ListenAddr:    "127.0.0.1:0",
		Verbose:       true,
		StatsInterval: 40 * time.Millisecond,
		LogFunc: func(format string, args ...interface{}) {
			mu.Lock()
			logs = append(logs, fmt.Sprintf(format, args...))
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewAgentServer failed: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// 模拟处于 RECORDING 租约期
	srv.mu.Lock()
	srv.activeLeaseType = "RECORDING"
	srv.activeRefID = 42
	srv.mu.Unlock()

	secretBody := "SECRET_BUSINESS_BODY_DO_NOT_LOG"
	meshPkt := buildTestReqPacketForStats(crosrpc.TransportMesh, 9001, "com.demo.OrderService", "CreateOrder", secretBody)
	err = srv.drtDriver.ProcessEvent(transport.DRTUprobeEvent{
		SessionID:   42,
		ProcessID:   100,
		PID:         3456,
		TimestampNs: uint64(time.Now().UnixNano()),
		Handle:      0x12,
		DataLen:     uint32(len(meshPkt)),
		Data:        meshPkt,
	}, transport.FrameSinkFunc(func(h agentproto.EnvelopeHeader, payload []byte) error {
		srv.recordFramePayload(h, payload)
		srv.buffer.Push(agentproto.MarshalEnvelope(h, payload))
		return nil
	}))
	if err != nil {
		t.Fatalf("ProcessEvent failed: %v", err)
	}

	deadline := time.Now().Add(1 * time.Second)
	var joined string
	for time.Now().Before(deadline) {
		mu.Lock()
		joined = strings.Join(logs, "\n")
		mu.Unlock()
		if strings.Contains(joined, "[Agent][Stats 5s] Session=42") && strings.Contains(joined, "[Agent][Stats 5s][Detail]") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !strings.Contains(joined, "[Agent][Stats 5s] Session=42") {
		t.Fatalf("expected [Agent][Stats 5s] line, got:\n%s", joined)
	}
	if !strings.Contains(joined, "Mesh: +1事件/+1帧(累计1, REQ:+1") {
		t.Fatalf("expected Mesh rolling delta in stats line, got:\n%s", joined)
	}
	if !strings.Contains(joined, "[Agent][Stats 5s][Detail] PID=3456 [MESH] com.demo.OrderService.CreateOrder:+1") {
		t.Fatalf("expected verbose breakdown detail line, got:\n%s", joined)
	}
	if strings.Contains(joined, secretBody) {
		t.Fatalf("stats logs must NEVER contain business payload, got:\n%s", joined)
	}

	// 验证 Shutdown 停止统计协程，不再继续刷日志
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
	mu.Lock()
	countAfterStop := len(logs)
	mu.Unlock()

	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	countLater := len(logs)
	mu.Unlock()
	if countLater != countAfterStop {
		t.Fatalf("expected no stats logs after Shutdown, before=%d after=%d", countAfterStop, countLater)
	}
}
