import React, { useCallback, useEffect, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Modal } from '@code/common';
import { useModulePath } from '../../hooks/useModulePath';
import * as agentApi from '../../api/agent';
import * as archApi from '../../api/arch';
import * as discoveryApi from '../../api/discovery';
import { ApiRequestError } from '../../api/client';
import { useToast } from '../../utils/toast';
import type {
  Agent,
  ArchProcessGroup,
  DiscoveryLiveRespDTO,
  DiscoveryProcessGroupDTO,
  DiscoverySnapshotDiffDTO,
  DiscoverySnapshotSummaryDTO,
  EnrollItemInput,
  EnrollValidationErrorPayload,
  QuickRecordResultDTO,
  SimulationPlanResultDTO,
} from '../../types';
import { DiscoveryTopBar } from './DiscoveryTopBar';
import { DiscoveryProcessTreeTable } from './DiscoveryProcessTreeTable';
import { DiscoveryLiveDepGraph } from './DiscoveryLiveDepGraph';
import { DiscoverySnapshotDiffDrawer } from './DiscoverySnapshotDiffDrawer';
import { DiscoveryBatchActionBar } from './DiscoveryBatchActionBar';
import { EnrollProcessModal } from './EnrollProcessModal';
import { IdentityDisambiguationModal } from './IdentityDisambiguationModal';
import './DiscoveryWorkbench.css';

export interface DiscoveryWorkbenchPageProps {
  initialAgents?: Agent[];
  initialLiveData?: DiscoveryLiveRespDTO | null;
  initialSnapshots?: DiscoverySnapshotSummaryDTO[];
  initialDiffResult?: DiscoverySnapshotDiffDTO | null;
  initialDrawerOpen?: boolean;
  initialViewMode?: 'tree' | 'graph';
  initialScopeTab?: 'local' | 'ldc';
  initialSearch?: string;
  initialSelectedKeys?: string[];
  initialEnrollModalOpen?: boolean;
  initialDisambiguation?: EnrollValidationErrorPayload | null;
}

