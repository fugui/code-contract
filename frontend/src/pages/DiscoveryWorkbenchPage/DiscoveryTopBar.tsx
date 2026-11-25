import React from 'react';
import {
  AlertTriangle,
  Camera,
  Compass,
  GitCompare,
  Lock,
  Network,
  RefreshCw,
  Table2,
  WifiOff,
} from 'lucide-react';
import type { Agent, DiscoveryLiveRespDTO } from '../../types';

export interface DiscoveryTopBarProps {
  agents: Agent[];
  selectedAgentId: string;
  onSelectAgent: (agentId: string) => void;
  selectedCsgAddr: string;
  onSelectCsgAddr: (addr: string) => void;
  liveData: DiscoveryLiveRespDTO | null;
  loading: boolean;
  errorMsg: string;
  viewMode: 'tree' | 'graph';
  onChangeViewMode: (mode: 'tree' | 'graph') => void;
  onRefresh: () => void;
  onOpenSaveSnapshot: () => void;
  onOpenSnapshotDrawer: () => void;
}

function formatHHMM(isoStr?: string): string {
  if (!isoStr) return '--:--';
  const d = new Date(isoStr);
  if (Number.isNaN(d.getTime())) return isoStr;
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  return `${hh}:${mm}`;
}

export const DiscoveryTopBar: React.FC<DiscoveryTopBarProps> = ({
  agents,
  selectedAgentId,
  onSelectAgent,
  selectedCsgAddr,
  onSelectCsgAddr,
  liveData,
  loading,
  errorMsg,
  viewMode,
  onChangeViewMode,
  onRefresh,
  onOpenSaveSnapshot,
  onOpenSnapshotDrawer,
}) => {
  const selectedAgent = agents.find((a) => String(a.id) === String(selectedAgentId)) ?? null;
  const isPrivate =
    selectedAgent?.scope === 'PRIVATE' || liveData?.agent_scope === 'PRIVATE';
  const endpoints = liveData?.csg_endpoints ?? [];
  const activeCsgAddr =
    selectedCsgAddr ||
    liveData?.csg_addr ||
    (endpoints[0] ? `${endpoints[0].listen_ip}:${endpoints[0].listen_port}` : '');
  const isProcOnly = liveData?.discovery_mode === 'PROC_ONLY';
  const hasErrors = Boolean(liveData?.errors && liveData.errors.length > 0);

  return (
    <div className="discovery-topbar" data-testid="discovery-topbar">
      <div className="discovery-topbar-row">
        <div className="discovery-topbar-env">
          <span className="discovery-title-group">
            <Compass size={17} style={{ color: 'var(--color-primary)' }} />
            <span>现网发现工作台</span>
          </span>

          <select
            aria-label="选择目标 Agent"
            data-testid="discovery-agent-select"
            className="discovery-select"
            value={selectedAgentId}
            onChange={(e) => onSelectAgent(e.target.value)}
          >
            {agents.length === 0 && <option value="">暂无可用 Agent</option>}
            {agents.map((ag) => (
              <option key={ag.id} value={String(ag.id)}>
                {ag.name} ({ag.scope === 'PRIVATE' ? '私有' : '共享'} ·{' '}
                {ag.status === 'ONLINE' ? '在线' : '离线'})
              </option>
            ))}
          </select>

          {selectedAgent && (
            <span
              data-testid="discovery-agent-scope-badge"
              className={`discovery-scope-badge ${
                isPrivate
                  ? 'discovery-scope-badge--private'
                  : 'discovery-scope-badge--shared'
              }`}
            >
              {isPrivate && <Lock size={11} />}
              {isPrivate ? '私有 · 仅本人可见' : 'SHARED · 团队共享'}
            </span>
          )}

          <select
            aria-label="选择 CSG 控制面地址"
            data-testid="discovery-csg-select"
            className="discovery-select discovery-select--mono"
            value={activeCsgAddr}
            disabled={endpoints.length === 0}
            onChange={(e) => onSelectCsgAddr(e.target.value)}
          >
            {endpoints.length === 0 ? (
              <option value="">{activeCsgAddr || '自动探测 CSG 控制面'}</option>
            ) : (
              endpoints.map((ep, idx) => {
                const addr = `${ep.listen_ip}:${ep.listen_port}`;
                return (
                  <option key={`${addr}-${idx}`} value={addr}>
                    {addr} ({ep.exe_name ? `${ep.exe_name} · ` : ''}
                    {ep.source})
                  </option>
                );
              })
            )}
          </select>
        </div>

        <div className="discovery-topbar-actions">
          <div className="discovery-mode-switch" role="tablist" aria-label="视图切换">
            <button
              type="button"
              role="tab"
              aria-selected={viewMode === 'tree'}
              data-testid="discovery-mode-tree"
              className={`discovery-mode-btn ${
                viewMode === 'tree' ? 'discovery-mode-btn--active' : ''
              }`}
              onClick={() => onChangeViewMode('tree')}
            >
              <Table2 size={14} />
              <span>进程树表</span>
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={viewMode === 'graph'}
              data-testid="discovery-mode-graph"
              className={`discovery-mode-btn ${
                viewMode === 'graph' ? 'discovery-mode-btn--active' : ''
              }`}
              onClick={() => onChangeViewMode('graph')}
            >
              <Network size={14} />
              <span>实时依赖图</span>
            </button>
          </div>

          <button
            type="button"
            className="code-btn code-btn--secondary code-btn--sm"
            data-testid="discovery-refresh-btn"
            disabled={!selectedAgentId || loading}
            onClick={onRefresh}
          >
            <RefreshCw size={13} className={loading ? 'discovery-spin' : undefined} />
            <span>刷新</span>
          </button>

          <button
            type="button"
            className="code-btn code-btn--secondary code-btn--sm"
            data-testid="discovery-save-snapshot-btn"
            disabled={!selectedAgentId || !liveData}
            onClick={onOpenSaveSnapshot}
          >
            <Camera size={13} />
            <span>保存快照</span>
          </button>

          <button
            type="button"
            className="code-btn code-btn--primary code-btn--sm"
            data-testid="discovery-open-diff-btn"
            disabled={!selectedAgentId}
            onClick={onOpenSnapshotDrawer}
          >
            <GitCompare size={13} />
            <span>快照对比</span>
          </button>
        </div>
      </div>

      {liveData?.stale && (
        <div
          className="discovery-banner discovery-banner--warning"
          data-testid="discovery-stale-banner"
        >
          <span className="discovery-banner-lead">
            <WifiOff size={14} />
            <span>
              离线 · 采集于 {formatHHMM(liveData.captured_at)}（当前展示最近持久化快照）
            </span>
          </span>
          {liveData.stale_reason && <span>原因: {liveData.stale_reason}</span>}
        </div>
      )}

      {(isProcOnly || hasErrors) && (
        <div
          className="discovery-banner discovery-banner--warning"
          data-testid="discovery-proconly-banner"
        >
          <span className="discovery-banner-lead">
            <AlertTriangle size={14} />
            <span>
              {isProcOnly
                ? 'PROC_ONLY 降级诊断：CSG rpcmon 未返回服务目录，当前仅基于宿主机 /proc 观测'
                : 'CSG 探测存在告警信息'}
            </span>
          </span>
          {hasErrors && (
            <span className="discovery-mono">{liveData?.errors?.slice(0, 2).join(' | ')}</span>
          )}
        </div>
      )}

      {errorMsg && (
        <div
          className="discovery-banner discovery-banner--danger"
          data-testid="discovery-error-banner"
        >
          <span className="discovery-banner-lead">
            <AlertTriangle size={14} />
            <span>{errorMsg}</span>
          </span>
        </div>
      )}
    </div>
  );
};
