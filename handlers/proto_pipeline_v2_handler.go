package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	commonModels "code-common/backend/models"
	"code-contract/config"
	"code-contract/internal/server/gitclient"
	"code-contract/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func handlePipelineLockOrStaleError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	var lockErr *gitclient.PipelineLockError
	if errors.As(err, &lockErr) || errors.Is(err, gitclient.ErrPipelineLocked) {
		holder := (*gitclient.PipelineLockHolder)(nil)
		if lockErr != nil {
			holder = lockErr.Holder
		} else {
			holder = gitclient.GetActivePipelineLockHolder()
		}
		c.JSON(http.StatusConflict, gin.H{
			"code":        "PIPELINE_LOCKED",
			"err_code":    "PIPELINE_LOCKED",
			"error":       err.Error(),
			"lock_holder": holder,
		})
		return true
	}
	if errors.Is(err, gitclient.ErrReconcileStale) {
		c.JSON(http.StatusConflict, gin.H{
			"code":     "RECONCILE_STALE",
			"err_code": "RECONCILE_STALE",
			"error":    err.Error(),
		})
		return true
	}
	return false
}

// GetProtoPipelineOverviewHandler GET /api/proto-pipeline/overview
func GetProtoPipelineOverviewHandler(c *gin.Context) {
	overview, err := gitclient.BuildPipelineOverview(c.Request.Context(), models.DB, &config.AppConfig)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询四阶段流水线概览失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, overview)
}

