@echo off
chcp 936 >nul
setlocal enabledelayedexpansion
rem =====================================================================
rem  ToShell 开发管理脚本（Windows CMD）—— 非部署入口
rem
rem  两种启动方式：
rem    deploy（默认）  构建**带内嵌前端**的服务端并启动，只起一个进程。
rem                    与发布包的行为一致：浏览器直接开 http://<host>:<api_port>。
rem    dev             构建**不带内嵌前端**的服务端（纯 API），同时起 Vite 开发
rem                    服务器。改 web\src\** 即时热更新，界面走 :3002，API 走 :api_port。
rem                    开发时用这种方式：唯一界面就是热更新那个，不会出现"改了前端
rem                    但端口上还是旧界面"的困惑。
rem
rem    启动：Toshell.bat start            部署方式
rem          Toshell.bat start --dev      开发方式
rem          Toshell.bat stop             两种都停（服务端 + Vite）
rem          Toshell.bat clean            清理构建产物、日志、运行时状态
rem
rem  其余命令：build / sync / config / status / help（不带参数进入交互式菜单）。
rem
rem  部署（解压发布包后安装并运行）请用发布包内的 install.ps1，它会在没有 Go 的
rem  机器上按需安装依赖，本脚本不做这件事。
rem
rem  为什么产物落在 release\ 而不是仓库根：
rem    release\ 是"复刻发布包布局"的目录。服务端解析植入端模板时按
rem    【配置 implant.template_dir → 环境变量 TOSHELL_IMPLANT_TEMPLATE_DIR →
rem      exe 同目录 implant\ → exe 同目录 internal\server\builder\implant →
rem      当前工作目录 internal\server\builder\implant】顺序回退。
rem    把 toserver.exe 放在 release\ 下，exe 同目录就有 implant\，于是本地开发与
rem    发布包走完全相同的模板解析路径，不会出现"本地能跑、发布包失效"。
rem
rem  编码：本文件必须是 GBK + CRLF。cmd 控制台按代码页 936 输出中文。
rem =====================================================================

set "ROOT=%~dp0"
if "%ROOT:~-1%"=="\" set "ROOT=%ROOT:~0,-1%"

set "SERVER_BIN=%ROOT%\release\toserver.exe"
set "CONFIG=%ROOT%\release\configs\server.yaml"
set "CONFIG_EXAMPLE=%ROOT%\release\configs\server.yaml.example"
set "LOG_OUT=%ROOT%\release\server.log"
set "LOG_ERR=%ROOT%\release\server.err.log"
set "WEB_DIST=%ROOT%\web\dist"
set "WEB_EMBED=%ROOT%\cmd\server\webdist"
set "RELEASE_DIR=%ROOT%\release"
set "IMPLANTS_DIR=%ROOT%\release\implants"
rem 运行时状态（开发方式用）：构建方式标记 + Vite 的 PID 与日志
set "BUILD_MODE_FILE=%RELEASE_DIR%\.build-mode"
set "VITE_PID=%RELEASE_DIR%\vite.pid"
set "VITE_LOG=%RELEASE_DIR%\vite.log"

rem ── 主入口 ──────────────────────────────────────────────
if "%~1"=="" goto :menu
if /i "%~1"=="build" goto :run_build
if /i "%~1"=="--build" goto :run_build
if /i "%~1"=="-b" goto :run_build
if /i "%~1"=="clean" goto :run_clean
if /i "%~1"=="--clean" goto :run_clean
if /i "%~1"=="-c" goto :run_clean
if /i "%~1"=="start" goto :run_start
if /i "%~1"=="--start" goto :run_start
if /i "%~1"=="-s" goto :run_start
if /i "%~1"=="stop" goto :run_stop
if /i "%~1"=="--stop" goto :run_stop
if /i "%~1"=="-t" goto :run_stop
if /i "%~1"=="status" goto :run_status
if /i "%~1"=="--status" goto :run_status
if /i "%~1"=="sync" goto :run_sync
if /i "%~1"=="--sync" goto :run_sync
if /i "%~1"=="config" goto :run_config
if /i "%~1"=="--config" goto :run_config
if /i "%~1"=="-e" goto :run_config
if /i "%~1"=="help" goto :run_help
if /i "%~1"=="--help" goto :run_help
if /i "%~1"=="-h" goto :run_help

