import {
  resolveExportPalette,
  type TopologyExportPalette,
} from '../../components/ArchCanvas/export/exportTopology';
import type {
  ProtoSchemaFlowEdge,
  ProtoSchemaFlowNode,
} from './ProtoDependenciesGraphPane';

export type ProtoGraphExportDirection = 'LR' | 'TB';

export interface ProtoGraphBuildSvgOptions {
  padding?: number;
  palette?: TopologyExportPalette;
  title?: string;
  direction?: ProtoGraphExportDirection;
}

export interface ProtoGraphExportResult {
  svg: string;
  width: number;
  height: number;
}

export interface ProtoGraphExportActionOptions extends ProtoGraphBuildSvgOptions {
  fileNamePrefix?: string;
  scale?: number;
  documentRef?: Document;
  urlApi?: Pick<typeof URL, 'createObjectURL' | 'revokeObjectURL'>;
  createImage?: () => HTMLImageElement;
}

interface NodeBounds {
  x: number;
  y: number;
  width: number;
  height: number;
}

const DEFAULT_NODE_WIDTH = 240;
const DEFAULT_NODE_HEIGHT = 104;

const CATEGORY_TEXT_MAP: Record<string, string> = {
  BASE_LIB: '基础公共库',
  BUSINESS: '业务域服务',
  GATEWAY: '网关聚合层',
};

const SOURCE_TEXT_MAP: Record<string, string> = {
  UPLOAD_PB: '二进制 .pb',
  ONLINE_COMPILE: '源码直编',
  GIT_SYNC: 'Git 同步',
  GIT_MR_AUTO: 'Git MR 门禁',
  GIT_PIPELINE: '流水线发布',
};

