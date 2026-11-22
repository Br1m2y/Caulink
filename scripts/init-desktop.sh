#!/usr/bin/env bash
# 生成 Wails v3 桌面工程到 desktop/。
#
# 用法（仓库根目录）：
#   scripts/init-desktop.sh
#
# 前置条件：
#   go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0
#   wails3 setup
#
# 设计说明见 desktop/README.md 与 docs/adr/0002-ui-framework-wails.md。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_NAME="${APP_NAME:-caulink-desktop}"
TEMPLATE="${TEMPLATE:-vanilla-js}"
MODULE="github.com/Br1m2y/caulink/desktop"

if ! command -v wails3 >/dev/null 2>&1; then
  cat >&2 <<'EOF'
找不到 wails3。先装：
  go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0
  wails3 setup
（go install 之后 $GOPATH/bin 要进 PATH，或者把 "$(go env GOPATH)/bin" 加进去）
EOF
  exit 1
fi

if [ -f "$ROOT/desktop/main.go" ]; then
  echo "desktop/main.go 已存在，跳过生成。要重新生成请先手动删掉 desktop/ 下 wails3 生成的文件（保留 README.md）。" >&2
  exit 0
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "==> 生成模板 $TEMPLATE 到临时目录"
( cd "$TMP" && wails3 init -n "$APP_NAME" -t "$TEMPLATE" )

GEN="$TMP/$APP_NAME"
if [ ! -d "$GEN" ]; then
  echo "生成目录不存在：$GEN" >&2
  exit 1
fi

echo "==> 移入 $ROOT/desktop"
mkdir -p "$ROOT/desktop"
shopt -s dotglob nullglob
for p in "$GEN"/*; do
  base="$(basename "$p")"
  # 保留仓库自己的 README（里面写了初始化与进程关系）
  [ "$base" = "README.md" ] && continue
  mv "$p" "$ROOT/desktop/"
done

if [ -f "$ROOT/desktop/go.mod" ]; then
  echo "==> 改写 module 为 $MODULE"
  sed -i.bak "s|^module .*|module $MODULE|" "$ROOT/desktop/go.mod"
  rm -f "$ROOT/desktop/go.mod.bak"
fi

# 示例 service 换成真实控制面调用由 C 负责，这里先删掉避免误导
rm -f "$ROOT/desktop/greetservice.go"

echo
echo "完成。下一步："
echo "  cd desktop && wails3 dev"
echo "首次构建 Release："
echo "  cd desktop && wails3 build"
