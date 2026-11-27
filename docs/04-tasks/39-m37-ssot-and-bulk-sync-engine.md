# M37 TDD 作战卡：唯一真值源收口与 500+ 全量双按钮异步同步 (`ADR-26 r4 / ADR-27`)

- **关联里程碑**：M37 (`F-44`, `SC-27`)

## 任务拆分

1. **数据模型与存量归并迁移 (`models/git_repo.go`, `models/db.go`)**：
   - 移除 `EXTERNAL_STANDALONE`，收口 `GitRepo.PlatformRepoID uint64 NOT NULL UNIQUE`（`source_mode = PLATFORM_SHARED`），新增 6 态 `proto_status`、`sync_enabled` 及方案 A 治理字段；
   - 新增模型 #38 `ProtoSyncJob`（`contract_proto_sync_jobs`）与 #39 `ProtoGovernanceConfig`（`contract_proto_governance_configs`）；
   - `ensureADR26SchemaAndBackfill` 自动按 `NormalizeGitURL` 归并存量手工仓并清理凭据、孤儿仓摘要写入 `contract_mig_backups` 后安全删除。
2. **Collector 毫秒快筛与无工作树缓存收口 (`internal/server/gitclient/collector.go`, `sync_service.go`, `platform_discovery.go`)**：
   - 删除 `_external` 与 `buildExternalGitAuthEnv`，统一走 `gitsync.ResolveRepoLocalPath`；
   - `CACHE_ONLY` 缺失 `.git` 立即返回 `ErrLocalCacheMissing`（零网络命令）；
   - `ProbeRepoPGStructure` 与全量快筛统一通过 `derivePGPrefixes` 仅查顶层与一级目录，禁止全仓 `ls-tree -r`。
3. **全量自动对齐与持久化批处理同步 (`internal/server/gitclient/platform_sync_job.go`, `compiler_pipeline.go`, `scheduler.go`)**：
   - 实现 `AlignPlatformRepositories`、`RunSyncAllJob`、`GetSyncSummary`；
   - 无 `pg/` 或 0 文件仓标 `NO_PROTO`（不建空 Bundle、不建空目录），缺失缓存标 `NO_LOCAL_CACHE`；
   - 取消 10 分钟硬超时，持久化计数并推送 SSE `proto_sync_job.progress`。
4. **控制面路由下线与新同步控制台 API (`handlers/routes.go`, `git_repo_handler.go`, `proto_pipeline_handler.go`, `sse_handler.go`)**：
   - 下线 `POST /api/git-repos`、`DELETE /api/git-repos/:id`、`POST /api/git-repos/:id/link-platform`、`GET /api/git-repos/platform-discover`、`POST /api/git-repos/platform-import`；
   - 新增 `POST /api/git-repos/sync-all`、`GET /api/git-repos/sync-summary`、`GET /api/git-repos/sync-jobs/:jobId`；升级 `GET /api/git-repos` 关联平台元数据与 6 态筛选；
   - 适配既有单测并通过 500 仓压测与下线路由 `404` TDD 验证。
