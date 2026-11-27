import { describe, expect, it, vi } from 'vitest';
import { renderToString } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { ConfirmProvider } from '@code/common';
import { ModuleUserProvider } from '../../../context/ModuleUserContext';
import {
  PipelineStepperHeader,
  ProtoPipelineWorkbenchPage,
  StageCollectPane,
  StageNormalizePane,
  StageReconcilePane,
  StagePublishPane,
} from '../index';
import { formatCommitDate } from '../StageCollectPane';
import { GlobalGovernanceConfigDrawer } from '../../ProtoSchemaWorkbenchPage/GlobalGovernanceConfigDrawer';
import type {
  ArchivedExternalSchemaDTO,
  CurrentUserProfile,
  GitRepoDTO,
  PipelineOverviewDTO,
  PoolReconcileResponse,
  ProtoPublishRunDTO,
  ProtoSchemaLedgerItemDTO,
  PublishRunDetailsDTO,
} from '../../../types';

const adminUser: CurrentUserProfile = {
  id: 1,
  username: 'pipe_admin',
  name: '契约管理员',
  roles: ['contract_admin'],
  is_super_admin: false,
  is_module_admin: true,
  required_ad_role: 'contract_admin',
};

const viewerUser: CurrentUserProfile = {
  id: 2,
  username: 'pipe_viewer',
  name: '普通研发',
  roles: ['developer'],
  is_super_admin: false,
  is_module_admin: false,
  required_ad_role: 'contract_admin',
};

const sampleOverview: PipelineOverviewDTO = {
  lock_holder: null,
  publish_mode: 'AUTO',
  fanout_warn_threshold: 20,
  block_on_unresolved_conflict: false,
  governance_revision: 5,
  governance_config_sha256: 'abcdef1234567890',
  normalize_stale: true,
  normalize_stale_reasons: ['仓库 #12 新增未清洗变更'],
  reconcile_stale: true,
  reconcile_stale_reasons: ['清洗规范化配置发生变更'],
  publish_stale: true,
  publish_stale_reasons: ['阶段 ③ 尚未最新收敛'],
  stage1: {
    platform_active_total: 10,
    has_proto_count: 8,
    no_proto_count: 1,
    no_local_cache_count: 1,
    excluded_count: 0,
    sync_failed_count: 0,
    shared_write_enabled: true,
  },
  stage2: {
    total_files: 42,
    normalized_count: 40,
    quarantined_count: 2,
    uservice_count: 9,
    unassigned_repos: 1,
  },
  stage3: {
    conflict_count: 2,
    unresolved_conflict_count: 1,
    auto_fix_count: 3,
    quarantined_count: 1,
    harvested_base_count: 4,
  },
  stage4: {
    active_schema_count: 7,
    active_service_count: 18,
    active_operation_count: 55,
    breaking_blocked_count: 1,
    pending_upstream_count: 2,
    conflict_blocked_count: 0,
    archived_active_count: 1,
    publish_mode: 'AUTO',
  },
};

const sampleBreakingDetails: PublishRunDetailsDTO = {
  publish_mode: 'PREVIEW',
  pool_reconcile_run_id: 11,
  input_fingerprint: 'fp-123',
  block_on_unresolved_conflict: false,
  estimated_seconds: 2,
  breaking_items: [
    {
      bundle_id: 1,
      bundle_name: 'base-bundle',
      target_schema_name: 'schema-base',
      schema_id: 99,
      version: 3,
      breaking_rules: [
        {
          rule: 'WIRE_FIELD_TYPE_CHANGED',
          proto_file: 'common/base.proto',
          symbol: 'BaseMeta.req_id',
          message: '字段类型从 string 变更为 int64',
        },
      ],
      affected_dependents: [
        {
          module_id: 22,
          module_code: 'MOD_ORDER',
          module_name: '订单核心模块',
          arch_process_code: 'sut-order',
          owner_name: 'alice',
        },
      ],
    },
  ],
  conflict_risks: [
    {
      bundle_id: 2,
      bundle_name: 'order-bundle',
      target_schema_name: 'schema-order',
      logical_paths: ['shared/Msg.proto'],
      blocked: false,
    },
  ],
  bundle_results: [
    {
      bundle_id: 1,
      git_repo_id: 101,
      repo_name: 'repo-base',
      subsystem_slug: 'common',
      bundle_name: 'base-bundle',
      target_schema_name: 'schema-base',
      category: 'BASE_LIB',
      layer: 0,
      status: 'BLOCKED_BREAKING',
      reason: '检测到 1 项破坏性变更',
    },
    {
      bundle_id: 2,
      git_repo_id: 102,
      repo_name: 'repo-order',
      subsystem_slug: 'order',
      bundle_name: 'order-bundle',
      target_schema_name: 'schema-order',
      category: 'BUSINESS',
      layer: 1,
      status: 'PENDING_UPSTREAM',
      blocked_by_bundles: ['base-bundle'],
      reason: '等待上游契约包就绪',
    },
  ],
};

