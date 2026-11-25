package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	globalAttrWorker   *topology.AttributionJobWorker
	globalAttrWorkerMu sync.RWMutex
)

// InitGlobalAttributionJobWorker 初始化全局可续跑归因回补 Worker
func InitGlobalAttributionJobWorker(db *gorm.DB, dec *decoder.Registry, broadcaster topology.EventBroadcaster) *topology.AttributionJobWorker {
	globalAttrWorkerMu.Lock()
	defer globalAttrWorkerMu.Unlock()
	globalAttrWorker = topology.NewAttributionJobWorker(db, dec, broadcaster)
	return globalAttrWorker
}

// GetGlobalAttributionJobWorker 获取全局归因回补 Worker
func GetGlobalAttributionJobWorker() *topology.AttributionJobWorker {
	globalAttrWorkerMu.RLock()
	defer globalAttrWorkerMu.RUnlock()
	return globalAttrWorker
}

// NotifyAttributionJobWorker 唤醒全局归因回补 Worker 处理新任务
func NotifyAttributionJobWorker() {
	if w := GetGlobalAttributionJobWorker(); w != nil {
		w.Notify()
	}
}

// GetAttributionJobHandler handles GET /api/attribution-jobs/:id.
func GetAttributionJobHandler(c *gin.Context) {
	jobID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || jobID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的归因作业 ID"})
		return
	}

	dto, err := topology.GetAttributionJobDTO(c.Request.Context(), models.DB, jobID)
	if err != nil {
		if errors.Is(err, topology.ErrAttributionJobNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, dto)
}

// CancelAttributionJobHandler handles POST /api/attribution-jobs/:id/cancel.
func CancelAttributionJobHandler(c *gin.Context) {
	jobID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || jobID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的归因作业 ID"})
		return
	}

	op, operatorName := currentTopologyOperator(c)
	dto, err := topology.CancelAttributionJob(c.Request.Context(), models.DB, op, jobID)
	if err != nil {
		switch {
		case errors.Is(err, topology.ErrAttributionJobNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrAttributionJobForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrAttributionJobTerminal):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	commonAudit.SetAuditContext(
		c,
		"contract_arch",
		"cancel_attribution_job",
		commonModels.AuditLevelP1,
		fmt.Sprintf("用户 [%s] 取消阶段 2 历史归因回补作业 #%d", operatorName, jobID),
		"attribution_job",
		strconv.FormatUint(jobID, 10),
		fmt.Sprintf("job_%d", jobID),
		nil,
		dto,
	)
	c.JSON(http.StatusOK, dto)
}
