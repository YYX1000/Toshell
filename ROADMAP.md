# ToShell 后续优化路线（ROADMAP）

> 记录 v1.3.3 之后待优化的方向。每条标注现状（代码事实）、问题/根因、目标与验收方式，按优先级排序。
> 已完成的项归档在本文件底部「已完成」章节；更新日志见 `CHANGELOG.md`。
> **本文只聚焦「下一步要做什么、为什么」**，不重复发版说明。

---

## 当前版本状态（v1.3.3）

| 能力 | 状态 |
|---|---|
| 会话判活 | ✅ 不再抖动（阈值含 3 倍心跳余量 + 广播去抖） |
| 屏幕流/截图（Windows） | ✅ 参数化（fps/quality/max_kbps/monitor/max_width）+ 带宽自适应 + 服务端限速 |
| 屏幕流/截图（Linux/macOS） | ❌ 仍为 stub |
| 内存执行 | ✅ shellcode / BOF / DLL / **EXE（反射映射 + 参数注入）** |
| BYOVD 驱动 | ✅ 内置 kgameprotect（仅进程终止 IOCTL）；❌ 失去任意内核读写（PPL 无法直接改 Protection） |
| C 植入端工具链 | ✅ 探测鲁棒（配置/环境变量/便携目录/注册表 PATH）+ 架构校验 |
| 通知 | ✅ 飞书/钉钉/企业微信/Slack/Discord 各自结构 + 业务码判定 |
| 平台工具库 `data/tools/` | ❌ 未建设（P2 前置项） |

---

## P0 — 下一版优先（稳定性 / 能力补齐 / 发版门禁）

### 1. 驱动能力补齐：把「任意内核读写」这一档补回来（PPL 回归）

- **现状**：v1.3.3 用 `kgameprotect.sys` 替换了 `RTCore64.sys`（前者只暴露进程终止 IOCTL `0x222048`）。好处是落地特征面小、能杀普通杀软/EDR；代价是**失去任意内核虚拟地址读写**，因此 `ppl_kill` 只能走句柄窃取，**PPL 保护进程（Defender MsMpEng 等）清不掉**。
- **目标**：驱动能力**分档可插拔**，不同用途用不同驱动，而不是"一个驱动包打天下"：
  - 驱动档案增加 `purpose`：`kill`（进程终止）/ `rw`（任意内核读写，用于 PPL）/ `both`；
  - 支持**多驱动内置**（`internal/server/drivers/` 目录 + 每驱动元数据：设备名、服务名、IOCTL、入参布局、能力档、SHA-256、签名者、来源出处）；
  - `ppl_kill` 自动挑选具备 `rw` 档的驱动；没有则明确提示"当前无 rw 档驱动，PPL 清除不可用（走句柄窃取）"。
- **附带加固**（同批做）：
  - 加载前自检：签名（`signtool verify /pa` 语义）+ SHA-256 与档案一致 + 是否命中微软易受攻击驱动黑名单（`CiValidateFileObject`/注册表 `VulnerableDriverBlocklist`）→ 命中就直接拒绝加载并说明原因；
  - 卸载兜底：进程重启后残留的驱动服务自动清理（当前依赖操作员手点「卸载驱动」）；
  - 驱动档案 manifest 化（`release/drivers/manifest.json`），支持操作员自行放入 .sys + 填元数据（不改代码即可扩展）。
- **验收**：放入一个 `rw` 档驱动后，`ppl_kill` 能打印命中的 EPROCESS 地址与 Protection 原值/新值；放入 `kill` 档驱动后 `byovd_kill` 能杀掉普通杀软进程；黑名单里的驱动（如 dbutil_2_3）加载时被明确拒绝。

### 2. 内存执行加固（`exe_mem` 的可用边界收窄）

- **现状**：`exe_mem`（反射映射 + PEB 命令行注入）已验证参数可用；但有三条硬边界：
  1. 载荷若自行退出，其 CRT（msvcrt）内部调用的 `ExitProcess` 会**带走宿主植入体**（已对镜像自身 IAT 的 ExitProcess/TerminateProcess 做 ExitThread 重定向，拦不住 CRT 内部调用）→ 需运行时 hook；
  2. Go 编译的载荷不能这样跑（宿主也是 Go，两个 runtime 冲突）；
  3. 未处理 TLS 回调与 .NET/CLR 载荷；无 stdout 捕获。