echo [错误] 未知参数: %~1
call :usage
exit /b 1

:run_build
call :mode_arg "%~2"
if errorlevel 1 exit /b 1
call :do_build "%MODE%"
exit /b %errorlevel%

:run_clean
call :do_clean
exit /b %errorlevel%

:run_start
call :mode_arg "%~2"
if errorlevel 1 exit /b 1
call :do_start "%MODE%"
exit /b %errorlevel%

:run_stop
call :do_stop
exit /b %errorlevel%

:run_status
call :do_status
exit /b 0

:run_config
call :do_config
exit /b %errorlevel%

:run_sync
call :sync_template
exit /b %errorlevel%

:run_help
call :usage
exit /b 0

rem 解析第二个参数：--dev / --deploy / 空
:mode_arg
set "MODE=deploy"
if "%~1"=="" exit /b 0
if /i "%~1"=="--dev" ( set "MODE=dev" & exit /b 0 )
if /i "%~1"=="--deploy" ( set "MODE=deploy" & exit /b 0 )
echo [错误] 未知参数: %~1（可用: --dev）
exit /b 1

rem ── 交互式菜单 ──────────────────────────────────────────
:menu
echo.
echo ==============================
echo   ToShell 管理菜单
echo ==============================
echo   [1] 构建（deploy 方式，带内嵌前端）
echo   [2] 清理全部构建产物与日志
echo   [3] 启动（deploy 方式）
echo   [4] 启动（dev 方式，含 Vite 热更新）
echo   [5] 停止（服务端 + Vite）
echo   [6] 查看状态
echo   [7] 修改项目配置
echo   [8] 退出
echo ==============================
set "CHOICE="
set /p CHOICE=请选择 [1-8]:
if "%CHOICE%"=="1" ( call :do_build deploy & goto :menu )
if "%CHOICE%"=="2" ( call :do_clean & goto :menu )
if "%CHOICE%"=="3" ( call :do_start deploy & goto :menu )
if "%CHOICE%"=="4" ( call :do_start dev & goto :menu )
if "%CHOICE%"=="5" ( call :do_stop & goto :menu )
if "%CHOICE%"=="6" ( call :do_status & goto :menu )
if "%CHOICE%"=="7" ( call :do_config & goto :menu )
if "%CHOICE%"=="8" ( echo [信息] 退出 & exit /b 0 )
echo [警告] 无效选择: %CHOICE%
goto :menu

rem ── 帮助 ────────────────────────────────────────────────
:usage
echo ToShell 开发管理脚本（本仓库源码构建用；部署请用发布包内 install.ps1）
echo.
echo 用法:
echo   Toshell.bat start [--dev]   启动。默认 deploy 方式（带内嵌前端）
echo                               --dev 为开发方式：纯 API 后端 + Vite 热更新（端口 3002）
echo   Toshell.bat stop            停止服务端与 Vite
echo   Toshell.bat status          查看构建产物、服务端与 Vite 的当前状态
echo   Toshell.bat build [--dev]   构建（--dev 构建不带内嵌前端的纯 API 版本）
echo   Toshell.bat clean           清理构建产物、日志与运行时状态
echo   Toshell.bat sync            仅把植入端模板同步到 release\（改模板后免于完整构建）
echo   Toshell.bat config          修改项目配置（编辑配置文件）
echo   Toshell.bat help            显示本帮助
echo.
echo 两种启动方式产出的二进制不同（dev 不嵌入前端），切换方式时会提示重建。
echo.
echo 产物位置：release\（复刻发布包布局，使本地与发布包的模板解析路径一致）
echo 植入端模板唯一源：internal\server\builder\implant（+ implant_c）
exit /b 0

rem ── 检测服务进程 ────────────────────────────────────────
:is_running
tasklist /FI "IMAGENAME eq toserver.exe" /NH 2>nul | findstr /I "toserver.exe" >nul
exit /b

