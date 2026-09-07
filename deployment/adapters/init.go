// Package adapters registers Stellar with chainlink-ccip CCIP 2.0 tooling, with
// chainlink-ccv/deployment (per-concern FamilyRegistry singletons plus the shared JD identity
// plumbing), and with shared infrastructure (MCMS, transfer ownership, tokens, fees, curse).
//
// Devenv-only hooks under chainlink-ccv/build/devenv (chain config loader, verifier/executor
// modifiers, ImplFactory, ExecutorInfo, CLDF provider, extra-args serializers) are not
// registered here; they live in ccv/chain/register.go and run when
// RegisterStellarDevenvComponents is called. See the package doc there for the full split.
// The ccvchain package blank-imports this package so its init runs as soon as ccvchain loads.
package adapters

import (
	"strings"

	"github.com/Masterminds/semver/v3"
	chainsel "github.com/smartcontractkit/chain-selectors"
	nodev1 "github.com/smartcontractkit/chainlink-protos/job-distributor/v1/node"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	"github.com/smartcontractkit/chainlink-ccip/deployment/fastcurse"
	"github.com/smartcontractkit/chainlink-ccip/deployment/fees"
	tokenscore "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/changesets"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"

	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	ccvshared "github.com/smartcontractkit/chainlink-ccv/deployment/shared"

	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
)

