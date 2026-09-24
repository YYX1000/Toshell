# =====================================================================
#  ToShell 开发管理脚本（Windows PowerShell 5.1+）—— 非部署入口
#
#  两种启动方式：
#    deploy（默认）  构建**带内嵌前端**的二进制并运行，单进程。
#                    运行时目录 = release\（配置 release\configs\server.yaml）
#    dev             用 `go run` 直接起**不带内嵌前端**的服务端，同时起 Vite 开发
#                    服务器。改 web\src\** 即时热更新，改 Go 代码重启即生效（无需
#                    先构建）；界面走 Vite 端口，后端只提供 API。
#                    运行时目录 = 仓库根（配置 configs\server.yaml）
#
#    启动：.\Toshell.ps1 start            部署方式
#          .\Toshell.ps1 start --dev      开发方式
#          .\Toshell.ps1 stop             两种都停（服务端 + Vite）
#          .\Toshell.ps1 status           查看当前状态
#          .\Toshell.ps1 clean            清理构建产物、日志、运行时状态
#
#  其余命令：build / sync / config / help / status（不带参数进入交互式菜单）。
#
#  ⚠️ 两种方式是**两个独立实例**：运行时目录不同（release\ vs 仓库根），因此配置、
#     SQLite 库、载荷输出目录都不共享，且 API 端口默认都是 18081 —— **不能同时跑**。
#
#  部署（解压发布包后安装并运行）请用发布包内的 install.ps1 —— 它会在没有 Go 的
#  机器上按需安装依赖，本脚本不做这件事。
#
#  为什么 deploy 的产物落在 release\ 而不是仓库根：
#    release\ 是"复刻发布包布局"的目录。服务端解析植入端模板时按
#    【配置 implant.template_dir → 环境变量 TOSHELL_IMPLANT_TEMPLATE_DIR →
#      exe 同目录 implant\ → exe 同目录 internal\server\builder\implant →
#      当前工作目录同路径】顺序回退。把 toserver.exe 放在 release\ 下，exe 同目录
#    就有 implant\，于是**deploy 与发布包走完全相同的模板解析路径**。
#    dev 走最后一条回退（cwd = 仓库根 → internal\server\builder\implant，即模板源），
#    所以改模板在 dev 下立刻生效、连 sync 都不用。
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

# deploy 的运行时目录与配置
$ReleaseDir = Join-Path $Root "release"
$ServerBin = Join-Path $ReleaseDir "toserver.exe"
$Config = Join-Path $ReleaseDir "configs\server.yaml"
$ConfigExample = Join-Path $ReleaseDir "configs\server.yaml.example"
$LogOut = Join-Path $ReleaseDir "server.log"
$LogErr = Join-Path $ReleaseDir "server.err.log"
$ImplantsDir = Join-Path $ReleaseDir "implants"
$WebDist = Join-Path $Root "web\dist"
$WebEmbed = Join-Path $Root "cmd\server\webdist"

# dev 的运行时目录与配置（仓库根，与 deploy 完全独立）
$DevConfig = Join-Path $Root "configs\server.yaml"
$DevConfigExample = Join-Path $Root "configs\server.yaml.example"
$DevLogOut = Join-Path $ReleaseDir "dev-server.log"
$DevLogErr = Join-Path $ReleaseDir "dev-server.err.log"

# 运行时状态：服务端当前的启动方式（dev|deploy）。status 用它说明"现在跑的是哪种"，
# "旧二进制"判定也只在 deploy 下有意义（dev 每次 go run 都用最新代码）。
$RunModeFile = Join-Path $ReleaseDir ".run-mode"
$VitePidFile = Join-Path $ReleaseDir "vite.pid"
$ViteLog = Join-Path $ReleaseDir "vite.log"

