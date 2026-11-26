package sequences

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	offrampops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/offramp"
)

// DeployOffRampInput creates the OffRamp contract. RmnProxy and
// TokenAdminRegistry are the dependency strkeys, required inputs.
type DeployOffRampInput struct {
	ChainSelector      uint64                 `json:"chainSelector"`
	Owner              string                 `json:"owner,omitempty"`
	WasmPath           string                 `json:"wasmPath"`
	RmnProxy           string                 `json:"rmnProxy"`
	TokenAdminRegistry string                 `json:"tokenAdminRegistry"`
	ExistingAddresses  []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployOffRamp = cldf_ops.NewSequence(
	"stellar-deploy-offramp",
	SequenceVersion,
	"Creates the OffRamp Soroban contract through its constructor with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployOffRampInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "OffRamp"); err != nil {
			return ComponentDeployOutput{}, err
		}
		if in.RmnProxy == "" {
			return ComponentDeployOutput{}, fmt.Errorf("rmnProxy is required: deploy RMN Proxy first")
		}
		if in.TokenAdminRegistry == "" {
			return ComponentDeployOutput{}, fmt.Errorf("tokenAdminRegistry is required: deploy TokenAdminRegistry first")
		}
		staticConfig := offrampbindings.StaticConfig{
			ChainSelector:      in.ChainSelector,
			RmnProxy:           in.RmnProxy,
			TokenAdminRegistry: in.TokenAdminRegistry,
		}
		ctorArgs, err := offrampbindings.ConstructorArgs(owner, staticConfig)
		if err != nil {
			return ComponentDeployOutput{}, fmt.Errorf("encode OffRamp constructor args: %w", err)
		}
		ctx := b.GetContext()
		return deployConstructedComponent(ctx, b, deps, offrampops.Deploy,
			stellarccip.OffRampDatastoreRef(), in.ChainSelector, "offramp", in.WasmPath, ctorArgs, in.ExistingAddresses,
			func(contractID string) error {
				c := offrampbindings.NewOffRampClient(deps.Invoker, contractID)
				gotOwner, err := c.Owner(ctx)
				if err != nil {
					return err
				}
				if err := requireOwner(gotOwner, owner); err != nil {
					return err
				}
				static, err := c.GetStaticConfig(ctx)
				if err != nil {
					return err
				}
				return requireField("static config", *static, staticConfig)
			})
	},
)
