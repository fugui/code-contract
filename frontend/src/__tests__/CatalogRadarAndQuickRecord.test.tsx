import { describe, expect, it } from 'vitest';
import { renderToString } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { ModuleUserProvider } from '../context/ModuleUserContext';
import { DiscoveryBatchActionBar } from '../pages/DiscoveryWorkbenchPage/DiscoveryBatchActionBar';
import { DiscoveryProcessTreeTable } from '../pages/DiscoveryWorkbenchPage/DiscoveryProcessTreeTable';
import { FiveStateRadarCard } from '../pages/DashboardWorkbenchPage/FiveStateRadarCard';
import { UnifiedCatalogPane } from '../pages/ProtoSchemaWorkbenchPage/UnifiedCatalogPane';
import type {
  CatalogServicesResponseDTO,
  CurrentUserProfile,
  DiscoveryProcessGroupDTO,
  FiveStateRadarDTO,
} from '../types';

const adminUser: CurrentUserProfile = {
  id: 1,
  username: 'admin',
  name: '平台管理员',
  roles: ['contract_admin'],
  is_super_admin: true,
  is_module_admin: true,
  required_ad_role: 'contract_admin',
};

describe('CatalogRadarAndQuickRecord (M31)', () => {
  it('testDiscoveryQuickRecord_FiveStateRadarCard_AndCatalogConfirm', () => {
    // 1. DiscoveryBatchActionBar 渲染 [一键立即录制] 与 [批量纳管（不布防）]
    const actionBarHtml = renderToString(
      <DiscoveryBatchActionBar
        selectedCount={2}
        enrolling={false}
        quickRecording={false}
        onClearSelection={() => {}}
        onOpenEnrollModal={() => {}}
        onQuickRecord={() => {}}
      />
    );
    expect(actionBarHtml).toContain('discovery-quick-record-btn');
    expect(actionBarHtml).toContain('一键立即录制');
    expect(actionBarHtml).toContain('discovery-batch-enroll-btn');
    expect(actionBarHtml).toContain('批量纳管（不布防）');

    // 2. DiscoveryProcessTreeTable 本机进程渲染 [编排仿真任务] 按钮，LDC 只读页不渲染
    const localProc: DiscoveryProcessGroupDTO = {
      process_key: 'order_svr-27001',
      proc_name: 'order_svr',
      exe_name: '/opt/bin/order_svr',
      cmdline: '/opt/bin/order_svr --port=27001',
      identity_fingerprint: 'fp-order',
      pids: [5101, 5102],
      listen_ports: [27001, 27002],
      observed_role: 'DUAL',
      observed_transports: ['TCP'],
      enroll_status: 'ARMED_READY',
      identity_match: 'MATCHED',
      arch_process_id: 11,
      arch_process_code: 'order_svr',
      arch_process_name: 'OrderSvr',
      process_id: 101,
      is_remote_ldc: false,
      claimed_service_count: 1,
      unclaimed_service_count: 0,
      services: [],
      subscriptions: [],
    };
    const treeHtml = renderToString(
      <DiscoveryProcessTreeTable
        localProcesses={[localProc]}
        remoteLdcProcesses={[]}
        scopeTab="local"
        onChangeScopeTab={() => {}}
        searchQuery=""
        onChangeSearchQuery={() => {}}
        selectedKeys={new Set(['order_svr-27001'])}
        onToggleSelectKey={() => {}}
        onToggleSelectAll={() => {}}
        onOpenSimulationPlan={() => {}}
      />
    );
    expect(treeHtml).toContain('discovery-sim-plan-btn-order_svr-27001');
    expect(treeHtml).toContain('编排仿真任务');
    expect(treeHtml).toContain('2 PIDs · 5101, 5102');

    // 3. Dashboard FiveStateRadarCard 渲染五态计数及 SHARED 权威域标识
    const radarData: FiveStateRadarDTO = {
      shadow_live_count: 3,
      unclaimed_orphan_count: 2,
      phantom_proto_count: 1,
      vanished_claimed_count: 4,
      placement_drift_count: 1,
      total_catalog_services: 18,
      authority_scope_only: true,
    };
    const radarHtml = renderToString(
      <FiveStateRadarCard radar={radarData} onNavigateCatalog={() => {}} />
    );
    expect(radarHtml).toContain('five-state-radar-card');
    expect(radarHtml).toContain('仅统计共享权威环境');
    expect(radarHtml).toContain('radar-tile-SHADOW_LIVE');
    expect(radarHtml).toContain('radar-tile-UNCLAIMED_ORPHAN');
    expect(radarHtml).toContain('radar-tile-PHANTOM_PROTO');
    expect(radarHtml).toContain('radar-tile-VANISHED_CLAIMED');
    expect(radarHtml).toContain('radar-tile-PLACEMENT_DRIFT');
    expect(radarHtml).toContain('无契约在线');
    expect(radarHtml).toContain('已上线未认领');
    expect(radarHtml).toContain('纸面未上线');
    expect(radarHtml).toContain('已认领但下线');
    expect(radarHtml).toContain('实际部署漂移');
    expect(radarHtml).toContain('已有 .pb 契约定义，但权威现网既无运行实例也无调用订阅');
    expect(radarHtml).not.toContain('幻影订阅');
    expect(radarHtml).not.toContain('(PHANTOM_PROTO)');

    // 4. UnifiedCatalogPane 渲染五态筛选、[一键匹配 .pb 契约] 与 SUGGESTED 行 [确认采用建议]
    const catalogData: CatalogServicesResponseDTO = {
      radar: radarData,
      total: 1,
      page: 1,
      page_size: 25,
      items: [
        {
          id: 901,
          service_name: 'BillingImpl-BillingService',
          source: 'CSG_DISCOVERED',
          claim_status: 'UNCLAIMED',
          proto_suggested_name: 'billing.v1.BillingService',
          proto_match_confidence: 'HEURISTIC',
          proto_status: 'SUGGESTED',
          operation_count: 3,
          shared_placement_count: 1,
          transports: ['MESH'],
          recent_traffic_count: 12,
          radar_state: 'SHADOW_LIVE',
        },
      ],
    };
    const catalogHtml = renderToString(
      <MemoryRouter>
        <ModuleUserProvider
          value={{
            user: adminUser,
            loadingUser: false,
            isEmbedded: false,
            reloadUser: async () => {},
          }}
        >
          <UnifiedCatalogPane initialData={catalogData} initialState="PHANTOM_PROTO" isAdmin={true} />
        </ModuleUserProvider>
      </MemoryRouter>
    );
    expect(catalogHtml).toContain('catalog-correlate-btn');
    expect(catalogHtml).toContain('一键匹配 .pb 契约');
    expect(catalogHtml).toContain('catalog-glossary-toggle');
    expect(catalogHtml).toContain('状态说明');
    expect(catalogHtml).toContain('catalog-active-state-callout');
    expect(catalogHtml).toContain('契约库中已激活该服务的 .pb 定义');
    expect(catalogHtml).toContain('confirm-proto-btn-901');
    expect(catalogHtml).toContain('确认采用建议');
    expect(catalogHtml).toContain('billing.v1.BillingService');
    expect(catalogHtml).toContain('未认领');
    expect(catalogHtml).toContain('无契约在线');
    expect(catalogHtml).toContain('名称推断');
    expect(catalogHtml).toContain('1 个实例');
    expect(catalogHtml).toContain('Mesh 网格');
    expect(catalogHtml).not.toContain('>UNCLAIMED<');
    expect(catalogHtml).not.toContain('>SHADOW_LIVE<');
    expect(catalogHtml).not.toContain('(HEURISTIC)');
  });
});
