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
	"github.com/smartcontractkit/chainlink-ccip/deployment/fees"
	"github.com/smartcontractkit/chainlink-ccip/deployment/lanes"

	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/changesets"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"

	// Registers the Stellar fee adapter the test looks up.
	_ "github.com/smartcontractkit/chainlink-stellar/deployment/adapters"
)

// TestFeeAdapterReadback writes fee configs on a live FeeQuoter through the
// fee adapter's write sequences, then reads them back through the on-chain
// getters and asserts the values round-trip. Both writes are re-applied on a
// fresh operations bundle: a reused bundle replays recorded reports for
// identical inputs instead of hitting the chain.
func TestFeeAdapterReadback(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	_, _, _, _, passphrase, _ := GetIsolatedTestEnv(ctx, t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	dst := chainsel.ETHEREUM_TESTNET_SEPOLIA.Selector

	// Dedicated random signer (same reason as
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

	newBundle := func() cldfops.Bundle {
		return cldfops.NewBundle(func() context.Context { return ctx }, cldflogger.Test(t), cldfops.NewMemoryReporter())
	}

	// Lean stack: RMN Remote (fee-token source, per the component harness) and
	// the FeeQuoter itself.
	apply := func(name string, apply func() (datastore.MutableDataStore, error)) {
		t.Helper()
		out, err := apply()
		require.NoError(t, err, "%s: apply failed", name)
		require.NoError(t, state.Merge(out.Seal()), "%s: merge output", name)
	}
	apply("RMN Remote", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRMNRemote{}.Apply(*env, changesets.DeployRMNRemoteConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	feeToken, err := ccip.RMNRemoteDatastoreRef().LookupStrkey(sealed, sel)
	require.NoError(t, err)
	apply("FeeQuoter", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployFeeQuoter{}.Apply(*env, changesets.DeployFeeQuoterConfig{
			ChainSelector: sel,
			FeeToken:     feeToken,
		})
		return out.DataStore, err
	})
	fqRef, err := ccip.FeeQuoterDatastoreRef().LookupAddressRef(sealed, sel)
	require.NoError(t, err)

	feeAdapter, ok := fees.GetRegistry().GetFeeAdapter(chainsel.FamilyStellar, stellarops.ContractDeploymentVersion)
	require.True(t, ok, "stellar fee adapter should be registered from init()")

	token := stellarutil.MustGenerateMockContractID("fee-readback", "token")
	wantFeeArgs := fees.TokenTransferFeeArgs{
		DestBytesOverhead: 10,
		DestGasOverhead:   50_000,
		MinFeeUSDCents:    3,
		IsEnabled:         true,
	}
	wantDestChainConfig := lanes.FeeQuoterDestChainConfig{
		IsEnabled:                   true,
		MaxDataBytes:                2_000,
		MaxPerMsgGasLimit:           4_000_000,
		DestGasOverhead:             350_000,
		DestGasPerPayloadByteBase:   16,
		DefaultTokenFeeUSDCents:     50,
		DefaultTokenDestGasOverhead: 50_000,
		DefaultTxGasLimit:           200_000,
		NetworkFeeUSDCents:          100,
		V2Params:                    &lanes.FeeQuoterV2Params{LinkFeeMultiplierPercent: 90},
	}

	runWrites := func(b cldfops.Bundle) {
		feeSeq := feeAdapter.SetTokenTransferFee(sealed, fqRef)
		_, err := cldfops.ExecuteSequence(b, feeSeq, env.BlockChains, fees.SetTokenTransferFeeSequenceInput{
			Selector: sel,
			Settings: map[uint64]map[string]*fees.TokenTransferFeeArgs{dst: {token: &wantFeeArgs}},
		})
		require.NoError(t, err)

		destSeq := feeAdapter.ApplyDestChainConfigUpdates(sealed, fqRef)
		_, err = cldfops.ExecuteSequence(b, destSeq, env.BlockChains, fees.ApplyDestChainConfigSequenceInput{
			Selector: sel,
			Settings: map[uint64]lanes.FeeQuoterDestChainConfig{dst: wantDestChainConfig},
		})
		require.NoError(t, err)
	}

	// A first write, then a second identical write on a fresh bundle: the
	// harness convention — re-applying the same config must succeed.
	runWrites(newBundle())
	runWrites(newBundle())

	// Read back through the getters: the values must equal what was written.
	readB := newBundle()
	gotFeeArgs, err := feeAdapter.GetOnchainTokenTransferFeeConfig(readB, env.BlockChains, fqRef, sel, dst, token)
	require.NoError(t, err)
	// MaxFeeUSDCents/DeciBps have no on-chain counterpart; expected zero.
	require.Equal(t, wantFeeArgs, gotFeeArgs)

	gotDestChainConfig, err := feeAdapter.GetOnchainDestChainConfig(readB, env.BlockChains, fqRef, sel, dst)
	require.NoError(t, err)
	require.Equal(t, wantDestChainConfig, gotDestChainConfig)
}
