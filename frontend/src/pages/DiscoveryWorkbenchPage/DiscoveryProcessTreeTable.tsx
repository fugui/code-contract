import React, { useMemo, useState } from 'react';
import { ChevronDown, ChevronRight, Search, Server, Globe } from 'lucide-react';
import type {
  DiscoveryProcessGroupDTO,
  EnrollStatus,
  IdentityMatchStatus,
  ObservedProcessRole,
  ProviderKind,
} from '../../types';

export interface DiscoveryProcessTreeTableProps {
  localProcesses: DiscoveryProcessGroupDTO[];
  remoteLdcProcesses: DiscoveryProcessGroupDTO[];
  scopeTab: 'local' | 'ldc';
  onChangeScopeTab: (tab: 'local' | 'ldc') => void;
  searchQuery: string;
  onChangeSearchQuery: (q: string) => void;
  selectedKeys?: Set<string>;
  onToggleSelectKey?: (processKey: string) => void;
  onToggleSelectAll?: (keys: string[], checked: boolean) => void;
}

const ROLE_META: Record<ObservedProcessRole, { label: string; cls: string }> = {
  SERVER: { label: 'SERVER', cls: 'discovery-pill--primary' },
  CLIENT_ONLY: { label: 'CLIENT_ONLY', cls: 'discovery-pill--info' },
  DUAL: { label: 'DUAL', cls: 'discovery-pill--success' },
};

const ENROLL_META: Record<EnrollStatus, { label: string; cls: string }> = {
  UNENROLLED: { label: '未纳管', cls: 'discovery-pill--neutral' },
  ENROLLED_DISARMED: { label: '已纳管·未布防', cls: 'discovery-pill--warning' },
  ARMED_READY: { label: '已布防·就绪', cls: 'discovery-pill--success' },
  ARMED_NEED_RECONNECT: { label: '已布防·待重连', cls: 'discovery-pill--warning' },
};

const IDENTITY_META: Record<IdentityMatchStatus, { label: string; cls: string }> = {
  MATCHED: { label: '已匹配', cls: 'discovery-pill--success' },
  AMBIGUOUS: { label: '歧义待选', cls: 'discovery-pill--warning' },
  LOW_CONFIDENCE: { label: '低置信', cls: 'discovery-pill--danger' },
  NEW: { label: '新发现', cls: 'discovery-pill--info' },
};

const PROVIDER_KIND_LABELS: Record<ProviderKind, string> = {
  LOCAL_ENROLLED: '本机已纳管',
  LOCAL_UNENROLLED: '本机未纳管',
  REMOTE_LDC: '跨板 LDC 远端',
  OFFLINE: '离线 / 未在线',
};

function matchesProcessGroup(group: DiscoveryProcessGroupDTO, kw: string): {
  matched: boolean;
  childMatched: boolean;
} {
  if (!kw) return { matched: true, childMatched: false };
  const lower = kw.toLowerCase();
  const parentFields = [
    group.proc_name,
    group.exe_name,
    group.cmdline,
    group.arch_process_code,
    group.arch_process_name,
    group.remote_node,
    ...group.pids.map(String),
  ];
  const parentHit = parentFields.some((f) => f && f.toLowerCase().includes(lower));

  const childHit =
    group.services.some(
      (s) =>
        s.service_name.toLowerCase().includes(lower) ||
        s.raw_service_name.toLowerCase().includes(lower) ||
        (s.proto_service_name && s.proto_service_name.toLowerCase().includes(lower)) ||
        (s.ssot_module_name && s.ssot_module_name.toLowerCase().includes(lower))
    ) ||
    group.subscriptions.some(
      (sub) =>
        sub.service_name.toLowerCase().includes(lower) ||
        (sub.provider_proc_name && sub.provider_proc_name.toLowerCase().includes(lower))
    );

  return { matched: parentHit || childHit, childMatched: childHit };
}

