export type RadarState =
  | 'HEALTHY'
  | 'SHADOW_LIVE'
  | 'UNCLAIMED_ORPHAN'
  | 'PHANTOM_PROTO'
  | 'VANISHED_CLAIMED'
  | 'PLACEMENT_DRIFT';

export interface FiveStateRadarDTO {
  shadow_live_count: number;
  unclaimed_orphan_count: number;
  phantom_proto_count: number;
  vanished_claimed_count: number;
  placement_drift_count: number;
  total_catalog_services: number;
  authority_scope_only: boolean;
  shadow_live_services?: string[];
  unclaimed_orphans?: string[];
  phantom_protos?: string[];
  vanished_claimed?: string[];
  placement_drifts?: string[];
}

export interface UnifiedServiceCatalogDTO {
  id: number;
  service_name: string;
  source: string;
  claim_status: 'CLAIMED' | 'UNCLAIMED' | 'PHANTOM_ONLY' | string;
  module_id?: number;
  module_code?: string;
  module_name?: string;
  contract_id?: number;
  impl_key?: string;
  proto_service_name?: string;
  proto_suggested_name?: string;
  proto_match_confidence: string;
  proto_status: 'CONFIRMED' | 'SUGGESTED' | 'UNMATCHED' | string;
  operation_count: number;
  shared_placement_count: number;
  active_arch_process_ids?: number[];
  transports?: string[];
  recent_traffic_count: number;
  radar_state: RadarState | string;
  radar_states?: string[];
}

export interface CatalogServicesResponseDTO {
  radar: FiveStateRadarDTO;
  items: UnifiedServiceCatalogDTO[];
  total: number;
  page: number;
  page_size: number;
}

export interface CatalogCorrelateResult {
  exact_matched: number;
  suggested_count: number;
  reloaded: boolean;
}
