# ToShell 后续优化路线（ROADMAP）

> 记录 v1.3.1 之后待优化的方向。每条标注现状（代码事实）、问题/根因、目标与建议方案，按优先级排序。
> 更新日志见 `CHANGELOG.md`；本文只聚焦「下一步要做什么、为什么」。

---

## P0 — 用户体验/稳定性（近期优先）

### 1. 会话「掉线一会又自动恢复在线」抖动 ✅ 已完成

- **现状**：会话状态依赖心跳判活。服务端 `session.HeartbeatTimeout`（配置 `listener.heartbeat_timeout`，默认 **60s**）由 10s 周期 checker 检查；植入端心跳间隔 `implant.interval=60s` + jitter（默认 ±2s），空闲时可能 ~62s 才来一个心跳。
- **根因**：心跳间隔与超时阈值几乎零余量（任何一次心跳迟到都判离线、下一个心跳又恢复）；且 TCP 闪断重连会立刻广播 offline、重连再广播 online。
- **已完成**：
  - 判活阈值强制留余量：实际超时 = `max(heartbeat_timeout, 3 × 实测心跳间隔)`，启动日志打印 `margin 3x`；尚未采样到节奏时再放宽到 2 倍基准（首周期保护）。
  - 按会话自适应实测心跳间隔（向上立即生效、向下缓慢回收），兼容构建时自定义 `interval` 的载荷。
  - TCP / HTTP 轮询 / MQTT 三个监听器统一走 `Session.IsAlive()`，扫描周期 10s → 5s，消除窗口误差。
  - 广播去抖：`session_offline` 延迟 15s 观察窗，期间重连则不发任何事件；重复注册不再重复广播 `session_online`（上线 webhook 同步去重）。
- **实测验收**：60s 心跳（jitter 5s）植入端连续运行 220s，零状态抖动；杀掉植入端后按 3m1s 判死并广播一次 `session_offline`。
- **未做**：前端 Sessions 页「最近活跃」过渡态（服务端已消除抖动，如需再加）。

### 2. 屏幕流 + 截图优化（Windows 参数化/限速已完成，跨平台与 DXGI 待做）

- **现状**（代码事实）：
  - 截图仅 **Windows** 实现（`internal/server/builder/implant/screenshot_windows.go`），采用 GDI `BitBlt` 抓屏 + `PrintWindow(PW_RENDERFULLCONTENT)` 应用窗口回退，含虚拟屏/多显示器（`SM_*VIRTUALSCREEN`）、DPI 感知、锁屏/无交互桌面回退（`OpenInputDesktop/SetThreadDesktop`/`CreateDC`）等逻辑。
  - 屏幕流（`screen_stream_windows.go`）复用截图模块，固定 ~**1.25fps（800ms）**轮询，把 `TypeScreenFrame` 帧回传服务端 → WebSocket 推送前端。
  - Linux/macOS：`screen_stream_unix.go` 与 `features_unix.go` 均为 **stub**（返回 "only supported on Windows"）。
- **已完成（P0.1/P0.2 的 Windows 部分）**：
  - 参数化：`fps`(1-10) / `quality`(20-95) / `max_kbps`（带宽上限）/ `monitor`（多显示器单选）/ `max_width`（缩放）/ `format`，服务端校验钳制后透传；屏幕流默认 JPEG（此前固定 1.25fps + PNG）。
  - 植入端带宽自适应（超限先降画质、再降帧+限宽，有余量逐级恢复）+ 服务端帧限速/合并（超发帧丢弃，硬上限 10fps）。
  - 捕获失败立即回传带原因的 error 帧（锁屏/无交互桌面/Headless），前端展示原因并在收到正常帧后自动清除。
  - 实测验收：`max_width=640` 使 2560×1440 → 640×360 JPEG（343KB → 21KB）；`monitor=1` 单选显示器生效；屏幕流回执确认参数已下发，WS 侧可见真实 JPEG 帧。
- **待做**：
  - **P0.3 跨平台**：Linux 用 **X11 (XGetImage)/Wayland (PipeWire)**，macOS 用 **ScreenCaptureKit** 或 CGDisplayStream；screenshot 同步补全（当前非 Windows 仍为 stub）。
  - **P0.2 DXGI 增量捕获**：Win8+ 换 **Desktop Duplication API**（当前仍是 BitBlt 全量抓屏 + PrintWindow 回退，帧率上限现在由带宽而非采集能力决定）。
  - **兼容矩阵**：Win7/8.1/10/11 + Server Core/服务会话的实测登记。

