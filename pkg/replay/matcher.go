package replay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
)

var (
	ErrInvalidFrame = errors.New("crosrpc invalid frame")
)

// MatchResultType 匹配分类
type MatchResultType string

const (
	MatchHitScoped    MatchResultType = "SCOPED_HIT"
	MatchHitScopedSeq MatchResultType = "SCOPED_SEQ_HIT"
	MatchHitGlobal    MatchResultType = "GLOBAL_FALLBACK_HIT"
	MatchHitMng       MatchResultType = "MNG_HIT"
	MatchOneway       MatchResultType = "ONEWAY_HIT"
	MatchMiss         MatchResultType = "MOCK_MISS"
)

// MatchResult 匹配结果封装
type MatchResult struct {
	Result         MatchResultType
	ServiceName    string
	Operation      string
	ResponseFrames [][]byte // 自愈后的应答帧（单帧或分片），MOCK_MISS 或 Oneway 为空
	Detail         string
}

// MockMissRecord 记录 Mock 未命中事件
type MockMissRecord struct {
	ServiceName string
	Operation   string
	SNO         uint64
	PayloadHash string
}

type mockEntry struct {
	consumed  bool
	exactKey  string
	methodKey string
	dstTag    string
	rsps      [][]byte
}

type scopedPool struct {
	exactMap    map[string][]*mockEntry
	seqQueue    map[string][]*mockEntry
	anyExactMap map[string][]*mockEntry
	anySeqQueue map[string][]*mockEntry
}

func newScopedPool() *scopedPool {
	return &scopedPool{
		exactMap:    make(map[string][]*mockEntry),
		seqQueue:    make(map[string][]*mockEntry),
		anyExactMap: make(map[string][]*mockEntry),
		anySeqQueue: make(map[string][]*mockEntry),
	}
}

func (p *scopedPool) add(exactKey, methodKey, baseExactKey, baseMethodKey, dstTag string, rsps [][]byte) {
	entry := &mockEntry{
		exactKey:  exactKey,
		methodKey: methodKey,
		dstTag:    dstTag,
		rsps:      rsps,
	}
	p.exactMap[exactKey] = append(p.exactMap[exactKey], entry)
	p.seqQueue[methodKey] = append(p.seqQueue[methodKey], entry)
	if dstTag != "" {
		p.anyExactMap[baseExactKey] = append(p.anyExactMap[baseExactKey], entry)
		p.anySeqQueue[baseMethodKey] = append(p.anySeqQueue[baseMethodKey], entry)
	}
}

func popFromMap(m map[string][]*mockEntry, key string) ([][]byte, bool) {
	entries := m[key]
	for i, entry := range entries {
		if !entry.consumed {
			entry.consumed = true
			m[key] = cleanUnconsumed(entries[i+1:])
			return entry.rsps, true
		}
	}
	return nil, false
}

func cleanUnconsumed(entries []*mockEntry) []*mockEntry {
	cleaned := entries[:0]
	for _, entry := range entries {
		if !entry.consumed {
			cleaned = append(cleaned, entry)
		}
	}
	return cleaned
}

func matchInPool(
	pool *scopedPool,
	dstTag, taggedExactKey, baseExactKey, taggedMethodKey, baseMethodKey string,
	allowStubFallback bool,
	exactHit, seqHit MatchResultType,
) ([][]byte, MatchResultType) {
	if pool == nil {
		return nil, ""
	}
	if dstTag != "" {
		if rsps, found := popFromMap(pool.exactMap, taggedExactKey); found {
			return rsps, exactHit
		}
	}
	if rsps, found := popFromMap(pool.exactMap, baseExactKey); found {
		return rsps, exactHit
	}
	if allowStubFallback {
		if rsps, found := popFromMap(pool.anyExactMap, baseExactKey); found {
			return rsps, exactHit
		}
	}
	if dstTag != "" {
		if rsps, found := popFromMap(pool.seqQueue, taggedMethodKey); found {
			return rsps, seqHit
		}
	}
	if rsps, found := popFromMap(pool.seqQueue, baseMethodKey); found {
		return rsps, seqHit
	}
	if allowStubFallback {
		if rsps, found := popFromMap(pool.anySeqQueue, baseMethodKey); found {
			return rsps, seqHit
		}
	}
	return nil, ""
}

type observedCallKey struct {
	serviceName string
	operation   string
	dstAddr     string
	matchStatus string
}