rem 正在运行的进程是不是"构建前的旧二进制"（构建过但没重启）
rem 返回 0 = 是旧版本；1 = 不是（没在跑 / 已是最新 / 检测不出来）
rem 用 PowerShell 比时间戳：cmd 自己拿不到进程启动时间
:is_stale
powershell -NoProfile -Command "$p=@(Get-Process -Name toserver -ErrorAction SilentlyContinue); $b=Get-Item -LiteralPath '%SERVER_BIN%' -ErrorAction SilentlyContinue; if($p.Count -eq 0 -or -not $b){exit 1}; if($b.LastWriteTime -gt ($p|Sort-Object StartTime|Select-Object -First 1).StartTime.AddSeconds(2)){exit 0}else{exit 1}"
exit /b

rem 读取构建方式标记（dev / deploy / 空）
:get_build_mode
set "BUILD_MODE="
if exist "%BUILD_MODE_FILE%" for /f "usebackq delims=" %%a in ("%BUILD_MODE_FILE%") do set "BUILD_MODE=%%a"
exit /b 0

rem ── Vite 开发服务器 ─────────────────────────────────────
rem 端口从 vite.config.ts 读，保持单一来源（不要在这里写死）
:vite_port
set "VPORT=3002"
for /f "usebackq delims=" %%a in (`powershell -NoProfile -Command "$m=Select-String -Path '%ROOT%\web\vite.config.ts' -Pattern 'port:\s*(\d{2,})' | Select-Object -First 1; if($m){$m.Matches[0].Groups[1].Value}else{'3002'}"`) do set "VPORT=%%a"
exit /b 0

rem 按端口找占用者。不依赖 PID 文件：npm run dev 会再 fork 出 vite 子进程，
rem 只杀 npm 的 PID 会留下 vite 继续占着端口。Windows 上端口属主即 vite。
:vite_pids
set "VPIDS="
call :vite_port
for /f "usebackq delims=" %%a in (`powershell -NoProfile -Command "(Get-NetTCPConnection -LocalPort %VPORT% -State Listen -ErrorAction SilentlyContinue).OwningProcess | Sort-Object -Unique"`) do set "VPIDS=!VPIDS! %%a"
exit /b 0

rem 返回 0 = Vite 在运行
:is_vite_running
call :vite_pids
if "!VPIDS!"=="" exit /b 1
exit /b 0

rem ── 构建方式校验 ────────────────────────────────────────
rem 保证 release\toserver.exe 是按 %1（dev / deploy）方式构建的，否则询问重建。
:ensure_build
set "WANT=%~1"
if not exist "%SERVER_BIN%" (
  echo [警告] 未找到构建产物: %SERVER_BIN%
  set "BUILDANS="
  set /p BUILDANS=是否现在以 !WANT! 方式构建？[Y/n]
  if /i "!BUILDANS!"=="n" ( echo [错误] 用户拒绝构建，终止启动 & exit /b 1 )
  if /i "!BUILDANS!"=="no" ( echo [错误] 用户拒绝构建，终止启动 & exit /b 1 )
  call :do_build "!WANT!"
  exit /b %errorlevel%
)
call :get_build_mode
if "!BUILD_MODE!"=="!WANT!" exit /b 0
if "!BUILD_MODE!"=="" (
  echo [警告] 无法判定现有产物的构建方式（缺 %BUILD_MODE_FILE%）
) else (
  echo [警告] 现有产物是 !BUILD_MODE! 方式构建的，与本次启动方式（!WANT!）不符
)
echo [警告] 两种方式的二进制不同：dev 不嵌入前端（界面走 Vite），deploy 嵌入前端
set "REBUILDANS="
set /p REBUILDANS=是否重新构建为 !WANT! 方式？[Y/n]
if /i "!REBUILDANS!"=="n" ( echo [警告] 继续使用现有产物，实际运行方式可能与本次启动方式不一致 & exit /b 0 )
if /i "!REBUILDANS!"=="no" ( echo [警告] 继续使用现有产物，实际运行方式可能与本次启动方式不一致 & exit /b 0 )
call :do_build "!WANT!"
exit /b %errorlevel%

