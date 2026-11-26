package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"

	"github.com/cilium/ebpf/link"
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
	// 缓冲区过短 (< 96B)
	_, err := ParseDRTUprobeEvent(make([]byte, 84))
	if err == nil {
		t.Fatalf("expected error for buffer < 96 bytes")
	}

	raw := make([]byte, DRTUprobeEventHeaderLen+10)
	binary.LittleEndian.PutUint64(raw[0:8], 111)
	binary.LittleEndian.PutUint64(raw[8:16], 222)
	binary.LittleEndian.PutUint64(raw[16:24], 333)
	binary.LittleEndian.PutUint64(raw[24:32], 444)
	binary.LittleEndian.PutUint32(raw[32:36], 555)
	binary.LittleEndian.PutUint32(raw[36:40], 10)
	raw[40] = 1 // isTruncated
	raw[41] = 2 // chunkIdx
	raw[42] = 4 // chunkCount
	binary.LittleEndian.PutUint32(raw[84:88], 131072)
	binary.LittleEndian.PutUint32(raw[92:96], 556)
	copy(raw[DRTUprobeEventHeaderLen:], []byte("0123456789"))

	evt, err := ParseDRTUprobeEvent(raw)
	if err != nil {
		t.Fatalf("ParseDRTUprobeEvent failed: %v", err)
	}
	if evt.SessionID != 111 || evt.ProcessID != 222 || evt.PID != 555 || !evt.IsTruncated ||
		evt.ChunkIdx != 2 || evt.ChunkCount != 4 || evt.OrigLen != 131072 || evt.TID != 556 {
		t.Errorf("parsed event mismatch: %+v", evt)
	}
	if string(evt.Data) != "0123456789" {
		t.Errorf("parsed data mismatch: %q", string(evt.Data))
	}
}

type mockClosableLink struct {
	closed atomic.Int32
}

func (m *mockClosableLink) Close() error {
	m.closed.Add(1)
	return nil
}

