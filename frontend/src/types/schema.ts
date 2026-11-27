export type ProtoCategory = 'BASE_LIB' | 'BUSINESS' | 'GATEWAY';

export interface ResolvedSchemaDependencyDTO {
  schema_id?: number | string;
  schema_name: string;
  version?: number;
  category?: string;
  matched_protos: string[];
  status: 'READY' | 'MISSING';
}

export interface ProtoPoolSummaryDTO {
  active_schema_count: number;
  active_base_lib_count: number;
  active_service_count: number;
  active_operation_count: number;
  claimed_service_count: number;
  orphan_service_count: number;
  claimed_ratio: number;
  unresolved_dep_count: number;
}

export type ProtoSourceType = 'UPLOAD_PB' | 'ONLINE_COMPILE' | 'GIT_SYNC' | 'GIT_MR_AUTO';

export interface ProtoSchemaLedgerItemDTO {
  id: string | number;
  name: string;
  version: number;
  version_tag?: string;
  category: ProtoCategory;
  source_type?: ProtoSourceType;
  git_repo_id?: number;
  source_bundle_id?: number;
  source_commit_sha?: string;
  source_mr_iid?: number | null;
  service_name?: string;
  file_name: string;
  file_sha256?: string;
  file_size: number;
  is_active: boolean;
  proto_file_count: number;
  service_count: number;
  operation_count: number;
  proto_files: string[];
  external_imports?: string[];
  description?: string;
  created_by?: number;
  created_by_name?: string;
  created_at?: string;
  updated_at?: string;
  version_count: number;
  latest_version: number;
  active_version?: number;
  claimed_service_count: number;
  claimed_ratio: number;
  resolved_dependencies: ResolvedSchemaDependencyDTO[];
}

// 兼容别名与已有代码引用
export interface ProtoSchema extends Partial<ProtoSchemaLedgerItemDTO> {
  id: string;
  name: string;
  version: number;
  is_active: boolean;
  operations_count?: number;
}

export interface ProtoSchemaLedgerListResponse {
  items: ProtoSchemaLedgerItemDTO[];
  pool_summary: ProtoPoolSummaryDTO;
  total: number;
  page: number;
  page_size: number;
}

export interface ProtoOperation {
  id: string;
  schema_id: string;
  proto_file: string;
  proto_package: string;
  proto_service: string;
  service_name: string;
  operation: string;
  req_message_full_name?: string;
  rsp_message_full_name?: string;
  client_streaming?: boolean;
  server_streaming?: boolean;
  is_active: boolean;
  diff_ignore_paths?: string[];
  created_at?: string;
}

export interface ConflictItem {
  service_name: string;
  operation: string;
  conflict_schema_name: string;
  conflict_version: number;
}

export interface SchemaConflict extends ConflictItem {}

export interface BrokenDependentItemDTO {
  schema_id: number;
  schema_name: string;
  version: number;
  reason: string;
}

export interface BreakingRuleViolation {
  rule: string;
  proto_file: string;
  symbol: string;
  message: string;
  service_name?: string;
}

export interface AffectedDependent {
  module_id: number;
  module_code: string;
  module_name: string;
  arch_process_code: string;
  owner_name: string;
}

export interface CompileProtoFileItem {
  path: string;
  content: string;
}

export interface CompileProtoSchemaPayload {
  name: string;
  version_tag?: string;
  category: ProtoCategory;
  include_dirs?: string[];
  files: CompileProtoFileItem[];
  auto_activate?: boolean;
  allow_breaking?: boolean;
  breaking_reason?: string;
  description?: string;
}

export interface CompileDiagnosticDTO {
  file: string;
  line: number;
  col: number;
  message: string;
}

export interface ProtoDependencyNodeDTO {
  id: number | string;
  name: string;
  version: number;
  category: ProtoCategory;
  source_type?: ProtoSourceType;
  proto_file_count: number;
  service_count: number;
  operation_count: number;
  is_active: boolean;
}

export interface ProtoDependencyEdgeDTO {
  from_schema_id: number | string;
  to_schema_id: number | string;
  matched_protos: string[];
}

export interface ProtoDependenciesGraphDTO {
  pool_summary?: ProtoPoolSummaryDTO;
  nodes: ProtoDependencyNodeDTO[];
  edges: ProtoDependencyEdgeDTO[];
}

export interface ConflictOrBrokenResponse {
  error: string;
  code?: string;
  conflicts?: ConflictItem[];
  broken_dependents?: BrokenDependentItemDTO[];
  breaking_rules?: BreakingRuleViolation[];
  affected_dependents?: AffectedDependent[];
  diagnostics?: CompileDiagnosticDTO[];
  missing_imports?: string[];
  service_name?: string;
}

export interface ClaimedModuleDTO {
  module_id: number | string;
  module_name: string;
  subsystem_id?: number | string;
  subsystem_name?: string;
}

export interface SourceSchemaDTO {
  id: number | string;
  name: string;
  version: number;
  category: string;
  is_active: boolean;
}

export type { ServiceContractGroupDTO } from './schema_tree';

// ==========================================
// M23: 活体接口参考文档中心与只读源码浏览器 DTO (F-18 / F-28)
// ==========================================

export interface ErrorCodeDocDTO {
  code: string;
  http_or_cros_code: string;
  description: string;
}

