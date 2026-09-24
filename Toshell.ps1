# =====================================================================
#  ToShell 开发管理脚本（Windows PowerShell 5.1+）—— 非部署入口
#
#  两种启动方式：
#    deploy（默认）  构建**带内嵌前端**的服务端并启动，只起一个进程。
#                    与发布包的行为一致：浏览器直接开 http://<host>:<api_port>。
#    dev             构建**不带内嵌前端**的服务端（纯 API），同时起 Vite 开发
#                    服务器。改 web/src/** 即时热更新，界面走 :3002，API 走 :api_port。
#                    开发时用这种方式：唯一界面就是热更新那个，不会出现"改了前端
#                    但端口上还是旧界面"的困惑。
#
#    启动：.\Toshell.ps1 start            部署方式
#          .\Toshell.ps1 start --dev      开发方式
#          .\Toshell.ps1 stop             两种都停（服务端 + Vite）
#          .\Toshell.ps1 clean            清理构建产物、日志、运行时状态
#
#  其余命令：build / sync / config / help / status（不带参数进入交互式菜单）。
#
#  部署（解压发布包后安装并运行）请用发布包内的 install.ps1 —— 它会在没有 Go 的
#  机器上按需安装依赖，本脚本不做这件事。
#
#  为什么产物落在 release\ 而不是仓库根：
#    release\ 是"复刻发布包布局"的目录。服务端解析植入端模板时按
#    【配置 implant.template_dir → 环境变量 TOSHELL_IMPLANT_TEMPLATE_DIR →
#      exe 同目录 implant\ → exe 同目录 internal\server\builder\implant →
#      当前工作目录 internal\server\builder\implant】顺序回退。
#    把 toserver.exe 放在 release\ 下，exe 同目录就有 implant\，于是**本地开发与
#    发布包走完全相同的模板解析路径**，不会出现"本地能跑、发布包失效"。
#
#  若被执行策略阻止，可用：
#    powershell -NoProfile -ExecutionPolicy Bypass -File .\Toshell.ps1
#
#  编码：本文件必须是 UTF-8 with BOM + CRLF。丢了 BOM 会让 PS 5.1 按 ANSI 解析，
#        中文全部乱码甚至语法报错。
# =====================================================================
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$Action = "",
    [Parameter(Position = 1)]
    [string]$Arg2 = ""
)

$ErrorActionPreference = "Stop"
$Root = $PSScriptRoot
$ServerBin = Join-Path $Root "release\toserver.exe"
$Config = Join-Path $Root "release\configs\server.yaml"
$ConfigExample = Join-Path $Root "release\configs\server.yaml.example"
$LogOut = Join-Path $Root "release\server.log"
$LogErr = Join-Path $Root "release\server.err.log"
$WebDist = Join-Path $Root "web\dist"
$WebEmbed = Join-Path $Root "cmd\server\webdist"
$ReleaseDir = Join-Path $Root "release"
$ImplantsDir = Join-Path $Root "release\implants"

# 运行时状态（开发方式用）：构建方式标记 + Vite 的 PID 与日志。
# 构建方式标记用于判断 release\toserver.exe 是否与本次启动方式匹配 —— 两种方式的
# 二进制不同（dev 不嵌入前端），不匹配就得重建，否则你以为在开发态、其实跑的是
# 带内嵌前端的部署态二进制。
$BuildModeFile = Join-Path $ReleaseDir ".build-mode"
$VitePidFile = Join-Path $ReleaseDir "vite.pid"
$ViteLog = Join-Path $ReleaseDir "vite.log"

function Write-Info  { Write-Host "[信息] $args" -ForegroundColor Cyan }
function Write-Ok    { Write-Host "[成功] $args" -ForegroundColor Green }
function Write-Warn  { Write-Warning "$args" }
function Write-Err   { Write-Host "[错误] $args" -ForegroundColor Red }

function Get-ServerProcess {
    Get-CimInstance Win32_Process -Filter "Name='toserver.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -like "$Root\release\toserver.exe" }
}

function Get-ApiPort {
    $port = 18081
    if (Test-Path $Config) {
        $m = Select-String -Path $Config -Pattern '^\s*api_port:\s*(\d+)' | Select-Object -First 1
        if ($m -and $m.Matches[0].Groups[1].Success) { $port = [int]$m.Matches[0].Groups[1].Value }
    }
    return $port
}

