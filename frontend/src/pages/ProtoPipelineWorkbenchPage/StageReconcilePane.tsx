import React, { useEffect, useState } from 'react';
import {
  AlertTriangle,
  ChevronDown,
  ChevronRight,
  Code2,
  FileCheck2,
  Play,
  ShieldAlert,
  Wrench,
} from 'lucide-react';
import * as schemaApi from '../../api/schema';
import { useToast } from '../../utils/toast';
import { PoolReconcileReportDrawer } from '../ProtoSchemaWorkbenchPage/PoolReconcileReportDrawer';
import { ProtoConflictArbitrationDrawer } from '../ProtoSchemaWorkbenchPage/ProtoConflictArbitrationDrawer';
import {
  ProtoSourceAnchorDrawer,
  useSourceAnchorDrawer,
} from '../ProtoSchemaWorkbenchPage/ProtoSourceAnchorDrawer';
import type {
  PipelineOverviewDTO,
  PoolReconcileProgressEventPayload,
  PoolReconcileResponse,
} from '../../types';

export interface StageReconcilePaneProps {
  overview: PipelineOverviewDTO | null;
  isAdmin: boolean;
  locked: boolean;
  poolProgress: PoolReconcileProgressEventPayload | null;
  onRefresh: () => void;
  initialReconcileResp?: PoolReconcileResponse | null;
}

