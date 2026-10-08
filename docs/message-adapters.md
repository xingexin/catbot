# 消息收发依赖与替换

当前支持私聊文本，以及 OneBot 通道中已授权群的 @ 机器人文本；官方 QQ 通道仍只接入私聊。NapCat 是可选外部服务，不复制其源码。通过普通 Go 接口和启动注入替换依赖；更换后重新编译、重启，不提供跨账号迁移、动态安装或不中断切换。

## 代码边界

```text
外部平台 → 收件适配器（验签、解析）
         → messaging.InboundMessage → biz/messaging.Service.HandleIncoming
         → 联系人或群授权 → 并发去重 → 会话/人格/上下文 → Agent

即时回复 / Temporal Notify → biz/messaging.Service.NotifyRecord
         → DeliverNotification
         → 授权、操作 ID 锁、持久化发送意图
         → messaging.Sender.Send(OutboundMessage)
         → 平台请求 → sent / failed / uncertain
```

| 位置 | 责任 |
|---|---|
| `internal/domain/messaging/message.go` | 独立消息结构、`Sender`、可选 `StatusChecker` |
| `internal/biz/messaging/service.go` | 公共收件、绑定检查、历史兼容、发送去重与结果记录 |
| `internal/biz/messaging/management_http_usecases.go` | 绑定配置与授权、连接状态查询用例 |
| `internal/infra/messaging/onebot` | OneBot 11 HTTP 协议，不依赖 NapCat 专属接口 |
| `internal/infra/messaging/qqofficial` | 官方 QQ API、Ed25519 验签，注入加密 token 缓存 |
| `internal/bootstrap/channels.go` | 创建依赖、固定逻辑路由键、绑定接收回调 |

`OutboundMessage` 包含账号、联系人、可选房间 ID、文本、操作 ID 与可选回复引用。`RoomID` 为空时向 `Peer` 私聊；非空时发往该群，`Peer` 是需要 @ 的发言人。它不包含 `Session`、OneBot action 或 QQ 请求体。发送器不需要实现管理 API；`StatusChecker` 和接收 HTTP handler 分别注入。暂不支持群发送的实现应明确拒绝非空 `RoomID`，不能降级为私聊。

`official / onebot` 是已有数据的逻辑路由键，和 `Implementation` 展示名不同。即使换成自研服务也保留原键，已有 `channelProvider/channelAccount/channelRoom/recipient`、会话历史、人格绑定、任务通知目标和投递记录继续有效。前提是新实现仍使用同一账号、群及联系人标识；切换账号或不同平台 ID 体系需要另外设计迁移。`channelRoom` 为空的旧会话仍按私聊路由，群会话按路由、账号、群与发言人隔离。

## 更换 OneBot 服务

修改项目 `deploy/.env`：

```dotenv
NAPCAT_ENABLED=false
ONEBOT_URL=http://your-onebot-service:3000
ONEBOT_TOKEN=与你的服务一致的令牌
```

执行 `make`。URL 必须能从 backend 容器访问；容器内的 `localhost` 指向 backend 自身。外部服务启用以下能力：

- HTTP API：`get_login_info`、`get_status`、`send_private_msg`，以及使用群聊时的 `send_group_msg`，Bearer token 认证。
- HTTP 事件上报：将事件发往 catbot 的 `/qq/onebot/events`，使用相同 token 生成 `X-Signature: sha1=<HMAC-SHA1(rawBody)>`。
- 消息格式 `messagePostFormat=array`，上报私聊与群消息，关闭自发消息上报；保留群消息中的 `at` 消息段，供适配器判断是否真正 @ 当前机器人。

不需修改 Compose 或业务代码。保持同一登录账号、群与联系人标识后，在管理端刷新连接并检查已有绑定。群权限由 `allowedGroupIds` 和 `groupPersonaId` 单独配置，群人格必须设置明确的工具清单（允许空清单）；群内所有成员都可 @ 机器人，私聊联系人名单不限制群成员。外部服务需要自行部署、登录；catbot 只依赖其协议。

`NAPCAT_ENABLED` 默认 `true`，兼容旧 `deploy/.env`。`ONEBOT_URL` 空值在默认模式下解析为 `http://napcat:3000`；关闭本地依赖后空值表示未配置 OneBot。`make` 在关闭时停止原有 NapCat 容器，保留数据卷和配置文件。重新设为 `true` 后 `make` 可恢复。环境变量优先于 `deploy/.env`，所以 `NAPCAT_ENABLED=false make` 可临时验证。

