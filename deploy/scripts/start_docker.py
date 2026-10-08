"""Prepare the selected Docker daemon, without polling a stale default context."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time

from deployment import colima_profile

PROBE_TIMEOUT = 5
WAIT_TIMEOUT = 120
POLL_INTERVAL = 2


def probe(context=None, timeout=PROBE_TIMEOUT):
    command = ["docker"]
    if context:
        command += ["--context", context]
    try:
        result = subprocess.run(command + ["info"], stdout=subprocess.DEVNULL,
                                stderr=subprocess.DEVNULL, timeout=timeout)
        return result.returncode == 0
    except subprocess.TimeoutExpired:
        return False


def current_context():
    result = subprocess.run(["docker", "context", "show"], check=True,
                            capture_output=True, text=True, timeout=PROBE_TIMEOUT)
    return result.stdout.strip()


def activate_context(context):
    subprocess.run(["docker", "context", "use", context], check=True,
                   stdout=subprocess.DEVNULL, timeout=PROBE_TIMEOUT)
    print(f"Docker 已就绪，使用环境：{context}。", flush=True)


def wait_ready(context=None, activate=False):
    deadline = time.monotonic() + WAIT_TIMEOUT
    next_notice = 0
    while (remaining := deadline - time.monotonic()) > 0:
        if probe(context, timeout=min(PROBE_TIMEOUT, remaining)):
            if activate:
                activate_context(context)
            else:
                print("Docker 已就绪。", flush=True)
            return
        now = time.monotonic()
        if now >= next_notice:
            print(f"正在等待 Docker 就绪（环境：{context or '当前环境'}）…", flush=True)
            next_notice = now + 10
        remaining = deadline - time.monotonic()
        if remaining > 0:
            time.sleep(min(POLL_INTERVAL, remaining))
    raise RuntimeError(f"等待 Docker 就绪超时（环境：{context or '当前环境'}），请检查 Colima 或 Docker Desktop 日志。")


def prepare_colima(profile, automatic=False):
    context = "colima" if profile == "default" else "colima-" + profile
    if probe(context):
        if automatic:
            activate_context(context)
        else:
            print(f"Docker 已就绪，复用环境：{context}。", flush=True)
        return
    print(f"正在启动 Colima 环境：{profile}", flush=True)
    command = ["colima", "--profile", profile, "start", "--activate=false"]
    if automatic:
        command += ["--cpu", "2", "--memory", "4", "--disk", "60"]
    subprocess.run(command, check=True, timeout=180)
    wait_ready(context, activate=automatic)


def start_desktop():
    if sys.platform != "darwin":
        raise RuntimeError("请先启动当前平台的 Docker Desktop。")
    print("正在启动 Docker Desktop…", flush=True)
    subprocess.run(["open", "-a", "Docker"], check=True, timeout=10)
    wait_ready()


def ensure_docker():
    if not shutil.which("docker"):
        raise RuntimeError("缺少 Docker，请先安装 Docker Desktop 或 Docker CLI + Colima。")
    if probe():
        print("Docker 已就绪，复用现有环境。", flush=True)
        return
    # Explicit endpoints must never silently fall back to a different daemon.
    if os.environ.get("DOCKER_HOST") and not os.environ.get("DOCKER_CONTEXT"):
        raise RuntimeError("DOCKER_HOST 指定的 Docker 不可用，请先启动对应环境或检查连接配置。")
    context = current_context()
    if context == "colima" or context.startswith("colima-"):
        if not shutil.which("colima"):
            raise RuntimeError("当前 Docker 环境由 Colima 提供，请先安装 Colima。")
        profile = "default" if context == "colima" else context.removeprefix("colima-")
        prepare_colima(profile)
    elif context == "default":
        if not os.environ.get("DOCKER_CONTEXT") and shutil.which("colima"):
            # Switching here is intentional: Make's later recipes run in separate
            # processes and must use the same daemon whose readiness was checked.
            prepare_colima(colima_profile(), automatic=True)
        elif sys.platform == "darwin" and (Path("/Applications/Docker.app").is_dir()
                                           or (Path.home() / "Applications/Docker.app").is_dir()):
            start_desktop()
        else:
            raise RuntimeError("当前 Docker 服务未启动，且没有可自动启动的 Colima 或 Docker Desktop。")
    elif context == "desktop-linux":
        start_desktop()
    else:
        raise RuntimeError(f"Docker 环境 {context} 不可用，请检查该环境；不会自动切换到其他环境。")


def main():
    try:
        ensure_docker()
    except (RuntimeError, OSError, subprocess.SubprocessError) as error:
        print(str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
