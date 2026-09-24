# ToShell Team Server

> 自托管的 C2（命令与控制）远程管理平台，用于**授权红队演练、渗透测试与安全研究**。
> **仅限获得授权后使用。** 严禁未授权的入侵 / 攻击 / 数据窃取。

**v1.3.5** · [MIT License](LICENSE) · [![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE) · 上游作者：青山 / Q1lintu / c0ffee · 联系：[qingshan@88.com](mailto:qingshan@88.com)

> **本仓库是 [iQingshan/Toshell](https://github.com/iQingshan/Toshell) 的 fork**（fork 点 `54ca806`），
> 独立演进。上游关系、本仓库相对上游的结构差异、以及如何按需取用上游修复，
> 见 [docs/FORK.md](docs/FORK.md)。
>
> **问题反馈请提到本仓库的 Issue**，不要提到上游 —— 本仓库的构建与目录结构与上游已有差异，
> 上游无法据此定位。
>
> 本仓库自建的产物可从两处获得：GitHub Release 的 `toshell-server-<os>-<arch>**-yyx1000**.zip`
> （后缀标识来源仓库），或 `ghcr.io/yyx1000/toshell-server` 容器镜像（见「快速开始」）。
> 自建版本号形如 `1.3.5-dev.yyx1`，可用 `toserver -version` 与上游的 `1.3.5` 区分。

---

## 这是什么

ToShell 是一个轻量 C2 框架，由 **服务端（Team Server）+ Web 控制台 + 多平台植入端** 组成，覆盖「生成载荷 → 会话管理 → 任务执行」的完整链路。单二进制即可部署整套服务，开箱即用。

## 文档导航

| 文档 | 什么时候看 |
|---|---|
| [USAGE.md](USAGE.md) | **主手册**：安装部署、配置项逐条说明、生成载荷、会话操作、签名与加载器链、常见问题排查 |
| [docs/EVASION.md](docs/EVASION.md) | **免杀到底做了什么**：三类能力（落地 / 动态免杀 / 静态降特征）的实现位置、验证状态与自查方法 |
| [docs/LOADERS.md](docs/LOADERS.md) | **不落地未签名 PE 怎么上线**：白加黑、计划任务、rundll32/mshta/certutil、内存加载链的前置条件与取舍 |
| [docs/DEPLOY-DOMAIN-CDN.md](docs/DEPLOY-DOMAIN-CDN.md) | 用**域名 + CDN / Nginx 反代 / 域前置**上线时的配置与排错 |
| [CHANGELOG.md](CHANGELOG.md) | 每个版本改了什么 |
| [ROADMAP.md](ROADMAP.md) | 已做 / 待做的路线与优先级 |
| [SECURITY.md](SECURITY.md) · [DISCLAIMER.md](DISCLAIMER.md) | 漏洞披露流程 · 授权与合规边界 |
| [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md) | 发布包内捆绑组件（UPX 等）与 Go 依赖的许可清单 |
| [docs/skills/toshell-api/SKILL.md](docs/skills/toshell-api/SKILL.md) | 想用**脚本 / Agent 直接调 API**（不点界面）时的接口参考 |
| [docs/FORK.md](docs/FORK.md) | **本仓库与上游的关系**：fork 点、分叉原因、结构差异、如何取用上游修复 |
| [AGENT.md](AGENT.md) | **参与开发前先读**：开发红线、工作流、提交前门禁、注释规范、已知故障模式 |

> 遇到问题先看 [USAGE.md](USAGE.md) 的「六、常见问题」章节；提 Issue 时附上**版本号、系统环境、复现步骤与服务端日志**，否则很难定位。
> 版本号用 `toserver -version` 取（本仓库自建版本形如 `1.3.5-dev.yyx1`）。

## 核心特性

**多通道 · 多平台植入端**
- 回连通道：**TCP / HTTP(S) / WebSocket / MQTT**（可配合域前置、TLS 拟态）。
- 植入端：**Windows / Linux / macOS**，格式 `exe / dll / raw / shellcode（hex 文本）/ shellcode_bin（原始字节）`，支持 `full / light` 档案与老系统兼容（Go 1.20 工具链）。

**全功能会话操作**
- 交互 Shell、目录/文件管理（上传/下载/删除/预览，断点续传）、进程枚举/注入/杀死。
- 截图、实时屏幕流、凭据收集、UAC 提权、持久化、文件无执行、BOF/DLL/EXE 插件。

**组网与隧道**
- Beacon Mesh 多跳中继、SOCKS5 隧道代理（横向访问内网）、中继链路加密。

**任务流 + AI 副驾驶**
- 任务流/剧本化执行（可编辑模板，跑完自动 AI 复盘）。
- **AI 副驾驶**：联网搜索、远程下载工具、按用途插件/内存加载、结果分析与下一步建议；支持**权限审批**（影响会话的操作需确认）。
- **自主 Agent**：异步自主执行（不阻塞对话）、SSE 流式思考可见、连续上下文记忆、自主提权/横向闭环、失败自动恢复、只输出结论与建议。

**免杀与隐蔽**（本项目把三类东西分开说，避免误解）
- **落地（delivery）—— 对付"未签名 PE 被拒绝执行"**：构建后 **Authenticode 代码签名**
  （pfx / 证书存储指纹，签完立即复核并把签名者与状态回传界面）；**8 条加载器链**
  （白加黑 DLL 侧加载 / 计划任务 + 已签名宿主 / rundll32·mshta·certutil / 内存加载 shellcode）
  并按"是否已签名"给出降级顺序；`dll` 格式产出的是**真 DLL**（c-shared，加载即启动、导出名可配）。
  详见 [docs/LOADERS.md](docs/LOADERS.md)。
- **动态免杀 —— 对付运行时内存扫描与行为引擎**：**休眠期内存加密（sleep mask）**
  （空闲窗口对隧道子密钥与任务结果缓存做 XOR 加密，休眠走 `NtDelayExecution` 分片 + 随机抖动）、
  **去 RWX**（内存一律 RW 写 → RX 执行）、apihash/PEB 手工解析（不进 IAT 明文）、
  直接系统调用（注入路径）。**如实说明**：Go 植入端**做不到**加密整个镜像/代码段
  （runtime 时刻在跑），C2 地址这类 string 也暂不在掩码范围（可能位于只读段）——这些限制写在
  `sleepmask_windows.go` 顶部与 CHANGELOG 里。
- **静态降特征 —— 只影响文件特征，不影响"能不能跑"**：编译期字符串混淆（仓库原有）、
  pclntab 高信号标识符中性化、**BOF 按需编译**（默认载荷不含 `Beacon*`）、
  **Go 构建期指纹擦除**（buildinfo 魔数 / 构建 ID）、每构建随机化（配置块魔数/密钥/API 哈希种子）、
  `evasion_scan` 默认关（不再枚举进程找杀软）、garble / UPX 可选、`light` 裁剪。
  ⚠️ 这一列**不会**让载荷在装有 360/电脑管家的主机上"跑起来"。

**Web 控制台**
- 统一的深/浅色主题与组件层（卡片/分组/徽标/提示条/空态/骨架屏），键盘焦点可见；侧栏折叠记忆、窄屏浮层。
- 生成载荷页把 30 多个选项收进可折叠分组，一键上线命令支持**分组筛选 / 搜索 / 复制全部**并标注**风险等级**，结果面板直接给出**载荷 ID（一键复制）**、**签名结论**与**落地建议**；加载器链命令可用「填入载荷 ID」把占位符替换成真实 ID。
- 顶栏状态是**真实探测**服务端 `/api/v1/health`（20s 一次 + 窗口聚焦时触发），不是写死的"在线"；会话详情把不常用的标签收进「更多」下拉，避免标签栏挤压。

**加密通信**
- 控制帧 AES-256-GCM 认证加密 + 隧道数据 SM4-GCM（国密自研），密钥域分离；配置热更新。

## 功能部分截图

**仪表盘**：会话/任务概览、图表与实时状态。

<img src="docs/screenshots/dashboard.png" width="2557" alt="仪表盘">

**会话管理**：多平台会话、任务下发与状态跟踪。

<img src="docs/screenshots/sessions.png" width="2555" alt="会话管理">

**交互式shell**：动态交互式shell独立标签页面。

<img src="docs/screenshots/shell.png" width="100%" alt="会话管理">

**AI 副驾驶**：联网分析、工具调用与权限审批。

<img src="docs/screenshots/copilot.png" width="100%" alt="AI 副驾驶">

**文件管理**：上传 / 下载 / 在线预览，断点续传。

<img src="docs/screenshots/files.png" width="100%" alt="文件管理">

**内存执行**：免落地加载 DLL / EXE / BOF 与内存操作。

<img src="docs/screenshots/memexec.png" width="100%" alt="内存执行">

**杀软识别**：检测目标环境杀软 / 安全监控进程，辅助规避。

<img src="docs/screenshots/avidentify.png" width="100%" alt="杀软识别">

## 使用前准备

### 环境依赖

下面按**用途**列出需要安装的工具。只有对应功能用到时才需要，基础运行只装 **Go** 即可。

| 用途 | 依赖 | 说明 |
|---|---|---|
| **构建 / 运行服务端** | [Go](https://go.dev/dl/) **≥ 1.25**（go.mod 为 `go 1.25.0`） | 必需。服务端 + 植入端都用它编译。 |
| **源码构建时嵌入 Web 界面** | [Node.js](https://nodejs.org/) **≥ 20** + npm | 仅当从源码构建并带前端时用 `-tags webui`。发行版自带前端，可跳过。 |
| **构建 C 植入端（体积极小）** | [mingw-w64](https://www.msys2.org/) gcc（`x86_64-w64-mingw32-gcc`） | 仅生成 C 植入端时需要。普通 Go 植入端不依赖。 |
| **字符串混淆（可选）** | [garble](https://github.com/burrowers/garble) | 可选。未装则候选载荷自动禁用混淆。 |
| **UPX 压缩（可选）** | UPX | 发行版 `upx/` 目录已内置，自动探测。未装则候选载荷不启用压缩。 |

> **Go 工具链**：服务端用当前 Go 版本编译；植入端模板锁定 go1.20 工具链，构建时自动下载对应版本，无需手动安装。

### 安装步骤

**Windows**
```bash
# 1) 安装 Go（下载安装包或 winget）
winget install GoLang.Go

# 2) （可选，C 植入端）安装 MSYS2，并在其中安装 mingw-w64 工具链
#    MSYS2 安装后执行: pacman -S mingw-w64-x86_64-gcc

# 3) （可选，字符串混淆）
go install mvdan.cc/garble@latest
```

**Linux / macOS**
```bash
# Debian/Ubuntu / macOS (Homebrew)
sudo apt install -y golang-go                    # Linux
# brew install go                                 # macOS

# （可选，C 植入端）mingw-w64
sudo apt install -y gcc-mingw-w64-x86-64         # Linux
# brew install mingw-w64                          # macOS

# （可选，字符串混淆）
go install mvdan.cc/garble@latest
```

> 确认安装成功：`go version` 应输出 ≥ 1.25；`go env GOPROXY` 可设国内镜像加速依赖下载（如 `https://goproxy.cn,direct`）。

## 快速开始

### 一键部署（推荐）

```bash
cd release && chmod +x deploy.sh && ./deploy.sh   # Linux / macOS
# Windows: 在 release 目录双击 deploy.bat
```

> 首次运行会基于 `server.yaml.example` 自动生成配置，请先修改敏感项。完整部署说明见 [USAGE.md](USAGE.md)。

> **想用域名 + CDN 上线？** 见 [docs/DEPLOY-DOMAIN-CDN.md](docs/DEPLOY-DOMAIN-CDN.md)：CDN 回源、Nginx 反代、域前置（Domain Fronting）三种方式与排错 FAQ。

### 容器（ghcr.io）

```bash
docker run -d --name toshell \
  -p 18081:18081 -p 8080:8080 \
  -v toshell-data:/app/data -v toshell-cache:/app/.cache \
  ghcr.io/yyx1000/toshell-server:latest
```

控制台 `http://<host>:18081`。镜像同时提供 `linux/amd64` 与 `linux/arm64`。

> 若拉取提示未授权，说明该包被设为私有，先登录再拉：
> `echo <你的 PAT> | docker login ghcr.io -u YYX1000 --password-stdin`
> （PAT 需要 `read:packages` 权限）。包的可见性可在仓库的 Packages 页面调整。

> **镜像约 400MB，这不是可以优化掉的体积。** 服务端在**运行时现场编译植入端**（按模板
> `go.mod` 切换 `GOTOOLCHAIN=go1.20.14` 以兼容 Win7），因此运行镜像必须自带 Go 工具链 ——
> 多阶段构建消不掉。首次生成载荷还会按需下载 go1.20.14 与依赖模块，需要能访问模块代理；
> `-v toshell-cache:/app/.cache` 保存的正是这部分缓存，挂上可避免容器重建后重复下载
> （要完全离线，可在构建镜像时取消 Dockerfile 里预热那行的注释）。

### 默认启动方式

服务端是一个**单一可执行文件**，无需安装任何服务，直接运行即可：

```bash
# Linux / macOS
./toserver -config configs/server.yaml

# Windows
toserver.exe -config configs\server.yaml
```

启动后浏览器打开 **http://<服务器IP>:18081**（`server.api_port`，默认 18081）进入 Web 控制台。

> 首次启动会自动创建 SQLite 数据库 `./data/toshell.db`；未指定 `-config` 时默认加载 `./configs/server.yaml`。

### 从源码构建

本仓库的开发流程封装在 `Toshell.*` 中（Windows 用 `.bat`，Linux/macOS 用 `.sh`）。
两种启动方式：

```bash
./Toshell.sh start            # 部署方式：带内嵌前端的单进程，开 http://<host>:18081
./Toshell.sh start --dev      # 开发方式：纯 API 后端 + Vite 热更新（改 web/src 即时生效）
./Toshell.sh stop             # 两种都停
./Toshell.sh status           # 看当前状态
./Toshell.sh build [--dev]    # 只构建（--dev 构建不带内嵌前端的版本）
./Toshell.sh sync             # 只改了植入端模板时，免于完整构建
```

`--dev` 下界面走 Vite 的端口（默认 3002，`/api` 代理到后端），后端不嵌入前端 ——
这样开发时**只有一个界面**（热更新那个），不会出现"改了前端但 18081 上还是旧界面"。
两种方式的二进制不同，`start` 发现不匹配时会提示重建。

**产物落在 `release/` 而不是仓库根，这是有意的**：服务端按
【`implant.template_dir` → 环境变量 `TOSHELL_IMPLANT_TEMPLATE_DIR` → exe 同目录 `implant/`
→ exe 同目录 `internal/server/builder/implant` → 当前工作目录同路径】回退解析植入端模板。
把 exe 放在 `release/` 下，exe 同目录就有 `implant/`，从而让**本地开发与发布包走完全相同的
模板解析路径**，不会出现「本地能跑、发布包失效」。

要手工构建（不用上述脚本）：

```bash
# 1) 前端 → webdist（-tags webui 的嵌入目标）
npm --prefix web ci && npm --prefix web run build
rm -rf cmd/server/webdist && mkdir -p cmd/server/webdist && cp -r web/dist/. cmd/server/webdist/

# 2) 服务端
go build -tags webui -ldflags "-s -w" -o release/toserver ./cmd/server
```

> 只构建后端、不带 Web 控制台：跳过第 1 步并去掉 `-tags webui`。
> 打包 6 平台发布包用 `go run ./cmd/devtool package --all`；提交前的不变量检查用
> `go run ./cmd/devtool check`。
> **参与开发前请先读 [AGENT.md](AGENT.md)** —— 里面写了开发红线（编码约束、模板单一源、
> 部署脚本不可改等）、提交前必跑的 4 条门禁与已知故障模式。

## 生成植入端

登录 Web 控制台 →「生成载荷」→ 选平台 / 通道 / 免杀配置 → 构建 → 目标机运行即回连。

### 构建档位与免杀边界（v1.3.5）

| 选项 | 默认 | 说明 |
|---|---|---|
| 构建档案 `profile` | `full` | `full` = 全功能；`light` = 裁剪截图/屏幕流/中继/BOF/凭据/持久化/EDR/BYOVD/UAC/注入/插件（体积小、特征少） |
| **代码签名 (Authenticode)** | 关（需先在服务端配证书） | **v1.3.5 新增**：构建后签名。未签名的新 PE 在装有 360/电脑管家的主机上会被拒绝执行 —— 这是"能不能跑起来"的敲门砖；配 `builder.sign_pfx_path`(+密码) 或 `builder.sign_thumbprint` |
| **BOF 支持** | **关** | **v1.3.5 起按需编译**：开启会带上整套 Cobalt Strike `Beacon*` API 名字（实测 22 处明文），只有确实要跑 BOF 才勾 |
| 启动随机延迟 | 服务端配置（默认 2~10s） | 载荷启动后随机休眠 [最小,最大] 秒再首次回连；**生成载荷页留空即跟随服务端**（填了才覆盖。注意 0 = "未设置"，仍回退配置值，不是"不延迟"） |
| 回连节奏：心跳间隔 / 抖动 / 重试 | 服务端配置（`implant.interval` / `jitter` / `retry_wait`） | **生成载荷页这些输入框默认留空**，留空 = 跟随「设置 → 植入端默认参数」，placeholder 直接显示服务端当前生效值；只有确实要覆盖时才填。未配置时回退 60s / ±20% / 5s |
| 主动反沙箱进程检测 | **关** | 开启（`evasion_scan`）会枚举全系统进程并与一批杀软/分析工具进程名比对后延迟执行 —— **这正是 360/火绒/电脑管家主动防御拦截的对抗行为**，只在明确需要时开 |
| Garble 混淆 / UPX | 关 | 未安装对应工具时界面上如实显示"不可用 + 原因"。注意：**签名后再 UPX/改资源会让签名失效** |

> **默认载荷里已经没有这些高信号明文**（实测）：`beacon*`（BOF 按需编译）、`loadShellcode`/`memexe_windows.go` 等（pclntab 中性化）、`\xff Go buildinf:` 与 `Go build ID:`（构建期指纹擦除）、杀软进程名（字符串混淆 + 默认不枚举进程）。

> **如何确认参数真的生效**：服务端日志有 `rendering implant: … interval=… jitter=… startup_delay=… evasion_scan=… profile=…`、`compiling implant: … tags="…"`、`代码签名成功（…）` 与 `go fingerprint scrubbed: …`，这是唯一可信的"真正烘焙进载荷的值"。

> ⚠️ **必须知道的边界**：在装有 360/电脑管家等国产安全软件的主机上，**未签名的 PE 会在进程创建阶段被直接拒绝执行并删除文件**（实测：连一个只有 `time.Sleep` 的 Hello-World Go 程序也被拒，而微软签名的 `notepad.exe` 副本可正常执行）。这属于"签名/信誉/策略"拦截，**改载荷代码无用**。v1.3.5 给出的解法是：
> 1. **签名**：配好证书后勾选「代码签名」重新构建（自签名证书还需导入目标机「受信任的根证书颁发机构」，否则状态是"已签名但链不受信任"，仍可能被拦）；
> 2. **不落地未签名 PE**：用生成载荷页给出的 **8 条加载器链**（白加黑 DLL 侧加载 / 计划任务 + 已签名宿主 / rundll32·mshta·certutil / 内存加载 shellcode），详见 **[docs/LOADERS.md](docs/LOADERS.md)**；
> 3. 构建响应会按"是否已签名 + 平台/格式"给出 `loader_advice_title/tips`，写明**降级顺序**：直接运行 → 计划任务 → 白加黑 → 内存加载。

## 配置

复制 `configs/server.yaml.example` → 修改 `public_host / api_keys / jwt_key / encryption_key / admin_password`；更多字段说明见 [USAGE.md](USAGE.md)。设置页可热更新多数配置（无需重启）。

## 发版前自检

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File scripts/e2e_smoke.ps1
```

起临时服务端 → 校验鉴权/关键接口 → 构建 windows full / light / linux 三档载荷 → （可选）真植入端上线并下发一条任务 → 输出 ✅/⚠️/❌ 摘要，**有 ❌ 即非 0 退出**，可直接进 CI 作为发版门禁。

日常提交前的门禁另有三条（`go vet ./...`、`go test ./...`、`go run ./cmd/devtool check`），
完整清单与各自的用途见 [AGENT.md](AGENT.md) 的「3.4 提交前门禁」。

## 联系方式 / Contact

| 渠道 / Channel | 地址 / Link |
| :--- | :--- |
| 上游作者 / Upstream author | 青山（iQingshan） · [@iQingshan](https://github.com/iQingshan) · [qingshan@88.com](mailto:qingshan@88.com) |
| **本仓库 / This fork** | [YYX1000/Toshell](https://github.com/YYX1000/Toshell) |
| **Issue / 功能建议** | [**在本仓库提交 Issue**](https://github.com/YYX1000/Toshell/issues) |
| 安全 / 滥用报告 | [SECURITY.md](SECURITY.md)（**不要**在公开 Issue 里贴可利用细节，可走邮件） |

> 使用问题、配置排查、功能建议：优先提到**本仓库**的 Issue（附 `toserver -version` 的输出、
> 系统环境、复现步骤与相关日志）—— 本仓库的构建方式与目录结构已与上游有差异，提到上游
> 无法据此定位。若问题在 v1.3.5 原版就存在、与本仓库改动无关，也可同时提给上游。
> 商务合作 / 授权咨询 / 上游漏洞披露：邮件联系上游作者。

> *Bug reports and feature requests: please open an issue **on this fork** with the output of
> `toserver -version`, environment and reproduction steps.*

## 开源与授权

- 使用说明：[USAGE.md](USAGE.md)
- **参与开发**：[AGENT.md](AGENT.md) —— 开发红线、开发流程、提交前必跑的 4 条门禁、注释规范与已知故障模式
- **与上游的关系**：[docs/FORK.md](docs/FORK.md) —— fork 点、分叉原因、本仓库相对上游的结构差异、如何按需取用上游修复
- 后续优化路线：[ROADMAP.md](ROADMAP.md)（驱动能力分档与加载前自检、内存执行加固、屏幕流跨平台、动态查杀收敛、平台工具库与远程加载）
- 安全披露：[SECURITY.md](SECURITY.md)
- 一键部署：[release/install.ps1](release/install.ps1) / [release/install.sh](release/install.sh)（发布包内自带，环境检测 + 按需在线安装 + 直接启动）
- License：**[MIT](LICENSE)**（Copyright © 2026 iQingshan 与 ToShell 贡献者）
- 使用声明：**[DISCLAIMER.md](DISCLAIMER.md)** —— 仅限**授权**安全测试/红队演练/自建实验环境，禁止任何未授权用途；使用者须自行确保授权充分并承担全部责任。
- 第三方组件声明：**[THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md)** —— 发布包内捆绑的 **UPX（GPL-2.0-or-later + 特殊例外）**以及 Go 模块依赖的许可清单；发布包与二进制**不含任何内核驱动**（BYOVD 驱动由使用者自备并自负合规责任），第三方组件**不受 MIT 覆盖**，各自遵循原许可。
- **免责声明**：仅用于授权测试与学习研究，禁止任何未授权的入侵、攻击或数据窃取行为；使用者后果自负。

---

**© 2026 ToShell · MIT License** · 仅供授权测试与学习
