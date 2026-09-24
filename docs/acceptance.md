# 首版验收报告

验收日期：2026-09-23。代码、部署配置、管理端、SDK 适配、插件开发包和两个业务验证插件已交付。已完成本地自动化验收；厂商服务与个人账号需要凭证联调，不能把本报告视为所有真实服务均已兼容的结论。

## 本次实际运行的环境

- macOS，独立 Colima `secretary` 环境，Docker Compose 实际构建并启动全部应用服务。
- 容器内 Node.js 22、PostgreSQL 17.6、Temporal 1.29.1、FFmpeg、Nginx。
- 本地 Go 1.26.4、Node.js 20.20.2；隔离的无头 Chrome 访问真实管理端。
- 数据库测试使用独立 `secretary_test`；Temporal 集成测试使用独立任务队列。
- 模型协议测试使用明确标记的确定性 HTTP/SSE 端点；没有调用真实厂商模型、邮箱或 QQ 账号。

## 验收矩阵

| 计划场景 | 结果与证据 | 真实服务状态 |
|---|---|---|
| 同一人格与插件跨执行策略 | 三种 API 协议完成真实 Go → 独立 MCP 插件 → 工具结果回传；SDK 适配完成编译、事件映射及运行服务契约测试 | 三家 SDK 与厂商模型未联调 |
| 插件注册、权限、停用、版本 | 实际启动 Node MCP 子进程；测试参数验证、权限拒绝、新调用阻止、在途版本保留、插件退出和不确定操作拒绝重跑 | 本地通过 |
| API 工具循环与流式边界 | 三种协议参数分片、完整响应、损坏/截断流、429 重试、工具失败、步数超限、禁用工具能力均有自动化测试 | 兼容端点需逐个联调 |
| QQ 去重、会话串行 | 官方格式 Ed25519 回调测试；12 次并发重复推送只创建一次运行；同一会话连续消息与取消测试通过 | 真实 QQ 回调、绑定和投递未联调 |
| 任务管理与实际调度一致 | 实际 Temporal Schedule 创建、改期、暂停、恢复、删除检查；Start Delay、取消、手动触发去重及调度意图恢复测试通过 | 本地 Temporal 通过 |
| 重启、回放与副作用 | 实际重启 backend 后同一 Workflow 的持久化 Timer 继续，已保存步骤保留；稳定 Workflow ID 和操作 ID 去重；QQ 投递结果不明不重发 | 真实外部写入仍需平台验证 |
| 邮箱和视频业务链路 | IMAP 游标/UIDVALIDITY、摘要输入测试；实际上传视频、FFmpeg 抽音频/画面、调用转写/视觉接口、保存完整结果；多步任务引用前一步结果通过 | 真实 IMAP 与 ASR/视觉模型未联调 |
| 中断与失败可查询 | SDK SSE 中断不自动重跑；实际插件子进程退出；模型限流、通知失败、不确定发送记录测试通过 | 厂商 SDK 中断需凭证复验 |

## 自动化检查

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
