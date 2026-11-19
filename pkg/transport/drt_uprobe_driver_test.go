package transport

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
)

func buildMockCrosPacket(proto uint8, sno uint64, msgType uint8, body []byte) []byte {
	head := crosrpc.CommHead{
		TransportProto: proto,
		SNO:            sno,
		Length:         uint32(crosrpc.CommHeadLen + len(body)),
		HeadLen:        uint16(crosrpc.CommHeadLen),
		MsgType:        msgType,
		IsPacked:       0,
	}
	copy(head.Magic[:], []byte(crosrpc.MagicPrefix))
	hBytes := head.Marshal()
	return append(hBytes, body...)
}

func TestDRTUprobeDriver_PayloadOffsetAutoAndFixed_AndCorrelatesBidirectionalExchange(t *testing.T) {
	// 1. 测试载荷偏移提取：固定偏移与首 64B "CSE.TCP" 自动锚定
	t.Run("PayloadOffset_AutoAndFixed", func(t *testing.T) {
		pkt := buildMockCrosPacket(crosrpc.TransportMesh, 1001, crosrpc.MsgTypeReq, []byte("hello"))

		// Case A: 偏移为 0，首字节即为 "CSE.TCP"
		res, off := ExtractCROSPacket(pkt, 0)
		if off != 0 || len(res) != len(pkt) {
			t.Errorf("expected off=0, got %d", off)
		}

		// Case B: 带 16 字节私有头部，配置 PayloadOffset = 16
		prefixed := append(make([]byte, 16), pkt...)
		res, off = ExtractCROSPacket(prefixed, 16)
		if off != 16 || len(res) != len(pkt) {
			t.Errorf("expected off=16, got %d", off)
		}

		// Case C: 带 12 字节私有头部，配置 PayloadOffset = 0 (自动锚定)
		prefixed12 := append(make([]byte, 12), pkt...)
		res, off = ExtractCROSPacket(prefixed12, 0)
		if off != 12 || len(res) != len(pkt) {
			t.Errorf("expected auto off=12, got %d", off)
		}
	})

	// 2. 注入 200 次往返模拟事件，验证 ADR-11 双向相关性归一化与 56B 信封封装
	t.Run("BidirectionalExchange_200Roundtrips_Correlation", func(t *testing.T) {
		drv := NewDRTUprobeDriver(nil, DRTUprobeConfig{
			ProcRoot:      "/proc",
			PayloadOffset: 0,
			MaxInflight:   1000,
			InflightTTL:   30 * time.Second,
		})

		var capturedEnvs []agentproto.EnvelopeHeader
		var capturedPayloads [][]byte

		sink := FrameSinkFunc(func(h agentproto.EnvelopeHeader, payload []byte) error {
			capturedEnvs = append(capturedEnvs, h)
			capturedPayloads = append(capturedPayloads, payload)
			return nil
		})

		sessionID := uint64(8888)
		clientProcID := uint64(101)
		clientPID := uint32(5001)
		serverProcID := uint64(202)
		serverPID := uint32(6002)

		totalRounds := 200
		for i := 1; i <= totalRounds; i++ {
			sno := uint64(20000 + i)
			handle := uint64(0xabcd0000 + uint64(i%10))

			// (A) 模拟客户端写入 REQ (ProcessID = 101, PID = 5001)
			reqPkt := buildMockCrosPacket(crosrpc.TransportMesh, sno, crosrpc.MsgTypeReq, []byte(fmt.Sprintf("req-payload-%d", i)))
			reqEvt := DRTUprobeEvent{
				SessionID:   sessionID,
				ProcessID:   clientProcID,
				TimestampNs: uint64(time.Now().UnixNano()),
				Handle:      handle,
				PID:         clientPID,
				DataLen:     uint32(len(reqPkt)),
				Data:        reqPkt,
			}
			if err := drv.ProcessEvent(reqEvt, sink); err != nil {
				t.Fatalf("round %d req failed: %v", i, err)
			}

			// (B) 模拟服务端写入 RSP (ProcessID = 202, PID = 6002)
			rspPkt := buildMockCrosPacket(crosrpc.TransportMesh, sno, crosrpc.MsgTypeRsp, []byte(fmt.Sprintf("rsp-payload-%d", i)))
			rspEvt := DRTUprobeEvent{
				SessionID:   sessionID,
				ProcessID:   serverProcID, // 触发写侧的是服务端
				TimestampNs: uint64(time.Now().UnixNano()),
				Handle:      handle + 1, // 内部 handle 无论是否对齐
				PID:         serverPID,  // 写入进程 PID
				DataLen:     uint32(len(rspPkt)),
				Data:        rspPkt,
			}
			if err := drv.ProcessEvent(rspEvt, sink); err != nil {
				t.Fatalf("round %d rsp failed: %v", i, err)
			}
		}

		if len(capturedEnvs) != totalRounds*2 {
			t.Fatalf("expected %d captured envelopes, got %d", totalRounds*2, len(capturedEnvs))
		}

		// 验证每对 REQ / RSP 的 ADR-11 投影归一化
		for i := 0; i < totalRounds; i++ {
			reqEnv := capturedEnvs[i*2]
			rspEnv := capturedEnvs[i*2+1]

			sno := uint64(20000 + i + 1)

			// 验证信封头基础属性
			if reqEnv.TransportProto != crosrpc.TransportMesh {
				t.Errorf("round %d: req TransportProto = %d, expected 4 (MESH)", i+1, reqEnv.TransportProto)
			}
			if rspEnv.TransportProto != crosrpc.TransportMesh {
				t.Errorf("round %d: rsp TransportProto = %d, expected 4 (MESH)", i+1, rspEnv.TransportProto)
			}

			// 验证 ADR-11 核心不变量：RSP 投影为与 REQ 完全一致的 ProcessID、Direction 与 ConnSeq
			if reqEnv.ProcessID != clientProcID {
				t.Errorf("round %d: req ProcessID = %d, expected %d", i+1, reqEnv.ProcessID, clientProcID)
			}
			if rspEnv.ProcessID != clientProcID {
				t.Errorf("round %d: rsp ProcessID should be projected to client %d, got %d", i+1, clientProcID, rspEnv.ProcessID)
			}

			if reqEnv.Direction != agentproto.DirectionEgress {
				t.Errorf("round %d: req Direction = %d, expected 2 (EGRESS)", i+1, reqEnv.Direction)
			}
			if rspEnv.Direction != agentproto.DirectionEgress {
				t.Errorf("round %d: rsp Direction = %d, expected 2 (EGRESS, projected)", i+1, rspEnv.Direction)
			}

			if reqEnv.ConnSeq != rspEnv.ConnSeq {
				t.Errorf("round %d: ConnSeq mismatch: req=%d, rsp=%d", i+1, reqEnv.ConnSeq, rspEnv.ConnSeq)
			}
			if reqEnv.SrcPort != rspEnv.SrcPort || reqEnv.DstPort != rspEnv.DstPort {
				t.Errorf("round %d: Port tuple mismatch: req=(%d->%d), rsp=(%d->%d)", i+1, reqEnv.SrcPort, reqEnv.DstPort, rspEnv.SrcPort, rspEnv.DstPort)
			}

			// 验证载荷内容完整性与 SNO
			reqComm, _ := crosrpc.ParseCommHead(capturedPayloads[i*2])
			rspComm, _ := crosrpc.ParseCommHead(capturedPayloads[i*2+1])
			if reqComm.SNO != sno || rspComm.SNO != sno {
				t.Errorf("round %d: SNO mismatch: expected %d, got req=%d, rsp=%d", i+1, sno, reqComm.SNO, rspComm.SNO)
			}
		}

		// 验证一元应答消费完成后，inflightReqs 自动清空
		drv.mu.Lock()
		remainingInflight := len(drv.inflightReqs)
		drv.mu.Unlock()
		if remainingInflight != 0 {
			t.Errorf("expected 0 remaining inflight requests, got %d", remainingInflight)
		}
	})
}

