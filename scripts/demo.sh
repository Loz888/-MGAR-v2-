#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
source scripts/dev-env.sh
# Validate the independent frozen manifest before opening a synthetic namespace.
python eval/oracle/validate_manifest.py >/dev/null
build_dir="$(mktemp -d .local/demo-bin.XXXXXXXX)"
trap 'rm -rf -- "$build_dir"' EXIT
go build -o "$build_dir/mgardemo" ./cmd/mgardemo
"$build_dir/mgardemo" "$@"
