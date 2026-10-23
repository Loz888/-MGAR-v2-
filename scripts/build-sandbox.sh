#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
source scripts/dev-env.sh
sandbox_context="$(mktemp -d "$MGAR_ROOT/.local/sandbox-build-XXXXXXXX")"
trap 'rm -rf -- "$sandbox_context"' EXIT
mkdir -m 1777 "$sandbox_context/tmp"
cp -a -- "$(go env GOROOT)" "$sandbox_context/go"
chmod -R a+rX "$sandbox_context/go"
CGO_ENABLED=0 go build -trimpath -o "$sandbox_context/entrypoint" ./cmd/mgar-sandbox
chmod 0755 "$sandbox_context/entrypoint"
cp deploy/Dockerfile.sandbox "$sandbox_context/Dockerfile"
# Clear endpoint selectors only. Preserve the managed registry/proxy config.
env -u DOCKER_HOST -u DOCKER_CONTEXT -u DOCKER_TLS -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH \
 BUILDX_CONFIG="$MGAR_ROOT/.local/buildx" docker --host=unix:///var/run/docker.sock build --network=none --provenance=false --iidfile "$sandbox_context/image.id" --tag mgar-v2-sandbox:local "$sandbox_context"
cp "$sandbox_context/image.id" .local/sandbox-image.id
cat .local/sandbox-image.id
