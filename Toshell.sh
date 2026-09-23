#!/usr/bin/env bash
# =====================================================================
#  ToShell 管理脚本（Linux / macOS）
#
#  两种使用模式：
#    1) 交互式菜单：不带任何参数直接运行      ./Toshell.sh
#    2) 命令行参数：./Toshell.sh <build|clean|start|stop|config|help>
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

if [ -t 1 ]; then
  RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; NC='\033[0m'
else
  RED=''; GREEN=''; YELLOW=''; CYAN=''; NC=''
fi

info() { printf "${CYAN}[信息]${NC} %s\n" "$*"; }
ok()   { printf "${GREEN}[成功]${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}[警告]${NC} %s\n" "$*"; }
err()  { printf "${RED}[错误]${NC} %s\n" "$*" >&2; }

# 返回正在运行的 toserver 进程 PID 列表
running_pids() { pgrep -x toserver 2>/dev/null || true; }
is_running() { [ -n "$(running_pids)" ]; }

# 从配置读取 api_port（默认 18081）
api_port() {
  local p
  p=$(sed -n 's/^[[:space:]]*api_port:[[:space:]]*\([0-9]*\).*/\1/p' "$CONFIG" 2>/dev/null | head -n1)
  echo "${p:-18081}"
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

# 正在运行的是旧二进制吗？（构建过但没重启）
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

# 跑着旧二进制时提示，经确认后重启；返回 0 表示已重启
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
      do_start
      return 0
      ;;
    *)
      info "已跳过重启。需要生效时执行: ./Toshell.sh stop  然后  ./Toshell.sh start"
      return 1
      ;;
  esac
}

do_build() {
  info "开始从源码构建项目（根目录: $ROOT）"
  command -v go >/dev/null 2>&1 || { err "未找到 Go 工具链（需要 Go >= 1.25），请先安装"; return 1; }

  if [ -f "$ROOT/web/package.json" ]; then
    if command -v npm >/dev/null 2>&1; then
      info "构建前端（npm ci && npm run build）..."
      ( cd "$ROOT/web" && npm ci && npm run build ) || { err "前端构建失败"; return 1; }
      info "同步前端产物到 cmd/server/webdist"
      rm -rf "$WEB_EMBED"
      mkdir -p "$WEB_EMBED"
      cp -R "$WEB_DIST/." "$WEB_EMBED/" || { err "同步 webdist 失败"; return 1; }
    else
      warn "未找到 npm，跳过前端构建（服务端将以纯 API / 无 Web 控制台方式构建）"
    fi
  fi

  local commit buildtime tags ldflags
  commit=$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo dev)
  buildtime=$(date -u +'%Y-%m-%dT%H:%M:%SZ')
  ldflags="-s -w -X main.commit=$commit -X main.buildTime=$buildtime"
  tags=""
  [ -f "$WEB_EMBED/index.html" ] && tags="-tags webui"

  info "编译服务端（go build $tags -o $SERVER_BIN）..."
  mkdir -p "$RELEASE_DIR"
  ( cd "$ROOT" && go build $tags -ldflags "$ldflags" -o "$SERVER_BIN" ./cmd/server ) || { err "服务端构建失败"; return 1; }
  [ -f "$SERVER_BIN" ] || { err "未生成产物: $SERVER_BIN"; return 1; }
  ok "构建完成: $SERVER_BIN（commit=$commit）"
  # 构建 ≠ 生效：服务还在跑的话，它跑的是内存里的旧二进制
  if is_stale_run; then
    restart_for_stale || true
  fi
}

