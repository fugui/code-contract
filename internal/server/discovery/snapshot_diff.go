package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"code-contract/internal/server/topology"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Diff category constants (M30a, ADR-24 §5.7).
const (
	DiffKindAddedProcess        = "ADDED_PROCESS"
	DiffKindRemovedProcess      = "REMOVED_PROCESS"
	DiffKindAddedService        = "ADDED_SERVICE"
	DiffKindRemovedService      = "REMOVED_SERVICE"
	DiffKindTransportChanged    = "TRANSPORT_CHANGED"
	DiffKindAddedSubscription   = "ADDED_SUBSCRIPTION"
	DiffKindRemovedSubscription = "REMOVED_SUBSCRIPTION"
	DiffKindProviderKindChanged = "PROVIDER_KIND_CHANGED"
)

const (
	DefaultRetentionCount = 50
	DefaultPinLimit       = 20
)

// DiscoverySnapshotSummaryDTO is returned in paginated snapshot timeline listings (omits large payload_json).
type DiscoverySnapshotSummaryDTO struct {
	ID               uint64              `json:"id"`
	AgentID          uint64              `json:"agent_id"`
	AgentName        string              `json:"agent_name"`
	TriggerMode      string              `json:"trigger_mode"`
	CSGAddr          string              `json:"csg_addr"`
	DiscoveryMode    string              `json:"discovery_mode"`
	Label            string              `json:"label"`
	IsPinned         bool                `json:"is_pinned"`
	ContentHash      string              `json:"content_hash"`
	RedactionVersion int                 `json:"redaction_version"`
	Summary          DiscoverySummaryDTO `json:"summary"`
	CreatedBy        uint                `json:"created_by"`
	CapturedAt       string              `json:"captured_at"`
	CreatedAt        string              `json:"created_at"`
}

// DiscoverySnapshotDTO is returned on create/update of a snapshot.
type DiscoverySnapshotDTO struct {
	DiscoverySnapshotSummaryDTO
	Deduplicated bool `json:"deduplicated,omitempty"`
}

// SaveSnapshotInput carries input for persisting a discovery snapshot.
type SaveSnapshotInput struct {
	AgentID     uint64
	CSGAddr     string
	TriggerMode string
	Label       string
	IsPinned    bool
	CreatedBy   uint
	IsAdmin     bool
	Live        *DiscoveryLiveRespDTO
}

// SaveSnapshotResult reports the saved (or deduplicated existing) snapshot.
type SaveSnapshotResult struct {
	Snapshot     *DiscoverySnapshotDTO
	Deduplicated bool
}

// ListSnapshotsQuery specifies filters and pagination for GET /api/discovery/snapshots.
type ListSnapshotsQuery struct {
	AgentID    uint64
	CSGAddr    string
	PinnedOnly bool
	Page       int
	PageSize   int
}

// UpdateSnapshotInput specifies mutable fields for PATCH /api/discovery/snapshots/:id.
type UpdateSnapshotInput struct {
	Label    *string
	IsPinned *bool
}

// DiscoveryDiffItemDTO describes a single normalized structural change between two snapshots.
type DiscoveryDiffItemDTO struct {
	Kind              string `json:"kind"`
	ProcessKey        string `json:"process_key"`
	ProcessName       string `json:"process_name"`
	ServiceName       string `json:"service_name,omitempty"`
	LeftTransport     string `json:"left_transport,omitempty"`
	RightTransport    string `json:"right_transport,omitempty"`
	LeftProviderKind  string `json:"left_provider_kind,omitempty"`
	RightProviderKind string `json:"right_provider_kind,omitempty"`
	Detail            string `json:"detail"`
}

// DiscoveryDiffSummaryDTO aggregates counts across all 8 diff categories.
type DiscoveryDiffSummaryDTO struct {
	Total               int `json:"total"`
	AddedProcess        int `json:"added_process"`
	RemovedProcess      int `json:"removed_process"`
	AddedService        int `json:"added_service"`
	RemovedService      int `json:"removed_service"`
	TransportChanged    int `json:"transport_changed"`
	AddedSubscription   int `json:"added_subscription"`
	RemovedSubscription int `json:"removed_subscription"`
	ProviderKindChanged int `json:"provider_kind_changed"`
}

