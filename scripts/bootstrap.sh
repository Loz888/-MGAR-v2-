#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
source scripts/dev-env.sh
[[ "${1:-}" == --fresh-test-db ]] || { echo 'use --fresh-test-db (synthetic only)' >&2; exit 2; }
docker compose --env-file .local/db.env -f deploy/compose.yaml up -d --wait
db_name="mgar_test_$(python -c 'import secrets;print(secrets.token_hex(8))')"
docker compose --env-file .local/db.env -f deploy/compose.yaml exec -T postgres createdb -U mgar_test_admin --owner=mgar_test_admin "$db_name"
python - "$db_name" <<'PY'
from pathlib import Path
import sys,urllib.parse
p=Path('.local/db.env');values=dict(line.split('=',1) for line in p.read_text().splitlines())
u=urllib.parse.urlsplit(values['MGAR_TEST_DATABASE_URL']);values['MGAR_TEST_DATABASE_URL']=urllib.parse.urlunsplit(u._replace(path='/'+sys.argv[1]))
p.write_text(''.join(k+'='+v+'\n' for k,v in values.items()))
PY
if [[ -d cmd/mgarmigrate ]]; then
  source scripts/dev-env.sh
  go run ./cmd/mgarmigrate
fi
printf 'Prepared new synthetic database: %s\n' "$db_name"
