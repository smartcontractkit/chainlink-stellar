package operations

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// NewDeployOperation returns a CLDF operation that deploys WASM using SorobanContractDeployer.
// Use a distinct id per contract family (e.g. "onramp:deploy", "router:deploy").
func NewDeployOperation(id, description string) *cldfops.Operation[DeployInput, DeployOutput, stellardeps.StellarDeps] {
	return cldfops.NewOperation(
		id,
		ContractDeploymentVersion,
		description,
		func(b cldfops.Bundle, d stellardeps.StellarDeps, in DeployInput) (DeployOutput, error) {
			cid, err := d.Deploy.DeployContract(b.GetContext(), in.WasmPath, in.Salt)
			if err != nil {
				return DeployOutput{}, err
			}
			return DeployOutput{ContractID: cid}, nil
		},
	)
}