func TestMultiPIDCapture_TCPAndUprobe_FiveExitCleanup(t *testing.T) {
	// 1. 验证 agentproto.ProcessMonitorSpec TargetPIDs JSON 向后兼容与 ComputeArmedDigest 稳定
	specWithPIDs := agentproto.ProcessMonitorSpec{
		ProcessID:  10,
		ListenPort: 20010,
		CmdPattern: "worker",
		TargetPIDs: []uint32{2001, 2002},
	}
	specWithoutPIDs := agentproto.ProcessMonitorSpec{
		ProcessID:  10,
		ListenPort: 20010,
		CmdPattern: "worker",
	}
	rawJSON, err := json.Marshal(specWithPIDs)
	if err != nil {
		t.Fatalf("Marshal ProcessMonitorSpec failed: %v", err)
	}
	var decoded agentproto.ProcessMonitorSpec
	if err := json.Unmarshal(rawJSON, &decoded); err != nil {
		t.Fatalf("Unmarshal ProcessMonitorSpec failed: %v", err)
	}
	if len(decoded.TargetPIDs) != 2 || decoded.TargetPIDs[0] != 2001 || decoded.TargetPIDs[1] != 2002 {
		t.Fatalf("TargetPIDs roundtrip mismatch: %+v", decoded.TargetPIDs)
	}
	if agentproto.ComputeArmedDigest([]agentproto.ProcessMonitorSpec{specWithPIDs}) !=
		agentproto.ComputeArmedDigest([]agentproto.ProcessMonitorSpec{specWithoutPIDs}) {
		t.Fatalf("ComputeArmedDigest should remain backward compatible when TargetPIDs is set")
	}

	// 2. 验证 TargetProcess.EffectivePIDs() 去重与单 PID 回退
	tpMulti := TargetProcess{ProcessID: 10, PID: 2001, PIDs: []uint32{2001, 2002, 2001, 0}}
	effMulti := tpMulti.EffectivePIDs()
	if len(effMulti) != 2 || effMulti[0] != 2001 || effMulti[1] != 2002 {
		t.Fatalf("EffectivePIDs dedup mismatch: %v", effMulti)
	}
	tpFallback := TargetProcess{ProcessID: 11, PID: 3001}
	effFallback := tpFallback.EffectivePIDs()
	if len(effFallback) != 1 || effFallback[0] != 3001 {
		t.Fatalf("EffectivePIDs fallback mismatch: %v", effFallback)
	}

	// 3. 验证 TCPDriver 多 PID 登记及 RemoveTarget / StopCapture / Close 全量清理
	tcpDrv := NewTCPDriver(nil)
	ctx := context.Background()
	if err := tcpDrv.StartCapture(ctx, CaptureSpec{
		SessionOrTaskID: 5001,
		RefKind:         agentproto.RefKindRecordingSession,
		Processes: []TargetProcess{
			{ProcessID: 10, PID: 2001, PIDs: []uint32{2001, 2002}},
			{ProcessID: 11, PID: 3001},
		},
	}); err != nil {
		t.Fatalf("TCPDriver StartCapture failed: %v", err)
	}
	if tcpDrv.ActivePIDsCount() != 3 {
		t.Fatalf("expected 3 active TCP PIDs, got %d", tcpDrv.ActivePIDsCount())
	}
	if err := tcpDrv.RemoveTarget(5001, 10); err != nil {
		t.Fatalf("TCPDriver RemoveTarget failed: %v", err)
	}
	if tcpDrv.ActivePIDsCount() != 1 {
		t.Fatalf("expected 1 active TCP PID after RemoveTarget, got %d", tcpDrv.ActivePIDsCount())
	}
	if err := tcpDrv.StopCapture(ctx, 5001); err != nil {
		t.Fatalf("TCPDriver StopCapture failed: %v", err)
	}
	if tcpDrv.ActivePIDsCount() != 0 {
		t.Fatalf("expected 0 active TCP PIDs after StopCapture, got %d", tcpDrv.ActivePIDsCount())
	}
	if err := tcpDrv.StartCapture(ctx, CaptureSpec{
		SessionOrTaskID: 5002,
		Processes:       []TargetProcess{{ProcessID: 10, PIDs: []uint32{4001, 4002}}},
	}); err != nil {
		t.Fatalf("TCPDriver second StartCapture failed: %v", err)
	}
	if err := tcpDrv.Close(); err != nil {
		t.Fatalf("TCPDriver Close failed: %v", err)
	}
	if tcpDrv.ActivePIDsCount() != 0 {
		t.Fatalf("expected 0 active TCP PIDs after Close, got %d", tcpDrv.ActivePIDsCount())
	}

	// 4. 验证 DRTUprobeDriver 对同进程多 PID 分别挂载独立 bpf_link，并在五出口全量幂等清理
	var createdLinks []*mockClosableLink
	var attachedPIDs []uint32
	uprobeDrv := NewDRTUprobeDriver(nil, DRTUprobeConfig{
		ProcRoot: "/proc",
		AttachUprobeFunc: func(procRoot string, pid uint32) (io.Closer, error) {
			attachedPIDs = append(attachedPIDs, pid)
			lnk := &mockClosableLink{}
			createdLinks = append(createdLinks, lnk)
			return lnk, nil
		},
	})
	if uprobeDrv.ActiveLinksCount() != 0 {
		t.Fatalf("ARMED standby must have 0 links, got %d", uprobeDrv.ActiveLinksCount())
	}

	if err := uprobeDrv.StartCapture(ctx, CaptureSpec{
		SessionOrTaskID: 6001,
		RefKind:         agentproto.RefKindRecordingSession,
		Processes: []TargetProcess{
			{ProcessID: 20, PID: 7001, PIDs: []uint32{7001, 7002}},
			{ProcessID: 21, PID: 8001},
		},
	}); err != nil {
		t.Fatalf("DRTUprobeDriver StartCapture failed: %v", err)
	}
	if uprobeDrv.ActiveLinksCount() != 3 || len(attachedPIDs) != 3 {
		t.Fatalf("expected 3 per-PID uprobe links, active=%d attached=%v", uprobeDrv.ActiveLinksCount(), attachedPIDs)
	}

	// 验证 CapturedPID 在多实例事件中精确保留
	var envs []agentproto.EnvelopeHeader
	sink := FrameSinkFunc(func(h agentproto.EnvelopeHeader, payload []byte) error {
		envs = append(envs, h)
		return nil
	})
	reqPkt := buildMockCrosPacket(crosrpc.TransportMesh, 9101, crosrpc.MsgTypeReq, []byte("req"))
	if err := uprobeDrv.ProcessEvent(DRTUprobeEvent{
		SessionID: 6001, ProcessID: 20, PID: 7002, Handle: 11, DataLen: uint32(len(reqPkt)), Data: reqPkt,
	}, sink); err != nil {
		t.Fatalf("ProcessEvent req failed: %v", err)
	}
	if len(envs) != 1 || envs[0].CapturedPID != 7002 {
		t.Fatalf("expected CapturedPID=7002, got %+v", envs)
	}

	// RemoveTarget 移除进程 20 应同时卸载 7001 与 7002 的 2 个 link
	if err := uprobeDrv.RemoveTarget(6001, 20); err != nil {
		t.Fatalf("DRTUprobeDriver RemoveTarget failed: %v", err)
	}
	if uprobeDrv.ActiveLinksCount() != 1 {
		t.Fatalf("expected 1 link remaining after RemoveTarget, got %d", uprobeDrv.ActiveLinksCount())
	}
	if createdLinks[0].closed.Load() != 1 || createdLinks[1].closed.Load() != 1 || createdLinks[2].closed.Load() != 0 {
		t.Fatalf("unexpected link close counts after RemoveTarget: %d,%d,%d",
			createdLinks[0].closed.Load(), createdLinks[1].closed.Load(), createdLinks[2].closed.Load())
	}

	// StopCapture 卸载剩余 link
	if err := uprobeDrv.StopCapture(ctx, 6001); err != nil {
		t.Fatalf("DRTUprobeDriver StopCapture failed: %v", err)
	}
	if uprobeDrv.ActiveLinksCount() != 0 || createdLinks[2].closed.Load() != 1 {
		t.Fatalf("expected 0 links after StopCapture, got %d (closed=%d)", uprobeDrv.ActiveLinksCount(), createdLinks[2].closed.Load())
	}

	// 重新挂载并验证 Close 幂等全清
	if err := uprobeDrv.StartCapture(ctx, CaptureSpec{
		SessionOrTaskID: 6002,
		Processes:       []TargetProcess{{ProcessID: 22, PIDs: []uint32{9001, 9002}}},
	}); err != nil {
		t.Fatalf("DRTUprobeDriver StartCapture 6002 failed: %v", err)
	}
	if uprobeDrv.ActiveLinksCount() != 2 {
		t.Fatalf("expected 2 links for session 6002, got %d", uprobeDrv.ActiveLinksCount())
	}
	if err := uprobeDrv.Close(); err != nil {
		t.Fatalf("DRTUprobeDriver Close failed: %v", err)
	}
	if uprobeDrv.ActiveLinksCount() != 0 || createdLinks[3].closed.Load() != 1 || createdLinks[4].closed.Load() != 1 {
		t.Fatalf("expected all links closed on Close, active=%d", uprobeDrv.ActiveLinksCount())
	}
	if err := uprobeDrv.Close(); err != nil {
		t.Fatalf("DRTUprobeDriver second Close failed: %v", err)
	}
}

