import type {
  GitRepoDTO,
  ProtoFileDTO,
  ProtoFilesTreeResponse,
  ProtoTreeRepoGroupDTO,
} from '../../types';

export interface CleanFileNode {
  kind: 'file';
  key: string;
  name: string;
  cleanPath: string;
  file: ProtoFileDTO;
}

export interface CleanFolderNode {
  kind: 'folder';
  key: string;
  label: string;
  children: Array<CleanFolderNode | CleanFileNode>;
  fileCount: number;
}

export interface CleanRepoNode {
  kind: 'repo';
  key: string;
  repoId: number;
  label: string;
  subsystemSlug: string;
  group: ProtoTreeRepoGroupDTO;
  totalCount: number;
  quarantinedCount: number;
  filesLoaded: boolean;
  children: Array<CleanFolderNode | CleanFileNode>;
}

export interface CleanSubsystemNode {
  kind: 'subsystem';
  key: string;
  slug: string;
  repos: CleanRepoNode[];
  totalCount: number;
  quarantinedCount: number;
}

interface RawFolderBucket {
  name: string;
  fullPath: string;
  subFolders: Map<string, RawFolderBucket>;
  files: CleanFileNode[];
}

export function resolveCleanRelativePath(file: ProtoFileDTO): string {
  return (file.clean_rel_path || file.file_path || file.repo_file_path || '')
    .replace(/^\/+|\/+$/g, '');
}

function sortTreeNodes(
  nodes: Array<CleanFolderNode | CleanFileNode>
): Array<CleanFolderNode | CleanFileNode> {
  return nodes.sort((a, b) => {
    if (a.kind !== b.kind) {
      return a.kind === 'folder' ? -1 : 1;
    }
    if (a.kind === 'folder' && b.kind === 'folder') {
      return a.label.localeCompare(b.label);
    }
    if (a.kind === 'file' && b.kind === 'file') {
      return a.name.localeCompare(b.name);
    }
    return 0;
  });
}

function finalizeFolderBucket(
  bucket: RawFolderBucket,
  repoId: number
): CleanFolderNode {
  let curr = bucket;
  const labelParts = [curr.name];

  // 单子目录路径压缩：当前目录无直属文件且仅有一个子目录时合并展示
  while (curr.files.length === 0 && curr.subFolders.size === 1) {
    const nextBucket = curr.subFolders.values().next().value;
    if (!nextBucket) break;
    curr = nextBucket;
    labelParts.push(curr.name);
  }

  const children: Array<CleanFolderNode | CleanFileNode> = [];
  for (const sub of curr.subFolders.values()) {
    children.push(finalizeFolderBucket(sub, repoId));
  }
  for (const f of curr.files) {
    children.push(f);
  }

  sortTreeNodes(children);
  const fileCount = children.reduce(
    (sum, node) => sum + (node.kind === 'folder' ? node.fileCount : 1),
    0
  );

  return {
    kind: 'folder',
    key: `${repoId}:${curr.fullPath}`,
    label: labelParts.join('/'),
    children,
    fileCount,
  };
}

/**
 * 将单仓内的 .proto 文件按 clean_rel_path 构建为可压缩单子目录的层级目录树。
 */
export function buildCleanDirTree(
  files: ProtoFileDTO[],
  repoId: number
): Array<CleanFolderNode | CleanFileNode> {
  const rootFolders = new Map<string, RawFolderBucket>();
  const rootFiles: CleanFileNode[] = [];

  for (const file of files) {
    const cleanPath = resolveCleanRelativePath(file);
    const segments = cleanPath.split('/').filter(Boolean);
    const fileName = segments.length > 0 ? segments[segments.length - 1] : `file-${file.id}.proto`;
    const fileNode: CleanFileNode = {
      kind: 'file',
      key: `file:${file.id}`,
      name: fileName,
      cleanPath: cleanPath || fileName,
      file,
    };

    if (segments.length <= 1) {
      rootFiles.push(fileNode);
      continue;
    }

    let currentMap = rootFolders;
    let currentBucket: RawFolderBucket | undefined;
    let accPath = '';

    for (let i = 0; i < segments.length - 1; i += 1) {
      const seg = segments[i];
      accPath = accPath ? `${accPath}/${seg}` : seg;
      let next = currentMap.get(seg);
      if (!next) {
        next = {
          name: seg,
          fullPath: accPath,
          subFolders: new Map(),
          files: [],
        };
        currentMap.set(seg, next);
      }
      currentBucket = next;
      currentMap = next.subFolders;
    }

    if (currentBucket) {
      currentBucket.files.push(fileNode);
    } else {
      rootFiles.push(fileNode);
    }
  }

  const result: Array<CleanFolderNode | CleanFileNode> = [];
  for (const folder of rootFolders.values()) {
    result.push(finalizeFolderBucket(folder, repoId));
  }
  for (const f of rootFiles) {
    result.push(f);
  }

  return sortTreeNodes(result);
}

