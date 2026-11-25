package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"
	"code-contract/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	DefaultReconcileIntervalSec = 600
	DefaultStaleMissThreshold   = 3
	DefaultStaleHours           = 24
	DefaultRetireDays           = 7
	DefaultGCDays               = 30
	DefaultGCDailyDeleteCap     = 500
	DefaultReconcileConcurrency = 4
)

// GCResult 每日受限 GC 执行统计
type GCResult struct {
	DeletedServices             int `json:"deleted_services"`
	DeletedProvisionalProcesses int `json:"deleted_provisional_processes"`
	DeletedPlacementCount       int `json:"deleted_placement_count"`
}

// PhysicalReconciler 周期物理盘点对账器、离线冻结与每日受限 GC 引擎 (ADR-24 §6)
type PhysicalReconciler struct {
	db               *gorm.DB
	liveSvc          *LiveDiscoveryService
	GCDailyDeleteCap int
	MaxConcurrency   int
}

// NewPhysicalReconciler 创建物理盘点对账器实例
func NewPhysicalReconciler(db *gorm.DB, liveSvc *LiveDiscoveryService) *PhysicalReconciler {
	return &PhysicalReconciler{
		db:               db,
		liveSvc:          liveSvc,
		GCDailyDeleteCap: DefaultGCDailyDeleteCap,
		MaxConcurrency:   DefaultReconcileConcurrency,
	}
}

// Start 启动后台周期巡检与每日受限 GC 协程（随 ctx 取消优雅退出）
func (r *PhysicalReconciler) Start(ctx context.Context) {
	if r == nil || r.db == nil || r.liveSvc == nil {
		return
	}
	go func() {
		lastGCAt := time.Time{}
		for {
			intervalSec := ReadIntSetting(r.db, "discovery.reconcile_interval_sec", DefaultReconcileIntervalSec)
			if intervalSec < 10 {
				intervalSec = 10
			}
			// ±10% 随机抖动，避免多节点瞬时齐射 Agent
			jitterPct := 0.9 + rand.Float64()*0.2
			waitDur := time.Duration(float64(intervalSec)*jitterPct) * time.Second

			timer := time.NewTimer(waitDur)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}

			_ = r.ReconcileOnce(ctx)
			if lastGCAt.IsZero() || time.Since(lastGCAt) >= 24*time.Hour {
				_, _ = r.RunGCOnce(ctx)
				lastGCAt = time.Now()
			}
		}
	}()
}

// ReconcileOnce 扫描全部 ONLINE 且已挂载物理资产的 Agent 并执行限流并发对账
func (r *PhysicalReconciler) ReconcileOnce(ctx context.Context) error {
	if r == nil || r.db == nil || r.liveSvc == nil {
		return nil
	}
	if !IsV2Enabled(r.db) {
		return nil
	}

	var agentIDs []uint64
	if err := r.db.Model(&models.Agent{}).
		Select("contract_agents.id").
		Where("contract_agents.status = ?", models.AgentStatusOnline).
		Where("EXISTS (SELECT 1 FROM contract_processes p WHERE p.agent_id = contract_agents.id AND p.enroll_source = ?)", models.EnrollSourceDiscovery).
		Order("CASE WHEN contract_agents.lease_type = '' THEN 0 ELSE 1 END ASC, contract_agents.id ASC").
		Pluck("id", &agentIDs).Error; err != nil {
		return err
	}
	if len(agentIDs) == 0 {
		return nil
	}

	workers := r.MaxConcurrency
	if workers <= 0 {
		workers = DefaultReconcileConcurrency
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

	for _, aid := range agentIDs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(agentID uint64) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := r.ReconcileAgent(ctx, agentID); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			}
		}(aid)
	}
	wg.Wait()
	return firstErr
}

