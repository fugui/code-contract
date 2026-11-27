package gitclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"code-contract/models"

	"gorm.io/gorm"
)

const ruleNormalizerBaseVersion = "rule-normalizer/v1"

// RuleBasedNormalizer 实现 ADR-27 三层叠加 Stage B 确定性清理整顿器。
type RuleBasedNormalizer struct {
	Config       *GovernanceConfigDoc
	ImportOwners map[string]string
}

// NewRuleBasedNormalizer 创建基于规则的 Stage B 清理整顿器。
func NewRuleBasedNormalizer(cfg *GovernanceConfigDoc) *RuleBasedNormalizer {
	if cfg == nil {
		def := DefaultGovernanceConfig()
		cfg = &def
	}
	return &RuleBasedNormalizer{
		Config:       cfg,
		ImportOwners: map[string]string{},
	}
}

// NewRuleBasedNormalizerWithOwners 创建含权威导入路径表的 Stage B 清理整顿器。
func NewRuleBasedNormalizerWithOwners(cfg *GovernanceConfigDoc, owners map[string]string) *RuleBasedNormalizer {
	n := NewRuleBasedNormalizer(cfg)
	if owners != nil {
		n.ImportOwners = owners
	}
	return n
}

// Version 返回规则库基础版本号。
func (n *RuleBasedNormalizer) Version() string {
	return ruleNormalizerBaseVersion
}

type trackedLine struct {
	text    string
	rawLine int
}

type effectiveRepoRules struct {
	enabled          map[string]bool
	defaultSyntax    string
	stripPrefixes    []string
	pathRewrites     map[string]string
	regexRewrites    []RegexRewriteRule
	importRewrites   map[string]string
	exemptSet        map[string]struct{}
	quarantinedSet   map[string]struct{}
	appliedRuleNames map[string]struct{}
}

var (
	syntaxDeclRegex    = regexp.MustCompile(`(?m)^\s*syntax\s*=\s*["']proto[23]["']\s*;`)
	preprocessorRegex  = regexp.MustCompile(`^\s*#\s*(include|define|undef|ifdef|ifndef|if|else|elif|endif|pragma)\b`)
	dophiMacroLineRe   = regexp.MustCompile(`^\s*(DOPHI_DECLARE_MESSAGE|DOPHI_EXPORT|DECLARE_MSG|END_DECLARE_MSG)\b.*$`)
	packageStmtRegex   = regexp.MustCompile(`^(\s*package\s+)([^;]+)(;\s*(?://.*)?)$`)
	importStmtRegex    = regexp.MustCompile(`^(\s*import\s+(?:public\s+|weak\s+)?)["']([^"']+)["'](\s*;\s*(?://.*)?)$`)
	invalidPkgCharRe   = regexp.MustCompile(`[^A-Za-z0-9_.]`)
	collapseDotPkgRe   = regexp.MustCompile(`\.{2,}`)
	trailingCommaBOMRe = regexp.MustCompile(`\x{FEFF}`)
)

