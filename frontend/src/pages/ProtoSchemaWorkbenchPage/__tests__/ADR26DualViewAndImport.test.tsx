import { describe, expect, it } from 'vitest';
import { renderToString } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { GitSourcesWorkbenchPane } from '../GitSourcesWorkbenchPane';
import { PlatformRepoImportDrawer } from '../PlatformRepoImportDrawer';
import { ProtoConflictArbitrationDrawer } from '../ProtoConflictArbitrationDrawer';
import { ProtoReadOnlyCodeViewer } from '../ProtoReadOnlyCodeViewer';
import type {
  BaselineReportDTO,
  GitRepoDTO,
  PlatformRepoPreviewDTO,
  ProtoConflictsReportDTO,
  ProtoFileContentResponse,
  ProtoFileDTO,
  ProtoImportOwnerDTO,
} from '../../../types';

const sharedRepo: GitRepoDTO = {
  id: 21,
  platform_repo_id: 301,
  source_mode: 'PLATFORM_SHARED',
  sync_strategy: 'LOCAL_CACHE_ONLY',
  subsystem_slug: 'trade-core',
  resolved_branch: 'main',
  is_stale: true,
  normalized_count: 5,
  quarantined_count: 1,
  external_copy_count: 1,
  name: 'trade-gateway',
  provider: 'GITLAB',
  repo_url: 'https://git.example.com/trade/gateway.git',
  api_base_url: 'https://git.example.com',
  default_branch: 'main',
  auth_type: 'NONE',
  has_credentials: false,
  credential_hint: '',
  has_webhook_secret: false,
  sync_mode: 'POLL',
  poll_interval_sec: 1800,
  auto_compile: true,
  auto_activate: false,
  lint_on_sync: true,
  last_sync_commit: 'abcdef1234567890',
  last_sync_status: 'SUCCESS',
  last_sync_error: '',
  description: '平台共享交易网关契约仓',
  created_by: 1,
  created_at: '2026-11-26T00:00:00Z',
  updated_at: '2026-11-26T00:00:00Z',
  bundles: [
    {
      id: 201,
      git_repo_id: 21,
      bundle_name: 'pg-trade',
      target_schema_name: 'trade-gateway-proto',
      category: 'BUSINESS',
      subdir_path: 'pg/trade',
      include_dirs: ['.'],
      entry_files: [],
      file_globs: ['**/*.proto'],
      exclude_globs: [],
      auto_compile: true,
      auto_activate: false,
      mr_auto_publish: 'DISABLED',
      lint_on_sync: true,
      last_sync_commit: 'abcdef1',
      description: '',
      created_at: '2026-11-26T00:00:00Z',
      updated_at: '2026-11-26T00:00:00Z',
    },
  ],
};

const sampleFiles: ProtoFileDTO[] = [
  {
    id: 901,
    schema_id: 1,
    git_repo_id: 21,
    git_repo_name: 'trade-gateway',
    bundle_id: 201,
    bundle_name: 'pg-trade',
    target_schema_name: 'trade-gateway-proto',
    category: 'BUSINESS',
    file_path: 'trade/order.proto',
    repo_file_path: 'pg/trade/order.proto',
    package_name: 'acme.trade.v1',
    latest_version: 2,
    is_active_schema: false,
    has_source_info: true,
    is_deleted: false,
    pipeline_status: 'NORMALIZED',
    normalizer_version: 'passthrough-0',
    is_external_copy: true,
    source_origin: 'GIT_SYNC',
    line_count: 4,
    updated_at: '2026-11-26T00:00:00Z',
  },
  {
    id: 902,
    schema_id: 0,
    git_repo_id: 21,
    git_repo_name: 'trade-gateway',
    bundle_id: 201,
    bundle_name: 'pg-trade',
    target_schema_name: 'trade-gateway-proto',
    category: 'BUSINESS',
    file_path: 'trade/broken.proto',
    repo_file_path: 'pg/trade/broken.proto',
    package_name: 'acme.trade.v1',
    latest_version: 1,
    is_active_schema: false,
    has_source_info: true,
    is_deleted: false,
    pipeline_status: 'QUARANTINED',
    status_reason: 'PARSE_FAILED: L3: syntax error',
    normalizer_version: 'passthrough-0',
    is_external_copy: false,
    source_origin: 'GIT_SYNC',
    line_count: 3,
    updated_at: '2026-11-26T00:00:00Z',
  },
];

