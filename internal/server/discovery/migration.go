package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"code-contract/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrMigBackupNotFound 表示未找到可回滚的迁移备份批次
	ErrMigBackupNotFound = errors.New("未找到可回滚的 CSG 占位模块迁移备份")
)

// BlockedPlaceholderModule 因手工引用或契约定义被阻断清理的占位模块
type BlockedPlaceholderModule struct {
	ModuleID   uint64 `json:"module_id"`
	ModuleCode string `json:"module_code"`
	Reason     string `json:"reason"`
}

// MigratePlaceholdersInput CSG 占位假模块迁移/预演/回滚输入参数
type MigratePlaceholdersInput struct {
	DryRun     bool
	Action     string // EXECUTE | ROLLBACK | DRY_RUN
	BatchID    string
	OperatorID uint
}

// CSGPlaceholderMigrationResp CSG 占位假模块迁移/预演/回滚结果
type CSGPlaceholderMigrationResp struct {
	Mode             string                     `json:"mode"` // DRY_RUN | EXECUTE | ROLLBACK
	BackupBatchID    string                     `json:"backup_batch_id,omitempty"`
	ClearedModules   int                        `json:"cleared_modules"`
	MigratedServices int                        `json:"migrated_services"`
	RewrittenRecords int64                      `json:"rewritten_records"`
	AttributionJobID *uint64                    `json:"attribution_job_id,omitempty"`
	BlockedItems     []BlockedPlaceholderModule `json:"blocked_items"`
}