- **方案**：
  - **hook `ExitProcess`/`RtlExitUserProcess`**（IAT + 运行时 inline hook 双保险），把载荷的退出改成只退线程；
  - **按载荷类型自动选路**：native PE（无 TLS 依赖）→ `exe_mem`；带 TLS 回调/复杂 CRT/.NET → donut（`kind=exe`，Thread=1 + ExitOpt=1）；Go 载荷 → 直接拒绝并提示走落地执行；
  - 可选 **stdout/stderr 重定向捕获**（管道 + 读取线程），让"跑完即退"的工具也能在 `exe_mem` 下拿到输出；
  - 面板把"这条载荷能不能内存执行"的判断前置（读 PE 头：架构、TLS 目录、是否 .NET、是否 Go 特征），而不是失败后才知道。
- **验收**：`attrib.exe`/`cmd.exe` 这类原生工具在 `exe_mem` 下执行完，植入体**不掉线**；带参数的输出类工具能拿到返回内容；Go 载荷被明确拒绝。

### 3. 屏幕流 / 截图：跨平台 + 增量捕获

- **现状**：只有 Windows 实现（GDI BitBlt + PrintWindow 回退，参数化后帧率上限由带宽而非采集能力决定）；Linux/macOS 全是 stub。
- **P0.3 跨平台**：Linux 用 **X11 (XGetImage)** 起步（Wayland 走 PipeWire 需 portal 授权，作为后续）；macOS 用 **ScreenCaptureKit**（需屏幕录制权限，明确提示授权路径）；screenshot 同步补全。
- **P0.2 DXGI**：Win8+ 换 **Desktop Duplication API** 做增量帧捕获（当前每帧全量 BitBlt，1080p 下 CPU 占用偏高），保留 GDI 作为回退。
- **兼容矩阵**：Win7/8.1/10/11 + Server Core/服务会话/锁屏 各场景登记行为；无桌面时明确返回原因帧（已实现，补矩阵记录）。
- **验收**：Linux amd64 能出图；Win10/11 桌面会话 ≥5fps 且 CPU 占用可控；Server 无桌面返回明确错误。

### 4. 发版门禁：一条命令的端到端冒烟

- **现状**：发版靠人工点测；本会话中出现过"改动后忘重编服务端导致验证用旧二进制""模板构建档位失败未被发现"这类事故（都已通过人工构建三档规避）。
- **方案**：`scripts/e2e_smoke.ps1`（幂等、可本地跑、也进 CI）：
  1. 起临时服务端（独立端口 + 临时配置/DB）→ 校验 `/health`、`/builders`、鉴权；
  2. 用 API 构建 **windows full/light + linux full** 三档载荷（验证模板与工具链）；
  3. 本地跑一个真植入端（TCP）→ 校验注册/心跳/任务下发链路（含一条 `whoami` 类任务返回）；
  4. 校验关键新接口存在且鉴权正常（`/drivers`、`byovd-kill`、`fileless-exec`、`screen-stream`）；
  5. 清理临时进程/文件，输出成败摘要（非 0 退出即阻断发版）。
- **验收**：CI 在 tag 前跑该脚本；本地 `scripts/e2e_smoke.ps1` 一条命令可复现。

---

## P1 — Agent / 智能化（延续 v1.3.x 方向）

- **分层记忆持久化**：agent 会话记忆落 sqlite（当前 run 内存保留 30 分钟即清），支持跨重启召回历史目标/判断/已采集情报；前端可按会话回放历史 run。
- **多 Agent 编排 / 委派**：把单 run + 剧本 delegate 升级为「规划者 + 执行子代理」，子代理并发跑不同会话/路径，规划者聚合结果并去重。
- **执行轨迹回放面板**：timeline 已有结构化事件（thinking/tool/result），前端做可暂停、可跳转、可导出的回放视图。
- **信息收集「一键情报库」**：侦察报告结构化（用户/组/IP/端口/杀软/凭据线索）写入 intel 库，后续会话直接 `intel_query` 命中，避免重复侦察。
- **工具面原子化/收敛**：playbook 与 agent 共用统一原子工具层；长耗时任务（大文件传输、凭据 lsa）设计为「可查询的原子任务」而非仅轮询。
- **Agent 与 BYOVD/EDR 对抗联动**：把「检测到杀软 → 选路（驱动击杀/EDR 失明/进程注入）→ 验证是否成功」做成确定性剧本，减少模型自由发挥。

