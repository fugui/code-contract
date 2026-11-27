# M38 TDD 作战卡：方案 A 导出目录治理与 Stage B 清理规则框架 (`ADR-26 r4 / ADR-27`)

- **关联里程碑**：M38 (`F-45`, `SC-28`)

## 任务拆分

1. **辅助 `.json` 治理配置与方案 A 路径映射 (`internal/server/gitclient/governance_config.go`)**：
   - 定义 `proto-governance/v1` 文档契约与持久化加载/保存（`contract_proto_governance_configs`）；
   - 实现 `(effective_subsystem, effective_repo_dir)` 全局唯一性校验（碰撞返回 `*ExportDirConflictError` 对应 `409 Conflict`）；
   - 实现仓内 `strip_prefixes` / `path_rewrites` 裁剪重写与仓内路径碰撞检测。
2. **Stage B 三层清理引擎 (`internal/server/gitclient/rule_normalizer.go`)**：
   - 实现 `RuleBasedNormalizer`（`NORM-001`~`NORM-005` 全局内置规则 + 仓级 `disabled_rules`/`regex_rewrites`/`import_rewrites` + 文件级 `exempt_files`/`quarantined_files`）；
   - 精准维护 `line_map`（未改行数保持 `LinePreserving = true`）并保证确定性逐字节输出。
3. **流水线、目录树对账与 MR 门禁联动 (`compiler_pipeline.go`, `proto_tree.go`, `mr_runner.go`, `analyzer.go`)**：
   - `raw/` 保持原始子系统与路径不变；`clean/` 按方案 A 目标目录与裁剪后路径写入并原子切换/清理旧链；
   - 停用/无 Proto 仓库自动清理 `protos/clean/` 软链；MR 门禁复用同一 `RuleBasedNormalizer` 与治理配置。
4. **治理与 Dry-Run API (`handlers/proto_governance_handler.go`, `handlers/routes.go`) & TDD 验证**：
   - 交付 6 个治理与 Dry-Run 接口，接入 `commonAudit` 与 Admin RBAC；
   - 编写规则、`line_map`、409 全局碰撞、仓内碰撞及 Dry-Run 免网络重出树闭环单测。
