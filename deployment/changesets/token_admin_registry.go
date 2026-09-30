package changesets

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// DeployTokenAdminRegistryConfig is the input of the TokenAdminRegistry deploy
// changeset. No params beyond owner, and no dependencies.
type DeployTokenAdminRegistryConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	Owner         string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath      string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
}

// DeployTokenAdminRegistry deploys and initializes the TokenAdminRegistry
// contract on one Stellar chain.
type DeployTokenAdminRegistry struct{}

var _ cldf.ChangeSetV2[DeployTokenAdminRegistryConfig] = DeployTokenAdminRegistry{}

func (DeployTokenAdminRegistry) VerifyPreconditions(e cldf.Environment, cfg DeployTokenAdminRegistryConfig) error {
	return verifyComponent(e, cfg.ChainSelector, nil, nil)
}

func (DeployTokenAdminRegistry) Apply(e cldf.Environment, cfg DeployTokenAdminRegistryConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, nil, cfg.WasmPath, "token_admin_registry.wasm", sequences.DeployTokenAdminRegistry,
		func(_ map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployTokenAdminRegistryInput {
			return sequences.DeployTokenAdminRegistryInput{
				ChainSelector:     cfg.ChainSelector,
				Owner:             cfg.Owner,
				WasmPath:          wasmPath,
				ExistingAddresses: existing,
			}
		})
}
