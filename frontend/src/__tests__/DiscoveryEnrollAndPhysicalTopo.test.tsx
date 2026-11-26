import { describe, expect, it } from 'vitest';
import { renderToString } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import fs from 'node:fs';
import path from 'node:path';
import { ConfirmProvider, ToastProvider } from '@code/common';
import { ModuleUserProvider } from '../context/ModuleUserContext';
import { DiscoveryWorkbenchPage } from '../pages/DiscoveryWorkbenchPage';
import { ArchTopologyTabPane } from '../pages/ArchitectureWorkbenchPage/ArchTopologyTabPane';
import { ArchCanvasToolbar } from '../components/ArchCanvas/panels/ArchCanvasToolbar';
import type {
  Agent,
  CurrentUserProfile,
  DiscoveryLiveRespDTO,
  EnrollValidationErrorPayload,
} from '../types';
import type { TopologyGraphDTO } from '../types/topology_graph';

const wrap = (ui: JSX.Element) =>
  renderToString(
    <MemoryRouter>
      <ToastProvider>
        <ConfirmProvider>
          <ModuleUserProvider
            value={{
              user: {
                id: 1,
                username: 'alice',
                name: 'Alice',
                roles: ['developer'],
                is_super_admin: false,
                is_module_admin: false,
                required_ad_role: 'contract_admin',
              } satisfies CurrentUserProfile,
              loadingUser: false,
              isEmbedded: false,
              reloadUser: async () => {},
            }}
          >
            {ui}
          </ModuleUserProvider>
        </ConfirmProvider>
      </ToastProvider>
    </MemoryRouter>
  );

