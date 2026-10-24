#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════
# dsh-fnos 一键构建（应用层自维护，运行时跟官方 npm 包）
#
# 工具链（版本固定，勿随意升级）：
#   Go   1.23.12  → /vol1/1000/work/deepseek/tools/go/bin/go（不在系统 PATH）
#   Node v24.15.0 / npm 11.12.1 → /var/apps/nodejs_v24/target/bin
#   fnpack 1.2.3  → 仓库内 fnpack/fnpack-1.2.3-linux-amd64
#
# 目录约定：
#   源码树  /vol1/1000/work/deepseek/fnos-apps/deepseek.harness/src    （本脚本所在仓库）
#   构建树  /vol1/1000/work/deepseek/fnos-apps/deepseek.harness/build  （runtime/node_modules、二进制）
#   产物    /vol1/1000/work/deepseek/fnos-apps/deepseek.harness/dist/*.fpk
#
# ⚠️ 硬红线（务必遵守）：
#   绝不在以下目录（含其任何子目录、以及 .bak/.old 之类兄弟目录）里做任何
#   npm/pnpm install、前端构建或 go build —— 那里有正在运行的生产进程：
#     /vol1/@appdata/deepseek.harness      （dsh-runtime / dsh-data / secrets / profile / bot 全在里面）
#     /var/apps/deepseek.harness
#     /vol1/@appcenter/deepseek.harness
#     /vol1/@apphome|@appconf|@appmeta|@apptemp/deepseek.harness
#   本脚本开头有目录守卫，命中红线路径直接退出。
#
# 用法：
#   ./scripts/build.sh                 # 全量：校验 → runtime（复用/重建）→ 前端 → Go → 打 fpk
#   ./scripts/build.sh --runtime       # 强制重建 runtime tar（npm install 到构建树）
#   ./scripts/build.sh --skip-frontend # 跳过前端构建（复用已有 dist）
# ═══════════════════════════════════════════════════════════════════════════
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APPDIR="$(cd "${ROOT}/.." && pwd)"
BUILD="${DSH_BUILD_DIR:-${APPDIR}/build}"
TOOLS="/vol1/1000/work/deepseek/tools"
NODE_BIN="/var/apps/nodejs_v24/target/bin"
GO_BIN="${TOOLS}/go/bin"
DIST="${APPDIR}/dist"
APP_VER="$(tr -d '[:space:]' < "${ROOT}/VERSION")"
RT_VER="$(tr -d '[:space:]' < "${ROOT}/RUNTIME_VERSION")"

FORCE_RUNTIME=0
SKIP_FRONTEND=0
for arg in "$@"; do
  case "$arg" in
    --runtime) FORCE_RUNTIME=1 ;;
    --skip-frontend) SKIP_FRONTEND=1 ;;
    *) echo "未知参数：$arg" >&2; exit 1 ;;
  esac
done

# ── 目录守卫：绝不碰生产目录 ──────────────────────────────────────────
# 生产目录不止 dsh-runtime / dsh-data：secrets、profile、bot、日志、缓存全在
# /vol1/@appdata/deepseek.harness 下，还有 @appdata 的 .bak/.old 之类兄弟目录，
# 因此按"整棵树 + 通配兄弟"拦截，而不是只列两个子目录。
guard() {
  local target="$1"
  for forbidden in "/vol1/@appdata/deepseek.harness" \
                   "/var/apps/deepseek.harness" \
                   "/vol1/@appcenter/deepseek.harness" \
                   "/vol1/@apphome/deepseek.harness" \
                   "/vol1/@appconf/deepseek.harness" \
                   "/vol1/@appmeta/deepseek.harness" \
                   "/vol1/@apptemp/deepseek.harness"; do
    case "$target" in
      "$forbidden"|"$forbidden"/*|"$forbidden".*|"$forbidden".*/*)
        echo "✗ 拒绝在受保护目录内构建：$target" >&2
        echo "  生产进程正在这些目录上运行，构建必须全部在 ${ROOT} / ${BUILD} 内完成。" >&2
        exit 1
        ;;
    esac
  done
}
guard "${ROOT}"
guard "${BUILD}"
guard "$(pwd)"

export PATH="${NODE_BIN}:${GO_BIN}:${PATH}"
export GOPATH="${TOOLS}/gopath"
export GOCACHE="${TOOLS}/gocache"
export GOMODCACHE="${TOOLS}/gopath/pkg/mod"
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export GOSUMDB="${GOSUMDB:-sum.golang.google.cn}"
export npm_config_registry="${npm_config_registry:-https://registry.npmmirror.com}"

mkdir -p "${BUILD}" "${DIST}" "${BUILD}/bin"

log() { printf '\n\033[1m── %s ──\033[0m\n' "$*"; }

log "0/6 工具链"
go version
node -v
npm -v
echo "应用版本 ${APP_VER} / 运行时 ${RT_VER}"

log "1/6 版本与图标自检"
python3 "${ROOT}/scripts/check-version.py" --root "${ROOT}" \
  --runtime-dir "${BUILD}/runtime" --tar "${BUILD}/deepseek-harness.tar.gz" 2>/dev/null \
  || python3 "${ROOT}/scripts/check-version.py" --root "${ROOT}" --runtime-dir "${BUILD}/runtime"
