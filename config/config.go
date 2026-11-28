package config

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code-common/backend/configutil"
	commonModels "code-common/backend/models"

	"gopkg.in/yaml.v3"
)

// 模块级核心元数据常量（集中声明，便于 scripts/init-module.sh 或手工派生时一站式替换）
const (
	ModuleKey   = "contract"
	ServiceName = "Code-Contract"
	AdminRole   = "contract_admin"
	TablePrefix = "contract_"
	DefaultPort = ":8091"
)

// AuthSection 定义模块认证与独立运行开关配置
type AuthSection struct {
	JWTSecret            string                    `yaml:"jwt_secret"`
	StandaloneMode       bool                      `yaml:"standalone_mode"`
	PasswordLoginEnabled bool                      `yaml:"password_login_enabled"`
	OAuth2               commonModels.OAuth2Config `yaml:"oauth2"`
}

// AgentHubSection 定义主控与数据面 Agent 管控信道配置
type AgentHubSection struct {
	DevAllowLoopback     bool `yaml:"dev_allow_loopback"`     // 仅本地开发与自动化测试允许连接 127.0.0.0/8 Agent
	HeartbeatIntervalSec int  `yaml:"heartbeat_interval_sec"` // 心跳与双向租约对账周期（默认 10s）
}

// StorageSection 定义宿主机共享 Git 缓存与只读聚合 Proto 目录树存储配置 (ADR-26)
type StorageSection struct {
	Root               string `yaml:"root"`                 // 默认 ./data
	CodesDir           string `yaml:"codes_dir"`            // 为空取 <root>/codes；同机部署可与 code-shield 共享
	ProtosDir          string `yaml:"protos_dir"`           // 为空取 <root>/protos
	SharedWriteEnabled bool   `yaml:"shared_write_enabled"` // code-shield 完成两层锁升级前保持 false
}

// GetCodesDir 返回共享 Git 缓存根目录（默认 <root>/codes）。
func (s StorageSection) GetCodesDir() string {
	if trimmed := strings.TrimSpace(s.CodesDir); trimmed != "" {
		return filepath.Clean(trimmed)
	}
	root := strings.TrimSpace(s.Root)
	if root == "" {
		root = "./data"
	}
	return filepath.Join(filepath.Clean(root), "codes")
}

// GetProtosDir 返回只读聚合 Proto 目录树根目录（默认 <root>/protos）。
func (s StorageSection) GetProtosDir() string {
	if trimmed := strings.TrimSpace(s.ProtosDir); trimmed != "" {
		return filepath.Clean(trimmed)
	}
	root := strings.TrimSpace(s.Root)
	if root == "" {
		root = "./data"
	}
	return filepath.Join(filepath.Clean(root), "protos")
}

// ProtoCollectSection 定义跨仓 pg 目录白名单采集、清理与定时调度配置 (ADR-26)
type ProtoCollectSection struct {
	CollectGlobs    []string `yaml:"collect_globs"`
	ExcludeGlobs    []string `yaml:"exclude_globs"`
	MaxFileBytes    int64    `yaml:"max_file_bytes"`
	MaxFilesPerRepo int      `yaml:"max_files_per_repo"`
	SyncCron        string   `yaml:"sync_cron"`
	SyncConcurrency int      `yaml:"sync_concurrency"`
	StaleThreshold  string   `yaml:"stale_threshold"`
}

// StaleThresholdDuration 解析缓存陈旧阈值（默认 24h）。
func (p ProtoCollectSection) StaleThresholdDuration() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(p.StaleThreshold)); err == nil && d > 0 {
		return d
	}
	return 24 * time.Hour
}

