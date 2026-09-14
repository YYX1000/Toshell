@echo off
rem =====================================================================
rem  ToShell Team Server —— 一键部署入口 (Windows)
rem  直接双击本文件（或运行 deploy.bat）即可：检测环境 → 按需在线安装依赖
rem  → 生成配置 → 启动打包好的 toserver.exe。
rem
rem  想先只看环境不安装/不启动：
rem      deploy.bat -Check
rem  其它参数见 install.ps1 头部说明（-Yes / -NoStart / -WithMingw /
rem  -WithGarble / -OpenFirewall / -GoVersion）。
rem =====================================================================
setlocal
cd /d "%~dp0"

where powershell >nul 2>nul
if errorlevel 1 (
  echo [x] 未找到 powershell（需要 Windows PowerShell，Win7 及以上自带）
  pause
  exit /b 1
)

powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1" %*
set RC=%ERRORLEVEL%
if not "%RC%"=="0" pause
exit /b %RC%
