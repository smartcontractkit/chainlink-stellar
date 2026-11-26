package sequences

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	rrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ramp_registry"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	rrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ramp_registry"
)

// DeployRampRegistryInput creates the RampRegistry contract.
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
	"Creates the RampRegistry Soroban contract through its constructor with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployRampRegistryInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "RampRegistry"); err != nil {
			return ComponentDeployOutput{}, err
		}
		ctorArgs, err := rrbindings.ConstructorArgs(owner)
		if err != nil {
			return ComponentDeployOutput{}, fmt.Errorf("encode RampRegistry constructor args: %w", err)
		}
		ctx := b.GetContext()
		return deployConstructedComponent(ctx, b, deps, rrops.Deploy,
			stellarccip.RampRegistryDatastoreRef(), in.ChainSelector, "ramp-registry", in.WasmPath, ctorArgs, in.ExistingAddresses,
			func(contractID string) error {
				c := rrbindings.NewRampRegistryClient(deps.Invoker, contractID)
				gotOwner, err := c.Owner(ctx)
				if err != nil {
					return err
				}
				return requireOwner(gotOwner, owner)
			})
	},
)
