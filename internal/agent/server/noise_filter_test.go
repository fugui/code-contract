package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
)

func buildTestReqPacket(sno uint64, svc, op string) []byte {
	comm := crosrpc.CommHead{
		TransportProto: crosrpc.TransportTCP,
		SNO:            sno,
		Length:         uint32(crosrpc.ReqHeadMinLen + 4),
		HeadLen:        crosrpc.ReqHeadMinLen,
		MsgType:        crosrpc.MsgTypeReq,
		IsPacked:       1,
	}
	copy(comm.Magic[:], crosrpc.MagicPrefix)

	reqHead := make([]byte, crosrpc.ReqHeadMinLen)
	binary.BigEndian.PutUint16(reqHead[0:2], 1)
	binary.BigEndian.PutUint16(reqHead[2:4], crosrpc.ReqHeadMinLen)
	copy(reqHead[8:72], svc)
	copy(reqHead[72:136], op)
	reqHead[136] = crosrpc.ReqTypeOneway

	return append(append(comm.Marshal(), reqHead...), []byte{1, 2, 3, 4}...)
}

func buildTestRspPacket(sno uint64, msgType uint8, svc, op string) []byte {
	comm := crosrpc.CommHead{
		TransportProto: crosrpc.TransportTCP,
		SNO:            sno,
		Length:         uint32(crosrpc.RspHeadMinLen + 2),
		HeadLen:        crosrpc.RspHeadMinLen,
		MsgType:        msgType,
		IsPacked:       1,
	}
	copy(comm.Magic[:], crosrpc.MagicPrefix)

	rspHead := make([]byte, crosrpc.RspHeadMinLen)
	binary.BigEndian.PutUint16(rspHead[0:2], 1)
	binary.BigEndian.PutUint16(rspHead[2:4], crosrpc.RspHeadMinLen)
	copy(rspHead[24:88], svc)
	copy(rspHead[88:152], op)

	return append(append(comm.Marshal(), rspHead...), []byte{0x08, 0x01}...)
}

func buildTestMngPacket(sno uint64, procName string) []byte {
	comm := crosrpc.CommHead{
		TransportProto: crosrpc.TransportTCP,
		SNO:            sno,
		Length:         uint32(crosrpc.MngHeadMinLen),
		HeadLen:        crosrpc.MngHeadMinLen,
		MsgType:        crosrpc.MsgTypeMng,
		IsPacked:       1,
	}
	copy(comm.Magic[:], crosrpc.MagicPrefix)
	mng := make([]byte, crosrpc.MngHeadMinLen)
	copy(mng[8:72], procName)
	return append(comm.Marshal(), mng...)
}

func TestSessionNoiseFilter_PairedDropAndControlPassthrough(t *testing.T) {
	f := newSessionNoiseFilter([]string{"*LogService*", "MonitorSvc:Ping"})

	envBase := agentproto.EnvelopeHeader{
		Magic:           agentproto.EnvelopeMagic,
		ProtoVer:        agentproto.EnvelopeProtoVer,
		EventType:       agentproto.EventTrafficFrame,
		SessionOrTaskID: 10,
		ProcessID:       1,
		Direction:       agentproto.DirectionEgress,
		SrcPort:         40001,
		DstPort:         8080,
		ConnSeq:         3,
	}

	// 1. 命中规则的 REQ -> 丢弃并记录 SNO
	reqLog := buildTestReqPacket(101, "dophi.LogService", "Write")
	if !f.ShouldDrop(envBase, reqLog) {
		t.Fatalf("expected LogService REQ to be dropped")
	}

	// 2. 同 SNO 应答帧（即便应答未回填 ServiceName）-> 通过 SNO 配对同步丢弃
	rspSegEmpty := buildTestRspPacket(101, crosrpc.MsgTypeRspSeg, "", "")
	if !f.ShouldDrop(envBase, rspSegEmpty) {
		t.Fatalf("expected paired RSP_SEG to be dropped by SNO")
	}
	rspEmpty := buildTestRspPacket(101, crosrpc.MsgTypeRsp, "", "")
	if !f.ShouldDrop(envBase, rspEmpty) {
		t.Fatalf("expected paired RSP to be dropped by SNO")
	}

	// 3. 非噪音业务 REQ -> 放行
	reqBiz := buildTestReqPacket(102, "dophi.OrderService", "CreateOrder")
	if f.ShouldDrop(envBase, reqBiz) {
		t.Fatalf("expected OrderService REQ to pass through")
	}

	// 4. MNG 管理帧 -> 永不丢弃
	mng := buildTestMngPacket(103, "dophi.LogService")
	if f.ShouldDrop(envBase, mng) {
		t.Fatalf("MNG frame must never be dropped")
	}

	// 5. EventSessionDrained 等非流量事件 -> 永不丢弃
	drainedEnv := envBase
	drainedEnv.EventType = agentproto.EventSessionDrained
	if f.ShouldDrop(drainedEnv, nil) {
		t.Fatalf("EventSessionDrained must never be dropped")
	}

	if got := f.FilteredCount(); got != 3 {
		t.Fatalf("expected filtered count 3, got %d", got)
	}
}

func TestAgentServer_NoiseFilterSyncAndStopStats(t *testing.T) {
	srv, err := NewAgentServer(AgentServerConfig{
		ClaimCode: "test-claim",
	})
	if err != nil {
		t.Fatalf("NewAgentServer failed: %v", err)
	}
	defer srv.buffer.Close()

	srv.setNoiseFilter([]string{"*LogService*"})
	envHeader := agentproto.EnvelopeHeader{
		Magic:           agentproto.EnvelopeMagic,
		ProtoVer:        agentproto.EnvelopeProtoVer,
		EventType:       agentproto.EventTrafficFrame,
		SessionOrTaskID: 55,
		ProcessID:       9,
		Direction:       agentproto.DirectionEgress,
	}
	noisePayload := buildTestReqPacket(1, "infra.LogService", "Append")
	bizPayload := buildTestReqPacket(2, "biz.TradeService", "Pay")

	// 通过 eBPF FrameSink 与 Mesh payload 入口模拟上行
	srv.pushIncomingEnvelope(agentproto.MarshalEnvelope(envHeader, noisePayload))
	srv.pushIncomingPayload(envHeader, bizPayload)

	_, _, total := srv.buffer.Stats()
	if total != 1 {
		t.Fatalf("expected 1 frame pushed to buffer (noise dropped), got %d", total)
	}

	// 模拟停录响应返回 filtered_noise_frames=1
	srv.mu.Lock()
	srv.activeLeaseType = "RECORDING"
	srv.activeRefID = 55
	srv.mu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/agent/v1/monitors/55/stop", bytes.NewReader(nil))
	req.SetPathValue("sessionId", "55")
	rec := httptest.NewRecorder()
	srv.handleStopMonitor(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop monitor status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp agentproto.StopMonitorResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode StopMonitorResp failed: %v", err)
	}
	if resp.FilteredNoiseFrames != 1 {
		t.Fatalf("expected FilteredNoiseFrames=1, got %d", resp.FilteredNoiseFrames)
	}
	if srv.currentFilteredNoiseFrames() != 0 {
		t.Fatalf("expected noise filter reset after stop")
	}
}
