package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"code-contract/internal/server/hub"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

const (
	DefaultLiveCacheTTL     = 5 * time.Second
	DefaultRefreshRateLimit = 3 * time.Second
)

var (
	ErrAgentNotFound      = hub.ErrAgentNotFound
	ErrAgentForbidden     = hub.ErrAgentForbidden
	ErrAgentUnavailable   = errors.New("discovery: agent unavailable and no fallback snapshot")
	ErrRefreshRateLimited = errors.New("discovery: refresh rate limited")
	ErrSnapshotNotFound   = errors.New("discovery: snapshot not found")
	ErrPinLimitExceeded   = errors.New("discovery: snapshot pin limit exceeded")
	ErrSnapshotPinned     = errors.New("discovery: cannot delete pinned snapshot")
	ErrInvalidDiffTarget  = errors.New("discovery: invalid diff target")
)

// AgentDiscoveryFetcher abstracts Agent live discovery RPCs for production HubManager and unit tests.
type AgentDiscoveryFetcher interface {
	FetchAgentCSGTopology(ctx context.Context, agentID uint64, csgAddr string, currentUserID uint, isAdmin bool) (*agentproto.CSGTopologyReport, error)
	FetchAgentDiscoveredProcesses(ctx context.Context, agentID uint64, currentUserID uint, isAdmin bool) ([]agentproto.DiscoveredProcess, error)
}

// DiscoveryLiveRespDTO is returned by GET /api/discovery/live and persisted inside snapshot payload_json.
type DiscoveryLiveRespDTO struct {
	AgentID            uint64                       `json:"agent_id"`
	AgentName          string                       `json:"agent_name"`
	AgentScope         string                       `json:"agent_scope"`
	CSGAddr            string                       `json:"csg_addr"`
	CSGEndpoints       []agentproto.CSGEndpointInfo `json:"csg_endpoints"`
	Stale              bool                         `json:"stale"`
	StaleReason        string                       `json:"stale_reason,omitempty"`
	CapturedAt         string                       `json:"captured_at"`
	DiscoveryMode      string                       `json:"discovery_mode"`
	Ports              []agentproto.CSGPortUsage    `json:"ports"`
	Processes          []DiscoveryProcessGroupDTO   `json:"processes"`
	RemoteLDCProcesses []DiscoveryProcessGroupDTO   `json:"remote_ldc_processes"`
	Summary            DiscoverySummaryDTO          `json:"summary"`
	Errors             []string                     `json:"errors,omitempty"`
	SnapshotID         *uint64                      `json:"snapshot_id,omitempty"`
}

// DiscoverySummaryDTO summarizes live or snapshot discovery counts.
type DiscoverySummaryDTO struct {
	ProcessCount       int `json:"process_count"`
	ClientOnlyCount    int `json:"client_only_count"`
	RemoteLDCCount     int `json:"remote_ldc_count"`
	TotalServices      int `json:"total_services"`
	TotalSubscriptions int `json:"total_subscriptions"`
	TCPCount           int `json:"tcp_count"`
	MeshCount          int `json:"mesh_count"`
	MatchedServices    int `json:"matched_services"`
	UndeclaredServices int `json:"undeclared_services"`
}

// DiscoveryProcessGroupDTO represents one process-centric row (with multi-PID folding).
type DiscoveryProcessGroupDTO struct {
	ProcessKey            string                    `json:"process_key"`
	ProcName              string                    `json:"proc_name"`
	ExeName               string                    `json:"exe_name"`
	Cmdline               string                    `json:"cmdline"`
	IdentityFingerprint   string                    `json:"identity_fingerprint"`
	PIDs                  []uint32                  `json:"pids"`
	ListenPorts           []uint16                  `json:"listen_ports"`
	ObservedRole          string                    `json:"observed_role"`       // SERVER | CLIENT_ONLY | DUAL
	ObservedTransports    []string                  `json:"observed_transports"` // ["TCP"] / ["MESH"] / ["TCP","MESH"]
	EnrollStatus          string                    `json:"enroll_status"`       // UNENROLLED | ENROLLED_DISARMED | ARMED_READY | ARMED_NEED_RECONNECT
	IdentityMatch         string                    `json:"identity_match"`      // MATCHED | AMBIGUOUS | LOW_CONFIDENCE | NEW
	Candidates            []ProcessCandidateDTO     `json:"candidates,omitempty"`
	ArchProcessID         *uint64                   `json:"arch_process_id,omitempty"`
	ArchProcessCode       string                    `json:"arch_process_code,omitempty"`
	ArchProcessName       string                    `json:"arch_process_name,omitempty"`
	ProcessID             *uint64                   `json:"process_id,omitempty"`
	ProbeStatus           string                    `json:"probe_status,omitempty"`
	IsRemoteLDC           bool                      `json:"is_remote_ldc"`
	RemoteNode            string                    `json:"remote_node,omitempty"`
	ClaimedServiceCount   int                       `json:"claimed_service_count"`
	UnclaimedServiceCount int                       `json:"unclaimed_service_count"`
	Services              []DiscoveryLiveServiceDTO `json:"services"`
	Subscriptions         []DiscoveryLiveSubDTO     `json:"subscriptions"`
}

