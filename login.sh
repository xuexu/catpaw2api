#!/usr/bin/env bash
# login.sh — CatPaw Passport 登录：生成登录链接 → 浏览器登录 → token 落盘 auths/catpaw-{uid}.json
#
# 两种模式：
#   ./login.sh              本机有浏览器：自动打开浏览器，回调 + 轮询双通道
#   ./login.sh -print-only  服务器/无浏览器：打印登录链接，在任意机器浏览器打开，
#                           登录完成后浏览器跳转 127.0.0.1 失败属正常（token 由轮询下发）
#
# 用法: ./login.sh [-print-only] [-auth-dir ./auths]
set -euo pipefail
cd "$(dirname "$0")"

AUTH_DIR="./auths"
EXTRA=()
if [[ "${1:-}" == "-print-only" ]]; then
    EXTRA+=("-print-only")
    shift
fi
if [[ "${1:-}" == "-auth-dir" ]]; then
    AUTH_DIR="${2:?usage: -auth-dir <dir>}"
    EXTRA+=("-auth-dir" "$AUTH_DIR")
fi
mkdir -p "$AUTH_DIR"

BIN=./bin/catpaw2api-login
if [ ! -x "$BIN" ]; then
    echo "build login binary ..."
    mkdir -p ./bin
    go build -o "$BIN" ./cmd/login
fi

echo "============================================================"
echo "  CatPaw 登录（catx-passport）"
echo "============================================================"
echo "步骤："
echo "  1. 打开下面链接，在 CatPaw 登录页完成登录"
echo "  2. 登录成功后如浏览器跳到打不开的 127.0.0.1 地址，忽略即可"
echo "  3. 本脚本会自动轮询换取 token 并落盘 auths/"
echo ""

exec "$BIN" "${EXTRA[@]}"
