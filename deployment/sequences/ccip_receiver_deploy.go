package sequences

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	recvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ccip_receiver"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	recvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ccip_receiver"
)

// DeployCCIPReceiverInput creates the ccip_receiver_example
// contract (the 12th component; devenv needs it). Router is a required input.
// Enabling remote chains is a config op and stays with the orchestrator.
// Qualifier deploys an additional receiver instance recorded under that
// qualifier (empty keeps the original instance); the deploy salt and
// idempotency key are suffixed with it so each instance lands at its own
// deterministic contract ID.
type DeployCCIPReceiverInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	Router            string                 `json:"router"`
	Qualifier         string                 `json:"qualifier,omitempty"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployCCIPReceiver = cldf_ops.NewSequence(
	"stellar-deploy-ccip-receiver",
	SequenceVersion,
	"Creates the ccip_receiver_example Soroban contract through its constructor with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployCCIPReceiverInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "ccip_receiver_example"); err != nil {
			return ComponentDeployOutput{}, err
		}
		if in.Router == "" {
			return ComponentDeployOutput{}, fmt.Errorf("router is required: deploy Router first")
		}
		ctorArgs, err := recvbindings.ConstructorArgs(owner, in.Router)
		if err != nil {
			return ComponentDeployOutput{}, fmt.Errorf("encode ccip_receiver_example constructor args: %w", err)
		}
		ctx := b.GetContext()
		return deployConstructedComponent(ctx, b, deps, recvops.Deploy,
			stellarccip.CCIPReceiverDatastoreRefWithQualifier(in.Qualifier), in.ChainSelector,
			componentSaltLabel("ccip-receiver-example", in.Qualifier), in.WasmPath, ctorArgs, in.ExistingAddresses,
			func(contractID string) error {
				c := recvbindings.NewExampleCcipReceiverClient(deps.Invoker, contractID)
				gotOwner, err := c.Owner(ctx)
				if err != nil {
					return err
				}
				if err := requireOwner(gotOwner, owner); err != nil {
					return err
				}
				router, err := c.GetRouter(ctx)
				if err != nil {
					return err
				}
				return requireField("router", router, in.Router)
			})
	},
)
