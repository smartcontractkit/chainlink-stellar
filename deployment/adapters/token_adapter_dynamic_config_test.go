package adapters

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	tpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	bmpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// dynConfigFixtures holds the identities one dynamic-config test needs.
type dynConfigFixtures struct {
	pool     string // pool contract strkey
	poolHex  string // the same pool as the 0x-hex the datastore records
	router   string
	admin    string
	feeAdmin string
	deployer string
	timelock string
	third    string
}

func newDynConfigFixtures(t *testing.T) dynConfigFixtures {
	t.Helper()
	pool := stellarutil.MustGenerateMockContractID("deployer", "dyn-config-test-pool")
	poolHex, err := stellarutil.StrkeyToHex(pool)
	require.NoError(t, err)
	return dynConfigFixtures{
		pool:     pool,
		poolHex:  poolHex,
		router:   stellarutil.MustGenerateMockContractID("deployer", "dyn-config-test-router"),
		admin:    stellarutil.MustGenerateMockContractID("deployer", "dyn-config-test-admin"),
		feeAdmin: stellarutil.MustGenerateMockContractID("deployer", "dyn-config-test-fee-admin"),
		deployer: stellarutil.MustGenerateMockContractID("deployer", "dyn-config-test-deployer"),
		timelock: stellarutil.MustGenerateMockContractID("deployer", "dyn-config-test-timelock"),
		third:    stellarutil.MustGenerateMockContractID("deployer", "dyn-config-test-third"),
	}
}

// poolDS returns a datastore holding the pool ref the Settings-key lookups
// and the pool type need.
func poolDS(t *testing.T, f dynConfigFixtures) datastore.DataStore {
	t.Helper()
	ds := datastore.NewMemoryDataStore()
	require.NoError(t, ds.Addresses().Upsert(datastore.AddressRef{
		ChainSelector: 42,
		Type:          datastore.ContractType(bmpops.ContractType),
		Version:       semver.MustParse("2.0.0"),
		Qualifier:     "dyn-config-test",
		Address:       f.poolHex,
	}))
	return ds.Seal()
}

func dynConfigPoolRef(f dynConfigFixtures) datastore.AddressRef {
	return datastore.AddressRef{
		ChainSelector: 42,
		Type:          datastore.ContractType(bmpops.ContractType),
		Version:       semver.MustParse("2.0.0"),
		Qualifier:     "dyn-config-test",
		Address:       f.poolHex,
	}
}

// dynConfigStubs stubs the identity reads for the given owner and the current
// router/admin/fee-admin values ("" stubs the on-chain unset form).
func dynConfigStubs(inv *operationstest.RecordingInvoker, f dynConfigFixtures, owner, router, admin, feeAdmin string) *operationstest.RecordingInvoker {
	ownerVal := scval.AddressToScVal(owner)
	inv = inv.WithSimulateResultForContract(f.pool, "owner", &ownerVal)
	stubOptionalAddr := func(fn, addr string) {
		if addr == "" {
			void := scval.VoidScVal()
			inv = inv.WithSimulateResultForContract(f.pool, fn, &void)
			return
		}
		v := scval.AddressToScVal(addr)
		inv = inv.WithSimulateResultForContract(f.pool, fn, &v)
	}
	stubOptionalAddr("get_router", router)
	stubOptionalAddr("get_rate_limit_admin", admin)
	stubOptionalAddr("get_fee_admin", feeAdmin)
	return inv
}

// invokeRecordsFor returns the recorded InvokeContract calls for one fn.
func invokeRecordsFor(inv *operationstest.RecordingInvoker, fn string) []operationstest.InvokeRecord {
	var out []operationstest.InvokeRecord
	for _, rec := range inv.Records() {
		if rec.Fn == fn {
			out = append(out, rec)
		}
	}
	return out
}

func dynConfigInput(f dynConfigFixtures, router, admin, feeAdmin *string) tokens.SetTokenPoolDynamicConfigSequenceInput {
	return tokens.SetTokenPoolDynamicConfigSequenceInput{
		Selector:       42,
		Router:         router,
		RateLimitAdmin: admin,
		FeeAdmin:       feeAdmin,
		TokenPoolRef:   dynConfigPoolRef(f),
	}
}