export interface EnumValueDocDTO {
  name: string;
  number: number;
  comment: string;
  is_deprecated: boolean;
  deprecation_reason?: string;
  start_line: number;
}

export interface EnumDocDTO {
  full_name: string;
  short_name: string;
  proto_file: string;
  source_file_id?: number;
  start_line: number;
  end_line: number;
  comment: string;
  is_deprecated: boolean;
  deprecation_reason?: string;
  values: EnumValueDocDTO[];
}

export interface FieldDocNodeDTO {
  name: string;
  json_name?: string;
  number: number;
  type: string;
  type_full_name?: string;
  proto_file?: string;
  start_line?: number;
  label: string;
  is_map?: boolean;
  map_key_type?: string;
  map_value_type?: string;
  oneof_group?: string;
  oneof_comment?: string;
  comment: string;
  is_deprecated?: boolean;
  deprecation_reason?: string;
  enum_values?: EnumValueDocDTO[];
  truncated?: boolean;
  truncated_reason?: string;
  children?: FieldDocNodeDTO[];
}

export interface MessageDocMetaDTO {
  full_name: string;
  proto_file: string;
  start_line: number;
  end_line: number;
  comment: string;
  fields: FieldDocNodeDTO[];
  truncated?: boolean;
}

export interface GoldenSampleDTO {
  suite_id: number;
  suite_code: string;
  suite_name: string;
  item_id: number;
  order_no: number;
  decode_status: 'DECODED_JSON' | 'DECODE_ERROR' | 'NO_SCHEMA' | string;
  req_json?: unknown;
  rsp_json?: unknown;
  remark?: string;
}

export interface MethodDocDTO {
  operation: string;
  full_name: string;
  proto_file: string;
  source_file_id?: number;
  start_line: number;
  end_line: number;
  summary: string;
  leading_comments: string;
  trailing_comments: string;
  is_deprecated: boolean;
  deprecation_reason: string;
  owner_tag: string;
  error_codes: ErrorCodeDocDTO[];
  req_message_full_name: string;
  rsp_message_full_name: string;
  client_streaming: boolean;
  server_streaming: boolean;
  req_fields: FieldDocNodeDTO[];
  rsp_fields: FieldDocNodeDTO[];
  default_req_json: unknown;
  default_rsp_json: unknown;
  golden_samples: GoldenSampleDTO[];
}

export interface ContractDefinerDTO {
  contract_id: number;
  service_name: string;
  definer_module_id: number;
  module_code: string;
  module_name: string;
  subsystem_id: number;
  subsystem_name: string;
  provide_mode: string;
  impl_key_label: string;
}

export interface ServiceImplementationDTO {
  service_id: number;
  module_id: number;
  module_code: string;
  module_name: string;
  subsystem_id: number;
  subsystem_name: string;
  arch_process_code: string;
  owner_name: string;
  impl_key: string;
  is_custom_service_name: boolean;
  service_name: string;
}

export interface ServiceConsumerDTO {
  module_id: number;
  module_code: string;
  module_name: string;
  subsystem_id: number;
  subsystem_name: string;
  arch_process_code: string;
  dep_type: string;
}

export interface ServiceContractLineageDTO {
  claim_status: 'CONTRACT' | 'SINGLE_IMPL' | 'UNCLAIMED' | string;
  definer?: ContractDefinerDTO;
  implementations: ServiceImplementationDTO[];
  consumers: ServiceConsumerDTO[];
}

export interface ProtoDocTreeNodeDTO {
  node_key: string;
  node_type: 'SUBSYSTEM' | 'SCHEMA' | 'PACKAGE' | 'SERVICE' | 'METHOD' | string;
  label: string;
  subsystem_id?: number;
  schema_id?: number;
  schema_name?: string;
  category?: string;
  service_name?: string;
  operation?: string;
  summary?: string;
  is_deprecated?: boolean;
  method_count?: number;
  deprecated_count?: number;
  has_source_info: boolean;
  children?: ProtoDocTreeNodeDTO[];
}

export interface ProtoDocTreeResponse {
  items: ProtoDocTreeNodeDTO[];
  total_services: number;
  total_methods: number;
  deprecated_count: number;
}

export interface ProtoServiceDocDetailDTO {
  schema_id: number;
  schema_name: string;
  schema_version: number;
  schema_category: string;
  source_type: string;
  has_source_info: boolean;
  package_name: string;
  service_name: string;
  short_name: string;
  proto_file: string;
  source_file_id?: number;
  start_line: number;
  end_line: number;
  file_comments: string;
  summary: string;
  leading_comments: string;
  trailing_comments: string;
  is_deprecated: boolean;
  deprecation_reason: string;
  owner_tag: string;
  error_codes: ErrorCodeDocDTO[];
  methods: MethodDocDTO[];
  enums: EnumDocDTO[];
  referenced_types: MessageDocMetaDTO[];
  lineage: ServiceContractLineageDTO;
}

export interface ProtoSymbolAnchorDTO {
  symbol_kind: string;
  symbol_full_name: string;
  start_line: number;
  end_line: number;
}

export interface ReconstructedProtoFileDTO {
  file_id: number;
  schema_id: number;
  schema_name: string;
  schema_version: number;
  file_path: string;
  package_name: string;
  content: string;
  line_count: number;
  has_source_info: boolean;
  source_origin: string;
  symbol_anchors: ProtoSymbolAnchorDTO[];
}

