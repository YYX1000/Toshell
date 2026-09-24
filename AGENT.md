# AGENT.md

本仓库的开发约束，适用于所有协作者。**必须** / **禁止** 为强制条目。

---

## 1. 项目

### 1.1 性质

ToShell 是自托管 C2 框架（服务端 + Web 控制台 + 多平台植入端），仅限授权的红队演练、
渗透测试与安全研究。禁止移除 [DISCLAIMER.md](DISCLAIMER.md) 的授权约束与免责声明。

本仓库是 `iQingshan/Toshell` 的 fork，fork 点 `54ca806`，独立演进，许可 MIT，`upstream`
remote 只读。分叉原因与上游修复的取用见 [docs/FORK.md](docs/FORK.md)。

### 1.2 产物链

三条链的工具链版本要求不同，是构建类问题的常见来源。

| 产物 | 源码 | 工具链 |
|---|---|---|
| 服务端 | `cmd/server`、`internal/server/**`（16 子包）、`internal/common/**` | Go 1.25 |
| Web 控制台 | `web/`，构建后复制至 `cmd/server/webdist/` 并由服务端 `//go:embed` 嵌入 | Node ≥ 20 |
| 植入端模板 | `internal/server/builder/implant{,_c}/`，不预编译，服务端生成载荷时现场编译 | 独立 module，Go 1.20 |

植入端是**独立 module**，无法 import 主模块任何包；`internal/common/` 仅由服务端使用。

### 1.3 release/ 与启动路径

| `release/` 下的内容 | 性质 | 入库 |
|---|---|---|
| `deploy.*`、`install.*`、`upx/`、`plugins/`、`configs/*.example`、`README.md` | 打包素材 | 是 |
| `implant/`、`implant_c/` | 构建期生成物 | 否 |
| `toserver.exe`、`server.log`、`data/`、`implants/` | 本地运行残留 | 否 |

服务端解析植入端模板的顺序：`implant.template_dir` → 环境变量 `TOSHELL_IMPLANT_TEMPLATE_DIR`
→ exe 同目录 `implant/` → exe 同目录 `internal/server/builder/implant` → 当前工作目录同路径。
将服务端二进制置于 `release/`，exe 同目录即命中 `implant/`，使本地开发与发布包使用相同的
解析路径。禁止更改该布局。

**`release/implant`（单数）是模板生成物；`release/implants`（复数）是 `implant.output_dir`
指定的载荷产物目录。两者无关。**

> 其余目录（`web/`、`scripts/`、`docs/`、`configs/`、`Toshell.{sh,ps1,bat}`）职责见名知义；
> `scripts/e2e_smoke.ps1` 是发版门禁。

---

## 2. 红线

### R1 禁止手工编辑生成物

`release/implant{,_c}/` 由 `devtool sync` 从 `internal/server/builder/implant{,_c}/` 生成，
会被后续同步覆盖。模板的唯一可编辑位置是 `internal/server/builder/implant{,_c}/`。
修改模板后必须 `go run ./cmd/devtool sync`，否则服务端读取的仍是旧生成物。

### R2 禁止变更植入端模板的 Go 版本与依赖

`internal/server/builder/implant/go.mod` 声明 `go 1.20`，用于兼容 Windows 7 / Server 2008 R2。
对该文件禁止 `go mod tidy`、禁止升级依赖、禁止跟随主模块提升 Go 版本。

### R3 禁止引入需人工同步的重复副本

同一内容存在两份以上且需人工保持一致，视为架构缺陷。已有的三处均已处置：植入端模板改为
单源生成；构建与打包逻辑收敛为 `cmd/devtool`；两份 `server.yaml.example` 保留但由
`devtool check` 校验。

新增设计必须优先单源化；确实无法单源化时，必须同步添加 `devtool check` 校验项。

### R4 编码与行尾

