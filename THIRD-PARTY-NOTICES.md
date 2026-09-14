# 第三方组件与许可声明 / Third-Party Notices

ToShell 本体以 [MIT License](LICENSE) 发布。**发布包（release zip）内还捆绑了以下第三方组件，
它们不受 MIT 许可覆盖，各自遵循原许可**；本文件用于集中声明，随包分发。

The ToShell project itself is licensed under the [MIT License](LICENSE).
The release packages additionally **bundle the third-party components below**;
they are **not** covered by the MIT license and remain under their own terms.

---

## 1. UPX（可执行文件压缩器）· GPL-2.0-or-later + 特殊例外

| 项 | 说明 |
|---|---|
| 位置 | `upx/win64/upx-5.2.0-win64/upx.exe`、`upx/linux-amd64/upx-5.2.0-amd64_linux/upx` |
| 许可 | GNU General Public License v2.0 or later（`COPYING`），另附「压缩产物不受 GPL 传染」的特殊例外（`LICENSE`） |
| 上游 | <https://upx.github.io/> · <https://github.com/upx/upx> |
| 用途 | 服务端调用它对生成好的 Windows 载荷做 `--best --lzma` 压缩（界面里的「UPX 压缩」选项） |

- 完整的 `COPYING`（GPL-2.0）与 `LICENSE`（特殊例外）**已随包放在各自目录内**，未做任何修改；
- UPX 仅作为**独立可执行文件**被调用（进程调用，非链接库），其压缩产物不受 GPL 约束；
- UPX 对应源码可从上游获取（<https://github.com/upx/upx/releases>）；如需我们随包提供源码副本，请开 issue。

## 2. 内置 BYOVD 驱动 `kgameprotect.sys`（仅 Windows，可选）

| 项 | 说明 |
|---|---|
| 位置 | `drivers/kgameprotect.sys`（同时以嵌入资源形式编译进 `toserver`） |
| 大小 / SHA-256 | 59,592 字节 / `6c1d596d18213e24f0c88d58ea7f3ca24114eded806b6198a8abc701251126ee` |
| 签名 | Authenticode **Valid** —— `CN=Microsoft Windows Hardware Compatibility Publisher`（WHQL 认证签名），PE32+ / AMD64 |
| 来源 | LOLDrivers 目录收录的公开样本（PR #428：*Add vulnerable kgameprotect.sys process-termination driver*） |
| 权属 | 该驱动为**第三方厂商**（游戏反作弊驱动）作品，版权归其各自权利人；本项目仅按公开样本原样分发，未做修改 |

⚠️ 该驱动存在已知设计缺陷（对外暴露一个**无鉴权**的进程终止 IOCTL `0x222048`），属于 BYOVD
（Bring Your Own Vulnerable Driver）类利用面。**只在你有授权的测试环境加载**，测完请立即在控制台
「杀软对抗」页点「卸载驱动」。若权利人要求移除，请开 issue，我们会从仓库与后续发布包中删除。

`RTCore64.sys` / `dbutil_2_3.sys` 等其它易受攻击驱动**自 v1.3.3 起不再捆绑**（曾被微软易受攻击驱动
黑名单与主流杀软重点标记）。如需使用请自行准备并通过控制台上传。

## 3. Go 模块依赖（MIT / BSD / Apache 等）

服务端与植入端由 Go 构建，依赖见 [`go.mod`](go.mod) / [`go.sum`](go.sum)；植入端模板另有
[`internal/server/builder/implant/go.mod`](internal/server/builder/implant/go.mod)。
主要依赖与其许可（均为宽松许可）：

| 模块 | 许可 |
|---|---|
| `github.com/gorilla/mux`、`github.com/gorilla/websocket` | BSD-3-Clause |
| `github.com/refraction-networking/utls`（HTTP 载荷 TLS 指纹） | BSD-3-Clause |
| `github.com/Binject/go-donut`（EXE→shellcode 转换） | MIT |
| `github.com/spf13/viper`、`cobra`、`fsnotify` | MIT |
| `github.com/eclipse/paho.mqtt.golang`、`mochi-mqtt/server` | EPL-2.0 / MIT |
| `github.com/quic-go/quic-go` | MIT |
| `golang.org/x/*`（crypto、net、sys 等） | BSD-3-Clause |

构建产物中包含这些依赖的编译结果；按各自许可要求保留版权声明。如需完整清单，可在仓库根执行：

```bash
go list -m all        # 全部模块及版本
go mod download -json # 含各自 LICENSE 文件路径
```

## 4. 数据文件

| 文件 | 说明 |
|---|---|
| `data/av_fingerprints.json` | 安全软件指纹库（进程/服务/驱动特征），由项目自行整理与公开资料汇整，随 MIT 许可发布 |
| `data/tools/`（如存在） | 远程加载用的工具目录。**第三方二进制不预置**；由操作员按需下载或自行放入，其许可与合规由操作员自行负责 |

## 5. 商标与名称

「ToShell」为本项目名称；文中出现的第三方产品名（Windows、UPX、MSYS2、飞书、钉钉、企业微信、
Slack、Discord 等）为其各自权利人的商标或注册商标，本项目与这些厂商无隶属或背书关系。

---

_如认为本文件遗漏了某个组件的声明，欢迎开 issue 指出，我们会尽快补充。_
