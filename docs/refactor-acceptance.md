# Go / Runtime 分层重构验收记录

本记录针对 2026-10-08 的目录与职责拆分。运行环境为 macOS、Docker context `colima-secretary`，既有 Compose project 为 `secretary`。本页记录分层、协议、构建及隔离环境中的启动与恢复验证。现有生产容器未重建或重启。

## 部署资源集中

部署资源位于 `deploy/`：Dockerfile、专用 ignore、Compose 基础及测试配置、环境配置、nginx、启动脚本和部署测试。根 `.dockerignore` 仅保留指向 `deploy/Dockerfile.dockerignore` 的兼容链接，以支持当前 legacy Docker builder；依据见 [部署说明](../deploy/README.md)。

| 检查 | 结果 |
| --- | --- |
| `python3 -m unittest discover -s deploy/tests -p 'test_deployment.py' -v` | 16 项通过；覆盖旧配置迁移、目标冲突时停止、文件内容及权限保留、新旧项目名、Colima 复用、Compose 参数、ignore 链接 |
| 初始化路径 | 使用 `deploy/.env`；旧根 `.env` 与 `compose.override.yaml` 迁移前统一检查冲突，不覆盖目标 |
| 本机私密配置迁移 | `.env` 与 override 原样迁移，继续被 Git 忽略；未重新生成数据库密码、加密密钥或 QQ 配置 |
| 基础及测试 Compose config | 均可解析；明确 `--project-directory` 为项目根，`--env-file deploy/.env`，默认叠加本机 override |
| 项目与卷 | project 仍为 `secretary`；保留 `secretary_app-data`、`secretary_napcat-qq`、`secretary_postgres`、`secretary_sdk` |
| 构建及挂载路径 | 构建上下文为项目根、Dockerfile 为 `deploy/Dockerfile`；NapCat 配置仍挂载根 `data/napcat/config`，fixture 仍指向根 `scripts/fixture-model.mjs` |
| 本机网络 | runtime/backend 本机覆盖 DNS `119.29.29.29` 保留；QQ 账号配置仍存在，未输出凭证值 |
| Make 接口 | `make -n up docker status logs restart stop down build dev` 均通过；`make` 默认启动与 `make docker` 保持 |
| 脚本 | JS 语法、Python AST、Shell 语法及 diff 空白检查通过 |

上述是配置迁移与解析验证，不等同于已经重新构建、重启整套应用。该阶段没有重启生产容器。

## 任务职责及兼容边界

- `domain/task/entity`：任务、步骤、快照、执行输入及不会安全重试的领域错误。
- `domain/task/service`：任务定义、步骤及通知引用验证，结果引用解析、可读通知生成。
- `domain/task/repository`：具体类型化仓储包装器，复用 `infra/store`，封装任务/执行读写、筛选和任务锁。底层 `infra/store` 不引用业务领域。
- `biz/task`：创建和控制任务、调度意图恢复、执行快照、步骤去重、执行结果及通知状态。
- `infra/temporal`：Temporal 客户端、Schedule、Start Delay、取消及运行状态查询。
- `worker/temporal`：Workflow、Activity 入口、心跳及领域错误到 Temporal 错误的转换。

生产队列保持 `secretary-tasks`，Scheduler 构造器直接提供此默认值供 Worker 装配。持久化 Workflow 名保持 `TaskWorkflow`，Activity 名保持 `Begin`、`ExecuteStep`、`Finish`。Input 的 `TaskID`、`Revision`、`Manual` 序列化名称及快照结构不变；Activity 顺序、Timer、超时、取消语义和重试参数不变。

以下记录类型与操作约定保留：`task`、`execution`、`execution-snapshot`、`step-result`、`schedule-intent`、`notification-incident`，`task:<id>` 锁，`_resultRef`，`task-notify:<executionID>` 和 `<executionID>:<stepID>`。新仓储直接读取旧 JSON 记录，不进行数据重写迁移。

## 单元与并发验证

```sh
go test -race ./internal/domain/task/... ./internal/biz/task ./internal/infra/temporal ./internal/worker/temporal
go vet ./internal/domain/task/... ./internal/biz/task ./internal/infra/temporal ./internal/worker/temporal
go test -tags=integration ./internal/infra/temporal ./internal/worker/temporal -run '^$'
```

