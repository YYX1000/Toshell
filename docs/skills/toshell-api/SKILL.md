---
name: toshell-api
description: Remote-control a ToShell C2 team server through its REST API — authenticate (API key or JWT), discover sessions, run commands and built-in recon through the atomic /mcp/tools executor, fetch files/screenshots/credentials, drive async Agent runs and playbooks via SSE, and react to live WebSocket events. Use this skill whenever the user asks an AI to query or operate a ToShell server over HTTP.
---

# ToShell C2 — 远程 REST API 调用（AI Skill）

ToShell（服务端 Web 端口默认 `8080`、API 端口默认 `18081`）提供一套 REST + WebSocket 接口。本 Skill 教你**以一个外部 AI 的身份**安全、正确地远程调用它。所有端点、请求体、响应体均以本仓库源码为准（`internal/server/api` 路由清单见 `api.go`；前端调用契约见 `web/src/api/index.ts`）。

> ⚠️ **授权纪律**：ToShell 是 C2 平台。只能操作**用户明确授权**的服务器与目标会话。涉及命令执行、凭据收集、进程注入、隧道、插件/内存加载等影响会话的操作，先确认用户意图；服务器处于「正常权限模式（consent_mode=normal）」时这些操作需先走审批端点。

---

## 1. 连接与认证

- 基础路径：`{scheme}://{host}:{api_port}/api/v1`（如 `http://127.0.0.1:18081/api/v1`）。
- 健康检查（**免认证**）：`GET /api/v1/health` → `{"status":"ok",...}`。
- 认证两种方式，二选一：
  - **API Key**：请求头 `X-API-Key: <key>`（推荐给脚本/AI）。
  - **JWT**：`Authorization: Bearer <token>`。JWT 通过 `POST /api/v1/login`（`Content-Type: application/x-www-form-urlencoded`，字段 `username`、`password`）获取 → `{"token":"..."}`。
- 除 `health`、`login` 与个别免认证下载端点外，所有 `/api/v1/*` 都需要以上任一凭据；否则 `401 Unauthorized`。
- 响应统一 JSON。错误形如 `{"error":"..."}`，HTTP 状态码表达语义（400 参数、401 未认证、404 不存在）。

**先做**：`GET /health` 确认可达 → 带凭据 `GET /sessions` 确认授权与在线会话。

---

## 2. 首选执行通道：统一原子工具端点（推荐）

`POST /api/v1/mcp/tools/{tool}` —— 本平台为 AI 归一化的工具面，**一次调用即返回最终结果**（服务端自动创建→推送→等待→回传），不需要你自行拼 task_id 轮询。参数为扁平 JSON 字符串对象 `{"session_id":"...", "command":"..."}`。

- 发现可用工具：`GET /api/v1/mcp/tools`（返回 `tools[]` 名称/描述）。
- 常用工具（已原子化，见 `handlers_mcp.go` / `copilot.go` toolSchemas）：
  - `session_list`、`session_context{session_id}`
  - `exec{session_id, command 或 kind, timeout_sec?}` —— 命令执行首选；`kind` 可省为内置语义命令（user_info/system_info/service_list/check_av/net_info/net_connections/env_vars/scheduled_tasks）
  - `file_list{session_id,path}`、`file_download{session_id,path}`
  - `process_list{session_id}`、`process_kill{session_id,pid}`
  - `screenshot{session_id}`、`credentials{session_id,action?}`（action: all/browser/wifi/rdp/lsa）
  - `plugin_list`、`plugin_load{session_id,plugin_id,args?}`、`tunnel_start{session_id,local_port?}`、`tunnel_list`、`tunnel_stop{session_id}`
  - `intel_query{kind?}`（跨会话情报库）、`web_search{query}`、`remote_download{url}`
- 结果形状（exec/读类）：`{"session_id","task_id","status":"completed|failed|timeout","output":"...","exit_code":0,"error":""}`。`status=completed` 且 `exit_code==0` 即成功；`failed`/`timeout` 读 `error` 与 `output`。
- **不存在** `task_wait`/`task_submit`（已从 AI 工具面移除以杜绝 task_id 编造）；不要试图猜测任务号。

```bash
# 示例：对会话执行命令（Windows 目标）
curl -sk -X POST http://127.0.0.1:18081/api/v1/mcp/tools/exec \
  -H "X-API-Key: $KEY" -H "Content-Type: application/json" \
  -d '{"session_id":"5041edce2618d318","command":"whoami /groups"}'
```

