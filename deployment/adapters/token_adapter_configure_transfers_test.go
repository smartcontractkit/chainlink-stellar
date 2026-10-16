package adapters

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	tpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	scval "github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	bmpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// configureFixtures holds the identities one configure/registration test needs.
type configureFixtures struct {
	pool     string
	poolHex  string
	token    string
	tokenHex string
	tar      string
	deployer string
	timelock string
	third    string
	// evmRemote is a 20-byte EVM-style remote pool address; evmRemoteToken likewise.
	evmRemote      []byte
	evmRemoteToken []byte
}

func newConfigureFixtures(t *testing.T) configureFixtures {
	t.Helper()
	f := configureFixtures{
		pool:     stellarutil.MustGenerateMockContractID("deployer", "cfg-test-pool"),
		token:    stellarutil.MustGenerateMockContractID("deployer", "cfg-test-token"),
		tar:      stellarutil.MustGenerateMockContractID("deployer", "cfg-test-tar"),
		deployer: stellarutil.MustGenerateMockContractID("deployer", "cfg-test-deployer"),
		timelock: stellarutil.MustGenerateMockContractID("deployer", "cfg-test-timelock"),
		third:    stellarutil.MustGenerateMockContractID("deployer", "cfg-test-third"),
	}
	var err error
	f.poolHex, err = stellarutil.StrkeyToHex(f.pool)
	require.NoError(t, err)
	f.tokenHex, err = stellarutil.StrkeyToHex(f.token)
	require.NoError(t, err)
	f.evmRemote = make([]byte, 20)
	f.evmRemote[0] = 0xaa
	f.evmRemoteToken = make([]byte, 20)
	f.evmRemoteToken[0] = 0xbb
	return f
}

// tarDS returns a datastore holding the TAR ref.
func tarDS(t *testing.T, f configureFixtures) datastore.DataStore {
	t.Helper()
	ds := datastore.NewMemoryDataStore()
	require.NoError(t, stellarccip.TokenAdminRegistryDatastoreRef().UpsertDeployedStrKey(ds, 42, f.tar))
	return ds.Seal()
}

// stubTarOwner stubs the TAR owner read.
func stubTarOwner(inv *operationstest.RecordingInvoker, f configureFixtures, owner string) *operationstest.RecordingInvoker {
	v := scval.AddressToScVal(owner)
	return inv.WithSimulateResultForContract(f.tar, "owner", &v)
}

// stubTarConfig stubs the TAR token config read.
func stubTarConfig(t *testing.T, inv *operationstest.RecordingInvoker, f configureFixtures, cfg tarbindings.TokenConfig) *operationstest.RecordingInvoker {
	t.Helper()
	v, err := cfg.ToScVal()
	require.NoError(t, err)
	return inv.WithSimulateResultForContract(f.tar, "get_token_config", &v)
}

// stubPoolReads stubs the reads every configure pass performs (decimals,
// owner, rate-limit admin) plus the TAR reads.
func stubPoolReads(t *testing.T, inv *operationstest.RecordingInvoker, f configureFixtures) *operationstest.RecordingInvoker {
	t.Helper()
	decimals := scval.Uint32ToScVal(7)
	inv = inv.WithSimulateResultForContract(f.pool, "get_token_decimals", &decimals)
	inv = stubPoolOwner(inv, f, f.deployer)
	return stubTarConfig(t, stubTarOwner(inv, f, f.deployer), f, tarbindings.TokenConfig{})
}

// stubPoolOwner stubs the pool owner and rate-limit admin reads.
func stubPoolOwner(inv *operationstest.RecordingInvoker, f configureFixtures, owner string) *operationstest.RecordingInvoker {
	v := scval.AddressToScVal(owner)
	inv = inv.WithSimulateResultForContract(f.pool, "owner", &v)
	return inv.WithSimulateResultForContract(f.pool, "get_rate_limit_admin", &v)
}

func recordsFor(inv *operationstest.RecordingInvoker, fn string) []operationstest.InvokeRecord {
	var out []operationstest.InvokeRecord
	for _, r := range inv.Records() {
		if r.Fn == fn {
			out = append(out, r)
		}
	}
	return out
}

