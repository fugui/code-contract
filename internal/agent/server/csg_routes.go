package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"code-contract/internal/agent/csg"
	"code-contract/pkg/agentproto"
)

func (s *AgentServer) locateCSGEndpoints() []agentproto.CSGEndpointInfo {
	eps, err := csg.LocateCSGEndpoints(csg.LocateOptions{
		CLICSGAddrs: s.cfg.CSGAddrs,
		ProcRoot:    s.cfg.ProcRoot,
	})
	if err != nil {
		return nil
	}
	return eps
}

func (s *AgentServer) resolveEffectiveCSGAddr(explicit string) string {
	if trimmed := strings.TrimSpace(explicit); trimmed != "" {
		return trimmed
	}
	if len(s.cfg.CSGAddrs) > 0 && strings.TrimSpace(s.cfg.CSGAddrs[0]) != "" {
		return strings.TrimSpace(s.cfg.CSGAddrs[0])
	}
	eps := s.locateCSGEndpoints()
	if len(eps) > 0 {
		return eps[0].Addr()
	}
	return ""
}

func (s *AgentServer) discoverCSGAndFuse(ctx context.Context, explicitCSGAddr string, l1Procs []agentproto.DiscoveredProcess) (*agentproto.CSGTopologyReport, []agentproto.DiscoveredProcess, error) {
	csgAddr := s.resolveEffectiveCSGAddr(explicitCSGAddr)
	return csg.DiscoverAndFuse(ctx, csg.DiscovererConfig{
		Locate: csg.LocateOptions{
			CLICSGAddrs: s.cfg.CSGAddrs,
			ProcRoot:    s.cfg.ProcRoot,
		},
		MonitorAddr:      s.cfg.CSGMonitorAddr,
		CSGAddr:          csgAddr,
		ProbeBin:         s.cfg.CSGProbeBin,
		ProbeAllowedDirs: s.cfg.CSGProbeAllowedDirs,
		LocalPIDs:        l1Procs,
		SubEnrich:        csgAddr != "",
	})
}

func (s *AgentServer) handleCSGTopology(w http.ResponseWriter, r *http.Request) {
	procs, l1Err := s.procScanner.ScanListenProcesses()
	rep, _, err := s.discoverCSGAndFuse(r.Context(), r.URL.Query().Get("csg_addr"), procs)
	if err != nil {
		http.Error(w, fmt.Sprintf("CSG 拓扑发现失败: %v", err), http.StatusInternalServerError)
		return
	}
	if l1Err != nil {
		rep.Errors = append(rep.Errors, fmt.Sprintf("L1 proc scan degraded: %v", l1Err))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rep)
}

func (s *AgentServer) prepareReplayCSG(ctx context.Context, w http.ResponseWriter, req *agentproto.ExecuteReplayReq) (*csg.SimController, *csg.SimPrepareOutput, uint16, bool) {
	effectivePort := req.ProcessSpec.ListenPort
	strat, ok := agentproto.ParseCSGReplayStrategy(string(req.CSGStrategy))
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("非法 CSG 仿真策略: %s", req.CSGStrategy),
		})
		return nil, nil, effectivePort, true
	}
	if strat == agentproto.CSGStrategyPassthrough {
		return nil, nil, effectivePort, false
	}

	var mockPort uint16
	if s.replayExec != nil {
		var lErr error
		mockPort, lErr = s.replayExec.EnsureMockListener()
		if lErr != nil {
			http.Error(w, fmt.Sprintf("初始化 Mock 监听端口失败: %v", lErr), http.StatusInternalServerError)
			return nil, nil, effectivePort, true
		}
	}

	csgAddr := s.resolveEffectiveCSGAddr(req.CSGAddr)
	if csgAddr == "" {
		csgAddr = s.resolveEffectiveCSGAddr(req.ProcessSpec.CSGAddr)
	}

	simCtrl := csg.NewSimController()
	out, err := simCtrl.Prepare(ctx, csg.SimPrepareInput{
		Strategy:       strat,
		CSGAddr:        csgAddr,
		MockDataPort:   mockPort,
		ListenPort:     req.ProcessSpec.ListenPort,
		TargetServices: req.TargetServices,
		Snapshots:      req.CSGSnapshots,
		Timeout:        3 * time.Second,
	})
	if err != nil {
		_ = simCtrl.Cleanup()
		if s.replayExec != nil {
			s.replayExec.CloseMockListener()
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case errors.Is(err, csg.ErrServiceAlreadyPublished):
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": err.Error(),
				"code":  csg.ErrCodeServiceAlreadyPublished,
			})
		case errors.Is(err, csg.ErrDataPlaneMeshUnsupported):
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": err.Error(),
				"code":  csg.ErrCodeDataPlaneMeshUnsupported,
			})
		default:
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": err.Error(),
			})
		}
		return nil, nil, effectivePort, true
	}

	if out != nil && out.TargetDialAddr != "" {
		if _, p, ok := csg.ParseCSGAddr(out.TargetDialAddr); ok && p > 0 {
			effectivePort = p
		}
	}
	return simCtrl, out, effectivePort, false
}

func (s *AgentServer) finishCSGSim(sim *csg.SimController, taskID uint64) {
	if s.ebpfMgr != nil {
		s.ebpfMgr.ResetPassthrough()
	}
	if s.replayExec != nil {
		s.replayExec.SetTargetDialOverride("")
	}
	if sim == nil {
		return
	}
	_ = sim.Cleanup()
	if s.replayExec != nil && taskID > 0 {
		rep := sim.Report()
		if rep != nil {
			s.replayExec.AttachCSGSandboxReport(taskID, rep, rep.ErrorCode)
		}
	}
	s.mu.Lock()
	if s.activeCSGSim == sim {
		s.activeCSGSim = nil
	}
	s.mu.Unlock()
}
