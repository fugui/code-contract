import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderToString } from 'react-dom/server';
import type {
  ArchInboxResponseDTO,
  AttributionJobDTO,
  DepSuggestionDTO,
  Module,
} from '../../../types';

vi.mock('../../../api/arch', () => ({
  getArchInbox: vi.fn(),
  claimArchServices: vi.fn(),
  splitMultiImpl: vi.fn(),
  unclaimArchService: vi.fn(),
  decideDependencySuggestion: vi.fn(),
  getAttributionJob: vi.fn(),
  cancelAttributionJob: vi.fn(),
  listModules: vi.fn(),
}));

import * as archApi from '../../../api/arch';
import {
  attributionPercent,
  buildClaimItems,
  defaultConsumerModuleId,
  mergeAttributionEvent,
  submitDependencyDecision,
  submitInboxClaim,
  submitInboxSplit,
  trackAttributionJob,
} from '../inboxLogic';
import {
  AttributionProgressBar,
  InboxCountBadge,
  InboxDepSuggestionCard,
  InboxProcessGroupList,
  InboxSummaryBar,
} from '../DiscoveryInboxParts';

const mocked = vi.mocked(archApi);

const inbox: ArchInboxResponseDTO = {
  summary: {
    unclaimed_count: 2,
    multi_host_count: 1,
    placement_drift_count: 1,
    dep_suggestion_count: 1,
    total_pending: 5,
  },
  process_groups: [
    {
      arch_process_id: 11,
      arch_process_code: 'TRADE_SVR',
      arch_process_name: '交易进程',
      group_name: '交易进程组',
      items: [
        {
          service_id: 101,
          service_name: 'trade.OrderService',
          proto_service_name: 'trade.OrderService',
          proto_suggested_name: 'trade.OrderService',
          kind: 'UNCLAIMED',
          placements: [
            {
              placement_id: 1,
              arch_process_id: 11,
              process_id: 21,
              agent_id: 31,
              arch_process_code: 'TRADE_SVR',
              arch_process_name: '交易进程',
              agent_name: 'sit-host-a',
              endpoints: ['10.0.0.1:9001'],
              observed_transport: 'builtin-tcp',
              instance_count: 2,
            },
          ],
          candidate_arch_process_ids: [11],
        },
        {
          service_id: 102,
          service_name: 'trade.QuoteService',
          proto_service_name: '',
          proto_suggested_name: 'trade.QuoteService',
          kind: 'UNCLAIMED',
          placements: [],
          candidate_arch_process_ids: [11],
        },
        {
          service_id: 103,
          service_name: 'trade.RouteService',
          proto_service_name: 'trade.RouteService',
          proto_suggested_name: 'trade.RouteService',
          kind: 'MULTI_HOST',
          placements: [
            {
              placement_id: 2,
              arch_process_id: 11,
              process_id: 22,
              agent_id: 31,
              arch_process_code: 'TRADE_SVR',
              arch_process_name: '交易进程',
              agent_name: 'sit-host-a',
              endpoints: [],
              observed_transport: 'builtin-tcp',
              instance_count: 1,
            },
            {
              placement_id: 3,
              arch_process_id: 12,
              process_id: 23,
              agent_id: 32,
              arch_process_code: 'MATCH_SVR',
              arch_process_name: '撮合进程',
              agent_name: 'sit-host-b',
              endpoints: [],
              observed_transport: 'builtin-drt-uprobe',
              instance_count: 1,
            },
          ],
          candidate_arch_process_ids: [11, 12],
        },
        {
          service_id: 104,
          service_name: 'trade.DriftService',
          proto_service_name: 'trade.DriftService',
          proto_suggested_name: 'trade.DriftService',
          kind: 'PLACEMENT_DRIFT',
          module_id: 7,
          module_name: '漂移模块',
          placements: [],
          candidate_arch_process_ids: [12],
        },
      ],
    },
  ],
  dep_suggestions: [],
  total: 1,
  page: 1,
  page_size: 25,
};

const depSuggestion: DepSuggestionDTO = {
  consumer_arch_process_id: 12,
  consumer_arch_process_code: 'MATCH_SVR',
  consumer_arch_process_name: '撮合进程',
  service_id: 101,
  service_name: 'trade.OrderService',
  provider_module_id: 5,
  provider_module_name: '订单模块',
  candidate_consumer_modules: [
    { module_id: 201, module_code: 'MATCH_CORE', module_name: '撮合核心' },
    { module_id: 202, module_code: 'MATCH_RISK', module_name: '撮合风控' },
  ],
  recommended_consumer_module_id: 202,
  recommendation_source: 'STAGE2_TRAFFIC',
};