func TestDRTUprobeDriver_ArmedZeroAttach_AndFiveExitCleanup(t *testing.T) {
	drv := NewDRTUprobeDriver(nil, DRTUprobeConfig{
		ProcRoot: "/proc",
	})

	// 1. ARMED 待命期：0 uprobe 挂载
	if drv.ActiveLinksCount() != 0 {
		t.Fatalf("expected 0 active links during ARMED standby, got %d", drv.ActiveLinksCount())
	}

	ctx := context.Background()
	spec := CaptureSpec{
		SessionOrTaskID: 9999,
		RefKind:         agentproto.RefKindRecordingSession,
		Processes: []TargetProcess{
			{ProcessID: 1, PID: 1234},
			{ProcessID: 2, PID: 5678},
		},
	}

	// 2. StartCapture 模拟下发
	if err := drv.StartCapture(ctx, spec); err != nil {
		t.Fatalf("StartCapture failed: %v", err)
	}

	// 3. RemoveTarget 移除单进程监控
	if err := drv.RemoveTarget(9999, 1); err != nil {
		t.Fatalf("RemoveTarget failed: %v", err)
	}

	// 4. StopCapture 停止会话
	if err := drv.StopCapture(ctx, 9999); err != nil {
		t.Fatalf("StopCapture failed: %v", err)
	}
	if drv.ActiveLinksCount() != 0 {
		t.Fatalf("expected 0 active links after StopCapture, got %d", drv.ActiveLinksCount())
	}

	// 5. Close 五出口幂等自洁
	if err := drv.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if err := drv.Close(); err != nil {
		t.Fatalf("Second Close failed: %v", err)
	}
}

func TestParseDRTUprobeEvent_BufferValidation(t *testing.T) {
	// 缓冲区过短
	_, err := ParseDRTUprobeEvent(make([]byte, 50))
	if err == nil {
		t.Fatalf("expected error for buffer < 84 bytes")
	}

	raw := make([]byte, 84+10)
	binary.LittleEndian.PutUint64(raw[0:8], 111)
	binary.LittleEndian.PutUint64(raw[8:16], 222)
	binary.LittleEndian.PutUint64(raw[16:24], 333)
	binary.LittleEndian.PutUint64(raw[24:32], 444)
	binary.LittleEndian.PutUint32(raw[32:36], 555)
	binary.LittleEndian.PutUint32(raw[36:40], 10)
	raw[40] = 1 // isTruncated
	copy(raw[84:], []byte("0123456789"))

	evt, err := ParseDRTUprobeEvent(raw)
	if err != nil {
		t.Fatalf("ParseDRTUprobeEvent failed: %v", err)
	}
	if evt.SessionID != 111 || evt.ProcessID != 222 || evt.PID != 555 || !evt.IsTruncated {
		t.Errorf("parsed event mismatch: %+v", evt)
	}
	if string(evt.Data) != "0123456789" {
		t.Errorf("parsed data mismatch: %q", string(evt.Data))
	}
}