以上均通过。原 job 测试迁移到对应包，保留以下行为验证：

- Begin 快照写失败不会永久占用周期任务；执行记录写失败后重试复用旧人格快照。
- 重试仍检查取消、暂停、版本更新和正在执行的占用；只有确认关闭的 Temporal 执行才释放占用。
- SDK 结果不明时不自动重复整步；安全重试分类、取消和非重试错误码通过 Activity 边界保留。
- 通知条件和文本类型错误被显式报告；通知失败不会被一次性任务显示为完成。
- Finish 重试修复持久化状态时不会重复发送；后续配置更新或取消不被旧 Finish 覆盖。
- 周期错误通知节流及恢复、保存结果引用、旧记录读取、旧锁键、默认队列和稳定操作 ID 兼容。

## 真实 Temporal 历史回放

执行时间：**2026-10-08 17:29（Asia/Shanghai）**。在启动任何本轮新测试 Workflow 之前，从现有 Temporal 只读读取三条已完成 `TaskWorkflow` 历史，由新 `worker/temporal.TaskWorkflow` 回放。

| 原执行开始时间（UTC） | 历史事件数 | 结果 |
| --- | ---: | --- |
| 2026-10-08 07:37:41 | 29 | 回放通过，无 nondeterminism |
| 2026-10-08 07:37:40 | 29 | 回放通过，无 nondeterminism |
| 2026-10-07 10:52:14 | 23 | 回放通过，无 nondeterminism |

用例：`internal/worker/temporal/replay_integration_test.go` 中的 `TestReplayExistingTaskWorkflowHistories`。必须显式设置 `TEST_TEMPORAL_REPLAY_HISTORY=true` 才读取现有历史。回放只执行确定性的 Workflow 代码，Activity 结果来自已记录历史，不调用模型、插件或 QQ；完整历史只在内存处理，文档和日志仅记录元数据。

复验时先查询 Temporal 映射端口，再指定该地址：

```sh
DOCKER_CONTEXT=colima-secretary ./deploy/scripts/compose port temporal 7233
TEST_TEMPORAL_ADDRESS=<实际映射地址> TEST_TEMPORAL_REPLAY_HISTORY=true   go test -tags=integration -race ./internal/worker/temporal   -run '^TestReplayExistingTaskWorkflowHistories$' -count=1 -v
```

## 真实 PostgreSQL / Temporal 隔离验证

执行时间：**2026-10-08 17:29（Asia/Shanghai）**。使用现有 Docker 中的 PostgreSQL 和 Temporal。数据库固定为 **`secretary_test`**；Temporal 使用随机 `integration-*` / `integration-restart-*` 队列及随机任务 ID。沿用现有容器和卷，不修改现有业务任务或 Schedule，不调用真实模型或发送 QQ 消息。

| 用例 | 结果 |
| --- | --- |
| PostgreSQL 持久化与锁竞争 | 通过 |
| PostgreSQL 会话执行队列与 outbox 有界查询 | 通过 |
| Schedule 创建、改时间、暂停、恢复、删除 | 通过；时区、SKIP 重叠策略和 catch-up 窗口保持 |
| 一次性延时、取消、步骤落库 | 通过 |
| 已完成一次性任务重复提交 | 不重做 |
| 同一稳定操作 ID 重复手动触发 | 复用同一执行，不重做 |
| 测试 Worker 停启恢复 | 通过；首步骤已落库后停止 Worker、关闭并重开数据库连接、启动新 Worker；首步总共调用 1 次，后续步调用 1 次 |
| 恢复时人格快照 | 通过；测试期间修改当前人格，原执行仍使用启动时快照 |

运行命令核心为：

```sh
go test -tags=integration -race ./internal/infra/store ./internal/infra/temporal -count=1 -v
```

`TEST_DATABASE_URL` 由本机 `deploy/.env` 和 Compose 映射端口在内存生成，不记录明文。完整项目集成入口已同步到新部署路径，并启用 `integration` tag：

```sh
TEST_DOCKER_CONTEXT=colima-secretary python3 scripts/test-integration.py
```