| 文件 | 编码 | 行尾 |
|---|---|---|
| `*.sh` | UTF-8 | LF（CRLF 会产生 `bad interpreter: ...^M`） |
| `*.ps1` | UTF-8 with BOM | CRLF（PS 5.1 无 BOM 时按 ANSI 解析，中文乱码或语法错误） |
| `*.bat` | GBK | CRLF（cmd 按代码页 936 输出中文） |

修改后必须校验字节，不得依赖编辑工具默认行为：`head -c 3 文件 | od -An -tx1`。
`.ps1` 首字节必须为 `ef bb bf`；`.bat` 不得含 BOM。

### R5 禁止提交含真实密钥的配置

`configs/server.yaml`、`release/configs/server.yaml`、`release/USAGE.md` 含真实密钥或口令，
已在 `.gitignore` 中。新增示例配置仅修改 `*.example`。

### R6 禁止变更部署侧的对外接口

`release/install.{sh,ps1}`、`release/deploy.{sh,bat}` 是已发布的包内入口，名称与参数已被用户
依赖。`install` 的职责是在未安装 Go 的机器上安装 Go，必须为纯 shell / PowerShell，
禁止改为调用 Go 工具。`deploy.*` 是 `install.*` 的别名。

### R7 禁止使门禁失效

发版门禁 `scripts/e2e_smoke.ps1` 必须指向模板唯一源；打包必须经由 `cmd/devtool package`。
两者由 `devtool check` 校验。禁止为使检查通过而放宽检查；如需修改，须先确认其原始用途。

---

## 3. 工作流

**开发循环**（服务端二进制、日志与数据均在 `release/`；Windows 用 `.bat`）：

```
./Toshell.sh start            # 部署方式：带内嵌前端的单进程
./Toshell.sh start --dev      # 开发方式：纯 API 后端 + Vite 热更新（界面走 Vite 端口）
./Toshell.sh stop             # 两种都停（服务端 + Vite）
./Toshell.sh status           # 构建产物、服务端、Vite 的当前状态
./Toshell.sh build [--dev]    # 只构建；--dev 构建不带内嵌前端的版本
./Toshell.sh sync             # 仅同步植入端模板，跳过完整构建
```

两种方式是**两个独立实例**，运行时目录不同，因此配置、SQLite 库、载荷输出目录都
不共享，且 API 端口默认相同 —— 不能同时跑：

| | 运行时目录 | 配置 | 启动方式 | 模板来源 |
|---|---|---|---|---|
| `deploy` | `release/` | `release/configs/server.yaml` | 构建出的二进制 | `release/implant/`（构建期同步） |
| `dev` | 仓库根 | `configs/server.yaml` | `go run ./cmd/server` | `internal/server/builder/implant/`（源码，改完即生效） |

`dev` 不构建产物，所以改 Go 代码只需重启、改前端即时热更新、改模板连 `sync` 都不用。

**前端**：开发态 `cd web && npm run dev`（监听 3002，将 `/api` 代理至 `server.api_port`，
默认 18081；用 `VITE_PROXY_TARGET` 覆盖目标）；生产态 `go run ./cmd/devtool build` 后访问 `:18081`。

**打包**（本地与 CI 同一实现，禁止在 `release.yml` 内重新实现）：

```
go run ./cmd/devtool package --all                  # 6 平台 zip + checksums.txt
go run ./cmd/devtool package --target linux/amd64   # 单平台
```

**提交前门禁**，四条必须全部通过：

```
go build -tags webui -o release/toserver ./cmd/server
go vet ./...
go test ./...
go run ./cmd/devtool check
```

涉及前端或打包的改动，须额外执行 `devtool package --target <本机平台>` 验证产物。

---

## 4. 约定

### 4.1 注释

注释必须描述代码的**既定语义与契约**，且与代码保持一致。

- **应当写**：该函数/类型解决什么问题；参数与返回值含义；前置条件；边界与不变量；
  失败时的行为；非直觉设计的取舍理由。