rem ── 构建 ────────────────────────────────────────────────
:do_build
set "BMODE=%~1"
if "!BMODE!"=="" set "BMODE=deploy"
echo [信息] 构建服务端 + Web 前端（方式: !BMODE!，根目录: %ROOT%）
where go >nul 2>nul
if errorlevel 1 (
  echo [错误] 未找到 Go 工具链（需要 Go ^>= 1.25），请先安装
  exit /b 1
)

rem dev 方式用 --no-webui：不构建前端也不嵌入（不影响 cmd\server\webdist，
rem 因此随时可以切回 deploy 而不必重新 npm build）
pushd "%ROOT%"
if /i "!BMODE!"=="dev" (
  go run ./cmd/devtool build --no-webui
) else (
  go run ./cmd/devtool build
)
set "RC=%ERRORLEVEL%"
popd
if not "%RC%"=="0" ( echo [错误] 构建失败 & exit /b 1 )
if not exist "%SERVER_BIN%" ( echo [错误] 未生成产物: %SERVER_BIN% & exit /b 1 )
> "%BUILD_MODE_FILE%" echo !BMODE!
echo [成功] 构建完成: %SERVER_BIN%（方式 !BMODE!）

rem 构建 ≠ 生效：服务若还在跑，它跑的是内存里的旧二进制（构建不会自动重启它）
call :is_running
if errorlevel 1 exit /b 0
call :is_stale
if errorlevel 1 (
  echo [信息] 服务正在运行（已是最新二进制），无需重启。
  exit /b 0
)
echo [警告] 服务运行的是【构建前】的旧二进制，不重启不会生效。
set "RESTARTANS="
set /p RESTARTANS=是否立即重启服务以加载新版本？会断开当前所有会话（植入端会在一个心跳周期内自动重连）[Y/n]
if /i "!RESTARTANS!"=="n" ( echo [信息] 已跳过重启。需要生效时执行: Toshell.bat stop 然后 start & exit /b 0 )
if /i "!RESTARTANS!"=="no" ( echo [信息] 已跳过重启。需要生效时执行: Toshell.bat stop 然后 start & exit /b 0 )
call :do_stop
if errorlevel 1 ( echo [错误] 停止服务失败，终止重启 & exit /b 1 )
call :do_start "!BMODE!"
exit /b 0

rem ── 清理 ────────────────────────────────────────────────
:do_clean
echo [信息] 清理构建产物与日志文件...
call :is_running
if not errorlevel 1 (
  echo [警告] 服务端正在运行，正在运行的二进制可能无法删除；建议先停止服务
)
call :is_vite_running
if not errorlevel 1 (
  echo [警告] Vite 正在运行（PID: !VPIDS!），先停止它
  call :stop_vite
)
if exist "%SERVER_BIN%" del /f /q "%SERVER_BIN%" 2>nul
if exist "%ROOT%\toserver.exe" del /f /q "%ROOT%\toserver.exe" 2>nul
if exist "%IMPLANTS_DIR%" del /f /q "%IMPLANTS_DIR%\*" 2>nul
if exist "%IMPLANTS_DIR%" for /d %%d in ("%IMPLANTS_DIR%\*") do rmdir /s /q "%%d" 2>nul
rem release\implant{,_c} 是 build 时从模板源生成的产物（不是源码），一并清理
if exist "%RELEASE_DIR%\implant" rmdir /s /q "%RELEASE_DIR%\implant" 2>nul
if exist "%RELEASE_DIR%\implant_c" rmdir /s /q "%RELEASE_DIR%\implant_c" 2>nul
if exist "%WEB_DIST%" rmdir /s /q "%WEB_DIST%" 2>nul
if exist "%WEB_EMBED%" rmdir /s /q "%WEB_EMBED%" 2>nul
if exist "%ROOT%\web\tsconfig.tsbuildinfo" del /f /q "%ROOT%\web\tsconfig.tsbuildinfo" 2>nul
if exist "%ROOT%\web\tsconfig.node.tsbuildinfo" del /f /q "%ROOT%\web\tsconfig.node.tsbuildinfo" 2>nul
rem 运行时状态：构建方式标记与 Vite 的 PID/日志
if exist "%BUILD_MODE_FILE%" del /f /q "%BUILD_MODE_FILE%" 2>nul
if exist "%VITE_PID%" del /f /q "%VITE_PID%" 2>nul
if exist "%VITE_LOG%" del /f /q "%VITE_LOG%" 2>nul
if exist "%VITE_LOG%.err" del /f /q "%VITE_LOG%.err" 2>nul
if exist "%LOG_OUT%" del /f /q "%LOG_OUT%" 2>nul
if exist "%LOG_ERR%" del /f /q "%LOG_ERR%" 2>nul
if exist "%ROOT%\server.log" del /f /q "%ROOT%\server.log" 2>nul
if exist "%ROOT%\server.err.log" del /f /q "%ROOT%\server.err.log" 2>nul
echo [成功] 构建产物与日志已清理（node_modules 保留）

