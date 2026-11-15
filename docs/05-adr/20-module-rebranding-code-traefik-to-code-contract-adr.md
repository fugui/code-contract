# ADR-20：将模块正式更名为 Code-Contract（code-contract）的战略演进与全仓无损重构方案

- **文档编号**：`20-module-rebranding-code-traefik-to-code-contract-adr.md`
- **文档版本**：v1.0.0
- **日期**：2026-11-15
- **状态**：📋 提议中 (Proposed — 仅完成方案定稿与影响面盘点，**暂不实施**)
- **关联特性**：全系统全局标识（涉及 `F-01`~`F-32`、微前端契约、数据库表前缀、CI/CD 门禁与跨仓集成）
- **演进与替代关系**：
  - 部分替代并演进 [`ADR-01`](./01-microfrontend-and-table-prefix-isolation.md) 中关于 `ModuleKey = "traefik"`、`traefik_` 表前缀与 `base: '/traefik/'` 的初始命名约定；
  - 演进 [`ADR-14`](./14-governance-dashboard-golden-suite-and-ci-gate-adr.md) 中 `X-Traefik-CI-Token` 鉴权头约定与 [`ADR-18`](./18-proto-full-lifecycle-and-git-mr-governance-adr.md) 中 `TRAEFIK_CRYPTO_KEY` 及 MR 状态上下文 `code-traefik/proto-mr-gate` 约定；
  - 联动调整宿主门户 `code-bench`、公共基础设施 `code-common` 与本地/GitHub 仓库命名。

---

## 1. 背景与问题陈述 (Context & Problem Statement)

### 1.1 历史命名与终局产品定位倒置

项目自 `M1` 启动至 `M27` 收官，产品内涵经历了从“流量工具”到“全生命周期活体契约平台”的根本性跃迁：

1. **立项切入点时期（M1 ~ M13）**：早期以「eBPF 内核态流量录制与零下游重放（*Traffic* Capture & Replay）」为切入点，故取 `traffic` 的音近变体命名工程为 `code-traefik`。
2. **战略升维与版图吞并时期（M14 ~ M27）**：
   - 经 `ADR-14` 确立「接口资产治理、架构漂移雷达、无依赖自愈防劣化」三大支柱，**流量录制与重放已被明确界定为底层零侵入数据面手段**；
   - 经 `ADR-15`、`ADR-16`、`ADR-17` 构建大规模 C4 单层下钻架构数字孪生与多实现服务契约模型；
   - 经 `ADR-18`、`ADR-19` 正式吸收原未落地的 `code-proto` 与 `protohub` 职责，打通「Git-Only `.proto` 源码 ➔ 四合一 MR 静态门禁 ➔ 纯 Go `protocompile` 编译入池 ➔ 全注释活体接口参考文档中心 ➔ 运行态流量防漂移与回归防劣化」全链路闭环。

继续沿用 `code-traefik` 已形成**“以底层采样手段代指上层治理平台”**的严重名实不符。

### 1.2 强烈的行业开源组件撞名歧义

在云原生与后端基础设施领域，**Traefik** 是广为人知的反向代理与 API 网关（Ingress Controller）。首次接触本系统的研发、测试或运维人员容易将其误解为“网关路由配置平台”（尤其同工作区已存在 `code-gate` 等兄弟工程），带来不必要的沟通成本与心智误导。

### 1.3 为何选定 `code-contract`

在候选命名（`code-contract` / `code-pact` / `code-twin` / `code-atlas`）的权衡评估中，**`code-contract`（码约 · 架构与活体契约治理平台）** 具备最强的业务公约数与零门槛理解力：

- **设计态契约**：`.proto` 源码管理、AST/语义规范体检、MR 四合一门禁、活体参考文档与 `.pb` 描述集资产；
- **架构态契约**：三级架构数字孪生、模块间 `PROVIDE / DEPEND` 与 `MULTI_IMPL` 接口库契约；
- **运行态契约**：eBPF 实测流量与设计态拓扑的一致性格栅校验（`Violation / Uncovered`）、黄金套件与 `ScopedMock` 因果闭包防劣化回归。

---

## 2. 目标身份契约与全维度映射方案 (Target Identity Mapping)

