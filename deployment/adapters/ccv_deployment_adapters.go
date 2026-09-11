package adapters

import (
	"fmt"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/committee_verifier"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/versioned_verifier_resolver"
	dsutils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/datastore"
	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	"github.com/smartcontractkit/chainlink-ccv/executor"
	"github.com/smartcontractkit/chainlink-ccv/pkg/chainaccess"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	stellarcommon "github.com/smartcontractkit/chainlink-stellar/ccv/common"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
)

// refAddress is the datastore ref formatter used throughout this file: Stellar records
// contract IDs verbatim, so no per-family address conversion is needed.
func refAddress(r datastore.AddressRef) (string, error) { return r.Address, nil }

// StellarCCVDeploymentAggregatorConfigAdapter implements
// github.com/smartcontractkit/chainlink-ccv/deployment/adapters.AggregatorConfigAdapter
// for the devenv (datastore-only verifier address resolution). On-chain committee
// state for GenerateAggregatorConfig lives on StellarCCVCommitteeVerifierOnchainAdapter.
type StellarCCVDeploymentAggregatorConfigAdapter struct{}

var _ ccvdeploymentadapters.AggregatorConfigAdapter = (*StellarCCVDeploymentAggregatorConfigAdapter)(nil)

func (a *StellarCCVDeploymentAggregatorConfigAdapter) ResolveSourceVerifierAddress(
	ds datastore.DataStore,
	chainSelector uint64,
	qualifier string,
) (string, error) {
	return a.resolveVerifierAddress(ds, chainSelector, qualifier)
}

func (a *StellarCCVDeploymentAggregatorConfigAdapter) ResolveDestinationVerifierAddress(
	ds datastore.DataStore,
	chainSelector uint64,
	qualifier string,
) (string, error) {
	return a.resolveVerifierAddress(ds, chainSelector, qualifier)
}

func (a *StellarCCVDeploymentAggregatorConfigAdapter) GetDeployedChains(ds datastore.DataStore, qualifier string) []uint64 {
	if ds == nil {
		return nil
	}
	refs := ds.Addresses().Filter(
		datastore.AddressRefByQualifier(qualifier),
		datastore.AddressRefByType(stellarccip.CommitteeVerifierDatastoreRef().Type),
	)
	seen := make(map[uint64]struct{}, len(refs))
	chains := make([]uint64, 0, len(refs))
	for _, ref := range refs {
		if _, exists := seen[ref.ChainSelector]; exists {
			continue
		}
		family, err := chainsel.GetSelectorFamily(ref.ChainSelector)
		if err != nil || family != chainsel.FamilyStellar {
			continue
		}
		seen[ref.ChainSelector] = struct{}{}
		chains = append(chains, ref.ChainSelector)
	}
	return chains
}

func (a *StellarCCVDeploymentAggregatorConfigAdapter) resolveVerifierAddress(
	ds datastore.DataStore,
	chainSelector uint64,
	qualifier string,
) (string, error) {
	return dsutils.FindAndFormatFirstRef(ds, chainSelector, refAddress,
		datastore.AddressRef{
			Type:      datastore.ContractType(versioned_verifier_resolver.CommitteeVerifierResolverType),
			Qualifier: qualifier,
		},
		datastore.AddressRef{
			Type:      datastore.ContractType(committee_verifier.ContractType),
			Qualifier: qualifier,
		},
	)
}

// StellarCCVDeploymentExecutorConfigAdapter implements
// github.com/smartcontractkit/chainlink-ccv/deployment/adapters.ExecutorConfigAdapter.
type StellarCCVDeploymentExecutorConfigAdapter struct{}

var _ ccvdeploymentadapters.ExecutorConfigAdapter = (*StellarCCVDeploymentExecutorConfigAdapter)(nil)

