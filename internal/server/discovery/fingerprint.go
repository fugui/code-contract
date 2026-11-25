package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"code-contract/models"
)

// CurrentFingerprintVersion 当前进程规范化身份指纹算法版本 (ADR-24 §5.5)
const CurrentFingerprintVersion int16 = 1

var (
	procPIDSuffixRe = regexp.MustCompile(`^(.+)-(\d+)$`)
	ipv4TokenRe     = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b`)
	hexLongTokenRe  = regexp.MustCompile(`^[0-9a-fA-F]{8,}$`)
	pureDigitsRe    = regexp.MustCompile(`^\d+$`)
	nonCodeCharsRe  = regexp.MustCompile(`[^a-z0-9_-]+`)
)

// StripProcPIDSuffix 剥离运行期进程名尾部的 -<pid> 后缀
func StripProcPIDSuffix(name string) string {
	trimmed := strings.TrimSpace(name)
	if m := procPIDSuffixRe.FindStringSubmatch(trimmed); len(m) == 3 {
		return m[1]
	}
	return trimmed
}

// ComputeIdentityFingerprint 按 ADR-24 §5.5 计算规范化版本指纹（严禁包含任何发布服务名）
func ComputeIdentityFingerprint(exeName, cmdline, procName string) (fingerprint string, canonicalKey string, isLowConfidence bool) {
	redacted := RedactCmdline(cmdline)
	fields := splitCmdTokens(redacted)

	exeBase := strings.TrimSpace(exeName)
	if exeBase == "" && len(fields) > 0 {
		exeBase = fields[0]
	}
	if exeBase != "" {
		exeBase = filepath.Base(exeBase)
	}

	if len(fields) == 0 {
		baseName := strings.ToLower(StripProcPIDSuffix(firstNonEmpty(exeBase, procName, "unknown")))
		sum := sha256.Sum256([]byte("v1|proc|" + baseName))
		return hex.EncodeToString(sum[:]), "proc:" + baseName, true
	}

	entryToken := ""
	skipIdx := map[int]bool{0: true}
	lowerExe := strings.ToLower(exeBase)
	switch {
	case strings.HasPrefix(lowerExe, "java"):
		for i := 1; i < len(fields); i++ {
			tok := fields[i]
			if tok == "-jar" && i+1 < len(fields) {
				entryToken = filepath.Base(fields[i+1])
				skipIdx[i] = true
				skipIdx[i+1] = true
				break
			}
			if !strings.HasPrefix(tok, "-") && !strings.Contains(tok, "=") {
				entryToken = filepath.Base(tok)
				skipIdx[i] = true
				break
			}
		}
	case strings.HasPrefix(lowerExe, "python") || lowerExe == "node" || lowerExe == "sh" || lowerExe == "bash":
		for i := 1; i < len(fields); i++ {
			tok := fields[i]
			if !strings.HasPrefix(tok, "-") && !strings.Contains(tok, "=") {
				entryToken = filepath.Base(tok)
				skipIdx[i] = true
				break
			}
		}
	}

	stableArgs := make([]string, 0, len(fields))
	for i := 1; i < len(fields); i++ {
		if skipIdx[i] {
			continue
		}
		tok := strings.TrimSpace(fields[i])
		if tok == "" {
			continue
		}
		// 跳过空格分隔的敏感参数键值对（如 --password *** / --token mytok）
		if isSensitiveArg(tok) {
			if !strings.Contains(tok, "=") && i+1 < len(fields) && !strings.HasPrefix(fields[i+1], "-") {
				i++
			}
			continue
		}
		// 跳过空格分隔的易变参数键值对（如 --port 8080 / -p 9090 / --listen :8080）
		if isVolatileFlagName(tok) {
			if i+1 < len(fields) && !strings.HasPrefix(fields[i+1], "-") {
				i++
			}
			continue
		}
		if isVolatileArg(tok) {
			continue
		}
		stableArgs = append(stableArgs, tok)
	}
	sort.Strings(stableArgs)
	stableArgs = dedupSortedStrings(stableArgs)

	canonical := "v1|" + strings.ToLower(exeBase) + "|" + strings.ToLower(entryToken) + "|" + strings.Join(stableArgs, " ")
	sum := sha256.Sum256([]byte(canonical))
	fp := hex.EncodeToString(sum[:])
	return fp, "fp:" + fp[:24], false
}

func isVolatileFlagName(tok string) bool {
	if !strings.HasPrefix(tok, "-") || strings.Contains(tok, "=") {
		return false
	}
	k := strings.ToLower(strings.TrimLeft(tok, "-"))
	switch k {
	case "p", "l", "port", "listen", "pid", "host", "ip", "addr", "bind", "endpoint", "instance", "node_id", "node-id", "worker_id", "worker-id":
		return true
	}
	for _, suffix := range []string{"_port", "-port", "_listen", "-listen", "_pid", "-pid", "_addr", "-addr", "_bind", "-bind", "_host", "-host", "_ip", "-ip"} {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

func isVolatileArg(tok string) bool {
	lower := strings.ToLower(tok)
	if pureDigitsRe.MatchString(tok) || hexLongTokenRe.MatchString(tok) {
		return true
	}
	if ipv4TokenRe.MatchString(tok) || strings.Contains(tok, "::") {
		return true
	}
	if strings.HasPrefix(lower, "/tmp/") || strings.HasPrefix(lower, "/var/run/") {
		return true
	}
	if idx := strings.IndexByte(lower, '='); idx >= 0 {
		k := strings.TrimLeft(lower[:idx], "-")
		v := lower[idx+1:]
		for _, volatileKey := range []string{"port", "listen", "pid", "host", "ip", "addr", "bind", "endpoint", "instance", "node_id", "worker_id"} {
			if k == volatileKey || strings.HasSuffix(k, "_"+volatileKey) || strings.HasSuffix(k, "-"+volatileKey) {
				return true
			}
		}
		if pureDigitsRe.MatchString(v) || hexLongTokenRe.MatchString(v) || ipv4TokenRe.MatchString(v) || strings.HasPrefix(v, "/tmp/") || strings.HasPrefix(v, "/var/run/") {
			return true
		}
	}
	return false
}

func dedupSortedStrings(sorted []string) []string {
	if len(sorted) <= 1 {
		return sorted
	}
	out := sorted[:1]
	for i := 1; i < len(sorted); i++ {
		if sorted[i] != sorted[i-1] {
			out = append(out, sorted[i])
		}
	}
	return out
}

// MatchCmdPattern 判断 cmdline / exeName 是否匹配 P1 模式（支持 re: RE2 正则或子串匹配）
func MatchCmdPattern(pattern, cmdline, exeName string) bool {
	pat := strings.TrimSpace(pattern)
	if pat == "" {
		return false
	}
	if strings.HasPrefix(pat, "re:") {
		expr := strings.TrimSpace(strings.TrimPrefix(pat, "re:"))
		if expr == "" {
			return false
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return false
		}
		return (cmdline != "" && re.MatchString(cmdline)) || (exeName != "" && re.MatchString(exeName))
	}
	lowerPat := strings.ToLower(pat)
	return (cmdline != "" && strings.Contains(strings.ToLower(cmdline), lowerPat)) ||
		(exeName != "" && strings.Contains(strings.ToLower(exeName), lowerPat))
}

// ProcessCandidateDTO 身份歧义候选标准架构进程摘要
type ProcessCandidateDTO struct {
	ArchProcessID uint64 `json:"arch_process_id"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Lifecycle     string `json:"lifecycle"`
	MatchTier     string `json:"match_tier"`
}

