import React from 'react';
import type { RpcLatencyWaterfallProps } from './types';

function formatLatencyLabel(us: number): string {
  if (us >= 1_000_000) {
    return `${us} μs (${(us / 1_000_000).toFixed(2)} s)`;
  }
  if (us >= 1000) {
    return `${us} μs (${(us / 1000).toFixed(2)} ms)`;
  }
  return `${us} μs`;
}

export const RpcLatencyWaterfallBar: React.FC<RpcLatencyWaterfallProps> = ({ data }) => {
  const timing =
    data.rspSide?.sessionHead.timingBreakdownUs ||
    data.reqSide?.sessionHead.timingBreakdownUs;

  const queueWaitFromExtend =
    data.reqSide?.extendHead.queueWaitUs ??
    data.rspSide?.extendHead.queueWaitUs;

  const hasClientToServer = timing?.clientToServerUs !== undefined;
  const hasNetworkAndFraming = timing?.networkAndFramingUs !== undefined;
  const transportLabel = hasClientToServer ? '客户端传输' : '网络与分帧';
  // Never sum clientToServerUs and networkAndFramingUs together to prevent double-counting.
  const transportUs = hasClientToServer
    ? Math.max(0, timing?.clientToServerUs ?? 0)
    : Math.max(0, timing?.networkAndFramingUs ?? 0);

  const queueWaitUs = Math.max(0, timing?.queueWaitUs ?? queueWaitFromExtend ?? 0);
  const serverExecUs = Math.max(0, timing?.serverExecUs ?? timing?.serverProcessUs ?? 0);

  const hasBreakdown =
    hasClientToServer ||
    hasNetworkAndFraming ||
    timing?.queueWaitUs !== undefined ||
    queueWaitFromExtend !== undefined ||
    timing?.serverExecUs !== undefined ||
    timing?.serverProcessUs !== undefined;

  const stageSumUs = transportUs + queueWaitUs + serverExecUs;
  const totalUs =
    data.roundTripLatencyUs !== undefined && data.roundTripLatencyUs > 0
      ? data.roundTripLatencyUs
      : stageSumUs;

  if (totalUs <= 0) {
    return null;
  }

  let transportPct = transportUs > 0 ? Math.max(2, Math.round((transportUs / totalUs) * 100)) : 0;
  let queuePct = queueWaitUs > 0 ? Math.max(2, Math.round((queueWaitUs / totalUs) * 100)) : 0;
  let serverPct = serverExecUs > 0 ? Math.max(2, Math.round((serverExecUs / totalUs) * 100)) : 0;

  const stagePctSum = transportPct + queuePct + serverPct;
  if (stagePctSum > 100) {
    const scale = 100 / stagePctSum;
    transportPct = transportUs > 0 ? Math.max(1, Math.floor(transportPct * scale)) : 0;
    queuePct = queueWaitUs > 0 ? Math.max(1, Math.floor(queuePct * scale)) : 0;
    serverPct = serverExecUs > 0 ? Math.max(1, Math.floor(serverPct * scale)) : 0;
    const diff = 100 - (transportPct + queuePct + serverPct);
    if (diff > 0) {
      if (serverExecUs >= transportUs && serverExecUs >= queueWaitUs) {
        serverPct += diff;
      } else if (transportUs >= queueWaitUs) {
        transportPct += diff;
      } else {
        queuePct += diff;
      }
    }
  }

  const remainingUs = Math.max(0, totalUs - stageSumUs);
  const remainingPct = remainingUs > 0 ? Math.max(0, 100 - transportPct - queuePct - serverPct) : 0;

  // 诊断结论
  let diagnosis = '各阶段耗时平稳';
  if (!hasBreakdown) {
    diagnosis = '未采集到服务端内部阶段时间戳，当前展示端到端往返总耗时';
  } else if (serverExecUs > totalUs * 0.7) {
    diagnosis = `耗时主要集中在服务端业务执行 (${Math.round((serverExecUs / totalUs) * 100)}%)，网络与排队无延迟`;
  } else if (queueWaitUs > totalUs * 0.4) {
    diagnosis = `网关或线程池排队等待耗时偏高 (${Math.round((queueWaitUs / totalUs) * 100)}%)，建议关注服务端并发瓶颈`;
  } else if (transportUs > totalUs * 0.5) {
    diagnosis = `${transportLabel}耗时占主导 (${Math.round((transportUs / totalUs) * 100)}%)，可能受网络跨机房或慢链路影响`;
  }

  return (
    <div className="code-latency-waterfall-card" data-testid="rpc-latency-waterfall">
      <div className="code-latency-waterfall-header">
        <div className="code-latency-waterfall-title">
          <span>⏱️ 微秒级调用阶段耗时瀑布</span>
          <span className="code-font-mono code-color-primary code-ml-xs">
            {formatLatencyLabel(totalUs)}
          </span>
        </div>
        <div className="code-latency-diagnosis code-text-xs code-color-muted">
          💡 {diagnosis}
        </div>
      </div>

      {/* 比例横向甘特条 */}
      <div className="code-waterfall-bar-track">
        {transportUs > 0 && (
          <div
            className="code-waterfall-segment code-waterfall-segment--client"
            style={{ width: `${transportPct}%` }}
            title={`${transportLabel}: ${transportUs} μs (${transportPct}%)`}
          >
            {transportPct > 12 && (
              <span>
                {hasClientToServer ? '传输' : '网络'} {transportUs}μs
              </span>
            )}
          </div>
        )}
        {queueWaitUs > 0 && (
          <div
            className="code-waterfall-segment code-waterfall-segment--queue"
            style={{ width: `${queuePct}%` }}
            title={`入队排队: ${queueWaitUs} μs (${queuePct}%)`}
          >
            {queuePct > 12 && <span>排队 {queueWaitUs}μs</span>}
          </div>
        )}
        {serverExecUs > 0 && (
          <div
            className="code-waterfall-segment code-waterfall-segment--server"
            style={{ width: `${serverPct}%` }}
            title={`服务端业务执行: ${serverExecUs} μs (${serverPct}%)`}
          >
            {serverPct > 12 && <span>服务处理 {serverExecUs}μs</span>}
          </div>
        )}
        {remainingPct > 0 && (
          <div
            className="code-waterfall-segment code-waterfall-segment--other"
            style={{ width: `${remainingPct}%` }}
            title={hasBreakdown ? `网络回传与分帧: ${remainingUs} μs` : `端到端往返耗时: ${totalUs} μs`}
          />
        )}
      </div>

      {/* 图例与详细微秒列表 */}
      <div className="code-waterfall-legend">
        {hasBreakdown ? (
          <>
            <div className="code-waterfall-legend-item">
              <span className="code-legend-dot code-bg-info" />
              <span className="code-color-muted">{transportLabel}:</span>
              <span className="code-font-mono">{transportUs} μs</span>
            </div>
            <div className="code-waterfall-legend-item">
              <span className="code-legend-dot code-bg-warning" />
              <span className="code-color-muted">入队排队:</span>
              <span className="code-font-mono">{queueWaitUs} μs</span>
            </div>
            <div className="code-waterfall-legend-item">
              <span className="code-legend-dot code-bg-primary" />
              <span className="code-color-muted">服务端业务执行:</span>
              <span className="code-font-mono">{serverExecUs} μs</span>
            </div>
          </>
        ) : (
          <div className="code-waterfall-legend-item">
            <span className="code-legend-dot code-bg-primary" />
            <span className="code-color-muted">端到端往返耗时:</span>
            <span className="code-font-mono">{formatLatencyLabel(totalUs)}</span>
          </div>
        )}
      </div>
    </div>
  );
};
