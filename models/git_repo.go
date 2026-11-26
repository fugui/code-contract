package models

import (
	"encoding/json"
	"strings"
	"time"

	"code-contract/config"
	"code-contract/internal/server/security"

	"gorm.io/datatypes"
	"gorm.io/gorm"
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

	// ADR-26: 平台代码仓共享模式、同步策略、流水线状态与导入归属来源
	GitSourceModePlatformShared     = "PLATFORM_SHARED"
	GitSourceModeExternalStandalone = "EXTERNAL_STANDALONE"

	GitSyncStrategyFetchAndRead   = "FETCH_AND_READ"
	GitSyncStrategyLocalCacheOnly = "LOCAL_CACHE_ONLY"

	ProtoPipelineCollected   = "COLLECTED"
	ProtoPipelineNormalized  = "NORMALIZED"
	ProtoPipelineQuarantined = "QUARANTINED"

	ImportOwnerSourceManual  = "MANUAL"
	ImportOwnerSourceBaseLib = "BASE_LIB"
	ImportOwnerSourceAuto    = "AUTO"
)

// GitRepo 实体 #20：Git 契约仓库连接与鉴权配置表（SSOT 控制面元数据）
type GitRepo struct {
	ID                  uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	PlatformRepoID      *uint64        `gorm:"column:platform_repo_id" json:"platform_repo_id,omitempty"` // 关联公共 repositories.id（可空唯一部分索引）
	SourceMode          string         `gorm:"column:source_mode;size:32;not null;default:'EXTERNAL_STANDALONE'" json:"source_mode"`
	SyncStrategy        string         `gorm:"column:sync_strategy;size:32;not null;default:'FETCH_AND_READ'" json:"sync_strategy"`
	SubsystemSlug       string         `gorm:"column:subsystem_slug;size:128;not null;default:''" json:"subsystem_slug"`
	ResolvedBranch      string         `gorm:"column:resolved_branch;size:128;not null;default:''" json:"resolved_branch"`
	CollectGlobsJSON    datatypes.JSON `gorm:"column:collect_globs_json;type:jsonb" json:"collect_globs_json,omitempty"`
	ExcludeGlobsJSON    datatypes.JSON `gorm:"column:exclude_globs_json;type:jsonb" json:"exclude_globs_json,omitempty"`
	LastFetchedAt       *time.Time     `gorm:"column:last_fetched_at" json:"last_fetched_at,omitempty"`
	RefCommitTime       *time.Time     `gorm:"column:ref_commit_time" json:"ref_commit_time,omitempty"`
	LastCollectedCommit string         `gorm:"column:last_collected_commit;size:64;not null;default:''" json:"last_collected_commit"`
	Name                string         `gorm:"size:128;not null;uniqueIndex:uk_contract_git_repo_name" json:"name"`
	Provider            string         `gorm:"size:32;not null;default:'GITLAB'" json:"provider"` // GITLAB | GITHUB | GITEA | GENERIC
	RepoURL             string         `gorm:"size:512;not null" json:"repo_url"`
	APIBaseURL          string         `gorm:"size:512;not null;default:''" json:"api_base_url"`
	DefaultBranch       string         `gorm:"size:128;not null;default:'main'" json:"default_branch"`
	AuthType            string         `gorm:"size:32;not null;default:'TOKEN'" json:"auth_type"` // NONE | TOKEN | SSH_KEY
	CredentialsEnc      string         `gorm:"type:text;not null;default:''" json:"-"`            // AES-256-GCM 密文，严禁序列化泄漏
	CredentialHint      string         `gorm:"size:64;not null;default:''" json:"credential_hint"`
	WebhookSecretEnc    string         `gorm:"type:text;not null;default:''" json:"-"`             // AES-256-GCM 密文，严禁序列化泄漏
	SyncMode            string         `gorm:"size:32;not null;default:'MANUAL'" json:"sync_mode"` // WEBHOOK | POLL | MANUAL
	PollIntervalSec     int            `gorm:"not null;default:300" json:"poll_interval_sec"`
	AutoCompile         bool           `gorm:"not null;default:true" json:"auto_compile"`
	AutoActivate        bool           `gorm:"not null;default:false" json:"auto_activate"`
	LintOnSync          bool           `gorm:"not null;default:true" json:"lint_on_sync"`
	LastSyncAt          *time.Time     `json:"last_sync_at,omitempty"`
	LastSyncCommit      string         `gorm:"size:64;not null;default:''" json:"last_sync_commit"`
	LastSyncStatus      string         `gorm:"size:32;not null;default:'IDLE'" json:"last_sync_status"` // IDLE | RUNNING | SUCCESS | FAILED
	LastSyncError       string         `gorm:"type:text;not null;default:''" json:"last_sync_error"`
	Description         string         `gorm:"type:text;not null;default:''" json:"description"`
	CreatedBy           uint           `gorm:"not null;default:0" json:"created_by"`
	CreatedAt           time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt           time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
}

func (GitRepo) TableName() string {
	return config.TablePrefix + "git_repos"
}

