# API 与配置

所有管理 API 位于 `/api`，登录外需要会话 cookie。JSON 出错返回 `{"error":"..."}`。

| 方法与路径 | 说明 |
|---|---|
| `POST /api/login` | `password` 登录 |
| `POST /api/logout` | 退出 |
| `GET /api/status` | 服务连接状态 |
| `GET /api/qq` | QQ 两种渠道状态、个人号绑定（不返回令牌） |
| `PUT /api/qq/onebot` | 修改 OneBot 私聊联系人、群及人格绑定，需管理员登录 |
| `GET/POST /api/secrets` | 凭证引用列表 / 加密保存 |
| `GET/POST /api/configs` | 模型配置 |
| `DELETE /api/configs/{id}` | 兼容旧客户端：归档未被引用的模型配置，不执行永久删除 |
| `POST /api/configs/{id}/test` | API 连接检查；SDK 通过对话联调 |
| `GET/POST /api/personas` | 人格；复制或导入时不传 ID |
| `DELETE /api/personas/{id}` | 兼容旧客户端：归档未被引用的非默认人格，不执行永久删除 |
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
| `GET /api/model-calls` | 插件生成、转写的模型调用记录，需管理员登录 |
| `POST /api/files` | multipart file 上传 |
| `GET /api/files/{id}` | 下载附件 |
| `GET /api/artifacts` | 上传文件、插件解析结果 |
| `GET /api/notifications` | 任务通知 |
| `GET /api/deliveries` | QQ 投递状态 |
| `GET /api/archives` | 归档索引，仅返回类型、记录 ID、名称和归档时间 |
| `POST /api/lifecycle` | 按数字枚举批量归档、恢复或永久删除 |

## 归档、恢复与永久删除

普通管理列表只返回未归档记录。管理端在各列表提供选择与批量归档，在「归档栏」提供恢复和永久删除；归档保留原始记录，永久删除移除业务记录及按资源处理的关联数据。只有已归档记录才能永久删除，系统保留防止重复执行所需的无正文标识。

`GET /api/archives` 返回数组：

```json
[
  {
    "id": "1:session-example",
    "resource": 1,
    "recordId": "session-example",
    "name": "项目讨论",
    "archivedAt": "2026-10-09T02:00:00Z"
  }
]
```

`id` 是归档索引的复合键。调用生命周期接口时使用 `recordId`，不要把归档索引 `id` 当作业务记录 ID。归档索引不包含会话正文、提示词、运行结果或凭证内容；凭证列表也只提供名称和引用信息，不返回 Key、密码或加密正文。

`POST /api/lifecycle` 请求：

```json
{
  "resource": 1,
  "action": 1,
  "ids": ["session-example", "session-busy"]
}
```

`resource` 和 `action` 必须是 JSON 数字，不接受字符串名称。`0` 为 `Unknown` 保留值，不能提交；其他未定义值同样拒绝，不会自动转成某种操作。

| `resource` | 对应列表 | 含义 |
|---|---|---|
| `1` | `sessions` | 对话 |
| `2` | `tasks` | 定时任务，包括邮箱监听 |
| `3` | `artifacts` | 文件与解析产物 |
| `4` | `personas` | 人格 |
| `5` | `plugins` | 插件 |
| `6` | `configs` | 模型配置 |
| `7` | `runs` | 对话运行记录 |
| `8` | `executions` | 任务执行记录 |
| `9` | `notifications` | 任务通知 |
| `10` | `deliveries` | QQ 投递记录 |
| `11` | `model-calls` | 插件模型调用记录 |
| `12` | `secrets` | 凭证 |

| `action` | 操作 |
|---|---|
| `1` | Archive：归档 |
| `2` | Restore：恢复 |
| `3` | Purge：永久删除 |

每次请求只处理一种资源，`ids` 长度必须为 **1 至 100**，批内重复 ID 只处理一次。ID 不能为空、超过 512 字节或包含 NUL。跨类型选择先按 `resource` 分组，超过 100 项分批提交，不可静默截断。请求结构、枚举或数量不合法时返回 HTTP 400 和 `{"error":"..."}`，不会开始处理该批。

有效请求逐项执行，允许部分成功，不是整批事务。HTTP 200 的响应也可能包含失败项，调用方必须检查两个数组：

```json
{
  "succeeded": ["session-example"],
  "failed": [
    {
      "id": "session-busy",
      "error": "会话仍有执行中、排队中或等待通知的请求，请稍后再试"
    }
  ]
}
```

管理端仅移除成功项的选择，保留失败项并显示名称及原因，然后刷新列表。永久删除前明确提示无法恢复并要求确认。

资源行为与依赖保护：

