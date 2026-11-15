# 架构与活体契约治理 (`code-contract`) 联调验证与生产部署手册

- **文档编号**：`01-verification-and-deployment-guide.md`
- **文档版本**：v1.3.0
- **编制日期**：2026-10-29
- **更新日期**：2026-11-13
- **归属阶段**：阶段四：交付验证与运维指南

---

## 1. 本地开发与物理隔离测试验证

### 1.1 执行全套静态检查与单元测试
```bash
# 后端格式化、静态检查与基础单测
make test

# 携带物理隔离临时数据库的完整 CRUD 集成单测（注意：dbname 必须为 postgres 或 *_test）
TEST_DB_DSN="host=127.0.0.1 port=5432 user=postgres password=CodeShield618! dbname=postgres sslmode=disable" make test

# 前端 TypeScript 与 ESLint 检查（零 any、零警告）
make lint
```

### 1.2 独立调试模式启动
```bash
# 终端 1：编译并启动控制面后端（监听 :8091，自动承载内嵌微前端）
cp config.yaml.example config.yaml
make build && ./code-contract-server

# 终端 2（可选）：在目标被测节点上启动数据面 Agent（监听 :8092）
./code-contract-agent --endpoint http://0.0.0.0:8092 --claim-code "your-claim-code"

# 终端 3（可选，仅用于前端热更新调试）：启动 Vite 开发服务器（监听 :5181）
cd frontend && npm run dev
```
> 调试完成后请务必关闭启动的服务进程，释放 `:8091`、`:8092` 与 `:5181` 端口。

---

## 2. 挂载接入 CodeBench 宿主门户

在 `../code-bench/config.yaml` 的 `gateways:` 节点下添加本模块配置：

```yaml
gateways:
  contract:
    url: "http://127.0.0.1:8091"
    title: "架构与活体契约治理"
    icon: "Activity"
    description: "基于真实流量的活体契约、架构一致性与防劣化自愈业务子系统"
```

重启 `code-bench` 后，访问 `http://192.168.56.18:8000` 即可在工作台首页与左侧导航栏看到 **「架构与活体契约治理」** 模块。

---

## 3. 上线前必过自检清单 (Pre-Release Checklist)

- [x] `config/config.go` 与所有新增 GORM 模型的 `TableName()` 均以 `contract_` 开头；
- [x] 所有写操作接口均已接入 `commonAudit` 操作审计，管理员接口均已挂载 `RequireAdmin("contract_admin")`；
- [x] 前端所有新增页面在亮色（Light）与暗色（Dark）主题下均无视觉黑块或文字融底，无硬编码色值，无 `any` 类型；
- [x] `make test && make lint && make build && make clean` 全量零错误通过；
- [x] SC-01~SC-12 已交付场景全链路端到端回归测试 100% 绿灯（M22~M26 完成后同步纳入 `SC-13~SC-16` 与前端基于 M21 基线 `<= 150KB gzip` / `<= 500KB` 原文产物体积增量门禁）。

---

## 4. 大规模架构拓扑验收 (`SC-11`, M19 / [`ADR-15`](../05-adr/15-scalable-topology-focus-and-layout-persistence-adr.md))

> [!NOTE]
> 本节步骤依赖 M19-P7 交付的种子脚本 `scripts/seed-large-topology`，在 M19 实施完成后执行。浏览器访问统一使用物理 IP `192.168.56.18`，验收结束后必须停止全部后台进程。

### 4.1 准备大规模数据集

```bash
# 仅允许写入库名以 _dev 或 _test 结尾的数据库（脚本内置熔断，指向其他库直接拒绝执行）
go run ./scripts/seed-large-topology \
  -dsn "postgres://postgres:postgres@127.0.0.1:5432/code_contract_dev?sslmode=disable" \
  -subsystems 25 -groups 10 -modules 10 -hub-callers 12 -with-violation-session
```

数据集包含：25 个子系统 × 10 个功能组 × 10 个功能模块（约 2,775 个节点）、约 1 万条依赖边、1 个被 12 个子系统依赖的公共底座子系统、若干外部系统，以及一份含未声明跨子系统调用的录制批次一致性报告。

### 4.2 浏览器验收步骤（`http://192.168.56.18:8091`）

1. 进入「架构拓扑」：默认 `L0` 渲染，拖动层级滑杆至 `L2` 被守卫弹窗拦截；在搜索框输入单个字母不改变展开态；
2. 点击某子系统 `[🎯 聚焦]`：出现面包屑与外部邻居卡；挂载含违规调用的批次后，违规对端出现在邻居卡中；开启 `mod_*` 2 跳邻域、Hub 折叠与「仅异常」，结果符合 `F-04.1` / `F-04.2` 验收标准；
3. 拖拽 2 个子系统后展开第 3 个子系统：无全局重排、无镜头跳变、无容器重叠；
4. 管理员发布团队基准布局；普通用户另存个人书签；刷新页面后两者坐标完整恢复；
5. 管理员新增 1 个模块后重新打开拓扑页：存量节点原位，新模块带 `✨ 新增` 标记且出现琥珀色提示条；
6. 使用浏览器开发者工具记录下表性能数据。

