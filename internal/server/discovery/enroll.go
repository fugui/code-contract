package discovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"code-contract/internal/server/topology"
	"code-contract/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultEnrollRateLimit       = 5 * time.Second
	defaultSharedBatchLimit      = 50
	defaultProvisionalPfxFmt     = "prv%d-%s"
	defaultCSGAutoProcessGrpCode = "PGRP_CSG_AUTO"
)

var (
	// ErrDiscoveryV2Disabled 表示 discovery.v2_enabled=false 时 V2 接口未启用
	ErrDiscoveryV2Disabled = errors.New("discovery v2 is disabled")
	// ErrEnrollBatchLimit 表示 SHARED 环境单次纳管数量超过上限
	ErrEnrollBatchLimit = errors.New("enroll batch size exceeds shared limit")
	// ErrEnrollRateLimited 表示同一操作者对同一 Agent 的纳管请求过于频繁
	ErrEnrollRateLimited = errors.New("enroll rate limited")
	// ErrEnrollTargetMissing 表示请求中指定的 process_key 在当前发现视图中不存在
	ErrEnrollTargetMissing = errors.New("discovery process_key not found")
	// ErrTargetGroupNotFound 表示纳管指定的目标物理进程组不存在
	ErrTargetGroupNotFound = errors.New("target process group not found")
)

// EnrollValidationError 表示需要前端交互确认（422 语义歧义或低置信）的校验错误
type EnrollValidationError struct {
	Code       string                `json:"code"`
	Message    string                `json:"error"`
	ProcessKey string                `json:"process_key,omitempty"`
	Candidates []ProcessCandidateDTO `json:"candidates,omitempty"`
}

func (e *EnrollValidationError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// EnrollItemInput 单条待纳管物理进程参数
type EnrollItemInput struct {
	ProcessKey           string  `json:"process_key" binding:"required"`
	ForceArchProcessID   *uint64 `json:"force_arch_process_id"`
	ConfirmLowConfidence bool    `json:"confirm_low_confidence"`
	DisplayName          string  `json:"display_name"`
}

// EnrollInput 批量物理纳管输入参数
type EnrollInput struct {
	AgentID         uint64
	CSGAddr         string
	TargetGroupID   *uint64
	Items           []EnrollItemInput
	OperatorID      uint
	IsAdmin         bool
	BypassRateLimit bool
}

// EnrolledProcessDTO 单条纳管结果明细
type EnrolledProcessDTO struct {
	ProcessKey        string `json:"process_key"`
	ArchProcessID     uint64 `json:"arch_process_id"`
	ArchProcessCode   string `json:"arch_process_code"`
	ArchProcessName   string `json:"arch_process_name"`
	GroupID           uint64 `json:"group_id,omitempty"`
	Lifecycle         string `json:"lifecycle"`
	ProcessID         uint64 `json:"process_id"`
	Result            string `json:"result"` // MATCHED | CREATED_PROVISIONAL | CREATED_CONFIRMED
	ServicesCreated   int    `json:"services_created"`
	PlacementsUpdated int    `json:"placements_updated"`
}

// EnrollResult 批量物理纳管响应载体
type EnrollResult struct {
	AgentID           uint64               `json:"agent_id"`
	Enrolled          []EnrolledProcessDTO `json:"enrolled"`
	PlacementsCount   int                  `json:"placements_count"`
	DependenciesCount int                  `json:"dependencies_count"`
	SnapshotID        *uint64              `json:"snapshot_id,omitempty"`
}

type enrollRateKey struct {
	operatorID uint
	agentID    uint64
}

var (
	enrollRateMu sync.Mutex
	enrollRateAt = make(map[enrollRateKey]time.Time)
)

// ResetEnrollRateLimiter 清空内存级纳管频率限制器（供单测隔离使用）
func ResetEnrollRateLimiter() {
	enrollRateMu.Lock()
	enrollRateAt = make(map[enrollRateKey]time.Time)
	enrollRateMu.Unlock()
}

// IsV2Enabled 读取 contract_settings 中的 discovery.v2_enabled 开关
func IsV2Enabled(db *gorm.DB) bool {
	return ReadBoolSetting(db, "discovery.v2_enabled", false)
}

// ReadBoolSetting 读取布尔类型系统配置
func ReadBoolSetting(db *gorm.DB, key string, def bool) bool {
	if db == nil {
		return def
	}
	var s models.ModuleSetting
	if err := db.Select("setting_val").Where("setting_key = ?", key).First(&s).Error; err != nil {
		return def
	}
	v := strings.TrimSpace(strings.ToLower(s.SettingVal))
	if v == "true" || v == "1" {
		return true
	}
	if v == "false" || v == "0" {
		return false
	}
	return def
}

// ReadIntSetting 读取整数类型系统配置
func ReadIntSetting(db *gorm.DB, key string, def int) int {
	if db == nil {
		return def
	}
	var s models.ModuleSetting
	if err := db.Select("setting_val").Where("setting_key = ?", key).First(&s).Error; err != nil {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s.SettingVal))
	if err != nil {
		return def
	}
	return n
}