func TestStellarTokenAdapter_ManualRegistration(t *testing.T) {
	t.Parallel()
	f := newConfigureFixtures(t)

	newInput := func(proposedOwner string) tokens.ManualRegistrationSequenceInput {
		return tokens.ManualRegistrationSequenceInput{
			RegisterTokenConfig: tokens.RegisterTokenConfig{
				TokenPoolRef:  datastore.AddressRef{Type: datastore.ContractType(bmpops.ContractType), Address: f.poolHex},
				TokenRef:      datastore.AddressRef{Address: f.tokenHex},
				ChainSelector: 42,
				ProposedOwner: proposedOwner,
			},
			ExistingDataStore: tarDS(t, f),
		}
	}

	t.Run("fresh registration with our proposed owner", func(t *testing.T) {
		t.Parallel()
		inv := stubTarConfig(t, stubTarOwner(operationstest.NewRecordingInvoker(), f, f.deployer), f, tarbindings.TokenConfig{})
		out, err := registerTokenInTAR(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, tarDS(t, f), 42, f.tar, f.token, f.pool, f.deployer, true)
		require.NoError(t, err)
		require.Len(t, recordsFor(inv, "propose_administrator"), 1)
		require.Len(t, recordsFor(inv, "accept_admin_role"), 1)
		require.Len(t, recordsFor(inv, "set_pool"), 1)
		// One ExecInfo-marked empty batch op for the direct executions.
		require.Len(t, out.BatchOps, 1)
		require.Empty(t, out.BatchOps[0].Transactions)
	})

	t.Run("external proposed owner is left pending", func(t *testing.T) {
		t.Parallel()
		inv := stubTarConfig(t, stubTarOwner(operationstest.NewRecordingInvoker(), f, f.deployer), f, tarbindings.TokenConfig{})
		_, err := registerTokenInTAR(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, tarDS(t, f), 42, f.tar, f.token, f.pool, f.third, true)
		require.NoError(t, err)
		require.Len(t, recordsFor(inv, "propose_administrator"), 1)
		require.Empty(t, recordsFor(inv, "accept_admin_role"), "external admin accepts its own role")
		require.Empty(t, recordsFor(inv, "set_pool"))
	})

	t.Run("already registered is a no-op", func(t *testing.T) {
		t.Parallel()
		admin := f.deployer
		pool := f.pool
		inv := stubTarConfig(t, stubTarOwner(operationstest.NewRecordingInvoker(), f, f.deployer), f, tarbindings.TokenConfig{
			Administrator: &admin, TokenPool: &pool,
		})
		out, err := registerTokenInTAR(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, tarDS(t, f), 42, f.tar, f.token, f.pool, f.deployer, true)
		require.NoError(t, err)
		for _, r := range inv.Records() {
			require.NotContains(t, []string{"propose_administrator", "accept_admin_role", "set_pool", "transfer_admin_role"}, r.Fn,
				"an already-registered token must not be rewritten")
		}
		require.Empty(t, out.BatchOps)
	})

	t.Run("reassign transfers the admin role", func(t *testing.T) {
		t.Parallel()
		admin := f.deployer
		inv := stubTarConfig(t, stubTarOwner(operationstest.NewRecordingInvoker(), f, f.deployer), f, tarbindings.TokenConfig{Administrator: &admin})
		_, err := registerTokenInTAR(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, tarDS(t, f), 42, f.tar, f.token, f.pool, f.timelock, true)
		require.NoError(t, err)
		transfers := recordsFor(inv, "transfer_admin_role")
		require.Len(t, transfers, 1)
		newAdmin, err := scval.OptionalAddressFromScVal(transfers[0].Args[1])
		require.NoError(t, err)
		require.NotNil(t, newAdmin)
		require.Equal(t, f.timelock, *newAdmin)
		require.Empty(t, recordsFor(inv, "propose_administrator"))
	})

	t.Run("timelock-pending admin accepts via MCMS proposal", func(t *testing.T) {
		t.Parallel()
		ds := datastore.NewMemoryDataStore()
		require.NoError(t, stellarccip.TokenAdminRegistryDatastoreRef().UpsertDeployedStrKey(ds, 42, f.tar))
		require.NoError(t, ds.Addresses().Upsert(datastore.AddressRef{
			ChainSelector: 42,
			Type:          datastore.ContractType(cciputils.RBACTimelock),
			Version:       deploy.MCMSVersion,
			Qualifier:     cciputils.CLLQualifier,
			Address:       f.timelock,
		}))
		// A timelock-owned TAR: both the propose (caller/authorizer = the
		// timelock owner) and the accept (pending admin = the timelock) route
		// to MCMS proposals instead of direct invokes.
		inv := stubTarConfig(t, stubTarOwner(operationstest.NewRecordingInvoker(), f, f.timelock), f, tarbindings.TokenConfig{})
		out, err := registerTokenInTAR(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, ds.Seal(), 42, f.tar, f.token, f.pool, "", false)
		require.NoError(t, err)
		require.Empty(t, recordsFor(inv, "accept_admin_role"), "the timelock accepts via proposal, not a direct invoke")
		require.Empty(t, recordsFor(inv, "propose_administrator"))
		require.Len(t, out.BatchOps, 1)
		fns := map[string]bool{}
		for _, tx := range out.BatchOps[0].Transactions {
			fn, _, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
			require.NoError(t, err)
			fns[fn] = true
		}
		require.True(t, fns["propose_administrator"] && fns["accept_admin_role"],
			"a timelock-owned TAR proposes and accepts via MCMS, got %v", fns)
	})

	t.Run("sequence errors on missing chain", func(t *testing.T) {
		t.Parallel()
		a := &StellarTokenAdapter{}
		_, err := cldf_ops.ExecuteSequence(operationstest.NewBundle(t), a.ManualRegistration(), cldf_chain.NewBlockChains(nil), newInput(f.deployer))
		require.ErrorContains(t, err, "stellar chain 42 not found")
	})
}

