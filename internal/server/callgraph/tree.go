package callgraph

import (
	"regexp"
	"sort"
	"strconv"

	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/gorm"
)

var compensationRegex = regexp.MustCompile(`(?i)^(Cancel|Release|Rollback|Refund|Unlock|Compensate|Revert)`)

// BuildTransactionTrees 构建调用树全流程（向后兼容旧签名）
func BuildTransactionTrees(
	db *gorm.DB,
	records []models.TrafficRecord,
	session *models.RecordingSession,
	filter *TreeFilterOptions,
) []TransactionTreeGroupDTO {
	return BuildTransactionTreesWithStats(db, records, session, filter).Groups
}

// BuildTransactionTreesWithStats 构建调用树并执行噪音标记、先算账后折叠及统计
func BuildTransactionTreesWithStats(
	db *gorm.DB,
	records []models.TrafficRecord,
	session *models.RecordingSession,
	filter *TreeFilterOptions,
) TransactionTreesResult {
	res := TransactionTreesResult{
		Groups:           []TransactionTreeGroupDTO{},
		VisibleRecordIDs: make(map[uint64]bool),
	}
	if len(records) == 0 {
		return res
	}

	// 1. 批量水合
	hydratedList := HydrateRecords(db, session, records)
	hydratedMap := make(map[uint64]*TrafficRecordSummary, len(records))
	recByID := make(map[uint64]*models.TrafficRecord, len(records))
	for i := range records {
		recByID[records[i].ID] = &records[i]
		s := hydratedList[i]
		hydratedMap[records[i].ID] = &s
	}

	// 2. 跨跳缝合 (P1.EGRESS ⇔ P2.INGRESS)
	stitchedIngressMap, netHopMap, confidenceMap, consumedIngressIDs := StitchCrossHops(records, hydratedMap)

	// 3. 构建索引映射以加速父子寻找
	// 按 TraceID 分组 (TraceID != "" && TraceID != "0")
	traceChildren := make(map[string]map[string][]uint64) // TraceID -> FatherSpanID -> []recordID
	// 按 process_id 分组用于时间窗兜底
	procRecords := make(map[uint64][]uint64)

	for _, r := range records {
		if consumedIngressIDs[r.ID] {
			continue
		}
		if r.TraceID != "" && r.TraceID != "0" {
			if traceChildren[r.TraceID] == nil {
				traceChildren[r.TraceID] = make(map[string][]uint64)
			}
			traceChildren[r.TraceID][r.FatherSpanID] = append(traceChildren[r.TraceID][r.FatherSpanID], r.ID)
		}
		procRecords[r.ProcessID] = append(procRecords[r.ProcessID], r.ID)
	}

	// 4. 识别根节点
	// 根节点是:
	// a. INGRESS 且 (FatherSpanID == "" || FatherSpanID == "0" || 不在同 Trace 内部 SpanID 中)
	// b. 或者孤儿 EGRESS/MNG，且不在任何父 INGRESS 的子节点中
	spanIDsInTrace := make(map[string]map[string]bool)
	for _, r := range records {
		if r.TraceID != "" && r.TraceID != "0" && r.SpanID != "" {
			if spanIDsInTrace[r.TraceID] == nil {
				spanIDsInTrace[r.TraceID] = make(map[string]bool)
			}
			spanIDsInTrace[r.TraceID][r.SpanID] = true
		}
	}

	isRoot := func(r *models.TrafficRecord) bool {
		if consumedIngressIDs[r.ID] {
			return false
		}
		if r.Direction == "INGRESS" {
			if r.FatherSpanID == "" || r.FatherSpanID == "0" {
				return true
			}
			if r.TraceID != "" && !spanIDsInTrace[r.TraceID][r.FatherSpanID] {
				return true
			}
			return false
		}
		// EGRESS: 检查是否能挂载到同进程时间窗或 Trace 父节点
		if r.TraceID != "" && r.TraceID != "0" && r.FatherSpanID != "" && spanIDsInTrace[r.TraceID][r.FatherSpanID] {
			return false
		}
		// 检查是否有同进程时间包含窗的 INGRESS
		hasParentIngress := false
		for _, inID := range procRecords[r.ProcessID] {
			inRec := recByID[inID]
			if inRec.Direction == "INGRESS" && inRec.ID != r.ID {
				inEnd := inRec.ReqCapturedAtNs
				if inRec.RspCapturedAtNs != nil {
					inEnd = *inRec.RspCapturedAtNs
				}
				if inRec.ReqCapturedAtNs-500_000 <= r.ReqCapturedAtNs && r.ReqCapturedAtNs <= inEnd+500_000 {
					hasParentIngress = true
					break
				}
			}
		}
		return !hasParentIngress
	}

	var rootRecords []*models.TrafficRecord
	for i := range records {
		if isRoot(&records[i]) {
			rootRecords = append(rootRecords, &records[i])
		}
	}

	// 按 req_captured_at_ns ASC, seq_no ASC 排序根节点
	sort.Slice(rootRecords, func(i, j int) bool {
		if rootRecords[i].ReqCapturedAtNs != rootRecords[j].ReqCapturedAtNs {
			return rootRecords[i].ReqCapturedAtNs < rootRecords[j].ReqCapturedAtNs
		}
		if rootRecords[i].SeqNo != rootRecords[j].SeqNo {
			return rootRecords[i].SeqNo < rootRecords[j].SeqNo
		}
		return rootRecords[i].ID < rootRecords[j].ID
	})

	// 5. 递归组装每个根事务树
	visitedGlobal := make(map[uint64]bool)
	var groups []TransactionTreeGroupDTO

	for _, root := range rootRecords {
		if visitedGlobal[root.ID] {
			continue
		}

		visitedInTree := make(map[uint64]bool)
		truncatedCount := 0

		rootNode := buildSubtree(
			root.ID,
			0,
			recByID,
			hydratedMap,
			stitchedIngressMap,
			netHopMap,
			confidenceMap,
			traceChildren,
			procRecords,
			consumedIngressIDs,
			visitedInTree,
			visitedGlobal,
			&truncatedCount,
		)

		// 计算该树的总耗时及起止范围
		minStartNs, maxEndNs := computeTimeSpan(&rootNode, recByID)
		totalNs := maxEndNs - minStartNs
		if totalNs < 1000 {
			totalNs = 1000
		}

		// 归一化瀑布百分比
		normalizeWaterfall(&rootNode, recByID, minStartNs, totalNs)

		// 计算自身的净执行耗时 self_exec_us
		computeSelfExecUs(&rootNode, recByID)

		// 标记关键路径
		markCriticalPath(&rootNode)

		// 标注噪音属性并在需要时隐藏纯噪音根 / 折叠树内噪音子调用
		if filter != nil && filter.NoiseMatcher != nil && !filter.NoiseMatcher.Empty() {
			annotateNoiseFlags(&rootNode, filter.NoiseMatcher)
			if filter.HideNoise {
				if isPureHideableNoiseSubtree(&rootNode) {
					res.HiddenNoiseRootCount++
					res.HiddenNoiseCount += countNodes(&rootNode)
					continue
				}
				foldedCount := foldNoiseChildren(&rootNode)
				res.FoldedNoiseChildCount += foldedCount
				res.HiddenNoiseCount += foldedCount
			}
		}

		// 统计节点信息（基于折叠后可见子树）
		nodeCount := countNodes(&rootNode)
		hasProtoErr := false
		hasBizErr := false
		hasComp := false
		checkTreeFlags(&rootNode, &hasProtoErr, &hasBizErr, &hasComp)

		// 提取实体标签（基于折叠后可见节点，避免噪音污染胶囊条）
		entityTags := extractEntityTagsFromTree(&rootNode, recByID)

		// 过滤高亮与保留祖先链
		hasMatch := applyFilterAndHighlight(&rootNode, filter, entityTags)
		if filter != nil && !hasMatch {
			continue
		}

		collectVisibleNodeIDs(&rootNode, res.VisibleRecordIDs)

		group := TransactionTreeGroupDTO{
			RootRecordID:           rootNode.Record.ID,
			TraceID:                rootNode.Record.TraceID,
			TotalNodeCount:         nodeCount,
			TruncatedChildrenCount: truncatedCount,
			TotalDurationUs:        totalNs / 1000,
			HasProtocolError:       hasProtoErr,
			HasBizError:            hasBizErr,
			HasCompensation:        hasComp,
			EntityTags:             entityTags,
			Root:                   rootNode,
		}
		groups = append(groups, group)
	}

	if groups == nil {
		groups = []TransactionTreeGroupDTO{}
	}
	res.Groups = groups
	return res
}