// DiscoveryLiveServiceDTO represents a normalized service published by a process group.
type DiscoveryLiveServiceDTO struct {
	ServiceName      string   `json:"service_name"`
	RawServiceName   string   `json:"raw_service_name"`
	Endpoints        []string `json:"endpoints"`
	Transport        string   `json:"transport"` // TCP | MESH | MIXED
	ServerPIDs       []uint32 `json:"server_pids"`
	InstanceCount    int      `json:"instance_count"`
	IsMultiHost      bool     `json:"is_multi_host"`
	SubscriberCount  int      `json:"subscriber_count"`
	ProtoMatchStatus string   `json:"proto_match_status"` // EXACT | UNMATCHED
	ProtoServiceName string   `json:"proto_service_name,omitempty"`
	ClaimStatus      string   `json:"claim_status"` // CLAIMED | UNCLAIMED
	SSOTServiceID    *uint64  `json:"ssot_service_id,omitempty"`
	SSOTModuleID     *uint64  `json:"ssot_module_id,omitempty"`
	SSOTModuleCode   string   `json:"ssot_module_code,omitempty"`
	SSOTModuleName   string   `json:"ssot_module_name,omitempty"`
}

// DiscoveryLiveSubDTO represents a process subscription edge and its resolved provider kind.
type DiscoveryLiveSubDTO struct {
	ServiceName      string `json:"service_name"`
	RawServiceName   string `json:"raw_service_name"`
	ProviderKind     string `json:"provider_kind"` // LOCAL_ENROLLED | LOCAL_UNENROLLED | REMOTE_LDC | OFFLINE
	ProviderProcKey  string `json:"provider_proc_key,omitempty"`
	ProviderProcName string `json:"provider_proc_name,omitempty"`
	ProviderEndpoint string `json:"provider_endpoint,omitempty"`
	RemoteNode       string `json:"remote_node,omitempty"`
	Transport        string `json:"transport,omitempty"`
}

type liveCacheKey struct {
	agentID uint64
	csgAddr string
}

type liveCacheEntry struct {
	resp      *DiscoveryLiveRespDTO
	expiresAt time.Time
}

type refreshRateKey struct {
	userID  uint
	agentID uint64
}

// LiveDiscoveryService orchestrates singleflight live discovery, 5s TTL caching,
// 3s per-user-per-agent manual refresh rate limiting, and offline snapshot fallback.
type LiveDiscoveryService struct {
	db          *gorm.DB
	fetcher     AgentDiscoveryFetcher
	ttl         time.Duration
	rateLimit   time.Duration
	sfGroup     singleflight.Group
	cacheMu     sync.RWMutex
	cache       map[liveCacheKey]liveCacheEntry
	rateMu      sync.Mutex
	lastRefresh map[refreshRateKey]time.Time
}

// NewLiveDiscoveryService builds a LiveDiscoveryService with default 5s TTL and 3s refresh limit.
func NewLiveDiscoveryService(db *gorm.DB, fetcher AgentDiscoveryFetcher) *LiveDiscoveryService {
	return &LiveDiscoveryService{
		db:          db,
		fetcher:     fetcher,
		ttl:         DefaultLiveCacheTTL,
		rateLimit:   DefaultRefreshRateLimit,
		cache:       make(map[liveCacheKey]liveCacheEntry),
		lastRefresh: make(map[refreshRateKey]time.Time),
	}
}

// ResetCache clears in-memory TTL cache and refresh rate limit records (used in tests).
func (s *LiveDiscoveryService) ResetCache() {
	s.cacheMu.Lock()
	s.cache = make(map[liveCacheKey]liveCacheEntry)
	s.cacheMu.Unlock()

	s.rateMu.Lock()
	s.lastRefresh = make(map[refreshRateKey]time.Time)
	s.rateMu.Unlock()
}

// InvalidateCache clears cached live discovery entries for the specified agent.
func (s *LiveDiscoveryService) InvalidateCache(agentID uint64) {
	s.cacheMu.Lock()
	for k := range s.cache {
		if k.agentID == agentID {
			delete(s.cache, k)
		}
	}
	s.cacheMu.Unlock()
}

