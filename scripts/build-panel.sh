#!/bin/bash
# 打面板包 harness-panel（确定性：同一份源码连跑多次 sha256 一致）
#
# 步骤：图标/版本自检 → make-fpk.py → 包纹自动核对（fingerprint.py，不通过退出非 0）
# 用法：bash scripts/build-panel.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/harness-panel/fnpack"
APPDIR="$(cd "${ROOT}/.." && pwd)"
BUILD_ROOT="${DSH_BUILD:-${APPDIR}/build}"
OUT="${APPDIR}/dist"
# 版本唯一来源是 manifest：面板包不带 VERSION 文件（两个来源会漂移）
VERSION="$(grep -E '^version' "$SRC/manifest" | head -1 | sed 's/.*=\s*//' | tr -d '[:space:]')"

green() { printf '\033[32m%s\033[0m\n' "$*"; }
red() { printf '\033[31m%s\033[0m\n' "$*" >&2; }

# ── 0. 自检 ────────────────────────────────────────────────
python3 "$ROOT/scripts/check-icon.py" --src "$SRC" --name harness-panel \
  --apps-root /vol1/1000/work/deepseek/fnos-apps --extra "$ROOT/fnpack" || exit 1
# 图标必须 644：应用中心按 /var/apps/<app>/ICON.PNG 取图，若以组身份读取，
# 组无读权限（如 705）会退回默认占位图（1.0.0 首次装机踩过）
for _icon in "$SRC/ICON.PNG" "$SRC/ICON_256.PNG"; do
  [ -f "$_icon" ] && chmod 644 "$_icon"
done
[ -n "$VERSION" ] || { red "✗ manifest 里没有 version 行"; exit 1; }
# 面板走独立的 1.x 版本线，防止误把本体的 0.2.1-rc.x 写进来
case "$VERSION" in
  1.*.*) ;;
  *) red "✗ 面板版本应为 1.x（独立版本线），实际 $VERSION"; exit 1 ;;
esac
[ -f "$SRC/app/fnshell.py" ] || { red "✗ 缺少 app/fnshell.py"; exit 1; }
[ -d "$SRC/app/panel_dist" ] || { red "✗ 缺少 app/panel_dist（先跑前端构建）"; exit 1; }

# wizard 必须是真的配置：飞牛应用中心解析空数组会空指针（APP_INSTALL_FAILED_PKG_EXCEPTION，
# CLI 侧 code 10111 invalid memory address）。曾经留过 []\n，装不上就是这个原因。
python3 - "$SRC/wizard/install" "$SRC/wizard/uninstall" <<'PY'
import json, os, sys

def load(path, label, required_field):
    if not os.path.exists(path):
        raise SystemExit(f"✗ 缺少 {label}（{path}）")
    size = os.path.getsize(path)
    if size <= 100:
        raise SystemExit(f"✗ {label} 只有 {size} 字节，必须是真实配置（>100 字节），不能是空数组")
    try:
        data = json.load(open(path, encoding="utf-8"))
    except Exception as exc:
        raise SystemExit(f"✗ {label} 不是合法 JSON：{exc}")
    if not isinstance(data, list) or not data:
        raise SystemExit(f"✗ {label} 必须是【非空】JSON 数组，实际 {type(data).__name__}")
    fields = []
    for step in data:
        if not isinstance(step, dict):
            continue
        for item in step.get("items") or []:
            if isinstance(item, dict) and item.get("field"):
                fields.append(item["field"])
    if required_field not in fields:
        raise SystemExit(f"✗ {label} 缺少必需字段 {required_field}，实际字段 {fields}")
    return size, len(data), fields

s1, n1, f1 = load(sys.argv[1], "wizard/install", "wizard_data_library_type")
s2, n2, f2 = load(sys.argv[2], "wizard/uninstall", "wizard_data_action")
print(f"✓ wizard 校验通过：install {s1}B（{n1} 步，字段 {f1}）、uninstall {s2}B（{n2} 步，字段 {f2}）")
PY

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
