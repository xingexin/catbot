# 联网搜索插件

`web-search` 是独立 Node 插件，通过 MCP stdio 提供两个工具：

| 工具 | 用途 | 服务端授权 |
| --- | --- | --- |
| `web-search__search` | 搜索公开网页，返回标题、URL、摘要和查询时间 | `web.search` |
| `web-search__read` | 读取给定网页的静态正文、标题和来源 | `web.read` |

API 直连、CodeBuddy、Claude 和 Codex 使用同一套业务工具。SDK 原生 WebSearch / WebFetch 仍保持禁用，权限、版本和调用记录继续由 catbot 管理。插件不依赖模型提供商，也不修改人格或群聊会话的隔离规则。

## 配置与使用

1. 执行 `make` 构建启动后，在管理端「插件」找到「联网搜索」。首次登记默认停用，不自动授权。
2. 点击配置，选择 Tavily 或 Brave Search，填写对应搜索服务的 API Key。Key 使用现有凭证库加密保存，插件记录和任务快照只保存引用。搜索 Key 与模型 Key 不通用；两家服务的 Key 独立保存，切换服务不会复用另一家的凭证。
3. 勾选所需的 `web.search`、`web.read` 授权并保存，启用插件。
4. 在需要使用联网能力的人格中勾选 `web-search__search` / `web-search__read`。群聊仍须显式配置人格工具；不同模型无需重复接入插件。
5. 在 Web 或 QQ 对话里请求“搜索 Temporal Schedule 的文档并附上链接”，或“阅读 https://example.com 并概括内容”。搜索后可由 Agent 按需继续调用网页读取。

暂时没有 Key 时，可以只授权并使用 `web.read`。`search` 会在联网前明确报告缺少凭证，不返回伪造结果，也不会自动切换搜索服务。当前没有为 Tavily、Brave 提供登录代办或自动购买。

配置字段均可留空以使用默认值：

| 字段 | 默认值 | 含义 |
| --- | --- | --- |
| `provider` | `1` | 固定整数枚举：0 未知并拒绝，1 Tavily，2 Brave |
| `tavilyApiKey` | 无 | 仅用于 Tavily；网页读取不需要 |
| `braveApiKey` | 无 | 仅用于 Brave；网页读取不需要 |
| `maxResults` | 5 | 最多搜索结果数，1–10；工具参数只能请求更少的条数 |
| `timeoutSec` | 15 | 单次请求总超时，1–60 秒，包括 DNS、重定向和读取 |
| `maxResponseBytes` | 1048576 | 响应体字节上限，65536–2097152 |
| `maxTextChars` | 12000 | 网页正文字符上限，1000–30000；超出返回 `truncated: true` |

本版使用 [Tavily Search 官方接口](https://docs.tavily.com/documentation/api-reference/endpoint/search) 的 basic 搜索和 [Brave Web Search 官方接口](https://api-dashboard.search.brave.com/app/documentation/web-search/get-started)。请求固定发往所选提供商，不开放模型控制的搜索服务地址。搜索声明 `retrySafe: false`，插件及后台任务均不自动重试收费请求；模型或用户再次发起新的搜索调用仍可能产生新的查询计费。网页读取声明 `retrySafe: true`。

## 网页读取边界

- 只访问公开 HTTP/HTTPS 地址，禁止 URL 内嵌账号密码、内网、回环、链路本地及保留 IP；所有 DNS 结果及每次重定向都校验，连接固定到已校验的地址。
- 不携带搜索 API Key 读取网页。搜索 API 的重定向不跟随，网页最多跟随 3 次重定向。
- 支持静态 HTML、XHTML 和纯文本；不执行 JavaScript、不登录、不访问本地文件，不解析 PDF、图片或视频。需要登录、脚本渲染或反爬验证的页面可能无法读取。
- 有总超时、响应大小和输出长度限制，取消执行会中止网络请求。提取时移除脚本、表单、导航等内容；正文是尽力提取，不保证覆盖页面所有可见内容。请求使用 identity 编码；若网站强制返回压缩响应，本版明确报错，避免无界解压。
- 返回 `untrusted: true`。标题、摘要和正文均为外部资料，不作为指令或权限依据。工具说明要求仅发送必要关键词、不要上传无关群聊和隐私，并在回答中引用来源。这是提示约束，不是内容泄露的绝对保证。
- `web.search` / `web.read` 是宿主调用授权，独立进程不是操作系统网络沙箱；其他管理员安装的插件不自动受本插件 URL 校验约束。

## 开发与安装

```sh
npm ci
npm run build -w @catbot/plugin-sdk
npm run build -w plugins/web-search
npm test -w plugins/web-search
```

不重编译核心也可安装：将完整 `plugins/web-search` 插件包复制到服务的 `PLUGIN_DIR/web-search`，在管理端「注册 / 更新插件」填 `web-search`。Docker 运行时可执行：

```sh
./deploy/scripts/compose cp ./plugins/web-search backend:/app/plugins/web-search
```

独立包入口 `dist/index.cjs` 已打包依赖，运行无需共享 node_modules。升级代码需要提升 `plugin.json` 和 `package.json` 的版本号；执行中的任务保留原包快照。

## 验证范围

自动化检查覆盖两个搜索提供商的请求与响应协议、空结果与畸形响应、缺失 Key、鉴权失败与限流、无回退、输入与输出限制、DNS 与重定向校验、超时取消、正文提取和真实 MCP 子进程的发现及错误返回。搜索提供商通过隔离测试响应验证；没有 API Key 时，不将这些测试描述为真实搜索服务联调。

2026-10-13 验证记录：联网搜索插件 32 项测试通过；全 Node 工作区测试通过（3 项既有可选测试跳过），完整工作区构建通过；Go bootstrap/plugin 相关并发测试与 36 项部署脚本测试通过。最终打包插件通过真实 MCP 子进程读取 `https://example.com`，返回标题和正文。Tavily / Brave 尚无 Key，真实搜索联调待配置后完成。本次未发送 QQ 消息，未调整现有人格授权。