func TestDRTUprobeDriver_StatsAndVerboseNoPayload(t *testing.T) {
	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origOut)

	drv := NewDRTUprobeDriver(nil, DRTUprobeConfig{
		ProcRoot: "/proc",
		Verbose:  true,
	})

	sink := FrameSinkFunc(func(h agentproto.EnvelopeHeader, payload []byte) error {
		return nil
	})

	secretReq := "SUPER_SECRET_REQ_PAYLOAD_XYZ"
	secretRsp := "SUPER_SECRET_RSP_PAYLOAD_ABC"

	// 1. REQ
	reqPkt := buildMockCrosPacket(crosrpc.TransportMesh, 7001, crosrpc.MsgTypeReq, []byte(secretReq))
	if err := drv.ProcessEvent(DRTUprobeEvent{
		SessionID: 1, ProcessID: 10, PID: 1001, Handle: 0x11, DataLen: uint32(len(reqPkt)), Data: reqPkt,
	}, sink); err != nil {
		t.Fatalf("ProcessEvent req failed: %v", err)
	}

	// 2. RSP 配对 (含截断标记)
	rspPkt := buildMockCrosPacket(crosrpc.TransportMesh, 7001, crosrpc.MsgTypeRsp, []byte(secretRsp))
	if err := drv.ProcessEvent(DRTUprobeEvent{
		SessionID: 1, ProcessID: 11, PID: 1002, Handle: 0x22, DataLen: uint32(len(rspPkt)), IsTruncated: true, Data: rspPkt,
	}, sink); err != nil {
		t.Fatalf("ProcessEvent rsp paired failed: %v", err)
	}

	// 3. RSP 孤立
	orphanPkt := buildMockCrosPacket(crosrpc.TransportMesh, 7002, crosrpc.MsgTypeRsp, []byte(secretRsp))
	if err := drv.ProcessEvent(DRTUprobeEvent{
		SessionID: 1, ProcessID: 11, PID: 1002, Handle: 0x22, DataLen: uint32(len(orphanPkt)), Data: orphanPkt,
	}, sink); err != nil {
		t.Fatalf("ProcessEvent rsp orphan failed: %v", err)
	}

	// 4. 非法 Magic 拒绝
	badPkt := make([]byte, 32)
	copy(badPkt, []byte("BAD.HDR_0123456789ABCDEF"))
	if err := drv.ProcessEvent(DRTUprobeEvent{
		SessionID: 1, ProcessID: 10, PID: 1001, Handle: 0x11, DataLen: uint32(len(badPkt)), Data: badPkt,
	}, sink); err == nil {
		t.Fatalf("expected error for invalid magic packet")
	}

	// 5. 缓冲区过短拒绝
	if err := drv.ProcessEvent(DRTUprobeEvent{
		SessionID: 1, ProcessID: 10, PID: 1001, Handle: 0x11, DataLen: 8, Data: []byte("short"),
	}, sink); err == nil {
		t.Fatalf("expected error for short buffer")
	}

	st := drv.Stats()
	if st.RawEvents != 5 || st.ValidFrames != 3 || st.ReqFrames != 1 || st.RspPaired != 1 || st.RspOrphan != 1 || st.TruncatedFrames != 1 || st.DroppedEvents != 2 {
		t.Fatalf("unexpected DRTUprobeStats: %+v", st)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "[DRTUprobe][FirstFrame]") || !strings.Contains(logs, "[DRTUprobe][Truncated]") || !strings.Contains(logs, "[DRTUprobe][Drop]") {
		t.Fatalf("expected verbose first-frame/truncated/drop logs, got:\n%s", logs)
	}
	if strings.Contains(logs, secretReq) || strings.Contains(logs, secretRsp) {
		t.Fatalf("verbose logs must NEVER contain business payload, got:\n%s", logs)
	}
}

