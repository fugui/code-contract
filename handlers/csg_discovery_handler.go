package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	commonAuth "code-common/backend/auth"
	"code-contract/internal/server/hub"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"github.com/gin-gonic/gin"
)

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
