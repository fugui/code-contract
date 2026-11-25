import type { CSGEndpointInfo, CSGPortUsage } from './agent';
import type { RecordingSession } from './traffic';

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

export interface ProcessCandidateDTO {
  arch_process_id: number;
  code: string;
  name: string;
  lifecycle: string;
  match_tier: string;
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
  candidates?: ProcessCandidateDTO[];
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

export interface EnrollItemInput {
  process_key: string;
  force_arch_process_id?: number;
  confirm_low_confidence?: boolean;
  display_name?: string;
}

export interface EnrolledProcessDTO {
  process_key: string;
  arch_process_id: number;
  arch_process_code: string;
  arch_process_name: string;
  lifecycle: string;
  process_id: number;
  result: string;
  services_created: number;
  placements_updated: number;
}

export interface EnrollResultDTO {
  agent_id: number;
  enrolled: EnrolledProcessDTO[];
  placements_count: number;
  dependencies_count: number;
  snapshot_id?: number;
}

export interface EnrollValidationErrorPayload {
  code: 'IDENTITY_AMBIGUOUS' | 'LOW_CONFIDENCE_CONFIRM_REQUIRED' | 'ENROLL_BATCH_LIMIT' | string;
  error: string;
  process_key?: string;
  candidates?: ProcessCandidateDTO[];
}

export interface PhysicalProcessDTO {
  process_id: number;
  agent_id: number;
  agent_name: string;
  agent_scope: string;
  arch_process_id: number;
  arch_process_code: string;
  arch_process_name: string;
  group_id: number;
  lifecycle: string;
  origin: string;
  enroll_source: string;
  declared_role: string;
  observed_role: string;
  observed_transports: string[];
  observed_pids: number[];
  effective_listen_port: number;
  probe_desired_state: string;
  probe_status: string;
  last_observed_at?: string;
}

export interface ServicePlacementDTO {
  id: number;
  module_service_id: number;
  service_name: string;
  process_id: number;
  agent_id: number;
  arch_process_id: number;
  endpoints: string[];
  observed_transport: string;
  instance_count: number;
  status: string;
  miss_count: number;
  claim_status: 'CLAIMED' | 'UNCLAIMED' | string;
  module_id?: number;
  last_seen_at: string;
}

export interface ProcessDependencyDTO {
  id: number;
  consumer_process_id: number;
  agent_id: number;
  consumer_arch_process_id: number;
  service_name: string;
  service_id?: number;
  provider_kind: ProviderKind | string;
  provider_process_id?: number;
  provider_arch_process_id?: number;
  remote_node?: string;
  resolved_endpoint?: string;
  status: string;
  miss_count: number;
  last_seen_at: string;
}

export interface PhysicalAssetsRespDTO {
  processes: PhysicalProcessDTO[];
  placements: ServicePlacementDTO[];
  dependencies: ProcessDependencyDTO[];
  total_processes: number;
  page: number;
  page_size: number;
}

export interface BlockedPlaceholderModuleDTO {
  module_id: number;
  module_code: string;
  reason: string;
}

export interface CSGPlaceholderMigrationRespDTO {
  mode: 'DRY_RUN' | 'EXECUTE' | 'ROLLBACK' | string;
  backup_batch_id?: string;
  cleared_modules: number;
  migrated_services: number;
  rewritten_records: number;
  attribution_job_id?: number;
  blocked_items: BlockedPlaceholderModuleDTO[];
}

export interface CoverageWarningDTO {
  code: 'NEED_RECONNECT' | 'LEGACY_AGENT_SINGLE_PID' | 'MESH_REQUIRES_DRT_UPROBE' | string;
  process_id: number;
  process_name: string;
  transport?: string;
  message: string;
}

export interface QuickRecordReqInput {
  agent_id: number | string;
  process_keys: string[];
  name?: string;
  scope?: string;
  duration_sec?: number;
  max_frames?: number;
  max_frame_bytes?: number;
  csg_addr?: string;
  request_id: string;
  identity_overrides?: Record<string, number>;
  confirm_low_confidence?: boolean;
}

export interface QuickRecordResultDTO {
  session: RecordingSession;
  coverage_warnings: CoverageWarningDTO[];
  enrolled_count: number;
  target_drivers: string[];
  idempotent_hit: boolean;
}

export interface DownstreamLiveStatusDTO {
  service_name: string;
  provider_kind: string;
  resolved_endpoint?: string;
  transport: 'TCP' | 'MESH' | 'OFFLINE' | string;
  is_online: boolean;
  has_online_conflict: boolean;
  note?: string;
}

export interface SimulationPlanReqInput {
  agent_id: number | string;
  arch_process_id?: number | string;
  process_key?: string;
  csg_addr?: string;
}

export interface SimulationPlanResultDTO {
  agent_id: number;
  arch_process_id: number;
  process_id?: number;
  process_name: string;
  recommended_strategy: 'PASSTHROUGH' | 'CLIENT_INJECT' | 'PUB_STUB' | 'MOCK_SERVER' | string;
  rationale: string;
  prefill_replay_spec: Record<string, unknown>;
  downstream_statuses: DownstreamLiveStatusDTO[];
}