// GetLiveDiscovery fetches live discovery for (agentID, csgAddr) with singleflight, 5s TTL cache,
// 3s per-user-per-agent manual refresh rate limit, ScopeReadGuard, and offline snapshot fallback.
func (s *LiveDiscoveryService) GetLiveDiscovery(ctx context.Context, agentID uint64, csgAddr string, refresh bool, userID uint, isAdmin bool) (*DiscoveryLiveRespDTO, error) {
	agent, err := loadAndGuardAgent(s.db, agentID, userID, isAdmin)
	if err != nil {
		return nil, err
	}

	normCSG := strings.TrimSpace(csgAddr)
	if refresh {
		if err := s.checkAndRecordRefresh(userID, agentID); err != nil {
			return nil, err
		}
	} else {
		if cached, ok := s.getCached(agentID, normCSG); ok {
			return cached, nil
		}
	}

	sfKey := fmt.Sprintf("%d|%s|%t", agentID, normCSG, refresh)
	val, err, _ := s.sfGroup.Do(sfKey, func() (interface{}, error) {
		if !refresh {
			if cached, ok := s.getCached(agentID, normCSG); ok {
				return cached, nil
			}
		}

		if agent.Status == models.AgentStatusOffline || agent.Endpoint == "" || s.fetcher == nil {
			return s.fallbackFromSnapshot(agent, normCSG, "Agent 当前离线")
		}

		var (
			report     *agentproto.CSGTopologyReport
			discovered []agentproto.DiscoveredProcess
			topoErr    error
			procErr    error
			wg         sync.WaitGroup
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			report, topoErr = s.fetcher.FetchAgentCSGTopology(ctx, agentID, normCSG, userID, isAdmin)
		}()
		go func() {
			defer wg.Done()
			discovered, procErr = s.fetcher.FetchAgentDiscoveredProcesses(ctx, agentID, userID, isAdmin)
		}()
		wg.Wait()

		if topoErr != nil {
			if errors.Is(topoErr, ErrAgentForbidden) || errors.Is(topoErr, ErrAgentNotFound) {
				return nil, topoErr
			}
			// Fallback to persisted snapshot when agent is unreachable/offline
			if fb, fbErr := s.fallbackFromSnapshot(agent, normCSG, topoErr.Error()); fbErr == nil {
				return fb, nil
			}
			// If /proc succeeded even when CSG RPC failed, degrade gracefully to PROC_ONLY
			if procErr == nil && len(discovered) > 0 {
				report = &agentproto.CSGTopologyReport{
					DiscoveryMode: "PROC_ONLY",
					CapturedAt:    time.Now().UTC().Format(time.RFC3339),
					Errors:        []string{topoErr.Error()},
				}
			} else {
				return nil, fmt.Errorf("%w: %v", ErrAgentUnavailable, topoErr)
			}
		}

		if procErr != nil && report != nil {
			report = RedactCSGReport(report)
			report.Errors = append(report.Errors, "proc discover: "+procErr.Error())
		}

		resp, aggErr := AggregateProcessCentricForUser(s.db, agentID, report, discovered, userID, isAdmin)
		if aggErr != nil {
			return nil, aggErr
		}
		resp.AgentName = agent.Name
		resp.AgentScope = agent.Scope
		if resp.CSGAddr == "" {
			resp.CSGAddr = normCSG
		}
		if resp.CSGAddr == "" && len(resp.CSGEndpoints) > 0 {
			resp.CSGAddr = resp.CSGEndpoints[0].Addr()
		}

		s.setCached(agentID, normCSG, resp)
		if resp.CSGAddr != "" && resp.CSGAddr != normCSG {
			s.setCached(agentID, resp.CSGAddr, resp)
		}
		return resp, nil
	})
	if err != nil {
		return nil, err
	}
	return val.(*DiscoveryLiveRespDTO), nil
}

func (s *LiveDiscoveryService) checkAndRecordRefresh(userID uint, agentID uint64) error {
	key := refreshRateKey{userID: userID, agentID: agentID}
	now := time.Now()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if last, ok := s.lastRefresh[key]; ok && now.Sub(last) < s.rateLimit {
		return ErrRefreshRateLimited
	}
	s.lastRefresh[key] = now
	return nil
}

func (s *LiveDiscoveryService) getCached(agentID uint64, csgAddr string) (*DiscoveryLiveRespDTO, bool) {
	now := time.Now()
	s.cacheMu.RLock()
	entry, ok := s.cache[liveCacheKey{agentID: agentID, csgAddr: csgAddr}]
	s.cacheMu.RUnlock()
	if !ok || now.After(entry.expiresAt) {
		return nil, false
	}
	return entry.resp, true
}

func (s *LiveDiscoveryService) setCached(agentID uint64, csgAddr string, resp *DiscoveryLiveRespDTO) {
	s.cacheMu.Lock()
	s.cache[liveCacheKey{agentID: agentID, csgAddr: csgAddr}] = liveCacheEntry{
		resp:      resp,
		expiresAt: time.Now().Add(s.ttl),
	}
	s.cacheMu.Unlock()
}

func (s *LiveDiscoveryService) fallbackFromSnapshot(agent *models.Agent, csgAddr, reason string) (*DiscoveryLiveRespDTO, error) {
	if s.db == nil {
		return nil, ErrAgentUnavailable
	}
	var snap models.DiscoverySnapshot
	q := s.db.Where("agent_id = ?", agent.ID)
	if csgAddr != "" {
		q = q.Where("csg_addr = ?", csgAddr)
	}
	if err := q.Order("captured_at DESC, id DESC").First(&snap).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAgentUnavailable
		}
		return nil, err
	}

	var dto DiscoveryLiveRespDTO
	if err := json.Unmarshal(snap.PayloadJSON, &dto); err != nil {
		return nil, fmt.Errorf("解析离线快照失败: %w", err)
	}
	snapID := snap.ID
	dto.AgentID = agent.ID
	if dto.AgentName == "" {
		dto.AgentName = agent.Name
	}
	dto.AgentScope = agent.Scope
	dto.Stale = true
	dto.StaleReason = reason
	dto.CapturedAt = snap.CapturedAt.UTC().Format(time.RFC3339)
	dto.SnapshotID = &snapID
	normalizeLiveRespSlices(&dto)
	return &dto, nil
}

