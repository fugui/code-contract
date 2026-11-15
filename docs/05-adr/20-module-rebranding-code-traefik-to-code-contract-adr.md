# ADR-20：将模块正式更名为 Code-Contract（code-contract）的战略演进与全仓干净重构方案

- **文档编号**：`20-module-rebranding-code-traefik-to-code-contract-adr.md`
- **文档版本**：v1.1.0（依据「当前系统尚无任何现网部署、所有存量数据均可直接清库重建」及「严格保障 `cmd/seed` 与 `demo` 演示数据端到端准确性」修订：移除冗余的存量表重命名迁移钩子与多套环境变量/请求头过渡期双兼容代码，确立**零遗留兼容债的干净切换策略**，并补全 `demo/proto/*.proto`、`demo/demo.pb` 重编译、`cmd/seed` 硬编码表清单及多维 Demo 数据准确性校验门禁）
- **日期**：2026-11-15
- **状态**：📋 提议中 (Proposed — 仅完成方案定稿与影响面盘点，**暂不实施**)
- **关联特性**：全系统全局标识（涉及 `F-01`~`F-32`、微前端契约、数据库表前缀、CI/CD 门禁、Demo 集群与 Seed 演示基线）
- **演进与替代关系**：
  - 替代并演进 [`ADR-01`](./01-microfrontend-and-table-prefix-isolation.md) 中关于 `ModuleKey = "traefik"`、`traefik_` 表前缀与 `base: '/traefik/'` 的初始命名约定；
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

### 1.3 零现网包袱窗口期与 `code-contract` 选型

当前系统**尚未有任何现网生产部署，所有历史开发/测试数据库数据均可直接删除并由 `cmd/seed` 重建**。趁投产前完成更名，可做到**零历史兼容包袱、零灰度双轨成本、一次性干净切换**。

在候选命名（`code-contract` / `code-pact` / `code-twin` / `code-atlas`）评估中，**`code-contract`（码约 · 架构与活体契约治理平台）** 具备最强业务公约数：
- **设计态契约**：`.proto` 源码管理、AST/语义规范体检、MR 四合一门禁、活体参考文档与 `.pb` 描述集资产；
- **架构态契约**：三级架构数字孪生、模块间 `PROVIDE / DEPEND` 与 `MULTI_IMPL` 接口库契约；
- **运行态契约**：eBPF 实测流量与设计态拓扑的一致性格栅校验（`Violation / Uncovered`）、黄金套件与 `ScopedMock` 因果闭包防劣化回归。

---

## 2. 目标身份契约与全维度映射方案 (Target Identity Mapping)

为保持与工作区上位宪法 [`code-common/rules/AGENTS.md`](../../../code-common/rules/AGENTS.md) 中 `ModuleKey`、`TablePrefix`、`AdminRole` 三位一体契约一致，确立如下标准映射：

| 契约维度 | 现行标识 (`code-traefik`) | 目标标识 (`code-contract`) | 变更说明与约束 |
| :--- | :--- | :--- | :--- |
| **工程目录与 Git 仓库** | `code-traefik` / `fugui/code-traefik.git` | `code-contract` / `fugui/code-contract.git` | 本地目录、GitHub 远端仓库名同步更新 |
| **Go Module 路径** | `module code-traefik` | `module code-contract` | 全仓 Go `import` 路径、`Makefile` `LDFLAGS` 及 `demo/proto` `go_package` 同步更新 |
| **产品展示名** | `Code-Traefik` / `架构与活体契约治理平台` | `Code-Contract` / `码约 · 架构与活体契约治理平台` | `config.ServiceName`、前端标题及文档品牌统一 |
| **模块标识 (`ModuleKey`)** | `traefik` | `contract` | 门户路由 `/contract`、Vite `base: '/contract/'`、Federation `name: 'contract'` |
| **双二进制产物名** | `code-traefik-server` / `code-traefik-agent` | `code-contract-server` / `code-contract-agent` | `Makefile`、`cmd/server`、`cmd/agent`、`demo/` 启动器及 `code-common/scripts/start.sh` |
| **管理员角色 (`AdminRole`)** | `traefik_admin` | `contract_admin` | `config.AdminRole`、`frontend/src/constants.ts` 及 `code-bench` 角色徽章与鉴权逻辑 |
| **端口分配** | 后端 `:8091` / 前端 `5181` | **保持 `:8091` / `5181` 不变** | 避免与兄弟模块端口冲突，保持现有本地联调习惯与 Agent 默认端口 `:9091` 不变 |
| **数据库表前缀 (`TablePrefix`)** | `traefik_`（31 张物理表 + 分区子表） | **`contract_`** | 详见 §2.1 权衡决策 |
| **AES-256-GCM 主密钥环境变量** | `TRAEFIK_CRYPTO_KEY` | `CONTRACT_CRYPTO_KEY` | 直接切换，不保留旧环境变量兜底 |
| **Agent 环境变量** | `TRAEFIK_AGENT_ALLOW_IPS`<br>`TRAEFIK_AGENT_BIN_PATH` | `CONTRACT_AGENT_ALLOW_IPS`<br>`CONTRACT_AGENT_BIN_PATH` | 直接切换，不保留旧环境变量兜底 |
| **CI/CD 门禁请求头** | `X-Traefik-CI-Token` | `X-Contract-CI-Token` | 直接切换，Query 防泄拦截更新为 `x-contract-ci-token` |
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

