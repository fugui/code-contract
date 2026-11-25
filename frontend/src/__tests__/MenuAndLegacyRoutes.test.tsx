import { describe, it, expect } from 'vitest';
import { renderToString } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { contractMenuConfig, DEFAULT_ENTRY_PATH, DEFAULT_STANDALONE_PATH } from '../menu';
import { EmbeddedRoutes, SchemasRouteWrapper } from '../App';

function getRedirectTarget(html: string): string | null {
  const match = html.match(/data-testid="legacy-redirect"[^>]*data-target="([^"]+)"/);
  return match ? match[1].replace(/&amp;/g, '&') : null;
}

export function testMenuStructure_FiveGroupsFourteenItems_DiscoveryFirstInArchTwin(): void {
  // 1. 严格包含 全局概览、架构数字孪生、Proto 契约治理、契约核验与回归、环境与治理 5 个分组
  expect(contractMenuConfig.groups.length).toBe(5);
  expect(contractMenuConfig.groups[0].title).toBe('全局概览');
  expect(contractMenuConfig.groups[1].title).toBe('架构数字孪生');
  expect(contractMenuConfig.groups[2].title).toBe('Proto 契约治理');
  expect(contractMenuConfig.groups[3].title).toBe('契约核验与回归');
  expect(contractMenuConfig.groups[4].title).toBe('环境与治理');

  const allItems = contractMenuConfig.groups.flatMap((g) => g.items);
  // 严格共 14 个子项（ADR-24 在架构数字孪生首位新增 /discovery 现网发现）
  expect(allItems.length).toBe(14);

  const group0Paths = contractMenuConfig.groups[0].items.map((i) => i.path);
  expect(group0Paths).toEqual(['/dashboard']);
  expect(contractMenuConfig.groups[0].items[0].label).toBe('治理大盘');

  const group1Paths = contractMenuConfig.groups[1].items.map((i) => i.path);
  expect(group1Paths).toEqual(['/discovery', '/topology', '/architecture']);
  expect(contractMenuConfig.groups[1].items[0].label).toBe('现网发现');
  expect(contractMenuConfig.groups[1].items[1].label).toBe('架构拓扑');
  expect(contractMenuConfig.groups[1].items[2].label).toBe('架构元素');

  const group2Paths = contractMenuConfig.groups[2].items.map((i) => i.path);
  expect(group2Paths).toEqual(['/proto-docs', '/proto-sources', '/proto-governance', '/schemas']);
  expect(contractMenuConfig.groups[2].items[0].label).toBe('接口参考文档');
  expect(contractMenuConfig.groups[2].items[1].label).toBe('源码与 Git 仓');
  expect(contractMenuConfig.groups[2].items[2].label).toBe('MR 门禁与规范');
  expect(contractMenuConfig.groups[2].items[3].label).toBe('描述集与依赖');

  const group3Paths = contractMenuConfig.groups[3].items.map((i) => i.path);
  expect(group3Paths).toEqual(['/record', '/debug', '/replay', '/golden-suites']);
  expect(contractMenuConfig.groups[3].items[0].label).toBe('契约采集');
  expect(contractMenuConfig.groups[3].items[1].label).toBe('事务分析');
  expect(contractMenuConfig.groups[3].items[2].label).toBe('防劣化回归');
  expect(contractMenuConfig.groups[3].items[3].label).toBe('黄金用例库');

  const group4Paths = contractMenuConfig.groups[4].items.map((i) => i.path);
  expect(group4Paths).toEqual(['/agents', '/admin/settings']);
  expect(contractMenuConfig.groups[4].items[0].label).toBe('Agent 与进程');
  expect(contractMenuConfig.groups[4].items[1].label).toBe('模块参数配置');

  // 2. 模块根默认路径 path === '/contract/dashboard'
  expect(DEFAULT_ENTRY_PATH).toBe('/contract/dashboard');
  expect(contractMenuConfig.defaultPath).toBe('/contract/dashboard');
  expect(DEFAULT_STANDALONE_PATH).toBe('/dashboard');

  // 仅 /admin/settings (或 /contract/admin/settings) 标记 adminOnly: true
  allItems.forEach((item) => {
    if (item.path === '/admin/settings' || item.path === '/contract/admin/settings') {
      expect(item.adminOnly).toBe(true);
    } else {
      expect(Boolean(item.adminOnly)).toBe(false);
    }
  });
}

