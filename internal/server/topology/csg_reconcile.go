package topology

import (
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
		mid := svc.ModuleID
		item.SSOTServiceID = &sid
		item.SSOTModuleID = &mid
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

	// Reconcile subscriptions against ModuleDependency for modules mounted on this Agent.
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
	ServiceNames       []string // empty => import all local live services
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

			// Ensure Process mount on agentID
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

		// Sort selected services for deterministic locking order
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
			_, mod, err := ensureProcessAndModule(procHint, ls.Port)
			if err != nil {
				return err
			}

			svcName := truncateMax(StripCSGPIDSuffix(ls.ServiceName), 128)
			var existing models.ModuleService
			err = tx.Where("service_name = ?", svcName).First(&existing).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				ms := models.ModuleService{
					ModuleID:        mod.ID,
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
				if consumerMod.ID == providerSvc.ModuleID {
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