// ReconcileAgent 对单台在线 Agent 执行只读观测刷新、未失联重现复活及 miss 衰减流转
// 严禁在 Agent OFFLINE 或探测失败时推进 miss_count，也严禁自动纳管新进程或修改 ProbeDesiredState
func (r *PhysicalReconciler) ReconcileAgent(ctx context.Context, agentID uint64) error {
	if r == nil || r.db == nil || r.liveSvc == nil {
		return errors.New("discovery: reconciler not initialized")
	}

	var agent models.Agent
	if err := r.db.First(&agent, agentID).Error; err != nil {
		return err
	}
	// 离线冻结：Agent 非 ONLINE 绝不推进 miss_count 或状态流转
	if agent.Status != models.AgentStatusOnline {
		return nil
	}

	// 使本地短期 TTL 缓存失效后经 singleflight 拉取实时态（不触发用户 3s 手动刷新限流）
	r.liveSvc.InvalidateCache(agentID)
	live, err := r.liveSvc.GetLiveDiscovery(ctx, agentID, "", false, 0, true)
	if err != nil || live == nil || live.Stale {
		// 探测失败或回落到旧快照（Stale=true）时同样冻结状态，避免停机或断连误退役
		return nil
	}

	now := time.Now()
	staleMiss := ReadIntSetting(r.db, "discovery.stale_miss_threshold", DefaultStaleMissThreshold)
	staleHours := ReadIntSetting(r.db, "discovery.stale_hours", DefaultStaleHours)
	retireDays := ReadIntSetting(r.db, "discovery.retire_days", DefaultRetireDays)

	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var procs []models.Process
		if err := tx.Where("agent_id = ?", agentID).Find(&procs).Error; err != nil {
			return err
		}
		if len(procs) == 0 {
			return nil
		}

		procByArchID := make(map[uint64]models.Process, len(procs))
		for _, p := range procs {
			procByArchID[p.ArchProcessID] = p
		}

		var archProcs []models.ArchProcess
		if err := tx.Where("id IN ?", extractArchIDsFromProcs(procs)).Find(&archProcs).Error; err != nil {
			return err
		}

		type svcKey struct {
			procID  uint64
			svcName string
		}
		observedSvcs := make(map[svcKey]DiscoveryLiveServiceDTO)
		observedDeps := make(map[svcKey]DiscoveryLiveSubDTO)

		for _, lp := range live.Processes {
			if lp.IsRemoteLDC {
				continue
			}
			idRes := ResolveProcessIdentity(archProcs, procByArchID, lp.ExeName, lp.Cmdline, lp.ProcName, lp.ArchProcessID, true)
			if idRes.Matched == nil {
				// 仅刷新已纳管进程；绝不自动纳管未纳管的新进程
				continue
			}
			proc, mounted := procByArchID[idRes.Matched.ID]
			if !mounted {
				continue
			}

			transportsJSON, _ := json.Marshal(lp.ObservedTransports)
			pidsJSON, _ := json.Marshal(lp.PIDs)
			if err := tx.Model(&models.Process{}).Where("id = ?", proc.ID).Updates(map[string]interface{}{
				"observed_role":       lp.ObservedRole,
				"observed_transports": datatypes.JSON(transportsJSON),
				"observed_pids":       datatypes.JSON(pidsJSON),
				"last_observed_at":    &now,
			}).Error; err != nil {
				return err
			}

			for _, s := range lp.Services {
				clean := strings.TrimSpace(s.ServiceName)
				if clean != "" {
					observedSvcs[svcKey{procID: proc.ID, svcName: clean}] = s
				}
			}
			for _, d := range lp.Subscriptions {
				clean := strings.TrimSpace(d.ServiceName)
				if clean != "" {
					observedDeps[svcKey{procID: proc.ID, svcName: clean}] = d
				}
			}
		}

		// 刷新 ServicePlacement 状态与 miss 衰减
		var placements []models.ServicePlacement
		if err := tx.Where("agent_id = ?", agentID).Find(&placements).Error; err != nil {
			return err
		}
		msIDs := make([]uint64, 0, len(placements))
		for _, p := range placements {
			msIDs = append(msIDs, p.ModuleServiceID)
		}
		svcNameByID := make(map[uint64]string)
		if len(msIDs) > 0 {
			var svcs []models.ModuleService
			if err := tx.Select("id", "service_name").Where("id IN ?", msIDs).Find(&svcs).Error; err != nil {
				return err
			}
			for _, s := range svcs {
				svcNameByID[s.ID] = s.ServiceName
			}
		}

		for _, plc := range placements {
			svcName := svcNameByID[plc.ModuleServiceID]
			if svcDTO, seen := observedSvcs[svcKey{procID: plc.ProcessID, svcName: svcName}]; seen {
				epJSON, _ := json.Marshal(svcDTO.Endpoints)
				tr := strings.TrimSpace(svcDTO.Transport)
				if tr == "" {
					tr = models.ObservedTransportTCP
				}
				instCount := svcDTO.InstanceCount
				if instCount <= 0 {
					instCount = maxInt(len(svcDTO.ServerPIDs), 1)
				}
				if err := tx.Model(&models.ServicePlacement{}).Where("id = ?", plc.ID).Updates(map[string]interface{}{
					"raw_service_name":   svcDTO.RawServiceName,
					"endpoints":          datatypes.JSON(epJSON),
					"observed_transport": tr,
					"instance_count":     instCount,
					"status":             models.PlacementStatusActive,
					"miss_count":         0,
					"last_seen_at":       now,
				}).Error; err != nil {
					return err
				}
				continue
			}

			nextStatus, nextMiss := computeDecayedState(plc.Status, plc.MissCount, plc.LastSeenAt, now, staleMiss, staleHours, retireDays)
			if nextStatus != plc.Status || nextMiss != plc.MissCount {
				if err := tx.Model(&models.ServicePlacement{}).Where("id = ?", plc.ID).Updates(map[string]interface{}{
					"status":     nextStatus,
					"miss_count": nextMiss,
				}).Error; err != nil {
					return err
				}
			}
		}

		// 刷新 ProcessDependency 状态与 miss 衰减
		var deps []models.ProcessDependency
		if err := tx.Where("agent_id = ?", agentID).Find(&deps).Error; err != nil {
			return err
		}
		localProviderBySvc := make(map[string]uint64)
		for key := range observedSvcs {
			if _, exists := localProviderBySvc[key.svcName]; !exists {
				localProviderBySvc[key.svcName] = key.procID
			}
		}

		for _, dep := range deps {
			if subDTO, seen := observedDeps[svcKey{procID: dep.ConsumerProcessID, svcName: dep.ServiceName}]; seen {
				provKind := models.ProviderKindOffline
				var provProcID *uint64
				var provArchID *uint64
				if pid, ok := localProviderBySvc[dep.ServiceName]; ok && pid > 0 {
					provKind = models.ProviderKindLocalEnrolled
					pidCopy := pid
					provProcID = &pidCopy
					for _, pr := range procs {
						if pr.ID == pid {
							apCopy := pr.ArchProcessID
							provArchID = &apCopy
							break
						}
					}
				} else if subDTO.ProviderKind == models.ProviderKindRemoteLDC {
					provKind = models.ProviderKindRemoteLDC
				} else if subDTO.ProviderKind == models.ProviderKindLocalUnenrolled {
					provKind = models.ProviderKindLocalUnenrolled
				}
				if err := tx.Model(&models.ProcessDependency{}).Where("id = ?", dep.ID).Updates(map[string]interface{}{
					"provider_kind":            provKind,
					"provider_process_id":      provProcID,
					"provider_arch_process_id": provArchID,
					"remote_node":              subDTO.RemoteNode,
					"resolved_endpoint":        subDTO.ProviderEndpoint,
					"status":                   models.PlacementStatusActive,
					"miss_count":               0,
					"last_seen_at":             now,
				}).Error; err != nil {
					return err
				}
				continue
			}

			nextStatus, nextMiss := computeDecayedState(dep.Status, dep.MissCount, dep.LastSeenAt, now, staleMiss, staleHours, retireDays)
			if nextStatus != dep.Status || nextMiss != dep.MissCount {
				if err := tx.Model(&models.ProcessDependency{}).Where("id = ?", dep.ID).Updates(map[string]interface{}{
					"status":     nextStatus,
					"miss_count": nextMiss,
				}).Error; err != nil {
					return err
				}
			}
		}

		return nil
	}); err != nil {
		return err
	}

	// 若内容哈希变更则自动写入 SCHEDULED 持久化快照
	_, _ = SaveSnapshot(r.db, SaveSnapshotInput{
		AgentID:     agentID,
		CSGAddr:     live.CSGAddr,
		TriggerMode: models.DiscoveryTriggerScheduled,
		CreatedBy:   0,
		IsAdmin:     true,
		Live:        live,
	})
	return nil
}

