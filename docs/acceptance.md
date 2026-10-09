# 首版验收报告

初次验收日期：2026-09-23；CodeBuddy 真实联调补充日期：2026-09-30。代码、部署配置、管理端、SDK 适配、插件开发包和两个业务验证插件已交付。已完成本地自动化验收及下述 CodeBuddy 联调；不能把本报告视为所有真实服务均已兼容的结论。

## 批量归档与整型枚举约定（2026-10-09）

管理端主要资源列表增加选择、全选和批量归档；左侧归档栏支持按资源筛选、恢复与永久删除。旧人格/模型配置 DELETE 入口改为归档。归档索引与业务正文分开保存，不要求修改已有 JSON 数据格式。

- 新增资源、归档操作、活动状态及任务控制动作使用固定编号的具名 int 枚举。规范固定在 [AGENTS.md](../AGENTS.md) 与 [设计理念](design-principles.md)；历史字符串协议按领域渐进兼容迁移，本次不宣称已全仓迁移。
- 默认 Web 查询与 Agent 的任务/产物列表隐藏归档项。新消息自动恢复所属会话；会话归档不等于停止 QQ 接收。恢复任务仍暂停，恢复插件仍停用。
- 归档任务先确认实际调度已停止；取消失败保留持久化意图，恢复扫描继续取消。通知完成与归档共用任务锁，未知/活动/结果不明的任务不得清理。恢复递增任务版本，旧排队触发不能重新启动。
- 永久删除要求已归档，并检查默认人格、通道绑定、任务、快照等依赖。数据库记录与运行事件在同一事务删除；最小防重放墓碑拒绝晚到写入，墓碑不保存正文。附件删除失败保留归档元数据供重试。插件清除私有 KV、版本及未共享的私有凭证，旧宿主请求不能复活已删除 KV。
- 新工具调用记录明确的 run/execution 缓存归属；永久删除同步清理所属工具结果，仍被其他记录共享的缓存保留，内置工具在副作用前检查墓碑。历史缓存只清理快照或 SDK 事件可证明归属的键，不猜测未知旧键；SDK 原生会话、执行服务日志与 Temporal 历史不在业务记录删除范围内。
- 批量请求逐项报告成功/失败；单项失败不伪装整批成功，失败项在页面保留选择并展示原因。

已实际验证：

- 全量 `go test -race ./...`、`go vet ./...`；新增生命周期 HTTP 契约、无效数字枚举、默认列表过滤、会话上下文恢复、并发通知/投递、引用保护、任务取消恢复和插件清理回归。
- 专用 `secretary_test` 数据库、独立 schema 的 PostgreSQL 测试：第二条删除故意失败时整批事务及事件回滚；成功后重试幂等、旧 Put/Append 被阻止、允许复用的归档索引可重建。
- 真实 Temporal 独立测试队列：Schedule 创建/改期/暂停/恢复/取消、Start Delay、Worker 重启保留已完成步骤和人格快照；只读回放三份已有 TaskWorkflow 历史。关闭 NapCat 的隔离 bootstrap 测试通过，没有调用外部系统。 新增独立 schema 和唯一 Schedule 的归档集成测试也已实际通过：归档后真实 Schedule 不存在，恢复后仍暂停且不自动创建，只有显式 resume 重建调度；硬删除后旧 Begin、编辑与 PostgreSQL 晚到写入均被拦截。
- Go/TypeScript 双语言真实 MCP 插件集成测试通过，使用独立 fixture 验证宿主兼容。
- `npm test` 工作区测试通过；Web 测试 9 项与前端构建通过。真实 FFmpeg 的可选用例在本次 `npm test` 中跳过，不新增视频真实服务结论。
- Chrome + 模拟 API 的页面回归：部分归档、当前聊天/SSE 清理、跨类型恢复、永久删除取消与确认、各主要列表入口、错误原因、手机选择界面，无页面脚本错误。使用隔离 fixture，没有归档或删除真实用户数据。