// cllTimelockDS returns a datastore holding the pool ref and the CLL-qualifier
// RBACTimelock ref.
func cllTimelockDS(t *testing.T, f dynConfigFixtures) datastore.DataStore {
	t.Helper()
	ds := datastore.NewMemoryDataStore()
	require.NoError(t, ds.Addresses().Upsert(dynConfigPoolRef(f)))
	require.NoError(t, ds.Addresses().Upsert(mcmsutil.StellarTimelockDatastoreRef(42, cciputils.CLLQualifier, f.timelock)))
	return ds.Seal()
}

func TestSetTokenPoolDynamicConfig_DirectExec(t *testing.T) {
	t.Parallel()
	f := newDynConfigFixtures(t)
	oldRouter := stellarutil.MustGenerateMockContractID("deployer", "dyn-config-test-old-router")
	inv := dynConfigStubs(operationstest.NewRecordingInvoker(), f, f.deployer, oldRouter, "", "")

	router, admin, feeAdmin := f.router, f.admin, f.feeAdmin
	res, err := setTokenPoolDynamicConfig(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, dynConfigInput(f, &router, &admin, &feeAdmin))
	require.NoError(t, err)
	// Direct-exec marker only, with zero transactions.
	require.Len(t, res.BatchOps, 1)
	require.Empty(t, res.BatchOps[0].Transactions)

	for fn, want := range map[string]string{
		"set_router":           f.router,
		"set_rate_limit_admin": f.admin,
		"set_fee_admin":        f.feeAdmin,
	} {
		records := invokeRecordsFor(inv, fn)
		require.Len(t, records, 1, fn)
		require.Equal(t, f.pool, records[0].ContractID)
		require.Len(t, records[0].Args, 1)
		require.Equal(t, scval.AddressToScVal(want), records[0].Args[0])
	}
}

func TestSetTokenPoolDynamicConfig_SkipsUnchangedFields(t *testing.T) {
	t.Parallel()
	f := newDynConfigFixtures(t)
	// Current values equal the desired ones: nothing to write.
	inv := dynConfigStubs(operationstest.NewRecordingInvoker(), f, f.deployer, f.router, f.admin, f.feeAdmin)
	router, admin, feeAdmin := f.router, f.admin, f.feeAdmin
	res, err := setTokenPoolDynamicConfig(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, dynConfigInput(f, &router, &admin, &feeAdmin))
	require.NoError(t, err)
	require.Empty(t, res.BatchOps)
	require.Empty(t, invokeRecordsFor(inv, "set_router"))
	require.Empty(t, invokeRecordsFor(inv, "set_rate_limit_admin"))
	require.Empty(t, invokeRecordsFor(inv, "set_fee_admin"))

	// An unset current admin changes when any address is supplied (nil current
	// != any desired value).
	inv2 := dynConfigStubs(operationstest.NewRecordingInvoker(), f, f.deployer, f.router, "", "")
	admin2 := f.admin
	res, err = setTokenPoolDynamicConfig(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv2}, f.deployer, dynConfigInput(f, nil, &admin2, nil))
	require.NoError(t, err)
	require.Len(t, res.BatchOps, 1)
	require.Len(t, invokeRecordsFor(inv2, "set_rate_limit_admin"), 1)
	require.Empty(t, invokeRecordsFor(inv2, "set_router"))
	require.Empty(t, invokeRecordsFor(inv2, "set_fee_admin"))
}

func TestSetTokenPoolDynamicConfig_TimelockProposal(t *testing.T) {
	t.Parallel()
	f := newDynConfigFixtures(t)
	// Any non-deployer owner routes to a proposal; the sequence input carries
	// no datastore, so no timelock verification happens here.
	oldRouter := stellarutil.MustGenerateMockContractID("deployer", "dyn-config-test-old-router")
	inv := dynConfigStubs(operationstest.NewRecordingInvoker(), f, f.timelock, oldRouter, "", "")
	router := f.router
	res, err := setTokenPoolDynamicConfig(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, dynConfigInput(f, &router, nil, nil))
	require.NoError(t, err)
	require.Len(t, res.BatchOps, 1)
	require.Len(t, res.BatchOps[0].Transactions, 1)
	tx := res.BatchOps[0].Transactions[0]
	require.Equal(t, f.pool, tx.To)
	require.Empty(t, invokeRecordsFor(inv, "set_router"), "a non-deployer-owned pool must not be written directly")

	fn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
	require.NoError(t, err)
	require.Equal(t, "set_router", fn)
	require.Len(t, args, 1)
	require.Equal(t, scval.AddressToScVal(f.router), args[0])
}