## P2 — 平台工具库与远程加载（不增植入端体积）

> **设计原则**：植入端保持「小而精」——只内置命令执行/文件/进程/注入/凭据/截屏/网络等底层原语；高阶能力（端口扫描、横向、凭据传递、内网探测）**不编译进植入端**，由服务端工具库按需远程加载执行。

### 2.1 工具库建设（前置，本会话已确认范围待开工）
- 目录按 **平台/架构** 组织：`data/tools/<os>/<arch>/<tool>`，同级 `manifest.json` 记元数据：用途、平台/arch、调用参数示例、**SHA-256**、来源出处、许可、体积、最后校验时间。
- **预置范围（已确认）**：只预置**自研脚本型工具**（纯文本、可审计、无版权/供应链风险）：端口扫描、共享枚举、内网存活探测、凭据线索收集、域信息收集等 PowerShell/Bash 脚本；
- **第三方二进制不预置**：走「按需下载 + sha256 校验 + 版本轮换」，下载源与哈希写进 manifest（agent 用 `remote_download` 拉取到服务端 `data/tools/`，再分发到会话）。
- 校验：下载后强校验 sha256（不匹配即拒绝并告警），记录来源 URL 与时间；定期轮换版本。

### 2.2 能力链（都走远程加载，不内置）
- **内网探测链**：端口扫描/主机发现/共享枚举 → 优先加载轻量扫描器跑在目标会话上；结果收敛进情报库，落成确定性 playbook。
- **横向移动**：加载成熟横向工具/载荷到会话执行；复用既有 exec 原子机制、会话忙期判活与高危审批护栏。
- **凭据利用（PTH/Kerberoast 等）**：凭据**采集**仍走植入端原生原语，**利用**工具走远程加载；纳入高危审批。
- **免杀/规避载荷远程化**：新注入方式、新载荷形态按需下发，不常驻；痕迹清理（amcache/事件日志）做成轻量命令链 playbook。
- **验收**：植入端二进制体积/功能面不随上述能力增长；agent（或一条指令）能完成「下载→校验→内存加载→执行→回传→清理」闭环；工具元数据缺失/哈希不符时明确拒绝执行。

## P3 — 平台 / 工程

- **多通道一致性**：MQTT/relay/HTTP-polling 与 TCP 的判活、忙期、任务重放语义对齐（TCP 最完整；本会话已统一判活入口 `Session.IsAlive`，其余语义仍待对齐）。
- **Release/打包**：产物校验（zip 内 `toserver -version` 与 tag 一致）+ `checksums.txt`（sha256）+ 打包清单；本地 `scripts/package_release.ps1` 与 CI 逻辑继续保持镜像。
- **配置热更新边界**：心跳超时等会话参数改动后对存量会话的生效时机文档化（本会话改成自适应阈值后，需说明"改动后新会话立即生效、存量会话按采样自适应"）。
- **前端**：任务列表虚拟滚动（大量任务不卡）；Sessions/仪表盘 WS 事件统一订阅组件（现在多处重复实现）；杀软对抗页信息架构继续收敛（本会话已重排 BYOVD 区块）。
- **可观测性**：屏幕流帧限速丢弃数、广播去抖抑制次数等运行指标暴露到界面/日志汇总（当前只在日志里）。

---

## 已完成（归档，见 `CHANGELOG.md` 对应版本）

<details>
<summary><b>v1.3.3（2026-09）</b></summary>