// DiscoverySnapshotDiffDTO is returned by GET /api/discovery/snapshots/diff.
type DiscoverySnapshotDiffDTO struct {
	Mode            string                  `json:"mode"` // TIMELINE | CROSS_ENV
	LeftLabel       string                  `json:"left_label"`
	RightLabel      string                  `json:"right_label"`
	LeftCapturedAt  string                  `json:"left_captured_at,omitempty"`
	RightCapturedAt string                  `json:"right_captured_at,omitempty"`
	Summary         DiscoveryDiffSummaryDTO `json:"summary"`
	Items           []DiscoveryDiffItemDTO  `json:"items"`
}

// ComputeSnapshotContentHash computes a deterministic SHA-256 over normalized processes, services,
// transports, and subscriptions, explicitly ignoring PIDs, dynamic IP:Port endpoints, and timestamps.
func ComputeSnapshotContentHash(resp *DiscoveryLiveRespDTO) string {
	if resp == nil {
		return ""
	}
	procLines := make([]string, 0, len(resp.Processes)+len(resp.RemoteLDCProcesses))
	appendGroups := func(scope string, list []DiscoveryProcessGroupDTO) {
		for _, g := range list {
			key := normalizedDiffProcessKey(g)
			svcParts := make([]string, 0, len(g.Services))
			for _, s := range g.Services {
				svcParts = append(svcParts, topology.StripCSGPIDSuffix(s.ServiceName)+"@"+normalizeTransportLabel(s.Transport))
			}
			sort.Strings(svcParts)

			subParts := make([]string, 0, len(g.Subscriptions))
			for _, sub := range g.Subscriptions {
				subParts = append(subParts, topology.StripCSGPIDSuffix(sub.ServiceName)+"="+strings.TrimSpace(sub.ProviderKind))
			}
			sort.Strings(subParts)

			procLines = append(procLines, fmt.Sprintf("%s|%s|svcs:[%s]|subs:[%s]",
				scope, key, strings.Join(svcParts, ","), strings.Join(subParts, ",")))
		}
	}
	appendGroups("LOCAL", resp.Processes)
	appendGroups("REMOTE", resp.RemoteLDCProcesses)
	sort.Strings(procLines)

	sum := sha256.Sum256([]byte(strings.Join(procLines, "\n")))
	return hex.EncodeToString(sum[:])
}

func cloneSlice[T any](src []T) []T {
	dst := make([]T, len(src))
	copy(dst, src)
	return dst
}

func normalizeProcessGroupSlices(groups []DiscoveryProcessGroupDTO) {
	for i := range groups {
		g := &groups[i]
		if g.PIDs == nil {
			g.PIDs = []uint32{}
		}
		if g.ListenPorts == nil {
			g.ListenPorts = []uint16{}
		}
		if g.ObservedTransports == nil {
			g.ObservedTransports = []string{}
		}
		if g.Services == nil {
			g.Services = []DiscoveryLiveServiceDTO{}
		}
		for j := range g.Services {
			svc := &g.Services[j]
			if svc.Endpoints == nil {
				svc.Endpoints = []string{}
			}
			if svc.ServerPIDs == nil {
				svc.ServerPIDs = []uint32{}
			}
		}
		if g.Subscriptions == nil {
			g.Subscriptions = []DiscoveryLiveSubDTO{}
		}
	}
}

// normalizeLiveRespSlices ensures all slices inside DiscoveryLiveRespDTO are non-nil so JSON encoding emits [] instead of null.
func normalizeLiveRespSlices(dto *DiscoveryLiveRespDTO) {
	if dto == nil {
		return
	}
	if dto.CSGEndpoints == nil {
		dto.CSGEndpoints = []agentproto.CSGEndpointInfo{}
	}
	if dto.Ports == nil {
		dto.Ports = []agentproto.CSGPortUsage{}
	}
	if dto.Processes == nil {
		dto.Processes = []DiscoveryProcessGroupDTO{}
	}
	if dto.RemoteLDCProcesses == nil {
		dto.RemoteLDCProcesses = []DiscoveryProcessGroupDTO{}
	}
	normalizeProcessGroupSlices(dto.Processes)
	normalizeProcessGroupSlices(dto.RemoteLDCProcesses)
}

