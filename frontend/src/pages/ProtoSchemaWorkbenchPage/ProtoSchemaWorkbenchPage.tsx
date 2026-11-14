import React, { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { BookOpen, FolderKanban, Network, ShieldCheck } from 'lucide-react';
import { Modal, usePaginationState } from '@code/common';
import { useToast } from '../../utils/toast';
import { useModuleUser } from '../../context/ModuleUserContext';
import { useModulePath } from '../../hooks/useModulePath';
import * as schemaApi from '../../api/schema';
import type {
  ProtoCategory,
  ProtoSchemaLedgerItemDTO,
  ProtoPoolSummaryDTO,
  ConflictOrBrokenResponse,
  BreakingRuleViolation,
  AffectedDependent,
} from '../../types';
import { SchemaPoolSummaryBar } from './SchemaPoolSummaryBar';
import { SchemaAssetLedgerTable } from './SchemaAssetLedgerTable';
import { SchemaAssetInspectDrawer } from './SchemaAssetInspectDrawer';
import { SchemaUploadModal, type SchemaUploadMode } from './SchemaUploadModal';
import { SchemaConflictModal } from './SchemaConflictModal';
import { BreakingChangeAlertDrawer } from '../ProtoSchemasPage/BreakingChangeAlertDrawer';
import { ProtoDependenciesGraphPane } from './ProtoDependenciesGraphPane';
import './ProtoDocsAndCodeViewer.css';

export type SchemaWorkbenchTab = 'assets' | 'graph';

/**
 * 描述集与跨包依赖工作台 (/schemas)
 * 专注 .pb 描述集版本生命周期、生效池仲裁、在线编译入池与跨描述集 Import 依赖关系图。
 */
export const ProtoSchemaWorkbenchPage: React.FC = () => {
  const [searchParams, setSearchParams] = useSearchParams();
  const navigate = useNavigate();
  const resolvePath = useModulePath();
  const { user } = useModuleUser();
  const toast = useToast();
  const isAdmin = Boolean(user?.is_module_admin || user?.is_super_admin);

  const rawTab = searchParams.get('tab');
  const schemaIdQuery = searchParams.get('schema_id');
  const activeTab: SchemaWorkbenchTab = rawTab === 'graph' ? 'graph' : 'assets';

  const setTab = (tab: SchemaWorkbenchTab) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set('tab', tab);
      return next;
    });
  };

  const [poolSummary, setPoolSummary] = useState<ProtoPoolSummaryDTO | null>(null);

  const {
    page: assetPage,
    pageSize: assetPageSize,
    setPage: setAssetPage,
    setPageSize: setAssetPageSize,
  } = usePaginationState({ defaultPageSize: 25 });

  const selectedCategory = searchParams.get('category') || '';
  const activeOnlyParam = searchParams.get('active_only');
  const activeStatusFilter =
    searchParams.get('status') || (activeOnlyParam === 'true' ? 'ACTIVE' : 'ALL');
  const sourceTypeFilter = searchParams.get('source_type') || '';
  const assetSearch = searchParams.get('search') || '';

  const [assetSchemas, setAssetSchemas] = useState<ProtoSchemaLedgerItemDTO[]>([]);
  const [totalAssetSchemas, setTotalAssetSchemas] = useState(0);
  const [assetLoading, setAssetLoading] = useState(false);

  // 弹窗与抽屉受控状态
  const [uploadModalOpen, setUploadModalOpen] = useState(false);
  const [uploadModalMode, setUploadModalMode] = useState<SchemaUploadMode>('upload');
  const [targetSchemaForNewVer, setTargetSchemaForNewVer] = useState<ProtoSchemaLedgerItemDTO | null>(
    null
  );

  const [inspectDrawerOpen, setInspectDrawerOpen] = useState(false);
  const [inspectSchema, setInspectSchema] = useState<ProtoSchemaLedgerItemDTO | null>(null);

  const [conflictModalOpen, setConflictModalOpen] = useState(false);
  const [conflictData, setConflictData] = useState<ConflictOrBrokenResponse | null>(null);

  const [breakingDrawerOpen, setBreakingDrawerOpen] = useState(false);
  const [breakingDrawerData, setBreakingDrawerData] = useState<{
    schemaId: string | number | null;
    batchIds: (string | number)[] | null;
    breakingRules: BreakingRuleViolation[];
    affectedDependents: AffectedDependent[];
    onCustomOverride?: (reason: string) => Promise<void>;
  } | null>(null);
  const [isOverriding, setIsOverriding] = useState(false);

  const [deleteConfirmOpen, setDeleteConfirmOpen] = useState(false);
  const [schemaToDelete, setSchemaToDelete] = useState<ProtoSchemaLedgerItemDTO | null>(null);

  const loadAssetLedger = useCallback(async () => {
    setAssetLoading(true);
    try {
      const res = await schemaApi.listProtoSchemas({
        category: selectedCategory || undefined,
        active_only:
          activeStatusFilter === 'ACTIVE'
            ? true
            : activeStatusFilter === 'INACTIVE'
              ? false
              : undefined,
        source_type: sourceTypeFilter || undefined,
        search: assetSearch || undefined,
        latest_only: true,
        page: assetPage,
        page_size: assetPageSize,
      });
      setAssetSchemas(res.items ?? []);
      setTotalAssetSchemas(res.total ?? 0);
      if (res.pool_summary) {
        setPoolSummary(res.pool_summary);
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '加载描述集资产台账失败');
    } finally {
      setAssetLoading(false);
    }
  }, [selectedCategory, activeStatusFilter, sourceTypeFilter, assetSearch, assetPage, assetPageSize, toast]);

  useEffect(() => {
    if (activeTab === 'assets') {
      void loadAssetLedger();
    }
  }, [activeTab, loadAssetLedger]);

  // 停留在 graph 视图时，补拉取一次全局生效池摘要条
  useEffect(() => {
    if (!poolSummary && activeTab === 'graph') {
      void schemaApi
        .listProtoSchemas({ page_size: 1, latest_only: true })
        .then((res) => {
          if (res.pool_summary) setPoolSummary(res.pool_summary);
        })
        .catch(() => {});
    }
  }, [poolSummary, activeTab]);

  const handleCategoryChange = (cat: string) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      if (cat) next.set('category', cat);
      else next.delete('category');
      next.set('page', '1');
      return next;
    });
    setAssetPage(1);
  };

  const handleStatusFilterChange = (status: string) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.delete('active_only');
      if (status !== 'ALL') next.set('status', status);
      else next.delete('status');
      next.set('page', '1');
      return next;
    });
    setAssetPage(1);
  };

  const handleSourceTypeFilterChange = (sourceType: string) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      if (sourceType) next.set('source_type', sourceType);
      else next.delete('source_type');
      next.set('page', '1');
      return next;
    });
    setAssetPage(1);
  };

  const handleSearchChange = (keyword: string) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      if (keyword.trim()) next.set('search', keyword.trim());
      else next.delete('search');
      next.set('page', '1');
      return next;
    });
    setAssetPage(1);
  };

  const handleActivateSchema = async (s: ProtoSchemaLedgerItemDTO) => {
    try {
      const res = await schemaApi.activateProtoSchema(s.id);
      toast.success(`描述集 [${s.name} v${s.version}] 激活成功，生效 ${res.activated_operations} 个服务契约`);
      void loadAssetLedger();
      if (inspectDrawerOpen && inspectSchema?.name === s.name) {
        setInspectSchema(res.schema);
      }
    } catch (err: unknown) {
      const respData = (err as unknown as { responseData?: ConflictOrBrokenResponse })?.responseData;
      if (
        respData &&
        (respData.code === 'BREAKING_CHANGE_DETECTED' ||
          (respData.breaking_rules && respData.breaking_rules.length > 0))
      ) {
        setBreakingDrawerData({
          schemaId: s.id,
          batchIds: null,
          breakingRules: respData.breaking_rules || [],
          affectedDependents: respData.affected_dependents || [],
        });
        setBreakingDrawerOpen(true);
        return;
      }
      if (respData && (respData.conflicts || respData.broken_dependents)) {
        setConflictData(respData);
        setConflictModalOpen(true);
        return;
      }
      toast.error(err instanceof Error ? err.message : '激活描述集失败');
    }
  };

  const handleDeactivateSchema = async (s: ProtoSchemaLedgerItemDTO) => {
    try {
      const res = await schemaApi.deactivateProtoSchema(s.id);
      toast.success(`描述集 [${s.name} v${s.version}] 已停用，卸载 ${res.deactivated_operations} 个服务契约`);
      void loadAssetLedger();
      if (inspectDrawerOpen && inspectSchema?.name === s.name) {
        setInspectSchema(res.schema);
      }
    } catch (err: unknown) {
      const respData = (err as unknown as { responseData?: ConflictOrBrokenResponse })?.responseData;
      if (respData && (respData.conflicts || respData.broken_dependents)) {
        setConflictData(respData);
        setConflictModalOpen(true);
        return;
      }
      toast.error(err instanceof Error ? err.message : '停用描述集失败');
    }
  };

  const handleBatchActivate = async (selectedIds: (string | number)[]) => {
    try {
      const res = await schemaApi.batchActivateProtoSchemas(selectedIds);
      toast.success(res.message || `成功批量激活 ${res.activated_count} 个描述集`);
      void loadAssetLedger();
    } catch (err: unknown) {
      const respData = (err as unknown as { responseData?: ConflictOrBrokenResponse })?.responseData;
      if (
        respData &&
        (respData.code === 'BREAKING_CHANGE_DETECTED' ||
          (respData.breaking_rules && respData.breaking_rules.length > 0))
      ) {
        setBreakingDrawerData({
          schemaId: null,
          batchIds: selectedIds,
          breakingRules: respData.breaking_rules || [],
          affectedDependents: respData.affected_dependents || [],
        });
        setBreakingDrawerOpen(true);
        return;
      }
      if (respData && (respData.conflicts || respData.broken_dependents)) {
        setConflictData(respData);
        setConflictModalOpen(true);
        return;
      }
      toast.error(err instanceof Error ? err.message : '批量激活失败');
    }
  };

  const handleConfirmBreakingOverride = async (reason: string) => {
    if (!breakingDrawerData) return;
    setIsOverriding(true);
    try {
      if (breakingDrawerData.onCustomOverride) {
        await breakingDrawerData.onCustomOverride(reason);
      } else if (breakingDrawerData.schemaId) {
        const res = await schemaApi.activateProtoSchema(breakingDrawerData.schemaId, {
          allow_breaking: true,
          breaking_reason: reason,
        });
        toast.success(`已强制激活破坏性版本，生效 ${res.activated_operations} 个服务契约 (已记录 P1 审计)`);
      } else if (breakingDrawerData.batchIds) {
        const res = await schemaApi.batchActivateProtoSchemas(breakingDrawerData.batchIds, {
          allow_breaking: true,
          breaking_reason: reason,
        });
        toast.success(res.message || `已强制批量激活 ${res.activated_count} 个描述集 (已记录 P1 审计)`);
      }
      setBreakingDrawerOpen(false);
      setBreakingDrawerData(null);
      void loadAssetLedger();
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : '强制激活操作失败');
    } finally {
      setIsOverriding(false);
    }
  };

  const handleDeleteSchema = (s: ProtoSchemaLedgerItemDTO) => {
    setSchemaToDelete(s);
    setDeleteConfirmOpen(true);
  };

  const confirmDeleteSchema = async () => {
    if (!schemaToDelete) return;
    try {
      await schemaApi.deleteProtoSchema(schemaToDelete.id);
      toast.success(`描述集 [${schemaToDelete.name} v${schemaToDelete.version}] 已彻底删除`);
      setDeleteConfirmOpen(false);
      setSchemaToDelete(null);
      void loadAssetLedger();
      if (inspectDrawerOpen && inspectSchema?.id === schemaToDelete.id) {
        setInspectDrawerOpen(false);
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '删除描述集失败');
    }
  };

  const handleInspectSchema = (s: ProtoSchemaLedgerItemDTO) => {
    setInspectSchema(s);
    setInspectDrawerOpen(true);
  };

  const handleInspectSchemaById = async (schemaId: string | number) => {
    const found = assetSchemas.find((item) => String(item.id) === String(schemaId));
    if (found) {
      handleInspectSchema(found);
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
        handleInspectSchema(item);
      }
    } catch {
      toast.error('未找到对应描述集元数据');
    }
  };

  const handleNavigateToCatalog = (schemaId: string | number) => {
    setInspectDrawerOpen(false);
    navigate(resolvePath(`/proto-docs?schema_id=${schemaId}`));
  };

  const handleNavigateToSchemaAsset = (schemaId: string | number) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set('tab', 'assets');
      next.set('schema_id', String(schemaId));
      return next;
    });
    void handleInspectSchemaById(schemaId);
  };

  return (
    <div className="code-proto-schema-workbench" data-testid="proto-schemas-workbench-page">
      <SchemaPoolSummaryBar
        summary={poolSummary}
        isAdmin={isAdmin}
        onUploadClick={() => {
          setUploadModalMode('upload');
          setTargetSchemaForNewVer(null);
          setUploadModalOpen(true);
        }}
        onCompileClick={() => {
          setUploadModalMode('compile');
          setTargetSchemaForNewVer(null);
          setUploadModalOpen(true);
        }}
      />

      <div className="pdoc-top-tabs">
        <div className="pdoc-top-tabs__list" role="tablist" aria-label="描述集资产与依赖视图">
          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'assets'}
            onClick={() => setTab('assets')}
            className={`pdoc-top-tab ${activeTab === 'assets' ? 'pdoc-top-tab--active' : ''}`}
          >
            <FolderKanban size={15} />
            <span>描述集资产台账</span>
          </button>

          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'graph'}
            onClick={() => setTab('graph')}
            className={`pdoc-top-tab ${activeTab === 'graph' ? 'pdoc-top-tab--active' : ''}`}
          >
            <Network size={15} />
            <span>跨包依赖关系图</span>
          </button>
        </div>

        <div className="pschema-quicklinks">
          <Link to={resolvePath('/proto-docs')} className="btn btn-secondary">
            <BookOpen size={14} />
            <span>接口参考文档</span>
          </Link>
          <Link to={resolvePath('/proto-governance')} className="btn btn-secondary">
            <ShieldCheck size={14} />
            <span>MR 门禁与规范</span>
          </Link>
        </div>
      </div>

      {activeTab === 'assets' && (
        <SchemaAssetLedgerTable
          schemas={assetSchemas}
          total={totalAssetSchemas}
          page={assetPage}
          pageSize={assetPageSize}
          onPageChange={setAssetPage}
          onPageSizeChange={setAssetPageSize}
          selectedCategory={selectedCategory}
          onCategoryChange={handleCategoryChange}
          activeStatusFilter={activeStatusFilter}
          onStatusFilterChange={handleStatusFilterChange}
          sourceTypeFilter={sourceTypeFilter}
          onSourceTypeFilterChange={handleSourceTypeFilterChange}
          highlightedSchemaId={schemaIdQuery}
          searchKeyword={assetSearch}
          onSearchChange={handleSearchChange}
          loading={assetLoading}
          isAdmin={isAdmin}
          onInspect={handleInspectSchema}
          onUploadNewVersion={(s) => {
            setUploadModalMode('upload');
            setTargetSchemaForNewVer(s);
            setUploadModalOpen(true);
          }}
          onActivate={(s) => void handleActivateSchema(s)}
          onDeactivate={(s) => void handleDeactivateSchema(s)}
          onBatchActivate={(ids) => void handleBatchActivate(ids)}
          onDelete={handleDeleteSchema}
          onDownload={(s) => void schemaApi.downloadProtoSchema(s.id, s.file_name)}
        />
      )}

      {activeTab === 'graph' && (
        <ProtoDependenciesGraphPane onNavigateToSchemaAsset={handleNavigateToSchemaAsset} />
      )}

      <SchemaUploadModal
        open={uploadModalOpen}
        onClose={() => {
          setUploadModalOpen(false);
          setTargetSchemaForNewVer(null);
        }}
        initialName={targetSchemaForNewVer ? targetSchemaForNewVer.name : ''}
        initialCategory={targetSchemaForNewVer ? targetSchemaForNewVer.category : 'BUSINESS'}
        initialMode={uploadModalMode}
        onSuccess={() => {
          void loadAssetLedger();
        }}
        onConflict={(cData) => {
          setConflictData(cData);
          setConflictModalOpen(true);
        }}
        onBreakingChange={(bData, onOverrideSubmit) => {
          setBreakingDrawerData({
            schemaId: null,
            batchIds: null,
            breakingRules: bData.breaking_rules || [],
            affectedDependents: bData.affected_dependents || [],
            onCustomOverride: onOverrideSubmit,
          });
          setBreakingDrawerOpen(true);
        }}
      />

      <SchemaConflictModal
        open={conflictModalOpen}
        onClose={() => {
          setConflictModalOpen(false);
          setConflictData(null);
        }}
        conflictData={conflictData}
      />

      <BreakingChangeAlertDrawer
        isOpen={breakingDrawerOpen}
        onClose={() => {
          setBreakingDrawerOpen(false);
          setBreakingDrawerData(null);
        }}
        breakingRules={breakingDrawerData?.breakingRules || []}
        affectedDependents={breakingDrawerData?.affectedDependents || []}
        isAdmin={isAdmin}
        onConfirmOverride={handleConfirmBreakingOverride}
        isSubmitting={isOverriding}
      />

      <SchemaAssetInspectDrawer
        open={inspectDrawerOpen}
        onClose={() => {
          setInspectDrawerOpen(false);
          setInspectSchema(null);
        }}
        schema={inspectSchema}
        isAdmin={isAdmin}
        onActivate={handleActivateSchema}
        onDeactivate={handleDeactivateSchema}
        onDelete={async (s) => {
          handleDeleteSchema(s);
        }}
        onUploadNewVersion={(s) => {
          setInspectDrawerOpen(false);
          setTargetSchemaForNewVer(s);
          setUploadModalOpen(true);
        }}
        onNavigateToCatalog={handleNavigateToCatalog}
      />

      <Modal
        open={deleteConfirmOpen}
        onClose={() => {
          setDeleteConfirmOpen(false);
          setSchemaToDelete(null);
        }}
        title="确认删除描述集"
        footer={
          <div className="pschema-delete-modal__footer">
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => {
                setDeleteConfirmOpen(false);
                setSchemaToDelete(null);
              }}
            >
              取消
            </button>
            <button
              type="button"
              className="btn btn-danger pschema-delete-modal__confirm-btn"
              onClick={() => void confirmDeleteSchema()}
            >
              确认彻底删除
            </button>
          </div>
        }
      >
        <div className="pschema-delete-modal__body">
          <p className="pschema-delete-modal__title">
            确定要删除描述集版本{' '}
            <strong>
              {schemaToDelete?.name} v{schemaToDelete?.version}
            </strong>{' '}
            吗？
          </p>
          <p className="pschema-delete-modal__hint">
            删除后该版本对应的二进制产物与已解析的方法契约将被物理清除，且不可恢复。
          </p>
        </div>
      </Modal>
    </div>
  );
};

export default ProtoSchemaWorkbenchPage;
