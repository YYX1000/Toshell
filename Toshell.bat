@echo off
chcp 936 >nul
setlocal enabledelayedexpansion
rem =====================================================================
rem  ToShell 管理脚本（Windows CMD）
rem  两种使用模式：
rem    1) 交互式菜单：不带参数直接运行 Toshell.bat
rem    2) 命令行参数：Toshell.bat 后接 build / clean / start / stop / config / help
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
call :do_build
exit /b %errorlevel%

:run_clean
call :do_clean
exit /b %errorlevel%

:run_start
call :do_start
exit /b %errorlevel%

:run_stop
call :do_stop
exit /b %errorlevel%

:run_config
call :do_config
exit /b %errorlevel%

:run_help
call :usage
exit /b 0

rem ── 交互式菜单 ──────────────────────────────────────────
:menu
echo.
echo ==============================
echo   ToShell 管理菜单
echo ==============================
echo   [1] 从源码构建项目
echo   [2] 清理全部构建产物、日志文件
echo   [3] 启动服务
echo   [4] 停止正在运行的服务
echo   [5] 修改项目配置
echo   [6] 退出
echo ==============================
set "CHOICE="
set /p CHOICE=请选择 [1-6]: 
if "%CHOICE%"=="1" ( call :do_build & goto :menu )
if "%CHOICE%"=="2" ( call :do_clean & goto :menu )
if "%CHOICE%"=="3" ( call :do_start & goto :menu )
if "%CHOICE%"=="4" ( call :do_stop & goto :menu )
if "%CHOICE%"=="5" ( call :do_config & goto :menu )
if "%CHOICE%"=="6" ( echo [信息] 退出 & exit /b 0 )
echo [警告] 无效选择: %CHOICE%
goto :menu

rem ── 帮助 ────────────────────────────────────────────────
:usage
echo ToShell 管理脚本
echo.
echo 用法:
echo   Toshell.bat                   进入交互式菜单
echo   Toshell.bat build             从源码构建项目
echo   Toshell.bat clean             清理全部构建产物、日志文件
echo   Toshell.bat start             启动服务（未构建时会询问是否先构建）
echo   Toshell.bat stop              停止正在运行的服务
echo   Toshell.bat config            修改项目配置（编辑配置文件）
echo   Toshell.bat help              显示本帮助
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

rem ── 构建 ────────────────────────────────────────────────
:do_build
echo [信息] 开始从源码构建项目（根目录: %ROOT%）
where go >nul 2>nul
if errorlevel 1 (
  echo [错误] 未找到 Go 工具链（需要 Go ^>= 1.25），请先安装
  exit /b 1
)

if not exist "%ROOT%\web\package.json" goto :build_server
where npm >nul 2>nul
if errorlevel 1 (
  echo [警告] 未找到 npm，跳过前端构建（服务端将以纯 API / 无 Web 控制台方式构建）
  goto :build_server
)

echo [信息] 构建前端（npm ci ^&^& npm run build）...
pushd "%ROOT%\web"
call npm ci
if errorlevel 1 ( echo [错误] 前端依赖安装失败 & popd & exit /b 1 )
call npm run build
if errorlevel 1 ( echo [错误] 前端构建失败 & popd & exit /b 1 )
popd

echo [信息] 同步前端产物到 cmd\server\webdist
if exist "%WEB_EMBED%" rmdir /s /q "%WEB_EMBED%"
mkdir "%WEB_EMBED%"
xcopy /e /y /q "%WEB_DIST%\*" "%WEB_EMBED%\"
if errorlevel 1 ( echo [错误] 同步 webdist 失败 & exit /b 1 )

:build_server
set "BUILD_TAGS="
if exist "%WEB_EMBED%\index.html" set "BUILD_TAGS=-tags webui"
set "COMMIT=dev"
for /f "delims=" %%i in ('git -C "%ROOT%" rev-parse --short HEAD 2^>nul') do set "COMMIT=%%i"

if not exist "%RELEASE_DIR%" mkdir "%RELEASE_DIR%"
echo [信息] 编译服务端（go build %BUILD_TAGS% -o %SERVER_BIN%）...
pushd "%ROOT%"
go build %BUILD_TAGS% -ldflags "-s -w -X main.commit=%COMMIT%" -o "%SERVER_BIN%" ./cmd/server
set "RC=%ERRORLEVEL%"
popd
if not "%RC%"=="0" ( echo [错误] 服务端构建失败 & exit /b 1 )
if not exist "%SERVER_BIN%" ( echo [错误] 未生成产物: %SERVER_BIN% & exit /b 1 )
echo [成功] 构建完成: %SERVER_BIN%（commit=%COMMIT%）
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
call :do_start
exit /b

