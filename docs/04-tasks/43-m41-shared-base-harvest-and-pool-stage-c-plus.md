# M41 TDD 作战卡：R4 共享基座入池与全池 Stage C+ 协调 (`ADR-28`)

- **关联里程碑**：M41 (`F-48`, `SC-29`)

## 任务拆分

1. **R4 双通道共享基座收割与内存索引 (`internal/server/gitclient/base_harvester.go`)**：
   - 定义系统共享基座仓常量（`SystemSharedBasePlatformRepoID`），在 `AlignPlatformRepositories` 中显式豁免以防止被平台同步停用；
   - 实现通道 A（`shared_base_sources` 声明目录正规 `BASE_LIB` 采集，**100% 免疫 R0 `exclude_subsystems`/`exclude_globs`**）；
   - 实现单次目录遍历构建 `SharedBaseMemoryIndex`（文件索引 + 符号索引，严禁 `os/exec`），支持按缺失 `import` 或唯一基座符号按需递归收割（标记 `is_harvested_base=true`、`Category=BASE_LIB`，并完整走 Stage B v2 与去 `/pg` 归一）。
2. **Stage C+ 统一坐标系全池协调器 (`internal/server/gitclient/pool_reconciler.go`)**：
   - 在统一去 `/pg` 坐标空间下实现 **R2 全池投票逻辑路径映射**（`显式规则 > 最高票[平票字典序] > 归一化启发式`）与裸文件名 `import` 展开；
   - 实现 **R1 / R3 / R4 协同迭代闭包 BFS**（以 `is_uservice_source=true` 为种子标记 `in_service_closure`，非闭包 `*TC.proto` 保留库中供源码查阅）；
   - 实现 **7 级确定性 R3 自动消歧**（`MANUAL > BASE_LIB > 目录逻辑前缀匹配 > 自由目录认领 > 同子系统 > 同仓谱系 > 大仓文件数 > 字典序`），标记非胜出副本 `is_external_copy=true`，在协调报告 `conflicts` 中附带 `content_sha256`、`content_identical` 与逐候选 `symbol_diff`；
   - 执行全池剥离碰撞检测（`post_strip_collisions \ pre_strip_collisions == ∅`），全部 map 遍历先排序确保 100% 确定性，并将结果事务写回 DB 与 `contract_proto_pool_reconcile_runs`。
3. **TDD 验收 (`base_harvester_test.go`, `pool_reconciler_test.go`)**：
   - 验证 `**/il/**` 与 `exclude_subsystems` 绝对不误伤 R4 基座；
   - 验证 65 类非启发式目录投票准确、裸 `import` 100% 展开、7 级 R3 消歧与 `symbol_diff`、全池剥离碰撞拦截及乱序多轮确定性一致。
