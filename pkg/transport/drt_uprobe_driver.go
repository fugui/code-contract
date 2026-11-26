package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
	"code-contract/pkg/ebpf"

	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
)

// DRTUprobeConfig dophi_mesh uprobe 传输驱动配置
type DRTUprobeConfig struct {
	ProcRoot         string                                               // 进程扫描根目录 (默认 "/proc")
	PayloadOffset    int                                                  // 业务载荷偏移量 (默认 0: 首 64B 自动锚定 "CSE.TCP")
	MaxInflight      int                                                  // 在途请求容量上限 (默认 10000)
	InflightTTL      time.Duration                                        // 在途请求超时清理时间 (默认 30s)
	StopDrainWait    time.Duration                                        // 停录摘除 uprobe 后等待 RingBuf 排空时间 (默认 50ms，负数为 0)
	Verbose          bool                                                 // 开启详细头自检与首帧诊断日志（不打印业务 payload）
	AttachUprobeFunc func(procRoot string, pid uint32) (io.Closer, error) `json:"-"` // 可选测试注入钩子
}

// DRTUprobeStats dophi_mesh uprobe 采集累计统计快照
type DRTUprobeStats struct {
	RawEvents       int64
	ValidFrames     int64
	ReqFrames       int64
	RspPaired       int64
	RspOrphan       int64
	MngFrames       int64
	TruncatedFrames int64
	DroppedEvents   int64
	ActiveLinks     int
}

// DRTUprobeEvent 从 drt_ringbuf 消费的内核事件反序列化结构
type DRTUprobeEvent struct {
	SessionID   uint64
	ProcessID   uint64
	TimestampNs uint64
	Handle      uint64
	PID         uint32
	DataLen     uint32
	IsTruncated bool
	ChunkIdx    uint8
	ChunkCount  uint8
	Hdr40       [40]byte
	OrigLen     uint32
	TID         uint32
	Data        []byte
}

type inflightReq struct {
	sessionID   uint64
	processID   uint64
	capturedPID uint32
	direction   uint8
	srcIP       [4]byte
	srcPort     uint16
	dstIP       [4]byte
	dstPort     uint16
	connSeq     uint32
	serviceName string
	operation   string
	createdAt   time.Time
}

type drtChunkKey struct {
	sessionID   uint64
	pid         uint32
	tid         uint32
	timestampNs uint64
	handle      uint64
}

type drtChunkAssembly struct {
	chunks      [][]byte
	received    int
	totalLen    int
	chunkCount  uint8
	origLen     uint32
	isTruncated bool
	hdr40       [40]byte
	createdAt   time.Time
}

const (
	drtWriteBulkSymbol      = "DRT_WriteBulk"
	DRTUprobeEventHeaderLen = 96
	MaxDRTChunkSize         = 32768
	MaxDRTChunks            = 8
	MaxDRTTotalSize         = MaxDRTChunkSize * MaxDRTChunks
	defaultChunkTTL         = 5 * time.Second
	maxPendingChunks        = 1024
	defaultStopDrainWait    = 50 * time.Millisecond
)

func buildDRTUprobeOptions(fileOffset uint64, pid uint32) *link.UprobeOptions {
	return &link.UprobeOptions{
		Address: fileOffset,
		PID:     int(pid),
	}
}

// DRTUprobeDriver dophi_mesh 数据面 uprobe 旁路采集传输驱动 (ADR-22 & ADR-23)
type DRTUprobeDriver struct {
	mu               sync.Mutex
	mgr              *ebpf.Manager
	cfg              DRTUprobeConfig
	closed           bool
	activeSpecs      map[uint64]CaptureSpec
	links            map[string]io.Closer
	lastAttachErr    map[string]string
	attachExecUprobe func(ex *link.Executable, symbol string, opts *link.UprobeOptions) (io.Closer, error)
	ringbufReader    *ringbuf.Reader
	readerStop       chan struct{}
	inflightReqs     map[uint64][]*inflightReq
	inflightCount    int
	pendingChunks    map[drtChunkKey]*drtChunkAssembly
	seenChannels     map[string]bool
	errLogCount      int
	truncLogCount    int
	statRawEvents    atomic.Int64
	statValidFrames  atomic.Int64
	statReqFrames    atomic.Int64
	statRspPaired    atomic.Int64
	statRspOrphan    atomic.Int64
	statMngFrames    atomic.Int64
	statTruncated    atomic.Int64
	statDropped      atomic.Int64
}