/**
 * 将后端 /api/proto-files/tree?view=subsystem 的仓库分组聚合成「子系统 -> 仓库 -> clean/ 目录树」。
 */
export function buildCleanSubsystemTree(
  groups: ProtoTreeRepoGroupDTO[]
): CleanSubsystemNode[] {
  const subsystemMap = new Map<string, CleanSubsystemNode>();

  for (const grp of groups) {
    const slug = (grp.subsystem_slug || '').trim() || '_unassigned';
    let subNode = subsystemMap.get(slug);
    if (!subNode) {
      subNode = {
        kind: 'subsystem',
        key: `sub:${slug}`,
        slug,
        repos: [],
        totalCount: 0,
        quarantinedCount: 0,
      };
      subsystemMap.set(slug, subNode);
    }

    const totalCount = grp.total_count ?? grp.files.length;
    const quarantinedCount =
      grp.quarantined_count ??
      grp.files.filter((f) => f.pipeline_status === 'QUARANTINED').length;
    const filesLoaded = grp.files.length > 0;
    const repoLabel =
      (grp.export_target_repo_dir || '').trim() ||
      (grp.git_repo_name || '').trim() ||
      `repo-${grp.git_repo_id}`;

    subNode.repos.push({
      kind: 'repo',
      key: `repo:${grp.git_repo_id}`,
      repoId: grp.git_repo_id,
      label: repoLabel,
      subsystemSlug: slug,
      group: grp,
      totalCount,
      quarantinedCount,
      filesLoaded,
      children: filesLoaded ? buildCleanDirTree(grp.files, grp.git_repo_id) : [],
    });

    subNode.totalCount += totalCount;
    subNode.quarantinedCount += quarantinedCount;
  }

  const subsystems = Array.from(subsystemMap.values());
  subsystems.sort((a, b) => {
    if (a.slug === '_unassigned' && b.slug !== '_unassigned') return 1;
    if (b.slug === '_unassigned' && a.slug !== '_unassigned') return -1;
    return a.slug.localeCompare(b.slug);
  });

  for (const sub of subsystems) {
    sub.repos.sort((a, b) => a.label.localeCompare(b.label));
  }

  return subsystems;
}

/**
 * 为 SSR / 单元测试仅注入 initialFiles 时的兼容兜底树构建函数。
 */
export function buildFallbackSubsystemTree(
  files: ProtoFileDTO[],
  repos: GitRepoDTO[]
): ProtoFilesTreeResponse {
  const repoById = new Map<number, GitRepoDTO>();
  for (const r of repos) {
    repoById.set(r.id, r);
  }

  const grouped = new Map<number, ProtoTreeRepoGroupDTO>();
  for (const file of files) {
    const rid = file.git_repo_id ?? 0;
    let grp = grouped.get(rid);
    if (!grp) {
      const repo = repoById.get(rid);
      const subSlug =
        repo?.export_target_subsystem ||
        repo?.subsystem_slug ||
        repo?.service_group ||
        '_unassigned';
      const repoDir =
        repo?.export_target_repo_dir ||
        repo?.name ||
        file.git_repo_name ||
        (rid > 0 ? `repo-${rid}` : 'default');
      grp = {
        subsystem_slug: subSlug,
        export_target_repo_dir: repoDir,
        git_repo_id: rid,
        git_repo_name: repo?.name || file.git_repo_name || repoDir,
        source_mode: repo?.source_mode || 'PLATFORM_SHARED',
        sync_strategy: repo?.sync_strategy || 'LOCAL_CACHE_ONLY',
        is_stale: Boolean(repo?.is_stale),
        last_sync_at: repo?.last_sync_at ?? null,
        total_count: 0,
        normalized_count: 0,
        quarantined_count: 0,
        files: [],
      };
      grouped.set(rid, grp);
    }

    grp.files.push(file);
    grp.total_count = (grp.total_count ?? 0) + 1;
    if (file.pipeline_status === 'NORMALIZED') {
      grp.normalized_count = (grp.normalized_count ?? 0) + 1;
    } else if (file.pipeline_status === 'QUARANTINED') {
      grp.quarantined_count = (grp.quarantined_count ?? 0) + 1;
    }
  }

  return {
    view: 'subsystem',
    total: files.length,
    summary_only: false,
    groups: Array.from(grouped.values()),
  };
}
