#!/usr/bin/env bash
# =====================================================================
#  ToShell 开发管理脚本（Linux / macOS）—— 非部署入口
#
#  两种启动方式：
#    deploy（默认）  构建**带内嵌前端**的服务端并启动，只起一个进程。
#                    与发布包的行为一致：浏览器直接开 http://<host>:<api_port>。
#    dev             构建**不带内嵌前端**的服务端（纯 API），同时起 Vite 开发
#                    服务器。改 web/src/** 即时热更新，界面走 :3002，API 走 :api_port。
#                    开发时用这种方式：唯一界面就是热更新那个，不会出现"改了前端
#                    但端口上还是旧界面"的困惑。
#
#    启动：./Toshell.sh start            部署方式
#          ./Toshell.sh start --dev      开发方式
#          ./Toshell.sh stop             两种都停（服务端 + Vite）
#          ./Toshell.sh clean            清理构建产物、日志、运行时状态
#
#  其余命令：build / sync / config / help（不带参数进入交互式菜单）。
#
#  部署（解压发布包后安装并运行）请用发布包内的 install.sh —— 它会在没有 Go 的
#  机器上按需安装依赖，本脚本不做这件事。
#
#  为什么产物落在 release/ 而不是仓库根：
#    release/ 是"复刻发布包布局"的目录。服务端解析植入端模板时按
#    【配置 implant.template_dir → 环境变量 TOSHELL_IMPLANT_TEMPLATE_DIR →
#      exe 同目录 implant/ → exe 同目录 internal/server/builder/implant →
#      当前工作目录 internal/server/builder/implant】顺序回退。
#    把 toserver 放在 release/ 下，exe 同目录就有 implant/，于是**本地开发与
#    发布包走完全相同的模板解析路径**，不会出现"本地能跑、发布包失效"。
# =====================================================================
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_BIN="$ROOT/release/toserver"
CONFIG="$ROOT/release/configs/server.yaml"
CONFIG_EXAMPLE="$ROOT/release/configs/server.yaml.example"
LOG_OUT="$ROOT/release/server.log"
LOG_ERR="$ROOT/release/server.err.log"
WEB_DIST="$ROOT/web/dist"
WEB_EMBED="$ROOT/cmd/server/webdist"
RELEASE_DIR="$ROOT/release"
IMPLANTS_DIR="$ROOT/release/implants"

# 运行时状态（开发方式用）：构建方式标记 + Vite 的 PID 与日志。
# 构建方式标记用于判断 release/toserver 是否与本次启动方式匹配 —— 两种方式的
# 二进制不同（dev 不嵌入前端），不匹配就得重建，否则你以为在开发态、其实跑的是
# 带内嵌前端的部署态二进制。
BUILD_MODE_FILE="$RELEASE_DIR/.build-mode"
VITE_PID="$RELEASE_DIR/vite.pid"
VITE_LOG="$RELEASE_DIR/vite.log"

if [ -t 1 ]; then
  RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; NC='\033[0m'
else
  RED=''; GREEN=''; YELLOW=''; CYAN=''; NC=''
fi

info() { printf "${CYAN}[信息]${NC} %s\n" "$*"; }
ok()   { printf "${GREEN}[成功]${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}[警告]${NC} %s\n" "$*"; }
err()  { printf "${RED}[错误]${NC} %s\n" "$*" >&2; }

# 返回正在运行的服务端进程 PID 列表
running_pids() { pgrep -x toserver 2>/dev/null || true; }
is_running() { [ -n "$(running_pids)" ]; }

# 从配置读取 api_port（默认 18081）
api_port() {
  local p
  p=$(sed -n 's/^[[:space:]]*api_port:[[:space:]]*\([0-9]*\).*/\1/p' "$CONFIG" 2>/dev/null | head -n1)
  echo "${p:-18081}"
}

# Vite 监听端口：从 vite.config.ts 读，保持单一来源（不要在这里写死）
vite_port() {
  local p
  p=$(sed -n 's/.*port:[[:space:]]*\([0-9]\{2,\}\).*/\1/p' "$ROOT/web/vite.config.ts" 2>/dev/null | head -n1)
  echo "${p:-3002}"
}

# ── 构建与启动方式 ───────────────────────────────────────────────────

build_mode() { cat "$BUILD_MODE_FILE" 2>/dev/null || echo ""; }

