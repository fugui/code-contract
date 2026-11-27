# M44 TDD 作战卡：`/proto-pipeline` 四阶段可视化流水线工作台与完全自封闭改造 (`ADR-29 r4`)

- **关联里程碑**：M44 (`F-51`, `SC-32`)

## 任务拆分

1. **前端强类型 DTO 与 API 客户端 (`frontend/src/types/schema.ts`, `frontend/src/api/schema.ts`)**：
   - 移除 `CompileProtoSchemaPayload`、`uploadProtoSchema`、`compileProtoSchema`。
   - 新增 `ProtoPipelineOverviewDTO`、`ProtoPipelineRunDTO`、`ProtoPublishRunDTO`、`PublishBundleItemDTO`、`ArchivedExternalSchemaDTO` 等强类型（零 `any`）与 `/api/proto-pipeline/*` API 方法；`GlobalGovernanceConfig` 补齐 `block_on_unresolved_conflict`。

2. **彻底下线外部上传与在线直编入口 (`SchemaUploadModal.tsx`, `ProtoSchemaWorkbenchPage.tsx`, `index.ts`)**：
   - 删除 `SchemaUploadModal.tsx` 及全站所有「上传描述集 (`.pb`)」「在线源码直编 (`.proto`)」「上传新版本」按钮，仅保留只读详情、同源 Git 历史回滚、停用与 `.pb` 下载。

3. **四阶段可视化流水线工作台 (`frontend/src/pages/ProtoPipelineWorkbenchPage/`)**：
   - `ProtoPipelineWorkbenchPage.tsx` & `.css`：支持 `?stage=collect|normalize|reconcile|publish`，接入 SSE `proto_pipeline.progress` / `proto_publish.progress` / `proto_sync_job.progress` / `proto_pool_reconcile.progress`。
   - `PipelineStepperHeader.tsx`：主按钮「一键同步并发布（仅读缓存）」、规则入口、实时进度条、运行锁态禁用与 4 阶段 Stale 漂移状态卡。
   - `StageCollectPane.tsx`（阶段 ①）：6 态业务胶囊筛选、`扫描本地缓存`、`补拉缺失缓存`（`useConfirm` 二次确认）、`重试失败仓` 与极简分页台账。
   - `StageNormalizePane.tsx`（阶段 ②）：规则摘要、复用 `GlobalGovernanceConfigDrawer`（含 `block_on_unresolved_conflict` 开关）、异常置顶排序与单仓操作。
   - `StageReconcilePane.tsx`（阶段 ③）：内联 4 视图（同名冲突 `symbol_diff` 与权威指定合一、R7 补丁固化、R8 根因源码抽屉、R4 基座清单）。
   - `StagePublishPane.tsx`（阶段 ④）：大扇出耗时提示、自动裁决风险提示、`PREVIEW` 确认生效、破坏性待审 P1 放行面板、等待上游清单、生效台账与历史归档对账清单。

4. **业务化文案（去黑话）与 TDD 验证**：
   - 按 ADR-29 §2.4.2 逐项替换为业务语言；新增 `ProtoPipelineWorkbenchPage.test.tsx`，通过 `make lint`、`TEST_DB_DSN=... make test`、`make build && make clean`。