func cloneProcessGroupList(src []DiscoveryProcessGroupDTO) []DiscoveryProcessGroupDTO {
	out := make([]DiscoveryProcessGroupDTO, len(src))
	for i, p := range src {
		p.Cmdline = RedactCmdline(p.Cmdline)
		p.PIDs = cloneSlice(p.PIDs)
		p.ListenPorts = cloneSlice(p.ListenPorts)
		p.ObservedTransports = cloneSlice(p.ObservedTransports)
		p.Candidates = cloneSlice(p.Candidates)
		svcs := make([]DiscoveryLiveServiceDTO, len(p.Services))
		for j, s := range p.Services {
			s.Endpoints = cloneSlice(s.Endpoints)
			s.ServerPIDs = cloneSlice(s.ServerPIDs)
			svcs[j] = s
		}
		p.Services = svcs
		p.Subscriptions = cloneSlice(p.Subscriptions)
		out[i] = p
	}
	return out
}

// RedactLiveResp returns a deep copy of resp with sensitive cmdline parameters masked.
func RedactLiveResp(resp *DiscoveryLiveRespDTO) *DiscoveryLiveRespDTO {
	if resp == nil {
		return nil
	}
	cp := *resp
	cp.CSGEndpoints = cloneSlice(resp.CSGEndpoints)
	for i := range cp.CSGEndpoints {
		cp.CSGEndpoints[i].Cmdline = RedactCmdline(cp.CSGEndpoints[i].Cmdline)
	}
	cp.Ports = cloneSlice(resp.Ports)
	cp.Errors = cloneSlice(resp.Errors)
	cp.Processes = cloneProcessGroupList(resp.Processes)
	cp.RemoteLDCProcesses = cloneProcessGroupList(resp.RemoteLDCProcesses)
	normalizeLiveRespSlices(&cp)
	return &cp
}

