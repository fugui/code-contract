import React from 'react';
import type { RecordingSession } from '../../types';

export interface DebugSessionContextBarProps {
  currentSessionId: string;
  sessions: RecordingSession[];
  onSelectSession: (id: string) => void;
  liveStreamActive: boolean;
  onToggleLiveStream: () => void;
  isPaused: boolean;
  onTogglePause: () => void;
  viewMode?: 'tree' | 'flat';
  onChangeViewMode?: (mode: 'tree' | 'flat') => void;
  onToggleExpandAll?: (expand: boolean) => void;
  hideNoise?: boolean;
  hiddenNoiseCount?: number;
  onToggleHideNoise?: () => void;
}

export const DebugSessionContextBar: React.FC<DebugSessionContextBarProps> = ({
  currentSessionId,
  sessions,
  onSelectSession,
  liveStreamActive,
  onToggleLiveStream,
  isPaused,
  onTogglePause,
  viewMode = 'tree',
  onChangeViewMode,
  onToggleExpandAll,
  hideNoise = true,
  hiddenNoiseCount = 0,
  onToggleHideNoise,
}) => {
  const currentSession = sessions.find((s) => String(s.id) === String(currentSessionId));
  const isRecording = currentSession?.status === 'RECORDING';

  return (
    <div className="code-session-bar-wrap" data-testid="debug-session-context-bar">
      <div className="code-session-bar">
        <div className="code-session-bar__left">
          <label className="code-field-label">调试目标批次:</label>
          <select
            className="code-select code-select--compact"
            value={currentSessionId}
            onChange={(e) => onSelectSession(e.target.value)}
            data-testid="select-debug-session"
          >
            {sessions.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name} ({s.session_no}) [{s.scope}]
              </option>
            ))}
            {sessions.length === 0 && <option value="">暂无可选批次</option>}
          </select>

          {currentSession && (
            <div className="code-session-meta-chips">
              <span
                className={`code-status-tag code-status-tag--${currentSession.status.toLowerCase()}`}
              >
                {currentSession.status}
              </span>
              <span className="code-badge code-badge--secondary">{currentSession.scope}</span>
              <span className="code-font-mono code-text-sm">
                总帧数: {currentSession.captured_frames}
              </span>
              {currentSession.filtered_noise_frames !== undefined && currentSession.filtered_noise_frames > 0 && (
                <span
                  className="code-badge code-badge--secondary"
                  data-testid="session-filtered-noise-badge"
                  title="录制期 Agent 源头丢弃的低关注/噪音帧数"
                >
                  {`🔇 源头已过滤 ${currentSession.filtered_noise_frames} 帧`}
                </span>
              )}
              {isRecording && (
                <span className="code-recording-pulse-tag" data-testid="recording-pulse-badge">
                  <span className="code-pulse-dot" />
                  <span className="code-pulse-text">录制中实时流</span>
                </span>
              )}
            </div>
          )}
        </div>

        <div className="code-session-bar__actions" style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
          {/* 视图模式分段切换 */}
          {onChangeViewMode && (
            <div
              className="code-view-segmented-switch"
              style={{
                display: 'inline-flex',
                borderRadius: '6px',
                border: '1px solid var(--color-border, #cbd5e1)',
                padding: '2px',
                backgroundColor: 'var(--color-bg-secondary, #f1f5f9)',
              }}
            >
              <button
                type="button"
                data-testid="switch-view-tree"
                onClick={() => onChangeViewMode('tree')}
                style={{
                  padding: '3px 10px',
                  fontSize: '12px',
                  borderRadius: '4px',
                  border: 'none',
                  cursor: 'pointer',
                  backgroundColor: viewMode === 'tree' ? 'var(--color-bg-primary, #ffffff)' : 'transparent',
                  color: viewMode === 'tree' ? 'var(--color-primary, #3b82f6)' : 'var(--color-text-secondary, #64748b)',
                  fontWeight: viewMode === 'tree' ? 600 : 400,
                  boxShadow: viewMode === 'tree' ? '0 1px 2px rgba(0,0,0,0.05)' : 'none',
                }}
              >
                🌳 事务调用树
              </button>
              <button
                type="button"
                data-testid="switch-view-flat"
                onClick={() => onChangeViewMode('flat')}
                style={{
                  padding: '3px 10px',
                  fontSize: '12px',
                  borderRadius: '4px',
                  border: 'none',
                  cursor: 'pointer',
                  backgroundColor: viewMode === 'flat' ? 'var(--color-bg-primary, #ffffff)' : 'transparent',
                  color: viewMode === 'flat' ? 'var(--color-primary, #3b82f6)' : 'var(--color-text-secondary, #64748b)',
                  fontWeight: viewMode === 'flat' ? 600 : 400,
                  boxShadow: viewMode === 'flat' ? '0 1px 2px rgba(0,0,0,0.05)' : 'none',
                }}
              >
                📋 原始时序平铺
              </button>
            </div>
          )}

          {/* 树视图下的展开/折叠全部 */}
          {viewMode === 'tree' && onToggleExpandAll && (
            <div style={{ display: 'inline-flex', gap: '4px' }}>
              <button
                type="button"
                className="code-btn code-btn--sm code-btn--secondary"
                onClick={() => onToggleExpandAll(true)}
                title="展开所有树子节点"
              >
                全部展开
              </button>
              <button
                type="button"
                className="code-btn code-btn--sm code-btn--secondary"
                onClick={() => onToggleExpandAll(false)}
                title="折叠所有树子节点"
              >
                全部折叠
              </button>
            </div>
          )}

          {onToggleHideNoise && (
            <button
              type="button"
              className={`code-btn code-btn--sm ${hideNoise ? 'code-btn--primary' : 'code-btn--secondary'}`}
              onClick={onToggleHideNoise}
              data-testid="btn-toggle-hide-noise"
              title={hideNoise ? '点击显示被隐藏的噪音/基础设施调用' : '点击隐藏低关注/噪音服务调用'}
            >
              {hideNoise
                ? `🔇 隐藏噪音${hiddenNoiseCount > 0 ? ` (已隐 ${hiddenNoiseCount})` : ''}`
                : '👁 显示全部 (含噪音)'}
            </button>
          )}

          <button
            type="button"
            className={`code-btn code-btn--sm ${liveStreamActive ? 'code-btn--primary' : 'code-btn--secondary'}`}
            onClick={onToggleLiveStream}
            data-testid="btn-toggle-live-stream"
          >
            {liveStreamActive ? '● 正在跟流 (SSE)' : '开启实时跟流'}
          </button>

          {liveStreamActive && (
            <button
              type="button"
              className={`code-btn code-btn--sm ${isPaused ? 'code-btn--warning' : 'code-btn--secondary'}`}
              onClick={onTogglePause}
              data-testid="btn-toggle-pause"
              title={isPaused ? '恢复流式刷新' : '暂停流式刷新以固定屏幕'}
            >
              {isPaused ? '▶ 恢复推送' : '⏸ 暂停固定'}
            </button>
          )}
        </div>
      </div>
    </div>
  );
};

export default DebugSessionContextBar;
