//go:build integration

package integration

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	bmpbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/burn_mint_pool"
	timelockbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/timelock"
	tpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellaradapters "github.com/smartcontractkit/chainlink-stellar/deployment/adapters"
	"github.com/smartcontractkit/chainlink-stellar/deployment/changesets"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	bmpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// TestTokenAdapterPoolOps proves the token adapter's pool operations end to
// end on a live network: the shared pipeline's token + pool deploy sequences,
// rate limits on a deployer-owned pool (direct exec, read back on-chain),
// UpdateAuthorities transferring the pool to the CLL timelock (direct
// transfer, MCMS-gated accept), and rate limits again on the timelock-owned
// pool (MCMS proposals with caller = timelock, executed through the timelock).
func TestTokenAdapterPoolOps(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, passphrase, _ := GetIsolatedTestEnv(ctx, t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	dst := chainsel.ETHEREUM_TESTNET_SEPOLIA.Selector

	ds := datastore.NewMemoryDataStore()
	sealed := ds.Seal()
	e2eEnv := &helpers.E2ETestEnv{
		DeployerKP:        deployerKP,
		Deployer:          deployer,
		RPCClient:         rpcClient,
		NetworkPassphrase: passphrase,
		StellarRoot:       projectRoot,
	}
	newBundle := func() cldfops.Bundle {
		return cldfops.NewBundle(func() context.Context { return ctx }, cldflogger.Test(t), cldfops.NewMemoryReporter())
	}
	cldfEnv := cldf.Environment{
		Logger:     cldflogger.Test(t),
		GetContext: func() context.Context { return ctx },
		DataStore:  sealed,
		BlockChains: cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: cldf_stellar.Chain{
			ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
			Signer:            bindings.NewStellarKeypairSigner(deployerKP),
			Client:            rpcClient,
			NetworkPassphrase: passphrase,
		}}),
		OperationsBundle: newBundle(),
	}

	// Lean core: the refs the pool deploy sequence resolves.
	apply := func(name string, apply func() (datastore.MutableDataStore, error)) {
		t.Helper()
		out, err := apply()
		require.NoError(t, err, "%s: apply failed", name)
		require.NoError(t, ds.Merge(out.Seal()), "%s: merge output", name)
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

	// The shared pipeline's deploy path: token, then pool.
	preMint := uint64(1_000)
	tokenReport, err := cldfops.ExecuteSequence(newBundle(), a.DeployToken(), cldfEnv.BlockChains, tokens.DeployTokenInput{
		ChainSelector:     sel,
		Symbol:            "BnM",
		Name:              "BnM",
		Decimals:          7,
		PreMint:           &preMint,
		ExistingDataStore: sealed,
	})
	require.NoError(t, err)
	require.Len(t, tokenReport.Output.Addresses, 1)
	tokenRef := tokenReport.Output.Addresses[0]
	require.NoError(t, ds.Addresses().Upsert(tokenRef))

	poolReport, err := cldfops.ExecuteSequence(newBundle(), a.DeployTokenPoolForToken(), cldfEnv.BlockChains, tokens.DeployTokenPoolInput{
		ChainSelector:      sel,
		TokenPoolVersion:   semver.MustParse("2.0.0"),
		PoolType:           bmpops.ContractType,
		TokenPoolQualifier: "pool-ops-test",
		TokenRef:           &tokenRef,
		ExistingDataStore:  sealed,
	})
	require.NoError(t, err)
	require.Len(t, poolReport.Output.Addresses, 1)
	poolRef := poolReport.Output.Addresses[0]
	require.NoError(t, ds.Addresses().Upsert(poolRef))
	poolStrkey, err := scval.HexToContractStrkey(poolRef.Address)
	require.NoError(t, err)
	poolClient := bmpbindings.NewBurnMintPoolClient(deployer, poolStrkey)

	t.Run("rate limits on deployer-owned pool execute directly", func(t *testing.T) {
		// Wire the dst lane first while the deployer still owns the pool:
		// set_rate_limit_config rejects unsupported chains
		// (ChainNotSupported), and the pool is born with no remote lanes.
		// Distinct synthetic values (0xcc/0xdd) so the later configuration
		// subtest still exercises a real (replacement) change.
		lanePool := make([]byte, 20)
		lanePool[0] = 0xcc
		laneToken := make([]byte, 20)
		laneToken[0] = 0xdd
		wireOut, err := cldfops.ExecuteSequence(newBundle(), a.ConfigureTokenForTransfersSequence(), cldfEnv.BlockChains, tokens.ConfigureTokenForTransfersInput{
			ChainSelector:     sel,
			TokenPoolAddress:  poolRef.Address,
			TokenAddress:      tokenRef.Address,
			PoolType:          bmpops.ContractType,
			TokenRef:          datastore.AddressRef{Address: tokenRef.Address},
			ExistingDataStore: sealed,
			RemoteChains: map[uint64]tokens.RemoteChainConfig[[]byte, string]{
				dst: {RemotePool: lanePool, RemoteToken: laneToken, RemoteDecimals: 18},
			},
		})
		require.NoError(t, err)
		require.NotEmpty(t, wireOut.Output.BatchOps, "the lane must be wired before the rate-limit calls")

		out, err := cldfops.ExecuteSequence(newBundle(), a.SetTokenPoolRateLimits(), cldfEnv.BlockChains, poolRateLimitsInput(sel, dst, poolRef, sealed, 1_000))
		require.NoError(t, err)
		require.Len(t, out.Output.BatchOps, 1)
		require.Empty(t, out.Output.BatchOps[0].Transactions, "direct exec returns an ExecInfo-marked empty batch op")

		for _, fastFinality := range []bool{false, true} {
			state, err := poolClient.GetCurrentRateLimiterState(ctx, dst, fastFinality)
			require.NoError(t, err)
			require.True(t, state.Outbound.IsEnabled)
			require.Equal(t, xdr.Uint64(1_000), state.Outbound.Capacity.Lo)
			require.True(t, state.Inbound.IsEnabled)
		}
	})

	// The CLL governance stack: UpdateAuthorities transfers the pool to its timelock.
	gov := helpers.DeployMCMSStackWithRoles(t, ctx, e2eEnv, sel, cciputils.CLLQualifier, helpers.DefaultMCMSTimelockMinDelaySec)
	require.NoError(t, ds.Addresses().Upsert(datastore.AddressRef{
		ChainSelector: sel,
		Type:          datastore.ContractType(cciputils.RBACTimelock),
		Version:       deploy.MCMSVersion,
		Qualifier:     cciputils.CLLQualifier,
		Address:       gov.TimelockID,
	}))

	t.Run("update authorities transfers the pool to the timelock", func(t *testing.T) {
		out, err := cldfops.ExecuteSequence(newBundle(), a.UpdateAuthorities(), &cldfEnv, tokens.UpdateAuthoritiesInput{
			ChainSelector: sel,
			TokenRef:      tokenRef,
			TokenPoolRef:  poolRef,
		})
		require.NoError(t, err)
		// The deployer still owns the pool: transfer_ownership ran directly;
		// accept_ownership (pending owner = timelock) routes to an MCMS proposal.
		require.Len(t, out.Output.BatchOps, 1)
		require.Len(t, out.Output.BatchOps[0].Transactions, 1)
		tx := out.Output.BatchOps[0].Transactions[0]
		fn, _, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
		require.NoError(t, err)
		require.Equal(t, "accept_ownership", fn)

		calls, err := helpers.TimelockCallsFromProposalTx(tx)
		require.NoError(t, err)
		var pred, salt [32]byte
		salt[31] = 1
		helpers.MCMSTimelockScheduleAndExecute(t, ctx, e2eEnv, gov, calls, pred, salt)

		owner, err := poolClient.Owner(ctx)
		require.NoError(t, err)
		require.NotNil(t, owner)
		require.Equal(t, gov.TimelockID, *owner, "timelock should own the pool after UpdateAuthorities")

		// Idempotent rerun on a fresh bundle: both sequences skip, no proposals.
		rerun, err := cldfops.ExecuteSequence(newBundle(), a.UpdateAuthorities(), &cldfEnv, tokens.UpdateAuthoritiesInput{
			ChainSelector: sel,
			TokenRef:      tokenRef,
			TokenPoolRef:  poolRef,
		})
		require.NoError(t, err)
		require.Empty(t, rerun.Output.BatchOps, "rerun after transfer emits no proposal")
	})

	t.Run("configure token for transfers wires the lane idempotently", func(t *testing.T) {
		// A synthetic EVM-style remote pool/token (20 bytes raw); the sequence
		// stores both left-padded to 32 on chain.
		evmRemotePool := make([]byte, 20)
		evmRemotePool[0] = 0xaa
		evmRemoteToken := make([]byte, 20)
		evmRemoteToken[0] = 0xbb
		input := tokens.ConfigureTokenForTransfersInput{
			ChainSelector:     sel,
			TokenPoolAddress:  poolRef.Address,
			TokenAddress:      tokenRef.Address,
			PoolType:          bmpops.ContractType,
			TokenRef:          datastore.AddressRef{Address: tokenRef.Address},
			ExistingDataStore: sealed,
			RemoteChains: map[uint64]tokens.RemoteChainConfig[[]byte, string]{
				dst: {RemotePool: evmRemotePool, RemoteToken: evmRemoteToken, RemoteDecimals: 18},
			},
		}
		out, err := cldfops.ExecuteSequence(newBundle(), a.ConfigureTokenForTransfersSequence(), cldfEnv.BlockChains, input)
		require.NoError(t, err)
		require.NotEmpty(t, out.Output.BatchOps)
		// The pool is timelock-owned at this point: the lane replacement rides
		// proposals — execute them through the timelock before reading back.
		calls := timelockbindings.Calls{}
		for _, op := range out.Output.BatchOps {
			for _, tx := range op.Transactions {
				txCalls, err := helpers.TimelockCallsFromProposalTx(tx)
				require.NoError(t, err)
				calls.Inner = append(calls.Inner, txCalls.Inner...)
			}
		}
		require.NotEmpty(t, calls.Inner, "the remote-token change must emit proposals")
		var pred, execSalt [32]byte
		execSalt[31] = 3
		helpers.MCMSTimelockScheduleAndExecute(t, ctx, e2eEnv, gov, calls, pred, execSalt)

		// The lane is wired on chain: supported, padded remote token and pool.
		sharedPool := tpoolbindings.NewTokenPoolClient(deployer, poolStrkey)
		supported, err := sharedPool.IsSupportedChain(ctx, dst)
		require.NoError(t, err)
		require.True(t, supported, "remote lane should be supported after configure")
		remoteToken, err := sharedPool.GetRemoteToken(ctx, dst)
		require.NoError(t, err)
		require.Equal(t, common.LeftPadBytes(evmRemoteToken, 32), remoteToken)
		remotePools, err := sharedPool.GetRemotePools(ctx, dst)
		require.NoError(t, err)
		require.Len(t, remotePools, 1)
		require.Equal(t, common.LeftPadBytes(evmRemotePool, 32), remotePools[0])

		// Idempotent rerun on a fresh bundle: everything already matches, no writes.
		rerun, err := cldfops.ExecuteSequence(newBundle(), a.ConfigureTokenForTransfersSequence(), cldfEnv.BlockChains, input)
		require.NoError(t, err)
		require.Empty(t, rerun.Output.BatchOps, "an already-configured lane must emit no writes")
	})

	t.Run("rate limits on timelock-owned pool go through the timelock", func(t *testing.T) {
		out, err := cldfops.ExecuteSequence(newBundle(), a.SetTokenPoolRateLimits(), cldfEnv.BlockChains, poolRateLimitsInput(sel, dst, poolRef, sealed, 2_000))
		require.NoError(t, err)
		require.Len(t, out.Output.BatchOps, 1)
		require.Len(t, out.Output.BatchOps[0].Transactions, 2, "one proposal transaction per finality bucket")
		calls := timelockbindings.Calls{}
		for _, tx := range out.Output.BatchOps[0].Transactions {
			fn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
			require.NoError(t, err)
			require.Equal(t, "set_rate_limit_config", fn)
			caller, err := scval.AddressFromScVal(args[0])
			require.NoError(t, err)
			require.Equal(t, gov.TimelockID, caller, "the timelock is the authorized caller on the proposal")
			txCopy := tx
			txCalls, err := helpers.TimelockCallsFromProposalTx(txCopy)
			require.NoError(t, err)
			calls.Inner = append(calls.Inner, txCalls.Inner...)
		}
		var pred, salt [32]byte
		salt[31] = 2
		helpers.MCMSTimelockScheduleAndExecute(t, ctx, e2eEnv, gov, calls, pred, salt)

		for _, fastFinality := range []bool{false, true} {
			state, err := poolClient.GetCurrentRateLimiterState(ctx, dst, fastFinality)
			require.NoError(t, err)
			require.True(t, state.Outbound.IsEnabled)
			require.Equal(t, xdr.Uint64(2_000), state.Outbound.Capacity.Lo,
				"the timelock-executed proposal should have updated the on-chain limit")
		}
	})
}

// poolRateLimitsInput builds the TPRL input with both finality buckets at one capacity.
func poolRateLimitsInput(sel, dst uint64, poolRef datastore.AddressRef, ds datastore.DataStore, capacity int64) tokens.TPRLRemotes {
	cfg := tokens.RateLimiterConfig{IsEnabled: true, Capacity: big.NewInt(capacity), Rate: big.NewInt(1)}
	return tokens.TPRLRemotes{
		ChainSelector:       sel,
		RemoteChainSelector: dst,
		TokenPoolRef:        poolRef,
		ExistingDataStore:   ds,
		RateLimitBuckets: []tokens.TPRLRateLimitBucket{
			{FastFinality: false, OutboundRateLimiterConfig: cfg, InboundRateLimiterConfig: cfg},
			{FastFinality: true, OutboundRateLimiterConfig: cfg, InboundRateLimiterConfig: cfg},
		},
	}
}