function Write-Info  { Write-Host "[信息] $args" -ForegroundColor Cyan }
function Write-Ok    { Write-Host "[成功] $args" -ForegroundColor Green }
function Write-Warn  { Write-Warning "$args" }
function Write-Err   { Write-Host "[错误] $args" -ForegroundColor Red }

# ── 端口与进程 ───────────────────────────────────────────────────────
# 服务端一律**按端口**定位，不按进程名：deploy 的进程叫 toserver.exe，dev 是 go run
# 出来的 server.exe —— 后者名字太通用，按名字查会误伤其它进程。

function Get-PortOfConfig {
    param([string]$Path)
    if (Test-Path $Path) {
        $m = Select-String -Path $Path -Pattern '^\s*api_port:\s*(\d+)' | Select-Object -First 1
        if ($m -and $m.Matches[0].Groups[1].Success) { return [int]$m.Matches[0].Groups[1].Value }
    }
    return 18081
}

function Get-DeployApiPort { return Get-PortOfConfig $Config }
function Get-DevApiPort    { return Get-PortOfConfig $DevConfig }

function Get-PidsOnPort {
    param([int]$Port)
    $conns = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
    if ($conns) { return @($conns | Select-Object -ExpandProperty OwningProcess -Unique) }
    return @()
}

# 服务端进程：两种方式的端口都查（stop 时不知道在跑哪种）
function Get-ServerPids {
    # 注意写法：@(A, B) 会被当作「命令 A 带参数 B」，必须各自加括号才是两个调用结果
    $ports = @((Get-DeployApiPort), (Get-DevApiPort)) | Sort-Object -Unique
    $out = @()
    foreach ($p in $ports) { $out += Get-PidsOnPort $p }
    return @($out | Sort-Object -Unique)
}
function Test-ServerRunning { return (@(Get-ServerPids).Count -gt 0) }