// AggregateProcessCentric folds CSGTopologyReport + /proc DiscoveredProcess into process-centric DTOs.
func AggregateProcessCentric(db *gorm.DB, agentID uint64, report *agentproto.CSGTopologyReport, discovered []agentproto.DiscoveredProcess) (*DiscoveryLiveRespDTO, error) {
	return AggregateProcessCentricForUser(db, agentID, report, discovered, 0, true)
}

// AggregateProcessCentricForUser folds CSGTopologyReport + /proc DiscoveredProcess with V2 PROVISIONAL visibility filtering.
func AggregateProcessCentricForUser(db *gorm.DB, agentID uint64, report *agentproto.CSGTopologyReport, discovered []agentproto.DiscoveredProcess, userID uint, isAdmin bool) (*DiscoveryLiveRespDTO, error) {
	cleanReport := RedactCSGReport(report)
	if cleanReport == nil {
		cleanReport = &agentproto.CSGTopologyReport{}
	}
	cleanDisc := RedactDiscoveredProcesses(discovered)

	// Load read-only SSOT & mounted process metadata when DB is available.
	ssotMeta := loadSSOTReadOnlyMeta(db, agentID, userID, isAdmin)

	discByPID := make(map[uint32]agentproto.DiscoveredProcess, len(cleanDisc))
	for _, dp := range cleanDisc {
		if dp.PID > 0 {
			discByPID[dp.PID] = dp
		}
	}

	type svcAccum struct {
		serviceName string
		rawNames    map[string]struct{}
		endpoints   map[string]struct{}
		transports  map[string]struct{}
		pids        map[uint32]struct{}
	}
	type subAccum struct {
		serviceName string
		rawName     string
		transport   string
		clientAddr  string
	}
	type procAccum struct {
		processKey  string
		procName    string
		exeName     string
		cmdline     string
		fingerprint string
		lowConf     bool
		isRemoteLDC bool
		remoteNode  string
		pids        map[uint32]struct{}
		listenPorts map[uint16]struct{}
		transports  map[string]struct{}
		services    map[string]*svcAccum
		subs        map[string]*subAccum
	}

	groups := make(map[string]*procAccum)
	pidToGroupKey := make(map[uint32]string)

	ensureGroup := func(pid uint32, procHint string, isRemote bool, osNodeID uint32, hostIP string) *procAccum {
		dp, hasDP := discByPID[pid]
		exeName := ""
		cmdline := ""
		if hasDP && !isRemote {
			exeName = dp.ExeName
			cmdline = dp.Cmdline
		}
		procName := StripProcPIDSuffix(firstNonEmpty(procHint, exeName))
		if procName == "" && pid > 0 {
			procName = fmt.Sprintf("pid-%d", pid)
		}

		fp, canonKey, lowConf := ComputeIdentityFingerprint(exeName, cmdline, procName)
		groupKey := canonKey
		remoteNode := ""
		if isRemote {
			if osNodeID > 0 {
				remoteNode = fmt.Sprintf("node-%d", osNodeID)
			} else if hostIP != "" {
				remoteNode = hostIP
			} else {
				remoteNode = "remote-ldc"
			}
			groupKey = fmt.Sprintf("remote:%s:%s", remoteNode, strings.ToLower(procName))
		}

		g, exists := groups[groupKey]
		if !exists {
			g = &procAccum{
				processKey:  groupKey,
				procName:    procName,
				exeName:     firstNonEmpty(filepath.Base(exeName), procName),
				cmdline:     cmdline,
				fingerprint: fp,
				lowConf:     lowConf,
				isRemoteLDC: isRemote,
				remoteNode:  remoteNode,
				pids:        make(map[uint32]struct{}),
				listenPorts: make(map[uint16]struct{}),
				transports:  make(map[string]struct{}),
				services:    make(map[string]*svcAccum),
				subs:        make(map[string]*subAccum),
			}
			groups[groupKey] = g
		} else {
			if g.cmdline == "" && cmdline != "" {
				g.cmdline = cmdline
				g.lowConf = lowConf
			}
			if g.exeName == "" && exeName != "" {
				g.exeName = filepath.Base(exeName)
			}
		}

		if pid > 0 {
			g.pids[pid] = struct{}{}
			if !isRemote {
				pidToGroupKey[pid] = groupKey
			}
		}
		if hasDP && !isRemote && dp.ListenPort > 0 {
			g.listenPorts[dp.ListenPort] = struct{}{}
		}
		if hasDP && !isRemote {
			for _, tr := range dp.Transports {
				if normTr := normalizeTransportLabel(tr); normTr != "" {
					g.transports[normTr] = struct{}{}
				}
			}
		}
		return g
	}

	// 1. Fold all /proc discovered local processes first (guarantees pure clients & mesh processes enter tree)
	for _, dp := range cleanDisc {
		ensureGroup(dp.PID, dp.ExeName, false, 0, "")
	}

	// Track normalized service providers for subscriber count & provider_kind resolution
	type providerInfo struct {
		groupKey    string
		procName    string
		isRemoteLDC bool
		remoteNode  string
		endpoint    string
		transport   string
	}
	serviceProviders := make(map[string][]providerInfo)
	serviceSubscriberCount := make(map[string]int)

	// 2. Fold CSG services (local & remote LDC)
	for _, ls := range cleanReport.Services {
		isRemote := ls.IsRemoteLDC || (!ls.IsLocal && ls.OsNodeID > 0)
		var g *procAccum
		if !isRemote && ls.ServerPID > 0 {
			if existingKey, ok := pidToGroupKey[ls.ServerPID]; ok {
				g = groups[existingKey]
			}
		}
		if g == nil {
			g = ensureGroup(ls.ServerPID, ls.ServerProc, isRemote, ls.OsNodeID, ls.HostIP)
		}

		normSvc := topology.StripCSGPIDSuffix(ls.ServiceName)
		if normSvc == "" {
			normSvc = strings.TrimSpace(ls.ServiceName)
		}
		normTr := normalizeTransportLabel(ls.Transport)
		if normTr != "" {
			g.transports[normTr] = struct{}{}
		}
		if ls.Port > 0 && !isRemote && normTr != "MESH" {
			g.listenPorts[ls.Port] = struct{}{}
		}

		sa, ok := g.services[normSvc]
		if !ok {
			sa = &svcAccum{
				serviceName: normSvc,
				rawNames:    make(map[string]struct{}),
				endpoints:   make(map[string]struct{}),
				transports:  make(map[string]struct{}),
				pids:        make(map[uint32]struct{}),
			}
			g.services[normSvc] = sa
		}
		sa.rawNames[ls.ServiceName] = struct{}{}
		if ls.Addr != "" {
			sa.endpoints[ls.Addr] = struct{}{}
		}
		if normTr != "" {
			sa.transports[normTr] = struct{}{}
		}
		if ls.ServerPID > 0 {
			sa.pids[ls.ServerPID] = struct{}{}
		}

		serviceProviders[normSvc] = append(serviceProviders[normSvc], providerInfo{
			groupKey:    g.processKey,
			procName:    g.procName,
			isRemoteLDC: isRemote,
			remoteNode:  g.remoteNode,
			endpoint:    ls.Addr,
			transport:   normTr,
		})
	}

	// 3. Fold CSG subscriptions + RawClients (deduplicated)
	type subInput struct {
		pid        uint32
		procName   string
		clientAddr string
		svcName    string
		isRemote   bool
	}
	subSeen := make(map[string]struct{})
	var allSubs []subInput
	addSubInput := func(pid uint32, procName, clientAddr, svcName string, isRemote bool) {
		normSvc := topology.StripCSGPIDSuffix(svcName)
		if normSvc == "" {
			return
		}
		dedupKey := fmt.Sprintf("%d|%s|%s|%t", pid, strings.ToLower(StripProcPIDSuffix(procName)), normSvc, isRemote)
		if _, ok := subSeen[dedupKey]; ok {
			return
		}
		subSeen[dedupKey] = struct{}{}
		allSubs = append(allSubs, subInput{
			pid:        pid,
			procName:   procName,
			clientAddr: clientAddr,
			svcName:    svcName,
			isRemote:   isRemote,
		})
	}
	for _, sub := range cleanReport.Subscriptions {
		addSubInput(sub.ClientPID, sub.ClientProc, sub.ClientAddr, sub.ServiceName, false)
	}
	for _, rc := range cleanReport.RawClients {
		addSubInput(rc.PID, rc.ProcName, rc.ClientAddr, rc.ServiceName, rc.IsRemoteLDC)
	}

	for _, sub := range allSubs {
		var g *procAccum
		if !sub.isRemote && sub.pid > 0 {
			if existingKey, ok := pidToGroupKey[sub.pid]; ok {
				g = groups[existingKey]
			}
		}
		if g == nil {
			g = ensureGroup(sub.pid, sub.procName, sub.isRemote, 0, sub.clientAddr)
		}
		normSvc := topology.StripCSGPIDSuffix(sub.svcName)
		if _, exists := g.subs[normSvc]; !exists {
			g.subs[normSvc] = &subAccum{
				serviceName: normSvc,
				rawName:     sub.svcName,
				clientAddr:  sub.clientAddr,
			}
			if !sub.isRemote {
				serviceSubscriberCount[normSvc]++
			}
		}
	}

	// Determine multi-host services across distinct local process groups
	localHostGroupsBySvc := make(map[string]map[string]struct{})
	for svcName, provs := range serviceProviders {
		for _, p := range provs {
			if p.isRemoteLDC {
				continue
			}
			if localHostGroupsBySvc[svcName] == nil {
				localHostGroupsBySvc[svcName] = make(map[string]struct{})
			}
			localHostGroupsBySvc[svcName][p.groupKey] = struct{}{}
		}
	}

	// Precompute SSOT match per process group once for O(1) reuse across process rows and subscriptions.
	matchByGroupKey := make(map[string]procSSOTMatchResult, len(groups))
	for k, g := range groups {
		matchByGroupKey[k] = matchProcessSSOT(ssotMeta, g.exeName, g.cmdline, g.procName, g.lowConf)
	}

	// Build final DTO lists
	discoveryMode := firstNonEmpty(cleanReport.DiscoveryMode, "PROC_ONLY")
	isProcOnly := strings.EqualFold(discoveryMode, "PROC_ONLY")

	localDTOs := make([]DiscoveryProcessGroupDTO, 0, len(groups))
	remoteDTOs := make([]DiscoveryProcessGroupDTO, 0)
	var summary DiscoverySummaryDTO

	for _, g := range groups {
		// Match process against SSOT ArchProcess & mounted Process
		matchResult := matchByGroupKey[g.processKey]

		// 在非 PROC_ONLY（已成功连通 CSG rpcmon）模式下，过滤掉既无发布服务、也无下游订阅、
		// 且尚未在平台纳管的宿主机无关监听进程（如 systemd/sshd/postgres/agent 自身等）。
		if !isProcOnly && !g.isRemoteLDC && len(g.services) == 0 && len(g.subs) == 0 && matchResult.processID == nil {
			continue
		}

		pids := sortedUint32Set(g.pids)
		ports := sortedUint16Set(g.listenPorts)

		role := "CLIENT_ONLY"
		if len(g.services) > 0 && len(g.subs) > 0 {
			role = "DUAL"
		} else if len(g.services) > 0 {
			role = "SERVER"
		}

		svcDTOs := make([]DiscoveryLiveServiceDTO, 0, len(g.services))
		claimedCnt := 0
		unclaimedCnt := 0
		for _, sa := range g.services {
			endpoints := sortedStringSet(sa.endpoints)
			rawNames := sortedStringSet(sa.rawNames)
			rawName := sa.serviceName
			if len(rawNames) > 0 {
				rawName = rawNames[0]
			}
			trList := sortedStringSet(sa.transports)
			tr := "TCP"
			if len(trList) > 1 {
				tr = "MIXED"
			} else if len(trList) == 1 {
				tr = trList[0]
			}
			if !g.isRemoteLDC {
				switch tr {
				case "TCP":
					summary.TCPCount++
				case "MESH":
					summary.MeshCount++
				case "MIXED":
					summary.TCPCount++
					summary.MeshCount++
				}
			}

			svcDTO := DiscoveryLiveServiceDTO{
				ServiceName:      sa.serviceName,
				RawServiceName:   rawName,
				Endpoints:        endpoints,
				Transport:        tr,
				ServerPIDs:       sortedUint32Set(sa.pids),
				InstanceCount:    maxInt(len(sa.pids), 1),
				IsMultiHost:      len(localHostGroupsBySvc[sa.serviceName]) > 1,
				SubscriberCount:  serviceSubscriberCount[sa.serviceName],
				ProtoMatchStatus: "UNMATCHED",
				ClaimStatus:      "UNCLAIMED",
			}

			if ms, ok := ssotMeta.svcByName[sa.serviceName]; ok {
				sid := ms.ID
				svcDTO.SSOTServiceID = &sid
				if ms.ModuleID != nil && *ms.ModuleID > 0 {
					mid := *ms.ModuleID
					svcDTO.SSOTModuleID = &mid
					svcDTO.ClaimStatus = "CLAIMED"
					claimedCnt++
				} else {
					unclaimedCnt++
				}
				if ms.Module != nil {
					svcDTO.SSOTModuleCode = ms.Module.Code
					svcDTO.SSOTModuleName = ms.Module.Name
				}
				if protoSvc := strings.TrimSpace(ms.ProtoServiceName); protoSvc != "" {
					svcDTO.ProtoServiceName = protoSvc
					svcDTO.ProtoMatchStatus = "EXACT"
				}
				if !g.isRemoteLDC {
					summary.MatchedServices++
				}
			} else {
				unclaimedCnt++
				if !g.isRemoteLDC {
					summary.UndeclaredServices++
				}
			}

			if svcDTO.ProtoMatchStatus == "UNMATCHED" {
				if protoOpSvc, ok := ssotMeta.protoOpsByName[sa.serviceName]; ok {
					svcDTO.ProtoMatchStatus = "EXACT"
					svcDTO.ProtoServiceName = protoOpSvc
				}
			}
			svcDTOs = append(svcDTOs, svcDTO)
		}
		sort.Slice(svcDTOs, func(i, j int) bool {
			return svcDTOs[i].ServiceName < svcDTOs[j].ServiceName
		})

		subDTOs := make([]DiscoveryLiveSubDTO, 0, len(g.subs))
		for _, sub := range g.subs {
			subDTO := DiscoveryLiveSubDTO{
				ServiceName:    sub.serviceName,
				RawServiceName: sub.rawName,
				ProviderKind:   "OFFLINE",
			}
			if provs := serviceProviders[sub.serviceName]; len(provs) > 0 {
				chosen := provs[0]
				for _, p := range provs {
					if !p.isRemoteLDC {
						chosen = p
						break
					}
				}
				subDTO.ProviderProcKey = chosen.groupKey
				subDTO.ProviderProcName = chosen.procName
				subDTO.ProviderEndpoint = chosen.endpoint
				subDTO.RemoteNode = chosen.remoteNode
				subDTO.Transport = chosen.transport
				if chosen.isRemoteLDC {
					subDTO.ProviderKind = "REMOTE_LDC"
				} else if provMatch, ok := matchByGroupKey[chosen.groupKey]; ok {
					if provMatch.processID != nil {
						subDTO.ProviderKind = "LOCAL_ENROLLED"
					} else {
						subDTO.ProviderKind = "LOCAL_UNENROLLED"
					}
				} else {
					subDTO.ProviderKind = "LOCAL_UNENROLLED"
				}
			}
			subDTOs = append(subDTOs, subDTO)
		}
		sort.Slice(subDTOs, func(i, j int) bool {
			return subDTOs[i].ServiceName < subDTOs[j].ServiceName
		})

		transports := sortedStringSet(g.transports)
		if len(transports) == 0 && len(subDTOs) > 0 {
			inferredTransports := make(map[string]struct{})
			for _, sub := range subDTOs {
				tr := normalizeTransportLabel(sub.Transport)
				switch tr {
				case "":
					continue
				case "MIXED":
					inferredTransports["TCP"] = struct{}{}
					inferredTransports["MESH"] = struct{}{}
				default:
					inferredTransports[tr] = struct{}{}
				}
			}
			transports = sortedStringSet(inferredTransports)
		}

		dto := DiscoveryProcessGroupDTO{
			ProcessKey:            g.processKey,
			ProcName:              g.procName,
			ExeName:               g.exeName,
			Cmdline:               g.cmdline,
			IdentityFingerprint:   g.fingerprint,
			PIDs:                  pids,
			ListenPorts:           ports,
			ObservedRole:          role,
			ObservedTransports:    transports,
			EnrollStatus:          matchResult.enrollStatus,
			IdentityMatch:         matchResult.identityMatch,
			Candidates:            matchResult.candidates,
			ArchProcessID:         matchResult.archProcessID,
			ArchProcessCode:       matchResult.archProcessCode,
			ArchProcessName:       matchResult.archProcessName,
			ProcessID:             matchResult.processID,
			ProbeStatus:           matchResult.probeStatus,
			IsRemoteLDC:           g.isRemoteLDC,
			RemoteNode:            g.remoteNode,
			ClaimedServiceCount:   claimedCnt,
			UnclaimedServiceCount: unclaimedCnt,
			Services:              svcDTOs,
			Subscriptions:         subDTOs,
		}

		if g.isRemoteLDC {
			remoteDTOs = append(remoteDTOs, dto)
			summary.RemoteLDCCount++
		} else {
			localDTOs = append(localDTOs, dto)
			summary.ProcessCount++
			if role == "CLIENT_ONLY" {
				summary.ClientOnlyCount++
			}
			summary.TotalServices += len(svcDTOs)
			summary.TotalSubscriptions += len(subDTOs)
		}
	}

	sort.Slice(localDTOs, func(i, j int) bool {
		if localDTOs[i].ProcName == localDTOs[j].ProcName {
			return localDTOs[i].ProcessKey < localDTOs[j].ProcessKey
		}
		return localDTOs[i].ProcName < localDTOs[j].ProcName
	})
	sort.Slice(remoteDTOs, func(i, j int) bool {
		if remoteDTOs[i].RemoteNode == remoteDTOs[j].RemoteNode {
			return remoteDTOs[i].ProcName < remoteDTOs[j].ProcName
		}
		return remoteDTOs[i].RemoteNode < remoteDTOs[j].RemoteNode
	})

	endpoints := cleanReport.CSGEndpoints
	if endpoints == nil {
		endpoints = []agentproto.CSGEndpointInfo{}
	}
	ports := cleanReport.PortUsages
	if ports == nil {
		ports = []agentproto.CSGPortUsage{}
	}
	csgAddr := ""
	if len(endpoints) > 0 {
		csgAddr = endpoints[0].Addr()
	}
	capturedAt := cleanReport.CapturedAt
	if capturedAt == "" {
		capturedAt = time.Now().UTC().Format(time.RFC3339)
	}

	resp := &DiscoveryLiveRespDTO{
		AgentID:            agentID,
		CSGAddr:            csgAddr,
		CSGEndpoints:       endpoints,
		Stale:              false,
		CapturedAt:         capturedAt,
		DiscoveryMode:      discoveryMode,
		Ports:              ports,
		Processes:          localDTOs,
		RemoteLDCProcesses: remoteDTOs,
		Summary:            summary,
		Errors:             cleanReport.Errors,
	}
	normalizeLiveRespSlices(resp)
	return resp, nil
}

