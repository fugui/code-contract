import React, { useCallback, useEffect, useState } from 'react';
import { Check, HelpCircle, Info, RefreshCw, Sparkles, X } from 'lucide-react';
import { Pagination, usePaginationState } from '@code/common';
import * as catalogApi from '../../api/catalog';
import { useToast } from '../../utils/toast';
import {
  RADAR_ALL_ORDER,
  RADAR_STATE_META,
  formatMatchConfidence,
  formatTransport,
  getClaimStatusMeta,
  getProtoStatusMeta,
  getRadarStateMeta,
} from '../../utils/catalogTerms';
import type {
  CatalogServicesResponseDTO,
  FiveStateRadarDTO,
  RadarState,
  UnifiedServiceCatalogDTO,
} from '../../types/catalog';

export interface UnifiedCatalogPaneProps {
  initialState?: string;
  initialData?: CatalogServicesResponseDTO | null;
  isAdmin?: boolean;
  onStateChange?: (nextState: string) => void;
}

const STATE_FILTER_TABS: Array<{ key: '' | RadarState; label: string; title: string }> = [
  { key: '', label: '全部目录契约', title: '展示全部契约与现网服务' },
  ...RADAR_ALL_ORDER.map((key) => ({
    key,
    label: RADAR_STATE_META[key].label,
    title: RADAR_STATE_META[key].summary,
  })),
];

function radarCountForKey(radar: FiveStateRadarDTO | undefined, key: '' | RadarState): number | null {
  if (!radar) return null;
  switch (key) {
    case '':
      return radar.total_catalog_services;
    case 'SHADOW_LIVE':
      return radar.shadow_live_count;
    case 'UNCLAIMED_ORPHAN':
      return radar.unclaimed_orphan_count;
    case 'PHANTOM_PROTO':
      return radar.phantom_proto_count;
    case 'VANISHED_CLAIMED':
      return radar.vanished_claimed_count;
    case 'PLACEMENT_DRIFT':
      return radar.placement_drift_count;
    default:
      return null;
  }
}

