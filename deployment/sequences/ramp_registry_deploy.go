package sequences

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	rrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ramp_registry"
)

// DeployRampRegistryInput deploys and initializes the RampRegistry contract.
// The ramp map updates are config ops and stay with the orchestrator.
type DeployRampRegistryInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployRampRegistry = cldf_ops.NewSequence(
	"stellar-deploy-ramp-registry",
	SequenceVersion,
	"Deploys and initializes the RampRegistry Soroban contract with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployRampRegistryInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "RampRegistry"); err != nil {
			return ComponentDeployOutput{}, err
		}
		return deployAndInitialize(b.GetContext(), b, deps, rrops.Deploy,
			stellarccip.RampRegistryDatastoreRef(), in.ChainSelector, "ramp-registry", in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, rrops.Initialize, rrops.InitializeInput{
					ContractID: contractID,
					Owner:      owner,
				}, withComponentIdempotencyKey[rrops.InitializeInput](in.ChainSelector))
				return err
			})
	},
)
