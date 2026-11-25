package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"code-contract/internal/server/decoder"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	AttributionSingleModuleProc = "SINGLE_MODULE_PROC"
	AttributionTraceSpan        = "TRACE_SPAN"
	AttributionUniqueDepend     = "UNIQUE_DEPEND"
	AttributionProcessLevel     = "PROCESS_LEVEL"

	PeerAttributionModeResolved  = "RESOLVED"
	PeerAttributionModeAmbiguous = "AMBIGUOUS"
)

// ResolveDestinationModule 依据全局唯一供给原则 (A-05) 穿透定位目标功能模块 (Service 级别)
func ResolveDestinationModule(provideMap map[string]uint64, serviceName string) *uint64 {
	key := strings.TrimSpace(serviceName)
	if modID, ok := provideMap[key]; ok && modID > 0 {
		res := modID
		return &res
	}
	return nil
}

// ResolveSourceModule 针对进程出站请求，执行三级优先级来源模块判定 (F-05.2)
// 优先级：
// 0. 单模块独占进程：|M(P)| == 1 -> SINGLE_MODULE_PROC
// 1. Trace Span 匹配：FatherSpanID 匹配同批次该进程入站请求 SpanID -> TRACE_SPAN
// 2. 唯一 DEPEND 声明：M(P) 中仅有 1 个模块声明了对该 Service 的 DEPEND -> UNIQUE_DEPEND
// 3. 歧义或未声明：返回 PROCESS_LEVEL，SrcModule=nil，candidates 为 M(P)
func ResolveSourceModule(
	processModules map[uint64][]uint64,
	ingressReqsBySpanID map[string]*models.TrafficRecord,
	egressRecord *models.TrafficRecord,
	dependMap map[uint64]map[string]bool,
) (srcModuleID *uint64, mode string, candidates []uint64) {
	if egressRecord == nil {
		return nil, AttributionProcessLevel, nil
	}

	modules := processModules[egressRecord.ProcessID]
	if len(modules) == 0 {
		return nil, AttributionProcessLevel, nil
	}

	// 0. 单模块独占进程前置收敛
	if len(modules) == 1 {
		modID := modules[0]
		return &modID, AttributionSingleModuleProc, nil
	}

	// 1. 优先级 ①: DOTR 扩展头存在非零 TraceID 与 FatherSpanID
	fatherSpan := strings.TrimSpace(egressRecord.FatherSpanID)
	if fatherSpan != "" && fatherSpan != "0" && ingressReqsBySpanID != nil {
		if parentReq, ok := ingressReqsBySpanID[fatherSpan]; ok && parentReq != nil {
			if parentReq.DstModuleID != nil && *parentReq.DstModuleID > 0 {
				dstID := *parentReq.DstModuleID
				// 校验该 DstModule 属于当前进程所承载的模块
				for _, m := range modules {
					if m == dstID {
						return &dstID, AttributionTraceSpan, nil
					}
				}
			}
		}
	}

	// 2. 优先级 ②: 过滤 M(P) 中声明了 DEPEND(svc) 的子集
	callKey := strings.TrimSpace(egressRecord.ServiceName)
	var depCandidates []uint64
	if dependMap != nil {
		for _, m := range modules {
			if mDeps, ok := dependMap[m]; ok && mDeps[callKey] {
				depCandidates = append(depCandidates, m)
			}
		}
	}

	if len(depCandidates) == 1 {
		modID := depCandidates[0]
		return &modID, AttributionUniqueDepend, nil
	}

	// 3. 优先级 ③: 歧义或均未声明，降级为进程级归属
	return nil, AttributionProcessLevel, modules
}

// OfflineBatchAttribution 在录制批次停止后，统一归集全链路报文并执行 Stage 1 物理进程归因与 Stage 2 逻辑模块结算
func OfflineBatchAttribution(ctx context.Context, db *gorm.DB, sessionID uint64) error {
	return OfflineBatchAttributionWithDecoder(ctx, db, sessionID, nil)
}

type stage1PlacementEntry struct {
	ArchProcessID uint64
	AgentID       uint64
	AgentScope    string
	Lifecycle     string
	Status        string
	ServiceName   string
	ProtoSvcName  string
	Endpoints     []string
}

