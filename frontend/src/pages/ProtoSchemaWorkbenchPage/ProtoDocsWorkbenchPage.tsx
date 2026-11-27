import React, { useCallback, useEffect, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { BookOpen, ListTree, Network } from 'lucide-react';
import { usePaginationState } from '@code/common';
import { useModuleUser } from '../../context/ModuleUserContext';
import { useModulePath } from '../../hooks/useModulePath';
import { useToast } from '../../utils/toast';
import * as schemaApi from '../../api/schema';
import type {
  ProtoCategory,
  ProtoDocTreeResponse,
  ProtoOperation,
  ProtoSchemaLedgerItemDTO,
  ProtoServiceDocDetailDTO,
  ServiceContractGroupDTO,
} from '../../types';
import { LivingProtoDocsPane } from './LivingProtoDocsPane';
import { OperationMappingTable } from './OperationMappingTable';
import { OperationDocDrawer } from './OperationDocDrawer';
import { SchemaAssetInspectDrawer } from './SchemaAssetInspectDrawer';
import { ProtoSourceAnchorDrawer, useSourceAnchorDrawer } from './ProtoSourceAnchorDrawer';
import { UnifiedCatalogPane } from './UnifiedCatalogPane';
import { ProtoDependenciesGraphPane } from './ProtoDependenciesGraphPane';
import './ProtoDocsAndCodeViewer.css';

export type ProtoDocsViewMode = 'reference' | 'catalog' | 'graph';

export interface ProtoDocsWorkbenchPageProps {
  initialTree?: ProtoDocTreeResponse;
  initialDetail?: ProtoServiceDocDetailDTO | null;
  initialCatalogGroups?: ServiceContractGroupDTO[];
  initialCatalogTotal?: number;
  initialAvailableSchemas?: ProtoSchemaLedgerItemDTO[];
}

/**
 * 接口参考文档与契约认领目录工作台 (/proto-docs)
 * 聚焦日常研发与架构认领高频场景，剥离底层 .pb 上传与资产池汇聚干扰条。
 */
export const ProtoDocsWorkbenchPage: React.FC<ProtoDocsWorkbenchPageProps> = ({
  initialTree,
  initialDetail,
  initialCatalogGroups,
  initialCatalogTotal,
  initialAvailableSchemas,
}) => {
  const [searchParams, setSearchParams] = useSearchParams();
  const navigate = useNavigate();
  const resolvePath = useModulePath();
  const { user } = useModuleUser();
  const toast = useToast();
  const isAdmin = Boolean(user?.is_module_admin || user?.is_super_admin);

  const rawDocsView = searchParams.get('docs_view');
  const rawView = searchParams.get('view');
  const legacyTab = searchParams.get('tab');
  const radarStateQuery = searchParams.get('state') || '';
  const serviceNameQuery = searchParams.get('service_name') || '';
  const schemaIdQuery = searchParams.get('schema_id') || '';
  const claimedStatusQuery = searchParams.get('claimed_status');
  const searchQueryParam = searchParams.get('search') || '';

  const docsView: ProtoDocsViewMode =
    rawDocsView === 'graph' || rawView === 'graph' || legacyTab === 'graph'
      ? 'graph'
      : rawDocsView === 'catalog' ||
        rawDocsView === 'catalog-ops' ||
        rawDocsView === 'unified-catalog' ||
        rawView === 'catalog' ||
        rawView === 'catalog-ops' ||
        rawView === 'unified-catalog' ||
        (!rawDocsView &&
          !rawView &&
          (claimedStatusQuery === 'UNCLAIMED' ||
            legacyTab === 'catalog' ||
            Boolean(radarStateQuery)))
      ? 'catalog'
      : 'reference';

  const setDocsView = (mode: ProtoDocsViewMode) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set('docs_view', mode);
      next.delete('view');
      next.delete('tab');
      if (mode !== 'catalog') {
        next.delete('state');
      }
      return next;
    });
  };

  const sourceAnchorDrawer = useSourceAnchorDrawer();

  // 契约认领与方法目录分页状态
  const {
    page: catalogPage,
    pageSize: catalogPageSize,
    setPage: setCatalogPage,
    setPageSize: setCatalogPageSize,
  } = usePaginationState({ defaultPageSize: 25 });

  const [catalogGroups, setCatalogGroups] = useState<ServiceContractGroupDTO[]>(
    initialCatalogGroups ?? []
  );
  const [totalCatalogServices, setTotalCatalogServices] = useState<number>(
    initialCatalogTotal ?? initialCatalogGroups?.length ?? 0
  );
  const [catalogLoading, setCatalogLoading] = useState(false);
  const [orphanOnly, setOrphanOnly] = useState(claimedStatusQuery === 'UNCLAIMED');
  const [availableSchemas, setAvailableSchemas] = useState<ProtoSchemaLedgerItemDTO[]>(
    initialAvailableSchemas ?? []
  );

  const [docDrawerOpen, setDocDrawerOpen] = useState(false);
  const [selectedOp, setSelectedOp] = useState<ProtoOperation | null>(null);

  const [inspectDrawerOpen, setInspectDrawerOpen] = useState(false);
  const [inspectSchema, setInspectSchema] = useState<ProtoSchemaLedgerItemDTO | null>(null);

  useEffect(() => {
    setOrphanOnly(claimedStatusQuery === 'UNCLAIMED');
  }, [claimedStatusQuery]);

  const loadCatalogServices = useCallback(async () => {
    setCatalogLoading(true);
    try {
      const res = await schemaApi.listProtoServices({
        schema_id: schemaIdQuery || undefined,
        claimed_status: orphanOnly ? 'UNCLAIMED' : undefined,
        service_name: serviceNameQuery || undefined,
        search: searchQueryParam || undefined,
        page: catalogPage,
        page_size: catalogPageSize,
      });
      setCatalogGroups(res.items ?? []);
      setTotalCatalogServices(res.total ?? 0);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '加载全局服务目录失败');
    } finally {
      setCatalogLoading(false);
    }
  }, [schemaIdQuery, orphanOnly, serviceNameQuery, searchQueryParam, catalogPage, catalogPageSize, toast]);

  useEffect(() => {
    if (docsView !== 'catalog') {
      return;
    }
    if (initialCatalogGroups === undefined) {
      void loadCatalogServices();
    }
    if (initialAvailableSchemas === undefined && availableSchemas.length === 0) {
      void schemaApi
        .listProtoSchemas({ page_size: 100, latest_only: true })
        .then((res) => setAvailableSchemas(res.items ?? []))
        .catch(() => {});
    }
  }, [
    docsView,
    initialCatalogGroups,
    initialAvailableSchemas,
    availableSchemas.length,
    loadCatalogServices,
  ]);

  const handleInspectSchemaById = async (schemaId: string | number) => {
    const found = availableSchemas.find((item) => String(item.id) === String(schemaId));
    if (found) {
      setInspectSchema(found);
      setInspectDrawerOpen(true);
      return;
    }
    try {
      const summary = await schemaApi.getProtoSchemaSummary(schemaId);
      if (summary?.schema) {
        const item: ProtoSchemaLedgerItemDTO = {
          ...summary.schema,
          id: summary.schema.id,
          name: summary.schema.name,
          version: summary.schema.version,
          category: (summary.schema.category as ProtoCategory) || 'BUSINESS',
          file_name: summary.schema.file_name || `${summary.schema.name}.pb`,
          file_size: summary.schema.file_size || 0,
          is_active: summary.schema.is_active,
          proto_file_count: summary.proto_files.length,
          service_count: summary.services_summary.length,
          operation_count: summary.schema.operation_count || 0,
          proto_files: summary.proto_files,
          version_count: 1,
          latest_version: summary.schema.version,
          claimed_service_count: summary.services_summary.filter((s) => s.is_claimed).length,
          claimed_ratio: summary.claimed_ratio,
          resolved_dependencies: [],
        };
        setInspectSchema(item);
        setInspectDrawerOpen(true);
      }
    } catch {
      toast.error('未找到对应描述集元数据');
    }
  };

  const navigateToAssetManager = (schemaId: string | number) => {
    setInspectDrawerOpen(false);
    navigate(resolvePath(`/proto-pipeline?stage=publish&schema_id=${schemaId}`));
  };

  return (
    <div className="pdoc-page-shell" data-testid="proto-docs-workbench-page">
      <div className="pdoc-page-header">
        <div className="pdoc-page-header__meta">
          <h2 className="pdoc-page-header__title">
            <BookOpen size={17} />
            <span>接口参考文档与认领目录</span>
          </h2>
          <p className="pdoc-page-header__desc">
            全注释驱动 RPC 服务说明书、字段字典、黄金样本 JSON 与未认领服务契约目录检索。
          </p>
        </div>

        <div className="code-tab-pills" role="tablist" aria-label="接口文档视图切换">
          <button
            type="button"
            role="tab"
            aria-selected={docsView === 'reference'}
            className={`code-pill-btn ${docsView === 'reference' ? 'code-pill-btn--active' : ''}`}
            onClick={() => setDocsView('reference')}
          >
            <BookOpen size={14} /> 全注释参考文档
          </button>
          <button
            type="button"
            role="tab"
            data-testid="pdoc-tab-unified-catalog"
            aria-selected={docsView === 'catalog'}
            className={`code-pill-btn ${docsView === 'catalog' ? 'code-pill-btn--active' : ''}`}
            onClick={() => setDocsView('catalog')}
          >
            <ListTree size={14} /> 契约目录与五态雷达
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
      ) : docsView === 'graph' ? (
        <ProtoDependenciesGraphPane onNavigateToSchemaAsset={navigateToAssetManager} />
      ) : (
        <div className="pdoc-catalog-unified-stack" style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
          <UnifiedCatalogPane
            initialState={radarStateQuery}
            isAdmin={isAdmin}
            onStateChange={(nextState) => {
              setSearchParams((prev) => {
                const next = new URLSearchParams(prev);
                next.set('docs_view', 'catalog');
                if (nextState) next.set('state', nextState);
                else next.delete('state');
                return next;
              });
            }}
          />
          <OperationMappingTable
            serviceGroups={catalogGroups}
            totalServices={totalCatalogServices}
            page={catalogPage}
            pageSize={catalogPageSize}
            onPageChange={setCatalogPage}
            onPageSizeChange={setCatalogPageSize}
            searchKeyword={searchQueryParam}
            loading={catalogLoading}
            onSelectOperation={(op) => {
              setSelectedOp(op);
              setDocDrawerOpen(true);
            }}
            onInspectSchema={(sid) => void handleInspectSchemaById(sid)}
            orphanOnly={orphanOnly}
            onOrphanOnlyChange={(val) => {
              setOrphanOnly(val);
              setSearchParams((prev) => {
                const next = new URLSearchParams(prev);
                if (val) next.set('claimed_status', 'UNCLAIMED');
                else next.delete('claimed_status');
                next.set('page', '1');
                return next;
              });
              setCatalogPage(1);
            }}
            selectedSchemaId={schemaIdQuery}
            onSelectSchemaId={(sid) => {
              setSearchParams((prev) => {
                const next = new URLSearchParams(prev);
                if (sid) next.set('schema_id', sid);
                else next.delete('schema_id');
                next.set('page', '1');
                return next;
              });
              setCatalogPage(1);
            }}
            availableSchemas={availableSchemas}
          />
        </div>
      )}

      <OperationDocDrawer
        open={docDrawerOpen}
        onClose={() => {
          setDocDrawerOpen(false);
          setSelectedOp(null);
        }}
        operation={selectedOp}
        isAdmin={isAdmin}
        onOperationUpdated={(updated) => {
          setCatalogGroups((prev) =>
            prev.map((grp) => ({
              ...grp,
              operations: (grp.operations || []).map((op) =>
                op.id === updated.id ? { ...op, ...updated } : op
              ),
            }))
          );
        }}
      />

      <SchemaAssetInspectDrawer
        open={inspectDrawerOpen}
        onClose={() => {
          setInspectDrawerOpen(false);
          setInspectSchema(null);
        }}
        schema={inspectSchema}
        isAdmin={isAdmin}
        onActivate={async (s) => navigateToAssetManager(s.id)}
        onDeactivate={async (s) => navigateToAssetManager(s.id)}
        onDelete={async (s) => navigateToAssetManager(s.id)}
        onNavigateToCatalog={(sid) => {
          setInspectDrawerOpen(false);
          setSearchParams((prev) => {
            const next = new URLSearchParams(prev);
            next.set('docs_view', 'reference');
            next.set('schema_id', String(sid));
            return next;
          });
        }}
      />

      <ProtoSourceAnchorDrawer drawer={sourceAnchorDrawer} />
    </div>
  );
};

export default ProtoDocsWorkbenchPage;