func TestSetTokenPoolDynamicConfig_AddressValidation(t *testing.T) {
	t.Parallel()
	f := newDynConfigFixtures(t)

	// The router must be a contract, not an account.
	gAccount := "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
	_, err := normalizePoolConfigAddress(gAccount, "router", false)
	require.ErrorContains(t, err, "an account strkey is not valid here")

	// The router must not be the zero address, in either form.
	zeroStrkey, err := strkey.Encode(strkey.VersionByteContract, make([]byte, 32))
	require.NoError(t, err)
	_, err = normalizePoolConfigAddress(zeroStrkey, "router", false)
	require.ErrorContains(t, err, "is the zero address")
	_, err = normalizePoolConfigAddress("0x"+strings.Repeat("00", 32), "router", false)
	require.ErrorContains(t, err, "is the zero address")

	_, err = normalizePoolConfigAddress("nope", "router", false)
	require.ErrorContains(t, err, "neither a strkey nor 0x-hex")

	// Admins may be accounts or contracts; hex normalizes to the strkey form.
	_, err = normalizePoolConfigAddress(gAccount, "rate limit admin", true)
	require.NoError(t, err)
	got, err := normalizePoolConfigAddress(f.poolHex, "router", false)
	require.NoError(t, err)
	require.Equal(t, f.pool, got)
}

func TestSetAllowedFinalityConfigForPools(t *testing.T) {
	t.Parallel()
	f := newDynConfigFixtures(t)
	ds := poolDS(t, f)
	cfg := finality.Config{WaitForFinality: true, WaitForSafe: true, BlockDepth: 1}
	other := finality.Config{WaitForFinality: false, WaitForSafe: false, BlockDepth: 2}
	want := cfg.Raw()

	stubFinality := func(inv *operationstest.RecordingInvoker, current uint32) *operationstest.RecordingInvoker {
		v := scval.Uint32ToScVal(current)
		return inv.WithSimulateResultForContract(f.pool, "get_allowed_finality_config", &v)
	}

	t.Run("direct exec when current differs", func(t *testing.T) {
		t.Parallel()
		inv := stubFinality(dynConfigStubs(operationstest.NewRecordingInvoker(), f, f.deployer, "", "", ""), 0)
		res, err := setAllowedFinalityConfigForPools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, ds, tokens.SetAllowedFinalityConfigSequenceInput{
			Selector: 42,
			Settings: map[string]finality.Config{f.poolHex: cfg},
		})
		require.NoError(t, err)
		require.Len(t, res.BatchOps, 1)
		require.Empty(t, res.BatchOps[0].Transactions)
		records := invokeRecordsFor(inv, "set_allowed_finality_config")
		require.Len(t, records, 1)
		require.Equal(t, scval.Uint32ToScVal(binary.BigEndian.Uint32(want[:])), records[0].Args[0])
	})

	t.Run("skip when equal", func(t *testing.T) {
		t.Parallel()
		cfgRaw := cfg.Raw()
		inv := stubFinality(dynConfigStubs(operationstest.NewRecordingInvoker(), f, f.deployer, "", "", ""), binary.BigEndian.Uint32(cfgRaw[:]))
		res, err := setAllowedFinalityConfigForPools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, ds, tokens.SetAllowedFinalityConfigSequenceInput{
			Selector: 42,
			Settings: map[string]finality.Config{f.poolHex: cfg},
		})
		require.NoError(t, err)
		require.Empty(t, res.BatchOps)
		require.Empty(t, invokeRecordsFor(inv, "set_allowed_finality_config"))
	})

	t.Run("timelock owner proposes", func(t *testing.T) {
		t.Parallel()
		timelockDS := cllTimelockDS(t, f)
		inv := stubFinality(dynConfigStubs(operationstest.NewRecordingInvoker(), f, f.timelock, "", "", ""), 0)
		res, err := setAllowedFinalityConfigForPools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, timelockDS, tokens.SetAllowedFinalityConfigSequenceInput{
			Selector: 42,
			Settings: map[string]finality.Config{f.poolHex: cfg},
		})
		require.NoError(t, err)
		require.Len(t, res.BatchOps, 1)
		require.Len(t, res.BatchOps[0].Transactions, 1)
		require.Empty(t, invokeRecordsFor(inv, "set_allowed_finality_config"))
		fn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(res.BatchOps[0].Transactions[0].Data)
		require.NoError(t, err)
		require.Equal(t, "set_allowed_finality_config", fn)
		require.Len(t, args, 1)
	})

	t.Run("third-party owner fails closed", func(t *testing.T) {
		t.Parallel()
		inv := stubFinality(dynConfigStubs(operationstest.NewRecordingInvoker(), f, f.third, "", "", ""), 0)
		_, err := setAllowedFinalityConfigForPools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, ds, tokens.SetAllowedFinalityConfigSequenceInput{
			Selector: 42,
			Settings: map[string]finality.Config{f.poolHex: cfg},
		})
		require.ErrorContains(t, err, "is neither deployer")
	})

	t.Run("zero config skips and strkey keys resolve", func(t *testing.T) {
		t.Parallel()
		inv := dynConfigStubs(operationstest.NewRecordingInvoker(), f, f.deployer, "", "", "")
		res, err := setAllowedFinalityConfigForPools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, ds, tokens.SetAllowedFinalityConfigSequenceInput{
			Selector: 42,
			Settings: map[string]finality.Config{f.pool: finality.Config{}},
		})
		require.NoError(t, err)
		require.Empty(t, res.BatchOps)
	})

	t.Run("unknown pool address errors", func(t *testing.T) {
		t.Parallel()
		inv := dynConfigStubs(operationstest.NewRecordingInvoker(), f, f.deployer, "", "", "")
		_, err := setAllowedFinalityConfigForPools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, ds, tokens.SetAllowedFinalityConfigSequenceInput{
			Selector: 42,
			Settings: map[string]finality.Config{f.third: other},
		})
		require.ErrorContains(t, err, "no token pool ref found")
	})
}