// IdentityResolution 进程三阶身份解析结果
type IdentityResolution struct {
	MatchStatus     string // MATCHED | NEW | AMBIGUOUS | LOW_CONFIDENCE
	MatchTier       string // P1_PATTERN | P2_FINGERPRINT | P3_PROC_NAME | OVERRIDE | ""
	Matched         *models.ArchProcess
	Candidates      []ProcessCandidateDTO
	Fingerprint     string
	CanonicalKey    string
	IsLowConfidence bool
}

// ResolveProcessIdentity 按 ADR-24 §5.5 三阶优先级解析物理进程归属的标准架构进程蓝图
func ResolveProcessIdentity(
	visibleArchProcs []models.ArchProcess,
	mountedByArchID map[uint64]models.Process,
	exeName, cmdline, procName string,
	forceArchProcessID *uint64,
	confirmLowConfidence bool,
) IdentityResolution {
	fp, canonKey, isLow := ComputeIdentityFingerprint(exeName, cmdline, procName)
	res := IdentityResolution{
		MatchStatus:     "NEW",
		Fingerprint:     fp,
		CanonicalKey:    canonKey,
		IsLowConfidence: isLow,
	}

	if forceArchProcessID != nil && *forceArchProcessID > 0 {
		for i := range visibleArchProcs {
			if visibleArchProcs[i].ID == *forceArchProcessID {
				ap := visibleArchProcs[i]
				res.MatchStatus = "MATCHED"
				res.MatchTier = "OVERRIDE"
				res.Matched = &ap
				return res
			}
		}
	}

	// P1: 人工/蓝图模式命中 (Process.OverrideCmdPattern 优先，其次 ArchProcess.DefaultCmdPattern)
	var p1Matches []models.ArchProcess
	for _, ap := range visibleArchProcs {
		pat := ap.DefaultCmdPattern
		if mountedByArchID != nil {
			if m, ok := mountedByArchID[ap.ID]; ok && strings.TrimSpace(m.OverrideCmdPattern) != "" {
				pat = m.OverrideCmdPattern
			}
		}
		if MatchCmdPattern(pat, cmdline, exeName) {
			p1Matches = append(p1Matches, ap)
		}
	}
	if len(p1Matches) == 1 {
		ap := p1Matches[0]
		res.MatchStatus = "MATCHED"
		res.MatchTier = "P1_PATTERN"
		res.Matched = &ap
		return res
	}
	if len(p1Matches) > 1 {
		res.MatchStatus = "AMBIGUOUS"
		res.MatchTier = "P1_PATTERN"
		res.Candidates = toCandidateDTOs(p1Matches, "P1_PATTERN")
		return res
	}

	// P2: 同版本规范化指纹命中
	if !isLow {
		var p2Matches []models.ArchProcess
		for _, ap := range visibleArchProcs {
			if ap.FingerprintVersion == CurrentFingerprintVersion && ap.IdentityFingerprint != "" && ap.IdentityFingerprint == fp {
				p2Matches = append(p2Matches, ap)
			}
		}
		if len(p2Matches) == 1 {
			ap := p2Matches[0]
			res.MatchStatus = "MATCHED"
			res.MatchTier = "P2_FINGERPRINT"
			res.Matched = &ap
			return res
		}
		if len(p2Matches) > 1 {
			res.MatchStatus = "AMBIGUOUS"
			res.MatchTier = "P2_FINGERPRINT"
			res.Candidates = toCandidateDTOs(p2Matches, "P2_FINGERPRINT")
			return res
		}
		return res
	}

	// P3: cmdline 不可读时 procName 兜底（低置信，须显式确认）
	normProc := strings.ToLower(StripProcPIDSuffix(firstNonEmpty(procName, exeName)))
	var p3Matches []models.ArchProcess
	if normProc != "" {
		for _, ap := range visibleArchProcs {
			lowerCode := strings.ToLower(strings.TrimSpace(ap.Code))
			lowerName := strings.ToLower(strings.TrimSpace(ap.Name))
			lowerPat := strings.ToLower(strings.TrimSpace(ap.DefaultCmdPattern))
			if lowerCode == normProc || lowerName == normProc || (lowerPat != "" && lowerPat == normProc) ||
				(strings.HasPrefix(lowerCode, "prv") && strings.HasSuffix(lowerCode, "-"+NormalizeArchProcessCode(normProc))) {
				p3Matches = append(p3Matches, ap)
			}
		}
	}
	if len(p3Matches) > 1 {
		res.MatchStatus = "AMBIGUOUS"
		res.MatchTier = "P3_PROC_NAME"
		res.Candidates = toCandidateDTOs(p3Matches, "P3_PROC_NAME")
		return res
	}
	if !confirmLowConfidence {
		res.MatchStatus = "LOW_CONFIDENCE"
		res.MatchTier = "P3_PROC_NAME"
		if len(p3Matches) == 1 {
			res.Candidates = toCandidateDTOs(p3Matches, "P3_PROC_NAME")
		}
		return res
	}
	if len(p3Matches) == 1 {
		ap := p3Matches[0]
		res.MatchStatus = "MATCHED"
		res.MatchTier = "P3_PROC_NAME"
		res.Matched = &ap
		return res
	}
	res.MatchStatus = "NEW"
	res.MatchTier = "P3_PROC_NAME"
	return res
}

