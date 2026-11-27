package gitclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"code-contract/models"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/reporter"
)

func init() {
	poolR7R8Hook = runR7R8Stage
	workspaceExportHook = exportReconciledWorkspace
}

type poolCompileError struct {
	File string
	Line int
	Col  int
	Msg  string
}

var (
	reMissingImportPatterns = []*regexp.Regexp{
		regexp.MustCompile(`import\s+"([^"]+)"\s+not found`),
		regexp.MustCompile(`could not resolve import\s+"([^"]+)"`),
		regexp.MustCompile(`open\s+([^\s:]+):\s+(?:file does not exist|no such file)`),
	}
	reUnknownTypePatterns = []*regexp.Regexp{
		regexp.MustCompile(`unknown\s+(?:request\s+|response\s+|extendee\s+)?type\s+"?([A-Za-z_][A-Za-z0-9_.]*)"?`),
		regexp.MustCompile(`unknown\s+extension\s+"?([A-Za-z_][A-Za-z0-9_.]*)"?`),
		regexp.MustCompile(`(?:type|symbol)\s+"?([A-Za-z_][A-Za-z0-9_.]*)"?\s+is not defined`),
		regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_.]*)\s+is not defined`),
	}
	reAlreadyDefinedSymbol = regexp.MustCompile(`(?:symbol|type|extension|enum value)\s+"?([A-Za-z_][A-Za-z0-9_.]*)"?\s+(?:already defined|is already defined)`)
	reImportPathLine       = regexp.MustCompile(`^\s*import\s+(?:(?:public|weak)\s+)?"([^"]+)"\s*;`)
)

// CompileClosureWithProtocompile 使用纯 Go bufbuild/protocompile 对活动逻辑文件映射执行批量编译并返回确定性错误列表。
func CompileClosureWithProtocompile(ctx context.Context, files map[string][]byte) ([]poolCompileError, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(files) == 0 {
		return nil, nil
	}

	targetPaths := make([]string, 0, len(files))
	for p := range files {
		targetPaths = append(targetPaths, p)
	}
	sort.Strings(targetPaths)

	var (
		mu   sync.Mutex
		errs []poolCompileError
	)

	rep := reporter.NewReporter(func(err reporter.ErrorWithPos) error {
		pos := err.GetPosition()
		msg := strings.TrimSpace(err.Error())
		if unwrapped := err.Unwrap(); unwrapped != nil {
			msg = strings.TrimSpace(unwrapped.Error())
		}
		mu.Lock()
		errs = append(errs, poolCompileError{
			File: strings.TrimPrefix(path.Clean(strings.ReplaceAll(pos.Filename, "\\", "/")), "/"),
			Line: pos.Line,
			Col:  pos.Col,
			Msg:  msg,
		})
		mu.Unlock()
		return nil
	}, nil)

	resolver := protocompile.WithStandardImports(&protocompile.SourceResolver{
		Accessor: func(filename string) (io.ReadCloser, error) {
			cleaned := strings.TrimPrefix(path.Clean(strings.ReplaceAll(filename, "\\", "/")), "/")
			b, ok := files[cleaned]
			if !ok {
				return nil, fmt.Errorf("file does not exist: %s", cleaned)
			}
			return io.NopCloser(strings.NewReader(string(b))), nil
		},
	})

	compiler := protocompile.Compiler{
		Resolver:       resolver,
		Reporter:       rep,
		MaxParallelism: 1,
	}

	_, compileErr := compiler.Compile(ctx, targetPaths...)

	mu.Lock()
	defer mu.Unlock()
	if compileErr != nil && len(errs) == 0 {
		errs = append(errs, poolCompileError{
			Msg: compileErr.Error(),
		})
	}

	sort.Slice(errs, func(i, j int) bool {
		if errs[i].File != errs[j].File {
			return errs[i].File < errs[j].File
		}
		if errs[i].Line != errs[j].Line {
			return errs[i].Line < errs[j].Line
		}
		if errs[i].Col != errs[j].Col {
			return errs[i].Col < errs[j].Col
		}
		return errs[i].Msg < errs[j].Msg
	})
	return errs, nil
}