func TestSetTokenTransferFeeForPools(t *testing.T) {
	t.Parallel()
	f := newDynConfigFixtures(t)
	ds := poolDS(t, f)
	fee := &tokens.TokenTransferFeeConfig{
		DefaultFinalityTransferFeeBps: 100,
		CustomFinalityTransferFeeBps:  150,
		DefaultFinalityFeeUSDCents:    25,
		CustomFinalityFeeUSDCents:     35,
		DestBytesOverhead:             64,
		DestGasOverhead:               50_000,
		IsEnabled:                     true,
	}

	t.Run("direct exec applies adds and disables", func(t *testing.T) {
		t.Parallel()
		inv := dynConfigStubs(operationstest.NewRecordingInvoker(), f, f.deployer, "", "", "")
		res, err := setTokenTransferFeeForPools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, ds, tokens.SetTokenTransferFeeSequenceInput{
			Selector: 42,
			Settings: map[string]map[uint64]*tokens.TokenTransferFeeConfig{
				f.poolHex: {7: fee, 8: nil},
			},
		})
		require.NoError(t, err)
		require.Len(t, res.BatchOps, 1)
		require.Empty(t, res.BatchOps[0].Transactions)

		records := invokeRecordsFor(inv, "apply_token_fee_config_updates")
		require.Len(t, records, 1)
		require.Len(t, records[0].Args, 2)
		addsVec, ok := records[0].Args[0].GetVec()
		require.True(t, ok)
		require.NotNil(t, addsVec)
		require.Len(t, *addsVec, 1)
		adds, err := tpoolbindings.TokenTransferFeeConfigArgsFromScVal((*addsVec)[0])
		require.NoError(t, err)
		require.Equal(t, uint64(7), adds.DestChainSelector)
		require.Equal(t, tokenTransferFeeConfigToBinding(*fee), adds.Config)
		disablesVec, ok := records[0].Args[1].GetVec()
		require.True(t, ok)
		require.NotNil(t, disablesVec)
		require.Len(t, *disablesVec, 1)
		u64, ok := (*disablesVec)[0].GetU64()
		require.True(t, ok)
		require.EqualValues(t, 8, u64)
	})

	t.Run("timelock owner proposes", func(t *testing.T) {
		t.Parallel()
		timelockDS := cllTimelockDS(t, f)
		inv := dynConfigStubs(operationstest.NewRecordingInvoker(), f, f.timelock, "", "", "")
		res, err := setTokenTransferFeeForPools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, timelockDS, tokens.SetTokenTransferFeeSequenceInput{
			Selector: 42,
			Settings: map[string]map[uint64]*tokens.TokenTransferFeeConfig{
				f.poolHex: {7: fee},
			},
		})
		require.NoError(t, err)
		require.Len(t, res.BatchOps, 1)
		require.Len(t, res.BatchOps[0].Transactions, 1)
		require.Empty(t, invokeRecordsFor(inv, "apply_token_fee_config_updates"))
		fn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(res.BatchOps[0].Transactions[0].Data)
		require.NoError(t, err)
		require.Equal(t, "apply_token_fee_config_updates", fn)
		require.Len(t, args, 2)
	})

	t.Run("third-party owner fails closed", func(t *testing.T) {
		t.Parallel()
		inv := dynConfigStubs(operationstest.NewRecordingInvoker(), f, f.third, "", "", "")
		_, err := setTokenTransferFeeForPools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, ds, tokens.SetTokenTransferFeeSequenceInput{
			Selector: 42,
			Settings: map[string]map[uint64]*tokens.TokenTransferFeeConfig{
				f.poolHex: {7: fee},
			},
		})
		require.ErrorContains(t, err, "is neither deployer")
	})
}