export const DiscoveryWorkbenchPage: React.FC<DiscoveryWorkbenchPageProps> = ({
  initialAgents,
  initialLiveData = null,
  initialSnapshots,
  initialDiffResult = null,
  initialDrawerOpen = false,
  initialViewMode,
  initialScopeTab,
  initialSearch,
  initialSelectedKeys,
  initialEnrollModalOpen = false,
  initialDisambiguation = null,
}) => {
  const toast = useToast();
  const resolvePath = useModulePath();
  const [searchParams, setSearchParams] = useSearchParams();

  const [agents, setAgents] = useState<Agent[]>(initialAgents ?? []);
  const [selectedAgentId, setSelectedAgentId] = useState<string>(() => {
    const qAgent = searchParams.get('agent_id');
    if (qAgent) return qAgent;
    if (initialLiveData?.agent_id) return String(initialLiveData.agent_id);
    if (initialAgents && initialAgents.length > 0) return String(initialAgents[0].id);
    return '';
  });
  const [selectedCsgAddr, setSelectedCsgAddr] = useState<string>(
    () => searchParams.get('csg_addr') ?? initialLiveData?.csg_addr ?? ''
  );
  const [viewMode, setViewMode] = useState<'tree' | 'graph'>(() => {
    if (initialViewMode) return initialViewMode;
    return searchParams.get('mode') === 'graph' ? 'graph' : 'tree';
  });
  const [scopeTab, setScopeTab] = useState<'local' | 'ldc'>(() => {
    if (initialScopeTab) return initialScopeTab;
    return searchParams.get('scope_tab') === 'ldc' ? 'ldc' : 'local';
  });
  const [searchQuery, setSearchQuery] = useState<string>(
    () => initialSearch ?? searchParams.get('search') ?? ''
  );

  const [liveData, setLiveData] = useState<DiscoveryLiveRespDTO | null>(initialLiveData);
  const [loadingLive, setLoadingLive] = useState(false);
  const [errorMsg, setErrorMsg] = useState('');
  const [quickRecording, setQuickRecording] = useState(false);
  const [quickRecordResult, setQuickRecordResult] = useState<QuickRecordResultDTO | null>(null);
  const [simPlanModalOpen, setSimPlanModalOpen] = useState(false);
  const [loadingSimPlan, setLoadingSimPlan] = useState(false);
  const [simPlanResult, setSimPlanResult] = useState<SimulationPlanResultDTO | null>(null);
  const [simPlanError, setSimPlanError] = useState('');

  const [drawerOpen, setDrawerOpen] = useState(initialDrawerOpen);
  const [snapshots, setSnapshots] = useState<DiscoverySnapshotSummaryDTO[]>(
    initialSnapshots ?? []
  );
  const [snapshotsTotal, setSnapshotsTotal] = useState<number>(
    initialSnapshots ? initialSnapshots.length : 0
  );
  const [snapPage, setSnapPage] = useState(1);
  const [snapPageSize, setSnapPageSize] = useState(15);
  const [loadingSnapshots, setLoadingSnapshots] = useState(false);

  const [diffLeftId, setDiffLeftId] = useState<string>(
    () => searchParams.get('diff_left') ?? ''
  );
  const [diffRightId, setDiffRightId] = useState<string>(
    () => searchParams.get('diff_right') ?? ''
  );
  const [diffLiveAgentId, setDiffLiveAgentId] = useState<string>('');
  const [diffResult, setDiffResult] = useState<DiscoverySnapshotDiffDTO | null>(
    initialDiffResult
  );
  const [loadingDiff, setLoadingDiff] = useState(false);

  const [saveModalOpen, setSaveModalOpen] = useState(false);
  const [snapshotLabel, setSnapshotLabel] = useState('');
  const [snapshotPinned, setSnapshotPinned] = useState(false);
  const [savingSnapshot, setSavingSnapshot] = useState(false);

  const [selectedKeys, setSelectedKeys] = useState<Set<string>>(
    () => new Set(initialSelectedKeys ?? [])
  );
  const [enrollModalOpen, setEnrollModalOpen] = useState(initialEnrollModalOpen);
  const [enrolling, setEnrolling] = useState(false);
  const [enrollTargetGroupId, setEnrollTargetGroupId] = useState('');
  const [processGroups, setProcessGroups] = useState<ArchProcessGroup[]>([]);
  const [enrollOverrides, setEnrollOverrides] = useState<Record<string, Partial<EnrollItemInput>>>(
    {}
  );
  const [disambiguationPayload, setDisambiguationPayload] =
    useState<EnrollValidationErrorPayload | null>(initialDisambiguation);

  useEffect(() => {
    if (!enrollModalOpen) return;
    let active = true;
    void (async () => {
      try {
        const res = await archApi.listArchProcessGroups();
        if (active) {
          setProcessGroups(res.items ?? []);
        }
      } catch {
        // 加载失败时静默降级为默认暂存组，不阻塞纳管流程
      }
    })();
    return () => {
      active = false;
    };
  }, [enrollModalOpen]);

  const syncQuery = useCallback(
    (patch: Record<string, string | null>) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          Object.entries(patch).forEach(([k, v]) => {
            if (v === null || v === '') {
              next.delete(k);
            } else {
              next.set(k, v);
            }
          });
          return next;
        },
        { replace: true }
      );
    },
    [setSearchParams]
  );

  useEffect(() => {
    if (initialAgents !== undefined) return;
    let active = true;
    void (async () => {
      try {
        const res = await agentApi.listAgents({ page: 1, page_size: 100 });
        if (!active) return;
        const list = res.items ?? [];
        setAgents(list);
        if (!selectedAgentId && list.length > 0) {
          const firstId = String(list[0].id);
          setSelectedAgentId(firstId);
        }
      } catch (err) {
        if (active) {
          setErrorMsg(err instanceof Error ? err.message : '加载 Agent 列表失败');
        }
      }
    })();
    return () => {
      active = false;
    };
  }, [initialAgents, selectedAgentId]);

  const loadLive = useCallback(
    async (agentId: string, csgAddr: string, refresh: boolean) => {
      if (!agentId) return;
      setLoadingLive(true);
      setErrorMsg('');
      try {
        const resp = await discoveryApi.getLiveDiscovery({
          agent_id: agentId,
          csg_addr: csgAddr || undefined,
          refresh,
        });
        setLiveData(resp);
        if (resp.csg_addr && !csgAddr) {
          setSelectedCsgAddr(resp.csg_addr);
        }
      } catch (err) {
        setErrorMsg(err instanceof Error ? err.message : '加载现网发现实况失败');
      } finally {
        setLoadingLive(false);
      }
    },
    []
  );

  useEffect(() => {
    if (initialLiveData !== null || !selectedAgentId) return;
    void loadLive(selectedAgentId, selectedCsgAddr, false);
  }, [initialLiveData, selectedAgentId, selectedCsgAddr, loadLive]);

  const loadSnapshots = useCallback(
    async (agentId: string, page: number, pageSize: number) => {
      if (!agentId) return;
      setLoadingSnapshots(true);
      try {
        const res = await discoveryApi.listDiscoverySnapshots({
          agent_id: agentId,
          csg_addr: selectedCsgAddr || undefined,
          page,
          page_size: pageSize,
        });
        setSnapshots(res.items ?? []);
        setSnapshotsTotal(res.total ?? 0);
      } catch (err) {
        toast.error(err instanceof Error ? err.message : '加载快照列表失败');
      } finally {
        setLoadingSnapshots(false);
      }
    },
    [selectedCsgAddr, toast]
  );

  useEffect(() => {
    if (!drawerOpen || !selectedAgentId || initialSnapshots !== undefined) return;
    void loadSnapshots(selectedAgentId, snapPage, snapPageSize);
  }, [drawerOpen, selectedAgentId, snapPage, snapPageSize, initialSnapshots, loadSnapshots]);

  const handleSaveSnapshot = async () => {
    if (!selectedAgentId) return;
    setSavingSnapshot(true);
    try {
      await discoveryApi.createDiscoverySnapshot({
        agent_id: selectedAgentId,
        csg_addr: selectedCsgAddr || liveData?.csg_addr || undefined,
        label: snapshotLabel.trim() || undefined,
        is_pinned: snapshotPinned,
      });
      toast.success('现网快照已保存');
      setSaveModalOpen(false);
      setSnapshotLabel('');
      setSnapshotPinned(false);
      if (drawerOpen) {
        void loadSnapshots(selectedAgentId, 1, snapPageSize);
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '保存快照失败');
    } finally {
      setSavingSnapshot(false);
    }
  };

  const handleRunDiff = async () => {
    if (!diffLeftId || (!diffRightId && !diffLiveAgentId)) return;
    setLoadingDiff(true);
    try {
      const res = await discoveryApi.diffDiscoverySnapshots({
        left_snapshot_id: diffLeftId,
        right_snapshot_id: diffRightId || undefined,
        live_agent_id: diffLiveAgentId || undefined,
        csg_addr: selectedCsgAddr || undefined,
      });
      setDiffResult(res);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '快照对比失败');
    } finally {
      setLoadingDiff(false);
    }
  };

  const selectedAgent = agents.find((a) => String(a.id) === String(selectedAgentId)) ?? null;
  const summary = liveData?.summary;
  const selectedProcesses = (liveData?.processes ?? []).filter((p) =>
    selectedKeys.has(p.process_key)
  );

  const submitEnroll = useCallback(
    async (nextOverrides?: Record<string, Partial<EnrollItemInput>>) => {
      if (!selectedAgentId || selectedKeys.size === 0) return;
      const overridesMap = nextOverrides ?? enrollOverrides;
      setEnrolling(true);
      try {
        const items: EnrollItemInput[] = Array.from(selectedKeys).map((pk) => ({
          process_key: pk,
          ...(overridesMap[pk] ?? {}),
        }));
        const res = await discoveryApi.enrollDiscoveryProcesses({
          agent_id: selectedAgentId,
          csg_addr: selectedCsgAddr || undefined,
          target_group_id: enrollTargetGroupId || undefined,
          items,
        });
        toast.success(
          `成功零侵扰纳管 ${res.enrolled.length} 个进程（放置 ${res.placements_count}，订阅 ${res.dependencies_count}）`
        );
        setEnrollModalOpen(false);
        setEnrollTargetGroupId('');
        setDisambiguationPayload(null);
        setSelectedKeys(new Set());
        setEnrollOverrides({});
        const refreshed = await discoveryApi.getLiveDiscovery({
          agent_id: selectedAgentId,
          csg_addr: selectedCsgAddr || undefined,
        });
        setLiveData(refreshed);
      } catch (err) {
        if (err instanceof ApiRequestError && err.status === 422 && err.data && typeof err.data === 'object') {
          const payload = err.data as EnrollValidationErrorPayload;
          if (
            payload.code === 'IDENTITY_AMBIGUOUS' ||
            payload.code === 'LOW_CONFIDENCE_CONFIRM_REQUIRED'
          ) {
            setEnrollModalOpen(false);
            setDisambiguationPayload(payload);
            return;
          }
        }
        toast.error(err instanceof Error ? err.message : '物理纳管提交失败');
      } finally {
        setEnrolling(false);
      }
    },
    [selectedAgentId, selectedCsgAddr, enrollTargetGroupId, selectedKeys, enrollOverrides, toast]
  );

  const handleQuickRecord = useCallback(async () => {
    if (!selectedAgentId || selectedKeys.size === 0 || quickRecording) return;
    setQuickRecording(true);
    setQuickRecordResult(null);
    const reqId = `qr-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    try {
      const res = await discoveryApi.quickRecordDiscovery({
        agent_id: selectedAgentId,
        csg_addr: selectedCsgAddr || undefined,
        process_keys: Array.from(selectedKeys),
        request_id: reqId,
      });
      setQuickRecordResult(res);
      toast.success(`已启动一键直录会话 #${res.session.id}`);
      try {
        const refreshed = await discoveryApi.getLiveDiscovery({
          agent_id: selectedAgentId,
          csg_addr: selectedCsgAddr || undefined,
        });
        setLiveData(refreshed);
      } catch {
        // 保持当前实况视图，避免刷新波动覆盖直录结果横幅
      }
    } catch (err) {
      if (err instanceof ApiRequestError && err.status === 422 && err.data && typeof err.data === 'object') {
        const payload = err.data as EnrollValidationErrorPayload;
        if (
          payload.code === 'IDENTITY_AMBIGUOUS' ||
          payload.code === 'LOW_CONFIDENCE_CONFIRM_REQUIRED'
        ) {
          setDisambiguationPayload(payload);
          return;
        }
      }
      toast.error(err instanceof Error ? err.message : '一键直录启动失败');
    } finally {
      setQuickRecording(false);
    }
  }, [selectedAgentId, selectedCsgAddr, selectedKeys, quickRecording, toast]);

  const handleOpenSimulationPlan = useCallback(
    async (group: DiscoveryProcessGroupDTO) => {
      if (!selectedAgentId) return;
      setSimPlanModalOpen(true);
      setLoadingSimPlan(true);
      setSimPlanResult(null);
      setSimPlanError('');
      try {
        const plan = await discoveryApi.buildDiscoverySimulationPlan({
          agent_id: selectedAgentId,
          arch_process_id: group.arch_process_id || undefined,
          process_key: group.process_key,
          csg_addr: selectedCsgAddr || undefined,
        });
        setSimPlanResult(plan);
      } catch (err) {
        setSimPlanError(err instanceof Error ? err.message : '仿真策略推导失败');
      } finally {
        setLoadingSimPlan(false);
      }
    },
    [selectedAgentId, selectedCsgAddr]
  );

  return (
    <div className="discovery-workbench-page" data-testid="discovery-workbench-page">
      <DiscoveryTopBar
        agents={agents}
        selectedAgentId={selectedAgentId}
        onSelectAgent={(nextId) => {
          setSelectedAgentId(nextId);
          setSelectedCsgAddr('');
          setLiveData(null);
          setSelectedKeys(new Set());
          setEnrollOverrides({});
          syncQuery({ agent_id: nextId, csg_addr: null });
        }}
        selectedCsgAddr={selectedCsgAddr}
        onSelectCsgAddr={(addr) => {
          setSelectedCsgAddr(addr);
          syncQuery({ csg_addr: addr });
          void loadLive(selectedAgentId, addr, false);
        }}
        liveData={liveData}
        loading={loadingLive}
        errorMsg={errorMsg}
        viewMode={viewMode}
        onChangeViewMode={(mode) => {
          setViewMode(mode);
          syncQuery({ mode });
        }}
        onRefresh={() => void loadLive(selectedAgentId, selectedCsgAddr, true)}
        onOpenSaveSnapshot={() => setSaveModalOpen(true)}
        onOpenSnapshotDrawer={() => setDrawerOpen(true)}
      />

      {summary && (
        <div className="discovery-summary-strip" data-testid="discovery-summary-strip">
          <div className="discovery-stat-card">
            <span className="discovery-stat-label">本机进程</span>
            <span className="discovery-stat-value">{summary.process_count}</span>
          </div>
          <div className="discovery-stat-card">
            <span className="discovery-stat-label">纯客户端进程</span>
            <span className="discovery-stat-value">{summary.client_only_count}</span>
          </div>
          <div className="discovery-stat-card">
            <span className="discovery-stat-label">现网服务数</span>
            <span className="discovery-stat-value">{summary.total_services}</span>
          </div>
          <div className="discovery-stat-card">
            <span className="discovery-stat-label">订阅下游数</span>
            <span className="discovery-stat-value">{summary.total_subscriptions}</span>
          </div>
          <div className="discovery-stat-card">
            <span className="discovery-stat-label">跨板 LDC 节点</span>
            <span className="discovery-stat-value">{summary.remote_ldc_count}</span>
          </div>
        </div>
      )}

      {viewMode === 'tree' ? (
        <>
          {scopeTab === 'local' && (
            <DiscoveryBatchActionBar
              selectedCount={selectedKeys.size}
              enrolling={enrolling}
              quickRecording={quickRecording}
              onClearSelection={() => setSelectedKeys(new Set())}
              onOpenEnrollModal={() => setEnrollModalOpen(true)}
              onQuickRecord={() => void handleQuickRecord()}
            />
          )}
          {quickRecordResult && (
            <div
              className="discovery-quick-record-banner"
              data-testid="discovery-quick-record-banner"
            >
              <div className="discovery-quick-record-banner__main">
                <strong>
                  直录会话 #{quickRecordResult.session.id} ({quickRecordResult.session.name}) 已就绪
                </strong>
                <span className="discovery-mono">
                  驱动: {(quickRecordResult.target_drivers || []).join(', ') || 'builtin-tcp'} ·
                  自动纳管 {quickRecordResult.enrolled_count} 个进程
                </span>
                <Link
                  to={resolvePath(`/record?session_id=${quickRecordResult.session.id}`)}
                  className="code-btn code-btn--primary code-btn--xs"
                  data-testid="discovery-quick-record-goto-session"
                >
                  前往录制工作台查看
                </Link>
              </div>
              {quickRecordResult.coverage_warnings &&
                quickRecordResult.coverage_warnings.length > 0 && (
                  <ul className="discovery-quick-record-warnings">
                    {quickRecordResult.coverage_warnings.map((w, idx) => (
                      <li
                        key={`${w.code}-${w.process_id}-${idx}`}
                        data-testid={
                          w.code === 'NEED_RECONNECT'
                            ? 'discovery-need-reconnect-warning'
                            : `discovery-coverage-warning-${w.code}`
                        }
                        className={
                          w.code === 'NEED_RECONNECT'
                            ? 'discovery-warning-item discovery-warning-item--danger'
                            : 'discovery-warning-item'
                        }
                      >
                        <strong>[{w.code}]</strong> {w.process_name}: {w.message}
                      </li>
                    ))}
                  </ul>
                )}
            </div>
          )}
          <DiscoveryProcessTreeTable
            localProcesses={liveData?.processes ?? []}
            remoteLdcProcesses={liveData?.remote_ldc_processes ?? []}
            scopeTab={scopeTab}
            onChangeScopeTab={(tab) => {
              setScopeTab(tab);
              syncQuery({ scope_tab: tab });
            }}
            searchQuery={searchQuery}
            onChangeSearchQuery={(q) => {
              setSearchQuery(q);
              syncQuery({ search: q || null });
            }}
            selectedKeys={selectedKeys}
            onToggleSelectKey={(pk) => {
              setSelectedKeys((prev) => {
                const next = new Set(prev);
                if (next.has(pk)) next.delete(pk);
                else next.add(pk);
                return next;
              });
            }}
            onToggleSelectAll={(keys, checked) => {
              setSelectedKeys((prev) => {
                const next = new Set(prev);
                for (const k of keys) {
                  if (checked) next.add(k);
                  else next.delete(k);
                }
                return next;
              });
            }}
            onOpenSimulationPlan={(grp) => void handleOpenSimulationPlan(grp)}
          />
        </>
      ) : (
        <DiscoveryLiveDepGraph
          localProcesses={liveData?.processes ?? []}
          remoteLdcProcesses={liveData?.remote_ldc_processes ?? []}
        />
      )}

      <DiscoverySnapshotDiffDrawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        agent={selectedAgent}
        allAgents={agents}
        snapshots={snapshots}
        snapshotsTotal={snapshotsTotal}
        page={snapPage}
        pageSize={snapPageSize}
        onPageChange={(p) => setSnapPage(p)}
        onPageSizeChange={(ps) => {
          setSnapPageSize(ps);
          setSnapPage(1);
        }}
        loadingSnapshots={loadingSnapshots}
        diffResult={diffResult}
        loadingDiff={loadingDiff}
        diffLeftId={diffLeftId}
        diffRightId={diffRightId}
        diffLiveAgentId={diffLiveAgentId}
        onChangeDiffTargets={(next) => {
          if (next.leftId !== undefined) {
            setDiffLeftId(next.leftId);
            syncQuery({ diff_left: next.leftId || null });
          }
          if (next.rightId !== undefined) {
            setDiffRightId(next.rightId);
            syncQuery({ diff_right: next.rightId || null });
          }
          if (next.liveAgentId !== undefined) {
            setDiffLiveAgentId(next.liveAgentId);
          }
        }}
        onRunDiff={() => void handleRunDiff()}
        onUpdateSnapshot={async (id, data) => {
          await discoveryApi.updateDiscoverySnapshot(id, data);
          toast.success('快照已更新');
          await loadSnapshots(selectedAgentId, snapPage, snapPageSize);
        }}
        onDeleteSnapshot={async (id) => {
          await discoveryApi.deleteDiscoverySnapshot(id);
          toast.success('快照已删除');
          await loadSnapshots(selectedAgentId, snapPage, snapPageSize);
        }}
      />

      <Modal
        open={saveModalOpen}
        onClose={() => setSaveModalOpen(false)}
        title="保存现网发现快照"
        width="sm"
        footer={
          <div className="code-modal-footer">
            <button
              type="button"
              className="code-btn code-btn--secondary"
              onClick={() => setSaveModalOpen(false)}
            >
              取消
            </button>
            <button
              type="button"
              className="code-btn code-btn--primary"
              disabled={savingSnapshot}
              onClick={() => void handleSaveSnapshot()}
            >
              {savingSnapshot ? '保存中...' : '确认保存'}
            </button>
          </div>
        }
      >
        <div className="code-form">
          <div className="code-form-item">
            <label className="code-form-label">快照标签 (可选):</label>
            <input
              type="text"
              className="code-input"
              value={snapshotLabel}
              onChange={(e) => setSnapshotLabel(e.target.value)}
              placeholder="例如 v2.3.0-联调基线"
            />
          </div>
          <label style={{ display: 'inline-flex', alignItems: 'center', gap: 8, fontSize: 13 }}>
            <input
              type="checkbox"
              checked={snapshotPinned}
              onChange={(e) => setSnapshotPinned(e.target.checked)}
            />
            <span>置顶为基线（免疫 50 条 FIFO 滚动清理，上限 20 条）</span>
          </label>
        </div>
      </Modal>

      <EnrollProcessModal
        open={enrollModalOpen}
        agent={selectedAgent}
        selectedProcesses={selectedProcesses}
        processGroups={processGroups}
        targetGroupId={enrollTargetGroupId}
        onTargetGroupChange={setEnrollTargetGroupId}
        submitting={enrolling}
        onClose={() => setEnrollModalOpen(false)}
        onConfirm={() => void submitEnroll()}
      />

      <IdentityDisambiguationModal
        payload={disambiguationPayload}
        processGroup={
          (liveData?.processes ?? []).find(
            (p) => p.process_key === disambiguationPayload?.process_key
          ) ?? null
        }
        submitting={enrolling}
        onClose={() => setDisambiguationPayload(null)}
        onResolveAmbiguous={(processKey, archProcessId) => {
          const nextOverrides = {
            ...enrollOverrides,
            [processKey]: {
              ...(enrollOverrides[processKey] ?? {}),
              force_arch_process_id: archProcessId,
            },
          };
          setEnrollOverrides(nextOverrides);
          void submitEnroll(nextOverrides);
        }}
        onConfirmLowConfidence={(processKey) => {
          const nextOverrides = {
            ...enrollOverrides,
            [processKey]: {
              ...(enrollOverrides[processKey] ?? {}),
              confirm_low_confidence: true,
            },
          };
          setEnrollOverrides(nextOverrides);
          void submitEnroll(nextOverrides);
        }}
      />

      <Modal
        open={simPlanModalOpen}
        onClose={() => setSimPlanModalOpen(false)}
        title="CSG 三模仿真策略预研"
        width="lg"
        footer={
          <div className="code-modal-footer">
            <button
              type="button"
              className="code-btn code-btn--secondary"
              onClick={() => setSimPlanModalOpen(false)}
            >
              关闭
            </button>
            {simPlanResult && (
              <Link
                to={resolvePath(
                  `/replay?agent_id=${simPlanResult.agent_id}&arch_process_id=${simPlanResult.arch_process_id}&strategy=${encodeURIComponent(simPlanResult.recommended_strategy)}`
                )}
                className="code-btn code-btn--primary"
                data-testid="sim-plan-goto-replay-btn"
                onClick={() => setSimPlanModalOpen(false)}
              >
                前往重放工作台执行 ({simPlanResult.recommended_strategy})
              </Link>
            )}
          </div>
        }
      >
        {loadingSimPlan ? (
          <div className="discovery-modal-notice">正在结合同环境 CSG 在线订阅推导最佳仿真策略...</div>
        ) : simPlanError ? (
          <div className="discovery-modal-notice" style={{ color: 'var(--color-danger)' }}>
            {simPlanError}
          </div>
        ) : simPlanResult ? (
          <div className="discovery-modal-body" data-testid="discovery-sim-plan-modal-body">
            <div className="discovery-modal-notice">
              <div>
                <strong>目标进程：</strong>
                <span>{simPlanResult.process_name}</span>
                {' · '}
                <strong>推荐策略：</strong>
                <span className="discovery-pill discovery-pill--primary">
                  {simPlanResult.recommended_strategy}
                </span>
              </div>
              <div className="discovery-modal-subnotice">{simPlanResult.rationale}</div>
            </div>
            {simPlanResult.downstream_statuses && simPlanResult.downstream_statuses.length > 0 && (
              <table className="discovery-subtable">
                <thead>
                  <tr>
                    <th>下游服务名</th>
                    <th>制式</th>
                    <th>在线状态</th>
                    <th>端点 / 说明</th>
                  </tr>
                </thead>
                <tbody>
                  {simPlanResult.downstream_statuses.map((ds) => (
                    <tr key={ds.service_name}>
                      <td className="discovery-mono">{ds.service_name}</td>
                      <td>{ds.transport}</td>
                      <td>{ds.is_online ? '在线' : '离线'}</td>
                      <td className="discovery-mono">{ds.resolved_endpoint || ds.note || '-'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        ) : null}
      </Modal>
    </div>
  );
};

export default DiscoveryWorkbenchPage;
