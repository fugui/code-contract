import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import ELK from 'elkjs/lib/elk.bundled.js';
import {
  Background,
  BackgroundVariant,
  Handle,
  MarkerType,
  MiniMap,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeProps,
  type ReactFlowInstance,
  type Viewport,
} from '@xyflow/react';
import {
  ArrowDownUp,
  ArrowLeftRight,
  Download,
  FileCode2,
  Map as MapIcon,
  Maximize2,
  Minimize2,
  RefreshCw,
  RotateCcw,
  ZoomIn,
  ZoomOut,
} from 'lucide-react';
import { Modal } from '@code/common';
import * as schemaApi from '../../api/schema';
import { useToast } from '../../utils/toast';
import type { ProtoCategory, ProtoDependenciesGraphDTO, ProtoDependencyNodeDTO } from '../../types';
import { exportProtoGraphAsPng, exportProtoGraphAsSvg } from './exportProtoGraph';

export type ProtoLayoutDirection = 'TB' | 'LR';

export interface ProtoGraphNodeData extends Record<string, unknown> {
  schemaNode: ProtoDependencyNodeDTO;
  isSelected?: boolean;
  isHighlighted?: boolean;
  isDimmed?: boolean;
  layoutDirection?: ProtoLayoutDirection;
}

export interface ProtoGraphEdgeData extends Record<string, unknown> {
  matchedProtos?: string[];
}

export type ProtoSchemaFlowNode = Node<ProtoGraphNodeData, 'protoSchemaNode'>;
export type ProtoSchemaFlowEdge = Edge<ProtoGraphEdgeData>;

export type ProtoCategoryFilter = 'ALL' | ProtoCategory;

export interface SchemaNodeRelationItem {
  node: ProtoDependencyNodeDTO;
  matchedProtos: string[];
}

export interface SchemaNodeRelations {
  upstream: SchemaNodeRelationItem[];
  downstream: SchemaNodeRelationItem[];
}

export interface GraphHighlightState {
  activeNodeIds: Set<string>;
  activeEdgeIds: Set<string>;
}

export interface ProtoDependenciesGraphPaneProps {
  initialGraph?: ProtoDependenciesGraphDTO;
  onNavigateToSchemaAsset?: (schemaId: string | number) => void;
  onSelectNode?: (node: ProtoDependencyNodeDTO, graph: ProtoDependenciesGraphDTO) => void;
  selectedSchemaId?: string | number | null;
}

const NODE_WIDTH = 240;
const NODE_HEIGHT = 104;
const DEFAULT_FIT_PADDING = 0.14;

const elkInstance = new ELK();

export const CATEGORY_META: Record<
  ProtoCategory,
  { label: string; icon: string; badgeBg: string; badgeColor: string; borderColor: string; modifierClass: string }
> = {
  BASE_LIB: {
    label: '基础公共库',
    icon: '🧱',
    badgeBg: 'var(--color-bg-muted)',
    badgeColor: 'var(--color-info, var(--color-primary))',
    borderColor: 'var(--color-info, var(--color-primary))',
    modifierClass: 'code-proto-graph-node--base-lib',
  },
  BUSINESS: {
    label: '业务域服务',
    icon: '📦',
    badgeBg: 'var(--color-bg-muted)',
    badgeColor: 'var(--color-primary)',
    borderColor: 'var(--color-primary)',
    modifierClass: 'code-proto-graph-node--business',
  },
  GATEWAY: {
    label: '网关聚合层',
    icon: '🌐',
    badgeBg: 'var(--color-bg-muted)',
    badgeColor: 'var(--color-success)',
    borderColor: 'var(--color-success)',
    modifierClass: 'code-proto-graph-node--gateway',
  },
};

export const SOURCE_TYPE_LABEL: Record<string, string> = {
  UPLOAD_PB: '二进制 .pb',
  ONLINE_COMPILE: '源码直编',
  GIT_SYNC: 'Git 同步',
  GIT_MR_AUTO: 'Git MR 门禁',
  GIT_PIPELINE: '流水线发布',
};

/**
 * 格式化依赖边上的 proto 引用标签；常规态仅显示文件名防重叠，高亮态展示完整路径
 */