- `make` 已重新构建并部署，backend、Web、PostgreSQL、Temporal、SDK runtime、NapCat 六个服务健康。部署后在实际管理端只读复验：20 个原有会话仍显示，「批量管理」与「归档栏」入口可见，归档页正常加载且未操作真实记录。

此次没有调用真实 QQ 发送、邮箱或收费模型；恢复/硬删除的含义和跨服务边界见 [API 与配置](api.md#归档恢复与永久删除)。

## QQ 群聊扩展（2026-10-07）

应用户指定群聊验收的要求，OneBot 通道增加群文本支持：结构化 @ 机器人才触发，群白名单与私聊联系人分别配置；群和发言人的会话、上下文及原生 SDK 会话相互隔离。回复与提醒回原群并 @ 发起人。群人格需显式列出工具，任务列表及修改限制为发起人的原群会话，后台任务继承该边界；无归属字段的私人产物工具不向群暴露。官方 QQ 适配器仍只支持私聊。

- 全包 `go test -race`（含实际 PostgreSQL/Temporal）、`go vet ./...`、Web 构建和 `make` 部署通过。新增测试覆盖结构化 @、群目标和回复引用、并发重复事件、不同群/发言人/私聊隔离、群撤销授权、工具与任务权限、管理员并发改绑后的权限复查。
- 用户扫码后，NapCat 实际账号 `2875219670` 在线，已核实机器人为指定群 `1128987429` 的成员。管理 API 与实际页面显示群已启用，选用 CodeBuddy `glm-5.3` 和仅开放回显、个人任务工具的群人格。
- 真实群聊首轮收发已通过：群成员 `1903002109` 在群 `1128987429` @ 机器人发送 `111`，真实回调进入会话 `qq-onebot-0f3218aac77824b605ebbc934c3f0a37`；CodeBuddy `glm-5.3` 运行 `run-qq-a5415ca51bbacc17186eebebaa72ce88` 完成，统一通知与投递记录均为 `sent`，平台回复 ID 为 `262853841`。随后只读调用 OneBot `get_msg` 返回 `ok`，核对目标群、引用原消息 `1904798086`、@ 发起人及实际回复正文一致。此链路使用真实群员消息与真实模型，没有模拟入站回调。
- 真实连续上下文已通过：用户先发送验收标记，随后仅问「刚才的验收标记是什么？」；运行 `run-qq-a5478203a427e9c295adf64f6bdc74a8` 正确回答 `QQ-1007-A`，复用原生 SDK 会话 `01a11520-3f25-7f9e-bfea-41614d874531`。
- 首次真实群提醒发现故障：运行 `run-qq-9c67d3f995c9bf803c64971863155bde` 实际成功调用 `system__task_create` 创建 `task-578b1c5d7abf85d92c4ebbb8`，但模型误把 `notifyWhen` 填为 `${steps.reminder.text}`，创建时未拦截。Temporal 于北京时间 14:53:40.031546528 准时启动并完成回显步骤，随后通知条件解析因得到字符串而非布尔值失败，执行记录为 `notification_failed`，错误为 `notification condition must resolve to a boolean`，没有发送该定时群提醒。原始任务、执行和工具事件已冻结在 `firstReminderFailure`，没有覆盖为成功记录。
- 已修复任务保存时的通知条件类型预校验，并补充工具 Schema 说明；同时让一次性任务结束时同步通知失败状态，避免任务显示完成却隐藏投递失败。相关 16 项场景回归通过，部署接口对 `${steps.reminder.text}` 作为条件返回 HTTP 400，提示无条件提醒应省略 `notifyWhen`。确认原任务未投递后，管理员修正同一任务为 revision 2、移除错误条件，改期至北京时间 15:00:05.347266；实际于 15:00:05.391526379 启动，执行完成，通知尝试一次即为 `sent`，平台消息 `182296124` 经 `get_msg` 核对原群、@ 发起人和提醒正文一致。这证明管理员修正后的真实 Temporal → 统一通知 → QQ 群投递成功，不能以管理员修正替代自然语言新建验收。
- 独立自然语言复测通过：从管理端向同一群会话提交「一分钟后提醒我：模型创建提醒复测完成。」，运行 `run-qq-group-model-reminder-retest-20261007` 的真实 CodeBuddy 首次填写错误条件被拒绝，随后自动修正，两个工具尝试仅成功创建一个任务 `task-9f801390696874ee3202f76b`。该任务省略 `notifyWhen`，于北京时间 15:04:32.014083422 实际执行，唯一通知尝试一次即发送成功；平台消息 `2033711544` 经 `get_msg` 核对群、@ 发起人和正文一致。该项覆盖模型自动纠错创建 → Temporal → 真实群提醒，入口是管理 API，不冒充修复后的 QQ 入站测试。修复后由群员从 QQ 新发自然语言提醒的完整链路仍待补验。
- 修复后的全包 race（含实际 PostgreSQL/Temporal）、`go vet ./...`、差异检查通过；最终后端镜像 `403935651b35` 已于北京时间 15:05:57 部署并健康，其他服务未重启。管理端可看到新提醒及「任务通知：模型创建提醒复测完成」，截图为 `data/acceptance/qq-group-conversation.jpg`。
- 发送回执与平台消息查询不等同于用户已确认在 QQ 客户端看到回复。先前提示消息 `2085802705` 由直接调用 OneBot 发出，只作 NapCat 连通性证据，不计作秘书业务回复。完整记录见 [QQ 群聊真实联调](../data/acceptance/qq-group-live-report.json)，管理端截图为 `data/acceptance/qq-group-settings.jpg`。

协议依据：[OneBot 群消息事件](https://github.com/botuniverse/onebot-11/blob/master/event/message.md)、[群发送 API](https://github.com/botuniverse/onebot-11/blob/master/api/public.md#send_group_msg-发送群消息)、[@ 消息段](https://github.com/botuniverse/onebot-11/blob/master/message/segment.md#某人)。以下章节保留先前日期的验收状态，最新 QQ 状态以本节及真实联调记录为准。

## 日常可用性改造（2026-10-03）

本轮补齐了邮箱增量提醒、失败投递管理、上下文预算、任务和进程恢复、视频长输入处理，并部署到本机。当前真实 CodeBuddy 可用于 Web 对话；QQ 未在线且联系人白名单为空，邮箱尚未配置，视频只有验收端点。**这些真实账号链路尚未全部验收，不能据此宣称已全面上线。** 使用步骤见 [日常使用](daily-use.md)。

交付的行为：

- 「邮箱监听」独立页面支持配置、启用、连接检查、创建/修改/暂停/恢复监听和查询最近结果。邮箱插件 1.1.1 使用文件夹、UIDVALIDITY、UID 及订阅标识保存增量位置；首次默认静默建立基线，无新邮件不通知、不调用模型。新邮件通知可投递到已授权 QQ 会话或 Web，完整内容保存为产物。
- 普通回复和任务通知都先保存投递意图。运行结束与通知创建之间的退出由持久待回复标记恢复；已发送或结果不明的操作不会自动重复发送。明确失败支持单独重试投递，不重跑原模型/插件，聊天回复的引用在重试时保留。周期故障每小时最多提醒一次，每次执行仍可查。
- 后台 Agent 创建的提醒回到原 QQ/Web 会话。Temporal 初始化先保存配置快照，再写运行占位；数据库失败重试保留快照，已关闭 Workflow 遗留的运行占位可释放，未知状态不冒险重做。
- 三种直连协议具备编码后输入预算、完整轮次裁剪、工具结果边界、取消、异常流和工具参数/ID 检查。SDK 业务工具开关在宿主执行；CodeBuddy 原生工具限制经真实初始化清单核对，仅暴露本次允许的秘书 MCP 工具。
- 视频插件 1.1.0 使用实际 FFmpeg 提取媒体，长音频分段、画面抽样覆盖全片，校验模型返回的摘要/时间线/事项。缺失能力或处理失败明确报错并保留标为未完成的产物。容器 DNS 和 Temporal 地址解析故障已修复，`make` 实际启动通过。

本轮证据区分：

| 检查 | 实际结果 | 边界 |
| --- | --- | --- |
| Go 并发与故障测试 | 全包 race、vet；PostgreSQL/Temporal 集成通过 | 包含模拟发送器和故障存储；不能替代真实 QQ |
| Worker 重启 | 实际重启 backend 后，同一 Workflow 的 20 秒持久 Timer 继续完成 | 不代表厂商 SDK 内部每次模型调用可恢复 |
| CodeBuddy iOA / GLM 5.3 | 真实两轮流式对话、原生会话恢复、一次 echo 工具调用通过；初始化工具仅含允许的 MCP | Claude、Codex、真实直连端点未联调；SDK 限制不是操作系统沙箱 |
| 对话创建定时提醒 | 真实 CodeBuddy 调用一次 `system__task_create`，约 90 秒后真实 Temporal 执行插件，唯一 Web 通知保存成功 | 本次通过明确任务参数验证工具与调度链路；没有真实 QQ 投递，也不等同于所有自然语言日期均能正确解析 |
| 邮箱协议 | 真正的 MCP 子进程、ImapFlow 和 mailparser 连接本地 TLS IMAP 测试服务，11 项测试通过 | 验证增量、重复操作、正文解析、只读获取和显式标记已读；没有真实邮箱账号 |
| 视频链路 | 实际文件上传、容器 FFmpeg、模型 HTTP 接口、结构化产物链路通过 | 转写/视觉使用固定响应的本地测试服务；内容理解质量未验收。主机缺 FFmpeg 的测试跳过，由容器媒体测试补验 |
| 管理端 | 登录、模型接入、QQ 双策略、邮箱监听、人格/任务/产物页面及前端构建通过，无页面脚本错误 | 邮箱/模型配置表单回归拦截写入，不保存测试密码或发真实消息 |

本轮报告：[界面与视频链路](../data/acceptance/browser-report.json)、[邮箱管理页](../data/acceptance/mail-settings-report.json)、[QQ 管理页](../data/acceptance/qq-browser-report.json)、[模型接入](../data/acceptance/model-settings-report.json)、[CodeBuddy 实际调用](../data/acceptance/codebuddy-live-report.json)、[Worker 恢复](../data/acceptance/recovery-report.json)。后文保留早期验收历史；以本节说明的当前配置状态为准。

定时提醒实测：`task-8f9d79e2182fd4624d31c32a` 计划北京时间 05:05:15.562，实际 05:05:15.588 启动，插件结果和唯一通知均保留随机校验标记。详见 [真实 Agent 定时提醒报告](../data/acceptance/codebuddy-task-live-report.json)，复现命令 `node scripts/codebuddy-task-smoke.mjs`。脚本会消耗现有 CodeBuddy 模型额度，只创建 Web 提醒。

### 对照原计划的补充修复

- SDK 的工具开关此前会在保存时被覆盖为开启，已修正为保留管理员选择。三家 SDK 均通过保存、重新读取、宿主拒绝工具的回归；SDK 图片输入明确拒绝，界面不再展示不能生效的图片、流式或最大输出 Token 开关。API 勾选行为保留。
- 邮箱插件 1.1.2 对模型摘要和结构化事项严格校验：非空摘要/标题、有效且带时区的日期或 `null`、来源 UID 必须属于实际读取邮件。不合法输出不保存为成功产物。49 项单元及本地 TLS/MCP 测试通过；结构正确仍不能证明真实模型的理解质量。
- 插件 SDK 1.1.0 提供独立 `host.notify`，需要显式 `notifications` 授权，复用 Web/QQ 投递与联系人授权。同一插件操作 ID 不可改用于不同内容或接收人；并发重复和结果不明保护通过测试。
- 插件生成/转写调用先保存记录，再发模型请求，保存实际返回的用量、状态、模型、插件版本和操作关联；未返回的用量为 `null`，不记为零。重启后未完成记录标记 `interrupted`，不自动重发。实际视频链路验证了一条转写记录（未知用量）和一条生成记录（测试服务返回用量），两条均关联到同一任务步骤；管理端可查询，未保存提示词、媒体、正文或凭证。
- 最终完整 Go race（含 PostgreSQL/Temporal）、vet、Node 工作区测试及 `make` 镜像构建与启动通过。已部署 example 1.0.2、mail 1.1.2、video 1.1.1；升级前后配置、凭证引用、权限和启停状态的哈希保持一致，见 [插件升级报告](../data/acceptance/plugin-upgrade-report.json)。
- 部署后再次完成三种 API 协议、插件工具、实际 Temporal 和实际 FFmpeg 的浏览器回归，页面无脚本错误，见 [链路及模型调用记录](../data/acceptance/browser-report.json)。模型仍使用本地协议测试端点，不新增真实模型兼容性或内容理解结论。
- 管理端真实验收通过人格创建、编辑、复制、导入导出、默认人格用于新会话、引用删除保护、会话重绑及删除无引用人格；完成示例插件健康/日志读取，原默认人格已恢复。另验收 API 能力勾选、切换 SDK 后隐藏无效选项和保存工具禁用。没有真实模型调用或 QQ 发送，见 [人格及插件回归](../data/acceptance/persona-plugin-report.json)、[模型配置回归](../data/acceptance/model-settings-report.json)。

## 本次实际运行的环境

- macOS，独立 Colima `secretary` 环境，Docker Compose 实际构建并启动全部应用服务。
- 容器内 Node.js 22、PostgreSQL 17.6、Temporal 1.29.1、FFmpeg、Nginx。
- 本地 Go 1.26.4、Node.js 20.20.2；隔离的无头 Chrome 访问真实管理端。
- 数据库测试使用独立 `secretary_test`；Temporal 集成测试使用独立任务队列。
- 初次模型协议测试使用明确标记的确定性 HTTP/SSE 端点；2026-09-30 补充了真实 CodeBuddy 模型调用，邮箱与 QQ 仍未联调。

## 模型配置入口改进（2026-10-01）

- 「系统与凭证」增加模型接入区，支持添加 API 或 Agent SDK、搜索、编辑和连接检查；原左侧入口及弹窗、会话、QQ、任务表单统一称为「模型配置」。接入方式表单明确提示 CodeBuddy 位于 Agent SDK，API 协议按端点实际格式选择。
- 模型弹窗可直接添加 Key，保存后自动选用凭证引用。取消添加或保存期间按 Esc 不丢失模型草稿；配置请求不携带 Key 原文。
- `npm run build -w web`、Web 镜像构建和部署通过。`node scripts/model-settings-smoke.mjs` 使用真实管理页和读取接口，通过三种协议选项、三家 SDK 选项、CodeBuddy 选择、Key 自动选用、草稿保护及改名入口检查，无页面脚本错误。
- 浏览器测试拦截配置/凭证写入，不修改真实数据，不调用模型。此次验证的是界面和请求契约，不新增真实模型兼容性结论。

报告：[模型配置浏览器验收](../data/acceptance/model-settings-report.json)。

## 验收矩阵

| 计划场景 | 结果与证据 | 真实服务状态 |
|---|---|---|
| 同一人格与插件跨执行策略 | 三种 API 协议完成真实 Go → 独立 MCP 插件 → 工具结果回传；CodeBuddy SDK 使用同一示例插件完成真实调用 | CodeBuddy iOA / GLM 5.3 部分通过；Claude、Codex 与真实 API 端点未联调 |
| 插件注册、权限、停用、版本 | 实际启动 Node MCP 子进程；测试参数验证、权限拒绝、新调用阻止、在途版本保留、插件退出和不确定操作拒绝重跑 | 本地通过 |
| API 工具循环与流式边界 | 三种协议参数分片、完整响应、损坏/截断流、429 重试、工具失败、步数超限、禁用工具能力均有自动化测试 | 兼容端点需逐个联调 |
| QQ 去重、会话串行 | 官方 Ed25519 与 OneBot HMAC 回调测试；并发去重、账号/群/发言人隔离、群工具权限、发送失败与不确定状态测试通过；真实群员 @ → CodeBuddy → 统一通知 → QQ 群回复完成，`get_msg` 核对回复 `262853841` | 2026-10-07 真实 QQ 入站/回复/上下文通过；管理端向同群会话提交自然语言，由模型自动纠错新建并实际群提醒通过；修复后 QQ 入站新提醒与客户端收悉待补验；官方 QQ 未联调 |
| 任务管理与实际调度一致 | 实际 Temporal Schedule 创建、改期、暂停、恢复、删除检查；Start Delay、取消、手动触发去重及调度意图恢复测试通过 | 本地 Temporal 通过 |
| 重启、回放与副作用 | 实际重启 backend 后同一 Workflow 的持久化 Timer 继续，已保存步骤保留；稳定 Workflow ID 和操作 ID 去重；QQ 投递结果不明不重发 | 真实外部写入仍需平台验证 |
| 邮箱和视频业务链路 | IMAP 游标/UIDVALIDITY、摘要输入测试；实际上传视频、FFmpeg 抽音频/画面、调用转写/视觉接口、保存完整结果；多步任务引用前一步结果通过 | 真实 IMAP 与 ASR/视觉模型未联调 |
| 中断与失败可查询 | SDK SSE 中断不自动重跑；实际插件子进程退出；模型限流、通知失败、不确定发送记录测试通过 | 厂商 SDK 中断需凭证复验 |

## CodeBuddy iOA / GLM 5.3 真实联调（2026-09-30）

- 使用加密凭证库中的 iOA Key、`CODEBUDDY_INTERNET_ENVIRONMENT=ioa`、模型 ID `glm-5.3`，由官方 `@tencent-ai/agent-sdk 0.3.268` 执行工具循环；没有切换到 API 直连策略。
- 流式对话通过：运行 `run-3352a7cb-3c50-47d7-91f7-a96d51592ea5` 回复 `CODEBUDDY_GLM53_OK`。
- 会话续接与实际插件调用通过：运行 `run-04f9f751-d2a1-4a03-9377-a4acff4bdcf4` 复用原生会话 `01a0f13d-ff47-719a-bef9-797ac05bdffd`，从上一轮提取随机标记 `glm53-44b08ed4`，调用 `example__echo`。Go 网关保存了一次成功的 `tool.completed`，返回原文和 `characters: 14`，模型据此给出最终回复。
- 修复旧 SDK 恢复会话误返回历史结果的问题；业务 MCP 配置 `alwaysLoad: true`，避免默认延迟加载与禁用内置 ToolSearch 冲突。配置依据：[CodeBuddy MCP 首轮加载说明](https://www.codebuddy.ai/docs/cli/mcp-first-run-status)。
- SDK 初始化事件保存模型及 MCP 连接状态。畸形 NUL 文本在输出边界明确报错，避免退化为 PostgreSQL JSONB 写入错误；没有把模型输出的工具调用样式文本当作实际工具执行。
- 自动化：`npm test` 全部 11 项测试（含执行服务 4 项）、完整构建及真实双轮联调脚本通过。取消、进程中断、长期运行稳定性、其它插件和模型不在本次真实联调结论内。

报告：[CodeBuddy 真实联调](../data/acceptance/codebuddy-live-report.json)。复现：先在凭证库与模型配置中设置自己的账号，然后运行 `node scripts/codebuddy-smoke.mjs`。脚本不包含 API Key，会产生实际模型调用。

## QQ 双策略交付（2026-09-30）

- 实现 `official / onebot` 策略、共用接收队列和投递记录，新增个人号管理员绑定、联系人白名单、登录账号校验和 Web 连接状态。
- `go test -race ./...`、`go vet ./...`、`npm test`（11 项）、Web 构建通过。QQ 测试使用受控 HTTP 服务和模拟执行器，覆盖回调签名、12 次并发去重、账号隔离、自发/群/过期消息过滤、禁用、普通回复及任务通知路由、HTTP 拒绝、异步或畸形回执、连接断开和禁止自动重发。此次未重新运行数据库与 Temporal 专用集成测试。

- `make` 实际构建并启动全部服务，包括 NapCat v4.18.28 ARM64 镜像。初始化升级与重复启动验证保留已有凭证、管理员密码和 OneBot 配置。
- 发现原 20 GB Colima 数据盘已满导致 PostgreSQL 重启；原环境扩容到 60 GB 后恢复，保留全部数据卷。新环境脚本也改为 60 GB。
- 扩容后 `node scripts/qq-browser-smoke.mjs` 通过：管理端正常显示两种策略和已有 CodeBuddy 配置；数据库、Temporal、SDK 状态正常；访问控制、回调签名拒绝、令牌不泄露、NapCat WebUI 和无页面错误检查通过。
- 当前官方渠道未配置、NapCat 尚未登录，真实 QQ 发消息及真实定时投递仍未联调。需要用户扫码并绑定联系人后，用另一账号发消息，再创建提醒验证通知。

产物：[浏览器报告](../data/acceptance/qq-browser-report.json)、[管理页截图](../data/acceptance/qq-settings.png)。复现脚本只检查页面和接口，不会绑定账号或发送消息。

### 消息收发依赖解耦补充（2026-09-30）

- `Sender` 与连接状态接口分离；官方 QQ / OneBot 适配器独立成包，不依赖 `service.App`、业务会话或存储。统一收件入口负责授权与去重，统一发送入口负责意图落库及结果不明保护。
- `go test -race ./...` 和 `go vet ./...` 通过。新增同一套聊天执行与 `Notify` 测试，分别注入官方 QQ、OneBot 和自研替代 Sender；验证人格配置、会话历史、回复引用、定时通知、稳定操作去重与持久化发送意图。自研 Sender 不实现状态检查也可工作，无法绕过宿主绑定；未知或矛盾发送结果保守记为 `uncertain`。
- 启动入口测试用两个受控 OneBot HTTP 服务，改变 `ONEBOT_URL` 后重建应用，复用相同数据库和会话，确认请求发往新服务。4 项部署配置测试通过，覆盖旧配置默认开启、关闭后无隐式本地地址、环境覆盖和非法布尔值。
- 实际执行 `NAPCAT_ENABLED=false make`：本地 NapCat 容器停止，backend / PostgreSQL / Temporal / SDK runtime 健康，Web 可登录。Chrome 回归确认两个通道显示、状态 API、鉴权与令牌脱敏正常，本地 NapCat 登录入口隐藏。
- 再执行默认 `make`：本地 NapCat 和秘书主体恢复运行；完整镜像构建包含 Web 构建。Chrome 回归确认本地登录入口恢复，NapCat WebUI 可访问，无页面脚本错误。回归发现并修复 Nginx 缓存旧 backend 容器地址导致的登录 502，修复后复验通过。另行重建 backend、保持 Web 容器不变，API 代理仍可用；该次重建分配到了相同 IP，单独记录为重建回归，不宣称完成强制换 IP 故障注入。
- 本次未重新运行真实数据库 / Temporal 专用集成测试，也未调用付费模型或真实 QQ 账号收发。QQ 发送验证使用接口模拟；调用了任务服务的实际 `Notify` 方法，但不把它表述为真实 QQ 定时投递已通过。

报告：[关闭 NapCat 的管理端回归](../data/acceptance/qq-browser-report-napcat-disabled.json)、[默认部署管理端回归](../data/acceptance/qq-browser-report.json)。[后端重建与代理回归](../data/acceptance/proxy-recovery-report.json)。替换与自研说明：[消息适配器](message-adapters.md)。

## 初次自动化检查

已通过：

- `go test -race ./...`，包含实际 PostgreSQL 与 Temporal 集成测试。
- `go vet ./...`。
- `npm run build`：插件 SDK、三个独立插件、Node 执行服务及 Web。
- `npm test`：10 个 TypeScript/Node 测试，覆盖运行服务、事件映射、插件宿主、邮箱、视频和 Web JSON 处理。
- `npm audit`：本次锁文件报告 0 个已知漏洞；这是执行时的依赖检查结果。
- 实际 Docker 镜像构建、服务健康检查和浏览器验收，无页面脚本错误。

本地生成的验收产物：

- [浏览器与视频报告](../data/acceptance/browser-report.json)
- [Worker 重启恢复报告](../data/acceptance/recovery-report.json)
- [对话页面截图](../data/acceptance/conversation.png)
- [人格页面截图](../data/acceptance/personas.png)
- [解析结果页面截图](../data/acceptance/artifacts.png)

`data/` 是本机验收产物与运行数据目录，不应提交到源代码仓库。重新执行以下脚本可生成新的报告。

## 复现步骤

初始化并构建：

```sh
python3 deploy/scripts/init-env.py
npm ci
npm run build
./deploy/scripts/compose -f deploy/compose.test.yaml up --build -d
```

不需要厂商凭证的基础测试：

```sh
go test -race ./...
go vet ./...
npm test
```

真实数据库与 Temporal 测试。脚本读取本地 `deploy/.env`，只创建测试数据库，不打印密码：

```sh
TEST_DOCKER_CONTEXT=colima-secretary python3 scripts/test-integration.py
```

使用其他 Docker context 时，将变量改成实际名称，例如 `default`。若自行准备测试服务，也可以直接设置 `TEST_DATABASE_URL` 和 `TEST_TEMPORAL_ADDRESS` 后运行 `go test -tags=integration -race ./...`。

浏览器与视频链路验收：

```sh
TEST_DOCKER_CONTEXT=colima-secretary node scripts/browser-smoke.mjs
TEST_DOCKER_CONTEXT=colima-secretary node scripts/recovery-smoke.mjs
```

浏览器脚本默认使用 macOS Chrome；其他系统通过 `CHROME_PATH` 指定可执行文件。它创建标记为验收用途的配置、人格、会话、任务与文件。恢复脚本依赖浏览器脚本创建的配置，会重启本项目的 backend 容器。

## 凭证联调清单

在 [运维与联调](operations.md) 中配置相应账号，然后逐项记录厂商、端点、模型、日期与运行 ID：

1. CodeBuddy、Claude、Codex 各自完成普通对话、同一业务工具调用、会话恢复、取消和进程中断。确认各自模型/账号允许所配认证及 MCP 功能。
2. 三种 API 协议分别连接真实端点，验证流式参数、工具结果、上下文续接、限流和能力不支持错误。
3. QQ 官方后台完成 HTTPS 回调与绑定用户验证，测试普通回复、重复推送及后台通知。
4. IMAP 使用测试文件夹完成增量读取、搜索、UIDVALIDITY 检查、摘要、标记已读；创建实际周期摘要任务，确认第二轮不会重复同步。
5. 转写与视觉模型使用有语音的真实视频，检查摘要与事项时间。固定回复的测试端点只证明接口和处理链路，不能证明内容理解质量。

真实服务联调前，不应把验收矩阵第 1、4、7、8 项报告为已全面通过。

## 2026-10-07 米雪儿人格真实模型验收

使用用户提供的 `米雪儿.md` 原文与 CodeBuddy `glm-5.3` 完成 24 次真实模型请求：原业务会话连续 21 轮，另一个新会话 3 轮。身份与关系、话题切换、工具后人格、早期随机约定、诱导改人格、公共历史摘要、新 SDK 上下文及正常重建服务后的续聊均未观察到人格遗忘；不宣称 SDK 内部压缩或无限长期记忆已获验证。

基线发现“只要安排建议却创建 5 个提醒”的工具决策偏差。增加统一规则后，相同请求在新会话只返回建议；明确要求的单个提醒仍能创建并取消。保留原失败记录，测试任务均已取消。未知童年细节仍有轻度扩写，回答也有偏长问题。本轮批量测试走 Web 实际模型，未冒充 QQ 的多轮真实入站测试，也未等待已取消提醒到期投递。详见 [独立逐轮人格评估](persona-test-review.md) 与 [原始实测记录](../data/acceptance/persona-michele-live-report.json)。
