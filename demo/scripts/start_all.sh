#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEMO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
BIN_DIR="${DEMO_DIR}/bin"
PID_FILE="/tmp/demo-cros-cluster.pid"
CSG_ADDR="${DEMO_CSG_ADDR:-127.0.0.1:31806}"
CSG_MON_ADDR="${DEMO_CSG_MONITOR_ADDR:-127.0.0.1:31901}"

echo "=========================================================="
echo " Starting CROS RPC + CSG Demo Cluster (7 Processes)"
echo "=========================================================="

# 1. 编译全部可执行文件
echo "[1/7] Building binaries into ${BIN_DIR}..."
mkdir -p "${BIN_DIR}"
go build -o "${BIN_DIR}/csgserver" "${DEMO_DIR}/cmd/csgserver"
go build -o "${BIN_DIR}/backend-account" "${DEMO_DIR}/cmd/backend-account"
go build -o "${BIN_DIR}/backend-inventory" "${DEMO_DIR}/cmd/backend-inventory"
go build -o "${BIN_DIR}/backend-payment" "${DEMO_DIR}/cmd/backend-payment"
go build -o "${BIN_DIR}/sut-order" "${DEMO_DIR}/cmd/sut-order"
go build -o "${BIN_DIR}/client" "${DEMO_DIR}/cmd/client"

# 2. 清理已存在的进程
if [ -f "${PID_FILE}" ]; then
    echo "Stopping existing processes..."
    "${SCRIPT_DIR}/stop_all.sh" || true
fi

# 释放端口 (9001~9005, 31806, 31901)
for port in 9001 9002 9003 9004 9005 31806 31901; do
    fuser -k -n tcp "${port}" 2>/dev/null || true
done
sleep 0.5

# 3. 优先拉起 CSG 控制面与 RPCMonitorService
echo "[2/7] Starting CSG Control Plane (${CSG_ADDR}) & RPCMonitorService (${CSG_MON_ADDR})..."
setsid nohup "${BIN_DIR}/csgserver" --addr="${CSG_ADDR}" --monitor="${CSG_MON_ADDR}" </dev/null >/tmp/demo-csgserver.log 2>&1 &
PID_CSG=$!
sleep 0.4

# 4. 依次拉起下游 Backend (使用 setsid nohup 脱离会话独立存活并自动向 CSG PUB)
echo "[3/7] Starting Backend 1: Account Service (:9002)..."
setsid nohup "${BIN_DIR}/backend-account" --addr="127.0.0.1:9002" --csg="${CSG_ADDR}" </dev/null >/tmp/demo-backend-account.log 2>&1 &
PID_ACC=$!

echo "[4/7] Starting Backend 2: Inventory Service (:9003)..."
setsid nohup "${BIN_DIR}/backend-inventory" --addr="127.0.0.1:9003" --csg="${CSG_ADDR}" </dev/null >/tmp/demo-backend-inventory.log 2>&1 &
PID_INV=$!

echo "[5/7] Starting Payment Instance 1 (LIB Impl ALIPAY :9004)..."
setsid nohup "${BIN_DIR}/backend-payment" --addr="127.0.0.1:9004" --channel="ALIPAY" --csg="${CSG_ADDR}" </dev/null >/tmp/demo-backend-payment-alipay.log 2>&1 &
PID_PAY_ALI=$!

echo "[6/7] Starting Payment Instance 2 (LIB Impl WECHAT :9005)..."
setsid nohup "${BIN_DIR}/backend-payment" --addr="127.0.0.1:9005" --channel="WECHAT" --csg="${CSG_ADDR}" </dev/null >/tmp/demo-backend-payment-wechat.log 2>&1 &
PID_PAY_WX=$!

sleep 0.5

# 5. 拉起被测核心服务 SUT (自动 PUB OrderService 并通过 CSG SUB 解析下游)
echo "[7/7] Starting SUT: Order Gateway (:9001)..."
setsid nohup "${BIN_DIR}/sut-order" \
    --addr="127.0.0.1:9001" \
    --csg="${CSG_ADDR}" \
    --account-addr="127.0.0.1:9002" \
    --inventory-addr="127.0.0.1:9003" \
    --payment-alipay-addr="127.0.0.1:9004" \
    --payment-wechat-addr="127.0.0.1:9005" </dev/null >/tmp/demo-sut-order.log 2>&1 &
PID_SUT=$!

# 保存 PID
echo "${PID_CSG} ${PID_ACC} ${PID_INV} ${PID_PAY_ALI} ${PID_PAY_WX} ${PID_SUT}" > "${PID_FILE}"

sleep 0.5
echo ""
echo "=========================================================="
echo " Cluster Started Successfully!"
echo "   • CSG Server & Monitor       : PID ${PID_CSG} -> dlink ${CSG_ADDR}, mon ${CSG_MON_ADDR} (log: /tmp/demo-csgserver.log)"
echo "   • Backend 1 (Account)        : PID ${PID_ACC} -> 127.0.0.1:9002 (log: /tmp/demo-backend-account.log)"
echo "   • Backend 2 (Inventory)      : PID ${PID_INV} -> 127.0.0.1:9003 (log: /tmp/demo-backend-inventory.log)"
echo "   • Payment Impl 1 (ALIPAY)    : PID ${PID_PAY_ALI} -> 127.0.0.1:9004 (log: /tmp/demo-backend-payment-alipay.log)"
echo "   • Payment Impl 2 (WECHAT)    : PID ${PID_PAY_WX} -> 127.0.0.1:9005 (log: /tmp/demo-backend-payment-wechat.log)"
echo "   • SUT (Order Gateway)        : PID ${PID_SUT} -> 127.0.0.1:9001 (log: /tmp/demo-sut-order.log)"
echo "=========================================================="
echo ""
echo "To inspect CSG service registry:"
echo "   ${BIN_DIR}/csgserver --query --addr=${CSG_ADDR} --monitor=${CSG_MON_ADDR} --op=all"
echo "To interact with the cluster, launch the TUI Client:"
echo "   ${BIN_DIR}/client --csg=${CSG_ADDR} --addr=127.0.0.1:9001"
echo "or run:"
echo "   make -C demo run-client"
echo ""
