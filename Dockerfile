# =====================================================================
#  ToShell Team Server 容器镜像
#
#  构建：
#      docker build -t toshell-server .
#  运行：
#      docker run -d --name toshell \
#          -p 18081:18081 -p 8080:8080 \
#          -v toshell-data:/app/data -v toshell-cache:/app/.cache \
#          toshell-server
#      控制台 http://<host>:18081
#
#  关于镜像体积 —— 这是本镜像最重要的取舍，先读这段：
#    服务端在**运行时现场编译植入端**（builder 调 go build，并按 go.mod 里的声明
#    切换 GOTOOLCHAIN=go1.20.14 以兼容 Win7）。因此运行镜像必须自带 Go 工具链，
#    它占镜像体积的绝大部分。
#    另外首次生成载荷会按需下载 go1.20.14 工具链与依赖模块，需要能访问模块代理；
#    把这些缓存放在 /app/.cache 并挂成卷，可避免容器重建后重复下载（离线环境可在
#    构建阶段预热，见 build 阶段的预热注释）。
# =====================================================================

# ── 1. 前端：产物供服务端 //go:embed 嵌入 ──────────────────────────────
FROM node:20-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ── 2. 服务端：交叉编译出静态二进制 ────────────────────────────────────
FROM golang:1.25-bookworm AS server
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# webdist 是 -tags webui 的嵌入目标，必须先就位再编译
COPY --from=web /src/web/dist ./cmd/server/webdist
RUN CGO_ENABLED=0 go build -tags webui \
        -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
        -o /out/toserver ./cmd/server
# 如需离线可用的运行镜像，取消下面一行的注释：构建阶段就把载荷工具链拉下来，
# 随镜像一起分发（代价是镜像再大 ~100MB）。
# RUN GOTOOLCHAIN=go1.20.14 go version

# ── 3. 运行镜像 ────────────────────────────────────────────────────────
FROM debian:bookworm-slim

# ca-certificates 供 HTTPS；git 供 GOPROXY 取模块与工具链；curl 供 HEALTHCHECK
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git curl \
 && rm -rf /var/lib/apt/lists/*

# 运行时编译载荷需要 go 命令本身
COPY --from=server /usr/local/go /usr/local/go

ENV PATH="/usr/local/go/bin:${PATH}"
# GOTOOLCHAIN=auto：让工具链按模板 go.mod 的声明自动切换（模板要求 go1.20）
ENV GOTOOLCHAIN=auto
# 工具链与模块缓存集中到 /app/.cache，便于挂卷复用
ENV GOPATH=/app/.cache/go
ENV GOMODCACHE=/app/.cache/go/pkg/mod

WORKDIR /app

COPY --from=server /out/toserver ./toserver
# 植入端模板：服务端在 exe 同目录找 implant/，故与二进制同级。
# 取的是唯一源目录，不是 release/ 下的生成物。
COPY --from=server /src/internal/server/builder/implant   ./implant
COPY --from=server /src/internal/server/builder/implant_c ./implant_c
# UPX 随镜像分发（服务端按 upx/<os>-<arch>/ 探测；未安装时不影响运行）
COPY --from=server /src/release/upx ./upx
COPY --from=server /src/configs/server.yaml.example ./configs/server.yaml.example
COPY --from=server /src/data/av_fingerprints.json   ./data/av_fingerprints.json
COPY --from=server /src/docs ./docs
COPY --from=server /src/USAGE.md /src/LICENSE /src/DISCLAIMER.md /src/THIRD-PARTY-NOTICES.md ./

# data/ 存 SQLite 库与上传/传输文件；.cache/ 存 Go 工具链与模块缓存
VOLUME ["/app/data", "/app/.cache"]

# 配置缺失时从样例自举。服务端本身对缺项有默认值，故 cp 失败也不阻断启动
# （例如 /app/configs 以只读方式挂载的场景）。
CMD ["sh", "-c", "[ -f /app/configs/server.yaml ] || cp /app/configs/server.yaml.example /app/configs/server.yaml 2>/dev/null || true; exec /app/toserver -config /app/configs/server.yaml"]

# 18081 = 管理 API / Web 控制台（server.api_port）；8080 = 默认 TCP 监听器端口
EXPOSE 18081 8080

# /api/v1/health 无需鉴权，适合做容器健康检查
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -fsS http://127.0.0.1:18081/api/v1/health || exit 1