type fakeCloser struct {
	closed atomic.Bool
}

func (f *fakeCloser) Close() error {
	f.closed.Store(true)
	return nil
}

func TestDRTUprobeDriver_SyncProcessPIDs_DynamicReattachAndStandbyGuard(t *testing.T) {
	closers := make(map[uint32]*fakeCloser)
	drv := NewDRTUprobeDriver(nil, DRTUprobeConfig{
		AttachUprobeFunc: func(_ string, pid uint32) (io.Closer, error) {
			fc := &fakeCloser{}
			closers[pid] = fc
			return fc, nil
		},
	})

	// 1. 待命期（Session 未在 StartCapture 激活）严禁挂载任何 uprobe
	if err := drv.SyncProcessPIDs(99, 10, []uint32{1001}); err != nil {
		t.Fatalf("SyncProcessPIDs in standby failed: %v", err)
	}
	if drv.ActiveLinksCount() != 0 {
		t.Fatalf("expected 0 active links when session is not capturing, got %d", drv.ActiveLinksCount())
	}

	// 2. 开启录制挂载初始 PID [1001, 1002]
	if err := drv.StartCapture(context.Background(), CaptureSpec{
		SessionOrTaskID: 99,
		Processes: []TargetProcess{
			{ProcessID: 10, PID: 1001, PIDs: []uint32{1001, 1002}},
		},
	}); err != nil {
		t.Fatalf("StartCapture failed: %v", err)
	}
	if drv.ActiveLinksCount() != 2 {
		t.Fatalf("expected 2 active links, got %d", drv.ActiveLinksCount())
	}

	// 3. 进程 1002 退出、新进程 1003 拉起 -> 摘除 1002，保留 1001，补挂 1003
	if err := drv.SyncProcessPIDs(99, 10, []uint32{1001, 1003}); err != nil {
		t.Fatalf("SyncProcessPIDs during capture failed: %v", err)
	}
	if drv.ActiveLinksCount() != 2 {
		t.Fatalf("expected 2 active links after sync, got %d", drv.ActiveLinksCount())
	}
	if closers[1001].closed.Load() {
		t.Fatalf("expected PID 1001 link to stay open")
	}
	if !closers[1002].closed.Load() {
		t.Fatalf("expected dead PID 1002 link to be closed")
	}
	if closers[1003] == nil || closers[1003].closed.Load() {
		t.Fatalf("expected new PID 1003 link to be attached and open")
	}

	// 4. 停录卸载全部链接
	if err := drv.StopCapture(context.Background(), 99); err != nil {
		t.Fatalf("StopCapture failed: %v", err)
	}
	if drv.ActiveLinksCount() != 0 {
		t.Fatalf("expected 0 active links after StopCapture, got %d", drv.ActiveLinksCount())
	}
}

func TestBuildDRTUprobeOptions_UsesAbsoluteAddressNotRelativeOffset(t *testing.T) {
	opts := buildDRTUprobeOptions(0x219be0, 27250)
	if opts == nil {
		t.Fatalf("expected non-nil UprobeOptions")
	}
	if opts.Address != 0x219be0 {
		t.Fatalf("expected Address=0x219be0, got 0x%x", opts.Address)
	}
	if opts.Offset != 0 {
		t.Fatalf("expected Offset=0 (relative offset must not carry absolute file offset), got %d", opts.Offset)
	}
	if opts.PID != 27250 {
		t.Fatalf("expected PID=27250, got %d", opts.PID)
	}
}