// NewDRTUprobeDriver 实例化 builtin-drt-uprobe 传输驱动
func NewDRTUprobeDriver(mgr *ebpf.Manager, cfg DRTUprobeConfig) *DRTUprobeDriver {
	if cfg.ProcRoot == "" {
		cfg.ProcRoot = "/proc"
	}
	if cfg.MaxInflight <= 0 {
		cfg.MaxInflight = 10000
	}
	if cfg.InflightTTL <= 0 {
		cfg.InflightTTL = 30 * time.Second
	}
	if cfg.StopDrainWait == 0 {
		cfg.StopDrainWait = defaultStopDrainWait
	} else if cfg.StopDrainWait < 0 {
		cfg.StopDrainWait = 0
	}
	return &DRTUprobeDriver{
		mgr:           mgr,
		cfg:           cfg,
		activeSpecs:   make(map[uint64]CaptureSpec),
		links:         make(map[string]io.Closer),
		lastAttachErr: make(map[string]string),
		inflightReqs:  make(map[uint64][]*inflightReq),
		pendingChunks: make(map[drtChunkKey]*drtChunkAssembly),
		seenChannels:  make(map[string]bool),
	}
}

// Stats 返回 dophi_mesh uprobe 采集累计统计快照
func (d *DRTUprobeDriver) Stats() DRTUprobeStats {
	d.mu.Lock()
	activeLinks := len(d.links)
	d.mu.Unlock()
	return DRTUprobeStats{
		RawEvents:       d.statRawEvents.Load(),
		ValidFrames:     d.statValidFrames.Load(),
		ReqFrames:       d.statReqFrames.Load(),
		RspPaired:       d.statRspPaired.Load(),
		RspOrphan:       d.statRspOrphan.Load(),
		MngFrames:       d.statMngFrames.Load(),
		TruncatedFrames: d.statTruncated.Load(),
		DroppedEvents:   d.statDropped.Load(),
		ActiveLinks:     activeLinks,
	}
}

// Info 返回驱动元信息
func (d *DRTUprobeDriver) Info() DriverInfo {
	return DriverInfo{
		Name:          "builtin-drt-uprobe",
		Version:       "1.0.0",
		Transports:    []string{"dophi_mesh"},
		TransportCode: crosrpc.TransportMesh,
		Capabilities:  agentproto.CapCapture,
	}
}

// IsAvailable 返回宿主机运行权限
func (d *DRTUprobeDriver) IsAvailable() (bool, string) {
	if d.mgr == nil {
		return false, "ebpf manager not initialized"
	}
	hasCaps, missing, detail := d.mgr.CheckCapabilities()
	if !hasCaps {
		return false, fmt.Sprintf("缺少必须的特权集合: %v (%s)", missing, detail)
	}
	return true, detail
}

// ExtractCROSPacket 根据配置偏移或首 64B "CSE.TCP" 自动锚定提取原始 CROS 帧
func ExtractCROSPacket(raw []byte, configuredOffset int) ([]byte, int) {
	if configuredOffset > 0 {
		if len(raw) <= configuredOffset {
			return nil, -1
		}
		return raw[configuredOffset:], configuredOffset
	}

	// 自动锚定首 64B 内的 "CSE.TCP" 魔术字
	limit := len(raw)
	if limit > 64 {
		limit = 64
	}
	if bytes.HasPrefix(raw, []byte(crosrpc.MagicPrefix)) {
		return raw, 0
	}
	idx := bytes.Index(raw[:limit], []byte(crosrpc.MagicPrefix))
	if idx >= 0 {
		return raw[idx:], idx
	}
	return raw, 0
}

