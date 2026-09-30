package sequences

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	cvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
)

// DeployCommitteeVerifierInput deploys and initializes the CommitteeVerifier
// contract. StorageLocations is a required input (G4): the deploy never invents
// one. AllowlistAdmin and FeeAggregator default to the owner, VersionTag to the
// default committee-verifier tag, and RmnProxy is a required dependency.
type DeployCommitteeVerifierInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	StorageLocations  [][]byte               `json:"storageLocations"`
	RmnProxy          string                 `json:"rmnProxy"`
	AllowlistAdmin    string                 `json:"allowlistAdmin,omitempty"`
	FeeAggregator     string                 `json:"feeAggregator,omitempty"`
	VersionTag        [4]byte                `json:"versionTag"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployCommitteeVerifier = cldf_ops.NewSequence(
	"stellar-deploy-committee-verifier",
	SequenceVersion,
	"Deploys and initializes the CommitteeVerifier Soroban contract with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployCommitteeVerifierInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		allowlistAdmin := in.AllowlistAdmin
		if allowlistAdmin == "" {
			allowlistAdmin = owner
		}
		feeAggregator := in.FeeAggregator
		if feeAggregator == "" {
			feeAggregator = owner
		}
		versionTag := in.VersionTag
		if versionTag == [4]byte{} {
			versionTag = stellarutil.DefaultCommitteeVerifierVersionTag()
		}
		if err := statReleaseWasm(in.WasmPath, "Committee Verifier"); err != nil {
			return ComponentDeployOutput{}, err
		}
		if len(in.StorageLocations) == 0 {
			return ComponentDeployOutput{}, fmt.Errorf("storageLocations is required: pass the committee storage locations")
		}
		if in.RmnProxy == "" {
			return ComponentDeployOutput{}, fmt.Errorf("rmnProxy is required: deploy RMN Proxy first")
		}
		return deployAndInitialize(b.GetContext(), b, deps, cvops.Deploy,
			stellarccip.CommitteeVerifierDatastoreRef(), in.ChainSelector, "committee-verifier", in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, cvops.Initialize, cvops.InitializeInput{
					ContractID: contractID,
					Owner:      owner,
					DynamicConfig: cvbindings.DynamicConfig{
						AllowlistAdmin: &allowlistAdmin,
						FeeAggregator:  &feeAggregator,
					},
					StorageLocations: in.StorageLocations,
					RmnProxy:         in.RmnProxy,
					VersionTag:       versionTag,
				}, withComponentIdempotencyKey[cvops.InitializeInput](in.ChainSelector))
				return err
			})
	},
)