// ScopeEnrollGuard 按 Agent 作用域校验当前用户是否有权发起零侵扰纳管
// SHARED: 任意已登录用户（含他人持有租约时）均可无副作用纳管；PRIVATE: 仅 Owner 或 Admin
func ScopeEnrollGuard(agent *models.Agent, userID uint, isAdmin bool) error {
	if agent == nil {
		return ErrAgentNotFound
	}
	if isAdmin || agent.Scope == models.ScopeShared {
		return nil
	}
	if agent.OwnerID != nil && *agent.OwnerID == userID {
		return nil
	}
	return ErrAgentForbidden
}

// Enroll 执行零侵扰物理纳管事务：身份匹配/新建、Process DISARMED 挂载、ServicePlacement 与 ProcessDependency 落库
func Enroll(ctx context.Context, db *gorm.DB, liveSvc *LiveDiscoveryService, in EnrollInput) (*EnrollResult, error) {
	if db == nil {
		return nil, errors.New("discovery: nil db")
	}
	if !IsV2Enabled(db) {
		return nil, ErrDiscoveryV2Disabled
	}

	var agent models.Agent
	if err := db.First(&agent, in.AgentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAgentNotFound
		}
		return nil, err
	}
	if err := ScopeEnrollGuard(&agent, in.OperatorID, in.IsAdmin); err != nil {
		return nil, err
	}
	if len(in.Items) == 0 {
		return &EnrollResult{AgentID: in.AgentID, Enrolled: []EnrolledProcessDTO{}}, nil
	}

	if agent.Scope == models.ScopeShared {
		limit := ReadIntSetting(db, "discovery.enroll_batch_limit_shared", defaultSharedBatchLimit)
		if limit > 0 && len(in.Items) > limit {
			return nil, fmt.Errorf("%w: 请求 %d 条，SHARED 上限 %d 条", ErrEnrollBatchLimit, len(in.Items), limit)
		}
	}

	if !in.BypassRateLimit {
		rk := enrollRateKey{operatorID: in.OperatorID, agentID: in.AgentID}
		enrollRateMu.Lock()
		lastAt, exists := enrollRateAt[rk]
		now := time.Now()
		if exists && now.Sub(lastAt) < defaultEnrollRateLimit {
			enrollRateMu.Unlock()
			return nil, ErrEnrollRateLimited
		}
		enrollRateAt[rk] = now
		enrollRateMu.Unlock()
	}

	if liveSvc == nil {
		return nil, errors.New("discovery: nil liveSvc")
	}
	liveResp, err := liveSvc.GetLiveDiscovery(ctx, in.AgentID, in.CSGAddr, false, in.OperatorID, in.IsAdmin)
	if err != nil {
		return nil, err
	}

	liveByKey := make(map[string]DiscoveryProcessGroupDTO, len(liveResp.Processes))
	for _, p := range liveResp.Processes {
		liveByKey[p.ProcessKey] = p
	}
	for _, it := range in.Items {
		if _, ok := liveByKey[it.ProcessKey]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrEnrollTargetMissing, it.ProcessKey)
		}
	}

	res := &EnrollResult{
		AgentID:  in.AgentID,
		Enrolled: make([]EnrolledProcessDTO, 0, len(in.Items)),
	}

	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lockedAgent models.Agent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedAgent, in.AgentID).Error; err != nil {
			return err
		}

		var targetGroup models.ArchProcessGroup
		if in.TargetGroupID != nil && *in.TargetGroupID > 0 {
			if err := tx.First(&targetGroup, *in.TargetGroupID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrTargetGroupNotFound
				}
				return err
			}
		} else {
			targetGroup, err = ensureCSGAutoProcessGroupTx(tx)
			if err != nil {
				return err
			}
		}

		// 载入全部 Agent 作用域索引与所有标准架构进程
		var allAgents []models.Agent
		if err := tx.Select("id", "scope", "owner_id").Find(&allAgents).Error; err != nil {
			return err
		}
		agentMap := make(map[uint64]*models.Agent, len(allAgents))
		for i := range allAgents {
			ag := allAgents[i]
			agentMap[ag.ID] = &ag
		}

		var allArchProcs []models.ArchProcess
		if err := tx.Order("id ASC").Find(&allArchProcs).Error; err != nil {
			return err
		}
		var mounted []models.Process
		if err := tx.Where("agent_id = ?", in.AgentID).Find(&mounted).Error; err != nil {
			return err
		}
		mountedByArch := make(map[uint64]models.Process, len(mounted))
		for _, m := range mounted {
			mountedByArch[m.ArchProcessID] = m
		}

		// 构建本次可见标准进程池；SHARED 环境额外允许检索跨用户的 PROVISIONAL 指纹以便自动转正
		matchPool := make([]models.ArchProcess, 0, len(allArchProcs))
		provisionalByFP := make(map[string]*models.ArchProcess)
		usedCodes := make(map[string]uint64, len(allArchProcs))
		for i := range allArchProcs {
			ap := allArchProcs[i]
			usedCodes[ap.Code] = ap.ID
			if ap.Lifecycle == models.ArchProcLifecycleProvisional && ap.FingerprintVersion == CurrentFingerprintVersion && ap.IdentityFingerprint != "" {
				if _, exists := provisionalByFP[ap.IdentityFingerprint]; !exists {
					cp := ap
					provisionalByFP[ap.IdentityFingerprint] = &cp
				}
			}
			if IsArchProcessVisibleToUser(ap, agentMap, in.OperatorID, in.IsAdmin) {
				matchPool = append(matchPool, ap)
			}
		}

		// 第一轮：完成全部请求的身份解析、新建/转正 ArchProcess 与 DISARMED Process 挂载
		type resolvedItem struct {
			item   EnrollItemInput
			live   DiscoveryProcessGroupDTO
			ap     models.ArchProcess
			proc   models.Process
			dtoIdx int
		}
		resolved := make([]resolvedItem, 0, len(in.Items))
		serviceNamesSet := make(map[string]struct{})
		now := time.Now()

		for _, it := range in.Items {
			lp := liveByKey[it.ProcessKey]
			identity := ResolveProcessIdentity(
				matchPool,
				mountedByArch,
				lp.ExeName,
				lp.Cmdline,
				lp.ProcName,
				it.ForceArchProcessID,
				it.ConfirmLowConfidence,
			)

			if identity.MatchStatus == "AMBIGUOUS" {
				return &EnrollValidationError{
					Code:       "IDENTITY_AMBIGUOUS",
					Message:    fmt.Sprintf("进程 %s 匹配到 %d 个标准架构进程候选，请选择确认", lp.ProcName, len(identity.Candidates)),
					ProcessKey: it.ProcessKey,
					Candidates: identity.Candidates,
				}
			}
			if identity.MatchStatus == "LOW_CONFIDENCE" {
				return &EnrollValidationError{
					Code:       "LOW_CONFIDENCE_CONFIRM_REQUIRED",
					Message:    fmt.Sprintf("进程 %s 仅获取到低置信名称指纹（cmdline 不可读），请二次确认", lp.ProcName),
					ProcessKey: it.ProcessKey,
					Candidates: identity.Candidates,
				}
			}

			var targetAP models.ArchProcess
			resultKind := "MATCHED"

			if identity.Matched != nil {
				targetAP = *identity.Matched
			} else if lockedAgent.Scope == models.ScopeShared && !identity.IsLowConfidence {
				// SHARED 环境即使原 PROVISIONAL 由他人在 PRIVATE 环境创建（对当前用户隐藏），同指纹命中仍自动转正复用
				if prov, ok := provisionalByFP[identity.Fingerprint]; ok {
					targetAP = *prov
				}
			}

			if targetAP.ID == 0 {
				var created bool
				targetAP, err = createDiscoveredArchProcessTx(tx, &lockedAgent, targetGroup.ID, lp, identity, it.DisplayName, in.OperatorID, usedCodes)
				if err != nil {
					return err
				}
				created = true
				if targetAP.Lifecycle == models.ArchProcLifecycleProvisional {
					resultKind = "CREATED_PROVISIONAL"
				} else {
					resultKind = "CREATED_CONFIRMED"
				}
				if created {
					matchPool = append(matchPool, targetAP)
					if targetAP.Lifecycle == models.ArchProcLifecycleProvisional && targetAP.IdentityFingerprint != "" {
						cp := targetAP
						provisionalByFP[targetAP.IdentityFingerprint] = &cp
					}
				}
			} else if lockedAgent.Scope == models.ScopeShared && targetAP.Lifecycle == models.ArchProcLifecycleProvisional {
				// 从 PRIVATE 试用态自动提升为 CONFIRMED 全员可见，并去除 prv<agent_id>- 前缀
				if err := promoteProvisionalArchProcessTx(tx, &targetAP, in.OperatorID, usedCodes); err != nil {
					return err
				}
				for idx := range matchPool {
					if matchPool[idx].ID == targetAP.ID {
						matchPool[idx] = targetAP
						break
					}
				}
			}

			procRow, err := upsertEnrolledProcessTx(tx, &lockedAgent, &targetAP, lp, mountedByArch, now, in.OperatorID)
			if err != nil {
				return err
			}
			mountedByArch[targetAP.ID] = procRow

			for _, svc := range lp.Services {
				cleanSvc := strings.TrimSpace(topology.StripCSGPIDSuffix(svc.ServiceName))
				if cleanSvc != "" {
					serviceNamesSet[cleanSvc] = struct{}{}
				}
			}

			res.Enrolled = append(res.Enrolled, EnrolledProcessDTO{
				ProcessKey:      it.ProcessKey,
				ArchProcessID:   targetAP.ID,
				ArchProcessCode: targetAP.Code,
				ArchProcessName: targetAP.Name,
				GroupID:         targetAP.GroupID,
				Lifecycle:       targetAP.Lifecycle,
				ProcessID:       procRow.ID,
				Result:          resultKind,
			})
			resolved = append(resolved, resolvedItem{
				item:   it,
				live:   lp,
				ap:     targetAP,
				proc:   procRow,
				dtoIdx: len(res.Enrolled) - 1,
			})
		}

		// 第二轮：按服务名升序一次性获取 PostgreSQL 事务级咨询锁，预载并批量写入 ModuleService 与 ServicePlacement
		sortedServiceNames := make([]string, 0, len(serviceNamesSet))
		for name := range serviceNamesSet {
			sortedServiceNames = append(sortedServiceNames, name)
		}
		sort.Strings(sortedServiceNames)

		if len(sortedServiceNames) > 0 {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(s)) FROM unnest(?::text[]) AS s", pqTextArrayLiteral(sortedServiceNames)).Error; err != nil {
				return err
			}
		}

		// 批量预载已存在服务、关联模块（MULTI_IMPL）与历史 ServicePlacement
		contractByName := make(map[string]models.ServiceContract)
		existingSvcsByName := make(map[string][]models.ModuleService)
		modArchMap := make(map[uint64]uint64)
		if len(sortedServiceNames) > 0 {
			var contracts []models.ServiceContract
			if err := tx.Where("service_name IN ?", sortedServiceNames).Find(&contracts).Error; err != nil {
				return err
			}
			for _, sc := range contracts {
				contractByName[sc.ServiceName] = sc
			}

			var existingSvcs []models.ModuleService
			if err := tx.Where("service_name IN ?", sortedServiceNames).Order("id ASC").Find(&existingSvcs).Error; err != nil {
				return err
			}
			modIDSet := make(map[uint64]bool)
			for _, ms := range existingSvcs {
				existingSvcsByName[ms.ServiceName] = append(existingSvcsByName[ms.ServiceName], ms)
				if ms.ModuleID != nil && *ms.ModuleID > 0 {
					modIDSet[*ms.ModuleID] = true
				}
			}
			if len(modIDSet) > 0 {
				modIDs := make([]uint64, 0, len(modIDSet))
				for id := range modIDSet {
					modIDs = append(modIDs, id)
				}
				var mods []models.Module
				if err := tx.Select("id", "arch_process_id").Where("id IN ?", modIDs).Find(&mods).Error; err != nil {
					return err
				}
				for _, m := range mods {
					if m.ArchProcessID != nil {
						modArchMap[m.ID] = *m.ArchProcessID
					}
				}
			}
		}

		// 预载本 Agent 现有放置表，避免 N+1 查询
		type plcTupleKey struct {
			msID   uint64
			procID uint64
		}
		existingPlcMap := make(map[plcTupleKey]models.ServicePlacement)
		var agentPlcs []models.ServicePlacement
		if err := tx.Where("agent_id = ?", in.AgentID).Find(&agentPlcs).Error; err != nil {
			return err
		}
		for _, p := range agentPlcs {
			existingPlcMap[plcTupleKey{msID: p.ModuleServiceID, procID: p.ProcessID}] = p
		}

		// 先识别并批量创建缺失的未认领 ModuleService
		var newServicesToCreate []models.ModuleService
		newServiceIdxByName := make(map[string]int)
		resolvedSvcForItem := make([]models.ModuleService, 0)
		type pendingPlacementItem struct {
			svcName string
			procID  uint64
			agentID uint64
			archID  uint64
			svcDTO  DiscoveryLiveServiceDTO
			dtoIdx  int
		}
		pendingPlacements := make([]pendingPlacementItem, 0)

		for _, ri := range resolved {
			for _, svc := range ri.live.Services {
				cleanSvc := strings.TrimSpace(topology.StripCSGPIDSuffix(svc.ServiceName))
				if cleanSvc == "" {
					continue
				}
				ms, found := pickModuleServiceFromCache(existingSvcsByName[cleanSvc], ri.ap.ID, modArchMap)
				if !found {
					if _, queued := newServiceIdxByName[cleanSvc]; !queued {
						newMS := models.ModuleService{
							ModuleID:    nil,
							ServiceName: cleanSvc,
							Source:      models.ServiceSourceCSGDiscovered,
						}
						if sc, ok := contractByName[cleanSvc]; ok {
							newMS.ProtoSuggestedName = sc.ServiceName
							newMS.ProtoMatchConfidence = models.ProtoMatchConfExact
						}
						newServiceIdxByName[cleanSvc] = len(newServicesToCreate)
						newServicesToCreate = append(newServicesToCreate, newMS)
						res.Enrolled[ri.dtoIdx].ServicesCreated++
					}
				}
				_ = ms
				_ = resolvedSvcForItem
				pendingPlacements = append(pendingPlacements, pendingPlacementItem{
					svcName: cleanSvc,
					procID:  ri.proc.ID,
					agentID: lockedAgent.ID,
					archID:  ri.ap.ID,
					svcDTO:  svc,
					dtoIdx:  ri.dtoIdx,
				})
			}
		}

		if len(newServicesToCreate) > 0 {
			if err := tx.CreateInBatches(&newServicesToCreate, 100).Error; err != nil {
				return err
			}
			for i := range newServicesToCreate {
				existingSvcsByName[newServicesToCreate[i].ServiceName] = append(existingSvcsByName[newServicesToCreate[i].ServiceName], newServicesToCreate[i])
			}
		}

		var newPlcsToCreate []models.ServicePlacement
		for _, pp := range pendingPlacements {
			ms, _ := pickModuleServiceFromCache(existingSvcsByName[pp.svcName], pp.archID, modArchMap)
			endpointsJSON, _ := json.Marshal(pp.svcDTO.Endpoints)
			transport := strings.TrimSpace(pp.svcDTO.Transport)
			if transport == "" {
				transport = models.ObservedTransportTCP
			}
			instCount := pp.svcDTO.InstanceCount
			if instCount <= 0 {
				instCount = len(pp.svcDTO.ServerPIDs)
			}
			if instCount <= 0 {
				instCount = 1
			}
			key := plcTupleKey{msID: ms.ID, procID: pp.procID}
			if plc, ok := existingPlcMap[key]; ok {
				if err := tx.Model(&models.ServicePlacement{}).Where("id = ?", plc.ID).Updates(map[string]interface{}{
					"raw_service_name":   pp.svcDTO.RawServiceName,
					"endpoints":          datatypes.JSON(endpointsJSON),
					"observed_transport": transport,
					"instance_count":     instCount,
					"status":             models.PlacementStatusActive,
					"miss_count":         0,
					"last_seen_at":       now,
				}).Error; err != nil {
					return err
				}
			} else {
				newPlc := models.ServicePlacement{
					ModuleServiceID:   ms.ID,
					ProcessID:         pp.procID,
					AgentID:           pp.agentID,
					ArchProcessID:     pp.archID,
					RawServiceName:    pp.svcDTO.RawServiceName,
					Endpoints:         datatypes.JSON(endpointsJSON),
					ObservedTransport: transport,
					InstanceCount:     instCount,
					Status:            models.PlacementStatusActive,
					MissCount:         0,
					FirstSeenAt:       now,
					LastSeenAt:        now,
				}
				newPlcsToCreate = append(newPlcsToCreate, newPlc)
				existingPlcMap[key] = newPlc
			}
			res.Enrolled[pp.dtoIdx].PlacementsUpdated++
			res.PlacementsCount++
		}
		if len(newPlcsToCreate) > 0 {
			if err := tx.CreateInBatches(&newPlcsToCreate, 100).Error; err != nil {
				return err
			}
		}

		// 第三轮：预载现有依赖并批量写入 ProcessDependency（需在同批 Placement 落库后解析 provider_process_id / service_id）
		type plcProviderRow struct {
			ServiceName     string
			ProcessID       uint64
			ArchProcessID   uint64
			ModuleServiceID uint64
		}
		localProviderBySvc := make(map[string]plcProviderRow)
		var plcRows []plcProviderRow
		if err := tx.Table("contract_service_placements AS sp").
			Select("ms.service_name, sp.process_id, sp.arch_process_id, sp.module_service_id").
			Joins("JOIN contract_module_services AS ms ON ms.id = sp.module_service_id").
			Where("sp.agent_id = ? AND sp.status = ?", in.AgentID, models.PlacementStatusActive).
			Order("sp.process_id ASC").
			Scan(&plcRows).Error; err != nil {
			return err
		}
		for _, r := range plcRows {
			if _, exists := localProviderBySvc[r.ServiceName]; !exists {
				localProviderBySvc[r.ServiceName] = r
			}
		}

		type depTupleKey struct {
			consumerProcID uint64
			svcName        string
		}
		existingDepMap := make(map[depTupleKey]models.ProcessDependency)
		var agentDeps []models.ProcessDependency
		if err := tx.Where("agent_id = ?", in.AgentID).Find(&agentDeps).Error; err != nil {
			return err
		}
		for _, d := range agentDeps {
			existingDepMap[depTupleKey{consumerProcID: d.ConsumerProcessID, svcName: d.ServiceName}] = d
		}

		var newDepsToCreate []models.ProcessDependency
		for _, ri := range resolved {
			seenDeps := make(map[string]bool)
			for _, sub := range ri.live.Subscriptions {
				cleanSvc := strings.TrimSpace(topology.StripCSGPIDSuffix(sub.ServiceName))
				if cleanSvc == "" || seenDeps[cleanSvc] {
					continue
				}
				seenDeps[cleanSvc] = true

				providerKind := models.ProviderKindOffline
				var providerProcID *uint64
				var providerArchProcID *uint64
				var serviceID *uint64
				if row, ok := localProviderBySvc[cleanSvc]; ok && row.ProcessID > 0 {
					providerKind = models.ProviderKindLocalEnrolled
					pidCopy := row.ProcessID
					providerProcID = &pidCopy
					apCopy := row.ArchProcessID
					providerArchProcID = &apCopy
					sidCopy := row.ModuleServiceID
					serviceID = &sidCopy
				} else if sub.ProviderKind == models.ProviderKindRemoteLDC {
					providerKind = models.ProviderKindRemoteLDC
				} else if sub.ProviderKind == models.ProviderKindLocalUnenrolled {
					providerKind = models.ProviderKindLocalUnenrolled
				}

				dKey := depTupleKey{consumerProcID: ri.proc.ID, svcName: cleanSvc}
				if dep, ok := existingDepMap[dKey]; ok {
					if err := tx.Model(&models.ProcessDependency{}).Where("id = ?", dep.ID).Updates(map[string]interface{}{
						"service_id":               serviceID,
						"provider_kind":            providerKind,
						"provider_process_id":      providerProcID,
						"provider_arch_process_id": providerArchProcID,
						"remote_node":              sub.RemoteNode,
						"resolved_endpoint":        sub.ProviderEndpoint,
						"source":                   models.ProcDepSourceCSGSub,
						"status":                   models.PlacementStatusActive,
						"miss_count":               0,
						"last_seen_at":             now,
					}).Error; err != nil {
						return err
					}
				} else {
					newDep := models.ProcessDependency{
						ConsumerProcessID:     ri.proc.ID,
						AgentID:               lockedAgent.ID,
						ConsumerArchProcessID: ri.ap.ID,
						ServiceName:           cleanSvc,
						ServiceID:             serviceID,
						ProviderKind:          providerKind,
						ProviderProcessID:     providerProcID,
						ProviderArchProcessID: providerArchProcID,
						RemoteNode:            sub.RemoteNode,
						ResolvedEndpoint:      sub.ProviderEndpoint,
						Source:                models.ProcDepSourceCSGSub,
						Status:                models.PlacementStatusActive,
						MissCount:             0,
						FirstSeenAt:           now,
						LastSeenAt:            now,
					}
					newDepsToCreate = append(newDepsToCreate, newDep)
					existingDepMap[dKey] = newDep
				}
				res.DependenciesCount++
			}
		}
		if len(newDepsToCreate) > 0 {
			if err := tx.CreateInBatches(&newDepsToCreate, 100).Error; err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		return nil, err
	}

	// 事务提交成功后立即失效缓存并刷新 AUTO 快照（同步执行，避免并发读脏与单测删库竞争）
	liveSvc.InvalidateCache(in.AgentID)
	if refreshed, snapErr := liveSvc.GetLiveDiscovery(ctx, in.AgentID, in.CSGAddr, false, in.OperatorID, in.IsAdmin); snapErr == nil && refreshed != nil {
		if snapRes, err := SaveSnapshot(db, SaveSnapshotInput{
			AgentID:     in.AgentID,
			CSGAddr:     refreshed.CSGAddr,
			TriggerMode: models.DiscoveryTriggerAuto,
			CreatedBy:   in.OperatorID,
			IsAdmin:     in.IsAdmin,
			Live:        refreshed,
		}); err == nil && snapRes != nil && snapRes.Snapshot != nil {
			res.SnapshotID = &snapRes.Snapshot.ID
		}
	}

	return res, nil
}