function escapeXml(raw: string): string {
  return raw
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function truncateLabel(text: string, maxChars: number): string {
  if (text.length <= maxChars) return text;
  return `${text.slice(0, Math.max(1, maxChars - 1))}…`;
}

function readNodeBounds(node: ProtoSchemaFlowNode): NodeBounds {
  return {
    x: node.position?.x ?? 0,
    y: node.position?.y ?? 0,
    width: node.measured?.width ?? node.width ?? DEFAULT_NODE_WIDTH,
    height: node.measured?.height ?? node.height ?? DEFAULT_NODE_HEIGHT,
  };
}

function resolveCategoryAccent(category: string, palette: TopologyExportPalette): string {
  if (category === 'BASE_LIB') return palette.info;
  if (category === 'GATEWAY') return palette.success;
  return palette.primary;
}

function renderProtoNodeSvg(
  node: ProtoSchemaFlowNode,
  bounds: NodeBounds,
  offsetX: number,
  offsetY: number,
  palette: TopologyExportPalette
): string {
  const x = Math.round(bounds.x + offsetX);
  const y = Math.round(bounds.y + offsetY);
  const w = Math.round(bounds.width);
  const h = Math.round(bounds.height);
  const schemaNode = node.data.schemaNode;
  const accent = resolveCategoryAccent(schemaNode.category, palette);
  const nameText = truncateLabel(schemaNode.name || '', Math.max(12, Math.floor((w - 72) / 7)));
  const versionText = `v${schemaNode.version}`;
  const catLabel = CATEGORY_TEXT_MAP[schemaNode.category] || CATEGORY_TEXT_MAP.BUSINESS;
  const srcLabel = schemaNode.source_type
    ? SOURCE_TEXT_MAP[schemaNode.source_type] || schemaNode.source_type
    : '';
  const statsText = `${schemaNode.proto_file_count} 文件 / ${schemaNode.service_count} 服务 / ${schemaNode.operation_count} 方法`;
  const statusText = schemaNode.is_active ? '生效' : '草稿';
  const statusColor = schemaNode.is_active ? palette.success : palette.textMuted;

  const verPillW = Math.max(28, versionText.length * 7 + 12);
  const catPillW = Math.min(w - 24, catLabel.length * 11 + 16);

  const parts: string[] = [
    `<g data-node-id="${escapeXml(node.id)}">`,
    `<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="8" ry="8" fill="${escapeXml(
      palette.surfaceBg
    )}" stroke="${escapeXml(accent)}" stroke-width="1.6" />`,
    `<text x="${x + 12}" y="${y + 23}" font-family="system-ui, -apple-system, sans-serif" font-size="13" font-weight="600" fill="${escapeXml(
      palette.textPrimary
    )}">${escapeXml(nameText)}</text>`,
    `<rect x="${x + w - verPillW - 12}" y="${y + 9}" width="${verPillW}" height="18" rx="4" fill="${escapeXml(
      palette.mutedBg
    )}" />`,
    `<text x="${x + w - 12 - verPillW / 2}" y="${y + 21}" text-anchor="middle" font-family="system-ui, sans-serif" font-size="11" font-weight="600" fill="${escapeXml(
      palette.textSecondary
    )}">${escapeXml(versionText)}</text>`,
    `<rect x="${x + 12}" y="${y + 38}" width="${catPillW}" height="20" rx="4" fill="${escapeXml(
      palette.mutedBg
    )}" />`,
    `<text x="${x + 20}" y="${y + 52}" font-family="system-ui, sans-serif" font-size="11" font-weight="600" fill="${escapeXml(
      accent
    )}">${escapeXml(catLabel)}</text>`,
  ];

  if (srcLabel) {
    const srcX = x + 12 + catPillW + 6;
    const availW = w - (12 + catPillW + 18);
    if (availW > 42) {
      const srcPillW = Math.min(availW, srcLabel.length * 11 + 14);
      parts.push(
        `<rect x="${srcX}" y="${y + 38}" width="${srcPillW}" height="20" rx="4" fill="${escapeXml(
          palette.mutedBg
        )}" />`,
        `<text x="${srcX + 7}" y="${y + 52}" font-family="system-ui, sans-serif" font-size="11" fill="${escapeXml(
          palette.textMuted
        )}">${escapeXml(truncateLabel(srcLabel, Math.max(4, Math.floor((srcPillW - 10) / 10))))}</text>`
      );
    }
  }

  parts.push(
    `<text x="${x + 12}" y="${y + h - 14}" font-family="system-ui, sans-serif" font-size="11" fill="${escapeXml(
      palette.textSecondary
    )}">${escapeXml(statsText)}</text>`,
    `<text x="${x + w - 12}" y="${y + h - 14}" text-anchor="end" font-family="system-ui, sans-serif" font-size="11" font-weight="600" fill="${escapeXml(
      statusColor
    )}">${escapeXml(statusText)}</text>`,
    '</g>'
  );

  return parts.join('');
}

function renderProtoEdgesSvg(
  edges: ProtoSchemaFlowEdge[],
  boundsMap: Map<string, NodeBounds>,
  offsetX: number,
  offsetY: number,
  direction: ProtoGraphExportDirection,
  palette: TopologyExportPalette
): { defs: string; paths: string } {
  const markerId = 'proto-dep-arrow';
  const paths: string[] = [];

  for (const edge of edges) {
    const src = boundsMap.get(edge.source);
    const dst = boundsMap.get(edge.target);
    if (!src || !dst) continue;

    let sx: number;
    let sy: number;
    let tx: number;
    let ty: number;
    let d: string;

    if (direction === 'TB') {
      sx = src.x + src.width / 2 + offsetX;
      sy = src.y + src.height + offsetY;
      tx = dst.x + dst.width / 2 + offsetX;
      ty = dst.y + offsetY;
      const dy = Math.max(36, Math.abs(ty - sy) * 0.45);
      d = `M ${Math.round(sx)} ${Math.round(sy)} C ${Math.round(sx)} ${Math.round(sy + dy)}, ${Math.round(
        tx
      )} ${Math.round(ty - dy)}, ${Math.round(tx)} ${Math.round(ty)}`;
    } else {
      sx = src.x + src.width + offsetX;
      sy = src.y + src.height / 2 + offsetY;
      tx = dst.x + offsetX;
      ty = dst.y + dst.height / 2 + offsetY;
      const dx = Math.max(40, Math.abs(tx - sx) * 0.45);
      d = `M ${Math.round(sx)} ${Math.round(sy)} C ${Math.round(sx + dx)} ${Math.round(sy)}, ${Math.round(
        tx - dx
      )} ${Math.round(ty)}, ${Math.round(tx)} ${Math.round(ty)}`;
    }

    paths.push(
      `<path d="${d}" fill="none" stroke="${escapeXml(palette.primary)}" stroke-width="1.6" marker-end="url(#${markerId})" />`
    );

    const rawLabel = typeof edge.label === 'string' ? edge.label.trim() : '';
    if (rawLabel) {
      const mx = Math.round((sx + tx) / 2);
      const my = Math.round((sy + ty) / 2);
      const labelText = truncateLabel(rawLabel, 34);
      const pillW = Math.max(48, labelText.length * 6.8 + 14);
      paths.push(
        `<rect x="${mx - pillW / 2}" y="${my - 10}" width="${pillW}" height="18" rx="4" fill="${escapeXml(
          palette.surfaceBg
        )}" stroke="${escapeXml(palette.borderPrimary)}" stroke-width="0.8" />`,
        `<text x="${mx}" y="${my + 3}" text-anchor="middle" font-family="system-ui, sans-serif" font-size="11" fill="${escapeXml(
          palette.textSecondary
        )}">${escapeXml(labelText)}</text>`
      );
    }
  }

  const defs = `<defs><marker id="${markerId}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 1 L 10 5 L 0 9 z" fill="${escapeXml(
    palette.primary
  )}" /></marker></defs>`;

  return { defs, paths: paths.join('') };
}

/**
 * 纯矢量编程式构建跨包依赖关系图 SVG 字符串（无 foreignObject，主题色动态解析）
 */
export function buildProtoGraphSvgString(
  nodes: ProtoSchemaFlowNode[],
  edges: ProtoSchemaFlowEdge[],
  options: ProtoGraphBuildSvgOptions = {}
): ProtoGraphExportResult {
  const padding = options.padding ?? 32;
  const palette = options.palette ?? resolveExportPalette();
  const direction = options.direction ?? 'TB';

  if (nodes.length === 0) {
    const emptyW = 480;
    const emptyH = 220;
    const svg = [
      `<svg xmlns="http://www.w3.org/2000/svg" width="${emptyW}" height="${emptyH}" viewBox="0 0 ${emptyW} ${emptyH}">`,
      `<rect x="0" y="0" width="${emptyW}" height="${emptyH}" fill="${escapeXml(palette.canvasBg)}" />`,
      `<text x="${emptyW / 2}" y="${emptyH / 2}" text-anchor="middle" font-family="system-ui, sans-serif" font-size="14" fill="${escapeXml(
        palette.textSecondary
      )}">${escapeXml(options.title || '当前筛选条件下暂无契约依赖关系节点')}</text>`,
      '</svg>',
    ].join('');
    return { svg, width: emptyW, height: emptyH };
  }

  const boundsMap = new Map<string, NodeBounds>();
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;

  for (const node of nodes) {
    const b = readNodeBounds(node);
    boundsMap.set(node.id, b);
    if (b.x < minX) minX = b.x;
    if (b.y < minY) minY = b.y;
    if (b.x + b.width > maxX) maxX = b.x + b.width;
    if (b.y + b.height > maxY) maxY = b.y + b.height;
  }

  const width = Math.max(320, Math.ceil(maxX - minX + padding * 2));
  const height = Math.max(220, Math.ceil(maxY - minY + padding * 2));
  const offsetX = -minX + padding;
  const offsetY = -minY + padding;

  const { defs, paths: edgesSvg } = renderProtoEdgesSvg(
    edges,
    boundsMap,
    offsetX,
    offsetY,
    direction,
    palette
  );

  const nodesSvg = nodes
    .map((node) => {
      const b = boundsMap.get(node.id);
      return b ? renderProtoNodeSvg(node, b, offsetX, offsetY, palette) : '';
    })
    .join('');

  const svg = [
    `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}">`,
    defs,
    `<rect x="0" y="0" width="${width}" height="${height}" fill="${escapeXml(palette.canvasBg)}" />`,
    `<g data-layer="edges">${edgesSvg}</g>`,
    `<g data-layer="nodes">${nodesSvg}</g>`,
    '</svg>',
  ].join('');

  return { svg, width, height };
}

function buildExportTimestamp(): string {
  const d = new Date();
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(
    d.getMinutes()
  )}${pad(d.getSeconds())}`;
}

function triggerBlobDownload(
  blob: Blob,
  filename: string,
  documentRef: Document,
  urlApi: Pick<typeof URL, 'createObjectURL' | 'revokeObjectURL'>
): void {
  const objectUrl = urlApi.createObjectURL(blob);
  const anchor = documentRef.createElement('a');
  anchor.href = objectUrl;
  anchor.download = filename;
  documentRef.body.appendChild(anchor);
  anchor.click();
  documentRef.body.removeChild(anchor);
  urlApi.revokeObjectURL(objectUrl);
}

/**
 * 纯前端下载矢量 SVG 跨包依赖图
 */
export function exportProtoGraphAsSvg(
  nodes: ProtoSchemaFlowNode[],
  edges: ProtoSchemaFlowEdge[],
  options: ProtoGraphExportActionOptions = {}
): ProtoGraphExportResult {
  const doc = options.documentRef ?? document;
  const urlApi = options.urlApi ?? URL;
  const result = buildProtoGraphSvgString(nodes, edges, options);
  const blob = new Blob([result.svg], { type: 'image/svg+xml;charset=utf-8' });
  const prefix = options.fileNamePrefix || 'proto-dependencies-graph';
  const filename = `${prefix}-${buildExportTimestamp()}.svg`;
  triggerBlobDownload(blob, filename, doc, urlApi);
  return result;
}

/**
 * 将跨包依赖图 SVG 通过 2x 离屏 Canvas 光栅化并本地下载 PNG
 */
export async function exportProtoGraphAsPng(
  nodes: ProtoSchemaFlowNode[],
  edges: ProtoSchemaFlowEdge[],
  options: ProtoGraphExportActionOptions = {}
): Promise<ProtoGraphExportResult> {
  const doc = options.documentRef ?? document;
  const urlApi = options.urlApi ?? URL;
  const scale = options.scale ?? 2;
  const result = buildProtoGraphSvgString(nodes, edges, options);
  const svgBlob = new Blob([result.svg], { type: 'image/svg+xml;charset=utf-8' });
  const svgObjectUrl = urlApi.createObjectURL(svgBlob);

  try {
    const pngBlob = await new Promise<Blob>((resolve, reject) => {
      const img = options.createImage ? options.createImage() : new Image();
      img.onload = () => {
        const canvas = doc.createElement('canvas');
        canvas.width = Math.max(1, Math.round(result.width * scale));
        canvas.height = Math.max(1, Math.round(result.height * scale));
        const ctx = canvas.getContext('2d');
        if (!ctx) {
          reject(new Error('无法创建 2D 离屏画布上下文'));
          return;
        }
        ctx.scale(scale, scale);
        ctx.drawImage(img, 0, 0);
        canvas.toBlob((blob) => {
          if (!blob) {
            reject(new Error('离屏 Canvas PNG 编码失败'));
            return;
          }
          resolve(blob);
        }, 'image/png');
      };
      img.onerror = () => {
        reject(new Error('SVG 矢量图加载失败，无法转为 PNG'));
      };
      img.src = svgObjectUrl;
    });

    const prefix = options.fileNamePrefix || 'proto-dependencies-graph';
    const filename = `${prefix}-${buildExportTimestamp()}.png`;
    triggerBlobDownload(pngBlob, filename, doc, urlApi);
    return result;
  } finally {
    urlApi.revokeObjectURL(svgObjectUrl);
  }
}