基于当前工作区代码基线实测，受影响范围精准收口为以下 **3 个仓库 / 7 个层次**：

### 3.1 本仓（`code-traefik` ➔ `code-contract`）

1. **构建、模块与运行配置层**：
   - `go.mod`、`Makefile`（`BINARY`、`AGENT_BINARY`、`LDFLAGS`）、`config/config.go`（`ModuleKey`、`ServiceName`、`AdminRole`、`TablePrefix`）、`config.yaml.example` / `config.yaml`（`oauth2.client_id` 等）；
   - `cmd/agent/main.go`（命令行提示、`--claim-code-file` 默认路径示例、`TRAEFIK_AGENT_ALLOW_IPS`）、`cmd/agent/deps_test.go`（源码包导入路径禁止清单）。
2. **数据库模型与重建底座（`models/`）**：
   - `models/setting.go`（`TraefikSetting` ➔ `ContractSetting`，移除不再需要的 `TemplateSetting` 别名或按需同步）；
   - 全部 31 张业务与配置表 `TableName()` 及 `models/git_repo.go`、`models/linter.go` 中硬编码的 `"traefik_*"` 字符串（实施时建议统一收口为 `config.TablePrefix + "..."` 彻底消除表前缀硬编码）；
   - `models/db.go` 与 `models/partition_manager.go`：表、序列（`contract_traffic_records_id_seq`）、分区命名正则（`^contract_traffic_records_y(\d{4})m(\d{2})d(\d{2})$`）以及全部 `chk_contract_*`、`uk_contract_*`、`idx_contract_*`、`fk_contract_*` 约束/索引名；
   - `models/*_test.go` 中表名前缀与环境变量断言。
3. **服务端业务逻辑与 HTTP 接口层（`handlers/` & `internal/`）**：
   - `internal/server/security/crypto.go`（`EnvTraefikCryptoKey` ➔ `EnvContractCryptoKey`）；
   - `internal/server/security/ssrf.go`（注释引用 `contract_settings.outbound_trusted_cidrs`）；
   - `internal/server/gitclient/sync_service.go`（Postgres 咨询锁键 `contract_git_sync_%d`）、`virtual_tree.go`（原生 SQL 表名）、`mr_runner.go`（Commit Status `code-contract/proto-mr-gate`）；
   - `handlers/ci_auth.go`（`X-Contract-CI-Token`）、`handlers/settings.go`（错误文案提示 `CONTRACT_CRYPTO_KEY`）、`handlers/agent_handler.go`（`CONTRACT_AGENT_BIN_PATH`）；
   - `e2e_scenarios_test.go` 及各 `*_test.go`。
