# ADR-20：将模块正式更名为 Code-Contract（code-contract）的战略演进与全仓干净重构方案

- **文档编号**：`20-module-rebranding-code-traefik-to-code-contract-adr.md`
- **文档版本**：v1.2.0
- **日期**：2026-11-15
- **状态**：📋 提议中 (Proposed — 仅完成方案定稿与影响面盘点，**暂不实施**)
- **关联特性**：全系统全局标识（涉及 `F-01`~`F-32`、微前端契约、数据库表前缀、CI/CD 门禁、Demo 集群与 Seed 演示基线）
- **演进与替代关系**：
  - 替代并演进 [`ADR-01`](./01-microfrontend-and-table-prefix-isolation.md) 中关于 `ModuleKey = "traefik"`、`traefik_` 表前缀与 `base: '/traefik/'` 的初始命名约定；
  - 演进 [`ADR-14`](./14-governance-dashboard-golden-suite-and-ci-gate-adr.md) 中 `X-Traefik-CI-Token` 鉴权头约定与 [`ADR-18`](./18-proto-full-lifecycle-and-git-mr-governance-adr.md) 中 `TRAEFIK_CRYPTO_KEY` 及 MR 状态上下文 `code-traefik/proto-mr-gate` 约定；
  - 联动调整宿主门户 `code-bench`、公共基础设施 `code-common`、文档引用仓 `code-archdoc` 与本地/GitHub 仓库命名。

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

### 1.3 零现网包袱窗口期与候选命名评估

当前系统**尚未有任何现网生产部署，所有历史开发/测试数据库数据均可直接删除并由 `cmd/seed` 重建**。趁投产前完成更名，可做到**零历史兼容包袱、零灰度双轨成本、一次性干净切换**。

针对候选命名进行了四维度评估（5 分制）：

| 候选名称 | 语义贴合度 | 行业撞名风险（低分佳） | 仓内领域术语冲突度 | `grep` 可区分度 | 综合结论 |
| :--- | :---: | :---: | :---: | :---: | :--- |
| **`code-contract`**（码约） | **5 / 5** | 低（虽中文有“合同”日常义，但在 `code-*` 工程体系中指代明确） | 中（仓内已有 `ServiceContract` / `contract_id` 137 处） | 需区分 `contract_` 与领域字段 | **✅ 选用**：最精确覆盖“设计态 + 架构态 + 运行态”三重契约 |
| `code-pact` | 4 / 5 | 高（与契约测试框架 Pact 强撞名） | 低 | 高 | 淘汰：仍会引发开源组件误解 |
| `code-twin` | 3 / 5 | 低 | 低 | 高 | 淘汰：偏重“架构数字孪生”，弱化了 `.proto` 资产与 MR 门禁主线 |
| `code-atlas` | 3 / 5 | 中（与数据治理/架构图谱工具同名） | 低 | 高 | 淘汰：偏重静态地图，难以体现运行时校验与自愈防劣化 |

**关于 `code-contract` 与仓内领域术语重名的显式缓解措施**：
实测当前代码库中已存在 `contract_id`（137 处）、`contract_service_name`（14 处）、`ServiceContract`、`contract_resolver.go` 与 `LIB_PAYMENT_CONTRACT` 等领域术语。为防止“模块前缀 `contract_`”与“服务契约领域模型 `ServiceContract`”造成阅读与维护混淆，确立以下三条缓解规则：
1. **模块配置实体中性命名**：`models/setting.go` 中的 `TraefikSetting` 更名为 **`ModuleSetting`**（而非 `ContractSetting`），避免被误解为“单个服务契约的局部配置”；
2. **显式术语边界**：在 `code-contract/AGENTS.md` 术语表中明确区分「平台级模块标识 `code-contract` / 表前缀 `contract_`」与「业务领域实体 `ServiceContract` / 外键 `contract_id`」；
3. **单向残留门禁**：更名验收以搜索旧词 `traefik` 归零作为客观门禁，不依赖模糊搜索 `contract`。

**`code-contract` 三位一体业务公约数**：
- **设计态契约**：`.proto` 源码管理、AST/语义规范体检、MR 四合一门禁、活体参考文档与 `.pb` 描述集资产；
- **架构态契约**：三级架构数字孪生、模块间 `PROVIDE / DEPEND` 与 `MULTI_IMPL` 接口库契约；
- **运行态契约**：eBPF 实测流量与设计态拓扑的一致性格栅校验（`Violation / Uncovered`）、黄金套件与 `ScopedMock` 因果闭包防劣化回归。

### 1.4 否决的备选方案 (Rejected Alternatives)

1. **方案 A：维持 `code-traefik` 命名不变**
   - **否决理由**：名实倒置与网关工具（Traefik）撞名歧义会随着文档、CI/CD 流水线接入与跨团队推广呈指数级放大，越晚纠正成本越高。
