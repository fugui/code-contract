# ADR-32：五线活体契约飞轮动线校准、双闸门可视拉通、破坏性变更 MR 溯源与黄金用例反向滋养架构决策

- **文档编号**：`32-five-lines-flywheel-ux-dual-gate-and-audit-traceability-adr.md`
- **状态**：Proposed（2026-11-28，待评审后分期实施）
- **日期**：`2026-11-28`
- **修订**：`2026-11-28` 代码实证复核修订——补齐 `CI_GATE` 任务可见性断层、门禁前置拦截全集与 `RUNNING` 态、Squash/回滚/门禁漏检场景下的七态归因与异步化、零评估回归子分与在途任务口径。
- **关联与演进**：`ADR-03`、`ADR-14`、`ADR-18`、`ADR-24`、`ADR-29`、`ADR-30`、`ADR-31`

---

## 1. 背景、产品评审与代码现状校准

`v0.5.0` 完成五大核心主线文档体系收敛、非阻断式上线破坏性变更审计归口（[`handlers/breaking_audit_handler.go`](file:///home/fugui/codes/code-contract/handlers/breaking_audit_handler.go)）以及 RPC 离线重放端到端审计（`ADR-31`）后，产品侧围绕“五线飞轮”提出了 4 项用户动线与闭环演进建议。

在正式进入排期前，我们对控制面后端、数据模型与微前端代码进行了逐项实证核验，区分出 **「已实现能力」、「真实断层」与「工程风险」**：

| 维度 | 现网代码实现基线 | 真实断层 / 需校准点 |
| :--- | :--- | :--- |
| **菜单与信息架构** | [`frontend/src/menu.ts`](file:///home/fugui/codes/code-contract/frontend/src/menu.ts) 按 `ADR-30` 组织为 6 组 14 项；主线 3（采集/调试）与主线 4（黄金套件/重放）已合并在「流量核验与回归」分组，主线 5 独立为「开发合规」。 | ① 「架构与契约对齐」分组内 `现网发现` 排在第 3 位，与 `ADR-24` Discovery-First 因果链逆序；<br>② 「流量核验与回归」中 `防劣化回归 (/replay)` 排在 `黄金用例库 (/golden-suites)` 之前，与“采 → 析 → 存 → 验”沉淀顺序倒置。 |
| **设计态 MR 与运行态 CI 双闸门** | 设计态 MR 门禁已拥有完整页面 `/proto-governance`；运行态 CI 门禁 [`POST /api/gates/evaluate`](file:///home/fugui/codes/code-contract/handlers/gate_handler.go#L60-L360) 目前为纯 API，通过时生成 `trigger_source='CI_GATE'` 的 `ReplayTask`（[`models/replay.go`](file:///home/fugui/codes/code-contract/models/replay.go#L14)）。 | ① [`ReplayTaskListQuery`](file:///home/fugui/codes/code-contract/internal/server/diff/scheduler.go#L51-L60) 与 [`ReplayWorkbenchPage.tsx`](file:///home/fugui/codes/code-contract/frontend/src/pages/ReplayWorkbenchPage/ReplayWorkbenchPage.tsx) 不支持按 `trigger_source` 筛选和展示（页面仅同步 `status` / `session_id` / `auto_create` 三个 URL 参数）；<br>② **可见性断层**：[`ReplayScheduler.ListTasks`](file:///home/fugui/codes/code-contract/internal/server/diff/scheduler.go#L796-L800) 对非管理员强制 `owner_id = 当前用户`，而 `CI_GATE` 任务的 owner 为 CI Token 调用方，普通研发**即使加了筛选也看不到任何 CI 门禁任务**；<br>③ 环境离线/进程未就绪（`503 GATED_ENV_NOT_READY`）、租约等待超时（`503 GATED_ENV_BUSY`）、套件缺失（`404`，以 `code: SUITE_NOT_FOUND` 返回）、非 `SHARED` 环境（`403`）、目标未挂载或套件为空（`400`）、CSG 冲突（`409 CSG_SERVICE_ALREADY_PUBLISHED`）/ Mesh 不支持（`422 CSG_DATAPLANE_MESH_UNSUPPORTED`）等拦截**均不产出可检索的门禁凭证**，控制台存在盲区。 |
| **破坏性变更审计溯源** | [`models.BreakingChangeEvent`](file:///home/fugui/codes/code-contract/models/breaking_audit.go#L22-L46) 已记录 `Source`（`PIPELINE_PUBLISH` / `MANUAL_ACTIVATE` / `BATCH_ACTIVATE` / `AUTO_ACTIVATE`）、`PublishRunID`、`GitRepoID`、`BundleID`、`FromSchemaID`/`ToSchemaID` 及合规规则明细，但在 `persistBreakingChangeObservation`（[`internal/server/decoder/breaking.go`](file:///home/fugui/codes/code-contract/internal/server/decoder/breaking.go#L990-L1068)，于激活事务内被 `registry.go` 调用）中未抄录 Commit SHA 或 MR 信息。 | ① 缺少 `from_commit_sha` / `to_commit_sha` 与 `MRCheckRun` 关联字段，无法直接判定该次上线不兼容是“经 MR Override 豁免合入”、“MR 门禁失败强合”、“MR 门禁漏检”、“人工回滚”还是“绕过 MR 直推”；<br>② 顺带发现 [`models/db.go`](file:///home/fugui/codes/code-contract/models/db.go#L20-L65) 的 `BusinessTableNames()` 尚未补录 `BreakingChangeEvent{}.TableName()`。 |
| **黄金用例反向滋养契约库与大盘** | **已实现**：[`MatchGoldenSamples`](file:///home/fugui/codes/code-contract/internal/server/protodoc/sample_json.go#L202-L255) 与 [`ServiceReferenceDocView.tsx`](file:///home/fugui/codes/code-contract/frontend/src/pages/ProtoSchemaWorkbenchPage/ServiceReferenceDocView.tsx#L162-L306) 已将 `ACTIVE` 黄金套件真实 JSON 样本（按 `service_name + operation` 等值匹配、最多 5 条）投射至 `/proto-docs` 方法卡片并支持回切 AST 缺省样例。 | ① **跳转断链 Bug**：`/proto-docs` 跳 `/golden-suites?search=<serviceName>` 时，[`ListGoldenSuites`](file:///home/fugui/codes/code-contract/handlers/golden_suite_handler.go#L55-L58) 仅搜套件 `code/name/description`，不匹配 `GoldenSuiteItem.service_name`；<br>② 前端硬编码只取 `golden_samples[0]`，无法浏览其余样本；<br>③ `/proto-docs` 与 `/proto-catalog` 缺少黄金覆盖率与最近回归健康态；<br>④ **零用例满分（两处）**：[`overview.go`](file:///home/fugui/codes/code-contract/internal/server/dashboard/overview.go#L497-L503) 在 `evalCount == 0` 时把 `PassRate30d` 置为 `1.0`（100%）；且回归子分 [`regSubScore = 30 - unhealthyProcs7d*10`](file:///home/fugui/codes/code-contract/internal/server/dashboard/overview.go#L524-L528) 在零评估时同样拿满 30 分；<br>⑤ **在途任务误计阻断**：`blockedCount` 以 `ResultStatus != PASS` 计数，执行中（`result_status=''`）的任务被计为阻断；且 `gate_evaluations_30d` 实际统计 `GOLDEN_SUITE + CI_GATE` 两类权威回归，并非仅 CI 门禁。 |

---

## 2. 决策一：顺应因果链的菜单顺序校准（演进 ADR-30）

维持 `ADR-30` 定义的 **6 组 14 项**（Phase 3 引入独立 CI 门禁视图后扩展为 6 组 15 项）整体骨架不变，调整以下两个业务分组内的二级菜单排列顺序：

1. **「架构与契约对齐」分组顺序调整**：
   - 调整前：`契约认领与雷达 (/proto-catalog)` → `架构元素 (/architecture)` → `现网发现 (/discovery)` → `架构拓扑 (/topology)`
   - 调整后：**`现网发现 (/discovery)` → `契约认领与雷达 (/proto-catalog)` → `架构元素 (/architecture)` → `架构拓扑 (/topology)`**
   - **设计依据**：呼应 `ADR-24` Discovery-First 工作流——新用户先看现网实况与 `Enroll`，再到目录雷达完成 `.pb` 撮合与认领，继而管理逻辑模块，最后在单层平铺拓扑核验结果。
   - **静态先行团队兜底约束**：针对尚未部署 Agent、先梳理 `.proto` 与逻辑架构的团队，`/discovery` 空态页面必须提供清晰的双引导卡片（“去「Agent 与进程」接入现网节点” + “跳过现网，先前往「契约认领与雷达」进行静态认领”），避免首次点击落空。
2. **「流量核验与回归」分组顺序调整**：
   - 调整前：`流量采集 (/record)` → `事务分析 (/debug)` → `防劣化回归 (/replay)` → `黄金用例库 (/golden-suites)`
   - 调整后：**`流量采集 (/record)` → `事务分析 (/debug)` → `黄金用例库 (/golden-suites)` → `防劣化回归 (/replay)`**
   - **设计依据**：严格映射 **「采 → 析 → 存 → 验」** 的测试资产沉淀顺序（先录制流量、再解剖与固化高价值事务为黄金套件，最后在回归台集中发起与查看批次/套件/CI 重放结论）。

**受影响基线清单**：[`frontend/src/menu.ts`](file:///home/fugui/codes/code-contract/frontend/src/menu.ts#L46-L94)、[`frontend/src/__tests__/MenuAndLegacyRoutes.test.tsx`](file:///home/fugui/codes/code-contract/frontend/src/__tests__/MenuAndLegacyRoutes.test.tsx#L35-L44)、[`docs/05-adr/30-contract-library-and-three-lines-ia-adr.md`](file:///home/fugui/codes/code-contract/docs/05-adr/30-contract-library-and-three-lines-ia-adr.md)。

---

## 3. 决策二：设计态 MR 门禁与运行态 CI 门禁双闸门拉通

系统面向研发改动的两道质量闸门目前处于“静态闸门重可视化、动态闸门纯 API 化”的不对称状态：

```mermaid
flowchart LR
    subgraph GateA["闸门 A：设计态静态门禁 (改 .proto)"]
        MR["Git MR / PR Webhook"] -->|"POST /api/webhooks/git-mr"| MRRun["MRCheckRun (contract_mr_check_runs)"]
        MRRun -->|"四合一静态预检 + Override"| GovUI["/proto-governance (规范与 MR 门禁)"]
    end

    subgraph GateB["闸门 B：运行态动态门禁 (改业务代码)"]
        CI["CI/CD 构建流水线"] -->|"POST /api/gates/evaluate"| GateRun["CIGateRun (contract_ci_gate_runs)"]
        GateRun -->|"环境与套件预检通过"| Replay["ReplayTask (trigger_source=CI_GATE)"]
        GateRun -->|"前置拦截 (ENV_NOT_READY / ENV_BUSY / SUITE_NOT_FOUND / REJECTED)"| GateUI["/ci-gates (CI 回归门禁视图)"]
        Replay -->|"架构漂移 + 黄金套件 Diff"| GateUI
        Replay -->|"回归明细与双层 Diff 穿透"| ReplayUI["/replay?trigger_source=CI_GATE&task_id=id"]
    end
```

### 3.1 信息架构裁决：不向 `/proto-governance` 硬塞第 6 个 Tab
[`ProtoGovernanceWorkbenchPage.tsx`](file:///home/fugui/codes/code-contract/frontend/src/pages/ProtoSchemaWorkbenchPage/ProtoGovernanceWorkbenchPage.tsx#L150-L207) 当前已承载 `Git MR 门禁流水`、`破坏性变更审计`、`静态检查规则`、`领域标准词库`、`体检报告与 AST 重复簇` **5 个静态规范 Tab**，操作上下文均围绕 `.proto` 源码与 Git 仓。若将依赖 `ArchProcess` + 共享测试 `Agent` + `GoldenSuite` 的运行态 CI 回归强行并入同一 Tab 栏，会造成受众混乱与标签溢出。因此采用**分两期演进**策略：

### 3.2 Phase A（P0 轻量拉通，零新增表）
1. **后端检索增强**：扩展 [`ReplayTaskListQuery`](file:///home/fugui/codes/code-contract/internal/server/diff/scheduler.go#L51-L60) 增加 `TriggerSource`（`form:"trigger_source"`，大写归一后校验属于 `MANUAL | SINGLE_DEBUG | GOLDEN_SUITE | CI_GATE`）与 `ArchProcessID`（`form:"arch_process_id"`）过滤，使 `GET /api/replay-tasks` 支持精准筛选。
2. **CI 门禁任务只读可见性（修复可见性断层）**：[`ListTasks`](file:///home/fugui/codes/code-contract/internal/server/diff/scheduler.go#L796-L800) 的非管理员可见条件由 `owner_id = ?` 放宽为 `owner_id = ? OR trigger_source = 'CI_GATE'`（CI 门禁仅允许 `SHARED` 环境执行，属团队共享权威凭证）；`GetTask` / `ListItems` / 单条明细同步采用同一谓词。取消（`/cancel`）与重新比对（`/rediff`）等写操作仍仅限 owner 或 `contract_admin`。
3. **重放工作台可见性**：在 [`ReplayWorkbenchPage.tsx`](file:///home/fugui/codes/code-contract/frontend/src/pages/ReplayWorkbenchPage/ReplayWorkbenchPage.tsx) 顶部筛选条增加 `trigger_source` 下拉筛选，并与 URL 查询参数 `trigger_source` **双向同步**（与现有 `status` / `session_id` 同模式，保证深链可直达）；在 [`ReplayTaskTable.tsx`](file:///home/fugui/codes/code-contract/frontend/src/pages/ReplayWorkbenchPage/ReplayTaskTable.tsx) 增加「触发来源」徽标列与「架构违规边数 (`violation_count`，`ReplayTask` 已有字段)」摘要列。
4. **大盘穿透**：治理大盘 [`RegressionDefenseCard`](file:///home/fugui/codes/code-contract/frontend/src/pages/DashboardWorkbenchPage/DashboardWorkbenchPage.tsx#L347-L410) 增加「查看 CI 门禁与权威回归流水」直达按钮，跳转 `/replay?trigger_source=CI_GATE`。

### 3.3 Phase B（P2 完整双闸门台账，覆盖前置拦截态）
1. **新增 `contract_ci_gate_runs`（`models.CIGateRun`）**：
   - **落库时机**：[`EvaluateGate`](file:///home/fugui/codes/code-contract/handlers/gate_handler.go#L60-L360) 在请求体 JSON 绑定与必填 ID 校验通过后，**立即插入一条 `RUNNING` 流水**（评估为同步阻塞，最长可达 `timeout_seconds ≤ 300s` + `wait_lease_seconds ≤ 300s`），并以 `defer` 在任一出口回写终态；请求体解析失败（无可追溯主体）不落库。主控重启时将遗留 `RUNNING` 流水对账置为 `ERROR`（遵循 `ADR-03` 控制面状态恢复）。
   - **状态全集 `gate_status`**：`RUNNING | PASSED | GATED_VIOLATION | GATED_BREAKING_DIFF | GATED_ENV_NOT_READY | GATED_ENV_BUSY | SUITE_NOT_FOUND | REJECTED | ERROR`（CHECK 约束）。其中 `REJECTED` 覆盖其余前置校验失败，并以 `reject_code` 细分：`ARCH_PROCESS_NOT_FOUND`、`AGENT_NOT_FOUND`、`SCOPE_NOT_SHARED`、`PROCESS_NOT_MOUNTED`、`SUITE_EMPTY`、`CSG_SERVICE_ALREADY_PUBLISHED`、`CSG_DATAPLANE_MESH_UNSUPPORTED`。
   - **核心字段**：`id`、`gate_run_no`、`arch_process_id`、`env_agent_id`（二者保存**请求值**快照，不建外键，因前置失败时目标可能不存在）、`arch_process_name` / `agent_name` 快照、`golden_suite_id` 与 `replay_task_id`（可空外键，`ON DELETE SET NULL`）、`gate_status`、`reject_code`、`http_status`、`error_message`、`violation_count`、`passed_requests`、`total_requests`、`duration_ms`、`markdown_report`，以及可选调用方上下文（`caller_repo`、`caller_commit_sha`、`caller_mr_iid`、`pipeline_url`）。
   - **向后兼容**：`GateEvaluateReq` 新增的可选上下文参数均为非必填；**既有 HTTP 状态码与响应体字段保持不变**，保证既有 CI 脚本 100% 零破坏兼容；执行触发仍严格保留 `CIOrAdminGuard()`。
2. **新增只读门禁控制台路由与菜单**：
   - 新增只读接口 `GET /api/ci-gate-runs` 与 `GET /api/ci-gate-runs/:id`（常规认证守卫）。可见性：环境 Agent 为 `SHARED` 的流水对所有已认证用户可见；`SCOPE_NOT_SHARED` 等指向 `PRIVATE` Agent 的拒绝流水仅该 Agent owner 与 `contract_admin` 可见。
   - 在「开发合规」分组下新增第 2 个平级菜单 **`CI 回归门禁` (`/ci-gates`)**，与 `规范与 MR 门禁` (`/proto-governance`) 构成“静态设计闸门 + 动态运行闸门”完整双翼，支持查看每次评估的 `MarkdownReport` 凭证，并一键穿透 **`/replay?trigger_source=CI_GATE&task_id=<replay_task_id>`**（`ReplayWorkbenchPage` 新增 `task_id` URL 参数，直接打开 `ReplayReportDrawer`；现有 `/debug` 不支持按重放任务定位，不作为穿透目标）。

---

## 4. 决策三：上线破坏性变更审计与 MR 门禁多态关联溯源

### 4.1 必须规避的 4 个 Git 归因陷阱
[`models.BreakingChangeEvent`](file:///home/fugui/codes/code-contract/models/breaking_audit.go#L22-L46) 目前没有 Commit SHA 字段，但落地溯源时**严禁采用 `ProtoSchema.SourceCommitSHA == MRCheckRun.HeadCommitSHA` 的简单等值匹配**，否则会在以下 4 类真实场景中高频误报“绕过 MR 直推主干”：
1. **Squash Merge / Merge Commit 导致 SHA 漂移**：`MRCheckRun(MR_GATE)` 记录的是特性分支 `head_commit_sha`（[`models/mr_gate.go`](file:///home/fugui/codes/code-contract/models/mr_gate.go#L36)）。合入后主干生成新的 Squash/合并 Commit；**Squash 场景下特性分支提交根本不在主干祖先链中**，因此无论等值匹配还是 `git log from..to` 与 `MR_GATE.head_commit_sha` 求交集都必然落空，必须先把主干提交解析为 **MR IID**，再按 `(git_repo_id, mr_iid)` 回查 `MR_GATE` 运行记录。
2. **多 MR 批量聚合发布**：阶段 ④ 发布（[`workspace_publisher.go`](file:///home/fugui/codes/code-contract/internal/server/gitclient/workspace_publisher.go#L664)）以 `repo.LastSyncCommit` 作为 `SourceCommitSHA`，若 `FromVersion → ToVersion` 跨度内合入了多个 MR，破坏性改动可能位于区间中段的某个 MR，而非 `HEAD` 提交。
3. **非源码改动引发破坏性 Diff**：管理员修改全局 Stage B 清洗规则、调整 `ProtoImportOwner` 跨仓权威归属或上游 `BASE_LIB` 升级时，即使当前仓 `from_commit_sha == to_commit_sha`，或区间内提交**未触及本 Bundle 的 proto 路径**，也会触发新版本编译并产生 `BreakingChangeEvent`，此类事件绝非“绕过 MR 直推”。
4. **人工 / 批量激活与历史版本回滚**：`Source = MANUAL_ACTIVATE / BATCH_ACTIVATE` 时，管理员可能激活一个**更旧**的版本（`to_sha` 是 `from_sha` 的祖先），此时 `from..to` 区间为空或反向，若不单独识别将被误判；其责任主体已由 `Source + TriggeredBy` 明确记录。

### 4.2 数据模型扩展与持久化补漏
1. **扩展 `models.BreakingChangeEvent`（[`models/breaking_audit.go`](file:///home/fugui/codes/code-contract/models/breaking_audit.go)）**：
   - `FromCommitSHA` (`size:64`, `not null default ''`) 与 `ToCommitSHA` (`size:64`, `not null default ''`, 建索引)：在 [`persistBreakingChangeObservation`](file:///home/fugui/codes/code-contract/internal/server/decoder/breaking.go#L990-L1068) 中直接从 `active.SourceCommitSHA` 与 `target.SourceCommitSHA` 抄录（纯字段拷贝，零额外 I/O）。
   - `AttributionStatus` (`size:32`, 默认 `'UNATTRIBUTED'`, CHECK 约束 7 态枚举，见 4.3)、`AttributionSummary` (`type:text`) 与 `AttributedAt` (`*time.Time`，`NULL` 表示尚未完成归因计算，用于区分“未计算”与“已计算但无法归因”)。
   - `PrimaryMRRunID` (`*uint64`, `REFERENCES contract_mr_check_runs(id) ON DELETE SET NULL`)、`PrimaryMRIID` (`*int`)、`RelatedMRRunIDsJSON` (`jsonb`, `[]uint64`, 默认 `'[]'`)。
2. **归因异步化（不得阻塞或拖长激活事务）**：`persistBreakingChangeObservation` 运行在 `registry.go` 激活事务内，**事务内只做 SHA 抄录**；共享 Git 缓存的祖先判定、`git log` 与 MR 回查一律在事务提交后异步执行（尽力而为，失败仅写 `UNATTRIBUTED` + `AttributionSummary` 原因，并置 `AttributedAt`），严格守住 Non-Goal 2 的非阻断定位。
3. **存量回填与重算入口**：幂等迁移按 `from_schema_id` / `to_schema_id` 关联 `contract_proto_schemas.source_commit_sha` 回填存量事件的 SHA（`AttributedAt` 保持 `NULL`）；新增 `POST /api/breaking-change-events/:id/reattribute`（`requireAdmin`）与批量重算任务，用于 Webhook 迟到或 Git 缓存补齐后重新归因。
4. **清表 SSOT 补全**：同步将 `BreakingChangeEvent{}.TableName()`（及 Phase 3 的 `CIGateRun{}.TableName()`）补充进 [`models.BusinessTableNames()`](file:///home/fugui/codes/code-contract/models/db.go#L20-L65)。该列表按“外键依赖拓扑倒序”排列：`BreakingChangeEvent` 必须位于 `MRCheckRun` 之前，`CIGateRun` 必须位于 `ReplayTask` 与 `GoldenSuite` 之前。

### 4.3 七态归因状态机与匹配算法

**MR IID 解析来源（按优先级合并去重）**：① `target.SourceMRIID` 精确命中（`MAIN_MERGE` 自动入池路径）；② `event_phase='MAIN_MERGE'` 且 `head_commit_sha` 属于区间提交集合的 `MRCheckRun`；③ 区间提交的 Squash/合并提交信息中的 MR 引用（如 GitLab `See merge request <path>!<iid>`）；④ Merge Commit 工作流下，`event_phase='MR_GATE'` 且 `head_commit_sha` 出现在区间祖先链中的 `MRCheckRun`。每个 IID 取其**最新一次 `MR_GATE` 运行**的 `status` 作为门禁结论。

```mermaid
flowchart TD
    Start["生成 BreakingChangeEvent (from_sha, to_sha, repo_id, bundle_id)"] --> HasMeta{"具备 repo_id、to_sha 非空<br/>且提交存在于共享 Git 缓存?"}
    HasMeta -- "否 (无基线/非 Git 来源/历史被改写)" --> Unattr["UNATTRIBUTED<br/>(无法归因)"]
    HasMeta -- "是" --> SameSHA{"from_sha 非空且<br/>from_sha == to_sha?"}
    SameSHA -- "是 (仓内源码未变)" --> PipeDep["PIPELINE_OR_DEP_CHANGE<br/>(清洗规则/导入仲裁/上游依赖变更)"]
    SameSHA -- "否" --> Rollback{"to_sha 是 from_sha<br/>的严格祖先?"}
    Rollback -- "是" --> ManualRb["MANUAL_ROLLBACK<br/>(人工激活历史版本)"]
    Rollback -- "否" --> Commits["git log from_sha..to_sha -- Bundle proto 路径<br/>得到触及本 Bundle 的提交集合 C"]
    Commits --> EmptyC{"C 为空?"}
    EmptyC -- "是" --> PipeDep
    EmptyC -- "否" --> Resolve["按 IID 解析来源将 C 映射到 MR,<br/>每个 IID 取最新 MR_GATE 结论"]
    Resolve --> AnyFailed{"存在 FAILED 且未豁免的 MR?"}
    AnyFailed -- "是" --> FailedMerged["MR_FAILED_MERGED<br/>(MR 门禁失败但强合主干)"]
    AnyFailed -- "否" --> Uncovered{"C 中存在未被任何 MR 覆盖的提交?"}
    Uncovered -- "是" --> DirectPush["DIRECT_PUSH_SUSPECTED<br/>(疑似绕过 MR 直推或未接入 MR Webhook)"]
    Uncovered -- "否" --> AnyOverride{"存在 OVERRIDDEN 的 MR?"}
    AnyOverride -- "是" --> Overridden["MR_OVERRIDDEN<br/>(经 MR 管理员豁免合入)"]
    AnyOverride -- "否 (均 PASSED/WARN)" --> Undetected["MR_PASSED_UNDETECTED<br/>(MR 门禁未检出: 基线漂移/多 MR 叠加)"]
```

- **判定优先级**：`MR_FAILED_MERGED` > `DIRECT_PUSH_SUSPECTED` > `MR_OVERRIDDEN` > `MR_PASSED_UNDETECTED`（未经许可的风险高于已获豁免的风险）。`PrimaryMRRunID` 取决定最终状态的那条 `MR_GATE` 运行，其余命中写入 `RelatedMRRunIDsJSON`；`AttributionSummary` 记录未覆盖提交短 SHA 与命中依据。
- **`MR_PASSED_UNDETECTED` 不得并入 `PIPELINE_OR_DEP_CHANGE`**：MR 静态预检全部通过但上线仍检出破坏性变更，说明 MR 预检基线（`target_head_sha`）与上线时激活版本存在漂移或多 MR 叠加效应，属于门禁有效性问题，需独立暴露而非免责。
- **前端呈现**：[`BreakingAuditWorkbenchPane.tsx`](file:///home/fugui/codes/code-contract/frontend/src/pages/ProtoSchemaWorkbenchPage/BreakingAuditWorkbenchPane.tsx) 列表与详情抽屉增加「MR 溯源结论」徽标（`AttributedAt` 为空时显示“归因计算中”）、`from_commit_sha..to_commit_sha` 短链、以及一键跳转 `/proto-governance?gov_tab=mr-gates&runId=<primary_mr_run_id>` 的合规穿透镜（`MRGateWorkbenchPane` 已支持 `runId` 参数）。

---

## 5. 决策四：黄金用例向契约库、目录雷达与治理大盘的反向滋养

结合现有 [`MatchGoldenSamples`](file:///home/fugui/codes/code-contract/internal/server/protodoc/sample_json.go#L202-L255) 实现，按以下 4 点补齐双向飞轮闭环：

1. **修复 `/proto-docs → /golden-suites` 服务筛选断链（P0）**：
   - [`ListGoldenSuites`](file:///home/fugui/codes/code-contract/handlers/golden_suite_handler.go#L33-L75) 新增 `service_name` 查询参数，使用 `EXISTS (SELECT 1 FROM contract_golden_suite_items i WHERE i.suite_id = contract_golden_suites.id AND i.service_name = ?)` 精准过滤包含该服务用例的套件（与 `MatchGoldenSamples` 采用同一 `service_name` 等值口径，保证跳转结果与文档样本一致）；既有 `search` 参数语义保持不变。
   - [`ServiceReferenceDocView.tsx`](file:///home/fugui/codes/code-contract/frontend/src/pages/ProtoSchemaWorkbenchPage/ServiceReferenceDocView.tsx#L444-L451) 的「关联黄金用例」按钮改为跳转 `/golden-suites?service_name=${encodeURIComponent(detail.service_name)}`；[`GoldenSuitesWorkbenchPage.tsx`](file:///home/fugui/codes/code-contract/frontend/src/pages/GoldenSuitesWorkbenchPage/GoldenSuitesWorkbenchPage.tsx) 在现有 `search` / `arch_process_id` / `status` 之外读取 `service_name` URL 参数，展示筛选标签与重置操作。
2. **开放契约文档多黄金样本切换（P0）**：
   - [`ServiceReferenceDocView.tsx`](file:///home/fugui/codes/code-contract/frontend/src/pages/ProtoSchemaWorkbenchPage/ServiceReferenceDocView.tsx#L162-L306) 为 `method.golden_samples`（后端已限制最多 5 条）提供样本下拉/胶囊切换器（展示 `suite_code`、`remark` 与 `decode_status`），并增加点击直达对应 `/golden-suites?search=${encodeURIComponent(activeGolden.suite_code)}` 的跳转链接。
3. **向契约库与目录雷达投射「黄金覆盖率 + 最近权威回归态」（P1）**：
   - **契约库详情（[`extractor.go`](file:///home/fugui/codes/code-contract/internal/server/protodoc/extractor.go) / `ProtoServiceDocDetailDTO`）**：增加服务级 `golden_coverage`（`covered_method_count`、`total_method_count`、`active_suite_count`、`latest_regression_status`、`latest_regression_at`）及方法级 `latest_regression_status`。
   - **契约目录与五态雷达（[`catalog.go`](file:///home/fugui/codes/code-contract/internal/server/topology/catalog.go#L896) / `UnifiedServiceCatalogDTO`）**：增加 `golden_suite_count`、`golden_item_count`、`golden_method_coverage`、`latest_regression_status`，支持按“无黄金用例保护的已认领服务”筛选。
   - **零 `bytea` / 零 N+1 性能红线（约束新增覆盖率聚合）**：所有聚合必须通过针对 `contract_golden_suite_items`（`JOIN contract_golden_suites WHERE status='ACTIVE'`）和 `contract_replay_items`（限定关联 `GOLDEN_SUITE`/`CI_GATE` 且已终态的任务）的单次批量 `GROUP BY` 查询完成，**严禁读取 `raw_req_packet` / `raw_rsp_packet` / `raw_actual_packet` 等 `bytea` 列或逐服务循环查询**。（`MatchGoldenSamples` 为解码样本 JSON 需读取 `raw_*_packet` 且已限 5 条，属既有设计，不在此红线约束范围内。）
4. **治理大盘消除“零用例满分”与在途误计盲区（P1，严格守住 `40+30+30` 健康分契约）**：
   - 保持 [`overview.go`](file:///home/fugui/codes/code-contract/internal/server/dashboard/overview.go#L520-L535) 的 `HealthScore`（`40 + 30 + 30`）权重与 `regSubScore = 30 - unhealthyProcs7d*10` 公式不变；
   - **终态口径校准**：在近 30 天权威回归任务查询处统一追加终态过滤（排除 `result_status=''` 的在途任务），使 `PassRate30d`、`BlockedCount30d` 与 `procLatestStatus7d`（同源于 `replayTasks30d`）共享同一终态口径；
   - 在 [`RegressionDefenseDTO`](file:///home/fugui/codes/code-contract/internal/server/dashboard/overview.go#L65-L70) 中新增显性覆盖指标：`has_evaluations_30d`（`bool`）、`active_golden_suite_count`、`golden_covered_services`、`golden_service_coverage_ratio`；
   - **零评估回归子分显性化（不改分值）**：当 `has_evaluations_30d == false` 时，在 `ActionHints` 末尾追加提示“近 30 天无黄金套件/CI 门禁权威回归评估，回归子分为未经验证的满分”，前端在回归子分旁标注「未验证」；
   - 前端 [`RegressionDefenseCard`](file:///home/fugui/codes/code-contract/frontend/src/pages/DashboardWorkbenchPage/DashboardWorkbenchPage.tsx#L347-L410) 在 `has_evaluations_30d == false`（即 `gate_evaluations_30d == 0`）时，将通过率渲染为 **「暂无评估」**（而非具有误导性的 `100.0%`），文案统一为“权威回归（黄金套件 + CI 门禁）”以匹配实际统计口径，并展示「黄金套件服务覆盖率」磁贴与行动提示。

---

## 6. 分期实施路线、TDD 验收与 Non-Goals

### 6.1 分期实施建议（与 `ADR-31` 协同）

> **依赖提示**：由于 `P1/P2` 会将回归状态投射到 `/proto-docs`、`/proto-catalog` 与 `/ci-gates` 控制台，**建议在 `ADR-31` P0 修复（反“假 PASS”）合并后再上线 P1/P2**，避免把失真的 `PASS` 结论扩散至全平台看板。

| 阶段 | 优先级 | 包含决策项 | 涉及主要文件 |
| :--- | :--- | :--- | :--- |
| **Phase 1：动线与断链快修** | **P0** | 决策一（菜单顺序校准 + `/discovery` 空态双引导）<br>决策二 Phase A（`trigger_source` / `arch_process_id` 筛选、`CI_GATE` 只读可见性、URL 参数同步、大盘穿透）<br>决策四 #1/#2（`GET /api/golden-suites?service_name=` 与多样本切换） | `frontend/src/menu.ts`、`MenuAndLegacyRoutes.test.tsx`、`DiscoveryWorkbenchPage`、`internal/server/diff/scheduler.go`、`handlers/replay_handler.go`、`ReplayWorkbenchPage`、`ReplayTaskTable.tsx`、`DashboardWorkbenchPage.tsx`、`handlers/golden_suite_handler.go`、`ServiceReferenceDocView.tsx`、`GoldenSuitesWorkbenchPage.tsx` |
| **Phase 2：溯源与反向滋养** | **P1** | 决策三（`BreakingChangeEvent` SHA 抄录 + 七态异步 MR 溯源 + 存量回填/重算入口 + `BusinessTableNames` 补全）<br>决策四 #3/#4（契约库/目录黄金覆盖率批量聚合 + 终态口径 + 零评估区分与覆盖率磁贴） | `models/breaking_audit.go`、`models/db.go`、`internal/server/decoder/breaking.go`、`internal/server/gitclient/`（共享 Git 缓存祖先判定与 `git log`）、`handlers/breaking_audit_handler.go`、`handlers/routes.go`、`BreakingAuditWorkbenchPane.tsx`、`internal/server/protodoc/`、`internal/server/topology/catalog.go`、`internal/server/dashboard/overview.go` |
| **Phase 3：双闸门完整控制台** | **P2** | 决策二 Phase B（`models.CIGateRun` 持久化含 `RUNNING` 与全部前置拦截态、重启对账、`/ci-gates` 菜单与页面、`/replay?task_id=` 穿透） | `models/ci_gate.go`、`models/db.go`、`handlers/gate_handler.go`、`handlers/routes.go`、`frontend/src/menu.ts`、`ReplayWorkbenchPage`、新增 `CIGatesWorkbenchPage` |

### 6.2 强制验收标准（Acceptance Criteria）
- **AC-01（菜单与路由回归）**：[`MenuAndLegacyRoutes.test.tsx`](file:///home/fugui/codes/code-contract/frontend/src/__tests__/MenuAndLegacyRoutes.test.tsx) 断言两组新顺序全绿，且历史书签路由重定向零回归。
- **AC-02（双闸门流水可观测）**：
  - `GET /api/replay-tasks?trigger_source=CI_GATE` 精确过滤，且**非管理员普通用户**可列出并查看他人（CI Token）触发的 `CI_GATE` 任务明细，但对其调用 `/cancel`、`/rediff` 返回 `403`；
  - Phase B 上线后，模拟 Agent 离线（`503 GATED_ENV_NOT_READY`）、租约等待超时（`503 GATED_ENV_BUSY`）、套件缺失（`404 SUITE_NOT_FOUND`）、非共享环境（`403`，`REJECTED/SCOPE_NOT_SHARED`）、`GATED_VIOLATION`（`422`）与 `PASSED`（`200`），均能在 `contract_ci_gate_runs` 与 `/ci-gates` 查到对应流水（终态评估含 `MarkdownReport`），且各接口 HTTP 状态码与响应体与改造前逐字节一致；模拟评估中途主控重启，遗留 `RUNNING` 流水被对账为 `ERROR`。
- **AC-03（破坏性审计七态溯源防误报）**：
  - 构造 Squash Merge 场景（`MR_GATE` 源分支 SHA 不在主干祖先链，经 `MAIN_MERGE` 或提交信息 MR 引用解析出 IID），有 `OVERRIDDEN` MR 记录时准确判为 `MR_OVERRIDDEN` 而非 `DIRECT_PUSH_SUSPECTED`；
  - 同 SHA 仅改 Stage B 清洗规则，或区间提交未触及本 Bundle proto 路径，判为 `PIPELINE_OR_DEP_CHANGE`；
  - 人工激活历史版本（`to_sha` 为 `from_sha` 祖先）判为 `MANUAL_ROLLBACK`；区间 MR 均 `PASSED` 时判为 `MR_PASSED_UNDETECTED`；
  - 归因计算失败或 Git 缓存缺失时，激活事务照常提交，事件落 `UNATTRIBUTED` 并附原因。
- **AC-04（反向滋养与性能红线）**：`/proto-docs` 点击「关联黄金用例」进入 `/golden-suites?service_name=...` 可准确命中所属套件；多样本可切换；`/proto-docs` 与 `/api/catalog/services` 的覆盖率聚合单测断言 SQL 查询次数为常数 `O(1)` 且零 `bytea` 列投影；`evalCount == 0` 时大盘展示「暂无评估」与“未验证满分”行动提示，在途任务不计入阻断，且 `HealthScore` 权重与公式相关的既有单元测试断言不被破坏。

### 6.3 Non-Goals（明确不做）
1. 不修改 `cmd/agent` 数据面采集/重放二进制协议及 `pkg/transport`、`pkg/replay` 核心包（保持 `cmd/agent/deps_test.go` 物理防腐红线）。
2. 不回退“取消入库与激活阻断”原则——`BreakingChangeEvent` 保持非阻断审计与合规追责定位，MR 溯源仅做异步客观归因展示，不阻塞流水线发布与激活事务。
3. 不改动治理大盘 `HealthScore` `40 + 30 + 30` 既有评分权重、子分公式与兼容字段，避免破坏外部基线契约；零评估场景仅通过新增字段与 `ActionHints` 显性化。
