WASM_DIR := target/wasm32v1-none/release

.PHONY: build test test-e2e check fmt clean generate-interfaces generate-bindings docker-verifier docker-executor restart-verifier restart-executor restart-verifier-executor

build:
	stellar contract build

test-e2e:
	go test -v -timeout 30m ./tests/e2e/...

test-integration:
	go test -v -tags=integration -count=1 -p=1 -timeout=15m ./tests/integration/...

# Generate Rust interface files for all contracts from their WASM files.
# This can be run with `--no-build` to skip the build of the contracts.
generate-interfaces:
	./scripts/gen_interfaces.sh && cargo fmt -p common-interfaces

# Generate Go bindings for all contracts from their Rust interface files.
# This can be run with `--no-interfaces` to skip the 
# generation of the Rust interface files.
generate-bindings:
	./scripts/gen_bindings.sh && gofmt -w ./bindings/contracts/

test:
	cargo test --workspace

check:
	cargo check --workspace

lint:
	cargo fmt --all

clean:
	cargo clean

# Build the Stellar committee verifier Docker image used by E2E tests.
docker-verifier:
	docker build -f Dockerfile.verifier -t stellarcommittee-verifier:dev .

# Build the Stellar (standalone) executor Docker image used by E2E tests.
docker-executor:
	docker build -f Dockerfile.executor -t stellarexecutor:dev .

# Rebuild the verifier image and restart the devenv to pick up the new image.
restart-verifier: docker-verifier
	$(MAKE) down && $(MAKE) up

# Rebuild the executor image and restart the devenv to pick up the new image.
restart-executor: docker-executor
	$(MAKE) down && $(MAKE) up

# Rebuild both verifier and executor images and restart the devenv to pick up the new images.
restart-verifier-executor: docker-verifier docker-executor
	$(MAKE) down && $(MAKE) up

up:
	CTF_CONFIGS=tests/env/env-stellar-evm.toml go run ./tests/testutils/cmd/devenv up tests/env/env-stellar-evm.toml

down:
	CTF_CONFIGS=tests/env/env-stellar-evm.toml go run ./tests/testutils/cmd/devenv down tests/env/env-stellar-evm.toml
