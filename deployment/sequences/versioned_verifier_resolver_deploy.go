package sequences

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	vvrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/versioned_verifier_resolver"
)

// DeployVVRInput deploys and initializes the VersionedVerifierResolver
// contract. FeeAggregator defaults to the owner (the monolith's devenv value).
type DeployVVRInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	FeeAggregator     string                 `json:"feeAggregator,omitempty"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployVVR = cldf_ops.NewSequence(
	"stellar-deploy-versioned-verifier-resolver",
	SequenceVersion,
	"Deploys and initializes the VersionedVerifierResolver Soroban contract with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployVVRInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		feeAggregator := in.FeeAggregator
		if feeAggregator == "" {
			feeAggregator = owner
		}
		if err := statReleaseWasm(in.WasmPath, "VVR"); err != nil {
			return ComponentDeployOutput{}, err
		}
		return deployAndInitialize(b.GetContext(), b, deps, vvrops.Deploy,
			stellarccip.VVRDatastoreRef(), in.ChainSelector, "versioned-verifier-resolver", in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, vvrops.Initialize, vvrops.InitializeInput{
					ContractID:    contractID,
					Owner:         owner,
					FeeAggregator: feeAggregator,
				}, withComponentIdempotencyKey[vvrops.InitializeInput](in.ChainSelector))
				return err
			})
	},
)