func buildSubtree(
	recordID uint64,
	depth int,
	recByID map[uint64]*models.TrafficRecord,
	hydratedMap map[uint64]*TrafficRecordSummary,
	stitchedIngressMap map[uint64]*TrafficRecordSummary,
	netHopMap map[uint64]int64,
	confidenceMap map[uint64]string,
	traceChildren map[string]map[string][]uint64,
	procRecords map[uint64][]uint64,
	consumedIngressIDs map[uint64]bool,
	visitedInTree map[uint64]bool,
	visitedGlobal map[uint64]bool,
	truncatedCount *int,
) TransactionTreeNodeDTO {
	visitedInTree[recordID] = true
	visitedGlobal[recordID] = true

	rec := recByID[recordID]
	summary := *hydratedMap[recordID]

	conf := ConfidenceRoot
	if depth > 0 {
		if c, ok := confidenceMap[recordID]; ok {
			conf = c
		} else if rec.TraceID != "" && rec.TraceID != "0" {
			conf = ConfidenceDOTRExact
		} else {
			conf = ConfidenceTemporalInferred
		}
	}

	node := TransactionTreeNodeDTO{
		Record:          summary,
		Depth:           depth,
		LinkConfidence:  conf,
		SiblingRelation: SiblingRoot,
		Children:        make([]TransactionTreeNodeDTO, 0),
	}

	if stitched := stitchedIngressMap[recordID]; stitched != nil {
		node.StitchedIngressRecord = stitched
		hop := netHopMap[recordID]
		node.NetHopUs = &hop
	}

	if depth >= MaxTreeDepth {
		return node
	}

	// 查找子节点:
	var candidateIDs []uint64
	seenCandidate := make(map[uint64]bool)

	// 1. DOTR 权威子节点: FatherSpanID == rec.SpanID
	if rec.TraceID != "" && rec.TraceID != "0" && rec.SpanID != "" {
		if childrenByFather, ok := traceChildren[rec.TraceID]; ok {
			for _, cid := range childrenByFather[rec.SpanID] {
				if !visitedInTree[cid] && !consumedIngressIDs[cid] && !seenCandidate[cid] {
					seenCandidate[cid] = true
					candidateIDs = append(candidateIDs, cid)
				}
			}
		}
	}

	// 2. 时间窗降级子节点: 同进程内无 Trace 或 Trace 不匹配的 EGRESS 属于该 INGRESS
	if rec.Direction == "INGRESS" {
		inEnd := rec.ReqCapturedAtNs
		if rec.RspCapturedAtNs != nil {
			inEnd = *rec.RspCapturedAtNs
		}
		for _, cid := range procRecords[rec.ProcessID] {
			if cid == recordID || visitedInTree[cid] || consumedIngressIDs[cid] || seenCandidate[cid] {
				continue
			}
			candRec := recByID[cid]
			if candRec.Direction == "EGRESS" && (candRec.TraceID == "" || candRec.TraceID == "0" || candRec.TraceID == rec.TraceID) {
				if rec.ReqCapturedAtNs-500_000 <= candRec.ReqCapturedAtNs && candRec.ReqCapturedAtNs <= inEnd+500_000 {
					seenCandidate[cid] = true
					candidateIDs = append(candidateIDs, cid)
				}
			}
		}
	}

	// 排序子候选: 按 ReqCapturedAtNs ASC, SeqNo ASC, ID ASC
	sort.Slice(candidateIDs, func(i, j int) bool {
		rI := recByID[candidateIDs[i]]
		rJ := recByID[candidateIDs[j]]
		if rI.ReqCapturedAtNs != rJ.ReqCapturedAtNs {
			return rI.ReqCapturedAtNs < rJ.ReqCapturedAtNs
		}
		if rI.SeqNo != rJ.SeqNo {
			return rI.SeqNo < rJ.SeqNo
		}
		return rI.ID < rJ.ID
	})

	// 超出 MaxChildrenPerNode 截断
	if len(candidateIDs) > MaxChildrenPerNode {
		*truncatedCount += len(candidateIDs) - MaxChildrenPerNode
		candidateIDs = candidateIDs[:MaxChildrenPerNode]
	}

	// 递归构建子节点并判定同层兄弟关系 (SEQUENTIAL, PARALLEL, COMPENSATION)
	var prevChildEndNs int64
	hasPriorError := false

	for i, cid := range candidateIDs {
		childNode := buildSubtree(
			cid,
			depth+1,
			recByID,
			hydratedMap,
			stitchedIngressMap,
			netHopMap,
			confidenceMap,
			traceChildren,
			procRecords,
			consumedIngressIDs,
			visitedInTree,
			visitedGlobal,
			truncatedCount,
		)

		childRec := recByID[cid]
		childEndNs := childRec.ReqCapturedAtNs
		if childRec.RspCapturedAtNs != nil {
			childEndNs = *childRec.RspCapturedAtNs
		} else if childRec.LatencyUs != nil {
			childEndNs += *childRec.LatencyUs * 1000
		}

		if i == 0 {
			childNode.SiblingRelation = SiblingSequential
		} else {
			// 判断是否补偿
			isCompMethod := compensationRegex.MatchString(childRec.Operation)
			if hasPriorError && isCompMethod {
				childNode.SiblingRelation = SiblingCompensation
			} else if prevChildEndNs > 0 && childRec.ReqCapturedAtNs < prevChildEndNs-50_000 {
				// 重叠超过 50μs 为并发
				childNode.SiblingRelation = SiblingParallel
			} else {
				childNode.SiblingRelation = SiblingSequential
			}
		}

		// 检查当前子节点是否有错误，供后续兄弟判定补偿
		isErr := childRec.StatusCode.String() != "0" || childRec.ExchangeType == "REQ_ONLY" || childNode.Record.BizError
		if isErr {
			hasPriorError = true
		}

		if childEndNs > prevChildEndNs {
			prevChildEndNs = childEndNs
		}

		node.Children = append(node.Children, childNode)
	}

	return node
}