export interface ProtoSchemaSourcesResponse {
  schema_id: number;
  schema_name: string;
  schema_version: number;
  source_type: string;
  has_source_info: boolean;
  source_origin: string;
  files: ReconstructedProtoFileDTO[];
}

export interface ProtoSourceViolationDTO {
  rule_code: string;
  severity: 'ERROR' | 'WARNING' | 'INFO' | string;
  message: string;
  line: number;
  file_line?: number;
  column?: number;
}

export interface ProtoFileDTO {
  id: number;
  schema_id: number;
  git_repo_id?: number;
  git_repo_name?: string;
  bundle_id?: number;
  bundle_name?: string;
  target_schema_name: string;
  category: string;
  file_path: string;
  repo_file_path?: string;
  package_name: string;
  latest_version: number;
  latest_commit_sha?: string;
  content_sha256?: string;
  is_active_schema: boolean;
  has_source_info: boolean;
  is_deleted?: boolean;
  pipeline_status?: 'COLLECTED' | 'NORMALIZED' | 'QUARANTINED' | string;
  status_reason?: string;
  normalizer_version?: string;
  clean_rel_path?: string;
  is_uservice_source?: boolean;
  in_service_closure?: boolean;
  is_harvested_base?: boolean;
  auto_fix_count?: number;
  is_external_copy?: boolean;
  raw_blob_sha?: string;
  source_origin: string;
  line_count: number;
  updated_at: string;
}

export interface ProtoFileVersionDTO {
  id?: number;
  proto_file_id?: number;
  schema_id: number;
  version: number;
  version_tag: string;
  commit_sha: string;
  commit_message?: string;
  content_sha256: string;
  source_type: string;
  author_name: string;
  author_email?: string;
  summary: string;
  committed_at?: string;
  created_at: string;
  line_count: number;
  is_active: boolean;
}

export interface ProtoFileVersionSummaryDTO {
  id?: number;
  proto_file_id?: number;
  schema_id: number;
  version: number;
  version_tag: string;
  commit_sha: string;
  commit_message?: string;
  content_sha256: string;
  source_type: string;
  author_name: string;
  author_email?: string;
  summary: string;
  is_active: boolean;
  committed_at?: string;
  created_at: string;
}

export interface ProtoFileContentResponse {
  file: ProtoFileDTO;
  version: ProtoFileVersionDTO;
  content: string;
  raw_content?: string;
  line_map?: Record<string, number> | null;
  violations: ProtoSourceViolationDTO[];
  symbol_anchors: ProtoSymbolAnchorDTO[];
}

export interface ProtoFileDiffResponse {
  file: ProtoFileDTO;
  from_version: ProtoFileVersionDTO;
  to_version: ProtoFileVersionDTO;
  unified_diff: string;
  added_lines: number;
  deleted_lines: number;
}

export type RuleSeverity = 'ERROR' | 'WARNING' | 'INFO';
export type RuleCategory = 'NAMING' | 'COMMENT' | 'PACKAGE' | 'STYLE' | 'DUPLICATE' | 'VOCAB';
export type RuleTargetScope = 'PACKAGE' | 'SERVICE' | 'RPC' | 'MESSAGE' | 'FIELD' | 'ENUM' | 'ENUM_VALUE' | 'ALL';
export type RuleCheckMode = 'REGEX' | 'STRUCTURE' | 'COMMENT' | 'AST_DUPLICATE' | 'VOCAB';
export type VocabSource = 'MANUAL' | 'BUILTIN_SEED' | 'LLM_SUGGESTED';
export type ReportStatus = 'PASSED' | 'WARN' | 'FAILED';
export type LLMStatus = 'SUCCEEDED' | 'HIT_CACHE' | 'TIMEOUT_FALLBACK' | 'ERROR_FALLBACK' | 'SKIPPED';

export interface RuleExemptionConfig {
  exclude_paths?: string[];
  exclude_packages?: string[];
  exclude_categories?: string[];
}

export interface CheckRuleDTO {
  id: number;
  rule_code: string;
  name: string;
  category: RuleCategory | string;
  target_scope: RuleTargetScope | string;
  severity: RuleSeverity | string;
  check_mode: RuleCheckMode | string;
  pattern: string;
  description: string;
  suggestion_template: string;
  exemption_rule: RuleExemptionConfig;
  is_builtin: boolean;
  is_enabled: boolean;
  updated_by?: number;
  created_at: string;
  updated_at: string;
}

export interface CheckRuleListResponse {
  items: CheckRuleDTO[];
  total: number;
  page: number;
  page_size: number;
}

export interface CreateCheckRulePayload {
  rule_code: string;
  name: string;
  category: string;
  target_scope: string;
  severity?: RuleSeverity;
  check_mode?: string;
  pattern?: string;
  description?: string;
  suggestion_template?: string;
  exemption_rule?: RuleExemptionConfig;
  is_enabled?: boolean;
}

export interface UpdateCheckRulePayload {
  name?: string;
  category?: string;
  target_scope?: string;
  severity?: RuleSeverity;
  check_mode?: string;
  pattern?: string;
  description?: string;
  suggestion_template?: string;
  exemption_rule?: RuleExemptionConfig;
  is_enabled?: boolean;
}