2. **方案 B：仅修改产品展示名（`ServiceName` / 菜单标题），保留 `traefik` 全部技术标识**
   - **否决理由**：`ModuleKey`（`/traefik` 路由）、`traefik_*` 表名、`X-Traefik-CI-Token`、`TRAEFIK_CRYPTO_KEY` 与 `code-traefik-agent` 二进制名直接暴露给接入方、CI 流水线及新开发者，“界面叫契约平台、命令行和数据库全叫 Traefik”反而制造更严重的认知撕裂；且当前处于零现网窗口期，一步到位干净切换的综合成本最低。

---

## 2. 目标身份契约与全维度映射方案 (Target Identity Mapping)

为保持与工作区上位宪法 [`code-common/rules/AGENTS.md`](../../../code-common/rules/AGENTS.md) 中 `ModuleKey`、`TablePrefix`、`AdminRole` 三位一体契约一致，确立如下完整映射：

| 契约维度 | 现行标识 (`code-traefik`) | 目标标识 (`code-contract`) | 变更说明与约束 |
| :--- | :--- | :--- | :--- |
| **工程目录与 Git 仓库** | `code-traefik` / `fugui/code-traefik.git` | `code-contract` / `fugui/code-contract.git` | 本地目录、GitHub 远端仓库名同步更新 |
| **Go Module 与根包名** | `module code-traefik`<br>`package traefik`（`embed.go`） | `module code-contract`<br>`package contract` | 全仓 Go `import`、`Makefile` `LDFLAGS`、`cmd/server/main.go` 别名及 `demo/proto` `go_package` 同步更新 |
| **产品展示名** | `Code-Traefik` / `架构与活体契约治理平台` | `Code-Contract` / `码约 · 架构与活体契约治理平台` | `config.ServiceName`、前端标题、`frontend/index.html` 与文档品牌统一 |
| **模块标识 (`ModuleKey`)** | `traefik` | `contract` | 门户路由 `/contract`、Vite `base: '/contract/'`、Federation `name: 'contract'` |
| **双二进制产物名** | `code-traefik-server` / `code-traefik-agent` | `code-contract-server` / `code-contract-agent` | `Makefile`、`cmd/server`、`cmd/agent`、`handlers/agent_handler.go`、`.gitignore`、`demo/` 与 `code-common/scripts/start.sh` |
| **管理员角色 (`AdminRole`)** | `traefik_admin` | `contract_admin` | `config.AdminRole`、`frontend/src/constants.ts` 及 `code-bench` 角色徽章与鉴权逻辑 |
| **端口分配** | 后端 `:8091` / 前端 `5181` | **保持 `:8091` / `5181` 不变** | 避免与兄弟模块端口冲突，保持现有本地联调习惯与 Agent 默认端口 `:9091` 不变 |
| **数据库表前缀 (`TablePrefix`)** | `traefik_`（31 张物理表 + 分区子表） | **`contract_`** | 详见 §2.1 权衡决策 |
| **配置模型类型名** | `TraefikSetting`（含 `TemplateSetting` 别名） | **`ModuleSetting`** | `models/setting.go` 及 17 个引用文件（48 处）同步切换，彻底删除 `TemplateSetting` 别名 |
| **AES-256-GCM 主密钥环境变量** | `TRAEFIK_CRYPTO_KEY` | `CONTRACT_CRYPTO_KEY` | `EnvTraefikCryptoKey` ➔ `EnvContractCryptoKey`，直接切换，不保留旧环境变量兜底 |
| **Agent 环境变量** | `TRAEFIK_AGENT_ALLOW_IPS`<br>`TRAEFIK_AGENT_BIN_PATH` | `CONTRACT_AGENT_ALLOW_IPS`<br>`CONTRACT_AGENT_BIN_PATH` | 直接切换，不保留旧环境变量兜底 |
| **CI/CD 门禁请求头与变量指引** | `X-Traefik-CI-Token`<br>`TRAEFIK_CI_TOKEN`（UI 示例） | `X-Contract-CI-Token`<br>`CONTRACT_CI_TOKEN` | 直接切换，Query 防泄拦截更新为 `x-contract-ci-token` |
| **Agent 鉴权请求头（F-17 契约）** | `X-Traefik-Agent-Token` | `X-Contract-Agent-Token` | 同步更新 `internal/agent/server/http_server.go` TODO 标注与特征矩阵/路线图契约 |
| **审计动作命名空间** | `traefik_git:*`（7 处 `recordAudit`） | `contract_git:*` | `handlers/git_repo_handler.go` 同步更新 |
| **MR 状态外部上下文** | `code-traefik/proto-mr-gate` | `code-contract/proto-mr-gate` | `internal/server/gitclient/mr_runner.go` 推送至 GitLab/GitHub 的 Commit Status Context |
| **前端包名、DOM、存储键与 E2E** | `code-traefik-frontend` / `appName: "code-traefik"`<br>`.code-traefik-*` / `traefik_topo_draft_v2_`<br>`TraefikMenuConfig` / `traefikMenuConfig`<br>`TRAEFIK_ENTRY_URL` | `code-contract-frontend` / `appName: "code-contract"`<br>`.code-contract-*` / `contract_topo_draft_v2_`<br>`ContractMenuConfig` / `contractMenuConfig`<br>`CONTRACT_ENTRY_URL` | `package.json`、`version.json`、CSS 类名前缀、`localStorage` 拓扑草稿前缀、菜单配置符号及 E2E 脚本常量 |

