#!/usr/bin/env bash
# Source from a checkout; each cloud task is already isolated (no worktree needed).
MGAR_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
export PATH="/workspace/.mgar-tools/go/bin:$MGAR_ROOT/.venv/bin:$PATH"
export GOTOOLCHAIN=local GOMAXPROCS=4
export GOCACHE=/workspace/.mgar-tools/gocache GOPATH=/workspace/.mgar-tools/gopath
export UV_CACHE_DIR=/workspace/.mgar-tools/uv-cache UV_PYTHON_DOWNLOADS=never
if [[ -f "$MGAR_ROOT/.local/db.env" ]]; then
  set -a
  source "$MGAR_ROOT/.local/db.env"
  set +a
fi
