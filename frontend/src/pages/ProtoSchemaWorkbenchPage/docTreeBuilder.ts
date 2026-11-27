import type { ProtoDocTreeNodeDTO } from '../../types';

export type DocTreeViewMode = 'subsystem' | 'package';

export interface DocServiceLeaf {
  key: string;
  label: string;
  serviceName: string;
  packageName: string;
  schemaId?: number;
  schemaName?: string;
  category?: string;
  summary?: string;
  isDeprecated: boolean;
  methodCount: number;
}

export interface DocPackageBranch {
  key: string;
  packageName: string;
  services: DocServiceLeaf[];
  serviceCount: number;
  methodCount: number;
}

export interface DocSchemaBranch {
  key: string;
  label: string;
  schemaId?: number;
  schemaName?: string;
  category?: string;
  packages: DocPackageBranch[];
  /** 当制品仅包含单个 Package 时，直接挂接该包下的服务以消除多余层级；多包时为 null */
  directServices: DocServiceLeaf[] | null;
  singlePackageName?: string;
  serviceCount: number;
  methodCount: number;
}

export interface DocSubsystemBranch {
  key: string;
  label: string;
  subsystemId?: number;
  schemas: DocSchemaBranch[];
  serviceCount: number;
  methodCount: number;
}

function inferPackageFromServiceName(serviceName?: string): string {
  const trimmed = (serviceName || '').trim();
  if (!trimmed) return 'default';
  const lastDot = trimmed.lastIndexOf('.');
  if (lastDot <= 0) return 'default';
  return trimmed.slice(0, lastDot);
}

function toServiceLeaf(
  node: ProtoDocTreeNodeDTO,
  fallbackPkg: string,
  fallbackSchema?: { schemaId?: number; schemaName?: string; category?: string }
): DocServiceLeaf | null {
  const serviceName = (node.service_name || '').trim();
  if (!serviceName) return null;
  const packageName = fallbackPkg || inferPackageFromServiceName(serviceName);
  return {
    key: node.node_key || `svc:${node.schema_id ?? fallbackSchema?.schemaId ?? 0}:${serviceName}`,
    label: (node.label || '').trim() || serviceName,
    serviceName,
    packageName,
    schemaId: node.schema_id ?? fallbackSchema?.schemaId,
    schemaName: node.schema_name || fallbackSchema?.schemaName,
    category: node.category || fallbackSchema?.category,
    summary: node.summary,
    isDeprecated: Boolean(node.is_deprecated),
    methodCount: node.method_count ?? 0,
  };
}

function collectServicesFromNode(
  node: ProtoDocTreeNodeDTO,
  pkgHint: string,
  schemaHint: { schemaId?: number; schemaName?: string; category?: string },
  out: DocServiceLeaf[]
): void {
  if (node.node_type === 'SERVICE') {
    const leaf = toServiceLeaf(node, pkgHint, schemaHint);
    if (leaf) out.push(leaf);
    return;
  }
  if (!node.children || node.children.length === 0) return;

  const nextPkg = node.node_type === 'PACKAGE' ? node.label || pkgHint : pkgHint;
  const nextSchema =
    node.node_type === 'SCHEMA'
      ? {
          schemaId: node.schema_id ?? schemaHint.schemaId,
          schemaName: node.schema_name || schemaHint.schemaName,
          category: node.category || schemaHint.category,
        }
      : schemaHint;

  for (const child of node.children) {
    collectServicesFromNode(child, nextPkg, nextSchema, out);
  }
}

/**
 * 将后端 /api/proto-docs/tree 数据规整为「子系统 -> 制品(Schema) -> (多包时 Package) -> Service」树。
 * 自动过滤不含任何 Service 的空节点，并在单 Package 制品下压缩中间层以减少视觉噪点。
 */
