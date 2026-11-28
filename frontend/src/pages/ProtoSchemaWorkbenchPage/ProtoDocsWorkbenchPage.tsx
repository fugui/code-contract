import React, { useEffect, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { BookOpen, Code2, Network } from 'lucide-react';
import { useModuleUser } from '../../context/ModuleUserContext';
import { useModulePath } from '../../hooks/useModulePath';
import type {
  ProtoDependenciesGraphDTO,
  ProtoDependencyNodeDTO,
  ProtoDocTreeResponse,
  ProtoServiceDocDetailDTO,
} from '../../types';
import { LivingProtoDocsPane } from './LivingProtoDocsPane';
import { GitSourcesWorkbenchPane } from './GitSourcesWorkbenchPane';
import { ProtoDependenciesGraphPane } from './ProtoDependenciesGraphPane';
import { ProtoGraphNodeInspectDrawer } from './ProtoGraphNodeInspectDrawer';
import { ProtoSourceAnchorDrawer, useSourceAnchorDrawer } from './ProtoSourceAnchorDrawer';
import './ProtoDocsAndCodeViewer.css';

export type ProtoDocsViewMode = 'reference' | 'sources' | 'graph';

export interface ProtoDocsWorkbenchPageProps {
  initialTree?: ProtoDocTreeResponse;
  initialDetail?: ProtoServiceDocDetailDTO | null;
}

/**
 * 契约库工作台 (/proto-docs)
 * 聚焦「主线 2A：契约库本体消费」，以只读方式提供全注释参考文档、规范化契约源码与跨包依赖关系图。
 */
export const ProtoDocsWorkbenchPage: React.FC<ProtoDocsWorkbenchPageProps> = ({
  initialTree,
  initialDetail,
}) => {
  const [searchParams, setSearchParams] = useSearchParams();
  const navigate = useNavigate();
  const resolvePath = useModulePath();
  const { user } = useModuleUser();
  const isAdmin = Boolean(user?.is_module_admin || user?.is_super_admin);

  const rawDocsView = searchParams.get('docs_view');
  const rawView = searchParams.get('view');
  const legacyTab = searchParams.get('tab');
  const serviceNameQuery = searchParams.get('service_name') || '';
  const schemaIdQuery = searchParams.get('schema_id') || '';
  const searchQueryParam = searchParams.get('search') || '';
  const repoIdParam = searchParams.get('repo_id');

  const docsView: ProtoDocsViewMode =
    rawDocsView === 'graph' || rawView === 'graph' || legacyTab === 'graph'
      ? 'graph'
      : rawDocsView === 'sources' || rawView === 'sources' || legacyTab === 'sources'
        ? 'sources'
        : 'reference';

  const setDocsView = (mode: ProtoDocsViewMode) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set('docs_view', mode);
      next.delete('view');
      next.delete('tab');
      return next;
    });
  };

  const sourceAnchorDrawer = useSourceAnchorDrawer();
  const [inspectedGraphNode, setInspectedGraphNode] = useState<ProtoDependencyNodeDTO | null>(null);
  const [inspectedGraphData, setInspectedGraphData] = useState<ProtoDependenciesGraphDTO | null>(
    null
  );

  useEffect(() => {
    if (docsView !== 'graph') {
      setInspectedGraphNode(null);
    }
  }, [docsView]);

  const handleOpenDocsFromGraph = (schemaId: string | number, serviceName?: string) => {
    setInspectedGraphNode(null);
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set('docs_view', 'reference');
      next.set('schema_id', String(schemaId));
      if (serviceName) {
        next.set('service_name', serviceName);
      } else {
        next.delete('service_name');
      }
      next.delete('view');
      next.delete('tab');
      next.delete('repo_id');
      next.delete('file_id');
      next.delete('search');
      return next;
    });
  };

  const handleOpenSourcesFromGraph = (node: ProtoDependencyNodeDTO, firstProtoFile?: string) => {
    if (node.git_repo_id && node.git_repo_id > 0) {
      const targetRepoId = String(node.git_repo_id);
      setInspectedGraphNode(null);
      setSearchParams((prev) => {
        const next = new URLSearchParams(prev);
        next.set('docs_view', 'sources');
        next.set('repo_id', targetRepoId);
        next.delete('view');
        next.delete('tab');
        next.delete('schema_id');
        next.delete('service_name');
        next.delete('file_id');
        next.delete('search');
        return next;
      });
      return;
    }
    void sourceAnchorDrawer.openBySchemaFile(Number(node.id), firstProtoFile || '', 1);
  };

  const handleOpenPipeline = (schemaId: string | number) => {
    setInspectedGraphNode(null);
    navigate(resolvePath(`/proto-pipeline?stage=publish&schema_id=${schemaId}`));
  };

  return (
    <div
      className={`pdoc-page-shell ${docsView === 'graph' ? 'pdoc-page-shell--graph' : ''}`}
      data-testid="proto-docs-workbench-page"
    >
      <div className="pdoc-page-header">
        <div className="pdoc-page-header__meta">
          <h2 className="pdoc-page-header__title">
            <BookOpen size={17} />
            <span>契约库</span>
          </h2>
          <p className="pdoc-page-header__desc">
            全注释 RPC 接口参考文档、只读规范化 Proto 源码与跨包依赖关系图。
          </p>
        </div>

        <div className="code-tab-pills" role="tablist" aria-label="契约库视图切换">
          <button
            type="button"
            role="tab"
            data-testid="pdoc-tab-reference"
            aria-selected={docsView === 'reference'}
            className={`code-pill-btn ${docsView === 'reference' ? 'code-pill-btn--active' : ''}`}
            onClick={() => setDocsView('reference')}
          >
            <BookOpen size={14} /> 全注释参考文档
          </button>
          <button
            type="button"
            role="tab"
            data-testid="pdoc-tab-sources"
            aria-selected={docsView === 'sources'}
            className={`code-pill-btn ${docsView === 'sources' ? 'code-pill-btn--active' : ''}`}
            onClick={() => setDocsView('sources')}
          >
            <Code2 size={14} /> 契约源码
          </button>
          <button
            type="button"
            role="tab"
            data-testid="pdoc-tab-graph"
            aria-selected={docsView === 'graph'}
            className={`code-pill-btn ${docsView === 'graph' ? 'code-pill-btn--active' : ''}`}
            onClick={() => setDocsView('graph')}
          >
            <Network size={14} /> 跨包依赖关系图
          </button>
        </div>
      </div>

      {docsView === 'reference' ? (
        <LivingProtoDocsPane
          initialTree={initialTree}
          initialDetail={initialDetail}
          initialServiceName={serviceNameQuery}
          initialSchemaId={schemaIdQuery}
          initialSearch={searchQueryParam}
          onJumpToSource={(sid, fp, line) => void sourceAnchorDrawer.openBySchemaFile(sid, fp, line)}
        />
      ) : docsView === 'sources' ? (
        <GitSourcesWorkbenchPane
          readOnlyConsumer
          isAdmin={isAdmin}
          initialSelectedRepoId={repoIdParam ? Number(repoIdParam) : undefined}
          initialSchemaName={searchQueryParam}
        />
      ) : (
        <ProtoDependenciesGraphPane
          selectedSchemaId={inspectedGraphNode?.id ?? null}
          onSelectNode={(node, graph) => {
            setInspectedGraphNode(node);
            setInspectedGraphData(graph);
          }}
        />
      )}

      <ProtoGraphNodeInspectDrawer
        open={Boolean(inspectedGraphNode)}
        onClose={() => setInspectedGraphNode(null)}
        node={inspectedGraphNode}
        graph={inspectedGraphData}
        isAdmin={isAdmin}
        onOpenDocs={handleOpenDocsFromGraph}
        onOpenSources={handleOpenSourcesFromGraph}
        onPreviewSourceFile={(sid, fp) => void sourceAnchorDrawer.openBySchemaFile(sid, fp, 1)}
        onSwitchNode={(targetNode) => setInspectedGraphNode(targetNode)}
        onOpenPipeline={isAdmin ? handleOpenPipeline : undefined}
      />

      <ProtoSourceAnchorDrawer drawer={sourceAnchorDrawer} />
    </div>
  );
};

export default ProtoDocsWorkbenchPage;
