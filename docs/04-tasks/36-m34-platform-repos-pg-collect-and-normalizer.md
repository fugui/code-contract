# M34 TDD 作战卡：平台仓打通、`pg` 白名单采集与预留清理层 (`ADR-26 Phase 2`)

- **关联里程碑**：M34 (`F-41`, `F-42`, `SC-24`)

## 任务拆分

1. **模型与迁移 (`models/`)**：
   - `Repository` 公共模型别名；扩展 `GitRepo`、`ProtoFile`、`ProtoFileVersion`；新增表 #37 `ProtoImportOwner`（`contract_proto_import_owners`）；扩展 `MRCheckRun.NormalizeFailed`。
   - `ensureADR26SchemaAndBackfill` 幂等建约束/索引并回填存量 `contract_proto_files`（`raw_blob_sha`、`passthrough-0`、`NORMALIZED`）。
2. **Stage A 白名单采集器 (`internal/server/gitclient/collector.go`)**：
   - 严格全小写 `pg/` 与 `*/pg/` 白名单 + 二级 `exclude_globs`；
   - 前缀 `ls-tree -z` 列举、过滤软链(`120000`)/子模块(`160000`)、`max_file_bytes` / `max_files_per_repo` 守卫、LFS 指针隔离、按 `raw_blob_sha` 增量 `cat-file --batch`；
   - `PLATFORM_SHARED` 在 `shared_write_enabled=false` 退化 `LOCAL_CACHE_ONLY`；`EXTERNAL_STANDALONE` 落盘 `<codes_dir>/_external/<git_repo_id>/` + 临时凭据注入。
3. **Stage B 预留清理层 (`internal/server/gitclient/normalizer.go`)**：
   - 定义 `Normalizer` 接口、`NormalizeInput`（预留辅助 `.json` 映射配置）、`PassthroughNormalizer`（`passthrough-0`）；
   - `protocompile/parser` 仅解析不链接后置语法门禁（失败标 `QUARANTINED`，独立隔离不污染 Tier-2）；增量键 `(raw_blob_sha, normalizer_version)`。
4. **只读聚合目录树 (`internal/server/gitclient/proto_tree.go`) & 平台仓发现/迁移/基线 (`platform_discovery.go`)**：
   - `NormalizeSlug`、`.versions/raw` 版本目录与原子软链切换、每仓保留 2 版本；
   - 平台仓批量发现、候选 Bundle（每 `pg` 目录 1 个）批量导入、存量关联平台仓、基线报告生成。
