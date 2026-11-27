import { describe, it, expect, vi } from 'vitest';
import { renderToString } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import {
  ProtoDependenciesGraphPane,
  SOURCE_TYPE_LABEL,
  computeSchemaNodeRelations,
  filterProtoGraphByCategory,
  layoutProtoDependenciesGraph,
  getSchemaNodeNavigationTarget,
} from '../ProtoDependenciesGraphPane';
import { ProtoGraphNodeInspectContent } from '../ProtoGraphNodeInspectDrawer';
import { ProtoSchemaWorkbenchPage } from '../ProtoSchemaWorkbenchPage';
import { SchemaAssetLedgerTable } from '../SchemaAssetLedgerTable';
import type {
  ProtoDependenciesGraphDTO,
  ProtoSchemaLedgerItemDTO,
  ProtoSchemaSummaryDTO,
} from '../../../types';

vi.mock('../../../utils/toast', () => ({
  useToast: () => ({
    success: vi.fn(),
    error: vi.fn(),
    info: vi.fn(),
    warning: vi.fn(),
  }),
}));

vi.mock('../../../context/ModuleUserContext', () => ({
  useModuleUser: () => ({
    user: { id: 1, username: 'admin', is_module_admin: true, is_super_admin: true },
    loadingUser: false,
  }),
}));

vi.mock('../../../api/schema', () => ({
  listProtoSchemas: vi.fn().mockResolvedValue({ items: [], total: 0 }),
  listProtoServices: vi.fn().mockResolvedValue({ items: [], total: 0 }),
  getProtoDependenciesGraph: vi.fn().mockResolvedValue({
    nodes: [
      {
        id: 1,
        name: 'common-base-lib',
        version: 2,
        category: 'BASE_LIB',
        source_type: 'ONLINE_COMPILE',
        proto_file_count: 1,
        service_count: 0,
        operation_count: 0,
        is_active: true,
      },
      {
        id: 2,
        name: 'order-service',
        version: 1,
        category: 'BUSINESS',
        source_type: 'UPLOAD_PB',
        proto_file_count: 1,
        service_count: 1,
        operation_count: 3,
        is_active: true,
      },
    ],
    edges: [
      {
        from_schema_id: 2,
        to_schema_id: 1,
        matched_protos: ['common/v1/base.proto'],
      },
    ],
  }),
}));

