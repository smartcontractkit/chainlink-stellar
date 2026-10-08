package changesets

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

var committeeVerifierDeps = []componentDep{
	{name: "RMN Proxy", ref: stellarccip.RMNProxyDatastoreRef()},
}

// DeployCommitteeVerifierConfig is the input of the CommitteeVerifier deploy
// changeset. StorageLocations is a required input: the deploy never invents
// one. AllowlistAdmin and FeeAggregator default to the owner and VersionTag
// to the default committee-verifier tag. The RmnProxy strkey is resolved from
// the environment datastore, not configured here. An empty Qualifier deploys
// the default instance; a non-empty one deploys a distinct, extra verifier
// (its own datastore ref and salt).
type DeployCommitteeVerifierConfig struct {
	ChainSelector    uint64   `json:"chainSelector" yaml:"chainSelector"`
	Qualifier        string   `json:"qualifier,omitempty" yaml:"qualifier,omitempty"`
	Owner            string   `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath         string   `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
	StorageLocations [][]byte `json:"storageLocations" yaml:"storageLocations"`
	AllowlistAdmin   string   `json:"allowlistAdmin,omitempty" yaml:"allowlistAdmin,omitempty"`
	FeeAggregator    string   `json:"feeAggregator,omitempty" yaml:"feeAggregator,omitempty"`
	VersionTag       [4]byte  `json:"versionTag" yaml:"versionTag"`
}

// DeployCommitteeVerifier deploys and initializes the CommitteeVerifier
// contract on one Stellar chain. Requires RMN Proxy.
type DeployCommitteeVerifier struct{}

var _ cldf.ChangeSetV2[DeployCommitteeVerifierConfig] = DeployCommitteeVerifier{}

func (DeployCommitteeVerifier) VerifyPreconditions(e cldf.Environment, cfg DeployCommitteeVerifierConfig) error {
	return verifyComponent(e, cfg.ChainSelector, committeeVerifierDeps, func() error {
		if len(cfg.StorageLocations) == 0 {
			return fmt.Errorf("storageLocations is required: pass the committee storage locations")
		}
		return nil
	})
}

func (DeployCommitteeVerifier) Apply(e cldf.Environment, cfg DeployCommitteeVerifierConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, committeeVerifierDeps, cfg.WasmPath, "ccvs_committee_verifier.wasm", sequences.DeployCommitteeVerifier,
		func(resolved map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployCommitteeVerifierInput {
			return sequences.DeployCommitteeVerifierInput{
				ChainSelector:     cfg.ChainSelector,
				Qualifier:         cfg.Qualifier,
				Owner:             cfg.Owner,
				WasmPath:          wasmPath,
				StorageLocations:  cfg.StorageLocations,
				RmnProxy:          resolved["RMN Proxy"],
				AllowlistAdmin:    cfg.AllowlistAdmin,
				FeeAggregator:     cfg.FeeAggregator,
				VersionTag:        cfg.VersionTag,
				ExistingAddresses: existing,
			}
		})
}
