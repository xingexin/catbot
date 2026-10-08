"""Local deployment settings and safe migration of private configuration."""
import os
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[2]
DEPLOY = ROOT / "deploy"
LOCAL_FILES = (".env", "compose.override.yaml")


def local_paths(root=ROOT):
    pairs = [(root / name, root / "deploy" / name) for name in LOCAL_FILES]
    for old, current in pairs:
        if (old.exists() or old.is_symlink()) and (current.exists() or current.is_symlink()):
            raise ValueError(f"Both {old} and {current} exist; refusing to overwrite local configuration")
    return pairs


def migrate_legacy_config(root=ROOT):
    # Check every destination before moving any file. Preserve original bytes,
    # permissions and deployment identity; never regenerate existing secrets.
    pairs = local_paths(root)
    (root / "deploy").mkdir(parents=True, exist_ok=True)
    for old, current in pairs:
        if old.exists() or old.is_symlink():
            old.rename(current)


def settings(root=ROOT, overrides=None):
    local_paths(root)
    path = root / "deploy" / ".env"
    if not path.exists():
        path = root / ".env"
    values = {}
    if path.exists():
        for line in path.read_text().splitlines():
            key, sep, value = line.strip().partition("=")
            if sep and not key.startswith("#"):
                values[key.strip()] = value.strip().strip("'\"")
    values.update(os.environ if overrides is None else overrides)
    # Legacy installations without an explicit name used secretary volumes.
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
    """Reuse legacy VM storage when selecting a local Colima context."""
    root = Path(colima_home or os.environ.get("COLIMA_HOME", Path.home() / ".colima"))
    return "secretary" if (root / "secretary").is_dir() else "catbot"