- ✅ **会话判活去抖（P0-1）**：阈值 = `max(heartbeat_timeout, 3×实测心跳间隔)`（按会话自适应 + 首周期保护），三监听器统一判活、扫描 10s→5s；`session_offline` 延迟 15s 观察窗，窗口内重连不发任何事件；重复注册不再重复广播上线（webhook 同步去重）。实测 60s 心跳跑 220s 零抖动、真掉线仍按 3m1s 判死。
- ✅ **屏幕流/截图参数化（P0.2 Windows 部分）**：`fps/quality/max_kbps/monitor/max_width/format`；植入端带宽自适应（超限降质→降帧+限宽，有余量逐级恢复）；服务端帧限速/合并（硬上限 10fps）；捕获失败回传原因帧。实测 2560×1440 → 640×360 JPEG（343KB → 21KB）。
- ✅ **内存执行 EXE 带参数**：`fileless_exec` 新增 `exe_mem`（反射映射 + PEB 命令行注入 + IAT 退出重定向 + 架构校验 + `wait_ms`）；修正 donut 路径 `Thread/ExitOpt` 导致**杀宿主**的老 bug，并支持 donut 参数。实测 argv 正确注入。
- ✅ **BYOVD 驱动换代**：删除 `RTCore64.sys`，内置 **kgameprotect.sys**（WHQL 签名、设备 `\\.\kgameprotect`、终止 IOCTL `0x222048`、SHA-256 与 LOLDrivers PR #428 一致）；新增 `byovd_kill` 任务/接口/前端入口；`ppl_kill` 保留句柄窃取路线（能力变化见 P0-1）。
- ✅ **C 植入端工具链探测（issue #6）**：配置 → 环境变量 → 便携目录 → 常见安装目录 → PATH → **注册表 PATH** 逐级探测 + `gcc -dumpmachine` 架构校验；garble 改为真实构建探测（修掉"显示可用但构建必失败"）。
- ✅ **多平台 webhook（issue #7）**：飞书 `msg_type`+`content.text`／企业微信 `msgtype`+`text.content`／Slack／Discord／钉钉 markdown 各发各的结构；飞书 body 内加签；响应体业务码（`code`/`errcode`/`ok`）判定成功与失败原因。真植入端上线实测通知送达。
- ✅ **一键命令上线**：下载地址改为服务端按配置解析（`public_host` 优先，否则控制台地址/`server_url` 主机/内网 IP，不可达时显式告警）；Windows 6 种 + Linux 6 种免杀变体；载荷列表实时重新生成。
- ✅ **版本 1.3.3**：全量版本号统一；CI `main.version` 跟随 tag。

</details>

<details>
<summary><b>v1.3.2（2026-09-14）</b></summary>

- ✅ Web 控制台防资产测绘（Basic 前置门槛 / 404 伪装 / `/__gate` 隐蔽入口，issue #1）
- ✅ 配置写入可靠性：原子写 + 读-改-写保注释、凭据必须落盘（否则拒绝启动）、设置页不再破坏 API Key（issue #2）
- ✅ 跨平台载荷构建修复（macOS/Linux full/light 标签冲突，issue #2）

</details>

---

## 附：本会话实测发现、尚未修的问题（现场记录）

| 现象 | 影响 | 处置建议 |
|---|---|---|
| garble v0.16 要求 Go ≥ 1.26，本机 go1.25.0 → 任何 garble 构建必失败 | 混淆选项不可用（界面已如实显示"不可用 + 原因"） | 升级 Go 或安装匹配版本 garble（属环境问题，非代码缺陷） |
| 本机 AV 会拦截新编译的 **386 位 Go 载荷**（换文件名也 Access denied） | 无法在本机实测 32 位载荷链路 | 换 64 位植入端验证，或用免杀/白名单环境；建议在 ROADMAP P0-4 冒烟脚本里用 64 位载荷 |
| Go 编译的 EXE 用 `exe_mem` 反射执行会崩宿主（双 Go runtime） | 边界已写入面板与任务输出 | P0-2 里做"载荷类型自动选路 + 明确拒绝" |
| `exe_mem` 下载荷自行退出会带走植入体（CRT 内部 ExitProcess） | 只对"跑完即退"的工具致命 | P0-2 里做 ExitProcess hook |
| PPL 进程杀不掉（kgameprotect 无内核读写） | Defender 等 PPL 保护进程需句柄窃取 | P0-1 里补 `rw` 档驱动档案 |
| 一条上线命令的下载地址依赖人工配置 `public_host` | 配置错则命令不可用（已有告警与自动回退） | 可加"服务端主动探测该地址可达性"的自检（P3 可观测性一起做） |