func computeDecayedState(curStatus string, curMiss int, lastSeenAt, now time.Time, staleMiss, staleHours, retireDays int) (string, int) {
	if curStatus == models.PlacementStatusRetired {
		return curStatus, curMiss
	}
	nextMiss := curMiss + 1
	nextStatus := curStatus
	elapsed := now.Sub(lastSeenAt)

	if nextStatus == models.PlacementStatusActive {
		if (staleMiss > 0 && nextMiss >= staleMiss) || (staleHours > 0 && elapsed >= time.Duration(staleHours)*time.Hour) {
			nextStatus = models.PlacementStatusStale
		}
	}
	if nextStatus == models.PlacementStatusStale {
		if retireDays > 0 && elapsed >= time.Duration(retireDays)*24*time.Hour {
			nextStatus = models.PlacementStatusRetired
		}
	}
	return nextStatus, nextMiss
}

func extractArchIDsFromProcs(procs []models.Process) []uint64 {
	seen := make(map[uint64]bool, len(procs))
	out := make([]uint64, 0, len(procs))
	for _, p := range procs {
		if !seen[p.ArchProcessID] {
			seen[p.ArchProcessID] = true
			out = append(out, p.ArchProcessID)
		}
	}
	return out
}

// RunGCOnce 每日受限 GC：清理超期的未认领全退役服务与孤立 PROVISIONAL 进程，严禁清理任何已认领/被引用/含 ignore_paths 服务
func (r *PhysicalReconciler) RunGCOnce(ctx context.Context) (GCResult, error) {
	var out GCResult
	if r == nil || r.db == nil {
		return out, errors.New("discovery: nil db")
	}
	if !IsV2Enabled(r.db) {
		return out, nil
	}

	gcDays := ReadIntSetting(r.db, "discovery.gc_days", DefaultGCDays)
	if gcDays < 1 {
		gcDays = DefaultGCDays
	}
	cutoff := time.Now().Add(-time.Duration(gcDays) * 24 * time.Hour)
	capLimit := r.GCDailyDeleteCap
	if capLimit <= 0 {
		capLimit = DefaultGCDailyDeleteCap
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 候选未认领且未配置 diff_ignore_paths 的老化服务
		var candidates []models.ModuleService
		if err := tx.Where("module_id IS NULL AND source IN ? AND updated_at < ?",
			[]string{models.ServiceSourceCSGDiscovered, models.ServiceSourceMigrated}, cutoff).
			Order("updated_at ASC, id ASC").
			Limit(capLimit * 2).
			Find(&candidates).Error; err != nil {
			return err
		}

		for _, ms := range candidates {
			if out.DeletedServices+out.DeletedProvisionalProcesses >= capLimit {
				break
			}
			// 再次校验未认领与 ignore_paths 为空
			if ms.ModuleID != nil || !isEmptyIgnorePaths(ms.DiffIgnorePaths) {
				continue
			}

			// 校验不被 ModuleDependency / GoldenSuiteItem 引用
			var depCnt int64
			if err := tx.Model(&models.ModuleDependency{}).Where("service_id = ?", ms.ID).Count(&depCnt).Error; err != nil {
				return err
			}
			if depCnt > 0 {
				continue
			}
			var suiteCnt int64
			if err := tx.Model(&models.GoldenSuiteItem{}).Where("service_name = ?", ms.ServiceName).Count(&suiteCnt).Error; err != nil {
				return err
			}
			if suiteCnt > 0 {
				continue
			}

			// 校验名下不存在活跃、陈旧或尚未退役满期限的放置
			var blockingPlc int64
			if err := tx.Model(&models.ServicePlacement{}).
				Where("module_service_id = ? AND (status != ? OR updated_at >= ?)", ms.ID, models.PlacementStatusRetired, cutoff).
				Count(&blockingPlc).Error; err != nil {
				return err
			}
			if blockingPlc > 0 {
				continue
			}

			delPlcRes := tx.Where("module_service_id = ?", ms.ID).Delete(&models.ServicePlacement{})
			if delPlcRes.Error != nil {
				return delPlcRes.Error
			}
			out.DeletedPlacementCount += int(delPlcRes.RowsAffected)

			// 清零引用该 service_id 的历史 ProcessDependency 弱引用后删服务
			if err := tx.Model(&models.ProcessDependency{}).Where("service_id = ?", ms.ID).Update("service_id", nil).Error; err != nil {
				return err
			}
			if err := tx.Delete(&models.ModuleService{}, ms.ID).Error; err != nil {
				return err
			}
			out.DeletedServices++
		}

		// 2. 候选无任何挂载满 GC 保留期的 PROVISIONAL 标准进程
		rem := capLimit - (out.DeletedServices + out.DeletedProvisionalProcesses)
		if rem > 0 {
			var orphanProcs []models.ArchProcess
			if err := tx.Where("lifecycle = ? AND updated_at < ?", models.ArchProcLifecycleProvisional, cutoff).
				Where("NOT EXISTS (SELECT 1 FROM contract_processes cp WHERE cp.arch_process_id = contract_arch_processes.id)").
				Order("updated_at ASC, id ASC").
				Limit(rem).
				Find(&orphanProcs).Error; err != nil {
				return err
			}
			for _, ap := range orphanProcs {
				var modCnt int64
				if err := tx.Model(&models.Module{}).Where("arch_process_id = ?", ap.ID).Count(&modCnt).Error; err != nil || modCnt > 0 {
					continue
				}
				if err := tx.Delete(&models.ArchProcess{}, ap.ID).Error; err != nil {
					return err
				}
				out.DeletedProvisionalProcesses++
			}
		}

		return nil
	})
	if err != nil {
		return out, err
	}

	if out.DeletedServices > 0 || out.DeletedProvisionalProcesses > 0 {
		commonAudit.Push(&commonModels.SysAuditLog{
			Module:     "contract_discovery",
			Action:     "physical_gc",
			Level:      commonModels.AuditLevelP2,
			Username:   "system",
			Summary:    fmt.Sprintf("物理资产每日受限 GC 清理：未认领全退役服务 %d 个、孤立试用进程 %d 个、退役放置 %d 条", out.DeletedServices, out.DeletedProvisionalProcesses, out.DeletedPlacementCount),
			TargetType: "physical_gc",
		})
	}
	return out, nil
}