本轮恢复测试重建的是独立测试 Worker 实例及数据库连接，没有强杀或重启生产容器，也没有验证各 Agent SDK 内部模型步骤的恢复能力。

## 关闭 NapCat 后真实主体启动

执行时间：**2026-10-08 17:35:45（Asia/Shanghai）**。用例 `internal/bootstrap/deployment_integration_test.go` 中的 `TestRealBootstrapWithoutNapCat` 实际启动服务，区别于前面的 Compose 静态解析检查。

- 通过 `config.Load` 读取 `NAPCAT_ENABLED=false` 与空 `ONEBOT_URL`。
- 在 `secretary_test` 内创建随机独立 schema，使用临时数据目录和随机 `integration-bootstrap-*` Temporal 队列，结束后清理自建 schema。这样启动恢复和 outbox 扫描不会读取其他测试或真实业务记录。
- 调用真实 `App.New`、`registerChannels`、`Bootstrap`、`App.Start`，启动真实 Temporal Worker 与 HTTP 监听。
- `/healthz` 返回 200/ok；管理员可登录，`/api/qq` 正常返回；OneBot 状态为 unconfigured，`napcatWebUrl` 为空；Temporal Ping 成功。
- 用本地拦截服务统计可能的外部请求，启动与关闭阶段总数为 **0**；未调用模型或 QQ。

以下命令在隔离测试环境变量下通过（含 race）：

```sh
go test -tags=integration -race ./internal/bootstrap \
  -run '^TestRealBootstrapWithoutNapCat$' -count=1 -v
```

此次通过的是不依赖 NapCat 的 Go 主体、后台 Worker 和 HTTP 管理 API 启动。原生产 NapCat 容器无需为测试停机，既有 Compose 服务和登录状态保持。

## 全项目验收

| 检查 | 结果 |
| --- | --- |
| `go test -race ./...` | 全部通过；原 service 场景迁移到 bootstrap 集成测试，覆盖人格快照、原生会话恢复、多轮历史、工具授权与幂等、QQ 私聊/群聊隔离、发送不确定状态、通知恢复及模型调用账本 |
| `go vet ./...` | 通过；分包后的结构体跨包初始化使用具名字段 |
| `go build ./cmd/catbot` | 通过；已构建本地可执行文件 |
| 依赖方向 | Go AST 架构检查通过；domain 规则不依赖 biz/transport/worker/bootstrap/infra，领域 repository 仅允许复用 infra/store；infra/store 不反向引用 domain；biz 不依赖 Temporal SDK |
| 管理 HTTP/SSE | 38 条显式路由与原实现一致，动态列表路由保留；登录 Cookie/Origin/限流顺序、凭证隐藏、单 JSON 请求、SSE 游标和终态、上传下载、缺少消息适配器状态通过回归 |
| 内部 MCP / pluginhost | 运行与插件快照鉴权、工具范围、宿主数据访问、模型记录脱敏通过回归；保留缺失记录/文件的 404 行为 |
| `npm run build` | plugin-sdk、Runtime、三个业务插件及 Web 构建通过 |
| `npm test` | 79 项通过，1 项视频 FFmpeg 检查因本机缺少 FFmpeg 跳过；三家 Provider、注册、取消、权限和事件转换包含在 Runtime 测试中 |
| 部署脚本 | 16 项通过；实际无 NapCat 主体启动结果见上一节 |
| 差异检查 | `git diff --check` 通过 |

领域和仓储按实际复杂度拆分，没有创建空的 common 或占位领域服务。`domain/conversation`、`domain/persona`、`domain/task` 使用类型化仓储；其他简单管理用例按需复用存储底座。旧 `internal/service`、`internal/job`、集中 `domain/types.go` 和旧适配器目录已移除，不保留生产转发层。

本轮平台请求采用本地协议替身，未重新进行真实 QQ 收发、IMAP 账号或三家模型服务联调。此前真实联调记录与本次结构迁移验证分别保留，不能以本轮模拟测试代替真实兼容性结论。已有 Compose 容器仍运行此前的镜像；在项目根执行 `make` 才会按新结构重新构建并启动，`make docker` 仍仅准备 Docker。配置入口为 `deploy/.env`。
