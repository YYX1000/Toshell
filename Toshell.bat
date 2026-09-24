@echo off
chcp 936 >nul
setlocal enabledelayedexpansion
rem =====================================================================
rem  ToShell 开发管理脚本（Windows CMD）—— 非部署入口
rem
rem  两种启动方式：
rem    deploy（默认）  构建带内嵌前端的二进制并运行，单进程。
rem                    运行时目录 = release\（配置 release\configs\server.yaml）
rem    dev             用 go run 直接起不带内嵌前端的服务端，同时起 Vite 开发
rem                    服务器。改 web\src\** 即时热更新，改 Go 代码重启即生效
rem                    （无需先构建）；界面走 Vite 端口，后端只提供 API。
rem                    运行时目录 = 仓库根（配置 configs\server.yaml）
rem
rem    启动：Toshell.bat start            部署方式
rem          Toshell.bat start --dev      开发方式
rem          Toshell.bat stop             两种都停（服务端 + Vite）
rem          Toshell.bat status           查看当前状态
rem          Toshell.bat clean            清理构建产物、日志、运行时状态
rem
rem  其余命令：build / sync / config / status / help（不带参数进入交互式菜单）。
rem
rem  注意：两种方式是**两个独立实例**：运行时目录不同（release\ vs 仓库根），因此
rem        配置、SQLite 库、载荷输出目录都不共享，且 API 端口默认都是 18081
rem        —— 不能同时跑。
rem
rem  部署（解压发布包后安装并运行）请用发布包内的 install.ps1 —— 它会在没有 Go 的
rem  机器上按需安装依赖，本脚本不做这件事。
rem
rem  为什么 deploy 的产物落在 release\ 而不是仓库根：
rem    release\ 是"复刻发布包布局"的目录。服务端解析植入端模板时按
rem    【配置 implant.template_dir -> 环境变量 TOSHELL_IMPLANT_TEMPLATE_DIR ->
rem      exe 同目录 implant\ -> exe 同目录 internal\server\builder\implant ->
rem      当前工作目录同路径】顺序回退。把 toserver.exe 放在 release\ 下，exe 同目录
rem    就有 implant\，于是 deploy 与发布包走完全相同的模板解析路径。
rem    dev 走最后一条回退（cwd = 仓库根 -> internal\server\builder\implant，即模板源），
rem    所以改模板在 dev 下立刻生效、连 sync 都不用。
rem
rem  编码：本文件必须是 GBK + CRLF。cmd 控制台按代码页 936 输出中文。
rem =====================================================================

set "ROOT=%~dp0"
if "%ROOT:~-1%"=="\" set "ROOT=%ROOT:~0,-1%"

rem deploy 的运行时目录与配置
set "RELEASE_DIR=%ROOT%\release"
set "SERVER_BIN=%RELEASE_DIR%\toserver.exe"
set "CONFIG=%RELEASE_DIR%\configs\server.yaml"
set "CONFIG_EXAMPLE=%RELEASE_DIR%\configs\server.yaml.example"
set "LOG_OUT=%RELEASE_DIR%\server.log"
set "LOG_ERR=%RELEASE_DIR%\server.err.log"
set "IMPLANTS_DIR=%RELEASE_DIR%\implants"
set "WEB_DIST=%ROOT%\web\dist"
set "WEB_EMBED=%ROOT%\cmd\server\webdist"

rem dev 的运行时目录与配置（仓库根，与 deploy 完全独立）
set "DEV_CONFIG=%ROOT%\configs\server.yaml"
set "DEV_CONFIG_EXAMPLE=%ROOT%\configs\server.yaml.example"
set "DEV_LOG_OUT=%RELEASE_DIR%\dev-server.log"
set "DEV_LOG_ERR=%RELEASE_DIR%\dev-server.err.log"

rem 运行时状态：服务端当前的启动方式（dev|deploy）
set "RUN_MODE_FILE=%RELEASE_DIR%\.run-mode"
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
if /i "%~2"=="--dev" (
  echo [信息] dev 方式用 go run 启动，不需要构建 —— 直接 Toshell.bat start --dev
  exit /b 0
)
if not "%~2"=="" if /i not "%~2"=="--deploy" (
  echo [错误] 未知参数: %~2
  exit /b 1
)
call :do_build
exit /b %errorlevel%

