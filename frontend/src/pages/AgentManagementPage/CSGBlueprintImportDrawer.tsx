import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Drawer } from '@code/common';
import { useModuleUser } from '../../context/ModuleUserContext';
import {
  AlertTriangle,
  CheckCircle2,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronUp,
  Compass,
  Download,
  GitFork,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  Server,
  ShieldAlert,
  X,
} from 'lucide-react';
import * as agentApi from '../../api/agent';
import * as archApi from '../../api/arch';
import { useToast } from '../../utils/toast';
import type {
  Agent,
  CSGReconcileState,
  CSGTopologyReconcileResp,
  ReconciledCSGService,
  ReconciledCSGSubscription,
  Subsystem,
} from '../../types';
import './CSGBlueprintImportDrawer.css';

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
  initialTab?: 'services' | 'subscriptions';
  onImported?: () => void;
  onClose?: () => void;
}

export interface CSGServiceFilterState {
  search: string;
  state: CSGReconcileState | 'ALL';
  proc: string;
  transport: string;
  onlySelected: boolean;
}

export interface CSGSubscriptionFilterState {
  search: string;
  state: CSGReconcileState | 'ALL';
  clientProc: string;
  onlySelectedServices: boolean;
}

const UNKNOWN_PROC_KEY = '__UNKNOWN__';
const DEFAULT_PAGE_SIZE = 50;
const PAGE_SIZE_OPTIONS = [50, 100, 200, 0] as const;

const STATE_BADGE_MAP: Record<
  CSGReconcileState,
  { label: string; fullLabel: string; color: string; bg: string; border: string }
> = {
  MATCHED: {
    label: '已纳管',
    fullLabel: '已纳管 (MATCHED)',
    color: 'var(--color-success)',
    bg: 'var(--color-success-subtle)',
    border: 'var(--color-success-border)',
  },
  UNDECLARED_IN_SSOT: {
    label: '待导入',
    fullLabel: '待导入 (UNDECLARED_IN_SSOT)',
    color: 'var(--color-warning)',
    bg: 'var(--color-warning-subtle)',
    border: 'var(--color-warning-border)',
  },
  MISSING_IN_LIVE: {
    label: '现网缺失',
    fullLabel: '现网缺失 (MISSING_IN_LIVE)',
    color: 'var(--color-danger)',
    bg: 'var(--color-danger-subtle)',
    border: 'var(--color-danger-border)',
  },
};

function getServiceProcKey(svc: ReconciledCSGService): string {
  return svc.server_proc || svc.ssot_module_name || svc.ssot_module_code || UNKNOWN_PROC_KEY;
}

function getSubscriptionClientKey(sub: ReconciledCSGSubscription): string {
  return sub.client_proc || (sub.client_pid ? `pid-${sub.client_pid}` : UNKNOWN_PROC_KEY);
}

export function filterReconciledServices(
  services: ReconciledCSGService[],
  filter: CSGServiceFilterState,
  selectedServices: Set<string>
): ReconciledCSGService[] {
  const kw = filter.search.trim().toLowerCase();
  return services.filter((svc) => {
    if (filter.state !== 'ALL' && svc.state !== filter.state) {
      return false;
    }
    if (filter.proc !== 'ALL' && getServiceProcKey(svc) !== filter.proc) {
      return false;
    }
    if (filter.transport !== 'ALL') {
      const transport = (svc.transport || UNKNOWN_PROC_KEY).toUpperCase();
      if (transport !== filter.transport) {
        return false;
      }
    }
    if (filter.onlySelected && !selectedServices.has(svc.service_name)) {
      return false;
    }
    if (kw) {
      const haystacks = [
        svc.service_name,
        svc.ssot_proto_svc,
        svc.server_proc,
        svc.server_pid !== undefined ? String(svc.server_pid) : '',
        svc.addr,
        svc.ssot_module_name,
        svc.ssot_module_code,
        svc.expected_port !== undefined ? String(svc.expected_port) : '',
      ];
      if (!haystacks.some((val) => val && val.toLowerCase().includes(kw))) {
        return false;
      }
    }
    return true;
  });
}