export interface VocabularyTermDTO {
  id: number;
  term: string;
  description_en: string;
  description_zh: string;
  aliases: string[];
  similar_terms: string[];
  domain: string;
  source: VocabSource | string;
  is_approved: boolean;
  created_by?: number;
  created_at: string;
  updated_at: string;
}

export interface VocabularyTermListResponse {
  items: VocabularyTermDTO[];
  total: number;
  page: number;
  page_size: number;
}

export interface CreateVocabularyTermPayload {
  term: string;
  description_en?: string;
  description_zh: string;
  aliases?: string[];
  similar_terms?: string[];
  domain?: string;
  source?: 'MANUAL';
  is_approved?: boolean;
}

export interface UpdateVocabularyTermPayload {
  description_en?: string;
  description_zh?: string;
  aliases?: string[];
  similar_terms?: string[];
  domain?: string;
  is_approved?: boolean;
}

export interface DuplicateMemberDTO {
  proto_file: string;
  package: string;
  message: string;
  file_line: number;
  field_cnt: number;
}

export interface DuplicateGroupDTO {
  cluster_hash: string;
  similarity: number;
  suggestion: string;
  members: DuplicateMemberDTO[];
}

export interface CheckViolationDTO {
  id: number;
  report_id: number;
  rule_id?: number;
  rule_code: string;
  severity: RuleSeverity | string;
  proto_file: string;
  symbol_path: string;
  file_line: number;
  file_column: number;
  message: string;
  suggestion: string;
  suggestion_source: string;
  duplicate_cluster_hash?: string;
  duplicate_similarity?: number;
}

export interface CheckReportDTO {
  id: number;
  git_repo_id?: number;
  bundle_id?: number;
  proto_file_id?: number;
  schema_id?: number;
  mr_check_run_id?: number;
  trigger_source: string;
  status: ReportStatus | string;
  error_count: number;
  warning_count: number;
  info_count: number;
  duplicate_group_count: number;
  llm_status: LLMStatus | string;
  duration_ms: number;
  triggered_by?: number;
  created_at: string;
  violations?: CheckViolationDTO[];
  duplicate_groups?: DuplicateGroupDTO[];
}

export interface CheckReportListResponse {
  items: CheckReportDTO[];
  total: number;
  page: number;
  page_size: number;
}

export interface TriggerCheckScanPayload {
  schema_id?: number;
  schema_ids?: number[];
  proto_file_id?: number;
  proto_file_ids?: number[];
  include_duplicates?: boolean;
  include_llm?: boolean;
}

// =====================================================================
// M25: Git 仓库、Monorepo 多包 (Bundles) 与只读一致性对账契约类型
// =====================================================================

export type GitProviderType = 'GITLAB' | 'GITHUB' | 'GITEA' | 'CUSTOM_GIT';
export type GitAuthType = 'NONE' | 'TOKEN' | 'BASIC' | 'SSH_KEY';
export type GitSyncMode = 'WEBHOOK' | 'POLLING' | 'MANUAL';
export type GitSyncStatus = 'IDLE' | 'RUNNING' | 'SUCCESS' | 'FAILED';
export type MRAutoPublishMode = 'OFF' | 'DRAFT_ONLY' | 'AUTO_ACTIVATE';

export interface GitProtoBundleDTO {
  id: number;
  git_repo_id: number;
  bundle_name: string;
  target_schema_name: string;
  category: ProtoCategory | string;
  subdir_path: string;
  include_dirs: string[];
  entry_files: string[];
  file_globs: string[];
  exclude_globs: string[];
  auto_compile: boolean;
  auto_activate: boolean;
  mr_auto_publish: MRAutoPublishMode | string;
  lint_on_sync: boolean;
  last_sync_commit: string;
  last_sync_at?: string;
  last_schema_id?: number | null;
  description: string;
  created_at: string;
  updated_at: string;
}

export type GitRepoProtoStatus =
  | 'PENDING'
  | 'HAS_PROTO'
  | 'NO_PROTO'
  | 'NO_LOCAL_CACHE'
  | 'EXCLUDED'
  | 'SYNC_FAILED';

export interface RegexRewriteRuleDTO {
  id: string;
  pattern: string;
  replacement: string;
  multiline?: boolean;
}

export interface RepoNormalizeConfigDTO {
  disabled_rules?: string[];
  regex_rewrites?: RegexRewriteRuleDTO[];
  import_rewrites?: Record<string, string>;
  exempt_files?: string[];
  quarantined_files?: string[];
}

export interface GitRepoDTO {
  id: number;
  platform_repo_id: number;
  source_mode?: 'PLATFORM_SHARED' | string;
  sync_strategy?: 'FETCH_AND_READ' | 'LOCAL_CACHE_ONLY' | string;
  proto_status?: GitRepoProtoStatus | string;
  sync_enabled?: boolean;
  subsystem_slug?: string;
  export_target_subsystem?: string;
  export_target_repo_dir?: string;
  effective_export_dir?: string;
  has_export_override?: boolean;
  strip_prefixes?: string[];
  path_rewrites?: Record<string, string>;
  repo_normalize_config?: RepoNormalizeConfigDTO;
  collect_globs?: string[];
  exclude_globs?: string[];
  service_group?: string;
  department_id?: number;
  department_name?: string;
  owner_id?: number;
  owner_name?: string;
  is_platform_active?: boolean;
  resolved_branch?: string;
  last_fetched_at?: string | null;
  ref_commit_time?: string | null;
  last_collected_commit?: string;
  is_stale?: boolean;
  whitelist_count?: number;
  normalized_count?: number;
  quarantined_count?: number;
  external_copy_count?: number;
  name: string;
  provider: GitProviderType | string;
  repo_url: string;
  api_base_url: string;
  default_branch: string;
  auth_type: GitAuthType | string;
  has_credentials: boolean;
  credential_hint: string;
  has_webhook_secret: boolean;
  sync_mode: GitSyncMode | string;
  poll_interval_sec: number;
  auto_compile: boolean;
  auto_activate: boolean;
  lint_on_sync: boolean;
  last_sync_at?: string;
  last_sync_commit: string;
  last_sync_status: GitSyncStatus | string;
  last_sync_error: string;
  description: string;
  created_by: number;
  created_at: string;
  updated_at: string;
  bundles: GitProtoBundleDTO[];
}

