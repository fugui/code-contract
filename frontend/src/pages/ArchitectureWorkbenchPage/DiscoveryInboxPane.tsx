import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { CheckSquare, RefreshCw } from 'lucide-react';
import { Modal, Pagination, useConfirm, usePaginationState } from '@code/common';
import * as archApi from '../../api/arch';
import { subscribeServerEvents } from '../../api/sse';
import { useToast } from '../../utils/toast';
import type {
  ArchInboxResponseDTO,
  AttributionJobDTO,
  AttributionJobEventDTO,
  DependencyDecision,
  DepSuggestionDTO,
  InboxServiceItemDTO,
  Module,
} from '../../types';
import {
  AttributionProgressBar,
  InboxDepSuggestionCard,
  InboxFilterKind,
  InboxProcessGroupList,
  InboxSummaryBar,
} from './DiscoveryInboxParts';
import {
  defaultConsumerModuleId,
  mergeAttributionEvent,
  SplitFormRow,
  submitDependencyDecision,
  submitInboxClaim,
  submitInboxSplit,
  trackAttributionJob,
} from './inboxLogic';
import './DiscoveryInbox.css';

export interface DiscoveryInboxPaneProps {
  isAdmin: boolean;
  modules: Module[];
  onPendingCountChange?: (totalPending: number) => void;
  onGovernanceChanged?: () => void;
}