func (n *RuleBasedNormalizer) resolveRepoRules(repo models.GitRepo) effectiveRepoRules {
	doc := DefaultGovernanceConfig()
	if n != nil && n.Config != nil {
		doc = *n.Config
	}

	enabled := make(map[string]bool, len(validBuiltinRuleIDs))
	if len(doc.Global.EnabledRules) == 0 {
		for id := range validBuiltinRuleIDs {
			enabled[id] = true
		}
	} else {
		for _, id := range doc.Global.EnabledRules {
			enabled[strings.TrimSpace(id)] = true
		}
	}

	syn := strings.TrimSpace(doc.Global.DefaultSyntax)
	if syn != "proto2" && syn != "proto3" {
		syn = "proto3"
	}

	importRewrites := make(map[string]string, len(doc.Global.ImportRewrites))
	for k, v := range doc.Global.ImportRewrites {
		importRewrites[k] = v
	}
	regexRewrites := append([]RegexRewriteRule(nil), doc.Global.RegexRewrites...)

	repoCfg := ParseRepoNormalizeConfig(repo.RepoNormalizeConfigJSON)
	stripPrefixes := repo.StripPrefixes()
	pathRewrites := repo.PathRewrites()

	if spec, ok := matchRepoSpecInDoc(repo, &doc); ok {
		for _, d := range spec.DisabledRules {
			enabled[strings.TrimSpace(d)] = false
		}
		if len(spec.StripPrefixes) > 0 {
			stripPrefixes = spec.StripPrefixes
		}
		if len(spec.PathRewrites) > 0 {
			pathRewrites = spec.PathRewrites
		}
		regexRewrites = append(regexRewrites, spec.RegexRewrites...)
		for k, v := range spec.ImportRewrites {
			importRewrites[k] = v
		}
		for _, ex := range spec.ExemptFiles {
			repoCfg.ExemptFiles = append(repoCfg.ExemptFiles, ex)
		}
		for _, q := range spec.QuarantinedFiles {
			repoCfg.QuarantinedFiles = append(repoCfg.QuarantinedFiles, q)
		}
	}

	for _, d := range repoCfg.DisabledRules {
		enabled[strings.TrimSpace(d)] = false
	}
	regexRewrites = append(regexRewrites, repoCfg.RegexRewrites...)
	for k, v := range repoCfg.ImportRewrites {
		importRewrites[k] = v
	}

	exemptSet := make(map[string]struct{}, len(repoCfg.ExemptFiles))
	for _, f := range repoCfg.ExemptFiles {
		if trimmed := strings.TrimSpace(f); trimmed != "" {
			exemptSet[trimmed] = struct{}{}
		}
	}
	quarantinedSet := make(map[string]struct{}, len(repoCfg.QuarantinedFiles))
	for _, f := range repoCfg.QuarantinedFiles {
		if trimmed := strings.TrimSpace(f); trimmed != "" {
			quarantinedSet[trimmed] = struct{}{}
		}
	}

	return effectiveRepoRules{
		enabled:          enabled,
		defaultSyntax:    syn,
		stripPrefixes:    stripPrefixes,
		pathRewrites:     pathRewrites,
		regexRewrites:    regexRewrites,
		importRewrites:   importRewrites,
		exemptSet:        exemptSet,
		quarantinedSet:   quarantinedSet,
		appliedRuleNames: make(map[string]struct{}),
	}
}

func matchesPathRuleSet(ruleSet map[string]struct{}, repoRelPath string) bool {
	if _, ok := ruleSet[repoRelPath]; ok {
		return true
	}
	for pat := range ruleSet {
		if matched, _ := path.Match(pat, repoRelPath); matched {
			return true
		}
	}
	return false
}

