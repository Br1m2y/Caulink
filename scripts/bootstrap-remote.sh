#!/usr/bin/env bash
# 仓库创建者跑一次：把远端配成《仓库骨架设计.md》第 5 节要求的形态。
#
# 做的事（按顺序）：
#   1. 检查 gh 已安装并登录
#   2. 解析 owner/repo（从 origin 推断，可用 OWNER/REPO 环境变量覆盖）
#   3. 远端没有 main 时，把当前 HEAD 推成 main（迁移用）
#   4. 把默认分支设为 main
#   5. 给 main 配分支保护（默认跳过，见 SKIP_PROTECTION）
#
# 用法：
#   bash scripts/bootstrap-remote.sh                  # 执行（默认跳过保护）
#   DRY_RUN=1 bash scripts/bootstrap-remote.sh        # 只打印，不改远端
#   SKIP_PROTECTION=1 bash scripts/bootstrap-remote.sh   # 明确跳过保护（默认值）
#   SKIP_PROTECTION=0 bash scripts/bootstrap-remote.sh   # 配保护，需要仓库 admin
#   SKIP_PROTECTION=0 REQUIRE_CHECKS=1 bash scripts/bootstrap-remote.sh  # 再要求 CI 通过
#
# 团队当前选择：跳过远端保护，只靠本地 pre-push hook + 约定。
# 注意：GitHub Free 账号的私有仓库本来也不支持分支保护（403 Upgrade to GitHub Pro）。
set -euo pipefail

DRY_RUN="${DRY_RUN:-0}"
REQUIRE_CHECKS="${REQUIRE_CHECKS:-0}"
BRANCH="${BRANCH:-main}"
CREATE_MAIN="${CREATE_MAIN:-1}"
# 团队选择：暂不配远端分支保护，只靠本地 hook + 约定。
SKIP_PROTECTION="${SKIP_PROTECTION:-1}"

say()  { printf '%s\n' "$*"; }
ok()   { printf '  [ok]   %s\n' "$*"; }
warn() { printf '  [warn] %s\n' "$*" >&2; }
die()  { printf '  [fail] %s\n' "$*" >&2; exit 1; }
run()  {
  if [ "$DRY_RUN" = "1" ]; then
    printf '  [dry]  %s\n' "$*"
  else
    "$@"
  fi
}

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

say "==> 1/5 前置检查"
command -v gh >/dev/null 2>&1 || die "没装 gh：winget install --id GitHub.cli -e，然后 gh auth login"
gh auth status >/dev/null 2>&1 || die "gh 未登录：gh auth login"
ok "gh 已安装并登录"
[ -f .github/CODEOWNERS ] || warn "缺少 .github/CODEOWNERS，require_code_owner_reviews 会配不上"

say "==> 2/5 解析仓库"
REMOTE_URL="$(git remote get-url origin 2>/dev/null || true)"
[ -n "$REMOTE_URL" ] || die "没有 origin 远端"
DERIVED="$(printf '%s' "$REMOTE_URL" | sed -E 's#(git@|https://)github\.com[:/]##; s#\.git$##')"
OWNER="${OWNER:-${DERIVED%%/*}}"
REPO="${REPO:-${DERIVED##*/}}"
ok "owner=$OWNER repo=$REPO url=$REMOTE_URL"

say "==> 3/5 确保 $BRANCH 存在"
if git ls-remote --exit-code --heads origin "$BRANCH" >/dev/null 2>&1; then
  ok "远端已有 $BRANCH"
else
  warn "远端没有 $BRANCH"
  if [ "$CREATE_MAIN" != "1" ]; then
    say "  跳过创建（CREATE_MAIN=0）。手动执行：git push origin HEAD:refs/heads/$BRANCH"
  else
    say "  把当前分支 $(git rev-parse --abbrev-ref HEAD) 的 HEAD 推为 $BRANCH"
    run git push origin "HEAD:refs/heads/$BRANCH"
    ok "已创建 $BRANCH"
  fi
fi

say "==> 4/5 默认分支设为 $BRANCH"
if [ "$DRY_RUN" = "1" ]; then
  say "  [dry]  gh repo edit $OWNER/$REPO --default-branch $BRANCH"
else
  gh repo edit "$OWNER/$REPO" --default-branch "$BRANCH" >/dev/null 2>&1 || warn "设置默认分支失败（权限或分支不存在）"
fi
ok "默认分支 -> $BRANCH"

say "==> 5/5 分支保护"
if [ "$SKIP_PROTECTION" = "1" ]; then
  say "  已按 SKIP_PROTECTION=1 跳过（团队当前选择：只靠本地 hook + 约定）"
  say "  以后要补：bash scripts/bootstrap-remote.sh（需要仓库 admin 权限）"
  exit 0
fi
if [ "$REQUIRE_CHECKS" = "1" ]; then
  CHECKS_JSON='{"strict":true,"contexts":["go"]}'
  say "  同时要求 CI 检查：go"
else
  CHECKS_JSON='null'
  say "  暂不要求 CI 检查（REQUIRE_CHECKS=0）；等第一次 CI 跑过之后再跑一次本脚本并设 REQUIRE_CHECKS=1"
fi

PAYLOAD=$(cat <<JSON
{
  "required_status_checks": $CHECKS_JSON,
  "enforce_admins": false,
  "required_pull_request_reviews": {
    "dismiss_stale_reviews": true,
    "require_code_owner_reviews": true,
    "required_approving_review_count": 1
  },
  "restrictions": null,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "required_conversation_resolution": true
}
JSON
)

if [ "$DRY_RUN" = "1" ]; then
  say "  [dry] 将 PUT repos/$OWNER/$REPO/branches/$BRANCH/protection，payload："
  printf '%s\n' "$PAYLOAD" | sed 's/^/         /'
else
  set +e
  OUT=$(printf '%s\n' "$PAYLOAD" | gh api -X PUT "repos/$OWNER/$REPO/branches/$BRANCH/protection" --input - 2>&1)
  RC=$?
  set -e
  if [ $RC -eq 0 ]; then
    ok "main 保护已配置"
  else
    warn "配置失败："
    printf '%s\n' "$OUT" | sed 's/^/         /'
    case "$OUT" in
      *"Upgrade to GitHub Pro"*|*"403"*)
        say ""
        say "  原因：GitHub Free 账号的私有仓库不支持分支保护/rulesets。三个选择："
        say "    A. 申请 Student Developer Pack（免费拿 Pro，1–3 天）：https://education.github.com/pack"
        say "    B. 仓库设为 public：gh repo edit $OWNER/$REPO --visibility public --accept-visibility-change-consequences"
        say "    C. 先只靠本地 hook：bash scripts/dev-setup.sh（每人各跑一次）"
        say "  推荐顺序：先用 C 顶着开发，A 下来后重跑本脚本。"
        ;;
      *"Not Found"*)
        say "  提示：先确认 $BRANCH 已存在，且 gh 账号对 $OWNER/$REPO 有 admin 权限。"
        ;;
    esac
    say "  （保护是可选步骤：确认跳过请加 SKIP_PROTECTION=1，这是当前团队的默认值）"
  fi
fi

say ""
say "完成。后续："
say "  1. 每位成员各跑一次：bash scripts/dev-setup.sh"
say "  2. 第一次 CI 在 $BRANCH 上跑过之后：REQUIRE_CHECKS=1 bash scripts/bootstrap-remote.sh"
say "  3. 从此刻起禁止直推 $BRANCH，一律开分支 + PR"
