//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cldfstellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	stellarprovider "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar/provider"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	testenv "github.com/smartcontractkit/chainlink-deployments-framework/engine/test/environment"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/changesets"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// TestComponentChangesetsApplyAllTwice proves the per-component changesets on
// a live network: a first pass in dependency order deploys and initializes all
// twelve components, and a second pass against the recorded refs (fresh
// operations bundle, no cached reports) sends no transactions and records no
// new refs — the skip layers do all the work.
func TestComponentChangesetsApplyAllTwice(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	_, _, _, _, passphrase, _ := GetIsolatedTestEnv(ctx, t)
	sel := chainsel.STELLAR_LOCALNET.Selector

	// A dedicated random signer: the component salt labels are fixed, so
	// reusing the suite-wide deployer would deploy the same contract IDs as
	// the other integration tests (same deployer, same salt, same network) and
	// break them by test order.
	provider := stellarprovider.NewRPCChainProvider(sel, stellarprovider.RPCChainProviderConfig{
		SorobanRPCURL:      sharedEnv.Output.Nodes[0].ExternalHTTPUrl,
		NetworkPassphrase:  passphrase,
		FriendbotURL:       sharedEnv.FriendbotURL,
		DeployerKeypairGen: stellarprovider.KeypairRandom(),
	})
	blockchain, err := provider.Initialize(ctx)
	require.NoError(t, err)

	// Fund the signer the provider actually installed: KeypairRandom.Generate
	// returns a new keypair on every call, so funding must happen after
	// Initialize, on the chain's own signer.
	chain, ok := blockchain.(*cldfstellar.Chain)
	require.True(t, ok, "provider returned %T, want *cldfstellar.Chain", blockchain)
	require.NoError(t, helpers.FundViaFriendbot(sharedEnv.FriendbotURL, chain.Signer.Address()))

	env, err := testenv.New(ctx, testenv.WithChains(blockchain))
	require.NoError(t, err)

	storageLocation := stellarutil.GenerateContractAddress("storage-location", passphrase)

	// Pass 1: apply all twelve in dependency order against a growing datastore
	// (Seal shares the underlying stores, so the view sees every merge).
	state := datastore.NewMemoryDataStore()
	sealed := state.Seal()
	env.DataStore = sealed
	feeToken := "" // resolved from RMN Remote after the first apply

	apply := func(t *testing.T, name string, apply func() (datastore.MutableDataStore, error)) {
		t.Helper()
		out, err := apply()
		require.NoError(t, err, "%s: apply failed", name)
		require.NoError(t, state.Merge(out.Seal()), "%s: merge output", name)
	}

	apply(t, "RMN Remote", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRMNRemote{}.Apply(*env, changesets.DeployRMNRemoteConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	feeToken, err = ccip.RMNRemoteDatastoreRef().LookupStrkey(sealed, sel)
	require.NoError(t, err)

	apply(t, "TokenAdminRegistry", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployTokenAdminRegistry{}.Apply(*env, changesets.DeployTokenAdminRegistryConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply(t, "RampRegistry", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRampRegistry{}.Apply(*env, changesets.DeployRampRegistryConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply(t, "VVR", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployVVR{}.Apply(*env, changesets.DeployVVRConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply(t, "Executor", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployExecutor{}.Apply(*env, changesets.DeployExecutorConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply(t, "RMN Proxy", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRMNProxy{}.Apply(*env, changesets.DeployRMNProxyConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply(t, "FeeQuoter", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployFeeQuoter{}.Apply(*env, changesets.DeployFeeQuoterConfig{
			ChainSelector: sel,
			FeeToken:      feeToken,
		})
		return out.DataStore, err
	})
	apply(t, "OnRamp", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployOnRamp{}.Apply(*env, changesets.DeployOnRampConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply(t, "OffRamp", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployOffRamp{}.Apply(*env, changesets.DeployOffRampConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply(t, "Router", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRouter{}.Apply(*env, changesets.DeployRouterConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply(t, "CommitteeVerifier", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployCommitteeVerifier{}.Apply(*env, changesets.DeployCommitteeVerifierConfig{
			ChainSelector:    sel,
			StorageLocations: [][]byte{storageLocation},
		})
		return out.DataStore, err
	})
	apply(t, "ccip_receiver_example", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployCCIPReceiver{}.Apply(*env, changesets.DeployCCIPReceiverConfig{ChainSelector: sel})
		return out.DataStore, err
	})

	// Twelve components; the Executor writes the executor and executor-proxy
	// rows, so 13 refs total.
	refs, err := state.Addresses().Fetch()
	require.NoError(t, err)
	require.Len(t, refs, 13)

	// Pass 2: fresh operations bundle (no cached op reports) against the
	// recorded refs. Every changeset must verify, apply without error, record
	// no new refs, and send no transactions — the reporter holds only the
	// twelve sequence reports, no *:deploy / *:initialize op reports.
	rerunReporter := cldfops.NewMemoryReporter()
	env.OperationsBundle = cldfops.NewBundle(func() context.Context { return ctx }, cldflogger.Nop(), rerunReporter)

	rerun := func(t *testing.T, name string, verify func() error, apply func() (datastore.MutableDataStore, error)) {
		t.Helper()
		require.NoError(t, verify(), "%s: verify preconditions", name)
		out, err := apply()
		require.NoError(t, err, "%s: re-apply failed", name)
		newRefs, err := out.Addresses().Fetch()
		require.NoError(t, err)
		require.Empty(t, newRefs, "%s: re-apply recorded new refs", name)
	}

	rerun(t, "RMN Remote",
		func() error {
			return changesets.DeployRMNRemote{}.VerifyPreconditions(*env, changesets.DeployRMNRemoteConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployRMNRemote{}.Apply(*env, changesets.DeployRMNRemoteConfig{ChainSelector: sel})
			return out.DataStore, err
		})
	rerun(t, "TokenAdminRegistry",
		func() error {
			return changesets.DeployTokenAdminRegistry{}.VerifyPreconditions(*env, changesets.DeployTokenAdminRegistryConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployTokenAdminRegistry{}.Apply(*env, changesets.DeployTokenAdminRegistryConfig{ChainSelector: sel})
			return out.DataStore, err
		})
	rerun(t, "RampRegistry",
		func() error {
			return changesets.DeployRampRegistry{}.VerifyPreconditions(*env, changesets.DeployRampRegistryConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployRampRegistry{}.Apply(*env, changesets.DeployRampRegistryConfig{ChainSelector: sel})
			return out.DataStore, err
		})
	rerun(t, "VVR",
		func() error {
			return changesets.DeployVVR{}.VerifyPreconditions(*env, changesets.DeployVVRConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployVVR{}.Apply(*env, changesets.DeployVVRConfig{ChainSelector: sel})
			return out.DataStore, err
		})
	rerun(t, "Executor",
		func() error {
			return changesets.DeployExecutor{}.VerifyPreconditions(*env, changesets.DeployExecutorConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployExecutor{}.Apply(*env, changesets.DeployExecutorConfig{ChainSelector: sel})
			return out.DataStore, err
		})
	rerun(t, "RMN Proxy",
		func() error {
			return changesets.DeployRMNProxy{}.VerifyPreconditions(*env, changesets.DeployRMNProxyConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployRMNProxy{}.Apply(*env, changesets.DeployRMNProxyConfig{ChainSelector: sel})
			return out.DataStore, err
		})
	rerun(t, "FeeQuoter",
		func() error {
			return changesets.DeployFeeQuoter{}.VerifyPreconditions(*env, changesets.DeployFeeQuoterConfig{ChainSelector: sel, FeeToken: feeToken})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployFeeQuoter{}.Apply(*env, changesets.DeployFeeQuoterConfig{ChainSelector: sel, FeeToken: feeToken})
			return out.DataStore, err
		})
	rerun(t, "OnRamp",
		func() error {
			return changesets.DeployOnRamp{}.VerifyPreconditions(*env, changesets.DeployOnRampConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployOnRamp{}.Apply(*env, changesets.DeployOnRampConfig{ChainSelector: sel})
			return out.DataStore, err
		})
	rerun(t, "OffRamp",
		func() error {
			return changesets.DeployOffRamp{}.VerifyPreconditions(*env, changesets.DeployOffRampConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployOffRamp{}.Apply(*env, changesets.DeployOffRampConfig{ChainSelector: sel})
			return out.DataStore, err
		})
	rerun(t, "Router",
		func() error {
			return changesets.DeployRouter{}.VerifyPreconditions(*env, changesets.DeployRouterConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployRouter{}.Apply(*env, changesets.DeployRouterConfig{ChainSelector: sel})
			return out.DataStore, err
		})
	rerun(t, "CommitteeVerifier",
		func() error {
			return changesets.DeployCommitteeVerifier{}.VerifyPreconditions(*env, changesets.DeployCommitteeVerifierConfig{
				ChainSelector:    sel,
				StorageLocations: [][]byte{storageLocation},
			})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployCommitteeVerifier{}.Apply(*env, changesets.DeployCommitteeVerifierConfig{
				ChainSelector:    sel,
				StorageLocations: [][]byte{storageLocation},
			})
			return out.DataStore, err
		})
	rerun(t, "ccip_receiver_example",
		func() error {
			return changesets.DeployCCIPReceiver{}.VerifyPreconditions(*env, changesets.DeployCCIPReceiverConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployCCIPReceiver{}.Apply(*env, changesets.DeployCCIPReceiverConfig{ChainSelector: sel})
			return out.DataStore, err
		})

	reports, err := rerunReporter.GetReports()
	require.NoError(t, err)
	require.Len(t, reports, 12)
	for _, r := range reports {
		require.NotContains(t, r.Def.ID, ":", "unexpected op report %q: rerun must send no transactions", r.Def.ID)
	}
}
