#!/bin/bash
# 打面板包 harness-panel（确定性：同一份源码连跑多次 sha256 一致）
#
# 步骤：图标/版本自检 → make-fpk.py → 包纹自动核对（fingerprint.py，不通过退出非 0）
# 用法：bash scripts/build-panel.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/harness-panel/fnpack"
BUILD_ROOT="${DSH_BUILD:-/vol1/1000/work/deepseek/dsh-build}"
OUT="$BUILD_ROOT/dist"
# 版本唯一来源是 manifest：面板包不带 VERSION 文件（两个来源会漂移）
VERSION="$(grep -E '^version' "$SRC/manifest" | head -1 | sed 's/.*=\s*//' | tr -d '[:space:]')"

green() { printf '\033[32m%s\033[0m\n' "$*"; }
red() { printf '\033[31m%s\033[0m\n' "$*" >&2; }

# ── 0. 自检 ────────────────────────────────────────────────
python3 "$ROOT/scripts/check-icon.py" --src "$SRC" --name harness-panel \
  --apps-root /vol1/1000/work/deepseek/fnos-apps --extra /vol1/1000/work/deepseek/dsh-src/fnpack || exit 1
[ -n "$VERSION" ] || { red "✗ manifest 里没有 version 行"; exit 1; }
# 面板走独立的 1.x 版本线，防止误把本体的 0.2.1-rc.x 写进来
case "$VERSION" in
  1.*.*) ;;
  *) red "✗ 面板版本应为 1.x（独立版本线），实际 $VERSION"; exit 1 ;;
esac
[ -f "$SRC/app/fnshell.py" ] || { red "✗ 缺少 app/fnshell.py"; exit 1; }
[ -d "$SRC/app/panel_dist" ] || { red "✗ 缺少 app/panel_dist（先跑前端构建）"; exit 1; }
echo "✓ 前置检查通过：version=$VERSION（来源 manifest）"

# ── 1. 确定性打包 ─────────────────────────────────────────
mkdir -p "$OUT"
FPK="$OUT/harness-panel_v${VERSION}_x86.fpk"
python3 "$ROOT/scripts/make-fpk.py" --src "$SRC" --out "$FPK"

# ── 2. 包纹核对（自动，不通过就退出非 0）──────────────────
echo "── 包纹核对 ──"
python3 "$ROOT/scripts/fingerprint.py" \
  --root "$ROOT" --rt "${RUNTIME_VERSION:-0.2.1-rc.2}" \
  --panel "$FPK" --panel-version "$VERSION" \
  --apps-root /vol1/1000/work/deepseek/fnos-apps
green "✓ $FPK"