`deploy/scripts/compose` 负责选择 `napcat` Compose profile；直接调用 Docker Compose 时需自行显式选择 profile。通常使用 `make` 或 `./deploy/scripts/compose`。管理端仅在当前地址指向启用的本地 NapCat 时显示本地登录入口；外部服务显示为「外部 OneBot 服务」。

## 新增自研发送实现

在 `internal/infra/messaging/myqq` 编写实现，下面是依赖底层客户端的接口示意。`Client` 自行实现自研服务的认证、HTTP 请求与回执分类：

```go
package myqq

import (
    "context"
    message "github.com/xingexin/catbot/internal/domain/messaging"
)

type Client interface {
    SendText(ctx context.Context, account, peer, roomID, text, operationID string) (message.SendResult, error)
}

type Adapter struct { client Client }

func New(client Client) *Adapter { return &Adapter{client: client} }

func (a *Adapter) Send(ctx context.Context, in message.OutboundMessage) (message.SendResult, error) {
    return a.client.SendText(ctx, in.Account, in.Peer, in.RoomID, in.Text, in.OperationID)
}

var _ message.Sender = (*Adapter)(nil)
```

然后在 `internal/bootstrap/channels.go` **替换**原来 `onebot` 的创建及注册，而不是重复注册；以下 `messagingbiz` 是 `internal/biz/messaging` 的导入别名：

```go
sender := myqq.New(client) // client 由启动入口创建
return a.Messaging.RegisterChannel("onebot", messagingbiz.Channel{
    Title: "QQ 个人号",
    Implementation: "自研 QQ 服务",
    Sender: sender,
    Binding: a.Messaging.OneBotChannelBinding, // 保留已有授权及人格/配置绑定
    Receive: incomingHTTPHandler,   // 自研协议的验签和解析 handler
    Status: connectionChecker,      // 可选；没有则省略
})
```

底层 `SendText` 根据 `roomID` 选择私聊或群目标；群消息应带有对 `peer` 的真实 @ 消息段。回复和任务通知都携带原会话的目标，不应自行重新选择最近会话或默认联系人。

接收 handler 在完成认证、过滤自发消息与不支持的消息类型后，调用启动时传入的 `a.Messaging.IncomingHandler("onebot")` 回调。私聊传入 `message.InboundMessage{Account: ..., Peer: ..., MessageID: ..., Text: ..., ReceivedAt: ...}`；群消息另外设置 `RoomID` 和 `Mentioned`，其中 `Peer` 始终为发言人。`Mentioned` 必须根据平台的 @ 消息结构判断，只有明确 @ 当前登录机器人才为 `true`，不能用正文出现账号数字代替。宿主再次检查群白名单和 `Mentioned`，未 @ 的群消息不进入 Agent。

回调会覆盖 `Route`，不信任外部事件选择路由。去重要求平台提供稳定消息 ID，不要每次收到重推都生成随机 ID。如果接收协议仍兼容 OneBot，可以保留原 OneBot 接收器，只替换 `Sender`。

连接检查独立实现 `Status(ctx) message.ConnectionStatus`。现有 OneBot 在管理端保存绑定和每次发送前核实实际登录账号。自研实现应同样在发送前校验目标账号；没有状态接口时管理端显示「实现未提供连接检查」，允许管理员明确配置绑定，不把它显示为已验证在线。

无需修改 Agent、上下文、人格、业务插件或 Temporal 通知。参考 `internal/bootstrap/channels_test.go` 的 `TestConversationAndTaskNotificationAcrossSenders`：只替换发送依赖，复用相同聊天与 `Messaging.Notify` 用例路径；替代实现也不能绕过宿主联系人或群授权。

## 结果和恢复约定

- 发送前宿主保存 `uncertain` 意图。`OperationID` 贯穿发送；重试相同操作不会再次调用发送器。
- 明确成功返回 `SendResult{Status: message.Sent, MessageID: ...}, nil`。
- 确定未发出或平台明确拒绝，返回 `message.Failed` 和错误。
- 发送后超时、连接断开、畸形回执或缺少消息 ID，返回 `message.Uncertain` 和错误，不能当作可安全重试的失败。
- 零值状态、未知状态、`Sent` 同时带错误都会被宿主保守记录为 `uncertain`。
- 不自动重试失败或结果不明的操作。排查后主动新建操作 ID 才会再次发送；不承诺外部平台绝对只发送一次。
- 正常传递 `context.Context`，网络请求设置超时，错误与日志不得包含令牌或完整请求认证头。

验证命令：`go test -race ./...`、`go vet ./...`、`python3 deploy/tests/test_deployment.py`、`npm run build -w web`。真实 QQ 账号收发需单独联调，接口模拟测试不能代替平台验收。
