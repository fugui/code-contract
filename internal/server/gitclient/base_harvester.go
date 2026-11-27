package gitclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"code-contract/models"

	"gorm.io/gorm"
)

const (
	// SystemSharedBasePlatformRepoID 为 R4 共享基座收割器在 contract_git_repos 中使用的固定系统保留平台 ID（免疫平台仓自动停用）。
	SystemSharedBasePlatformRepoID uint64 = 9000000001
	// SystemSharedBaseRepoName 为 R4 按需收割基座的平台系统仓名称。
	SystemSharedBaseRepoName = "_shared_base_harvested"
	// SystemSharedBaseBundleName 为 R4 系统基座仓下默认 BASE_LIB Bundle 名称。
	SystemSharedBaseBundleName = "shared-base-lib"
	// SystemSharedBaseSchemaName 为 R4 系统基座仓默认目标 Schema 名称。
	SystemSharedBaseSchemaName = "shared-base-lib"
)

// SharedBaseSourceCandidate 描述单次目录扫描索引中的一条共享基座 .proto 候选文件。
type SharedBaseSourceCandidate struct {
	AbsPath          string              `json:"abs_path"`
	RelPath          string              `json:"rel_path"`
	NormPath         string              `json:"norm_path"`
	Basename         string              `json:"basename"`
	Priority         int                 `json:"priority"`
	IsChannelA       bool                `json:"is_channel_a"`
	RawBytes         []byte              `json:"-"`
	CleanBytes       []byte              `json:"-"`
	RawBlobSHA       string              `json:"raw_blob_sha"`
	CleanSHA256      string              `json:"clean_sha256"`
	PackageName      string              `json:"package_name"`
	Imports          []string            `json:"imports"`
	Symbols          []ParsedProtoSymbol `json:"symbols"`
	IsUServiceSource bool                `json:"is_uservice_source"`
	LineMap          map[int]int         `json:"line_map,omitempty"`
	SyntaxOK         bool                `json:"syntax_ok"`
}

// SharedBaseMemoryIndex 为纯 Go 单次遍历构建的共享基座内存索引（绝不调用外部 find/protoc）。
type SharedBaseMemoryIndex struct {
	StripPG    bool
	Candidates []SharedBaseSourceCandidate
	ByNormPath map[string][]int
	ByBasename map[string][]int
	BySymbol   map[string][]int
}

type baseWalkRoot struct {
	absDir     string
	priority   int
	isChannelA bool
}

