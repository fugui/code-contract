package gitclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"code-contract/config"
	"code-contract/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// PoolFileInput 为 Stage C+ 全池协调的内存输入文件单元。
type PoolFileInput struct {
	ProtoFileID         uint64
	GitRepoID           uint64
	RepoName            string
	SubsystemSlug       string
	BundleID            uint64
	Category            string // BASE_LIB | BUSINESS | GATEWAY
	RepoFilePath        string
	CleanRelPath        string
	LogicalPath         string
	HasExplicitMapping  bool
	Content             string
	RawContent          string
	ContentSHA256       string
	RawBlobSHA          string
	PackageName         string
	Imports             []string
	Symbols             []ParsedProtoSymbol
	LineMap             map[int]int
	IsUServiceSource    bool
	IsHarvestedBase     bool
	PipelineStatus      string
	StatusReason        string
	ManualOwnerBundleID uint64
}

// PoolFileState 为 Stage C+ 处理过程中的可变文件状态（最终写回 DB 与 workspace/）。
type PoolFileState struct {
	PoolFileInput
	PhysDirKey          string
	IsExternalCopy      bool
	InServiceClosure    bool
	ForcedVariantWinner bool
	AutoFixCount        int
	Diagnostics         []NormalizeDiagnostic
}

// PoolReconcileOptions 控制 Stage C+ 全池协调运行参数。
type PoolReconcileOptions struct {
	ClosureOnly        bool
	EnableR7AutoFix    bool
	MaxR7Rounds        int
	EnableR8Quarantine bool
	MaxR8Rounds        int
	StripPGSegment     bool
	GovernanceConfig   *GovernanceConfigDoc
	BaseIndex          *SharedBaseMemoryIndex
	CodesDir           string
	ProtosDir          string
	TriggeredBy        string
	ProgressFn         func(PoolReconcileProgressEvent)
}

// PoolReconcileProgressEvent 为 SSE 推送的 proto_pool_reconcile.progress 事件载荷。
type PoolReconcileProgressEvent struct {
	RunID         uint64 `json:"run_id"`
	Stage         string `json:"stage"`
	Status        string `json:"status"`
	Message       string `json:"message"`
	ServiceCount  int    `json:"service_count"`
	ClosureCount  int    `json:"closure_count"`
	AutoFixCount  int    `json:"auto_fix_count"`
	ConflictCount int    `json:"conflict_count"`
	Quarantined   int    `json:"quarantined_count"`
}

// PoolConflictCandidate 描述全池同一逻辑路径下单个竞争候选文件的哈希与独有符号。
type PoolConflictCandidate struct {
	ProtoFileID   uint64   `json:"proto_file_id"`
	GitRepoID     uint64   `json:"git_repo_id"`
	RepoName      string   `json:"repo_name"`
	BundleID      uint64   `json:"bundle_id"`
	SubsystemSlug string   `json:"subsystem_slug"`
	RepoFilePath  string   `json:"repo_file_path"`
	CleanRelPath  string   `json:"clean_rel_path"`
	ContentSHA256 string   `json:"content_sha256"`
	UniqueSymbols []string `json:"unique_symbols,omitempty"`
	IsWinner      bool     `json:"is_winner"`
}

// PoolConflictEntry 描述全池 R3 消歧后的冲突组及内容/符号差异报告。
type PoolConflictEntry struct {
	LogicalPath      string                  `json:"logical_path"`
	Reason           string                  `json:"reason"`
	WinnerRepoName   string                  `json:"winner_repo_name"`
	WinnerFileID     uint64                  `json:"winner_file_id"`
	ContentIdentical bool                    `json:"content_identical"`
	SymbolDiff       map[string][]string     `json:"symbol_diff,omitempty"`
	Candidates       []PoolConflictCandidate `json:"candidates"`
}

// PoolHarvestedEntry 描述 R4 收割入池的共享基座文件条目。
type PoolHarvestedEntry struct {
	LogicalPath    string `json:"logical_path"`
	SourceAbsPath  string `json:"source_abs_path"`
	SourcePriority int    `json:"source_priority"`
	TriggeredBy    string `json:"triggered_by"` // CHANNEL_A | IMPORT | SYMBOL
}

// PoolAutoFixEntry 描述 R7 编译自愈回路产生的单条源码修正记录。
type PoolAutoFixEntry struct {
	FilePath  string `json:"file_path"`
	RepoName  string `json:"repo_name"`
	FixKind   string `json:"fix_kind"` // fix(a) | fix(b) | fix(c) | variant_swap
	OldImport string `json:"old_import,omitempty"`
	NewImport string `json:"new_import"`
	Symbol    string `json:"symbol,omitempty"`
	Round     int    `json:"round"`
	RepoID    uint64 `json:"repo_id,omitempty"`
	BundleID  uint64 `json:"bundle_id,omitempty"`
}

// PoolQuarantinedEntry 描述 R8 根因优先固损隔离的文件条目。
type PoolQuarantinedEntry struct {
	LogicalPath  string `json:"logical_path"`
	RepoName     string `json:"repo_name"`
	RepoFilePath string `json:"repo_file_path"`
	Reason       string `json:"reason"`
	ErrorCount   int    `json:"error_count"`
	IsRootCause  bool   `json:"is_root_cause"`
}

// PoolReconcileStats 汇总 Stage C+ 全池协调的核心指标。
type PoolReconcileStats struct {
	TotalFiles              int `json:"total_files"`
	ServiceCount            int `json:"service_count"`
	ClosureFileCount        int `json:"closure_file_count"`
	HarvestedBaseCount      int `json:"harvested_base_count"`
	LogicalPathCount        int `json:"logical_path_count"`
	ConflictCount           int `json:"conflict_count"`
	AutoFixCount            int `json:"auto_fix_count"`
	QuarantinedCount        int `json:"quarantined_count"`
	BareImportExpandedCount int `json:"bare_import_expanded_count"`
	PostStripCollisionCount int `json:"post_strip_collision_count"`
	R7Rounds                int `json:"r7_rounds"`
	R8Rounds                int `json:"r8_rounds"`
	CompileErrorCount       int `json:"compile_error_count"`
}

