package adapter

import (
	"fmt"

	"github.com/Masterminds/semver/v3"

	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_6_0/operations/rmn_remote"
	offrampoperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/offramp"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/proxy"
	dsutil "github.com/smartcontractkit/chainlink-ccip/deployment/utils/datastore"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
)

type StellarExecutorConfigAdapter struct{}

var _ ccvadapters.ExecutorConfigAdapter = (*StellarExecutorConfigAdapter)(nil)

func (a *StellarExecutorConfigAdapter) GetDeployedChains(ds datastore.DataStore, qualifier string) []uint64 {
	if ds == nil {
		return nil
	}
	refs := ds.Addresses().Filter(
		datastore.AddressRefByQualifier(qualifier),
		datastore.AddressRefByType(datastore.ContractType(proxy.ContractType)),
	)
	seen := make(map[uint64]struct{}, len(refs))
	chains := make([]uint64, 0, len(refs))
	for _, ref := range refs {
		if _, exists := seen[ref.ChainSelector]; !exists {
			seen[ref.ChainSelector] = struct{}{}
			chains = append(chains, ref.ChainSelector)
		}
	}
	return chains
}

func (a *StellarExecutorConfigAdapter) BuildChainConfig(ds datastore.DataStore, chainSelector uint64, qualifier string) (ccvadapters.ExecutorChainConfig, error) {
	toAddress := func(ref datastore.AddressRef) (string, error) { return ref.Address, nil }

	offRampAddr, err := dsutil.FindAndFormatRef(ds, datastore.AddressRef{
		Type:    datastore.ContractType(offrampoperations.ContractType),
		Version: semver.MustParse(offrampoperations.Deploy.Version()),
	}, chainSelector, toAddress)
	if err != nil {
		return ccvadapters.ExecutorChainConfig{}, fmt.Errorf("off ramp address for chain %d: %w", chainSelector, err)
	}

	rmnRemoteAddr, err := dsutil.FindAndFormatRef(ds, datastore.AddressRef{
		Type:    datastore.ContractType(rmn_remote.ContractType),
		Version: semver.MustParse(rmn_remote.Deploy.Version()),
	}, chainSelector, toAddress)
	if err != nil {
		return ccvadapters.ExecutorChainConfig{}, fmt.Errorf("rmn remote address for chain %d: %w", chainSelector, err)
	}

	executorAddr, err := dsutil.FindAndFormatRef(ds, datastore.AddressRef{
		Type:      datastore.ContractType(proxy.ContractType),
		Qualifier: qualifier,
		Version:   proxy.Version,
	}, chainSelector, toAddress)
	if err != nil {
		return ccvadapters.ExecutorChainConfig{}, fmt.Errorf("executor proxy address for chain %d: %w", chainSelector, err)
	}

	return ccvadapters.ExecutorChainConfig{
		OffRampAddress:       offRampAddr,
		RmnAddress:           rmnRemoteAddr,
		ExecutorProxyAddress: executorAddr,
	}, nil
}
