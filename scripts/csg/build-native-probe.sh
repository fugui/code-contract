#!/usr/bin/env bash
# 编译 scripts/csg/rpcmon_probe.cpp（只读探针：调用 RPCMonitorService 的 5 个查询接口）。
#
# 两种模式：
#   1) 本机编译（本机装有 DOPRA SW SDK）：
#        ./scripts/csg/build-native-probe.sh -o bin/rpcmon_probe
#   2) 在真机上编译（本机无 SDK 时）：
#        RPCMON_SSHPASS=... ./scripts/csg/build-native-probe.sh \
#            -r user@host -o '$HOME/csgprobe/rpcmon_probe'
#
# 探针严格遵守 D5 只读约束：只做 PlatformInit / CSG_ClientInit /
# CSG_SubServiceByAssignedPort / CSG_WaitForServiceClient 和 5 个 Get* 查询，
# 不 PUB / UNPUB / GET_BIND_ADDR / FREE_BIND_ADDR，不影响现网业务。
set -euo pipefail
cd "$(dirname "$0")/../.."

SRC="scripts/csg/rpcmon_probe.cpp"
OUT="./rpcmon_probe"
REMOTE=""
SSH_PORT=""
SDK='$HOME/SW'

usage() {
    sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
    cat <<'EOF'

参数：
  -o, --out <path>        产物路径。本机模式为本地路径；-r 模式为远端路径（可含 $HOME）。
  -r, --remote <u@host>   在远端编译（经 ssh；设置了 RPCMON_SSHPASS 时走 sshpass）。
      --ssh-port <port>   远端 ssh 端口。
      --sdk <dir>         远端 SW SDK 根目录，默认 $HOME/SW。
  -h, --help              显示本帮助。
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        -o|--out) OUT="$2"; shift 2 ;;
        -r|--remote) REMOTE="$2"; shift 2 ;;
        --ssh-port) SSH_PORT="$2"; shift 2 ;;
        --sdk) SDK="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
    esac
done

# DOPRA 编译宏，取自 SW SDK 的 me/gdf/export/mefac/FindDopra.cmake。
DOPRA_DEFS=(
    -DVOS_OS_VER=VOS_LINUX
    -DVOS_WORDSIZE=64
    -DVOS_BYTE_ORDER=VOS_LITTLE_ENDIAN
    -DVOS_HARDWARE_PLATFORM=VOS_X86
    -DVOS_CPU_TYPE=VOS_X86
)
# 必须 c++17：CrosServiceStub.h 的 CrosStatusEx 初始化在 c++11 下编译失败。
CXX_COMMON=(-std=c++17 -O2 -Wall -Wno-unused-variable "${DOPRA_DEFS[@]}")

# 注意：真机上直接 -I$SDK/include 会因符号链接/相对路径失效，
# 所以先把需要的头文件目录摊平到 $STAGE 再引用。
STAGE='${TMPDIR:-/tmp}/swinc'

remote_cmd() {
    if [[ -n "${RPCMON_SSHPASS:-}" ]]; then
        SSHPASS="$RPCMON_SSHPASS" sshpass -e ssh -o StrictHostKeyChecking=no \
            -o UserKnownHostsFile=/dev/null -o PubkeyAuthentication=no \
            -o PreferredAuthentications=password ${SSH_PORT:+-p "$SSH_PORT"} "$REMOTE" "$@"
    else
        ssh -o BatchMode=yes -o StrictHostKeyChecking=no ${SSH_PORT:+-p "$SSH_PORT"} "$REMOTE" "$@"
    fi
}

build_body() {
    cat <<EOF
set -e
SDK="$SDK"
STAGE="$STAGE"
mkdir -p "\$STAGE/3rd" "\$STAGE/out"
if [[ ! -f "\$STAGE/sw/rpc/gen/cplus/RPCMonitorServicePgStub.h" ]]; then
    cp -r "\$SDK/include/sw" "\$STAGE/" 2>/dev/null || true
fi
cp -r "\$SDK/include/3rd/dopra" "\$STAGE/3rd/" 2>/dev/null || true
cp -r "\$SDK/include/3rd/google" "\$STAGE/3rd/" 2>/dev/null || true
cp -r "\$SDK/include/3rd/securec" "\$STAGE/3rd/" 2>/dev/null || true
mkdir -p "\$(dirname "$OUT")"
g++ $(printf '%q ' "${CXX_COMMON[@]}") \\
    -I"\$STAGE" -I"\$STAGE/3rd" -I"\$STAGE/3rd/dopra" -I"\$STAGE/3rd/securec" \\
    -I"\$STAGE/sw/ssp/export" -I"\$STAGE/sw/ltcommon/export" \\
    -I"\$STAGE/sw/rpc/gen/cplus" -I"\$STAGE/sw/gen/cplus" \\
    "\$STAGE/out/rpcmon_probe.cpp" -o "$OUT" \\
    -L"\$SDK/lib/link/sw" -L"\$SDK/lib/3rd/dopra" -L"\$SDK/lib/3rd" \\
    -lSRMLFClient -lCommonType -l:libprotobuf.so.24 -lcroscppapi -lcroscpp \\
    -ldophicros -ldophimesh -lddmbase -lsecurec -lSSPAFplat -lSSPAFcf -lSSPAFCfg \\
    -lpthread -ldl -lrt
echo "built: $OUT"
EOF
}

if [[ -z "$REMOTE" ]]; then
    echo "== 本机编译 $SRC -> $OUT" >&2
    STAGE_DIR="${TMPDIR:-/tmp}/swinc"
    mkdir -p "$STAGE_DIR/3rd" "$STAGE_DIR/out"
    [[ -d "$STAGE_DIR/sw" ]] || cp -r "$HOME/SW/include/sw" "$STAGE_DIR/" 2>/dev/null || true
    for d in dopra google securec; do
        [[ -d "$STAGE_DIR/3rd/$d" ]] || cp -r "$HOME/SW/include/3rd/$d" "$STAGE_DIR/3rd/" 2>/dev/null || true
    done
    cp "$SRC" "$STAGE_DIR/out/rpcmon_probe.cpp"
    g++ "${CXX_COMMON[@]}" \
        -I"$STAGE_DIR" -I"$STAGE_DIR/3rd" -I"$STAGE_DIR/3rd/dopra" -I"$STAGE_DIR/3rd/securec" \
        -I"$STAGE_DIR/sw/ssp/export" -I"$STAGE_DIR/sw/ltcommon/export" \
        -I"$STAGE_DIR/sw/rpc/gen/cplus" -I"$STAGE_DIR/sw/gen/cplus" \
        "$STAGE_DIR/out/rpcmon_probe.cpp" -o "$OUT" \
        -L"$HOME/SW/lib/link/sw" -L"$HOME/SW/lib/3rd/dopra" -L"$HOME/SW/lib/3rd" \
        -lSRMLFClient -lCommonType -l:libprotobuf.so.24 -lcroscppapi -lcroscpp \
        -ldophicros -ldophimesh -lddmbase -lsecurec -lSSPAFplat -lSSPAFcf -lSSPAFCfg \
        -lpthread -ldl -lrt
    echo "built: $OUT"
    exit 0
fi

echo "== 上传 $SRC -> $REMOTE:\$STAGE/out/rpcmon_probe.cpp" >&2
remote_cmd "mkdir -p $STAGE/out && cat > $STAGE/out/rpcmon_probe.cpp" < "$SRC"
echo "== 远端编译 -> $REMOTE:$OUT" >&2
remote_cmd "$(build_body)"
