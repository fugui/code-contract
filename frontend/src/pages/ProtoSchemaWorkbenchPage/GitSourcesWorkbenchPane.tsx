import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useLocation, useSearchParams } from 'react-router-dom';
import {
  ChevronDown,
  ChevronRight,
  ExternalLink,
  FileCode2,
  Folder,
  FolderGit2,
  FolderOpen,
  GitBranch,
  GitCommitHorizontal,
  Info,
  Layers,
  Package,
  RefreshCw,
  Scale,
  Search,
  Settings2,
  ShieldCheck,
  SlidersHorizontal,
  Sparkles,
} from 'lucide-react';
import * as schemaApi from '../../api/schema';
import { subscribeServerEvents } from '../../api/sse';
import { useModulePath } from '../../hooks/useModulePath';
import { useToast } from '../../utils/toast';
import type {
  GitConsistencyReportDTO,
  GitProtoBundleDTO,
  GitRepoDTO,
  GitSyncCompletedEventPayload,
  ProtoFileContentResponse,
  ProtoFileDTO,
  ProtoFileDiffResponse,
  ProtoFilesTreeResponse,
  ProtoFileVersionSummaryDTO,
} from '../../types';
import {
  buildCleanSubsystemTree,
  buildFallbackSubsystemTree,
  resolveCleanRelativePath,
  type CleanFileNode,
  type CleanFolderNode,
} from './cleanTreeBuilder';
import { ProtoCommitDiffView } from './ProtoCommitDiffView';
import { ProtoReadOnlyCodeViewer } from './ProtoReadOnlyCodeViewer';
import { GitConsistencyDrawer } from './GitConsistencyDrawer';
import { GitRepoConfigDrawer } from './GitRepoConfigDrawer';
import { ProtoConflictArbitrationDrawer } from './ProtoConflictArbitrationDrawer';
import './ProtoDocsAndCodeViewer.css';

export type TreeViewMode = 'clean' | 'package';

export interface GitSourcesWorkbenchPaneProps {
  isAdmin?: boolean;
  readOnlyConsumer?: boolean;
  initialSchemaName?: string;
  initialFileId?: number;
  initialHighlightLine?: number;
  initialGitRepos?: GitRepoDTO[];
  initialSelectedRepoId?: number | null;
  initialSelectedBundleId?: number | null;
  initialFiles?: ProtoFileDTO[];
  initialTotal?: number;
  initialSelectedFileId?: number | null;
  initialContent?: ProtoFileContentResponse | null;
  initialVersions?: ProtoFileVersionSummaryDTO[];
  initialManageOpen?: boolean;
  initialViewMode?: 'code' | 'diff';
  initialDiff?: ProtoFileDiffResponse | null;
  initialConsistencyOpen?: boolean;
  initialConsistencyReport?: GitConsistencyReportDTO | null;
  initialTree?: ProtoFilesTreeResponse | null;
  initialTreeViewMode?: TreeViewMode;
}

function parseHashLine(hash: string): number {
  const matched = /^#L(\d+)$/i.exec(hash.trim());
  if (!matched) return 1;
  const parsed = Number(matched[1]);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 1;
}

function parsePositiveParam(raw: string | null): number | null {
  if (!raw) return null;
  const num = Number(raw);
  return Number.isFinite(num) && num > 0 ? num : null;
}

function renderRepoSyncBadge(status?: string) {
  switch (status) {
    case 'SUCCESS':
      return <span className="code-badge code-badge-success">同步成功</span>;
    case 'RUNNING':
      return <span className="code-badge code-badge-info">同步中...</span>;
    case 'FAILED':
      return <span className="code-badge code-badge-danger">同步失败</span>;
    default:
      return <span className="pdoc-pill">{status || 'IDLE'}</span>;
  }
}

function findFileInTree(
  tree: ProtoFilesTreeResponse | null,
  fileId: number | null
): ProtoFileDTO | null {
  if (!tree || !fileId) return null;
  for (const grp of tree.groups) {
    const hit = grp.files.find((f) => f.id === fileId);
    if (hit) return hit;
  }
  return null;
}

function mergeSubsystemGroupFiles(
  prev: ProtoFilesTreeResponse | null,
  next: ProtoFilesTreeResponse,
  preferHydratedFromNext: boolean
): ProtoFilesTreeResponse {
  if (!prev || prev.view !== next.view) {
    return next;
  }
  if (prev.view === 'subsystem' && next.view === 'subsystem') {
    const prevMap = new Map<number, ProtoFileDTO[]>();
    for (const g of prev.groups) {
      if (g.files.length > 0) prevMap.set(g.git_repo_id, g.files);
    }
    return {
      ...next,
      groups: next.groups.map((g) => {
        if (preferHydratedFromNext && g.files.length > 0) {
          return g;
        }
        const cached = prevMap.get(g.git_repo_id);
        return g.files.length === 0 && cached && cached.length > 0
          ? { ...g, files: cached }
          : g;
      }),
    };
  }
  if (prev.view === 'package' && next.view === 'package') {
    const prevMap = new Map<string, ProtoFileDTO[]>();
    for (const g of prev.groups) {
      if (g.files.length > 0) prevMap.set(g.package_name, g.files);
    }
    return {
      ...next,
      groups: next.groups.map((g) => {
        if (preferHydratedFromNext && g.files.length > 0) {
          return g;
        }
        const cached = prevMap.get(g.package_name);
        return g.files.length === 0 && cached && cached.length > 0
          ? { ...g, files: cached }
          : g;
      }),
    };
  }
  return next;
}