// RunProtoPipelineHandler POST /api/proto-pipeline/run (AdminOnly)
func RunProtoPipelineHandler(c *gin.Context) {
	var req struct {
		FromStage      string `json:"from_stage"`
		CachePolicy    string `json:"cache_policy"`
		ForceRecompile bool   `json:"force_recompile"`
		Sync           bool   `json:"sync"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法: " + err.Error()})
		return
	}
	fromStage := strings.ToUpper(strings.TrimSpace(req.FromStage))
	if fromStage == "" {
		fromStage = models.PipelineStageCollectNormalize
	}
	switch fromStage {
	case models.PipelineStageCollectNormalize, models.PipelineStageReconcile, models.PipelineStagePublish:
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的 from_stage，仅支持 COLLECT_NORMALIZE, RECONCILE, PUBLISH"})
		return
	}

	uid, opName := currentOperatorName(c)
	decReg, err := GetDecoderRegistry()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	broker := GetGlobalSSEBroker()
	sched := EnsureSyncScheduler(models.DB, decReg, broker)

	runReq := gitclient.FullPipelineRunRequest{
		FromStage:      fromStage,
		CachePolicy:    strings.TrimSpace(req.CachePolicy),
		ForceRecompile: req.ForceRecompile,
		TriggerSource:  models.PipelineTriggerManual,
		TriggeredBy:    opName,
		OperatorID:     uid,
		WaitSync:       req.Sync,
		ProgressFn: func(evt gitclient.PipelineProgressEvent) {
			if broker != nil {
				broker.BroadcastProtoPipelineProgress(evt)
			}
		},
		PublishProgressFn: func(evt gitclient.PublishProgressEvent) {
			if broker != nil {
				broker.BroadcastProtoPublishProgress(evt)
			}
		},
		ReconcileProgFn: func(evt gitclient.PoolReconcileProgressEvent) {
			if broker != nil {
				broker.BroadcastProtoPoolReconcileProgress(evt)
			}
		},
	}

	run, runErr := gitclient.RunFullProtoPipeline(c.Request.Context(), models.DB, sched, decReg, &config.AppConfig, runReq)
	if runErr != nil {
		if handlePipelineLockOrStaleError(c, runErr) {
			return
		}
		if run != nil && run.ID > 0 {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":        runErr.Error(),
				"pipeline_run": run,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": runErr.Error()})
		return
	}

	recordAudit(c, "contract_proto_pipeline:run", commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 触发四阶段流水线 #%d (from_stage=%s, sync=%v)", opName, run.ID, run.FromStage, req.Sync),
		"proto_pipeline_run", run.ID, run.FromStage, nil, run)

	statusCode := http.StatusAccepted
	if req.Sync {
		statusCode = http.StatusOK
	}
	c.JSON(statusCode, gin.H{
		"pipeline_run": run,
		"lock_holder":  gitclient.GetActivePipelineLockHolder(),
	})
}

// GetProtoPipelineRunHandler GET /api/proto-pipeline/runs/:runId
func GetProtoPipelineRunHandler(c *gin.Context) {
	runID, err := strconv.ParseUint(c.Param("runId"), 10, 64)
	if err != nil || runID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的流水线运行 ID"})
		return
	}
	var run models.ProtoPipelineRun
	if err := models.DB.WithContext(c.Request.Context()).First(&run, runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "未找到指定的流水线运行记录"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	resp := gin.H{
		"pipeline_run": run,
		"lock_holder":  gitclient.GetActivePipelineLockHolder(),
	}
	if run.SyncJobID != nil && *run.SyncJobID > 0 {
		var sj models.ProtoSyncJob
		if err := models.DB.WithContext(c.Request.Context()).First(&sj, *run.SyncJobID).Error; err == nil {
			resp["sync_job"] = sj
		}
	}
	if run.ReconcileRunID != nil && *run.ReconcileRunID > 0 {
		var rr models.ProtoPoolReconcileRun
		if err := models.DB.WithContext(c.Request.Context()).First(&rr, *run.ReconcileRunID).Error; err == nil {
			resp["reconcile_run"] = rr
		}
	}
	if run.PublishRunID != nil && *run.PublishRunID > 0 {
		var pr models.ProtoPublishRun
		if err := models.DB.WithContext(c.Request.Context()).First(&pr, *run.PublishRunID).Error; err == nil {
			resp["publish_run"] = pr
			if len(pr.DetailsJSON) > 0 {
				var details gitclient.PublishRunDetails
				if json.Unmarshal(pr.DetailsJSON, &details) == nil {
					resp["publish_details"] = details
				}
			}
		}
	}
	c.JSON(http.StatusOK, resp)
}

// PublishProtoPipelineHandler POST /api/proto-pipeline/publish (AdminOnly)
func PublishProtoPipelineHandler(c *gin.Context) {
	var req struct {
		BundleIDs      []uint64 `json:"bundle_ids"`
		Subsystem      string   `json:"subsystem"`
		ForceRecompile bool     `json:"force_recompile"`
		PublishMode    string   `json:"publish_mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法: " + err.Error()})
		return
	}
	uid, opName := currentOperatorName(c)
	decReg, err := GetDecoderRegistry()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	broker := GetGlobalSSEBroker()
	run, details, pubErr := gitclient.PublishWorkspaceBundles(c.Request.Context(), models.DB, decReg, &config.AppConfig, gitclient.PublishWorkspaceOptions{
		BundleIDs:      req.BundleIDs,
		Subsystem:      strings.TrimSpace(req.Subsystem),
		ForceRecompile: req.ForceRecompile,
		PublishMode:    strings.TrimSpace(req.PublishMode),
		TriggeredBy:    opName,
		OperatorID:     uid,
		ProgressFn: func(evt gitclient.PublishProgressEvent) {
			if broker != nil {
				broker.BroadcastProtoPublishProgress(evt)
			}
		},
	})
	if pubErr != nil {
		if handlePipelineLockOrStaleError(c, pubErr) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": pubErr.Error(), "run": run, "details": details})
		return
	}

	recordAudit(c, "contract_proto_pipeline:publish", commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 执行阶段 ④ 拓扑编译发布 #%d (status=%s, published=%d, blocking=%d)",
			opName, run.ID, run.Status, run.PublishedCount, run.BreakingBlockedCount),
		"proto_publish_run", run.ID, run.PublishMode, nil, run)

	c.JSON(http.StatusOK, gin.H{
		"run":     run,
		"details": details,
	})
}

func respondWithPublishRun(c *gin.Context, run *models.ProtoPublishRun) {
	var details gitclient.PublishRunDetails
	if len(run.DetailsJSON) > 0 {
		_ = json.Unmarshal(run.DetailsJSON, &details)
	}
	c.JSON(http.StatusOK, gin.H{
		"run":     run,
		"details": details,
	})
}

// GetLatestProtoPublishRunHandler GET /api/proto-pipeline/publish-runs/latest
func GetLatestProtoPublishRunHandler(c *gin.Context) {
	var run models.ProtoPublishRun
	if err := models.DB.WithContext(c.Request.Context()).Order("id DESC").First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusOK, gin.H{"run": nil, "details": nil})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	respondWithPublishRun(c, &run)
}