// PoolReconcileReport 为持久化至 DB 及写入 workspace/.clean-report.json 的全池结构化报告。
type PoolReconcileReport struct {
	LogicalMap          map[string]string      `json:"logical_map"`
	Conflicts           []PoolConflictEntry    `json:"conflicts"`
	AutoFixes           []PoolAutoFixEntry     `json:"auto_fixes"`
	Quarantined         []PoolQuarantinedEntry `json:"quarantined"`
	Harvested           []PoolHarvestedEntry   `json:"harvested"`
	PostStripCollisions []PoolStripCollision   `json:"post_strip_collisions,omitempty"`
	Stats               PoolReconcileStats     `json:"stats"`
}

// PoolReconcileOutput 为内存态全池协调完整输出。
type PoolReconcileOutput struct {
	Files  []*PoolFileState
	Report PoolReconcileReport
}

// poolR7R8Hook 允许 M42 注入纯 Go protocompile R7/R8 执行器（M41 阶段为 nil）。
var poolR7R8Hook func(ctx context.Context, states *[]*PoolFileState, report *PoolReconcileReport, opts PoolReconcileOptions) error

// ReconcilePoolInMemory 执行 ADR-28 Stage C+ 全池协调：
// R2 统一坐标系投票 & 裸文件名展开 -> 全池剥离碰撞检测 -> R1/R3/R4 协同迭代闭包与消歧 -> (可选 M42) R7/R8。
func ReconcilePoolInMemory(ctx context.Context, inputs []PoolFileInput, opts PoolReconcileOptions) (*PoolReconcileOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	stripPG := opts.StripPGSegment
	if opts.GovernanceConfig != nil {
		stripPG = opts.GovernanceConfig.Global.EffectiveStripPGSegment()
	}

	// 1. 确定性排序输入并构建状态集
	sortedInputs := append([]PoolFileInput(nil), inputs...)
	sort.Slice(sortedInputs, func(i, j int) bool {
		if sortedInputs[i].GitRepoID != sortedInputs[j].GitRepoID {
			return sortedInputs[i].GitRepoID < sortedInputs[j].GitRepoID
		}
		if sortedInputs[i].RepoName != sortedInputs[j].RepoName {
			return sortedInputs[i].RepoName < sortedInputs[j].RepoName
		}
		if sortedInputs[i].RepoFilePath != sortedInputs[j].RepoFilePath {
			return sortedInputs[i].RepoFilePath < sortedInputs[j].RepoFilePath
		}
		return sortedInputs[i].ProtoFileID < sortedInputs[j].ProtoFileID
	})

	states := make([]*PoolFileState, 0, len(sortedInputs))
	repoFileCounts := make(map[uint64]int)
	for _, in := range sortedInputs {
		if in.PipelineStatus == "" {
			in.PipelineStatus = models.ProtoPipelineNormalized
		}
		if in.ContentSHA256 == "" && in.Content != "" {
			sum := sha256.Sum256([]byte(in.Content))
			in.ContentSHA256 = hex.EncodeToString(sum[:])
		}
		// 统一坐标系归一化文件内非 WKT 非裸 import
		normImps := make([]string, 0, len(in.Imports))
		for _, imp := range in.Imports {
			trimmed := strings.TrimSpace(imp)
			if trimmed == "" {
				continue
			}
			if !strings.Contains(trimmed, "/") || IsWellKnownProtoImport(trimmed) {
				normImps = append(normImps, trimmed)
			} else {
				normImps = append(normImps, NormalizeLogicalPath(trimmed, stripPG))
			}
		}
		in.Imports = normImps

		rawDir := path.Dir(strings.TrimLeft(strings.ReplaceAll(in.RepoFilePath, "\\", "/"), "/"))
		physKey := fmt.Sprintf("%d::%s", in.GitRepoID, rawDir)
		st := &PoolFileState{
			PoolFileInput: in,
			PhysDirKey:    physKey,
		}
		states = append(states, st)
		repoFileCounts[in.GitRepoID]++
	}

	// 2. R2 全池投票逻辑路径推导（在统一去 /pg 坐标系运算）
	byBasename := make(map[string][]*PoolFileState)
	for _, st := range states {
		if st.PipelineStatus == models.ProtoPipelineQuarantined {
			continue
		}
		base := path.Base(st.RepoFilePath)
		byBasename[base] = append(byBasename[base], st)
	}

	// dirVotes[physDirKey][candidatePrefix] = count
	dirVotes := make(map[string]map[string]int)
	for _, st := range states {
		if st.PipelineStatus == models.ProtoPipelineQuarantined {
			continue
		}
		for _, imp := range st.Imports {
			if IsWellKnownProtoImport(imp) || !strings.Contains(imp, "/") {
				continue
			}
			normImp := NormalizeLogicalPath(imp, stripPG)
			base := path.Base(normImp)
			cands := byBasename[base]
			// 仅当 basename 在全池仅命中唯一物理目录时投票
			physSet := make(map[string]struct{})
			var targetDirKey string
			for _, c := range cands {
				physSet[c.PhysDirKey] = struct{}{}
				targetDirKey = c.PhysDirKey
			}
			if len(physSet) == 1 && targetDirKey != "" {
				impDir := path.Dir(normImp)
				if impDir == "." {
					impDir = ""
				}
				if dirVotes[targetDirKey] == nil {
					dirVotes[targetDirKey] = make(map[string]int)
				}
				dirVotes[targetDirKey][impDir]++
			}
		}
	}

	// 计算每个物理目录的权威逻辑前缀：
	// 显式 strip_prefixes/path_rewrites > 最高票 (平票字典序首项) > 归一化启发式 <subsys_lower>/<comp_lower>
	physDirLogicalPrefix := make(map[string]string)
	physDirVoteTotal := make(map[string]int)
	for _, st := range states {
		if _, done := physDirLogicalPrefix[st.PhysDirKey]; done {
			continue
		}
		if st.HasExplicitMapping && st.CleanRelPath != "" {
			d := path.Dir(NormalizeLogicalPath(st.CleanRelPath, stripPG))
			if d == "." {
				d = ""
			}
			physDirLogicalPrefix[st.PhysDirKey] = d
			continue
		}
		votes := dirVotes[st.PhysDirKey]
		if len(votes) > 0 {
			bestPfx := ""
			bestCnt := -1
			pfxKeys := make([]string, 0, len(votes))
			total := 0
			for p, cnt := range votes {
				pfxKeys = append(pfxKeys, p)
				total += cnt
			}
			sort.Strings(pfxKeys) // 平票时按字典序确定性取首项
			for _, p := range pfxKeys {
				cnt := votes[p]
				if cnt > bestCnt {
					bestCnt = cnt
					bestPfx = p
				}
			}
			physDirLogicalPrefix[st.PhysDirKey] = bestPfx
			physDirVoteTotal[st.PhysDirKey] = total
			continue
		}
		// 启发式兜底：<subsys_lower>/<comp_lower>（或已含子系统前缀的归一化目录）
		physDirLogicalPrefix[st.PhysDirKey] = deriveNormalizedHeuristicDir(st.SubsystemSlug, st.RepoFilePath, st.LogicalPath, stripPG)
	}

	// 应用推导后的 LogicalPath 到全池文件
	for _, st := range states {
		if st.IsHarvestedBase && st.LogicalPath != "" {
			st.LogicalPath = NormalizeLogicalPath(st.LogicalPath, stripPG)
			continue
		}
		pfx := physDirLogicalPrefix[st.PhysDirKey]
		base := path.Base(st.RepoFilePath)
		if pfx == "" || pfx == "." {
			st.LogicalPath = NormalizeLogicalPath(base, stripPG)
		} else {
			st.LogicalPath = NormalizeLogicalPath(path.Join(pfx, base), stripPG)
		}
	}

	// 3. 裸文件名 import 展开（import "Foo.proto"; -> import "<logical_dir>/Foo.proto";）
	bareExpandedCount := 0
	for _, st := range states {
		if st.PipelineStatus == models.ProtoPipelineQuarantined {
			continue
		}
		logicalDir := path.Dir(st.LogicalPath)
		if logicalDir == "." {
			logicalDir = ""
		}
		rewrote := false
		newImports := make([]string, 0, len(st.Imports))
		for _, imp := range st.Imports {
			if imp != "" && !strings.Contains(imp, "/") && !IsWellKnownProtoImport(imp) && logicalDir != "" {
				expanded := path.Join(logicalDir, imp)
				if newContent, changed := rewriteBareImportInSource(st.Content, imp, expanded); changed {
					st.Content = newContent
					rewrote = true
				}
				newImports = append(newImports, expanded)
				bareExpandedCount++
				continue
			}
			newImports = append(newImports, imp)
		}
		if rewrote {
			st.Imports = newImports
			sum := sha256.Sum256([]byte(st.Content))
			st.ContentSHA256 = hex.EncodeToString(sum[:])
			gate := ParseProtoSyntaxOnly(st.LogicalPath, []byte(st.Content))
			if gate.Passed {
				st.Imports = gate.Imports
				st.Symbols = gate.Symbols
			}
		}
	}

	// 4. R4 通道 A：先将显式声明的共享基座（免疫 R0）并入全池
	harvestedEntries := make([]PoolHarvestedEntry, 0)
	existingPoolPaths := make(map[string]bool, len(states))
	for _, st := range states {
		existingPoolPaths[st.LogicalPath] = true
	}
	if opts.BaseIndex != nil {
		chanABases := opts.BaseIndex.CollectChannelABases(ctx, existingPoolPaths)
		for _, cand := range chanABases {
			if existingPoolPaths[cand.NormPath] {
				continue
			}
			existingPoolPaths[cand.NormPath] = true
			st := stateFromHarvestedCandidate(cand, stripPG)
			states = append(states, st)
			harvestedEntries = append(harvestedEntries, PoolHarvestedEntry{
				LogicalPath:    cand.NormPath,
				SourceAbsPath:  cand.AbsPath,
				SourcePriority: cand.Priority,
				TriggeredBy:    "CHANNEL_A",
			})
		}
	}

	// 5. 全池剥离碰撞检测（业务仓 + R4 基座）
	stripCandidates := make([]PoolPathCandidate, 0, len(states))
	for _, st := range states {
		if st.PipelineStatus == models.ProtoPipelineQuarantined {
			continue
		}
		prePath := NormalizeLogicalPath(st.RepoFilePath, false)
		if st.HasExplicitMapping && st.CleanRelPath != "" {
			prePath = NormalizeLogicalPath(st.CleanRelPath, false)
		}
		stripCandidates = append(stripCandidates, PoolPathCandidate{
			RepoID:        st.GitRepoID,
			RepoName:      st.RepoName,
			PreStripPath:  prePath,
			PostStripPath: st.LogicalPath,
		})
	}
	var stripCols []PoolStripCollision
	if stripPG {
		stripCols = CheckPoolStripPGCollisions(stripCandidates)
	}

	// 6. R1 / R3 / R4 协同迭代闭包 BFS
	report := PoolReconcileReport{
		LogicalMap:          make(map[string]string, len(states)),
		Conflicts:           []PoolConflictEntry{},
		AutoFixes:           []PoolAutoFixEntry{},
		Quarantined:         []PoolQuarantinedEntry{},
		Harvested:           harvestedEntries,
		PostStripCollisions: stripCols,
	}

	rebuildPoolLookups := func() (map[string][]*PoolFileState, map[string][]*PoolFileState) {
		byLP := make(map[string][]*PoolFileState, len(states))
		byBase := make(map[string][]*PoolFileState, len(states))
		for _, st := range states {
			if st.PipelineStatus == models.ProtoPipelineQuarantined {
				continue
			}
			byLP[st.LogicalPath] = append(byLP[st.LogicalPath], st)
			base := path.Base(st.LogicalPath)
			byBase[base] = append(byBase[base], st)
		}
		for lp := range byLP {
			cands := byLP[lp]
			sort.Slice(cands, func(i, j int) bool {
				if cands[i].GitRepoID != cands[j].GitRepoID {
					return cands[i].GitRepoID < cands[j].GitRepoID
				}
				return cands[i].RepoFilePath < cands[j].RepoFilePath
			})
		}
		for b := range byBase {
			cands := byBase[b]
			sort.Slice(cands, func(i, j int) bool {
				if cands[i].LogicalPath != cands[j].LogicalPath {
					return cands[i].LogicalPath < cands[j].LogicalPath
				}
				return cands[i].GitRepoID < cands[j].GitRepoID
			})
		}
		return byLP, byBase
	}

	byLogicalPath, byBaseAll := rebuildPoolLookups()
	alreadyHarvestedNorm := make(map[string]bool, len(states))
	for _, st := range states {
		if st.IsHarvestedBase {
			alreadyHarvestedNorm[st.LogicalPath] = true
		}
	}

	var queue []*PoolFileState
	inQueueOrSeen := make(map[*PoolFileState]bool)
	for _, st := range states {
		if st.PipelineStatus == models.ProtoPipelineQuarantined {
			continue
		}
		if st.IsUServiceSource {
			st.InServiceClosure = true
			queue = append(queue, st)
			inQueueOrSeen[st] = true
		}
	}
	sortPoolStates(queue)

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		sortedImps := append([]string(nil), cur.Imports...)
		sort.Strings(sortedImps)
		for _, imp := range sortedImps {
			if IsWellKnownProtoImport(imp) {
				continue
			}
			normImp := NormalizeLogicalPath(imp, stripPG)
			impDir := path.Dir(normImp)
			if impDir == "." {
				impDir = ""
			}

			cands := byLogicalPath[normImp]
			var chosen *PoolFileState
			switch len(cands) {
			case 1:
				chosen = cands[0]
			case 0:
				// 尝试 R4 通道 B 按需收割
				if opts.BaseIndex != nil {
					harvested := opts.BaseIndex.HarvestOnDemandByImport(normImp, alreadyHarvestedNorm)
					if len(harvested) > 0 {
						for _, h := range harvested {
							if _, exists := byLogicalPath[h.NormPath]; !exists {
								alreadyHarvestedNorm[h.NormPath] = true
								hst := stateFromHarvestedCandidate(h, stripPG)
								hst.InServiceClosure = true
								states = append(states, hst)
								report.Harvested = append(report.Harvested, PoolHarvestedEntry{
									LogicalPath:    h.NormPath,
									SourceAbsPath:  h.AbsPath,
									SourcePriority: h.Priority,
									TriggeredBy:    "IMPORT",
								})
								if !inQueueOrSeen[hst] {
									inQueueOrSeen[hst] = true
									queue = append(queue, hst)
								}
							}
						}
						byLogicalPath, byBaseAll = rebuildPoolLookups()
						if after := byLogicalPath[normImp]; len(after) > 0 {
							chosen = after[0]
						}
					}
				}
				// 否则按 basename 兜底匹配，多候选必须经过 R3 消歧择一，严禁全量拉入闭包
				if chosen == nil {
					baseCands := byBaseAll[path.Base(normImp)]
					if len(baseCands) == 1 {
						chosen = baseCands[0]
					} else if len(baseCands) > 1 {
						chosen = selectR3Winner(impDir, baseCands, cur, physDirVoteTotal, repoFileCounts)
					}
				}
			default:
				// 多候选精确命中：立即走 R3 多级确定性消歧择一
				chosen = selectR3Winner(impDir, cands, cur, physDirVoteTotal, repoFileCounts)
			}

			if chosen != nil {
				chosen.InServiceClosure = true
				if !inQueueOrSeen[chosen] {
					inQueueOrSeen[chosen] = true
					queue = append(queue, chosen)
					sortPoolStates(queue)
				}
			}
		}
	}

	// 如果 closure_only = false，则全部正常文件视为编译目标
	if !opts.ClosureOnly {
		for _, st := range states {
			if st.PipelineStatus != models.ProtoPipelineQuarantined {
				st.InServiceClosure = true
			}
		}
	}

	// 对全池所有同逻辑路径多候选组执行 R3 权威仲裁 + 标记 is_external_copy + 生成含哈希与 symbol_diff 冲突报告
	recomputeConflictsAndExternalCopies := func() {
		byLP, _ := rebuildPoolLookups()
		lpKeys := make([]string, 0, len(byLP))
		for lp, grp := range byLP {
			if len(grp) > 1 {
				lpKeys = append(lpKeys, lp)
			} else if len(grp) == 1 {
				grp[0].IsExternalCopy = false
			}
		}
		sort.Strings(lpKeys)
		report.Conflicts = make([]PoolConflictEntry, 0, len(lpKeys))
		for _, lp := range lpKeys {
			grp := byLP[lp]
			impDir := path.Dir(lp)
			if impDir == "." {
				impDir = ""
			}
			winner := selectR3Winner(impDir, grp, nil, physDirVoteTotal, repoFileCounts)
			for _, c := range grp {
				c.IsExternalCopy = c != winner
			}
			report.Conflicts = append(report.Conflicts, buildPoolConflictEntry(lp, grp, winner))
		}
	}
	recomputeConflictsAndExternalCopies()

	// 7. 预留 M42 R7/R8 编译自愈与根因隔离扩展钩子
	if poolR7R8Hook != nil && (opts.EnableR7AutoFix || opts.EnableR8Quarantine) {
		if err := poolR7R8Hook(ctx, &states, &report, opts); err != nil {
			return nil, err
		}
		recomputeConflictsAndExternalCopies()
	}

	// 8. 汇总 LogicalMap 与 Stats
	serviceCnt := 0
	closureCnt := 0
	harvestedCnt := 0
	quarantinedCnt := 0
	totalAutoFixes := len(report.AutoFixes)
	logicalSet := make(map[string]struct{}, len(states))

	sortPoolStates(states)
	for _, st := range states {
		report.LogicalMap[st.RepoName+":"+st.RepoFilePath] = st.LogicalPath
		if st.IsUServiceSource {
			serviceCnt++
		}
		if st.IsHarvestedBase {
			harvestedCnt++
		}
		if st.PipelineStatus == models.ProtoPipelineQuarantined {
			quarantinedCnt++
			continue
		}
		if !st.IsExternalCopy {
			logicalSet[st.LogicalPath] = struct{}{}
		}
		if st.InServiceClosure && !st.IsExternalCopy {
			closureCnt++
		}
	}

	report.Stats = PoolReconcileStats{
		TotalFiles:              len(states),
		ServiceCount:            serviceCnt,
		ClosureFileCount:        closureCnt,
		HarvestedBaseCount:      harvestedCnt,
		LogicalPathCount:        len(logicalSet),
		ConflictCount:           len(report.Conflicts),
		AutoFixCount:            totalAutoFixes,
		QuarantinedCount:        quarantinedCnt,
		BareImportExpandedCount: bareExpandedCount,
		PostStripCollisionCount: len(report.PostStripCollisions),
		R7Rounds:                report.Stats.R7Rounds,
		R8Rounds:                report.Stats.R8Rounds,
		CompileErrorCount:       report.Stats.CompileErrorCount,
	}

	return &PoolReconcileOutput{
		Files:  states,
		Report: report,
	}, nil
}

