import React, { useEffect, useState } from 'react';
import { AlertTriangle, FileCheck2, Play, ShieldAlert, Wrench } from 'lucide-react';
import * as schemaApi from '../../api/schema';
import { useToast } from '../../utils/toast';
import { PoolReconcileReportDrawer } from '../ProtoSchemaWorkbenchPage/PoolReconcileReportDrawer';
import { ProtoConflictArbitrationDrawer } from '../ProtoSchemaWorkbenchPage/ProtoConflictArbitrationDrawer';
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
}

export const StageReconcilePane: React.FC<StageReconcilePaneProps> = ({
  overview,
  isAdmin,
  locked,
  poolProgress,
  onRefresh,
}) => {
  const toast = useToast();
  const [reconcileResp, setReconcileResp] = useState<PoolReconcileResponse | null>(null);
  const [subTab, setSubTab] = useState<'conflicts' | 'auto_fixes' | 'quarantined'>('conflicts');
  const [running, setRunning] = useState(false);
  const [reportDrawerOpen, setReportDrawerOpen] = useState(false);
  const [conflictDrawerOpen, setConflictDrawerOpen] = useState(false);
  const [promoting, setPromoting] = useState(false);

  const loadLatestRun = async () => {
    try {
      const res = await schemaApi.getLatestPoolReconcileRun();
      setReconcileResp(res);
    } catch {
      // 尚未存在收敛记录时静默跳过
    }
  };

  useEffect(() => {
    void loadLatestRun();
  }, [overview?.stage3.latest_run?.id]);

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
      toast.success(`已将 ${res.promoted_import_rewrites} 条自动修复规则固化至全局配置 rev #${res.governance_revision}`);
      onRefresh();
    } catch (err) {
      toast.error((err as Error).message || '固化规则失败');
    } finally {
      setPromoting(false);
    }
  };

  const stage3 = overview?.stage3;
  const conflicts = reconcileResp?.report?.conflicts || [];
  const autoFixes = reconcileResp?.report?.auto_fixes || [];
  const quarantined = reconcileResp?.report?.quarantined || [];

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
            <ShieldAlert size={14} /> 人工仲裁同名冲突
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

      <div className="ppipe-subtabs">
        <button
          type="button"
          className={`btn btn-sm ${subTab === 'conflicts' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setSubTab('conflicts')}
        >
          同名冲突裁决 ({conflicts.length})
        </button>
        <button
          type="button"
          className={`btn btn-sm ${subTab === 'auto_fixes' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setSubTab('auto_fixes')}
        >
          导入路径自愈 ({autoFixes.length})
        </button>
        <button
          type="button"
          className={`btn btn-sm ${subTab === 'quarantined' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setSubTab('quarantined')}
        >
          异常隔离清单 ({quarantined.length})
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
          <table className="ppipe-table">
            <thead>
              <tr>
                <th>标准逻辑路径</th>
                <th>胜出仓库（自动兜底 / 人工裁决）</th>
                <th>内容一致性</th>
                <th>候选数</th>
                <th>胜出依据</th>
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
                conflicts.slice(0, 50).map((c) => (
                  <tr key={c.logical_path}>
                    <td>
                      <code>{c.logical_path}</code>
                    </td>
                    <td>{c.winner_repo_name}</td>
                    <td>{c.content_identical ? '内容等价' : '存在符号/字段差异'}</td>
                    <td>{c.candidates?.length || 0}</td>
                    <td>{c.reason}</td>
                  </tr>
                ))
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
              </tr>
            </thead>
            <tbody>
              {quarantined.length === 0 ? (
                <tr>
                  <td colSpan={5} style={{ textAlign: 'center', color: 'var(--color-text-secondary)' }}>
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
    </div>
  );
};
