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
import { GlobalGovernanceConfigDrawer } from '../../ProtoSchemaWorkbenchPage/GlobalGovernanceConfigDrawer';
import type {
  ArchivedExternalSchemaDTO,
  CurrentUserProfile,
  PipelineOverviewDTO,
  ProtoPublishRunDTO,
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
});
