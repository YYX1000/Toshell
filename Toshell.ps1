# =====================================================================
#  ToShell 开发管理脚本（Windows PowerShell 5.1+）—— 非部署入口
#
#  用途：在本仓库里构建 / 启停服务端，面向"从源码开发"。
#  部署（解压发布包后安装并运行）请用发布包内的 install.ps1 —— 它会在没有 Go 的
#  机器上按需安装依赖，本脚本不做这件事。
#
#  两种使用模式：
#    1) 交互式菜单：不带参数直接运行      .\Toshell.ps1
#    2) 命令行参数：.\Toshell.ps1 <build|clean|start|stop|config|help>
#
#  若被执行策略阻止，可用：
#    powershell -NoProfile -ExecutionPolicy Bypass -File .\Toshell.ps1
#
#  为什么产物落在 release\ 而不是仓库根：
#    release\ 是"复刻发布包布局"的目录。服务端解析植入端模板时按
#    【配置 implant.template_dir → 环境变量 TOSHELL_IMPLANT_TEMPLATE_DIR →
#      exe 同目录 implant\ → exe 同目录 internal\server\builder\implant →
#      当前工作目录 internal\server\builder\implant】顺序回退。
#    把 toserver.exe 放在 release\ 下，exe 同目录就有 implant\，于是**本地开发与
#    发布包走完全相同的模板解析路径**，不会出现"本地能跑、发布包失效"。
#    因此本脚本 build 时会把模板源同步到 release\implant{,_c}\。
#
#  编码：本文件必须是 UTF-8 with BOM + CRLF。丢了 BOM 会让 PS 5.1 按 ANSI 解析，
#        中文全部乱码甚至语法报错。
# =====================================================================
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$Action = ""
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
# 植入端模板唯一源；release\implant{,_c} 是构建期生成物（见 Sync-ImplantTemplate）
$TemplateSrc = Join-Path $Root "internal\server\builder\implant"
$TemplateSrcC = Join-Path $Root "internal\server\builder\implant_c"

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

# 打印将被嵌入的前端产物标识（vite 在 index.html 里引用带内容哈希的 assets 文件）。
# 用途：npm 缺失时会沿用已有的 webdist，把标识打出来才能看出"嵌入的是哪一版前端"，
# 而不是默默编进一版旧界面后让人对着"界面怎么没变"排查半天。
function Get-WebAssetId {
    $idx = Join-Path $WebEmbed "index.html"
    if (-not (Test-Path $idx)) { return "(无前端产物)" }
    $m = Select-String -Path $idx -Pattern 'assets/index-[A-Za-z0-9_-]+\.(js|css)' -AllMatches |
        Select-Object -First 1
    if ($m -and $m.Matches.Count -gt 0) { return $m.Matches[0].Value }
    return "未知（index.html 中未找到 assets 引用）"
}

# 把植入端模板源同步到 release\，复刻发布包布局。
# 服务端在 exe 同目录找 implant\，所以这一步让本地开发与发布包走同一条解析路径。
# 用 Copy-Item 二进制复制：模板内是 CRLF，文本模式（Get-Content|Set-Content）会破坏字节一致。
function Sync-ImplantTemplate {
    if (-not (Test-Path $TemplateSrc)) {
        Write-Err "植入端模板源不存在: $TemplateSrc"
        return $false
    }
    if (-not (Test-Path (Join-Path $TemplateSrc "main.go"))) {
        Write-Err "模板源缺少 main.go（服务端以它判定目录是否有效）: $TemplateSrc"
        return $false
    }
    Write-Info "同步植入端模板到 release\（exe 同目录解析用）"
    Remove-Item (Join-Path $ReleaseDir "implant")   -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $ReleaseDir "implant_c") -Recurse -Force -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Path $ReleaseDir -Force | Out-Null
    Copy-Item $TemplateSrc (Join-Path $ReleaseDir "implant") -Recurse -Force
    # implant_c 必须与 implant 同级：builder.go / toolchain.go 以 ..\implant_c 推导它
    if (Test-Path $TemplateSrcC) {
        Copy-Item $TemplateSrcC (Join-Path $ReleaseDir "implant_c") -Recurse -Force
    }
    $n = @(Get-ChildItem (Join-Path $ReleaseDir "implant") -Recurse -File -ErrorAction SilentlyContinue).Count
    Write-Ok "模板已同步: $n 个文件"
    return $true
}

# ── 构建 ≠ 生效：判断正在运行的进程是不是"构建前的旧二进制" ──────────────
# 历史坑：构建成功后服务没重启，进程一直在内存里跑旧的 toserver.exe，
# 表现为"构建过了，但 Web 控制台和接口毫无变化"。所以必须能识别出这种状态。
function Get-StaleRunInfo {
    $procs = @(Get-ServerProcess)
    if ($procs.Count -eq 0) { return $null }
    if (-not (Test-Path $ServerBin)) { return $null }
    $binTime = (Get-Item $ServerBin).LastWriteTime
    $oldest = $procs | Sort-Object CreationDate | Select-Object -First 1
    # 2 秒容差：刚构建完就启动时，文件时间与进程启动时间几乎相同
    if ($binTime -le $oldest.CreationDate.AddSeconds(2)) { return $null }
    return [pscustomobject]@{
        Pids      = (($procs | ForEach-Object { $_.ProcessId }) -join ', ')
        StartedAt = $oldest.CreationDate
        BuiltAt   = $binTime
    }
}

