import React, { useMemo, useState } from 'react';
import type { DiscoveryProcessGroupDTO } from '../../types';

export interface DiscoveryLiveDepGraphProps {
  localProcesses: DiscoveryProcessGroupDTO[];
  remoteLdcProcesses: DiscoveryProcessGroupDTO[];
}

interface GraphNode {
  id: string;
  col: 0 | 1 | 2;
  label: string;
  subLabel: string;
  x: number;
  y: number;
}

interface GraphEdge {
  id: string;
  fromId: string;
  toId: string;
}

export const DiscoveryLiveDepGraph: React.FC<DiscoveryLiveDepGraphProps> = ({
  localProcesses,
  remoteLdcProcesses,
}) => {
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);

  const { nodes, edges, width, height } = useMemo(() => {
    const consumerMap = new Map<string, { label: string; sub: string }>();
    const serviceMap = new Map<string, { label: string; sub: string }>();
    const providerMap = new Map<string, { label: string; sub: string }>();
    const edgeList: GraphEdge[] = [];
    const seenEdge = new Set<string>();

    const allProcs = [...localProcesses, ...remoteLdcProcesses];
    const providerByService = new Map<string, { id: string; label: string; sub: string }>();

    for (const p of allProcs) {
      for (const svc of p.services) {
        const pid = `prov:${p.process_key}`;
        const label = p.is_remote_ldc
          ? `${p.proc_name} (${p.remote_node || 'LDC'})`
          : p.proc_name;
        const sub = `${p.pids.length} PIDs · ${svc.transport}`;
        providerByService.set(svc.service_name, { id: pid, label, sub });
      }
    }

    for (const p of localProcesses) {
      if (p.subscriptions.length === 0) continue;
      const cid = `cons:${p.process_key}`;
      consumerMap.set(cid, {
        label: p.proc_name,
        sub: `${p.observed_role} · ${p.subscriptions.length} 订阅`,
      });

      for (const sub of p.subscriptions) {
        const sid = `svc:${sub.service_name}`;
        serviceMap.set(sid, {
          label: sub.service_name,
          sub: sub.transport || sub.provider_kind,
        });

        const e1 = `${cid}->${sid}`;
        if (!seenEdge.has(e1)) {
          seenEdge.add(e1);
          edgeList.push({ id: e1, fromId: cid, toId: sid });
        }

        const matchedProv = providerByService.get(sub.service_name);
        const pid = matchedProv ? matchedProv.id : `prov:offline:${sub.service_name}`;
        if (matchedProv) {
          providerMap.set(pid, { label: matchedProv.label, sub: matchedProv.sub });
        } else {
          providerMap.set(pid, {
            label: sub.provider_proc_name || '未上线提供方',
            sub: sub.provider_kind,
          });
        }

        const e2 = `${sid}->${pid}`;
        if (!seenEdge.has(e2)) {
          seenEdge.add(e2);
          edgeList.push({ id: e2, fromId: sid, toId: pid });
        }
      }
    }

    // Also show pure standalone server processes if no edges exist yet
    if (edgeList.length === 0) {
      for (const p of localProcesses) {
        for (const svc of p.services) {
          const sid = `svc:${svc.service_name}`;
          const pid = `prov:${p.process_key}`;
          serviceMap.set(sid, { label: svc.service_name, sub: svc.transport });
          providerMap.set(pid, { label: p.proc_name, sub: `${p.services.length} 服务` });
          const e2 = `${sid}->${pid}`;
          if (!seenEdge.has(e2)) {
            seenEdge.add(e2);
            edgeList.push({ id: e2, fromId: sid, toId: pid });
          }
        }
      }
    }

    const colX: Record<0 | 1 | 2, number> = { 0: 24, 1: 280, 2: 536 };
    const rowH = 68;
    const builtNodes: GraphNode[] = [];

    const appendCol = (
      col: 0 | 1 | 2,
      entries: Array<[string, { label: string; sub: string }]>
    ) => {
      entries.forEach(([id, info], idx) => {
        builtNodes.push({
          id,
          col,
          label: info.label,
          subLabel: info.sub,
          x: colX[col],
          y: 28 + idx * rowH,
        });
      });
    };

    appendCol(0, Array.from(consumerMap.entries()));
    appendCol(1, Array.from(serviceMap.entries()));
    appendCol(2, Array.from(providerMap.entries()));

    const maxRows = Math.max(consumerMap.size, serviceMap.size, providerMap.size, 1);
    return {
      nodes: builtNodes,
      edges: edgeList,
      width: 760,
      height: Math.max(360, maxRows * rowH + 56),
    };
  }, [localProcesses, remoteLdcProcesses]);

  const nodeById = useMemo(() => {
    const m = new Map<string, GraphNode>();
    nodes.forEach((n) => m.set(n.id, n));
    return m;
  }, [nodes]);

  const neighborIds = useMemo(() => {
    if (!selectedNodeId) return null;
    const set = new Set<string>([selectedNodeId]);
    for (const e of edges) {
      if (e.fromId === selectedNodeId) set.add(e.toId);
      if (e.toId === selectedNodeId) set.add(e.fromId);
    }
    return set;
  }, [edges, selectedNodeId]);

  const selectedNode = selectedNodeId ? nodeById.get(selectedNodeId) ?? null : null;

  return (
    <div className="discovery-panel" data-testid="discovery-live-dep-graph">
      <div className="discovery-toolbar">
        <span style={{ fontSize: 13, fontWeight: 600 }}>
          零抓包实时依赖图（订阅进程 ➔ 服务名 ➔ 发布进程）
        </span>
        {selectedNodeId && (
          <button
            type="button"
            className="code-btn code-btn--secondary code-btn--xs"
            onClick={() => setSelectedNodeId(null)}
          >
            清除 1-Hop 聚焦
          </button>
        )}
      </div>

      {nodes.length === 0 ? (
        <div className="discovery-empty">暂无可渲染的实时服务或订阅依赖</div>
      ) : (
        <div className="discovery-dep-graph-shell">
          <div className="discovery-dep-canvas">
            <svg
              className="discovery-dep-svg"
              viewBox={`0 0 ${width} ${height}`}
              height={height}
            >
              {edges.map((e) => {
                const from = nodeById.get(e.fromId);
                const to = nodeById.get(e.toId);
                if (!from || !to) return null;
                const x1 = from.x + 196;
                const y1 = from.y + 24;
                const x2 = to.x;
                const y2 = to.y + 24;
                const midX = (x1 + x2) / 2;
                const isActive =
                  selectedNodeId &&
                  (e.fromId === selectedNodeId || e.toId === selectedNodeId);
                const isDim = Boolean(selectedNodeId && !isActive);
                return (
                  <path
                    key={e.id}
                    d={`M ${x1} ${y1} C ${midX} ${y1}, ${midX} ${y2}, ${x2} ${y2}`}
                    className={`discovery-dep-edge ${
                      isActive ? 'discovery-dep-edge--active' : ''
                    } ${isDim ? 'discovery-dep-edge--dim' : ''}`}
                  />
                );
              })}

              {nodes.map((n) => {
                const isSelected = selectedNodeId === n.id;
                const isDim = Boolean(neighborIds && !neighborIds.has(n.id));
                const shortLabel =
                  n.label.length > 24 ? `${n.label.slice(0, 22)}...` : n.label;
                return (
                  <g
                    key={n.id}
                    transform={`translate(${n.x}, ${n.y})`}
                    className={`discovery-dep-node ${
                      isSelected ? 'discovery-dep-node--selected' : ''
                    } ${isDim ? 'discovery-dep-node--dim' : ''}`}
                    onClick={() =>
                      setSelectedNodeId((prev) => (prev === n.id ? null : n.id))
                    }
                  >
                    <rect className="discovery-dep-node-rect" width={196} height={48} />
                    <text x={10} y={20} className="discovery-dep-node-title">
                      {shortLabel}
                    </text>
                    <text x={10} y={37} className="discovery-dep-node-sub">
                      {n.subLabel}
                    </text>
                  </g>
                );
              })}
            </svg>
          </div>

          <div className="discovery-dep-inspector" data-testid="discovery-dep-inspector">
            <div style={{ fontWeight: 700 }}>
              {selectedNode ? '1-Hop 上下游聚焦' : '点击画布节点聚焦'}
            </div>
            {selectedNode ? (
              <>
                <div className="discovery-mono">{selectedNode.label}</div>
                <div style={{ color: 'var(--color-text-secondary)' }}>
                  {selectedNode.subLabel}
                </div>
                <div style={{ fontSize: 12, color: 'var(--color-text-muted)' }}>
                  1-Hop 关联节点 ({Math.max((neighborIds?.size ?? 1) - 1, 0)}):
                </div>
                <ul style={{ margin: 0, paddingLeft: 16, fontSize: 12 }}>
                  {Array.from(neighborIds ?? [])
                    .filter((id) => id !== selectedNode.id)
                    .map((id) => (
                      <li key={id} className="discovery-mono">
                        {nodeById.get(id)?.label ?? id}
                      </li>
                    ))}
                </ul>
              </>
            ) : (
              <div style={{ color: 'var(--color-text-muted)', fontSize: 12 }}>
                无需抓包或预建逻辑模块，直接基于 CSG rpcmon 服务发布与订阅关系绘制实时调用拓扑。
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
};
