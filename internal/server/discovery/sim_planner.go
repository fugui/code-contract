package discovery

import (
	"context"
	"errors"
	"strings"

	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/gorm"
)

// SimulationPlanInput 发现驱动三模仿真策略推荐入参
type SimulationPlanInput struct {
	AgentID       uint64
	ArchProcessID uint64
	ProcessKey    string
	CSGAddr       string
	UserID        uint
	IsAdmin       bool
}

// DownstreamLiveStatusDTO 下游订阅服务的实时在线与传输制式摘要
type DownstreamLiveStatusDTO struct {
	ServiceName       string `json:"service_name"`
	ProviderKind      string `json:"provider_kind"`
	ResolvedEndpoint  string `json:"resolved_endpoint,omitempty"`
	Transport         string `json:"transport"` // TCP | MESH | OFFLINE
	IsOnline          bool   `json:"is_online"`
	HasOnlineConflict bool   `json:"has_online_conflict"`
	Note              string `json:"note,omitempty"`
}

// SimulationPlanResult 发现驱动仿真推荐响应 DTO（预填 /replay 任务表单）
type SimulationPlanResult struct {
	AgentID             uint64                    `json:"agent_id"`
	ArchProcessID       uint64                    `json:"arch_process_id"`
	ProcessID           *uint64                   `json:"process_id,omitempty"`
	ProcessName         string                    `json:"process_name"`
	RecommendedStrategy string                    `json:"recommended_strategy"` // PASSTHROUGH | CLIENT_INJECT | PUB_STUB | MOCK_SERVER
	Rationale           string                    `json:"rationale"`
	PrefillReplaySpec   map[string]interface{}    `json:"prefill_replay_spec"`
	DownstreamStatuses  []DownstreamLiveStatusDTO `json:"downstream_statuses"`
}