为保持与工作区上位宪法 [`code-common/rules/AGENTS.md`](../../../code-common/rules/AGENTS.md) 中 `ModuleKey`、`TablePrefix`、`AdminRole` 三位一体契约一致，确立如下标准映射：

| 契约维度 | 现行标识 (`code-traefik`) | 目标标识 (`code-contract`) | 变更说明与约束 |
| :--- | :--- | :--- | :--- |
| **工程目录与 Git 仓库** | `code-traefik` / `fugui/code-traefik.git` | `code-contract` / `fugui/code-contract.git` | 本地目录、GitHub 远端仓库名同步更新 |
| **Go Module 路径** | `module code-traefik` | `module code-contract` | 全仓 Go `import` 路径与 `Makefile` `LDFLAGS` 同步更新 |
| **产品展示名** | `Code-Traefik` / `架构与活体契约治理平台` | `Code-Contract` / `码约 · 架构与活体契约治理平台` | `config.ServiceName`、前端标题及文档品牌统一 |
| **模块标识 (`ModuleKey`)** | `traefik` | `contract` | 门户路由 `/contract`、Vite `base: '/contract/'`、Federation `name: 'contract'` |
| **双二进制产物名** | `code-traefik-server` / `code-traefik-agent` | `code-contract-server` / `code-contract-agent` | `Makefile`、`cmd/server`、`cmd/agent`、安装指引命令及 `code-common/scripts/start.sh` |
| **管理员角色 (`AdminRole`)** | `traefik_admin` | `contract_admin` | `config.AdminRole`、`frontend/src/constants.ts` 及 `code-bench` 角色徽章与鉴权逻辑 |
| **端口分配** | 后端 `:8091` / 前端 `5181` | **保持 `:8091` / `5181` 不变** | 避免与兄弟模块端口冲突，保持现有本地联调习惯与防火墙规则零改动 |
| **数据库表前缀 (`TablePrefix`)** | `traefik_`（31 张物理表 + 分区子表） | **`contract_`** | 详见 §2.1 权衡决策 |
| **AES-256-GCM 主密钥环境变量** | `TRAEFIK_CRYPTO_KEY` | `CONTRACT_CRYPTO_KEY` | 过渡期支持回退读取 `TRAEFIK_CRYPTO_KEY`（见 §4.2） |
| **Agent 环境变量** | `TRAEFIK_AGENT_ALLOW_IPS`<br>`TRAEFIK_AGENT_BIN_PATH` | `CONTRACT_AGENT_ALLOW_IPS`<br>`CONTRACT_AGENT_BIN_PATH` | 过渡期支持回退读取旧环境变量（见 §4.2） |
| **CI/CD 门禁请求头** | `X-Traefik-CI-Token` | `X-Contract-CI-Token` | 过渡期双头兼容并输出弃用提示；Query 禁用拦截同步覆盖两者 |
| **MR 状态外部上下文** | `code-traefik/proto-mr-gate` | `code-contract/proto-mr-gate` | `internal/server/gitclient/mr_runner.go` 推送至 GitLab/GitHub 的 Commit Status Context |
| **前端包名与 DOM/存储键** | `code-traefik-frontend`<br>`.code-traefik-*`<br>`traefik_topo_draft_v2_`<br>`traefikMenuConfig` | `code-contract-frontend`<br>`.code-contract-*`<br>`contract_topo_draft_v2_`<br>`contractMenuConfig` | 包名、CSS 类名前缀、`localStorage` 拓扑草稿前缀与菜单配置符号 |

### 2.1 关键权衡：`TablePrefix` 坚持使用 `contract_` 而非缩写 `ct_`

针对 `traefik_service_contracts` 更名为 `contract_service_contracts` 后出现的轻微词根重复（`contract_..._contracts`），团队评估了“统一使用 `contract_`”与“例外缩写为 `ct_`”两个方案：

- **最终决策**：**统一使用 `contract_` 作为唯一 `TablePrefix`**，不引入 `ct_` 等缩写特例。
- **理由**：
  1. 严格遵循工作区上位规范中「`TablePrefix = "<module_key>_"`」的单一真实源（SSOT）约定，避免后续开发者或 AI 代理误判模块标识与表前缀映射关系；
  2. 全库 31 张表语义各异（`contract_subsystems`、`contract_modules`、`contract_proto_schemas`、`contract_traffic_records`、`contract_agents` 等），除 `contract_service_contracts` 外其余 30 张表均清晰自然，不应为单表微瑕破坏全局命名一致性。