func (a *StellarCCVDeploymentExecutorConfigAdapter) GetDeployedChains(ds datastore.DataStore, qualifier string) []uint64 {
	if ds == nil {
		return nil
	}
	refs := ds.Addresses().Filter(
		datastore.AddressRefByQualifier(qualifier),
		datastore.AddressRefByType(stellarccip.ExecutorProxyDatastoreRef(qualifier).Type),
	)
	seen := make(map[uint64]struct{}, len(refs))
	chains := make([]uint64, 0, len(refs))
	for _, ref := range refs {
		if _, exists := seen[ref.ChainSelector]; exists {
			continue
		}
		family, err := chainsel.GetSelectorFamily(ref.ChainSelector)
		if err != nil || family != chainsel.FamilyStellar {
			continue
		}
		seen[ref.ChainSelector] = struct{}{}
		chains = append(chains, ref.ChainSelector)
	}
	return chains
}

// ResolveExecutorAddress is the single source of truth for the Stellar executor address:
// BuildChainConfig uses it for DefaultExecutorAddress, and the committee verifier changeset
// uses it as the executor on-ramp address. Stellar keeps its existing ExecutorProxy datastore
// type — the adapter API no longer prescribes a contract-type name, but changing the recorded
// type would orphan every existing ref.
func (a *StellarCCVDeploymentExecutorConfigAdapter) ResolveExecutorAddress(
	ds datastore.DataStore,
	chainSelector uint64,
	qualifier string,
) (string, error) {
	addr, err := dsutils.FindAndFormatRef(
		ds,
		stellarccip.ExecutorProxyDatastoreRef(qualifier).PartialAddressRef(),
		chainSelector,
		refAddress,
	)
	if err != nil {
		return "", fmt.Errorf("executor address for chain %d: %w", chainSelector, err)
	}
	return addr, nil
}

func (a *StellarCCVDeploymentExecutorConfigAdapter) BuildChainConfig(
	ds datastore.DataStore,
	chainSelector uint64,
	qualifier string,
) (executor.ChainConfiguration, error) {
	offRampAddr, err := dsutils.FindAndFormatRef(
		ds, stellarccip.OffRampDatastoreRef().PartialAddressRef(), chainSelector, refAddress)
	if err != nil {
		return executor.ChainConfiguration{}, fmt.Errorf("off ramp address for chain %d: %w", chainSelector, err)
	}

	// RMN Remote is deprecated upstream — readers derive it from the OffRamp's on-chain static
	// config. Emit it when the datastore has it so specs keep working for node binaries that
	// predate the derivation cutover, but do not fail the build when it is absent.
	rmnRemoteAddr, err := dsutils.FindAndFormatRef(
		ds, stellarccip.RMNRemoteDatastoreRef().PartialAddressRef(), chainSelector, refAddress)
	if err != nil {
		rmnRemoteAddr = ""
	}

	executorAddr, err := a.ResolveExecutorAddress(ds, chainSelector, qualifier)
	if err != nil {
		return executor.ChainConfiguration{}, err
	}

	return executor.ChainConfiguration{
		DestinationChainConfig: chainaccess.DestinationChainConfig{
			OffRampAddress: offRampAddr,
			RmnAddress:     rmnRemoteAddr,
			// Make the Stellar Ed25519 transmitter key explicit in the generated job spec
			// rather than relying on the accessor's default-key fallback.
			TransmitterKeyName: stellarcommon.StellarTransmitterKeyName,
		},
		DefaultExecutorAddress: executorAddr,
	}, nil
}

// StellarCCVDeploymentVerifierConfigAdapter implements
// github.com/smartcontractkit/chainlink-ccv/deployment/adapters.VerifierConfigAdapter.
type StellarCCVDeploymentVerifierConfigAdapter struct{}

var _ ccvdeploymentadapters.VerifierConfigAdapter = (*StellarCCVDeploymentVerifierConfigAdapter)(nil)

func (a *StellarCCVDeploymentVerifierConfigAdapter) GetSignerAddressFamily() string {
	return chainsel.FamilyStellar
}

