//go:build integration

package integration

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"

	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	_ "github.com/smartcontractkit/chainlink-stellar/deployment/adapters" // registers the token adapter + fee resolver the changeset dispatches to
	stellaradapters "github.com/smartcontractkit/chainlink-stellar/deployment/adapters"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/changesets"
	bmpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
)

// TestConfigureTokenPool drives the ccip ConfigureTokenPool changeset
// against a live deployer-owned pool through the stellar token adapter's
// TokenPoolDynamicConfigAdapter + TokenFeeAdapter surfaces: router,
// rate-limit admin and fee admin updates, the pool's allowed-finality config,
// and a per-remote token transfer fee that rides both the fee-quoter arm (via
// the registered fee resolver through a wired Router→OnRamp lane) and the
// pool-level arm. A fresh-bundle re-apply is idempotent.
func TestConfigureTokenPool(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	_, deployerKP, deployer, rpcClient, passphrase, _ := GetIsolatedTestEnv(ctx, t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	dst := chainsel.ETHEREUM_TESTNET_SEPOLIA.Selector

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

	// Lean core incl. the OnRamp deploy deps (TAR, RMN Proxy, FeeQuoter).
	apply("RMN Remote", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRMNRemote{}.Apply(cldfEnv, changesets.DeployRMNRemoteConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	feeToken, err := ccip.RMNRemoteDatastoreRef().LookupStrkey(cldfEnv.DataStore, sel)
	require.NoError(t, err)
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
	apply("FeeQuoter", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployFeeQuoter{}.Apply(cldfEnv, changesets.DeployFeeQuoterConfig{ChainSelector: sel, FeeToken: feeToken})
		return out.DataStore, err
	})
	apply("OnRamp", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployOnRamp{}.Apply(cldfEnv, changesets.DeployOnRampConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	routerID, err := ccip.RouterDatastoreRef().LookupStrkey(cldfEnv.DataStore, sel)
	require.NoError(t, err)
	onRampID, err := ccip.OnRampDatastoreRef().LookupStrkey(cldfEnv.DataStore, sel)
	require.NoError(t, err)
	routerClient := routerbindings.NewRouterClient(deployer, routerID)
	// Wire the Router→OnRamp lane for the destination: the per-remote fee
	// arm's fee resolver reads it to resolve the canonical OnRamp ref.
	require.NoError(t, routerClient.SetOnramp(ctx, dst, onRampID))

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
		TokenPoolQualifier: "configure-token-pool-test",
		TokenRef:           &tokenRef,
		ExistingDataStore:  cldfEnv.DataStore,
	})
	require.NoError(t, err)
	poolRef := poolReport.Output.Addresses[0]
	require.NoError(t, ds.Addresses().Upsert(poolRef))
	cldfEnv.DataStore = ds.Seal()
	poolStrkey, err := scval.HexToContractStrkey(poolRef.Address)
	require.NoError(t, err)
	poolClient := tpoolbindings.NewTokenPoolClient(deployer, poolStrkey)

	// Wire the pool's lane so the per-remote fee arm has a supported remote.
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

	// The changeset input: dynamic config (router + admins), finality, and a
	// per-remote fee config on the wired lane.
	admin := stellarutil.MustGenerateMockContractID("configure-token-pool", "admin")
	wantFinality := finality.Config{WaitForSafe: true, BlockDepth: 1}
	wantFee := &tokens.PartialTokenTransferFeeConfig{
		IsEnabled:       cciputils.NewOptional(true),
		DestGasOverhead: cciputils.NewOptional(uint32(50_000)),
	}
	input := tokens.ConfigureTokenPoolInput{
		Chains: []tokens.ConfigureTokenPoolPerChain{{
			ChainSelector: sel,
			Pools: []tokens.PoolConfigUpdate{{
				TokenPoolRef:   poolRef,
				FinalityConfig: &wantFinality,
				RateLimitAdmin: &admin,
				FeeAdmin:       &admin,
				Router:         &routerID,
				Remotes: []tokens.RemoteConfigUpdate{{
					RemoteChainSelector:    dst,
					TokenTransferFeeConfig: wantFee,
				}},
			}},
		}},
	}

	cs := tokens.ConfigureTokenPool()
	require.NoError(t, cs.VerifyPreconditions(cldfEnv, input))
	out, err := cs.Apply(cldfEnv, input)
	require.NoError(t, err)
	require.Empty(t, out.MCMSTimelockProposals, "deployer-key mode submits directly")

	// The dynamic config arms: raw reads agree with the requested values.
	gotRouter, err := poolClient.GetRouter(ctx)
	require.NoError(t, err)
	require.NotNil(t, gotRouter)
	require.Equal(t, routerID, *gotRouter)
	gotAdmin, err := poolClient.GetRateLimitAdmin(ctx)
	require.NoError(t, err)
	require.NotNil(t, gotAdmin)
	require.Equal(t, admin, *gotAdmin)
	gotFeeAdmin, err := poolClient.GetFeeAdmin(ctx)
	require.NoError(t, err)
	require.NotNil(t, gotFeeAdmin)
	require.Equal(t, admin, *gotFeeAdmin)

	// The finality arm: raw bytes agree.
	wantRaw := wantFinality.Raw()
	gotFinality, err := poolClient.GetAllowedFinalityConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, binary.BigEndian.Uint32(wantRaw[:]), gotFinality)

	// The per-remote fee arm, pool side: the mapped binding form.
	poolFee, err := poolClient.GetTokenTransferFeeConfig(ctx, dst)
	require.NoError(t, err)
	require.NotNil(t, poolFee)
	require.True(t, poolFee.IsEnabled)
	require.Equal(t, uint32(50_000), poolFee.DestGasOverhead)

	// The per-remote fee arm, fee-quoter side: the fee resolver resolved the
	// wired Router→OnRamp lane and applied the config through the fee adapter.
	fqID, err := ccip.FeeQuoterDatastoreRef().LookupStrkey(cldfEnv.DataStore, sel)
	require.NoError(t, err)
	fqClient := fqbindings.NewFeeQuoterClient(deployer, fqID)
	tokenStrkey, err := scval.HexToContractStrkey(tokenRef.Address)
	require.NoError(t, err)
	fqFee, err := fqClient.GetTokenFeeConfig(ctx, dst, tokenStrkey)
	require.NoError(t, err)
	require.NotNil(t, fqFee)
	require.True(t, fqFee.IsEnabled)

	// Idempotent re-apply on a fresh bundle: every arm reads current state
	// first, so nothing re-executes on chain.
	cldfEnv.OperationsBundle = newBundle()
	rerun, err := cs.Apply(cldfEnv, input)
	require.NoError(t, err)
	require.Empty(t, rerun.MCMSTimelockProposals)
	gotRouter2, err := poolClient.GetRouter(ctx)
	require.NoError(t, err)
	require.Equal(t, routerID, *gotRouter2)
}
