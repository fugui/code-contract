package topology

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ============================================================================
// Part 1: 原 claimable_service.go 合并接口与 DTO
// ============================================================================

// ClaimedModuleDTO 认领模块信息 DTO
type ClaimedModuleDTO struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// ClaimableProtoServiceDTO 可认领 Proto 服务 DTO
type ClaimableProtoServiceDTO struct {
	ServiceName     string            `json:"service_name"`
	ProtoPackage    string            `json:"proto_package"`
	ProtoService    string            `json:"proto_service"`
	SchemaID        uint64            `json:"schema_id"`
	SchemaName      string            `json:"schema_name"`
	OperationCount  int               `json:"operation_count"`
	Operations      []string          `json:"operations"`
	IsClaimed       bool              `json:"is_claimed"`
	ClaimedByModule *ClaimedModuleDTO `json:"claimed_by_module"`
	IsMultiImpl     bool              `json:"is_multi_impl"`
	ContractID      *uint64           `json:"contract_id,omitempty"`
	ImplKeyLabel    string            `json:"impl_key_label,omitempty"`
}

// ListClaimableProtoServices 从已激活的 ProtoOperation 中聚合服务并关联已认领的 FM 信息
func ListClaimableProtoServices(db *gorm.DB) ([]ClaimableProtoServiceDTO, error) {
	var ops []models.ProtoOperation
	if err := db.Where("is_active = true").Order("service_name ASC, operation ASC").Find(&ops).Error; err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return []ClaimableProtoServiceDTO{}, nil
	}

	schemaIDs := make([]uint64, 0)
	schemaIDMap := make(map[uint64]bool)
	for _, op := range ops {
		if !schemaIDMap[op.SchemaID] {
			schemaIDMap[op.SchemaID] = true
			schemaIDs = append(schemaIDs, op.SchemaID)
		}
	}

	schemaNameMap := make(map[uint64]string)
	if len(schemaIDs) > 0 {
		var schemas []models.ProtoSchema
		if err := db.Where("id IN ?", schemaIDs).Find(&schemas).Error; err != nil {
			return nil, err
		}
		for _, s := range schemas {
			name := s.Name
			if name == "" {
				name = s.FileName
			}
			schemaNameMap[s.ID] = name
		}
	}

	var modSvcs []models.ModuleService
	if err := db.Preload("Module").Find(&modSvcs).Error; err != nil {
		return nil, err
	}

	claimedMap := make(map[string]*models.Module)
	for _, ms := range modSvcs {
		if ms.ContractID == nil || ms.IsCustomServiceName {
			claimedMap[ms.ServiceName] = ms.Module
		}
	}

	var contracts []models.ServiceContract
	_ = db.Find(&contracts).Error
	contractMap := make(map[string]*models.ServiceContract)
	for i := range contracts {
		contractMap[contracts[i].ServiceName] = &contracts[i]
	}

	type serviceAgg struct {
		dto   ClaimableProtoServiceDTO
		opSet map[string]bool
	}
	aggMap := make(map[string]*serviceAgg)
	var orderList []string

	for _, op := range ops {
		svcName := op.ServiceName
		agg, exists := aggMap[svcName]
		if !exists {
			schName := schemaNameMap[op.SchemaID]
			agg = &serviceAgg{
				dto: ClaimableProtoServiceDTO{
					ServiceName:  svcName,
					ProtoPackage: op.ProtoPackage,
					ProtoService: op.ProtoService,
					SchemaID:     op.SchemaID,
					SchemaName:   schName,
					Operations:   make([]string, 0),
				},
				opSet: make(map[string]bool),
			}
			aggMap[svcName] = agg
			orderList = append(orderList, svcName)
		}

		if !agg.opSet[op.Operation] {
			agg.opSet[op.Operation] = true
			agg.dto.Operations = append(agg.dto.Operations, op.Operation)
		}
	}

	sort.Strings(orderList)

	items := make([]ClaimableProtoServiceDTO, 0, len(orderList))
	for _, svcName := range orderList {
		agg := aggMap[svcName]
		agg.dto.OperationCount = len(agg.dto.Operations)
		if mod, ok := claimedMap[svcName]; ok && mod != nil {
			agg.dto.IsClaimed = true
			agg.dto.ClaimedByModule = &ClaimedModuleDTO{
				ID:   fmt.Sprintf("%d", mod.ID),
				Code: mod.Code,
				Name: mod.Name,
			}
		} else {
			agg.dto.IsClaimed = false
			agg.dto.ClaimedByModule = nil
		}

		if c, ok := contractMap[svcName]; ok && c != nil {
			agg.dto.IsMultiImpl = true
			agg.dto.ContractID = &c.ID
			agg.dto.ImplKeyLabel = c.ImplKeyLabel
		} else {
			agg.dto.IsMultiImpl = false
			agg.dto.ContractID = nil
			agg.dto.ImplKeyLabel = ""
		}

		items = append(items, agg.dto)
	}

	return items, nil
}

// ============================================================================
// Part 2: 原 csg_reconcile.go 合并接口与三态对账引擎
// ============================================================================

// CSGReconcileState represents the 3-way reconciliation status between live CSG and SSOT.
type CSGReconcileState string

const (
	CSGReconcileMatched       CSGReconcileState = "MATCHED"
	CSGReconcileUndeclared    CSGReconcileState = "UNDECLARED_IN_SSOT"
	CSGReconcileMissingInLive CSGReconcileState = "MISSING_IN_LIVE"
)

var (
	// ErrInvalidGroupTarget indicates the functional group does not belong to the selected subsystem.
	ErrInvalidGroupTarget = errors.New("功能组不属于指定子系统")

	// ErrCatalogForbidden indicates user lacks RBAC permissions to confirm or inspect a catalog service.
	ErrCatalogForbidden = errors.New("无权操作该服务契约的 Proto 映射")

	pidSuffixNormPattern = regexp.MustCompile(`^(.+-\d+)-(\d+)$`)
	nonCodeCharsPattern  = regexp.MustCompile(`[^A-Za-z0-9_-]+`)
)

// ReconciledCSGService merges a live CSG service instance (or SSOT-declared service missing in live)
// with SSOT relational IDs.
type ReconciledCSGService struct {
	ServiceName       string            `json:"service_name"`
	Addr              string            `json:"addr"`
	Transport         string            `json:"transport"`
	HostIP            string            `json:"host_ip"`
	Port              uint16            `json:"port"`
	ServerPID         uint32            `json:"server_pid"`
	ServerProc        string            `json:"server_proc,omitempty"`
	OsNodeID          uint32            `json:"os_node_id,omitempty"`
	Version           uint64            `json:"version,omitempty"`
	ResolvedBySub     bool              `json:"resolved_by_sub,omitempty"`
	State             CSGReconcileState `json:"state"`
	SSOTServiceID     *uint64           `json:"ssot_service_id,omitempty"`
	SSOTModuleID      *uint64           `json:"ssot_module_id,omitempty"`
	SSOTModuleCode    string            `json:"ssot_module_code,omitempty"`
	SSOTModuleName    string            `json:"ssot_module_name,omitempty"`
	SSOTArchProcessID *uint64           `json:"ssot_arch_process_id,omitempty"`
	SSOTProtoSvc      string            `json:"ssot_proto_svc,omitempty"`
	ExpectedPort      int               `json:"expected_port,omitempty"`
}

