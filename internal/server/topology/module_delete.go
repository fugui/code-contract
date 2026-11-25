package topology

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"code-contract/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ReferencingModuleDTO 依赖引用阻断时的消费方模块摘要
type ReferencingModuleDTO struct {
	ModuleID    uint64 `json:"module_id"`
	ModuleCode  string `json:"module_code"`
	ModuleName  string `json:"module_name"`
	ServiceID   uint64 `json:"service_id"`
	ServiceName string `json:"service_name"`
}

// DependencyReferencedError 不变量 I6 依赖守卫阻断错误（对应 HTTP 409 DEPENDENCY_REFERENCED）
type DependencyReferencedError struct {
	Code               string                 `json:"code"`
	Message            string                 `json:"error"`
	ReferencingModules []ReferencingModuleDTO `json:"referencing_modules"`
}

func (e *DependencyReferencedError) Error() string {
	return e.Message
}

// DeleteModuleResult 记录 DeleteModuleTx 原子迁移与删除结果，供 P1 审计快照使用
type DeleteModuleResult struct {
	Module           models.Module `json:"module"`
	DeletedServices  int           `json:"deleted_services"`
	RevertedServices int           `json:"reverted_services"`
	MergedServices   int           `json:"merged_services"`
}

// DeleteModuleTx 按 ADR-24 §5.1 不变量 I6/I7 在单事务内删除模块并显式处理旗下服务：
// 1. 锁定 Module 行并校验无契约定义者/消费方依赖；
// 2. I6 守卫：若旗下任一服务被 ModuleDependency.service_id 引用，返回 *DependencyReferencedError (409)；
// 3. I7 迁移：有放置的服务原地回退为未认领 (source=MIGRATED) 或合并到既有同名未认领身份；无放置的手工服务物理删除；
// 4. 删除 Module 行。
func DeleteModuleTx(tx *gorm.DB, moduleID uint64) (*DeleteModuleResult, error) {
	var mod models.Module
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&mod, moduleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrModuleNotFound
		}
		return nil, err
	}

	if mod.ModuleKind == models.ModuleKindContractLib {
		var contractCount int64
		if err := tx.Model(&models.ServiceContract{}).Where("definer_module_id = ?", moduleID).Count(&contractCount).Error; err != nil {
			return nil, err
		}
		if contractCount > 0 {
			return nil, ErrContractLibHasContractsCannotDelete
		}
	}

	var consumerDepCount int64
	if err := tx.Model(&models.ModuleDependency{}).Where("consumer_module_id = ?", moduleID).Count(&consumerDepCount).Error; err != nil {
		return nil, err
	}
	if consumerDepCount > 0 {
		return nil, ErrModuleNotEmpty
	}

	var svcs []models.ModuleService
	if err := tx.Where("module_id = ?", moduleID).Order("id ASC").Find(&svcs).Error; err != nil {
		return nil, err
	}

	if len(svcs) > 0 {
		svcIDs := make([]uint64, 0, len(svcs))
		svcNameByID := make(map[uint64]string, len(svcs))
		for _, s := range svcs {
			svcIDs = append(svcIDs, s.ID)
			svcNameByID[s.ID] = s.ServiceName
		}

		// I6 依赖守卫：检查服务是否被下游 ModuleDependency.service_id 引用
		var refDeps []models.ModuleDependency
		if err := tx.Preload("ConsumerModule").Where("service_id IN ?", svcIDs).Order("id ASC").Find(&refDeps).Error; err != nil {
			return nil, err
		}
		if len(refDeps) > 0 {
			refs := make([]ReferencingModuleDTO, 0, len(refDeps))
			for _, d := range refDeps {
				ref := ReferencingModuleDTO{
					ModuleID: d.ConsumerModuleID,
				}
				if d.ServiceID != nil {
					ref.ServiceID = *d.ServiceID
					ref.ServiceName = svcNameByID[*d.ServiceID]
				}
				if d.ConsumerModule != nil {
					ref.ModuleCode = d.ConsumerModule.Code
					ref.ModuleName = d.ConsumerModule.Name
				}
				refs = append(refs, ref)
			}
			return nil, &DependencyReferencedError{
				Code:               "DEPENDENCY_REFERENCED",
				Message:            fmt.Sprintf("模块 [%s] 下存在被 %d 项下游逻辑依赖引用的服务，请先解除相关依赖", mod.Name, len(refs)),
				ReferencingModules: refs,
			}
		}
	}

	res := &DeleteModuleResult{Module: mod}

	// I7 逐个处理服务：有放置回退或合并，无放置纯手工直接删除
	for _, svc := range svcs {
		var plcCount int64
		if err := tx.Model(&models.ServicePlacement{}).Where("module_service_id = ?", svc.ID).Count(&plcCount).Error; err != nil {
			return nil, err
		}

		if plcCount == 0 && svc.Source == models.ServiceSourceManual {
			// 纯手工无放置服务直接物理删除（先清理可能残留的 DependencyDecision，实际外键也是 CASCADE）
			if err := tx.Delete(&models.ModuleService{}, svc.ID).Error; err != nil {
				return nil, err
			}
			res.DeletedServices++
			continue
		}

		// 检查是否已存在同名未认领身份 (MULTI_IMPL 场景)
		var existingUnclaimed models.ModuleService
		findErr := tx.Where("service_name = ? AND module_id IS NULL", svc.ServiceName).First(&existingUnclaimed).Error
		if findErr == nil {
			if err := mergeServiceIntoUnclaimedTx(tx, &svc, &existingUnclaimed); err != nil {
				return nil, err
			}
			res.MergedServices++
			continue
		} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return nil, findErr
		}

		// 若无同名未认领身份，且有放置（或来源为 CSG_DISCOVERED/MIGRATED），原地清空认领归属并置 MIGRATED
		if err := tx.Model(&models.ModuleService{}).Where("id = ?", svc.ID).Updates(map[string]interface{}{
			"module_id":              nil,
			"contract_id":            nil,
			"impl_key":               "",
			"is_custom_service_name": false,
			"source":                 models.ServiceSourceMigrated,
			"claimed_at":             nil,
			"claimed_by":             0,
		}).Error; err != nil {
			return nil, err
		}
		res.RevertedServices++
	}

	// 清理以该模块作为冗余 provider_module_id 的历史残留引用（如有）
	if err := tx.Model(&models.ModuleDependency{}).Where("provider_module_id = ?", moduleID).Update("provider_module_id", nil).Error; err != nil {
		return nil, err
	}

	if err := tx.Delete(&mod).Error; err != nil {
		return nil, err
	}
	return res, nil
}

