import React, { useCallback, useEffect, useState } from 'react';
import { Drawer } from '@code/common';
import { useModuleUser } from '../../context/ModuleUserContext';
import {
  AlertTriangle,
  CheckCircle2,
  Compass,
  Download,
  Plus,
  RefreshCw,
  ShieldAlert,
} from 'lucide-react';
import * as agentApi from '../../api/agent';
import * as archApi from '../../api/arch';
import { useToast } from '../../utils/toast';
import type {
  Agent,
  CSGReconcileState,
  CSGTopologyReconcileResp,
  ReconciledCSGService,
  Subsystem,
} from '../../types';

export interface CSGBlueprintImportDrawerProps {
  open: boolean;
  onClose: () => void;
  agent: Agent;
  onImported?: () => void;
}

export interface CSGBlueprintImportContentProps {
  agent: Agent;
  initialReport?: CSGTopologyReconcileResp | null;
  initialSubsystems?: Subsystem[];
  onImported?: () => void;
  onClose?: () => void;
}

const STATE_BADGE_MAP: Record<
  CSGReconcileState,
  { label: string; color: string; bg: string; border: string }
> = {
  MATCHED: {
    label: '已纳管 (MATCHED)',
    color: 'var(--color-success)',
    bg: 'color-mix(in srgb, var(--color-success) 12%, transparent)',
    border: 'var(--color-success)',
  },
  UNDECLARED_IN_SSOT: {
    label: '待导入 (UNDECLARED_IN_SSOT)',
    color: 'var(--color-warning)',
    bg: 'color-mix(in srgb, var(--color-warning) 14%, transparent)',
    border: 'var(--color-warning)',
  },
  MISSING_IN_LIVE: {
    label: '现网未上线 (MISSING_IN_LIVE)',
    color: 'var(--color-danger)',
    bg: 'color-mix(in srgb, var(--color-danger) 12%, transparent)',
    border: 'var(--color-danger)',
  },
};

function buildDefaultSubsystemCode(agentName?: string): string {
  const normalized = (agentName || '')
    .trim()
    .toUpperCase()
    .replace(/[^A-Z0-9_-]+/g, '_')
    .replace(/^[_-]+|[_-]+$/g, '');
  if (normalized.length >= 2) {
    return normalized.slice(0, 64);
  }
  return 'CSG_AUTO_SYS';
}