// ReconciledCSGSubscription merges a live CSG subscription (or SSOT dependency missing in live).
type ReconciledCSGSubscription struct {
	ClientPID   uint32            `json:"client_pid"`
	ClientProc  string            `json:"client_proc"`
	ClientAddr  string            `json:"client_addr"`
	ServiceName string            `json:"service_name"`
	State       CSGReconcileState `json:"state"`
	SSOTDepID   *uint64           `json:"ssot_dep_id,omitempty"`
}

// CSGReconcileSummary summarizes the 3-state diff counts across services and subscriptions.
type CSGReconcileSummary struct {
	LiveServiceCount   int `json:"live_service_count"`
	MatchedCount       int `json:"matched_count"`
	UndeclaredCount    int `json:"undeclared_count"`
	MissingInLiveCount int `json:"missing_in_live_count"`
	LiveSubCount       int `json:"live_sub_count"`
	SubMatchedCount    int `json:"sub_matched_count"`
	SubUndeclaredCount int `json:"sub_undeclared_count"`
	SubMissingCount    int `json:"sub_missing_count"`
}

// CSGTopologyReconcileResp is returned by GET /api/agents/:id/csg/topology.
type CSGTopologyReconcileResp struct {
	AgentID              uint64                       `json:"agent_id"`
	DiscoveryMode        string                       `json:"discovery_mode"`
	CSGEndpoints         []agentproto.CSGEndpointInfo `json:"csg_endpoints"`
	PortUsages           []agentproto.CSGPortUsage    `json:"port_usages,omitempty"`
	Services             []ReconciledCSGService       `json:"services"`
	Subscriptions        []ReconciledCSGSubscription  `json:"subscriptions"`
	Summary              CSGReconcileSummary          `json:"summary"`
	FilteredRemoteCount  int                          `json:"filtered_remote_count"`
	FilteredTransientCnt int                          `json:"filtered_transient_count"`
	CapturedAt           string                       `json:"captured_at"`
	Errors               []string                     `json:"errors,omitempty"`
}

// StripCSGPIDSuffix normalizes runtime CSG names such as Svc-0-12345 to Svc-0.
func StripCSGPIDSuffix(name string) string {
	trimmed := strings.TrimSpace(name)
	if m := pidSuffixNormPattern.FindStringSubmatch(trimmed); len(m) == 3 {
		return m[1]
	}
	return trimmed
}

// ReconcileLiveCSGWithSSOT compares live Agent CSG discovery output against SSOT definitions
// mounted on agentID and global ModuleService declarations.
func ReconcileLiveCSGWithSSOT(db *gorm.DB, agentID uint64, live *agentproto.CSGTopologyReport) (*CSGTopologyReconcileResp, error) {
	if live == nil {
		live = &agentproto.CSGTopologyReport{}
	}

	var procs []models.Process
	if err := db.Where("agent_id = ?", agentID).Find(&procs).Error; err != nil {
		return nil, err
	}
	mountedArchProcIDs := make(map[uint64]struct{}, len(procs))
	mountedPortByArchProcID := make(map[uint64]int, len(procs))
	for _, p := range procs {
		mountedArchProcIDs[p.ArchProcessID] = struct{}{}
		if p.EffectiveListenPort > 0 {
			mountedPortByArchProcID[p.ArchProcessID] = p.EffectiveListenPort
		}
	}

	var allSvcs []models.ModuleService
	if err := db.Preload("Module").Preload("Module.ArchProcess").Order("id ASC").Find(&allSvcs).Error; err != nil {
		return nil, err
	}

	byExactName := make(map[string]models.ModuleService, len(allSvcs))
	byNormName := make(map[string]models.ModuleService, len(allSvcs))
	mountedServices := make([]models.ModuleService, 0)
	for _, s := range allSvcs {
		byExactName[s.ServiceName] = s
		byNormName[StripCSGPIDSuffix(s.ServiceName)] = s
		if protoSvc := strings.TrimSpace(s.ProtoServiceName); protoSvc != "" {
			if _, exists := byExactName[protoSvc]; !exists {
				byExactName[protoSvc] = s
			}
			normProto := StripCSGPIDSuffix(protoSvc)
			if _, exists := byNormName[normProto]; !exists {
				byNormName[normProto] = s
			}
		}
		if s.Module != nil && s.Module.ArchProcessID != nil {
			if _, ok := mountedArchProcIDs[*s.Module.ArchProcessID]; ok {
				mountedServices = append(mountedServices, s)
			}
		}
	}

	endpoints := live.CSGEndpoints
	if endpoints == nil {
		endpoints = []agentproto.CSGEndpointInfo{}
	}
	resp := &CSGTopologyReconcileResp{
		AgentID:              agentID,
		DiscoveryMode:        live.DiscoveryMode,
		CSGEndpoints:         endpoints,
		PortUsages:           live.PortUsages,
		Services:             []ReconciledCSGService{},
		Subscriptions:        []ReconciledCSGSubscription{},
		FilteredRemoteCount:  live.FilteredRemoteCount,
		FilteredTransientCnt: live.FilteredTransientCnt,
		CapturedAt:           live.CapturedAt,
		Errors:               live.Errors,
	}
	resp.Summary.LiveServiceCount = len(live.Services)
	resp.Summary.LiveSubCount = len(live.Subscriptions)

	populateSSOTServiceMeta := func(item *ReconciledCSGService, svc models.ModuleService, includeExpectedPort bool) {
		sid := svc.ID
		item.SSOTServiceID = &sid
		if svc.ModuleID != nil && *svc.ModuleID > 0 {
			mid := *svc.ModuleID
			item.SSOTModuleID = &mid
		}
		item.SSOTProtoSvc = strings.TrimSpace(svc.ProtoServiceName)
		if svc.Module != nil {
			item.SSOTModuleCode = svc.Module.Code
			item.SSOTModuleName = svc.Module.Name
			item.SSOTArchProcessID = svc.Module.ArchProcessID
			if includeExpectedPort && svc.Module.ArchProcessID != nil {
				if p := mountedPortByArchProcID[*svc.Module.ArchProcessID]; p > 0 {
					item.ExpectedPort = p
				} else if svc.Module.ArchProcess != nil && svc.Module.ArchProcess.DefaultListenPort > 0 {
					item.ExpectedPort = svc.Module.ArchProcess.DefaultListenPort
				}
			}
		}
	}

	matchedSvcIDs := make(map[uint64]struct{})
	for _, ls := range live.Services {
		item := ReconciledCSGService{
			ServiceName:   ls.ServiceName,
			Addr:          ls.Addr,
			Transport:     ls.Transport,
			HostIP:        ls.HostIP,
			Port:          ls.Port,
			ServerPID:     ls.ServerPID,
			ServerProc:    ls.ServerProc,
			OsNodeID:      ls.OsNodeID,
			Version:       ls.Version,
			ResolvedBySub: ls.ResolvedBySub,
		}

		matched, ok := byExactName[ls.ServiceName]
		if !ok {
			matched, ok = byNormName[StripCSGPIDSuffix(ls.ServiceName)]
		}
		if ok {
			item.State = CSGReconcileMatched
			populateSSOTServiceMeta(&item, matched, false)
			matchedSvcIDs[matched.ID] = struct{}{}
			resp.Summary.MatchedCount++
		} else {
			item.State = CSGReconcileUndeclared
			resp.Summary.UndeclaredCount++
		}
		resp.Services = append(resp.Services, item)
	}

	for _, ms := range mountedServices {
		if _, seen := matchedSvcIDs[ms.ID]; seen {
			continue
		}
		item := ReconciledCSGService{
			ServiceName: ms.ServiceName,
			State:       CSGReconcileMissingInLive,
		}
		populateSSOTServiceMeta(&item, ms, true)
		resp.Summary.MissingInLiveCount++
		resp.Services = append(resp.Services, item)
	}

	var deps []models.ModuleDependency
	if err := db.Preload("ConsumerModule").
		Preload("ConsumerModule.ArchProcess").
		Preload("Service").
		Preload("Contract").
		Where("is_external = ?", false).
		Order("id ASC").
		Find(&deps).Error; err != nil {
		return nil, err
	}

	type depKey struct {
		procHint string
		svcNorm  string
	}
	depByKey := make(map[depKey]models.ModuleDependency)
	mountedDeps := make([]models.ModuleDependency, 0)
	for _, d := range deps {
		if d.ConsumerModule == nil {
			continue
		}
		var targetName string
		if d.Service != nil {
			targetName = d.Service.ServiceName
		} else if d.Contract != nil {
			targetName = d.Contract.ServiceName
		}
		if targetName == "" {
			continue
		}
		normTarget := StripCSGPIDSuffix(targetName)
		if d.ConsumerModule.ArchProcess != nil {
			ap := d.ConsumerModule.ArchProcess
			for _, h := range []string{ap.Code, ap.Name, ap.DefaultCmdPattern} {
				if hn := strings.ToLower(strings.TrimSpace(h)); hn != "" {
					depByKey[depKey{procHint: hn, svcNorm: normTarget}] = d
				}
			}
		}
		depByKey[depKey{procHint: strings.ToLower(d.ConsumerModule.Code), svcNorm: normTarget}] = d
		if d.ConsumerModule.ArchProcessID != nil {
			if _, ok := mountedArchProcIDs[*d.ConsumerModule.ArchProcessID]; ok {
				mountedDeps = append(mountedDeps, d)
			}
		}
	}

	matchedDepIDs := make(map[uint64]struct{})
	for _, lsub := range live.Subscriptions {
		item := ReconciledCSGSubscription{
			ClientPID:   lsub.ClientPID,
			ClientProc:  lsub.ClientProc,
			ClientAddr:  lsub.ClientAddr,
			ServiceName: lsub.ServiceName,
		}
		procHint := strings.ToLower(strings.TrimSpace(lsub.ClientProc))
		normSvc := StripCSGPIDSuffix(lsub.ServiceName)
		if d, ok := depByKey[depKey{procHint: procHint, svcNorm: normSvc}]; ok {
			did := d.ID
			item.State = CSGReconcileMatched
			item.SSOTDepID = &did
			matchedDepIDs[d.ID] = struct{}{}
			resp.Summary.SubMatchedCount++
		} else {
			item.State = CSGReconcileUndeclared
			resp.Summary.SubUndeclaredCount++
		}
		resp.Subscriptions = append(resp.Subscriptions, item)
	}

	for _, d := range mountedDeps {
		if _, seen := matchedDepIDs[d.ID]; seen {
			continue
		}
		did := d.ID
		var svcName string
		if d.Service != nil {
			svcName = d.Service.ServiceName
		} else if d.Contract != nil {
			svcName = d.Contract.ServiceName
		}
		procName := ""
		if d.ConsumerModule != nil {
			procName = d.ConsumerModule.Code
			if d.ConsumerModule.ArchProcess != nil {
				procName = d.ConsumerModule.ArchProcess.Code
			}
		}
		resp.Summary.SubMissingCount++
		resp.Subscriptions = append(resp.Subscriptions, ReconciledCSGSubscription{
			ClientProc:  procName,
			ServiceName: svcName,
			State:       CSGReconcileMissingInLive,
			SSOTDepID:   &did,
		})
	}

	return resp, nil
}

