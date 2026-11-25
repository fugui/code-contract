import * as archApi from '../../api/arch';
import type {
  AttributionJobDTO,
  AttributionJobEventDTO,
  AttributionJobStatus,
  ClaimArchServicesResp,
  ClaimServiceItemInput,
  DecideDependencySuggestionResp,
  DependencyDecision,
  DepSuggestionDTO,
} from '../../types';

export interface SplitFormRow {
  arch_process_id: number;
  module_id: string;
  impl_key: string;
}

/** 计算归因回补作业完成百分比 (0~100) */
export function attributionPercent(job: AttributionJobDTO): number {
  if (job.total > 0) {
    return Math.min(100, Math.max(0, Math.round((job.processed / job.total) * 100)));
  }
  return job.status === 'DONE' ? 100 : 0;
}

/** 根据已勾选服务与目标模块映射组装 ClaimServiceItemInput[] */
export function buildClaimItems(
  selectedIds: number[],
  targets: Record<number, string>
): ClaimServiceItemInput[] {
  const items: ClaimServiceItemInput[] = [];
  for (const id of selectedIds) {
    const modId = Number(targets[id]);
    if (Number.isFinite(modId) && modId > 0) {
      items.push({ service_id: id, module_id: modId });
    }
  }
  return items;
}

/** 校验并提交批量认领 */
export async function submitInboxClaim(
  selectedIds: number[],
  targets: Record<number, string>
): Promise<ClaimArchServicesResp> {
  if (selectedIds.length === 0) {
    throw new Error('请至少选择一项待认领服务');
  }
  const items = buildClaimItems(selectedIds, targets);
  if (items.length !== selectedIds.length) {
    throw new Error('请为所有选中的服务指定目标业务模块');
  }
  return archApi.claimArchServices(items);
}

/** 校验并提交多宿主拆分请求 */
export async function submitInboxSplit(params: {
  sourceServiceId: number;
  definerModuleId: string;
  rows: SplitFormRow[];
}): Promise<{
  contract: unknown;
  implementations: unknown[];
  attribution_job_id?: number;
}> {
  const definerId = Number(params.definerModuleId);
  if (!Number.isFinite(definerId) || definerId <= 0) {
    throw new Error('请选择有效的接口契约库模块 (CONTRACT_LIB)');
  }
  if (params.rows.length < 2) {
    throw new Error('多实现拆分至少需要 2 个物理进程实现');
  }
  const seenKeys = new Set<string>();
  const seenMods = new Set<number>();
  const implementations = params.rows.map((r) => {
    const modId = Number(r.module_id);
    const implKey = r.impl_key.trim();
    if (!Number.isFinite(modId) || modId <= 0) {
      throw new Error('请为每个物理进程选择目标业务模块');
    }
    if (!implKey) {
      throw new Error('请填写非空的实现标识 (impl_key)');
    }
    if (seenKeys.has(implKey)) {
      throw new Error(`实现标识 "${implKey}" 重复，请为每个进程指定唯一 impl_key`);
    }
    if (seenMods.has(modId)) {
      throw new Error('不同进程的实现必须对应不同的业务模块');
    }
    seenKeys.add(implKey);
    seenMods.add(modId);
    return {
      arch_process_id: r.arch_process_id,
      module_id: modId,
      impl_key: implKey,
    };
  });

  return archApi.splitMultiImpl({
    source_service_id: params.sourceServiceId,
    definer_module_id: definerId,
    implementations,
  });
}

/** 计算反向依赖建议默认选中的消费方业务模块 ID */
export function defaultConsumerModuleId(suggestion: DepSuggestionDTO): number | undefined {
  if (
    suggestion.recommended_consumer_module_id !== undefined &&
    suggestion.recommended_consumer_module_id > 0
  ) {
    return suggestion.recommended_consumer_module_id;
  }
  if (suggestion.candidate_consumer_modules.length === 1) {
    return suggestion.candidate_consumer_modules[0].module_id;
  }
  return undefined;
}

/** 提交反向依赖建议裁决 */
export async function submitDependencyDecision(
  suggestion: DepSuggestionDTO,
  decision: DependencyDecision,
  consumerModuleId?: number,
  rationale?: string
): Promise<DecideDependencySuggestionResp> {
  if (decision === 'APPROVE' && (!consumerModuleId || consumerModuleId <= 0)) {
    throw new Error('批准依赖前请先选择消费方业务模块');
  }
  return archApi.decideDependencySuggestion({
    consumer_arch_process_id: suggestion.consumer_arch_process_id,
    service_id: suggestion.service_id,
    decision,
    consumer_module_id: consumerModuleId,
    rationale,
  });
}

/** 将 SSE attribution_job.updated 事件合并进当前作业状态 */
export function mergeAttributionEvent(
  job: AttributionJobDTO,
  evt: AttributionJobEventDTO
): AttributionJobDTO {
  if (evt.job_id !== job.id) {
    return job;
  }
  return {
    ...job,
    status: evt.status as AttributionJobStatus,
    processed: evt.processed,
    total: evt.total,
    cursor_session_id: evt.cursor_session_id,
    matched_count: evt.matched_count,
    unresolved_count: evt.unresolved_count,
    conflict_count: evt.conflict_count,
    error: evt.last_error ?? job.error,
  };
}

export interface TrackAttributionJobOptions {
  intervalMs?: number;
  maxAttempts?: number;
  wait?: (ms: number) => Promise<void>;
  onUpdate?: (job: AttributionJobDTO) => void;
  signal?: AbortSignal;
}

const defaultWait = (ms: number) =>
  new Promise<void>((resolve) => {
    setTimeout(resolve, ms);
  });

/** 轮询归因回补作业直至到达终态或取消 */
export async function trackAttributionJob(
  jobId: number,
  options?: TrackAttributionJobOptions
): Promise<AttributionJobDTO> {
  const intervalMs = options?.intervalMs ?? 1200;
  const maxAttempts = options?.maxAttempts ?? 120;
  const wait = options?.wait ?? defaultWait;

  let latest: AttributionJobDTO | null = null;
  for (let i = 0; i < maxAttempts; i++) {
    if (options?.signal?.aborted) {
      break;
    }
    latest = await archApi.getAttributionJob(jobId);
    options?.onUpdate?.(latest);
    if (latest.status === 'DONE' || latest.status === 'FAILED' || latest.status === 'CANCELLED') {
      return latest;
    }
    if (i + 1 < maxAttempts) {
      await wait(intervalMs);
    }
  }
  if (!latest) {
    throw new Error('未能获取归因作业状态');
  }
  return latest;
}
