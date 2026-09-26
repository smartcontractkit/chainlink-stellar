package stellarutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// releaseWasmDir is the directory holding freshly-compiled release WASMs,
// relative to the chainlink-stellar repo root.
const releaseWasmDir = "target/wasm32v1-none/release"

// upgradeWasmByContractType maps each upgradeable contract's ContractType
// (as declared by its deployment/operations/<x> package) to the release WASM
// filename produced by `stellar contract build` / `make build`. The filename
// is NOT always 1:1 with the type string (e.g. pools_*, ccvs_*, the example
// receiver), so it is centralized here rather than derived.
var upgradeWasmByContractType = map[string]string{
	"OnRamp":                    "onramp.wasm",
	"OffRamp":                   "offramp.wasm",
	"Router":                    "router.wasm",
	"FeeQuoter":                 "fee_quoter.wasm",
	"TokenAdminRegistry":        "token_admin_registry.wasm",
	"Executor":                  "executor.wasm",
	"CommitteeVerifier":         "ccvs_committee_verifier.wasm",
	"VersionedVerifierResolver": "ccvs_versioned_verifier_resolver.wasm",
	"RmnProxy":                  "rmn_proxy.wasm",
	"RmnRemote":                 "rmn_remote.wasm",
	"BurnMintPool":              "pools_burn_mint_pool.wasm",
	"LockReleasePool":           "pools_lock_release_pool.wasm",
	"SiloedLockReleasePool":     "pools_siloed_lock_release_pool.wasm",
	"TokenLockBox":              "pools_token_lock_box.wasm",
	"AdvancedPoolHooks":         "pools_advanced_pool_hooks.wasm",
	"RampRegistry":              "ccip_ramp_registry.wasm",
	"MCMS":                      "mcms.wasm",
	"CCIPReceiver":              "ccip_receiver_example.wasm",
}

// UpgradeWasmFilename returns the release WASM filename for the given contract
// type, or false if the type is not an upgradeable CCIP contract.
func UpgradeWasmFilename(contractType string) (string, bool) {
	name, ok := upgradeWasmByContractType[contractType]
	return name, ok
}

// ResolveUpgradeWasmPath returns the path to the freshly-compiled release WASM
// for the given contract type. Resolution order (mirrors ResolveMCMSWasmPath /
// ResolveTimelockWasmPath):
//  1. STELLAR_<UPPER_TYPE>_WASM env var (full path override, e.g.
//     STELLAR_FEEQUOTER_WASM=/tmp/fee_quoter.wasm).
//  2. CHAINLINK_STELLAR_ROOT (module-validated) + releaseWasmDir/<name>.wasm.
//  3. cwd-relative releaseWasmDir/<name>.wasm.
//
// The override lets an operator point at a specific artifact (e.g. one built in
// a different checkout) without rebuilding in place.
func ResolveUpgradeWasmPath(contractType string) (string, error) {
	name, ok := upgradeWasmByContractType[contractType]
	if !ok {
		return "", fmt.Errorf("no upgrade WASM mapping for contract type %q", contractType)
	}
	rel := filepath.Join(releaseWasmDir, name)

	envName := "STELLAR_" + strings.ToUpper(contractType) + "_WASM"
	if p := os.Getenv(envName); p != "" {
		return p, nil
	}
	root, err := FindStellarRoot()
	if err == nil {
		return filepath.Join(root, rel), nil
	}
	// Fall back to cwd-relative when the root cannot be located (e.g. the
	// process CWD is the repo root itself).
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, rel), nil
}
