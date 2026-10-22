import hashlib
from pathlib import Path


def fingerprint():
    h = hashlib.sha256()
    files = [Path(p) for p in ("go.mod", "go.sum", "pyproject.toml", "uv.lock") if Path(p).is_file()]
    for directory in ("internal", "cmd", "agentkit", "tests", "scripts", "migrations"):
        files.extend(p for p in Path(directory).rglob("*") if p.is_file() and p.suffix in {".go", ".py", ".sh", ".sql"} and "__pycache__" not in p.parts)
    for p in sorted(files):
        h.update(str(p).encode() + b"\0" + p.read_bytes() + b"\0")
    return h.hexdigest()


if __name__ == "__main__":
    print(fingerprint())