func ensureCSGAutoProcessGroupTx(tx *gorm.DB) (models.ArchProcessGroup, error) {
	var grp models.ArchProcessGroup
	if err := tx.Where("code = ?", defaultCSGAutoProcessGrpCode).First(&grp).Error; err == nil {
		return grp, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return grp, err
	}
	grp = models.ArchProcessGroup{
		Code:        defaultCSGAutoProcessGrpCode,
		Name:        "现网发现自动纳管进程组",
		Description: "CSG 零侵扰物理发现自动建档的默认进程归属组",
		SortOrder:   999,
	}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "code"}}, DoNothing: true}).Create(&grp).Error; err != nil {
		return grp, err
	}
	if err := tx.Where("code = ?", defaultCSGAutoProcessGrpCode).First(&grp).Error; err != nil {
		return grp, err
	}
	return grp, nil
}

func createDiscoveredArchProcessTx(
	tx *gorm.DB,
	agent *models.Agent,
	groupID uint64,
	lp DiscoveryProcessGroupDTO,
	idRes IdentityResolution,
	displayName string,
	operatorID uint,
	usedCodes map[string]uint64,
) (models.ArchProcess, error) {
	norm := NormalizeArchProcessCode(firstNonEmpty(lp.ProcName, filepath.Base(lp.ExeName), "proc"))
	lifecycle := models.ArchProcLifecycleConfirmed
	baseCode := norm
	if agent.Scope == models.ScopePrivate {
		lifecycle = models.ArchProcLifecycleProvisional
		baseCode = fmt.Sprintf(defaultProvisionalPfxFmt, agent.ID, norm)
	}
	uniqueCode, err := allocateUniqueArchProcessCodeInMap(usedCodes, baseCode, 0)
	if err != nil {
		return models.ArchProcess{}, err
	}

	name := strings.TrimSpace(displayName)
	if name == "" {
		name = firstNonEmpty(lp.ProcName, filepath.Base(lp.ExeName), uniqueCode)
	}
	role := lp.ObservedRole
	if role == "" {
		role = models.ArchProcRoleServer
	}
	cmdPat := strings.TrimSpace(lp.Cmdline)
	if cmdPat == "" {
		cmdPat = strings.TrimSpace(firstNonEmpty(lp.ExeName, lp.ProcName))
	}
	originAgentID := agent.ID
	ap := models.ArchProcess{
		GroupID:             groupID,
		Code:                uniqueCode,
		Name:                name,
		DefaultCmdPattern:   cmdPat,
		DeclaredRole:        role,
		IdentityFingerprint: idRes.Fingerprint,
		FingerprintVersion:  CurrentFingerprintVersion,
		Lifecycle:           lifecycle,
		Origin:              models.ArchProcOriginDiscovery,
		OriginAgentID:       &originAgentID,
		CreatedBy:           operatorID,
		UpdatedBy:           operatorID,
	}
	if err := tx.Create(&ap).Error; err != nil {
		return models.ArchProcess{}, err
	}
	usedCodes[ap.Code] = ap.ID
	return ap, nil
}

