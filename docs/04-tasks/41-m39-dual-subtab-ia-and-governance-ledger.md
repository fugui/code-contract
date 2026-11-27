# M39 TDD 作战卡：双子标签 IA、树懒加载与 500+ 仓前端治理台账 (`ADR-26 r4 / ADR-27`)

- **关联里程碑**：M39 (`F-46`, `SC-28`)

## 任务拆分

1. **后端树懒加载接口扩展 (`handlers/proto_pipeline_handler.go`)**：
   - 升级 `GET /api/proto-files/tree` 支持 `summary_only=true`、`expand_repo_id`、`expand_package`、`subsystem`，默认排除 `sync_enabled=false` 仓库。
2. **前端类型与 API 清理演进 (`frontend/src/types/schema.ts`, `frontend/src/api/schema.ts`)**：
   - 删除 `createGitRepo`、`deleteGitRepo`、`discoverPlatformRepos`、`importPlatformRepos`、`linkGitRepoPlatform` 等废弃接口与类型；
   - 新增同步控制台、同步任务、单仓/全局治理与 Dry-Run 预览类型和请求方法。
3. **双子标签工作台与治理抽屉重构 (`frontend/src/pages/ProtoSchemaWorkbenchPage/`)**：
   - 删除 `PlatformRepoImportDrawer.tsx`；
   - 新建 `ProtoExportGovernancePane.tsx`（6 态汇总卡 + 双按钮确认 + SSE 进度条 + 分面 Tab + 标准分页 `15/25/50/100`）；
   - 新建 `GlobalGovernanceConfigDrawer.tsx`（`NORM-001`~`NORM-005` + `proto-governance/v1` `.json` 导入导出与全局 Dry-Run + 基线报告）；
   - 重构 `GitRepoConfigDrawer.tsx` 为单仓契约治理抽屉（平台真值只读 + 目录覆写/路径裁剪/规则覆写 + Dry-Run 对比预览）；
   - 重构 `GitSourcesWorkbenchPane.tsx` 支持 `?view=sources|governance` 双子标签与树懒加载，彻底移除独立建仓/逐仓导入/关联按钮。
4. **Vitest、Lint、Overlay Guard 与单二进制构建验收**：
   - 补充/更新双子标签、治理台账、单仓/全局治理抽屉 Vitest 测试，确保 `make test`、`make lint`、`npm test`、`make build && make clean` 全绿。
