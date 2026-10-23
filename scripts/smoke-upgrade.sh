#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════
# 原地升级冒烟：先装 0.4.1，再覆盖升级到当前包（默认 0.4.1.1），验数据不丢。
#
# 为什么单独一个脚本：接管应用层后"升级不丢数据"是第一优先级——用户的生产
# 数据（会话/配置/插件/记忆）全在 ${TRIM_PKGVAR}，而 fnOS 覆盖安装只会替换
# 应用目录。这个脚本在隔离目录里把整条升级路径走一遍。
#
# 隔离约定（绝不碰生产）：
#   工作目录 /tmp/dshupgrade（每次重建）
#   端口     2398（DSH 后端）/ 2399（反代）—— 生产占 2298/2299
#   0.4.1 的载荷从现装应用只读复制，不启动、不写生产目录
#
# 用法：
#   ./scripts/smoke-upgrade.sh              # 跑完自动清理
#   ./scripts/smoke-upgrade.sh --keep       # 保留工作目录排查
# ═══════════════════════════════════════════════════════════════════════════
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BUILD="${DSH_BUILD_DIR:-/vol1/1000/work/deepseek/dsh-build}"
APP_VER="$(tr -d '[:space:]' < "${ROOT}/VERSION")"
RT_VER="$(tr -d '[:space:]' < "${ROOT}/RUNTIME_VERSION")"
NEW_FPK="${BUILD}/dist/deepseek.harness_v${APP_VER}_x86.fpk"

WORK=/tmp/dshupgrade
APPDEST="${WORK}/appdest"
VAR="${WORK}/var"
PK="${WORK}/pkg"           # 新包解出来的 cmd/ 等
OLD_SRC="/vol1/@appcenter/deepseek.harness"   # 现装 0.4.1 载荷（只读）
OLD_CMD="/var/apps/deepseek.harness/cmd"      # 现装 0.4.1 生命周期脚本（只读）
PORT_SERVER=2398
PORT_PROXY=2399
KEEP=0
[ "${1:-}" = "--keep" ] && KEEP=1

