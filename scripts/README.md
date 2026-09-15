# scripts/

发版与运维脚本目录。

## `e2e_smoke.ps1` — 端到端冒烟（发版门禁 P0-4）

一条命令跑完的端到端冒烟：**自动构建服务端 → 起临时服务端（随机端口/随机临时目录）→ 打接口 → 构建三档载荷并下载校验 → 校验关键路由存在性 → （可选）真实 Windows 载荷上线并下发任务**。
存在 ❌ 则 `exit 1`；幂等，可本地反复跑，也能进 CI。脚本不会改动仓库内任何文件。

```powershell
# 默认：随机端口 + 随机临时目录，含真实植入端上线环节（本机安全软件放行时）
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\e2e_smoke.ps1

# CI / 无植入端授权环境：只降级"真实植入端上线"环节，其它环节仍必须通过
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\e2e_smoke.ps1 -SkipImplant

# 排查：保留临时目录（服务端日志、构建产物、载荷都在里面）
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\e2e_smoke.ps1 -SkipImplant -KeepArtifacts
```

### 参数

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `-Port` | 随机（18000-19000 挑两个连续空闲端口） | 控制台/API 端口；C2 TCP 监听用 `Port+1` |
| `-WorkDir` | `$env:TEMP\toshell-e2e-<随机>` | 临时工作目录，存放 `toserver.exe`、`configs/server.yaml`、`data/`、`implants/`、日志 |
| `-SkipImplant` | 关 | 跳过"真实植入端上线 + 任务下发"（等价环境变量 `TOSHELL_E2E_SKIP_IMPLANT=1`），只降级该环节（⚠️） |
| `-KeepArtifacts` | 关 | 保留临时目录（默认删除；删除前会先停掉服务端与载荷进程） |
| `-ServerExe` | 空（自动构建） | 用已构建好的服务端，跳过"重新构建"环节（该环节记 ⚠️：未验证二进制与源码一致） |
| `-RequireImplant` | 关 | 要求植入端环节必须成功；安全软件拦截载荷时记 ❌ 并 `exit 1` |
| `-ApiKey` | `smoke-key` | 冒烟用 API Key（与脚本生成的最小配置一致） |

### 检查清单（每项 ✅/⚠️/❌）

1. 运行环境：Windows + `go` 可用
2. 临时工作目录创建
3. 端口选择（API 端口与 C2 监听端口都空闲）
4. **服务端构建**：仓库根目录执行 `go build -tags webui -ldflags "-s -w -X main.version=smoke" -o <工作目录>\toserver.exe ./cmd/server`（历史事故：改动后忘重编服务端）
5. 最小配置写入（字段名与 `internal/server/config/config.go` 一致）
6. **服务端健康检查**：轮询 `GET /api/v1/health`（带 `X-API-Key`）→ 200；401/403/404/5xx 分别给出中文原因
7. **鉴权生效**：不带 Key 访问 `GET /api/v1/sessions` → 401（200 直接判失败）
8. C2 TCP 监听端口可连接
9. **构建器能力接口**：`GET /api/v1/builders` → 200 且含 `formats`、`evasion.garble_available`
10. **三档载荷构建**：`windows/amd64 full`(exe)、`windows/amd64 light`(exe)、`linux/amd64 full`(bin) →
    每档校验 HTTP 200、`size > 100000`、`sha256` 为 64 位 hex，并按 `download_url` 下载（带 Key）后校验**文件大小与 sha256 与响应一致**
11. **路由存在性**：`GET /api/v1/drivers`(200)、`POST /api/v1/sessions/{id}/fileless-exec`、`POST /api/v1/sessions/{id}/screen-stream`
    （判定规则：404 = 路由缺失 → ❌；400/401/403/409 = 路由存在 → ✅；5xx → ❌）
12. 观察项（不阻断）：会话不存在时 `screen-stream` 的错误码（当前实现返回 500，见下）
13. 真实植入端上线：启动 windows/amd64 载荷，60s 内轮询 `GET /api/v1/sessions` 等新会话
14. 植入端任务下发：`POST /api/v1/sessions/{id}/interact` 下发 `whoami`，60s 内轮询 `GET /api/v1/tasks/{id}` 拿到非空输出
15. 清理：杀载荷进程 → 删会话 → 停服务端 → 确认端口释放 → 删除临时目录

### 已知环境事实与脚本行为

- **安全软件拦截新生成的未签名 PE**（360/电脑管家等，`Access is denied` 且文件被删）：脚本**不会假装成功也不会死循环**，
  会在输出里写"载荷无法在本机执行（安全软件拦截），已跳过植入端环节"，该环节记 ⚠️（只降级该环节），并计入摘要；
  加 `-RequireImplant` 则该情况记 ❌。
- **`server.port` 不被绑定**：服务端管理 API 实际监听 `server.api_port`（`cmd/server/main.go`）。脚本把两者写成同一个端口，
  避免"健康检查打在默认 8081 上"。另外配置是**按进程工作目录**（`viper.AddConfigPath(".")` / `./configs`）解析的，
  不是"可执行文件同目录"；脚本用 `Start-Process -WorkingDirectory <工作目录>` 让两者一致，并显式 `-config`。
- **植入端模板目录**：服务端工作目录在临时目录里找不到模板源码，脚本在最小配置里写死 `implant.template_dir`
  并额外设置 `TOSHELL_IMPLANT_TEMPLATE_DIR` 环境变量。
