package adapters

import (
	"testing"

	"github.com/Masterminds/semver/v3"
	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/changesets"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	"github.com/stretchr/testify/require"

	stellarsequences "github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

func TestCCIPAdapterRegistrations_stellar(t *testing.T) {
	t.Parallel()
	v2 := semver.MustParse("2.0.0")

	_, ok := deploy.GetRegistry().GetDeployer(chainsel.FamilyStellar, v2)
	require.True(t, ok, "Deployer for stellar 2.0.0")

	_, ok = ccvadapters.GetDeployChainContractsRegistry().Get(chainsel.FamilyStellar)
	require.True(t, ok, "DeployChainContractsAdapter")

	_, ok = ccvadapters.GetChainFamilyRegistry().GetChainFamily(chainsel.FamilyStellar)
	require.True(t, ok, "ChainFamily")

	// The ccip-side offchain-config registries (verifier, executor, indexer, token verifier,
	// aggregator) were removed upstream; those adapters now live in chainlink-ccv
	// (see TestCCVDeploymentAdapterRegistrations_stellar below).

	_, ok = ccvadapters.GetCommitteeVerifierContractRegistry().Get(chainsel.FamilyStellar)
	require.True(t, ok, "CommitteeVerifierContract")

	_, ok = changesets.GetRegistry().GetMCMSReader(chainsel.FamilyStellar)
	require.True(t, ok, "MCMSReader")

	_, ok = tokens.GetTokenAdapterRegistry().GetTokenAdapter(chainsel.FamilyStellar, v2)
	require.True(t, ok, "TokenAdapter 2.0.0")
}

func TestCCVDeploymentAdapterRegistrations_stellar(t *testing.T) {
	t.Parallel()
	sel := chainsel.STELLAR_TESTNET.Selector

	_, err := ccvdeploymentadapters.GetAggregatorRegistry().Get(sel)
	require.NoError(t, err, "Aggregator")
	_, err = ccvdeploymentadapters.GetExecutorRegistry().Get(sel)
	require.NoError(t, err, "Executor")
	_, err = ccvdeploymentadapters.GetVerifierRegistry().Get(sel)
	require.NoError(t, err, "Verifier")
	_, err = ccvdeploymentadapters.GetIndexerRegistry().Get(sel)
	require.NoError(t, err, "Indexer")
	_, err = ccvdeploymentadapters.GetTokenVerifierRegistry().Get(sel)
	require.NoError(t, err, "TokenVerifier")
	_, err = ccvdeploymentadapters.GetCommitteeVerifierOnchainRegistry().Get(sel)
	require.NoError(t, err, "CommitteeVerifierOnchain")
}

func TestStellarDeployChainContractsAdapter_smoke(t *testing.T) {
	t.Parallel()
	var _ ccvadapters.DeployChainContractsAdapter = (*StellarDeployChainContractsAdapter)(nil)
	a := &StellarDeployChainContractsAdapter{}

	require.Empty(t, a.GetDefaultDeployContractParams(1))

	env := cldf.Environment{BlockChains: cldf_chain.NewBlockChains(nil)}
	_, err := a.ResolveDeployAddresses(env, 1)
	require.Error(t, err, "no stellar chain in environment")

	_, err = a.BuildDeployContractParams(ccvadapters.BuildDeployContractParamsInput{ChainSelector: 1})
	require.Error(t, err, "committee verifiers required")
	params, err := a.BuildDeployContractParams(ccvadapters.BuildDeployContractParamsInput{
		ChainSelector:      1,
		CommitteeVerifiers: []ccvadapters.CommitteeVerifierDeployParams{{Qualifier: "q"}},
	})
	require.NoError(t, err)
	require.Len(t, params.CommitteeVerifiers, 1)

	seqDep := a.DeployChainContracts()
	require.NotNil(t, seqDep)
	require.Equal(t, stellarsequences.StellarDeployChainContracts.ID(), seqDep.ID())
}

func TestStellarTokenAdapter_smoke(t *testing.T) {
	t.Parallel()
	var _ tokens.TokenAdapter = (*StellarTokenAdapter)(nil)
	a := &StellarTokenAdapter{}
	require.NotNil(t, a.ConfigureTokenForTransfersSequence())
	_, err := a.AddressRefToBytes(datastore.AddressRef{Address: "not-hex"})
	require.Error(t, err)
}

func TestStellarTransferOwnershipAdapter_smoke(t *testing.T) {
	t.Parallel()
	var _ deploy.TransferOwnershipAdapter = (*StellarTransferOwnershipAdapter)(nil)
	a := &StellarTransferOwnershipAdapter{}
	seqTransfer := a.SequenceTransferOwnershipViaMCMS()
	require.NotNil(t, seqTransfer)
	require.Equal(t, stellarsequences.StellarTransferOwnershipViaMCMS.ID(), seqTransfer.ID())
	require.Equal(t, deploy.MCMSVersion.String(), seqTransfer.Version())
	seqAccept := a.SequenceAcceptOwnership()
	require.NotNil(t, seqAccept)
	require.Equal(t, stellarsequences.StellarAcceptOwnership.ID(), seqAccept.ID())
	require.Equal(t, deploy.MCMSVersion.String(), seqAccept.Version())
}

func TestStellarCommitteeVerifierContractAdapter_smoke(t *testing.T) {
	t.Parallel()
	var _ ccvadapters.CommitteeVerifierContractAdapter = (*StellarCommitteeVerifierContractAdapter)(nil)
	ds := datastore.NewMemoryDataStore().Seal()
	_, err := (&StellarCommitteeVerifierContractAdapter{}).ResolveCommitteeVerifierContracts(ds, 1, "q")
	require.Error(t, err)
}

func TestStellarChainFamilyAdapter_smoke(t *testing.T) {
	t.Parallel()
	var _ ccvadapters.ChainFamily = (*StellarChainFamilyAdapter)(nil)
	a := &StellarChainFamilyAdapter{}
	require.True(t, a.GetFeeQuoterDestChainConfig().IsEnabled)
	require.NotNil(t, a.ConfigureChainForLanes())
	ds := datastore.NewMemoryDataStore().Seal()
	_, err := a.GetOnRampAddress(ds, 1)
	require.Error(t, err)
}

func TestStellarMCMSDeployer_smoke(t *testing.T) {
	t.Parallel()
	var _ deploy.Deployer = (*StellarMCMSDeployer)(nil)
	var _ changesets.MCMSReader = (*StellarMCMSReader)(nil)
	d := StellarMCMSDeployer{}
	require.Nil(t, d.DeployChainContracts())
	require.NotNil(t, d.DeployMCMS())
	require.NotNil(t, d.FinalizeDeployMCMS())
	require.NotNil(t, d.GrantAdminRoleToTimelock())
	require.NotNil(t, d.UpdateMCMSConfig())
}

func TestStellarCCVDeploymentAdapters_smoke(t *testing.T) {
	t.Parallel()
	var _ ccvdeploymentadapters.AggregatorConfigAdapter = (*StellarCCVDeploymentAggregatorConfigAdapter)(nil)
	var _ ccvdeploymentadapters.ExecutorConfigAdapter = (*StellarCCVDeploymentExecutorConfigAdapter)(nil)
	var _ ccvdeploymentadapters.VerifierConfigAdapter = (*StellarCCVDeploymentVerifierConfigAdapter)(nil)
	var _ ccvdeploymentadapters.IndexerConfigAdapter = (*StellarCCVDeploymentIndexerConfigAdapter)(nil)
	var _ ccvdeploymentadapters.TokenVerifierConfigAdapter = (*StellarCCVDeploymentTokenVerifierConfigAdapter)(nil)

	ds := datastore.NewMemoryDataStore().Seal()
	_, err := (&StellarCCVDeploymentAggregatorConfigAdapter{}).ResolveSourceVerifierAddress(ds, 1, "q")
	require.Error(t, err)
	_, err = (&StellarCCVDeploymentAggregatorConfigAdapter{}).ResolveDestinationVerifierAddress(ds, 1, "q")
	require.Error(t, err)

	require.Empty(t, (&StellarCCVDeploymentExecutorConfigAdapter{}).GetDeployedChains(ds, "q"))
	_, err = (&StellarCCVDeploymentExecutorConfigAdapter{}).BuildChainConfig(ds, 1, "q")
	require.Error(t, err)

	require.Equal(t, chainsel.FamilyStellar, (&StellarCCVDeploymentVerifierConfigAdapter{}).GetSignerAddressFamily())
	_, err = (&StellarCCVDeploymentVerifierConfigAdapter{}).ResolveVerifierContractAddresses(ds, 1, "a", "b")
	require.Error(t, err)

	_, err = (&StellarCCVDeploymentIndexerConfigAdapter{}).ResolveVerifierAddresses(ds, 1, "q", ccvdeploymentadapters.CommitteeVerifierKind)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no committee verifier addresses")

	_, err = (&StellarCCVDeploymentTokenVerifierConfigAdapter{}).ResolveTokenVerifierAddresses(ds, 1, "", "")
	require.Error(t, err)
}