export interface GitRepoBundleInput {
  bundle_name: string;
  target_schema_name: string;
  category?: ProtoCategory;
  subdir_path?: string;
  include_dirs?: string[];
  entry_files?: string[];
  file_globs?: string[];
  exclude_globs?: string[];
  auto_compile?: boolean;
  auto_activate?: boolean;
  mr_auto_publish?: MRAutoPublishMode;
  lint_on_sync?: boolean;
  description?: string;
}

export interface UpdateGitRepoPayload {
  sync_strategy?: GitSyncStrategy | string;
  sync_enabled?: boolean;
  sync_mode?: GitSyncMode;
  poll_interval_sec?: number;
  auto_compile?: boolean;
  auto_activate?: boolean;
  lint_on_sync?: boolean;
  description?: string;
}

export type GitConsistencyItemStatus = 'in_sync' | 'modified' | 'remote_only' | 'db_only';

export interface GitConsistencyItemDTO {
  bundle_id: number;
  bundle_name: string;
  target_schema_name: string;
  file_path: string;
  status: GitConsistencyItemStatus | string;
  remote_sha256?: string;
  db_sha256?: string;
  db_version?: number;
}

export interface GitConsistencySummaryDTO {
  in_sync: number;
  modified: number;
  remote_only: number;
  db_only: number;
}

export interface GitConsistencyReportDTO {
  repo_id: number;
  repo_name: string;
  bundle_id?: number | null;
  branch: string;
  commit_sha: string;
  head_commit_sha?: string;
  in_sync: boolean;
  remote_only_files: string[];
  db_only_files: string[];
  modified_files: string[];
  summary: GitConsistencySummaryDTO;
  items: GitConsistencyItemDTO[];
  checked_at: string;
}

export interface TriggerGitSyncResponse {
  repo_id: number;
  bundle_id?: number;
  branch: string;
  status: GitSyncStatus | string;
}

export interface GitRepoBranchesResponse {
  default_branch: string;
  branches: string[];
}

export interface GitSyncCompletedEventPayload {
  repo_id: number;
  repo_name?: string;
  branch?: string;
  commit_sha?: string;
  status: GitSyncStatus | string;
  files_updated?: number;
  compiled_schema_id?: number;
  message?: string;
}

// =====================================================================
// M26: Git MR / PR 四合一契约门禁与站内收件箱类型契约
// =====================================================================

export type MREventPhase = 'MR_GATE' | 'MAIN_MERGE';
export type MRCheckStatus = 'PENDING' | 'RUNNING' | 'PASSED' | 'WARN' | 'FAILED' | 'OVERRIDDEN';

export interface MRCheckRunDTO {
  id: number;
  git_repo_id: number;
  git_repo_name?: string;
  provider?: string;
  bundle_id?: number | null;
  bundle_name?: string;
  target_schema_name?: string;
  category?: ProtoCategory | string;
  mr_iid: number;
  event_phase: MREventPhase | string;
  mr_title: string;
  mr_url: string;
  source_branch: string;
  target_branch: string;
  head_commit_sha: string;
  target_head_sha: string;
  author_username: string;
  author_email: string;
  status: MRCheckStatus | string;
  normalize_failed?: boolean;
  compile_passed: boolean;
  lint_error_count: number;
  lint_warning_count: number;
  duplicate_group_count: number;
  breaking_change_count: number;
  affected_modules_count: number;
  changed_proto_files: string[];
  compile_error_text: string;
  breaking_details: BreakingRuleViolation[];
  affected_modules: AffectedDependent[];
  check_report_id?: number | null;
  remote_comment_id: string;
  attempt_count: number;
  overridden_by?: number | null;
  overridden_by_name?: string;
  override_reason: string;
  overridden_at?: string | null;
  duration_ms: number;
  started_at?: string | null;
  completed_at?: string | null;
  created_at: string;
  updated_at: string;
}

export interface MRCheckRunDetailDTO extends MRCheckRunDTO {
  violations: CheckViolationDTO[];
  duplicate_groups: DuplicateGroupDTO[];
}

export interface MRCheckRunListResponse {
  items: MRCheckRunDTO[];
  total: number;
  page: number;
  page_size: number;
}

export type NotificationCategory =
  | 'BREAKING_ALERT'
  | 'MR_GATE_BLOCKED'
  | 'MR_GATE_PASSED'
  | 'DUPLICATE_WARN'
  | 'BASE_LIB_IMPACT'
  | 'SCHEMA_PUBLISHED';

