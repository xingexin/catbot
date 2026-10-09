# 插件开发

业务能力通过独立进程提供 MCP stdio 服务，支持 Node.js 脚本和 Go 编译的二进制。插件语言不影响 Agent、人格、任务或权限逻辑，新增插件不需要修改或重新编译 Go 核心。清单由宿主加载，工具 Schema、权限、超时和版本决定可执行范围。

## 创建与构建

### TypeScript

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

使用 `make test-plugins` 可构建 TS 示例并运行 Go / TS 插件的真实进程互通测试，验证它们共用宿主协议；测试使用本地测试宿主，不调用真实模型，也不发送 QQ 消息。`make test` 会包含这项检查。

### Go

仓库内提供 `packages/plugin-sdk-go`，导入路径为 `github.com/xingexin/catbot/packages/plugin-sdk-go`。Go 插件与主项目共用根目录的 `go.mod`；生成器直接复用 `plugins/example-go` 中的源码和清单，不需要安装 npm 包。

```sh
node scripts/create-plugin.mjs notes-go --language go
sh scripts/build-go-plugins.sh notes-go
```

新目录包含 `main.go`、`plugin.json`，二进制输出到 `dist/plugin`。`make build-plugins` 编译仓库内全部含 Go 源码的 binary 插件；`make build` 同时编译主程序、Go 插件与原有 Node 项目。已有的 TS 创建命令保持不变。

内置 `example-go` 提供 `echo` 和使用宿主私有 KV 的 `note`，与 TS `example` 插件可以同时使用，工具名分别为 `example-go__echo` 和 `example__echo`。构建后重启服务会自动登记这个示例，但默认停用；在 Web「插件」中为它授权 `storage` 并启用，即可使用便签。也可以在运行中的服务里通过「注册 / 更新插件」填写 `example-go`，无需重启。

本地 Go 后端可直接注册构建后的目录；Docker 后端必须使用 **与后端容器相同操作系统和 CPU 架构** 的二进制。`make` 构建镜像时已在 Go 阶段编译仓库内的 Go 插件，所以新源码随镜像构建部署即可；不要求开发机器安装 Go。

需要单独分发时，例如目标容器为 Linux arm64：

```sh
GOOS=linux GOARCH=arm64 sh scripts/build-go-plugins.sh notes-go
./deploy/scripts/compose cp ./plugins/notes-go backend:/app/plugins/notes-go
```

Linux amd64 容器改为 `GOARCH=amd64`。在 macOS 上默认构建的是 Darwin 二进制，不能直接复制到 Linux 容器运行；重新执行不带 `GOOS/GOARCH` 的构建命令可恢复本机版本。构建器默认 `CGO_ENABLED=0`，输出保留执行权限。插件注册冻结包时也会设置二进制入口的执行权限；启动错误会提示后端平台。

同一插件 ID 与版本只能对应一个包内容，包括二进制内容。从本地平台切换到容器平台分发、修改源代码或更新 SDK 后，都应提升版本再重新注册；不要用同一版本覆盖已冻结的包。

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

`runtime` 可选 `node` 或 `binary`，省略时兼容已有 Node 插件。Go 清单将启动字段设为：

```json
{"runtime":"binary","entry":"dist/plugin"}
```

`entry` 始终是包内的相对路径，不能指向包外，也不会作为 Shell 命令执行。Go 构建器要求输出在 `dist/` 内。运行目录是冻结后的插件包，SDK 从其中的 `plugin.json` 读取工具声明。

配置中 `format: "password"` 的字段由服务端转存加密凭证，列表仅返回引用。插件接收合并后的配置；禁止自行把密码写日志、返回值或模型提示词。

## 实现工具

### TypeScript

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

### Go

```go
package main

import (
    "context"
    "fmt"
    "os"

    pluginsdk "github.com/xingexin/catbot/packages/plugin-sdk-go"
)

func main() {
    err := pluginsdk.Serve(context.Background(), map[string]pluginsdk.Handler{
        "save": func(ctx context.Context, args map[string]any, tool pluginsdk.ToolContext) (any, error) {
            name, ok := args["name"].(string)
            if !ok {
                return nil, fmt.Errorf("name must be a string")
            }
            // 与 TS 相同，按名字覆盖是幂等写入；外部追加操作应使用 tool.OperationID。
            err := tool.Host.Set(ctx, "note:"+name, map[string]any{"text": args["text"]})
            if err != nil {
                return nil, err
            }
            return map[string]any{"saved": true, "name": name}, nil
        },
    })
    if err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
}
```

`ToolContext.Config` 是当前调用的配置副本，`OperationID` 是宿主传入的稳定工具操作 ID。把 `ctx` 传递给宿主请求、数据库和外部 HTTP 调用，以遵守取消和超时。stdout 同样只用于 MCP，日志使用 stderr。SDK 检查输入与输出 Schema，并将数组、标量结果包装为 `{"value": ...}`；结果上限 512 KiB，大结果保存为产物并返回引用。

## 宿主能力

| TypeScript 方法 | Go 方法 | 所需授权 | 用途 |
|---|---|---|---|
| `host.get/set` | `Host.Get/Set` | storage | 插件私有 KV |
| `host.save` | `Host.Save` | storage | 保存解析结果 |
| `host.download/upload` | `Host.Download/Upload` | files | 读取上传文件、保存附件 |
| `host.generate` | `Host.Generate` | models | 使用配置引用请求文本或图片模型 |
| `host.transcribe` | `Host.Transcribe` | models | 使用 OpenAI 兼容转写端点 |
| `host.task` | `Host.Task` | tasks | 幂等提交后台任务 |
| `host.notify` | `Host.Notify` | notifications | 向已存在的 Web/QQ 会话通知结果 |

Go 方法首个参数均为 `context.Context`。`Generate`、`Transcribe`、`Upload` 分别接收 SDK 的 `GenerateRequest`、`TranscribeRequest`、`UploadRequest`；`Download` 返回 `[]byte`。任务与通知需要显式传入稳定操作 ID：

```go
result, err := tool.Host.Generate(ctx, pluginsdk.GenerateRequest{
    ConfigID: "model-config-id", Prompt: "整理这些内容", Images: []string{},
})
if err != nil {
    return nil, err
}
return tool.Host.Notify(ctx, sessionID, result.Text, tool.OperationID+":result")
```

示例中的通知只应在用户授权的任务中调用。SDK 不自动重试外部写操作。

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