// selectR3Winner 严格按 ADR-28 §2.4.3 7 级确定性顺序裁决多候选权威文件。
func selectR3Winner(
	importDir string,
	candidates []*PoolFileState,
	consumer *PoolFileState,
	physDirVotes map[string]int,
	repoFileCounts map[uint64]int,
) *PoolFileState {
	if len(candidates) == 0 {
		return nil
	}
	sorted := append([]*PoolFileState(nil), candidates...)
	consumerLineage := ""
	consumerSubsys := ""
	if consumer != nil {
		consumerSubsys = strings.ToLower(strings.TrimSpace(consumer.SubsystemSlug))
		consumerLineage = extractRepoLineage(consumer.RepoName)
	}

	score := func(c *PoolFileState) (tier int, repoCount int) {
		// 0. R7 Variant Swap 临时赢家覆盖
		if c.ForcedVariantWinner {
			return 0, repoFileCounts[c.GitRepoID]
		}
		// 1. MANUAL
		if c.ManualOwnerBundleID > 0 && c.BundleID == c.ManualOwnerBundleID {
			return 1, repoFileCounts[c.GitRepoID]
		}
		// 2. BASE_LIB
		if c.Category == models.ProtoCategoryBaseLib {
			return 2, repoFileCounts[c.GitRepoID]
		}
		// 3. 目录逻辑前缀恰好等于 importDir
		candDir := path.Dir(c.LogicalPath)
		if candDir == "." {
			candDir = ""
		}
		if importDir != "" && candDir == importDir {
			return 3, repoFileCounts[c.GitRepoID]
		}
		// 4. 自由目录认领（尚无投票前缀的目录）
		if physDirVotes[c.PhysDirKey] == 0 {
			return 4, repoFileCounts[c.GitRepoID]
		}
		// 5. 同顶层子系统优先
		if consumerSubsys != "" && strings.EqualFold(strings.TrimSpace(c.SubsystemSlug), consumerSubsys) {
			return 5, repoFileCounts[c.GitRepoID]
		}
		// 6. 同仓库谱系优先（如 LT-）
		if consumerLineage != "" && extractRepoLineage(c.RepoName) == consumerLineage {
			return 6, repoFileCounts[c.GitRepoID]
		}
		// 7. 大仓自洽 + 字典序
		return 7, repoFileCounts[c.GitRepoID]
	}

	sort.Slice(sorted, func(i, j int) bool {
		ti, ri := score(sorted[i])
		tj, rj := score(sorted[j])
		if ti != tj {
			return ti < tj
		}
		if ri != rj {
			return ri > rj
		}
		if sorted[i].RepoName != sorted[j].RepoName {
			return sorted[i].RepoName < sorted[j].RepoName
		}
		if sorted[i].RepoFilePath != sorted[j].RepoFilePath {
			return sorted[i].RepoFilePath < sorted[j].RepoFilePath
		}
		return sorted[i].ProtoFileID < sorted[j].ProtoFileID
	})
	return sorted[0]
}

