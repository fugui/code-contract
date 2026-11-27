package gitclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"code-contract/internal/server/notify"
	"code-contract/models"

	"gorm.io/gorm"
)

func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// RunArchivedSchemaReminderCheck 检查上线/生效满 reminderDays（默认 30）天且仍生效、未被 Git 来源完全覆盖的归档外部 Schema，
// 每 30 天向 contract_admin 及关联模块负责人发送一次站内信提醒 (ADR-29 §2.5.5 / §6.5)。
func RunArchivedSchemaReminderCheck(
	ctx context.Context,
	db *gorm.DB,
	notifier *notify.Dispatcher,
	now time.Time,
	reminderDays int,
) (int, error) {
	if db == nil || notifier == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if reminderDays <= 0 {
		reminderDays = 30
	}

	items, err := ListArchivedExternalSchemas(ctx, db)
	if err != nil {
		return 0, err
	}

	cutoff := now.Add(-time.Duration(reminderDays) * 24 * time.Hour)
	sentCount := 0

	for _, item := range items {
		if !item.IsActive || item.CreatedAt.After(cutoff) {
			continue
		}
		if item.TotalServices > 0 && len(item.UncoveredNames) == 0 {
			continue
		}

		// 检查最近 reminderDays 天内是否已经发过提醒
		var recentCnt int64
		_ = db.WithContext(ctx).Model(&models.Notification{}).
			Where("category = ? AND source_type = ? AND source_id = ? AND created_at > ?",
				models.NotifCategoryArchiveReminder, models.NotifSourceProtoSchema, item.SchemaID, cutoff).
			Count(&recentCnt).Error
		if recentCnt > 0 {
			continue
		}

		// 解析关联模块负责人 + contract_admin
		var modIDs []uint64
		if len(item.UncoveredNames) > 0 {
			_ = db.WithContext(ctx).Model(&models.ModuleService{}).
				Where("service_name IN ? AND module_id IS NOT NULL", item.UncoveredNames).
				Distinct("module_id").
				Pluck("module_id", &modIDs).Error
		}
		rcpts := notify.ResolveRecipientUserIDsForModules(db, modIDs, "", "", true)
		if len(rcpts) == 0 {
			continue
		}

		uncoveredStr := "无"
		if len(item.UncoveredNames) > 0 {
			uncoveredStr = strings.Join(item.UncoveredNames, ", ")
		}
		body := fmt.Sprintf(
			"历史归档描述集 **%s (v%d)**（来源 `%s`）已生效满 %d 天，目前仍有 %d/%d 个服务未被平台 Git 流水线生效资产覆盖。\n\n"+
				"- 未覆盖服务：`%s`\n\n"+
				"请前往 [契约同步与发布](/proto-pipeline?stage=publish) 检查对应 Git 仓并同步发布后，再停用该归档版本。",
			item.Name, item.Version, item.SourceType, reminderDays, len(item.UncoveredNames), item.TotalServices, uncoveredStr,
		)
		if _, dErr := notifier.DispatchSync(ctx, notify.DispatchInput{
			RecipientUserIDs: rcpts,
			Category:         models.NotifCategoryArchiveReminder,
			Severity:         models.NotifSeverityWarning,
			Title:            fmt.Sprintf("历史归档描述集 [%s v%d] 待切换至 Git 流水线", item.Name, item.Version),
			BodyMarkdown:     body,
			LinkURL:          "/proto-pipeline?stage=publish",
			SourceType:       models.NotifSourceProtoSchema,
			SourceID:         item.SchemaID,
			Metadata: map[string]any{
				"schema_id":       item.SchemaID,
				"schema_name":     item.Name,
				"uncovered_names": item.UncoveredNames,
			},
		}); dErr != nil {
			log.Printf("[ArchiveReminder] 发送归档提醒失败 (schema#%d): %v", item.SchemaID, dErr)
			continue
		}
		sentCount++
	}
	return sentCount, nil
}
