# ToShell 后续优化路线（ROADMAP）

> 记录 v1.3.5 之后待优化的方向。每条标注现状（代码事实）、问题/根因、目标与验收方式，按优先级排序。
> 已完成的项归档在本文件底部「已完成」章节；更新日志见 `CHANGELOG.md`。
> **本文只聚焦「下一步要做什么、为什么」**，不重复发版说明。

---

## 当前版本状态（v1.3.5）

| 能力 | 状态 |
|---|---|
| 会话判活 | ✅ 不再抖动（阈值含 3 倍心跳余量 + 广播去抖） |
| 屏幕流/截图（Windows） | ✅ 参数化（fps/quality/max_kbps/monitor/max_width）+ 带宽自适应 + 服务端限速 |
| 屏幕流/截图（Linux/macOS） | ❌ 仍为 stub（P0-3，下一版） |
| 内存执行 | ✅ shellcode / BOF / DLL / **EXE（反射映射 + 参数注入）**；✅ **下发前 PE 预检**（Go 载荷/架构不符/.NET 直接拒绝，TLS 无重定位给出警告，可 `force` 强制） |
| BYOVD 驱动 | ✅ **不内置任何驱动**（操作员自备 .sys + manifest 档案）；✅ 驱动击杀/档案登记/会话级记忆；✅ **加载前自检**（sha256 一致性硬拦 + Authenticode 签名者 + 易受攻击驱动黑名单提示） |
| C 植入端工具链 | ✅ 探测鲁棒（配置/环境变量/便携目录/注册表 PATH）+ 架构校验 |
| 通知 | ✅ 飞书/钉钉/企业微信/Slack/Discord 各自结构 + 业务码判定 |
| 运行时行为足迹 | ✅ 启动自动"枚举进程找杀软"默认关闭（`evasion_scan` 显式开启）；pclntab 高信号名中性化；**BOF 默认不编译（`beacon*`=0）**；**Go buildinfo/构建 ID 已擦除**；启动延迟/心跳节奏可按载荷配置 |
| 落地能力（"起不来"的正解） | ✅ **代码签名**（pfx/证书指纹 + 签名后复核 + 前端显示）；✅ **8 条加载器链**（白加黑/计划任务/LOLBin/内存加载）+ 降级建议；⚠️ 真实证书获取与目标机信任链落地仍需操作员准备 |
| 发版门禁 | ✅ `scripts/e2e_smoke.ps1`（临时服务端 + 三档载荷 + 关键接口 + 可选真植入端上线）+ CI 侧版本号与包内容校验 + `checksums.txt` |
| 平台工具库 `data/tools/` | ❌ 未建设（P2 前置项） |

---

## P0 — 下一版优先（稳定性 / 能力补齐 / 发版门禁）

### 1. 驱动体系：操作员自备 + 多档能力 + 加载前自检（PPL 回归）

- **v1.3.4 已完成**：**加载前自检**（`internal/server/drivers/verify*.go`）——manifest `sha256` 一致性不一致直接拒绝下发；Authenticode 用 `WinVerifyTrust` 判签名与篡改、尽力取签名者；易受攻击驱动黑名单只读本机策略与名单文件、给出"1275 静默拒绝"提示；新增 `GET /api/v1/drivers/{name}/verify`，前端驱动按钮直接标出"哈希不符/未签名"并展示警告。
- **现状**：不再内置任何驱动（v1.3.3 起），`ppl_kill` 只能走句柄窃取，**PPL 保护进程（Defender MsMpEng 等）清不掉** —— 因为没有具备内核读写的 `rw` 档驱动。
- **待做**：
  - 驱动档案的 `purpose` 分档（`kill` / `rw` / `both`）真正驱动选路：`ppl_kill` 自动挑 `rw` 档；没有就明确提示"当前无 rw 档驱动，PPL 清除不可用（走句柄窃取）"；
  - **卸载兜底**：进程重启后残留的驱动服务自动清理（当前依赖操作员手点「卸载驱动」；可按服务名前缀 + 驱动目录清单做一次"清场"任务）；
  - 目录签名（catalog）驱动：目前只能给"未内嵌签名"，可补 `CryptCATAdminCalcHashFromFileHandle` 路线。
- **验收**：放入 `rw` 档驱动后 `ppl_kill` 能打印命中的 EPROCESS 与 Protection 原值/新值；`kill` 档驱动能杀普通杀软进程；manifest 哈希不符的驱动在加载时被 400 拒绝。

### 2. 内存执行加固（`exe_mem` 的可用边界收窄）