// ParseDRTUprobeEvent 从内核 RingBuf 原始字节流反序列化 DRTUprobeEvent (96B 定长头 + 载荷切片)
func ParseDRTUprobeEvent(raw []byte) (DRTUprobeEvent, error) {
	if len(raw) < DRTUprobeEventHeaderLen {
		return DRTUprobeEvent{}, fmt.Errorf("drt uprobe event buffer too short (must be >= %d bytes)", DRTUprobeEventHeaderLen)
	}

	var evt DRTUprobeEvent
	evt.SessionID = binary.LittleEndian.Uint64(raw[0:8])
	evt.ProcessID = binary.LittleEndian.Uint64(raw[8:16])
	evt.TimestampNs = binary.LittleEndian.Uint64(raw[16:24])
	evt.Handle = binary.LittleEndian.Uint64(raw[24:32])
	evt.PID = binary.LittleEndian.Uint32(raw[32:36])
	evt.DataLen = binary.LittleEndian.Uint32(raw[36:40])
	evt.IsTruncated = raw[40] != 0
	evt.ChunkIdx = raw[41]
	evt.ChunkCount = raw[42]
	copy(evt.Hdr40[:], raw[44:84])
	evt.OrigLen = binary.LittleEndian.Uint32(raw[84:88])
	evt.TID = binary.LittleEndian.Uint32(raw[92:96])

	payloadLen := int(evt.DataLen)
	if len(raw) >= DRTUprobeEventHeaderLen+payloadLen {
		evt.Data = raw[DRTUprobeEventHeaderLen : DRTUprobeEventHeaderLen+payloadLen]
	} else if len(raw) > DRTUprobeEventHeaderLen {
		evt.Data = raw[DRTUprobeEventHeaderLen:]
	}

	return evt, nil
}

func (d *DRTUprobeDriver) evictPendingChunksLocked(now time.Time) {
	for k, asm := range d.pendingChunks {
		if now.Sub(asm.createdAt) > defaultChunkTTL {
			delete(d.pendingChunks, k)
			d.statDropped.Add(1)
		}
	}
}

func (d *DRTUprobeDriver) evictOldestPendingChunkLocked() {
	var oldestKey drtChunkKey
	var oldestTime time.Time
	first := true
	for k, asm := range d.pendingChunks {
		if first || asm.createdAt.Before(oldestTime) {
			oldestTime = asm.createdAt
			oldestKey = k
			first = false
		}
	}
	if !first {
		delete(d.pendingChunks, oldestKey)
		d.statDropped.Add(1)
	}
}

func (d *DRTUprobeDriver) reassembleChunkLocked(evt DRTUprobeEvent, now time.Time) (DRTUprobeEvent, bool, error) {
	if evt.ChunkCount <= 1 {
		return evt, true, nil
	}
	if evt.ChunkCount > MaxDRTChunks || evt.ChunkIdx >= evt.ChunkCount {
		d.statDropped.Add(1)
		return DRTUprobeEvent{}, false, fmt.Errorf("invalid drt chunk metadata: chunk_idx=%d chunk_count=%d", evt.ChunkIdx, evt.ChunkCount)
	}

	d.evictPendingChunksLocked(now)

	key := drtChunkKey{
		sessionID:   evt.SessionID,
		pid:         evt.PID,
		tid:         evt.TID,
		timestampNs: evt.TimestampNs,
		handle:      evt.Handle,
	}

	asm, exists := d.pendingChunks[key]
	if !exists {
		if len(d.pendingChunks) >= maxPendingChunks {
			d.evictOldestPendingChunkLocked()
		}
		asm = &drtChunkAssembly{
			chunks:      make([][]byte, evt.ChunkCount),
			chunkCount:  evt.ChunkCount,
			origLen:     evt.OrigLen,
			isTruncated: evt.IsTruncated,
			createdAt:   now,
		}
		d.pendingChunks[key] = asm
	} else if asm.chunkCount != evt.ChunkCount {
		delete(d.pendingChunks, key)
		d.statDropped.Add(1)
		return DRTUprobeEvent{}, false, fmt.Errorf("mismatched drt chunk_count: got %d expected %d", evt.ChunkCount, asm.chunkCount)
	}

	if evt.ChunkIdx == 0 {
		asm.hdr40 = evt.Hdr40
	}
	if evt.OrigLen > 0 {
		asm.origLen = evt.OrigLen
	}
	if evt.IsTruncated {
		asm.isTruncated = true
	}

	if asm.chunks[evt.ChunkIdx] == nil {
		cp := make([]byte, len(evt.Data))
		copy(cp, evt.Data)
		asm.chunks[evt.ChunkIdx] = cp
		asm.received++
		asm.totalLen += len(cp)
	}

	if asm.received < int(asm.chunkCount) {
		return DRTUprobeEvent{}, false, nil
	}

	delete(d.pendingChunks, key)
	fullData := make([]byte, 0, asm.totalLen)
	for _, part := range asm.chunks {
		fullData = append(fullData, part...)
	}

	out := evt
	out.ChunkIdx = 0
	out.ChunkCount = 1
	out.DataLen = uint32(len(fullData))
	out.OrigLen = asm.origLen
	out.IsTruncated = asm.isTruncated
	out.Hdr40 = asm.hdr40
	out.Data = fullData
	return out, true, nil
}