### 2.1 关键权衡：`TablePrefix` 坚持使用 `contract_` 而非缩写 `ct_`

针对 `traefik_service_contracts` 更名为 `contract_service_contracts` 后出现的轻微词根重复（`contract_..._contracts`），团队评估了“统一使用 `contract_`”与“例外缩写为 `ct_`”两个方案：

- **最终决策**：**统一使用 `contract_` 作为唯一 `TablePrefix`**，不引入 `ct_` 等缩写特例。
- **理由**：
  1. 严格遵循工作区上位规范中「`TablePrefix = "<module_key>_"`」的单一真实源（SSOT）约定，避免后续开发者或 AI 代理误判模块标识与表前缀映射关系；
  2. 全库 31 张表语义各异（`contract_subsystems`、`contract_modules`、`contract_proto_schemas`、`contract_traffic_records`、`contract_agents` 等），除 `contract_service_contracts` 外其余 30 张表均清晰自然，不应为单表微瑕破坏全局命名一致性。

---

## 3. 全仓与跨仓影响面精确盘点 (Impact Inventory)

基于当前代码基线（`HEAD 3da3577`）实测，全工作区共涉及 **4 个仓库**（3 个核心联动代码仓 + 1 个文档引用仓）：

### 3.1 本仓（`code-traefik` ➔ `code-contract`）

1. **构建、根包、配置与版本层**：
   - `go.mod`、`embed.go`（`package contract`）、`cmd/server/main.go`（导入别名）、`Makefile`（`BINARY`、`AGENT_BINARY`、`LDFLAGS`）、`.gitignore`（`code-traefik-server`、`code-traefik-agent`）；
   - `config/config.go`（`ModuleKey`、`ServiceName`、`AdminRole`、`TablePrefix`）、`config.yaml.example`（注释、`dbname` 说明、`oauth2.client_id: "code-contract"`）及本地未入库 `config.yaml`；
   - `cmd/agent/main.go`（命令行提示、`--claim-code-file` 默认路径示例、`CONTRACT_AGENT_ALLOW_IPS`）、`cmd/agent/allow_ips_test.go`、`cmd/agent/deps_test.go`（源码包导入路径禁止清单）。
2. **数据库模型与重建底座（`models/`）**：
   - `models/setting.go`：`TraefikSetting` 更名为 **`ModuleSetting`**（移除 `TemplateSetting` 别名），同步修改其在 `handlers/`（`ci_auth.go`、`settings.go`、`git_repo_handler.go` 等）、`internal/server/`、`models/` 共 **17 个文件 / 48 处引用**；
   - 全部 31 张业务与配置表 `TableName()`（其中 `git_repo.go` 与 `linter.go` 中 8 张表的 `TableName()` 尚为硬编码字符串，统一改用 `config.TablePrefix + "..."`）；
   - **GORM 标签与原生 DDL 边界说明**：由于 Go 结构体 Tag（如 `uniqueIndex:uk_traefik_subsystems_code`，分布于 `arch.go` 46 处、`recording.go` 31 处、`mr_gate.go` 30 处、`replay.go` 20 处等）与 `models/db.go` 原生 SQL DDL（251 处）无法拼接运行期变量，**不强求消除结构体 Tag 与原生 DDL 中的表/约束名硬编码**，而是将其全部规范替换为 `contract_*`，并以“全仓残留 `traefik` 扫描归零”作为最终门禁；
   - `models/partition_manager.go`：物理分区母表、序列（`contract_traffic_records_id_seq`）、分区命名正则（`^contract_traffic_records_y(\d{4})m(\d{2})d(\d{2})$`）与 GIN/普通索引；
   - `models/db.go`：**借更名窗口彻底清理历史升级垫片**（详见 §4.2.1），保留纯净的幂等建表与最终态约束定义；
   - `models/*_test.go` 中表名前缀、索引名与环境变量断言。
