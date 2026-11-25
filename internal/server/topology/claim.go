package topology

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"code-contract/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrClaimForbidden      = errors.New("无权将服务认领到目标功能模块（仅模块负责人、子系统负责人或管理员可操作）")
	ErrClaimEmptyItems     = errors.New("请至少选择一项待认领服务")
	ErrClaimAlreadyClaimed = errors.New("目标服务已被其他模块认领")
	ErrUnclaimNotClaimed   = errors.New("目标服务尚未被认领，无需取消")
	ErrSplitInvalidInput   = errors.New("多实现拆分参数不合法")
	ErrArchProcessNotFound = errors.New("标准架构物理进程不存在")
)

const (
	UnclaimActionUnclaimed = "UNCLAIMED"
	UnclaimActionMerged    = "MERGED"
	UnclaimActionDeleted   = "DELETED"
)

// ClaimPlacementMismatchError 不变量 I4 阻断错误（HTTP 422 CLAIM_PLACEMENT_MISMATCH）
type ClaimPlacementMismatchError struct {
	Code                  string   `json:"code"`
	Message               string   `json:"error"`
	ServiceID             uint64   `json:"service_id"`
	ServiceName           string   `json:"service_name"`
	ExpectedArchProcessID *uint64  `json:"expected_arch_process_id,omitempty"`
	ActualArchProcessIDs  []uint64 `json:"actual_arch_process_ids,omitempty"`
}

func (e *ClaimPlacementMismatchError) Error() string {
	return e.Message
}

// ClaimItem 单宿主认领条目
type ClaimItem struct {
	ServiceID uint64 `json:"service_id"`
	ModuleID  uint64 `json:"module_id"`
	ImplKey   string `json:"impl_key,omitempty"`
}

// ClaimResult 单宿主批量认领结果
type ClaimResult struct {
	ClaimedCount     int     `json:"claimed_count"`
	AttributionJobID *uint64 `json:"attribution_job_id,omitempty"`
}

// SplitImplInput 多宿主拆分单个物理进程实现配置
type SplitImplInput struct {
	ArchProcessID uint64 `json:"arch_process_id"`
	ModuleID      uint64 `json:"module_id"`
	ImplKey       string `json:"impl_key"`
}

// SplitMultiImplInput 多宿主未认领服务拆分入参
type SplitMultiImplInput struct {
	SourceServiceID uint64           `json:"source_service_id"`
	DefinerModuleID uint64           `json:"definer_module_id"`
	Implementations []SplitImplInput `json:"implementations"`
}

// SplitMultiImplResult 多宿主拆分结果
type SplitMultiImplResult struct {
	Contract         *models.ServiceContract `json:"contract"`
	Implementations  []models.ModuleService  `json:"implementations"`
	AttributionJobID *uint64                 `json:"attribution_job_id,omitempty"`
}

// UnclaimResult 取消认领结果
type UnclaimResult struct {
	Action    string `json:"action"`
	ServiceID uint64 `json:"service_id"`
}

func canOperateModule(op Operator, mod *models.Module) bool {
	if op.IsAdmin {
		return true
	}
	if op.UserID == 0 || mod == nil {
		return false
	}
	if mod.OwnerID != nil && *mod.OwnerID == op.UserID {
		return true
	}
	if mod.Subsystem != nil && mod.Subsystem.OwnerID != nil && *mod.Subsystem.OwnerID == op.UserID {
		return true
	}
	return false
}

