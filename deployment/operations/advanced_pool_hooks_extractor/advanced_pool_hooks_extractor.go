package advanced_pool_hooks_extractor

import (
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
)

// ContractType labels the Advanced Pool Hooks Extractor — the policy extractor
// that projects the pool-hooks preflight/postflight payloads into the named
// Parameter array the policy engine evaluates (CCIP 2.0
// "AdvancedPoolHooksExtractor", EVM deploy-advanced-pool-hooks-extractor
// parity).
const ContractType = "AdvancedPoolHooksExtractor"

// Deploy uploads pools_advanced_pool_hooks_extractor.wasm.
//
// The extractor is stateless: it exposes only the pure `extract` /
// `type_and_version` views, so there is no initialize step. Wiring the
// extractor to a policy engine (`set_extractor`) is an operator action on the
// engine itself, not CCIP deployment tooling — as on EVM, where CCIP's
// changeset only deploys the contract.
var Deploy = stellarops.NewDeployOperation(
	"advanced-pool-hooks-extractor:deploy",
	"Deploys the Advanced Pool Hooks Extractor Soroban contract from WASM",
)