export const CSGBlueprintImportContent: React.FC<CSGBlueprintImportContentProps> = ({
  agent,
  initialReport = null,
  initialSubsystems,
  onImported,
  onClose,
}) => {
  const toast = useToast();
  const { user } = useModuleUser();
  const isAdmin =
    Boolean(user?.is_module_admin) ||
    Boolean(user?.is_super_admin) ||
    Boolean(user?.roles?.includes('contract_admin')) ||
    Boolean(user?.roles?.includes('super_admin'));

  const hasInitialSubsystems = initialSubsystems !== undefined;
  const resolvedInitialSubsystems = initialSubsystems ?? [];

  const [csgAddrInput, setCsgAddrInput] = useState('');
  const [loading, setLoading] = useState(false);
  const [errorMsg, setErrorMsg] = useState<string | null>(null);
  const [report, setReport] = useState<CSGTopologyReconcileResp | null>(initialReport);
  const [subsystems, setSubsystems] = useState<Subsystem[]>(resolvedInitialSubsystems);
  const [subsystemsLoaded, setSubsystemsLoaded] = useState<boolean>(hasInitialSubsystems);
  const [subsystemsError, setSubsystemsError] = useState<string | null>(null);
  const [selectedSubsystemId, setSelectedSubsystemId] = useState<string>(() =>
    resolvedInitialSubsystems.length > 0 ? String(resolvedInitialSubsystems[0].id) : ''
  );
  const [showCreateSubsystem, setShowCreateSubsystem] = useState<boolean>(false);
  const [creatingSubsystem, setCreatingSubsystem] = useState<boolean>(false);
  const [newSubCode, setNewSubCode] = useState<string>(() => buildDefaultSubsystemCode(agent.name));
  const [newSubName, setNewSubName] = useState<string>(() =>
    agent.name?.trim() ? `${agent.name.trim()} 子系统` : 'CSG 自动发现子系统'
  );
  const [selectedServices, setSelectedServices] = useState<Set<string>>(() => {
    const init = new Set<string>();
    initialReport?.services?.forEach((svc) => {
      if (svc.state === 'UNDECLARED_IN_SSOT') {
        init.add(svc.service_name);
      }
    });
    return init;
  });
  const [importDeps, setImportDeps] = useState(true);
  const [importing, setImporting] = useState(false);

  const loadSubsystems = useCallback(async () => {
    setSubsystemsError(null);
    try {
      const res = await archApi.listSubsystems({ page: 1, page_size: 100 });
      const items = res.items || [];
      setSubsystems(items);
      setSubsystemsLoaded(true);
      if (items.length > 0) {
        setSelectedSubsystemId((prev) => prev || String(items[0].id));
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setSubsystemsError(msg);
      setSubsystemsLoaded(true);
    }
  }, []);

  const loadTopology = useCallback(
    async (explicitAddr?: string) => {
      if (!agent?.id) return;
      setLoading(true);
      setErrorMsg(null);
      try {
        const res = await agentApi.getAgentCSGTopology(
          agent.id,
          explicitAddr !== undefined ? explicitAddr : csgAddrInput.trim() || undefined
        );
        setReport(res);
        const undeclared = new Set<string>();
        (res.services || []).forEach((s) => {
          if (s.state === 'UNDECLARED_IN_SSOT') {
            undeclared.add(s.service_name);
          }
        });
        setSelectedServices(undeclared);
      } catch (err: unknown) {
        const msg = err instanceof Error ? err.message : String(err);
        setErrorMsg(msg);
      } finally {
        setLoading(false);
      }
    },
    [agent?.id, csgAddrInput]
  );

  useEffect(() => {
    if (!initialReport && agent?.id) {
      void loadTopology();
    }
    if (!hasInitialSubsystems) {
      void loadSubsystems();
    }
  }, [agent?.id, initialReport, hasInitialSubsystems, loadTopology, loadSubsystems]);

  const handleQuickCreateSubsystem = async () => {
    if (!isAdmin || creatingSubsystem) return;
    const code = newSubCode.trim().toUpperCase();
    const name = newSubName.trim();
    if (!/^[A-Z0-9_-]{2,64}$/.test(code)) {
      toast.error('子系统编码格式不合法（须为 2~64 位大写字母、数字、下划线或中划线）');
      return;
    }
    if (name.length === 0 || name.length > 128) {
      toast.error('子系统名称不能为空且长度不得超过 128 个字符');
      return;
    }
    setCreatingSubsystem(true);
    setErrorMsg(null);
    try {
      const created = await archApi.createSubsystem({
        code,
        name,
        description: '由 CSG 蓝图导入抽屉快速创建',
      });
      setSubsystems((prev) => [...prev, created]);
      setSelectedSubsystemId(String(created.id));
      setShowCreateSubsystem(false);
      setSubsystemsError(null);
      setSubsystemsLoaded(true);
      toast.success(`已创建并选中子系统：${created.name} (${created.code})`);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setErrorMsg(msg);
      toast.error(`创建子系统失败: ${msg}`);
    } finally {
      setCreatingSubsystem(false);
    }
  };

  const toggleService = (serviceName: string) => {
    setSelectedServices((prev) => {
      const next = new Set(prev);
      if (next.has(serviceName)) {
        next.delete(serviceName);
      } else {
        next.add(serviceName);
      }
      return next;
    });
  };

  const handleImport = async () => {
    if (!isAdmin) return;
    if (!selectedSubsystemId) {
      toast.error('请先选择要导入归属的目标子系统');
      return;
    }
    setImporting(true);
    setErrorMsg(null);
    try {
      const res = await agentApi.importAgentCSGBlueprint(agent.id, {
        subsystem_id: Number(selectedSubsystemId),
        csg_addr: csgAddrInput.trim() || undefined,
        service_names: Array.from(selectedServices),
        import_dependencies: importDeps,
      });
      const createdProcs = res.created_processes ?? res.created_arch_processes ?? 0;
      toast.success(
        `CSG 蓝图导入完成：新增进程 ${createdProcs}、服务 ${res.created_services}、依赖 ${res.created_dependencies}`
      );
      await loadTopology();
      onImported?.();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setErrorMsg(msg);
      toast.error(`CSG 蓝图导入失败: ${msg}`);
    } finally {
      setImporting(false);
    }
  };

  const renderStateBadge = (state: CSGReconcileState) => {
    const cfg = STATE_BADGE_MAP[state] || STATE_BADGE_MAP.UNDECLARED_IN_SSOT;
    return (
      <span
        data-testid={`csg-state-badge-${state}`}
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          padding: '2px 8px',
          borderRadius: '4px',
          fontSize: '12px',
          fontWeight: 500,
          color: cfg.color,
          backgroundColor: cfg.bg,
          border: `1px solid ${cfg.border}`,
        }}
      >
        {cfg.label}
      </span>
    );
  };

  const services: ReconciledCSGService[] = report?.services || [];
  const endpoints = report?.csg_endpoints || [];
  const discoveryErrors = report?.errors || [];
  const liveServiceCount = report
    ? report.summary.live_service_count ??
      report.summary.matched_count + report.summary.undeclared_count
    : 0;
  const liveSubCount = report
    ? report.summary.live_sub_count ??
      (report.summary.sub_matched_count ?? 0) + (report.summary.sub_undeclared_count ?? 0)
    : 0;
  const isProcOnlyDegraded =
    Boolean(report) &&
    (report?.discovery_mode || 'PROC_ONLY') === 'PROC_ONLY' &&
    liveServiceCount === 0;
  const isMeshOnlyMonitorErr = discoveryErrors.some((e) => e.includes('dophi_mesh://'));

  const isNoSubsystems = subsystemsLoaded && subsystems.length === 0 && !subsystemsError;
  const showQuickCreateForm = showCreateSubsystem || isNoSubsystems;
  const importDisabled = importing || selectedServices.size === 0 || !selectedSubsystemId;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '16px', padding: '4px 0' }}>
      <div
        style={{
          display: 'flex',
          flexWrap: 'wrap',
          gap: '8px',
          alignItems: 'center',
          padding: '12px',
          borderRadius: '8px',
          backgroundColor: 'var(--color-bg-muted)',
          border: '1px solid var(--color-border-subtle)',
        }}
      >
        <Compass size={16} style={{ color: 'var(--color-primary)' }} />
        <span style={{ fontSize: '13px', fontWeight: 600, color: 'var(--color-text-primary)' }}>
          目标节点：{agent.name}
        </span>
        <input
          type="text"
          value={csgAddrInput}
          onChange={(e) => setCsgAddrInput(e.target.value)}
          placeholder="可选指定 CSG dlink 地址 (如 127.0.0.1:31806)"
          data-testid="csg-addr-probe-input"
          style={{
            flex: 1,
            minWidth: '220px',
            padding: '6px 10px',
            borderRadius: '6px',
            border: '1px solid var(--color-border-primary)',
            backgroundColor: 'var(--color-bg-input)',
            color: 'var(--color-text-primary)',
            fontSize: '13px',
          }}
        />
        <button
          type="button"
          onClick={() => void loadTopology()}
          disabled={loading}
          data-testid="csg-refresh-topology-btn"
          style={{
            display: 'inline-flex',
            alignItems: 'center',
            gap: '6px',
            padding: '6px 12px',
            borderRadius: '6px',
            border: '1px solid var(--color-border-primary)',
            backgroundColor: 'var(--color-bg-surface)',
            color: 'var(--color-text-primary)',
            cursor: loading ? 'wait' : 'pointer',
            fontSize: '13px',
          }}
        >
          <RefreshCw size={14} />
          {loading ? '探测中...' : '重新探测现网拓扑'}
        </button>
      </div>

      {errorMsg && (
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: '8px',
            padding: '10px 12px',
            borderRadius: '6px',
            backgroundColor: 'color-mix(in srgb, var(--color-danger) 12%, transparent)',
            border: '1px solid var(--color-danger)',
            color: 'var(--color-danger)',
            fontSize: '13px',
          }}
        >
          <ShieldAlert size={16} />
          <span>{errorMsg}</span>
        </div>
      )}

      {report && (
        <>
          {endpoints.length > 0 && (
            <div
              data-testid="csg-endpoints-selector"
              style={{
                display: 'flex',
                flexWrap: 'wrap',
                alignItems: 'center',
                gap: '6px',
                padding: '8px 12px',
                borderRadius: '6px',
                border: '1px solid var(--color-border-subtle)',
                backgroundColor: 'var(--color-bg-muted)',
                fontSize: '12px',
              }}
            >
              <span style={{ color: 'var(--color-text-secondary)', fontWeight: 500 }}>
                候选 CSG 控制面 ({endpoints.length})：
              </span>
              {endpoints.map((ep, idx) => {
                const epAddr = `${ep.listen_ip}:${ep.listen_port}`;
                const active =
                  csgAddrInput.trim() === epAddr || (csgAddrInput.trim() === '' && idx === 0);
                return (
                  <button
                    key={`${epAddr}-${ep.source}-${idx}`}
                    type="button"
                    disabled={loading}
                    onClick={() => {
                      setCsgAddrInput(epAddr);
                      void loadTopology(epAddr);
                    }}
                    style={{
                      padding: '3px 8px',
                      borderRadius: '4px',
                      border: `1px solid ${
                        active ? 'var(--color-primary)' : 'var(--color-border-primary)'
                      }`,
                      backgroundColor: active
                        ? 'color-mix(in srgb, var(--color-primary) 14%, transparent)'
                        : 'var(--color-bg-surface)',
                      color: active ? 'var(--color-primary)' : 'var(--color-text-primary)',
                      fontFamily: 'monospace',
                      fontSize: '12px',
                      cursor: loading ? 'wait' : 'pointer',
                    }}
                  >
                    {epAddr} ({ep.exe_name ? `${ep.exe_name} · ` : ''}{ep.source})
                  </button>
                );
              })}
            </div>
          )}

          {discoveryErrors.length > 0 && (
            <div
              data-testid="csg-discovery-errors-banner"
              style={{
                display: 'flex',
                flexDirection: 'column',
                gap: '4px',
                padding: '10px 12px',
                borderRadius: '6px',
                backgroundColor: 'color-mix(in srgb, var(--color-warning) 12%, transparent)',
                border: '1px solid var(--color-warning)',
                color: 'var(--color-warning)',
                fontSize: '12px',
              }}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: '6px', fontWeight: 600 }}>
                <AlertTriangle size={14} />
                <span>CSG 发现告警 / 降级原因</span>
              </div>
              {discoveryErrors.map((errItem, idx) => (
                <div key={idx} style={{ fontFamily: 'monospace', wordBreak: 'break-all' }}>
                  {errItem}
                </div>
              ))}
            </div>
          )}

          {isProcOnlyDegraded && (
            <div
              data-testid="csg-proc-only-degraded-banner"
              style={{
                display: 'flex',
                flexDirection: 'column',
                gap: '6px',
                padding: '12px',
                borderRadius: '6px',
                backgroundColor: 'color-mix(in srgb, var(--color-warning) 10%, transparent)',
                border: '1px solid var(--color-warning)',
                color: 'var(--color-text-primary)',
                fontSize: '12px',
                lineHeight: 1.5,
              }}
            >
              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: '6px',
                  fontWeight: 600,
                  color: 'var(--color-warning)',
                }}
              >
                <AlertTriangle size={14} />
                <span>
                  {isMeshOnlyMonitorErr
                    ? 'L2 dophi_mesh:// 纯 Go 发现失败（当前停留在 PROC_ONLY 降级模式）'
                    : 'L2 CSG 服务枚举未生效（当前停留在 PROC_ONLY 降级模式）'}
                </span>
              </div>
              <div style={{ color: 'var(--color-text-secondary)' }}>
                {isMeshOnlyMonitorErr ? (
                  <>
                    Agent 已通过 CSG 发现 <code>RPCMonitorService-RPCMonitorService</code> 的{' '}
                    <code>dophi_mesh://</code> 端点，但纯 Go Mesh 查询（<code>CROSMESH_RPCMON</code>）失败。请检查对应{' '}
                    <code>dophi_mesh://</code> 端口网络连通性，并确认目标环境未启用未支持的 SSP/SECLINK 加密；若已知监控端点，也可在 Agent 启动时通过{' '}
                    <code>--csg-monitor-addr</code> 指定。
                  </>
                ) : (
                  <>
                    未通过 <code>CROSMESH_RPCMON</code> 或 <code>CROSTCP_RPCMON</code> 获取到现网 CSG 服务目录。请检查目标 CSG 控制面端点可达性、确认{' '}
                    <code>RPCMonitorService-RPCMonitorService</code> 已在线发布，或通过 Agent 启动参数{' '}
                    <code>--csg-addr</code> / <code>--csg-monitor-addr</code> 显式指定后重试。
                  </>
                )}
              </div>
            </div>
          )}

          <div
            style={{
              display: 'grid',
              gridTemplateColumns: 'repeat(auto-fit, minmax(150px, 1fr))',
              gap: '8px',
            }}
          >
            <div
              style={{
                padding: '10px 12px',
                borderRadius: '6px',
                border: '1px solid var(--color-border-subtle)',
                backgroundColor: 'var(--color-bg-muted)',
              }}
            >
              <div style={{ fontSize: '12px', color: 'var(--color-text-secondary)' }}>发现模式</div>
              <div style={{ fontSize: '14px', fontWeight: 600, color: 'var(--color-text-primary)' }}>
                {report.discovery_mode || 'PROC_ONLY'}
              </div>
            </div>
            <div
              style={{
                padding: '10px 12px',
                borderRadius: '6px',
                border: '1px solid var(--color-border-subtle)',
                backgroundColor: 'var(--color-bg-muted)',
              }}
            >
              <div style={{ fontSize: '12px', color: 'var(--color-text-secondary)' }}>已纳管 / 现网数</div>
              <div style={{ fontSize: '14px', fontWeight: 600, color: 'var(--color-success)' }}>
                {report.summary.matched_count} / {liveServiceCount}
              </div>
            </div>
            <div
              style={{
                padding: '10px 12px',
                borderRadius: '6px',
                border: '1px solid var(--color-border-subtle)',
                backgroundColor: 'var(--color-bg-muted)',
              }}
            >
              <div style={{ fontSize: '12px', color: 'var(--color-text-secondary)' }}>待导入 SSOT</div>
              <div style={{ fontSize: '14px', fontWeight: 600, color: 'var(--color-warning)' }}>
                {report.summary.undeclared_count}
              </div>
            </div>
            <div
              style={{
                padding: '10px 12px',
                borderRadius: '6px',
                border: '1px solid var(--color-border-subtle)',
                backgroundColor: 'var(--color-bg-muted)',
              }}
            >
              <div style={{ fontSize: '12px', color: 'var(--color-text-secondary)' }}>现网缺失 / 订阅</div>
              <div style={{ fontSize: '14px', fontWeight: 600, color: 'var(--color-text-primary)' }}>
                {report.summary.missing_in_live_count} / {liveSubCount}
              </div>
            </div>
          </div>

          <div
            style={{
              border: '1px solid var(--color-border-subtle)',
              borderRadius: '8px',
              overflow: 'hidden',
            }}
          >
            <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '13px' }}>
              <thead>
                <tr
                  style={{
                    backgroundColor: 'var(--color-bg-muted)',
                    color: 'var(--color-text-secondary)',
                    textAlign: 'left',
                  }}
                >
                  <th style={{ padding: '8px 10px', width: '36px' }}>选</th>
                  <th style={{ padding: '8px 10px' }}>CSG 服务名 (64B)</th>
                  <th style={{ padding: '8px 10px' }}>传输与地址</th>
                  <th style={{ padding: '8px 10px' }}>宿主进程</th>
                  <th style={{ padding: '8px 10px' }}>三态对账</th>
                </tr>
              </thead>
              <tbody>
                {services.length === 0 ? (
                  <tr>
                    <td
                      colSpan={5}
                      style={{
                        padding: '20px',
                        textAlign: 'center',
                        color: 'var(--color-text-secondary)',
                      }}
                    >
                      未探测到 CSG 现网服务实例
                    </td>
                  </tr>
                ) : (
                  services.map((svc, idx) => {
                    const isUndeclared = svc.state === 'UNDECLARED_IN_SSOT';
                    const checked = selectedServices.has(svc.service_name);
                    return (
                      <tr
                        key={`${svc.service_name}-${svc.server_pid ?? 0}-${svc.port ?? 0}-${svc.state}-${idx}`}
                        style={{ borderTop: '1px solid var(--color-border-subtle)' }}
                      >
                        <td style={{ padding: '8px 10px' }}>
                          <input
                            type="checkbox"
                            disabled={!isUndeclared || !isAdmin}
                            checked={checked}
                            onChange={() => toggleService(svc.service_name)}
                          />
                        </td>
                        <td style={{ padding: '8px 10px', fontFamily: 'monospace', color: 'var(--color-text-primary)' }}>
                          <div>{svc.service_name}</div>
                          {svc.ssot_proto_svc && (
                            <div style={{ fontSize: '11px', color: 'var(--color-text-secondary)' }}>
                              pb: {svc.ssot_proto_svc}
                            </div>
                          )}
                        </td>
                        <td style={{ padding: '8px 10px', color: 'var(--color-text-secondary)' }}>
                          <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                            {svc.transport === 'MESH' ? (
                              <span
                                data-testid="csg-transport-badge-MESH"
                                style={{
                                  display: 'inline-flex',
                                  alignItems: 'center',
                                  padding: '1px 6px',
                                  borderRadius: '4px',
                                  fontSize: '11px',
                                  fontWeight: 600,
                                  color: 'var(--color-primary)',
                                  backgroundColor:
                                    'color-mix(in srgb, var(--color-primary) 12%, transparent)',
                                  border: '1px solid var(--color-primary)',
                                }}
                              >
                                dophi_mesh
                              </span>
                            ) : (
                              <span>{svc.transport || '-'}</span>
                            )}
                          </div>
                          <div style={{ fontSize: '12px', fontFamily: 'monospace' }}>
                            {svc.addr || (svc.expected_port ? `expected:${svc.expected_port}` : '-')}
                          </div>
                        </td>
                        <td style={{ padding: '8px 10px', color: 'var(--color-text-primary)' }}>
                          {svc.server_proc
                            ? `${svc.server_proc} (${svc.server_pid || 0})`
                            : svc.ssot_module_name || svc.ssot_module_code || '-'}
                        </td>
                        <td style={{ padding: '8px 10px' }}>{renderStateBadge(svc.state)}</td>
                      </tr>
                    );
                  })
                )}
              </tbody>
            </table>
          </div>

          {(report.subscriptions || []).length > 0 && (
            <div
              style={{
                padding: '10px 12px',
                borderRadius: '8px',
                border: '1px solid var(--color-border-subtle)',
                backgroundColor: 'var(--color-bg-muted)',
              }}
            >
              <div style={{ fontSize: '12px', fontWeight: 600, marginBottom: '6px', color: 'var(--color-text-secondary)' }}>
                现网观测调用订阅边 ({report.subscriptions.length})
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: '4px', fontSize: '12px' }}>
                {report.subscriptions.map((sub, idx) => (
                  <div
                    key={`${sub.client_pid}-${sub.service_name}-${idx}`}
                    style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}
                  >
                    <span style={{ fontFamily: 'monospace', color: 'var(--color-text-primary)' }}>
                      {sub.client_proc || `pid-${sub.client_pid}`} ({sub.client_pid}) → {sub.service_name}
                    </span>
                    {renderStateBadge(sub.state)}
                  </div>
                ))}
              </div>
            </div>
          )}

          <div
            style={{
              display: 'flex',
              flexDirection: 'column',
              gap: '10px',
              padding: '12px',
              borderRadius: '8px',
              border: '1px solid var(--color-border-subtle)',
              backgroundColor: 'var(--color-bg-muted)',
            }}
          >
            {isAdmin ? (
              <>
                <div
                  style={{
                    display: 'flex',
                    flexWrap: 'wrap',
                    gap: '12px',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                  }}
                >
                  <div style={{ display: 'flex', flexWrap: 'wrap', gap: '10px', alignItems: 'center' }}>
                    <label style={{ fontSize: '13px', color: 'var(--color-text-primary)' }}>
                      归属子系统：
                      <select
                        value={selectedSubsystemId}
                        onChange={(e) => setSelectedSubsystemId(e.target.value)}
                        data-testid="csg-import-subsystem-select"
                        style={{
                          marginLeft: '6px',
                          padding: '5px 8px',
                          borderRadius: '6px',
                          border: '1px solid var(--color-border-primary)',
                          backgroundColor: 'var(--color-bg-input)',
                          color: 'var(--color-text-primary)',
                        }}
                      >
                        <option value="">请选择子系统</option>
                        {subsystems.map((sub) => (
                          <option key={sub.id} value={String(sub.id)}>
                            {sub.name} ({sub.code})
                          </option>
                        ))}
                      </select>
                    </label>
                    <button
                      type="button"
                      disabled={creatingSubsystem}
                      onClick={() => setShowCreateSubsystem((prev) => !prev)}
                      data-testid="csg-toggle-create-subsystem-btn"
                      style={{
                        display: 'inline-flex',
                        alignItems: 'center',
                        gap: '4px',
                        padding: '5px 10px',
                        borderRadius: '6px',
                        border: '1px solid var(--color-border-primary)',
                        backgroundColor: 'var(--color-bg-surface)',
                        color: 'var(--color-text-primary)',
                        cursor: creatingSubsystem ? 'wait' : 'pointer',
                        fontSize: '12px',
                      }}
                    >
                      <Plus size={13} />
                      新建子系统
                    </button>
                    <label style={{ display: 'inline-flex', alignItems: 'center', gap: '6px', fontSize: '13px' }}>
                      <input
                        type="checkbox"
                        checked={importDeps}
                        onChange={(e) => setImportDeps(e.target.checked)}
                      />
                      同时导入现网订阅依赖边 (含分层守卫)
                    </label>
                  </div>
                  <div style={{ display: 'flex', gap: '8px' }}>
                    {onClose && (
                      <button
                        type="button"
                        onClick={onClose}
                        style={{
                          padding: '6px 12px',
                          borderRadius: '6px',
                          border: '1px solid var(--color-border-primary)',
                          backgroundColor: 'var(--color-bg-surface)',
                          color: 'var(--color-text-primary)',
                          cursor: 'pointer',
                        }}
                      >
                        关闭
                      </button>
                    )}
                    <button
                      type="button"
                      onClick={() => void handleImport()}
                      disabled={importDisabled}
                      data-testid="csg-import-blueprint-btn"
                      style={{
                        display: 'inline-flex',
                        alignItems: 'center',
                        gap: '6px',
                        padding: '6px 14px',
                        borderRadius: '6px',
                        border: '1px solid var(--color-primary)',
                        backgroundColor: 'var(--color-primary)',
                        color: 'var(--color-text-white)',
                        cursor: importing ? 'wait' : importDisabled ? 'not-allowed' : 'pointer',
                        fontWeight: 500,
                        opacity: importDisabled ? 0.55 : 1,
                      }}
                    >
                      <Download size={14} />
                      {importing ? '导入中...' : `一键导入 SSOT 蓝图 (${selectedServices.size})`}
                    </button>
                  </div>
                </div>

                {subsystemsError && (
                  <div
                    data-testid="csg-subsystems-error-banner"
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      gap: '8px',
                      padding: '8px 10px',
                      borderRadius: '6px',
                      backgroundColor: 'color-mix(in srgb, var(--color-warning) 12%, transparent)',
                      border: '1px solid var(--color-warning)',
                      color: 'var(--color-warning)',
                      fontSize: '12px',
                    }}
                  >
                    <span>子系统列表加载失败：{subsystemsError}</span>
                    <button
                      type="button"
                      onClick={() => void loadSubsystems()}
                      style={{
                        padding: '3px 8px',
                        borderRadius: '4px',
                        border: '1px solid var(--color-border-primary)',
                        backgroundColor: 'var(--color-bg-surface)',
                        color: 'var(--color-text-primary)',
                        cursor: 'pointer',
                        fontSize: '12px',
                      }}
                    >
                      重试加载
                    </button>
                  </div>
                )}

                {isNoSubsystems && (
                  <div
                    data-testid="csg-empty-subsystem-hint"
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: '6px',
                      fontSize: '12px',
                      color: 'var(--color-warning)',
                    }}
                  >
                    <AlertTriangle size={14} />
                    <span>当前架构库中暂无子系统，请先快速新建一个归属子系统后再一键导入蓝图。</span>
                  </div>
                )}

                {showQuickCreateForm && (
                  <div
                    data-testid="csg-quick-create-subsystem-form"
                    style={{
                      display: 'flex',
                      flexWrap: 'wrap',
                      gap: '8px',
                      alignItems: 'center',
                      padding: '10px',
                      borderRadius: '6px',
                      border: '1px solid var(--color-border-subtle)',
                      backgroundColor: 'var(--color-bg-surface)',
                    }}
                  >
                    <input
                      type="text"
                      value={newSubCode}
                      onChange={(e) => setNewSubCode(e.target.value)}
                      placeholder="子系统编码 (如 LOCAL_DEMO)"
                      data-testid="csg-new-subsystem-code-input"
                      style={{
                        width: '180px',
                        padding: '5px 8px',
                        borderRadius: '6px',
                        border: '1px solid var(--color-border-primary)',
                        backgroundColor: 'var(--color-bg-input)',
                        color: 'var(--color-text-primary)',
                        fontSize: '12px',
                        fontFamily: 'monospace',
                      }}
                    />
                    <input
                      type="text"
                      value={newSubName}
                      onChange={(e) => setNewSubName(e.target.value)}
                      placeholder="子系统名称 (如 local-demo 子系统)"
                      data-testid="csg-new-subsystem-name-input"
                      style={{
                        flex: 1,
                        minWidth: '180px',
                        padding: '5px 8px',
                        borderRadius: '6px',
                        border: '1px solid var(--color-border-primary)',
                        backgroundColor: 'var(--color-bg-input)',
                        color: 'var(--color-text-primary)',
                        fontSize: '12px',
                      }}
                    />
                    <button
                      type="button"
                      disabled={creatingSubsystem}
                      onClick={() => void handleQuickCreateSubsystem()}
                      data-testid="csg-quick-create-subsystem-submit"
                      style={{
                        padding: '5px 12px',
                        borderRadius: '6px',
                        border: '1px solid var(--color-primary)',
                        backgroundColor: 'var(--color-primary)',
                        color: 'var(--color-text-white)',
                        cursor: creatingSubsystem ? 'wait' : 'pointer',
                        fontSize: '12px',
                        fontWeight: 500,
                        opacity: creatingSubsystem ? 0.6 : 1,
                      }}
                    >
                      {creatingSubsystem ? '创建中...' : '创建并选中'}
                    </button>
                    {subsystems.length > 0 && (
                      <button
                        type="button"
                        disabled={creatingSubsystem}
                        onClick={() => setShowCreateSubsystem(false)}
                        style={{
                          padding: '5px 10px',
                          borderRadius: '6px',
                          border: '1px solid var(--color-border-primary)',
                          backgroundColor: 'var(--color-bg-surface)',
                          color: 'var(--color-text-primary)',
                          cursor: 'pointer',
                          fontSize: '12px',
                        }}
                      >
                        取消
                      </button>
                    )}
                  </div>
                )}
              </>
            ) : (
              <div
                data-testid="csg-admin-only-notice"
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: '8px',
                  fontSize: '13px',
                  color: 'var(--color-text-secondary)',
                }}
              >
                <AlertTriangle size={15} style={{ color: 'var(--color-warning)' }} />
                <span>蓝图写回 SSOT 仅限平台管理员 (contract_admin) 操作，当前为只读对账视图。</span>
                {report.summary.undeclared_count === 0 && (
                  <CheckCircle2 size={15} style={{ color: 'var(--color-success)' }} />
                )}
              </div>
            )}
          </div>
        </>
      )}
    </div>
  );
};

export const CSGBlueprintImportDrawer: React.FC<CSGBlueprintImportDrawerProps> = ({
  open,
  onClose,
  agent,
  onImported,
}) => {
  return (
    <Drawer
      open={open}
      onClose={onClose}
      title={`CSG 现网服务探测与蓝图对账 — ${agent.name}`}
      width="860px"
    >
      <CSGBlueprintImportContent agent={agent} onImported={onImported} onClose={onClose} />
    </Drawer>
  );
};