// Normalize 对单仓全部白名单文件执行三层叠加清理整顿与精确 line_map 生成。
func (n *RuleBasedNormalizer) Normalize(_ context.Context, in NormalizeInput) (NormalizeOutput, error) {
	ver := EffectiveNormalizerVersion(n, in.AuxConfigJSON)
	out := NormalizeOutput{
		NormalizerVersion: ver,
		Files:             make(map[string]NormalizedFileResult, len(in.Files)),
	}

	keys := make([]string, 0, len(in.Files))
	for k := range in.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rules := n.resolveRepoRules(in.Repo)

	for _, rp := range keys {
		raw := in.Files[rp]
		if matchesPathRuleSet(rules.quarantinedSet, rp) {
			sum := sha256.Sum256(raw)
			out.Files[rp] = NormalizedFileResult{
				RepoFilePath:   rp,
				CleanContent:   append([]byte(nil), raw...),
				CleanSHA256:    hex.EncodeToString(sum[:]),
				LineCount:      countNormalizedLines(string(raw)),
				LinePreserving: true,
				Diagnostics: []NormalizeDiagnostic{
					{
						RepoFilePath: rp,
						RuleID:       "FILE_QUARANTINED",
						Severity:     "ERROR",
						Line:         1,
						Column:       1,
						Message:      "文件已被治理配置标记为文件级隔离（quarantined_files）",
					},
				},
			}
			continue
		}

		if matchesPathRuleSet(rules.exemptSet, rp) {
			sum := sha256.Sum256(raw)
			out.Files[rp] = NormalizedFileResult{
				RepoFilePath:   rp,
				CleanContent:   append([]byte(nil), raw...),
				CleanSHA256:    hex.EncodeToString(sum[:]),
				LineCount:      countNormalizedLines(string(raw)),
				LinePreserving: true,
				Diagnostics: []NormalizeDiagnostic{
					{
						RepoFilePath: rp,
						RuleID:       "FILE_EXEMPT",
						Severity:     "INFO",
						Line:         1,
						Column:       1,
						Message:      "文件命中白名单豁免规则（exempt_files），已原样透传",
					},
				},
			}
			continue
		}

		res := n.normalizeSingleFile(rp, raw, rules)
		out.Files[rp] = res
	}
	return out, nil
}

