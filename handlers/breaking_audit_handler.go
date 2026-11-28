package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	commonModels "code-common/backend/models"
	"code-contract/internal/server/decoder"
	"code-contract/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// BreakingChangeEventDTO 破坏性变更审计事件标准化 DTO
type BreakingChangeEventDTO struct {
	ID                 uint64                          `json:"id"`
	Source             string                          `json:"source"`
	PublishRunID       *uint64                         `json:"publish_run_id,omitempty"`
	GitRepoID          *uint64                         `json:"git_repo_id,omitempty"`
	BundleID           *uint64                         `json:"bundle_id,omitempty"`
	BundleName         string                          `json:"bundle_name"`
	SchemaName         string                          `json:"schema_name"`
	FromSchemaID       *uint64                         `json:"from_schema_id,omitempty"`
	FromVersion        int                             `json:"from_version"`
	ToSchemaID         uint64                          `json:"to_schema_id"`
	ToVersion          int                             `json:"to_version"`
	BreakingCount      int                             `json:"breaking_count"`
	AffectedCount      int                             `json:"affected_count"`
	BreakingRules      []decoder.BreakingRuleViolation `json:"breaking_rules"`
	AffectedDependents []decoder.AffectedDependent     `json:"affected_dependents"`
	Status             string                          `json:"status"`
	TriggeredBy        string                          `json:"triggered_by"`
	AcknowledgedBy     *uint                           `json:"acknowledged_by,omitempty"`
	AcknowledgedByName string                          `json:"acknowledged_by_name"`
	AcknowledgeNote    string                          `json:"acknowledge_note"`
	AcknowledgedAt     *time.Time                      `json:"acknowledged_at,omitempty"`
	CreatedAt          time.Time                       `json:"created_at"`
	UpdatedAt          time.Time                       `json:"updated_at"`
}

func toBreakingChangeEventDTO(ev models.BreakingChangeEvent) BreakingChangeEventDTO {
	rules := make([]decoder.BreakingRuleViolation, 0)
	if len(ev.BreakingRulesJSON) > 0 {
		if err := json.Unmarshal(ev.BreakingRulesJSON, &rules); err != nil {
			rules = make([]decoder.BreakingRuleViolation, 0)
		}
	}
	affected := make([]decoder.AffectedDependent, 0)
	if len(ev.AffectedDependentsJSON) > 0 {
		if err := json.Unmarshal(ev.AffectedDependentsJSON, &affected); err != nil {
			affected = make([]decoder.AffectedDependent, 0)
		}
	}
	return BreakingChangeEventDTO{
		ID:                 ev.ID,
		Source:             ev.Source,
		PublishRunID:       ev.PublishRunID,
		GitRepoID:          ev.GitRepoID,
		BundleID:           ev.BundleID,
		BundleName:         ev.BundleName,
		SchemaName:         ev.SchemaName,
		FromSchemaID:       ev.FromSchemaID,
		FromVersion:        ev.FromVersion,
		ToSchemaID:         ev.ToSchemaID,
		ToVersion:          ev.ToVersion,
		BreakingCount:      ev.BreakingCount,
		AffectedCount:      ev.AffectedCount,
		BreakingRules:      rules,
		AffectedDependents: affected,
		Status:             ev.Status,
		TriggeredBy:        ev.TriggeredBy,
		AcknowledgedBy:     ev.AcknowledgedBy,
		AcknowledgedByName: ev.AcknowledgedByName,
		AcknowledgeNote:    ev.AcknowledgeNote,
		AcknowledgedAt:     ev.AcknowledgedAt,
		CreatedAt:          ev.CreatedAt,
		UpdatedAt:          ev.UpdatedAt,
	}
}