export function testLegacyDeepLinks_LosslessRedirect(): void {
  // 1. /traffic?session_id=s1&trace_id=t9 自动重定向至 /debug?session_id=s1&trace_id=t9（参数零丢失）
  {
    const html = renderToString(
      <MemoryRouter initialEntries={['/traffic?session_id=s1&trace_id=t9']}>
        <EmbeddedRoutes />
      </MemoryRouter>
    );
    expect(getRedirectTarget(html)).toBe('/contract/debug?session_id=s1&trace_id=t9');
  }

  // 2. /architecture?tab=physical&session_id=s1 重定向至 /topology?view=PHYSICAL&session_id=s1
  {
    const html = renderToString(
      <MemoryRouter initialEntries={['/architecture?tab=physical&session_id=s1']}>
        <EmbeddedRoutes />
      </MemoryRouter>
    );
    expect(getRedirectTarget(html)).toBe('/contract/topology?session_id=s1&view=PHYSICAL');
  }

  // 3. / 自动落地至 /dashboard (嵌入模式下为 /contract/dashboard)
  {
    const html = renderToString(
      <MemoryRouter initialEntries={['/']}>
        <EmbeddedRoutes />
      </MemoryRouter>
    );
    expect(getRedirectTarget(html)).toBe('/contract/dashboard');
  }

  // 4. /sessions?page=1 自动落地至 /record (保留 query 参数)
  {
    const html = renderToString(
      <MemoryRouter initialEntries={['/sessions?page=1']}>
        <EmbeddedRoutes />
      </MemoryRouter>
    );
    expect(getRedirectTarget(html)).toBe('/contract/record?page=1');
  }

  // 5. ADR-19: /schemas?tab=docs|catalog|sources|mr-gates|lint-* 无损重映射至独立工作台路由（含 Hash）
  const schemaRedirectCases: Array<{ from: string; expectedEmbeddedTarget: string }> = [
    {
      from: '/schemas?tab=docs&service_name=demo.billing.v1.BillingService',
      expectedEmbeddedTarget: '/contract/proto-docs?service_name=demo.billing.v1.BillingService&docs_view=reference',
    },
    {
      from: '/schemas?tab=catalog&claimed_status=UNCLAIMED',
      expectedEmbeddedTarget: '/contract/proto-docs?claimed_status=UNCLAIMED&docs_view=catalog-ops',
    },
    {
      from: '/schemas?tab=sources&file_id=501#L42',
      expectedEmbeddedTarget: '/contract/proto-sources?file_id=501#L42',
    },
    {
      from: '/schemas?tab=mr-gates&runId=102',
      expectedEmbeddedTarget: '/contract/proto-governance?runId=102&gov_tab=mr-gates',
    },
    {
      from: '/schemas?tab=lint-rules',
      expectedEmbeddedTarget: '/contract/proto-governance?gov_tab=rules',
    },
    {
      from: '/schemas?tab=lint-vocab',
      expectedEmbeddedTarget: '/contract/proto-governance?gov_tab=vocab',
    },
    {
      from: '/schemas?tab=lint-report',
      expectedEmbeddedTarget: '/contract/proto-governance?gov_tab=report',
    },
  ];

  for (const testCase of schemaRedirectCases) {
    const html = renderToString(
      <MemoryRouter initialEntries={[testCase.from]}>
        <EmbeddedRoutes />
      </MemoryRouter>
    );
    expect(getRedirectTarget(html)).toBe(testCase.expectedEmbeddedTarget);
  }

  // 6. Standalone 模式下的 SchemasRouteWrapper 同样保持无损跳转
  {
    const html = renderToString(
      <MemoryRouter initialEntries={['/schemas?tab=sources&file_id=88#L19']}>
        <SchemasRouteWrapper />
      </MemoryRouter>
    );
    expect(getRedirectTarget(html)).toBe('/proto-sources?file_id=88#L19');
  }

  // 7. /schemas（无 tab）、/schemas?tab=assets、/schemas?tab=graph 及 /schemas?active_only=true&search=Sync 留在本页渲染，不触发重定向
  for (const stayPath of ['/schemas', '/schemas?tab=assets', '/schemas?tab=graph', '/schemas?active_only=true&search=Sync']) {
    const html = renderToString(
      <MemoryRouter initialEntries={[stayPath]}>
        <EmbeddedRoutes />
      </MemoryRouter>
    );
    expect(getRedirectTarget(html)).toBeNull();
    expect(html).toContain('proto-schemas-workbench-page');
    expect(html).toContain('描述集资产台账');
    expect(html).toContain('跨包依赖关系图');
    const tabMatches = html.match(/role="tab"/g) ?? [];
    expect(tabMatches.length).toBe(2);
  }

  // 8. 独立路由 /proto-docs、/proto-sources、/proto-governance 正常直达，且不再渲染全局 .pb 上传按钮
  {
    const docsHtml = renderToString(
      <MemoryRouter initialEntries={['/proto-docs']}>
        <EmbeddedRoutes />
      </MemoryRouter>
    );
    expect(getRedirectTarget(docsHtml)).toBeNull();
    expect(docsHtml).toContain('proto-docs-workbench-page');
    expect(docsHtml).not.toContain('上传描述集 (.pb)');

    const sourcesHtml = renderToString(
      <MemoryRouter initialEntries={['/proto-sources']}>
        <EmbeddedRoutes />
      </MemoryRouter>
    );
    expect(getRedirectTarget(sourcesHtml)).toBeNull();
    expect(sourcesHtml).toContain('proto-sources-workbench-page');
    expect(sourcesHtml).not.toContain('上传描述集 (.pb)');

    const govHtml = renderToString(
      <MemoryRouter initialEntries={['/proto-governance']}>
        <EmbeddedRoutes />
      </MemoryRouter>
    );
    expect(getRedirectTarget(govHtml)).toBeNull();
    expect(govHtml).toContain('proto-governance-workbench-page');
    expect(govHtml).not.toContain('上传描述集 (.pb)');

    const discHtml = renderToString(
      <MemoryRouter initialEntries={['/discovery']}>
        <EmbeddedRoutes />
      </MemoryRouter>
    );
    expect(getRedirectTarget(discHtml)).toBeNull();
    expect(discHtml).toContain('discovery-workbench-page');
  }
}

describe('MenuAndLegacyRoutes Test Suite', () => {
  it('TC-MENU-01: 五组十四项菜单结构（含现网发现首位入口）与 /dashboard 默认落地路径校验', () => {
    testMenuStructure_FiveGroupsFourteenItems_DiscoveryFirstInArchTwin();
  });

  it('TC-MENU-02: 历史旧路由与 /schemas?tab=* 带参及 Hash 无损平滑重定向校验', () => {
    testLegacyDeepLinks_LosslessRedirect();
  });
});
