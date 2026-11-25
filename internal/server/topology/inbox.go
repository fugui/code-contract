package topology

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"code-contract/models"
	"gorm.io/gorm"
)

const (
	InboxKindUnclaimed      = "UNCLAIMED"
	InboxKindMultiHost      = "MULTI_HOST"
	InboxKindPlacementDrift = "PLACEMENT_DRIFT"
	InboxKindDepSuggestion  = "DEP_SUGGESTION"
)

// Operator 表示发起收件箱治理动作的当前用户上下文
type Operator struct {
	UserID  uint
	IsAdmin bool
}

// InboxQuery 架构收件箱过滤与分页参数
type InboxQuery struct {
	Kind          string
	ArchProcessID uint64
	Search        string
	Page          int
	PageSize      int
}

// InboxCountsDTO 收件箱各类待治理计数摘要
type InboxCountsDTO struct {
	UnclaimedCount      int `json:"unclaimed_count"`
	MultiHostCount      int `json:"multi_host_count"`
	PlacementDriftCount int `json:"placement_drift_count"`
	DepSuggestionCount  int `json:"dep_suggestion_count"`
	TotalPending        int `json:"total_pending"`
}

// InboxPlacementDTO 收件箱展示的单条权威环境物理放置详情
type InboxPlacementDTO struct {
	PlacementID       uint64   `json:"placement_id"`
	ArchProcessID     uint64   `json:"arch_process_id"`
	ProcessID         uint64   `json:"process_id"`
	AgentID           uint64   `json:"agent_id"`
	ArchProcessCode   string   `json:"arch_process_code"`
	ArchProcessName   string   `json:"arch_process_name"`
	AgentName         string   `json:"agent_name"`
	Endpoints         []string `json:"endpoints"`
	ObservedTransport string   `json:"observed_transport"`
	InstanceCount     int      `json:"instance_count"`
}

// InboxServiceItemDTO 收件箱进程分组下的待处理服务条目
type InboxServiceItemDTO struct {
	ServiceID               uint64              `json:"service_id"`
	ServiceName             string              `json:"service_name"`
	ProtoServiceName        string              `json:"proto_service_name"`
	ProtoSuggestedName      string              `json:"proto_suggested_name"`
	Kind                    string              `json:"kind"`
	ModuleID                *uint64             `json:"module_id,omitempty"`
	ModuleName              string              `json:"module_name,omitempty"`
	Placements              []InboxPlacementDTO `json:"placements"`
	CandidateArchProcessIDs []uint64            `json:"candidate_arch_process_ids"`
}

// InboxProcessGroupDTO 按 CONFIRMED 标准物理进程分组的收件箱聚合视图
type InboxProcessGroupDTO struct {
	ArchProcessID   uint64                `json:"arch_process_id"`
	ArchProcessCode string                `json:"arch_process_code"`
	ArchProcessName string                `json:"arch_process_name"`
	GroupName       string                `json:"group_name"`
	Items           []InboxServiceItemDTO `json:"items"`
}

// ArchInboxResponseDTO GET /api/arch/inbox 响应结构
type ArchInboxResponseDTO struct {
	Summary        InboxCountsDTO         `json:"summary"`
	ProcessGroups  []InboxProcessGroupDTO `json:"process_groups"`
	DepSuggestions []DepSuggestionDTO     `json:"dep_suggestions"`
	Total          int                    `json:"total"`
	Page           int                    `json:"page"`
	PageSize       int                    `json:"page_size"`
}

