package agentproto

import (
	"bytes"
	"net"
	"testing"
)

func TestEnvelopeHeaderMarshalUnmarshal(t *testing.T) {
	rawPayload := []byte("CROS-RPC-RAW-PACKET-BYTES-PAYLOAD-FOR-TESTING")

	header := EnvelopeHeader{
		Magic:           EnvelopeMagic,
		ProtoVer:        EnvelopeProtoVer,
		EventType:       EventTrafficFrame,
		SessionOrTaskID: 998877665544,
		ProcessID:       12345,
		CapturedPID:     8899,
		Direction:       DirectionIngress,
		Flags:           FlagIsTruncated | FlagRefKind | FlagHasGap,
		Reserved:        0,
		CapturedAtNs:    1711223344556677,
		SrcPort:         8091,
		DstPort:         9092,
		ConnSeq:         42,
	}
	header.SetSrcIP(net.ParseIP("192.168.56.18"))
	header.SetDstIP(net.ParseIP("10.0.0.1"))

	encoded := MarshalEnvelope(header, rawPayload)
	if len(encoded) != EnvelopeHeaderLen+len(rawPayload) {
		t.Fatalf("序列化信封总长度期望为 %d，实际得到 %d", EnvelopeHeaderLen+len(rawPayload), len(encoded))
	}

	decodedHeader, payload, err := UnmarshalEnvelope(encoded)
	if err != nil {
		t.Fatalf("UnmarshalEnvelope 失败: %v", err)
	}

	// 字段逐一比对，断言 100% 无损还原
	if decodedHeader.Magic != EnvelopeMagic {
		t.Fatalf("Magic 不匹配: %x", decodedHeader.Magic)
	}
	if decodedHeader.ProtoVer != 1 {
		t.Fatalf("ProtoVer 不匹配: %d", decodedHeader.ProtoVer)
	}
	if decodedHeader.EventType != EventTrafficFrame {
		t.Fatalf("EventType 不匹配: %d", decodedHeader.EventType)
	}
	if decodedHeader.SessionOrTaskID != 998877665544 {
		t.Fatalf("SessionOrTaskID 不匹配: %d", decodedHeader.SessionOrTaskID)
	}
	if decodedHeader.ProcessID != 12345 {
		t.Fatalf("ProcessID 不匹配: %d", decodedHeader.ProcessID)
	}
	if decodedHeader.CapturedPID != 8899 {
		t.Fatalf("CapturedPID 不匹配: %d", decodedHeader.CapturedPID)
	}
	if decodedHeader.Direction != DirectionIngress {
		t.Fatalf("Direction 不匹配: %d", decodedHeader.Direction)
	}
	if decodedHeader.CapturedAtNs != 1711223344556677 {
		t.Fatalf("CapturedAtNs 不匹配: %d", decodedHeader.CapturedAtNs)
	}
	if decodedHeader.SrcPort != 8091 || decodedHeader.DstPort != 9092 {
		t.Fatalf("Port 不匹配: %d -> %d", decodedHeader.SrcPort, decodedHeader.DstPort)
	}
	if decodedHeader.ConnSeq != 42 {
		t.Fatalf("ConnSeq 不匹配: %d", decodedHeader.ConnSeq)
	}
	if decodedHeader.PayloadLen != uint32(len(rawPayload)) {
		t.Fatalf("PayloadLen 不匹配: %d", decodedHeader.PayloadLen)
	}

	// 辅助方法断言
	if !decodedHeader.IsTruncated() {
		t.Fatalf("期望 IsTruncated 为 true")
	}
	if decodedHeader.RefKind() != RefKindReplayTask {
		t.Fatalf("期望 RefKind 为 ReplayTask (1)")
	}
	if !decodedHeader.HasGap() {
		t.Fatalf("期望 HasGap 为 true")
	}
	if decodedHeader.SrcIP().String() != "192.168.56.18" {
		t.Fatalf("SrcIP 不匹配: %s", decodedHeader.SrcIP().String())
	}
	if decodedHeader.DstIP().String() != "10.0.0.1" {
		t.Fatalf("DstIP 不匹配: %s", decodedHeader.DstIP().String())
	}
	if !bytes.Equal(payload, rawPayload) {
		t.Fatalf("反序列化载荷与原始载荷不一致")
	}

	// 异常帧测试: 长度不足 56B
	if _, _, err := UnmarshalEnvelope(encoded[:50]); err != ErrEnvelopeBufferTooShort {
		t.Fatalf("小于 56B 期望 ErrEnvelopeBufferTooShort，实际得到: %v", err)
	}

	// 异常帧测试: 错误魔术字
	badMagicEncoded := make([]byte, len(encoded))
	copy(badMagicEncoded, encoded)
	badMagicEncoded[0] = 0x12
	badMagicEncoded[1] = 0x34
	if _, _, err := UnmarshalEnvelope(badMagicEncoded); err != ErrInvalidEnvelopeMagic {
		t.Fatalf("错误魔术字期望 ErrInvalidEnvelopeMagic，实际得到: %v", err)
	}

	// 异常帧测试: 截断载荷
	truncatedPayload := encoded[:len(encoded)-5]
	if _, _, err := UnmarshalEnvelope(truncatedPayload); err != ErrPayloadLengthMismatch {
		t.Fatalf("截断载荷期望 ErrPayloadLengthMismatch，实际得到: %v", err)
	}
}