type ssotReadOnlyMeta struct {
	mountedByArchID map[uint64]models.Process
	archProcesses   []models.ArchProcess
	svcByName       map[string]models.ModuleService
	protoOpsByName  map[string]string
}

func loadSSOTReadOnlyMeta(db *gorm.DB, agentID uint64, userID uint, isAdmin bool) ssotReadOnlyMeta {
	meta := ssotReadOnlyMeta{
		mountedByArchID: make(map[uint64]models.Process),
		svcByName:       make(map[string]models.ModuleService),
		protoOpsByName:  make(map[string]string),
	}
	if db == nil {
		return meta
	}

	var procs []models.Process
	if err := db.Where("agent_id = ?", agentID).Find(&procs).Error; err == nil {
		for _, p := range procs {
			meta.mountedByArchID[p.ArchProcessID] = p
		}
	}

	var agents []models.Agent
	agentMap := make(map[uint64]*models.Agent)
	if err := db.Select("id, scope, owner_id").Find(&agents).Error; err == nil {
		for i := range agents {
			agentMap[agents[i].ID] = &agents[i]
		}
	}

	var aps []models.ArchProcess
	if err := db.Order("id ASC").Find(&aps).Error; err == nil {
		for _, ap := range aps {
			if IsArchProcessVisibleToUser(ap, agentMap, userID, isAdmin) {
				meta.archProcesses = append(meta.archProcesses, ap)
			}
		}
	}

	var svcs []models.ModuleService
	if err := db.Preload("Module").Order("id ASC").Find(&svcs).Error; err == nil {
		for _, s := range svcs {
			norm := topology.StripCSGPIDSuffix(s.ServiceName)
			if _, exists := meta.svcByName[s.ServiceName]; !exists {
				meta.svcByName[s.ServiceName] = s
			}
			if _, exists := meta.svcByName[norm]; !exists {
				meta.svcByName[norm] = s
			}
			if pSvc := strings.TrimSpace(s.ProtoServiceName); pSvc != "" {
				if _, exists := meta.svcByName[pSvc]; !exists {
					meta.svcByName[pSvc] = s
				}
			}
		}
	}

	var protoNames []string
	if err := db.Model(&models.ProtoOperation{}).Where("is_active = ?", true).Distinct("service_name").Pluck("service_name", &protoNames).Error; err == nil {
		for _, pn := range protoNames {
			meta.protoOpsByName[pn] = pn
			norm := topology.StripCSGPIDSuffix(pn)
			if _, exists := meta.protoOpsByName[norm]; !exists {
				meta.protoOpsByName[norm] = pn
			}
		}
	}

	return meta
}