- **v1.3.4 已完成**：**下发前 PE 预检**（`internal/server/builder/pecheck.go` + `handlers_fileless.go`）——手写解析 PE 头（架构/DLL/TLS/CLR/重定位）+ Go 载荷识别（`.gopclntab`、`\xff Go buildinf:`），`exe_mem`/`dll` 命中硬边界直接 400 拒绝并给出 `reasons`/`suggestion`/`pe_info`，`warn` 类照常下发但回传 warnings，`force:true` 可强制（留痕）；前端展示原因并支持强制下发。
- **待做**：
  - **hook `ExitProcess`/`RtlExitUserProcess`**（IAT + 运行时 inline hook 双保险），把载荷的退出改成只退线程 —— 当前"跑完即退"的载荷仍会带走宿主；
  - **stdout/stderr 重定向捕获**（管道 + 读取线程），让 `exe_mem` 跑出来的工具能给回输出；
  - donut 路径的 `warn` 项细化（TLS/复杂 CRT/.NET 各自给更明确的结论）。
- **验收**：`attrib.exe`/`cmd.exe` 这类原生工具在 `exe_mem` 下执行完，植入体**不掉线**；带输出的工具能拿到返回内容；Go 载荷被明确拒绝（已达成）。

### 3. 屏幕流 / 截图：跨平台 + 增量捕获（**v1.3.4 未动**）

- **现状**：只有 Windows 实现（GDI BitBlt + PrintWindow 回退，参数化后帧率上限由带宽而非采集能力决定）；Linux/macOS 全是 stub。
- **P0.3 跨平台**：Linux 用 **X11 (XGetImage)** 起步（Wayland 走 PipeWire 需 portal 授权，作为后续）；macOS 用 **ScreenCaptureKit**（需屏幕录制权限，明确提示授权路径）；screenshot 同步补全。
- **P0.2 DXGI**：Win8+ 换 **Desktop Duplication API** 做增量帧捕获（当前每帧全量 BitBlt，1080p 下 CPU 占用偏高），保留 GDI 作为回退。
- **为什么没做**：这三项都**必须在真机（Linux 桌面 / macOS 授权 / Win10+ GPU）上验证**，本机连新生成的载荷都无法执行（见第 5 条），写了只能交付未验证代码；放到下一版在有验证条件时做。
- **验收**：Linux amd64 能出图；Win10/11 桌面会话 ≥5fps 且 CPU 占用可控；Server 无桌面返回明确错误。

### 4. 发版门禁：一条命令的端到端冒烟

- **v1.3.4 已完成**：`scripts/e2e_smoke.ps1`（临时服务端 + 鉴权/关键路由校验 + windows full/light + linux 三档载荷构建 + 可选真植入端上线与任务下发 + ✅/⚠️/❌ 摘要，有 ❌ 即非 0 退出）；CI 侧补了 **tag 与 `toserver -version` 一致性校验**、**zip 内容清单校验**（`implant/main.go`、配置样例、许可证等）与 **`checksums.txt`**（sha256 清单随 Release 发布，本地 `scripts/package_release.ps1` 同步生成）。
- **待做**：把该脚本接进 CI（tag 前 workflow）；补"发布包内 `toserver -version` == tag"的本地校验（CI 已有）。
- **验收**：CI 在 tag 前跑该脚本；本地 `powershell -File scripts/e2e_smoke.ps1` 一条命令可复现。

### 5. 动态（行为）查杀：先解决"起不来"，再谈"藏得深"

- **现状（本机实测，2026-09）**：
  - 装有 360 安全卫士 + 腾讯电脑管家 + 无边界安全系统的主机上，**任何新生成/未签名的 PE 一执行就被拒并删文件**：一个只有 `time.Sleep` 的 Hello-World Go 程序同样被拒（`Access is denied` + 文件被删除），`release/implants/` 历史产物已被清空；对照 MS 签名的 `notepad.exe` 副本可正常执行。
  - 所以本机看到的"动态被查杀"**判别不出载荷特征**：拦截依据是"未签名/未知 PE + 主动防御策略"。Defender 日志里唯一的 C2 类记录是历史样本的 `Behavior:Win32/CommandAndControl.A!ml`（行为判定，非本项目）。
