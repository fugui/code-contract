import type { CSGEndpointInfo, CSGPortUsage } from './agent';

export type ObservedProcessRole = 'SERVER' | 'CLIENT_ONLY' | 'DUAL';
export type EnrollStatus =
  | 'UNENROLLED'
  | 'ENROLLED_DISARMED'
  | 'ARMED_READY'
  | 'ARMED_NEED_RECONNECT';
export type IdentityMatchStatus = 'MATCHED' | 'AMBIGUOUS' | 'LOW_CONFIDENCE' | 'NEW';
export type ProviderKind =
  | 'LOCAL_ENROLLED'
  | 'LOCAL_UNENROLLED'
  | 'REMOTE_LDC'
  | 'OFFLINE';

export interface DiscoverySummaryDTO {
  process_count: number;
  client_only_count: number;
  remote_ldc_count: number;
  total_services: number;
  total_subscriptions: number;
  tcp_count: number;
  mesh_count: number;
  matched_services: number;
  undeclared_services: number;
}

export interface DiscoveryLiveServiceDTO {
  service_name: string;
  raw_service_name: string;
  endpoints: string[];
  transport: string; // 'TCP' | 'MESH' | 'MIXED'
  server_pids: number[];
  instance_count: number;
  is_multi_host: boolean;
  subscriber_count: number;
  proto_match_status: 'EXACT' | 'UNMATCHED';
  proto_service_name?: string;
  claim_status: 'CLAIMED' | 'UNCLAIMED';
  ssot_service_id?: number;
  ssot_module_id?: number;
  ssot_module_code?: string;
  ssot_module_name?: string;
}

export interface DiscoveryLiveSubDTO {
  service_name: string;
  raw_service_name: string;
  provider_kind: ProviderKind;
  provider_proc_key?: string;
  provider_proc_name?: string;
  provider_endpoint?: string;
  remote_node?: string;
  transport?: string;
}

export interface DiscoveryProcessGroupDTO {
  process_key: string;
  proc_name: string;
  exe_name: string;
  cmdline: string;
  identity_fingerprint: string;
  pids: number[];
  listen_ports: number[];
  observed_role: ObservedProcessRole;
  observed_transports: string[];
  enroll_status: EnrollStatus;
  identity_match: IdentityMatchStatus;
  arch_process_id?: number;
  arch_process_code?: string;
  arch_process_name?: string;
  process_id?: number;
  probe_status?: string;
  is_remote_ldc: boolean;
  remote_node?: string;
  claimed_service_count: number;
  unclaimed_service_count: number;
  services: DiscoveryLiveServiceDTO[];
  subscriptions: DiscoveryLiveSubDTO[];
}

export interface DiscoveryLiveRespDTO {
  agent_id: number;
  agent_name: string;
  agent_scope: 'SHARED' | 'PRIVATE' | string;
  csg_addr: string;
  csg_endpoints: CSGEndpointInfo[];
  stale: boolean;
  stale_reason?: string;
  captured_at: string;
  discovery_mode: string;
  ports: CSGPortUsage[];
  processes: DiscoveryProcessGroupDTO[];
  remote_ldc_processes: DiscoveryProcessGroupDTO[];
  summary: DiscoverySummaryDTO;
  errors?: string[];
  snapshot_id?: number;
}

export interface DiscoverySnapshotSummaryDTO {
  id: number;
  agent_id: number;
  agent_name: string;
  trigger_mode: 'MANUAL' | 'SCHEDULED' | 'AUTO' | 'QUICK_RECORD' | string;
  csg_addr: string;
  discovery_mode: string;
  label: string;
  is_pinned: boolean;
  content_hash: string;
  redaction_version: number;
  summary: DiscoverySummaryDTO;
  created_by: number;
  captured_at: string;
  created_at: string;
}

export interface DiscoverySnapshotDTO extends DiscoverySnapshotSummaryDTO {
  deduplicated?: boolean;
}

export type DiscoveryDiffKind =
  | 'ADDED_PROCESS'
  | 'REMOVED_PROCESS'
  | 'ADDED_SERVICE'
  | 'REMOVED_SERVICE'
  | 'TRANSPORT_CHANGED'
  | 'ADDED_SUBSCRIPTION'
  | 'REMOVED_SUBSCRIPTION'
  | 'PROVIDER_KIND_CHANGED';

export interface DiscoveryDiffItemDTO {
  kind: DiscoveryDiffKind;
  process_key: string;
  process_name: string;
  service_name?: string;
  left_transport?: string;
  right_transport?: string;
  left_provider_kind?: string;
  right_provider_kind?: string;
  detail: string;
}

export interface DiscoveryDiffSummaryDTO {
  total: number;
  added_process: number;
  removed_process: number;
  added_service: number;
  removed_service: number;
  transport_changed: number;
  added_subscription: number;
  removed_subscription: number;
  provider_kind_changed: number;
}

export interface DiscoverySnapshotDiffDTO {
  mode: 'TIMELINE' | 'CROSS_ENV' | string;
  left_label: string;
  right_label: string;
  left_captured_at?: string;
  right_captured_at?: string;
  summary: DiscoveryDiffSummaryDTO;
  items: DiscoveryDiffItemDTO[];
}
