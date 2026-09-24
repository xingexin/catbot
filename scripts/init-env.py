"""Create local secrets once; never overwrite an existing installation key."""
import base64
import os
from pathlib import Path
import secrets

root = Path(__file__).resolve().parent.parent
path = root / ".env"
if path.exists():
    print(".env already exists; preserved without changes.")
else:
    values = {
        "POSTGRES_PASSWORD": secrets.token_hex(24),
        "MASTER_KEY": base64.b64encode(secrets.token_bytes(32)).decode(),
        "RUNTIME_TOKEN": secrets.token_hex(32),
        "ADMIN_PASSWORD": secrets.token_urlsafe(20),
    }
    text = (root / ".env.example").read_text()
    lines = []
    for line in text.splitlines():
        key = line.partition("=")[0]
        lines.append(key + "=" + values[key] if key in values else line)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as out:
        out.write("\n".join(lines) + "\n")
    print("Created .env with private file permissions. Read ADMIN_PASSWORD locally to log in.")