- **已做（v1.3.4）**：启动阶段的"枚举全系统进程 + 比对 38 个杀软/分析工具进程名"默认关闭（`-tags evasionscan` 才编译）；启动随机延迟、心跳间隔/抖动可按载荷配置且服务端配置真正生效（示例配置默认改为 60s/20%）；pclntab 高信号标识符中性化；驱动加载失败回传具体 Win32 错误码；构建参数写入服务端日志便于核对。
- **已做（v1.3.5，本版重点）**：
  1. ✅ **代码签名**（`sign.go`）：pfx / 证书存储指纹两种模式，签名栈优先 signtool、回退系统自带 PowerShell（密码走环境变量），签名后立刻复核并把 `signed/signer/sign_method/sign_status/sign_message` 回传前端；**本机实测已签上**（`SignatureType=Authenticode`、签名者与指纹一致、+1.4KB），自签证书因根未受信任为 `UnknownError`（已如实区分"已签名但链不受信任"与"未签名"）。
  2. ✅ **加载器链**（`oneliner.go` + `docs/LOADERS.md`）：8 条（白加黑 DLL 侧加载 / 计划任务 + 已签名宿主 / rundll32 / mshta / regsvr32 Squiblydoo / certutil + 宿主 / 内存注入 shellcode / mshta+宿主注入骨架），每条带前置条件与风险等级；`LoaderAdvice()` 给出"直接运行 → 计划任务 → 白加黑 → 内存加载"的降级顺序。
  3. ✅ **BOF 按需编译**：默认载荷 `beaconAPI=0`（勾选后 22），full 档案最后一项高信号明文消失。
  4. ✅ **Go 构建期指纹擦除**：`\xff Go buildinf:` 魔数 / buildinfo 内版本串 / `Go build ID:` 前缀（长度不变、只置零），exe 与 dll 两条路径都接入，实测均归零。
- **待做（按收益排序）**：
  1. **真实证书落地**：目前只有自签名可用（目标机需导入受信任根）；后续可做"证书导入自检 + 目标机信任状态提示"，以及用 EV/受信任证书的完整验证记录；
  2. **内存模块 build tag 化（剩下的一半）**：`injection/edr/stomp/memexe` 等仍随 full 编译（函数名已中性化），可按需 `-tags` 裁剪；
  3. **EDR/杀软名单字符串外置**：`edr_windows.go` 的 `defaultAVProcesses` 改由服务端下发，去掉"大段杀软名单"这种高熵特征；
  4. **网络侧节奏**：默认 `interval`/`jitter` 继续收敛（已有 60s/20%），UPX 默认关闭（压缩壳本身是被行为引擎重点标记的特征）；
  5. **内存执行 hook**：`ExitProcess`/`RtlExitUserProcess` 运行时 hook + stdout 捕获（见 P0-2）。
- **验收**：在干净 VM（仅 Defender）里，默认载荷执行后 5 分钟内不触发 `Behavior:` 类拦截；在装有 360 的机器上给出"签名载荷可执行 / 白加黑链可执行"的实测记录（**需要目标机配合，本机因安全软件拦截无法执行任何新 PE**）。

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
- **Release/打包**：✅ **v1.3.4 已完成**：CI 增加 `toserver -version` == tag 校验、zip 内容清单校验、`checksums.txt`（sha256，随 Release 发布；本地 `scripts/package_release.ps1` 同步生成）。待做：tag 前接 `e2e_smoke.ps1`。
- **配置热更新边界**：心跳超时等会话参数改动后对存量会话的生效时机文档化（本会话改成自适应阈值后，需说明"改动后新会话立即生效、存量会话按采样自适应"）。
- **前端**：任务列表虚拟滚动（大量任务不卡）；Sessions/仪表盘 WS 事件统一订阅组件（现在多处重复实现）；杀软对抗页信息架构继续收敛（本会话已重排 BYOVD 区块，并把驱动加载前自检结论（哈希不符/未签名/警告）直接标在按钮与提示区）。
- **可观测性**：屏幕流帧限速丢弃数、广播去抖抑制次数等运行指标暴露到界面/日志汇总（当前只在日志里；v1.3.4 已把"真正烘焙进载荷的参数"写进构建日志）。

---

## 已完成（归档，见 `CHANGELOG.md` 对应版本）

<details>
<summary><b>v1.3.5（2026-09）</b></summary>

- ✅ **构建后代码签名（P0-5）**：pfx / 证书存储指纹两种模式；签名栈优先 signtool、回退系统自带 PowerShell（密码走环境变量、不进命令行）；签名后复核并回传签名者/状态/中文说明；前端「上次构建」显示签名结果。实测自签证书已签上（`SignatureType=Authenticode`、+1.4KB），并修掉两个真实坑：PowerShell 5.1 的 `Get-PfxCertificate` 无 `-Password`；`UnknownError` + 有签名者应判为"已签名但链不受信任"。
- ✅ **8 条加载器链 + 落地建议（P0-5）**：白加黑 DLL 侧加载 / 计划任务 + 已签名宿主 / rundll32 / mshta / regsvr32 Squiblydoo / certutil + 宿主 / PowerShell 内存注入 shellcode / mshta + 宿主注入骨架，每条带 `note`（前置条件 + 风险等级）；`LoaderAdvice()` → `loader_advice_title/tips` 给出降级顺序；新增 `docs/LOADERS.md`。
- ✅ **BOF 按需编译（P0-5）**：`-tags bof` 才编译，默认载荷 `beaconAPI=0`（勾选后 22），full 档案最后一项高信号明文消失；新增 `TestBOFIsOptIn`。
- ✅ **Go 构建期指纹擦除（P0-5）**：`ScrubGoFingerprint` 擦除 buildinfo 魔数 / 窗口内版本串 / `Go build ID:` 前缀（长度不变），exe 与 dll 路径都接入，实测均归零；单测覆盖擦除/幂等/边界。
- ✅ **版本 1.3.5**：全量版本号统一。

