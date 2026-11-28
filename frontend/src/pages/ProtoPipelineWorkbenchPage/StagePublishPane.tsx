import React, { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import {
  AlertTriangle,
  Archive,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Layers,
  Play,
  ShieldCheck,
  Trash2,
} from 'lucide-react';
import { useConfirm, usePaginationState } from '@code/common';
import * as schemaApi from '../../api/schema';
import { useModulePath } from '../../hooks/useModulePath';
import { useToast } from '../../utils/toast';
import { SchemaAssetLedgerTable } from '../ProtoSchemaWorkbenchPage/SchemaAssetLedgerTable';
import { SchemaAssetInspectDrawer } from '../ProtoSchemaWorkbenchPage/SchemaAssetInspectDrawer';
import { SchemaConflictModal } from '../ProtoSchemaWorkbenchPage/SchemaConflictModal';
import type {
  ArchivedExternalSchemaDTO,
  ConflictOrBrokenResponse,
  PipelineOverviewDTO,
  ProtoPublishRunDTO,
  ProtoSchemaLedgerItemDTO,
  PublishProgressEventPayload,
  PublishRunDetailsDTO,
} from '../../types';

export interface StagePublishPaneProps {
  overview: PipelineOverviewDTO | null;
  isAdmin: boolean;
  locked: boolean;
  publishProgress: PublishProgressEventPayload | null;
  onRefresh: () => void;
  initialPublishRun?: ProtoPublishRunDTO | null;
  initialPublishDetails?: PublishRunDetailsDTO | null;
  initialArchivedSchemas?: ArchivedExternalSchemaDTO[];
  initialSubTab?: 'results' | 'ledger';
  initialLedgerSchemas?: ProtoSchemaLedgerItemDTO[];
}

function renderBundleStatusBadge(status: string) {
  switch (status) {
    case 'PUBLISHED':
      return <span className="code-badge code-badge-success">已发布生效</span>;
    case 'SKIPPED_UNCHANGED':
      return <span className="code-badge code-badge-neutral">无变更跳过</span>;
    case 'BLOCKED_BREAKING':
      return <span className="code-badge code-badge-danger">待审破坏性变更</span>;
    case 'BLOCKED_CONFLICT':
      return <span className="code-badge code-badge-warning">未裁决冲突阻断</span>;
    case 'PENDING_UPSTREAM':
      return <span className="code-badge code-badge-warning">等待上游放行</span>;
    case 'PENDING':
      return <span className="code-badge code-badge-info">待确认生效</span>;
    default:
      return <span className="code-badge code-badge-danger">{status}</span>;
  }
}

export const StagePublishPane: React.FC<StagePublishPaneProps> = ({
  overview,
  isAdmin,
  locked,
  publishProgress,
  onRefresh,
  initialPublishRun = null,
  initialPublishDetails = null,
  initialArchivedSchemas = [],
  initialSubTab = 'results',
  initialLedgerSchemas,
}) => {
  const resolvePath = useModulePath();
  const navigate = useNavigate();
  const toast = useToast();
  const confirmDialog = useConfirm();

  const [subTab, setSubTab] = useState<'results' | 'ledger'>(initialSubTab);
  const [publishRun, setPublishRun] = useState<ProtoPublishRunDTO | null>(initialPublishRun);
  const [publishDetails, setPublishDetails] = useState<PublishRunDetailsDTO | null>(
    initialPublishDetails
  );
  const [archivedSchemas, setArchivedSchemas] = useState<ArchivedExternalSchemaDTO[]>(
    initialArchivedSchemas
  );
  const [archiveOpen, setArchiveOpen] = useState(false);

  const [forceRecompile, setForceRecompile] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const [confirming, setConfirming] = useState(false);

  // 生效契约库版本台账状态
  const {
    page: ledgerPage,
    pageSize: ledgerPageSize,
    setPage: setLedgerPage,
    setPageSize: setLedgerPageSize,
  } = usePaginationState({ defaultPageSize: 25 });
  const [ledgerSchemas, setLedgerSchemas] = useState<ProtoSchemaLedgerItemDTO[]>(
    initialLedgerSchemas ?? []
  );
  const [ledgerTotal, setLedgerTotal] = useState<number>(initialLedgerSchemas?.length ?? 0);
  const [ledgerLoading, setLedgerLoading] = useState(false);
  const [selectedCategory, setSelectedCategory] = useState('');
  const [activeStatusFilter, setActiveStatusFilter] = useState('ALL');
  const [sourceTypeFilter, setSourceTypeFilter] = useState('');
  const [ledgerSearch, setLedgerSearch] = useState('');
  const [inspectDrawerOpen, setInspectDrawerOpen] = useState(false);
  const [inspectSchema, setInspectSchema] = useState<ProtoSchemaLedgerItemDTO | null>(null);
  const [conflictModalOpen, setConflictModalOpen] = useState(false);
  const [conflictData, setConflictData] = useState<ConflictOrBrokenResponse | null>(null);

  const loadPublishState = useCallback(async () => {
    try {
      const [latestRes, archRes] = await Promise.all([
        schemaApi.fetchLatestProtoPublishRun(),
        schemaApi.fetchArchivedExternalSchemas(),
      ]);
      setPublishRun(latestRes.run);
      setPublishDetails(latestRes.details);
      setArchivedSchemas(archRes.items || []);
    } catch {
      // 忽略初次无发布记录情形
    }
  }, []);

  const loadLedgerSchemas = useCallback(async () => {
    setLedgerLoading(true);
    try {
      const res = await schemaApi.listProtoSchemas({
        category: selectedCategory || undefined,
        active_only:
          activeStatusFilter === 'ACTIVE'
            ? true
            : activeStatusFilter === 'INACTIVE'
              ? false
              : undefined,
        source_type: sourceTypeFilter || undefined,
        search: ledgerSearch || undefined,
        latest_only: true,
        page: ledgerPage,
        page_size: ledgerPageSize,
      });
      setLedgerSchemas(res.items ?? []);
      setLedgerTotal(res.total ?? 0);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '加载生效契约版本台账失败');
    } finally {
      setLedgerLoading(false);
    }
  }, [selectedCategory, activeStatusFilter, sourceTypeFilter, ledgerSearch, ledgerPage, ledgerPageSize, toast]);

  useEffect(() => {
    if (initialPublishRun !== null || initialPublishDetails !== null) return;
    void loadPublishState();
  }, [loadPublishState, initialPublishRun, initialPublishDetails, overview?.stage4.latest_run?.id]);

  useEffect(() => {
    if (subTab !== 'ledger' || initialLedgerSchemas !== undefined) return;
    void loadLedgerSchemas();
  }, [subTab, initialLedgerSchemas, loadLedgerSchemas]);

  const handlePublishNow = async () => {
    if (!isAdmin || locked) return;
    setPublishing(true);
    try {
      const res = await schemaApi.publishProtoPipeline({
        force_recompile: forceRecompile,
      });
      setPublishRun(res.run);
      setPublishDetails(res.details);
      if (res.run.status === 'PREVIEW_READY') {
        toast.info('已完成 PREVIEW 模式预编译，请审核下方报告后点击「确认切入生效池」');
      } else if (res.run.status === 'PARTIAL_BLOCKED') {
        toast.warning(
          `部分契约包受阻：已生效 ${res.run.published_count}，冲突阻断 ${res.run.conflict_blocked_count}，等待上游 ${res.run.pending_upstream_count}`
        );
      } else {
        const auditHint =
          res.details?.breaking_audit_count && res.details.breaking_audit_count > 0
            ? `，其中 ${res.details.breaking_audit_count} 个含破坏性变更已归档至「规范与 MR 门禁」审计`
            : '';
        toast.success(
          `发布完成：新生效 ${res.run.published_count} 个契约包，跳过无变更 ${res.run.skipped_count} 个${auditHint}`
        );
      }
      await loadPublishState();
      if (subTab === 'ledger') {
        await loadLedgerSchemas();
      }
      onRefresh();
    } catch (err) {
      toast.error((err as Error).message || '拓扑编译发布失败');
    } finally {
      setPublishing(false);
    }
  };

  const handleConfirmPreview = async () => {
    if (!publishRun || !isAdmin || locked) return;
    const ok = await confirmDialog({
      title: '确认正式发布生效',
      content: `确定将预编译批次 #${publishRun.id} 中的契约描述集原子切入生效池吗？`,
      confirmText: '确认生效',
      cancelText: '取消',
    });
    if (!ok) return;
    setConfirming(true);
    try {
      const res = await schemaApi.confirmPreviewPublishRun(publishRun.id);
      setPublishRun(res.run);
      setPublishDetails(res.details);
      toast.success(`预览发布批次 #${res.run.id} 已正式生效`);
      await loadPublishState();
      onRefresh();
    } catch (err) {
      toast.error((err as Error).message || '确认切入生效池失败');
    } finally {
      setConfirming(false);
    }
  };

  const handleDeactivateArchived = async (item: ArchivedExternalSchemaDTO) => {
    if (!isAdmin) return;
    const ok = await confirmDialog({
      title: '确认停用历史归档资产',
      content: `确定停用归档资产「${item.name} v${item.version}」（${item.source_type}）吗？停用后该版本仍保留于历史记录中。`,
      confirmText: '确认停用',
      cancelText: '取消',
    });
    if (!ok) return;
    try {
      await schemaApi.deactivateProtoSchema(item.schema_id);
      toast.success(`已停用归档资产 ${item.name} v${item.version}`);
      await loadPublishState();
      onRefresh();
    } catch (err) {
      toast.error((err as Error).message || '停用失败');
    }
  };

  const handleDeleteArchived = async (item: ArchivedExternalSchemaDTO) => {
    if (!isAdmin) return;
    const ok = await confirmDialog({
      title: '确认物理删除历史归档资产',
      content: `确定彻底删除未生效的历史归档资产「${item.name} v${item.version}」吗？该操作不可恢复。`,
      confirmText: '确认删除',
      cancelText: '取消',
    });
    if (!ok) return;
    try {
      await schemaApi.deleteProtoSchema(item.schema_id);
      toast.success(`已删除归档资产 ${item.name} v${item.version}`);
      await loadPublishState();
      onRefresh();
    } catch (err) {
      toast.error((err as Error).message || '删除失败');
    }
  };

  const handleActivateLedgerSchema = async (s: ProtoSchemaLedgerItemDTO) => {
    try {
      const res = await schemaApi.activateProtoSchema(s.id);
      const auditHint =
        res.breaking_event_count && res.breaking_event_count > 0
          ? '（含破坏性变更，已归档至规范与 MR 门禁审计）'
          : '';
      toast.success(
        `描述集 [${s.name} v${s.version}] 激活成功，生效 ${res.activated_operations} 个服务契约${auditHint}`
      );
      await loadLedgerSchemas();
      if (inspectDrawerOpen && inspectSchema?.name === s.name) {
        setInspectSchema(res.schema);
      }
      onRefresh();
    } catch (err: unknown) {
      const respData = (err as unknown as { responseData?: ConflictOrBrokenResponse })?.responseData;
      if (respData && (respData.conflicts || respData.broken_dependents)) {
        setConflictData(respData);
        setConflictModalOpen(true);
        return;
      }
      toast.error(err instanceof Error ? err.message : '激活描述集失败');
    }
  };

  const handleDeactivateLedgerSchema = async (s: ProtoSchemaLedgerItemDTO) => {
    try {
      const res = await schemaApi.deactivateProtoSchema(s.id);
      toast.success(`描述集 [${s.name} v${s.version}] 已停用`);
      await loadLedgerSchemas();
      if (inspectDrawerOpen && inspectSchema?.name === s.name) {
        setInspectSchema(res.schema);
      }
      onRefresh();
    } catch (err: unknown) {
      const respData = (err as unknown as { responseData?: ConflictOrBrokenResponse })?.responseData;
      if (respData && (respData.conflicts || respData.broken_dependents)) {
        setConflictData(respData);
        setConflictModalOpen(true);
        return;
      }
      toast.error(err instanceof Error ? err.message : '停用描述集失败');
    }
  };

  const handleBatchActivateLedger = async (selectedIds: (string | number)[]) => {
    try {
      const res = await schemaApi.batchActivateProtoSchemas(selectedIds);
      const auditHint =
        res.breaking_event_count && res.breaking_event_count > 0
          ? `（其中 ${res.breaking_event_count} 个含破坏性变更已归档至规范与 MR 门禁审计）`
          : '';
      toast.success((res.message || `成功批量激活 ${res.activated_count} 个描述集`) + auditHint);
      await loadLedgerSchemas();
      onRefresh();
    } catch (err: unknown) {
      const respData = (err as unknown as { responseData?: ConflictOrBrokenResponse })?.responseData;
      if (respData && (respData.conflicts || respData.broken_dependents)) {
        setConflictData(respData);
        setConflictModalOpen(true);
        return;
      }
      toast.error(err instanceof Error ? err.message : '批量激活失败');
    }
  };

  const handleDeleteLedgerSchema = async (s: ProtoSchemaLedgerItemDTO) => {
    const ok = await confirmDialog({
      title: '确认物理删除描述集',
      content: `确定彻底删除描述集「${s.name} v${s.version}」吗？仅未激活的资产允许删除。`,
      confirmText: '确认删除',
      cancelText: '取消',
    });
    if (!ok) return;
    try {
      await schemaApi.deleteProtoSchema(s.id);
      toast.success(`已删除描述集 ${s.name} v${s.version}`);
      setInspectDrawerOpen(false);
      await loadLedgerSchemas();
      onRefresh();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '删除描述集失败');
    }
  };

  const stage4 = overview?.stage4;
  const breakingItems = publishDetails?.breaking_items || [];
  const conflictRisks = publishDetails?.conflict_risks || [];
  const bundleResults = publishDetails?.bundle_results || [];

  return (
    <div className="ppipe-panel-card" data-testid="stage-publish-pane">
      <div className="ppipe-panel-header">
        <h3>
          <ShieldCheck size={17} /> 阶段 ④：单仓拓扑编译与分级发布
        </h3>
        <div className="ppipe-actions-row">
          <Link to={resolvePath('/proto-docs')} className="btn btn-secondary btn-sm">
            进入契约库消费文档
          </Link>
          <span className="pdoc-pill">
            发布模式：<strong>{overview?.publish_mode || 'AUTO'}</strong>
          </span>
          {isAdmin && (
            <>
              <label className="ppipe-inline-check">
                <input
                  type="checkbox"
                  checked={forceRecompile}
                  onChange={(e) => setForceRecompile(e.target.checked)}
                  disabled={publishing || locked}
                />
                <span>忽略指纹强制重编</span>
              </label>
              <button
                type="button"
                className="btn btn-primary btn-sm"
                data-testid="publish-workspace-btn"
                onClick={() => void handlePublishNow()}
                disabled={publishing || locked}
              >
                <Play size={14} /> {publishing ? '正在编译发布...' : '发布当前已收敛工作区'}
              </button>
            </>
          )}
        </div>
      </div>

      {overview?.reconcile_stale && (
        <div className="ppipe-alert ppipe-alert--danger">
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            <AlertTriangle size={15} />
            <span>
              当前阶段 ③ 收敛结果已过期或尚未就绪，直接发布将被安全拦截 (409 RECONCILE_STALE)。请先执行阶段 ③ 或点击顶部「一键执行流水线」。
            </span>
          </div>
        </div>
      )}

      {publishProgress && publishProgress.status === 'RUNNING' && (
        <div className="ppipe-progress-banner">
          <span>
            拓扑发布 #{publishProgress.run_id}：{publishProgress.processed_bundles}/
            {publishProgress.total_bundles} 包 · {publishProgress.message}
          </span>
        </div>
      )}

      <div className="ppipe-kpi-grid">
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">生效描述集</div>
          <div className="ppipe-kpi-value">{stage4?.active_schema_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">生效服务数</div>
          <div className="ppipe-kpi-value">{stage4?.active_service_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">生效方法数</div>
          <div className="ppipe-kpi-value">{stage4?.active_operation_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">待确认破坏性审计</div>
          <div className="ppipe-kpi-value">
            <Link
              to={resolvePath('/proto-governance?gov_tab=breaking-audit')}
              style={{ color: 'inherit', textDecoration: 'none' }}
              title="前往规范与 MR 门禁查看破坏性变更审计"
            >
              {stage4?.open_breaking_audit_count ?? 0}
            </Link>
          </div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">等待上游传播</div>
          <div className="ppipe-kpi-value">{stage4?.pending_upstream_count ?? 0}</div>
        </div>
      </div>

      {publishRun?.status === 'PREVIEW_READY' && (
        <div className="ppipe-alert ppipe-alert--success" data-testid="preview-confirm-banner">
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            <CheckCircle2 size={16} />
            <span>
              预览发布批次 <strong>#{publishRun.id}</strong> 已全部通过编译与兼容性校验，尚未切入线上生效池。
            </span>
          </div>
          {isAdmin && (
            <button
              type="button"
              className="btn btn-primary btn-sm"
              data-testid="confirm-preview-publish-btn"
              onClick={() => void handleConfirmPreview()}
              disabled={confirming || locked}
            >
              {confirming ? '正在切入...' : '确认切入生效池'}
            </button>
          )}
        </div>
      )}

      {conflictRisks.length > 0 && (
        <div className="ppipe-alert ppipe-alert--warning">
          <div>
            <strong>跨仓内容不等价冲突提示（{conflictRisks.length} 个契约包）：</strong>
            {conflictRisks.map((r) => `${r.bundle_name} (${r.logical_paths.join(', ')})`).join('；')}
          </div>
        </div>
      )}

      {breakingItems.length > 0 && (
        <div className="ppipe-alert ppipe-alert--warning" data-testid="breaking-audit-notice">
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px', flex: 1 }}>
            <AlertTriangle size={16} />
            <span>
              {publishRun?.status === 'PREVIEW_READY'
                ? `预览检测发现 ${breakingItems.length} 个契约包存在破坏性变更，确认切入生效池后将自动归档至「规范与 MR 门禁」进行合规审计。`
                : `本次发布已正常推进，共检出 ${breakingItems.length} 个契约包存在破坏性变更，已自动归档至「规范与 MR 门禁」进行合规审计。`}
            </span>
          </div>
          <Link
            to={resolvePath('/proto-governance?gov_tab=breaking-audit')}
            className="btn btn-secondary btn-sm"
            data-testid="goto-breaking-audit-btn"
          >
            查看破坏性变更审计
          </Link>
        </div>
      )}

      {/* 中部视图切换：本次拓扑发布结果 vs 生效契约库版本台账 */}
      <div className="ppipe-subtabs" role="tablist" aria-label="阶段 ④ 发布与契约库版本视图">
        <button
          type="button"
          role="tab"
          data-testid="stage4-subtab-results"
          aria-selected={subTab === 'results'}
          className={`btn btn-sm ${subTab === 'results' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setSubTab('results')}
        >
          本次拓扑发布结果 ({bundleResults.length})
        </button>
        <button
          type="button"
          role="tab"
          data-testid="stage4-subtab-ledger"
          aria-selected={subTab === 'ledger'}
          className={`btn btn-sm ${subTab === 'ledger' ? 'btn-primary' : 'btn-secondary'}`}
          onClick={() => setSubTab('ledger')}
        >
          <Layers size={14} /> 生效契约库版本台账
        </button>
      </div>

      {subTab === 'results' ? (
        <div className="ppipe-table-wrap">
          <table className="ppipe-table" data-testid="stage4-publish-results-table">
            <thead>
              <tr>
                <th>拓扑层</th>
                <th>契约包</th>
                <th>目标描述集</th>
                <th>类别</th>
                <th>发布结论</th>
                <th>说明 / 下游依赖阻断</th>
              </tr>
            </thead>
            <tbody>
              {bundleResults.length === 0 ? (
                <tr>
                  <td colSpan={6} style={{ textAlign: 'center', color: 'var(--color-text-secondary)' }}>
                    暂无最近一次拓扑发布记录，可点击右上角「发布当前已收敛工作区」生成契约资产
                  </td>
                </tr>
              ) : (
                bundleResults.map((br) => (
                  <tr key={br.bundle_id}>
                    <td>L{br.layer}</td>
                    <td>
                      <strong>{br.bundle_name}</strong>
                      <div style={{ fontSize: '11px', color: 'var(--color-text-secondary)' }}>
                        {br.repo_name}
                      </div>
                    </td>
                    <td>
                      <code>{br.target_schema_name}</code>
                    </td>
                    <td>{br.category}</td>
                    <td>{renderBundleStatusBadge(br.status)}</td>
                    <td>
                      {br.reason || '-'}
                      {br.blocked_by_bundles && br.blocked_by_bundles.length > 0 && (
                        <div style={{ fontSize: '11px', color: 'var(--color-text-secondary)' }}>
                          阻塞上游：{br.blocked_by_bundles.join(', ')}
                        </div>
                      )}
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      ) : (
        <div data-testid="stage4-schema-ledger-panel">
          <SchemaAssetLedgerTable
            schemas={ledgerSchemas}
            total={ledgerTotal}
            page={ledgerPage}
            pageSize={ledgerPageSize}
            onPageChange={setLedgerPage}
            onPageSizeChange={setLedgerPageSize}
            selectedCategory={selectedCategory}
            onCategoryChange={(cat) => {
              setSelectedCategory(cat);
              setLedgerPage(1);
            }}
            activeStatusFilter={activeStatusFilter}
            onStatusFilterChange={(st) => {
              setActiveStatusFilter(st);
              setLedgerPage(1);
            }}
            sourceTypeFilter={sourceTypeFilter}
            onSourceTypeFilterChange={(src) => {
              setSourceTypeFilter(src);
              setLedgerPage(1);
            }}
            searchKeyword={ledgerSearch}
            onSearchChange={(kw) => {
              setLedgerSearch(kw);
              setLedgerPage(1);
            }}
            loading={ledgerLoading}
            isAdmin={isAdmin}
            onInspect={(s) => {
              setInspectSchema(s);
              setInspectDrawerOpen(true);
            }}
            onActivate={(s) => void handleActivateLedgerSchema(s)}
            onDeactivate={(s) => void handleDeactivateLedgerSchema(s)}
            onBatchActivate={(ids) => void handleBatchActivateLedger(ids)}
            onDelete={(s) => void handleDeleteLedgerSchema(s)}
            onDownload={(s) => void schemaApi.downloadProtoSchema(s.id, s.file_name)}
          />
        </div>
      )}

      <div style={{ borderTop: '1px solid var(--color-border-primary)', paddingTop: '12px' }}>
        <button
          type="button"
          className="btn btn-secondary btn-sm"
          data-testid="toggle-archived-migration-btn"
          onClick={() => setArchiveOpen((prev) => !prev)}
        >
          {archiveOpen ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
          <Archive size={14} />
          <span>
            历史归档外部来源迁移清单（{archivedSchemas.length} 项 · 生效中{' '}
            {archivedSchemas.filter((a) => a.is_active).length}）
          </span>
        </button>

        {archiveOpen && (
          <div className="ppipe-table-wrap" style={{ marginTop: '10px' }} data-testid="archived-migration-table">
            <table className="ppipe-table">
              <thead>
                <tr>
                  <th>历史描述集</th>
                  <th>原始来源</th>
                  <th>当前状态</th>
                  <th>Git 覆盖进度</th>
                  <th>未覆盖服务</th>
                  <th>只读治理操作</th>
                </tr>
              </thead>
              <tbody>
                {archivedSchemas.length === 0 ? (
                  <tr>
                    <td colSpan={6} style={{ textAlign: 'center', color: 'var(--color-text-secondary)' }}>
                      当前无历史遗留的 UPLOAD_PB / ONLINE_COMPILE 资产
                    </td>
                  </tr>
                ) : (
                  archivedSchemas.map((arch) => (
                    <tr key={arch.schema_id}>
                      <td>
                        <strong>{arch.name}</strong> v{arch.version}
                      </td>
                      <td>
                        <span className="code-badge code-badge-neutral">{arch.source_type}</span>
                      </td>
                      <td>
                        {arch.is_active ? (
                          <span className="code-badge code-badge-warning">生效中（待迁出）</span>
                        ) : (
                          <span className="code-badge code-badge-neutral">已停用</span>
                        )}
                      </td>
                      <td>
                        {arch.covered_services}/{arch.total_services} (
                        {Math.round(arch.coverage_ratio * 100)}%)
                      </td>
                      <td>
                        {arch.uncovered_names && arch.uncovered_names.length > 0
                          ? arch.uncovered_names.join(', ')
                          : '已全部覆盖'}
                      </td>
                      <td>
                        {isAdmin ? (
                          arch.is_active ? (
                            <button
                              type="button"
                              className="btn btn-secondary btn-sm"
                              onClick={() => void handleDeactivateArchived(arch)}
                            >
                              停用
                            </button>
                          ) : (
                            <button
                              type="button"
                              className="btn btn-danger btn-sm"
                              onClick={() => void handleDeleteArchived(arch)}
                            >
                              <Trash2 size={12} /> 删除
                            </button>
                          )
                        ) : (
                          '-'
                        )}
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <SchemaAssetInspectDrawer
        open={inspectDrawerOpen}
        onClose={() => {
          setInspectDrawerOpen(false);
          setInspectSchema(null);
        }}
        schema={inspectSchema}
        isAdmin={isAdmin}
        onActivate={handleActivateLedgerSchema}
        onDeactivate={handleDeactivateLedgerSchema}
        onDelete={handleDeleteLedgerSchema}
        onNavigateToCatalog={(sid) => {
          setInspectDrawerOpen(false);
          navigate(resolvePath(`/proto-docs?docs_view=reference&schema_id=${sid}`));
        }}
      />

      <SchemaConflictModal
        open={conflictModalOpen}
        onClose={() => {
          setConflictModalOpen(false);
          setConflictData(null);
        }}
        conflictData={conflictData}
      />
    </div>
  );
};
