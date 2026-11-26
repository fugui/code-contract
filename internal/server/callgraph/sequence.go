package callgraph

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"code-contract/models"
	"gorm.io/gorm"
)

// BuildTransactionSequence 构建单事务的时序泳道响应 DTO
func BuildTransactionSequence(
	db *gorm.DB,
	sessionID uint64,
	rootRecordID uint64,
	replayTaskID uint64,
) (*TransactionSequenceDTO, error) {
	// 1. 加载批次全部记录
	var records []models.TrafficRecord
	query := db.Model(&models.TrafficRecord{}).Where("session_id = ?", sessionID).Order("req_captured_at_ns ASC, seq_no ASC, id ASC")
	if err := query.Find(&records).Error; err != nil {
		return nil, fmt.Errorf("查询会话记录失败: %w", err)
	}

	var session models.RecordingSession
	_ = db.Where("id = ?", sessionID).First(&session).Error

	// 2. 建树并找到指定根事务
	trees := BuildTransactionTrees(db, records, &session, nil)
	var rootGroup *TransactionTreeGroupDTO
	for i := range trees {
		if trees[i].RootRecordID == strconv.FormatUint(rootRecordID, 10) {
			rootGroup = &trees[i]
			break
		}
	}
	if rootGroup == nil {
		return nil, fmt.Errorf("未找到根记录 ID=%d 对应的事务树", rootRecordID)
	}

	// 3. 收集该事务树中包含的全部记录
	recMap := make(map[uint64]*models.TrafficRecord)
	for i := range records {
		recMap[records[i].ID] = &records[i]
	}

	var treeRecords []models.TrafficRecord
	var collectNodes func(n *TransactionTreeNodeDTO)
	collectNodes = func(n *TransactionTreeNodeDTO) {
		id, _ := strconv.ParseUint(n.Record.ID, 10, 64)
		if r, ok := recMap[id]; ok {
			treeRecords = append(treeRecords, *r)
		}
		if n.StitchedIngressRecord != nil {
			sid, _ := strconv.ParseUint(n.StitchedIngressRecord.ID, 10, 64)
			if sr, ok := recMap[sid]; ok {
				treeRecords = append(treeRecords, *sr)
			}
		}
		for i := range n.Children {
			collectNodes(&n.Children[i])
		}
	}
	collectNodes(&rootGroup.Root)

	// 4. 分析参与者
	participantsMap := make(map[string]*SequenceParticipantDTO)
	callerTitle := "外部调用方"
	if rootGroup.Root.Record.PeerProcName != "" {
		callerTitle = rootGroup.Root.Record.PeerProcName
	}
	callerKey := "caller:client"
	participantsMap[callerKey] = &SequenceParticipantDTO{
		ParticipantKey: callerKey,
		Title:          callerTitle,
		Subtitle:       rootGroup.Root.Record.SrcAddr,
		Kind:           "EXTERNAL_CALLER",
	}

	procSubtitleFromIngress := make(map[string]bool)
	for _, r := range treeRecords {
		pKey := fmt.Sprintf("proc:%d", r.ProcessID)
		if existing, exists := participantsMap[pKey]; !exists {
			title := fmt.Sprintf("进程 #%d", r.ProcessID)
			if r.ProcessID > 0 {
				var proc models.Process
				if err := db.Select("id", "arch_process_id").Where("id = ?", r.ProcessID).First(&proc).Error; err == nil && proc.ArchProcessID > 0 {
					var ap models.ArchProcess
					if err := db.Select("id", "name").Where("id = ?", proc.ArchProcessID).First(&ap).Error; err == nil && ap.Name != "" {
						title = ap.Name
					}
				}
			}
			sub, fromIngress := resolveMonitoredProcessSubtitle("", false, r)
			procSubtitleFromIngress[pKey] = fromIngress
			participantsMap[pKey] = &SequenceParticipantDTO{
				ParticipantKey: pKey,
				Title:          title,
				Subtitle:       sub,
				Kind:           "MONITORED_PROCESS",
			}
		} else {
			sub, fromIngress := resolveMonitoredProcessSubtitle(existing.Subtitle, procSubtitleFromIngress[pKey], r)
			existing.Subtitle = sub
			procSubtitleFromIngress[pKey] = fromIngress
		}
	}

	// 5. 加载可选的重放任务结果覆盖
	replayItemMap := make(map[uint64]*models.ReplayItem)
	if replayTaskID > 0 {
		var replayItems []models.ReplayItem
		if err := db.Where("task_id = ?", replayTaskID).Find(&replayItems).Error; err == nil {
			for i := range replayItems {
				if replayItems[i].RecordID != nil {
					replayItemMap[*replayItems[i].RecordID] = &replayItems[i]
				}
			}
		}
	}

	// 6. 生成泳道步骤
	var steps []SequenceStepDTO
	stepIdx := 1
	rootStartNs, _ := computeTimeSpan(&rootGroup.Root, recMap)

	var walkTree func(n *TransactionTreeNodeDTO, parentParticipant string)
	walkTree = func(n *TransactionTreeNodeDTO, parentParticipant string) {
		id, _ := strconv.ParseUint(n.Record.ID, 10, 64)
		rec := recMap[id]

		fromPart := parentParticipant
		toPart := fmt.Sprintf("proc:%d", rec.ProcessID)
		if rec.Direction == "EGRESS" {
			fromPart = fmt.Sprintf("proc:%d", rec.ProcessID)
			if n.StitchedIngressRecord != nil {
				sid, _ := strconv.ParseUint(n.StitchedIngressRecord.ID, 10, 64)
				srec := recMap[sid]
				toPart = fmt.Sprintf("proc:%d", srec.ProcessID)
			} else {
				toPart = fmt.Sprintf("downstream:%s", rec.ServiceName)
				if _, exists := participantsMap[toPart]; !exists {
					participantsMap[toPart] = &SequenceParticipantDTO{
						ParticipantKey: toPart,
						Title:          rec.ServiceName,
						Subtitle:       rec.DstAddr,
						Kind:           "EXTERNAL_DOWNSTREAM",
					}
				}
			}
		}

		offsetUs := (rec.ReqCapturedAtNs - rootStartNs) / 1000
		if offsetUs < 0 {
			offsetUs = 0
		}
		durationUs := int64(0)
		if rec.LatencyUs != nil {
			durationUs = *rec.LatencyUs
		}

		// 阶段耗时
		var stageTiming *SequenceStageTimingDTO
		if len(rec.HeadersJSON) > 0 {
			var hw struct {
				Timing *struct {
					NetworkAndFramingUs *int64 `json:"network_and_framing_us"`
					QueueWaitUs         *int64 `json:"queue_wait_us"`
					ServerExecUs        *int64 `json:"server_exec_us"`
				} `json:"timing"`
			}
			if err := json.Unmarshal(rec.HeadersJSON, &hw); err == nil && hw.Timing != nil {
				stageTiming = &SequenceStageTimingDTO{
					NetworkAndFramingUs: hw.Timing.NetworkAndFramingUs,
					QueueWaitUs:         hw.Timing.QueueWaitUs,
					ServerExecUs:        hw.Timing.ServerExecUs,
				}
			}
		}

		// 重放叠加
		var replayOverlay *SequenceReplayOverlayDTO
		if ri, ok := replayItemMap[rec.ID]; ok {
			diffCnt := 0
			if len(ri.FieldDiffs) > 0 {
				var diffList []interface{}
				if err := json.Unmarshal(ri.FieldDiffs, &diffList); err == nil {
					diffCnt = len(diffList)
				}
			}
			summary := ri.ErrorMessage
			if summary == "" && diffCnt > 0 {
				summary = fmt.Sprintf("%d 处字段差异", diffCnt)
			}
			replayOverlay = &SequenceReplayOverlayDTO{
				Status:    ri.ResultStatus,
				DiffCount: &diffCnt,
				Summary:   summary,
			}
		}

		step := SequenceStepDTO{
			StepIndex:             stepIdx,
			Record:                n.Record,
			StitchedIngressRecord: n.StitchedIngressRecord,
			FromParticipant:       fromPart,
			ToParticipant:         toPart,
			SiblingRelation:       n.SiblingRelation,
			OffsetUs:              offsetUs,
			DurationUs:            durationUs,
			StageTiming:           stageTiming,
			ReplayOverlay:         replayOverlay,
		}
		steps = append(steps, step)
		stepIdx++

		for i := range n.Children {
			walkTree(&n.Children[i], toPart)
		}
	}

	walkTree(&rootGroup.Root, callerKey)

	// 7. 计算血缘边与实体链
	lineages := ExtractParameterLineage(treeRecords)
	entityChains := BuildEntityChains(treeRecords)

	// 过滤幽灵参与者并按调用拓扑尽量从左到右排序
	participants := orderSequenceParticipants(participantsMap, steps)

	if lineages == nil {
		lineages = []ParameterLineageEdgeDTO{}
	}
	if entityChains == nil {
		entityChains = []EntityLifecycleChainDTO{}
	}
	if steps == nil {
		steps = []SequenceStepDTO{}
	}

	return &TransactionSequenceDTO{
		RootRecordID:      rootGroup.RootRecordID,
		TraceID:           rootGroup.TraceID,
		TotalDurationUs:   rootGroup.TotalDurationUs,
		Participants:      participants,
		Steps:             steps,
		ParameterLineages: lineages,
		EntityChains:      entityChains,
	}, nil
}

