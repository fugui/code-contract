import React from 'react';
import { Radar, ArrowUpRight, Shield } from 'lucide-react';
import { RADAR_ABNORMAL_ORDER, RADAR_STATE_META } from '../../utils/catalogTerms';
import type { FiveStateRadarDTO, RadarState } from '../../types/catalog';

export interface FiveStateRadarCardProps {
  radar: FiveStateRadarDTO;
  onNavigateCatalog: (state?: RadarState) => void;
}

interface RadarTileDef {
  state: Exclude<RadarState, 'HEALTHY'>;
  label: string;
  desc: string;
  title: string;
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

const RADAR_COUNT_KEY: Record<Exclude<RadarState, 'HEALTHY'>, RadarTileDef['countKey']> = {
  SHADOW_LIVE: 'shadow_live_count',
  UNCLAIMED_ORPHAN: 'unclaimed_orphan_count',
  PHANTOM_PROTO: 'phantom_proto_count',
  VANISHED_CLAIMED: 'vanished_claimed_count',
  PLACEMENT_DRIFT: 'placement_drift_count',
};

const RADAR_TILES: RadarTileDef[] = RADAR_ABNORMAL_ORDER.map((state) => {
  const meta = RADAR_STATE_META[state];
  return {
    state,
    label: meta.label,
    desc: meta.summary,
    title: meta.detail,
    countKey: RADAR_COUNT_KEY[state],
    tone: meta.radarTileTone,
  };
});

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
            title="仅统计团队共享（SHARED）权威环境中已确认架构进程的服务，排除个人私有环境"
          >
            <Shield size={12} />
            仅统计共享权威环境
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
              title={tile.title}
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
