package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	commonModels "code-common/backend/models"
	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/gitclient"
	"code-contract/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type triggerPoolReconcileRequest struct {
	ClosureOnly        *bool  `json:"closure_only"`
	EnableR7AutoFix    *bool  `json:"enable_r7_auto_fix"`
	MaxR7Rounds        int    `json:"max_r7_rounds"`
	EnableR8Quarantine *bool  `json:"enable_r8_quarantine"`
	MaxR8Rounds        int    `json:"max_r8_rounds"`
	CodesDir           string `json:"codes_dir"`
	Sync               bool   `json:"sync"`
}

// TriggerPoolReconcileHandler POST /api/proto-governance/reconcile-pool
func TriggerPoolReconcileHandler(c *gin.Context) {
	var req triggerPoolReconcileRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法: " + err.Error()})
		return
	}

	_, opName := currentOperatorName(c)
	snap, err := gitclient.LoadActiveGovernanceConfig(c.Request.Context(), models.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "加载治理配置失败: " + err.Error()})
		return
	}
	govDoc := snap.Doc
	prCfg := govDoc.Global.EffectivePoolReconcile()

	closureOnly := prCfg.EffectiveClosureOnly()
	if req.ClosureOnly != nil {
		closureOnly = *req.ClosureOnly
	}
	enableR7 := prCfg.EffectiveEnableR7()
	if req.EnableR7AutoFix != nil {
		enableR7 = *req.EnableR7AutoFix
	}
	enableR8 := prCfg.EffectiveEnableR8()
	if req.EnableR8Quarantine != nil {
		enableR8 = *req.EnableR8Quarantine
	}
	maxR7 := prCfg.EffectiveMaxR7Rounds()
	if req.MaxR7Rounds > 0 {
		maxR7 = req.MaxR7Rounds
	}
	maxR8 := prCfg.EffectiveMaxR8Rounds()
	if req.MaxR8Rounds > 0 {
		maxR8 = req.MaxR8Rounds
	}
	codesDir := strings.TrimSpace(req.CodesDir)
	if codesDir == "" {
		codesDir = config.AppConfig.Storage.GetCodesDir()
	}

	broker := GetGlobalSSEBroker()
	opts := gitclient.PoolReconcileOptions{
		ClosureOnly:        closureOnly,
		EnableR7AutoFix:    enableR7,
		MaxR7Rounds:        maxR7,
		EnableR8Quarantine: enableR8,
		MaxR8Rounds:        maxR8,
		StripPGSegment:     govDoc.Global.EffectiveStripPGSegment(),
		GovernanceConfig:   &govDoc,
		CodesDir:           codesDir,
		ProtosDir:          config.AppConfig.Storage.GetProtosDir(),
		TriggeredBy:        opName,
		ProgressFn: func(evt gitclient.PoolReconcileProgressEvent) {
			if broker != nil {
				broker.BroadcastProtoPoolReconcileProgress(evt)
			}
		},
	}

	if req.Sync {
		run, report, rErr := gitclient.RunPoolReconcile(c.Request.Context(), models.DB, opts)
		if rErr != nil {
			if handlePipelineLockOrStaleError(c, rErr) {
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "全池协调执行失败: " + rErr.Error()})
			return
		}
		recordAudit(c, "contract_git:pool_reconcile", commonModels.AuditLevelP2,
			fmt.Sprintf("同步执行全池 Stage C+ 协调 #%d（闭包=%d，自愈=%d，复活=%d，隔离=%d，余错=%d）",
				run.ID, run.ClosureFileCount, run.AutoFixCount, report.Stats.RevivedCount, run.QuarantinedCount, report.Stats.CompileErrorCount),
			"proto_pool_reconcile", run.ID, run.WorkspaceVersion, nil, report.Stats)
		c.JSON(http.StatusOK, gin.H{
			"run":    run,
			"report": report,
		})
		return
	}

	db := models.DB
	go func() {
		_, _, _ = gitclient.RunPoolReconcile(context.Background(), db, opts)
	}()
	recordAudit(c, "contract_git:pool_reconcile", commonModels.AuditLevelP2,
		"异步触发全池 Stage C+ 协调与 workspace/ 导出",
		"proto_pool_reconcile", 0, "async", nil, req)

	c.JSON(http.StatusAccepted, gin.H{
		"status":  "ACCEPTED",
		"message": "全池编译收敛任务已启动，请通过 SSE proto_pool_reconcile.progress 或最新运行接口追踪结果",
	})
}

func buildPoolRunPayload(run *models.ProtoPoolReconcileRun) gin.H {
	if run == nil {
		return gin.H{"run": nil, "report": nil}
	}
	var report gitclient.PoolReconcileReport
	if len(run.ReportJSON) > 0 {
		_ = json.Unmarshal(run.ReportJSON, &report)
	}
	return gin.H{
		"run":    run,
		"report": report,
	}
}