// ListBreakingChangeEventsHandler GET /api/breaking-change-events
func ListBreakingChangeEventsHandler(c *gin.Context) {
	page, pageSize := parseMRPageParams(c)
	status := strings.ToUpper(strings.TrimSpace(c.Query("status")))
	source := strings.ToUpper(strings.TrimSpace(c.Query("source")))
	publishRunIDStr := strings.TrimSpace(c.Query("publish_run_id"))
	q := strings.TrimSpace(c.Query("q"))

	db := models.DB.WithContext(c.Request.Context())
	query := db.Model(&models.BreakingChangeEvent{})
	if status == models.BreakingAuditStatusOpen || status == models.BreakingAuditStatusAcknowledged {
		query = query.Where("status = ?", status)
	}
	if source != "" {
		query = query.Where("source = ?", source)
	}
	if publishRunIDStr != "" {
		if runID, err := strconv.ParseUint(publishRunIDStr, 10, 64); err == nil && runID > 0 {
			query = query.Where("publish_run_id = ?", runID)
		}
	}
	if q != "" {
		like := "%" + q + "%"
		query = query.Where("schema_name ILIKE ? OR bundle_name ILIKE ? OR triggered_by ILIKE ?", like, like, like)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询破坏性变更审计总数失败: " + err.Error()})
		return
	}

	var openCount int64
	_ = db.Model(&models.BreakingChangeEvent{}).
		Where("status = ?", models.BreakingAuditStatusOpen).
		Count(&openCount).Error

	var rows []models.BreakingChangeEvent
	if err := query.
		Order("CASE WHEN status = 'OPEN' THEN 0 ELSE 1 END, id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询破坏性变更审计列表失败: " + err.Error()})
		return
	}

	items := make([]BreakingChangeEventDTO, 0, len(rows))
	for _, r := range rows {
		items = append(items, toBreakingChangeEventDTO(r))
	}

	c.JSON(http.StatusOK, gin.H{
		"items":      items,
		"total":      total,
		"open_count": openCount,
		"page":       page,
		"page_size":  pageSize,
	})
}

// GetBreakingChangeEventDetailHandler GET /api/breaking-change-events/:id
func GetBreakingChangeEventDetailHandler(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的审计事件 ID"})
		return
	}
	var ev models.BreakingChangeEvent
	if err := models.DB.WithContext(c.Request.Context()).First(&ev, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "未找到指定的破坏性变更审计事件"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, toBreakingChangeEventDTO(ev))
}

// AcknowledgeBreakingChangeEventHandler POST /api/breaking-change-events/:id/acknowledge (AdminOnly)
func AcknowledgeBreakingChangeEventHandler(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的审计事件 ID"})
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	_ = c.ShouldBindJSON(&req)

	var ev models.BreakingChangeEvent
	if err := models.DB.WithContext(c.Request.Context()).First(&ev, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "未找到指定的破坏性变更审计事件"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	uid, opName := currentOperatorName(c)
	now := time.Now().UTC()
	note := strings.TrimSpace(req.Note)
	before := ev
	updates := map[string]any{
		"status":               models.BreakingAuditStatusAcknowledged,
		"acknowledged_by":      &uid,
		"acknowledged_by_name": opName,
		"acknowledge_note":     note,
		"acknowledged_at":      &now,
	}
	if err := models.DB.WithContext(c.Request.Context()).Model(&ev).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "确认审计事件失败: " + err.Error()})
		return
	}
	_ = models.DB.WithContext(c.Request.Context()).First(&ev, id).Error

	recordAudit(c, "breaking_change_event:acknowledge", commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 确认破坏性变更审计事件 #%d (%s v%d -> v%d)", opName, ev.ID, ev.SchemaName, ev.FromVersion, ev.ToVersion),
		"breaking_change_event", ev.ID, ev.SchemaName, before, ev)

	c.JSON(http.StatusOK, toBreakingChangeEventDTO(ev))
}

// BatchAcknowledgeBreakingChangeEventsHandler POST /api/breaking-change-events/batch-acknowledge (AdminOnly)
func BatchAcknowledgeBreakingChangeEventsHandler(c *gin.Context) {
	var req struct {
		IDs  []uint64 `json:"ids"`
		Note string   `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法: " + err.Error()})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids 不能为空"})
		return
	}
	if len(req.IDs) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "单次批量确认不能超过 200 条"})
		return
	}

	uid, opName := currentOperatorName(c)
	now := time.Now().UTC()
	note := strings.TrimSpace(req.Note)
	res := models.DB.WithContext(c.Request.Context()).Model(&models.BreakingChangeEvent{}).
		Where("id IN ? AND status = ?", req.IDs, models.BreakingAuditStatusOpen).
		Updates(map[string]any{
			"status":               models.BreakingAuditStatusAcknowledged,
			"acknowledged_by":      &uid,
			"acknowledged_by_name": opName,
			"acknowledge_note":     note,
			"acknowledged_at":      &now,
		})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "批量确认审计事件失败: " + res.Error.Error()})
		return
	}

	recordAudit(c, "breaking_change_event:batch_acknowledge", commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 批量确认 %d 条破坏性变更审计事件", opName, res.RowsAffected),
		"breaking_change_event", 0, "batch_acknowledge", nil, req)

	c.JSON(http.StatusOK, gin.H{
		"acknowledged_count": res.RowsAffected,
	})
}