func promoteProvisionalArchProcessTx(tx *gorm.DB, ap *models.ArchProcess, operatorID uint, usedCodes map[string]uint64) error {
	targetCode := ap.Code
	if ap.OriginAgentID != nil && *ap.OriginAgentID > 0 {
		pfx := fmt.Sprintf("prv%d-", *ap.OriginAgentID)
		if strings.HasPrefix(targetCode, pfx) {
			targetCode = strings.TrimPrefix(targetCode, pfx)
		}
	}
	targetCode = NormalizeArchProcessCode(targetCode)
	uniqueCode, err := allocateUniqueArchProcessCodeInMap(usedCodes, targetCode, ap.ID)
	if err != nil {
		return err
	}
	updates := map[string]interface{}{
		"lifecycle":  models.ArchProcLifecycleConfirmed,
		"code":       uniqueCode,
		"updated_by": operatorID,
	}
	if err := tx.Model(&models.ArchProcess{}).Where("id = ?", ap.ID).Updates(updates).Error; err != nil {
		return err
	}
	delete(usedCodes, ap.Code)
	usedCodes[uniqueCode] = ap.ID
	ap.Lifecycle = models.ArchProcLifecycleConfirmed
	ap.Code = uniqueCode
	return nil
}

func allocateUniqueArchProcessCodeInMap(usedCodes map[string]uint64, baseCode string, excludeID uint64) (string, error) {
	if len(baseCode) > 56 {
		baseCode = strings.TrimRight(baseCode[:56], "-_")
	}
	if baseCode == "" {
		baseCode = "proc"
	}
	candidate := baseCode
	for suffix := 0; suffix < 64; suffix++ {
		if suffix > 0 {
			candidate = fmt.Sprintf("%s-%d", baseCode, suffix+1)
		}
		existingID, exists := usedCodes[candidate]
		if !exists || (excludeID > 0 && existingID == excludeID) {
			return candidate, nil
		}
	}
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s", baseCode, hex.EncodeToString(buf)), nil
}