# ensure_build <dev|deploy>：保证 release/toserver 是按该方式构建的，否则询问重建。
ensure_build() {
  local want="$1" have
  if [ ! -f "$SERVER_BIN" ]; then
    warn "未找到构建产物: $SERVER_BIN"
    local ans
    read -r -p "是否现在以 $want 方式构建？[Y/n] " ans || ans="n"
    case "$ans" in
      ""|[Yy]*) do_build "$want" || return 1 ;;
      *) err "用户拒绝构建，终止启动"; return 1 ;;
    esac
    return 0
  fi

  have=$(build_mode)
  [ "$have" = "$want" ] && return 0

  if [ -z "$have" ]; then
    warn "无法判定现有产物的构建方式（缺 $BUILD_MODE_FILE）"
  else
    warn "现有产物是 $have 方式构建的，与本次启动方式（$want）不符"
  fi
  warn "两种方式的二进制不同：dev 不嵌入前端（界面走 Vite），deploy 嵌入前端"
  local ans
  read -r -p "是否重新构建为 $want 方式？[Y/n] " ans || ans="n"
  case "$ans" in
    ""|[Yy]*) do_build "$want" || return 1 ;;
    *) warn "继续使用现有产物 —— 实际运行方式可能与本次启动方式不一致" ;;
  esac
  return 0
}

# ── Vite 开发服务器 ─────────────────────────────────────────────────

# 按端口找 Vite 的 PID。
# 不依赖 PID 文件：npm run dev 会再 fork 出 vite 子进程，只杀 npm 的 PID 会留下
# vite 继续占着端口（本机实测过）。按端口定位才可靠。
vite_pids() {
  local p; p=$(vite_port)
  if command -v lsof >/dev/null 2>&1; then
    lsof -ti "tcp:$p" 2>/dev/null
  elif command -v ss >/dev/null 2>&1; then
    ss -lptnH "sport = :$p" 2>/dev/null | grep -oE 'pid=[0-9]+' | cut -d= -f2 | sort -u
  fi
}

vite_running() { [ -n "$(vite_pids)" ]; }

start_vite() {
  local p; p=$(vite_port)
  if vite_running; then
    warn "Vite 已在运行（PID: $(vite_pids | tr '\n' ' ')）"
    return 0
  fi
  if [ ! -d "$ROOT/web/node_modules" ]; then
    err "缺少 web/node_modules，请先在 web/ 下执行 npm ci"
    return 1
  fi
  if ! command -v npm >/dev/null 2>&1; then
    err "未找到 npm（需要 Node.js >= 20）"
    return 1
  fi

  info "启动 Vite 开发服务器（端口 $p，日志 $VITE_LOG）..."
  ( cd "$ROOT/web" && nohup npm run dev >"$VITE_LOG" 2>&1 & echo $! >"$VITE_PID" )
  sleep 3

  if vite_running; then
    ok "Vite 已启动（PID: $(vite_pids | tr '\n' ' ')）"
    info "  前端（热更新）: http://127.0.0.1:$p"
    info "  后端纯 API:     http://127.0.0.1:$(api_port)"
    info "  /api 由 Vite 代理到后端（改代理目标用环境变量 VITE_PROXY_TARGET）"
  else
    err "Vite 启动失败，见日志: $VITE_LOG"
    return 1
  fi
}

# 停止 Vite。未在运行时静默返回 —— stop 会无条件调用它。
stop_vite() {
  vite_running || { rm -f "$VITE_PID"; return 0; }
  info "停止 Vite（PID: $(vite_pids | tr '\n' ' ')）..."
  kill $(vite_pids) 2>/dev/null
  sleep 2
  if vite_running; then
    kill -9 $(vite_pids) 2>/dev/null
    sleep 1
  fi
  rm -f "$VITE_PID"
  if vite_running; then
    err "Vite 未能停止，仍占用端口 $(vite_port)（PID: $(vite_pids | tr '\n' ' ')）"
    return 1
  fi
  ok "Vite 已停止"
}

# ── 构建 ≠ 生效：判断正在运行的进程是不是"构建前的旧二进制" ──────────────
# 历史坑：构建成功后服务没重启，进程一直在内存里跑旧的 toserver，
# 表现为"构建过了，但 Web 控制台和接口毫无变化"。所以必须能识别出这种状态。
proc_start_epoch() {
  local pid="$1" s
  s=$(ps -o lstart= -p "$pid" 2>/dev/null) || return 1
  [ -n "$s" ] || return 1
  if date -d "$s" +%s >/dev/null 2>&1; then
    date -d "$s" +%s                                  # GNU date
  else
    date -j -f "%a %b %d %T %Y" "$s" +%s 2>/dev/null   # BSD / macOS date
  fi
}