// OfflineBatchAttributionWithDecoder 执行 Stage 1 物理进程级归因（含 4 级优先级与 AMBIGUOUS 候选保存）、.pb 字段级解码及 Stage 2 逻辑模块归因
func OfflineBatchAttributionWithDecoder(ctx context.Context, db *gorm.DB, sessionID uint64, dec *decoder.Registry) error {
	// 1. 加载契约与服务解析器 (支持独占服务与多实现契约解析)
	resolver, err := LoadContractResolver(ctx, db)
	if err != nil {
		return err
	}

	// 2. 加载全局 DEPEND 映射: moduleID -> map[serviceName]bool (包含 DISPATCH 契约依赖)
	var deps []models.ModuleDependency
	if err := db.WithContext(ctx).Preload("Service").Preload("Contract").Find(&deps).Error; err != nil {
		return err
	}
	dependMap := make(map[uint64]map[string]bool)
	for _, dep := range deps {
		if dependMap[dep.ConsumerModuleID] == nil {
			dependMap[dep.ConsumerModuleID] = make(map[string]bool)
		}
		if dep.Service != nil {
			key := strings.TrimSpace(dep.Service.ServiceName)
			dependMap[dep.ConsumerModuleID][key] = true
		} else if dep.Contract != nil {
			key := strings.TrimSpace(dep.Contract.ServiceName)
			dependMap[dep.ConsumerModuleID][key] = true
			// 若各实现模块使用了自定义运行期服务名（如 DH_SyncService），同步展开至源模块归因匹配集合
			if dep.ContractID != nil {
				if cInfo, ok := resolver.contractsByID[*dep.ContractID]; ok {
					for _, impl := range cInfo.Implementations {
						if impl.ServiceName != "" {
							dependMap[dep.ConsumerModuleID][impl.ServiceName] = true
						}
					}
				}
			}
		} else if dep.IsExternal && dep.ExternalService != "" {
			dependMap[dep.ConsumerModuleID][strings.TrimSpace(dep.ExternalService)] = true
		}
	}

	// 3. 加载进程承载模块映射 (通过 ArchProcessID 映射) 与 Stage 1 物理放置索引
	var allProcs []models.Process
	if err := db.WithContext(ctx).Find(&allProcs).Error; err != nil {
		return err
	}
	var allModules []models.Module
	if err := db.WithContext(ctx).Where("arch_process_id IS NOT NULL").Find(&allModules).Error; err != nil {
		return err
	}
	archModules := make(map[uint64][]uint64)
	for _, m := range allModules {
		if m.ArchProcessID != nil {
			archModules[*m.ArchProcessID] = append(archModules[*m.ArchProcessID], m.ID)
		}
	}
	processModules := make(map[uint64][]uint64)
	procArchMap := make(map[uint64]uint64)
	envProcesses := make(map[string]uint64)
	for _, p := range allProcs {
		p.FillFromArchProcess()
		procArchMap[p.ID] = p.ArchProcessID
		if mods, ok := archModules[p.ArchProcessID]; ok {
			processModules[p.ID] = mods
		}
		if p.EffectiveListenPort > 0 && p.ArchProcessID > 0 {
			if p.AdvertiseIP != "" {
				envProcesses[fmt.Sprintf("%s:%d", p.AdvertiseIP, p.EffectiveListenPort)] = p.ArchProcessID
			}
			envProcesses[fmt.Sprintf("127.0.0.1:%d", p.EffectiveListenPort)] = p.ArchProcessID
		}
	}

	var allPlacements []models.ServicePlacement
	_ = db.WithContext(ctx).
		Preload("ModuleService").
		Preload("Agent").
		Preload("ArchProcess").
		Where("status <> ?", models.PlacementStatusRetired).
		Find(&allPlacements).Error

	placementsByService := make(map[string][]stage1PlacementEntry)
	for _, pl := range allPlacements {
		if pl.ModuleService == nil {
			continue
		}
		var eps []string
		if len(pl.Endpoints) > 0 {
			_ = json.Unmarshal(pl.Endpoints, &eps)
		}
		scope := models.ScopeShared
		if pl.Agent != nil && pl.Agent.Scope != "" {
			scope = pl.Agent.Scope
		}
		lifecycle := models.ArchProcLifecycleConfirmed
		if pl.ArchProcess != nil && pl.ArchProcess.Lifecycle != "" {
			lifecycle = pl.ArchProcess.Lifecycle
		}
		entry := stage1PlacementEntry{
			ArchProcessID: pl.ArchProcessID,
			AgentID:       pl.AgentID,
			AgentScope:    scope,
			Lifecycle:     lifecycle,
			Status:        pl.Status,
			ServiceName:   strings.TrimSpace(pl.ModuleService.ServiceName),
			ProtoSvcName:  strings.TrimSpace(pl.ModuleService.ProtoServiceName),
			Endpoints:     eps,
		}
		for _, key := range []string{
			entry.ServiceName,
			StripCSGPIDSuffix(entry.ServiceName),
			entry.ProtoSvcName,
		} {
			if key == "" {
				continue
			}
			placementsByService[key] = append(placementsByService[key], entry)
		}
	}

	// 3.5 加载会话级 endpoint_snapshots 与 csg_snapshots
	var session models.RecordingSession
	var snapshots []models.EndpointSnapshotItem
	var csgSnapshots []agentproto.CSGSnapshotRecord
	if err := db.WithContext(ctx).Where("id = ?", sessionID).First(&session).Error; err == nil {
		if len(session.EndpointSnapshots) > 0 {
			_ = json.Unmarshal(session.EndpointSnapshots, &snapshots)
		}
		if len(session.CSGSnapshots) > 0 {
			_ = json.Unmarshal(session.CSGSnapshots, &csgSnapshots)
		}
	}

	// 4. 查询该 session 的所有流量记录
	var records []models.TrafficRecord
	if err := db.WithContext(ctx).Where("session_id = ?", sessionID).Order("id ASC").Find(&records).Error; err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}

	// 5. Stage 1 物理归因 + .pb 解码 + 构建入站 Span 索引
	ingressMap := make(map[string]*models.TrafficRecord)
	for i := range records {
		rec := &records[i]

		// 5.1 Stage 1 本地端物理进程归因
		localArchPID := procArchMap[rec.ProcessID]
		if localArchPID > 0 {
			if rec.Direction == models.TrafficDirEgress && rec.SrcArchProcessID == nil {
				apID := localArchPID
				rec.SrcArchProcessID = &apID
			} else if rec.Direction == models.TrafficDirIngress && rec.DstArchProcessID == nil {
				apID := localArchPID
				rec.DstArchProcessID = &apID
			}
		}

		// 5.2 Stage 1 对端物理进程 4 级优先级解析（不强行猜绑）
		var peerNeedResolve bool
		var peerAddr string
		if rec.Direction == models.TrafficDirEgress && rec.DstArchProcessID == nil {
			peerNeedResolve = true
			peerAddr = rec.DstAddr
		} else if rec.Direction == models.TrafficDirIngress && rec.SrcArchProcessID == nil {
			peerNeedResolve = true
			peerAddr = rec.SrcAddr
		}

		if peerNeedResolve {
			resolvedPeerArchID, candidates := resolveStage1PeerArchProcess(
				rec.AgentID,
				rec.ServiceName,
				peerAddr,
				csgSnapshots,
				placementsByService,
			)
			if resolvedPeerArchID != nil {
				if rec.Direction == models.TrafficDirEgress {
					rec.DstArchProcessID = resolvedPeerArchID
				} else {
					rec.SrcArchProcessID = resolvedPeerArchID
				}
			} else if len(candidates) > 1 {
				rec.HeadersJSON = mergeAmbiguousPeerMeta(rec.HeadersJSON, candidates)
			}
		}

		// 5.3 Stage 1 .pb 字段级同步解码（当此前未解码且存在注册表时）
		if dec != nil && len(rec.RawReqPacket) > 0 &&
			(rec.DecodeStatus == "" || rec.DecodeStatus == decoder.DecodeStatusNoSchemaRawHex || rec.DecodeStatus == decoder.DecodeStatusDecodeErrRawHex) {
			if reqDec, dErr := dec.DecodeCrosFrame(rec.RawReqPacket, rec.IsTruncated); dErr == nil && reqDec != nil {
				if reqDec.Decode.DecodeStatus == decoder.DecodeStatusDecodedJSON || reqDec.Decode.DecodeStatus == decoder.DecodeStatusSuccess {
					var reqObj interface{}
					if len(reqDec.Decode.JSONBytes) > 0 {
						_ = json.Unmarshal(reqDec.Decode.JSONBytes, &reqObj)
					}
					var rspObj interface{}
					if len(rec.RawRspPacket) > 0 {
						if rspDec, rErr := dec.DecodeCrosFrame(rec.RawRspPacket, rec.IsTruncated); rErr == nil && rspDec != nil &&
							(rspDec.Decode.DecodeStatus == decoder.DecodeStatusDecodedJSON || rspDec.Decode.DecodeStatus == decoder.DecodeStatusSuccess) && len(rspDec.Decode.JSONBytes) > 0 {
							_ = json.Unmarshal(rspDec.Decode.JSONBytes, &rspObj)
						}
					}
					pBytes, _ := json.Marshal(map[string]interface{}{
						"req": reqObj,
						"rsp": rspObj,
					})
					rec.PayloadJSON = datatypes.JSON(pBytes)
					rec.DecodeStatus = reqDec.Decode.DecodeStatus
					rec.SchemaID = reqDec.Decode.SchemaID
					rec.SchemaVersion = reqDec.Decode.SchemaVersion
				}
			}
		}

		// 5.4 Stage 2 目标逻辑模块与契约解析
		targetArchPID := procArchMap[rec.ProcessID]
		dstID, dstContractID := resolver.ResolveDestinationFull(
			rec.ServiceName,
			rec.Direction,
			targetArchPID,
			rec.DstAddr,
			snapshots,
			envProcesses,
			nil,
		)
		if dstID != nil {
			rec.DstModuleID = dstID
		}
		if dstContractID != nil {
			rec.DstContractID = dstContractID
		}

		if rec.Direction == models.TrafficDirIngress && rec.SpanID != "" && rec.SpanID != "0" {
			ingressMap[rec.SpanID] = rec
		}
	}

	// 6. 批量处理出站记录的来源模块归属并回写数据库
	for i := range records {
		rec := &records[i]
		if rec.Direction == models.TrafficDirEgress {
			// 如果记录已具备明确有效的来源模块归属（如仿真测试或前序结算），予以保留
			if rec.SrcAttributionMode == "" || rec.SrcModuleID == nil {
				srcID, mode, candidates := ResolveSourceModule(processModules, ingressMap, rec, dependMap)
				rec.SrcModuleID = srcID
				rec.SrcAttributionMode = mode
				if len(candidates) > 0 {
					candBytes, _ := json.Marshal(candidates)
					rec.SrcCandidateModuleIDs = datatypes.JSON(candBytes)
				} else {
					rec.SrcCandidateModuleIDs = nil
				}
			}
		}

		updates := map[string]interface{}{
			"src_arch_process_id":      rec.SrcArchProcessID,
			"dst_arch_process_id":      rec.DstArchProcessID,
			"headers_json":             rec.HeadersJSON,
			"payload_json":             rec.PayloadJSON,
			"decode_status":            rec.DecodeStatus,
			"schema_id":                rec.SchemaID,
			"schema_version":           rec.SchemaVersion,
			"dst_module_id":            rec.DstModuleID,
			"dst_contract_id":          rec.DstContractID,
			"src_module_id":            rec.SrcModuleID,
			"src_attribution_mode":     rec.SrcAttributionMode,
			"src_candidate_module_ids": rec.SrcCandidateModuleIDs,
		}
		if err := db.WithContext(ctx).Model(&models.TrafficRecord{}).
			Where("id = ? AND created_at = ?", rec.ID, rec.CreatedAt).
			Updates(updates).Error; err != nil {
			return err
		}
	}

	return nil
}