export type NotificationSeverity = 'INFO' | 'WARNING' | 'CRITICAL';

export interface NotificationDTO {
  id: number;
  recipient_user_id: number;
  category: NotificationCategory | string;
  severity: NotificationSeverity | string;
  title: string;
  body_markdown: string;
  link_url: string;
  source_type: string;
  source_id: number;
  metadata_json?: Record<string, unknown>;
  is_read: boolean;
  read_at?: string | null;
  external_channel: string;
  email_status: string;
  email_error: string;
  created_at: string;
}

export interface NotificationListResponse {
  items: NotificationDTO[];
  total: number;
  unread_count: number;
  page: number;
  page_size: number;
}

export interface UnreadCountResponse {
  unread_count: number;
}

// =====================================================================
// ADR-26 r4 & ADR-27: 平台唯一真值源、500+ 同步作业、方案 A 治理与双视图懒加载类型契约
// =====================================================================

export type GitSourceMode = 'PLATFORM_SHARED';
export type GitSyncStrategy = 'FETCH_AND_READ' | 'LOCAL_CACHE_ONLY';
export type SyncJobScope = 'ALL' | 'DEPARTMENT' | 'SERVICE_GROUP' | 'FAILED_ONLY' | 'SELECTED';
export type SyncCachePolicy = 'CACHE_ONLY' | 'FETCH_MISSING';
export type SyncJobStatus = 'RUNNING' | 'COMPLETED' | 'PARTIAL_FAILED' | 'FAILED';

export interface SyncJobErrorItemDTO {
  git_repo_id: number;
  repo_name: string;
  error: string;
}

export interface ProtoSyncJobDTO {
  id: number;
  scope: SyncJobScope | string;
  cache_policy: SyncCachePolicy | string;
  status: SyncJobStatus | string;
  total_repos: number;
  processed_repos: number;
  has_proto_count: number;
  no_proto_count: number;
  no_cache_count: number;
  failed_count: number;
  skipped_unchanged_count: number;
  error_summary?: SyncJobErrorItemDTO[];
  triggered_by: string;
  started_at: string;
  finished_at?: string | null;
}

export interface SyncJobProgressEventPayload {
  job_id: number;
  scope: string;
  cache_policy: string;
  status: string;
  total_repos: number;
  processed_repos: number;
  has_proto_count: number;
  no_proto_count: number;
  no_cache_count: number;
  failed_count: number;
  skipped_unchanged_count: number;
  error_summary?: SyncJobErrorItemDTO[];
  started_at: string;
  finished_at?: string | null;
}

export interface GitReposSyncSummaryDTO {
  platform_active_total: number;
  aligned_total: number;
  pending_count: number;
  has_proto_count: number;
  no_proto_count: number;
  no_local_cache_count: number;
  excluded_count: number;
  sync_failed_count: number;
  shared_write_enabled: boolean;
  service_groups: string[];
  latest_job?: ProtoSyncJobDTO | null;
}

export interface SyncAllGitReposPayload {
  scope?: SyncJobScope | string;
  cache_policy?: SyncCachePolicy | string;
  department_id?: number;
  service_group?: string;
  repo_ids?: number[];
  force_recompile?: boolean;
  sync?: boolean;
}

export interface BuiltinRuleMetaDTO {
  id: string;
  code: string;
  name: string;
  description: string;
  default_enabled: boolean;
}

export interface SharedBaseSourceSpecDTO {
  path_hint: string;
  priority: number;
  include_globs?: string[];
  description?: string;
}

export interface PoolReconcileConfigDTO {
  closure_only?: boolean;
  enable_r7_auto_fix?: boolean;
  max_r7_rounds?: number;
  enable_r8_quarantine?: boolean;
  max_r8_rounds?: number;
}

export interface PoolStripCollisionDTO {
  post_strip_path: string;
  pre_strip_paths: string[];
  repo_ids: number[];
  repo_names: string[];
}

export interface GlobalGovernanceSectionDTO {
  collect_globs?: string[];
  exclude_globs?: string[];
  exclude_subsystems?: string[];
  exclude_repo_patterns?: string[];
  enabled_rules?: string[];
  default_syntax?: 'auto' | 'proto2' | 'proto3' | string;
  strip_pg_segment?: boolean;
  import_rewrites?: Record<string, string>;
  implicit_symbol_imports?: Record<string, string>;
  regex_rewrites?: RegexRewriteRuleDTO[];
  shared_base_sources?: SharedBaseSourceSpecDTO[];
  pool_reconcile?: PoolReconcileConfigDTO;
}

export interface RepoGovernanceSpecDTO {
  platform_repo_id?: number;
  sync_enabled?: boolean;
  sync_strategy?: string;
  export_target_subsystem?: string;
  export_target_repo_dir?: string;
  strip_prefixes?: string[];
  path_rewrites?: Record<string, string>;
  collect_globs?: string[];
  exclude_globs?: string[];
  extra_exclude_globs?: string[];
  disabled_rules?: string[];
  regex_rewrites?: RegexRewriteRuleDTO[];
  import_rewrites?: Record<string, string>;
  exempt_files?: string[];
  quarantined_files?: string[];
}

export interface GovernanceConfigDocDTO {
  schema_version: string;
  revision: number;
  global: GlobalGovernanceSectionDTO;
  repos?: Record<string, RepoGovernanceSpecDTO>;
}

