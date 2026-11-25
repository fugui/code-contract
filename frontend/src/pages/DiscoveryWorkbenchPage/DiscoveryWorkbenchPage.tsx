import React, { useCallback, useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Modal } from '@code/common';
import * as agentApi from '../../api/agent';
import * as discoveryApi from '../../api/discovery';
import { ApiRequestError } from '../../api/client';
import { useToast } from '../../utils/toast';
import type {
  Agent,
  DiscoveryLiveRespDTO,
  DiscoverySnapshotDiffDTO,
  DiscoverySnapshotSummaryDTO,
  EnrollItemInput,
  EnrollValidationErrorPayload,
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
  const [enrollOverrides, setEnrollOverrides] = useState<Record<string, Partial<EnrollItemInput>>>(
    {}
  );
  const [disambiguationPayload, setDisambiguationPayload] =
    useState<EnrollValidationErrorPayload | null>(initialDisambiguation);

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
          items,
        });
        toast.success(
          `成功零侵扰纳管 ${res.enrolled.length} 个进程（放置 ${res.placements_count}，订阅 ${res.dependencies_count}）`
        );
        setEnrollModalOpen(false);
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
    [selectedAgentId, selectedCsgAddr, selectedKeys, enrollOverrides, toast]
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
              onClearSelection={() => setSelectedKeys(new Set())}
              onOpenEnrollModal={() => setEnrollModalOpen(true)}
            />
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
    </div>
  );
};

export default DiscoveryWorkbenchPage;
