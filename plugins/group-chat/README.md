# 群聊总结插件

`group-chat__summarize` 对**已授权读取并传入的文本**进行分段总结与归并，不接收群号、不读取数据库、不创建任务、不直接发 QQ。它是独立 Node 子进程，使用 MCP stdio；模型请求走宿主 `host.generate`。插件默认登记但停用。

## 配置与启用

1. 在「模型配置」准备一个可用的 **API 直连**配置。宿主生成接口不支持 Agent SDK 配置。
2. 在「插件 → 群聊总结」填写 `summaryConfigId`（上述模型配置 ID），授权 `models` 后启用。
3. 在「系统与凭证 → QQ 设置」开启群聊记录，明确选择采集群。
4. 在群人格的明确工具清单中加入 `system__group_history` 和 `group-chat__summarize`，再将该人格绑定到群会话。插件不会自动更改人格权限。
5. 在该群 @ 机器人请求总结。Agent 先通过框架只读历史工具获取当前群数据，再传入总结插件。插件本身没有群存储权限；它无法证明传入数据来源，不应将它当作授权校验器。

输入携带 `messages`（`sentAt`、`senderId`、可选 `senderName/id/truncated`、`text`）、宿主 `coverage`，以及明确的 `hasMore` 和 `truncated`。合并分页须按消息 ID 去重，保留宿主覆盖和截断信息。`coverage` 至少包括 `availableFrom/availableUntil/localOnly/limited`；可带宿主其他范围字段及 `requestedFrom/requestedUntil`。时间使用有时区的 ISO 格式。可选 `focus` 仅指定关注主题。

默认单次 1000 条、262144 UTF-8 字节、每段 JSON 12000 字节；可配置上限分别为 2000 条、524288 字节和 32768 字节。超过输入限制直接失败，请分批调用并报告各批实际范围，不静默丢弃记录。单次最多 40 个初始分段、64 次模型请求；输出超限、模型错误或取消会返回失败，不将中间片段显示成完整总结。模型调用可能产生费用；失败重试可能再次生成。

输出 `text` 可直接交给 Agent 展示，其中始终说明消息条数、宿主可用范围、本地记录局限和媒体未解析；`coverage/hasMore/truncated/warnings` 同时保留结构化信息。摘要只覆盖调用者提供的数据，宿主可用范围不表示该范围内的每一条都已传入。提示词将引用消息和中间摘要标作不可信数据；这是防护措施，不是对模型永不受提示注入影响的保证。

## 每日摘要模板

模板 `daily_summary` 只有现有任务契约支持的一个 `agent` 步骤和 `notifyText: ${steps.summary.text}`。它要求按 `nextCursor` 分页读取当天历史，再调用总结插件；遇到上限必须说明缺口，不得称为全量。

- 最稳妥的是在目标群 @ 机器人创建每日摘要任务，使 `sessionId` 绑定该群来源；也可在任务表单明确选择该群会话。
- 默认提示词使用 `Asia/Shanghai`。设置任务 `TimeZone` 时保持一致；其他时区须同时修改提示词。模板不自动创建 Schedule。
- 设置周期和 Cron（例如当地每日 23:00）、任务模型与上述群人格，并打开任务通知。通过现有任务 `notify` 发送最终文本，插件不需要 `notifications` 权限。
- 启用记录前、离线或清理过的消息不会自动补齐；截至执行时刻的摘要不包含当天尚未发生的消息。
- 模板的执行 Agent 可选受支持的 API 或 SDK；插件内部 `summaryConfigId` 仍必须是 API 配置。

## 开发与验证

```sh
npm run build -w plugins/group-chat
npm run test -w plugins/group-chat
```

单元测试使用 mock `host.generate` 验证 UTF-8 分段归并、原文保留、范围与截断提示、输入/输出限制、错误与取消传播，以及引用内容的提示词边界。未使用真实模型或 QQ 进行本插件联调。