4. **Demo 微服务集群与 Proto 编译产物层（`demo/` — 关键准确性依赖！）**：
   - `demo/proto/{common,account,inventory,payment,order}.proto`：全部 5 个文件内的 `option go_package = "code-traefik/demo/pkg/proto/...";` 必须同步改为 `code-contract/demo/pkg/proto/...`；
   - `demo/Makefile`：`protoc --go_out=. --go_opt=module=code-traefik` ➔ `module=code-contract`，以及 `code-traefik-agent` 编译拷贝产物名 ➔ `code-contract-agent`；
   - **必须重新执行 `make -C demo proto` 与 `make -C demo build`**：确保二进制描述符文件 **`demo/demo.pb`**（内含 `SourceCodeInfo` 注释与 `go_package` 元数据）以及生成的 **`demo/pkg/proto/*pb/*.pb.go`** 与最新 `.proto` 源码 **100% 字节同步**，严禁仅靠文本替换 `.pb.go` 而漏掉 `demo/demo.pb` 重编译；
   - `demo/cmd/**` & `demo/pkg/crosframe/**`：Go import 路径更新为 `code-contract/...`；
   - `demo/start.sh`、`demo/start-agent.sh`、`demo/docs/01-cros-rpc-demo-app-proposal.md`：`code-traefik-agent` 产物路径、Tmux 面板标题及提示信息更新为 `code-contract-agent`。
5. **Seed 演示数据与大规模拓扑种子层（`cmd/seed/` & `scripts/seed-large-topology/` — 核心准确性红线！）**：
   - `cmd/seed/main.go`：
     - 日志文案 `开始清除 Traefik 数据...` ➔ `开始清除 Code-Contract 数据...`；
     - 第 68~99 行 `TRUNCATE TABLE ... RESTART IDENTITY CASCADE;` 中硬编码的 **30 张 `traefik_*` 业务表清单**必须全部同步改为 `contract_*`（或通过 `config.TablePrefix` 拼接生成，防止遗漏任一子表导致外键/旧数据残留）；
     - 保持 1 个 `demo-local-agent`（`:9091`）、2 个子系统（`ECOMMERCE` L2 / `CLIENT` L4）、4 个功能组、7 个功能模块（含 `LIB_PAYMENT_CONTRACT` L1 接口库与 `ORDER_CORE` L3）、5 个服务契约（含 `PaymentService` 多实现 `ALIPAY`/`WECHAT`）、5 项依赖及 6 个 `contract_processes` 挂载实例拓扑完整无损。
   - `cmd/seed/governance_seed.go`：
     - 第 273 行 `security.EnvTraefikCryptoKey` ➔ `security.EnvContractCryptoKey`；
     - 第 995、1040 行内嵌历史快照常量 `legacyPaymentProtoV1` / `legacyOrderProtoV1` 中的 `option go_package` 必须同步更新为 `"code-contract/demo/pkg/proto/..."`，确保 v1/v2 历史版本对比演示不出现无意义的 `go_package` 路径漂移噪音；
     - 演示用户/提交人邮箱 `@code-traefik.local` ➔ `@code-contract.local`；
     - 校验 `seedGitBundlesAndCompileSchemas`（4 个 `GitProtoBundle` ➔ 4 个 `ProtoSchema` / 14 个 `ProtoOperation`）、`seedDemoGoldenSuite`（3 条序列化 `orderpb`/`commonpb` CROS RPC 黄金样本）、`protodoc.RebuildForSchema`（全注释与 `@owner/@error_code/@deprecated` 提取）、`linter.RunAndPersistScan`（AST 重复结构体探测 `AccountSettlementSnapshot` vs `InventorySettlementSnapshot`）及 `seedMRCheckRunsAndNotifications`（3 条 MR 战报 + 3 条指向 `/proto-governance` 的站内通知）全链路数据闭环准确。
   - `scripts/seed-large-topology/main.go`：
     - Go import 更新为 `code-contract/...`；
     - 第 82~107 行 `TRUNCATE TABLE` 清单中的 24 张 `traefik_*` 表名全部更新为 `contract_*`。
