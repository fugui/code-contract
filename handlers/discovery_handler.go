package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	commonAudit "code-common/backend/audit"
	commonAuth "code-common/backend/auth"
	commonModels "code-common/backend/models"
	"code-contract/internal/server/discovery"
	"code-contract/internal/server/hub"
	"code-contract/internal/server/recorder"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"github.com/gin-gonic/gin"
)

var (
	discoverySvcMu     sync.RWMutex
	globalDiscoverySvc *discovery.LiveDiscoveryService
)

// GetDiscoveryService returns or lazily initializes the global LiveDiscoveryService.
func GetDiscoveryService() *discovery.LiveDiscoveryService {
	discoverySvcMu.RLock()
	svc := globalDiscoverySvc
	discoverySvcMu.RUnlock()
	if svc != nil {
		return svc
	}

	discoverySvcMu.Lock()
	defer discoverySvcMu.Unlock()
	if globalDiscoverySvc == nil {
		globalDiscoverySvc = discovery.NewLiveDiscoveryService(models.DB, GetHubManager())
	}
	return globalDiscoverySvc
}

// ResetGlobalDiscoveryService resets the global LiveDiscoveryService (used by tests).
func ResetGlobalDiscoveryService(svc *discovery.LiveDiscoveryService) {
	discoverySvcMu.Lock()
	globalDiscoverySvc = svc
	discoverySvcMu.Unlock()
}

