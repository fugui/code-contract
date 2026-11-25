/** M32 架构收件箱 (Discovery Inbox) 前后端契约类型，id 均为 number */

export type InboxItemKind = 'UNCLAIMED' | 'MULTI_HOST' | 'PLACEMENT_DRIFT';

export interface InboxCountsDTO {
  unclaimed_count: number;
  multi_host_count: number;
  placement_drift_count: number;
  dep_suggestion_count: number;
  total_pending: number;
}

export interface InboxPlacementDTO {
  placement_id: number;
  arch_process_id: number;
  process_id: number;
  agent_id: number;
  arch_process_code: string;
  arch_process_name: string;
  agent_name: string;
  endpoints: string[];
  observed_transport: string;
  instance_count: number;
}

export interface InboxServiceItemDTO {
  service_id: number;
  service_name: string;
  proto_service_name: string;
  proto_suggested_name: string;
  kind: InboxItemKind;
  module_id?: number;
  module_name?: string;
  placements: InboxPlacementDTO[];
  candidate_arch_process_ids: number[];
}

export interface InboxProcessGroupDTO {
  arch_process_id: number;
  arch_process_code: string;
  arch_process_name: string;
  group_name: string;
  items: InboxServiceItemDTO[];
}

export interface CandidateConsumerModuleDTO {
  module_id: number;
  module_code: string;
  module_name: string;
}

export type DepSuggestionRecommendationSource = '' | 'SINGLE_MODULE' | 'STAGE2_TRAFFIC';

export interface DepSuggestionDTO {
  consumer_arch_process_id: number;
  consumer_arch_process_code: string;
  consumer_arch_process_name: string;
  service_id: number;
  service_name: string;
  provider_module_id: number;
  provider_module_name: string;
  candidate_consumer_modules: CandidateConsumerModuleDTO[];
  recommended_consumer_module_id?: number;
  recommendation_source: DepSuggestionRecommendationSource;
}

export interface ArchInboxResponseDTO {
  summary: InboxCountsDTO;
  process_groups: InboxProcessGroupDTO[];
  dep_suggestions: DepSuggestionDTO[];
  /** process_groups 总数（分页按进程分组） */
  total: number;
  page: number;
  page_size: number;
}

export interface ArchInboxQuery {
  kind?: InboxItemKind | 'DEP_SUGGESTION';
  arch_process_id?: number;
  search?: string;
  page?: number;
  page_size?: number;
}

export interface ClaimServiceItemInput {
  service_id: number;
  module_id: number;
  impl_key?: string;
}

export interface ClaimArchServicesResp {
  claimed_count: number;
  attribution_job_id?: number;
}

export interface SplitMultiImplImplementationInput {
  arch_process_id: number;
  module_id: number;
  impl_key: string;
}

export interface SplitMultiImplReq {
  source_service_id: number;
  definer_module_id: number;
  implementations: SplitMultiImplImplementationInput[];
}

export type UnclaimAction = 'UNCLAIMED' | 'MERGED' | 'DELETED';

export interface UnclaimArchServiceResp {
  action: UnclaimAction;
  service_id: number;
}

export type DependencyDecision = 'APPROVE' | 'IGNORE' | 'VIOLATION';

export interface DecideDependencySuggestionReq {
  consumer_arch_process_id: number;
  service_id: number;
  decision: DependencyDecision;
  consumer_module_id?: number;
  rationale?: string;
}

export interface DecideDependencySuggestionResp {
  decision: string;
  module_dependency_id?: number;
}

export type AttributionJobStatus = 'PENDING' | 'RUNNING' | 'DONE' | 'FAILED' | 'CANCELLED';

export interface AttributionJobDTO {
  id: number;
  job_type: string;
  status: AttributionJobStatus;
  processed: number;
  total: number;
  cursor_session_id: number;
  matched_count: number;
  unresolved_count: number;
  conflict_count: number;
  error: string;
  created_by: number;
  created_at: string;
  updated_at: string;
  service_names: string[];
}

/** SSE 事件 attribution_job.updated 的 data */
export interface AttributionJobEventDTO {
  job_id: number;
  trigger_type: string;
  target_id: number;
  status: string;
  processed: number;
  total: number;
  cursor_session_id: number;
  matched_count: number;
  unresolved_count: number;
  conflict_count: number;
  last_error?: string;
}
