#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
if [[ ! -x /workspace/.mgar-tools/go/bin/go ]]; then
  mkdir -p /workspace/.mgar-tools
  archive="$(mktemp /tmp/mgar-go-XXXXXX.tar.gz)"
  trap 'rm -f "$archive"' EXIT
  curl --fail --silent --show-error --location https://go.dev/dl/go1.27.3.linux-amd64.tar.gz --output "$archive"
  printf '%s  %s\n' 65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40 "$archive" | sha256sum --check
  tar -xzf "$archive" -C /workspace/.mgar-tools
fi
source scripts/dev-env.sh
uv sync --frozen
go mod download
python - <<'PY'
from pathlib import Path
import secrets
p=Path('.local/db.env');p.parent.mkdir(mode=0o700,exist_ok=True)
if not p.exists():
    password=secrets.token_hex(24)
    with p.open('x') as f:
        f.write('MGAR_TEST_DB_PASSWORD='+password+'\nMGAR_TEST_DATABASE_URL=postgres://mgar_test_admin:'+password+'@127.0.0.1:55432/mgar_test?sslmode=disable\n')
    p.chmod(0o600)
PY