3. **服务端业务逻辑、Agent 与公共包层（`handlers/`、`internal/`、`pkg/`）**：
   - **鉴权与安全**：`internal/server/security/crypto.go`（`EnvContractCryptoKey`）、`ssrf.go`（`contract_settings.outbound_trusted_cidrs` 注释）、`handlers/ci_auth.go`（`X-Contract-CI-Token`）、`handlers/settings.go`、`handlers/agent_handler.go`（`CONTRACT_AGENT_BIN_PATH` 与 `code-contract-agent` 下载名）、`internal/agent/server/http_server.go`（服务端注释与 F-17 `X-Contract-Agent-Token` TODO）；
   - **Git/MR 与原生 SQL 表名**：`internal/server/gitclient/{sync_service,virtual_tree,mr_runner}.go`（咨询锁 `contract_git_sync_%d`、原生 SQL、Commit Status `code-contract/proto-mr-gate`）以及**所有含原生 SQL / 字符串表名引用的业务文件**：`handlers/git_repo_handler.go`（子查询与 7 处 `traefik_git:*` 审计动作）、`handlers/process_handler.go`、`handlers/lint_vocab_handler.go`、`handlers/arch_process_handler.go`、`handlers/proto_handler.go`、`handlers/proto_file_handler.go`、`handlers/golden_suite_handler.go`、`handlers/topology_layout_handler.go`、`handlers/mr_gate_handler.go`、`internal/server/decoder/{protocompile,registry,breaking}.go`、`internal/server/topology/{static_topology,service_contract,graph_topology,tree}.go`、`internal/server/protodoc/{extractor_service,sample_json}.go`、`internal/server/linter/scanner.go`、`internal/server/notify/dispatcher.go`、`internal/server/diff/scheduler.go`、`internal/server/dashboard/overview.go`；
   - **共享基础包与测试（`pkg/`）**：`pkg/agentproto/protocol.go`（注释 `contract_processes.id`）、`pkg/ebpf/` 与 `pkg/replay/` 中 Go import 路径及 `pkg/ebpf/ebpf_unit_test.go`（`comm: "code-contract-agent"`）；
   - 根目录 `e2e_scenarios_test.go` 及各层 `*_test.go`。
4. **Demo 微服务集群与 Proto 编译产物层（`demo/` — 关键准确性依赖！）**：
   - `demo/proto/{common,account,inventory,payment,order}.proto`：全部 5 个文件内的 `option go_package = "code-traefik/demo/pkg/proto/...";` 同步改为 `code-contract/demo/pkg/proto/...`；
   - `demo/Makefile`：`protoc --go_out=. --go_opt=module=code-traefik` ➔ `module=code-contract`，以及 `code-traefik-agent` 拷贝产物名 ➔ `code-contract-agent`；
   - **锁定工具链版本重编译 `demo/demo.pb` 与 `*.pb.go`**：使用与当前基线完全一致的 **`protoc v3.21.12` + `protoc-gen-go v1.36.12`** 执行 `make -C demo proto` 与 `make -C demo build`，确保二进制描述符文件 **`demo/demo.pb`**（内含 `SourceCodeInfo` 注释与 `go_package` 元数据）及 **`demo/pkg/proto/*pb/*.pb.go`** 与源码 **100% 字节同步**且不引入无关版本头噪音，严禁仅文本替换 `.pb.go`；
   - `demo/cmd/**`、`demo/pkg/crosframe/**`、`demo/pkg/payment/**` & `demo/integration_test.go`：Go import 路径更新为 `code-contract/...`；
   - `demo/start.sh`、`demo/start-agent.sh`、`demo/README.md`、`demo/docs/01-cros-rpc-demo-app-proposal.md`：更新二进制路径、Tmux 面板标题、表名说明与联动指引。
5. **Seed 演示数据与大规模拓扑种子层（`cmd/seed/` & `scripts/seed-large-topology/` — 核心准确性红线！）**：
   - **消除重复硬编码清表清单（单一来源重构）**：目前 `cmd/seed/main.go`（第 68~98 行，30 张表）与 `scripts/seed-large-topology/main.go`（第 82~107 行，24 张表）各自复制了一份硬编码 `TRUNCATE` 列表，违背工作区禁止 Copy-Paste 原则。实施时应在 `models` 中收敛导出统一的 **`BusinessTableNames()`**（按受外键依赖拓扑或配合 `CASCADE` 列出全部业务数据表，排除 `ModuleSetting` 等配置表），供两处 Seed 共用，彻底杜绝漏改或增删表不同步；
   - `cmd/seed/main.go`：日志文案更新为 `Code-Contract`，保持 1 个 `demo-local-agent`（`:9091`）、2 个子系统（`ECOMMERCE` L2 / `CLIENT` L4）、4 个功能组、7 个功能模块、5 个服务契约（含 `PaymentService` 多实现 `ALIPAY`/`WECHAT`）、5 项依赖及 6 个 `contract_processes` 挂载实例拓扑完整无损；
   - `cmd/seed/governance_seed.go`：第 273 行 `security.EnvContractCryptoKey`、第 995/1040 行 `legacyPaymentProtoV1` / `legacyOrderProtoV1` 的 `option go_package` 同步更新为 `"code-contract/demo/pkg/proto/..."`（避免历史版本对比产生无意义的包路径噪音）、演示邮箱 `@code-traefik.local` ➔ `@code-contract.local`；
   - `scripts/seed-large-topology/main.go`：Go import 更新为 `code-contract/...`，复用统一清表清单。
