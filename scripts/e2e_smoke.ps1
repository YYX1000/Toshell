#Requires -Version 5.1
<#
.SYNOPSIS
    ToShell 端到端冒烟（发版门禁 P0-4）：一条命令跑完，失败非 0 退出。

.DESCRIPTION
    覆盖人工点测最容易漏掉的环节：
      1) 服务端能否按当前源码构建（历史事故：改动后忘重编服务端）
      2) 临时服务端能否起来 + /api/v1/health 带 Key 通过
      3) 鉴权真的生效（不带 Key 访问受保护接口必须 401）
      4) /api/v1/builders 能力接口结构完整（formats / evasion.garble_available）
      5) 模板三档载荷构建：windows/amd64 full+light、linux/amd64 full
         （历史事故：模板档位构建失败、下载地址/大小/sha256 对不上）
      6) 关键路由存在性 + 会话不存在时的错误码
      7) 真实 Windows 载荷上线 + 下发 whoami 拿到输出（可 -SkipImplant 跳过）
    跑完打印中文清单（每项 ✅/⚠️/❌），存在 ❌ 则 exit 1。

    幂等：端口随机（18000-19000 内挑两个连续空闲端口）、临时目录随机并默认删除，
    不依赖调用者当前目录，也不改动仓库内任何文件。

.PARAMETER Port
    控制台/API 端口，默认在 18000-19000 之间随机挑一个未占用端口（C2 TCP 监听用 Port+1）。
    注意：服务端实际监听 server.api_port（默认 8081），脚本会把 server.port 与
    server.api_port 同时设为该端口。

.PARAMETER WorkDir
    临时工作目录，默认 $env:TEMP\toshell-e2e-<随机数>。

.PARAMETER SkipImplant
    跳过"真实植入端上线 + 任务下发"环节（等价于设置环境变量 TOSHELL_E2E_SKIP_IMPLANT=1）。
    跳过只降级该环节（记 ⚠️），其它环节仍必须通过。

.PARAMETER KeepArtifacts
    保留临时目录（含服务端日志、构建产物），便于排查。

.PARAMETER ServerExe
    使用已构建好的服务端 exe，跳过"重新构建服务端"环节（该环节记 ⚠️：未验证二进制与源码一致）。

.PARAMETER RequireImplant
    要求植入端环节必须成功。本机安全软件若拦截新生成的未签名 PE（Access is denied 且文件被删），
    默认只记 ⚠️ 不阻断；加本开关则该情况记 ❌ 并 exit 1。

.PARAMETER ApiKey
    冒烟用 API Key，默认 smoke-key（与脚本生成的最小配置一致）。

.EXAMPLE
    powershell -NoProfile -ExecutionPolicy Bypass -File scripts\e2e_smoke.ps1

.EXAMPLE
    powershell -NoProfile -ExecutionPolicy Bypass -File scripts\e2e_smoke.ps1 -SkipImplant -KeepArtifacts
#>
[CmdletBinding()]
param(
    [int]$Port = 0,
    [string]$WorkDir = '',
    [switch]$SkipImplant,
    [switch]$KeepArtifacts,
    [string]$ServerExe = '',
    [switch]$RequireImplant,
    [string]$ApiKey = 'smoke-key'
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false) } catch { }

# ─── 全局状态 ────────────────────────────────────────────────────────────────
$script:RepoRoot       = Split-Path -Parent $PSScriptRoot
$script:BaseUrl        = ''
$script:ApiPort        = 0
$script:ListenerPort   = 0
$script:ServerProc     = $null
$script:ImplantProc    = $null
$script:OnlineSessionId = ''
$script:WorkDir        = ''
$script:PayloadDir     = ''
$script:SrvOutLog      = ''
$script:SrvErrLog      = ''
$script:ImpOutLog      = ''
$script:ImpErrLog      = ''
$script:Payloads       = New-Object System.Collections.ArrayList
$script:Results        = New-Object System.Collections.ArrayList
$script:KeepArtifacts  = [bool]$KeepArtifacts
$script:RequireImplant = [bool]$RequireImplant
$script:SkipImpl       = ([bool]$SkipImplant) -or ($env:TOSHELL_E2E_SKIP_IMPLANT -eq '1')
$script:ApiKey         = $ApiKey

# ─── 结果收集 ────────────────────────────────────────────────────────────────
function Add-Result {
    param([string]$Name, [string]$Level, [string]$Detail)
    [void]$script:Results.Add([pscustomobject]@{ Name = $Name; Level = $Level; Detail = $Detail })
    $tag = '[OK]'
    $color = 'Green'
    if ($Level -eq 'WARN') { $tag = '[WARN]'; $color = 'Yellow' }
    if ($Level -eq 'FAIL') { $tag = '[FAIL]'; $color = 'Red' }
    Write-Host ("  {0} {1}：{2}" -f $tag, $Name, $Detail) -ForegroundColor $color
}

function Fail-Fatal {
    param([string]$Name, [string]$Detail)
    Add-Result -Name $Name -Level 'FAIL' -Detail $Detail
    throw ("E2E_FATAL: " + $Name)
}

# ─── 通用工具 ────────────────────────────────────────────────────────────────
function Test-IsWindows { return ($env:OS -eq 'Windows_NT') }

function Test-PortFree {
    param([int]$Port)
    $listener = $null
    try {
        $listener = New-Object System.Net.Sockets.TcpListener([System.Net.IPAddress]::Any, $Port)
        $listener.Start()
        return $true
    } catch {
        return $false
    } finally {
        if ($null -ne $listener) { try { $listener.Stop() } catch { } }
    }
}

function Get-FreePortPair {
    param([int]$Min = 18000, [int]$Max = 19000)
    for ($i = 0; $i -lt 200; $i++) {
        $candidate = Get-Random -Minimum $Min -Maximum ($Max - 1)
        if ((Test-PortFree $candidate) -and (Test-PortFree ($candidate + 1))) { return $candidate }
    }
    return 0
}

