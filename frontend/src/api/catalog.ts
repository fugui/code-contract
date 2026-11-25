import { apiRequest } from './client';
import type {
  CatalogCorrelateResult,
  CatalogServicesResponseDTO,
} from '../types';

export async function listCatalogServices(params?: {
  state?: string;
  proto_status?: string;
  arch_process_id?: number | string;
  search?: string;
  page?: number;
  page_size?: number;
}): Promise<CatalogServicesResponseDTO> {
  const query = new URLSearchParams();
  if (params?.state) query.set('state', params.state);
  if (params?.proto_status) query.set('proto_status', params.proto_status);
  if (params?.arch_process_id) query.set('arch_process_id', String(params.arch_process_id));
  if (params?.search) query.set('search', params.search);
  if (params?.page) query.set('page', String(params.page));
  if (params?.page_size) query.set('page_size', String(params.page_size));
  const qs = query.toString();
  return apiRequest<CatalogServicesResponseDTO>(`/catalog/services${qs ? `?${qs}` : ''}`);
}

export async function correlateCatalogServices(
  serviceIds?: number[]
): Promise<CatalogCorrelateResult> {
  return apiRequest<CatalogCorrelateResult>('/catalog/correlate', {
    method: 'POST',
    body: JSON.stringify(
      serviceIds && serviceIds.length > 0 ? { service_ids: serviceIds } : {}
    ),
  });
}

export async function confirmCatalogServiceProto(
  serviceId: number | string,
  protoServiceName: string
): Promise<Record<string, unknown>> {
  return apiRequest<Record<string, unknown>>(`/catalog/services/${serviceId}/confirm-proto`, {
    method: 'POST',
    body: JSON.stringify({ proto_service_name: protoServiceName }),
  });
}
