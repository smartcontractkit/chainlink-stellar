package sequences

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	offrampops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/offramp"
)

// DeployOffRampInput deploys and initializes the OffRamp contract. RmnProxy and
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
	"Deploys and initializes the OffRamp Soroban contract with skip-if-exists",
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
		return deployAndInitialize(b.GetContext(), b, deps, offrampops.Deploy,
			stellarccip.OffRampDatastoreRef(), in.ChainSelector, "offramp", in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, offrampops.Initialize, offrampops.InitializeInput{
					ContractID: contractID,
					Owner:      owner,
					Config: offrampbindings.StaticConfig{
						ChainSelector:      in.ChainSelector,
						RmnProxy:           in.RmnProxy,
						TokenAdminRegistry: in.TokenAdminRegistry,
					},
				}, withComponentIdempotencyKey[offrampops.InitializeInput](in.ChainSelector))
				return err
			})
	},
)
