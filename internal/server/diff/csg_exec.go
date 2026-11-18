package diff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"code-contract/internal/server/topology"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
	"gorm.io/gorm"
)

// CSGConflictError indicates PUB_STUB preflight detected live CSG instances (HTTP 409).
type CSGConflictError struct {
	Code    string `json:"code"`
	Message string `json:"error"`
}

func (e *CSGConflictError) Error() string {
	return fmt.Sprintf("409 Conflict (%s): %s", e.Code, e.Message)
}

// CSGMeshUnsupportedError indicates pure dophi_mesh transport cannot be replayed on TCP data plane (HTTP 422).
type CSGMeshUnsupportedError struct {
	Code    string `json:"code"`
	Message string `json:"error"`
}

func (e *CSGMeshUnsupportedError) Error() string {
	return fmt.Sprintf("422 Unprocessable Entity (%s): %s", e.Code, e.Message)
}

func parseCSGStrategyOrErr(raw string) (agentproto.CSGReplayStrategy, error) {
	strat, ok := agentproto.ParseCSGReplayStrategy(strings.TrimSpace(raw))
	if !ok {
		return "", fmt.Errorf("400 Bad Request: 非法的 csg_strategy 值 %q", raw)
	}
	return strat, nil
}

func collectReplayTargetServices(
	db *gorm.DB,
	strat agentproto.CSGReplayStrategy,
	archProcID uint64,
	ingressSvcNames []string,
	egressRecords []models.TrafficRecord,
	scopedMocks map[uint64][]agentproto.MockRuleDownstream,
	snapshots []agentproto.CSGSnapshotRecord,
) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(name string) {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return
		}
		if _, ok := seen[trimmed]; !ok {
			seen[trimmed] = struct{}{}
			out = append(out, trimmed)
		}
	}

	if strat == agentproto.CSGStrategyClientInject {
		for _, n := range ingressSvcNames {
			add(n)
		}
		if archProcID > 0 && db != nil {
			var svcs []models.ModuleService
			_ = db.Joins("JOIN contract_modules ON contract_modules.id = contract_module_services.module_id").
				Where("contract_modules.arch_process_id = ?", archProcID).
				Find(&svcs).Error
			for _, s := range svcs {
				add(s.ServiceName)
			}
		}
		return out
	}

	// For PUB_STUB / MOCK_SERVER, target services are downstream dependencies.
	for _, eg := range egressRecords {
		add(eg.ServiceName)
	}
	for _, list := range scopedMocks {
		for _, rule := range list {
			if len(rule.ReqPacket) >= crosrpc.CommHeadLen {
				if parsed, err := crosrpc.ParsePacket(rule.ReqPacket); err == nil && parsed.Req != nil {
					add(parsed.Req.ServiceName)
				}
			}
		}
	}
	for _, snap := range snapshots {
		add(snap.ServiceName)
	}
	if archProcID > 0 && db != nil {
		var mods []models.Module
		if err := db.Where("arch_process_id = ?", archProcID).Find(&mods).Error; err == nil && len(mods) > 0 {
			modIDs := make([]uint64, 0, len(mods))
			for _, m := range mods {
				modIDs = append(modIDs, m.ID)
			}
			var deps []models.ModuleDependency
			if err := db.Preload("Service").Preload("Contract").Where("consumer_module_id IN ?", modIDs).Find(&deps).Error; err == nil {
				for _, d := range deps {
					if d.Service != nil {
						add(d.Service.ServiceName)
					}
					if d.Contract != nil {
						add(d.Contract.ServiceName)
					}
				}
			}
		}
	}
	return out
}

func loadSessionCSGSnapshots(db *gorm.DB, sessionID *uint64) []agentproto.CSGSnapshotRecord {
	if db == nil || sessionID == nil || *sessionID == 0 {
		return nil
	}
	var sess models.RecordingSession
	if err := db.Select("id, csg_snapshots").First(&sess, *sessionID).Error; err != nil || len(sess.CSGSnapshots) == 0 {
		return nil
	}
	var snaps []agentproto.CSGSnapshotRecord
	_ = json.Unmarshal(sess.CSGSnapshots, &snaps)
	return snaps
}

func mergeSuiteItemCSGSnapshots(items []models.GoldenSuiteItem) []agentproto.CSGSnapshotRecord {
	var out []agentproto.CSGSnapshotRecord
	seen := make(map[string]struct{})
	for _, item := range items {
		if len(item.CSGSnapshots) == 0 {
			continue
		}
		var list []agentproto.CSGSnapshotRecord
		if err := json.Unmarshal(item.CSGSnapshots, &list); err != nil {
			continue
		}
		for _, snap := range list {
			key := snap.ServiceName + "|" + snap.Addr
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, snap)
		}
	}
	return out
}

func postExecuteReplayToAgent(
	ctx context.Context,
	httpClient *http.Client,
	agent *models.Agent,
	execReq agentproto.ExecuteReplayReq,
) error {
	if agent == nil || agent.Endpoint == "" {
		return errors.New("目标 Agent 端点未配置")
	}
	body, err := json.Marshal(execReq)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/agent/v1/replays/execute", agent.Endpoint)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if agent.ClaimCode != "" {
		httpReq.Header.Set(agentproto.HeaderClaimCode, agent.ClaimCode)
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("调用 Agent 失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	rawErr, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var errResp struct {
		Error       string `json:"error"`
		Code        string `json:"code"`
		Status      string `json:"status"`
		DiagMessage string `json:"diag_message"`
	}
	_ = json.Unmarshal(rawErr, &errResp)

	msg := errResp.Error
	if msg == "" {
		msg = fmt.Sprintf("Agent 返回状态码 %d", resp.StatusCode)
	}

	if resp.StatusCode == http.StatusConflict && errResp.Code == "CSG_SERVICE_ALREADY_PUBLISHED" {
		return &CSGConflictError{Code: errResp.Code, Message: msg}
	}
	if resp.StatusCode == http.StatusUnprocessableEntity || errResp.Code == "CSG_DATAPLANE_MESH_UNSUPPORTED" {
		code := errResp.Code
		if code == "" {
			code = "CSG_DATAPLANE_MESH_UNSUPPORTED"
		}
		return &CSGMeshUnsupportedError{Code: code, Message: msg}
	}
	return errors.New(msg)
}

func extractSandboxInfo(result *agentproto.ReplayTaskResultResp) ([]byte, []string) {
	if result == nil || result.CSGSandboxReport == nil {
		return nil, nil
	}
	raw, err := json.Marshal(result.CSGSandboxReport)
	if err != nil {
		return nil, result.CSGSandboxReport.SandboxSubscribedSvcs
	}
	return raw, result.CSGSandboxReport.SandboxSubscribedSvcs
}

func verifyObservedWithSandbox(
	ctx context.Context,
	db *gorm.DB,
	archProcID uint64,
	observed []agentproto.ObservedEgressCall,
	result *agentproto.ReplayTaskResultResp,
) ([]byte, []topology.ObservedViolationDTO, error) {
	sandboxJSON, sandboxSubs := extractSandboxInfo(result)
	violations, err := topology.VerifyProcessObservedCallsWithSandbox(ctx, db, archProcID, observed, sandboxSubs)
	return sandboxJSON, violations, err
}
