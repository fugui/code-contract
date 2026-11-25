import React, { useEffect, useState } from 'react';
import { Modal } from '@code/common';
import type {
  DiscoveryProcessGroupDTO,
  EnrollValidationErrorPayload,
} from '../../types';

export interface IdentityDisambiguationModalProps {
  payload: EnrollValidationErrorPayload | null;
  processGroup?: DiscoveryProcessGroupDTO | null;
  submitting: boolean;
  onClose: () => void;
  onResolveAmbiguous: (processKey: string, archProcessId: number) => void;
  onConfirmLowConfidence: (processKey: string) => void;
}

export const IdentityDisambiguationModal: React.FC<IdentityDisambiguationModalProps> = ({
  payload,
  processGroup,
  submitting,
  onClose,
  onResolveAmbiguous,
  onConfirmLowConfidence,
}) => {
  const [selectedCandidateId, setSelectedCandidateId] = useState<number>(0);
  const candidates = payload?.candidates ?? processGroup?.candidates ?? [];

  useEffect(() => {
    if (candidates.length > 0) {
      setSelectedCandidateId(candidates[0].arch_process_id);
    } else {
      setSelectedCandidateId(0);
    }
  }, [candidates, payload?.process_key]);

  if (!payload) {
    return null;
  }

  const isLowConfidence = payload.code === 'LOW_CONFIDENCE_CONFIRM_REQUIRED';
  const targetKey = payload.process_key || processGroup?.process_key || '';

  return (
    <Modal
      open={Boolean(payload)}
      onClose={onClose}
      title={isLowConfidence ? '低置信进程纳管确认' : '身份消歧确认'}
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
          {isLowConfidence ? (
            <button
              type="button"
              className="code-btn code-btn--primary"
              data-testid="discovery-confirm-low-conf-btn"
              disabled={submitting || !targetKey}
              onClick={() => onConfirmLowConfidence(targetKey)}
            >
              {submitting ? '提交中...' : '确认按进程名称纳管'}
            </button>
          ) : (
            <button
              type="button"
              className="code-btn code-btn--primary"
              data-testid="discovery-confirm-disambig-btn"
              disabled={submitting || !targetKey || selectedCandidateId <= 0}
              onClick={() => onResolveAmbiguous(targetKey, selectedCandidateId)}
            >
              {submitting ? '提交中...' : '绑定所选标准进程并重试'}
            </button>
          )}
        </div>
      }
    >
      <div className="discovery-modal-body" data-testid="identity-disambiguation-modal">
        <div className="discovery-modal-notice">
          <strong>{payload.error}</strong>
          {processGroup && (
            <div className="discovery-modal-subnotice discovery-mono">
              目标进程: {processGroup.proc_name} ({processGroup.exe_name || targetKey})
            </div>
          )}
        </div>

        {!isLowConfidence && candidates.length > 0 && (
          <div className="discovery-candidate-list" role="radiogroup" aria-label="标准进程候选列表">
            {candidates.map((cand) => (
              <label key={cand.arch_process_id} className="discovery-candidate-item">
                <input
                  type="radio"
                  name="disambig-candidate"
                  checked={selectedCandidateId === cand.arch_process_id}
                  onChange={() => setSelectedCandidateId(cand.arch_process_id)}
                />
                <div className="discovery-candidate-item__info">
                  <div className="discovery-candidate-item__title">
                    <strong>{cand.name}</strong>
                    <span className="discovery-pill discovery-pill--neutral discovery-mono">
                      {cand.code}
                    </span>
                    <span className="discovery-pill discovery-pill--info">
                      {cand.lifecycle}
                    </span>
                  </div>
                  <div className="discovery-candidate-item__tier discovery-mono">
                    命中层级: {cand.match_tier} · ArchProcess #{cand.arch_process_id}
                  </div>
                </div>
              </label>
            ))}
          </div>
        )}

        {isLowConfidence && (
          <div className="discovery-modal-subnotice">
            该进程未能读取到完整 <code className="discovery-mono">/proc/&lt;pid&gt;/cmdline</code>
            ，仅能依据剥离 PID 后缀后的进程名执行低置信指纹纳管。确认无误后将创建或匹配对应标准进程。
          </div>
        )}
      </div>
    </Modal>
  );
};