export function filterReconciledSubscriptions(
  subscriptions: ReconciledCSGSubscription[],
  filter: CSGSubscriptionFilterState,
  selectedServices: Set<string>
): ReconciledCSGSubscription[] {
  const kw = filter.search.trim().toLowerCase();
  return subscriptions.filter((sub) => {
    if (filter.state !== 'ALL' && sub.state !== filter.state) {
      return false;
    }
    if (filter.clientProc !== 'ALL' && getSubscriptionClientKey(sub) !== filter.clientProc) {
      return false;
    }
    if (filter.onlySelectedServices && !selectedServices.has(sub.service_name)) {
      return false;
    }
    if (kw) {
      const haystacks = [
        sub.client_proc,
        sub.client_pid !== undefined ? String(sub.client_pid) : '',
        sub.client_addr,
        sub.service_name,
      ];
      if (!haystacks.some((val) => val && val.toLowerCase().includes(kw))) {
        return false;
      }
    }
    return true;
  });
}

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
  initialTab = 'services',
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

  const [selectedCsgAddr, setSelectedCsgAddr] = useState('');
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

  // 布局、标签页与现网过滤状态
  const [activeTab, setActiveTab] = useState<'services' | 'subscriptions'>(initialTab);
  const [errorsExpanded, setErrorsExpanded] = useState(false);

  const [serviceSearch, setServiceSearch] = useState('');
  const [serviceStateFilter, setServiceStateFilter] = useState<CSGReconcileState | 'ALL'>('ALL');
  const [serviceProcFilter, setServiceProcFilter] = useState<string>('ALL');
  const [serviceTransportFilter, setServiceTransportFilter] = useState<string>('ALL');
  const [onlySelectedServices, setOnlySelectedServices] = useState(false);
  const [servicePage, setServicePage] = useState(1);
  const [servicePageSize, setServicePageSize] = useState<number>(DEFAULT_PAGE_SIZE);

  const [subSearch, setSubSearch] = useState('');
  const [subStateFilter, setSubStateFilter] = useState<CSGReconcileState | 'ALL'>('ALL');
  const [subClientFilter, setSubClientFilter] = useState<string>('ALL');
  const [onlySelectedSubServices, setOnlySelectedSubServices] = useState(false);
  const [subPage, setSubPage] = useState(1);
  const [subPageSize, setSubPageSize] = useState<number>(DEFAULT_PAGE_SIZE);

  const masterCheckboxRef = useRef<HTMLInputElement | null>(null);

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
          explicitAddr !== undefined ? explicitAddr : selectedCsgAddr || undefined
        );
        setReport(res);
        const undeclared = new Set<string>();
        (res.services || []).forEach((s) => {
          if (s.state === 'UNDECLARED_IN_SSOT') {
            undeclared.add(s.service_name);
          }
        });
        setSelectedServices(undeclared);
        setServicePage(1);
        setSubPage(1);
      } catch (err: unknown) {
        const msg = err instanceof Error ? err.message : String(err);
        setErrorMsg(msg);
      } finally {
        setLoading(false);
      }
    },
    [agent?.id, selectedCsgAddr]
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
        csg_addr: selectedCsgAddr || undefined,
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
        title={cfg.fullLabel}
        className="csg-state-badge"
        style={{
          color: cfg.color,
          backgroundColor: cfg.bg,
          border: `1px solid ${cfg.border}`,
        }}
      >
        <span className="csg-state-dot" style={{ backgroundColor: cfg.color }} />
        <span>{cfg.label}</span>
      </span>
    );
  };

  const services: ReconciledCSGService[] = useMemo(() => report?.services || [], [report]);
  const subscriptions: ReconciledCSGSubscription[] = useMemo(
    () => report?.subscriptions || [],
    [report]
  );
  const endpoints = useMemo(() => report?.csg_endpoints || [], [report]);
  const discoveryErrors = report?.errors || [];

  const activeEndpointAddr = useMemo(() => {
    if (selectedCsgAddr) return selectedCsgAddr;
    if (endpoints.length > 0) {
      return `${endpoints[0].listen_ip}:${endpoints[0].listen_port}`;
    }
    return '';
  }, [selectedCsgAddr, endpoints]);

  const liveServiceCount = report
    ? report.summary.live_service_count ??
    report.summary.matched_count + report.summary.undeclared_count
    : 0;
  const isProcOnlyDegraded =
    Boolean(report) &&
    (report?.discovery_mode || 'PROC_ONLY') === 'PROC_ONLY' &&
    liveServiceCount === 0;
  const isMeshOnlyMonitorErr = discoveryErrors.some((e) => e.includes('dophi_mesh://'));

  // 订阅边计数映射（按服务名）
  const subCountByService = useMemo(() => {
    const map = new Map<string, number>();
    for (const sub of subscriptions) {
      map.set(sub.service_name, (map.get(sub.service_name) || 0) + 1);
    }
    return map;
  }, [subscriptions]);

  // 服务维度筛选项与计数
  const serviceStateCounts = useMemo(() => {
    const counts: Record<CSGReconcileState, number> = {
      MATCHED: 0,
      UNDECLARED_IN_SSOT: 0,
      MISSING_IN_LIVE: 0,
    };
    for (const svc of services) {
      if (counts[svc.state] !== undefined) {
        counts[svc.state] += 1;
      }
    }
    return counts;
  }, [services]);

  const serviceProcOptions = useMemo(() => {
    const counts = new Map<string, number>();
    for (const svc of services) {
      const key = getServiceProcKey(svc);
      counts.set(key, (counts.get(key) || 0) + 1);
    }
    return Array.from(counts.entries())
      .map(([value, count]) => ({
        value,
        label: value === UNKNOWN_PROC_KEY ? '未知进程/模块' : value,
        count,
      }))
      .sort((a, b) => b.count - a.count || a.label.localeCompare(b.label));
  }, [services]);

  const serviceTransportOptions = useMemo(() => {
    const counts = new Map<string, number>();
    for (const svc of services) {
      const key = (svc.transport || UNKNOWN_PROC_KEY).toUpperCase();
      counts.set(key, (counts.get(key) || 0) + 1);
    }
    return Array.from(counts.entries())
      .map(([value, count]) => ({
        value,
        label: value === UNKNOWN_PROC_KEY ? '未指定协议' : value === 'MESH' ? 'dophi_mesh' : value,
        count,
      }))
      .sort((a, b) => b.count - a.count || a.label.localeCompare(b.label));
  }, [services]);

  // 订阅边维度筛选项与计数
  const subStateCounts = useMemo(() => {
    const counts: Record<CSGReconcileState, number> = {
      MATCHED: 0,
      UNDECLARED_IN_SSOT: 0,
      MISSING_IN_LIVE: 0,
    };
    for (const sub of subscriptions) {
      if (counts[sub.state] !== undefined) {
        counts[sub.state] += 1;
      }
    }
    return counts;
  }, [subscriptions]);

  const subClientOptions = useMemo(() => {
    const counts = new Map<string, number>();
    for (const sub of subscriptions) {
      const key = getSubscriptionClientKey(sub);
      counts.set(key, (counts.get(key) || 0) + 1);
    }
    return Array.from(counts.entries())
      .map(([value, count]) => ({
        value,
        label: value === UNKNOWN_PROC_KEY ? '未知调用方' : value,
        count,
      }))
      .sort((a, b) => b.count - a.count || a.label.localeCompare(b.label));
  }, [subscriptions]);

  // 过滤后服务列表 & 分页
  const filteredServices = useMemo(
    () =>
      filterReconciledServices(
        services,
        {
          search: serviceSearch,
          state: serviceStateFilter,
          proc: serviceProcFilter,
          transport: serviceTransportFilter,
          onlySelected: onlySelectedServices,
        },
        selectedServices
      ),
    [
      services,
      serviceSearch,
      serviceStateFilter,
      serviceProcFilter,
      serviceTransportFilter,
      onlySelectedServices,
      selectedServices,
    ]
  );

  const serviceTotalPages = useMemo(() => {
    if (servicePageSize === 0 || filteredServices.length === 0) return 1;
    return Math.max(1, Math.ceil(filteredServices.length / servicePageSize));
  }, [filteredServices.length, servicePageSize]);

  const safeServicePage = Math.min(servicePage, serviceTotalPages);

  const pagedServices = useMemo(() => {
    if (servicePageSize === 0) return filteredServices;
    const start = (safeServicePage - 1) * servicePageSize;
    return filteredServices.slice(start, start + servicePageSize);
  }, [filteredServices, safeServicePage, servicePageSize]);

  // 过滤后订阅列表 & 分页
  const filteredSubscriptions = useMemo(
    () =>
      filterReconciledSubscriptions(
        subscriptions,
        {
          search: subSearch,
          state: subStateFilter,
          clientProc: subClientFilter,
          onlySelectedServices: onlySelectedSubServices,
        },
        selectedServices
      ),
    [subscriptions, subSearch, subStateFilter, subClientFilter, onlySelectedSubServices, selectedServices]
  );

  const subTotalPages = useMemo(() => {
    if (subPageSize === 0 || filteredSubscriptions.length === 0) return 1;
    return Math.max(1, Math.ceil(filteredSubscriptions.length / subPageSize));
  }, [filteredSubscriptions.length, subPageSize]);

  const safeSubPage = Math.min(subPage, subTotalPages);

  const pagedSubscriptions = useMemo(() => {
    if (subPageSize === 0) return filteredSubscriptions;
    const start = (safeSubPage - 1) * subPageSize;
    return filteredSubscriptions.slice(start, start + subPageSize);
  }, [filteredSubscriptions, safeSubPage, subPageSize]);

  // 批量勾选聚合
  const filteredUndeclaredNames = useMemo(() => {
    const names = new Set<string>();
    for (const svc of filteredServices) {
      if (svc.state === 'UNDECLARED_IN_SSOT') {
        names.add(svc.service_name);
      }
    }
    return Array.from(names);
  }, [filteredServices]);

  const filteredSelectedCount = useMemo(
    () => filteredUndeclaredNames.filter((name) => selectedServices.has(name)).length,
    [filteredUndeclaredNames, selectedServices]
  );

  const isAllFilteredSelected =
    filteredUndeclaredNames.length > 0 && filteredSelectedCount === filteredUndeclaredNames.length;
  const isPartialFilteredSelected =
    filteredSelectedCount > 0 && filteredSelectedCount < filteredUndeclaredNames.length;

  useEffect(() => {
    if (masterCheckboxRef.current) {
      masterCheckboxRef.current.indeterminate = isPartialFilteredSelected;
    }
  }, [isPartialFilteredSelected]);

  const relatedSelectedUndeclaredSubCount = useMemo(() => {
    if (selectedServices.size === 0) return 0;
    let count = 0;
    for (const sub of subscriptions) {
      if (sub.state === 'UNDECLARED_IN_SSOT' && selectedServices.has(sub.service_name)) {
        count += 1;
      }
    }
    return count;
  }, [subscriptions, selectedServices]);

  const selectFilteredUndeclared = () => {
    if (filteredUndeclaredNames.length === 0) return;
    setSelectedServices((prev) => {
      const next = new Set(prev);
      filteredUndeclaredNames.forEach((name) => next.add(name));
      return next;
    });
  };

  const deselectFilteredUndeclared = () => {
    if (filteredUndeclaredNames.length === 0) return;
    setSelectedServices((prev) => {
      const next = new Set(prev);
      filteredUndeclaredNames.forEach((name) => next.delete(name));
      return next;
    });
  };

  const toggleMasterFilteredCheckbox = () => {
    if (isAllFilteredSelected) {
      deselectFilteredUndeclared();
    } else {
      selectFilteredUndeclared();
    }
  };

  const clearAllSelected = () => {
    setSelectedServices(new Set());
  };

  const hasActiveServiceFilter =
    serviceSearch.trim() !== '' ||
    serviceStateFilter !== 'ALL' ||
    serviceProcFilter !== 'ALL' ||
    serviceTransportFilter !== 'ALL' ||
    onlySelectedServices;

  const resetServiceFilters = () => {
    setServiceSearch('');
    setServiceStateFilter('ALL');
    setServiceProcFilter('ALL');
    setServiceTransportFilter('ALL');
    setOnlySelectedServices(false);
    setServicePage(1);
  };

  const hasActiveSubFilter =
    subSearch.trim() !== '' ||
    subStateFilter !== 'ALL' ||
    subClientFilter !== 'ALL' ||
    onlySelectedSubServices;

  const resetSubFilters = () => {
    setSubSearch('');
    setSubStateFilter('ALL');
    setSubClientFilter('ALL');
    setOnlySelectedSubServices(false);
    setSubPage(1);
  };

  const jumpToServiceSubscriptions = (serviceName: string) => {
    setActiveTab('subscriptions');
    setSubStateFilter('ALL');
    setSubClientFilter('ALL');
    setOnlySelectedSubServices(false);
    setSubSearch(serviceName);
    setSubPage(1);
  };

  const isNoSubsystems = subsystemsLoaded && subsystems.length === 0 && !subsystemsError;
  const showQuickCreateForm = showCreateSubsystem || isNoSubsystems;
  const importDisabled = importing || selectedServices.size === 0 || !selectedSubsystemId;

  const visibleErrors = errorsExpanded ? discoveryErrors : discoveryErrors.slice(0, 2);

  return (
    <div className="csg-drawer-shell">
      {/* ── Zone A: 顶部固定探测栏 + 告警 ── */}
      <div className="csg-drawer-top">
        <div className="csg-probe-bar">
          <span className="csg-probe-node-label">
            <Compass size={15} style={{ color: 'var(--color-primary)' }} />
            目标节点：{agent.name}
          </span>

          <span className="csg-probe-divider" aria-hidden="true" />

          <div data-testid="csg-endpoints-selector" className="csg-probe-endpoint-group">
            <label className="csg-probe-endpoint-label" htmlFor="csg-endpoint-select">
              CSG 控制面{endpoints.length > 1 ? ` (${endpoints.length})` : ''}：
            </label>
            {endpoints.length > 0 ? (
              <select
                id="csg-endpoint-select"
                data-testid="csg-endpoint-select"
                aria-label="候选 CSG 控制面"
                className="csg-probe-select"
                value={activeEndpointAddr}
                disabled={loading}
                onChange={(e) => {
                  const next = e.target.value;
                  setSelectedCsgAddr(next);
                  void loadTopology(next);
                }}
              >
                {endpoints.map((ep, idx) => {
                  const epAddr = `${ep.listen_ip}:${ep.listen_port}`;
                  return (
                    <option key={`${epAddr}-${ep.source}-${idx}`} value={epAddr}>
                      {epAddr} ({ep.exe_name ? `${ep.exe_name} · ` : ''}
                      {ep.source})
                    </option>
                  );
                })}
              </select>
            ) : (
              <span className="csg-probe-empty-endpoint">
                {loading && !report ? '正在探测本机 CSG 端点...' : '未发现候选 CSG 端点'}
              </span>
            )}
          </div>

          <button
            type="button"
            onClick={() => void loadTopology()}
            disabled={loading}
            data-testid="csg-refresh-topology-btn"
            className="csg-probe-icon-btn"
            title={loading ? '探测中...' : '刷新现网探测'}
            aria-label="刷新现网探测"
          >
            <RefreshCw size={14} className={loading ? 'csg-spin' : undefined} />
          </button>
        </div>

        {errorMsg && (
          <div className="csg-banner-error">
            <ShieldAlert size={16} />
            <span>{errorMsg}</span>
          </div>
        )}

        {report && (
          <>
            {discoveryErrors.length > 0 && (
              <div data-testid="csg-discovery-errors-banner" className="csg-banner-warning">
                <div className="csg-banner-warning-header">
                  <span className="csg-banner-warning-title">
                    <AlertTriangle size={14} />
                    <span>CSG 发现告警 / 降级原因 ({discoveryErrors.length})</span>
                  </span>
                  {discoveryErrors.length > 2 && (
                    <button
                      type="button"
                      className="csg-link-btn"
                      onClick={() => setErrorsExpanded((prev) => !prev)}
                    >
                      {errorsExpanded ? (
                        <>
                          <ChevronUp size={13} /> 收起
                        </>
                      ) : (
                        <>
                          <ChevronDown size={13} /> 展开全部 ({discoveryErrors.length})
                        </>
                      )}
                    </button>
                  )}
                </div>
                {visibleErrors.map((errItem, idx) => (
                  <div
                    key={idx}
                    style={{
                      fontFamily: 'var(--font-family-mono, monospace)',
                      wordBreak: 'break-all',
                    }}
                  >
                    {errItem}
                  </div>
                ))}
              </div>
            )}

            {isProcOnlyDegraded && (
              <div data-testid="csg-proc-only-degraded-banner" className="csg-banner-warning">
                <div className="csg-banner-warning-header">
                  <span className="csg-banner-warning-title">
                    <AlertTriangle size={14} />
                    <span>
                      {isMeshOnlyMonitorErr
                        ? 'L2 dophi_mesh:// 纯 Go 发现失败（当前停留在 PROC_ONLY 降级模式）'
                        : 'L2 CSG 服务枚举未生效（当前停留在 PROC_ONLY 降级模式）'}
                    </span>
                  </span>
                </div>
                <div style={{ color: 'var(--color-text-secondary)' }}>
                  {isMeshOnlyMonitorErr ? (
                    <>
                      Agent 已通过 CSG 发现 <code>RPCMonitorService-RPCMonitorService</code> 的{' '}
                      <code>dophi_mesh://</code> 端点，但纯 Go Mesh 查询（<code>CROSMESH_RPCMON</code>
                      ）失败。请检查对应 <code>dophi_mesh://</code> 端口网络连通性，并确认目标环境未启用未支持的
                      SSP/SECLINK 加密；若已知监控端点，也可在 Agent 启动时通过{' '}
                      <code>--csg-monitor-addr</code> 指定。
                    </>
                  ) : (
                    <>
                      未通过 <code>CROSMESH_RPCMON</code> 或 <code>CROSTCP_RPCMON</code> 获取到现网
                      CSG 服务目录。请检查目标 CSG 控制面端点可达性、确认{' '}
                      <code>RPCMonitorService-RPCMonitorService</code> 已在线发布，或通过 Agent 启动参数{' '}
                      <code>--csg-addr</code> / <code>--csg-monitor-addr</code> 显式指定后重试。
                    </>
                  )}
                </div>
              </div>
            )}
          </>
        )}
      </div>

      {/* ── Zone B: 中间标签页 + 多维过滤 + 滚动表格区 ── */}
      {report && (
        <div className="csg-workspace">
          <div data-testid="csg-reconcile-tabs" className="csg-tabs-header">
            <div className="csg-tabs-nav">
              <button
                type="button"
                data-testid="csg-tab-services"
                onClick={() => setActiveTab('services')}
                className={`csg-tab-btn${activeTab === 'services' ? ' csg-tab-btn--active' : ''}`}
              >
                <Server size={14} />
                <span>现网服务列表</span>
                <span
                  data-testid="csg-live-coverage-badge"
                  className={`csg-tab-count${report.summary.matched_count > 0 ? ' csg-tab-count--coverage' : ''}`}
                  title={`现网发现 ${liveServiceCount} 个服务，已纳管 ${report.summary.matched_count} 个（列表共 ${services.length} 条）`}
                >
                  {`已纳管 ${report.summary.matched_count}/${liveServiceCount}`}
                </span>
              </button>

              <button
                type="button"
                data-testid="csg-tab-subscriptions"
                onClick={() => setActiveTab('subscriptions')}
                className={`csg-tab-btn${activeTab === 'subscriptions' ? ' csg-tab-btn--active' : ''
                  }`}
              >
                <GitFork size={14} />
                <span>调用订阅边</span>
                <span className="csg-tab-count">{subscriptions.length}</span>
              </button>
            </div>

            {(report.filtered_remote_count > 0 || report.filtered_transient_count > 0) && (
              <span className="csg-tabs-meta">
                采集已自动降噪：远端 {report.filtered_remote_count} / 瞬态{' '}
                {report.filtered_transient_count}
              </span>
            )}
          </div>

          {activeTab === 'services' ? (
            <>
              {/* 服务列表单行过滤栏 */}
              <div data-testid="csg-services-filter-bar" className="csg-filter-bar">
                <div className="csg-filter-row">
                  <div className="csg-search-box">
                    <Search size={14} className="csg-search-icon" />
                    <input
                      type="text"
                      value={serviceSearch}
                      onChange={(e) => {
                        setServiceSearch(e.target.value);
                        setServicePage(1);
                      }}
                      placeholder="搜索服务名、Proto、进程、PID 或地址..."
                      data-testid="csg-service-search-input"
                      className="csg-search-input"
                    />
                    {serviceSearch && (
                      <button
                        type="button"
                        aria-label="清空服务搜索"
                        onClick={() => {
                          setServiceSearch('');
                          setServicePage(1);
                        }}
                        className="csg-search-clear"
                      >
                        <X size={13} />
                      </button>
                    )}
                  </div>

                  <select
                    value={serviceProcFilter}
                    onChange={(e) => {
                      setServiceProcFilter(e.target.value);
                      setServicePage(1);
                    }}
                    data-testid="csg-service-proc-filter"
                    className="csg-filter-select csg-filter-select--proc"
                    aria-label="按宿主进程过滤"
                  >
                    <option value="ALL">全部进程 ({services.length})</option>
                    {serviceProcOptions.map((opt) => (
                      <option key={opt.value} value={opt.value}>
                        {opt.label} ({opt.count})
                      </option>
                    ))}
                  </select>

                  <select
                    value={serviceTransportFilter}
                    onChange={(e) => {
                      setServiceTransportFilter(e.target.value);
                      setServicePage(1);
                    }}
                    data-testid="csg-service-transport-filter"
                    className="csg-filter-select csg-filter-select--transport"
                    aria-label="按传输协议过滤"
                  >
                    <option value="ALL">全部协议</option>
                    {serviceTransportOptions.map((opt) => (
                      <option key={opt.value} value={opt.value}>
                        {opt.label} ({opt.count})
                      </option>
                    ))}
                  </select>

                  <div data-testid="csg-service-state-filter" className="csg-state-pills">
                    <button
                      type="button"
                      onClick={() => {
                        setServiceStateFilter('ALL');
                        setServicePage(1);
                      }}
                      className={`csg-pill${serviceStateFilter === 'ALL' ? ' csg-pill--active' : ''}`}
                    >
                      全部 ({services.length})
                    </button>
                    {(
                      ['UNDECLARED_IN_SSOT', 'MATCHED', 'MISSING_IN_LIVE'] as CSGReconcileState[]
                    ).map((st) => {
                      const cfg = STATE_BADGE_MAP[st];
                      const active = serviceStateFilter === st;
                      return (
                        <button
                          key={st}
                          type="button"
                          onClick={() => {
                            setServiceStateFilter(active ? 'ALL' : st);
                            setServicePage(1);
                          }}
                          className={`csg-pill${active ? ' csg-pill--active' : ''}`}
                        >
                          <span
                            className="csg-pill-dot"
                            style={{ backgroundColor: cfg.color }}
                          />
                          <span>
                            {cfg.label} ({serviceStateCounts[st]})
                          </span>
                        </button>
                      );
                    })}
                    <button
                      type="button"
                      onClick={() => {
                        setOnlySelectedServices((prev) => !prev);
                        setServicePage(1);
                      }}
                      title="仅看已选待导入服务"
                      className={`csg-pill${onlySelectedServices ? ' csg-pill--active' : ''}`}
                    >
                      已选 ({selectedServices.size})
                    </button>
                  </div>

                  {hasActiveServiceFilter && (
                    <button
                      type="button"
                      onClick={resetServiceFilters}
                      className="csg-filter-reset-btn"
                      title="重置筛选"
                      aria-label="重置筛选"
                    >
                      <RotateCcw size={13} />
                    </button>
                  )}
                </div>
              </div>

              {/* 服务数据表格 */}
              <div className="csg-table-card">
                <div className="csg-table-scroll">
                  <table className="csg-table">
                    <thead>
                      <tr>
                        <th style={{ width: '40px', textAlign: 'center' }}>
                          <input
                            ref={masterCheckboxRef}
                            type="checkbox"
                            data-testid="csg-select-all-filtered-checkbox"
                            disabled={!isAdmin || filteredUndeclaredNames.length === 0}
                            checked={isAllFilteredSelected}
                            onChange={toggleMasterFilteredCheckbox}
                            title="全选/取消全选当前筛选出的待导入服务"
                          />
                        </th>
                        <th style={{ width: '34%' }}>CSG 服务名 (64B)</th>
                        <th style={{ width: '24%' }}>传输与地址</th>
                        <th style={{ width: '19%' }}>宿主进程</th>
                        <th style={{ width: '84px' }}>订阅边</th>
                        <th style={{ width: '100px' }}>状态</th>
                      </tr>
                    </thead>
                    <tbody>
                      {pagedServices.length === 0 ? (
                        <tr>
                          <td colSpan={6} className="csg-empty-cell">
                            {services.length === 0
                              ? '未探测到 CSG 现网服务实例'
                              : '没有符合当前筛选条件的 CSG 服务'}
                          </td>
                        </tr>
                      ) : (
                        pagedServices.map((svc, idx) => {
                          const isUndeclared = svc.state === 'UNDECLARED_IN_SSOT';
                          const checked = selectedServices.has(svc.service_name);
                          const edgeCount = subCountByService.get(svc.service_name) || 0;
                          const addrText =
                            svc.addr || (svc.expected_port ? `expected:${svc.expected_port}` : '-');
                          const procText = svc.server_proc
                            ? `${svc.server_proc} (${svc.server_pid || 0})`
                            : svc.ssot_module_name || svc.ssot_module_code || '-';
                          return (
                            <tr
                              key={`${svc.service_name}-${svc.server_pid ?? 0}-${svc.port ?? 0}-${svc.state}-${idx}`}
                              className={checked ? 'csg-row--selected' : undefined}
                            >
                              <td style={{ textAlign: 'center' }}>
                                <input
                                  type="checkbox"
                                  disabled={!isUndeclared || !isAdmin}
                                  checked={checked}
                                  onChange={() => toggleService(svc.service_name)}
                                />
                              </td>
                              <td>
                                <div className="csg-mono-ellipsis" title={svc.service_name}>
                                  {svc.service_name}
                                </div>
                                {svc.ssot_proto_svc && (
                                  <div className="csg-sub-meta" title={svc.ssot_proto_svc}>
                                    pb: {svc.ssot_proto_svc}
                                  </div>
                                )}
                              </td>
                              <td>
                                <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                                  {svc.transport === 'MESH' ? (
                                    <span
                                      data-testid="csg-transport-badge-MESH"
                                      className="csg-transport-pill"
                                      style={{
                                        color: 'var(--color-primary)',
                                        backgroundColor: 'var(--color-primary-subtle)',
                                        border: '1px solid var(--color-primary-border)',
                                      }}
                                    >
                                      dophi_mesh
                                    </span>
                                  ) : (
                                    <span
                                      className="csg-transport-pill"
                                      style={{
                                        color: 'var(--color-text-secondary)',
                                        backgroundColor: 'var(--color-bg-muted)',
                                        border: '1px solid var(--color-border-subtle)',
                                      }}
                                    >
                                      {svc.transport || '-'}
                                    </span>
                                  )}
                                </div>
                                <div className="csg-sub-meta csg-mono-ellipsis" title={addrText}>
                                  {addrText}
                                </div>
                              </td>
                              <td>
                                <div className="csg-mono-ellipsis" title={procText}>
                                  {procText}
                                </div>
                              </td>
                              <td>
                                {edgeCount > 0 ? (
                                  <button
                                    type="button"
                                    onClick={() => jumpToServiceSubscriptions(svc.service_name)}
                                    className="csg-edge-count-btn"
                                    title={`查看 ${svc.service_name} 的 ${edgeCount} 条现网调用订阅边`}
                                  >
                                    {edgeCount} 条
                                  </button>
                                ) : (
                                  <span style={{ color: 'var(--color-text-muted)' }}>-</span>
                                )}
                              </td>
                              <td>{renderStateBadge(svc.state)}</td>
                            </tr>
                          );
                        })
                      )}
                    </tbody>
                  </table>
                </div>

                {/* 服务分页条 */}
                <div data-testid="csg-services-pagination" className="csg-pagination-bar">
                  <span>
                    匹配 <strong>{filteredServices.length}</strong> / {services.length} 个服务
                    {servicePageSize > 0 && filteredServices.length > 0 && (
                      <>
                        {' '}
                        · 第 {(safeServicePage - 1) * servicePageSize + 1}-
                        {Math.min(safeServicePage * servicePageSize, filteredServices.length)} 条
                      </>
                    )}
                  </span>
                  <div className="csg-pagination-controls">
                    <select
                      value={servicePageSize}
                      onChange={(e) => {
                        setServicePageSize(Number(e.target.value));
                        setServicePage(1);
                      }}
                      className="csg-filter-select"
                      style={{ height: '26px' }}
                      aria-label="服务列表每页条数"
                    >
                      {PAGE_SIZE_OPTIONS.map((size) => (
                        <option key={size} value={size}>
                          {size === 0 ? '全部显示' : `${size} 条/页`}
                        </option>
                      ))}
                    </select>
                    {servicePageSize > 0 && (
                      <>
                        <button
                          type="button"
                          disabled={safeServicePage <= 1}
                          onClick={() => setServicePage((p) => Math.max(1, p - 1))}
                          className="csg-page-btn"
                          aria-label="上一页"
                        >
                          <ChevronLeft size={14} />
                        </button>
                        <span>
                          {safeServicePage} / {serviceTotalPages}
                        </span>
                        <button
                          type="button"
                          disabled={safeServicePage >= serviceTotalPages}
                          onClick={() => setServicePage((p) => Math.min(serviceTotalPages, p + 1))}
                          className="csg-page-btn"
                          aria-label="下一页"
                        >
                          <ChevronRight size={14} />
                        </button>
                      </>
                    )}
                  </div>
                </div>
              </div>
            </>
          ) : (
            <>
              {/* 订阅边单行过滤栏 */}
              <div data-testid="csg-subscriptions-filter-bar" className="csg-filter-bar">
                <div className="csg-filter-row">
                  <div className="csg-search-box">
                    <Search size={14} className="csg-search-icon" />
                    <input
                      type="text"
                      value={subSearch}
                      onChange={(e) => {
                        setSubSearch(e.target.value);
                        setSubPage(1);
                      }}
                      placeholder="搜索调用方进程、PID、IP 或目标服务名..."
                      data-testid="csg-sub-search-input"
                      className="csg-search-input"
                    />
                    {subSearch && (
                      <button
                        type="button"
                        aria-label="清空订阅边搜索"
                        onClick={() => {
                          setSubSearch('');
                          setSubPage(1);
                        }}
                        className="csg-search-clear"
                      >
                        <X size={13} />
                      </button>
                    )}
                  </div>

                  <select
                    value={subClientFilter}
                    onChange={(e) => {
                      setSubClientFilter(e.target.value);
                      setSubPage(1);
                    }}
                    data-testid="csg-sub-client-filter"
                    className="csg-filter-select csg-filter-select--proc"
                    aria-label="按调用方进程过滤"
                  >
                    <option value="ALL">全部调用方 ({subscriptions.length})</option>
                    {subClientOptions.map((opt) => (
                      <option key={opt.value} value={opt.value}>
                        {opt.label} ({opt.count})
                      </option>
                    ))}
                  </select>

                  <div data-testid="csg-sub-state-filter" className="csg-state-pills">
                    <button
                      type="button"
                      onClick={() => {
                        setSubStateFilter('ALL');
                        setSubPage(1);
                      }}
                      className={`csg-pill${subStateFilter === 'ALL' ? ' csg-pill--active' : ''}`}
                    >
                      全部 ({subscriptions.length})
                    </button>
                    {(
                      ['UNDECLARED_IN_SSOT', 'MATCHED', 'MISSING_IN_LIVE'] as CSGReconcileState[]
                    ).map((st) => {
                      const cfg = STATE_BADGE_MAP[st];
                      const active = subStateFilter === st;
                      return (
                        <button
                          key={st}
                          type="button"
                          onClick={() => {
                            setSubStateFilter(active ? 'ALL' : st);
                            setSubPage(1);
                          }}
                          className={`csg-pill${active ? ' csg-pill--active' : ''}`}
                        >
                          <span
                            className="csg-pill-dot"
                            style={{ backgroundColor: cfg.color }}
                          />
                          <span>
                            {cfg.label} ({subStateCounts[st]})
                          </span>
                        </button>
                      );
                    })}
                    <button
                      type="button"
                      onClick={() => {
                        setOnlySelectedSubServices((prev) => !prev);
                        setSubPage(1);
                      }}
                      title="仅看关联已选服务的订阅边"
                      className={`csg-pill${onlySelectedSubServices ? ' csg-pill--active' : ''}`}
                    >
                      关联已选
                    </button>
                  </div>

                  {hasActiveSubFilter && (
                    <button
                      type="button"
                      onClick={resetSubFilters}
                      className="csg-filter-reset-btn"
                      title="重置筛选"
                      aria-label="重置筛选"
                    >
                      <RotateCcw size={13} />
                    </button>
                  )}
                </div>
              </div>

              {/* 订阅边表格 */}
              <div className="csg-table-card">
                <div className="csg-table-scroll">
                  <table className="csg-table">
                    <thead>
                      <tr>
                        <th style={{ width: '28%' }}>调用方进程</th>
                        <th style={{ width: '36px', textAlign: 'center' }} />
                        <th style={{ width: '50%' }}>目标 CSG 服务名</th>
                        <th style={{ width: '110px' }}>状态</th>
                      </tr>
                    </thead>
                    <tbody>
                      {pagedSubscriptions.length === 0 ? (
                        <tr>
                          <td colSpan={4} className="csg-empty-cell">
                            {subscriptions.length === 0
                              ? '未观测到现网调用订阅边'
                              : '没有符合当前筛选条件的调用订阅边'}
                          </td>
                        </tr>
                      ) : (
                        pagedSubscriptions.map((sub, idx) => {
                          const clientText = `${sub.client_proc || `pid-${sub.client_pid}`} (${sub.client_pid})`;
                          const isTargetSelected = selectedServices.has(sub.service_name);
                          return (
                            <tr
                              key={`${sub.client_pid}-${sub.service_name}-${idx}`}
                              className={isTargetSelected ? 'csg-row--selected' : undefined}
                            >
                              <td>
                                <div className="csg-mono-ellipsis" title={clientText}>
                                  {clientText}
                                </div>
                                {sub.client_addr && (
                                  <div className="csg-sub-meta csg-mono-ellipsis" title={sub.client_addr}>
                                    {sub.client_addr}
                                  </div>
                                )}
                              </td>
                              <td style={{ textAlign: 'center', color: 'var(--color-text-muted)' }}>
                                →
                              </td>
                              <td>
                                <div className="csg-mono-ellipsis" title={sub.service_name}>
                                  {sub.service_name}
                                </div>
                                {isTargetSelected && (
                                  <div className="csg-sub-meta" style={{ color: 'var(--color-primary)' }}>
                                    目标服务已勾选导入
                                  </div>
                                )}
                              </td>
                              <td>{renderStateBadge(sub.state)}</td>
                            </tr>
                          );
                        })
                      )}
                    </tbody>
                  </table>
                </div>

                {/* 订阅边分页条 */}
                <div data-testid="csg-subscriptions-pagination" className="csg-pagination-bar">
                  <span>
                    匹配 <strong>{filteredSubscriptions.length}</strong> / {subscriptions.length}{' '}
                    条订阅边
                    {subPageSize > 0 && filteredSubscriptions.length > 0 && (
                      <>
                        {' '}
                        · 第 {(safeSubPage - 1) * subPageSize + 1}-
                        {Math.min(safeSubPage * subPageSize, filteredSubscriptions.length)} 条
                      </>
                    )}
                  </span>
                  <div className="csg-pagination-controls">
                    <select
                      value={subPageSize}
                      onChange={(e) => {
                        setSubPageSize(Number(e.target.value));
                        setSubPage(1);
                      }}
                      className="csg-filter-select"
                      style={{ height: '26px' }}
                      aria-label="订阅边每页条数"
                    >
                      {PAGE_SIZE_OPTIONS.map((size) => (
                        <option key={size} value={size}>
                          {size === 0 ? '全部显示' : `${size} 条/页`}
                        </option>
                      ))}
                    </select>
                    {subPageSize > 0 && (
                      <>
                        <button
                          type="button"
                          disabled={safeSubPage <= 1}
                          onClick={() => setSubPage((p) => Math.max(1, p - 1))}
                          className="csg-page-btn"
                          aria-label="上一页"
                        >
                          <ChevronLeft size={14} />
                        </button>
                        <span>
                          {safeSubPage} / {subTotalPages}
                        </span>
                        <button
                          type="button"
                          disabled={safeSubPage >= subTotalPages}
                          onClick={() => setSubPage((p) => Math.min(subTotalPages, p + 1))}
                          className="csg-page-btn"
                          aria-label="下一页"
                        >
                          <ChevronRight size={14} />
                        </button>
                      </>
                    )}
                  </div>
                </div>
              </div>
            </>
          )}
        </div>
      )}

      {/* ── Zone C: 底部固定导入操作区 ── */}
      {report && (
        <div data-testid="csg-import-footer" className="csg-footer">
          {isAdmin ? (
            <>
              <div className="csg-footer-config-row">
                <div className="csg-footer-subsystem-group">
                  <label className="csg-footer-label" htmlFor="csg-import-subsystem-select">
                    归属子系统：
                  </label>
                  <select
                    id="csg-import-subsystem-select"
                    value={selectedSubsystemId}
                    onChange={(e) => setSelectedSubsystemId(e.target.value)}
                    data-testid="csg-import-subsystem-select"
                    className="csg-subsystem-select"
                  >
                    <option value="">请选择子系统</option>
                    {subsystems.map((sub) => (
                      <option key={sub.id} value={String(sub.id)}>
                        {sub.name} ({sub.code})
                      </option>
                    ))}
                  </select>
                  <button
                    type="button"
                    disabled={creatingSubsystem}
                    onClick={() => setShowCreateSubsystem((prev) => !prev)}
                    data-testid="csg-toggle-create-subsystem-btn"
                    className="csg-subsystem-btn"
                  >
                    <Plus size={13} />
                    新建子系统
                  </button>
                </div>

                <label className="csg-deps-toggle">
                  <input
                    type="checkbox"
                    checked={importDeps}
                    onChange={(e) => setImportDeps(e.target.checked)}
                  />
                  <span>同时导入现网订阅依赖边</span>
                  <span className="csg-deps-hint">(含分层守卫)</span>
                </label>
              </div>

              {subsystemsError && (
                <div
                  data-testid="csg-subsystems-error-banner"
                  className="csg-banner-warning"
                  style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}
                >
                  <span>子系统列表加载失败：{subsystemsError}</span>
                  <button
                    type="button"
                    onClick={() => void loadSubsystems()}
                    className="csg-page-btn"
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
                  className="csg-quick-create-form"
                >
                  <input
                    type="text"
                    value={newSubCode}
                    onChange={(e) => setNewSubCode(e.target.value)}
                    placeholder="子系统编码 (如 LOCAL_DEMO)"
                    data-testid="csg-new-subsystem-code-input"
                    className="csg-search-input"
                    style={{
                      width: '180px',
                      padding: '0 10px',
                      fontFamily: 'var(--font-family-mono, monospace)',
                    }}
                  />
                  <input
                    type="text"
                    value={newSubName}
                    onChange={(e) => setNewSubName(e.target.value)}
                    placeholder="子系统名称 (如 local-demo 子系统)"
                    data-testid="csg-new-subsystem-name-input"
                    className="csg-search-input"
                    style={{ flex: 1, minWidth: '180px', padding: '0 10px' }}
                  />
                  <button
                    type="button"
                    disabled={creatingSubsystem}
                    onClick={() => void handleQuickCreateSubsystem()}
                    data-testid="csg-quick-create-subsystem-submit"
                    className="csg-btn-primary"
                    style={{ height: '32px', padding: '0 12px', fontSize: '12px' }}
                  >
                    {creatingSubsystem ? '创建中...' : '创建并选中'}
                  </button>
                  {subsystems.length > 0 && (
                    <button
                      type="button"
                      disabled={creatingSubsystem}
                      onClick={() => setShowCreateSubsystem(false)}
                      className="csg-btn-secondary"
                      style={{ height: '32px', padding: '0 10px', fontSize: '12px' }}
                    >
                      取消
                    </button>
                  )}
                </div>
              )}

              <div className="csg-footer-action-row">
                <div className="csg-footer-selection-summary">
                  <span>
                    已选待导入服务{' '}
                    <strong style={{ color: 'var(--color-primary)' }}>
                      {selectedServices.size}
                    </strong>{' '}
                    / {report.summary.undeclared_count} 个
                  </span>
                  {importDeps && relatedSelectedUndeclaredSubCount > 0 && (
                    <span className="csg-deps-hint">
                      · 预计关联导入 {relatedSelectedUndeclaredSubCount} 条未纳管订阅边
                    </span>
                  )}
                  {selectedServices.size > 0 && (
                    <button
                      type="button"
                      onClick={clearAllSelected}
                      className="csg-link-btn csg-link-btn--muted"
                    >
                      清空已选
                    </button>
                  )}
                </div>

                <div className="csg-footer-buttons">
                  {onClose && (
                    <button type="button" onClick={onClose} className="csg-btn-secondary">
                      关闭
                    </button>
                  )}
                  <button
                    type="button"
                    onClick={() => void handleImport()}
                    disabled={importDisabled}
                    data-testid="csg-import-blueprint-btn"
                    className="csg-btn-primary"
                  >
                    <Download size={14} />
                    {importing ? '导入中...' : `一键导入 SSOT 蓝图 (${selectedServices.size})`}
                  </button>
                </div>
              </div>
            </>
          ) : (
            <div className="csg-footer-action-row" style={{ borderTop: 'none', paddingTop: 0 }}>
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
              {onClose && (
                <div className="csg-footer-buttons">
                  <button type="button" onClick={onClose} className="csg-btn-secondary">
                    关闭
                  </button>
                </div>
              )}
            </div>
          )}
        </div>
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
      width="min(1080px, 94vw)"
      bodyStyle={{ padding: 0, overflow: 'hidden', display: 'flex', flexDirection: 'column' }}
    >
      <CSGBlueprintImportContent agent={agent} onImported={onImported} onClose={onClose} />
    </Drawer>
  );
};
