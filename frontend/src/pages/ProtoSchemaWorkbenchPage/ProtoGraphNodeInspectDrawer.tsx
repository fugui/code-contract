import React, { useEffect, useMemo, useState } from 'react';
import {
  ArrowDownLeft,
  ArrowUpRight,
  BookOpen,
  Code2,
  ExternalLink,
  FileCode2,
  Info,
  Layers,
} from 'lucide-react';
import { Drawer } from '@code/common';
import * as schemaApi from '../../api/schema';
import type {
  ProtoDependenciesGraphDTO,
  ProtoDependencyNodeDTO,
  ProtoSchemaSummaryDTO,
} from '../../types';
import {
  CATEGORY_META,
  SOURCE_TYPE_LABEL,
  computeSchemaNodeRelations,
} from './ProtoDependenciesGraphPane';

export interface ProtoGraphNodeInspectContentProps {
  node: ProtoDependencyNodeDTO;
  graph: ProtoDependenciesGraphDTO;
  isAdmin?: boolean;
  initialSummary?: ProtoSchemaSummaryDTO | null;
  onOpenDocs: (schemaId: string | number, serviceName?: string) => void;
  onOpenSources: (node: ProtoDependencyNodeDTO, firstProtoFile?: string) => void;
  onPreviewSourceFile: (schemaId: number, filePath: string) => void;
  onSwitchNode: (targetNode: ProtoDependencyNodeDTO) => void;
  onOpenPipeline?: (schemaId: string | number) => void;
}

/**
 * 契约库跨包依赖图节点只读速览主体（抽离自 Drawer 以便 SSR 单元测试直渲染）
 */