// SaveSnapshot redacts live discovery payload, enforces AUTO/SCHEDULED dedup & pin limit,
// persists row in contract_discovery_snapshots, and prunes unpinned rows via FIFO retention.
func SaveSnapshot(db *gorm.DB, in SaveSnapshotInput) (*SaveSnapshotResult, error) {
	if db == nil {
		return nil, errors.New("discovery: db is nil")
	}
	if in.Live == nil {
		return nil, errors.New("discovery: live payload is nil")
	}

	triggerMode := strings.ToUpper(strings.TrimSpace(in.TriggerMode))
	if triggerMode == "" {
		triggerMode = models.DiscoveryTriggerManual
	}

	cleanLive := RedactLiveResp(in.Live)
	csgAddr := strings.TrimSpace(in.CSGAddr)
	if csgAddr == "" {
		csgAddr = strings.TrimSpace(cleanLive.CSGAddr)
	}
	cleanLive.CSGAddr = csgAddr
	cleanLive.Stale = false
	cleanLive.SnapshotID = nil

	contentHash := ComputeSnapshotContentHash(cleanLive)
	capturedAt := time.Now().UTC()
	if cleanLive.CapturedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, cleanLive.CapturedAt); err == nil {
			capturedAt = parsed
		}
	} else {
		cleanLive.CapturedAt = capturedAt.Format(time.RFC3339)
	}

	retentionLimit := readIntSetting(db, "discovery.snapshot_retention_count", DefaultRetentionCount)
	pinLimit := readIntSetting(db, "discovery.snapshot_pin_limit", DefaultPinLimit)

	var result *SaveSnapshotResult
	err := db.Transaction(func(tx *gorm.DB) error {
		var agent models.Agent
		if err := tx.First(&agent, in.AgentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAgentNotFound
			}
			return err
		}
		if agent.Scope == models.ScopePrivate && !in.IsAdmin && in.CreatedBy != 0 && (agent.OwnerID == nil || *agent.OwnerID != in.CreatedBy) {
			return ErrAgentForbidden
		}

		// AUTO and SCHEDULED snapshots skip insertion if latest content_hash matches.
		if triggerMode == models.DiscoveryTriggerAuto || triggerMode == models.DiscoveryTriggerScheduled {
			var latest models.DiscoverySnapshot
			err := tx.Where("agent_id = ? AND csg_addr = ?", in.AgentID, csgAddr).
				Order("captured_at DESC, id DESC").
				First(&latest).Error
			if err == nil && latest.ContentHash == contentHash {
				dto := snapshotModelToDTO(&latest, true)
				result = &SaveSnapshotResult{Snapshot: &dto, Deduplicated: true}
				return nil
			}
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}

		if in.IsPinned {
			var pinnedCount int64
			if err := tx.Model(&models.DiscoverySnapshot{}).
				Where("agent_id = ? AND csg_addr = ? AND is_pinned = true", in.AgentID, csgAddr).
				Count(&pinnedCount).Error; err != nil {
				return err
			}
			if int(pinnedCount) >= pinLimit {
				return ErrPinLimitExceeded
			}
		}

		cleanLive.AgentID = agent.ID
		cleanLive.AgentName = agent.Name
		cleanLive.AgentScope = agent.Scope

		summaryBytes, err := json.Marshal(cleanLive.Summary)
		if err != nil {
			return err
		}
		payloadBytes, err := json.Marshal(cleanLive)
		if err != nil {
			return err
		}

		snap := models.DiscoverySnapshot{
			AgentID:          agent.ID,
			AgentName:        agent.Name,
			TriggerMode:      triggerMode,
			CSGAddr:          csgAddr,
			DiscoveryMode:    firstNonEmpty(cleanLive.DiscoveryMode, "PROC_ONLY"),
			Label:            strings.TrimSpace(in.Label),
			IsPinned:         in.IsPinned,
			RedactionVersion: RedactionVersion,
			SummaryJSON:      datatypes.JSON(summaryBytes),
			PayloadJSON:      datatypes.JSON(payloadBytes),
			ContentHash:      contentHash,
			CreatedBy:        in.CreatedBy,
			CapturedAt:       capturedAt,
		}
		if err := tx.Create(&snap).Error; err != nil {
			return err
		}

		// FIFO prune unpinned snapshots exceeding retentionLimit
		if !snap.IsPinned && retentionLimit > 0 {
			var unpinnedIDs []uint64
			if err := tx.Model(&models.DiscoverySnapshot{}).
				Where("agent_id = ? AND csg_addr = ? AND is_pinned = false", in.AgentID, csgAddr).
				Order("captured_at DESC, id DESC").
				Pluck("id", &unpinnedIDs).Error; err != nil {
				return err
			}
			if len(unpinnedIDs) > retentionLimit {
				toDelete := unpinnedIDs[retentionLimit:]
				if err := tx.Where("id IN ?", toDelete).Delete(&models.DiscoverySnapshot{}).Error; err != nil {
					return err
				}
			}
		}

		dto := snapshotModelToDTO(&snap, false)
		result = &SaveSnapshotResult{Snapshot: &dto, Deduplicated: false}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ListSnapshots lists snapshot timeline metadata (excluding payload_json) with ScopeReadGuard.
func ListSnapshots(db *gorm.DB, query ListSnapshotsQuery, userID uint, isAdmin bool) ([]DiscoverySnapshotSummaryDTO, int64, error) {
	if _, err := loadAndGuardAgent(db, query.AgentID, userID, isAdmin); err != nil {
		return nil, 0, err
	}

	q := db.Model(&models.DiscoverySnapshot{}).Where("agent_id = ?", query.AgentID)
	if addr := strings.TrimSpace(query.CSGAddr); addr != "" {
		q = q.Where("csg_addr = ?", addr)
	}
	if query.PinnedOnly {
		q = q.Where("is_pinned = true")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page := query.Page
	if page < 1 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 25
	}
	if pageSize > 100 {
		pageSize = 100
	}

	var rows []models.DiscoverySnapshot
	if err := q.Select("id, agent_id, agent_name, trigger_mode, csg_addr, discovery_mode, label, is_pinned, redaction_version, summary_json, content_hash, created_by, captured_at, created_at").
		Order("is_pinned DESC, captured_at DESC, id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}

	items := make([]DiscoverySnapshotSummaryDTO, 0, len(rows))
	for i := range rows {
		items = append(items, snapshotModelToSummaryDTO(&rows[i]))
	}
	return items, total, nil
}

// GetSnapshotPayload fetches a single snapshot and decodes its redacted DiscoveryLiveRespDTO under ScopeReadGuard.
func GetSnapshotPayload(db *gorm.DB, id uint64, userID uint, isAdmin bool) (*models.DiscoverySnapshot, *DiscoveryLiveRespDTO, error) {
	if db == nil {
		return nil, nil, ErrSnapshotNotFound
	}
	var snap models.DiscoverySnapshot
	if err := db.First(&snap, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrSnapshotNotFound
		}
		return nil, nil, err
	}
	if _, err := loadAndGuardAgent(db, snap.AgentID, userID, isAdmin); err != nil {
		return nil, nil, err
	}
	var payload DiscoveryLiveRespDTO
	if err := json.Unmarshal(snap.PayloadJSON, &payload); err != nil {
		return nil, nil, err
	}
	normalizeLiveRespSlices(&payload)
	return &snap, &payload, nil
}

// UpdateSnapshot updates label and/or is_pinned under ScopeReadGuard + creator/admin check on SHARED.
func UpdateSnapshot(db *gorm.DB, id uint64, in UpdateSnapshotInput, userID uint, isAdmin bool) (*DiscoverySnapshotDTO, error) {
	if db == nil {
		return nil, ErrSnapshotNotFound
	}
	pinLimit := readIntSetting(db, "discovery.snapshot_pin_limit", DefaultPinLimit)

	var updated models.DiscoverySnapshot
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&updated, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSnapshotNotFound
			}
			return err
		}
		agent, err := loadAndGuardAgent(tx, updated.AgentID, userID, isAdmin)
		if err != nil {
			return err
		}
		if agent.Scope == models.ScopeShared && !isAdmin && updated.CreatedBy != userID {
			return ErrAgentForbidden
		}

		updates := make(map[string]interface{})
		if in.Label != nil {
			lbl := strings.TrimSpace(*in.Label)
			updates["label"] = lbl
			updated.Label = lbl
		}
		if in.IsPinned != nil && *in.IsPinned != updated.IsPinned {
			if *in.IsPinned {
				var pinnedCount int64
				if err := tx.Model(&models.DiscoverySnapshot{}).
					Where("agent_id = ? AND csg_addr = ? AND is_pinned = true", updated.AgentID, updated.CSGAddr).
					Count(&pinnedCount).Error; err != nil {
					return err
				}
				if int(pinnedCount) >= pinLimit {
					return ErrPinLimitExceeded
				}
			}
			updates["is_pinned"] = *in.IsPinned
			updated.IsPinned = *in.IsPinned
		}

		if len(updates) > 0 {
			if err := tx.Model(&models.DiscoverySnapshot{}).Where("id = ?", updated.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	dto := snapshotModelToDTO(&updated, false)
	return &dto, nil
}

// DeleteSnapshot deletes an unpinned snapshot under ScopeReadGuard + creator/admin check on SHARED.
func DeleteSnapshot(db *gorm.DB, id uint64, userID uint, isAdmin bool) error {
	if db == nil {
		return ErrSnapshotNotFound
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var snap models.DiscoverySnapshot
		if err := tx.First(&snap, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSnapshotNotFound
			}
			return err
		}
		agent, err := loadAndGuardAgent(tx, snap.AgentID, userID, isAdmin)
		if err != nil {
			return err
		}
		if agent.Scope == models.ScopeShared && !isAdmin && snap.CreatedBy != userID {
			return ErrAgentForbidden
		}
		if snap.IsPinned {
			return ErrSnapshotPinned
		}
		return tx.Delete(&snap).Error
	})
}

