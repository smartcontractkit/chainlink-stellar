package adapters

import (
	"context"
	"math"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/Masterminds/semver/v3"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	slrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/siloed_lock_release_pool"
	tpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	bmpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// poolOpsFixtures holds the identities one pool-ops test needs.
type poolOpsFixtures struct {
	pool     string // pool contract strkey
	poolHex  string // the same pool as the 0x-hex the datastore records
	deployer string
	timelock string
	third    string
}

func newPoolOpsFixtures(t *testing.T) poolOpsFixtures {
	t.Helper()
	pool := stellarutil.MustGenerateMockContractID("deployer", "pool-ops-test-pool")
	poolHex, err := stellarutil.StrkeyToHex(pool)
	require.NoError(t, err)
	return poolOpsFixtures{
		pool:     pool,
		poolHex:  poolHex,
		deployer: stellarutil.MustGenerateMockContractID("deployer", "pool-ops-deployer"),
		timelock: stellarutil.MustGenerateMockContractID("deployer", "pool-ops-timelock"),
		third:    stellarutil.MustGenerateMockContractID("deployer", "pool-ops-third-party"),
	}
}

// timelockDS returns a datastore holding the CLL-qualifier RBACTimelock ref.
func timelockDS(t *testing.T, f poolOpsFixtures) datastore.DataStore {
	t.Helper()
	ds := datastore.NewMemoryDataStore()
	require.NoError(t, ds.Addresses().Upsert(datastore.AddressRef{
		ChainSelector: 42,
		Type:          datastore.ContractType(cciputils.RBACTimelock),
		Version:       deploy.MCMSVersion,
		Qualifier:     cciputils.CLLQualifier,
		Address:       f.timelock,
	}))
	return ds.Seal()
}

func poolRateLimitInput(t *testing.T, f poolOpsFixtures, ds datastore.DataStore) tokens.TPRLRemotes {
	t.Helper()
	return tokens.TPRLRemotes{
		ChainSelector:       42,
		RemoteChainSelector: 7,
		TokenPoolRef: datastore.AddressRef{
			ChainSelector: 42,
			Type:          datastore.ContractType(bmpops.ContractType),
			Version:       semver.MustParse("2.0.0"),
			Qualifier:     "pool-ops-test",
			Address:       f.poolHex,
		},
		ExistingDataStore: ds,
		RateLimitBuckets: []tokens.TPRLRateLimitBucket{
			{
				FastFinality: false,
				OutboundRateLimiterConfig: tokens.RateLimiterConfig{
					IsEnabled: true, Capacity: big.NewInt(1_000), Rate: big.NewInt(10),
				},
				InboundRateLimiterConfig: tokens.RateLimiterConfig{
					IsEnabled: true, Capacity: big.NewInt(2_000), Rate: big.NewInt(20),
				},
			},
			{
				FastFinality: true,
				OutboundRateLimiterConfig: tokens.RateLimiterConfig{
					IsEnabled: true, Capacity: big.NewInt(3_000), Rate: big.NewInt(30),
				},
				InboundRateLimiterConfig: tokens.RateLimiterConfig{
					IsEnabled: true, Capacity: big.NewInt(4_000), Rate: big.NewInt(40),
				},
			},
		},
	}
}

// ownerAndAdmin stubs the two identity reads for the given owner/admin; an
// empty admin stubs the on-chain unset (void) form.
func ownerAndAdmin(inv *operationstest.RecordingInvoker, f poolOpsFixtures, owner, admin string) *operationstest.RecordingInvoker {
	ownerVal := scval.AddressToScVal(owner)
	inv = inv.WithSimulateResultForContract(f.pool, "owner", &ownerVal)
	if admin == "" {
		void := scval.VoidScVal()
		return inv.WithSimulateResultForContract(f.pool, "get_rate_limit_admin", &void)
	}
	adminVal := scval.AddressToScVal(admin)
	return inv.WithSimulateResultForContract(f.pool, "get_rate_limit_admin", &adminVal)
}

func rateLimitRecords(inv *operationstest.RecordingInvoker) []operationstest.InvokeRecord {
	var out []operationstest.InvokeRecord
	for _, r := range inv.Records() {
		if r.Fn == "set_rate_limit_config" {
			out = append(out, r)
		}
	}
	return out
}

func TestStellarTokenAdapter_SetTokenPoolRateLimits(t *testing.T) {
	t.Parallel()
	f := newPoolOpsFixtures(t)

	t.Run("deployer-owned pool executes directly", func(t *testing.T) {
		t.Parallel()
		input := poolRateLimitInput(t, f, newSealedDatastore())
		inv := ownerAndAdmin(operationstest.NewRecordingInvoker(), f, f.deployer, f.deployer)
		out, err := setTokenPoolRateLimits(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, false, input)
		require.NoError(t, err)
		// One bucket per call, invoked directly with caller = deployer.
		recs := rateLimitRecords(inv)
		require.Len(t, recs, 2)
		for i, fastFinality := range []bool{false, true} {
			require.Equal(t, f.pool, recs[i].ContractID)
			caller, err := scval.AddressFromScVal(recs[i].Args[0])
			require.NoError(t, err)
			require.Equal(t, f.deployer, caller)
			remote, ok := recs[i].Args[1].GetU64()
			require.True(t, ok)
			require.Equal(t, uint64(7), uint64(remote))
			ff, ok := recs[i].Args[4].GetB()
			require.True(t, ok)
			require.Equal(t, fastFinality, ff)
			wantOut, wantErr := rateLimiterConfigToBinding(input.RateLimitBuckets[i].OutboundRateLimiterConfig)
			require.NoError(t, wantErr)
			require.Equal(t, scval.MustToScVal(wantOut.ToScVal()), recs[i].Args[2])
		}
		// Direct-exec marker: one ExecInfo-marked empty batch op.
		require.Len(t, out.BatchOps, 1)
		require.Empty(t, out.BatchOps[0].Transactions)
	})

	t.Run("timelock-owned pool gets one proposal transaction per bucket", func(t *testing.T) {
		t.Parallel()
		input := poolRateLimitInput(t, f, timelockDS(t, f))
		// Rate-limit admin unset (void): owner alone routes the proposal.
		inv := ownerAndAdmin(operationstest.NewRecordingInvoker(), f, f.timelock, "")
		out, err := setTokenPoolRateLimits(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, false, input)
		require.NoError(t, err)
		require.Empty(t, rateLimitRecords(inv), "no direct invokes on a timelock-owned pool")
		require.Len(t, out.BatchOps, 1)
		require.Len(t, out.BatchOps[0].Transactions, 2)
		for i, tx := range out.BatchOps[0].Transactions {
			require.Equal(t, f.pool, tx.To)
			require.Contains(t, string(tx.AdditionalFields), `"family":"stellar"`)
			fn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
			require.NoError(t, err)
			require.Equal(t, "set_rate_limit_config", fn)
			caller, err := scval.AddressFromScVal(args[0])
			require.NoError(t, err)
			require.Equal(t, f.timelock, caller)
			remote, ok := args[1].GetU64()
			require.True(t, ok)
			require.Equal(t, uint64(7), uint64(remote))
			ff, ok := args[4].GetB()
			require.True(t, ok)
			require.Equal(t, i == 1, ff)
		}
	})

	t.Run("allowed finality applied idempotently", func(t *testing.T) {
		t.Parallel()
		input := poolRateLimitInput(t, f, newSealedDatastore())
		input.AllowedFinalityConfig = finality.Config{WaitForSafe: true}
		want := uint32(1 << 16)

		t.Run("differs: set", func(t *testing.T) {
			t.Parallel()
			inv := ownerAndAdmin(operationstest.NewRecordingInvoker(), f, f.deployer, f.deployer)
			current := scval.Uint32ToScVal(0)
			inv = inv.WithSimulateResultForContract(f.pool, "get_allowed_finality_config", &current)
			_, err := setTokenPoolRateLimits(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, false, input)
			require.NoError(t, err)
			var setRec *operationstest.InvokeRecord
			for _, r := range inv.Records() {
				if r.Fn == "set_allowed_finality_config" {
					setRec = &r
				}
			}
			require.NotNil(t, setRec, "set_allowed_finality_config must be invoked when on-chain differs")
			v, ok := setRec.Args[0].GetU32()
			require.True(t, ok)
			require.Equal(t, want, uint32(v))
		})

		t.Run("matches: skipped", func(t *testing.T) {
			t.Parallel()
			inv := ownerAndAdmin(operationstest.NewRecordingInvoker(), f, f.deployer, f.deployer)
			current := scval.Uint32ToScVal(want)
			inv = inv.WithSimulateResultForContract(f.pool, "get_allowed_finality_config", &current)
			_, err := setTokenPoolRateLimits(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, false, input)
			require.NoError(t, err)
			for _, r := range inv.Records() {
				require.NotEqual(t, "set_allowed_finality_config", r.Fn, "matching finality must not be rewritten")
			}
		})
	})

	t.Run("third-party pool", func(t *testing.T) {
		t.Parallel()
		input := poolRateLimitInput(t, f, timelockDS(t, f))
		inv := ownerAndAdmin(operationstest.NewRecordingInvoker(), f, f.third, "")

		t.Run("skip when SkipIfMissingPermissions", func(t *testing.T) {
			t.Parallel()
			skipInput := input
			skipInput.SkipIfMissingPermissions = true
			out, err := setTokenPoolRateLimits(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, false, skipInput)
			require.NoError(t, err)
			require.Empty(t, out.BatchOps)
			require.Empty(t, rateLimitRecords(inv))
		})

		t.Run("error without skip", func(t *testing.T) {
			t.Parallel()
			_, err := setTokenPoolRateLimits(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, false, input)
			require.ErrorContains(t, err, "are neither deployer")
		})
	})

	t.Run("unsupported pool type", func(t *testing.T) {
		t.Parallel()
		input := poolRateLimitInput(t, f, newSealedDatastore())
		input.TokenPoolRef.Type = "SomeOtherPool"
		inv := ownerAndAdmin(operationstest.NewRecordingInvoker(), f, f.deployer, f.deployer)
		_, err := setTokenPoolRateLimits(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, false, input)
		require.ErrorContains(t, err, "unsupported pool type")
	})

	t.Run("finality routes by the owner while buckets route by the admin", func(t *testing.T) {
		t.Parallel()
		// set_allowed_finality_config is owner-only while the buckets accept
		// the rate-limit admin: a timelock-owned pool with a deployer
		// rate-limit admin must execute the buckets directly and queue the
		// finality change as a proposal — not attempt an unauthorized direct
		// finality write.
		input := poolRateLimitInput(t, f, timelockDS(t, f))
		input.AllowedFinalityConfig = finality.Config{WaitForSafe: true}
		inv := ownerAndAdmin(operationstest.NewRecordingInvoker(), f, f.timelock, f.deployer)
		current := scval.Uint32ToScVal(0)
		inv = inv.WithSimulateResultForContract(f.pool, "get_allowed_finality_config", &current)
		out, err := setTokenPoolRateLimits(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, false, input)
		require.NoError(t, err)
		// The buckets ran directly under the deployer rate-limit admin.
		require.Len(t, rateLimitRecords(inv), 2)
		// The finality change did NOT run directly.
		for _, r := range inv.Records() {
			require.NotEqual(t, "set_allowed_finality_config", r.Fn)
		}
		// Both results are preserved: the direct-exec marker plus a proposal
		// transaction for the owner-routed finality write.
		var marker, finalityProposal bool
		for _, op := range out.BatchOps {
			if len(op.Transactions) == 0 {
				marker = true
				continue
			}
			for _, tx := range op.Transactions {
				fn, _, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
				require.NoError(t, err)
				if fn == "set_allowed_finality_config" {
					finalityProposal = true
				}
			}
		}
		require.True(t, marker)
		require.True(t, finalityProposal)
	})

	t.Run("sequence errors on missing chain", func(t *testing.T) {
		t.Parallel()
		a := &StellarTokenAdapter{}
		input := poolRateLimitInput(t, f, newSealedDatastore())
		_, err := cldf_ops.ExecuteSequence(operationstest.NewBundle(t), a.SetTokenPoolRateLimits(), cldf_chain.NewBlockChains(nil), input)
		require.ErrorContains(t, err, "stellar chain 42 not found")
	})
}

func TestStellarTokenAdapter_UpdateAuthorities_refCollection(t *testing.T) {
	t.Parallel()
	f := newPoolOpsFixtures(t)
	aph := stellarutil.MustGenerateMockContractID("deployer", "pool-ops-aph")

	newInput := func(poolType, qualifier string) tokens.UpdateAuthoritiesInput {
		return tokens.UpdateAuthoritiesInput{
			ChainSelector: 42,
			TokenPoolRef: datastore.AddressRef{
				ChainSelector: 42,
				Type:          datastore.ContractType(poolType),
				Version:       semver.MustParse("2.0.0"),
				Qualifier:     qualifier,
				Address:       f.poolHex,
			},
		}
	}
	hooksStub := func(inv *operationstest.RecordingInvoker, hooks string) *operationstest.RecordingInvoker {
		if hooks == "" {
			void := scval.VoidScVal()
			return inv.WithSimulateResultForContract(f.pool, "get_advanced_pool_hooks", &void)
		}
		v := scval.AddressToScVal(hooks)
		return inv.WithSimulateResultForContract(f.pool, "get_advanced_pool_hooks", &v)
	}

	t.Run("burn-mint pool only", func(t *testing.T) {
		t.Parallel()
		inv := hooksStub(operationstest.NewRecordingInvoker(), "")
		refs, err := collectPoolOwnershipRefs(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, newSealedDatastore(), newInput(bmpops.ContractType, "bm"))
		require.NoError(t, err)
		require.Len(t, refs, 1)
	})

	t.Run("burn-mint pool with AdvancedPoolHooks", func(t *testing.T) {
		t.Parallel()
		inv := hooksStub(operationstest.NewRecordingInvoker(), aph)
		refs, err := collectPoolOwnershipRefs(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, newSealedDatastore(), newInput(bmpops.ContractType, "bm"))
		require.NoError(t, err)
		require.Len(t, refs, 2)
		require.Equal(t, "AdvancedPoolHooks", string(refs[1].Type))
		require.Equal(t, aph, refs[1].Address)
	})

	t.Run("lock-release pool adds the matching-qualifier lockbox", func(t *testing.T) {
		t.Parallel()
		lockbox := stellarutil.MustGenerateMockContractID("deployer", "pool-ops-lockbox")
		ds := datastore.NewMemoryDataStore()
		require.NoError(t, stellarccip.TokenLockBoxDatastoreRef("lr").UpsertDeployedStrKey(ds, 42, lockbox))
		inv := hooksStub(operationstest.NewRecordingInvoker(), "")
		refs, err := collectPoolOwnershipRefs(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, ds.Seal(), newInput("LockReleasePool", "lr"))
		require.NoError(t, err)
		require.Len(t, refs, 2)
		require.Equal(t, "TokenLockBox", string(refs[1].Type))
	})

	t.Run("lock-release pool without a lockbox errors", func(t *testing.T) {
		t.Parallel()
		inv := hooksStub(operationstest.NewRecordingInvoker(), "")
		_, err := collectPoolOwnershipRefs(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, newSealedDatastore(), newInput("LockReleasePool", "lr"))
		require.ErrorContains(t, err, "find TokenLockBox")
	})

	t.Run("siloed pool adds every distinct lockbox", func(t *testing.T) {
		t.Parallel()
		lbA := stellarutil.MustGenerateMockContractID("deployer", "pool-ops-lockbox-a")
		lbB := stellarutil.MustGenerateMockContractID("deployer", "pool-ops-lockbox-b")
		entries := []slrbindings.LockBoxEntry{
			{LockBox: lbA, RemoteChainSelector: 7},
			{LockBox: lbA, RemoteChainSelector: 8}, // duplicate lockbox, deduped
			{LockBox: lbB, RemoteChainSelector: 9},
		}
		vals := make([]xdr.ScVal, 0, len(entries))
		for _, e := range entries {
			vals = append(vals, scval.MustToScVal(e.ToScVal()))
		}
		vec := scval.VecToScVal(vals)
		inv := hooksStub(operationstest.NewRecordingInvoker().WithSimulateResultForContract(f.pool, "get_all_lock_box_configs", &vec), "")
		refs, err := collectPoolOwnershipRefs(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, newSealedDatastore(), newInput("SiloedLockReleasePool", "slr"))
		require.NoError(t, err)
		require.Len(t, refs, 3) // pool + two distinct lockboxes
		require.Equal(t, "TokenLockBox", string(refs[1].Type))
		require.Equal(t, "TokenLockBox", string(refs[2].Type))
	})

	t.Run("siloed pool with no lockboxes errors", func(t *testing.T) {
		t.Parallel()
		empty := scval.VecToScVal(nil)
		inv := hooksStub(operationstest.NewRecordingInvoker().WithSimulateResultForContract(f.pool, "get_all_lock_box_configs", &empty), "")
		_, err := collectPoolOwnershipRefs(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, newSealedDatastore(), newInput("SiloedLockReleasePool", "slr"))
		require.ErrorContains(t, err, "no lockboxes configured")
	})
}

func TestStellarTokenAdapter_UpdateAuthorities_errorPaths(t *testing.T) {
	t.Parallel()
	a := &StellarTokenAdapter{}

	t.Run("missing chain", func(t *testing.T) {
		t.Parallel()
		e := &cldf.Environment{BlockChains: cldf_chain.NewBlockChains(nil)}
		_, err := cldf_ops.ExecuteSequence(operationstest.NewBundle(t), a.UpdateAuthorities(), e, tokens.UpdateAuthoritiesInput{ChainSelector: 42})
		require.ErrorContains(t, err, "stellar chain 42 not found")
	})
}

func TestRateLimiterConfigToBinding(t *testing.T) {
	t.Parallel()
	cfg, err := rateLimiterConfigToBinding(tokens.RateLimiterConfig{})
	require.NoError(t, err)
	require.Equal(t, tpoolbindings.RateLimitConfig{}, cfg)

	cfg, err = rateLimiterConfigToBinding(tokens.RateLimiterConfig{IsEnabled: true, Capacity: big.NewInt(1), Rate: big.NewInt(2)})
	require.NoError(t, err)
	require.True(t, cfg.IsEnabled)
	require.Equal(t, xdr.Uint64(1), cfg.Capacity.Lo)
	require.Equal(t, xdr.Uint64(0), cfg.Capacity.Hi)
	require.Equal(t, xdr.Uint64(2), cfg.Rate.Lo)

	// The 2^64 boundary splits across hi/lo.
	edge := new(big.Int).Lsh(big.NewInt(1), 64)
	cfg, err = rateLimiterConfigToBinding(tokens.RateLimiterConfig{Capacity: edge})
	require.NoError(t, err)
	require.Equal(t, xdr.Uint64(1), cfg.Capacity.Hi)
	require.Equal(t, xdr.Uint64(0), cfg.Capacity.Lo)

	// The U128 upper bound fits without truncation.
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
	cfg, err = rateLimiterConfigToBinding(tokens.RateLimiterConfig{Capacity: max})
	require.NoError(t, err)
	require.Equal(t, xdr.Uint64(math.MaxUint64), cfg.Capacity.Lo)
	require.Equal(t, xdr.Uint64(math.MaxUint64), cfg.Capacity.Hi)

	cfg, err = rateLimiterConfigToBinding(tokens.RateLimiterConfig{Capacity: nil})
	require.NoError(t, err)
	require.Equal(t, scval.U128{}, cfg.Capacity)

	// Negative amounts are rejected, not coerced into unsigned garbage.
	_, err = rateLimiterConfigToBinding(tokens.RateLimiterConfig{Capacity: big.NewInt(-1)})
	require.ErrorContains(t, err, "negative amount")

	// Amounts wider than 128 bits are rejected, not truncated: 2^128+1000
	// must not silently apply as 1000.
	over := new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1000))
	_, err = rateLimiterConfigToBinding(tokens.RateLimiterConfig{Rate: over})
	require.ErrorContains(t, err, "exceeds the 128-bit limit")
}