// runR7R8Stage 执行纯 Go protocompile R7 四类联合自愈与 R8 根因优先固损隔离。
func runR7R8Stage(ctx context.Context, statesPtr *[]*PoolFileState, report *PoolReconcileReport, opts PoolReconcileOptions) error {
	if statesPtr == nil || report == nil {
		return nil
	}
	states := *statesPtr
	defer func() {
		*statesPtr = states
	}()

	maxR7 := opts.MaxR7Rounds
	if maxR7 <= 0 {
		maxR7 = 10
	}
	maxR8 := opts.MaxR8Rounds
	if maxR8 <= 0 {
		maxR8 = 150
	}

	alreadyHarvested := make(map[string]bool, len(states))
	for _, st := range states {
		if st.IsHarvestedBase {
			alreadyHarvested[st.LogicalPath] = true
		}
	}

	// fix(c) 防震荡已试导入集合：file_logical_path -> tried_import_paths
	triedSwapTargets := make(map[string]map[string]struct{})
	// fix(a) 防重复尝试集合
	triedFixARewrites := make(map[string]map[string]struct{})
	// variant swap 防重复切换：logical_path -> tried Candidate Key
	triedVariants := make(map[string]map[string]struct{})

	buildActiveAndIndexes := func() (
		activeByLP map[string]*PoolFileState,
		poolByLP map[string][]*PoolFileState,
		poolByBase map[string][]*PoolFileState,
		symToStates map[string][]*PoolFileState,
	) {
		activeByLP = make(map[string]*PoolFileState)
		poolByLP = make(map[string][]*PoolFileState)
		poolByBase = make(map[string][]*PoolFileState)
		symToStates = make(map[string][]*PoolFileState)

		sortPoolStates(states)
		for _, st := range states {
			if st.PipelineStatus == models.ProtoPipelineQuarantined {
				continue
			}
			poolByLP[st.LogicalPath] = append(poolByLP[st.LogicalPath], st)
			poolByBase[path.Base(st.LogicalPath)] = append(poolByBase[path.Base(st.LogicalPath)], st)

			if st.InServiceClosure && !st.IsExternalCopy {
				activeByLP[st.LogicalPath] = st
			}
		}

		// 构建全池确定性符号倒排索引（全名 pkg.Type 与末段短名 Type）
		for _, st := range states {
			if st.PipelineStatus == models.ProtoPipelineQuarantined || st.IsExternalCopy {
				continue
			}
			addSym := func(k string) {
				k = strings.TrimPrefix(strings.TrimSpace(k), ".")
				if k == "" {
					return
				}
				for _, prev := range symToStates[k] {
					if prev == st {
						return
					}
				}
				symToStates[k] = append(symToStates[k], st)
			}
			for _, sym := range st.Symbols {
				addSym(sym.FullName)
				if dot := strings.LastIndexByte(sym.FullName, '.'); dot >= 0 && dot+1 < len(sym.FullName) {
					addSym(sym.FullName[dot+1:])
				}
			}
		}
		for sym := range symToStates {
			sortPoolStates(symToStates[sym])
		}
		return activeByLP, poolByLP, poolByBase, symToStates
	}

	compileCurrentActive := func(activeByLP map[string]*PoolFileState) ([]poolCompileError, map[string][]poolCompileError) {
		fileBytes := make(map[string][]byte, len(activeByLP))
		for lp, st := range activeByLP {
			fileBytes[lp] = []byte(st.Content)
		}
		errs, _ := CompileClosureWithProtocompile(ctx, fileBytes)
		byFile := make(map[string][]poolCompileError)
		for _, e := range errs {
			if e.File != "" {
				byFile[e.File] = append(byFile[e.File], e)
			}
		}
		return errs, byFile
	}

	pullTransitiveIntoClosure := func(start *PoolFileState, poolByLP map[string][]*PoolFileState, poolByBase map[string][]*PoolFileState) {
		if start == nil {
			return
		}
		q := []*PoolFileState{start}
		seen := map[*PoolFileState]bool{start: true}
		for len(q) > 0 {
			cur := q[0]
			q = q[1:]
			cur.InServiceClosure = true
			for _, imp := range cur.Imports {
				if imp == "" || IsWellKnownProtoImport(imp) {
					continue
				}
				var next *PoolFileState
				if cands := poolByLP[imp]; len(cands) > 0 {
					for _, c := range cands {
						if !c.IsExternalCopy {
							next = c
							break
						}
					}
					if next == nil {
						next = cands[0]
					}
				} else if baseCands := poolByBase[path.Base(imp)]; len(baseCands) == 1 {
					next = baseCands[0]
				}
				if next != nil && next.PipelineStatus != models.ProtoPipelineQuarantined && !next.IsExternalCopy && !seen[next] {
					seen[next] = true
					q = append(q, next)
				}
			}
		}
	}

	var (
		lastErrors []poolCompileError
		errsByFile map[string][]poolCompileError
	)

	// R7 编译自愈回路（四类联合判定进展）
	if opts.EnableR7AutoFix {
		for round := 1; round <= maxR7; round++ {
			activeByLP, poolByLP, poolByBase, symToStates := buildActiveAndIndexes()
			lastErrors, errsByFile = compileCurrentActive(activeByLP)
			if len(lastErrors) == 0 {
				break
			}
			report.Stats.R7Rounds = round

			madeProgress := false
			editedInRound := make(map[string]bool, len(activeByLP))
			activeKeys := sortedActiveLogicalPaths(activeByLP)

			// ---- Class 1: fix(a) 错误 import 路径重写（带顶层非包符号碰撞护栏） ----
			for _, lp := range activeKeys {
				st := activeByLP[lp]
				missingImps := collectMissingImportsForState(st, errsByFile[lp], activeByLP)
				for _, badImp := range missingImps {
					if triedFixARewrites[lp] == nil {
						triedFixARewrites[lp] = make(map[string]struct{})
					}
					if _, seen := triedFixARewrites[lp][badImp]; seen {
						continue
					}

					var cand *PoolFileState
					base := path.Base(badImp)
					var matching []*PoolFileState
					for _, c := range poolByBase[base] {
						if c != st && !c.IsExternalCopy && c.PipelineStatus != models.ProtoPipelineQuarantined {
							matching = append(matching, c)
						}
					}
					if len(matching) == 1 {
						cand = matching[0]
					} else if len(matching) > 1 {
						// 优先同子系统或已有 active
						cand = pickBestActiveOrSubsystemCandidate(st, matching, activeByLP)
					} else if opts.BaseIndex != nil {
						// 尝试 R4 通道 B 按 import 收割
						harvested := opts.BaseIndex.HarvestOnDemandByImport(badImp, alreadyHarvested)
						for _, h := range harvested {
							if _, exists := poolByLP[h.NormPath]; !exists {
								alreadyHarvested[h.NormPath] = true
								hst := stateFromHarvestedCandidate(h, opts.StripPGSegment)
								hst.InServiceClosure = true
								states = append(states, hst)
								report.Harvested = append(report.Harvested, PoolHarvestedEntry{
									LogicalPath:    h.NormPath,
									SourceAbsPath:  h.AbsPath,
									SourcePriority: h.Priority,
									TriggeredBy:    "IMPORT",
								})
							}
						}
						activeByLP, poolByLP, poolByBase, symToStates = buildActiveAndIndexes()
						if after := poolByBase[base]; len(after) > 0 {
							cand = after[0]
						}
					}

					if cand == nil || cand.LogicalPath == badImp {
						continue
					}
					triedFixARewrites[lp][badImp] = struct{}{}

					// 顶层非包符号碰撞护栏
					if hasDirectImportSymbolCollision(st, badImp, cand, activeByLP, poolByLP) {
						continue
					}

					if newContent, ok := replaceImportPathInSource(st.Content, badImp, cand.LogicalPath); ok {
						st.Content = newContent
						refreshPoolStateAfterEdit(st)
						pullTransitiveIntoClosure(cand, poolByLP, poolByBase)
						activeByLP, poolByLP, poolByBase, symToStates = buildActiveAndIndexes()

						report.AutoFixes = append(report.AutoFixes, PoolAutoFixEntry{
							FilePath:  st.LogicalPath,
							RepoName:  st.RepoName,
							FixKind:   "fix(a)",
							OldImport: badImp,
							NewImport: cand.LogicalPath,
							Round:     round,
							RepoID:    st.GitRepoID,
							BundleID:  st.BundleID,
						})
						st.Diagnostics = append(st.Diagnostics, NormalizeDiagnostic{
							RepoFilePath: st.RepoFilePath,
							RuleID:       "AUTO_FIX_IMPORT_REWRITE",
							Severity:     "INFO",
							Line:         1,
							Message:      fmt.Sprintf("R7 fix(a): %s -> %s", badImp, cand.LogicalPath),
						})
						editedInRound[lp] = true
						madeProgress = true
					}
				}
			}

			// ---- Class 2: fix(b) 缺失符号补 import（同目录兄弟优先 + 全池索引 + R4 通道 B 符号收割） ----
			for _, lp := range activeKeys {
				st := activeByLP[lp]
				missingSyms := extractMissingSymbols(errsByFile[lp])
				for _, missSym := range missingSyms {
					target, newlyHarvested := resolveMissingSymbolTarget(st, missSym, activeByLP, poolByLP, symToStates, opts.BaseIndex, alreadyHarvested)
					if len(newlyHarvested) > 0 {
						for _, h := range newlyHarvested {
							if _, exists := poolByLP[h.NormPath]; !exists {
								alreadyHarvested[h.NormPath] = true
								hst := stateFromHarvestedCandidate(h, opts.StripPGSegment)
								hst.InServiceClosure = true
								states = append(states, hst)
								report.Harvested = append(report.Harvested, PoolHarvestedEntry{
									LogicalPath:    h.NormPath,
									SourceAbsPath:  h.AbsPath,
									SourcePriority: h.Priority,
									TriggeredBy:    "SYMBOL",
								})
								if target == nil && stateDefinesSymbol(hst, missSym, st.PackageName) {
									target = hst
								}
							}
						}
						activeByLP, poolByLP, poolByBase, symToStates = buildActiveAndIndexes()
					}
					if target == nil || target == st || stateAlreadyImports(st, target.LogicalPath) {
						continue
					}
					if hasDirectImportSymbolCollision(st, "", target, activeByLP, poolByLP) {
						continue
					}

					if newContent, newLM, ok := insertImportAndShiftLineMap(st.Content, st.LineMap, target.LogicalPath); ok {
						st.Content = newContent
						st.LineMap = newLM
						refreshPoolStateAfterEdit(st)
						pullTransitiveIntoClosure(target, poolByLP, poolByBase)
						activeByLP, poolByLP, poolByBase, symToStates = buildActiveAndIndexes()

						report.AutoFixes = append(report.AutoFixes, PoolAutoFixEntry{
							FilePath:  st.LogicalPath,
							RepoName:  st.RepoName,
							FixKind:   "fix(b)",
							NewImport: target.LogicalPath,
							Symbol:    missSym,
							Round:     round,
							RepoID:    st.GitRepoID,
							BundleID:  st.BundleID,
						})
						st.Diagnostics = append(st.Diagnostics, NormalizeDiagnostic{
							RepoFilePath: st.RepoFilePath,
							RuleID:       "AUTO_FIX_ADD_IMPORT",
							Severity:     "INFO",
							Line:         1,
							Message:      fmt.Sprintf("R7 fix(b): 为符号 %s 补录 import %q", missSym, target.LogicalPath),
						})
						editedInRound[lp] = true
						madeProgress = true
						break // 每轮每文件稳定推进一个缺失符号后进入下文件/下轮验证
					}
				}
			}

			// ---- Class 3: fix(c) 坏依赖替代切换（带防震荡已试集合） ----
			for _, lp := range activeKeys {
				if editedInRound[lp] {
					continue
				}
				st := activeByLP[lp]
				missingSyms := extractMissingSymbols(errsByFile[lp])
				if len(missingSyms) == 0 {
					continue
				}
				if triedSwapTargets[lp] == nil {
					triedSwapTargets[lp] = make(map[string]struct{})
				}
				swapped := false
				for _, missSym := range missingSyms {
					altTarget, _ := resolveMissingSymbolTarget(st, missSym, activeByLP, poolByLP, symToStates, nil, nil)
					if altTarget == nil || altTarget == st {
						continue
					}
					if _, alreadyTried := triedSwapTargets[lp][altTarget.LogicalPath]; alreadyTried {
						continue
					}
					if stateAlreadyImports(st, altTarget.LogicalPath) {
						continue
					}
					// 找到当前已引入却未提供 missSym 的关联旧依赖（优先同 package/同名 basename 或任一已引入未提供该符号者）
					badImp := findBadImportToSwap(st, missSym, altTarget, activeByLP, poolByLP)
					if badImp == "" || badImp == altTarget.LogicalPath {
						continue
					}
					triedSwapTargets[lp][badImp] = struct{}{}
					triedSwapTargets[lp][altTarget.LogicalPath] = struct{}{}

					if newContent, ok := replaceImportPathInSource(st.Content, badImp, altTarget.LogicalPath); ok {
						st.Content = newContent
						refreshPoolStateAfterEdit(st)
						if opts.ClosureOnly {
							recomputeClosureFromSeeds(states, poolByLP, poolByBase)
						}
						pullTransitiveIntoClosure(altTarget, poolByLP, poolByBase)
						activeByLP, poolByLP, poolByBase, symToStates = buildActiveAndIndexes()

						report.AutoFixes = append(report.AutoFixes, PoolAutoFixEntry{
							FilePath:  st.LogicalPath,
							RepoName:  st.RepoName,
							FixKind:   "fix(c)",
							OldImport: badImp,
							NewImport: altTarget.LogicalPath,
							Symbol:    missSym,
							Round:     round,
							RepoID:    st.GitRepoID,
							BundleID:  st.BundleID,
						})
						st.Diagnostics = append(st.Diagnostics, NormalizeDiagnostic{
							RepoFilePath: st.RepoFilePath,
							RuleID:       "AUTO_FIX_SWAP_BAD_DEP",
							Severity:     "INFO",
							Line:         1,
							Message:      fmt.Sprintf("R7 fix(c): 将坏依赖 %s 切换为 %s (缺失符号 %s)", badImp, altTarget.LogicalPath, missSym),
						})
						madeProgress = true
						swapped = true
						break
					}
				}
				if swapped {
					continue
				}
			}

			// ---- Class 4: variant_swap 同路径冲突候选回退替换 ----
			for _, lp := range activeKeys {
				st := activeByLP[lp]
				fileErrs := errsByFile[lp]
				if len(fileErrs) == 0 {
					continue
				}
				// 检查 st 自己是否是多候选赢家，或其引入的某多候选赢家缺少所需符号
				// 1) st 自身存在多候选且自身编译错误
				grpSelf := poolByLP[lp]
				if len(grpSelf) > 1 && !groupHasManualOwner(grpSelf) {
					if triedVariants[lp] == nil {
						triedVariants[lp] = make(map[string]struct{})
					}
					triedVariants[lp][variantCandidateKey(st)] = struct{}{}
					for _, alt := range grpSelf {
						if alt == st || alt.PipelineStatus == models.ProtoPipelineQuarantined {
							continue
						}
						if _, tried := triedVariants[lp][variantCandidateKey(alt)]; tried {
							continue
						}
						if _, parseErr := CompileClosureWithProtocompile(ctx, map[string][]byte{alt.LogicalPath: []byte(alt.Content)}); parseErr == nil {
							triedVariants[lp][variantCandidateKey(alt)] = struct{}{}
							prevRepo := st.RepoName
							st.ForcedVariantWinner = false
							st.IsExternalCopy = true
							alt.ForcedVariantWinner = true
							alt.IsExternalCopy = false
							alt.InServiceClosure = st.InServiceClosure
							report.AutoFixes = append(report.AutoFixes, PoolAutoFixEntry{
								FilePath:  lp,
								RepoName:  alt.RepoName,
								FixKind:   "variant_swap",
								OldImport: prevRepo,
								NewImport: alt.RepoName,
								Round:     round,
								RepoID:    alt.GitRepoID,
								BundleID:  alt.BundleID,
							})
							madeProgress = true
							break
						}
					}
				}
				if madeProgress {
					break
				}
				// 2) 下游 fileErrs 缺失某符号，检查当前引入的某冲突赢家的落选兄弟是否拥有该符号
				missSyms := extractMissingSymbols(fileErrs)
				for _, missSym := range missSyms {
					var swappedAny bool
					for _, imp := range st.Imports {
						grp := poolByLP[imp]
						if len(grp) <= 1 || groupHasManualOwner(grp) {
							continue
						}
						curWinner := activeByLP[imp]
						if curWinner == nil {
							continue
						}
						if stateDefinesSymbol(curWinner, missSym, st.PackageName) {
							continue
						}
						if triedVariants[imp] == nil {
							triedVariants[imp] = make(map[string]struct{})
						}
						triedVariants[imp][variantCandidateKey(curWinner)] = struct{}{}
						for _, alt := range grp {
							if alt == curWinner || alt.PipelineStatus == models.ProtoPipelineQuarantined {
								continue
							}
							if _, tried := triedVariants[imp][variantCandidateKey(alt)]; tried {
								continue
							}
							if stateDefinesSymbol(alt, missSym, st.PackageName) {
								triedVariants[imp][variantCandidateKey(alt)] = struct{}{}
								prevRepo := curWinner.RepoName
								curWinner.ForcedVariantWinner = false
								curWinner.IsExternalCopy = true
								alt.ForcedVariantWinner = true
								alt.IsExternalCopy = false
								alt.InServiceClosure = true
								pullTransitiveIntoClosure(alt, poolByLP, poolByBase)
								report.AutoFixes = append(report.AutoFixes, PoolAutoFixEntry{
									FilePath:  imp,
									RepoName:  alt.RepoName,
									FixKind:   "variant_swap",
									OldImport: prevRepo,
									NewImport: alt.RepoName,
									Symbol:    missSym,
									Round:     round,
									RepoID:    alt.GitRepoID,
									BundleID:  alt.BundleID,
								})
								madeProgress = true
								swappedAny = true
								break
							}
						}
						if swappedAny {
							break
						}
					}
					if swappedAny {
						break
					}
				}
			}

			if !madeProgress {
				break
			}
		}
	}

	// 在进 R8 前刷新一次编译结果
	activeByLP, _, _, _ := buildActiveAndIndexes()
	lastErrors, errsByFile = compileCurrentActive(activeByLP)

	// R8 根因优先固损隔离
	if opts.EnableR8Quarantine && len(lastErrors) > 0 {
		for round := 1; round <= maxR8; round++ {
			activeByLP, _, _, _ = buildActiveAndIndexes()
			lastErrors, errsByFile = compileCurrentActive(activeByLP)
			if len(lastErrors) == 0 {
				break
			}
			report.Stats.R8Rounds = round

			victim, isRootCause, errCnt, reason := selectR8QuarantineTarget(activeByLP, lastErrors, errsByFile)
			if victim == nil {
				break
			}
			victim.PipelineStatus = models.ProtoPipelineQuarantined
			victim.InServiceClosure = false
			if victim.ManualOwnerBundleID > 0 && victim.BundleID == victim.ManualOwnerBundleID {
				reason = "人工仲裁文件编译失败，请复核仲裁: " + reason
			}
			victim.StatusReason = reason
			report.Quarantined = append(report.Quarantined, PoolQuarantinedEntry{
				LogicalPath:  victim.LogicalPath,
				RepoName:     victim.RepoName,
				RepoFilePath: victim.RepoFilePath,
				Reason:       reason,
				ErrorCount:   errCnt,
				IsRootCause:  isRootCause,
			})
		}
		// 最终验证计数
		activeByLP, _, _, _ = buildActiveAndIndexes()
		lastErrors, _ = compileCurrentActive(activeByLP)
	}

	report.Stats.CompileErrorCount = len(lastErrors)
	sort.Slice(report.Quarantined, func(i, j int) bool {
		if report.Quarantined[i].LogicalPath != report.Quarantined[j].LogicalPath {
			return report.Quarantined[i].LogicalPath < report.Quarantined[j].LogicalPath
		}
		return report.Quarantined[i].RepoName < report.Quarantined[j].RepoName
	})
	return nil
}

