package models

import (
	"errors"
	"time"

	"code-contract/config"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 现网发现持久化快照触发模式常量 (M30a, ADR-24)
const (
	DiscoveryTriggerManual      = "MANUAL"
	DiscoveryTriggerScheduled   = "SCHEDULED"
	DiscoveryTriggerAuto        = "AUTO"
	DiscoveryTriggerQuickRecord = "QUICK_RECORD"
)

// M30b 双态物理资产、放置、订阅边、裁决、归因与迁移常量 (ADR-24)
const (
	PlacementStatusActive  = "ACTIVE"
	PlacementStatusStale   = "STALE"
	PlacementStatusRetired = "RETIRED"

	ObservedTransportTCP   = "TCP"
	ObservedTransportMESH  = "MESH"
	ObservedTransportMixed = "MIXED"

	ProviderKindLocalEnrolled   = "LOCAL_ENROLLED"
	ProviderKindLocalUnenrolled = "LOCAL_UNENROLLED"
	ProviderKindRemoteLDC       = "REMOTE_LDC"
	ProviderKindOffline         = "OFFLINE"

	ProcDepSourceCSGSub  = "CSG_SUB"
	ProcDepSourceTraffic = "TRAFFIC_OBSERVED"

	DepDecisionIgnored   = "IGNORED"
	DepDecisionViolation = "VIOLATION"

	AttrJobTypeReattribute = "REATTRIBUTE"
	AttrJobTypeMigration   = "MIGRATION"

	AttrJobStatusPending   = "PENDING"
	AttrJobStatusRunning   = "RUNNING"
	AttrJobStatusDone      = "DONE"
	AttrJobStatusFailed    = "FAILED"
	AttrJobStatusCancelled = "CANCELLED"

	MigBackupStateApplied    = "APPLIED"
	MigBackupStateRolledBack = "ROLLED_BACK"
)

// 31. DiscoverySnapshot 现网进程、CSG 与 L1 流量探测持久化快照表 (contract_discovery_snapshots, M30a ADR-24)
// 自动按 (agent_id, csg_addr) 非置顶 FIFO 保留最近 discovery.snapshot_retention_count (默认 50) 份；置顶上限 discovery.snapshot_pin_limit (默认 20)
type DiscoverySnapshot struct {
	ID               uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	AgentID          uint64         `gorm:"not null;index:idx_contract_disc_snap_agent_time,priority:1;index:idx_contract_disc_snap_hash,priority:1" json:"agent_id"`
	Agent            *Agent         `gorm:"foreignKey:AgentID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	AgentName        string         `gorm:"size:128;not null;default:''" json:"agent_name"`
	TriggerMode      string         `gorm:"size:24;not null;default:'MANUAL'" json:"trigger_mode"`
	CSGAddr          string         `gorm:"size:128;not null;default:'';index:idx_contract_disc_snap_agent_time,priority:2;index:idx_contract_disc_snap_hash,priority:2" json:"csg_addr"`
	DiscoveryMode    string         `gorm:"size:32;not null;default:''" json:"discovery_mode"`
	Label            string         `gorm:"size:128;not null;default:''" json:"label"`
	IsPinned         bool           `gorm:"not null;default:false" json:"is_pinned"`
	RedactionVersion int            `gorm:"not null;default:1" json:"redaction_version"`
	SummaryJSON      datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"summary_json"`
	PayloadJSON      datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"payload_json"`
	ContentHash      string         `gorm:"size:64;not null;index:idx_contract_disc_snap_hash,priority:3" json:"content_hash"`
	CreatedBy        uint           `gorm:"not null;default:0" json:"created_by"`
	CapturedAt       time.Time      `gorm:"not null;index:idx_contract_disc_snap_agent_time,priority:3,sort:desc" json:"captured_at"`
	CreatedAt        time.Time      `json:"created_at"`
}

func (DiscoverySnapshot) TableName() string {
	return config.TablePrefix + "discovery_snapshots"
}

func ensureM30aDiscoveryConstraints(db *gorm.DB) error {
	sql := `
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_disc_snap_trigger') THEN
        ALTER TABLE contract_discovery_snapshots
            ADD CONSTRAINT chk_contract_disc_snap_trigger
            CHECK (trigger_mode IN ('MANUAL','SCHEDULED','AUTO','QUICK_RECORD'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_disc_snap_agent') THEN
        ALTER TABLE contract_discovery_snapshots
            ADD CONSTRAINT fk_contract_disc_snap_agent
            FOREIGN KEY (agent_id) REFERENCES contract_agents(id)
            ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_contract_disc_snap_agent_time ON contract_discovery_snapshots (agent_id, csg_addr, captured_at DESC);
CREATE INDEX IF NOT EXISTS idx_contract_disc_snap_hash ON contract_discovery_snapshots (agent_id, csg_addr, content_hash);
`
	return db.Exec(sql).Error
}

func ensureM30aDefaultSettings(db *gorm.DB) error {
	defaults := []ModuleSetting{
		{
			SettingKey:  "discovery.snapshot_retention_count",
			SettingVal:  "50",
			Description: "现网发现非置顶快照按 (agent_id, csg_addr) FIFO 保留上限（默认 50）",
			UpdatedBy:   "system",
		},
		{
			SettingKey:  "discovery.snapshot_pin_limit",
			SettingVal:  "20",
			Description: "现网发现置顶基线快照每 (agent_id, csg_addr) 组数量上限（默认 20）",
			UpdatedBy:   "system",
		},
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "setting_key"}},
		DoNothing: true,
	}).Create(&defaults).Error
}

// 32. ServicePlacement 物理服务放置事实表 (contract_service_placements, M30b ADR-24)
type ServicePlacement struct {
	ID                uint64             `gorm:"primaryKey;autoIncrement" json:"id"`
	ModuleServiceID   uint64             `gorm:"not null;uniqueIndex:uk_contract_svc_plc_tuple,priority:1" json:"module_service_id"`
	ModuleService     *ModuleService     `gorm:"foreignKey:ModuleServiceID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"module_service,omitempty"`
	ProcessID         uint64             `gorm:"not null;uniqueIndex:uk_contract_svc_plc_tuple,priority:2" json:"process_id"`
	Process           *Process           `gorm:"foreignKey:ProcessID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"process,omitempty"`
	AgentID           uint64             `gorm:"not null;index:idx_contract_svc_plc_agent,priority:1" json:"agent_id"`
	Agent             *Agent             `gorm:"foreignKey:AgentID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	ArchProcessID     uint64             `gorm:"not null;index:idx_contract_svc_plc_arch,priority:1" json:"arch_process_id"`
	ArchProcess       *ArchProcess       `gorm:"foreignKey:ArchProcessID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	RawServiceName    string             `gorm:"size:128;not null;default:''" json:"raw_service_name"`
	Endpoints         datatypes.JSON     `gorm:"type:jsonb;not null;default:'[]'" json:"endpoints"`
	ObservedTransport string             `gorm:"size:8;not null;default:'TCP'" json:"observed_transport"`
	InstanceCount     int                `gorm:"not null;default:1" json:"instance_count"`
	Status            string             `gorm:"size:16;not null;default:'ACTIVE';index:idx_contract_svc_plc_agent,priority:2;index:idx_contract_svc_plc_arch,priority:2" json:"status"`
	MissCount         int                `gorm:"not null;default:0" json:"miss_count"`
	FirstSeenAt       time.Time          `gorm:"not null;default:now()" json:"first_seen_at"`
	LastSeenAt        time.Time          `gorm:"not null;default:now()" json:"last_seen_at"`
	LastSnapshotID    *uint64            `gorm:"index:idx_contract_svc_plc_snap" json:"last_snapshot_id,omitempty"`
	LastSnapshot      *DiscoverySnapshot `gorm:"foreignKey:LastSnapshotID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL" json:"-"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

func (ServicePlacement) TableName() string {
	return config.TablePrefix + "service_placements"
}

// BeforeCreate 强制从 Process 派生 AgentID 与 ArchProcessID，保证冗余列一致 (ADR-24 §4.3)
func (sp *ServicePlacement) BeforeCreate(tx *gorm.DB) error {
	if sp.ProcessID == 0 {
		return errors.New("ServicePlacement: process_id 不能为空")
	}
	var proc Process
	if err := tx.Select("id, agent_id, arch_process_id").First(&proc, sp.ProcessID).Error; err != nil {
		return err
	}
	sp.AgentID = proc.AgentID
	sp.ArchProcessID = proc.ArchProcessID
	return nil
}

// BeforeUpdate 在更新 ProcessID 时重新派生 AgentID 与 ArchProcessID，并忽略未变更 ProcessID 的局部 Updates
func (sp *ServicePlacement) BeforeUpdate(tx *gorm.DB) error {
	if sp.ProcessID > 0 {
		var proc Process
		if err := tx.Select("id, agent_id, arch_process_id").First(&proc, sp.ProcessID).Error; err != nil {
			return err
		}
		sp.AgentID = proc.AgentID
		sp.ArchProcessID = proc.ArchProcessID
		tx.Statement.SetColumn("agent_id", proc.AgentID)
		tx.Statement.SetColumn("arch_process_id", proc.ArchProcessID)
	}
	return nil
}

// 33. ProcessDependency 环境级物理订阅边表 (contract_process_dependencies, M30b ADR-24)
type ProcessDependency struct {
	ID                    uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	ConsumerProcessID     uint64         `gorm:"not null;uniqueIndex:uk_contract_proc_dep_tuple,priority:1" json:"consumer_process_id"`
	ConsumerProcess       *Process       `gorm:"foreignKey:ConsumerProcessID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	AgentID               uint64         `gorm:"not null;index:idx_contract_proc_dep_agent" json:"agent_id"`
	Agent                 *Agent         `gorm:"foreignKey:AgentID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	ConsumerArchProcessID uint64         `gorm:"not null;index:idx_contract_proc_dep_type,priority:1" json:"consumer_arch_process_id"`
	ConsumerArchProcess   *ArchProcess   `gorm:"foreignKey:ConsumerArchProcessID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	ServiceName           string         `gorm:"size:128;not null;uniqueIndex:uk_contract_proc_dep_tuple,priority:2;index:idx_contract_proc_dep_type,priority:2" json:"service_name"`
	ServiceID             *uint64        `gorm:"index:idx_contract_proc_dep_service" json:"service_id,omitempty"`
	Service               *ModuleService `gorm:"foreignKey:ServiceID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL" json:"-"`
	ProviderKind          string         `gorm:"size:24;not null;default:'OFFLINE'" json:"provider_kind"`
	ProviderProcessID     *uint64        `gorm:"index:idx_contract_proc_dep_prov_proc" json:"provider_process_id,omitempty"`
	ProviderProcess       *Process       `gorm:"foreignKey:ProviderProcessID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL" json:"-"`
	ProviderArchProcessID *uint64        `gorm:"index:idx_contract_proc_dep_prov_arch" json:"provider_arch_process_id,omitempty"`
	ProviderArchProcess   *ArchProcess   `gorm:"foreignKey:ProviderArchProcessID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL" json:"-"`
	RemoteNode            string         `gorm:"size:128;not null;default:''" json:"remote_node"`
	ResolvedEndpoint      string         `gorm:"size:128;not null;default:''" json:"resolved_endpoint"`
	Source                string         `gorm:"size:24;not null;default:'CSG_SUB'" json:"source"`
	Status                string         `gorm:"size:16;not null;default:'ACTIVE'" json:"status"`
	MissCount             int            `gorm:"not null;default:0" json:"miss_count"`
	FirstSeenAt           time.Time      `gorm:"not null;default:now()" json:"first_seen_at"`
	LastSeenAt            time.Time      `gorm:"not null;default:now()" json:"last_seen_at"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
}

func (ProcessDependency) TableName() string {
	return config.TablePrefix + "process_dependencies"
}

// BeforeCreate 强制从 ConsumerProcess 派生 AgentID 与 ConsumerArchProcessID (ADR-24 §4.4)
func (pd *ProcessDependency) BeforeCreate(tx *gorm.DB) error {
	if pd.ConsumerProcessID == 0 {
		return errors.New("ProcessDependency: consumer_process_id 不能为空")
	}
	var proc Process
	if err := tx.Select("id, agent_id, arch_process_id").First(&proc, pd.ConsumerProcessID).Error; err != nil {
		return err
	}
	pd.AgentID = proc.AgentID
	pd.ConsumerArchProcessID = proc.ArchProcessID
	return nil
}

// BeforeUpdate 在更新 ConsumerProcessID 时重新派生 AgentID 与 ConsumerArchProcessID
func (pd *ProcessDependency) BeforeUpdate(tx *gorm.DB) error {
	if pd.ConsumerProcessID > 0 {
		var proc Process
		if err := tx.Select("id, agent_id, arch_process_id").First(&proc, pd.ConsumerProcessID).Error; err != nil {
			return err
		}
		pd.AgentID = proc.AgentID
		pd.ConsumerArchProcessID = proc.ArchProcessID
		tx.Statement.SetColumn("agent_id", proc.AgentID)
		tx.Statement.SetColumn("consumer_arch_process_id", proc.ArchProcessID)
	}
	return nil
}

// 34. DependencyDecision 逻辑依赖反向合成裁决记录表 (contract_dependency_decisions, M30b/M32 ADR-24)
type DependencyDecision struct {
	ID               uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	ConsumerModuleID uint64         `gorm:"not null;uniqueIndex:uk_contract_ddec_mod_svc,priority:1;index:idx_contract_ddec_mod" json:"consumer_module_id"`
	ConsumerModule   *Module        `gorm:"foreignKey:ConsumerModuleID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	ServiceID        uint64         `gorm:"not null;uniqueIndex:uk_contract_ddec_mod_svc,priority:2;index:idx_contract_ddec_svc" json:"service_id"`
	Service          *ModuleService `gorm:"foreignKey:ServiceID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	Decision         string         `gorm:"size:16;not null" json:"decision"` // IGNORED | VIOLATION
	Reason           string         `gorm:"size:512;not null;default:''" json:"reason"`
	DecidedBy        uint           `gorm:"not null;default:0" json:"decided_by"`
	DecidedAt        time.Time      `gorm:"not null;default:now()" json:"decided_at"`
}

func (DependencyDecision) TableName() string {
	return config.TablePrefix + "dependency_decisions"
}

// 35. AttributionJob 历史逻辑归因回补与迁移流量改写异步作业表 (contract_attribution_jobs, M30b~M32 ADR-24)
type AttributionJob struct {
	ID              uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	JobType         string         `gorm:"size:24;not null;default:'REATTRIBUTE'" json:"job_type"` // REATTRIBUTE | MIGRATION
	ScopeJSON       datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"scope_json"`
	Status          string         `gorm:"size:24;not null;default:'PENDING';index:idx_contract_ajob_status" json:"status"` // PENDING | RUNNING | DONE | FAILED | CANCELLED
	CursorSessionID uint64         `gorm:"not null;default:0" json:"cursor_session_id"`
	Processed       int64          `gorm:"not null;default:0" json:"processed"`
	Total           int64          `gorm:"not null;default:0" json:"total"`
	Error           string         `gorm:"type:text;not null;default:''" json:"error"`
	CreatedBy       uint           `gorm:"not null;default:0" json:"created_by"`
	CreatedAt       time.Time      `gorm:"index:idx_contract_ajob_created" json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

func (AttributionJob) TableName() string {
	return config.TablePrefix + "attribution_jobs"
}

// 36. MigBackup 存量 CSG 假模块迁移备份与回滚快照表 (contract_mig_backups, M30b ADR-24)
type MigBackup struct {
	ID            uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	MigrationKey  string         `gorm:"size:64;not null;uniqueIndex:uk_contract_mig_key_entity,priority:1;index:idx_contract_mig_key" json:"migration_key"`
	Entity        string         `gorm:"size:48;not null;uniqueIndex:uk_contract_mig_key_entity,priority:2" json:"entity"`
	EntityID      uint64         `gorm:"not null;uniqueIndex:uk_contract_mig_key_entity,priority:3" json:"entity_id"`
	Payload       datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"payload"`
	MigratedState string         `gorm:"size:24;not null;default:'APPLIED'" json:"migrated_state"` // APPLIED | ROLLED_BACK
	CreatedAt     time.Time      `json:"created_at"`
	RolledBackAt  *time.Time     `json:"rolled_back_at,omitempty"`
}

func (MigBackup) TableName() string {
	return config.TablePrefix + "mig_backups"
}

func ensureM30bSchemaAndConstraints(db *gorm.DB) error {
	// 按 ADR-24 §7.1 顺序幂等演进：
	// 1. contract_arch_processes & contract_processes CHECK/索引/外键
	// 2. contract_module_services module_id 可空化、FK 改 RESTRICT、CHECK I1 与部分索引
	// 3. 5 张新表 CHECK 与外键约束
	sql := `
DO $$
BEGIN
    -- 1. contract_arch_processes 约束与外键
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_arch_proc_declared_role') THEN
        ALTER TABLE contract_arch_processes
            ADD CONSTRAINT chk_contract_arch_proc_declared_role
            CHECK (declared_role IN ('UNKNOWN','SERVER','CLIENT_ONLY','DUAL'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_arch_proc_lifecycle') THEN
        ALTER TABLE contract_arch_processes
            ADD CONSTRAINT chk_contract_arch_proc_lifecycle
            CHECK (lifecycle IN ('PROVISIONAL','CONFIRMED'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_arch_proc_origin') THEN
        ALTER TABLE contract_arch_processes
            ADD CONSTRAINT chk_contract_arch_proc_origin
            CHECK (origin IN ('MANUAL','DISCOVERY','MIGRATED'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_arch_proc_origin_agent') THEN
        ALTER TABLE contract_arch_processes
            ADD CONSTRAINT fk_contract_arch_proc_origin_agent
            FOREIGN KEY (origin_agent_id) REFERENCES contract_agents(id)
            ON UPDATE CASCADE ON DELETE SET NULL;
    END IF;

    -- 2. contract_processes 约束
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proc_enroll_src') THEN
        ALTER TABLE contract_processes
            ADD CONSTRAINT chk_contract_proc_enroll_src
            CHECK (enroll_source IN ('MANUAL','DISCOVERY'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proc_obs_role') THEN
        ALTER TABLE contract_processes
            ADD CONSTRAINT chk_contract_proc_obs_role
            CHECK (observed_role IN ('UNKNOWN','SERVER','CLIENT_ONLY','DUAL'));
    END IF;

    -- 3. contract_module_services module_id 可空化、FK 改 RESTRICT 与 I1 不变量约束
    ALTER TABLE contract_module_services ALTER COLUMN module_id DROP NOT NULL;

    -- 清理旧 CASCADE 外键（兼容不同命名版本）并重建 RESTRICT
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_module_services_module') THEN
        ALTER TABLE contract_module_services DROP CONSTRAINT fk_contract_module_services_module;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_modules_services') THEN
        ALTER TABLE contract_module_services DROP CONSTRAINT fk_contract_modules_services;
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_constraint c
        JOIN pg_class t ON t.oid = c.conrelid
        WHERE t.relname = 'contract_module_services'
          AND c.conname = 'fk_contract_mod_svc_module'
          AND c.confdeltype <> 'r'
    ) THEN
        ALTER TABLE contract_module_services DROP CONSTRAINT fk_contract_mod_svc_module;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_mod_svc_module') THEN
        ALTER TABLE contract_module_services
            ADD CONSTRAINT fk_contract_mod_svc_module
            FOREIGN KEY (module_id) REFERENCES contract_modules(id)
            ON UPDATE CASCADE ON DELETE RESTRICT;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_mod_svc_source') THEN
        ALTER TABLE contract_module_services
            ADD CONSTRAINT chk_contract_mod_svc_source
            CHECK (source IN ('MANUAL', 'CSG_DISCOVERED', 'MIGRATED'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_mod_svc_proto_conf') THEN
        ALTER TABLE contract_module_services
            ADD CONSTRAINT chk_contract_mod_svc_proto_conf
            CHECK (proto_match_confidence IN ('NONE', 'FINGERPRINT', 'HEURISTIC', 'EXACT', 'MANUAL'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_mod_svc_unclaimed') THEN
        ALTER TABLE contract_module_services
            ADD CONSTRAINT chk_contract_mod_svc_unclaimed
            CHECK (module_id IS NOT NULL OR (contract_id IS NULL AND impl_key = '' AND source IN ('CSG_DISCOVERED', 'MIGRATED')));
    END IF;

    -- 4. contract_service_placements 约束与外键
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_sp_transport') THEN
        ALTER TABLE contract_service_placements
            ADD CONSTRAINT chk_contract_sp_transport
            CHECK (observed_transport IN ('TCP','MESH','MIXED'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_sp_status') THEN
        ALTER TABLE contract_service_placements
            ADD CONSTRAINT chk_contract_sp_status
            CHECK (status IN ('ACTIVE','STALE','RETIRED'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_sp_mod_svc') THEN
        ALTER TABLE contract_service_placements
            ADD CONSTRAINT fk_contract_sp_mod_svc
            FOREIGN KEY (module_service_id) REFERENCES contract_module_services(id)
            ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_sp_proc') THEN
        ALTER TABLE contract_service_placements
            ADD CONSTRAINT fk_contract_sp_proc
            FOREIGN KEY (process_id) REFERENCES contract_processes(id)
            ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_sp_agent') THEN
        ALTER TABLE contract_service_placements
            ADD CONSTRAINT fk_contract_sp_agent
            FOREIGN KEY (agent_id) REFERENCES contract_agents(id)
            ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_sp_arch_proc') THEN
        ALTER TABLE contract_service_placements
            ADD CONSTRAINT fk_contract_sp_arch_proc
            FOREIGN KEY (arch_process_id) REFERENCES contract_arch_processes(id)
            ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_sp_last_snap') THEN
        ALTER TABLE contract_service_placements
            ADD CONSTRAINT fk_contract_sp_last_snap
            FOREIGN KEY (last_snapshot_id) REFERENCES contract_discovery_snapshots(id)
            ON UPDATE CASCADE ON DELETE SET NULL;
    END IF;

    -- 5. contract_process_dependencies 约束与外键
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_pdep_prov_kind') THEN
        ALTER TABLE contract_process_dependencies
            ADD CONSTRAINT chk_contract_pdep_prov_kind
            CHECK (provider_kind IN ('LOCAL_ENROLLED','LOCAL_UNENROLLED','REMOTE_LDC','OFFLINE'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_pdep_source') THEN
        ALTER TABLE contract_process_dependencies
            ADD CONSTRAINT chk_contract_pdep_source
            CHECK (source IN ('CSG_SUB','TRAFFIC_OBSERVED'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_pdep_status') THEN
        ALTER TABLE contract_process_dependencies
            ADD CONSTRAINT chk_contract_pdep_status
            CHECK (status IN ('ACTIVE','STALE','RETIRED'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_pdep_consumer_proc') THEN
        ALTER TABLE contract_process_dependencies
            ADD CONSTRAINT fk_contract_pdep_consumer_proc
            FOREIGN KEY (consumer_process_id) REFERENCES contract_processes(id)
            ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_pdep_agent') THEN
        ALTER TABLE contract_process_dependencies
            ADD CONSTRAINT fk_contract_pdep_agent
            FOREIGN KEY (agent_id) REFERENCES contract_agents(id)
            ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_pdep_consumer_arch') THEN
        ALTER TABLE contract_process_dependencies
            ADD CONSTRAINT fk_contract_pdep_consumer_arch
            FOREIGN KEY (consumer_arch_process_id) REFERENCES contract_arch_processes(id)
            ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_pdep_service') THEN
        ALTER TABLE contract_process_dependencies
            ADD CONSTRAINT fk_contract_pdep_service
            FOREIGN KEY (service_id) REFERENCES contract_module_services(id)
            ON UPDATE CASCADE ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_pdep_prov_proc') THEN
        ALTER TABLE contract_process_dependencies
            ADD CONSTRAINT fk_contract_pdep_prov_proc
            FOREIGN KEY (provider_process_id) REFERENCES contract_processes(id)
            ON UPDATE CASCADE ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_pdep_prov_arch') THEN
        ALTER TABLE contract_process_dependencies
            ADD CONSTRAINT fk_contract_pdep_prov_arch
            FOREIGN KEY (provider_arch_process_id) REFERENCES contract_arch_processes(id)
            ON UPDATE CASCADE ON DELETE SET NULL;
    END IF;

    -- 6. contract_dependency_decisions 约束与外键
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_ddec_decision') THEN
        ALTER TABLE contract_dependency_decisions
            ADD CONSTRAINT chk_contract_ddec_decision
            CHECK (decision IN ('IGNORED','VIOLATION'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_ddec_consumer_mod') THEN
        ALTER TABLE contract_dependency_decisions
            ADD CONSTRAINT fk_contract_ddec_consumer_mod
            FOREIGN KEY (consumer_module_id) REFERENCES contract_modules(id)
            ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_ddec_service') THEN
        ALTER TABLE contract_dependency_decisions
            ADD CONSTRAINT fk_contract_ddec_service
            FOREIGN KEY (service_id) REFERENCES contract_module_services(id)
            ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;

    -- 7. contract_attribution_jobs 约束
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_ajob_type') THEN
        ALTER TABLE contract_attribution_jobs
            ADD CONSTRAINT chk_contract_ajob_type
            CHECK (job_type IN ('REATTRIBUTE','MIGRATION'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_ajob_status') THEN
        ALTER TABLE contract_attribution_jobs
            ADD CONSTRAINT chk_contract_ajob_status
            CHECK (status IN ('PENDING','RUNNING','DONE','FAILED','CANCELLED'));
    END IF;

    -- 8. contract_mig_backups 约束
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_mig_state') THEN
        ALTER TABLE contract_mig_backups
            ADD CONSTRAINT chk_contract_mig_state
            CHECK (migrated_state IN ('APPLIED','ROLLED_BACK'));
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_contract_arch_proc_fp ON contract_arch_processes (fingerprint_version, identity_fingerprint);
CREATE INDEX IF NOT EXISTS idx_contract_mod_svc_unclaimed ON contract_module_services (service_name) WHERE module_id IS NULL;
`
	return db.Exec(sql).Error
}

func ensureM30bDefaultSettings(db *gorm.DB) error {
	defaults := []ModuleSetting{
		{
			SettingKey:  "discovery.v2_enabled",
			SettingVal:  "false",
			Description: "现网发现双态解耦 v2 灰度开关（控制 Enroll、物理拓扑放置数据源与 import-blueprint 委托）",
			UpdatedBy:   "system",
		},
		{
			SettingKey:  "discovery.enroll_batch_limit_shared",
			SettingVal:  "50",
			Description: "SHARED 环境单次 Enroll 纳管进程数上限（1..200，默认 50）",
			UpdatedBy:   "system",
		},
		{
			SettingKey:  "discovery.reconcile_interval_sec",
			SettingVal:  "600",
			Description: "PhysicalReconciler 周期对账间隔秒数（60..86400，默认 600）",
			UpdatedBy:   "system",
		},
		{
			SettingKey:  "discovery.stale_miss_threshold",
			SettingVal:  "3",
			Description: "连续未观测触发 STALE 的次数阈值（1..20，默认 3）",
			UpdatedBy:   "system",
		},
		{
			SettingKey:  "discovery.stale_hours",
			SettingVal:  "24",
			Description: "未观测触发 STALE 的时长阈值（小时，1..720，默认 24）",
			UpdatedBy:   "system",
		},
		{
			SettingKey:  "discovery.retire_days",
			SettingVal:  "7",
			Description: "STALE 持续满指定天数转为 RETIRED（1..90，默认 7）",
			UpdatedBy:   "system",
		},
		{
			SettingKey:  "discovery.gc_days",
			SettingVal:  "30",
			Description: "全放置 RETIRED 后未认领身份及孤立 PROVISIONAL 进程 GC 保留天数（7..365，默认 30）",
			UpdatedBy:   "system",
		},
		{
			SettingKey:  "discovery.auto_downstreams",
			SettingVal:  "false",
			Description: "是否将同环境 ACTIVE TCP 订阅边自动并入常驻布防进程 connect4 下游集合（默认 false）",
			UpdatedBy:   "system",
		},
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "setting_key"}},
		DoNothing: true,
	}).Create(&defaults).Error
}