// MigrateCSGPlaceholderModules 安全清理存量 CSG_DISCOVERED / MOD_* 占位假模块并支持按备份批次全量回滚
func MigrateCSGPlaceholderModules(ctx context.Context, db *gorm.DB, in MigratePlaceholdersInput) (*CSGPlaceholderMigrationResp, error) {
	if db == nil {
		return nil, errors.New("discovery: nil db")
	}

	mode := "EXECUTE"
	if in.DryRun || strings.EqualFold(strings.TrimSpace(in.Action), "DRY_RUN") {
		mode = "DRY_RUN"
	} else if strings.EqualFold(strings.TrimSpace(in.Action), "ROLLBACK") {
		mode = "ROLLBACK"
	}

	if mode == "ROLLBACK" {
		return rollbackPlaceholderMigration(ctx, db, strings.TrimSpace(in.BatchID))
	}

	// 1. 扫描所有 CSG_DISCOVERED 功能组下属模块并判定阻断清单
	var groups []models.FunctionalGroup
	if err := db.WithContext(ctx).Where("code = ?", "CSG_DISCOVERED").Order("id ASC").Find(&groups).Error; err != nil {
		return nil, err
	}
	groupIDs := make([]uint64, 0, len(groups))
	for _, g := range groups {
		groupIDs = append(groupIDs, g.ID)
	}

	var candidateModules []models.Module
	if len(groupIDs) > 0 {
		if err := db.WithContext(ctx).Where("group_id IN ?", groupIDs).Order("id ASC").Find(&candidateModules).Error; err != nil {
			return nil, err
		}
	}

	safeModules := make([]models.Module, 0, len(candidateModules))
	safeSvcsByMod := make(map[uint64][]models.ModuleService)
	blocked := make([]BlockedPlaceholderModule, 0)
	totalSafeServices := 0

	for _, mod := range candidateModules {
		svcs := make([]models.ModuleService, 0)
		if err := db.WithContext(ctx).Where("module_id = ?", mod.ID).Order("id ASC").Find(&svcs).Error; err != nil {
			return nil, err
		}
		reason, err := checkPlaceholderModuleBlocked(ctx, db, mod, svcs)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			blocked = append(blocked, BlockedPlaceholderModule{
				ModuleID:   mod.ID,
				ModuleCode: mod.Code,
				Reason:     reason,
			})
			continue
		}
		safeModules = append(safeModules, mod)
		safeSvcsByMod[mod.ID] = svcs
		totalSafeServices += len(svcs)
	}

	if mode == "DRY_RUN" {
		return &CSGPlaceholderMigrationResp{
			Mode:             "DRY_RUN",
			ClearedModules:   len(safeModules),
			MigratedServices: totalSafeServices,
			BlockedItems:     blocked,
		}, nil
	}

	resp := &CSGPlaceholderMigrationResp{
		Mode:             "EXECUTE",
		ClearedModules:   len(safeModules),
		MigratedServices: totalSafeServices,
		BlockedItems:     blocked,
	}
	if len(safeModules) == 0 {
		return resp, nil
	}

	batchID := fmt.Sprintf("mig-csg-%d", time.Now().UnixNano())
	resp.BackupBatchID = batchID

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		safeModIDs := make([]uint64, 0, len(safeModules))
		affectedModArch := make(map[uint64]uint64)

		for _, mod := range safeModules {
			safeModIDs = append(safeModIDs, mod.ID)
			if mod.ArchProcessID != nil && *mod.ArchProcessID > 0 {
				affectedModArch[mod.ID] = *mod.ArchProcessID
			}
			modBytes, _ := json.Marshal(mod)
			if err := tx.Create(&models.MigBackup{
				MigrationKey:  batchID,
				Entity:        "module",
				EntityID:      mod.ID,
				Payload:       datatypes.JSON(modBytes),
				MigratedState: models.MigBackupStateApplied,
			}).Error; err != nil {
				return err
			}

			// 若该模块关联了标准进程，查找已挂载实例用于补齐历史 STALE ServicePlacement
			var mountedProcs []models.Process
			if mod.ArchProcessID != nil && *mod.ArchProcessID > 0 {
				_ = tx.Where("arch_process_id = ?", *mod.ArchProcessID).Find(&mountedProcs).Error
			}

			for _, svc := range safeSvcsByMod[mod.ID] {
				svcBytes, _ := json.Marshal(svc)
				if err := tx.Create(&models.MigBackup{
					MigrationKey:  batchID,
					Entity:        "module_service",
					EntityID:      svc.ID,
					Payload:       datatypes.JSON(svcBytes),
					MigratedState: models.MigBackupStateApplied,
				}).Error; err != nil {
					return err
				}

				for _, mp := range mountedProcs {
					var existingPlcCount int64
					_ = tx.Model(&models.ServicePlacement{}).
						Where("module_service_id = ? AND process_id = ?", svc.ID, mp.ID).
						Count(&existingPlcCount).Error
					if existingPlcCount == 0 {
						plc := models.ServicePlacement{
							ModuleServiceID:   svc.ID,
							ProcessID:         mp.ID,
							ObservedTransport: models.ObservedTransportTCP,
							InstanceCount:     1,
							Status:            models.PlacementStatusStale,
							MissCount:         0,
							FirstSeenAt:       now,
							LastSeenAt:        now,
						}
						if err := tx.Create(&plc).Error; err != nil {
							return err
						}
					}
				}

				// 按不变量 I1 / I7 将服务安全回退为未认领 MIGRATED 态
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
			}

			if err := tx.Delete(&models.Module{}, mod.ID).Error; err != nil {
				return err
			}
		}

		// 清理变空的 CSG_DISCOVERED 功能组并备份
		for _, grp := range groups {
			var remainMod int64
			if err := tx.Model(&models.Module{}).Where("group_id = ?", grp.ID).Count(&remainMod).Error; err != nil {
				return err
			}
			if remainMod == 0 {
				grpBytes, _ := json.Marshal(grp)
				if err := tx.Create(&models.MigBackup{
					MigrationKey:  batchID,
					Entity:        "functional_group",
					EntityID:      grp.ID,
					Payload:       datatypes.JSON(grpBytes),
					MigratedState: models.MigBackupStateApplied,
				}).Error; err != nil {
					return err
				}
				if err := tx.Delete(&models.FunctionalGroup{}, grp.ID).Error; err != nil {
					return err
				}
			}
		}

		// 回填已归因到占位假模块的历史流量物理进程列，并记 MIGRATION AttributionJob (DONE)
		var totalRewritten int64
		for modID, archID := range affectedModArch {
			r1 := tx.Table("contract_traffic_records").
				Where("src_module_id = ? AND src_arch_process_id IS NULL", modID).
				Update("src_arch_process_id", archID)
			if r1.Error != nil {
				return r1.Error
			}
			totalRewritten += r1.RowsAffected

			r2 := tx.Table("contract_traffic_records").
				Where("dst_module_id = ? AND dst_arch_process_id IS NULL", modID).
				Update("dst_arch_process_id", archID)
			if r2.Error != nil {
				return r2.Error
			}
			totalRewritten += r2.RowsAffected
		}

		// 对无 ArchProcessID 的占位模块流量记录通过 source Process.ArchProcessID 兜底回填
		if len(safeModIDs) > 0 {
			r3 := tx.Exec(`
UPDATE contract_traffic_records AS tr
SET src_arch_process_id = p.arch_process_id
FROM contract_processes AS p
WHERE tr.process_id = p.id
  AND tr.direction = 'INGRESS'
  AND tr.dst_arch_process_id IS NULL
  AND tr.dst_module_id IN ?`, safeModIDs)
			_ = r3
		}

		scopeBytes, _ := json.Marshal(map[string]interface{}{
			"batch_id":         batchID,
			"cleared_modules":  safeModIDs,
			"migrated_service": totalSafeServices,
		})
		job := models.AttributionJob{
			JobType:   models.AttrJobTypeMigration,
			ScopeJSON: datatypes.JSON(scopeBytes),
			Status:    models.AttrJobStatusDone,
			Processed: totalRewritten,
			Total:     totalRewritten,
			CreatedBy: in.OperatorID,
		}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		jobID := job.ID
		resp.AttributionJobID = &jobID
		resp.RewrittenRecords = totalRewritten
		return nil
	})
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func checkPlaceholderModuleBlocked(ctx context.Context, db *gorm.DB, mod models.Module, svcs []models.ModuleService) (string, error) {
	if mod.ModuleKind == models.ModuleKindContractLib {
		return "CONTRACT_LIB 接口库模块禁止自动清理", nil
	}

	var depCnt int64
	if err := db.WithContext(ctx).Model(&models.ModuleDependency{}).
		Where("consumer_module_id = ? OR provider_module_id = ?", mod.ID, mod.ID).
		Count(&depCnt).Error; err != nil {
		return "", err
	}
	if depCnt > 0 {
		return "存在模块间手工依赖引用 (ModuleDependency)", nil
	}

	if len(svcs) > 0 {
		svcIDs := make([]uint64, 0, len(svcs))
		for _, s := range svcs {
			if s.ContractID != nil {
				return "下属服务已绑定多实现契约 (ServiceContract)", nil
			}
			svcIDs = append(svcIDs, s.ID)
		}
		var svcDepCnt int64
		if err := db.WithContext(ctx).Model(&models.ModuleDependency{}).
			Where("service_id IN ?", svcIDs).
			Count(&svcDepCnt).Error; err != nil {
			return "", err
		}
		if svcDepCnt > 0 {
			return "下属服务正被 ModuleDependency 引用", nil
		}
	}

	var scDefCnt int64
	if err := db.WithContext(ctx).Model(&models.ServiceContract{}).
		Where("definer_module_id = ?", mod.ID).
		Count(&scDefCnt).Error; err != nil {
		return "", err
	}
	if scDefCnt > 0 {
		return "该模块定义了多实现 ServiceContract", nil
	}

	var ddecCnt int64
	if err := db.WithContext(ctx).Model(&models.DependencyDecision{}).
		Where("consumer_module_id = ?", mod.ID).
		Count(&ddecCnt).Error; err != nil {
		return "", err
	}
	if ddecCnt > 0 {
		return "存在依赖治理裁决记录 (DependencyDecision)", nil
	}

	return "", nil
}

