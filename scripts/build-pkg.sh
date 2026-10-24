#!/bin/bash
# 打本体包 deepseek.harness（版本取 VERSION，当前 0.2.1-rc.2.1）
#
# 确定性：同一份源码 + 同一份 dist，连跑多次 sha256 完全一致
# （make-fpk.py 固定 mtime/uid/gid，gzip -n 不写时间戳）。
#
# 用法：bash scripts/build-pkg.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/fnpack"
BUILD_ROOT="${DSH_BUILD:-/vol1/1000/work/deepseek/dsh-build}"
RUNTIME_TAR="$BUILD_ROOT/deepseek-harness.tar.gz"
BIN="$BUILD_ROOT/bin/deepseek.harness"
OUT="$BUILD_ROOT/dist"
VERSION_MAIN="0.2.1-rc.2.1"
RUNTIME_VERSION="$(tr -d '[:space:]' < "$ROOT/RUNTIME_VERSION")"

green() { printf '\033[32m%s\033[0m\n' "$*"; }
red() { printf '\033[31m%s\033[0m\n' "$*" >&2; }

# ── 0. 前置检查 ────────────────────────────────────────────
[ -f "$BIN" ] || { red "✗ 缺少 Go 二进制 $BIN（先跑 go build）"; exit 1; }
[ -f "$RUNTIME_TAR" ] || { red "✗ 缺少运行时包 $RUNTIME_TAR"; exit 1; }
python3 "$ROOT/scripts/check-icon.py" --src "$SRC" --name deepseek-harness \
  --apps-root /vol1/1000/work/deepseek/fnos-apps --extra /vol1/1000/work/deepseek/dsh-src/fnpack || exit 1
python3 "$ROOT/scripts/check-version.py" --root "$ROOT" \
  --runtime-dir "$BUILD_ROOT/runtime" --tar "$RUNTIME_TAR" || exit 1

# ── 1. 组装应用载荷 ───────────────────────────────────────
green "── 组装应用载荷（运行时 $RUNTIME_VERSION）"
mkdir -p "$SRC/app/bin" "$SRC/app/ui"
cp "$BIN" "$SRC/app/bin/deepseek.harness"
chmod 0755 "$SRC/app/bin/deepseek.harness"
cp "$RUNTIME_TAR" "$SRC/app/deepseek-harness.tar.gz"
printf '%s\n' "$RUNTIME_VERSION" > "$SRC/app/.version"

# ui/config 必须只有 1 个 url 入口，指向本体直连端口
python3 - "$SRC/app/ui/config" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
urls = d.get(".url", {})
assert len(urls) == 1, f"ui/config 入口数应为 1，实际 {len(urls)}"
(key, entry), = urls.items()
assert entry.get("type") == "url", f"入口类型必须是 url，实际 {entry.get('type')}"
assert int(entry.get("port") or 0) == 2299, f"端口必须是 2299，实际 {entry.get('port')}"
assert entry.get("url") == "/", f"url 必须是 /，实际 {entry.get('url')}"
assert "gatewaySocket" not in entry and "gatewayPrefix" not in entry, "不应再有网关 socket 形态字段"
print(f"✓ ui/config 校验通过：{key} type=url port=2299 url=/")
PY

# install_callback 必须保证装完能自启：把 last_run_state 置为 running。
# 否则「停着卸载 → 重装」的用户装完只有控制面、2298 不监听、桌面图标打不开，
# 而那时面板可能还没装，用户没地方点启动（曾经踩过，是真实死循环）。
CALLBACK="$SRC/cmd/install_callback"
[ -f "$CALLBACK" ] || { red "✗ 缺少 cmd/install_callback"; exit 1; }
grep -q "last_run_state" "$CALLBACK" || { red "✗ install_callback 没有写 last_run_state 的逻辑"; exit 1; }
grep -q "'running'" "$CALLBACK" || grep -q '"running"' "$CALLBACK" || { red "✗ install_callback 没有把 last_run_state 置为 running"; exit 1; }
grep -q "config.json" "$CALLBACK" || { red "✗ install_callback 没有操作 config.json"; exit 1; }
echo "✓ install_callback 自启校验通过：装完会把 last_run_state 置为 running"

# ── 2. 确定性打包（make-fpk.py：固定 mtime/uid/gid + gzip -n）──
green "── 打包 $VERSION_MAIN"
mkdir -p "$OUT"
MAIN_FPK="$OUT/deepseek.harness_v${VERSION_MAIN}_x86.fpk"
python3 "$ROOT/scripts/make-fpk.py" --src "$SRC" --out "$MAIN_FPK" || exit 1
green "✓ $MAIN_FPK"


# ── 3. 包纹核对（七项）──────────────────
green "── 包纹核对"
python3 "$ROOT/scripts/fingerprint.py" \
  --root "$ROOT" --rt "$RUNTIME_VERSION" \
  --main "$MAIN_FPK" --main-version "$VERSION_MAIN" \
  --apps-root /vol1/1000/work/deepseek/fnos-apps