python3 "${ROOT}/scripts/check-icon.py" --src "${ROOT}/fnpack" --name "deepseek.harness" \
  --apps-root /vol1/1000/work/deepseek/fnos-apps

# ── 2) 运行时 tar：复用或重建 ────────────────────────────────────────
log "2/6 运行时（@deepseek-ai/dsh@${RT_VER}）"
TAR="${BUILD}/deepseek-harness.tar.gz"
need_runtime=0
if [ "${FORCE_RUNTIME}" = "1" ] || [ ! -f "${TAR}" ] || [ ! -d "${BUILD}/runtime/node_modules" ]; then
  need_runtime=1
elif [ "$(tr -d '[:space:]' < "${BUILD}/.version" 2>/dev/null || echo '')" != "${RT_VER}" ]; then
  need_runtime=1
fi
if [ "${need_runtime}" = "1" ]; then
  echo "重建运行时工程（只在构建树内 npm install）"
  mkdir -p "${BUILD}/runtime"
  cat > "${BUILD}/runtime/package.json" <<JSON
{
  "name": "dsh-fnos-runtime",
  "version": "1.0.0",
  "private": true,
  "description": "DeepSeek Harness runtime for fnOS",
  "dependencies": {
    "@deepseek-ai/dsh": "${RT_VER}"
  }
}
JSON
  ( cd "${BUILD}/runtime" && npm install --no-audit --no-fund )
  echo "${RT_VER}" > "${BUILD}/.version"
  rm -f "${TAR}"
  ( cd "${BUILD}/runtime" && tar --numeric-owner -czf "${TAR}" package.json package-lock.json node_modules )
else
  echo "复用已有运行时 tar（$(du -h "${TAR}" | cut -f1)）"
fi
python3 "${ROOT}/scripts/check-version.py" --root "${ROOT}" \
  --runtime-dir "${BUILD}/runtime" --tar "${TAR}"

# ── 3) 前端 ──────────────────────────────────────────────────────────
log "3/6 管理前端"
if [ "${SKIP_FRONTEND}" = "1" ] && [ -f "${ROOT}/frontend/dist/index.html" ]; then
  echo "跳过（复用已有 dist）"
else
  ( cd "${ROOT}/frontend" && npm ci --no-audit --no-fund && npm run build )
fi
[ -f "${ROOT}/frontend/dist/index.html" ] || { echo "✗ 前端产物缺失" >&2; exit 1; }

# ── 4) Go 二进制（前端已 embed 进去） ────────────────────────────────
log "4/6 Go 二进制"
( cd "${ROOT}" && go build -trimpath -ldflags="-s -w" -o "${BUILD}/bin/deepseek.harness" . )
ls -la "${BUILD}/bin/deepseek.harness"

# ── 5) 组装 fnpack 目录 ──────────────────────────────────────────────
log "5/6 组装 fnpack_x86"
STAGE="${BUILD}/fnpack_x86"
rm -rf "${STAGE}"
mkdir -p "${STAGE}/app/bin"
cp -r "${ROOT}"/fnpack/. "${STAGE}/"
cp "${BUILD}/bin/deepseek.harness" "${STAGE}/app/bin/deepseek.harness"
cp "${TAR}" "${STAGE}/app/deepseek-harness.tar.gz"
echo "${RT_VER}" > "${STAGE}/app/.version"
# 只保留 x86 的 skill 二进制（ARM 版不在本包范围内）
rm -f "${STAGE}/app/bin/skill/bin/trim-cli-linux-arm64"
# manifest 版本以 VERSION 为准
sed -i "s/^version.*=.*/version               = ${APP_VER}/" "${STAGE}/manifest"

# 包内容自检
for f in app/bin/deepseek.harness app/deepseek-harness.tar.gz app/.version app/ui/config manifest cmd/main; do
  [ -e "${STAGE}/$f" ] || { echo "✗ 缺少 $f" >&2; exit 1; }
done
grep -q "version               = ${APP_VER}" "${STAGE}/manifest" || { echo "✗ manifest 版本未写入" >&2; exit 1; }
grep -q "distributor           = xiguan99" "${STAGE}/manifest" || { echo "✗ manifest 分发者未替换" >&2; exit 1; }
grep -rq 'yuexps' "${STAGE}/manifest" && { echo "✗ manifest 里仍有上游作者痕迹" >&2; exit 1; } || true
echo "✓ 包内容自检通过"

# ── 6) fnpack 打包 ───────────────────────────────────────────────────
log "6/6 打 fpk"
( cd "${STAGE}" && chmod +x ./fnpack-1.2.3-linux-amd64 && ./fnpack-1.2.3-linux-amd64 build )
OUT_SRC="$(find "${STAGE}" -maxdepth 1 -name '*.fpk' | head -1)"
[ -n "${OUT_SRC}" ] || { echo "✗ fnpack 未产出 fpk" >&2; exit 1; }
OUT="${DIST}/deepseek.harness_v${APP_VER}_x86.fpk"
mv -f "${OUT_SRC}" "${OUT}"

echo
echo "════ 产物 ════"
ls -la "${OUT}"
sha256sum "${OUT}"
echo "运行时 ${RT_VER}（tar $(du -h "${TAR}" | cut -f1)，sha256 $(sha256sum "${TAR}" | cut -d' ' -f1)）"