# 跑着旧二进制时提示，经确认后重启；返回 $true 表示已重启
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
    Invoke-Stop
    if (Get-ServerProcess) { Write-Err "停止失败，终止重启"; return $false }
    Invoke-Start
    return $true
}

function Invoke-Build {
    Write-Info "构建服务端 + Web 前端（根目录: $Root）"
    Write-Info "  植入端不在此构建 —— 由服务端生成载荷时按需编译（go1.20 工具链）"
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Write-Err "未找到 Go 工具链（需要 Go >= 1.25），请先安装"
        return $false
    }

    if (Test-Path (Join-Path $Root "web\package.json")) {
        if (Get-Command npm -ErrorAction SilentlyContinue) {
            Write-Info "构建前端（npm ci && npm run build）..."
            Push-Location (Join-Path $Root "web")
            try {
                npm ci
                if ($LASTEXITCODE -ne 0) { throw "npm ci 失败" }
                npm run build
                if ($LASTEXITCODE -ne 0) { throw "npm run build 失败" }
            }
            finally { Pop-Location }

            Write-Info "同步前端产物到 cmd\server\webdist"
            Remove-Item $WebEmbed -Recurse -Force -ErrorAction SilentlyContinue
            New-Item -ItemType Directory -Path $WebEmbed -Force | Out-Null
            Copy-Item "$WebDist\*" "$WebEmbed\" -Recurse -Force
            Write-Ok "前端已同步，产物标识: $(Get-WebAssetId)"
        }
        else {
            # 注意：这里**不会**构建成"纯 API 版"。是否带 -tags webui 取决于
            # cmd\server\webdist\index.html 是否存在（见下方 $hasWebui 判定），而 webdist
            # 只在 clean 时删除。所以只要之前成功构建过一次前端，本次就会带着**上一版**
            # 前端产物构建。把产物标识打出来，避免"界面怎么没变"却查不出原因。
            Write-Warn "未找到 npm，跳过前端构建 —— 将沿用 cmd\server\webdist 中已有的前端产物"
            Write-Warn "  本次将嵌入的前端产物标识: $(Get-WebAssetId)"
            Write-Warn "  若前端有改动，请安装 Node.js >= 20 后重新执行 build，否则界面仍是旧版本"
        }
    }

    if (-not (Sync-ImplantTemplate)) { return $false }

    $commit = "dev"
    $c = & git -C $Root rev-parse --short HEAD 2>$null
    if ($LASTEXITCODE -eq 0 -and $c) { $commit = "$c".Trim() }

    $buildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
    $ldflags = "-s -w -X main.commit=$commit -X main.buildTime=$buildTime"

    $hasWebui = Test-Path (Join-Path $WebEmbed "index.html")
    $goArgs = @("build")
    if ($hasWebui) { $goArgs += @("-tags", "webui") }
    $goArgs += @("-ldflags", $ldflags, "-o", $ServerBin, "./cmd/server")

    $tagsDesc = if ($hasWebui) { "-tags webui " } else { "" }
    Write-Info "编译服务端（go build ${tagsDesc}-o $ServerBin）..."
    New-Item -ItemType Directory -Path $ReleaseDir -Force | Out-Null
    Push-Location $Root
    try {
        & go @goArgs
        if ($LASTEXITCODE -ne 0) { throw "go build 失败" }
    }
    finally { Pop-Location }

    if (-not (Test-Path $ServerBin)) { Write-Err "未生成产物: $ServerBin"; return $false }
    Write-Ok "构建完成: $ServerBin（commit=$commit）"
    # 构建 ≠ 生效：服务还在跑的话，它跑的是内存里的旧二进制
    Restart-ForStaleBinary | Out-Null
    return $true
}