func isEmptyIgnorePaths(raw datatypes.JSON) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "[]" || s == "null"
}

// PhysicalAssetQuery GET /api/discovery/physical 查询参数
type PhysicalAssetQuery struct {
	AgentID            uint64
	GroupID            uint64
	Status             string
	ClaimStatus        string // ALL | CLAIMED | UNCLAIMED
	Transport          string // ALL | TCP | MESH | MIXED
	Search             string
	IncludeProvisional bool
	Page               int
	PageSize           int
}

// PhysicalProcessDTO 已纳管物理进程汇总行
type PhysicalProcessDTO struct {
	ProcessID           uint64   `json:"process_id"`
	AgentID             uint64   `json:"agent_id"`
	AgentName           string   `json:"agent_name"`
	AgentScope          string   `json:"agent_scope"`
	ArchProcessID       uint64   `json:"arch_process_id"`
	ArchProcessCode     string   `json:"arch_process_code"`
	ArchProcessName     string   `json:"arch_process_name"`
	GroupID             uint64   `json:"group_id"`
	Lifecycle           string   `json:"lifecycle"`
	Origin              string   `json:"origin"`
	EnrollSource        string   `json:"enroll_source"`
	DeclaredRole        string   `json:"declared_role"`
	ObservedRole        string   `json:"observed_role"`
	ObservedTransports  []string `json:"observed_transports"`
	ObservedPIDs        []uint32 `json:"observed_pids"`
	EffectiveListenPort int      `json:"effective_listen_port"`
	ProbeDesiredState   string   `json:"probe_desired_state"`
	ProbeStatus         string   `json:"probe_status"`
	LastObservedAt      string   `json:"last_observed_at,omitempty"`
}

