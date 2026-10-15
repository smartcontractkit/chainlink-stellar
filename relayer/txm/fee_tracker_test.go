package txm

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type countingFeeStatsClient struct {
	calls atomic.Int32
	resp  protocolrpc.GetFeeStatsResponse
	err   error
}

func (c *countingFeeStatsClient) GetFeeStats(_ context.Context) (protocolrpc.GetFeeStatsResponse, error) {
	c.calls.Add(1)
	return c.resp, c.err
}

func feeStats(p50, p90, p99 uint64) protocolrpc.GetFeeStatsResponse {
	return protocolrpc.GetFeeStatsResponse{SorobanInclusionFee: protocolrpc.FeeDistribution{P50: p50, P90: p90, P99: p99}}
}

// fakeClock drives feeTracker.now without sleeping.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestFeeTracker(pollInterval, lookback time.Duration) (*feeTracker, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	tr := newFeeTracker(pollInterval, lookback)
	tr.now = clock.now
	return tr, clock
}

func TestFeeTracker_UsesTTLBetweenCalls(t *testing.T) {
	t.Parallel()
	client := &countingFeeStatsClient{resp: feeStats(100, 200, 300)}
	tr, clock := newTestFeeTracker(5*time.Second, time.Minute)
	ctx := t.Context()

	bid, latest, err := tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err)
	assert.Equal(t, uint64(300), bid)
	assert.Equal(t, feeSample{at: clock.t, p50: 100, p90: 200, p99: 300}, latest)
	assert.Equal(t, int32(1), client.calls.Load())

	clock.advance(4 * time.Second)
	bid, _, err = tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err)
	assert.Equal(t, uint64(300), bid)
	assert.Equal(t, int32(1), client.calls.Load(), "second call within TTL must not hit RPC")

	clock.advance(time.Second)
	_, _, err = tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err)
	assert.Equal(t, int32(2), client.calls.Load(), "call at TTL refreshes")
}

func TestFeeTracker_ZeroPollIntervalAlwaysFetches(t *testing.T) {
	t.Parallel()
	client := &countingFeeStatsClient{resp: feeStats(1, 2, 3)}
	tr, _ := newTestFeeTracker(0, time.Minute)
	ctx := t.Context()

	_, _, err := tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err)
	_, _, err = tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err)
	assert.Equal(t, int32(2), client.calls.Load())
}

func TestFeeTracker_StaleWhileRevalidate(t *testing.T) {
	t.Parallel()
	client := &countingFeeStatsClient{resp: feeStats(10, 20, 30)}
	tr, clock := newTestFeeTracker(10*time.Millisecond, time.Minute)
	ctx := t.Context()

	_, _, err := tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err)
	require.Equal(t, int32(1), client.calls.Load())

	clock.advance(15 * time.Millisecond)
	client.err = assert.AnError
	client.resp = protocolrpc.GetFeeStatsResponse{}

	bid, latest, err := tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err, "should serve stale data when refresh fails")
	assert.Equal(t, uint64(30), bid)
	assert.Equal(t, uint64(10), latest.p50)
	assert.Equal(t, int32(2), client.calls.Load())
}

func TestFeeTracker_FirstFetchErrorNoData(t *testing.T) {
	t.Parallel()
	client := &countingFeeStatsClient{err: assert.AnError}
	tr, _ := newTestFeeTracker(time.Hour, time.Minute)

	_, _, err := tr.sorobanInclusionFeeBid(t.Context(), client)
	assert.Error(t, err)
	assert.Equal(t, int32(1), client.calls.Load())
}

func TestFeeTracker_BidsPeakP99WithinLookback(t *testing.T) {
	t.Parallel()
	client := &countingFeeStatsClient{resp: feeStats(100, 100, 100)}
	tr, clock := newTestFeeTracker(5*time.Second, 2*time.Minute)
	ctx := t.Context()

	bid, _, err := tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err)
	assert.Equal(t, uint64(100), bid)

	// P99 jumps while P50 and P90 stay at base.
	clock.advance(5 * time.Second)
	client.resp = feeStats(100, 100, 26_000_000)
	bid, _, err = tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err)
	assert.Equal(t, uint64(26_000_000), bid)

	// P99 falls within the lookback: still bid the peak.
	clock.advance(5 * time.Second)
	client.resp = feeStats(100, 100, 1_000_000)
	bid, latest, err := tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err)
	assert.Equal(t, uint64(26_000_000), bid)
	assert.Equal(t, uint64(1_000_000), latest.p99)

	// The peak sample ages out of the lookback.
	clock.advance(2 * time.Minute)
	bid, _, err = tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err)
	assert.Equal(t, uint64(1_000_000), bid)
}

func TestFeeTracker_ZeroLookbackKeepsLatestOnly(t *testing.T) {
	t.Parallel()
	client := &countingFeeStatsClient{resp: feeStats(100, 100, 26_000_000)}
	tr, clock := newTestFeeTracker(5*time.Second, 0)
	ctx := t.Context()

	bid, _, err := tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err)
	assert.Equal(t, uint64(26_000_000), bid)

	clock.advance(5 * time.Second)
	client.resp = feeStats(100, 100, 100)
	bid, _, err = tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err)
	assert.Equal(t, uint64(100), bid)
	assert.Len(t, tr.samples, 1)
}

func TestFeeTracker_RefreshErrorKeepsOldSampleBeyondLookback(t *testing.T) {
	t.Parallel()
	client := &countingFeeStatsClient{resp: feeStats(100, 100, 5_000)}
	tr, clock := newTestFeeTracker(5*time.Second, time.Minute)
	ctx := t.Context()

	_, _, err := tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err)

	clock.advance(time.Hour)
	client.err = assert.AnError
	bid, _, err := tr.sorobanInclusionFeeBid(ctx, client)
	require.NoError(t, err)
	assert.Equal(t, uint64(5_000), bid)
}