func rollbackPlaceholderMigration(ctx context.Context, db *gorm.DB, batchID string) (*CSGPlaceholderMigrationResp, error) {
	if batchID == "" {
		var latest models.MigBackup
		if err := db.WithContext(ctx).
			Where("migrated_state = ?", models.MigBackupStateApplied).
			Order("id DESC").
			First(&latest).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrMigBackupNotFound
			}
			return nil, err
		}
		batchID = latest.MigrationKey
	}

	var backups []models.MigBackup
	if err := db.WithContext(ctx).
		Where("migration_key = ? AND migrated_state = ?", batchID, models.MigBackupStateApplied).
		Order("id ASC").
		Find(&backups).Error; err != nil {
		return nil, err
	}
	if len(backups) == 0 {
		return nil, ErrMigBackupNotFound
	}

	resp := &CSGPlaceholderMigrationResp{
		Mode:          "ROLLBACK",
		BackupBatchID: batchID,
		BlockedItems:  []BlockedPlaceholderModule{},
	}

	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先恢复 FunctionalGroup，再恢复 Module，最后恢复 ModuleService（若未被重新认领）
		for _, bk := range backups {
			if bk.Entity != "functional_group" {
				continue
			}
			var fg models.FunctionalGroup
			if err := json.Unmarshal(bk.Payload, &fg); err != nil {
				return err
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoNothing: true}).Create(&fg).Error; err != nil {
				return err
			}
		}
		_ = tx.Exec("SELECT setval(pg_get_serial_sequence('contract_functional_groups', 'id'), COALESCE((SELECT MAX(id) FROM contract_functional_groups), 1))").Error

		for _, bk := range backups {
			if bk.Entity != "module" {
				continue
			}
			var mod models.Module
			if err := json.Unmarshal(bk.Payload, &mod); err != nil {
				return err
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoNothing: true}).Create(&mod).Error; err != nil {
				return err
			}
			resp.ClearedModules++
		}
		_ = tx.Exec("SELECT setval(pg_get_serial_sequence('contract_modules', 'id'), COALESCE((SELECT MAX(id) FROM contract_modules), 1))").Error

		for _, bk := range backups {
			if bk.Entity != "module_service" {
				continue
			}
			var origSvc models.ModuleService
			if err := json.Unmarshal(bk.Payload, &origSvc); err != nil {
				return err
			}
			var currentSvc models.ModuleService
			if err := tx.First(&currentSvc, origSvc.ID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return err
			}
			// 仅恢复当前仍为未认领状态的服务，防止覆盖后续人工认领
			if currentSvc.ModuleID != nil {
				continue
			}
			if err := tx.Model(&models.ModuleService{}).Where("id = ?", origSvc.ID).Updates(map[string]interface{}{
				"module_id":              origSvc.ModuleID,
				"contract_id":            origSvc.ContractID,
				"impl_key":               origSvc.ImplKey,
				"is_custom_service_name": origSvc.IsCustomServiceName,
				"source":                 origSvc.Source,
				"claimed_at":             origSvc.ClaimedAt,
				"claimed_by":             origSvc.ClaimedBy,
			}).Error; err != nil {
				return err
			}
			resp.MigratedServices++
		}

		now := time.Now()
		return tx.Model(&models.MigBackup{}).
			Where("migration_key = ?", batchID).
			Updates(map[string]interface{}{
				"migrated_state": models.MigBackupStateRolledBack,
				"rolled_back_at": &now,
			}).Error
	}); err != nil {
		return nil, err
	}

	return resp, nil
}
