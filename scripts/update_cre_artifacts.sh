#!/usr/bin/env bash
# Rebuild the embedded CRE contract WASM committed under deployment/cre/artifacts/.
set -euo pipefail
cd "$(dirname "$0")/.."

PACKAGES="forwarder receiver rejecting-receiver read-fixture"
IMAGE="rust:1.93.1@sha256:ecbe59a8408895edd02d9ef422504b8501dd9fa1526de27a45b73406d734d659"
STELLAR_CLI_VERSION="25.1.0"
STELLAR_CLI_SHA256="e6fac619b2ae9b3ecb843a9e8e3bfc94dce79e0b73c63cb1cbbd08682bb0a0ba"

MOUNTS=()
if [[ -n "${CRE_CARGO_REGISTRY_DIR:-}" ]]; then
  mkdir -p "$CRE_CARGO_REGISTRY_DIR"
  MOUNTS+=(-v "$CRE_CARGO_REGISTRY_DIR":/usr/local/cargo/registry)
fi

docker run --rm --platform linux/amd64 \
  -v "$PWD":/src:ro \
  -v "$PWD/deployment/cre/artifacts":/out \
  ${MOUNTS[@]+"${MOUNTS[@]}"} \
  -w /src \
  -e CARGO_TARGET_DIR=/build \
  -e PACKAGES="$PACKAGES" \
  -e STELLAR_CLI_VERSION="$STELLAR_CLI_VERSION" \
  -e STELLAR_CLI_SHA256="$STELLAR_CLI_SHA256" \
  "$IMAGE" bash -c '
    set -euo pipefail
    apt-get update -qq >/dev/null && apt-get install -y -qq libdbus-1-3 >/dev/null
    rustup target add wasm32v1-none >/dev/null 2>&1
    curl --fail --silent --show-error --location \
      --proto "=https" --proto-redir "=https" \
      -o /tmp/stellar-cli.tar.gz \
      "https://github.com/stellar/stellar-cli/releases/download/v${STELLAR_CLI_VERSION}/stellar-cli-${STELLAR_CLI_VERSION}-x86_64-unknown-linux-gnu.tar.gz"
    echo "${STELLAR_CLI_SHA256}  /tmp/stellar-cli.tar.gz" | sha256sum --check --quiet
    tar -xzf /tmp/stellar-cli.tar.gz -C /usr/local/bin
    for pkg in $PACKAGES; do
      stellar contract build --package "$pkg"
      cp "/build/wasm32v1-none/release/${pkg//-/_}.wasm" /out/
    done
  '

echo "Updated:"
git hash-object deployment/cre/artifacts/*.wasm 2>/dev/null || true