func resolveStage1PeerArchProcess(
	agentID uint64,
	serviceName string,
	peerAddr string,
	csgSnapshots []agentproto.CSGSnapshotRecord,
	placementsByService map[string][]stage1PlacementEntry,
) (*uint64, []uint64) {
	normSvc := StripCSGPIDSuffix(strings.TrimSpace(serviceName))
	candidates := placementsByService[strings.TrimSpace(serviceName)]
	if len(candidates) == 0 && normSvc != "" {
		candidates = placementsByService[normSvc]
	}

	peerHP := normalizeStage1HostPort(peerAddr)

	// 优先级 ①: 会话级 CSGSnapshots 端点匹配服务 -> 同环境放置
	if len(csgSnapshots) > 0 && peerHP != "" {
		matchedCSG := false
		for _, snap := range csgSnapshots {
			snapSvc := StripCSGPIDSuffix(strings.TrimSpace(snap.ServiceName))
			if snapSvc != "" && normSvc != "" && snapSvc != normSvc && strings.TrimSpace(snap.ServiceName) != strings.TrimSpace(serviceName) {
				continue
			}
			snapHP := normalizeStage1HostPort(snap.Addr)
			if snapHP == "" && snap.HostIP != "" && snap.Port > 0 {
				snapHP = fmt.Sprintf("%s:%d", snap.HostIP, snap.Port)
			}
			if snapHP != "" && matchHostPort(snapHP, peerHP) {
				matchedCSG = true
				break
			}
		}
		if matchedCSG {
			var matchedArch []uint64
			for _, pl := range candidates {
				if pl.AgentID == agentID && placementMatchesHostPort(pl.Endpoints, peerHP) {
					matchedArch = append(matchedArch, pl.ArchProcessID)
				}
			}
			matchedArch = uniqueUint64Slice(matchedArch)
			if len(matchedArch) == 1 {
				id := matchedArch[0]
				return &id, nil
			}
		}
	}

	// 优先级 ②: 报文对端 IP:Port 匹配同环境放置 endpoints（次选其他权威环境唯一放置端点）
	if peerHP != "" && len(candidates) > 0 {
		var sameEnvMatches []uint64
		var sharedEnvMatches []uint64
		for _, pl := range candidates {
			if !placementMatchesHostPort(pl.Endpoints, peerHP) {
				continue
			}
			if pl.AgentID == agentID {
				sameEnvMatches = append(sameEnvMatches, pl.ArchProcessID)
			}
			if pl.AgentScope == models.ScopeShared && pl.Lifecycle == models.ArchProcLifecycleConfirmed {
				sharedEnvMatches = append(sharedEnvMatches, pl.ArchProcessID)
			}
		}
		sameEnvMatches = uniqueUint64Slice(sameEnvMatches)
		if len(sameEnvMatches) == 1 {
			id := sameEnvMatches[0]
			return &id, nil
		}
		sharedEnvMatches = uniqueUint64Slice(sharedEnvMatches)
		if len(sharedEnvMatches) == 1 {
			id := sharedEnvMatches[0]
			return &id, nil
		}
	}

	// 优先级 ③: 服务身份在权威 SHARED 环境恰有 1 个 ACTIVE 放置的 ArchProcess
	var sharedActiveArch []uint64
	for _, pl := range candidates {
		if pl.AgentScope == models.ScopeShared &&
			pl.Lifecycle == models.ArchProcLifecycleConfirmed &&
			pl.Status == models.PlacementStatusActive {
			sharedActiveArch = append(sharedActiveArch, pl.ArchProcessID)
		}
	}
	sharedActiveArch = uniqueUint64Slice(sharedActiveArch)
	if len(sharedActiveArch) == 1 {
		id := sharedActiveArch[0]
		return &id, nil
	}

	// 优先级 ④: 不唯一时绝不猜绑，透出候选集供解剖抽屉展示
	if len(sharedActiveArch) > 1 {
		return nil, sharedActiveArch
	}
	var allActive []uint64
	for _, pl := range candidates {
		if pl.Status == models.PlacementStatusActive && pl.AgentID == agentID {
			allActive = append(allActive, pl.ArchProcessID)
		}
	}
	allActive = uniqueUint64Slice(allActive)
	if len(allActive) > 1 {
		return nil, allActive
	}
	return nil, nil
}

