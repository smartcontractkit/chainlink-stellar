package changesets

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

var rmnProxyDeps = []componentDep{
	{name: "RMN Remote", ref: stellarccip.RMNRemoteDatastoreRef()},
}

// DeployRMNProxyConfig is the input of the RMN Proxy deploy changeset. The
// RmnRemote strkey is resolved from the environment datastore, not
// configured here.
type DeployRMNProxyConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	Owner         string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath      string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
}

// DeployRMNProxy deploys and initializes the RMN Proxy contract on one
// Stellar chain. Requires RMN Remote.
type DeployRMNProxy struct{}

var _ cldf.ChangeSetV2[DeployRMNProxyConfig] = DeployRMNProxy{}

func (DeployRMNProxy) VerifyPreconditions(e cldf.Environment, cfg DeployRMNProxyConfig) error {
	return verifyComponent(e, cfg.ChainSelector, rmnProxyDeps, nil)
}

func (DeployRMNProxy) Apply(e cldf.Environment, cfg DeployRMNProxyConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, rmnProxyDeps, cfg.WasmPath, "rmn_proxy.wasm", sequences.DeployRMNProxy,
		func(resolved map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployRMNProxyInput {
			return sequences.DeployRMNProxyInput{
				ChainSelector:     cfg.ChainSelector,
				Owner:             cfg.Owner,
				WasmPath:          wasmPath,
				RmnRemote:         resolved["RMN Remote"],
				ExistingAddresses: existing,
			}
		})
}
