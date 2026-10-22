#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
source scripts/dev-env.sh
[[ "$(uname -s)" == Linux ]] || { echo 'Linux/WSL2 required for process-fault tests' >&2; exit 1; }
[[ "${MGAR_ALLOW_PAID:-0}" == 0 ]] || { echo 'offline suite forbids paid mode' >&2; exit 1; }
go version
python --version
docker compose version
python scripts/check_secrets.py
