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
	"code-contract/internal/server/hub"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"github.com/gin-gonic/gin"
)

// CSGImportBlueprintReq defines POST /api/agents/:id/csg/import-blueprint request JSON.
type CSGImportBlueprintReq struct {
	SubsystemID        FlexUint64  `json:"subsystem_id" binding:"required"`
	GroupID            *FlexUint64 `json:"group_id"`
	ProcessGroupID     *FlexUint64 `json:"process_group_id"`
	CSGAddr            string      `json:"csg_addr"`
	ServiceNames       []string    `json:"service_names"`
	ImportDependencies *bool       `json:"import_dependencies"`
}

// GetAgentCSGTopology queries Agent live CSG topology and performs 3-state SSOT reconciliation.
// GET /api/agents/:id/csg/topology
func GetAgentCSGTopology(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 Agent ID"})
		return
	}

	uc := commonAuth.GetUserContext(c)
	isAdmin := isCurrentUserAdmin(uc)
	userID := uint(0)
	if uc != nil {
		userID = uc.UserID
	}

	csgAddr := strings.TrimSpace(c.Query("csg_addr"))
	live, err := GetHubManager().FetchAgentCSGTopology(c.Request.Context(), id, csgAddr, userID, isAdmin)
	if err != nil {
		switch {
		case errors.Is(err, hub.ErrAgentNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, hub.ErrAgentForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		}
		return
	}

	reconciled, err := topology.ReconcileLiveCSGWithSSOT(models.DB, id, live)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, reconciled)
}

// ImportAgentCSGBlueprint idempotently imports live CSG services and dependencies into SSOT (Admin only).
// POST /api/agents/:id/csg/import-blueprint
func ImportAgentCSGBlueprint(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 Agent ID"})
		return
	}

	var req CSGImportBlueprintReq
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

	live, err := GetHubManager().FetchAgentCSGTopology(c.Request.Context(), id, strings.TrimSpace(req.CSGAddr), userID, isAdmin)
	if err != nil {
		switch {
		case errors.Is(err, hub.ErrAgentNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, hub.ErrAgentForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		}
		return
	}

	importDeps := true
	if req.ImportDependencies != nil {
		importDeps = *req.ImportDependencies
	}
	var grpID *uint64
	if req.GroupID != nil {
		v := uint64(*req.GroupID)
		grpID = &v
	}
	var pgrpID *uint64
	if req.ProcessGroupID != nil {
		v := uint64(*req.ProcessGroupID)
		pgrpID = &v
	}

	res, err := topology.ImportCSGBlueprint(models.DB, topology.CSGBlueprintImportInput{
		AgentID:            id,
		SubsystemID:        uint64(req.SubsystemID),
		GroupID:            grpID,
		ProcessGroupID:     pgrpID,
		ServiceNames:       req.ServiceNames,
		ImportDependencies: importDeps,
		OperatorID:         userID,
		Live:               live,
	})
	if err != nil {
		var layerErr *topology.LayerViolationError
		var cycleErr *topology.CyclicDependencyError
		switch {
		case errors.Is(err, topology.ErrAgentNotFound),
			errors.Is(err, topology.ErrSubsystemNotFound),
			errors.Is(err, topology.ErrInvalidGroupTarget):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.As(err, &layerErr):
			c.JSON(http.StatusConflict, layerErr)
		case errors.As(err, &cycleErr):
			c.JSON(http.StatusConflict, cycleErr)
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	commonAudit.SetAuditContext(
		c,
		"agents",
		"csg_import_blueprint",
		commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 从 Agent #%d 导入 CSG 拓扑（新增服务 %d，新增依赖 %d）", operator, id, res.CreatedServices, res.CreatedDependencies),
		"csg_blueprint",
		strconv.FormatUint(id, 10),
		fmt.Sprintf("agent_%d", id),
		nil,
		res,
	)

	c.JSON(http.StatusOK, res)
}
