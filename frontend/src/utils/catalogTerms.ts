import type { RadarState } from '../types/catalog';

export type CatalogBadgeTone = 'success' | 'warning' | 'danger' | 'info' | 'neutral';

export interface RadarStateMeta {
  key: string;
  label: string;
  summary: string;
  detail: string;
  badgeTone: CatalogBadgeTone;
  radarTileTone: 'warning' | 'info' | 'danger';
}

export const RADAR_ABNORMAL_ORDER: Array<Exclude<RadarState, 'HEALTHY'>> = [
  'SHADOW_LIVE',
  'UNCLAIMED_ORPHAN',
  'PHANTOM_PROTO',
  'VANISHED_CLAIMED',
  'PLACEMENT_DRIFT',
];

export const RADAR_ALL_ORDER: RadarState[] = [...RADAR_ABNORMAL_ORDER, 'HEALTHY'];

export const RADAR_STATE_META: Record<RadarState, RadarStateMeta> = {
  SHADOW_LIVE: {
    key: 'SHADOW_LIVE',
    label: '无契约在线',
    summary: '现网正在运行，但未匹配到已激活的 .pb 契约',
    detail:
      '权威共享环境已观测到该服务运行，但尚未关联任何生效的 .pb 接口定义，无法自动解码流量或做契约校验。建议点击「一键匹配 .pb 契约」或补齐并发布对应 .proto 定义。',
    badgeTone: 'warning',
    radarTileTone: 'warning',
  },
  UNCLAIMED_ORPHAN: {
    key: 'UNCLAIMED_ORPHAN',
    label: '已上线未认领',
    summary: '现网正在运行且已有 .pb 契约，但尚未归属任何逻辑模块',
    detail:
      '权威共享环境已观测到该服务运行并关联了 .pb 契约，但尚未在架构中心被逻辑模块认领，责任归属不明确。建议前往架构中心认领归属模块。',
    badgeTone: 'info',
    radarTileTone: 'info',
  },
  PHANTOM_PROTO: {
    key: 'PHANTOM_PROTO',
    label: '纸面未上线',
    summary: '已有 .pb 契约定义，但权威现网既无运行实例也无调用订阅',
    detail:
      '契约库中已激活该服务的 .pb 定义，但在权威共享环境中既未探测到提供方实例，也无任何下游订阅调用（常见于尚未上线的新接口或已废弃的僵尸契约）。请核实是否待上线或可清理。',
    badgeTone: 'danger',
    radarTileTone: 'danger',
  },
  VANISHED_CLAIMED: {
    key: 'VANISHED_CLAIMED',
    label: '已认领但下线',
    summary: '已被逻辑模块认领且曾上线过，但当前权威现网已无运行实例',
    detail:
      '该服务已归属逻辑模块且历史上曾在权威共享环境运行，但目前所有现网实例均已退出或失联。请核实服务是否异常宕机、已改名迁移或已正式下线。',
    badgeTone: 'warning',
    radarTileTone: 'warning',
  },
  PLACEMENT_DRIFT: {
    key: 'PLACEMENT_DRIFT',
    label: '实际部署漂移',
    summary: '实际运行的架构进程与逻辑模块认领的设计进程不一致',
    detail:
      '该服务已被逻辑模块认领（架构设计指定了目标进程），但权威共享环境探测到它实际运行在其他架构进程中。请前往架构收件箱核查并修正部署归属或架构设计。',
    badgeTone: 'danger',
    radarTileTone: 'danger',
  },
  HEALTHY: {
    key: 'HEALTHY',
    label: '状态健康',
    summary: '契约已匹配、模块已认领，且现网运行与架构设计一致',
    detail:
      '该服务已关联生效的 .pb 契约、已被逻辑模块认领，且权威现网实际部署位置与架构设计完全一致。',
    badgeTone: 'success',
    radarTileTone: 'info',
  },
};

/** 获取五态雷达状态的中文释义元数据 */
export function getRadarStateMeta(state: string): RadarStateMeta {
  if (state in RADAR_STATE_META) {
    return RADAR_STATE_META[state as RadarState];
  }
  return {
    key: state,
    label: state || '未知状态',
    summary: '未知状态',
    detail: '',
    badgeTone: 'neutral',
    radarTileTone: 'info',
  };
}

export interface ClaimStatusMeta {
  label: string;
  tone: CatalogBadgeTone;
  desc: string;
}

export const CLAIM_STATUS_META: Record<string, ClaimStatusMeta> = {
  CLAIMED: {
    label: '已认领',
    tone: 'success',
    desc: '已在架构中心归属至具体逻辑模块',
  },
  UNCLAIMED: {
    label: '未认领',
    tone: 'warning',
    desc: '尚未在架构中心归属至任何逻辑模块',
  },
  PHANTOM_ONLY: {
    label: '仅契约定义',
    tone: 'neutral',
    desc: '仅存在于 .pb 契约库，尚未在权威现网运行或被认领',
  },
};

/** 获取逻辑认领状态的中文标签与样式色标 */
export function getClaimStatusMeta(status: string): ClaimStatusMeta {
  return (
    CLAIM_STATUS_META[status] ?? {
      label: status || '未知状态',
      tone: 'neutral',
      desc: '',
    }
  );
}

export interface ProtoStatusMeta {
  label: string;
  tone: CatalogBadgeTone;
}

export const PROTO_STATUS_META: Record<string, ProtoStatusMeta> = {
  CONFIRMED: { label: '已确认', tone: 'success' },
  SUGGESTED: { label: '待确认建议', tone: 'info' },
  UNMATCHED: { label: '未匹配 .pb', tone: 'neutral' },
};

/** 获取 .pb 契约匹配状态的中文标签与色标 */
export function getProtoStatusMeta(status: string): ProtoStatusMeta {
  return (
    PROTO_STATUS_META[status] ?? {
      label: status || '未知状态',
      tone: 'neutral',
    }
  );
}

/** 格式化 .pb 撮合置信度为中文说明 */
export function formatMatchConfidence(confidence?: string): string {
  switch (confidence) {
    case 'EXACT':
      return '精确匹配';
    case 'HEURISTIC':
      return '名称推断';
    case 'FINGERPRINT':
      return '方法指纹';
    case 'MANUAL':
      return '人工指定';
    case 'NONE':
    case '':
    case undefined:
      return '未匹配';
    default:
      return confidence;
  }
}

/** 格式化传输协议名称 */
export function formatTransport(transport: string): string {
  switch (transport) {
    case 'TCP':
      return 'TCP';
    case 'MESH':
      return 'Mesh 网格';
    default:
      return transport;
  }
}

/** 格式化契约血缘认领状态 */
export function formatLineageClaimStatus(status: string): string {
  switch (status) {
    case 'CONTRACT':
      return '多实现契约';
    case 'SINGLE_IMPL':
      return '单实现认领';
    case 'UNCLAIMED':
      return '未认领';
    default:
      return status;
  }
}

/** 格式化 RPC 方法流式调用模式 */
export function formatStreamMode(
  clientStreaming?: boolean,
  serverStreaming?: boolean
): string {
  if (clientStreaming && serverStreaming) {
    return '双向流';
  }
  if (clientStreaming) {
    return '客户端流';
  }
  if (serverStreaming) {
    return '服务端流';
  }
  return '普通调用';
}