const sampleContent: ProtoFileContentResponse = {
  file: sampleFiles[0],
  version: {
    id: 1,
    proto_file_id: 901,
    schema_id: 1,
    version: 2,
    version_tag: 'git-abcdef1',
    commit_sha: 'abcdef1',
    content_sha256: 'hash',
    source_type: 'GIT_SYNC',
    author_name: 'bot',
    summary: 'sync',
    created_at: '2026-11-26T00:00:00Z',
    line_count: 4,
    is_active: false,
  },
  content: 'syntax = "proto3";\npackage acme.trade.v1;\nmessage Order { string id = 1; }\n',
  raw_content: '// raw header\nsyntax = "proto3";\npackage acme.trade.v1;\nmessage Order { string id = 1; }\n',
  line_map: { '1': 2, '2': 3, '3': 4 },
  violations: [],
  symbol_anchors: [],
};

describe('ADR-26 Platform Shared Repo, Dual View, Raw/Clean LineMap & Conflict Arbitration UI', () => {
  it('GitSourcesWorkbenchPane renders dual-view toggle, stale/shared badges, quarantine/copy pills and Raw/Clean viewer', () => {
    const html = renderToString(
      <MemoryRouter initialEntries={['/schemas?tab=sources']}>
        <GitSourcesWorkbenchPane
          isAdmin
          initialGitRepos={[sharedRepo]}
          initialSelectedRepoId={21}
          initialFiles={sampleFiles}
          initialTotal={2}
          initialSelectedFileId={901}
          initialContent={sampleContent}
        />
      </MemoryRouter>
    );

    expect(html).toContain('PLATFORM_SHARED');
    expect(html).toContain('LOCAL_CACHE_ONLY');
    expect(html).toContain('陈旧待同步');
    expect(html).toContain('按子系统/仓库');
    expect(html).toContain('按 Proto 包路径');
    expect(html).toContain('平台仓导入');
    expect(html).toContain('冲突仲裁');
    expect(html).toContain('重跑清洗');
    expect(html).toContain('QUARANTINED');
    expect(html).toContain('外部副本');
    expect(html).toContain('Clean');
    expect(html).toContain('Raw');
    expect(html).toContain('并排对比');
    expect(html).toContain('原L2');
  });

  it('ProtoReadOnlyCodeViewer renders quarantine reason banner when statusReason present', () => {
    const html = renderToString(
      <ProtoReadOnlyCodeViewer
        filePath="pg/trade/broken.proto"
        content={'syntax = "proto3";\nmessage Bad {}\n'}
        rawContent={'// raw\nsyntax = "proto3";\nmessage Bad {}\n'}
        lineMap={{ '1': 2, '2': 3 }}
        pipelineStatus="QUARANTINED"
        statusReason="PARSE_FAILED: L3: expecting semicolon"
        normalizerVersion="passthrough-0"
      />
    );
    expect(html).toContain('QUARANTINED');
    expect(html).toContain('PARSE_FAILED: L3: expecting semicolon');
    expect(html).toContain('passthrough-0');
    expect(html).toContain('原L2');
  });

  it('PlatformRepoImportDrawer renders discovered repos, pg whitelist probe & baseline metrics with RBAC guard', () => {
    const previews: PlatformRepoPreviewDTO[] = [
      {
        platform_repo_id: 301,
        name: 'payment-core',
        url: 'https://git.example.com/pay/payment-core.git',
        http_url: 'https://git.example.com/pay/payment-core.git',
        branch: 'main',
        department_id: 1,
        department_name: '基础研发',
        service_group: 'pay-sub',
        subsystem_slug: 'pay-sub',
        owner_id: 9,
        already_imported: false,
        local_cache_present: true,
        resolved_branch: 'main',
        pg_dirs: ['pg', 'order/pg'],
        whitelist_proto_count: 14,
        outside_whitelist_count: 2,
        candidate_bundles: [
          {
            bundle_name: 'pg',
            target_schema_name: 'payment-core-pg',
            category: 'BUSINESS',
            subdir_path: 'pg',
          },
        ],
      },
    ];
    const baseline: BaselineReportDTO = {
      generated_at: '2026-11-26T00:00:00Z',
      total_repos: 12,
      whitelist_files: 120,
      normalized_files: 118,
      quarantined_files: 2,
      syntax_pass_rate: 0.983,
      compile_pass_rate: 0.91,
      error_distribution: { PARSE_FAILED: 2 },
      repos: [],
    };

    const adminHtml = renderToString(
      <PlatformRepoImportDrawer
        open
        isAdmin
        initialRepos={previews}
        initialBaseline={baseline}
        onClose={() => undefined}
      />
    );
    expect(adminHtml).toContain('payment-core');
    expect(adminHtml).toContain('pg, order/pg');
    expect(adminHtml).toContain('已命中共享缓存');
    expect(adminHtml).toContain('批量导入');
    expect(adminHtml).toContain('98.3%');

    const viewerHtml = renderToString(
      <PlatformRepoImportDrawer
        open
        isAdmin={false}
        initialRepos={previews}
        onClose={() => undefined}
      />
    );
    expect(viewerHtml).not.toContain('批量导入 (');
  });

  it('ProtoConflictArbitrationDrawer renders import ambiguities, symbol conflicts, and owner list', () => {
    const conflicts: ProtoConflictsReportDTO = {
      import_ambiguities: [
        {
          logical_path: 'common/types.proto',
          candidates: [
            {
              git_repo_id: 1,
              repo_name: 'repo-a',
              bundle_id: 10,
              bundle_name: 'base-a',
              category: 'BASE_LIB',
              logical_path: 'common/types.proto',
              repo_file_path: 'pg/common/types.proto',
            },
            {
              git_repo_id: 2,
              repo_name: 'repo-b',
              bundle_id: 20,
              bundle_name: 'base-b',
              category: 'BASE_LIB',
              logical_path: 'common/types.proto',
              repo_file_path: 'pg/common/types.proto',
            },
          ],
        },
      ],
      symbol_conflicts: [
        {
          symbol_kind: 'MESSAGE',
          symbol_full_name: 'acme.common.Money',
          content_matches: false,
          candidates: [
            {
              proto_file_id: 101,
              git_repo_id: 1,
              repo_name: 'repo-a',
              bundle_id: 10,
              bundle_name: 'base-a',
              category: 'BASE_LIB',
              logical_path: 'common/types.proto',
              repo_file_path: 'pg/common/types.proto',
              content_sha256: 'aaa',
              is_external_copy: false,
            },
          ],
        },
      ],
    };
    const owners: ProtoImportOwnerDTO[] = [
      {
        id: 1,
        logical_path: 'common/meta.proto',
        owner_git_repo_id: 1,
        owner_bundle_id: 10,
        source: 'MANUAL',
        updated_by: 'admin',
        updated_at: '2026-11-26T00:00:00Z',
      },
    ];

    const html = renderToString(
      <ProtoConflictArbitrationDrawer
        open
        isAdmin
        initialConflicts={conflicts}
        initialOwners={owners}
        onClose={() => undefined}
      />
    );
    expect(html).toContain('common/types.proto');
    expect(html).toContain('acme.common.Money');
    expect(html).toContain('文件内容存在差异');
    expect(html).toContain('设为权威提供方');
    expect(html).toContain('common/meta.proto');
    expect(html).toContain('MANUAL');
  });
});
