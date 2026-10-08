# 运维与真实服务联调

## 部署与持久化

`deploy/compose.yaml` 包含 PostgreSQL 17.6、Temporal 1.29.1、Go 后端、Node SDK 服务和 Nginx Web。PostgreSQL 同时保存业务库与 Temporal 库。数据库、附件/插件包、SDK 会话目录和 QQ 登录状态各自保存在命名数据卷。

默认仅发布到本机回环地址：

| 端口 | 用途 |
|---|---|
| 5173 | Web |
| 5442 | PostgreSQL |
| 7233 | Temporal |
| 6099 | NapCat 登录与连接管理（仅绑定 localhost） |

公网 QQ 回调需要自己的 HTTPS 域名与反向代理。开启 HTTPS 后设置 `COOKIE_SECURE=true`。不要对公网暴露 `/internal`、数据库或 Temporal 原生端口。

```sh
make
make status
make logs SERVICE=backend
make down
```

`down` 保留数据卷；不要在需要保留数据时使用 `down -v`。备份需要同时保存数据库、app-data、sdk、napcat-qq 卷、`data/napcat/config` 和 MASTER_KEY。插件包与凭证历史快照暂不自动清理，避免删除在途任务依赖。

Nginx 通过 Docker 内置 DNS 定期刷新 backend 地址，避免 `make` 重建后端后继续代理到旧容器 IP。配置使用变量形式的 `proxy_pass` 和 5 秒 DNS 缓存；重建期间仍会有短暂不可用，不是不中断切换。参考 [Nginx resolver](https://nginx.org/en/docs/http/ngx_http_core_module.html#resolver)。

新安装默认使用 Compose 项目名 `catbot`；当前机器沿用已有 Colima profile `secretary` 和 Compose 项目 `secretary`，保留原数据。默认 `make` 已包含 Docker 环境启动，也可以单独准备环境：

```sh
make docker
make
```

部署文件统一位于 `deploy/`：`Dockerfile`、Compose 基础/测试文件、私密 `.env`、本机 `compose.override.yaml`、`nginx/nginx.conf`、启动脚本及部署测试。源码开发和真实联调脚本保留在根 `scripts/`。

升级时初始化脚本会把旧根目录 `.env` 和 `compose.override.yaml` 原样移入 `deploy/`，保留权限；发现新旧两处同时存在任一文件时，在任何迁移前报错，不覆盖或合并配置。部署启动器总是指定根目录 `--project-directory`、`--env-file deploy/.env` 和 `-f deploy/compose.yaml`，并自动加载存在的 `deploy/compose.override.yaml`。因此 NapCat 的 `./data/napcat/config`、测试 fixture 的 `./scripts/fixture-model.mjs` 仍从项目根解析。额外测试服务用 `./deploy/scripts/compose -f deploy/compose.test.yaml up -d fixture`。

`make` 在启动 Compose 服务前初始化配置。已有 `deploy/.env` 未设置项目名时，脚本补写 `COMPOSE_PROJECT_NAME=secretary`；新生成的 `deploy/.env` 使用 `catbot`。`deploy/scripts/compose` 在初始化之前也会按同一规则解析项目名，避免检查命令误选新项目。已有自定义项目名保留；环境变量显式覆盖仍然有效。

Compose 项目名决定数据卷前缀，不能把它当作界面标题随意修改。数据库名、默认人格 ID、Temporal 队列、MCP 名称等兼容标识也不因品牌改名迁移。`./deploy/scripts/compose ps`、`exec` 和 `cp` 会选择当前配置的项目，运维脚本不再固定 `secretary-backend-1` 一类容器名。

新建项目 Colima 环境默认磁盘 60 GB（镜像、QQ 客户端和构建缓存需要空间）。已有 20 GB 环境不会在每次启动时强制改动；若数据库日志出现 `No space left on device`，可执行 `colima --profile secretary stop`，再 `colima --profile secretary start --disk 60` 后运行 `make`。扩容保留数据卷，不需要删除数据库或登录状态。

## SDK

锁文件当前使用：

- `@tencent-ai/agent-sdk 0.3.268`（固定版本；旧版 0.1.30 的会话恢复会误返回历史结果）
- `@anthropic-ai/claude-agent-sdk 0.2.141`
- `@openai/codex-sdk 0.107.0`

执行服务为各 SDK 设置独立 HOME，不读取开发机其他项目或浏览器中的登录信息。可以在凭证库保存对应官方 SDK 支持的 Key。订阅账号是否能用某种认证方式，以厂商 SDK 条款和文档为准；不能把通用兼容 Key 当作三家 SDK 通用凭证。

Claude 适配使用 `ANTHROPIC_API_KEY / ANTHROPIC_BASE_URL`。CodeBuddy 适配使用 SDK 支持的环境配置；按账号区域设置 `CODEBUDDY_INTERNET_ENVIRONMENT`。Codex 使用构造参数 `apiKey / baseUrl`。原生会话目录在 SDK 数据卷内按提供商分开。

CodeBuddy iOA 接入：在 `deploy/.env` 设置 `CODEBUDDY_INTERNET_ENVIRONMENT=ioa` 后执行 `make`。打开「系统与凭证 → 模型接入」（或左侧「模型配置」），添加配置并选择 CodeBuddy Agent SDK；在配置对话框内添加 iOA Key 后自动选用，也可选用已保存的凭证。保存后的配置使用 `kind=sdk / provider=codebuddy`；GLM 5.3 的模型 ID 为 `glm-5.3`，Base URL 留空使用官方路由。`glm5.3` 会收到模型不存在的 400 响应。

「接口协议」用于模型 API 直连，CodeBuddy Agent SDK 在 SDK 接入方式中选择。若连接 CodeBuddy 的兼容 API，则根据端点支持的协议选择 OpenAI Chat Completions、Responses 或 Anthropic Messages。

本项目提供真实联调脚本 `node scripts/codebuddy-smoke.mjs`，默认使用配置 `codebuddy-ioa-glm53`（可用 `TEST_CODEBUDDY_CONFIG` 指定）。它创建仅授权回显工具的测试人格与会话，验证流式回复、原生会话续接及实际工具事件，将报告保存到 `data/acceptance/codebuddy-live-report.json`。脚本使用服务端已保存的凭证，不读取或输出 API Key；运行会实际调用模型。

CodeBuddy 的业务 MCP 使用 `alwaysLoad: true`。SDK 默认延迟加载 MCP，而本框架禁用 SDK 内置工具搜索；缺少此设置时可能出现 MCP 显示已连接但模型请求没有工具的情况。判断工具调用成功应检查 Go 网关的 `tool.completed` 事件，不能只看模型的回复。

对三家 SDK 分别完成：

1. 创建配置与会话，普通回复成功。
2. 调用同一 `example__echo`，检查 Go 运行事件中的工具结果。
3. 继续同一会话，检查原生会话 ID 复用。
4. 切换人格或策略，确认新的原生上下文。
5. 取消运行、终止运行服务后检查错误与中断记录。

不会因为 SDK 认证失败而自动改用 API 直连。

## QQ

两种通道可同时工作。Web「系统与凭证」分别显示通道和实际接入实现；会话来源决定回复和定时通知的路由。业务发送依赖 `message.Sender`，接收统一为 `InboundMessage`。更换实现只改启动依赖与注册，见 [消息适配器开发与替换](message-adapters.md)。

### 个人号：NapCat / OneBot 11

默认 `make` 启动固定版本 `mlikiowa/napcat-docker:v4.18.28`，支持本机 ARM64。`make docker` 仍只准备 Docker 运行环境。

`deploy/.env` 的 `NAPCAT_ENABLED` 默认 `true`。设为 `false` 后 `make` 不启动本地 NapCat，并停止已运行的 NapCat 容器，保留配置与数据卷；其余服务照常启动。`ONEBOT_URL` 可指向另一个兼容服务。空值在 NapCat 启用时使用容器地址，关闭时表示不接入 OneBot。管理端在未使用本地 NapCat 时隐藏本地登录入口。Compose 的 NapCat profile 由 `deploy/scripts/compose` 自动选择。

1. 打开 `http://localhost:6099/webui`，使用 `deploy/.env` 的 `NAPCAT_WEBUI_TOKEN` 登录。扫码登录作为秘书的 QQ。
2. 打开秘书管理端「系统与凭证」的个人 QQ 卡片，刷新连接，点击「填入当前登录 QQ」。
3. 填写允许联系人的 QQ 号（用另一个账号给秘书发私聊），选择模型配置和人格，打开启用开关并保存。
4. 从允许的联系人发送文本；在 Web 对话和运行记录中查看执行，在投递记录中查看 `provider=onebot`、状态与平台消息 ID。

首次扫码登录成功后，可在 `deploy/.env` 设置 `NAPCAT_ACCOUNT=机器人QQ号`。容器重建时会尝试使用原数据卷中的登录状态快速登录；登录态失效时仍需扫码。留空则启动二维码登录。

`ONEBOT_TOKEN` 是框架与 NapCat 之间的机器凭证，`NAPCAT_WEBUI_TOKEN` 是 NapCat 管理页面的初始密码，两者不同于秘书管理员密码。初始化脚本只补充缺失项，保留已有 MASTER_KEY、管理员密码与 SDK 配置；不打印生成的令牌。

容器内 HTTP API 为 `http://napcat:3000`，不映射到宿主机。事件上报地址为 `http://backend:8080/qq/onebot/events`。两侧使用同一个 `ONEBOT_TOKEN`；上报为 HMAC-SHA1 签名，API 调用为 Bearer token。配置自动写入 `data/napcat/config/onebot11.json`，首次登录后 NapCat 生成 `onebot11_<QQ>.json`。运行状态存入 `napcat-qq` 数据卷。不要给容器增加 privileged 或挂载 Docker socket。

已有 NapCat 或其他兼容 OneBot 11 服务也可使用：在 `deploy/.env` 设置 `NAPCAT_ENABLED=false`、`ONEBOT_URL / ONEBOT_TOKEN`，无需修改 Compose。外部服务启用 HTTP Server 与 HTTP Client，将事件指向本服务 `/qq/onebot/events`，`messagePostFormat=array`。本地 Go 进程需要导出相同环境变量。首版单实例只绑定一个个人 QQ 登录账号。

排错：

- `unavailable`：NapCat 尚未扫码登录、离线或 API/令牌不一致。健康检查只探测 WebUI，无法代表 QQ 在线。
- `disabled`：账号在线，但尚未启用秘书绑定；保存联系人配置后再刷新。
- `account_mismatch`：当前登录账号和绑定不同。确认后重新绑定，旧账号会话不会发给新账号。
- 更换 `ONEBOT_TOKEN` 时，需要同步更新默认与已登录账号的 OneBot HTTP Server / Client token，再重启相关服务；初始化脚本不会覆盖已有账号配置。
- 若在 NapCat 中修改了 WebUI 密码，以当前 `data/napcat/config/webui.json` 为准，`deploy/.env` 中仍是初始值。
- 停用后不再接受新消息或发送通知；已排队或执行的模型任务可在运行记录取消。

支持私聊文本和群白名单内的 @ 文本消息；群聊需要选择使用显式工具清单的群人格，不会默认开放私人秘书的全部工具。群内按发言人分别保存上下文，任务仅能由创建者的原群会话查询和管理；提醒回原群并 @ 发起人。匿名、系统提示、未 @ 机器人及未授权群消息不触发执行。官方 QQ 适配器仍只支持私聊。

混合消息中的非文本片段会标记未解析。字符串 CQ 消息请改为 array 格式。每次最多回复 1800 字，完整结果保存在 Web。网络中断和缺少发送回执会标记 `uncertain`；不会自动再次发送。HTTP 事件推送没有补齐离线消息的保证。

参考：[NapCat 配置](https://napneko.github.io/config/basic)、[官方 Docker 仓库](https://github.com/NapNeko/NapCat-Docker)、[OneBot 11 HTTP 上报](https://github.com/botuniverse/onebot-11/blob/master/communication/http-post.md)。

### 官方机器人

配置 `QQ_APP_ID / QQ_SECRET / QQ_USER_OPENID / QQ_CONFIG_ID`，可指定 `QQ_PERSONA_ID`。OpenID 必须是机器人官方私聊回调中的 `author.user_openid`，不是普通 QQ 号码。

官方后台填写 HTTPS `/qq/webhook` 回调地址，完成签名验证并订阅 `C2C_MESSAGE_CREATE`。非绑定用户消息会忽略，重复消息映射到同一运行。

即时回复尽可能携带原消息 ID，后台通知使用主动消息。平台权限、额度或回复有效期会影响发送；Web 的投递记录显示具体状态。网络中断后不自动重复发送。

参考：[QQ 安全与授权](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/sign.html)、[QQ 官方接口文档](https://bot.q.qq.com/wiki/develop/api-v2/)。

## 邮箱

插件配置 TLS IMAP 主机、端口、用户名和密码/应用授权码。授权 `mail.read` 可搜索；增量同步需要 `storage`；摘要需要 `models + storage` 并设置 `summaryConfigId`；标记已读需要 `mail.write`。

搜索与摘要默认最多 20 封，可调整到 50。单封邮件大于 2 MB 会标记跳过正文，保留可用元数据；复杂附件不解析。正文返回有长度上限，完整业务产物存入结果库。

同步键按主机、账号、文件夹区分，并保存 UIDVALIDITY 与 UID；UIDVALIDITY 改变时重新开始。标记已读必须提供当时的 UIDVALIDITY，避免操作到重新编号后的其他邮件。

## 视频

配置 `transcriptionConfigId`、`transcriptionModel`、`visionConfigId`，授权 `files/models/storage`。上传后使用「解析视频」创建手动任务，再点「立即执行」；也可以从插件模板创建任务。

视频默认 100 MB / 30 分钟；可调整插件 `maxMB / maxMinutes`。更大的文件还需同步调整后端 `MAX_UPLOAD_MB` 和 `deploy/nginx/nginx.conf` 的请求大小限制。转写音频单次限制 25 MB，超出会明确失败。

没有音轨、没有视觉能力或转写失败都会返回错误，不保存成完整解析。已知时间来源为抽样帧；未提供音频时间戳时，无法定位的音频事项应使用 null。

## 已知运行限制

- 首版是单用户模块化服务。JSONB 全量列表与即时运行轮询适合个人规模，数据量扩大后应增加分页、索引查询与队列领取机制。
- SDK 原生循环内部的模型重试和上下文压缩由厂商控制。Temporal 只持久化整轮 SDK Activity 的结果边界。
- 业务插件虽有权限检查，可信插件进程仍可访问其操作系统权限允许的网络和文件；不是恶意代码沙箱。
- 人格参考 AstrBot 的结构思想，自行实现；不复用其服务代码。[AstrBot 仓库](https://github.com/AstrBotDevs/AstrBot)。
- MCP 与 Temporal 的使用边界参考 [MCP 服务开发](https://modelcontextprotocol.io/docs/develop/build-server)、[Temporal Schedule](https://docs.temporal.io/schedule)。

## 升级与网络排错补充

`make` 升级内置插件时，只在自带语义版本更高时注册新版，保留原配置、加密凭证引用、权限和启停状态。执行中的工作继续使用冻结的旧包；不会用新文件覆盖已运行的版本。自定义插件仍由管理页显式注册/更新。

Compose 的单节点 Temporal 地址使用 `passthrough:///temporal:7233`，避免不需要的 gRPCLB/SRV 查询在上游 DNS 不可用时阻塞正常 A 记录连接。业务服务仍通过容器服务名连接，不固定容器 IP。

如果 `npm ci` 出现 `EAI_AGAIN` 或 `Exit handler never called`，先检查日志中依赖域名的 DNS 错误，不要把安装器的最终报错当成依赖代码错误。可以用一次性容器分别验证默认 DNS 与你网络中可用的 DNS。当前机器的独立 Colima `secretary` 环境已设置 Docker 引擎 DNS 为 `223.5.5.5`、`1.1.1.1`，用于绕过失效的 VM 转发器；没有改 macOS 系统 DNS。这是本机配置，不会强制其它部署使用同一解析器。企业私网域名应选择公司网络允许的 DNS。

重启 Colima 后如 Docker 当前上下文被恢复为 `default`，可显式使用 `DOCKER_CONTEXT=colima-secretary make`。数据卷仍保留在原环境，不需要创建新的数据库或复制凭证。

Docker 构建上下文仍是项目根。忽略规则集中在 `deploy/Dockerfile.dockerignore`，根 `.dockerignore` 仅作为兼容符号链接，保证无 BuildKit 的 legacy builder 也排除 `deploy/.env`、本机覆盖文件、数据和依赖目录。不要删除该链接后使用 legacy builder。