// BuildSimulationPlan 根据目标进程同环境订阅边在线状态与传输制式推荐 CSG 三模仿真策略
func BuildSimulationPlan(
	ctx context.Context,
	db *gorm.DB,
	liveSvc *LiveDiscoveryService,
	in SimulationPlanInput,
) (*SimulationPlanResult, error) {
	if db == nil {
		return nil, errors.New("discovery: nil db")
	}
	agent, err := loadAndGuardAgent(db, in.AgentID, in.UserID, in.IsAdmin)
	if err != nil {
		return nil, err
	}

	var proc models.Process
	hasProc := false
	if in.ArchProcessID > 0 {
		if err := db.WithContext(ctx).Preload("ArchProcess").
			Where("agent_id = ? AND arch_process_id = ?", agent.ID, in.ArchProcessID).
			First(&proc).Error; err == nil {
			proc.FillFromArchProcess()
			hasProc = true
		}
	}

	// 采集实况以识别在线服务与 dophi_mesh:// 冲突（Agent 离线或 liveSvc 为空时优雅降级为纯 DB 推导）
	var liveResp *DiscoveryLiveRespDTO
	if liveSvc != nil {
		if lr, lErr := liveSvc.GetLiveDiscovery(ctx, agent.ID, in.CSGAddr, false, in.UserID, in.IsAdmin); lErr == nil {
			liveResp = lr
		}
	}

	onlineServices := make(map[string]DiscoveryLiveServiceDTO)
	var matchedLiveProc *DiscoveryProcessGroupDTO
	if liveResp != nil {
		for i := range liveResp.Processes {
			lp := &liveResp.Processes[i]
			for _, svc := range lp.Services {
				onlineServices[svc.ServiceName] = svc
			}
			if in.ProcessKey != "" && lp.ProcessKey == in.ProcessKey {
				matchedLiveProc = lp
			} else if in.ArchProcessID > 0 && lp.ArchProcessID != nil && *lp.ArchProcessID == in.ArchProcessID {
				matchedLiveProc = lp
			}
		}
		if !hasProc && matchedLiveProc != nil && matchedLiveProc.ProcessID != nil {
			if err := db.WithContext(ctx).Preload("ArchProcess").First(&proc, *matchedLiveProc.ProcessID).Error; err == nil {
				proc.FillFromArchProcess()
				hasProc = true
			}
		}
	}

	processName := ""
	archProcID := in.ArchProcessID
	var procIDPtr *uint64
	if hasProc {
		pidVal := proc.ID
		procIDPtr = &pidVal
		archProcID = proc.ArchProcessID
		processName = proc.Name
		if processName == "" && proc.ArchProcess != nil {
			processName = proc.ArchProcess.Name
		}
	} else if matchedLiveProc != nil {
		processName = matchedLiveProc.ProcName
		if matchedLiveProc.ArchProcessID != nil {
			archProcID = *matchedLiveProc.ArchProcessID
		}
	}
	if archProcID > 0 && processName == "" {
		var ap models.ArchProcess
		if err := db.WithContext(ctx).First(&ap, archProcID).Error; err == nil {
			processName = ap.Name
		}
	}

	// 汇总目标进程的发布服务与下游订阅状态
	targetServices := make([]string, 0)
	targetSvcSet := make(map[string]struct{})
	hasMeshPublished := false
	hasOnlineConflict := false
	hasTCPPublished := false

	if hasProc {
		var placements []models.ServicePlacement
		if err := db.WithContext(ctx).Preload("ModuleService").
			Where("process_id = ? AND status <> ?", proc.ID, models.PlacementStatusRetired).
			Find(&placements).Error; err == nil {
			for _, pl := range placements {
				if pl.ModuleService == nil {
					continue
				}
				sn := pl.ModuleService.ServiceName
				if _, exists := targetSvcSet[sn]; !exists {
					targetSvcSet[sn] = struct{}{}
					targetServices = append(targetServices, sn)
				}
				if strings.EqualFold(pl.ObservedTransport, models.ObservedTransportMESH) ||
					strings.EqualFold(pl.ObservedTransport, models.ObservedTransportMixed) {
					hasMeshPublished = true
				} else {
					hasTCPPublished = true
				}
				if _, online := onlineServices[sn]; online {
					hasOnlineConflict = true
				}
			}
		}
	}
	if matchedLiveProc != nil {
		for _, svc := range matchedLiveProc.Services {
			if _, exists := targetSvcSet[svc.ServiceName]; !exists {
				targetSvcSet[svc.ServiceName] = struct{}{}
				targetServices = append(targetServices, svc.ServiceName)
			}
			if strings.EqualFold(svc.Transport, "MESH") || strings.EqualFold(svc.Transport, "MIXED") {
				hasMeshPublished = true
			} else {
				hasTCPPublished = true
			}
			hasOnlineConflict = true
		}
	}

	downstreamMap := make(map[string]DownstreamLiveStatusDTO)
	if hasProc {
		var deps []models.ProcessDependency
		if err := db.WithContext(ctx).
			Where("consumer_process_id = ? AND status <> ?", proc.ID, models.PlacementStatusRetired).
			Find(&deps).Error; err == nil {
			for _, d := range deps {
				tr := "TCP"
				isOnline := d.ProviderKind != models.ProviderKindOffline
				if strings.HasPrefix(strings.ToLower(d.ResolvedEndpoint), "dophi_mesh://") {
					tr = "MESH"
				} else if !isOnline && d.ResolvedEndpoint == "" {
					tr = "OFFLINE"
				}
				if svc, ok := onlineServices[d.ServiceName]; ok {
					isOnline = true
					if strings.EqualFold(svc.Transport, "MESH") {
						tr = "MESH"
					} else {
						tr = "TCP"
					}
				}
				downstreamMap[d.ServiceName] = DownstreamLiveStatusDTO{
					ServiceName:       d.ServiceName,
					ProviderKind:      d.ProviderKind,
					ResolvedEndpoint:  d.ResolvedEndpoint,
					Transport:         tr,
					IsOnline:          isOnline,
					HasOnlineConflict: isOnline,
				}
			}
		}
	}
	if matchedLiveProc != nil {
		for _, sub := range matchedLiveProc.Subscriptions {
			if _, exists := downstreamMap[sub.ServiceName]; exists {
				continue
			}
			isOnline := sub.ProviderKind != models.ProviderKindOffline
			tr := strings.ToUpper(strings.TrimSpace(sub.Transport))
			if strings.HasPrefix(strings.ToLower(sub.ProviderEndpoint), "dophi_mesh://") {
				tr = "MESH"
			} else if tr == "" {
				if isOnline {
					tr = "TCP"
				} else {
					tr = "OFFLINE"
				}
			}
			downstreamMap[sub.ServiceName] = DownstreamLiveStatusDTO{
				ServiceName:       sub.ServiceName,
				ProviderKind:      sub.ProviderKind,
				ResolvedEndpoint:  sub.ProviderEndpoint,
				Transport:         tr,
				IsOnline:          isOnline,
				HasOnlineConflict: isOnline,
			}
		}
	}

	downstreamStatuses := make([]DownstreamLiveStatusDTO, 0, len(downstreamMap))
	downstreamNames := make([]string, 0, len(downstreamMap))
	hasMeshDownstream := false
	allDownstreamsOffline := len(downstreamMap) > 0
	hasOnlineTCPDownstream := false

	for _, st := range downstreamMap {
		downstreamStatuses = append(downstreamStatuses, st)
		downstreamNames = append(downstreamNames, st.ServiceName)
		if st.Transport == "MESH" {
			hasMeshDownstream = true
		}
		if st.IsOnline {
			allDownstreamsOffline = false
			if st.Transport == "TCP" {
				hasOnlineTCPDownstream = true
			}
		}
	}

	// 策略推荐决策：
	// 1. 纯 dophi_mesh:// 下游/发布，或存在发布同名在线冲突 -> MOCK_SERVER
	// 2. 全部下游订阅均离线缺席 -> PUB_STUB
	// 3. 在线 TCP 动态端口 -> CLIENT_INJECT
	// 4. 其余兜底 -> PASSTHROUGH
	strategy := string(agentproto.CSGStrategyPassthrough)
	rationale := "无冲突且无需虚拟桩，默认使用 PASSTHROUGH 原样透传重放"
	switch {
	case hasMeshDownstream || hasMeshPublished:
		strategy = string(agentproto.CSGStrategyMockServer)
		rationale = "检测到 dophi_mesh:// 传输服务或下游，推荐使用 MOCK_SERVER 在沙箱中自动改写为 tcp:// 并零外泄仿真"
	case allDownstreamsOffline:
		strategy = string(agentproto.CSGStrategyPubStub)
		rationale = "目标进程下游订阅服务当前全部离线缺席（Sub==0），推荐使用 PUB_STUB 发布本地仿真桩"
	case hasOnlineTCPDownstream || (hasTCPPublished && !hasOnlineConflict):
		strategy = string(agentproto.CSGStrategyClientInject)
		rationale = "下游/目标为在线 TCP 动态端口服务，推荐使用 CLIENT_INJECT 经 CSG 实时路由直拨注入"
	case hasOnlineConflict:
		strategy = string(agentproto.CSGStrategyMockServer)
		rationale = "检测到同名服务在现网在线发布冲突，推荐使用 MOCK_SERVER 沙箱隔离防冲突"
	}

	csgAddr := strings.TrimSpace(in.CSGAddr)
	if csgAddr == "" && liveResp != nil {
		csgAddr = liveResp.CSGAddr
	}

	prefill := map[string]interface{}{
		"target_agent_id":     agent.ID,
		"arch_process_id":     archProcID,
		"process_id":          procIDPtr,
		"csg_strategy":        strategy,
		"csg_addr":            csgAddr,
		"target_services":     targetServices,
		"downstream_services": downstreamNames,
	}

	return &SimulationPlanResult{
		AgentID:             agent.ID,
		ArchProcessID:       archProcID,
		ProcessID:           procIDPtr,
		ProcessName:         processName,
		RecommendedStrategy: strategy,
		Rationale:           rationale,
		PrefillReplaySpec:   prefill,
		DownstreamStatuses:  downstreamStatuses,
	}, nil
}
