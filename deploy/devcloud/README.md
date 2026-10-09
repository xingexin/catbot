# DevCloud 部署与运维

此目录对应本次 **全新部署**：TencentOS Server 4.2、专用用户 `catbot`（UID 1000）、Rootless Docker 29.9.0、Supervisor 4.3.0，无 systemd。Compose 插件安装在该用户的 Docker CLI 插件目录；以 `manage.sh start` 输出的版本和实际验收记录为准。

这里提供已安装环境的管理入口，不是系统安装脚本。不会复制开发机数据库、历史任务、SDK 会话或 QQ 登录态。

## 目录和运行身份

| 路径 | 用途 |
| --- | --- |
| `/data/catbot` | 项目源码、构建上下文 |
| `/data/catbot/deploy/.env` | 服务器独立私密配置，权限 0600，由 catbot 读取 |
| `/data/catbot/deploy/compose.override.yaml` | 仅本实例使用的覆盖配置，如确有需要 |
| `/data/catbot-home` | catbot 用户 HOME；Docker 二进制、配置、CLI 插件 |
| `/data/catbot-home/.docker/run` | Rootless Docker 临时 Unix socket，权限 0700 |
| `/data/catbot-home/.local/share/docker` | 默认 Docker 数据目录；自定义 data-root 时以 docker info 为准 |
| `/data/catbot-ops/venv` | Supervisor 的 Python 虚拟环境 |
| `/data/catbot-ops/supervisord.conf` | 现场 Supervisor 配置，首次启动时从本目录样例复制 |
| `/data/catbot-ops/logs` | Docker 和 Supervisor 的滚动日志 |

管理脚本由 root 执行。Docker daemon 和项目的 Compose、make 都以 catbot 用户运行，使用明确的用户环境与专属 socket。Supervisor 仅开放权限 0700 的本机 Unix socket，未开放 HTTP 管理端口。不要通过另一套 Docker daemon 或另一个 Compose 项目名操作同一份应用数据。

## 首次启动前

基础条件应由部署人员完成：Rootless Docker、Compose 插件、Python 3、make、runuser、timeout、flock、Supervisor 虚拟环境；catbot 的 UID/GID 映射及运行用户下的容器启动已验证。当前 cgroup v1 不支持 Rootless Docker 的子容器 CPU、内存及 PID 配额，仍受外层 DevCloud 实例资源配额约束。

源码、私密部署配置和 NapCat 的项目内配置目录需允许 catbot 使用。初始化按现有 `deploy/scripts/init-env.py` 流程生成服务器自己的 `deploy/.env`，不要沿用开发机的环境文件或 DNS override。管理脚本不安装依赖，不重建用户，不覆盖已有密钥或现场 Supervisor 配置。

用 root 执行：

```sh
/data/catbot/deploy/devcloud/manage.sh up
/data/catbot/deploy/devcloud/manage.sh status
```

`up` 启动 Supervisor 和 Rootless Docker，等待 daemon 就绪，然后在项目根目录以 catbot 身份执行 `make`，构建镜像并启动服务。首次构建需要网络下载依赖及镜像。不要上传开发机的 `node_modules` 或 Go 插件二进制代替目标 Linux 构建。

## 日常命令

```sh
# 用现有镜像启动，等待健康检查；不重新构建
/data/catbot/deploy/devcloud/manage.sh start

# 查看 Docker、Supervisor 和各 Compose 服务
/data/catbot/deploy/devcloud/manage.sh status

# 查看最近日志，输出后退出，不持续跟随
/data/catbot/deploy/devcloud/manage.sh logs

# 源码更新后重新构建并部署
/data/catbot/deploy/devcloud/manage.sh up

# 停止 catbot 服务、专用 Docker daemon 和 Supervisor，保留全部数据
/data/catbot/deploy/devcloud/manage.sh stop
```

`start` 使用 `compose up --no-build --wait`；镜像不存在时不会偷偷编译，应执行 `up`。没有 `down -v`、卷清理、数据库重置或强制删除 PID/socket 的行为。正常停机先停止 Compose 服务，再停止 Docker，最后结束 Supervisor。Compose 停止失败时会中止后续停机；Docker 已不可用时无法确认应用是否优雅退出，脚本会明确显示这一点。

脚本以文件锁避免同时执行多个启动、停止或更新命令。Supervisor 对 Docker 意外退出自动重启，Docker 中的 `restart: unless-stopped` 负责应用容器恢复；显式执行 stop 后，需执行 start 或 up 才恢复这些服务。Supervisor 的 RUNNING 状态不等于应用就绪，最终要看 Compose 和管理 API。

默认日志只保留 Docker 日志 3 × 20 MB 备份、Supervisor 日志 3 × 10 MB 备份，另有各自当前日志。业务容器日志仍由 Docker 的日志配置管理；日志可能包含业务文本，不应公开分享整个日志目录。

## 访问与功能配置

基础 Compose 只把 Web 5173 和 NapCat 6099 绑定到服务器回环地址。使用 SSH 端口转发或平台正式支持的访问入口；SSH 端口不是 Web 端口。数据库和 Temporal 端口同样只用于内部管理，不用公网开放。

新部署只创建默认人格和内置插件登记，不包含可直接使用的模型凭证、邮箱账号、QQ 绑定。模型接入、QQ 扫码及联系人/群授权、邮箱监听都需在服务器实例单独配置。视频插件仍需真实转写及视觉模型；安装插件不代表这些外部能力已联调。

验收时先验证未登录 `/api/me` 返回 401，再登录并检查 `/api/status` 的数据库、Temporal、Runtime 状态。后端容器内的 `/healthz` 才是健康端点；Web 当前没有代理该路径，不能用返回首页的 HTTP 200 冒充后端健康。QQ 在线和提醒收发需要真实登录后单独验收。

## 实例重启、恢复和限制

**当前没有配置 DevCloud 平台启动 hook。** Supervisor 可重启当前实例内意外退出的 Docker，但不能让外层实例在停机、休眠或重建后自动重新启动。实例回来后，先确认 `/data` 与专用用户、UID/GID 映射和所需工具仍存在，再手动执行 `manage.sh start`。

`/data` 的保留范围由平台决定；仅有该目录不代表具备备份。持久化数据、应用 `MASTER_KEY` 与数据库需匹配保存。不要删除数据根或重新初始化密钥来“修复”连接错误。故障时先看 `manage.sh logs`，不要因检测失败直接删 socket、PID 或数据卷。

本部署不承诺外层实例自动开机、休眠期间继续收消息或全年在线。若需要无人值守长期运行，应另行配置平台认可的实例保活、启动 hook 和备份，并实际验证外层重启恢复。不要为此修改外层宿主的隔离权限或挂载宿主 Docker socket。

更新本目录的 Supervisor 样例不会自动覆盖现场配置。需要修改时，先 stop，审核并更新 `/data/catbot-ops/supervisord.conf`，再 start；普通源码更新只需 up。
