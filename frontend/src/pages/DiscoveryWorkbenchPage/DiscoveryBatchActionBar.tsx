import React from 'react';
import { Radio, ShieldCheck, X } from 'lucide-react';

export interface DiscoveryBatchActionBarProps {
  selectedCount: number;
  enrolling: boolean;
  quickRecording?: boolean;
  onClearSelection: () => void;
  onOpenEnrollModal: () => void;
  onQuickRecord?: () => void;
}

export const DiscoveryBatchActionBar: React.FC<DiscoveryBatchActionBarProps> = ({
  selectedCount,
  enrolling,
  quickRecording = false,
  onClearSelection,
  onOpenEnrollModal,
  onQuickRecord,
}) => {
  if (selectedCount <= 0) {
    return null;
  }

  return (
    <div className="discovery-batch-action-bar" data-testid="discovery-batch-action-bar">
      <div className="discovery-batch-action-bar__summary">
        <ShieldCheck size={16} />
        <span>
          已选中 <strong>{selectedCount}</strong> 个物理进程主轴，可零侵扰纳管（仅落库观测资产，保持 DISARMED 不修改探针布防）或直接启动录制
        </span>
      </div>
      <div className="discovery-batch-action-bar__actions">
        <button
          type="button"
          className="code-btn code-btn--secondary code-btn--sm"
          onClick={onClearSelection}
          disabled={enrolling || quickRecording}
        >
          <X size={13} />
          <span>取消选择</span>
        </button>
        <button
          type="button"
          className="code-btn code-btn--secondary code-btn--sm"
          data-testid="discovery-batch-enroll-btn"
          onClick={onOpenEnrollModal}
          disabled={enrolling || quickRecording}
        >
          <span>{enrolling ? '纳管提交中...' : '批量纳管（不布防）'}</span>
        </button>
        {onQuickRecord && (
          <button
            type="button"
            className="code-btn code-btn--primary code-btn--sm"
            data-testid="discovery-quick-record-btn"
            onClick={onQuickRecord}
            disabled={enrolling || quickRecording}
          >
            <Radio size={13} />
            <span>{quickRecording ? '直录启动中...' : '一键立即录制'}</span>
          </button>
        )}
      </div>
    </div>
  );
};
