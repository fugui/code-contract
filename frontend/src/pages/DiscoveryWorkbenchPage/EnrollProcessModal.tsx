import React from 'react';
import { Modal } from '@code/common';
import type { Agent, ArchProcessGroup, DiscoveryProcessGroupDTO } from '../../types';

export interface EnrollProcessModalProps {
  open: boolean;
  agent: Agent | null;
  selectedProcesses: DiscoveryProcessGroupDTO[];
  processGroups?: ArchProcessGroup[];
  targetGroupId?: string;
  onTargetGroupChange?: (groupId: string) => void;
  submitting: boolean;
  onClose: () => void;
  onConfirm: () => void;
}

export const EnrollProcessModal: React.FC<EnrollProcessModalProps> = ({
  open,
  agent,
  selectedProcesses,
  processGroups = [],
  targetGroupId = '',
  onTargetGroupChange,
  submitting,
  onClose,
  onConfirm,
}) => {
  const customGroups = processGroups.filter((g) => g.code !== 'PGRP_CSG_AUTO');

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="零侵扰物理纳管确认"
      width="md"
      footer={
        <div className="code-modal-footer">
          <button
            type="button"
            className="code-btn code-btn--secondary"
            onClick={onClose}
            disabled={submitting}
          >
            取消
          </button>
          <button
            type="button"
            className="code-btn code-btn--primary"
            data-testid="discovery-confirm-enroll-btn"
            disabled={submitting || selectedProcesses.length === 0}
            onClick={onConfirm}
          >
            {submitting ? '正在纳管...' : `确认纳管 (${selectedProcesses.length})`}
          </button>
        </div>
      }
    >
      <div className="discovery-modal-body" data-testid="enroll-process-modal">
        <div className="discovery-modal-notice">
          <div>
            <strong>零副作用承诺：</strong>新纳管进程挂载将固定写入{' '}
            <code className="discovery-mono">DISARMED</code> 态（
            <code className="discovery-mono">enroll_source = DISCOVERY</code>
            ），绝不修改 Agent{' '}
            <code className="discovery-mono">ExpectedArmedDigest</code>，也不下发任何探针挂载命令。
          </div>
          {agent?.scope === 'PRIVATE' && (
            <div className="discovery-modal-subnotice">
              当前环境为 <strong>PRIVATE</strong> 私有域，新发现标准进程将创建为{' '}
              <code className="discovery-mono">PROVISIONAL</code> 试用态，仅您本人与管理员可见。
            </div>
          )}
        </div>

        <div className="discovery-enroll-group-field">
          <label
            htmlFor="enroll-target-group-select"
            className="discovery-enroll-group-field__label"
          >
            新发现进程归属组：
          </label>
          <select
            id="enroll-target-group-select"
            className="code-select"
            value={targetGroupId}
            onChange={(e) => onTargetGroupChange?.(e.target.value)}
            disabled={submitting}
            data-testid="enroll-target-group-select"
          >
            <option value="">现网发现自动纳管进程组（默认暂存）</option>
            {customGroups.map((g) => (
              <option key={g.id} value={String(g.id)}>
                {g.name} ({g.code})
              </option>
            ))}
          </select>
          <span className="discovery-enroll-group-field__help">
            仅对本次新建的标准架构进程生效；已匹配既有进程的实例保持原所属组。
          </span>
        </div>

        <div className="discovery-enroll-preview-list">
          {selectedProcesses.map((proc) => (
            <div key={proc.process_key} className="discovery-enroll-preview-item">
              <div className="discovery-enroll-preview-item__main">
                <span className="discovery-proc-name">{proc.proc_name}</span>
                <span className="discovery-pill discovery-pill--neutral discovery-mono">
                  {proc.pids.length > 0 ? `PID ${proc.pids.join(', ')}` : 'PID -'}
                </span>
              </div>
              <div className="discovery-enroll-preview-item__meta discovery-mono">
                提供 {proc.services.length} 个服务 · 订阅 {proc.subscriptions.length} 条下游
              </div>
            </div>
          ))}
        </div>
      </div>
    </Modal>
  );
};
