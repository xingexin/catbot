# 运维与真实服务联调

## 部署与持久化

`compose.yaml` 包含 PostgreSQL 17.6、Temporal 1.29.1、Go 后端、Node SDK 服务和 Nginx Web。PostgreSQL 同时保存业务库与 Temporal 库。三个数据卷分别保存数据库、附件/插件包、SDK 会话目录。

默认仅发布到本机回环地址：

| 端口 | 用途 |
|---|---|
| 5173 | Web |
| 5442 | PostgreSQL |
| 7233 | Temporal |

公网 QQ 回调需要自己的 HTTPS 域名与反向代理。开启 HTTPS 后设置 `COOKIE_SECURE=true`。不要对公网暴露 `/internal`、数据库或 Temporal 原生端口。

```sh
./scripts/compose ps
./scripts/compose logs --tail=100 backend runtime temporal
./scripts/compose down
```

`down` 保留数据卷；不要在需要保留数据时使用 `down -v`。备份需要同时保存数据库、app-data、sdk 卷和 MASTER_KEY。插件包与凭证历史快照暂不自动清理，避免删除在途任务依赖。

本次本机验收使用独立 Colima profile `secretary`。若需要：

```sh
colima start secretary
DOCKER_CONTEXT=colima-secretary ./scripts/compose up -d
```

## SDK

锁文件当前使用：

- `@tencent-ai/agent-sdk 0.1.30`
- `@anthropic-ai/claude-agent-sdk 0.2.141`
- `@openai/codex-sdk 0.107.0`

执行服务为各 SDK 设置独立 HOME，不读取开发机其他项目或浏览器中的登录信息。可以在凭证库保存对应官方 SDK 支持的 Key。订阅账号是否能用某种认证方式，以厂商 SDK 条款和文档为准；不能把通用兼容 Key 当作三家 SDK 通用凭证。

Claude 适配使用 `ANTHROPIC_API_KEY / ANTHROPIC_BASE_URL`。CodeBuddy 适配使用 SDK 支持的环境配置；按账号区域设置 `CODEBUDDY_INTERNET_ENVIRONMENT`。Codex 使用构造参数 `apiKey / baseUrl`。原生会话目录在 SDK 数据卷内按提供商分开。

对三家 SDK 分别完成：

1. 创建配置与会话，普通回复成功。
2. 调用同一 `example__echo`，检查 Go 运行事件中的工具结果。
3. 继续同一会话，检查原生会话 ID 复用。
4. 切换人格或策略，确认新的原生上下文。
5. 取消运行、终止运行服务后检查错误与中断记录。

不会因为 SDK 认证失败而自动改用 API 直连。

## QQ

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

视频默认 100 MB / 30 分钟；可调整插件 `maxMB / maxMinutes`。更大的文件还需同步调整后端 `MAX_UPLOAD_MB` 和 `deploy/nginx.conf` 的请求大小限制。转写音频单次限制 25 MB，超出会明确失败。

没有音轨、没有视觉能力或转写失败都会返回错误，不保存成完整解析。已知时间来源为抽样帧；未提供音频时间戳时，无法定位的音频事项应使用 null。

## 已知运行限制

- 首版是单用户模块化服务。JSONB 全量列表与即时运行轮询适合个人规模，数据量扩大后应增加分页、索引查询与队列领取机制。
- SDK 原生循环内部的模型重试和上下文压缩由厂商控制。Temporal 只持久化整轮 SDK Activity 的结果边界。
- 业务插件虽有权限检查，可信插件进程仍可访问其操作系统权限允许的网络和文件；不是恶意代码沙箱。
- 人格参考 AstrBot 的结构思想，自行实现；不复用其服务代码。[AstrBot 仓库](https://github.com/AstrBotDevs/AstrBot)。
- MCP 与 Temporal 的使用边界参考 [MCP 服务开发](https://modelcontextprotocol.io/docs/develop/build-server)、[Temporal Schedule](https://docs.temporal.io/schedule)。
