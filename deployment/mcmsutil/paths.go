package mcmsutil

import (
	"os"
	"path/filepath"

	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
)

// DefaultMCMSWasmRelative is the release artifact path relative to the chainlink-stellar repo root.
const DefaultMCMSWasmRelative = "target/wasm32v1-none/release/mcms.wasm"

// DefaultTimelockWasmRelative is the timelock WASM path relative to the chainlink-stellar repo root.
const DefaultTimelockWasmRelative = "target/wasm32v1-none/release/timelock.wasm"

// ResolveMCMSWasmPath returns the path to mcms.wasm.
// Order: STELLAR_MCMS_WASM (full path), then CHAINLINK_STELLAR_ROOT + DefaultMCMSWasmRelative, then a walk up from CWD to the repo root.
func ResolveMCMSWasmPath() (string, error) {
	if p := os.Getenv("STELLAR_MCMS_WASM"); p != "" {
		return p, nil
	}
	if root := os.Getenv("CHAINLINK_STELLAR_ROOT"); root != "" {
		return filepath.Join(root, DefaultMCMSWasmRelative), nil
	}
	root, err := stellarutil.FindStellarRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, DefaultMCMSWasmRelative), nil
}

// ResolveTimelockWasmPath returns the path to timelock.wasm.
// Order: STELLAR_TIMELOCK_WASM (full path), then CHAINLINK_STELLAR_ROOT + DefaultTimelockWasmRelative, then a walk up from CWD to the repo root.
func ResolveTimelockWasmPath() (string, error) {
	if p := os.Getenv("STELLAR_TIMELOCK_WASM"); p != "" {
		return p, nil
	}
	if root := os.Getenv("CHAINLINK_STELLAR_ROOT"); root != "" {
		return filepath.Join(root, DefaultTimelockWasmRelative), nil
	}
	root, err := stellarutil.FindStellarRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, DefaultTimelockWasmRelative), nil
}