function Get-RunMode {
    if (Test-Path $RunModeFile) { return (Get-Content $RunModeFile -First 1).Trim() }
    return ""
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
function Get-VitePids { return Get-PidsOnPort (Get-VitePort) }
function Test-ViteRunning { return (@(Get-VitePids).Count -gt 0) }

# ── Vite 开发服务器 ──────────────────────────────────────────────────

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
    # 只杀 npm 的 PID 会留下 vite 子进程继续占着端口（实测过），停止时按端口定位
    $proc = Start-Process -FilePath "npm" -ArgumentList "run", "dev" `
        -WorkingDirectory (Join-Path $Root "web") `
        -RedirectStandardOutput $ViteLog -RedirectStandardError "$ViteLog.err" `
        -WindowStyle Hidden -PassThru
    if ($proc) { Set-Content -Path $VitePidFile -Value $proc.Id -Encoding ASCII }
    Start-Sleep -Seconds 4

    if (Test-ViteRunning) {
        Write-Ok "Vite 已启动（PID: $((Get-VitePids) -join ', ')）"
        Write-Info "  前端（热更新）: http://127.0.0.1:$p"
        Write-Info "  /api 由 Vite 代理到后端（改代理目标用环境变量 VITE_PROXY_TARGET）"
        return $true
    }
    Write-Err "Vite 启动失败，见日志: $ViteLog"
    return $false
}

# 未在运行时静默返回 —— Invoke-Stop 会无条件调用它
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
    if (Test-ViteRunning) { Start-Sleep -Seconds 2 }
    Remove-Item $VitePidFile -Force -ErrorAction SilentlyContinue
    if (Test-ViteRunning) {
        Write-Err "Vite 未能停止，仍占用端口 $(Get-VitePort)（PID: $((Get-VitePids) -join ', ')）"
        return $false
    }
    Write-Ok "Vite 已停止"
    return $true
}

# ── 构建 ≠ 生效：判断正在运行的是不是"构建前的旧二进制" ────────────────
# 仅对 deploy 有意义：dev 用 go run，每次都是最新代码。
function Get-StaleRunInfo {
    if ((Get-RunMode) -ne "deploy") { return $null }
    $pids = @(Get-ServerPids)
    if ($pids.Count -eq 0) { return $null }
    if (-not (Test-Path $ServerBin)) { return $null }
    $binTime = (Get-Item $ServerBin).LastWriteTime
    $oldest = $pids | ForEach-Object { Get-Process -Id $_ -ErrorAction SilentlyContinue } |
        Sort-Object StartTime | Select-Object -First 1
    if (-not $oldest) { return $null }
    if ($binTime -le $oldest.StartTime.AddSeconds(2)) { return $null }
    return [pscustomobject]@{
        Pids      = ($pids -join ', ')
        StartedAt = $oldest.StartTime
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
    Invoke-Start "deploy"
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
    Write-Info "构建服务端 + Web 前端（deploy 方式，根目录: $Root）"
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Write-Err "未找到 Go 工具链（需要 Go >= 1.25），请先安装"
        return $false
    }
    if (-not (Invoke-Devtool build)) { Write-Err "构建失败"; return $false }
    if (-not (Test-Path $ServerBin)) { Write-Err "未生成产物: $ServerBin"; return $false }
    Write-Ok "构建完成: $ServerBin"
    Restart-ForStaleBinary | Out-Null
    return $true
}

function Invoke-Clean {
    Write-Info "清理构建产物与日志文件..."
    if (Test-ServerRunning) {
        Write-Warn "服务端正在运行（PID: $((Get-ServerPids) -join ', ')）；建议先停止服务（本脚本不会自动停）"
    }
    if (Test-ViteRunning) {
        Write-Warn "Vite 正在运行（PID: $((Get-VitePids) -join ', ')），先停止它"
        Stop-Vite | Out-Null
    }
    Remove-Item $ServerBin -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $Root "toserver.exe") -Force -ErrorAction SilentlyContinue
    if (Test-Path $ImplantsDir) { Remove-Item "$ImplantsDir\*" -Recurse -Force -ErrorAction SilentlyContinue }
    # release\implant{,_c} 是 deploy 构建时从模板源生成的产物（不是源码），一并清理
    Remove-Item (Join-Path $ReleaseDir "implant")   -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $ReleaseDir "implant_c") -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item $WebDist -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item $WebEmbed -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $Root "web\tsconfig.tsbuildinfo") -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $Root "web\tsconfig.node.tsbuildinfo") -Force -ErrorAction SilentlyContinue
    # 运行时状态：运行方式、Vite 的 PID/日志、两种方式的服务端日志
    Remove-Item $RunModeFile -Force -ErrorAction SilentlyContinue
    Remove-Item $VitePidFile -Force -ErrorAction SilentlyContinue
    Remove-Item $ViteLog -Force -ErrorAction SilentlyContinue
    Remove-Item "$ViteLog.err" -Force -ErrorAction SilentlyContinue
    Remove-Item $LogOut -Force -ErrorAction SilentlyContinue
    Remove-Item $LogErr -Force -ErrorAction SilentlyContinue
    Remove-Item $DevLogOut -Force -ErrorAction SilentlyContinue
    Remove-Item $DevLogErr -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $Root "server.log") -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $Root "server.err.log") -Force -ErrorAction SilentlyContinue
    Write-Ok "构建产物与日志已清理（node_modules 保留）"

    $ans = Read-Host "是否同时清理数据库（SQLite 数据，sessions/tasks 等将丢失）？输入 yes 确认，其他任意键跳过"
    if ($ans -and $ans.ToLower() -eq "yes") {
        if (Test-ServerRunning) {
            Write-Info "数据库清理需要先停止服务，正在停止..."
            Invoke-Stop | Out-Null
        }
        # 两种方式各有自己的库（deploy: release\data；dev: .\data）
        Remove-Item (Join-Path $ReleaseDir "data\toshell.db") -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $ReleaseDir "data\toshell.db-wal") -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $ReleaseDir "data\toshell.db-shm") -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $Root "data\toshell.db") -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $Root "data\toshell.db-wal") -Force -ErrorAction SilentlyContinue
        Remove-Item (Join-Path $Root "data\toshell.db-shm") -Force -ErrorAction SilentlyContinue
        Write-Ok "两处数据库均已清理（服务重启后将自动重建空库）"
    }
    else {
        Write-Info "已跳过数据库清理"
    }
}

# 轮询健康检查（dev 首次 go run 要编译，耗时可达数十秒，不能只 sleep 固定秒数）
function Wait-Healthy {
    param([int]$Port, [int]$TimeoutSec = 80)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        try {
            $code = (Invoke-WebRequest -Uri "http://127.0.0.1:$Port/api/v1/health" -UseBasicParsing -TimeoutSec 5).StatusCode
            if ($code -eq 200) { return $true }
        }
        catch { }
        Start-Sleep -Seconds 2
    }
    return $false
}

# ── 启动 ─────────────────────────────────────────────────────────────

function Start-Dev {
    if (Test-ServerRunning) {
        Write-Warn "服务端已在运行（PID: $((Get-ServerPids) -join ', ')），请先 stop（dev 与 deploy 端口相同，不能同时跑）"
        return
    }
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Write-Err "未找到 Go 工具链（需要 Go >= 1.25）"
        return
    }

    # dev 用仓库根的配置（与 deploy 的 release\configs 是两份独立配置）
    if (-not (Test-Path $DevConfig)) {
        if (Test-Path $DevConfigExample) {
            Write-Warn "dev 配置不存在，已从示例生成: $DevConfig"
            Copy-Item $DevConfigExample $DevConfig
        }
        else {
            Write-Err "dev 配置不存在且无示例文件: $DevConfig"
            return
        }
    }

    $port = Get-DevApiPort
    Write-Info "以 go run 启动服务端（开发方式，纯 API，cwd=$Root）..."
    Write-Info "  首次运行需编译，可能等数十秒；改 Go 代码后重启即生效，无需先构建"
    Remove-Item $DevLogOut -Force -ErrorAction SilentlyContinue
    Remove-Item $DevLogErr -Force -ErrorAction SilentlyContinue
    Start-Process -FilePath "go" -ArgumentList "run", "./cmd/server", "-config", "configs/server.yaml" `
        -WorkingDirectory $Root `
        -RedirectStandardOutput $DevLogOut -RedirectStandardError $DevLogErr `
        -WindowStyle Hidden | Out-Null
    Set-Content -Path $RunModeFile -Value "dev" -Encoding ASCII

    Write-Info "等待服务端就绪（轮询 /api/v1/health，最多 80s）..."
    if (Wait-Healthy -Port $port) {
        Write-Ok "服务端已就绪（PID: $((Get-ServerPids) -join ', ')）"
    }
    elseif (Test-ServerRunning) {
        Write-Warn "进程在跑但健康检查未通过，可查看日志: $DevLogErr"
    }
    else {
        Write-Err "启动失败，请检查日志: $DevLogErr"
        Remove-Item $RunModeFile -Force -ErrorAction SilentlyContinue
        return
    }

    Write-Info "后端纯 API: http://127.0.0.1:${port}（开发方式不嵌入前端，界面走 Vite）"
    Write-Info "数据与日志在仓库根: 配置 configs\server.yaml、库 .\data\toshell.db、载荷 .\implants\"
    Write-Warn "这是**独立的开发实例**：监听器配置与 deploy 的 release\data 不共享"

    Start-Vite | Out-Null
}