// ServicePlacementDTO 物理服务放置摘要
type ServicePlacementDTO struct {
	ID                uint64   `json:"id"`
	ModuleServiceID   uint64   `json:"module_service_id"`
	ServiceName       string   `json:"service_name"`
	ProcessID         uint64   `json:"process_id"`
	AgentID           uint64   `json:"agent_id"`
	ArchProcessID     uint64   `json:"arch_process_id"`
	Endpoints         []string `json:"endpoints"`
	ObservedTransport string   `json:"observed_transport"`
	InstanceCount     int      `json:"instance_count"`
	Status            string   `json:"status"`
	MissCount         int      `json:"miss_count"`
	ClaimStatus       string   `json:"claim_status"` // CLAIMED | UNCLAIMED
	ModuleID          *uint64  `json:"module_id,omitempty"`
	LastSeenAt        string   `json:"last_seen_at"`
}

// ProcessDependencyDTO 环境级物理订阅边摘要
type ProcessDependencyDTO struct {
	ID                    uint64  `json:"id"`
	ConsumerProcessID     uint64  `json:"consumer_process_id"`
	AgentID               uint64  `json:"agent_id"`
	ConsumerArchProcessID uint64  `json:"consumer_arch_process_id"`
	ServiceName           string  `json:"service_name"`
	ServiceID             *uint64 `json:"service_id,omitempty"`
	ProviderKind          string  `json:"provider_kind"`
	ProviderProcessID     *uint64 `json:"provider_process_id,omitempty"`
	ProviderArchProcessID *uint64 `json:"provider_arch_process_id,omitempty"`
	RemoteNode            string  `json:"remote_node,omitempty"`
	ResolvedEndpoint      string  `json:"resolved_endpoint,omitempty"`
	Status                string  `json:"status"`
	MissCount             int     `json:"miss_count"`
	LastSeenAt            string  `json:"last_seen_at"`
}

