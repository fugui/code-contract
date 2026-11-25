package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	commonAudit "code-common/backend/audit"
	commonAuth "code-common/backend/auth"
	commonModels "code-common/backend/models"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"github.com/gin-gonic/gin"
)

// ListCatalogServices handles GET /api/catalog/services (M31 统一契约目录与五态雷达).
func ListCatalogServices(c *gin.Context) {
	page, pageSize := parsePagination(c)
	var archProcessID uint64
	if raw := strings.TrimSpace(c.Query("arch_process_id")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "arch_process_id 参数无效"})
			return
		}
		archProcessID = parsed
	}

	uc := commonAuth.GetUserContext(c)
	isAdmin := isCurrentUserAdmin(uc)
	userID := uint(0)
	if uc != nil {
		userID = uc.UserID
	}

	resp, err := topology.ListUnifiedCatalogServices(c.Request.Context(), models.DB, topology.CatalogServicesQuery{
		State:         strings.TrimSpace(c.Query("state")),
		ProtoStatus:   strings.TrimSpace(c.Query("proto_status")),
		ArchProcessID: archProcessID,
		Search:        strings.TrimSpace(c.Query("search")),
		Page:          page,
		PageSize:      pageSize,
	}, userID, isAdmin)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

type correlateCatalogReq struct {
	ServiceIDs []uint64 `json:"service_ids"`
}

// CorrelateCatalogServicesHandler handles POST /api/catalog/correlate (三级 .pb 撮合).
func CorrelateCatalogServicesHandler(c *gin.Context) {
	var req correlateCatalogReq
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: " + err.Error()})
			return
		}
	}

	res, err := topology.CorrelateCatalogServices(c.Request.Context(), models.DB, req.ServiceIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if res.Reloaded {
		if reg, rErr := GetDecoderRegistry(); rErr == nil && reg != nil {
			_ = reg.Reload()
		}
	}

	uc := commonAuth.GetUserContext(c)
	operator := "system"
	if uc != nil {
		operator = uc.Username
	}
	commonAudit.SetAuditContext(
		c,
		"contract_catalog",
		"correlate",
		commonModels.AuditLevelP2,
		fmt.Sprintf("用户 [%s] 执行统一契约目录三级撮合（EXACT=%d，建议=%d）", operator, res.ExactMatched, res.SuggestedCount),
		"catalog",
		"global",
		"catalog_correlate",
		nil,
		res,
	)

	c.JSON(http.StatusOK, res)
}

type confirmCatalogProtoReq struct {
	ProtoServiceName string `json:"proto_service_name" binding:"required"`
}

// ConfirmCatalogServiceProtoHandler handles POST /api/catalog/services/:id/confirm-proto.
func ConfirmCatalogServiceProtoHandler(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的服务 ID"})
		return
	}

	var req confirmCatalogProtoReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: " + err.Error()})
		return
	}

	uc := commonAuth.GetUserContext(c)
	isAdmin := isCurrentUserAdmin(uc)
	userID := uint(0)
	operator := "system"
	if uc != nil {
		userID = uc.UserID
		operator = uc.Username
	}

	ms, auditLevel, err := topology.ConfirmCatalogServiceProto(c.Request.Context(), models.DB, id, req.ProtoServiceName, userID, isAdmin)
	if err != nil {
		switch {
		case errors.Is(err, topology.ErrInvalidProtoServiceNameFormat):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrModuleServiceNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrCatalogForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	if reg, rErr := GetDecoderRegistry(); rErr == nil && reg != nil {
		_ = reg.Reload()
	}

	lvl := commonModels.AuditLevelP2
	if auditLevel == "P1" {
		lvl = commonModels.AuditLevelP1
	}
	commonAudit.SetAuditContext(
		c,
		"contract_catalog",
		"confirm_proto",
		lvl,
		fmt.Sprintf("用户 [%s] 确认服务 [%s] 绑定 Proto 契约 [%s]", operator, ms.ServiceName, ms.ProtoServiceName),
		"module_service",
		strconv.FormatUint(ms.ID, 10),
		ms.ServiceName,
		nil,
		ms,
	)

	c.JSON(http.StatusOK, ms)
}
