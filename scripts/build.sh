#!/usr/bin/env bash
# 观复构建脚本：前端 SvelteKit 静态产物 → 内嵌 → Go 单二进制。
#
# 与慎始/幕间同范式：显式指定工具链（沙箱 PATH 常缺 go、node 版本多），
# 避免隐式依赖交互式 shell 的环境。
#
# 用法：
#   ./scripts/build.sh            # 全量构建（前端 + 内嵌 + go build）
#   GO_BIN=/path/to/go NODE_BIN=/path/to/node ./scripts/build.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# ---- 工具链定位（均可用 env 覆盖） ----
GO_BIN="${GO_BIN:-$(command -v go || echo /opt/homebrew/bin/go)}"
NODE_BIN="${NODE_BIN:-$(command -v node || echo /Users/yufei/.workbuddy/binaries/node/versions/22.22.2-6/bin/node)}"
PNPM_BIN="${PNPM_BIN:-$(command -v pnpm || echo /opt/homebrew/bin/pnpm)}"

if [[ ! -x "$GO_BIN" ]]; then
  echo "错误：找不到 go（GO_BIN=$GO_BIN）" >&2
  exit 1
fi
if [[ ! -x "$NODE_BIN" ]]; then
  echo "错误：找不到 node（NODE_BIN=$NODE_BIN）" >&2
  exit 1
fi

# ---- 1. 前端构建 ----
echo "==> [1/3] 构建前端（SvelteKit 5 + Tailwind v4）"
(
  cd web
  PATH="$(dirname "$NODE_BIN"):$PATH" "$PNPM_BIN" install --frozen-lockfile
  PATH="$(dirname "$NODE_BIN"):$PATH" "$PNPM_BIN" build
)

# ---- 2. 产物内嵌 ----
echo "==> [2/3] 复制前端产物到 internal/web/dist（go:embed）"
rm -rf internal/web/dist
mkdir -p internal/web/dist
cp -r web/build/. internal/web/dist/
# 仓库里 tracked 的 .gitkeep 会被上面的 rm -rf 一起删掉，工作区就永久挂着一条删除记录。
touch internal/web/dist/.gitkeep

# ---- 3. Go 单二进制 ----
echo "==> [3/3] go build"
CGO_ENABLED=0 "$GO_BIN" build -trimpath -ldflags "-s -w" -o bin/guanfu ./cmd/guanfu

echo ""
echo "构建完成：bin/guanfu（前端已内嵌，单文件分发）"
