import React, { useState } from 'react';
import { Link } from 'react-router-dom';
import { AlertTriangle, Play, Settings2, Sliders } from 'lucide-react';
import * as schemaApi from '../../api/schema';
import { useModulePath } from '../../hooks/useModulePath';
import { useToast } from '../../utils/toast';
import { GlobalGovernanceConfigDrawer } from '../ProtoSchemaWorkbenchPage/GlobalGovernanceConfigDrawer';
import type { PipelineOverviewDTO } from '../../types';

export interface StageNormalizePaneProps {
  overview: PipelineOverviewDTO | null;
  isAdmin: boolean;
  locked: boolean;
  onRefresh: () => void;
}

export const StageNormalizePane: React.FC<StageNormalizePaneProps> = ({
  overview,
  isAdmin,
  locked,
  onRefresh,
}) => {
  const resolvePath = useModulePath();
  const toast = useToast();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [running, setRunning] = useState(false);

  const stage2 = overview?.stage2;

  const handleRenormalizeAll = async () => {
    if (!isAdmin || locked) return;
    setRunning(true);
    try {
      const res = await schemaApi.applyAndRenormalizeGovernance({
        force_recompile: false,
      });
      toast.success(`免网络重清洗完成（配置修订 rev=${res.revision}）：成功 ${res.succeeded_count}/${res.processed_count} 仓`);
      onRefresh();
    } catch (err) {
      toast.error((err as Error).message || '重清洗失败');
    } finally {
      setRunning(false);
    }
  };

  return (
    <div className="ppipe-panel-card" data-testid="stage-normalize-pane">
      <div className="ppipe-panel-header">
        <h3>
          <Sliders size={17} /> 阶段 ②：清洗规范化与目录映射
        </h3>
        <div className="ppipe-actions-row">
          <span className="pdoc-pill">
            配置版本 rev #{overview?.governance_revision ?? 0}
            {overview?.governance_config_sha256
              ? ` · ${overview.governance_config_sha256.slice(0, 8)}`
              : ''}
          </span>
          <Link to={resolvePath('/proto-sources')} className="btn btn-secondary btn-sm">
            浏览仓库清洗源码
          </Link>
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            onClick={() => setDrawerOpen(true)}
          >
            <Settings2 size={14} /> 清洗与导出规则配置
          </button>
          {isAdmin && (
            <button
              type="button"
              className="btn btn-primary btn-sm"
              onClick={() => void handleRenormalizeAll()}
              disabled={running || locked}
            >
              <Play size={14} /> {running ? '正在重洗...' : '按当前规则免网络重洗'}
            </button>
          )}
        </div>
      </div>

      {overview?.normalize_stale && (
        <div className="ppipe-alert ppipe-alert--warning">
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            <AlertTriangle size={15} />
            <span>
              检测到清洗配置或源码变化，建议重新清洗：
              {(overview.normalize_stale_reasons || []).join('；')}
            </span>
          </div>
        </div>
      )}

      <div className="ppipe-kpi-grid">
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">入池契约源文件</div>
          <div className="ppipe-kpi-value">{stage2?.total_files ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">已完成清洗规范化</div>
          <div className="ppipe-kpi-value">{stage2?.normalized_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">规范化隔离文件</div>
          <div className="ppipe-kpi-value">{stage2?.quarantined_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">微服务定义源码</div>
          <div className="ppipe-kpi-value">{stage2?.uservice_count ?? 0}</div>
        </div>
        <div className="ppipe-kpi-item">
          <div className="ppipe-kpi-label">未归属子系统仓</div>
          <div className="ppipe-kpi-value">{stage2?.unassigned_repos ?? 0}</div>
        </div>
      </div>

      <GlobalGovernanceConfigDrawer
        open={drawerOpen}
        isAdmin={isAdmin}
        onClose={() => setDrawerOpen(false)}
        onApplied={onRefresh}
      />
    </div>
  );
};