is_stale_run() {
  is_running || return 1
  [ -f "$SERVER_BIN" ] || return 1
  local pid bin_ts proc_ts
  pid=$(running_pids | head -n1)
  [ -n "$pid" ] || return 1
  bin_ts=$(stat -c %Y "$SERVER_BIN" 2>/dev/null || stat -f %m "$SERVER_BIN" 2>/dev/null)
  proc_ts=$(proc_start_epoch "$pid") || return 1
  [ -n "$bin_ts" ] && [ -n "$proc_ts" ] || return 1
  # 2 秒容差：刚构建完就启动时两者几乎相同
  [ "$bin_ts" -gt "$((proc_ts + 2))" ]
}

restart_for_stale() {
  is_stale_run || return 1
  warn "服务运行的是【构建前】的旧二进制（PID: $(running_pids | tr '\n' ' ')）"
  info "  进程启动于: $(ps -o lstart= -p "$(running_pids | head -n1)" 2>/dev/null | tr -s ' ')"
  info "  产物生成于: $(date -r "$SERVER_BIN" '+%Y-%m-%d %H:%M:%S' 2>/dev/null)"
  warn "不重启的话，Web 控制台与接口仍然是旧版本。"
  local ans
  read -r -p "是否立即重启服务以加载新版本？会断开当前所有会话（植入端会在一个心跳周期内自动重连）[Y/n] " ans || ans="n"
  case "$ans" in
    ""|[Yy]*)
      do_stop || return 1
      do_start "$(build_mode | grep -q dev && echo dev || echo deploy)"
      return 0
      ;;
    *)
      info "已跳过重启。需要生效时执行: ./Toshell.sh stop  然后  ./Toshell.sh start"
      return 1
      ;;
  esac
}

# ── 构建与模板 ───────────────────────────────────────────────────────

devtool() { ( cd "$ROOT" && go run ./cmd/devtool "$@" ); }

sync_implant_template() {
  command -v go >/dev/null 2>&1 || { err "未找到 Go 工具链（需要 Go >= 1.25）"; return 1; }
  devtool sync || { err "模板同步失败"; return 1; }
}

do_build() {
  local mode="${1:-deploy}"
  info "构建服务端 + Web 前端（方式: $mode，根目录: $ROOT）"
  command -v go >/dev/null 2>&1 || { err "未找到 Go 工具链（需要 Go >= 1.25），请先安装"; return 1; }

  # dev 方式用 --no-webui：不构建前端也不嵌入（不影响 cmd/server/webdist，
  # 因此随时可以切回 deploy 而不必重新 npm build）。
  if [ "$mode" = "dev" ]; then
    devtool build --no-webui || { err "构建失败"; return 1; }
  else
    devtool build || { err "构建失败"; return 1; }
  fi

  [ -f "$SERVER_BIN" ] || { err "未生成产物: $SERVER_BIN"; return 1; }
  echo "$mode" >"$BUILD_MODE_FILE"
  ok "构建完成: $SERVER_BIN（方式 $mode）"

  if is_stale_run; then
    restart_for_stale || true
  fi
}