rem ── 清理 ────────────────────────────────────────────────
:do_clean
echo [信息] 清理构建产物与日志文件...
call :is_running
if not errorlevel 1 (
  echo [警告] 服务正在运行，正在运行的二进制可能无法删除；建议先停止服务
)
if exist "%SERVER_BIN%" del /f /q "%SERVER_BIN%" 2>nul
if exist "%ROOT%\toserver.exe" del /f /q "%ROOT%\toserver.exe" 2>nul
if exist "%IMPLANTS_DIR%" del /f /q "%IMPLANTS_DIR%\*" 2>nul
if exist "%IMPLANTS_DIR%" for /d %%d in ("%IMPLANTS_DIR%\*") do rmdir /s /q "%%d" 2>nul
if exist "%WEB_DIST%" rmdir /s /q "%WEB_DIST%" 2>nul
if exist "%WEB_EMBED%" rmdir /s /q "%WEB_EMBED%" 2>nul
if exist "%ROOT%\web\tsconfig.tsbuildinfo" del /f /q "%ROOT%\web\tsconfig.tsbuildinfo" 2>nul
if exist "%ROOT%\web\tsconfig.node.tsbuildinfo" del /f /q "%ROOT%\web\tsconfig.node.tsbuildinfo" 2>nul
if exist "%LOG_OUT%" del /f /q "%LOG_OUT%" 2>nul
if exist "%LOG_ERR%" del /f /q "%LOG_ERR%" 2>nul
if exist "%ROOT%\server.log" del /f /q "%ROOT%\server.log" 2>nul
if exist "%ROOT%\server.err.log" del /f /q "%ROOT%\server.err.log" 2>nul
echo [成功] 构建产物与日志已清理（node_modules 保留）

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

rem ── 启动 ────────────────────────────────────────────────
:do_start
call :is_running
if errorlevel 1 goto :start_fresh

rem 服务已在运行。若它跑的是构建前的旧二进制，光警告不重启就什么都变不了
rem （这正是"构建完界面没变化"的根因），所以这里必须提示并让用户选。
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
exit /b 0

:start_running_fresh
echo [警告] 服务已在运行：
tasklist /FI "IMAGENAME eq toserver.exe" /FO CSV /NH 2>nul | findstr /I "toserver.exe"
exit /b 0

:start_fresh
if not exist "%SERVER_BIN%" (
  echo [警告] 未找到构建产物: %SERVER_BIN%
  set /p BUILDANS=是否先执行源码构建？[Y/n] 
  if /i "!BUILDANS!"=="n" ( echo [错误] 用户拒绝构建，终止启动 & exit /b 1 )
  if /i "!BUILDANS!"=="no" ( echo [错误] 用户拒绝构建，终止启动 & exit /b 1 )
  call :do_build
  if errorlevel 1 ( echo [错误] 构建失败，终止启动 & exit /b 1 )
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
echo [信息] 启动服务...
if exist "%LOG_OUT%" del /f /q "%LOG_OUT%" 2>nul
if exist "%LOG_ERR%" del /f /q "%LOG_ERR%" 2>nul
powershell -NoProfile -Command "Start-Process -FilePath '%SERVER_BIN%' -ArgumentList '-config','configs\server.yaml' -WorkingDirectory '%RELEASE_DIR%' -RedirectStandardOutput '%LOG_OUT%' -RedirectStandardError '%LOG_ERR%' -WindowStyle Hidden"
if errorlevel 1 ( echo [错误] 无法启动服务进程 & exit /b 1 )
timeout /t 2 /nobreak >nul
call :is_running
if not errorlevel 1 (
  echo [成功] 服务已启动
  set "API_PORT=18081"
  if exist "%CONFIG%" for /f "tokens=2 delims=:" %%p in ('findstr /c:"api_port:" "%CONFIG%" 2^>nul') do set "API_PORT=%%p"
  set "API_PORT=!API_PORT: =!"
  echo [信息] Web 控制台: http://127.0.0.1:!API_PORT!  （API 端口: !API_PORT!）
  where curl >nul 2>nul
  if not errorlevel 1 (
    curl -s -o nul "http://127.0.0.1:!API_PORT!/"
    if not errorlevel 1 ( echo [成功] 健康检查通过 ) else ( echo [警告] 健康检查异常，可查看 %LOG_ERR% )
  ) else (
    echo [信息] 未找到 curl，跳过健康检查
  )
) else (
  echo [错误] 启动失败，请检查日志: %LOG_ERR%
  exit /b 1
)
exit /b 0

rem ── 停止 ────────────────────────────────────────────────
:do_stop
call :is_running
if errorlevel 1 (
  echo [警告] 服务未在运行
  exit /b 0
)
echo [信息] 发现服务进程:
tasklist /FI "IMAGENAME eq toserver.exe" /FO CSV /NH 2>nul | findstr /I "toserver.exe"
echo [信息] 正在停止...
taskkill /F /IM toserver.exe >nul 2>nul
timeout /t 1 /nobreak >nul
call :is_running
if not errorlevel 1 (
  echo [错误] 停止失败，进程仍在
  exit /b 1
)
echo [成功] 服务已停止
exit /b 0

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