func TestDRTUprobeDriver_AttachPID_RealELFFixtureUsesAddressNotOffset(t *testing.T) {
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang not available on host, skipping ELF fixture attach test")
	}

	tmpDir := t.TempDir()
	cSource := filepath.Join(tmpDir, "drt_fixture.c")
	soPath := filepath.Join(tmpDir, "libdopra.so")
	cCode := `
__attribute__((visibility("default"))) int DRT_WriteBulk(void* handle, int count, void* entries) {
    return 42;
}
`
	if err := os.WriteFile(cSource, []byte(cCode), 0644); err != nil {
		t.Fatalf("write c source failed: %v", err)
	}
	if out, err := exec.Command(clang, "-shared", "-fPIC", "-O2", "-o", soPath, cSource).CombinedOutput(); err != nil {
		t.Fatalf("clang build shared library failed: %v, out: %s", err, string(out))
	}
	if strip, sErr := exec.LookPath("llvm-strip"); sErr == nil {
		_ = exec.Command(strip, "--strip-all", soPath).Run()
	}

	expectedOffsets, err := ResolveDRTSymbolOffsets(soPath)
	if err != nil || expectedOffsets.WriteBulkOffset == 0 {
		t.Fatalf("ResolveDRTSymbolOffsets failed: %v (offsets=%+v)", err, expectedOffsets)
	}

	pid := uint32(27250)
	procRoot := filepath.Join(tmpDir, "proc")
	mapsDir := filepath.Join(procRoot, fmt.Sprintf("%d", pid))
	if err := os.MkdirAll(mapsDir, 0755); err != nil {
		t.Fatalf("mkdir maps dir failed: %v", err)
	}
	mapsContent := fmt.Sprintf("7f3b89350000-7f3b89400000 r-xp 00000000 08:01 3001 %s\n", soPath)
	if err := os.WriteFile(filepath.Join(mapsDir, "maps"), []byte(mapsContent), 0644); err != nil {
		t.Fatalf("write maps failed: %v", err)
	}

	var calls int
	var gotSymbol string
	var gotOpts link.UprobeOptions
	createdLink := &mockClosableLink{}

	drv := NewDRTUprobeDriver(nil, DRTUprobeConfig{
		ProcRoot: procRoot,
	})
	drv.attachExecUprobe = func(ex *link.Executable, symbol string, opts *link.UprobeOptions) (io.Closer, error) {
		calls++
		gotSymbol = symbol
		if opts != nil {
			gotOpts = *opts
		}
		return createdLink, nil
	}

	ctx := context.Background()
	if err := drv.StartCapture(ctx, CaptureSpec{
		SessionOrTaskID: 3001,
		RefKind:         agentproto.RefKindRecordingSession,
		Processes: []TargetProcess{
			{ProcessID: 10, PID: pid},
		},
	}); err != nil {
		t.Fatalf("StartCapture failed: %v", err)
	}

	if calls != 1 {
		t.Fatalf("expected attachExecUprobe to be called once, got %d", calls)
	}
	if gotSymbol != drtWriteBulkSymbol {
		t.Fatalf("expected symbol %q, got %q", drtWriteBulkSymbol, gotSymbol)
	}
	if gotOpts.Address != expectedOffsets.WriteBulkOffset || gotOpts.Address == 0 {
		t.Fatalf("expected Address=0x%x, got 0x%x", expectedOffsets.WriteBulkOffset, gotOpts.Address)
	}
	if gotOpts.Offset != 0 {
		t.Fatalf("expected Offset=0, got %d", gotOpts.Offset)
	}
	if gotOpts.PID != int(pid) {
		t.Fatalf("expected PID=%d, got %d", pid, gotOpts.PID)
	}
	if drv.ActiveLinksCount() != 1 {
		t.Fatalf("expected ActiveLinksCount=1, got %d", drv.ActiveLinksCount())
	}

	if err := drv.StopCapture(ctx, 3001); err != nil {
		t.Fatalf("StopCapture failed: %v", err)
	}
	if drv.ActiveLinksCount() != 0 {
		t.Fatalf("expected ActiveLinksCount=0 after StopCapture, got %d", drv.ActiveLinksCount())
	}
	if createdLink.closed.Load() != 1 {
		t.Fatalf("expected created link to be closed once, got %d", createdLink.closed.Load())
	}
}

func TestDRTUprobeDriver_AttachFailureLogDedupAndCleanup(t *testing.T) {
	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origOut)

	tmpDir := t.TempDir()
	pid := uint32(4001)
	procRoot := filepath.Join(tmpDir, "proc")
	mapsDir := filepath.Join(procRoot, fmt.Sprintf("%d", pid))
	if err := os.MkdirAll(mapsDir, 0755); err != nil {
		t.Fatalf("mkdir maps dir failed: %v", err)
	}
	// Intentionally omit libdopra from maps to trigger FindMappedLibraryPath failure
	if err := os.WriteFile(filepath.Join(mapsDir, "maps"), []byte("55a6d3620000-55a6d3650000 r-xp 00000000 08:01 1 /bin/bash\n"), 0644); err != nil {
		t.Fatalf("write maps failed: %v", err)
	}

	drv := NewDRTUprobeDriver(nil, DRTUprobeConfig{
		ProcRoot: procRoot,
	})
	drv.attachExecUprobe = func(ex *link.Executable, symbol string, opts *link.UprobeOptions) (io.Closer, error) {
		return &mockClosableLink{}, nil
	}

	ctx := context.Background()
	if err := drv.StartCapture(ctx, CaptureSpec{
		SessionOrTaskID: 5001,
		Processes:       []TargetProcess{{ProcessID: 20, PID: pid}},
	}); err != nil {
		t.Fatalf("StartCapture failed: %v", err)
	}

	// Repeated SyncProcessPIDs with the same failure must deduplicate logs
	_ = drv.SyncProcessPIDs(5001, 20, []uint32{pid})
	_ = drv.SyncProcessPIDs(5001, 20, []uint32{pid})

	skipNeedle := "未找到 libdopra 映射"
	if got := strings.Count(logBuf.String(), skipNeedle); got != 1 {
		t.Fatalf("expected 1 deduplicated skip log, got %d:\n%s", got, logBuf.String())
	}

	// PID exits -> cache cleared -> reappearing PID logs once again
	_ = drv.SyncProcessPIDs(5001, 20, nil)
	_ = drv.SyncProcessPIDs(5001, 20, []uint32{pid})
	if got := strings.Count(logBuf.String(), skipNeedle); got != 2 {
		t.Fatalf("expected 2 skip logs after PID exit cleared cache, got %d:\n%s", got, logBuf.String())
	}

	if err := drv.StopCapture(ctx, 5001); err != nil {
		t.Fatalf("StopCapture failed: %v", err)
	}
	drv.mu.Lock()
	errCacheLen := len(drv.lastAttachErr)
	drv.mu.Unlock()
	if errCacheLen != 0 {
		t.Fatalf("expected lastAttachErr cleared after StopCapture, got %d", errCacheLen)
	}
}

