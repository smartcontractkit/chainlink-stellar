default: build

test: build
	cargo test

build:
	stellar contract build --package mock-forwarder
	@ls -l ../../../target/wasm32v1-none/release/mock_forwarder.wasm

fmt:
	cargo fmt --all