func buildPoolConflictEntry(logicalPath string, grp []*PoolFileState, winner *PoolFileState) PoolConflictEntry {
	hashes := make(map[string]struct{}, len(grp))
	candSymSets := make([]map[string]struct{}, len(grp))
	allSyms := make(map[string]int)

	for i, c := range grp {
		if c.ContentSHA256 != "" {
			hashes[c.ContentSHA256] = struct{}{}
		} else {
			sum := sha256.Sum256([]byte(c.Content))
			hashes[hex.EncodeToString(sum[:])] = struct{}{}
		}
		set := make(map[string]struct{}, len(c.Symbols))
		for _, s := range c.Symbols {
			if s.FullName == "" {
				continue
			}
			if _, seen := set[s.FullName]; !seen {
				set[s.FullName] = struct{}{}
				allSyms[s.FullName]++
			}
		}
		candSymSets[i] = set
	}

	symbolDiff := make(map[string][]string)
	candsOut := make([]PoolConflictCandidate, 0, len(grp))
	for i, c := range grp {
		var unique []string
		for sym := range candSymSets[i] {
			if allSyms[sym] < len(grp) {
				unique = append(unique, sym)
			}
		}
		sort.Strings(unique)
		key := fmt.Sprintf("%s:%s", c.RepoName, c.RepoFilePath)
		if len(unique) > 0 {
			symbolDiff[key] = unique
		}
		cleanRel := c.CleanRelPath
		if cleanRel == "" {
			cleanRel = c.LogicalPath
		}
		candsOut = append(candsOut, PoolConflictCandidate{
			ProtoFileID:   c.ProtoFileID,
			GitRepoID:     c.GitRepoID,
			RepoName:      c.RepoName,
			BundleID:      c.BundleID,
			SubsystemSlug: c.SubsystemSlug,
			RepoFilePath:  c.RepoFilePath,
			CleanRelPath:  cleanRel,
			ContentSHA256: c.ContentSHA256,
			UniqueSymbols: unique,
			IsWinner:      c == winner,
		})
	}

	reason := "R3_AUTO_DISAMBIGUATED"
	if winner != nil {
		if winner.ManualOwnerBundleID > 0 && winner.BundleID == winner.ManualOwnerBundleID {
			reason = "MANUAL_OWNER"
		} else if winner.Category == models.ProtoCategoryBaseLib {
			reason = "BASE_LIB_PRIORITY"
		}
	}
	var winnerRepo string
	var winnerID uint64
	if winner != nil {
		winnerRepo = winner.RepoName
		winnerID = winner.ProtoFileID
	}

	return PoolConflictEntry{
		LogicalPath:      logicalPath,
		Reason:           reason,
		WinnerRepoName:   winnerRepo,
		WinnerFileID:     winnerID,
		ContentIdentical: len(hashes) <= 1,
		SymbolDiff:       symbolDiff,
		Candidates:       candsOut,
	}
}

