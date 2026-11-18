import type { ArchProcess } from './arch';

export interface Agent {
  id: string;
  name: string;
  endpoint: string;
  scope: 'SHARED' | 'PRIVATE';
  owner_id?: string;
  owner_name?: string;
  owner?: { id: number | string; username?: string; name?: string };
  status: 'ONLINE' | 'OFFLINE';
  boot_id?: string;
  mounts_revision?: number;
  fallback_agent_id?: number | null;
  expected_armed_digest?: string;
  armed_digest?: string;
  agent_version?: string;
  protocol_version?: string;
  kernel_version?: string;
  has_ebpf_cap?: boolean;
  cap_detail?: string;
  lease_type: 'RECORDING' | 'REPLAY' | '';
  lease_ref_id?: string;
  lease_holder_id?: string;
  lease_holder_name?: string;
  lease_acquired_at?: string;
  lease_expires_at?: string;
  last_heartbeat_at?: string;
  claim_code?: string;
  description?: string;
  processes_count?: number;
  processes?: Process[];
  created_at?: string;
  updated_at?: string;
}

export interface ProcessModuleBinding {
  process_id: string;
  module_id: string;
  module?: {
    id: string;
    code: string;
    name: string;
  };
}

export type ProbeStatus =
  | 'READY'
  | 'NEED_RECONNECT'
  | 'WAITING_PROC'
  | 'MONITORING'
  | 'DISARMED'
  | 'OFFLINE'
  | 'CAP_DENIED'
  | 'ERROR';

export interface Process {
  id: string;
  agent_id: string;
  arch_process_id?: string | number | null;
  arch_process?: ArchProcess;
  group_name?: string;
  name: string;
  listen_port: number;
  effective_listen_port?: number;
  override_listen_port?: number | null;
  advertise_ip?: string;
  cmd_pattern?: string;
  override_cmd_pattern?: string;
  probe_desired_state?: 'ARMED' | 'DISARMED';
  probe_status?: ProbeStatus;
  probe_status_reason?: string;
  diag_message?: string;
  module_ids?: string[];
  extra_downstreams?: string[];
  description?: string;
  agent?: Agent;
  process_modules?: ProcessModuleBinding[];
  created_at?: string;
  updated_at?: string;
}

export interface BatchProcessMountItem {
  arch_process_id: number | string;
  override_listen_port?: number | null;
  override_cmd_pattern?: string;
  advertise_ip?: string;
  extra_downstreams?: string[];
  probe_desired_state?: 'ARMED' | 'DISARMED' | string;
  description?: string;
}

export interface BatchProcessMountReq {
  expected_revision: number;
  mounts: BatchProcessMountItem[];
}

export interface CSGEndpointInfo {
  pid: number;
  exe_name?: string;
  cmdline?: string;
  listen_ip: string;
  listen_port: number;
  source: string;
}

export interface DiscoveredProcess {
  pid: number;
  listen_port: number;
  cmdline: string;
  exe_name: string;
  discovery_source?: string; // "PROC_ONLY" | "CSG_RPCMON" | "HYBRID"
  csg_service_names?: string[];
  csg_transports?: string[];
}

export interface HandshakeResp {
  agent_version: string;
  protocol_version: string;
  boot_id: string;
  started_at: string;
  kernel_version: string;
  has_ebpf_cap: boolean;
  cap_detail?: string;
  active_lease_type: string;
  active_ref_id: number;
  uptime_sec: number;
  csg_endpoints?: CSGEndpointInfo[];
}

export type CSGReconcileState =
  | 'MATCHED'
  | 'UNDECLARED_IN_SSOT'
  | 'MISSING_IN_LIVE';

export interface CSGPortUsage {
  ip_addr: string;
  used: number;
  total: number;
}

export interface ReconciledCSGService {
  service_name: string;
  addr?: string;
  transport?: string;
  host_ip?: string;
  port?: number;
  server_pid?: number;
  server_proc?: string;
  os_node_id?: number;
  version?: number;
  is_local?: boolean;
  resolved_by_sub?: boolean;
  state: CSGReconcileState;
  ssot_service_id?: number;
  ssot_module_id?: number;
  ssot_module_code?: string;
  ssot_module_name?: string;
  ssot_arch_process_id?: number;
  ssot_arch_proc_id?: number;
  ssot_proto_svc?: string;
  expected_port?: number;
}

export interface ReconciledCSGSubscription {
  client_pid: number;
  client_proc: string;
  client_addr: string;
  service_name: string;
  state: CSGReconcileState;
  ssot_dep_id?: number;
}

export interface CSGReconcileSummary {
  live_service_count: number;
  matched_count: number;
  undeclared_count: number;
  missing_in_live_count: number;
  live_sub_count: number;
  sub_matched_count?: number;
  sub_undeclared_count?: number;
  sub_missing_count?: number;
  undeclared_sub_count?: number;
}

export interface CSGTopologyReconcileResp {
  agent_id: number;
  discovery_mode: string;
  csg_endpoints: CSGEndpointInfo[];
  port_usages?: CSGPortUsage[];
  services: ReconciledCSGService[];
  subscriptions: ReconciledCSGSubscription[];
  summary: CSGReconcileSummary;
  filtered_remote_count: number;
  filtered_transient_count: number;
  captured_at: string;
  errors?: string[];
}

export interface CSGImportBlueprintReq {
  subsystem_id: number | string;
  group_id?: number | string;
  process_group_id?: number | string;
  csg_addr?: string;
  service_names?: string[];
  import_dependencies?: boolean;
}

export interface CSGImportBlueprintResult {
  created_processes: number;
  skipped_processes?: number;
  created_arch_processes?: number;
  created_modules: number;
  created_services: number;
  skipped_services: number;
  created_dependencies: number;
  skipped_dependencies: number;
  functional_group_id?: number;
  process_group_id?: number;
  imported_service_names?: string[];
  imported_dependencies?: string[];
}
