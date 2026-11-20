import React, { useCallback, useEffect, useState } from 'react';
import { Drawer } from '@code/common';
import { useModuleUser } from '../../context/ModuleUserContext';
import {
  AlertTriangle,
  CheckCircle2,
  Compass,
  Download,
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

export const CSGBlueprintImportContent: React.FC<CSGBlueprintImportContentProps> = ({
  agent,
  initialReport = null,
  initialSubsystems = [],
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

  const [csgAddrInput, setCsgAddrInput] = useState('');
  const [loading, setLoading] = useState(false);
  const [errorMsg, setErrorMsg] = useState<string | null>(null);
  const [report, setReport] = useState<CSGTopologyReconcileResp | null>(initialReport);
  const [subsystems, setSubsystems] = useState<Subsystem[]>(initialSubsystems);
  const [selectedSubsystemId, setSelectedSubsystemId] = useState<string>(() =>
    initialSubsystems.length > 0 ? String(initialSubsystems[0].id) : ''
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
    if (initialSubsystems.length === 0) {
      archApi
        .listSubsystems({ page: 1, page_size: 100 })
        .then((res) => {
          const items = res.items || [];
          setSubsystems(items);
          if (items.length > 0) {
            setSelectedSubsystemId((prev) => prev || String(items[0].id));
          }
        })
        .catch(() => {
          // ignore silent subsystem load error until user imports
        });
    }
  }, [agent?.id, initialReport, initialSubsystems.length, loadTopology]);

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
          backgroundColor: 'var(--color-bg-secondary)',
          border: '1px solid var(--color-border-subtle)',
        }}
      >
        <Compass size={16} style={{ color: 'var(--color-accent-primary)' }} />
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
            border: '1px solid var(--color-border-default)',
            backgroundColor: 'var(--color-bg-primary)',
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
            border: '1px solid var(--color-border-default)',
            backgroundColor: 'var(--color-bg-primary)',
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
                backgroundColor: 'var(--color-bg-secondary)',
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
                        active ? 'var(--color-accent-primary)' : 'var(--color-border-default)'
                      }`,
                      backgroundColor: active
                        ? 'color-mix(in srgb, var(--color-accent-primary) 14%, transparent)'
                        : 'var(--color-bg-primary)',
                      color: active ? 'var(--color-accent-primary)' : 'var(--color-text-primary)',
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
                    ? '当前现网 RPCMonitorService 仅发布 dophi_mesh://，需启用只读原生探针 (NATIVE_PROBE)'
                    : 'L2 CSG 服务枚举未生效（当前停留在 PROC_ONLY 降级模式）'}
                </span>
              </div>
              <div style={{ color: 'var(--color-text-secondary)' }}>
                {isMeshOnlyMonitorErr ? (
                  <>
                    现网 <code>RPCMonitorService-RPCMonitorService</code> 未暴露 <code>tcp://</code>{' '}
                    监听端口，CrosTCP 无法直连，配置 <code>--csg-monitor-addr</code> 亦不可用。请在目标宿主机按{' '}
                    <code>scripts/csg/build-native-probe.sh</code> 编译只读探针{' '}
                    <code>rpcmon_probe</code>，放置于 Agent 同目录 / <code>/usr/local/bin/rpcmon_probe</code>
                    （支持热发现），或在启动 <code>code-contract-agent</code> 时传入{' '}
                    <code>--csg-probe-bin &lt;路径&gt; --csg-addr &lt;ip:port&gt;</code> 后重试。
                  </>
                ) : (
                  <>
                    未通过 <code>CROSTCP_RPCMON</code> 或 <code>NATIVE_PROBE</code> 获取到现网 CSG 服务目录。若现网为{' '}
                    <code>dophi_mesh://</code> 协议，请在目标宿主机编译 <code>rpcmon_probe</code> 并配置{' '}
                    <code>--csg-probe-bin</code>（或放入 Agent 同目录自动发现），同时确认候选 CSG 控制面端点可达。
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
                backgroundColor: 'var(--color-bg-secondary)',
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
                backgroundColor: 'var(--color-bg-secondary)',
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
                backgroundColor: 'var(--color-bg-secondary)',
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
                backgroundColor: 'var(--color-bg-secondary)',
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
                    backgroundColor: 'var(--color-bg-secondary)',
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
                                  color: 'var(--color-accent-primary)',
                                  backgroundColor:
                                    'color-mix(in srgb, var(--color-accent-primary) 12%, transparent)',
                                  border: '1px solid var(--color-accent-primary)',
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
                backgroundColor: 'var(--color-bg-secondary)',
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
              flexWrap: 'wrap',
              gap: '12px',
              alignItems: 'center',
              justifyContent: 'space-between',
              padding: '12px',
              borderRadius: '8px',
              border: '1px solid var(--color-border-subtle)',
              backgroundColor: 'var(--color-bg-secondary)',
            }}
          >
            {isAdmin ? (
              <>
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
                        border: '1px solid var(--color-border-default)',
                        backgroundColor: 'var(--color-bg-primary)',
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
                        border: '1px solid var(--color-border-default)',
                        backgroundColor: 'var(--color-bg-primary)',
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
                    disabled={importing || selectedServices.size === 0 || !selectedSubsystemId}
                    data-testid="csg-import-blueprint-btn"
                    style={{
                      display: 'inline-flex',
                      alignItems: 'center',
                      gap: '6px',
                      padding: '6px 14px',
                      borderRadius: '6px',
                      border: 'none',
                      backgroundColor: 'var(--color-accent-primary)',
                      color: '#fff',
                      cursor: importing ? 'wait' : 'pointer',
                      fontWeight: 500,
                      opacity: importing || selectedServices.size === 0 || !selectedSubsystemId ? 0.6 : 1,
                    }}
                  >
                    <Download size={14} />
                    {importing ? '导入中...' : `一键导入 SSOT 蓝图 (${selectedServices.size})`}
                  </button>
                </div>
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
