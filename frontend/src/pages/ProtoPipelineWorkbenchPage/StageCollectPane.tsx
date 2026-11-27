import React, { useState } from 'react';
import { Link } from 'react-router-dom';
import { DownloadCloud, FolderGit2 } from 'lucide-react';
import * as schemaApi from '../../api/schema';
import { useModulePath } from '../../hooks/useModulePath';
import { useToast } from '../../utils/toast';
import type {
  PipelineOverviewDTO,
  SyncCachePolicy,
  SyncJobProgressEventPayload,
} from '../../types';

export interface StageCollectPaneProps {
  overview: PipelineOverviewDTO | null;
  isAdmin: boolean;
  locked: boolean;
  syncProgress: SyncJobProgressEventPayload | null;
  onRefresh: () => void;
}

export const StageCollectPane: React.FC<StageCollectPaneProps> = ({
  overview,
  isAdmin,
  locked,
  syncProgress,
  onRefresh,
}) => {
  const resolvePath = useModulePath();
  const toast = useToast();
  const [cachePolicy, setCachePolicy] = useState<SyncCachePolicy>('CACHE_ONLY');
  const [submitting, setSubmitting] = useState(false);

  const stage1 = overview?.stage1;

  const handleTriggerSyncAll = async () => {
    if (!isAdmin || locked) return;
    setSubmitting(true);
    try {
      const job = await schemaApi.syncAllGitRepos({
        scope: 'ALL',
        cache_policy: cachePolicy,
        skip_compile: true,
      });
      toast.success(`已触发全平台代码仓拉取任务 #${job.id}（仅执行阶段 ①~②，跳过单仓盲编）`);
      onRefresh();
    } catch (err) {
      toast.error((err as Error).message || '触发全平台拉取失败');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="ppipe-panel-card" data-testid="stage-collect-pane">
      <div className="ppipe-panel-header">
        <h3>
          <FolderGit2 size={17} /> 阶段 ①：全平台 Git 代码仓拉取
        </h3>
        <div className="ppipe-actions-row">
          <Link to={resolvePath('/proto-sources')} className="btn btn-secondary btn-sm">
            查看代码仓与源码详情
          </Link>
          {isAdmin && (
            <>
              <select
                className="pgit-input"
                aria-label="同步缓存策略"
                value={cachePolicy}
                onChange={(e) => setCachePolicy(e.target.value as SyncCachePolicy)}
                disabled={submitting || locked}
              >
                <option value="CACHE_ONLY">仅使用本地缓存 (CACHE_ONLY)</option>
                <option value="FETCH_MISSING">缺失缓存时自动拉取 (FETCH_MISSING)</option>
              </select>
              <button
                type="button"
                className="btn btn-primary btn-sm"
                onClick={() => void handleTriggerSyncAll()}
                disabled={submitting || locked}
              >
                <DownloadCloud size={14} /> {submitting ? '正在调度...' : '立即拉取全平台仓库'}
              </button>
            </>
          )}
        </div>
      </div>

      <div className="ppipe-kpi-grid">
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">平台活跃代码仓</div>
          <div className="ppipe-kpi-value">{stage1?.platform_active_total ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">含契约源码仓</div>
          <div className="ppipe-kpi-value">{stage1?.has_proto_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">无匹配目录</div>
          <div className="ppipe-kpi-value">{stage1?.no_proto_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">缺少本地缓存</div>
          <div className="ppipe-kpi-value">{stage1?.no_local_cache_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">最近同步失败</div>
          <div className="ppipe-kpi-value">{stage1?.sync_failed_count ?? 0}</div>
        </div>
      </div>

      {syncProgress && (
        <div className="ppipe-progress-banner">
          <span>
            全量拉取进度 #{syncProgress.job_id}：{syncProgress.processed_repos}/
            {syncProgress.total_repos} 仓（含 Proto {syncProgress.has_proto_count}，失败{' '}
            {syncProgress.failed_count}）· {syncProgress.status}
          </span>
        </div>
      )}

      {!stage1?.shared_write_enabled && (
        <div className="ppipe-alert ppipe-alert--warning">
          <span>当前部署处于只读共享模式（shared_write_enabled=false），拉取仅允许使用本地缓存。</span>
        </div>
      )}
    </div>
  );
};