function Test-PortListening {
    param([int]$Port, [int]$TimeoutMs = 1500)
    $client = $null
    try {
        $client = New-Object System.Net.Sockets.TcpClient
        $task = $client.ConnectAsync('127.0.0.1', $Port)
        if (-not $task.Wait($TimeoutMs)) { return $false }
        return $client.Connected
    } catch {
        return $false
    } finally {
        if ($null -ne $client) { try { $client.Close() } catch { } }
    }
}

function Test-ProcAlive {
    param($Proc)
    if ($null -eq $Proc) { return $false }
    try { return (-not $Proc.HasExited) } catch { return $false }
}

function Stop-ProcSafe {
    param($Proc, [string]$Label)
    if ($null -eq $Proc) { return }
    $procId = 0
    try { $procId = $Proc.Id } catch { }
    try {
        if (-not $Proc.HasExited) {
            Stop-Process -Id $procId -Force -ErrorAction Stop
            try { [void]$Proc.WaitForExit(10000) } catch { }
            Write-Host ("  [清理] 已停止{0}（PID {1}）" -f $Label, $procId) -ForegroundColor DarkGray
        } else {
            Write-Host ("  [清理] {0}（PID {1}）此前已退出" -f $Label, $procId) -ForegroundColor DarkGray
        }
    } catch {
        Write-Host ("  [清理] {0}（PID {1}）停止失败：{2}" -f $Label, $procId, $_.Exception.Message) -ForegroundColor DarkYellow
    }
}

function Show-LogTail {
    param([string]$Path, [int]$Lines = 30, [string]$Label = '日志')
    if ($Path -eq '' -or -not (Test-Path -LiteralPath $Path)) { return }
    Write-Host ("  ---- {0} 末尾 {1} 行（{2}）----" -f $Label, $Lines, $Path) -ForegroundColor DarkGray
    try {
        Get-Content -LiteralPath $Path -Tail $Lines -ErrorAction SilentlyContinue | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray }
    } catch { }
}

# 只取首行，避免把整个构建日志塞进结果明细。
function Get-FirstLine {
    param([string]$Text, [int]$Max = 200)
    if ([string]::IsNullOrWhiteSpace($Text)) { return '(无输出)' }
    $line = ($Text -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -First 1)
    if ($null -eq $line) { return '(无输出)' }
    if ($line.Length -gt $Max) { $line = $line.Substring(0, $Max) + '...' }
    return $line
}

# HTTP 帮助函数：永不抛异常，统一返回 StatusCode / Content / Error。
# StatusCode = 0 表示连接层面失败（服务未起、端口不对、超时）。
function Invoke-TshApi {
    param(
        [string]$Method = 'GET',
        [string]$Url,
        [string]$ApiKey = '',
        [string]$Body = '',
        [string]$ContentType = 'application/json',
        [int]$TimeoutSec = 30,
        [string]$OutFile = ''
    )
    $headers = @{}
    if ($ApiKey -ne '') { $headers['X-API-Key'] = $ApiKey }
    $params = @{
        Method = $Method
        Uri = $Url
        Headers = $headers
        TimeoutSec = $TimeoutSec
        UseBasicParsing = $true
        ErrorAction = 'Stop'
    }
    if ($Body -ne '') { $params['Body'] = $Body; $params['ContentType'] = $ContentType }
    if ($OutFile -ne '') { $params['OutFile'] = $OutFile }

    $status = 0
    $content = ''
    $errText = ''
    try {
        $resp = Invoke-WebRequest @params
        if ($OutFile -ne '') {
            # 关键：PowerShell 5.1 用 -OutFile 时，Invoke-WebRequest 不返回带 StatusCode 的响应对象
            # （$resp.StatusCode 为空 → [int] 转换得 0），会让调用方误判成"HTTP 0 连接层失败"。
            # 既然没有抛异常、文件已落盘，这里直接判定 200（调用方还会核对文件大小与 sha256）。
            $status = 200
        } else {
            $status = [int]$resp.StatusCode
            $content = [string]$resp.Content
        }
    } catch {
        $errText = $_.Exception.Message
        $respObj = $null
        try { $respObj = $_.Exception.Response } catch { $respObj = $null }
        if ($null -ne $respObj) {
            try { $status = [int]$respObj.StatusCode } catch { $status = 0 }
            try {
                $stream = $respObj.GetResponseStream()
                if ($null -ne $stream) {
                    $reader = New-Object System.IO.StreamReader($stream)
                    $content = $reader.ReadToEnd()
                    $reader.Close()
                }
            } catch { }
        }
        if ($content -eq '') {
            try { if ($null -ne $_.ErrorDetails -and $_.ErrorDetails.Message) { $content = [string]$_.ErrorDetails.Message } } catch { }
        }
    }
    return [pscustomobject]@{ StatusCode = $status; Content = $content; Error = $errText }
}

# 判断一个异常消息是否像"安全软件拦截/删文件"（本机 360/电脑管家等会直接拒绝执行新生成的未签名 PE）。
function Test-AvBlockMessage {
    param([string]$Text)
    if ([string]::IsNullOrWhiteSpace($Text)) { return $false }
    $t = $Text.ToLower()
    foreach ($k in @('access is denied', 'denied', '拒绝访问', 'blocked', 'quarantine', 'virus', '木马', '威胁', 'threat')) {
        if ($t.Contains($k)) { return $true }
    }
    return $false
}

# ─── 接口封装 ────────────────────────────────────────────────────────────────
function Get-SessionIds {
    # 返回会话 ID 数组；查询失败返回 $null（与"空会话列表"区分）。
    $ids = @()
    $r = Invoke-TshApi -Method 'GET' -Url ($script:BaseUrl + '/api/v1/sessions') -ApiKey $script:ApiKey -TimeoutSec 20
    if ($r.StatusCode -ne 200) { return $null }
    $obj = $null
    try { $obj = $r.Content | ConvertFrom-Json } catch { return $null }
    if ($null -eq $obj) { return $null }
    foreach ($s in $obj.sessions) { $ids += [string]$s.id }
    return , $ids
}

