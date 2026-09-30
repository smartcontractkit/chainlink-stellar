package sequences

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	recvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ccip_receiver"
)

// DeployCCIPReceiverInput deploys and initializes the ccip_receiver_example
// contract (the 12th component; devenv needs it). Router is a required input.
// Enabling remote chains is a config op and stays with the orchestrator.
type DeployCCIPReceiverInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	Router            string                 `json:"router"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployCCIPReceiver = cldf_ops.NewSequence(
	"stellar-deploy-ccip-receiver",
	SequenceVersion,
	"Deploys and initializes the ccip_receiver_example Soroban contract with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployCCIPReceiverInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "ccip_receiver_example"); err != nil {
			return ComponentDeployOutput{}, err
		}
		if in.Router == "" {
			return ComponentDeployOutput{}, fmt.Errorf("router is required: deploy Router first")
		}
		return deployAndInitialize(b.GetContext(), b, deps, recvops.Deploy,
			stellarccip.CCIPReceiverDatastoreRef(), in.ChainSelector, "ccip-receiver-example", in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, recvops.Initialize, recvops.InitializeInput{
					ContractID: contractID,
					Owner:      owner,
					Router:     in.Router,
				}, withComponentIdempotencyKey[recvops.InitializeInput](in.ChainSelector))
				return err
			})
	},
)
