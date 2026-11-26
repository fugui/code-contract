import { apiRequest, apiRequestWithErrorPayload } from './client';
import type {
  CSGPlaceholderMigrationRespDTO,
  DiscoveryLiveRespDTO,
  DiscoverySnapshotDTO,
  DiscoverySnapshotDiffDTO,
  DiscoverySnapshotSummaryDTO,
  EnrollItemInput,
  EnrollResultDTO,
  PaginatedResponse,
  PhysicalAssetsRespDTO,
  QuickRecordReqInput,
  QuickRecordResultDTO,
  SimulationPlanReqInput,
  SimulationPlanResultDTO,
} from '../types';

export async function getLiveDiscovery(params: {
  agent_id: string | number;
  csg_addr?: string;
  refresh?: boolean;
}): Promise<DiscoveryLiveRespDTO> {
  const query = new URLSearchParams();
  query.set('agent_id', String(params.agent_id));
  if (params.csg_addr) query.set('csg_addr', params.csg_addr);
  if (params.refresh) query.set('refresh', 'true');
  return apiRequest<DiscoveryLiveRespDTO>(`/discovery/live?${query.toString()}`);
}

export async function listDiscoverySnapshots(params: {
  agent_id: string | number;
  csg_addr?: string;
  pinned_only?: boolean;
  page?: number;
  page_size?: number;
}): Promise<PaginatedResponse<DiscoverySnapshotSummaryDTO>> {
  const query = new URLSearchParams();
  query.set('agent_id', String(params.agent_id));
  if (params.csg_addr) query.set('csg_addr', params.csg_addr);
  if (params.pinned_only) query.set('pinned_only', 'true');
  if (params.page) query.set('page', String(params.page));
  if (params.page_size) query.set('page_size', String(params.page_size));
  return apiRequest<PaginatedResponse<DiscoverySnapshotSummaryDTO>>(
    `/discovery/snapshots?${query.toString()}`
  );
}

export async function createDiscoverySnapshot(data: {
  agent_id: string | number;
  csg_addr?: string;
  label?: string;
  is_pinned?: boolean;
}): Promise<DiscoverySnapshotDTO> {
  return apiRequest<DiscoverySnapshotDTO>('/discovery/snapshots', {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

export async function updateDiscoverySnapshot(
  id: string | number,
  data: { label?: string; is_pinned?: boolean }
): Promise<DiscoverySnapshotDTO> {
  return apiRequest<DiscoverySnapshotDTO>(`/discovery/snapshots/${id}`, {
    method: 'PATCH',
    body: JSON.stringify(data),
  });
}

export async function deleteDiscoverySnapshot(
  id: string | number
): Promise<{ message: string }> {
  return apiRequest<{ message: string }>(`/discovery/snapshots/${id}`, {
    method: 'DELETE',
  });
}

export async function diffDiscoverySnapshots(params: {
  left_snapshot_id: string | number;
  right_snapshot_id?: string | number;
  live_agent_id?: string | number;
  csg_addr?: string;
}): Promise<DiscoverySnapshotDiffDTO> {
  const query = new URLSearchParams();
  query.set('left_snapshot_id', String(params.left_snapshot_id));
  if (params.right_snapshot_id !== undefined && params.right_snapshot_id !== '') {
    query.set('right_snapshot_id', String(params.right_snapshot_id));
  }
  if (params.live_agent_id !== undefined && params.live_agent_id !== '') {
    query.set('live_agent_id', String(params.live_agent_id));
  }
  if (params.csg_addr) {
    query.set('csg_addr', params.csg_addr);
  }
  return apiRequest<DiscoverySnapshotDiffDTO>(
    `/discovery/snapshots/diff?${query.toString()}`
  );
}

export async function enrollDiscoveryProcesses(data: {
  agent_id: string | number;
  csg_addr?: string;
  target_group_id?: string | number;
  items: EnrollItemInput[];
}): Promise<EnrollResultDTO> {
  const targetGroupNum = data.target_group_id !== undefined && data.target_group_id !== ''
    ? Number(data.target_group_id)
    : 0;
  return apiRequestWithErrorPayload<EnrollResultDTO>('/discovery/enroll', {
    method: 'POST',
    body: JSON.stringify({
      agent_id: Number(data.agent_id),
      csg_addr: data.csg_addr,
      ...(targetGroupNum > 0 ? { target_group_id: targetGroupNum } : {}),
      items: data.items,
    }),
  });
}

export async function getPhysicalAssets(params: {
  agent_id?: string | number;
  group_id?: string | number;
  status?: string;
  claim_status?: string;
  transport?: string;
  search?: string;
  include_provisional?: boolean;
  page?: number;
  page_size?: number;
}): Promise<PhysicalAssetsRespDTO> {
  const query = new URLSearchParams();
  if (params.agent_id) query.set('agent_id', String(params.agent_id));
  if (params.group_id) query.set('group_id', String(params.group_id));
  if (params.status) query.set('status', params.status);
  if (params.claim_status) query.set('claim_status', params.claim_status);
  if (params.transport) query.set('transport', params.transport);
  if (params.search) query.set('search', params.search);
  if (params.include_provisional) query.set('include_provisional', 'true');
  if (params.page) query.set('page', String(params.page));
  if (params.page_size) query.set('page_size', String(params.page_size));
  return apiRequest<PhysicalAssetsRespDTO>(`/discovery/physical?${query.toString()}`);
}

export async function promoteArchProcess(
  archProcessId: number | string,
  data: { code?: string; name?: string } = {}
): Promise<{ arch_process: Record<string, unknown> }> {
  return apiRequest<{ arch_process: Record<string, unknown> }>(
    `/admin/arch-processes/${archProcessId}/promote`,
    {
      method: 'POST',
      body: JSON.stringify(data),
    }
  );
}

export async function migrateCSGPlaceholderModules(data: {
  dry_run?: boolean;
  action?: 'EXECUTE' | 'ROLLBACK';
  batch_id?: string;
}): Promise<CSGPlaceholderMigrationRespDTO> {
  return apiRequest<CSGPlaceholderMigrationRespDTO>(
    '/admin/migrations/csg-placeholder-modules',
    {
      method: 'POST',
      body: JSON.stringify(data),
    }
  );
}

export async function quickRecordDiscovery(
  data: QuickRecordReqInput
): Promise<QuickRecordResultDTO> {
  return apiRequestWithErrorPayload<QuickRecordResultDTO>('/discovery/quick-record', {
    method: 'POST',
    body: JSON.stringify({
      ...data,
      agent_id: Number(data.agent_id),
    }),
  });
}

export async function buildDiscoverySimulationPlan(
  data: SimulationPlanReqInput
): Promise<SimulationPlanResultDTO> {
  return apiRequest<SimulationPlanResultDTO>('/discovery/simulation-plan', {
    method: 'POST',
    body: JSON.stringify({
      agent_id: Number(data.agent_id),
      arch_process_id: data.arch_process_id ? Number(data.arch_process_id) : undefined,
      process_key: data.process_key,
      csg_addr: data.csg_addr,
    }),
  });
}
