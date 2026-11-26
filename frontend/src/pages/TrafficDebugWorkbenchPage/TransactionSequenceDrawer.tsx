import React, { useState, useEffect, useRef } from 'react';
import { Drawer } from '@code/common';
import type {
  TransactionSequenceDTO,
  SequenceStepDTO,
  PacketAnatomyDrawerDTO,
} from '../../types';
import { getTransactionSequence, getTrafficRecordAnatomy, redecodeTrafficRecord } from '../../api/traffic';
import { SequenceSwimlaneCanvas } from './SequenceSwimlaneCanvas';
import { PacketAnatomyContent } from '../../components/PacketAnatomyDrawer/PacketAnatomyDrawer';
import './TrafficDebugWorkbenchPage.css';

export interface TransactionSequenceDrawerProps {
  open: boolean;
  onClose: () => void;
  sessionId: string;
  rootRecordId?: string;
  replayTaskId?: string;
}

export const TransactionSequenceDrawer: React.FC<TransactionSequenceDrawerProps> = ({
  open,
  onClose,
  sessionId,
  rootRecordId,
  replayTaskId,
}) => {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sequence, setSequence] = useState<TransactionSequenceDTO | null>(null);
  const [selectedStep, setSelectedStep] = useState<SequenceStepDTO | null>(null);

  // 右侧报文解剖面板默认收起，按需展开且支持拖拽调整宽度
  const [anatomyOpen, setAnatomyOpen] = useState(false);
  const [anatomyWidth, setAnatomyWidth] = useState(380);
  const [isResizing, setIsResizing] = useState(false);
  const bodyRef = useRef<HTMLDivElement | null>(null);

  // 跨跳缝合视角切换 (EGRESS 客户端视角 vs INGRESS 服务端视角)
  const [stitchPerspective, setStitchPerspective] = useState<'EGRESS' | 'INGRESS'>('EGRESS');

  // 右侧报文解剖状态
  const [anatomyLoading, setAnatomyLoading] = useState(false);
  const [anatomyData, setAnatomyData] = useState<PacketAnatomyDrawerDTO | null>(null);

  useEffect(() => {
    setAnatomyOpen(false);
    setSelectedStep(null);
    setAnatomyData(null);

    if (!open || !sessionId || !rootRecordId) {
      setSequence(null);
      return;
    }

    let isMounted = true;
    setLoading(true);
    setError(null);

    getTransactionSequence(sessionId, rootRecordId, replayTaskId)
      .then((data) => {
        if (!isMounted) return;
        setSequence(data);
        setStitchPerspective('EGRESS');
      })
      .catch((err) => {
        if (!isMounted) return;
        setError(err.message || '加载时序泳道失败');
      })
      .finally(() => {
        if (isMounted) setLoading(false);
      });

    return () => {
      isMounted = false;
    };
  }, [open, sessionId, rootRecordId, replayTaskId]);

  // 仅在解剖面板展开且已选步骤时加载四段解剖
  useEffect(() => {
    if (!anatomyOpen || !selectedStep) {
      setAnatomyData(null);
      return;
    }

    let targetRecordId = selectedStep.record.id;
    if (stitchPerspective === 'INGRESS' && selectedStep.stitched_ingress_record) {
      targetRecordId = selectedStep.stitched_ingress_record.id;
    }

    let isMounted = true;
    setAnatomyLoading(true);

    getTrafficRecordAnatomy(targetRecordId, sessionId)
      .then((dto) => {
        if (!isMounted) return;
        setAnatomyData(dto);
      })
      .catch(() => {
        if (!isMounted) return;
        setAnatomyData(null);
      })
      .finally(() => {
        if (isMounted) setAnatomyLoading(false);
      });

    return () => {
      isMounted = false;
    };
  }, [anatomyOpen, selectedStep, stitchPerspective, sessionId]);

  // 拖拽调整右侧报文解剖面板宽度
  useEffect(() => {
    if (!isResizing) return;

    const handleMouseMove = (e: MouseEvent) => {
      if (!bodyRef.current) return;
      const rect = bodyRef.current.getBoundingClientRect();
      const rawWidth = rect.right - e.clientX;
      const maxByContainer = Math.max(320, rect.width - 420);
      const clamped = Math.min(680, maxByContainer, Math.max(320, rawWidth));
      setAnatomyWidth(clamped);
    };

    const handleMouseUp = () => {
      setIsResizing(false);
    };

    window.addEventListener('mousemove', handleMouseMove);
    window.addEventListener('mouseup', handleMouseUp);
    return () => {
      window.removeEventListener('mousemove', handleMouseMove);
      window.removeEventListener('mouseup', handleMouseUp);
    };
  }, [isResizing]);

  const handleSelectStep = (step: SequenceStepDTO) => {
    setSelectedStep(step);
    setStitchPerspective('EGRESS');
    setAnatomyOpen(true);
  };

  const handleToggleAnatomy = () => {
    if (anatomyOpen) {
      setAnatomyOpen(false);
      return;
    }
    if (!selectedStep && sequence?.steps && sequence.steps.length > 0) {
      setSelectedStep(sequence.steps[0]);
      setStitchPerspective('EGRESS');
    }
    setAnatomyOpen(true);
  };

  const handleRedecode = async (recordId: string) => {
    const updated = await redecodeTrafficRecord(recordId);
    setAnatomyData(updated);
  };

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title={`全链路时序泳道与报文因果透视 (根事务 #${rootRecordId || ''})`}
      width="min(1440px, 95vw)"
      bodyStyle={{ padding: 0, overflow: 'hidden' }}
      extra={
        replayTaskId && (
          <span className="code-sequence-replay-badge">
            叠加重放任务 #{replayTaskId}
          </span>
        )
      }
    >
      {loading && (
        <div className="code-sequence-state-box">
          正在加载全链路时序图与参数血缘...
        </div>
      )}

      {error && (
        <div className="code-sequence-state-box code-sequence-state-box--error">
          加载失败: {error}
        </div>
      )}

      {!loading && !error && sequence && (
        <div
          ref={bodyRef}
          className={`code-sequence-drawer-body ${isResizing ? 'is-resizing' : ''}`}
        >
          {/* 左栏：SVG 时序泳道画布（默认占满全宽，展开解剖后自适应剩余空间） */}
          <div className="code-sequence-drawer-left">
            <SequenceSwimlaneCanvas
              sequence={sequence}
              selectedStepIndex={selectedStep?.step_index}
              onSelectStep={handleSelectStep}
              toolbarExtra={
                <button
                  type="button"
                  className="code-btn code-btn--secondary code-btn--sm"
                  onClick={handleToggleAnatomy}
                  data-testid="toggle-anatomy-panel"
                >
                  {anatomyOpen ? '收起报文解剖' : '展开报文解剖'}
                </button>
              }
            />
          </div>

          {/* 展开态：拖拽分隔条 + 右栏紧凑报文解剖 */}
          {anatomyOpen && (
            <>
              <div
                className={`code-sequence-resizer ${isResizing ? 'is-dragging' : ''}`}
                onMouseDown={(e) => {
                  e.preventDefault();
                  setIsResizing(true);
                }}
                role="separator"
                aria-orientation="vertical"
                title="拖拽调整报文解剖面板宽度"
              />
              <div
                className="code-sequence-drawer-right"
                style={{ width: `${anatomyWidth}px` }}
                data-testid="sequence-anatomy-panel"
              >
                <div className="code-sequence-anatomy-header">
                  <span className="code-sequence-anatomy-title">
                    {selectedStep
                      ? `报文解剖 · #${selectedStep.step_index} ${selectedStep.record.operation}`
                      : '报文解剖'}
                  </span>
                  <button
                    type="button"
                    className="code-btn code-btn--secondary code-btn--xs"
                    onClick={() => setAnatomyOpen(false)}
                    data-testid="collapse-anatomy-panel"
                  >
                    收起
                  </button>
                </div>

                <div className="code-sequence-anatomy-scroll code-sequence-anatomy-compact">
                  {selectedStep ? (
                    <div>
                      {/* 跨跳缝合时的视角切换器 */}
                      {selectedStep.stitched_ingress_record && (
                        <div className="code-sequence-stitch-bar">
                          <span className="code-sequence-stitch-label">🔗 跨进程缝合视角:</span>
                          <button
                            type="button"
                            onClick={() => setStitchPerspective('EGRESS')}
                            className={`code-sequence-stitch-btn ${
                              stitchPerspective === 'EGRESS' ? 'code-sequence-stitch-btn--active' : ''
                            }`}
                          >
                            客户端 (EGRESS)
                          </button>
                          <button
                            type="button"
                            onClick={() => setStitchPerspective('INGRESS')}
                            className={`code-sequence-stitch-btn ${
                              stitchPerspective === 'INGRESS' ? 'code-sequence-stitch-btn--active' : ''
                            }`}
                          >
                            服务端 (INGRESS)
                          </button>
                          <span className="code-sequence-stitch-hop">
                            网络跳耗时 ~{selectedStep.duration_us}µs
                          </span>
                        </div>
                      )}

                      {anatomyLoading ? (
                        <div className="code-sequence-state-box">
                          正在解析当前交互步骤的报文四段解剖...
                        </div>
                      ) : anatomyData ? (
                        <PacketAnatomyContent
                          data={anatomyData}
                          onRedecode={handleRedecode}
                        />
                      ) : (
                        <div className="code-sequence-state-box">
                          暂无该步骤的报文解剖数据
                        </div>
                      )}
                    </div>
                  ) : (
                    <div className="code-sequence-state-box">
                      请在左侧时序泳道图中点击任意交互步骤查看报文解剖。
                    </div>
                  )}
                </div>
              </div>
            </>
          )}
        </div>
      )}
    </Drawer>
  );
};
