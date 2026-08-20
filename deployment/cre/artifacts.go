// Package cre exposes the CRE contract WASM artifacts produced by
// `stellar contract build` (into target/wasm32v1-none/release/).
//
// These filenames are the source of truth for callers that must select a specific
// artifact out of the shared cargo workspace target dir — e.g. the chainlink CRE test
// harness, which builds the workspace and then reads one wasm by name. Each constant
// must match the corresponding Cargo package name with '-' replaced by '_'; that
// invariant is enforced by artifacts_test.go so a Rust package rename can't silently
// drift from the Go name.
//
// The compiled WASM for each constant is also committed under artifacts/ and
// exposed via Artifact, so Go consumers can deploy the pinned bytecode without
// a Rust toolchain; the check-generated CI job keeps the blobs in sync with
// the contract sources.
package cre

const (
	// ReadFixtureWasm is the CRE ReadContract test fixture (contracts/cre/test/read_fixture).
	ReadFixtureWasm = "read_fixture.wasm"

	// ForwarderWasm is the CRE forwarder (contracts/cre/forwarder).
	ForwarderWasm = "forwarder.wasm"

	// ReceiverWasm is the CRE test receiver (contracts/cre/test/receiver).
	ReceiverWasm = "receiver.wasm"

	// RejectingReceiverWasm is the CRE test receiver that always rejects on_report.
	RejectingReceiverWasm = "rejecting_receiver.wasm"

	// MCMSWasm is the MCMS contract (contracts/mcms).
	MCMSWasm = "mcms.wasm"

	// TimelockWasm is the RBAC Timelock contract (contracts/timelock).
	TimelockWasm = "timelock.wasm"
)
