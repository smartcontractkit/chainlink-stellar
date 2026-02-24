WASM_DIR := target/wasm32v1-none/release

.PHONY: build test check fmt clean generate-interfaces generate-bindings

build:
	stellar contract build

# Generate Rust interface files for all contracts from their WASM files.
# This can be run with `--no-build` to skip the build of the contracts.
generate-interfaces:
	./scripts/gen_interfaces.sh && cargo fmt -p common-interfaces

# Generate Go bindings for all contracts from their Rust interface files.
# This can be run with `--no-interfaces` to skip the 
# generation of the Rust interface files.
generate-bindings:
	./scripts/gen_bindings.sh && gofmt -w ./bindings/contracts

test:
	cargo test --workspace

check:
	cargo check --workspace

lint:
	cargo fmt --all

clean:
	cargo clean