export const GitSourcesWorkbenchPane: React.FC<GitSourcesWorkbenchPaneProps> = ({
  isAdmin = false,
  readOnlyConsumer = false,
  initialSchemaName = '',
  initialFileId,
  initialHighlightLine,
  initialGitRepos,
  initialSelectedRepoId,
  initialSelectedBundleId,
  initialFiles,
  initialSelectedFileId,
  initialContent,
  initialVersions,
  initialManageOpen = false,
  initialViewMode = 'code',
  initialDiff = null,
  initialConsistencyOpen = false,
  initialConsistencyReport = null,
  initialTree,
  initialTreeViewMode = 'clean',
}) => {
  const [searchParams, setSearchParams] = useSearchParams();
  const location = useLocation();
  const resolvePath = useModulePath();
  const toast = useToast();

  const urlRepoId = parsePositiveParam(searchParams.get('repo_id'));
  const urlBundleId = parsePositiveParam(searchParams.get('bundle_id'));
  const urlFileId = parsePositiveParam(searchParams.get('file_id'));
  const hashLine = parseHashLine(location.hash);

  // 1. 仓库与 Monorepo Bundle 状态
  const [gitRepos, setGitRepos] = useState<GitRepoDTO[]>(initialGitRepos ?? []);
  const [reposLoading, setReposLoading] = useState<boolean>(initialGitRepos === undefined);
  const [selectedRepoId, setSelectedRepoId] = useState<number | null>(
    urlRepoId ??
      initialSelectedRepoId ??
      (!readOnlyConsumer ? (initialGitRepos?.[0]?.id ?? null) : null)
  );
  const [selectedBundleId, setSelectedBundleId] = useState<number | null>(
    urlBundleId ?? initialSelectedBundleId ?? null
  );

  // 2. 左侧树视图、筛选与懒加载状态
  const resolvedInitialTree = useMemo<ProtoFilesTreeResponse | null>(() => {
    if (initialTree !== undefined) return initialTree;
    if (initialFiles !== undefined) {
      return buildFallbackSubsystemTree(initialFiles, initialGitRepos ?? []);
    }
    return null;
  }, [initialTree, initialFiles, initialGitRepos]);

  const [treeViewMode, setTreeViewMode] = useState<TreeViewMode>(initialTreeViewMode);
  const [treeData, setTreeData] = useState<ProtoFilesTreeResponse | null>(resolvedInitialTree);
  const [treeLoading, setTreeLoading] = useState<boolean>(
    initialTree === undefined && initialFiles === undefined
  );
  const [loadingRepoId, setLoadingRepoId] = useState<number | null>(null);
  const [loadingPackage, setLoadingPackage] = useState<string | null>(null);

  const [searchText, setSearchText] = useState<string>(initialSchemaName);
  const [includeDeleted, setIncludeDeleted] = useState<boolean>(false);
  const [closureOnly, setClosureOnly] = useState<boolean>(false);
  const [subsystemFilter, setSubsystemFilter] = useState<string>('');

  const [expandedSubsystems, setExpandedSubsystems] = useState<Set<string>>(() => {
    const next = new Set<string>();
    if (resolvedInitialTree?.view === 'subsystem') {
      for (const g of resolvedInitialTree.groups) {
        if (g.files.length > 0) {
          next.add((g.subsystem_slug || '').trim() || '_unassigned');
        }
      }
    }
    return next;
  });
  const [expandedRepoIds, setExpandedRepoIds] = useState<Set<number>>(() => {
    const next = new Set<number>();
    if (resolvedInitialTree?.view === 'subsystem') {
      for (const g of resolvedInitialTree.groups) {
        if (g.files.length > 0) {
          next.add(g.git_repo_id);
        }
      }
    }
    return next;
  });
  const [collapsedFolders, setCollapsedFolders] = useState<Set<string>>(() => new Set());
  const [expandedPackages, setExpandedPackages] = useState<Set<string>>(() => {
    const next = new Set<string>();
    if (resolvedInitialTree?.view === 'package') {
      for (const g of resolvedInitialTree.groups) {
        if (g.files.length > 0) {
          next.add(g.package_name);
        }
      }
    }
    return next;
  });

  // 3. 选中文件、版本、高亮行与视图状态
  const resolvedInitialFileId =
    urlFileId ?? initialSelectedFileId ?? initialFileId ?? (initialFiles?.[0]?.id ?? null);
  const resolvedInitialLine = hashLine > 1 ? hashLine : (initialHighlightLine ?? 1);

  const [selectedFileId, setSelectedFileId] = useState<number | null>(resolvedInitialFileId);
  const [highlightLine, setHighlightLine] = useState<number>(resolvedInitialLine);
  const [contentData, setContentData] = useState<ProtoFileContentResponse | null>(
    initialContent ?? null
  );
  const [contentLoading, setContentLoading] = useState<boolean>(false);
  const [versions, setVersions] = useState<ProtoFileVersionSummaryDTO[]>(initialVersions ?? []);
  const [selectedVersion, setSelectedVersion] = useState<number | undefined>(
    initialContent?.version?.version
  );

  const [viewMode, setViewMode] = useState<'code' | 'diff'>(initialViewMode);
  const [sourceStageMode, setSourceStageMode] = useState<'clean' | 'raw' | 'split'>('clean');
  const [infoExpanded, setInfoExpanded] = useState<boolean>(false);

  const [fromVersion, setFromVersion] = useState<number>(
    initialDiff?.from_version?.version ??
      (initialVersions && initialVersions.length > 1 ? initialVersions[1].version : 1)
  );
  const [toVersion, setToVersion] = useState<number>(
    initialDiff?.to_version?.version ?? (initialVersions?.[0]?.version ?? 1)
  );
  const [diffData, setDiffData] = useState<ProtoFileDiffResponse | null>(initialDiff);
  const [diffLoading, setDiffLoading] = useState<boolean>(false);

  // 4. 抽屉与同步运行状态
  const [manageDrawerOpen, setManageDrawerOpen] = useState<boolean>(initialManageOpen);
  const [consistencyDrawerOpen, setConsistencyDrawerOpen] = useState<boolean>(
    initialConsistencyOpen
  );
  const [conflictDrawerOpen, setConflictDrawerOpen] = useState<boolean>(false);
  const [syncDispatching, setSyncDispatching] = useState<boolean>(false);

  const skipInitialReposFetchRef = useRef<boolean>(initialGitRepos !== undefined);
  const skipInitialTreeFetchRef = useRef<boolean>(
    initialTree !== undefined || initialFiles !== undefined
  );
  const skipInitialContentFetchRef = useRef<boolean>(initialContent !== undefined);

  const selectedFileIdRef = useRef<number | null>(selectedFileId);
  selectedFileIdRef.current = selectedFileId;
  const expandedRepoIdsRef = useRef<Set<number>>(expandedRepoIds);
  expandedRepoIdsRef.current = expandedRepoIds;
  const expandedPackagesRef = useRef<Set<string>>(expandedPackages);
  expandedPackagesRef.current = expandedPackages;
  const contentFileRef = useRef<ProtoFileDTO | null>(contentData?.file ?? null);
  contentFileRef.current = contentData?.file ?? null;

  const selectedRepo = useMemo(
    () => gitRepos.find((r) => r.id === selectedRepoId) ?? null,
    [gitRepos, selectedRepoId]
  );

  const selectedBundle: GitProtoBundleDTO | null = useMemo(() => {
    if (!selectedRepo || !selectedBundleId) return null;
    return selectedRepo.bundles?.find((b) => b.id === selectedBundleId) ?? null;
  }, [selectedRepo, selectedBundleId]);

  const activeFileMeta = useMemo<ProtoFileDTO | null>(() => {
    return (
      contentData?.file ??
      findFileInTree(treeData, selectedFileId) ??
      initialFiles?.find((f) => f.id === selectedFileId) ??
      null
    );
  }, [contentData?.file, treeData, selectedFileId, initialFiles]);

  const activeRepo = useMemo(
    () => gitRepos.find((r) => r.id === activeFileMeta?.git_repo_id) ?? selectedRepo,
    [gitRepos, activeFileMeta?.git_repo_id, selectedRepo]
  );

  const normalizedViolations = useMemo(
    () =>
      (contentData?.violations ?? []).map((v) => ({
        ...v,
        line: v.line && v.line > 0 ? v.line : (v.file_line ?? 1),
      })),
    [contentData?.violations]
  );

  // URL 深链同步
  const prevUrlFileIdRef = useRef<number | null>(urlFileId);
  useEffect(() => {
    if (urlFileId && urlFileId !== prevUrlFileIdRef.current) {
      prevUrlFileIdRef.current = urlFileId;
      skipInitialContentFetchRef.current = false;
      setSelectedFileId(urlFileId);
    }
    if (hashLine > 1 || location.hash === '#L1') {
      setHighlightLine(hashLine);
    }
  }, [urlFileId, hashLine, location.hash]);

  const loadGitRepos = useCallback(async () => {
    setReposLoading(true);
    try {
      const resp = await schemaApi.listGitRepos({
        proto_status: 'HAS_PROTO',
        sync_enabled: true,
        service_group: !readOnlyConsumer ? subsystemFilter || undefined : undefined,
        page: 1,
        page_size: 100,
      });
      const items = resp.items ?? [];
      setGitRepos(items);
      if (items.length > 0 && !readOnlyConsumer && selectedRepoId === null && !urlFileId) {
        setSelectedRepoId(items[0].id);
      }
    } catch (err) {
      toast.error((err as Error).message || '加载 Git 仓库列表失败');
    } finally {
      setReposLoading(false);
    }
  }, [readOnlyConsumer, subsystemFilter, selectedRepoId, urlFileId, toast]);

  const buildTreeQueryBase = useCallback(
    (mode: TreeViewMode) => ({
      view: (mode === 'clean' ? 'subsystem' : 'package') as 'subsystem' | 'package',
      subsystem: !readOnlyConsumer ? subsystemFilter || undefined : undefined,
      git_repo_id: !readOnlyConsumer ? (selectedRepoId ?? undefined) : undefined,
      in_service_closure: closureOnly ? true : undefined,
      include_deleted: includeDeleted,
      search: searchText.trim() || undefined,
    }),
    [readOnlyConsumer, subsystemFilter, selectedRepoId, closureOnly, includeDeleted, searchText]
  );

  const expandRepoFiles = useCallback(
    async (repoId: number) => {
      setLoadingRepoId(repoId);
      try {
        const resp = await schemaApi.getProtoFilesTree({
          ...buildTreeQueryBase('clean'),
          summary_only: true,
          expand_repo_id: repoId,
        });
        setTreeData((prev) => mergeSubsystemGroupFiles(prev, resp, true));
        if (resp.view === 'subsystem') {
          const grp = resp.groups.find((g) => g.git_repo_id === repoId);
          if (grp && grp.files.length > 0 && !selectedFileIdRef.current && !urlFileId) {
            setSelectedFileId(grp.files[0].id);
          }
        }
      } catch (err) {
        toast.error((err as Error).message || '懒加载仓库契约文件失败');
      } finally {
        setLoadingRepoId((prev) => (prev === repoId ? null : prev));
      }
    },
    [buildTreeQueryBase, urlFileId, toast]
  );

  const expandPackageFiles = useCallback(
    async (pkgName: string) => {
      setLoadingPackage(pkgName);
      try {
        const resp = await schemaApi.getProtoFilesTree({
          ...buildTreeQueryBase('package'),
          summary_only: true,
          expand_package: pkgName,
        });
        setTreeData((prev) => mergeSubsystemGroupFiles(prev, resp, true));
        if (resp.view === 'package') {
          const grp = resp.groups.find((g) => g.package_name === pkgName);
          if (grp && grp.files.length > 0 && !selectedFileIdRef.current && !urlFileId) {
            setSelectedFileId(grp.files[0].id);
          }
        }
      } catch (err) {
        toast.error((err as Error).message || '懒加载包契约文件失败');
      } finally {
        setLoadingPackage((prev) => (prev === pkgName ? null : prev));
      }
    },
    [buildTreeQueryBase, urlFileId, toast]
  );

  const loadTreeSummary = useCallback(
    async (mode: TreeViewMode) => {
      setTreeLoading(true);
      try {
        const hasSearch = searchText.trim().length > 0;
        const resp = await schemaApi.getProtoFilesTree({
          ...buildTreeQueryBase(mode),
          summary_only: !hasSearch,
        });
        setTreeData((prev) => mergeSubsystemGroupFiles(prev, resp, false));

        if (resp.view === 'subsystem') {
          if (hasSearch) {
            const nextSubs = new Set<string>();
            const nextRepos = new Set<number>();
            let firstFile: ProtoFileDTO | null = null;
            for (const g of resp.groups) {
              if (g.files.length > 0) {
                nextSubs.add((g.subsystem_slug || '').trim() || '_unassigned');
                nextRepos.add(g.git_repo_id);
                if (!firstFile) firstFile = g.files[0];
              }
            }
            setExpandedSubsystems(nextSubs);
            setExpandedRepoIds(nextRepos);
            if (!selectedFileIdRef.current && !urlFileId && firstFile) {
              setSelectedFileId(firstFile.id);
            }
            return;
          }

          if (resp.groups.length > 0) {
            const preferredRepoId =
              urlRepoId ?? initialSelectedRepoId ?? contentFileRef.current?.git_repo_id ?? null;
            const currentExpanded = expandedRepoIdsRef.current;
            const targetGrp =
              (preferredRepoId
                ? resp.groups.find(
                    (g) =>
                      g.git_repo_id === preferredRepoId &&
                      (g.total_count ?? g.files.length) > 0
                  )
                : undefined) ??
              resp.groups.find(
                (g) =>
                  currentExpanded.has(g.git_repo_id) &&
                  (g.total_count ?? g.files.length) > 0
              ) ??
              resp.groups.find((g) => (g.total_count ?? g.files.length) > 0) ??
              resp.groups[0];

            if (targetGrp) {
              const targetSubSlug = (targetGrp.subsystem_slug || '').trim() || '_unassigned';
              setExpandedSubsystems((prev) => {
                if (prev.has(targetSubSlug)) return prev;
                const next = new Set(prev);
                next.add(targetSubSlug);
                return next;
              });
              setExpandedRepoIds((prev) => {
                if (prev.has(targetGrp.git_repo_id)) return prev;
                const next = new Set(prev);
                next.add(targetGrp.git_repo_id);
                return next;
              });
              if (targetGrp.files.length === 0 && (targetGrp.total_count ?? 0) > 0) {
                void expandRepoFiles(targetGrp.git_repo_id);
              } else if (
                targetGrp.files.length > 0 &&
                !selectedFileIdRef.current &&
                !urlFileId
              ) {
                setSelectedFileId(targetGrp.files[0].id);
              }
            }
          }
          return;
        }

        if (resp.view === 'package') {
          if (hasSearch) {
            const nextPkgs = new Set<string>();
            let firstFile: ProtoFileDTO | null = null;
            for (const g of resp.groups) {
              if (g.files.length > 0) {
                nextPkgs.add(g.package_name);
                if (!firstFile) firstFile = g.files[0];
              }
            }
            setExpandedPackages(nextPkgs);
            if (!selectedFileIdRef.current && !urlFileId && firstFile) {
              setSelectedFileId(firstFile.id);
            }
            return;
          }

          if (resp.groups.length > 0) {
            const activePkg = contentFileRef.current?.package_name || '';
            const currentExpanded = expandedPackagesRef.current;
            const targetPkg =
              (activePkg
                ? resp.groups.find(
                    (g) =>
                      g.package_name === activePkg && (g.total_count ?? g.files.length) > 0
                  )
                : undefined) ??
              resp.groups.find(
                (g) =>
                  currentExpanded.has(g.package_name) && (g.total_count ?? g.files.length) > 0
              ) ??
              resp.groups.find((g) => (g.total_count ?? g.files.length) > 0) ??
              resp.groups[0];

            if (targetPkg) {
              setExpandedPackages((prev) => {
                if (prev.has(targetPkg.package_name)) return prev;
                const next = new Set(prev);
                next.add(targetPkg.package_name);
                return next;
              });
              if (targetPkg.files.length === 0 && (targetPkg.total_count ?? 0) > 0) {
                void expandPackageFiles(targetPkg.package_name);
              } else if (
                targetPkg.files.length > 0 &&
                !selectedFileIdRef.current &&
                !urlFileId
              ) {
                setSelectedFileId(targetPkg.files[0].id);
              }
            }
          }
        }
      } catch (err) {
        toast.error((err as Error).message || '加载契约目录树失败');
      } finally {
        setTreeLoading(false);
      }
    },
    [
      searchText,
      buildTreeQueryBase,
      urlFileId,
      urlRepoId,
      initialSelectedRepoId,
      expandRepoFiles,
      expandPackageFiles,
      toast,
    ]
  );

  const loadFileDetail = useCallback(
    async (fileId: number, versionOverride?: number) => {
      setContentLoading(true);
      try {
        const [contentResp, versionsResp] = await Promise.all([
          schemaApi.getProtoFileContent(fileId, versionOverride),
          schemaApi.listProtoFileVersions(fileId),
        ]);
        setContentData(contentResp);
        const verItems = versionsResp.items ?? [];
        setVersions(verItems);
        setSelectedVersion(contentResp.version?.version ?? versionOverride);
        if (verItems.length >= 2) {
          setToVersion(verItems[0].version);
          setFromVersion(verItems[1].version);
        } else if (verItems.length === 1) {
          setToVersion(verItems[0].version);
          setFromVersion(verItems[0].version);
        }
      } catch (err) {
        toast.error((err as Error).message || '加载源码与版本明细失败');
      } finally {
        setContentLoading(false);
      }
    },
    [toast]
  );

  useEffect(() => {
    if (skipInitialReposFetchRef.current) {
      skipInitialReposFetchRef.current = false;
      return;
    }
    void loadGitRepos();
  }, [loadGitRepos]);

  useEffect(() => {
    if (skipInitialTreeFetchRef.current) {
      skipInitialTreeFetchRef.current = false;
      return;
    }
    void loadTreeSummary(treeViewMode);
  }, [treeViewMode, loadTreeSummary]);

  useEffect(() => {
    if (!selectedFileId) {
      return;
    }
    if (skipInitialContentFetchRef.current) {
      skipInitialContentFetchRef.current = false;
      return;
    }
    void loadFileDetail(selectedFileId, selectedVersion);
  }, [selectedFileId, selectedVersion, loadFileDetail]);

  // 深链进入时，若选中文件所属仓库尚未在树中展开，自动定位展开并懒加载
  useEffect(() => {
    const repoId = contentData?.file?.git_repo_id;
    if (!repoId || !treeData || treeData.view !== 'subsystem') return;
    const grp = treeData.groups.find((g) => g.git_repo_id === repoId);
    if (!grp) return;
    const slug = (grp.subsystem_slug || '').trim() || '_unassigned';
    if (!expandedSubsystems.has(slug)) {
      setExpandedSubsystems((prev) => new Set(prev).add(slug));
    }
    if (!expandedRepoIds.has(repoId)) {
      setExpandedRepoIds((prev) => new Set(prev).add(repoId));
    }
    if (grp.files.length === 0 && (grp.total_count ?? 0) > 0 && loadingRepoId !== repoId) {
      void expandRepoFiles(repoId);
    }
  }, [
    contentData?.file?.git_repo_id,
    treeData,
    expandedSubsystems,
    expandedRepoIds,
    loadingRepoId,
    expandRepoFiles,
  ]);

  useEffect(() => {
    if (viewMode !== 'diff' || !selectedFileId || fromVersion <= 0 || toVersion <= 0) {
      return;
    }
    if (
      initialDiff &&
      fromVersion === initialDiff.from_version.version &&
      toVersion === initialDiff.to_version.version &&
      selectedFileId === initialDiff.file.id
    ) {
      return;
    }
    let cancelled = false;
    const runDiff = async () => {
      setDiffLoading(true);
      try {
        const resp = await schemaApi.getProtoFileDiff(selectedFileId, fromVersion, toVersion);
        if (!cancelled) {
          setDiffData(resp);
        }
      } catch (err) {
        if (!cancelled) {
          toast.error((err as Error).message || '加载 Commit Diff 失败');
        }
      } finally {
        if (!cancelled) {
          setDiffLoading(false);
        }
      }
    };
    void runDiff();
    return () => {
      cancelled = true;
    };
  }, [viewMode, selectedFileId, fromVersion, toVersion, initialDiff, toast]);

  // 订阅 /api/events SSE git_sync.completed 事件
  useEffect(() => {
    if (typeof window === 'undefined' || typeof fetch === 'undefined') {
      return;
    }
    const unsubscribe = subscribeServerEvents({
      onMessage: (evt) => {
        if (evt.event !== 'git_sync.completed') {
          return;
        }
        const payload = evt.data as GitSyncCompletedEventPayload | undefined;
        if (payload?.status === 'SUCCESS') {
          toast.success(
            `Git 同步完成 (${payload.repo_name || `#${payload.repo_id}`})：更新 ${payload.files_updated ?? 0} 个文件`
          );
        } else if (payload?.status === 'FAILED') {
          toast.error(`Git 同步失败：${payload.message || '未知错误'}`);
        }
        skipInitialReposFetchRef.current = false;
        skipInitialTreeFetchRef.current = false;
        skipInitialContentFetchRef.current = false;
        void loadGitRepos();
        void loadTreeSummary(treeViewMode);
        if (selectedFileId) {
          void loadFileDetail(selectedFileId, undefined);
        }
      },
    });
    return unsubscribe;
  }, [loadGitRepos, loadTreeSummary, treeViewMode, loadFileDetail, selectedFileId, toast]);

  const handleSelectRepo = (repoId: number | null) => {
    skipInitialTreeFetchRef.current = false;
    setSelectedRepoId(repoId);
    setSelectedBundleId(null);
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      if (!next.has('docs_view')) next.set('tab', 'sources');
      if (repoId) next.set('repo_id', String(repoId));
      else next.delete('repo_id');
      next.delete('bundle_id');
      return next;
    });
  };

  const handleSelectBundle = (bundleId: number | null) => {
    skipInitialTreeFetchRef.current = false;
    setSelectedBundleId(bundleId);
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      if (!next.has('docs_view')) next.set('tab', 'sources');
      if (bundleId) next.set('bundle_id', String(bundleId));
      else next.delete('bundle_id');
      return next;
    });
  };

  const handleSelectFile = (file: ProtoFileDTO) => {
    skipInitialContentFetchRef.current = false;
    setSelectedFileId(file.id);
    setSelectedVersion(undefined);
    setHighlightLine(1);
    setDiffData(null);
    setViewMode('code');
    setSourceStageMode('clean');
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      if (!next.has('docs_view')) {
        next.set('tab', 'sources');
      }
      next.set('file_id', String(file.id));
      return next;
    });
  };

  const handleSelectLine = (line: number) => {
    setHighlightLine(line);
    if (typeof window !== 'undefined' && window.history?.replaceState) {
      const url = new URL(window.location.href);
      url.hash = `#L${line}`;
      window.history.replaceState(null, '', url.toString());
    }
  };

  const toggleSubsystem = (slug: string) => {
    setExpandedSubsystems((prev) => {
      const next = new Set(prev);
      if (next.has(slug)) next.delete(slug);
      else next.add(slug);
      return next;
    });
  };

  const toggleExpandRepoGroup = async (repoId: number, hasFilesLoaded: boolean) => {
    const nextOpen = !expandedRepoIds.has(repoId);
    setExpandedRepoIds((prev) => {
      const next = new Set(prev);
      if (nextOpen) next.add(repoId);
      else next.delete(repoId);
      return next;
    });
    if (nextOpen && !hasFilesLoaded) {
      await expandRepoFiles(repoId);
    }
  };

  const toggleFolder = (folderKey: string) => {
    setCollapsedFolders((prev) => {
      const next = new Set(prev);
      if (next.has(folderKey)) next.delete(folderKey);
      else next.add(folderKey);
      return next;
    });
  };

  const toggleExpandPackageGroup = async (pkgName: string, hasFilesLoaded: boolean) => {
    const nextOpen = !expandedPackages.has(pkgName);
    setExpandedPackages((prev) => {
      const next = new Set(prev);
      if (nextOpen) next.add(pkgName);
      else next.delete(pkgName);
      return next;
    });
    if (nextOpen && !hasFilesLoaded) {
      await expandPackageFiles(pkgName);
    }
  };

  const handleTriggerSync = async (branchOverride?: string, bundleOverride?: number | null) => {
    const targetRepo = activeRepo ?? selectedRepo;
    if (!targetRepo || !isAdmin) return;
    setSyncDispatching(true);
    try {
      const res = await schemaApi.triggerGitRepoSync(targetRepo.id, {
        bundle_id:
          bundleOverride !== undefined
            ? (bundleOverride ?? undefined)
            : (selectedBundleId ?? undefined),
        branch: branchOverride || targetRepo.default_branch || 'main',
      });
      toast.info(`已发起分支 [${res.branch}] 异步契约同步，请稍候 SSE 自动刷新`);
      setGitRepos((prev) =>
        prev.map((r) => (r.id === targetRepo.id ? { ...r, last_sync_status: 'RUNNING' } : r))
      );
    } catch (err) {
      toast.error((err as Error).message || '触发同步失败');
    } finally {
      setSyncDispatching(false);
    }
  };

  const handleReNormalize = async () => {
    if (!selectedRepo || !isAdmin) return;
    setSyncDispatching(true);
    try {
      const res = await schemaApi.reNormalizeGitRepo(selectedRepo.id, true);
      toast.success(
        `已完成 [${res.repo_name}] 免网络重清洗：${res.normalized_count} 个通过，${res.quarantined_count} 个隔离`
      );
      await loadTreeSummary(treeViewMode);
    } catch (err) {
      toast.error((err as Error).message || '重跑 Stage B~D 失败');
    } finally {
      setSyncDispatching(false);
    }
  };

  const distinctSubsystems = useMemo(() => {
    const set = new Set<string>();
    for (const r of gitRepos) {
      const sg = r.service_group || r.export_target_subsystem || r.subsystem_slug;
      if (sg) set.add(sg);
    }
    return Array.from(set).sort();
  }, [gitRepos]);

  const cleanSubsystems = useMemo(() => {
    if (!treeData || treeData.view !== 'subsystem') return [];
    const groups =
      !readOnlyConsumer && selectedBundleId
        ? treeData.groups
            .map((g) => ({
              ...g,
              files: g.files.filter((f) => f.bundle_id === selectedBundleId),
            }))
            .filter((g) => g.files.length > 0 || (g.total_count ?? 0) > 0)
        : treeData.groups;
    return buildCleanSubsystemTree(groups);
  }, [treeData, readOnlyConsumer, selectedBundleId]);

  const activeFilterCount = (closureOnly ? 1 : 0) + (includeDeleted ? 1 : 0);
  const hasRawVariant = Boolean(contentData?.raw_content && contentData.raw_content.length > 0);
  const cleanRelDisplay = activeFileMeta ? resolveCleanRelativePath(activeFileMeta) : '';

  const breadcrumbInfo = useMemo(() => {
    if (!activeFileMeta) {
      return { dirSegments: [] as string[], fileName: '' };
    }
    const cleanRel = resolveCleanRelativePath(activeFileMeta);
    const parts = cleanRel.split('/').filter(Boolean);
    const fileName = parts.length > 0 ? parts[parts.length - 1] : activeFileMeta.file_path;
    const dirParts = parts.slice(0, -1);

    const treeGroup =
      treeData?.view === 'subsystem'
        ? treeData.groups.find((g) => g.git_repo_id === activeFileMeta.git_repo_id)
        : undefined;
    const subSlug =
      treeGroup?.subsystem_slug ||
      activeRepo?.export_target_subsystem ||
      activeRepo?.subsystem_slug ||
      activeRepo?.service_group ||
      '';
    const repoDir =
      treeGroup?.export_target_repo_dir ||
      treeGroup?.git_repo_name ||
      activeRepo?.export_target_repo_dir ||
      activeFileMeta.git_repo_name ||
      '';

    const dirSegments = [subSlug, repoDir, ...dirParts].filter(Boolean);
    return { dirSegments, fileName };
  }, [activeFileMeta, treeData, activeRepo]);

  const pipelineGovernPath = resolvePath(
    activeFileMeta?.git_repo_id || selectedRepoId
      ? `/proto-pipeline?stage=normalize&repo_id=${activeFileMeta?.git_repo_id || selectedRepoId}`
      : '/proto-pipeline?stage=normalize'
  );

  const renderFileStatusTags = (file: ProtoFileDTO) => (
    <span className="pgit-file-meta">
      {file.pipeline_status === 'QUARANTINED' && (
        <span className="code-badge code-badge-danger" title="QUARANTINED">
          隔离
        </span>
      )}
      {file.is_external_copy && (
        <span className="code-badge code-badge-warning">外部副本</span>
      )}
      {file.is_deleted && <span className="code-badge code-badge-danger">已删除</span>}
    </span>
  );

  const renderCleanNodes = (
    nodes: Array<CleanFolderNode | CleanFileNode>,
    depth: number
  ): React.ReactNode => {
    const paddingLeft = 10 + depth * 14;
    return nodes.map((node) => {
      if (node.kind === 'folder') {
        const isOpen = !collapsedFolders.has(node.key);
        return (
          <div key={node.key} className="pgit-tree-branch">
            <button
              type="button"
              className="pgit-tree-row pgit-tree-row--folder"
              style={{ paddingLeft }}
              onClick={() => toggleFolder(node.key)}
              title={node.label}
            >
              <span className="pgit-tree-row__main">
                {isOpen ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                {isOpen ? <FolderOpen size={13} /> : <Folder size={13} />}
                <span className="pgit-tree-row__label">{node.label}</span>
              </span>
              <span className="pgit-tree-count">{node.fileCount}</span>
            </button>
            {isOpen && renderCleanNodes(node.children, depth + 1)}
          </div>
        );
      }

      const active = node.file.id === selectedFileId;
      return (
        <button
          key={node.key}
          type="button"
          className={`pgit-tree-row pgit-tree-row--file ${active ? 'pgit-tree-row--active' : ''}`}
          style={{ paddingLeft }}
          onClick={() => handleSelectFile(node.file)}
          title={node.file.repo_file_path || node.cleanPath}
        >
          <span className="pgit-tree-row__main">
            <FileCode2 size={13} />
            <span className="pgit-tree-row__label">{node.name}</span>
          </span>
          {renderFileStatusTags(node.file)}
        </button>
      );
    });
  };

  return (
    <div className="pgit-workbench" data-testid="git-sources-workbench">
      {/* 仅在管理员治理/非只读消费模式下渲染顶部仓库治理控制栏 */}
      {!readOnlyConsumer && (
        <div className="pdoc-card pgit-repo-bar">
          <div className="pgit-toolbar-row">
            <div className="pgit-toolbar-group">
              {distinctSubsystems.length > 0 && (
                <select
                  className="pgit-input"
                  aria-label="筛选子系统"
                  value={subsystemFilter}
                  onChange={(e) => {
                    skipInitialTreeFetchRef.current = false;
                    setSubsystemFilter(e.target.value);
                    setSelectedRepoId(null);
                  }}
                >
                  <option value="">全部含 Proto 子系统</option>
                  {distinctSubsystems.map((sg) => (
                    <option key={sg} value={sg}>
                      {sg}
                    </option>
                  ))}
                </select>
              )}

              <label className="pgit-inline-field">
                <span>
                  <FolderGit2 size={14} /> Git 仓库
                </span>
                <select
                  className="pgit-input"
                  aria-label="选择 Git 仓库"
                  value={selectedRepoId ?? ''}
                  disabled={reposLoading}
                  onChange={(e) =>
                    handleSelectRepo(e.target.value ? Number(e.target.value) : null)
                  }
                >
                  <option value="">全部 HAS_PROTO 启用仓 ({gitRepos.length})</option>
                  {gitRepos.map((repo) => (
                    <option key={repo.id} value={repo.id}>
                      {repo.name} ({repo.service_group || repo.subsystem_slug || 'default'} ·{' '}
                      {repo.default_branch})
                    </option>
                  ))}
                </select>
              </label>

              {selectedRepo && (
                <div className="pdoc-meta-row">
                  <span className="pdoc-pill">
                    <GitBranch size={12} />{' '}
                    {selectedRepo.resolved_branch || selectedRepo.default_branch}
                  </span>
                  {selectedRepo.source_mode === 'PLATFORM_SHARED' && (
                    <span className="code-badge code-badge-info">PLATFORM_SHARED</span>
                  )}
                  {selectedRepo.sync_strategy === 'LOCAL_CACHE_ONLY' && (
                    <span className="code-badge code-badge-warning">LOCAL_CACHE_ONLY</span>
                  )}
                  {selectedRepo.is_stale && (
                    <span className="code-badge code-badge-danger">陈旧待同步</span>
                  )}
                  {renderRepoSyncBadge(selectedRepo.last_sync_status)}
                  {selectedRepo.last_sync_commit && (
                    <code className="pgit-commit-pill" title={selectedRepo.last_sync_commit}>
                      <GitCommitHorizontal size={12} />{' '}
                      {selectedRepo.last_sync_commit.slice(0, 7)}
                    </code>
                  )}
                  {selectedRepo.has_credentials && selectedRepo.credential_hint && (
                    <span className="pdoc-pill" title="AES-256-GCM 脱敏凭据指纹">
                      凭据: <code>{selectedRepo.credential_hint}</code>
                    </span>
                  )}
                </div>
              )}
            </div>

            <div className="pdoc-meta-row">
              <button
                type="button"
                className="btn btn-secondary"
                onClick={() => setConflictDrawerOpen(true)}
              >
                <Scale size={14} /> 冲突仲裁
              </button>

              {selectedRepo && (
                <button
                  type="button"
                  className="btn btn-secondary"
                  onClick={() => setConsistencyDrawerOpen(true)}
                >
                  <ShieldCheck size={14} /> 远端 SHA256 对账
                </button>
              )}

              {isAdmin && selectedRepo && (
                <button
                  type="button"
                  className="btn btn-secondary"
                  disabled={syncDispatching}
                  onClick={() => void handleReNormalize()}
                >
                  <Sparkles size={14} /> 重跑清洗
                </button>
              )}

              {isAdmin && selectedRepo && (
                <button
                  type="button"
                  className="btn btn-secondary"
                  disabled={syncDispatching}
                  onClick={() => void handleTriggerSync()}
                >
                  <RefreshCw size={14} className={syncDispatching ? 'pgit-spin' : undefined} />
                  {syncDispatching ? '派发中...' : '立即同步'}
                </button>
              )}

              {isAdmin && selectedRepo && (
                <button
                  type="button"
                  className="btn btn-secondary"
                  onClick={() => setManageDrawerOpen(true)}
                >
                  <Settings2 size={14} /> 导出与治理配置
                </button>
              )}
            </div>
          </div>

          {selectedRepo && selectedRepo.bundles && selectedRepo.bundles.length > 0 && (
            <div className="pgit-bundle-strip">
              <span className="pgit-bundle-strip__label">
                <Layers size={13} /> Monorepo Bundles：
              </span>
              <button
                type="button"
                className={`code-pill-btn ${selectedBundleId === null ? 'code-pill-btn--active' : ''}`}
                onClick={() => handleSelectBundle(null)}
              >
                全部 ({selectedRepo.bundles.length})
              </button>
              {selectedRepo.bundles.map((b) => (
                <button
                  key={b.id}
                  type="button"
                  className={`code-pill-btn ${selectedBundleId === b.id ? 'code-pill-btn--active' : ''}`}
                  onClick={() => handleSelectBundle(b.id)}
                  title={`${b.target_schema_name} (${b.subdir_path || '/'})`}
                >
                  {b.bundle_name}
                  <span className="pgit-bundle-chip-sub"> → {b.target_schema_name}</span>
                </button>
              ))}
              {selectedBundle && (
                <span className="pgit-muted">
                  目录: <code>{selectedBundle.subdir_path || '/'}</code>
                </span>
              )}
            </div>
          )}
        </div>
      )}

      {/* 经典左右分栏：左侧 clean/ 目录树 & 按包名两态导航 | 右侧精简融合源码阅读器 */}
      <div className="pgit-split-shell">
        <aside className="pgit-tree-panel" aria-label="Git 契约源码文件树">
          <div className="pgit-tree-header">
            <div className="pgit-search-box">
              <Search size={14} />
              <input
                type="text"
                className="pdoc-tree-search"
                placeholder="检索文件路径 / 包名..."
                value={searchText}
                onChange={(e) => {
                  skipInitialTreeFetchRef.current = false;
                  setSearchText(e.target.value);
                }}
              />
            </div>

            <div className="pgit-tree-mode-row">
              <div className="code-tab-pills" role="group" aria-label="目录树视图模式">
                <button
                  type="button"
                  data-testid="tree-mode-clean"
                  className={`code-pill-btn ${treeViewMode === 'clean' ? 'code-pill-btn--active' : ''}`}
                  onClick={() => {
                    skipInitialTreeFetchRef.current = false;
                    setTreeViewMode('clean');
                  }}
                >
                  目录树 (clean/)
                </button>
                <button
                  type="button"
                  data-testid="tree-mode-package"
                  className={`code-pill-btn ${treeViewMode === 'package' ? 'code-pill-btn--active' : ''}`}
                  onClick={() => {
                    skipInitialTreeFetchRef.current = false;
                    setTreeViewMode('package');
                  }}
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
                  <label className="pgit-check">
                    <input
                      type="checkbox"
                      checked={closureOnly}
                      data-testid="filter-closure-only-checkbox"
                      onChange={(e) => {
                        skipInitialTreeFetchRef.current = false;
                        setClosureOnly(e.target.checked);
                      }}
                    />
                    仅看服务闭包
                  </label>
                  <label className="pgit-check">
                    <input
                      type="checkbox"
                      checked={includeDeleted}
                      onChange={(e) => {
                        skipInitialTreeFetchRef.current = false;
                        setIncludeDeleted(e.target.checked);
                      }}
                    />
                    包含仓库已删除归档文件
                  </label>
                </div>
              </details>
            </div>
          </div>

          <div className="pgit-tree-scroll">
            {treeLoading && !treeData ? (
              <div className="pdoc-empty">正在加载契约目录树...</div>
            ) : treeViewMode === 'clean' ? (
              cleanSubsystems.length === 0 ? (
                <div className="pdoc-empty">当前条件下暂无匹配的 .proto 契约文件。</div>
              ) : (
                cleanSubsystems.map((sub) => {
                  const subOpen = expandedSubsystems.has(sub.slug);
                  return (
                    <div key={sub.key} className="pgit-tree-branch">
                      <button
                        type="button"
                        className="pgit-tree-row pgit-tree-row--subsystem"
                        onClick={() => toggleSubsystem(sub.slug)}
                      >
                        <span className="pgit-tree-row__main">
                          {subOpen ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                          {subOpen ? <FolderOpen size={14} /> : <Folder size={14} />}
                          <span className="pgit-tree-row__label">{sub.slug}</span>
                        </span>
                        <span className="pgit-tree-count">{sub.totalCount}</span>
                      </button>

                      {subOpen &&
                        sub.repos.map((repoNode) => {
                          const repoOpen =
                            expandedRepoIds.has(repoNode.repoId) ||
                            (repoNode.filesLoaded && treeData?.summary_only === false);
                          const isRepoLoading = loadingRepoId === repoNode.repoId;
                          return (
                            <div key={repoNode.key} className="pgit-tree-branch">
                              <button
                                type="button"
                                className="pgit-tree-row pgit-tree-row--repo"
                                data-testid={`tree-repo-group-${repoNode.repoId}`}
                                onClick={() =>
                                  void toggleExpandRepoGroup(
                                    repoNode.repoId,
                                    repoNode.filesLoaded
                                  )
                                }
                                title={`${sub.slug}/${repoNode.label}`}
                              >
                                <span className="pgit-tree-row__main">
                                  {repoOpen ? (
                                    <ChevronDown size={13} />
                                  ) : (
                                    <ChevronRight size={13} />
                                  )}
                                  <FolderGit2 size={13} />
                                  <span className="pgit-tree-row__label">{repoNode.label}</span>
                                </span>
                                <span className="pgit-tree-count">{repoNode.totalCount}</span>
                              </button>

                              {repoOpen && (
                                isRepoLoading && !repoNode.filesLoaded ? (
                                  <div className="pgit-tree-loading">正在加载文件...</div>
                                ) : (
                                  renderCleanNodes(repoNode.children, 2)
                                )
                              )}
                            </div>
                          );
                        })}
                    </div>
                  );
                })
              )
            ) : !treeData || treeData.view !== 'package' || treeData.groups.length === 0 ? (
              <div className="pdoc-empty">当前条件下暂无匹配的 Proto 包分组。</div>
            ) : (
              treeData.groups.map((grp) => {
                const pkgOpen =
                  expandedPackages.has(grp.package_name) ||
                  (grp.files.length > 0 && !treeData.summary_only);
                const countBadge = grp.total_count ?? grp.files.length;
                const isPkgLoading = loadingPackage === grp.package_name;
                return (
                  <div key={grp.package_name} className="pgit-tree-branch">
                    <button
                      type="button"
                      className="pgit-tree-row pgit-tree-row--repo"
                      data-testid={`tree-pkg-group-${grp.package_name}`}
                      onClick={() =>
                        void toggleExpandPackageGroup(grp.package_name, grp.files.length > 0)
                      }
                      title={grp.package_name}
                    >
                      <span className="pgit-tree-row__main">
                        {pkgOpen ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                        <Package size={13} />
                        <span className="pgit-tree-row__label">{grp.package_name}</span>
                      </span>
                      <span className="pgit-tree-count">{countBadge}</span>
                    </button>

                    {pkgOpen &&
                      (isPkgLoading && grp.files.length === 0 ? (
                        <div className="pgit-tree-loading">正在加载文件...</div>
                      ) : (
                        grp.files.map((file) => {
                          const active = file.id === selectedFileId;
                          const displayPath = resolveCleanRelativePath(file) || file.file_path;
                          return (
                            <button
                              key={file.id}
                              type="button"
                              className={`pgit-tree-row pgit-tree-row--file ${active ? 'pgit-tree-row--active' : ''}`}
                              style={{ paddingLeft: 24 }}
                              onClick={() => handleSelectFile(file)}
                              title={file.repo_file_path || displayPath}
                            >
                              <span className="pgit-tree-row__main">
                                <FileCode2 size={13} />
                                <span className="pgit-tree-row__label">{displayPath}</span>
                              </span>
                              {renderFileStatusTags(file)}
                            </button>
                          );
                        })
                      ))}
                  </div>
                );
              })
            )}
          </div>

          {readOnlyConsumer && (
            <div className="pgit-tree-footer">
              <Link
                to={pipelineGovernPath}
                className="pdoc-deep-link"
                data-testid="git-sources-pipeline-govern-link"
              >
                在流水线中治理 <ExternalLink size={12} />
              </Link>
            </div>
          )}
        </aside>

        <section className="pdoc-main-panel">
          {contentLoading ? (
            <div className="pdoc-card">
              <p className="pdoc-card__subtitle">正在读取只读契约版本快照...</p>
            </div>
          ) : !contentData && !activeFileMeta ? (
            <div className="pdoc-card">
              <p className="pdoc-card__subtitle">
                请在左侧选择任意 .proto 文件以查看规范化源码、行号锚点 (#L)、规则告警与历史 Commit Diff。
              </p>
            </div>
          ) : (
            <div className="pgit-code-card">
              {/* 合并后的单行紧凑文件头 */}
              <div className="pgit-file-header">
                <div className="pgit-file-header__left">
                  <div
                    className="pgit-breadcrumb"
                    title={activeFileMeta?.file_path || cleanRelDisplay}
                  >
                    {breadcrumbInfo.dirSegments.map((seg, idx) => (
                      <React.Fragment key={`${seg}-${idx}`}>
                        <span className="pgit-breadcrumb__seg">{seg}</span>
                        <span className="pgit-breadcrumb__sep">/</span>
                      </React.Fragment>
                    ))}
                    <strong className="pgit-breadcrumb__file">{breadcrumbInfo.fileName}</strong>
                  </div>

                  <div className="pgit-file-submeta">
                    {activeFileMeta?.package_name && (
                      <span className="pgit-file-pkg">package {activeFileMeta.package_name}</span>
                    )}
                    {activeFileMeta?.pipeline_status === 'QUARANTINED' && (
                      <span className="code-badge code-badge-danger">QUARANTINED</span>
                    )}
                    {activeFileMeta?.is_deleted && (
                      <span className="code-badge code-badge-danger">已删除</span>
                    )}
                    {activeFileMeta?.is_uservice_source && (
                      <span className="code-badge code-badge-info" data-testid="badge-uservice-converted">
                        uService 已转换
                      </span>
                    )}
                    {(activeFileMeta?.auto_fix_count ?? 0) > 0 && (
                      <span className="code-badge code-badge-success" data-testid="badge-r7-autofix">
                        R7 自动修复 ({activeFileMeta?.auto_fix_count})
                      </span>
                    )}
                    {activeFileMeta?.is_harvested_base && (
                      <span className="code-badge code-badge-neutral" data-testid="badge-r4-harvested">
                        R4 收割基座
                      </span>
                    )}
                    {activeFileMeta?.is_external_copy && (
                      <span className="code-badge code-badge-warning">外部副本</span>
                    )}
                  </div>
                </div>

                <div className="pgit-file-header__actions">
                  {versions.length > 0 && (
                    <select
                      className="pgit-input"
                      aria-label="切换历史版本"
                      value={selectedVersion ?? versions[0].version}
                      onChange={(e) => {
                        skipInitialContentFetchRef.current = false;
                        setSelectedVersion(Number(e.target.value));
                      }}
                    >
                      {versions.map((ver) => (
                        <option key={ver.version} value={ver.version}>
                          v{ver.version} · {ver.commit_sha?.slice(0, 7) || ver.version_tag || 'snap'}
                        </option>
                      ))}
                    </select>
                  )}

                  <div className="code-tab-pills" role="group" aria-label="源码视图切换">
                    <button
                      type="button"
                      className={`code-pill-btn ${
                        viewMode === 'code' && sourceStageMode === 'clean'
                          ? 'code-pill-btn--active'
                          : ''
                      }`}
                      onClick={() => {
                        setViewMode('code');
                        setSourceStageMode('clean');
                      }}
                    >
                      Clean
                    </button>
                    {hasRawVariant && (
                      <>
                        <button
                          type="button"
                          className={`code-pill-btn ${
                            viewMode === 'code' && sourceStageMode === 'raw'
                              ? 'code-pill-btn--active'
                              : ''
                          }`}
                          onClick={() => {
                            setViewMode('code');
                            setSourceStageMode('raw');
                          }}
                        >
                          Raw
                        </button>
                        <button
                          type="button"
                          className={`code-pill-btn ${
                            viewMode === 'code' && sourceStageMode === 'split'
                              ? 'code-pill-btn--active'
                              : ''
                          }`}
                          onClick={() => {
                            setViewMode('code');
                            setSourceStageMode('split');
                          }}
                        >
                          并排对比
                        </button>
                      </>
                    )}
                    <button
                      type="button"
                      className={`code-pill-btn ${viewMode === 'diff' ? 'code-pill-btn--active' : ''}`}
                      disabled={versions.length < 2}
                      title={
                        versions.length < 2
                          ? '当前文件仅存在单个快照版本'
                          : `对比版本变更 (${versions.length})`
                      }
                      onClick={() => setViewMode('diff')}
                    >
                      Diff
                    </button>
                  </div>

                  <button
                    type="button"
                    data-testid="pgit-file-info-toggle"
                    aria-expanded={infoExpanded}
                    className={`code-pill-btn ${infoExpanded ? 'code-pill-btn--active' : ''}`}
                    onClick={() => setInfoExpanded((v) => !v)}
                    title="展开或收起仓库溯源、路径映射与治理操作"
                  >
                    <Info size={13} /> 详情
                  </button>
                </div>
              </div>

              {/* 低频溯源信息与治理入口：默认收起，按需展开 */}
              {infoExpanded && (
                <div className="pgit-file-info-panel" data-testid="pgit-file-info-panel">
                  <div className="pgit-file-info-grid">
                    <div className="pgit-file-info-item">
                      <span className="pgit-file-info-label">来源仓库</span>
                      <span className="pgit-file-info-val">
                        {activeRepo?.name || activeFileMeta?.git_repo_name || '—'}
                        {activeRepo && (
                          <>
                            {' · '}
                            {activeRepo.resolved_branch || activeRepo.default_branch}
                          </>
                        )}
                      </span>
                    </div>
                    <div className="pgit-file-info-item">
                      <span className="pgit-file-info-label">提交快照</span>
                      <span className="pgit-file-info-val">
                        {contentData?.version?.commit_sha ? (
                          <code>{contentData.version.commit_sha.slice(0, 7)}</code>
                        ) : (
                          '—'
                        )}
                        {' · '}
                        {contentData?.version?.author_name || '—'}
                      </span>
                    </div>
                    <div className="pgit-file-info-item">
                      <span className="pgit-file-info-label">变更摘要</span>
                      <span className="pgit-file-info-val">
                        {contentData?.version?.commit_message ||
                          contentData?.version?.summary ||
                          '—'}
                      </span>
                    </div>
                    <div className="pgit-file-info-item">
                      <span className="pgit-file-info-label">路径映射</span>
                      <span className="pgit-file-info-val">
                        <code>{activeFileMeta?.repo_file_path || activeFileMeta?.file_path || '—'}</code>
                        {cleanRelDisplay &&
                          activeFileMeta?.repo_file_path &&
                          cleanRelDisplay !== activeFileMeta.repo_file_path && (
                            <>
                              {' → '}
                              <code>{cleanRelDisplay}</code>
                            </>
                          )}
                      </span>
                    </div>
                    <div className="pgit-file-info-item">
                      <span className="pgit-file-info-label">规范化状态</span>
                      <span className="pgit-file-info-val">
                        {activeFileMeta?.pipeline_status || 'NORMALIZED'}
                        {activeFileMeta?.normalizer_version
                          ? ` (${activeFileMeta.normalizer_version})`
                          : ''}
                      </span>
                    </div>
                    {activeRepo && (activeRepo.bundles?.length ?? 0) > 1 && (
                      <div className="pgit-file-info-item">
                        <span className="pgit-file-info-label">所属 Bundle</span>
                        <span className="pgit-file-info-val">
                          {activeFileMeta?.bundle_name || 'default'} →{' '}
                          {activeFileMeta?.target_schema_name}
                        </span>
                      </div>
                    )}
                  </div>

                  <div className="pgit-file-info-actions">
                    <span className="pgit-muted">只读权威快照 · Git-Only SSOT</span>
                    <div className="pdoc-meta-row">
                      {activeRepo && (
                        <button
                          type="button"
                          className="btn btn-secondary btn-xs"
                          onClick={() => setConsistencyDrawerOpen(true)}
                        >
                          <ShieldCheck size={13} /> 远端 SHA256 对账
                        </button>
                      )}
                      {readOnlyConsumer && (
                        <Link to={pipelineGovernPath} className="btn btn-secondary btn-xs">
                          在流水线中治理 <ExternalLink size={12} />
                        </Link>
                      )}
                    </div>
                  </div>
                </div>
              )}

              {viewMode === 'code' && contentData && (
                <ProtoReadOnlyCodeViewer
                  filePath={contentData.file.file_path}
                  cleanRelPath={contentData.file.clean_rel_path}
                  repoFilePath={contentData.file.repo_file_path}
                  content={contentData.content}
                  rawContent={contentData.raw_content}
                  lineMap={contentData.line_map}
                  pipelineStatus={contentData.file.pipeline_status}
                  statusReason={contentData.file.status_reason}
                  normalizerVersion={contentData.file.normalizer_version}
                  isExternalCopy={contentData.file.is_external_copy}
                  isUServiceSource={contentData.file.is_uservice_source}
                  isHarvestedBase={contentData.file.is_harvested_base}
                  autoFixCount={contentData.file.auto_fix_count}
                  highlightLine={highlightLine}
                  violations={normalizedViolations}
                  symbolAnchors={contentData.symbol_anchors}
                  versionLabel={`v${contentData.version.version} (${contentData.version.version_tag || contentData.version.source_type})`}
                  hasSourceInfo={contentData.file.has_source_info}
                  sourceOrigin={contentData.file.source_origin}
                  hideToolbar
                  hidePathBanner
                  stageMode={sourceStageMode}
                  onStageModeChange={setSourceStageMode}
                  className="pdoc-code-viewer--embedded"
                  onSelectLine={handleSelectLine}
                />
              )}

              {viewMode === 'diff' && (
                <div className="pgit-diff-wrap">
                  {versions.length >= 2 && (
                    <div className="pgit-diff-selectors">
                      <label className="pgit-inline-field">
                        <span>基准旧版本 (From)</span>
                        <select
                          className="pgit-input"
                          value={fromVersion}
                          onChange={(e) => setFromVersion(Number(e.target.value))}
                        >
                          {versions.map((v) => (
                            <option key={`from-${v.version}`} value={v.version}>
                              v{v.version} ({v.commit_sha?.slice(0, 7) || v.version_tag})
                            </option>
                          ))}
                        </select>
                      </label>
                      <label className="pgit-inline-field">
                        <span>目标新版本 (To)</span>
                        <select
                          className="pgit-input"
                          value={toVersion}
                          onChange={(e) => setToVersion(Number(e.target.value))}
                        >
                          {versions.map((v) => (
                            <option key={`to-${v.version}`} value={v.version}>
                              v{v.version} ({v.commit_sha?.slice(0, 7) || v.version_tag})
                            </option>
                          ))}
                        </select>
                      </label>
                    </div>
                  )}

                  {diffLoading ? (
                    <div className="pdoc-empty">正在计算版本间 Commit Diff...</div>
                  ) : diffData ? (
                    <ProtoCommitDiffView
                      filePath={diffData.file.file_path}
                      fromVersionLabel={`v${diffData.from_version.version} (${
                        diffData.from_version.commit_sha?.slice(0, 7) ||
                        diffData.from_version.version_tag ||
                        'snap'
                      })`}
                      toVersionLabel={`v${diffData.to_version.version} (${
                        diffData.to_version.commit_sha?.slice(0, 7) ||
                        diffData.to_version.version_tag ||
                        'snap'
                      })`}
                      unifiedDiff={diffData.unified_diff}
                      addedLines={diffData.added_lines}
                      deletedLines={diffData.deleted_lines}
                      commitSummary={
                        diffData.to_version.commit_message || diffData.to_version.summary
                      }
                      authorName={diffData.to_version.author_name}
                    />
                  ) : (
                    <div className="pdoc-empty">
                      当前文件仅存在单个快照版本，暂无法对比历史 Diff。
                    </div>
                  )}
                </div>
              )}
            </div>
          )}
        </section>
      </div>

      {/* 单仓导出与清洗治理抽屉 */}
      <GitRepoConfigDrawer
        open={manageDrawerOpen}
        isAdmin={isAdmin}
        repo={activeRepo ?? selectedRepo}
        onClose={() => setManageDrawerOpen(false)}
        onSaved={(savedRepo) => {
          setGitRepos((prev) => prev.map((r) => (r.id === savedRepo.id ? savedRepo : r)));
          setSelectedRepoId(savedRepo.id);
        }}
      />

      {/* Git 远端 SHA256 只读一致性对账抽屉 */}
      <GitConsistencyDrawer
        open={consistencyDrawerOpen}
        isAdmin={isAdmin}
        repo={activeRepo ?? selectedRepo}
        selectedBundleId={selectedBundleId}
        initialReport={initialConsistencyReport}
        onClose={() => setConsistencyDrawerOpen(false)}
        onTriggerSync={handleTriggerSync}
      />

      {/* ADR-26: 跨仓导入路径歧义与符号冲突仲裁抽屉 */}
      <ProtoConflictArbitrationDrawer
        open={conflictDrawerOpen}
        isAdmin={isAdmin}
        onClose={() => setConflictDrawerOpen(false)}
        onResolved={() => {
          void loadTreeSummary(treeViewMode);
        }}
      />
    </div>
  );
};

export default GitSourcesWorkbenchPane;
