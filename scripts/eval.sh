#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
source scripts/dev-env.sh
if [[ $# == 1 && $1 == --offline-load ]]; then
  exec python eval/load/runner.py --mode load
fi
if [[ ${1:-} == --live ]]; then
  mkdir -p .local/bin
  go build -o .local/bin/mgareval ./cmd/mgareval
  exec .local/bin/mgareval --provider deepseek "$@"
fi
echo 'usage: eval.sh --offline-load | --live --preflight | --live --confirm-live' >&2
exit 2
