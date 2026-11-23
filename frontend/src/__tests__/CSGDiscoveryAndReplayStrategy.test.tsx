import type { ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderToString } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';

vi.mock('@code/common', async () => {
  const actual = await vi.importActual<typeof import('@code/common')>('@code/common');
  return {
    ...actual,
    Modal: ({
      open,
      children,
      footer,
    }: {
      open?: boolean;
      children?: ReactNode;
      footer?: ReactNode;
    }) => (open ? <div data-testid="ssr-modal">{children}{footer}</div> : null),
  };
});
import { ModuleUserProvider } from '../context/ModuleUserContext';
import { CSGBlueprintImportContent } from '../pages/AgentManagementPage/CSGBlueprintImportDrawer';
import { GoldenSuiteExecuteModal } from '../pages/GoldenSuitesWorkbenchPage/GoldenSuiteExecuteModal';
import { ReplayTaskCreatorModal } from '../pages/ReplayWorkbenchPage/ReplayTaskCreatorModal';
import { ReplayReportDrawer } from '../pages/ReplayWorkbenchPage/ReplayReportDrawer/ReplayReportDrawer';
import type {
  Agent,
  CSGTopologyReconcileResp,
  CurrentUserProfile,
  GoldenSuite,
  Process,
  RecordingSession,
  ReplayTask,
  Subsystem,
} from '../types';

