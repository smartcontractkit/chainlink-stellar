package changesets

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// DeployVVRConfig is the input of the VersionedVerifierResolver deploy
// changeset. FeeAggregator defaults to the owner. No dependencies.
type DeployVVRConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	Owner         string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath      string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
	FeeAggregator string `json:"feeAggregator,omitempty" yaml:"feeAggregator,omitempty"`
}

// DeployVVR deploys and initializes the VersionedVerifierResolver contract on
// one Stellar chain.
type DeployVVR struct{}

var _ cldf.ChangeSetV2[DeployVVRConfig] = DeployVVR{}

func (DeployVVR) VerifyPreconditions(e cldf.Environment, cfg DeployVVRConfig) error {
	return verifyComponent(e, cfg.ChainSelector, nil, nil)
}

func (DeployVVR) Apply(e cldf.Environment, cfg DeployVVRConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, nil, cfg.WasmPath, "ccvs_versioned_verifier_resolver.wasm", sequences.DeployVVR,
		func(_ map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployVVRInput {
			return sequences.DeployVVRInput{
				ChainSelector:     cfg.ChainSelector,
				Owner:             cfg.Owner,
				WasmPath:          wasmPath,
				FeeAggregator:     cfg.FeeAggregator,
				ExistingAddresses: existing,
			}
		})
}
