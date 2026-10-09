# catbot · 可扩展 AI 秘书

Go 对话主干 + 独立 Go / TypeScript 插件 + 三家 Agent SDK 适配 + Temporal 后台任务。提供 Web、QQ 官方机器人和 NapCat / OneBot 个人 QQ 私聊入口。

## 快速启动

需要 Make、Python 3、Docker 与支持 `--wait` 的 Compose；Mac 可使用 Colima 或 Docker Desktop 提供运行环境。Go、Node.js 和 FFmpeg 由镜像提供；从源码开发才需要在本机安装 Go 1.26、Node.js 22、npm 和 FFmpeg。

在项目根目录执行：

```sh
make
```

默认 `make` 会先准备 Docker 环境，再初始化 `deploy/.env`、构建镜像、在后台启动 PostgreSQL、Temporal、SDK 执行服务、后端、Web 和 NapCat，等待服务就绪后输出访问地址。首次构建需要下载镜像和依赖，后续启动会复用构建缓存。修改代码或 `deploy/.env` 后再次执行 `make` 即可应用更新。

NapCat 是可选外部依赖。在 `deploy/.env` 设置 `NAPCAT_ENABLED=false` 后执行 `make`，只启动秘书主体，并停止原来运行的本地 NapCat（保留登录数据）。要连接其他 OneBot 服务，再设置 `ONEBOT_URL` 和 `ONEBOT_TOKEN`。也可临时用 `NAPCAT_ENABLED=false make` 验证。

`make docker` 可单独准备 Docker 环境：已运行时直接复用；当前 Colima 环境停止时自动启动它；Mac 的 Docker Desktop 环境会自动唤起应用。当前为默认环境且安装了 Colima 时，新安装会启动项目的 `catbot` 环境；检测到已有 `secretary` Colima 环境时继续复用其中的数据。显式设置的远程地址或其他不可用环境会提示检查，不自动替换。

```sh
make docker                 # 只准备 Docker 环境
make status                 # 查看服务状态
make logs                   # 持续查看日志，Ctrl+C 退出
make logs SERVICE=backend   # 只看后端日志
make restart                # 重启现有容器
make stop                   # 停止服务，保留容器和数据
make down                   # 移除容器，保留数据卷
make help                   # 查看命令说明
```

打开 **http://localhost:5173**，使用本地 `deploy/.env` 中的 `ADMIN_PASSWORD` 登录。新安装的 Compose 项目名为 `catbot`。从旧版本升级时，初始化脚本会在 `deploy/.env` 补写 `COMPOSE_PROJECT_NAME=secretary`，继续使用原数据卷、SDK 会话和 QQ 登录；不要为了改显示名称手动修改这个部署标识。自定义项目名也会保留。初始化脚本不会覆盖已有密钥。请备份 `MASTER_KEY`；丢失后无法解密已保存凭证。

1. 打开「系统与凭证 → 模型接入」，或左侧「模型配置」。
2. 添加模型配置，选择 API 或 SDK、模型及 Base URL；可选用已有凭证，或在配置对话框内添加 Key 并自动选用。
3. 在「对话」新建会话，选择人格与模型配置。
4. 让秘书调用 `example__echo` 验证工具链。
5. 在「邮箱监听」配置、测试并启用 IMAP，选择接收提醒的 QQ 或 Web 会话。
6. 在「插件」配置视频能力；上传文件后创建后台解析任务。

部署目录与迁移说明见 [部署说明](deploy/README.md)。完整设置、通知失败处理和实际账号验收见 [日常使用指南](docs/daily-use.md)。

**CodeBuddy iOA + `glm-5.3` 已完成真实流式对话、原生会话续接和示例插件调用联调（2026-09-30），OneBot QQ 群聊已完成真实收发、上下文和定时通知联调（2026-10-07）。** Claude、Codex、API 直连端点、真实邮箱与官方 QQ 仍需凭证联调。以 `[本地验收]` 或 `[浏览器验收]` 命名的配置连接确定性测试端点，不能作为真实模型能力或效果证明。详见 [验收报告](docs/acceptance.md)。

## 接入矩阵

QQ 保留 `official`、`onebot` 两个逻辑通道键，具体收发实现由启动入口注入。`onebot` 默认连接 NapCat，也能连接其他兼容 OneBot 11 的服务。两种通道可同时启用；模型策略、人格和插件共用，会话保存渠道来源以路由回复和定时通知。非 OneBot 框架只需实现 `message.Sender` 和接收适配，再替换注册代码，见 [消息适配器开发与替换](docs/message-adapters.md)。

个人 QQ 接入：