export function buildDocSubsystemTree(items: ProtoDocTreeNodeDTO[]): DocSubsystemBranch[] {
  const subsystems: DocSubsystemBranch[] = [];

  for (const subNode of items) {
    if (subNode.node_type !== 'SUBSYSTEM') continue;
    const schemas: DocSchemaBranch[] = [];

    for (const schemaNode of subNode.children ?? []) {
      if (schemaNode.node_type !== 'SCHEMA') continue;

      const schemaHint = {
        schemaId: schemaNode.schema_id,
        schemaName: schemaNode.schema_name,
        category: schemaNode.category,
      };
      const packages: DocPackageBranch[] = [];
      const directLooseLeaves: DocServiceLeaf[] = [];

      for (const child of schemaNode.children ?? []) {
        if (child.node_type === 'PACKAGE') {
          const pkgName = (child.label || '').trim() || 'default';
          const pkgServices: DocServiceLeaf[] = [];
          for (const grandChild of child.children ?? []) {
            collectServicesFromNode(grandChild, pkgName, schemaHint, pkgServices);
          }
          if (pkgServices.length === 0) continue;
          const pkgMethodCount = pkgServices.reduce((sum, s) => sum + s.methodCount, 0);
          packages.push({
            key: child.node_key || `pkg:${schemaNode.schema_id ?? 0}:${pkgName}`,
            packageName: pkgName,
            services: pkgServices,
            serviceCount: pkgServices.length,
            methodCount: pkgMethodCount,
          });
        } else {
          collectServicesFromNode(child, '', schemaHint, directLooseLeaves);
        }
      }

      if (directLooseLeaves.length > 0) {
        const byPkg = new Map<string, DocServiceLeaf[]>();
        for (const leaf of directLooseLeaves) {
          const list = byPkg.get(leaf.packageName) ?? [];
          list.push(leaf);
          byPkg.set(leaf.packageName, list);
        }
        for (const [pkgName, list] of byPkg.entries()) {
          packages.push({
            key: `pkg:${schemaNode.schema_id ?? 0}:${pkgName}`,
            packageName: pkgName,
            services: list,
            serviceCount: list.length,
            methodCount: list.reduce((sum, s) => sum + s.methodCount, 0),
          });
        }
      }

      if (packages.length === 0) continue;

      const serviceCount = packages.reduce((sum, p) => sum + p.serviceCount, 0);
      const methodCount = packages.reduce((sum, p) => sum + p.methodCount, 0);
      const isSinglePackage = packages.length === 1;

      schemas.push({
        key: schemaNode.node_key,
        label: schemaNode.label,
        schemaId: schemaNode.schema_id,
        schemaName: schemaNode.schema_name,
        category: schemaNode.category,
        packages,
        directServices: isSinglePackage ? packages[0].services : null,
        singlePackageName: isSinglePackage ? packages[0].packageName : undefined,
        serviceCount,
        methodCount,
      });
    }

    if (schemas.length === 0) continue;

    subsystems.push({
      key: subNode.node_key,
      label: subNode.label,
      subsystemId: subNode.subsystem_id,
      schemas,
      serviceCount: schemas.reduce((sum, s) => sum + s.serviceCount, 0),
      methodCount: schemas.reduce((sum, s) => sum + s.methodCount, 0),
    });
  }

  return subsystems;
}

/**
 * 跨子系统与制品提取全部 Service，按 Proto package 聚合排序，用于「按包名」视图。
 */
export function buildDocPackageTree(items: ProtoDocTreeNodeDTO[]): DocPackageBranch[] {
  const pkgMap = new Map<string, Map<string, DocServiceLeaf>>();

  for (const root of items) {
    const collected: DocServiceLeaf[] = [];
    collectServicesFromNode(root, '', {}, collected);
    for (const leaf of collected) {
      const pkgName = leaf.packageName || inferPackageFromServiceName(leaf.serviceName);
      let svcMap = pkgMap.get(pkgName);
      if (!svcMap) {
        svcMap = new Map<string, DocServiceLeaf>();
        pkgMap.set(pkgName, svcMap);
      }
      const dedupKey = `${leaf.schemaId ?? 0}::${leaf.serviceName}`;
      if (!svcMap.has(dedupKey)) {
        svcMap.set(dedupKey, { ...leaf, packageName: pkgName });
      }
    }
  }

  const branches: DocPackageBranch[] = [];
  for (const [packageName, svcMap] of pkgMap.entries()) {
    const services = Array.from(svcMap.values()).sort((a, b) =>
      a.label.localeCompare(b.label)
    );
    branches.push({
      key: `doc-pkg:${packageName}`,
      packageName,
      services,
      serviceCount: services.length,
      methodCount: services.reduce((sum, s) => sum + s.methodCount, 0),
    });
  }

  branches.sort((a, b) => a.packageName.localeCompare(b.packageName));
  return branches;
}

/**
 * 定位指定服务在「按子系统」树中的全部祖先折叠 key，便于选中或深链跳转时自动展开所在分支。
 */
export function findAncestorKeysForService(
  subsystems: DocSubsystemBranch[],
  serviceName: string,
  schemaId?: number | string
): string[] {
  const targetSvc = serviceName.trim();
  if (!targetSvc) return [];
  const targetSchemaId =
    schemaId !== undefined && schemaId !== '' && Number.isFinite(Number(schemaId))
      ? Number(schemaId)
      : undefined;

  for (const sub of subsystems) {
    for (const schema of sub.schemas) {
      for (const pkg of schema.packages) {
        const hit = pkg.services.find(
          (s) =>
            s.serviceName === targetSvc &&
            (targetSchemaId === undefined || s.schemaId === targetSchemaId)
        );
        if (hit) {
          const keys = [sub.key, schema.key];
          if (schema.directServices === null) {
            keys.push(pkg.key);
          }
          return keys;
        }
      }
    }
  }

  // 若带 schemaId 未精确匹配，降级仅按 serviceName 匹配一次
  if (targetSchemaId !== undefined) {
    return findAncestorKeysForService(subsystems, targetSvc, undefined);
  }
  return [];
}
