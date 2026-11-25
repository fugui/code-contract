import React from 'react';
import { Modal } from '@code/common';
import type { Agent, DiscoveryProcessGroupDTO } from '../../types';

export interface EnrollProcessModalProps {
  open: boolean;
  agent: Agent | null;
  selectedProcesses: DiscoveryProcessGroupDTO[];
  submitting: boolean;
  onClose: () => void;
  onConfirm: () => void;
}

export const EnrollProcessModal: React.FC<EnrollProcessModalProps> = ({
  open,
  agent,
  selectedProcesses,
  submitting,
  onClose,
  onConfirm,
}) => {
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
