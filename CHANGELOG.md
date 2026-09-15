# 更新日志 / Changelog

本项目采用 [语义化版本](https://semver.org/lang/zh-CN/)。所有值得注意的改动都会记录在本文件。
后续优化方向（含驱动能力分档、内存执行加固、屏幕流跨平台、平台工具库与远程加载型红队能力等）见 [ROADMAP.md](ROADMAP.md)。

## [v1.3.3] - 2026-09-15

重点：ROADMAP P0 两项（会话掉线抖动、屏幕流/截图可控）、内存模块 EXE 带参执行、平台工具库、BYOVD 驱动可插拔、issue #6 / #7。

### 🧠 内存模块支持 EXE 带参数执行
- 新增 `fileless_exec` 的 **`exe_mem`**：植入端反射式映射 EXE（映射/重定位/导入表）后 `CreateThread` 到入口点，并把 `args` **注入 PEB 命令行**（`RTL_USER_PROCESS_PARAMETERS.CommandLine`），使被执行的 EXE 的 `argv` 真正拿到参数。实测：反射执行测试 PE 后其 `argv` 为 `["tool.exe","mem-hello","42"]`（`argc=3`）。
- 新增 `wait_ms`：等待执行线程结束并回传退出码；0 = 立即返回（后台线程继续运行）。
- 退出保护：对映射镜像 **IAT 里的 `ExitProcess`/`TerminateProcess`/`RtlExitUserProcess` 重定向到 `ExitThread`**，避免载荷退出时带走植入体（局限见下）。
- 架构校验：32 位 PE 无法进 64 位进程时**明确报错**，不再静默失败。
- 前端「内存执行」面板新增 `exe_mem` 选项、`args`（EXE 命令行）、`entry`（argv[0]）与 `wait_ms` 输入，并按 kind 给出提示。
- 修正既有 `exe`（donut）路径：原先 `Thread=0 + ExitOpt=2`，内存执行的程序结束时调用 `RtlExitUserProcess` 会**把植入体一起杀掉**；改为 `Thread=1 + ExitOpt=1`（独立线程 + 只退线程），并支持把 `args` 作为 donut `Parameters`（上限 255 字节，超长直接报错）。
- **已知边界**（写在面板提示与任务输出里）：`exe_mem` 要求与植入体同架构；**不要用它跑 Go 编译的 EXE**（宿主也是 Go，两个 runtime 冲突）；载荷 CRT 内部直接调用的 `ExitProcess` 拦不住（彻底解决需 hook 系统 API），「跑完即退」的工具请走落地执行；内存执行不重定向 stdout，故无控制台输出。

### 🔩 BYOVD 内置驱动换成 kgameprotect（删除 RTCore64.sys）
- **内置驱动替换**：删除 `RTCore64.sys`（MSI Afterburner / CVE-2019-16098，任意内核读写，被微软易受攻击驱动黑名单与几乎所有杀软重点标记），改为内置 **`kgameprotect.sys`**（WHQL 签名 / Microsoft Windows Hardware Compatibility Publisher，AMD64，59,592 字节，SHA-256 `6c1d596d…126ee`，与 [LOLDrivers PR #428](https://github.com/magicsword-io/LOLDrivers/pull/428) 记录一致）。
- **能力定位改为"击杀"**：该驱动设备 `\\.\kgameprotect`，暴露**无鉴权进程终止 IOCTL `0x222048`**（METHOD_BUFFERED + FILE_ANY_ACCESS，入参首个 DWORD 为 PID），驱动内部 `PsLookupProcessByProcessId → ObOpenObjectByPointer(PROCESS_TERMINATE) → ZwTerminateProcess`，因此**不需要调用方持有目标进程权限**即可终止普通杀软/EDR 进程。
- **新增 `byovd_kill` 任务与接口**：`POST /api/v1/sessions/{id}/edr/byovd-kill`（`pid` 或 `process_name`，`driver` 可选），前端杀软对抗页新增「驱动击杀」入口与目标输入。
- **能力变化（如实说明）**：kgameprotect 只提供进程终止、**没有任意内核读写**，因此"用驱动改 EPROCESS.Protection"的 PPL 清除路线**已移除**；`ppl_kill` 只保留**句柄窃取**路线，对 PPL 保护进程（如 Defender 的 MsMpEng）无效时会有明确提示。
- 前端文案与排版一并整理：BYOVD 区块改为「驱动说明 + 内置驱动一键加载/卸载 + 驱动击杀 + 自定义 .sys 上传」四段式，去掉原先那段与实现不再匹配的 RTCore64 长文与挤在一行的排版；About/USAGE 同步更新。


### 🚫 不再内置任何 BYOVD 驱动（改为操作员自备）
- **移除 kgameprotect.sys**（连同上一版的 RTCore64.sys）：内置驱动的代价是**载荷与服务端里都带驱动名/IOCTL 明文**，而 AV/EDR 普遍按易受攻击驱动名做规则 —— 实测改动前载荷里可直接搜到 `kgameprotect` ×5 与 `\\.\kgameprotect`。现在发布包与二进制里**不含任何驱动**。
- **驱动目录改为运行期扫描 + manifest**：`drivers/`（发布包同目录）与 `data/drivers/` 下的 `*.sys` 由 `/api/v1/drivers` 实时列出（SHA-256 现算），同目录 `manifest.json` 声明 `device/service/ioctl/purpose`；没有 manifest 也会列出，只是元数据留空（前端提示补全）。
- **植入端不再内置任何驱动默认值**：设备名/服务名/终止 IOCTL 全部由服务端在任务数据里下发；缺参数时明确报错（不再回退到某个内置驱动名）。同时把 `handleBYOVD*` / `byovdKillByPID` 等**高信号函数名改成中性名**（`handleDrvLoad/Unload/Kill` / `drvXferPid`），减少 Go pclntab 里的明文特征。
- **服务端新增会话级驱动档案**：`byovd_load` 可带 `name/device_name/kill_ioctl`，服务端登记后 `byovd_kill` 直接复用（也可在请求里显式带 `device`/`ioctl`）；前端「杀软对抗」页改为"服务端 drivers/ 目录 + 上传 .sys"，并新增设备名/服务名/终止 IOCTL 三个输入框。
- **合规**：`THIRD-PARTY-NOTICES.md` 相应改写为"本项目不再内置任何驱动；使用者自行提供并自负合规责任"。
### 📡 会话稳定性：不再「离线几秒又在线」（ROADMAP P0-1）
- **判活阈值强制留余量**：实际超时 = `max(listener.heartbeat_timeout, 3 × 实测心跳间隔)`，并在启动日志里写明「margin 3x」。此前 `heartbeat_timeout=60s` 与心跳间隔 60s 几乎零余量，任何一次心跳迟到（调度抖动/网络排队/休眠唤醒）都会被判离线，下一个心跳又恢复，前端表现为闪断。
- **按会话自适应**：会话运行中采样实测心跳间隔（只向上立即生效、向下缓慢回收），因此构建时可自定义 `interval` 的载荷也按自己的节奏判活，不再依赖全局配置猜。
- **首周期保护**：会话刚上线、还没采样到节奏时阈值放宽到 2 倍基准，避免第一个心跳刚好踩线被判死。
- **判定统一**：TCP / HTTP 轮询 / MQTT 三个监听器的判活都改为走 `Session.IsAlive()`（此前各自用 10s ticker + 固定阈值，且窗口误差达 10s），扫描周期调整为 5s。
- **广播去抖**：`session_offline` 延迟 15s 观察窗广播，期间会话重连则**不发任何事件**（消除「离线→在线」闪烁），只有持续失联才广播离线；重复注册不再重复广播 `session_online`，上线 webhook 也随之不再重复推送。
- 实测验收：60s 心跳（jitter 5s）的植入端连续运行 220s，**零次状态抖动**；杀掉植入端后按 3 倍间隔（3m1s）判死并广播一次 `session_offline`。

### 🖥 屏幕流 / 截图参数化与限速（ROADMAP P0-2 部分）
- **参数化**：屏幕流与截图支持 `fps`(1-10) / `quality`(20-95) / `max_kbps`(带宽上限) / `monitor`(多显示器单选) / `max_width`(缩放宽度)，服务端校验钳制后透传植入端；`POST /sessions/{id}/screen-stream` 与 `POST /sessions/{id}/screenshot` 均接受这些参数。
- **带宽自适应**：植入端按秒统计实际发送量，超过 `max_kbps` 时先降 JPEG 画质、再降帧率并限宽，带宽有余量时逐级恢复；默认帧率由固定 1.25fps 提升到可选 1-10fps，屏幕流默认 JPEG。
- **服务端限速/帧合并**：新增帧限速器，超过会话期望帧率的帧直接丢弃（前端只需要最新帧），硬上限 10fps，并在丢弃累计时输出诊断日志。
- **明确的失败提示**：捕获失败时立即回传带原因的 error 帧（锁屏/无交互桌面/Headless），前端展示原因而不是一直「等待画面」；收到正常帧后自动清除错误提示。
- **前端**：屏幕流面板新增「画质参数」面板（帧率/画质/带宽/显示器/缩放宽度）与分辨率显示。
- 实测验收：真实植入端截图 `max_width=640` → 640×360 JPEG 约 21KB（原始 2560×1440 约 343KB，体积降到 1/16）；`monitor=1` 单选显示器生效；屏幕流任务回执确认 `fps=5 quality=60 max_kbps=1200` 已下发，WS 侧可见真实 JPEG 帧。

### 🔔 通知与一键上线
- **飞书通知修复（issue #7）**：各平台按自己的消息结构推送——飞书 `msg_type`+`content.text`、企业微信 `msgtype`+`text.content`、Slack `text`、Discord `content`、钉钉 markdown、其它通用 JSON；新增飞书 body 内 `timestamp`+`sign` 加签。判定结果同时解析响应体业务码（飞书 `code≠0`、钉钉/企业微信 `errcode≠0` 均判失败并给出平台原话），不再出现「HTTP 200 显示成功但消息没发出去」。
- **一键上线命令**：下载地址改为服务端按配置解析（`public_host` 优先，其次控制台访问地址 / 载荷 `server_url` 主机 / 本机内网 IP，不可达时显式告警），不再取控制台自身的 `localhost`；新增 Windows 6 种 / Linux 6 种免杀上线命令变体（PowerShell -enc、BITS、HttpClient、certutil、bitsadmin、curl.exe / curl-wget、wget、busybox、python3、规避 noexec、setsid）。

### 🧰 C 植入端工具链探测（issue #6）
- mingw gcc 探测改为「配置 → 环境变量 → 服务端同目录便携工具链 → 常见安装目录 → PATH → **Windows 注册表 PATH**」逐级查找，并用 `gcc -dumpmachine` 校验目标架构；解决「gcc 已加入系统环境变量但服务端仍显示无 gcc」（进程环境是旧快照）与「32 位 gcc 静默编译 amd64」两个问题。
- 新增配置项 `builder.mingw_gcc_path`；garble 可用性改为一次极小真实构建探测（此前只查 PATH，会出现「显示可用但构建必失败」）。

### 🎛 前端可读性
- 屏幕流参数面板在中文名后标出接口字段名（`fps` / `quality` / `max_kbps` / `monitor` / `max_width`），并补范围与悬浮说明，避免"只有一个中文名不知道对应哪个参数"。
- 杀软对抗页 BYOVD 区块重排为「驱动说明 / 内置驱动一键加载与卸载 / 驱动击杀 / 自定义 .sys 上传」四段式，驱动信息（设备、服务名、IOCTL、SHA-256、签名者）来自 `/drivers` 接口，不再把长说明挤在一行。

### 🔢 版本
- 全量版本号更新为 **1.3.3**（服务端 `-version`、Web、About、README、USAGE、部署脚本、打包脚本、CDN 指南），CI 的 `main.version` 改为跟随 tag（`${GITHUB_REF_NAME#v}`），避免每次发版手改 workflow。

### 🚀 一键部署脚本（发布包自带）
- 新增 **`install.ps1`（Windows）/ `install.sh`（Linux、macOS）**；`deploy.bat` / `deploy.sh` 变为调用它们的入口（双击 `deploy.bat` 即可，不用记参数）：
  1. **环境检测**：服务端二进制（并打印 `-version`）、配置文件（缺失自动生成）、`data/` 可写、磁盘剩余空间、**控制台端口与监听端口占用**（正确区分 `listener.port` 与 `database.port`）、Go 工具链版本、`GOPROXY` 可达性、UPX（随包自带）、garble、mingw gcc（含 32/64 位提示）；
  2. **按需在线安装**：Go 缺失或过旧时从 go.dev 官方源下载并**校验官方 SHA-256**，解压到 `./.tools/go`（Windows 为 `%LOCALAPPDATA%\ToShell\tools`）并写入 PATH；`-WithGarble` 装 garble；`-WithMingw` 在 Windows 上尝试用 winget/choco 装 MSYS2/MinGW；`-OpenFirewall` 用 netsh 放行端口（需管理员）；
  3. 末尾给出「通过 / 警告 / 失败」汇总与「必须处理 / 建议处理」清单，随后可直接启动服务端并打印控制台地址。
- 安全设计：默认**不改动系统**（每步询问，`-Check` 只检测）；所有在线下载均做 SHA-256 校验，失败即删除并报错；脚本以 UTF-8 BOM 存盘，避免 PowerShell 5.1 按 ANSI 解析导致乱码/语法错误。

### 📦 发布包修正
- **移除包内残留的 `RTCore64.sys`**：v1.3.3 首次打包时 `release/drivers/RTCore64.sys`（git 跟踪的另一份副本）仍被 CI 打进 zip，与「删除 RTCore64」目标不符；现已删除，改为随包提供内置驱动 `kgameprotect.sys` 副本 + `release/drivers/README.md`（写明 SHA-256、签名者、设备名、IOCTL 与离线复核命令），便于操作员加载前自行核对。
- CI 与本地 `scripts/package_release.ps1` 同步：`install.sh` / `install.ps1` 一并打进包。

### ⚖️ 开源许可补齐
- 补回丢失的 **`LICENSE`（MIT）**：README 一直标注并链接 `LICENSE`，但仓库里没有该文件（链接 404、GitHub 侧识别不到许可）。现补回**纯净的 MIT 全文**（不掺别的文字，保证 GitHub 正确识别为 MIT），README 加 MIT 徽章；授权使用声明单独放在新增的 **`DISCLAIMER.md`**（中英双语：仅限授权测试/红队演练/自建实验环境，禁止未授权用途）。
- 新增 **`THIRD-PARTY-NOTICES.md`**：集中声明发布包内**不受 MIT 覆盖**的第三方组件——**UPX**（GPL-2.0-or-later + 压缩产物例外，`COPYING`/`LICENSE` 已随包放在 `upx/` 目录内）、内置 **`kgameprotect.sys`**（第三方 WHQL 签名驱动，版权归原权利人，附哈希/来源/移除承诺）、Go 模块依赖清单（BSD/MIT/EPL 等）与 `data/` 数据说明；并说明 `RTCore64.sys`/`dbutil_2_3.sys` 自 v1.3.3 起不再捆绑。
- CI 与 `scripts/package_release.ps1` 把 `LICENSE` 与 `THIRD-PARTY-NOTICES.md` 一并打进发布包（此前 zip 内含 UPX 却没有任何许可文本）。
### 📮 联系方式
- README 新增「联系方式 / Contact」：作者、GitHub、邮箱 **qingshan@88.com**、Issue 入口与安全/滥用报告渠道（并说明"使用问题先提 Issue、商务合作与漏洞披露走邮件"）；顶部信息行补邮箱。
- `SECURITY.md` 的报告渠道具体化：邮箱 + GitHub Security Advisories 私密报告入口，并说明建议附上的材料（影响版本/复现要点/影响面/披露时间线）。
- `USAGE.md` 新增「七、联系方式与支持」（原免责声明顺延为八），发布包内的 `release/README.md` 也补了联系表（用户实际先看到的是包内 README）。
### ✅ 实测验收（本轮已跑过的真实验证）
- **会话抖动**：60s 心跳（jitter 5s）的植入端连续运行 220s → 状态**零抖动**；杀掉植入端后按 3m1s（3 倍间隔）判死并只广播一次 `session_offline`；启动日志可见 `Session heartbeat timeout: 3m0s (implant interval 1m0s, margin 3x)`。
- **屏幕流/截图**：`max_width=640` → 640×360 JPEG 约 21KB（原始 2560×1440 约 343KB）；`monitor=1` 单选显示器生效；屏幕流回执确认 `fps=5 quality=60 max_kbps=1200` 已下发且 WS 侧可见真实 JPEG 帧。
- **内存执行 EXE 带参**：反射执行测试 PE 后其 `argv` 为 `["tool.exe","mem-hello","42"]`（`argc=3`）——参数注入生效；32 位 PE 注入 64 位植入体被明确拒绝。
- **webhook**：真植入端上线触发通知，假飞书收到 `{"msg_type":"text","content":{...}}` 返回 `code=0`；同一地址强发旧通用 JSON 时被正确判为失败并显示 `19002` 原因。
- **驱动**：`kgameprotect.sys` 本地复算 SHA-256 与 LOLDrivers PR #428 记录一致、`Get-AuthenticodeSignature` 为 **Valid**（Microsoft Windows Hardware Compatibility Publisher / WHQL）；`/api/v1/drivers` 返回 `purpose=kill, device=\\.\kgameprotect, ioctl=0x222048`。
- **模板构建矩阵**：变更后 windows/amd64 full、windows/amd64 light、linux/amd64 full 三档均构建成功；`internal/server/builder/implant` 与 `release/implant` 全量模板 MD5 一致。

### ⚠️ 本轮已知边界（未修，已列入 ROADMAP P0）
- `exe_mem` 下**载荷自行退出会带走植入体**（载荷 CRT 内部调用 `ExitProcess` 拦不住）；**Go 编译的载荷不能这样跑**（双 runtime 冲突）；内存执行不重定向 stdout。
- BYOVD 换成 kgameprotect 后**失去任意内核读写**，**PPL 保护进程杀不掉**（`ppl_kill` 仅剩句柄窃取路线）。
- 本机 AV 会拦截新编译的 **386 位 Go 载荷**（换文件名亦然），32 位链路未能本机实测。
- garble 与 Go 版本不兼容（garble v0.16 要求 Go ≥ 1.26，本机 go1.25.0），混淆选项在升级 Go 前不可用（界面已如实提示）。
- **未在本机加载内核驱动**：驱动加载与真实击杀由操作员在授权环境实测。

## [v1.3.2] - 2026-09-14

重点：Web 控制台防资产测绘、配置写入可靠性、跨平台载荷构建修复（对应 issue #1 / #2）。

### 🛡 Web 控制台防资产测绘（issue #1）
- **基础认证前置门槛**：新增 `web.*` 配置（`basic_auth_enabled` / `basic_auth_user` / `basic_auth_password`(bcrypt) / `unauth_mode` / `decoy_title` / `allow_cidrs`），在控制台与全部管理 API 前加一道认证，阻止 Fofa/Quake/Hunter 等测绘引擎抓取收录；设置页「安全 → 防资产测绘」可视化配置，保存即热生效。
- **未认证响应双模式**：`basic` = 401 + 认证挑战（浏览器弹框，前端照常可用，默认）；`disguise` = 纯 404 伪装（不泄露任何 C2 特征）。
- **隐蔽入口**：disguise 模式下浏览器无法弹认证框（服务端不返回 401 挑战，且浏览器不会把 URL 内嵌凭据带到 JS/CSS 子资源请求），新增 `GET /__gate`：
  - `/__gate?k=<stealth_key>` 带密钥直接进入（可书签，无需输入）；
  - `/__gate` 返回 401 挑战，浏览器弹框输入控制台凭据即可进入；
  - 两者都只种下 HttpOnly 入口 Cookie（30 天），之后前端与 API 凭 Cookie 通行；`/` 与其他任何路径依旧 404 伪装。
- **不破坏业务**：植入端 `/api/v1/implant/*`（注册/心跳/结果/一条命令上线载荷/UAC 一次性载荷）完全豁免；已持有 API Key / JWT 的脚本、AI 副驾驶与 MCP 调用继续放行；C2 监听器端口不受影响。
- 附带加固：`/api/v1/health` 纳入防护（原先 200 响应是测绘指纹）、`robots.txt` 拒绝收录、入口密钥用 hex 生成（base64 的 `+` `/` 放进 URL 查询串会被解析坏）。

### ⚙️ 配置写入可靠性（issue #2 第一部分）
- **原子写 + 读-改-写**：配置保存改为 YAML 节点树「只改目标 key → 同目录临时文件 fsync → rename 原子替换」，不再使用 `viper.WriteConfig()`。修复点：不再丢字段、**不再抹掉手工维护的注释与字段顺序**、写入中途中断不会损坏配置。
- **凭据必须落盘**：首次启动生成的 admin 密码 / JWT key / 监听加密 key 若写入失败，将**直接终止启动并打印配置路径**（此前只打 WARNING 继续运行，导致每次重启都重新生成随机密码 → 用户被永久锁在门外）；配置文件不存在时自动创建（含父目录）。
- 启动日志打印实际使用的配置路径，避免"改了 A 文件、服务端读的是 B 文件"这类排查困难。
- 修复设置页保存破坏 API Key 的事故：前端脱敏回显值（`Qing****2026`）曾被当作新密钥整组写回，现前端不再回传该字段、后端忽略含 `****` 的条目。
- 测试新增 14 项（auth 10 + config 4：伪装/挑战/凭据/植入端豁免/API Key 放行/CIDR/入口 Cookie、注释保留、缺文件创建、示例文件保护）。

### 🔧 跨平台载荷构建修复（issue #2 第二部分）
- **非 Windows full 档构建失败**：`stompShellcode` 仅存在 `windows && !light` 与 `light` 两个实现，macOS/Linux 的 full 档报 `undefined: stompShellcode`；补 `!windows && !light` 空实现。
- **非 Windows light 档编译失败**：`features_unix.go` / `injection_unix.go` / `screen_stream_unix.go` 构建标签只写了 `!windows`，与 `features_stub_light.go` 重复定义；补齐 `&& !light`。
- 实测验收：`{windows,linux,darwin} × {amd64,arm64} × {full,light}` 构建矩阵全部通过（此前仅 Windows 可用）。

### 🧪 工程
- 新增本地打包脚本 `scripts/package_release.ps1`（复刻 CI 布局，输出 6 平台 zip）；CI `-ldflags` 版本号同步。

## [v1.3.1] - 2026-09-08

重点：Agent 执行可靠性重构、会话实时事件推送、Web 控制台体验优化。

### 🤖 Agent 执行可靠性
- **全原子工具面**：命令/文件/进程/凭据/截屏/插件/内存加载等一次性工具全部原子化（服务端自动等待，一次调用直接返回最终结果）；agent 工具清单移除 `task_submit/task_result/task_wait/run_command`，杜绝编造 task_id 引发的 task not found 死循环。
- **信息收集收敛**：侦察/枚举类请求按固定清单一次收齐 → 命令级去重（同命令只下发一次，重复调用回放结果）→ 结构化情报报告收尾；不再重复刷命令、不再空转到轮数上限后只给工具清单。
- **建议/咨询类分流**：只要「建议/思路/方案」不执行时只做轻量取材（session_context/attack_suggest）直接输出可执行方案，不自动展开侦察链；会话在线状态只信工具返回值，不再臆断掉线。
- **短消息纯聊**：极短输入（如「1」「好」）走独立纯聊通道（无工具、token 封顶、一两句回应），不再触发长篇执行回复。
- **剧本 AI 建议补发**：剧本完成后的 AI 分析生成较慢或超限时先出兜底摘要，analysis 晚到仍会补发正式建议；服务端分析超时放宽至 90s。

### 🌐 Web 控制台
- **Agent 执行内联**：移除副驾驶页独立 Agent 状态条/计划面板，执行过程以带 icon 的日志行（🎯/🔧/✅/❌）内联进聊天气泡，随执行实时滚动并可随历史消息保存。
- **会话列表实时刷新**：TCP/HTTP/WS/MQTT/relay 监听器的上线/下线/复活统一广播 WS 事件，Sessions 页即时更新（此前仅 10s 轮询兜底）；副驾驶侧栏会话列表 10s 静默刷新。

### ⚙️ 稳定性 / 工程
- **植入端心跳隔离**：心跳改异步入队，绝不阻塞主读循环（消除长任务期间掉线/network error）；服务端心跳超时可配（`listener.heartbeat_timeout`），任务运行中按 busy 宽限 ×3 判活。
- **原子 exec 工具（TaskOrchestrator）**：下发命令由服务端统一创建→推送→等待→归位，结果与命令一一对应。
- **新增剧本** `capability-assess`（原子 exec 链快速评估目标能力）；新增 `scripts/smoke.sh` 核心链路冒烟回归脚本（release gate）。
- i18n：修复侧边栏缺失的 `nav.settings` 键；时间线/轨迹结构化展示与审计保留。

---

## [v1.3.0] - 2026-09-07

大版本：Agent 智能升级、安全加固、Web 多语言与 Agent 控制台、工程化与稳定性。

### 🧠 Agent 智能
- **目标驱动执行**：Agent 收到复杂/多步目标会先输出【执行计划】并逐步推进；run 记录 objective 与计划进度，跨消息保持（会话记忆续接）。
- **上下文压缩**：历史过长时保留 system + 最近 14 条，更早的工具结果折叠为一行摘要，防止长任务 token 爆炸。
- **动作审计**：每次工具调用写结构化日志（component=agent-audit：run/tool/args/成败），动作可追溯。
- （此前 v1.2.1 已具备：异步不阻塞、SSE 流式、连续记忆、自主提权闭环、失败刹车。）

### 🛡 安全加固
- **API Key 一键轮换**：设置页轮换生成新密钥（仅一次性展示并复制），支持整组替换/删除/脱敏展示。
- **密钥环境变量覆盖**：`TOSHELL_AUTH_JWT_KEY` / `TOSHELL_AUTH_API_KEYS` / `TOSHELL_LISTENER_ENCRYPTION_KEY` / `TOSHELL_AI_API_KEY` 等可用环境变量注入敏感配置。
- 登录防爆破锁定（此前已具备：5 次失败锁 5 分钟）。

### 🌐 Web 多语言 + Agent 控制台
- **i18n（中/英）**：新增语言切换（顶栏与登录页），导航/侧栏/页头/登录页双语；字典可扩展。
- **Agent 控制台视图**：副驾驶页顶部显示当前 run 的目标、执行计划步骤与运行状态，任务结束自动收起。
- 浅色主题（此前已具备的 CSS 变量与切换按钮保持可用）。

### ⚙️ 工程 / 稳定性
- **版本号升级至 v1.3.0**；README/USAGE/本文件同步。
- **CI**：`check-latest` 修正 Go 工具链；构建时 `-ldflags` 注入 `version/commit/buildTime`（`-version` 可溯源）。
- **任务幂等**：Complete/Fail 对已终态任务忽略重复结果帧，杜绝重连补发导致的重复副作用。

### 🧪 测试
- Agent 计划解析、上下文压缩单测；既有 SSRF/工具/状态机等测试全通过。

### ⏳ 未纳入 v1.3.0（规划 v1.4+，详见 [ROADMAP.md](ROADMAP.md)）
- 高阶红队能力（内网探测/端口扫描/共享枚举、PsExec/WMI 横向、NTLM PTH）**不做植入端内置**，改由服务端工具库 + 远程加载（`remote_download`→`plugin_load`/`fileless_exec`）实现，保持植入端轻量。
- 多 Agent 编排委派、Agent 分层记忆持久化到 sqlite、轨迹时间线回放面板。

## [v1.2.1] - 2026-08-28

本次更新的核心是把「指令式的 AI 副驾驶」升级为「真正自主的 Agent」，并配套一批任务执行稳定性的修复。

### ✨ 自主 Agent（新特性）

- **异步自主执行（不阻塞对话）**
  - 交代任务后立即返回 `run_id`，后台 goroutine 自主完成完整 ReAct 循环（侦察 → 行动 → 等结果 → 复盘）；
  - 支持**取消**、**并发上限**（`ai.agent_concurrency`，默认 2）、超出排队。

- **SSE 流式思考可见**
  - agent 的推理（reasoning_content）与每步工具执行，经 SSE 事件流实时推送前端；
  - 前端展示「思考中」、打字机光标、工具轨迹滚动。

- **连续上下文记忆**
  - 同一 agent 会话跨消息追加记忆（`session_id` 续接），agent 记住全部历史判断，能持续规划。

- **System 提示真正生效**
  - 修复此前 `RunAgent` 未注入系统提示的问题，现在注入完整角色 + 方法论（自主提权闭环、失败恢复、只输出结论与建议、联网搜索约定）。

- **只输出结论与建议**
  - 最终答复聚焦【现状】+【建议】+【为何】，不再大段堆砌原始结果；已执行步骤摘要优化。

- **审慎决策 + 失败刹车**
  - 提权等高风险操作**先评估后谨慎行动**，不再一提提权就无脑连发工具；
  - 工具连续失败 ≥3 次自动**强制收敛**，输出建议而非无限重试；
  - **严禁**对不存在/不匹配的工具执行内存注入（避免崩溃植入端）。

### 🛠 稳定性修复

- **任务结果乱序修复（`clearResultCache`）**
  - 根因：服务端重启后 task.ID 复用旧结果缓存，导致任务输出错位（如 `whoami` 返回 `systeminfo`）；
  - 修复：植入端每次重连清空结果缓存。

- **任务/命令超时保护**
  - 植入端所有任务（含重活 plugin_exe/BOF 等）强制超时（轻 90s / 重 180s），不再无限占住 worker，任务不卡在 `sent`。

- **串行防错乱**
  - agent 每轮**只执行一个工具**，结果与任务一一对应，不再并发串联。

- **task_wait 短路**
  - 等待不存在任务或会话离线时**立即返回错误**，不再空转到超时（避免 network error）。

- **前端轮询兜底**
  - 轮询 `status()` 作为可靠主路径，SSE 仅作实时增强；SSE 错误**静默**，不再覆盖已显示内容。

### ⚙️ 其它

- **启动随机延迟修复**：反沙箱延迟从 25s/15s + 0~20s 降到 5s/3s + 0~3s，默认 5~30 秒调为 2~10 秒，上线时间从约 50s 降到约 2s。
- **版本号升级至 v1.2.1**，README / USAGE 更新自主 Agent 说明与更新日志。

### 🧪 新增测试

- SSRF 防护（拒绝内网/回环/保留地址）
- 工具元数据推断（platform/arch/usage/kind）
- `isRiskyTool` 权限判定
- run 状态机、并发信号量

---

## [v1.2.0] - 2026-08-27
- AI 副驾驶（LLM ReAct，30+ 工具），自动注入在线会话上下文、连续编排「侦察→行动→等结果→复盘」；支持 Markdown 结构化输出。
- 权限模式与操作审批（全自动 / 正常模式，影响会话的操作执行前需用户确认，任务流除外）。
- 任务流（剧本）统一：副驾驶任务面板与「任务模板」页同源，可编辑/删除；一键执行跑完自动 AI 总结。
- 多通道监听（TCP / HTTP / WebSocket / MQTT），监听列表实时统计在线会话数。
