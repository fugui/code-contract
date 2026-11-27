import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  BookOpen,
  ChevronDown,
  ChevronRight,
  FileCode2,
  FileWarning,
  Folder,
  FolderGit2,
  FolderOpen,
  FolderTree,
  Package,
  Search,
  SlidersHorizontal,
} from 'lucide-react';
import { EmptyState } from '@code/common';
import { getProtoDocsTree, getProtoServiceDocDetail } from '../../api/schema';
import { useToast } from '../../utils/toast';
import type {
  ProtoDocTreeNodeDTO,
  ProtoDocTreeResponse,
  ProtoServiceDocDetailDTO,
} from '../../types';
import {
  buildDocPackageTree,
  buildDocSubsystemTree,
  findAncestorKeysForService,
  type DocServiceLeaf,
  type DocTreeViewMode,
} from './docTreeBuilder';
import { ServiceReferenceDocView } from './ServiceReferenceDocView';
import './ProtoDocsAndCodeViewer.css';

export interface LivingProtoDocsPaneProps {
  initialTree?: ProtoDocTreeResponse;
  initialDetail?: ProtoServiceDocDetailDTO | null;
  initialServiceName?: string;
  initialSchemaId?: number | string;
  initialSearch?: string;
  initialViewMode?: DocTreeViewMode;
  onJumpToSource: (schemaId: number, filePath: string, line: number) => void;
}

function findFirstService(
  nodes: ProtoDocTreeNodeDTO[]
): { serviceName: string; schemaId?: number } | null {
  for (const n of nodes) {
    if (n.node_type === 'SERVICE' && n.service_name) {
      return { serviceName: n.service_name, schemaId: n.schema_id };
    }
    if (n.children && n.children.length > 0) {
      const hit = findFirstService(n.children);
      if (hit) return hit;
    }
  }
  return null;
}

function normalizeInitialServiceQuery(rawSearch: string): string {
  const trimmed = rawSearch.trim();
  if (!trimmed) return '';
  if (trimmed.includes('/')) {
    return trimmed.split('/')[0];
  }
  return trimmed;
}