:run_clean
call :do_clean
exit /b %errorlevel%

:run_start
call :mode_arg "%~2"
if errorlevel 1 exit /b 1
if /i "!MODE!"=="dev" ( call :do_start_dev ) else ( call :do_start_deploy )
exit /b %errorlevel%

:run_stop
call :do_stop
exit /b %errorlevel%

:run_status
call :do_status
exit /b 0

:run_config
call :mode_arg "%~2"
if errorlevel 1 exit /b 1
call :do_config "!MODE!"
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
echo   [4] 启动（dev 方式，go run + Vite 热更新）
echo   [5] 停止（服务端 + Vite）
echo   [6] 查看状态
echo   [7] 修改配置
echo   [8] 退出
echo ==============================
set "CHOICE="
set /p CHOICE=请选择 [1-8]:
if "%CHOICE%"=="1" ( call :do_build & goto :menu )
if "%CHOICE%"=="2" ( call :do_clean & goto :menu )
if "%CHOICE%"=="3" ( call :do_start_deploy & goto :menu )
if "%CHOICE%"=="4" ( call :do_start_dev & goto :menu )
if "%CHOICE%"=="5" ( call :do_stop & goto :menu )
if "%CHOICE%"=="6" ( call :do_status & goto :menu )
if "%CHOICE%"=="7" ( call :do_config deploy & goto :menu )
if "%CHOICE%"=="8" ( echo [信息] 退出 & exit /b 0 )
echo [警告] 无效选择: %CHOICE%
goto :menu

rem ── 帮助 ────────────────────────────────────────────────
:usage
echo ToShell 开发管理脚本（本仓库源码构建用；部署请用发布包内 install.ps1）
echo.
echo 用法:
echo   Toshell.bat start [--dev]   启动。默认 deploy（构建带内嵌前端的二进制并运行）
echo                               --dev 用 go run 起纯 API 后端 + Vite 热更新（端口 3002）
echo   Toshell.bat stop            停止服务端与 Vite
echo   Toshell.bat status          查看构建产物、服务端与 Vite 的当前状态
echo   Toshell.bat build           构建 deploy 方式的二进制（dev 方式用 go run，无需构建）
echo   Toshell.bat clean           清理构建产物、日志与运行时状态
echo   Toshell.bat sync            仅把植入端模板同步到 release\（改模板后免于完整构建）
echo   Toshell.bat config [--dev]  修改配置（默认 deploy 的 release\configs\server.yaml）
echo   Toshell.bat help            显示本帮助
echo.
echo 两种方式是【两个独立实例】，运行时目录不同，因此配置/SQLite 库/载荷目录都不共享，
echo 且 API 端口默认都是 18081 —— 不能同时跑：
echo   deploy  release\configs\server.yaml + release\data\toshell.db，单进程带内嵌前端
echo   dev     configs\server.yaml        + .\data\toshell.db，      go run + Vite 热更新
exit /b 0

rem ── 端口与进程 ──────────────────────────────────────────
rem 服务端一律按端口定位，不按进程名：deploy 的进程叫 toserver.exe，dev 是 go run
rem 出来的 server.exe —— 后者名字太通用，按名字查会误伤其它进程。

rem 从给定配置读 api_port（默认 18081）。%1 = 配置路径，结果放入 PORT
:port_of_config
set "PORT=18081"
if exist "%~1" for /f "tokens=2 delims=:" %%p in ('findstr /c:"api_port:" "%~1" 2^>nul') do set "PORT=%%p"
set "PORT=!PORT: =!"
exit /b 0

rem 列出监听指定端口的 PID，空格分隔放入 PIDS
:pids_on_port
set "PIDS="
for /f "usebackq delims=" %%a in (`powershell -NoProfile -Command "(Get-NetTCPConnection -LocalPort %~1 -State Listen -ErrorAction SilentlyContinue).OwningProcess | Sort-Object -Unique"`) do set "PIDS=!PIDS! %%a"
exit /b 0