func findMatchingInflight(bucket []*inflightReq, pid uint32, svcName, opName string) (int, *inflightReq) {
	if len(bucket) == 0 {
		return -1, nil
	}
	if svcName != "" {
		// 1. 优先精确匹配服务/方法且来自对端进程 (排除同进程冲突)
		for i, req := range bucket {
			if req.serviceName == svcName && req.operation == opName && req.capturedPID != pid {
				return i, req
			}
		}
		// 2. 同进程自调：精确匹配服务/方法
		for i, req := range bucket {
			if req.serviceName == svcName && req.operation == opName {
				return i, req
			}
		}
	}
	// 3. 向后兼容：若任一方缺少服务/方法头（如极简测试帧），按 FIFO 匹配首条无冲突记录
	for i, req := range bucket {
		if svcName == "" || req.serviceName == "" {
			return i, req
		}
	}
	return -1, nil
}

// ProcessEvent 处理单条 DRT Uprobe 事件，执行多分片重组、双向相关性归一化与 56B 信封封装推送
func (d *DRTUprobeDriver) ProcessEvent(evt DRTUprobeEvent, sink FrameSink) error {
	now := time.Now()
	if evt.ChunkCount > 1 {
		d.mu.Lock()
		reassembled, ready, err := d.reassembleChunkLocked(evt, now)
		d.mu.Unlock()
		if err != nil || !ready {
			return err
		}
		evt = reassembled
	}

	d.statRawEvents.Add(1)

	crosPkt, payloadOff := ExtractCROSPacket(evt.Data, d.cfg.PayloadOffset)
	var comm crosrpc.CommHead
	var parseErr error
	if len(crosPkt) < crosrpc.CommHeadLen {
		parseErr = crosrpc.ErrBufferTooShort
	} else {
		comm, parseErr = crosrpc.ParseCommHead(crosPkt)
		if parseErr == nil && !bytes.Equal(comm.Magic[:], []byte(crosrpc.MagicPrefix)) {
			parseErr = crosrpc.ErrInvalidMagic
		}
	}
	if parseErr != nil {
		d.statDropped.Add(1)
		d.logHeaderError(evt, payloadOff, parseErr)
		return parseErr
	}

	// 尽力提取 ServiceName / Operation 与 Oneway 标记，避免多进程 SNO 冲突误配对
	var svcName, opName string
	isOneway := false
	if parsed, pErr := crosrpc.ParsePacket(crosPkt); pErr == nil && parsed != nil {
		if parsed.Req != nil {
			svcName = parsed.Req.ServiceName
			opName = parsed.Req.Operation
			if parsed.Req.ReqType == crosrpc.ReqTypeOneway {
				isOneway = true
			}
		} else if parsed.Rsp != nil {
			svcName = parsed.Rsp.ServiceName
			opName = parsed.Rsp.Operation
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if evt.IsTruncated {
		d.statTruncated.Add(1)
		truncLimit := 5
		if d.cfg.Verbose {
			truncLimit = 10
		}
		if d.truncLogCount < truncLimit {
			d.truncLogCount++
			origLen := evt.OrigLen
			if origLen == 0 {
				origLen = evt.DataLen
			}
			log.Printf("[DRTUprobe][Truncated] PID=%d handle=0x%x origLen=%d capturedLen=%d (>%dB 已截断, proto=%d msgType=%d sno=%d)",
				evt.PID, evt.Handle, origLen, evt.DataLen, MaxDRTTotalSize, comm.TransportProto, comm.MsgType, comm.SNO)
		}
	}

	if d.cfg.Verbose && len(d.seenChannels) < 256 {
		chanKey := fmt.Sprintf("%d:%d", evt.PID, evt.Handle)
		if !d.seenChannels[chanKey] {
			d.seenChannels[chanKey] = true
			log.Printf("[DRTUprobe][FirstFrame] PID=%d handle=0x%x dataLen=%d origLen=%d chunks=%d payloadOff=%d proto=%d msgType=%d sno=%d truncated=%v hdr40[:16]=%x",
				evt.PID, evt.Handle, evt.DataLen, evt.OrigLen, evt.ChunkCount, payloadOff, comm.TransportProto, comm.MsgType, comm.SNO, evt.IsTruncated, evt.Hdr40[:16])
		}
	}

	d.evictExpiredInflightLocked(now)

	env := agentproto.EnvelopeHeader{
		Magic:           agentproto.EnvelopeMagic,
		ProtoVer:        agentproto.EnvelopeProtoVer,
		EventType:       agentproto.EventTrafficFrame,
		SessionOrTaskID: evt.SessionID,
		ProcessID:       evt.ProcessID,
		CapturedPID:     evt.PID,
		TransportProto:  crosrpc.TransportMesh,
		CapturedAtNs:    int64(evt.TimestampNs),
		SrcIPv4:         [4]byte{127, 0, 0, 1},
		DstIPv4:         [4]byte{127, 0, 0, 1},
		SrcPort:         uint16(evt.PID),
		DstPort:         8000,
		ConnSeq:         uint32(evt.Handle),
	}
	if evt.IsTruncated {
		env.Flags |= agentproto.FlagIsTruncated
	}

	var frameKind string
	switch {
	case comm.MsgType == crosrpc.MsgTypeReq:
		frameKind = "REQ"
		env.Direction = agentproto.DirectionEgress
		if !isOneway {
			item := &inflightReq{
				sessionID:   evt.SessionID,
				processID:   evt.ProcessID,
				capturedPID: evt.PID,
				direction:   agentproto.DirectionEgress,
				srcIP:       env.SrcIPv4,
				srcPort:     env.SrcPort,
				dstIP:       env.DstIPv4,
				dstPort:     env.DstPort,
				connSeq:     env.ConnSeq,
				serviceName: svcName,
				operation:   opName,
				createdAt:   now,
			}
			for d.inflightCount >= d.cfg.MaxInflight {
				d.evictOldestInflightLocked()
			}
			d.inflightReqs[comm.SNO] = append(d.inflightReqs[comm.SNO], item)
			d.inflightCount++
		}

	case comm.MsgType == crosrpc.MsgTypeRsp || comm.MsgType == crosrpc.MsgTypeRspSeg:
		bucket := d.inflightReqs[comm.SNO]
		if idx, req := findMatchingInflight(bucket, evt.PID, svcName, opName); req != nil {
			frameKind = "RSP_PAIRED"
			// ADR-11 投影归一化：将应答帧投影到对应的请求四元组与 ProcessID，保证 ExchangeAssembler 配对
			env.ProcessID = req.processID
			env.CapturedPID = req.capturedPID
			env.Direction = req.direction
			env.SrcIPv4 = req.srcIP
			env.SrcPort = req.srcPort
			env.DstIPv4 = req.dstIP
			env.DstPort = req.dstPort
			env.ConnSeq = req.connSeq

			if comm.MsgType == crosrpc.MsgTypeRsp {
				if len(bucket) <= 1 {
					delete(d.inflightReqs, comm.SNO)
				} else {
					d.inflightReqs[comm.SNO] = append(bucket[:idx], bucket[idx+1:]...)
				}
				if d.inflightCount > 0 {
					d.inflightCount--
				}
			}
		} else {
			frameKind = "RSP_ORPHAN"
			env.Direction = agentproto.DirectionIngress
			env.SrcPort = 8000
			env.DstPort = uint16(evt.PID)
		}

	default:
		// 管理帧或其它控制帧
		frameKind = "MNG"
		env.Direction = agentproto.DirectionIngress
	}

	d.statValidFrames.Add(1)
	switch frameKind {
	case "REQ":
		d.statReqFrames.Add(1)
	case "RSP_PAIRED":
		d.statRspPaired.Add(1)
	case "RSP_ORPHAN":
		d.statRspOrphan.Add(1)
	case "MNG":
		d.statMngFrames.Add(1)
	}

	if sink != nil {
		return sink.PushFrame(env, crosPkt)
	}
	return nil
}

func (d *DRTUprobeDriver) logHeaderError(evt DRTUprobeEvent, payloadOff int, err error) {
	d.mu.Lock()
	limit := 5
	if d.cfg.Verbose {
		limit = 20
	}
	if d.errLogCount >= limit {
		d.mu.Unlock()
		return
	}
	d.errLogCount++
	d.mu.Unlock()

	headHexLen := len(evt.Data)
	if headHexLen > 24 {
		headHexLen = 24
	}
	log.Printf("[DRTUprobe][Drop] PID=%d handle=0x%x dataLen=%d payloadOff=%d err=%v hdr40[:16]=%x head[:%d]=%x",
		evt.PID, evt.Handle, evt.DataLen, payloadOff, err, evt.Hdr40[:16], headHexLen, evt.Data[:headHexLen])
}

func (d *DRTUprobeDriver) evictExpiredInflightLocked(now time.Time) {
	ttl := d.cfg.InflightTTL
	for sno, bucket := range d.inflightReqs {
		kept := bucket[:0]
		for _, item := range bucket {
			if now.Sub(item.createdAt) > ttl {
				if d.inflightCount > 0 {
					d.inflightCount--
				}
				continue
			}
			kept = append(kept, item)
		}
		if len(kept) == 0 {
			delete(d.inflightReqs, sno)
		} else {
			d.inflightReqs[sno] = kept
		}
	}
}

func (d *DRTUprobeDriver) evictOldestInflightLocked() {
	var oldestSNO uint64
	var oldestIdx int
	var oldestTime time.Time
	first := true
	for sno, bucket := range d.inflightReqs {
		for idx, item := range bucket {
			if first || item.createdAt.Before(oldestTime) {
				oldestTime = item.createdAt
				oldestSNO = sno
				oldestIdx = idx
				first = false
			}
		}
	}
	if first {
		return
	}
	bucket := d.inflightReqs[oldestSNO]
	if len(bucket) <= 1 {
		delete(d.inflightReqs, oldestSNO)
	} else {
		d.inflightReqs[oldestSNO] = append(bucket[:oldestIdx], bucket[oldestIdx+1:]...)
	}
	if d.inflightCount > 0 {
		d.inflightCount--
	}
}

func (d *DRTUprobeDriver) logAttachErrorLocked(linkKey, msg string) {
	if d.lastAttachErr == nil {
		d.lastAttachErr = make(map[string]string)
	}
	if d.lastAttachErr[linkKey] == msg && !d.cfg.Verbose {
		return
	}
	d.lastAttachErr[linkKey] = msg
	log.Print(msg)
}

func (d *DRTUprobeDriver) attachPIDLocked(sessionOrTaskID uint64, processID uint64, pid uint32) {
	if pid == 0 {
		return
	}
	linkKey := fmt.Sprintf("%d:%d:%d", sessionOrTaskID, processID, pid)
	if _, exists := d.links[linkKey]; exists {
		return
	}

	if d.cfg.AttachUprobeFunc != nil {
		lnk, err := d.cfg.AttachUprobeFunc(d.cfg.ProcRoot, pid)
		if err != nil || lnk == nil {
			return
		}
		delete(d.lastAttachErr, linkKey)
		d.links[linkKey] = lnk
		return
	}

	if d.attachExecUprobe == nil {
		if d.mgr == nil || d.mgr.DRTUprobeProgram() == nil {
			return
		}
	}

	soPath, err := FindMappedLibraryPath(d.cfg.ProcRoot, pid, "libdopra")
	if err != nil {
		d.logAttachErrorLocked(linkKey, fmt.Sprintf("[DRTUprobe] 跳过挂载: PID=%d (ProcessID=%d) 未找到 libdopra 映射: %v", pid, processID, err))
		return
	}

	offsets, err := ResolveDRTSymbolOffsets(soPath)
	if err != nil || offsets.WriteBulkOffset == 0 {
		d.logAttachErrorLocked(linkKey, fmt.Sprintf("[DRTUprobe] 跳过挂载: PID=%d Lib=%s 解析 %s 失败: %v", pid, soPath, drtWriteBulkSymbol, err))
		return
	}

	ex, err := link.OpenExecutable(soPath)
	if err != nil {
		d.logAttachErrorLocked(linkKey, fmt.Sprintf("[DRTUprobe] OpenExecutable(%s) 失败: %v", soPath, err))
		return
	}

	opts := buildDRTUprobeOptions(offsets.WriteBulkOffset, pid)
	var up io.Closer
	if d.attachExecUprobe != nil {
		up, err = d.attachExecUprobe(ex, drtWriteBulkSymbol, opts)
	} else {
		up, err = ex.Uprobe(drtWriteBulkSymbol, d.mgr.DRTUprobeProgram(), opts)
	}
	if err != nil {
		d.logAttachErrorLocked(linkKey, fmt.Sprintf("[DRTUprobe] Uprobe(pid=%d, lib=%s, addr=0x%x) 挂载失败: %v", pid, soPath, opts.Address, err))
		return
	}

	delete(d.lastAttachErr, linkKey)
	d.links[linkKey] = up
	log.Printf("[DRTUprobe] [✓ Uprobe 动态挂载成功] PID=%d, Lib=%s, Addr=0x%x, SessionID=%d", pid, soPath, opts.Address, sessionOrTaskID)
}

// SyncProcessPIDs 在 CAPTURING 期间按最新存活 PID 列表动态同步 uprobe（摘除退出旧 PID，补挂新 PID）；待命期不执行挂载
func (d *DRTUprobeDriver) SyncProcessPIDs(sessionOrTaskID uint64, processID uint64, pids []uint32) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed || sessionOrTaskID == 0 {
		return nil
	}
	if _, active := d.activeSpecs[sessionOrTaskID]; !active {
		return nil
	}

	desired := make(map[string]struct{}, len(pids))
	for _, pid := range pids {
		if pid == 0 {
			continue
		}
		desired[fmt.Sprintf("%d:%d:%d", sessionOrTaskID, processID, pid)] = struct{}{}
	}

	prefix := fmt.Sprintf("%d:%d:", sessionOrTaskID, processID)
	for k, l := range d.links {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			if _, keep := desired[k]; !keep {
				_ = l.Close()
				delete(d.links, k)
				log.Printf("[DRTUprobe] [Detach] 动态对账摘除旧 PID uprobe: key=%s, remainingLinks=%d", k, len(d.links))
			}
		}
	}
	for k := range d.lastAttachErr {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			if _, keep := desired[k]; !keep {
				delete(d.lastAttachErr, k)
			}
		}
	}

	for _, pid := range pids {
		d.attachPIDLocked(sessionOrTaskID, processID, pid)
	}
	return nil
}

