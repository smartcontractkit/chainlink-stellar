package adapter

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/committee_verifier"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/versioned_verifier_resolver"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
)

type StellarIndexerConfigAdapter struct{}

var _ ccvadapters.IndexerConfigAdapter = (*StellarIndexerConfigAdapter)(nil)

func (a *StellarIndexerConfigAdapter) ResolveVerifierAddresses(
	ds datastore.DataStore, chainSelector uint64, qualifier string, kind ccvadapters.VerifierKind,
) ([]string, error) {
	switch kind {
	case ccvadapters.CommitteeVerifierKind:
		refs := ds.Addresses().Filter(
			datastore.AddressRefByChainSelector(chainSelector),
			datastore.AddressRefByQualifier(qualifier),
			datastore.AddressRefByType(datastore.ContractType(versioned_verifier_resolver.CommitteeVerifierResolverType)),
			datastore.AddressRefByVersion(versioned_verifier_resolver.Version),
		)

		if len(refs) == 0 {
			refs = ds.Addresses().Filter(
				datastore.AddressRefByChainSelector(chainSelector),
				datastore.AddressRefByQualifier(qualifier),
				datastore.AddressRefByType(datastore.ContractType(committee_verifier.ContractType)),
				datastore.AddressRefByVersion(committee_verifier.Version),
			)
		}

		if len(refs) == 0 {
			return nil, &ccvadapters.MissingIndexerVerifierAddressesError{
				Kind:          kind,
				ChainSelector: chainSelector,
				Qualifier:     qualifier,
			}
		}

		addresses := make([]string, 0, len(refs))
		for _, r := range refs {
			addresses = append(addresses, r.Address)
		}
		return addresses, nil

	default:
		return nil, fmt.Errorf("Stellar does not support verifier kind %q", kind)
	}
}