6. **微前端工程层（`frontend/`）**：
   - `frontend/package.json`（`name`）、`frontend/index.html`（`<title>`）、`frontend/public/version.json`（`appName: "code-contract"`）、`frontend/vite.config.ts`（`versionTrackerPlugin.appName`、Federation `name`、`base`、代理 `/contract/api`）；
   - `frontend/src/constants.ts`（`MODULE_KEY`、`MODULE_TITLE`、`ADMIN_ROLE`）、`frontend/src/menu.ts`（`ContractMenuConfig` / `contractMenuConfig`）、`frontend/src/App.tsx`、`frontend/src/hooks/useModulePath.ts`（注释）、`frontend/src/index.css`（注释）；
   - `frontend/src/components/ArchCanvas/layout/layoutPersistence.ts`（`DRAFT_KEY_PREFIX = 'contract_topo_draft_v2_'`）；
   - `frontend/src/components/NotificationBell/NotificationBell.css` & `WorkspaceUtilityHeader.tsx`（`.code-contract-embedded-shell`、`.code-contract-utility-header*`）；
   - `frontend/src/pages/AgentManagementPage/AgentInstallGuidePane.tsx`、`frontend/src/pages/AdminSettingsPage.tsx`（`CONTRACT_CRYPTO_KEY`、`CONTRACT_CI_TOKEN`、`X-Contract-CI-Token` 与邮箱占位提示）、`ArchitectureWorkbenchPage/ArchTopologyTabPane.tsx`、`ProtoSchemasPage/BreakingChangeAlertDrawer.tsx`、`ProtoSchemaWorkbenchPage/GitConsistencyDrawer.tsx`；
   - `frontend/src/__tests__/` 中全部 8 个关联单测文件（含 `MenuAndLegacyRoutes.test.tsx`、`AdminSettingsPage.test.tsx`、`ArchCanvasHybridLayout.test.ts` 等）及 `NotificationBell.test.tsx`。
7. **端到端脚本与活跃文档层（`e2e_browser_test/` & `docs/` & `AGENTS.md` & `README.md`）**：
   - `e2e_browser_test/run_portal_e2e.js`（`TRAEFIK_ENTRY_URL` ➔ `CONTRACT_ENTRY_URL`、`/contract`）、`e2e_browser_test/e2e_audit_log.json`；
   - `AGENTS.md`、`README.md` 及全量活跃基线文档：
     - `docs/README.md`、`docs/00-prompt/`（全部 5 篇提示词资产同步更新，避免后续新建 Prompt 沿用旧名）；
     - `docs/01-features/01-feature-matrix.md`（含 F-17 `X-Contract-Agent-Token`）、`docs/02-design/*`（**重点关注 `05-database-schema.md`，实测含 413 处 `traefik` 引用**）、`docs/03-api/01-http-api-contract.md`、`docs/04-tasks/01-roadmap.md`、`docs/06-ops/{01,02}*.md`。

### 3.2 宿主门户（`code-bench`）

- `code-bench/frontend/src/pages/UserManagement.tsx`：`traefik_admin` 角色徽章样式映射与展示标签注释更新为 `contract_admin`；
- `code-bench/models/config_test.go`、`code-bench/handlers/user_test.go`：单测样例中的 `"traefik"` / `"traefik_admin"` 同步更新为 `"contract"` / `"contract_admin"`；
- **本地配置（不入 Git 版本库，须手工切换）**：实测 `code-bench/config.yaml.example` 当前未包含 `traefik` 网关条目，而 `code-bench/config.yaml` 已被 `.gitignore` 忽略；因此切换时必须在本地手工将 `code-bench/config.yaml` 中的 `gateways.traefik` 改为 `gateways.contract`，并将 `docs.path` 更新为 `/home/fugui/codes/code-contract/docs`。

### 3.3 公共底座与上位规范（`code-common`）

- `code-common/scripts/start.sh`：默认模块列表 `DEFAULT_MODULES`、Tmux 分屏标题与启动命令 `code-traefik-server` ➔ `code-contract-server`；
- `code-common/rules/AGENTS.md`（即工作区根目录软链 `AGENTS.md`）：工程名列表与表前缀示例 `traefik_` ➔ `contract_`。

### 3.4 兄弟文档引用仓（`code-archdoc`）

- `code-archdoc/docs/01-features/01-feature-matrix.md`（共 2 处）：端口占用表中的 `traefik :8091` 更新为 `contract :8091`，L1 Code IR 描述中的 `code-traefik` 资产复用说明更新为 `code-contract`。

---

## 4. 干净重建与零兼容债切换策略 (Clean Rebuild & Zero-Legacy-Debt Strategy)

鉴于**当前系统无任何现网部署，所有开发/测试库数据均允许删除重建**，确立以下彻底无技术债的切换策略：

### 4.1 数据库采用「运维一次性清库 + 原生重建 + `make seed` 重塑」