// StartCapture 下发采集规格并在 CAPTURING 期间按 PID 挂载 DRT_WriteBulk uprobe（支持同进程多 PID）
func (d *DRTUprobeDriver) StartCapture(ctx context.Context, spec CaptureSpec) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return errors.New("builtin-drt-uprobe driver is closed")
	}

	d.activeSpecs[spec.SessionOrTaskID] = spec

	if d.cfg.AttachUprobeFunc != nil {
		for _, p := range spec.Processes {
			for _, pid := range p.EffectivePIDs() {
				d.attachPIDLocked(spec.SessionOrTaskID, p.ProcessID, pid)
			}
		}
		return nil
	}

	if d.mgr == nil && d.attachExecUprobe == nil {
		return nil
	}

	var progReady bool
	if d.mgr != nil {
		// 确保内核 eBPF 探针已加载
		if err := d.mgr.EnsureAttached(); err != nil {
			log.Printf("[DRTUprobe] EnsureAttached 警告: %v", err)
		}
		progReady = d.mgr.DRTUprobeProgram() != nil
	}

	if !progReady && d.attachExecUprobe == nil {
		log.Printf("[DRTUprobe] 跳过挂载: 内核 CrosDrtWriteBulk eBPF 程序未就绪 (SessionID=%d)", spec.SessionOrTaskID)
	} else {
		for _, p := range spec.Processes {
			pids := p.EffectivePIDs()
			if len(pids) == 0 {
				log.Printf("[DRTUprobe] 跳过挂载: ProcessID=%d 未解析到存活 PID (cmd=%q)", p.ProcessID, p.CmdPattern)
				continue
			}
			for _, pid := range pids {
				d.attachPIDLocked(spec.SessionOrTaskID, p.ProcessID, pid)
			}
		}
	}

	// 启动 RingBuf 消费协程
	if d.mgr != nil && d.ringbufReader == nil {
		rbMap := d.mgr.DRTRingbufMap()
		if rbMap != nil {
			rb, err := ringbuf.NewReader(rbMap)
			if err == nil {
				d.ringbufReader = rb
				d.readerStop = make(chan struct{})
				go d.consumeRingbuf(rb, d.readerStop)
				log.Printf("[DRTUprobe] drt_ringbuf 消费协程已启动")
			} else {
				log.Printf("[DRTUprobe] 启动 drt_ringbuf 消费失败: %v", err)
			}
		} else {
			log.Printf("[DRTUprobe] drt_ringbuf Map 未就绪，无法消费 uprobe 事件")
		}
	}

	if d.cfg.Verbose {
		log.Printf("[DRTUprobe] StartCapture 完成: SessionID=%d, 本次活跃 uprobe 数=%d", spec.SessionOrTaskID, len(d.links))
	}

	return nil
}

