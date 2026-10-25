#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
source scripts/dev-env.sh
python eval/oracle/validate_manifest.py >/dev/null
build_dir="$(mktemp -d .local/baseline-bin.XXXXXXXX)"
trap 'rm -rf -- "$build_dir"' EXIT
go build -o "$build_dir/mgarbaseline" ./cmd/mgarbaseline
"$build_dir/mgarbaseline" "$@"