### 4.3 性能实测记录模板（对照 ADR-15 §5 性能预算）

| 指标 | 预算 (P95) | 实测值 | 结论 |
| :--- | :--- | :--- | :--- |
| 单容器局部展开排版（子树 ≤ 120 节点） | ≤ 150 ms | 48.5 ms | ✅ 达标 |
| 全局 ELK 排版（可见 ≤ 150 节点，Worker 异步） | ≤ 400 ms | 86.2 ms | ✅ 达标 |
| 快轨直出（可见 ≤ 150 节点，免 ELK） | ≤ 100 ms | 14.6 ms | ✅ 达标 |
| `GET /api/arch/topology/graph` 传输体积（gzip 前 / 后） | 启用压缩 | 4,078,638 B (3.89 MB) / 218,878 B (213.7 KB, 压缩率 94.6%) | ✅ 达标 |

---

## 5. Proto 全生命周期与 Git/MR 治理运维与验证基线 (`SC-13`~`SC-16`, `M22~M26` / [`ADR-18`](../05-adr/18-proto-full-lifecycle-and-git-mr-governance-adr.md))

> [!NOTE]
> 本节参数与命令行指引随 M22~M26 推进分阶段启用；因 `code-proto` 与 `protohub` 均未开发完成且未实际投入运行，**系统无任何历史 SQLite 或外部旧系统数据需要迁移**；`code-contract-server` 与 `code-contract-agent` 全仓严禁引入任何 SQLite 运行时依赖。

### 5.1 关键环境变量与安全配置
1. **`CONTRACT_CRYPTO_KEY`（32 字节 Base64 / Hex AES-256-GCM 主密钥）**：用于加密持久化 Git 访问令牌、Webhook Secret、LLM API Key、SMTP 密码及 `code-notifier` 调用令牌；生产环境由 KMS/环境变量注入，严禁硬编码于 `config.yaml` 或源码中；
2. **内置标准规则与通用词根幂等初始化 (`M24`)**：
   - 无需运行任何外部迁移脚本，服务启动 `models.SetupWithDB` 时自动通过 `INSERT ... ON CONFLICT DO NOTHING` 幂等初始化默认静态规范规则（含 `dophi.order.v1` 多段式包名合规、`COMMENT_REQUIRED=WARNING` 等）及 `contract_vocabulary_terms` 通用基础词根（`source='BUILTIN_SEED'`、`is_approved=true`）；
   - 团队业务域专有标准词根与常见不规范别名（`aliases` / `description_en` / `similar_terms`）直接在上线后通过 `/schemas` 规则/词库工作台按需增删维护。
3. **跨机 / 内网外部通道与信任白名单（`M24~M26` 管理后台 `/admin/settings` 配置）**：
   - **出站信任白名单 (`outbound_trusted_cidrs`)**：`SafeDialer` 永久硬阻断云元数据段（`169.254.0.0/16`、`100.100.100.200/32`）并默认拦截回环与 RFC1918 私网；当 GitLab/CodeHub、内网 LLM 网关或 **`code-notifier` 部署在同内网其他主机** 时，管理员必须在 `/admin/settings` 显式配置受信任 CIDR/精确主机（如 `192.168.56.20/32,gitlab.internal.corp:443`）；
   - **外部 LLM 词根建议服务 (`M24`)**：通过 `llm_enabled`、`llm_base_url`、`llm_api_key`（经 `CONTRACT_CRYPTO_KEY` AES-256-GCM 密文落库且不返显原文）控制，强制 3s 超时熔断降级且受 `SafeDialer` 校验；
   - **跨机部署 **`code-notifier`** 邮件通道 (`M26`)**：`code-notifier` 运行在同内网另一台独立机器上负责实际邮件发送。在 `/admin/settings` 配置 `code_notifier_enabled=true`、`code_notifier_url`（例如 `http://<code-notifier-lan-host>:8081`，服务端请求 `POST <url>/api/notify/email` 携带必填非空 `markdown_content`、HTML 正文与解析后的真实收件邮箱，同步将该主机 IP/CIDR 加入 `outbound_trusted_cidrs`）及可选 `code_notifier_token`（同样 AES-256-GCM 加密）；可选 SMTP 作为备用兜底。远程 `code-notifier` 网络不可达或返回 `4xx/5xx` 仅记录告警与 `email_status='FAILED'`，绝不阻断 PostgreSQL `contract_notifications` 站内通知持久落库与 MR 门禁主流程。