// BuildArchInbox 聚合权威 SHARED 环境 + CONFIRMED 进程的未认领、多宿主、放置漂移与反向依赖建议
func BuildArchInbox(ctx context.Context, db *gorm.DB, q InboxQuery) (*ArchInboxResponseDTO, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize <= 0 {
		q.PageSize = 25
	}

	var allSvcs []models.ModuleService
	if err := db.WithContext(ctx).
		Preload("Module").
		Order("id ASC").
		Find(&allSvcs).Error; err != nil {
		return nil, err
	}

	// 仅查询权威 SHARED + CONFIRMED + ACTIVE 放置，绝不泄露 PRIVATE / PROVISIONAL / STALE / RETIRED
	var placements []models.ServicePlacement
	if err := db.WithContext(ctx).
		Preload("Agent").
		Preload("ArchProcess.Group").
		Where("status = ?", models.PlacementStatusActive).
		Order("id ASC").
		Find(&placements).Error; err != nil {
		return nil, err
	}

	plsBySvc := make(map[uint64][]models.ServicePlacement)
	archMeta := make(map[uint64]*models.ArchProcess)
	for _, pl := range placements {
		if pl.Agent == nil || pl.Agent.Scope != models.ScopeShared {
			continue
		}
		if pl.ArchProcess == nil || pl.ArchProcess.Lifecycle != models.ArchProcLifecycleConfirmed {
			continue
		}
		plsBySvc[pl.ModuleServiceID] = append(plsBySvc[pl.ModuleServiceID], pl)
		archMeta[pl.ArchProcessID] = pl.ArchProcess
	}

	depSugs, err := SynthesizeDependencySuggestions(ctx, db)
	if err != nil {
		return nil, err
	}

	var summary InboxCountsDTO
	summary.DepSuggestionCount = len(depSugs)

	kindFilter := strings.ToUpper(strings.TrimSpace(q.Kind))
	searchFilter := strings.ToLower(strings.TrimSpace(q.Search))

	groupMap := make(map[uint64]*InboxProcessGroupDTO)
	ensureGroup := func(ap *models.ArchProcess) *InboxProcessGroupDTO {
		if g, ok := groupMap[ap.ID]; ok {
			return g
		}
		grpName := ""
		if ap.Group != nil {
			grpName = ap.Group.Name
		}
		g := &InboxProcessGroupDTO{
			ArchProcessID:   ap.ID,
			ArchProcessCode: ap.Code,
			ArchProcessName: ap.Name,
			GroupName:       grpName,
			Items:           make([]InboxServiceItemDTO, 0),
		}
		groupMap[ap.ID] = g
		return g
	}

	for _, svc := range allSvcs {
		svcPls := plsBySvc[svc.ID]
		if len(svcPls) == 0 {
			continue
		}

		// 收集唯一权威 ArchProcess IDs 与 DTO 列表
		seenAP := make(map[uint64]struct{})
		candidateAPIDs := make([]uint64, 0, len(svcPls))
		plDTOs := make([]InboxPlacementDTO, 0, len(svcPls))
		for _, pl := range svcPls {
			if _, ok := seenAP[pl.ArchProcessID]; !ok {
				seenAP[pl.ArchProcessID] = struct{}{}
				candidateAPIDs = append(candidateAPIDs, pl.ArchProcessID)
			}
			var eps []string
			if len(pl.Endpoints) > 0 {
				_ = json.Unmarshal(pl.Endpoints, &eps)
			}
			if eps == nil {
				eps = []string{}
			}
			plDTOs = append(plDTOs, InboxPlacementDTO{
				PlacementID:       pl.ID,
				ArchProcessID:     pl.ArchProcessID,
				ProcessID:         pl.ProcessID,
				AgentID:           pl.AgentID,
				ArchProcessCode:   pl.ArchProcess.Code,
				ArchProcessName:   pl.ArchProcess.Name,
				AgentName:         pl.Agent.Name,
				Endpoints:         eps,
				ObservedTransport: pl.ObservedTransport,
				InstanceCount:     pl.InstanceCount,
			})
		}
		sort.Slice(candidateAPIDs, func(i, j int) bool { return candidateAPIDs[i] < candidateAPIDs[j] })

		var itemKind string
		var targetGroupAPs []uint64
		if !svc.IsClaimed() {
			if len(candidateAPIDs) == 1 {
				itemKind = InboxKindUnclaimed
				summary.UnclaimedCount++
				targetGroupAPs = candidateAPIDs
			} else {
				itemKind = InboxKindMultiHost
				summary.MultiHostCount++
				targetGroupAPs = candidateAPIDs
			}
		} else if svc.Module != nil && svc.Module.ArchProcessID != nil {
			expectedAP := *svc.Module.ArchProcessID
			for _, apID := range candidateAPIDs {
				if apID != expectedAP {
					targetGroupAPs = append(targetGroupAPs, apID)
				}
			}
			if len(targetGroupAPs) > 0 {
				itemKind = InboxKindPlacementDrift
				summary.PlacementDriftCount++
			}
		}

		if itemKind == "" {
			continue
		}
		if kindFilter != "" && kindFilter != "ALL" && kindFilter != itemKind {
			continue
		}

		modName := ""
		if svc.Module != nil {
			modName = svc.Module.Name
		}

		item := InboxServiceItemDTO{
			ServiceID:               svc.ID,
			ServiceName:             svc.ServiceName,
			ProtoServiceName:        svc.ProtoServiceName,
			ProtoSuggestedName:      svc.ProtoSuggestedName,
			Kind:                    itemKind,
			ModuleID:                svc.ModuleID,
			ModuleName:              modName,
			Placements:              plDTOs,
			CandidateArchProcessIDs: candidateAPIDs,
		}

		for _, apID := range targetGroupAPs {
			if q.ArchProcessID > 0 && apID != q.ArchProcessID {
				continue
			}
			ap := archMeta[apID]
			if ap == nil {
				continue
			}
			if searchFilter != "" && !matchesInboxSearch(searchFilter, &item, ap) {
				continue
			}
			grp := ensureGroup(ap)
			grp.Items = append(grp.Items, item)
		}
	}

	summary.TotalPending = summary.UnclaimedCount + summary.MultiHostCount + summary.PlacementDriftCount + summary.DepSuggestionCount

	groups := make([]InboxProcessGroupDTO, 0, len(groupMap))
	for _, g := range groupMap {
		if len(g.Items) == 0 {
			continue
		}
		sort.Slice(g.Items, func(i, j int) bool { return g.Items[i].ServiceID < g.Items[j].ServiceID })
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ArchProcessID < groups[j].ArchProcessID })

	// 过滤反向依赖建议
	filteredDeps := make([]DepSuggestionDTO, 0, len(depSugs))
	if kindFilter == "" || kindFilter == "ALL" || kindFilter == InboxKindDepSuggestion {
		for _, d := range depSugs {
			if q.ArchProcessID > 0 && d.ConsumerArchProcessID != q.ArchProcessID {
				continue
			}
			if searchFilter != "" && !matchesDepSearch(searchFilter, &d) {
				continue
			}
			filteredDeps = append(filteredDeps, d)
		}
	}

	totalGroups := len(groups)
	start := (q.Page - 1) * q.PageSize
	var pagedGroups []InboxProcessGroupDTO
	if start >= totalGroups {
		pagedGroups = []InboxProcessGroupDTO{}
	} else {
		end := start + q.PageSize
		if end > totalGroups {
			end = totalGroups
		}
		pagedGroups = groups[start:end]
	}

	return &ArchInboxResponseDTO{
		Summary:        summary,
		ProcessGroups:  pagedGroups,
		DepSuggestions: filteredDeps,
		Total:          totalGroups,
		Page:           q.Page,
		PageSize:       q.PageSize,
	}, nil
}

func matchesInboxSearch(needle string, item *InboxServiceItemDTO, ap *models.ArchProcess) bool {
	fields := []string{
		item.ServiceName,
		item.ProtoServiceName,
		item.ProtoSuggestedName,
		item.ModuleName,
		ap.Code,
		ap.Name,
	}
	for _, f := range fields {
		if f != "" && strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}

func matchesDepSearch(needle string, d *DepSuggestionDTO) bool {
	fields := []string{
		d.ServiceName,
		d.ProviderModuleName,
		d.ConsumerArchProcessCode,
		d.ConsumerArchProcessName,
	}
	for _, f := range fields {
		if f != "" && strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	for _, m := range d.CandidateConsumerModules {
		if strings.Contains(strings.ToLower(m.ModuleCode), needle) || strings.Contains(strings.ToLower(m.ModuleName), needle) {
			return true
		}
	}
	return false
}