set "DBANS="
set /p DBANS=是否同时清理数据库（SQLite 数据，sessions/tasks 等将丢失）？输入 yes 确认，其他任意键跳过:
if /i "!DBANS!"=="yes" (
  call :is_running
  if not errorlevel 1 (
    echo [信息] 数据库清理需要先停止服务，正在停止...
    call :do_stop
    if errorlevel 1 ( echo [错误] 停止服务失败，已跳过数据库清理 & exit /b 1 )
  )
  if exist "%ROOT%\release\data\toshell.db" del /f /q "%ROOT%\release\data\toshell.db" 2>nul
  if exist "%ROOT%\release\data\toshell.db-wal" del /f /q "%ROOT%\release\data\toshell.db-wal" 2>nul
  if exist "%ROOT%\release\data\toshell.db-shm" del /f /q "%ROOT%\release\data\toshell.db-shm" 2>nul
  if exist "%ROOT%\data\toshell.db" del /f /q "%ROOT%\data\toshell.db" 2>nul
  if exist "%ROOT%\data\toshell.db-wal" del /f /q "%ROOT%\data\toshell.db-wal" 2>nul
  if exist "%ROOT%\data\toshell.db-shm" del /f /q "%ROOT%\data\toshell.db-shm" 2>nul
  echo [成功] 数据库已清理（服务重启后将自动重建空库）
) else (
  echo [信息] 已跳过数据库清理
)
exit /b 0

rem ── 状态 ────────────────────────────────────────────────
:do_status
call :get_build_mode
if "!BUILD_MODE!"=="" set "BUILD_MODE=未知"
if exist "%SERVER_BIN%" (
  echo   构建产物: %SERVER_BIN%（方式 !BUILD_MODE!）
) else (
  echo   构建产物: 未构建
)
call :is_running
if not errorlevel 1 (
  echo   服务端:   运行中
) else (
  echo   服务端:   未运行
)
call :is_vite_running
if not errorlevel 1 (
  call :vite_port
  echo   Vite:     运行中（端口 !VPORT!，PID: !VPIDS!）
) else (
  echo   Vite:     未运行
)
call :is_running
if not errorlevel 1 (
  call :api_port
  echo   入口:     http://127.0.0.1:!API_PORT!
)
call :is_vite_running
if not errorlevel 1 (
  call :vite_port
  echo   开发入口: http://127.0.0.1:!VPORT!（热更新，/api 代理到后端）
)
exit /b 0

rem ── 启动 ────────────────────────────────────────────────
:do_start
set "DMODE=%~1"
if "!DMODE!"=="" set "DMODE=deploy"

call :is_running
if errorlevel 1 goto :start_fresh

rem 服务已在运行。若它跑的是构建前的旧二进制，光警告不重启就什么都变不了。
call :is_stale
if errorlevel 1 goto :start_running_fresh
echo [警告] 服务运行的是【构建前】的旧二进制，不重启则 Web 控制台与接口仍是旧版本。
set "RESTARTANS="
set /p RESTARTANS=是否立即重启服务以加载新版本？会断开当前所有会话（植入端会在一个心跳周期内自动重连）[Y/n]
if /i "!RESTARTANS!"=="n" goto :start_no_restart
if /i "!RESTARTANS!"=="no" goto :start_no_restart
echo [信息] 正在重启服务...
call :do_stop
if errorlevel 1 ( echo [错误] 停止服务失败，终止重启 & exit /b 1 )
goto :start_fresh

