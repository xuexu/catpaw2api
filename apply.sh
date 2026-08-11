#!/usr/bin/env bash
# apply.sh — 批量自动申请额度（对应其他项目的自动签到）
#
# 用法:
#   ./apply.sh                        # 默认 register，低于阈值 50 才申请
#   ./apply.sh -force                 # 无视阈值，全部申请
#   ./apply.sh -method campaign       # 改用积分中心活动接口
#   ./apply.sh -threshold 30
set -e
cd "$(dirname "$0")"

BIN=./bin/catpaw2api-apply
if [ ! -x "$BIN" ]; then
    echo "build apply binary ..."
    mkdir -p ./bin
    go build -o "$BIN" ./cmd/apply
fi

exec "$BIN" "$@"
