import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { BreakingAuditWorkbenchPane } from '../BreakingAuditWorkbenchPane';
import { ProtoGovernanceWorkbenchPage } from '../ProtoGovernanceWorkbenchPage';
import type { BreakingChangeEventDTO } from '../../../types';

vi.mock('@code/common', async () => {
  const actual = await vi.importActual<typeof import('@code/common')>('@code/common');
  return {
    ...actual,
    Drawer: ({
      open,
      title,
      children,
    }: {
      open: boolean;
      title?: React.ReactNode;
      children?: React.ReactNode;
    }) =>
      open ? (
        <div data-testid="mock-drawer">
          <h2>{title}</h2>
          {children}
        </div>
      ) : null,
    Modal: ({
      open,
      title,
      children,
    }: {
      open: boolean;
      title?: React.ReactNode;
      children?: React.ReactNode;
    }) =>
      open ? (
        <div data-testid="mock-modal">
          <h3>{title}</h3>
          {children}
        </div>
      ) : null,
  };
});

const openEvent: BreakingChangeEventDTO = {
  id: 11,
  source: 'PIPELINE_PUBLISH',
  publish_run_id: 88,
  bundle_id: 1,
  bundle_name: 'base-bundle',
  schema_name: 'schema-base',
  from_schema_id: 90,
  from_version: 2,
  to_schema_id: 99,
  to_version: 3,
  breaking_count: 1,
  affected_count: 1,
  breaking_rules: [
    {
      rule: 'TYPE_INCOMPATIBLE',
      proto_file: 'common/base.proto',
      symbol: 'BaseMeta.req_id',
      message: "字段 'req_id' 类型从 TYPE_STRING 变更为 TYPE_INT64",
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
  status: 'OPEN',
  triggered_by: 'pipe_admin',
  acknowledged_by_name: '',
  acknowledge_note: '',
  created_at: '2026-11-28T06:30:00Z',
  updated_at: '2026-11-28T06:30:00Z',
};

const ackedEvent: BreakingChangeEventDTO = {
  ...openEvent,
  id: 12,
  source: 'MANUAL_ACTIVATE',
  schema_name: 'account-schema',
  status: 'ACKNOWLEDGED',
  acknowledged_by: 1,
  acknowledged_by_name: 'admin',
  acknowledge_note: '已通知下游同步适配',
  acknowledged_at: '2026-11-28T06:45:00Z',
};

describe('BreakingAuditWorkbenchPane & ProtoGovernanceWorkbenchPage Suite', () => {
  it('renders breaking-audit tab inside ProtoGovernanceWorkbenchPage', () => {
    const html = renderToStaticMarkup(
      <MemoryRouter initialEntries={['/proto-governance?gov_tab=breaking-audit']}>
        <ProtoGovernanceWorkbenchPage
          initialTab="breaking-audit"
          initialBreakingEvents={[openEvent, ackedEvent]}
          initialBreakingTotal={2}
          initialBreakingOpenCount={1}
          initialBreakingDetail={openEvent}
        />
      </MemoryRouter>
    );

    expect(html).toContain('破坏性变更审计');
    expect(html).toContain('契约上线破坏性变更审计');
    expect(html).toContain('schema-base');
    expect(html).toContain('account-schema');
  });

  it('renders admin acknowledge controls, detail rules and affected dependents', () => {
    const html = renderToStaticMarkup(
      <MemoryRouter initialEntries={['/proto-governance?gov_tab=breaking-audit']}>
        <BreakingAuditWorkbenchPane
          isAdmin
          initialEvents={[openEvent, ackedEvent]}
          initialTotal={2}
          initialOpenCount={1}
          initialDetail={openEvent}
        />
      </MemoryRouter>
    );

    expect(html).toContain('v2 → v3');
    expect(html).toContain('流水线发布');
    expect(html).toContain('TYPE_INCOMPATIBLE');
    expect(html).toContain('订单核心模块');
    expect(html).toContain('MOD_ORDER');
    expect(html).toContain('sut-order');
    expect(html).toContain('ack-breaking-audit-11');
    expect(html).toContain('confirm-ack-breaking-audit-btn');
  });

  it('hides admin acknowledge actions when rendered for non-admin users', () => {
    const html = renderToStaticMarkup(
      <MemoryRouter initialEntries={['/proto-governance?gov_tab=breaking-audit']}>
        <BreakingAuditWorkbenchPane
          isAdmin={false}
          initialEvents={[openEvent]}
          initialTotal={1}
          initialOpenCount={1}
          initialDetail={openEvent}
        />
      </MemoryRouter>
    );

    expect(html).not.toContain('ack-breaking-audit-11');
    expect(html).not.toContain('confirm-ack-breaking-audit-btn');
  });
});