type procSSOTMatchResult struct {
	enrollStatus    string
	identityMatch   string
	candidates      []ProcessCandidateDTO
	archProcessID   *uint64
	archProcessCode string
	archProcessName string
	processID       *uint64
	probeStatus     string
}

func matchProcessSSOT(meta ssotReadOnlyMeta, exeName, cmdline, procName string, lowConf bool) procSSOTMatchResult {
	res := procSSOTMatchResult{
		enrollStatus:  "UNENROLLED",
		identityMatch: "NEW",
	}

	resolved := ResolveProcessIdentity(meta.archProcesses, meta.mountedByArchID, exeName, cmdline, procName, nil, false)
	if lowConf || resolved.IsLowConfidence {
		res.identityMatch = "LOW_CONFIDENCE"
	}

	switch resolved.MatchStatus {
	case "AMBIGUOUS":
		res.identityMatch = "AMBIGUOUS"
		res.candidates = resolved.Candidates
		return res
	case "LOW_CONFIDENCE":
		res.identityMatch = "LOW_CONFIDENCE"
		res.candidates = resolved.Candidates
		if len(resolved.Candidates) == 1 {
			c := resolved.Candidates[0]
			res.archProcessID = &c.ArchProcessID
			res.archProcessCode = c.Code
			res.archProcessName = c.Name
		} else {
			return res
		}
	case "MATCHED":
		if resolved.Matched != nil {
			ap := *resolved.Matched
			apID := ap.ID
			res.archProcessID = &apID
			res.archProcessCode = ap.Code
			res.archProcessName = ap.Name
			res.identityMatch = "MATCHED"
		}
	}

	if res.archProcessID != nil {
		if mounted, ok := meta.mountedByArchID[*res.archProcessID]; ok {
			pid := mounted.ID
			res.processID = &pid
			res.probeStatus = mounted.ProbeStatus
			switch mounted.ProbeDesiredState {
			case models.ProbeDesiredStateDisarmed:
				res.enrollStatus = "ENROLLED_DISARMED"
			default:
				if mounted.ProbeStatus == models.ProbeStatusNeedReconnect {
					res.enrollStatus = "ARMED_NEED_RECONNECT"
				} else {
					res.enrollStatus = "ARMED_READY"
				}
			}
		}
	}
	return res
}

func loadAndGuardAgent(db *gorm.DB, agentID uint64, userID uint, isAdmin bool) (*models.Agent, error) {
	if db == nil {
		return nil, ErrAgentNotFound
	}
	var agent models.Agent
	if err := db.First(&agent, agentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAgentNotFound
		}
		return nil, err
	}
	if agent.Scope == models.ScopePrivate && !isAdmin && (agent.OwnerID == nil || *agent.OwnerID != userID) {
		return nil, ErrAgentForbidden
	}
	return &agent, nil
}

func normalizeTransportLabel(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "TCP":
		return "TCP"
	case "MESH", "DOPHI_MESH":
		return "MESH"
	case "MIXED":
		return "MIXED"
	case "":
		return ""
	default:
		return strings.ToUpper(strings.TrimSpace(raw))
	}
}

func sortedUint32Set(m map[uint32]struct{}) []uint32 {
	out := make([]uint32, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedUint16Set(m map[uint16]struct{}) []uint16 {
	out := make([]uint16, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedStringSet(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