func normalizeStage1HostPort(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "tcp://ipv4:")
	s = strings.TrimPrefix(s, "dophi_mesh://ipv4:")
	s = strings.TrimPrefix(s, "tcp://")
	s = strings.TrimPrefix(s, "dophi_mesh://")
	if idx := strings.Index(s, "@"); idx >= 0 {
		s = s[:idx]
	}
	return s
}

func matchHostPort(a, b string) bool {
	if a == b {
		return true
	}
	ha, pa, okA := splitHostPortSafe(a)
	hb, pb, okB := splitHostPortSafe(b)
	if !okA || !okB || pa != pb {
		return false
	}
	if ha == hb {
		return true
	}
	return ha == "127.0.0.1" || hb == "127.0.0.1"
}

func splitHostPortSafe(hp string) (string, string, bool) {
	idx := strings.LastIndex(hp, ":")
	if idx <= 0 || idx == len(hp)-1 {
		return "", "", false
	}
	return hp[:idx], hp[idx+1:], true
}

func placementMatchesHostPort(endpoints []string, targetHP string) bool {
	for _, ep := range endpoints {
		norm := normalizeStage1HostPort(ep)
		if norm != "" && matchHostPort(norm, targetHP) {
			return true
		}
	}
	return false
}

func uniqueUint64Slice(in []uint64) []uint64 {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[uint64]struct{}, len(in))
	out := make([]uint64, 0, len(in))
	for _, v := range in {
		if v == 0 {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func mergeAmbiguousPeerMeta(raw datatypes.JSON, candidates []uint64) datatypes.JSON {
	hdr := make(map[string]interface{})
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &hdr)
	}
	meta, ok := hdr["meta"].(map[string]interface{})
	if !ok || meta == nil {
		meta = make(map[string]interface{})
	}
	meta["peer_attribution_mode"] = PeerAttributionModeAmbiguous
	meta["peer_candidate_arch_process_ids"] = candidates
	hdr["meta"] = meta
	b, _ := json.Marshal(hdr)
	return datatypes.JSON(b)
}
