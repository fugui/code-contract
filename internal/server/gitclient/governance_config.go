package gitclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	// GovernanceSchemaVersion 为 ADR-27 辅助 .json 治理配置的 Schema 标识。
	GovernanceSchemaVersion = "proto-governance/v1"

	NormRuleEncodingAndEOL   = "NORM-001"
	NormRuleSyntaxHeaderFix  = "NORM-002"
	NormRulePackageNormalize = "NORM-003"
	NormRuleDirectiveStrip   = "NORM-004"
	NormRuleImportRewrite    = "NORM-005"
	NormRuleUServiceConvert  = "NORM-006"
	NormRulePGSegmentFlatten = "NORM-007"
)

var (
	// ErrExportTargetConflict 表示多个有效仓库映射到相同的 (effective_subsystem, effective_repo_dir) 导出目录。
	ErrExportTargetConflict = errors.New("目标导出目录存在全局冲突")
	// ErrIntraRepoPathConflict 表示单仓内多份源文件经裁剪重写后碰撞为同一逻辑/导出路径。
	ErrIntraRepoPathConflict = errors.New("仓内路径裁剪或重写后存在目标路径碰撞")
	// ErrGovernanceValidation 表示治理配置或正则表达式校验不通过。
	ErrGovernanceValidation = errors.New("契约治理配置校验失败")
)

var validBuiltinRuleIDs = map[string]struct{}{
	NormRuleEncodingAndEOL:   {},
	NormRuleSyntaxHeaderFix:  {},
	NormRulePackageNormalize: {},
	NormRuleDirectiveStrip:   {},
	NormRuleImportRewrite:    {},
	NormRuleUServiceConvert:  {},
	NormRulePGSegmentFlatten: {},
}