// BuildSharedBaseMemoryIndex 单次扫描 shared_base_sources（通道 A）与 codesDir 共享目录（通道 B 兜底），
// 严格免疫 R0 exclude_subsystems 与 exclude_globs 规则，经 Stage B v2 清洗与统一坐标归一后构建内存索引。
func BuildSharedBaseMemoryIndex(
	ctx context.Context,
	baseSources []SharedBaseSourceSpec,
	fallbackCodesDir string,
	govDoc *GovernanceConfigDoc,
) (*SharedBaseMemoryIndex, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if govDoc == nil {
		def := DefaultGovernanceConfig()
		govDoc = &def
	}
	stripPG := govDoc.Global.EffectiveStripPGSegment()

	// 构建不含 R0 exclude_globs 的基座专用清洗配置（绝对不误伤 **/il/**、**/alpha/** 等基座路径）
	baseCfg := *govDoc
	baseCfg.Global.ExcludeSubsystems = nil
	baseCfg.Global.ExcludeRepoPatterns = nil
	baseCfg.Global.ExcludeGlobs = nil
	norm := NewRuleBasedNormalizer(&baseCfg)
	dummyRepo := models.GitRepo{
		ID:             SystemSharedBasePlatformRepoID,
		Name:           SystemSharedBaseRepoName,
		PlatformRepoID: SystemSharedBasePlatformRepoID,
		SourceMode:     models.GitSourceModePlatformShared,
	}
	auxJSON := BuildEffectiveRepoAuxJSON(dummyRepo, &baseCfg)

	var roots []baseWalkRoot
	seenRoot := make(map[string]struct{})
	addRoot := func(dir string, prio int, channelA bool) {
		trimmed := strings.TrimSpace(dir)
		if trimmed == "" {
			return
		}
		candidates := []string{trimmed}
		if !filepath.IsAbs(trimmed) && strings.TrimSpace(fallbackCodesDir) != "" {
			candidates = append([]string{filepath.Join(fallbackCodesDir, filepath.FromSlash(trimmed))}, candidates...)
		}
		for _, c := range candidates {
			abs, err := filepath.Abs(c)
			if err != nil {
				continue
			}
			st, sErr := os.Stat(abs)
			if sErr != nil || !st.IsDir() {
				continue
			}
			if _, ok := seenRoot[abs]; ok {
				return
			}
			seenRoot[abs] = struct{}{}
			roots = append(roots, baseWalkRoot{
				absDir:     abs,
				priority:   prio,
				isChannelA: channelA,
			})
			return
		}
	}

	sortedSpecs := append([]SharedBaseSourceSpec(nil), baseSources...)
	sort.Slice(sortedSpecs, func(i, j int) bool {
		if sortedSpecs[i].Priority != sortedSpecs[j].Priority {
			return sortedSpecs[i].Priority < sortedSpecs[j].Priority
		}
		return sortedSpecs[i].PathHint < sortedSpecs[j].PathHint
	})
	for i, sp := range sortedSpecs {
		prio := sp.Priority
		if prio <= 0 {
			prio = i + 1
		}
		addRoot(sp.PathHint, prio, true)
	}

	// 通道 B 兜底基座目录（include / ltcommon 等），仅在显式根目录外补充扫描
	if strings.TrimSpace(fallbackCodesDir) != "" {
		absCodes, err := filepath.Abs(fallbackCodesDir)
		if err == nil {
			if entries, rErr := os.ReadDir(absCodes); rErr == nil {
				sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
				prio := 1000
				for _, ent := range entries {
					if !ent.IsDir() {
						continue
					}
					nameLower := strings.ToLower(ent.Name())
					if strings.Contains(nameLower, "include") || strings.Contains(nameLower, "ltcommon") || strings.Contains(nameLower, "simulator") {
						incSub := filepath.Join(absCodes, ent.Name(), "include")
						if st, sErr := os.Stat(incSub); sErr == nil && st.IsDir() {
							addRoot(incSub, prio, false)
							prio++
						}
						addRoot(filepath.Join(absCodes, ent.Name()), prio, false)
						prio++
					}
				}
			}
		}
	}

	var rawItems []SharedBaseSourceCandidate
	seenAbs := make(map[string]struct{})
	for _, root := range roots {
		_ = filepath.WalkDir(root.absDir, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".proto") {
				return nil
			}
			absFile, aErr := filepath.Abs(p)
			if aErr != nil {
				absFile = p
			}
			if _, ok := seenAbs[absFile]; ok {
				return nil
			}
			seenAbs[absFile] = struct{}{}

			rel, rErr := filepath.Rel(root.absDir, absFile)
			if rErr != nil {
				return nil
			}
			relSlash := filepath.ToSlash(rel)
			rawBytes, readErr := os.ReadFile(absFile)
			if readErr != nil {
				return nil
			}
			rawItems = append(rawItems, SharedBaseSourceCandidate{
				AbsPath:    absFile,
				RelPath:    relSlash,
				NormPath:   NormalizeLogicalPath(relSlash, stripPG),
				Basename:   path.Base(relSlash),
				Priority:   root.priority,
				IsChannelA: root.isChannelA,
				RawBytes:   rawBytes,
			})
			return nil
		})
	}

	sort.Slice(rawItems, func(i, j int) bool {
		if rawItems[i].Priority != rawItems[j].Priority {
			return rawItems[i].Priority < rawItems[j].Priority
		}
		if rawItems[i].NormPath != rawItems[j].NormPath {
			return rawItems[i].NormPath < rawItems[j].NormPath
		}
		return rawItems[i].AbsPath < rawItems[j].AbsPath
	})

	idx := &SharedBaseMemoryIndex{
		StripPG:    stripPG,
		Candidates: make([]SharedBaseSourceCandidate, 0, len(rawItems)),
		ByNormPath: make(map[string][]int),
		ByBasename: make(map[string][]int),
		BySymbol:   make(map[string][]int),
	}

	if len(rawItems) == 0 {
		return idx, nil
	}

	// 批量运行 Stage B v2 清洗，同时保留每个候选自己的 normPath 键
	for _, item := range rawItems {
		normOut, nErr := norm.Normalize(ctx, NormalizeInput{
			Repo:          dummyRepo,
			Files:         map[string][]byte{item.RelPath: item.RawBytes},
			AuxConfigJSON: auxJSON,
		})
		if nErr != nil {
			continue
		}
		res, ok := normOut.Files[item.RelPath]
		if !ok {
			continue
		}
		rawSum := sha256.Sum256(item.RawBytes)
		item.RawBlobSHA = hex.EncodeToString(rawSum[:])
		item.CleanBytes = res.CleanContent
		item.CleanSHA256 = res.CleanSHA256
		item.IsUServiceSource = res.IsUServiceSource
		item.LineMap = res.LineMap

		gate := ParseProtoSyntaxOnly(item.NormPath, res.CleanContent)
		item.SyntaxOK = gate.Passed
		if gate.Passed {
			item.PackageName = gate.PackageName
			item.Imports = gate.Imports
			item.Symbols = gate.Symbols
		}

		cIdx := len(idx.Candidates)
		idx.Candidates = append(idx.Candidates, item)
		idx.ByNormPath[item.NormPath] = append(idx.ByNormPath[item.NormPath], cIdx)
		idx.ByBasename[item.Basename] = append(idx.ByBasename[item.Basename], cIdx)
		for _, sym := range item.Symbols {
			if sym.FullName == "" {
				continue
			}
			idx.BySymbol[sym.FullName] = append(idx.BySymbol[sym.FullName], cIdx)
			if dot := strings.LastIndex(sym.FullName, "."); dot >= 0 && dot+1 < len(sym.FullName) {
				shortName := sym.FullName[dot+1:]
				idx.BySymbol[shortName] = append(idx.BySymbol[shortName], cIdx)
			}
		}
	}

	return idx, nil
}