// ScopedMockMatcher 零 .pb 纯二进制 Mock 分层匹配器
type ScopedMockMatcher struct {
	mu             sync.Mutex
	activeRecordID uint64
	scopedMocks    map[uint64]*scopedPool
	globalFallback *scopedPool
	mngMocks       map[uint8][]byte  // msgType -> raw mng packet
	endpointTags   map[string]string // "IP:Port" -> DstTag
	stubEndpoints  map[string]bool   // ephemeral stub endpoints (e.g. PUB_STUB MockPort)
	mockHitCount   int
	mockMissCount  int
	missRecords    []MockMissRecord
	observedCalls  map[observedCallKey]int
	observedOrder  []observedCallKey
}

// NewScopedMockMatcher 创建匹配器
func NewScopedMockMatcher() *ScopedMockMatcher {
	return &ScopedMockMatcher{
		scopedMocks:    make(map[uint64]*scopedPool),
		globalFallback: newScopedPool(),
		mngMocks:       make(map[uint8][]byte),
		endpointTags:   make(map[string]string),
		stubEndpoints:  make(map[string]bool),
		observedCalls:  make(map[observedCallKey]int),
	}
}

// AddStubEndpoint 注册由 Agent 临时接管发布的本地 Stub 端点地址（如 PUB_STUB 监听口）
func (m *ScopedMockMatcher) AddStubEndpoint(addr string) {
	if addr == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stubEndpoints == nil {
		m.stubEndpoints = make(map[string]bool)
	}
	m.stubEndpoints[addr] = true
}

// SetEndpointTags 设置网络端点到进程 DstTag 的映射 (IP:Port -> DstTag)
func (m *ScopedMockMatcher) SetEndpointTags(tags map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.endpointTags = make(map[string]string, len(tags))
	for k, v := range tags {
		m.endpointTags[k] = v
	}
}

// SetActiveRecordID 设定当前活跃入站请求的 record_id
func (m *ScopedMockMatcher) SetActiveRecordID(recordID uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeRecordID = recordID
}

// AddScopedMock 录入专属局部 Mock
func (m *ScopedMockMatcher) AddScopedMock(recordID uint64, service, operation string, reqPayload []byte, rsps [][]byte) {
	m.AddScopedMockWithTag(recordID, service, operation, "", reqPayload, rsps)
}

// AddScopedMockWithTag 录入带 DstTag 隔离标签的专属局部 Mock (M20)
func (m *ScopedMockMatcher) AddScopedMockWithTag(recordID uint64, service, operation, dstTag string, reqPayload []byte, rsps [][]byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	pool, ok := m.scopedMocks[recordID]
	if !ok {
		pool = newScopedPool()
		m.scopedMocks[recordID] = pool
	}

	baseMethodKey := service + ":" + operation
	methodKey := baseMethodKey
	if dstTag != "" {
		methodKey = methodKey + "@" + dstTag
	}
	hash := sha256.Sum256(reqPayload)
	hashHex := hex.EncodeToString(hash[:])
	baseExactKey := baseMethodKey + ":" + hashHex
	exactKey := methodKey + ":" + hashHex
	pool.add(exactKey, methodKey, baseExactKey, baseMethodKey, dstTag, rsps)
}

// AddGlobalFallback 录入全局兜底 Mock
func (m *ScopedMockMatcher) AddGlobalFallback(service, operation string, reqPayload []byte, rsps [][]byte) {
	m.AddGlobalFallbackWithTag(service, operation, "", reqPayload, rsps)
}

// AddGlobalFallbackWithTag 录入带 DstTag 隔离标签的全局兜底 Mock (M20)
func (m *ScopedMockMatcher) AddGlobalFallbackWithTag(service, operation, dstTag string, reqPayload []byte, rsps [][]byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	baseMethodKey := service + ":" + operation
	methodKey := baseMethodKey
	if dstTag != "" {
		methodKey = methodKey + "@" + dstTag
	}
	hash := sha256.Sum256(reqPayload)
	hashHex := hex.EncodeToString(hash[:])
	baseExactKey := baseMethodKey + ":" + hashHex
	exactKey := methodKey + ":" + hashHex
	m.globalFallback.add(exactKey, methodKey, baseExactKey, baseMethodKey, dstTag, rsps)
}

// RegisterScopedDownstreamRule 解析出站请求帧与应答帧列表，注册到指定 RecordID 的专属局部作用域 (ADR-12, M13, M20)
func (m *ScopedMockMatcher) RegisterScopedDownstreamRule(recordID uint64, rule agentproto.MockRuleDownstream) error {
	if len(rule.ReqPacket) < crosrpc.CommHeadLen {
		return ErrFrameTooShort
	}
	parsed, err := crosrpc.ParsePacket(rule.ReqPacket)
	if err != nil {
		return err
	}
	svc, op := "", ""
	if parsed.Req != nil {
		svc = parsed.Req.ServiceName
		op = parsed.Req.Operation
	} else if parsed.Rsp != nil {
		svc = parsed.Rsp.ServiceName
		op = parsed.Rsp.Operation
	}
	if svc == "" {
		return ErrInvalidFrame
	}
	var payload []byte
	if len(rule.ReqPacket) > parsed.PayloadOffset {
		payload = rule.ReqPacket[parsed.PayloadOffset:]
	}
	m.AddScopedMockWithTag(recordID, svc, op, rule.DstTag, payload, rule.RspPackets)
	return nil
}

