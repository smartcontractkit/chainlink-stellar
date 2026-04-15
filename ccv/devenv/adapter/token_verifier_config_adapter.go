package adapter

import (
	"fmt"

	"github.com/Masterminds/semver/v3"

	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_6_0/operations/rmn_remote"
	onrampoperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/onramp"
	dsutil "github.com/smartcontractkit/chainlink-ccip/deployment/utils/datastore"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
)

type StellarTokenVerifierConfigAdapter struct{}

var _ ccvadapters.TokenVerifierConfigAdapter = (*StellarTokenVerifierConfigAdapter)(nil)

func (a *StellarTokenVerifierConfigAdapter) ResolveTokenVerifierAddresses(
	ds datastore.DataStore,
	chainSelector uint64,
	_ string,
	_ string,
) (*ccvadapters.TokenVerifierChainAddresses, error) {
	toAddress := func(ref datastore.AddressRef) (string, error) { return ref.Address, nil }

	onRampAddr, err := dsutil.FindAndFormatRef(ds, datastore.AddressRef{
		Type:    datastore.ContractType(onrampoperations.ContractType),
		Version: semver.MustParse(onrampoperations.Deploy.Version()),
	}, chainSelector, toAddress)
	if err != nil {
		return nil, fmt.Errorf("on ramp address for chain %d: %w", chainSelector, err)
	}

	rmnRemoteAddr, err := dsutil.FindAndFormatRef(ds, datastore.AddressRef{
		Type:    datastore.ContractType(rmn_remote.ContractType),
		Version: semver.MustParse(rmn_remote.Deploy.Version()),
	}, chainSelector, toAddress)
	if err != nil {
		return nil, fmt.Errorf("rmn remote address for chain %d: %w", chainSelector, err)
	}

	return &ccvadapters.TokenVerifierChainAddresses{
		OnRampAddress:    onRampAddr,
		RMNRemoteAddress: rmnRemoteAddr,
	}, nil
}
