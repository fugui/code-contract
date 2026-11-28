package recorder

import (
	"encoding/binary"
	"testing"

	"code-contract/models"
	"code-contract/pkg/crosrpc"
)

func buildTestV04RspPacket(
	sno uint64,
	clientSendTime uint64,
	serverRecvTime uint64,
	serverSendTime uint64,
	serverEnqueue *uint64,
	svc, op string,
) []byte {
	rspHeadLen := 200
	rawRsp := make([]byte, rspHeadLen)
	binary.BigEndian.PutUint16(rawRsp[0:2], 4)
	binary.BigEndian.PutUint16(rawRsp[2:4], uint16(rspHeadLen))
	binary.BigEndian.PutUint32(rawRsp[4:8], 0)
	binary.BigEndian.PutUint64(rawRsp[32:40], clientSendTime)
	binary.BigEndian.PutUint64(rawRsp[40:48], serverRecvTime)
	binary.BigEndian.PutUint64(rawRsp[48:56], serverSendTime)
	copy(rawRsp[56:120], []byte(svc))
	copy(rawRsp[120:184], []byte(op))
	binary.BigEndian.PutUint16(rawRsp[192:194], 1)

	var extBuf []byte
	if serverEnqueue != nil {
		ext := make([]byte, 12) // 4B comm + 8B timestamp
		binary.BigEndian.PutUint16(ext[0:2], 8)
		ext[2] = crosrpc.ExtTypeTimeStamp
		binary.BigEndian.PutUint64(ext[4:12], *serverEnqueue)
		extBuf = append(extBuf, ext...)
	}

	totalHeadLen := uint16(rspHeadLen + len(extBuf))
	comm := crosrpc.CommHead{
		TransportProto: crosrpc.TransportTCP,
		SNO:            sno,
		Length:         uint32(totalHeadLen),
		HeadLen:        totalHeadLen,
		MsgType:        crosrpc.MsgTypeRsp,
		IsPacked:       1,
	}
	copy(comm.Magic[:], []byte(crosrpc.MagicPrefix))

	var pkt []byte
	pkt = append(pkt, comm.Marshal()...)
	pkt = append(pkt, rawRsp...)
	pkt = append(pkt, extBuf...)
	return pkt
}