func deriveNormalizedHeuristicDir(subsys, repoFilePath, existingLogical string, stripPG bool) string {
	subLower := strings.ToLower(strings.TrimSpace(subsys))
	if subLower == UnassignedSubsystemSlug {
		subLower = ""
	}
	normRepoDir := path.Dir(NormalizeLogicalPath(repoFilePath, stripPG))
	if normRepoDir == "." {
		normRepoDir = ""
	}
	if existingLogical != "" {
		existingDir := path.Dir(NormalizeLogicalPath(existingLogical, stripPG))
		if existingDir == "." {
			existingDir = ""
		}
		if existingDir != "" {
			return existingDir
		}
	}
	if normRepoDir == "" {
		return ""
	}
	if subLower != "" && (normRepoDir == subLower || strings.HasPrefix(normRepoDir, subLower+"/")) {
		return normRepoDir
	}
	if subLower != "" {
		// 提取组件首段或保留归一化目录并前置子系统
		return path.Join(subLower, normRepoDir)
	}
	return normRepoDir
}

func extractRepoLineage(repoName string) string {
	trimmed := strings.TrimSpace(repoName)
	if idx := strings.IndexByte(trimmed, '-'); idx > 0 {
		return strings.ToUpper(trimmed[:idx])
	}
	return ""
}

