package sequences

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	aphxops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/advanced_pool_hooks_extractor"
)

// DeployAdvancedPoolHooksExtractorInput deploys the Advanced Pool Hooks
// Extractor — the stateless policy extractor that projects the pool-hooks
// preflight/postflight payloads into the named Parameter array the policy
// engine evaluates. There is no initialize step (the contract exposes only the
// pure extract / type_and_version views), so the sequence runs skip layers 1-2
// only. Wiring the extractor to a policy engine (set_extractor) is an operator
// action on the engine, not deployment tooling (EVM parity).
type DeployAdvancedPoolHooksExtractorInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	WasmPath          string                 `json:"wasmPath"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployAdvancedPoolHooksExtractor = cldf_ops.NewSequence(
	"stellar-deploy-advanced-pool-hooks-extractor",
	SequenceVersion,
	"Deploys the stateless Advanced Pool Hooks Extractor Soroban contract with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployAdvancedPoolHooksExtractorInput) (ComponentDeployOutput, error) {
		if err := statReleaseWasm(in.WasmPath, "Advanced Pool Hooks Extractor"); err != nil {
			return ComponentDeployOutput{}, err
		}
		return deployStatelessComponent(b.GetContext(), b, deps, aphxops.Deploy,
			stellarccip.AdvancedPoolHooksExtractorDatastoreRef(), in.ChainSelector,
			"advanced-pool-hooks-extractor", in.WasmPath, in.ExistingAddresses)
	},
)
