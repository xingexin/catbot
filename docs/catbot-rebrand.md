# catbot 改名与界面验收

日期：2026-10-08。

## 改动

- GitHub 私有仓库已由 `xingexin/agentTest` 改为 `xingexin/catbot`，本地 origin 同步更新。本地工作目录为 `GolandProjects/catbot`，GoLand 项目与模块名也已同步。
- Go module 使用 `github.com/xingexin/catbot`，启动入口为 `cmd/catbot`；npm 工作空间使用 `@catbot/*`。
- 管理端统一为 catbot：黑白为主、浅粉点缀，包含猫标识、favicon、登录页、导航、对话、表格与表单。宽屏对话侧栏展示实际人格、任务和模型；小屏保留会话选择。
- 新安装使用 `catbot` 部署名；旧安装通过 `.env` 的 `COMPOSE_PROJECT_NAME=secretary` 复用现有卷。既有默认人格 ID、MCP 名称、会话与鉴权协议标识保留兼容。

## 验证

- Go 并发测试、`go vet ./...` 与二进制构建通过；使用独立测试库及任务队列的 PostgreSQL/Temporal 集成测试通过。
- npm 各工作空间构建、SDK 执行服务、插件 SDK、邮箱和视频插件现有测试通过；Web API 测试通过。视频测试中的真实 FFmpeg 条件用例在单元测试环境未运行。
- 12 项部署配置测试通过，覆盖新安装、旧环境迁移、自定义部署名、原卷选择与 Compose launcher。
- 使用实际浏览器检查登录、对话、模型配置及 SDK 表单、人格和任务管理；检查 1440px 宽屏信息栏和 390px 小屏会话选择。未用模拟数据替换业务页面。
- Docker 镜像构建完成，已更新本机 runtime、backend、web。原 PostgreSQL、Temporal、NapCat 未重启。
- 部署前后逐一核对：原 21 个会话、18 个人格、29 个任务、12 个模型配置、1 个凭证引用及 3 个插件 ID 全部保留；QQ 绑定未改变，个人 QQ 通道仍在线。验证后另新增 1 个明确命名的 Web 验收会话。

本地证据：`data/acceptance/catbot-rebrand-before.json`、`catbot-rebrand-after.json`、`catbot-login.png`。

## 本机网络验证

本轮发现 Docker 的 DNS 上游不可用，导致依赖构建与 CodeBuddy 域名解析失败。构建通过临时 `build.extra_hosts` 使用宿主机刚解析的地址完成，没有将固定公网 IP 写入项目源码。首次真实对话记录为 `interrupted`，实际错误为 `getaddrinfo EAI_AGAIN wb.tencentbuddy.com`；MCP 插件入口已成功连接，不将这次调用记为成功。事件记录位于 `data/acceptance/catbot-live-events.json`。

随后使用一次性容器验证可用 DNS，将本机 `compose.override.yaml` 中 runtime/backend 的 DNS 设置为 `119.29.29.29`。此文件不进 Git，`make` 和 Compose 自动读取；没有修改 Colima 的全局网络或重新登录 QQ。运行容器访问 CodeBuddy HTTPS 返回 200。

复测 `run-d817fad7-dcd7-4969-a623-09bae7142612` 成功完成：CodeBuddy `glm-5.3` 经业务 MCP 实际调用 `example__echo`，工具成功返回 `catbot-ready`，并流式输出米雪儿人格问候。模型未在最终问候中再次逐字重复标记，结果检查以真实工具事件为准。这次没有给 QQ 发测试消息，也没有创建提醒。完整记录：`data/acceptance/catbot-live-success.json`。

## 本地目录迁移补充

本地目录已实际从 `GolandProjects/agentTest` 移至 `GolandProjects/catbot`，没有保留旧名目录或软链接。迁移前后核对 Git 状态、HEAD、凭证文件及本机配置，原有未提交改动完整保留。GoLand 的模块文件、项目名和缓存路径已同步。

从新目录重建 NapCat 和测试 fixture，两个主机绑定挂载均指向 catbot。NapCat 使用新增的可选 `NAPCAT_ACCOUNT` 参数，从原 QQ 数据卷恢复同一账号快速登录，管理 API 确认在线。`make status` 和 12 项部署测试通过。其他业务容器及数据卷保持原部署身份。

当前 Codex 保存的项目入口仍指向旧目录，需要在应用中重新打开 catbot 目录。
