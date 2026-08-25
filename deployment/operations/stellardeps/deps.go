package stellardeps

import (
	"context"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	"github.com/smartcontractkit/chainlink-stellar/deployment"
)

// SorobanContractDeployer is the minimal surface needed to upload Soroban WASM
// and create a contract instance (contract ID string).
type SorobanContractDeployer interface {
	DeployContract(ctx context.Context, wasmPath string, salt [32]byte) (string, error)
}

// StellarDeps bundles deploy-time and runtime chain I/O used by Soroban
// operations. The same *deployment.Deployer satisfies both interfaces; use
// FromDeployer to wire it without expanding the public Deployer API.
type StellarDeps struct {
	Deploy  SorobanContractDeployer
	Invoker bindings.Invoker
}

// FromDeployer returns deps backed by d for both deploy and invoke/simulate.
// If d is nil, both fields are nil; callers must not use such a value.
func FromDeployer(d *deployment.Deployer) StellarDeps {
	if d == nil {
		return StellarDeps{}
	}
	return StellarDeps{
		Deploy:  d,
		Invoker: d,
	}
}
