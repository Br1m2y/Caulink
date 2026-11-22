#!/usr/bin/env bash
# 每人本地跑一次：启用共享 git hook、检查工具链、准备本地配置。
#
# 用法（仓库根目录，Git Bash / WSL / macOS / Linux）：
#   bash scripts/dev-setup.sh
#
# 幂等：重复执行不会覆盖已有的本地配置。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

say()  { printf '%s\n' "$*"; }
ok()   { printf '  [ok]   %s\n' "$*"; }
warn() { printf '  [warn] %s\n' "$*" >&2; }
die()  { printf '  [fail] %s\n' "$*" >&2; exit 1; }

say "==> 1/4 仓库检查"
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "这里不是 git 仓库：$ROOT"
ok "仓库根目录 $ROOT"
ok "当前分支 $(git rev-parse --abbrev-ref HEAD)"

say "==> 2/4 启用共享 hook（挡直推 main）"
git config core.hooksPath .githooks
chmod +x .githooks/pre-push 2>/dev/null || warn "chmod 失败，Windows 下可忽略（hook 由 Git 自带的 sh 执行）"
ok "core.hooksPath = $(git config core.hooksPath)"
if [ -f .githooks/pre-push ]; then ok "hook 文件存在"; else warn ".githooks/pre-push 缺失，直推保护不会生效"; fi

say "==> 3/4 工具链"
if command -v go >/dev/null 2>&1; then
  ok "go $(go env GOVERSION 2>/dev/null || go version | awk '{print $3}')"
  if go vet ./... >/dev/null 2>&1; then ok "go vet 通过"; else warn "go vet 失败，先跑 make check 看具体错误"; fi
else
  warn "没找到 go：winget install --id GoLang.Go -e（需要 1.23+）"
fi

if command -v wails3 >/dev/null 2>&1; then
  ok "wails3 $(wails3 version 2>/dev/null | head -n1)"
else
  warn "没找到 wails3（Phase 3 才需要）：go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0"
fi

if command -v gh >/dev/null 2>&1; then
  ok "gh $(gh --version | head -n1 | awk '{print $3}')"
else
  warn "没找到 gh（只有仓库创建者配分支保护时需要）：winget install --id GitHub.cli -e"
fi

say "==> 4/4 本地配置"
if [ -f configs/caulinkd.local.json ]; then
  ok "configs/caulinkd.local.json 已存在，不覆盖"
elif [ -f configs/caulinkd.example.json ]; then
  cp configs/caulinkd.example.json configs/caulinkd.local.json
  ok "已从示例复制 configs/caulinkd.local.json（已在 .gitignore 中）"
else
  warn "找不到 configs/caulinkd.example.json，跳过"
fi

say ""
say "完成。日常约定："
say "  - 开短分支：git switch -c feat/<模块>-<主题>"
say "  - 2–3 天内 PR 合回 main，PR 描述见 .github/PULL_REQUEST_TEMPLATE.md"
say "  - 改动 pkg/contract 或 api/schema：PR 标题加 contract: 前缀，A、B 双批"