func sortedActiveLogicalPaths(m map[string]*PoolFileState) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func groupHasManualOwner(grp []*PoolFileState) bool {
	for _, st := range grp {
		if st != nil && st.ManualOwnerBundleID > 0 && st.BundleID == st.ManualOwnerBundleID {
			return true
		}
	}
	return false
}

func variantCandidateKey(st *PoolFileState) string {
	return fmt.Sprintf("%d:%s", st.GitRepoID, st.RepoFilePath)
}

func collectMissingImportsForState(st *PoolFileState, fileErrs []poolCompileError, activeByLP map[string]*PoolFileState) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(imp string) {
		imp = strings.TrimSpace(imp)
		if imp == "" || IsWellKnownProtoImport(imp) {
			return
		}
		if _, ok := activeByLP[imp]; ok {
			return
		}
		if _, exists := seen[imp]; !exists {
			seen[imp] = struct{}{}
			out = append(out, imp)
		}
	}
	for _, imp := range st.Imports {
		add(imp)
	}
	for _, e := range fileErrs {
		for _, re := range reMissingImportPatterns {
			if m := re.FindStringSubmatch(e.Msg); len(m) >= 2 {
				add(m[1])
			}
		}
	}
	sort.Strings(out)
	return out
}

func extractMissingSymbols(fileErrs []poolCompileError) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(sym string) {
		sym = strings.TrimPrefix(strings.TrimSpace(sym), ".")
		if sym == "" || strings.HasSuffix(sym, ".proto") {
			return
		}
		if _, exists := seen[sym]; !exists {
			seen[sym] = struct{}{}
			out = append(out, sym)
		}
	}
	for _, e := range fileErrs {
		for _, re := range reUnknownTypePatterns {
			if m := re.FindStringSubmatch(e.Msg); len(m) >= 2 {
				add(m[1])
			}
		}
	}
	sort.Strings(out)
	return out
}

