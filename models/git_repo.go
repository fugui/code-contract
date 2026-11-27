package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"code-common/backend/gitsync"
	"code-contract/config"
	"code-contract/internal/server/security"

	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	GitProviderGitLab  = "GITLAB"
	GitProviderGitHub  = "GITHUB"
	GitProviderGitea   = "GITEA"
	GitProviderGeneric = "GENERIC"

	GitAuthNone   = "NONE"
	GitAuthToken  = "TOKEN"
	GitAuthSSHKey = "SSH_KEY"

	GitSyncWebhook = "WEBHOOK"
	GitSyncPoll    = "POLL"
	GitSyncManual  = "MANUAL"

	GitSyncStatusIdle    = "IDLE"
	GitSyncStatusRunning = "RUNNING"
	GitSyncStatusSuccess = "SUCCESS"
	GitSyncStatusFailed  = "FAILED"

	MRAutoPublishDisabled     = "DISABLED"
	MRAutoPublishDraftOnly    = "DRAFT_ONLY"
	MRAutoPublishAutoActivate = "AUTO_ACTIVATE"

	// ADR-26 (r4) & ADR-27: 平台代码仓唯一共享模式、同步策略、6 态分层、流水线状态与导入归属来源
	GitSourceModePlatformShared = "PLATFORM_SHARED"

	GitSyncStrategyFetchAndRead   = "FETCH_AND_READ"
	GitSyncStrategyLocalCacheOnly = "LOCAL_CACHE_ONLY"

	GitRepoProtoStatusPending      = "PENDING"
	GitRepoProtoStatusHasProto     = "HAS_PROTO"
	GitRepoProtoStatusNoProto      = "NO_PROTO"
	GitRepoProtoStatusNoLocalCache = "NO_LOCAL_CACHE"
	GitRepoProtoStatusExcluded     = "EXCLUDED"
	GitRepoProtoStatusSyncFailed   = "SYNC_FAILED"

	SyncJobScopeAll          = "ALL"
	SyncJobScopeDepartment   = "DEPARTMENT"
	SyncJobScopeServiceGroup = "SERVICE_GROUP"
	SyncJobScopeFailedOnly   = "FAILED_ONLY"
	SyncJobScopeSelected     = "SELECTED"

	SyncCachePolicyCacheOnly    = "CACHE_ONLY"
	SyncCachePolicyFetchMissing = "FETCH_MISSING"

	SyncJobStatusRunning       = "RUNNING"
	SyncJobStatusCompleted     = "COMPLETED"
	SyncJobStatusPartialFailed = "PARTIAL_FAILED"
	SyncJobStatusFailed        = "FAILED"

	ProtoPipelineCollected   = "COLLECTED"
	ProtoPipelineNormalized  = "NORMALIZED"
	ProtoPipelineQuarantined = "QUARANTINED"

	ImportOwnerSourceManual  = "MANUAL"
	ImportOwnerSourceBaseLib = "BASE_LIB"
	ImportOwnerSourceAuto    = "AUTO"
	ImportOwnerSourceR3Auto  = "R3_AUTO"

	PoolReconcileStatusRunning   = "RUNNING"
	PoolReconcileStatusCompleted = "COMPLETED"
	PoolReconcileStatusFailed    = "FAILED"

	// ADR-29: 阶段 ④ 单仓拓扑发布状态与一键四阶段流水线状态
	BundlePublishStatusPending          = "PENDING"
	BundlePublishStatusPublished        = "PUBLISHED"
	BundlePublishStatusSkippedUnchanged = "SKIPPED_UNCHANGED"
	BundlePublishStatusBlockedBreaking  = "BLOCKED_BREAKING"
	BundlePublishStatusBlockedConflict  = "BLOCKED_CONFLICT"
	BundlePublishStatusPendingUpstream  = "PENDING_UPSTREAM"
	BundlePublishStatusFailed           = "FAILED"

	PublishModeAuto    = "AUTO"
	PublishModePreview = "PREVIEW"

	PublishRunStatusRunning        = "RUNNING"
	PublishRunStatusCompleted      = "COMPLETED"
	PublishRunStatusPreviewReady   = "PREVIEW_READY"
	PublishRunStatusPartialBlocked = "PARTIAL_BLOCKED"
	PublishRunStatusFailed         = "FAILED"
	PublishRunStatusRejectedStale  = "REJECTED_STALE"

	PipelineStageCollectNormalize = "COLLECT_NORMALIZE"
	PipelineStageReconcile        = "RECONCILE"
	PipelineStagePublish          = "PUBLISH"

	PipelineRunStatusRunning        = "RUNNING"
	PipelineRunStatusCompleted      = "COMPLETED"
	PipelineRunStatusPartialBlocked = "PARTIAL_BLOCKED"
	PipelineRunStatusFailed         = "FAILED"

	PipelineTriggerManual   = "MANUAL"
	PipelineTriggerMRMerge  = "MR_MERGE"
	PipelineTriggerSchedule = "SCHEDULE"
)

