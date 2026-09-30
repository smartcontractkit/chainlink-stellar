package changesets

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

var offRampDeps = []componentDep{
	{name: "RMN Proxy", ref: stellarccip.RMNProxyDatastoreRef()},
	{name: "TokenAdminRegistry", ref: stellarccip.TokenAdminRegistryDatastoreRef()},
}

// DeployOffRampConfig is the input of the OffRamp deploy changeset. The
// RmnProxy and TokenAdminRegistry strkeys are resolved from the environment
// datastore, not configured here.
type DeployOffRampConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	Owner         string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath      string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
}

// DeployOffRamp deploys and initializes the OffRamp contract on one Stellar
// chain. Requires RMN Proxy and TokenAdminRegistry.
type DeployOffRamp struct{}

var _ cldf.ChangeSetV2[DeployOffRampConfig] = DeployOffRamp{}

func (DeployOffRamp) VerifyPreconditions(e cldf.Environment, cfg DeployOffRampConfig) error {
	return verifyComponent(e, cfg.ChainSelector, offRampDeps, nil)
}

func (DeployOffRamp) Apply(e cldf.Environment, cfg DeployOffRampConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, offRampDeps, cfg.WasmPath, "offramp.wasm", sequences.DeployOffRamp,
		func(resolved map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployOffRampInput {
			return sequences.DeployOffRampInput{
				ChainSelector:      cfg.ChainSelector,
				Owner:              cfg.Owner,
				WasmPath:           wasmPath,
				RmnProxy:           resolved["RMN Proxy"],
				TokenAdminRegistry: resolved["TokenAdminRegistry"],
				ExistingAddresses:  existing,
			}
		})
}