func TestBuildPacketAnatomy_TimingBreakdownSafety(t *testing.T) {
	t.Run("V04 cross-host uptime timestamps do not leak ClientSendTime or cross-host diff", func(t *testing.T) {
		latUs := int64(1200)
		clientUptimeNs := uint64(36_000_000_000_000) // 10h monotonic uptime
		serverRecvNs := uint64(50_000_000_000_000)
		serverSendNs := uint64(50_000_000_850_000)

		rspRaw := buildTestV04RspPacket(101, clientUptimeNs, serverRecvNs, serverSendNs, nil, "demo.OrderSvc", "Create")
		rec := &models.TrafficRecord{
			ID:           1,
			SessionID:    10,
			Direction:    models.TrafficDirIngress,
			ExchangeType: models.ExchangeUnary,
			LatencyUs:    &latUs,
			RawRspPacket: rspRaw,
			IsReplayable: true,
		}

		dto, err := BuildPacketAnatomy(rec, nil)
		if err != nil {
			t.Fatalf("BuildPacketAnatomy failed: %v", err)
		}
		if dto.RspSide == nil || dto.RspSide.SessionHead.TimingBreakdownUs == nil {
			t.Fatalf("expected non-nil TimingBreakdownUs")
		}

		tb := dto.RspSide.SessionHead.TimingBreakdownUs
		if tb.ClientToServerUs != nil {
			t.Fatalf("ClientToServerUs must be nil, got %d", *tb.ClientToServerUs)
		}
		if tb.ServerProcessUs == nil || *tb.ServerProcessUs != 850 {
			t.Fatalf("ServerProcessUs expected 850, got %v", tb.ServerProcessUs)
		}
		if tb.ServerExecUs == nil || *tb.ServerExecUs != 850 {
			t.Fatalf("ServerExecUs expected 850, got %v", tb.ServerExecUs)
		}
		if tb.NetworkAndFramingUs == nil || *tb.NetworkAndFramingUs != 350 {
			t.Fatalf("NetworkAndFramingUs expected 350, got %v", tb.NetworkAndFramingUs)
		}
		if *tb.NetworkAndFramingUs+*tb.ServerExecUs != 1200 {
			t.Fatalf("expected sum == 1200, got %d", *tb.NetworkAndFramingUs+*tb.ServerExecUs)
		}

		foundClientSendField := false
		for _, f := range dto.RspSide.SessionHead.Fields {
			if f.Name == "ullClientSendTime" && f.OffsetRange == "32..39" && f.Value == "36000000000000" {
				foundClientSendField = true
				break
			}
		}
		if !foundClientSendField {
			t.Errorf("expected ullClientSendTime in SessionHead.Fields, got %+v", dto.RspSide.SessionHead.Fields)
		}
	})

	t.Run("same-host monotonic timestamps with valid ServerEnqueue", func(t *testing.T) {
		latUs := int64(1200)
		serverRecv := uint64(10_000_000_000)
		serverEnq := uint64(10_000_100_000)
		serverSend := uint64(10_000_900_000)

		rspRaw := buildTestV04RspPacket(102, 1, serverRecv, serverSend, &serverEnq, "demo.OrderSvc", "Create")
		rec := &models.TrafficRecord{
			ID:           2,
			SessionID:    10,
			Direction:    models.TrafficDirIngress,
			ExchangeType: models.ExchangeUnary,
			LatencyUs:    &latUs,
			RawRspPacket: rspRaw,
		}

		dto, err := BuildPacketAnatomy(rec, nil)
		if err != nil {
			t.Fatalf("BuildPacketAnatomy failed: %v", err)
		}
		tb := dto.RspSide.SessionHead.TimingBreakdownUs
		if tb == nil {
			t.Fatalf("expected non-nil TimingBreakdownUs")
		}
		if tb.QueueWaitUs == nil || *tb.QueueWaitUs != 100 {
			t.Fatalf("QueueWaitUs expected 100, got %v", tb.QueueWaitUs)
		}
		if tb.ServerExecUs == nil || *tb.ServerExecUs != 800 {
			t.Fatalf("ServerExecUs expected 800, got %v", tb.ServerExecUs)
		}
		if tb.ServerProcessUs == nil || *tb.ServerProcessUs != 900 {
			t.Fatalf("ServerProcessUs expected 900, got %v", tb.ServerProcessUs)
		}
		if tb.NetworkAndFramingUs == nil || *tb.NetworkAndFramingUs != 300 {
			t.Fatalf("NetworkAndFramingUs expected 300, got %v", tb.NetworkAndFramingUs)
		}
	})

	t.Run("ServerEnqueue in different clock domain is ignored safely", func(t *testing.T) {
		latUs := int64(1200)
		serverRecv := uint64(10_000_000_000)
		serverSend := uint64(10_000_900_000)
		badEnq := uint64(1_760_000_000_000_000_000) // Unix ns vs monotonic ns

		rspRaw := buildTestV04RspPacket(103, 1, serverRecv, serverSend, &badEnq, "demo.OrderSvc", "Create")
		rec := &models.TrafficRecord{
			ID:           3,
			SessionID:    10,
			Direction:    models.TrafficDirIngress,
			ExchangeType: models.ExchangeUnary,
			LatencyUs:    &latUs,
			RawRspPacket: rspRaw,
		}

		dto, err := BuildPacketAnatomy(rec, nil)
		if err != nil {
			t.Fatalf("BuildPacketAnatomy failed: %v", err)
		}
		tb := dto.RspSide.SessionHead.TimingBreakdownUs
		if tb == nil {
			t.Fatalf("expected non-nil TimingBreakdownUs")
		}
		if tb.QueueWaitUs != nil {
			t.Fatalf("expected QueueWaitUs == nil when clock domain mismatches, got %d", *tb.QueueWaitUs)
		}
		if tb.ServerExecUs == nil || *tb.ServerExecUs != 900 {
			t.Fatalf("ServerExecUs expected 900, got %v", tb.ServerExecUs)
		}
	})

	t.Run("Unix microsecond server timestamps are detected properly", func(t *testing.T) {
		latUs := int64(1200)
		serverRecvUs := uint64(1_764_315_000_000_000)
		serverSendUs := uint64(1_764_315_000_000_850)

		rspRaw := buildTestV04RspPacket(104, 1, serverRecvUs, serverSendUs, nil, "demo.OrderSvc", "Create")
		rec := &models.TrafficRecord{
			ID:           4,
			SessionID:    10,
			Direction:    models.TrafficDirIngress,
			ExchangeType: models.ExchangeUnary,
			LatencyUs:    &latUs,
			RawRspPacket: rspRaw,
		}

		dto, err := BuildPacketAnatomy(rec, nil)
		if err != nil {
			t.Fatalf("BuildPacketAnatomy failed: %v", err)
		}
		tb := dto.RspSide.SessionHead.TimingBreakdownUs
		if tb == nil {
			t.Fatalf("expected non-nil TimingBreakdownUs")
		}
		if tb.ServerProcessUs == nil || *tb.ServerProcessUs != 850 {
			t.Fatalf("ServerProcessUs expected 850, got %v", tb.ServerProcessUs)
		}
		if tb.NetworkAndFramingUs == nil || *tb.NetworkAndFramingUs != 350 {
			t.Fatalf("NetworkAndFramingUs expected 350, got %v", tb.NetworkAndFramingUs)
		}
	})

	t.Run("only ClientSendTime or distorted server timestamps yield nil TimingBreakdownUs", func(t *testing.T) {
		latUs := int64(1200)
		rspOnlyClient := buildTestV04RspPacket(105, 36_000_000_000_000, 0, 0, nil, "demo.OrderSvc", "Create")
		rec1 := &models.TrafficRecord{
			ID:           5,
			SessionID:    10,
			Direction:    models.TrafficDirIngress,
			ExchangeType: models.ExchangeUnary,
			LatencyUs:    &latUs,
			RawRspPacket: rspOnlyClient,
		}
		dto1, err := BuildPacketAnatomy(rec1, nil)
		if err != nil {
			t.Fatalf("BuildPacketAnatomy failed: %v", err)
		}
		if dto1.RspSide.SessionHead.TimingBreakdownUs != nil {
			t.Fatalf("expected nil TimingBreakdownUs when server timestamps are 0, got %+v", dto1.RspSide.SessionHead.TimingBreakdownUs)
		}

		// Distorted server timestamps: ServerSendTime - ServerRecvTime far exceeds LatencyUs
		rspDistorted := buildTestV04RspPacket(106, 0, 10_000_000_000, 50_000_000_000, nil, "demo.OrderSvc", "Create")
		rec2 := &models.TrafficRecord{
			ID:           6,
			SessionID:    10,
			Direction:    models.TrafficDirIngress,
			ExchangeType: models.ExchangeUnary,
			LatencyUs:    &latUs,
			RawRspPacket: rspDistorted,
		}
		dto2, err := BuildPacketAnatomy(rec2, nil)
		if err != nil {
			t.Fatalf("BuildPacketAnatomy failed: %v", err)
		}
		if dto2.RspSide.SessionHead.TimingBreakdownUs != nil {
			t.Fatalf("expected nil TimingBreakdownUs when server delta far exceeds RTT, got %+v", dto2.RspSide.SessionHead.TimingBreakdownUs)
		}
	})

	t.Run("V01 response frame yields nil TimingBreakdownUs", func(t *testing.T) {
		latUs := int64(500)
		rspV01 := buildCrosRspFrame(107, crosrpc.MsgTypeRsp, 1, 0, "demo.OrderSvc", "Create", []byte{0x08, 0x00})
		rec := &models.TrafficRecord{
			ID:           7,
			SessionID:    10,
			Direction:    models.TrafficDirIngress,
			ExchangeType: models.ExchangeUnary,
			LatencyUs:    &latUs,
			RawRspPacket: rspV01,
		}
		dto, err := BuildPacketAnatomy(rec, nil)
		if err != nil {
			t.Fatalf("BuildPacketAnatomy failed: %v", err)
		}
		if dto.RspSide.SessionHead.TimingBreakdownUs != nil {
			t.Fatalf("expected nil TimingBreakdownUs for V01 frame, got %+v", dto.RspSide.SessionHead.TimingBreakdownUs)
		}
	})
}
