import React, { useCallback, useEffect, useState } from 'react';
import {
  AlertTriangle,
  BarChart3,
  CheckCircle2,
  DownloadCloud,
  FolderGit2,
  Play,
  RefreshCw,
  Search,
  Settings2,
  Sliders,
} from 'lucide-react';
import { Modal, Pagination, useConfirm, usePaginationState } from '@code/common';
import * as schemaApi from '../../api/schema';
import { subscribeServerEvents } from '../../api/sse';
import { useToast } from '../../utils/toast';
import type {
  BaselineReportDTO,
  GitRepoDTO,
  GitRepoProtoStatus,
  GitReposSyncSummaryDTO,
  ProtoSyncJobDTO,
  SyncCachePolicy,
  SyncJobProgressEventPayload,
  SyncJobScope,
} from '../../types';
import { GitRepoConfigDrawer } from './GitRepoConfigDrawer';
import { GlobalGovernanceConfigDrawer } from './GlobalGovernanceConfigDrawer';
import './ProtoDocsAndCodeViewer.css';

export interface ProtoExportGovernancePaneProps {
  isAdmin: boolean;
  onBrowseRepoSources?: (repoId: number) => void;
  initialSummary?: GitReposSyncSummaryDTO;
  initialRepos?: GitRepoDTO[];
  initialTotal?: number;
}

const STATUS_TABS: Array<{ key: '' | GitRepoProtoStatus; label: string }> = [
  { key: '', label: '全部仓库' },
  { key: 'HAS_PROTO', label: '含 Proto (HAS_PROTO)' },
  { key: 'NO_PROTO', label: '无 pg/ (NO_PROTO)' },
  { key: 'NO_LOCAL_CACHE', label: '无缓存 (NO_LOCAL_CACHE)' },
  { key: 'SYNC_FAILED', label: '失败 (SYNC_FAILED)' },
  { key: 'EXCLUDED', label: '已停用 (EXCLUDED)' },
  { key: 'PENDING', label: '待扫描 (PENDING)' },
];

function renderProtoStatusBadge(status?: string) {
  switch (status) {
    case 'HAS_PROTO':
      return <span className="code-badge code-badge-success">HAS_PROTO</span>;
    case 'NO_PROTO':
      return <span className="code-badge code-badge-neutral">NO_PROTO</span>;
    case 'NO_LOCAL_CACHE':
      return <span className="code-badge code-badge-warning">NO_LOCAL_CACHE</span>;
    case 'SYNC_FAILED':
      return <span className="code-badge code-badge-danger">SYNC_FAILED</span>;
    case 'EXCLUDED':
      return <span className="code-badge code-badge-neutral">EXCLUDED</span>;
    default:
      return <span className="code-badge code-badge-info">{status || 'PENDING'}</span>;
  }
}

