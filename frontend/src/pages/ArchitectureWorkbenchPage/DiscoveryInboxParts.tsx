import React from 'react';
import { AlertTriangle, Check, GitBranch, Layers, Server, Split, Undo2, XCircle } from 'lucide-react';
import type {
  AttributionJobDTO,
  DependencyDecision,
  DepSuggestionDTO,
  InboxCountsDTO,
  InboxItemKind,
  InboxProcessGroupDTO,
  InboxServiceItemDTO,
  Module,
} from '../../types';
import { attributionPercent } from './inboxLogic';

export type InboxFilterKind = 'ALL' | InboxItemKind | 'DEP_SUGGESTION';

export const InboxCountBadge: React.FC<{ count: number }> = ({ count }) => (
  <span className="code-inbox-badge" data-testid="inbox-unread-badge">
    {count}
  </span>
);

export interface InboxSummaryBarProps {
  summary: InboxCountsDTO;
  activeKind: InboxFilterKind;
  onChange: (kind: InboxFilterKind) => void;
}

export const InboxSummaryBar: React.FC<InboxSummaryBarProps> = ({
  summary,
  activeKind,
  onChange,
}) => {
  const tabs: Array<{ key: InboxFilterKind; label: string; count: number }> = [
    { key: 'ALL', label: '全部待治理', count: summary.total_pending },
    { key: 'UNCLAIMED', label: '待认领单宿主', count: summary.unclaimed_count },
    { key: 'MULTI_HOST', label: '多宿主待裁决', count: summary.multi_host_count },
    { key: 'PLACEMENT_DRIFT', label: '放置漂移', count: summary.placement_drift_count },
    { key: 'DEP_SUGGESTION', label: '反向依赖建议', count: summary.dep_suggestion_count },
  ];

  return (
    <div className="code-inbox-summary" role="tablist" data-testid="inbox-summary-bar">
      {tabs.map((tab) => (
        <button
          key={tab.key}
          type="button"
          className={`code-inbox-filter-pill ${
            activeKind === tab.key ? 'code-inbox-filter-pill--active' : ''
          }`}
          data-testid={`inbox-kind-${tab.key}`}
          onClick={() => onChange(tab.key)}
        >
          <span>{tab.label}</span>
          <span className="code-inbox-filter-pill__count">{tab.count}</span>
        </button>
      ))}
    </div>
  );
};

export interface InboxProcessGroupListProps {
  groups: InboxProcessGroupDTO[];
  modules: Module[];
  selected: Set<number>;
  targets: Record<number, string>;
  isAdmin: boolean;
  busy: boolean;
  onToggle: (serviceId: number) => void;
  onTarget: (serviceId: number, moduleId: string) => void;
  onSplit: (item: InboxServiceItemDTO) => void;
  onUnclaim: (item: InboxServiceItemDTO) => void;
}

const KIND_LABELS: Record<InboxItemKind, string> = {
  UNCLAIMED: '待认领',
  MULTI_HOST: '多宿主',
  PLACEMENT_DRIFT: '放置漂移',
};