rem 服务端进程（两种方式的端口都查），放入 SPIDS；返回 0 = 在运行
:is_server_running
set "SPIDS="
call :port_of_config "%CONFIG%"
set "DPORT=!PORT!"
call :port_of_config "%DEV_CONFIG%"
set "VPORT2=!PORT!"
if not "!DPORT!"=="" (
  call :pids_on_port !DPORT!
  set "SPIDS=!SPIDS!!PIDS!"
)
if not "!VPORT2!"=="!DPORT!" if not "!VPORT2!"=="" (
  call :pids_on_port !VPORT2!
  set "SPIDS=!SPIDS!!PIDS!"
)
if "!SPIDS!"=="" exit /b 1
exit /b 0

rem 读取服务端当前的启动方式，放入 RUN_MODE
:get_run_mode
set "RUN_MODE="
if exist "%RUN_MODE_FILE%" for /f "usebackq delims=" %%a in ("%RUN_MODE_FILE%") do set "RUN_MODE=%%a"
exit /b 0

rem ── Vite ────────────────────────────────────────────────
rem 端口从 vite.config.ts 读，保持单一来源（不要在这里写死）
:vite_port
set "VPORT=3002"
for /f "usebackq delims=" %%a in (`powershell -NoProfile -Command "$m=Select-String -Path '%ROOT%\web\vite.config.ts' -Pattern 'port:\s*(\d{2,})' | Select-Object -First 1; if($m){$m.Matches[0].Groups[1].Value}else{'3002'}"`) do set "VPORT=%%a"
exit /b 0

:vite_pids
call :vite_port
call :pids_on_port !VPORT!
set "VPIDS=!PIDS!"
exit /b 0

rem 返回 0 = Vite 在运行
:is_vite_running
call :vite_pids
if "!VPIDS!"=="" exit /b 1
exit /b 0

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
rem 只杀 npm 的 PID 会留下 vite 子进程继续占着端口（实测过），停止时按端口定位
powershell -NoProfile -Command "Start-Process -FilePath 'npm' -ArgumentList 'run','dev' -WorkingDirectory '%ROOT%\web' -RedirectStandardOutput '%VITE_LOG%' -RedirectStandardError '%VITE_LOG%.err' -WindowStyle Hidden"
timeout /t 5 /nobreak >nul
call :is_vite_running
if errorlevel 1 (
  echo [错误] Vite 启动失败，见日志: %VITE_LOG%
  exit /b 1
)
echo [成功] Vite 已启动（PID: !VPIDS!）
echo [信息]   前端（热更新）: http://127.0.0.1:!VPORT!
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

rem ── 构建不等于生效：判断在跑的是不是"构建前的旧二进制" ──────
rem 仅对 deploy 有意义：dev 用 go run，每次都是最新代码。
:is_stale
call :get_run_mode
if not "!RUN_MODE!"=="deploy" exit /b 1
call :is_server_running
if errorlevel 1 exit /b 1
powershell -NoProfile -Command "$b=Get-Item -LiteralPath '%SERVER_BIN%' -ErrorAction SilentlyContinue; if(-not $b){exit 1}; $p=Get-Process -Id (!SPIDS!) -ErrorAction SilentlyContinue | Sort-Object StartTime | Select-Object -First 1; if(-not $p){exit 1}; if($b.LastWriteTime -gt $p.StartTime.AddSeconds(2)){exit 0}else{exit 1}"
exit /b

rem ── 健康检查轮询 ────────────────────────────────────────
rem dev 首次 go run 要编译，耗时可达数十秒，不能只 sleep 固定秒数
rem %1 = 端口；返回 0 = 通过
:wait_healthy
where curl >nul 2>nul
if errorlevel 1 exit /b 2
for /l %%i in (1,1,40) do (
  curl -s -o nul "http://127.0.0.1:%~1/api/v1/health"
  if not errorlevel 1 exit /b 0
  timeout /t 2 /nobreak >nul
)
exit /b 1

rem ── 构建 ────────────────────────────────────────────────
:do_build
echo [信息] 构建服务端 + Web 前端（deploy 方式，根目录: %ROOT%）
where go >nul 2>nul
if errorlevel 1 (
  echo [错误] 未找到 Go 工具链（需要 Go ^>= 1.25），请先安装
  exit /b 1
)
pushd "%ROOT%"
go run ./cmd/devtool build
set "RC=%ERRORLEVEL%"
popd
if not "%RC%"=="0" ( echo [错误] 构建失败 & exit /b 1 )
if not exist "%SERVER_BIN%" ( echo [错误] 未生成产物: %SERVER_BIN% & exit /b 1 )
echo [成功] 构建完成: %SERVER_BIN%

