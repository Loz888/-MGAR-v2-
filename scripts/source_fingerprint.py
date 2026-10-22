import hashlib
import os
from pathlib import Path
import subprocess


def fingerprint():
    """Hash tracked and candidate inputs, excluding acceptance bookkeeping."""
    root = Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"]).decode().rstrip("\n"))
    names = subprocess.check_output(
        ["git", "ls-files", "-z", "-co", "--exclude-standard"], cwd=root,
    ).split(b"\0")
    h = hashlib.sha256()
    for name in sorted(set(names) - {b""}):
        if name.startswith(b"evidence/") or name == b"TASK_QUEUE.md":
            continue
        path = root / os.fsdecode(name)
        if path.is_symlink():
            # The repository input is the link, not an external target's bytes.
            content = b"symlink\0" + os.fsencode(path.readlink())
        elif path.is_file():
            content = b"file\0" + path.read_bytes()
        else:
            # Deleted tracked paths have no current input bytes.
            continue
        h.update(len(name).to_bytes(8, "big") + name)
        h.update(hashlib.sha256(content).digest())
    return h.hexdigest()


if __name__ == "__main__":
    print(fingerprint())