// DiffSnapshots compares two DiscoveryLiveRespDTO instances (TIMELINE or CROSS_ENV),
// ignoring PIDs, host IPs, and dynamic port values across all 8 diff categories.
func DiffSnapshots(left, right *DiscoveryLiveRespDTO, mode, leftLabel, rightLabel string) *DiscoverySnapshotDiffDTO {
	if left == nil {
		left = &DiscoveryLiveRespDTO{}
	}
	if right == nil {
		right = &DiscoveryLiveRespDTO{}
	}
	normMode := strings.ToUpper(strings.TrimSpace(mode))
	if normMode == "" {
		if left.AgentID != 0 && right.AgentID != 0 && left.AgentID != right.AgentID {
			normMode = "CROSS_ENV"
		} else {
			normMode = "TIMELINE"
		}
	}

	leftMap := buildDiffProcessMap(left.Processes)
	rightMap := buildDiffProcessMap(right.Processes)

	var items []DiscoveryDiffItemDTO
	var summary DiscoveryDiffSummaryDTO

	allProcKeys := make(map[string]struct{}, len(leftMap)+len(rightMap))
	for k := range leftMap {
		allProcKeys[k] = struct{}{}
	}
	for k := range rightMap {
		allProcKeys[k] = struct{}{}
	}
	sortedKeys := sortedStringSet(allProcKeys)

	for _, pk := range sortedKeys {
		lg, hasLeft := leftMap[pk]
		rg, hasRight := rightMap[pk]

		if !hasLeft && hasRight {
			items = append(items, DiscoveryDiffItemDTO{
				Kind:        DiffKindAddedProcess,
				ProcessKey:  pk,
				ProcessName: rg.procName,
				Detail:      fmt.Sprintf("新增进程 %s（服务 %d / 订阅 %d）", rg.procName, len(rg.services), len(rg.subs)),
			})
			summary.AddedProcess++
			continue
		}
		if hasLeft && !hasRight {
			items = append(items, DiscoveryDiffItemDTO{
				Kind:        DiffKindRemovedProcess,
				ProcessKey:  pk,
				ProcessName: lg.procName,
				Detail:      fmt.Sprintf("移除进程 %s（原服务 %d / 订阅 %d）", lg.procName, len(lg.services), len(lg.subs)),
			})
			summary.RemovedProcess++
			continue
		}

		// Compare services for shared process
		svcKeys := unionStringKeys(lg.services, rg.services)
		for _, svcName := range svcKeys {
			ls, okL := lg.services[svcName]
			rs, okR := rg.services[svcName]
			switch {
			case !okL && okR:
				items = append(items, DiscoveryDiffItemDTO{
					Kind:           DiffKindAddedService,
					ProcessKey:     pk,
					ProcessName:    rg.procName,
					ServiceName:    svcName,
					RightTransport: rs,
					Detail:         fmt.Sprintf("进程 %s 新增发布服务 %s (%s)", rg.procName, svcName, rs),
				})
				summary.AddedService++
			case okL && !okR:
				items = append(items, DiscoveryDiffItemDTO{
					Kind:          DiffKindRemovedService,
					ProcessKey:    pk,
					ProcessName:   lg.procName,
					ServiceName:   svcName,
					LeftTransport: ls,
					Detail:        fmt.Sprintf("进程 %s 下线服务 %s (%s)", lg.procName, svcName, ls),
				})
				summary.RemovedService++
			case ls != rs:
				items = append(items, DiscoveryDiffItemDTO{
					Kind:           DiffKindTransportChanged,
					ProcessKey:     pk,
					ProcessName:    rg.procName,
					ServiceName:    svcName,
					LeftTransport:  ls,
					RightTransport: rs,
					Detail:         fmt.Sprintf("服务 %s 传输制式变更: %s ➔ %s", svcName, ls, rs),
				})
				summary.TransportChanged++
			}
		}

		// Compare subscriptions for shared process
		subKeys := unionStringKeys(lg.subs, rg.subs)
		for _, svcName := range subKeys {
			lKind, okL := lg.subs[svcName]
			rKind, okR := rg.subs[svcName]
			switch {
			case !okL && okR:
				items = append(items, DiscoveryDiffItemDTO{
					Kind:              DiffKindAddedSubscription,
					ProcessKey:        pk,
					ProcessName:       rg.procName,
					ServiceName:       svcName,
					RightProviderKind: rKind,
					Detail:            fmt.Sprintf("进程 %s 新增订阅 %s (%s)", rg.procName, svcName, rKind),
				})
				summary.AddedSubscription++
			case okL && !okR:
				items = append(items, DiscoveryDiffItemDTO{
					Kind:             DiffKindRemovedSubscription,
					ProcessKey:       pk,
					ProcessName:      lg.procName,
					ServiceName:      svcName,
					LeftProviderKind: lKind,
					Detail:           fmt.Sprintf("进程 %s 移除订阅 %s (%s)", lg.procName, svcName, lKind),
				})
				summary.RemovedSubscription++
			case lKind != rKind:
				items = append(items, DiscoveryDiffItemDTO{
					Kind:              DiffKindProviderKindChanged,
					ProcessKey:        pk,
					ProcessName:       rg.procName,
					ServiceName:       svcName,
					LeftProviderKind:  lKind,
					RightProviderKind: rKind,
					Detail:            fmt.Sprintf("订阅 %s 提供方类型变更: %s ➔ %s", svcName, lKind, rKind),
				})
				summary.ProviderKindChanged++
			}
		}
	}

	if items == nil {
		items = []DiscoveryDiffItemDTO{}
	}
	summary.Total = len(items)

	return &DiscoverySnapshotDiffDTO{
		Mode:            normMode,
		LeftLabel:       leftLabel,
		RightLabel:      rightLabel,
		LeftCapturedAt:  left.CapturedAt,
		RightCapturedAt: right.CapturedAt,
		Summary:         summary,
		Items:           items,
	}
}