func (a *StellarCCVDeploymentVerifierConfigAdapter) ResolveVerifierContractAddresses(
	ds datastore.DataStore,
	chainSelector uint64,
	committeeQualifier string,
	_ string,
) (*ccvdeploymentadapters.VerifierContractAddresses, error) {
	committeeVerifierAddr, err := dsutils.FindAndFormatFirstRef(ds, chainSelector, refAddress,
		datastore.AddressRef{
			Type:      datastore.ContractType(versioned_verifier_resolver.CommitteeVerifierResolverType),
			Qualifier: committeeQualifier,
		},
		datastore.AddressRef{
			Type:      datastore.ContractType(committee_verifier.ContractType),
			Qualifier: committeeQualifier,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("committee verifier address for chain %d: %w", chainSelector, err)
	}

	onRampAddr, err := dsutils.FindAndFormatRef(
		ds, stellarccip.OnRampDatastoreRef().PartialAddressRef(), chainSelector, refAddress)
	if err != nil {
		return nil, fmt.Errorf("on ramp address for chain %d: %w", chainSelector, err)
	}

	// The executor address is no longer part of this struct — the changeset resolves it via
	// ExecutorConfigAdapter.ResolveExecutorAddress. RMN Remote is deprecated and optional.
	rmnRemoteAddr, err := dsutils.FindAndFormatRef(
		ds, stellarccip.RMNRemoteDatastoreRef().PartialAddressRef(), chainSelector, refAddress)
	if err != nil {
		rmnRemoteAddr = ""
	}

	return &ccvdeploymentadapters.VerifierContractAddresses{
		CommitteeVerifierAddress: committeeVerifierAddr,
		OnRampAddress:            onRampAddr,
		RMNRemoteAddress:         rmnRemoteAddr,
	}, nil
}

// StellarCCVDeploymentIndexerConfigAdapter implements
// github.com/smartcontractkit/chainlink-ccv/deployment/adapters.IndexerConfigAdapter.
type StellarCCVDeploymentIndexerConfigAdapter struct{}

var _ ccvdeploymentadapters.IndexerConfigAdapter = (*StellarCCVDeploymentIndexerConfigAdapter)(nil)

func (a *StellarCCVDeploymentIndexerConfigAdapter) ResolveVerifierAddresses(
	ds datastore.DataStore,
	chainSelector uint64,
	qualifier string,
	kind ccvdeploymentadapters.VerifierKind,
) ([]string, error) {
	if kind != ccvdeploymentadapters.CommitteeVerifierKind {
		return nil, fmt.Errorf("stellar does not support verifier kind %q", kind)
	}

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
		return nil, &ccvdeploymentadapters.MissingIndexerVerifierAddressesError{
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
}

// StellarCCVDeploymentTokenVerifierConfigAdapter implements
// github.com/smartcontractkit/chainlink-ccv/deployment/adapters.TokenVerifierConfigAdapter.
type StellarCCVDeploymentTokenVerifierConfigAdapter struct{}

var _ ccvdeploymentadapters.TokenVerifierConfigAdapter = (*StellarCCVDeploymentTokenVerifierConfigAdapter)(nil)

func (a *StellarCCVDeploymentTokenVerifierConfigAdapter) ResolveTokenVerifierAddresses(
	ds datastore.DataStore,
	chainSelector uint64,
	_ string,
	_ string,
) (*ccvdeploymentadapters.TokenVerifierChainAddresses, error) {
	onRampAddr, err := dsutils.FindAndFormatRef(
		ds, stellarccip.OnRampDatastoreRef().PartialAddressRef(), chainSelector, refAddress)
	if err != nil {
		return nil, fmt.Errorf("on ramp address for chain %d: %w", chainSelector, err)
	}

	// Deprecated and optional; see ResolveVerifierContractAddresses.
	rmnRemoteAddr, err := dsutils.FindAndFormatRef(
		ds, stellarccip.RMNRemoteDatastoreRef().PartialAddressRef(), chainSelector, refAddress)
	if err != nil {
		rmnRemoteAddr = ""
	}

	// Stellar deploys no CCTP or Lombard token verifiers; those addresses stay empty.
	return &ccvdeploymentadapters.TokenVerifierChainAddresses{
		OnRampAddress:    onRampAddr,
		RMNRemoteAddress: rmnRemoteAddr,
	}, nil
}