func rewriteBareImportInSource(content, bareImp, expandedImp string) (string, bool) {
	lines := strings.Split(content, "\n")
	changed := false
	inBlk := false
	for i, l := range lines {
		code, nextBlk := stripLineCommentsAndTrackBlock(l, inBlk)
		inBlk = nextBlk
		if strings.TrimSpace(code) == "" {
			continue
		}
		if m := importStmtRegex.FindStringSubmatch(l); len(m) == 4 && m[2] == bareImp {
			lines[i] = fmt.Sprintf(`%s"%s"%s`, m[1], expandedImp, m[3])
			changed = true
		}
	}
	if !changed {
		return content, false
	}
	return strings.Join(lines, "\n"), true
}

func stateFromHarvestedCandidate(cand SharedBaseSourceCandidate, stripPG bool) *PoolFileState {
	normImps := make([]string, 0, len(cand.Imports))
	for _, imp := range cand.Imports {
		if IsWellKnownProtoImport(imp) || !strings.Contains(imp, "/") {
			normImps = append(normImps, imp)
		} else {
			normImps = append(normImps, NormalizeLogicalPath(imp, stripPG))
		}
	}
	return &PoolFileState{
		PoolFileInput: PoolFileInput{
			GitRepoID:        SystemSharedBasePlatformRepoID,
			RepoName:         SystemSharedBaseRepoName,
			SubsystemSlug:    "shared-base",
			Category:         models.ProtoCategoryBaseLib,
			RepoFilePath:     cand.RelPath,
			CleanRelPath:     cand.NormPath,
			LogicalPath:      cand.NormPath,
			Content:          string(cand.CleanBytes),
			RawContent:       string(cand.RawBytes),
			ContentSHA256:    cand.CleanSHA256,
			RawBlobSHA:       cand.RawBlobSHA,
			PackageName:      cand.PackageName,
			Imports:          normImps,
			Symbols:          cand.Symbols,
			LineMap:          cand.LineMap,
			IsUServiceSource: cand.IsUServiceSource,
			IsHarvestedBase:  true,
			PipelineStatus:   models.ProtoPipelineNormalized,
		},
		PhysDirKey: fmt.Sprintf("%d::%s", SystemSharedBasePlatformRepoID, path.Dir(cand.RelPath)),
	}
}

func sortPoolStates(list []*PoolFileState) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].LogicalPath != list[j].LogicalPath {
			return list[i].LogicalPath < list[j].LogicalPath
		}
		if list[i].GitRepoID != list[j].GitRepoID {
			return list[i].GitRepoID < list[j].GitRepoID
		}
		return list[i].RepoFilePath < list[j].RepoFilePath
	})
}