export const UnifiedCatalogPane: React.FC<UnifiedCatalogPaneProps> = ({
  initialState = '',
  initialData = null,
  isAdmin = false,
  onStateChange,
}) => {
  const toast = useToast();
  const { page, pageSize, setPage, setPageSize } = usePaginationState({ defaultPageSize: 25 });

  const [stateFilter, setStateFilter] = useState<string>(initialState);
  const [searchKeyword, setSearchKeyword] = useState<string>('');
  const [loading, setLoading] = useState<boolean>(false);
  const [correlating, setCorrelating] = useState<boolean>(false);
  const [confirmingId, setConfirmingId] = useState<number | null>(null);
  const [showGlossary, setShowGlossary] = useState<boolean>(false);
  const [data, setData] = useState<CatalogServicesResponseDTO | null>(initialData);

  useEffect(() => {
    setStateFilter(initialState);
  }, [initialState]);

  const loadCatalog = useCallback(async () => {
    setLoading(true);
    try {
      const resp = await catalogApi.listCatalogServices({
        state: stateFilter || undefined,
        search: searchKeyword.trim() || undefined,
        page,
        page_size: pageSize,
      });
      setData(resp);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '加载统一契约目录失败');
    } finally {
      setLoading(false);
    }
  }, [stateFilter, searchKeyword, page, pageSize, toast]);

  useEffect(() => {
    if (initialData !== null && stateFilter === initialState && !searchKeyword && page === 1) {
      return;
    }
    void loadCatalog();
  }, [initialData, stateFilter, initialState, searchKeyword, page, loadCatalog]);

  const handleCorrelate = async () => {
    setCorrelating(true);
    try {
      const res = await catalogApi.correlateCatalogServices();
      toast.success(`匹配完成：精确自动绑定 ${res.exact_matched} 条，待确认建议 ${res.suggested_count} 条`);
      await loadCatalog();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '执行 .pb 匹配失败');
    } finally {
      setCorrelating(false);
    }
  };

  const handleConfirmProto = async (item: UnifiedServiceCatalogDTO) => {
    const suggested = item.proto_suggested_name?.trim();
    if (!suggested) return;
    setConfirmingId(item.id);
    try {
      await catalogApi.confirmCatalogServiceProto(item.id, suggested);
      toast.success(`已确认采纳建议 ${suggested}（如需重建索引将自动生效）`);
      await loadCatalog();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '确认建议失败');
    } finally {
      setConfirmingId(null);
    }
  };

  const items = data?.items ?? [];
  const total = data?.total ?? 0;
  const radar = data?.radar;
  const activeStateMeta = stateFilter ? getRadarStateMeta(stateFilter) : null;

  return (
    <div className="ucat-pane" data-testid="unified-catalog-pane">
      <div className="ucat-toolbar">
        <div className="ucat-state-pills" role="group" aria-label="契约健康状态筛选">
          {STATE_FILTER_TABS.map((tab) => {
            const active = stateFilter === tab.key;
            const cnt = radarCountForKey(radar, tab.key);
            return (
              <button
                key={tab.key || 'ALL'}
                type="button"
                title={tab.title}
                data-testid={`catalog-state-filter-${tab.key || 'ALL'}`}
                className={`code-pill-btn ${active ? 'code-pill-btn--active' : ''}`}
                onClick={() => {
                  setStateFilter(tab.key);
                  setPage(1);
                  onStateChange?.(tab.key);
                }}
              >
                <span>{tab.label}</span>
                {cnt !== null && <span className="ucat-pill-count">{cnt}</span>}
              </button>
            );
          })}
        </div>

        <div className="ucat-toolbar-right">
          <input
            type="search"
            className="code-input ucat-search-input"
            value={searchKeyword}
            onChange={(e) => {
              setSearchKeyword(e.target.value);
              setPage(1);
            }}
            placeholder="搜索现网服务名 / Proto / 模块..."
          />
          <button
            type="button"
            className={`code-btn code-btn--sm ${showGlossary ? 'code-btn--primary' : 'code-btn--secondary'}`}
            data-testid="catalog-glossary-toggle"
            aria-expanded={showGlossary}
            onClick={() => setShowGlossary((prev) => !prev)}
            title="查看各健康状态含义与排查建议"
          >
            <HelpCircle size={13} />
            <span>状态说明</span>
          </button>
          <button
            type="button"
            className="code-btn code-btn--secondary code-btn--sm"
            onClick={() => void loadCatalog()}
            disabled={loading}
          >
            <RefreshCw size={13} />
            <span>刷新</span>
          </button>
          <button
            type="button"
            className="code-btn code-btn--primary code-btn--sm"
            data-testid="catalog-correlate-btn"
            title="按精确名称、命名规则、流量方法指纹三级自动匹配 .pb 契约"
            onClick={() => void handleCorrelate()}
            disabled={correlating}
          >
            <Sparkles size={13} />
            <span>{correlating ? '匹配计算中...' : '一键匹配 .pb 契约'}</span>
          </button>
        </div>
      </div>

      {activeStateMeta && (
        <div
          className={`ucat-state-callout ucat-state-callout--${activeStateMeta.badgeTone}`}
          data-testid="catalog-active-state-callout"
        >
          <Info size={15} className="ucat-state-callout__icon" />
          <div className="ucat-state-callout__body">
            <strong>{activeStateMeta.label}：</strong>
            <span>{activeStateMeta.detail || activeStateMeta.summary}</span>
          </div>
        </div>
      )}

      {showGlossary && (
        <div className="ucat-glossary-panel" data-testid="catalog-glossary-panel">
          <div className="ucat-glossary-header">
            <div className="ucat-glossary-title">
              <HelpCircle size={14} />
              <span>契约健康状态说明（基于共享权威环境实时判定）</span>
            </div>
            <button
              type="button"
              className="code-btn code-btn--secondary code-btn--xs"
              onClick={() => setShowGlossary(false)}
            >
              <X size={12} />
              <span>收起</span>
            </button>
          </div>
          <div className="ucat-glossary-grid">
            {RADAR_ALL_ORDER.map((stateKey) => {
              const meta = RADAR_STATE_META[stateKey];
              return (
                <div key={stateKey} className="ucat-glossary-item">
                  <div className="ucat-glossary-item__head">
                    <span className={`ucat-badge ucat-badge--${meta.badgeTone}`}>
                      {meta.label}
                    </span>
                    <span className="ucat-glossary-item__summary">{meta.summary}</span>
                  </div>
                  <div className="ucat-glossary-item__detail">{meta.detail}</div>
                </div>
              );
            })}
          </div>
        </div>
      )}

      <div className="ucat-table-card">
        <table className="code-table ucat-table" data-testid="unified-catalog-table">
          <thead>
            <tr>
              <th>
                <span title="现网 CSG 注册的 64 字节服务名">现网服务名</span>
              </th>
              <th>逻辑认领</th>
              <th>.pb 契约匹配状态</th>
              <th>
                <span title="团队共享（SHARED）权威环境中的活跃运行实例数及传输协议">
                  共享环境运行实例
                </span>
              </th>
              <th>健康状态</th>
              <th style={{ textAlign: 'right' }}>操作</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={6} className="ucat-empty-cell">
                  正在加载统一契约服务目录...
                </td>
              </tr>
            ) : items.length === 0 ? (
              <tr>
                <td colSpan={6} className="ucat-empty-cell">
                  当前筛选条件下暂无契约服务记录
                </td>
              </tr>
            ) : (
              items.map((item) => {
                const canConfirm =
                  item.proto_status === 'SUGGESTED' &&
                  Boolean(item.proto_suggested_name) &&
                  (item.claim_status !== 'CLAIMED' || isAdmin);
                const claimMeta = getClaimStatusMeta(item.claim_status);
                const protoMeta = getProtoStatusMeta(item.proto_status);
                const radarMeta = getRadarStateMeta(item.radar_state);
                const extraStates = (item.radar_states ?? []).filter(
                  (st, idx, arr) => Boolean(st) && st !== item.radar_state && arr.indexOf(st) === idx
                );
                return (
                  <tr
                    key={`${item.id}-${item.service_name}`}
                    data-testid={`catalog-row-${item.id}`}
                  >
                    <td className="ucat-mono">
                      <div>{item.service_name}</div>
                      {item.recent_traffic_count > 0 && (
                        <div className="ucat-subtext">近期流量 {item.recent_traffic_count} 条</div>
                      )}
                    </td>
                    <td>
                      <span
                        className={`ucat-badge ucat-badge--${claimMeta.tone}`}
                        title={claimMeta.desc}
                      >
                        {claimMeta.label}
                      </span>
                      {item.module_name && (
                        <div className="ucat-subtext">
                          {item.module_name}
                          {item.module_code ? ` (${item.module_code})` : ''}
                        </div>
                      )}
                    </td>
                    <td>
                      <span className={`ucat-badge ucat-badge--${protoMeta.tone}`}>
                        {protoMeta.label}
                      </span>
                      {item.proto_service_name && (
                        <div className="ucat-mono ucat-subtext">{item.proto_service_name}</div>
                      )}
                      {item.proto_suggested_name && (
                        <div className="ucat-mono ucat-subtext">
                          建议（{formatMatchConfidence(item.proto_match_confidence)}）：
                          {item.proto_suggested_name}
                        </div>
                      )}
                    </td>
                    <td>
                      <span className="ucat-mono">{`${item.shared_placement_count} 个实例`}</span>
                      {item.transports && item.transports.length > 0 && (
                        <div className="ucat-subtext">
                          {item.transports.map(formatTransport).join(' / ')}
                        </div>
                      )}
                    </td>
                    <td>
                      <div className="ucat-badge-group">
                        <span
                          className={`ucat-badge ucat-badge--${radarMeta.badgeTone}`}
                          title={radarMeta.summary}
                        >
                          {radarMeta.label}
                        </span>
                        {extraStates.map((st) => {
                          const extraMeta = getRadarStateMeta(st);
                          return (
                            <span
                              key={st}
                              className={`ucat-badge ucat-badge--${extraMeta.badgeTone}`}
                              title={extraMeta.summary}
                            >
                              {extraMeta.label}
                            </span>
                          );
                        })}
                      </div>
                    </td>
                    <td style={{ textAlign: 'right' }}>
                      {canConfirm ? (
                        <button
                          type="button"
                          className="code-btn code-btn--primary code-btn--xs"
                          data-testid={`confirm-proto-btn-${item.id}`}
                          disabled={confirmingId === item.id}
                          onClick={() => void handleConfirmProto(item)}
                        >
                          <Check size={12} />
                          <span>
                            {confirmingId === item.id ? '确认中...' : '确认采用建议'}
                          </span>
                        </button>
                      ) : item.proto_status === 'SUGGESTED' ? (
                        <span className="ucat-subtext">需模块主管/管理员确认</span>
                      ) : (
                        <span className="ucat-subtext">-</span>
                      )}
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>

        {total > 0 && (
          <div className="ucat-pagination-wrap">
            <Pagination
              page={page}
              pageSize={pageSize}
              totalItems={total}
              onPageChange={setPage}
              onPageSizeChange={(ps: number) => {
                setPageSize(ps);
                setPage(1);
              }}
              pageSizeOptions={[15, 25, 50, 100]}
            />
          </div>
        )}
      </div>
    </div>
  );
};
