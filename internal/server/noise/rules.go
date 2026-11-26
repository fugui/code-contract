package noise

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/gorm"
)

const (
	// SettingKeyNoiseServiceRules 全局噪音服务通配规则配置键（contract_settings）
	SettingKeyNoiseServiceRules = "noise_service_rules"
)

// MarkedNoiseServiceDTO 服务台账中已标记为低关注/噪音的服务摘要
type MarkedNoiseServiceDTO struct {
	ID               uint64  `json:"id"`
	ServiceName      string  `json:"service_name"`
	ProtoServiceName string  `json:"proto_service_name,omitempty"`
	ModuleID         *uint64 `json:"module_id,omitempty"`
	ModuleName       string  `json:"module_name,omitempty"`
}

// NoiseRulePresetDTO 管理设置页推荐模板条目（仅展示，不自动写入数据库）
type NoiseRulePresetDTO struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Rules       []string `json:"rules"`
}

// EffectiveNoiseConfigDTO 团队双源噪音配置聚合视图
type EffectiveNoiseConfigDTO struct {
	GlobalRules     []string                `json:"global_rules"`
	MarkedServices  []MarkedNoiseServiceDTO `json:"marked_services"`
	EffectiveRules  []string                `json:"effective_rules"`
	PresetTemplates []NoiseRulePresetDTO    `json:"preset_templates"`
}

// DefaultPresetTemplates 返回推荐模板，供管理员按需一键填入，避免默认通配误伤业务服务
func DefaultPresetTemplates() []NoiseRulePresetDTO {
	return []NoiseRulePresetDTO{
		{
			ID:          "logging",
			Name:        "日志类服务",
			Description: "匹配常见异步/集中式日志服务（如 *LogService*、*Logger*），避免误伤 LoginService",
			Rules:       []string{"*LogService*", "*Logger*"},
		},
		{
			ID:          "monitoring",
			Name:        "监控与指标上报",
			Description: "匹配监控采集、指标打点与遥测上报服务",
			Rules:       []string{"*MonitorService*", "*Metrics*", "*Telemetry*"},
		},
		{
			ID:          "heartbeat",
			Name:        "心跳与探活",
			Description: "匹配周期性心跳与健康检查方法",
			Rules:       []string{"*Heartbeat*", "*:Ping", "*:HealthCheck"},
		},
	}
}

// LoadEffectiveNoiseConfig 读取全局设置规则与服务台账标记并合并去重生成 SSOT 生效规则
func LoadEffectiveNoiseConfig(ctx context.Context, db *gorm.DB) (*EffectiveNoiseConfigDTO, error) {
	dto := &EffectiveNoiseConfigDTO{
		GlobalRules:     []string{},
		MarkedServices:  []MarkedNoiseServiceDTO{},
		EffectiveRules:  []string{},
		PresetTemplates: DefaultPresetTemplates(),
	}
	if db == nil {
		return dto, nil
	}

	var setting models.ModuleSetting
	if err := db.WithContext(ctx).Where("setting_key = ?", SettingKeyNoiseServiceRules).First(&setting).Error; err == nil {
		dto.GlobalRules = agentproto.ParseNoiseRulesText(setting.SettingVal)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("读取全局噪音规则失败: %w", err)
	}

	var markedRows []models.ModuleService
	if err := db.WithContext(ctx).
		Preload("Module").
		Where("is_noise_service = ?", true).
		Order("id ASC").
		Find(&markedRows).Error; err != nil {
		return nil, fmt.Errorf("读取台账噪音服务标记失败: %w", err)
	}

	combined := make([]string, 0, len(dto.GlobalRules)+len(markedRows)*2)
	combined = append(combined, dto.GlobalRules...)

	for _, svc := range markedRows {
		modName := ""
		if svc.Module != nil {
			modName = svc.Module.Name
		}
		dto.MarkedServices = append(dto.MarkedServices, MarkedNoiseServiceDTO{
			ID:               svc.ID,
			ServiceName:      svc.ServiceName,
			ProtoServiceName: svc.ProtoServiceName,
			ModuleID:         svc.ModuleID,
			ModuleName:       modName,
		})
		if strings.TrimSpace(svc.ServiceName) != "" {
			combined = append(combined, strings.TrimSpace(svc.ServiceName))
		}
		if strings.TrimSpace(svc.ProtoServiceName) != "" {
			combined = append(combined, strings.TrimSpace(svc.ProtoServiceName))
		}
	}

	dto.EffectiveRules = agentproto.NormalizeNoiseRules(combined)
	return dto, nil
}