// RunPoolReconcile 执行完整的 DB 级 Stage C+ 全池协调、持久化更新与运行时记录归档。
func RunPoolReconcile(ctx context.Context, db *gorm.DB, opts PoolReconcileOptions) (*models.ProtoPoolReconcileRun, *PoolReconcileReport, error) {
	if db == nil {
		return nil, nil, errors.New("db 不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	startedAt := time.Now().UTC()

	govDoc := DefaultGovernanceConfig()
	govRev := 0
	govSHA := ""
	if opts.GovernanceConfig != nil {
		govDoc = *opts.GovernanceConfig
		_, govSHA, _ = MarshalGovernanceConfigCanonical(govDoc)
	} else if snap, err := LoadActiveGovernanceConfig(ctx, db); err == nil && snap != nil {
		govDoc = snap.Doc
		govRev = snap.Doc.Revision
		govSHA = snap.ConfigSHA256
	}
	stripPG := govDoc.Global.EffectiveStripPGSegment()
	prCfg := govDoc.Global.EffectivePoolReconcile()
	if !opts.ClosureOnly && opts.MaxR7Rounds == 0 && opts.MaxR8Rounds == 0 {
		opts.ClosureOnly = prCfg.EffectiveClosureOnly()
		opts.EnableR7AutoFix = prCfg.EffectiveEnableR7()
		opts.MaxR7Rounds = prCfg.EffectiveMaxR7Rounds()
		opts.EnableR8Quarantine = prCfg.EffectiveEnableR8()
		opts.MaxR8Rounds = prCfg.EffectiveMaxR8Rounds()
	}
	opts.StripPGSegment = stripPG
	opts.GovernanceConfig = &govDoc
	if strings.TrimSpace(opts.CodesDir) == "" {
		opts.CodesDir = config.AppConfig.Storage.GetCodesDir()
	}
	if strings.TrimSpace(opts.ProtosDir) == "" {
		opts.ProtosDir = config.AppConfig.Storage.GetProtosDir()
	}

	run := models.ProtoPoolReconcileRun{
		Status:         models.PoolReconcileStatusRunning,
		TriggeredBy:    strings.TrimSpace(opts.TriggeredBy),
		ConfigRevision: govRev,
		ConfigSHA256:   govSHA,
		StripPGSegment: stripPG,
		ClosureOnly:    opts.ClosureOnly,
		StartedAt:      startedAt,
	}
	if err := db.WithContext(ctx).Create(&run).Error; err != nil {
		return nil, nil, err
	}

	emit := func(stage, msg string, report *PoolReconcileReport) {
		if opts.ProgressFn == nil {
			return
		}
		evt := PoolReconcileProgressEvent{
			RunID:   run.ID,
			Stage:   stage,
			Status:  run.Status,
			Message: msg,
		}
		if report != nil {
			evt.ServiceCount = report.Stats.ServiceCount
			evt.ClosureCount = report.Stats.ClosureFileCount
			evt.AutoFixCount = report.Stats.AutoFixCount
			evt.ConflictCount = report.Stats.ConflictCount
			evt.Quarantined = report.Stats.QuarantinedCount
		}
		opts.ProgressFn(evt)
	}

	emit("INDEXING", "正在构建共享基座内存索引与加载全池快照...", nil)

	if opts.BaseIndex == nil {
		baseIdx, bErr := BuildSharedBaseMemoryIndex(ctx, govDoc.Global.SharedBaseSources, opts.CodesDir, &govDoc)
		if bErr == nil {
			opts.BaseIndex = baseIdx
		}
	}

	// 加载全池启用仓库的非删除 ProtoFile 与最新源码
	var repos []models.GitRepo
	if err := db.WithContext(ctx).Order("id ASC").Find(&repos).Error; err != nil {
		return nil, nil, failPoolRun(ctx, db, &run, startedAt, err)
	}
	repoByID := make(map[uint64]models.GitRepo, len(repos))
	for _, r := range repos {
		repoByID[r.ID] = r
	}

	var owners []models.ProtoImportOwner
	_ = db.WithContext(ctx).Where("source = ?", models.ImportOwnerSourceManual).Find(&owners).Error
	manualOwnerByPath := make(map[string]uint64, len(owners))
	for _, o := range owners {
		manualOwnerByPath[o.LogicalPath] = o.OwnerBundleID
	}

	var dbFiles []models.ProtoFile
	if err := db.WithContext(ctx).Where("is_deleted = false").Order("id ASC").Find(&dbFiles).Error; err != nil {
		return nil, nil, failPoolRun(ctx, db, &run, startedAt, err)
	}

	verIDs := make([]uint64, 0, len(dbFiles))
	for _, f := range dbFiles {
		if f.LatestVersionID != nil && *f.LatestVersionID > 0 {
			verIDs = append(verIDs, *f.LatestVersionID)
		}
	}
	verMap := make(map[uint64]models.ProtoFileVersion, len(verIDs))
	if len(verIDs) > 0 {
		var vers []models.ProtoFileVersion
		if err := db.WithContext(ctx).Where("id IN ?", verIDs).Find(&vers).Error; err == nil {
			for _, v := range vers {
				verMap[v.ProtoFileID] = v
			}
		}
	}

	inputs := make([]PoolFileInput, 0, len(dbFiles))
	for _, f := range dbFiles {
		r, ok := repoByID[f.GitRepoID]
		if ok && !r.SyncEnabled && r.PlatformRepoID != SystemSharedBasePlatformRepoID {
			continue
		}
		v := verMap[f.ID]
		var imps []string
		if len(f.ImportsJSON) > 0 {
			_ = json.Unmarshal(f.ImportsJSON, &imps)
		}
		var syms []ParsedProtoSymbol
		if len(f.SymbolsJSON) > 0 {
			_ = json.Unmarshal(f.SymbolsJSON, &syms)
		}
		var lm map[int]int
		if len(f.LineMapJSON) > 0 {
			_ = json.Unmarshal(f.LineMapJSON, &lm)
		}
		inputs = append(inputs, PoolFileInput{
			ProtoFileID:         f.ID,
			GitRepoID:           f.GitRepoID,
			RepoName:            r.Name,
			SubsystemSlug:       r.SubsystemSlug,
			BundleID:            f.BundleID,
			Category:            f.Category,
			RepoFilePath:        f.RepoFilePath,
			CleanRelPath:        f.CleanRelPath,
			LogicalPath:         f.FilePath,
			HasExplicitMapping:  len(r.StripPrefixes()) > 0 || len(r.PathRewrites()) > 0,
			Content:             v.ContentText,
			RawContent:          v.RawContentText,
			ContentSHA256:       f.ContentSHA256,
			RawBlobSHA:          f.RawBlobSHA,
			PackageName:         f.PackageName,
			Imports:             imps,
			Symbols:             syms,
			LineMap:             lm,
			IsUServiceSource:    f.IsUServiceSource,
			IsHarvestedBase:     f.IsHarvestedBase,
			PipelineStatus:      f.PipelineStatus,
			StatusReason:        f.StatusReason,
			ManualOwnerBundleID: manualOwnerByPath[NormalizeLogicalPath(f.FilePath, stripPG)],
		})
	}

	emit("RECONCILING", "正在执行 Stage C+ 全池投票、闭包收敛与编译自愈...", nil)

	out, err := ReconcilePoolInMemory(ctx, inputs, opts)
	if err != nil {
		return nil, nil, failPoolRun(ctx, db, &run, startedAt, err)
	}

	// 将协调结果、R4 新收割基座及 R7 修复写回 DB
	if err := persistPoolReconcileOutput(ctx, db, out, opts.BaseIndex); err != nil {
		return nil, nil, failPoolRun(ctx, db, &run, startedAt, err)
	}

	// 原子导出 workspace/ 与 .clean-report.json（M42 扩展 TreeKindWorkspace；此处按钩子安全执行）
	workspaceVer := ""
	if workspaceExportHook != nil {
		ver, _, wErr := workspaceExportHook(opts.ProtosDir, run.ID, out)
		if wErr != nil {
			return nil, nil, failPoolRun(ctx, db, &run, startedAt, wErr)
		}
		workspaceVer = ver
	}

	fin := time.Now().UTC()
	reportBytes, _ := json.Marshal(out.Report)
	run.Status = models.PoolReconcileStatusCompleted
	run.TotalFiles = out.Report.Stats.TotalFiles
	run.ServiceCount = out.Report.Stats.ServiceCount
	run.ClosureFileCount = out.Report.Stats.ClosureFileCount
	run.HarvestedBaseCount = out.Report.Stats.HarvestedBaseCount
	run.LogicalPathCount = out.Report.Stats.LogicalPathCount
	run.ConflictCount = out.Report.Stats.ConflictCount
	run.AutoFixCount = out.Report.Stats.AutoFixCount
	run.QuarantinedCount = out.Report.Stats.QuarantinedCount
	run.BareImportExpandedCount = out.Report.Stats.BareImportExpandedCount
	run.PostStripCollisionCount = out.Report.Stats.PostStripCollisionCount
	run.WorkspaceVersion = workspaceVer
	run.ReportJSON = datatypes.JSON(reportBytes)
	run.DurationMs = fin.Sub(startedAt).Milliseconds()
	run.FinishedAt = &fin

	if err := db.WithContext(ctx).Save(&run).Error; err != nil {
		return nil, nil, err
	}

	emit("COMPLETED", "全池 Stage C+ 编译收敛与工作区导出完成", &out.Report)
	return &run, &out.Report, nil
}

// workspaceExportHook 允许 M42 接入 WriteAndSwitchWorkspace 导出 workspace/ 与 .clean-report.json。
var workspaceExportHook func(protosDir string, runID uint64, out *PoolReconcileOutput) (versionName string, linkPath string, err error)

func failPoolRun(ctx context.Context, db *gorm.DB, run *models.ProtoPoolReconcileRun, startedAt time.Time, cause error) error {
	fin := time.Now().UTC()
	run.Status = models.PoolReconcileStatusFailed
	run.ErrorMessage = cause.Error()
	run.DurationMs = fin.Sub(startedAt).Milliseconds()
	run.FinishedAt = &fin
	_ = db.WithContext(ctx).Save(run).Error
	return cause
}

func persistPoolReconcileOutput(ctx context.Context, db *gorm.DB, out *PoolReconcileOutput, _ *SharedBaseMemoryIndex) error {
	if out == nil {
		return nil
	}
	var sysRepo *models.GitRepo
	var sysBundle *models.GitProtoBundle
	needSysRepo := false
	for _, st := range out.Files {
		if st.ProtoFileID == 0 && st.IsHarvestedBase {
			needSysRepo = true
			break
		}
	}
	if needSysRepo {
		var err error
		sysRepo, sysBundle, err = EnsureSystemSharedBaseRepoAndBundle(ctx, db)
		if err != nil {
			return err
		}
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		for _, st := range out.Files {
			impsJSON, _ := json.Marshal(nonNilStringsSlice(st.Imports))
			symsJSON, _ := json.Marshal(st.Symbols)
			var lmJSON datatypes.JSON
			if len(st.LineMap) > 0 {
				if b, err := json.Marshal(st.LineMap); err == nil {
					lmJSON = datatypes.JSON(b)
				}
			}

			if st.ProtoFileID == 0 && st.IsHarvestedBase && sysRepo != nil && sysBundle != nil {
				// 检查系统基座仓下是否已存在该逻辑路径记录，避免重复插入违反 uk_contract_pfile_bundle_path
				var existing models.ProtoFile
				_ = tx.Where("bundle_id = ? AND file_path = ? AND is_deleted = false", sysBundle.ID, st.LogicalPath).Limit(1).Find(&existing).Error
				if existing.ID > 0 {
					st.ProtoFileID = existing.ID
					st.GitRepoID = existing.GitRepoID
					st.BundleID = existing.BundleID
				} else {
					pf := models.ProtoFile{
						GitRepoID:         sysRepo.ID,
						BundleID:          sysBundle.ID,
						TargetSchemaName:  sysBundle.TargetSchemaName,
						Category:          models.ProtoCategoryBaseLib,
						FilePath:          st.LogicalPath,
						RepoFilePath:      st.RepoFilePath,
						CleanRelPath:      st.CleanRelPath,
						PackageName:       st.PackageName,
						RawBlobSHA:        st.RawBlobSHA,
						NormalizerVersion: ruleNormalizerBaseVersion,
						PipelineStatus:    st.PipelineStatus,
						StatusReason:      st.StatusReason,
						LineMapJSON:       lmJSON,
						IsExternalCopy:    st.IsExternalCopy,
						IsUServiceSource:  st.IsUServiceSource,
						InServiceClosure:  st.InServiceClosure,
						IsHarvestedBase:   true,
						AutoFixCount:      st.AutoFixCount,
						ImportsJSON:       datatypes.JSON(impsJSON),
						SymbolsJSON:       datatypes.JSON(symsJSON),
						ContentSHA256:     st.ContentSHA256,
						LineCount:         countNormalizedLines(st.Content),
						LatestVersionNum:  1,
						LatestCommitSHA:   "r4-harvest",
					}
					if err := tx.Create(&pf).Error; err != nil {
						return err
					}
					ver := models.ProtoFileVersion{
						ProtoFileID:    pf.ID,
						Version:        1,
						VersionTag:     "r4-harvest",
						SourceType:     models.ProtoSourceGitSync,
						CommitSHA:      "r4-harvest",
						CommitMessage:  "ADR-28 R4 共享基座自动收割",
						RawContentText: st.RawContent,
						ContentText:    st.Content,
						ContentSHA256:  st.ContentSHA256,
						LineCount:      pf.LineCount,
						CommittedAt:    now,
					}
					if err := tx.Create(&ver).Error; err != nil {
						return err
					}
					if err := tx.Model(&pf).Update("latest_version_id", ver.ID).Error; err != nil {
						return err
					}
					st.ProtoFileID = pf.ID
					st.GitRepoID = sysRepo.ID
					st.BundleID = sysBundle.ID
					continue
				}
			}

			if st.ProtoFileID == 0 {
				continue
			}

			updates := map[string]any{
				"file_path":          st.LogicalPath,
				"clean_rel_path":     st.CleanRelPath,
				"pipeline_status":    st.PipelineStatus,
				"status_reason":      st.StatusReason,
				"is_external_copy":   st.IsExternalCopy,
				"is_uservice_source": st.IsUServiceSource,
				"in_service_closure": st.InServiceClosure,
				"is_harvested_base":  st.IsHarvestedBase,
				"auto_fix_count":     st.AutoFixCount,
				"imports_json":       datatypes.JSON(impsJSON),
				"symbols_json":       datatypes.JSON(symsJSON),
				"content_sha256":     st.ContentSHA256,
				"line_count":         countNormalizedLines(st.Content),
			}
			if len(lmJSON) > 0 {
				updates["line_map_json"] = lmJSON
			}
			if err := tx.Model(&models.ProtoFile{}).Where("id = ?", st.ProtoFileID).Updates(updates).Error; err != nil {
				return err
			}
			if st.AutoFixCount > 0 || st.Content != "" {
				var curPF models.ProtoFile
				if err := tx.Select("id, latest_version_id").First(&curPF, st.ProtoFileID).Error; err == nil && curPF.LatestVersionID != nil && *curPF.LatestVersionID > 0 {
					_ = tx.Model(&models.ProtoFileVersion{}).Where("id = ?", *curPF.LatestVersionID).Updates(map[string]any{
						"content_text":   st.Content,
						"content_sha256": st.ContentSHA256,
						"line_count":     countNormalizedLines(st.Content),
					}).Error
				}
			}
		}
		return nil
	})
}