export interface ProtoGovernanceConfigResponse {
  doc: GovernanceConfigDocDTO;
  revision: number;
  config_sha256: string;
  builtin_rules: BuiltinRuleMetaDTO[];
}

export interface ExportTargetCollisionDTO {
  target_subsystem: string;
  target_repo_dir: string;
  repo_ids: number[];
  repo_names: string[];
}

export interface IntraRepoPathCollisionDTO {
  mapped_path: string;
  source_paths: string[];
}

export interface DryRunPathMappingDTO {
  raw_path: string;
  clean_path: string;
  raw_syntax_ok: boolean;
  clean_syntax_ok: boolean;
  status: string;
  status_reason?: string;
  applied_rules?: string[];
  line_preserving: boolean;
  unified_diff?: string;
}

export interface RepoGovernanceDryRunResultDTO {
  git_repo_id: number;
  repo_name: string;
  before_target_dir: string;
  after_target_dir: string;
  global_collisions?: ExportTargetCollisionDTO[];
  path_collisions?: IntraRepoPathCollisionDTO[];
  total_files: number;
  raw_syntax_pass_count: number;
  clean_syntax_pass_count: number;
  quarantined_count: number;
  raw_syntax_pass_rate: number;
  clean_syntax_pass_rate: number;
  files: DryRunPathMappingDTO[];
}

export interface GlobalGovernanceDryRunResultDTO {
  schema_valid: boolean;
  validation_error?: string;
  global_collisions?: ExportTargetCollisionDTO[];
  pool_strip_collisions?: PoolStripCollisionDTO[];
  affected_repos: RepoGovernanceDryRunResultDTO[];
}

export interface UpdateGitRepoGovernancePayload {
  sync_enabled?: boolean;
  sync_strategy?: GitSyncStrategy | string;
  export_target_subsystem?: string;
  export_target_repo_dir?: string;
  strip_prefixes?: string[];
  path_rewrites?: Record<string, string>;
  collect_globs?: string[];
  exclude_globs?: string[];
  disabled_rules?: string[];
  regex_rewrites?: RegexRewriteRuleDTO[];
  import_rewrites?: Record<string, string>;
  exempt_files?: string[];
  quarantined_files?: string[];
}

export interface ApplyGovernanceResponse {
  revision: number;
  config_sha256: string;
  processed_count: number;
  succeeded_count: number;
  failed_count: number;
  errors?: Record<string, string>;
}

export interface BaselineReportRepoItemDTO {
  git_repo_id: number;
  repo_name: string;
  subsystem_slug?: string;
  proto_status?: string;
  source_mode: string;
  sync_strategy?: string;
  last_sync_status: string;
  whitelist_files: number;
  normalized_files: number;
  quarantined_files: number;
  external_copies?: number;
  external_copy_files?: number;
  compiled_bundles: number;
  bundle_count?: number;
  total_bundles?: number;
  compile_errors?: string[];
}

export interface BaselineReportDTO {
  generated_at?: string;
  total_repos: number;
  has_proto_repos?: number;
  no_proto_repos?: number;
  no_cache_repos?: number;
  excluded_repos?: number;
  failed_repos?: number;
  whitelist_files: number;
  collected_files?: number;
  normalized_files: number;
  quarantined_files: number;
  external_copy_files?: number;
  syntax_pass_rate: number;
  total_bundles?: number;
  compiled_bundles?: number;
  compile_pass_rate: number;
  error_distribution: Record<string, number>;
  repos: BaselineReportRepoItemDTO[];
}

export interface ArchAssignmentSuggestionDTO {
  git_repo_id: number;
  repo_name: string;
  platform_repo_id: number;
  service_group: string;
  recommended_subsystem_id?: number | null;
  recommended_subsystem?: string;
  recommended_module_id?: number | null;
  recommended_module?: string;
  confidence: 'HIGH' | 'MEDIUM' | 'LOW' | string;
  reason: string;
}

export interface ProtoTreeRepoGroupDTO {
  subsystem_slug: string;
  raw_subsystem_slug?: string;
  export_target_repo_dir?: string;
  git_repo_id: number;
  git_repo_name: string;
  source_mode: string;
  sync_strategy: string;
  is_stale: boolean;
  last_sync_at?: string | null;
  total_count?: number;
  normalized_count?: number;
  quarantined_count?: number;
  files: ProtoFileDTO[];
}

export interface ProtoTreePackageGroupDTO {
  package_name: string;
  total_count?: number;
  normalized_count?: number;
  quarantined_count?: number;
  files: ProtoFileDTO[];
}

export type ProtoFilesTreeResponse =
  | {
      view: 'subsystem';
      total: number;
      summary_only?: boolean;
      groups: ProtoTreeRepoGroupDTO[];
    }
  | {
      view: 'package';
      total: number;
      summary_only?: boolean;
      groups: ProtoTreePackageGroupDTO[];
    };

export interface ProtoImportOwnerDTO {
  id: number;
  logical_path: string;
  owner_git_repo_id: number;
  owner_bundle_id: number;
  source: 'MANUAL' | 'BASE_LIB' | 'AUTO' | string;
  updated_by: string;
  updated_at: string;
}

export interface ProtoImportOwnerListResponse {
  items: ProtoImportOwnerDTO[];
  total: number;
  page: number;
  page_size: number;
}