function Invoke-Clean {
    Write-Info "清理构建产物与日志文件..."
    if (Get-ServerProcess) {
        Write-Warn "服务正在运行，正在运行的二进制可能无法删除；建议先停止服务"
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
    Remove-Item $LogOut -Force -ErrorAction SilentlyContinue
    Remove-Item $LogErr -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $Root "server.log") -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $Root "server.err.log") -Force -ErrorAction SilentlyContinue
    Write-Ok "构建产物与日志已清理（node_modules 保留）"

    $ans = Read-Host "是否同时清理数据库（SQLite 数据，sessions/tasks 等将丢失）？输入 yes 确认，其他任意键跳过"
    if ($ans -and $ans.ToLower() -eq "yes") {
        if (Get-ServerProcess) {
            Write-Info "数据库清理需要先停止服务，正在停止..."
            Invoke-Stop
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
    $procs = @(Get-ServerProcess)
    if ($procs.Count -gt 0) {
        # 跑着旧二进制时不能只警告就 return —— 那正是"构建完毫无变化"的根因：
        # 服务会一直跑内存里构建前的旧二进制，重新 start 是个静默空操作。
        if (Restart-ForStaleBinary) { return }
        $pids = ($procs | ForEach-Object { $_.ProcessId }) -join ', '
        Write-Warn "服务已在运行（PID: $pids）"
        return
    }
    if (-not (Test-Path $ServerBin)) {
        Write-Warn "未找到构建产物: $ServerBin"
        $ans = Read-Host "是否先执行源码构建？[Y/n]"
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
    Write-Info "启动服务..."
    Remove-Item $LogOut -Force -ErrorAction SilentlyContinue
    Remove-Item $LogErr -Force -ErrorAction SilentlyContinue
    Start-Process -FilePath $ServerBin -ArgumentList '-config','configs\server.yaml' -WorkingDirectory $ReleaseDir -RedirectStandardOutput $LogOut -RedirectStandardError $LogErr -WindowStyle Hidden
    Start-Sleep -Seconds 2

    $procs = @(Get-ServerProcess)
    if ($procs.Count -gt 0) {
        $pids = ($procs | ForEach-Object { $_.ProcessId }) -join ', '
        $port = Get-ApiPort
        Write-Ok "服务已启动（PID: $pids）"
        Write-Info "Web 控制台: http://127.0.0.1:${port}  （API 端口: ${port}）"
        try {
            $code = (Invoke-WebRequest -Uri "http://127.0.0.1:${port}/" -UseBasicParsing -TimeoutSec 5).StatusCode
            Write-Ok "健康检查通过（HTTP $code）"
        }
        catch {
            Write-Warn "健康检查异常，可查看 $LogErr"
        }
    }
    else {
        Write-Err "启动失败，请检查日志: $LogErr"
    }
}

function Invoke-Stop {
    $procs = @(Get-ServerProcess)
    if ($procs.Count -eq 0) {
        Write-Warn "服务未在运行"
        return
    }
    $pids = ($procs | ForEach-Object { $_.ProcessId }) -join ', '
    Write-Info "发现服务进程: $pids"
    Write-Info "正在停止..."
    $procs | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    Start-Sleep -Milliseconds 500
    $left = @(Get-ServerProcess)
    if ($left.Count -gt 0) {
        $leftPids = ($left | ForEach-Object { $_.ProcessId }) -join ', '
        Write-Err "停止失败，进程仍在: $leftPids"
    }
    else {
        Write-Ok "服务已停止"
    }
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

function Show-Usage {
    @"
ToShell 开发管理脚本（本仓库源码构建用；部署请用发布包内 install.ps1）

用法:
  .\Toshell.ps1                    进入交互式菜单
  .\Toshell.ps1 build              构建服务端 + Web 前端，并同步植入端模板到 release\
  .\Toshell.ps1 clean              清理全部构建产物、日志文件
  .\Toshell.ps1 start              启动服务（未构建时会询问是否先构建）
  .\Toshell.ps1 stop               停止正在运行的服务
  .\Toshell.ps1 config             修改项目配置（编辑配置文件）
  .\Toshell.ps1 sync               仅把植入端模板同步到 release\（改模板后免于完整构建）
  .\Toshell.ps1 help               显示本帮助

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
        Write-Host "  [1] 构建服务端 + Web 前端"
        Write-Host "  [2] 清理全部构建产物、日志文件"
        Write-Host "  [3] 启动服务"
        Write-Host "  [4] 停止正在运行的服务"
        Write-Host "  [5] 修改项目配置"
        Write-Host "  [6] 退出"
        Write-Host "=============================="
        $choice = Read-Host "请选择 [1-6]"
        switch ($choice) {
            "1" { Invoke-Build | Out-Null }
            "2" { Invoke-Clean }
            "3" { Invoke-Start }
            "4" { Invoke-Stop }
            "5" { Invoke-Config }
            "6" { Write-Info "退出"; return }
            default { Write-Warn "无效选择: $choice" }
        }
    }
}

# 主入口
if ([string]::IsNullOrWhiteSpace($Action)) {
    Show-Menu
}
else {
    switch -Regex ($Action.ToLower()) {
        '^(build|--build|-b)$'                { Invoke-Build | Out-Null }
        '^(clean|--clean|-c)$'                { Invoke-Clean }
        '^(start|--start|-s)$'                { Invoke-Start }
        '^(stop|--stop|-t)$'                  { Invoke-Stop }
        '^(config|--config|--edit-config|-e)$' { Invoke-Config }
        '^(sync|--sync)$'                     { Sync-ImplantTemplate | Out-Null }
        '^(help|--help|-h)$'                  { Show-Usage }
        default                               { Write-Err "未知参数: $Action"; Show-Usage; exit 1 }
    }
}