func TestCllTimelockStrkey(t *testing.T) {
	t.Parallel()
	f := newPoolOpsFixtures(t)

	tl, ok, err := cllTimelockStrkey(newSealedDatastore(), 42)
	require.NoError(t, err)
	require.False(t, ok, "no timelock on an empty datastore")

	tl, ok, err = cllTimelockStrkey(timelockDS(t, f), 42)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, f.timelock, tl)

	// A different qualifier must not resolve as the CLL timelock.
	ds := datastore.NewMemoryDataStore()
	require.NoError(t, ds.Addresses().Upsert(datastore.AddressRef{
		ChainSelector: 42,
		Type:          datastore.ContractType(cciputils.RBACTimelock),
		Version:       deploy.MCMSVersion,
		Qualifier:     cciputils.RMNTimelockQualifier,
		Address:       f.timelock,
	}))
	tl, ok, err = cllTimelockStrkey(ds.Seal(), 42)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestStellarTokenAdapter_IsPoolExternallyOwned(t *testing.T) {
	t.Parallel()
	f := newPoolOpsFixtures(t)
	poolRef := datastore.AddressRef{
		ChainSelector: 42,
		Type:          datastore.ContractType(bmpops.ContractType),
		Address:       f.poolHex,
	}
	ownerStub := func(owner string) *operationstest.RecordingInvoker {
		v := scval.AddressToScVal(owner)
		return operationstest.NewRecordingInvoker().WithSimulateResultForContract(f.pool, "owner", &v)
	}

	t.Run("deployer-owned pool", func(t *testing.T) {
		t.Parallel()
		externallyOwned, err := isPoolExternallyOwned(context.Background(), stellardeps.StellarDeps{Invoker: ownerStub(f.deployer)}, f.deployer, timelockDS(t, f), 42, poolRef)
		require.NoError(t, err)
		require.False(t, externallyOwned)
	})

	t.Run("timelock-owned pool", func(t *testing.T) {
		t.Parallel()
		externallyOwned, err := isPoolExternallyOwned(context.Background(), stellardeps.StellarDeps{Invoker: ownerStub(f.timelock)}, f.deployer, timelockDS(t, f), 42, poolRef)
		require.NoError(t, err)
		require.False(t, externallyOwned)
	})

	t.Run("third-party pool", func(t *testing.T) {
		t.Parallel()
		externallyOwned, err := isPoolExternallyOwned(context.Background(), stellardeps.StellarDeps{Invoker: ownerStub(f.third)}, f.deployer, timelockDS(t, f), 42, poolRef)
		require.NoError(t, err)
		require.True(t, externallyOwned)
	})

	t.Run("third-party pool with no timelock on the chain", func(t *testing.T) {
		t.Parallel()
		externallyOwned, err := isPoolExternallyOwned(context.Background(), stellardeps.StellarDeps{Invoker: ownerStub(f.third)}, f.deployer, newSealedDatastore(), 42, poolRef)
		require.NoError(t, err)
		require.True(t, externallyOwned)
	})

	t.Run("deployer-owned pool with no timelock on the chain", func(t *testing.T) {
		t.Parallel()
		externallyOwned, err := isPoolExternallyOwned(context.Background(), stellardeps.StellarDeps{Invoker: ownerStub(f.deployer)}, f.deployer, newSealedDatastore(), 42, poolRef)
		require.NoError(t, err)
		require.False(t, externallyOwned)
	})

	t.Run("unset owner is fatal", func(t *testing.T) {
		t.Parallel()
		void := scval.VoidScVal()
		inv := operationstest.NewRecordingInvoker().WithSimulateResultForContract(f.pool, "owner", &void)
		_, err := isPoolExternallyOwned(context.Background(), stellardeps.StellarDeps{Invoker: inv}, f.deployer, timelockDS(t, f), 42, poolRef)
		require.ErrorContains(t, err, "no owner")
	})

	t.Run("unsupported pool type is fatal", func(t *testing.T) {
		t.Parallel()
		badType := poolRef
		badType.Type = "SomeOtherPool"
		_, err := isPoolExternallyOwned(context.Background(), stellardeps.StellarDeps{Invoker: ownerStub(f.deployer)}, f.deployer, timelockDS(t, f), 42, badType)
		require.ErrorContains(t, err, "unsupported contract type")
	})
}

