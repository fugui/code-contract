import React, { useEffect, useMemo, useState } from 'react';
import {
  ChevronRight,
  ChevronDown,
  Server,
  Layers,
  Plus,
  Edit2,
  Trash2,
  Search,
  Radio,
  FolderInput,
  GripVertical,
  CheckSquare,
  X,
} from 'lucide-react';
import './ArchProcessBlueprint.css';
import type { ArchProcessGroup } from '../../types';

const STAGING_GROUP_CODE = 'PGRP_CSG_AUTO';
const PGRP_CODE_REGEX = /^[A-Z0-9_-]{2,64}$/;

export interface ArchProcessTreePanelProps {
  panelWidth?: number;
  groups: ArchProcessGroup[];
  selectedProcessId: string;
  onSelectProcess: (processId: string) => void;
  isAdmin: boolean;
  onCreateGroup: () => void;
  onEditGroup: (group: ArchProcessGroup) => void;
  onDeleteGroup: (groupId: string | number) => void;
  onCreateProcess: (groupId?: string | number) => void;
  onBatchMoveProcesses?: (processIds: string[], targetGroupId: string) => Promise<void>;
  onQuickCreateGroup?: (payload: { code: string; name: string; description?: string }) => Promise<ArchProcessGroup>;
  onDropMoveProcess?: (processId: string, targetGroupId: string) => Promise<void>;
  expandGroupSignal?: { groupId: string; seq: number } | null;
}

