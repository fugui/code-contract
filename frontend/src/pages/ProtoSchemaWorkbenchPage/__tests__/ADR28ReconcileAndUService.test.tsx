import { describe, expect, it, vi } from 'vitest';
import { renderToString } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { ConfirmProvider } from '@code/common';
import * as schemaApi from '../../../api/schema';
import {
  GlobalGovernanceConfigDrawer,
  PRODUCTION_USERVICE_PRESET_GLOBAL,
} from '../GlobalGovernanceConfigDrawer';
import { PoolReconcileReportDrawer } from '../PoolReconcileReportDrawer';
import { ProtoExportGovernancePane } from '../ProtoExportGovernancePane';
import { GitSourcesWorkbenchPane } from '../GitSourcesWorkbenchPane';
import { ProtoReadOnlyCodeViewer } from '../ProtoReadOnlyCodeViewer';
import type {
  GitRepoDTO,
  GitReposSyncSummaryDTO,
  PoolReconcileReportDTO,
  PoolReconcileRunDTO,
  ProtoFileContentResponse,
  ProtoFileDTO,
} from '../../../types';

const stripComments = (html: string): string => html.replace(/<!-- -->/g, '');

const sampleRun: PoolReconcileRunDTO = {
  id: 88,
  status: 'COMPLETED',
  triggered_by: 'admin',
  config_revision: 3,
  config_sha256: 'deadbeef1234',
  strip_pg_segment: true,
  closure_only: true,
  total_files: 12,
  service_count: 3,
  closure_file_count: 8,
  harvested_base_count: 2,
  logical_path_count: 10,
  conflict_count: 1,
  auto_fix_count: 2,
  quarantined_count: 1,
  bare_import_expanded_count: 4,
  post_strip_collision_count: 0,
  workspace_version: 'run-88-20261127T080000Z',
  duration_ms: 420,
  started_at: '2026-11-27T08:00:00Z',
};

const sampleReport: PoolReconcileReportDTO = {
  logical_map: {
    'app-svc:pg/AppSvc.proto': 'app/AppSvc.proto',
  },
  conflicts: [
    {
      logical_path: 'shared/DupMsg.proto',
      reason: 'BASE_LIB_PRIORITY',
      winner_repo_name: 'shared-core',
      winner_file_id: 11,
      content_identical: false,
      symbol_diff: {
        'shared-core:shared/pg/DupMsg.proto': ['shared.dup.WinnerExtra'],
        'legacy-dup:shared/pg/DupMsg.proto': ['shared.dup.LegacyExtra'],
      },
      candidates: [
        {
          proto_file_id: 11,
          git_repo_id: 101,
          repo_name: 'shared-core',
          bundle_id: 1001,
          subsystem_slug: 'shared',
          repo_file_path: 'shared/pg/DupMsg.proto',
          clean_rel_path: 'shared/DupMsg.proto',
          content_sha256: 'aaa111222333',
          unique_symbols: ['shared.dup.WinnerExtra'],
          is_winner: true,
        },
        {
          proto_file_id: 12,
          git_repo_id: 102,
          repo_name: 'legacy-dup',
          bundle_id: 1002,
          subsystem_slug: 'shared',
          repo_file_path: 'shared/pg/DupMsg.proto',
          clean_rel_path: 'shared/DupMsg.proto',
          content_sha256: 'bbb444555666',
          unique_symbols: ['shared.dup.LegacyExtra'],
          is_winner: false,
        },
      ],
    },
  ],
  auto_fixes: [
    {
      file_path: 'app/AppSvc.proto',
      repo_name: 'app-svc',
      fix_kind: 'fix(a)',
      old_import: 'wrong/Helper.proto',
      new_import: 'app/Helper.proto',
      round: 1,
      repo_id: 10,
      bundle_id: 100,
    },
  ],
  quarantined: [
    {
      logical_path: 'broken/BadRoot.proto',
      repo_name: 'broken-repo',
      repo_file_path: 'pg/BadRoot.proto',
      reason: 'L5:2 syntax error',
      error_count: 1,
      is_root_cause: true,
    },
  ],
  harvested: [
    {
      logical_path: 'il/ilfmp.proto',
      source_abs_path: '/codes/il/pg/ilfmp.proto',
      source_priority: 1,
      triggered_by: 'SYMBOL',
    },
  ],
  stats: {
    total_files: 12,
    service_count: 3,
    closure_file_count: 8,
    harvested_base_count: 2,
    logical_path_count: 10,
    conflict_count: 1,
    auto_fix_count: 2,
    quarantined_count: 1,
    bare_import_expanded_count: 4,
    post_strip_collision_count: 0,
    r7_rounds: 3,
    r8_rounds: 1,
    compile_error_count: 0,
  },
};