// queryAuthorityActiveArchProcessIDs 查询服务在权威 SHARED + CONFIRMED 环境的 ACTIVE 放置唯一 ArchProcessID 集合
func queryAuthorityActiveArchProcessIDs(tx *gorm.DB, serviceID uint64) ([]uint64, error) {
	var pls []models.ServicePlacement
	if err := tx.Preload("Agent").Preload("ArchProcess").
		Where("module_service_id = ? AND status = ?", serviceID, models.PlacementStatusActive).
		Find(&pls).Error; err != nil {
		return nil, err
	}
	seen := make(map[uint64]struct{})
	out := make([]uint64, 0, len(pls))
	for _, pl := range pls {
		if pl.Agent == nil || pl.Agent.Scope != models.ScopeShared {
			continue
		}
		if pl.ArchProcess == nil || pl.ArchProcess.Lifecycle != models.ArchProcLifecycleConfirmed {
			continue
		}
		if _, ok := seen[pl.ArchProcessID]; !ok {
			seen[pl.ArchProcessID] = struct{}{}
			out = append(out, pl.ArchProcessID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func enqueueReattributeJobTx(tx *gorm.DB, serviceNames []string, triggerType string, targetID uint64, userID uint) (*uint64, error) {
	if len(serviceNames) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(serviceNames))
	uniq := make([]string, 0, len(serviceNames))
	for _, n := range serviceNames {
		trimmed := strings.TrimSpace(n)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; !ok {
			seen[trimmed] = struct{}{}
			uniq = append(uniq, trimmed)
		}
	}
	if len(uniq) == 0 {
		return nil, nil
	}
	scopePayload, _ := json.Marshal(map[string]interface{}{
		"service_names": uniq,
		"trigger_type":  triggerType,
		"target_id":     targetID,
	})
	job := models.AttributionJob{
		JobType:   models.AttrJobTypeReattribute,
		ScopeJSON: datatypes.JSON(scopePayload),
		Status:    models.AttrJobStatusPending,
		CreatedBy: userID,
	}
	if err := tx.Create(&job).Error; err != nil {
		return nil, err
	}
	jobID := job.ID
	return &jobID, nil
}

// ClaimServices 单宿主批量认领（支持 1 进程 : N 模块、ModuleOwnerOrAdminGuard 与不变量 I4）
func ClaimServices(ctx context.Context, db *gorm.DB, op Operator, items []ClaimItem) (*ClaimResult, error) {
	if len(items) == 0 {
		return nil, ErrClaimEmptyItems
	}
	seenSvc := make(map[uint64]struct{}, len(items))
	for _, it := range items {
		if it.ServiceID == 0 || it.ModuleID == 0 {
			return nil, ErrSplitInvalidInput
		}
		if _, dup := seenSvc[it.ServiceID]; dup {
			return nil, ErrSplitInvalidInput
		}
		seenSvc[it.ServiceID] = struct{}{}
	}

	var result ClaimResult
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		modCache := make(map[uint64]*models.Module)
		modProcCache := make(map[uint64]*uint64)
		svcList := make([]*models.ModuleService, 0, len(items))

		// 1. 预加载并严格检验存在性与 RBAC（任意一项越权则整批 403 零写入）
		for _, it := range items {
			var svc models.ModuleService
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&svc, it.ServiceID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrModuleServiceNotFound
				}
				return err
			}
			svcList = append(svcList, &svc)

			mod, ok := modCache[it.ModuleID]
			if !ok {
				var loaded models.Module
				if err := tx.Preload("Subsystem").Clauses(clause.Locking{Strength: "UPDATE"}).First(&loaded, it.ModuleID).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return ErrModuleNotFound
					}
					return err
				}
				mod = &loaded
				modCache[it.ModuleID] = mod
				modProcCache[it.ModuleID] = mod.ArchProcessID
			}
			if !canOperateModule(op, mod) {
				return ErrClaimForbidden
			}
		}

		// 2. 逐项校验业务约束、I4 放置一致性并执行认领与 ArchProcessID 回填
		now := time.Now().UTC()
		svcNames := make([]string, 0, len(items))

		for idx, it := range items {
			svc := svcList[idx]
			mod := modCache[it.ModuleID]
			if mod.ModuleKind == models.ModuleKindContractLib {
				return ErrContractLibNoProvide
			}
			if svc.IsClaimed() {
				return ErrClaimAlreadyClaimed
			}

			apIDs, err := queryAuthorityActiveArchProcessIDs(tx, svc.ID)
			if err != nil {
				return err
			}
			if len(apIDs) != 1 {
				return &ClaimPlacementMismatchError{
					Code:                  "CLAIM_PLACEMENT_MISMATCH",
					Message:               fmt.Sprintf("服务 [%s] 在权威环境存在 %d 个活跃物理进程，不满足单宿主认领条件", svc.ServiceName, len(apIDs)),
					ServiceID:             svc.ID,
					ServiceName:           svc.ServiceName,
					ExpectedArchProcessID: modProcCache[mod.ID],
					ActualArchProcessIDs:  apIDs,
				}
			}
			procP := apIDs[0]
			expectedP := modProcCache[mod.ID]
			if expectedP != nil && *expectedP != procP {
				return &ClaimPlacementMismatchError{
					Code:                  "CLAIM_PLACEMENT_MISMATCH",
					Message:               fmt.Sprintf("服务 [%s] 位于进程 #%d，与目标模块 [%s] 绑定的进程 #%d 不一致", svc.ServiceName, procP, mod.Name, *expectedP),
					ServiceID:             svc.ID,
					ServiceName:           svc.ServiceName,
					ExpectedArchProcessID: expectedP,
					ActualArchProcessIDs:  apIDs,
				}
			}
			if expectedP == nil {
				if err := tx.Model(&models.Module{}).Where("id = ?", mod.ID).Update("arch_process_id", procP).Error; err != nil {
					return err
				}
				pCopy := procP
				modProcCache[mod.ID] = &pCopy
				mod.ArchProcessID = &pCopy
			}

			if err := tx.Model(&models.ModuleService{}).Where("id = ?", svc.ID).Updates(map[string]interface{}{
				"module_id":  mod.ID,
				"claimed_at": &now,
				"claimed_by": op.UserID,
			}).Error; err != nil {
				return err
			}
			svcNames = append(svcNames, svc.ServiceName)
		}

		jobID, err := enqueueReattributeJobTx(tx, svcNames, "SERVICE_CLAIM", items[0].ServiceID, op.UserID)
		if err != nil {
			return err
		}
		result.ClaimedCount = len(items)
		result.AttributionJobID = jobID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// SplitMultiImpl 管理员将多宿主未认领服务拆分为 ServiceContract + 多个 MULTI_IMPL 实现行并迁移放置