func (idx *SharedBaseMemoryIndex) expandTransitive(seedIndices []int, alreadyHarvested map[string]bool) []SharedBaseSourceCandidate {
	if idx == nil || len(seedIndices) == 0 {
		return nil
	}
	visitedNorm := make(map[string]bool, len(alreadyHarvested)+len(seedIndices))
	for k, v := range alreadyHarvested {
		if v {
			visitedNorm[k] = true
		}
	}
	queue := append([]int(nil), seedIndices...)
	var out []SharedBaseSourceCandidate

	for len(queue) > 0 {
		curIdx := queue[0]
		queue = queue[1:]
		if curIdx < 0 || curIdx >= len(idx.Candidates) {
			continue
		}
		cand := idx.Candidates[curIdx]
		if visitedNorm[cand.NormPath] {
			continue
		}
		visitedNorm[cand.NormPath] = true
		out = append(out, cand)

		sortedImps := append([]string(nil), cand.Imports...)
		sort.Strings(sortedImps)
		for _, imp := range sortedImps {
			if IsWellKnownProtoImport(imp) {
				continue
			}
			normImp := NormalizeLogicalPath(imp, idx.StripPG)
			if visitedNorm[normImp] {
				continue
			}
			if hits := idx.ByNormPath[normImp]; len(hits) > 0 {
				queue = append(queue, hits[0])
				continue
			}
			base := path.Base(normImp)
			if baseHits := idx.ByBasename[base]; len(baseHits) > 0 {
				queue = append(queue, baseHits[0])
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].NormPath != out[j].NormPath {
			return out[i].NormPath < out[j].NormPath
		}
		return out[i].Priority < out[j].Priority
	})
	return out
}

// CollectChannelABases 收集通道 A（显式声明的 shared_base_sources）中全部共享基座及其递归依赖。
func (idx *SharedBaseMemoryIndex) CollectChannelABases(_ context.Context, alreadyInPool map[string]bool) []SharedBaseSourceCandidate {
	if idx == nil || len(idx.Candidates) == 0 {
		return nil
	}
	var seeds []int
	for i, c := range idx.Candidates {
		if c.IsChannelA {
			seeds = append(seeds, i)
		}
	}
	return idx.expandTransitive(seeds, alreadyInPool)
}

// HarvestOnDemandByImport 按缺失 import 路径从内存索引按需收割对应基座及其递归依赖。
func (idx *SharedBaseMemoryIndex) HarvestOnDemandByImport(importPath string, alreadyHarvested map[string]bool) []SharedBaseSourceCandidate {
	if idx == nil || IsWellKnownProtoImport(importPath) {
		return nil
	}
	normImp := NormalizeLogicalPath(importPath, idx.StripPG)
	if normImp == "" {
		return nil
	}
	if hits := idx.ByNormPath[normImp]; len(hits) > 0 {
		return idx.expandTransitive([]int{hits[0]}, alreadyHarvested)
	}
	base := path.Base(normImp)
	if baseHits := idx.ByBasename[base]; len(baseHits) > 0 {
		// 仅当 basename 唯一或命中单一规范路径时才按 basename 兜底收割
		distinctNorm := make(map[string]int)
		for _, bi := range baseHits {
			np := idx.Candidates[bi].NormPath
			if _, exists := distinctNorm[np]; !exists {
				distinctNorm[np] = bi
			}
		}
		if len(distinctNorm) == 1 {
			return idx.expandTransitive([]int{baseHits[0]}, alreadyHarvested)
		}
	}
	return nil
}