export function formatEdgeProtoLabel(
  matchedProtos: string[] | undefined,
  verbose = false
): string {
  if (!matchedProtos || matchedProtos.length === 0) {
    return 'import';
  }
  const first = matchedProtos[0];
  let display = first;
  if (!verbose) {
    const parts = first.split('/');
    display = parts[parts.length - 1] || first;
  }
  return matchedProtos.length === 1 ? display : `${display} (+${matchedProtos.length - 1})`;
}

/**
 * 计算指定焦点节点的 1-hop 上下游高亮节点与关联边集合
 */
export function computeGraphHighlightSet(
  graph: ProtoDependenciesGraphDTO,
  activeSchemaId: string | number | null | undefined
): GraphHighlightState | null {
  if (activeSchemaId === null || activeSchemaId === undefined || activeSchemaId === '') {
    return null;
  }
  const targetId = String(activeSchemaId);
  const activeNodeIds = new Set<string>([`schema_${targetId}`]);
  const activeEdgeIds = new Set<string>();

  (graph.edges || []).forEach((edge, idx) => {
    const fromId = String(edge.from_schema_id);
    const toId = String(edge.to_schema_id);
    if (fromId === targetId || toId === targetId) {
      activeNodeIds.add(`schema_${fromId}`);
      activeNodeIds.add(`schema_${toId}`);
      activeEdgeIds.add(`edge_${edge.from_schema_id}_${edge.to_schema_id}_${idx}`);
    }
  });

  return { activeNodeIds, activeEdgeIds };
}

/**
 * 按描述集分类过滤节点，并仅保留两端均位于可见集合内的依赖有向边
 */
export function filterProtoGraphByCategory(
  graph: ProtoDependenciesGraphDTO,
  category: ProtoCategoryFilter
): ProtoDependenciesGraphDTO {
  const nodes = graph.nodes || [];
  const edges = graph.edges || [];
  const visibleNodes = category === 'ALL' ? nodes : nodes.filter((n) => n.category === category);
  const visibleIdSet = new Set(visibleNodes.map((n) => String(n.id)));
  const visibleEdges = edges.filter(
    (e) => visibleIdSet.has(String(e.from_schema_id)) && visibleIdSet.has(String(e.to_schema_id))
  );
  return {
    pool_summary: graph.pool_summary,
    nodes: visibleNodes,
    edges: visibleEdges,
  };
}

/**
 * 从图节点 ID (schema_123) 解析供 ?tab=assets&schema_id=X 使用的导航参数
 */
export function getSchemaNodeNavigationTarget(nodeId: string): { tab: 'assets'; schema_id: string } {
  const schemaId = nodeId.startsWith('schema_') ? nodeId.slice('schema_'.length) : nodeId;
  return { tab: 'assets', schema_id: schemaId };
}

/**
 * 计算指定契约包节点在跨包依赖图中的上游（本包引用）与下游（被谁引用）关系
 */
export function computeSchemaNodeRelations(
  graph: ProtoDependenciesGraphDTO,
  schemaId: string | number
): SchemaNodeRelations {
  const nodes = graph.nodes || [];
  const edges = graph.edges || [];
  const targetId = String(schemaId);
  const nodeMap = new Map<string, ProtoDependencyNodeDTO>();
  for (const n of nodes) {
    nodeMap.set(String(n.id), n);
  }

  const upstream: SchemaNodeRelationItem[] = [];
  const downstream: SchemaNodeRelationItem[] = [];

  for (const edge of edges) {
    const fromId = String(edge.from_schema_id);
    const toId = String(edge.to_schema_id);
    const matchedProtos = edge.matched_protos || [];
    if (fromId === targetId) {
      const targetNode = nodeMap.get(toId);
      if (targetNode) {
        upstream.push({ node: targetNode, matchedProtos });
      }
    }
    if (toId === targetId) {
      const sourceNode = nodeMap.get(fromId);
      if (sourceNode) {
        downstream.push({ node: sourceNode, matchedProtos });
      }
    }
  }

  return { upstream, downstream };
}

/**
 * 使用既有 ELK 引擎对 Proto 描述集跨包 import 有向图执行分栏层次布局
 */