func SplitMultiImpl(ctx context.Context, db *gorm.DB, op Operator, in SplitMultiImplInput) (*SplitMultiImplResult, error) {
	if !op.IsAdmin {
		return nil, ErrClaimForbidden
	}
	if in.SourceServiceID == 0 || in.DefinerModuleID == 0 || len(in.Implementations) < 2 {
		return nil, ErrSplitInvalidInput
	}

	// 基础结构与去重检验
	seenAP := make(map[uint64]struct{}, len(in.Implementations))
	seenMod := make(map[uint64]struct{}, len(in.Implementations))
	seenKey := make(map[string]struct{}, len(in.Implementations))
	for i := range in.Implementations {
		impl := &in.Implementations[i]
		impl.ImplKey = strings.TrimSpace(impl.ImplKey)
		if impl.ArchProcessID == 0 || impl.ModuleID == 0 || !implKeyRegex.MatchString(impl.ImplKey) {
			return nil, ErrSplitInvalidInput
		}
		if _, ok := seenAP[impl.ArchProcessID]; ok {
			return nil, ErrSplitInvalidInput
		}
		if _, ok := seenMod[impl.ModuleID]; ok {
			return nil, ErrSplitInvalidInput
		}
		if _, ok := seenKey[impl.ImplKey]; ok {
			return nil, ErrSplitInvalidInput
		}
		seenAP[impl.ArchProcessID] = struct{}{}
		seenMod[impl.ModuleID] = struct{}{}
		seenKey[impl.ImplKey] = struct{}{}
	}

	var res SplitMultiImplResult
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var src models.ModuleService
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&src, in.SourceServiceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrModuleServiceNotFound
			}
			return err
		}
		if src.IsClaimed() || src.ContractID != nil {
			return ErrClaimAlreadyClaimed
		}

		var definer models.Module
		if err := tx.First(&definer, in.DefinerModuleID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrModuleNotFound
			}
			return err
		}
		if definer.ModuleKind != models.ModuleKindContractLib {
			return ErrDefinerNotContractLib
		}

		if err := lockServiceNamesSorted(tx, src.ServiceName); err != nil {
			return err
		}

		// 校验源服务权威环境 ACTIVE 放置必须全部被 implementations 覆盖
		activeAPIDs, err := queryAuthorityActiveArchProcessIDs(tx, src.ID)
		if err != nil {
			return err
		}
		for _, apID := range activeAPIDs {
			if _, covered := seenAP[apID]; !covered {
				return ErrSplitInvalidInput
			}
		}

		// 校验每个目标 ArchProcess 与目标 Module 的存在性及 I4 一致性
		targetMods := make(map[uint64]*models.Module, len(in.Implementations))
		for _, impl := range in.Implementations {
			var ap models.ArchProcess
			if err := tx.Select("id").First(&ap, impl.ArchProcessID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrSplitInvalidInput
				}
				return err
			}
			var m models.Module
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&m, impl.ModuleID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrModuleNotFound
				}
				return err
			}
			if m.ModuleKind == models.ModuleKindContractLib {
				return ErrContractLibNoProvide
			}
			if m.ArchProcessID != nil && *m.ArchProcessID != impl.ArchProcessID {
				return &ClaimPlacementMismatchError{
					Code:                  "CLAIM_PLACEMENT_MISMATCH",
					Message:               fmt.Sprintf("模块 [%s] 已绑定进程 #%d，与拆分配对进程 #%d 不一致", m.Name, *m.ArchProcessID, impl.ArchProcessID),
					ServiceID:             src.ID,
					ServiceName:           src.ServiceName,
					ExpectedArchProcessID: m.ArchProcessID,
					ActualArchProcessIDs:  []uint64{impl.ArchProcessID},
				}
			}
			targetMods[impl.ModuleID] = &m
		}

		// 新建或复用 ServiceContract
		var contract models.ServiceContract
		findErr := tx.Where("service_name = ?", src.ServiceName).First(&contract).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			contract = models.ServiceContract{
				ServiceName:     src.ServiceName,
				DefinerModuleID: in.DefinerModuleID,
				ProvideMode:     models.ContractProvideModeMultiImpl,
				CreatedBy:       op.UserID,
				UpdatedBy:       op.UserID,
			}
			if err := tx.Create(&contract).Error; err != nil {
				return err
			}
		} else if findErr != nil {
			return findErr
		} else if contract.DefinerModuleID != in.DefinerModuleID {
			return ErrServiceNameOccupiedByContract
		}

		now := time.Now().UTC()
		createdImpls := make([]models.ModuleService, 0, len(in.Implementations))
		implByAP := make(map[uint64]uint64, len(in.Implementations))

		for _, item := range in.Implementations {
			m := targetMods[item.ModuleID]
			if m.ArchProcessID == nil {
				if err := tx.Model(&models.Module{}).Where("id = ?", m.ID).Update("arch_process_id", item.ArchProcessID).Error; err != nil {
					return err
				}
				apCopy := item.ArchProcessID
				m.ArchProcessID = &apCopy
			}

			if err := AssertContractImplProcessDisjoint(tx, m.ID, &contract.ID, m.ArchProcessID); err != nil {
				return err
			}
			if err := ValidateContractRealizationGuard(tx, contract.ID, m.ID); err != nil {
				return err
			}

			diffPaths := src.DiffIgnorePaths
			if len(diffPaths) == 0 {
				diffPaths = datatypes.JSON("[]")
			}
			newImpl := models.ModuleService{
				ModuleID:             models.Uint64Ptr(m.ID),
				ContractID:           &contract.ID,
				ServiceName:          src.ServiceName,
				ProtoServiceName:     src.ProtoServiceName,
				ImplKey:              item.ImplKey,
				IsCustomServiceName:  false,
				Source:               src.Source,
				ProtoMatchConfidence: src.ProtoMatchConfidence,
				ProtoSuggestedName:   src.ProtoSuggestedName,
				ClaimedAt:            &now,
				ClaimedBy:            op.UserID,
				DiffIgnorePaths:      diffPaths,
				Description:          src.Description,
				CreatedBy:            op.UserID,
			}
			if err := tx.Create(&newImpl).Error; err != nil {
				return err
			}

			// 按 arch_process_id 将源服务权威放置迁移至对应实现行
			if err := tx.Model(&models.ServicePlacement{}).
				Where("module_service_id = ? AND arch_process_id = ?", src.ID, item.ArchProcessID).
				Update("module_service_id", newImpl.ID).Error; err != nil {
				return err
			}

			implByAP[item.ArchProcessID] = newImpl.ID
			createdImpls = append(createdImpls, newImpl)
		}

		// 若原未认领身份名下无残留放置（如无 PRIVATE 放置），迁移/清空引用并删除原未认领身份行
		var remainingPls int64
		if err := tx.Model(&models.ServicePlacement{}).Where("module_service_id = ?", src.ID).Count(&remainingPls).Error; err != nil {
			return err
		}
		if remainingPls == 0 {
			var pdeps []models.ProcessDependency
			if err := tx.Where("service_id = ?", src.ID).Find(&pdeps).Error; err != nil {
				return err
			}
			for _, pd := range pdeps {
				var targetSvcID *uint64
				if pd.ProviderArchProcessID != nil {
					if mapped, ok := implByAP[*pd.ProviderArchProcessID]; ok {
						idCopy := mapped
						targetSvcID = &idCopy
					}
				}
				if err := tx.Model(&models.ProcessDependency{}).Where("id = ?", pd.ID).Update("service_id", targetSvcID).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("service_id = ?", src.ID).Delete(&models.DependencyDecision{}).Error; err != nil {
				return err
			}
			if err := tx.Delete(&models.ModuleService{}, src.ID).Error; err != nil {
				return err
			}
		}

		jobID, err := enqueueReattributeJobTx(tx, []string{src.ServiceName}, "SPLIT_MULTI_IMPL", contract.ID, op.UserID)
		if err != nil {
			return err
		}

		res.Contract = &contract
		res.Implementations = createdImpls
		res.AttributionJobID = jobID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// UnclaimService 取消服务认领（执行 ModuleOwnerOrAdminGuard、不变量 I6 依赖拦截与 I7 回退/合并）
func UnclaimService(ctx context.Context, db *gorm.DB, op Operator, serviceID uint64) (*UnclaimResult, error) {
	if serviceID == 0 {
		return nil, ErrModuleServiceNotFound
	}
	var res UnclaimResult
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var svc models.ModuleService
		if err := tx.Preload("Module.Subsystem").Clauses(clause.Locking{Strength: "UPDATE"}).First(&svc, serviceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrModuleServiceNotFound
			}
			return err
		}
		if !svc.IsClaimed() {
			return ErrUnclaimNotClaimed
		}
		if !canOperateModule(op, svc.Module) {
			return ErrClaimForbidden
		}

		// I6 依赖守卫：被 ModuleDependency.service_id 引用则返回 409 DEPENDENCY_REFERENCED
		var refDeps []models.ModuleDependency
		if err := tx.Preload("ConsumerModule").Where("service_id = ?", svc.ID).Order("id ASC").Find(&refDeps).Error; err != nil {
			return err
		}
		if len(refDeps) > 0 {
			refs := make([]ReferencingModuleDTO, 0, len(refDeps))
			for _, d := range refDeps {
				ref := ReferencingModuleDTO{
					ModuleID:    d.ConsumerModuleID,
					ServiceID:   svc.ID,
					ServiceName: svc.ServiceName,
				}
				if d.ConsumerModule != nil {
					ref.ModuleCode = d.ConsumerModule.Code
					ref.ModuleName = d.ConsumerModule.Name
				}
				refs = append(refs, ref)
			}
			return &DependencyReferencedError{
				Code:               "DEPENDENCY_REFERENCED",
				Message:            fmt.Sprintf("服务 [%s] 正被 %d 个下游模块依赖引用，请先解除逻辑依赖", svc.ServiceName, len(refs)),
				ReferencingModules: refs,
			}
		}

		// I7：无放置的纯手工服务直接删除；否则合并到既有未认领身份或原地回退为 MIGRATED
		var plcCount int64
		if err := tx.Model(&models.ServicePlacement{}).Where("module_service_id = ?", svc.ID).Count(&plcCount).Error; err != nil {
			return err
		}
		if plcCount == 0 && svc.Source == models.ServiceSourceManual {
			if err := tx.Delete(&models.ModuleService{}, svc.ID).Error; err != nil {
				return err
			}
			res = UnclaimResult{Action: UnclaimActionDeleted, ServiceID: svc.ID}
			return nil
		}

		var existingUnclaimed models.ModuleService
		findErr := tx.Where("service_name = ? AND module_id IS NULL AND id <> ?", svc.ServiceName, svc.ID).First(&existingUnclaimed).Error
		if findErr == nil {
			if err := mergeServiceIntoUnclaimedTx(tx, &svc, &existingUnclaimed); err != nil {
				return err
			}
			res = UnclaimResult{Action: UnclaimActionMerged, ServiceID: existingUnclaimed.ID}
			return nil
		} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}

		if err := tx.Model(&models.ModuleService{}).Where("id = ?", svc.ID).Updates(map[string]interface{}{
			"module_id":              nil,
			"contract_id":            nil,
			"impl_key":               "",
			"is_custom_service_name": false,
			"source":                 models.ServiceSourceMigrated,
			"claimed_at":             nil,
			"claimed_by":             0,
		}).Error; err != nil {
			return err
		}
		res = UnclaimResult{Action: UnclaimActionUnclaimed, ServiceID: svc.ID}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}
