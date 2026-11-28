# M46 作战卡：RPC 离线自愈重放端到端生产就绪修复 (`ADR-31`)

- **里程碑编号**：`M46`
- **对应 ADR**：[`ADR-31`（RPC 离线自愈重放端到端代码审计、生产就绪修复决策与用户操作规范）](../05-adr/31-rpc-replay-end-to-end-audit-and-production-readiness-adr.md)
- **对应特性**：`F-55`
- **对应验收场景**：`SC-34`（同时覆盖 `AC-01` ~ `AC-07`）
- **状态**：`🚧 进行中`
- **前置依赖**：`M45`（`✅ 已交付`）

---

## 1. 作战目标与分阶段实施矩阵

围绕 `ADR-31` 审计暴露的 Agent ➔ Server ➔ DB ➔ SSE ➔ Frontend 五层脱节、静默伪绿、Mock/协议边界及用户体验断点，分三个递进阶段按「红灯单测先行 ➔ 最小正确实现 ➔ 检视与门禁验证」闭环落地：

### Phase 1：P0 生产阻断项修复（`D1` + `D2` + `P1-4` + `D4-1`）

| 子任务 | 修改目标文件 | 核心设计要点 | 对应验收 |
| :--- | :--- | :--- | :--- |
| **1.1 Agent 结构化 `ReplayResult`** (`D1`) | `pkg/agentproto/protocol.go`<br>`pkg/replay/matcher.go`<br>`pkg/replay/healer.go`<br>`pkg/replay/executor.go` | 新增 `MockTraceEvent`、`ReplayItemDetail`（`RecordID`, `SeqNo`, `SNO`, `Status`, `LatencyUs`, `ActualRspHex`, `MockTrace`, `Error`），在 `ReplayTaskResultResp` 新增 `Items []ReplayItemDetail` 与 `MockEvents []MockTraceEvent`（保留旧 `Details` 向后兼容）；`ScopedMockMatcher` 返回结构化命中轨迹；`ReplayExecutor` 按 `TIMEOUT` / `MOCK_MISS` / `ERROR` / `RSP_OK` 显式判态并透传 `ActualRspHex` | `AC-01`, `AC-03`, `AC-06` |
| **1.2 SSEBroker 重放实时事件** (`D2-5`) | `internal/server/hub/sse_broker.go`<br>`handlers/sse_handler.go` | 在 `hub.SSEBroker` 直接增加 `BroadcastReplayItemDone` 与 `BroadcastReplayCompleted`（严禁 `internal/server/diff` 反向依赖 `handlers`），`handlers.SSEBroker` 透传代理并写入 `replay.item_done` / `replay.completed` | `AC-06` |
| **1.3 Fail-Closed 调度回收与异常保留** (`D2` + `P1-4`) | `models/replay.go`<br>`internal/server/diff/engine.go`<br>`internal/server/diff/scheduler.go`<br>`internal/server/diff/csg_exec.go` | 1. 彻底废除 `rec.RawRspPacket` / `item.ExpectedRspPacket` 自降级自比较，缺失 `actual_rsp_hex` 或解码失败强制 Fail-Closed 为 `ERROR`；<br>2. 抽取共享 `evaluateReplayItem` 供录制批次重放、黄金套件重放、`RediffTask` 三处复用（`ONEWAY` 跳过 Layer 1）；<br>3. `ActualPayload` 解码落盘 `contract_replay_items.actual_payload`（DB 新增 `FailedLayer`）；<br>4. 状态持久化强制带 `lifecycle_state != 'CANCELLED'` 条件守卫防并发覆写；<br>5. `RediffTask` 合并 `task.ExtraIgnorePaths` 与 `suite.MaskRulesJSON`，保留既有 `TIMEOUT`/`MOCK_MISS`/`ERROR` 并刷新 `MockTrace`；<br>6. `csg_exec.go` 透传 `409 Conflict` 结构化错误包 | `AC-01`, `AC-03`, `AC-04`, `AC-05`, `AC-06` |
| **1.4 单包调试抽屉全链修通** (`D4-1`) | `internal/server/callgraph/{types,hydrate}.go`<br>`models/agent.go`<br>`handlers/process_handler.go`<br>`frontend/src/types/traffic.ts`<br>`frontend/src/api/replay.ts`<br>`frontend/src/pages/TrafficDebugWorkbenchPage/components/SinglePacketReplayDrawer.tsx` | 后端补齐 `TrafficRecordSummary.ArchProcessID` 与 `Process.ModuleIDs`（零破坏 `GET /api/processes`）；前端迁移为按 `ArchProcessID` 匹配同构在线进程、补齐 `csg_strategy` 选择、`POST /api/replay-tasks` 后轮询 `/api/replay-tasks/:id` 至终态再拉 `/api/replay-tasks/:id/items?record_id=` | `AC-02`, `AC-06` |

### Phase 2：P1 契约、可观测性与操作体验闭环（`D3` + `D4-2`/`D4-3` + `P1-1`）

