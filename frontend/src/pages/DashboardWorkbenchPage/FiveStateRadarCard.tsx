import React from 'react';
import { Radar, ArrowUpRight, Shield } from 'lucide-react';
import type { FiveStateRadarDTO, RadarState } from '../../types/catalog';

export interface FiveStateRadarCardProps {
  radar: FiveStateRadarDTO;
  onNavigateCatalog: (state?: RadarState) => void;
}

interface RadarTileDef {
  state: Exclude<RadarState, 'HEALTHY'>;
  label: string;
  desc: string;
  countKey: keyof Pick<
    FiveStateRadarDTO,
    | 'shadow_live_count'
    | 'unclaimed_orphan_count'
    | 'phantom_proto_count'
    | 'vanished_claimed_count'
    | 'placement_drift_count'
  >;
  tone: 'warning' | 'info' | 'danger';
}

const RADAR_TILES: RadarTileDef[] = [
  {
    state: 'SHADOW_LIVE',
    label: '阴影在线 (SHADOW_LIVE)',
    desc: '现网已活跃但无 .pb / 无认领模块',
    countKey: 'shadow_live_count',
    tone: 'warning',
  },
  {
    state: 'UNCLAIMED_ORPHAN',
    label: '未认领孤儿 (UNCLAIMED_ORPHAN)',
    desc: '有 .pb 定义但尚无逻辑模块认领',
    countKey: 'unclaimed_orphan_count',
    tone: 'info',
  },
  {
    state: 'PHANTOM_PROTO',
    label: '幻影订阅 (PHANTOM_PROTO)',
    desc: '存在活跃依赖订阅但缺失 .pb 定义',
    countKey: 'phantom_proto_count',
    tone: 'danger',
  },
  {
    state: 'VANISHED_CLAIMED',
    label: '已认领失联 (VANISHED_CLAIMED)',
    desc: 'L2 已认领但权威环境未见现网发布',
    countKey: 'vanished_claimed_count',
    tone: 'warning',
  },
  {
    state: 'PLACEMENT_DRIFT',
    label: '部署漂移 (PLACEMENT_DRIFT)',
    desc: '同一服务跨越多个确定态架构进程',
    countKey: 'placement_drift_count',
    tone: 'danger',
  },
];

export const FiveStateRadarCard: React.FC<FiveStateRadarCardProps> = ({
  radar,
  onNavigateCatalog,
}) => {
  return (
    <section
      className="dashboard-card dashboard-card--full"
      data-testid="five-state-radar-card"
    >
      <div className="dashboard-card__header">
        <h2 className="dashboard-card__title">
          <Radar size={18} style={{ color: 'var(--color-primary)' }} />
          <span>契约五态健康雷达</span>
          <span
            className="dashboard-radar-scope-pill"
            data-testid="five-state-radar-shared-badge"
          >
            <Shield size={12} />
            仅统计 SHARED 权威环境
          </span>
        </h2>
        <div className="dashboard-radar-header-right">
          <span className="dashboard-card__meta">
            目录契约总计：{radar.total_catalog_services}
          </span>
          <button
            type="button"
            className="dashboard-link-btn"
            onClick={() => onNavigateCatalog()}
          >
            进入统一契约目录 <ArrowUpRight size={13} />
          </button>
        </div>
      </div>

      <div className="dashboard-radar-tiles">
        {RADAR_TILES.map((tile) => {
          const val = radar[tile.countKey];
          return (
            <button
              key={tile.state}
              type="button"
              className={`dashboard-radar-tile dashboard-radar-tile--${tile.tone}`}
              data-testid={`radar-tile-${tile.state}`}
              onClick={() => onNavigateCatalog(tile.state)}
            >
              <span className="dashboard-radar-tile__count">{val}</span>
              <span className="dashboard-radar-tile__label">{tile.label}</span>
              <span className="dashboard-radar-tile__desc">{tile.desc}</span>
            </button>
          );
        })}
      </div>
    </section>
  );
};