// GitRepo 实体 #20：公共 repositories 的 1:1 契约治理扩展表（code-bench 为唯一真值源）
type GitRepo struct {
	ID                      uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	PlatformRepoID          uint64         `gorm:"column:platform_repo_id;not null" json:"platform_repo_id"` // 关联公共 repositories.id（NOT NULL 唯一索引）
	SourceMode              string         `gorm:"column:source_mode;size:32;not null;default:'PLATFORM_SHARED'" json:"source_mode"`
	SyncStrategy            string         `gorm:"column:sync_strategy;size:32;not null;default:'LOCAL_CACHE_ONLY'" json:"sync_strategy"`
	ProtoStatus             string         `gorm:"column:proto_status;size:32;not null;default:'PENDING';index:idx_contract_git_repo_proto_status" json:"proto_status"`
	SyncEnabled             bool           `gorm:"column:sync_enabled;not null;default:true;index:idx_contract_git_repo_sync_enabled" json:"sync_enabled"`
	SubsystemSlug           string         `gorm:"column:subsystem_slug;size:128;not null;default:''" json:"subsystem_slug"`
	ExportTargetSubsystem   string         `gorm:"column:export_target_subsystem;size:128;not null;default:''" json:"export_target_subsystem"`
	ExportTargetRepoDir     string         `gorm:"column:export_target_repo_dir;size:128;not null;default:''" json:"export_target_repo_dir"`
	StripPrefixesJSON       datatypes.JSON `gorm:"column:strip_prefixes_json;type:jsonb" json:"strip_prefixes_json,omitempty"`
	PathRewritesJSON        datatypes.JSON `gorm:"column:path_rewrites_json;type:jsonb" json:"path_rewrites_json,omitempty"`
	RepoNormalizeConfigJSON datatypes.JSON `gorm:"column:repo_normalize_config_json;type:jsonb" json:"repo_normalize_config_json,omitempty"`
	ResolvedBranch          string         `gorm:"column:resolved_branch;size:128;not null;default:''" json:"resolved_branch"`
	CollectGlobsJSON        datatypes.JSON `gorm:"column:collect_globs_json;type:jsonb" json:"collect_globs_json,omitempty"`
	ExcludeGlobsJSON        datatypes.JSON `gorm:"column:exclude_globs_json;type:jsonb" json:"exclude_globs_json,omitempty"`
	LastFetchedAt           *time.Time     `gorm:"column:last_fetched_at" json:"last_fetched_at,omitempty"`
	RefCommitTime           *time.Time     `gorm:"column:ref_commit_time" json:"ref_commit_time,omitempty"`
	LastCollectedCommit     string         `gorm:"column:last_collected_commit;size:64;not null;default:''" json:"last_collected_commit"`
	Name                    string         `gorm:"size:128;not null;uniqueIndex:uk_contract_git_repo_name" json:"name"`
	Provider                string         `gorm:"size:32;not null;default:'GITLAB'" json:"provider"` // GITLAB | GITHUB | GITEA | GENERIC
	RepoURL                 string         `gorm:"size:512;not null" json:"repo_url"`
	APIBaseURL              string         `gorm:"size:512;not null;default:''" json:"api_base_url"`
	DefaultBranch           string         `gorm:"size:128;not null;default:'main'" json:"default_branch"`
	AuthType                string         `gorm:"size:32;not null;default:'NONE'" json:"auth_type"` // NONE | TOKEN | SSH_KEY
	CredentialsEnc          string         `gorm:"type:text;not null;default:''" json:"-"`           // 兼容保留列，禁止新写入或导出
	CredentialHint          string         `gorm:"size:64;not null;default:''" json:"credential_hint"`
	WebhookSecretEnc        string         `gorm:"type:text;not null;default:''" json:"-"`           // AES-256-GCM 密文，严禁序列化泄漏
	SyncMode                string         `gorm:"size:32;not null;default:'POLL'" json:"sync_mode"` // WEBHOOK | POLL | MANUAL
	PollIntervalSec         int            `gorm:"not null;default:1800" json:"poll_interval_sec"`
	AutoCompile             bool           `gorm:"not null;default:true" json:"auto_compile"`
	AutoActivate            bool           `gorm:"not null;default:false" json:"auto_activate"`
	LintOnSync              bool           `gorm:"not null;default:true" json:"lint_on_sync"`
	LastSyncAt              *time.Time     `json:"last_sync_at,omitempty"`
	LastSyncCommit          string         `gorm:"size:64;not null;default:''" json:"last_sync_commit"`
	LastSyncStatus          string         `gorm:"size:32;not null;default:'IDLE'" json:"last_sync_status"` // IDLE | RUNNING | SUCCESS | FAILED
	LastSyncError           string         `gorm:"type:text;not null;default:''" json:"last_sync_error"`
	Description             string         `gorm:"type:text;not null;default:''" json:"description"`
	CreatedBy               uint           `gorm:"not null;default:0" json:"created_by"`
	CreatedAt               time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt               time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
}

func (GitRepo) TableName() string {
	return config.TablePrefix + "git_repos"
}