# 路由存在性判定（写清楚判定规则）：
#   404 = 路由缺失（gorilla mux 未注册 / SPA 兜底）→ 失败
#   400/401/403/409 = 路由存在（参数/凭据/状态被拒）→ 通过
#   5xx = 路由存在但服务端内部错误 → 失败
function Test-Route {
    param(
        [string]$Label,
        [string]$Method,
        [string]$Path,
        [string]$Body = '',
        [int[]]$PassCodes = @(400, 401, 403, 409)
    )
    $r = Invoke-TshApi -Method $Method -Url ($script:BaseUrl + $Path) -ApiKey $script:ApiKey -Body $Body -TimeoutSec 20
    $s = $r.StatusCode
    if ($PassCodes -contains $s) {
        Add-Result -Name $Label -Level 'OK' -Detail ("HTTP {0}（路由存在且鉴权正常）" -f $s)
        return
    }
    if ($s -eq 404) {
        Add-Result -Name $Label -Level 'FAIL' -Detail ("HTTP 404：路由缺失（未注册或被 SPA 兜底），期望 {0}" -f ($PassCodes -join '/'))
        return
    }
    if ($s -eq 0) {
        Add-Result -Name $Label -Level 'FAIL' -Detail ("请求失败（连接层）：{0}" -f $r.Error)
        return
    }
    Add-Result -Name $Label -Level 'FAIL' -Detail ("HTTP {0}：既不是路由缺失(404)也不是可接受的 4xx，期望 {1}" -f $s, ($PassCodes -join '/'))
}

function Build-Payload {
    param([string]$Name, [string]$TargetOS, [string]$Arch, [string]$Profile, [string]$Format)
    $label = ("载荷构建 {0}/{1} {2}" -f $TargetOS, $Arch, $Profile)
    $bodyObj = [ordered]@{
        name              = $Name
        format            = $Format
        language          = 'go'
        os                = $TargetOS
        arch              = $Arch
        profile           = $Profile
        protocol          = 'tcp'
        server_url        = ("127.0.0.1:{0}" -f $script:ListenerPort)
        interval          = 5
        jitter            = 20
        # 说明：服务端 builder 把 0 当作"未指定"并回退到 implant.startup_delay_min/max，
        # 最终仍有 2~10s 内置随机延迟（见 internal/server/builder/builder.go），故这里显式传 0
        # 只是表达"期望无启动延迟"，本机等待窗口按 60s 设计。
        startup_delay_min = 0
        startup_delay_max = 0
    }
    $body = $bodyObj | ConvertTo-Json -Compress

    $r = Invoke-TshApi -Method 'POST' -Url ($script:BaseUrl + '/api/v1/builders') -ApiKey $script:ApiKey -Body $body -TimeoutSec 1800
    if ($r.StatusCode -ne 200) {
        Add-Result -Name $label -Level 'FAIL' -Detail ("POST /api/v1/builders → {0}：{1}" -f $r.StatusCode, (Get-FirstLine $r.Content))
        return $null
    }

    $obj = $null
    try { $obj = $r.Content | ConvertFrom-Json } catch { $obj = $null }
    if ($null -eq $obj) {
        Add-Result -Name $label -Level 'FAIL' -Detail '构建响应不是合法 JSON'
        return $null
    }

    $size = [int]$obj.size
    $sha = [string]$obj.sha256
    $dlUrl = [string]$obj.download_url
    $problems = @()
    if ($size -le 100000) { $problems += ("size={0} 不大于 100000" -f $size) }
    if ($sha -notmatch '^[0-9a-fA-F]{64}$') { $problems += ("sha256 不是 64 位 hex：'{0}'" -f $sha) }
    if ([string]::IsNullOrWhiteSpace($dlUrl)) { $problems += 'download_url 为空' }
    if ($problems.Count -gt 0) {
        Add-Result -Name $label -Level 'FAIL' -Detail ($problems -join '；')
        return $null
    }

    # 下载（download_url 在受保护路由下，必须带 API Key）
    $ext = '.bin'
    if ($TargetOS -eq 'windows') { $ext = '.exe' }
    $localPath = Join-Path $script:PayloadDir ($Name + $ext)
    $dl = Invoke-TshApi -Method 'GET' -Url ($script:BaseUrl + $dlUrl) -ApiKey $script:ApiKey -OutFile $localPath -TimeoutSec 300
    if ($dl.StatusCode -ne 200) {
        Add-Result -Name $label -Level 'FAIL' -Detail ("下载 {0} → HTTP {1}：{2}" -f $dlUrl, $dl.StatusCode, $dl.Error)
        return $null
    }
    if (-not (Test-Path -LiteralPath $localPath)) {
        Add-Result -Name $label -Level 'FAIL' -Detail ("下载未落地文件：{0}" -f $localPath)
        return $null
    }
    $fileLen = (Get-Item -LiteralPath $localPath).Length
    if ($fileLen -ne $size) {
        Add-Result -Name $label -Level 'FAIL' -Detail ("下载文件大小 {0} 与响应 size {1} 不一致" -f $fileLen, $size)
        return $null
    }
    $fileSha = (Get-FileHash -LiteralPath $localPath -Algorithm SHA256).Hash.ToLower()
    if ($fileSha -ne $sha.ToLower()) {
        Add-Result -Name $label -Level 'FAIL' -Detail ("下载文件 sha256 {0} 与响应 {1} 不一致" -f $fileSha, $sha)
        return $null
    }

    Add-Result -Name $label -Level 'OK' -Detail ("size={0} sha256={1}... 已下载 {2}" -f $size, $sha.Substring(0, 12), $localPath)
    return [pscustomobject]@{ Name = $Name; OS = $TargetOS; Path = $localPath; Size = $size; Sha256 = $sha }
}

