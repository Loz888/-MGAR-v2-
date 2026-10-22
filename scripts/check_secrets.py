"""Scan tracked and candidate source files; do not print matching content."""
from pathlib import Path
import re
import subprocess

patterns = [rb"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----", rb"gh[pousr]_[A-Za-z0-9]{30,}", rb"github_pat_[A-Za-z0-9_]{30,}", rb"\bAKIA[0-9A-Z]{16}\b", rb"\bsk-[A-Za-z0-9_-]{32,}"]
files = set(subprocess.check_output(["git", "ls-files", "-co", "--exclude-standard"], text=True).splitlines())
bad = []
for name in files:
    path = Path(name)
    if path.name == ".env" or name.startswith(".local/"):
        bad.append(name)
    elif path.is_file() and any(re.search(pattern, path.read_bytes()) for pattern in patterns):
        bad.append(name)
if bad:
    raise SystemExit("possible secret files (contents withheld): " + ", ".join(sorted(bad)))
print("credential pattern scan passed:", len(files), "source/evidence files; local credentials excluded")
