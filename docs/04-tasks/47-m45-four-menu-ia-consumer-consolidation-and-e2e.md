# M45 TDD 作战卡：四菜单 IA 切换、消费端聚合与全量旧深链无损重定向 (`ADR-29 r4`)

- **关联里程碑**：M45 (`F-52`, `SC-33`)

## 任务拆分

1. **四菜单 IA 切换与双模式路由重定向 (`frontend/src/menu.ts`, `frontend/src/App.tsx`)**：
   - 更新 `menu.ts` 中 `Proto 契约治理` 4 菜单项：`/proto-docs`（接口文档与目录）、`/proto-sources`（Proto 源码库）、`/proto-pipeline`（契约同步与发布）、`/proto-governance`（规范与 MR 门禁）。
   - 在 `EmbeddedRoutes` 与 `StandaloneShell`（含 `${MODULE_BASE_PATH}` 变体）注册 `/proto-pipeline`、`ProtoSourcesRouteWrapper`（`?view=governance` → `/proto-pipeline`）与升级后 `SchemasRouteWrapper`（全量 `tab` 无损重定向，保留 Query 与 `#L<n>` hash）。

2. **消费端三视图聚合 (`/proto-docs`) 与纯净源码库 (`/proto-sources`)**：
   - 重构 `ProtoDocsWorkbenchPage.tsx` 为 `reference | catalog | graph` 三视图：将方法目录与五态雷达融合至 `catalog`（顶部全部 + 五态胶囊筛选、行内展开模块认领与方法查看），合入 `ProtoDependenciesGraphPane` 至 `graph`（支持点击节点直达文档或流水线对应仓位），页面内将旧 `docs_view=catalog-ops|unified-catalog` 归一化为 `catalog`。
   - 精简 `ProtoSourcesWorkbenchPage.tsx` 与 `GitSourcesWorkbenchPane.tsx`：移除 `?view=governance`、冲突仲裁抽屉与冗余运维按钮，单仓头部新增「前往流水线治理此仓 ↗」。
   - 更新 `DashboardWorkbenchPage.tsx` 等全站硬编码旧链接。

3. **文档收口、全链路 E2E 与最终门禁 (`docs/`, Vitest, Go E2E)**：
   - 新增/更新 `MenuAndLegacyRoutes.test.tsx` 与 `LivingProtoDocsPane.test.tsx` 覆盖嵌入/独立全量旧深链重定向；
   - 跑通本地多仓沙箱 E2E 集成测试（一键同步发布 → 接口参考文档/目录/依赖图可见 → 源码行号定位）；
   - 将 ADR-29 标为 `Implemented`，更新路线图与 `docs/README.md`，通过 `TEST_DB_DSN=... make test`、`go test -race ./...`、`make lint`、`make build && make clean`。
