# 拾一 · 可扩展 AI 秘书

Go 对话主干 + 独立 TypeScript 插件 + 三家 Agent SDK 适配 + Temporal 后台任务。提供 Web 管理端和 QQ 官方 C2C 私聊入口。

## 快速启动

需要 Docker 与 Compose；从源码开发需要 Go 1.26、Node.js 22、npm 和 FFmpeg。

```sh
python3 scripts/init-env.py
./scripts/compose up --build -d
```

打开 **http://localhost:5173**，使用本地 `.env` 中的 `ADMIN_PASSWORD` 登录。初始化脚本不会覆盖已有密钥。请备份 `MASTER_KEY`；丢失后无法解密已保存凭证。

1. 在「系统与凭证」添加模型 Key。
2. 在「执行配置」选择 API 或 SDK、模型、凭证和 Base URL。
3. 在「对话」新建会话，选择人格与执行配置。
4. 让秘书调用 `example__echo` 验证工具链。
5. 在「插件」配置邮箱或视频能力，授权并启用；从任务模板创建后台工作。

**三家 SDK、真实模型端点、真实邮箱和 QQ 账号仍需使用你自己的凭证联调。** 本地验收配置均以 `[本地验收]` 或 `[浏览器验收]` 命名，连接的是确定性测试端点，不能作为真实模型能力或效果证明。详见 [验收报告](docs/acceptance.md)。

## 接入矩阵

| 方式 | 实现 | 工具循环归属 |
|---|---|---|
| SDK | CodeBuddy Agent SDK / Claude Agent SDK / Codex SDK | 官方 SDK |
| API | OpenAI Chat Completions / Responses / Anthropic Messages | Go Direct 执行器 |

API 配置支持 `credentialId + baseUrl + model + protocol`。Key 先写入加密凭证库，公开配置只保存引用。CodeBuddy 直连使用该端点实际支持的兼容协议。

OpenAI 兼容地址一般以 `/v1` 结束；框架追加 `/chat/completions` 或 `/responses`。Anthropic 地址可填写服务根地址或 `/v1`。不要填写完整接口路径或包含 Key 的查询参数。

SDK 使用独立 HOME、工作目录和原生会话数据。SDK 中断后停止自动续用不确定会话，后续请求从公共历史建立上下文。普通成功会话支持各 SDK 自己的会话恢复。SDK 内置文件、Shell 等工具被限制，业务能力经核心 MCP 网关统一授权。

## 项目结构

```text
cmd/secretary/        Go 服务入口，HTTP + Temporal Worker
internal/agent/      API 协议、工具循环、SDK 桥接
internal/service/    对话、人格、管理 API、QQ、插件宿主
internal/plugin/     包版本、MCP 子进程、权限、操作去重
internal/job/        Temporal Workflow / Activity / Schedule
internal/store/      PostgreSQL 存储与并发锁
internal/secret/     AES-GCM 凭证库
runtime/             官方 Agent SDK 执行服务
packages/plugin-sdk/ TypeScript 插件开发包
plugins/             示例、IMAP 邮箱、视频解析
web/                 React + Ant Design 管理端
scripts/             初始化、插件模板、集成与浏览器测试
```

参见 [架构与恢复语义](docs/architecture.md)、[API 与配置](docs/api.md)、[插件开发](docs/plugins.md)、[运维与联调](docs/operations.md)。

面试或项目展示可按 [演示场景](docs/demo.md) 依次演示策略切换、插件扩展、邮箱日程、视频处理与重启恢复。

## 本地开发

```sh
npm ci
npm run build
./scripts/compose up -d postgres temporal
```

在 Go 进程环境设置 `DATABASE_URL`、`MASTER_KEY`、`ADMIN_PASSWORD`、`RUNTIME_TOKEN`。数据库端口为 `5442`，Temporal 为 `7233`。随后分别运行：

```sh
go run ./cmd/secretary
npm run dev:runtime
npm run dev:web
```

三个进程必须使用相同的运行凭证配置。SDK 服务默认 `127.0.0.1:8091`；Go 默认 `:8080`；Vite 自动代理 `/api`。生产模式不会降级使用内存数据库。

```sh
go test -race ./...
npm test
```

完整集成测试及浏览器验收命令见验收报告。没有 `TEST_DATABASE_URL` 时，真实数据库与 Temporal 测试明确跳过。

## 首版边界

- 单用户、单管理员、单个 Go 服务实例；QQ 仅官方私聊。
- 插件是经过管理员信任的本地代码，独立进程不等同于操作系统沙箱。
- 邮箱支持 TLS IMAP、搜索、增量读取、摘要、事项提取、标记已读；不包含 SMTP 发送。
- 视频支持上传文件、音频转写、抽样画面分析。默认 100 MB / 30 分钟，不抓取登录网站。缺少音轨或模型能力会失败并说明原因。
- 视频时间依据为抽样画面时间；转写端点只返回文本时，不伪造逐字时间戳。
- 公共上下文使用有界历史与早期对话摘录；长期记忆、知识库可作为插件添加。
- 外部消息无法保证绝对只发送一次；结果不明会保留 `uncertain`。