func (d *DRTUprobeDriver) consumeRingbuf(rb *ringbuf.Reader, stopCh chan struct{}) {
	for {
		select {
		case <-stopCh:
			return
		default:
		}

		rec, err := rb.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) {
				return
			}
			continue
		}

		evt, pErr := ParseDRTUprobeEvent(rec.RawSample)
		if pErr != nil {
			d.statDropped.Add(1)
			d.logHeaderError(DRTUprobeEvent{DataLen: uint32(len(rec.RawSample))}, -1, pErr)
			continue
		}

		d.mu.Lock()
		spec, ok := d.activeSpecs[evt.SessionID]
		sink := spec.Sink
		d.mu.Unlock()

		if !ok || sink == nil {
			d.statDropped.Add(1)
			if d.cfg.Verbose {
				d.logHeaderError(evt, -1, fmt.Errorf("session %d inactive or sink nil", evt.SessionID))
			}
			continue
		}
		_ = d.ProcessEvent(evt, sink)
	}
}

// StopCapture 停止指定会话采集，先摘除 uprobe 链接、短暂排空 RingBuf 在途事件后再关闭消费器
func (d *DRTUprobeDriver) StopCapture(ctx context.Context, sessionOrTaskID uint64) error {
	d.mu.Lock()
	prefix := fmt.Sprintf("%d:", sessionOrTaskID)
	detached := 0
	for k, l := range d.links {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			_ = l.Close()
			delete(d.links, k)
			detached++
		}
	}
	for k := range d.lastAttachErr {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(d.lastAttachErr, k)
		}
	}
	if detached > 0 {
		log.Printf("[DRTUprobe] [✓ 停录卸载完成] SessionID=%d, 已摘除 %d 个 uprobe 链接, remainingLinks=%d", sessionOrTaskID, detached, len(d.links))
	}

	needDrain := d.ringbufReader != nil && d.cfg.StopDrainWait > 0
	drainWait := d.cfg.StopDrainWait
	d.mu.Unlock()

	// 在释放 uprobe 后短暂等待 consumeRingbuf 将 drt_ringbuf 在途尾部事件推入 sink
	if needDrain {
		timer := time.NewTimer(drainWait)
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	delete(d.activeSpecs, sessionOrTaskID)

	if len(d.activeSpecs) == 0 {
		if d.ringbufReader != nil {
			if d.readerStop != nil {
				close(d.readerStop)
				d.readerStop = nil
			}
			_ = d.ringbufReader.Close()
			d.ringbufReader = nil
		}
		clear(d.inflightReqs)
		d.inflightCount = 0
		clear(d.pendingChunks)
		clear(d.seenChannels)
		clear(d.lastAttachErr)
		d.errLogCount = 0
		d.truncLogCount = 0
	}

	return nil
}