1. **不做存量表在线 `RENAME` 迁移**：不引入复杂的 `MigrateLegacyTraefikPrefix` 迁移代码。
2. **不将旧前缀清理写入后端启动代码**：不在 `models/db.go` `SetupWithDB` 中增加 `DROP TABLE IF EXISTS traefik_*` 代码，防止在应用主流程中永久留存对旧表前缀的引用。改为在 `docs/06-ops/01-verification-and-deployment-guide.md` 中沉淀**一次性运维清库 SQL 脚本**，于本地共享库（如 `code_shield`）切换前执行：
   - 级联删除全部旧版 `traefik_*` 物理表（含分区母表/子表及历史上可能残留的 `traefik_process_modules`）与 `traefik_traffic_records_id_seq` 序列；
   - **保留共享平台公共表**：`users`、`departments`、`sys_audit_logs`；
   - **孤儿审计记录处置**：`sys_audit_logs` 中历史 `action LIKE 'traefik_git:%'` 记录在本地开发库切换时随脚本一并 `DELETE`（如为预发/演示归档环境可按需保留为只读历史审计）。
3. **共享库角色更新**：在共享库 `users` 中将已有测试账号的 `traefik_admin` 角色更新或重新初始化为 `contract_admin`。
4. **建表与 Seed 一键重建**：清库后由 `models.SetupWithDB` 从零幂等创建全部 **31 张 `contract_*` 物理表**与分区母表，并立即执行 `make seed`（`go run ./cmd/seed`）重塑全部演示拓扑、Git/Proto 资产、文档符号、黄金用例、检查报告、MR 战报与通知数据。

### 4.2 外部标识与代码符号直接干净切换（零过渡兼容垫片）

- **不保留双环境变量、双请求头、旧路由重定向或旧 `localStorage` 兜底分支**：
  - `CONTRACT_CRYPTO_KEY`、`CONTRACT_AGENT_ALLOW_IPS`、`CONTRACT_AGENT_BIN_PATH` 直接替换 `TRAEFIK_*`；
  - `X-Contract-CI-Token`（含 Query 防泄 `x-contract-ci-token`）与 `X-Contract-Agent-Token` 直接替换旧请求头；
  - 前端 `contract_topo_draft_v2_`、`.code-contract-*`、`contractMenuConfig` 及 `/contract/*` 路由直接切换，不保留 `/traefik/*` 兼容垫片。

#### 4.2.1 同步清除现有历史升级垫片（彻底偿还存量迁移债）

在 `traefik_*` 切换为全新 `contract_*` 的窗口下，`contract_*` 从未有过历史旧 Schema，当前 `models/db.go` 中为兼容早期版本演进而保留的垫片将全部变成死代码，**必须在实施更名时一并删除**：
1. 删除 `models/db.go` 中的 `cleanLegacyTablesIfNeeded` 函数及 `SetupWithDB` step 0 对其的调用（该函数还存在 `_ = db.Raw(...).Error` 忽略错误的规范违例）；
2. 删除 `models/db.go` 建表后置脚本中历史遗留的 `DROP CONSTRAINT IF EXISTS`、`DROP INDEX IF EXISTS` 以及针对旧空值的历史修复 `UPDATE ... SET source_type = 'UPLOAD_PB'` 等升级垫片，仅保留面向全新 `contract_*` 表的幂等 `ADD CONSTRAINT IF NOT EXISTS`（或 `IF NOT EXISTS (SELECT 1 FROM pg_constraint ...)` 保护块）与 `CREATE INDEX IF NOT EXISTS`；
3. 删除 `models/setting.go` 中的向后兼容别名 `type TemplateSetting = ...`。

### 4.3 历史 SDD/ADR 文档冻结原则

- 已交付归档的 **`ADR-01` ~ `ADR-19`** 与 **`04-tasks/02` ~ `28`** 历史作战卡属于工程演进历史快照，**实施改名时不回改历史正文中的历史语境**；
- 仅在实施阶段为直接受本 ADR 演进的 **`ADR-01`、`ADR-14`、`ADR-18`** 顶部元数据区各追加一行演进提示（指向 `ADR-20`），其余全部改动收口于**活跃基线文档**（`README.md`、`AGENTS.md`、`docs/README.md`、`docs/00-prompt/*`、`01-features/*`、`02-design/*`、`03-api/*`、`04-tasks/01-roadmap.md`、`06-ops/*`）。

---

## 5. 分阶段实施路线图与验收门禁 (Execution Roadmap & DoD — 待批复后启动)

本 ADR 当前仅作架构决策记录与范围冻结，**暂不实施**。后续确认启动实施时，按以下三阶段一次性闭环交付：

### 5.1 替换执行顺序规范

