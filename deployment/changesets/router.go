package changesets

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

var routerDeps = []componentDep{
	{name: "RMN Proxy", ref: stellarccip.RMNProxyDatastoreRef()},
}

// DeployRouterConfig is the input of the Router deploy changeset. The
// RmnProxy strkey is resolved from the environment datastore, not configured
// here.
type DeployRouterConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	Owner         string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath      string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
}

// DeployRouter deploys and initializes the Router contract on one Stellar
// chain. Requires RMN Proxy.
type DeployRouter struct{}

var _ cldf.ChangeSetV2[DeployRouterConfig] = DeployRouter{}

func (DeployRouter) VerifyPreconditions(e cldf.Environment, cfg DeployRouterConfig) error {
	return verifyComponent(e, cfg.ChainSelector, routerDeps, nil)
}

func (DeployRouter) Apply(e cldf.Environment, cfg DeployRouterConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, routerDeps, cfg.WasmPath, "router.wasm", sequences.DeployRouter,
		func(resolved map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployRouterInput {
			return sequences.DeployRouterInput{
				ChainSelector:     cfg.ChainSelector,
				Owner:             cfg.Owner,
				WasmPath:          wasmPath,
				RmnProxy:          resolved["RMN Proxy"],
				ExistingAddresses: existing,
			}
		})
}