function Start-Deploy {
    if (Test-ServerRunning) {
        # 跑着旧二进制时不能只警告就 return —— 那正是"构建完毫无变化"的根因。
        if (Restart-ForStaleBinary) { return }
        Write-Warn "服务端已在运行（PID: $((Get-ServerPids) -join ', ')），请先 stop"
        return
    }

    if (-not (Test-Path $ServerBin)) {
        Write-Warn "未找到构建产物: $ServerBin"
        $ans = Read-Host "是否现在构建？[Y/n]"
        if ($ans -eq "" -or $ans -match '^[Yy]') {
            if (-not (Invoke-Build)) { Write-Err "构建失败，终止启动"; return }
        }
        else {
            Write-Err "用户拒绝构建，终止启动"
            return
        }
    }

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

    $port = Get-DeployApiPort
    Write-Info "启动服务端（部署方式，带内嵌前端）..."
    Remove-Item $LogOut -Force -ErrorAction SilentlyContinue
    Remove-Item $LogErr -Force -ErrorAction SilentlyContinue
    Start-Process -FilePath $ServerBin -ArgumentList '-config','configs\server.yaml' `
        -WorkingDirectory $ReleaseDir `
        -RedirectStandardOutput $LogOut -RedirectStandardError $LogErr -WindowStyle Hidden
    Set-Content -Path $RunModeFile -Value "deploy" -Encoding ASCII
    Start-Sleep -Seconds 2

    if (-not (Test-ServerRunning)) {
        Write-Err "启动失败，请检查日志: $LogErr"
        Remove-Item $RunModeFile -Force -ErrorAction SilentlyContinue
        return
    }
    Write-Ok "服务端已启动（PID: $((Get-ServerPids) -join ', ')）"
    Write-Info "Web 控制台: http://127.0.0.1:${port}"
    if (Wait-Healthy -Port $port -TimeoutSec 20) {
        Write-Ok "健康检查通过（/api/v1/health → 200）"
    }
    else {
        Write-Warn "健康检查未通过，可查看 $LogErr"
    }
}

