# M27 作战卡：Proto 契约信息架构拆解与四工作台独立化

> **所属阶段**: 阶段四（TDD 实施与前端信息架构重构）  
> **关联设计**: [ADR-19](../05-adr/19-proto-contract-ia-unbundling-and-four-workbenches-adr.md) · [控制面与控制台设计](../02-design/04-control-plane-and-web-console.md) · [Feature Matrix](../01-features/01-feature-matrix.md)  
> **前置依赖**: `M26`（Git MR 门禁流水与站内通知中心已交付）  
> **交付状态**: ✅ 已交付 (`2026-11-15`)

---

## 1. Milestone 目标

解决原 `/schemas`（`Proto 契约`）页面塞入 5 个跨角色重型页签与全局统计条造成的认知负担、首屏挤压和侧边栏高亮失焦问题，按 `ADR-19` 完成：

1. 将侧边栏菜单升级为 **5 组 13 项**，设立独立分组 **`Proto 契约治理`**（`接口参考文档` `/proto-docs`、`源码与 Git 仓` `/proto-sources`、`MR 门禁与规范` `/proto-governance`、`描述集与依赖` `/schemas`）。
2. 将原 `LintRuleAndVocabDrawer` 内部核心面板抽离为可嵌入式 `LintRuleAndVocabPanel`，同时保留抽屉兼容壳。
3. 交付 3 个全新轻量专注工作台（`ProtoDocsWorkbenchPage`、`ProtoSourcesWorkbenchPage`、`ProtoGovernanceWorkbenchPage`），并将 `ProtoSchemaWorkbenchPage` 精简聚焦为 `.pb` 资产台账与跨包依赖图（2 页签）。
4. 在 `App.tsx` 实现 `SchemasRouteWrapper`，对 `/schemas?tab=docs|catalog|sources|mr-gates|lint-*` 提供零断链无损重定向（保留所有 query params 与 `#L<n>` hash），更新站内互跳链接（`ServiceReferenceDocView`、`DashboardWorkbenchPage`）。

---

## 2. TDD 先行测试用例清单

| 用例编号 | 目标测试文件 | 验证场景与预期断言 |
| :--- | :--- | :--- |
| `TC-MENU-01` | `MenuAndLegacyRoutes.test.tsx` | 校验 `traefikMenuConfig.groups` 为 **5 组 13 项**，精确匹配分组名（`全局概览`、`架构数字孪生`、`Proto 契约治理`、`契约核验与回归`、`环境与治理`）与 `Proto 契约治理` 下 4 个路径及 `adminOnly` 标识。 |
| `TC-MENU-02` | `MenuAndLegacyRoutes.test.tsx` | 验证 `SchemasRouteWrapper` 在独立模式与 `/traefik` 嵌入模式下对 `?tab=docs`、`?tab=catalog&claimed_status=UNCLAIMED`、`?tab=sources&file_id=501#L42`、`?tab=mr-gates&runId=102`、`?tab=lint-rules`、`?tab=lint-vocab`、`?tab=lint-report` 的无损重定向目标；验证无 `tab` 或 `?tab=assets|graph` 保留在 `/schemas` 渲染资产工作台。 |
| `TC-PDOC-01` | `LivingProtoDocsPane.test.tsx` + `MenuAndLegacyRoutes.test.tsx` | `/proto-docs` 默认展示 `全注释参考文档`，切换 `契约认领与方法目录` 同步 `docs_view=catalog-ops`；页面**不渲染** `SchemaPoolSummaryBar` 与 `上传描述集 (.pb)` 按钮；旧路径 `/schemas?tab=catalog&claimed_status=UNCLAIMED` 自动导向 `/proto-docs` 且继承筛选参数。 |
| `TC-PSRC-01` | `GitSourcesWorkbenchPane.test.tsx` + `MenuAndLegacyRoutes.test.tsx` | `/proto-sources` 直接挂载源码与 Git 仓工作台且不渲染 `SchemaPoolSummaryBar`；`ServiceReferenceDocView` 中「工作台源码」链接生成 `/proto-sources?tab=sources&file_id=501#L42`（含嵌入前缀适配）。 |
| `TC-PGOV-01` | `MRGateWorkbenchPane.test.tsx` + `LintRuleAndVocabDrawer.test.tsx` | `/proto-governance` 渲染 4 个主导航 Tab（`Git MR 门禁流水` / `静态检查规则` / `领域标准词库` / `体检报告与 AST 重复簇`），URL `gov_tab` 与主 Tab 双向同步；点击后直接在主工作区嵌入式渲染 `LintRuleAndVocabPanel` 而非狭长抽屉；页面不渲染 `上传描述集 (.pb)`。 |
| `TC-PSCHEMA-01` | `ProtoDependenciesGraphPane.test.tsx` + `SchemaAssetLedgerWorkbench.test.tsx` | `/schemas` 保留 `SchemaPoolSummaryBar`（含 `在线源码直编 (.proto)` 与 `上传描述集 (.pb)`）与双页签（`描述集资产台账` / `跨包依赖关系图`）；`/schemas?active_only=true&search=Account` 保留状态与检索参数兼容；右侧提供前往 `/proto-docs` 与 `/proto-governance` 的快捷导流。 |
| `TC-THEME-01` | `ThemeAndPagination.test.tsx` | 静态扫描新增及改造页面：**零 `any`、零 `dangerouslySetInnerHTML`/`innerHTML`/`alert()`/`confirm()`、100% 复用 `--color-*` 语义 Design Tokens（Light/Dark 双模兼容）**。 |