6. **微前端工程层（`frontend/`）**：
   - `frontend/package.json`（`name`）、`frontend/vite.config.ts`（`versionTrackerPlugin.appName`、Federation `name`、`base`、代理 `/contract/api`）；
   - `frontend/src/constants.ts`（`MODULE_KEY`、`MODULE_TITLE`、`ADMIN_ROLE`）、`frontend/src/menu.ts`（`TraefikMenuConfig` / `traefikMenuConfig`）、`frontend/src/App.tsx`；
   - `frontend/src/components/ArchCanvas/layout/layoutPersistence.ts`（`DRAFT_KEY_PREFIX = 'contract_topo_draft_v2_'`）；
   - `frontend/src/components/NotificationBell/NotificationBell.css` & `WorkspaceUtilityHeader.tsx`（`.code-contract-embedded-shell`、`.code-contract-utility-header*`）；
   - `frontend/src/pages/AgentManagementPage/AgentInstallGuidePane.tsx`（`code-contract-agent` 下载文件名与命令行指引）、`frontend/src/pages/AdminSettingsPage.tsx`（环境变量 `CONTRACT_CRYPTO_KEY` / `CONTRACT_CI_TOKEN` 与邮箱占位提示）；
   - `frontend/src/__tests__/` 中对应断言。
7. **端到端脚本与文档层（`e2e_browser_test/` & `docs/` & `AGENTS.md` & `README.md`）**：
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

## 4. 干净重建与零兼容债切换策略 (Clean Rebuild & Zero-Legacy-Debt Strategy)

鉴于**当前系统无任何现网部署，所有开发/测试库数据均允许删除重建**，确立以下极简、无技术债的切换策略（延续 `ADR-18 v1.3.0` 拒绝无用兼容代码的洁净工程原则）：

### 4.1 数据库采用「彻底清库 + 原生重建 + `make seed` 重塑」

1. **不做存量表在线 `RENAME` 迁移**：不引入复杂的 `MigrateLegacyTraefikPrefix`（逐表/分区/序列/约束重命名脚本）以免在代码库中留下死代码与维护负担。
2. **一次性清理本地旧 `traefik_*` 遗留表**：在本地开发库（如 `code_shield`）切换时，通过前置清理步骤（或在 `models/db.go` `SetupWithDB` step 0 一次性 `DROP TABLE IF EXISTS` 遗留的旧 `traefik_*` 表及 `traefik_traffic_records_id_seq` 序列，保留共享公共表 `users` / `departments` / `audit_logs`）彻底清除旧表，随后由 `models.SetupWithDB` 从零幂等创建全部 **31 张 `contract_*` 物理表**与分区母表。
3. **共享库角色直接重建/更新**：若本地共享库 `users` 中已有 `traefik_admin` 测试账号权限，直接更新或重新初始化为 `contract_admin`。
4. **Seed 数据一键重建与精准校验**：建表完成后立即执行 `make seed`（即 `go run ./cmd/seed`），重建全部演示拓扑、Git 仓/Bundle、`.proto` 历史快照、编译激活 `.pb`、文档符号、黄金用例、静态检查报告、MR 战报与通知数据。

### 4.2 外部标识与代码符号直接干净切换（零过渡兼容垫片）

- **不保留双环境变量、双请求头、旧路由重定向或旧 `localStorage` 兜底分支**：
  - `CONTRACT_CRYPTO_KEY`、`CONTRACT_AGENT_ALLOW_IPS`、`CONTRACT_AGENT_BIN_PATH` 直接替换 `TRAEFIK_*`；
  - `X-Contract-CI-Token`（含 Query 防泄 `x-contract-ci-token`）直接替换 `X-Traefik-CI-Token`；
  - 前端 `contract_topo_draft_v2_`、`.code-contract-*`、`contractMenuConfig` 及 `/contract/*` 路由直接切换，不保留 `/traefik/*` 兼容垫片。

### 4.3 历史 SDD/ADR 文档冻结原则

- 已交付归档的 **`ADR-01` ~ `ADR-19`** 与 **`04-tasks/02` ~ `28`** 历史作战卡属于工程演进历史快照，**实施改名时不回改历史 ADR/作战卡正文中的历史语境**，仅在后续新文档及**活跃基线文档**（根目录 `README.md`、`AGENTS.md`、`docs/README.md`、`01-features/01-feature-matrix.md`、`02-design/*`、`03-api/01-http-api-contract.md`、`04-tasks/01-roadmap.md`、`06-ops/*`）中全面切换为 `code-contract` 契约。

---

## 5. 分阶段实施路线图与 Seed 准确性验收门禁 (Execution Roadmap — 待批复后启动)