// RegisterGlobalFallbackRule 解析出站请求帧与应答帧列表，注册到全局兜底作用域 (ADR-12, M13, M20)
func (m *ScopedMockMatcher) RegisterGlobalFallbackRule(rule agentproto.MockRuleDownstream) error {
	if len(rule.ReqPacket) < crosrpc.CommHeadLen {
		return ErrFrameTooShort
	}
	parsed, err := crosrpc.ParsePacket(rule.ReqPacket)
	if err != nil {
		return err
	}
	svc, op := "", ""
	if parsed.Req != nil {
		svc = parsed.Req.ServiceName
		op = parsed.Req.Operation
	} else if parsed.Rsp != nil {
		svc = parsed.Rsp.ServiceName
		op = parsed.Rsp.Operation
	}
	if svc == "" {
		return ErrInvalidFrame
	}
	var payload []byte
	if len(rule.ReqPacket) > parsed.PayloadOffset {
		payload = rule.ReqPacket[parsed.PayloadOffset:]
	}
	m.AddGlobalFallbackWithTag(svc, op, rule.DstTag, payload, rule.RspPackets)
	return nil
}

// AddMngMock 录入管理类消息
func (m *ScopedMockMatcher) AddMngMock(msgType uint8, rawMng []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mngMocks[msgType] = rawMng
}

// MatchAndHeal 针对 liveReq 执行匹配与表驱动头部自愈
func (m *ScopedMockMatcher) MatchAndHeal(liveReq []byte) (*MatchResult, error) {
	return m.MatchAndHealWithDst(liveReq, "")
}

// MatchAndHealWithDst 针对 liveReq 附带目的地址执行匹配与表驱动头部自愈，并记录 ObservedEgressCall (ADR-14, M17)
func (m *ScopedMockMatcher) MatchAndHealWithDst(liveReq []byte, dstAddr string) (*MatchResult, error) {
	return m.MatchAndHealWithDstTag(liveReq, dstAddr, "")
}