// HarvestOnDemandBySymbol 当未写 import 且符号在共享基座索引中唯一定义时，触发通道 B 按需收割。
func (idx *SharedBaseMemoryIndex) HarvestOnDemandBySymbol(symName string, alreadyHarvested map[string]bool) (*SharedBaseSourceCandidate, []SharedBaseSourceCandidate) {
	if idx == nil || strings.TrimSpace(symName) == "" {
		return nil, nil
	}
	trimmed := strings.TrimSpace(symName)
	hits := idx.BySymbol[trimmed]
	if len(hits) == 0 {
		return nil, nil
	}
	distinctNorm := make(map[string]int)
	for _, ci := range hits {
		np := idx.Candidates[ci].NormPath
		if _, exists := distinctNorm[np]; !exists {
			distinctNorm[np] = ci
		}
	}
	if len(distinctNorm) != 1 {
		return nil, nil
	}
	primaryIdx := hits[0]
	primary := idx.Candidates[primaryIdx]
	all := idx.expandTransitive([]int{primaryIdx}, alreadyHarvested)
	return &primary, all
}

// EnsureSystemSharedBaseRepoAndBundle 确保平台系统基座仓与 BASE_LIB Bundle 幂等存在，用于持久化 R4 收割资产。
func EnsureSystemSharedBaseRepoAndBundle(ctx context.Context, db *gorm.DB) (*models.GitRepo, *models.GitProtoBundle, error) {
	if db == nil {
		return nil, nil, errors.New("db 不能为空")
	}
	var repo models.GitRepo
	err := db.WithContext(ctx).Where("platform_repo_id = ?", SystemSharedBasePlatformRepoID).Limit(1).Find(&repo).Error
	if err != nil {
		return nil, nil, err
	}
	if repo.ID == 0 {
		repo = models.GitRepo{
			PlatformRepoID:  SystemSharedBasePlatformRepoID,
			SourceMode:      models.GitSourceModePlatformShared,
			SyncStrategy:    models.GitSyncStrategyLocalCacheOnly,
			ProtoStatus:     models.GitRepoProtoStatusHasProto,
			SyncEnabled:     true,
			SubsystemSlug:   "shared-base",
			Name:            SystemSharedBaseRepoName,
			Provider:        models.GitProviderGeneric,
			RepoURL:         "local://shared-base-harvested",
			DefaultBranch:   "main",
			AuthType:        models.GitAuthNone,
			SyncMode:        models.GitSyncManual,
			PollIntervalSec: 86400,
			AutoCompile:     true,
			LintOnSync:      false,
			LastSyncStatus:  models.GitSyncStatusSuccess,
			Description:     "ADR-28 R4 共享基座自动收割系统仓（免疫 R0 排除）",
		}
		if cErr := db.WithContext(ctx).Create(&repo).Error; cErr != nil {
			return nil, nil, cErr
		}
	} else if !repo.SyncEnabled || repo.ProtoStatus != models.GitRepoProtoStatusHasProto {
		_ = db.WithContext(ctx).Model(&repo).Updates(map[string]any{
			"sync_enabled": true,
			"proto_status": models.GitRepoProtoStatusHasProto,
		}).Error
		repo.SyncEnabled = true
		repo.ProtoStatus = models.GitRepoProtoStatusHasProto
	}

	var bundle models.GitProtoBundle
	if err := db.WithContext(ctx).Where("git_repo_id = ? AND bundle_name = ?", repo.ID, SystemSharedBaseBundleName).Limit(1).Find(&bundle).Error; err != nil {
		return nil, nil, err
	}
	if bundle.ID == 0 {
		bundle = models.GitProtoBundle{
			GitRepoID:        repo.ID,
			BundleName:       SystemSharedBaseBundleName,
			TargetSchemaName: SystemSharedBaseSchemaName,
			Category:         models.ProtoCategoryBaseLib,
			SubdirPath:       ".",
			AutoCompile:      true,
			AutoActivate:     false,
			MRAutoPublish:    models.MRAutoPublishDisabled,
			LintOnSync:       false,
			Description:      "R4 共享基座库聚合单元",
		}
		if cErr := db.WithContext(ctx).Create(&bundle).Error; cErr != nil {
			return nil, nil, cErr
		}
	}
	return &repo, &bundle, nil
}