实施阶段一建议按从长到短、精确边界的顺序进行批量替换与人工核对（严格排除 §4.3 冻结的历史 ADR/作战卡及 `ADR-20` 本身）：
1. 包/仓库/制品长标识：`code-traefik-server` ➔ `code-contract-server`、`code-traefik-agent` ➔ `code-contract-agent`、`code-traefik-frontend` ➔ `code-contract-frontend`、`code-traefik` ➔ `code-contract`；
2. 环境变量、请求头与角色/枚举前缀：`TRAEFIK_` ➔ `CONTRACT_`、`X-Traefik-` ➔ `X-Contract-`、`x-traefik-` ➔ `x-contract-`、`traefik_admin` ➔ `contract_admin`、`traefik_git:` ➔ `contract_git:`；
3. 数据库表前缀、索引/约束与存储键：`traefik_` ➔ `contract_`；
4. 符号、展示名与路由键：`Code-Traefik` ➔ `Code-Contract`、`TraefikSetting` / `TemplateSetting` ➔ `ModuleSetting`、`TraefikMenuConfig` ➔ `ContractMenuConfig`、`traefikMenuConfig` ➔ `contractMenuConfig`、根包 `package traefik` ➔ `package contract`、模块键 `'traefik'` / `"/traefik"` ➔ `'contract'` / `"/contract"`。

### 5.2 三阶段执行计划

1. **阶段一：本仓内部重构、垫片清理与 Demo/Proto 重编译**
   - 按 §3.1 与 §5.1 完成全仓重构，`models/setting.go` 切换为 `ModuleSetting`，`models/db.go` 清理 `cleanLegacyTablesIfNeeded` 与历史升级垫片，`models` 导出 `BusinessTableNames()` 供 `cmd/seed` 与 `scripts/seed-large-topology` 共用；
   - 更新 `demo/proto/*.proto` `go_package` 与 `demo/Makefile`，**锁定使用 `protoc v3.21.12` + `protoc-gen-go v1.36.12` 执行 `make -C demo proto`** 重新生成 `demo/demo.pb` 与 `demo/pkg/proto/*/*.pb.go`，并修正 `cmd/seed/governance_seed.go` 中的 `legacyPaymentProtoV1` / `legacyOrderProtoV1`；
   - 更新 `docs/06-ops/01-verification-and-deployment-guide.md`，补齐一次性清库 SQL 与本地环境变量/JWT 切换说明。
2. **阶段二：残留归零门禁、全量测试与真库/门户端到端验证**
   - **客观残留归零门禁 (Definition of Done)**：执行以下扫描命令，**输出必须严格为 0 行**（排除已冻结历史 ADR、历史作战卡及本 `ADR-20`）：
     ```bash
     git grep -n -i traefik -- . \
       ':!docs/05-adr/0*' ':!docs/05-adr/1*' ':!docs/05-adr/20-*' \
       ':!docs/04-tasks/0[2-9]-*' ':!docs/04-tasks/[12][0-9]-*'
     ```
   - **静态与前端门禁**：`make lint`（`tsc --noEmit` + `eslint`）与前端 Vitest 全部通过；
   - **物理隔离库全量后端单测**：配置 `TEST_DB_DSN` 执行 `make test`，特别确保 **`TEST_DB_DSN=... go test -v ./cmd/seed`**（`TestSeedDemoData_CompleteGraphAndDecoderAliases`）全绿通过，逐项断言验证 7 模块分层合法、多实现支付契约与自定义服务名解码、4 个 `GIT_SYNC` `ProtoSchema` / 14 个 `ProtoOperation`、带 `@owner/@error_code/@deprecated` 的 `ProtoDocSymbol`、3 条 `DECODED_JSON` 黄金样本、AST 重复簇告警、3 条 MR 战报、3 条 `/proto-governance` 站内通知及 `dashboard.GetOverview` 统计全部准确；
   - **真库重建、Demo 与宿主门户联调实测**：执行一次性清库 SQL 后运行 `make build && make seed`（并验证二次幂等执行）及 `make -C demo build && make -C demo test`；在本地同时更新 `code-bench/config.yaml` 启动联调栈后，验证 `http://192.168.56.18:8000/contract/assets/remoteEntry.js` 可达并运行 `e2e_browser_test/run_portal_e2e.js`（验证通过后立即停止全部后台服务并清理进程与端口）。
3. **阶段三：跨仓联动、目录重命名、本地切换与推送**
   - 同步更新 `code-bench`、`code-common`（`start.sh`、`rules/AGENTS.md`）与 `code-archdoc`（2 处文档引用）并验证构建测试；在 `ADR-01`、`ADR-14`、`ADR-18` 顶部添加演进提示；
   - 在 GitHub 将远端仓库 `code-traefik` 重命名为 `code-contract`（GitHub 会自动为旧远端 URL 维持重定向，但所有本地 clone 须更新），本地执行 `mv code-traefik code-contract` 与 `git remote set-url origin git@github.com:fugui/code-contract.git`，提交并推送全部相关仓库。

### 5.3 本地开发工作环境手工切换清单

