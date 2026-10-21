//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	ccvchangesets "github.com/smartcontractkit/chainlink-ccv/deployment/changesets"

	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	_ "github.com/smartcontractkit/chainlink-stellar/deployment/adapters" // registers the ccv deploy adapters the changesets dispatch to
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/changesets"
)

// TestCCVDeployAdapters drives the ccv DeployCommitteeVerifier and
// DeployProtocolContracts changesets through the registered stellar ccv
// deploy adapters on a live chain: the committee verifier + its resolver
// deploy as a pair under one qualifier, and the protocol-contracts deploy
// wraps the shared full-deploy sequence (the same one the ccip
// DeployChainContracts path drives). Re-runs are idempotent: no new refs, no
// batch ops. The underlying deploy sequences are covered by the component
// changeset harness; this test covers the ccv registration + input-mapping
// layer on top of them.
func TestCCVDeployAdapters(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	_, deployerKP, deployer, rpcClient, passphrase, _ := GetIsolatedTestEnv(ctx, t)
	sel := chainsel.STELLAR_LOCALNET.Selector

	newBundle := func() cldfops.Bundle {
		return cldfops.NewBundle(func() context.Context { return ctx }, cldflogger.Test(t), cldfops.NewMemoryReporter())
	}
	ds := datastore.NewMemoryDataStore()
	cldfEnv := cldf.Environment{
		Logger:     cldflogger.Test(t),
		GetContext: func() context.Context { return ctx },
		DataStore:  ds.Seal(),
		BlockChains: cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: cldf_stellar.Chain{
			ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
			Signer:            bindings.NewStellarKeypairSigner(deployerKP),
			Client:            rpcClient,
			NetworkPassphrase: passphrase,
		}}),
		OperationsBundle: newBundle(),
	}
	apply := func(name string, apply func() (datastore.MutableDataStore, error)) {
		t.Helper()
		out, err := apply()
		require.NoError(t, err, "%s: apply failed", name)
		require.NoError(t, ds.Merge(out.Seal()), "%s: merge output", name)
		cldfEnv.DataStore = ds.Seal()
	}

	// The committee verifier deploy resolves the RMN Proxy from the chain's
	// existing addresses: deploy the RMN pair first.
	apply("RMN Remote", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRMNRemote{}.Apply(cldfEnv, changesets.DeployRMNRemoteConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply("RMN Proxy", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRMNProxy{}.Apply(cldfEnv, changesets.DeployRMNProxyConfig{ChainSelector: sel})
		return out.DataStore, err
	})

	feeAggregator := stellarutil.MustGenerateMockContractID("ccv-deploy-adapters", "fee-aggregator")
	cvInput := ccvchangesets.DeployCommitteeVerifierInput{
		ChainSelectors: []uint64{sel},
		Committees: []ccvdeploymentadapters.CommitteeVerifierDeployParams{{
			Version:          semver.MustParse("2.0.0"),
			FeeAggregator:    feeAggregator,
			StorageLocations: []string{"integration-cv-deploy"},
			Qualifier:        "default",
		}},
		DefaultCfg: ccvchangesets.DeployCommitteeVerifierPerChainCfg{
			DeployerContract: "placeholder", // no deployer factory on stellar; the adapter ignores the value
		},
	}

	t.Run("committee verifier and resolver deploy as a pair", func(t *testing.T) {
		cs := ccvchangesets.DeployCommitteeVerifier()
		require.NoError(t, cs.VerifyPreconditions(cldfEnv, cvInput))
		out, err := cs.Apply(cldfEnv, cvInput)
		require.NoError(t, err)
		require.NoError(t, ds.Merge(out.DataStore.Seal()))
		cldfEnv.DataStore = ds.Seal()

		// Both halves of the pair record refs under the committee qualifier.
		cvRef, err := ccip.CommitteeVerifierDatastoreRef().LookupAddressRef(cldfEnv.DataStore, sel)
		require.NoError(t, err, "committee verifier ref should record")
		vvrRef, err := ccip.VVRDatastoreRef().LookupAddressRef(cldfEnv.DataStore, sel)
		require.NoError(t, err, "versioned verifier resolver ref should record")

		// The resolver is wired to this committee's verifier: the readbacks
		// return it for the chain's outbound lane and the default inbound
		// version tag. Without the wiring a lane consulting the resolver fails
		// with Inbound/OutboundImplementationNotFound.
		cvID, err := ccip.CommitteeVerifierDatastoreRef().LookupStrkey(cldfEnv.DataStore, sel)
		require.NoError(t, err)
		vvrID, err := ccip.VVRDatastoreRef().LookupStrkey(cldfEnv.DataStore, sel)
		require.NoError(t, err)
		vvrClient := vvrbindings.NewVersionedVerifierResolverClient(deployer, vvrID)
		outbound, err := vvrClient.GetAllOutboundImplementations(ctx)
		require.NoError(t, err)
		require.Len(t, outbound, 1)
		require.Equal(t, sel, outbound[0].DestChainSelector)
		require.Equal(t, cvID, outbound[0].Verifier)
		inbound, err := vvrClient.GetAllInboundImplementations(ctx)
		require.NoError(t, err)
		require.Len(t, inbound, 1)
		require.Equal(t, cvID, inbound[0].Verifier)
		require.Equal(t, stellarutil.DefaultCommitteeVerifierVersionTag(), inbound[0].Version)

		// Idempotent re-run on a fresh bundle: the refs already exist, so
		// nothing new records and no batch ops are produced.
		cldfEnv.OperationsBundle = newBundle()
		rerun, err := cs.Apply(cldfEnv, cvInput)
		require.NoError(t, err)
		require.Empty(t, rerun.DataStore.Addresses().Filter(), "re-run records no new refs")
		require.Empty(t, rerun.MCMSTimelockProposals)

		_ = cvRef
		_ = vvrRef
	})

	protocolInput := ccvchangesets.DeployProtocolContractsInput{
		ChainSelectors: []uint64{sel},
		DefaultCfg: ccvchangesets.DeployProtocolContractsPerChainCfg{
			DeployerContract: "placeholder",
			DeployerKeyOwned: true,
		},
	}

	t.Run("protocol contracts wrap the full deploy idempotently", func(t *testing.T) {
		cs := ccvchangesets.DeployProtocolContracts()
		require.NoError(t, cs.VerifyPreconditions(cldfEnv, protocolInput))
		out, err := cs.Apply(cldfEnv, protocolInput)
		require.NoError(t, err)
		require.NotEmpty(t, out.DataStore.Addresses().Filter(), "the full deploy records component refs")

		// Deployer-key owned: no refs flagged for ownership transfer, and the
		// EOA-only deploy emits no MCMS proposals.
		require.Empty(t, out.MCMSTimelockProposals)
		require.NoError(t, ds.Merge(out.DataStore.Seal()))
		cldfEnv.DataStore = ds.Seal()

		// The stack the full deploy owns is resolvable from the merged refs.
		_, err = ccip.RouterDatastoreRef().LookupStrkey(cldfEnv.DataStore, sel)
		require.NoError(t, err, "router ref should record")
		_, err = ccip.OnRampDatastoreRef().LookupStrkey(cldfEnv.DataStore, sel)
		require.NoError(t, err, "onramp ref should record")
		_, err = ccip.OffRampDatastoreRef().LookupStrkey(cldfEnv.DataStore, sel)
		require.NoError(t, err, "offramp ref should record")
		_, err = ccip.FeeQuoterDatastoreRef().LookupStrkey(cldfEnv.DataStore, sel)
		require.NoError(t, err, "fee quoter ref should record")

		// Idempotent re-run: every component resolves via its ref, so nothing
		// re-deploys and no batch ops are produced.
		cldfEnv.OperationsBundle = newBundle()
		rerun, err := cs.Apply(cldfEnv, protocolInput)
		require.NoError(t, err)
		require.Empty(t, rerun.DataStore.Addresses().Filter(), "re-run records no new refs")
		require.Empty(t, rerun.MCMSTimelockProposals)
	})
}
