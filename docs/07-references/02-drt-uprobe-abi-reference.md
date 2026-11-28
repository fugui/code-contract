# DRT_ReadLen / DRT_ReadNoCheckLen uprobe 关键结论

> 数据来源：本地 dopra 源码（`dopra/DOPRA SSP/.../drt/client/drt_client/src/drt_clientread.c`、
> `drt_clientwrite.c`，`dopra/DOPRA DDM/.../platform/ssp/include/drt_client.h`，`mesh_drt.c`、
> `mesh_ucm_rstream_recv.c`）+ 现网 ftrace uprobe 实测证据
> （`drt-uprobe-lab/evidence/REPORT.md`、`trace_raw.txt`、`summary.json`）。
> 实测环境：私有库副本 + 内核 6.4.0，200 条消息逐条与 trafficgen 真值对账 1:1。

## 0. 符号偏移（实测，私有 libdopra.so）

| 符号 | 偏移 | 说明 |
|---|---|---|
| `DRT_WriteBulk` | `0x218d00` | 写侧 |
| `DRT_ReadLen` | `0x21be20` | 收侧·取长（peek，不消费） |
| `DRT_ReadNoCheckLen` | `0x21be70` | 收侧·读取（consume，推进读偏移） |

偏移由运行期 `.dynsym` 解析（首个 PT_LOAD vaddr==offset）。注意本地仓库中的
`dopra/DOPRA DDM/.../depend_DOPRA_library/libdopra.so` 是裁剪过的测试依赖库，
`nm -D` 无这些符号；现网偏移需在目标机对真实 libdopra.so 执行
`readelf -sW | grep DRT_Read` 复核。

## 1. 参数与返回值（x86_64 SysV）

```c
/* drt_client.h:681 */ VOS_UINT32 DRT_ReadLen(DRT_HANDLE_T handle, VOS_UINT32 *puiMsgLen);
/* drt_client.h:865 */ VOS_UINT32 DRT_ReadNoCheckLen(DRT_HANDLE_T handle, VOS_CHAR *pcMsgBuf, VOS_UINT32 *puiMsgLen);
```

| 函数 | RDI | RSI | RDX | RAX |
|---|---|---|---|---|
| `DRT_ReadLen(h, puiMsgLen)` | handle | `uint32*` **OUT**，回填下一条消息总长 | — | 0=OK / 错误码 |
| `DRT_ReadNoCheckLen(h, pcMsgBuf, puiMsgLen)` | handle | **输出缓冲区 buf_ptr** | `uint32*` **IN/OUT**：入口=容量，出口=实际读出字节数 | 0=OK / 错误码 |

- **长度不通过 RAX 返回**，一律由输出指针回填。
- 源码佐证：
  - `drtRingMsgReadLen()`（drt_clientread.c:49-85）：只读环上 `DRT_MSG_HEAD_S.uiMagic/uiLength`，
    `*puiMsgLen = pstMsgHead->uiLength`，不拷贝、不推进读偏移（peek）；
  - `drtRingMsgRead()`（:192-254）：`memcpy_sp(pcMsgBuf, ..., pstMsgHead+1, uiMsgLen)` 后
    `pstReader->uiReadOffset += uiMsgLen + sizeof(DRT_MSG_HEAD_S); *puiMsgLen = uiMsgLen;`。
- 错误码：`VOS_OK == 0`（vos_errno.h:81）；
  `DRT_ERRNO_READ_EMPTY = VOS_MID_DRT_CLIENT|0x1`、
  `DRT_ERRNO_WRITE_DOWN = ...|0x4`、
  `DRT_ERRNO_READBUFF_NOT_ENOUGH = ...|0x5`（drt_public.h:18-27）。

## 2. 两者关系：两段式，非内部调用

MESH 接收路径：

```
MESH_Recv
 ├─ meshUcmRstreamGetRecvBufIntra / ...BigMsg            (mesh_ucm_rstream_recv.c:474, mesh_sock_public.c:2269)
 │    └─ MESH_VpiDataGetMsgLen → MESH_DrtGetMsgLen → DRT_ReadLen        ← peek 长度，决定/扩容接收缓冲区
 └─ MESH_VpiDataRecv → MESH_DrtDataRecv → DRT_ReadNoCheckLen            ← 真正读出载荷 (mesh_drt.c:708)
```

