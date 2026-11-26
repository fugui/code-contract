import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { CheckSquare, Download, FolderGit2, RefreshCw, Search } from 'lucide-react';
import { Drawer } from '@code/common';
import * as schemaApi from '../../api/schema';
import { useToast } from '../../utils/toast';
import type {
  BaselineReportDTO,
  GitRepoDTO,
  GitSyncStrategy,
  PlatformRepoImportItemInput,
  PlatformRepoPreviewDTO,
} from '../../types';
import './ProtoDocsAndCodeViewer.css';

export interface PlatformRepoImportDrawerProps {
  open: boolean;
  isAdmin: boolean;
  initialRepos?: PlatformRepoPreviewDTO[];
  initialBaseline?: BaselineReportDTO | null;
  onClose: () => void;
  onImported?: (repos: GitRepoDTO[]) => void;
}

export const PlatformRepoImportDrawer: React.FC<PlatformRepoImportDrawerProps> = ({
  open,
  isAdmin,
  initialRepos,
  initialBaseline = null,
  onClose,
  onImported,
}) => {
  const toast = useToast();
  const [search, setSearch] = useState('');
  const [serviceGroup, setServiceGroup] = useState('');
  const [loading, setLoading] = useState(false);
  const [importing, setImporting] = useState(false);
  const [repos, setRepos] = useState<PlatformRepoPreviewDTO[]>(initialRepos ?? []);
  const [selectedIds, setSelectedIds] = useState<number[]>([]);
  const [strategyMap, setStrategyMap] = useState<Record<number, GitSyncStrategy>>({});
  const [baseline, setBaseline] = useState<BaselineReportDTO | null>(initialBaseline);
  const [baselineLoading, setBaselineLoading] = useState(false);

  const loadDiscover = useCallback(async () => {
    setLoading(true);
    try {
      const resp = await schemaApi.discoverPlatformRepos({
        search: search.trim() || undefined,
        service_group: serviceGroup.trim() || undefined,
        probe_protos: true,
        page: 1,
        page_size: 50,
      });
      setRepos(resp.items ?? []);
    } catch (err) {
      toast.error((err as Error).message || '发现平台代码仓失败');
    } finally {
      setLoading(false);
    }
  }, [search, serviceGroup, toast]);

  useEffect(() => {
    if (!open || initialRepos !== undefined) return;
    void loadDiscover();
  }, [open, initialRepos, loadDiscover]);

  const loadBaseline = async () => {
    setBaselineLoading(true);
    try {
      const rep = await schemaApi.getGitReposBaselineReport();
      setBaseline(rep);
    } catch (err) {
      toast.error((err as Error).message || '加载基线报告失败');
    } finally {
      setBaselineLoading(false);
    }
  };

  const selectableIds = useMemo(
    () => repos.filter((r) => !r.already_imported).map((r) => r.platform_repo_id),
    [repos]
  );

  const toggleSelect = (id: number) => {
    setSelectedIds((prev) =>
      prev.includes(id) ? prev.filter((item) => item !== id) : [...prev, id]
    );
  };

  const handleBatchImport = async () => {
    if (!isAdmin || selectedIds.length === 0) return;
    setImporting(true);
    try {
      const byId = new Map<number, PlatformRepoPreviewDTO>();
      for (const r of repos) {
        byId.set(r.platform_repo_id, r);
      }
      const items: PlatformRepoImportItemInput[] = selectedIds.map((id) => {
        const preview = byId.get(id);
        return {
          platform_repo_id: id,
          sync_strategy: strategyMap[id] ?? 'FETCH_AND_READ',
          bundles: preview?.candidate_bundles?.map((cb) => ({
            bundle_name: cb.bundle_name,
            target_schema_name: cb.target_schema_name,
            category: cb.category,
            subdir_path: cb.subdir_path,
          })),
        };
      });
      const res = await schemaApi.importPlatformRepos(items);
      toast.success(`成功导入 ${res.imported_count} 个平台共享代码仓（默认仅试编译、不自动激活）`);
      setSelectedIds([]);
      if (onImported) {
        onImported(res.items ?? []);
      }
      if (initialRepos === undefined) {
        await loadDiscover();
      }
    } catch (err) {
      toast.error((err as Error).message || '批量导入平台代码仓失败');
    } finally {
      setImporting(false);
    }
  };

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title="从平台代码仓批量发现与导入 (PLATFORM_SHARED)"
      width={920}
    >
      <div className="pgit-drawer-stack" data-testid="platform-repo-import-drawer">
        <div className="pdoc-card pgit-toolbar-row">
          <div className="pgit-toolbar-group">
            <div className="pgit-search-box">
              <Search size={14} />
              <input
                type="text"
                className="pdoc-tree-search"
                placeholder="检索平台仓库名称或 URL..."
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
            </div>
            <input
              type="text"
              className="pgit-input"
              placeholder="子系统 (service_group)"
              value={serviceGroup}
              onChange={(e) => setServiceGroup(e.target.value)}
            />
            <button
              type="button"
              className="btn btn-secondary"
              disabled={loading}
              onClick={() => void loadDiscover()}
            >
              <RefreshCw size={14} className={loading ? 'pgit-spin' : undefined} /> 发现与探测 pg/
            </button>
          </div>

          <div className="pdoc-meta-row">
            <button
              type="button"
              className="btn btn-secondary"
              disabled={baselineLoading}
              onClick={() => void loadBaseline()}
            >
              <FolderGit2 size={14} /> {baselineLoading ? '统计中...' : '基线试编译报告'}
            </button>
            {isAdmin && (
              <button
                type="button"
                className="btn btn-primary"
                disabled={importing || selectedIds.length === 0}
                onClick={() => void handleBatchImport()}
              >
                <Download size={14} />{' '}
                {importing ? '导入中...' : `批量导入 (${selectedIds.length})`}
              </button>
            )}
          </div>
        </div>

        {baseline && (
          <div className="pdoc-card" data-testid="baseline-report-panel">
            <div className="plint-metrics">
              <div className="plint-metric">
                <div className="plint-metric__label">已纳管仓</div>
                <div className="plint-metric__val">{baseline.total_repos}</div>
              </div>
              <div className="plint-metric">
                <div className="plint-metric__label">pg 白名单文件</div>
                <div className="plint-metric__val">{baseline.whitelist_files}</div>
              </div>
              <div className="plint-metric">
                <div className="plint-metric__label">NORMALIZED 文件</div>
                <div className="plint-metric__val">{baseline.normalized_files}</div>
              </div>
              <div className="plint-metric">
                <div className="plint-metric__label">语法通过率</div>
                <div className="plint-metric__val">
                  {`${(baseline.syntax_pass_rate * 100).toFixed(1)}%`}
                </div>
              </div>
              <div className="plint-metric">
                <div className="plint-metric__label">编译通过率</div>
                <div className="plint-metric__val">
                  {`${(baseline.compile_pass_rate * 100).toFixed(1)}%`}
                </div>
              </div>
            </div>
          </div>
        )}

        <div className="plint-table-wrap">
          <table className="plint-table">
            <thead>
              <tr>
                <th>
                  <button
                    type="button"
                    className="btn btn-secondary"
                    aria-label="全选未导入仓库"
                    onClick={() =>
                      setSelectedIds((prev) =>
                        prev.length === selectableIds.length ? [] : selectableIds
                      )
                    }
                  >
                    <CheckSquare size={12} />
                  </button>
                </th>
                <th>平台仓库 / 子系统</th>
                <th>本地缓存 & 分支</th>
                <th>pg 白名单探测</th>
                <th>同步策略</th>
                <th>状态</th>
              </tr>
            </thead>
            <tbody>
              {repos.length === 0 ? (
                <tr>
                  <td colSpan={6} className="ucat-empty-cell">
                    {loading ? '正在扫描平台仓库...' : '暂无匹配的平台代码仓'}
                  </td>
                </tr>
              ) : (
                repos.map((r) => {
                  const checked = selectedIds.includes(r.platform_repo_id);
                  const strategy = strategyMap[r.platform_repo_id] ?? 'FETCH_AND_READ';
                  return (
                    <tr key={r.platform_repo_id}>
                      <td>
                        <input
                          type="checkbox"
                          disabled={r.already_imported}
                          checked={checked}
                          aria-label={`选择平台仓 ${r.name}`}
                          onChange={() => toggleSelect(r.platform_repo_id)}
                        />
                      </td>
                      <td>
                        <div>
                          <strong>{r.name}</strong>
                        </div>
                        <div className="plint-hint">
                          子系统: {r.service_group || r.subsystem_slug || 'unassigned'}
                        </div>
                      </td>
                      <td>
                        <div>
                          <span
                            className={`code-badge ${
                              r.local_cache_present ? 'code-badge-success' : 'code-badge-info'
                            }`}
                          >
                            {r.local_cache_present ? '已命中共享缓存' : '首次同步克隆'}
                          </span>
                        </div>
                        <div className="plint-hint">分支: {r.resolved_branch || r.branch || 'main'}</div>
                      </td>
                      <td>
                        <div>
                          {r.pg_dirs.length > 0 ? r.pg_dirs.join(', ') : '未检测到 pg/'}
                        </div>
                        <div className="plint-hint">
                          白名单 {r.whitelist_proto_count} · 忽略 {r.outside_whitelist_count}
                        </div>
                      </td>
                      <td>
                        <select
                          className="pgit-input"
                          aria-label={`同步策略 ${r.name}`}
                          disabled={r.already_imported}
                          value={strategy}
                          onChange={(e) =>
                            setStrategyMap((prev) => ({
                              ...prev,
                              [r.platform_repo_id]: e.target.value as GitSyncStrategy,
                            }))
                          }
                        >
                          <option value="FETCH_AND_READ">FETCH_AND_READ</option>
                          <option value="LOCAL_CACHE_ONLY">LOCAL_CACHE_ONLY</option>
                        </select>
                      </td>
                      <td>
                        {r.already_imported ? (
                          <span className="code-badge code-badge-success">已纳管</span>
                        ) : (
                          <span className="pdoc-pill">待导入</span>
                        )}
                      </td>
                    </tr>
                  );
                })
              )}
            </tbody>
          </table>
        </div>
      </div>
    </Drawer>
  );
};

export default PlatformRepoImportDrawer;