---

## P1 — Agent / 智能化（延续 v1.3.x 方向）

- **分层记忆持久化**：agent 会话记忆落到 sqlite（当前 run 内存保留 30 分钟即清），支持跨重启/跨会话召回历史目标、判断与已采集情报；前端可按会话回放历史 run。
- **多 Agent 编排 / 委派**：把现有单 run + 剧本 delegate 升级为「规划者 + 执行子代理」；sub-agent 并发跑不同会话/路径，规划者聚合结果。
- **执行轨迹回放面板**：timeline 已有结构化事件（thinking/tool/result），前端做成可暂停、可跳转的回放视图（现有内联日志行作为轻量版保留）。
- **信息收集「一键情报库」**：信息收集报告关键字段（用户/组/IP/端口/杀软/凭据线索）结构化后写入 intel 情报库，后续会话直接 `intel_query` 命中，减少重复侦察。
- **工具面继续原子化/收敛**：playbook 与 agent 共用统一原子工具层；为长耗时任务（大文件传输、凭据 lsa）设计「可查询的原子任务」而非仅轮询，避免误导模型。

## P2 — 红队能力深化：远程工具加载（不增植入端体积）

> **设计原则（核心）**：植入端保持「小而精」——只内置命令执行、文件、进程、注入、凭据、截屏、网络等底层原语；所有高阶红队能力（端口扫描、横向移动、凭据传递、内网探测等）**不再编译进植入端**，而是由服务端维护工具库，按需 `remote_download` 到服务端 `data/tools/` 再由 agent 经 `plugin_upload/plugin_load` 或 `fileless_exec` **远程加载到会话内存执行**，或落地执行后清理。避免单二进制臃肿、扩大杀软特征面与审计面。

- **平台工具库建设（前置）**：`data/tools/` 预置常用红队工具（按平台/架构组织），配元数据（用途/平台/arch/参数/哈希）；agent 的 `tool_list/tool_download_status` 已支持查与等，补齐「工具来源可信校验（sha256）+ 版本轮换」。
- **内网探测链（端口扫描/主机发现/共享枚举）**：不做原生内置；优先 remote 加载轻量扫描器（如基于既有 exec 的确定性脚本/静态工具）跑在目标会话上，agent 收敛结果进情报库；也可由服务端直接对会话内网发起扫描后经会话回传。收敛为确定性 playbook。
- **横向移动（PsExec/WMI/SCM 服务）**：加载成熟横向工具/载荷至会话执行；复用既有 exec 原子机制、会话 busy 判活与高危审批护栏（normal 模式需确认）。
- **NTLM 凭据传递（PTH）/ Kerberoast 等**：凭据原语仍走植入端原生（采集），但 PTH/票据利用工具走远程加载执行；纳入高危审批。
- **免杀/规避**：植入端维持现混淆/反沙箱/EDR 对抗基线；新规避手段（新注入方式、新载荷形态）以远程工具/载荷形式按需下发，避免常驻；痕迹清理（amcache/事件日志等）做成轻量命令链 playbook 而非内置任务。
- **验收**：植入端二进制体积/功能面不随上述能力增长；agent 能一条指令完成「下载→校验→内存加载→执行→回传→清理」闭环。

## P3 — 平台 / 工程

- **会话多通道一致性**：MQTT/relay/HTTP-polling 与 TCP 的判活、忙期、任务重放语义对齐（当前 TCP 最完整）。
- **Release/打包**：`v1.3.1` 已支持本地 `scripts/package_release.ps1` 镜像 CI；后续做产物校验（zip 内 toserver `-version`）与 checksum 文件。
- **配置热更新边界**：心跳超时等会话参数改动后对存量会话生效的文档化。
- **前端**：会话详情/任务列表虚拟滚动（大量任务不卡）；Sessions/仪表盘 WS 事件统一订阅组件（减少重复实现）。

---

## 记录方法

- 每完成一条：移入 `CHANGELOG.md` 对应版本章节，并在此文件中将该条目标记 ✅。
- 新问题（尤其用户实测反馈）优先补进 P0，写明复现现象与代码位置。