func stateDefinesSymbol(st *PoolFileState, symName string, callerPkg string) bool {
	if st == nil {
		return false
	}
	target := strings.TrimPrefix(strings.TrimSpace(symName), ".")
	if target == "" {
		return false
	}
	qualifiedCaller := ""
	if callerPkg != "" && !strings.Contains(target, ".") {
		qualifiedCaller = callerPkg + "." + target
	}
	for _, s := range st.Symbols {
		fn := strings.TrimPrefix(s.FullName, ".")
		if fn == target || (qualifiedCaller != "" && fn == qualifiedCaller) {
			return true
		}
		if !strings.Contains(target, ".") && strings.HasSuffix(fn, "."+target) && st.PackageName == callerPkg {
			return true
		}
		if strings.Contains(target, ".") && strings.HasSuffix(fn, "."+target) {
			return true
		}
	}
	return false
}

func stateAlreadyImports(st *PoolFileState, targetLP string) bool {
	for _, imp := range st.Imports {
		if imp == targetLP {
			return true
		}
	}
	return false
}

func pickBestActiveOrSubsystemCandidate(caller *PoolFileState, cands []*PoolFileState, activeByLP map[string]*PoolFileState) *PoolFileState {
	sorted := append([]*PoolFileState(nil), cands...)
	sort.Slice(sorted, func(i, j int) bool {
		_, ai := activeByLP[sorted[i].LogicalPath]
		_, aj := activeByLP[sorted[j].LogicalPath]
		if ai != aj {
			return ai
		}
		si := strings.EqualFold(sorted[i].SubsystemSlug, caller.SubsystemSlug)
		sj := strings.EqualFold(sorted[j].SubsystemSlug, caller.SubsystemSlug)
		if si != sj {
			return si
		}
		return sorted[i].LogicalPath < sorted[j].LogicalPath
	})
	return sorted[0]
}