// RemoveTarget 移除单进程全部 PID 实例监控
func (d *DRTUprobeDriver) RemoveTarget(sessionOrTaskID uint64, processID uint64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	prefix := fmt.Sprintf("%d:%d:", sessionOrTaskID, processID)
	detached := 0
	for k, l := range d.links {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			_ = l.Close()
			delete(d.links, k)
			detached++
		}
	}
	for k := range d.lastAttachErr {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(d.lastAttachErr, k)
		}
	}
	if detached > 0 {
		log.Printf("[DRTUprobe] [Detach] 移除目标进程 uprobe: SessionID=%d, ProcessID=%d, detached=%d, remainingLinks=%d", sessionOrTaskID, processID, detached, len(d.links))
	}
	return nil
}

func (d *DRTUprobeDriver) cleanupLocked(markClosed bool) {
	if markClosed {
		d.closed = true
	}

	for k, l := range d.links {
		_ = l.Close()
		delete(d.links, k)
	}

	if d.ringbufReader != nil {
		if d.readerStop != nil {
			close(d.readerStop)
			d.readerStop = nil
		}
		_ = d.ringbufReader.Close()
		d.ringbufReader = nil
	}

	clear(d.activeSpecs)
	clear(d.inflightReqs)
	d.inflightCount = 0
	clear(d.pendingChunks)
	clear(d.seenChannels)
	clear(d.lastAttachErr)
	d.errLogCount = 0
	d.truncLogCount = 0
}

// Reset 幂等自洁重置：卸载全部链接与 RingBuf reader，但不永久置 closed=true，支持后续录制复用
func (d *DRTUprobeDriver) Reset() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cleanupLocked(false)
	return nil
}

// Close 五出口幂等卸载：永久关闭驱动并清理全部探针链接与环形缓冲 reader
func (d *DRTUprobeDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cleanupLocked(true)
	return nil
}

// ActiveLinksCount 获取当前挂载中的 uprobe 数量 (供单测断言 ARMED 待命期 0 uprobe)
func (d *DRTUprobeDriver) ActiveLinksCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.links)
}