// GitRepoRedactedDTO 脱敏后的 GitRepo 接口输出模型（绝不返回 CredentialsEnc / WebhookSecretEnc / 明文）
type GitRepoRedactedDTO struct {
	ID                    uint64            `json:"id"`
	PlatformRepoID        uint64            `json:"platform_repo_id"`
	SourceMode            string            `json:"source_mode"`
	SyncStrategy          string            `json:"sync_strategy"`
	ProtoStatus           string            `json:"proto_status"`
	SyncEnabled           bool              `json:"sync_enabled"`
	SubsystemSlug         string            `json:"subsystem_slug"`
	ExportTargetSubsystem string            `json:"export_target_subsystem"`
	ExportTargetRepoDir   string            `json:"export_target_repo_dir"`
	StripPrefixes         []string          `json:"strip_prefixes,omitempty"`
	PathRewrites          map[string]string `json:"path_rewrites,omitempty"`
	RepoNormalizeConfig   datatypes.JSON    `json:"repo_normalize_config,omitempty"`
	ResolvedBranch        string            `json:"resolved_branch"`
	CollectGlobs          []string          `json:"collect_globs,omitempty"`
	ExcludeGlobs          []string          `json:"exclude_globs,omitempty"`
	LastFetchedAt         *time.Time        `json:"last_fetched_at,omitempty"`
	RefCommitTime         *time.Time        `json:"ref_commit_time,omitempty"`
	LastCollectedCommit   string            `json:"last_collected_commit"`
	Name                  string            `json:"name"`
	Provider              string            `json:"provider"`
	RepoURL               string            `json:"repo_url"`
	APIBaseURL            string            `json:"api_base_url"`
	DefaultBranch         string            `json:"default_branch"`
	AuthType              string            `json:"auth_type"`
	HasCredentials        bool              `json:"has_credentials"`
	CredentialHint        string            `json:"credential_hint"`
	HasWebhookSecret      bool              `json:"has_webhook_secret"`
	SyncMode              string            `json:"sync_mode"`
	PollIntervalSec       int               `json:"poll_interval_sec"`
	AutoCompile           bool              `json:"auto_compile"`
	AutoActivate          bool              `json:"auto_activate"`
	LintOnSync            bool              `json:"lint_on_sync"`
	LastSyncAt            *time.Time        `json:"last_sync_at,omitempty"`
	LastSyncCommit        string            `json:"last_sync_commit"`
	LastSyncStatus        string            `json:"last_sync_status"`
	LastSyncError         string            `json:"last_sync_error"`
	Description           string            `json:"description"`
	CreatedBy             uint              `json:"created_by"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
	Bundles               []GitProtoBundle  `json:"bundles,omitempty"`
}

// MaskSecretHint 生成不可逆的脱敏凭证尾部摘要提示（永不暴露核心秘密片段）
func MaskSecretHint(plain string) string {
	trimmed := strings.TrimSpace(plain)
	if trimmed == "" {
		return ""
	}
	// SSH 私钥统一固定格式摘要，避免暴露关键头尾内容
	if strings.Contains(trimmed, "PRIVATE KEY") {
		last := trimmed
		if len(last) > 4 {
			last = last[len(last)-4:]
		}
		return "ssh-key****" + last
	}
	if len(trimmed) < 10 {
		return "****"
	}
	if len(trimmed) >= 12 {
		return trimmed[:4] + "****" + trimmed[len(trimmed)-4:]
	}
	return "****" + trimmed[len(trimmed)-4:]
}

// SetCredentials 使用 CONTRACT_CRYPTO_KEY 通过 AES-256-GCM 加密认证凭证并生成脱敏提示
func (g *GitRepo) SetCredentials(plain string) error {
	trimmed := strings.TrimSpace(plain)
	if trimmed == "" {
		g.CredentialsEnc = ""
		g.CredentialHint = ""
		return nil
	}
	enc, err := security.EncryptSecret(trimmed)
	if err != nil {
		return err
	}
	g.CredentialsEnc = enc
	g.CredentialHint = MaskSecretHint(trimmed)
	return nil
}

// DecryptCredentials 使用 CONTRACT_CRYPTO_KEY 在内存中按需解密认证凭证
func (g *GitRepo) DecryptCredentials() (string, error) {
	if strings.TrimSpace(g.CredentialsEnc) == "" {
		return "", nil
	}
	return security.DecryptSecret(g.CredentialsEnc)
}

// SetWebhookSecret 使用 CONTRACT_CRYPTO_KEY 通过 AES-256-GCM 加密 Webhook Secret
func (g *GitRepo) SetWebhookSecret(plain string) error {
	trimmed := strings.TrimSpace(plain)
	if trimmed == "" {
		g.WebhookSecretEnc = ""
		return nil
	}
	enc, err := security.EncryptSecret(trimmed)
	if err != nil {
		return err
	}
	g.WebhookSecretEnc = enc
	return nil
}

// DecryptWebhookSecret 使用 CONTRACT_CRYPTO_KEY 在内存中按需解密 Webhook Secret
func (g *GitRepo) DecryptWebhookSecret() (string, error) {
	if strings.TrimSpace(g.WebhookSecretEnc) == "" {
		return "", nil
	}
	return security.DecryptSecret(g.WebhookSecretEnc)
}

// ToRedactedDTO 转换为严格脱敏 API 视图对象
func (g *GitRepo) ToRedactedDTO(bundles ...GitProtoBundle) GitRepoRedactedDTO {
	hasCreds := strings.TrimSpace(g.CredentialsEnc) != ""
	hint := g.CredentialHint
	if hasCreds && hint == "" {
		if dec, err := g.DecryptCredentials(); err == nil && dec != "" {
			hint = MaskSecretHint(dec)
		} else {
			hint = "****"
		}
	}
	if !hasCreds {
		hint = ""
	}
	sourceMode := g.SourceMode
	if sourceMode == "" {
		sourceMode = GitSourceModePlatformShared
	}
	syncStrategy := g.SyncStrategy
	if syncStrategy == "" {
		syncStrategy = GitSyncStrategyLocalCacheOnly
	}
	protoStatus := g.ProtoStatus
	if protoStatus == "" {
		protoStatus = GitRepoProtoStatusPending
	}
	return GitRepoRedactedDTO{
		ID:                    g.ID,
		PlatformRepoID:        g.PlatformRepoID,
		SourceMode:            sourceMode,
		SyncStrategy:          syncStrategy,
		ProtoStatus:           protoStatus,
		SyncEnabled:           g.SyncEnabled,
		SubsystemSlug:         g.SubsystemSlug,
		ExportTargetSubsystem: g.ExportTargetSubsystem,
		ExportTargetRepoDir:   g.ExportTargetRepoDir,
		StripPrefixes:         g.StripPrefixes(),
		PathRewrites:          g.PathRewrites(),
		RepoNormalizeConfig:   g.RepoNormalizeConfigJSON,
		ResolvedBranch:        g.ResolvedBranch,
		CollectGlobs:          g.CollectGlobs(),
		ExcludeGlobs:          g.ExcludeGlobs(),
		LastFetchedAt:         g.LastFetchedAt,
		RefCommitTime:         g.RefCommitTime,
		LastCollectedCommit:   g.LastCollectedCommit,
		Name:                  g.Name,
		Provider:              g.Provider,
		RepoURL:               g.RepoURL,
		APIBaseURL:            g.APIBaseURL,
		DefaultBranch:         g.DefaultBranch,
		AuthType:              g.AuthType,
		HasCredentials:        hasCreds,
		CredentialHint:        hint,
		HasWebhookSecret:      strings.TrimSpace(g.WebhookSecretEnc) != "",
		SyncMode:              g.SyncMode,
		PollIntervalSec:       g.PollIntervalSec,
		AutoCompile:           g.AutoCompile,
		AutoActivate:          g.AutoActivate,
		LintOnSync:            g.LintOnSync,
		LastSyncAt:            g.LastSyncAt,
		LastSyncCommit:        g.LastSyncCommit,
		LastSyncStatus:        g.LastSyncStatus,
		LastSyncError:         g.LastSyncError,
		Description:           g.Description,
		CreatedBy:             g.CreatedBy,
		CreatedAt:             g.CreatedAt,
		UpdatedAt:             g.UpdatedAt,
		Bundles:               bundles,
	}
}

// CollectGlobs 返回仓库级采集白名单覆盖配置（未配置则返回 nil，表示使用全局默认）
func (g GitRepo) CollectGlobs() []string {
	return decodeStringSliceJSON(g.CollectGlobsJSON, nil)
}

// ExcludeGlobs 返回仓库级排除规则覆盖配置（未配置则返回 nil，表示使用全局默认）
func (g GitRepo) ExcludeGlobs() []string {
	return decodeStringSliceJSON(g.ExcludeGlobsJSON, nil)
}

// StripPrefixes 返回仓库级导出相对路径剥离前缀列表（ADR-27 §2.3）
func (g GitRepo) StripPrefixes() []string {
	return decodeStringSliceJSON(g.StripPrefixesJSON, nil)
}

// PathRewrites 返回仓库级精确路径前缀重映射表（ADR-27 §2.3）
func (g GitRepo) PathRewrites() map[string]string {
	if len(g.PathRewritesJSON) == 0 || string(g.PathRewritesJSON) == "null" {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal(g.PathRewritesJSON, &out); err != nil || len(out) == 0 {
		return nil
	}
	return out
}

// GitProtoBundle 实体 #21：Monorepo 多独立 Proto 子目录 / 编译打包单元 (1:N 归属 GitRepo)
type GitProtoBundle struct {
	ID                      uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	GitRepoID               uint64         `gorm:"not null;uniqueIndex:uk_contract_git_bundle_repo_name;index:idx_contract_git_bundle_repo" json:"git_repo_id"`
	BundleName              string         `gorm:"size:128;not null;uniqueIndex:uk_contract_git_bundle_repo_name" json:"bundle_name"`
	TargetSchemaName        string         `gorm:"size:128;not null;uniqueIndex:uk_contract_git_bundle_target" json:"target_schema_name"`
	Category                string         `gorm:"size:32;not null;default:'BUSINESS'" json:"category"` // BASE_LIB | BUSINESS | GATEWAY
	SubdirPath              string         `gorm:"size:512;not null;default:'.'" json:"subdir_path"`
	IncludeDirsJSON         datatypes.JSON `gorm:"column:include_dirs_json;type:jsonb;not null;default:'[\".\"]'" json:"include_dirs_json"`
	EntryFilesJSON          datatypes.JSON `gorm:"column:entry_files_json;type:jsonb;not null;default:'[]'" json:"entry_files_json"`
	FileGlobsJSON           datatypes.JSON `gorm:"column:file_globs_json;type:jsonb;not null;default:'[\"*.proto\"]'" json:"file_globs_json"`
	ExcludeGlobsJSON        datatypes.JSON `gorm:"column:exclude_globs_json;type:jsonb;not null;default:'[]'" json:"exclude_globs_json"`
	AutoCompile             bool           `gorm:"not null;default:true" json:"auto_compile"`
	AutoActivate            bool           `gorm:"not null;default:false" json:"auto_activate"`
	MRAutoPublish           string         `gorm:"column:mr_auto_publish;size:32;not null;default:'DISABLED'" json:"mr_auto_publish"` // DISABLED | DRAFT_ONLY | AUTO_ACTIVATE
	LintOnSync              bool           `gorm:"not null;default:true" json:"lint_on_sync"`
	LastSyncCommit          string         `gorm:"size:64;not null;default:''" json:"last_sync_commit"`
	LastSyncAt              *time.Time     `json:"last_sync_at,omitempty"`
	LastSchemaID            *uint64        `json:"last_schema_id,omitempty"`
	LastPublishStatus       string         `gorm:"column:last_publish_status;size:32;not null;default:'PENDING'" json:"last_publish_status"`
	LastPublishRunID        *uint64        `gorm:"column:last_publish_run_id" json:"last_publish_run_id,omitempty"`
	PendingBreakingSchemaID *uint64        `gorm:"column:pending_breaking_schema_id" json:"pending_breaking_schema_id,omitempty"`
	Description             string         `gorm:"type:text;not null;default:''" json:"description"`
	CreatedAt               time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt               time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
}

func (GitProtoBundle) TableName() string {
	return config.TablePrefix + "git_proto_bundles"
}

func decodeStringSliceJSON(raw datatypes.JSON, fallback []string) []string {
	if len(raw) == 0 {
		return fallback
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil || len(out) == 0 {
		return fallback
	}
	return out
}

// IncludeDirs 返回 Bundle 配置的编译 include 搜索目录（POSIX 相对路径）
func (b GitProtoBundle) IncludeDirs() []string {
	return decodeStringSliceJSON(b.IncludeDirsJSON, []string{"."})
}

// EntryFiles 返回显式指定的入口 .proto 文件列表
func (b GitProtoBundle) EntryFiles() []string {
	return decodeStringSliceJSON(b.EntryFilesJSON, []string{})
}

// FileGlobs 返回包含匹配 glob 模式
func (b GitProtoBundle) FileGlobs() []string {
	return decodeStringSliceJSON(b.FileGlobsJSON, []string{"*.proto"})
}

// ExcludeGlobs 返回排除匹配 glob 模式
func (b GitProtoBundle) ExcludeGlobs() []string {
	return decodeStringSliceJSON(b.ExcludeGlobsJSON, []string{})
}

// ProtoFile 实体 #22：Git 同步后的只读 Proto 源文件实体（严禁任何悲观写锁或 Web 编辑字段）
type ProtoFile struct {
	ID                uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	GitRepoID         uint64         `gorm:"not null;index:idx_contract_pfile_repo" json:"git_repo_id"`
	BundleID          uint64         `gorm:"not null;index:idx_contract_pfile_bundle" json:"bundle_id"`
	TargetSchemaName  string         `gorm:"size:128;not null;index:idx_contract_pfile_target_schema" json:"target_schema_name"`
	Category          string         `gorm:"size:32;not null;default:'BUSINESS'" json:"category"`                      // BASE_LIB | BUSINESS | GATEWAY
	FilePath          string         `gorm:"size:512;not null" json:"file_path"`                                       // 逻辑导入路径（由 Stage C/C+ 填充，统一去 /pg 坐标系）
	RepoFilePath      string         `gorm:"size:768;not null;default:''" json:"repo_file_path"`                       // 相对于 Git 仓库根目录的 POSIX 路径（raw/ 保留）
	CleanRelPath      string         `gorm:"column:clean_rel_path;size:512;not null;default:''" json:"clean_rel_path"` // clean/ 仓级导出相对路径（默认去 /pg）
	PackageName       string         `gorm:"size:255;not null;default:''" json:"package_name"`
	RawBlobSHA        string         `gorm:"column:raw_blob_sha;size:64;not null;default:''" json:"raw_blob_sha"`
	NormalizerVersion string         `gorm:"column:normalizer_version;size:64;not null;default:''" json:"normalizer_version"`
	PipelineStatus    string         `gorm:"column:pipeline_status;size:24;not null;default:'NORMALIZED';index:idx_contract_pfile_pipeline_status" json:"pipeline_status"` // COLLECTED | NORMALIZED | QUARANTINED
	StatusReason      string         `gorm:"column:status_reason;type:text;not null;default:''" json:"status_reason"`
	LineMapJSON       datatypes.JSON `gorm:"column:line_map_json;type:jsonb" json:"line_map_json,omitempty"`
	IsExternalCopy    bool           `gorm:"column:is_external_copy;not null;default:false" json:"is_external_copy"`
	IsUServiceSource  bool           `gorm:"column:is_uservice_source;not null;default:false" json:"is_uservice_source"`
	InServiceClosure  bool           `gorm:"column:in_service_closure;not null;default:false" json:"in_service_closure"`
	IsHarvestedBase   bool           `gorm:"column:is_harvested_base;not null;default:false" json:"is_harvested_base"`
	AutoFixCount      int            `gorm:"column:auto_fix_count;not null;default:0" json:"auto_fix_count"`
	ImportsJSON       datatypes.JSON `gorm:"column:imports_json;type:jsonb;not null;default:'[]'" json:"imports_json"`
	SymbolsJSON       datatypes.JSON `gorm:"column:symbols_json;type:jsonb;not null;default:'[]'" json:"symbols_json"`
	ContentSHA256     string         `gorm:"column:content_sha256;size:64;not null;default:''" json:"content_sha256"` // 清理后内容哈希
	LineCount         int            `gorm:"not null;default:0" json:"line_count"`
	LatestVersionID   *uint64        `gorm:"index:idx_contract_pfile_latest_ver" json:"latest_version_id,omitempty"`
	LatestVersionNum  int            `gorm:"not null;default:0" json:"latest_version"`
	LatestCommitSHA   string         `gorm:"size:64;not null;default:''" json:"latest_commit_sha"`
	IsDeleted         bool           `gorm:"not null;default:false;index:idx_contract_pfile_deleted" json:"is_deleted"`
	CreatedAt         time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt         time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
}

func (ProtoFile) TableName() string {
	return config.TablePrefix + "proto_files"
}

// ProtoFileVersion 实体 #23：只读 Proto 源文件的 Commit 不可变历史快照表
type ProtoFileVersion struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ProtoFileID    uint64    `gorm:"not null;uniqueIndex:uk_contract_pfile_ver_num;index:idx_contract_pfv_file" json:"proto_file_id"`
	Version        int       `gorm:"not null;uniqueIndex:uk_contract_pfile_ver_num" json:"version"`
	VersionTag     string    `gorm:"size:128;not null;default:''" json:"version_tag"`
	SourceType     string    `gorm:"size:32;not null;default:'GIT_SYNC'" json:"source_type"` // GIT_SYNC | GIT_MR_AUTO
	CommitSHA      string    `gorm:"size:64;not null;default:'';index:idx_contract_pfv_commit" json:"commit_sha"`
	CommitMessage  string    `gorm:"type:text;not null;default:''" json:"commit_message"`
	AuthorName     string    `gorm:"size:128;not null;default:''" json:"author_name"`
	AuthorEmail    string    `gorm:"size:128;not null;default:''" json:"author_email"`
	RawContentText string    `gorm:"column:raw_content_text;type:text;not null;default:''" json:"-"` // Stage A 原始源码
	ContentText    string    `gorm:"type:text;not null" json:"-"`                                    // 清理后只读源码全文
	ContentSHA256  string    `gorm:"column:content_sha256;size:64;not null" json:"content_sha256"`
	LineCount      int       `gorm:"not null;default:0" json:"line_count"`
	SchemaID       *uint64   `gorm:"index:idx_contract_pfv_schema" json:"schema_id,omitempty"`
	CommittedAt    time.Time `gorm:"not null" json:"committed_at"`
	CreatedAt      time.Time `gorm:"autoCreateTime" json:"created_at"`
}

func (ProtoFileVersion) TableName() string {
	return config.TablePrefix + "proto_file_versions"
}

// ProtoImportOwner 实体 #37：跨仓逻辑导入路径权威提供方裁决表 (ADR-26 §2.6)
type ProtoImportOwner struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	LogicalPath    string    `gorm:"column:logical_path;size:512;not null;uniqueIndex:uk_contract_import_owner_path" json:"logical_path"`
	OwnerGitRepoID uint64    `gorm:"column:owner_git_repo_id;not null;index:idx_contract_import_owner_repo" json:"owner_git_repo_id"`
	OwnerBundleID  uint64    `gorm:"column:owner_bundle_id;not null;index:idx_contract_import_owner_bundle" json:"owner_bundle_id"`
	Source         string    `gorm:"column:source;size:24;not null;default:'AUTO'" json:"source"` // MANUAL | BASE_LIB | AUTO
	UpdatedBy      string    `gorm:"column:updated_by;size:128;not null;default:''" json:"updated_by"`
	CreatedAt      time.Time `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt      time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

func (ProtoImportOwner) TableName() string {
	return config.TablePrefix + "proto_import_owners"
}

// ProtoSyncJob 实体 #38：500+ 平台仓持久化异步同步批处理任务表 (ADR-27 §2.2)
type ProtoSyncJob struct {
	ID                    uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	Scope                 string         `gorm:"size:24;not null;default:'ALL'" json:"scope"`               // ALL | DEPARTMENT | SERVICE_GROUP | FAILED_ONLY | SELECTED
	CachePolicy           string         `gorm:"size:24;not null;default:'CACHE_ONLY'" json:"cache_policy"` // CACHE_ONLY | FETCH_MISSING
	FilterJSON            datatypes.JSON `gorm:"column:filter_json;type:jsonb;not null;default:'{}'" json:"filter_json"`
	Status                string         `gorm:"size:24;not null;default:'RUNNING';index:idx_contract_psync_job_status" json:"status"` // RUNNING | COMPLETED | PARTIAL_FAILED | FAILED
	TotalRepos            int            `gorm:"not null;default:0" json:"total_repos"`
	ProcessedRepos        int            `gorm:"not null;default:0" json:"processed_repos"`
	HasProtoCount         int            `gorm:"not null;default:0" json:"has_proto_count"`
	NoProtoCount          int            `gorm:"not null;default:0" json:"no_proto_count"`
	NoCacheCount          int            `gorm:"not null;default:0" json:"no_cache_count"`
	FailedCount           int            `gorm:"not null;default:0" json:"failed_count"`
	SkippedUnchangedCount int            `gorm:"not null;default:0" json:"skipped_unchanged_count"`
	ErrorSummaryJSON      datatypes.JSON `gorm:"column:error_summary_json;type:jsonb;not null;default:'[]'" json:"error_summary_json"`
	TriggeredBy           string         `gorm:"size:128;not null;default:''" json:"triggered_by"`
	StartedAt             time.Time      `gorm:"not null" json:"started_at"`
	FinishedAt            *time.Time     `json:"finished_at,omitempty"`
	CreatedAt             time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt             time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
}

func (ProtoSyncJob) TableName() string {
	return config.TablePrefix + "proto_sync_jobs"
}

// ProtoGovernanceConfig 实体 #39：辅助 .json 治理配置版本快照表 (ADR-27 §2.4)
type ProtoGovernanceConfig struct {
	ID           uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	Revision     int            `gorm:"not null;default:1;index:idx_contract_pgov_rev" json:"revision"`
	ConfigSHA256 string         `gorm:"column:config_sha256;size:64;not null" json:"config_sha256"`
	ConfigJSON   datatypes.JSON `gorm:"column:config_json;type:jsonb;not null;default:'{}'" json:"config_json"`
	IsActive     bool           `gorm:"not null;default:true;index:idx_contract_pgov_active" json:"is_active"`
	UpdatedBy    string         `gorm:"size:128;not null;default:''" json:"updated_by"`
	CreatedAt    time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
}

func (ProtoGovernanceConfig) TableName() string {
	return config.TablePrefix + "proto_governance_configs"
}

// ProtoPoolReconcileRun 实体 #40：全池 Stage C+ 编译收敛与 workspace/ 导出运行记录表 (ADR-28 §4)
type ProtoPoolReconcileRun struct {
	ID                      uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	Status                  string         `gorm:"size:24;not null;default:'RUNNING';index:idx_contract_pool_reconcile_status" json:"status"` // RUNNING | COMPLETED | FAILED
	TriggeredBy             string         `gorm:"size:128;not null;default:''" json:"triggered_by"`
	ConfigRevision          int            `gorm:"not null;default:0" json:"config_revision"`
	ConfigSHA256            string         `gorm:"column:config_sha256;size:64;not null;default:''" json:"config_sha256"`
	StripPGSegment          bool           `gorm:"column:strip_pg_segment;not null;default:true" json:"strip_pg_segment"`
	ClosureOnly             bool           `gorm:"column:closure_only;not null;default:true" json:"closure_only"`
	TotalFiles              int            `gorm:"not null;default:0" json:"total_files"`
	ServiceCount            int            `gorm:"not null;default:0" json:"service_count"`
	ClosureFileCount        int            `gorm:"not null;default:0" json:"closure_file_count"`
	HarvestedBaseCount      int            `gorm:"not null;default:0" json:"harvested_base_count"`
	LogicalPathCount        int            `gorm:"not null;default:0" json:"logical_path_count"`
	ConflictCount           int            `gorm:"not null;default:0" json:"conflict_count"`
	AutoFixCount            int            `gorm:"not null;default:0" json:"auto_fix_count"`
	QuarantinedCount        int            `gorm:"not null;default:0" json:"quarantined_count"`
	BareImportExpandedCount int            `gorm:"not null;default:0" json:"bare_import_expanded_count"`
	PostStripCollisionCount int            `gorm:"not null;default:0" json:"post_strip_collision_count"`
	WorkspaceVersion        string         `gorm:"size:128;not null;default:''" json:"workspace_version"`
	InputFingerprint        string         `gorm:"column:input_fingerprint;size:64;not null;default:''" json:"input_fingerprint"`
	ReportJSON              datatypes.JSON `gorm:"column:report_json;type:jsonb;not null;default:'{}'" json:"report_json"`
	ErrorMessage            string         `gorm:"type:text;not null;default:''" json:"error_message"`
	DurationMs              int64          `gorm:"not null;default:0" json:"duration_ms"`
	StartedAt               time.Time      `gorm:"not null" json:"started_at"`
	FinishedAt              *time.Time     `json:"finished_at,omitempty"`
	CreatedAt               time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt               time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
}

func (ProtoPoolReconcileRun) TableName() string {
	return config.TablePrefix + "proto_pool_reconcile_runs"
}

// ProtoPublishRun 实体 #41：阶段 ④ 单仓拓扑编译与分级发布运行记录表 (ADR-29 §4.1)
type ProtoPublishRun struct {
	ID                   uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	PoolReconcileRunID   uint64         `gorm:"column:pool_reconcile_run_id;not null;index:idx_contract_publish_reconcile" json:"pool_reconcile_run_id"`
	PublishMode          string         `gorm:"column:publish_mode;size:16;not null;default:'AUTO'" json:"publish_mode"`
	Status               string         `gorm:"size:24;not null;default:'RUNNING';index:idx_contract_publish_status" json:"status"`
	TotalBundles         int            `gorm:"not null;default:0" json:"total_bundles"`
	PublishedCount       int            `gorm:"not null;default:0" json:"published_count"`
	SkippedCount         int            `gorm:"not null;default:0" json:"skipped_count"`
	BreakingBlockedCount int            `gorm:"not null;default:0" json:"breaking_blocked_count"`
	ConflictBlockedCount int            `gorm:"not null;default:0" json:"conflict_blocked_count"`
	PendingUpstreamCount int            `gorm:"not null;default:0" json:"pending_upstream_count"`
	FailedCount          int            `gorm:"not null;default:0" json:"failed_count"`
	FanoutCount          int            `gorm:"not null;default:0" json:"fanout_count"`
	DetailsJSON          datatypes.JSON `gorm:"column:details_json;type:jsonb;not null;default:'{}'" json:"details_json"`
	ErrorMessage         string         `gorm:"type:text;not null;default:''" json:"error_message"`
	TriggeredBy          string         `gorm:"size:128;not null;default:''" json:"triggered_by"`
	DurationMs           int64          `gorm:"not null;default:0" json:"duration_ms"`
	StartedAt            time.Time      `gorm:"not null" json:"started_at"`
	FinishedAt           *time.Time     `json:"finished_at,omitempty"`
	CreatedAt            time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt            time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
}

func (ProtoPublishRun) TableName() string {
	return config.TablePrefix + "proto_publish_runs"
}

// ProtoPipelineRun 实体 #42：一键四阶段 Proto 流水线编排任务表 (ADR-29 §4.1)
type ProtoPipelineRun struct {
	ID             uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	FromStage      string     `gorm:"column:from_stage;size:24;not null;default:'COLLECT_NORMALIZE'" json:"from_stage"`
	CurrentStage   string     `gorm:"column:current_stage;size:24;not null;default:'COLLECT_NORMALIZE'" json:"current_stage"`
	Status         string     `gorm:"size:24;not null;default:'RUNNING';index:idx_contract_pipeline_status" json:"status"`
	TriggerSource  string     `gorm:"column:trigger_source;size:24;not null;default:'MANUAL'" json:"trigger_source"`
	SyncJobID      *uint64    `gorm:"column:sync_job_id" json:"sync_job_id,omitempty"`
	ReconcileRunID *uint64    `gorm:"column:reconcile_run_id" json:"reconcile_run_id,omitempty"`
	PublishRunID   *uint64    `gorm:"column:publish_run_id" json:"publish_run_id,omitempty"`
	ErrorMessage   string     `gorm:"type:text;not null;default:''" json:"error_message"`
	TriggeredBy    string     `gorm:"size:128;not null;default:''" json:"triggered_by"`
	StartedAt      time.Time  `gorm:"not null" json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	CreatedAt      time.Time  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (ProtoPipelineRun) TableName() string {
	return config.TablePrefix + "proto_pipeline_runs"
}

// PreMigrateADR26R4Orphans 在 AutoMigrate 收口 platform_repo_id NOT NULL 前归并存量手工仓与清理孤儿记录。
func PreMigrateADR26R4Orphans(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&GitRepo{}) {
		return nil
	}
	// 先放宽旧版 source_mode CHECK 约束，避免升级写入 PLATFORM_SHARED 冲突
	_ = db.Exec(`ALTER TABLE contract_git_repos DROP CONSTRAINT IF EXISTS chk_contract_git_repo_source_mode;`).Error
	_ = db.Exec(`DROP INDEX IF EXISTS uk_contract_git_repo_platform;`).Error

	return migrateLegacyGitReposToPlatformShared(db)
}

func migrateLegacyGitReposToPlatformShared(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&GitRepo{}) {
		return nil
	}

	type legacyRepoRow struct {
		ID             uint64
		PlatformRepoID *uint64
		Name           string
		RepoURL        string
		DefaultBranch  string
		SourceMode     string
	}
	var legacyRows []legacyRepoRow
	if err := db.Table("contract_git_repos").
		Select("id, platform_repo_id, name, repo_url, default_branch, source_mode").
		Where("platform_repo_id IS NULL OR platform_repo_id = 0").
		Order("id ASC").
		Scan(&legacyRows).Error; err != nil {
		return err
	}

	if len(legacyRows) > 0 {
		// 确保备份表存在以归档无法匹配的孤儿仓摘要
		if !db.Migrator().HasTable(&MigBackup{}) {
			if err := db.AutoMigrate(&MigBackup{}); err != nil {
				return err
			}
		}

		var plats []Repository
		if db.Migrator().HasTable(&Repository{}) {
			_ = db.Order("id ASC").Find(&plats).Error
		}
		platByNormURL := make(map[string]Repository, len(plats)*2)
		for _, p := range plats {
			if u := gitsync.NormalizeGitURL(p.URL); u != "" {
				if _, ok := platByNormURL[u]; !ok {
					platByNormURL[u] = p
				}
			}
			if hu := gitsync.NormalizeGitURL(p.HTTPURL); hu != "" {
				if _, ok := platByNormURL[hu]; !ok {
					platByNormURL[hu] = p
				}
			}
		}

		var existingPlatIDs []uint64
		_ = db.Table("contract_git_repos").
			Where("platform_repo_id IS NOT NULL AND platform_repo_id > 0").
			Pluck("platform_repo_id", &existingPlatIDs).Error
		usedPlatIDs := make(map[uint64]struct{}, len(existingPlatIDs))
		for _, pid := range existingPlatIDs {
			usedPlatIDs[pid] = struct{}{}
		}

		for _, lr := range legacyRows {
			normURL := gitsync.NormalizeGitURL(lr.RepoURL)
			matched, ok := platByNormURL[normURL]
			if ok && matched.ID > 0 {
				if _, taken := usedPlatIDs[uint64(matched.ID)]; !taken {
					usedPlatIDs[uint64(matched.ID)] = struct{}{}
					effectiveURL := strings.TrimSpace(matched.URL)
					if effectiveURL == "" {
						effectiveURL = strings.TrimSpace(matched.HTTPURL)
					}
					if err := db.Table("contract_git_repos").Where("id = ?", lr.ID).Updates(map[string]any{
						"platform_repo_id":   uint64(matched.ID),
						"source_mode":        GitSourceModePlatformShared,
						"repo_url":           effectiveURL,
						"default_branch":     gitsync.ResolveBranchDefault(matched.Branch),
						"auth_type":          GitAuthNone,
						"credentials_enc":    "",
						"credential_hint":    "",
						"webhook_secret_enc": "",
					}).Error; err != nil {
						return err
					}
					continue
				}
			}

			// 无法匹配或已被占用：备份摘要至 contract_mig_backups 并安全清理孤儿手工仓（保全历史 ProtoSchema）
			backupBytes, _ := json.Marshal(map[string]any{
				"legacy_repo_id": lr.ID,
				"name":           lr.Name,
				"repo_url":       lr.RepoURL,
				"default_branch": lr.DefaultBranch,
				"source_mode":    lr.SourceMode,
				"reason":         "unmatched_or_duplicate_legacy_standalone_repo",
			})
			if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&MigBackup{
				MigrationKey:  "ADR26_R4_ORPHAN_GIT_REPO",
				Entity:        "contract_git_repos",
				EntityID:      lr.ID,
				Payload:       datatypes.JSON(backupBytes),
				MigratedState: "APPLIED",
			}).Error; err != nil {
				return err
			}
			if err := db.Transaction(func(tx *gorm.DB) error {
				if tx.Migrator().HasTable(&ProtoSchema{}) {
					if err := tx.Exec(`UPDATE contract_proto_schemas SET git_repo_id = NULL, source_bundle_id = NULL WHERE git_repo_id = ?`, lr.ID).Error; err != nil {
						return err
					}
				}
				if tx.Migrator().HasTable(&ProtoFile{}) {
					if err := tx.Exec(`UPDATE contract_proto_files SET latest_version_id = NULL WHERE git_repo_id = ?`, lr.ID).Error; err != nil {
						return err
					}
				}
				if tx.Migrator().HasTable(&ProtoFileVersion{}) && tx.Migrator().HasTable(&ProtoFile{}) {
					if err := tx.Exec(`DELETE FROM contract_proto_file_versions WHERE proto_file_id IN (SELECT id FROM contract_proto_files WHERE git_repo_id = ?)`, lr.ID).Error; err != nil {
						return err
					}
				}
				if tx.Migrator().HasTable(&ProtoFile{}) {
					if err := tx.Exec(`DELETE FROM contract_proto_files WHERE git_repo_id = ?`, lr.ID).Error; err != nil {
						return err
					}
				}
				if tx.Migrator().HasTable(&GitProtoBundle{}) {
					if err := tx.Exec(`DELETE FROM contract_git_proto_bundles WHERE git_repo_id = ?`, lr.ID).Error; err != nil {
						return err
					}
				}
				return tx.Exec(`DELETE FROM contract_git_repos WHERE id = ?`, lr.ID).Error
			}); err != nil {
				return fmt.Errorf("归档清理孤儿 GitRepo #%d 失败: %w", lr.ID, err)
			}
		}
	}

	// 回填并清理所有历史独立模式下留存的凭据（r4 唯一真值源收口）
	_ = db.Exec(`UPDATE contract_git_repos SET source_mode = 'PLATFORM_SHARED', auth_type = 'NONE', credentials_enc = '', credential_hint = '' WHERE source_mode <> 'PLATFORM_SHARED' OR source_mode = '' OR source_mode IS NULL;`).Error
	return nil
}

// ensureADR26SchemaAndBackfill 幂等收口 ADR-26 (r4) & ADR-27 约束、索引与存量数据回填。
func ensureADR26SchemaAndBackfill(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if err := migrateLegacyGitReposToPlatformShared(db); err != nil {
		return err
	}

	stmts := []string{
		`ALTER TABLE contract_git_repos ALTER COLUMN platform_repo_id SET NOT NULL;`,
		`DROP INDEX IF EXISTS uk_contract_git_repo_platform;`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_git_repo_platform ON contract_git_repos (platform_repo_id);`,
		`ALTER TABLE contract_git_repos DROP CONSTRAINT IF EXISTS chk_contract_git_repo_source_mode;`,
		`ALTER TABLE contract_git_repos DROP CONSTRAINT IF EXISTS chk_contract_git_repo_platform_id;`,
		`ALTER TABLE contract_git_repos DROP CONSTRAINT IF EXISTS chk_contract_git_repo_proto_status;`,
		`ALTER TABLE contract_proto_import_owners DROP CONSTRAINT IF EXISTS chk_contract_import_owner_source;`,
		`ALTER TABLE contract_proto_schemas DROP CONSTRAINT IF EXISTS chk_contract_proto_source_type;`,
		`DO $$
BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_git_repo_source_mode') THEN
		ALTER TABLE contract_git_repos ADD CONSTRAINT chk_contract_git_repo_source_mode CHECK (source_mode = 'PLATFORM_SHARED');
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_git_repo_platform_id') THEN
		ALTER TABLE contract_git_repos ADD CONSTRAINT chk_contract_git_repo_platform_id CHECK (platform_repo_id > 0);
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_git_repo_sync_strategy') THEN
		ALTER TABLE contract_git_repos ADD CONSTRAINT chk_contract_git_repo_sync_strategy CHECK (sync_strategy IN ('FETCH_AND_READ', 'LOCAL_CACHE_ONLY'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_git_repo_proto_status') THEN
		ALTER TABLE contract_git_repos ADD CONSTRAINT chk_contract_git_repo_proto_status CHECK (proto_status IN ('PENDING', 'HAS_PROTO', 'NO_PROTO', 'NO_LOCAL_CACHE', 'EXCLUDED', 'SYNC_FAILED'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_pfile_pipeline_status') THEN
		ALTER TABLE contract_proto_files ADD CONSTRAINT chk_contract_pfile_pipeline_status CHECK (pipeline_status IN ('COLLECTED', 'NORMALIZED', 'QUARANTINED'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_import_owner_source') THEN
		ALTER TABLE contract_proto_import_owners ADD CONSTRAINT chk_contract_import_owner_source CHECK (source IN ('MANUAL', 'BASE_LIB', 'AUTO', 'R3_AUTO'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proto_source_type') THEN
		ALTER TABLE contract_proto_schemas ADD CONSTRAINT chk_contract_proto_source_type CHECK (source_type IN ('UPLOAD_PB', 'ONLINE_COMPILE', 'GIT_SYNC', 'GIT_MR_AUTO', 'GIT_PIPELINE'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proto_sync_job_scope') THEN
		ALTER TABLE contract_proto_sync_jobs ADD CONSTRAINT chk_contract_proto_sync_job_scope CHECK (scope IN ('ALL', 'DEPARTMENT', 'SERVICE_GROUP', 'FAILED_ONLY', 'SELECTED'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proto_sync_job_policy') THEN
		ALTER TABLE contract_proto_sync_jobs ADD CONSTRAINT chk_contract_proto_sync_job_policy CHECK (cache_policy IN ('CACHE_ONLY', 'FETCH_MISSING'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proto_sync_job_status') THEN
		ALTER TABLE contract_proto_sync_jobs ADD CONSTRAINT chk_contract_proto_sync_job_status CHECK (status IN ('RUNNING', 'COMPLETED', 'PARTIAL_FAILED', 'FAILED'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_pool_reconcile_status') THEN
		ALTER TABLE contract_proto_pool_reconcile_runs ADD CONSTRAINT chk_contract_pool_reconcile_status CHECK (status IN ('RUNNING', 'COMPLETED', 'FAILED'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_git_bundle_publish_status') THEN
		ALTER TABLE contract_git_proto_bundles ADD CONSTRAINT chk_contract_git_bundle_publish_status CHECK (last_publish_status IN ('PENDING', 'PUBLISHED', 'SKIPPED_UNCHANGED', 'BLOCKED_BREAKING', 'BLOCKED_CONFLICT', 'PENDING_UPSTREAM', 'FAILED'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proto_publish_mode') THEN
		ALTER TABLE contract_proto_publish_runs ADD CONSTRAINT chk_contract_proto_publish_mode CHECK (publish_mode IN ('AUTO', 'PREVIEW'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proto_publish_status') THEN
		ALTER TABLE contract_proto_publish_runs ADD CONSTRAINT chk_contract_proto_publish_status CHECK (status IN ('RUNNING', 'COMPLETED', 'PREVIEW_READY', 'PARTIAL_BLOCKED', 'FAILED', 'REJECTED_STALE'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proto_pipeline_from_stage') THEN
		ALTER TABLE contract_proto_pipeline_runs ADD CONSTRAINT chk_contract_proto_pipeline_from_stage CHECK (from_stage IN ('COLLECT_NORMALIZE', 'RECONCILE', 'PUBLISH'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proto_pipeline_status') THEN
		ALTER TABLE contract_proto_pipeline_runs ADD CONSTRAINT chk_contract_proto_pipeline_status CHECK (status IN ('RUNNING', 'COMPLETED', 'PARTIAL_BLOCKED', 'FAILED'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proto_pipeline_trigger') THEN
		ALTER TABLE contract_proto_pipeline_runs ADD CONSTRAINT chk_contract_proto_pipeline_trigger CHECK (trigger_source IN ('MANUAL', 'MR_MERGE', 'SCHEDULE'));
	END IF;
END $$;`,
		`CREATE INDEX IF NOT EXISTS idx_contract_pfile_closure ON contract_proto_files (in_service_closure, is_deleted);`,
		`UPDATE contract_git_repos SET source_mode = 'PLATFORM_SHARED' WHERE source_mode <> 'PLATFORM_SHARED' OR source_mode = '' OR source_mode IS NULL;`,
		`UPDATE contract_git_repos SET sync_strategy = 'LOCAL_CACHE_ONLY' WHERE sync_strategy = '' OR sync_strategy IS NULL;`,
		`UPDATE contract_git_repos SET proto_status = 'HAS_PROTO' WHERE (proto_status = '' OR proto_status IS NULL OR proto_status = 'PENDING') AND EXISTS (SELECT 1 FROM contract_proto_files f WHERE f.git_repo_id = contract_git_repos.id AND f.is_deleted = false);`,
		`UPDATE contract_git_repos SET proto_status = 'PENDING' WHERE proto_status = '' OR proto_status IS NULL;`,
		`UPDATE contract_proto_files SET pipeline_status = 'NORMALIZED' WHERE pipeline_status = '' OR pipeline_status IS NULL;`,
		`UPDATE contract_proto_files SET normalizer_version = 'passthrough-0' WHERE normalizer_version = '' OR normalizer_version IS NULL;`,
		`UPDATE contract_proto_files SET raw_blob_sha = content_sha256 WHERE (raw_blob_sha = '' OR raw_blob_sha IS NULL) AND content_sha256 <> '';`,
		`UPDATE contract_git_proto_bundles SET last_publish_status = 'PENDING' WHERE last_publish_status = '' OR last_publish_status IS NULL;`,
	}
	for _, stmt := range stmts {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}