do_clean() {
  info "清理构建产物与日志文件..."
  if is_running; then
    warn "服务端正在运行（PID: $(running_pids | tr '\n' ' ')），正在运行的二进制可能无法删除；建议先停止服务"
  fi
  if vite_running; then
    warn "Vite 正在运行（PID: $(vite_pids | tr '\n' ' ')），先停止它"
    stop_vite || true
  fi
  rm -f "$SERVER_BIN" "$ROOT/toserver" 2>/dev/null
  rm -rf "$IMPLANTS_DIR"/* 2>/dev/null
  # release/implant{,_c} 是 build 时从模板源生成的产物（不是源码），一并清理
  rm -rf "$RELEASE_DIR/implant" "$RELEASE_DIR/implant_c" 2>/dev/null
  rm -rf "$WEB_DIST" "$WEB_EMBED" 2>/dev/null
  rm -f "$ROOT/web/tsconfig.tsbuildinfo" "$ROOT/web/tsconfig.node.tsbuildinfo" 2>/dev/null
  # 运行时状态：构建方式标记与 Vite 的 PID/日志
  rm -f "$BUILD_MODE_FILE" "$VITE_PID" "$VITE_LOG" 2>/dev/null
  rm -f "$LOG_OUT" "$LOG_ERR" "$ROOT/server.log" "$ROOT/server.err.log" 2>/dev/null
  ok "构建产物与日志已清理（node_modules 保留）"

  local ans
  read -r -p "是否同时清理数据库（SQLite 数据，sessions/tasks 等将丢失）？输入 yes 确认，其他任意键跳过: " ans || ans=""
  case "$ans" in
    [yY][eE][sS])
      if is_running; then
        info "数据库清理需要先停止服务，正在停止..."
        do_stop || { err "停止服务失败，已跳过数据库清理"; return 1; }
      fi
      rm -f "$ROOT/release/data/toshell.db" "$ROOT/release/data/toshell.db-wal" "$ROOT/release/data/toshell.db-shm" 2>/dev/null
      rm -f "$ROOT/data/toshell.db" "$ROOT/data/toshell.db-wal" "$ROOT/data/toshell.db-shm" 2>/dev/null
      ok "数据库已清理（服务重启后将自动重建空库）"
      ;;
    *)
      info "已跳过数据库清理"
      ;;
  esac
}

do_start() {
  local mode="${1:-deploy}"

  if is_running; then
    # 跑着旧二进制时不能只警告就 return —— 那正是"构建完毫无变化"的根因。
    if is_stale_run && restart_for_stale; then
      return 0
    fi
    warn "服务端已在运行（PID: $(running_pids | tr '\n' ' ')）"
  else
    ensure_build "$mode" || return 1

    # 模板目录不在就不启动：否则服务端能起来，但生成载荷时才发现没有模板
    if [ ! -d "$RELEASE_DIR/implant" ] || [ ! -f "$RELEASE_DIR/implant/main.go" ]; then
      warn "植入端模板未同步到 $RELEASE_DIR/implant（服务端将无法生成载荷）"
      sync_implant_template || { err "模板同步失败，终止启动"; return 1; }
    fi
    if [ ! -f "$CONFIG" ]; then
      if [ -f "$CONFIG_EXAMPLE" ]; then
        warn "配置不存在，已从示例生成: $CONFIG"
        cp "$CONFIG_EXAMPLE" "$CONFIG"
      else
        err "配置不存在且无示例文件: $CONFIG"
        return 1
      fi
    fi

    info "启动服务端（$mode 方式）..."
    ( cd "$RELEASE_DIR" && nohup ./toserver -config configs/server.yaml > server.log 2> server.err.log & )
    sleep 2
    if ! is_running; then
      err "启动失败，请检查日志: $LOG_ERR"
      return 1
    fi
    local port; port=$(api_port)
    ok "服务端已启动（PID: $(running_pids | tr '\n' ' ')）"
    if [ "$mode" = "deploy" ]; then
      info "Web 控制台: http://127.0.0.1:${port}"
    else
      info "后端 API: http://127.0.0.1:${port}（开发方式不嵌入前端，界面走 Vite）"
    fi
    if command -v curl >/dev/null 2>&1; then
      local code
      code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/api/v1/health" 2>/dev/null)
      if [ "$code" = "200" ]; then ok "健康检查通过（/api/v1/health → 200）"; else warn "健康检查返回 HTTP ${code:-无响应}，可查看 $LOG_ERR"; fi
    fi
  fi

  if [ "$mode" = "dev" ]; then
    start_vite || return 1
  fi
}

do_stop() {
  local rc=0
  if is_running; then
    info "发现服务端进程: $(running_pids | tr '\n' ' ')"
    info "正在停止..."
    kill $(running_pids) 2>/dev/null
    sleep 1
    if is_running; then
      kill -9 $(running_pids) 2>/dev/null
      sleep 1
    fi
    if is_running; then
      err "服务端停止失败，进程仍在: $(running_pids | tr '\n' ' ')"
      rc=1
    else
      ok "服务端已停止"
    fi
  else
    warn "服务端未在运行"
  fi
  stop_vite || rc=1
  return $rc
}

do_config() {
  if [ ! -f "$CONFIG" ]; then
    if [ -f "$CONFIG_EXAMPLE" ]; then
      cp "$CONFIG_EXAMPLE" "$CONFIG"
      info "已从示例生成配置: $CONFIG"
    else
      err "配置不存在: $CONFIG"
      return 1
    fi
  fi
  info "常用配置项: server.api_port（服务端口）、server.public_host（回连地址）、auth.admin_password、auth.api_keys"
  "${EDITOR:-vi}" "$CONFIG"
  info "已退出编辑器。若修改了服务端口，需重启服务生效。"
}

do_status() {
  local mode; mode=$(build_mode)
  printf "  构建产物: %s\n" "$([ -f "$SERVER_BIN" ] && echo "$SERVER_BIN（方式 ${mode:-未知}）" || echo "未构建")"
  printf "  服务端:   %s\n" "$(is_running && echo "运行中（PID: $(running_pids | tr '\n' ' ')）" || echo "未运行")"
  printf "  Vite:     %s\n" "$(vite_running && echo "运行中（端口 $(vite_port)，PID: $(vite_pids | tr '\n' ' ')）" || echo "未运行")"
  if is_running; then
    printf "  入口:     http://127.0.0.1:%s\n" "$(api_port)"
  fi
  if vite_running; then
    printf "  开发入口: http://127.0.0.1:%s（热更新，/api 代理到后端）\n" "$(vite_port)"
  fi
}

usage() {
  cat <<EOF
ToShell 开发管理脚本（本仓库源码构建用；部署请用发布包内 install.sh）

用法:
  $0 start [--dev]     启动。默认 deploy 方式（带内嵌前端）
                       --dev 为开发方式：纯 API 后端 + Vite 热更新（端口 $(vite_port)）
  $0 stop              停止服务端与 Vite
  $0 status            查看构建产物、服务端与 Vite 的当前状态
  $0 build [--dev]     构建（--dev 构建不带内嵌前端的纯 API 版本）
  $0 clean             清理构建产物、日志与运行时状态
  $0 sync              仅把植入端模板同步到 release/（改模板后免于完整构建）
  $0 config            修改项目配置（编辑配置文件）
  $0 help              显示本帮助
  $0                   进入交互式菜单

两种启动方式产出的二进制不同（dev 不嵌入前端），切换方式时会提示重建。

产物位置：release/（复刻发布包布局，使本地与发布包的模板解析路径一致）
植入端模板唯一源：internal/server/builder/implant（+ implant_c）
EOF
}

interactive_menu() {
  while true; do
    echo
    echo "=============================="
    echo "  ToShell 管理菜单"
    echo "=============================="
    echo "  [1] 构建（deploy 方式，带内嵌前端）"
    echo "  [2] 清理全部构建产物与日志"
    echo "  [3] 启动（deploy 方式）"
    echo "  [4] 启动（dev 方式，含 Vite 热更新）"
    echo "  [5] 停止（服务端 + Vite）"
    echo "  [6] 查看状态"
    echo "  [7] 修改项目配置"
    echo "  [8] 退出"
    echo "=============================="
    local choice
    read -r -p "请选择 [1-8]: " choice
    case "$choice" in
      1) do_build deploy ;;
      2) do_clean ;;
      3) do_start deploy ;;
      4) do_start dev ;;
      5) do_stop ;;
      6) do_status ;;
      7) do_config ;;
      8) info "退出"; return 0 ;;
      *) warn "无效选择: $choice" ;;
    esac
  done
}

# 解析 <命令> [--dev|--deploy]
mode_arg() {
  case "${1:-}" in
    --dev) echo "dev" ;;
    ""|--deploy) echo "deploy" ;;
    *) err "未知参数: $1（可用: --dev）"; return 1 ;;
  esac
}

if [ $# -eq 0 ]; then
  interactive_menu
else
  m=""
  case "$1" in
    start|--start|-s) m=$(mode_arg "${2:-}") || exit 1; do_start "$m" ;;
    build|--build|-b) m=$(mode_arg "${2:-}") || exit 1; do_build "$m" ;;
    stop|--stop|-t)   do_stop ;;
    status|--status)  do_status ;;
    clean|--clean|-c) do_clean ;;
    sync|--sync)      sync_implant_template ;;
    config|--config|--edit-config|-e) do_config ;;
    help|--help|-h)   usage ;;
    *) err "未知参数: $1"; usage; exit 1 ;;
  esac
fi
