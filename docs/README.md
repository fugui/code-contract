# 架构与活体契约治理 (`code-contract`) 文档知识库 (Docs Hub)

> **定位升级**：本项目已从纯技术视角的“业务流量录制工具”战略升维为**「基于真实流量的活体契约、架构一致性与防劣化自愈平台」**。录制与重放作为底层零侵入数据面手段，服务于**接口资产治理、架构防漂移雷达、无依赖自愈防劣化**三大核心业务价值。

本项目严格采用 **SDD (Spec-Driven Development，规范驱动开发) + TDD (Test-Driven Development，测试驱动开发)** 模式，所有提示词资产、需求边界、系统架构、数据表模型、API 契约、TDD 任务拆解与运维验证手册均在此目录中进行统一版本化管理。

---

## 目录分层架构

```text
docs/
├── 00-prompt/       # 提示词库：记录各阶段 SDD/TDD Prompt 资产与上下文演进指令
│   ├── 01-init-features-prompt.md                 # 阶段一：特性矩阵 (Feature Matrix) 与需求边界生成提示词
│   ├── 02-design-prompt.md                        # 阶段二：系统架构、GORM 模型与微前端交互设计提示词
│   ├── 03-api-and-tasks-prompt.md                 # 阶段三：API 契约冻结与 TDD Milestone 任务拆解提示词
│   ├── 04-tdd-implementation-prompt.md            # 阶段四：TDD 红-绿-重构编码实施与全链路交付验证提示词
│   └── 05-e2e-browser-test-and-ux-review-goal-prompt.md # 阶段四/验收：微前端全链路端到端黑盒测试与产品交互体验走查 Goal 提示词
├── 01-features/     # 特性与需求边界：特性清单矩阵 (Feature Matrix)、P0~P2 优先级与 Non-Goals
│   └── 01-feature-matrix.md                       # 阶段一：需求范围、核心特性矩阵 (F-01~F-39 含子特性) 与验收场景 (SC-01~SC-22)
├── 02-design/       # 系统技术架构 (SDD)：双子系统边界、数据面引擎、业务拓扑与数据模型
│   ├── 01-system-architecture.md                  # 【总览篇】系统总体架构、双模拓扑、纳管/布防分离与可见性隔离、主从信道与全链路时序 (v1.12.0)
│   ├── 02-data-plane-and-cros-rpc-engine.md       # 【数据面篇】CROS RPC 协议栈、Agent eBPF 采集、纯 Go 可插拔传输驱动 (`pkg/transport` / `builtin-drt-uprobe` / `target_pids[]`)、自愈重放与 CSG 底座 (v1.9.0)
│   ├── 03-architecture-elements-and-topology.md   # 【业务篇】架构元素建模 (SSOT)、双态服务身份、L1 双粒度放置/依赖、分层身份指纹、PhysicalReconciler、两阶段归因、统一目录五态雷达与架构收件箱 (v1.11.0)
│   ├── 04-control-plane-and-web-console.md        # 【管理面篇】Server 控制面、5 组 14 项菜单、`/discovery` 现网发现工作台、直录 Saga、假模块迁移、架构收件箱与 RBAC 审计 (v1.15.0)
│   ├── 05-database-schema.md                      # 【数据模型篇】36 张核心 contract_* 业务表 + contract_settings 物理表 GORM 字典（含 ADR-24 新增 6 表）、分区并发索引与迁移顺序 (v1.11.0)
│   └── 06-agent-observability-and-self-healing-design.md # 【运维可观测与自愈篇】Agent 探针特权感知、体检诊断与全链路闭环自愈设计
├── 03-api/          # 接口与数据契约：RESTful / SSE API 签名、主从信道契约、RBAC 权限与审计规范
│   └── 01-http-api-contract.md                    # 【API 契约】控制面 REST/SSE 字典（含 ADR-24 现网发现/快照/纳管/直录/目录/收件箱 21 项接口）、主从 HTTP/WS (`target_pids[]`, `0x01~0x08`) 与 RBAC 守卫 (v1.11.0)
├── 04-tasks/        # TDD 实施规划与验收：Milestone 1~32 依赖拓扑、任务作战卡与自动化验收标准 (AC)
│   ├── 01-roadmap.md                              # 【工程路线图】模板清理策略、M1~M32 DAG 拓扑、F-01~F-39 / SC-01~SC-22 覆盖矩阵与安全质量门禁 (v1.20.0)
│   ├── 02-m1-crosrpc-and-domain-models.md         # 【M1 作战卡】协议栈 (pkg/crosrpc)、信封协议与 13 张表/分区管理器
│   ├── 03-m2-arch-proto-and-agent-control.md      # 【M2 作战卡】三级架构元素、.pb 动态编解码与公私域 Agent 排他租约管控
│   ├── 04-m3-agent-dataplane-ebpf-and-replay.md   # 【M3 作战卡】数据面 Agent 信道、cilium/ebpf 采集与零 .pb 二进制自愈重放
│   ├── 05-m4-recording-topology-and-diff-engine.md# 【M4 作战卡】多进程协同录制、离线架构一致性验证与双层回归 Diff 引擎
│   ├── 06-m5-microfrontend-and-e2e-delivery.md    # 【M5 作战卡】微前端五大工作台、四段报文解剖抽屉与 SC-01~SC-08 端到端交付
│   ├── 07-m6-hierarchical-topology-canvas.md      # 【M6 作战卡】分层复合架构拓扑与一致性画布（React Flow + ELK Worker）
│   ├── 08-m7-proto-schema-deepening-and-nested-contract-workbench.md # 【M7 作战卡】Protobuf 契约资产化治理、嵌套服务表与消息体字段树
│   ├── 09-m8-console-ia-restructuring-and-debug-workflow.md          # 【M8 作战卡】控制台三层信息架构重组、录制-调试-重放闭环与物理进程单点收口
│   ├── 10-m9-recording-drawer-and-process-tree-selection.md          # 【M9 作战卡】协同录制抽屉重构、作用域卡片解挤、Agent 状态防呆与业务进程树
│   ├── 11-m10-arch-process-blueprint-and-premount-workflow.md        # 【M10 作战卡】架构物理进程蓝图解耦、Agent 1:1 挂载覆盖与 eBPF 默认自动预挂待命工作流
│   ├── 12-m11-enhanced-proto-schema-asset-management.md              # 【M11 作战卡】增强 Protobuf 描述集资产管理、跨集依赖闭包解析与双模契约工作台
│   ├── 13-m12-bidirectional-capture-and-agent-recording-ux.md        # 【M12 作战卡】单进程开箱即用出入站双录、数据面链路修复与双工作台协同录制体验优化
│   ├── 14-m13-packet-causality-call-tree-and-sequence-workbench.md   # 【M13 作战卡】报文时序因果树、内联甘特瀑布、全链路时序泳道与事务闭包重放
│   ├── 15-m14-governance-dashboard-and-ia-restructuring.md           # 【M14 作战卡】平台级契约治理全景大盘、三维封顶健康指数与信息架构升维
│   ├── 16-m15-golden-contract-suites-and-curation-workbench.md       # 【M15 作战卡】黄金契约套件自包含快照库、双源重放解耦与事务固化工作台
│   ├── 17-m16-design-time-breaking-change-radar.md                   # 【M16 作战卡】设计态契约破坏性变更阻断雷达、下游血缘推导与回滚逃生舱
│   ├── 18-m17-ci-cd-quality-gate-and-automated-enforcement.md        # 【M17 作战卡】CI/CD 自动化强阻断质量门禁、实测边回传与排他锁有界排队
│   ├── 19-m18-business-centric-packet-inspector-redesign.md          # 【M18 作战卡】面向业务视角的 RPC 报文透视与双模解剖抽屉重构
│   ├── 20-m19-scalable-topology-focus-and-layout-persistence.md      # 【M19 作战卡】大规模架构拓扑渐进式下钻聚焦、多维降噪与双层混合布局持久化
│   ├── 21-m20-multi-implementation-service-contract.md               # 【M20 作战卡】多实现服务契约（接口库定义 + 多进程实现 + 调度中心分发）建模、拓扑与验证
│   ├── 22-m21-topology-single-layer-drilldown-and-layout-ux.md       # 【M21 作战卡】架构拓扑单层平铺逐级下钻重构、ELK 正交布局校准与全链路易用性升级
│   ├── 23-m22-protocompile-layering-and-dependency-graph.md          # 【M22 作战卡 | ✅ 已交付】纯 Go protocompile 四级虚拟解析器、layer_level 正反向拦截与 BASE_LIB 影响及依赖图/矢量导出 (F-32)
│   ├── 24-m23-living-proto-docs-and-readonly-sources.md              # 【M23 作战卡 | ✅ 已交付】全注释活体参考文档中心 (F-18) 与轻量无锁只读源码视图 (增量 <=150KB gzip)
│   ├── 25-m24-static-lint-vocabulary-and-duplicate-ast.md            # 【M24 作战卡 | ✅ 已交付】安全底座 (CONTRACT_CRYPTO_KEY / SafeDialer 信任白名单)、设置脱敏、规范引擎、原生词库 Seed/CRUD、3s LLM 与 Jaccard>=0.85 三护栏重复检测
│   ├── 26-m25-git-sync-and-monorepo-bundles.md                       # 【M25 作战卡 | ✅ 已交付】Git 无状态同步、GitRepo 1:N GitProtoBundle 多包对账、Tier-2 DB Resolver 与只读历史 Diff
│   ├── 27-m26-mr-gates-notifications-and-compliance-dashboard.md     # 【M26 作战卡 | ✅ 已交付】MR 四合一门禁 (FF 去重/原位评论/Override P1)、SSE 按 userID 定向、持久化通知 (LAN code-notifier/SMTP) 与 F-24 合标卡片
│   ├── 28-m27-proto-ia-unbundling-and-four-workbenches.md            # 【M27 作战卡 | ✅ 已交付】Proto 契约控制台 IA 拆解：5 组 13 项菜单、四工作台正交拆分与无损深链兼容 (ADR-19)
│   ├── 29-m28-csg-service-discovery-and-multi-mode-simulation.md     # 【M28 作战卡 | ✅ 已交付】CSG 双层自动发现 (纯 Go CrosTCP/CrosMesh)、蓝图一键导入、64B 服务名映射与三模仿真闭环 (ADR-21 v2.2.0)
│   ├── 30-m29-pluggable-transport-and-mesh-uprobe-capture.md         # 【M29 作战卡 | ✅ 已交付】纯 Go 可插拔传输驱动 (pkg/transport)、builtin-drt-uprobe 零侵入 Mesh 采集与 Mesh➔TCP 仿真重放闭环 (ADR-22 v2.0.0 & ADR-23 v1.1.0)
│   ├── 31-m30a-discovery-workbench-and-snapshot-diff.md              # 【M30a 作战卡 | 📋 待实施】只读破冰：/discovery 进程主轴工作台、实时缓存/离线回退与持久化快照双模 Diff (ADR-24)
│   ├── 32-m30b-physical-logical-decoupling-and-migration.md          # 【M30b 作战卡 | 📋 待实施】物理/逻辑双态解耦：L1 双粒度 Schema、Enroll/Arm 分离、Reconciler、无模块物理拓扑与假模块迁移 (ADR-24)
│   ├── 33-m31-quick-record-two-stage-attribution-and-catalog-radar.md # 【M31 作战卡 | 📋 待实施】倍增层：target_pids[]、一键直录 Saga、两阶段归因 Stage 1 与统一契约目录五态雷达 (ADR-24)
│   ├── 34-m32-architecture-inbox-dependency-synthesis-and-mr-impact.md # 【M32 作战卡 | ✅ 已交付】护城河层：架构收件箱 (1:N 拆分/多宿主裁决)、依赖反向审批、归因回补与 MR 权威影响面 (ADR-24)
│   ├── 35-m33-shared-git-cache-foundation.md                         # 【M33 作战卡 | ✅ 已交付】共享 Git 缓存底座、两层锁与防浅克隆降级 (ADR-26)
│   ├── 36-m34-platform-repos-pg-collect-and-normalizer.md             # 【M34 作战卡 | ✅ 已交付】平台仓打通、pg 白名单采集与 Stage B 清理层 (ADR-26)
│   ├── 37-m35-analyze-compile-clean-tree-and-dual-view.md             # 【M35 作战卡 | ✅ 已交付】Stage C/D 解析编译、clean 目录树与双视图工作台 (ADR-26)
│   ├── 38-m36-conflict-arbitration-blast-radius-and-memfs-retirement.md # 【M36 作战卡 | ✅ 已交付】冲突仲裁、影响面联动与下线 go-git memfs (ADR-26)
│   ├── 39-m37-ssot-and-bulk-sync-engine.md                            # 【M37 作战卡 | ✅ 已交付】平台唯一真值源收口与 500+ 仓全量异步同步引擎 (ADR-26 r4 / ADR-27)
│   ├── 40-m38-export-dir-governance-and-stage-b-normalizer.md         # 【M38 作战卡 | ✅ 已交付】方案 A 导出目录治理与 Stage B 清理规则框架 (ADR-26 r4 / ADR-27)
│   ├── 41-m39-dual-subtab-ia-and-governance-ledger.md                 # 【M39 作战卡 | ✅ 已交付】双子标签 IA、树懒加载与 500+ 仓前端治理台账 (ADR-26 r4 / ADR-27)
│   ├── 42-m40-uservice-normalizer-v2-and-pg-flattening.md             # 【M40 作战卡 | 🔄 进行中】Stage B rule-normalizer/v2、uService 转换与 clean 去 /pg 扁平化 (ADR-28)
│   ├── 43-m41-shared-base-harvest-and-pool-stage-c-plus.md            # 【M41 作战卡 | 📋 待实施】R4 共享基座入池与全池 Stage C+ 协调 R1/R2/R3 (ADR-28)
│   └── 44-m42-protocompile-r7-r8-workspace-and-console-closure.md     # 【M42 作战卡 | 📋 待实施】纯 Go protocompile R7 自愈、R8 隔离、workspace 导出与治理台闭环 (ADR-28)
├── 05-adr/          # 架构决策记录 (ADR)：重大技术选型与权衡记录 (Architecture Decision Records)
│   ├── 01-microfrontend-and-table-prefix-isolation.md                  # ADR-01：微前端集成、共享库表前缀隔离与 TDD 测试策略
│   ├── 02-ebpf-hook-and-dual-subsystem-adr.md                          # ADR-02：eBPF 入站旁路钩子、Cookie 原目的还原与无 Schema 数据面解耦
│   ├── 03-control-plane-state-recovery-and-lease-reconciliation.md     # ADR-03：控制面状态自愈恢复、Agent 双向租约对账与孤儿探针防御
│   ├── 04-traffic-storage-partitioning-and-relational-normalization.md # ADR-04：海量流量物理分区归档与核心关系规范化解耦
│   ├── 05-hierarchical-topology-canvas-redesign.md                     # ADR-05：分层复合架构拓扑画布重设计、去 Agent 物理建模与一致性比对融合
│   ├── 06-proto-schema-repository-and-field-inspection-adr.md          # ADR-06：Protobuf 契约资产化治理、嵌套服务表重构与消息体字段树 Diff 忽略设计
│   ├── 07-console-ia-restructuring-and-workflow-workbench-adr.md       # ADR-07：微前端控制台信息架构重组、“录制-调试-重放”高频工作流闭环与物理进程收口
│   ├── 08-recording-drawer-and-process-tree-selection-adr.md          # ADR-08：协同录制抽屉重构与进程树选择器设计（Drawer 侧边栏、Agent 选择防呆与业务分组树）
│   ├── 09-arch-process-ssot-and-ebpf-premount-standby-adr.md          # ADR-09：架构物理进程蓝图 SSOT 解耦、Agent 1:1 挂载覆盖与 eBPF 默认自动预挂待命架构
│   ├── 10-enhanced-proto-schema-file-management-adr.md                # ADR-10：增强 Protobuf 描述集管理：多 .pb 资产台账、跨集依赖闭包解析与全局单一生效池
│   ├── 11-single-process-bidirectional-capture-and-recording-ux-adr.md # ADR-11：单进程开箱即用出入站双向采集、数据面链路修复与双工作台协同录制体验优化
│   ├── 12-packet-causality-call-tree-and-sequence-ux-adr.md            # ADR-12：报文时序因果树、内联甘特瀑布、全链路时序泳道与事务闭包重放架构决策
│   ├── 13-traffic-debug-tree-metadata-hydration-and-ux-repair-adr.md   # ADR-13：独立流量调试台因果树元数据水合缺陷与链路展示降级修复决策
│   ├── 14-governance-dashboard-golden-suite-and-ci-gate-adr.md        # ADR-14：契约治理全景大盘、黄金用例套件与 CI/CD 发布强阻断门禁架构决策
│   ├── 15-scalable-topology-focus-and-layout-persistence-adr.md       # ADR-15：大规模架构拓扑渐进式下钻聚焦、多维降噪与双层混合布局持久化架构决策
│   ├── 16-multi-implementation-service-contract-adr.md               # ADR-16：多实现服务契约（接口库定义 + 多进程实现 + 调度中心分发）建模与表达架构决策
│   ├── 17-topology-single-layer-drilldown-and-layout-ux-adr.md       # ADR-17：架构拓扑单层平铺逐级下钻、ELK 正交布局校准与全链路易用性升级设计
│   ├── 18-proto-full-lifecycle-and-git-mr-governance-adr.md            # ADR-18：废弃未落地的 code-proto 与 protohub 后，Proto 全生命周期与 Git/MR 协同治理架构决策
│   ├── 19-proto-contract-ia-unbundling-and-four-workbenches-adr.md     # ADR-19：Proto 契约控制台信息架构拆解与四工作台正交拆分架构决策 (5 组 13 项菜单 + 无损深链重定向)
│   ├── 20-module-rebranding-*-to-code-contract-adr.md       # ADR-20：[已实施] 模块正式更名为 code-contract 的战略演进与全仓干净重构方案 (v1.2.0)
│   ├── 21-csg-aware-agent-service-discovery-and-replay-mock-adr.md     # ADR-21：[已采纳] 基于 @csg-ver 现网实证与纯 Go CrosMeshBackend 的 CSG 服务/依赖自动发现及多模式仿真测试架构 (v2.2.0)
│   ├── 22-pluggable-transport-driver-and-mesh-capture-adr.md           # ADR-22：[已采纳] Agent 纯 Go 可插拔传输层架构 (pkg/transport) 与 dophi_mesh 采集➔TCP 重放跨传输闭环设计 (v2.0.0)
│   ├── 23-dophi-mesh-uprobe-bypass-capture-adr.md                      # ADR-23：[已采纳] 基于内核 uprobe 的 dophi_mesh 数据面旁路观测工程实证与纯 Go 采集通道裁决 (v1.1.0)
│   ├── 24-csg-discovery-process-centric-and-physical-logical-boundary-adr.md # ADR-24：[已定稿待实施] 以「现网发现」为中心的活体契约工作流水线与物理/逻辑双态架构决策 (v1.1.0)
│   ├── 25-noise-service-filtering-and-traffic-denoising-adr.md               # ADR-25：[已实施] 低关注/噪音服务全链路降噪治理（双源 SSOT + Agent 源头丢弃 + 事务分析折叠）
│   ├── 26-shared-git-cache-and-subsystem-proto-tree-adr.md                 # ADR-26：[Implemented / r4] 复用平台代码仓与共享宿主机 Git 缓存，构建「采集 → 清理整顿 → 解析 → 分仓编译」四段式跨仓 Proto 流水线与按子系统组织的只读聚合目录树
│   ├── 27-proto-export-directory-and-normalization-governance-adr.md       # ADR-27：[Implemented] 500+ 平台仓全量零勾选同步、方案 A 导出后目录治理与 Stage B 清理整顿架构决策
│   └── 28-uservice-normalization-pg-flattening-and-compile-closure-adr.md  # ADR-28：[Proposed / r2] 现网 uService 清洗转换、clean 目录去 /pg 扁平化与全池编译收敛架构决策 (R0~R8)
├── 06-ops/          # 交付与验证：双模联调、物理隔离数据库测试与生产环境单二进制部署手册
│   └── 01-verification-and-deployment-guide.md    # 联调验证、自动化回归测试与生产部署操作指南
└── 07-references/   # 外部参考与上位契约基准：协议包规范、字节字典与领域标准文档
    └── 01-cros-rpc-protocol-spec.md               # DOPHI CROS RPC 协议二进制报文格式与字段级规范说明
```

---

## 目录治理规则

1. **全链路双位编号递进**：
   - 一级子目录采用阶段双位数字前缀（`00-` ~ `07-`）；
   - 各阶段目录下的**所有 Markdown 文档必须带双位数字编号前缀**（如 `01-feature-matrix.md`、`01-system-architecture.md`、`01-http-api-contract.md`、`01-roadmap.md` ~ `09-m8-*.md`），保证在文件树、IDE 及文档中心中保持严格时序与逻辑有序性。
2. **规范先行与测试驱动 (SDD + TDD)**：
   - 任何业务代码实施前，必须先在 `01-features/`、`02-design/`、`03-api/` 与 `04-tasks/` 中冻结设计契约与验收标准 (AC)；
   - 实施阶段遵循 **TDD（先写测试 -> 运行失败 Red -> 编写实现通过 Green -> 重构优化 Refactor）**，所有数据库单测必须通过 `testdb.SetupIsolatedDB(t)` 实现物理隔离，eBPF 内核测试通过 `//go:build linux && ebpf` 隔离。
3. **决策可溯**：凡涉及关键架构选型、数据模型拆分、跨模块交互或性能权衡，须在 `05-adr/` 中以标准 ADR 模板记录背景与决策理由。