// GitRepoRedactedDTO 脱敏后的 GitRepo 接口输出模型（绝不返回 CredentialsEnc / WebhookSecretEnc / 明文）
type GitRepoRedactedDTO struct {
	ID                  uint64           `json:"id"`
	PlatformRepoID      *uint64          `json:"platform_repo_id,omitempty"`
	SourceMode          string           `json:"source_mode"`
	SyncStrategy        string           `json:"sync_strategy"`
	SubsystemSlug       string           `json:"subsystem_slug"`
	ResolvedBranch      string           `json:"resolved_branch"`
	CollectGlobs        []string         `json:"collect_globs,omitempty"`
	ExcludeGlobs        []string         `json:"exclude_globs,omitempty"`
	LastFetchedAt       *time.Time       `json:"last_fetched_at,omitempty"`
	RefCommitTime       *time.Time       `json:"ref_commit_time,omitempty"`
	LastCollectedCommit string           `json:"last_collected_commit"`
	Name                string           `json:"name"`
	Provider            string           `json:"provider"`
	RepoURL             string           `json:"repo_url"`
	APIBaseURL          string           `json:"api_base_url"`
	DefaultBranch       string           `json:"default_branch"`
	AuthType            string           `json:"auth_type"`
	HasCredentials      bool             `json:"has_credentials"`
	CredentialHint      string           `json:"credential_hint"`
	HasWebhookSecret    bool             `json:"has_webhook_secret"`
	SyncMode            string           `json:"sync_mode"`
	PollIntervalSec     int              `json:"poll_interval_sec"`
	AutoCompile         bool             `json:"auto_compile"`
	AutoActivate        bool             `json:"auto_activate"`
	LintOnSync          bool             `json:"lint_on_sync"`
	LastSyncAt          *time.Time       `json:"last_sync_at,omitempty"`
	LastSyncCommit      string           `json:"last_sync_commit"`
	LastSyncStatus      string           `json:"last_sync_status"`
	LastSyncError       string           `json:"last_sync_error"`
	Description         string           `json:"description"`
	CreatedBy           uint             `json:"created_by"`
	CreatedAt           time.Time        `json:"created_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
	Bundles             []GitProtoBundle `json:"bundles,omitempty"`
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
		sourceMode = GitSourceModeExternalStandalone
	}
	syncStrategy := g.SyncStrategy
	if syncStrategy == "" {
		syncStrategy = GitSyncStrategyFetchAndRead
	}
	return GitRepoRedactedDTO{
		ID:                  g.ID,
		PlatformRepoID:      g.PlatformRepoID,
		SourceMode:          sourceMode,
		SyncStrategy:        syncStrategy,
		SubsystemSlug:       g.SubsystemSlug,
		ResolvedBranch:      g.ResolvedBranch,
		CollectGlobs:        g.CollectGlobs(),
		ExcludeGlobs:        g.ExcludeGlobs(),
		LastFetchedAt:       g.LastFetchedAt,
		RefCommitTime:       g.RefCommitTime,
		LastCollectedCommit: g.LastCollectedCommit,
		Name:                g.Name,
		Provider:            g.Provider,
		RepoURL:             g.RepoURL,
		APIBaseURL:          g.APIBaseURL,
		DefaultBranch:       g.DefaultBranch,
		AuthType:            g.AuthType,
		HasCredentials:      hasCreds,
		CredentialHint:      hint,
		HasWebhookSecret:    strings.TrimSpace(g.WebhookSecretEnc) != "",
		SyncMode:            g.SyncMode,
		PollIntervalSec:     g.PollIntervalSec,
		AutoCompile:         g.AutoCompile,
		AutoActivate:        g.AutoActivate,
		LintOnSync:          g.LintOnSync,
		LastSyncAt:          g.LastSyncAt,
		LastSyncCommit:      g.LastSyncCommit,
		LastSyncStatus:      g.LastSyncStatus,
		LastSyncError:       g.LastSyncError,
		Description:         g.Description,
		CreatedBy:           g.CreatedBy,
		CreatedAt:           g.CreatedAt,
		UpdatedAt:           g.UpdatedAt,
		Bundles:             bundles,
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

// GitProtoBundle 实体 #21：Monorepo 多独立 Proto 子目录 / 编译打包单元 (1:N 归属 GitRepo)
type GitProtoBundle struct {
	ID               uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	GitRepoID        uint64         `gorm:"not null;uniqueIndex:uk_contract_git_bundle_repo_name;index:idx_contract_git_bundle_repo" json:"git_repo_id"`
	BundleName       string         `gorm:"size:128;not null;uniqueIndex:uk_contract_git_bundle_repo_name" json:"bundle_name"`
	TargetSchemaName string         `gorm:"size:128;not null;uniqueIndex:uk_contract_git_bundle_target" json:"target_schema_name"`
	Category         string         `gorm:"size:32;not null;default:'BUSINESS'" json:"category"` // BASE_LIB | BUSINESS | GATEWAY
	SubdirPath       string         `gorm:"size:512;not null;default:'.'" json:"subdir_path"`
	IncludeDirsJSON  datatypes.JSON `gorm:"column:include_dirs_json;type:jsonb;not null;default:'[\".\"]'" json:"include_dirs_json"`
	EntryFilesJSON   datatypes.JSON `gorm:"column:entry_files_json;type:jsonb;not null;default:'[]'" json:"entry_files_json"`
	FileGlobsJSON    datatypes.JSON `gorm:"column:file_globs_json;type:jsonb;not null;default:'[\"*.proto\"]'" json:"file_globs_json"`
	ExcludeGlobsJSON datatypes.JSON `gorm:"column:exclude_globs_json;type:jsonb;not null;default:'[]'" json:"exclude_globs_json"`
	AutoCompile      bool           `gorm:"not null;default:true" json:"auto_compile"`
	AutoActivate     bool           `gorm:"not null;default:false" json:"auto_activate"`
	MRAutoPublish    string         `gorm:"column:mr_auto_publish;size:32;not null;default:'DISABLED'" json:"mr_auto_publish"` // DISABLED | DRAFT_ONLY | AUTO_ACTIVATE
	LintOnSync       bool           `gorm:"not null;default:true" json:"lint_on_sync"`
	LastSyncCommit   string         `gorm:"size:64;not null;default:''" json:"last_sync_commit"`
	LastSyncAt       *time.Time     `json:"last_sync_at,omitempty"`
	LastSchemaID     *uint64        `json:"last_schema_id,omitempty"`
	Description      string         `gorm:"type:text;not null;default:''" json:"description"`
	CreatedAt        time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt        time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
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
	Category          string         `gorm:"size:32;not null;default:'BUSINESS'" json:"category"` // BASE_LIB | BUSINESS | GATEWAY
	FilePath          string         `gorm:"size:512;not null" json:"file_path"`                  // 逻辑导入路径（由 Stage C 填充）
	RepoFilePath      string         `gorm:"size:768;not null;default:''" json:"repo_file_path"`  // 相对于 Git 仓库根目录的 POSIX 路径
	PackageName       string         `gorm:"size:255;not null;default:''" json:"package_name"`
	RawBlobSHA        string         `gorm:"column:raw_blob_sha;size:64;not null;default:''" json:"raw_blob_sha"`
	NormalizerVersion string         `gorm:"column:normalizer_version;size:64;not null;default:''" json:"normalizer_version"`
	PipelineStatus    string         `gorm:"column:pipeline_status;size:24;not null;default:'NORMALIZED';index:idx_contract_pfile_pipeline_status" json:"pipeline_status"` // COLLECTED | NORMALIZED | QUARANTINED
	StatusReason      string         `gorm:"column:status_reason;type:text;not null;default:''" json:"status_reason"`
	LineMapJSON       datatypes.JSON `gorm:"column:line_map_json;type:jsonb" json:"line_map_json,omitempty"`
	IsExternalCopy    bool           `gorm:"column:is_external_copy;not null;default:false" json:"is_external_copy"`
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

// ensureADR26SchemaAndBackfill 幂等创建 ADR-26 部分唯一索引、CHECK 约束，并执行存量数据安全回填。
func ensureADR26SchemaAndBackfill(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	stmts := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_git_repo_platform ON contract_git_repos (platform_repo_id) WHERE platform_repo_id IS NOT NULL;`,
		`DO $$
BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_git_repo_source_mode') THEN
		ALTER TABLE contract_git_repos ADD CONSTRAINT chk_contract_git_repo_source_mode CHECK (source_mode IN ('PLATFORM_SHARED', 'EXTERNAL_STANDALONE'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_git_repo_sync_strategy') THEN
		ALTER TABLE contract_git_repos ADD CONSTRAINT chk_contract_git_repo_sync_strategy CHECK (sync_strategy IN ('FETCH_AND_READ', 'LOCAL_CACHE_ONLY'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_pfile_pipeline_status') THEN
		ALTER TABLE contract_proto_files ADD CONSTRAINT chk_contract_pfile_pipeline_status CHECK (pipeline_status IN ('COLLECTED', 'NORMALIZED', 'QUARANTINED'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_import_owner_source') THEN
		ALTER TABLE contract_proto_import_owners ADD CONSTRAINT chk_contract_import_owner_source CHECK (source IN ('MANUAL', 'BASE_LIB', 'AUTO'));
	END IF;
END $$;`,
		`UPDATE contract_git_repos SET source_mode = 'EXTERNAL_STANDALONE' WHERE source_mode = '' OR source_mode IS NULL;`,
		`UPDATE contract_git_repos SET sync_strategy = 'FETCH_AND_READ' WHERE sync_strategy = '' OR sync_strategy IS NULL;`,
		`UPDATE contract_proto_files SET pipeline_status = 'NORMALIZED' WHERE pipeline_status = '' OR pipeline_status IS NULL;`,
		`UPDATE contract_proto_files SET normalizer_version = 'passthrough-0' WHERE normalizer_version = '' OR normalizer_version IS NULL;`,
		`UPDATE contract_proto_files SET raw_blob_sha = content_sha256 WHERE (raw_blob_sha = '' OR raw_blob_sha IS NULL) AND content_sha256 <> '';`,
	}
	for _, stmt := range stmts {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}
