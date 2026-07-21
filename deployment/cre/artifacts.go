// Package cre exposes metadata about the CRE contract WASM artifacts produced by
// `stellar contract build` (into target/wasm32v1-none/release/).
//
// These filenames are the source of truth for callers that must select a specific
// artifact out of the shared cargo workspace target dir — e.g. the chainlink CRE test
// harness, which builds the workspace and then reads one wasm by name. Each constant
// must match the corresponding Cargo package name with '-' replaced by '_'; that
// invariant is enforced by artifacts_test.go so a Rust package rename can't silently
// drift from the Go name.
package cre

const (
	// ReadFixtureWasm is the CRE ReadContract test fixture (contracts/cre/test/read_fixture).
	ReadFixtureWasm = "read_fixture.wasm"

	// ForwarderWasm is the CRE forwarder (contracts/cre/forwarder).
	ForwarderWasm = "forwarder.wasm"

	// ReceiverWasm is the CRE test receiver (contracts/cre/test/receiver).
	ReceiverWasm = "receiver.wasm"
)