func computeTimeSpan(node *TransactionTreeNodeDTO, recByID map[uint64]*models.TrafficRecord) (int64, int64) {
	id, _ := strconv.ParseUint(node.Record.ID, 10, 64)
	r := recByID[id]
	startNs := r.ReqCapturedAtNs
	endNs := r.ReqCapturedAtNs
	if r.RspCapturedAtNs != nil {
		endNs = *r.RspCapturedAtNs
	} else if r.LatencyUs != nil {
		endNs += *r.LatencyUs * 1000
	} else {
		endNs += 1000
	}

	for i := range node.Children {
		cStart, cEnd := computeTimeSpan(&node.Children[i], recByID)
		if cStart < startNs {
			startNs = cStart
		}
		if cEnd > endNs {
			endNs = cEnd
		}
	}
	return startNs, endNs
}

func normalizeWaterfall(node *TransactionTreeNodeDTO, recByID map[uint64]*models.TrafficRecord, rootStartNs, totalNs int64) {
	id, _ := strconv.ParseUint(node.Record.ID, 10, 64)
	r := recByID[id]

	nodeStart := r.ReqCapturedAtNs
	nodeEnd := r.ReqCapturedAtNs
	if r.RspCapturedAtNs != nil {
		nodeEnd = *r.RspCapturedAtNs
	} else if r.LatencyUs != nil {
		nodeEnd += *r.LatencyUs * 1000
	} else {
		nodeEnd += 1000
	}

	offsetPct := float64(nodeStart-rootStartNs) / float64(totalNs) * 100.0
	if offsetPct < 0 {
		offsetPct = 0
	} else if offsetPct > 100 {
		offsetPct = 100
	}

	widthPct := float64(nodeEnd-nodeStart) / float64(totalNs) * 100.0
	if widthPct < 0.5 {
		widthPct = 0.5
	} else if widthPct > 100 {
		widthPct = 100
	}

	node.WaterfallOffsetPct = mathRound(offsetPct, 2)
	node.WaterfallWidthPct = mathRound(widthPct, 2)

	for i := range node.Children {
		normalizeWaterfall(&node.Children[i], recByID, rootStartNs, totalNs)
	}
}

