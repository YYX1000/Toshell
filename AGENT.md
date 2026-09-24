# AGENT.md

本文件规定本仓库的开发约束，适用于所有协作者。**必须** / **禁止** 为强制条目。

---

## 1. 项目

### 1.1 性质

ToShell 是自托管 C2 框架，由服务端、Web 控制台、多平台植入端组成。仅限授权的红队演练、
渗透测试与安全研究。禁止移除 [DISCLAIMER.md](DISCLAIMER.md) 的授权约束与免责声明。

本仓库是 `iQingshan/Toshell` 的 fork，fork 点 `54ca806`，独立演进。`upstream` remote
仅作只读参考（push 已禁用）。分叉原因及上游修复的取用方式见 [docs/FORK.md](docs/FORK.md)。
许可为 MIT；包内捆绑的 UPX 等第三方组件不适用 MIT，见
[THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md)。

### 1.2 产物链

三条链的工具链版本互不兼容，是多数构建类问题的来源。

| 产物 | 源码 | 构建 | 工具链 |
|---|---|---|---|
| 服务端 | `cmd/server`、`internal/server/**` | `go build -tags webui ./cmd/server` | Go 1.25 |
| Web 控制台 | `web/` | `npm ci && npm run build`，产物复制至 `cmd/server/webdist/`，由服务端 `//go:embed` 嵌入 | Node ≥ 20 |
| 植入端模板 | `internal/server/builder/implant{,_c}/` | 不预编译，服务端生成载荷时现场编译 | 独立 module，Go 1.20 |

### 1.3 目录

| 路径 | 职责 |
|---|---|
| `cmd/server/` | 服务端入口。CI 仅构建此入口 |
| `cmd/devtool/` | 构建、打包、不变量校验 |
| `internal/server/` | 服务端业务：api、builder、listener、session、ai、drivers 等 |
| `internal/common/` | 服务端与植入端共享的协议、加密、隧道 |
| `web/` | 前端源码（React + Vite） |
| `scripts/` | 发版期脚本；`e2e_smoke.ps1` 为发版门禁 |
| `release/` | 打包素材与本地运行目录，见 1.4 |
| `docs/` | 正式文档 |
| `configs/` | 配置模板。`server.yaml.example` 入库，`server.yaml` 不入库 |
| `Toshell.{sh,ps1,bat}` | 开发管理脚本（build / clean / start / stop / config / sync） |

### 1.4 release/

| 内容 | 性质 | 入库 |
|---|---|---|
| `deploy.*`、`install.*`、`upx/`、`plugins/`、`configs/*.example`、`README.md` | 打包素材 | 是 |
| `implant/`、`implant_c/` | 构建期生成物 | 否 |
| `toserver.exe`、`server.log`、`data/`、`implants/` | 本地运行残留 | 否 |

服务端解析植入端模板的顺序：`implant.template_dir` → 环境变量 `TOSHELL_IMPLANT_TEMPLATE_DIR`
→ exe 同目录 `implant/` → exe 同目录 `internal/server/builder/implant` → 当前工作目录同路径。
将服务端二进制置于 `release/`，可使 exe 同目录直接命中 `implant/`，令本地开发与发布包
使用相同的模板解析路径。禁止更改该布局。

**`release/implant`（单数）为模板生成物；`release/implants`（复数）为 `implant.output_dir`
指定的载荷产物目录。两者无关。**

---

## 2. 红线

### R1 禁止手工编辑生成物

`release/implant{,_c}/` 由 `devtool sync` 从 `internal/server/builder/implant{,_c}/` 生成，
后续同步会覆盖。模板的唯一可编辑位置是 `internal/server/builder/implant{,_c}/`。
修改模板后必须同步，否则服务端读取的仍是旧生成物：

```
go run ./cmd/devtool sync
```

### R2 禁止变更植入端模板的 Go 版本与依赖

`internal/server/builder/implant/go.mod` 声明 `go 1.20`，用于兼容 Windows 7 / Server 2008 R2。
对该文件禁止执行 `go mod tidy`、禁止升级依赖、禁止跟随主模块提升 Go 版本。

植入端是独立 module，无法 import 主模块包。禁止将主模块代码复制进模板。

### R3 禁止引入需人工同步的重复副本

同一内容存在两份以上且需人工保持一致，视为架构缺陷。本项目已有的三处处置：

| 副本 | 处置 |
|---|---|
| 植入端模板（源 ↔ `release/`） | 单源生成 |
| `configs/server.yaml.example` ↔ `release/configs/server.yaml.example` | 保留，由 `devtool check` 校验 |
| 构建与打包逻辑（3 份实现） | 收敛为 `cmd/devtool` |

新增设计必须优先单源化。确实无法单源化时，必须同步添加 `devtool check` 校验项。

### R4 编码与行尾

| 文件 | 编码 | 行尾 |
|---|---|---|
| `*.sh` | UTF-8 | LF（CRLF 会产生 `bad interpreter: ...^M`） |
| `*.ps1` | UTF-8 with BOM | CRLF（PS 5.1 无 BOM 时按 ANSI 解析，中文乱码或语法错误） |
| `*.bat` | GBK | CRLF（cmd 按代码页 936 输出中文） |
| 其余 | UTF-8 | 不限 |

修改上述文件后必须校验字节，不得依赖编辑工具默认行为：

```
head -c 3 文件 | od -An -tx1
```

`.ps1` 首字节必须为 `ef bb bf`；`.bat` 不得含 BOM。

### R5 禁止提交含真实密钥的配置

`configs/server.yaml`、`release/configs/server.yaml`、`release/USAGE.md` 含真实密钥或口令，
已在 `.gitignore` 中。新增示例配置仅修改 `*.example`。