function Invoke-SmokeTask {
    param([string]$SessionId)
    $label = '植入端任务下发'
    $escaped = [uri]::EscapeDataString($SessionId)
    $body = '{"command":"whoami","timeout":15,"task_type":"command"}'
    $r = Invoke-TshApi -Method 'POST' -Url ("{0}/api/v1/sessions/{1}/interact" -f $script:BaseUrl, $escaped) -ApiKey $script:ApiKey -Body $body -TimeoutSec 30
    if ($r.StatusCode -ne 200) {
        Add-Result -Name $label -Level 'FAIL' -Detail ("POST /api/v1/sessions/{0}/interact → {1}：{2}" -f $SessionId, $r.StatusCode, (Get-FirstLine $r.Content))
        return
    }
    $taskId = ''
    try { $taskId = [string](($r.Content | ConvertFrom-Json).task_id) } catch { $taskId = '' }
    if ($taskId -eq '' -or $taskId -eq '0') {
        Add-Result -Name $label -Level 'FAIL' -Detail '任务下发响应中没有 task_id'
        return
    }

    $output = ''
    $status = ''
    $deadline = (Get-Date).AddSeconds(60)
    while ((Get-Date) -lt $deadline) {
        $t = Invoke-TshApi -Method 'GET' -Url ("{0}/api/v1/tasks/{1}" -f $script:BaseUrl, $taskId) -ApiKey $script:ApiKey -TimeoutSec 20
        if ($t.StatusCode -eq 200) {
            $obj = $null
            try { $obj = $t.Content | ConvertFrom-Json } catch { $obj = $null }
            if ($null -ne $obj) {
                $status = [string]$obj.status
                $output = [string]$obj.output
                if ($output -ne '') { break }
                if ($status -eq 'failed' -or $status -eq 'timeout') { break }
            }
        }
        Start-Sleep -Seconds 2
    }

    if ($output -ne '') {
        $preview = ($output.Trim() -replace "`r?`n", ' | ')
        if ($preview.Length -gt 80) { $preview = $preview.Substring(0, 80) + '...' }
        Add-Result -Name $label -Level 'OK' -Detail ("任务 {0}（whoami）status={1}，输出：{2}" -f $taskId, $status, $preview)
    } else {
        Add-Result -Name $label -Level 'FAIL' -Detail ("任务 {0} 未在 60s 内返回非空输出（status='{1}'）" -f $taskId, $status)
    }
}

# ─── 清理 ────────────────────────────────────────────────────────────────────
function Invoke-Cleanup {
    $problems = @()

    # 1) 载荷进程
    Stop-ProcSafe $script:ImplantProc '载荷进程'

    # 2) 删除会话（服务端还活着时才能删）
    if ($script:OnlineSessionId -ne '' -and (Test-ProcAlive $script:ServerProc)) {
        $url = ("{0}/api/v1/sessions/{1}" -f $script:BaseUrl, [uri]::EscapeDataString($script:OnlineSessionId))
        $r = Invoke-TshApi -Method 'DELETE' -Url $url -ApiKey $script:ApiKey -TimeoutSec 20
        if ($r.StatusCode -eq 200 -or $r.StatusCode -eq 404) {
            Write-Host ("  [清理] 已删除会话 {0}（HTTP {1}）" -f $script:OnlineSessionId, $r.StatusCode) -ForegroundColor DarkGray
        } else {
            $problems += ("删除会话 {0} 失败（HTTP {1}）" -f $script:OnlineSessionId, $r.StatusCode)
        }
    }

    # 3) 服务端进程
    Stop-ProcSafe $script:ServerProc '服务端进程'

    # 4) 确认端口已释放（避免残留进程占端口）
    if ($script:ApiPort -gt 0) {
        $released = $false
        for ($i = 0; $i -lt 10; $i++) {
            if (Test-PortFree $script:ApiPort) { $released = $true; break }
            Start-Sleep -Milliseconds 500
        }
        if (-not $released) { $problems += ("端口 {0} 仍被占用（可能有残留 toserver.exe 进程）" -f $script:ApiPort) }
    }

    # 5) 临时目录
    if ($script:WorkDir -eq '') {
        Add-Result -Name '清理临时环境' -Level 'OK' -Detail '无需清理（脚本未创建临时环境）'
    } elseif ($script:KeepArtifacts) {
        Add-Result -Name '清理临时环境' -Level 'OK' -Detail ("-KeepArtifacts：保留临时目录 {0}" -f $script:WorkDir)
    } else {
        $removed = $false
        for ($i = 0; $i -lt 3; $i++) {
            try {
                Remove-Item -LiteralPath $script:WorkDir -Recurse -Force -ErrorAction Stop
                $removed = $true
                break
            } catch {
                Start-Sleep -Milliseconds 700
            }
        }
        if ($removed) {
            Add-Result -Name '清理临时环境' -Level 'OK' -Detail '已删除临时目录与后台进程'
        } else {
            Add-Result -Name '清理临时环境' -Level 'WARN' -Detail ("临时目录删除失败（可能被杀软或残留进程锁定），请手动删除：{0}" -f $script:WorkDir)
        }
    }

    foreach ($p in $problems) { Add-Result -Name '清理' -Level 'WARN' -Detail $p }
}

# ─── 摘要 ────────────────────────────────────────────────────────────────────
function Show-Summary {
    Write-Host ''
    Write-Host '================ ToShell 端到端冒烟结果 ================' -ForegroundColor Cyan
    $fails = 0
    $warns = 0
    foreach ($res in $script:Results) {
        $sym = '✅'
        if ($res.Level -eq 'WARN') { $sym = '⚠️'; $warns++ }
        if ($res.Level -eq 'FAIL') { $sym = '❌'; $fails++ }
        Write-Host ("{0} {1}：{2}" -f $sym, $res.Name, $res.Detail)
    }
    Write-Host ('-' * 56)
    Write-Host ("合计 {0} 项：❌ {1} 项，⚠️ {2} 项" -f $script:Results.Count, $fails, $warns)
    if ($fails -gt 0) {
        Write-Host '结论：发版门禁未通过（exit 1）' -ForegroundColor Red
        return 1
    }
    Write-Host '结论：发版门禁通过（exit 0）' -ForegroundColor Green
    return 0
}

# ─── 主流程 ──────────────────────────────────────────────────────────────────
Write-Host '================ ToShell 端到端冒烟（发版门禁 P0-4）================' -ForegroundColor Cyan

