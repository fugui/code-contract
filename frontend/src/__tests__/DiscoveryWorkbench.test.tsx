import { describe, expect, it } from 'vitest';
import { renderToString } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import fs from 'node:fs';
import path from 'node:path';
import { ConfirmProvider, ToastProvider } from '@code/common';
import { ModuleUserProvider } from '../context/ModuleUserContext';
import { DiscoveryWorkbenchPage } from '../pages/DiscoveryWorkbenchPage';
import { CSGBlueprintImportContent } from '../pages/AgentManagementPage/CSGBlueprintImportDrawer';
import type {
  Agent,
  CurrentUserProfile,
  DiscoveryLiveRespDTO,
  DiscoverySnapshotDiffDTO,
  DiscoverySnapshotSummaryDTO,
} from '../types';

export function testDiscoveryWorkbench_ProcessTree_PureClient_LDC_LiveGraph_AndSnapshotDiffDrawer(): void {
  const user: CurrentUserProfile = {
    id: 1,
    username: 'alice',
    name: 'Alice',
    roles: ['developer'],
    is_super_admin: false,
    is_module_admin: false,
    required_ad_role: 'contract_admin',
  };

  const privateAgent: Agent = {
    id: '42',
    name: 'alice-dev-box',
    endpoint: 'http://127.0.0.1:9091',
    scope: 'PRIVATE',
    owner_id: '1',
    status: 'OFFLINE',
    lease_type: '',
  };

  const liveResp: DiscoveryLiveRespDTO = {
    agent_id: 42,
    agent_name: 'alice-dev-box',
    agent_scope: 'PRIVATE',
    csg_addr: '127.0.0.1:31806',
    csg_endpoints: [
      {
        pid: 100,
        exe_name: 'csg_server',
        listen_ip: '127.0.0.1',
        listen_port: 31806,
        source: 'CLI_FLAG',
      },
    ],
    stale: true,
    stale_reason: 'Agent 当前离线',
    captured_at: '2026-11-25T08:15:00Z',
    discovery_mode: 'PROC_ONLY',
    ports: [],
    errors: ['rpcmon unreachable'],
    summary: {
      process_count: 2,
      client_only_count: 1,
      remote_ldc_count: 1,
      total_services: 1,
      total_subscriptions: 2,
      tcp_count: 1,
      mesh_count: 0,
      matched_services: 1,
      undeclared_services: 0,
    },
    processes: [
      {
        process_key: 'fp:order_svr',
        proc_name: 'order_svr',
        exe_name: 'order_svr',
        cmdline: '/opt/bin/order_svr --password=***',
        identity_fingerprint: 'abc123',
        pids: [12345, 12398],
        listen_ports: [21001, 21002],
        observed_role: 'DUAL',
        observed_transports: ['TCP'],
        enroll_status: 'UNENROLLED',
        identity_match: 'NEW',
        is_remote_ldc: false,
        claimed_service_count: 0,
        unclaimed_service_count: 1,
        services: [
          {
            service_name: 'OrderService-0',
            raw_service_name: 'OrderService-0-12345',
            endpoints: ['tcp://ipv4:10.0.0.1:21001'],
            transport: 'TCP',
            server_pids: [12345, 12398],
            instance_count: 2,
            is_multi_host: false,
            subscriber_count: 1,
            proto_match_status: 'EXACT',
            proto_service_name: 'order.v1.OrderService',
            claim_status: 'UNCLAIMED',
          },
        ],
        subscriptions: [
          {
            service_name: 'RemoteAuthSvc-0',
            raw_service_name: 'RemoteAuthSvc-0',
            provider_kind: 'REMOTE_LDC',
            provider_proc_name: 'remote_auth_svr',
            remote_node: 'node-2',
          },
        ],
      },
      {
        process_key: 'fp:pure_cli',
        proc_name: 'pure_cli_worker',
        exe_name: 'pure_cli_worker',
        cmdline: '/opt/bin/pure_cli_worker',
        identity_fingerprint: 'cli999',
        pids: [22001],
        listen_ports: [],
        observed_role: 'CLIENT_ONLY',
        observed_transports: [],
        enroll_status: 'UNENROLLED',
        identity_match: 'NEW',
        is_remote_ldc: false,
        claimed_service_count: 0,
        unclaimed_service_count: 0,
        services: [],
        subscriptions: [
          {
            service_name: 'OrderService-0',
            raw_service_name: 'OrderService-0',
            provider_kind: 'LOCAL_UNENROLLED',
            provider_proc_name: 'order_svr',
          },
        ],
      },
    ],
    remote_ldc_processes: [
      {
        process_key: 'remote:node-2:remote_auth_svr',
        proc_name: 'remote_auth_svr',
        exe_name: 'remote_auth_svr',
        cmdline: '',
        identity_fingerprint: 'rem1',
        pids: [9001],
        listen_ports: [],
        observed_role: 'SERVER',
        observed_transports: ['MESH'],
        enroll_status: 'UNENROLLED',
        identity_match: 'NEW',
        is_remote_ldc: true,
        remote_node: 'node-2',
        claimed_service_count: 0,
        unclaimed_service_count: 1,
        services: [
          {
            service_name: 'RemoteAuthSvc-0',
            raw_service_name: 'RemoteAuthSvc-0',
            endpoints: ['dophi_mesh://ipv4:10.0.9.9:32001'],
            transport: 'MESH',
            server_pids: [9001],
            instance_count: 1,
            is_multi_host: false,
            subscriber_count: 1,
            proto_match_status: 'UNMATCHED',
            claim_status: 'UNCLAIMED',
          },
        ],
        subscriptions: [],
      },
    ],
  };

  const snapshots: DiscoverySnapshotSummaryDTO[] = [
    {
      id: 11,
      agent_id: 42,
      agent_name: 'alice-dev-box',
      trigger_mode: 'MANUAL',
      csg_addr: '127.0.0.1:31806',
      discovery_mode: 'CROSTCP_RPCMON',
      label: 'baseline-v1',
      is_pinned: true,
      content_hash: 'deadbeef123456',
      redaction_version: 1,
      summary: liveResp.summary,
      created_by: 1,
      captured_at: '2026-11-25T08:00:00Z',
      created_at: '2026-11-25T08:00:00Z',
    },
  ];

  const diffResult: DiscoverySnapshotDiffDTO = {
    mode: 'TIMELINE',
    left_label: '快照 #11 · baseline-v1',
    right_label: '实时态 (alice-dev-box)',
    summary: {
      total: 2,
      added_process: 0,
      removed_process: 0,
      added_service: 1,
      removed_service: 0,
      transport_changed: 1,
      added_subscription: 0,
      removed_subscription: 0,
      provider_kind_changed: 0,
    },
    items: [
      {
        kind: 'ADDED_SERVICE',
        process_key: 'fp:order_svr',
        process_name: 'order_svr',
        service_name: 'NewOrderSvc-0',
        detail: '进程 order_svr 新增发布服务 NewOrderSvc-0 (TCP)',
      },
      {
        kind: 'TRANSPORT_CHANGED',
        process_key: 'fp:order_svr',
        process_name: 'order_svr',
        service_name: 'OrderService-0',
        left_transport: 'TCP',
        right_transport: 'MESH',
        detail: '服务 OrderService-0 传输制式变更: TCP ➔ MESH',
      },
    ],
  };

  const wrap = (node: JSX.Element) =>
    renderToString(
      <MemoryRouter>
        <ModuleUserProvider
          value={{
            user,
            loadingUser: false,
            isEmbedded: false,
            reloadUser: async () => {},
          }}
        >
          <ToastProvider>
            <ConfirmProvider>{node}</ConfirmProvider>
          </ToastProvider>
        </ModuleUserProvider>
      </MemoryRouter>
    );

  // 1. Tree mode + search auto-expand + PRIVATE badge + stale + PROC_ONLY + multi-PID + CLIENT_ONLY
  const treeHtml = wrap(
    <DiscoveryWorkbenchPage
      initialAgents={[privateAgent]}
      initialLiveData={liveResp}
      initialViewMode="tree"
      initialScopeTab="local"
      initialSearch="OrderService-0"
    />
  );
  expect(treeHtml).toContain('discovery-workbench-page');
  expect(treeHtml).toContain('私有 · 仅本人可见');
  expect(treeHtml).toContain('离线 · 采集于');
  expect(treeHtml).toContain('PROC_ONLY');
  expect(treeHtml).toContain('2 PIDs · 12345, 12398');
  expect(treeHtml).toContain('pure_cli_worker');
  expect(treeHtml).toContain('CLIENT_ONLY');
  // Search auto-expands matching parent row to render OrderService-0 child row
  expect(treeHtml).toContain('discovery-child-row-fp:order_svr');
  expect(treeHtml).toContain('order.v1.OrderService');

  // 2. Remote LDC tab
  const ldcHtml = wrap(
    <DiscoveryWorkbenchPage
      initialAgents={[privateAgent]}
      initialLiveData={liveResp}
      initialViewMode="tree"
      initialScopeTab="ldc"
    />
  );
  expect(ldcHtml).toContain('remote_auth_svr');
  expect(ldcHtml).toContain('远端 node-2 (只读)');

  // 3. Live dependency graph mode
  const graphHtml = wrap(
    <DiscoveryWorkbenchPage
      initialAgents={[privateAgent]}
      initialLiveData={liveResp}
      initialViewMode="graph"
    />
  );
  expect(graphHtml).toContain('discovery-live-dep-graph');
  expect(graphHtml).toContain('pure_cli_worker');
  expect(graphHtml).toContain('OrderService-0');
  expect(graphHtml).toContain('order_svr');

  // 4. Snapshot timeline & Diff drawer
  const drawerHtml = wrap(
    <DiscoveryWorkbenchPage
      initialAgents={[privateAgent]}
      initialLiveData={liveResp}
      initialSnapshots={snapshots}
      initialDiffResult={diffResult}
      initialDrawerOpen
    />
  );
  expect(drawerHtml).toContain('discovery-snapshot-diff-drawer');
  expect(drawerHtml).toContain('discovery-diff-results');
  expect(drawerHtml).toContain('新增服务');
  expect(drawerHtml).toContain('制式变更');

  // 5. /agents CSG drawer deep-link to /discovery?agent_id=42
  const agentDrawerHtml = wrap(<CSGBlueprintImportContent agent={privateAgent} />);
  expect(agentDrawerHtml).toContain('csg-discovery-deeplink-banner');
  expect(agentDrawerHtml).toContain('/discovery?agent_id=42');

  // 6. Enforce 100% --color-* CSS tokens (no hardcoded hex/rgb/hsl colors) & zero `any`
  const pageDir = path.resolve(__dirname, '../pages/DiscoveryWorkbenchPage');
  const cssContent = fs.readFileSync(path.join(pageDir, 'DiscoveryWorkbench.css'), 'utf-8');
  expect(cssContent).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
  expect(cssContent).not.toMatch(/\brgba?\s*\(/i);
  expect(cssContent).not.toMatch(/\bhsla?\s*\(/i);

  for (const file of fs.readdirSync(pageDir)) {
    if (file.endsWith('.ts') || file.endsWith('.tsx')) {
      const code = fs.readFileSync(path.join(pageDir, file), 'utf-8');
      expect(code).not.toMatch(/:\s*any\b|\bas\s+any\b/);
    }
  }
}

describe('DiscoveryWorkbench Test Suite', () => {
  it('renders process-centric tree, pure client, remote LDC, live dep graph, snapshot diff drawer & deep-link', () => {
    testDiscoveryWorkbench_ProcessTree_PureClient_LDC_LiveGraph_AndSnapshotDiffDrawer();
  });
});
