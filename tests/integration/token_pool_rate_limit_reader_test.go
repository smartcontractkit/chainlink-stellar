//go:build integration

package integration

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	tpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellaradapters "github.com/smartcontractkit/chainlink-stellar/deployment/adapters"
	"github.com/smartcontractkit/chainlink-stellar/deployment/changesets"
	bmpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"

	// Registers the stellar token adapter the test type-asserts.
	_ "github.com/smartcontractkit/chainlink-stellar/deployment/adapters"
)

// TestTokenPoolRateLimitReader drives the token adapter's
// RateLimitReaderAdapter (GetOnchainRateLimits) on a live pool: after a
// bidirectional baseline is written through the adapter's rate-limit sequence,
// the reader returns both buckets matching the raw on-chain state, and an
// unconfigured lane reads as the zero-value configs with no error.
func TestTokenPoolRateLimitReader(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	_, deployerKP, deployer, rpcClient, passphrase, _ := GetIsolatedTestEnv(ctx, t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	dst := chainsel.ETHEREUM_TESTNET_SEPOLIA.Selector
	unwired := chainsel.STELLAR_MAINNET.Selector

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
	apply("RMN Remote", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRMNRemote{}.Apply(cldfEnv, changesets.DeployRMNRemoteConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply("RampRegistry", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRampRegistry{}.Apply(cldfEnv, changesets.DeployRampRegistryConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply("RMN Proxy", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRMNProxy{}.Apply(cldfEnv, changesets.DeployRMNProxyConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply("Router", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRouter{}.Apply(cldfEnv, changesets.DeployRouterConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply("TokenAdminRegistry", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployTokenAdminRegistry{}.Apply(cldfEnv, changesets.DeployTokenAdminRegistryConfig{ChainSelector: sel})
		return out.DataStore, err
	})

	a := &stellaradapters.StellarTokenAdapter{}
	preMint := uint64(1_000)
	tokenReport, err := cldfops.ExecuteSequence(newBundle(), a.DeployToken(), cldfEnv.BlockChains, tokens.DeployTokenInput{
		ChainSelector:     sel,
		Symbol:            "BnM",
		Name:              "BnM",
		Decimals:          7,
		PreMint:           &preMint,
		ExistingDataStore: cldfEnv.DataStore,
	})
	require.NoError(t, err)
	tokenRef := tokenReport.Output.Addresses[0]
	require.NoError(t, ds.Addresses().Upsert(tokenRef))
	cldfEnv.DataStore = ds.Seal()

	poolReport, err := cldfops.ExecuteSequence(newBundle(), a.DeployTokenPoolForToken(), cldfEnv.BlockChains, tokens.DeployTokenPoolInput{
		ChainSelector:      sel,
		TokenPoolVersion:   semver.MustParse("2.0.0"),
		PoolType:           bmpops.ContractType,
		TokenPoolQualifier: "rate-limit-reader-test",
		TokenRef:           &tokenRef,
		ExistingDataStore:  cldfEnv.DataStore,
	})
	require.NoError(t, err)
	poolRef := poolReport.Output.Addresses[0]
	require.NoError(t, ds.Addresses().Upsert(poolRef))
	cldfEnv.DataStore = ds.Seal()
	poolStrkey, err := scval.HexToContractStrkey(poolRef.Address)
	require.NoError(t, err)

	// Wire the lane, then write the bidirectional baseline through the
	// adapter's rate-limit sequence (the proven C2 arm) — the state both
	// buckets read back against.
	evmRemotePool := make([]byte, 20)
	evmRemotePool[0] = 0xaa
	evmRemoteToken := make([]byte, 20)
	evmRemoteToken[0] = 0xbb
	_, err = cldfops.ExecuteSequence(newBundle(), a.ConfigureTokenForTransfersSequence(), cldfEnv.BlockChains, tokens.ConfigureTokenForTransfersInput{
		ChainSelector:               sel,
		TokenPoolAddress:            poolRef.Address,
		TokenAddress:                tokenRef.Address,
		PoolType:                    bmpops.ContractType,
		TokenRef:                    tokenRef,
		ExistingDataStore:           cldfEnv.DataStore,
		SkipTokenAdminRegistrySetup: true,
		RemoteChains: map[uint64]tokens.RemoteChainConfig[[]byte, string]{
			dst: {RemotePool: evmRemotePool, RemoteToken: evmRemoteToken, RemoteDecimals: 18},
		},
	})
	require.NoError(t, err)

	wantOutbound := tokens.RateLimiterConfig{IsEnabled: true, Capacity: big.NewInt(1_000), Rate: big.NewInt(10)}
	wantInbound := tokens.RateLimiterConfig{IsEnabled: true, Capacity: big.NewInt(2_000), Rate: big.NewInt(20)}
	_, err = cldfops.ExecuteSequence(newBundle(), a.SetTokenPoolRateLimits(), cldfEnv.BlockChains, tokens.TPRLRemotes{
		ChainSelector:       sel,
		RemoteChainSelector: dst,
		TokenPoolRef:        poolRef,
		TokenRef:            tokenRef,
		ExistingDataStore:   cldfEnv.DataStore,
		RateLimitBuckets: []tokens.TPRLRateLimitBucket{{
			FastFinality:              false,
			OutboundRateLimiterConfig: wantOutbound,
			InboundRateLimiterConfig:  wantInbound,
		}},
	})
	require.NoError(t, err)

	// The reader on the registered token adapter (the form the outboundOnly
	// arm of SetTokenPoolRateLimits discovers it in).
	adapter, ok := tokens.GetTokenAdapterRegistry().GetTokenAdapter(chainsel.FamilyStellar, semver.MustParse("2.0.0"))
	require.True(t, ok)
	reader, ok := adapter.(tokens.RateLimitReaderAdapter)
	require.True(t, ok)

	t.Run("configured lane reads both buckets", func(t *testing.T) {
		got, err := reader.GetOnchainRateLimits(newBundle(), cldfEnv.BlockChains, cldfEnv.DataStore, sel, poolRef, tokenRef, dst, false)
		require.NoError(t, err)
		require.Equal(t, wantOutbound, got.Outbound)
		require.Equal(t, wantInbound, got.Inbound)

		// The raw on-chain state agrees with the reader (the read the
		// outboundOnly arm substitutes for a user-supplied inbound).
		poolClient := tpoolbindings.NewTokenPoolClient(deployer, poolStrkey)
		raw, err := poolClient.GetCurrentRateLimiterState(ctx, dst, false)
		require.NoError(t, err)
		require.Equal(t, raw.Outbound.IsEnabled, got.Outbound.IsEnabled)
	})

	t.Run("unconfigured lane reads as zero-value configs", func(t *testing.T) {
		got, err := reader.GetOnchainRateLimits(newBundle(), cldfEnv.BlockChains, cldfEnv.DataStore, sel, poolRef, tokenRef, unwired, false)
		require.NoError(t, err)
		require.Equal(t, tokens.RateLimiterConfig{IsEnabled: false, Capacity: big.NewInt(0), Rate: big.NewInt(0)}, got.Outbound)
		require.Equal(t, got.Outbound, got.Inbound)
	})
}