export async function layoutProtoDependenciesGraph(
  graph: ProtoDependenciesGraphDTO,
  selectedSchemaId?: string | number | null,
  direction: ProtoLayoutDirection = 'TB'
): Promise<{ nodes: ProtoSchemaFlowNode[]; edges: ProtoSchemaFlowEdge[] }> {
  const rawNodes = graph.nodes || [];
  const rawEdges = graph.edges || [];

  if (rawNodes.length === 0) {
    return { nodes: [], edges: [] };
  }

  const validIds = new Set(rawNodes.map((n) => `schema_${n.id}`));
  const elkEdges = rawEdges
    .map((e, idx) => ({
      id: `p_edge_${e.from_schema_id}_${e.to_schema_id}_${idx}`,
      sources: [`schema_${e.from_schema_id}`],
      targets: [`schema_${e.to_schema_id}`],
    }))
    .filter((e) => validIds.has(e.sources[0]) && validIds.has(e.targets[0]));

  const positionMap = new Map<string, { x: number; y: number }>();

  try {
    const layouted = await elkInstance.layout({
      id: 'proto-graph-root',
      layoutOptions: {
        'elk.algorithm': 'layered',
        'elk.direction': direction === 'TB' ? 'DOWN' : 'RIGHT',
        'elk.aspectRatio': '2.2',
        'elk.spacing.nodeNode': direction === 'TB' ? '48' : '44',
        'elk.spacing.componentComponent': '64',
        'elk.layered.spacing.nodeNodeBetweenLayers': direction === 'TB' ? '120' : '168',
        'elk.layered.nodePlacement.strategy': 'NETWORK_SIMPLEX',
        'elk.edgeRouting': 'SPLINES',
      },
      children: rawNodes.map((n) => ({
        id: `schema_${n.id}`,
        width: NODE_WIDTH,
        height: NODE_HEIGHT,
      })),
      edges: elkEdges,
    });

    for (const child of layouted.children || []) {
      positionMap.set(child.id, {
        x: Number.isFinite(child.x) ? Number(child.x) : 0,
        y: Number.isFinite(child.y) ? Number(child.y) : 0,
      });
    }
  } catch {
    rawNodes.forEach((n, index) => {
      positionMap.set(`schema_${n.id}`, {
        x: (index % 3) * (NODE_WIDTH + 60),
        y: Math.floor(index / 3) * (NODE_HEIGHT + 56),
      });
    });
  }

  const selectedKey =
    selectedSchemaId !== undefined && selectedSchemaId !== null ? String(selectedSchemaId) : null;

  const flowNodes: ProtoSchemaFlowNode[] = rawNodes.map((n, index) => {
    const id = `schema_${n.id}`;
    const pos = positionMap.get(id) ?? {
      x: (index % 3) * (NODE_WIDTH + 60),
      y: Math.floor(index / 3) * (NODE_HEIGHT + 56),
    };
    return {
      id,
      type: 'protoSchemaNode',
      position: pos,
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
      data: {
        schemaNode: n,
        isSelected: selectedKey !== null && String(n.id) === selectedKey,
        layoutDirection: direction,
      },
    };
  });

  const flowEdges: ProtoSchemaFlowEdge[] = rawEdges.map((e, idx) => {
    const matchedProtos = e.matched_protos || [];
    return {
      id: `edge_${e.from_schema_id}_${e.to_schema_id}_${idx}`,
      source: `schema_${e.from_schema_id}`,
      target: `schema_${e.to_schema_id}`,
      label: formatEdgeProtoLabel(matchedProtos, false),
      data: { matchedProtos },
      animated: false,
      markerEnd: {
        type: MarkerType.ArrowClosed,
        color: 'var(--color-primary)',
      },
      style: {
        stroke: 'var(--color-primary)',
        strokeWidth: 1.6,
      },
      labelStyle: {
        fontSize: 11,
        fill: 'var(--color-text-secondary)',
      },
      labelBgStyle: {
        fill: 'var(--color-bg-surface)',
      },
    };
  });

  return { nodes: flowNodes, edges: flowEdges };
}

