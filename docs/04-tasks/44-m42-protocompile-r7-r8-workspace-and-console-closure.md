# M42 TDD 作战卡：纯 Go `protocompile` R7 自愈、R8 隔离、`workspace/` 导出与治理台闭环 (`ADR-28`)

- **关联里程碑**：M42 (`F-49`, `SC-30`)

## 任务拆分

1. **纯 Go `protocompile` R7 编译自愈与 R8 根因隔离 (`internal/server/gitclient/pool_r7_r8.go`)**：
   - 构造全池符号倒排索引，使用 `bufbuild/protocompile` + `reporter.Handler` 驱动 4 类修复：`fix(a)` 错路径重写、`fix(b)` 同目录兄弟优先补录 + 唯一基座符号触发 R4 通道 B、`fix(c)` 坏依赖替代切换（含已试集合防震荡）与 `variant swap`；
   - 严格保证四类修复中任一产生有效改写即判定有进展（防漏 `fix(c)` 提前落入 R8），同步更新 `content_text`/`line_map_json`/`auto_fix_count`/`AUTO_FIX_*` 诊断；
   - R8 赋予根因错误（`already defined`/`multiple package`/语法根错）显著高于受害者错误（`unknown`/`not defined`）的权重，按 `(score DESC, errCount DESC, path ASC)` 隔离直至 0 error 或达 `max_r8_rounds`。
2. **三视图 `workspace/` 原子落盘与控制面 API (`proto_tree.go`, `handlers/proto_pool_reconcile_handler.go`, `handlers/sse_handler.go`, `handlers/routes.go`)**：
   - `ProtoTreeManager` 新增 `TreeKindWorkspace` 与 `WriteAndSwitchWorkspace` 原子切换 `<protos_dir>/workspace/` 与 `.clean-report.json`；
   - 实现 `POST /api/proto-governance/reconcile-pool`（`Admin` + SSE `proto_pool_reconcile.progress`）、`GET /api/proto-governance/reconcile-runs/{latest,:id}`、`POST /api/proto-governance/auto-fixes/promote`（`Admin` + `P1` 审计）；增强 `GET /api/proto-files*` 的 `in_service_closure`/`is_uservice_source`/`is_harvested_base` 筛选与返回。
3. **前端 `/contract/proto-sources` 闭环 (`frontend/src/pages/ProtoSchemaWorkbenchPage/*`)**：
   - `GlobalGovernanceConfigDrawer` 增加 `NORM-006/007`、`default_syntax (auto/proto2/proto3)`、`strip_pg_segment`、`exclude_subsystems`、`implicit_symbol_imports`、`shared_base_sources` 与「载入现网 uService + 去 /pg 治理预设」按钮；
   - 新建 `PoolReconcileReportDrawer.tsx`，在 `ProtoExportGovernancePane` 接入「全池编译收敛与工作区导出」、SSE 进度条与报告抽屉（`conflicts` 含 `symbol_diff`、`auto_fixes` 勾选固化、`quarantined`、`harvested`）；
   - `GitSourcesWorkbenchPane` 与 `ProtoReadOnlyCodeViewer` 增加「仅看服务闭包」筛选开关与 `uService 已转换`/`R7 自动修复`/`R4 收割基座` 徽标及去 `/pg` 对照。
4. **全量验收与回归 (`pool_r7_r8_test.go`, `proto_pool_reconcile_handler_test.go`, `ADR28ReconcileAndUService.test.tsx`)**：
   - 完成高仿真基准回归与可选 `ADR28_CORPUS_DIR` 现网快照回归；
   - 执行 `TEST_DB_DSN=... make test`、`go test -race`、`make lint`、`make build && make clean` 全绿交付。
