import React, { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useModulePath } from '../../hooks/useModulePath';
import {
  Server,
  Layers,
  Cpu,
  Globe,
  Radio,
  ExternalLink,
  Edit2,
  Trash2,
  AlertTriangle,
  CheckCircle2,
  Clock,
  Activity,
  Plus,
  Unlink,
  ChevronDown,
  Check,
  Search,
  RotateCcw,
  X,
} from 'lucide-react';
import { Drawer } from '@code/common';
import './ArchProcessBlueprint.css';
import type {
  ArchProcess,
  ArchProcessGroup,
  Module,
  ModuleService,
  ModuleDependency,
  Process,
  AutoPinnedOverride,
} from '../../types';
import { isMeshTransportProcess } from '../../types';

const PGRP_CODE_REGEX = /^[A-Z0-9_-]{2,64}$/;

export interface ArchProcessDetailPaneProps {
  archProcess: ArchProcess;
  processGroups?: ArchProcessGroup[];
  modules: Module[];
  providedServices: ModuleService[];
  dependencies: ModuleDependency[];
  agentMounts: Process[];
  autoPinnedOverrides?: AutoPinnedOverride[];
  allModules?: Module[];
  isAdmin: boolean;
  undoHint?: {
    message: string;
    onUndo: () => void | Promise<void>;
    onDismiss: () => void;
  } | null;
  onEditProcess: () => void;
  onDeleteProcess: () => void;
  onQuickChangeGroup?: (targetGroupId: string) => Promise<void>;
  onQuickCreateGroupAndMove?: (payload: { code: string; name: string; description?: string }) => Promise<void>;
  onUnbindModule?: (moduleId: string) => Promise<void>;
  onBindModule?: (moduleId: string) => Promise<void>;
}

