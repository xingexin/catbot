"""Local dependency settings shared by initialization and the Compose launcher."""
import os
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent


def settings(root=ROOT, overrides=None):
    values = {}
    path = root / ".env"
    if path.exists():
        for line in path.read_text().splitlines():
            key, sep, value = line.strip().partition("=")
            if sep and not key.startswith("#"):
                values[key.strip()] = value.strip().strip("'\"")
    values.update(os.environ if overrides is None else overrides)
    enabled = values.get("NAPCAT_ENABLED", "true").lower()
    if enabled not in ("true", "false"):
        raise ValueError("NAPCAT_ENABLED must be true or false")
    values["NAPCAT_ENABLED"] = enabled
    values["ONEBOT_URL"] = values.get("ONEBOT_URL") or (
        "http://napcat:3000" if enabled == "true" else ""
    )
    return values