- **任务**：归档前确认没有执行中的工作，并停止实际调度；恢复后保持暂停，不会自动补执行。检查配置后可通过现有 `resume` 或 `trigger` 接口继续使用。邮箱监听适用相同规则。
- **插件**：归档时停用并暂停依赖任务，已有任务使用的固定版本保留；恢复后仍为停用，需要手动启用。仍被任务、运行或执行快照引用的插件不能永久删除。
- **会话**：归档不停止 QQ 通道，也不撤销联系人授权。归档会话收到新消息后自动恢复到普通列表，继续使用原会话上下文。永久删除仍被任务引用的会话会失败，需要先更换通知会话或清理依赖任务。
- **人格、模型与凭证**：默认人格及仍被会话、任务、QQ 绑定、插件或执行快照等引用的相应资源受到保护；需根据失败原因解除依赖后再操作。归档也不会绕过凭证引用检查。
- **记录与文件**：正在执行、排队或等待投递的记录不能归档或永久删除。文件永久删除还会检查引用和执行状态；被拒绝的项目保留原记录。恢复会校验必要关联资源，缺失或仍处于归档状态的依赖可能阻止恢复。

旧的 `DELETE /api/configs/{id}` 和 `DELETE /api/personas/{id}` 现在等价于对应资源的单项 `Archive`，成功仍返回 `{"ok":true}`。新客户端统一使用 `/api/lifecycle`，永久删除必须先归档，再提交 `action: 3`。

## 模型配置

管理端入口为「系统与凭证 → 模型接入」，左侧「模型配置」也可进入。配置对话框支持选用已有凭证，或添加 Key 后自动选用；API 数据仍只保存凭证 ID。

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

## 插件模型调用记录

`GET /api/model-calls` 返回数组，管理端「运行记录 → 插件模型调用」展示相同数据。每条记录包含 `id`、`pluginId/pluginVersion`、`configId/model/protocol`、`kind`（`generate/transcribe`）、可选 `operationId`、`status`、`startedAt`，以及已知的 `finishedAt/durationMs`、`usage` 和安全错误说明 `error`。它只覆盖宿主的生成、转写接口，不包含插件自行调用的外部模型；对话运行用量仍见运行记录。

模型请求发出前保存 `running`，正常结束为 `completed/failed`。主机重启时遗留 `running` 改为 `interrupted`，不自动重跑，也不推算完成时间、耗时或用量。`usage=null` 表示服务未返回可用用量，不等于零；已返回的零值会保留。提供商内部重试或中断时未返回的用量仍未知，因此这些记录不能作为完整账单或成本估算。记录不保存 Key、提示词、媒体或模型正文。

插件 SDK 通过 `X-Secretary-Operation-ID` 传递当前工具操作 ID（最多 512 字节）。该值仅用于关联记录，不参与认证或模型调用去重；一次工具操作中的多次模型请求各有独立记录。

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
  "configId": "模型配置ID",
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

QQ 个人号绑定请求（`PUT /api/qq/onebot`）：

```json
{
  "enabled": true,
  "selfId": "10001",
  "allowedUserIds": ["20002"],
  "allowedGroupIds": ["1128987429"],
  "configId": "已保存的模型配置ID",
  "personaId": "secretary",
  "groupPersonaId": "具有明确工具清单的群人格ID"
}
```

现有 OneBot 的 `selfId` 必须与接入服务实际登录账号一致。启用时，`allowedUserIds` 与 `allowedGroupIds` 至少一类非空，两类各最多 20 个，ID 使用字符串。`allowedUserIds` 仅授权私聊；允许群中的所有成员均可通过真正的 @ 机器人消息触发，不要求列入私聊允许列表。

`personaId` 用于新私聊会话；`groupPersonaId` 独立指定新群聊会话的人格。群人格必须设置明确的 `tools` 数组，允许 `[]`，不接受表示开放全部工具的 `null`。Web 群人格选项只展示符合此要求的人格。群聊固定仅 @ 机器人触发，没有关闭该限制的选项。

默认模型与人格只影响新会话。`GET /api/qq` 保留 `strategies`、`onebot` 绑定及 `napcatWebUrl`，不会返回任何令牌。`onebot` 中返回上述联系人、群和人格字段，保存时应同时保留它们；只使用私聊时群数组为空。每个策略的 `implementation` 展示实际实现，`provider` 仍是兼容路由键 `official/onebot`。未使用本地 NapCat 时 `napcatWebUrl` 为空字符串。

状态中 `configured` 仅表示官方配置齐备，不代表真实收发通过；`online` 表示个人号已登录并启用绑定。自研发送器可不实现连接检查，此时 `state=unknown`，管理员可配置绑定，但不会显示为已验证在线。