export const ArchProcessTreePanel: React.FC<ArchProcessTreePanelProps> = ({
  panelWidth,
  groups,
  selectedProcessId,
  onSelectProcess,
  isAdmin,
  onCreateGroup,
  onEditGroup,
  onDeleteGroup,
  onCreateProcess,
  onBatchMoveProcesses,
  onQuickCreateGroup,
  onDropMoveProcess,
  expandGroupSignal,
}) => {
  const [search, setSearch] = useState('');
  const [collapsedGroups, setCollapsedGroups] = useState<Record<string, boolean>>({});

  // 批量归类模式状态
  const [batchMode, setBatchMode] = useState(false);
  const [selectedBatchIds, setSelectedBatchIds] = useState<string[]>([]);
  const [batchTargetGroupId, setBatchTargetGroupId] = useState('');
  const [batchSubmitting, setBatchSubmitting] = useState(false);
  const [batchNewGroupCode, setBatchNewGroupCode] = useState('');
  const [batchNewGroupName, setBatchNewGroupName] = useState('');
  const [batchCreateError, setBatchCreateError] = useState('');

  // 拖拽移组状态
  const [draggingProc, setDraggingProc] = useState<{ id: string; sourceGroupId: string } | null>(null);
  const [dragOverGroupId, setDragOverGroupId] = useState<string | null>(null);

  useEffect(() => {
    if (!expandGroupSignal?.groupId) return;
    setCollapsedGroups((prev) => ({ ...prev, [expandGroupSignal.groupId]: false }));
  }, [expandGroupSignal]);

  const toggleGroup = (groupId: string | number, currentCollapsed: boolean) => {
    const key = String(groupId);
    setCollapsedGroups((prev) => ({ ...prev, [key]: !currentCollapsed }));
  };

  // 搜索过滤与暂存组置顶（有待归类进程时置顶）
  const filteredGroups = useMemo(() => {
    const s = search.trim().toLowerCase();
    const matched = !s
      ? groups
      : groups
          .map((g) => {
            const groupMatch =
              g.name.toLowerCase().includes(s) || g.code.toLowerCase().includes(s);
            const matchingProcesses = (g.processes || []).filter(
              (p) =>
                p.name.toLowerCase().includes(s) ||
                p.code.toLowerCase().includes(s) ||
                String(p.default_listen_port).includes(s)
            );

            if (groupMatch) {
              return g;
            }
            if (matchingProcesses.length > 0) {
              return {
                ...g,
                processes: matchingProcesses,
              };
            }
            return null;
          })
          .filter((g): g is ArchProcessGroup => g !== null);

    return [...matched].sort((a, b) => {
      const aStagingPending = a.code === STAGING_GROUP_CODE && (a.processes?.length ?? 0) > 0;
      const bStagingPending = b.code === STAGING_GROUP_CODE && (b.processes?.length ?? 0) > 0;
      if (aStagingPending && !bStagingPending) return -1;
      if (!aStagingPending && bStagingPending) return 1;
      return 0;
    });
  }, [groups, search]);

  const selectedBatchSet = useMemo(() => new Set(selectedBatchIds), [selectedBatchIds]);

  const visibleProcessIds = useMemo(
    () =>
      filteredGroups.flatMap((g) =>
        (g.processes || []).map((p) => String(p.id))
      ),
    [filteredGroups]
  );

  const toggleBatchItem = (processId: string) => {
    setSelectedBatchIds((prev) =>
      prev.includes(processId) ? prev.filter((id) => id !== processId) : [...prev, processId]
    );
  };

  const toggleGroupBatchSelection = (groupProcessIds: string[]) => {
    if (groupProcessIds.length === 0) return;
    const allSelected = groupProcessIds.every((id) => selectedBatchSet.has(id));
    if (allSelected) {
      const removeSet = new Set(groupProcessIds);
      setSelectedBatchIds((prev) => prev.filter((id) => !removeSet.has(id)));
    } else {
      setSelectedBatchIds((prev) => Array.from(new Set([...prev, ...groupProcessIds])));
    }
  };

  const handleEnterBatchForGroup = (groupId: string | number) => {
    setBatchMode(true);
    setCollapsedGroups((prev) => ({ ...prev, [String(groupId)]: false }));
  };

  const handleExitBatchMode = () => {
    setBatchMode(false);
    setSelectedBatchIds([]);
    setBatchTargetGroupId('');
    setBatchNewGroupCode('');
    setBatchNewGroupName('');
    setBatchCreateError('');
  };

  const handleExecuteBatchMove = async () => {
    if (!onBatchMoveProcesses || selectedBatchIds.length === 0 || batchSubmitting) return;
    setBatchSubmitting(true);
    setBatchCreateError('');
    try {
      let targetId = batchTargetGroupId;
      if (targetId === '__NEW__') {
        if (!onQuickCreateGroup) {
          setBatchSubmitting(false);
          return;
        }
        const code = batchNewGroupCode.trim().toUpperCase();
        const name = batchNewGroupName.trim();
        if (!PGRP_CODE_REGEX.test(code)) {
          setBatchCreateError('编码需为 2-64 位大写字母/数字/下划线/中划线');
          setBatchSubmitting(false);
          return;
        }
        if (!name) {
          setBatchCreateError('请填写新进程组名称');
          setBatchSubmitting(false);
          return;
        }
        const created = await onQuickCreateGroup({ code, name });
        targetId = String(created.id);
      }
      if (!targetId) {
        setBatchSubmitting(false);
        return;
      }
      await onBatchMoveProcesses(selectedBatchIds, targetId);
      setSelectedBatchIds([]);
      if (batchTargetGroupId === '__NEW__') {
        setBatchTargetGroupId(targetId);
        setBatchNewGroupCode('');
        setBatchNewGroupName('');
      }
    } catch (err) {
      setBatchCreateError(err instanceof Error ? err.message : '批量移组失败');
    } finally {
      setBatchSubmitting(false);
    }
  };

  return (
    <div
      className="code-arch-tree-panel"
      data-testid="arch-process-tree-panel"
      style={{
        width: panelWidth ? `${panelWidth}px` : '380px',
        flexShrink: 0,
        display: 'flex',
        flexDirection: 'column',
        background: 'var(--color-bg-surface)',
        borderRight: '1px solid var(--color-border-primary)',
        height: '100%',
      }}
    >
      {/* 顶部工具栏与搜索 */}
      <div
        style={{
          padding: '0.75rem 1rem',
          borderBottom: '1px solid var(--color-border-primary)',
          display: 'flex',
          flexDirection: 'column',
          gap: '0.5rem',
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', fontWeight: 600 }}>
            <Server size={16} style={{ color: 'var(--color-primary)' }} />
            <span>物理进程蓝图</span>
          </div>
          {isAdmin && (
            <div style={{ display: 'flex', gap: '0.25rem' }}>
              {onBatchMoveProcesses && (
                <button
                  type="button"
                  className={`code-btn code-btn--xs ${batchMode ? 'code-btn--primary' : 'code-btn--secondary'}`}
                  onClick={() => (batchMode ? handleExitBatchMode() : setBatchMode(true))}
                  title="批量选中多个进程并迁移到指定进程组"
                  data-testid="toggle-batch-move-btn"
                >
                  <CheckSquare size={12} />
                  <span>{batchMode ? '退出归类' : '批量归类'}</span>
                </button>
              )}
              <button
                type="button"
                className="code-btn code-btn--secondary code-btn--xs"
                onClick={onCreateGroup}
                title="新建物理进程组"
                data-testid="create-process-group-btn"
              >
                <Plus size={12} />
                <span>分组</span>
              </button>
              <button
                type="button"
                className="code-btn code-btn--primary code-btn--xs"
                onClick={() => onCreateProcess()}
                title="新建标准架构进程"
                data-testid="create-arch-process-btn"
              >
                <Plus size={12} />
                <span>进程</span>
              </button>
            </div>
          )}
        </div>

        <div style={{ position: 'relative' }}>
          <Search
            size={13}
            style={{
              position: 'absolute',
              left: '0.6rem',
              top: '50%',
              transform: 'translateY(-50%)',
              color: 'var(--color-text-muted)',
            }}
          />
          <input
            type="text"
            className="code-input"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="过滤进程组、进程名、端口..."
            style={{ paddingLeft: '1.8rem', fontSize: '0.8rem', height: '30px' }}
          />
        </div>
      </div>

      {/* 批量归类快捷栏 */}
      {batchMode && (
        <div className="code-arch-proc-batch-toolbar" data-testid="batch-move-toolbar">
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.35rem' }}>
            <button
              type="button"
              className="code-btn code-btn--secondary code-btn--xs"
              onClick={() => setSelectedBatchIds(visibleProcessIds)}
              disabled={visibleProcessIds.length === 0}
              data-testid="batch-select-filtered-btn"
            >
              全选当前过滤 ({visibleProcessIds.length})
            </button>
            {selectedBatchIds.length > 0 && (
              <button
                type="button"
                className="code-btn code-btn--secondary code-btn--xs"
                onClick={() => setSelectedBatchIds([])}
              >
                清空
              </button>
            )}
          </div>
          <button
            type="button"
            className="code-btn-icon"
            onClick={handleExitBatchMode}
            title="退出批量归类"
          >
            <X size={13} />
          </button>
        </div>
      )}

      {/* 进程树列表 */}
      <div style={{ flex: 1, overflowY: 'auto', padding: '0.5rem' }}>
        {filteredGroups.length === 0 ? (
          <div
            style={{
              padding: '2rem 1rem',
              textAlign: 'center',
              color: 'var(--color-text-muted)',
              fontSize: '0.85rem',
            }}
          >
            {search ? '无匹配的标准架构进程' : '暂无物理进程组，请点击右上角新建'}
          </div>
        ) : (
          filteredGroups.map((g) => {
            const procs = g.processes || [];
            const isStaging = g.code === STAGING_GROUP_CODE;
            const groupKey = String(g.id);
            const isCollapsed = search
              ? false
              : (collapsedGroups[groupKey] ?? (isStaging && procs.length === 0));
            const isDragOver =
              dragOverGroupId === groupKey &&
              draggingProc !== null &&
              draggingProc.sourceGroupId !== groupKey;
            const groupProcIds = procs.map((p) => String(p.id));
            const allGroupSelected =
              groupProcIds.length > 0 && groupProcIds.every((id) => selectedBatchSet.has(id));

            return (
              <div
                key={g.id}
                className={[
                  'code-arch-proc-group-node',
                  isStaging && procs.length > 0 ? 'code-arch-proc-group-node--staging' : '',
                  isDragOver ? 'code-arch-proc-group-node--drag-over' : '',
                ]
                  .filter(Boolean)
                  .join(' ')}
                data-testid={`process-group-node-${g.id}`}
                onDragOver={(e) => {
                  if (!isAdmin || batchMode || !onDropMoveProcess || !draggingProc) return;
                  if (draggingProc.sourceGroupId === groupKey) return;
                  e.preventDefault();
                  if (dragOverGroupId !== groupKey) {
                    setDragOverGroupId(groupKey);
                  }
                }}
                onDragLeave={() => {
                  if (dragOverGroupId === groupKey) {
                    setDragOverGroupId(null);
                  }
                }}
                onDrop={(e) => {
                  if (!isAdmin || batchMode || !onDropMoveProcess || !draggingProc) return;
                  if (draggingProc.sourceGroupId === groupKey) return;
                  e.preventDefault();
                  const procId = draggingProc.id;
                  setDraggingProc(null);
                  setDragOverGroupId(null);
                  void onDropMoveProcess(procId, groupKey);
                }}
              >
                {/* 组头部 */}
                <div
                  className={[
                    'code-arch-proc-group-header',
                    isStaging && procs.length > 0 ? 'code-arch-proc-group-header--staging' : '',
                  ]
                    .filter(Boolean)
                    .join(' ')}
                  onClick={() => toggleGroup(g.id, isCollapsed)}
                >
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.35rem', overflow: 'hidden', minWidth: 0 }}>
                    {batchMode && (
                      <input
                        type="checkbox"
                        checked={allGroupSelected}
                        disabled={groupProcIds.length === 0}
                        onChange={() => toggleGroupBatchSelection(groupProcIds)}
                        onClick={(e) => e.stopPropagation()}
                        title="选中/取消该组全部可见进程"
                      />
                    )}
                    <span style={{ color: 'var(--color-text-muted)' }}>
                      {isCollapsed ? <ChevronRight size={13} /> : <ChevronDown size={13} />}
                    </span>
                    <Layers size={13} style={{ color: 'var(--color-primary)', flexShrink: 0 }} />
                    <span
                      style={{
                        fontWeight: 600,
                        fontSize: '0.825rem',
                        color: 'var(--color-text-primary)',
                        whiteSpace: 'nowrap',
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                      }}
                      title={`${g.name} (${g.code})`}
                    >
                      {g.name}
                    </span>
                    {isStaging ? (
                      <span
                        className={`code-arch-proc-staging-badge ${
                          procs.length === 0 ? 'code-arch-proc-staging-badge--cleared' : ''
                        }`}
                        data-testid="staging-group-badge"
                      >
                        {procs.length > 0 ? `待归类 ${procs.length}` : '已清零'}
                      </span>
                    ) : (
                      <span
                        className="code-badge code-badge--secondary"
                        style={{ fontSize: '0.7rem', padding: '1px 5px' }}
                      >
                        {procs.length}
                      </span>
                    )}
                    {isDragOver && (
                      <span className="code-arch-proc-drop-hint">松开移入此组</span>
                    )}
                  </div>

                  {isAdmin && !batchMode && (
                    <div
                      style={{ display: 'flex', gap: '0.2rem', flexShrink: 0 }}
                      onClick={(e) => e.stopPropagation()}
                    >
                      {isStaging && procs.length > 0 && onBatchMoveProcesses && (
                        <button
                          type="button"
                          className="code-btn code-btn--secondary code-btn--xs"
                          onClick={() => handleEnterBatchForGroup(g.id)}
                          title="批量整理待归类进程"
                          data-testid="staging-group-batch-btn"
                        >
                          <FolderInput size={11} />
                          <span>整理</span>
                        </button>
                      )}
                      <button
                        type="button"
                        className="code-btn-icon"
                        onClick={() => onCreateProcess(g.id)}
                        title="在此组下新建架构进程"
                        style={{ padding: '2px', color: 'var(--color-text-secondary)' }}
                      >
                        <Plus size={12} />
                      </button>
                      <button
                        type="button"
                        className="code-btn-icon"
                        onClick={() => onEditGroup(g)}
                        title="编辑进程组"
                        style={{ padding: '2px', color: 'var(--color-text-secondary)' }}
                      >
                        <Edit2 size={12} />
                      </button>
                      <button
                        type="button"
                        className="code-btn-icon"
                        onClick={() => onDeleteGroup(g.id)}
                        title="删除进程组"
                        style={{ padding: '2px', color: 'var(--color-danger)' }}
                      >
                        <Trash2 size={12} />
                      </button>
                    </div>
                  )}
                </div>

                {/* 进程子列表 */}
                {!isCollapsed && (
                  <div style={{ padding: '0.25rem 0.35rem' }}>
                    {procs.length === 0 ? (
                      <div
                        style={{
                          padding: '0.5rem',
                          color: 'var(--color-text-muted)',
                          fontSize: '0.75rem',
                          textAlign: 'center',
                        }}
                      >
                        {isStaging ? '待归类池已清零' : '组内暂无进程'}
                      </div>
                    ) : (
                      procs.map((p) => {
                        const procIdStr = String(p.id);
                        const isSelected = procIdStr === String(selectedProcessId);
                        const isBatchChecked = selectedBatchSet.has(procIdStr);
                        const isClient = p.default_listen_port === 0;
                        const canDrag = Boolean(isAdmin && !batchMode && onDropMoveProcess);
                        const isDragging = draggingProc?.id === procIdStr;

                        return (
                          <div
                            key={p.id}
                            draggable={canDrag}
                            onDragStart={(e) => {
                              if (!canDrag) return;
                              e.dataTransfer.effectAllowed = 'move';
                              e.dataTransfer.setData('text/plain', procIdStr);
                              setDraggingProc({ id: procIdStr, sourceGroupId: groupKey });
                            }}
                            onDragEnd={() => {
                              setDraggingProc(null);
                              setDragOverGroupId(null);
                            }}
                            onClick={() => {
                              if (batchMode) {
                                toggleBatchItem(procIdStr);
                              } else {
                                onSelectProcess(procIdStr);
                              }
                            }}
                            data-testid={`arch-process-item-${p.id}`}
                            className={isDragging ? 'code-arch-proc-item--dragging' : undefined}
                            style={{
                              display: 'flex',
                              alignItems: 'center',
                              justifyContent: 'space-between',
                              padding: '0.4rem 0.5rem',
                              borderRadius: 'var(--radius-sm)',
                              marginBottom: '2px',
                              cursor: 'pointer',
                              background:
                                (batchMode && isBatchChecked) || (!batchMode && isSelected)
                                  ? 'var(--color-bg-hover)'
                                  : 'transparent',
                              border:
                                (batchMode && isBatchChecked) || (!batchMode && isSelected)
                                  ? '1px solid var(--color-primary)'
                                  : '1px solid transparent',
                              transition: 'all 0.15s ease',
                            }}
                          >
                            <div
                              style={{
                                display: 'flex',
                                alignItems: 'center',
                                gap: '0.35rem',
                                minWidth: 0,
                                flex: 1,
                              }}
                            >
                              {batchMode ? (
                                <input
                                  type="checkbox"
                                  checked={isBatchChecked}
                                  onChange={() => toggleBatchItem(procIdStr)}
                                  onClick={(e) => e.stopPropagation()}
                                  data-testid={`batch-checkbox-${p.id}`}
                                />
                              ) : canDrag ? (
                                <span
                                  className="code-arch-proc-drag-handle"
                                  title="拖拽至目标物理进程组"
                                >
                                  <GripVertical size={12} />
                                </span>
                              ) : null}
                              <Radio
                                size={12}
                                style={{
                                  color: isSelected
                                    ? 'var(--color-primary)'
                                    : 'var(--color-text-muted)',
                                  flexShrink: 0,
                                }}
                              />
                              <div style={{ display: 'flex', flexDirection: 'column', minWidth: 0 }}>
                                <span
                                  style={{
                                    fontSize: '0.8rem',
                                    fontWeight: isSelected ? 600 : 500,
                                    color: isSelected
                                      ? 'var(--color-primary)'
                                      : 'var(--color-text-primary)',
                                    whiteSpace: 'nowrap',
                                    overflow: 'hidden',
                                    textOverflow: 'ellipsis',
                                  }}
                                  title={p.name}
                                >
                                  {p.name}
                                </span>
                                <span
                                  className="code-font-mono"
                                  style={{
                                    fontSize: '0.675rem',
                                    color: 'var(--color-text-muted)',
                                    whiteSpace: 'nowrap',
                                    overflow: 'hidden',
                                    textOverflow: 'ellipsis',
                                  }}
                                  title={p.code}
                                >
                                  {p.code}
                                </span>
                              </div>
                            </div>

                            <div style={{ display: 'flex', alignItems: 'center', gap: '0.3rem', flexShrink: 0 }}>
                              <span
                                className="code-font-mono"
                                style={{
                                  fontSize: '0.7rem',
                                  padding: '1px 5px',
                                  borderRadius: 'var(--radius-sm)',
                                  background: isClient
                                    ? 'var(--color-bg-muted)'
                                    : 'var(--color-bg-surface)',
                                  border: '1px solid var(--color-border-primary)',
                                  color: isClient
                                    ? 'var(--color-text-muted)'
                                    : 'var(--color-primary)',
                                }}
                              >
                                {isClient ? '纯客户端' : `:${p.default_listen_port}`}
                              </span>
                              {p.mount_count !== undefined && p.mount_count > 0 && (
                                <span
                                  style={{
                                    fontSize: '0.675rem',
                                    padding: '1px 4px',
                                    borderRadius: 'var(--radius-sm)',
                                    background: 'rgba(34, 197, 94, 0.12)',
                                    color: 'var(--color-success)',
                                    fontWeight: 500,
                                  }}
                                  title={`在 ${p.mount_count} 个环境中已挂载`}
                                >
                                  {p.mount_count} 挂载
                                </span>
                              )}
                            </div>
                          </div>
                        );
                      })
                    )}
                  </div>
                )}
              </div>
            );
          })
        )}
      </div>

      {/* 批量归类底部操作条 */}
      {batchMode && onBatchMoveProcesses && (
        <div className="code-arch-proc-batch-footer" data-testid="batch-move-action-bar">
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', fontSize: '0.78rem' }}>
            <span>
              已选 <strong>{selectedBatchIds.length}</strong> 个进程
            </span>
            <span style={{ color: 'var(--color-text-muted)', fontSize: '0.72rem' }}>
              选择目标物理进程组
            </span>
          </div>
          <div className="code-arch-proc-batch-footer__row">
            <select
              className="code-select"
              value={batchTargetGroupId}
              onChange={(e) => {
                setBatchTargetGroupId(e.target.value);
                setBatchCreateError('');
              }}
              style={{ flex: 1, height: '30px', fontSize: '0.78rem' }}
              data-testid="batch-move-target-select"
            >
              <option value="">-- 请选择目标进程组 --</option>
              {groups.map((g) => (
                <option key={g.id} value={String(g.id)}>
                  {g.name} ({g.code})
                </option>
              ))}
              {onQuickCreateGroup && (
                <option value="__NEW__">+ 新建进程组并移入...</option>
              )}
            </select>
            {batchTargetGroupId !== '__NEW__' && (
              <button
                type="button"
                className="code-btn code-btn--primary code-btn--xs"
                disabled={selectedBatchIds.length === 0 || !batchTargetGroupId || batchSubmitting}
                onClick={() => void handleExecuteBatchMove()}
                data-testid="batch-move-confirm-btn"
              >
                {batchSubmitting ? '移动中...' : '确认移入'}
              </button>
            )}
          </div>
          {batchTargetGroupId === '__NEW__' && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: '0.35rem' }}>
              <div className="code-arch-proc-batch-footer__row">
                <input
                  type="text"
                  className="code-input"
                  value={batchNewGroupName}
                  onChange={(e) => setBatchNewGroupName(e.target.value)}
                  placeholder="新进程组名称"
                  style={{ flex: 1, height: '28px', fontSize: '0.76rem' }}
                />
                <input
                  type="text"
                  className="code-input code-font-mono"
                  value={batchNewGroupCode}
                  onChange={(e) => setBatchNewGroupCode(e.target.value.toUpperCase())}
                  placeholder="编码 (如 CORE_PAY)"
                  style={{ flex: 1, height: '28px', fontSize: '0.76rem' }}
                />
              </div>
              {batchCreateError && (
                <div style={{ fontSize: '0.72rem', color: 'var(--color-danger)' }}>
                  {batchCreateError}
                </div>
              )}
              <button
                type="button"
                className="code-btn code-btn--primary code-btn--xs"
                disabled={selectedBatchIds.length === 0 || batchSubmitting}
                onClick={() => void handleExecuteBatchMove()}
                data-testid="batch-move-create-confirm-btn"
              >
                {batchSubmitting ? '创建并移入中...' : '创建进程组并移入所选'}
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  );
};
export default ArchProcessTreePanel;
