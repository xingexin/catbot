"""Compose launcher: select optional dependencies without changing core services."""
import os
import subprocess
import sys

from deployment import ROOT, settings


def main():
    values = settings()
    env = dict(os.environ)
    # Leave other interpolation to Compose; only supply resolved dependency values.
    for key in ("NAPCAT_ENABLED", "ONEBOT_URL"):
        env[key] = values[key]
    probe = subprocess.run(["docker", "compose", "version"], capture_output=True)
    command = ["docker", "compose"] if probe.returncode == 0 else ["docker-compose"]
    args = sys.argv[1:]
    action = next((arg for arg in args if arg in
                   ("up", "down", "stop", "ps", "logs", "port", "restart")), "")
    enabled = values["NAPCAT_ENABLED"] == "true"
    if not enabled and action == "up":
        # Disabling a previously running dependency stops it without deleting data.
        global_options = args[:args.index(action)]
        subprocess.run(command + global_options + ["--profile", "napcat", "stop", "napcat"],
                       cwd=ROOT, env=env, check=True)
    # Include optional containers for inspection/shutdown even after disabling them.
    lifecycle = action in ("down", "stop", "ps", "logs", "port")
    profile = ["--profile", "napcat"] if enabled or lifecycle else []
    os.chdir(ROOT)
    os.execvpe(command[0], command + profile + args, env)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, FileNotFoundError) as error:
        raise SystemExit(str(error))