type diffProcessEntry struct {
	procName string
	services map[string]string
	subs     map[string]string
}

func buildDiffProcessMap(groups []DiscoveryProcessGroupDTO) map[string]*diffProcessEntry {
	out := make(map[string]*diffProcessEntry, len(groups))
	for _, g := range groups {
		pk := normalizedDiffProcessKey(g)
		entry, ok := out[pk]
		if !ok {
			entry = &diffProcessEntry{
				procName: firstNonEmpty(g.ProcName, pk),
				services: make(map[string]string, len(g.Services)),
				subs:     make(map[string]string, len(g.Subscriptions)),
			}
			out[pk] = entry
		}
		for _, s := range g.Services {
			normSvc := topology.StripCSGPIDSuffix(s.ServiceName)
			tr := normalizeTransportLabel(s.Transport)
			if existing, exists := entry.services[normSvc]; exists && existing != tr {
				entry.services[normSvc] = "MIXED"
			} else {
				entry.services[normSvc] = tr
			}
		}
		for _, sub := range g.Subscriptions {
			normSvc := topology.StripCSGPIDSuffix(sub.ServiceName)
			entry.subs[normSvc] = strings.TrimSpace(sub.ProviderKind)
		}
	}
	return out
}

func normalizedDiffProcessKey(g DiscoveryProcessGroupDTO) string {
	if strings.TrimSpace(g.ProcessKey) != "" {
		return strings.TrimSpace(g.ProcessKey)
	}
	if strings.TrimSpace(g.IdentityFingerprint) != "" {
		return "fp:" + g.IdentityFingerprint
	}
	return "proc:" + strings.ToLower(StripProcPIDSuffix(firstNonEmpty(g.ExeName, g.ProcName, "unknown")))
}

