import { describe, expect, it } from 'vitest';
import { renderToString } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { buildCleanDirTree, buildCleanSubsystemTree } from '../cleanTreeBuilder';
import { GitSourcesWorkbenchPane } from '../GitSourcesWorkbenchPane';
import type {
  GitRepoDTO,
  ProtoFileContentResponse,
  ProtoFileDTO,
  ProtoFilesTreeResponse,
  ProtoTreeRepoGroupDTO,
} from '../../../types';

const makeFile = (overrides: Partial<ProtoFileDTO> & { id: number; file_path: string }): ProtoFileDTO => ({
  schema_id: 1,
  git_repo_id: 10,
  git_repo_name: 'repo-demo',
  target_schema_name: 'demo-schema',
  category: 'BUSINESS',
  package_name: 'demo.v1',
  latest_version: 1,
  is_active_schema: true,
  has_source_info: true,
  is_deleted: false,
  pipeline_status: 'NORMALIZED',
  source_origin: 'GIT_SYNC',
  line_count: 10,
  updated_at: '2026-11-27T08:00:00Z',
  ...overrides,
});

describe('cleanTreeBuilder & GitSourcesWorkbenchPane readOnlyConsumer layout', () => {
  it('builds nested clean/ tree with single-child folder compaction, folder-first sorting, and lazy repo placeholders', () => {
    const files: ProtoFileDTO[] = [
      makeFile({
        id: 1,
        git_repo_id: 10,
        file_path: 'demo/proto/account.proto',
        clean_rel_path: 'demo/proto/account.proto',
      }),
      makeFile({
        id: 2,
        git_repo_id: 10,
        file_path: 'pg.proto',
        clean_rel_path: 'pg.proto',
      }),
      makeFile({
        id: 3,
        git_repo_id: 10,
        file_path: 'fab/shared/IdenticalUtilMsg.proto',
        clean_rel_path: 'fab/shared/IdenticalUtilMsg.proto',
      }),
    ];

    const dirNodes = buildCleanDirTree(files, 10);
    expect(dirNodes.map((n) => n.kind)).toEqual(['folder', 'folder', 'file']);
    expect(dirNodes[0].kind === 'folder' && dirNodes[0].label).toBe('demo/proto');
    expect(dirNodes[1].kind === 'folder' && dirNodes[1].label).toBe('fab/shared');
    expect(dirNodes[2].kind === 'file' && dirNodes[2].name).toBe('pg.proto');

    const groups: ProtoTreeRepoGroupDTO[] = [
      {
        subsystem_slug: '_unassigned',
        export_target_repo_dir: 'sandbox-unassigned-svc',
        git_repo_id: 30,
        git_repo_name: 'sandbox-unassigned-svc',
        source_mode: 'PLATFORM_SHARED',
        sync_strategy: 'LOCAL_CACHE_ONLY',
        is_stale: false,
        total_count: 1,
        files: [],
      },
      {
        subsystem_slug: 'SBX-ASSET',
        export_target_repo_dir: 'sandbox-account-svc',
        git_repo_id: 10,
        git_repo_name: 'sandbox-account-svc',
        source_mode: 'PLATFORM_SHARED',
        sync_strategy: 'LOCAL_CACHE_ONLY',
        is_stale: false,
        total_count: 3,
        files,
      },
    ];

    const subsystems = buildCleanSubsystemTree(groups);
    expect(subsystems.map((s) => s.slug)).toEqual(['SBX-ASSET', '_unassigned']);
    expect(subsystems[0].totalCount).toBe(3);
    expect(subsystems[0].repos[0].filesLoaded).toBe(true);
    expect(subsystems[1].repos[0].filesLoaded).toBe(false);
    expect(subsystems[1].repos[0].totalCount).toBe(1);
    expect(subsystems[1].repos[0].children).toEqual([]);
  });

  it('renders clean split layout without top repo bar, flat list, pagination, or redundant header badges in readOnlyConsumer mode', () => {
    const repo: GitRepoDTO = {
      id: 41,
      platform_repo_id: 401,
      source_mode: 'PLATFORM_SHARED',
      sync_strategy: 'LOCAL_CACHE_ONLY',
      subsystem_slug: '_unassigned',
      export_target_subsystem: '_unassigned',
      export_target_repo_dir: 'sandbox-unassigned-svc',
      name: 'sandbox-unassigned-svc',
      provider: 'GITLAB',
      repo_url: 'https://git.example.com/misc/unassigned.git',
      api_base_url: 'https://git.example.com',
      default_branch: 'main',
      auth_type: 'NONE',
      has_credentials: false,
      credential_hint: '',
      has_webhook_secret: false,
      sync_mode: 'MANUAL',
      poll_interval_sec: 0,
      auto_compile: false,
      auto_activate: false,
      lint_on_sync: false,
      last_sync_commit: '0b90fc3123456789',
      last_sync_status: 'SUCCESS',
      last_sync_error: '',
      description: '',
      created_by: 1,
      created_at: '2026-11-27T08:00:00Z',
      updated_at: '2026-11-27T08:00:00Z',
      bundles: [
        {
          id: 401,
          git_repo_id: 41,
          bundle_name: 'default',
          target_schema_name: 'sandbox-unassigned-svc',
          category: 'BUSINESS',
          subdir_path: '',
          include_dirs: ['.'],
          entry_files: [],
          file_globs: ['**/*.proto'],
          exclude_globs: [],
          auto_compile: false,
          auto_activate: false,
          mr_auto_publish: 'OFF',
          lint_on_sync: false,
          last_sync_commit: '0b90fc3',
          description: '',
          created_at: '2026-11-27T08:00:00Z',
          updated_at: '2026-11-27T08:00:00Z',
        },
      ],
    };

    const file = makeFile({
      id: 701,
      git_repo_id: 41,
      git_repo_name: 'sandbox-unassigned-svc',
      file_path: 'legacy/health_check.proto',
      clean_rel_path: 'legacy/health_check.proto',
      repo_file_path: 'pg/legacy/health_check.proto',
      package_name: 'misc.health',
      normalizer_version: 'rule-normalizer/v2+1198895a',
    });

    const tree: ProtoFilesTreeResponse = {
      view: 'subsystem',
      total: 1,
      summary_only: true,
      groups: [
        {
          subsystem_slug: '_unassigned',
          export_target_repo_dir: 'sandbox-unassigned-svc',
          git_repo_id: 41,
          git_repo_name: 'sandbox-unassigned-svc',
          source_mode: 'PLATFORM_SHARED',
          sync_strategy: 'LOCAL_CACHE_ONLY',
          is_stale: false,
          total_count: 1,
          files: [file],
        },
      ],
    };

    const content: ProtoFileContentResponse = {
      file,
      version: {
        id: 1,
        proto_file_id: 701,
        schema_id: 1,
        version: 1,
        version_tag: 'git-0b90fc3',
        commit_sha: '0b90fc3123456789',
        commit_message: 'chore(sandbox): initial contract corpus (v1)',
        content_sha256: 'abc',
        source_type: 'GIT_SYNC',
        author_name: 'Sandbox Bot',
        summary: 'chore(sandbox): initial contract corpus (v1)',
        created_at: '2026-11-27T08:00:00Z',
        line_count: 4,
        is_active: true,
      },
      content: 'syntax = "proto3";\npackage misc.health;\nmessage CheckHealthReq {}\n',
      raw_content: 'syntax = "proto3";\npackage misc.health;\nmessage CheckHealthReq {}\n',
      line_map: { '1': 1, '2': 2, '3': 3 },
      violations: [],
      symbol_anchors: [
        {
          symbol_kind: 'SERVICE',
          symbol_full_name: 'misc.health.HealthCheckService',
          start_line: 3,
          end_line: 3,
        },
      ],
    };

    const html = renderToString(
      <MemoryRouter initialEntries={['/proto-docs?docs_view=sources']}>
        <GitSourcesWorkbenchPane
          readOnlyConsumer
          isAdmin
          initialGitRepos={[repo]}
          initialTree={tree}
          initialSelectedFileId={701}
          initialContent={content}
          initialVersions={[
            {
              id: 1,
              proto_file_id: 701,
              schema_id: 1,
              version: 1,
              version_tag: 'git-0b90fc3',
              commit_sha: '0b90fc3123456789',
              content_sha256: 'abc',
              source_type: 'GIT_SYNC',
              author_name: 'Sandbox Bot',
              summary: 'init',
              is_active: true,
              created_at: '2026-11-27T08:00:00Z',
            },
          ]}
        />
      </MemoryRouter>
    );

    expect(html).toContain('pgit-split-shell');
    expect(html).toContain('目录树 (clean/)');
    expect(html).toContain('按包名');
    expect(html).toContain('health_check.proto');
    expect(html).toContain('pgit-file-info-toggle');
    expect(html).toContain('git-sources-pipeline-govern-link');

    // 移除顶部仓库控制栏与冗余噪声
    expect(html).not.toContain('pgit-repo-bar');
    expect(html).not.toContain('PLATFORM_SHARED');
    expect(html).not.toContain('LOCAL_CACHE_ONLY');
    expect(html).not.toContain('Monorepo Bundles');
    expect(html).not.toContain('只读权威快照');
    expect(html).not.toContain('只读 · Git-Only SSOT');
    expect(html).not.toContain('pg-flatten-path-banner');
    expect(html).not.toContain('25 条/页');
    expect(html).not.toContain('锚点 #L1');
  });
});
