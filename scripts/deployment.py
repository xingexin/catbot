"""Local dependency settings shared by initialization and the Compose launcher."""
import os
from pathlib import Path
import re


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
    # Existing installations used the fixed Compose name "secretary". Keeping
    # it is essential: a different project name would select empty data volumes.
    project = values.get("COMPOSE_PROJECT_NAME", "")
    if not project:
        project = "secretary" if path.exists() else "catbot"
    if not re.fullmatch(r"[a-z0-9][a-z0-9_-]*", project):
        raise ValueError("COMPOSE_PROJECT_NAME must use lowercase letters, digits, hyphens or underscores")
    values["COMPOSE_PROJECT_NAME"] = project
    enabled = values.get("NAPCAT_ENABLED", "true").lower()
    if enabled not in ("true", "false"):
        raise ValueError("NAPCAT_ENABLED must be true or false")
    values["NAPCAT_ENABLED"] = enabled
    values["ONEBOT_URL"] = values.get("ONEBOT_URL") or (
        "http://napcat:3000" if enabled == "true" else ""
    )
    return values


def colima_profile(colima_home=None):
    """Reuse legacy VM storage when creating/selecting a local Colima context."""
    root = Path(colima_home or os.environ.get("COLIMA_HOME", Path.home() / ".colima"))
    return "secretary" if (root / "secretary").is_dir() else "catbot"
