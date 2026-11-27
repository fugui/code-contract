import React, { useMemo, useState } from 'react';
import {
  AlertTriangle,
  CheckCircle2,
  GitBranch,
  Layers,
  ShieldAlert,
  Sparkles,
  Wrench,
} from 'lucide-react';
import { Drawer, useConfirm } from '@code/common';
import * as schemaApi from '../../api/schema';
import { useToast } from '../../utils/toast';
import type {
  PoolAutoFixEntryDTO,
  PoolReconcileReportDTO,
  PoolReconcileRunDTO,
  PromoteAutoFixItem,
} from '../../types';
import './ProtoDocsAndCodeViewer.css';

export interface PoolReconcileReportDrawerProps {
  open: boolean;
  isAdmin: boolean;
  run: PoolReconcileRunDTO | null;
  report: PoolReconcileReportDTO | null;
  loading?: boolean;
  onClose: () => void;
  onPromoted?: () => void;
}

type ReportTabKey = 'conflicts' | 'auto_fixes' | 'quarantined' | 'harvested';

function fixItemKey(item: PoolAutoFixEntryDTO, idx: number): string {
  return `${idx}:${item.fix_kind}:${item.file_path}:${item.old_import || ''}->${item.new_import}`;
}

export const PoolReconcileReportDrawer: React.FC<PoolReconcileReportDrawerProps> = ({
  open,
  isAdmin,
  run,
  report,
  loading = false,
  onClose,
  onPromoted,
}) => {
  const toast = useToast();
  const confirmDialog = useConfirm();
  const [activeTab, setActiveTab] = useState<ReportTabKey>('conflicts');
  const [selectedFixKeys, setSelectedFixKeys] = useState<Set<string>>(new Set());
  const [promoting, setPromoting] = useState(false);

  const conflicts = useMemo(() => report?.conflicts ?? [], [report]);
  const autoFixes = useMemo(() => report?.auto_fixes ?? [], [report]);
  const quarantined = useMemo(() => report?.quarantined ?? [], [report]);
  const harvested = useMemo(() => report?.harvested ?? [], [report]);
  const stats = report?.stats;

  const toggleSelectFix = (key: string, checked: boolean) => {
    setSelectedFixKeys((prev) => {
      const next = new Set(prev);
      if (checked) next.add(key);
      else next.delete(key);
      return next;
    });
  };

  const selectedFixItems: PromoteAutoFixItem[] = useMemo(() => {
    const out: PromoteAutoFixItem[] = [];
    autoFixes.forEach((fx, idx) => {
      if (selectedFixKeys.has(fixItemKey(fx, idx))) {
        out.push({
          file_path: fx.file_path,
          fix_kind: fx.fix_kind,
          old_import: fx.old_import,
          new_import: fx.new_import,
          repo_id: fx.repo_id,
          bundle_id: fx.bundle_id,
        });
      }
    });
    return out;
  }, [autoFixes, selectedFixKeys]);

  const handlePromoteSelected = async () => {
    if (!isAdmin || selectedFixItems.length === 0) return;
    const confirmed = await confirmDialog({
      title: '确认固化所选 R7 自动修复规则',
      content: `将把选中的 ${selectedFixItems.length} 条修复写入激活治理配置 (global.import_rewrites) 或人工裁决归属 (MANUAL)。确认继续？`,
      confirmText: '确认固化',
      cancelText: '取消',
      type: 'info',
    });
    if (!confirmed) return;

    setPromoting(true);
    try {
      const res = await schemaApi.promotePoolAutoFixes({
        run_id: run?.id,
        fixes: selectedFixItems,
      });
      toast.success(
        `固化完成：import_rewrites +${res.promoted_import_rewrites}，人工裁决 +${res.promoted_manual_owners} (rev=${res.governance_revision})`
      );
      setSelectedFixKeys(new Set());
      onPromoted?.();
    } catch (err) {
      toast.error((err as Error).message || '固化自动修复规则失败');
    } finally {
      setPromoting(false);
    }
  };

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title="全池 Stage C+ 编译收敛与 Workspace 导出报告 (ADR-28)"
      subtitle={
        run
          ? `运行 #${run.id} · 状态 ${run.status} · Workspace 版本 ${run.workspace_version || '—'}`
          : '查看 R1~R4 闭环收敛、R7 自愈详情、R8 根因隔离及一键固化'
      }
      width={880}
    >
      <div className="pgit-drawer-stack" data-testid="pool-reconcile-report-drawer">
        {loading ? (
          <div className="pdoc-card">正在加载全池协调报告...</div>
        ) : !report ? (
          <div className="pdoc-card">
            <p className="pdoc-card__subtitle">
              尚无全池 Stage C+ 协调运行记录，请先点击「全池编译收敛与工作区导出」。
            </p>
          </div>
        ) : (
          <>
            <div className="pgit-summary-grid">
              <div className="pgit-stat-card">
                <span className="pgit-stat-card__label">服务闭包文件</span>
                <strong className="pgit-stat-card__val is-success">
                  {stats?.closure_file_count ?? run?.closure_file_count ?? 0}
                </strong>
                <span className="pdoc-card__subtitle">
                  uService 种子 {stats?.service_count ?? run?.service_count ?? 0}
                </span>
              </div>
              <div className="pgit-stat-card">
                <span className="pgit-stat-card__label">R4 基座收割</span>
                <strong className="pgit-stat-card__val">
                  {stats?.harvested_base_count ?? harvested.length}
                </strong>
                <span className="pdoc-card__subtitle">逻辑路径 {stats?.logical_path_count ?? 0}</span>
              </div>
              <div className="pgit-stat-card">
                <span className="pgit-stat-card__label">R3 同路径冲突</span>
                <strong className="pgit-stat-card__val is-warning">
                  {stats?.conflict_count ?? conflicts.length}
                </strong>
                <span className="pdoc-card__subtitle">
                  剥离新碰撞 {stats?.post_strip_collision_count ?? 0}
                </span>
              </div>
              <div className="pgit-stat-card">
                <span className="pgit-stat-card__label">R7 自动修复</span>
                <strong className="pgit-stat-card__val is-success">
                  {stats?.auto_fix_count ?? autoFixes.length}
                </strong>
                <span className="pdoc-card__subtitle">收敛轮次 {stats?.r7_rounds ?? 0}</span>
              </div>
              <div className="pgit-stat-card">
                <span className="pgit-stat-card__label">R8 根因隔离</span>
                <strong className="pgit-stat-card__val is-danger">
                  {stats?.quarantined_count ?? quarantined.length}
                </strong>
                <span className="pdoc-card__subtitle">
                  剩余编译错 {stats?.compile_error_count ?? 0}
                </span>
              </div>
            </div>

            <div className="pgit-toolbar-row">
              <div className="code-tab-pills" role="tablist" aria-label="全池协调报告分类">
                <button
                  type="button"
                  role="tab"
                  aria-selected={activeTab === 'conflicts'}
                  className={`code-pill-btn ${activeTab === 'conflicts' ? 'code-pill-btn-active' : ''}`}
                  onClick={() => setActiveTab('conflicts')}
                >
                  <GitBranch size={13} /> R3 冲突与 symbol_diff ({conflicts.length})
                </button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={activeTab === 'auto_fixes'}
                  className={`code-pill-btn ${activeTab === 'auto_fixes' ? 'code-pill-btn-active' : ''}`}
                  onClick={() => setActiveTab('auto_fixes')}
                >
                  <Wrench size={13} /> R7 自动修复 ({autoFixes.length})
                </button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={activeTab === 'quarantined'}
                  className={`code-pill-btn ${activeTab === 'quarantined' ? 'code-pill-btn-active' : ''}`}
                  onClick={() => setActiveTab('quarantined')}
                >
                  <ShieldAlert size={13} /> R8 隔离清单 ({quarantined.length})
                </button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={activeTab === 'harvested'}
                  className={`code-pill-btn ${activeTab === 'harvested' ? 'code-pill-btn-active' : ''}`}
                  onClick={() => setActiveTab('harvested')}
                >
                  <Layers size={13} /> R4 收割基座 ({harvested.length})
                </button>
              </div>
            </div>

            {activeTab === 'conflicts' && (
              <div className="pdoc-card" data-testid="pool-report-conflicts-tab">
                {conflicts.length === 0 ? (
                  <p className="pdoc-card__subtitle">未检测到同逻辑路径多仓重复候选。</p>
                ) : (
                  <div className="pgit-autofix-list">
                    {conflicts.map((c) => (
                      <div key={c.logical_path} className="pgit-path-compare">
                        <div className="pgit-toolbar-row">
                          <strong>
                            <code>{c.logical_path}</code>
                          </strong>
                          <div className="pgit-toolbar-actions">
                            <span className="code-badge code-badge-info">
                              赢家：{c.winner_repo_name} ({c.reason})
                            </span>
                            {c.content_identical ? (
                              <span className="code-badge code-badge-success">内容完全一致</span>
                            ) : (
                              <span className="code-badge code-badge-warning">存在内容/符号差异</span>
                            )}
                          </div>
                        </div>
                        {c.candidates.map((cand) => {
                          const key = `${cand.repo_name}:${cand.repo_file_path}`;
                          const diffSyms = c.symbol_diff?.[key] ?? cand.unique_symbols ?? [];
                          return (
                            <div key={key} className="pgit-dryrun-repo-row">
                              <span>
                                {cand.is_winner ? (
                                  <CheckCircle2 size={13} />
                                ) : (
                                  <AlertTriangle size={13} />
                                )}{' '}
                                <code>{key}</code> · SHA256 <code>{cand.content_sha256.slice(0, 12)}</code>
                              </span>
                              <span className="pdoc-meta-row">
                                {diffSyms.length > 0 && (
                                  <span className="pdoc-pill">独有符号: {diffSyms.join(', ')}</span>
                                )}
                              </span>
                            </div>
                          );
                        })}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}

            {activeTab === 'auto_fixes' && (
              <div className="pdoc-card" data-testid="pool-report-autofixes-tab">
                <div className="pdoc-card__header">
                  <span className="pdoc-card__subtitle">
                    勾选确定无误的修复条目，一键固化写入全局规则或手工归属 SSOT
                  </span>
                  <button
                    type="button"
                    className="btn-primary btn-sm"
                    data-testid="promote-autofix-btn"
                    disabled={!isAdmin || promoting || selectedFixItems.length === 0}
                    onClick={() => void handlePromoteSelected()}
                  >
                    <Sparkles size={13} />{' '}
                    {promoting
                      ? '固化中...'
                      : `将所选固化为治理规则 (${selectedFixItems.length})`}
                  </button>
                </div>
                {autoFixes.length === 0 ? (
                  <p className="pdoc-card__subtitle">本轮协调无需 R7 自动修复。</p>
                ) : (
                  <div className="pgit-autofix-list">
                    {autoFixes.map((fx, idx) => {
                      const key = fixItemKey(fx, idx);
                      const checked = selectedFixKeys.has(key);
                      return (
                        <label key={key} className="pgit-dryrun-repo-row">
                          <span className="pgit-check">
                            <input
                              type="checkbox"
                              checked={checked}
                              disabled={!isAdmin}
                              onChange={(e) => toggleSelectFix(key, e.target.checked)}
                            />
                            <span className="code-badge code-badge-info">{fx.fix_kind}</span>
                            <code>{fx.file_path}</code>
                            <span>
                              {fx.old_import ? `${fx.old_import} → ` : '+ '}
                              <code>{fx.new_import}</code>
                            </span>
                          </span>
                          <span className="pdoc-meta-row">
                            {fx.symbol && <span className="pdoc-pill">符号: {fx.symbol}</span>}
                            <span className="pdoc-pill">R{fx.round}</span>
                          </span>
                        </label>
                      );
                    })}
                  </div>
                )}
              </div>
            )}

            {activeTab === 'quarantined' && (
              <div className="pdoc-card" data-testid="pool-report-quarantined-tab">
                {quarantined.length === 0 ? (
                  <p className="pdoc-card__subtitle">全部闭包文件编译通过，无 R8 隔离文件。</p>
                ) : (
                  <div className="pgit-autofix-list">
                    {quarantined.map((q) => (
                      <div key={`${q.repo_name}:${q.logical_path}`} className="pgit-dryrun-repo-row">
                        <span>
                          {q.is_root_cause ? (
                            <span className="code-badge code-badge-danger">根因 (Root Cause)</span>
                          ) : (
                            <span className="code-badge code-badge-warning">下游受害者</span>
                          )}{' '}
                          <code>{q.logical_path}</code> ({q.repo_name}) · {q.reason}
                        </span>
                        <span className="pdoc-pill">报错 {q.error_count}</span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}

            {activeTab === 'harvested' && (
              <div className="pdoc-card" data-testid="pool-report-harvested-tab">
                {harvested.length === 0 ? (
                  <p className="pdoc-card__subtitle">本轮协调未收割额外共享基座文件。</p>
                ) : (
                  <div className="pgit-autofix-list">
                    {harvested.map((h) => (
                      <div key={`${h.logical_path}:${h.source_abs_path}`} className="pgit-dryrun-repo-row">
                        <span>
                          <span className="code-badge code-badge-info">{h.triggered_by}</span>{' '}
                          <code>{h.logical_path}</code>
                        </span>
                        <span className="pdoc-meta-row">
                          <span className="pdoc-pill">优先级 P{h.source_priority}</span>
                          <code title={h.source_abs_path}>{h.source_abs_path}</code>
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}
          </>
        )}
      </div>
    </Drawer>
  );
};

export default PoolReconcileReportDrawer;