// CSGBlueprintImportInput controls idempotent blueprint import from live CSG discoveries.
type CSGBlueprintImportInput struct {
	AgentID            uint64
	SubsystemID        uint64
	GroupID            *uint64
	ProcessGroupID     *uint64
	ServiceNames       []string
	ImportDependencies bool
	OperatorID         uint
	Live               *agentproto.CSGTopologyReport
}

// CSGBlueprintImportResult reports created/skipped counts for idempotent audit feedback.
type CSGBlueprintImportResult struct {
	CreatedProcesses    int    `json:"created_processes"`
	SkippedProcesses    int    `json:"skipped_processes"`
	CreatedModules      int    `json:"created_modules"`
	CreatedServices     int    `json:"created_services"`
	SkippedServices     int    `json:"skipped_services"`
	CreatedDependencies int    `json:"created_dependencies"`
	SkippedDependencies int    `json:"skipped_dependencies"`
	FunctionalGroupID   uint64 `json:"functional_group_id"`
	ProcessGroupID      uint64 `json:"process_group_id"`
}

// ImportCSGBlueprint idempotently persists live CSG processes, modules, services,
// and optional subscription dependencies within a single ACID transaction guarded
// by ValidateDependencyGuard.
func ImportCSGBlueprint(db *gorm.DB, in CSGBlueprintImportInput) (*CSGBlueprintImportResult, error) {
	if in.Live == nil {
		return &CSGBlueprintImportResult{}, nil
	}

	result := &CSGBlueprintImportResult{}

	err := db.Transaction(func(tx *gorm.DB) error {
		var agent models.Agent
		if err := tx.First(&agent, in.AgentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAgentNotFound
			}
			return err
		}

		var subsys models.Subsystem
		if err := tx.First(&subsys, in.SubsystemID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSubsystemNotFound
			}
			return err
		}

		group, err := ensureBlueprintFunctionalGroup(tx, in.SubsystemID, in.GroupID, in.OperatorID)
		if err != nil {
			return err
		}
		result.FunctionalGroupID = group.ID

		pgrp, err := ensureBlueprintProcessGroup(tx, in.ProcessGroupID, in.OperatorID)
		if err != nil {
			return err
		}
		result.ProcessGroupID = pgrp.ID

		allowSvc := make(map[string]struct{}, len(in.ServiceNames))
		for _, n := range in.ServiceNames {
			if t := strings.TrimSpace(n); t != "" {
				allowSvc[t] = struct{}{}
				allowSvc[StripCSGPIDSuffix(t)] = struct{}{}
			}
		}

		type modEntity struct {
			mod *models.Module
			ap  *models.ArchProcess
		}
		procModulesByKey := make(map[string]modEntity)

		ensureProcessAndModule := func(rawProc string, listenPort uint16) (*models.ArchProcess, *models.Module, error) {
			procKey := sanitizeBlueprintCode(rawProc, "csg_proc")
			if cached, ok := procModulesByKey[procKey]; ok {
				return cached.ap, cached.mod, nil
			}

			apCode := truncateMax(procKey, 64)
			var ap models.ArchProcess
			err := tx.Where("code = ?", apCode).First(&ap).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				ap = models.ArchProcess{
					GroupID:           pgrp.ID,
					Code:              apCode,
					Name:              firstNonEmpty(rawProc, apCode),
					DefaultListenPort: int(listenPort),
					DefaultCmdPattern: firstNonEmpty(rawProc, apCode),
					CreatedBy:         in.OperatorID,
					UpdatedBy:         in.OperatorID,
				}
				if err := tx.Create(&ap).Error; err != nil {
					return nil, nil, err
				}
				result.CreatedProcesses++
			} else if err != nil {
				return nil, nil, err
			} else {
				result.SkippedProcesses++
			}

			var mount models.Process
			err = tx.Where("agent_id = ? AND arch_process_id = ?", in.AgentID, ap.ID).First(&mount).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				effPort := ap.DefaultListenPort
				if effPort == 0 && listenPort > 0 {
					effPort = int(listenPort)
				}
				mount = models.Process{
					AgentID:             in.AgentID,
					ArchProcessID:       ap.ID,
					EffectiveListenPort: effPort,
					ExtraDownstreams:    datatypes.JSON("[]"),
					ProbeDesiredState:   models.ProbeDesiredStateArmed,
					ProbeStatus:         models.ProbeStatusDisarmed,
					CreatedBy:           in.OperatorID,
					UpdatedBy:           in.OperatorID,
				}
				if err := tx.Create(&mount).Error; err != nil {
					return nil, nil, err
				}
			} else if err != nil {
				return nil, nil, err
			}

			modCode := truncateMax("MOD_"+apCode, 64)
			var mod models.Module
			err = tx.Where("code = ?", modCode).First(&mod).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				apID := ap.ID
				mod = models.Module{
					SubsystemID:   in.SubsystemID,
					GroupID:       group.ID,
					Code:          modCode,
					Name:          firstNonEmpty(rawProc, modCode),
					ArchProcessID: &apID,
					ModuleKind:    models.ModuleKindBusiness,
					CreatedBy:     in.OperatorID,
					UpdatedBy:     in.OperatorID,
				}
				if err := tx.Create(&mod).Error; err != nil {
					return nil, nil, err
				}
				result.CreatedModules++
			} else if err != nil {
				return nil, nil, err
			}

			procModulesByKey[procKey] = modEntity{mod: &mod, ap: &ap}
			return &ap, &mod, nil
		}

		var selectedSvcs []agentproto.CSGServiceInfo
		for _, ls := range in.Live.Services {
			if len(allowSvc) > 0 {
				if _, ok := allowSvc[ls.ServiceName]; !ok {
					if _, okNorm := allowSvc[StripCSGPIDSuffix(ls.ServiceName)]; !okNorm {
						continue
					}
				}
			}
			selectedSvcs = append(selectedSvcs, ls)
		}
		sort.Slice(selectedSvcs, func(i, j int) bool {
			return selectedSvcs[i].ServiceName < selectedSvcs[j].ServiceName
		})

		svcNamesToLock := make([]string, 0, len(selectedSvcs))
		for _, s := range selectedSvcs {
			svcNamesToLock = append(svcNamesToLock, StripCSGPIDSuffix(s.ServiceName))
		}
		if len(svcNamesToLock) > 0 {
			if err := lockServiceNamesSorted(tx, svcNamesToLock...); err != nil {
				return err
			}
		}

		for _, ls := range selectedSvcs {
			procHint := firstNonEmpty(ls.ServerProc, "csg_svc_"+strconv.FormatUint(uint64(ls.ServerPID), 10))
			listenPort := ls.Port
			if strings.EqualFold(strings.TrimSpace(ls.Transport), "MESH") {
				listenPort = 0
			}
			_, mod, err := ensureProcessAndModule(procHint, listenPort)
			if err != nil {
				return err
			}

			svcName := truncateMax(StripCSGPIDSuffix(ls.ServiceName), 128)
			var existing models.ModuleService
			err = tx.Where("service_name = ?", svcName).First(&existing).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				ms := models.ModuleService{
					ModuleID:        models.Uint64Ptr(mod.ID),
					ServiceName:     svcName,
					DiffIgnorePaths: datatypes.JSON("[]"),
					Description:     fmt.Sprintf("Imported from CSG (%s %s)", ls.Transport, ls.Addr),
					CreatedBy:       in.OperatorID,
				}
				if err := tx.Create(&ms).Error; err != nil {
					return err
				}
				result.CreatedServices++
			} else if err != nil {
				return err
			} else {
				result.SkippedServices++
			}
		}

		if in.ImportDependencies {
			for _, sub := range in.Live.Subscriptions {
				if len(allowSvc) > 0 {
					if _, ok := allowSvc[sub.ServiceName]; !ok {
						if _, okNorm := allowSvc[StripCSGPIDSuffix(sub.ServiceName)]; !okNorm {
							continue
						}
					}
				}

				targetSvcName := StripCSGPIDSuffix(sub.ServiceName)
				var providerSvc models.ModuleService
				if err := tx.Where("service_name = ?", targetSvcName).First(&providerSvc).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						continue
					}
					return err
				}

				consumerProc := firstNonEmpty(sub.ClientProc, "csg_cli_"+strconv.FormatUint(uint64(sub.ClientPID), 10))
				_, consumerMod, err := ensureProcessAndModule(consumerProc, 0)
				if err != nil {
					return err
				}
				if providerSvc.ModuleID != nil && consumerMod.ID == *providerSvc.ModuleID {
					continue
				}

				var existingDep models.ModuleDependency
				err = tx.Where("consumer_module_id = ? AND service_id = ?", consumerMod.ID, providerSvc.ID).First(&existingDep).Error
				if err == nil {
					result.SkippedDependencies++
					continue
				}
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}

				svcID := providerSvc.ID
				dep := models.ModuleDependency{
					ConsumerModuleID: consumerMod.ID,
					IsExternal:       false,
					ServiceID:        &svcID,
					Description:      "Imported from CSG live subscription",
					CreatedBy:        in.OperatorID,
				}
				if err := tx.Create(&dep).Error; err != nil {
					return err
				}
				if err := ValidateDependencyGuard(tx, &dep); err != nil {
					return err
				}
				result.CreatedDependencies++
			}
		}

		if result.CreatedProcesses > 0 {
			if err := tx.Model(&models.Agent{}).Where("id = ?", in.AgentID).
				Update("mounts_revision", gorm.Expr("mounts_revision + 1")).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func ensureBlueprintFunctionalGroup(tx *gorm.DB, subsystemID uint64, groupID *uint64, operatorID uint) (*models.FunctionalGroup, error) {
	var grp models.FunctionalGroup
	if groupID != nil && *groupID > 0 {
		if err := tx.First(&grp, *groupID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrInvalidGroupTarget
			}
			return nil, err
		}
		if grp.SubsystemID != subsystemID {
			return nil, ErrInvalidGroupTarget
		}
		return &grp, nil
	}

	const defaultCode = "CSG_DISCOVERED"
	err := tx.Where("subsystem_id = ? AND code = ?", subsystemID, defaultCode).First(&grp).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		grp = models.FunctionalGroup{
			SubsystemID: subsystemID,
			Code:        defaultCode,
			Name:        "CSG 自动发现组",
			Level:       1,
			CreatedBy:   operatorID,
			UpdatedBy:   operatorID,
		}
		if err := tx.Create(&grp).Error; err != nil {
			return nil, err
		}
		return &grp, nil
	}
	if err != nil {
		return nil, err
	}
	return &grp, nil
}

