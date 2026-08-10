# Default: show available recipes
default:
    just --list

# Coverage exclusion regex (mockery-generated files), aligned with chainlink-ccv Justfile
COVERAGE_EXCLUDE_REGEX := '(/mock_.*\\.go:|/_mocks/.*:|/mocks/.*:)'

# Host target triple (needed to override .cargo/config.toml wasm target for tests)
host_target := `rustc -vV | grep host | awk '{print $2}'`

mod data_feeds 'contracts/data-feeds/Justfile'

# Run all Soroban contract tests
test-contracts:
    cargo test --workspace --target {{host_target}} --verbose
    just data_feeds test

# Check all contracts compile (faster than full test)
check-contracts:
    cargo check --workspace --target {{host_target}}
    just data_feeds check

# Build all contract WASMs
build-contracts:
    stellar contract build
    just data_feeds build

# Rebuild the embedded CRE contract WASM committed under deployment/cre/artifacts/
# (served to Go consumers via deployment/cre.Artifact). CI fails if out of sync.
# Always builds in a pinned linux/amd64 docker container (bytes are
# host-platform-dependent, so there is exactly one canonical environment).
update-cre-artifacts:
    ./scripts/update_cre_artifacts.sh

# Format all Rust code
fmt-contracts:
    cargo fmt --all
    just data_feeds fmt

# Check Rust formatting (CI mode, no changes)
fmt-contracts-check:
    cargo fmt --all -- --check
    just data_feeds fmt-check

# Run contract linters
lint-contracts:
    just data_feeds lint

# Run Go unit tests (root module) with coverage; optional second arg "short" runs -short tests only.
# Excludes tests/e2e (requires running devenv; same idea as chainlink-ccv -short for heavy tests).
# Writes filtered coverprofile to coverage_file (strips mock files matching COVERAGE_EXCLUDE_REGEX).
test-coverage coverage_file="coverage.out" short="":
    #!/usr/bin/env bash
    set -euo pipefail
    pkgs=$(go list ./... | grep -v '/tests/e2e' || true)
    go test -v -race -fullpath -shuffle on {{ if short != "" { "-short" } else { "" } }} -coverprofile={{ coverage_file }} $pkgs
    { head -n1 {{ coverage_file }}; tail -n +2 {{ coverage_file }} | grep -v -E '{{ COVERAGE_EXCLUDE_REGEX }}' || true; } > {{ coverage_file }}.filtered
    mv {{ coverage_file }}.filtered {{ coverage_file }}

# Run Go unit tests (root module) with coverage.
# Excludes tests/e2e (requires running devenv; run those with: go test -v -timeout 10m ./tests/e2e/...).
test-go:
    #!/usr/bin/env bash
    set -euo pipefail
    pkgs=$(go list ./... | grep -v '/tests/e2e' || true)
    go test -v -race -fullpath -shuffle on -coverprofile=coverage.out $pkgs
    go tool cover -func=coverage.out

# Run Go unit tests (bindings module) with coverage
test-go-bindings:
    cd bindings && go test -v -race -fullpath -shuffle on -coverprofile=coverage-bindings.out ./...
    cd bindings && go tool cover -func=coverage-bindings.out

# Run all Go unit tests
test-go-all: test-go test-go-bindings

# Run Go integration tests (requires running Stellar localnet)
test-go-integration:
    go test -tags integration -v -timeout 20m ./tests/integration/...

# Generate mocks using mockery
mock:
    @echo "Cleaning existing mocks..."
    find ./internal/mocks -type f -name 'mock_*.go' -delete 2>/dev/null || true
    @echo "Generating mocks with mockery..."
    mockery

# Run all tests (contracts + Go)
test-all: test-contracts test-go-all

# Full pipeline: build WASM, make generate-interfaces + generate-bindings, fmt, lint, test-all
all: build-contracts
    make generate-interfaces
    make generate-bindings
    just fmt-contracts
    just lint-contracts
    just test-all