const ProtoSchemaGraphNodeCard: React.FC<NodeProps<ProtoSchemaFlowNode>> = ({ data }) => {
  const { schemaNode, isSelected, isHighlighted, isDimmed, layoutDirection = 'TB' } = data;
  const catMeta = CATEGORY_META[schemaNode.category] || CATEGORY_META.BUSINESS;
  const srcLabel = schemaNode.source_type
    ? SOURCE_TYPE_LABEL[schemaNode.source_type] || schemaNode.source_type
    : '';

  const nodeClasses = [
    'code-proto-graph-node',
    catMeta.modifierClass,
    isSelected ? 'code-proto-graph-node--selected' : '',
    !isSelected && isHighlighted ? 'code-proto-graph-node--highlighted' : '',
    isDimmed ? 'code-proto-graph-node--dimmed' : '',
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div className={nodeClasses} data-testid={`proto-graph-node-${schemaNode.id}`}>
      <Handle
        type="target"
        position={layoutDirection === 'TB' ? Position.Top : Position.Left}
      />
      <Handle
        type="source"
        position={layoutDirection === 'TB' ? Position.Bottom : Position.Right}
      />

      <div className="code-proto-graph-node__top">
        <span className="code-proto-graph-node__name" title={schemaNode.name}>
          {schemaNode.name}
        </span>
        <span className="code-proto-graph-node__version">v{schemaNode.version}</span>
      </div>

      <div className="code-proto-graph-node__badges">
        <span className="code-proto-graph-node__cat">
          {catMeta.icon} {catMeta.label}
        </span>
        {srcLabel && <span className="code-proto-graph-node__src">{srcLabel}</span>}
      </div>

      <div className="code-proto-graph-node__footer">
        <span>
          {schemaNode.proto_file_count} 文件 / {schemaNode.service_count} 服务 /{' '}
          {schemaNode.operation_count} 方法
        </span>
        <span>{schemaNode.is_active ? '🟢 生效' : '⚪ 草稿'}</span>
      </div>
    </div>
  );
};

const protoNodeTypes = {
  protoSchemaNode: ProtoSchemaGraphNodeCard,
};