---

## 3. 全仓与跨仓影响面精确盘点 (Impact Inventory)

基于当前工作区代码基线实测，受影响范围精准收口为以下 **3 个仓库 / 6 个层次**：

### 3.1 本仓（`code-traefik` ➔ `code-contract`）

1. **构建、模块与运行配置层**：
   - `go.mod`、`Makefile`（`BINARY`、`AGENT_BINARY`、`LDFLAGS`）、`config/config.go`（`ModuleKey`、`ServiceName`、`AdminRole`、`TablePrefix`）、`config.yaml.example` / `config.yaml`（`oauth2.client_id` 等）；
   - `cmd/agent/main.go`（命令行提示、`--claim-code-file` 默认路径注释、`TRAEFIK_AGENT_ALLOW_IPS`）、`cmd/agent/deps_test.go`、`cmd/seed/governance_seed.go`（Demo 邮箱后缀 `@code-traefik.local` ➔ `@code-contract.local`）。
2. **数据库模型与迁移底座（`models/`）**：
   - `models/setting.go`（`TraefikSetting` ➔ `ContractSetting`，对应 `contract_settings`）；
   - 全部 31 张业务与配置表 `TableName()` 及 `models/git_repo.go`、`models/linter.go` 中硬编码的表名字符串（建议同步收口为 `config.TablePrefix + "..."` 消除硬编码漂移）；
   - `models/db.go` 与 `models/partition_manager.go`：表、序列（`contract_traffic_records_id_seq`）、分区命名正则（`^contract_traffic_records_y(\d{4})m(\d{2})d(\d{2})$`）以及全部 `chk_traefik_*`、`uk_traefik_*`、`idx_traefik_*`、`fk_traefik_*` 约束/索引名；
   - `models/*_test.go` 中表名前缀与环境变量断言。
3. **服务端业务逻辑与 HTTP 接口层（`handlers/` & `internal/`）**：
   - `internal/server/security/crypto.go`（`EnvTraefikCryptoKey` ➔ `EnvContractCryptoKey`）；
   - `internal/server/security/ssrf.go`（注释引用）；
   - `internal/server/gitclient/sync_service.go`（Postgres 咨询锁键 `traefik_git_sync_%d` ➔ `contract_git_sync_%d`）、`virtual_tree.go`（原生 SQL 表名）、`mr_runner.go`（Commit Status `code-traefik/proto-mr-gate`）；
   - `handlers/ci_auth.go`（`X-Traefik-CI-Token` ➔ `X-Contract-CI-Token`）、`handlers/settings.go`（错误文案提示 `TRAEFIK_CRYPTO_KEY`）、`handlers/agent_handler.go`（`TRAEFIK_AGENT_BIN_PATH`）；
   - `e2e_scenarios_test.go` 及各 `*_test.go`。
4. **微前端工程层（`frontend/`）**：
   - `frontend/package.json`（`name`）、`frontend/vite.config.ts`（`versionTrackerPlugin.appName`、Federation `name`、`base`、代理 `/contract/api`）；
   - `frontend/src/constants.ts`（`MODULE_KEY`、`MODULE_TITLE`、`ADMIN_ROLE`）、`frontend/src/menu.ts`（`TraefikMenuConfig` / `traefikMenuConfig`）、`frontend/src/App.tsx`；
   - `frontend/src/components/ArchCanvas/layout/layoutPersistence.ts`（`DRAFT_KEY_PREFIX = 'contract_topo_draft_v2_'`）；
   - `frontend/src/components/NotificationBell/NotificationBell.css` & `WorkspaceUtilityHeader.tsx`（`.code-contract-embedded-shell`、`.code-contract-utility-header*`）；
   - `frontend/src/pages/AgentManagementPage/AgentInstallGuidePane.tsx`（`code-contract-agent` 下载文件名与命令行指引）、`frontend/src/pages/AdminSettingsPage.tsx`（环境变量与邮箱占位提示）；
   - `frontend/src/__tests__/` 中对应断言。