func mathRound(v float64, decimals int) float64 {
	pow := 1.0
	for i := 0; i < decimals; i++ {
		pow *= 10
	}
	return float64(int64(v*pow+0.5)) / pow
}

func computeSelfExecUs(node *TransactionTreeNodeDTO, recByID map[uint64]*models.TrafficRecord) {
	id, _ := strconv.ParseUint(node.Record.ID, 10, 64)
	r := recByID[id]

	totalUs := int64(0)
	if r.LatencyUs != nil {
		totalUs = *r.LatencyUs
	}

	childSumUs := int64(0)
	for i := range node.Children {
		computeSelfExecUs(&node.Children[i], recByID)
		cid, _ := strconv.ParseUint(node.Children[i].Record.ID, 10, 64)
		cr := recByID[cid]
		if cr.LatencyUs != nil {
			childSumUs += *cr.LatencyUs
		}
	}

	selfUs := totalUs - childSumUs
	if selfUs < 0 {
		selfUs = 0
	}
	node.SelfExecUs = &selfUs
}

func markCriticalPath(node *TransactionTreeNodeDTO) int64 {
	node.IsCriticalPath = true
	if len(node.Children) == 0 {
		if node.Record.LatencyUs != nil {
			return *node.Record.LatencyUs
		}
		return 0
	}

	var maxChildIdx = -1
	var maxChildPathLat int64 = -1

	for i := range node.Children {
		childLat := markCriticalPath(&node.Children[i])
		if childLat > maxChildPathLat {
			maxChildPathLat = childLat
			maxChildIdx = i
		}
	}

	// 仅保留最大耗时子节点为 critical path，其余子树清除
	for i := range node.Children {
		if i != maxChildIdx {
			clearCriticalPath(&node.Children[i])
		}
	}

	selfLat := int64(0)
	if node.Record.LatencyUs != nil {
		selfLat = *node.Record.LatencyUs
	}
	return selfLat + maxChildPathLat
}