PASS=0; FAIL=0
ok()   { echo "  ✓ $*"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $*"; FAIL=$((FAIL+1)); }
step() { printf '\n\033[1m── %s ──\033[0m\n' "$*"; }

cleanup() {
  if [ -x "${PK}/cmd/main" ]; then
    TRIM_APPDEST="${APPDEST}" TRIM_PKGVAR="${VAR}" bash "${PK}/cmd/main" stop >/dev/null 2>&1 || true
  fi
  [ "${KEEP}" = "1" ] || rm -rf "${WORK}"
}
trap cleanup EXIT

envrun() {
  env TRIM_APPDEST="${APPDEST}" TRIM_PKGVAR="${VAR}" TRIM_APPVER="${1}" \
      DATA_LIBRARY_PATH="${VAR}" wizard_data_library_type=data wizard_dsh_run_user=root \
      "${@:2}"
}

wait_http() { # wait_http <url> <秒>
  local url="$1" limit="${2:-90}" i=0
  while [ "$i" -lt "$limit" ]; do
    [ "$(curl -s -o /dev/null -w '%{http_code}' "$url" 2>/dev/null)" = "200" ] && return 0
    sleep 1; i=$((i+1))
  done
  return 1
}

api() { # api <path>
  curl -s --unix-socket "${APPDEST}/web.sock" "http://localhost/app/deepseek-harness${1}"
}

json_field() { # 响应结构 {"code":0,"message":"success","data":{...}}
  python3 -c "import sys,json;d=json.load(sys.stdin);print((d.get('data') or {}).get('$1',''))" 2>/dev/null
}

inventory() { # 数据目录文件清单（排除运行期会变的日志/sqlite 临时文件）
  find "${VAR}" -type f \( -path '*/dsh-runtime/*' -o -path '*/npm-cache/*' -o -path '*/pnpm-*/*' \) -prune -o \
       -type f -print 2>/dev/null | grep -vE 'harness\.log|info\.log|app\.pid|harness\.pid|\.sqlite-(wal|shm)$' \
       | sed "s|^${VAR}/||" | sort
}

step "0 前置检查"
[ -f "${NEW_FPK}" ] || { echo "✗ 找不到新包 ${NEW_FPK}，先跑 scripts/build.sh" >&2; exit 1; }
[ -d "${OLD_SRC}" ] || { echo "✗ 找不到现装载荷 ${OLD_SRC}" >&2; exit 1; }
echo "  新包 ${NEW_FPK}（$(stat -c%s "${NEW_FPK}") B）"
echo "  旧包载荷 ${OLD_SRC}（manifest $(grep -E '^version' /var/apps/deepseek.harness/manifest | head -1 | sed 's/.*= *//')）"
ss -ltn 2>/dev/null | grep -qE ":(${PORT_SERVER}|${PORT_PROXY})\b" && { echo "✗ 端口 ${PORT_SERVER}/${PORT_PROXY} 被占用" >&2; exit 1; }

step "1 装 0.4.1（隔离目录，只读复制现装载荷）"
rm -rf "${WORK}"; mkdir -p "${APPDEST}" "${VAR}" "${PK}" "${WORK}/oldcmd"
cp -a "${OLD_SRC}/." "${APPDEST}/" 2>/dev/null
rm -f "${APPDEST}/web.sock"
cp -a "${OLD_CMD}/." "${WORK}/oldcmd/"
envrun "0.4.1" bash "${WORK}/oldcmd/install_callback" >/dev/null 2>&1
echo "  install_callback exit=$?"
[ -d "${VAR}/dsh-runtime" ] && [ -d "${VAR}/dsh-data" ] && ok "数据目录骨架已建（dsh-runtime / dsh-data）" || bad "数据目录骨架缺失"

# 端口钉在测试值上（生产 2298/2299 被占）
cat > "${VAR}/config.json" <<JSON
{
  "server_port": ${PORT_SERVER},
  "proxy_port": ${PORT_PROXY},
  "npm_registry": "https://registry.npmjs.org",
  "update_channel": "latest",
  "access_mode": "port",
  "data_library_path": "${VAR}",
  "enable_builtin_skill": true,
  "version": "${RT_VER}",
  "last_run_state": "running"
}
JSON

envrun "0.4.1" bash "${WORK}/oldcmd/main" start >/dev/null 2>&1
wait_http "http://127.0.0.1:${PORT_PROXY}/" 120 && ok "0.4.1 启动，反代 ${PORT_PROXY} 可访问" || bad "0.4.1 启动失败"

# 造一份"用户数据"：会话目录 + 插件配置 + 标记文件
mkdir -p "${VAR}/dsh-data/sessions/demo-session" "${VAR}/dsh-data/plugin-config"
echo '{"title":"升级前的会话","messages":["hello"]}' > "${VAR}/dsh-data/sessions/demo-session/session.json"
echo '{"plugin":"demo","enabled":true}' > "${VAR}/dsh-data/plugin-config/demo.json"
echo '{"seeded":"before-upgrade"}' > "${VAR}/dsh-data/upgrade-marker.json"
MARK_SHA_BEFORE="$(sha256sum "${VAR}/dsh-data/upgrade-marker.json" | cut -d' ' -f1)"
SESSION_SHA_BEFORE="$(sha256sum "${VAR}/dsh-data/sessions/demo-session/session.json" | cut -d' ' -f1)"
INV_BEFORE="$(inventory | wc -l)"
echo "  造数据完成：dsh-data 文件数 ${INV_BEFORE}，marker sha ${MARK_SHA_BEFORE:0:12}…"

CFG_SERVER="$(api /api/config | json_field server_port)"
CFG_PROXY="$(api /api/config | json_field proxy_port)"
[ "${CFG_SERVER}" = "${PORT_SERVER}" ] && ok "0.4.1 /api/config server_port=${CFG_SERVER}" || bad "0.4.1 /api/config server_port=${CFG_SERVER:-空}"
[ "${CFG_PROXY}" = "${PORT_PROXY}" ] && ok "0.4.1 /api/config proxy_port=${CFG_PROXY}" || bad "0.4.1 /api/config proxy_port=${CFG_PROXY:-空}"
TOKEN_LINES="$(grep -c 'token=' "${VAR}/harness.log" 2>/dev/null || true)"; TOKEN_LINES="${TOKEN_LINES:-0}"
[ "${TOKEN_LINES}" -ge 1 ] && ok "0.4.1 已抓到 DSH launch token（harness.log 命中 ${TOKEN_LINES} 行）" || bad "0.4.1 未抓到 token"
envrun "0.4.1" bash "${WORK}/oldcmd/main" stop >/dev/null 2>&1
sleep 2
[ -S "${APPDEST}/web.sock" ] && bad "停止后 web.sock 仍存在" || ok "0.4.1 停止，web.sock 已清理"

step "2 覆盖升级到 ${APP_VER}（只换应用目录，保留数据目录）"
mkdir -p "${WORK}/extract" && tar -xzf "${NEW_FPK}" -C "${WORK}/extract"
tar -xzf "${WORK}/extract/app.tgz" -C "${APPDEST}"        # 覆盖载荷（bin/ui/tar/.version）
mkdir -p "${PK}/cmd"; cp -a "${WORK}/extract/cmd/." "${PK}/cmd/"
envrun "${APP_VER}" bash "${PK}/cmd/install_callback" >/dev/null 2>&1
echo "  install_callback exit=$?"

[ "$(tr -d '[:space:]' < "${APPDEST}/.version")" = "${RT_VER}" ] && ok "载荷已换成新包（.version=${RT_VER}）" || bad "载荷 .version 不是 ${RT_VER}"
grep -q "version               = ${APP_VER}" "${WORK}/extract/manifest" && ok "新包 manifest 版本 ${APP_VER}" || bad "新包 manifest 版本不对"

MARK_SHA_AFTER="$(sha256sum "${VAR}/dsh-data/upgrade-marker.json" 2>/dev/null | cut -d' ' -f1)"
SESSION_SHA_AFTER="$(sha256sum "${VAR}/dsh-data/sessions/demo-session/session.json" 2>/dev/null | cut -d' ' -f1)"
INV_AFTER="$(inventory | wc -l)"
[ "${MARK_SHA_AFTER}" = "${MARK_SHA_BEFORE}" ] && ok "升级后标记文件未变（sha ${MARK_SHA_AFTER:0:12}…）" || bad "标记文件被改动/丢失"
[ "${SESSION_SHA_AFTER}" = "${SESSION_SHA_BEFORE}" ] && ok "升级后会话文件未变" || bad "会话文件被改动/丢失"
[ "${INV_AFTER}" -ge "${INV_BEFORE}" ] && ok "数据目录文件数未减少（${INV_BEFORE} → ${INV_AFTER}）" || bad "数据目录文件数减少（${INV_BEFORE} → ${INV_AFTER}）"
CFG_FILE_SERVER="$(python3 -c "import json;print(json.load(open('${VAR}/config.json')).get('server_port',''))" 2>/dev/null)"
CFG_FILE_PROXY="$(python3 -c "import json;print(json.load(open('${VAR}/config.json')).get('proxy_port',''))" 2>/dev/null)"
CFG_FILE_REG="$(python3 -c "import json;print(json.load(open('${VAR}/config.json')).get('npm_registry',''))" 2>/dev/null)"
[ "${CFG_FILE_SERVER}" = "${PORT_SERVER}" ] && [ "${CFG_FILE_PROXY}" = "${PORT_PROXY}" ] \
  && ok "升级后 config.json 端口仍是 ${CFG_FILE_SERVER}/${CFG_FILE_PROXY}（用户配置未被重置）" \
  || bad "升级后 config.json 端口变成 ${CFG_FILE_SERVER:-空}/${CFG_FILE_PROXY:-空}"
[ "${CFG_FILE_REG}" = "https://registry.npmjs.org" ] \
  && ok "升级后 npm_registry 自定义值仍在（配置未丢）" \
  || bad "升级后 npm_registry 变成 ${CFG_FILE_REG:-空}"

step "3 升级后启动与功能验证"
envrun "${APP_VER}" bash "${PK}/cmd/main" start >/dev/null 2>&1
wait_http "http://127.0.0.1:${PORT_PROXY}/" 120 && ok "升级后启动成功，反代 ${PORT_PROXY} 可访问" || bad "升级后启动失败"
[ -S "${APPDEST}/web.sock" ] && ok "web.sock 已重建" || bad "web.sock 未重建"

ADMIN_CODE="$(curl -s -o /tmp/admin.html -w '%{http_code}' --unix-socket "${APPDEST}/web.sock" "http://localhost/app/deepseek-harness/")"
[ "${ADMIN_CODE}" = "200" ] && ok "管理面板 200（$(stat -c%s /tmp/admin.html) B）" || bad "管理面板 ${ADMIN_CODE}"

CFG_SERVER2="$(api /api/config | json_field server_port)"
CFG_PROXY2="$(api /api/config | json_field proxy_port)"
[ "${CFG_SERVER2}" = "${PORT_SERVER}" ] && ok "升级后 /api/config server_port=${CFG_SERVER2}（沿用用户配置，未被重置为 2298）" || bad "升级后 server_port=${CFG_SERVER2:-空}"
[ "${CFG_PROXY2}" = "${PORT_PROXY}" ] && ok "升级后 /api/config proxy_port=${CFG_PROXY2}（未被重置为 2299）" || bad "升级后 proxy_port=${CFG_PROXY2:-空}"

GW_CODE="$(curl -s -o /tmp/gw.html -w '%{http_code}' --unix-socket "${APPDEST}/web.sock" "http://localhost/app/deepseek-harness/fngateway/")"
GW_SIZE="$(stat -c%s /tmp/gw.html 2>/dev/null || echo 0)"
[ "${GW_CODE}" = "200" ] && [ "${GW_SIZE}" -gt 1000 ] && ok "WebUI 网关 200（${GW_SIZE} B，说明面板拿到了 token 并成功代持会话）" || bad "WebUI 网关 ${GW_CODE} / ${GW_SIZE} B"

TOKEN_LINES2="$(grep -c 'token=' "${VAR}/harness.log" 2>/dev/null || true)"; TOKEN_LINES2="${TOKEN_LINES2:-0}"
[ "${TOKEN_LINES2}" -ge 2 ] && ok "升级后再次抓到 token（日志累计 ${TOKEN_LINES2} 行）" || bad "升级后未抓到 token"

UP_CODE="$(curl -s -o /tmp/up.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  --unix-socket "${APPDEST}/web.sock" -d '{"action":"upgrade"}' \
  "http://localhost/app/deepseek-harness/api/action")"
grep -q '在线升级运行时已关闭' /tmp/up.json 2>/dev/null && ok "在线升级仍被拦截（${UP_CODE}，提示文案在）" || bad "在线升级拦截异常（${UP_CODE}）"

step "4 停与卸"
envrun "${APP_VER}" bash "${PK}/cmd/main" stop >/dev/null 2>&1
sleep 2
if ss -ltn 2>/dev/null | grep -qE ":(${PORT_SERVER}|${PORT_PROXY})\b"; then bad "停止后端口仍占用"; else ok "停止后端口已释放"; fi
[ -S "${APPDEST}/web.sock" ] && bad "停止后 web.sock 仍存在" || ok "停止后 web.sock 已清理"
envrun "${APP_VER}" bash "${PK}/cmd/uninstall_callback" >/dev/null 2>&1
echo "  uninstall_callback exit=$?"
[ -d "${VAR}/dsh-data" ] && ok "卸载回调不动数据目录（用户数据保留，符合 fnOS 卸载语义）" || bad "卸载回调删了数据目录"

printf '\n════ 冒烟结果：%d 通过 / %d 失败 ════\n' "${PASS}" "${FAIL}"
[ "${FAIL}" -eq 0 ] || exit 1