export const StageReconcilePane: React.FC<StageReconcilePaneProps> = ({
  overview,
  isAdmin,
  locked,
  poolProgress,
  onRefresh,
  initialReconcileResp = null,
}) => {
  const toast = useToast();
  const sourceAnchorDrawer = useSourceAnchorDrawer();
  const [reconcileResp, setReconcileResp] = useState<PoolReconcileResponse | null>(
    initialReconcileResp
  );
  const [subTab, setSubTab] = useState<'conflicts' | 'auto_fixes' | 'quarantined' | 'harvested'>(
    'conflicts'
  );
  const [running, setRunning] = useState(false);
  const [reportDrawerOpen, setReportDrawerOpen] = useState(false);
  const [conflictDrawerOpen, setConflictDrawerOpen] = useState(false);
  const [promoting, setPromoting] = useState(false);
  const [resolvingKey, setResolvingKey] = useState<string | null>(null);
  const [collapsedConflicts, setCollapsedConflicts] = useState<Set<string>>(new Set());

  const loadLatestRun = async () => {
    try {
      const res = await schemaApi.getLatestPoolReconcileRun();
      setReconcileResp(res);
    } catch {
      // 尚未存在收敛记录时静默跳过
    }
  };

  useEffect(() => {
    if (initialReconcileResp !== null) return;
    void loadLatestRun();
  }, [initialReconcileResp, overview?.stage3.latest_run?.id]);

  const handleRunReconcile = async () => {
    if (!isAdmin || locked) return;
    setRunning(true);
    try {
      await schemaApi.triggerPoolReconcile({ sync: true });
      toast.success('阶段 ③ 全池依赖收敛与工作区导出完成');
      await loadLatestRun();
      onRefresh();
    } catch (err) {
      toast.error((err as Error).message || '触发全池收敛失败');
    } finally {
      setRunning(false);
    }
  };

  const handleResolveInlineConflict = async (
    logicalPath: string,
    ownerGitRepoId: number,
    ownerBundleId: number
  ) => {
    if (!isAdmin || locked) return;
    const key = `${logicalPath}:${ownerGitRepoId}:${ownerBundleId}`;
    setResolvingKey(key);
    try {
      await schemaApi.resolveProtoConflict({
        kind: 'IMPORT',
        logical_path: logicalPath,
        owner_git_repo_id: ownerGitRepoId,
        owner_bundle_id: ownerBundleId,
      });
      toast.success(`已将 [${logicalPath}] 权威提供方设为所选仓库，请重新全池收敛验证`);
      await loadLatestRun();
      onRefresh();
    } catch (err) {
      toast.error((err as Error).message || '冲突裁决失败');
    } finally {
      setResolvingKey(null);
    }
  };

  const handlePromoteAllAutoFixes = async () => {
    if (!isAdmin || locked || !reconcileResp?.report?.auto_fixes?.length) return;
    setPromoting(true);
    try {
      const fixes = reconcileResp.report.auto_fixes
        .filter((f) => f.fix_kind !== 'variant_swap')
        .map((f) => ({
          file_path: f.file_path,
          fix_kind: f.fix_kind,
          old_import: f.old_import,
          new_import: f.new_import,
          repo_id: f.repo_id,
          bundle_id: f.bundle_id,
        }));
      if (fixes.length === 0) {
        toast.info('当前无可固化至全局配置的导入修复项');
        return;
      }
      const res = await schemaApi.promotePoolAutoFixes({
        run_id: reconcileResp.run?.id,
        fixes,
      });
      toast.success(
        `已将 ${res.promoted_import_rewrites} 条自动修复规则固化至全局配置 rev #${res.governance_revision}`
      );
      onRefresh();
    } catch (err) {
      toast.error((err as Error).message || '固化规则失败');
    } finally {
      setPromoting(false);
    }
  };

  const toggleConflictExpand = (logicalPath: string) => {
    setCollapsedConflicts((prev) => {
      const next = new Set(prev);
      if (next.has(logicalPath)) next.delete(logicalPath);
      else next.add(logicalPath);
      return next;
    });
  };

  const stage3 = overview?.stage3;
  const conflicts = reconcileResp?.report?.conflicts || [];
  const autoFixes = reconcileResp?.report?.auto_fixes || [];
  const quarantined = reconcileResp?.report?.quarantined || [];
  const harvested = reconcileResp?.report?.harvested || [];

  return (
    <div className="ppipe-panel-card" data-testid="stage-reconcile-pane">
      <div className="ppipe-panel-header">
        <h3>
          <Wrench size={17} /> 阶段 ③：全池依赖收敛与冲突仲裁
        </h3>
        <div className="ppipe-actions-row">
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            onClick={() => setConflictDrawerOpen(true)}
          >
            <ShieldAlert size={14} /> 权威归属台账
          </button>
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            onClick={() => setReportDrawerOpen(true)}
          >
            <FileCheck2 size={14} /> 完整收敛报告
          </button>
          {isAdmin && (
            <button
              type="button"
              className="btn btn-primary btn-sm"
              onClick={() => void handleRunReconcile()}
              disabled={running || locked}
            >
              <Play size={14} /> {running ? '正在收敛...' : '立即执行全池收敛'}
            </button>
          )}
        </div>
      </div>

      {overview?.reconcile_stale && (
        <div className="ppipe-alert ppipe-alert--warning">
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            <AlertTriangle size={15} />
            <span>
              全池依赖图已发生漂移，阶段 ④ 发布前需先重跑本阶段：
              {(overview.reconcile_stale_reasons || []).join('；')}
            </span>
          </div>
        </div>
      )}

      {poolProgress && poolProgress.status === 'RUNNING' && (
        <div className="ppipe-progress-banner">
          <span>
            全池收敛中 #{poolProgress.run_id}：{poolProgress.message}（闭包文件{' '}
            {poolProgress.closure_count}，自动修复 {poolProgress.auto_fix_count}）
          </span>
        </div>
      )}

      <div className="ppipe-kpi-grid">
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">同名文件路径冲突</div>
          <div className="ppipe-kpi-value">{stage3?.conflict_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">未人工裁决不等价冲突</div>
          <div className="ppipe-kpi-value">{stage3?.unresolved_conflict_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">导入路径自动修复</div>
          <div className="ppipe-kpi-value">{stage3?.auto_fix_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">根因编译隔离文件</div>
          <div className="ppipe-kpi-value">{stage3?.quarantined_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">公共基座自动收割</div>
          <div className="ppipe-kpi-value">{stage3?.harvested_base_count ?? 0}</div>
        </div>
      </div>

      <div className="ppipe-subtabs" role="tablist" aria-label="阶段 ③ 收敛明细视图">
        <button
          type="button"
          role="tab"
          aria-selected={subTab === 'conflicts'}
          className={`btn btn-sm ${subTab === 'conflicts' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setSubTab('conflicts')}
        >
          同名冲突裁决 ({conflicts.length})
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={subTab === 'auto_fixes'}
          className={`btn btn-sm ${subTab === 'auto_fixes' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setSubTab('auto_fixes')}
        >
          导入路径自愈 ({autoFixes.length})
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={subTab === 'quarantined'}
          className={`btn btn-sm ${subTab === 'quarantined' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setSubTab('quarantined')}
        >
          异常隔离清单 ({quarantined.length})
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={subTab === 'harvested'}
          className={`btn btn-sm ${subTab === 'harvested' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setSubTab('harvested')}
        >
          共享基座收割 ({harvested.length})
        </button>
        {isAdmin && subTab === 'auto_fixes' && autoFixes.length > 0 && (
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            onClick={() => void handlePromoteAllAutoFixes()}
            disabled={promoting || locked}
          >
            {promoting ? '固化中...' : '固化全部自愈规则'}
          </button>
        )}
      </div>

      {subTab === 'conflicts' && (
        <div className="ppipe-table-wrap">
          <table className="ppipe-table" data-testid="stage-reconcile-conflicts-table">
            <thead>
              <tr>
                <th>标准逻辑路径</th>
                <th>胜出仓库（自动兜底 / 人工裁决）</th>
                <th>内容一致性</th>
                <th>候选数</th>
                <th>胜出依据与操作</th>
              </tr>
            </thead>
            <tbody>
              {conflicts.length === 0 ? (
                <tr>
                  <td colSpan={5} style={{ textAlign: 'center', color: 'var(--color-text-secondary)' }}>
                    当前无跨仓同名路径冲突
                  </td>
                </tr>
              ) : (
                conflicts.slice(0, 50).map((c) => {
                  const isExpanded = !collapsedConflicts.has(c.logical_path);
                  return (
                    <React.Fragment key={c.logical_path}>
                      <tr>
                        <td>
                          <code>{c.logical_path}</code>
                        </td>
                        <td>{c.winner_repo_name}</td>
                        <td>{c.content_identical ? '内容等价' : '存在符号/字段差异'}</td>
                        <td>{c.candidates?.length || 0}</td>
                        <td>
                          <div className="ppipe-actions-row">
                            <span>{c.reason}</span>
                            {c.candidates && c.candidates.length > 0 && (
                              <button
                                type="button"
                                className="btn btn-secondary btn-xs"
                                onClick={() => toggleConflictExpand(c.logical_path)}
                              >
                                {isExpanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                                {isExpanded ? '收起候选' : '展开裁决'}
                              </button>
                            )}
                          </div>
                        </td>
                      </tr>
                      {isExpanded && c.candidates && c.candidates.length > 0 && (
                        <tr>
                          <td colSpan={5}>
                            <div className="ppipe-candidate-list">
                              {c.candidates.map((cand) => {
                                const rKey = `${c.logical_path}:${cand.git_repo_id}:${cand.bundle_id}`;
                                const uniqueSyms =
                                  cand.unique_symbols ||
                                  c.symbol_diff?.[cand.repo_name] ||
                                  [];
                                return (
                                  <div
                                    key={`${cand.proto_file_id}-${cand.bundle_id}`}
                                    className="ppipe-candidate-row"
                                  >
                                    <div className="ppipe-candidate-meta">
                                      <div>
                                        <strong>{cand.repo_name}</strong>{' '}
                                        {cand.is_winner && (
                                          <span className="code-badge code-badge-success">当前胜出</span>
                                        )}
                                      </div>
                                      <div>
                                        仓内路径：<code>{cand.repo_file_path}</code> · SHA256：
                                        <code>{cand.content_sha256.slice(0, 8)}</code>
                                      </div>
                                      {uniqueSyms.length > 0 && (
                                        <div>独有符号：{uniqueSyms.join(', ')}</div>
                                      )}
                                    </div>
                                    {isAdmin && (
                                      <button
                                        type="button"
                                        className={`btn btn-xs ${
                                          cand.is_winner ? 'btn-secondary' : 'btn-primary'
                                        }`}
                                        data-testid={`inline-resolve-conflict-${cand.git_repo_id}-${cand.bundle_id}`}
                                        disabled={locked || resolvingKey === rKey}
                                        onClick={() =>
                                          void handleResolveInlineConflict(
                                            c.logical_path,
                                            cand.git_repo_id,
                                            cand.bundle_id
                                          )
                                        }
                                      >
                                        {resolvingKey === rKey ? '裁决中...' : '设为权威提供方'}
                                      </button>
                                    )}
                                  </div>
                                );
                              })}
                            </div>
                          </td>
                        </tr>
                      )}
                    </React.Fragment>
                  );
                })
              )}
            </tbody>
          </table>
        </div>
      )}

      {subTab === 'auto_fixes' && (
        <div className="ppipe-table-wrap">
          <table className="ppipe-table">
            <thead>
              <tr>
                <th>所属仓库</th>
                <th>源文件</th>
                <th>修复类型</th>
                <th>原 Import</th>
                <th>修复后 Import</th>
              </tr>
            </thead>
            <tbody>
              {autoFixes.length === 0 ? (
                <tr>
                  <td colSpan={5} style={{ textAlign: 'center', color: 'var(--color-text-secondary)' }}>
                    无自动修复项
                  </td>
                </tr>
              ) : (
                autoFixes.slice(0, 50).map((af, idx) => (
                  <tr key={`${af.file_path}-${idx}`}>
                    <td>{af.repo_name}</td>
                    <td>
                      <code>{af.file_path}</code>
                    </td>
                    <td>{af.fix_kind}</td>
                    <td>
                      <code>{af.old_import || '-'}</code>
                    </td>
                    <td>
                      <code>{af.new_import}</code>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      )}

      {subTab === 'quarantined' && (
        <div className="ppipe-table-wrap">
          <table className="ppipe-table">
            <thead>
              <tr>
                <th>所属仓库</th>
                <th>逻辑路径</th>
                <th>根因隔离</th>
                <th>错误数</th>
                <th>原因摘要</th>
                <th>源码定位</th>
              </tr>
            </thead>
            <tbody>
              {quarantined.length === 0 ? (
                <tr>
                  <td colSpan={6} style={{ textAlign: 'center', color: 'var(--color-text-secondary)' }}>
                    全池编译健康，无被隔离文件
                  </td>
                </tr>
              ) : (
                quarantined.slice(0, 50).map((q) => (
                  <tr key={`${q.repo_name}-${q.logical_path}`}>
                    <td>{q.repo_name}</td>
                    <td>
                      <code>{q.logical_path}</code>
                    </td>
                    <td>{q.is_root_cause ? '是（根因）' : '受连带影响'}</td>
                    <td>{q.error_count}</td>
                    <td>{q.reason}</td>
                    <td>
                      <button
                        type="button"
                        className="btn btn-secondary btn-xs"
                        onClick={() =>
                          void sourceAnchorDrawer.openByFilePath(q.logical_path || q.repo_file_path, 1)
                        }
                      >
                        <Code2 size={12} /> 定位源码
                      </button>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      )}

      {subTab === 'harvested' && (
        <div className="ppipe-table-wrap" data-testid="stage-reconcile-harvested-table">
          <table className="ppipe-table">
            <thead>
              <tr>
                <th>逻辑导入路径</th>
                <th>基座源绝对路径</th>
                <th>来源优先级</th>
                <th>触发方式</th>
              </tr>
            </thead>
            <tbody>
              {harvested.length === 0 ? (
                <tr>
                  <td colSpan={4} style={{ textAlign: 'center', color: 'var(--color-text-secondary)' }}>
                    本次收敛未触发额外共享基座收割
                  </td>
                </tr>
              ) : (
                harvested.slice(0, 100).map((hb) => (
                  <tr key={`${hb.logical_path}-${hb.source_abs_path}`}>
                    <td>
                      <code>{hb.logical_path}</code>
                    </td>
                    <td>
                      <code>{hb.source_abs_path}</code>
                    </td>
                    <td>P{hb.source_priority}</td>
                    <td>{hb.triggered_by}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      )}

      <ProtoConflictArbitrationDrawer
        open={conflictDrawerOpen}
        isAdmin={isAdmin}
        onClose={() => setConflictDrawerOpen(false)}
        onResolved={() => {
          void loadLatestRun();
          onRefresh();
        }}
      />

      <PoolReconcileReportDrawer
        open={reportDrawerOpen}
        isAdmin={isAdmin}
        run={reconcileResp?.run ?? null}
        report={reconcileResp?.report ?? null}
        onClose={() => setReportDrawerOpen(false)}
        onPromoted={() => {
          void loadLatestRun();
          onRefresh();
        }}
      />

      <ProtoSourceAnchorDrawer drawer={sourceAnchorDrawer} />
    </div>
  );
};