func TestStellarTokenAdapter_ConfigureTokenForTransfers(t *testing.T) {
	t.Parallel()
	f := newConfigureFixtures(t)

	newInput := func(mutate func(*tokens.ConfigureTokenForTransfersInput)) tokens.ConfigureTokenForTransfersInput {
		input := tokens.ConfigureTokenForTransfersInput{
			ChainSelector:     42,
			TokenPoolAddress:  f.poolHex,
			TokenAddress:      f.tokenHex,
			PoolType:          bmpops.ContractType,
			TokenRef:          datastore.AddressRef{Address: f.tokenHex},
			ExistingDataStore: tarDS(t, f),
			RemoteChains: map[uint64]tokens.RemoteChainConfig[[]byte, string]{
				7: {
					RemotePool:     f.evmRemote,
					RemoteToken:    f.evmRemoteToken,
					RemoteDecimals: 18,
				},
			},
		}
		if mutate != nil {
			mutate(&input)
		}
		return input
	}

	t.Run("new lane writes the padded chain update and registers the token", func(t *testing.T) {
		t.Parallel()
		inv := stubTarConfig(t, stubTarOwner(stubPoolOwner(operationstest.NewRecordingInvoker(), f, f.deployer), f, f.deployer), f, tarbindings.TokenConfig{})
		unsupported := scval.BoolToScVal(false)
		inv = inv.WithSimulateResultForContract(f.pool, "is_supported_chain", &unsupported)
		decimals := scval.Uint32ToScVal(7)
		inv = inv.WithSimulateResultForContract(f.pool, "get_token_decimals", &decimals)
		out, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, newInput(nil))
		require.NoError(t, err)

		applies := recordsFor(inv, "apply_chain_updates")
		require.Len(t, applies, 1)
		wantUpdate := poolChainUpdate(7, newInput(nil).RemoteChains[7], tokens.TPRLRateLimitBucket{})
		require.Equal(t, scval.StructSliceToScVal([]tpoolbindings.ChainUpdate{wantUpdate}), applies[0].Args[0])
		// Disabled rate limits on a fresh lane: no separate set_rate_limit_config.
		require.Empty(t, recordsFor(inv, "set_rate_limit_config"))
		// TAR registration ran (propose → accept → set_pool).
		require.Len(t, recordsFor(inv, "propose_administrator"), 1)
		require.Len(t, recordsFor(inv, "set_pool"), 1)
		// Direct executions: pool router + TAR router markers, both empty.
		require.Len(t, out.BatchOps, 2)
		for _, op := range out.BatchOps {
			require.Empty(t, op.Transactions)
		}
	})

	t.Run("already configured lane is a no-op", func(t *testing.T) {
		t.Parallel()
		inv := stubPoolReads(t, operationstest.NewRecordingInvoker(), f)
		admin := f.deployer
		pool := f.pool
		inv = stubTarConfig(t, inv, f, tarbindings.TokenConfig{Administrator: &admin, TokenPool: &pool})
		supported := scval.BoolToScVal(true)
		inv = inv.WithSimulateResultForContract(f.pool, "is_supported_chain", &supported)
		remoteToken := scval.BytesToScVal(common.LeftPadBytes(f.evmRemoteToken, 32))
		inv = inv.WithSimulateResultForContract(f.pool, "get_remote_token", &remoteToken)
		remotePools := scval.VecToScVal([]xdr.ScVal{scval.BytesToScVal(common.LeftPadBytes(f.evmRemote, 32))})
		inv = inv.WithSimulateResultForContract(f.pool, "get_remote_pools", &remotePools)
		out, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, newInput(nil))
		require.NoError(t, err)
		for _, r := range inv.Records() {
			require.NotContains(t, []string{"apply_chain_updates", "add_remote_pool", "set_pool", "propose_administrator"}, r.Fn,
				"an already-configured lane and registered token must not rewrite")
		}
		require.Empty(t, out.BatchOps)
	})

	t.Run("missing remote pool is added", func(t *testing.T) {
		t.Parallel()
		inv := stubPoolReads(t, operationstest.NewRecordingInvoker(), f)
		supported := scval.BoolToScVal(true)
		inv = inv.WithSimulateResultForContract(f.pool, "is_supported_chain", &supported)
		remoteToken := scval.BytesToScVal(common.LeftPadBytes(f.evmRemoteToken, 32))
		inv = inv.WithSimulateResultForContract(f.pool, "get_remote_token", &remoteToken)
		empty := scval.VecToScVal(nil)
		inv = inv.WithSimulateResultForContract(f.pool, "get_remote_pools", &empty)
		_, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, newInput(nil))
		require.NoError(t, err)
		adds := recordsFor(inv, "add_remote_pool")
		require.Len(t, adds, 1)
		got, ok := adds[0].Args[1].GetBytes()
		require.True(t, ok)
		require.Equal(t, xdr.ScBytes(common.LeftPadBytes(f.evmRemote, 32)), got)
	})

	t.Run("changed remote token re-adds the lane", func(t *testing.T) {
		t.Parallel()
		inv := stubPoolReads(t, operationstest.NewRecordingInvoker(), f)
		supported := scval.BoolToScVal(true)
		inv = inv.WithSimulateResultForContract(f.pool, "is_supported_chain", &supported)
		stale := scval.BytesToScVal(common.LeftPadBytes([]byte{0x99, 0x99}, 32))
		inv = inv.WithSimulateResultForContract(f.pool, "get_remote_token", &stale)
		_, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, newInput(nil))
		require.NoError(t, err)
		applies := recordsFor(inv, "apply_chain_updates")
		require.Len(t, applies, 2, "remove then re-add")
	})

	t.Run("timelock-owned pool goes through proposals", func(t *testing.T) {
		t.Parallel()
		ds := datastore.NewMemoryDataStore()
		require.NoError(t, stellarccip.TokenAdminRegistryDatastoreRef().UpsertDeployedStrKey(ds, 42, f.tar))
		require.NoError(t, ds.Addresses().Upsert(datastore.AddressRef{
			ChainSelector: 42,
			Type:          datastore.ContractType(cciputils.RBACTimelock),
			Version:       deploy.MCMSVersion,
			Qualifier:     cciputils.CLLQualifier,
			Address:       f.timelock,
		}))
		inv := stubTarConfig(t, stubTarOwner(stubPoolOwner(operationstest.NewRecordingInvoker(), f, f.timelock), f, f.timelock), f, tarbindings.TokenConfig{})
		unsupported := scval.BoolToScVal(false)
		inv = inv.WithSimulateResultForContract(f.pool, "is_supported_chain", &unsupported)
		decimals := scval.Uint32ToScVal(7)
		inv = inv.WithSimulateResultForContract(f.pool, "get_token_decimals", &decimals)
		out, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, newInput(func(in *tokens.ConfigureTokenForTransfersInput) {
			in.ExistingDataStore = ds.Seal()
		}))
		require.NoError(t, err)
		require.Empty(t, recordsFor(inv, "apply_chain_updates"), "no direct invoke on a timelock-owned pool")
		var found bool
		for _, op := range out.BatchOps {
			for _, tx := range op.Transactions {
				fn, _, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
				require.NoError(t, err)
				if fn == "apply_chain_updates" {
					found = true
				}
			}
		}
		require.True(t, found, "an apply_chain_updates proposal must be emitted")
	})

	t.Run("input rate limits are applied", func(t *testing.T) {
		t.Parallel()
		input := newInput(func(in *tokens.ConfigureTokenForTransfersInput) {
			rc := in.RemoteChains[7]
			rc.OutboundRateLimiterConfig = &tokens.RateLimiterConfigFloatInput{IsEnabled: true, Capacity: 1000, Rate: 10}
			rc.InboundRateLimiterConfig = &tokens.RateLimiterConfigFloatInput{IsEnabled: true, Capacity: 2000, Rate: 20}
			in.RemoteChains[7] = rc
		})
		inv := stubPoolReads(t, operationstest.NewRecordingInvoker(), f)
		supported := scval.BoolToScVal(true)
		inv = inv.WithSimulateResultForContract(f.pool, "is_supported_chain", &supported)
		remoteToken := scval.BytesToScVal(common.LeftPadBytes(f.evmRemoteToken, 32))
		inv = inv.WithSimulateResultForContract(f.pool, "get_remote_token", &remoteToken)
		remotePools := scval.VecToScVal([]xdr.ScVal{scval.BytesToScVal(common.LeftPadBytes(f.evmRemote, 32))})
		inv = inv.WithSimulateResultForContract(f.pool, "get_remote_pools", &remotePools)
		_, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
		require.NoError(t, err)
		require.Len(t, recordsFor(inv, "set_rate_limit_config"), 1, "the default bucket applies via the rate-limit core")
	})

	t.Run("unsupported inputs fail loudly", func(t *testing.T) {
		t.Parallel()
		inv := stubPoolOwner(operationstest.NewRecordingInvoker(), f, f.deployer)

		t.Run("CCV lists", func(t *testing.T) {
			t.Parallel()
			input := newInput(func(in *tokens.ConfigureTokenForTransfersInput) {
				rc := in.RemoteChains[7]
				rc.InboundCCVs = []string{"0x1"}
				in.RemoteChains[7] = rc
			})
			_, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
			require.ErrorContains(t, err, "CCV")
		})

		t.Run("pool-level token transfer fee config", func(t *testing.T) {
			t.Parallel()
			input := newInput(func(in *tokens.ConfigureTokenForTransfersInput) {
				rc := in.RemoteChains[7]
				rc.TokenTransferFeeConfig = &tokens.PartialTokenTransferFeeConfig{}
				in.RemoteChains[7] = rc
			})
			_, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
			require.ErrorContains(t, err, "token transfer fee")
		})

		t.Run("unsupported pool type", func(t *testing.T) {
			t.Parallel()
			input := newInput(func(in *tokens.ConfigureTokenForTransfersInput) {
				in.PoolType = "SomeOtherPool"
			})
			_, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
			require.ErrorContains(t, err, "unsupported pool type")
		})
	})

	t.Run("sequence errors on missing chain", func(t *testing.T) {
		t.Parallel()
		a := &StellarTokenAdapter{}
		_, err := cldf_ops.ExecuteSequence(operationstest.NewBundle(t), a.ConfigureTokenForTransfersSequence(), cldf_chain.NewBlockChains(nil), newInput(nil))
		require.ErrorContains(t, err, "stellar chain 42 not found")
	})
}