- **启动随机延迟无法关闭**：构建请求里的 `startup_delay_min/max = 0` 会被 `internal/server/builder/builder.go`
  当作"未指定"并回退到内置的 2~10s，因此等待窗口按 60s 设计。
- **首次构建 Windows 载荷需要 Go 1.20.14 工具链**（builder 用 `GOTOOLCHAIN=go1.20.14` 兼容 Win7），
  离线机器需先预热（见 CI 片段的 Prefetch 步骤）。
- 观察项：`POST /api/v1/sessions/nonexistent/screen-stream`（请求体合法）当前返回 **500**（`taskMgr.Create` 校验会话失败 →
  handler 直接 500），语义上应为 404；脚本把它记为 ⚠️ 观察项，不阻断发版。

### 文件编码要求

本仓库 `.ps1` 必须是 **UTF-8 with BOM + CRLF**（`.gitattributes` 已规定 CRLF；PS 5.1 读无 BOM 的中文会乱码并报语法错误）。
新增/改写 `.ps1` 后请检查首字节为 `EF BB BF`。脚本内不使用 `$args`。

## CI 片段

`release.yml` 已接入发版门禁（`.github/workflows/release.yml` 的 `e2e-smoke` job，`build` 的 `needs` 为 `[web, e2e-smoke]`，用 Windows PowerShell 5.1 跑，固定 `-SkipImplant`）。
第 1 段片段即仓库内 `release.yml` 的现状；第 2、3 段保留作为 `ci.yml` 与自托管 runner 的接入参考（`ci.yml` 目前**尚未**接入该 job）。

### 1) `release.yml`：tag 后的发版门禁（windows runner）

```yaml
  # ── 发版门禁：端到端冒烟（必须在打包发布前通过）─────────────────────────
  # 仓库内的 release.yml 已按本片段接入（含 build 的 needs: [web, e2e-smoke]）。
  # 托管 runner 上不跑真实植入端（未签名载荷会被托管杀软拦），故固定 -SkipImplant；
  # 真实"载荷上线 + 命令回显"闭环见第 3 段（自托管 runner）。
  e2e-smoke:
    runs-on: windows-latest
    needs: web
    timeout-minutes: 40
    steps:
      - uses: actions/checkout@v4

      - uses: actions/download-artifact@v4
        with:
          name: webdist
          path: cmd/server/webdist

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
          check-latest: true

      # 预热 Windows 载荷所需的 go1.20.14 工具链（builder 用 GOTOOLCHAIN 固定它）
      - name: Prefetch implant toolchain (Go 1.20.14)
        shell: powershell
        run: |
          $env:GOTOOLCHAIN = 'go1.20.14'
          go version

      - name: E2E smoke (server build + 3 payload flavors + auth + routes)
        # 用 Windows PowerShell 5.1 跑：脚本是按 5.1 语义编写与静态校验的
        shell: powershell
        run: powershell -NoProfile -ExecutionPolicy Bypass -File scripts\e2e_smoke.ps1 -SkipImplant -KeepArtifacts

      - name: Upload smoke artifacts
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: e2e-smoke-artifacts
          path: ${{ runner.temp }}\toshell-e2e-*
          if-no-files-found: ignore
          retention-days: 7
```

### 2) `ci.yml`：PR/push 上的快速冒烟（同样 windows runner）

```yaml
  # ── 端到端冒烟（PR 门禁；windows runner）───────────────────────────────
  e2e-smoke:
    runs-on: windows-latest
    timeout-minutes: 40
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
          check-latest: true

      - name: Prefetch implant toolchain
        shell: pwsh
        run: |
          $env:GOTOOLCHAIN = 'go1.20.14'
          go version

      - name: E2E smoke
        shell: pwsh
        run: pwsh -NoProfile -ExecutionPolicy Bypass -File scripts\e2e_smoke.ps1 -SkipImplant
```

### 3) 自托管 Windows runner：含真实植入端上线的完整版

```yaml
  e2e-smoke-full:
    runs-on: [self-hosted, windows]
    timeout-minutes: 40
    steps:
      - uses: actions/checkout@v4

      - name: E2E smoke (含真实植入端上线)
        shell: pwsh
        # 该机器必须放行新生成的载荷；拦截时用 -RequireImplant 让发版直接失败
        run: pwsh -NoProfile -ExecutionPolicy Bypass -File scripts\e2e_smoke.ps1 -RequireImplant -KeepArtifacts
```

> 注：托管 runner 上建议始终带 `-SkipImplant`（结果确定）；真实"载荷上线 + 命令回显"闭环请在
> 干净 VM 或自托管 runner 上跑，并配合 `-RequireImplant` 作为硬门禁。

## 其他脚本

- `package_release.ps1`：本地按 CI 逻辑打 6 个平台发布包。
- `smoke.sh`：轻量接口冒烟（面向已运行的服务端，不构建、不起进程）。
- `reset_release_db.py`：清理发布用数据库。

## 相关文档

- [README.md](../README.md) — 项目总览与开发说明
- [docs/EVASION.md](../docs/EVASION.md) — 落地 / 动态免杀 / 静态降特征三类能力与验证状态（冒烟脚本的验证思路与它一致）
- [CHANGELOG.md](../CHANGELOG.md) ｜ [ROADMAP.md](../ROADMAP.md) — 版本变更与后续计划
- [SECURITY.md](../SECURITY.md) — 支持版本与安全 / 滥用报告渠道
