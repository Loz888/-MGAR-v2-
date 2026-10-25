#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
source scripts/dev-env.sh
[[ $# == 2 && $1 == --set && ( $2 == core || $2 == all ) ]] || { echo 'usage: kill_matrix.sh --set core|all' >&2; exit 2; }
[[ "$(uname -s)" == Linux && "${MGAR_ALLOW_PAID:-0}" == 0 ]] || { echo 'Linux and offline mode required' >&2; exit 2; }
# The Go harness allocates disposable schemas and new child sessions. It accepts
# no arbitrary PID, process group, user directory, or production database target.
if [[ $2 == all ]]; then
  exec python eval/load/runner.py --mode kill
fi
go test -count=1 -timeout=120s -v ./tests/chaos/... -run '^TestKillMatrixCore$'
