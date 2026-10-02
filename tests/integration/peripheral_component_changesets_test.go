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
	"github.com/smartcontractkit/chainlink-stellar/deployment/changesets"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// TestPeripheralComponentChangesetsApplyTwice proves the peripheral component
// changesets (tokens, hooks, extractor, lock box, pools) on a live network: a
// lean core (the stack pools initialize against) is deployed first, then the
// peripheral changesets apply in dependency order against one BnM token, and a
// second pass against the recorded refs (fresh operations bundle, no cached
// reports) sends no transactions and records no new refs.
func TestPeripheralComponentChangesetsApplyTwice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	_, _, _, _, passphrase, _ := GetSharedTestEnv(ctx, t)
	sel := chainsel.STELLAR_LOCALNET.Selector

	// A dedicated random signer (same reason as
	// TestComponentChangesetsApplyAllTwice: fixed salt labels must not collide
	// with the suite-wide deployer's contracts).
	provider := stellarprovider.NewRPCChainProvider(sel, stellarprovider.RPCChainProviderConfig{
		SorobanRPCURL:      sharedEnv.Output.Nodes[0].ExternalHTTPUrl,
		NetworkPassphrase:  passphrase,
		FriendbotURL:       sharedEnv.FriendbotURL,
		DeployerKeypairGen: stellarprovider.KeypairRandom(),
	})
	blockchain, err := provider.Initialize(ctx)
	require.NoError(t, err)

	chain, ok := blockchain.(*cldfstellar.Chain)
	require.True(t, ok, "provider returned %T, want *cldfstellar.Chain", blockchain)
	require.NoError(t, helpers.FundViaFriendbot(sharedEnv.FriendbotURL, chain.Signer.Address()))

	env, err := testenv.New(ctx, testenv.WithChains(blockchain))
	require.NoError(t, err)

	state := datastore.NewMemoryDataStore()
	sealed := state.Seal()
	env.DataStore = sealed

	apply := func(t *testing.T, name string, apply func() (datastore.MutableDataStore, error)) {
		t.Helper()
		out, err := apply()
		require.NoError(t, err, "%s: apply failed", name)
		require.NoError(t, state.Merge(out.Seal()), "%s: merge output", name)
	}
	strkey := func(t *testing.T, ref ccip.DatastoreSorobanContractRef) string {
		t.Helper()
		s, err := ref.LookupStrkey(sealed, sel)
		require.NoError(t, err)
		return s
	}

	// Lean core: the datastore dependencies the pools resolve (RMN Remote is
	// only RMN Proxy's dependency; pools need Router, RampRegistry, RMN Proxy).
	apply(t, "RMN Remote", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRMNRemote{}.Apply(*env, changesets.DeployRMNRemoteConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply(t, "RampRegistry", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRampRegistry{}.Apply(*env, changesets.DeployRampRegistryConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply(t, "RMN Proxy", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRMNProxy{}.Apply(*env, changesets.DeployRMNProxyConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply(t, "Router", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRouter{}.Apply(*env, changesets.DeployRouterConfig{ChainSelector: sel})
		return out.DataStore, err
	})

	// Peripherals in dependency order: extractor and tokens (no deps), hooks,
	// lock box (token), then the three pools (core stack + token, and the lock
	// box for the canonical lock-release pool).
	apply(t, "advanced-pool-hooks-extractor", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployAdvancedPoolHooksExtractor{}.Apply(*env, changesets.DeployAdvancedPoolHooksExtractorConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply(t, "bnm-token", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployBnmToken{}.Apply(*env, changesets.DeployBnmTokenConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply(t, "link-token", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployLinkToken{}.Apply(*env, changesets.DeployLinkTokenConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	bnm := strkey(t, ccip.BnmTokenDatastoreRef())
	apply(t, "advanced-pool-hooks", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployAdvancedPoolHooks{}.Apply(*env, changesets.DeployAdvancedPoolHooksConfig{ChainSelector: sel, Qualifier: "BnM"})
		return out.DataStore, err
	})
	apply(t, "token-lock-box", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployTokenLockBox{}.Apply(*env, changesets.DeployTokenLockBoxConfig{ChainSelector: sel, Qualifier: "BnM", Token: bnm})
		return out.DataStore, err
	})
	lockBox := strkey(t, ccip.TokenLockBoxDatastoreRef("BnM"))
	apply(t, "burn-mint-pool", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployBurnMintPool{}.Apply(*env, changesets.DeployBurnMintPoolConfig{
			ChainSelector: sel, Qualifier: "BnM", Token: bnm, TokenDecimals: 7,
		})
		return out.DataStore, err
	})
	apply(t, "siloed-lock-release-pool", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeploySiloedLockReleasePool{}.Apply(*env, changesets.DeploySiloedLockReleasePoolConfig{
			ChainSelector: sel, Qualifier: "BnM", Token: bnm, TokenDecimals: 7,
		})
		return out.DataStore, err
	})
	apply(t, "lock-release-pool", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployLockReleasePool{}.Apply(*env, changesets.DeployLockReleasePoolConfig{
			ChainSelector: sel, Qualifier: "BnM", Token: bnm, TokenDecimals: 7, LockBox: lockBox,
		})
		return out.DataStore, err
	})

	// Twelve components, twelve refs.
	refs, err := state.Addresses().Fetch()
	require.NoError(t, err)
	require.Len(t, refs, 12)

	// Pass 2: fresh operations bundle (no cached op reports) against the
	// recorded refs — every changeset must verify, re-apply without error,
	// record no new refs, and send no transactions.
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
	rerun(t, "RampRegistry",
		func() error {
			return changesets.DeployRampRegistry{}.VerifyPreconditions(*env, changesets.DeployRampRegistryConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployRampRegistry{}.Apply(*env, changesets.DeployRampRegistryConfig{ChainSelector: sel})
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
	rerun(t, "Router",
		func() error {
			return changesets.DeployRouter{}.VerifyPreconditions(*env, changesets.DeployRouterConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployRouter{}.Apply(*env, changesets.DeployRouterConfig{ChainSelector: sel})
			return out.DataStore, err
		})
	rerun(t, "advanced-pool-hooks-extractor",
		func() error {
			return changesets.DeployAdvancedPoolHooksExtractor{}.VerifyPreconditions(*env, changesets.DeployAdvancedPoolHooksExtractorConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployAdvancedPoolHooksExtractor{}.Apply(*env, changesets.DeployAdvancedPoolHooksExtractorConfig{ChainSelector: sel})
			return out.DataStore, err
		})
	rerun(t, "bnm-token",
		func() error {
			return changesets.DeployBnmToken{}.VerifyPreconditions(*env, changesets.DeployBnmTokenConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployBnmToken{}.Apply(*env, changesets.DeployBnmTokenConfig{ChainSelector: sel})
			return out.DataStore, err
		})
	rerun(t, "link-token",
		func() error {
			return changesets.DeployLinkToken{}.VerifyPreconditions(*env, changesets.DeployLinkTokenConfig{ChainSelector: sel})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployLinkToken{}.Apply(*env, changesets.DeployLinkTokenConfig{ChainSelector: sel})
			return out.DataStore, err
		})
	rerun(t, "advanced-pool-hooks",
		func() error {
			return changesets.DeployAdvancedPoolHooks{}.VerifyPreconditions(*env, changesets.DeployAdvancedPoolHooksConfig{ChainSelector: sel, Qualifier: "BnM"})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployAdvancedPoolHooks{}.Apply(*env, changesets.DeployAdvancedPoolHooksConfig{ChainSelector: sel, Qualifier: "BnM"})
			return out.DataStore, err
		})
	rerun(t, "token-lock-box",
		func() error {
			return changesets.DeployTokenLockBox{}.VerifyPreconditions(*env, changesets.DeployTokenLockBoxConfig{ChainSelector: sel, Qualifier: "BnM", Token: bnm})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployTokenLockBox{}.Apply(*env, changesets.DeployTokenLockBoxConfig{ChainSelector: sel, Qualifier: "BnM", Token: bnm})
			return out.DataStore, err
		})
	rerun(t, "burn-mint-pool",
		func() error {
			return changesets.DeployBurnMintPool{}.VerifyPreconditions(*env, changesets.DeployBurnMintPoolConfig{
				ChainSelector: sel, Qualifier: "BnM", Token: bnm, TokenDecimals: 7,
			})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployBurnMintPool{}.Apply(*env, changesets.DeployBurnMintPoolConfig{
				ChainSelector: sel, Qualifier: "BnM", Token: bnm, TokenDecimals: 7,
			})
			return out.DataStore, err
		})
	rerun(t, "siloed-lock-release-pool",
		func() error {
			return changesets.DeploySiloedLockReleasePool{}.VerifyPreconditions(*env, changesets.DeploySiloedLockReleasePoolConfig{
				ChainSelector: sel, Qualifier: "BnM", Token: bnm, TokenDecimals: 7,
			})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeploySiloedLockReleasePool{}.Apply(*env, changesets.DeploySiloedLockReleasePoolConfig{
				ChainSelector: sel, Qualifier: "BnM", Token: bnm, TokenDecimals: 7,
			})
			return out.DataStore, err
		})
	rerun(t, "lock-release-pool",
		func() error {
			return changesets.DeployLockReleasePool{}.VerifyPreconditions(*env, changesets.DeployLockReleasePoolConfig{
				ChainSelector: sel, Qualifier: "BnM", Token: bnm, TokenDecimals: 7, LockBox: lockBox,
			})
		},
		func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployLockReleasePool{}.Apply(*env, changesets.DeployLockReleasePoolConfig{
				ChainSelector: sel, Qualifier: "BnM", Token: bnm, TokenDecimals: 7, LockBox: lockBox,
			})
			return out.DataStore, err
		})

	reports, err := rerunReporter.GetReports()
	require.NoError(t, err)
	require.Len(t, reports, 12)
	for _, r := range reports {
		require.NotContains(t, r.Def.ID, ":", "unexpected op report %q: rerun must send no transactions", r.Def.ID)
	}
}