func TestEnvelopeHeader_Offset26TransportProto_56BytesBackwardCompat(t *testing.T) {
	rawPayload := []byte("MESH-TRAFFIC-PAYLOAD")

	// 1. 验证 TransportProto=4 (Mesh) 序列化与反序列化
	hMesh := EnvelopeHeader{
		Magic:           EnvelopeMagic,
		ProtoVer:        EnvelopeProtoVer,
		EventType:       EventTrafficFrame,
		SessionOrTaskID: 101,
		ProcessID:       202,
		CapturedPID:     303,
		Direction:       DirectionIngress,
		Flags:           0,
		TransportProto:  4, // MESH
		Reserved:        0,
		CapturedAtNs:    99887766,
		SrcPort:         1234,
		DstPort:         5678,
		ConnSeq:         1,
	}

	buf := MarshalEnvelope(hMesh, rawPayload)
	if len(buf) != EnvelopeHeaderLen+len(rawPayload) {
		t.Fatalf("envelope length mismatch: expected %d, got %d", EnvelopeHeaderLen+len(rawPayload), len(buf))
	}
	if buf[26] != 4 {
		t.Fatalf("expected offset 26 to be TransportProto=4, got %d", buf[26])
	}
	if buf[27] != 0 {
		t.Fatalf("expected offset 27 to be Reserved=0, got %d", buf[27])
	}

	decoded, payload, err := UnmarshalEnvelope(buf)
	if err != nil {
		t.Fatalf("UnmarshalEnvelope failed: %v", err)
	}
	if decoded.TransportProto != 4 {
		t.Fatalf("expected decoded TransportProto == 4, got %d", decoded.TransportProto)
	}
	if decoded.Reserved != 0 {
		t.Fatalf("expected decoded Reserved == 0, got %d", decoded.Reserved)
	}
	if !bytes.Equal(payload, rawPayload) {
		t.Fatalf("payload mismatch")
	}

	// 2. 向后兼容验证：旧版本写入 offset 26..27 全 0
	bufLegacy := make([]byte, EnvelopeHeaderLen+len(rawPayload))
	copy(bufLegacy, buf)
	bufLegacy[26] = 0
	bufLegacy[27] = 0

	decodedLegacy, _, err := UnmarshalEnvelope(bufLegacy)
	if err != nil {
		t.Fatalf("UnmarshalEnvelope on legacy packet failed: %v", err)
	}
	if decodedLegacy.TransportProto != 0 {
		t.Fatalf("legacy packet expected TransportProto == 0, got %d", decodedLegacy.TransportProto)
	}
}
