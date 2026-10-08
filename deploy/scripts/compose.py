"""Compose launcher: select optional dependencies without changing core services."""
import os
import subprocess
import sys

from deployment import ROOT, settings


def compose_options(root=ROOT):
    options = ["--project-directory", str(root), "--env-file", str(root / "deploy" / ".env"),
               "-f", str(root / "deploy" / "compose.yaml")]
    override = root / "deploy" / "compose.override.yaml"
    if override.exists():
        options += ["-f", str(override)]
    return options


def main():
    values = settings()
    env = dict(os.environ)
    # Leave other interpolation to Compose; only supply resolved dependency values.
    for key in ("NAPCAT_ENABLED", "ONEBOT_URL", "COMPOSE_PROJECT_NAME"):
        env[key] = values[key]
    probe = subprocess.run(["docker", "compose", "version"], capture_output=True)
    command = ["docker", "compose"] if probe.returncode == 0 else ["docker-compose"]
    args = sys.argv[1:]
    options = compose_options()
    action = next((arg for arg in args if arg in
                   ("up", "down", "stop", "ps", "logs", "port", "restart")), "")
    enabled = values["NAPCAT_ENABLED"] == "true"
    if not enabled and action == "up":
        # Disabling a previously running dependency stops it without deleting data.
        global_options = args[:args.index(action)]
        subprocess.run(command + options + global_options + ["--profile", "napcat", "stop", "napcat"],
                       cwd=ROOT, env=env, check=True)
    # Include optional containers for inspection/shutdown even after disabling them.
    lifecycle = action in ("down", "stop", "ps", "logs", "port")
    profile = ["--profile", "napcat"] if enabled or lifecycle else []
    os.chdir(ROOT)
    os.execvpe(command[0], command + options + profile + args, env)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, FileNotFoundError) as error:
        raise SystemExit(str(error))
