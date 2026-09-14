#!/usr/bin/env bash
# =====================================================================
#  ToShell Team Server —— 一键部署入口 (Linux / macOS)
#  直接运行本脚本即可：检测环境 → 按需在线安装依赖 → 生成配置 →
#  启动打包好的 toserver。
#
#  想先只看环境不安装/不启动：
#      ./deploy.sh --check
#  其它参数见 install.sh 头部说明（--yes / --no-start / --daemon /
#  --with-garble / --go-version）。
# =====================================================================
set -e
cd "$(dirname "$0")"

if [ ! -f ./install.sh ]; then
  echo "[x] 缺少 install.sh（发布包应包含它）。若是从源码仓库运行，请在项目根执行："
  echo "    go build -tags webui -ldflags \"-s -w\" -o release/toserver ./cmd/server"
  exit 1
fi
chmod +x ./install.sh 2>/dev/null || true
exec ./install.sh "$@"