// ProtoPipelineSection 定义四阶段 Proto 流水线与阶段 ④ 拓扑发布控制配置 (ADR-29 r4)
type ProtoPipelineSection struct {
	PublishMode         string `yaml:"publish_mode"`          // AUTO | PREVIEW（默认 AUTO）
	CompileConcurrency  int    `yaml:"compile_concurrency"`   // 阶段 ④ 同拓扑层并发编译数（默认 4）
	FanoutWarnThreshold int    `yaml:"fanout_warn_threshold"` // 扇出提示阈值（默认 100）
	MRMergeDebounceSec  int    `yaml:"mr_merge_debounce_sec"` // MR 合入增量流水线去抖秒数（默认 300）
	ArchiveReminderDays int    `yaml:"archive_reminder_days"` // 归档外部资产提醒周期天数（默认 30）
}

// EffectivePublishMode 返回归一化后的发布模式（AUTO | PREVIEW，默认 AUTO）。
func (p ProtoPipelineSection) EffectivePublishMode() string {
	if strings.EqualFold(strings.TrimSpace(p.PublishMode), "PREVIEW") {
		return "PREVIEW"
	}
	return "AUTO"
}

// Config 定义模块全局配置结构
type Config struct {
	Server        configutil.ServerConfig     `yaml:"server"`
	Database      commonModels.DatabaseConfig `yaml:"database"`
	Auth          AuthSection                 `yaml:"auth"`
	AgentHub      AgentHubSection             `yaml:"agent_hub"`
	Storage       StorageSection              `yaml:"storage"`
	ProtoCollect  ProtoCollectSection         `yaml:"proto_collect"`
	ProtoPipeline ProtoPipelineSection        `yaml:"proto_pipeline"`
}

// AppConfig 全局运行时配置单例
var AppConfig Config

// LoadConfig 从指定路径加载 YAML 配置；若不存在则尝试回退至 config.yaml.example 或默认配置
func LoadConfig(filename string) error {
	AppConfig = Config{}

	actualPath := filename
	if _, err := os.Stat(actualPath); os.IsNotExist(err) {
		if _, exErr := os.Stat("config.yaml.example"); exErr == nil {
			log.Printf("[%s] 未检测到 %s，回退加载 config.yaml.example", ServiceName, filename)
			actualPath = "config.yaml.example"
		} else {
			log.Printf("[%s] 配置文件不存在，启用内置默认安全配置", ServiceName)
			applyDefaults()
			return nil
		}
	}

	data, err := os.ReadFile(actualPath)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(data, &AppConfig); err != nil {
		return err
	}

	applyDefaults()
	return nil
}

func applyDefaults() {
	configutil.ApplyServerDefaults(&AppConfig.Server, DefaultPort)

	// 支持环境变量覆盖 JWT_SECRET（生产环境推荐通过环境变量或密钥管理系统注入）
	// TODO(security): 生产环境集群部署时请接入企业 KMS / Secret Manager 注入统一密钥，禁用文件明文落盘
	if envSecret := strings.TrimSpace(os.Getenv("JWT_SECRET")); envSecret != "" {
		AppConfig.Auth.JWTSecret = envSecret
	}
	configutil.EnsureJWTSecret(&AppConfig.Auth.JWTSecret, ServiceName)

	if AppConfig.Auth.OAuth2.Enabled {
		if len(AppConfig.Auth.OAuth2.Scopes) == 0 {
			AppConfig.Auth.OAuth2.Scopes = []string{"openid", "profile", "email"}
		}
		if AppConfig.Auth.OAuth2.FieldMapping.Username == "" {
			AppConfig.Auth.OAuth2.FieldMapping.Username = "preferred_username"
		}
		if AppConfig.Auth.OAuth2.FieldMapping.Email == "" {
			AppConfig.Auth.OAuth2.FieldMapping.Email = "email"
		}
		if AppConfig.Auth.OAuth2.FieldMapping.Name == "" {
			AppConfig.Auth.OAuth2.FieldMapping.Name = "name"
		}
		if AppConfig.Auth.OAuth2.RedirectURL == "" {
			AppConfig.Auth.OAuth2.RedirectURL = strings.TrimRight(AppConfig.Server.ExternalURL, "/") + "/api/oauth2/callback"
		}
	}

	ApplyStorageAndCollectDefaults(&AppConfig.Storage, &AppConfig.ProtoCollect)
	ApplyProtoPipelineDefaults(&AppConfig.ProtoPipeline)
}