func buildFullCrosReqPacket(proto uint8, sno uint64, reqType uint8, svc, op string, body []byte) []byte {
	headLen := crosrpc.ReqHeadMinLen
	sessionHead := make([]byte, headLen)
	binary.BigEndian.PutUint16(sessionHead[0:2], 1)
	binary.BigEndian.PutUint16(sessionHead[2:4], uint16(headLen))
	copy(sessionHead[8:72], []byte(svc))
	copy(sessionHead[72:136], []byte(op))
	sessionHead[136] = reqType

	comm := crosrpc.CommHead{
		TransportProto: proto,
		SNO:            sno,
		Length:         uint32(headLen + len(body)),
		HeadLen:        uint16(headLen),
		MsgType:        crosrpc.MsgTypeReq,
		IsPacked:       1,
	}
	copy(comm.Magic[:], []byte(crosrpc.MagicPrefix))

	out := make([]byte, 0, crosrpc.CommHeadLen+headLen+len(body))
	out = append(out, comm.Marshal()...)
	out = append(out, sessionHead...)
	out = append(out, body...)
	return out
}

func buildFullCrosRspPacket(proto uint8, sno uint64, svc, op string, body []byte) []byte {
	headLen := crosrpc.RspHeadMinLen
	rspHead := make([]byte, headLen)
	binary.BigEndian.PutUint16(rspHead[0:2], 1)
	binary.BigEndian.PutUint16(rspHead[2:4], uint16(headLen))
	copy(rspHead[24:88], []byte(svc))
	copy(rspHead[88:152], []byte(op))

	comm := crosrpc.CommHead{
		TransportProto: proto,
		SNO:            sno,
		Length:         uint32(headLen + len(body)),
		HeadLen:        uint16(headLen),
		MsgType:        crosrpc.MsgTypeRsp,
		IsPacked:       1,
	}
	copy(comm.Magic[:], []byte(crosrpc.MagicPrefix))

	out := make([]byte, 0, crosrpc.CommHeadLen+headLen+len(body))
	out = append(out, comm.Marshal()...)
	out = append(out, rspHead...)
	out = append(out, body...)
	return out
}