export const ProtoDependenciesGraphPane: React.FC<ProtoDependenciesGraphPaneProps> = ({
  initialGraph,
  onNavigateToSchemaAsset,
  onSelectNode,
  selectedSchemaId,
}) => {
  const toast = useToast();
  const [graph, setGraph] = useState<ProtoDependenciesGraphDTO>(
    initialGraph || { nodes: [], edges: [] }
  );
  const [loading, setLoading] = useState(!initialGraph);
  const [categoryFilter, setCategoryFilter] = useState<ProtoCategoryFilter>('ALL');
  const [activeOnly, setActiveOnly] = useState(true);
  const [layoutDirection, setLayoutDirection] = useState<ProtoLayoutDirection>('TB');
  const [flowNodes, setFlowNodes] = useState<ProtoSchemaFlowNode[]>([]);
  const [flowEdges, setFlowEdges] = useState<ProtoSchemaFlowEdge[]>([]);
  const [layoutVersion, setLayoutVersion] = useState(0);
  const [hoveredSchemaId, setHoveredSchemaId] = useState<string | null>(null);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [showMiniMap, setShowMiniMap] = useState(false);
  const [zoom, setZoom] = useState(1);
  const [exportingPng, setExportingPng] = useState(false);
  const [rfInstance, setRfInstance] = useState<
    ReactFlowInstance<ProtoSchemaFlowNode, ProtoSchemaFlowEdge> | null
  >(null);

  const inlineMiniMapPrefRef = useRef(false);

  const loadGraph = useCallback(async () => {
    setLoading(true);
    try {
      const data = await schemaApi.getProtoDependenciesGraph({ active_only: activeOnly });
      setGraph(data);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '加载跨包依赖图谱失败');
    } finally {
      setLoading(false);
    }
  }, [activeOnly, toast]);

  useEffect(() => {
    if (!initialGraph) {
      void loadGraph();
    }
  }, [initialGraph, loadGraph]);

  const filteredGraph = useMemo(
    () => filterProtoGraphByCategory(graph, categoryFilter),
    [graph, categoryFilter]
  );

  useEffect(() => {
    let cancelled = false;
    void layoutProtoDependenciesGraph(filteredGraph, null, layoutDirection).then((res) => {
      if (!cancelled) {
        setFlowNodes(res.nodes);
        setFlowEdges(res.edges);
        setLayoutVersion((v) => v + 1);
      }
    });
    return () => {
      cancelled = true;
    };
  }, [filteredGraph, layoutDirection]);

  const activeFocusSchemaId = hoveredSchemaId ?? selectedSchemaId ?? null;

  const highlightSet = useMemo(
    () => computeGraphHighlightSet(filteredGraph, activeFocusSchemaId),
    [filteredGraph, activeFocusSchemaId]
  );

  const renderedFlowNodes = useMemo(() => {
    const selectedKey =
      selectedSchemaId !== undefined && selectedSchemaId !== null ? String(selectedSchemaId) : null;
    if (selectedKey === null && !highlightSet) {
      return flowNodes;
    }
    return flowNodes.map((n) => {
      const isSelected = selectedKey !== null && String(n.data.schemaNode.id) === selectedKey;
      const isHighlighted = highlightSet ? highlightSet.activeNodeIds.has(n.id) : false;
      const isDimmed = highlightSet ? !isHighlighted : false;
      return {
        ...n,
        data: {
          ...n.data,
          isSelected,
          isHighlighted,
          isDimmed,
        },
      };
    });
  }, [flowNodes, selectedSchemaId, highlightSet]);

  const renderedFlowEdges = useMemo(() => {
    if (!highlightSet) {
      return flowEdges;
    }
    return flowEdges.map((edge) => {
      const isActiveEdge = highlightSet.activeEdgeIds.has(edge.id);
      return {
        ...edge,
        label: formatEdgeProtoLabel(edge.data?.matchedProtos, isActiveEdge),
        zIndex: isActiveEdge ? 10 : 0,
        style: {
          stroke: 'var(--color-primary)',
          strokeWidth: isActiveEdge ? 2.3 : 1.3,
          opacity: isActiveEdge ? 1 : 0.16,
        },
        labelStyle: {
          fontSize: 11,
          fill: isActiveEdge ? 'var(--color-primary)' : 'var(--color-text-secondary)',
          fontWeight: isActiveEdge ? 600 : 400,
          opacity: isActiveEdge ? 1 : 0.2,
        },
        labelBgStyle: {
          fill: 'var(--color-bg-surface)',
          opacity: isActiveEdge ? 1 : 0.2,
        },
      };
    });
  }, [flowEdges, highlightSet]);

  // 布局重排或切全屏后自动居中适配视口并同步缩放读数
  useEffect(() => {
    if (!rfInstance || flowNodes.length === 0) {
      return;
    }
    let cancelled = false;
    const rafId = window.requestAnimationFrame(() => {
      void rfInstance
        .fitView({ padding: DEFAULT_FIT_PADDING, duration: 180 })
        .then(() => {
          if (!cancelled) {
            setZoom(rfInstance.getZoom());
          }
        })
        .catch(() => {});
    });
    return () => {
      cancelled = true;
      window.cancelAnimationFrame(rafId);
    };
  }, [rfInstance, layoutVersion, isFullscreen, flowNodes.length]);

  const handleFitView = useCallback(() => {
    if (!rfInstance) return;
    void rfInstance
      .fitView({ padding: DEFAULT_FIT_PADDING, duration: 180 })
      .then(() => setZoom(rfInstance.getZoom()))
      .catch(() => {});
  }, [rfInstance]);

  const handleActualSize = useCallback(() => {
    if (!rfInstance) return;
    void rfInstance
      .zoomTo(1, { duration: 180 })
      .then(() => setZoom(rfInstance.getZoom()))
      .catch(() => {});
  }, [rfInstance]);

  const handleZoomIn = useCallback(() => {
    if (!rfInstance) return;
    void rfInstance
      .zoomIn({ duration: 150 })
      .then(() => setZoom(rfInstance.getZoom()))
      .catch(() => {});
  }, [rfInstance]);

  const handleZoomOut = useCallback(() => {
    if (!rfInstance) return;
    void rfInstance
      .zoomOut({ duration: 150 })
      .then(() => setZoom(rfInstance.getZoom()))
      .catch(() => {});
  }, [rfInstance]);

  const handleToggleFullscreen = useCallback((nextFullscreen: boolean) => {
    setRfInstance(null);
    setHoveredSchemaId(null);
    if (nextFullscreen) {
      setShowMiniMap((curr) => {
        inlineMiniMapPrefRef.current = curr;
        return true;
      });
    } else {
      setShowMiniMap(inlineMiniMapPrefRef.current);
    }
    setIsFullscreen(nextFullscreen);
  }, []);

  // 全屏模式下支持 +/-/0/1 快捷缩放（Esc 由公共 Modal 的 escManager LIFO 统一托管）
  useEffect(() => {
    if (!isFullscreen) {
      return;
    }
    const handleKeyDown = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      if (
        target &&
        (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)
      ) {
        return;
      }
      if (e.key === '+' || e.key === '=') {
        e.preventDefault();
        handleZoomIn();
      } else if (e.key === '-') {
        e.preventDefault();
        handleZoomOut();
      } else if (e.key === '0') {
        e.preventDefault();
        handleFitView();
      } else if (e.key === '1') {
        e.preventDefault();
        handleActualSize();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [isFullscreen, handleZoomIn, handleZoomOut, handleFitView, handleActualSize]);

  const handleExportSvg = useCallback(() => {
    if (renderedFlowNodes.length === 0) return;
    try {
      exportProtoGraphAsSvg(renderedFlowNodes, renderedFlowEdges, {
        direction: layoutDirection,
      });
      toast.success('已导出跨包依赖关系 SVG 矢量图');
    } catch {
      toast.error('SVG 导出失败，请重试');
    }
  }, [renderedFlowNodes, renderedFlowEdges, layoutDirection, toast]);

  const handleExportPng = useCallback(async () => {
    if (renderedFlowNodes.length === 0 || exportingPng) return;
    setExportingPng(true);
    try {
      await exportProtoGraphAsPng(renderedFlowNodes, renderedFlowEdges, {
        direction: layoutDirection,
      });
      toast.success('已导出高清 PNG 图片');
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'PNG 导出失败，请尝试导出 SVG');
    } finally {
      setExportingPng(false);
    }
  }, [renderedFlowNodes, renderedFlowEdges, layoutDirection, exportingPng, toast]);

  const handleInitFlow = useCallback(
    (instance: ReactFlowInstance<ProtoSchemaFlowNode, ProtoSchemaFlowEdge>) => {
      setRfInstance(instance);
      setZoom(instance.getZoom());
    },
    []
  );

  const handleViewportMove = useCallback((_: unknown, viewport: Viewport) => {
    setZoom(viewport.zoom);
  }, []);

  const getMiniMapNodeColor = useCallback((node: ProtoSchemaFlowNode) => {
    const cat = node.data?.schemaNode?.category;
    if (cat === 'BASE_LIB') return 'var(--color-info, var(--color-primary))';
    if (cat === 'GATEWAY') return 'var(--color-success)';
    return 'var(--color-primary)';
  }, []);

  const filterButtons: Array<{ value: ProtoCategoryFilter; label: string }> = [
    { value: 'ALL', label: `全部 (${graph.nodes.length})` },
    {
      value: 'BASE_LIB',
      label: `🧱 基础公共库 (${graph.nodes.filter((n) => n.category === 'BASE_LIB').length})`,
    },
    {
      value: 'BUSINESS',
      label: `📦 业务域服务 (${graph.nodes.filter((n) => n.category === 'BUSINESS').length})`,
    },
    {
      value: 'GATEWAY',
      label: `🌐 网关聚合层 (${graph.nodes.filter((n) => n.category === 'GATEWAY').length})`,
    },
  ];

  const hasVisibleNodes = filteredGraph.nodes.length > 0;
  const zoomPercent = `${Math.round(zoom * 100)}%`;
  const isAtActualSize = Math.abs(zoom - 1) < 0.02;

  const renderToolbar = (inFullscreen: boolean) => (
    <div className="proto-graph-toolbar">
      <div className="proto-graph-toolbar__filters">
        {filterButtons.map((btn) => {
          const selected = categoryFilter === btn.value;
          return (
            <button
              key={btn.value}
              type="button"
              onClick={() => setCategoryFilter(btn.value)}
              className={`proto-graph-filter-btn ${selected ? 'proto-graph-filter-btn--active' : ''}`}
            >
              {btn.label}
            </button>
          );
        })}

        <label className="proto-graph-checkbox">
          <input
            type="checkbox"
            checked={activeOnly}
            onChange={(e) => setActiveOnly(e.target.checked)}
          />
          <span>仅展示生效池版本</span>
        </label>
      </div>

      <div className="proto-graph-toolbar__actions">
        <div className="proto-graph-segmented" role="group" aria-label="依赖图布局方向">
          <button
            type="button"
            className={`proto-graph-segmented__btn ${layoutDirection === 'TB' ? 'proto-graph-segmented__btn--active' : ''}`}
            onClick={() => setLayoutDirection('TB')}
            title="自上而下分层布局（推荐宽屏）"
          >
            <ArrowDownUp size={13} />
            <span>纵向</span>
          </button>
          <button
            type="button"
            className={`proto-graph-segmented__btn ${layoutDirection === 'LR' ? 'proto-graph-segmented__btn--active' : ''}`}
            onClick={() => setLayoutDirection('LR')}
            title="自左向右分层布局"
          >
            <ArrowLeftRight size={13} />
            <span>横向</span>
          </button>
        </div>

        <span className="proto-graph-divider" aria-hidden="true" />

        <div className="proto-graph-control-group" role="group" aria-label="画布缩放控制">
          <button
            type="button"
            className="proto-graph-tool-btn proto-graph-tool-btn--icon"
            onClick={handleZoomOut}
            disabled={!hasVisibleNodes}
            title="缩小 (-)"
            aria-label="缩小依赖图"
          >
            <ZoomOut size={14} />
          </button>
          <button
            type="button"
            className="proto-graph-zoom-readout"
            onClick={handleFitView}
            disabled={!hasVisibleNodes}
            title="点击居中并适应视口 (0)"
            aria-label={`当前缩放比例 ${zoomPercent}，点击适应视口`}
          >
            {zoomPercent}
          </button>
          <button
            type="button"
            className="proto-graph-tool-btn proto-graph-tool-btn--icon"
            onClick={handleZoomIn}
            disabled={!hasVisibleNodes}
            title="放大 (+)"
            aria-label="放大依赖图"
          >
            <ZoomIn size={14} />
          </button>
          <button
            type="button"
            className={`proto-graph-tool-btn ${isAtActualSize ? 'proto-graph-tool-btn--active' : ''}`}
            onClick={handleActualSize}
            disabled={!hasVisibleNodes}
            title="1:1 原始清晰比例 (1)"
          >
            <span>1:1</span>
          </button>
          <button
            type="button"
            className="proto-graph-tool-btn"
            onClick={handleFitView}
            disabled={!hasVisibleNodes}
            title="居中并适应当前视口 (0)"
          >
            <RotateCcw size={13} />
            <span>适应</span>
          </button>
        </div>

        <span className="proto-graph-divider" aria-hidden="true" />

        <button
          type="button"
          className={`proto-graph-tool-btn ${showMiniMap ? 'proto-graph-tool-btn--active' : ''}`}
          onClick={() => setShowMiniMap((prev) => !prev)}
          disabled={!hasVisibleNodes}
          title={showMiniMap ? '收起右下角小地图' : '展开右下角小地图'}
        >
          <MapIcon size={13} />
          <span>小地图</span>
        </button>

        <button
          type="button"
          className="proto-graph-tool-btn"
          onClick={() => void handleExportPng()}
          disabled={!hasVisibleNodes || exportingPng}
          title="导出高清 PNG 图片"
        >
          <Download size={13} />
          <span>{exportingPng ? '导出中...' : 'PNG'}</span>
        </button>

        <button
          type="button"
          className="proto-graph-tool-btn"
          onClick={handleExportSvg}
          disabled={!hasVisibleNodes}
          title="导出无损 SVG 矢量图"
        >
          <FileCode2 size={13} />
          <span>SVG</span>
        </button>

        <button
          type="button"
          className="proto-graph-tool-btn"
          onClick={() => void loadGraph()}
          disabled={loading}
          title="刷新依赖关系图数据"
        >
          <RefreshCw size={13} />
          <span>{loading ? '刷新中...' : '刷新'}</span>
        </button>

        <button
          type="button"
          className={`proto-graph-tool-btn ${inFullscreen ? 'proto-graph-tool-btn--close' : ''}`}
          onClick={() => handleToggleFullscreen(!inFullscreen)}
          title={inFullscreen ? '退出全屏 (Esc)' : '全屏沉浸式查看大图'}
          data-testid={inFullscreen ? 'proto-graph-exit-fullscreen-btn' : 'proto-graph-fullscreen-btn'}
        >
          {inFullscreen ? <Minimize2 size={13} /> : <Maximize2 size={13} />}
          <span>{inFullscreen ? '退出全屏' : '全屏'}</span>
        </button>
      </div>
    </div>
  );

  const renderGraphWorkspace = (inFullscreen: boolean) => (
    <div
      className={`proto-graph-workspace ${inFullscreen ? 'proto-graph-workspace--fullscreen' : ''}`}
    >
      {renderToolbar(inFullscreen)}

      {!hasVisibleNodes ? (
        <div className="proto-graph-empty">
          <p className="proto-graph-empty__title">当前筛选条件下暂无描述集依赖关系节点</p>
          <p className="proto-graph-empty__desc">
            点击任意节点可查看契约包详情并跳转参考文档或契约源码；可切换分类筛选依赖视图。
          </p>
          <div className="proto-graph-empty__tags">
            {graph.nodes.map((n) => (
              <span key={n.id} className="proto-graph-empty__tag">
                {n.name}
              </span>
            ))}
          </div>
        </div>
      ) : (
        <div className="proto-graph-stage">
          {/* 供无头 SSR 或无障碍列表快速索引当前节点名 */}
          <div className="proto-graph-ssr-index" aria-hidden="true">
            {filteredGraph.nodes.map((n) => (
              <span key={n.id}>{n.name}</span>
            ))}
          </div>

          <ReactFlow<ProtoSchemaFlowNode, ProtoSchemaFlowEdge>
            onInit={handleInitFlow}
            onMove={handleViewportMove}
            onMoveEnd={handleViewportMove}
            nodes={renderedFlowNodes}
            edges={renderedFlowEdges}
            nodeTypes={protoNodeTypes}
            fitView
            fitViewOptions={{ padding: DEFAULT_FIT_PADDING }}
            minZoom={0.2}
            maxZoom={2.5}
            nodesDraggable={false}
            nodesConnectable={false}
            proOptions={{ hideAttribution: true }}
            onNodeMouseEnter={(_, node) => setHoveredSchemaId(String(node.data.schemaNode.id))}
            onNodeMouseLeave={() => setHoveredSchemaId(null)}
            onPaneClick={() => setHoveredSchemaId(null)}
            onNodeClick={(_, node) => {
              if (onSelectNode) {
                const hit = graph.nodes.find(
                  (n) => `schema_${n.id}` === node.id || String(n.id) === node.id
                );
                if (hit) {
                  onSelectNode(hit, graph);
                  return;
                }
              }
              const target = getSchemaNodeNavigationTarget(node.id);
              onNavigateToSchemaAsset?.(target.schema_id);
            }}
          >
            <Background variant={BackgroundVariant.Dots} gap={18} size={1} />
            {showMiniMap && (
              <MiniMap
                pannable
                zoomable
                nodeStrokeWidth={2}
                nodeColor={getMiniMapNodeColor}
                maskColor="var(--color-bg-muted)"
              />
            )}
          </ReactFlow>

          <div className="proto-graph-hint">
            {inFullscreen
              ? '滚轮缩放 · 拖拽平移 · 悬停高亮链路 · 点击节点看详情 · 快捷键 +/-/1/0/Esc'
              : '悬停高亮链路 · 点击节点查看详情 · 滚轮缩放 · 右上角切换全屏'}
          </div>
        </div>
      )}
    </div>
  );

  return (
    <div className="proto-graph-pane" data-testid="proto-dependencies-graph-pane">
      {isFullscreen ? (
        <div className="proto-graph-inline-placeholder">
          <span>正在沉浸全屏视图中查看跨包依赖关系图</span>
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => handleToggleFullscreen(false)}
          >
            <Minimize2 size={14} />
            <span>返回内联视图</span>
          </button>
        </div>
      ) : (
        renderGraphWorkspace(false)
      )}

      <Modal
        open={isFullscreen}
        onClose={() => handleToggleFullscreen(false)}
        fullscreen
        showCloseButton={false}
        destroyOnClose
        bodyStyle={{ padding: 0, overflow: 'hidden' }}
        testId="proto-graph-fullscreen-modal"
      >
        {isFullscreen ? renderGraphWorkspace(true) : null}
      </Modal>
    </div>
  );
};