export interface ImportCandidateDTO {
  git_repo_id: number;
  repo_name: string;
  bundle_id: number;
  bundle_name: string;
  category: string;
  logical_path: string;
  repo_file_path: string;
}

export interface ImportAmbiguityItemDTO {
  logical_path: string;
  candidates: ImportCandidateDTO[];
}

export interface SymbolConflictCandidateDTO {
  proto_file_id: number;
  git_repo_id: number;
  repo_name: string;
  bundle_id: number;
  bundle_name: string;
  category: string;
  logical_path: string;
  repo_file_path: string;
  content_sha256: string;
  is_external_copy: boolean;
}

export interface SymbolConflictItemDTO {
  symbol_kind: string;
  symbol_full_name: string;
  content_matches: boolean;
  candidates: SymbolConflictCandidateDTO[];
}

export interface ProtoConflictsReportDTO {
  import_ambiguities: ImportAmbiguityItemDTO[];
  symbol_conflicts: SymbolConflictItemDTO[];
}

export interface ResolveProtoConflictInput {
  kind: 'IMPORT' | 'SYMBOL';
  logical_path?: string;
  symbol_full_name?: string;
  owner_git_repo_id: number;
  owner_bundle_id: number;
}

// =====================================================================
// ADR-28: 全池 Stage C+ 协调、R7 自愈、R8 根因隔离与 Workspace 导出
// =====================================================================

export interface PoolConflictCandidateDTO {
  proto_file_id: number;
  git_repo_id: number;
  repo_name: string;
  bundle_id: number;
  subsystem_slug: string;
  repo_file_path: string;
  clean_rel_path: string;
  content_sha256: string;
  unique_symbols?: string[];
  is_winner: boolean;
}

export interface PoolConflictEntryDTO {
  logical_path: string;
  reason: string;
  winner_repo_name: string;
  winner_file_id: number;
  content_identical: boolean;
  symbol_diff?: Record<string, string[]>;
  candidates: PoolConflictCandidateDTO[];
}

export interface PoolHarvestedEntryDTO {
  logical_path: string;
  source_abs_path: string;
  source_priority: number;
  triggered_by: 'CHANNEL_A' | 'IMPORT' | 'SYMBOL' | string;
}

export interface PoolAutoFixEntryDTO {
  file_path: string;
  repo_name: string;
  fix_kind: 'fix(a)' | 'fix(b)' | 'fix(c)' | 'variant_swap' | string;
  old_import?: string;
  new_import: string;
  symbol?: string;
  round: number;
  repo_id?: number;
  bundle_id?: number;
}

export interface PoolQuarantinedEntryDTO {
  logical_path: string;
  repo_name: string;
  repo_file_path: string;
  reason: string;
  error_count: number;
  is_root_cause: boolean;
}

export interface PoolReconcileStatsDTO {
  total_files: number;
  service_count: number;
  closure_file_count: number;
  harvested_base_count: number;
  logical_path_count: number;
  conflict_count: number;
  auto_fix_count: number;
  quarantined_count: number;
  bare_import_expanded_count: number;
  post_strip_collision_count: number;
  r7_rounds: number;
  r8_rounds: number;
  compile_error_count: number;
}

export interface PoolReconcileReportDTO {
  logical_map: Record<string, string>;
  conflicts: PoolConflictEntryDTO[];
  auto_fixes: PoolAutoFixEntryDTO[];
  quarantined: PoolQuarantinedEntryDTO[];
  harvested: PoolHarvestedEntryDTO[];
  post_strip_collisions?: PoolStripCollisionDTO[];
  stats: PoolReconcileStatsDTO;
}

export interface PoolReconcileRunDTO {
  id: number;
  status: 'RUNNING' | 'COMPLETED' | 'FAILED' | string;
  triggered_by: string;
  config_revision: number;
  config_sha256: string;
  strip_pg_segment: boolean;
  closure_only: boolean;
  total_files: number;
  service_count: number;
  closure_file_count: number;
  harvested_base_count: number;
  logical_path_count: number;
  conflict_count: number;
  auto_fix_count: number;
  quarantined_count: number;
  bare_import_expanded_count: number;
  post_strip_collision_count: number;
  workspace_version: string;
  error_message?: string;
  duration_ms: number;
  started_at: string;
  finished_at?: string | null;
}

export interface PoolReconcileResponse {
  run: PoolReconcileRunDTO | null;
  report: PoolReconcileReportDTO | null;
}

export interface PoolReconcileProgressEventPayload {
  run_id: number;
  stage: string;
  status: string;
  message: string;
  service_count: number;
  closure_count: number;
  auto_fix_count: number;
  conflict_count: number;
  quarantined_count: number;
}

export interface TriggerPoolReconcilePayload {
  closure_only?: boolean;
  enable_r7_auto_fix?: boolean;
  max_r7_rounds?: number;
  enable_r8_quarantine?: boolean;
  max_r8_rounds?: number;
  codes_dir?: string;
  sync?: boolean;
}

export interface PromoteAutoFixItem {
  file_path: string;
  fix_kind: string;
  old_import?: string;
  new_import: string;
  repo_id?: number;
  bundle_id?: number;
}

export interface PromoteAutoFixesRequest {
  run_id?: number;
  fixes: PromoteAutoFixItem[];
}

export interface PromoteAutoFixesResponse {
  promoted_import_rewrites: number;
  promoted_manual_owners: number;
  governance_revision: number;
}
