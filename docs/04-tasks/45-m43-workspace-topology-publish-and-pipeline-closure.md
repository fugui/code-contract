# M43 TDD 作战卡：阶段 ④ 单仓拓扑发布引擎、同源解析与一键流水线闭环 (`ADR-29 r4`)

- **关联里程碑**：M43 (`F-50`, `SC-31`)

## 任务拆分

1. **配置、数据模型增量与幂等迁移 (`config/config.go`, `config.yaml.example`, `models/proto_schema.go`, `models/git_repo.go`, `models/db.go`)**：
   - 新增 `proto_pipeline` 配置节（`publish_mode = "AUTO" | "PREVIEW"`、`compile_concurrency = 4`、`fanout_warn_threshold = 100`、`mr_merge_debounce_sec = 300`、`archive_reminder_days = 30`）及治理规则 `pool_reconcile.block_on_unresolved_conflict`（默认 `false`）。
   - `ProtoSchema` 新增 `compile_input_sha256`、`publish_run_id` 与 `ProtoSourceGitPipeline = "GIT_PIPELINE"`；`GitProtoBundle` 新增 `last_publish_status`、`last_publish_run_id`、`pending_breaking_schema_id`；`ProtoImportOwner` 扩展 `R3_AUTO`；`ProtoPoolReconcileRun` 新增 `input_fingerprint`；新增实体 #41 `ProtoPublishRun`（`contract_proto_publish_runs`）与 #42 `ProtoPipelineRun`（`contract_proto_pipeline_runs`）。
   - `ensureADR26SchemaAndBackfill` 幂等重建 `chk_contract_proto_source_type` 与 `chk_contract_import_owner_source`，并补全新增表及列约束。

2. **R3 同源解析、输入快照与全局流水线锁 (`internal/server/gitclient/pool_reconciler.go`, `pool_r7_r8.go`, `pipeline_lock.go`)**：
   - `selectR3Winner` 将 `MANUAL` 设为 tier 0 最高优先级，`variant_swap` 禁止覆盖 `MANUAL` 仲裁。
   - `persistPoolReconcileOutput` 同事务 upsert 多候选 R3 胜出方为 `R3_AUTO`（不覆盖 `MANUAL`/`BASE_LIB`）并清理过期 `R3_AUTO`；计算并持久化 `ProtoPoolReconcileRun.InputFingerprint`。
   - 新建 `pipeline_lock.go`，基于独占物理 `*sql.Conn` 实现 PostgreSQL 会话级 advisory lock（`hashtext('contract_proto_pipeline')`），统一串行化全量同步、重洗、全池收敛、阶段 ④ 发布、单仓同步落库与 MR 合入落库。

3. **阶段 ④ 单仓拓扑发布引擎与一键流水线编排 (`workspace_publisher.go`, `pipeline_orchestrator.go`, `pipeline_debouncer.go`, `archive_reminder.go`)**：
   - 实现 `PublishWorkspaceBundles`：校验 `input_fingerprint`（`RECONCILE_STALE`）、按 `ComputeBundleSCCs` 拓扑层序推进（`BASE_LIB` 先行，SCC 同进同退）、`compile_input_sha256` 增量跳过、破坏性局部隔离（`BLOCKED_BREAKING`）与传递下游传播（`PENDING_UPSTREAM`）、`publish_mode = AUTO | PREVIEW`、`block_on_unresolved_conflict` 严格开关、有界并发编译、层内原子激活 + `protodoc.RebuildForSchema` + 单次 `Registry.Reload()` + 异步目录撮合、以及被 Git 全覆盖的归档外部 Schema 自动停用。
   - 实现 `ApproveBreakingSchemas`（`P1` 审计放行并自动为 `PENDING_UPSTREAM` 下游补发增量发布）与 `ConfirmPreviewPublishRun`。
   - 实现 `RunFullProtoPipeline` 与 `BuildPipelineOverview`（5 条 Stale 漂移判定规则）及 30 天历史归档站内信提醒巡检。

4. **激活入口收口、自封闭路由下线与新控制面 API (`platform_sync_job.go`, `compiler_pipeline.go`, `mr_runner.go`, `decoder/registry.go`, `handlers/proto_handler.go`, `handlers/proto_pipeline_v2_handler.go`)**：
   - `sync-all` 默认 `skip_compile=true`；单仓同步与 Webhook 强制不激活；MR `AUTO_ACTIVATE` 合入落库后入队 `from_stage=RECONCILE` 去抖（默认 300s）增量流水线，并更新 MR 站内信链接为 `/proto-governance?gov_tab=mr-gates&runId=%d`。
   - 下线 `POST /api/proto-schemas` 与 `POST /api/proto-schemas/compile`（返回 `404`）；`ActivateSchemaVersion` / `BatchActivate` 拦截历史外部来源返回 `422 SCHEMA_SOURCE_ARCHIVED`；完成 5 个测试文件夹具迁移。
   - 交付 `/api/proto-pipeline/*` 全部接口与 SSE 事件 `proto_pipeline.progress` / `proto_publish.progress`。

5. **全量 TDD 验证**：
   - 覆盖 `SC-31` 全量测试矩阵、`TEST_DB_DSN=... make test`、`go test -race`、`cmd/agent/deps_test.go` 与 `make build && make clean`。
