import React, { useCallback, useEffect, useState } from 'react';
import { Check, RefreshCw, Sparkles } from 'lucide-react';
import { Pagination, usePaginationState } from '@code/common';
import * as catalogApi from '../../api/catalog';
import { useToast } from '../../utils/toast';
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

const STATE_FILTER_TABS: Array<{ key: '' | RadarState; label: string }> = [
  { key: '', label: '全部目录契约' },
  { key: 'SHADOW_LIVE', label: '阴影在线 (SHADOW_LIVE)' },
  { key: 'UNCLAIMED_ORPHAN', label: '未认领孤儿 (UNCLAIMED_ORPHAN)' },
  { key: 'PHANTOM_PROTO', label: '幻影订阅 (PHANTOM_PROTO)' },
  { key: 'VANISHED_CLAIMED', label: '已认领失联 (VANISHED_CLAIMED)' },
  { key: 'PLACEMENT_DRIFT', label: '部署漂移 (PLACEMENT_DRIFT)' },
  { key: 'HEALTHY', label: '健康 (HEALTHY)' },
];

const PROTO_STATUS_LABEL: Record<string, string> = {
  CONFIRMED: '已确认',
  SUGGESTED: '待确认建议',
  UNMATCHED: '未匹配 .pb',
};

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
      toast.success(`三级撮合完成：EXACT 自动绑定 ${res.exact_matched} 条，候选建议 ${res.suggested_count} 条`);
      await loadCatalog();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '执行 .pb 撮合失败');
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

  return (
    <div className="ucat-pane" data-testid="unified-catalog-pane">
      <div className="ucat-toolbar">
        <div className="ucat-state-pills" role="group" aria-label="五态雷达状态筛选">
          {STATE_FILTER_TABS.map((tab) => {
            const active = stateFilter === tab.key;
            const cnt = radarCountForKey(radar, tab.key);
            return (
              <button
                key={tab.key || 'ALL'}
                type="button"
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
            placeholder="搜索 CSG 服务名 / Proto / 模块..."
          />
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
            onClick={() => void handleCorrelate()}
            disabled={correlating}
          >
            <Sparkles size={13} />
            <span>{correlating ? '撮合计算中...' : '一键三级撮合 .pb'}</span>
          </button>
        </div>
      </div>

      <div className="ucat-table-card">
        <table className="code-table ucat-table" data-testid="unified-catalog-table">
          <thead>
            <tr>
              <th>CSG 64B 服务名</th>
              <th>逻辑认领</th>
              <th>.pb 契约撮合状态</th>
              <th>权威放置 (SHARED)</th>
              <th>雷达状态</th>
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
                        className={`ucat-badge ${
                          item.claim_status === 'CLAIMED'
                            ? 'ucat-badge--success'
                            : 'ucat-badge--warning'
                        }`}
                      >
                        {item.claim_status}
                      </span>
                      {item.module_name && (
                        <div className="ucat-subtext">
                          {item.module_name} ({item.module_code})
                        </div>
                      )}
                    </td>
                    <td>
                      <span
                        className={`ucat-badge ${
                          item.proto_status === 'CONFIRMED'
                            ? 'ucat-badge--success'
                            : item.proto_status === 'SUGGESTED'
                            ? 'ucat-badge--info'
                            : 'ucat-badge--neutral'
                        }`}
                      >
                        {PROTO_STATUS_LABEL[item.proto_status] || item.proto_status}
                      </span>
                      {item.proto_service_name && (
                        <div className="ucat-mono ucat-subtext">{item.proto_service_name}</div>
                      )}
                      {item.proto_suggested_name && (
                        <div className="ucat-mono ucat-subtext">
                          建议 ({item.proto_match_confidence}): {item.proto_suggested_name}
                        </div>
                      )}
                    </td>
                    <td>
                      <span className="ucat-mono">{item.shared_placement_count}</span>
                      {item.transports && item.transports.length > 0 && (
                        <div className="ucat-subtext">{item.transports.join(' / ')}</div>
                      )}
                    </td>
                    <td>
                      <span
                        className={`ucat-badge ${
                          item.radar_state === 'HEALTHY'
                            ? 'ucat-badge--success'
                            : item.radar_state === 'PHANTOM_PROTO' ||
                              item.radar_state === 'PLACEMENT_DRIFT'
                            ? 'ucat-badge--danger'
                            : 'ucat-badge--warning'
                        }`}
                      >
                        {item.radar_state}
                      </span>
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