# Vite 监听端口：从 vite.config.ts 读，保持单一来源（不要在这里写死）
function Get-VitePort {
    $cfg = Join-Path $Root "web\vite.config.ts"
    if (Test-Path $cfg) {
        $m = Select-String -Path $cfg -Pattern 'port:\s*(\d{2,})' | Select-Object -First 1
        if ($m -and $m.Matches[0].Groups[1].Success) { return [int]$m.Matches[0].Groups[1].Value }
    }
    return 3002
}

# ── 构建方式 ─────────────────────────────────────────────────────────

function Get-BuildMode {
    if (Test-Path $BuildModeFile) { return (Get-Content $BuildModeFile -First 1).Trim() }
    return ""
}

# 保证 release\toserver.exe 是按该方式构建的，否则询问重建。
function Ensure-Build {
    param([string]$Want)

    if (-not (Test-Path $ServerBin)) {
        Write-Warn "未找到构建产物: $ServerBin"
        $ans = Read-Host "是否现在以 $Want 方式构建？[Y/n]"
        if ($ans -eq "" -or $ans -match '^[Yy]') {
            if (-not (Invoke-Build $Want)) { Write-Err "构建失败，终止启动"; return $false }
            return $true
        }
        Write-Err "用户拒绝构建，终止启动"
        return $false
    }

    $have = Get-BuildMode
    if ($have -eq $Want) { return $true }

    if ($have -eq "") {
        Write-Warn "无法判定现有产物的构建方式（缺 $BuildModeFile）"
    }
    else {
        Write-Warn "现有产物是 $have 方式构建的，与本次启动方式（$Want）不符"
    }
    Write-Warn "两种方式的二进制不同：dev 不嵌入前端（界面走 Vite），deploy 嵌入前端"
    $ans = Read-Host "是否重新构建为 $Want 方式？[Y/n]"
    if ($ans -eq "" -or $ans -match '^[Yy]') {
        if (-not (Invoke-Build $Want)) { Write-Err "构建失败，终止启动"; return $false }
    }
    else {
        Write-Warn "继续使用现有产物 —— 实际运行方式可能与本次启动方式不一致"
    }
    return $true
}

# ── Vite 开发服务器 ──────────────────────────────────────────────────

# 按端口找占用者。不依赖 PID 文件：npm run dev 会再 fork 出 vite 子进程，只杀 npm
# 的 PID 会留下 vite 继续占着端口。按端口定位才可靠（Windows 上端口属主即 vite）。
function Get-VitePids {
    $p = Get-VitePort
    $conns = Get-NetTCPConnection -LocalPort $p -State Listen -ErrorAction SilentlyContinue
    if ($conns) { return @($conns | Select-Object -ExpandProperty OwningProcess -Unique) }
    return @()
}

function Test-ViteRunning {
    return (@(Get-VitePids).Count -gt 0)
}