const samplePublishRun: ProtoPublishRunDTO = {
  id: 88,
  pool_reconcile_run_id: 11,
  publish_mode: 'PREVIEW',
  status: 'PREVIEW_READY',
  total_bundles: 2,
  published_count: 0,
  skipped_count: 0,
  breaking_blocked_count: 1,
  conflict_blocked_count: 0,
  pending_upstream_count: 1,
  failed_count: 0,
  fanout_count: 1,
  duration_ms: 350,
  started_at: '2026-11-27T10:00:00Z',
};

const sampleArchived: ArchivedExternalSchemaDTO[] = [
  {
    schema_id: 501,
    name: 'legacy-order-pb',
    version: 1,
    source_type: 'UPLOAD_PB',
    category: 'BUSINESS',
    is_active: true,
    created_at: '2026-10-01T00:00:00Z',
    total_services: 2,
    covered_services: 1,
    coverage_ratio: 0.5,
    uncovered_names: ['legacy.v1.OrderSvc'],
  },
];

describe('ProtoPipelineWorkbenchPage Suite (ADR-29 M44)', () => {
  it('renders 4-stage stepper with stale badges, lock banner, and one-click pipeline control', () => {
    const lockedOverview: PipelineOverviewDTO = {
      ...sampleOverview,
      lock_holder: {
        job_type: 'PIPELINE_RUN',
        job_id: 77,
        holder: 'alice',
        acquired_at: '2026-11-27T10:00:00Z',
      },
    };
    const html = renderToString(
      <MemoryRouter initialEntries={['/proto-pipeline?stage=publish']}>
        <ModuleUserProvider
          value={{ user: adminUser, loadingUser: false, isEmbedded: false, reloadUser: async () => {} }}
        >
          <ConfirmProvider>
            <ProtoPipelineWorkbenchPage initialOverview={lockedOverview} />
          </ConfirmProvider>
        </ModuleUserProvider>
      </MemoryRouter>
    );

    expect(html).toContain('仓库拉取');
    expect(html).toContain('清洗规范化');
    expect(html).toContain('全池依赖收敛');
    expect(html).toContain('拓扑编译与发布');
    expect(html).toContain('pipeline-lock-banner');
    expect(html).toContain('alice');
    expect(html).toContain('stale-badge-normalize');
    expect(html).toContain('stale-badge-reconcile');
    expect(html).toContain('stale-badge-publish');
    expect(html).toContain('一键执行流水线');
    expect(html).not.toContain('上传描述集 (.pb)');
    expect(html).not.toContain('在线源码直编');
  });

  it('hides write triggers for non-admin viewer users (RBAC read-only)', () => {
    const html = renderToString(
      <MemoryRouter>
        <ModuleUserProvider
          value={{ user: viewerUser, loadingUser: false, isEmbedded: false, reloadUser: async () => {} }}
        >
          <PipelineStepperHeader
            activeStage="collect"
            onSelectStage={vi.fn()}
            overview={sampleOverview}
            lockHolder={null}
            pipelineProgress={null}
            isAdmin={false}
            running={false}
            fromStage="COLLECT_NORMALIZE"
            onChangeFromStage={vi.fn()}
            onRunAll={vi.fn()}
            onRefresh={vi.fn()}
          />
          <StageCollectPane
            overview={sampleOverview}
            isAdmin={false}
            locked={false}
            syncProgress={null}
            onRefresh={vi.fn()}
          />
          <StageNormalizePane
            overview={sampleOverview}
            isAdmin={false}
            locked={false}
            onRefresh={vi.fn()}
          />
          <StageReconcilePane
            overview={sampleOverview}
            isAdmin={false}
            locked={false}
            poolProgress={null}
            onRefresh={vi.fn()}
          />
        </ModuleUserProvider>
      </MemoryRouter>
    );

    expect(html).not.toContain('一键执行流水线');
    expect(html).not.toContain('立即拉取全平台仓库');
    expect(html).not.toContain('按当前规则免网络重洗');
    expect(html).not.toContain('立即执行全池收敛');
  });

  it('renders StagePublishPane PREVIEW_READY confirm banner, breaking approval panel & archived migration', () => {
    const html = renderToString(
      <MemoryRouter>
        <ConfirmProvider>
          <StagePublishPane
            overview={sampleOverview}
            isAdmin={true}
            locked={false}
            publishProgress={null}
            onRefresh={vi.fn()}
            initialPublishRun={samplePublishRun}
            initialPublishDetails={sampleBreakingDetails}
            initialArchivedSchemas={sampleArchived}
          />
        </ConfirmProvider>
      </MemoryRouter>
    );

    const cleanHtml = html.replace(/<!-- -->/g, '');
    expect(cleanHtml).toContain('confirm-preview-publish-btn');
    expect(cleanHtml).toContain('确认切入生效池');
    expect(cleanHtml).toContain('open-breaking-approve-btn');
    expect(cleanHtml).toContain('审批放行并自动补发下游');
    expect(cleanHtml).toContain('订单核心模块');
    expect(cleanHtml).toContain('等待上游放行');
    expect(cleanHtml).toContain('历史归档外部来源迁移清单');
    expect(cleanHtml).toContain('生效中 1');
  });

  it('renders block_on_unresolved_conflict toggle inside GlobalGovernanceConfigDrawer', () => {
    const html = renderToString(
      <GlobalGovernanceConfigDrawer
        open={true}
        isAdmin={true}
        onClose={vi.fn()}
      />
    );
    expect(html).toContain('toggle-block-on-conflict');
    expect(html).toContain('跨仓冲突发布拦截');
  });

  it('renders embedded repo ledgers in StageCollectPane & StageNormalizePane (quarantined first)', () => {
    const repos: GitRepoDTO[] = [
      {
        id: 1,
        platform_repo_id: 101,
        name: 'clean-repo',
        provider: 'GITLAB',
        repo_url: 'https://git.example.com/clean.git',
        api_base_url: 'https://git.example.com',
        default_branch: 'main',
        auth_type: 'NONE',
        credential_hint: '',
        proto_status: 'HAS_PROTO',
        service_group: 'trade-core',
        sync_enabled: true,
        last_sync_commit: '1122334455',
        ref_commit_time: '2026-11-20T08:30:00Z',
        whitelist_count: 3,
        normalized_count: 3,
        quarantined_count: 0,
        effective_export_dir: 'clean/trade/clean-repo',
        has_credentials: false,
        has_webhook_secret: false,
        sync_mode: 'POLL',
        poll_interval_sec: 300,
        auto_compile: true,
        auto_activate: false,
        lint_on_sync: true,
        last_sync_status: 'SUCCESS',
        last_sync_error: '',
        description: '',
        created_by: 1,
        created_at: '',
        updated_at: '',
        bundles: [],
      },
      {
        id: 2,
        platform_repo_id: 102,
        name: 'broken-repo',
        provider: 'GITLAB',
        repo_url: 'https://git.example.com/broken.git',
        api_base_url: 'https://git.example.com',
        default_branch: 'main',
        auth_type: 'NONE',
        credential_hint: '',
        proto_status: 'HAS_PROTO',
        service_group: 'pay-core',
        sync_enabled: true,
        last_sync_commit: 'abcdef1234',
        whitelist_count: 4,
        normalized_count: 2,
        quarantined_count: 2,
        effective_export_dir: 'clean/pay/broken-repo',
        has_export_override: true,
        has_credentials: false,
        has_webhook_secret: false,
        sync_mode: 'POLL',
        poll_interval_sec: 300,
        auto_compile: true,
        auto_activate: false,
        lint_on_sync: true,
        last_sync_status: 'SUCCESS',
        last_sync_error: '',
        description: '',
        created_by: 1,
        created_at: '',
        updated_at: '',
        bundles: [],
      },
    ];

    const collectHtml = renderToString(
      <MemoryRouter>
        <StageCollectPane
          overview={sampleOverview}
          isAdmin={true}
          locked={false}
          syncProgress={null}
          onRefresh={vi.fn()}
          initialRepos={repos}
          initialTotal={2}
        />
      </MemoryRouter>
    );
    expect(collectHtml).toContain('stage-collect-repos-table');
    expect(collectHtml).toContain('clean-repo');
    expect(collectHtml).toContain('broken-repo');
    expect(collectHtml).toContain('/proto-docs?docs_view=sources&amp;repo_id=1');
    expect(collectHtml).toContain('单仓拉取');
    expect(collectHtml).toContain('role="tab" aria-selected="true" class="code-pill-btn code-pill-btn--active">含 Proto</button>');
    expect(collectHtml).toContain('role="tab" aria-selected="false" class="code-pill-btn ">全部仓库</button>');
    expect(collectHtml).toContain('1122334');
    expect(collectHtml).toContain('repo-commit-date-1');
    expect(collectHtml).toContain(formatCommitDate('2026-11-20T08:30:00Z'));
    expect(collectHtml).not.toContain('repo-commit-date-2');

    const normalizeHtml = renderToString(
      <MemoryRouter>
        <StageNormalizePane
          overview={sampleOverview}
          isAdmin={true}
          locked={false}
          onRefresh={vi.fn()}
          initialRepos={repos}
          initialTotal={2}
        />
      </MemoryRouter>
    );
    expect(normalizeHtml).toContain('stage-normalize-repos-table');
    expect(normalizeHtml).toContain('clean/pay/broken-repo');
    expect(normalizeHtml).toContain('已覆写');
    expect(normalizeHtml).toContain('目录与规则');
    // broken-repo (quarantined_count=2) 应优先于 clean-repo 置顶渲染
    expect(normalizeHtml.indexOf('broken-repo')).toBeLessThan(normalizeHtml.indexOf('clean-repo'));
  });

  it('renders 4 subtabs and inline conflict arbitration in StageReconcilePane (RBAC guarded)', () => {
    const reconcileResp: PoolReconcileResponse = {
      run: {
        id: 11,
        status: 'COMPLETED',
        triggered_by: 'admin',
        config_revision: 5,
        config_sha256: 'abc',
        strip_pg_segment: true,
        closure_only: true,
        total_files: 10,
        service_count: 2,
        closure_file_count: 8,
        harvested_base_count: 1,
        logical_path_count: 8,
        conflict_count: 1,
        auto_fix_count: 0,
        quarantined_count: 0,
        bare_import_expanded_count: 0,
        post_strip_collision_count: 0,
        workspace_version: 'v1',
        duration_ms: 120,
        started_at: '2026-11-27T10:00:00Z',
      },
      report: {
        logical_map: {},
        auto_fixes: [],
        quarantined: [],
        harvested: [
          {
            logical_path: 'include/common.proto',
            source_abs_path: '/cache/base/include/common.proto',
            source_priority: 1,
            triggered_by: 'IMPORT',
          },
        ],
        conflicts: [
          {
            logical_path: 'shared/types.proto',
            reason: 'R3 自动投票',
            winner_repo_name: 'repo-alpha',
            winner_file_id: 101,
            content_identical: false,
            candidates: [
              {
                proto_file_id: 101,
                git_repo_id: 11,
                repo_name: 'repo-alpha',
                bundle_id: 101,
                subsystem_slug: 'core',
                repo_file_path: 'pg/shared/types.proto',
                clean_rel_path: 'shared/types.proto',
                content_sha256: 'aaaa111122223333',
                unique_symbols: ['AlphaMsg'],
                is_winner: true,
              },
              {
                proto_file_id: 102,
                git_repo_id: 12,
                repo_name: 'repo-beta',
                bundle_id: 102,
                subsystem_slug: 'trade',
                repo_file_path: 'pg/shared/types.proto',
                clean_rel_path: 'shared/types.proto',
                content_sha256: 'bbbb444455556666',
                unique_symbols: ['BetaMsg'],
                is_winner: false,
              },
            ],
          },
        ],
        stats: {
          total_files: 10,
          service_count: 2,
          closure_file_count: 8,
          harvested_base_count: 1,
          logical_path_count: 8,
          conflict_count: 1,
          auto_fix_count: 0,
          quarantined_count: 0,
          bare_import_expanded_count: 0,
          post_strip_collision_count: 0,
          r7_rounds: 1,
          r8_rounds: 1,
          compile_error_count: 0,
        },
      },
    };

    const adminHtml = renderToString(
      <MemoryRouter>
        <StageReconcilePane
          overview={sampleOverview}
          isAdmin={true}
          locked={false}
          poolProgress={null}
          onRefresh={vi.fn()}
          initialReconcileResp={reconcileResp}
        />
      </MemoryRouter>
    );
    const cleanAdminHtml = adminHtml.replace(/<!-- -->/g, '');
    expect(cleanAdminHtml).toContain('共享基座收割 (1)');
    expect(cleanAdminHtml).toContain('repo-beta');
    expect(cleanAdminHtml).toContain('BetaMsg');
    expect(cleanAdminHtml).toContain('inline-resolve-conflict-12-102');
    expect(cleanAdminHtml).toContain('设为权威提供方');

    const viewerHtml = renderToString(
      <MemoryRouter>
        <StageReconcilePane
          overview={sampleOverview}
          isAdmin={false}
          locked={false}
          poolProgress={null}
          onRefresh={vi.fn()}
          initialReconcileResp={reconcileResp}
        />
      </MemoryRouter>
    );
    expect(viewerHtml).not.toContain('设为权威提供方');
  });

  it('renders StagePublishPane active contract ledger subtab without external upload', () => {
    const ledgerItems: ProtoSchemaLedgerItemDTO[] = [
      {
        id: 201,
        name: 'order-proto',
        version: 4,
        version_tag: 'git-1234567',
        category: 'BUSINESS',
        source_type: 'GIT_PIPELINE',
        file_name: 'order.pb',
        file_sha256: 'abc',
        file_size: 1024,
        is_active: true,
        proto_file_count: 2,
        service_count: 1,
        operation_count: 3,
        proto_files: ['order/v1/order.proto'],
        external_imports: [],
        description: '',
        created_by: 1,
        created_at: '',
        updated_at: '',
        version_count: 1,
        latest_version: 4,
        claimed_service_count: 1,
        claimed_ratio: 1,
        resolved_dependencies: [],
      },
    ];

    const html = renderToString(
      <MemoryRouter>
        <ConfirmProvider>
          <StagePublishPane
            overview={sampleOverview}
            isAdmin={true}
            locked={false}
            publishProgress={null}
            onRefresh={vi.fn()}
            initialSubTab="ledger"
            initialLedgerSchemas={ledgerItems}
          />
        </ConfirmProvider>
      </MemoryRouter>
    );

    expect(html).toContain('stage4-schema-ledger-panel');
    expect(html).toContain('生效契约库版本台账');
    expect(html).toContain('order-proto');
    expect(html).not.toContain('上传描述集 (.pb)');
    expect(html).not.toContain('⬆️ 新版');
  });

  it('formats ref_commit_time safely with formatCommitDate', () => {
    const formatted = formatCommitDate('2026-11-20T08:30:00Z');
    expect(formatted).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/);
    expect(formatCommitDate(null)).toBe('');
    expect(formatCommitDate(undefined)).toBe('');
    expect(formatCommitDate('')).toBe('');
    expect(formatCommitDate('not-a-valid-date')).toBe('');
  });
});
