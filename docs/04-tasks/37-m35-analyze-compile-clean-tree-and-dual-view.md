# M35 TDD 作战卡：Stage C/D 解析编译、`clean` 树、调度与 MR 统一 Normalizer (`ADR-26 Phase 3`)

- **关联里程碑**：M35 (`F-41`~`F-43`, `SC-25`)

## 任务拆分

1. **Stage C 解析器 (`internal/server/gitclient/analyzer.go`)**：
   - 仅对 `NORMALIZED` 执行逻辑导入路径与 `include_dirs` 推断（Bundle 显式配置 > 所属 `pg` 根 > import 后缀反推）；
   - 导入归属登记（`MANUAL` > `BASE_LIB` > 全池唯一 `AUTO`）；
   - 符号冲突预检：全限定符号比对，同哈希非权威副本标 `is_external_copy = true`。
2. **Tier-2 升级 (`internal/server/decoder/protocompile.go`)**：
   - `DBProtoSourceProvider` 过滤 `pipeline_status='NORMALIZED' AND is_external_copy=false AND is_deleted=false`，按 `contract_proto_import_owners` 解析，多提供方无唯一权威返回 `IMPORT_AMBIGUOUS`；`MISSING_IMPORTS` 附全池近似候选与 `QUARANTINED` 提示。
3. **Stage D 分仓编译与 SCC (`internal/server/gitclient/compiler_pipeline.go`)**：
   - 四段式流水线 `RunFourStagePipeline` + 基于 `raw/` 或本地 Git 对象重跑清理 `ReNormalizeRepo`；
   - 文件级依赖图（DAG）+ Bundle 级 Tarjan SCC；`PLATFORM_SHARED` + `passthrough-0` 强制 `auto_activate=false` 试编译。
4. **Clean 树物化与对账 (`proto_tree.go`) + `SyncScheduler` (`scheduler.go`) + MR 门禁统一 Normalizer (`virtual_tree.go` / `mr_runner.go`)**：
   - `clean/` 原子软链切换与 `ProtoTreeReconciler`；
   - `SyncScheduler`（可配 worker 池=4、按 host 限流、`singleflight`、定时 cron、`LOCAL_CACHE_ONLY` ref 变化检测）；
   - MR 门禁白名单 + 同版本 Normalizer 过滤，失败呈现 `NORMALIZE_FAILED`（禁止 Override）。
