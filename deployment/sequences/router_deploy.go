package sequences

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	routerops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/router"
)

// DeployRouterInput deploys and initializes the Router contract. RmnProxy is a
// required input.
type DeployRouterInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	RmnProxy          string                 `json:"rmnProxy"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployRouter = cldf_ops.NewSequence(
	"stellar-deploy-router",
	SequenceVersion,
	"Deploys and initializes the Router Soroban contract with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployRouterInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "Router"); err != nil {
			return ComponentDeployOutput{}, err
		}
		if in.RmnProxy == "" {
			return ComponentDeployOutput{}, fmt.Errorf("rmnProxy is required: deploy RMN Proxy first")
		}
		return deployAndInitialize(b.GetContext(), b, deps, routerops.Deploy,
			stellarccip.RouterDatastoreRef(), in.ChainSelector, "router", in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, routerops.Initialize, routerops.InitializeInput{
					ContractID: contractID,
					Owner:      owner,
					RmnProxy:   in.RmnProxy,
				}, withComponentIdempotencyKey[routerops.InitializeInput](in.ChainSelector))
				return err
			})
	},
)