describe('M28 CSG Discovery, Blueprint Import & 3-Mode Simulation Console', () => {
  const dummyAgent: Agent = {
    id: '10',
    name: 'Agent-CSG-01',
    endpoint: 'http://127.0.0.1:9091',
    scope: 'SHARED',
    status: 'ONLINE',
    lease_type: '',
  };

  const dummyProcesses: Process[] = [
    {
      id: '101',
      agent_id: '10',
      name: 'order_svr',
      listen_port: 18090,
      arch_process_id: '501',
    },
  ];

  const dummySubsystems: Subsystem[] = [
    {
      id: '1',
      code: 'SUB_TRADE',
      name: '交易核心子系统',
      layer_level: 20,
    },
  ];

  const dummyReport: CSGTopologyReconcileResp = {
    agent_id: 10,
    discovery_mode: 'CROSTCP_RPCMON',
    csg_endpoints: [
      {
        pid: 4100,
        exe_name: 'csg_server',
        listen_ip: '127.0.0.1',
        listen_port: 31806,
        source: 'CLI_FLAG',
      },
    ],
    services: [
      {
        service_name: 'OrderServiceImpl-OrderService',
        addr: 'tcp://ipv4:127.0.0.1:28001',
        transport: 'TCP',
        server_pid: 9101,
        server_proc: 'order_svr',
        state: 'MATCHED',
        ssot_proto_svc: 'order.v1.OrderService',
      },
      {
        service_name: 'LedgerServiceImpl-LedgerService',
        addr: 'dophi_mesh://ipv4:127.0.0.1:39002',
        transport: 'MESH',
        server_pid: 9102,
        server_proc: 'ledger_svr',
        state: 'UNDECLARED_IN_SSOT',
      },
      {
        service_name: 'LegacyAudit-0',
        expected_port: 19988,
        state: 'MISSING_IN_LIVE',
        ssot_module_name: '审计模块',
      },
    ],
    subscriptions: [
      {
        client_pid: 9101,
        client_proc: 'order_svr',
        client_addr: '127.0.0.1',
        service_name: 'LedgerServiceImpl-LedgerService',
        state: 'UNDECLARED_IN_SSOT',
      },
    ],
    summary: {
      live_service_count: 2,
      matched_count: 1,
      undeclared_count: 1,
      missing_in_live_count: 1,
      live_sub_count: 1,
      undeclared_sub_count: 1,
    },
    filtered_remote_count: 2,
    filtered_transient_count: 1,
    captured_at: '2026-11-18T00:00:00Z',
    errors: ['rpcmon: native probe warning'],
  };

  const adminUser: CurrentUserProfile = {
    id: 1,
    username: 'admin',
    name: '管理员',
    roles: ['contract_admin'],
    is_super_admin: false,
    is_module_admin: true,
    required_ad_role: 'contract_admin',
  };

  const normalUser: CurrentUserProfile = {
    id: 2,
    username: 'operator',
    name: '普通研发',
    roles: ['developer'],
    is_super_admin: false,
    is_module_admin: false,
    required_ad_role: 'contract_admin',
  };

  it('renders 3-state reconciliation badges and enforces AdminOnly blueprint import button', () => {
    const adminHtml = renderToString(
      <MemoryRouter>
        <ModuleUserProvider
          value={{
            user: adminUser,
            loadingUser: false,
            isEmbedded: false,
            reloadUser: async () => {},
          }}
        >
          <CSGBlueprintImportContent
            agent={dummyAgent}
            initialReport={dummyReport}
            initialSubsystems={dummySubsystems}
          />
        </ModuleUserProvider>
      </MemoryRouter>
    );

    expect(adminHtml).toContain('已纳管 (MATCHED)');
    expect(adminHtml).toContain('待导入 (UNDECLARED_IN_SSOT)');
    expect(adminHtml).toContain('现网未上线 (MISSING_IN_LIVE)');
    expect(adminHtml).toContain('OrderServiceImpl-OrderService');
    expect(adminHtml).toContain('LedgerServiceImpl-LedgerService');
    expect(adminHtml).toContain('csg-transport-badge-MESH');
    expect(adminHtml).toContain('dophi_mesh');
    expect(adminHtml).toContain('LegacyAudit-0');
    expect(adminHtml).toContain('csg-endpoints-selector');
    expect(adminHtml).toContain('127.0.0.1:31806');
    expect(adminHtml).toContain('csg-discovery-errors-banner');
    expect(adminHtml).toContain('rpcmon: native probe warning');
    expect(adminHtml).toContain('1<!-- --> / <!-- -->2');
    expect(adminHtml).toContain('一键导入 SSOT 蓝图');
    expect(adminHtml).not.toContain('csg-admin-only-notice');

    const nonAdminHtml = renderToString(
      <MemoryRouter>
        <ModuleUserProvider
          value={{
            user: normalUser,
            loadingUser: false,
            isEmbedded: false,
            reloadUser: async () => {},
          }}
        >
          <CSGBlueprintImportContent
            agent={dummyAgent}
            initialReport={dummyReport}
            initialSubsystems={dummySubsystems}
          />
        </ModuleUserProvider>
      </MemoryRouter>
    );

    expect(nonAdminHtml).toContain('csg-admin-only-notice');
    expect(nonAdminHtml).not.toContain('csg-import-blueprint-btn');
  });

  it('renders explicit PROC_ONLY mesh degradation banner when L2 discovery is unavailable', () => {
    const degradedReport: CSGTopologyReconcileResp = {
      agent_id: 10,
      discovery_mode: 'PROC_ONLY',
      csg_endpoints: [
        {
          pid: 4100,
          exe_name: 'DophiServer',
          listen_ip: '10.29.92.80',
          listen_port: 31805,
          source: 'PROC_LISTEN',
        },
      ],
      services: [],
      subscriptions: [],
      summary: {
        live_service_count: 0,
        matched_count: 0,
        undeclared_count: 0,
        missing_in_live_count: 0,
        live_sub_count: 0,
      },
      filtered_remote_count: 0,
      filtered_transient_count: 0,
      captured_at: '2026-11-20T00:00:00Z',
      errors: [
        'L2 发现降级为 PROC_ONLY：RPCMonitorService-RPCMonitorService 在 CSG(10.29.92.80:31805) 仅发布了 dophi_mesh:// 实例 (dophi_mesh://ipv4:10.29.92.80:16000)，CrosTCP 无法直连',
      ],
    };

    const html = renderToString(
      <MemoryRouter>
        <ModuleUserProvider
          value={{
            user: adminUser,
            loadingUser: false,
            isEmbedded: false,
            reloadUser: async () => {},
          }}
        >
          <CSGBlueprintImportContent
            agent={dummyAgent}
            initialReport={degradedReport}
            initialSubsystems={dummySubsystems}
          />
        </ModuleUserProvider>
      </MemoryRouter>
    );

    expect(html).toContain('csg-discovery-errors-banner');
    expect(html).toContain('csg-proc-only-degraded-banner');
    expect(html).toContain('dophi_mesh://');
    expect(html).toContain('CROSMESH_RPCMON');
    expect(html).not.toContain('rpcmon_probe');
    expect(html).not.toContain('--csg-probe-bin');
  });

  it('renders all 4 csg_strategy options in ReplayTaskCreatorModal and GoldenSuiteExecuteModal', () => {
    const dummySessions: RecordingSession[] = [
      {
        id: '11',
        session_no: 'REC_CSG_01',
        name: 'CSG 基线录制',
        scope: 'SHARED',
        status: 'COMPLETED',
        target_process_ids: ['101'],
        max_duration_sec: 300,
        max_frames: 1000,
        captured_frames: 12,
      },
    ];

    const replayModalHtml = renderToString(
      <MemoryRouter>
        <ReplayTaskCreatorModal
          open={true}
          onClose={() => {}}
          sessions={dummySessions}
          processes={dummyProcesses}
          onSuccess={() => {}}
        />
      </MemoryRouter>
    );

    expect(replayModalHtml).toContain('select-csg-strategy');
    expect(replayModalHtml).toContain('PASSTHROUGH');
    expect(replayModalHtml).toContain('CLIENT_INJECT');
    expect(replayModalHtml).toContain('PUB_STUB');
    expect(replayModalHtml).toContain('MOCK_SERVER');
    expect(replayModalHtml).toContain('input-csg-addr');

    const dummySuite: GoldenSuite = {
      id: 77,
      code: 'GS_CSG_CORE',
      name: '核心交易回归套件',
      arch_process_id: 501,
      version: 'v1.0.0',
      status: 'ACTIVE',
      item_count: 3,
    };

    const suiteModalHtml = renderToString(
      <GoldenSuiteExecuteModal
        open={true}
        onClose={() => {}}
        suite={dummySuite}
        allAgents={[dummyAgent]}
        allProcesses={dummyProcesses}
      />
    );

    expect(suiteModalHtml).toContain('select-suite-csg-strategy');
    expect(suiteModalHtml).toContain('PASSTHROUGH');
    expect(suiteModalHtml).toContain('CLIENT_INJECT');
    expect(suiteModalHtml).toContain('PUB_STUB');
    expect(suiteModalHtml).toContain('MOCK_SERVER');
  });

  it('renders CSGSandboxReport card inside ReplayReportDrawer', () => {
    const task: ReplayTask = {
      id: '901',
      task_no: 'RPL_MOCK_01',
      name: '孤岛沙箱验收',
      session_id: '11',
      source_process_id: '101',
      target_process_id: '101',
      agent_id: '10',
      lifecycle_state: 'COMPLETED',
      result_status: 'PASS',
      total_requests: 1,
      passed_requests: 1,
      failed_requests: 0,
      mock_miss_requests: 0,
      timeout_requests: 0,
      error_requests: 0,
      csg_strategy: 'MOCK_SERVER',
      csg_sandbox_report: {
        strategy: 'MOCK_SERVER',
        mock_csg_addr: 'in-memory://mock-csg',
        sandbox_subscribed_svcs: ['LedgerServiceImpl-LedgerService'],
        mesh_rewrites: 2,
        real_csg_leak_count: 0,
        cleanup_completed: true,
      },
    };

    const drawerHtml = renderToString(
      <MemoryRouter>
        <ReplayReportDrawer open={true} onClose={() => {}} task={task} />
      </MemoryRouter>
    );

    expect(drawerHtml).toContain('csg-sandbox-report-card');
    expect(drawerHtml).toContain('MOCK_SERVER');
    expect(drawerHtml).toContain('自洁完成');
    expect(drawerHtml).toContain('LedgerServiceImpl-LedgerService');
  });
});