### R6 禁止变更部署侧的对外接口

`release/install.{sh,ps1}`、`release/deploy.{sh,bat}` 是已发布的包内入口，其名称与参数已
被用户依赖。`install` 的职责是在未安装 Go 的机器上安装 Go，必须为纯 shell / PowerShell，
禁止改为调用 Go 工具。`deploy.*` 是 `install.*` 的别名。

### R7 禁止使门禁失效

- 发版门禁 `scripts/e2e_smoke.ps1` 必须指向模板唯一源 `internal/server/builder/implant{,_c}`。
- 打包必须经由 `cmd/devtool package`。

上述两条由 `devtool check` 校验。禁止为使检查通过而放宽检查；如需修改，须先确认其原始用途。

---

## 3. 工作流

### 3.1 开发循环

```
./Toshell.sh build      # Windows: Toshell.bat build
./Toshell.sh start
./Toshell.sh stop
./Toshell.sh sync       # 仅同步植入端模板，跳过完整构建
```

服务端二进制、日志与数据均位于 `release/`。

### 3.2 前端

| 模式 | 命令 | 用途 |
|---|---|---|
| 开发态 | `cd web && npm run dev` | 修改 `web/src/**`，热更新 |
| 生产态 | `go run ./cmd/devtool build`，访问 `:18081` | 验证嵌入后的真实形态 |

Vite 开发服务器监听 3002，将 `/api` 代理至 `server.api_port`（默认 18081）。覆盖代理目标
使用环境变量 `VITE_PROXY_TARGET`。

### 3.3 打包

```
go run ./cmd/devtool package --all                  # 6 平台 zip + checksums.txt
go run ./cmd/devtool package --target linux/amd64   # 单平台
```

本地与 CI 使用同一实现。禁止在 `release.yml` 内重新实现打包逻辑。

### 3.4 提交前门禁

以下四条必须全部通过：

```
go build -tags webui -o release/toserver ./cmd/server
go vet ./...
go test ./...
go run ./cmd/devtool check
```

涉及前端或打包的改动，须额外执行 `devtool package --target <本机平台>` 验证产物。

---

## 4. 约定

**注释**：使用中文，说明设计原因（约束、非直觉取舍、历史事故），不复述代码行为。
对看似冗余但不可删除的代码，必须注明原因。

**提交信息**：Conventional Commits 格式，中文标题。正文包含背景、改动、验证三部分；
验证部分须给出实际执行的命令与结果。

**测试**：新增包必须纳入 `go test ./...`（CI 执行全量）。不变量检查应加入
`cmd/devtool/check.go`，不以一次性脚本形式实现。

**文档**：正式文档置于 `docs/` 或仓库根目录。一次性排查记录须归档并标注，不得混入正式文档。

---

## 5. 已知故障模式

| 现象 | 根因 | 处置 |
|---|---|---|
| 修改 `vite.config.ts` 后不生效 | 同目录存在 `vite.config.js`（`tsc -b` 产物）时，Vite 优先加载 `.js` | 已由 `tsconfig.node.json` 的 `emitDeclarationOnly` 禁止；`devtool check` 兜底 |
| 载荷在异机不上线 | 监听器 `public_addr` 为空或为 `127.0.0.1` 时，后端 `applyListenerDefaults` 与前端 `Builds.tsx` 均回退为 `localhost` | 构建前设置监听器公网地址；构建后核对日志 `rendering implant: url=…` |
| 配置端口后无法连接 | 服务端不监听 `server.port`，管理 API 仅监听 `server.api_port`（默认 18081） | 端口配置以 `api_port` 为准 |
| 修改模板后载荷无变化 | 源与生成物未同步 | 执行 `devtool sync`；`devtool check` 会报告不一致 |
| 载荷在装有 360 / 电脑管家的主机上被拒绝执行 | 未签名 PE 的签名与信誉策略拦截，与载荷代码无关 | 使用代码签名或加载器链，见 [docs/LOADERS.md](docs/LOADERS.md) |
| 本地启动时找不到模板 | 未从 `release/` 启动，exe 同目录无 `implant/` | 使用 `Toshell.* start` |

**排错依据**：服务端日志的
`rendering implant: url=… interval=… jitter=… startup_delay=… evasion_scan=… profile=…`
是唯一可信的载荷实际参数来源。上游请求、配置文件与页面输入均可能覆盖该值。

---

## 6. 协作者准则

1. 涉及构建、打包、模板的改动，必须先以命令验证现状，不得依据目录名或注释推断。
2. 修改 `release/` 下文件前，先确认该项是源还是生成物。
3. 修改编码敏感文件后，必须做字节级校验。
4. `git commit` 提交全部已暂存内容。提交前执行 `git diff --cached --name-only` 核对边界。
5. 多文件重构、目录调整、门禁增删，须先提交方案与影响面并获确认；局部修复可直接执行。
6. 报告结果时区分「已验证」与「仅静态检查」。编译通过不等同于运行验证。
7. 删除性改动（移除文件、目录、依赖、门禁）前，必须确认无引用。

---

## 7. 参考

| 文档 | 内容 |
|---|---|
| [docs/FORK.md](docs/FORK.md) | 上游关系、fork 点、上游修复取用方式、结构差异 |
| [docs/EVASION.md](docs/EVASION.md) | 免杀三类能力的实现位置与逐项验证状态 |
| [docs/LOADERS.md](docs/LOADERS.md) | 加载器链的前置条件与取舍 |
| [scripts/README.md](scripts/README.md) | 发版门禁参数、检查清单、已知环境事实 |
| [USAGE.md](USAGE.md) | 主手册 |
| [ROADMAP.md](ROADMAP.md)、[CHANGELOG.md](CHANGELOG.md) | 计划与版本变更 |
