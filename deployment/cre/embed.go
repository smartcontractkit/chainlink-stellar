package cre

import (
	"embed"
	"fmt"
)

// artifactsFS holds the compiled CRE contract WASM committed under artifacts/.
// The blobs are kept in sync with the contracts/cre sources by the
// Check Generated Code CI job (make update-cre-artifacts + git diff), so the
// bytes at any commit are the build of that commit's sources with the pinned
// toolchain (rust-toolchain.toml + CI's stellar-cli).
//
// Consumers that pin this module in go.mod (e.g. chainlink/deployment's
// BuildStellar) can deploy these bytes directly instead of compiling the
// workspace from source at deploy time.
//
//go:embed artifacts/*.wasm
var artifactsFS embed.FS

// Artifact returns the compiled WASM for one of the artifact filename
// constants above, e.g. Artifact(ForwarderWasm).
func Artifact(name string) ([]byte, error) {
	switch name {
	case ForwarderWasm, ReceiverWasm, RejectingReceiverWasm, ReadFixtureWasm:
	default:
		return nil, fmt.Errorf("unknown CRE artifact %q", name)
	}
	wasm, err := artifactsFS.ReadFile("artifacts/" + name)
	if err != nil {
		return nil, fmt.Errorf("embedded CRE artifact %q: %w", name, err)
	}
	return wasm, nil
}