// AppendGlobalNoiseRule 校验并追加一条全局噪音规则（去重保序），返回更新后的双源配置
func AppendGlobalNoiseRule(ctx context.Context, db *gorm.DB, rule, operator string) (*EffectiveNoiseConfigDTO, error) {
	if db == nil {
		return nil, errors.New("数据库未就绪")
	}
	trimmed := strings.TrimSpace(rule)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return nil, errors.New("噪音规则不能为空")
	}
	if err := agentproto.ValidateNoiseRules([]string{trimmed}); err != nil {
		return nil, err
	}

	var setting models.ModuleSetting
	err := db.WithContext(ctx).Where("setting_key = ?", SettingKeyNoiseServiceRules).First(&setting).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("查询全局噪音规则失败: %w", err)
	}

	existing := agentproto.ParseNoiseRulesText(setting.SettingVal)
	merged := agentproto.NormalizeNoiseRules(append(existing, trimmed))
	if err := agentproto.ValidateNoiseRules(merged); err != nil {
		return nil, err
	}
	nextVal := strings.Join(merged, "\n")

	if errors.Is(err, gorm.ErrRecordNotFound) {
		setting = models.ModuleSetting{
			SettingKey:  SettingKeyNoiseServiceRules,
			SettingVal:  nextVal,
			Description: "全局低关注/噪音服务匹配规则（支持 * 通配与 Service:Operation）",
			UpdatedBy:   operator,
		}
		if createErr := db.WithContext(ctx).Create(&setting).Error; createErr != nil {
			return nil, fmt.Errorf("保存全局噪音规则失败: %w", createErr)
		}
	} else {
		setting.SettingVal = nextVal
		if setting.Description == "" {
			setting.Description = "全局低关注/噪音服务匹配规则（支持 * 通配与 Service:Operation）"
		}
		setting.UpdatedBy = operator
		if saveErr := db.WithContext(ctx).Save(&setting).Error; saveErr != nil {
			return nil, fmt.Errorf("更新全局噪音规则失败: %w", saveErr)
		}
	}

	return LoadEffectiveNoiseConfig(ctx, db)
}

var sqlLikeReplacer = strings.NewReplacer(
	`\`, `\\`,
	`%`, `\%`,
	`_`, `\_`,
	`*`, `%`,
)

func globToSQLLike(pat string) string {
	return sqlLikeReplacer.Replace(strings.TrimSpace(pat))
}

// BuildNoiseSQLFilter 将归一化后的噪音规则列表编译为安全的 PostgreSQL 匹配子句（供平铺列表 SQL 过滤与计数复用）
func BuildNoiseSQLFilter(rules []string) (string, []interface{}) {
	normalized := agentproto.NormalizeNoiseRules(rules)
	if len(normalized) == 0 {
		return "", nil
	}

	clauses := make([]string, 0, len(normalized))
	args := make([]interface{}, 0, len(normalized)*3)

	const svcNormExpr = `(service_name ~ '^.+-[0-9]+-[0-9]+$' AND regexp_replace(service_name, '-[0-9]+$', '') ILIKE ? ESCAPE '\')`

	for _, rule := range normalized {
		svcPat := rule
		opPat := ""
		if idx := strings.Index(rule, ":"); idx >= 0 {
			svcPat = strings.TrimSpace(rule[:idx])
			opPat = strings.TrimSpace(rule[idx+1:])
		}
		if svcPat == "" {
			continue
		}
		svcLike := globToSQLLike(svcPat)
		svcClause := `(service_name ILIKE ? ESCAPE '\' OR ` + svcNormExpr + `)`
		if opPat != "" {
			opLike := globToSQLLike(opPat)
			clauses = append(clauses, `(`+svcClause+` AND operation ILIKE ? ESCAPE '\')`)
			args = append(args, svcLike, svcLike, opLike)
		} else {
			clauses = append(clauses, svcClause)
			args = append(args, svcLike, svcLike)
		}
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}
