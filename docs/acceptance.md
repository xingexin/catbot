# 首版验收报告

初次验收日期：2026-09-23；CodeBuddy 真实联调补充日期：2026-09-30。代码、部署配置、管理端、SDK 适配、插件开发包和两个业务验证插件已交付。已完成本地自动化验收及下述 CodeBuddy 联调；不能把本报告视为所有真实服务均已兼容的结论。

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
| QQ 去重、会话串行 | 官方 Ed25519 与 OneBot HMAC 回调测试；两种渠道的并发重复推送只创建一次运行；个人号账号隔离、停用、发送回执和不确定状态测试通过 | 真实 QQ 回调、绑定和投递未联调 |
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
python3 scripts/init-env.py
npm ci
npm run build
./scripts/compose -f compose.yaml -f compose.test.yaml up --build -d
```

不需要厂商凭证的基础测试：

```sh
go test -race ./...
go vet ./...
npm test
```

真实数据库与 Temporal 测试。脚本读取本地 `.env`，只创建测试数据库，不打印密码：

```sh
TEST_DOCKER_CONTEXT=colima-secretary python3 scripts/test-integration.py
```

使用其他 Docker context 时，将变量改成实际名称，例如 `default`。若自行准备测试服务，也可以直接设置 `TEST_DATABASE_URL` 和 `TEST_TEMPORAL_ADDRESS` 后运行 Go 测试。

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
