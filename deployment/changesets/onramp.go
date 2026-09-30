package changesets

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

var onRampDeps = []componentDep{
	{name: "TokenAdminRegistry", ref: stellarccip.TokenAdminRegistryDatastoreRef()},
	{name: "RMN Proxy", ref: stellarccip.RMNProxyDatastoreRef()},
	{name: "FeeQuoter", ref: stellarccip.FeeQuoterDatastoreRef()},
}

// DeployOnRampConfig is the input of the OnRamp deploy changeset. The
// TokenAdminRegistry, RmnProxy and FeeQuoter strkeys are resolved from the
// environment datastore, not configured here. MaxUsdCentsPerMessage defaults
// to 500000 ($5000) and FeeAggregator to the owner.
type DeployOnRampConfig struct {
	ChainSelector         uint64 `json:"chainSelector" yaml:"chainSelector"`
	Owner                 string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath              string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
	MaxUsdCentsPerMessage uint32 `json:"maxUsdCentsPerMessage,omitempty" yaml:"maxUsdCentsPerMessage,omitempty"`
	FeeAggregator         string `json:"feeAggregator,omitempty" yaml:"feeAggregator,omitempty"`
}

// DeployOnRamp deploys and initializes the OnRamp contract on one Stellar
// chain. Requires TokenAdminRegistry, RMN Proxy and FeeQuoter.
type DeployOnRamp struct{}

var _ cldf.ChangeSetV2[DeployOnRampConfig] = DeployOnRamp{}

func (DeployOnRamp) VerifyPreconditions(e cldf.Environment, cfg DeployOnRampConfig) error {
	return verifyComponent(e, cfg.ChainSelector, onRampDeps, nil)
}

func (DeployOnRamp) Apply(e cldf.Environment, cfg DeployOnRampConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, onRampDeps, cfg.WasmPath, "onramp.wasm", sequences.DeployOnRamp,
		func(resolved map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployOnRampInput {
			return sequences.DeployOnRampInput{
				ChainSelector:         cfg.ChainSelector,
				Owner:                 cfg.Owner,
				WasmPath:              wasmPath,
				TokenAdminRegistry:    resolved["TokenAdminRegistry"],
				RmnProxy:              resolved["RMN Proxy"],
				FeeQuoter:             resolved["FeeQuoter"],
				MaxUsdCentsPerMessage: cfg.MaxUsdCentsPerMessage,
				FeeAggregator:         cfg.FeeAggregator,
				ExistingAddresses:     existing,
			}
		})
}