完成阶段三代码推送后，本机环境须按以下清单完成非入库状态切换：
- [ ] **本地配置文件**：修改 `code-bench/config.yaml`（`gateways.contract`、`docs.path: "/home/fugui/codes/code-contract/docs"`）与 `code-contract/config.yaml`（`oauth2.client_id: "code-contract"`）；
- [ ] **开发者 Shell 环境变量**：将 `~/.bashrc` / `~/.profile` 或启动脚本中的 `TRAEFIK_CRYPTO_KEY`、`TRAEFIK_AGENT_*` 更新为 `CONTRACT_CRYPTO_KEY`、`CONTRACT_AGENT_*`（否则 `crypto.go` 将拒绝加解密并导致 Seed 凭据写入与设置页报错）；
- [ ] **浏览器会话**：清除原有登录态并重新登录门户，使 JWT 刷新携带 `contract_admin` 角色；
- [ ] **IDE 工作区与外部工具**：更新 IDE 打开路径为 `/home/fugui/codes/code-contract`（若存在 `.vscode` 或外部脚本引用亦一并刷新）。

---

## 6. 风险评估与实施前约束

| 风险项 | 影响面 | 缓解与保障措施 |
| :--- | :--- | :--- |
| **`demo/demo.pb` 未重编译或版本漂移** | `make seed` 校验、黄金样本解码或无关 `.pb.go` 大面积 diff | 阶段一锁定 `protoc v3.21.12` + `protoc-gen-go v1.36.12` 执行 `make -C demo proto`，并通过 `TestSeedDemoData_CompleteGraphAndDecoderAliases` 端到端校验 |
| **`cmd/seed` 与 `seed-large-topology` 清表漏改** | `TRUNCATE` 报错或表数据清理不彻底 | 重构为共享 `models.BusinessTableNames()` 单一来源，并通过真库两次幂等执行 `make seed` 验证 |
| **共享库残留 `traefik_*` 或孤儿 `sys_audit_logs`** | `code_shield` 库中两套前缀表混存 | 执行 `06-ops` 中的一次性运维清库 SQL（含清理 `traefik_*` 表、序列及可选清理 `sys_audit_logs` 中 `traefik_git:%` 测试日志） |
| **GORM 标签/原生 SQL 漏改** | 运行时查询报错 `relation "traefik_*" does not exist` | 执行 §5.2 `git grep -i traefik` 残留归零门禁，并跑通物理隔离库全量单测 |
| **跨仓与本地 `config.yaml` 不同步导致白屏** | `code-bench` 仍请求 `/traefik/assets/remoteEntry.js` | 跨仓联动提交并在阶段二实测 `http://192.168.56.18:8000/contract/assets/remoteEntry.js` 与 `run_portal_e2e.js` |
| **未批复前误实施** | 打断当前稳定基线 | 本 ADR 状态严格锁定为 `Proposed`；在用户明确下达实施指令前，**严禁修改任何运行代码、数据库前缀、配置或仓库目录** |

---

## 7. 变更记录 (Changelog)

- **v1.2.0 (2026-11-15)**：依据 ADR 检视意见修订：
  1. 修正事实错误：公共审计表更正为 `sys_audit_logs`；澄清 `code-bench/config.yaml.example` 无 `traefik` 条目且 `config.yaml` 不入库，需纳入本地手工切换清单；补充 `code-archdoc` 为第 4 个关联仓库。
  2. 补全映射与影响面：新增根包 `package contract`、`ModuleSetting`（替代 `TraefikSetting` 与 `TemplateSetting` 别名以避开 `Contract` 歧义）、`X-Contract-Agent-Token`、`CONTRACT_CI_TOKEN`、`contract_git:*` 审计动作、`TRAEFIK_ENTRY_URL`、`embed.go`、`.gitignore`、`pkg/`、`demo/integration_test.go`、`frontend/index.html`、`version.json`、`docs/00-prompt/*` 及约 25 个含原生 SQL/表名的 Go 源文件，并指出 `05-database-schema.md`（413 处）为文档重构重点。
  3. 消除自相矛盾并落地可实施性：移除 `SetupWithDB` 内插 `DROP traefik_*` 的提议，改为 `06-ops` 一次性运维清库脚本；将清除 `models/db.go` `cleanLegacyTablesIfNeeded` 与旧升级垫片纳入范围；将“消除硬编码”修正为“基于 `git grep` 的残留归零门禁”；补充候选命名评分表、否决的备选方案、`protoc`/`protoc-gen-go` 锁定版本、Seed 单一清表来源、`ADR-01/14/18` 顶部演进提示、门户集成验证与本地切换检查清单。
- **v1.1.0 (2026-11-15)**：依据“零现网部署可直接清库重建”与“保障 `cmd/seed` 及 `demo` 端到端准确性”修订，移除存量在线表重命名迁移与多兼容垫片策略，补全 `demo/demo.pb` 重编译与 Seed 验收门禁。
- **v1.0.0 (2026-11-15)**：初稿形成，提出 `code-traefik` 更名为 `code-contract` 的战略背景、身份契约与全仓盘点方案（Proposed）。
