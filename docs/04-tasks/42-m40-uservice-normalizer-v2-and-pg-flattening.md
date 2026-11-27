# M40 TDD 作战卡：Stage B `rule-normalizer/v2` 与 `clean/` 去 `/pg` 扁平化 (`ADR-28`)

- **关联里程碑**：M40 (`F-47`, `SC-29`)

## 任务拆分

1. **数据模型增量与迁移 (`models/git_repo.go`, `models/db.go`)**：
   - `ProtoFile` 新增 `clean_rel_path`、`is_uservice_source`、`in_service_closure`、`is_harvested_base`、`auto_fix_count`；
   - 新增表 #40 `contract_proto_pool_reconcile_runs` (`ProtoPoolReconcileRun`) 及状态 CHECK 与闭包复合索引。
2. **`proto-governance/v1` 扩展与去 `/pg` 坐标归一化 (`internal/server/gitclient/governance_config.go`)**：
   - 新增 `NORM-006`、`NORM-007` 元数据，扩展 `exclude_subsystems`、`exclude_repo_patterns`、`strip_pg_segment`、`implicit_symbol_imports`、`shared_base_sources`、`pool_reconcile` 与单仓 `disable_strip_pg_segment`；
   - 新增 `ProductionUServicePresetConfig()` 预设，升级 `ValidateGovernanceConfig` 支持 `"auto" | "proto2" | "proto3"`；
   - 实现 `StripPGPathSegment`、`NormalizeLogicalPath`、`ApplyIntraRepoPathMappingWithPG`、`MapAndValidateIntraRepoPathsWithPG`、`CheckPoolStripPGCollisions` 与 `ShouldExcludeRepoByR0`。
3. **注释感知 `uService` 转换器 (`internal/server/gitclient/uservice_converter.go`)**：
   - 基于行/词法状态机跳过 `//` 与 `/* ... */` 注释，按内容识别 `is_uservice_source`；
   - 转换 `uService` / 多行 `interface ... return ... (timeout:N)` / 无返回 `event` 型，块内 `import` 提升去重，按 `implicit_symbol_imports` 单一真值源自动注入并归一，未识别行转 `// UNCONVERTED:` + `WARNING`，全程维护 `trackedLine` 精确 `line_map`。
4. **Stage B `rule-normalizer/v2` 与流水线/API 联动 (`rule_normalizer.go`, `compiler_pipeline.go`, `proto_tree.go`, `handlers/*`)**：
   - 升级 `ruleNormalizerBaseVersion = "rule-normalizer/v2"`，强化 `NORM-003`（重复 `package` 去重）、`NORM-005`（单引号/漏分号）、`NORM-002`（`auto/proto2/proto3`）；
   - `raw/` 保持原样不变，`clean/` 默认去 `/pg`（单仓逃生舱仅限 `clean_rel_path`），Dry-Run 返回去 `/pg` 前后对比、`pool_strip_collisions` 与 `line_map`。
5. **TDD 验收 (`uservice_converter_test.go`, `pg_flatten_test.go`, `governance_normalizer_test.go`, `proto_governance_handler_test.go`)**：
   - 309 样本 `uService` 语法门禁 100% 通过、`StripPGPathSegment` 边界与双层碰撞拦截、`line_map` 精确映射与 DB 隔离迁移全绿。
