#!/usr/bin/env bash
# Rebuild the embedded data feeds contract WASM committed under deployment/data-feeds/artifacts/.
set -euo pipefail
cd "$(dirname "$0")/.."

# The data feeds contracts live in a nested cargo workspace with its own
# soroban-sdk pin, so every build must go through --manifest-path; the root
# workspace does not descend into it.
MANIFEST="contracts/data-feeds/Cargo.toml"
PACKAGES="data-feeds-cache data-feeds-proxy"

# Pinned to contracts/data-feeds/rust-toolchain.toml (1.96.0) and the stellar-cli
# that supports its soroban-sdk 26.1. Both differ from the root workspace's pins,
# which is why this script exists separately from update_cre_artifacts.sh.
IMAGE="rust:1.96.0@sha256:58fe97504a0e4cbba5d85599619a589923d3e779472a6fb0840d58d1c4ba99d7"
STELLAR_CLI_VERSION="27.0.0"
STELLAR_CLI_SHA256="357bf712f6353c28cd33c794402a3c87231757a5b305e6ef1604365af4fdd556"

MOUNTS=()
if [[ -n "${CRE_CARGO_REGISTRY_DIR:-}" ]]; then
  mkdir -p "$CRE_CARGO_REGISTRY_DIR"
  MOUNTS+=(-v "$CRE_CARGO_REGISTRY_DIR":/usr/local/cargo/registry)
fi

# Always builds in a pinned linux/amd64 container: the wasm bytes are
# host-platform-dependent, so there is exactly one canonical build environment.
docker run --rm --platform linux/amd64 \
  -v "$PWD":/src:ro \
  -v "$PWD/deployment/data-feeds/artifacts":/out \
  ${MOUNTS[@]+"${MOUNTS[@]}"} \
  -w /src \
  -e CARGO_TARGET_DIR=/build \
  -e MANIFEST="$MANIFEST" \
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
      stellar contract build --manifest-path "$MANIFEST" --package "$pkg"
      cp "/build/wasm32v1-none/release/${pkg//-/_}.wasm" /out/
    done
  '

echo "Updated:"
git hash-object deployment/data-feeds/artifacts/*.wasm 2>/dev/null || true
