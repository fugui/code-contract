import React, { useCallback, useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import {
  AlertTriangle,
  CheckCircle2,
  Eye,
  RefreshCw,
  Search,
  ShieldCheck,
} from 'lucide-react';
import { Drawer, EmptyState, Modal, Pagination } from '@code/common';
import * as schemaApi from '../../api/schema';
import type { BreakingChangeEventDTO } from '../../types';
import { useToast } from '../../utils/toast';

export interface BreakingAuditWorkbenchPaneProps {
  isAdmin: boolean;
  initialEvents?: BreakingChangeEventDTO[];
  initialTotal?: number;
  initialOpenCount?: number;
  initialDetail?: BreakingChangeEventDTO | null;
}

const VALID_PAGE_SIZES = new Set([15, 25, 50, 100]);

function formatAuditTime(iso?: string | null): string {
  if (!iso) return '-';
  const dt = new Date(iso);
  if (Number.isNaN(dt.getTime())) return iso;
  return dt.toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });
}

function getSourceLabel(source: string): string {
  switch (source) {
    case 'PIPELINE_PUBLISH':
      return '流水线发布';
    case 'MANUAL_ACTIVATE':
      return '手动激活';
    case 'BATCH_ACTIVATE':
      return '批量激活';
    case 'AUTO_ACTIVATE':
      return '自动激活';
    default:
      return source || '-';
  }
}

