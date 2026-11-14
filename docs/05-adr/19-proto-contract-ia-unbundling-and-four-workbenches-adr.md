# ADR-19: Proto 契约控制台信息架构拆解与四工作台解耦决策 (Proto IA Unbundling & Four Workbenches)

* **状态**: ✅ 已采纳与交付 (M27)
* **日期**: 2026-11-15
* **关联特性**: `F-15`, `F-18`, `F-25`, `F-28`, `F-29`, `F-30`, `F-31`, `F-32`
* **替代/演进关系**: 继承并演进 `ADR-07`（控制台 IA 重构）与 `ADR-18`（Proto 全生命周期与 Git MR 门禁治理）

---

## 1. 背景与痛点分析

随着 `M23` ~ `M26` 的交付，CodeTraefik 围绕 Protobuf 契约沉淀了四类复杂度与受众完全不同的能力：

1. **接口查阅与架构认领**（面向全员开发者 / 客户端联调）：全注释接口参考文档、服务检索、Markdown 手册导出、`@em` 枚举检索、契约认领与方法目录映射。
2. **源码管理与在线协作**（面向契约维护者 / 后端研发）：Git 仓库集成、外部公共库同步、在线 `.proto` 源码直编、只读源码 `#L<n>` 精确定位。
3. **研发流程门禁与静态规范**（面向架构师 / 质量守门人）：Git MR 门禁流水台账、Breaking Change 与命名规范体检、领域标准词库、AST 跨仓重复簇扫描、合规评分卡。
4. **底层描述集与依赖资产**（面向平台管理员 / 底层运维）：`.pb` DescriptorSet 文件上传/启停/批量激活/清理、全局资产汇总栏、跨包 Import 依赖拓扑图。

此前上述四大域全部堆叠在单一左侧菜单 `Proto 契约`（`/schemas`）下的**5 个一级标签页 + 顶部全局汇总栏 + 右上角次级胶囊栏 + 大尺寸抽屉**中，造成四重结构性痛点：

* **角色与场景混杂**：只查接口文档的日常研发被迫跨越重管理员操作（`[上传描述集 (.pb)]`、`[在线源码直编 (.proto)]`）和全局池统计条才能看到业务文档。
* **导航与心智负担过重**：三层嵌套导航（门户菜单 → 5 个主 Tab → 二级胶囊/抽屉）导致关键能力如「静态检查规则 / 标准词库 / AST 重复簇」长期隐藏在 960px 抽屉里，缺乏直接 URL 锚点。
* **首屏空间挤压与微前端高亮失焦**：`/schemas?tab=*` 共享同一路径前缀，CodeBench 宿主侧边栏无法根据当前所处能力域给出精确的导航高亮反馈，也不利于团队按需直达与浏览器历史回溯。
* **模块结构耦合**：`ProtoSchemaWorkbenchPage.tsx` 承载过重，混合 5 个业务视图状态、Modal 与 Drawer 状态。

---

## 2. 决策内容

### 2.1 全局侧边栏菜单升维：5 组 13 项金字塔结构

将原第 3 组 `架构与契约` 拆分为 `架构数字孪生` 与独立的 **`Proto 契约治理`** 一级分组，全局侧边栏升级为 **5 组 13 项**：

| 排序 | 一级分组 (`GroupTitle`) | 二级菜单 (`Label`) | 路由 (`Path`) | 权限 | 定位与受众 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| 1 | **全局概览** | `治理大盘` | `/dashboard` | 全员 | 全局契约健康度、静检趋势与门禁雷达 |
| 2 | **架构数字孪生** | `架构拓扑` | `/topology` | 全员 | 子系统/进程拓扑与实时链路调用 |
| 3 | **架构数字孪生** | `架构元素` | `/architecture` | 全员 | 子系统、进程与代码模块数字孪生台账 |
| 4 | **Proto 契约治理** | `接口参考文档` | `/proto-docs` | 全员 | 高频日常查询：全注释文档、Markdown 导出、认领与目录 |
| 5 | **Proto 契约治理** | `源码与 Git 仓` | `/proto-sources` | 全员 | 只读源码 `#L<n>` 精确定位、Git/外部仓管理、在线源码直编 |
| 6 | **Proto 契约治理** | `MR 门禁与规范` | `/proto-governance` | 全员 | MR 门禁流水、静态检查规则、标准词库、AST 重复簇扫描 |
| 7 | **Proto 契约治理** | `描述集与依赖` | `/schemas` | 全员 | 底层 `.pb` 描述集资产台账、`.pb` 上传启停与跨包依赖图 |
| 8 | **契约核验与回归** | `契约采集` | `/record` | 全员 | 流量采集与契约录制任务 |
| 9 | **契约核验与回归** | `事务分析` | `/debug` | 全员 | 抓取事务帧级解码与对比分析 |
| 10 | **契约核验与回归** | `防劣化回归` | `/replay` | 全员 | 契约回归回放引擎与基线对比 |
| 11 | **契约核验与回归** | `黄金用例库` | `/golden-suites` | 全员 | 黄金回归用例集与契约基线管理 |
| 12 | **环境与治理** | `Agent 与进程` | `/agents` | 全员 | Edge Agent 在线状态与采集进程监控 |
| 13 | **环境与治理** | `模块参数配置` | `/admin/settings` | `adminOnly` | 全局治理参数、规则开关与通知渠道配置 |