// ApplyProtoPipelineDefaults 为四阶段流水线与阶段 ④ 发布填充安全默认值 (ADR-29 r4)。
func ApplyProtoPipelineDefaults(pp *ProtoPipelineSection) {
	if pp == nil {
		return
	}
	pp.PublishMode = pp.EffectivePublishMode()
	if pp.CompileConcurrency <= 0 {
		pp.CompileConcurrency = 4
	}
	if pp.FanoutWarnThreshold <= 0 {
		pp.FanoutWarnThreshold = 100
	}
	if pp.MRMergeDebounceSec <= 0 {
		pp.MRMergeDebounceSec = 300
	}
	if pp.ArchiveReminderDays <= 0 {
		pp.ArchiveReminderDays = 30
	}
}

// DefaultCollectGlobs 返回默认 pg 白名单采集规则（严格全小写 pg，覆盖根目录、一级及任意深层 pg 目录）。
func DefaultCollectGlobs() []string {
	return []string{"pg/**/*.proto", "*/pg/**/*.proto", "**/pg/**/*.proto"}
}

// EffectiveCollectGlobs 返回生效的 pg 白名单采集规则：优先使用运行时配置 proto_collect.collect_globs，为空时回退至 DefaultCollectGlobs。
func EffectiveCollectGlobs() []string {
	if len(AppConfig.ProtoCollect.CollectGlobs) > 0 {
		out := make([]string, len(AppConfig.ProtoCollect.CollectGlobs))
		copy(out, AppConfig.ProtoCollect.CollectGlobs)
		return out
	}
	return DefaultCollectGlobs()
}

// EffectiveExcludeGlobs 返回生效的二级排除规则：优先使用运行时配置 proto_collect.exclude_globs，为空时回退至 DefaultExcludeGlobs。
func EffectiveExcludeGlobs() []string {
	if len(AppConfig.ProtoCollect.ExcludeGlobs) > 0 {
		out := make([]string, len(AppConfig.ProtoCollect.ExcludeGlobs))
		copy(out, AppConfig.ProtoCollect.ExcludeGlobs)
		return out
	}
	return DefaultExcludeGlobs()
}

// DefaultExcludeGlobs 返回 ADR-26 规定的默认二级排除规则。
func DefaultExcludeGlobs() []string {
	return []string{
		"**/test/**",
		"**/testdata/**",
		"**/mock/**",
		"**/examples/**",
		"**/third_party/**",
		"**/vendor/**",
	}
}

// ApplyStorageAndCollectDefaults 为 Storage 与 ProtoCollect 填充安全默认值。
func ApplyStorageAndCollectDefaults(st *StorageSection, pc *ProtoCollectSection) {
	if st != nil {
		if strings.TrimSpace(st.Root) == "" {
			st.Root = "./data"
		}
	}
	if pc != nil {
		if len(pc.CollectGlobs) == 0 {
			pc.CollectGlobs = DefaultCollectGlobs()
		}
		if len(pc.ExcludeGlobs) == 0 {
			pc.ExcludeGlobs = DefaultExcludeGlobs()
		}
		if pc.MaxFileBytes <= 0 {
			pc.MaxFileBytes = 2 * 1024 * 1024 // 2 MiB
		}
		if pc.MaxFilesPerRepo <= 0 {
			pc.MaxFilesPerRepo = 5000
		}
		if strings.TrimSpace(pc.SyncCron) == "" {
			pc.SyncCron = "*/30 * * * *"
		}
		if pc.SyncConcurrency <= 0 {
			pc.SyncConcurrency = 4
		}
		if strings.TrimSpace(pc.StaleThreshold) == "" {
			pc.StaleThreshold = "24h"
		}
	}
}