func resolveMissingSymbolTarget(
	caller *PoolFileState,
	missSym string,
	activeByLP map[string]*PoolFileState,
	poolByLP map[string][]*PoolFileState,
	symToStates map[string][]*PoolFileState,
	baseIndex *SharedBaseMemoryIndex,
	alreadyHarvested map[string]bool,
) (*PoolFileState, []SharedBaseSourceCandidate) {
	callerDir := path.Dir(caller.LogicalPath)
	if callerDir == "." {
		callerDir = ""
	}

	// 1. 同目录兄弟文件优先（无论是否同 pkg，均先核对同目录是否存在精确/短名符号）
	var dirSiblings []*PoolFileState
	for _, grp := range poolByLP {
		for _, cand := range grp {
			if cand == caller || cand.IsExternalCopy || cand.PipelineStatus == models.ProtoPipelineQuarantined {
				continue
			}
			cDir := path.Dir(cand.LogicalPath)
			if cDir == "." {
				cDir = ""
			}
			if cDir == callerDir && stateDefinesSymbol(cand, missSym, caller.PackageName) {
				dirSiblings = append(dirSiblings, cand)
			}
		}
	}
	if len(dirSiblings) == 1 {
		return dirSiblings[0], nil
	}
	if len(dirSiblings) > 1 {
		return pickBestActiveOrSubsystemCandidate(caller, dirSiblings, activeByLP), nil
	}

	// 2. 全池符号倒排索引匹配（完整名或同包限定名优先于裸短名）
	lookupKeys := []string{missSym}
	if caller.PackageName != "" && !strings.Contains(missSym, ".") {
		lookupKeys = append([]string{caller.PackageName + "." + missSym}, lookupKeys...)
	}
	for _, key := range lookupKeys {
		cands := symToStates[key]
		var valid []*PoolFileState
		for _, c := range cands {
			if c != caller && !c.IsExternalCopy && c.PipelineStatus != models.ProtoPipelineQuarantined {
				valid = append(valid, c)
			}
		}
		if len(valid) == 1 {
			return valid[0], nil
		}
		if len(valid) > 1 {
			// 若多个候选全部同属一个 LogicalPath（只取 active 或首项），否则选最匹配者
			return pickBestActiveOrSubsystemCandidate(caller, valid, activeByLP), nil
		}
	}

	// 3. R4 通道 B：按符号名在共享基座索引中唯一收割
	if baseIndex != nil {
		for _, key := range lookupKeys {
			primary, harvested := baseIndex.HarvestOnDemandBySymbol(key, alreadyHarvested)
			if primary != nil && len(harvested) > 0 {
				return nil, harvested
			}
		}
	}
	return nil, nil
}

