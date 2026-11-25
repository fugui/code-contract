# ADR-24：CSG 现网发现的进程主轴视图与物理/逻辑架构导入边界设计（草案）

- **文档编号**：`24-csg-discovery-process-centric-and-physical-logical-boundary-adr.md`
- **文档版本**：v0.1.0
- **日期**：2026-11-25
- **状态**：📝 草案待定稿 (Draft — 待进一步设计后落地)
- **关联特性**：`F-02`（物理进程蓝图与模块服务契约）、`F-05`（实测调用拓扑还原与一致性验证）、`F-33`（CSG 现网服务与依赖双层自动发现及蓝图导入）
- **关联 ADR**：[`ADR-09`](./09-arch-process-ssot-and-ebpf-premount-standby-adr.md)、[`ADR-16`](./16-multi-implementation-service-contract-adr.md)、[`ADR-21`](./21-csg-aware-agent-service-discovery-and-replay-mock-adr.md)

---

## 1. 背景与问题诊断 (Context & Problem Statement)

在 [`ADR-21`](./21-csg-aware-agent-service-discovery-and-replay-mock-adr.md) 落地后，Agent 已能通过 `RPCMonitorService` + `csgdlink.Sub` + `/proc` 融合获取本机现网的进程、发布服务与客户端订阅边。但在实际使用「CSG 现网服务探测与蓝图对账」工作台时，暴露出两个结构性错位：

1. **现网发现的客观事实是「宿主进程 → 服务」，而非平铺的「服务列表」**：
   - 运行期实体是宿主机上的**操作系统进程**（`procName` + `PID`），每个进程发布若干 CROS RPC 服务、并订阅若干下游服务；
   - 当前列表以 64B `ServiceName` 平铺展示（可达数百行），导致用户无法一眼看清“机器上究竟有哪些进程、哪些进程已纳管、哪些纯客户端进程只订阅不发布服务”，且重复展示宿主进程列造成大量视觉噪音。
2. **现行「一键导入」将物理发现强行耦合进单一逻辑子系统，破坏逻辑架构纯净度**：
   - 在现有模型（[`models/arch.go`](../../models/arch.go)）中，`ModuleService` 与 `ModuleDependency` 强绑定 `ModuleID NOT NULL`，而 `Module` 强绑定 `SubsystemID` 与 `FunctionalGroupID`；
   - 因此当前 [`ImportCSGBlueprint`](../../internal/server/topology/csg_reconcile.go) 在导入时要求用户必选一个 `Subsystem`，在该子系统下创建 `CSG_DISCOVERED` 功能组，并按进程名 1:1 自动生成 `MOD_<procName>` 占位模块来挂载 `ModuleService`；
   - 然而，**逻辑架构（业务子系统 / 功能组 / FM 模块拆分）属于人工架构设计范畴，无法通过现网端口和进程直接“发现”**。同台宿主机上的进程往往跨越多个业务子系统，且单个物理进程常承载多个逻辑模块（1 进程 : N 模块），强制 1:1 造假模块会污染逻辑架构树并增加后续拆分成本。

---

## 2. 核心演进方向：进程主轴视图 (Process-Centric Discovery View)

在不改动后端发现协议的前提下，前端「现网服务探测」主表拟从“按服务平铺”演进为 **「以宿主进程为入口列的分组可展开树表」**：

* **一级行（宿主进程）**：
  * 展示：`[勾选框] | 展开箭头 + 宿主进程名 (PID) | 物理纳管与挂载状态 | 提供服务数（如 12 个 · 4 待导入） | 传输协议（TCP / dophi_mesh） | 订阅下游数`；
  * **覆盖纯客户端进程**：即使某进程 `提供服务数 = 0`（仅出现在 `Subscriptions` 中），也能作为一级进程行直观呈现。
* **二级子行（进程下属服务）**：
  * 展开一级进程后展示其发布的所有 64B `ServiceName`、关联 `.pb`、传输与监听地址、已认领逻辑模块（如有）、订阅边数与三态对账徽章；
  * 搜索具体服务名时自动展开命中的进程分组，勾选进程支持批量联动下属服务。

---

## 3. 物理架构与逻辑架构导入边界：三种候选方案对比

针对“一键导入”究竟写入哪些 SSOT 实体，整理以下三种候选方案供后续评审拍板：