---

## 3. 实施拆解与交付清单

1. **文档先行（SDD）**：
   - 新增 `ADR-19` 与 `M27` 作战卡；
   - 更新 `docs/README.md`、`docs/01-features/01-feature-matrix.md`、`docs/02-design/04-control-plane-and-web-console.md`、`docs/04-tasks/01-roadmap.md`。
2. **组件解耦与公共面板抽离**：
   - 改造 `LintRuleAndVocabDrawer.tsx`：导出 `LintRuleAndVocabPanel`（支持 `embedded`、`activeSubTab`、`onSubTabChange`、`hideInternalTabs`）并监听 `initialSubTab`/`activeSubTab` 变更自动刷新对应子面板数据。
   - 更新 `ServiceReferenceDocView.tsx` 中的 `SourceWorkbenchDeepLink` 目标路由至 `/proto-sources?tab=sources&file_id=${fileId}${hash}`。
3. **四工作台页面实现**：
   - 新增 `ProtoDocsWorkbenchPage.tsx`（含紧凑头部切换胶囊、文档/目录视图、只读源码抽屉与单方法文档抽屉、资产详情抽屉）。
   - 新增 `ProtoSourcesWorkbenchPage.tsx`（直接聚焦源码与 Git 仓工作台）。
   - 新增 `ProtoGovernanceWorkbenchPage.tsx`（4 个独立主 Tab：`mr-gates` | `rules` | `vocab` | `report` + `LintRuleAndVocabPanel` 内联嵌入式渲染 + 只读源码跳转抽屉）。
   - 精简重构 `ProtoSchemaWorkbenchPage.tsx`（保留资产池汇聚条 + `assets`/`graph` 2 页签 + 向导/抽屉与新工作台跳转入口，新增 `active_only` 查询参数联动）。
   - 更新 `ProtoSchemaWorkbenchPage/index.ts` 导出四个页面及 `LintRuleAndVocabPanel`。
4. **菜单、路由与全站联动**：
   - 更新 `frontend/src/menu.ts` 至 5 组 13 项。
   - 更新 `frontend/src/App.tsx`：新增 `BookOpen`/`Code2`/`ShieldCheck` 图标映射，注册 `/proto-docs`、`/proto-sources`、`/proto-governance` 与 `SchemasRouteWrapper`（嵌入模式 + 独立模式全套路由）。
   - 更新 `DashboardWorkbenchPage.tsx` 的静检卡片跳转目标到 `/proto-docs`、`/proto-sources`、`/proto-governance`。
5. **质量门禁验证与推送**：
   - `make test`（Go `go fmt` + `go vet` + `go test` + 前端 Vitest 全绿）
   - `make lint`（`tsc --noEmit` + `eslint` 零错误零警告）
   - `make build && make clean`（全栈嵌入二进制构建验证后清理临时产物）
   - 自检后执行符合 Conventional Commits 的中文提交并推送至 GitHub。
