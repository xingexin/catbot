"""Create local secrets once; never overwrite an existing installation key."""
import base64
import json
import os
from pathlib import Path
import secrets
from deployment import ROOT, migrate_legacy_config, settings

root = ROOT
migrate_legacy_config(root)
path = root / "deploy" / ".env"
if path.exists():
    print("deploy/.env already exists; preserving existing values.")
else:
    values = {
        "POSTGRES_PASSWORD": secrets.token_hex(24),
        "MASTER_KEY": base64.b64encode(secrets.token_bytes(32)).decode(),
        "RUNTIME_TOKEN": secrets.token_hex(32),
        "ADMIN_PASSWORD": secrets.token_urlsafe(20),
        "ONEBOT_TOKEN": secrets.token_hex(32),
        "NAPCAT_WEBUI_TOKEN": secrets.token_urlsafe(24),
    }
    text = (root / "deploy" / ".env.example").read_text()
    lines = []
    for line in text.splitlines():
        key = line.partition("=")[0]
        lines.append(key + "=" + values[key] if key in values else line)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as out:
        out.write("\n".join(lines) + "\n")
    print("Created deploy/.env with private file permissions. Read ADMIN_PASSWORD locally to log in.")

# Add only missing settings when upgrading an existing installation.
text = path.read_text()
values = dict(line.split("=", 1) for line in text.splitlines()
              if "=" in line and not line.lstrip().startswith("#"))
missing = {key: secrets.token_hex(32) for key in
           ("ONEBOT_TOKEN", "NAPCAT_WEBUI_TOKEN") if key not in values}
missing.update({key: value for key, value in
                {"NAPCAT_ENABLED": "true", "ONEBOT_URL": ""}.items() if key not in values})
# Resolve before appending: an old .env without a project name must keep its
# original volumes. Explicit environment overrides are intentionally not saved.
if not values.get("COMPOSE_PROJECT_NAME", "").strip("'\""):
    missing["COMPOSE_PROJECT_NAME"] = settings(root, {})["COMPOSE_PROJECT_NAME"]
if missing:
    with path.open("a") as out:
        if not text.endswith("\n"):
            out.write("\n")
        for key, value in missing.items():
            out.write(f"{key}={value}\n")
    values.update(missing)
    print("Added missing deployment settings; existing data identity preserved.")

values = settings(root)
if values["NAPCAT_ENABLED"] == "false":
    print("Local NapCat disabled; starting catbot services only.")
    raise SystemExit(0)

token = values["ONEBOT_TOKEN"]
if not token or token.startswith("replace-"):
    raise SystemExit("Set a non-empty ONEBOT_TOKEN in deploy/.env before starting NapCat.")

config_dir = root / "data" / "napcat" / "config"
config_dir.mkdir(parents=True, exist_ok=True)

def write_once(name, value):
    target = config_dir / name
    if target.exists():
        return
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as out:
        json.dump(value, out, ensure_ascii=False, indent=2)
        out.write("\n")

# NapCat uses this template on first login and persists onebot11_<QQ>.json.
# Never overwrite its per-account configuration or login state.
write_once("napcat.json", {"fileLog": True, "consoleLog": True, "fileLogLevel": "info", "consoleLogLevel": "info"})
write_once("onebot11.json", {
    "network": {
        "httpServers": [{
            "name": "catbot-api", "enable": True, "host": "0.0.0.0", "port": 3000,
            "enableCors": False, "enableWebsocket": False,
            "messagePostFormat": "array", "token": token, "debug": False,
        }],
        "httpClients": [{
            "name": "catbot-events", "enable": True,
            "url": "http://backend:8080/qq/onebot/events",
            "messagePostFormat": "array", "reportSelfMessage": False,
            "token": token, "debug": False,
        }],
        "websocketServers": [], "websocketClients": [],
    },
    "musicSignUrl": "", "enableLocalFile2Url": False, "parseMultMsg": False,
})
print("NapCat default configuration ready; existing account files preserved.")