func TestStellarTokenAdapter_RemoveRemotePools(t *testing.T) {
	t.Parallel()
	f := newPoolOpsFixtures(t)
	evmPool := common.HexToAddress("0x1234567890AbCdEf1234567890aBcDeF12345678")
	evmPadded := common.LeftPadBytes(evmPool.Bytes(), 32)
	stellarRemote := stellarutil.MustGenerateMockContractID("deployer", "remove-remote-pools-stellar-remote")
	stellarRemoteHex, err := stellarutil.StrkeyToHex(stellarRemote)
	require.NoError(t, err)

	newInput := func(removes ...tokens.RemotePoolToRemove) tokens.RemoveRemotePoolsSequenceInput {
		return tokens.RemoveRemotePoolsSequenceInput{
			Selector: 42,
			TokenPoolRef: datastore.AddressRef{
				ChainSelector: 42,
				Type:          datastore.ContractType(bmpops.ContractType),
				Address:       f.poolHex,
			},
			RemotePoolsToRemove: removes,
		}
	}
	remotePoolsStub := func(inv *operationstest.RecordingInvoker, stored ...[]byte) *operationstest.RecordingInvoker {
		vals := make([]xdr.ScVal, 0, len(stored))
		for _, b := range stored {
			vals = append(vals, scval.BytesToScVal(b))
		}
		vec := scval.VecToScVal(vals)
		return inv.WithSimulateResultForContract(f.pool, "get_remote_pools", &vec)
	}
	removeRecords := func(inv *operationstest.RecordingInvoker) []operationstest.InvokeRecord {
		var out []operationstest.InvokeRecord
		for _, r := range inv.Records() {
			if r.Fn == "remove_remote_pool" {
				out = append(out, r)
			}
		}
		return out
	}

	t.Run("deployer-owned pool removes the stored padded form directly", func(t *testing.T) {
		t.Parallel()
		input := newInput(tokens.RemotePoolToRemove{
			Selector: 7,
			Remote:   datastore.AddressRef{ChainSelector: 7, Address: evmPool.Hex()},
		})
		inv := remotePoolsStub(ownerAndAdmin(operationstest.NewRecordingInvoker(), f, f.deployer, ""), evmPadded)
		out, err := removeRemotePools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
		require.NoError(t, err)
		recs := removeRecords(inv)
		require.Len(t, recs, 1)
		require.Equal(t, f.pool, recs[0].ContractID)
		remote, ok := recs[0].Args[0].GetU64()
		require.True(t, ok)
		require.Equal(t, uint64(7), uint64(remote))
		stored, ok := recs[0].Args[1].GetBytes()
		require.True(t, ok)
		require.Equal(t, evmPadded, []byte(stored))
		// Direct-exec marker: one ExecInfo-marked empty batch op.
		require.Len(t, out.BatchOps, 1)
		require.Empty(t, out.BatchOps[0].Transactions)
	})

	t.Run("raw stellar remote form is removed by its exact bytes", func(t *testing.T) {
		t.Parallel()
		rawRemote, err := strkey.Decode(strkey.VersionByteContract, stellarRemote)
		require.NoError(t, err)
		input := newInput(tokens.RemotePoolToRemove{
			Selector: 7,
			Remote:   datastore.AddressRef{ChainSelector: 7, Address: stellarRemoteHex},
		})
		inv := remotePoolsStub(ownerAndAdmin(operationstest.NewRecordingInvoker(), f, f.deployer, ""), rawRemote)
		out, err := removeRemotePools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
		require.NoError(t, err)
		recs := removeRecords(inv)
		require.Len(t, recs, 1)
		stored, ok := recs[0].Args[1].GetBytes()
		require.True(t, ok)
		require.Equal(t, rawRemote, []byte(stored))
		require.Len(t, out.BatchOps, 1)
		require.Empty(t, out.BatchOps[0].Transactions)
	})

	t.Run("absent pairing is skipped with no write", func(t *testing.T) {
		t.Parallel()
		input := newInput(tokens.RemotePoolToRemove{
			Selector: 7,
			Remote:   datastore.AddressRef{ChainSelector: 7, Address: evmPool.Hex()},
		})
		inv := remotePoolsStub(ownerAndAdmin(operationstest.NewRecordingInvoker(), f, f.deployer, ""), []byte{1, 2, 3})
		out, err := removeRemotePools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
		require.NoError(t, err)
		require.Empty(t, removeRecords(inv))
		require.Empty(t, out.BatchOps)
	})

	t.Run("timelock-owned pool gets one proposal transaction per removal", func(t *testing.T) {
		t.Parallel()
		input := newInput(
			tokens.RemotePoolToRemove{Selector: 7, Remote: datastore.AddressRef{ChainSelector: 7, Address: evmPool.Hex()}},
			tokens.RemotePoolToRemove{Selector: 8, Remote: datastore.AddressRef{ChainSelector: 8, Address: stellarRemoteHex}},
		)
		inv := remotePoolsStub(ownerAndAdmin(operationstest.NewRecordingInvoker(), f, f.timelock, ""),
			evmPadded,
			common.LeftPadBytes(mustDecodeContractStrkey(t, stellarRemote), 32))
		out, err := removeRemotePools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
		require.NoError(t, err)
		require.Empty(t, removeRecords(inv), "no direct invokes on a timelock-owned pool")
		require.Len(t, out.BatchOps, 1)
		require.Len(t, out.BatchOps[0].Transactions, 2)
		want := map[uint64][]byte{
			7: evmPadded,
			8: common.LeftPadBytes(mustDecodeContractStrkey(t, stellarRemote), 32),
		}
		for _, tx := range out.BatchOps[0].Transactions {
			require.Equal(t, f.pool, tx.To)
			require.Contains(t, string(tx.AdditionalFields), `"family":"stellar"`)
			fn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
			require.NoError(t, err)
			require.Equal(t, "remove_remote_pool", fn)
			remote, ok := args[0].GetU64()
			require.True(t, ok)
			stored, ok := args[1].GetBytes()
			require.True(t, ok)
			require.Equal(t, want[uint64(remote)], []byte(stored))
		}
	})

	t.Run("duplicate requests remove the pairing once", func(t *testing.T) {
		t.Parallel()
		input := newInput(
			tokens.RemotePoolToRemove{Selector: 7, Remote: datastore.AddressRef{ChainSelector: 7, Address: evmPool.Hex()}},
			tokens.RemotePoolToRemove{Selector: 7, Remote: datastore.AddressRef{ChainSelector: 7, Address: evmPool.Hex()}},
		)
		inv := remotePoolsStub(ownerAndAdmin(operationstest.NewRecordingInvoker(), f, f.deployer, ""), evmPadded)
		_, err := removeRemotePools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
		require.NoError(t, err)
		require.Len(t, removeRecords(inv), 1)
	})

	t.Run("unsupported pool type errors", func(t *testing.T) {
		t.Parallel()
		input := newInput(tokens.RemotePoolToRemove{Selector: 7, Remote: datastore.AddressRef{ChainSelector: 7, Address: evmPool.Hex()}})
		input.TokenPoolRef.Type = "SomeOtherPool"
		inv := ownerAndAdmin(operationstest.NewRecordingInvoker(), f, f.deployer, "")
		_, err := removeRemotePools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
		require.ErrorContains(t, err, "unsupported pool type")
	})

	t.Run("invalid remote address errors", func(t *testing.T) {
		t.Parallel()
		input := newInput(tokens.RemotePoolToRemove{Selector: 7, Remote: datastore.AddressRef{ChainSelector: 7, Address: "not-an-address"}})
		inv := ownerAndAdmin(operationstest.NewRecordingInvoker(), f, f.deployer, "")
		_, err := removeRemotePools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, input)
		require.ErrorContains(t, err, "remote pool address")
	})

	t.Run("sequence errors on missing chain", func(t *testing.T) {
		t.Parallel()
		a := &StellarTokenAdapter{}
		_, err := cldf_ops.ExecuteSequence(operationstest.NewBundle(t), a.RemoveRemotePools(), cldf_chain.NewBlockChains(nil), newInput())
		require.ErrorContains(t, err, "stellar chain 42 not found")
	})
}

// mustDecodeContractStrkey decodes a contract strkey to its raw 32 bytes.
func mustDecodeContractStrkey(t *testing.T, cid string) []byte {
	t.Helper()
	raw, err := strkey.Decode(strkey.VersionByteContract, cid)
	require.NoError(t, err)
	return raw
}
