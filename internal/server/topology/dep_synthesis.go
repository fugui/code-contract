package topology

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"code-contract/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RecommendSourceSingleModule  = "SINGLE_MODULE"
	RecommendSourceStage2Traffic = "STAGE2_TRAFFIC"

	DecisionApprove   = "APPROVE"
	DecisionIgnore    = "IGNORE"
	DecisionViolation = "VIOLATION"
)

var (
	ErrDecideInvalidInput               = errors.New("依赖裁决参数不合法（decision 须为 APPROVE / IGNORE / VIOLATION）")
	ErrDecideConsumerModuleRequired     = errors.New("消费方进程承载多个业务模块，批准时必须指定 consumer_module_id")
	ErrDecideConsumerModuleNotInProcess = errors.New("指定的 consumer_module_id 不属于该消费方标准架构进程")
)

// CandidateConsumerModuleDTO 消费方进程承载的候选业务模块
type CandidateConsumerModuleDTO struct {
	ModuleID   uint64 `json:"module_id"`
	ModuleCode string `json:"module_code"`
	ModuleName string `json:"module_name"`
}

// DepSuggestionDTO 逻辑依赖反向合成建议
type DepSuggestionDTO struct {
	ConsumerArchProcessID       uint64                       `json:"consumer_arch_process_id"`
	ConsumerArchProcessCode     string                       `json:"consumer_arch_process_code"`
	ConsumerArchProcessName     string                       `json:"consumer_arch_process_name"`
	ServiceID                   uint64                       `json:"service_id"`
	ServiceName                 string                       `json:"service_name"`
	ProviderModuleID            uint64                       `json:"provider_module_id"`
	ProviderModuleName          string                       `json:"provider_module_name"`
	CandidateConsumerModules    []CandidateConsumerModuleDTO `json:"candidate_consumer_modules"`
	RecommendedConsumerModuleID *uint64                      `json:"recommended_consumer_module_id,omitempty"`
	RecommendationSource        string                       `json:"recommendation_source"`
}

// DecideInput 依赖反向审批裁决入参
type DecideInput struct {
	ConsumerArchProcessID uint64 `json:"consumer_arch_process_id"`
	ServiceID             uint64 `json:"service_id"`
	Decision              string `json:"decision"`
	ConsumerModuleID      uint64 `json:"consumer_module_id,omitempty"`
	Rationale             string `json:"rationale,omitempty"`
}

// DecideResult 依赖裁决结果
type DecideResult struct {
	Decision           string  `json:"decision"`
	ModuleDependencyID *uint64 `json:"module_dependency_id,omitempty"`
}

