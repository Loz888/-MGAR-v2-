#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
source scripts/dev-env.sh
[[ $# == 1 && $1 == --offline-load ]] || { echo 'usage: eval.sh --offline-load; live evaluation requires P7-06 configuration and explicit authorization' >&2; exit 2; }
exec python eval/load/runner.py --mode load