### 5.2 上线前追加验证门禁 (`M22~M26`)
- **二进制依赖隔离**：`go list -deps ./cmd/agent` 不含 `protocompile`、`go-git`、`gorm`、`dynamicpb`、`sqlite`；`go list -deps ./cmd/server` 不含 `sqlite`；全仓 `grep` 零 `os/exec` 调用外部 `protoc`/`git`；
- **只读无锁与体积增量红线**：`frontend/package.json` 不含 `monaco-editor`、`contract_proto_files` 不含 `locked`/`locked_by` 字段；以 M21 交付基线为基准，`M22~M26` 前端静态 JS 构建产物总增量严格控制在 **`<= 150KB gzip`（`<= 500KB` 未压缩）**；
- **`SC-13` ~ `SC-16` 全链路验收**：逐项覆盖纯 Go 编译 + `layer_level`（含 `DISPATCH` 消费方反向校核与成环 `409`）+ `BASE_LIB` 传递影响雷达 + `/topology` 程序化矢量 SVG/PNG 导出、活体参考文档 + `#L` 行定位 + Markdown 导出、多段式包名 + `Jaccard >= 0.85` 三护栏去重 + LLM 3s 降级、`GitRepo (1:N) GitProtoBundle` + 含删除文件/Fast-Forward 同 SHA 的 MR 四合一预检 + 原位评论更新 + `Override` P1 审计 + `SSEBroker(userID)` 定向推送 + `contract_notifications`/跨机 `code-notifier` 邮件载荷校验及大盘静默合标卡片。

---

## 6. 模块更名 (`code-contract`) 一次性清库重建与环境切换运维指南 ([`ADR-20`](../05-adr/))

### 6.1 共享库 (`code_shield`) 一次性清库与角色切换 SQL

本模块在投产前由旧模块标识切换为 `contract_` 表前缀，采用**「一次性清库 + 原生重建 + `make seed` 重塑」**策略，**保留平台共享表 `users`、`departments`、`sys_audit_logs` 及其他兄弟子系统表**。在本地或测试共享库执行以下幂等脚本：

```sql
-- 一次性清库与管理员角色迁移脚本（直接对 code_shield 执行）
DO $$
DECLARE
    v_old_prefix text := 'tr' || 'aefik_';
    r record;
BEGIN
    -- 1. 级联删除旧前缀全部物理表（含分区母表、按日分区子表及历史遗留表）
    FOR r IN
        SELECT tablename
        FROM pg_tables
        WHERE schemaname = 'public'
          AND tablename LIKE v_old_prefix || '%'
    LOOP
        EXECUTE 'DROP TABLE IF EXISTS ' || quote_ident(r.tablename) || ' CASCADE';
    END LOOP;

    -- 2. 删除旧流量分区自增序列
    EXECUTE 'DROP SEQUENCE IF EXISTS ' || quote_ident(v_old_prefix || 'traffic_records_id_seq') || ' CASCADE';

    -- 3. 清理旧命名空间下的孤儿操作审计记录（归档环境如需保留只读历史可注释此句）
    DELETE FROM sys_audit_logs WHERE action LIKE v_old_prefix || 'git:%';

    -- 4. 将共享库 users 中的旧模块管理员角色迁移为 contract_admin
    UPDATE users
    SET roles = (
        SELECT jsonb_agg(
            CASE WHEN elem = (v_old_prefix || 'admin') THEN 'contract_admin' ELSE elem END
        )
        FROM jsonb_array_elements_text(roles) AS elem
    )
    WHERE roles IS NOT NULL
      AND roles @> jsonb_build_array(v_old_prefix || 'admin');
END $$;
```

执行清库后，立即运行构建与双次幂等 Seed 重建全套 `contract_*` 表与演示基线：

```bash
make build && make seed
```

### 6.2 本地环境变量、配置与 JWT 会话切换清单

1. **后端主密钥与 Agent 环境变量**：
   - 主控主密钥：`CONTRACT_CRYPTO_KEY`（替代原旧变量，未配置时敏感凭据加解密将 fail-closed 拒绝落盘）；
   - Agent 环境变量：`CONTRACT_AGENT_ALLOW_IPS`、`CONTRACT_AGENT_BIN_PATH`；
   - CI/CD 门禁请求头与变量：`X-Contract-CI-Token`（UI 指引变量 `CONTRACT_CI_TOKEN`，严禁通过 URL Query 传递 `x-contract-ci-token`）。
2. **本地配置文件（未入库）**：
   - `code-contract/config.yaml`：确认 `auth.oauth2.client_id: "code-contract"` 与 `auth.jwt_secret` 与宿主门户保持一致；
   - `code-bench/config.yaml`：确认网关节点已切换为 `gateways.contract`（指向 `http://127.0.0.1:8091`），且 `docs.path` 指向 `/home/fugui/codes/code-contract/docs`。
3. **浏览器 JWT 会话刷新**：
   - 完成数据库角色切换与服务重启后，请**退出当前门户登录并重新登录**（或清除浏览器 `localStorage` Token），确保新签发的 JWT 声明中携带最新 `contract_admin` 角色。