// BuiltinRuleMeta 描述内置 Stage B 清理规则元数据。
type BuiltinRuleMeta struct {
	ID             string `json:"id"`
	Code           string `json:"code"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	DefaultEnabled bool   `json:"default_enabled"`
}

// BuiltinGovernanceRules 返回所有预置内置规则元数据。
func BuiltinGovernanceRules() []BuiltinRuleMeta {
	return []BuiltinRuleMeta{
		{
			ID:             NormRuleEncodingAndEOL,
			Code:           "encoding_and_eol",
			Name:           "编码与换行符统一",
			Description:    "剥离 UTF-8 BOM 头并将 CRLF/CR 统一规范为 LF",
			DefaultEnabled: true,
		},
		{
			ID:             NormRuleDirectiveStrip,
			Code:           "nonstandard_directive_strip",
			Name:           "非标指令剥离",
			Description:    "清理 C/C++ 预处理指令（如 #include/#ifdef/#pragma）及自定义历史宏指令并保留注释",
			DefaultEnabled: true,
		},
		{
			ID:             NormRuleUServiceConvert,
			Code:           "uservice_to_proto_service",
			Name:           "uService DSL 转换",
			Description:    "注释感知状态机转换 uService/interface 为标准 proto service/rpc，提升块内 import 并注入隐式依赖",
			DefaultEnabled: true,
		},
		{
			ID:             NormRulePackageNormalize,
			Code:           "package_normalize",
			Name:           "Package 标识符规范化",
			Description:    "修正 package 声明中的连字符、斜杠或非法空白字符，并对重复 package 声明去重",
			DefaultEnabled: true,
		},
		{
			ID:             NormRuleImportRewrite,
			Code:           "import_path_rewrite",
			Name:           "Import 路径重写",
			Description:    "兼容单引号与缺失分号的 import 声明，规范化反斜杠与前导 ./ 并应用重写映射",
			DefaultEnabled: true,
		},
		{
			ID:             NormRulePGSegmentFlatten,
			Code:           "pg_segment_flatten",
			Name:           "clean 去 /pg 目录扁平化",
			Description:    "在统一坐标系中剥离独立 /pg 目录段（保护 extpg/intpg/pg.proto），使 clean/ 与 workspace/ 对齐",
			DefaultEnabled: true,
		},
		{
			ID:             NormRuleSyntaxHeaderFix,
			Code:           "syntax_header_fix",
			Name:           "Syntax 声明补齐",
			Description:    "为缺失 syntax = \"proto2\"/\"proto3\" 的存量文件按 auto/proto2/proto3 补齐语法头并维护 line_map",
			DefaultEnabled: true,
		},
	}
}

// SharedBaseSourceSpec 描述 R4 共享基座采集通道 A 的目录或仓库源声明。
type SharedBaseSourceSpec struct {
	PathHint string `json:"path_hint"`
	Priority int    `json:"priority,omitempty"`
}

// PoolReconcileConfig 控制 Stage C+ 全池协调与 R7/R8 编译自愈开关及轮数上限。
type PoolReconcileConfig struct {
	ClosureOnly        *bool `json:"closure_only,omitempty"`
	EnableR7AutoFix    *bool `json:"enable_r7_auto_fix,omitempty"`
	MaxR7Rounds        int   `json:"max_r7_rounds,omitempty"`
	EnableR8Quarantine *bool `json:"enable_r8_quarantine,omitempty"`
	MaxR8Rounds        int   `json:"max_r8_rounds,omitempty"`
}

// EffectiveClosureOnly 返回是否仅导出与编译服务依赖闭包（默认 true）。
func (p PoolReconcileConfig) EffectiveClosureOnly() bool {
	if p.ClosureOnly == nil {
		return true
	}
	return *p.ClosureOnly
}

// EffectiveEnableR7 返回是否启用 R7 protocompile 反馈自愈（默认 true）。
func (p PoolReconcileConfig) EffectiveEnableR7() bool {
	if p.EnableR7AutoFix == nil {
		return true
	}
	return *p.EnableR7AutoFix
}

// EffectiveMaxR7Rounds 返回 R7 最大迭代轮数（默认 12）。
func (p PoolReconcileConfig) EffectiveMaxR7Rounds() int {
	if p.MaxR7Rounds <= 0 {
		return 12
	}
	return p.MaxR7Rounds
}

// EffectiveEnableR8 返回是否启用 R8 根因优先固损隔离（默认 true）。
func (p PoolReconcileConfig) EffectiveEnableR8() bool {
	if p.EnableR8Quarantine == nil {
		return true
	}
	return *p.EnableR8Quarantine
}

// EffectiveMaxR8Rounds 返回 R8 最大隔离轮数（默认 150）。
func (p PoolReconcileConfig) EffectiveMaxR8Rounds() int {
	if p.MaxR8Rounds <= 0 {
		return 150
	}
	return p.MaxR8Rounds
}

// PoolPathCandidate 描述全池去 /pg 碰撞检测的单条路径记录。
type PoolPathCandidate struct {
	RepoID        uint64 `json:"repo_id"`
	RepoName      string `json:"repo_name"`
	PreStripPath  string `json:"pre_strip_path"`
	PostStripPath string `json:"post_strip_path"`
}

// PoolStripCollision 描述去 /pg 剥离动作本身新增的跨仓路径碰撞。
type PoolStripCollision struct {
	PostStripPath string   `json:"post_strip_path"`
	PreStripPaths []string `json:"pre_strip_paths"`
	RepoIDs       []uint64 `json:"repo_ids"`
	RepoNames     []string `json:"repo_names"`
}

// RegexRewriteRule 描述单条正则表达式清洗规则。
type RegexRewriteRule struct {
	ID          string `json:"id"`
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
	Multiline   bool   `json:"multiline,omitempty"`
}

// GlobalGovernanceSection 为 proto-governance/v1 的全局规则段。
type GlobalGovernanceSection struct {
	CollectGlobs          []string               `json:"collect_globs,omitempty"`
	ExcludeSubsystems     []string               `json:"exclude_subsystems,omitempty"`
	ExcludeRepoPatterns   []string               `json:"exclude_repo_patterns,omitempty"`
	ExcludeGlobs          []string               `json:"exclude_globs,omitempty"`
	EnabledRules          []string               `json:"enabled_rules,omitempty"`
	DefaultSyntax         string                 `json:"default_syntax,omitempty"` // auto | proto2 | proto3
	StripPGSegment        *bool                  `json:"strip_pg_segment,omitempty"`
	ImplicitSymbolImports map[string]string      `json:"implicit_symbol_imports,omitempty"`
	SharedBaseSources     []SharedBaseSourceSpec `json:"shared_base_sources,omitempty"`
	PoolReconcile         *PoolReconcileConfig   `json:"pool_reconcile,omitempty"`
	ImportRewrites        map[string]string      `json:"import_rewrites,omitempty"`
	RegexRewrites         []RegexRewriteRule     `json:"regex_rewrites,omitempty"`
}

// EffectiveStripPGSegment 返回全池逻辑坐标系及 clean/ 默认是否去除独立 /pg 目录段（默认 true）。
func (g GlobalGovernanceSection) EffectiveStripPGSegment() bool {
	if g.StripPGSegment == nil {
		return true
	}
	return *g.StripPGSegment
}

// EffectivePoolReconcile 返回带默认值的全池协调配置。
func (g GlobalGovernanceSection) EffectivePoolReconcile() PoolReconcileConfig {
	if g.PoolReconcile == nil {
		return PoolReconcileConfig{}
	}
	return *g.PoolReconcile
}

// RepoNormalizeConfig 描述单仓级别的 Stage B 清洗规则与文件豁免/隔离清单（持久化在 contract_git_repos.repo_normalize_config_json）。
type RepoNormalizeConfig struct {
	DisableStripPGSegment bool               `json:"disable_strip_pg_segment,omitempty"`
	DisabledRules         []string           `json:"disabled_rules,omitempty"`
	RegexRewrites         []RegexRewriteRule `json:"regex_rewrites,omitempty"`
	ImportRewrites        map[string]string  `json:"import_rewrites,omitempty"`
	ExemptFiles           []string           `json:"exempt_files,omitempty"`
	QuarantinedFiles      []string           `json:"quarantined_files,omitempty"`
}

// RepoGovernanceSpec 描述单个仓库在辅助 .json 中的完整导出目录与清洗配置。
type RepoGovernanceSpec struct {
	PlatformRepoID        uint64             `json:"platform_repo_id,omitempty"`
	SyncEnabled           *bool              `json:"sync_enabled,omitempty"`
	SyncStrategy          string             `json:"sync_strategy,omitempty"`
	ExportTargetSubsystem string             `json:"export_target_subsystem,omitempty"`
	ExportTargetRepoDir   string             `json:"export_target_repo_dir,omitempty"`
	DisableStripPGSegment bool               `json:"disable_strip_pg_segment,omitempty"`
	StripPrefixes         []string           `json:"strip_prefixes,omitempty"`
	PathRewrites          map[string]string  `json:"path_rewrites,omitempty"`
	CollectGlobs          []string           `json:"collect_globs,omitempty"`
	ExcludeGlobs          []string           `json:"exclude_globs,omitempty"`
	ExtraExcludeGlobs     []string           `json:"extra_exclude_globs,omitempty"`
	DisabledRules         []string           `json:"disabled_rules,omitempty"`
	RegexRewrites         []RegexRewriteRule `json:"regex_rewrites,omitempty"`
	ImportRewrites        map[string]string  `json:"import_rewrites,omitempty"`
	ExemptFiles           []string           `json:"exempt_files,omitempty"`
	QuarantinedFiles      []string           `json:"quarantined_files,omitempty"`
}

// GovernanceConfigDoc 为 proto-governance/v1 完整双向互通配置文档。
type GovernanceConfigDoc struct {
	SchemaVersion string                        `json:"schema_version"`
	Revision      int                           `json:"revision"`
	Global        GlobalGovernanceSection       `json:"global"`
	Repos         map[string]RepoGovernanceSpec `json:"repos,omitempty"`
}

// GovernanceConfigSnapshot 为带持久化摘要的生效配置视图。
type GovernanceConfigSnapshot struct {
	Doc          GovernanceConfigDoc `json:"doc"`
	ConfigSHA256 string              `json:"config_sha256"`
	UpdatedBy    string              `json:"updated_by,omitempty"`
}

// ExportTargetCollision 描述两个仓库在 clean/ 目录树下的导出目录碰撞。
type ExportTargetCollision struct {
	TargetSubsystem string   `json:"target_subsystem"`
	TargetRepoDir   string   `json:"target_repo_dir"`
	RepoIDs         []uint64 `json:"repo_ids"`
	RepoNames       []string `json:"repo_names"`
}

// IntraRepoPathCollision 描述单仓内路径裁剪重写引发的路径碰撞。
type IntraRepoPathCollision struct {
	MappedPath  string   `json:"mapped_path"`
	SourcePaths []string `json:"source_paths"`
}

// DefaultGovernanceConfig 返回内置默认 proto-governance/v1 配置文档（ADR-28 默认开启 uService 转换与去 /pg 扁平化）。
func DefaultGovernanceConfig() GovernanceConfigDoc {
	stripPG := true
	closureOnly := true
	enableR7 := true
	enableR8 := true
	return GovernanceConfigDoc{
		SchemaVersion: GovernanceSchemaVersion,
		Revision:      0,
		Global: GlobalGovernanceSection{
			CollectGlobs: config.DefaultCollectGlobs(),
			ExcludeGlobs: config.DefaultExcludeGlobs(),
			EnabledRules: []string{
				NormRuleEncodingAndEOL,
				NormRuleDirectiveStrip,
				NormRuleUServiceConvert,
				NormRulePackageNormalize,
				NormRuleImportRewrite,
				NormRulePGSegmentFlatten,
				NormRuleSyntaxHeaderFix,
			},
			DefaultSyntax:  "auto",
			StripPGSegment: &stripPG,
			ImplicitSymbolImports: map[string]string{
				"google.protobuf.Empty": "google/protobuf/empty.proto",
				"VoidType":              "sw/pg/BasicTypeMsg.proto",
			},
			SharedBaseSources: []SharedBaseSourceSpec{},
			PoolReconcile: &PoolReconcileConfig{
				ClosureOnly:        &closureOnly,
				EnableR7AutoFix:    &enableR7,
				MaxR7Rounds:        12,
				EnableR8Quarantine: &enableR8,
				MaxR8Rounds:        150,
			},
			ImportRewrites: map[string]string{},
			RegexRewrites:  []RegexRewriteRule{},
		},
		Repos: map[string]RepoGovernanceSpec{},
	}
}

// ProductionUServicePresetConfig 返回现网 uService + R0 分层排除 + 共享基座 + 去 /pg 扁平化推荐治理预设 (ADR-28 §3.1)。
func ProductionUServicePresetConfig() GovernanceConfigDoc {
	doc := DefaultGovernanceConfig()
	doc.Global.DefaultSyntax = "proto2"
	doc.Global.ExcludeSubsystems = []string{"3M"}
	doc.Global.ExcludeRepoPatterns = []string{}
	doc.Global.ExcludeGlobs = []string{
		"**/test/**", "**/testdata/**", "**/mock/**", "**/examples/**", "**/third_party/**", "**/vendor/**",
		"**/alpha/**", "**/beta/**", "**/m1/**", "**/m2/**", "**/m3/**",
		"**/m1_alpha/**", "**/m1_beta1/**", "**/fconst/**",
	}
	doc.Global.SharedBaseSources = []SharedBaseSourceSpec{
		{PathHint: "devbench-hpmc-simulator/include", Priority: 1},
		{PathHint: "ltcommon/pg", Priority: 2},
	}
	return doc
}

// ParseRepoNormalizeConfig 安全反序列化单仓 repo_normalize_config_json。
func ParseRepoNormalizeConfig(raw datatypes.JSON) RepoNormalizeConfig {
	var out RepoNormalizeConfig
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if out.ImportRewrites == nil {
		out.ImportRewrites = map[string]string{}
	}
	return out
}

func validateRelRewriteToken(token string, allowEmpty bool) error {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("%w: 路径规则不能为空", ErrGovernanceValidation)
	}
	if strings.ContainsRune(trimmed, 0) || strings.Contains(trimmed, "\\") {
		return fmt.Errorf("%w: 路径规则 %q 包含非法反斜杠或空字节", ErrGovernanceValidation, token)
	}
	if strings.HasPrefix(trimmed, "/") {
		return fmt.Errorf("%w: 禁止使用绝对路径前缀 %q", ErrGovernanceValidation, token)
	}
	for _, seg := range strings.Split(trimmed, "/") {
		if seg == ".." {
			return fmt.Errorf("%w: 禁止路径穿越片段 %q", ErrGovernanceValidation, token)
		}
	}
	return nil
}

func validateExportSegment(seg string, label string) error {
	trimmed := strings.TrimSpace(seg)
	if trimmed == "" {
		return nil
	}
	if strings.ContainsRune(trimmed, 0) || strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\") {
		return fmt.Errorf("%w: %s %q 不能包含路径分隔符", ErrGovernanceValidation, label, seg)
	}
	if trimmed == "." || trimmed == ".." || trimmed == ".versions" || trimmed == ".tmp" {
		return fmt.Errorf("%w: %s %q 为保留目录名", ErrGovernanceValidation, label, seg)
	}
	return nil
}

func validateRegexRules(rules []RegexRewriteRule) error {
	for i, r := range rules {
		if strings.TrimSpace(r.Pattern) == "" {
			return fmt.Errorf("%w: regex_rewrites[%d] pattern 不能为空", ErrGovernanceValidation, i)
		}
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return fmt.Errorf("%w: regex_rewrites[%d] (%s) 正则表达式非法: %v", ErrGovernanceValidation, i, r.ID, err)
		}
	}
	return nil
}

// ValidateGovernanceConfig 严格校验 proto-governance/v1 文档 Schema、路径安全与正则合法性。
func ValidateGovernanceConfig(doc *GovernanceConfigDoc) error {
	if doc == nil {
		return fmt.Errorf("%w: 配置文档不能为空", ErrGovernanceValidation)
	}
	if strings.TrimSpace(doc.SchemaVersion) != GovernanceSchemaVersion {
		return fmt.Errorf("%w: schema_version 必须为 %s", ErrGovernanceValidation, GovernanceSchemaVersion)
	}
	syn := strings.TrimSpace(doc.Global.DefaultSyntax)
	if syn == "" {
		doc.Global.DefaultSyntax = "auto"
	} else if syn != "auto" && syn != "proto2" && syn != "proto3" {
		return fmt.Errorf("%w: global.default_syntax 仅支持 auto、proto2 或 proto3", ErrGovernanceValidation)
	}

	for sym, imp := range doc.Global.ImplicitSymbolImports {
		if strings.TrimSpace(sym) == "" {
			return fmt.Errorf("%w: implicit_symbol_imports symbol key 不能为空", ErrGovernanceValidation)
		}
		if err := validateRelRewriteToken(imp, false); err != nil {
			return err
		}
	}
	for _, sb := range doc.Global.SharedBaseSources {
		if err := validateRelRewriteToken(sb.PathHint, false); err != nil {
			return err
		}
	}
	for _, pat := range doc.Global.ExcludeRepoPatterns {
		if strings.TrimSpace(pat) == "" {
			return fmt.Errorf("%w: exclude_repo_patterns 项不能为空", ErrGovernanceValidation)
		}
		if _, err := regexp.Compile(pat); err != nil {
			return fmt.Errorf("%w: exclude_repo_patterns %q 正则非法: %v", ErrGovernanceValidation, pat, err)
		}
	}
	if doc.Global.PoolReconcile != nil {
		if doc.Global.PoolReconcile.MaxR7Rounds < 0 || doc.Global.PoolReconcile.MaxR7Rounds > 100 {
			return fmt.Errorf("%w: pool_reconcile.max_r7_rounds 范围须在 [0, 100]", ErrGovernanceValidation)
		}
		if doc.Global.PoolReconcile.MaxR8Rounds < 0 || doc.Global.PoolReconcile.MaxR8Rounds > 1000 {
			return fmt.Errorf("%w: pool_reconcile.max_r8_rounds 范围须在 [0, 1000]", ErrGovernanceValidation)
		}
	}

	for _, id := range doc.Global.EnabledRules {
		if _, ok := validBuiltinRuleIDs[strings.TrimSpace(id)]; !ok {
			return fmt.Errorf("%w: 未知内置规则 ID %q", ErrGovernanceValidation, id)
		}
	}
	if err := validateRegexRules(doc.Global.RegexRewrites); err != nil {
		return err
	}
	for from, to := range doc.Global.ImportRewrites {
		if err := validateRelRewriteToken(from, false); err != nil {
			return err
		}
		if err := validateRelRewriteToken(to, false); err != nil {
			return err
		}
	}

	for key, spec := range doc.Repos {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%w: repos map key 不能为空", ErrGovernanceValidation)
		}
		if spec.SyncStrategy != "" && spec.SyncStrategy != models.GitSyncStrategyLocalCacheOnly && spec.SyncStrategy != models.GitSyncStrategyFetchAndRead {
			return fmt.Errorf("%w: repos[%s].sync_strategy 非法: %q", ErrGovernanceValidation, key, spec.SyncStrategy)
		}
		if err := validateExportSegment(spec.ExportTargetSubsystem, "export_target_subsystem"); err != nil {
			return err
		}
		if err := validateExportSegment(spec.ExportTargetRepoDir, "export_target_repo_dir"); err != nil {
			return err
		}
		for _, pfx := range spec.StripPrefixes {
			if err := validateRelRewriteToken(pfx, false); err != nil {
				return err
			}
		}
		for from, to := range spec.PathRewrites {
			if err := validateRelRewriteToken(from, false); err != nil {
				return err
			}
			if err := validateRelRewriteToken(to, true); err != nil {
				return err
			}
		}
		for _, rid := range spec.DisabledRules {
			if _, ok := validBuiltinRuleIDs[strings.TrimSpace(rid)]; !ok {
				return fmt.Errorf("%w: repos[%s].disabled_rules 包含未知规则 ID %q", ErrGovernanceValidation, key, rid)
			}
		}
		if err := validateRegexRules(spec.RegexRewrites); err != nil {
			return err
		}
		for from, to := range spec.ImportRewrites {
			if err := validateRelRewriteToken(from, false); err != nil {
				return err
			}
			if err := validateRelRewriteToken(to, false); err != nil {
				return err
			}
		}
		for _, f := range spec.ExemptFiles {
			if err := validateRelRewriteToken(f, false); err != nil {
				return err
			}
		}
		for _, f := range spec.QuarantinedFiles {
			if err := validateRelRewriteToken(f, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// MarshalGovernanceConfigCanonical 生成确定性 JSON 字节及 SHA-256 摘要，保证增量缓存键稳定。
func MarshalGovernanceConfigCanonical(doc GovernanceConfigDoc) ([]byte, string, error) {
	if doc.SchemaVersion == "" {
		doc.SchemaVersion = GovernanceSchemaVersion
	}
	if doc.Global.DefaultSyntax == "" {
		doc.Global.DefaultSyntax = "auto"
	}
	if doc.Global.StripPGSegment == nil {
		stripPG := true
		doc.Global.StripPGSegment = &stripPG
	}
	if doc.Global.ImportRewrites == nil {
		doc.Global.ImportRewrites = map[string]string{}
	}
	if doc.Repos == nil {
		doc.Repos = map[string]RepoGovernanceSpec{}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

// ResolveEffectiveExportTarget 按 ADR-27 方案 A 计算单仓在 protos/clean/ 下的有效导出目录。
// 注意：protos/raw/ 始终使用平台原始 subsystem_slug + repo_slug，不受此覆写影响。
func ResolveEffectiveExportTarget(repo models.GitRepo, override *RepoGovernanceSpec) (subsystemSlug string, repoDirSlug string) {
	rawSub := strings.TrimSpace(repo.ExportTargetSubsystem)
	if override != nil && strings.TrimSpace(override.ExportTargetSubsystem) != "" {
		rawSub = strings.TrimSpace(override.ExportTargetSubsystem)
	}
	if rawSub != "" {
		subsystemSlug = NormalizeSlug(rawSub, nil, "exp-sub:"+rawSub, true)
	} else if strings.TrimSpace(repo.SubsystemSlug) != "" {
		subsystemSlug = NormalizeSlug(repo.SubsystemSlug, nil, "sub:"+repo.SubsystemSlug, true)
	} else {
		subsystemSlug = UnassignedSubsystemSlug
	}

	rawDir := strings.TrimSpace(repo.ExportTargetRepoDir)
	if override != nil && strings.TrimSpace(override.ExportTargetRepoDir) != "" {
		rawDir = strings.TrimSpace(override.ExportTargetRepoDir)
	}
	if rawDir != "" {
		repoDirSlug = NormalizeSlug(rawDir, nil, fmt.Sprintf("exp-repo:%d", repo.ID), false)
	} else {
		repoDirSlug = NormalizeSlug(repo.Name, nil, fmt.Sprintf("repo:%d", repo.ID), false)
	}
	return subsystemSlug, repoDirSlug
}

// CheckGlobalExportTargetCollisions 检测全平台所有启用同步仓库之间的 (effective_subsystem, effective_repo_dir) 碰撞。
func CheckGlobalExportTargetCollisions(
	ctx context.Context,
	db *gorm.DB,
	overridesByRepoID map[uint64]*RepoGovernanceSpec,
) ([]ExportTargetCollision, error) {
	if db == nil {
		return nil, nil
	}
	var repos []models.GitRepo
	if err := db.WithContext(ctx).Order("id ASC").Find(&repos).Error; err != nil {
		return nil, err
	}

	type bucket struct {
		sub   string
		dir   string
		ids   []uint64
		names []string
	}
	buckets := make(map[string]*bucket, len(repos))
	var keys []string

	for _, r := range repos {
		ov := overridesByRepoID[r.ID]
		enabled := r.SyncEnabled
		if ov != nil && ov.SyncEnabled != nil {
			enabled = *ov.SyncEnabled
		}
		if !enabled {
			continue
		}
		sub, dir := ResolveEffectiveExportTarget(r, ov)
		k := sub + "\x00" + dir
		b, ok := buckets[k]
		if !ok {
			b = &bucket{sub: sub, dir: dir}
			buckets[k] = b
			keys = append(keys, k)
		}
		b.ids = append(b.ids, r.ID)
		b.names = append(b.names, r.Name)
	}

	sort.Strings(keys)
	var collisions []ExportTargetCollision
	for _, k := range keys {
		b := buckets[k]
		if len(b.ids) > 1 {
			collisions = append(collisions, ExportTargetCollision{
				TargetSubsystem: b.sub,
				TargetRepoDir:   b.dir,
				RepoIDs:         b.ids,
				RepoNames:       b.names,
			})
		}
	}
	if len(collisions) > 0 {
		first := collisions[0]
		return collisions, fmt.Errorf("%w: clean/%s/%s 被仓库 %v 同时占用",
			ErrExportTargetConflict, first.TargetSubsystem, first.TargetRepoDir, first.RepoNames)
	}
	return nil, nil
}

// IsWellKnownProtoImport 判断是否为标准 Protobuf Well-Known Types 导入路径（永不改写或剥离）。
func IsWellKnownProtoImport(relPath string) bool {
	cleaned := strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(relPath), "\\", "/"), "./")
	return strings.HasPrefix(cleaned, "google/protobuf/")
}

// StripPGPathSegment 按 POSIX '/' 切分路径段，仅移除严格等于 "pg" 且非最后一段文件名的独立目录段 (ADR-28 §2.1)。
// 严禁使用子串替换，确保 extpg/intpg/pggen/pg_common/mypg 与文件名 pg.proto 绝对不被误伤。
func StripPGPathSegment(relPath string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(relPath, "\\", "/"))
	if trimmed == "" {
		return ""
	}
	for strings.HasPrefix(trimmed, "./") {
		trimmed = strings.TrimPrefix(trimmed, "./")
	}
	if IsWellKnownProtoImport(trimmed) {
		return trimmed
	}
	segs := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(segs) <= 1 {
		return strings.Trim(trimmed, "/")
	}
	out := make([]string, 0, len(segs))
	lastIdx := len(segs) - 1
	for i, seg := range segs {
		if seg == "" || seg == "." {
			continue
		}
		if i < lastIdx && seg == "pg" {
			continue
		}
		out = append(out, seg)
	}
	if len(out) == 0 {
		return segs[lastIdx]
	}
	return strings.Join(out, "/")
}

// NormalizeLogicalPath 将任意相对路径或 import 路径归一化至全池唯一逻辑坐标系 (ADR-28 §2.1)。
func NormalizeLogicalPath(relPath string, stripPG bool) string {
	cur := strings.TrimSpace(strings.ReplaceAll(relPath, "\\", "/"))
	for strings.HasPrefix(cur, "./") {
		cur = strings.TrimPrefix(cur, "./")
	}
	cur = strings.TrimLeft(cur, "/")
	if cur == "" {
		return ""
	}
	if IsWellKnownProtoImport(cur) {
		return cur
	}
	if stripPG {
		cur = StripPGPathSegment(cur)
	}
	if cleaned, err := decoder.SanitizeProtoRelPath(cur, true); err == nil && cleaned != "" {
		return cleaned
	}
	return cur
}

// ShouldExcludeRepoByR0 判断普通业务仓库是否命中 R0 仓库/子系统级排除规则（严禁用于 R4 shared_base_sources）。
func ShouldExcludeRepoByR0(subsystem, repoName string, global GlobalGovernanceSection) bool {
	subTrim := strings.TrimSpace(subsystem)
	for _, exSub := range global.ExcludeSubsystems {
		if exSub != "" && strings.EqualFold(strings.TrimSpace(exSub), subTrim) {
			return true
		}
	}
	nameTrim := strings.TrimSpace(repoName)
	for _, pat := range global.ExcludeRepoPatterns {
		if pat == "" {
			continue
		}
		if re, err := regexp.Compile(pat); err == nil && re.MatchString(nameTrim) {
			return true
		}
	}
	return false
}

// CheckPoolStripPGCollisions 对全池路径（业务仓 + R4 基座）执行去 /pg 剥离前后碰撞检测。
// 仅报告「剥离后新产生、而在剥离前未出现」的碰撞组，确保去 /pg 动作本身不新增任何冲突。
func CheckPoolStripPGCollisions(candidates []PoolPathCandidate) []PoolStripCollision {
	if len(candidates) == 0 {
		return nil
	}
	preBuckets := make(map[string]map[string]struct{}, len(candidates))
	postBuckets := make(map[string][]PoolPathCandidate, len(candidates))
	for _, c := range candidates {
		pre := strings.TrimSpace(c.PreStripPath)
		post := strings.TrimSpace(c.PostStripPath)
		if post == "" {
			continue
		}
		if pre != "" {
			if preBuckets[pre] == nil {
				preBuckets[pre] = make(map[string]struct{})
			}
			preBuckets[pre][fmt.Sprintf("%d:%s", c.RepoID, c.RepoName)] = struct{}{}
		}
		postBuckets[post] = append(postBuckets[post], c)
	}

	keys := make([]string, 0, len(postBuckets))
	for k := range postBuckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var collisions []PoolStripCollision
	for _, post := range keys {
		group := postBuckets[post]
		if len(group) <= 1 {
			continue
		}
		// 若该组内所有成员原本在 PreStripPath 下就已经指向同一个路径（即原池已有的多仓同名冲突），
		// 不算由去 /pg 引入的新碰撞。
		distinctPre := make(map[string]struct{}, len(group))
		for _, item := range group {
			distinctPre[item.PreStripPath] = struct{}{}
		}
		if len(distinctPre) <= 1 {
			continue
		}
		sort.Slice(group, func(i, j int) bool {
			if group[i].RepoID != group[j].RepoID {
				return group[i].RepoID < group[j].RepoID
			}
			return group[i].PreStripPath < group[j].PreStripPath
		})
		prePaths := make([]string, 0, len(group))
		repoIDs := make([]uint64, 0, len(group))
		repoNames := make([]string, 0, len(group))
		for _, item := range group {
			prePaths = append(prePaths, item.PreStripPath)
			repoIDs = append(repoIDs, item.RepoID)
			repoNames = append(repoNames, item.RepoName)
		}
		collisions = append(collisions, PoolStripCollision{
			PostStripPath: post,
			PreStripPaths: prePaths,
			RepoIDs:       repoIDs,
			RepoNames:     repoNames,
		})
	}
	return collisions
}

// ApplyIntraRepoPathMapping 应用单仓 strip_prefixes（按配置顺序首次命中即剥离）与 path_rewrites（最长前缀替换）。
func ApplyIntraRepoPathMapping(repoRelPath string, stripPrefixes []string, pathRewrites map[string]string) (string, error) {
	return ApplyIntraRepoPathMappingWithPG(repoRelPath, stripPrefixes, pathRewrites, false)
}

// ApplyIntraRepoPathMappingWithPG 按 ADR-28 §2.1 确定性顺序执行：strip_prefixes -> path_rewrites -> StripPGPathSegment(若启用) -> SanitizeProtoRelPath。
func ApplyIntraRepoPathMappingWithPG(repoRelPath string, stripPrefixes []string, pathRewrites map[string]string, stripPG bool) (string, error) {
	cleaned, err := decoder.SanitizeProtoRelPath(repoRelPath, true)
	if err != nil {
		return "", err
	}
	cur := cleaned

	for _, rawPrefix := range stripPrefixes {
		pfx := strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(rawPrefix), "\\", "/"), "./")
		if pfx == "" {
			continue
		}
		if !strings.HasSuffix(pfx, "/") {
			pfx += "/"
		}
		if strings.HasPrefix(cur, pfx) {
			trimmed := strings.TrimPrefix(cur, pfx)
			if trimmed != "" {
				cur = trimmed
			}
			break
		}
	}

	if len(pathRewrites) > 0 {
		type kv struct {
			from string
			to   string
		}
		rules := make([]kv, 0, len(pathRewrites))
		for k, v := range pathRewrites {
			nk := strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(k), "\\", "/"), "./")
			if nk == "" {
				continue
			}
			rules = append(rules, kv{from: nk, to: strings.ReplaceAll(strings.TrimSpace(v), "\\", "/")})
		}
		sort.Slice(rules, func(i, j int) bool {
			if len(rules[i].from) != len(rules[j].from) {
				return len(rules[i].from) > len(rules[j].from)
			}
			return rules[i].from < rules[j].from
		})
		for _, r := range rules {
			if strings.HasPrefix(cur, r.from) {
				rest := strings.TrimPrefix(cur, r.from)
				to := r.to
				if to != "" && !strings.HasSuffix(to, "/") && rest != "" && !strings.HasPrefix(rest, "/") {
					if strings.HasSuffix(r.from, "/") {
						to += "/"
					}
				}
				joined := strings.TrimLeft(to+rest, "/")
				if joined != "" {
					cur = joined
				}
				break
			}
		}
	}

	if stripPG {
		cur = StripPGPathSegment(cur)
	}

	mapped, err := decoder.SanitizeProtoRelPath(cur, true)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrIntraRepoPathConflict, err)
	}
	return mapped, nil
}

// MapAndValidateIntraRepoPaths 对仓内所有源文件应用裁剪与重写，并在出现多对一碰撞时返回冲突明细。
func MapAndValidateIntraRepoPaths(
	paths []string,
	stripPrefixes []string,
	pathRewrites map[string]string,
) (map[string]string, []IntraRepoPathCollision, error) {
	return MapAndValidateIntraRepoPathsWithPG(paths, stripPrefixes, pathRewrites, false)
}

// MapAndValidateIntraRepoPathsWithPG 对仓内所有源文件按是否剥离 /pg 执行裁剪重写与仓内碰撞检测。
func MapAndValidateIntraRepoPathsWithPG(
	paths []string,
	stripPrefixes []string,
	pathRewrites map[string]string,
	stripPG bool,
) (map[string]string, []IntraRepoPathCollision, error) {
	sortedPaths := append([]string(nil), paths...)
	sort.Strings(sortedPaths)

	mapped := make(map[string]string, len(sortedPaths))
	reverse := make(map[string][]string, len(sortedPaths))
	for _, p := range sortedPaths {
		m, err := ApplyIntraRepoPathMappingWithPG(p, stripPrefixes, pathRewrites, stripPG)
		if err != nil {
			return nil, nil, err
		}
		mapped[p] = m
		reverse[m] = append(reverse[m], p)
	}

	var keys []string
	for k, sources := range reverse {
		if len(sources) > 1 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	collisions := make([]IntraRepoPathCollision, 0, len(keys))
	for _, k := range keys {
		collisions = append(collisions, IntraRepoPathCollision{
			MappedPath:  k,
			SourcePaths: reverse[k],
		})
	}
	if len(collisions) > 0 {
		first := collisions[0]
		return mapped, collisions, fmt.Errorf("%w: %v 同时映射到 %s",
			ErrIntraRepoPathConflict, first.SourcePaths, first.MappedPath)
	}
	return mapped, nil, nil
}

func matchRepoSpecInDoc(repo models.GitRepo, doc *GovernanceConfigDoc) (RepoGovernanceSpec, bool) {
	if doc == nil || len(doc.Repos) == 0 {
		return RepoGovernanceSpec{}, false
	}
	if spec, ok := doc.Repos[repo.Name]; ok {
		return spec, true
	}
	for _, spec := range doc.Repos {
		if spec.PlatformRepoID > 0 && spec.PlatformRepoID == repo.PlatformRepoID {
			return spec, true
		}
	}
	return RepoGovernanceSpec{}, false
}

// MergeRepoGovernanceSpecFromModel 将 DB 中单仓治理字段合并导出为 RepoGovernanceSpec（用于生成 .json 视图）。
func MergeRepoGovernanceSpecFromModel(repo models.GitRepo) (RepoGovernanceSpec, bool) {
	normCfg := ParseRepoNormalizeConfig(repo.RepoNormalizeConfigJSON)
	stripPrefixes := repo.StripPrefixes()
	pathRewrites := repo.PathRewrites()
	hasOverride := !repo.SyncEnabled ||
		strings.TrimSpace(repo.ExportTargetSubsystem) != "" ||
		strings.TrimSpace(repo.ExportTargetRepoDir) != "" ||
		normCfg.DisableStripPGSegment ||
		len(stripPrefixes) > 0 ||
		len(pathRewrites) > 0 ||
		len(repo.CollectGlobs()) > 0 ||
		len(repo.ExcludeGlobs()) > 0 ||
		len(normCfg.DisabledRules) > 0 ||
		len(normCfg.RegexRewrites) > 0 ||
		len(normCfg.ImportRewrites) > 0 ||
		len(normCfg.ExemptFiles) > 0 ||
		len(normCfg.QuarantinedFiles) > 0
	if !hasOverride {
		return RepoGovernanceSpec{}, false
	}
	enabled := repo.SyncEnabled
	return RepoGovernanceSpec{
		PlatformRepoID:        repo.PlatformRepoID,
		SyncEnabled:           &enabled,
		SyncStrategy:          repo.SyncStrategy,
		ExportTargetSubsystem: repo.ExportTargetSubsystem,
		ExportTargetRepoDir:   repo.ExportTargetRepoDir,
		DisableStripPGSegment: normCfg.DisableStripPGSegment,
		StripPrefixes:         stripPrefixes,
		PathRewrites:          pathRewrites,
		CollectGlobs:          repo.CollectGlobs(),
		ExcludeGlobs:          repo.ExcludeGlobs(),
		DisabledRules:         normCfg.DisabledRules,
		RegexRewrites:         normCfg.RegexRewrites,
		ImportRewrites:        normCfg.ImportRewrites,
		ExemptFiles:           normCfg.ExemptFiles,
		QuarantinedFiles:      normCfg.QuarantinedFiles,
	}, true
}

// BuildEffectiveRepoAuxJSON 构造融合全局治理配置与单仓治理字段的规范 JSON，供 EffectiveNormalizerVersion 计算缓存键。
func BuildEffectiveRepoAuxJSON(repo models.GitRepo, globalCfg *GovernanceConfigDoc) []byte {
	payload := map[string]any{
		"export_target_subsystem": repo.ExportTargetSubsystem,
		"export_target_repo_dir":  repo.ExportTargetRepoDir,
		"strip_prefixes":          repo.StripPrefixes(),
		"path_rewrites":           repo.PathRewrites(),
		"repo_normalize_config":   ParseRepoNormalizeConfig(repo.RepoNormalizeConfigJSON),
	}
	if globalCfg != nil {
		payload["global"] = globalCfg.Global
		if spec, ok := matchRepoSpecInDoc(repo, globalCfg); ok {
			payload["doc_repo_override"] = spec
		}
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// LoadActiveGovernanceConfig 读取当前生效的 proto-governance/v1 配置；若尚未保存过则返回内置默认配置。
func LoadActiveGovernanceConfig(ctx context.Context, db *gorm.DB) (*GovernanceConfigSnapshot, error) {
	doc := DefaultGovernanceConfig()
	if db != nil {
		var rec models.ProtoGovernanceConfig
		err := db.WithContext(ctx).Where("is_active = true").Order("revision DESC, id DESC").First(&rec).Error
		if err == nil {
			var parsed GovernanceConfigDoc
			if uErr := json.Unmarshal(rec.ConfigJSON, &parsed); uErr == nil {
				doc = parsed
			}
			doc.Revision = rec.Revision
			_, canonicalSHA, _ := MarshalGovernanceConfigCanonical(doc)
			if rec.ConfigSHA256 != "" {
				canonicalSHA = rec.ConfigSHA256
			}
			return &GovernanceConfigSnapshot{
				Doc:          doc,
				ConfigSHA256: canonicalSHA,
				UpdatedBy:    rec.UpdatedBy,
			}, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}

		// 若表为空，自动补充已有单仓治理配置以便导出 .json 时完整体现当前状态
		var repos []models.GitRepo
		if err := db.WithContext(ctx).Order("id ASC").Find(&repos).Error; err == nil {
			for _, r := range repos {
				if spec, ok := MergeRepoGovernanceSpecFromModel(r); ok {
					doc.Repos[r.Name] = spec
				}
			}
		}
	}
	_, shaHex, _ := MarshalGovernanceConfigCanonical(doc)
	return &GovernanceConfigSnapshot{
		Doc:          doc,
		ConfigSHA256: shaHex,
	}, nil
}

// SaveGovernanceConfig 验证 proto-governance/v1、检测全局目录碰撞并持久化为新激活版本，同时回写匹配仓库字段。
func SaveGovernanceConfig(
	ctx context.Context,
	db *gorm.DB,
	doc GovernanceConfigDoc,
	updatedBy string,
) (*GovernanceConfigSnapshot, []models.GitRepo, []ExportTargetCollision, error) {
	if db == nil {
		return nil, nil, nil, errors.New("db 不能为空")
	}
	if err := ValidateGovernanceConfig(&doc); err != nil {
		return nil, nil, nil, err
	}

	var repos []models.GitRepo
	if err := db.WithContext(ctx).Order("id ASC").Find(&repos).Error; err != nil {
		return nil, nil, nil, err
	}
	overridesByRepoID := make(map[uint64]*RepoGovernanceSpec)
	matchedSpecByRepoID := make(map[uint64]RepoGovernanceSpec)
	for _, r := range repos {
		if spec, ok := matchRepoSpecInDoc(r, &doc); ok {
			sp := spec
			overridesByRepoID[r.ID] = &sp
			matchedSpecByRepoID[r.ID] = sp
		}
	}

	if cols, cErr := CheckGlobalExportTargetCollisions(ctx, db, overridesByRepoID); cErr != nil {
		return nil, nil, cols, cErr
	}

	var snapshot *GovernanceConfigSnapshot
	var affectedRepos []models.GitRepo

	txErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxRev int
		_ = tx.Model(&models.ProtoGovernanceConfig{}).
			Select("COALESCE(MAX(revision), 0)").
			Scan(&maxRev).Error
		doc.Revision = maxRev + 1

		canonBytes, shaHex, mErr := MarshalGovernanceConfigCanonical(doc)
		if mErr != nil {
			return mErr
		}

		if err := tx.Model(&models.ProtoGovernanceConfig{}).
			Where("is_active = true").
			Update("is_active", false).Error; err != nil {
			return err
		}

		rec := models.ProtoGovernanceConfig{
			Revision:     doc.Revision,
			ConfigSHA256: shaHex,
			ConfigJSON:   datatypes.JSON(canonBytes),
			UpdatedBy:    updatedBy,
			IsActive:     true,
		}
		if err := tx.Create(&rec).Error; err != nil {
			return err
		}

		for _, r := range repos {
			spec, ok := matchedSpecByRepoID[r.ID]
			if !ok {
				continue
			}
			updates := make(map[string]any)
			if spec.SyncEnabled != nil && *spec.SyncEnabled != r.SyncEnabled {
				updates["sync_enabled"] = *spec.SyncEnabled
				r.SyncEnabled = *spec.SyncEnabled
				if !*spec.SyncEnabled {
					updates["proto_status"] = models.GitRepoProtoStatusExcluded
					r.ProtoStatus = models.GitRepoProtoStatusExcluded
				} else if r.ProtoStatus == models.GitRepoProtoStatusExcluded {
					updates["proto_status"] = models.GitRepoProtoStatusPending
					r.ProtoStatus = models.GitRepoProtoStatusPending
				}
			}
			if spec.SyncStrategy != "" && spec.SyncStrategy != r.SyncStrategy {
				updates["sync_strategy"] = spec.SyncStrategy
				r.SyncStrategy = spec.SyncStrategy
			}
			if spec.ExportTargetSubsystem != r.ExportTargetSubsystem {
				updates["export_target_subsystem"] = strings.TrimSpace(spec.ExportTargetSubsystem)
				r.ExportTargetSubsystem = strings.TrimSpace(spec.ExportTargetSubsystem)
			}
			if spec.ExportTargetRepoDir != r.ExportTargetRepoDir {
				updates["export_target_repo_dir"] = strings.TrimSpace(spec.ExportTargetRepoDir)
				r.ExportTargetRepoDir = strings.TrimSpace(spec.ExportTargetRepoDir)
			}
			if b, err := json.Marshal(nonNilStringsSlice(spec.StripPrefixes)); err == nil {
				updates["strip_prefixes_json"] = datatypes.JSON(b)
				r.StripPrefixesJSON = datatypes.JSON(b)
			}
			if spec.PathRewrites != nil {
				if b, err := json.Marshal(spec.PathRewrites); err == nil {
					updates["path_rewrites_json"] = datatypes.JSON(b)
					r.PathRewritesJSON = datatypes.JSON(b)
				}
			}
			if len(spec.CollectGlobs) > 0 {
				if b, err := json.Marshal(spec.CollectGlobs); err == nil {
					updates["collect_globs_json"] = datatypes.JSON(b)
					r.CollectGlobsJSON = datatypes.JSON(b)
				}
			}
			mergedExclude := append([]string(nil), spec.ExcludeGlobs...)
			mergedExclude = append(mergedExclude, spec.ExtraExcludeGlobs...)
			if len(mergedExclude) > 0 {
				if b, err := json.Marshal(mergedExclude); err == nil {
					updates["exclude_globs_json"] = datatypes.JSON(b)
					r.ExcludeGlobsJSON = datatypes.JSON(b)
				}
			}
			normCfg := RepoNormalizeConfig{
				DisableStripPGSegment: spec.DisableStripPGSegment,
				DisabledRules:         nonNilStringsSlice(spec.DisabledRules),
				RegexRewrites:         spec.RegexRewrites,
				ImportRewrites:        spec.ImportRewrites,
				ExemptFiles:           nonNilStringsSlice(spec.ExemptFiles),
				QuarantinedFiles:      nonNilStringsSlice(spec.QuarantinedFiles),
			}
			if b, err := json.Marshal(normCfg); err == nil {
				updates["repo_normalize_config_json"] = datatypes.JSON(b)
				r.RepoNormalizeConfigJSON = datatypes.JSON(b)
			}
			if len(updates) > 0 {
				if err := tx.Model(&models.GitRepo{}).Where("id = ?", r.ID).Updates(updates).Error; err != nil {
					return err
				}
				affectedRepos = append(affectedRepos, r)
			}
		}

		snapshot = &GovernanceConfigSnapshot{
			Doc:          doc,
			ConfigSHA256: shaHex,
			UpdatedBy:    updatedBy,
		}
		return nil
	})
	if txErr != nil {
		return nil, nil, nil, txErr
	}
	return snapshot, affectedRepos, nil, nil
}