// resolveMonitoredProcessSubtitle 根据流量方向解析被观测进程自身地址，优先保留 INGRESS 的 DstAddr（监听地址）
func resolveMonitoredProcessSubtitle(existing string, existingFromIngress bool, rec models.TrafficRecord) (string, bool) {
	if rec.Direction == "INGRESS" {
		if rec.DstAddr != "" {
			return rec.DstAddr, true
		}
		return existing, existingFromIngress
	}
	if existingFromIngress && existing != "" {
		return existing, true
	}
	candidate := rec.SrcAddr
	if candidate == "" {
		candidate = rec.DstAddr
	}
	if existing == "" && candidate != "" {
		return candidate, false
	}
	return existing, existingFromIngress
}

// orderSequenceParticipants 过滤未在步骤中出现的参与者，并结合 Kahn 拓扑排序与 Eades-Lin-Smyth 破环启发式，
// 使调用方尽量处于被调用方左侧（无环 DAG 100% 零逆向箭头，有环时最小化逆向边权重）。
func orderSequenceParticipants(
	participantsMap map[string]*SequenceParticipantDTO,
	steps []SequenceStepDTO,
) []SequenceParticipantDTO {
	if len(steps) == 0 || len(participantsMap) == 0 {
		return []SequenceParticipantDTO{}
	}

	activeSet := make(map[string]bool)
	firstSeen := make(map[string]int)
	seenCounter := 0
	recordSeen := func(key string) {
		if key == "" {
			return
		}
		if _, ok := participantsMap[key]; !ok {
			return
		}
		if !activeSet[key] {
			activeSet[key] = true
			firstSeen[key] = seenCounter
			seenCounter++
		}
	}

	for _, s := range steps {
		recordSeen(s.FromParticipant)
		recordSeen(s.ToParticipant)
	}
	if len(activeSet) == 0 {
		return []SequenceParticipantDTO{}
	}

	keys := make([]string, 0, len(activeSet))
	for k := range activeSet {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	edgeWeight := make(map[string]map[string]int)
	remInWeight := make(map[string]int, len(activeSet))
	remOutWeight := make(map[string]int, len(activeSet))
	for _, s := range steps {
		u := s.FromParticipant
		v := s.ToParticipant
		if u == "" || v == "" || u == v || !activeSet[u] || !activeSet[v] {
			continue
		}
		if edgeWeight[u] == nil {
			edgeWeight[u] = make(map[string]int)
		}
		edgeWeight[u][v]++
		remOutWeight[u]++
		remInWeight[v]++
	}

	placed := make(map[string]bool, len(activeSet))
	orderedKeys := make([]string, 0, len(activeSet))

	for len(orderedKeys) < len(keys) {
		chosen := ""
		// 1. 优先挑选入度为 0 且首次出现最早的节点
		for _, k := range keys {
			if placed[k] || remInWeight[k] != 0 {
				continue
			}
			if chosen == "" || firstSeen[k] < firstSeen[chosen] || (firstSeen[k] == firstSeen[chosen] && k < chosen) {
				chosen = k
			}
		}

		// 2. 存在环路时，按 Eades-Lin-Smyth 选择 (remOut - remIn) 最大的节点破环
		if chosen == "" {
			for _, k := range keys {
				if placed[k] {
					continue
				}
				if chosen == "" {
					chosen = k
					continue
				}
				diffK := remOutWeight[k] - remInWeight[k]
				diffC := remOutWeight[chosen] - remInWeight[chosen]
				if diffK != diffC {
					if diffK > diffC {
						chosen = k
					}
					continue
				}
				if remOutWeight[k] != remOutWeight[chosen] {
					if remOutWeight[k] > remOutWeight[chosen] {
						chosen = k
					}
					continue
				}
				if firstSeen[k] != firstSeen[chosen] {
					if firstSeen[k] < firstSeen[chosen] {
						chosen = k
					}
					continue
				}
				if k < chosen {
					chosen = k
				}
			}
		}

		placed[chosen] = true
		orderedKeys = append(orderedKeys, chosen)

		for v, w := range edgeWeight[chosen] {
			if !placed[v] {
				remInWeight[v] -= w
			}
		}
		for _, u := range keys {
			if !placed[u] {
				if w := edgeWeight[u][chosen]; w > 0 {
					remOutWeight[u] -= w
				}
			}
		}
	}

	calcBackwardWeight := func(order []string) int {
		pos := make(map[string]int, len(order))
		for idx, k := range order {
			pos[k] = idx
		}
		bw := 0
		for u, targets := range edgeWeight {
			pu := pos[u]
			for v, w := range targets {
				if pu > pos[v] {
					bw += w
				}
			}
		}
		return bw
	}

	// 3. 存在逆向边且规模可控时，通过局部移位进一步压低逆向边权重
	if len(orderedKeys) <= 64 && calcBackwardWeight(orderedKeys) > 0 {
		for round := 0; round < len(orderedKeys); round++ {
			curBW := calcBackwardWeight(orderedKeys)
			if curBW == 0 {
				break
			}
			bestBW := curBW
			bestFrom := -1
			bestTo := -1

			for fromIdx := 0; fromIdx < len(orderedKeys); fromIdx++ {
				for toIdx := 0; toIdx < len(orderedKeys); toIdx++ {
					if fromIdx == toIdx {
						continue
					}
					candidate := make([]string, 0, len(orderedKeys))
					moving := orderedKeys[fromIdx]
					for i, k := range orderedKeys {
						if i != fromIdx {
							candidate = append(candidate, k)
						}
					}
					candidate = append(candidate, "")
					copy(candidate[toIdx+1:], candidate[toIdx:])
					candidate[toIdx] = moving

					bw := calcBackwardWeight(candidate)
					if bw < bestBW {
						bestBW = bw
						bestFrom = fromIdx
						bestTo = toIdx
					}
				}
			}

			if bestFrom < 0 {
				break
			}
			moving := orderedKeys[bestFrom]
			nextOrder := make([]string, 0, len(orderedKeys))
			for i, k := range orderedKeys {
				if i != bestFrom {
					nextOrder = append(nextOrder, k)
				}
			}
			nextOrder = append(nextOrder, "")
			copy(nextOrder[bestTo+1:], nextOrder[bestTo:])
			nextOrder[bestTo] = moving
			orderedKeys = nextOrder
		}
	}

	participants := make([]SequenceParticipantDTO, 0, len(orderedKeys))
	for _, k := range orderedKeys {
		participants = append(participants, *participantsMap[k])
	}
	return participants
}
