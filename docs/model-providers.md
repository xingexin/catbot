# 模型接入：协议、厂商与 SDK

## 配置入口

打开「模型配置 → 添加配置」。接入方式选择「模型 API 直连」后，可以使用服务商快捷配置，也可以保持自定义。快捷配置填写接口协议和 Base URL；模型名称、工具和图片能力由管理员选择。切换到不同服务地址时清空凭证引用，需重新选择对应的 API Key，避免把上一家厂商的 Key 发往新服务。

| 服务 | 快捷配置协议 | 默认 Base URL |
| --- | --- | --- |
| OpenAI / GPT | OpenAI Responses | `https://api.openai.com/v1` |
| Anthropic / Claude | Anthropic Messages | `https://api.anthropic.com` |
| DeepSeek | OpenAI Chat Completions | `https://api.deepseek.com` |
| Kimi / Moonshot | OpenAI Chat Completions | `https://api.moonshot.cn/v1` |
| GLM / 智谱 | OpenAI Chat Completions | `https://open.bigmodel.cn/api/paas/v4` |
| 自定义 | 自选上述三种协议 | 填写服务实际的 API 前缀 |

这些是标准 API 地址，不代表订阅版、Coding Plan、企业网关或其他区域账号共用该地址与凭证。地址可以继续编辑。OpenAI 也可选择 Chat Completions；其他厂商若支持 Responses 或 Messages，也可按其文档手动选择，不由框架推测。

随后选择对应服务的 API Key 凭证，点击「获取模型列表」，搜索并选择服务返回的原始模型 ID。支持手动填写；模型名称不维护一份假定长期有效的固定清单。目录不代表所有模型均能聊天、调用工具或输入图片，也不证明账号拥有调用权限。服务若不支持目录接口，获取失败会给出错误，但不会阻止手工配置模型。

切换快捷配置保留当前模型名称，但跨服务地址会清空凭证引用；请确认模型及新选择的凭证属于该服务。同一服务地址仅修改协议或路径时可保留凭证；切换接入方式或 SDK 提供商时也需要重新选择凭证。修改连接字段后，旧目录清空且迟到响应被忽略；保存仍需点击确认。

## 复用边界

catbot 按协议适配，不按模型名称适配。厂商快捷配置只是前端表单预设，保存的仍是现有 `kind / protocol / baseUrl / model / credentialId`；不增加影响后端路由的品牌字段，不为 DeepSeek、Kimi、GLM 新建重复的执行引擎。

```text
QQ / Web / 后台任务
          ↓
   公共人格、历史、插件工具
          ↓
   执行策略
   ├─ API：Go 管理上下文与工具循环
   │  ├─ Chat Completions → OpenAI 兼容端点
   │  ├─ Responses → 支持该协议的端点
   │  └─ Messages → Anthropic 兼容端点
   └─ SDK：Node 调用各 SDK
      ├─ CodeBuddy
      ├─ Claude Agent SDK
      └─ Codex SDK（GPT）
```

同一协议的新模型通常只需改 Model，同一协议的新服务通常只需改 Base URL、Key 和 Model。存在独有鉴权、输入结构、工具回传或流式事件时，在协议适配边界补兼容或新增适配器；不能静默切换执行策略，也不能把“兼容 OpenAI”理解为支持每一种 OpenAI 接口和能力。

## SDK 模型目录

CodeBuddy 通过 SDK 的 `getAvailableModels()`，Claude 通过公开的 `supportedModels()` 读取目录。目录查询采用独立临时目录，不发送用户提示词，不创建业务会话，不挂载秘书工具；不会读取开发机其他项目的登录态。SDK 可能返回内置或缓存目录，仍需单独验证模型调用权限。

当前固定版本 `@openai/codex-sdk 0.107.0` 没有公开的模型列表方法，因此 Codex SDK 明确使用手动填写。OpenAI API 直连可读取 `/models`，这是另一种接入方式，不会拿它冒充 Codex SDK 的目录。独立 Codex app-server 的目录能力也不是当前 SDK 的同一个接口。

## 思考模型与上下文

Chat Completions 适配器在一次 Agent 运行内，保留服务返回的 `reasoning_content`，连同工具调用和工具结果一起回传。非流式与 SSE 均支持，缺失和 null 不追加字段，显式空字符串原样保留；不把该字段当作用户可见答案。单次模型输出的此字段最多 256 KiB，并计入下一轮请求的输入预算；超过预算明确失败，不丢弃已执行的工具链。

跨用户轮次目前仍复用公共的用户消息、最终回答和历史摘录，不保存完整的厂商思考/工具轨迹。要求跨轮完整思考状态的模型模式尚未完整适配；当前运行内工具回传测试不能证明这种模式的连续对话已通过。特殊的推理强度参数、厂商内置搜索/文件工具也不由快捷配置自动开启。

## 验证范围

快捷配置与三种 API 的目录/工具循环通过本地协议测试。新增厂商的真实 Key、模型权限、连续对话和插件调用需要分别联调；没有凭证时不宣称已真实兼容。SDK 元数据查询成功也不等于实际推理成功。

参考：[OpenAI Responses](https://developers.openai.com/api/docs/guides/migrate-to-responses)、[Anthropic 模型目录](https://platform.claude.com/docs/en/api/models/list)、[DeepSeek 思考与工具回传](https://api-docs.deepseek.com/guides/thinking_mode/)、[Kimi OpenAI 兼容说明](https://platform.kimi.com/docs/guide/migrating-from-openai-to-kimi)、[智谱标准 API 示例](https://docs.bigmodel.cn/cn/best-practice/case/ai-search-engine)。