func TestGetOnchainPoolTokenTransferFeeConfig(t *testing.T) {
	t.Parallel()
	f := newDynConfigFixtures(t)
	want := tpoolbindings.TokenTransferFeeConfig{
		DestGasOverhead:            50_000,
		DestBytesOverhead:          64,
		FinalityFeeUsdCents:        25,
		FastFinalityFeeUsdCents:    35,
		FinalityTransferFeeBps:     100,
		FastFinalityTransferFeeBps: 150,
		IsEnabled:                  true,
	}
	val, err := want.ToScVal()
	require.NoError(t, err)
	inv := operationstest.NewRecordingInvoker().
		WithSimulateResultForContract(f.pool, "get_token_transfer_fee_config", &val)

	got, err := getOnchainPoolTokenTransferFeeConfig(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.pool, 42, 7)
	require.NoError(t, err)
	require.Equal(t, bindingToTokenTransferFeeConfig(want), got)

	// Unrepresentable bps values are rejected instead of truncated.
	bigBps := want
	bigBps.FinalityTransferFeeBps = 70_000
	bigVal, err := bigBps.ToScVal()
	require.NoError(t, err)
	inv2 := operationstest.NewRecordingInvoker().
		WithSimulateResultForContract(f.pool, "get_token_transfer_fee_config", &bigVal)
	_, err = getOnchainPoolTokenTransferFeeConfig(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv2}, f.pool, 42, 7)
	require.ErrorContains(t, err, "exceed the uint16 lane fields")
}

// TestStellarTokenAdapter_DynamicConfigInterfaces pins the interface
// compliance and that the globally registered TokenAdapter instance
// type-asserts to both optional interfaces, which is how ConfigureTokenPool
// discovers them.
func TestStellarTokenAdapter_DynamicConfigInterfaces(t *testing.T) {
	t.Parallel()
	var _ tokens.TokenPoolDynamicConfigAdapter = (*StellarTokenAdapter)(nil)
	var _ tokens.TokenFeeAdapter = (*StellarTokenAdapter)(nil)

	adapter, ok := tokens.GetTokenAdapterRegistry().GetTokenAdapter("stellar", semver.MustParse("2.0.0"))
	require.True(t, ok)
	_, ok = adapter.(tokens.TokenPoolDynamicConfigAdapter)
	require.True(t, ok, "registered TokenAdapter must implement TokenPoolDynamicConfigAdapter")
	_, ok = adapter.(tokens.TokenFeeAdapter)
	require.True(t, ok, "registered TokenAdapter must implement TokenFeeAdapter")

	require.NotNil(t, (&StellarTokenAdapter{}).SetTokenPoolDynamicConfig())
	require.NotNil(t, (&StellarTokenAdapter{}).SetAllowedFinalityConfig(nil))
	require.NotNil(t, (&StellarTokenAdapter{}).SetTokenTransferFee(nil))
	require.NotZero(t, (&StellarTokenAdapter{}).GetDefaultTokenTransferFeeConfig(1, 2))
}