func unionStringKeys(a, b map[string]string) []string {
	m := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		m[k] = struct{}{}
	}
	for k := range b {
		m[k] = struct{}{}
	}
	return sortedStringSet(m)
}

func snapshotModelToSummaryDTO(s *models.DiscoverySnapshot) DiscoverySnapshotSummaryDTO {
	var summary DiscoverySummaryDTO
	if len(s.SummaryJSON) > 0 {
		_ = json.Unmarshal(s.SummaryJSON, &summary)
	}
	return DiscoverySnapshotSummaryDTO{
		ID:               s.ID,
		AgentID:          s.AgentID,
		AgentName:        s.AgentName,
		TriggerMode:      s.TriggerMode,
		CSGAddr:          s.CSGAddr,
		DiscoveryMode:    s.DiscoveryMode,
		Label:            s.Label,
		IsPinned:         s.IsPinned,
		ContentHash:      s.ContentHash,
		RedactionVersion: s.RedactionVersion,
		Summary:          summary,
		CreatedBy:        s.CreatedBy,
		CapturedAt:       s.CapturedAt.UTC().Format(time.RFC3339),
		CreatedAt:        s.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func snapshotModelToDTO(s *models.DiscoverySnapshot, deduplicated bool) DiscoverySnapshotDTO {
	return DiscoverySnapshotDTO{
		DiscoverySnapshotSummaryDTO: snapshotModelToSummaryDTO(s),
		Deduplicated:                deduplicated,
	}
}

func readIntSetting(db *gorm.DB, key string, fallback int) int {
	if db == nil {
		return fallback
	}
	var setting models.ModuleSetting
	if err := db.Where("setting_key = ?", key).First(&setting).Error; err != nil {
		return fallback
	}
	val, err := strconv.Atoi(strings.TrimSpace(setting.SettingVal))
	if err != nil || val <= 0 {
		return fallback
	}
	return val
}
