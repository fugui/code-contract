import { apiRequest } from './client';
import type {
  DiscoveryLiveRespDTO,
  DiscoverySnapshotDTO,
  DiscoverySnapshotDiffDTO,
  DiscoverySnapshotSummaryDTO,
  PaginatedResponse,
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