export const ProtoGraphNodeInspectContent: React.FC<ProtoGraphNodeInspectContentProps> = ({
  node,
  graph,
  isAdmin = false,
  initialSummary,
  onOpenDocs,
  onOpenSources,
  onPreviewSourceFile,
  onSwitchNode,
  onOpenPipeline,
}) => {
  const [summary, setSummary] = useState<ProtoSchemaSummaryDTO | null>(initialSummary ?? null);
  const [loading, setLoading] = useState<boolean>(initialSummary === undefined);
  const [loadError, setLoadError] = useState<boolean>(false);

  useEffect(() => {
    if (initialSummary !== undefined) {
      setSummary(initialSummary);
      setLoading(false);
      setLoadError(false);
      return;
    }

    let cancelled = false;
    setLoading(true);
    setLoadError(false);

    void schemaApi
      .getProtoSchemaSummary(node.id)
      .then((res) => {
        if (!cancelled) {
          setSummary(res);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setSummary(null);
          setLoadError(true);
        }
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [node.id, initialSummary]);

  const relations = useMemo(
    () => computeSchemaNodeRelations(graph, node.id),
    [graph, node.id]
  );

  const services = summary?.services_summary ?? [];
  const protoFiles = summary?.proto_files ?? [];
  const hasServices = node.service_count > 0 || services.length > 0;
  const firstServiceName = services[0]?.service_name;
  const firstProtoFile = protoFiles[0];
  const catMeta = CATEGORY_META[node.category] || CATEGORY_META.BUSINESS;
  const srcLabel = node.source_type
    ? SOURCE_TYPE_LABEL[node.source_type] || node.source_type
    : '';

  return (
    <div className="pggraph-inspect-body" data-testid="proto-graph-node-inspect-content">
      {/* 1. 元信息与统计条 */}
      <div className="pggraph-inspect-meta-card">
        <div className="pggraph-inspect-badges">
          <span className="pggraph-inspect-badge pggraph-inspect-badge--category">
            {catMeta.icon} {catMeta.label}
          </span>
          {srcLabel && <span className="pggraph-inspect-badge">{srcLabel}</span>}
          <span
            className={`pggraph-inspect-badge ${
              node.is_active ? 'pggraph-inspect-badge--active' : ''
            }`}
          >
            {node.is_active ? '🟢 生效中' : '⚪ 草稿'}
          </span>
        </div>

        <div className="pggraph-inspect-stats">
          <span>
            {node.proto_file_count} 文件 · {node.service_count} 服务 · {node.operation_count} 方法
          </span>
          {summary && services.length > 0 && (
            <span className="pggraph-inspect-stats__claim">
              认领率 {Math.round((summary.claimed_ratio || 0) * 100)}%
            </span>
          )}
        </div>
      </div>

      {/* 2. 消费侧直达操作区（按是否有服务分流） */}
      <div className="pggraph-inspect-actions-box">
        {!hasServices && (
          <div className="pggraph-inspect-notice" data-testid="graph-inspect-no-service-notice">
            <Info size={15} />
            <span>该契约包不含 RPC Service，请在契约源码中查阅公共 Message / Enum 定义。</span>
          </div>
        )}

        <div className="pggraph-inspect-actions">
          {hasServices ? (
            <>
              <button
                type="button"
                className="btn btn-primary"
                data-testid="graph-inspect-open-docs"
                onClick={() => onOpenDocs(node.id, firstServiceName)}
              >
                <BookOpen size={14} />
                <span>查看全注释参考文档</span>
              </button>
              <button
                type="button"
                className="btn btn-secondary"
                data-testid="graph-inspect-open-sources"
                onClick={() => onOpenSources(node, firstProtoFile)}
              >
                <Code2 size={14} />
                <span>查看契约源码</span>
              </button>
            </>
          ) : (
            <button
              type="button"
              className="btn btn-primary"
              data-testid="graph-inspect-open-sources"
              onClick={() => onOpenSources(node, firstProtoFile)}
            >
              <Code2 size={14} />
              <span>查看契约源码</span>
            </button>
          )}
        </div>
      </div>

      {/* 3. 导出服务列表 */}
      {hasServices && (
        <section className="pggraph-inspect-section">
          <div className="pggraph-inspect-section__title">
            <Layers size={14} />
            <span>导出 RPC 服务 ({services.length || node.service_count})</span>
          </div>
          {loading && services.length === 0 ? (
            <p className="pggraph-inspect-muted">正在加载导出服务清单...</p>
          ) : services.length > 0 ? (
            <div className="pggraph-inspect-list">
              {services.map((svc) => (
                <button
                  key={svc.service_name}
                  type="button"
                  className="pggraph-inspect-list-item"
                  onClick={() => onOpenDocs(node.id, svc.service_name)}
                  title={`在全注释参考文档中打开 ${svc.service_name}`}
                >
                  <div className="pggraph-inspect-list-item__main">
                    <span className="pggraph-inspect-list-item__name">{svc.service_name}</span>
                    <span className="pggraph-inspect-list-item__sub">
                      {svc.proto_file} · {svc.operation_count} 方法
                    </span>
                  </div>
                  <div className="pggraph-inspect-list-item__right">
                    <span
                      className={`pggraph-inspect-claim-pill ${
                        svc.is_claimed ? 'pggraph-inspect-claim-pill--claimed' : ''
                      }`}
                    >
                      {svc.is_claimed ? svc.claimed_module_name || '已认领' : '未认领'}
                    </span>
                    <span className="pggraph-inspect-link-hint">参考文档 →</span>
                  </div>
                </button>
              ))}
            </div>
          ) : (
            <p className="pggraph-inspect-muted">暂无导出服务明细，可直接点击上方查看参考文档。</p>
          )}
        </section>
      )}

      {/* 4. 包含的 .proto 文件列表 */}
      <section className="pggraph-inspect-section">
        <div className="pggraph-inspect-section__title">
          <FileCode2 size={14} />
          <span>包含的契约源文件 ({protoFiles.length || node.proto_file_count})</span>
        </div>
        {loading ? (
          <p className="pggraph-inspect-muted">正在加载 .proto 文件清单...</p>
        ) : loadError ? (
          <p className="pggraph-inspect-muted">未能获取文件清单，可尝试直接跳转契约源码视图。</p>
        ) : protoFiles.length > 0 ? (
          <div className="pggraph-inspect-list">
            {protoFiles.map((filePath) => (
              <button
                key={filePath}
                type="button"
                className="pggraph-inspect-list-item"
                onClick={() => onPreviewSourceFile(Number(node.id), filePath)}
                title={`预览只读源码 ${filePath}`}
              >
                <span className="pggraph-inspect-list-item__mono">{filePath}</span>
                <span className="pggraph-inspect-link-hint">只读预览 →</span>
              </button>
            ))}
          </div>
        ) : (
          <p className="pggraph-inspect-muted">暂无 .proto 文件路径记录。</p>
        )}
      </section>

      {/* 5. 跨包依赖关系（上游 & 下游，支持抽屉内就地切换） */}
      <section className="pggraph-inspect-section">
        <div className="pggraph-inspect-relations">
          <div className="pggraph-inspect-relation-group">
            <div className="pggraph-inspect-section__title">
              <ArrowUpRight size={14} />
              <span>上游依赖（本包引用 · {relations.upstream.length}）</span>
            </div>
            {relations.upstream.length === 0 ? (
              <p className="pggraph-inspect-muted">无外部跨包引用</p>
            ) : (
              <div className="pggraph-inspect-list">
                {relations.upstream.map((rel) => (
                  <button
                    key={`up-${rel.node.id}`}
                    type="button"
                    className="pggraph-inspect-list-item"
                    onClick={() => onSwitchNode(rel.node)}
                  >
                    <div className="pggraph-inspect-list-item__main">
                      <span className="pggraph-inspect-list-item__name">
                        {rel.node.name} <span className="pggraph-inspect-ver">v{rel.node.version}</span>
                      </span>
                      {rel.matchedProtos.length > 0 && (
                        <span className="pggraph-inspect-list-item__sub">
                          {rel.matchedProtos.join(', ')}
                        </span>
                      )}
                    </div>
                    <span className="pggraph-inspect-link-hint">查看包 →</span>
                  </button>
                ))}
              </div>
            )}
          </div>

          <div className="pggraph-inspect-relation-group">
            <div className="pggraph-inspect-section__title">
              <ArrowDownLeft size={14} />
              <span>下游被依赖（被谁引用 · {relations.downstream.length}）</span>
            </div>
            {relations.downstream.length === 0 ? (
              <p className="pggraph-inspect-muted">暂无下游契约包引用</p>
            ) : (
              <div className="pggraph-inspect-list">
                {relations.downstream.map((rel) => (
                  <button
                    key={`down-${rel.node.id}`}
                    type="button"
                    className="pggraph-inspect-list-item"
                    onClick={() => onSwitchNode(rel.node)}
                  >
                    <div className="pggraph-inspect-list-item__main">
                      <span className="pggraph-inspect-list-item__name">
                        {rel.node.name} <span className="pggraph-inspect-ver">v{rel.node.version}</span>
                      </span>
                      {rel.matchedProtos.length > 0 && (
                        <span className="pggraph-inspect-list-item__sub">
                          {rel.matchedProtos.join(', ')}
                        </span>
                      )}
                    </div>
                    <span className="pggraph-inspect-link-hint">查看包 →</span>
                  </button>
                ))}
              </div>
            )}
          </div>
        </div>
      </section>

      {/* 6. 管理员弱入口 */}
      {isAdmin && onOpenPipeline && (
        <div className="pggraph-inspect-admin-footer">
          <button
            type="button"
            className="btn btn-secondary pggraph-inspect-admin-btn"
            data-testid="graph-inspect-open-pipeline"
            onClick={() => onOpenPipeline(node.id)}
          >
            <ExternalLink size={13} />
            <span>前往入库流水线管理版本</span>
          </button>
        </div>
      )}
    </div>
  );
};

export interface ProtoGraphNodeInspectDrawerProps {
  open: boolean;
  onClose: () => void;
  node: ProtoDependencyNodeDTO | null;
  graph: ProtoDependenciesGraphDTO | null;
  isAdmin?: boolean;
  onOpenDocs: (schemaId: string | number, serviceName?: string) => void;
  onOpenSources: (node: ProtoDependencyNodeDTO, firstProtoFile?: string) => void;
  onPreviewSourceFile: (schemaId: number, filePath: string) => void;
  onSwitchNode: (targetNode: ProtoDependencyNodeDTO) => void;
  onOpenPipeline?: (schemaId: string | number) => void;
}

export const ProtoGraphNodeInspectDrawer: React.FC<ProtoGraphNodeInspectDrawerProps> = ({
  open,
  onClose,
  node,
  graph,
  isAdmin = false,
  onOpenDocs,
  onOpenSources,
  onPreviewSourceFile,
  onSwitchNode,
  onOpenPipeline,
}) => {
  return (
    <Drawer
      open={open && Boolean(node)}
      onClose={onClose}
      width={560}
      title={node ? node.name : '契约包详情'}
      subtitle={node ? `v${node.version} · 只读契约包速览` : undefined}
      destroyOnClose
    >
      {node && graph ? (
        <ProtoGraphNodeInspectContent
          node={node}
          graph={graph}
          isAdmin={isAdmin}
          onOpenDocs={onOpenDocs}
          onOpenSources={onOpenSources}
          onPreviewSourceFile={onPreviewSourceFile}
          onSwitchNode={onSwitchNode}
          onOpenPipeline={onOpenPipeline}
        />
      ) : null}
    </Drawer>
  );
};