func respondDiscoveryError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, discovery.ErrDiscoveryV2Disabled):
		c.JSON(http.StatusNotFound, gin.H{"error": "现网发现 V2 功能未开启"})
	case errors.Is(err, discovery.ErrAgentNotFound), errors.Is(err, discovery.ErrSnapshotNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, discovery.ErrAgentForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问或操作该私有环境资源"})
	case errors.Is(err, discovery.ErrRefreshRateLimited):
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "手动刷新过于频繁，请 3 秒后重试"})
	case errors.Is(err, discovery.ErrEnrollRateLimited):
		c.JSON(http.StatusTooManyRequests, gin.H{"code": "RATE_LIMITED", "error": "物理纳管请求过于频繁，请 5 秒后重试"})
	case errors.Is(err, discovery.ErrEnrollBatchLimit):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "ENROLL_BATCH_LIMIT", "error": err.Error()})
	case errors.Is(err, discovery.ErrEnrollTargetMissing):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, discovery.ErrPinLimitExceeded):
		c.JSON(http.StatusBadRequest, gin.H{"error": "该环境下置顶快照数量已达上限 (20)"})
	case errors.Is(err, discovery.ErrSnapshotPinned):
		c.JSON(http.StatusConflict, gin.H{"error": "已置顶快照禁止直接删除，请先取消置顶"})
	case errors.Is(err, discovery.ErrAgentUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Agent 离线且暂无历史持久化快照"})
	case errors.Is(err, discovery.ErrInvalidDiffTarget):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

// GetDiscoveryLive handles GET /api/discovery/live.
func GetDiscoveryLive(c *gin.Context) {
	agentID, err := strconv.ParseUint(strings.TrimSpace(c.Query("agent_id")), 10, 64)
	if err != nil || agentID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent_id 参数无效"})
		return
	}

	csgAddr := strings.TrimSpace(c.Query("csg_addr"))
	refresh := strings.EqualFold(strings.TrimSpace(c.Query("refresh")), "true") || strings.TrimSpace(c.Query("refresh")) == "1"

	uc := commonAuth.GetUserContext(c)
	isAdmin := isCurrentUserAdmin(uc)
	userID := uint(0)
	if uc != nil {
		userID = uc.UserID
	}

	resp, err := GetDiscoveryService().GetLiveDiscovery(c.Request.Context(), agentID, csgAddr, refresh, userID, isAdmin)
	if err != nil {
		respondDiscoveryError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// ListDiscoverySnapshots handles GET /api/discovery/snapshots.
func ListDiscoverySnapshots(c *gin.Context) {
	agentID, err := strconv.ParseUint(strings.TrimSpace(c.Query("agent_id")), 10, 64)
	if err != nil || agentID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent_id 参数无效"})
		return
	}

	pinnedOnly := strings.EqualFold(strings.TrimSpace(c.Query("pinned_only")), "true") || strings.TrimSpace(c.Query("pinned_only")) == "1"
	page, pageSize := parsePagination(c)

	uc := commonAuth.GetUserContext(c)
	isAdmin := isCurrentUserAdmin(uc)
	userID := uint(0)
	if uc != nil {
		userID = uc.UserID
	}

	items, total, err := discovery.ListSnapshots(models.DB, discovery.ListSnapshotsQuery{
		AgentID:    agentID,
		CSGAddr:    strings.TrimSpace(c.Query("csg_addr")),
		PinnedOnly: pinnedOnly,
		Page:       page,
		PageSize:   pageSize,
	}, userID, isAdmin)
	if err != nil {
		respondDiscoveryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

type createDiscoverySnapshotReq struct {
	AgentID  FlexUint64 `json:"agent_id" binding:"required"`
	CSGAddr  string     `json:"csg_addr"`
	Label    string     `json:"label"`
	IsPinned bool       `json:"is_pinned"`
}

// CreateDiscoverySnapshot handles POST /api/discovery/snapshots.
func CreateDiscoverySnapshot(c *gin.Context) {
	var req createDiscoverySnapshotReq
	if err := c.ShouldBindJSON(&req); err != nil || uint64(req.AgentID) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误"})
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

	agentID := uint64(req.AgentID)
	csgAddr := strings.TrimSpace(req.CSGAddr)

	live, err := GetDiscoveryService().GetLiveDiscovery(c.Request.Context(), agentID, csgAddr, false, userID, isAdmin)
	if err != nil {
		respondDiscoveryError(c, err)
		return
	}

	saveRes, err := discovery.SaveSnapshot(models.DB, discovery.SaveSnapshotInput{
		AgentID:     agentID,
		CSGAddr:     csgAddr,
		TriggerMode: models.DiscoveryTriggerManual,
		Label:       req.Label,
		IsPinned:    req.IsPinned,
		CreatedBy:   userID,
		IsAdmin:     isAdmin,
		Live:        live,
	})
	if err != nil {
		respondDiscoveryError(c, err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		"contract_discovery",
		"save_snapshot",
		commonModels.AuditLevelP2,
		fmt.Sprintf("用户 [%s] 保存 Agent #%d 现网发现快照 #%d", operator, agentID, saveRes.Snapshot.ID),
		"discovery_snapshot",
		strconv.FormatUint(saveRes.Snapshot.ID, 10),
		fmt.Sprintf("agent_%d_snap_%d", agentID, saveRes.Snapshot.ID),
		nil,
		saveRes.Snapshot,
	)

	c.JSON(http.StatusCreated, saveRes.Snapshot)
}

type updateDiscoverySnapshotReq struct {
	Label    *string `json:"label"`
	IsPinned *bool   `json:"is_pinned"`
}

// UpdateDiscoverySnapshot handles PATCH /api/discovery/snapshots/:id.
func UpdateDiscoverySnapshot(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的快照 ID"})
		return
	}

	var req updateDiscoverySnapshotReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误"})
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

	updated, err := discovery.UpdateSnapshot(models.DB, id, discovery.UpdateSnapshotInput{
		Label:    req.Label,
		IsPinned: req.IsPinned,
	}, userID, isAdmin)
	if err != nil {
		respondDiscoveryError(c, err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		"contract_discovery",
		"update_snapshot",
		commonModels.AuditLevelP2,
		fmt.Sprintf("用户 [%s] 更新现网快照 #%d（label=%q, pinned=%t）", operator, id, updated.Label, updated.IsPinned),
		"discovery_snapshot",
		strconv.FormatUint(id, 10),
		fmt.Sprintf("snap_%d", id),
		nil,
		updated,
	)

	c.JSON(http.StatusOK, updated)
}

// DeleteDiscoverySnapshot handles DELETE /api/discovery/snapshots/:id.
func DeleteDiscoverySnapshot(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的快照 ID"})
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

	if err := discovery.DeleteSnapshot(models.DB, id, userID, isAdmin); err != nil {
		respondDiscoveryError(c, err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		"contract_discovery",
		"delete_snapshot",
		commonModels.AuditLevelP2,
		fmt.Sprintf("用户 [%s] 删除现网快照 #%d", operator, id),
		"discovery_snapshot",
		strconv.FormatUint(id, 10),
		fmt.Sprintf("snap_%d", id),
		nil,
		nil,
	)

	c.JSON(http.StatusOK, gin.H{"message": "现网快照已删除"})
}

// DiffDiscoverySnapshots handles GET /api/discovery/snapshots/diff.
func DiffDiscoverySnapshots(c *gin.Context) {
	leftID, err := strconv.ParseUint(strings.TrimSpace(c.Query("left_snapshot_id")), 10, 64)
	if err != nil || leftID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "left_snapshot_id 参数必填"})
		return
	}

	var rightID uint64
	if raw := strings.TrimSpace(c.Query("right_snapshot_id")); raw != "" {
		rightID, err = strconv.ParseUint(raw, 10, 64)
		if err != nil || rightID == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "right_snapshot_id 参数无效"})
			return
		}
	}

	var liveAgentID uint64
	if raw := strings.TrimSpace(c.Query("live_agent_id")); raw != "" {
		liveAgentID, err = strconv.ParseUint(raw, 10, 64)
		if err != nil || liveAgentID == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "live_agent_id 参数无效"})
			return
		}
	}

	if (rightID == 0 && liveAgentID == 0) || (rightID > 0 && liveAgentID > 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "right_snapshot_id 与 live_agent_id 必须二选一传入"})
		return
	}

	uc := commonAuth.GetUserContext(c)
	isAdmin := isCurrentUserAdmin(uc)
	userID := uint(0)
	if uc != nil {
		userID = uc.UserID
	}

	leftSnap, leftPayload, err := discovery.GetSnapshotPayload(models.DB, leftID, userID, isAdmin)
	if err != nil {
		respondDiscoveryError(c, err)
		return
	}
	leftLabel := fmt.Sprintf("快照 #%d (%s)", leftSnap.ID, leftSnap.AgentName)
	if leftSnap.Label != "" {
		leftLabel = fmt.Sprintf("快照 #%d · %s", leftSnap.ID, leftSnap.Label)
	}

	var rightPayload *discovery.DiscoveryLiveRespDTO
	var rightLabel string
	mode := "TIMELINE"

	if rightID > 0 {
		rightSnap, rp, err := discovery.GetSnapshotPayload(models.DB, rightID, userID, isAdmin)
		if err != nil {
			respondDiscoveryError(c, err)
			return
		}
		rightPayload = rp
		rightLabel = fmt.Sprintf("快照 #%d (%s)", rightSnap.ID, rightSnap.AgentName)
		if rightSnap.Label != "" {
			rightLabel = fmt.Sprintf("快照 #%d · %s", rightSnap.ID, rightSnap.Label)
		}
		if leftSnap.AgentID != rightSnap.AgentID {
			mode = "CROSS_ENV"
		}
	} else {
		csgAddr := strings.TrimSpace(c.Query("csg_addr"))
		rp, err := GetDiscoveryService().GetLiveDiscovery(c.Request.Context(), liveAgentID, csgAddr, false, userID, isAdmin)
		if err != nil {
			respondDiscoveryError(c, err)
			return
		}
		rightPayload = rp
		rightLabel = fmt.Sprintf("实时态 (%s)", rp.AgentName)
		if leftSnap.AgentID != liveAgentID {
			mode = "CROSS_ENV"
		}
	}

	diff := discovery.DiffSnapshots(leftPayload, rightPayload, mode, leftLabel, rightLabel)
	c.JSON(http.StatusOK, diff)
}

