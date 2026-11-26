# M36 TDD 作战卡：冲突仲裁、影响面联动、前端双视图与下线 `memfs` (`ADR-26 Phase 4`)

- **关联里程碑**：M36 (`F-42`, `F-43`, `SC-26`)

## 任务拆分

1. **控制面 API 与路由 (`handlers/proto_pipeline_handler.go`, `git_repo_handler.go`, `proto_file_handler.go`, `routes.go`)**：
   - 平台仓发现、批量导入、存量关联、基线报告、架构归属推荐、批量同步、仅重跑清理；
   - 导入归属 CRUD、跨仓冲突列表与权威指认（接入 `commonAudit` 字段级 Diff 审计）；
   - 双视图树 `GET /api/proto-files/tree`、`raw_content`/`line_map` 扩展与新鲜度标记；
   - `POST /api/webhooks/git-sync/:repoId` Webhook 触发同步；
   - 单仓变更后计算文件级反向可达影响面，局部重编受影响 Bundle 并触发 `F-26` 破坏性告警。
2. **下线 `go-git memfs` 路径**：
   - 删除 `internal/server/gitclient/memfs_sync.go` 与 `sync_service.go` 中 `memfs` 分支；`EXTERNAL_STANDALONE` 统一走 `<codes_dir>/_external/<git_repo_id>/` + `gitsync` 两层锁直读；移除 `go.mod` 中 `go-git`/`go-billy` 并通过 `cmd/agent/deps_test.go`。
3. **前端双视图、导入向导与冲突仲裁 (`frontend/src/`)**：
   - `PlatformRepoImportDrawer.tsx`（含基线报告查看与 Bundle 合并/拆分）；
   - `ProtoConflictArbitrationDrawer.tsx`；
   - 升级 `GitSourcesWorkbenchPane.tsx` & `ProtoReadOnlyCodeViewer.tsx`（双视图切换、状态/外部副本/缓存陈旧/`LOCAL_CACHE_ONLY` 标记、raw/clean 对比与 `line_map` 跳转）；
   - 补充 Vitest 单测并全量通过 `make test && make lint && make build && make clean`。
