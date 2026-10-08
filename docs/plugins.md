# 插件开发

业务能力通过独立 Node.js 进程提供 MCP stdio 服务。Go 核心无需重新编译。清单由宿主加载，工具 Schema、权限、超时和版本决定可执行范围。

## 创建与构建

```sh
node scripts/create-plugin.mjs notes
npm install
npm run build -w plugins/notes
```

新目录包含 `plugin.json`、`package.json`、`tsconfig.json` 和 `src/index.ts`。构建器将依赖打包成 `dist/index.cjs`；运行时不依赖共享 node_modules。

本地开发直接在 Web 注册 `notes`。Docker 环境先复制独立包：

```sh
./deploy/scripts/compose cp ./plugins/notes backend:/app/plugins/notes
```

随后 Web「注册 / 更新插件」填写 `notes`。注册时宿主把包复制到持久化目录并固定摘要，之后重建容器不会丢失已注册包。

## 清单

```json
{
  "id":"notes",
  "name":"便签",
  "version":"1.0.0",
  "entry":"dist/index.cjs",
  "configSchema":{
    "type":"object",
    "properties":{"prefix":{"type":"string"}}
  },
  "tools":[{
    "name":"save",
    "description":"保存命名便签",
    "inputSchema":{
      "type":"object",
      "properties":{"name":{"type":"string"},"text":{"type":"string"}},
      "required":["name","text"],
      "additionalProperties":false
    },
    "permissions":["storage"],
    "timeoutSec":15,
    "retrySafe":true
  }]
}
```

工具完整名为 `notes__save`，不超过 64 字符。输入 Schema 必须是对象。可声明 `outputSchema` 和顺序任务 `templates`。

配置中 `format: "password"` 的字段由服务端转存加密凭证，列表仅返回引用。插件接收合并后的配置；禁止自行把密码写日志、返回值或模型提示词。

## 实现工具

```ts
import { serve } from "@catbot/plugin-sdk";

serve({
  save: async (args, { host, signal, operationId }) => {
    // 按名字覆盖是幂等写入；追加型外部操作应传递 operationId。
    await host.set("note:" + args.name, { text: args.text }, signal);
    return { saved: true, name: args.name, operationId };
  }
}).catch(error => {
  process.stderr.write(String(error) + "\n");
  process.exitCode = 1;
});
```

stdout 专用于 MCP，业务日志写 stderr。宿主提供的 `AbortSignal` 必须传递到网络和子进程。不要使用 Shell 拼接用户输入，视频插件示例使用 `execFile` 参数数组。

## 宿主能力

| SDK 方法 | 所需授权 | 用途 |
|---|---|---|
| `host.get/set` | storage | 插件私有 KV |
| `host.save` | storage | 保存解析结果 |
| `host.download/upload` | files | 读取上传文件、保存附件 |
| `host.generate` | models | 使用配置引用请求文本或图片模型 |
| `host.transcribe` | models | 使用 OpenAI 兼容转写端点 |
| `host.task(task, operationId, signal)` | tasks | 幂等提交后台任务 |
| `host.notify(sessionId, text, operationId, signal)` | notifications | 向已存在的 Web/QQ 会话通知结果 |

模型调用由宿主解析凭证，业务插件不需要各家模型或 Agent SDK。`host.generate` 目前要求 API 类型配置；转写要求 OpenAI 兼容协议并显式指定转写模型。

`host.notify` 从 SDK 1.1.0 提供，需要在工具的 `permissions` 声明 `notifications`，再由管理员授权。它复用统一投递记录、QQ 联系人绑定和结果不明保护；不允许把内部任务会话作为接收人。同一插件重复使用相同操作 ID 和内容只会保留一条通知，改接收会话或改内容必须使用新操作 ID。结果不明时不能通过换 ID 自动重发；明确失败后可在管理端手动重试。

```ts
const delivery = await host.notify(args.sessionId, "解析完成，请查看结果", operationId + ":result", signal);
// Web 为 saved，平台确认发送为 sent；失败会抛错并保留投递记录。
```

SDK 1.1.0 自动把当前工具操作 ID 作为 `X-Secretary-Operation-ID` 元数据传给宿主，用于关联模型调用记录；该字段不参与权限认证或模型调用去重。操作 ID 最多 512 字节，不要包含密码或邮件正文。同一工具内的多次生成、转写请求使用相同关联 ID，各自保留独立记录。

`host.generate/transcribe` 在请求模型前保存调用记录，包含插件版本、模型配置、状态、耗时及实际返回的用量，管理员可通过 `GET /api/model-calls` 或「运行记录 → 插件模型调用」查询。记录不保存凭证、输入或输出正文。`usage=null` 表示用量未返回，不代表零消耗；生成接口记录已解析的用量字段，转写接口保留返回的 `usage` 结构，不推算缺失用量或成本。主机重启时未完成记录标为 `interrupted`，不会因此自动重发模型请求；插件自行连接外部模型的调用不在记录范围内。

## 重试、更新和停用

`retrySafe=true` 是插件作者对操作可重复性的声明；框架不会自动把任意写操作变成幂等。外部写入使用稳定 `operationId`，或采用设置最终状态的幂等写法。

框架会保存调用开始与完成状态。非重试安全操作若此前已开始但没有确认结果，再次提交同一操作会报告不确定状态。结果已经确认时直接返回此前结果。

修改任何包文件都需要提升 `plugin.json` 的版本。相同版本不能覆盖不同内容；冻结包不自动删除。停用后新执行不再获得该插件，依赖它的任务暂停；已经开始的任务继续使用固定版本。重新启用不会擅自恢复先前暂停的日程，应由用户在任务页恢复。

插件运行在独立进程，但与宿主共享容器操作系统权限。首版不支持运行来源不明的任意插件；需要多租户时应增加容器或微虚拟机沙箱。