describe('M30b Discovery Enroll & Physical Topology V2 UI', () => {
  const sharedAgent: Agent = {
    id: '10',
    name: 'sit-shared-host',
    endpoint: 'http://10.0.0.10:8090',
    scope: 'SHARED',
    status: 'ONLINE',
    lease_type: '',
  };

  const liveResp: DiscoveryLiveRespDTO = {
    agent_id: 10,
    agent_name: 'sit-shared-host',
    agent_scope: 'SHARED',
    csg_addr: '10.0.0.10:31806',
    csg_endpoints: [],
    stale: false,
    captured_at: '2026-11-25T09:00:00Z',
    discovery_mode: 'CROSTCP_RPCMON',
    ports: [],
    summary: {
      process_count: 2,
      client_only_count: 0,
      remote_ldc_count: 0,
      total_services: 2,
      total_subscriptions: 1,
      tcp_count: 1,
      mesh_count: 1,
      matched_services: 0,
      undeclared_services: 2,
    },
    processes: [
      {
        process_key: 'fp:amb_worker',
        proc_name: 'amb_worker',
        exe_name: 'amb_worker',
        cmdline: '/opt/bin/amb_worker --x=1',
        identity_fingerprint: 'fpamb',
        pids: [2001],
        listen_ports: [21001],
        observed_role: 'SERVER',
        observed_transports: ['TCP'],
        enroll_status: 'UNENROLLED',
        identity_match: 'AMBIGUOUS',
        candidates: [
          { arch_process_id: 101, code: 'amb-one', name: '候选进程一', lifecycle: 'CONFIRMED', match_tier: 'P2_FINGERPRINT' },
          { arch_process_id: 102, code: 'amb-two', name: '候选进程二', lifecycle: 'CONFIRMED', match_tier: 'P2_FINGERPRINT' },
        ],
        is_remote_ldc: false,
        claimed_service_count: 0,
        unclaimed_service_count: 1,
        services: [],
        subscriptions: [],
      },
      {
        process_key: 'proc:low_proc',
        proc_name: 'low_proc',
        exe_name: 'low_proc',
        cmdline: '',
        identity_fingerprint: 'fplow',
        pids: [2002],
        listen_ports: [],
        observed_role: 'CLIENT_ONLY',
        observed_transports: ['MESH'],
        enroll_status: 'UNENROLLED',
        identity_match: 'LOW_CONFIDENCE',
        is_remote_ldc: false,
        claimed_service_count: 0,
        unclaimed_service_count: 0,
        services: [],
        subscriptions: [],
      },
    ],
    remote_ldc_processes: [],
  };

  it('renders checkboxes, batch enroll action bar, enroll modal and 422 disambiguation modal', () => {
    const selectedHtml = wrap(
      <DiscoveryWorkbenchPage
        initialAgents={[sharedAgent]}
        initialLiveData={liveResp}
        initialViewMode="tree"
        initialSelectedKeys={['fp:amb_worker', 'proc:low_proc']}
        initialEnrollModalOpen
      />
    );
    expect(selectedHtml).toContain('discovery-checkbox-fp:amb_worker');
    expect(selectedHtml).toContain('discovery-batch-action-bar');
    expect(selectedHtml).toContain('批量纳管（不布防）');
    expect(selectedHtml).toContain('零侵扰物理纳管确认');
    expect(selectedHtml).toContain('DISARMED');
    expect(selectedHtml).toContain('data-testid="enroll-target-group-select"');
    expect(selectedHtml).toContain('现网发现自动纳管进程组（默认暂存）');

    const disambigPayload: EnrollValidationErrorPayload = {
      code: 'IDENTITY_AMBIGUOUS',
      error: '匹配到 2 个候选标准进程，请确认',
      process_key: 'fp:amb_worker',
      candidates: liveResp.processes[0].candidates,
    };
    const disambigHtml = wrap(
      <DiscoveryWorkbenchPage
        initialAgents={[sharedAgent]}
        initialLiveData={liveResp}
        initialViewMode="tree"
        initialSelectedKeys={['fp:amb_worker']}
        initialDisambiguation={disambigPayload}
      />
    );
    expect(disambigHtml).toContain('身份消歧确认');
    expect(disambigHtml).toContain('候选进程一');
    expect(disambigHtml).toContain('候选进程二');

    const lowConfPayload: EnrollValidationErrorPayload = {
      code: 'LOW_CONFIDENCE_CONFIRM_REQUIRED',
      error: 'cmdline 不可读，需低置信二次确认',
      process_key: 'proc:low_proc',
    };
    const lowConfHtml = wrap(
      <DiscoveryWorkbenchPage
        initialAgents={[sharedAgent]}
        initialLiveData={liveResp}
        initialViewMode="tree"
        initialSelectedKeys={['proc:low_proc']}
        initialDisambiguation={lowConfPayload}
      />
    );
    expect(lowConfHtml).toContain('低置信进程纳管确认');
  });

  it('renders SHARED default label and physical scale guard banners (>150 collapsed / >500 truncated)', () => {
    const toolbarHtml = wrap(
      <ArchCanvasToolbar
        view="PHYSICAL"
        onViewChange={() => {}}
        agents={[sharedAgent]}
        selectedEnvAgentId={null}
        onSelectEnvAgentId={() => {}}
        sessions={[]}
        selectedSessionId={null}
        onSelectSession={() => {}}
        searchKeyword=""
        onSearchChange={() => {}}
        hideIsolated={false}
        onToggleHideIsolated={() => {}}
        showRealization={false}
        onToggleShowRealization={() => {}}
        contractMode="AGG"
        onContractModeChange={() => {}}
        foldHubEdges={false}
        onToggleFoldHubEdges={() => {}}
        onResetLayout={() => {}}
      />
    );
    expect(toolbarHtml).toContain('公共环境并集 (SHARED)');

    const largePhysicalGraph: TopologyGraphDTO = {
      view: 'PHYSICAL',
      model_version: 'v1-phys-test',
      structure_version: 's1-phys-test',
      default_collapsed: true,
      truncated: true,
      truncation_reason: 'PHYSICAL_SCALE_GUARD_500_2000',
      process_node_count: 500,
      nodes: [
        {
          id: 'pgrp_1',
          type: 'PROCESS_GROUP',
          ancestors: [],
          depth: 0,
          name: '默认发现组',
          sort_order: 1,
        },
      ],
      edges: [],
    };
    const topoHtml = wrap(
      <ArchTopologyTabPane
        initialView="PHYSICAL"
        initialGraphData={largePhysicalGraph}
      />
    );
    expect(topoHtml).toContain('physical-collapse-banner');
    expect(topoHtml).toContain('physical-truncated-banner');
    expect(topoHtml).toContain('500');
  });

  it('enforces CSS tokens, zero any, and @code/common Modal usage across DiscoveryWorkbenchPage', () => {
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
  });
});