describe('M22-P3 ProtoDependenciesGraphPane (/schemas?tab=graph)', () => {
  const sampleGraph: ProtoDependenciesGraphDTO = {
    nodes: [
      {
        id: 10,
        name: 'common-base',
        version: 3,
        category: 'BASE_LIB',
        source_type: 'ONLINE_COMPILE',
        proto_file_count: 2,
        service_count: 0,
        operation_count: 0,
        is_active: true,
      },
      {
        id: 20,
        name: 'trade-core',
        version: 2,
        category: 'BUSINESS',
        source_type: 'UPLOAD_PB',
        proto_file_count: 2,
        service_count: 2,
        operation_count: 6,
        is_active: true,
      },
      {
        id: 30,
        name: 'edge-bff',
        version: 1,
        category: 'GATEWAY',
        source_type: 'GIT_SYNC',
        proto_file_count: 1,
        service_count: 1,
        operation_count: 2,
        is_active: false,
      },
    ],
    edges: [
      {
        from_schema_id: 20,
        to_schema_id: 10,
        matched_protos: ['common/base.proto'],
      },
      {
        from_schema_id: 30,
        to_schema_id: 20,
        matched_protos: ['trade/order.proto'],
      },
    ],
  };

  it('filters graph nodes and dependent edges by category (BASE_LIB/BUSINESS/GATEWAY)', () => {
    const allFiltered = filterProtoGraphByCategory(sampleGraph, 'ALL');
    expect(allFiltered.nodes).toHaveLength(3);
    expect(allFiltered.edges).toHaveLength(2);

    const baseOnly = filterProtoGraphByCategory(sampleGraph, 'BASE_LIB');
    expect(baseOnly.nodes.map((n) => n.name)).toEqual(['common-base']);
    expect(baseOnly.edges).toHaveLength(0);

    const businessOnly = filterProtoGraphByCategory(sampleGraph, 'BUSINESS');
    expect(businessOnly.nodes.map((n) => n.name)).toEqual(['trade-core']);
    expect(businessOnly.edges).toHaveLength(0);

    const gatewayOnly = filterProtoGraphByCategory(sampleGraph, 'GATEWAY');
    expect(gatewayOnly.nodes.map((n) => n.name)).toEqual(['edge-bff']);
  });

  it('computes ELK layout positions for Protobuf dependency DAG and generates asset navigation target', async () => {
    const layouted = await layoutProtoDependenciesGraph(sampleGraph);
    expect(layouted.nodes).toHaveLength(3);
    expect(layouted.edges).toHaveLength(2);

    for (const node of layouted.nodes) {
      expect(Number.isFinite(node.position.x)).toBe(true);
      expect(Number.isFinite(node.position.y)).toBe(true);
    }

    const navParams = getSchemaNodeNavigationTarget('schema_20');
    expect(navParams).toEqual({ tab: 'assets', schema_id: '20' });
  });

  it('renders graph view toolbar with category filters and graph stage container', () => {
    const html = renderToString(
      <MemoryRouter initialEntries={['/schemas?tab=graph']}>
        <ProtoDependenciesGraphPane
          initialGraph={sampleGraph}
          onNavigateToSchemaAsset={vi.fn()}
        />
      </MemoryRouter>
    );

    expect(html).toContain('proto-graph-pane');
    expect(html).toContain('基础公共库');
    expect(html).toContain('业务域服务');
    expect(html).toContain('网关聚合层');
    expect(html).toContain('common-base');
    expect(html).toContain('trade-core');
  });

  it('mounts /schemas?tab=graph tab and online compile entry in ProtoSchemaWorkbenchPage and renders source_type badge + highlight', () => {
    const pageHtml = renderToString(
      <MemoryRouter initialEntries={['/schemas?tab=graph']}>
        <ProtoSchemaWorkbenchPage />
      </MemoryRouter>
    );
    expect(pageHtml).toContain('跨包依赖关系图');
    expect(pageHtml).not.toContain('在线源码直编 (.proto)');
    expect(pageHtml).toContain('前往契约入库流水线');
    expect(pageHtml).toContain('proto-graph-pane');

    const ledgerItems: ProtoSchemaLedgerItemDTO[] = [
      {
        id: 20,
        name: 'online-order-schema',
        version: 1,
        category: 'BUSINESS',
        source_type: 'ONLINE_COMPILE',
        file_name: 'online-order-schema.pb',
        file_size: 4096,
        is_active: true,
        proto_file_count: 1,
        service_count: 1,
        operation_count: 2,
        proto_files: ['order/order.proto'],
        version_count: 1,
        latest_version: 1,
        claimed_service_count: 1,
        claimed_ratio: 1,
        resolved_dependencies: [],
      },
    ];
    const tableHtml = renderToString(
      <MemoryRouter>
        <SchemaAssetLedgerTable
          schemas={ledgerItems}
          total={1}
          page={1}
          pageSize={25}
          onPageChange={vi.fn()}
          onPageSizeChange={vi.fn()}
          selectedCategory=""
          onCategoryChange={vi.fn()}
          activeStatusFilter="ALL"
          onStatusFilterChange={vi.fn()}
          sourceTypeFilter="ONLINE_COMPILE"
          onSourceTypeFilterChange={vi.fn()}
          highlightedSchemaId={20}
          searchKeyword=""
          onSearchChange={vi.fn()}
          loading={false}
          isAdmin={true}
          onInspect={vi.fn()}
          onUploadNewVersion={vi.fn()}
          onActivate={vi.fn()}
          onDeactivate={vi.fn()}
          onBatchActivate={vi.fn()}
          onDelete={vi.fn()}
          onDownload={vi.fn()}
        />
      </MemoryRouter>
    );
    expect(tableHtml).toContain('归档直编');
    expect(tableHtml).toContain('data-schema-id="20"');
    expect(tableHtml).toContain('按来源筛选');
  });

  it('computes upstream/downstream relations and maps GIT_PIPELINE source label', () => {
    expect(SOURCE_TYPE_LABEL.GIT_PIPELINE).toBe('流水线发布');

    const relations = computeSchemaNodeRelations(sampleGraph, 20);
    expect(relations.upstream).toHaveLength(1);
    expect(relations.upstream[0].node.name).toBe('common-base');
    expect(relations.upstream[0].matchedProtos).toEqual(['common/base.proto']);

    expect(relations.downstream).toHaveLength(1);
    expect(relations.downstream[0].node.name).toBe('edge-bff');
    expect(relations.downstream[0].matchedProtos).toEqual(['trade/order.proto']);
  });

  it('renders ProtoGraphNodeInspectContent differently for service packages vs zero-service base libs', () => {
    const bizSummary: ProtoSchemaSummaryDTO = {
      schema: {
        id: '20',
        name: 'trade-core',
        version: 2,
        category: 'BUSINESS',
        source_type: 'GIT_PIPELINE',
        git_repo_id: 77,
        is_active: true,
      },
      proto_files: ['trade/order.proto'],
      services_summary: [
        {
          service_name: 'trade.order.v1.OrderService',
          proto_file: 'trade/order.proto',
          proto_package: 'trade.order.v1',
          proto_service: 'OrderService',
          claimed_module_id: 5,
          claimed_module_name: '交易核心域',
          is_claimed: true,
          operation_count: 6,
          unary_count: 6,
          stream_count: 0,
        },
      ],
      claimed_ratio: 1,
    };

    const bizViewerHtml = renderToString(
      <ProtoGraphNodeInspectContent
        node={{ ...sampleGraph.nodes[1], source_type: 'GIT_PIPELINE', git_repo_id: 77 }}
        graph={sampleGraph}
        isAdmin={false}
        initialSummary={bizSummary}
        onOpenDocs={vi.fn()}
        onOpenSources={vi.fn()}
        onPreviewSourceFile={vi.fn()}
        onSwitchNode={vi.fn()}
        onOpenPipeline={vi.fn()}
      />
    );
    expect(bizViewerHtml).toContain('流水线发布');
    expect(bizViewerHtml).toContain('查看全注释参考文档');
    expect(bizViewerHtml).toContain('查看契约源码');
    expect(bizViewerHtml).toContain('trade.order.v1.OrderService');
    expect(bizViewerHtml).toContain('trade/order.proto');
    expect(bizViewerHtml).toContain('common-base');
    expect(bizViewerHtml).toContain('edge-bff');
    expect(bizViewerHtml).not.toContain('前往入库流水线管理版本');

    const bizAdminHtml = renderToString(
      <ProtoGraphNodeInspectContent
        node={{ ...sampleGraph.nodes[1], source_type: 'GIT_PIPELINE', git_repo_id: 77 }}
        graph={sampleGraph}
        isAdmin={true}
        initialSummary={bizSummary}
        onOpenDocs={vi.fn()}
        onOpenSources={vi.fn()}
        onPreviewSourceFile={vi.fn()}
        onSwitchNode={vi.fn()}
        onOpenPipeline={vi.fn()}
      />
    );
    expect(bizAdminHtml).toContain('前往入库流水线管理版本');

    const baseSummary: ProtoSchemaSummaryDTO = {
      schema: {
        id: '10',
        name: 'common-base',
        version: 3,
        category: 'BASE_LIB',
        source_type: 'GIT_PIPELINE',
        git_repo_id: 11,
        is_active: true,
      },
      proto_files: ['common/base.proto', 'common/types.proto'],
      services_summary: [],
      claimed_ratio: 0,
    };

    const baseHtml = renderToString(
      <ProtoGraphNodeInspectContent
        node={{ ...sampleGraph.nodes[0], source_type: 'GIT_PIPELINE', git_repo_id: 11 }}
        graph={sampleGraph}
        isAdmin={false}
        initialSummary={baseSummary}
        onOpenDocs={vi.fn()}
        onOpenSources={vi.fn()}
        onPreviewSourceFile={vi.fn()}
        onSwitchNode={vi.fn()}
      />
    );
    expect(baseHtml).toContain('该契约包不含 RPC Service，请在契约源码中查阅公共 Message / Enum 定义。');
    expect(baseHtml).toContain('查看契约源码');
    expect(baseHtml).not.toContain('查看全注释参考文档');
    expect(baseHtml).toContain('common/base.proto');
    expect(baseHtml).toContain('common/types.proto');
  });
});