const job = (over: Partial<AttributionJobDTO>): AttributionJobDTO => ({
  id: 900,
  job_type: 'SERVICE_CLAIM',
  status: 'RUNNING',
  processed: 0,
  total: 10,
  cursor_session_id: 0,
  matched_count: 0,
  unresolved_count: 0,
  conflict_count: 0,
  error: '',
  created_by: 1,
  created_at: '2026-11-25T00:00:00Z',
  updated_at: '2026-11-25T00:00:00Z',
  service_names: ['trade.OrderService'],
  ...over,
});

const bizModules = [
  { id: '1', code: 'M_A', name: '模块A', module_kind: 'BUSINESS', arch_process_id: 11 },
  { id: '2', code: 'M_B', name: '模块B', module_kind: 'BUSINESS', arch_process_id: 11 },
] as unknown as Module[];

describe('testDiscoveryInboxPane_ClaimSplitDecideAndProgress', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders process groups, per-kind counts and unread badge', () => {
    const html = renderToString(
      <>
        <InboxSummaryBar summary={inbox.summary} activeKind="ALL" onChange={() => {}} />
        <InboxCountBadge count={inbox.summary.total_pending} />
        <InboxProcessGroupList
          groups={inbox.process_groups}
          modules={bizModules}
          selected={new Set<number>([101])}
          targets={{ 101: '2' }}
          isAdmin
          busy={false}
          onToggle={() => {}}
          onTarget={() => {}}
          onSplit={() => {}}
          onUnclaim={() => {}}
        />
      </>
    );
    expect(html).toContain('交易进程组');
    expect(html).toContain('TRADE_SVR');
    expect(html).toContain('trade.OrderService');
    expect(html).toContain('data-testid="inbox-unread-badge"');
    expect(html).toContain('>5<');
    expect(html).toContain('data-testid="inbox-kind-UNCLAIMED"');
    expect(html).toContain('data-testid="inbox-kind-DEP_SUGGESTION"');
    expect(html).toContain('data-testid="inbox-split-103"');
    expect(html).toContain('data-testid="inbox-unclaim-104"');
    // 仅 UNCLAIMED 项出现多选框与目标模块下拉
    expect(html).toContain('data-testid="inbox-check-101"');
    expect(html).not.toContain('data-testid="inbox-check-103"');
    expect(html).toContain('data-testid="inbox-target-102"');
    expect(html).toContain('<option value="2" selected="">');
  });

  it('claims selected services with different target modules as an items array', async () => {
    mocked.claimArchServices.mockResolvedValue({ claimed_count: 2, attribution_job_id: 900 });
    const targets = { 101: '1', 102: '2', 103: '1' };
    expect(buildClaimItems([101, 102], targets)).toEqual([
      { service_id: 101, module_id: 1 },
      { service_id: 102, module_id: 2 },
    ]);
    const res = await submitInboxClaim([101, 102], targets);
    expect(mocked.claimArchServices).toHaveBeenCalledWith([
      { service_id: 101, module_id: 1 },
      { service_id: 102, module_id: 2 },
    ]);
    expect(res.attribution_job_id).toBe(900);
    await expect(submitInboxClaim([101, 105], targets)).rejects.toThrow();
    expect(mocked.claimArchServices).toHaveBeenCalledTimes(1);
  });

  it('submits split-multi-impl with definer and per-process implementations', async () => {
    mocked.splitMultiImpl.mockResolvedValue({
      contract: {} as never,
      implementations: [],
      attribution_job_id: 901,
    });
    const res = await submitInboxSplit({
      sourceServiceId: 103,
      definerModuleId: '9',
      rows: [
        { arch_process_id: 11, module_id: '1', impl_key: 'trade' },
        { arch_process_id: 12, module_id: '2', impl_key: 'match' },
      ],
    });
    expect(mocked.splitMultiImpl).toHaveBeenCalledWith({
      source_service_id: 103,
      definer_module_id: 9,
      implementations: [
        { arch_process_id: 11, module_id: 1, impl_key: 'trade' },
        { arch_process_id: 12, module_id: 2, impl_key: 'match' },
      ],
    });
    expect(res.attribution_job_id).toBe(901);
    // impl_key 重复被前端拦截，不调用后端
    await expect(
      submitInboxSplit({
        sourceServiceId: 103,
        definerModuleId: '9',
        rows: [
          { arch_process_id: 11, module_id: '1', impl_key: 'same' },
          { arch_process_id: 12, module_id: '2', impl_key: 'same' },
        ],
      })
    ).rejects.toThrow();
    expect(mocked.splitMultiImpl).toHaveBeenCalledTimes(1);
  });

  it('preselects the recommended consumer module and approves with the chosen one', async () => {
    expect(defaultConsumerModuleId(depSuggestion)).toBe(202);
    const html = renderToString(
      <InboxDepSuggestionCard
        suggestion={depSuggestion}
        selectedModuleId={defaultConsumerModuleId(depSuggestion)}
        isAdmin
        busy={false}
        onSelectModule={() => {}}
        onDecide={() => {}}
      />
    );
    expect(html).toContain('<option value="202" selected="">');
    expect(html).toContain('<option value="201">');
    expect(html).toContain('data-testid="inbox-dep-approve-101-12"');
    expect(html).toContain('data-testid="inbox-dep-ignore-101-12"');
    expect(html).toContain('data-testid="inbox-dep-violation-101-12"');

    mocked.decideDependencySuggestion.mockResolvedValue({ decision: 'APPROVE', module_dependency_id: 77 });
    await submitDependencyDecision(depSuggestion, 'APPROVE', 201);
    expect(mocked.decideDependencySuggestion).toHaveBeenCalledWith(
      expect.objectContaining({
        consumer_arch_process_id: 12,
        service_id: 101,
        decision: 'APPROVE',
        consumer_module_id: 201,
      })
    );

    // 单模块消费方无推荐时自动取唯一候选；多候选且无推荐则不预选，批准被拦截
    const single: DepSuggestionDTO = {
      ...depSuggestion,
      candidate_consumer_modules: [depSuggestion.candidate_consumer_modules[0]],
      recommended_consumer_module_id: undefined,
      recommendation_source: '',
    };
    expect(defaultConsumerModuleId(single)).toBe(201);
    const ambiguous: DepSuggestionDTO = { ...depSuggestion, recommended_consumer_module_id: undefined };
    expect(defaultConsumerModuleId(ambiguous)).toBeUndefined();
    await expect(submitDependencyDecision(ambiguous, 'APPROVE', undefined)).rejects.toThrow();
    // IGNORE 不要求选择消费模块
    await submitDependencyDecision(ambiguous, 'IGNORE', undefined);
    expect(mocked.decideDependencySuggestion).toHaveBeenLastCalledWith(
      expect.objectContaining({ decision: 'IGNORE', consumer_module_id: undefined })
    );
  });

  it('shows progress bar after claim and refreshes to 100% via getAttributionJob polling', async () => {
    const running = renderToString(
      <AttributionProgressBar job={job({ processed: 4, total: 10 })} onCancel={() => {}} />
    );
    expect(running).toContain('role="progressbar"');
    expect(running).toContain('aria-valuenow="40"');
    expect(running).toContain('data-testid="inbox-job-cancel-900"');

    mocked.getAttributionJob
      .mockResolvedValueOnce(job({ processed: 4, total: 10 }))
      .mockResolvedValueOnce(job({ processed: 10, total: 10, status: 'DONE' }));
    const seen: number[] = [];
    const final = await trackAttributionJob(900, {
      intervalMs: 0,
      wait: async () => {},
      onUpdate: (j) => seen.push(attributionPercent(j)),
    });
    expect(mocked.getAttributionJob).toHaveBeenCalledTimes(2);
    expect(seen).toEqual([40, 100]);
    expect(final.status).toBe('DONE');

    const done = renderToString(
      <AttributionProgressBar job={job({ processed: 10, total: 10, status: 'DONE' })} onCancel={() => {}} />
    );
    expect(done).toContain('aria-valuenow="100"');
    expect(done).not.toContain('data-testid="inbox-job-cancel-900"');

    // SSE 事件合并
    const merged = mergeAttributionEvent(job({ processed: 4 }), {
      job_id: 900,
      trigger_type: 'SERVICE_CLAIM',
      target_id: 1,
      status: 'DONE',
      processed: 10,
      total: 10,
      cursor_session_id: 5,
      matched_count: 8,
      unresolved_count: 1,
      conflict_count: 1,
    });
    expect(merged.status).toBe('DONE');
    expect(attributionPercent(merged)).toBe(100);
  });
});