func ensureBlueprintProcessGroup(tx *gorm.DB, pgrpID *uint64, operatorID uint) (*models.ArchProcessGroup, error) {
	var pgrp models.ArchProcessGroup
	if pgrpID != nil && *pgrpID > 0 {
		if err := tx.First(&pgrp, *pgrpID).Error; err != nil {
			return nil, err
		}
		return &pgrp, nil
	}
	const defaultCode = "PGRP_CSG_AUTO"
	err := tx.Where("code = ?", defaultCode).First(&pgrp).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		pgrp = models.ArchProcessGroup{
			Code:        defaultCode,
			Name:        "CSG 自动发现进程组",
			Description: "由 Agent 现网 CSG 拓扑一键导入创建",
			CreatedBy:   operatorID,
			UpdatedBy:   operatorID,
		}
		if err := tx.Create(&pgrp).Error; err != nil {
			return nil, err
		}
		return &pgrp, nil
	}
	if err != nil {
		return nil, err
	}
	return &pgrp, nil
}

func sanitizeBlueprintCode(raw, fallback string) string {
	cleaned := strings.Trim(nonCodeCharsPattern.ReplaceAllString(strings.TrimSpace(raw), "_"), "_-")
	if cleaned == "" {
		return fallback
	}
	return cleaned
}