function Start-Vite {
    $p = Get-VitePort
    if (Test-ViteRunning) {
        Write-Warn "Vite 已在运行（PID: $((Get-VitePids) -join ', ')）"
        return $true
    }
    if (-not (Test-Path (Join-Path $Root "web\node_modules"))) {
        Write-Err "缺少 web\node_modules，请先在 web\ 下执行 npm ci"
        return $false
    }
    if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
        Write-Err "未找到 npm（需要 Node.js >= 20）"
        return $false
    }

    Write-Info "启动 Vite 开发服务器（端口 $p，日志 $ViteLog）..."
    # -PassThru 拿到 npm 的 PID；真正监听端口的是它的子进程，停止时按端口定位
    $proc = Start-Process -FilePath "npm" -ArgumentList "run", "dev" `
        -WorkingDirectory (Join-Path $Root "web") `
        -RedirectStandardOutput $ViteLog -RedirectStandardError "$ViteLog.err" `
        -WindowStyle Hidden -PassThru
    if ($proc) { Set-Content -Path $VitePidFile -Value $proc.Id -Encoding ASCII }
    Start-Sleep -Seconds 4

    if (Test-ViteRunning) {
        Write-Ok "Vite 已启动（PID: $((Get-VitePids) -join ', ')）"
        Write-Info "  前端（热更新）: http://127.0.0.1:$p"
        Write-Info "  后端纯 API:     http://127.0.0.1:$(Get-ApiPort)"
        Write-Info "  /api 由 Vite 代理到后端（改代理目标用环境变量 VITE_PROXY_TARGET）"
        return $true
    }
    Write-Err "Vite 启动失败，见日志: $ViteLog"
    return $false
}

# 停止 Vite。未在运行时静默返回 —— stop 会无条件调用它。
function Stop-Vite {
    if (-not (Test-ViteRunning)) {
        Remove-Item $VitePidFile -Force -ErrorAction SilentlyContinue
        return $true
    }
    $pids = Get-VitePids
    Write-Info "停止 Vite（PID: $($pids -join ', ')）..."
    # /T 连同子进程一起结束，避免留下 vite 占着端口
    foreach ($vp in $pids) { & taskkill /PID $vp /T /F 2>$null | Out-Null }
    Start-Sleep -Seconds 2
    if (Test-ViteRunning) {
        Start-Sleep -Seconds 2
    }
    Remove-Item $VitePidFile -Force -ErrorAction SilentlyContinue
    if (Test-ViteRunning) {
        Write-Err "Vite 未能停止，仍占用端口 $(Get-VitePort)（PID: $((Get-VitePids) -join ', ')）"
        return $false
    }
    Write-Ok "Vite 已停止"
    return $true
}

# ── 构建 ≠ 生效：判断正在运行的进程是不是"构建前的旧二进制" ──────────────
function Get-StaleRunInfo {
    $procs = @(Get-ServerProcess)
    if ($procs.Count -eq 0) { return $null }
    if (-not (Test-Path $ServerBin)) { return $null }
    $binTime = (Get-Item $ServerBin).LastWriteTime
    $oldest = $procs | Sort-Object CreationDate | Select-Object -First 1
    if ($binTime -le $oldest.CreationDate.AddSeconds(2)) { return $null }
    return [pscustomobject]@{
        Pids      = (($procs | ForEach-Object { $_.ProcessId }) -join ', ')
        StartedAt = $oldest.CreationDate
        BuiltAt   = $binTime
    }
}

function Restart-ForStaleBinary {
    $stale = Get-StaleRunInfo
    if (-not $stale) { return $false }
    Write-Warn "服务运行的是【构建前】的旧二进制（PID: $($stale.Pids)）"
    Write-Info "  进程启动于: $($stale.StartedAt.ToString('yyyy-MM-dd HH:mm:ss'))"
    Write-Info "  产物生成于: $($stale.BuiltAt.ToString('yyyy-MM-dd HH:mm:ss'))"
    Write-Warn "不重启的话，Web 控制台与接口仍然是旧版本。"
    $ans = Read-Host "是否立即重启服务以加载新版本？会断开当前所有会话（植入端会在一个心跳周期内自动重连）[Y/n]"
    if (-not ($ans -eq "" -or $ans -match '^[Yy]')) {
        Write-Info "已跳过重启。需要生效时执行: .\Toshell.ps1 stop  然后  .\Toshell.ps1 start"
        return $false
    }
    Invoke-Stop | Out-Null
    if (Get-ServerProcess) { Write-Err "停止失败，终止重启"; return $false }
    $m = Get-BuildMode
    if ($m -ne "dev") { $m = "deploy" }
    Invoke-Start $m
    return $true
}

# ── 构建与模板 ───────────────────────────────────────────────────────

function Invoke-Devtool {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]]$DevArgs)
    Push-Location $Root
    try {
        & go run ./cmd/devtool @DevArgs
        return ($LASTEXITCODE -eq 0)
    }
    finally { Pop-Location }
}

function Sync-ImplantTemplate {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Write-Err "未找到 Go 工具链（需要 Go >= 1.25）"
        return $false
    }
    if (-not (Invoke-Devtool sync)) { Write-Err "模板同步失败"; return $false }
    return $true
}

function Invoke-Build {
    param([string]$Mode = "deploy")

    Write-Info "构建服务端 + Web 前端（方式: $Mode，根目录: $Root）"
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Write-Err "未找到 Go 工具链（需要 Go >= 1.25），请先安装"
        return $false
    }

    # dev 方式用 --no-webui：不构建前端也不嵌入（不影响 cmd\server\webdist，
    # 因此随时可以切回 deploy 而不必重新 npm build）。
    if ($Mode -eq "dev") {
        if (-not (Invoke-Devtool build --no-webui)) { Write-Err "构建失败"; return $false }
    }
    else {
        if (-not (Invoke-Devtool build)) { Write-Err "构建失败"; return $false }
    }

    if (-not (Test-Path $ServerBin)) { Write-Err "未生成产物: $ServerBin"; return $false }
    Set-Content -Path $BuildModeFile -Value $Mode -Encoding ASCII
    Write-Ok "构建完成: $ServerBin（方式 $Mode）"

    Restart-ForStaleBinary | Out-Null
    return $true
}

function Invoke-Clean {
    Write-Info "清理构建产物与日志文件..."
    if (Get-ServerProcess) {
        Write-Warn "服务端正在运行，正在运行的二进制可能无法删除；建议先停止服务"
    }
    if (Test-ViteRunning) {
        Write-Warn "Vite 正在运行（PID: $((Get-VitePids) -join ', ')），先停止它"
        Stop-Vite | Out-Null
    }
    Remove-Item $ServerBin -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $Root "toserver.exe") -Force -ErrorAction SilentlyContinue
    if (Test-Path $ImplantsDir) { Remove-Item "$ImplantsDir\*" -Recurse -Force -ErrorAction SilentlyContinue }
    # release\implant{,_c} 是 build 时从模板源生成的产物（不是源码），一并清理
    Remove-Item (Join-Path $ReleaseDir "implant")   -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $ReleaseDir "implant_c") -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item $WebDist -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item $WebEmbed -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $Root "web\tsconfig.tsbuildinfo") -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $Root "web\tsconfig.node.tsbuildinfo") -Force -ErrorAction SilentlyContinue
    # 运行时状态：构建方式标记与 Vite 的 PID/日志
    Remove-Item $BuildModeFile -Force -ErrorAction SilentlyContinue
    Remove-Item $VitePidFile -Force -ErrorAction SilentlyContinue
    Remove-Item $ViteLog -Force -ErrorAction SilentlyContinue
    Remove-Item "$ViteLog.err" -Force -ErrorAction SilentlyContinue
    Remove-Item $LogOut -Force -ErrorAction SilentlyContinue
    Remove-Item $LogErr -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $Root "server.log") -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $Root "server.err.log") -Force -ErrorAction SilentlyContinue
    Write-Ok "构建产物与日志已清理（node_modules 保留）"

    $ans = Read-Host "是否同时清理数据库（SQLite 数据，sessions/tasks 等将丢失）？输入 yes 确认，其他任意键跳过"
    if ($ans -and $ans.ToLower() -eq "yes") {
        if (Get-ServerProcess) {
            Write-Info "数据库清理需要先停止服务，正在停止..."
            Invoke-Stop | Out-Null
            if (Get-ServerProcess) { Write-Err "停止服务失败，已跳过数据库清理"; return }
        }
        Remove-Item (Join-Path $Root "release\data\toshell.db") -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $Root "release\data\toshell.db-wal") -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $Root "release\data\toshell.db-shm") -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $Root "data\toshell.db") -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $Root "data\toshell.db-wal") -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $Root "data\toshell.db-shm") -Force -ErrorAction SilentlyContinue
        Write-Ok "数据库已清理（服务重启后将自动重建空库）"
    }
    else {
        Write-Info "已跳过数据库清理"
    }
}

function Invoke-Start {
    param([string]$Mode = "deploy")

    $procs = @(Get-ServerProcess)
    if ($procs.Count -gt 0) {
        # 跑着旧二进制时不能只警告就 return —— 那正是"构建完毫无变化"的根因。
        if (Restart-ForStaleBinary) { return }
        $pids = ($procs | ForEach-Object { $_.ProcessId }) -join ', '
        Write-Warn "服务端已在运行（PID: $pids）"
    }
    else {
        if (-not (Ensure-Build $Mode)) { return }

        # 模板目录不在就不启动：否则服务端能起来，但生成载荷时才发现没有模板
        if (-not (Test-Path (Join-Path $ReleaseDir "implant\main.go"))) {
            Write-Warn "植入端模板未同步到 $ReleaseDir\implant（服务端将无法生成载荷）"
            if (-not (Sync-ImplantTemplate)) { Write-Err "模板同步失败，终止启动"; return }
        }
        if (-not (Test-Path $Config)) {
            if (Test-Path $ConfigExample) {
                Write-Warn "配置不存在，已从示例生成: $Config"
                Copy-Item $ConfigExample $Config
            }
            else {
                Write-Err "配置不存在且无示例文件: $Config"
                return
            }
        }

        Write-Info "启动服务端（$Mode 方式）..."
        Remove-Item $LogOut -Force -ErrorAction SilentlyContinue
        Remove-Item $LogErr -Force -ErrorAction SilentlyContinue
        Start-Process -FilePath $ServerBin -ArgumentList '-config','configs\server.yaml' -WorkingDirectory $ReleaseDir -RedirectStandardOutput $LogOut -RedirectStandardError $LogErr -WindowStyle Hidden
        Start-Sleep -Seconds 2

        $procs = @(Get-ServerProcess)
        if ($procs.Count -gt 0) {
            $pids = ($procs | ForEach-Object { $_.ProcessId }) -join ', '
            $port = Get-ApiPort
            Write-Ok "服务端已启动（PID: $pids）"
            if ($Mode -eq "deploy") {
                Write-Info "Web 控制台: http://127.0.0.1:${port}"
            }
            else {
                Write-Info "后端 API: http://127.0.0.1:${port}（开发方式不嵌入前端，界面走 Vite）"
            }
            try {
                $code = (Invoke-WebRequest -Uri "http://127.0.0.1:${port}/api/v1/health" -UseBasicParsing -TimeoutSec 5).StatusCode
                Write-Ok "健康检查通过（/api/v1/health → HTTP $code）"
            }
            catch {
                Write-Warn "健康检查异常，可查看 $LogErr"
            }
        }
        else {
            Write-Err "启动失败，请检查日志: $LogErr"
            return
        }
    }

    if ($Mode -eq "dev") {
        Start-Vite | Out-Null
    }
}

function Invoke-Stop {
    $ok = $true
    $procs = @(Get-ServerProcess)
    if ($procs.Count -eq 0) {
        Write-Warn "服务端未在运行"
    }
    else {
        $pids = ($procs | ForEach-Object { $_.ProcessId }) -join ', '
        Write-Info "发现服务端进程: $pids"
        Write-Info "正在停止..."
        $procs | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
        Start-Sleep -Milliseconds 500
        $left = @(Get-ServerProcess)
        if ($left.Count -gt 0) {
            $leftPids = ($left | ForEach-Object { $_.ProcessId }) -join ', '
            Write-Err "服务端停止失败，进程仍在: $leftPids"
            $ok = $false
        }
        else {
            Write-Ok "服务端已停止"
        }
    }
    if (-not (Stop-Vite)) { $ok = $false }
    return $ok
}

function Invoke-Config {
    if (-not (Test-Path $Config)) {
        if (Test-Path $ConfigExample) {
            Copy-Item $ConfigExample $Config
            Write-Info "已从示例生成配置: $Config"
        }
        else {
            Write-Err "配置不存在: $Config"
            return
        }
    }
    Write-Info "常用配置项: server.api_port（服务端口）、server.public_host（回连地址）、auth.admin_password、auth.api_keys"
    notepad $Config
    Write-Info "已关闭编辑器。若修改了服务端口，需重启服务生效。"
}

function Show-Status {
    $mode = Get-BuildMode
    if ($mode -eq "") { $mode = "未知" }
    if (Test-Path $ServerBin) {
        Write-Host "  构建产物: $ServerBin（方式 $mode）"
    } else {
        Write-Host "  构建产物: 未构建"
    }
    $procs = @(Get-ServerProcess)
    if ($procs.Count -gt 0) {
        Write-Host "  服务端:   运行中（PID: $(($procs | ForEach-Object { $_.ProcessId }) -join ', ')）"
    } else {
        Write-Host "  服务端:   未运行"
    }
    if (Test-ViteRunning) {
        Write-Host "  Vite:     运行中（端口 $(Get-VitePort)，PID: $((Get-VitePids) -join ', ')）"
    } else {
        Write-Host "  Vite:     未运行"
    }
    if ($procs.Count -gt 0) {
        Write-Host "  入口:     http://127.0.0.1:$(Get-ApiPort)"
    }
    if (Test-ViteRunning) {
        Write-Host "  开发入口: http://127.0.0.1:$(Get-VitePort)（热更新，/api 代理到后端）"
    }
}

function Show-Usage {
    @"
ToShell 开发管理脚本（本仓库源码构建用；部署请用发布包内 install.ps1）

用法:
  .\Toshell.ps1 start [--dev]   启动。默认 deploy 方式（带内嵌前端）
                                --dev 为开发方式：纯 API 后端 + Vite 热更新（端口 $(Get-VitePort)）
  .\Toshell.ps1 stop            停止服务端与 Vite
  .\Toshell.ps1 status          查看构建产物、服务端与 Vite 的当前状态
  .\Toshell.ps1 build [--dev]   构建（--dev 构建不带内嵌前端的纯 API 版本）
  .\Toshell.ps1 clean           清理构建产物、日志与运行时状态
  .\Toshell.ps1 sync            仅把植入端模板同步到 release\（改模板后免于完整构建）
  .\Toshell.ps1 config          修改项目配置（编辑配置文件）
  .\Toshell.ps1 help            显示本帮助

两种启动方式产出的二进制不同（dev 不嵌入前端），切换方式时会提示重建。

产物位置：release\（复刻发布包布局，使本地与发布包的模板解析路径一致）
植入端模板唯一源：internal\server\builder\implant（+ implant_c）
"@
}

function Show-Menu {
    while ($true) {
        Write-Host ""
        Write-Host "=============================="
        Write-Host "  ToShell 管理菜单"
        Write-Host "=============================="
        Write-Host "  [1] 构建（deploy 方式，带内嵌前端）"
        Write-Host "  [2] 清理全部构建产物与日志"
        Write-Host "  [3] 启动（deploy 方式）"
        Write-Host "  [4] 启动（dev 方式，含 Vite 热更新）"
        Write-Host "  [5] 停止（服务端 + Vite）"
        Write-Host "  [6] 查看状态"
        Write-Host "  [7] 修改项目配置"
        Write-Host "  [8] 退出"
        Write-Host "=============================="
        $choice = Read-Host "请选择 [1-8]"
        switch ($choice) {
            "1" { Invoke-Build "deploy" | Out-Null }
            "2" { Invoke-Clean }
            "3" { Invoke-Start "deploy" }
            "4" { Invoke-Start "dev" }
            "5" { Invoke-Stop | Out-Null }
            "6" { Show-Status }
            "7" { Invoke-Config }
            "8" { Write-Info "退出"; return }
            default { Write-Warn "无效选择: $choice" }
        }
    }
}

# 解析 <命令> [--dev|--deploy]
function Resolve-Mode {
    param([string]$A)
    if ($A -eq "--dev") { return "dev" }
    if ($A -eq "" -or $A -eq "--deploy") { return "deploy" }
    Write-Err "未知参数: $A（可用: --dev）"
    return ""
}

# 主入口
if ([string]::IsNullOrWhiteSpace($Action)) {
    Show-Menu
}
else {
    switch -Regex ($Action.ToLower()) {
        '^(start|--start|-s)$' {
            $m = Resolve-Mode $Arg2
            if ($m -ne "") { Invoke-Start $m }
        }
        '^(build|--build|-b)$' {
            $m = Resolve-Mode $Arg2
            if ($m -ne "") { Invoke-Build $m | Out-Null }
        }
        '^(stop|--stop|-t)$'      { Invoke-Stop | Out-Null }
        '^(status|--status)$'     { Show-Status }
        '^(clean|--clean|-c)$'    { Invoke-Clean }
        '^(sync|--sync)$'         { Sync-ImplantTemplate | Out-Null }
        '^(config|--config|--edit-config|-e)$' { Invoke-Config }
        '^(help|--help|-h)$'      { Show-Usage }
        default                   { Write-Err "未知参数: $Action"; Show-Usage; exit 1 }
    }
}