function Invoke-Start {
    param([string]$Mode = "deploy")
    switch ($Mode) {
        "dev"    { Start-Dev }
        "deploy" { Start-Deploy }
        default  { Write-Err "未知启动方式: $Mode（可用: --dev）" }
    }
}

function Invoke-Stop {
    $ok = $true
    if (Test-ServerRunning) {
        $pids = @(Get-ServerPids)
        Write-Info "发现服务端进程: $($pids -join ', ')（方式: $(Get-RunMode)）"
        Write-Info "正在停止..."
        # dev 的进程是 go run 的子进程，kill 父进程可能留下它；两种方式都按端口定位到
        # 真正监听的进程再收掉
        foreach ($sp in $pids) { & taskkill /PID $sp /T /F 2>$null | Out-Null }
        Start-Sleep -Seconds 1
        if (Test-ServerRunning) { Start-Sleep -Seconds 1 }
        if (Test-ServerRunning) {
            Write-Err "服务端停止失败，进程仍在: $((Get-ServerPids) -join ', ')"
            $ok = $false
        }
        else {
            Write-Ok "服务端已停止"
        }
    }
    else {
        Write-Warn "服务端未在运行"
    }
    Remove-Item $RunModeFile -Force -ErrorAction SilentlyContinue
    if (-not (Stop-Vite)) { $ok = $false }
    return $ok
}

function Invoke-Config {
    param([string]$Mode = "deploy")
    $f = $Config
    $ex = $ConfigExample
    if ($Mode -eq "dev") { $f = $DevConfig; $ex = $DevConfigExample }
    if (-not (Test-Path $f)) {
        if (Test-Path $ex) {
            Copy-Item $ex $f
            Write-Info "已从示例生成配置: $f"
        }
        else {
            Write-Err "配置不存在且无示例: $f"
            return
        }
    }
    Write-Info "常用配置项: server.api_port（服务端口）、server.public_host（回连地址）、auth.admin_password、auth.api_keys"
    notepad $f
    Write-Info "已关闭编辑器。若修改了服务端口，需重启服务生效。"
}

