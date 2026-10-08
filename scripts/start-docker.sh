#!/bin/sh
set -eu

if ! command -v docker >/dev/null 2>&1; then
  printf '缺少 Docker，请先安装 Docker Desktop 或 Docker CLI + Colima。\n' >&2
  exit 1
fi

if docker info >/dev/null 2>&1; then
  printf 'Docker 已就绪，复用现有环境。\n'
  exit 0
fi

# An explicitly configured endpoint must not silently fall back to another daemon.
if [ -n "${DOCKER_HOST:-}" ] && [ -z "${DOCKER_CONTEXT:-}" ]; then
  printf 'DOCKER_HOST 指定的 Docker 不可用，请先启动对应环境或检查连接配置。\n' >&2
  exit 1
fi

docker_context=$(docker context show)
case "$docker_context" in
  colima|colima-*)
    if ! command -v colima >/dev/null 2>&1; then
      printf '当前 Docker 环境由 Colima 提供，请先安装 Colima。\n' >&2
      exit 1
    fi
    colima_profile=${docker_context#colima-}
    if [ "$docker_context" = colima ]; then colima_profile=default; fi
    printf '正在启动 Colima 环境：%s\n' "$colima_profile"
    colima --profile "$colima_profile" start --activate=false
    ;;
  default)
    if [ -z "${DOCKER_CONTEXT:-}" ] && command -v colima >/dev/null 2>&1; then
      script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
      colima_profile=$(python3 -c 'import sys; sys.path.insert(0,sys.argv[1]); from deployment import colima_profile; print(colima_profile())' "$script_dir")
      printf '正在启动项目的 Colima 环境：%s\n' "$colima_profile"
      colima --profile "$colima_profile" start --cpu 2 --memory 4 --disk 60
    elif [ "$(uname -s)" = Darwin ] && { [ -d /Applications/Docker.app ] || [ -d "$HOME/Applications/Docker.app" ]; }; then
      printf '正在启动 Docker Desktop…\n'
      open -a Docker
    else
      printf '当前 Docker 服务未启动，且没有可自动启动的 Colima 或 Docker Desktop。\n' >&2
      exit 1
    fi
    ;;
  desktop-linux)
    if [ "$(uname -s)" != Darwin ]; then
      printf '请先启动当前平台的 Docker Desktop。\n' >&2
      exit 1
    fi
    printf '正在启动 Docker Desktop…\n'
    open -a Docker
    ;;
  *)
    printf 'Docker 环境 %s 不可用，请检查该环境；不会自动切换到其他环境。\n' "$docker_context" >&2
    exit 1
    ;;
esac

attempt=0
while [ "$attempt" -lt 60 ]; do
  if docker info >/dev/null 2>&1; then
    printf 'Docker 已就绪。\n'
    exit 0
  fi
  attempt=$((attempt + 1))
  sleep 2
done

printf '等待 Docker 就绪超时，请检查 Colima 或 Docker Desktop 的启动日志。\n' >&2
exit 1