---

## 3. 原始 REST 端点（Web 控制台同源；部分为 task 异步式）

> 能用第 2 节原子工具完成的操作优先用工具面；以下端点用于完整覆盖或与前端行为对齐。

### 会话
| 方法/路径 | 说明 |
| --- | --- |
| `GET /sessions` | 会话列表：`{"sessions":[{id,hostname,username,os,arch,pid,status,listener,first_seen,last_seen,...}],"count":N}`；status: `active` 在线 / `asleep` / `dead` |
| `GET /sessions/{id}` | 单会话详情 |
| `PATCH /sessions/{id}` `{"comment":"..."}` | 更新备注 |
| `DELETE /sessions/{id}` | 删除（向植入端下发 exit）|
| `GET /sessions/{id}/capabilities` | 能力探测 |

### 任务（异步式：返回 task_id → `GET /tasks/{id}` 轮询终态）
| 端点 | 触发动作 |
| --- | --- |
| `POST /sessions/{id}/interact` `{"command":"...","task_type":"command"}` | 下发命令（返回 task_id）|
| `GET /tasks?session_id={id}` / `GET /tasks/{task_id}` | 查询任务状态/output/error/exit_code |
| `POST /tasks/{id}/cancel`、`DELETE /tasks/{id}` | 取消 / 删除 |
| `POST /sessions/{id}/files/...`、`POST /sessions/{id}/processes` 等 | 文件/进程操作（也返回 task）|
| `POST /sessions/{id}/credentials` `{"action":"all"}` | 凭据收集 |
| `POST /sessions/{id}/screenshot`（空 body） | 截屏 → 结果经 `GET /tasks/{task_id}` 取（`output` 含 base64）|
| `POST /sessions/{id}/screen-stream` `{"action":"start"|"stop"}` | 屏幕流启停 |
| `POST /sessions/{id}/plugin`、`/sessions/{id}/injection`、`/fileless-exec`、`/privesc-uac` 等 | 高级动作 |

任务轮询约定：`status ∈ {completed, failed, timeout}` 即终态；task_id 必须来自响应，**不要自造**。

### 副驾驶 / Agent（异步、流式）
| 方法/路径 | 说明 |
| --- | --- |
| `GET /copilot/status` | `{"enabled","model","consent_mode"}` |
| `POST /copilot/chat` `{"messages":[{role,content}]}` | 单轮（同步，带工具闭环），`{"reply","traces","pending_consents?"}` |
| `POST /copilot/consent` `{"token","decision":"allow|deny"}` | 处理 normal 模式审批 |
| `POST /agent/chat` `{"messages":[...], "session_id"?}` | **创建/续接异步 agent run** → `{"run_id","session_id","status"}`（非阻塞）|
| `GET /agent/runs/{id}` | `{"status","objective","plan","traces","timeline","reply"}` |
| `GET /agent/runs/{id}/events` | SSE：`thinking/message/tool_start/tool_result/final/done` |
| `POST /agent/runs/{id}/cancel`、`POST /agent/runs/{id}/consent` `{"decision"}` | 取消 / 审批 |

Agent 工作流（推荐给 AI 使用）：
1. `POST /agent/chat` 传消息与可选的 `session_id`（续接同一自主记忆），拿 `run_id`。
2. `GET /agent/runs/{id}` 轮询（1–2s）直到 `status ∈ {done,error,awaiting_consent}`；或用 `GET /agent/runs/{id}/events` 订阅 SSE。
3. 终态读 `reply`（最终答复）与 `timeline`（工具执行过程，可直接引用给用户）。
4. 极短消息（≤2 字，如「1」「好」）走纯聊通道，返回一两句，不要当执行指令。
5. 若 `status=awaiting_consent` 且 `consent_mode=normal`：把待确认操作展示给用户，经 `/agent/runs/{id}/consent` 后继续轮询。

### 剧本（确定性多步执行）
| 方法/路径 | 说明 |
| --- | --- |
| `GET /copilot/playbooks` | 内置 + 自定义模板合一列表 |
| `POST /copilot/playbook/run` `{"playbook_id","session_id"}` | 启动 → `{"run_id","status"}` |
| `GET /copilot/playbook/runs` / `GET /copilot/playbook/runs/{id}` | 运行列表 / 单次（`results[]`、`analysis`）|