// init performs CCIP-platform and CCV-deployment registration for FamilyStellar. It does not
// call into chainlink-ccv build/devenv registries; those are populated from ccv/chain/register.go.
func init() {
	v2 := semver.MustParse("2.0.0")

	// --- chainlink-ccip platform tooling ---
	deploy.GetRegistry().RegisterDeployer(chainsel.FamilyStellar, v2, &StellarMCMSDeployer{})
	changesets.GetRegistry().RegisterMCMSReader(chainsel.FamilyStellar, &StellarMCMSReader{})
	deploy.GetTransferOwnershipRegistry().RegisterAdapter(chainsel.FamilyStellar, deploy.MCMSVersion, &StellarTransferOwnershipAdapter{})
	deploy.GetTransferOwnershipRegistry().RegisterAdapter(chainsel.FamilyStellar, v2, &StellarTransferOwnershipAdapter{})

	// chainlink-ccip v2_0_0 adapters
	//
	// Only three registries survive here. The offchain-config adapters (verifier, executor,
	// indexer, token verifier, aggregator) were deleted from chainlink-ccip and now live solely
	// in chainlink-ccv/deployment/adapters; Stellar's implementations moved to the
	// StellarCCVDeployment* types registered below.
	ccvadapters.GetChainFamilyRegistry().RegisterChainFamily(chainsel.FamilyStellar, &StellarChainFamilyAdapter{})
	ccvadapters.GetCommitteeVerifierContractRegistry().Register(chainsel.FamilyStellar, &StellarCommitteeVerifierContractAdapter{})
	ccvadapters.GetDeployChainContractsRegistry().Register(chainsel.FamilyStellar, &StellarDeployChainContractsAdapter{})

	// chainlink-ccv/deployment/shared: JD identity and address plumbing
	//
	// All three are load-bearing for state-based changeset inputs:
	//   - RegisterSigningIdentityReader is what makes SigningKeysByNOP[alias]["stellar"] exist at
	//     all; fetch_signing_keys only iterates families that registered a reader.
	//   - RegisterAddressNormalizer closes the loop between ScanCommitteeStates (which emits
	//     EIP-55 mixed case) and the lowercase form fetch_signing_keys stored. Without it
	//     CommitteeInputFromState hard-errors on every Stellar on-chain signer.
	//   - RegisterChainTypeFamily feeds fetch_node_chain_support, which gates the
	//     ApplyVerifierConfig / ApplyExecutorConfig JD node-chain-support check.
	//
	// The EVM reader is correct here, not a shortcut: bootstrap publishes OnchainSigningAddress
	// as the EVM-derived 20-byte address for every chain family, and Soroban's committee_verifier
	// stores left-padded 20-byte ETH-style signers (see the signer[12:32] read in
	// ccv_committee_verifier_onchain.go). The Ed25519 transmitter key is a different key with a
	// different job and never appears in a signature config.
	ccvshared.RegisterChainTypeFamily(nodev1.ChainType_CHAIN_TYPE_STELLAR, chainsel.FamilyStellar)
	ccvshared.RegisterAddressNormalizer(chainsel.FamilyStellar, normalizeStellarSignerAddress)
	ccvshared.RegisterSigningIdentityReader(chainsel.FamilyStellar, ccvshared.EVMSigningIdentityReader{})

	// chainlink-ccv/deployment/adapters: one FamilyRegistry per concern
	ccvdeploymentadapters.GetAggregatorRegistry().Register(chainsel.FamilyStellar, &StellarCCVDeploymentAggregatorConfigAdapter{})
	ccvdeploymentadapters.GetExecutorRegistry().Register(chainsel.FamilyStellar, &StellarCCVDeploymentExecutorConfigAdapter{})
	ccvdeploymentadapters.GetVerifierRegistry().Register(chainsel.FamilyStellar, &StellarCCVDeploymentVerifierConfigAdapter{})
	ccvdeploymentadapters.GetIndexerRegistry().Register(chainsel.FamilyStellar, &StellarCCVDeploymentIndexerConfigAdapter{})
	ccvdeploymentadapters.GetTokenVerifierRegistry().Register(chainsel.FamilyStellar, &StellarCCVDeploymentTokenVerifierConfigAdapter{})
	ccvdeploymentadapters.GetCommitteeVerifierOnchainRegistry().Register(chainsel.FamilyStellar, &StellarCCVCommitteeVerifierOnchainAdapter{})

	// TODO: validate that it's ok to skip registeration
	//   GetCommitteeVerifierDeployRegistry / GetProtocolContractsDeployRegistry — consumed only by
	//     ccv's DeployProtocolContracts changeset; Stellar deploys through the ccip
	//     GetDeployChainContractsRegistry entry above.
	//   GetLaneConfigRegistry — consumed only by LaneExpansion / PromoteLaneRouter, which have no
	//     non-test callers. Devenv configures Stellar lanes through the ccip ChainFamily adapter.
	//     If someone does call LaneExpansion on an EVM<->Stellar pair, validateLaneInput fails
	//     fast with "no adapter registered for chain family \"stellar\"" rather than corrupting.
	//   GetTokenPoolOnchainRegistry — consumed only by the RemoveRemotePool changeset.

	// --- shared infrastructure ---
	tokenscore.GetTokenAdapterRegistry().RegisterTokenAdapter(
		chainsel.FamilyStellar, v2, &StellarTokenAdapter{},
	)

	fees.GetRegistry().RegisterFeeAdapter(chainsel.FamilyStellar, v2, &StellarFeeAdapter{})
	fees.GetFeeAggregatorRegistry().RegisterFeeAggregatorAdapter(chainsel.FamilyStellar, v2, &StellarFeeAggregatorAdapter{})

	curseAdapter := NewStellarCurseAdapter()
	fastcurse.GetCurseRegistry().RegisterNewCurse(fastcurse.CurseRegistryInput{
		CursingFamily:       chainsel.FamilyStellar,
		CursingVersion:      stellarops.ContractDeploymentVersion,
		CurseAdapter:        curseAdapter,
		CurseSubjectAdapter: curseAdapter,
	})
}

// normalizeStellarSignerAddress mirrors the EVM normalizer. Stellar's committee_verifier stores
// 20-byte ETH-style signer identities, so the canonical form is lowercase 0x-prefixed hex.
func normalizeStellarSignerAddress(addr string) string {
	lower := strings.ToLower(addr)
	if !strings.HasPrefix(lower, "0x") {
		return "0x" + lower
	}
	return lower
}