export const BreakingAuditWorkbenchPane: React.FC<BreakingAuditWorkbenchPaneProps> = ({
  isAdmin,
  initialEvents,
  initialTotal,
  initialOpenCount,
  initialDetail = null,
}) => {
  const [searchParams, setSearchParams] = useSearchParams();
  const toast = useToast();

  const rawPage = Number(searchParams.get('ba_page') || '1');
  const page = Number.isFinite(rawPage) && rawPage > 0 ? Math.floor(rawPage) : 1;

  const rawPageSize = Number(searchParams.get('ba_page_size') || '25');
  const pageSize = VALID_PAGE_SIZES.has(rawPageSize) ? rawPageSize : 25;

  const filterStatus = searchParams.get('ba_status') || '';
  const filterSource = searchParams.get('ba_source') || '';
  const filterKeyword = searchParams.get('ba_q') || '';

  const [keywordDraft, setKeywordDraft] = useState(filterKeyword);
  useEffect(() => {
    setKeywordDraft(filterKeyword);
  }, [filterKeyword]);

  const [events, setEvents] = useState<BreakingChangeEventDTO[]>(initialEvents ?? []);
  const [total, setTotal] = useState(initialTotal ?? initialEvents?.length ?? 0);
  const [openCount, setOpenCount] = useState(
    initialOpenCount ??
      (initialEvents ? initialEvents.filter((e) => e.status === 'OPEN').length : 0)
  );
  const [loading, setLoading] = useState(false);

  const [selectedIds, setSelectedIds] = useState<number[]>([]);
  const [detailOpen, setDetailOpen] = useState(Boolean(initialDetail));
  const [detailLoading, setDetailLoading] = useState(false);
  const [selectedDetail, setSelectedDetail] = useState<BreakingChangeEventDTO | null>(initialDetail);

  const [ackNote, setAckNote] = useState('');
  const [acknowledging, setAcknowledging] = useState(false);
  const [batchModalOpen, setBatchModalOpen] = useState(false);
  const [batchNote, setBatchNote] = useState('');

  const updateUrlParams = useCallback(
    (patch: Record<string, string | null>, resetPage = false) => {
      setSearchParams((prev) => {
        const next = new URLSearchParams(prev);
        next.set('gov_tab', 'breaking-audit');
        Object.entries(patch).forEach(([k, v]) => {
          if (v === null || v === '') next.delete(k);
          else next.set(k, v);
        });
        if (resetPage) next.set('ba_page', '1');
        return next;
      });
    },
    [setSearchParams]
  );

  const fetchEvents = useCallback(async () => {
    setLoading(true);
    try {
      const res = await schemaApi.listBreakingChangeEvents({
        page,
        page_size: pageSize,
        status: filterStatus || undefined,
        source: filterSource || undefined,
        q: filterKeyword || undefined,
      });
      setEvents(res.items ?? []);
      setTotal(res.total ?? 0);
      setOpenCount(res.open_count ?? 0);
      setSelectedIds([]);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '加载破坏性变更审计记录失败');
    } finally {
      setLoading(false);
    }
  }, [page, pageSize, filterStatus, filterSource, filterKeyword, toast]);

  useEffect(() => {
    if (initialEvents !== undefined) return;
    void fetchEvents();
  }, [fetchEvents, initialEvents]);

  const handleSearchSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    updateUrlParams({ ba_q: keywordDraft.trim() || null }, true);
  };

  const handleOpenDetail = async (ev: BreakingChangeEventDTO) => {
    setSelectedDetail(ev);
    setAckNote('');
    setDetailOpen(true);
    setDetailLoading(true);
    try {
      const latest = await schemaApi.getBreakingChangeEvent(ev.id);
      setSelectedDetail(latest);
    } catch {
      // 保留列表快照
    } finally {
      setDetailLoading(false);
    }
  };

  const handleAcknowledgeOne = async (ev: BreakingChangeEventDTO, note?: string) => {
    if (!isAdmin) return;
    setAcknowledging(true);
    try {
      const updated = await schemaApi.acknowledgeBreakingChangeEvent(ev.id, note);
      toast.success(`已确认破坏性变更审计事件 #${ev.id}`);
      if (selectedDetail?.id === ev.id) {
        setSelectedDetail(updated);
      }
      setAckNote('');
      await fetchEvents();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '确认审计事件失败');
    } finally {
      setAcknowledging(false);
    }
  };

  const handleBatchAcknowledge = async () => {
    if (!isAdmin || selectedIds.length === 0) return;
    setAcknowledging(true);
    try {
      const res = await schemaApi.batchAcknowledgeBreakingChangeEvents(selectedIds, batchNote);
      toast.success(`已批量确认 ${res.acknowledged_count} 条破坏性变更审计事件`);
      setBatchModalOpen(false);
      setBatchNote('');
      setSelectedIds([]);
      await fetchEvents();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '批量确认失败');
    } finally {
      setAcknowledging(false);
    }
  };

  const openRows = events.filter((e) => e.status === 'OPEN');
  const allOpenSelected =
    openRows.length > 0 && openRows.every((e) => selectedIds.includes(e.id));

  const toggleSelectAllOpen = () => {
    if (allOpenSelected) {
      setSelectedIds([]);
    } else {
      setSelectedIds(openRows.map((e) => e.id));
    }
  };

  const toggleSelectOne = (id: number) => {
    setSelectedIds((prev) =>
      prev.includes(id) ? prev.filter((item) => item !== id) : [...prev, id]
    );
  };

  return (
    <div className="pdoc-card pmrg-workbench" data-testid="breaking-audit-workbench">
      <div className="pdoc-card__header pmrg-header">
        <div>
          <h3 className="pdoc-card__title">契约上线破坏性变更审计</h3>
          <p className="pdoc-card__subtitle">
            契约入库流水线与版本激活优先保障契约真源及时上线，所有检出的 Wire 级不兼容演进及受波及下游模块自动归档于此，供合规复核与确认闭环。
          </p>
        </div>
        <div className="pmrg-header__actions">
          {isAdmin && selectedIds.length > 0 && (
            <button
              type="button"
              className="btn btn-primary"
              data-testid="batch-ack-breaking-btn"
              onClick={() => {
                setBatchNote('');
                setBatchModalOpen(true);
              }}
            >
              <CheckCircle2 size={15} />
              <span>批量确认 ({selectedIds.length})</span>
            </button>
          )}
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => void fetchEvents()}
            disabled={loading}
          >
            <RefreshCw size={15} className={loading ? 'pgit-spin' : ''} />
            <span>刷新审计</span>
          </button>
        </div>
      </div>

      <div className="pmrg-summary-strip">
        <div className="pmrg-stat-chip">
          <span className="pmrg-stat-chip__label">累计审计事件</span>
          <strong className="pmrg-stat-chip__val">{total}</strong>
        </div>
        <div className="pmrg-stat-chip">
          <span className="pmrg-stat-chip__label">待确认 (OPEN)</span>
          <strong className="pmrg-stat-chip__val pmrg-stat-chip__val--danger">{openCount}</strong>
        </div>
        <div className="pmrg-stat-chip">
          <span className="pmrg-stat-chip__label">已确认 (ACKNOWLEDGED)</span>
          <strong className="pmrg-stat-chip__val pmrg-stat-chip__val--success">
            {Math.max(0, total - openCount)}
          </strong>
        </div>
      </div>

      <form className="pmrg-filters" onSubmit={handleSearchSubmit}>
        <select
          className="code-select"
          aria-label="按审计状态筛选"
          value={filterStatus}
          onChange={(e) => updateUrlParams({ ba_status: e.target.value || null }, true)}
        >
          <option value="">全部状态</option>
          <option value="OPEN">待确认 (OPEN)</option>
          <option value="ACKNOWLEDGED">已确认 (ACKNOWLEDGED)</option>
        </select>

        <select
          className="code-select"
          aria-label="按触发来源筛选"
          value={filterSource}
          onChange={(e) => updateUrlParams({ ba_source: e.target.value || null }, true)}
        >
          <option value="">全部来源</option>
          <option value="PIPELINE_PUBLISH">流水线发布</option>
          <option value="MANUAL_ACTIVATE">手动激活</option>
          <option value="BATCH_ACTIVATE">批量激活</option>
          <option value="AUTO_ACTIVATE">自动激活</option>
        </select>

        <div className="pmrg-search-box">
          <input
            type="text"
            className="code-input"
            aria-label="搜索描述集、契约包或触发人"
            placeholder="搜索描述集名称 / 契约包 / 触发人..."
            value={keywordDraft}
            onChange={(e) => setKeywordDraft(e.target.value)}
          />
          <button type="submit" className="btn btn-secondary">
            <Search size={14} />
            <span>搜索</span>
          </button>
        </div>
      </form>

      {loading && events.length === 0 ? (
        <EmptyState compact title="正在加载破坏性变更审计记录..." />
      ) : events.length === 0 ? (
        <EmptyState
          compact
          title="暂无破坏性变更审计记录"
          description="当流水线发布或手动激活新版本契约并检出 Wire 级不兼容变更时，系统会自动在此记录并归档波及下游模块。"
        />
      ) : (
        <div className="pmrg-table-wrap">
          <table className="pmrg-table" data-testid="breaking-audit-table">
            <thead>
              <tr>
                {isAdmin && (
                  <th style={{ width: '36px' }}>
                    <input
                      type="checkbox"
                      aria-label="全选当前页待确认审计事件"
                      checked={allOpenSelected}
                      disabled={openRows.length === 0}
                      onChange={toggleSelectAllOpen}
                    />
                  </th>
                )}
                <th>描述集与版本演进</th>
                <th>契约包与来源</th>
                <th>违规项 / 波及下游</th>
                <th>触发人与时间</th>
                <th>状态</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {events.map((ev) => (
                <tr key={ev.id} data-testid={`breaking-audit-row-${ev.id}`}>
                  {isAdmin && (
                    <td>
                      <input
                        type="checkbox"
                        aria-label={`选择审计事件 #${ev.id}`}
                        checked={selectedIds.includes(ev.id)}
                        disabled={ev.status !== 'OPEN'}
                        onChange={() => toggleSelectOne(ev.id)}
                      />
                    </td>
                  )}
                  <td>
                    <div className="pmrg-mr-cell">
                      <div className="pmrg-mr-cell__top">
                        <button
                          type="button"
                          className="pmrg-title-link"
                          onClick={() => void handleOpenDetail(ev)}
                        >
                          {ev.schema_name}
                        </button>
                        <code className="code-tpl-code-inline">
                          v{ev.from_version} → v{ev.to_version}
                        </code>
                      </div>
                      {ev.publish_run_id ? (
                        <div className="pmrg-mr-cell__meta">
                          <span>发布批次 #{ev.publish_run_id}</span>
                        </div>
                      ) : null}
                    </div>
                  </td>

                  <td>
                    <div className="pmrg-mr-cell">
                      <div>{ev.bundle_name || '-'}</div>
                      <span className="code-badge code-badge-neutral">{getSourceLabel(ev.source)}</span>
                    </div>
                  </td>

                  <td>
                    <div className="pmrg-matrix-pills">
                      <span className="pmrg-dim-pill pmrg-dim-pill--fail">
                        违规 {ev.breaking_count} 项
                      </span>
                      <span
                        className={`pmrg-dim-pill ${
                          ev.affected_count > 0 ? 'pmrg-dim-pill--warn' : 'pmrg-dim-pill--ok'
                        }`}
                      >
                        波及 {ev.affected_count} 模块
                      </span>
                    </div>
                  </td>

                  <td>
                    <div className="pmrg-time-col">
                      <span>{ev.triggered_by || '-'}</span>
                      <span className="pgit-muted">{formatAuditTime(ev.created_at)}</span>
                    </div>
                  </td>

                  <td>
                    <div className="pmrg-status-col">
                      {ev.status === 'OPEN' ? (
                        <span className="code-badge code-badge-danger">待确认 (OPEN)</span>
                      ) : (
                        <span className="code-badge code-badge-success">已确认</span>
                      )}
                      {ev.status === 'ACKNOWLEDGED' && ev.acknowledged_by_name && (
                        <span className="pgit-muted">确认人: {ev.acknowledged_by_name}</span>
                      )}
                    </div>
                  </td>

                  <td>
                    <div className="pmrg-row-actions">
                      <button
                        type="button"
                        className="btn btn-secondary btn-sm"
                        data-testid={`inspect-breaking-audit-${ev.id}`}
                        onClick={() => void handleOpenDetail(ev)}
                      >
                        <Eye size={13} />
                        <span>详情</span>
                      </button>
                      {isAdmin && ev.status === 'OPEN' && (
                        <button
                          type="button"
                          className="btn btn-primary btn-sm"
                          data-testid={`ack-breaking-audit-${ev.id}`}
                          disabled={acknowledging}
                          onClick={() => void handleAcknowledgeOne(ev)}
                        >
                          <CheckCircle2 size={13} />
                          <span>确认</span>
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {total > 0 && (
        <div className="pmrg-pagination">
          <Pagination
            page={page}
            pageSize={pageSize}
            totalItems={total}
            onPageChange={(nextPage) => updateUrlParams({ ba_page: String(nextPage) })}
            onPageSizeChange={(nextSize) =>
              updateUrlParams({ ba_page_size: String(nextSize), ba_page: '1' })
            }
            pageSizeOptions={[15, 25, 50, 100]}
          />
        </div>
      )}

      <Drawer
        open={detailOpen}
        onClose={() => {
          setDetailOpen(false);
          setSelectedDetail(null);
        }}
        title={
          selectedDetail
            ? `破坏性变更审计详情 · ${selectedDetail.schema_name} (v${selectedDetail.from_version} → v${selectedDetail.to_version})`
            : '破坏性变更审计详情'
        }
        width="xl"
      >
        {detailLoading && !selectedDetail ? (
          <EmptyState compact title="正在加载破坏性变更明细..." />
        ) : !selectedDetail ? (
          <EmptyState compact title="未找到审计记录" />
        ) : (
          <div className="pmrg-detail-body" data-testid="breaking-audit-detail-drawer">
            <div className="pmrg-detail-summary">
              <div className="pmrg-detail-summary__badges">
                {selectedDetail.status === 'OPEN' ? (
                  <span className="code-badge code-badge-danger">待确认 (OPEN)</span>
                ) : (
                  <span className="code-badge code-badge-success">已确认 (ACKNOWLEDGED)</span>
                )}
                <span className="code-badge code-badge-neutral">
                  {getSourceLabel(selectedDetail.source)}
                </span>
                {selectedDetail.bundle_name && (
                  <code className="code-tpl-code-inline">{selectedDetail.bundle_name}</code>
                )}
                {selectedDetail.publish_run_id ? (
                  <span className="pgit-muted">发布批次 #{selectedDetail.publish_run_id}</span>
                ) : null}
              </div>
            </div>

            {selectedDetail.status === 'ACKNOWLEDGED' && (
              <div className="pmrg-callout pmrg-callout--info">
                <ShieldCheck size={16} />
                <div>
                  <strong>已完成合规审计确认</strong>
                  <p>
                    确认人：{selectedDetail.acknowledged_by_name || '-'} · 时间：
                    {formatAuditTime(selectedDetail.acknowledged_at)}
                    {selectedDetail.acknowledge_note
                      ? ` · 备注：${selectedDetail.acknowledge_note}`
                      : ''}
                  </p>
                </div>
              </div>
            )}

            <section className="pmrg-detail-section">
              <h4 className="pmrg-detail-section__title">
                检出破坏性规则明细 ({selectedDetail.breaking_rules?.length || 0})
              </h4>
              <ul className="pmrg-violation-list">
                {(selectedDetail.breaking_rules || []).map((brk, idx) => (
                  <li
                    key={`${brk.rule}-${brk.symbol}-${idx}`}
                    className="pmrg-violation-item pmrg-violation-item--danger"
                  >
                    <div className="pmrg-violation-item__head">
                      <span className="code-badge code-badge-danger">{brk.rule}</span>
                      <code className="code-tpl-code-inline">{brk.proto_file}</code>
                      {brk.symbol && <code className="code-tpl-code-inline">{brk.symbol}</code>}
                    </div>
                    <p className="pmrg-violation-item__msg">{brk.message}</p>
                  </li>
                ))}
              </ul>
            </section>

            <section className="pmrg-detail-section">
              <h4 className="pmrg-detail-section__title">
                下游受波及架构模块 ({selectedDetail.affected_dependents?.length || 0})
              </h4>
              {(selectedDetail.affected_dependents?.length || 0) === 0 ? (
                <div className="pmrg-ok-line">
                  <CheckCircle2 size={15} />
                  <span>当前架构依赖图谱中暂未检索到受波及的下游消费模块。</span>
                </div>
              ) : (
                <div className="pmrg-affected-modules">
                  <div className="pmrg-affected-modules__label">
                    <AlertTriangle size={14} />
                    <span>以下下游架构模块依赖了本次变更的契约服务或基础库，请及时跟进适配核验：</span>
                  </div>
                  <div className="pmrg-affected-chips">
                    {selectedDetail.affected_dependents.map((mod) => (
                      <div key={`${mod.module_id}-${mod.module_code}`} className="pmrg-module-chip">
                        <strong>{mod.module_name}</strong>
                        <span className="pgit-muted">({mod.module_code})</span>
                        {mod.arch_process_code && (
                          <span className="pgit-muted">· 进程: {mod.arch_process_code}</span>
                        )}
                        <span>· 负责人: {mod.owner_name || '未指定'}</span>
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </section>

            {isAdmin && selectedDetail.status === 'OPEN' && (
              <section className="pmrg-detail-section">
                <h4 className="pmrg-detail-section__title">标记为已确认</h4>
                <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
                  <textarea
                    className="ppipe-textarea"
                    data-testid="breaking-audit-ack-note"
                    placeholder="可选：填写已通知下游、已评估兼容影响或后续整改单号等备注信息"
                    value={ackNote}
                    onChange={(e) => setAckNote(e.target.value)}
                  />
                  <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
                    <button
                      type="button"
                      className="btn btn-primary"
                      data-testid="confirm-ack-breaking-audit-btn"
                      disabled={acknowledging}
                      onClick={() => void handleAcknowledgeOne(selectedDetail, ackNote)}
                    >
                      <CheckCircle2 size={14} />
                      <span>{acknowledging ? '正在提交...' : '确认已知悉并归档'}</span>
                    </button>
                  </div>
                </div>
              </section>
            )}
          </div>
        )}
      </Drawer>

      <Modal
        open={batchModalOpen}
        onClose={() => setBatchModalOpen(false)}
        title="批量确认破坏性变更审计事件"
        footer={
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '8px' }}>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setBatchModalOpen(false)}
              disabled={acknowledging}
            >
              取消
            </button>
            <button
              type="button"
              className="btn btn-primary"
              onClick={() => void handleBatchAcknowledge()}
              disabled={acknowledging}
            >
              {acknowledging ? '正在确认...' : `确认归档 (${selectedIds.length} 条)`}
            </button>
          </div>
        }
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
          <p style={{ margin: 0, fontSize: '13px', color: 'var(--color-text-secondary)' }}>
            即将把选中的 {selectedIds.length} 条破坏性变更审计事件标记为「已确认 (ACKNOWLEDGED)」。
          </p>
          <textarea
            className="ppipe-textarea"
            placeholder="可选：填写批量确认备注说明"
            value={batchNote}
            onChange={(e) => setBatchNote(e.target.value)}
          />
        </div>
      </Modal>
    </div>
  );
};