// PhysicalAssetsResp GET /api/discovery/physical 响应体
type PhysicalAssetsResp struct {
	Processes      []PhysicalProcessDTO   `json:"processes"`
	Placements     []ServicePlacementDTO  `json:"placements"`
	Dependencies   []ProcessDependencyDTO `json:"dependencies"`
	TotalProcesses int64                  `json:"total_processes"`
	Page           int                    `json:"page"`
	PageSize       int                    `json:"page_size"`
}

// QueryPhysicalAssets 查询已纳管物理进程、服务放置与环境级订阅边（严格执行 V1/V2/V4 可见性隔离）
func QueryPhysicalAssets(db *gorm.DB, query PhysicalAssetQuery, userID uint, isAdmin bool) (*PhysicalAssetsResp, error) {
	if db == nil {
		return nil, errors.New("discovery: nil db")
	}

	page := query.Page
	if page <= 0 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 25
	} else if pageSize > 200 {
		pageSize = 200
	}

	var agents []models.Agent
	if err := db.Select("id, name, scope, owner_id").Find(&agents).Error; err != nil {
		return nil, err
	}
	agentByID := make(map[uint64]*models.Agent, len(agents))
	allowedAgentIDs := make([]uint64, 0, len(agents))
	for i := range agents {
		ag := agents[i]
		agentByID[ag.ID] = &ag
	}

	if query.AgentID > 0 {
		ag, ok := agentByID[query.AgentID]
		if !ok {
			return nil, ErrAgentNotFound
		}
		if ag.Scope == models.ScopePrivate && !isAdmin && (ag.OwnerID == nil || *ag.OwnerID != userID) {
			return nil, ErrAgentForbidden
		}
		allowedAgentIDs = append(allowedAgentIDs, ag.ID)
	} else {
		// V4: 缺省跨环境视图仅聚合权威 SHARED 环境，不暴露他人 PRIVATE 数据
		for _, ag := range agents {
			if ag.Scope == models.ScopeShared {
				allowedAgentIDs = append(allowedAgentIDs, ag.ID)
			}
		}
	}

	resp := &PhysicalAssetsResp{
		Processes:    []PhysicalProcessDTO{},
		Placements:   []ServicePlacementDTO{},
		Dependencies: []ProcessDependencyDTO{},
		Page:         page,
		PageSize:     pageSize,
	}
	if len(allowedAgentIDs) == 0 {
		return resp, nil
	}

	procQuery := db.Preload("ArchProcess").Where("agent_id IN ?", allowedAgentIDs).Order("agent_id ASC, id ASC")
	var rawProcs []models.Process
	if err := procQuery.Find(&rawProcs).Error; err != nil {
		return nil, err
	}

	searchLower := strings.ToLower(strings.TrimSpace(query.Search))
	filteredProcs := make([]PhysicalProcessDTO, 0, len(rawProcs))
	visibleProcIDs := make([]uint64, 0, len(rawProcs))

	for _, p := range rawProcs {
		if p.ArchProcess == nil {
			continue
		}
		ap := *p.ArchProcess
		if query.GroupID > 0 && ap.GroupID != query.GroupID {
			continue
		}
		if ap.Lifecycle == models.ArchProcLifecycleProvisional {
			if !query.IncludeProvisional {
				continue
			}
			if !IsArchProcessVisibleToUser(ap, agentByID, userID, isAdmin) {
				continue
			}
		}
		if searchLower != "" {
			if !strings.Contains(strings.ToLower(ap.Code), searchLower) &&
				!strings.Contains(strings.ToLower(ap.Name), searchLower) {
				continue
			}
		}

		ag := agentByID[p.AgentID]
		var trs []string
		_ = json.Unmarshal(p.ObservedTransports, &trs)
		var pids []uint32
		_ = json.Unmarshal(p.ObservedPIDs, &pids)
		lastObs := ""
		if p.LastObservedAt != nil {
			lastObs = p.LastObservedAt.Format(time.RFC3339)
		}
		dto := PhysicalProcessDTO{
			ProcessID:           p.ID,
			AgentID:             p.AgentID,
			ArchProcessID:       ap.ID,
			ArchProcessCode:     ap.Code,
			ArchProcessName:     ap.Name,
			GroupID:             ap.GroupID,
			Lifecycle:           ap.Lifecycle,
			Origin:              ap.Origin,
			EnrollSource:        p.EnrollSource,
			DeclaredRole:        ap.DeclaredRole,
			ObservedRole:        p.ObservedRole,
			ObservedTransports:  trs,
			ObservedPIDs:        pids,
			EffectiveListenPort: p.EffectiveListenPort,
			ProbeDesiredState:   p.ProbeDesiredState,
			ProbeStatus:         p.ProbeStatus,
			LastObservedAt:      lastObs,
		}
		if ag != nil {
			dto.AgentName = ag.Name
			dto.AgentScope = ag.Scope
		}
		filteredProcs = append(filteredProcs, dto)
		visibleProcIDs = append(visibleProcIDs, p.ID)
	}

	resp.TotalProcesses = int64(len(filteredProcs))
	start := (page - 1) * pageSize
	if start < len(filteredProcs) {
		end := start + pageSize
		if end > len(filteredProcs) {
			end = len(filteredProcs)
		}
		resp.Processes = filteredProcs[start:end]
	}

	if len(visibleProcIDs) == 0 {
		return resp, nil
	}

	plcQuery := db.Preload("ModuleService").Where("process_id IN ?", visibleProcIDs)
	if st := strings.ToUpper(strings.TrimSpace(query.Status)); st != "" && st != "ALL" {
		plcQuery = plcQuery.Where("status = ?", st)
	}
	if tr := strings.ToUpper(strings.TrimSpace(query.Transport)); tr != "" && tr != "ALL" {
		plcQuery = plcQuery.Where("observed_transport = ?", tr)
	}
	var rawPlcs []models.ServicePlacement
	if err := plcQuery.Order("id ASC").Find(&rawPlcs).Error; err != nil {
		return nil, err
	}

	claimFilter := strings.ToUpper(strings.TrimSpace(query.ClaimStatus))
	for _, plc := range rawPlcs {
		if plc.ModuleService == nil {
			continue
		}
		cs := "UNCLAIMED"
		if plc.ModuleService.ModuleID != nil && *plc.ModuleService.ModuleID > 0 {
			cs = "CLAIMED"
		}
		if claimFilter != "" && claimFilter != "ALL" && cs != claimFilter {
			continue
		}
		var eps []string
		_ = json.Unmarshal(plc.Endpoints, &eps)
		resp.Placements = append(resp.Placements, ServicePlacementDTO{
			ID:                plc.ID,
			ModuleServiceID:   plc.ModuleServiceID,
			ServiceName:       plc.ModuleService.ServiceName,
			ProcessID:         plc.ProcessID,
			AgentID:           plc.AgentID,
			ArchProcessID:     plc.ArchProcessID,
			Endpoints:         eps,
			ObservedTransport: plc.ObservedTransport,
			InstanceCount:     plc.InstanceCount,
			Status:            plc.Status,
			MissCount:         plc.MissCount,
			ClaimStatus:       cs,
			ModuleID:          plc.ModuleService.ModuleID,
			LastSeenAt:        plc.LastSeenAt.Format(time.RFC3339),
		})
	}

	depQuery := db.Where("consumer_process_id IN ?", visibleProcIDs)
	if st := strings.ToUpper(strings.TrimSpace(query.Status)); st != "" && st != "ALL" {
		depQuery = depQuery.Where("status = ?", st)
	}
	var rawDeps []models.ProcessDependency
	if err := depQuery.Order("id ASC").Find(&rawDeps).Error; err != nil {
		return nil, err
	}
	for _, dep := range rawDeps {
		resp.Dependencies = append(resp.Dependencies, ProcessDependencyDTO{
			ID:                    dep.ID,
			ConsumerProcessID:     dep.ConsumerProcessID,
			AgentID:               dep.AgentID,
			ConsumerArchProcessID: dep.ConsumerArchProcessID,
			ServiceName:           dep.ServiceName,
			ServiceID:             dep.ServiceID,
			ProviderKind:          dep.ProviderKind,
			ProviderProcessID:     dep.ProviderProcessID,
			ProviderArchProcessID: dep.ProviderArchProcessID,
			RemoteNode:            dep.RemoteNode,
			ResolvedEndpoint:      dep.ResolvedEndpoint,
			Status:                dep.Status,
			MissCount:             dep.MissCount,
			LastSeenAt:            dep.LastSeenAt.Format(time.RFC3339),
		})
	}

	return resp, nil
}
