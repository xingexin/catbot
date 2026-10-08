# catbot 部署

在项目根目录运行 `make`；单独准备 Docker 用 `make docker`。其他 `make status/logs/restart/stop/down` 接口不变。

Docker 准备脚本先检查当前连接。当前 `default` 不可用时，会明确检查项目的 Colima context；已经运行就直接复用并选中该 context，避免 `colima start` 返回“already running”后仍等待错误连接。显式设置的 `DOCKER_HOST` / `DOCKER_CONTEXT` 不会被自动替换。单次连接探测最多 5 秒，就绪等待最多 120 秒，并显示等待进度。

- `Dockerfile`：三个构建目标 backend、runtime、web；构建上下文始终为项目根。
- `Dockerfile.dockerignore`：排除私密配置、数据、依赖和构建产物。根 `.dockerignore` 是兼容链接，不维护第二份规则。
- `compose.yaml`：基础服务；`compose.test.yaml`：验收 fixture。
- `.env.example`：配置模板；`.env`：初始化生成的本机私密配置，不提交。
- `compose.override.yaml`：可选本机覆盖，例如网络 DNS；不提交，默认自动加载。
- `nginx/nginx.conf`：Web 反向代理。
- `scripts/`：初始化、Docker 启动和 Compose 包装器。
- `tests/`：部署兼容性测试。

```sh
python3 deploy/scripts/init-env.py
./deploy/scripts/compose ps
./deploy/scripts/compose -f deploy/compose.test.yaml up -d fixture
python3 -m unittest discover -s deploy/tests
```

包装器显式设置项目根 `--project-directory`、`--env-file deploy/.env` 和基础 Compose 文件。额外 `-f` 叠加在基础文件和本机覆盖之后；从项目根传入 `deploy/compose.test.yaml` 即可。使用包装器可避免裸 Compose 因当前目录不同而解析到另一套挂载或环境配置。

旧根 `.env` 和 `compose.override.yaml` 会由初始化脚本原样迁移，保留权限。先检查全部目标是否冲突；任一新旧文件同时存在即报错，不覆盖任何一方。旧安装继续保留 `COMPOSE_PROJECT_NAME=secretary`、原命名卷、数据库、SDK 会话及 QQ 登录；新安装默认 `catbot`。

根 `.dockerignore` 兼容链接有意保留：[Compose legacy 构建代码](https://github.com/docker/compose/blob/v5.4.0/pkg/compose/build_classic.go#L183) 调用 Docker CLI 的 [ReadDockerignore](https://github.com/docker/cli/blob/v29.7.2/cli/command/image/build/dockerignore.go#L13)，后者用 `os.Open` 读取根文件，会跟随符号链接。这样 legacy 和 BuildKit 使用同一套排除规则，不把私密配置发送到构建上下文。

详细运维、QQ 接入和真实服务联调见 [运维说明](../docs/operations.md)。