export const InboxProcessGroupList: React.FC<InboxProcessGroupListProps> = ({
  groups,
  modules,
  selected,
  targets,
  isAdmin,
  busy,
  onToggle,
  onTarget,
  onSplit,
  onUnclaim,
}) => {
  const bizModules = modules.filter((m) => (m.module_kind || 'BUSINESS') === 'BUSINESS');

  if (groups.length === 0) {
    return (
      <div className="code-inbox-empty" data-testid="inbox-groups-empty">
        暂无匹配的物理进程待治理项
      </div>
    );
  }

  return (
    <div className="code-inbox-groups" data-testid="inbox-process-groups">
      {groups.map((grp) => (
        <section
          key={grp.arch_process_id}
          className="code-inbox-group-card"
          data-testid={`inbox-group-${grp.arch_process_id}`}
        >
          <header className="code-inbox-group-card__header">
            <div className="code-inbox-group-card__title">
              <Server size={15} />
              <strong>{grp.arch_process_name}</strong>
              <code className="code-inbox-code">{grp.arch_process_code}</code>
              {grp.group_name && (
                <span className="code-inbox-muted">{grp.group_name}</span>
              )}
            </div>
            <span className="code-inbox-muted">{grp.items.length} 项服务</span>
          </header>

          <div className="code-inbox-group-card__body">
            {grp.items.map((item) => {
              const targetVal = targets[item.service_id] ?? '';
              const isChecked = selected.has(item.service_id);
              const candidateMods = bizModules.filter(
                (m) => !m.arch_process_id || Number(m.arch_process_id) === grp.arch_process_id
              );
              const selectList = candidateMods.length > 0 ? candidateMods : bizModules;

              return (
                <div
                  key={`${grp.arch_process_id}-${item.service_id}`}
                  className="code-inbox-item-row"
                  data-testid={`inbox-item-${item.service_id}`}
                >
                  <div className="code-inbox-item-row__main">
                    {item.kind === 'UNCLAIMED' ? (
                      <input
                        type="checkbox"
                        checked={isChecked}
                        disabled={busy}
                        onChange={() => onToggle(item.service_id)}
                        data-testid={`inbox-check-${item.service_id}`}
                        aria-label={`选择服务 ${item.service_name}`}
                      />
                    ) : (
                      <span className="code-inbox-item-row__spacer" />
                    )}

                    <div className="code-inbox-item-row__info">
                      <div className="code-inbox-item-row__top">
                        <span className="code-inbox-svc-name">{item.service_name}</span>
                        <span
                          className={`code-inbox-kind-tag code-inbox-kind-tag--${item.kind.toLowerCase()}`}
                        >
                          {KIND_LABELS[item.kind]}
                        </span>
                        {item.proto_service_name ? (
                          <span className="code-inbox-proto-tag">
                            .pb: {item.proto_service_name}
                          </span>
                        ) : item.proto_suggested_name ? (
                          <span className="code-inbox-proto-tag code-inbox-proto-tag--suggested">
                            建议: {item.proto_suggested_name}
                          </span>
                        ) : null}
                      </div>

                      <div className="code-inbox-item-row__meta">
                        {item.module_name && (
                          <span>
                            当前归属模块: <strong>{item.module_name}</strong>
                          </span>
                        )}
                        {item.placements.map((pl) => (
                          <span key={pl.placement_id} className="code-inbox-placement-chip">
                            {pl.agent_name} · {pl.observed_transport} · {pl.instance_count} 实例
                            {pl.endpoints.length > 0 ? ` (${pl.endpoints[0]})` : ''}
                          </span>
                        ))}
                      </div>
                    </div>
                  </div>

                  <div className="code-inbox-item-row__actions">
                    {item.kind === 'UNCLAIMED' && (
                      <select
                        className="code-input code-inbox-select"
                        value={targetVal}
                        disabled={busy}
                        onChange={(e) => onTarget(item.service_id, e.target.value)}
                        data-testid={`inbox-target-${item.service_id}`}
                        aria-label={`选择 ${item.service_name} 认领目标模块`}
                      >
                        <option value="">选择认领目标模块...</option>
                        {selectList.map((m) => (
                          <option key={String(m.id)} value={String(m.id)}>
                            {m.name} ({m.code})
                          </option>
                        ))}
                      </select>
                    )}

                    {item.kind === 'MULTI_HOST' && (
                      <button
                        type="button"
                        className="btn btn-secondary btn-sm"
                        disabled={busy || !isAdmin}
                        title={isAdmin ? '拆分为多实现契约' : '仅管理员可拆分多宿主服务'}
                        onClick={() => onSplit(item)}
                        data-testid={`inbox-split-${item.service_id}`}
                      >
                        <Split size={13} />
                        <span>拆分为多实现</span>
                      </button>
                    )}

                    {item.kind === 'PLACEMENT_DRIFT' && (
                      <>
                        <span className="code-inbox-drift-warn">
                          <AlertTriangle size={13} />
                          放置跨进程漂移
                        </span>
                        <button
                          type="button"
                          className="btn btn-secondary btn-sm"
                          disabled={busy}
                          onClick={() => onUnclaim(item)}
                          data-testid={`inbox-unclaim-${item.service_id}`}
                        >
                          <Undo2 size={13} />
                          <span>取消认领</span>
                        </button>
                      </>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        </section>
      ))}
    </div>
  );
};

export interface InboxDepSuggestionCardProps {
  suggestion: DepSuggestionDTO;
  selectedModuleId?: number;
  isAdmin: boolean;
  busy: boolean;
  onSelectModule: (moduleId: number | undefined) => void;
  onDecide: (decision: DependencyDecision) => void;
}

export const InboxDepSuggestionCard: React.FC<InboxDepSuggestionCardProps> = ({
  suggestion,
  selectedModuleId,
  isAdmin,
  busy,
  onSelectModule,
  onDecide,
}) => {
  const selectVal = selectedModuleId !== undefined ? String(selectedModuleId) : '';
  const suffix = `${suggestion.service_id}-${suggestion.consumer_arch_process_id}`;

  return (
    <div className="code-inbox-dep-card" data-testid={`inbox-dep-card-${suffix}`}>
      <div className="code-inbox-dep-card__info">
        <div className="code-inbox-dep-card__title">
          <GitBranch size={14} />
          <strong>{suggestion.consumer_arch_process_name}</strong>
          <code className="code-inbox-code">{suggestion.consumer_arch_process_code}</code>
          <span className="code-inbox-muted">→ 订阅服务</span>
          <span className="code-inbox-svc-name">{suggestion.service_name}</span>
          <span className="code-inbox-muted">(提供方: {suggestion.provider_module_name})</span>
        </div>

        <div className="code-inbox-dep-card__meta">
          <Layers size={13} />
          <span>消费方业务模块：</span>
          <select
            className="code-input code-inbox-select"
            value={selectVal}
            disabled={busy}
            onChange={(e) => {
              const parsed = Number(e.target.value);
              onSelectModule(Number.isFinite(parsed) && parsed > 0 ? parsed : undefined);
            }}
            data-testid={`inbox-dep-module-${suffix}`}
            aria-label="选择消费方业务模块"
          >
            <option value="">请选择消费方模块...</option>
            {suggestion.candidate_consumer_modules.map((c) => (
              <option key={c.module_id} value={String(c.module_id)}>
                {c.module_name} ({c.module_code})
              </option>
            ))}
          </select>
          {suggestion.recommendation_source === 'STAGE2_TRAFFIC' && (
            <span className="code-inbox-proto-tag">Stage 2 流量推荐</span>
          )}
          {suggestion.recommendation_source === 'SINGLE_MODULE' && (
            <span className="code-inbox-proto-tag">进程单模块</span>
          )}
        </div>
      </div>

      <div className="code-inbox-dep-card__actions">
        <button
          type="button"
          className="btn btn-primary btn-sm"
          disabled={busy || !isAdmin}
          onClick={() => onDecide('APPROVE')}
          data-testid={`inbox-dep-approve-${suffix}`}
        >
          <Check size={13} />
          <span>批准</span>
        </button>
        <button
          type="button"
          className="btn btn-secondary btn-sm"
          disabled={busy || !isAdmin}
          onClick={() => onDecide('IGNORE')}
          data-testid={`inbox-dep-ignore-${suffix}`}
        >
          <span>忽略</span>
        </button>
        <button
          type="button"
          className="btn btn-danger btn-sm"
          disabled={busy || !isAdmin}
          onClick={() => onDecide('VIOLATION')}
          data-testid={`inbox-dep-violation-${suffix}`}
        >
          <XCircle size={13} />
          <span>标记违规</span>
        </button>
      </div>
    </div>
  );
};

export interface AttributionProgressBarProps {
  job: AttributionJobDTO;
  onCancel: (jobId: number) => void;
}

export const AttributionProgressBar: React.FC<AttributionProgressBarProps> = ({
  job,
  onCancel,
}) => {
  const pct = attributionPercent(job);
  const active = job.status === 'PENDING' || job.status === 'RUNNING';

  return (
    <div className="code-inbox-job-banner" data-testid={`inbox-job-banner-${job.id}`}>
      <div className="code-inbox-job-banner__header">
        <div>
          <strong>后台历史归因回补 #{job.id}</strong>
          <span className="code-inbox-muted" style={{ marginLeft: 8 }}>
            状态: {job.status} · 进度 {job.processed}/{job.total} 会话 ({pct}%) · 命中 {job.matched_count} 条
          </span>
        </div>
        {active && (
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            onClick={() => onCancel(job.id)}
            data-testid={`inbox-job-cancel-${job.id}`}
          >
            取消任务
          </button>
        )}
      </div>
      <div
        className="code-inbox-progress-track"
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={pct}
      >
        <div
          className={`code-inbox-progress-fill code-inbox-progress-fill--${job.status.toLowerCase()}`}
          style={{ width: `${pct}%` }}
        />
      </div>
      {job.error && <div className="code-inbox-job-error">{job.error}</div>}
    </div>
  );
};
