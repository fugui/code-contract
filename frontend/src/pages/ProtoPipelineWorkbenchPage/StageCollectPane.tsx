import React, { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { DownloadCloud, ExternalLink, FolderGit2, RefreshCw, Search } from 'lucide-react';
import { Pagination, usePaginationState } from '@code/common';
import * as schemaApi from '../../api/schema';
import { useModulePath } from '../../hooks/useModulePath';
import { useToast } from '../../utils/toast';
import type {
  GitRepoDTO,
  GitRepoProtoStatus,
  GitReposSyncSummaryDTO,
  PipelineOverviewDTO,
  SyncCachePolicy,
  SyncJobProgressEventPayload,
} from '../../types';

export interface StageCollectPaneProps {
  overview: PipelineOverviewDTO | null;
  isAdmin: boolean;
  locked: boolean;
  syncProgress: SyncJobProgressEventPayload | null;
  onRefresh: () => void;
  initialRepos?: GitRepoDTO[];
  initialTotal?: number;
  initialSummary?: GitReposSyncSummaryDTO;
}

const COLLECT_STATUS_TABS: Array<{ key: '' | GitRepoProtoStatus; label: string }> = [
  { key: '', label: '全部仓库' },
  { key: 'HAS_PROTO', label: '含 Proto' },
  { key: 'NO_PROTO', label: '无 Proto' },
  { key: 'NO_LOCAL_CACHE', label: '本地未缓存' },
  { key: 'SYNC_FAILED', label: '同步失败' },
  { key: 'EXCLUDED', label: '已停用' },
  { key: 'PENDING', label: '待扫描' },
];

function renderCollectStatusBadge(status?: string) {
  switch (status) {
    case 'HAS_PROTO':
      return <span className="code-badge code-badge-success">含 Proto</span>;
    case 'NO_PROTO':
      return <span className="code-badge code-badge-neutral">无 Proto</span>;
    case 'NO_LOCAL_CACHE':
      return <span className="code-badge code-badge-warning">本地未缓存</span>;
    case 'SYNC_FAILED':
      return <span className="code-badge code-badge-danger">同步失败</span>;
    case 'EXCLUDED':
      return <span className="code-badge code-badge-neutral">已停用</span>;
    default:
      return <span className="code-badge code-badge-info">待扫描</span>;
  }
}

export const StageCollectPane: React.FC<StageCollectPaneProps> = ({
  overview,
  isAdmin,
  locked,
  syncProgress,
  onRefresh,
  initialRepos,
  initialTotal,
  initialSummary,
}) => {
  const resolvePath = useModulePath();
  const toast = useToast();
  const [cachePolicy, setCachePolicy] = useState<SyncCachePolicy>('CACHE_ONLY');
  const [submitting, setSubmitting] = useState(false);

  const { page, pageSize, setPage, setPageSize, resetPage } = usePaginationState({
    defaultPageSize: 25,
  });
  const [summary, setSummary] = useState<GitReposSyncSummaryDTO | null>(initialSummary ?? null);
  const [repos, setRepos] = useState<GitRepoDTO[]>(initialRepos ?? []);
  const [total, setTotal] = useState<number>(initialTotal ?? (initialRepos?.length ?? 0));
  const [loadingRepos, setLoadingRepos] = useState<boolean>(initialRepos === undefined);
  const [statusFilter, setStatusFilter] = useState<'' | GitRepoProtoStatus>('');
  const [serviceGroupFilter, setServiceGroupFilter] = useState('');
  const [searchInput, setSearchInput] = useState('');
  const [searchText, setSearchText] = useState('');
  const [operatingRepoId, setOperatingRepoId] = useState<number | null>(null);

  const stage1 = overview?.stage1;

  const loadSummary = useCallback(async () => {
    try {
      const s = await schemaApi.getGitReposSyncSummary(false);
      setSummary(s);
    } catch {
      // 忽略概览辅表加载失败，由主 overview 兜底
    }
  }, []);

  const loadRepos = useCallback(async () => {
    setLoadingRepos(true);
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
      toast.error((err as Error).message || '加载代码仓拉取台账失败');
    } finally {
      setLoadingRepos(false);
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

  const handleTriggerSyncAll = async () => {
    if (!isAdmin || locked) return;
    setSubmitting(true);
    try {
      const job = await schemaApi.syncAllGitRepos({
        scope: 'ALL',
        cache_policy: cachePolicy,
        skip_compile: true,
      });
      toast.success(`已触发全平台代码仓拉取任务 #${job.id}（仅执行阶段 ①~②，跳过单仓盲编）`);
      onRefresh();
      await loadRepos();
    } catch (err) {
      toast.error((err as Error).message || '触发全平台拉取失败');
    } finally {
      setSubmitting(false);
    }
  };

  const handleSingleRepoSync = async (repo: GitRepoDTO) => {
    if (!isAdmin || locked) return;
    setOperatingRepoId(repo.id);
    try {
      await schemaApi.triggerGitRepoSync(repo.id);
      toast.success(`已触发代码仓 [${repo.name}] 拉取同步`);
      await loadRepos();
      onRefresh();
    } catch (err) {
      toast.error((err as Error).message || '单仓拉取同步失败');
    } finally {
      setOperatingRepoId(null);
    }
  };

  const handleToggleRepoEnabled = async (repo: GitRepoDTO) => {
    if (!isAdmin || locked) return;
    const nextEnabled = !(repo.sync_enabled ?? true);
    setOperatingRepoId(repo.id);
    try {
      await schemaApi.updateGitRepoGovernance(repo.id, { sync_enabled: nextEnabled });
      toast.success(nextEnabled ? `已恢复 [${repo.name}] 纳入采集` : `已排除 [${repo.name}]`);
      await loadRepos();
      onRefresh();
    } catch (err) {
      toast.error((err as Error).message || '更新代码仓采集开关失败');
    } finally {
      setOperatingRepoId(null);
    }
  };

  return (
    <div className="ppipe-panel-card" data-testid="stage-collect-pane">
      <div className="ppipe-panel-header">
        <h3>
          <FolderGit2 size={17} /> 阶段 ①：全平台 Git 代码仓拉取
        </h3>
        <div className="ppipe-actions-row">
          {isAdmin && (
            <>
              <select
                className="pgit-input"
                aria-label="同步缓存策略"
                value={cachePolicy}
                onChange={(e) => setCachePolicy(e.target.value as SyncCachePolicy)}
                disabled={submitting || locked}
              >
                <option value="CACHE_ONLY">仅使用本地缓存 (CACHE_ONLY)</option>
                <option value="FETCH_MISSING">缺失缓存时自动拉取 (FETCH_MISSING)</option>
              </select>
              <button
                type="button"
                className="btn btn-primary btn-sm"
                onClick={() => void handleTriggerSyncAll()}
                disabled={submitting || locked}
              >
                <DownloadCloud size={14} /> {submitting ? '正在调度...' : '立即拉取全平台仓库'}
              </button>
            </>
          )}
        </div>
      </div>

      <div className="ppipe-kpi-grid">
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">平台活跃代码仓</div>
          <div className="ppipe-kpi-value">{stage1?.platform_active_total ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">含契约源码仓</div>
          <div className="ppipe-kpi-value">{stage1?.has_proto_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">无匹配目录</div>
          <div className="ppipe-kpi-value">{stage1?.no_proto_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">缺少本地缓存</div>
          <div className="ppipe-kpi-value">{stage1?.no_local_cache_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">最近同步失败</div>
          <div className="ppipe-kpi-value">{stage1?.sync_failed_count ?? 0}</div>
        </div>
      </div>

      {syncProgress && (
        <div className="ppipe-progress-banner">
          <span>
            全量拉取进度 #{syncProgress.job_id}：{syncProgress.processed_repos}/
            {syncProgress.total_repos} 仓（含 Proto {syncProgress.has_proto_count}，失败{' '}
            {syncProgress.failed_count}）· {syncProgress.status}
          </span>
        </div>
      )}

      {!stage1?.shared_write_enabled && (
        <div className="ppipe-alert ppipe-alert--warning">
          <span>当前部署处于只读共享模式（shared_write_enabled=false），拉取仅允许使用本地缓存。</span>
        </div>
      )}

      {/* 内嵌：全平台代码仓拉取台账 */}
      <div className="ppipe-ledger-section" data-testid="stage-collect-ledger">
        <div className="ppipe-ledger-toolbar">
          <div className="ppipe-filter-pills" role="tablist" aria-label="拉取状态筛选">
            {COLLECT_STATUS_TABS.map((tab) => (
              <button
                key={tab.key || 'ALL'}
                type="button"
                className={`code-pill-btn ${statusFilter === tab.key ? 'code-pill-btn--active' : ''}`}
                onClick={() => {
                  setStatusFilter(tab.key);
                  resetPage();
                }}
              >
                {tab.label}
              </button>
            ))}
          </div>

          <form
            className="ppipe-search-form"
            onSubmit={(e) => {
              e.preventDefault();
              setSearchText(searchInput.trim());
              resetPage();
            }}
          >
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
            <input
              type="text"
              className="pgit-input"
              placeholder="搜索代码仓名称 / 路径..."
              value={searchInput}
              onChange={(e) => setSearchInput(e.target.value)}
            />
            <button type="submit" className="btn btn-secondary btn-sm">
              <Search size={13} /> 筛选
            </button>
          </form>
        </div>

        <div className="ppipe-table-wrap">
          <table className="ppipe-table" data-testid="stage-collect-repos-table">
            <thead>
              <tr>
                <th>平台代码仓</th>
                <th>缓存与探测状态</th>
                <th>最近同步 Commit</th>
                <th>拉取操作</th>
              </tr>
            </thead>
            <tbody>
              {loadingRepos ? (
                <tr>
                  <td colSpan={4} style={{ textAlign: 'center', color: 'var(--color-text-secondary)' }}>
                    正在加载代码仓拉取台账...
                  </td>
                </tr>
              ) : repos.length === 0 ? (
                <tr>
                  <td colSpan={4} style={{ textAlign: 'center', color: 'var(--color-text-secondary)' }}>
                    暂无匹配的代码仓记录，可点击右上角「立即拉取全平台仓库」扫描缓存
                  </td>
                </tr>
              ) : (
                repos.map((repo) => {
                  const busy = operatingRepoId === repo.id || locked;
                  return (
                    <tr key={repo.id}>
                      <td>
                        <strong>{repo.name}</strong>
                        <div style={{ fontSize: '12px', color: 'var(--color-text-secondary)', marginTop: '2px' }}>
                          {repo.service_group || repo.subsystem_slug || '未归属子系统'} ·{' '}
                          <code>{repo.resolved_branch || repo.default_branch}</code>
                        </div>
                      </td>
                      <td>
                        <div style={{ display: 'flex', alignItems: 'center', gap: '6px', flexWrap: 'wrap' }}>
                          {renderCollectStatusBadge(repo.proto_status)}
                          {repo.last_sync_status === 'FAILED' && (
                            <span className="code-badge code-badge-danger" title={repo.last_sync_error}>
                              同步异常
                            </span>
                          )}
                        </div>
                      </td>
                      <td>
                        {repo.last_sync_commit ? (
                          <code>{repo.last_sync_commit.slice(0, 7)}</code>
                        ) : (
                          <span>-</span>
                        )}
                      </td>
                      <td>
                        <div className="ppipe-actions-row">
                          {isAdmin && (
                            <>
                              <button
                                type="button"
                                className="btn btn-secondary btn-xs"
                                disabled={busy}
                                onClick={() => void handleSingleRepoSync(repo)}
                              >
                                <RefreshCw size={12} /> 单仓拉取
                              </button>
                              <button
                                type="button"
                                className="btn btn-secondary btn-xs"
                                disabled={busy}
                                onClick={() => void handleToggleRepoEnabled(repo)}
                              >
                                {repo.sync_enabled === false ? '启用采集' : '排除采集'}
                              </button>
                            </>
                          )}
                          {repo.proto_status === 'HAS_PROTO' && (
                            <Link
                              to={resolvePath(`/proto-docs?docs_view=sources&repo_id=${repo.id}`)}
                              className="btn btn-secondary btn-xs"
                            >
                              查看源码 <ExternalLink size={12} />
                            </Link>
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
          <div className="ppipe-pagination-wrap">
            <Pagination
              page={page}
              pageSize={pageSize}
              totalItems={total}
              onPageChange={setPage}
              onPageSizeChange={(ps) => {
                setPageSize(ps);
                setPage(1);
              }}
              pageSizeOptions={[15, 25, 50, 100]}
            />
          </div>
        )}
      </div>
    </div>
  );
};