5. **端到端脚本与文档层（`e2e_browser_test/` & `docs/` & `AGENTS.md` & `README.md`）**：
   - `e2e_browser_test/run_portal_e2e.js`（入口 URL `/contract`）；
   - `AGENTS.md`、`README.md` 及活跃基线文档（`docs/README.md`、`docs/01-features/01-feature-matrix.md`、`docs/02-design/*`、`docs/03-api/01-http-api-contract.md`、`docs/04-tasks/01-roadmap.md`、`docs/06-ops/01-verification-and-deployment-guide.md`）。

### 3.2 宿主门户（`code-bench`）

- `code-bench/config.yaml`（及示例配置）：`gateways.traefik` ➔ `gateways.contract`，`docs.path` 更新为 `/home/fugui/codes/code-contract/docs`；
- `code-bench/frontend/src/pages/UserManagement.tsx`：`traefik_admin` 角色徽章样式映射与展示标签注释更新为 `contract_admin`；
- `code-bench/models/config_test.go`、`code-bench/handlers/user_test.go`：单测样例中的 `"traefik"` / `"traefik_admin"` 同步更新为 `"contract"` / `"contract_admin"`。

### 3.3 公共底座与上位规范（`code-common`）

- `code-common/scripts/start.sh`：默认模块列表 `DEFAULT_MODULES`、Tmux 分屏标题与启动命令 `code-traefik-server` ➔ `code-contract-server`；
- `code-common/rules/AGENTS.md`（即工作区根目录软链 `AGENTS.md`）：工程名列表与示例 `traefik_` ➔ `contract_`。

---

## 4. 平滑迁移与双轨兼容策略 (Migration & Compatibility Strategy)

### 4.1 共享 PostgreSQL 数据库存量表无损原地重命名（零数据丢失）

针对已执行过建表的联调或准生产数据库，禁止采用“直接清库重来”的粗暴方式，实施时必须在 `models/db.go` `SetupWithDB` 的 `AutoMigrate` 之前增加幂等迁移钩子 **`MigrateLegacyTraefikPrefix(db *gorm.DB)`**：

1. **触发判定**：检测 `to_regclass('traefik_subsystems') IS NOT NULL AND to_regclass('contract_subsystems') IS NULL` 时自动在单事务内执行原地重命名；新库或已完成迁移的库直接 `O(1)` 跳过。
2. **表与分区级联重命名**：
   - 批量执行 `ALTER TABLE IF EXISTS traefik_<name> RENAME TO contract_<name>`（涵盖全部 31 张核心表及配置表）；
   - 对分区母表 `traefik_traffic_records` 重命名后，PostgreSQL 自动维持既有的全部按日分区挂载关系；同步遍历 `pg_inherits` 下属 `traefik_traffic_records_y*` 子表原地 `RENAME` 为 `contract_traffic_records_y*`，并执行 `ALTER SEQUENCE IF EXISTS traefik_traffic_records_id_seq RENAME TO contract_traffic_records_id_seq` 与默认值更新。
3. **索引与约束对齐**：将既有的 `uk_traefik_*`、`idx_traefik_*`、`chk_traefik_*`、`fk_traefik_*` 重命名为 `uk_contract_*` 等，保证后续 `SetupWithDB` 内幂等 `DO $$ ... END $$` 块不会产生重复索引或约束冲突。
4. **角色标识迁移**：由 `code-bench` 或共享库幂等脚本将存量用户权限表中的 `traefik_admin` 更新为 `contract_admin`。

### 4.2 外部集成（环境变量、CI Header、前端草稿）过渡期双兼容

为避免因改名导致正在联调的 CI 流水线或本地环境瞬间中断，在代码层保留 **1~2 个迭代窗口的向后兼容读取**：