try {
    # 0) 运行环境
    if (-not (Test-IsWindows)) {
        Fail-Fatal -Name '运行环境' -Detail '本脚本只能在 Windows 上运行（需要启动 toserver.exe 与 Windows 载荷）'
    }
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Fail-Fatal -Name '运行环境' -Detail '找不到 go 命令（构建服务端与载荷都需要 go）'
    }
    Add-Result -Name '运行环境' -Level 'OK' -Detail ("Windows + go 可用；仓库根目录 {0}" -f $script:RepoRoot)

    # 1) 临时工作目录
    if ($WorkDir -eq '') {
        $script:WorkDir = Join-Path $env:TEMP ("toshell-e2e-" + (Get-Random -Minimum 100000 -Maximum 999999))
    } else {
        $script:WorkDir = $WorkDir
    }
    $script:WorkDir = (New-Item -ItemType Directory -Force -Path $script:WorkDir).FullName
    $script:PayloadDir = Join-Path $script:WorkDir 'payloads'
    New-Item -ItemType Directory -Force -Path $script:PayloadDir | Out-Null
    Add-Result -Name '临时工作目录' -Level 'OK' -Detail $script:WorkDir

    # 2) 端口
    if ($Port -ne 0) {
        if (-not (Test-PortFree $Port)) { Fail-Fatal -Name '端口选择' -Detail ("-Port {0} 已被占用，请换端口或省略 -Port 由脚本随机挑选" -f $Port) }
        if (-not (Test-PortFree ($Port + 1))) { Fail-Fatal -Name '端口选择' -Detail ("-Port {0} 需要的 C2 监听端口 {1} 已被占用" -f $Port, ($Port + 1)) }
    } else {
        $picked = Get-FreePortPair
        if ($picked -eq 0) { Fail-Fatal -Name '端口选择' -Detail '在 18000-19000 区间找不到两个连续空闲端口' }
        $Port = $picked
    }
    $script:ApiPort = $Port
    $script:ListenerPort = $Port + 1
    $script:BaseUrl = ("http://127.0.0.1:{0}" -f $Port)
    Add-Result -Name '端口选择' -Level 'OK' -Detail ("控制台/API 端口 {0}，C2 TCP 监听端口 {1}" -f $Port, $script:ListenerPort)

    # 3) 构建服务端（-tags webui）
    $serverPath = Join-Path $script:WorkDir 'toserver.exe'
    if ($ServerExe -ne '') {
        if (-not (Test-Path -LiteralPath $ServerExe)) { Fail-Fatal -Name '服务端构建' -Detail ("-ServerExe 指定的文件不存在：{0}" -f $ServerExe) }
        $serverPath = (Resolve-Path -LiteralPath $ServerExe).Path
        Add-Result -Name '服务端构建' -Level 'WARN' -Detail ("使用外部服务端 {0}，未验证该二进制与当前源码一致" -f $serverPath)
    } else {
        $savedGoOs = $env:GOOS
        $savedGoArch = $env:GOARCH
        $savedCgo = $env:CGO_ENABLED
        # 显式固定目标平台：本机 `go env GOARCH` 可能是 386，避免产出非预期的 32 位服务端
        $env:GOOS = 'windows'
        $env:GOARCH = 'amd64'
        $env:CGO_ENABLED = '0'
        # PS 5.1 在 $ErrorActionPreference='Stop' 下会把原生命令的 stderr 变成终止性错误
        # （go build 的报错与 "go: downloading ..." 都走 stderr），故构建期间临时放开。
        $ErrorActionPreference = 'Continue'
        $buildLog = ''
        $buildCode = -1
        try {
            Push-Location $script:RepoRoot
            try {
                $buildLog = (& go build -tags webui -ldflags '-s -w -X main.version=smoke' -o $serverPath ./cmd/server 2>&1 | Out-String)
                $buildCode = $LASTEXITCODE
            } finally {
                Pop-Location
            }
        } finally {
            $env:GOOS = $savedGoOs
            $env:GOARCH = $savedGoArch
            $env:CGO_ENABLED = $savedCgo
            $ErrorActionPreference = 'Stop'
        }
        if ($buildCode -ne 0 -or -not (Test-Path -LiteralPath $serverPath)) {
            if ($buildLog -ne '') { ($buildLog -split "`r?`n" | Select-Object -Last 20) | ForEach-Object { Write-Host ("    " + $_) -ForegroundColor DarkGray } }
            Fail-Fatal -Name '服务端构建' -Detail ("go build 失败（退出码 {0}）：{1}" -f $buildCode, (Get-FirstLine $buildLog 300))
        }
        Add-Result -Name '服务端构建' -Level 'OK' -Detail ("{0}（{1:N0} 字节，-tags webui）" -f $serverPath, (Get-Item -LiteralPath $serverPath).Length)
    }

    # 4) 最小配置（字段名照抄 internal/server/config/config.go 与 release/configs/server.yaml.example）
    $cfgDir = Join-Path $script:WorkDir 'configs'
    New-Item -ItemType Directory -Force -Path $cfgDir | Out-Null
    $cfgPath = Join-Path $cfgDir 'server.yaml'
    $implantsDir = Join-Path $script:WorkDir 'implants'
    $dataDir = Join-Path $script:WorkDir 'data'
    New-Item -ItemType Directory -Force -Path $implantsDir | Out-Null
    New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
    $dbPath = Join-Path $dataDir 'toshell.db'
    $implantTemplateDir = Join-Path $script:RepoRoot 'internal\server\builder\implant'

    $configText = @"
# ToShell E2E smoke 最小配置（由 scripts/e2e_smoke.ps1 生成，随临时目录一起删除）
# 注意：服务端管理 API 实际监听 server.api_port（见 cmd/server/main.go），
# server.port 不被绑定；两者都写成同一个端口，避免"健康检查打在 8081 上"。
server:
    host: 127.0.0.1
    port: $Port
    api_host: 127.0.0.1
    api_port: $Port
    read_timeout: 30s
    write_timeout: 30s
    idle_timeout: 120s
    trust_proxy_headers: false
