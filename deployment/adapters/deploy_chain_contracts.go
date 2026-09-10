package adapters

import (
	"fmt"

	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// StellarDeployChainContractsAdapter connects Stellar to the shared
// deployment/v2_0_0 DeployChainContracts changeset.
//
// The Stellar deploy sequence derives its on-chain contract parameters from the offchain
// topology stashed during CCV PreDeployContractsForSelector (see
// sequences.StellarDeployChainContracts), so DeployContractParams and DeployerContract are
// threaded through for changeset parity but not consumed by the deploy itself.
type StellarDeployChainContractsAdapter struct{}

var _ ccvadapters.DeployChainContractsAdapter = (*StellarDeployChainContractsAdapter)(nil)

// GetDefaultDeployContractParams implements ccvadapters.DeployChainContractsAdapter.
func (a *StellarDeployChainContractsAdapter) GetDefaultDeployContractParams(_ uint64) ccvadapters.DeployContractParams {
	return ccvadapters.DeployContractParams{}
}

// ResolveDeployAddresses implements ccvadapters.DeployChainContractsAdapter.
// Soroban has no CREATE2-style deployer factory to resolve; this only validates the chain
// is present in the environment.
func (a *StellarDeployChainContractsAdapter) ResolveDeployAddresses(e cldf.Environment, chainSelector uint64) (ccvadapters.DeployChainResolvedAddresses, error) {
	if _, ok := e.BlockChains.StellarChains()[chainSelector]; !ok {
		return ccvadapters.DeployChainResolvedAddresses{}, fmt.Errorf("stellar chain not found for selector %d", chainSelector)
	}
	return ccvadapters.DeployChainResolvedAddresses{}, nil
}

// BuildDeployContractParams implements ccvadapters.DeployChainContractsAdapter.
func (a *StellarDeployChainContractsAdapter) BuildDeployContractParams(input ccvadapters.BuildDeployContractParamsInput) (ccvadapters.DeployContractParams, error) {
	if len(input.CommitteeVerifiers) == 0 {
		return ccvadapters.DeployContractParams{}, fmt.Errorf("chain %d: at least one committee verifier is required", input.ChainSelector)
	}
	params := input.Defaults
	params.CommitteeVerifiers = input.CommitteeVerifiers
	return ccvadapters.ApplyDeployContractParamsOverrides(params, input.Overrides), nil
}

// DeployChainContracts implements ccvadapters.DeployChainContractsAdapter.
func (a *StellarDeployChainContractsAdapter) DeployChainContracts() *cldf_ops.Sequence[ccvadapters.DeployChainContractsInput, ccvadapters.DeployChainContractsOutput, cldf_chain.BlockChains] {
	return sequences.StellarDeployChainContracts
}
