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
	Hdr40       [40]byte
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
	createdAt   time.Time
}

// DRTUprobeDriver dophi_mesh 数据面 uprobe 旁路采集传输驱动 (ADR-22 & ADR-23)
type DRTUprobeDriver struct {
	mu              sync.Mutex
	mgr             *ebpf.Manager
	cfg             DRTUprobeConfig
	closed          bool
	activeSpecs     map[uint64]CaptureSpec
	links           map[string]io.Closer
	ringbufReader   *ringbuf.Reader
	readerStop      chan struct{}
	inflightReqs    map[uint64]*inflightReq
	seenChannels    map[string]bool
	errLogCount     int
	truncLogCount   int
	statRawEvents   atomic.Int64
	statValidFrames atomic.Int64
	statReqFrames   atomic.Int64
	statRspPaired   atomic.Int64
	statRspOrphan   atomic.Int64
	statMngFrames   atomic.Int64
	statTruncated   atomic.Int64
	statDropped     atomic.Int64
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
	return &DRTUprobeDriver{
		mgr:          mgr,
		cfg:          cfg,
		activeSpecs:  make(map[uint64]CaptureSpec),
		links:        make(map[string]io.Closer),
		inflightReqs: make(map[uint64]*inflightReq),
		seenChannels: make(map[string]bool),
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

// ParseDRTUprobeEvent 从内核 RingBuf 原始字节流反序列化 DRTUprobeEvent
func ParseDRTUprobeEvent(raw []byte) (DRTUprobeEvent, error) {
	if len(raw) < 84 {
		return DRTUprobeEvent{}, errors.New("drt uprobe event buffer too short (must be >= 84 bytes)")
	}

	var evt DRTUprobeEvent
	evt.SessionID = binary.LittleEndian.Uint64(raw[0:8])
	evt.ProcessID = binary.LittleEndian.Uint64(raw[8:16])
	evt.TimestampNs = binary.LittleEndian.Uint64(raw[16:24])
	evt.Handle = binary.LittleEndian.Uint64(raw[24:32])
	evt.PID = binary.LittleEndian.Uint32(raw[32:36])
	evt.DataLen = binary.LittleEndian.Uint32(raw[36:40])
	evt.IsTruncated = raw[40] != 0
	copy(evt.Hdr40[:], raw[44:84])

	payloadLen := int(evt.DataLen)
	if len(raw) >= 84+payloadLen {
		evt.Data = raw[84 : 84+payloadLen]
	} else if len(raw) > 84 {
		evt.Data = raw[84:]
	}

	return evt, nil
}

// ProcessEvent 处理单条 DRT Uprobe 事件，执行双向相关性归一化与 56B 信封封装推送
func (d *DRTUprobeDriver) ProcessEvent(evt DRTUprobeEvent, sink FrameSink) error {
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
			log.Printf("[DRTUprobe][Truncated] PID=%d handle=0x%x dataLen=%d (>4096B 已截断, proto=%d msgType=%d sno=%d)",
				evt.PID, evt.Handle, evt.DataLen, comm.TransportProto, comm.MsgType, comm.SNO)
		}
	}

	if d.cfg.Verbose && len(d.seenChannels) < 256 {
		chanKey := fmt.Sprintf("%d:%d", evt.PID, evt.Handle)
		if !d.seenChannels[chanKey] {
			d.seenChannels[chanKey] = true
			log.Printf("[DRTUprobe][FirstFrame] PID=%d handle=0x%x dataLen=%d payloadOff=%d proto=%d msgType=%d sno=%d truncated=%v hdr40[:16]=%x",
				evt.PID, evt.Handle, evt.DataLen, payloadOff, comm.TransportProto, comm.MsgType, comm.SNO, evt.IsTruncated, evt.Hdr40[:16])
		}
	}

	now := time.Now()
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
			createdAt:   now,
		}
		if len(d.inflightReqs) >= d.cfg.MaxInflight {
			d.evictOldestInflightLocked()
		}
		d.inflightReqs[comm.SNO] = item

	case comm.MsgType == crosrpc.MsgTypeRsp || comm.MsgType == crosrpc.MsgTypeRspSeg:
		if req, ok := d.inflightReqs[comm.SNO]; ok {
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
				delete(d.inflightReqs, comm.SNO)
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
	for sno, item := range d.inflightReqs {
		if now.Sub(item.createdAt) > ttl {
			delete(d.inflightReqs, sno)
		}
	}
}

