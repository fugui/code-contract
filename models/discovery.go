package models

import (
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