func truncateMax(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// ============================================================================
// Part 3: M31 统一契约服务目录、三级 .pb 撮合与权威环境五态健康雷达 (ADR-24 §5.10)
// ============================================================================

const (
	RadarStateHealthy         = "HEALTHY"
	RadarStateShadowLive      = "SHADOW_LIVE"
	RadarStateUnclaimedOrphan = "UNCLAIMED_ORPHAN"
	RadarStatePhantomProto    = "PHANTOM_PROTO"
	RadarStateVanishedClaimed = "VANISHED_CLAIMED"
	RadarStatePlacementDrift  = "PLACEMENT_DRIFT"
)

// FiveStateRadarDTO 统一契约目录五态健康雷达（严格仅统计 SHARED + CONFIRMED 权威环境）
type FiveStateRadarDTO struct {
	ShadowLiveCount      int      `json:"shadow_live_count"`
	UnclaimedOrphanCount int      `json:"unclaimed_orphan_count"`
	PhantomProtoCount    int      `json:"phantom_proto_count"`
	VanishedClaimedCount int      `json:"vanished_claimed_count"`
	PlacementDriftCount  int      `json:"placement_drift_count"`
	TotalCatalogServices int      `json:"total_catalog_services"`
	AuthorityScopeOnly   bool     `json:"authority_scope_only"`
	ShadowLiveServices   []string `json:"shadow_live_services,omitempty"`
	UnclaimedOrphans     []string `json:"unclaimed_orphans,omitempty"`
	PhantomProtos        []string `json:"phantom_protos,omitempty"`
	VanishedClaimed      []string `json:"vanished_claimed,omitempty"`
	PlacementDrifts      []string `json:"placement_drifts,omitempty"`
}

// UnifiedServiceCatalogDTO 统一契约服务目录行项目（融合 L1 物理放置、L2 逻辑认领、.pb 撮合与五态健康标签）
type UnifiedServiceCatalogDTO struct {
	ID                   uint64   `json:"id"`
	ServiceName          string   `json:"service_name"`
	Source               string   `json:"source"`
	ClaimStatus          string   `json:"claim_status"` // CLAIMED | UNCLAIMED | PHANTOM_ONLY
	ModuleID             *uint64  `json:"module_id,omitempty"`
	ModuleCode           string   `json:"module_code,omitempty"`
	ModuleName           string   `json:"module_name,omitempty"`
	ContractID           *uint64  `json:"contract_id,omitempty"`
	ImplKey              string   `json:"impl_key,omitempty"`
	ProtoServiceName     string   `json:"proto_service_name,omitempty"`
	ProtoSuggestedName   string   `json:"proto_suggested_name,omitempty"`
	ProtoMatchConfidence string   `json:"proto_match_confidence"`
	ProtoStatus          string   `json:"proto_status"` // CONFIRMED | SUGGESTED | UNMATCHED
	OperationCount       int      `json:"operation_count"`
	SharedPlacementCount int      `json:"shared_placement_count"`
	ActiveArchProcessIDs []uint64 `json:"active_arch_process_ids,omitempty"`
	Transports           []string `json:"transports,omitempty"`
	RecentTrafficCount   int64    `json:"recent_traffic_count"`
	RadarState           string   `json:"radar_state"`
	RadarStates          []string `json:"radar_states,omitempty"`
}

// CatalogServicesQuery 统一契约服务目录检索条件
type CatalogServicesQuery struct {
	State         string
	ProtoStatus   string
	ArchProcessID uint64
	Search        string
	Page          int
	PageSize      int
}

// CatalogServicesResponseDTO GET /api/catalog/services 响应载荷
type CatalogServicesResponseDTO struct {
	Radar    FiveStateRadarDTO          `json:"radar"`
	Items    []UnifiedServiceCatalogDTO `json:"items"`
	Total    int64                      `json:"total"`
	Page     int                        `json:"page"`
	PageSize int                        `json:"page_size"`
}

// CatalogCorrelateResult POST /api/catalog/correlate 三级撮合结果
type CatalogCorrelateResult struct {
	ExactMatched   int  `json:"exact_matched"`
	SuggestedCount int  `json:"suggested_count"`
	Reloaded       bool `json:"reloaded"`
}

type catalogEvalSnapshot struct {
	radar *FiveStateRadarDTO
	items []UnifiedServiceCatalogDTO
}

func evaluateCatalogAndRadar(ctx context.Context, db *gorm.DB, userID uint, isAdmin bool, filterVisibility bool) (*catalogEvalSnapshot, error) {
	// 1. 查询全部 ModuleService
	var allSvcs []models.ModuleService
	if err := db.WithContext(ctx).Preload("Module").Preload("Contract").Order("id ASC").Find(&allSvcs).Error; err != nil {
		return nil, err
	}

	// 2. 查询全部 ServicePlacement（携带 Agent.Scope 与 ArchProcess.Lifecycle）
	var allPlacements []models.ServicePlacement
	if err := db.WithContext(ctx).
		Preload("Agent").
		Preload("ArchProcess").
		Find(&allPlacements).Error; err != nil {
		return nil, err
	}

	// 3. 查询权威 SHARED + CONFIRMED 订阅边（用于 PHANTOM_PROTO 判定）
	var allDeps []models.ProcessDependency
	if err := db.WithContext(ctx).
		Preload("Agent").
		Preload("ConsumerArchProcess").
		Where("status <> ?", models.PlacementStatusRetired).
		Find(&allDeps).Error; err != nil {
		return nil, err
	}

	// 4. 查询已激活 ProtoOperation
	var activeOps []models.ProtoOperation
	if err := db.WithContext(ctx).Where("is_active = true").Find(&activeOps).Error; err != nil {
		return nil, err
	}
	activeProtoOpCount := make(map[string]int)
	for _, op := range activeOps {
		activeProtoOpCount[op.ServiceName]++
	}

	// 5. 轻量化聚合近期待归因/已采集流量条数（按 service_name 分组，零 bytea 扫描）
	type svcTrafficAgg struct {
		ServiceName string
		Cnt         int64
	}
	var trafficCounts []svcTrafficAgg
	_ = db.WithContext(ctx).
		Model(&models.TrafficRecord{}).
		Select("service_name, COUNT(*) AS cnt").
		Where("service_name <> ''").
		Group("service_name").
		Scan(&trafficCounts).Error
	trafficCountMap := make(map[string]int64, len(trafficCounts))
	for _, tc := range trafficCounts {
		trafficCountMap[tc.ServiceName] += tc.Cnt
		norm := StripCSGPIDSuffix(tc.ServiceName)
		if norm != tc.ServiceName {
			trafficCountMap[norm] += tc.Cnt
		}
	}

	// 6. 汇总每个 ModuleService 的权威放置与私有可见性
	type svcPlacementAgg struct {
		hasSharedEver       bool
		hasSharedNonRetired bool
		sharedActiveCount   int
		sharedActiveArchIDs map[uint64]bool
		transports          map[string]bool
		hasPrivOwner        bool
		hasAnyPriv          bool
	}
	plAggBySvcID := make(map[uint64]*svcPlacementAgg, len(allSvcs))
	ensureAgg := func(id uint64) *svcPlacementAgg {
		if a, ok := plAggBySvcID[id]; ok {
			return a
		}
		a := &svcPlacementAgg{
			sharedActiveArchIDs: make(map[uint64]bool),
			transports:          make(map[string]bool),
		}
		plAggBySvcID[id] = a
		return a
	}

	for _, pl := range allPlacements {
		agg := ensureAgg(pl.ModuleServiceID)
		isSharedAuth := pl.Agent != nil && pl.Agent.Scope == models.ScopeShared &&
			pl.ArchProcess != nil && pl.ArchProcess.Lifecycle == models.ArchProcLifecycleConfirmed
		if isSharedAuth {
			agg.hasSharedEver = true
			if pl.Status != models.PlacementStatusRetired {
				agg.hasSharedNonRetired = true
				agg.transports[pl.ObservedTransport] = true
			}
			if pl.Status == models.PlacementStatusActive {
				agg.sharedActiveCount++
				agg.sharedActiveArchIDs[pl.ArchProcessID] = true
			}
		} else if pl.Agent != nil && pl.Agent.Scope == models.ScopePrivate {
			agg.hasAnyPriv = true
			if pl.Agent.OwnerID != nil && *pl.Agent.OwnerID == userID {
				agg.hasPrivOwner = true
			}
		}
	}

	// 记录被权威环境放置或订阅覆盖到的 .pb 服务名
	coveredProtoSvcs := make(map[string]bool)
	for _, d := range allDeps {
		if d.Agent != nil && d.Agent.Scope == models.ScopeShared &&
			d.ConsumerArchProcess != nil && d.ConsumerArchProcess.Lifecycle == models.ArchProcLifecycleConfirmed {
			coveredProtoSvcs[d.ServiceName] = true
			coveredProtoSvcs[StripCSGPIDSuffix(d.ServiceName)] = true
		}
	}

	radar := FiveStateRadarDTO{
		AuthorityScopeOnly: true,
	}
	svcByProtoName := make(map[string]bool)
	items := make([]UnifiedServiceCatalogDTO, 0, len(allSvcs))

	for _, ms := range allSvcs {
		agg := ensureAgg(ms.ID)
		normName := StripCSGPIDSuffix(ms.ServiceName)
		if ms.ProtoServiceName != "" {
			svcByProtoName[ms.ProtoServiceName] = true
		}
		svcByProtoName[ms.ServiceName] = true
		svcByProtoName[normName] = true

		hasConfirmedProto := strings.TrimSpace(ms.ProtoServiceName) != "" ||
			activeProtoOpCount[ms.ServiceName] > 0 ||
			activeProtoOpCount[normName] > 0

		if agg.hasSharedNonRetired {
			if ms.ProtoServiceName != "" {
				coveredProtoSvcs[ms.ProtoServiceName] = true
			}
			coveredProtoSvcs[ms.ServiceName] = true
			coveredProtoSvcs[normName] = true
			if ms.Contract != nil && ms.Contract.ServiceName != "" {
				coveredProtoSvcs[ms.Contract.ServiceName] = true
			}
		}

		// 计算权威环境五态分类（仅统计 SHARED + CONFIRMED）
		var states []string
		if agg.hasSharedNonRetired && !hasConfirmedProto {
			states = append(states, RadarStateShadowLive)
			radar.ShadowLiveCount++
			radar.ShadowLiveServices = append(radar.ShadowLiveServices, ms.ServiceName)
		}
		if agg.hasSharedNonRetired && hasConfirmedProto && !ms.IsClaimed() {
			states = append(states, RadarStateUnclaimedOrphan)
			radar.UnclaimedOrphanCount++
			radar.UnclaimedOrphans = append(radar.UnclaimedOrphans, ms.ServiceName)
		}
		if ms.IsClaimed() && agg.hasSharedEver && !agg.hasSharedNonRetired {
			states = append(states, RadarStateVanishedClaimed)
			radar.VanishedClaimedCount++
			radar.VanishedClaimed = append(radar.VanishedClaimed, ms.ServiceName)
		}
		if ms.IsClaimed() && ms.Module != nil && ms.Module.ArchProcessID != nil {
			expectedArch := *ms.Module.ArchProcessID
			drifted := false
			for apID := range agg.sharedActiveArchIDs {
				if apID != expectedArch {
					drifted = true
					break
				}
			}
			if drifted {
				states = append(states, RadarStatePlacementDrift)
				radar.PlacementDriftCount++
				radar.PlacementDrifts = append(radar.PlacementDrifts, ms.ServiceName)
			}
		}

		// V3 目录可见性过滤：未认领且无 SHARED 放置时，仅对 PRIVATE owner 与管理员可见
		if filterVisibility && !ms.IsClaimed() && !agg.hasSharedEver {
			if !isAdmin && !agg.hasPrivOwner {
				continue
			}
		}

		primaryState := RadarStateHealthy
		if len(states) > 0 {
			primaryState = states[0]
		}
		claimStatus := "UNCLAIMED"
		if ms.IsClaimed() {
			claimStatus = "CLAIMED"
		}
		protoStatus := "UNMATCHED"
		opCount := 0
		if strings.TrimSpace(ms.ProtoServiceName) != "" {
			protoStatus = "CONFIRMED"
			opCount = activeProtoOpCount[ms.ProtoServiceName]
		} else if activeProtoOpCount[ms.ServiceName] > 0 {
			protoStatus = "CONFIRMED"
			opCount = activeProtoOpCount[ms.ServiceName]
		} else if activeProtoOpCount[normName] > 0 {
			protoStatus = "CONFIRMED"
			opCount = activeProtoOpCount[normName]
		} else if strings.TrimSpace(ms.ProtoSuggestedName) != "" {
			protoStatus = "SUGGESTED"
			opCount = activeProtoOpCount[ms.ProtoSuggestedName]
		}

		archIDs := make([]uint64, 0, len(agg.sharedActiveArchIDs))
		for id := range agg.sharedActiveArchIDs {
			archIDs = append(archIDs, id)
		}
		sort.Slice(archIDs, func(i, j int) bool { return archIDs[i] < archIDs[j] })

		transports := make([]string, 0, len(agg.transports))
		for tr := range agg.transports {
			transports = append(transports, tr)
		}
		sort.Strings(transports)

		dto := UnifiedServiceCatalogDTO{
			ID:                   ms.ID,
			ServiceName:          ms.ServiceName,
			Source:               ms.Source,
			ClaimStatus:          claimStatus,
			ModuleID:             ms.ModuleID,
			ContractID:           ms.ContractID,
			ImplKey:              ms.ImplKey,
			ProtoServiceName:     ms.ProtoServiceName,
			ProtoSuggestedName:   ms.ProtoSuggestedName,
			ProtoMatchConfidence: ms.ProtoMatchConfidence,
			ProtoStatus:          protoStatus,
			OperationCount:       opCount,
			SharedPlacementCount: agg.sharedActiveCount,
			ActiveArchProcessIDs: archIDs,
			Transports:           transports,
			RecentTrafficCount:   trafficCountMap[ms.ServiceName],
			RadarState:           primaryState,
			RadarStates:          states,
		}
		if ms.Module != nil {
			dto.ModuleCode = ms.Module.Code
			dto.ModuleName = ms.Module.Name
		}
		items = append(items, dto)
	}

	// 计算 PHANTOM_PROTO（已激活 .pb 服务在权威 SHARED 环境既无非 RETIRED 放置也无非 RETIRED 订阅边）
	phantomNames := make([]string, 0)
	for protoSvc := range activeProtoOpCount {
		if !coveredProtoSvcs[protoSvc] {
			phantomNames = append(phantomNames, protoSvc)
		}
	}
	sort.Strings(phantomNames)
	for _, pn := range phantomNames {
		radar.PhantomProtoCount++
		radar.PhantomProtos = append(radar.PhantomProtos, pn)
		if !svcByProtoName[pn] {
			items = append(items, UnifiedServiceCatalogDTO{
				ID:                   0,
				ServiceName:          pn,
				Source:               models.ServiceSourceManual,
				ClaimStatus:          "PHANTOM_ONLY",
				ProtoServiceName:     pn,
				ProtoMatchConfidence: models.ProtoMatchConfExact,
				ProtoStatus:          "CONFIRMED",
				OperationCount:       activeProtoOpCount[pn],
				RecentTrafficCount:   trafficCountMap[pn],
				RadarState:           RadarStatePhantomProto,
				RadarStates:          []string{RadarStatePhantomProto},
			})
		} else {
			for idx := range items {
				if items[idx].ServiceName == pn || items[idx].ProtoServiceName == pn {
					items[idx].RadarStates = append(items[idx].RadarStates, RadarStatePhantomProto)
					if items[idx].RadarState == RadarStateHealthy {
						items[idx].RadarState = RadarStatePhantomProto
					}
				}
			}
		}
	}

	radar.TotalCatalogServices = len(items)
	return &catalogEvalSnapshot{
		radar: &radar,
		items: items,
	}, nil
}

// ComputeFiveStateRadar 计算权威 SHARED 环境五态契约健康雷达（严格排除 PRIVATE 与 PROVISIONAL）
func ComputeFiveStateRadar(ctx context.Context, db *gorm.DB) (*FiveStateRadarDTO, error) {
	if db == nil {
		return &FiveStateRadarDTO{AuthorityScopeOnly: true}, nil
	}
	snap, err := evaluateCatalogAndRadar(ctx, db, 0, true, false)
	if err != nil {
		return nil, err
	}
	return snap.radar, nil
}

// ListUnifiedCatalogServices 分页检索统一契约服务目录（含五态雷达摘要、V3 可见性隔离与过滤）
func ListUnifiedCatalogServices(
	ctx context.Context,
	db *gorm.DB,
	q CatalogServicesQuery,
	userID uint,
	isAdmin bool,
) (*CatalogServicesResponseDTO, error) {
	if db == nil {
		return nil, errors.New("catalog: nil db")
	}
	snap, err := evaluateCatalogAndRadar(ctx, db, userID, isAdmin, true)
	if err != nil {
		return nil, err
	}

	stateFilter := strings.ToUpper(strings.TrimSpace(q.State))
	protoFilter := strings.ToUpper(strings.TrimSpace(q.ProtoStatus))
	kw := strings.ToLower(strings.TrimSpace(q.Search))

	filtered := make([]UnifiedServiceCatalogDTO, 0, len(snap.items))
	for _, it := range snap.items {
		if stateFilter != "" && stateFilter != "ALL" {
			matchedState := it.RadarState == stateFilter
			if !matchedState {
				for _, st := range it.RadarStates {
					if st == stateFilter {
						matchedState = true
						break
					}
				}
			}
			if !matchedState {
				continue
			}
		}
		if protoFilter != "" && protoFilter != "ALL" && it.ProtoStatus != protoFilter {
			continue
		}
		if q.ArchProcessID > 0 {
			hasArch := false
			for _, ap := range it.ActiveArchProcessIDs {
				if ap == q.ArchProcessID {
					hasArch = true
					break
				}
			}
			if !hasArch {
				continue
			}
		}
		if kw != "" {
			hay := strings.ToLower(it.ServiceName + " " + it.ProtoServiceName + " " + it.ProtoSuggestedName + " " + it.ModuleName + " " + it.ModuleCode)
			if !strings.Contains(hay, kw) {
				continue
			}
		}
		filtered = append(filtered, it)
	}

	page := q.Page
	if page <= 0 {
		page = 1
	}
	pageSize := q.PageSize
	if pageSize <= 0 {
		pageSize = 25
	}
	total := int64(len(filtered))
	start := (page - 1) * pageSize
	var paged []UnifiedServiceCatalogDTO
	if start < len(filtered) {
		end := start + pageSize
		if end > len(filtered) {
			end = len(filtered)
		}
		paged = filtered[start:end]
	} else {
		paged = []UnifiedServiceCatalogDTO{}
	}

	return &CatalogServicesResponseDTO{
		Radar:    *snap.radar,
		Items:    paged,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// CorrelateCatalogServices 执行三级 .pb 自动撮合：
// 1. EXACT: 精确命中 ProtoOperation.ServiceName -> 自动写入 proto_service_name 并触发解码器重载
// 2. HEURISTIC: 拆解 <Class>-<Svr> 唯一命中 ProtoOperation.ProtoService -> 仅写 proto_suggested_name
// 3. FINGERPRINT: 录制操作集合覆盖激活 .pb 方法数 >= 80% 且唯一命中 -> 仅写 proto_suggested_name
func CorrelateCatalogServices(ctx context.Context, db *gorm.DB, serviceIDs []uint64) (*CatalogCorrelateResult, error) {
	if db == nil {
		return nil, errors.New("catalog: nil db")
	}

	var activeOps []models.ProtoOperation
	if err := db.WithContext(ctx).Where("is_active = true").Find(&activeOps).Error; err != nil {
		return nil, err
	}
	if len(activeOps) == 0 {
		return &CatalogCorrelateResult{}, nil
	}

	exactSet := make(map[string]bool)
	shortToServices := make(map[string]map[string]bool)
	protoOpSets := make(map[string]map[string]bool)

	for _, op := range activeOps {
		exactSet[op.ServiceName] = true
		if op.ProtoService != "" {
			if shortToServices[op.ProtoService] == nil {
				shortToServices[op.ProtoService] = make(map[string]bool)
			}
			shortToServices[op.ProtoService][op.ServiceName] = true
		}
		if protoOpSets[op.ServiceName] == nil {
			protoOpSets[op.ServiceName] = make(map[string]bool)
		}
		if op.Operation != "" {
			protoOpSets[op.ServiceName][op.Operation] = true
		}
	}

	var svcs []models.ModuleService
	q := db.WithContext(ctx).Order("id ASC")
	if len(serviceIDs) > 0 {
		q = q.Where("id IN ?", serviceIDs)
	}
	if err := q.Find(&svcs).Error; err != nil {
		return nil, err
	}

	// 预加载录制流量中观测到的方法集合（避免在循环内 N+1 查询）
	type recOpRow struct {
		ServiceName string
		Operation   string
	}
	var recOps []recOpRow
	_ = db.WithContext(ctx).
		Model(&models.TrafficRecord{}).
		Select("DISTINCT service_name, operation").
		Where("service_name <> '' AND operation <> ''").
		Scan(&recOps).Error
	recordedOpsBySvc := make(map[string]map[string]bool)
	for _, r := range recOps {
		for _, k := range []string{r.ServiceName, StripCSGPIDSuffix(r.ServiceName)} {
			if k == "" {
				continue
			}
			if recordedOpsBySvc[k] == nil {
				recordedOpsBySvc[k] = make(map[string]bool)
			}
			recordedOpsBySvc[k][r.Operation] = true
		}
	}

	res := &CatalogCorrelateResult{}

	for _, ms := range svcs {
		if strings.TrimSpace(ms.ProtoServiceName) != "" {
			continue
		}
		norm := StripCSGPIDSuffix(ms.ServiceName)

		// Tier 1: EXACT
		exactName := ""
		if exactSet[ms.ServiceName] {
			exactName = ms.ServiceName
		} else if exactSet[norm] {
			exactName = norm
		}
		if exactName != "" {
			if err := db.WithContext(ctx).Model(&models.ModuleService{}).Where("id = ?", ms.ID).Updates(map[string]interface{}{
				"proto_service_name":     exactName,
				"proto_suggested_name":   "",
				"proto_match_confidence": models.ProtoMatchConfExact,
			}).Error; err != nil {
				return nil, err
			}
			res.ExactMatched++
			res.Reloaded = true
			continue
		}

		// Tier 2: HEURISTIC (<Class>-<Svr> 拆分唯一命中)
		if sug := matchHeuristicProtoService(norm, shortToServices); sug != "" {
			if err := db.WithContext(ctx).Model(&models.ModuleService{}).Where("id = ?", ms.ID).Updates(map[string]interface{}{
				"proto_suggested_name":   sug,
				"proto_match_confidence": models.ProtoMatchConfHeuristic,
			}).Error; err != nil {
				return nil, err
			}
			res.SuggestedCount++
			continue
		}

		// Tier 3: FINGERPRINT（录制方法集合覆盖 >= 80% 且全局唯一）
		recSet := recordedOpsBySvc[ms.ServiceName]
		if len(recSet) == 0 {
			recSet = recordedOpsBySvc[norm]
		}
		if len(recSet) > 0 {
			if sug := matchFingerprintProtoService(recSet, protoOpSets); sug != "" {
				if err := db.WithContext(ctx).Model(&models.ModuleService{}).Where("id = ?", ms.ID).Updates(map[string]interface{}{
					"proto_suggested_name":   sug,
					"proto_match_confidence": models.ProtoMatchConfFingerprint,
				}).Error; err != nil {
					return nil, err
				}
				res.SuggestedCount++
			}
		}
	}

	return res, nil
}

func matchHeuristicProtoService(normName string, shortToServices map[string]map[string]bool) string {
	tokens := strings.Split(normName, "-")
	candidates := make([]string, 0, len(tokens)+1)
	if len(tokens) >= 2 {
		candidates = append(candidates, tokens[len(tokens)-1], tokens[0])
	}
	if dotIdx := strings.LastIndex(normName, "."); dotIdx >= 0 && dotIdx+1 < len(normName) {
		candidates = append(candidates, normName[dotIdx+1:])
	}

	for _, tok := range candidates {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		matches := shortToServices[tok]
		if len(matches) == 1 {
			for full := range matches {
				return full
			}
		}
	}
	return ""
}

func matchFingerprintProtoService(recOps map[string]bool, protoOpSets map[string]map[string]bool) string {
	var hits []string
	for protoSvc, opSet := range protoOpSets {
		if len(opSet) == 0 {
			continue
		}
		matched := 0
		for op := range opSet {
			if recOps[op] {
				matched++
			}
		}
		coverage := float64(matched) / float64(len(opSet))
		if coverage >= 0.80 {
			hits = append(hits, protoSvc)
		}
	}
	if len(hits) == 1 {
		return hits[0]
	}
	return ""
}

// ConfirmCatalogServiceProto 确认采纳推荐或人工绑定的 .pb 服务全名并校验层级权限：
// - 未认领身份：允许任意可见用户确认（P2 审计）
// - 已认领服务：仅限 contract_admin 或所属模块/子系统负责人确认（P1 审计）
func ConfirmCatalogServiceProto(
	ctx context.Context,
	db *gorm.DB,
	serviceID uint64,
	protoServiceName string,
	userID uint,
	isAdmin bool,
) (*models.ModuleService, string, error) {
	if db == nil {
		return nil, "", errors.New("catalog: nil db")
	}
	cleanProto, err := ValidateProtoServiceName(protoServiceName)
	if err != nil {
		return nil, "", err
	}
	if cleanProto == "" {
		return nil, "", ErrInvalidProtoServiceNameFormat
	}

	var ms models.ModuleService
	if err := db.WithContext(ctx).Preload("Module.Subsystem").First(&ms, serviceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", ErrModuleServiceNotFound
		}
		return nil, "", err
	}

	auditLevel := "P2"
	if ms.IsClaimed() {
		auditLevel = "P1"
		allowed := isAdmin
		if !allowed && ms.Module != nil {
			if ms.Module.OwnerID != nil && *ms.Module.OwnerID == userID {
				allowed = true
			} else if ms.Module.Subsystem != nil && ms.Module.Subsystem.OwnerID != nil && *ms.Module.Subsystem.OwnerID == userID {
				allowed = true
			}
		}
		if !allowed {
			return nil, "", ErrCatalogForbidden
		}
	} else if !isAdmin {
		// 未认领服务须满足 V3 可见性：有 SHARED 放置或当前用户是其 PRIVATE Agent 的 owner
		var pls []models.ServicePlacement
		if err := db.WithContext(ctx).Preload("Agent").Where("module_service_id = ?", ms.ID).Find(&pls).Error; err != nil {
			return nil, "", err
		}
		if len(pls) > 0 {
			visible := false
			for _, pl := range pls {
				if pl.Agent == nil {
					continue
				}
				if pl.Agent.Scope == models.ScopeShared || (pl.Agent.OwnerID != nil && *pl.Agent.OwnerID == userID) {
					visible = true
					break
				}
			}
			if !visible {
				return nil, "", ErrCatalogForbidden
			}
		}
	}

	if err := db.WithContext(ctx).Model(&models.ModuleService{}).Where("id = ?", ms.ID).Updates(map[string]interface{}{
		"proto_service_name":     cleanProto,
		"proto_suggested_name":   "",
		"proto_match_confidence": models.ProtoMatchConfManual,
	}).Error; err != nil {
		return nil, "", err
	}

	if err := db.WithContext(ctx).Preload("Module").First(&ms, ms.ID).Error; err != nil {
		return nil, "", err
	}
	return &ms, auditLevel, nil
}