外部消息入口：`POST /qq/webhook` 使用官方 Ed25519 签名，当前处理授权私聊；`POST /qq/onebot/events` 使用 `X-Signature: sha1=<HMAC-SHA1(rawBody, ONEBOT_TOKEN)>`，不使用管理员 cookie。OneBot 接受十分钟内的授权私聊，以及允许群内明确 @ 登录账号的群消息，由公共队列异步执行。未允许的联系人或群、未 @ 机器人的群消息及自发消息不提交执行。

群会话增加 `channelRoom`（QQ群号）；空值保持现有私聊语义。`recipient` 在群会话中表示发言人 QQ 号，不能作为群消息的发送目的地。上下文按通道、机器人账号、群号、发言人分别隔离。回复和来源于该会话的定时通知发送到 `channelRoom` 所指的原群，并 @ 对应发言人；不会自动转发到其私聊。Web 对话列表及标题显示群号与发言人。

`POST runtime:/runs` 使用运行服务 Bearer token，返回 SSE：`native.session`、`text.delta`、`sdk.tool.started/completed`、`completed`、`error`。HTTP 断开会取消 SDK。相同 run ID 不能重复占用执行日志。

`/internal/mcp` 是运行范围的 Streamable HTTP MCP 入口。`/internal/plugin/*` 使用插件快照范围 token：

- `GET/PUT /kv/{key}`：插件命名空间数据。
- `POST /artifacts`：`name + data` 保存产物。
- `GET/POST /files`：下载 / 上传文件。
- `POST /generate`：`configId + prompt + images`，宿主读取 Key。
- `POST /transcribe`：`configId + artifactId + model`。
- `POST /tasks`：任务字段与稳定 `operationId`；同操作返回同一任务。
- `POST /notifications`：`sessionId + text + operationId`，需要 `notifications` 授权；复用统一通知投递与 QQ 绑定检查，相同插件的操作 ID 不可改用于不同内容或接收人。

宿主根据 `storage/files/models/tasks/notifications` 授权校验。SDK Key 只在后端到 SDK 服务的内部请求中使用，不写入事件或 Temporal 参数。

## 邮箱监听与通知（可用性改造）

- `POST /api/mail/connection`，请求 `{ "folder": "INBOX" }`：调用已启用邮箱插件的真实 IMAP 连接检查。先通过 `/api/plugins/mail/configure` 保存配置和权限、`/enable` 启用。密码只在配置写入时上传，后续只使用服务端凭证引用。
- `POST /api/mail/watch`：创建或修改基于 `mail__watch` 的 Temporal 周期任务。请求可含 `id`（编辑）、`name`、`folder`、`intervalMinutes`（1/2/5/10/15/30/60）、`sessionId`、`configId`、`personaId`、`includeExisting`。默认不提醒历史邮件；新建会立即提交一次基线检查。未传 `id` 时，同一会话/文件夹使用稳定任务 ID，防止重复订阅。
- `POST /api/notifications/{id}/retry`：仅重试已知 `failed` 通知，生成新的投递操作 ID；`uncertain` 不接受自动重试，不会重新执行原任务。

普通任务和插件模板可以设置：

```json
{
  "notify": true,
  "notifyWhen": "${steps.watch.changed}",
  "notifyText": "${steps.watch.notificationText}",
  "steps": [{"id":"watch","kind":"tool","tool":"mail__watch","arguments":{"folder":"INBOX","monitorId":"my-inbox-watch"}}]
}
```

`notifyWhen` 必须解析为布尔值，`false` 静默完成；`notifyText` 必须解析为非空文本。两者只支持单个完整结果引用，保存时验证引用格式及步骤存在。任务失败不受成功条件影响，周期任务相同故障期间最多每小时尝试一次故障通知；执行记录始终保留。

通知记录包含 `text/status/error/operationId/attempts/createdAt/updatedAt` 和可选 `replyTo`。Web 会话通知为 `saved`，QQ 为 `sent/failed/uncertain`，尚未完成投递为 `pending`。新的 QQ 运行保存 `replyPending` 标记，服务启动及每 15 秒对账补齐通知；只恢复未发出的意图，已经发送或结果不明的外部操作不会自动重发。已知失败的手动重试使用新操作 ID，保留原回复引用。完整结果在通知记录及产物中保留，QQ 单条文本仍受平台发送长度保护。

模型配置新增 `maxInputBytes`（默认 98304，范围 8192..2097152）。它限制 API 请求编码后的字节预算，包含人格、工具声明和对话；不是 Token 数。API 执行器优先裁掉整轮旧历史，当前用户请求与已经执行的工具调用链不被静默丢弃，仍放不下则明确报错。SDK 自身的上下文与轮数边界见 [SDK 使用边界](SDK_LIMITS.md)。
