package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/gorm"
)

// FetchAgentCSGTopology queries GET /agent/v1/csg/topology on the target Agent
// while enforcing SHARED/PRIVATE RBAC boundaries.
func (m *Manager) FetchAgentCSGTopology(ctx context.Context, agentID uint64, csgAddr string, currentUserID uint, isAdmin bool) (*agentproto.CSGTopologyReport, error) {
	var agent models.Agent
	if err := m.db.First(&agent, agentID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrAgentNotFound
		}
		return nil, err
	}
	if agent.Scope == models.ScopePrivate && !isAdmin && (agent.OwnerID == nil || *agent.OwnerID != currentUserID) {
		return nil, ErrAgentForbidden
	}
	if agent.Status == models.AgentStatusOffline || agent.Endpoint == "" {
		return nil, fmt.Errorf("Agent %d 离线，无法探测 CSG 拓扑", agentID)
	}

	targetURL := agent.Endpoint + "/agent/v1/csg/topology"
	if csgAddr != "" {
		targetURL += "?csg_addr=" + url.QueryEscape(csgAddr)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	if agent.ClaimCode != "" {
		req.Header.Set(agentproto.HeaderClaimCode, agent.ClaimCode)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Agent CSG 拓扑失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("Agent 返回异常状态 %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var report agentproto.CSGTopologyReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return nil, fmt.Errorf("解析 Agent CSG 拓扑响应失败: %w", err)
	}
	return &report, nil
}
