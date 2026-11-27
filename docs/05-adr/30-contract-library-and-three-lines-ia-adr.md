# ADR-30：按「两条主线 + 独立开发 MR 合规线」重构契约菜单与四阶段流水线工作台

- **文档编号**：`30-contract-library-and-three-lines-ia-adr.md`
- **状态**：Implemented（2026-11-27）
- **日期**：`2026-11-27`
- **关联与演进**：演进 [`ADR-29`](./29-proto-contract-pipeline-closure-and-ia-redesign-adr.md) 与 [`ADR-24`](./24-csg-discovery-process-centric-and-physical-logical-boundary-adr.md)

---

## 1. 背景与决策动因

在 ADR-29 落地四阶段流水线后端引擎后，终端用户视角仍存在 4 个信息架构断层：
1. **主线 1（Git 代码仓 → 契约库）半截空洞**：`ProtoPipelineWorkbenchPage` 阶段 ①、阶段 ② 仅有 KPI 数字而无代码仓台账，用户仍需跳到 `/proto-sources` 才能查看与治理单仓；而 `/proto-sources` 页面内又保留了“仓库导出与治理”假 Tab 及生产写操作按钮。
2. **「契约库」消费入口分散**：接口文档与只读源码分属两个平级菜单，而属于“架构对齐”的服务认领与五态雷达又混在文档页内部且两张表上下堆叠。
3. **开发 MR 合规线与后台批处理流水线混淆**：管理员周期运行的“入库流水线”与研发日常关心的“规范与 MR 门禁”混排在同一分组。
4. **菜单命名歧义**：`契约采集` 实际承载的是流量录制，易与 Git 契约源采集混淆。

---

## 2. 核心架构决策

围绕**两条主线 + 一条横切合规线**重组左侧菜单为 **6 组 14 项**：

| 侧边栏分组 | 对应主线 | 二级菜单与路由 | 核心职责 |
| :--- | :--- | :--- | :--- |
| **全局概览** | 总览 | `治理大盘` (`/dashboard`) | 全系统健康分、漂移雷达、认领率与合规概览 |
| **契约库与构建** | **主线 1（生产）**<br>**主线 2A（本体消费）** | `契约入库流水线` (`/proto-pipeline`)<br>`契约库` (`/proto-docs`) | ① 完整承载四阶段仓库拉取/清洗目录/全池仲裁/编译发布与生效版本台账；<br>② 纯只读消费「全注释参考文档 · 契约源码 · 跨包依赖关系图」，并挂接架构与流量联动栏 |
| **架构与契约对齐** | **主线 2B（架构关联）** | `契约认领与雷达` (`/proto-catalog`)<br>`架构元素` (`/architecture`)<br>`现网发现` (`/discovery`)<br>`架构拓扑` (`/topology`) | 从文档页抽离并拆分为「五态健康雷达 / 服务方法认领目录」双正交视图，专注三方对齐 |
| **流量核验与回归** | **主线 2C（流量支撑）** | `流量采集` (`/record`)<br>`事务分析` (`/debug`)<br>`防劣化回归` (`/replay`)<br>`黄金用例库` (`/golden-suites`) | 更名 `流量采集` 消除歧义，服务文档页一键直达 |
| **开发合规** | **开发 MR 合规线** | `规范与 MR 门禁` (`/proto-governance`) | 独立承载 Git MR 四合一门禁、静态规则、词库与体检报告 |
| **系统与环境** | 底座运维 | `Agent 与进程` (`/agents`)<br>`模块参数配置` (`/admin/settings`) | 采集节点与系统级参数管理 |

### 关键路由收敛与无损重定向
- `/proto-sources?view=governance` → `/proto-pipeline`
- `/proto-sources`（其余参数）→ `/proto-docs?docs_view=sources`（保留 `file_id` / `repo_id` / `bundle_id` / `#L<n>`）
- `/proto-docs?docs_view=catalog|catalog-ops|unified-catalog` 或 `?state=*` / `?claimed_status=UNCLAIMED`（未显式指定 `reference|sources|graph`）→ `/proto-catalog`
- `/schemas?tab=sources` → `/proto-docs?docs_view=sources`；`/schemas?tab=catalog*` → `/proto-catalog`；`/schemas` 默认或 `?tab=assets` → `/proto-pipeline?stage=publish`