// GetLatestPoolReconcileRunHandler GET /api/proto-governance/reconcile-runs/latest
func GetLatestPoolReconcileRunHandler(c *gin.Context) {
	var run models.ProtoPoolReconcileRun
	err := models.DB.WithContext(c.Request.Context()).Order("id DESC").Limit(1).Find(&run).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if run.ID == 0 {
		c.JSON(http.StatusOK, gin.H{"run": nil, "report": nil})
		return
	}
	c.JSON(http.StatusOK, buildPoolRunPayload(&run))
}

// GetPoolReconcileRunHandler GET /api/proto-governance/reconcile-runs/:id
func GetPoolReconcileRunHandler(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的运行记录 ID"})
		return
	}
	var run models.ProtoPoolReconcileRun
	if err := models.DB.WithContext(c.Request.Context()).First(&run, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "协调运行记录不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, buildPoolRunPayload(&run))
}

type promoteAutoFixItem struct {
	FilePath  string `json:"file_path"`
	FixKind   string `json:"fix_kind"`
	OldImport string `json:"old_import,omitempty"`
	NewImport string `json:"new_import"`
	RepoID    uint64 `json:"repo_id,omitempty"`
	BundleID  uint64 `json:"bundle_id,omitempty"`
}

type promoteAutoFixesRequest struct {
	RunID uint64               `json:"run_id,omitempty"`
	Fixes []promoteAutoFixItem `json:"fixes"`
}

// PromotePoolAutoFixesHandler POST /api/proto-governance/auto-fixes/promote
func PromotePoolAutoFixesHandler(c *gin.Context) {
	var req promoteAutoFixesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法: " + err.Error()})
		return
	}
	if len(req.Fixes) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "fixes 不能为空"})
		return
	}

	_, opName := currentOperatorName(c)
	snap, err := gitclient.LoadActiveGovernanceConfig(c.Request.Context(), models.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	doc := snap.Doc
	if doc.Global.ImportRewrites == nil {
		doc.Global.ImportRewrites = make(map[string]string)
	}

	promotedRewrites := 0
	promotedOwners := 0
	needSaveDoc := false

	for _, fx := range req.Fixes {
		kind := strings.ToLower(strings.TrimSpace(fx.FixKind))
		if kind == "variant_swap" {
			if fx.BundleID == 0 || strings.TrimSpace(fx.FilePath) == "" {
				continue
			}
			cleanedPath, sErr := decoder.SanitizeProtoRelPath(fx.FilePath, true)
			if sErr != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": sErr.Error()})
				return
			}
			repoID := fx.RepoID
			if repoID == 0 {
				var bundle models.GitProtoBundle
				if bErr := models.DB.WithContext(c.Request.Context()).Select("id, git_repo_id").First(&bundle, fx.BundleID).Error; bErr == nil {
					repoID = bundle.GitRepoID
				}
			}
			if _, rErr := gitclient.ResolveProtoConflict(c.Request.Context(), models.DB, opName, gitclient.ResolveConflictInput{
				Kind:           "IMPORT",
				LogicalPath:    cleanedPath,
				OwnerGitRepoID: repoID,
				OwnerBundleID:  fx.BundleID,
			}); rErr != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": rErr.Error()})
				return
			}
			promotedOwners++
			continue
		}

		// fix(a) / fix(b) / fix(c) -> 固化为 global.import_rewrites 持久治理规则
		newImp := strings.TrimSpace(fx.NewImport)
		if newImp == "" {
			continue
		}
		ruleKey := strings.TrimSpace(fx.OldImport)
		if ruleKey == "" {
			// fix(b) 无旧 import 时以 "<file_path>+import" 作为确定性附加规则键
			fp := strings.TrimSpace(fx.FilePath)
			if fp == "" {
				continue
			}
			ruleKey = fp + "+import:" + newImp
		}
		if doc.Global.ImportRewrites[ruleKey] != newImp {
			doc.Global.ImportRewrites[ruleKey] = newImp
			promotedRewrites++
			needSaveDoc = true
		}
	}

	rev := doc.Revision
	if needSaveDoc {
		newSnap, _, cols, sErr := gitclient.SaveGovernanceConfig(c.Request.Context(), models.DB, doc, opName)
		if sErr != nil {
			if errors.Is(sErr, gitclient.ErrExportTargetConflict) {
				c.JSON(http.StatusConflict, gin.H{
					"err_code":   "EXPORT_TARGET_CONFLICT",
					"error":      sErr.Error(),
					"collisions": cols,
				})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": sErr.Error()})
			return
		}
		if newSnap != nil {
			rev = newSnap.Doc.Revision
		}
	}

	recordAudit(c, "contract_git:promote_auto_fixes", commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 固化 R7 修复规则：import_rewrites +%d, manual_owners +%d",
			opName, promotedRewrites, promotedOwners),
		"proto_governance", req.RunID, fmt.Sprintf("rev-%d", rev), snap.Doc.Global.ImportRewrites, doc.Global.ImportRewrites)

	c.JSON(http.StatusOK, gin.H{
		"promoted_import_rewrites": promotedRewrites,
		"promoted_manual_owners":   promotedOwners,
		"governance_revision":      rev,
	})
}