// SynthesizeDependencySuggestions 基于权威 SHARED 环境非 RETIRED ProcessDependency 反向合成逻辑依赖建议
func SynthesizeDependencySuggestions(ctx context.Context, db *gorm.DB) ([]DepSuggestionDTO, error) {
	var procDeps []models.ProcessDependency
	if err := db.WithContext(ctx).
		Preload("Agent").
		Preload("ConsumerArchProcess").
		Preload("Service.Module").
		Where("status <> ? AND service_id IS NOT NULL", models.PlacementStatusRetired).
		Order("consumer_arch_process_id ASC, service_id ASC").
		Find(&procDeps).Error; err != nil {
		return nil, err
	}

	// 加载所有绑定进程的 BUSINESS 模块
	var mods []models.Module
	if err := db.WithContext(ctx).
		Where("arch_process_id IS NOT NULL AND module_kind = ?", models.ModuleKindBusiness).
		Order("id ASC").
		Find(&mods).Error; err != nil {
		return nil, err
	}
	modsByAP := make(map[uint64][]models.Module)
	for _, m := range mods {
		if m.ArchProcessID != nil {
			modsByAP[*m.ArchProcessID] = append(modsByAP[*m.ArchProcessID], m)
		}
	}

	// 加载已有逻辑依赖与已有裁决用于去重过滤
	var existingDeps []models.ModuleDependency
	if err := db.WithContext(ctx).Find(&existingDeps).Error; err != nil {
		return nil, err
	}
	depByModSvc := make(map[[2]uint64]bool, len(existingDeps))
	depByModContract := make(map[[2]uint64]bool)
	for _, d := range existingDeps {
		if d.ServiceID != nil {
			depByModSvc[[2]uint64{d.ConsumerModuleID, *d.ServiceID}] = true
		}
		if d.ContractID != nil {
			depByModContract[[2]uint64{d.ConsumerModuleID, *d.ContractID}] = true
		}
	}

	var decisions []models.DependencyDecision
	if err := db.WithContext(ctx).Find(&decisions).Error; err != nil {
		return nil, err
	}
	decidedByModSvc := make(map[[2]uint64]bool, len(decisions))
	for _, dd := range decisions {
		decidedByModSvc[[2]uint64{dd.ConsumerModuleID, dd.ServiceID}] = true
	}

	type edgeKey struct {
		consumerAPID uint64
		serviceID    uint64
	}
	seenEdges := make(map[edgeKey]bool)
	out := make([]DepSuggestionDTO, 0)

	for _, pd := range procDeps {
		if pd.Agent == nil || pd.Agent.Scope != models.ScopeShared {
			continue
		}
		if pd.ConsumerArchProcess == nil || pd.ConsumerArchProcess.Lifecycle != models.ArchProcLifecycleConfirmed {
			continue
		}
		if pd.Service == nil || !pd.Service.IsClaimed() {
			continue
		}

		k := edgeKey{consumerAPID: pd.ConsumerArchProcessID, serviceID: pd.Service.ID}
		if seenEdges[k] {
			continue
		}
		seenEdges[k] = true

		apMods := modsByAP[pd.ConsumerArchProcessID]
		if len(apMods) == 0 {
			continue
		}

		provModID := pd.Service.ModuleIDVal()
		candidates := make([]CandidateConsumerModuleDTO, 0, len(apMods))
		candIDs := make([]uint64, 0, len(apMods))
		for _, m := range apMods {
			if m.ID == provModID {
				continue
			}
			keyPair := [2]uint64{m.ID, pd.Service.ID}
			if depByModSvc[keyPair] || decidedByModSvc[keyPair] {
				continue
			}
			if pd.Service.ContractID != nil && depByModContract[[2]uint64{m.ID, *pd.Service.ContractID}] {
				continue
			}
			candidates = append(candidates, CandidateConsumerModuleDTO{
				ModuleID:   m.ID,
				ModuleCode: m.Code,
				ModuleName: m.Name,
			})
			candIDs = append(candIDs, m.ID)
		}
		if len(candidates) == 0 {
			continue
		}

		provModName := ""
		if pd.Service.Module != nil {
			provModName = pd.Service.Module.Name
		}

		sug := DepSuggestionDTO{
			ConsumerArchProcessID:    pd.ConsumerArchProcessID,
			ConsumerArchProcessCode:  pd.ConsumerArchProcess.Code,
			ConsumerArchProcessName:  pd.ConsumerArchProcess.Name,
			ServiceID:                pd.Service.ID,
			ServiceName:              pd.Service.ServiceName,
			ProviderModuleID:         provModID,
			ProviderModuleName:       provModName,
			CandidateConsumerModules: candidates,
		}

		if len(apMods) == 1 {
			idCopy := candidates[0].ModuleID
			sug.RecommendedConsumerModuleID = &idCopy
			sug.RecommendationSource = RecommendSourceSingleModule
		} else {
			if recID := findStage2TrafficRecommendedModule(ctx, db, pd.Service, candIDs); recID != nil {
				sug.RecommendedConsumerModuleID = recID
				sug.RecommendationSource = RecommendSourceStage2Traffic
			}
		}

		out = append(out, sug)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].ConsumerArchProcessID != out[j].ConsumerArchProcessID {
			return out[i].ConsumerArchProcessID < out[j].ConsumerArchProcessID
		}
		return out[i].ServiceID < out[j].ServiceID
	})
	return out, nil
}

// findStage2TrafficRecommendedModule 从权威 SHARED 会话最近 Stage 2 EGRESS 归因流量中统计候选源模块频次最高者
func findStage2TrafficRecommendedModule(ctx context.Context, db *gorm.DB, svc *models.ModuleService, candIDs []uint64) *uint64 {
	if svc == nil || len(candIDs) == 0 {
		return nil
	}
	nameSet := map[string]struct{}{strings.TrimSpace(svc.ServiceName): {}}
	if norm := StripCSGPIDSuffix(svc.ServiceName); norm != "" {
		nameSet[norm] = struct{}{}
	}
	if strings.TrimSpace(svc.ProtoServiceName) != "" {
		nameSet[strings.TrimSpace(svc.ProtoServiceName)] = struct{}{}
	}
	svcNames := make([]string, 0, len(nameSet))
	for n := range nameSet {
		if n != "" {
			svcNames = append(svcNames, n)
		}
	}
	if len(svcNames) == 0 {
		return nil
	}

	type hitRow struct {
		SrcModuleID uint64
		HitCount    int64
	}
	var hits []hitRow
	err := db.WithContext(ctx).
		Table("contract_traffic_records tr").
		Select("tr.src_module_id AS src_module_id, COUNT(*) AS hit_count").
		Joins("JOIN contract_recording_sessions rs ON rs.id = tr.session_id").
		Where("rs.scope = ? AND tr.direction = ? AND tr.service_name IN ? AND tr.src_module_id IN ?",
			models.ScopeShared, models.TrafficDirEgress, svcNames, candIDs).
		Group("tr.src_module_id").
		Order("hit_count DESC, tr.src_module_id ASC").
		Limit(1).
		Scan(&hits).Error
	if err != nil || len(hits) == 0 || hits[0].SrcModuleID == 0 {
		return nil
	}
	res := hits[0].SrcModuleID
	return &res
}

