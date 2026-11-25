package sequences

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	tarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_admin_registry"
)

// DeployTokenAdminRegistryInput creates the TokenAdminRegistry contract with
// its owner set by the constructor. No params beyond owner and dependencies.
type DeployTokenAdminRegistryInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployTokenAdminRegistry = cldf_ops.NewSequence(
	"stellar-deploy-token-admin-registry",
	SequenceVersion,
	"Creates the TokenAdminRegistry Soroban contract through its constructor with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployTokenAdminRegistryInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "TokenAdminRegistry"); err != nil {
			return ComponentDeployOutput{}, err
		}
		ctorArgs, err := tarbindings.ConstructorArgs(owner)
		if err != nil {
			return ComponentDeployOutput{}, fmt.Errorf("encode TokenAdminRegistry constructor args: %w", err)
		}
		ctx := b.GetContext()
		return deployConstructedComponent(ctx, b, deps, tarops.Deploy,
			stellarccip.TokenAdminRegistryDatastoreRef(), in.ChainSelector, "token-admin-registry", in.WasmPath,
			ctorArgs, in.ExistingAddresses,
			func(contractID string) error {
				c := tarbindings.NewTokenAdminRegistryClient(deps.Invoker, contractID)
				got, err := c.Owner(ctx)
				if err != nil {
					return err
				}
				if err := requireOwner(got, owner); err != nil {
					return err
				}
				tokens, err := c.GetAllConfiguredTokens(ctx, 0, 1)
				if err != nil {
					return err
				}
				if len(tokens) != 0 {
					return fmt.Errorf("registry already has configured tokens, expected an empty registry")
				}
				return nil
			})
	},
)
