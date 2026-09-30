package changesets

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// DeployRampRegistryConfig is the input of the RampRegistry deploy changeset.
// The ramp map updates are config ops and stay with the orchestrator. No
// dependencies.
type DeployRampRegistryConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	Owner         string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath      string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
}

// DeployRampRegistry deploys and initializes the RampRegistry contract on one
// Stellar chain.
type DeployRampRegistry struct{}

var _ cldf.ChangeSetV2[DeployRampRegistryConfig] = DeployRampRegistry{}

func (DeployRampRegistry) VerifyPreconditions(e cldf.Environment, cfg DeployRampRegistryConfig) error {
	return verifyComponent(e, cfg.ChainSelector, nil, nil)
}

func (DeployRampRegistry) Apply(e cldf.Environment, cfg DeployRampRegistryConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, nil, cfg.WasmPath, "ccip_ramp_registry.wasm", sequences.DeployRampRegistry,
		func(_ map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployRampRegistryInput {
			return sequences.DeployRampRegistryInput{
				ChainSelector:     cfg.ChainSelector,
				Owner:             cfg.Owner,
				WasmPath:          wasmPath,
				ExistingAddresses: existing,
			}
		})
}