func TestDRTUprobeDriver_LargePayloadChunkReassembly_32K_124K_256K_AndTruncation(t *testing.T) {
	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origOut)

	drv := NewDRTUprobeDriver(nil, DRTUprobeConfig{ProcRoot: "/proc"})

	var capturedEnvs []agentproto.EnvelopeHeader
	var capturedPayloads [][]byte
	sink := FrameSinkFunc(func(h agentproto.EnvelopeHeader, payload []byte) error {
		capturedEnvs = append(capturedEnvs, h)
		cp := make([]byte, len(payload))
		copy(cp, payload)
		capturedPayloads = append(capturedPayloads, cp)
		return nil
	})

	makePatternBody := func(size int, seed byte) []byte {
		b := make([]byte, size)
		for i := range b {
			b[i] = byte((int(seed) + i) % 251)
		}
		return b
	}

	emitInChunks := func(sessionID, procID, ts, handle uint64, pid, tid uint32, full []byte, origLen uint32, isTrunc bool) {
		chunkCount := (len(full) + MaxDRTChunkSize - 1) / MaxDRTChunkSize
		for i := 0; i < chunkCount; i++ {
			start := i * MaxDRTChunkSize
			end := start + MaxDRTChunkSize
			if end > len(full) {
				end = len(full)
			}
			evt := DRTUprobeEvent{
				SessionID:   sessionID,
				ProcessID:   procID,
				TimestampNs: ts,
				Handle:      handle,
				PID:         pid,
				TID:         tid,
				DataLen:     uint32(end - start),
				OrigLen:     origLen,
				IsTruncated: isTrunc,
				ChunkIdx:    uint8(i),
				ChunkCount:  uint8(chunkCount),
				Data:        full[start:end],
			}
			if err := drv.ProcessEvent(evt, sink); err != nil {
				t.Fatalf("ProcessEvent chunk %d/%d failed: %v", i, chunkCount, err)
			}
		}
	}

	// 1. 32KB 单分片快路径
	pkt32K := buildFullCrosReqPacket(crosrpc.TransportMesh, 1001, crosrpc.ReqTypeSync, "svc.A", "Op32K", makePatternBody(32*1024-crosrpc.CommHeadLen-crosrpc.ReqHeadMinLen, 1))
	emitInChunks(1, 10, 1000, 0x10, 2001, 2001, pkt32K, uint32(len(pkt32K)), false)

	// 2. 124KB (4 chunks) 与 256KB (8 chunks) 交错到达 (来自不同 TID)
	pkt124K := buildFullCrosReqPacket(crosrpc.TransportMesh, 1002, crosrpc.ReqTypeSync, "svc.B", "Op124K", makePatternBody(124*1024-crosrpc.CommHeadLen-crosrpc.ReqHeadMinLen, 7))
	pkt256K := buildFullCrosReqPacket(crosrpc.TransportMesh, 1003, crosrpc.ReqTypeSync, "svc.C", "Op256K", makePatternBody(256*1024-crosrpc.CommHeadLen-crosrpc.ReqHeadMinLen, 13))

	// 先发 pkt124K 的前 2 片，再发 pkt256K 的全部 8 片，最后发 pkt124K 的后 2 片
	for i := 0; i < 2; i++ {
		start, end := i*MaxDRTChunkSize, (i+1)*MaxDRTChunkSize
		_ = drv.ProcessEvent(DRTUprobeEvent{
			SessionID: 1, ProcessID: 10, TimestampNs: 2000, Handle: 0x20, PID: 2001, TID: 2002,
			DataLen: uint32(end - start), OrigLen: uint32(len(pkt124K)), ChunkIdx: uint8(i), ChunkCount: 4, Data: pkt124K[start:end],
		}, sink)
	}
	emitInChunks(1, 11, 3000, 0x30, 2002, 3001, pkt256K, uint32(len(pkt256K)), false)
	for i := 2; i < 4; i++ {
		start := i * MaxDRTChunkSize
		end := start + MaxDRTChunkSize
		if end > len(pkt124K) {
			end = len(pkt124K)
		}
		_ = drv.ProcessEvent(DRTUprobeEvent{
			SessionID: 1, ProcessID: 10, TimestampNs: 2000, Handle: 0x20, PID: 2001, TID: 2002,
			DataLen: uint32(end - start), OrigLen: uint32(len(pkt124K)), ChunkIdx: uint8(i), ChunkCount: 4, Data: pkt124K[start:end],
		}, sink)
	}

	// 3. >256KB (300KB) 截断并上报 origLen=307200
	pkt300K := buildFullCrosReqPacket(crosrpc.TransportMesh, 1004, crosrpc.ReqTypeSync, "svc.D", "Op300K", makePatternBody(300*1024-crosrpc.CommHeadLen-crosrpc.ReqHeadMinLen, 29))
	emitInChunks(1, 12, 4000, 0x40, 2003, 4001, pkt300K[:MaxDRTTotalSize], uint32(len(pkt300K)), true)

	if len(capturedEnvs) != 4 {
		t.Fatalf("expected 4 reassembled frames, got %d", len(capturedEnvs))
	}
	if capturedEnvs[0].IsTruncated() || !bytes.Equal(capturedPayloads[0], pkt32K) {
		t.Errorf("32KB frame mismatch or unexpectedly truncated")
	}
	if capturedEnvs[1].IsTruncated() || !bytes.Equal(capturedPayloads[1], pkt256K) {
		t.Errorf("256KB frame mismatch or unexpectedly truncated")
	}
	if capturedEnvs[2].IsTruncated() || !bytes.Equal(capturedPayloads[2], pkt124K) {
		t.Errorf("124KB interleaved frame mismatch or unexpectedly truncated")
	}
	if !capturedEnvs[3].IsTruncated() || len(capturedPayloads[3]) != MaxDRTTotalSize {
		t.Errorf("300KB frame expected truncated at %d, got trunc=%v len=%d", MaxDRTTotalSize, capturedEnvs[3].IsTruncated(), len(capturedPayloads[3]))
	}

	st := drv.Stats()
	if st.RawEvents != 4 || st.ValidFrames != 4 || st.TruncatedFrames != 1 {
		t.Fatalf("expected RawEvents=4 ValidFrames=4 TruncatedFrames=1, got %+v", st)
	}
	if !strings.Contains(logBuf.String(), fmt.Sprintf("origLen=%d", len(pkt300K))) {
		t.Fatalf("expected truncation log to include origLen=%d, got:\n%s", len(pkt300K), logBuf.String())
	}
}

