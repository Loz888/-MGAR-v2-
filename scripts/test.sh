#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
source scripts/dev-env.sh
if [[ "${1:-}" == --security-sandbox ]]; then
  go test -count=1 -v ./internal/policy/... ./tests/security/... ./tests/e2e/... -run 'Sandbox|ReexecGoTest'
  exit 0
fi
[[ $# == 1 && ( "$1" == --offline || "$1" == --full ) ]] || { echo 'use --offline, --full (engineering only), or --security-sandbox' >&2; exit 2; }
# --full means all enabled engineering checks; P7-06 live authorization remains separate.
[[ "$1" != --full ]] || echo 'ENGINEERING_ONLY: complete offline suite with real PostgreSQL, Docker and process faults'
bash scripts/preflight.sh
mkdir -p .local
rm -f .local/offline-current
run_dir="$(mktemp -d "$MGAR_ROOT/.local/offline-XXXXXXXX")"
python scripts/source_fingerprint.py > "$run_dir/fingerprint"
go test -race -count=1 -json ./... > "$run_dir/go.json"
go vet ./... > "$run_dir/vet.log" 2>&1
python -m pytest -q -p scripts.pytest_evidence --mgar-report="$run_dir/pytest-events.json" --junitxml="$run_dir/pytest.xml" > "$run_dir/pytest.log" 2>&1
ruff check . > "$run_dir/ruff.log" 2>&1
mypy > "$run_dir/mypy.log" 2>&1
python scripts/validate_plan_docs.py > "$run_dir/docs.json"
python scripts/verify_results.py --go "$run_dir/go.json" --junit "$run_dir/pytest.xml" --pytest-events "$run_dir/pytest-events.json" > "$run_dir/report.json"
printf '%s\n' "$run_dir" > .local/offline-current
cat "$run_dir/report.json"