1. `make` 后打开 [NapCat 登录页](http://localhost:6099/webui)，用 `deploy/.env` 中的 `NAPCAT_WEBUI_TOKEN` 登录页面，再扫码登录秘书 QQ。
2. 打开 [管理端](http://localhost:5173) →「系统与凭证」→「个人 QQ」，刷新连接并填入当前登录 QQ。
3. 填写允许联系人的另一个 QQ 号，选择模型配置及人格，开启接收并保存。
4. 从允许的联系人账号向秘书 QQ 发私聊；会话及执行记录会同步出现在 Web。

OneBot 通道支持私聊文本和已授权群中的 @ 文本消息。群按群号及发言人隔离上下文，并使用显式配置工具的群人格；提醒回原群并 @ 发起人。默认拒绝未绑定联系人、未授权群和未 @ 机器人的群消息；QQ 图片、文件、语音暂不接入解析插件。NapCat WebUI 健康只代表页面可打开，实际登录和渠道状态以管理端为准。详细配置、升级及排错见 [QQ 运维说明](docs/operations.md#qq)。

| 方式 | 实现 | 工具循环归属 |
|---|---|---|
| SDK | CodeBuddy Agent SDK / Claude Agent SDK / Codex SDK | 官方 SDK |
| API | OpenAI Chat Completions / Responses / Anthropic Messages | Go Direct 执行器 |

API 配置支持 `credentialId + baseUrl + model + protocol`。Key 先写入加密凭证库，公开配置只保存引用。CodeBuddy 直连使用该端点实际支持的兼容协议。

CodeBuddy 属于 SDK 提供商，不是接口协议。使用 CodeBuddy Agent SDK 时选择对应的 SDK 接入方式；使用 CodeBuddy 提供的兼容 API 时，按端点实际支持的 OpenAI 或 Anthropic 协议配置。

OpenAI 兼容地址一般以 `/v1` 结束；框架追加 `/chat/completions` 或 `/responses`。Anthropic 地址可填写服务根地址或 `/v1`。不要填写完整接口路径或包含 Key 的查询参数。

SDK 使用独立 HOME、工作目录和原生会话数据。SDK 中断后停止自动续用不确定会话，后续请求从公共历史建立上下文。普通成功会话支持各 SDK 自己的会话恢复。SDK 内置文件、Shell 等工具被限制，业务能力经核心 MCP 网关统一授权。

仓库：[xingexin/catbot](https://github.com/xingexin/catbot)。管理端使用黑白为主、浅粉点缀的配色；显示名称与底层兼容标识分开。

## 项目结构

```text
cmd/catbot/          Go 进程入口
internal/bootstrap/ 依赖装配、通道注册、启动和关闭
internal/config/    环境配置读取与启动校验
internal/domain/    领域模型、纯规则、类型化仓储和 Agent 工具循环
internal/biz/       对话、任务、插件、消息等用例编排
internal/infra/     JSONB 底座、模型协议、SDK Bridge、消息适配器和外部设施
internal/transport/ 管理 HTTP/SSE、MCP、插件宿主协议入口
internal/worker/    Temporal、即时对话队列、恢复扫描
runtime/src/        SDK 服务、契约、注册表和三家 Provider
packages/plugin-sdk/ TypeScript 插件开发包
packages/plugin-sdk-go/ Go 插件开发包（MCP stdio）
plugins/            TS / Go 示例、IMAP 邮箱、视频解析
web/                React + Ant Design 管理端
scripts/            开发工具与集成验收脚本
deploy/             Docker、Compose、环境配置、Nginx 与部署脚本/测试
```

本次目录迁移与验证记录见 [重构验收](docs/refactor-acceptance.md)。

Go 插件可用 `node scripts/create-plugin.mjs notes-go --language go` 创建，再用 `make build-plugins` 编译；原有 TS 插件继续可用。Go 示例 `example-go` 构建后随服务启动登记，默认停用，可在管理端配置授权后启用。Docker 镜像会构建对应 Linux 平台的插件二进制，具体打包、授权和宿主接口见 [插件开发](docs/plugins.md)。

参见 [架构与恢复语义](docs/architecture.md)、[API 与配置](docs/api.md)、[插件开发](docs/plugins.md)、[运维与联调](docs/operations.md)。

面试或项目展示可按 [演示场景](docs/demo.md) 依次演示策略切换、插件扩展、邮箱日程、视频处理与重启恢复。

## 本地开发

```sh
npm ci
npm run build
make build-plugins
./deploy/scripts/compose up -d postgres temporal
```

在 Go 进程环境设置 `DATABASE_URL`、`MASTER_KEY`、`ADMIN_PASSWORD`、`RUNTIME_TOKEN`。数据库端口为 `5442`，Temporal 为 `7233`。随后分别运行：

```sh
go run ./cmd/catbot
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

- 单用户、单管理员、单个 Go 服务实例；QQ 支持官方机器人和一个 NapCat 个人账号的私聊。
- 插件是经过管理员信任的本地代码，独立进程不等同于操作系统沙箱。
- 邮箱支持 TLS IMAP、定时增量监听与提醒、搜索、摘要、事项提取、标记已读；不包含 SMTP 发送。
- 视频支持上传文件、音频转写、抽样画面分析。默认 100 MB / 30 分钟，不抓取登录网站。缺少音轨或模型能力会失败并说明原因。
- 视频时间依据为抽样画面时间；转写端点只返回文本时，不伪造逐字时间戳。
- 公共上下文使用有界历史与早期对话摘录；长期记忆、知识库可作为插件添加。
- 外部消息无法保证绝对只发送一次；结果不明会保留 `uncertain`。
