.DEFAULT_GOAL := up

COMPOSE := ./scripts/compose
SERVICE ?=
WAIT_TIMEOUT ?= 300

.PHONY: up start docker check down stop restart status logs help build test dev

up: check
	@python3 scripts/init-env.py
	$(COMPOSE) up --build --wait --wait-timeout $(WAIT_TIMEOUT)
	@web_address=$$($(COMPOSE) port web 80) && \
		printf '\n服务已启动：http://%s\n登录密码：项目 .env 中的 ADMIN_PASSWORD\n' "$$web_address"
	@if [ "$$(python3 -c 'import sys; sys.path.insert(0,"scripts"); from deployment import settings; print(settings()["NAPCAT_ENABLED"])')" = true ]; then \
		napcat_address=$$($(COMPOSE) port napcat 6099) && \
		printf '个人 QQ 登录：http://%s/webui\nNapCat 初始令牌：项目 .env 中的 NAPCAT_WEBUI_TOKEN\n' "$$napcat_address"; \
	fi

start: up

docker:
	@command -v python3 >/dev/null 2>&1 || { printf '缺少 Python 3，无法读取部署配置。\n' >&2; exit 1; }
	@sh scripts/start-docker.sh

check: docker
	@command -v python3 >/dev/null 2>&1 || { printf '缺少 Python 3，无法初始化本地配置。\n' >&2; exit 1; }
	@$(COMPOSE) version >/dev/null 2>&1 || { printf '缺少 Docker Compose，请安装支持 --wait 的 Compose。\n' >&2; exit 1; }

down:
	$(COMPOSE) down

stop:
	$(COMPOSE) stop $(SERVICE)

restart:
	$(COMPOSE) restart $(SERVICE)

status:
	$(COMPOSE) ps

logs:
	$(COMPOSE) logs --follow --tail=100 $(SERVICE)

help:
	@printf '%s\n' \
		'make / make up        自动准备 Docker、初始化配置并启动全部服务' \
		'make docker           只准备 Docker 环境，已运行则直接复用' \
		'make status           查看服务状态' \
		'make logs             持续查看日志，Ctrl+C 退出' \
		'make logs SERVICE=backend  只查看后端日志' \
		'make restart          重启现有容器，不重新构建' \
		'make stop             停止服务，保留容器和数据' \
		'make down             停止并移除容器，保留数据卷' \
		'make build            使用本机 Go、Node.js 编译源码' \
		'make test             运行 Go 和 TypeScript 测试'

build:
	npm ci
	npm run build
	go build -o bin/catbot ./cmd/catbot
test:
	go test -race ./...
	npm test
dev:
	go run ./cmd/catbot