func (d *DRTUprobeDriver) evictOldestInflightLocked() {
	var oldestSNO uint64
	var oldestTime time.Time
	first := true
	for sno, item := range d.inflightReqs {
		if first || item.createdAt.Before(oldestTime) {
			oldestTime = item.createdAt
			oldestSNO = sno
			first = false
		}
	}
	if !first {
		delete(d.inflightReqs, oldestSNO)
	}
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
				linkKey := fmt.Sprintf("%d:%d:%d", spec.SessionOrTaskID, p.ProcessID, pid)
				if _, exists := d.links[linkKey]; exists {
					continue
				}
				lnk, err := d.cfg.AttachUprobeFunc(d.cfg.ProcRoot, pid)
				if err != nil || lnk == nil {
					continue
				}
				d.links[linkKey] = lnk
			}
		}
		return nil
	}

	if d.mgr == nil {
		return nil
	}

	// 确保内核 eBPF 探针已加载
	if err := d.mgr.EnsureAttached(); err != nil {
		log.Printf("[DRTUprobe] EnsureAttached 警告: %v", err)
	}

	prog := d.mgr.DRTUprobeProgram()
	if prog == nil {
		log.Printf("[DRTUprobe] 跳过挂载: 内核 CrosDrtWriteBulk eBPF 程序未就绪 (SessionID=%d)", spec.SessionOrTaskID)
	} else {
		for _, p := range spec.Processes {
			pids := p.EffectivePIDs()
			if len(pids) == 0 {
				log.Printf("[DRTUprobe] 跳过挂载: ProcessID=%d 未解析到存活 PID (cmd=%q)", p.ProcessID, p.CmdPattern)
				continue
			}
			for _, pid := range pids {
				linkKey := fmt.Sprintf("%d:%d:%d", spec.SessionOrTaskID, p.ProcessID, pid)
				if _, exists := d.links[linkKey]; exists {
					continue
				}

				soPath, err := FindMappedLibraryPath(d.cfg.ProcRoot, pid, "libdopra")
				if err != nil {
					log.Printf("[DRTUprobe] 跳过挂载: PID=%d (ProcessID=%d) 未找到 libdopra 映射: %v", pid, p.ProcessID, err)
					continue
				}

				offsets, err := ResolveDRTSymbolOffsets(soPath)
				if err != nil || offsets.WriteBulkOffset == 0 {
					log.Printf("[DRTUprobe] 跳过挂载: PID=%d Lib=%s 解析 DRT_WriteBulk 失败: %v", pid, soPath, err)
					continue
				}

				ex, err := link.OpenExecutable(soPath)
				if err != nil {
					log.Printf("[DRTUprobe] OpenExecutable(%s) 失败: %v", soPath, err)
					continue
				}

				up, err := ex.Uprobe("DRT_WriteBulk", prog, &link.UprobeOptions{
					Offset: offsets.WriteBulkOffset,
					PID:    int(pid),
				})
				if err != nil {
					log.Printf("[DRTUprobe] Uprobe(pid=%d) 挂载失败: %v", pid, err)
					continue
				}

				d.links[linkKey] = up
				log.Printf("[DRTUprobe] [✓ Uprobe 动态挂载成功] PID=%d, Lib=%s, Offset=0x%x, SessionID=%d", pid, soPath, offsets.WriteBulkOffset, spec.SessionOrTaskID)
			}
		}
	}

	// 启动 RingBuf 消费协程
	if d.ringbufReader == nil {
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

// StopCapture 停止指定会话采集，幂等卸载对应 uprobe 链接
func (d *DRTUprobeDriver) StopCapture(ctx context.Context, sessionOrTaskID uint64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	delete(d.activeSpecs, sessionOrTaskID)

	prefix := fmt.Sprintf("%d:", sessionOrTaskID)
	for k, l := range d.links {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			_ = l.Close()
			delete(d.links, k)
		}
	}

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
		clear(d.seenChannels)
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
	for k, l := range d.links {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			_ = l.Close()
			delete(d.links, k)
		}
	}
	return nil
}

// Close 五出口幂等卸载：清理全部探针链接与环形缓冲 reader
func (d *DRTUprobeDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.closed = true

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
	clear(d.seenChannels)
	d.errLogCount = 0
	d.truncLogCount = 0
	return nil
}

// ActiveLinksCount 获取当前挂载中的 uprobe 数量 (供单测断言 ARMED 待命期 0 uprobe)
func (d *DRTUprobeDriver) ActiveLinksCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.links)
}
