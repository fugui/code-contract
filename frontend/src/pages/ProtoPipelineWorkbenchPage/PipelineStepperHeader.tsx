import React from 'react';
import { AlertTriangle, CheckCircle2, Lock, Play, RefreshCw } from 'lucide-react';
import type {
  PipelineLockHolderDTO,
  PipelineOverviewDTO,
  PipelineProgressEventPayload,
} from '../../types';

export type PipelineStageKey = 'collect' | 'normalize' | 'reconcile' | 'publish';

export interface PipelineStepperHeaderProps {
  activeStage: PipelineStageKey;
  onSelectStage: (stage: PipelineStageKey) => void;
  overview: PipelineOverviewDTO | null;
  lockHolder: PipelineLockHolderDTO | null;
  pipelineProgress: PipelineProgressEventPayload | null;
  isAdmin: boolean;
  running: boolean;
  fromStage: 'COLLECT_NORMALIZE' | 'RECONCILE' | 'PUBLISH';
  onChangeFromStage: (val: 'COLLECT_NORMALIZE' | 'RECONCILE' | 'PUBLISH') => void;
  onRunAll: () => void;
  onRefresh: () => void;
}

const STAGE_ITEMS: Array<{
  key: PipelineStageKey;
  num: string;
  title: string;
  desc: string;
}> = [
  {
    key: 'collect',
    num: '阶段 ①',
    title: '仓库拉取',
    desc: '全平台 Git 仓同步与增量采集',
  },
  {
    key: 'normalize',
    num: '阶段 ②',
    title: '清洗规范化',
    desc: '语法预处理、去 /pg 扁平与目录映射',
  },
  {
    key: 'reconcile',
    num: '阶段 ③',
    title: '全池依赖收敛',
    desc: '冲突仲裁、共享基座收割与自愈隔离',
  },
  {
    key: 'publish',
    num: '阶段 ④',
    title: '拓扑编译与发布',
    desc: '单仓编译、兼容性门禁与生效池切换',
  },
];

export const PipelineStepperHeader: React.FC<PipelineStepperHeaderProps> = ({
  activeStage,
  onSelectStage,
  overview,
  lockHolder,
  pipelineProgress,
  isAdmin,
  running,
  fromStage,
  onChangeFromStage,
  onRunAll,
  onRefresh,
}) => {
  const getStaleInfo = (key: PipelineStageKey): { stale: boolean; reasons: string[] } => {
    if (!overview) return { stale: false, reasons: [] };
    if (key === 'normalize') {
      return { stale: overview.normalize_stale, reasons: overview.normalize_stale_reasons || [] };
    }
    if (key === 'reconcile') {
      return { stale: overview.reconcile_stale, reasons: overview.reconcile_stale_reasons || [] };
    }
    if (key === 'publish') {
      return { stale: overview.publish_stale, reasons: overview.publish_stale_reasons || [] };
    }
    return { stale: false, reasons: [] };
  };

  const locked = Boolean(lockHolder);

  return (
    <div className="ppipe-header-card">
      <div className="ppipe-header-top">
        <div className="ppipe-title-wrap">
          <h2>四阶段一体化契约流水线</h2>
          <p className="ppipe-subtitle">
            从代码仓采集、规则清洗、全池依赖收敛到拓扑编译与发布生效的唯一收口工作台
          </p>
        </div>

        <div className="ppipe-actions-row">
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            onClick={onRefresh}
            disabled={running}
          >
            <RefreshCw size={14} /> 刷新状态
          </button>

          {isAdmin && (
            <>
              <select
                className="pgit-input"
                aria-label="流水线起始阶段"
                value={fromStage}
                onChange={(e) =>
                  onChangeFromStage(e.target.value as 'COLLECT_NORMALIZE' | 'RECONCILE' | 'PUBLISH')
                }
                disabled={running || locked}
              >
                <option value="COLLECT_NORMALIZE">从阶段 ① 开始（全流程）</option>
                <option value="RECONCILE">从阶段 ③ 开始（仅收敛与发布）</option>
                <option value="PUBLISH">仅阶段 ④（工作区拓扑发布）</option>
              </select>

              <button
                type="button"
                className="btn btn-primary"
                data-testid="run-full-pipeline-btn"
                onClick={onRunAll}
                disabled={running || locked}
              >
                <Play size={14} /> {running ? '流水线执行中...' : '一键执行流水线'}
              </button>
            </>
          )}
        </div>
      </div>

      {lockHolder && (
        <div className="ppipe-lock-banner" role="status" data-testid="pipeline-lock-banner">
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            <Lock size={15} />
            <span>
              全局流水线互斥锁占用中：<strong>{lockHolder.job_type}</strong>
              {lockHolder.job_id ? ` #${lockHolder.job_id}` : ''} · 触发人：
              <strong>{lockHolder.holder || 'system'}</strong>
            </span>
          </div>
          <span style={{ fontSize: '12px', color: 'var(--color-text-secondary)' }}>
            写操作已暂锁以防状态竞态
          </span>
        </div>
      )}

      {pipelineProgress && pipelineProgress.status === 'RUNNING' && (
        <div className="ppipe-progress-banner" role="status" data-testid="pipeline-progress-banner">
          <span>
            <strong>{pipelineProgress.current_stage}</strong> · {pipelineProgress.message}
          </span>
          <span style={{ fontSize: '12px', color: 'var(--color-text-secondary)' }}>
            运行批次 #{pipelineProgress.pipeline_run_id}
          </span>
        </div>
      )}

      <div className="ppipe-stepper" role="tablist" aria-label="契约流水线四阶段">
        {STAGE_ITEMS.map((item) => {
          const isActive = activeStage === item.key;
          const { stale, reasons } = getStaleInfo(item.key);
          return (
            <button
              key={item.key}
              type="button"
              role="tab"
              aria-selected={isActive}
              data-testid={`pipeline-step-${item.key}`}
              className={`ppipe-step-btn ${isActive ? 'ppipe-step-btn--active' : ''}`}
              onClick={() => onSelectStage(item.key)}
              title={reasons.join('；')}
            >
              <div className="ppipe-step-btn__top">
                <span className="ppipe-step-num">{item.num}</span>
                {stale ? (
                  <span className="ppipe-stale-pill" data-testid={`stale-badge-${item.key}`}>
                    <AlertTriangle size={11} /> 需重跑
                  </span>
                ) : (
                  <span className="ppipe-ok-pill">
                    <CheckCircle2 size={11} /> 已就绪
                  </span>
                )}
              </div>
              <div className="ppipe-step-title">{item.title}</div>
              <div className="ppipe-step-desc">{item.desc}</div>
            </button>
          );
        })}
      </div>
    </div>
  );
};
