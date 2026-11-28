package models

import (
	"time"

	"code-contract/config"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	BreakingSourcePipelinePublish = "PIPELINE_PUBLISH"
	BreakingSourceManualActivate  = "MANUAL_ACTIVATE"
	BreakingSourceBatchActivate   = "BATCH_ACTIVATE"
	BreakingSourceAutoActivate    = "AUTO_ACTIVATE"

	BreakingAuditStatusOpen         = "OPEN"
	BreakingAuditStatusAcknowledged = "ACKNOWLEDGED"
)

// BreakingChangeEvent 记录非阻断式契约上线（流水线发布 / 手动激活 / 批量激活）中检出的破坏性变更审计事件。
type BreakingChangeEvent struct {
	ID                     uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	Source                 string         `gorm:"size:32;not null;default:'PIPELINE_PUBLISH';index:idx_contract_bce_source" json:"source"`
	PublishRunID           *uint64        `gorm:"index:idx_contract_bce_pub_run" json:"publish_run_id,omitempty"`
	GitRepoID              *uint64        `gorm:"index:idx_contract_bce_repo" json:"git_repo_id,omitempty"`
	BundleID               *uint64        `gorm:"index:idx_contract_bce_bundle" json:"bundle_id,omitempty"`
	BundleName             string         `gorm:"size:160;not null;default:''" json:"bundle_name"`
	SchemaName             string         `gorm:"size:160;not null;index:idx_contract_bce_schema_name" json:"schema_name"`
	FromSchemaID           *uint64        `json:"from_schema_id,omitempty"`
	FromVersion            int            `gorm:"not null;default:0" json:"from_version"`
	ToSchemaID             uint64         `gorm:"not null;index:idx_contract_bce_to_schema" json:"to_schema_id"`
	ToVersion              int            `gorm:"not null;default:0" json:"to_version"`
	BreakingCount          int            `gorm:"not null;default:0" json:"breaking_count"`
	AffectedCount          int            `gorm:"not null;default:0" json:"affected_count"`
	BreakingRulesJSON      datatypes.JSON `gorm:"column:breaking_rules;type:jsonb;not null;default:'[]'" json:"breaking_rules"`
	AffectedDependentsJSON datatypes.JSON `gorm:"column:affected_dependents;type:jsonb;not null;default:'[]'" json:"affected_dependents"`
	Status                 string         `gorm:"size:24;not null;default:'OPEN';index:idx_contract_bce_status" json:"status"`
	TriggeredBy            string         `gorm:"size:100;not null;default:''" json:"triggered_by"`
	AcknowledgedBy         *uint          `gorm:"default:null" json:"acknowledged_by,omitempty"`
	AcknowledgedByName     string         `gorm:"size:100;not null;default:''" json:"acknowledged_by_name"`
	AcknowledgeNote        string         `gorm:"type:text;not null;default:''" json:"acknowledge_note"`
	AcknowledgedAt         *time.Time     `gorm:"default:null" json:"acknowledged_at,omitempty"`
	CreatedAt              time.Time      `json:"created_at"`
	UpdatedAt              time.Time      `json:"updated_at"`
}

// TableName 指定破坏性变更审计事件表名（带模块前缀）。
func (BreakingChangeEvent) TableName() string {
	return config.TablePrefix + "breaking_change_events"
}

// ensureBreakingChangeEventConstraints 幂等创建破坏性变更审计表索引与 CHECK 约束。
func ensureBreakingChangeEventConstraints(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_contract_bce_status_created
			ON contract_breaking_change_events (status, created_at DESC)`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_bce_status') THEN
				ALTER TABLE contract_breaking_change_events ADD CONSTRAINT chk_contract_bce_status
					CHECK (status IN ('OPEN', 'ACKNOWLEDGED'));
			END IF;
		END $$;`,
	}
	for _, stmt := range stmts {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}