export const ArchProcessDetailPane: React.FC<ArchProcessDetailPaneProps> = ({
  archProcess,
  processGroups = [],
  modules,
  providedServices,
  dependencies,
  agentMounts,
  autoPinnedOverrides = [],
  allModules = [],
  isAdmin,
  undoHint = null,
  onEditProcess,
  onDeleteProcess,
  onQuickChangeGroup,
  onQuickCreateGroupAndMove,
  onUnbindModule,
  onBindModule,
}) => {
  const navigate = useNavigate();
  const modulePath = useModulePath();
  const [activeTab, setActiveTab] = useState<'modules' | 'contracts' | 'mounts'>('modules');
  const [bindDrawerOpen, setBindDrawerOpen] = useState(false);
  const [selectedModuleIdToBind, setSelectedModuleIdToBind] = useState('');
  const [bindingSubmitting, setBindingSubmitting] = useState(false);

  // 顶部所属组快捷切换下拉状态
  const [groupMenuOpen, setGroupMenuOpen] = useState(false);
  const [groupSearch, setGroupSearch] = useState('');
  const [groupMoving, setGroupMoving] = useState(false);
  const [creatingGroup, setCreatingGroup] = useState(false);
  const [newGroupName, setNewGroupName] = useState('');
  const [newGroupCode, setNewGroupCode] = useState('');
  const [newGroupDesc, setNewGroupDesc] = useState('');
  const [createGroupError, setCreateGroupError] = useState('');
  const switcherRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!groupMenuOpen) return;
    const handleOutsideClick = (event: MouseEvent) => {
      if (switcherRef.current && !switcherRef.current.contains(event.target as Node)) {
        setGroupMenuOpen(false);
        setCreatingGroup(false);
        setCreateGroupError('');
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setGroupMenuOpen(false);
        setCreatingGroup(false);
        setCreateGroupError('');
      }
    };
    document.addEventListener('mousedown', handleOutsideClick);
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('mousedown', handleOutsideClick);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [groupMenuOpen]);

  const filteredGroups = useMemo(() => {
    const q = groupSearch.trim().toLowerCase();
    if (!q) return processGroups;
    return processGroups.filter(
      (g) => g.name.toLowerCase().includes(q) || g.code.toLowerCase().includes(q)
    );
  }, [processGroups, groupSearch]);

  const handleToggleGroupMenu = () => {
    if (groupMoving) return;
    setGroupMenuOpen((prev) => {
      const next = !prev;
      if (next) {
        setGroupSearch('');
        setCreatingGroup(false);
        setCreateGroupError('');
      }
      return next;
    });
  };

  const handleSelectQuickGroup = async (targetGroupId: string) => {
    if (!onQuickChangeGroup || groupMoving) return;
    if (String(archProcess.group_id) === targetGroupId) {
      setGroupMenuOpen(false);
      return;
    }
    setGroupMoving(true);
    try {
      await onQuickChangeGroup(targetGroupId);
      setGroupMenuOpen(false);
    } catch {
      // 错误提示由父组件统一处理
    } finally {
      setGroupMoving(false);
    }
  };

  const handleSubmitQuickCreateGroup = async () => {
    if (!onQuickCreateGroupAndMove || groupMoving) return;
    const code = newGroupCode.trim().toUpperCase();
    const name = newGroupName.trim();
    if (!PGRP_CODE_REGEX.test(code)) {
      setCreateGroupError('编码需为 2-64 位大写字母、数字、下划线或中划线');
      return;
    }
    if (!name) {
      setCreateGroupError('请填写物理进程组名称');
      return;
    }
    setCreateGroupError('');
    setGroupMoving(true);
    try {
      await onQuickCreateGroupAndMove({
        code,
        name,
        description: newGroupDesc.trim() || undefined,
      });
      setNewGroupCode('');
      setNewGroupName('');
      setNewGroupDesc('');
      setCreatingGroup(false);
      setGroupMenuOpen(false);
    } catch (err) {
      setCreateGroupError(err instanceof Error ? err.message : '新建物理进程组失败');
    } finally {
      setGroupMoving(false);
    }
  };

  const isClient = archProcess.default_listen_port === 0;

  // 待关联模块候选列表（排除当前已关联模块及已被其他架构进程占用的模块，遵循 A-03）
  const candidateModules = allModules.filter(
    (m) => !modules.some((cur) => String(cur.id) === String(m.id)) && !m.arch_process_id
  );

  const handleExecuteBind = async () => {
    if (!selectedModuleIdToBind || !onBindModule) return;
    setBindingSubmitting(true);
    try {
      await onBindModule(selectedModuleIdToBind);
      setBindDrawerOpen(false);
      setSelectedModuleIdToBind('');
    } catch {
      // 错误提示已由父组件统一展示，保持抽屉开启以便用户调整重试
    } finally {
      setBindingSubmitting(false);
    }
  };

  const renderProbeStatusBadge = (status?: string, reason?: string) => {
    switch (status) {
      case 'READY':
        return (
          <span
            className="code-badge"
            style={{
              background: 'rgba(34, 197, 94, 0.15)',
              color: 'var(--color-success)',
              border: '1px solid var(--color-success)',
              display: 'inline-flex',
              alignItems: 'center',
              gap: '4px',
            }}
          >
            <CheckCircle2 size={11} /> 探针已就绪 (READY)
          </span>
        );
      case 'NEED_RECONNECT':
        return (
          <span
            className="code-badge"
            style={{
              background: 'rgba(234, 179, 8, 0.15)',
              color: 'var(--color-warning)',
              border: '1px solid var(--color-warning)',
              display: 'inline-flex',
              alignItems: 'center',
              gap: '4px',
            }}
          >
            <Clock size={11} /> 需重连释放老连接 (NEED_RECONNECT)
          </span>
        );
      case 'WAITING_PROC':
        return (
          <span
            className="code-badge"
            style={{
              background: 'rgba(59, 130, 246, 0.15)',
              color: 'var(--color-primary)',
              border: '1px solid var(--color-primary)',
              display: 'inline-flex',
              alignItems: 'center',
              gap: '4px',
            }}
          >
            <Radio size={11} /> 等待宿主目标进程 (WAITING_PROC)
          </span>
        );
      case 'MONITORING':
        return (
          <span
            className="code-badge"
            style={{
              background: 'rgba(168, 85, 247, 0.15)',
              color: '#a855f7',
              border: '1px solid #a855f7',
              display: 'inline-flex',
              alignItems: 'center',
              gap: '4px',
            }}
          >
            <Activity size={11} /> 录制中 (MONITORING)
          </span>
        );
      case 'DISARMED':
        return (
          <span
            className="code-badge"
            style={{
              background: 'var(--color-bg-muted)',
              color: 'var(--color-text-muted)',
              border: '1px solid var(--color-border-primary)',
            }}
          >
            探针未激活 (DISARMED)
          </span>
        );
      case 'OFFLINE':
        return (
          <span
            className="code-badge"
            style={{
              background: 'var(--color-bg-muted)',
              color: 'var(--color-text-muted)',
              border: '1px solid var(--color-border-secondary)',
            }}
          >
            Agent 离线 (OFFLINE)
          </span>
        );
      case 'CAP_DENIED':
        return (
          <span
            className="code-badge"
            style={{
              background: 'rgba(239, 68, 68, 0.15)',
              color: 'var(--color-danger)',
              border: '1px solid var(--color-danger)',
              display: 'inline-flex',
              alignItems: 'center',
              gap: '4px',
            }}
            title={reason || '权限不足：缺少 CAP_BPF / root 特权 (CAP_DENIED)'}
          >
            <AlertTriangle size={11} /> 权限不足 (CAP_DENIED)
          </span>
        );
      case 'ERROR':
      default:
        return (
          <span
            className="code-badge"
            style={{
              background: 'rgba(239, 68, 68, 0.15)',
              color: 'var(--color-danger)',
              border: '1px solid var(--color-danger)',
              display: 'inline-flex',
              alignItems: 'center',
              gap: '4px',
            }}
            title={reason || (status ? `${status} 异常` : '探针异常 (ERROR)')}
          >
            <AlertTriangle size={11} /> 异常 ({status || 'ERROR'})
          </span>
        );
    }
  };

  return (
    <div
      className="code-module-pane"
      data-testid="arch-process-detail-pane"
      style={{
        flex: 1,
        display: 'flex',
        flexDirection: 'column',
        overflow: 'hidden',
        height: '100%',
        background: 'var(--color-bg-surface)',
      }}
    >
      {/* 快捷移组撤回提示条 */}
      {undoHint && (
        <div className="code-arch-proc-undo-bar" data-testid="quick-move-undo-bar">
          <span>{undoHint.message}</span>
          <div className="code-arch-proc-undo-bar__actions">
            <button
              type="button"
              className="code-btn code-btn--secondary code-btn--xs"
              onClick={() => void undoHint.onUndo()}
              data-testid="quick-move-undo-btn"
            >
              <RotateCcw size={12} />
              <span>撤销</span>
            </button>
            <button
              type="button"
              className="code-btn-icon"
              onClick={undoHint.onDismiss}
              title="关闭提示"
            >
              <X size={13} />
            </button>
          </div>
        </div>
      )}

      {/* 头部摘要信息区 */}
      <div
        style={{
          padding: '1.25rem 1.5rem',
          borderBottom: '1px solid var(--color-border-primary)',
          background: 'var(--color-bg-base)',
        }}
      >
        <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between' }}>
          <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.6rem', marginBottom: '0.35rem', flexWrap: 'wrap' }}>
              <Server size={20} style={{ color: 'var(--color-primary)' }} />
              <h3
                style={{
                  margin: 0,
                  fontSize: '1.25rem',
                  fontWeight: 700,
                  color: 'var(--color-text-primary)',
                }}
              >
                {archProcess.name}
              </h3>
              <span
                className="code-font-mono"
                style={{
                  fontSize: '0.8rem',
                  padding: '2px 8px',
                  borderRadius: 'var(--radius-sm)',
                  background: 'var(--color-bg-surface)',
                  border: '1px solid var(--color-border-primary)',
                  color: 'var(--color-primary)',
                }}
              >
                {archProcess.code}
              </span>
              {isAdmin && onQuickChangeGroup ? (
                <div className="code-arch-proc-group-switcher" ref={switcherRef}>
                  <button
                    type="button"
                    className="code-arch-proc-group-trigger"
                    onClick={handleToggleGroupMenu}
                    disabled={groupMoving}
                    data-testid="quick-group-switcher-trigger"
                    title="点击快捷切换所属物理进程组"
                  >
                    <span>所属组:</span>
                    <span className="code-arch-proc-group-trigger__name">
                      {archProcess.group?.name || '默认进程组'}
                    </span>
                    <ChevronDown size={12} />
                  </button>

                  {groupMenuOpen && (
                    <div
                      className="code-arch-proc-group-menu"
                      data-testid="quick-group-switcher-menu"
                    >
                      {creatingGroup ? (
                        <div className="code-arch-proc-group-menu__create-form">
                          <div className="code-arch-proc-group-menu__create-title">
                            新建进程组并立即移入
                          </div>
                          <input
                            type="text"
                            className="code-input"
                            value={newGroupName}
                            onChange={(e) => setNewGroupName(e.target.value)}
                            placeholder="进程组名称，如 支付核心组"
                            style={{ height: '30px', fontSize: '0.8rem' }}
                          />
                          <input
                            type="text"
                            className="code-input code-font-mono"
                            value={newGroupCode}
                            onChange={(e) => setNewGroupCode(e.target.value.toUpperCase())}
                            placeholder="编码，如 PAYMENT_CORE"
                            style={{ height: '30px', fontSize: '0.8rem' }}
                          />
                          <input
                            type="text"
                            className="code-input"
                            value={newGroupDesc}
                            onChange={(e) => setNewGroupDesc(e.target.value)}
                            placeholder="描述说明（可选）"
                            style={{ height: '30px', fontSize: '0.8rem' }}
                          />
                          {createGroupError && (
                            <div style={{ fontSize: '0.75rem', color: 'var(--color-danger)' }}>
                              {createGroupError}
                            </div>
                          )}
                          <div className="code-arch-proc-group-menu__create-actions">
                            <button
                              type="button"
                              className="code-btn code-btn--secondary code-btn--xs"
                              onClick={() => {
                                setCreatingGroup(false);
                                setCreateGroupError('');
                              }}
                              disabled={groupMoving}
                            >
                              取消
                            </button>
                            <button
                              type="button"
                              className="code-btn code-btn--primary code-btn--xs"
                              onClick={() => void handleSubmitQuickCreateGroup()}
                              disabled={groupMoving}
                              data-testid="quick-create-group-submit-btn"
                            >
                              {groupMoving ? '创建中...' : '创建并移入'}
                            </button>
                          </div>
                        </div>
                      ) : (
                        <>
                          <div className="code-arch-proc-group-menu__search">
                            <Search
                              size={12}
                              className="code-arch-proc-group-menu__search-icon"
                            />
                            <input
                              type="text"
                              className="code-input"
                              value={groupSearch}
                              onChange={(e) => setGroupSearch(e.target.value)}
                              placeholder="搜索进程组名称或编码..."
                              style={{ paddingLeft: '1.7rem', height: '28px', fontSize: '0.78rem' }}
                            />
                          </div>

                          <div className="code-arch-proc-group-menu__list">
                            {filteredGroups.length === 0 ? (
                              <div
                                style={{
                                  padding: '0.85rem',
                                  textAlign: 'center',
                                  fontSize: '0.75rem',
                                  color: 'var(--color-text-muted)',
                                }}
                              >
                                无匹配的物理进程组
                              </div>
                            ) : (
                              filteredGroups.map((g) => {
                                const isCurrent = String(g.id) === String(archProcess.group_id);
                                const isStaging = g.code === 'PGRP_CSG_AUTO';
                                const procCount = g.processes?.length ?? g.process_count ?? 0;

                                return (
                                  <button
                                    key={g.id}
                                    type="button"
                                    className={`code-arch-proc-group-menu__item ${
                                      isCurrent ? 'code-arch-proc-group-menu__item--current' : ''
                                    }`}
                                    onClick={() => void handleSelectQuickGroup(String(g.id))}
                                    disabled={groupMoving}
                                    data-testid={`quick-group-option-${g.id}`}
                                  >
                                    <div className="code-arch-proc-group-menu__item-main">
                                      <span className="code-arch-proc-group-menu__item-name">
                                        {g.name}
                                      </span>
                                      <span className="code-arch-proc-group-menu__item-code code-font-mono">
                                        {g.code}
                                      </span>
                                    </div>
                                    <div className="code-arch-proc-group-menu__item-meta">
                                      {isStaging && (
                                        <span className="code-arch-proc-staging-badge">暂存</span>
                                      )}
                                      <span>{procCount}</span>
                                      {isCurrent && <Check size={13} />}
                                    </div>
                                  </button>
                                );
                              })
                            )}
                          </div>

                          {onQuickCreateGroupAndMove && (
                            <div className="code-arch-proc-group-menu__footer">
                              <button
                                type="button"
                                className="code-btn code-btn--secondary code-btn--xs"
                                style={{ width: '100%', justifyContent: 'center' }}
                                onClick={() => {
                                  setCreatingGroup(true);
                                  setCreateGroupError('');
                                }}
                                data-testid="quick-create-group-trigger"
                              >
                                <Plus size={12} />
                                <span>新建进程组并移入</span>
                              </button>
                            </div>
                          )}
                        </>
                      )}
                    </div>
                  )}
                </div>
              ) : (
                <span className="code-badge code-badge--secondary" style={{ fontSize: '0.75rem' }}>
                  所属组: {archProcess.group?.name || '默认进程组'}
                </span>
              )}
            </div>

            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: '1.25rem',
                fontSize: '0.825rem',
                color: 'var(--color-text-secondary)',
                marginTop: '0.5rem',
              }}
            >
              <span>
                默认端口:{' '}
                <strong className="code-font-mono" style={{ color: 'var(--color-text-primary)' }}>
                  {isClient ? '0 (纯客户端)' : archProcess.default_listen_port}
                </strong>
              </span>
              <span>
                命令行匹配特征:{' '}
                <span
                  className="code-font-mono"
                  style={{
                    color: archProcess.default_cmd_pattern
                      ? 'var(--color-primary)'
                      : 'var(--color-text-muted)',
                  }}
                >
                  {archProcess.default_cmd_pattern || '无'}
                </span>
              </span>
              <span>
                多环境挂载实例: <strong>{agentMounts.length}</strong>
              </span>
            </div>

            {archProcess.description && (
              <p
                style={{
                  margin: '0.5rem 0 0',
                  fontSize: '0.825rem',
                  color: 'var(--color-text-muted)',
                  lineHeight: 1.5,
                }}
              >
                {archProcess.description}
              </p>
            )}
          </div>

          {isAdmin && (
            <div style={{ display: 'flex', gap: '0.5rem' }}>
              <button
                type="button"
                className="code-btn code-btn--secondary code-btn--sm"
                onClick={onEditProcess}
                data-testid="edit-arch-process-btn"
              >
                <Edit2 size={13} />
                <span>编辑进程</span>
              </button>
              <button
                type="button"
                className="code-btn code-btn--danger code-btn--sm"
                onClick={onDeleteProcess}
                data-testid="delete-arch-process-btn"
              >
                <Trash2 size={13} />
                <span>删除</span>
              </button>
            </div>
          )}
        </div>

        {/* 端口冲突自动保护警示 Banner */}
        {autoPinnedOverrides && autoPinnedOverrides.length > 0 && (
          <div
            className="code-agent-alert code-agent-alert--warning"
            data-testid="auto-pinned-overrides-banner"
            style={{
              marginTop: '1rem',
              display: 'flex',
              alignItems: 'flex-start',
              gap: '0.6rem',
              padding: '0.75rem 1rem',
              borderRadius: 'var(--radius-md)',
              background: 'rgba(234, 179, 8, 0.12)',
              border: '1px solid var(--color-warning)',
              color: 'var(--color-text-primary)',
            }}
          >
            <AlertTriangle size={18} style={{ color: 'var(--color-warning)', flexShrink: 0, marginTop: '2px' }} />
            <div>
              <div style={{ fontWeight: 600, fontSize: '0.875rem', marginBottom: '0.2rem' }}>
                默认端口变更触发多环境 Agent 端口冲突保护机制 (AC-M10-01)
              </div>
              <div style={{ fontSize: '0.8rem', color: 'var(--color-text-secondary)', lineHeight: 1.5 }}>
                以下宿主机环境中存在其他进程占用新默认端口，系统已自动为其实例固化覆盖端口：
                <ul style={{ margin: '0.3rem 0 0', paddingLeft: '1.2rem' }}>
                  {autoPinnedOverrides.map((o) => (
                    <li key={`${o.agent_id}-${o.process_id}`}>
                      Agent #{o.agent_id} (进程映射 #{o.process_id}) ➜ 自动固化端口为{' '}
                      <strong>{o.override_listen_port}</strong>
                    </li>
                  ))}
                </ul>
              </div>
            </div>
          </div>
        )}
      </div>

      {/* 选项卡栏 */}
      <div
        style={{
          display: 'flex',
          borderBottom: '1px solid var(--color-border-primary)',
          background: 'var(--color-bg-surface)',
          padding: '0 1.5rem',
        }}
      >
        <button
          type="button"
          onClick={() => setActiveTab('modules')}
          style={{
            padding: '0.75rem 1rem',
            background: 'transparent',
            border: 'none',
            borderBottom: activeTab === 'modules' ? '2px solid var(--color-primary)' : '2px solid transparent',
            color: activeTab === 'modules' ? 'var(--color-primary)' : 'var(--color-text-secondary)',
            fontWeight: activeTab === 'modules' ? 600 : 500,
            cursor: 'pointer',
            fontSize: '0.875rem',
            display: 'flex',
            alignItems: 'center',
            gap: '0.4rem',
          }}
          data-testid="tab-modules"
        >
          <Cpu size={14} />
          <span>关联功能模块 ({modules.length})</span>
        </button>

        <button
          type="button"
          onClick={() => setActiveTab('contracts')}
          style={{
            padding: '0.75rem 1rem',
            background: 'transparent',
            border: 'none',
            borderBottom: activeTab === 'contracts' ? '2px solid var(--color-primary)' : '2px solid transparent',
            color: activeTab === 'contracts' ? 'var(--color-primary)' : 'var(--color-text-secondary)',
            fontWeight: activeTab === 'contracts' ? 600 : 500,
            cursor: 'pointer',
            fontSize: '0.875rem',
            display: 'flex',
            alignItems: 'center',
            gap: '0.4rem',
          }}
          data-testid="tab-contracts"
        >
          <Layers size={14} />
          <span>进程级服务契约 (供 {providedServices.length} | 需 {dependencies.length})</span>
        </button>

        <button
          type="button"
          onClick={() => setActiveTab('mounts')}
          style={{
            padding: '0.75rem 1rem',
            background: 'transparent',
            border: 'none',
            borderBottom: activeTab === 'mounts' ? '2px solid var(--color-primary)' : '2px solid transparent',
            color: activeTab === 'mounts' ? 'var(--color-primary)' : 'var(--color-text-secondary)',
            fontWeight: activeTab === 'mounts' ? 600 : 500,
            cursor: 'pointer',
            fontSize: '0.875rem',
            display: 'flex',
            alignItems: 'center',
            gap: '0.4rem',
          }}
          data-testid="tab-mounts"
        >
          <Radio size={14} />
          <span>多环境挂载溯源 ({agentMounts.length})</span>
        </button>
      </div>

      {/* 选项卡内容区 */}
      <div style={{ flex: 1, overflowY: 'auto', padding: '1.25rem 1.5rem' }}>
        {/* Tab 1: 关联功能模块 */}
        {activeTab === 'modules' && (
          <div>
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                marginBottom: '1rem',
              }}
            >
              <div>
                <h4 style={{ margin: 0, fontSize: '0.95rem', fontWeight: 600 }}>承载功能模块清单</h4>
                <span style={{ fontSize: '0.8rem', color: 'var(--color-text-muted)' }}>
                  逻辑功能模块依托本物理进程进行对外服务承载与网络监听
                </span>
              </div>
              {isAdmin && onBindModule && (
                <button
                  type="button"
                  className="code-btn code-btn--secondary code-btn--sm"
                  onClick={() => setBindDrawerOpen(true)}
                  data-testid="bind-module-btn"
                >
                  <Plus size={13} />
                  <span>关联功能模块</span>
                </button>
              )}
            </div>

            {modules.length === 0 ? (
              <div
                style={{
                  padding: '3rem 1rem',
                  textAlign: 'center',
                  background: 'var(--color-bg-base)',
                  borderRadius: 'var(--radius-md)',
                  border: '1px dashed var(--color-border-primary)',
                  color: 'var(--color-text-muted)',
                }}
              >
                此标准物理进程尚未关联任何逻辑功能模块
              </div>
            ) : (
              <div className="code-table-wrapper">
                <table className="code-table">
                  <thead>
                    <tr>
                      <th>模块编码</th>
                      <th>模块名称</th>
                      <th>所属子系统</th>
                      <th>所属功能组</th>
                      <th>负责人</th>
                      {isAdmin && onUnbindModule && <th style={{ width: '80px' }}>操作</th>}
                    </tr>
                  </thead>
                  <tbody>
                    {modules.map((m) => (
                      <tr key={m.id} data-testid={`bound-module-row-${m.id}`}>
                        <td className="code-font-mono code-color-primary">{m.code}</td>
                        <td style={{ fontWeight: 600 }}>{m.name}</td>
                        <td>{m.subsystem?.name || '-'}</td>
                        <td>{m.group?.name || '-'}</td>
                        <td>{m.owner_name || '平台管理员'}</td>
                        {isAdmin && onUnbindModule && (
                          <td>
                            <button
                              type="button"
                              className="code-btn code-btn--secondary code-btn--xs"
                              onClick={() => void onUnbindModule(String(m.id))}
                              title="从物理进程中解除关联"
                              data-testid={`unbind-module-btn-${m.id}`}
                            >
                              <Unlink size={12} />
                              <span>解除</span>
                            </button>
                          </td>
                        )}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        )}

        {/* Tab 2: 进程级服务契约 */}
        {activeTab === 'contracts' && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: '1.5rem' }}>
            {/* 对外提供服务 */}
            <div>
              <h4 style={{ margin: '0 0 0.5rem', fontSize: '0.95rem', fontWeight: 600 }}>
                对外提供服务契约 (PROVIDE - 共 {providedServices.length} 个)
              </h4>
              {providedServices.length === 0 ? (
                <div
                  style={{
                    padding: '1.5rem',
                    textAlign: 'center',
                    background: 'var(--color-bg-base)',
                    borderRadius: 'var(--radius-md)',
                    color: 'var(--color-text-muted)',
                    fontSize: '0.85rem',
                  }}
                >
                  暂无对外声明的 RPC/HTTP 服务契约
                </div>
              ) : (
                <div className="code-table-wrapper">
                  <table className="code-table">
                    <thead>
                      <tr>
                        <th>接口契约服务名</th>
                        <th>供给功能模块</th>
                        <th>差异忽略字段</th>
                        <th>描述</th>
                      </tr>
                    </thead>
                    <tbody>
                      {providedServices.map((svc) => (
                        <tr key={svc.id}>
                          <td className="code-font-mono code-color-primary" style={{ fontWeight: 600 }}>
                            {svc.service_name}
                          </td>
                          <td>
                            <span className="code-badge code-badge--secondary">
                              {modules.find((m) => String(m.id) === String(svc.module_id))?.name || `模块 #${svc.module_id}`}
                            </span>
                          </td>
                          <td className="code-font-mono code-text-xs code-color-muted">
                            {svc.diff_ignore_paths && svc.diff_ignore_paths.length > 0
                              ? svc.diff_ignore_paths.join(', ')
                              : '-'}
                          </td>
                          <td>{svc.description || '-'}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </div>

            {/* 对外依赖消费 */}
            <div>
              <h4 style={{ margin: '0 0 0.5rem', fontSize: '0.95rem', fontWeight: 600 }}>
                所需依赖服务契约 (DEPEND - 共 {dependencies.length} 个)
              </h4>
              {dependencies.length === 0 ? (
                <div
                  style={{
                    padding: '1.5rem',
                    textAlign: 'center',
                    background: 'var(--color-bg-base)',
                    borderRadius: 'var(--radius-md)',
                    color: 'var(--color-text-muted)',
                    fontSize: '0.85rem',
                  }}
                >
                  此物理进程未声明任何对外部或其他进程的调用依赖
                </div>
              ) : (
                <div className="code-table-wrapper">
                  <table className="code-table">
                    <thead>
                      <tr>
                        <th>调用类型</th>
                        <th>目标服务名</th>
                        <th>目标提供方</th>
                        <th>消费发起模块</th>
                        <th>描述</th>
                      </tr>
                    </thead>
                    <tbody>
                      {dependencies.map((dep) => (
                        <tr key={dep.id}>
                          <td>
                            {dep.is_external ? (
                              <span
                                className="code-badge"
                                style={{
                                  background: 'rgba(234, 179, 8, 0.15)',
                                  color: 'var(--color-warning)',
                                }}
                              >
                                <Globe size={11} /> 外部系统
                              </span>
                            ) : (
                              <span
                                className="code-badge"
                                style={{
                                  background: 'rgba(59, 130, 246, 0.15)',
                                  color: 'var(--color-primary)',
                                }}
                              >
                                内部服务
                              </span>
                            )}
                          </td>
                          <td className="code-font-mono" style={{ fontWeight: 600 }}>
                            {dep.is_external ? dep.external_service : dep.service?.service_name || '-'}
                          </td>
                          <td>
                            {dep.is_external
                              ? dep.external_system
                              : dep.provider_module?.name || '-'}
                          </td>
                          <td>
                            {modules.find((m) => String(m.id) === String(dep.consumer_module_id))?.name ||
                              `模块 #${dep.consumer_module_id}`}
                          </td>
                          <td>{dep.description || '-'}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </div>
          </div>
        )}

        {/* Tab 3: 多环境挂载溯源 */}
        {activeTab === 'mounts' && (
          <div>
            <div style={{ marginBottom: '1rem' }}>
              <h4 style={{ margin: 0, fontSize: '0.95rem', fontWeight: 600 }}>
                多环境 Agent 实例挂载全景
              </h4>
              <span style={{ fontSize: '0.8rem', color: 'var(--color-text-muted)' }}>
                展示该架构进程在各宿主机 Agent 上的 1:1 物化映射、生效端口与 eBPF 探针状态
              </span>
            </div>

            {agentMounts.length === 0 ? (
              <div
                style={{
                  padding: '3rem 1rem',
                  textAlign: 'center',
                  background: 'var(--color-bg-base)',
                  borderRadius: 'var(--radius-md)',
                  color: 'var(--color-text-muted)',
                }}
              >
                尚未在任何 Agent 宿主机上挂载此进程。可在「Agent 运维」中心批量挂载。
              </div>
            ) : (
              <div className="code-table-wrapper">
                <table className="code-table">
                  <thead>
                    <tr>
                      <th>宿主机 Agent</th>
                      <th>作用域</th>
                      <th>生效端点 (Effective IP:Port)</th>
                      <th>端口策略</th>
                      <th>探针就绪状态</th>
                      <th>期望状态</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {agentMounts.map((m) => {
                      const effPort = m.effective_listen_port ?? m.listen_port;
                      const hasOverride =
                        m.override_listen_port !== undefined && m.override_listen_port !== null;

                      return (
                        <tr key={m.id} data-testid={`agent-mount-row-${m.id}`}>
                          <td>
                            <div style={{ display: 'flex', flexDirection: 'column' }}>
                              <strong style={{ color: 'var(--color-text-primary)' }}>
                                {m.agent?.name || `Agent #${m.agent_id}`}
                              </strong>
                              <span className="code-font-mono code-text-xs code-color-muted">
                                {m.agent?.endpoint || '-'}
                              </span>
                            </div>
                          </td>
                          <td>
                            <span
                              className={`code-badge ${
                                m.agent?.scope === 'SHARED'
                                  ? 'code-badge--primary'
                                  : 'code-badge--secondary'
                              }`}
                            >
                              {m.agent?.scope || 'SHARED'}
                            </span>
                          </td>
                          <td className="code-font-mono" style={{ fontWeight: 600 }}>
                            {effPort === 0
                              ? isMeshTransportProcess(m)
                                ? m.observed_role === 'CLIENT_ONLY'
                                  ? 'dophi_mesh (客户端)'
                                  : 'dophi_mesh (内存环)'
                                : '0 (纯客户端)'
                              : `${m.advertise_ip || '127.0.0.1'}:${effPort}`}
                          </td>
                          <td>
                            {hasOverride ? (
                              <span
                                className="code-font-mono"
                                style={{
                                  fontSize: '0.75rem',
                                  color: 'var(--color-warning)',
                                  background: 'rgba(234, 179, 8, 0.1)',
                                  padding: '1px 6px',
                                  borderRadius: 'var(--radius-sm)',
                                }}
                              >
                                覆盖为 :{m.override_listen_port}
                              </span>
                            ) : (
                              <span className="code-color-muted code-text-xs">继承默认</span>
                            )}
                          </td>
                          <td>{renderProbeStatusBadge(m.probe_status, m.diag_message || m.probe_status_reason)}</td>
                          <td>
                            <span
                              className="code-badge"
                              style={{
                                background:
                                  m.probe_desired_state === 'ARMED'
                                    ? 'rgba(34, 197, 94, 0.12)'
                                    : 'var(--color-bg-muted)',
                                color:
                                  m.probe_desired_state === 'ARMED'
                                    ? 'var(--color-success)'
                                    : 'var(--color-text-muted)',
                              }}
                            >
                              {m.probe_desired_state || 'ARMED'}
                            </span>
                          </td>
                          <td>
                            <button
                              type="button"
                              className="code-btn code-btn--secondary code-btn--xs"
                              onClick={() =>
                                navigate(
                                  modulePath(
                                    `/agents?agent_id=${m.agent_id}&process_id=${m.id}&scope=${
                                      m.agent?.scope || 'SHARED'
                                    }`
                                  )
                                )
                              }
                              title="前往该 Agent 管理页查看探针并实施运维"
                              data-testid={`goto-agent-${m.agent_id}`}
                            >
                              <ExternalLink size={12} />
                              <span>Agent 运维</span>
                            </button>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        )}
      </div>

      {/* 关联功能模块抽屉 (Drawer) */}
      <Drawer
        open={bindDrawerOpen}
        onClose={() => setBindDrawerOpen(false)}
        title={`关联功能模块至 [${archProcess.name}]`}
        subtitle="将逻辑功能模块绑定至当前物理进程进行对外服务承载"
        width="md"
        footer={
          <div className="code-drawer-footer">
            <button
              type="button"
              className="code-btn code-btn--secondary"
              onClick={() => setBindDrawerOpen(false)}
            >
              取消
            </button>
            <button
              type="button"
              className="code-btn code-btn--primary"
              disabled={!selectedModuleIdToBind || bindingSubmitting}
              onClick={() => void handleExecuteBind()}
            >
              {bindingSubmitting ? '保存中...' : '确认关联'}
            </button>
          </div>
        }
      >
        <div className="code-form">
          <div className="code-form-item">
            <label className="code-form-label">选择待绑定的功能模块:</label>
            <select
              className="code-select"
              value={selectedModuleIdToBind}
              onChange={(e) => setSelectedModuleIdToBind(e.target.value)}
            >
              <option value="">-- 请选择模块 --</option>
              {candidateModules.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name} ({m.code})
                </option>
              ))}
            </select>
          </div>
        </div>
      </Drawer>
    </div>
  );
};
export default ArchProcessDetailPane;
