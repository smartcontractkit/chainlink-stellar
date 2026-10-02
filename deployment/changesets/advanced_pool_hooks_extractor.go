package changesets

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// DeployAdvancedPoolHooksExtractorConfig is the input of the Advanced Pool
// Hooks Extractor deploy changeset. The extractor is stateless (only the pure
// extract / type_and_version views), so the changeset deploys and records the
// ref and nothing else. Wiring the extractor to a policy engine (set_extractor)
// is an operator action on the engine, not deploy tooling (EVM parity).
type DeployAdvancedPoolHooksExtractorConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	WasmPath      string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
}

// DeployAdvancedPoolHooksExtractor deploys the stateless Advanced Pool Hooks
// Extractor contract on one Stellar chain. No dependencies.
type DeployAdvancedPoolHooksExtractor struct{}

var _ cldf.ChangeSetV2[DeployAdvancedPoolHooksExtractorConfig] = DeployAdvancedPoolHooksExtractor{}

func (DeployAdvancedPoolHooksExtractor) VerifyPreconditions(e cldf.Environment, cfg DeployAdvancedPoolHooksExtractorConfig) error {
	return verifyComponent(e, cfg.ChainSelector, nil, nil)
}

func (DeployAdvancedPoolHooksExtractor) Apply(e cldf.Environment, cfg DeployAdvancedPoolHooksExtractorConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, nil, cfg.WasmPath, "pools_advanced_pool_hooks_extractor.wasm", sequences.DeployAdvancedPoolHooksExtractor,
		func(_ map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployAdvancedPoolHooksExtractorInput {
			return sequences.DeployAdvancedPoolHooksExtractorInput{
				ChainSelector:     cfg.ChainSelector,
				WasmPath:          wasmPath,
				ExistingAddresses: existing,
			}
		})
}