function Show-Status {
    if (Test-Path $ServerBin) {
        Write-Host "  构建产物: $ServerBin"
    } else {
        Write-Host "  构建产物: 未构建（deploy 方式需要；dev 方式用 go run，不需要）"
    }
    if (Test-ServerRunning) {
        $m = Get-RunMode
        if ($m -eq "") { $m = "未知" }
        Write-Host "  服务端:   运行中（方式 $m，PID: $((Get-ServerPids) -join ', ')）"
        $port = if ($m -eq "dev") { Get-DevApiPort } else { Get-DeployApiPort }
        Write-Host "  入口:     http://127.0.0.1:$port"
    } else {
        Write-Host "  服务端:   未运行"
    }
    if (Test-ViteRunning) {
        Write-Host "  Vite:     运行中（端口 $(Get-VitePort)，PID: $((Get-VitePids) -join ', ')）"
        Write-Host "  开发入口: http://127.0.0.1:$(Get-VitePort)（热更新，/api 代理到后端）"
    } else {
        Write-Host "  Vite:     未运行"
    }
}

function Show-Usage {
    @"
ToShell 开发管理脚本（本仓库源码构建用；部署请用发布包内 install.ps1）

用法:
  .\Toshell.ps1 start [--dev]   启动。默认 deploy（构建带内嵌前端的二进制并运行）
                                --dev 用 go run 起纯 API 后端 + Vite 热更新（端口 $(Get-VitePort)）
  .\Toshell.ps1 stop            停止服务端与 Vite
  .\Toshell.ps1 status          查看构建产物、服务端与 Vite 的当前状态
  .\Toshell.ps1 build           构建 deploy 方式的二进制（dev 方式用 go run，无需构建）
  .\Toshell.ps1 clean           清理构建产物、日志与运行时状态
  .\Toshell.ps1 sync            仅把植入端模板同步到 release\（改模板后免于完整构建）
  .\Toshell.ps1 config [--dev]  修改配置（默认 deploy 的 release\configs\server.yaml）
  .\Toshell.ps1 help            显示本帮助

两种方式是**两个独立实例**，运行时目录不同，因此配置/SQLite 库/载荷目录都不共享，
且 API 端口默认都是 18081 —— 不能同时跑：
  deploy  release\configs\server.yaml + release\data\toshell.db，单进程带内嵌前端
  dev     configs\server.yaml        + .\data\toshell.db，      go run + Vite 热更新
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
        Write-Host "  [4] 启动（dev 方式，go run + Vite 热更新）"
        Write-Host "  [5] 停止（服务端 + Vite）"
        Write-Host "  [6] 查看状态"
        Write-Host "  [7] 修改配置"
        Write-Host "  [8] 退出"
        Write-Host "=============================="
        $choice = Read-Host "请选择 [1-8]"
        switch ($choice) {
            "1" { Invoke-Build | Out-Null }
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

# 解析第二个参数：--dev / --deploy / 空
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
            if ($Arg2 -eq "--dev") {
                Write-Info "dev 方式用 go run 启动，不需要构建 —— 直接 .\Toshell.ps1 start --dev"
            }
            elseif ($Arg2 -eq "" -or $Arg2 -eq "--deploy") { Invoke-Build | Out-Null }
            else { Write-Err "未知参数: $Arg2" }
        }
        '^(stop|--stop|-t)$'      { Invoke-Stop | Out-Null }
        '^(status|--status)$'     { Show-Status }
        '^(clean|--clean|-c)$'    { Invoke-Clean }
        '^(sync|--sync)$'         { Sync-ImplantTemplate | Out-Null }
        '^(config|--config|--edit-config|-e)$' {
            $m = Resolve-Mode $Arg2
            if ($m -ne "") { Invoke-Config $m }
        }
        '^(help|--help|-h)$'      { Show-Usage }
        default                   { Write-Err "未知参数: $Action"; Show-Usage; exit 1 }
    }
}