1. **AES-256-GCM 主密钥**：`security/crypto.go` 优先读取 `CONTRACT_CRYPTO_KEY`；若为空则回退读取 `TRAEFIK_CRYPTO_KEY` 并打印一次性 `WARN` 弃用日志。
2. **Agent 环境变量**：`CONTRACT_AGENT_ALLOW_IPS`、`CONTRACT_AGENT_BIN_PATH` 优先，未配置时回退读取 `TRAEFIK_*` 旧变量并打印提示。
3. **CI 门禁请求头**：`CIOrAdminGuard` 优先读取 `X-Contract-CI-Token`，若为空则兼容读取 `X-Traefik-CI-Token` 并附加 `Deprecation: true` 响应头告警；URL Query 防泄拦截同步拒绝 `x-contract-ci-token` 与 `x-traefik-ci-token`。
4. **前端拓扑本地草稿**：`layoutPersistence.ts` 读取 `contract_topo_draft_v2_*` 未命中时，可选尝试迁移读取 `traefik_topo_draft_v2_*` 并落盘至新键名。
5. **门户旧路由兜底（可选）**：可在 `code-bench` 网关层为 `/traefik/*` 配置 301 重定向至 `/contract/*`，待通知全员切换书签后移除。

### 4.3 历史 SDD/ADR 文档冻结原则

- 已交付归档的 **`ADR-01` ~ `ADR-19`** 与 **`04-tasks/02` ~ `28`** 历史作战卡属于工程演进历史快照，**实施改名时不强行改写历史 ADR 正文中的历史语境**（避免扭曲历史事实），仅需在后续新文档及**活跃基线文档**（根目录 `README.md`、`AGENTS.md`、`docs/README.md`、`01-features/01-feature-matrix.md`、`02-design/*`、`03-api/01-http-api-contract.md`、`04-tasks/01-roadmap.md`、`06-ops/*`）中全面切换为 `code-contract` 契约。

---

## 5. 分阶段实施路线图 (Execution Roadmap — 待批复后启动)

本 ADR 当前仅作架构决策记录与范围冻结，**暂不实施**。后续确认启动实施时，建议按以下顺序一次性闭环交付：

1. **阶段一：本仓内部全量标识重构**
   - 修改 `go.mod`、全仓 Go 包导入路径、`config/config.go`、`Makefile`、`models/`（含统一消解 `git_repo.go`/`linter.go` 硬编码表前缀 + 新增 `MigrateLegacyTraefikPrefix`）、`handlers/`、`internal/`、`cmd/`、`demo/`、`frontend/`、`e2e_browser_test/` 及活跃基线文档。
2. **阶段二：本地全量质量门禁验证**
   - 在隔离库环境下执行 `make test`（含 `MigrateLegacyTraefikPrefix` 模拟旧库无损升级专项单测）、`make lint`、`make build && make clean`，确保零警告、零遗漏、零端口与进程残留。
3. **阶段三：跨仓联动与仓库重命名**
   - 同步更新 `code-bench`（网关、角色徽章与单测）和 `code-common`（`start.sh` 与上位 `AGENTS.md`）并验证构建测试；
   - 在 GitHub 将远端仓库 `code-traefik` 重命名为 `code-contract`，本地执行目录重命名 `mv code-traefik code-contract` 与 `git remote set-url origin`，推送全部相关仓库并更新各仓关联路径。

---

## 6. 风险评估与回滚策略

| 风险项 | 影响面 | 缓解与回滚措施 |
| :--- | :--- | :--- |
| **物理机器已驻留旧版 `code-traefik-agent`** | 控制面或管理员下载路径变化 | 保留 Agent HTTP/WS 协议结构完全不变，仅变更二进制文件名与启动引导文案；老版本 Agent 在线连接仍可正常通信，待平滑滚动替换 |
| **外部 GitLab Webhook 回调地址失效** | 已配置在 GitLab 上的 Webhook URL 含 `/traefik/api/...` | 切换期在 `code-bench` 网关保留 `/traefik/api/*` 反向代理/重写至 `/contract/api/*` 过渡窗口，通知仓库管理员更新 Webhook URL 后摘除 |
| **跨仓不同步导致宿主门户白屏** | `code-bench` 仍请求 `/traefik/assets/remoteEntry.js` | `code-contract` 与 `code-bench`、`code-common` 必须在同一次交付窗口内联动改名、联调验证并推送 |
| **未批复前误实施** | 打断当前稳定基线 | 本 ADR 状态严格锁定为 `Proposed`；在用户明确下达实施指令前，**严禁修改任何运行代码、数据库前缀、配置或仓库目录** |