func clearCriticalPath(node *TransactionTreeNodeDTO) {
	node.IsCriticalPath = false
	for i := range node.Children {
		clearCriticalPath(&node.Children[i])
	}
}

func countNodes(node *TransactionTreeNodeDTO) int {
	cnt := 1
	for i := range node.Children {
		cnt += countNodes(&node.Children[i])
	}
	return cnt
}

func checkTreeFlags(node *TransactionTreeNodeDTO, hasProtoErr, hasBizErr, hasComp *bool) {
	if node.Record.StatusCode != "0" || node.Record.ExchangeType == "REQ_ONLY" {
		*hasProtoErr = true
	}
	if node.Record.BizError {
		*hasBizErr = true
	}
	if node.SiblingRelation == SiblingCompensation {
		*hasComp = true
	}
	for i := range node.Children {
		checkTreeFlags(&node.Children[i], hasProtoErr, hasBizErr, hasComp)
	}
}

func extractEntityTagsFromTree(node *TransactionTreeNodeDTO, recByID map[uint64]*models.TrafficRecord) []string {
	tagSet := make(map[string]bool)
	collectTags(node, recByID, tagSet)
	tags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	return tags
}

func collectTags(node *TransactionTreeNodeDTO, recByID map[uint64]*models.TrafficRecord, tagSet map[string]bool) {
	id, _ := strconv.ParseUint(node.Record.ID, 10, 64)
	if r, ok := recByID[id]; ok {
		produced, consumed := ExtractEntitiesFromMeta(r.HeadersJSON)
		for _, e := range produced {
			tagSet[e] = true
		}
		for _, e := range consumed {
			tagSet[e] = true
		}
	}
	for i := range node.Children {
		collectTags(&node.Children[i], recByID, tagSet)
	}
}

func applyFilterAndHighlight(node *TransactionTreeNodeDTO, filter *TreeFilterOptions, entityTags []string) bool {
	matched := filter == nil || filter.Matches(&node.Record, entityTags)
	node.MatchedFilter = matched

	anyChildMatched := false
	for i := range node.Children {
		childMatched := applyFilterAndHighlight(&node.Children[i], filter, entityTags)
		if childMatched {
			anyChildMatched = true
		}
	}

	return matched || anyChildMatched
}

func isRecordAbnormal(r *TrafficRecordSummary) bool {
	if r == nil {
		return false
	}
	return (r.StatusCode != "" && r.StatusCode != "0") || r.ExchangeType == "REQ_ONLY" || r.BizError
}

func isNodeNoise(node *TransactionTreeNodeDTO) bool {
	if node == nil {
		return false
	}
	if node.Record.IsNoise {
		return true
	}
	return node.StitchedIngressRecord != nil && node.StitchedIngressRecord.IsNoise
}

