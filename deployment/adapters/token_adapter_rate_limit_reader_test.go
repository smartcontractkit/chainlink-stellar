package adapters

import (
	"math/big"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	tpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// rateLimitReaderFixtures holds the identities one rate-limit read test needs.
type rateLimitReaderFixtures struct {
	pool     string
	poolHex  string
	deployer string
}

func newRateLimitReaderFixtures(t *testing.T) rateLimitReaderFixtures {
	t.Helper()
	pool := stellarutil.MustGenerateMockContractID("deployer", "rate-limit-reader-test-pool")
	poolHex, err := stellarutil.StrkeyToHex(pool)
	require.NoError(t, err)
	return rateLimitReaderFixtures{
		pool:     pool,
		poolHex:  poolHex,
		deployer: stellarutil.MustGenerateMockContractID("deployer", "rate-limit-reader-test-deployer"),
	}
}

func rateLimitReaderPoolRef(f rateLimitReaderFixtures) datastore.AddressRef {
	return datastore.AddressRef{
		ChainSelector: 42,
		Type:          "BurnMintPool",
		Version:       semver.MustParse("2.0.0"),
		Qualifier:     "rate-limit-reader-test",
		Address:       f.poolHex,
	}
}

// stubRateLimiterState stubs the get_current_rate_limiter_state read with the
// given buckets.
func stubRateLimiterState(t *testing.T, f rateLimitReaderFixtures, state tpoolbindings.RateLimiterState) *operationstest.RecordingInvoker {
	t.Helper()
	val, err := state.ToScVal()
	require.NoError(t, err)
	return operationstest.NewRecordingInvoker().
		WithSimulateResultForContract(f.pool, "get_current_rate_limiter_state", &val)
}

// readCurrentRateLimits drives the core read with the stubbed invoker.
func readCurrentRateLimits(t *testing.T, inv *operationstest.RecordingInvoker, f rateLimitReaderFixtures, fastFinality bool) (tokens.OnchainRateLimits, error) {
	t.Helper()
	return readOnchainRateLimits(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.pool, 7, fastFinality)
}

func TestReadOnchainRateLimits(t *testing.T) {
	t.Parallel()
	f := newRateLimitReaderFixtures(t)
	loOut := scval.U128(xdr.UInt128Parts{Hi: 0, Lo: 1_000})
	loIn := scval.U128(xdr.UInt128Parts{Hi: 0, Lo: 2_000})
	rateOut := scval.U128(xdr.UInt128Parts{Hi: 0, Lo: 10})
	rateIn := scval.U128(xdr.UInt128Parts{Hi: 0, Lo: 20})
	hi := scval.U128(xdr.UInt128Parts{Hi: 1, Lo: 5})
	state := tpoolbindings.RateLimiterState{
		Outbound: tpoolbindings.TokenBucket{IsEnabled: true, Capacity: loOut, Rate: rateOut},
		Inbound:  tpoolbindings.TokenBucket{IsEnabled: true, Capacity: loIn, Rate: rateIn},
	}

	t.Run("maps both buckets", func(t *testing.T) {
		t.Parallel()
		inv := stubRateLimiterState(t, f, state)
		got, err := readCurrentRateLimits(t, inv, f, false)
		require.NoError(t, err)
		require.Equal(t, tokens.RateLimiterConfig{IsEnabled: true, Capacity: big.NewInt(1_000), Rate: big.NewInt(10)}, got.Outbound)
		require.Equal(t, tokens.RateLimiterConfig{IsEnabled: true, Capacity: big.NewInt(2_000), Rate: big.NewInt(20)}, got.Inbound)

		records := inv.Records()
		require.Len(t, records, 1)
		require.Equal(t, f.pool, records[0].ContractID)
		require.Equal(t, "get_current_rate_limiter_state", records[0].Fn)
		require.Equal(t, scval.Uint64ToScVal(7), records[0].Args[0])
		require.Equal(t, scval.BoolToScVal(false), records[0].Args[1])
	})

	t.Run("unconfigured lane reads as zero-value buckets", func(t *testing.T) {
		t.Parallel()
		inv := stubRateLimiterState(t, f, tpoolbindings.RateLimiterState{})
		got, err := readCurrentRateLimits(t, inv, f, true)
		require.NoError(t, err)
		require.Equal(t, tokens.RateLimiterConfig{IsEnabled: false, Capacity: big.NewInt(0), Rate: big.NewInt(0)}, got.Outbound)
		require.Equal(t, got.Outbound, got.Inbound)
		// The fastFinality flag threads through to the read.
		require.Equal(t, scval.BoolToScVal(true), inv.Records()[0].Args[1])
	})

	t.Run("U128 hi/lo widens through big.Int", func(t *testing.T) {
		t.Parallel()
		bigState := state
		bigState.Outbound.Capacity = hi
		inv := stubRateLimiterState(t, f, bigState)
		got, err := readCurrentRateLimits(t, inv, f, false)
		require.NoError(t, err)
		// (1 << 64) + 5
		want := new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(5))
		require.Equal(t, want, got.Outbound.Capacity)
	})
}

// TestStellarTokenAdapter_RateLimitReaderInterface pins the interface
// compliance and that the globally registered TokenAdapter instance
// type-asserts to RateLimitReaderAdapter, which is how the outbound-only arm
// of SetTokenPoolRateLimits discovers it.
func TestStellarTokenAdapter_RateLimitReaderInterface(t *testing.T) {
	t.Parallel()
	var _ tokens.RateLimitReaderAdapter = (*StellarTokenAdapter)(nil)

	adapter, ok := tokens.GetTokenAdapterRegistry().GetTokenAdapter("stellar", semver.MustParse("2.0.0"))
	require.True(t, ok)
	_, ok = adapter.(tokens.RateLimitReaderAdapter)
	require.True(t, ok, "registered TokenAdapter must implement RateLimitReaderAdapter")
}
