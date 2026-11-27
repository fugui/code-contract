import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  AlertTriangle,
  ExternalLink,
  Play,
  Search,
  Settings2,
  Sliders,
} from 'lucide-react';
import { Pagination, usePaginationState } from '@code/common';
import * as schemaApi from '../../api/schema';
import { useModulePath } from '../../hooks/useModulePath';
import { useToast } from '../../utils/toast';
import { GitRepoConfigDrawer } from '../ProtoSchemaWorkbenchPage/GitRepoConfigDrawer';
import { GlobalGovernanceConfigDrawer } from '../ProtoSchemaWorkbenchPage/GlobalGovernanceConfigDrawer';
import type { GitRepoDTO, PipelineOverviewDTO } from '../../types';

export interface StageNormalizePaneProps {
  overview: PipelineOverviewDTO | null;
  isAdmin: boolean;
  locked: boolean;
  onRefresh: () => void;
  initialRepos?: GitRepoDTO[];
  initialTotal?: number;
}

type NormalizeFilterMode = 'all_proto' | 'quarantined' | 'unassigned';

function isUnassignedRepo(repo: GitRepoDTO): boolean {
  return !repo.service_group || repo.subsystem_slug === '_unassigned';
}

export const StageNormalizePane: React.FC<StageNormalizePaneProps> = ({
  overview,
  isAdmin,
  locked,
  onRefresh,
  initialRepos,
  initialTotal,
}) => {
  const resolvePath = useModulePath();
  const toast = useToast();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [running, setRunning] = useState(false);

  const { page, pageSize, setPage, setPageSize, resetPage } = usePaginationState({
    defaultPageSize: 25,
  });
  const [filterMode, setFilterMode] = useState<NormalizeFilterMode>('all_proto');
  const [searchInput, setSearchInput] = useState('');
  const [searchText, setSearchText] = useState('');
  const [repos, setRepos] = useState<GitRepoDTO[]>(initialRepos ?? []);
  const [total, setTotal] = useState<number>(initialTotal ?? (initialRepos?.length ?? 0));
  const [loadingRepos, setLoadingRepos] = useState<boolean>(initialRepos === undefined);
  const [editingRepo, setEditingRepo] = useState<GitRepoDTO | null>(null);
  const [renormalizingRepoId, setRenormalizingRepoId] = useState<number | null>(null);

  const stage2 = overview?.stage2;

  const loadRepos = useCallback(async () => {
    setLoadingRepos(true);
    try {
      const resp = await schemaApi.listGitRepos({
        proto_status: 'HAS_PROTO',
        service_group: filterMode === 'unassigned' ? '_unassigned' : undefined,
        search: searchText || undefined,
        page: filterMode === 'quarantined' ? 1 : page,
        page_size: filterMode === 'quarantined' ? 100 : pageSize,
      });
      setRepos(resp.items ?? []);
      setTotal(resp.total ?? 0);
    } catch (err) {
      toast.error((err as Error).message || '加载清洗与目录映射台账失败');
    } finally {
      setLoadingRepos(false);
    }
  }, [filterMode, searchText, page, pageSize, toast]);

  useEffect(() => {
    if (initialRepos === undefined) {
      void loadRepos();
    }
  }, [initialRepos, loadRepos]);

  // 异常优先置顶：含隔离文件或未归属子系统的仓库排在前面
  const displayedRepos = useMemo(() => {
    const filtered =
      filterMode === 'quarantined'
        ? repos.filter((r) => (r.quarantined_count ?? 0) > 0)
        : repos;
    return [...filtered].sort((a, b) => {
      const qa = (a.quarantined_count ?? 0) > 0 ? 2 : isUnassignedRepo(a) ? 1 : 0;
      const qb = (b.quarantined_count ?? 0) > 0 ? 2 : isUnassignedRepo(b) ? 1 : 0;
      if (qa !== qb) return qb - qa;
      return (b.quarantined_count ?? 0) - (a.quarantined_count ?? 0);
    });
  }, [repos, filterMode]);

  const handleRenormalizeAll = async () => {
    if (!isAdmin || locked) return;
    setRunning(true);
    try {
      const res = await schemaApi.applyAndRenormalizeGovernance({
        force_recompile: false,
      });
      toast.success(
        `免网络重清洗完成（配置修订 rev=${res.revision}）：成功 ${res.succeeded_count}/${res.processed_count} 仓`
      );
      await loadRepos();
      onRefresh();
    } catch (err) {
      toast.error((err as Error).message || '重清洗失败');
    } finally {
      setRunning(false);
    }
  };

  const handleSingleReNormalize = async (repo: GitRepoDTO) => {
    if (!isAdmin || locked) return;
    setRenormalizingRepoId(repo.id);
    try {
      const res = await schemaApi.reNormalizeGitRepo(repo.id, true);
      toast.success(
        `已完成 [${res.repo_name}] 免网络重洗：通过 ${res.normalized_count} / 隔离 ${res.quarantined_count}`
      );
      await loadRepos();
      onRefresh();
    } catch (err) {
      toast.error((err as Error).message || '单仓免网络重洗失败');
    } finally {
      setRenormalizingRepoId(null);
    }
  };

  return (
    <div className="ppipe-panel-card" data-testid="stage-normalize-pane">
      <div className="ppipe-panel-header">
        <h3>
          <Sliders size={17} /> 阶段 ②：清洗规范化与目录映射
        </h3>
        <div className="ppipe-actions-row">
          <span className="pdoc-pill">
            配置版本 rev #{overview?.governance_revision ?? 0}
            {overview?.governance_config_sha256
              ? ` · ${overview.governance_config_sha256.slice(0, 8)}`
              : ''}
          </span>
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            onClick={() => setDrawerOpen(true)}
          >
            <Settings2 size={14} /> 清洗与导出规则配置
          </button>
          {isAdmin && (
            <button
              type="button"
              className="btn btn-primary btn-sm"
              onClick={() => void handleRenormalizeAll()}
              disabled={running || locked}
            >
              <Play size={14} /> {running ? '正在重洗...' : '按当前规则免网络重洗'}
            </button>
          )}
        </div>
      </div>

      {overview?.normalize_stale && (
        <div className="ppipe-alert ppipe-alert--warning">
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            <AlertTriangle size={15} />
            <span>
              检测到清洗配置或源码变化，建议重新清洗：
              {(overview.normalize_stale_reasons || []).join('；')}
            </span>
          </div>
        </div>
      )}

      <div className="ppipe-kpi-grid">
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">入池契约源文件</div>
          <div className="ppipe-kpi-value">{stage2?.total_files ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">已完成清洗规范化</div>
          <div className="ppipe-kpi-value">{stage2?.normalized_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">规范化隔离文件</div>
          <div className="ppipe-kpi-value">{stage2?.quarantined_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">微服务定义源码</div>
          <div className="ppipe-kpi-value">{stage2?.uservice_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">未归属子系统仓</div>
          <div className="ppipe-kpi-value">{stage2?.unassigned_repos ?? 0}</div>
        </div>
      </div>

      {/* 内嵌：仓库清洗与目录映射台账（异常优先置顶） */}
      <div className="ppipe-ledger-section" data-testid="stage-normalize-ledger">
        <div className="ppipe-ledger-toolbar">
          <div className="ppipe-filter-pills" role="tablist" aria-label="清洗台账筛选">
            <button
              type="button"
              className={`code-pill-btn ${filterMode === 'all_proto' ? 'code-pill-btn--active' : ''}`}
              onClick={() => {
                setFilterMode('all_proto');
                resetPage();
              }}
            >
              全部含 Proto
            </button>
            <button
              type="button"
              className={`code-pill-btn ${filterMode === 'quarantined' ? 'code-pill-btn--active' : ''}`}
              onClick={() => {
                setFilterMode('quarantined');
                resetPage();
              }}
            >
              含隔离文件
            </button>
            <button
              type="button"
              className={`code-pill-btn ${filterMode === 'unassigned' ? 'code-pill-btn--active' : ''}`}
              onClick={() => {
                setFilterMode('unassigned');
                resetPage();
              }}
            >
              未归属子系统
            </button>
          </div>

          <form
            className="ppipe-search-form"
            onSubmit={(e) => {
              e.preventDefault();
              setSearchText(searchInput.trim());
              resetPage();
            }}
          >
            <input
              type="text"
              className="pgit-input"
              placeholder="搜索仓库名 / 归属目录..."
              value={searchInput}
              onChange={(e) => setSearchInput(e.target.value)}
            />
            <button type="submit" className="btn btn-secondary btn-sm">
              <Search size={13} /> 筛选
            </button>
          </form>
        </div>

        <div className="ppipe-table-wrap">
          <table className="ppipe-table" data-testid="stage-normalize-repos-table">
            <thead>
              <tr>
                <th>代码仓</th>
                <th>清洗后归属目录</th>
                <th>文件统计（采集 / 通过 / 隔离）</th>
                <th>清洗操作</th>
              </tr>
            </thead>
            <tbody>
              {loadingRepos ? (
                <tr>
                  <td colSpan={4} style={{ textAlign: 'center', color: 'var(--color-text-secondary)' }}>
                    正在加载仓库清洗台账...
                  </td>
                </tr>
              ) : displayedRepos.length === 0 ? (
                <tr>
                  <td colSpan={4} style={{ textAlign: 'center', color: 'var(--color-text-secondary)' }}>
                    当前筛选条件下无匹配仓库
                  </td>
                </tr>
              ) : (
                displayedRepos.map((repo) => {
                  const exportDir =
                    repo.effective_export_dir ||
                    `clean/${repo.export_target_subsystem || repo.subsystem_slug || '_unassigned'}/${
                      repo.export_target_repo_dir || repo.name
                    }`;
                  const quarantined = repo.quarantined_count ?? 0;
                  const busy = renormalizingRepoId === repo.id || locked;
                  return (
                    <tr key={repo.id}>
                      <td>
                        <strong>{repo.name}</strong>
                        <div style={{ fontSize: '12px', color: 'var(--color-text-secondary)', marginTop: '2px' }}>
                          {repo.service_group || '未归属子系统'}
                          {isUnassignedRepo(repo) && (
                            <span className="code-badge code-badge-warning" style={{ marginLeft: '6px' }}>
                              未归属
                            </span>
                          )}
                        </div>
                      </td>
                      <td>
                        <code>{exportDir}</code>
                        {repo.has_export_override && (
                          <span className="code-badge code-badge-info" style={{ marginLeft: '6px' }}>
                            已覆写
                          </span>
                        )}
                      </td>
                      <td>
                        <span>
                          {repo.whitelist_count ?? 0} / {repo.normalized_count ?? 0} /{' '}
                          <strong style={{ color: quarantined > 0 ? 'var(--color-danger)' : undefined }}>
                            {quarantined}
                          </strong>
                        </span>
                      </td>
                      <td>
                        <div className="ppipe-actions-row">
                          <button
                            type="button"
                            className="btn btn-secondary btn-xs"
                            data-testid={`normalize-repo-config-btn-${repo.id}`}
                            onClick={() => setEditingRepo(repo)}
                          >
                            <Sliders size={12} /> 目录与规则
                          </button>
                          {isAdmin && (
                            <button
                              type="button"
                              className="btn btn-secondary btn-xs"
                              disabled={busy}
                              onClick={() => void handleSingleReNormalize(repo)}
                            >
                              {renormalizingRepoId === repo.id ? '重洗中...' : '免网络重洗'}
                            </button>
                          )}
                          <Link
                            to={resolvePath(`/proto-docs?docs_view=sources&repo_id=${repo.id}`)}
                            className="btn btn-secondary btn-xs"
                          >
                            对比 Raw/Clean <ExternalLink size={12} />
                          </Link>
                        </div>
                      </td>
                    </tr>
                  );
                })
              )}
            </tbody>
          </table>
        </div>

        {filterMode !== 'quarantined' && total > 0 && (
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

      <GitRepoConfigDrawer
        open={Boolean(editingRepo)}
        isAdmin={isAdmin}
        repo={editingRepo}
        onClose={() => setEditingRepo(null)}
        onSaved={() => {
          void loadRepos();
          onRefresh();
        }}
      />

      <GlobalGovernanceConfigDrawer
        open={drawerOpen}
        isAdmin={isAdmin}
        onClose={() => setDrawerOpen(false)}
        onApplied={() => {
          void loadRepos();
          onRefresh();
        }}
      />
    </div>
  );
};
