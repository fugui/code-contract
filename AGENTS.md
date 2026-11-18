# AGENTS.md - 架构与活体契约治理平台 (Code-Contract)

> 💡 **上位准则**：工作区通用 SDD+TDD 四阶段研发流水线、`docs/` 双位编号规范、Go/React 编码风格、双模主题 Token、通用分页及自动交付规章，统一遵循 [`../AGENTS.md`](../AGENTS.md)（`code-common/rules/AGENTS.md`），本文档仅定义本模块专属契约与边界。

---

## 1. 模块专属契约 (Module Contract)

| 契约项 | 规范值 | 强约束说明 |
| :--- | :--- | :--- |
| **模块标识 (`ModuleKey`)** | `contract` | 门户路由前缀 `/contract`，Vite Federation 名称 `name: 'contract'` |
| **服务与二进制名** | `Code-Contract` / `code-contract-server` + `code-contract-agent` | Go Module: `code-contract`；同仓双入口 `cmd/server`、`cmd/agent` |
| **端口分配** | 后端 `:8091` / 前端 `5181` | 验证完毕后必须主动关闭进程释放端口 |
| **数据库表前缀 (`TablePrefix`)** | `contract_` | 本模块所有 GORM 模型 `TableName()` 必须以 `contract_` 开头 |
| **管理员角色 (`AdminRole`)** | `contract_admin` | 管理接口必须挂载 `commonAuth.RequireAdmin("contract_admin")` |
| **模块配置模型** | `ModuleSetting` | 对应 `contract_settings` 表，使用中性命名以区分业务领域契约 |

### 1.1 术语边界约定（平台级标识 vs 业务领域模型）

为避免工程级 `contract` 标识与微服务“多实现服务契约”领域概念混淆，本仓严格执行以下术语边界：

| 层级 | 标准术语 / 符号 | 语义与使用范围 |
| :--- | :--- | :--- |
| **平台级模块标识** | `code-contract` / `ModuleKey="contract"` / `contract_*` 表前缀 / `CONTRACT_*` 环境变量 / `ModuleSetting` | 指代本微服务工程自身、数据库表名隔离前缀、微前端路由前缀、CI/Agent 请求头及模块级配置实体 |
| **业务领域实体** | `ServiceContract`（表 `contract_service_contracts`）、外键 `contract_id`、`contract_service_name`、`CONTRACT_LIB`、`contract_resolver.go` | 指代模块间 `MULTI_IMPL` 多实现服务接口契约模型及运行期服务名解析逻辑（`ADR-16`） |

---

## 2. 目录结构与业务边界

```text
code-contract/
├── docs/                # SDD + TDD 规范文档库 (00-prompt ~ 07-references)
├── cmd/server/          # 主控控制面入口 code-contract-server
├── cmd/agent/           # 数据面 Agent 入口 code-contract-agent（零数据库、零 .pb / protoregistry、零 CGO 依赖）
├── pkg/crosrpc/         # CROS RPC 分帧与头解析库（主控、Agent 与 rpcmon 共用）
├── pkg/csgdlink/        # CSG/dlink 控制面 32B 帧栈、只读发现与 Observer（纯 Go 标准库）
├── pkg/rpcmon/          # RPCMonitorService 统一监控模型、CrosTCP 传输（pkg/crosrpc + 手写 Wire）与探针适配
├── pkg/csgmock/         # 内存态 MockCSGServer 控制面沙箱（实现 rpcmon.Provider，禁依 pkg/ebpf）
├── pkg/ebpf/            # Agent 嵌入式 cilium/ebpf 采集引擎
├── pkg/replay/          # Agent 二进制 Mock 匹配与 ullSNO 自愈
├── internal/agent/csg/  # Agent 侧 CSG 双层自动发现融合器与三模仿真控制器
├── scripts/csg/         # 真机只读 C++ 探针源码 rpcmon_probe.cpp 与独立编译脚本 build-native-probe.sh
├── config/              # 配置加载与模块常量定义
├── models/              # GORM 数据模型（强制 contract_ 表前缀隔离，30 核心表 + contract_settings）
├── handlers/            # HTTP 控制器、RBAC 守卫、审计 Diff 与 *_test.go 隔离单测
└── frontend/            # React 18 微前端工程（暴露 ./App 与 ./menu）
```