// hasDirectImportSymbolCollision 检测引入 candidate 是否与 caller 自身或其他直接依赖存在顶层非包符号重定义碰撞。
func hasDirectImportSymbolCollision(
	caller *PoolFileState,
	replacingImp string,
	candidate *PoolFileState,
	activeByLP map[string]*PoolFileState,
	poolByLP map[string][]*PoolFileState,
) bool {
	if candidate == nil || len(candidate.Symbols) == 0 {
		return false
	}
	existingSyms := make(map[string]struct{})
	for _, s := range caller.Symbols {
		if s.FullName != "" {
			existingSyms[s.FullName] = struct{}{}
		}
	}
	for _, imp := range caller.Imports {
		if imp == "" || imp == replacingImp || imp == candidate.LogicalPath {
			continue
		}
		dep := activeByLP[imp]
		if dep == nil {
			if cands := poolByLP[imp]; len(cands) > 0 {
				dep = cands[0]
			}
		}
		if dep == nil {
			continue
		}
		for _, s := range dep.Symbols {
			if s.FullName != "" {
				existingSyms[s.FullName] = struct{}{}
			}
		}
	}
	for _, s := range candidate.Symbols {
		if s.FullName == "" {
			continue
		}
		if _, collision := existingSyms[s.FullName]; collision {
			return true
		}
	}
	return false
}

