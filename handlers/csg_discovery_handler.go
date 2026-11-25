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
	"code-contract/internal/server/discovery"
	"code-contract/internal/server/hub"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"github.com/gin-gonic/gin"
)

// CSGImportBlueprintReq defines POST /api/agents/:id/csg/import-blueprint request JSON.
type CSGImportBlueprintReq struct {
	SubsystemID        FlexUint64  `json:"subsystem_id"`
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

	// M30b V2 开启时：禁止创建任何 CSG_DISCOVERED/MOD_* 假模块，统一委托给零侵扰 Enroll，并附带 Deprecation: true 响应头
	if discovery.IsV2Enabled(models.DB) {
		liveResp, liveErr := GetDiscoveryService().GetLiveDiscovery(c.Request.Context(), id, strings.TrimSpace(req.CSGAddr), false, userID, isAdmin)
		if liveErr != nil {
			respondDiscoveryError(c, liveErr)
			return
		}

		wantSvc := make(map[string]bool)
		for _, sn := range req.ServiceNames {
			trimmed := strings.TrimSpace(sn)
			if trimmed == "" {
				continue
			}
			wantSvc[trimmed] = true
			wantSvc[topology.StripCSGPIDSuffix(trimmed)] = true
		}

		items := make([]discovery.EnrollItemInput, 0, len(liveResp.Processes))
		for _, p := range liveResp.Processes {
			if p.IsRemoteLDC {
				continue
			}
			if len(wantSvc) > 0 {
				matched := false
				for _, s := range p.Services {
					if wantSvc[s.ServiceName] || wantSvc[s.RawServiceName] {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
			items = append(items, discovery.EnrollItemInput{
				ProcessKey:           p.ProcessKey,
				ConfirmLowConfidence: true,
			})
		}

		enrollRes, enrollErr := discovery.Enroll(c.Request.Context(), models.DB, GetDiscoveryService(), discovery.EnrollInput{
			AgentID:         id,
			CSGAddr:         strings.TrimSpace(req.CSGAddr),
			Items:           items,
			OperatorID:      userID,
			IsAdmin:         isAdmin,
			BypassRateLimit: true,
		})
		if enrollErr != nil {
			var valErr *discovery.EnrollValidationError
			if errors.As(enrollErr, &valErr) {
				c.JSON(http.StatusUnprocessableEntity, valErr)
				return
			}
			respondDiscoveryError(c, enrollErr)
			return
		}

		c.Header("Deprecation", "true")
		commonAudit.SetAuditContext(
			c,
			"agents",
			"csg_import_blueprint",
			commonModels.AuditLevelP1,
			fmt.Sprintf("管理员 [%s] 通过已废弃 import-blueprint 在 Agent #%d 委托物理纳管 %d 个进程（零假模块）", operator, id, len(enrollRes.Enrolled)),
			"csg_blueprint",
			strconv.FormatUint(id, 10),
			fmt.Sprintf("agent_%d", id),
			nil,
			enrollRes,
		)
		c.JSON(http.StatusOK, gin.H{
			"deprecated":         true,
			"imported_processes": len(enrollRes.Enrolled),
			"enrolled":           enrollRes.Enrolled,
			"placements_count":   enrollRes.PlacementsCount,
			"dependencies_count": enrollRes.DependenciesCount,
			"created_modules":    0,
			"snapshot_id":        enrollRes.SnapshotID,
		})
		return
	}

	if uint64(req.SubsystemID) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: subsystem_id 必填"})
		return
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