- **不是**「ReadLen 内部调 ReadNoCheckLen」——两者平级；
- **是**「MESH_Recv 先调 DRT_ReadLen 预读长度，再调 DRT_ReadNoCheckLen 读载荷」；
- `DRT_Read` / `DRT_ReadForce` 在此数据面完全不触发（实测验证）；
- 每条消息**稳定命中一对 rl→rn**（实测 200:200 严格成对，间隔 2~40µs）。

BPF 设计含义：
- 两个都挂 = 每条消息 2 个事件；若 map key 只用 `pid+tid`，rl 的 entry（RSI=len 指针）
  会被随后的 rn entry（RSI=buf 指针）覆盖 → 需按「tid+探针类型」组合 key 或两张独立 map；
- **建议只挂 DRT_ReadNoCheckLen**（信息最全，且空环时不触发）；DRT_ReadLen 仅用于方向/长度对账。

## 3. 缓冲区布局：一次读出 [40B MESH 头][CROS 载荷]

- 写侧一次 `MESH_Send` = `DRT_WriteBulk(handle, 2, [entry0=40B MESH_MSG_DATA_HEAD_S][entry1=payload])`，
  在 DRT 环上拼成**一条**消息（`drtWriteBulkAsyn`，drt_clientwrite.c:1103-1151）：
  `uiTotalMsgLen = sizeof(DRT_MSG_HEAD_S) + Σ条目长度`；
- 读侧 `drtRingMsgRead` 把整个消息体一次性 memcpy 到 pcMsgBuf；
- 因此 `DRT_ReadNoCheckLen` 的 buf 内容 = `[40B MESH_MSG_DATA_HEAD_S][CROS 载荷]`
  （加密通道另有安全头/MAC，由 MESH 层后续剥离）；
- `DRT_ReadLen` 返回值 = 40 + payload_len（实测 e0l=40, e1l=34 → 74）；
- `MESH_Recv` 交给用户的最终 buffer 是 MESH 剥头后的纯业务载荷；
- **不是**「先读 40B 头再读载荷」两步。

## 4. 空环行为：零触发，无轮询风险

- 空环返回 `DRT_ERRNO_READ_EMPTY`（MESH 映射为 `MESH_ERRNO_RECV_EMPTY`，正常路径不记日志）；
  余量不足一个 DRT_MSG_HEAD_S 也按 READ_EMPTY 处理；
- 接收由 DRT 唤醒机制驱动（`DRT_EVENT_NOTIFY(DRT_EVENT_READ_EMPTY / DATA_PRE_EMPTY)` +
  `DRT_GetReaderPollFlag` / `DRT_ReaderAvailable`），事件驱动非忙轮询；
- **阴性对照实测**：探针 armed、30s 无流量窗口，rl/rn 触发数均为 0；
- 无消息时 MESH 在 DRT_ReadLen 返回 READ_EMPTY 后直接返回，
  **DRT_ReadNoCheckLen 根本不会被调用** → 只挂 rn 时空环零开销，
  uretprobe 无需额外快速退出逻辑；
- 唯一注意：若某上层应用自己忙轮询 MESH_Recv，DRT_ReadLen 会以空结果高频命中（每次 peek 一次）。

## 5. BPF 实现要点

```
uprobe   @0x21be70 (DRT_ReadNoCheckLen):
             保存 arg1(RSI=buf), arg2(RDX=&len) → map[key=tid]
uretprobe 同符号:
             RAX==0 时 deref map 中的 RDX 得实际长度，再从 buf 抓 [40B头][载荷]
可选对账: DRT_ReadLen @0x21be20，独立 map，uretprobe 后 deref RSI 得消息总长（= 40+payload）
```

注意事项：
- ⚠️ uretprobe 触发时除 RAX 外原寄存器不可靠，必须在 uprobe 入口存参；
- 红线（沿用 lab 结论）：只挂私有库副本 inode，不碰生产 libdopra.so 原 inode；
- ftrace 原生 uprobe_events 取参上限 15 个（16 个 EINVAL）；
- 若产品切到 `DRT_WriteBulkWithoutNotify`（免通知路径）或合并多消息 bulk，
  写侧条目索引会变，需按 `uiMsgNum` 解码多条目；读侧两符号语义不受影响。