func (n *RuleBasedNormalizer) normalizeSingleFile(rp string, raw []byte, rules effectiveRepoRules) NormalizedFileResult {
	var diags []NormalizeDiagnostic
	text := string(raw)

	// NORM-001: 剥离 UTF-8 BOM 并统一换行符为 LF
	if rules.enabled[NormRuleEncodingAndEOL] {
		cleaned := text
		cleaned = strings.TrimPrefix(cleaned, "\xEF\xBB\xBF")
		cleaned = trailingCommaBOMRe.ReplaceAllString(cleaned, "")
		if strings.Contains(cleaned, "\r") {
			cleaned = strings.ReplaceAll(cleaned, "\r\n", "\n")
			cleaned = strings.ReplaceAll(cleaned, "\r", "\n")
		}
		if cleaned != text {
			text = cleaned
			diags = append(diags, NormalizeDiagnostic{
				RepoFilePath: rp,
				RuleID:       NormRuleEncodingAndEOL,
				Severity:     "INFO",
				Line:         1,
				Column:       1,
				Message:      "已规范化 UTF-8 BOM 与 LF 换行符",
			})
		}
	}

	hadTrailingLF := strings.HasSuffix(text, "\n")
	rawSplit := strings.Split(text, "\n")
	if hadTrailingLF && len(rawSplit) > 0 && rawSplit[len(rawSplit)-1] == "" {
		rawSplit = rawSplit[:len(rawSplit)-1]
	}

	lines := make([]trackedLine, 0, len(rawSplit)+1)
	for i, l := range rawSplit {
		lines = append(lines, trackedLine{text: l, rawLine: i + 1})
	}

	// NORM-004: 剥离 C/C++ 预处理指令及已知非标宏块（保留语义注释并精确跟踪原行号）
	if rules.enabled[NormRuleDirectiveStrip] {
		filtered := make([]trackedLine, 0, len(lines))
		strippedCount := 0
		inBlockComment := false
		for _, tl := range lines {
			trimmed := strings.TrimSpace(tl.text)
			if inBlockComment {
				if strings.Contains(trimmed, "*/") {
					inBlockComment = false
				}
				filtered = append(filtered, tl)
				continue
			}
			if strings.HasPrefix(trimmed, "/*") {
				if !strings.Contains(trimmed, "*/") {
					inBlockComment = true
				}
				filtered = append(filtered, tl)
				continue
			}
			if !strings.HasPrefix(trimmed, "//") && (preprocessorRegex.MatchString(tl.text) || dophiMacroLineRe.MatchString(tl.text)) {
				strippedCount++
				continue
			}
			filtered = append(filtered, tl)
		}
		if strippedCount > 0 {
			lines = filtered
			diags = append(diags, NormalizeDiagnostic{
				RepoFilePath: rp,
				RuleID:       NormRuleDirectiveStrip,
				Severity:     "INFO",
				Line:         1,
				Column:       1,
				Message:      fmt.Sprintf("已剥离 %d 行非标指令或宏声明", strippedCount),
			})
		}
	}

	// Layer 2: 仓级及全局正则替换 (regex_rewrites)
	if len(rules.regexRewrites) > 0 {
		for _, rr := range rules.regexRewrites {
			re, err := regexp.Compile(rr.Pattern)
			if err != nil {
				continue
			}
			if rr.Multiline {
				before := joinTrackedLines(lines, false)
				after := re.ReplaceAllString(before, rr.Replacement)
				if after != before {
					afterSplit := strings.Split(after, "\n")
					newLines := make([]trackedLine, 0, len(afterSplit))
					for idx, s := range afterSplit {
						rawLine := idx + 1
						if idx < len(lines) {
							rawLine = lines[idx].rawLine
						} else if len(lines) > 0 {
							rawLine = lines[len(lines)-1].rawLine
						}
						newLines = append(newLines, trackedLine{text: s, rawLine: rawLine})
					}
					lines = newLines
					diags = append(diags, NormalizeDiagnostic{
						RepoFilePath: rp,
						RuleID:       "REGEX:" + rr.ID,
						Severity:     "INFO",
						Line:         1,
						Column:       1,
						Message:      fmt.Sprintf("已应用多行正则规则 %s", rr.ID),
					})
				}
			} else {
				changed := false
				newLines := make([]trackedLine, 0, len(lines))
				for _, tl := range lines {
					rep := re.ReplaceAllString(tl.text, rr.Replacement)
					if rep != tl.text {
						changed = true
					}
					// 若行内替换为多行则展开；若替换为空串且原整行匹配移除则跳过整行避免多余空行
					if rep == "" && tl.text != "" && strings.TrimSpace(re.FindString(tl.text)) == strings.TrimSpace(tl.text) {
						continue
					}
					newLines = append(newLines, trackedLine{text: rep, rawLine: tl.rawLine})
				}
				if changed {
					lines = newLines
					diags = append(diags, NormalizeDiagnostic{
						RepoFilePath: rp,
						RuleID:       "REGEX:" + rr.ID,
						Severity:     "INFO",
						Line:         1,
						Column:       1,
						Message:      fmt.Sprintf("已应用正则规则 %s", rr.ID),
					})
				}
			}
		}
	}

	// NORM-003 & NORM-005: 逐行规范 package 标识符与重写 import 路径
	inBlockComment := false
	for i := range lines {
		trimmed := strings.TrimSpace(lines[i].text)
		if inBlockComment {
			if strings.Contains(trimmed, "*/") {
				inBlockComment = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "/*") {
			if !strings.Contains(trimmed, "*/") {
				inBlockComment = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			continue
		}

		if rules.enabled[NormRulePackageNormalize] {
			if m := packageStmtRegex.FindStringSubmatch(lines[i].text); len(m) == 4 {
				origPkg := strings.TrimSpace(m[2])
				normPkg := normalizePackageIdent(origPkg)
				if normPkg != "" && normPkg != origPkg {
					lines[i].text = m[1] + normPkg + m[3]
					diags = append(diags, NormalizeDiagnostic{
						RepoFilePath: rp,
						RuleID:       NormRulePackageNormalize,
						Severity:     "INFO",
						Line:         i + 1,
						Column:       1,
						Message:      fmt.Sprintf("已规范化 package: %s -> %s", origPkg, normPkg),
					})
				}
			}
		}

		if rules.enabled[NormRuleImportRewrite] {
			if m := importStmtRegex.FindStringSubmatch(lines[i].text); len(m) == 4 {
				origImp := m[2]
				rewrittenImp := n.rewriteImportPath(origImp, rules)
				if rewrittenImp != "" && rewrittenImp != origImp {
					lines[i].text = fmt.Sprintf(`%s"%s"%s`, m[1], rewrittenImp, m[3])
					diags = append(diags, NormalizeDiagnostic{
						RepoFilePath: rp,
						RuleID:       NormRuleImportRewrite,
						Severity:     "INFO",
						Line:         i + 1,
						Column:       1,
						Message:      fmt.Sprintf("已重写 import 路径: %s -> %s", origImp, rewrittenImp),
					})
				}
			}
		}
	}

	// NORM-002: 缺失 syntax = "proto2/3"; 头则自动补齐（幂等）
	if rules.enabled[NormRuleSyntaxHeaderFix] && !hasSyntaxDeclaration(lines) {
		insertIdx := findHeaderInsertIndex(lines)
		syntaxTL := trackedLine{
			text:    fmt.Sprintf(`syntax = "%s";`, rules.defaultSyntax),
			rawLine: 1,
		}
		if insertIdx < len(lines) && lines[insertIdx].rawLine > 0 {
			syntaxTL.rawLine = lines[insertIdx].rawLine
		}
		updated := make([]trackedLine, 0, len(lines)+1)
		updated = append(updated, lines[:insertIdx]...)
		updated = append(updated, syntaxTL)
		updated = append(updated, lines[insertIdx:]...)
		lines = updated
		diags = append(diags, NormalizeDiagnostic{
			RepoFilePath: rp,
			RuleID:       NormRuleSyntaxHeaderFix,
			Severity:     "INFO",
			Line:         insertIdx + 1,
			Column:       1,
			Message:      fmt.Sprintf("缺失 syntax 声明，已补齐 syntax = %q", rules.defaultSyntax),
		})
	}

	// 生成最终文本与精确 line_map
	finalText := joinTrackedLines(lines, hadTrailingLF || len(lines) > 0)
	cleanBytes := []byte(finalText)
	sum := sha256.Sum256(cleanBytes)

	linePreserving := len(lines) == len(rawSplit)
	var lineMap map[int]int
	if linePreserving {
		for idx, tl := range lines {
			if tl.rawLine != idx+1 {
				linePreserving = false
				break
			}
		}
	}
	if !linePreserving && len(lines) > 0 {
		lineMap = make(map[int]int, len(lines))
		for idx, tl := range lines {
			lineMap[idx+1] = tl.rawLine
		}
	}

	return NormalizedFileResult{
		RepoFilePath:   rp,
		CleanContent:   cleanBytes,
		CleanSHA256:    hex.EncodeToString(sum[:]),
		LineCount:      countNormalizedLines(finalText),
		LinePreserving: linePreserving,
		LineMap:        lineMap,
		Diagnostics:    diags,
	}
}

func joinTrackedLines(lines []trackedLine, appendTrailingLF bool) string {
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	for i, tl := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(tl.text)
	}
	if appendTrailingLF {
		b.WriteByte('\n')
	}
	return b.String()
}

func normalizePackageIdent(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	s := strings.ReplaceAll(trimmed, "/", ".")
	s = strings.ReplaceAll(s, "-", "_")
	s = invalidPkgCharRe.ReplaceAllString(s, "_")
	s = collapseDotPkgRe.ReplaceAllString(s, ".")
	s = strings.Trim(s, ".")
	return s
}

func (n *RuleBasedNormalizer) rewriteImportPath(orig string, rules effectiveRepoRules) string {
	cur := strings.ReplaceAll(strings.TrimSpace(orig), "\\", "/")
	for strings.HasPrefix(cur, "./") {
		cur = strings.TrimPrefix(cur, "./")
	}
	if rep, ok := rules.importRewrites[cur]; ok && strings.TrimSpace(rep) != "" {
		return rep
	}
	if rep, ok := rules.importRewrites[orig]; ok && strings.TrimSpace(rep) != "" {
		return rep
	}
	if mapped, err := ApplyIntraRepoPathMapping(cur, rules.stripPrefixes, rules.pathRewrites); err == nil && mapped != "" {
		if rep, ok := rules.importRewrites[mapped]; ok && strings.TrimSpace(rep) != "" {
			return rep
		}
		cur = mapped
	}
	if n != nil && len(n.ImportOwners) > 0 {
		if canonical, ok := n.ImportOwners[cur]; ok && canonical != "" {
			return canonical
		}
	}
	return cur
}

func hasSyntaxDeclaration(lines []trackedLine) bool {
	inBlockComment := false
	for _, tl := range lines {
		trimmed := strings.TrimSpace(tl.text)
		if inBlockComment {
			if strings.Contains(trimmed, "*/") {
				inBlockComment = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "/*") {
			if !strings.Contains(trimmed, "*/") {
				inBlockComment = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if syntaxDeclRegex.MatchString(tl.text) {
			return true
		}
	}
	return false
}

func findHeaderInsertIndex(lines []trackedLine) int {
	inBlockComment := false
	for i, tl := range lines {
		trimmed := strings.TrimSpace(tl.text)
		if trimmed == "" {
			continue
		}
		if inBlockComment {
			if strings.Contains(trimmed, "*/") {
				inBlockComment = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "/*") {
			if !strings.Contains(trimmed, "*/") {
				inBlockComment = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		return i
	}
	return 0
}

// DryRunPathMapping 展示单文件在工作区导出后的路径重映射。
type DryRunPathMapping struct {
	RawPath        string   `json:"raw_path"`
	CleanPath      string   `json:"clean_path"`
	RawSyntaxOK    bool     `json:"raw_syntax_ok"`
	CleanSyntaxOK  bool     `json:"clean_syntax_ok"`
	Status         string   `json:"status"` // NORMALIZED | QUARANTINED | EXEMPT
	StatusReason   string   `json:"status_reason,omitempty"`
	AppliedRules   []string `json:"applied_rules,omitempty"`
	LinePreserving bool     `json:"line_preserving"`
	UnifiedDiff    string   `json:"unified_diff,omitempty"`
}

// RepoGovernanceDryRunResult 为单仓零网络、零写盘试算结果。
type RepoGovernanceDryRunResult struct {
	GitRepoID          uint64                   `json:"git_repo_id"`
	RepoName           string                   `json:"repo_name"`
	BeforeTargetDir    string                   `json:"before_target_dir"`
	AfterTargetDir     string                   `json:"after_target_dir"`
	GlobalCollisions   []ExportTargetCollision  `json:"global_collisions,omitempty"`
	PathCollisions     []IntraRepoPathCollision `json:"path_collisions,omitempty"`
	TotalFiles         int                      `json:"total_files"`
	RawSyntaxPassCount int                      `json:"raw_syntax_pass_count"`
	CleanSyntaxPassCnt int                      `json:"clean_syntax_pass_count"`
	QuarantinedCount   int                      `json:"quarantined_count"`
	RawSyntaxPassRate  float64                  `json:"raw_syntax_pass_rate"`
	CleanSyntaxPassRat float64                  `json:"clean_syntax_pass_rate"`
	Files              []DryRunPathMapping      `json:"files"`
}

// GlobalGovernanceDryRunResult 为整份 proto-governance/v1 JSON 的全局预览结果。
type GlobalGovernanceDryRunResult struct {
	SchemaValid      bool                         `json:"schema_valid"`
	ValidationError  string                       `json:"validation_error,omitempty"`
	GlobalCollisions []ExportTargetCollision      `json:"global_collisions,omitempty"`
	AffectedRepos    []RepoGovernanceDryRunResult `json:"affected_repos"`
}

// BuildUnifiedDiff 生成紧凑的源码 Unified Diff 片段（无外部 diff 命令依赖）。
func BuildUnifiedDiff(filePath, before, after string) string {
	if before == after {
		return ""
	}
	bLines := strings.Split(strings.TrimSuffix(before, "\n"), "\n")
	aLines := strings.Split(strings.TrimSuffix(after, "\n"), "\n")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -1,%d +1,%d @@\n", filePath, filePath, len(bLines), len(aLines)))
	maxLines := len(bLines)
	if len(aLines) > maxLines {
		maxLines = len(aLines)
	}
	for i := 0; i < maxLines; i++ {
		var bLine, aLine string
		hasB := i < len(bLines)
		hasA := i < len(aLines)
		if hasB {
			bLine = bLines[i]
		}
		if hasA {
			aLine = aLines[i]
		}
		if hasB && hasA && bLine == aLine {
			sb.WriteString(" " + bLine + "\n")
			continue
		}
		if hasB {
			sb.WriteString("-" + bLine + "\n")
		}
		if hasA {
			sb.WriteString("+" + aLine + "\n")
		}
	}
	return sb.String()
}

// DryRunRepoGovernance 对单仓数据库已有 raw 快照执行免网络、零写盘方案 A 导出与 Stage B 清洗试算。
func DryRunRepoGovernance(
	ctx context.Context,
	db *gorm.DB,
	repo models.GitRepo,
	override *RepoGovernanceSpec,
	globalCfg *GovernanceConfigDoc,
) (*RepoGovernanceDryRunResult, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if globalCfg == nil {
		snap, err := LoadActiveGovernanceConfig(ctx, db)
		if err != nil {
			return nil, err
		}
		globalCfg = &snap.Doc
	}

	bSub, bDir := ResolveEffectiveExportTarget(repo, nil)
	aSub, aDir := ResolveEffectiveExportTarget(repo, override)

	res := &RepoGovernanceDryRunResult{
		GitRepoID:       repo.ID,
		RepoName:        repo.Name,
		BeforeTargetDir: fmt.Sprintf("clean/%s/%s", bSub, bDir),
		AfterTargetDir:  fmt.Sprintf("clean/%s/%s", aSub, aDir),
		Files:           []DryRunPathMapping{},
	}

	overrides := map[uint64]*RepoGovernanceSpec{}
	candidateRepo := repo
	stripPrefixes := repo.StripPrefixes()
	pathRewrites := repo.PathRewrites()
	if override != nil {
		overrides[repo.ID] = override
		if override.StripPrefixes != nil {
			stripPrefixes = override.StripPrefixes
		}
		if override.PathRewrites != nil {
			pathRewrites = override.PathRewrites
		}
		normCfg := ParseRepoNormalizeConfig(repo.RepoNormalizeConfigJSON)
		if override.DisabledRules != nil {
			normCfg.DisabledRules = override.DisabledRules
		}
		if override.RegexRewrites != nil {
			normCfg.RegexRewrites = override.RegexRewrites
		}
		if override.ImportRewrites != nil {
			normCfg.ImportRewrites = override.ImportRewrites
		}
		if override.ExemptFiles != nil {
			normCfg.ExemptFiles = override.ExemptFiles
		}
		if override.QuarantinedFiles != nil {
			normCfg.QuarantinedFiles = override.QuarantinedFiles
		}
		if rawNorm, mErr := json.Marshal(normCfg); mErr == nil {
			candidateRepo.RepoNormalizeConfigJSON = rawNorm
		}
	}
	if cols, _ := CheckGlobalExportTargetCollisions(ctx, db, overrides); len(cols) > 0 {
		res.GlobalCollisions = cols
	}

	var pfiles []models.ProtoFile
	if err := db.WithContext(ctx).Where("git_repo_id = ? AND is_deleted = false", repo.ID).Order("id ASC").Find(&pfiles).Error; err != nil {
		return nil, err
	}
	if len(pfiles) == 0 {
		return res, nil
	}

	verIDs := make([]uint64, 0, len(pfiles))
	for _, pf := range pfiles {
		if pf.LatestVersionID != nil && *pf.LatestVersionID > 0 {
			verIDs = append(verIDs, *pf.LatestVersionID)
		}
	}
	verByFile := make(map[uint64]models.ProtoFileVersion, len(verIDs))
	if len(verIDs) > 0 {
		var vers []models.ProtoFileVersion
		if err := db.WithContext(ctx).Where("id IN ?", verIDs).Find(&vers).Error; err == nil {
			for _, v := range vers {
				verByFile[v.ProtoFileID] = v
			}
		}
	}

	rawPaths := make([]string, 0, len(pfiles))
	rawContents := make(map[string][]byte, len(pfiles))
	for _, pf := range pfiles {
		rp := pf.RepoFilePath
		if rp == "" {
			rp = pf.FilePath
		}
		v := verByFile[pf.ID]
		rawText := v.RawContentText
		if rawText == "" {
			rawText = v.ContentText
		}
		rawPaths = append(rawPaths, rp)
		rawContents[rp] = []byte(rawText)
	}

	mappedPaths, pathCols, _ := MapAndValidateIntraRepoPaths(rawPaths, stripPrefixes, pathRewrites)
	if len(pathCols) > 0 {
		res.PathCollisions = pathCols
	}

	norm := NewRuleBasedNormalizer(globalCfg)
	auxJSON := BuildEffectiveRepoAuxJSON(candidateRepo, globalCfg)
	normOut, err := norm.Normalize(ctx, NormalizeInput{
		Repo:          candidateRepo,
		Files:         rawContents,
		AuxConfigJSON: auxJSON,
	})
	if err != nil {
		return nil, err
	}

	sort.Strings(rawPaths)
	res.TotalFiles = len(rawPaths)
	for _, rp := range rawPaths {
		rawBytes := rawContents[rp]
		rawGate := ParseProtoSyntaxOnly(rp, rawBytes)
		if rawGate.Passed {
			res.RawSyntaxPassCount++
		}

		cleanPath := mappedPaths[rp]
		if cleanPath == "" {
			cleanPath = rp
		}
		nf := normOut.Files[rp]
		cleanGate := ParseProtoSyntaxOnly(cleanPath, nf.CleanContent)

		status := models.ProtoPipelineNormalized
		reason := ""
		hasErrorDiag := false
		applied := make([]string, 0, len(nf.Diagnostics))
		for _, d := range nf.Diagnostics {
			if d.RuleID != "" {
				applied = append(applied, d.RuleID)
			}
			if d.Severity == "ERROR" {
				hasErrorDiag = true
				reason = d.RuleID + ": " + d.Message
			}
		}
		if hasErrorDiag {
			status = models.ProtoPipelineQuarantined
		} else if !cleanGate.Passed {
			status = models.ProtoPipelineQuarantined
			reason = "PARSE_FAILED: " + cleanGate.ErrorSummary
		} else {
			res.CleanSyntaxPassCnt++
		}
		if status == models.ProtoPipelineQuarantined {
			res.QuarantinedCount++
		}

		res.Files = append(res.Files, DryRunPathMapping{
			RawPath:        rp,
			CleanPath:      cleanPath,
			RawSyntaxOK:    rawGate.Passed,
			CleanSyntaxOK:  !hasErrorDiag && cleanGate.Passed,
			Status:         status,
			StatusReason:   reason,
			AppliedRules:   applied,
			LinePreserving: nf.LinePreserving,
			UnifiedDiff:    BuildUnifiedDiff(cleanPath, string(rawBytes), string(nf.CleanContent)),
		})
	}
	if res.TotalFiles > 0 {
		res.RawSyntaxPassRate = float64(res.RawSyntaxPassCount) / float64(res.TotalFiles) * 100
		res.CleanSyntaxPassRat = float64(res.CleanSyntaxPassCnt) / float64(res.TotalFiles) * 100
	}
	return res, nil
}