func TestResolveRemoteRateLimitBuckets(t *testing.T) {
	t.Parallel()
	f := newConfigureFixtures(t)
	input := tokens.ConfigureTokenForTransfersInput{PoolType: bmpops.ContractType}

	t.Run("no input: disabled default, no fast-finality", func(t *testing.T) {
		t.Parallel()
		buckets, hasInput, err := resolveRemoteRateLimitBuckets(input, tokens.RemoteChainConfig[[]byte, string]{}, 7)
		require.NoError(t, err)
		require.False(t, hasInput)
		require.Len(t, buckets, 1)
		require.False(t, buckets[0].OutboundRateLimiterConfig.IsEnabled)
		_ = f
	})

	t.Run("paired default buckets scale to token amounts", func(t *testing.T) {
		t.Parallel()
		rc := tokens.RemoteChainConfig[[]byte, string]{
			RemoteDecimals:            18,
			OutboundRateLimiterConfig: &tokens.RateLimiterConfigFloatInput{IsEnabled: true, Capacity: 1000, Rate: 10},
			InboundRateLimiterConfig:  &tokens.RateLimiterConfigFloatInput{IsEnabled: true, Capacity: 1000, Rate: 10},
		}
		buckets, hasInput, err := resolveRemoteRateLimitBuckets(input, rc, 7)
		require.NoError(t, err)
		require.True(t, hasInput)
		require.True(t, buckets[0].OutboundRateLimiterConfig.IsEnabled)
		require.True(t, buckets[0].OutboundRateLimiterConfig.Capacity.Cmp(big.NewInt(0)) > 0)
	})

	t.Run("unpaired buckets error", func(t *testing.T) {
		t.Parallel()
		rc := tokens.RemoteChainConfig[[]byte, string]{
			OutboundRateLimiterConfig: &tokens.RateLimiterConfigFloatInput{IsEnabled: true, Capacity: 1, Rate: 1},
		}
		_, _, err := resolveRemoteRateLimitBuckets(input, rc, 7)
		require.ErrorContains(t, err, "both be specified together")
	})
}