// EnrollDiscoveryReq defines POST /api/discovery/enroll request body.
type EnrollDiscoveryReq struct {
	AgentID FlexUint64                  `json:"agent_id" binding:"required"`
	CSGAddr string                      `json:"csg_addr"`
	Items   []discovery.EnrollItemInput `json:"items"`
}

// EnrollDiscoveryProcesses handles POST /api/discovery/enroll.
func EnrollDiscoveryProcesses(c *gin.Context) {
	if !discovery.IsV2Enabled(models.DB) {
		c.JSON(http.StatusNotFound, gin.H{"error": "现网发现 V2 功能未开启"})
		return
	}

	var req EnrollDiscoveryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: " + err.Error()})
		return
	}
	if uint64(req.AgentID) == 0 || len(req.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent_id 与非空 items 参数必填"})
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

	res, err := discovery.Enroll(c.Request.Context(), models.DB, GetDiscoveryService(), discovery.EnrollInput{
		AgentID:    uint64(req.AgentID),
		CSGAddr:    strings.TrimSpace(req.CSGAddr),
		Items:      req.Items,
		OperatorID: userID,
		IsAdmin:    isAdmin,
	})
	if err != nil {
		var valErr *discovery.EnrollValidationError
		if errors.As(err, &valErr) {
			c.JSON(http.StatusUnprocessableEntity, valErr)
			return
		}
		respondDiscoveryError(c, err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		"contract_discovery",
		"enroll",
		commonModels.AuditLevelP2,
		fmt.Sprintf("用户 [%s] 在 Agent #%d 零侵扰物理纳管 %d 个进程（放置 %d，依赖 %d）", operator, req.AgentID, len(res.Enrolled), res.PlacementsCount, res.DependenciesCount),
		"agent",
		strconv.FormatUint(uint64(req.AgentID), 10),
		fmt.Sprintf("agent_%d", req.AgentID),
		nil,
		res,
	)

	c.JSON(http.StatusOK, res)
}

type quickRecordDiscoveryReq struct {
	AgentID              FlexUint64        `json:"agent_id" binding:"required"`
	ProcessKeys          []string          `json:"process_keys" binding:"required"`
	Name                 string            `json:"name"`
	Scope                string            `json:"scope"`
	DurationSec          int               `json:"duration_sec"`
	MaxFrames            int               `json:"max_frames"`
	MaxFrameBytes        int               `json:"max_frame_bytes"`
	CSGAddr              string            `json:"csg_addr"`
	RequestID            string            `json:"request_id" binding:"required"`
	IdentityOverrides    map[string]uint64 `json:"identity_overrides"`
	ConfirmLowConfidence bool              `json:"confirm_low_confidence"`
}

// QuickRecordDiscovery handles POST /api/discovery/quick-record (M31 4-step Saga).
func QuickRecordDiscovery(c *gin.Context) {
	if !discovery.IsV2Enabled(models.DB) {
		c.JSON(http.StatusNotFound, gin.H{"error": "现网发现 V2 功能未开启"})
		return
	}

	var req quickRecordDiscoveryReq
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

	var syncer discovery.AgentMonitorSyncer
	if orch := GetGlobalOrchestrator(); orch != nil {
		syncer = &orchestratorMonitorSyncer{orch: orch}
	}

	res, err := discovery.ExecuteQuickRecordSaga(c.Request.Context(), models.DB, GetDiscoveryService(), syncer, discovery.QuickRecordInput{
		AgentID:              uint64(req.AgentID),
		ProcessKeys:          req.ProcessKeys,
		Name:                 req.Name,
		Scope:                req.Scope,
		DurationSec:          req.DurationSec,
		MaxFrames:            req.MaxFrames,
		MaxFrameBytes:        req.MaxFrameBytes,
		CSGAddr:              strings.TrimSpace(req.CSGAddr),
		RequestID:            strings.TrimSpace(req.RequestID),
		IdentityOverrides:    req.IdentityOverrides,
		ConfirmLowConfidence: req.ConfirmLowConfidence,
		OperatorID:           userID,
		IsAdmin:              isAdmin,
	})
	if err != nil {
		var valErr *discovery.EnrollValidationError
		if errors.As(err, &valErr) {
			c.JSON(http.StatusUnprocessableEntity, valErr)
			return
		}
		var leaseErr *hub.LeaseConflictError
		if errors.As(err, &leaseErr) {
			c.JSON(http.StatusConflict, gin.H{
				"code":            "LEASE_CONFLICT",
				"error":           leaseErr.Error(),
				"lease_holder_id": leaseErr.LeaseHolderID,
				"lease_type":      leaseErr.LeaseType,
				"lease_ref_id":    leaseErr.LeaseRefID,
				"expire_at":       leaseErr.ExpireAt,
			})
			return
		}
		if errors.Is(err, discovery.ErrQuickRecordTargetsEmpty) || errors.Is(err, discovery.ErrQuickRecordRequestIDEmpty) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		respondDiscoveryError(c, err)
		return
	}

	if !res.IdempotentHit {
		commonAudit.SetAuditContext(
			c,
			"contract_discovery",
			"quick_record",
			commonModels.AuditLevelP2,
			fmt.Sprintf("用户 [%s] 在 Agent #%d 一键直录会话 #%d（进程 %d）", operator, req.AgentID, res.Session.ID, len(req.ProcessKeys)),
			"recording_session",
			strconv.FormatUint(res.Session.ID, 10),
			res.Session.Name,
			nil,
			res.Session,
		)
	}

	statusCode := http.StatusCreated
	if res.IdempotentHit {
		statusCode = http.StatusOK
	}
	c.JSON(statusCode, res)
}

// GetDiscoveryPhysical handles GET /api/discovery/physical.

type orchestratorMonitorSyncer struct {
	orch *recorder.Orchestrator
}

func (s *orchestratorMonitorSyncer) SyncMonitors(ctx context.Context, agt *models.Agent, req agentproto.SyncMonitorReq) ([]agentproto.ProcessDiagStatus, error) {
	return s.orch.SyncAgentMonitors(ctx, agt, req)
}

func (s *orchestratorMonitorSyncer) AttachRecordingSession(ctx context.Context, session *models.RecordingSession, agt *models.Agent) {
	s.orch.AttachRecordingSession(ctx, session, agt)
}

type simulationPlanReq struct {
	AgentID       FlexUint64 `json:"agent_id" binding:"required"`
	ArchProcessID FlexUint64 `json:"arch_process_id"`
	ProcessKey    string     `json:"process_key"`
	CSGAddr       string     `json:"csg_addr"`
}

// BuildDiscoverySimulationPlan handles POST /api/discovery/simulation-plan (M31).
func BuildDiscoverySimulationPlan(c *gin.Context) {
	var req simulationPlanReq
	if err := c.ShouldBindJSON(&req); err != nil || uint64(req.AgentID) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误"})
		return
	}
	if uint64(req.ArchProcessID) == 0 && strings.TrimSpace(req.ProcessKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "arch_process_id 与 process_key 至少提供一项"})
		return
	}

	uc := commonAuth.GetUserContext(c)
	isAdmin := isCurrentUserAdmin(uc)
	userID := uint(0)
	if uc != nil {
		userID = uc.UserID
	}

	plan, err := discovery.BuildSimulationPlan(c.Request.Context(), models.DB, GetDiscoveryService(), discovery.SimulationPlanInput{
		AgentID:       uint64(req.AgentID),
		ArchProcessID: uint64(req.ArchProcessID),
		ProcessKey:    strings.TrimSpace(req.ProcessKey),
		CSGAddr:       strings.TrimSpace(req.CSGAddr),
		UserID:        userID,
		IsAdmin:       isAdmin,
	})
	if err != nil {
		respondDiscoveryError(c, err)
		return
	}
	c.JSON(http.StatusOK, plan)
}
func GetDiscoveryPhysical(c *gin.Context) {
	var agentID uint64
	if raw := strings.TrimSpace(c.Query("agent_id")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "agent_id 参数无效"})
			return
		}
		agentID = parsed
	}
	var groupID uint64
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "group_id 参数无效"})
			return
		}
		groupID = parsed
	}
	includeProv := strings.EqualFold(strings.TrimSpace(c.Query("include_provisional")), "true") || strings.TrimSpace(c.Query("include_provisional")) == "1"
	page, pageSize := parsePagination(c)

	uc := commonAuth.GetUserContext(c)
	isAdmin := isCurrentUserAdmin(uc)
	userID := uint(0)
	if uc != nil {
		userID = uc.UserID
	}

	resp, err := discovery.QueryPhysicalAssets(models.DB, discovery.PhysicalAssetQuery{
		AgentID:            agentID,
		GroupID:            groupID,
		Status:             strings.TrimSpace(c.Query("status")),
		ClaimStatus:        strings.TrimSpace(c.Query("claim_status")),
		Transport:          strings.TrimSpace(c.Query("transport")),
		Search:             strings.TrimSpace(c.Query("search")),
		IncludeProvisional: includeProv,
		Page:               page,
		PageSize:           pageSize,
	}, userID, isAdmin)
	if err != nil {
		respondDiscoveryError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// MigratePlaceholdersReq defines POST /api/admin/migrations/csg-placeholder-modules body.
type MigratePlaceholdersReq struct {
	DryRun  bool   `json:"dry_run"`
	Action  string `json:"action"`
	BatchID string `json:"batch_id"`
}

// MigrateCSGPlaceholderModulesHandler handles POST /api/admin/migrations/csg-placeholder-modules (AdminOnly).
func MigrateCSGPlaceholderModulesHandler(c *gin.Context) {
	var req MigratePlaceholdersReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: " + err.Error()})
		return
	}

	uc := commonAuth.GetUserContext(c)
	userID := uint(0)
	operator := "system"
	if uc != nil {
		userID = uc.UserID
		operator = uc.Username
	}

	res, err := discovery.MigrateCSGPlaceholderModules(c.Request.Context(), models.DB, discovery.MigratePlaceholdersInput{
		DryRun:     req.DryRun,
		Action:     req.Action,
		BatchID:    req.BatchID,
		OperatorID: userID,
	})
	if err != nil {
		if errors.Is(err, discovery.ErrMigBackupNotFound) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		respondDiscoveryError(c, err)
		return
	}

	if res.Mode != "DRY_RUN" {
		commonAudit.SetAuditContext(
			c,
			"contract_admin",
			"migrate_placeholders",
			commonModels.AuditLevelP1,
			fmt.Sprintf("管理员 [%s] 执行 CSG 占位模块迁移 [%s]（模块 %d，服务 %d，批次 %s）", operator, res.Mode, res.ClearedModules, res.MigratedServices, res.BackupBatchID),
			"csg_migration",
			res.BackupBatchID,
			"csg_placeholder_modules",
			nil,
			res,
		)
	}

	c.JSON(http.StatusOK, res)
}
