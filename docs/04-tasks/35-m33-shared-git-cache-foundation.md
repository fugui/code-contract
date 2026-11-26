# M33 TDD 作战卡：共享宿主机 Git 缓存底座统一 (`ADR-26 Phase 1`)

- **关联里程碑**：M33 (`F-40`, `SC-23`)
- **涉及仓库**：`code-common/backend/gitsync`、`code-shield`、`code-contract`

## 任务拆分

1. **`code-common/backend/gitsync` 两层锁与路径/防降级/对象直读**：
   - `lock.go`：`GetPathLock` 升级为 `*sync.RWMutex`；新增 `AcquireRepoLock(ctx, codesRoot, localPath, mode, opts...)`，集中锁文件 `<codesRoot>/.locks/<sha1(absPath)>.lock`（0775/0664），先进程内 RWMutex 后独立 FD `flock`（`LOCK_NB` 退避轮询、超时、写锁告警），释放严格逆序。
   - `path.go`：下沉 `ResolveRepoLocalPath(codesRoot, repoURL)`（逐字节兼容 `code-shield` + SCP `git@host:path` + 路径穿越守卫）、`NormalizeGitURL`、`VerifyOriginURLConsistency`。
   - `sync.go` & `diff.go`：`IsShallowRepository`、`ResolveBranchDefault`、`CleanStaleGitLocks`/`AutoHealCache` 必须持 `LOCK_EX`；非 shallow 仓一律忽略 `--depth`（含 `FetchCommitMetadata`）。
   - `objects.go`：`FetchBranchToRef`（私有 `refs/contract/heads/*`、`gc.auto=0`、`clone --no-checkout`）、`ResolveLocalRef`、`ListTreeEntries -z`、`ReadBlobsBatch`（`cat-file --batch`）、`ReadCommitMeta`。
2. **`code-shield` 接入**：
   - `services/runner/git_sync.go` & `services/services.go` 接入 `*sync.RWMutex`、`ResolveRepoLocalPath` 与 `CodesRoot`。
3. **`code-contract` 配置扩展**：
   - `config/config.go` & `config.yaml.example` 新增 `storage.*`（`shared_write_enabled=false`）与 `proto_collect.*`。

## 验收门禁
- `code-common/backend/gitsync` 单元测试 + 多进程（`>= 1000` 轮 `Sync` 与 `FetchBranchToRef`/`LOCK_SH`）压测全绿；
- `code-shield` `ResolveRepoLocalPath` 回归一致性全绿。