- **禁止写**：改动记录与排查笔记（"修了 xx bug"、"以前这里有坑"、"临时这么处理"）——
  变更原因属于提交信息，不属于代码。
- **禁止写**：与代码不符的注释。修改代码必须同步修改注释；陈旧注释比没有注释更危险。
- **禁止写**：复述代码表面行为的注释（`i++ // i 加一`）。

```
✗  // 修复：以前这里按 cap 读会越界
✗  // 如果 err != nil 就返回 err
✓  // decodeFrame 解析一帧并返回其载荷。返回的切片由调用方持有，可安全跨
   // goroutine 使用。帧头声明长度超过 maxRead 时返回 ErrFrameTooLarge。
```

### 4.2 其余

**提交信息**：Conventional Commits，中文标题；正文含背景、改动、验证三部分，验证须给出
实际执行的命令与结果。

**测试**：新增包必须纳入 `go test ./...`（CI 执行全量）。不变量检查加入
`cmd/devtool/check.go`，不以一次性脚本实现。

**文档**：正式文档置于 `docs/` 或仓库根；一次性排查记录须归档并标注。

---

## 5. 已知故障模式

| 现象 | 根因 | 处置 |
|---|---|---|
| 修改 `vite.config.ts` 后不生效 | 同目录存在 `vite.config.js`（`tsc -b` 产物）时，Vite 优先加载 `.js` | 已由 `tsconfig.node.json` 的 `emitDeclarationOnly` 禁止；`devtool check` 兜底 |
| 载荷在其它主机上不上线 | 监听器 `public_addr` 为空或为 `127.0.0.1` 时，后端 `applyListenerDefaults` 与前端 `Builds.tsx` 均回退为 `localhost` | 构建前设置监听器公网地址；构建后核对日志 `rendering implant: url=…` |
| 配置端口后无法连接 | 服务端不监听 `server.port`，管理 API 仅监听 `server.api_port`（默认 18081） | 端口配置以 `api_port` 为准 |
| 修改模板后载荷无变化 | 源与生成物未同步 | 执行 `devtool sync`；`devtool check` 会报告不一致 |
| 载荷在装有 360 / 电脑管家的主机上被拒绝执行 | 未签名 PE 的签名与信誉策略拦截，与载荷代码无关 | 使用代码签名或加载器链，见 [docs/LOADERS.md](docs/LOADERS.md) |
| 本地启动时找不到模板 | 未从 `release/` 启动，exe 同目录无 `implant/` | 使用 `Toshell.* start` |

**排错依据**：服务端日志的
`rendering implant: url=… interval=… jitter=… startup_delay=… evasion_scan=… profile=…`
是唯一可信的载荷实际参数来源；上游请求、配置文件与页面输入均可能覆盖该值。

---

## 6. 协作者准则

1. 涉及构建、打包、模板的改动，必须先以命令验证现状，不得依据目录名或注释推断。
2. 修改 `release/` 下文件前，先确认该项是源还是生成物。
3. `git commit` 提交全部已暂存内容；提交前用 `git diff --cached --name-only` 核对边界。
4. 报告结果时区分「已验证」与「仅静态检查」；编译通过不等同于运行验证。
5. 删除性改动（文件、目录、依赖、门禁）前必须确认无引用。
6. 多文件重构、目录调整、门禁增删，须先提交方案与影响面并获确认。

---

## 7. 参考

[docs/FORK.md](docs/FORK.md)（上游关系与结构差异）· [docs/EVASION.md](docs/EVASION.md)
（免杀能力的实现位置与验证状态）· [docs/LOADERS.md](docs/LOADERS.md)（加载器链）·
[scripts/README.md](scripts/README.md)（发版门禁与已知环境事实）· [USAGE.md](USAGE.md)（主手册）·
[ROADMAP.md](ROADMAP.md) · [CHANGELOG.md](CHANGELOG.md)
