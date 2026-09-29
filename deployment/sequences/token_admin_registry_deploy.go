package sequences

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	tarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_admin_registry"
)

// DeployTokenAdminRegistryInput deploys and initializes the TokenAdminRegistry
// contract. No params beyond owner and dependencies.
type DeployTokenAdminRegistryInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployTokenAdminRegistry = cldf_ops.NewSequence(
	"stellar-deploy-token-admin-registry",
	SequenceVersion,
	"Deploys and initializes the TokenAdminRegistry Soroban contract with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployTokenAdminRegistryInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "TokenAdminRegistry"); err != nil {
			return ComponentDeployOutput{}, err
		}
		return deployAndInitialize(b.GetContext(), b, deps, tarops.Deploy,
			stellarccip.TokenAdminRegistryDatastoreRef(), in.ChainSelector, "token-admin-registry", in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, tarops.Initialize, tarops.InitializeInput{
					ContractID: contractID,
					Owner:      owner,
				}, withComponentIdempotencyKey[tarops.InitializeInput](in.ChainSelector))
				return err
			})
	},
)