func mergeServiceIntoUnclaimedTx(tx *gorm.DB, sourceSvc, targetUnclaimed *models.ModuleService) error {
	// 1. 合并 ServicePlacement（按 process_id 去重，保留 last_seen_at 最新一条）
	var srcPlacements []models.ServicePlacement
	if err := tx.Where("module_service_id = ?", sourceSvc.ID).Order("id ASC").Find(&srcPlacements).Error; err != nil {
		return err
	}

	for _, sp := range srcPlacements {
		var dstSP models.ServicePlacement
		err := tx.Where("module_service_id = ? AND process_id = ?", targetUnclaimed.ID, sp.ProcessID).First(&dstSP).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Model(&models.ServicePlacement{}).Where("id = ?", sp.ID).
				Update("module_service_id", targetUnclaimed.ID).Error; err != nil {
				return err
			}
			continue
		} else if err != nil {
			return err
		}

		// 目标已存在同一 process_id 放置：若源更新，则将源关键观测字段覆盖到目标，随后删除重复源放置
		if sp.LastSeenAt.After(dstSP.LastSeenAt) {
			if err := tx.Model(&models.ServicePlacement{}).Where("id = ?", dstSP.ID).Updates(map[string]interface{}{
				"raw_service_name":   sp.RawServiceName,
				"endpoints":          sp.Endpoints,
				"observed_transport": sp.ObservedTransport,
				"instance_count":     sp.InstanceCount,
				"status":             sp.Status,
				"miss_count":         sp.MissCount,
				"last_seen_at":       sp.LastSeenAt,
				"last_snapshot_id":   sp.LastSnapshotID,
			}).Error; err != nil {
				return err
			}
		}
		if err := tx.Delete(&models.ServicePlacement{}, sp.ID).Error; err != nil {
			return err
		}
	}

	// 2. 并集合并 diff_ignore_paths
	mergedPaths := mergeJSONStringLists(targetUnclaimed.DiffIgnorePaths, sourceSvc.DiffIgnorePaths)
	mergedJSON, _ := json.Marshal(mergedPaths)
	if err := tx.Model(&models.ModuleService{}).Where("id = ?", targetUnclaimed.ID).
		Update("diff_ignore_paths", datatypes.JSON(mergedJSON)).Error; err != nil {
		return err
	}

	// 3. 将 ProcessDependency.service_id 及 DependencyDecision 重指向既有未认领身份
	if err := tx.Model(&models.ProcessDependency{}).Where("service_id = ?", sourceSvc.ID).
		Update("service_id", targetUnclaimed.ID).Error; err != nil {
		return err
	}
	if err := tx.Where("service_id = ?", sourceSvc.ID).Delete(&models.DependencyDecision{}).Error; err != nil {
		return err
	}

	// 4. 删除回退源行
	return tx.Delete(&models.ModuleService{}, sourceSvc.ID).Error
}

func mergeJSONStringLists(rawA, rawB datatypes.JSON) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, raw := range []datatypes.JSON{rawA, rawB} {
		if len(raw) == 0 {
			continue
		}
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			continue
		}
		for _, item := range list {
			if item == "" {
				continue
			}
			if _, ok := seen[item]; !ok {
				seen[item] = struct{}{}
				out = append(out, item)
			}
		}
	}
	sort.Strings(out)
	return out
}