func toCandidateDTOs(aps []models.ArchProcess, tier string) []ProcessCandidateDTO {
	out := make([]ProcessCandidateDTO, 0, len(aps))
	for _, ap := range aps {
		out = append(out, ProcessCandidateDTO{
			ArchProcessID: ap.ID,
			Code:          ap.Code,
			Name:          ap.Name,
			Lifecycle:     ap.Lifecycle,
			MatchTier:     tier,
		})
	}
	return out
}

// IsArchProcessVisibleToUser 按 ADR-24 §5.4 规则 V2 校验 PROVISIONAL 标准进程可见性
func IsArchProcessVisibleToUser(ap models.ArchProcess, agentByID map[uint64]*models.Agent, userID uint, isAdmin bool) bool {
	if ap.Lifecycle != models.ArchProcLifecycleProvisional {
		return true
	}
	if isAdmin {
		return true
	}
	if ap.OriginAgentID == nil || *ap.OriginAgentID == 0 {
		return false
	}
	ag, ok := agentByID[*ap.OriginAgentID]
	if !ok || ag == nil {
		return false
	}
	if ag.Scope == models.ScopeShared {
		return true
	}
	return ag.OwnerID != nil && *ag.OwnerID == userID
}

// NormalizeArchProcessCode 将原始进程名规范化为标准 ArchProcess.Code 片段
func NormalizeArchProcessCode(raw string) string {
	base := strings.ToLower(strings.TrimSpace(StripProcPIDSuffix(filepath.Base(raw))))
	base = nonCodeCharsRe.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-_")
	if base == "" {
		base = "proc"
	}
	if len(base) > 48 {
		base = strings.TrimRight(base[:48], "-_")
	}
	return base
}