func isNodeAbnormal(node *TransactionTreeNodeDTO) bool {
	if node == nil {
		return false
	}
	if isRecordAbnormal(&node.Record) {
		return true
	}
	return node.StitchedIngressRecord != nil && isRecordAbnormal(node.StitchedIngressRecord)
}

func annotateNoiseFlags(node *TransactionTreeNodeDTO, matcher *agentproto.NoiseMatcher) {
	if node == nil || matcher == nil {
		return
	}
	node.Record.IsNoise = matcher.Match(node.Record.ServiceName, node.Record.Operation)
	if node.StitchedIngressRecord != nil {
		node.StitchedIngressRecord.IsNoise = matcher.Match(node.StitchedIngressRecord.ServiceName, node.StitchedIngressRecord.Operation)
	}
	for i := range node.Children {
		annotateNoiseFlags(&node.Children[i], matcher)
	}
}

func isPureHideableNoiseSubtree(node *TransactionTreeNodeDTO) bool {
	if node == nil || !isNodeNoise(node) || isNodeAbnormal(node) {
		return false
	}
	for i := range node.Children {
		if !isPureHideableNoiseSubtree(&node.Children[i]) {
			return false
		}
	}
	return true
}

func foldNoiseChildren(node *TransactionTreeNodeDTO) int {
	if node == nil || len(node.Children) == 0 {
		return 0
	}
	totalFolded := 0
	kept := make([]TransactionTreeNodeDTO, 0, len(node.Children))
	folded := make([]TransactionTreeNodeDTO, 0)

	for i := range node.Children {
		child := &node.Children[i]
		if isPureHideableNoiseSubtree(child) {
			folded = append(folded, *child)
			totalFolded += countNodes(child)
		} else {
			totalFolded += foldNoiseChildren(child)
			kept = append(kept, *child)
		}
	}

	node.Children = kept
	if len(folded) > 0 {
		node.FoldedNoiseChildren = folded
		node.FoldedNoiseSummary = summarizeFoldedNoise(folded)
	}
	return totalFolded
}

func summarizeFoldedNoise(folded []TransactionTreeNodeDTO) *FoldedNoiseSummaryDTO {
	type breakdownKey struct {
		service string
		op      string
	}
	agg := make(map[breakdownKey]*FoldedNoiseBreakdownItem)
	totalCount := 0
	var totalDurationUs int64

	var walk func(n *TransactionTreeNodeDTO)
	walk = func(n *TransactionTreeNodeDTO) {
		totalCount++
		var dur int64
		if n.Record.LatencyUs != nil {
			dur = *n.Record.LatencyUs
		}
		totalDurationUs += dur
		k := breakdownKey{service: n.Record.ServiceName, op: n.Record.Operation}
		item, ok := agg[k]
		if !ok {
			item = &FoldedNoiseBreakdownItem{
				ServiceName: n.Record.ServiceName,
				Operation:   n.Record.Operation,
			}
			agg[k] = item
		}
		item.Count++
		item.TotalDurationUs += dur
		for i := range n.Children {
			walk(&n.Children[i])
		}
	}
	for i := range folded {
		walk(&folded[i])
	}

	breakdown := make([]FoldedNoiseBreakdownItem, 0, len(agg))
	for _, item := range agg {
		breakdown = append(breakdown, *item)
	}
	sort.Slice(breakdown, func(i, j int) bool {
		if breakdown[i].Count != breakdown[j].Count {
			return breakdown[i].Count > breakdown[j].Count
		}
		if breakdown[i].ServiceName != breakdown[j].ServiceName {
			return breakdown[i].ServiceName < breakdown[j].ServiceName
		}
		return breakdown[i].Operation < breakdown[j].Operation
	})

	return &FoldedNoiseSummaryDTO{
		TotalCount:      totalCount,
		TotalDurationUs: totalDurationUs,
		Breakdown:       breakdown,
	}
}

func collectVisibleNodeIDs(node *TransactionTreeNodeDTO, out map[uint64]bool) {
	if node == nil || out == nil {
		return
	}
	if id, err := strconv.ParseUint(node.Record.ID, 10, 64); err == nil && id > 0 {
		out[id] = true
	}
	if node.StitchedIngressRecord != nil {
		if sid, err := strconv.ParseUint(node.StitchedIngressRecord.ID, 10, 64); err == nil && sid > 0 {
			out[sid] = true
		}
	}
	for i := range node.Children {
		collectVisibleNodeIDs(&node.Children[i], out)
	}
}