export const ProtoExportGovernancePane: React.FC<ProtoExportGovernancePaneProps> = ({
  isAdmin,
  onBrowseRepoSources,
  initialSummary,
  initialRepos,
  initialTotal,
}) => {
  const toast = useToast();
  const confirmDialog = useConfirm();
  const { page, pageSize, setPage, setPageSize, resetPage } = usePaginationState({
    defaultPageSize: 25,
  });

  const [summary, setSummary] = useState<GitReposSyncSummaryDTO | null>(initialSummary ?? null);
  const [repos, setRepos] = useState<GitRepoDTO[]>(initialRepos ?? []);
  const [total, setTotal] = useState<number>(initialTotal ?? (initialRepos?.length ?? 0));
  const [loading, setLoading] = useState<boolean>(initialRepos === undefined);

  const [statusFilter, setStatusFilter] = useState<'' | GitRepoProtoStatus>('');
  const [serviceGroupFilter, setServiceGroupFilter] = useState('');
  const [searchInput, setSearchInput] = useState('');
  const [searchText, setSearchText] = useState('');

  const [syncScope, setSyncScope] = useState<SyncJobScope>('ALL');
  const [activeJob, setActiveJob] = useState<ProtoSyncJobDTO | null>(
    initialSummary?.latest_job ?? null
  );
  const [jobStarting, setJobStarting] = useState(false);
  const [errorsExpanded, setErrorsExpanded] = useState(false);

  const [editingRepo, setEditingRepo] = useState<GitRepoDTO | null>(null);
  const [globalDrawerOpen, setGlobalDrawerOpen] = useState(false);
  const [baselineOpen, setBaselineOpen] = useState(false);
  const [baselineReport, setBaselineReport] = useState<BaselineReportDTO | null>(null);
  const [baselineLoading, setBaselineLoading] = useState(false);

  const loadSummary = useCallback(async () => {
    try {
      const s = await schemaApi.getGitReposSyncSummary(true);
      setSummary(s);
      if (s.latest_job) {
        setActiveJob((prev) => {
          if (!prev || s.latest_job!.id >= prev.id) {
            return s.latest_job ?? null;
          }
          return prev;
        });
      }
    } catch (err) {
      toast.error((err as Error).message || '加载平台仓库同步概览失败');
    }
  }, [toast]);

  const loadRepos = useCallback(async () => {
    setLoading(true);
    try {
      const resp = await schemaApi.listGitRepos({
        proto_status: statusFilter || undefined,
        service_group: serviceGroupFilter || undefined,
        search: searchText || undefined,
        page,
        page_size: pageSize,
      });
      setRepos(resp.items ?? []);
      setTotal(resp.total ?? 0);
    } catch (err) {
      toast.error((err as Error).message || '加载仓库治理台账失败');
    } finally {
      setLoading(false);
    }
  }, [statusFilter, serviceGroupFilter, searchText, page, pageSize, toast]);

  useEffect(() => {
    if (initialSummary === undefined) {
      void loadSummary();
    }
  }, [initialSummary, loadSummary]);

  useEffect(() => {
    if (initialRepos === undefined) {
      void loadRepos();
    }
  }, [initialRepos, loadRepos]);

  // 订阅 SSE proto_sync_job.progress
  useEffect(() => {
    if (typeof window === 'undefined' || typeof fetch === 'undefined') {
      return;
    }
    const unsubscribe = subscribeServerEvents({
      onMessage: (evt) => {
        if (evt.event !== 'proto_sync_job.progress') {
          return;
        }
        const payload = evt.data as SyncJobProgressEventPayload | undefined;
        if (!payload?.job_id) return;
        setActiveJob({
          id: payload.job_id,
          scope: payload.scope,
          cache_policy: payload.cache_policy,
          status: payload.status,
          total_repos: payload.total_repos,
          processed_repos: payload.processed_repos,
          has_proto_count: payload.has_proto_count,
          no_proto_count: payload.no_proto_count,
          no_cache_count: payload.no_cache_count,
          failed_count: payload.failed_count,
          skipped_unchanged_count: payload.skipped_unchanged_count,
          error_summary: payload.error_summary,
          triggered_by: 'sse',
          started_at: payload.started_at,
          finished_at: payload.finished_at,
        });
        if (payload.status !== 'RUNNING') {
          void loadSummary();
          void loadRepos();
        }
      },
    });
    return unsubscribe;
  }, [loadSummary, loadRepos]);

  // 运行态任务兜底轮询
  useEffect(() => {
    if (!activeJob || activeJob.status !== 'RUNNING') {
      return;
    }
    const timer = window.setInterval(() => {
      void schemaApi
        .getProtoSyncJob(activeJob.id)
        .then((job) => {
          setActiveJob(job);
          if (job.status !== 'RUNNING') {
            void loadSummary();
            void loadRepos();
          }
        })
        .catch(() => {
          // 静默忽略单次轮询失败
        });
    }, 2000);
    return () => window.clearInterval(timer);
  }, [activeJob, loadSummary, loadRepos]);

  const handleStartSyncAll = async (policy: SyncCachePolicy) => {
    if (!isAdmin) return;
    if (policy === 'FETCH_MISSING') {
      if (!summary?.shared_write_enabled) {
        toast.warning('服务端未开启 storage.shared_write_enabled，禁止执行缺失缓存网络拉取');
        return;
      }
      const ok = await confirmDialog({
        title: '确认执行「缺失时自动拉取」',
        content:
          '将对尚未建立本地 Git 缓存的平台仓库尝试发起浅克隆/拉取（无权限或已废弃仓可能耗时或失败）。确认继续？',
        confirmText: '确认拉取并同步',
        cancelText: '取消',
        type: 'warning',
      });
      if (!ok) return;
    }

    setJobStarting(true);
    try {
      const effectiveScope: SyncJobScope =
        syncScope === 'SERVICE_GROUP' && !serviceGroupFilter ? 'ALL' : syncScope;
      const job = await schemaApi.syncAllGitRepos({
        scope: effectiveScope,
        cache_policy: policy,
        service_group: effectiveScope === 'SERVICE_GROUP' ? serviceGroupFilter : undefined,
      });
      setActiveJob(job);
      toast.info(
        `已启动批处理同步作业 #${job.id} (${policy === 'CACHE_ONLY' ? '仅已有缓存' : '补拉缺失缓存'}，共 ${job.total_repos} 仓)`
      );
      if (job.status !== 'RUNNING') {
        await loadSummary();
        await loadRepos();
      }
    } catch (err) {
      toast.error((err as Error).message || '触发全量同步失败');
    } finally {
      setJobStarting(false);
    }
  };

  const handleToggleRepoEnabled = async (repo: GitRepoDTO) => {
    if (!isAdmin) return;
    const nextEnabled = !(repo.sync_enabled ?? true);
    try {
      await schemaApi.updateGitRepoGovernance(repo.id, { sync_enabled: nextEnabled });
      toast.success(nextEnabled ? `已恢复 [${repo.name}] 采集` : `已停用 [${repo.name}] 并将移除 Clean 软链`);
      await loadSummary();
      await loadRepos();
    } catch (err) {
      toast.error((err as Error).message || '更新仓库启用状态失败');
    }
  };

  const handleSingleReNormalize = async (repo: GitRepoDTO) => {
    if (!isAdmin) return;
    try {
      const res = await schemaApi.reNormalizeGitRepo(repo.id, true);
      toast.success(
        `已完成 [${res.repo_name}] 免网络重出树：通过 ${res.normalized_count} / 隔离 ${res.quarantined_count}`
      );
      await loadRepos();
    } catch (err) {
      toast.error((err as Error).message || '免网络重出树失败');
    }
  };

  const handleOpenBaselineReport = async () => {
    setBaselineOpen(true);
    setBaselineLoading(true);
    try {
      const report = await schemaApi.getGitReposBaselineReport();
      setBaselineReport(report);
    } catch (err) {
      toast.error((err as Error).message || '获取基线试编译报告失败');
    } finally {
      setBaselineLoading(false);
    }
  };

  const progressPct =
    activeJob && activeJob.total_repos > 0
      ? Math.min(100, Math.round((activeJob.processed_repos / activeJob.total_repos) * 100))
      : 0;

  return (
    <div className="pgit-governance-pane" data-testid="proto-export-governance-pane">
      {/* 1. 顶部 500+ 平台仓 6 态概览卡片 */}
      <div className="pgit-summary-grid">
        <div className="pgit-stat-card">
          <span className="pgit-stat-card__label">平台活跃仓总数</span>
          <strong className="pgit-stat-card__val">{summary?.platform_active_total ?? total}</strong>
        </div>
        <div className="pgit-stat-card">
          <span className="pgit-stat-card__label">含 Proto (HAS_PROTO)</span>
          <strong className="pgit-stat-card__val is-success">{summary?.has_proto_count ?? 0}</strong>
        </div>
        <div className="pgit-stat-card">
          <span className="pgit-stat-card__label">无 pg/ (NO_PROTO)</span>
          <strong className="pgit-stat-card__val">{summary?.no_proto_count ?? 0}</strong>
        </div>
        <div className="pgit-stat-card">
          <span className="pgit-stat-card__label">无本地缓存 (NO_LOCAL_CACHE)</span>
          <strong className="pgit-stat-card__val is-warning">{summary?.no_local_cache_count ?? 0}</strong>
        </div>
        <div className="pgit-stat-card">
          <span className="pgit-stat-card__label">同步失败 (SYNC_FAILED)</span>
          <strong className="pgit-stat-card__val is-danger">{summary?.sync_failed_count ?? 0}</strong>
        </div>
        <div className="pgit-stat-card">
          <span className="pgit-stat-card__label">已停用 (EXCLUDED)</span>
          <strong className="pgit-stat-card__val">{summary?.excluded_count ?? 0}</strong>
        </div>
      </div>

      {/* 2. 双按钮全量/局域同步控制台 & 全局治理动作栏 */}
      <div className="pdoc-card">
        <div className="pgit-toolbar-row">
          <div className="pgit-toolbar-group">
            <select
              className="pgit-input"
              aria-label="同步范围"
              value={syncScope}
              onChange={(e) => setSyncScope(e.target.value as SyncJobScope)}
            >
              <option value="ALL">全量平台活跃仓 (ALL)</option>
              <option value="SERVICE_GROUP">按所选子系统分组</option>
              <option value="FAILED_ONLY">仅重试失败仓 (FAILED_ONLY)</option>
            </select>

            <button
              type="button"
              className="btn-primary"
              data-testid="sync-all-cache-only-btn"
              disabled={!isAdmin || jobStarting}
              onClick={() => void handleStartSyncAll('CACHE_ONLY')}
            >
              <Play size={14} /> 仅同步已有缓存
            </button>

            <button
              type="button"
              className="btn-secondary"
              data-testid="sync-all-fetch-missing-btn"
              disabled={!isAdmin || jobStarting || summary?.shared_write_enabled === false}
              title={
                summary?.shared_write_enabled === false
                  ? '服务端未开启 storage.shared_write_enabled，禁止网络拉取'
                  : '缺失本地缓存时自动发起浅克隆拉取'
              }
              onClick={() => void handleStartSyncAll('FETCH_MISSING')}
            >
              <DownloadCloud size={14} /> 缺失时自动拉取
            </button>
          </div>

          <div className="pgit-toolbar-actions">
            <button
              type="button"
              className="btn-secondary"
              data-testid="open-global-governance-btn"
              onClick={() => setGlobalDrawerOpen(true)}
            >
              <Settings2 size={14} /> 全局清理规则与 JSON
            </button>
            <button
              type="button"
              className="btn-secondary"
              data-testid="open-baseline-report-btn"
              onClick={() => void handleOpenBaselineReport()}
            >
              <BarChart3 size={14} /> 基线试编译报告
            </button>
          </div>
        </div>

        {/* 作业进度条与异常明细 */}
        {activeJob && (
          <div className="pgit-job-banner" data-testid="sync-job-progress-banner">
            <div className="pgit-toolbar-row">
              <div className="pgit-toolbar-group">
                {activeJob.status === 'RUNNING' ? (
                  <RefreshCw size={14} />
                ) : activeJob.failed_count > 0 ? (
                  <AlertTriangle size={14} />
                ) : (
                  <CheckCircle2 size={14} />
                )}
                <span>
                  同步任务 #{activeJob.id} ({activeJob.cache_policy}) · <strong>{activeJob.status}</strong> ·{' '}
                  {activeJob.processed_repos}/{activeJob.total_repos} ({progressPct}%)
                </span>
                <span className="pdoc-meta-row">
                  <span>HAS_PROTO: {activeJob.has_proto_count}</span>
                  <span>NO_PROTO: {activeJob.no_proto_count}</span>
                  <span>NO_CACHE: {activeJob.no_cache_count}</span>
                  <span>FAILED: {activeJob.failed_count}</span>
                  <span>SKIPPED: {activeJob.skipped_unchanged_count}</span>
                </span>
              </div>
              {activeJob.error_summary && activeJob.error_summary.length > 0 && (
                <button
                  type="button"
                  className="btn-secondary btn-xs"
                  onClick={() => setErrorsExpanded((v) => !v)}
                >
                  {errorsExpanded
                    ? '收起异常详情'
                    : `查看 ${activeJob.error_summary.length} 条失败明细`}
                </button>
              )}
            </div>
            <div className="pgit-progress-track">
              <div className="pgit-progress-fill" style={{ width: `${progressPct}%` }} />
            </div>
            {errorsExpanded && activeJob.error_summary && (
              <div className="pgit-error-list">
                {activeJob.error_summary.map((item) => (
                  <div key={item.git_repo_id} className="pgit-error-banner">
                    <strong>{item.repo_name}</strong>: {item.error}
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>

      {/* 3. 500+ 仓标准分页台账 */}
      <div className="pdoc-card">
        <div className="pgit-toolbar-row" style={{ marginBottom: 12 }}>
          <div className="pgit-status-pills" role="tablist" aria-label="仓库状态筛选">
            {STATUS_TABS.map((tab) => (
              <button
                key={tab.key || 'ALL'}
                type="button"
                className={`pgit-status-pill ${statusFilter === tab.key ? 'is-active' : ''}`}
                onClick={() => {
                  setStatusFilter(tab.key);
                  resetPage();
                }}
              >
                {tab.label}
              </button>
            ))}
          </div>

          <div className="pgit-toolbar-group">
            <select
              className="pgit-input"
              aria-label="子系统筛选"
              value={serviceGroupFilter}
              onChange={(e) => {
                setServiceGroupFilter(e.target.value);
                resetPage();
              }}
            >
              <option value="">全部子系统分组</option>
              {(summary?.service_groups || []).map((sg) => (
                <option key={sg} value={sg}>
                  {sg}
                </option>
              ))}
            </select>

            <form
              className="pgit-search-inline"
              onSubmit={(e) => {
                e.preventDefault();
                setSearchText(searchInput.trim());
                resetPage();
              }}
            >
              <Search size={14} />
              <input
                type="text"
                className="pgit-input"
                placeholder="搜索仓库名 / 目标导出目录..."
                value={searchInput}
                onChange={(e) => setSearchInput(e.target.value)}
              />
            </form>
          </div>
        </div>

        <div className="pgit-table-wrap">
          <table className="code-table" data-testid="governance-repos-table">
            <thead>
              <tr>
                <th>平台代码仓（只读）</th>
                <th>契约状态</th>
                <th>Clean 目标导出目录（方案 A）</th>
                <th>文件统计（白名单 / 已清洗 / 隔离）</th>
                <th>最近同步</th>
                <th>治理操作</th>
              </tr>
            </thead>
            <tbody>
              {loading ? (
                <tr>
                  <td colSpan={6} className="ucat-empty-cell">
                    正在加载平台代码仓台账...
                  </td>
                </tr>
              ) : repos.length === 0 ? (
                <tr>
                  <td colSpan={6} className="ucat-empty-cell">
                    暂无匹配的平台代码仓记录（点击上方「仅同步已有缓存」自动对齐 code-bench）
                  </td>
                </tr>
              ) : (
                repos.map((repo) => {
                  const exportDir =
                    repo.effective_export_dir ||
                    `clean/${repo.export_target_subsystem || repo.subsystem_slug || '_unassigned'}/${
                      repo.export_target_repo_dir || repo.name
                    }`;
                  return (
                    <tr key={repo.id}>
                      <td>
                        <div className="pgit-repo-cell">
                          <strong>
                            <FolderGit2 size={13} /> {repo.name}
                          </strong>
                          <div className="pdoc-meta-row">
                            {repo.service_group && (
                              <span className="code-badge code-badge-neutral">
                                {repo.service_group}
                              </span>
                            )}
                            {repo.department_name && <span>{repo.department_name}</span>}
                            <code>{repo.default_branch}</code>
                          </div>
                        </div>
                      </td>
                      <td>
                        <div className="pdoc-meta-row">
                          {renderProtoStatusBadge(repo.proto_status)}
                          {repo.sync_strategy === 'LOCAL_CACHE_ONLY' && (
                            <span className="code-badge code-badge-neutral">CACHE_ONLY</span>
                          )}
                        </div>
                      </td>
                      <td>
                        <div className="pdoc-meta-row">
                          <code>{exportDir}</code>
                          {repo.has_export_override && (
                            <span className="code-badge code-badge-info">已覆写</span>
                          )}
                        </div>
                      </td>
                      <td>
                        <span>
                          {repo.whitelist_count ?? 0} / {repo.normalized_count ?? 0} /{' '}
                          <span className={(repo.quarantined_count ?? 0) > 0 ? 'is-danger' : ''}>
                            {repo.quarantined_count ?? 0}
                          </span>
                        </span>
                      </td>
                      <td>
                        {repo.last_sync_commit ? (
                          <code>{repo.last_sync_commit.slice(0, 7)}</code>
                        ) : (
                          <span>-</span>
                        )}
                      </td>
                      <td>
                        <div className="pgit-toolbar-actions">
                          {onBrowseRepoSources && repo.proto_status === 'HAS_PROTO' && (
                            <button
                              type="button"
                              className="btn-secondary btn-xs"
                              onClick={() => onBrowseRepoSources(repo.id)}
                            >
                              浏览源码
                            </button>
                          )}
                          <button
                            type="button"
                            className="btn-secondary btn-xs"
                            data-testid={`repo-governance-btn-${repo.id}`}
                            onClick={() => setEditingRepo(repo)}
                          >
                            <Sliders size={12} /> 治理
                          </button>
                          {isAdmin && (
                            <>
                              <button
                                type="button"
                                className="btn-secondary btn-xs"
                                onClick={() => void handleSingleReNormalize(repo)}
                              >
                                重洗
                              </button>
                              <button
                                type="button"
                                className="btn-secondary btn-xs"
                                onClick={() => void handleToggleRepoEnabled(repo)}
                              >
                                {repo.sync_enabled === false ? '启用' : '停用'}
                              </button>
                            </>
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })
              )}
            </tbody>
          </table>
        </div>

        {total > 0 && (
          <div className="ucat-pagination-wrap">
            <Pagination
              page={page}
              pageSize={pageSize}
              totalItems={total}
              onPageChange={setPage}
              onPageSizeChange={(ps: number) => {
                setPageSize(ps);
                setPage(1);
              }}
              pageSizeOptions={[15, 25, 50, 100]}
            />
          </div>
        )}
      </div>

      {/* 单仓导出与清洗治理抽屉 */}
      <GitRepoConfigDrawer
        open={Boolean(editingRepo)}
        isAdmin={isAdmin}
        repo={editingRepo}
        onClose={() => setEditingRepo(null)}
        onSaved={() => {
          void loadSummary();
          void loadRepos();
        }}
      />

      {/* 全局治理配置 .json 抽屉 */}
      <GlobalGovernanceConfigDrawer
        open={globalDrawerOpen}
        isAdmin={isAdmin}
        onClose={() => setGlobalDrawerOpen(false)}
        onApplied={() => {
          void loadSummary();
          void loadRepos();
        }}
      />

      {/* 基线试编译报告弹窗 */}
      <Modal
        open={baselineOpen}
        onClose={() => setBaselineOpen(false)}
        title="全平台 Proto 基线采集与试编译报告"
        width={760}
      >
        {baselineLoading || !baselineReport ? (
          <div className="ucat-empty-cell">正在统计全量基线指标...</div>
        ) : (
          <div className="pgit-drawer-stack">
            <div className="pgit-summary-grid">
              <div className="pgit-stat-card">
                <span className="pgit-stat-card__label">白名单文件</span>
                <strong className="pgit-stat-card__val">{baselineReport.whitelist_files}</strong>
              </div>
              <div className="pgit-stat-card">
                <span className="pgit-stat-card__label">通过 Stage B 语法</span>
                <strong className="pgit-stat-card__val is-success">
                  {baselineReport.normalized_files}
                </strong>
              </div>
              <div className="pgit-stat-card">
                <span className="pgit-stat-card__label">隔离文件</span>
                <strong className="pgit-stat-card__val is-danger">
                  {baselineReport.quarantined_files}
                </strong>
              </div>
              <div className="pgit-stat-card">
                <span className="pgit-stat-card__label">语法通过率</span>
                <strong className="pgit-stat-card__val">
                  {(baselineReport.syntax_pass_rate * 100).toFixed(1)}%
                </strong>
              </div>
              <div className="pgit-stat-card">
                <span className="pgit-stat-card__label">试编译通过率</span>
                <strong className="pgit-stat-card__val">
                  {(baselineReport.compile_pass_rate * 100).toFixed(1)}%
                </strong>
              </div>
            </div>
            {Object.keys(baselineReport.error_distribution || {}).length > 0 && (
              <div className="pdoc-card">
                <h4 className="pdoc-card__title">高频错误原因分布</h4>
                <div className="pgit-dryrun-list">
                  {Object.entries(baselineReport.error_distribution).map(([reason, cnt]) => (
                    <div key={reason} className="pgit-dryrun-repo-row">
                      <code>{reason}</code>
                      <strong>{cnt}</strong>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </div>
        )}
      </Modal>
    </div>
  );
};

export default ProtoExportGovernancePane;
