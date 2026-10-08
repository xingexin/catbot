# IMAP 邮箱插件

邮箱页可配置 TLS IMAP 服务器、账号、授权码和权限，保存后启用插件并测试连接。授权码由宿主存入凭证库，配置查询只返回凭证引用；编辑时留空保留已有授权码。邮箱服务需事先开启 IMAP。

监听使用 Temporal 周期检查，默认每 5 分钟查询 INBOX。每个监听有独立的 `monitorId`，由管理端使用任务 ID 填入；同一监听跨运行必须保持该值。收件人选择已有 QQ 或 Web 会话。QQ 通道与联系人绑定必须启用；Web 通知保存在管理端。

| 工具 | 用途 | 权限 |
| --- | --- | --- |
| `test_connection` | 验证 TLS 登录和文件夹访问，返回邮件数与未读数 | `mail.read` |
| `watch` | 增量检查新邮件，返回 `changed` 和 `notificationText` | `mail.read`, `storage` |
| `sync` | 从已有游标继续同步，首次从历史邮件开始 | `mail.read`, `storage` |
| `search` | 搜索邮件正文并读取最近匹配项 | `mail.read` |
| `summarize` | 用 API 模型生成邮件摘要和事项并保存 | `mail.read`, `models`, `storage` |
| `mark_read` | 校验 UIDVALIDITY 后将指定 UID 标记已读 | `mail.write` |

新邮件提醒本身不调用模型。`summarize` 另需配置 `summaryConfigId`，当前使用 API 直连模型。SMTP 发送、回复和复杂附件内容解析未包含在此版本中。

摘要返回 `analysis.summary` 和 `analysis.items`。每个事项包含非空 `title`、`dueAt` 和 `sourceUid`：无法确定截止时间时 `dueAt` 为 `null`，有值时必须是带时区且日历日期有效的 RFC3339 时间；`sourceUid` 必须引用本次实际读取的邮件。模型返回坏 JSON、缺字段、不合法日期或无效来源时，工具明确报错，不保存为成功分析产物。

这些校验保证数据结构、日期格式及来源引用有效，不能替代对模型内容的语义核实。提示词要求原文不足以确定日期、时间和时区时保留 `null`，不编造截止时间。

## 监听行为

- 首次运行默认记录当前最高 UID，不发送历史邮件提醒；明确设置 `includeExisting: true` 时才读取历史邮件。UIDVALIDITY 变化时也按同一规则重新建立基线。
- 游标按服务器、端口、账号、文件夹和监听标识隔离。`sync` 与 `watch` 使用不同游标。
- 每批默认最多 20 封，最大 50 封；`hasMore` 表示后续周期仍有积压。无新邮件时 `changed: false`，通知条件应使用 `${steps.watch.changed}`。
- 通知正文使用 `${steps.watch.notificationText}`，包含发件人和主题；大批次显示前 8 封，完整批次保存在解析结果中。
- 正文最多读取 2 MB 的单封原始邮件，并限制每条正文及整个批次的返回大小。超过限制会标注省略或截断，不声称已解析全部附件。
- 读取使用 IMAP `BODY.PEEK[]`，不会自动标记已读。更改已读状态需要单独授权并调用 `mark_read`。
- 网络、登录、读取、存储失败返回错误，不能作为“没有新邮件”处理。

宿主在不同插件版本之间串行化调用。插件用一次原子的 KV 更新同时保存游标和最近操作结果，替换前归档前一操作结果。进程中断或响应丢失后，相同 `operationId` 会返回原批次，且无需再次连接邮箱。写入失败不会消耗邮件；在保存产物后、提交游标前中断可能留下额外产物记录，通知结果仍可恢复。

游标提交不等同于 QQ 已投递。通知服务单独保存发送状态，明确失败的投递可以人工重试；结果不明的投递保留记录供核对，不自动重复发送。

## 验证

```sh
npm run build -w plugins/mail
node --import tsx --test plugins/mail/test/*.test.ts
```

测试包括游标与 UIDVALIDITY 变化、独立订阅、写入失败、提交后响应丢失、延迟重试、提醒长度，以及摘要/事项的有效结构、非法日期和来源 UID 校验。协议测试需要系统 `openssl`，临时创建本机 TLS IMAP 服务和受信任的测试证书，通过真实 ImapFlow、mailparser 和 MCP stdio 插件进程验证两次新增邮件、无变化轮询、正文解析和已读操作；摘要部分使用固定模型响应，验证合法结果保存及畸形结果拒绝，不能据此证明真实模型的语义质量。测试没有关闭 TLS 校验，也不使用真实邮箱或 QQ 凭证；真实账号联调需另行记录。