:start_no_restart
echo [信息] 已跳过重启。需要生效时执行: Toshell.bat stop  然后  Toshell.bat start
goto :start_after_server

:start_running_fresh
echo [警告] 服务端已在运行（未重启）
goto :start_after_server

:start_fresh
call :ensure_build "!DMODE!"
if errorlevel 1 exit /b 1

if not exist "%RELEASE_DIR%\implant\main.go" (
  echo [警告] 植入端模板未同步到 %RELEASE_DIR%\implant（服务端将无法生成载荷）
  call :sync_template
  if errorlevel 1 ( echo [错误] 模板同步失败，终止启动 & exit /b 1 )
)
if not exist "%CONFIG%" (
  if exist "%CONFIG_EXAMPLE%" (
    echo [警告] 配置不存在，已从示例生成: %CONFIG%
    copy /y "%CONFIG_EXAMPLE%" "%CONFIG%" >nul
  ) else (
    echo [错误] 配置不存在且无示例文件: %CONFIG%
    exit /b 1
  )
)
echo [信息] 启动服务端（!DMODE! 方式）...
if exist "%LOG_OUT%" del /f /q "%LOG_OUT%" 2>nul
if exist "%LOG_ERR%" del /f /q "%LOG_ERR%" 2>nul
powershell -NoProfile -Command "Start-Process -FilePath '%SERVER_BIN%' -ArgumentList '-config','configs\server.yaml' -WorkingDirectory '%RELEASE_DIR%' -RedirectStandardOutput '%LOG_OUT%' -RedirectStandardError '%LOG_ERR%' -WindowStyle Hidden"
if errorlevel 1 ( echo [错误] 无法启动服务进程 & exit /b 1 )
timeout /t 2 /nobreak >nul
call :is_running
if errorlevel 1 (
  echo [错误] 启动失败，请检查日志: %LOG_ERR%
  exit /b 1
)
echo [成功] 服务端已启动
call :api_port
if /i "!DMODE!"=="dev" (
  echo [信息] 后端 API: http://127.0.0.1:!API_PORT!（开发方式不嵌入前端，界面走 Vite）
) else (
  echo [信息] Web 控制台: http://127.0.0.1:!API_PORT!
)
where curl >nul 2>nul
if not errorlevel 1 (
  curl -s -o nul "http://127.0.0.1:!API_PORT!/api/v1/health"
  if not errorlevel 1 ( echo [成功] 健康检查通过 ) else ( echo [警告] 健康检查异常，可查看 %LOG_ERR% )
) else (
  echo [信息] 未找到 curl，跳过健康检查
)

:start_after_server
if /i "!DMODE!"=="dev" (
  call :start_vite
  if errorlevel 1 exit /b 1
)
exit /b 0

rem ── Vite 启停 ───────────────────────────────────────────
:start_vite
call :is_vite_running
if not errorlevel 1 (
  echo [警告] Vite 已在运行（PID: !VPIDS!）
  exit /b 0
)
call :vite_port
if not exist "%ROOT%\web\node_modules" (
  echo [错误] 缺少 web\node_modules，请先在 web\ 下执行 npm ci
  exit /b 1
)
where npm >nul 2>nul
if errorlevel 1 (
  echo [错误] 未找到 npm（需要 Node.js ^>= 20）
  exit /b 1
)
echo [信息] 启动 Vite 开发服务器（端口 !VPORT!，日志 %VITE_LOG%）...
powershell -NoProfile -Command "Start-Process -FilePath 'npm' -ArgumentList 'run','dev' -WorkingDirectory '%ROOT%\web' -RedirectStandardOutput '%VITE_LOG%' -RedirectStandardError '%VITE_LOG%.err' -WindowStyle Hidden"
timeout /t 5 /nobreak >nul
call :is_vite_running
if errorlevel 1 (
  echo [错误] Vite 启动失败，见日志: %VITE_LOG%
  exit /b 1
)
echo [成功] Vite 已启动（PID: !VPIDS!）
echo [信息]   前端（热更新）: http://127.0.0.1:!VPORT!
call :api_port
echo [信息]   后端纯 API:     http://127.0.0.1:!API_PORT!
echo [信息]   /api 由 Vite 代理到后端（改代理目标用环境变量 VITE_PROXY_TARGET）
exit /b 0

