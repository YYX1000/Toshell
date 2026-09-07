# ToShell 后续优化路线（ROADMAP）

> 记录 v1.3.1 之后待优化的方向。每条标注现状（代码事实）、问题/根因、目标与建议方案，按优先级排序。
> 更新日志见 `CHANGELOG.md`；本文只聚焦「下一步要做什么、为什么」。

---

## P0 — 用户体验/稳定性（近期优先）

### 1. 会话「掉线一会又自动恢复在线」抖动

- **现状**：会话状态依赖心跳判活。服务端 `session.HeartbeatTimeout`（配置 `listener.heartbeat_timeout`，默认 **60s**）由 10s 周期 checker 检查；植入端心跳间隔 `implant.interval=60s` + jitter（默认 ±2s），空闲时可能 ~62s 才来一个心跳。
- **根因方向**：
  - 心跳间隔与超时阈值**几乎零余量**：任何一次心跳延迟（调度抖动、网络排队、系统休眠唤醒）都会超过 60s → 被判 dead/asleep → 触发 `session_offline` 广播；下一次心跳到达又恢复 → `session_online`。前端表现为「离线几秒又在线」。
  - 断线重连场景：TCP 闪断重连会先触发 onSessionDead（广播 offline），重连注册又广播 online，形成肉眼可见的抖动。
- **目标**：会话状态在正常心跳节奏下**稳定在 online**；只有真实长时间失联才转 dead。
- **建议方案**：
  - 判活阈值留余量：`heartbeat_timeout` 默认提到心跳间隔的 **2.5~3 倍**（如 60s 心跳 → 180s 超时），或用 `max(3×interval, 60s)` 计算；心跳抖动只在掉线判定里吃 jitter 上限。
  - checker 从「每 10s 遍历全表」改为**按 LastSeen+timeout 精确到期判活**，避免批量扫描的窗口误差。
  - 广播去抖：offline 事件广播前做**短观察窗（如 2~3 次重试机会）**，或重连命中「复活」时抑制一次多余的 offline/online 闪烁（前端侧加状态去抖）。
  - 前端 Sessions 页状态迁移加过渡态（如 recently-seen 置灰而非直接「离线」），减少观感抖动。
- **验收**：植入端正常运行 30+ 分钟，Sessions 页状态不闪断；手动 kill 植入端后 ≤timeout+1 心跳周期内转 dead，重启植入端恢复 online。

### 2. 屏幕流 + 截图优化（当前仅 Win11 支持较好）

- **现状**（代码事实）：
  - 截图仅 **Windows** 实现（`internal/server/builder/implant/screenshot_windows.go`），采用 GDI `BitBlt` 抓屏 + `PrintWindow(PW_RENDERFULLCONTENT)` 应用窗口回退，含虚拟屏/多显示器（`SM_*VIRTUALSCREEN`）、DPI 感知、锁屏/无交互桌面回退（`OpenInputDesktop/SetThreadDesktop`/`CreateDC`）等逻辑。
  - 屏幕流（`screen_stream_windows.go`）复用截图模块，固定 ~**1.25fps（800ms）**轮询，把 `TypeScreenFrame` 帧回传服务端 → WebSocket 推送前端。
  - Linux/macOS：`screen_stream_unix.go` 与 `features_unix.go` 均为 **stub**（返回 "only supported on Windows"）。
- **问题**：Win 各版本/Server/服务会话/锁屏下表现不一致（用户实测目前 Win11 支持较好）；跨平台（Linux/macOS）完全缺失；帧率固定、带宽不可控。
- **建议方案（分阶段）**：
  - **P0.1 Windows 兼容加固**：按 Win7/8.1/10/11 + Server Core/服务会话 建矩阵实测；统一「BitBlt → PrintWindow → Desktop Duplication (Win8+)」三级捕获策略；处理高 DPI（每显示器 DPI aware）、多显示器拼接、锁屏/无桌面明确返回原因帧而非空转。
  - **P0.2 屏幕流引擎升级**：Win8+ 换 **Desktop Duplication API（DXGI）** 做增量帧捕获（性能与帧率大幅提升）；保持 JPEG 质量自适应 + 带宽上限（KB/s 可配），前端已有大图/低帧降级，服务端补限速与帧合并。
  - **P0.3 跨平台**：Linux 用 **X11 (XGetImage)/Wayland (PipeWire)**，macOS 用 **ScreenCaptureKit** 或 CGDisplayStream（需要时再评估权限）；screenshot 同步补全。
  - **验收**：Win10/11 桌面会话 ≥5fps 且 CPU 占用可控；Server 无桌面/锁屏返回明确错误；至少 Linux amd64 能出图。
- **备注**：截图/屏幕流涉及大量 native API，改动集中在植入端；服务端与前端（`BroadcastScreenFrame` / 前端屏幕流组件）基本可复用。

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