func TestDRTUprobeDriver_MultiProcessSNOCollisionAndOneway(t *testing.T) {
	drv := NewDRTUprobeDriver(nil, DRTUprobeConfig{ProcRoot: "/proc"})

	var envs []agentproto.EnvelopeHeader
	sink := FrameSinkFunc(func(h agentproto.EnvelopeHeader, payload []byte) error {
		envs = append(envs, h)
		return nil
	})

	// 两个独立进程均使用相同的 SNO=1 调用不同服务
	reqA := buildFullCrosReqPacket(crosrpc.TransportMesh, 1, crosrpc.ReqTypeSync, "svc.Alpha", "DoA", []byte("a"))
	reqB := buildFullCrosReqPacket(crosrpc.TransportMesh, 1, crosrpc.ReqTypeSync, "svc.Beta", "DoB", []byte("b"))
	rspB := buildFullCrosRspPacket(crosrpc.TransportMesh, 1, "svc.Beta", "DoB", []byte("rb"))
	rspA := buildFullCrosRspPacket(crosrpc.TransportMesh, 1, "svc.Alpha", "DoA", []byte("ra"))
	oneway := buildFullCrosReqPacket(crosrpc.TransportMesh, 99, crosrpc.ReqTypeOneway, "svc.Notify", "Push", []byte("n"))

	_ = drv.ProcessEvent(DRTUprobeEvent{SessionID: 1, ProcessID: 101, PID: 1001, Handle: 0x1, DataLen: uint32(len(reqA)), Data: reqA}, sink)
	_ = drv.ProcessEvent(DRTUprobeEvent{SessionID: 1, ProcessID: 202, PID: 2001, Handle: 0x2, DataLen: uint32(len(reqB)), Data: reqB}, sink)
	_ = drv.ProcessEvent(DRTUprobeEvent{SessionID: 1, ProcessID: 303, PID: 3001, Handle: 0x3, DataLen: uint32(len(rspB)), Data: rspB}, sink)
	_ = drv.ProcessEvent(DRTUprobeEvent{SessionID: 1, ProcessID: 404, PID: 4001, Handle: 0x4, DataLen: uint32(len(rspA)), Data: rspA}, sink)
	_ = drv.ProcessEvent(DRTUprobeEvent{SessionID: 1, ProcessID: 505, PID: 5001, Handle: 0x5, DataLen: uint32(len(oneway)), Data: oneway}, sink)

	if len(envs) != 5 {
		t.Fatalf("expected 5 emitted frames, got %d", len(envs))
	}
	// rspB 必须投影回 ProcessID=202, PID=2001
	if envs[2].ProcessID != 202 || envs[2].CapturedPID != 2001 {
		t.Errorf("rspB mis-projected: got ProcessID=%d PID=%d, want 202/2001", envs[2].ProcessID, envs[2].CapturedPID)
	}
	// rspA 必须投影回 ProcessID=101, PID=1001
	if envs[3].ProcessID != 101 || envs[3].CapturedPID != 1001 {
		t.Errorf("rspA mis-projected: got ProcessID=%d PID=%d, want 101/1001", envs[3].ProcessID, envs[3].CapturedPID)
	}

	st := drv.Stats()
	if st.RspPaired != 2 || st.RspOrphan != 0 {
		t.Fatalf("expected RspPaired=2 RspOrphan=0, got %+v", st)
	}

	drv.mu.Lock()
	remaining := drv.inflightCount
	drv.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected 0 remaining inflight requests (oneway must not leak), got %d", remaining)
	}
}

func TestDRTUprobeDriver_ResetAllowsReuseAndLogsDetach(t *testing.T) {
	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origOut)

	drv := NewDRTUprobeDriver(nil, DRTUprobeConfig{
		ProcRoot: "/proc",
		AttachUprobeFunc: func(_ string, pid uint32) (io.Closer, error) {
			return &mockClosableLink{}, nil
		},
	})

	ctx := context.Background()
	if err := drv.StartCapture(ctx, CaptureSpec{
		SessionOrTaskID: 77,
		Processes:       []TargetProcess{{ProcessID: 10, PIDs: []uint32{101, 102}}},
	}); err != nil {
		t.Fatalf("StartCapture failed: %v", err)
	}

	// SyncProcessPIDs 摘除 102
	_ = drv.SyncProcessPIDs(77, 10, []uint32{101})
	// RemoveTarget 摘除 101
	_ = drv.RemoveTarget(77, 10)

	// Reset 后仍可再次 StartCapture
	if err := drv.Reset(); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}
	if err := drv.StartCapture(ctx, CaptureSpec{
		SessionOrTaskID: 78,
		Processes:       []TargetProcess{{ProcessID: 10, PIDs: []uint32{103}}},
	}); err != nil {
		t.Fatalf("StartCapture after Reset failed: %v", err)
	}
	if err := drv.StopCapture(ctx, 78); err != nil {
		t.Fatalf("StopCapture failed: %v", err)
	}

	// Close 永久关闭后拒绝 StartCapture
	if err := drv.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if err := drv.StartCapture(ctx, CaptureSpec{SessionOrTaskID: 79}); err == nil {
		t.Fatalf("expected error from StartCapture after Close")
	}

	logs := logBuf.String()
	for _, needle := range []string{"[DRTUprobe] [Detach] 动态对账摘除旧 PID uprobe", "[DRTUprobe] [Detach] 移除目标进程 uprobe", "[DRTUprobe] [✓ 停录卸载完成]"} {
		if !strings.Contains(logs, needle) {
			t.Errorf("expected log %q, got:\n%s", needle, logs)
		}
	}
}
