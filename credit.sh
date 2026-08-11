#!/usr/bin/env bash
# credit.sh — CatPaw 额度日报
#
# 用法:
#   ./credit.sh            # 人类可读日报
#   ./credit.sh -json      # 原始 JSON
#   ./credit.sh -uid <uid> # 指定账号
#   ./credit.sh -apply register|campaign   # 手动申请额度
set -euo pipefail
cd "$(dirname "$0")"

BIN=./bin/catpaw2api-credit
if [ ! -x "$BIN" ]; then
    echo "build credit binary ..."
    mkdir -p ./bin
    go build -o "$BIN" ./cmd/credit
fi

exec "$BIN" "$@"
