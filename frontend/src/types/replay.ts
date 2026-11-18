import type { FrameSideAnatomyDTO } from './traffic';

export interface FieldDiff {
  field_path: string;
  diff_kind: 'MODIFIED' | 'ADDED' | 'REMOVED';
  expected: unknown;
  actual: unknown;
}

export interface MockTraceEvent {
  sno: string;
  service_name: string;
  operation: string;
  hit_kind: 'EXACT_HASH' | 'ORDER_FALLBACK' | 'MISS';
  healed_fields?: string[];
  is_miss: boolean;
  peer_addr?: string;
}

export type CSGReplayStrategy =
  | 'PASSTHROUGH'
  | 'CLIENT_INJECT'
  | 'PUB_STUB'
  | 'MOCK_SERVER';

export interface CSGResolution {
  service_name: string;
  strategy: string;
  original_addr?: string;
  effective_addr?: string;
  resolved_by_sub?: boolean;
  pub_stub_active?: boolean;
  mesh_rewritten?: boolean;
  note?: string;
}

export interface CSGSandboxReport {
  strategy: CSGReplayStrategy;
  csg_addr?: string;
  mock_csg_addr?: string;
  resolutions?: CSGResolution[];
  sandbox_subscribed_svcs?: string[];
  unpublished_stubs?: string[];
  mesh_rewrites?: number;
  real_csg_leak_count: number;
  cleanup_completed: boolean;
  error_code?: string;
  error_message?: string;
}

export interface ReplayTask {
  id: string;
  task_no: string;
  name: string;
  session_id: string;
  source_process_id: string;
  target_process_id: string;
  agent_id: string;
  lifecycle_state: 'PENDING' | 'ARMED' | 'INJECTING' | 'COMPLETED' | 'CANCELLED' | 'FAILED';
  result_status: 'PASS' | 'FAILED' | 'MOCK_MISS' | 'TIMEOUT' | 'ERROR';
  total_requests: number;
  passed_requests: number;
  failed_requests: number;
  mock_miss_requests: number;
  timeout_requests: number;
  error_requests: number;
  extra_ignore_paths?: string[];
  mock_events?: MockTraceEvent[];
  csg_strategy?: CSGReplayStrategy;
  csg_sandbox_report?: CSGSandboxReport;
  created_at?: string;
  updated_at?: string;
}

export interface ReplayItem {
  id: string;
  task_id: string;
  record_id: string;
  sno: string;
  service_name: string;
  operation: string;
  result_status: 'PASS' | 'FAILED' | 'MOCK_MISS' | 'TIMEOUT' | 'ERROR';
  latency_us: number;
  actual_status_code?: number;
  baseline_status_code?: number;
  header_diff_passed: boolean;
  field_diffs?: FieldDiff[];
  mock_trace?: MockTraceEvent;
  created_at?: string;
}

export interface ReplayItemDetailDTO {
  item: ReplayItem;
  baseline_rsp_anatomy: FrameSideAnatomyDTO;
  actual_rsp_anatomy?: FrameSideAnatomyDTO;
}
