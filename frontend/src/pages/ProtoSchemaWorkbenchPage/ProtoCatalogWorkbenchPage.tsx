import React, { useCallback, useEffect, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { ListTree, Radar } from 'lucide-react';
import { usePaginationState } from '@code/common';
import { useModuleUser } from '../../context/ModuleUserContext';
import { useModulePath } from '../../hooks/useModulePath';
import { useToast } from '../../utils/toast';
import * as schemaApi from '../../api/schema';
import type {
  CatalogServicesResponseDTO,
  ProtoCategory,
  ProtoOperation,
  ProtoSchemaLedgerItemDTO,
  ServiceContractGroupDTO,
} from '../../types';
import { UnifiedCatalogPane } from './UnifiedCatalogPane';
import { OperationMappingTable } from './OperationMappingTable';
import { OperationDocDrawer } from './OperationDocDrawer';
import { SchemaAssetInspectDrawer } from './SchemaAssetInspectDrawer';
import './ProtoDocsAndCodeViewer.css';

export type ProtoCatalogViewMode = 'radar' | 'methods';

export interface ProtoCatalogWorkbenchPageProps {
  initialCatalogGroups?: ServiceContractGroupDTO[];
  initialCatalogTotal?: number;
  initialAvailableSchemas?: ProtoSchemaLedgerItemDTO[];
  initialRadarData?: CatalogServicesResponseDTO | null;
}

/**
 * 契约认领与五态雷达工作台 (/proto-catalog)
 * 承接「架构与契约对齐」主线，统一核查契约库、架构元素与现网服务的对齐及认领状态。
 */
export const ProtoCatalogWorkbenchPage: React.FC<ProtoCatalogWorkbenchPageProps> = ({
  initialCatalogGroups,
  initialCatalogTotal,
  initialAvailableSchemas,
  initialRadarData = null,
}) => {
  const [searchParams, setSearchParams] = useSearchParams();
  const navigate = useNavigate();
  const resolvePath = useModulePath();
  const { user } = useModuleUser();
  const toast = useToast();
  const isAdmin = Boolean(user?.is_module_admin || user?.is_super_admin);

  const rawCatalogView = searchParams.get('catalog_view');
  const radarStateQuery = searchParams.get('state') || '';
  const serviceNameQuery = searchParams.get('service_name') || '';
  const schemaIdQuery = searchParams.get('schema_id') || '';
  const claimedStatusQuery = searchParams.get('claimed_status');
  const searchQueryParam = searchParams.get('search') || '';

  const catalogView: ProtoCatalogViewMode =
    rawCatalogView === 'methods'
      ? 'methods'
      : rawCatalogView === 'radar'
        ? 'radar'
        : claimedStatusQuery === 'UNCLAIMED'
          ? 'methods'
          : 'radar';

  const setCatalogView = (mode: ProtoCatalogViewMode) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set('catalog_view', mode);
      if (mode !== 'radar') {
        next.delete('state');
      } else {
        next.delete('claimed_status');
      }
      return next;
    });
  };

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
      toast.error(err instanceof Error ? err.message : '加载服务方法认领目录失败');
    } finally {
      setCatalogLoading(false);
    }
  }, [schemaIdQuery, orphanOnly, serviceNameQuery, searchQueryParam, catalogPage, catalogPageSize, toast]);

  useEffect(() => {
    if (catalogView !== 'methods') {
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
    catalogView,
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
    <div className="pdoc-page-shell" data-testid="proto-catalog-workbench-page">
      <div className="pdoc-page-header">
        <div className="pdoc-page-header__meta">
          <h2 className="pdoc-page-header__title">
            <Radar size={17} />
            <span>契约认领与五态雷达</span>
          </h2>
          <p className="pdoc-page-header__desc">
            对齐「契约库 ↔ 架构元素 ↔ 现网发现」，集中排查无契约在线、已上线未认领等偏差并完成服务认领归属。
          </p>
        </div>

        <div className="code-tab-pills" role="tablist" aria-label="契约认领与雷达视图切换">
          <button
            type="button"
            role="tab"
            data-testid="pcatalog-tab-radar"
            aria-selected={catalogView === 'radar'}
            className={`code-pill-btn ${catalogView === 'radar' ? 'code-pill-btn--active' : ''}`}
            onClick={() => setCatalogView('radar')}
          >
            <Radar size={14} /> 五态健康雷达
          </button>
          <button
            type="button"
            role="tab"
            data-testid="pcatalog-tab-methods"
            aria-selected={catalogView === 'methods'}
            className={`code-pill-btn ${catalogView === 'methods' ? 'code-pill-btn--active' : ''}`}
            onClick={() => setCatalogView('methods')}
          >
            <ListTree size={14} /> 服务方法认领目录
          </button>
        </div>
      </div>

      {catalogView === 'radar' ? (
        <UnifiedCatalogPane
          initialState={radarStateQuery}
          initialData={initialRadarData}
          isAdmin={isAdmin}
          onStateChange={(nextState) => {
            setSearchParams((prev) => {
              const next = new URLSearchParams(prev);
              next.set('catalog_view', 'radar');
              if (nextState) next.set('state', nextState);
              else next.delete('state');
              return next;
            });
          }}
        />
      ) : (
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
              next.set('catalog_view', 'methods');
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
              next.set('catalog_view', 'methods');
              if (sid) next.set('schema_id', sid);
              else next.delete('schema_id');
              next.set('page', '1');
              return next;
            });
            setCatalogPage(1);
          }}
          availableSchemas={availableSchemas}
        />
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
          navigate(resolvePath(`/proto-docs?docs_view=reference&schema_id=${sid}`));
        }}
      />
    </div>
  );
};

export default ProtoCatalogWorkbenchPage;