export const DiscoveryInboxPane: React.FC<DiscoveryInboxPaneProps> = ({
  isAdmin,
  modules,
  onPendingCountChange,
  onGovernanceChanged,
}) => {
  const toast = useToast();
  const confirmDialog = useConfirm();
  const { page, pageSize, setPage, setPageSize, resetPage } = usePaginationState({
    defaultPageSize: 25,
  });

  const [kindFilter, setKindFilter] = useState<InboxFilterKind>('ALL');
  const [searchInput, setSearchInput] = useState('');
  const [searchTerm, setSearchTerm] = useState('');
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [data, setData] = useState<ArchInboxResponseDTO>({
    summary: {
      unclaimed_count: 0,
      multi_host_count: 0,
      placement_drift_count: 0,
      dep_suggestion_count: 0,
      total_pending: 0,
    },
    process_groups: [],
    dep_suggestions: [],
    total: 0,
    page: 1,
    page_size: 25,
  });

  // 批量认领勾选与每个服务对应的目标业务模块
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set());
  const [claimTargets, setClaimTargets] = useState<Record<number, string>>({});

  // 依赖建议消费方模块下拉状态: key = `${service_id}-${consumer_arch_process_id}`
  const [depConsumerTargets, setDepConsumerTargets] = useState<Record<string, number | undefined>>({});

  // 多宿主拆分 Modal 状态
  const [splitTarget, setSplitTarget] = useState<InboxServiceItemDTO | null>(null);
  const [splitDefinerId, setSplitDefinerId] = useState<string>('');
  const [splitRows, setSplitRows] = useState<SplitFormRow[]>([]);

  // 后台归因回补进度
  const [activeJob, setActiveJob] = useState<AttributionJobDTO | null>(null);
  const pollAbortRef = useRef<AbortController | null>(null);

  const contractLibModules = useMemo(
    () => modules.filter((m) => m.module_kind === 'CONTRACT_LIB'),
    [modules]
  );
  const bizModules = useMemo(
    () => modules.filter((m) => (m.module_kind || 'BUSINESS') === 'BUSINESS'),
    [modules]
  );

  const loadInbox = useCallback(async () => {
    setLoading(true);
    try {
      const res = await archApi.getArchInbox({
        kind: kindFilter === 'ALL' ? undefined : kindFilter,
        search: searchTerm || undefined,
        page,
        page_size: pageSize,
      });
      setData(res);
      onPendingCountChange?.(res.summary.total_pending);
      setDepConsumerTargets((prev) => {
        const next = { ...prev };
        for (const s of res.dep_suggestions) {
          const key = `${s.service_id}-${s.consumer_arch_process_id}`;
          if (next[key] === undefined) {
            next[key] = defaultConsumerModuleId(s);
          }
        }
        return next;
      });
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '加载架构收件箱失败');
    } finally {
      setLoading(false);
    }
  }, [kindFilter, onPendingCountChange, page, pageSize, searchTerm, toast]);

  useEffect(() => {
    void loadInbox();
  }, [loadInbox]);

  // 订阅 SSE attribution_job.updated 实时刷新进度
  useEffect(() => {
    const unsubscribe = subscribeServerEvents({
      onMessage: (evt) => {
        if (evt.event !== 'attribution_job.updated' || !evt.data || typeof evt.data !== 'object') {
          return;
        }
        const payload = evt.data as AttributionJobEventDTO;
        setActiveJob((prev) => (prev ? mergeAttributionEvent(prev, payload) : prev));
      },
    });
    return () => {
      unsubscribe();
      pollAbortRef.current?.abort();
    };
  }, []);

  const startTrackingJob = useCallback(
    (jobId?: number) => {
      if (!jobId) return;
      pollAbortRef.current?.abort();
      const ctrl = new AbortController();
      pollAbortRef.current = ctrl;
      void trackAttributionJob(jobId, {
        signal: ctrl.signal,
        onUpdate: (j) => setActiveJob(j),
      }).catch(() => {
        // 轮询中断或失败静默兜底
      });
    },
    []
  );

  const handleToggleSelect = (serviceId: number) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(serviceId)) {
        next.delete(serviceId);
      } else {
        next.add(serviceId);
      }
      return next;
    });
  };

  const handleBatchClaim = async () => {
    const ids = Array.from(selectedIds);
    setBusy(true);
    try {
      const res = await submitInboxClaim(ids, claimTargets);
      toast.success(`已认领 ${res.claimed_count} 项服务`);
      setSelectedIds(new Set());
      startTrackingJob(res.attribution_job_id);
      await loadInbox();
      onGovernanceChanged?.();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '批量认领失败');
    } finally {
      setBusy(false);
    }
  };

  const handleOpenSplit = (item: InboxServiceItemDTO) => {
    const apIds = Array.from(new Set(item.candidate_arch_process_ids));
    const defaultDefiner = contractLibModules[0] ? String(contractLibModules[0].id) : '';
    setSplitTarget(item);
    setSplitDefinerId(defaultDefiner);
    setSplitRows(
      apIds.map((apId, idx) => ({
        arch_process_id: apId,
        module_id: '',
        impl_key: `impl-${idx + 1}`,
      }))
    );
  };

  const handleConfirmSplit = async () => {
    if (!splitTarget) return;
    setBusy(true);
    try {
      const res = await submitInboxSplit({
        sourceServiceId: splitTarget.service_id,
        definerModuleId: splitDefinerId,
        rows: splitRows,
      });
      toast.success(`已拆分为多实现契约 (${res.implementations.length} 个实现)`);
      setSplitTarget(null);
      startTrackingJob(res.attribution_job_id);
      await loadInbox();
      onGovernanceChanged?.();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '多宿主拆分失败');
    } finally {
      setBusy(false);
    }
  };

  const handleUnclaim = async (item: InboxServiceItemDTO) => {
    const ok = await confirmDialog({
      title: '确认取消认领',
      content: `确定将服务 "${item.service_name}" 退回未认领状态？若仍被下游模块依赖引用将阻断操作。`,
      confirmText: '确认取消认领',
      cancelText: '取消',
      type: 'danger',
    });
    if (!ok) return;

    setBusy(true);
    try {
      const res = await archApi.unclaimArchService(item.service_id);
      toast.success(`服务已取消认领 (${res.action})`);
      await loadInbox();
      onGovernanceChanged?.();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '取消认领失败');
    } finally {
      setBusy(false);
    }
  };

  const handleDecideDep = async (sug: DepSuggestionDTO, decision: DependencyDecision) => {
    const key = `${sug.service_id}-${sug.consumer_arch_process_id}`;
    setBusy(true);
    try {
      await submitDependencyDecision(sug, decision, depConsumerTargets[key]);
      toast.success(`已提交依赖裁决: ${decision}`);
      await loadInbox();
      onGovernanceChanged?.();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '依赖裁决失败');
    } finally {
      setBusy(false);
    }
  };

  const handleCancelJob = async (jobId: number) => {
    try {
      const updated = await archApi.cancelAttributionJob(jobId);
      if ('id' in updated) {
        setActiveJob(updated);
      }
      toast.info(`已请求取消归因作业 #${jobId}`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '取消作业失败');
    }
  };

  return (
    <div className="code-inbox-pane" data-testid="discovery-inbox-pane">
      <div className="code-inbox-toolbar">
        <InboxSummaryBar
          summary={data.summary}
          activeKind={kindFilter}
          onChange={(k) => {
            setKindFilter(k);
            resetPage();
          }}
        />

        <div className="code-inbox-toolbar__actions">
          <input
            type="search"
            className="code-input code-inbox-search"
            placeholder="搜索服务名、进程或模块..."
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                setSearchTerm(searchInput.trim());
                resetPage();
              }
            }}
            data-testid="inbox-search-input"
          />
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            disabled={loading}
            onClick={() => {
              setSearchTerm(searchInput.trim());
              void loadInbox();
            }}
            data-testid="inbox-refresh-btn"
          >
            <RefreshCw size={13} />
            <span>刷新</span>
          </button>
          <button
            type="button"
            className="btn btn-primary btn-sm"
            disabled={busy || selectedIds.size === 0}
            onClick={() => void handleBatchClaim()}
            data-testid="inbox-batch-claim-btn"
          >
            <CheckSquare size={13} />
            <span>批量认领 ({selectedIds.size})</span>
          </button>
        </div>
      </div>

      {activeJob && <AttributionProgressBar job={activeJob} onCancel={(id) => void handleCancelJob(id)} />}

      {(kindFilter === 'ALL' ||
        kindFilter === 'UNCLAIMED' ||
        kindFilter === 'MULTI_HOST' ||
        kindFilter === 'PLACEMENT_DRIFT') && (
        <InboxProcessGroupList
          groups={data.process_groups}
          modules={bizModules}
          selected={selectedIds}
          targets={claimTargets}
          isAdmin={isAdmin}
          busy={busy}
          onToggle={handleToggleSelect}
          onTarget={(svcId, modId) =>
            setClaimTargets((prev) => ({ ...prev, [svcId]: modId }))
          }
          onSplit={handleOpenSplit}
          onUnclaim={(item) => void handleUnclaim(item)}
        />
      )}

      {(kindFilter === 'ALL' || kindFilter === 'DEP_SUGGESTION') &&
        data.dep_suggestions.length > 0 && (
          <section className="code-inbox-dep-section" data-testid="inbox-dep-suggestions">
            <h4 className="code-inbox-section-title">
              逻辑依赖反向合成建议 ({data.dep_suggestions.length})
            </h4>
            {data.dep_suggestions.map((sug) => {
              const key = `${sug.service_id}-${sug.consumer_arch_process_id}`;
              return (
                <InboxDepSuggestionCard
                  key={key}
                  suggestion={sug}
                  selectedModuleId={depConsumerTargets[key]}
                  isAdmin={isAdmin}
                  busy={busy}
                  onSelectModule={(modId) =>
                    setDepConsumerTargets((prev) => ({ ...prev, [key]: modId }))
                  }
                  onDecide={(dec) => void handleDecideDep(sug, dec)}
                />
              );
            })}
          </section>
        )}

      {data.total > 0 && (
        <Pagination
          page={page}
          pageSize={pageSize}
          totalItems={data.total}
          onPageChange={setPage}
          onPageSizeChange={(nextSize) => {
            setPageSize(nextSize);
            resetPage();
          }}
          pageSizeOptions={[15, 25, 50, 100]}
        />
      )}

      <Modal
        open={splitTarget !== null}
        onClose={() => setSplitTarget(null)}
        title={`拆分多宿主服务为 MULTI_IMPL 契约 — ${splitTarget?.service_name ?? ''}`}
        width="lg"
        footer={
          <div className="code-inbox-modal-footer">
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setSplitTarget(null)}
            >
              取消
            </button>
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy}
              onClick={() => void handleConfirmSplit()}
              data-testid="inbox-split-confirm-btn"
            >
              确认拆分
            </button>
          </div>
        }
      >
        <div className="code-inbox-split-form">
          <div>
            <label className="code-form-label">契约定义接口库 (CONTRACT_LIB)</label>
            <select
              className="code-input"
              value={splitDefinerId}
              onChange={(e) => setSplitDefinerId(e.target.value)}
              data-testid="inbox-split-definer-select"
            >
              <option value="">请选择接口库模块...</option>
              {contractLibModules.map((m) => (
                <option key={String(m.id)} value={String(m.id)}>
                  {m.name} ({m.code})
                </option>
              ))}
            </select>
          </div>

          {splitRows.map((row, idx) => (
            <div key={row.arch_process_id} className="code-inbox-split-row">
              <span className="code-inbox-code">进程 #{row.arch_process_id}</span>
              <select
                className="code-input"
                value={row.module_id}
                onChange={(e) => {
                  const val = e.target.value;
                  setSplitRows((prev) =>
                    prev.map((r, i) => (i === idx ? { ...r, module_id: val } : r))
                  );
                }}
                data-testid={`inbox-split-mod-${row.arch_process_id}`}
              >
                <option value="">选择承载业务模块...</option>
                {bizModules
                  .filter(
                    (m) => !m.arch_process_id || Number(m.arch_process_id) === row.arch_process_id
                  )
                  .map((m) => (
                    <option key={String(m.id)} value={String(m.id)}>
                      {m.name} ({m.code})
                    </option>
                  ))}
              </select>
              <input
                type="text"
                className="code-input"
                placeholder="impl_key"
                value={row.impl_key}
                onChange={(e) => {
                  const val = e.target.value;
                  setSplitRows((prev) =>
                    prev.map((r, i) => (i === idx ? { ...r, impl_key: val } : r))
                  );
                }}
                data-testid={`inbox-split-key-${row.arch_process_id}`}
              />
            </div>
          ))}
        </div>
      </Modal>
    </div>
  );
};
