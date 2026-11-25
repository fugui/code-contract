import React, { useState } from 'react';
import { Drawer, Pagination, useConfirm } from '@code/common';
import { GitCompare, Pin, PinOff, Trash2 } from 'lucide-react';
import type {
  Agent,
  DiscoveryDiffKind,
  DiscoverySnapshotDiffDTO,
  DiscoverySnapshotSummaryDTO,
} from '../../types';

export interface DiscoverySnapshotDiffDrawerProps {
  open: boolean;
  onClose: () => void;
  agent: Agent | null;
  allAgents: Agent[];
  snapshots: DiscoverySnapshotSummaryDTO[];
  snapshotsTotal: number;
  page: number;
  pageSize: number;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
  loadingSnapshots: boolean;
  diffResult: DiscoverySnapshotDiffDTO | null;
  loadingDiff: boolean;
  diffLeftId: string;
  diffRightId: string;
  diffLiveAgentId: string;
  onChangeDiffTargets: (next: {
    leftId?: string;
    rightId?: string;
    liveAgentId?: string;
  }) => void;
  onRunDiff: () => void;
  onUpdateSnapshot: (id: number, data: { label?: string; is_pinned?: boolean }) => Promise<void>;
  onDeleteSnapshot: (id: number) => Promise<void>;
}

const DIFF_KIND_META: Record<DiscoveryDiffKind, { label: string; cls: string }> = {
  ADDED_PROCESS: { label: '新增进程', cls: 'discovery-pill--success' },
  REMOVED_PROCESS: { label: '移除进程', cls: 'discovery-pill--danger' },
  ADDED_SERVICE: { label: '新增服务', cls: 'discovery-pill--success' },
  REMOVED_SERVICE: { label: '下线服务', cls: 'discovery-pill--danger' },
  TRANSPORT_CHANGED: { label: '制式变更', cls: 'discovery-pill--warning' },
  ADDED_SUBSCRIPTION: { label: '新增订阅', cls: 'discovery-pill--info' },
  REMOVED_SUBSCRIPTION: { label: '移除订阅', cls: 'discovery-pill--warning' },
  PROVIDER_KIND_CHANGED: { label: '提供方变更', cls: 'discovery-pill--primary' },
};