export const DiscoveryProcessTreeTable: React.FC<DiscoveryProcessTreeTableProps> = ({
  localProcesses,
  remoteLdcProcesses,
  scopeTab,
  onChangeScopeTab,
  searchQuery,
  onChangeSearchQuery,
  selectedKeys,
  onToggleSelectKey,
  onToggleSelectAll,
}) => {
  const [roleFilter, setRoleFilter] = useState<'ALL' | ObservedProcessRole>('ALL');
  const [transportFilter, setTransportFilter] = useState<'ALL' | 'TCP' | 'MESH'>('ALL');
  const [manualExpanded, setManualExpanded] = useState<Record<string, boolean>>({});

  const activeList = scopeTab === 'ldc' ? remoteLdcProcesses : localProcesses;

  const filtered = useMemo(() => {
    const kw = searchQuery.trim();
    const results: Array<{ group: DiscoveryProcessGroupDTO; childMatched: boolean }> = [];
    for (const g of activeList) {
      if (roleFilter !== 'ALL' && g.observed_role !== roleFilter) continue;
      if (
        transportFilter !== 'ALL' &&
        !g.observed_transports.includes(transportFilter)
      ) {
        continue;
      }
      const m = matchesProcessGroup(g, kw);
      if (m.matched) {
        results.push({ group: g, childMatched: m.childMatched });
      }
    }
    return results;
  }, [activeList, roleFilter, transportFilter, searchQuery]);

  const toggleExpand = (key: string) => {
    setManualExpanded((prev) => ({ ...prev, [key]: !prev[key] }));
  };

  return (
    <div className="discovery-panel" data-testid="discovery-process-tree-panel">
      <div className="discovery-toolbar">
        <div className="discovery-tabs" role="tablist" aria-label="进程作用域">
          <button
            type="button"
            role="tab"
            aria-selected={scopeTab === 'local'}
            data-testid="discovery-tab-local"
            className={`discovery-tab ${scopeTab === 'local' ? 'discovery-tab--active' : ''}`}
            onClick={() => onChangeScopeTab('local')}
          >
            <Server size={13} />
            <span>本机进程</span>
            <span className="discovery-tab-count">{localProcesses.length}</span>
          </button>

          <button
            type="button"
            role="tab"
            aria-selected={scopeTab === 'ldc'}
            data-testid="discovery-tab-ldc"
            className={`discovery-tab ${scopeTab === 'ldc' ? 'discovery-tab--active' : ''}`}
            onClick={() => onChangeScopeTab('ldc')}
          >
            <Globe size={13} />
            <span>跨板 LDC 远端节点（只读）</span>
            <span className="discovery-tab-count">{remoteLdcProcesses.length}</span>
          </button>
        </div>

        <div className="discovery-filters">
          <div className="discovery-search-wrap">
            <Search size={13} className="discovery-search-icon" />
            <input
              type="text"
              className="discovery-search-input"
              data-testid="discovery-search-input"
              value={searchQuery}
              onChange={(e) => onChangeSearchQuery(e.target.value)}
              placeholder="搜索进程名、服务名、Proto 或 PID..."
            />
          </div>

          <select
            aria-label="按观测角色过滤"
            className="discovery-select"
            style={{ minWidth: 130 }}
            value={roleFilter}
            onChange={(e) => setRoleFilter(e.target.value as 'ALL' | ObservedProcessRole)}
          >
            <option value="ALL">全部角色</option>
            <option value="SERVER">SERVER</option>
            <option value="CLIENT_ONLY">CLIENT_ONLY (纯客户端)</option>
            <option value="DUAL">DUAL</option>
          </select>

          <select
            aria-label="按传输制式过滤"
            className="discovery-select"
            style={{ minWidth: 120 }}
            value={transportFilter}
            onChange={(e) => setTransportFilter(e.target.value as 'ALL' | 'TCP' | 'MESH')}
          >
            <option value="ALL">全部制式</option>
            <option value="TCP">TCP</option>
            <option value="MESH">MESH</option>
          </select>
        </div>
      </div>

      {filtered.length === 0 ? (
        <div className="discovery-empty" data-testid="discovery-tree-empty">
          当前过滤条件下暂无匹配进程
        </div>
      ) : (
        <div className="discovery-table-wrap">
          <table className="discovery-table" data-testid="discovery-process-table">
            <thead>
              <tr>
                {scopeTab !== 'ldc' && (
                  <th style={{ width: 34 }}>
                    <input
                      type="checkbox"
                      aria-label="全选当前本机进程"
                      checked={
                        filtered.length > 0 &&
                        filtered.every(({ group }) => Boolean(selectedKeys?.has(group.process_key)))
                      }
                      onChange={(e) => {
                        if (onToggleSelectAll) {
                          onToggleSelectAll(
                            filtered.map(({ group }) => group.process_key),
                            e.target.checked
                          );
                        }
                      }}
                    />
                  </th>
                )}
                <th>进程主轴 / 多实例 PID</th>
                <th>观测角色</th>
                <th>纳管状态</th>
                <th>身份匹配</th>
                <th>传输制式</th>
                <th>提供服务数</th>
                <th>订阅下游数</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map(({ group, childMatched }) => {
                const isExpanded =
                  manualExpanded[group.process_key] ??
                  (Boolean(searchQuery.trim()) && childMatched);
                const roleInfo = ROLE_META[group.observed_role] ?? ROLE_META.SERVER;
                const enrollInfo = ENROLL_META[group.enroll_status] ?? ENROLL_META.UNENROLLED;
                const idInfo = IDENTITY_META[group.identity_match] ?? IDENTITY_META.NEW;
                const pidBadgeText =
                  group.pids.length > 1
                    ? `${group.pids.length} PIDs · ${group.pids.join(', ')}`
                    : group.pids.length === 1
                    ? `PID ${group.pids[0]}`
                    : 'PID -';

                return (
                  <React.Fragment key={group.process_key}>
                    <tr
                      className="discovery-proc-row"
                      data-testid={`discovery-proc-row-${group.process_key}`}
                    >
                      {scopeTab !== 'ldc' && (
                        <td>
                          <input
                            type="checkbox"
                            aria-label={`选择进程 ${group.proc_name}`}
                            data-testid={`discovery-checkbox-${group.process_key}`}
                            checked={Boolean(selectedKeys?.has(group.process_key))}
                            onChange={() => {
                              if (onToggleSelectKey) {
                                onToggleSelectKey(group.process_key);
                              }
                            }}
                          />
                        </td>
                      )}
                      <td>
                        <div className="discovery-proc-name-cell">
                          <div className="discovery-proc-title-line">
                            <button
                              type="button"
                              className="discovery-expand-btn"
                              aria-label={isExpanded ? '收起进程明细' : '展开进程明细'}
                              data-testid={`discovery-expand-${group.process_key}`}
                              onClick={() => toggleExpand(group.process_key)}
                            >
                              {isExpanded ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                            </button>
                            <span className="discovery-proc-name">{group.proc_name}</span>
                            <span
                              className="discovery-pill discovery-pill--neutral discovery-mono"
                              data-testid={`discovery-pid-badge-${group.process_key}`}
                            >
                              {pidBadgeText}
                            </span>
                            {group.is_remote_ldc && group.remote_node && (
                              <span className="discovery-pill discovery-pill--info">
                                {`远端 ${group.remote_node} (只读)`}
                              </span>
                            )}
                          </div>
                          {group.cmdline && (
                            <div className="discovery-cmdline" title={group.cmdline}>
                              {group.cmdline}
                            </div>
                          )}
                        </div>
                      </td>
                      <td>
                        <span className={`discovery-pill ${roleInfo.cls}`}>{roleInfo.label}</span>
                      </td>
                      <td>
                        {group.is_remote_ldc ? (
                          <span className="discovery-pill discovery-pill--neutral">远端只读</span>
                        ) : (
                          <span className={`discovery-pill ${enrollInfo.cls}`}>
                            {enrollInfo.label}
                          </span>
                        )}
                      </td>
                      <td>
                        <span className={`discovery-pill ${idInfo.cls}`}>
                          {idInfo.label}
                          {group.arch_process_code ? ` (${group.arch_process_code})` : ''}
                        </span>
                      </td>
                      <td>
                        <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                          {group.observed_transports.length === 0 ? (
                            <span className="discovery-pill discovery-pill--neutral">-</span>
                          ) : (
                            group.observed_transports.map((tr) => (
                              <span
                                key={tr}
                                className={`discovery-pill ${
                                  tr === 'MESH'
                                    ? 'discovery-pill--primary'
                                    : 'discovery-pill--info'
                                }`}
                              >
                                {tr}
                              </span>
                            ))
                          )}
                        </div>
                      </td>
                      <td>
                        <span className="discovery-mono">
                          {group.services.length}
                          {group.unclaimed_service_count > 0 &&
                            ` · ${group.unclaimed_service_count} 待认领`}
                        </span>
                      </td>
                      <td>
                        <span className="discovery-mono">{group.subscriptions.length}</span>
                      </td>
                    </tr>

                    {isExpanded && (
                      <tr
                        className="discovery-child-row"
                        data-testid={`discovery-child-row-${group.process_key}`}
                      >
                        <td colSpan={scopeTab !== 'ldc' ? 8 : 7}>
                          <div className="discovery-child-section">
                            {group.services.length > 0 && (
                              <div className="discovery-child-block">
                                <div className="discovery-child-heading">
                                  发布服务 ({group.services.length})
                                </div>
                                <table className="discovery-subtable">
                                  <thead>
                                    <tr>
                                      <th>CSG 64B 服务名</th>
                                      <th>制式 / 监听端点</th>
                                      <th>Proto 撮合</th>
                                      <th>逻辑认领</th>
                                      <th>多宿主</th>
                                      <th>订阅方数</th>
                                    </tr>
                                  </thead>
                                  <tbody>
                                    {group.services.map((svc) => (
                                      <tr key={svc.service_name}>
                                        <td className="discovery-mono">{svc.service_name}</td>
                                        <td className="discovery-mono">
                                          {svc.transport}{' '}
                                          {svc.endpoints.length > 0
                                            ? `· ${svc.endpoints.join(', ')}`
                                            : ''}
                                        </td>
                                        <td>
                                          <span
                                            className={`discovery-pill ${
                                              svc.proto_match_status === 'EXACT'
                                                ? 'discovery-pill--success'
                                                : 'discovery-pill--warning'
                                            }`}
                                          >
                                            {svc.proto_match_status === 'EXACT'
                                              ? `已匹配 (${svc.proto_service_name})`
                                              : '裸奔 / 未匹配'}
                                          </span>
                                        </td>
                                        <td>
                                          <span
                                            className={`discovery-pill ${
                                              svc.claim_status === 'CLAIMED'
                                                ? 'discovery-pill--success'
                                                : 'discovery-pill--neutral'
                                            }`}
                                          >
                                            {svc.claim_status === 'CLAIMED'
                                              ? `已认领 (${svc.ssot_module_name || svc.ssot_module_code || '模块'})`
                                              : '待认领'}
                                          </span>
                                        </td>
                                        <td>
                                          {svc.is_multi_host ? (
                                            <span className="discovery-pill discovery-pill--warning">
                                              多宿主
                                            </span>
                                          ) : (
                                            '单宿主'
                                          )}
                                        </td>
                                        <td className="discovery-mono">{svc.subscriber_count}</td>
                                      </tr>
                                    ))}
                                  </tbody>
                                </table>
                              </div>
                            )}

                            {group.subscriptions.length > 0 && (
                              <div className="discovery-child-block">
                                <div className="discovery-child-heading">
                                  下游订阅 ({group.subscriptions.length})
                                </div>
                                <table className="discovery-subtable">
                                  <thead>
                                    <tr>
                                      <th>订阅目标服务</th>
                                      <th>提供方解析</th>
                                      <th>提供方进程 / 远端节点</th>
                                      <th>端点</th>
                                    </tr>
                                  </thead>
                                  <tbody>
                                    {group.subscriptions.map((sub) => (
                                      <tr key={sub.service_name}>
                                        <td className="discovery-mono">{sub.service_name}</td>
                                        <td>
                                          <span
                                            className={`discovery-pill ${
                                              sub.provider_kind === 'LOCAL_ENROLLED'
                                                ? 'discovery-pill--success'
                                                : sub.provider_kind === 'REMOTE_LDC'
                                                ? 'discovery-pill--info'
                                                : sub.provider_kind === 'OFFLINE'
                                                ? 'discovery-pill--danger'
                                                : 'discovery-pill--warning'
                                            }`}
                                          >
                                            {PROVIDER_KIND_LABELS[sub.provider_kind] ||
                                              sub.provider_kind}
                                          </span>
                                        </td>
                                        <td>
                                          {sub.provider_proc_name || sub.remote_node || '-'}
                                        </td>
                                        <td className="discovery-mono">
                                          {sub.provider_endpoint || '-'}
                                        </td>
                                      </tr>
                                    ))}
                                  </tbody>
                                </table>
                              </div>
                            )}

                            {group.services.length === 0 && group.subscriptions.length === 0 && (
                              <div className="discovery-empty" style={{ padding: 12 }}>
                                该进程暂未发布或订阅任何 CSG 服务
                              </div>
                            )}
                          </div>
                        </td>
                      </tr>
                    )}
                  </React.Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
};