| 维度 | 方案 1：纯物理进程导入 + 逻辑延迟认领 | 方案 2：物理进程 + 物理态未认领服务池 | 方案 3：过渡双模（默认只导物理，可选暂存模块） |
| :--- | :--- | :--- | :--- |
| **抽屉底部归属选择** | **归属物理进程组 (`ArchProcessGroup`)**，彻底移除“归属子系统”选择器 | **归属物理进程组 (`ArchProcessGroup`)**，彻底移除“归属子系统”选择器 | 主选**物理进程组**；逻辑子系统折叠至可选复选框下 |
| **一键导入写入范围** | 仅写入 `ArchProcess` + 当前 Agent 挂载 `Process`（不写 `Subsystem` / `FunctionalGroup` / `Module` / `ModuleService`） | 写入 `ArchProcess` + `Process`，并将发现的服务/订阅边先挂入物理进程下（`ModuleID` 为空，标记为待逻辑认领） | 默认仅写 `ArchProcess` + `Process`；仅当显式勾选“同时生成占位逻辑模块”时才写 `Module` / `ModuleService` |
| **逻辑架构纯净度** | **100% 纯净**：物理自动发现、逻辑纯手工设计后通过“认领”挂载 | **100% 纯净**：逻辑架构树不被占位模块污染，且支持 1 进程拆分为 N 逻辑模块逐个认领 | 折中：不勾选时纯净；勾选时保留现行占位模块机制 |
| **导入后即时效果** | 物理进程树与 Agent 探针挂载立即就绪；服务对账与进程间拓扑连线需待逻辑侧建模块并认领后呈现 | 导入后服务对账、录制归因及物理视图进程连线立即可用，后续由架构师在逻辑树将物理服务认领进真实 FM | 不改核心数据库 Schema 即可同时支持两种工作流 |
| **改造成本与风险** | 低~中：需将 CSG 发现服务汇总接入“可认领服务”视图（复用 [`claimable_service.go`](../../internal/server/topology/claimable_service.go) 模式） | 中~高：需演进 `contract_module_services`（允许 `module_id` 可空并新增 `arch_process_id`）及拓扑/归因/认领查询 | 低：改动集中在导入参数与后端条件分支，但会留存过渡期包袱 |

---

## 4. 待进一步设计确认的开放问题 (Open Questions)

正式定稿并实施前，需逐一明确以下 4 个设计问题：

1. **物理视图连线与服务归因的独立性**：当一个物理进程已导入 `ArchProcess` 并挂载到 Agent、但尚未在逻辑架构中拆分出 `Module` 时，物理拓扑画布的边（现由 `ModuleDependency` 聚合）与录制流量归因应如何展示与关联？
2. **进程身份锚定与多实例去重**：现网 `server_pid` 每次重启都会变化，如何根据 `exeName` / `cmdline` / `DefaultCmdPattern` 稳定匹配同一 `ArchProcess`，避免重启后产生重复待导入进程？
3. **CSG 64B 运行期服务名与 `.pb` 契约的统一认领**：现有逻辑侧已有基于激活 `.pb` 的“可认领服务”机制（[`ListClaimableProtoServices`](../../internal/server/topology/claimable_service.go)），如何与“现网 CSG 发现但尚无 `.pb` 的 64B 服务名”合并成统一认领流，避免重复建契约？
4. **纯客户端进程的物理蓝图默认配置**：只订阅下游、不监听端口的进程导入 `ArchProcess` 时（`DefaultListenPort = 0`），其探针就绪检查与出站代理挂载策略是否需要专属标识或默认 `ARMED` 策略提示？

---

## 5. 前置已完成项 (Prerequisite UI Cleanup)

作为本 ADR 演进的前置铺垫，已完成「CSG 现网服务探测与蓝图对账」抽屉的顶栏与汇总区瘦身：
- 将「目标节点」固定为只读展示，移除手动 CSG 地址输入框，保留极简刷新图标按钮；
- 将「候选 CSG 控制面」合并为顶栏单行下拉选择器，选中即自动重探；
- 移除常态「发现模式」展示（仅在 `PROC_ONLY` 降级或失败时通过诊断告警呈现）及顶部 4 张冗余 KPI 卡片，将「已纳管 / 现网数」内联至「现网服务列表」Tab 徽章，释放表格可视空间以承接后续的进程主轴分组树表。