| 子任务 | 修改目标文件 | 核心设计要点 | 对应验收 |
| :--- | :--- | :--- | :--- |
| **2.1 派生诊断与汇总字段水合** (`D3-1`/`D3-2`) | `models/replay.go`<br>`internal/server/diff/scheduler.go` | `ReplayTask` API 派生填充 `failed_requests`/`mock_miss_requests`/`timeout_requests`/`error_requests`（从 `contract_replay_items` 批量聚合 + `ListTasks` 修复 `agent_id` 过滤）；`ReplayItem` 派生填充 `header_diff_passed`、`sno`、`actual_status_code`、`baseline_status_code`（复用 `ActualStatus`/`BaselineStatus`）及持久化 `failed_layer` | `AC-06` |
| **2.2 前端报告抽屉与类型对齐** (`D3-3`) | `frontend/src/types/replay.ts`<br>`ReplayTaskTable.tsx`<br>`ReplayReportDrawer.tsx`<br>`HeaderAssertionCard.tsx`<br>`MockHitMissTimeline.tsx` | 全量消除前后端字段漂移；`HeaderAssertionCard` 展示真实 `sno`、`status_code`、`failed_layer`、`header_diff_passed`；`ReplayReportDrawer` 绑定 `FailedLayer`、真实计数、Payload 左右双栏与 `MockHitMissTimeline`；`ReplayTaskTable` 精准呈现 5 态计数 | `AC-06` |
| **2.3 源/目标过程解耦与就绪防呆** (`D4-2`, `D4-3`, `P1-1`) | `ReplayTaskCreatorModal.tsx`<br>`GoldenSuiteExecuteModal.tsx`<br>`ReplayWorkbenchPage.tsx` | 1. 将 `ReplayTaskCreatorModal` 从单下拉框拆分为「1. 选定源录制进程 (`source_process_id`) -> 2. 自动筛选并选择可运行重放的同构目标进程 (`target_process_id`)」，剔除自选非目标实例与未就绪进程；<br>2. 对 `WAITING_PROC`/`NEED_RECONNECT`/`DISARMED` 显示探针就绪告警与一键前往 `/agents` / `/discovery` 引导；<br>3. `/replay` 自动解析并预填 `?session_id=&auto_create=&agent_id=&arch_process_id=&strategy=&target_process_id=` 参数并绑定 SSE/有界轮询（`GoldenSuiteExecuteModal` 同步清理内联色值切换 `--color-*` Token） | `AC-02`, `AC-06` |

### Phase 3：P2 健壮性、eBPF 探针门禁与 `demo/` 端到端闭环（`P2-1` + `D5-2` + `SC-34`）

| 子任务 | 修改目标文件 | 核心设计要点 | 对应验收 |
| :--- | :--- | :--- | :--- |
| **3.1 多分片聚合与单条隔离重连** (`P2-1`) | `pkg/replay/executor.go`<br>`pkg/replay/replay_test.go` | `readResponseBySNO` 循环合并 `ucMsgType == MsgTypeRspSeg (4)` 连续分片至终结帧；单条注入发生 `TIMEOUT` 或连接 `EOF`/写错误时仅标当前条目异常，按需关闭并重建注入连接后继续剩余 `IngressItems`；确保超时/取消/错误出口完整上报已执行 `Items`、`MockEvents` 与 `ObservedEgressCalls` | `AC-07` |
| **3.2 eBPF 探针就绪守卫** (`D5-2`) | `pkg/ebpf/manager.go`<br>`internal/agent/server/http_server.go`<br>`internal/agent/server/server_test.go` | 新增 `Manager.IsProcessArmed(processID uint64) bool` 与 `HasCapabilities() bool`；在 `handleExecuteReplay` 启动执行前当重放需要 `EgressProxy` 拦截或特权宿主进程未挂载待命时，直接拒绝执行并返回 `409 Conflict` (`code: "PROCESS_NOT_ARMED"`) | `AC-07` |
| **3.3 真实双向进程 `demo/` 闭环与状态归档** (`SC-34`) | `demo/replay_readiness_e2e_test.go`<br>`docs/05-adr/31-*.md`<br>`docs/04-tasks/01-roadmap.md` | 1. 编写基于真实 `TCPDemoClient`/`TCPDemoBackend`/Agent/Server 的 `demo/` E2E 用例覆盖 `AC-01`~`AC-07`（正常自愈 `PASS` + `FAILED` 差异 + `MOCK_MISS` 零外泄 + `TIMEOUT` 隔离 + 多分片 + `Rediff` + `PROCESS_NOT_ARMED` 409）；<br>2. 将 `ADR-31` 与 `M46` 状态更新为 `Implemented` / `✅ 已交付` | `AC-01` ~ `AC-07` (`SC-34`) |

---

## 2. 自动化验收矩阵 (`AC-01` ~ `AC-07`)

1. `AC-01`（伪绿根除）：无 `actual_rsp_hex` 或空白响应严禁返回 `PASS`，必须 Fail-Closed 为 `ERROR`（Phase 1）；
2. `AC-02`（跨环境与就绪门槛）：单包抽屉与任务创建器按 `arch_process_id` 选目标，未就绪/`NEED_RECONNECT` 返回 `409`（Phase 1 & 2）；
3. `AC-03`（Mock 命中与未命中）：`EgressProxy` 命中返回结构化 `SCOPED_HIT`/`GLOBAL_FALLBACK_HIT`，未命中严格拦截不外泄并判 `MOCK_MISS`（Phase 1 & 3）；
4. `AC-04`（Payload 与状态码校验）：状态码变更或字段突变均报 `FAILED` 并写入真实 `field_diffs` 与 `actual_payload`（Phase 1）；
5. `AC-05`（`Rediff` 行为正确性）：仅更新 Diff 且保留 `TIMEOUT`/`MOCK_MISS`/`ERROR`，合并规则不丢套件掩码（Phase 1）；
6. `AC-06`（前后端字段零丢失）：`FailedLayer`、`ActualPayload`、5 态计数、头部断言卡、Mock 时间线及 SSE 事件逐项一致（Phase 1 & 2）；
7. `AC-07`（超时与 `PROCESS_NOT_ARMED`）：单条 `TIMEOUT` 隔离、多 `RSP_SEG` 分片聚合、未布防进程返 `409 PROCESS_NOT_ARMED`、`CANCELLED` 防覆盖及完整 `demo/` E2E 通过（Phase 1 & 3）。