AI 建议模板：任务完成 → 先给简短「执行结果摘要」，AI 深度分析可能晚到（服务端异步生成）——若暂时无 `analysis`，**不要谎称失败**，可先给基于 `results` 的摘要并说明分析生成中，稍后重查该 run。

### 情报 / 其它
- `GET /intel`：跨会话情报库条目。
- `GET /system/stats`、`GET /channels/health`、`GET /logs`、`GET /tunnels`、`GET /plugins`、`GET /listeners`、`GET /templates`。
- `GET /mcp/tools` 仍是发现 AI 工具面的最权威入口。

---

## 4. 实时事件（WebSocket）

`GET /api/v1/ws/events?token=<JWT>`（或 `Authorization: Bearer`）——推送类型：
`session_online / session_offline`（上线/下线/复活，全监听器统一广播）、`task_completed / task_failed`、`screen_frame`（实时屏幕帧）。适合做仪表盘联动、会话状态即时感知；无需轮询会话列表。

---

## 5. 完整 AI 操作工作流（推荐顺序）

1. **确认目标与授权**：问清/确认 server 地址、凭据与用户要做的动作（侦察？命令？凭据？）。
2. `GET /health` → 带凭据 `GET /sessions`：列出在线会话（`status=active`）。
3. 需要上下文：`POST /mcp/tools/session_context`（原子）或 `GET /sessions/{id}`。
4. 按意图分派：
   - **只问建议/思路**：`GET /copilot/status` → 直接给出结构化建议，不自动执行链。
   - **执行命令/内置侦察**：`POST /mcp/tools/exec`（一次一命令，看 output）。
   - **信息收集**：按固定清单逐项 exec（user_info/system_info/net_info/process_list/check_av…），拿到即收敛，最后汇总成结构化报告；不要对同一命令重复调用。
   - **自主长任务**：`POST /agent/chat` + 轮询/SSE。
   - **确定链路**：`POST /copilot/playbook/run` + `GET /copilot/playbook/runs/{id}`。
5. **汇报**：给结论与关键输出，不要倾倒原始大 JSON；截图等 base64 只报「已获取/大小/用途」，大内容提示可另存。

---

## 6. 错误处理与守则

- `401`：凭据缺失/过期 → 重新 login 或要求正确 API Key。
- `404 {"error":"session not found"}`：会话 ID 错或已删除 → 重新 `GET /sessions` 用活跃 ID。
- `{"status":"timeout"}` / `exit_code:-1`：任务超时可能仍在跑；不要立刻重发同一命令，先说明。
- 判断会话在线**只看** `session.status` / `session_context` 返回，禁止臆测「会话掉线」。
- 高危（kill/删除/注入/凭据/隧道/插件加载）：先征得用户同意；normal 模式下走审批端点。
- 命令输出可能很大：展示时截断；不要把任务原始输出无脑全文粘贴。
- 不暴露 API Key / token 于日志与对话正文以外的位置；示例用 `$KEY` 占位。
- 诚实：工具失败、分析未生成、会话离线都要如实说明，并给可执行的替代（换会话/换工具/重试说明）。

---

## 7. 快速参考（curl 模板）

```bash
BASE=http://127.0.0.1:18081/api/v1
KEY=your-api-key
AUTH=(-H "X-API-Key: $KEY" -H "Content-Type: application/json")

# 健康 & 会话
curl -sk $BASE/health
curl -sk "${AUTH[@]}" $BASE/sessions

# 原子执行（推荐）
curl -sk -X POST "${AUTH[@]}" $BASE/mcp/tools/exec \
  -d '{"session_id":"<SID>","command":"whoami"}'

# 异步 Agent
RUN=$(curl -sk -X POST "${AUTH[@]}" $BASE/agent/chat \
  -d '{"messages":[{"role":"user","content":"对会话 <SID> 做信息收集并输出报告"}]}')
echo "$RUN"                      # {"run_id":...}
curl -sk "${AUTH[@]}" $BASE/agent/runs/<RUN_ID>      # 轮询到 done，读 reply/timeline

# 剧本
curl -sk -X POST "${AUTH[@]}" $BASE/copilot/playbook/run \
  -d '{"playbook_id":"custom-<ID>","session_id":"<SID>"}'
curl -sk "${AUTH[@]}" $BASE/copilot/playbook/runs/<RUN_ID>
```

> 版本契约：本文面向 v1.3.x（`go run ./cmd/server -version` 可查）。端点以当前运行服务 `GET /api/v1/mcp/tools` 与源码为准；如与本文不符，以运行服务为准并回写本文。