</details>

<details>
<summary><b>v1.3.4（2026-09）</b></summary>

- ✅ **动态查杀定位（P0-5 前置）**：本机（360 + 电脑管家 + 无边界安全系统）实测证明"新生成/未签名 PE 一执行就被拒并删除"，与载荷代码无关（Hello-World Go 程序同样被拒、MS 签名程序正常）——结论与证据写进 `CHANGELOG.md`，避免后续重复踩坑。
- ✅ **植入端默认行为收敛（P0-5）**：启动时"枚举全系统进程 + 比对 38 个杀软进程名"默认关闭（`-tags evasionscan` 才编译，前端开关默认关）；pclntab 高信号标识符与文件名中性化（`stomp*→carve*`、`memexe→imgexec`、`memload→blob`、`evasion→gate`、`loadShellcode→runBlob` 等，`light` 档案连 `beacon*` 都为 0）。
- ✅ **三个"配置了却无效"的缺陷（P0-5）**：`startup_delay_min/max` 请求字段补齐并透传（此前被静默丢弃）；`interval==0 → 5s`、`jitter==0 → 2%` 的硬编码覆盖改为优先跟服务端配置（示例配置默认 60s/20%）；驱动加载失败回传 Win32 错误码与排查结论。构建参数写入服务端日志。
- ✅ **驱动加载前自检（P0-1）**：manifest `sha256` 一致性硬拦（不一致 400 拒发）+ `WinVerifyTrust` 签名/篡改校验与签名者 + 易受攻击驱动黑名单策略提示（不内置名单）；`GET /drivers/{name}/verify`；前端标出"哈希不符/未签名"并展示警告。
- ✅ **内存执行下发前 PE 预检（P0-2）**：手写 PE 解析 + Go 载荷识别，Go/架构不符/.NET 直接拒绝并给原因与建议，TLS/无重定位/donut 给警告，`force:true` 可强制但留痕。
- ✅ **发版门禁（P0-4）**：`scripts/e2e_smoke.ps1`（临时服务端 + 鉴权/路由 + 三档载荷 + 可选真植入端上线与任务回执 + 摘要与非 0 退出）；CI 增加 tag/版本一致性、zip 内容清单校验与 `checksums.txt`。
- ✅ **版本 1.3.4**：全量版本号统一（服务端默认版本、Web、About、README、USAGE、部署脚本、打包脚本）。

</details>

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
| garble v0.16 要求 Go ≥ 1.26，本机 go1.25.0 → 任何 garble 构建必失败 | 混淆选项不可用（界面已如实显示"不可用 + 原因"） | 升级 Go 或安装匹配版本 garble（属环境问题，非代码缺陷；本机已装 v0.15.0 可用） |
| 本机装有 360/电脑管家/无边界安全系统，**任何新生成或未签名的 PE 一执行就被拒并删文件**（连 Hello-World Go 程序也一样，MS 签名程序正常） | **本机无法做任何"动态/行为"验证**（载荷上不了线、`go test` 的新测试二进制也被杀） | 动态验证放到干净 VM（仅 Defender）或 CI；长期解见 P0-5（代码签名 / 由已签名宿主加载） |
| Go 编译的 EXE 用 `exe_mem` 反射执行会崩宿主（双 Go runtime） | ✅ v1.3.4 已在下发前**明确拒绝**并给建议（P0-2 预检） | —— |
| `exe_mem` 下载荷自行退出会带走植入体（CRT 内部 ExitProcess） | 只对"跑完即退"的工具致命 | 仍需 P0-2 的 ExitProcess hook（下一版） |
| PPL 进程杀不掉（无具备内核读写的 `rw` 档驱动） | Defender 等 PPL 保护进程需句柄窃取 | P0-1 里补 `rw` 档驱动档案与 `purpose` 选路 |
| 一条上线命令的下载地址依赖人工配置 `public_host` | 配置错则命令不可用（已有告警与自动回退） | 可加"服务端主动探测该地址可达性"的自检（P3 可观测性一起做） |