rem 构建不等于生效：服务若还在跑，它跑的是内存里的旧二进制（构建不会自动重启它）
call :is_server_running
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
call :do_start_deploy
exit /b 0

rem ── 清理 ────────────────────────────────────────────────
:do_clean
echo [信息] 清理构建产物与日志文件...
call :is_server_running
if not errorlevel 1 (
  echo [警告] 服务端正在运行（PID:!SPIDS!）；建议先停止服务（本脚本不会自动停）
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
rem release\implant{,_c} 是 deploy 构建时从模板源生成的产物（不是源码），一并清理
if exist "%RELEASE_DIR%\implant" rmdir /s /q "%RELEASE_DIR%\implant" 2>nul
if exist "%RELEASE_DIR%\implant_c" rmdir /s /q "%RELEASE_DIR%\implant_c" 2>nul
if exist "%WEB_DIST%" rmdir /s /q "%WEB_DIST%" 2>nul
if exist "%WEB_EMBED%" rmdir /s /q "%WEB_EMBED%" 2>nul
if exist "%ROOT%\web\tsconfig.tsbuildinfo" del /f /q "%ROOT%\web\tsconfig.tsbuildinfo" 2>nul
if exist "%ROOT%\web\tsconfig.node.tsbuildinfo" del /f /q "%ROOT%\web\tsconfig.node.tsbuildinfo" 2>nul
rem 运行时状态：运行方式、Vite 的 PID/日志、两种方式的服务端日志
if exist "%RUN_MODE_FILE%" del /f /q "%RUN_MODE_FILE%" 2>nul
if exist "%VITE_PID%" del /f /q "%VITE_PID%" 2>nul
if exist "%VITE_LOG%" del /f /q "%VITE_LOG%" 2>nul
if exist "%VITE_LOG%.err" del /f /q "%VITE_LOG%.err" 2>nul
if exist "%LOG_OUT%" del /f /q "%LOG_OUT%" 2>nul
if exist "%LOG_ERR%" del /f /q "%LOG_ERR%" 2>nul
if exist "%DEV_LOG_OUT%" del /f /q "%DEV_LOG_OUT%" 2>nul
if exist "%DEV_LOG_ERR%" del /f /q "%DEV_LOG_ERR%" 2>nul
if exist "%ROOT%\server.log" del /f /q "%ROOT%\server.log" 2>nul
if exist "%ROOT%\server.err.log" del /f /q "%ROOT%\server.err.log" 2>nul
echo [成功] 构建产物与日志已清理（node_modules 保留）

set "DBANS="
set /p DBANS=是否同时清理数据库（SQLite 数据，sessions/tasks 等将丢失）？输入 yes 确认，其他任意键跳过:
if /i "!DBANS!"=="yes" (
  call :is_server_running
  if not errorlevel 1 (
    echo [信息] 数据库清理需要先停止服务，正在停止...
    call :do_stop
  )
  rem 两种方式各有自己的库（deploy: release\data；dev: .\data）
  if exist "%ROOT%\release\data\toshell.db" del /f /q "%ROOT%\release\data\toshell.db" 2>nul
  if exist "%ROOT%\release\data\toshell.db-wal" del /f /q "%ROOT%\release\data\toshell.db-wal" 2>nul
  if exist "%ROOT%\release\data\toshell.db-shm" del /f /q "%ROOT%\release\data\toshell.db-shm" 2>nul
  if exist "%ROOT%\data\toshell.db" del /f /q "%ROOT%\data\toshell.db" 2>nul
  if exist "%ROOT%\data\toshell.db-wal" del /f /q "%ROOT%\data\toshell.db-wal" 2>nul
  if exist "%ROOT%\data\toshell.db-shm" del /f /q "%ROOT%\data\toshell.db-shm" 2>nul
  echo [成功] 两处数据库均已清理（服务重启后将自动重建空库）
) else (
  echo [信息] 已跳过数据库清理
)
exit /b 0

rem ── 状态 ────────────────────────────────────────────────
:do_status
if exist "%SERVER_BIN%" (
  echo   构建产物: %SERVER_BIN%
) else (
  echo   构建产物: 未构建（deploy 方式需要；dev 方式用 go run，不需要）
)
call :is_server_running
if not errorlevel 1 (
  call :get_run_mode
  if "!RUN_MODE!"=="" set "RUN_MODE=未知"
  echo   服务端:   运行中（方式 !RUN_MODE!，PID:!SPIDS!）
  call :port_of_config "%CONFIG%"
  set "DPORT=!PORT!"
  if /i "!RUN_MODE!"=="dev" (
    call :port_of_config "%DEV_CONFIG%"
    set "DPORT=!PORT!"
  )
  echo   入口:     http://127.0.0.1:!DPORT!
) else (
  echo   服务端:   未运行
)
call :is_vite_running
if not errorlevel 1 (
  call :vite_port
  echo   Vite:     运行中（端口 !VPORT!，PID:!VPIDS!）
  echo   开发入口: http://127.0.0.1:!VPORT!（热更新，/api 代理到后端）
) else (
  echo   Vite:     未运行
)
exit /b 0

rem ── 启动：deploy ─────────────────────────────────────────
:do_start_deploy
call :is_server_running
if not errorlevel 1 (
  rem 跑着旧二进制时不能只警告就 return —— 那正是"构建完毫无变化"的根因。
  call :is_stale
  if not errorlevel 1 (
    call :do_stop
    goto :deploy_fresh
  )
  echo [警告] 服务端已在运行（PID:!SPIDS!），请先 stop
  exit /b 0
)
:deploy_fresh
if not exist "%SERVER_BIN%" (
  echo [警告] 未找到构建产物: %SERVER_BIN%
  set "BUILDANS="
  set /p BUILDANS=是否现在构建？[Y/n]
  if /i "!BUILDANS!"=="n" ( echo [错误] 用户拒绝构建，终止启动 & exit /b 1 )
  if /i "!BUILDANS!"=="no" ( echo [错误] 用户拒绝构建，终止启动 & exit /b 1 )
  call :do_build
  if errorlevel 1 ( echo [错误] 构建失败，终止启动 & exit /b 1 )
)
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
echo [信息] 启动服务端（部署方式，带内嵌前端）...
if exist "%LOG_OUT%" del /f /q "%LOG_OUT%" 2>nul
if exist "%LOG_ERR%" del /f /q "%LOG_ERR%" 2>nul
powershell -NoProfile -Command "Start-Process -FilePath '%SERVER_BIN%' -ArgumentList '-config','configs\server.yaml' -WorkingDirectory '%RELEASE_DIR%' -RedirectStandardOutput '%LOG_OUT%' -RedirectStandardError '%LOG_ERR%' -WindowStyle Hidden"
if errorlevel 1 ( echo [错误] 无法启动服务进程 & exit /b 1 )
> "%RUN_MODE_FILE%" echo deploy
timeout /t 2 /nobreak >nul
call :is_server_running
if errorlevel 1 (
  echo [错误] 启动失败，请检查日志: %LOG_ERR%
  if exist "%RUN_MODE_FILE%" del /f /q "%RUN_MODE_FILE%" 2>nul
  exit /b 1
)
echo [成功] 服务端已启动（PID:!SPIDS!）
call :port_of_config "%CONFIG%"
echo [信息] Web 控制台: http://127.0.0.1:!PORT!
call :wait_healthy !PORT!
if errorlevel 1 ( echo [警告] 健康检查未通过，可查看 %LOG_ERR% ) else ( echo [成功] 健康检查通过 )
exit /b 0

rem ── 启动：dev ───────────────────────────────────────────
:do_start_dev
call :is_server_running
if not errorlevel 1 (
  echo [警告] 服务端已在运行（PID:!SPIDS!），请先 stop（dev 与 deploy 端口相同，不能同时跑）
  exit /b 0
)
where go >nul 2>nul
if errorlevel 1 (
  echo [错误] 未找到 Go 工具链（需要 Go ^>= 1.25）
  exit /b 1
)
rem dev 用仓库根的配置（与 deploy 的 release\configs 是两份独立配置）
if not exist "%DEV_CONFIG%" (
  if exist "%DEV_CONFIG_EXAMPLE%" (
    echo [警告] dev 配置不存在，已从示例生成: %DEV_CONFIG%
    copy /y "%DEV_CONFIG_EXAMPLE%" "%DEV_CONFIG%" >nul
  ) else (
    echo [错误] dev 配置不存在且无示例文件: %DEV_CONFIG%
    exit /b 1
  )
)
call :port_of_config "%DEV_CONFIG%"
set "DEVPORT=!PORT!"
echo [信息] 以 go run 启动服务端（开发方式，纯 API，cwd=%ROOT%）...
echo [信息]   首次运行需编译，可能等数十秒；改 Go 代码后重启即生效，无需先构建
if exist "%DEV_LOG_OUT%" del /f /q "%DEV_LOG_OUT%" 2>nul
if exist "%DEV_LOG_ERR%" del /f /q "%DEV_LOG_ERR%" 2>nul
powershell -NoProfile -Command "Start-Process -FilePath 'go' -ArgumentList 'run','./cmd/server','-config','configs/server.yaml' -WorkingDirectory '%ROOT%' -RedirectStandardOutput '%DEV_LOG_OUT%' -RedirectStandardError '%DEV_LOG_ERR%' -WindowStyle Hidden"
if errorlevel 1 ( echo [错误] 无法启动 go run & exit /b 1 )
> "%RUN_MODE_FILE%" echo dev
echo [信息] 等待服务端就绪（轮询 /api/v1/health，最多 80s）...
call :wait_healthy !DEVPORT!
if errorlevel 1 (
  call :is_server_running
  if not errorlevel 1 (
    echo [警告] 进程在跑但健康检查未通过，可查看日志: %DEV_LOG_ERR%
  ) else (
    echo [错误] 启动失败，请检查日志: %DEV_LOG_ERR%
    if exist "%RUN_MODE_FILE%" del /f /q "%RUN_MODE_FILE%" 2>nul
    exit /b 1
  )
) else (
  call :is_server_running
  echo [成功] 服务端已就绪（PID:!SPIDS!）
)
echo [信息] 后端纯 API: http://127.0.0.1:!DEVPORT!（开发方式不嵌入前端，界面走 Vite）
echo [信息] 数据与日志在仓库根: 配置 configs\server.yaml、库 .\data\toshell.db、载荷 .\implants\
echo [警告] 这是独立的开发实例：监听器配置与 deploy 的 release\data 不共享
call :start_vite
exit /b %errorlevel%

rem ── 停止 ────────────────────────────────────────────────
:do_stop
set "STOPRC=0"
call :is_server_running
if errorlevel 1 (
  echo [警告] 服务端未在运行
) else (
  call :get_run_mode
  echo [信息] 发现服务端进程（PID:!SPIDS!，方式: !RUN_MODE!）
  echo [信息] 正在停止...
  rem dev 的进程是 go run 的子进程，kill 父进程可能留下它；两种方式都按端口定位到
  rem 真正监听的进程，再连它一起收掉（/T 含子进程）
  for %%p in (!SPIDS!) do taskkill /F /T /PID %%p >nul 2>nul
  timeout /t 1 /nobreak >nul
  call :is_server_running
  if not errorlevel 1 (
    echo [错误] 服务端停止失败，进程仍在（PID:!SPIDS!）
    set "STOPRC=1"
  ) else (
    echo [成功] 服务端已停止
  )
)
if exist "%RUN_MODE_FILE%" del /f /q "%RUN_MODE_FILE%" 2>nul
call :stop_vite
if errorlevel 1 set "STOPRC=1"
exit /b !STOPRC!

rem ── 配置 ────────────────────────────────────────────────
:do_config
set "CFG=%CONFIG%"
set "CFGEX=%CONFIG_EXAMPLE%"
if /i "%~1"=="dev" (
  set "CFG=%DEV_CONFIG%"
  set "CFGEX=%DEV_CONFIG_EXAMPLE%"
)
if not exist "!CFG!" (
  if exist "!CFGEX!" (
    copy /y "!CFGEX!" "!CFG!" >nul
    echo [信息] 已从示例生成配置: !CFG!
  ) else (
    echo [错误] 配置不存在且无示例: !CFG!
    exit /b 1
  )
)
echo [信息] 常用配置项: server.api_port（服务端口）、server.public_host（回连地址）、auth.admin_password、auth.api_keys
start /wait "" notepad "!CFG!"
echo [信息] 已关闭编辑器。若修改了服务端口，需重启服务生效。
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
