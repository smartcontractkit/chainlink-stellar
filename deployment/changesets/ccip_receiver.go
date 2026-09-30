package changesets

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

var ccipReceiverDeps = []componentDep{
	{name: "Router", ref: stellarccip.RouterDatastoreRef()},
}

// DeployCCIPReceiverConfig is the input of the ccip_receiver_example deploy
// changeset. The Router strkey is resolved from the environment datastore,
// not configured here. Enabling remote chains is a config op and stays with
// the orchestrator.
type DeployCCIPReceiverConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	Owner         string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath      string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
}

// DeployCCIPReceiver deploys and initializes the ccip_receiver_example
// contract on one Stellar chain. Requires Router.
type DeployCCIPReceiver struct{}

var _ cldf.ChangeSetV2[DeployCCIPReceiverConfig] = DeployCCIPReceiver{}

func (DeployCCIPReceiver) VerifyPreconditions(e cldf.Environment, cfg DeployCCIPReceiverConfig) error {
	return verifyComponent(e, cfg.ChainSelector, ccipReceiverDeps, nil)
}

func (DeployCCIPReceiver) Apply(e cldf.Environment, cfg DeployCCIPReceiverConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, ccipReceiverDeps, cfg.WasmPath, "ccip_receiver_example.wasm", sequences.DeployCCIPReceiver,
		func(resolved map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployCCIPReceiverInput {
			return sequences.DeployCCIPReceiverInput{
				ChainSelector:     cfg.ChainSelector,
				Owner:             cfg.Owner,
				WasmPath:          wasmPath,
				Router:            resolved["Router"],
				ExistingAddresses: existing,
			}
		})
}
