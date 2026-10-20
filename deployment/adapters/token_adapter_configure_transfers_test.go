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

	aphbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/advanced_pool_hooks"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	tpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	scval "github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	bmpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
	lrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/lock_release_pool"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tlbops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_lock_box"
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
	// lockBox backs the lock-release ownership-ref collection fixture.
	lockBox string
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
		lockBox:  stellarutil.MustGenerateMockContractID("deployer", "cfg-test-lock-box"),
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
	// No AdvancedPoolHooks wired by default; later stubs for the same
	// contract+fn override this one.
	voidHooks := scval.VoidScVal()
	inv = inv.WithSimulateResultForContract(f.pool, "get_advanced_pool_hooks", &voidHooks)
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

	t.Run("mixed direct exec and queued proposal are both returned", func(t *testing.T) {
		t.Parallel()
		// A deployer-owned TAR proposing the timelock as admin: the propose
		// runs directly (authorizer = the deployer registry owner) while the
		// accept queues an MCMS proposal (authorizer = the pending timelock).
		// Both must come back — losing the proposal leaves the registration
		// pending despite reported success.
		ds := datastore.NewMemoryDataStore()
		require.NoError(t, stellarccip.TokenAdminRegistryDatastoreRef().UpsertDeployedStrKey(ds, 42, f.tar))
		require.NoError(t, ds.Addresses().Upsert(datastore.AddressRef{
			ChainSelector: 42,
			Type:          datastore.ContractType(cciputils.RBACTimelock),
			Version:       deploy.MCMSVersion,
			Qualifier:     cciputils.CLLQualifier,
			Address:       f.timelock,
		}))
		inv := stubTarConfig(t, stubTarOwner(operationstest.NewRecordingInvoker(), f, f.deployer), f, tarbindings.TokenConfig{})
		out, err := registerTokenInTAR(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, ds.Seal(), 42, f.tar, f.token, f.pool, f.timelock, true)
		require.NoError(t, err)
		require.Len(t, recordsFor(inv, "propose_administrator"), 1, "the propose runs directly")
		require.Empty(t, recordsFor(inv, "accept_admin_role"), "the accept queues a proposal")

		var marker, proposal bool
		for _, op := range out.BatchOps {
			if len(op.Transactions) == 0 {
				marker = true
				continue
			}
			for _, tx := range op.Transactions {
				fn, _, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
				require.NoError(t, err)
				if fn == "accept_admin_role" {
					proposal = true
				}
			}
		}
		require.True(t, marker, "the direct-exec marker must be returned")
		require.True(t, proposal, "the queued accept proposal must be returned")
	})

	t.Run("set_pool routes by the token admin, not the registry owner", func(t *testing.T) {
		t.Parallel()
		// A timelock-owned registry whose token admin is still the deployer:
		// set_pool authorizes the token administrator, so the deployer runs it
		// directly — the registry-owner gate would skip it.
		ds := datastore.NewMemoryDataStore()
		require.NoError(t, stellarccip.TokenAdminRegistryDatastoreRef().UpsertDeployedStrKey(ds, 42, f.tar))
		require.NoError(t, ds.Addresses().Upsert(datastore.AddressRef{
			ChainSelector: 42,
			Type:          datastore.ContractType(cciputils.RBACTimelock),
			Version:       deploy.MCMSVersion,
			Qualifier:     cciputils.CLLQualifier,
			Address:       f.timelock,
		}))
		admin := f.deployer
		inv := stubTarConfig(t, stubTarOwner(operationstest.NewRecordingInvoker(), f, f.timelock), f, tarbindings.TokenConfig{Administrator: &admin})
		_, err := registerTokenInTAR(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, ds.Seal(), 42, f.tar, f.token, f.pool, "", false)
		require.NoError(t, err)
		sets := recordsFor(inv, "set_pool")
		require.Len(t, sets, 1, "the deployer token admin must set the pool despite the timelock registry owner")
	})

	t.Run("staged proposal defers deployer acceptance to a rerun", func(t *testing.T) {
		t.Parallel()
		// A timelock-owned registry registering the deployer as admin: the
		// propose queues as a proposal, so the on-chain pending does not exist
		// yet — a direct accept would fail OnlyPendingAdministrator. The
		// acceptance must not run this turn.
		ds := datastore.NewMemoryDataStore()
		require.NoError(t, stellarccip.TokenAdminRegistryDatastoreRef().UpsertDeployedStrKey(ds, 42, f.tar))
		require.NoError(t, ds.Addresses().Upsert(datastore.AddressRef{
			ChainSelector: 42,
			Type:          datastore.ContractType(cciputils.RBACTimelock),
			Version:       deploy.MCMSVersion,
			Qualifier:     cciputils.CLLQualifier,
			Address:       f.timelock,
		}))
		inv := stubTarConfig(t, stubTarOwner(operationstest.NewRecordingInvoker(), f, f.timelock), f, tarbindings.TokenConfig{})
		out, err := registerTokenInTAR(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, ds.Seal(), 42, f.tar, f.token, f.pool, f.deployer, true)
		require.NoError(t, err)
		require.Empty(t, recordsFor(inv, "accept_admin_role"), "no direct accept this turn")
		require.Empty(t, recordsFor(inv, "set_pool"))
		var proposeProposal bool
		for _, op := range out.BatchOps {
			for _, tx := range op.Transactions {
				fn, _, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
				require.NoError(t, err)
				if fn == "propose_administrator" {
					proposeProposal = true
				}
				require.NotEqual(t, "accept_admin_role", fn, "no queued accept either")
			}
		}
		require.True(t, proposeProposal, "the propose itself queues as a proposal")
	})

	t.Run("matching on-chain pending admin is reused, not re-proposed", func(t *testing.T) {
		t.Parallel()
		// Rerun after the propose executed: the on-chain pending already
		// equals the desired admin — reuse it and accept directly instead of
		// proposing again.
		pending := f.deployer
		inv := stubTarConfig(t, stubTarOwner(operationstest.NewRecordingInvoker(), f, f.deployer), f, tarbindings.TokenConfig{PendingAdministrator: &pending})
		_, err := registerTokenInTAR(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, tarDS(t, f), 42, f.tar, f.token, f.pool, f.deployer, true)
		require.NoError(t, err)
		require.Empty(t, recordsFor(inv, "propose_administrator"), "a matching pending admin must not be re-proposed")
		require.Len(t, recordsFor(inv, "accept_admin_role"), 1)
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
		voidHooks := scval.VoidScVal()
		inv = inv.WithSimulateResultForContract(f.pool, "get_advanced_pool_hooks", &voidHooks)
		out, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, newInput(nil))
		require.NoError(t, err)

		applies := recordsFor(inv, "apply_chain_updates")
		require.Len(t, applies, 1)
		wantUpdate, err := poolChainUpdate(7, newInput(nil).RemoteChains[7], tokens.TPRLRateLimitBucket{})
		require.NoError(t, err)
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

	t.Run("changed remote token replaces the lane atomically and preserves limits", func(t *testing.T) {
		t.Parallel()
		inv := stubPoolReads(t, operationstest.NewRecordingInvoker(), f)
		supported := scval.BoolToScVal(true)
		inv = inv.WithSimulateResultForContract(f.pool, "is_supported_chain", &supported)
		stale := scval.BytesToScVal(common.LeftPadBytes([]byte{0x99, 0x99}, 32))
		inv = inv.WithSimulateResultForContract(f.pool, "get_remote_token", &stale)
		// The lane's live rate-limit state (both buckets read the same stub):
		// enabled with capacity 1000 — the replacement must preserve it.
		live := tpoolbindings.RateLimiterState{
			Outbound: tpoolbindings.TokenBucket{IsEnabled: true, Capacity: scval.U128(xdr.UInt128Parts{Lo: 1000}), Rate: scval.U128(xdr.UInt128Parts{Lo: 10})},
			Inbound:  tpoolbindings.TokenBucket{IsEnabled: true, Capacity: scval.U128(xdr.UInt128Parts{Lo: 1000}), Rate: scval.U128(xdr.UInt128Parts{Lo: 10})},
		}
		liveVal, err := live.ToScVal()
		require.NoError(t, err)
		inv = inv.WithSimulateResultForContract(f.pool, "get_current_rate_limiter_state", &liveVal)
		_, err = configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, newInput(nil))
		require.NoError(t, err)

		// One atomic replace: the add and the removal in a single call,
		// with the preserved limits riding the ChainUpdate.
		applies := recordsFor(inv, "apply_chain_updates")
		require.Len(t, applies, 1, "the replacement must be one atomic call")
		want, wantErr := poolChainUpdate(7, newInput(nil).RemoteChains[7], tokens.TPRLRateLimitBucket{
			OutboundRateLimiterConfig: tokenBucketToRateLimiter(live.Outbound),
			InboundRateLimiterConfig:  tokenBucketToRateLimiter(live.Inbound),
		})
		require.NoError(t, wantErr)
		require.Equal(t, scval.StructSliceToScVal([]tpoolbindings.ChainUpdate{want}), applies[0].Args[0])
		removes, ok := applies[0].Args[1].GetVec()
		require.True(t, ok)
		require.Len(t, *removes, 1, "the removal rides the same call")
		// The live state stub answers both finality reads (the invoker keys
		// stubs by fn), so the fast-finality bucket is preserved too — via a
		// separate rate-limit call, not the ChainUpdate.
		rlCalls := recordsFor(inv, "set_rate_limit_config")
		require.Len(t, rlCalls, 1)
		ff, ok := rlCalls[0].Args[4].GetB()
		require.True(t, ok)
		require.True(t, ff)
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
		voidHooks := scval.VoidScVal()
		inv = inv.WithSimulateResultForContract(f.pool, "get_advanced_pool_hooks", &voidHooks)
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

	t.Run("fast-finality-only input does not touch the default bucket", func(t *testing.T) {
		t.Parallel()
		// An omitted default bucket must keep its on-chain config: writing a
		// synthesized disabled default would unlimit (or alter) limits the
		// caller never asked to change.
		input := newInput(func(in *tokens.ConfigureTokenForTransfersInput) {
			rc := in.RemoteChains[7]
			rc.OutboundRateLimits = []tokens.RateLimitConfig{
				{FastFinality: true, RateLimit: tokens.RateLimiterConfigFloatInput{IsEnabled: true, Capacity: 5, Rate: 1}},
			}
			rc.InboundRateLimits = []tokens.RateLimitConfig{
				{FastFinality: true, RateLimit: tokens.RateLimiterConfigFloatInput{IsEnabled: true, Capacity: 5, Rate: 1}},
			}
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
		rlCalls := recordsFor(inv, "set_rate_limit_config")
		require.Len(t, rlCalls, 1, "only the supplied fast-finality bucket is written")
		ff, ok := rlCalls[0].Args[4].GetB()
		require.True(t, ok)
		require.True(t, ff, "the single call must target the fast-finality bucket")
	})

	t.Run("registry address in the datastore hex form is normalized", func(t *testing.T) {
		t.Parallel()
		// The changeset passes RegistryAddress as the raw datastore row
		// address (0x-hex); the TAR ops need the strkey — an unnormalized id
		// fails the client's base32 contract-id decode.
		tarHex, err := stellarutil.StrkeyToHex(f.tar)
		require.NoError(t, err)
		input := newInput(func(in *tokens.ConfigureTokenForTransfersInput) {
			in.RegistryAddress = tarHex
		})
		inv := stubPoolReads(t, operationstest.NewRecordingInvoker(), f)
		unsupported := scval.BoolToScVal(false)
		inv = inv.WithSimulateResultForContract(f.pool, "is_supported_chain", &unsupported)
		_, err = configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
		require.NoError(t, err)
		configs := recordsFor(inv, "get_token_config")
		require.NotEmpty(t, configs, "the TAR read must target the strkey")
		require.Equal(t, f.tar, configs[0].ContractID)
	})

	t.Run("staged lane defers dependent rate updates after the lane proposal", func(t *testing.T) {
		t.Parallel()
		// A timelock-owned pool with a deployer rate-limit admin creating a
		// new lane with fast-finality limits: the lane write queues as a
		// proposal, so the dependent FF write must NOT run directly (the lane
		// does not exist on chain — ChainNotSupported) and must be emitted
		// after the lane proposal in batch order.
		ds := datastore.NewMemoryDataStore()
		require.NoError(t, stellarccip.TokenAdminRegistryDatastoreRef().UpsertDeployedStrKey(ds, 42, f.tar))
		require.NoError(t, ds.Addresses().Upsert(datastore.AddressRef{
			ChainSelector: 42,
			Type:          datastore.ContractType(cciputils.RBACTimelock),
			Version:       deploy.MCMSVersion,
			Qualifier:     cciputils.CLLQualifier,
			Address:       f.timelock,
		}))
		input := newInput(func(in *tokens.ConfigureTokenForTransfersInput) {
			in.ExistingDataStore = ds.Seal()
			rc := in.RemoteChains[7]
			rc.OutboundRateLimits = []tokens.RateLimitConfig{
				{FastFinality: true, RateLimit: tokens.RateLimiterConfigFloatInput{IsEnabled: true, Capacity: 5, Rate: 1}},
			}
			rc.InboundRateLimits = []tokens.RateLimitConfig{
				{FastFinality: true, RateLimit: tokens.RateLimiterConfigFloatInput{IsEnabled: true, Capacity: 5, Rate: 1}},
			}
			in.RemoteChains[7] = rc
		})
		inv := stubPoolReads(t, operationstest.NewRecordingInvoker(), f)
		// The pool owner is the timelock; the deployer holds the rate-limit
		// admin — the case where the naive direct path would run the FF
		// update before the queued lane exists.
		owner := scval.AddressToScVal(f.timelock)
		inv = inv.WithSimulateResultForContract(f.pool, "owner", &owner)
		rlAdmin := scval.AddressToScVal(f.deployer)
		inv = inv.WithSimulateResultForContract(f.pool, "get_rate_limit_admin", &rlAdmin)
		unsupported := scval.BoolToScVal(false)
		inv = inv.WithSimulateResultForContract(f.pool, "is_supported_chain", &unsupported)
		out, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
		require.NoError(t, err)
		require.Empty(t, recordsFor(inv, "set_rate_limit_config"),
			"no direct FF write while the lane is only proposed")

		var laneIdx, ffIdx = -1, -1
		for i, op := range out.BatchOps {
			for _, tx := range op.Transactions {
				fn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
				require.NoError(t, err)
				switch fn {
				case "apply_chain_updates":
					laneIdx = i
				case "set_rate_limit_config":
					caller, err := scval.AddressFromScVal(args[0])
					require.NoError(t, err)
					require.Equal(t, f.timelock, caller, "the staged FF write rides the proposal with the timelock caller")
					ffIdx = i
				}
			}
		}
		require.True(t, laneIdx >= 0, "the lane creation must be proposed")
		require.True(t, ffIdx >= 0, "the FF update must be proposed")
		require.Less(t, laneIdx, ffIdx, "the lane proposal must precede its dependent rate update")
	})

	t.Run("unsupported inputs fail loudly", func(t *testing.T) {
		t.Parallel()
		inv := stubPoolReads(t, operationstest.NewRecordingInvoker(), f)

		t.Run("CCV lists are skipped when no hooks are wired", func(t *testing.T) {
			t.Parallel()
			input := newInput(func(in *tokens.ConfigureTokenForTransfersInput) {
				rc := in.RemoteChains[7]
				rc.InboundCCVs = []string{f.timelock}
				rc.OutboundCCVs = []string{f.timelock}
				in.RemoteChains[7] = rc
			})
			// stubPoolReads stubs no-hooks-wired; complete the new-lane reads.
			skipSupported := scval.BoolToScVal(false)
			skipInv := inv.WithSimulateResultForContract(f.pool, "is_supported_chain", &skipSupported)
			_, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: skipInv}, f.deployer, input)
			require.NoError(t, err, "EVM parity: CCV lists are unused when no AdvancedPoolHooks is wired")
			require.Empty(t, recordsFor(skipInv, "apply_ccv_config_updates"))
		})

		t.Run("CCV lists apply to the wired AdvancedPoolHooks", func(t *testing.T) {
			t.Parallel()
			aph := stellarutil.MustGenerateMockContractID("deployer", "cfg-test-aph")
			hooks := scval.AddressToScVal(aph)
			inv := stubPoolReads(t, operationstest.NewRecordingInvoker(), f).
				WithSimulateResultForContract(f.pool, "get_advanced_pool_hooks", &hooks)
			// The APH owner is the deployer (direct exec) and the remote has
			// no stored config yet.
			aphOwner := scval.AddressToScVal(f.deployer)
			inv = inv.WithSimulateResultForContract(aph, "owner", &aphOwner)
			unconfigured := scval.VoidScVal()
			inv = inv.WithSimulateResultForContract(aph, "get_ccv_config", &unconfigured)
			notSupported := scval.BoolToScVal(false)
			inv = inv.WithSimulateResultForContract(f.pool, "is_supported_chain", &notSupported)
			input := newInput(func(in *tokens.ConfigureTokenForTransfersInput) {
				rc := in.RemoteChains[7]
				rc.InboundCCVs = []string{f.timelock}
				rc.OutboundCCVs = []string{f.timelock}
				in.RemoteChains[7] = rc
			})
			_, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
			require.NoError(t, err)
			applies := recordsFor(inv, "apply_ccv_config_updates")
			require.Len(t, applies, 1)
			wantArg := aphbindings.CCVConfigArg{
				RemoteChainSelector: 7,
				InboundCcvs:         []string{f.timelock},
				OutboundCcvs:        []string{f.timelock},
			}
			require.Equal(t, scval.StructSliceToScVal([]aphbindings.CCVConfigArg{wantArg}), applies[0].Args[0])
		})

		t.Run("matching stored CCV config is not rewritten", func(t *testing.T) {
			t.Parallel()
			aph := stellarutil.MustGenerateMockContractID("deployer", "cfg-test-aph")
			hooks := scval.AddressToScVal(aph)
			inv := stubPoolReads(t, operationstest.NewRecordingInvoker(), f).
				WithSimulateResultForContract(f.pool, "get_advanced_pool_hooks", &hooks)
			aphOwner := scval.AddressToScVal(f.deployer)
			inv = inv.WithSimulateResultForContract(aph, "owner", &aphOwner)
			storedVal, err := aphbindings.CCVConfig{
				InboundCcvs:  []string{f.timelock},
				OutboundCcvs: []string{f.timelock},
			}.ToScVal()
			require.NoError(t, err)
			inv = inv.WithSimulateResultForContract(aph, "get_ccv_config", &storedVal)
			notSupported := scval.BoolToScVal(false)
			inv = inv.WithSimulateResultForContract(f.pool, "is_supported_chain", &notSupported)
			input := newInput(func(in *tokens.ConfigureTokenForTransfersInput) {
				rc := in.RemoteChains[7]
				rc.InboundCCVs = []string{f.timelock}
				rc.OutboundCCVs = []string{f.timelock}
				in.RemoteChains[7] = rc
			})
			_, err = configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
			require.NoError(t, err)
			require.Empty(t, recordsFor(inv, "apply_ccv_config_updates"), "matching config must not be rewritten")
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
	input := tokens.ConfigureTokenForTransfersInput{PoolType: bmpops.ContractType}

	t.Run("no input: both buckets omitted", func(t *testing.T) {
		t.Parallel()
		defBucket, ffBucket, err := resolveRemoteRateLimitBuckets(input, tokens.RemoteChainConfig[[]byte, string]{}, 7)
		require.NoError(t, err)
		require.Nil(t, defBucket)
		require.Nil(t, ffBucket)
	})

	t.Run("paired default buckets scale to token amounts", func(t *testing.T) {
		t.Parallel()
		rc := tokens.RemoteChainConfig[[]byte, string]{
			RemoteDecimals:            18,
			OutboundRateLimiterConfig: &tokens.RateLimiterConfigFloatInput{IsEnabled: true, Capacity: 1000, Rate: 10},
			InboundRateLimiterConfig:  &tokens.RateLimiterConfigFloatInput{IsEnabled: true, Capacity: 1000, Rate: 10},
		}
		defBucket, ffBucket, err := resolveRemoteRateLimitBuckets(input, rc, 7)
		require.NoError(t, err)
		require.Nil(t, ffBucket)
		require.NotNil(t, defBucket)
		require.True(t, defBucket.OutboundRateLimiterConfig.IsEnabled)
		require.True(t, defBucket.OutboundRateLimiterConfig.Capacity.Cmp(big.NewInt(0)) > 0)
	})

	t.Run("fast-finality-only input leaves the default omitted", func(t *testing.T) {
		t.Parallel()
		// An omitted default must not synthesize a disabled bucket: on an
		// existing lane that would disable limits the caller never asked to
		// change.
		rc := tokens.RemoteChainConfig[[]byte, string]{
			RemoteDecimals: 18,
			OutboundRateLimits: []tokens.RateLimitConfig{
				{FastFinality: true, RateLimit: tokens.RateLimiterConfigFloatInput{IsEnabled: true, Capacity: 5, Rate: 1}},
			},
			InboundRateLimits: []tokens.RateLimitConfig{
				{FastFinality: true, RateLimit: tokens.RateLimiterConfigFloatInput{IsEnabled: true, Capacity: 5, Rate: 1}},
			},
		}
		defBucket, ffBucket, err := resolveRemoteRateLimitBuckets(input, rc, 7)
		require.NoError(t, err)
		require.Nil(t, defBucket, "an omitted default stays omitted")
		require.NotNil(t, ffBucket)
		require.True(t, ffBucket.FastFinality)
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

func TestStellarTokenAdapter_CatalogPoolType(t *testing.T) {
	// Regression: the shared pipeline's pool refs carry the devenv catalog
	// name "BurnMintTokenPool" while type-keyed dispatch keys on the ops name
	// "BurnMintPool" — every arm must accept the catalog spelling.
	t.Parallel()
	f := newConfigureFixtures(t)
	poolRef := datastore.AddressRef{
		ChainSelector: 42,
		Type:          datastore.ContractType(stellarccip.DevenvCatalogBurnMintTokenPoolType),
		Address:       f.poolHex,
	}

	t.Run("configure token for transfers", func(t *testing.T) {
		t.Parallel()
		inv := stubPoolReads(t, operationstest.NewRecordingInvoker(), f)
		unsupported := scval.BoolToScVal(false)
		inv = inv.WithSimulateResultForContract(f.pool, "is_supported_chain", &unsupported)
		input := tokens.ConfigureTokenForTransfersInput{
			ChainSelector:     42,
			TokenPoolAddress:  f.poolHex,
			TokenAddress:      f.tokenHex,
			PoolType:          stellarccip.DevenvCatalogBurnMintTokenPoolType,
			TokenRef:          datastore.AddressRef{Address: f.tokenHex},
			ExistingDataStore: tarDS(t, f),
			RemoteChains: map[uint64]tokens.RemoteChainConfig[[]byte, string]{
				7: {RemotePool: f.evmRemote, RemoteToken: f.evmRemoteToken, RemoteDecimals: 18},
			},
		}
		out, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
		require.NoError(t, err)
		require.Len(t, recordsFor(inv, "apply_chain_updates"), 1)
		require.NotEmpty(t, out.BatchOps)
	})

	t.Run("set token pool rate limits", func(t *testing.T) {
		t.Parallel()
		inv := stubPoolOwner(operationstest.NewRecordingInvoker(), f, f.deployer)
		cfg := tokens.RateLimiterConfig{IsEnabled: true, Capacity: big.NewInt(10), Rate: big.NewInt(1)}
		out, err := setTokenPoolRateLimits(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, false, tokens.TPRLRemotes{
			ChainSelector:       42,
			RemoteChainSelector: 7,
			TokenPoolRef:        poolRef,
			ExistingDataStore:   newSealedDatastore(),
			RateLimitBuckets:    []tokens.TPRLRateLimitBucket{{OutboundRateLimiterConfig: cfg, InboundRateLimiterConfig: cfg}},
		})
		require.NoError(t, err)
		require.Len(t, recordsFor(inv, "set_rate_limit_config"), 1)
		require.Len(t, out.BatchOps, 1)
	})

	t.Run("update authorities ref collection", func(t *testing.T) {
		t.Parallel()
		void := scval.VoidScVal()
		inv := operationstest.NewRecordingInvoker().WithSimulateResultForContract(f.pool, "get_advanced_pool_hooks", &void)
		refs, err := collectPoolOwnershipRefs(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, newSealedDatastore(), tokens.UpdateAuthoritiesInput{
			ChainSelector: 42,
			TokenPoolRef:  poolRef,
		})
		require.NoError(t, err)
		require.Len(t, refs, 1)
		require.Equal(t, bmpops.ContractType, string(refs[0].Type), "the ref must be normalized for owner dispatch")
	})

	// LockRelease regression (live Phase-5 failure 2026-10-19): the LR catalog
	// spelling "LockReleaseTokenPool" previously failed the pool-type gate in
	// configure-token-for-transfers ("supported: burn-mint, lock-release,
	// siloed") and, in ref collection, switched on the raw type so the lock
	// box ownership ref was silently dropped for catalog-typed pools.
	lrPoolRef := datastore.AddressRef{
		ChainSelector: 42,
		Type:          datastore.ContractType(stellarccip.DevenvCatalogLockReleaseTokenPoolType),
		Address:       f.poolHex,
		Qualifier:     "LR-Q",
	}

	t.Run("lock-release catalog type configures token for transfers", func(t *testing.T) {
		t.Parallel()
		inv := stubPoolReads(t, operationstest.NewRecordingInvoker(), f)
		unsupported := scval.BoolToScVal(false)
		inv = inv.WithSimulateResultForContract(f.pool, "is_supported_chain", &unsupported)
		input := tokens.ConfigureTokenForTransfersInput{
			ChainSelector:     42,
			TokenPoolAddress:  f.poolHex,
			TokenAddress:      f.tokenHex,
			PoolType:          stellarccip.DevenvCatalogLockReleaseTokenPoolType,
			TokenRef:          datastore.AddressRef{Address: f.tokenHex},
			ExistingDataStore: tarDS(t, f),
			RemoteChains: map[uint64]tokens.RemoteChainConfig[[]byte, string]{
				7: {RemotePool: f.evmRemote, RemoteToken: f.evmRemoteToken, RemoteDecimals: 18},
			},
		}
		out, err := configureTokenForTransfers(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
		require.NoError(t, err)
		require.Len(t, recordsFor(inv, "apply_chain_updates"), 1)
		require.NotEmpty(t, out.BatchOps)
	})

	t.Run("lock-release catalog type sets token pool rate limits", func(t *testing.T) {
		t.Parallel()
		inv := stubPoolOwner(operationstest.NewRecordingInvoker(), f, f.deployer)
		cfg := tokens.RateLimiterConfig{IsEnabled: true, Capacity: big.NewInt(10), Rate: big.NewInt(1)}
		out, err := setTokenPoolRateLimits(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, false, tokens.TPRLRemotes{
			ChainSelector:       42,
			RemoteChainSelector: 7,
			TokenPoolRef:        lrPoolRef,
			ExistingDataStore:   newSealedDatastore(),
			RateLimitBuckets:    []tokens.TPRLRateLimitBucket{{OutboundRateLimiterConfig: cfg, InboundRateLimiterConfig: cfg}},
		})
		require.NoError(t, err)
		require.Len(t, recordsFor(inv, "set_rate_limit_config"), 1)
		require.Len(t, out.BatchOps, 1)
	})

	t.Run("lock-release catalog type collects the lock box ownership ref", func(t *testing.T) {
		t.Parallel()
		void := scval.VoidScVal()
		inv := operationstest.NewRecordingInvoker().WithSimulateResultForContract(f.pool, "get_advanced_pool_hooks", &void)
		ds := datastore.NewMemoryDataStore()
		require.NoError(t, stellarccip.TokenLockBoxDatastoreRef(lrPoolRef.Qualifier).UpsertDeployedStrKey(ds, 42, f.lockBox))
		refs, err := collectPoolOwnershipRefs(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, ds.Seal(), tokens.UpdateAuthoritiesInput{
			ChainSelector: 42,
			TokenPoolRef:  lrPoolRef,
		})
		require.NoError(t, err)
		require.Len(t, refs, 2)
		require.Equal(t, lrops.ContractType, string(refs[0].Type), "the pool ref must be normalized for owner dispatch")
		require.Equal(t, tlbops.ContractType, string(refs[1].Type), "the matching-qualifier lock box ref must ride along")
	})
}
