package adapters

import (
	"encoding/json"
	"fmt"

	frameworkdatastore "github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/changesets"
	mcmsutils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/mcms"

	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	mcmsops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/mcms"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// StellarMCMSReader implements changesets.MCMSReader for a single Soroban MCMS contract
// (proposer/bypasser/canceller share one on-chain instance; datastore may hold alias refs).
type StellarMCMSReader struct{}

var _ changesets.MCMSReader = (StellarMCMSReader{})

// GetChainMetadata implements changesets.MCMSReader.
func (StellarMCMSReader) GetChainMetadata(e cldf.Environment, chainSelector uint64, input mcmsutils.Input) (mcmstypes.ChainMetadata, error) {
	ref, err := mcmsutil.FindStellarMCMSAddressRef(e, chainSelector, input)
	if err != nil {
		return mcmstypes.ChainMetadata{}, err
	}
	stellarChains := e.BlockChains.StellarChains()
	ch, ok := stellarChains[chainSelector]
	if !ok {
		return mcmstypes.ChainMetadata{}, fmt.Errorf("stellar chain %d not in environment", chainSelector)
	}
	dep, err := stellardeployment.NewDeployerFromChain(ch)
	if err != nil {
		return mcmstypes.ChainMetadata{}, fmt.Errorf("deployer from chain: %w", err)
	}
	deps := stellardeps.FromDeployer(dep)
	rep, err := cldfops.ExecuteOperation(e.OperationsBundle, mcmsops.GetOpCount, deps, mcmsops.GetOpCountInput{ContractID: ref.Address})
	if err != nil {
		return mcmstypes.ChainMetadata{}, fmt.Errorf("get_op_count: %w", err)
	}
	configVersionRep, err := cldfops.ExecuteOperation(e.OperationsBundle, mcmsops.GetConfigVersion, deps, mcmsops.GetConfigVersionInput{ContractID: ref.Address})
	if err != nil {
		return mcmstypes.ChainMetadata{}, fmt.Errorf("get_config_version: %w", err)
	}
	// The encoder hashes the configVersion from these fields; keeping it in sync
	// with the chain is what makes SetRoot succeed after the first set_config.
	additionalFields, err := json.Marshal(map[string]uint64{"configVersion": configVersionRep.Output.ConfigVersion})
	if err != nil {
		return mcmstypes.ChainMetadata{}, fmt.Errorf("marshal additional fields: %w", err)
	}
	return mcmstypes.ChainMetadata{
		StartingOpCount:  rep.Output.OpCount,
		MCMAddress:       ref.Address,
		AdditionalFields: additionalFields,
	}, nil
}

// GetTimelockRef implements changesets.MCMSReader.
// Prefers RBACTimelock in the datastore; falls back to the MCMS contract id for legacy deployments.
func (StellarMCMSReader) GetTimelockRef(e cldf.Environment, chainSelector uint64, input mcmsutils.Input) (frameworkdatastore.AddressRef, error) {
	return mcmsutil.FindStellarTimelockAddressRef(e, chainSelector, input)
}

// GetMCMSRef implements changesets.MCMSReader.
func (StellarMCMSReader) GetMCMSRef(e cldf.Environment, chainSelector uint64, input mcmsutils.Input) (frameworkdatastore.AddressRef, error) {
	return mcmsutil.FindStellarMCMSAddressRef(e, chainSelector, input)
}
