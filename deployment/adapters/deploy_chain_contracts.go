package adapters

import (
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// StellarDeployChainContractsAdapter connects Stellar to the shared
// deployment/v2_0_0 DeployChainContracts changeset.
type StellarDeployChainContractsAdapter struct{}

var _ ccvadapters.DeployChainContractsAdapter = (*StellarDeployChainContractsAdapter)(nil)

// GetDefaultDeployContractParams returns empty defaults: StellarDeployChainContracts derives
// its contract parameters from the stashed offchain topology, not from ContractParams.
func (a *StellarDeployChainContractsAdapter) GetDefaultDeployContractParams(_ uint64) ccvadapters.DeployContractParams {
	return ccvadapters.DeployContractParams{}
}

// ResolveDeployAddresses has nothing to resolve: the deployer account uploads and
// instantiates Soroban contracts directly, so there is no deployer contract.
func (a *StellarDeployChainContractsAdapter) ResolveDeployAddresses(_ deployment.Environment, _ uint64) (ccvadapters.DeployChainResolvedAddresses, error) {
	return ccvadapters.DeployChainResolvedAddresses{}, nil
}

func (a *StellarDeployChainContractsAdapter) BuildDeployContractParams(input ccvadapters.BuildDeployContractParamsInput) (ccvadapters.DeployContractParams, error) {
	params := input.Defaults
	params.CommitteeVerifiers = input.CommitteeVerifiers
	return ccvadapters.ApplyDeployContractParamsOverrides(params, input.Overrides), nil
}

// SetContractParamsFromImportedConfig is the pass-through sequence of the legacy config-import path.
func (a *StellarDeployChainContractsAdapter) SetContractParamsFromImportedConfig() *cldf_ops.Sequence[ccvadapters.DeployChainConfigCreatorInput, ccvadapters.DeployContractParams, cldf_chain.BlockChains] {
	return sequences.StellarImportConfigForDeployContracts
}

// DeployChainContracts implements ccvadapters.DeployChainContractsAdapter.
func (a *StellarDeployChainContractsAdapter) DeployChainContracts() *cldf_ops.Sequence[ccvadapters.DeployChainContractsInput, ccvadapters.DeployChainContractsOutput, cldf_chain.BlockChains] {
	return sequences.StellarDeployChainContracts
}