auth:
    enabled: true
    jwt_enabled: true
    jwt_key: smoke-jwt-key-not-for-production
    jwt_expire: 24
    api_key_enabled: true
    api_keys:
        - $script:ApiKey
    admin_username: admin
    admin_password: smoke-admin-password
listener:
    enabled: true
    host: 127.0.0.1
    port: $script:ListenerPort
    public_host: 127.0.0.1
    protocol: tcp
    tls_enabled: false
    encryption_key: smoke-key-0123456789abcdefghijkl
    heartbeat_timeout: 60s
implant:
    interval: 5
    jitter: 20
    retry_count: 3
    retry_wait: 5
    startup_delay_min: 0
    startup_delay_max: 0
    output_dir: '$implantsDir'
    template_dir: '$implantTemplateDir'
database:
    type: sqlite
    path: '$dbPath'
logging:
    level: info
    format: json
    output: stdout
web:
    basic_auth_enabled: false
    unauth_mode: basic
"@
    [System.IO.File]::WriteAllText($cfgPath, $configText, (New-Object System.Text.UTF8Encoding($false)))
    Add-Result -Name '最小配置' -Level 'OK' -Detail $cfgPath

    # 5) 起临时服务端（cwd = 工作目录，配置/数据/载荷都落在工作目录内）
    $script:SrvOutLog = Join-Path $script:WorkDir 'server.stdout.log'
    $script:SrvErrLog = Join-Path $script:WorkDir 'server.stderr.log'
    $savedTemplateDir = $env:TOSHELL_IMPLANT_TEMPLATE_DIR
    $env:TOSHELL_IMPLANT_TEMPLATE_DIR = $implantTemplateDir
    try {
        $script:ServerProc = Start-Process -FilePath $serverPath -ArgumentList @('-config', ('"' + $cfgPath + '"')) -WorkingDirectory $script:WorkDir -RedirectStandardOutput $script:SrvOutLog -RedirectStandardError $script:SrvErrLog -PassThru -WindowStyle Hidden
    } catch {
        Fail-Fatal -Name '服务端启动' -Detail ("Start-Process 启动 toserver.exe 失败：{0}" -f $_.Exception.Message)
    } finally {
        $env:TOSHELL_IMPLANT_TEMPLATE_DIR = $savedTemplateDir
    }

    # 轮询 /api/v1/health（带 X-API-Key）：区分"没起来"与"起来了但 401/404/5xx"
    $healthOk = $false
    $lastStatus = 0
    $lastErr = ''
    $deadline = (Get-Date).AddSeconds(60)
    while ((Get-Date) -lt $deadline) {
        if (-not (Test-ProcAlive $script:ServerProc)) {
            Show-LogTail -Path $script:SrvOutLog -Lines 30 -Label '服务端 stdout'
            Show-LogTail -Path $script:SrvErrLog -Lines 30 -Label '服务端 stderr'
            Fail-Fatal -Name '服务端启动' -Detail ("toserver.exe 进程提前退出（PID {0}），未通过健康检查" -f $script:ServerProc.Id)
        }
        $r = Invoke-TshApi -Method 'GET' -Url ($script:BaseUrl + '/api/v1/health') -ApiKey $script:ApiKey -TimeoutSec 5
        $lastStatus = $r.StatusCode
        $lastErr = $r.Error
        if ($lastStatus -eq 200) { $healthOk = $true; break }
        Start-Sleep -Milliseconds 800
    }
    if (-not $healthOk) {
        Show-LogTail -Path $script:SrvOutLog -Lines 30 -Label '服务端 stdout'
        Show-LogTail -Path $script:SrvErrLog -Lines 30 -Label '服务端 stderr'
        $reason = ''
        if ($lastStatus -eq 0) {
            $reason = ("无法连接 {0}（{1}）：服务端未就绪或端口不对" -f $script:BaseUrl, $lastErr)
        } elseif ($lastStatus -eq 401) {
            $reason = '服务端已启动，但 /api/v1/health 返回 401：API Key 未被识别（检查 auth.api_key_enabled / auth.api_keys 与 X-API-Key 头）'
        } elseif ($lastStatus -eq 403) {
            $reason = '服务端已启动，但 /api/v1/health 返回 403：请求被拒（检查 web.allow_cidrs 等防护配置）'
        } elseif ($lastStatus -eq 404) {
            $reason = '服务端已启动，但 /api/v1/health 返回 404：路由缺失或被 Web 防护伪装（检查 web.basic_auth_enabled / web.unauth_mode）'
        } elseif ($lastStatus -ge 500) {
            $reason = ("服务端已启动，但 /api/v1/health 返回 {0}：服务端内部错误" -f $lastStatus)
        } else {
            $reason = ("等待 60s 仍未通过健康检查，最后状态 {0}" -f $lastStatus)
        }
        Fail-Fatal -Name '服务端健康检查' -Detail $reason
    }
    Add-Result -Name '服务端健康检查' -Level 'OK' -Detail ("GET /api/v1/health（带 X-API-Key）→ 200（{0}）" -f $script:BaseUrl)

    # 6) 鉴权真的生效：不带 Key 访问受保护接口必须 401
    $noKey = Invoke-TshApi -Method 'GET' -Url ($script:BaseUrl + '/api/v1/sessions') -TimeoutSec 15
    if ($noKey.StatusCode -eq 401) {
        Add-Result -Name '鉴权生效' -Level 'OK' -Detail '不带 X-API-Key 访问 GET /api/v1/sessions → 401'
    } elseif ($noKey.StatusCode -eq 200) {
        Add-Result -Name '鉴权生效' -Level 'FAIL' -Detail '不带 API Key 也能拿到 /api/v1/sessions（200）—— 鉴权失效！检查 auth.enabled / auth.api_key_enabled'
    } else {
        Add-Result -Name '鉴权生效' -Level 'FAIL' -Detail ("不带 API Key 访问 /api/v1/sessions 返回 {0}，期望 401（0 表示连接失败：{1}）" -f $noKey.StatusCode, $noKey.Error)
    }

    # 7) C2 TCP 监听端口（植入端要连它）
    $listening = $false
    $listenDeadline = (Get-Date).AddSeconds(20)
    while ((Get-Date) -lt $listenDeadline) {
        if (Test-PortListening -Port $script:ListenerPort) { $listening = $true; break }
        Start-Sleep -Milliseconds 500
    }
    if ($listening) {
        Add-Result -Name 'C2 TCP 监听端口' -Level 'OK' -Detail ("127.0.0.1:{0} 可连接" -f $script:ListenerPort)
    } else {
        Add-Result -Name 'C2 TCP 监听端口' -Level 'FAIL' -Detail ("127.0.0.1:{0} 未监听（listener.enabled / protocol=tcp 配置错误或端口冲突）" -f $script:ListenerPort)
    }

    # 8) 构建器能力接口
    $meta = Invoke-TshApi -Method 'GET' -Url ($script:BaseUrl + '/api/v1/builders') -ApiKey $script:ApiKey -TimeoutSec 20
    if ($meta.StatusCode -ne 200) {
        Add-Result -Name '构建器能力接口' -Level 'FAIL' -Detail ("GET /api/v1/builders → {0}，期望 200" -f $meta.StatusCode)
    } else {
        $m = $null
        try { $m = $meta.Content | ConvertFrom-Json } catch { $m = $null }
        if ($null -eq $m) {
            Add-Result -Name '构建器能力接口' -Level 'FAIL' -Detail '响应不是合法 JSON'
        } else {
            $problems = @()
            $names = $m.PSObject.Properties.Name
            if (-not ($names -contains 'formats')) { $problems += '缺少 formats 字段' }
            $evasion = $null
            if ($names -contains 'evasion') { $evasion = $m.evasion }
            if ($null -eq $evasion) {
                $problems += '缺少 evasion 字段'
            } elseif (-not ($evasion.PSObject.Properties.Name -contains 'garble_available')) {
                $problems += '缺少 evasion.garble_available 字段'
            }
            if ($problems.Count -gt 0) {
                Add-Result -Name '构建器能力接口' -Level 'FAIL' -Detail ($problems -join '；')
            } else {
                Add-Result -Name '构建器能力接口' -Level 'OK' -Detail ("formats={0}；evasion.garble_available={1}" -f ($m.formats -join '/'), $evasion.garble_available)
            }
        }
    }

    # 9) 三档载荷构建 + 下载校验
    $targets = @(
        [pscustomobject]@{ Name = 'smoke-win-amd64-full';  OS = 'windows'; Arch = 'amd64'; Profile = 'full';  Format = 'exe' },
        [pscustomobject]@{ Name = 'smoke-win-amd64-light'; OS = 'windows'; Arch = 'amd64'; Profile = 'light'; Format = 'exe' },
        [pscustomobject]@{ Name = 'smoke-linux-amd64-full'; OS = 'linux';  Arch = 'amd64'; Profile = 'full';  Format = 'bin' }
    )
    foreach ($t in $targets) {
        $built = Build-Payload -Name $t.Name -TargetOS $t.OS -Arch $t.Arch -Profile $t.Profile -Format $t.Format
        if ($null -ne $built) { [void]$script:Payloads.Add($built) }
    }

    $winPayload = $null
    foreach ($p in $script:Payloads) {
        if ($p.Name -eq 'smoke-win-amd64-full') { $winPayload = $p }
    }

    # 10) 关键路由存在性（不要求业务成功，只要求路由在且鉴权正常）
    Test-Route -Label '路由存在 GET /api/v1/drivers' -Method 'GET' -Path '/api/v1/drivers' -PassCodes @(200)
    Test-Route -Label '路由存在 POST /sessions/{id}/fileless-exec' -Method 'POST' -Path '/api/v1/sessions/nonexistent/fileless-exec' -Body '{' -PassCodes @(400, 401, 403, 409)
    Test-Route -Label '路由存在 POST /sessions/{id}/screen-stream' -Method 'POST' -Path '/api/v1/sessions/nonexistent/screen-stream' -Body '{' -PassCodes @(400, 401, 403, 409)

    # 观察项（不阻断）：会话不存在且请求体合法时，screen-stream 的错误码应当是 4xx
    $probe = Invoke-TshApi -Method 'POST' -Url ($script:BaseUrl + '/api/v1/sessions/nonexistent/screen-stream') -ApiKey $script:ApiKey -Body '{"action":"start"}' -TimeoutSec 20
    if ($probe.StatusCode -ge 400 -and $probe.StatusCode -lt 500 -and $probe.StatusCode -ne 404) {
        Add-Result -Name '会话不存在的错误码（观察项）' -Level 'OK' -Detail ("返回 {0}（4xx，符合预期）" -f $probe.StatusCode)
    } elseif ($probe.StatusCode -eq 404) {
        Add-Result -Name '会话不存在的错误码（观察项）' -Level 'WARN' -Detail '返回 404：与"路由缺失"无法区分（路由未注册时也是 404）'
    } elseif ($probe.StatusCode -ge 500) {
        Add-Result -Name '会话不存在的错误码（观察项）' -Level 'WARN' -Detail ("返回 {0}：会话不存在时服务端返回 5xx（taskMgr.Create 校验会话失败 → handler 500），语义上应为 404；属已知实现问题，不阻断发版" -f $probe.StatusCode)
    } else {
        Add-Result -Name '会话不存在的错误码（观察项）' -Level 'WARN' -Detail ("返回 {0}，与预期 （4xx） 不符" -f $probe.StatusCode)
    }

    # 11) 真实植入端上线 + 任务下发
    if ($script:SkipImpl) {
        Add-Result -Name '真实植入端上线' -Level 'WARN' -Detail '已按 -SkipImplant / TOSHELL_E2E_SKIP_IMPLANT=1 跳过（本环节未验证）'
        Add-Result -Name '植入端任务下发' -Level 'WARN' -Detail '随植入端环节一并跳过'
    } elseif ($null -eq $winPayload) {
        Add-Result -Name '真实植入端上线' -Level 'FAIL' -Detail '缺少 windows/amd64 full 载荷（构建或下载失败），无法验证植入端上线'
        Add-Result -Name '植入端任务下发' -Level 'WARN' -Detail '随植入端环节一并跳过'
    } else {
        $baseSessions = Get-SessionIds
        if ($null -eq $baseSessions) { $baseSessions = @() }
        $script:ImpOutLog = Join-Path $script:WorkDir 'implant.stdout.log'
        $script:ImpErrLog = Join-Path $script:WorkDir 'implant.stderr.log'

        $launchErr = ''
        $started = Get-Date
        try {
            $script:ImplantProc = Start-Process -FilePath $winPayload.Path -WorkingDirectory $script:WorkDir -RedirectStandardOutput $script:ImpOutLog -RedirectStandardError $script:ImpErrLog -PassThru -WindowStyle Hidden
        } catch {
            $launchErr = $_.Exception.Message
        }

        if ($launchErr -ne '') {
            $detail = ("载荷无法在本机执行（安全软件拦截），已跳过植入端环节：{0}" -f $launchErr)
            if (-not (Test-Path -LiteralPath $winPayload.Path)) { $detail += '（载荷文件已被删除）' }
            if ((Test-AvBlockMessage $launchErr) -or (-not (Test-Path -LiteralPath $winPayload.Path))) {
                if ($script:RequireImplant) {
                    Add-Result -Name '真实植入端上线' -Level 'FAIL' -Detail ($detail + ' [因 -RequireImplant，本项计为失败]')
                } else {
                    Add-Result -Name '真实植入端上线' -Level 'WARN' -Detail $detail
                }
            } else {
                Add-Result -Name '真实植入端上线' -Level 'FAIL' -Detail ("启动载荷失败：{0}" -f $launchErr)
            }
            Add-Result -Name '植入端任务下发' -Level 'WARN' -Detail '随植入端环节一并跳过'
        } else {
            $state = 'timeout'
            $newSessions = @()
            $deadline = (Get-Date).AddSeconds(60)
            while ((Get-Date) -lt $deadline) {
                $cur = Get-SessionIds
                if ($null -ne $cur) {
                    foreach ($sid in $cur) {
                        if ($baseSessions -notcontains $sid) { $newSessions += $sid }
                    }
                    if ($newSessions.Count -gt 0) { $state = 'online'; break }
                }
                if (-not (Test-ProcAlive $script:ImplantProc)) {
                    if (-not (Test-Path -LiteralPath $winPayload.Path)) { $state = 'blocked' } else { $state = 'exited' }
                    break
                }
                Start-Sleep -Seconds 2
            }
            $waited = [int]((Get-Date) - $started).TotalSeconds

            if ($state -eq 'online') {
                $script:OnlineSessionId = $newSessions[0]
                Add-Result -Name '真实植入端上线' -Level 'OK' -Detail ("新会话已上线：{0}（等待 {1}s）" -f $script:OnlineSessionId, $waited)
                Invoke-SmokeTask -SessionId $script:OnlineSessionId
            } elseif ($state -eq 'blocked') {
                $detail = ("载荷无法在本机执行（安全软件拦截：进程 {0}s 内退出且文件已被删除），已跳过植入端环节" -f $waited)
                Show-LogTail -Path $script:ImpErrLog -Lines 15 -Label '载荷 stderr'
                if ($script:RequireImplant) {
                    Add-Result -Name '真实植入端上线' -Level 'FAIL' -Detail ($detail + ' [因 -RequireImplant，本项计为失败]')
                } else {
                    Add-Result -Name '真实植入端上线' -Level 'WARN' -Detail $detail
                }
                Add-Result -Name '植入端任务下发' -Level 'WARN' -Detail '随植入端环节一并跳过'
            } elseif ($state -eq 'exited') {
                $code = '?'
                try { $code = [string]$script:ImplantProc.ExitCode } catch { $code = '?' }
                Show-LogTail -Path $script:ImpOutLog -Lines 20 -Label '载荷 stdout'
                Show-LogTail -Path $script:ImpErrLog -Lines 20 -Label '载荷 stderr'
                Add-Result -Name '真实植入端上线' -Level 'FAIL' -Detail ("载荷进程在 {0}s 内退出（退出码 {1}）且文件仍在，未产生新会话；若本机装有 360/电脑管家等安全软件，也可能是「拦截执行但未删文件」，请结合下方载荷日志区分（-RequireImplant 不影响本项判定）" -f $waited, $code)
                Add-Result -Name '植入端任务下发' -Level 'WARN' -Detail '随植入端环节一并跳过'
            } else {
                Show-LogTail -Path $script:ImpOutLog -Lines 20 -Label '载荷 stdout'
                Show-LogTail -Path $script:ImpErrLog -Lines 20 -Label '载荷 stderr'
                $sessProbe = Invoke-TshApi -Method 'GET' -Url ($script:BaseUrl + '/api/v1/sessions') -ApiKey $script:ApiKey -TimeoutSec 20
                Add-Result -Name '真实植入端上线' -Level 'FAIL' -Detail ("等待 60s 未出现新会话（GET /api/v1/sessions → HTTP {0}；服务端 C2 端口 {1}，协议 tcp）" -f $sessProbe.StatusCode, $script:ListenerPort)
                Add-Result -Name '植入端任务下发' -Level 'WARN' -Detail '随植入端环节一并跳过'
            }
        }
    }
} catch {
    if ($_.Exception.Message -notlike 'E2E_FATAL*') {
        Add-Result -Name '脚本异常' -Level 'FAIL' -Detail $_.Exception.Message
        Show-LogTail -Path $script:SrvOutLog -Lines 40 -Label '服务端 stdout'
        Show-LogTail -Path $script:SrvErrLog -Lines 40 -Label '服务端 stderr'
    }
} finally {
    try { Invoke-Cleanup } catch { Write-Host ("  [清理] 清理阶段异常：{0}" -f $_.Exception.Message) -ForegroundColor DarkYellow }
}

$exitCode = Show-Summary
exit $exitCode