> 注：CodeBench 宿主侧边栏通过 `@originjs/vite-plugin-federation` 拉取 `./menu`，并基于 `location.pathname` 前缀匹配高亮项；四个独立根路由可实现原生精确高亮与独立的面包屑标题。

### 2.2 四工作台职责正交拆分

| 工作台组件 | 路由 | 顶部资产汇聚条 (`SchemaPoolSummaryBar`) | 视图/子页签设计 | 核心职责与视觉降噪效果 |
| :--- | :--- | :--- | :--- | :--- |
| `ProtoDocsWorkbenchPage` | `/proto-docs` | ❌ 移除 | 双胶囊切换：`全注释参考文档` (`reference`) / `契约认领与方法目录` (`catalog-ops`)，URL 同步 `docs_view` | 极致压缩首屏冗余 Chrome；点击源码锚点通过只读代码查看器抽屉即时定位 `#L<n>` 或跳转 `/proto-sources` |
| `ProtoSourcesWorkbenchPage` | `/proto-sources` | ❌ 移除 | 单视图直达 `GitSourcesWorkbenchPane` | Git/外部公共仓导入同步、目录浏览、只读源码 `#L<n>` 高亮、在线源码直编入口 |
| `ProtoGovernanceWorkbenchPage` | `/proto-governance` | ❌ 移除 | 4 个 URL 同步子标签 (`gov_tab`)：`Git MR 门禁流水` (`mr-gates`) / `静态检查规则` (`rules`) / `领域标准词库` (`vocab`) / `体检报告与 AST 重复簇` (`report`) | 将原隐藏在 960px 抽屉里的规则、词库与 AST 重复簇解放为平等的全屏一级页面；保留抽屉组件 `LintRuleAndVocabDrawer` 供向后兼容，核心内容抽离为可复用 `LintRuleAndVocabPanel` |
| `ProtoSchemaWorkbenchPage` | `/schemas` | ✅ 保留 | 2 个标签 (`tab`)：`描述集资产台账` (`assets`) / `跨包依赖关系图` (`graph`)，右侧保留前往 `/proto-docs` 与 `/proto-governance` 的快捷导流 | 聚焦 `.pb` 资产全生命周期管理（上传、启停、批量激活、删除、导出）与跨包依赖拓扑 |

### 2.3 无损深链兼容与平滑重定向契约 (`SchemasRouteWrapper`)

所有历史上已散落于后端站内信通知（`internal/server/gitclient/mr_runner.go`）、Markdown 参考手册跨页链接、大盘跳转及架构拓扑服务详情页的 `/schemas?tab=...` 旧深链必须实现**零断链自动重定向（保留全部查询参数与 `#L<n>` Hash 锚点）**：

| 旧路径 / 查询条件 | 自动重定向目标 | 保留的参数 / Hash |
| :--- | :--- | :--- |
| `/schemas?tab=docs` | `/proto-docs`（补齐 `docs_view=reference` 若无） | `service`, `schema_id`, `search`, `#hash` 等 |
| `/schemas?tab=catalog` | `/proto-docs?docs_view=catalog-ops` | `claimed_status`, `schema_id`, `search`, `#hash` 等 |
| `/schemas?tab=sources` | `/proto-sources` | `file_id`, `schema_id`, `source_id`, `search`, `#L<n>` 等 |
| `/schemas?tab=mr-gates` | `/proto-governance?gov_tab=mr-gates` | `runId`, `mr_page`, `mr_page_size`, `repo`, `#hash` 等 |
| `/schemas?tab=lint-rules` / `lint-vocab` / `lint-report` | `/proto-governance?gov_tab=rules\|vocab\|report` | 全部其他查询参数与 `#hash` |
| `/schemas`（无 `tab` 或 `tab=assets` / `tab=graph`） | 留在本页渲染 `ProtoSchemaWorkbenchPage` | `tab`, `active_only`, `search`, `schema_id` 等 |

> 宿主页嵌入前缀（`/traefik/*`）与独立开发模式（`/*`）共享同一 `SchemasRouteWrapper` 重定向逻辑，通过 `resolveModulePath` + `#hash` 拼接保证双模一致。

---

## 3. 后果与影响分析

* **正面收益**：
  * 高频阅读型任务（查文档、看报错行号）与重管控型任务（传 `.pb`、配词库、审 MR）彻底物理分流，首屏垂直有效工作区提升显著。
  * 静态规则、词库、AST 重复簇获得可分享、可书签化的独立 URL（`/proto-governance?gov_tab=rules|vocab|report`）。
  * 宿主侧边栏导航高亮与实际业务工作台 100% 语义对齐。
  * `ProtoSchemaWorkbenchPage.tsx` 体积大幅精简，代码职责单一、可维护性提升。
* **代价与约束**：
  * 新增 3 个页面入口与路由，需要同步更新菜单单元测试、重定向单测与工作台单测。
  * 未来新增 Proto 相关前端能力时，必须明确归属上述 4 个工作台之一，禁止回流堆叠单页多 Tab。