func findBadImportToSwap(
	st *PoolFileState,
	missSym string,
	altTarget *PoolFileState,
	activeByLP map[string]*PoolFileState,
	poolByLP map[string][]*PoolFileState,
) string {
	altBase := path.Base(altTarget.LogicalPath)
	// 第一优先级：同 basename 或同包，但当前并不包含 missSym 的已导入路径
	for _, imp := range st.Imports {
		if imp == "" || IsWellKnownProtoImport(imp) {
			continue
		}
		cur := activeByLP[imp]
		if cur == nil {
			if grp := poolByLP[imp]; len(grp) > 0 {
				cur = grp[0]
			}
		}
		if cur == nil {
			// 未解析路径也可直接替换
			if path.Base(imp) == altBase {
				return imp
			}
			continue
		}
		if stateDefinesSymbol(cur, missSym, st.PackageName) {
			continue
		}
		if path.Base(imp) == altBase || (altTarget.PackageName != "" && cur.PackageName == altTarget.PackageName) {
			return imp
		}
		if cur.PipelineStatus == models.ProtoPipelineQuarantined {
			return imp
		}
	}
	return ""
}

func replaceImportPathInSource(content, oldImp, newImp string) (string, bool) {
	lines := strings.Split(content, "\n")
	changed := false
	for i, ln := range lines {
		if m := reImportPathLine.FindStringSubmatch(ln); len(m) >= 2 && m[1] == oldImp {
			lines[i] = strings.Replace(ln, `"`+oldImp+`"`, `"`+newImp+`"`, 1)
			changed = true
		}
	}
	if !changed {
		return content, false
	}
	return strings.Join(lines, "\n"), true
}

func insertImportAndShiftLineMap(content string, oldLM map[int]int, newImp string) (string, map[int]int, bool) {
	lines := strings.Split(content, "\n")
	insertIdx := 0
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "syntax") ||
			strings.HasPrefix(trimmed, "edition") ||
			strings.HasPrefix(trimmed, "package ") ||
			strings.HasPrefix(trimmed, "import ") {
			insertIdx = i + 1
		}
	}

	stmt := fmt.Sprintf(`import "%s";`, newImp)
	for _, ln := range lines {
		if m := reImportPathLine.FindStringSubmatch(ln); len(m) >= 2 && m[1] == newImp {
			return content, oldLM, false
		}
	}

	newLines := make([]string, 0, len(lines)+1)
	newLines = append(newLines, lines[:insertIdx]...)
	newLines = append(newLines, stmt)
	newLines = append(newLines, lines[insertIdx:]...)

	// 重算行号映射：newLineNo (1-based) -> origLineNo (1-based, 插入行置 0)
	newLM := make(map[int]int, len(newLines))
	insertedLine1Based := insertIdx + 1
	for new1 := 1; new1 <= len(newLines); new1++ {
		if new1 < insertedLine1Based {
			if len(oldLM) > 0 {
				if orig, ok := oldLM[new1]; ok {
					newLM[new1] = orig
				}
			} else {
				newLM[new1] = new1
			}
		} else if new1 == insertedLine1Based {
			newLM[new1] = 0
		} else {
			prev1 := new1 - 1
			if len(oldLM) > 0 {
				if orig, ok := oldLM[prev1]; ok {
					newLM[new1] = orig
				}
			} else {
				newLM[new1] = prev1
			}
		}
	}

	return strings.Join(newLines, "\n"), newLM, true
}

func refreshPoolStateAfterEdit(st *PoolFileState) {
	if st == nil {
		return
	}
	st.AutoFixCount++
	sum := sha256.Sum256([]byte(st.Content))
	st.ContentSHA256 = hex.EncodeToString(sum[:])
	gate := ParseProtoSyntaxOnly(st.LogicalPath, []byte(st.Content))
	if gate.Passed {
		st.PackageName = gate.PackageName
		st.Imports = gate.Imports
		st.Symbols = gate.Symbols
	} else {
		// 即使局部仍有其他缺失引用或暂解析失败，也更新 Imports 文本列表确保一致性
		var imps []string
		for _, ln := range strings.Split(st.Content, "\n") {
			if m := reImportPathLine.FindStringSubmatch(ln); len(m) >= 2 {
				imps = append(imps, m[1])
			}
		}
		if len(imps) > 0 {
			st.Imports = imps
		}
	}
}