do_clean() {
  info "清理构建产物与日志文件..."
  if is_running; then
    warn "服务正在运行（PID: $(running_pids | tr '\n' ' ')），正在运行的二进制可能无法删除；建议先停止服务"
  fi
  rm -f "$SERVER_BIN" "$ROOT/toserver" 2>/dev/null
  rm -rf "$IMPLANTS_DIR"/* 2>/dev/null
  rm -rf "$WEB_DIST" "$WEB_EMBED" 2>/dev/null
  rm -f "$ROOT/web/tsconfig.tsbuildinfo" "$ROOT/web/tsconfig.node.tsbuildinfo" 2>/dev/null
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
  if is_running; then
    # 跑着旧二进制时不能只警告就 return —— 那正是"构建完毫无变化"的根因：
    # 服务会一直跑内存里构建前的旧二进制，重新 start 是个静默空操作。
    if is_stale_run && restart_for_stale; then
      return 0
    fi
    warn "服务已在运行（PID: $(running_pids | tr '\n' ' ')）"
    return 0
  fi
  if [ ! -f "$SERVER_BIN" ]; then
    warn "未找到构建产物: $SERVER_BIN"
    local ans
    read -r -p "是否先执行源码构建？[Y/n] " ans || ans="n"
    case "$ans" in
      ""|[Yy]*) do_build || { err "构建失败，终止启动"; return 1; } ;;
      *) err "用户拒绝构建，终止启动"; return 1 ;;
    esac
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
  info "启动服务..."
  ( cd "$RELEASE_DIR" && nohup ./toserver -config configs/server.yaml > server.log 2> server.err.log & )
  sleep 2
  if is_running; then
    local port
    port=$(api_port)
    ok "服务已启动（PID: $(running_pids | tr '\n' ' ')）"
    info "Web 控制台: http://127.0.0.1:${port}  （API 端口: ${port}）"
    if command -v curl >/dev/null 2>&1; then
      local code
      code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/" 2>/dev/null)
      if [ "$code" = "200" ]; then ok "健康检查通过（HTTP $code）"; else warn "健康检查返回 HTTP ${code:-无响应}，可查看 $LOG_ERR"; fi
    fi
  else
    err "启动失败，请检查日志: $LOG_ERR"
    return 1
  fi
}

do_stop() {
  if ! is_running; then
    warn "服务未在运行"
    return 0
  fi
  info "发现服务进程: $(running_pids | tr '\n' ' ')"
  info "正在停止..."
  kill $(running_pids) 2>/dev/null
  sleep 1
  if is_running; then
    kill -9 $(running_pids) 2>/dev/null
    sleep 1
  fi
  if is_running; then
    err "停止失败，进程仍在: $(running_pids | tr '\n' ' ')"
    return 1
  fi
  ok "服务已停止"
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

usage() {
  cat <<EOF
ToShell 管理脚本

用法:
  $0                   进入交互式菜单
  $0 build             从源码构建项目
  $0 clean             清理全部构建产物、日志文件
  $0 start             启动服务（未构建时会询问是否先构建）
  $0 stop              停止正在运行的服务
  $0 config            修改项目配置（编辑配置文件）
  $0 help              显示本帮助
EOF
}

interactive_menu() {
  while true; do
    echo
    echo "=============================="
    echo "  ToShell 管理菜单"
    echo "=============================="
    echo "  [1] 从源码构建项目"
    echo "  [2] 清理全部构建产物、日志文件"
    echo "  [3] 启动服务"
    echo "  [4] 停止正在运行的服务"
    echo "  [5] 修改项目配置"
    echo "  [6] 退出"
    echo "=============================="
    local choice
    read -r -p "请选择 [1-6]: " choice
    case "$choice" in
      1) do_build ;;
      2) do_clean ;;
      3) do_start ;;
      4) do_stop ;;
      5) do_config ;;
      6) info "退出"; return 0 ;;
      *) warn "无效选择: $choice" ;;
    esac
  done
}

if [ $# -eq 0 ]; then
  interactive_menu
else
  case "$1" in
    build|--build|-b) do_build ;;
    clean|--clean|-c) do_clean ;;
    start|--start|-s) do_start ;;
    stop|--stop|-t) do_stop ;;
    config|--config|--edit-config|-e) do_config ;;
    help|--help|-h) usage ;;
    *) err "未知参数: $1"; usage; exit 1 ;;
  esac
fi
