# 上游关系与 fork 维护说明

> 面向**维护者**。本文说明本仓库与上游 ToShell 的关系、为什么要分叉、以及如何按需取用上游的修复。

---

## 1. 上游与 fork 点

| 项 | 值 |
|---|---|
| 上游仓库 | <https://github.com/iQingshan/Toshell> |
| 本仓库 | <https://github.com/YYX1000/Toshell> |
| **fork 点** | `54ca80661b3e8986b85e58c360aa4706d435e23d`（`54ca806`，2026-09-15） |
| 分叉时的上游 HEAD 提交 | `feat(tunnel): 并发隧道上限 100 -> 300；ROADMAP 增加服务端在线更新` |
| 许可 | MIT（Copyright © 2026 iQingshan 与 ToShell 贡献者）—— 分叉与修改均在 MIT 许可范围内 |

`54ca806` 之后本仓库独立演进。查询当前分叉了哪些提交：

```bash
git log --oneline upstream/main..HEAD        # 本仓库独有
git log --oneline HEAD..upstream/main        # 上游独有（可以捡的）
```

---

## 2. 为什么分叉

上游结构里有几处**与本仓库的部署假设冲突**的地方，改它们会与上游持续产生合并冲突，因此选择脱离：

1. **植入端模板双镜像**（`internal/server/builder/implant/` ↔ `release/implant/`）靠人工同步，且发版门禁校验的是其中不发货的那一份（详见 `docs/EVASION.md` 与 CHANGELOG 中的相关记录）。本仓库已改为**单一源 + 构建期生成**。
2. **死代码**：`go list -deps ./cmd/server`（唯一构建入口）显示 47 个包中 **24 个不可达**（43 个文件 / 12,437 行，占 Go 代码约 26%），全部来自 v1.2.0 开源导入。本仓库已清理，清理后包数 **47 → 23**、依赖闭包差集为 0，`go vet ./...` 首次可全量运行。
3. **构建管线三处重复**（`Toshell.*` / `release.yml` / `package_release.ps1`，跨三种语言），本仓库已收敛为 `cmd/devtool`。

---

## 3. 保留 upstream 作为只读参考

`upstream` remote **不删除**，但禁止误推：

```bash
git remote set-url --push upstream no_push
```

**为什么保留**：C2 框架的免杀能力是有保质期的——加载器链、代码签名、内存对抗、AV 指纹库都需要跟着防守方的节奏持续更新，而这些正是上游持续投入的方向。脱离结构不等于放弃情报，保留 fetch 能力的成本接近零。

### 取用上游修复

```bash
git fetch upstream
git log --oneline HEAD..upstream/main            # 看有什么新东西
git cherry-pick <commit>                          # 挑单个修复
git show upstream/main:<path>                     # 只看某个文件的上游版本
```

**注意**：本仓库已删除 `internal/implant/`、`internal/common/tunnel/{channel,implant,manager,optimized,protocol,proxy,quic,security}/`、`cmd/{cli,socks5proxy,implant}/` 等包。上游若在这些路径上有改动，cherry-pick 会冲突——这类提交**按需人工移植**，不要机械挑选。

---

## 4. 分叉后的结构差异（与上游对照）

| 位置 | 上游 | 本仓库 |
|---|---|---|
| `release/implant/`、`release/implant_c/` | 入库的模板镜像（61 + 1 文件） | **已删除**；构建/打包时从 `internal/server/builder/implant{,_c}/` 生成，已 gitignore |
| `internal/implant/`、`cmd/implant/` | 存在（第二套植入端实现） | **已删除**（死代码） |
| `internal/common/tunnel/` 8 个子包 | 存在（弃用的隧道框架） | **已删除**（死代码）；保留的活跃实现是 `internal/common/tunnel/tunnel.go` |
| `cmd/cli/`、`internal/operator/`、`cmd/socks5proxy/` | 存在 | **已删除**（零引用） |
| `internal/common/encoding/`、`internal/server/shellcode/`、`pkg/` | 存在 | **已删除**（零引用） |
| 构建/打包入口 | `Toshell.*`(三语言) + `release.yml` + `package_release.ps1` | 收敛为 `cmd/devtool`；`Toshell.*` 为 shim |
| 发版门禁 | `scripts/e2e_smoke.ps1` | 保留，且现在校验的是**真正发货**的模板 |
| 部署侧 `release/install.{sh,ps1}`、`release/deploy.{sh,bat}` | — | **未改动**（对外接口保持一致） |

### 唯一源约定

`internal/server/builder/implant/`（Go 模板）与 `internal/server/builder/implant_c/`（C 模板）是**唯一可信源**，二者必须保持**同级**——`builder.go` 与 `toolchain.go` 以 `../implant_c` 推导 C 模板位置。

`release/implant/` 与 `release/implant_c/` 是**生成物**，不要手工编辑（会被下次同步覆盖）。校验同步是否最新：

```bash
go run ./cmd/devtool check
```