// selectR8QuarantineTarget R8 根因优先评分选中唯一最高危害文件：
// 语法错误 / 符号重复定义 / 多包声明等根因错误权重大于下游 unknown/not defined 受害者错误。
func selectR8QuarantineTarget(
	activeByLP map[string]*PoolFileState,
	allErrs []poolCompileError,
	errsByFile map[string][]poolCompileError,
) (victim *PoolFileState, isRootCause bool, errCount int, reason string) {
	type candidateScore struct {
		st          *PoolFileState
		score       int
		errCount    int
		isRootCause bool
		firstReason string
	}

	// 建立符号 -> 定义文件（用于将 duplicate/already defined 回溯追责给上游根因定义者）
	dupSymbolFiles := make(map[string][]*PoolFileState)
	for _, st := range activeByLP {
		for _, s := range st.Symbols {
			if s.FullName != "" {
				dupSymbolFiles[s.FullName] = append(dupSymbolFiles[s.FullName], st)
			}
		}
	}

	scores := make(map[string]*candidateScore)
	ensure := func(lp string) *candidateScore {
		if sc, ok := scores[lp]; ok {
			return sc
		}
		st := activeByLP[lp]
		if st == nil {
			return nil
		}
		sc := &candidateScore{st: st}
		scores[lp] = sc
		return sc
	}

	for lp, errs := range errsByFile {
		sc := ensure(lp)
		if sc == nil {
			continue
		}
		sc.errCount += len(errs)
		for _, e := range errs {
			if sc.firstReason == "" {
				if e.Line > 0 {
					sc.firstReason = fmt.Sprintf("L%d:%d %s", e.Line, e.Col, e.Msg)
				} else {
					sc.firstReason = e.Msg
				}
			}
			lower := strings.ToLower(e.Msg)
			switch {
			case strings.Contains(lower, "already defined") ||
				strings.Contains(lower, "duplicate") ||
				strings.Contains(lower, "multiple package") ||
				strings.Contains(lower, "syntax error") ||
				strings.Contains(lower, "unexpected") ||
				strings.Contains(lower, "invalid") ||
				strings.Contains(lower, "parse failed") ||
				strings.Contains(lower, "option") && strings.Contains(lower, "unknown"):
				sc.score += 100
				sc.isRootCause = true
			case strings.Contains(lower, "import") && (strings.Contains(lower, "not found") || strings.Contains(lower, "could not resolve")):
				sc.score += 20
			case strings.Contains(lower, "unknown ") || strings.Contains(lower, "not defined"):
				// 受害者报错权重明显低于根因
				sc.score += 1
			default:
				sc.score += 15
			}
			// 若是符号重定义，提高参与冲突的所有上游文件根因权重
			if m := reAlreadyDefinedSymbol.FindStringSubmatch(e.Msg); len(m) >= 2 {
				for _, owner := range dupSymbolFiles[strings.TrimPrefix(m[1], ".")] {
					if osc := ensure(owner.LogicalPath); osc != nil {
						osc.score += 80
						osc.isRootCause = true
						if osc.firstReason == "" {
							osc.firstReason = "符号冲突根因: " + e.Msg
						}
					}
				}
			}
		}
	}

	// 若仍有未带文件名的全局错误，兜底归入 active 中首文件
	if len(scores) == 0 && len(allErrs) > 0 {
		for _, lp := range sortedActiveLogicalPaths(activeByLP) {
			st := activeByLP[lp]
			return st, true, len(allErrs), allErrs[0].Msg
		}
	}

	var ordered []*candidateScore
	for _, sc := range scores {
		ordered = append(ordered, sc)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].score != ordered[j].score {
			return ordered[i].score > ordered[j].score
		}
		if ordered[i].errCount != ordered[j].errCount {
			return ordered[i].errCount > ordered[j].errCount
		}
		return ordered[i].st.LogicalPath < ordered[j].st.LogicalPath
	})
	if len(ordered) == 0 {
		return nil, false, 0, ""
	}
	best := ordered[0]
	if best.firstReason == "" && len(allErrs) > 0 {
		best.firstReason = allErrs[0].Msg
	}
	return best.st, best.isRootCause, best.errCount, best.firstReason
}

func exportReconciledWorkspace(protosDir string, runID uint64, out *PoolReconcileOutput) (string, string, error) {
	if out == nil {
		return "", "", nil
	}
	mgr := NewProtoTreeManager(protosDir)
	files := make(map[string][]byte)
	for _, st := range out.Files {
		if st.PipelineStatus == models.ProtoPipelineQuarantined || st.IsExternalCopy {
			continue
		}
		if !st.InServiceClosure {
			continue
		}
		if st.LogicalPath == "" {
			continue
		}
		files[st.LogicalPath] = []byte(st.Content)
	}
	return mgr.WriteAndSwitchWorkspace(runID, files, &out.Report)
}

func stateHasService(st *PoolFileState) bool {
	if st == nil {
		return false
	}
	for _, s := range st.Symbols {
		if strings.EqualFold(s.Kind, "SERVICE") {
			return true
		}
	}
	return false
}

func recomputeClosureFromSeeds(states []*PoolFileState, poolByLP map[string][]*PoolFileState, poolByBase map[string][]*PoolFileState) {
	var queue []*PoolFileState
	seen := make(map[*PoolFileState]bool, len(states))
	for _, st := range states {
		if st.PipelineStatus == models.ProtoPipelineQuarantined || st.IsExternalCopy {
			st.InServiceClosure = false
			continue
		}
		if st.IsUServiceSource || stateHasService(st) {
			st.InServiceClosure = true
			seen[st] = true
			queue = append(queue, st)
		} else {
			st.InServiceClosure = false
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, imp := range cur.Imports {
			if imp == "" || IsWellKnownProtoImport(imp) {
				continue
			}
			var next *PoolFileState
			if cands := poolByLP[imp]; len(cands) > 0 {
				for _, c := range cands {
					if !c.IsExternalCopy {
						next = c
						break
					}
				}
				if next == nil {
					next = cands[0]
				}
			} else if baseCands := poolByBase[path.Base(imp)]; len(baseCands) == 1 {
				next = baseCands[0]
			}
			if next != nil && next.PipelineStatus != models.ProtoPipelineQuarantined && !next.IsExternalCopy && !seen[next] {
				next.InServiceClosure = true
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
}
