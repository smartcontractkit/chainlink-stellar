package ccip

import (
	"github.com/Masterminds/semver/v3"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_0_0/operations/rmn_proxy"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_2_0/operations/router"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/committee_verifier"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/executor"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/fee_quoter"
	offrampoperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/offramp"
	onrampoperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/onramp"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/proxy"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/versioned_verifier_resolver"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	aphops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/advanced_pool_hooks"
	aphxops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/advanced_pool_hooks_extractor"
	bnmops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/bnm_token"
	bmpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
	lnkops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/link_token"
	lrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/lock_release_pool"
	rrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ramp_registry"
	slrrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/siloed_lock_release_pool"
	tlbops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_lock_box"
)

// DatastoreSorobanContractRef is the (Type, Version, Qualifier) triple used when
// recording or resolving Soroban contracts in the CLDF deployment datastore.
// All Stellar CCIP writers and readers must use these constructors so keys stay
// aligned with Record* / UpsertDeployedStrKey.
type DatastoreSorobanContractRef struct {
	Type      datastore.ContractType
	Version   *semver.Version
	Qualifier string
}

// PartialAddressRef returns Type, Version, and Qualifier for datastore queries
// (e.g. FindAndFormatRef) where ChainSelector is supplied separately.
func (r DatastoreSorobanContractRef) PartialAddressRef() datastore.AddressRef {
	return datastore.AddressRef{
		Type:      r.Type,
		Version:   r.Version,
		Qualifier: r.Qualifier,
	}
}

// LaneAddressRef returns an AddressRef template including ChainSelector (Address unset).
func (r DatastoreSorobanContractRef) LaneAddressRef(chainSelector uint64) datastore.AddressRef {
	ref := r.PartialAddressRef()
	ref.ChainSelector = chainSelector
	return ref
}

// FullAddressRef returns a complete AddressRef for Upsert/Add.
func (r DatastoreSorobanContractRef) FullAddressRef(chainSelector uint64, addressHex string) datastore.AddressRef {
	ref := r.LaneAddressRef(chainSelector)
	ref.Address = addressHex
	return ref
}

// AddressRefKey returns the datastore key for this ref on a chain.
func (r DatastoreSorobanContractRef) AddressRefKey(chainSelector uint64) datastore.AddressRefKey {
	return datastore.NewAddressRefKey(chainSelector, r.Type, r.Version, r.Qualifier)
}

// LookupStrkey resolves this ref to a Soroban contract strkey.
func (r DatastoreSorobanContractRef) LookupStrkey(ds datastore.DataStore, chainSelector uint64) (string, error) {
	return LookupStellarContractStrkey(ds, chainSelector, r.Type, r.Version, r.Qualifier)
}

// LookupAddressRef loads the raw AddressRef row from the datastore.
func (r DatastoreSorobanContractRef) LookupAddressRef(ds datastore.DataStore, chainSelector uint64) (datastore.AddressRef, error) {
	return LookupAddressRef(ds, chainSelector, r.Type, r.Version, r.Qualifier)
}

// UpsertDeployedStrKey records a deployed Soroban contract strkey under this ref.
func (r DatastoreSorobanContractRef) UpsertDeployedStrKey(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return UpsertDeployedStrKey(ds, chainSelector, r.Type, r.Version, r.Qualifier, contractStrkey)
}

func OnRampDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(onrampoperations.ContractType),
		Version:   semver.MustParse(onrampoperations.Deploy.Version()),
		Qualifier: "",
	}
}

func OffRampDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(offrampoperations.ContractType),
		Version:   semver.MustParse(offrampoperations.Deploy.Version()),
		Qualifier: "",
	}
}

func RouterDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(router.ContractType),
		Version:   semver.MustParse(router.Deploy.Version()),
		Qualifier: "",
	}
}

func FeeQuoterDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(fee_quoter.ContractType),
		Version:   semver.MustParse(fee_quoter.Deploy.Version()),
		Qualifier: "",
	}
}

// RMNRemote's canonical (type, version) pair used to be importable from
// chainlink-ccip/chains/evm/deployment/v1_6_0/operations/rmn_remote. That package was removed;
// upstream now keeps the type string private (chains/evm/deployment/utils/rmn.go
// `rmnRemoteContractType`) and resolves it by datastore lookup. These constants preserve the
// exact values that package exported so existing Stellar datastore refs keep resolving — do not
// switch them to the Stellar-local deployment/operations/rmn_remote values ("RmnRemote"/2.0.0),
// which would silently orphan every recorded ref.
const (
	RMNRemoteContractType    = "RMNRemote"
	RMNRemoteContractVersion = "1.6.0"
)