export const DiscoverySnapshotDiffDrawer: React.FC<DiscoverySnapshotDiffDrawerProps> = ({
  open,
  onClose,
  agent,
  allAgents,
  snapshots,
  snapshotsTotal,
  page,
  pageSize,
  onPageChange,
  onPageSizeChange,
  loadingSnapshots,
  diffResult,
  loadingDiff,
  diffLeftId,
  diffRightId,
  diffLiveAgentId,
  onChangeDiffTargets,
  onRunDiff,
  onUpdateSnapshot,
  onDeleteSnapshot,
}) => {
  const confirmDialog = useConfirm();
  const [activeTab, setActiveTab] = useState<'timeline' | 'diff'>(
    diffResult || diffLeftId ? 'diff' : 'timeline'
  );
  const [editingId, setEditingId] = useState<number | null>(null);
  const [editingLabel, setEditingLabel] = useState('');

  const handleDelete = async (snap: DiscoverySnapshotSummaryDTO) => {
    const ok = await confirmDialog({
      title: '删除现网发现快照',
      content: `确定删除快照 #${snap.id}${snap.label ? `（${snap.label}）` : ''}？此操作不可撤销。`,
      confirmText: '确认删除',
      type: 'danger',
    });
    if (ok) {
      await onDeleteSnapshot(snap.id);
    }
  };

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title="现网发现快照时间线与双模 Diff"
      subtitle={agent ? `当前环境: ${agent.name}` : undefined}
      width="lg"
      testId="discovery-snapshot-diff-drawer"
    >
      <div className="discovery-drawer-body">
        <div className="discovery-tabs" role="tablist">
          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'timeline'}
            data-testid="discovery-drawer-tab-timeline"
            className={`discovery-tab ${
              activeTab === 'timeline' ? 'discovery-tab--active' : ''
            }`}
            onClick={() => setActiveTab('timeline')}
          >
            快照时间线 ({snapshotsTotal})
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'diff'}
            data-testid="discovery-drawer-tab-diff"
            className={`discovery-tab ${activeTab === 'diff' ? 'discovery-tab--active' : ''}`}
            onClick={() => setActiveTab('diff')}
          >
            <GitCompare size={13} />
            <span>双模 Diff 对比</span>
          </button>
        </div>

        {activeTab === 'timeline' ? (
          <>
            {loadingSnapshots ? (
              <div className="discovery-empty">正在加载快照时间线...</div>
            ) : snapshots.length === 0 ? (
              <div className="discovery-empty">当前环境暂无持久化快照</div>
            ) : (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                {snapshots.map((snap) => (
                  <div
                    key={snap.id}
                    data-testid={`discovery-snapshot-card-${snap.id}`}
                    className={`discovery-snapshot-card ${
                      snap.is_pinned ? 'discovery-snapshot-card--pinned' : ''
                    }`}
                  >
                    <div className="discovery-snapshot-header">
                      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                        <strong className="discovery-mono">#{snap.id}</strong>
                        <span className="discovery-pill discovery-pill--neutral">
                          {snap.trigger_mode}
                        </span>
                        {snap.is_pinned && (
                          <span className="discovery-pill discovery-pill--primary">置顶基线</span>
                        )}
                        {editingId === snap.id ? (
                          <div style={{ display: 'inline-flex', gap: 6 }}>
                            <input
                              type="text"
                              className="code-input"
                              style={{ height: 28, fontSize: 12 }}
                              value={editingLabel}
                              onChange={(e) => setEditingLabel(e.target.value)}
                            />
                            <button
                              type="button"
                              className="code-btn code-btn--primary code-btn--xs"
                              onClick={() => {
                                void onUpdateSnapshot(snap.id, { label: editingLabel }).then(
                                  () => setEditingId(null)
                                );
                              }}
                            >
                              保存
                            </button>
                          </div>
                        ) : (
                          <span>{snap.label || '未命名快照'}</span>
                        )}
                      </div>

                      <div className="discovery-snapshot-actions">
                        <button
                          type="button"
                          className="code-btn code-btn--secondary code-btn--xs"
                          onClick={() => {
                            onChangeDiffTargets({
                              leftId: String(snap.id),
                              liveAgentId: agent ? String(agent.id) : '',
                              rightId: '',
                            });
                            setActiveTab('diff');
                          }}
                        >
                          对比实时态
                        </button>
                        <button
                          type="button"
                          className="code-btn code-btn--secondary code-btn--xs"
                          onClick={() => {
                            setEditingId(snap.id);
                            setEditingLabel(snap.label);
                          }}
                        >
                          标签
                        </button>
                        <button
                          type="button"
                          className="code-btn code-btn--secondary code-btn--xs"
                          onClick={() =>
                            void onUpdateSnapshot(snap.id, { is_pinned: !snap.is_pinned })
                          }
                        >
                          {snap.is_pinned ? <PinOff size={12} /> : <Pin size={12} />}
                          <span>{snap.is_pinned ? '取消置顶' : '置顶'}</span>
                        </button>
                        <button
                          type="button"
                          className="code-btn code-btn--danger code-btn--xs"
                          disabled={snap.is_pinned}
                          title={snap.is_pinned ? '置顶快照需先取消置顶才可删除' : '删除快照'}
                          onClick={() => void handleDelete(snap)}
                        >
                          <Trash2 size={12} />
                        </button>
                      </div>
                    </div>

                    <div
                      style={{
                        display: 'flex',
                        flexWrap: 'wrap',
                        gap: 12,
                        fontSize: 12,
                        color: 'var(--color-text-secondary)',
                      }}
                    >
                      <span>进程 {snap.summary?.process_count ?? 0}</span>
                      <span>服务 {snap.summary?.total_services ?? 0}</span>
                      <span>订阅 {snap.summary?.total_subscriptions ?? 0}</span>
                      <span className="discovery-mono">
                        hash: {snap.content_hash.slice(0, 12)}
                      </span>
                      <span>采集: {snap.captured_at}</span>
                    </div>
                  </div>
                ))}

                <Pagination
                  totalItems={snapshotsTotal}
                  page={page}
                  pageSize={pageSize}
                  onPageChange={onPageChange}
                  onPageSizeChange={onPageSizeChange}
                />
              </div>
            )}
          </>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <div className="discovery-diff-controls">
              <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                <label style={{ fontSize: 12, fontWeight: 600 }}>左侧基线快照:</label>
                <select
                  aria-label="左侧基线快照"
                  className="discovery-select"
                  value={diffLeftId}
                  onChange={(e) => onChangeDiffTargets({ leftId: e.target.value })}
                >
                  <option value="">请选择快照...</option>
                  {snapshots.map((s) => (
                    <option key={s.id} value={String(s.id)}>
                      #{s.id} · {s.label || s.trigger_mode} ({s.captured_at})
                    </option>
                  ))}
                </select>
              </div>

              <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                <label style={{ fontSize: 12, fontWeight: 600 }}>右侧对比快照 (时间轴):</label>
                <select
                  aria-label="右侧对比快照"
                  className="discovery-select"
                  value={diffRightId}
                  onChange={(e) =>
                    onChangeDiffTargets({ rightId: e.target.value, liveAgentId: '' })
                  }
                >
                  <option value="">不使用快照（改用右侧实时环境）</option>
                  {snapshots.map((s) => (
                    <option key={s.id} value={String(s.id)}>
                      #{s.id} · {s.label || s.trigger_mode} ({s.captured_at})
                    </option>
                  ))}
                </select>
              </div>

              <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                <label style={{ fontSize: 12, fontWeight: 600 }}>或右侧实时环境 (跨环境):</label>
                <select
                  aria-label="右侧实时环境"
                  className="discovery-select"
                  value={diffLiveAgentId}
                  onChange={(e) =>
                    onChangeDiffTargets({ liveAgentId: e.target.value, rightId: '' })
                  }
                >
                  <option value="">请选择实时 Agent...</option>
                  {allAgents.map((ag) => (
                    <option key={ag.id} value={String(ag.id)}>
                      {ag.name} ({ag.scope})
                    </option>
                  ))}
                </select>
              </div>

              <div style={{ display: 'flex', alignItems: 'flex-end' }}>
                <button
                  type="button"
                  className="code-btn code-btn--primary code-btn--sm"
                  data-testid="discovery-run-diff-btn"
                  disabled={loadingDiff || !diffLeftId || (!diffRightId && !diffLiveAgentId)}
                  onClick={onRunDiff}
                >
                  {loadingDiff ? '对比计算中...' : '执行双模 Diff'}
                </button>
              </div>
            </div>

            {diffResult && (
              <div data-testid="discovery-diff-results" style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                <div className="discovery-banner discovery-banner--info">
                  <span>
                    对比模式: <strong>{diffResult.mode}</strong> · {diffResult.left_label} vs{' '}
                    {diffResult.right_label}
                  </span>
                  <span>共 {diffResult.summary.total} 项结构变更</span>
                </div>

                {diffResult.items.length === 0 ? (
                  <div className="discovery-empty">两侧现网拓扑结构完全一致（已忽略 PID 与动态端口）</div>
                ) : (
                  diffResult.items.map((item, idx) => {
                    const meta = DIFF_KIND_META[item.kind] ?? {
                      label: item.kind,
                      cls: 'discovery-pill--neutral',
                    };
                    return (
                      <div key={`${item.kind}-${item.process_key}-${idx}`} className="discovery-diff-item">
                        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                          <span className={`discovery-pill ${meta.cls}`}>{meta.label}</span>
                          <strong>{item.process_name}</strong>
                          {item.service_name && (
                            <span className="discovery-mono">{item.service_name}</span>
                          )}
                        </div>
                        <div style={{ color: 'var(--color-text-secondary)' }}>{item.detail}</div>
                      </div>
                    );
                  })
                )}
              </div>
            )}
          </div>
        )}
      </div>
    </Drawer>
  );
};