func pqTextArrayLiteral(items []string) string {
	escaped := make([]string, len(items))
	for i, s := range items {
		r := strings.ReplaceAll(s, `\`, `\\`)
		r = strings.ReplaceAll(r, `"`, `\"`)
		escaped[i] = `"` + r + `"`
	}
	return "{" + strings.Join(escaped, ",") + "}"
}

func pickModuleServiceFromCache(existing []models.ModuleService, archProcessID uint64, modArchMap map[uint64]uint64) (models.ModuleService, bool) {
	if len(existing) == 0 {
		return models.ModuleService{}, false
	}
	if len(existing) == 1 {
		return existing[0], true
	}
	for _, ms := range existing {
		if ms.ModuleID != nil && modArchMap[*ms.ModuleID] == archProcessID {
			return ms, true
		}
	}
	for _, ms := range existing {
		if ms.ModuleID == nil {
			return ms, true
		}
	}
	return existing[0], true
}

func upsertEnrolledProcessTx(
	tx *gorm.DB,
	agent *models.Agent,
	ap *models.ArchProcess,
	lp DiscoveryProcessGroupDTO,
	mountedByArch map[uint64]models.Process,
	now time.Time,
	operatorID uint,
) (models.Process, error) {
	transportsJSON, _ := json.Marshal(lp.ObservedTransports)
	pidsJSON, _ := json.Marshal(lp.PIDs)
	listenPort := 0
	if len(lp.ListenPorts) > 0 {
		listenPort = int(lp.ListenPorts[0])
	}

	if existing, ok := mountedByArch[ap.ID]; ok {
		// 仅刷新物理观测列与 updated_by；绝不覆盖 MANUAL 挂载的 enroll_source、probe_desired_state、probe_status、override_cmd_pattern
		updates := map[string]interface{}{
			"observed_role":       lp.ObservedRole,
			"observed_transports": datatypes.JSON(transportsJSON),
			"observed_pids":       datatypes.JSON(pidsJSON),
			"last_observed_at":    &now,
			"updated_by":          operatorID,
		}
		if existing.EnrollSource == models.EnrollSourceDiscovery && existing.EffectiveListenPort == 0 && listenPort > 0 {
			updates["effective_listen_port"] = listenPort
			existing.EffectiveListenPort = listenPort
		}
		if err := tx.Model(&models.Process{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
			return models.Process{}, err
		}
		existing.ObservedRole = lp.ObservedRole
		existing.ObservedTransports = datatypes.JSON(transportsJSON)
		existing.ObservedPIDs = datatypes.JSON(pidsJSON)
		existing.LastObservedAt = &now
		return existing, nil
	}

	proc := models.Process{
		AgentID:             agent.ID,
		ArchProcessID:       ap.ID,
		EffectiveListenPort: listenPort,
		ExtraDownstreams:    datatypes.JSON("[]"),
		EnrollSource:        models.EnrollSourceDiscovery,
		ObservedRole:        lp.ObservedRole,
		ObservedTransports:  datatypes.JSON(transportsJSON),
		ObservedPIDs:        datatypes.JSON(pidsJSON),
		LastObservedAt:      &now,
		ProbeDesiredState:   models.ProbeDesiredStateDisarmed,
		ProbeStatus:         models.ProbeStatusDisarmed,
		CreatedBy:           operatorID,
		UpdatedBy:           operatorID,
	}
	if err := tx.Create(&proc).Error; err != nil {
		return models.Process{}, err
	}
	return proc, nil
}