// Default datastore qualifiers for committee-verifier and executor contract refs.
// These mirror chainlink-ccv/build/devenv/common's DefaultCommitteeVerifierQualifier
// and DefaultExecutorQualifier (both "default") exactly so existing Stellar
// datastore refs keep resolving. They are duplicated locally to keep the root
// production module free of the chainlink-ccv/build/devenv dependency (and the
// transitive chainlink-testing-framework pull it would otherwise cause). If
// devenv ever changes these values, update both constants here to match.
const (
	DefaultCommitteeVerifierQualifier = "default"
	DefaultExecutorQualifier          = "default"
)

func RMNRemoteDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(RMNRemoteContractType),
		Version:   semver.MustParse(RMNRemoteContractVersion),
		Qualifier: "",
	}
}

func RMNProxyDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(rmn_proxy.ContractType),
		Version:   semver.MustParse(rmn_proxy.Deploy.Version()),
		Qualifier: "",
	}
}

func TokenAdminRegistryDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(TokenAdminRegistryContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: "",
	}
}

func VVRDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(versioned_verifier_resolver.CommitteeVerifierResolverType),
		Version:   versioned_verifier_resolver.Version,
		Qualifier: DefaultCommitteeVerifierQualifier,
	}
}

func CommitteeVerifierDatastoreRef() DatastoreSorobanContractRef {
	return CommitteeVerifierDatastoreRefWithQualifier("")
}

// CommitteeVerifierDatastoreRefWithQualifier returns the committee-verifier
// ref for one instance. An empty qualifier keeps the default instance, so
// existing refs and salts keep resolving.
func CommitteeVerifierDatastoreRefWithQualifier(qualifier string) DatastoreSorobanContractRef {
	if qualifier == "" {
		qualifier = DefaultCommitteeVerifierQualifier
	}
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(committee_verifier.ContractType),
		Version:   committee_verifier.Version,
		Qualifier: qualifier,
	}
}

func RampRegistryDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(rrops.ContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: "",
	}
}

func CCIPReceiverDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(CcipReceiverContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: "",
	}
}

func LockReleasePoolDevenvDatastoreRef() DatastoreSorobanContractRef {
	return LegacyLockReleasePoolDevenvDatastoreRef()
}

func LegacyLockReleasePoolDevenvDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(LockReleaseTokenPoolContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: DevenvLegacyLockReleasePoolQualifier,
	}
}

func SiloedLockReleasePoolDevenvDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(SiloedLockReleaseTokenPoolContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: DevenvTestTokenPoolQualifier,
	}
}

func TokenLockBoxDevenvDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(TokenLockBoxContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: DevenvTestTokenPoolQualifier,
	}
}

func DevenvTestTokenDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(TestTokenContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: DevenvTestTokenPoolQualifier,
	}
}

func DefaultExecutorDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(executor.ContractType),
		Version:   executor.Version,
		Qualifier: DefaultExecutorQualifier,
	}
}

// ExecutorProxyDatastoreRef returns the executor proxy row for the given qualifier
// (typically DefaultExecutorQualifier).
func ExecutorProxyDatastoreRef(qualifier string) DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(proxy.ContractType),
		Version:   proxy.Version,
		Qualifier: qualifier,
	}
}

// Datastore refs for the CCIP peripheral components (tokens, token pools, and
// the per-token hooks contracts). The BnM/LINK tokens and the stateless
// extractor are singletons per chain (empty qualifier); the pools, the lock box,
// and the hooks take a qualifier because more than one instance can live on a
// chain (one per token) — pass the token symbol or another instance-identifying
// string.

func BnmTokenDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(bnmops.ContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: "",
	}
}

func LinkTokenDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(lnkops.ContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: "",
	}
}

func AdvancedPoolHooksDatastoreRef(qualifier string) DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(aphops.ContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: qualifier,
	}
}

func AdvancedPoolHooksExtractorDatastoreRef() DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(aphxops.ContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: "",
	}
}

func BurnMintPoolDatastoreRef(qualifier string) DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(bmpops.ContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: qualifier,
	}
}

func LockReleasePoolDatastoreRef(qualifier string) DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(lrops.ContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: qualifier,
	}
}

func SiloedLockReleasePoolDatastoreRef(qualifier string) DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(slrrops.ContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: qualifier,
	}
}

func TokenLockBoxDatastoreRef(qualifier string) DatastoreSorobanContractRef {
	return DatastoreSorobanContractRef{
		Type:      datastore.ContractType(tlbops.ContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: qualifier,
	}
}