本 ADR 当前仅作架构决策记录与范围冻结，**暂不实施**。后续确认启动实施时，按以下三阶段一次性闭环交付：

1. **阶段一：本仓内部重构与 Demo/Proto 资产重编译**
   - 重构 `go.mod`、全仓 Go import、`config/config.go`、`Makefile`、`models/`（收口硬编码表名为 `config.TablePrefix + ...`）、`handlers/`、`internal/`、`cmd/`、`frontend/`、`e2e_browser_test/` 及活跃基线文档；
   - 更新 `demo/proto/*.proto` `go_package` 与 `demo/Makefile`，**执行 `make -C demo proto` 重新生成 `demo/demo.pb` 与 `demo/pkg/proto/*/*.pb.go`**，并同步修正 `cmd/seed/main.go`、`cmd/seed/governance_seed.go`（含 `legacyPaymentProtoV1` / `legacyOrderProtoV1`）及 `scripts/seed-large-topology/main.go`。
2. **阶段二：全量测试与 Seed/Demo 准确性专项验收门禁**
   - **静态与前端门禁**：`make lint`（`tsc --noEmit` + `eslint`）与前端 Vitest 全部通过；
   - **物理隔离库全量后端单测**：配置 `TEST_DB_DSN` 执行 `make test`，特别确保 **`TEST_DB_DSN=... go test -v ./cmd/seed`**（`TestSeedDemoData_CompleteGraphAndDecoderAliases`）全绿通过，逐项断言验证 7 模块分层合法、多实现支付契约与自定义服务名解码、4 个 `GIT_SYNC` `ProtoSchema` / 14 个 `ProtoOperation`、带 `@owner/@error_code/@deprecated` 的 `ProtoDocSymbol`、3 条 `DECODED_JSON` 黄金样本、AST 重复簇告警、3 条 MR 战报、3 条 `/proto-governance` 站内通知及 `dashboard.GetOverview` 统计全部准确；
   - **真库重建与 Demo 联动实测**：清理本地开发库旧 `traefik_*` 表并执行 `make build && make seed`，同时验证 `make -C demo build && make -C demo test` 全绿；验证完毕后清理临时构建/运行进程与端口。
3. **阶段三：跨仓联动、目录重命名与推送**
   - 同步更新 `code-bench` 与 `code-common`（`start.sh`、`rules/AGENTS.md`）并验证构建测试；
   - 在 GitHub 将远端仓库 `code-traefik` 重命名为 `code-contract`，本地执行 `mv code-traefik code-contract` 与 `git remote set-url origin`，推送全部相关仓库。

---

## 6. 风险评估与实施前约束

| 风险项 | 影响面 | 缓解与保障措施 |
| :--- | :--- | :--- |
| **`demo/demo.pb` 未重编译导致 `go_package` 或描述符失真** | `make seed` 校验、黄金样本解码或源码行号定位异常 | 阶段一强制执行 `make -C demo proto` 重新生成 `demo/demo.pb` 与 `*.pb.go`，并通过 `TestSeedDemoData_CompleteGraphAndDecoderAliases` 端到端校验 |
| **`cmd/seed` 或 `seed-large-topology` 漏改硬编码表名** | `TRUNCATE` 报错或清理不彻底 | 实施时将 `cmd/seed/main.go` 表清单统一转为 `contract_*`（或基于 `config.TablePrefix` 拼接），并通过真库 `make seed` + `go test ./cmd/seed` 验证两次幂等执行 |
| **本地开发库残留旧 `traefik_*` 表占用空间或混淆视听** | `code_shield` 库中同时存在两套前缀表 | 切换时执行一次清库（删除全部 `traefik_*` 表及 `traefik_traffic_records_id_seq` 序列）后重建 `contract_*` 并运行 `make seed` |
| **跨仓不同步导致宿主门户白屏** | `code-bench` 仍请求 `/traefik/assets/remoteEntry.js` | `code-contract`、`code-bench`、`code-common` 在同一次交付窗口内联动修改并验证 |
| **未批复前误实施** | 打断当前稳定基线 | 本 ADR 状态严格锁定为 `Proposed`；在用户明确下达实施指令前，**严禁修改任何运行代码、数据库前缀、配置或仓库目录** |
