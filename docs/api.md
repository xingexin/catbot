# API 与配置

所有管理 API 位于 `/api`，登录外需要会话 cookie。JSON 出错返回 `{"error":"..."}`。

| 方法与路径 | 说明 |
|---|---|
| `POST /api/login` | `password` 登录 |
| `POST /api/logout` | 退出 |
| `GET /api/status` | 服务连接状态 |
| `GET/POST /api/secrets` | 凭证引用列表 / 加密保存 |
| `GET/POST /api/configs` | 执行配置 |
| `POST /api/configs/{id}/test` | API 连接检查；SDK 通过对话联调 |
| `GET/POST /api/personas` | 人格；复制或导入时不传 ID |
| `DELETE /api/personas/{id}` | 删除未被引用的人格 |
| `GET/POST /api/sessions` | 会话创建 / 修改绑定 |
| `POST /api/sessions/{id}/messages` | `message`、可选 `requestId` |
| `GET /api/runs/{id}/events` | SSE，支持 Last-Event-ID |
| `POST /api/runs/{id}/cancel` | 取消 |
| `POST /api/runs/{id}/retry` | 显式新运行；先检查此前副作用 |
| `GET /api/tools` | 工具与 Schema |
| `POST /api/plugins/register` | `directory`，相对 PLUGIN_DIR |
| `POST /api/plugins/{id}/configure` | `config`、`grants` |
| `POST /api/plugins/{id}/enable` | `enabled` |
| `POST /api/plugins/{id}/health` | 启动和 MCP Ping |
| `GET /api/plugins/{id}/logs` | 有界日志尾部、已知凭证脱敏 |
| `GET/POST /api/tasks` | 创建、修改和查询任务 |
| `POST /api/tasks/{id}/{action}` | pause / resume / cancel / trigger |
| `GET /api/executions` | 后台任务及步骤结果 |
| `POST /api/files` | multipart file 上传 |
| `GET /api/files/{id}` | 下载附件 |
| `GET /api/artifacts` | 上传文件、插件解析结果 |
| `GET /api/notifications` | 任务通知 |
| `GET /api/deliveries` | QQ 投递状态 |

## 执行配置

```json
{
  "name": "我的模型",
  "kind": "api",
  "protocol": "openai-chat",
  "baseUrl": "https://api.example.com/v1",
  "model": "模型实际名称",
  "credentialId": "已保存的凭证ID",
  "maxSteps": 12,
  "maxTokens": 4096,
  "timeoutSec": 180,
  "capabilities": {"tools": true, "images": false, "stream": true, "resume": false}
}
```

SDK 配置改成 `kind=sdk`，`provider` 为 `codebuddy / claude / codex`。SDK 的 Token 上限及能力以各 SDK 实际支持为准；Go 的 `maxTokens` 用于 API 直连。Claude/CodeBuddy 使用 `maxTurns`，Codex 整轮通过超时控制，不假设其公开 SDK 提供相同逐轮步数语义。

## 人格

```json
{
  "name": "项目助理",
  "description": "记录行动项",
  "systemPrompt": "简洁说明事实，完成用户授权的工具操作。",
  "examples": [{"role":"user","content":"帮我安排一下"},{"role":"assistant","content":"请告诉我事项和时间。"}],
  "preferences": "用中文；不确定的日期先说明。",
  "tools": ["example__echo", "system__task_create"],
  "default": false
}
```

`tools=null` 表示全部已获服务端授权的工具；`[]` 表示不授予工具。人格不是权限提升入口。

## 任务

```json
{
  "name": "每日邮箱摘要",
  "kind": "recurring",
  "cron": "0 9 * * *",
  "timeZone": "Asia/Shanghai",
  "catchupSec": 3600,
  "configId": "执行配置ID",
  "personaId": "secretary",
  "sessionId": "结果通知会话ID",
  "notify": true,
  "steps": [
    {"id":"summary","kind":"tool","tool":"mail__summarize","arguments":{"folder":"INBOX","limit":20,"unreadOnly":true}},
    {"id":"review","kind":"agent","prompt":"基于前一步结果整理事项，缺失截止日期的事项不要自动安排。","delaySec":0}
  ]
}
```

顺序工具参数支持完整值引用 `${steps.stepId.field}`，保留对象、数组或数值的类型。Agent 步骤会接收先前结果。更新时带当前 `id` 和 `revision`，冲突后刷新再编辑。

## 内部协议

`POST runtime:/runs` 使用运行服务 Bearer token，返回 SSE：`native.session`、`text.delta`、`sdk.tool.started/completed`、`completed`、`error`。HTTP 断开会取消 SDK。相同 run ID 不能重复占用执行日志。

`/internal/mcp` 是运行范围的 Streamable HTTP MCP 入口。`/internal/plugin/*` 使用插件快照范围 token：

- `GET/PUT /kv/{key}`：插件命名空间数据。
- `POST /artifacts`：`name + data` 保存产物。
- `GET/POST /files`：下载 / 上传文件。
- `POST /generate`：`configId + prompt + images`，宿主读取 Key。
- `POST /transcribe`：`configId + artifactId + model`。
- `POST /tasks`：任务字段与稳定 `operationId`；同操作返回同一任务。

宿主根据 `storage/files/models/tasks` 授权校验。SDK Key 只在后端到 SDK 服务的内部请求中使用，不写入事件或 Temporal 参数。
