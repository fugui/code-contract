package models

import (
	"time"

	"code-contract/config"
)

// ModuleSetting 模块运行与治理参数配置模型 (contract_settings)
type ModuleSetting struct {
	ID          uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	SettingKey  string    `gorm:"size:64;uniqueIndex:uk_contract_settings_key;not null" json:"setting_key"`
	SettingVal  string    `gorm:"type:text;not null" json:"setting_val"`
	Description string    `gorm:"size:255" json:"description"`
	UpdatedBy   string    `gorm:"size:128" json:"updated_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName 显式添加模块专属表名前缀
func (ModuleSetting) TableName() string {
	return config.TablePrefix + "settings"
}