// GetProtoPublishRunHandler GET /api/proto-pipeline/publish-runs/:runId
func GetProtoPublishRunHandler(c *gin.Context) {
	runID, err := strconv.ParseUint(c.Param("runId"), 10, 64)
	if err != nil || runID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的发布批次 ID"})
		return
	}
	var run models.ProtoPublishRun
	if err := models.DB.WithContext(c.Request.Context()).First(&run, runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gitclient.ErrPublishRunNotFound.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	respondWithPublishRun(c, &run)
}

// ApproveBreakingPublishRunHandler POST /api/proto-pipeline/publish-runs/:runId/approve-breaking (AdminOnly)
func ApproveBreakingPublishRunHandler(c *gin.Context) {
	runID, err := strconv.ParseUint(c.Param("runId"), 10, 64)
	if err != nil || runID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的发布批次 ID"})
		return
	}
	var req struct {
		SchemaIDs      []uint64 `json:"schema_ids"`
		BreakingReason string   `json:"breaking_reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法: " + err.Error()})
		return
	}
	if len(req.SchemaIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "待放行的 schema_ids 不能为空"})
		return
	}
	if len([]rune(strings.TrimSpace(req.BreakingReason))) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gitclient.ErrInvalidBreakingReason.Error()})
		return
	}

	uid, opName := currentOperatorName(c)
	decReg, err := GetDecoderRegistry()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	res, aErr := gitclient.ApproveBreakingSchemas(
		c.Request.Context(),
		models.DB,
		decReg,
		&config.AppConfig,
		runID,
		req.SchemaIDs,
		strings.TrimSpace(req.BreakingReason),
		opName,
		uid,
	)
	if aErr != nil {
		if handlePipelineLockOrStaleError(c, aErr) {
			return
		}
		if errors.Is(aErr, gitclient.ErrPublishRunNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": aErr.Error()})
			return
		}
		if errors.Is(aErr, gitclient.ErrInvalidBreakingReason) || strings.Contains(aErr.Error(), "不在本批次") {
			c.JSON(http.StatusBadRequest, gin.H{"error": aErr.Error()})
			return
		}
		writeProtoMutationError(c, aErr)
		return
	}

	recordAudit(c, "contract_proto_pipeline:approve_breaking", commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 确认放行发布批次 #%d 破坏性变更并自动触发下游补发 (原因: %s)",
			opName, runID, strings.TrimSpace(req.BreakingReason)),
		"proto_publish_run", runID, "approve_breaking", nil, res)

	c.JSON(http.StatusOK, res)
}

// ConfirmPreviewPublishRunHandler POST /api/proto-pipeline/publish-runs/:runId/confirm (AdminOnly)
func ConfirmPreviewPublishRunHandler(c *gin.Context) {
	runID, err := strconv.ParseUint(c.Param("runId"), 10, 64)
	if err != nil || runID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的发布批次 ID"})
		return
	}
	uid, opName := currentOperatorName(c)
	decReg, err := GetDecoderRegistry()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	run, details, cErr := gitclient.ConfirmPreviewPublishRun(c.Request.Context(), models.DB, decReg, &config.AppConfig, runID, opName, uid)
	if cErr != nil {
		if handlePipelineLockOrStaleError(c, cErr) {
			return
		}
		if errors.Is(cErr, gitclient.ErrPublishRunNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": cErr.Error()})
			return
		}
		if errors.Is(cErr, gitclient.ErrPublishRunNotPreviewReady) {
			c.JSON(http.StatusConflict, gin.H{"code": "PREVIEW_NOT_READY", "error": cErr.Error()})
			return
		}
		writeProtoMutationError(c, cErr)
		return
	}

	recordAudit(c, "contract_proto_pipeline:confirm_preview", commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 确认将预览发布批次 #%d 正式切入生效池", opName, runID),
		"proto_publish_run", runID, "confirm_preview", nil, run)

	c.JSON(http.StatusOK, gin.H{
		"run":     run,
		"details": details,
	})
}

// ListArchivedExternalSchemasHandler GET /api/proto-pipeline/archived-external-schemas
func ListArchivedExternalSchemasHandler(c *gin.Context) {
	items, err := gitclient.ListArchivedExternalSchemas(c.Request.Context(), models.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询历史归档外部来源 Schema 失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