// DecideDependencySuggestion 管理员对反向逻辑依赖建议做出 APPROVE / IGNORE / VIOLATION 裁决
func DecideDependencySuggestion(ctx context.Context, db *gorm.DB, op Operator, in DecideInput) (*DecideResult, error) {
	if !op.IsAdmin {
		return nil, ErrClaimForbidden
	}
	rawDec := strings.ToUpper(strings.TrimSpace(in.Decision))
	if rawDec == models.DepDecisionIgnored {
		rawDec = DecisionIgnore
	}
	if rawDec != DecisionApprove && rawDec != DecisionIgnore && rawDec != DecisionViolation {
		return nil, ErrDecideInvalidInput
	}

	var ap models.ArchProcess
	if err := db.WithContext(ctx).First(&ap, in.ConsumerArchProcessID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrArchProcessNotFound
		}
		return nil, err
	}

	var svc models.ModuleService
	if err := db.WithContext(ctx).First(&svc, in.ServiceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrModuleServiceNotFound
		}
		return nil, err
	}
	if !svc.IsClaimed() {
		return nil, ErrModuleServiceNotFound
	}

	var apMods []models.Module
	if err := db.WithContext(ctx).
		Where("arch_process_id = ? AND module_kind = ?", in.ConsumerArchProcessID, models.ModuleKindBusiness).
		Order("id ASC").
		Find(&apMods).Error; err != nil {
		return nil, err
	}
	if len(apMods) == 0 {
		return nil, ErrModuleNotFound
	}
	apModSet := make(map[uint64]bool, len(apMods))
	for _, m := range apMods {
		apModSet[m.ID] = true
	}

	if rawDec == DecisionApprove {
		consumerModID := in.ConsumerModuleID
		if consumerModID == 0 {
			if len(apMods) == 1 {
				consumerModID = apMods[0].ID
			} else {
				return nil, ErrDecideConsumerModuleRequired
			}
		} else if !apModSet[consumerModID] {
			return nil, ErrDecideConsumerModuleNotInProcess
		}

		if consumerModID == svc.ModuleIDVal() {
			return nil, ErrSelfDependency
		}

		// 若已存在同 (consumer_module_id, service_id) 依赖，幂等返回
		var existing models.ModuleDependency
		if findErr := db.WithContext(ctx).
			Where("consumer_module_id = ? AND service_id = ?", consumerModID, svc.ID).
			First(&existing).Error; findErr == nil {
			depID := existing.ID
			return &DecideResult{Decision: "APPROVED", ModuleDependencyID: &depID}, nil
		}

		var createdDepID uint64
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if svc.ContractID != nil {
				var dispatchCount int64
				if err := tx.Model(&models.ModuleDependency{}).
					Where("consumer_module_id = ? AND contract_id = ?", consumerModID, *svc.ContractID).
					Count(&dispatchCount).Error; err != nil {
					return err
				}
				if dispatchCount > 0 {
					return ErrDuplicateContractDependency
				}
			}

			provModID := svc.ModuleIDVal()
			dep := &models.ModuleDependency{
				ConsumerModuleID: consumerModID,
				ServiceID:        &svc.ID,
				ProviderModuleID: &provModID,
				Description:      strings.TrimSpace(in.Rationale),
				CreatedBy:        op.UserID,
			}
			if err := tx.Create(dep).Error; err != nil {
				return err
			}
			if err := ValidateDependencyGuard(tx, dep); err != nil {
				return err
			}
			createdDepID = dep.ID
			return nil
		})
		if err != nil {
			return nil, err
		}
		return &DecideResult{Decision: "APPROVED", ModuleDependencyID: &createdDepID}, nil
	}

	// IGNORE / VIOLATION 写入表 #34 contract_dependency_decisions
	dbDecision := models.DepDecisionIgnored
	if rawDec == DecisionViolation {
		dbDecision = models.DepDecisionViolation
	}

	var targetModuleIDs []uint64
	if in.ConsumerModuleID > 0 {
		if !apModSet[in.ConsumerModuleID] {
			return nil, ErrDecideConsumerModuleNotInProcess
		}
		targetModuleIDs = []uint64{in.ConsumerModuleID}
	} else {
		for _, m := range apMods {
			targetModuleIDs = append(targetModuleIDs, m.ID)
		}
	}

	now := time.Now().UTC()
	cleanReason := strings.TrimSpace(in.Rationale)
	if len(cleanReason) > 512 {
		cleanReason = cleanReason[:512]
	}

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, mID := range targetModuleIDs {
			row := models.DependencyDecision{
				ConsumerModuleID: mID,
				ServiceID:        svc.ID,
				Decision:         dbDecision,
				Reason:           cleanReason,
				DecidedBy:        op.UserID,
				DecidedAt:        now,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "consumer_module_id"}, {Name: "service_id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"decision", "reason", "decided_by", "decided_at",
				}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &DecideResult{Decision: dbDecision}, nil
}
