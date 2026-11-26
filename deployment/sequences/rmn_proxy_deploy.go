package sequences

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	rmnproxybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_proxy"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	rmnproxyops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_proxy"
)

// DeployRMNProxyInput creates the RMN Proxy contract. RmnRemote
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
	"Creates the RMN Proxy Soroban contract through its constructor with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployRMNProxyInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		if err := statReleaseWasm(in.WasmPath, "RMN Proxy"); err != nil {
			return ComponentDeployOutput{}, err
		}
		if in.RmnRemote == "" {
			return ComponentDeployOutput{}, fmt.Errorf("rmnRemote is required: deploy RMN Remote first")
		}
		ctorArgs, err := rmnproxybindings.ConstructorArgs(owner, in.RmnRemote)
		if err != nil {
			return ComponentDeployOutput{}, fmt.Errorf("encode RMN Proxy constructor args: %w", err)
		}
		ctx := b.GetContext()
		return deployConstructedComponent(ctx, b, deps, rmnproxyops.Deploy,
			stellarccip.RMNProxyDatastoreRef(), in.ChainSelector, "rmn-proxy", in.WasmPath, ctorArgs, in.ExistingAddresses,
			func(contractID string) error {
				c := rmnproxybindings.NewRmnProxyClient(deps.Invoker, contractID)
				gotOwner, err := c.Owner(ctx)
				if err != nil {
					return err
				}
				if err := requireOwner(gotOwner, owner); err != nil {
					return err
				}
				rmn, err := c.GetRmn(ctx)
				if err != nil {
					return err
				}
				return requireField("rmn", rmn, in.RmnRemote)
			})
	},
)