* **本模块专有架构红线（阶段二已冻结，详见 [`docs/02-design/`](docs/02-design/)）**：
  1. **主从物理防腐（`cmd/agent/deps_test.go`）**：`cmd/agent` 及 `pkg/{crosrpc,agentproto,csgdlink,rpcmon,csgmock,ebpf,replay}` 严禁引入 `gorm`、`pgx`、数据库连接、`dynamicpb`、`protoregistry`、`protocompile` 或 `go-git`（`pkg/rpcmon` 采用手写纯 Go Wire 编解码，严禁依赖 `google.golang.org/protobuf`）；真机 `rpcmon_probe` 仅允许以受控白名单路径+合法 `ipv4:<ip>:<port>` 参数调用只读外部二进制（严禁 CGO 与拼接 Shell 命令）；主控 `cmd/server` 以非特权运行，严禁直接挂载 eBPF 或跨网发重放包。
  2. **协议与契约唯一性**：仅处理 IPv4 TCP 明文 DOPHI `CROS RPC` 数据面报文（`dophi_mesh://` 仅支持控制面发现并在 `MOCK_SERVER` 快照改写前缀为 `tcp://`，纯 Mesh 仿真显式返回 `422 CSG_DATAPLANE_MESH_UNSUPPORTED`）；`ModuleService.service_name` 以线上 `ascServiceName` / CSG 64B `acServiceName` 为准（支持 `{package}.{serviceName}` 与 `<ClassName[:31]>-<svrName[:31]>` 含 `-` 格式，可选 `proto_service_name` 注入 `ServiceAliases` 并支持 `^(.+-\d+)-(\d+)$` PID 归一化）；独占契约（`EXCLUSIVE`）`PROVIDE` 全局唯一，多实现契约（`MULTI_IMPL`，由 `CONTRACT_LIB` 接口库模块定义）按 `(contract_id, impl_key)` 唯一且共享契约名实现必须两两分属不同 `ArchProcess`（[`ADR-16`](docs/05-adr/16-multi-implementation-service-contract-adr.md)）。
  3. **公私域隔离与 Agent 动态排他租约**：三级架构元素与 `.pb` 为全局唯一公共 SSOT（现网 CSG 导入 `POST /api/agents/:id/csg/import-blueprint` 严格限 `contract_admin`）；`Agent` 与 `RecordingSession` 严格区分 `SHARED`（团队共享）与 `PRIVATE`（仅 `owner_id` 本人可见与操作，横向越权返 `403`）；同一台 `Agent` 同一时刻仅允许 1 名用户持有 1 项录制或重放排他租约（并发争抢返 `409 Conflict`）；`contract_traffic_records` 强制采用单次往返一行双模持久化（`raw_req_packet` + `raw_rsp_packet` `bytea` 与 `payload_json` `jsonb`）。
  4. **重放零外泄、真实 CSG 零污染与探针卫生**：重放 `MOCK_MISS` 严禁转发至真实下游；**严禁在任何模式下向真实 CSG 发送 `GET_BIND_ADDR (7)` / `FREE_BIND_ADDR (8)` 推进生产端口游标**，`PUB_STUB` 必须通过前置 `Sub==0` 门禁（在线冲突返 `409 CSG_SERVICE_ALREADY_PUBLISHED`）；在正常完成、取消、`/purge`、主控失联满 5 分钟或收到退出信号五个出口，Agent 必须幂等卸载全部 eBPF 探针与监听端口、`Unpub` 全部 `PUB_STUB` 桩并关闭 `MockCSGServer`。
  5. **架构拓扑单层平铺与布局校准（[`ADR-17`](docs/05-adr/17-topology-single-layer-drilldown-and-layout-ux-adr.md)）**：架构拓扑严格遵循「单层平铺逐级下钻（无外层包裹框、仅展示直属子节点 + 左右 `FIRST/LAST` 外部传送门卡）」、CSS `border-box` 与 ELK 节点宽高 `1:1` 对齐、四向智能锚点路由，以及按 `(view, focus_scope)` 隔离且仅用户显式拖拽/图钉固定才锁位的位置记忆策略。

---

## 3. 常用验证命令

```bash
make test                                                                                      # 后端 fmt + vet + 单元测试
TEST_DB_DSN="host=127.0.0.1 port=5432 user=postgres password=PWD dbname=postgres sslmode=disable" make test  # 物理隔离库全量单测
make lint                                                                                      # 前端 tsc + eslint 检查
make build && make clean                                                                       # 单二进制打包验证并清理产物
```
