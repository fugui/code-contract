# ADR-25: 低关注/噪音服务全链路降噪治理（双源 SSOT + Agent 源头丢弃 + 事务分析折叠）

- **状态**: Accepted (实施中)
- **日期**: 2026-11-26
- **关联**: [ADR-12](12-packet-causality-call-tree-and-sequence-ux-adr.md) · [ADR-16](16-multi-implementation-service-contract-adr.md) · [ADR-21](21-csg-aware-agent-service-discovery-and-replay-mock-adr.md) · [ADR-22](22-pluggable-transport-driver-and-mesh-capture-adr.md)

---

## 1. 背景与痛点

在线上微服务链路中，存在大量高频率、低关注度的基础设施服务调用（如 `LogService` 日志上报、`MonitorService` / `Metrics` 监控打点、周期性心跳等）。这些调用带来了两个突出问题：

1. **事务分析与时序泳道“喧宾夺主”**：批量录制后，独立日志/心跳请求会挤占“事务调用树”的分页配额，业务主流程树内部也充斥着大量高频日志子调用，且在“时序泳道”中强行撑出多列基础设施泳道，干扰业务排障视线。
2. **录制存储与帧数配额被刷爆**：高频日志/监控流量会快速消耗 `RecordingSession.MaxFrames`（默认 100,000 帧）配额并拖慢落盘与建树效率。

同时，在本平台架构中，录制流量不仅用于观测分析，还直接作为离线重放的下游 Mock 来源（`ScopedMockMatcher`）。若不加区分地截断或改动重放匹配语义，可能破坏同步依赖的 Mock 完整性或违背“严禁外泄 / 严格报告 `MOCK_MISS`”红线。

---

## 2. 核心架构裁决

### 2.1 双源规则 SSOT 与安全初始态
1. **双源取并集**：噪音/低关注服务名单由以下两处配置合并去重生成：
   - **全局通配规则（`contract_settings`：`noise_service_rules`）**：支持精确服务名、`*` 通配符以及 `ServicePattern:OperationPattern` 粒度（大小写不敏感，同步兼容 `^(.+-\d+)-(\d+)$` PID 后缀归一化），负责兜底尚未认领的 CSG 自动发现服务。
   - **服务台账标记（`contract_module_services.is_noise_service`）**：在架构模块供给服务中单服务显式勾选标记（同时覆盖其 `service_name` 与 `proto_service_name`）。
2. **初始默认留空，防止误伤**：`noise_service_rules` 初始化为空串，避免 `*Log*` 误匹配 `LoginService` / `CatalogService`；管理设置页内置三组常见安全推荐模板（日志、监控指标、心跳探活），支持管理员一键追加。

### 2.2 录制期源头丢弃（`DROP_AT_AGENT`）
1. **开始录制默认开启，可单次切换/微调**：
   - 开始录制抽屉默认勾选“源头过滤噪音服务 (`DROP_AT_AGENT`)”，支持一键取消切换为全量录制，并支持展开勾选放行继承规则或临时追加本次排除规则；配置快照落入 `RecordingSession`（`noise_filter_enabled`、`noise_exclude_rules`）。
2. **Agent 侧零 `.pb` 成对丢弃（严格遵守物理防腐红线）**：
   - 规则匹配器 `pkg/agentproto.NoiseMatcher` 仅依赖 Go 标准库。
   - `SyncMonitorReq` 携带 `exclude_rules []string`。Agent 在 **TCP eBPF**（`FrameSink`）与 **Mesh uprobe**（`transport.FrameSinkFunc`）统一上报入口拦截 `EventTrafficFrame`：
     - 解析 `crosrpc.CommHead` 与 `ReqHead` / `RspHead` 的 `ServiceName` / `Operation`；
     - **永不拦截 MNG 管理帧**（保留用于 `peerProcNames` 进程名自报学习）与非流量控制信封（`EventSessionDrained`、`EventConnClosed`、`EventCSGObservation`、`EventProcessLifecycle`）；
     - 命中规则的请求帧记录有界 `(connTuple, SNO)` 集合，其对应应答帧（含未回填 `ServiceName` 的分片应答）同步成对丢弃；
     - 丢弃帧**不进入 `BoundedSessionBuffer`、不占 `MaxFrames` 配额**，停录时通过 `StopMonitorResp.filtered_noise_frames` 上报并累计写入 `RecordingSession.filtered_noise_frames`。

### 2.3 重放引擎边界（零改动）
- `pkg/replay/matcher.go` 保持既有逻辑不动：现网主流 `ONEWAY` 异步日志/打点在重放时天然命中 `ONEWAY_HIT`；若被测进程依赖同步/异步回包的噪音服务，触发 `MOCK_MISS` 时按既定红线告警提醒，研发只需关闭本次录制的源头过滤即可重采全量 Mock。

### 2.4 事务分析、平铺检索与时序泳道完整视图降噪
1. **默认开启隐藏，支持一键全量切换**：事务调用树、原始平铺表、时序泳道查询默认 `hide_noise=true`（可切换全量显示），支持会话级临时屏蔽 `exclude_services`。
2. **先算账、后折叠**：`callgraph.BuildTransactionTreesWithStats` 必须先基于全量节点完成 `SelfExecUs` 与 `WaterfallOffsetPct`/`WaterfallWidthPct` 计算，然后再执行隐藏与折叠，防止把被折叠日志的耗时误算为业务方法自身耗时。
3. **异常破格原则**：只要某条记录存在协议错误（`status_code != 0`）、孤立超时（`REQ_ONLY`）或业务软错误（`biz_error = true`），**即使命中噪音规则也不得隐藏或折叠**，确保故障根因永不被掩盖。
4. **根隐藏与树内胶囊折叠**：
   - 全树均为非异常噪音的独立根事务直接隐藏（不占用根事务分页配额），并返回 `hidden_noise_root_count`；
   - 业务树内部的非异常噪音子树原地移入 `folded_noise_children`，并聚合 `folded_noise_summary`（按 `service_name + operation` 统计次数与总耗时），前端以紧凑胶囊行展示并支持原地展开；
   - 时序泳道自动裁剪无剩余交互步的噪音参与者列。

### 2.5 RBAC 权限边界
- `GET /api/noise-rules` 对已认证用户开放（供开始录制抽屉与调试页展示生效规则）。
- “本次视图屏蔽”为只读前端/查询参数操作，所有用户可用。
- “加入全局噪音名单”（`POST /api/admin/noise-rules/append`）、修改 `contract_settings[noise_service_rules]` 及修改 `ModuleService.is_noise_service` 严格限 `contract_admin`。
