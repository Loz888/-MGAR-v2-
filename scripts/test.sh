#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
source scripts/dev-env.sh
[[ "${1:-}" == --offline ]] || { echo 'only --offline supported' >&2; exit 2; }
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