// MatchAndHealWithDstTag 针对 liveReq 附带目的地址与目的标签执行多实现隔离匹配与自愈 (ADR-16, M20)
func (m *ScopedMockMatcher) MatchAndHealWithDstTag(liveReq []byte, dstAddr, dstTag string) (*MatchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if dstTag == "" && m.endpointTags != nil && dstAddr != "" {
		if tag, ok := m.endpointTags[dstAddr]; ok {
			dstTag = tag
		}
	}

	if len(liveReq) < crosrpc.CommHeadLen {
		return nil, ErrFrameTooShort
	}

	msgType := liveReq[22]

	// 1. 管理会话 (MNG) 消息处理
	if msgType == crosrpc.MsgTypeMng || msgType == crosrpc.MsgTypeMngReq || msgType == crosrpc.MsgTypeUUIDReq {
		if rawMng, ok := m.mngMocks[msgType]; ok {
			healed := make([]byte, len(rawMng))
			copy(healed, rawMng)
			copy(healed[8:16], liveReq[8:16]) // 覆写 SNO
			if healed[7] > crosrpc.TransportTCP {
				healed[7] = crosrpc.TransportTCP
			}
			m.mockHitCount++
			return &MatchResult{
				Result:         MatchHitMng,
				ResponseFrames: [][]byte{healed},
			}, nil
		}
		// 没有录制 MNG 则安全忽略
		return &MatchResult{Result: MatchHitMng}, nil
	}

	// 2. 仅对 REQ (MsgType == 2) 进行匹配
	if msgType != crosrpc.MsgTypeReq {
		return &MatchResult{Result: MatchMiss}, nil
	}

	// 提取请求类型
	var reqType uint8
	if len(liveReq) > 24+136 {
		reqType = liveReq[24+136]
	}

	// 提取 ServiceName 与 Operation
	var serviceName, operation string
	if len(liveReq) >= 24+136 {
		serviceName = string(bytes.TrimRight(liveReq[24+8:24+72], "\x00"))
		operation = string(bytes.TrimRight(liveReq[24+72:24+136], "\x00"))
	}
	baseMethodKey := serviceName + ":" + operation

	// 单向调用 (Oneway) 仅记录, 不回包
	if reqType == crosrpc.ReqTypeOneway {
		m.mockHitCount++
		if serviceName != "" {
			m.recordObservedCallLocked(serviceName, operation, dstAddr, string(MatchOneway))
		}
		return &MatchResult{
			Result:      MatchOneway,
			ServiceName: serviceName,
			Operation:   operation,
		}, nil
	}

	// 计算 Payload SHA256
	headLen := int(liveReq[20])<<8 | int(liveReq[21])
	if headLen == 0 && len(liveReq) >= 28 {
		headLen = int(liveReq[26])<<8 | int(liveReq[27])
	}
	payloadOffset := 24 + headLen
	var payload []byte
	if len(liveReq) > payloadOffset {
		payload = liveReq[payloadOffset:]
	}
	hash := sha256.Sum256(payload)
	hashHex := hex.EncodeToString(hash[:])
	baseExactKey := baseMethodKey + ":" + hashHex

	taggedMethodKey := baseMethodKey
	taggedExactKey := baseExactKey
	if dstTag != "" {
		taggedMethodKey = baseMethodKey + "@" + dstTag
		taggedExactKey = taggedMethodKey + ":" + hashHex
	}

	allowStubFallback := dstTag == "" && dstAddr != "" && m.stubEndpoints[dstAddr]

	// 3. 检查专属局部池
	matchedRsps, hitType := matchInPool(
		m.scopedMocks[m.activeRecordID],
		dstTag, taggedExactKey, baseExactKey, taggedMethodKey, baseMethodKey,
		allowStubFallback,
		MatchHitScoped, MatchHitScopedSeq,
	)

	// 4. 专属池未命中，检查全局兜底池
	if matchedRsps == nil {
		matchedRsps, hitType = matchInPool(
			m.globalFallback,
			dstTag, taggedExactKey, baseExactKey, taggedMethodKey, baseMethodKey,
			allowStubFallback,
			MatchHitGlobal, MatchHitGlobal,
		)
	}

	// 5. 命中处理：自愈头部并返回
	if matchedRsps != nil {
		healed, err := HealResponseFrames(liveReq, matchedRsps)
		if err != nil {
			return nil, err
		}
		m.mockHitCount++
		if serviceName != "" {
			m.recordObservedCallLocked(serviceName, operation, dstAddr, string(hitType))
		}
		return &MatchResult{
			Result:         hitType,
			ServiceName:    serviceName,
			Operation:      operation,
			ResponseFrames: healed,
		}, nil
	}

	// 6. 严禁外泄连接真实下游！记录 MOCK_MISS 并保持静默
	m.mockMissCount++
	m.missRecords = append(m.missRecords, MockMissRecord{
		ServiceName: serviceName,
		Operation:   operation,
		PayloadHash: hashHex,
	})
	if serviceName != "" {
		m.recordObservedCallLocked(serviceName, operation, dstAddr, string(MatchMiss))
	}

	return &MatchResult{
		Result:      MatchMiss,
		ServiceName: serviceName,
		Operation:   operation,
	}, nil
}

func (m *ScopedMockMatcher) recordObservedCallLocked(serviceName, operation, dstAddr, matchStatus string) {
	key := observedCallKey{
		serviceName: serviceName,
		operation:   operation,
		dstAddr:     dstAddr,
		matchStatus: matchStatus,
	}
	if _, exists := m.observedCalls[key]; !exists {
		m.observedOrder = append(m.observedOrder, key)
	}
	m.observedCalls[key]++
}

// ObservedEgressCalls 导出重放期间观测到的所有出站调用汇总 (ADR-14, M17)
func (m *ScopedMockMatcher) ObservedEgressCalls() []agentproto.ObservedEgressCall {
	m.mu.Lock()
	defer m.mu.Unlock()

	calls := make([]agentproto.ObservedEgressCall, 0, len(m.observedOrder))
	for _, k := range m.observedOrder {
		calls = append(calls, agentproto.ObservedEgressCall{
			ServiceName: k.serviceName,
			Operation:   k.operation,
			DstAddr:     k.dstAddr,
			CallCount:   m.observedCalls[k],
			MatchStatus: k.matchStatus,
		})
	}
	return calls
}

// MockHitCount 命中计数
func (m *ScopedMockMatcher) MockHitCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mockHitCount
}

// MockMissCount 未命中计数
func (m *ScopedMockMatcher) MockMissCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mockMissCount
}

// MissRecords 未命中详情
func (m *ScopedMockMatcher) MissRecords() []MockMissRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]MockMissRecord, len(m.missRecords))
	copy(res, m.missRecords)
	return res
}
