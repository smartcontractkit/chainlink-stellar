package sequences

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	rmnproxyops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_proxy"
)

// DeployRMNProxyInput deploys and initializes the RMN Proxy contract. RmnRemote
// is the RMN Remote contract strkey this proxy points at, a required input.
type DeployRMNProxyInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	RmnRemote         string                 `json:"rmnRemote"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployRMNProxy = cldf_ops.NewSequence(
	"stellar-deploy-rmn-proxy",
	SequenceVersion,
	"Deploys and initializes the RMN Proxy Soroban contract with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployRMNProxyInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "RMN Proxy"); err != nil {
			return ComponentDeployOutput{}, err
		}
		if in.RmnRemote == "" {
			return ComponentDeployOutput{}, fmt.Errorf("rmnRemote is required: deploy RMN Remote first")
		}
		return deployAndInitialize(b.GetContext(), b, deps, rmnproxyops.Deploy,
			stellarccip.RMNProxyDatastoreRef(), in.ChainSelector, "rmn-proxy", in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, rmnproxyops.Initialize, rmnproxyops.InitializeInput{
					ContractID: contractID,
					Owner:      owner,
					RmnRemote:  in.RmnRemote,
				}, withComponentIdempotencyKey[rmnproxyops.InitializeInput](in.ChainSelector))
				return err
			})
	},
)