describe('ADR-28 Frontend Controls & Pool Reconcile Flows', () => {
  it('exports authentic production uService + /pg flattening preset configuration', () => {
    expect(PRODUCTION_USERVICE_PRESET_GLOBAL.enabled_rules).toContain('NORM-006');
    expect(PRODUCTION_USERVICE_PRESET_GLOBAL.enabled_rules).toContain('NORM-007');
    expect(PRODUCTION_USERVICE_PRESET_GLOBAL.default_syntax).toBe('auto');
    expect(PRODUCTION_USERVICE_PRESET_GLOBAL.strip_pg_segment).toBe(true);
    expect(PRODUCTION_USERVICE_PRESET_GLOBAL.exclude_subsystems).toEqual(
      expect.arrayContaining(['build', 'test', 'doc'])
    );
    expect(PRODUCTION_USERVICE_PRESET_GLOBAL.exclude_globs).toEqual(
      expect.arrayContaining([
        'build/**',
        'test/**',
        'doc/**',
        '**/test/**',
        '**/testdata/**',
        '**/*_test.proto',
        '**/mock/**',
      ])
    );
    expect(PRODUCTION_USERVICE_PRESET_GLOBAL.shared_base_sources?.length).toBeGreaterThanOrEqual(2);
    expect(PRODUCTION_USERVICE_PRESET_GLOBAL.pool_reconcile?.max_r8_rounds).toBe(150);

    const html = renderToString(
      <GlobalGovernanceConfigDrawer open isAdmin onClose={() => undefined} />
    );
    expect(html).toContain('载入现网 uService + 去 /pg 治理预设');
    expect(html).toContain('clean/ 目录去 /pg 扁平化');
    expect(html).toContain('shared_base_sources');
  });

  it('renders PoolReconcileReportDrawer with conflicts (symbol_diff), KPI cards and governance actions', () => {
    const html = stripComments(
      renderToString(
        <ConfirmProvider>
          <PoolReconcileReportDrawer
            open
            isAdmin
            run={sampleRun}
            report={sampleReport}
            onClose={() => undefined}
          />
        </ConfirmProvider>
      )
    );
    expect(html).toContain('run-88-20261127T080000Z');
    expect(html).toContain('shared/DupMsg.proto');
    expect(html).toContain('存在内容/符号差异');
    expect(html).toContain('shared.dup.WinnerExtra');
    expect(html).toContain('shared.dup.LegacyExtra');
    expect(html).toContain('R7 自动修复 (1)');
    expect(html).toContain('R8 隔离清单 (1)');
    expect(html).toContain('R4 收割基座 (1)');

    const summary: GitReposSyncSummaryDTO = {
      platform_active_total: 10,
      aligned_total: 10,
      pending_count: 0,
      has_proto_count: 5,
      no_proto_count: 4,
      no_local_cache_count: 1,
      excluded_count: 0,
      sync_failed_count: 0,
      shared_write_enabled: false,
      service_groups: ['app'],
    };
    const repos: GitRepoDTO[] = [];
    const govPaneHtml = renderToString(
      <ConfirmProvider>
        <ProtoExportGovernancePane
          isAdmin
          initialSummary={summary}
          initialRepos={repos}
          initialTotal={0}
        />
      </ConfirmProvider>
    );
    expect(govPaneHtml).toContain('全池编译收敛与工作区导出');
    expect(govPaneHtml).toContain('查看最近协调报告');
  });

  it('calls /proto-governance/auto-fixes/promote & passes closure filter params cleanly', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        promoted_import_rewrites: 1,
        promoted_manual_owners: 1,
        governance_revision: 4,
      }),
    });
    vi.stubGlobal('localStorage', { getItem: () => 'test-jwt' });
    vi.stubGlobal('fetch', fetchMock);
    try {
      const res = await schemaApi.promotePoolAutoFixes({
        run_id: 88,
        fixes: [
          {
            file_path: 'app/AppSvc.proto',
            fix_kind: 'fix(a)',
            old_import: 'wrong/Helper.proto',
            new_import: 'app/Helper.proto',
          },
        ],
      });
      expect(res.promoted_import_rewrites).toBe(1);
      expect(fetchMock).toHaveBeenCalledTimes(1);
      const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
      expect(url).toContain('/proto-governance/auto-fixes/promote');

      await schemaApi.listProtoFiles({
        in_service_closure: true,
        is_uservice_source: true,
      });
      const [listUrl] = fetchMock.mock.calls[1] as [string, RequestInit];
      expect(listUrl).toContain('in_service_closure=true');
      expect(listUrl).toContain('is_uservice_source=true');
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('renders uService, R7 auto-fix, R4 harvested badges, flattened path compare & line_map jump in viewer', () => {
    const contentResp: ProtoFileContentResponse = {
      file: {
        id: 501,
        schema_id: 90,
        git_repo_id: 10,
        git_repo_name: 'app-svc',
        bundle_id: 100,
        bundle_name: 'app-main',
        target_schema_name: 'app_main',
        category: 'BUSINESS',
        file_path: 'app/AppSvc.proto',
        repo_file_path: 'app/pg/AppSvc.proto',
        clean_rel_path: 'app/AppSvc.proto',
        package_name: 'app.v1',
        latest_version: 2,
        is_active_schema: true,
        has_source_info: true,
        pipeline_status: 'NORMALIZED',
        normalizer_version: 'rule-normalizer/v2',
        is_uservice_source: true,
        in_service_closure: true,
        is_harvested_base: true,
        auto_fix_count: 3,
        source_origin: 'GIT_SYNC',
        line_count: 4,
        updated_at: '2026-11-27T08:00:00Z',
      },
      version: {
        id: 2,
        schema_id: 90,
        version: 2,
        version_tag: 'v2',
        commit_sha: 'abc1234',
        content_sha256: 'sha256',
        source_type: 'GIT_SYNC',
        author_name: 'ci',
        summary: 'norm v2',
        created_at: '2026-11-27T08:00:00Z',
        line_count: 4,
        is_active: true,
      },
      content: 'syntax = "proto2";\npackage app.v1;\nimport "app/Helper.proto";\nmessage Ping {}',
      raw_content: 'package app.v1;\nuService PingSvc {}',
      line_map: { '3': 0, '4': 2 },
      symbol_anchors: [],
      violations: [],
    };

    const viewerHtml = stripComments(
      renderToString(
        <ProtoReadOnlyCodeViewer
          filePath={contentResp.file.file_path}
          cleanRelPath={contentResp.file.clean_rel_path}
          repoFilePath={contentResp.file.repo_file_path}
          content={contentResp.content}
          rawContent={contentResp.raw_content}
          lineMap={contentResp.line_map}
          pipelineStatus={contentResp.file.pipeline_status}
          normalizerVersion={contentResp.file.normalizer_version}
          isUServiceSource={contentResp.file.is_uservice_source}
          isHarvestedBase={contentResp.file.is_harvested_base}
          autoFixCount={contentResp.file.auto_fix_count}
        />
      )
    );
    expect(viewerHtml).toContain('uService 已转换');
    expect(viewerHtml).toContain('R7 自动修复 (3)');
    expect(viewerHtml).toContain('R4 收割基座');
    expect(viewerHtml).toContain('app/pg/AppSvc.proto');
    expect(viewerHtml).toContain('app/AppSvc.proto');
    expect(viewerHtml).toContain('跳转到 Raw 原始第 2 行');

    const files: ProtoFileDTO[] = [contentResp.file];
    const paneHtml = stripComments(
      renderToString(
        <MemoryRouter>
          <GitSourcesWorkbenchPane
            initialGitRepos={[]}
            initialFiles={files}
            initialTotal={1}
            initialSelectedFileId={501}
            initialContent={contentResp}
          />
        </MemoryRouter>
      )
    );
    expect(paneHtml).toContain('仅看服务闭包');
    expect(paneHtml).toContain('uService 已转换');
    expect(paneHtml).toContain('R7 自动修复 (3)');
  });
});