export const LivingProtoDocsPane: React.FC<LivingProtoDocsPaneProps> = ({
  initialTree,
  initialDetail,
  initialServiceName = '',
  initialSchemaId = '',
  initialSearch = '',
  initialViewMode = 'subsystem',
  onJumpToSource,
}) => {
  const toast = useToast();
  const [tree, setTree] = useState<ProtoDocTreeResponse | null>(initialTree ?? null);
  const [loadingTree, setLoadingTree] = useState(false);
  const [searchInput, setSearchInput] = useState(() => normalizeInitialServiceQuery(initialSearch));
  const [categoryFilter, setCategoryFilter] = useState('');
  const [includeDeprecated, setIncludeDeprecated] = useState(true);
  const [viewMode, setViewMode] = useState<DocTreeViewMode>(initialViewMode);
  const [collapsedKeys, setCollapsedKeys] = useState<Set<string>>(() => new Set());

  const [selectedService, setSelectedService] = useState<string>(
    initialServiceName || initialDetail?.service_name || ''
  );
  const [selectedSchemaId, setSelectedSchemaId] = useState<number | string>(
    initialSchemaId || initialDetail?.schema_id || ''
  );
  const [detail, setDetail] = useState<ProtoServiceDocDetailDTO | null>(initialDetail ?? null);
  const [loadingDetail, setLoadingDetail] = useState(false);
  const detailRef = useRef<HTMLElement | null>(null);

  const loadTree = useCallback(async () => {
    if (typeof getProtoDocsTree !== 'function') return;
    setLoadingTree(true);
    try {
      const data = await getProtoDocsTree({
        category: categoryFilter || undefined,
        schema_id: selectedSchemaId && !selectedService ? Number(selectedSchemaId) : undefined,
        include_deprecated: includeDeprecated,
        search: searchInput.trim() || undefined,
      });
      setTree(data);
      if (!selectedService && data.items.length > 0) {
        const first = findFirstService(data.items);
        if (first) {
          setSelectedService(first.serviceName);
          if (first.schemaId) setSelectedSchemaId(first.schemaId);
        }
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '加载接口参考文档树失败');
    } finally {
      setLoadingTree(false);
    }
  }, [categoryFilter, includeDeprecated, searchInput, selectedSchemaId, selectedService, toast]);

  useEffect(() => {
    if (initialTree) return;
    void loadTree();
  }, [initialTree, loadTree]);

  useEffect(() => {
    if (initialDetail && initialDetail.service_name === selectedService) {
      return;
    }
    if (!selectedService || typeof getProtoServiceDocDetail !== 'function') {
      return;
    }
    let cancelled = false;
    setLoadingDetail(true);
    getProtoServiceDocDetail(selectedService, selectedSchemaId || undefined)
      .then((res) => {
        if (!cancelled) {
          setDetail(res);
        }
      })
      .catch((err) => {
        if (!cancelled) {
          toast.error(err instanceof Error ? err.message : '加载服务接口详情失败');
        }
      })
      .finally(() => {
        if (!cancelled) setLoadingDetail(false);
      });
    return () => {
      cancelled = true;
    };
  }, [initialDetail, selectedSchemaId, selectedService, toast]);

  const subsystems = useMemo(
    () => buildDocSubsystemTree(tree?.items ?? []),
    [tree]
  );
  const packageBranches = useMemo(
    () => buildDocPackageTree(tree?.items ?? []),
    [tree]
  );

  // 选中服务或深链跳转时，自动展开所在分支
  useEffect(() => {
    if (!selectedService || subsystems.length === 0) return;
    const ancestorKeys = findAncestorKeysForService(
      subsystems,
      selectedService,
      selectedSchemaId
    );
    if (ancestorKeys.length === 0) return;
    setCollapsedKeys((prev) => {
      let changed = false;
      for (const k of ancestorKeys) {
        if (prev.has(k)) {
          changed = true;
          break;
        }
      }
      if (!changed) return prev;
      const next = new Set(prev);
      for (const k of ancestorKeys) {
        next.delete(k);
      }
      return next;
    });
  }, [subsystems, selectedService, selectedSchemaId]);

  const handleSelectService = (svcName: string, schemaId?: number) => {
    const isChanged = svcName !== selectedService || (schemaId ?? '') !== selectedSchemaId;
    setSelectedService(svcName);
    setSelectedSchemaId(schemaId ?? '');
    if (isChanged && typeof detailRef.current?.scrollIntoView === 'function') {
      detailRef.current.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
  };

  const toggleNode = (key: string) => {
    setCollapsedKeys((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  const hasSearch = searchInput.trim().length > 0;
  const activeFilterCount = (categoryFilter ? 1 : 0) + (!includeDeprecated ? 1 : 0);

  const isServiceActive = (leaf: DocServiceLeaf): boolean => {
    if (selectedService !== leaf.serviceName) return false;
    if (
      selectedSchemaId !== '' &&
      selectedSchemaId !== undefined &&
      leaf.schemaId !== undefined
    ) {
      return Number(selectedSchemaId) === leaf.schemaId;
    }
    return true;
  };

  const renderServiceRow = (leaf: DocServiceLeaf, depth: number) => {
    const active = isServiceActive(leaf);
    const cls = [
      'pgit-tree-row',
      'pgit-tree-row--file',
      active ? 'pgit-tree-row--active' : '',
      leaf.isDeprecated ? 'pdoc-tree-row--deprecated' : '',
    ]
      .filter(Boolean)
      .join(' ');
    const title = leaf.summary
      ? `${leaf.serviceName} — ${leaf.summary}`
      : leaf.serviceName;

    return (
      <button
        key={leaf.key}
        type="button"
        className={cls}
        style={{ paddingLeft: 10 + depth * 14 }}
        onClick={() => handleSelectService(leaf.serviceName, leaf.schemaId)}
        title={title}
      >
        <span className="pgit-tree-row__main">
          <FileCode2 size={13} />
          <span className="pgit-tree-row__label">{leaf.label}</span>
        </span>
        <span className="pgit-tree-count">{leaf.methodCount}</span>
      </button>
    );
  };

  const renderSubsystemTree = () => {
    if (subsystems.length === 0) {
      return (
        <EmptyState
          icon={<FileWarning size={28} />}
          title="暂无匹配文档符号"
          description="请上传/在线编译或激活描述集后自动生成全注释参考文档。"
        />
      );
    }

    return subsystems.map((sub) => {
      const subOpen = hasSearch || !collapsedKeys.has(sub.key);
      return (
        <div key={sub.key} className="pgit-tree-branch">
          <button
            type="button"
            className="pgit-tree-row pgit-tree-row--subsystem"
            onClick={() => toggleNode(sub.key)}
            title={`${sub.label} · ${sub.serviceCount} 服务 · ${sub.methodCount} 方法`}
          >
            <span className="pgit-tree-row__main">
              {subOpen ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
              {subOpen ? <FolderOpen size={14} /> : <Folder size={14} />}
              <span className="pgit-tree-row__label">{sub.label}</span>
            </span>
            <span className="pgit-tree-count">{sub.serviceCount}</span>
          </button>

          {subOpen &&
            sub.schemas.map((schema) => {
              const schemaOpen = hasSearch || !collapsedKeys.has(schema.key);
              const schemaTitle = [
                schema.category || 'PROTO',
                schema.label,
                schema.singlePackageName,
              ]
                .filter(Boolean)
                .join(' · ');

              return (
                <div key={schema.key} className="pgit-tree-branch">
                  <button
                    type="button"
                    className="pgit-tree-row pgit-tree-row--repo"
                    style={{ paddingLeft: 24 }}
                    onClick={() => toggleNode(schema.key)}
                    title={schemaTitle}
                  >
                    <span className="pgit-tree-row__main">
                      {schemaOpen ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                      <FolderGit2 size={13} />
                      <span className="pgit-tree-row__label">{schema.label}</span>
                    </span>
                    <span className="pgit-tree-count">{schema.serviceCount}</span>
                  </button>

                  {schemaOpen &&
                    (schema.directServices !== null
                      ? schema.directServices.map((leaf) => renderServiceRow(leaf, 2))
                      : schema.packages.map((pkg) => {
                          const pkgOpen = hasSearch || !collapsedKeys.has(pkg.key);
                          return (
                            <div key={pkg.key} className="pgit-tree-branch">
                              <button
                                type="button"
                                className="pgit-tree-row pgit-tree-row--folder"
                                style={{ paddingLeft: 38 }}
                                onClick={() => toggleNode(pkg.key)}
                                title={pkg.packageName}
                              >
                                <span className="pgit-tree-row__main">
                                  {pkgOpen ? (
                                    <ChevronDown size={13} />
                                  ) : (
                                    <ChevronRight size={13} />
                                  )}
                                  <Package size={13} />
                                  <span className="pgit-tree-row__label">{pkg.packageName}</span>
                                </span>
                                <span className="pgit-tree-count">{pkg.serviceCount}</span>
                              </button>

                              {pkgOpen &&
                                pkg.services.map((leaf) => renderServiceRow(leaf, 3))}
                            </div>
                          );
                        }))}
                </div>
              );
            })}
        </div>
      );
    });
  };

  const renderPackageTree = () => {
    if (packageBranches.length === 0) {
      return (
        <EmptyState
          icon={<FileWarning size={28} />}
          title="暂无匹配文档符号"
          description="请上传/在线编译或激活描述集后自动生成全注释参考文档。"
        />
      );
    }

    return packageBranches.map((pkg) => {
      const pkgOpen = hasSearch || !collapsedKeys.has(pkg.key);
      return (
        <div key={pkg.key} className="pgit-tree-branch">
          <button
            type="button"
            className="pgit-tree-row pgit-tree-row--subsystem"
            onClick={() => toggleNode(pkg.key)}
            title={`${pkg.packageName} · ${pkg.serviceCount} 服务 · ${pkg.methodCount} 方法`}
          >
            <span className="pgit-tree-row__main">
              {pkgOpen ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
              <Package size={13} />
              <span className="pgit-tree-row__label">{pkg.packageName}</span>
            </span>
            <span className="pgit-tree-count">{pkg.serviceCount}</span>
          </button>

          {pkgOpen && pkg.services.map((leaf) => renderServiceRow(leaf, 1))}
        </div>
      );
    });
  };

  return (
    <div className="pdoc-workbench" data-testid="living-proto-docs-pane">
      <aside className="pgit-tree-panel" aria-label="活体文档服务树">
        <div className="pgit-tree-header">
          <div className="pdoc-tree-summary-bar">
            <h3 className="pdoc-tree-panel__title">
              <FolderTree size={13} />
              <span>活体接口参考目录</span>
            </h3>
            {tree && (
              <div className="pdoc-tree-summary-bar__stats">
                <span>{tree.total_services} 服务</span>
                <span>{tree.total_methods} 方法</span>
              </div>
            )}
          </div>

          <div className="pgit-search-box">
            <Search size={14} />
            <input
              type="text"
              className="pdoc-tree-search"
              placeholder="搜索服务、方法、注释、@error_code..."
              value={searchInput}
              onChange={(e) => setSearchInput(e.target.value)}
            />
          </div>

          <div className="pgit-tree-mode-row">
            <div className="code-tab-pills" role="group" aria-label="文档目录树视图模式">
              <button
                type="button"
                data-testid="doc-tree-mode-subsystem"
                className={`code-pill-btn ${viewMode === 'subsystem' ? 'code-pill-btn--active' : ''}`}
                onClick={() => setViewMode('subsystem')}
              >
                按子系统
              </button>
              <button
                type="button"
                data-testid="doc-tree-mode-package"
                className={`code-pill-btn ${viewMode === 'package' ? 'code-pill-btn--active' : ''}`}
                onClick={() => setViewMode('package')}
              >
                按包名
              </button>
            </div>

            <details className="pgit-filter-details">
              <summary className="pgit-filter-summary">
                <SlidersHorizontal size={12} />
                <span>筛选</span>
                {activeFilterCount > 0 && (
                  <span className="pgit-tree-count">{activeFilterCount}</span>
                )}
              </summary>
              <div className="pgit-filter-body">
                <select
                  className="code-select"
                  value={categoryFilter}
                  onChange={(e) => setCategoryFilter(e.target.value)}
                  aria-label="契约分类筛选"
                >
                  <option value="">全部分类</option>
                  <option value="BUSINESS">BUSINESS 业务契约</option>
                  <option value="GATEWAY">GATEWAY 网关契约</option>
                  <option value="BASE_LIB">BASE_LIB 公共依赖库</option>
                </select>

                <label className="pgit-check">
                  <input
                    type="checkbox"
                    checked={includeDeprecated}
                    onChange={(e) => setIncludeDeprecated(e.target.checked)}
                  />
                  <span>显示已废弃</span>
                </label>
              </div>
            </details>
          </div>
        </div>

        <div className="pgit-tree-scroll">
          {loadingTree ? (
            <div className="pdoc-empty">正在聚合活体文档索引...</div>
          ) : viewMode === 'subsystem' ? (
            renderSubsystemTree()
          ) : (
            renderPackageTree()
          )}
        </div>
      </aside>

      <main ref={detailRef} className="pdoc-detail">
        {loadingDetail ? (
          <div className="pdoc-card">
            <p className="pdoc-card__subtitle">正在解码服务契约详情、Enum 字典与黄金样本...</p>
          </div>
        ) : detail ? (
          <ServiceReferenceDocView detail={detail} onJumpToSource={onJumpToSource} />
        ) : (
          <div className="pdoc-card">
            <EmptyState
              icon={<BookOpen size={36} />}
              title="请选择左侧 Service 查看活体接口参考文档"
              description="支持 @owner、@deprecated、@error_code、AST 缺省示例、ACTIVE 黄金样本与离线 Markdown 一键导出。"
            />
          </div>
        )}
      </main>
    </div>
  );
};