rem 未在运行时静默返回 —— do_stop 会无条件调用它
:stop_vite
call :is_vite_running
if errorlevel 1 (
  if exist "%VITE_PID%" del /f /q "%VITE_PID%" 2>nul
  exit /b 0
)
echo [信息] 停止 Vite（PID: !VPIDS!）...
for %%p in (!VPIDS!) do taskkill /F /T /PID %%p >nul 2>nul
timeout /t 2 /nobreak >nul
if exist "%VITE_PID%" del /f /q "%VITE_PID%" 2>nul
call :is_vite_running
if not errorlevel 1 (
  call :vite_port
  echo [错误] Vite 未能停止，仍占用端口 !VPORT!（PID: !VPIDS!）
  exit /b 1
)
echo [成功] Vite 已停止
exit /b 0

rem ── 停止 ────────────────────────────────────────────────
:do_stop
set "STOPRC=0"
call :is_running
if errorlevel 1 (
  echo [警告] 服务端未在运行
) else (
  echo [信息] 发现服务端进程:
  tasklist /FI "IMAGENAME eq toserver.exe" /FO CSV /NH 2>nul | findstr /I "toserver.exe"
  echo [信息] 正在停止...
  taskkill /F /IM toserver.exe >nul 2>nul
  timeout /t 1 /nobreak >nul
  call :is_running
  if not errorlevel 1 (
    echo [错误] 服务端停止失败，进程仍在
    set "STOPRC=1"
  ) else (
    echo [成功] 服务端已停止
  )
)
call :stop_vite
if errorlevel 1 set "STOPRC=1"
exit /b !STOPRC!

rem ── 配置 ────────────────────────────────────────────────
:do_config
if not exist "%CONFIG%" (
  if exist "%CONFIG_EXAMPLE%" (
    copy /y "%CONFIG_EXAMPLE%" "%CONFIG%" >nul
    echo [信息] 已从示例生成配置: %CONFIG%
  ) else (
    echo [错误] 配置不存在: %CONFIG%
    exit /b 1
  )
)
echo [信息] 常用配置项: server.api_port（服务端口）、server.public_host（回连地址）、auth.admin_password、auth.api_keys
start /wait "" notepad "%CONFIG%"
echo [信息] 已关闭编辑器。若修改了服务端口，需重启服务生效。
exit /b 0

rem ── 读取 api_port（默认 18081）──────────────────────────
:api_port
set "API_PORT=18081"
if exist "%CONFIG%" for /f "tokens=2 delims=:" %%p in ('findstr /c:"api_port:" "%CONFIG%" 2^>nul') do set "API_PORT=%%p"
set "API_PORT=!API_PORT: =!"
exit /b 0

rem ── 同步植入端模板（委托 cmd/devtool）──────────────────
rem 构建与模板同步的唯一实现在 cmd/devtool —— 本地与 CI 跑同一份。
rem 本脚本只保留平台相关的 run / stop / logs 与"跑着旧二进制"的判定：
rem 那部分并非重复（每个平台各一份、互不冗余），且是踩过真实事故才调对的，重写是净风险。
:sync_template
where go >nul 2>nul
if errorlevel 1 (
  echo [错误] 未找到 Go 工具链（需要 Go ^>= 1.25）
  exit /b 1
)
pushd "%ROOT%"
go run ./cmd/devtool sync
set "RC=%ERRORLEVEL%"
popd
if not "%RC%"=="0" ( echo [错误] 模板同步失败 ^& exit /b 1 )
exit /b 0
