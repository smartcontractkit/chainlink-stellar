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

func TestFeeTracker_UsesTTLBetweenCalls(t *testing.T) {
	t.Parallel()
	client := &countingFeeStatsClient{
		resp: protocolrpc.GetFeeStatsResponse{
			SorobanInclusionFee: protocolrpc.FeeDistribution{
				P50: 100,
				P90: 200,
				P99: 900,
			},
		},
	}
	tr := newFeeTracker(5 * time.Second)
	ctx := t.Context()

	want := inclusionFeePercentiles{p50: 100, p90: 200, p99: 900}
	fees, err := tr.sorobanInclusionPercentiles(ctx, client)
	require.NoError(t, err)
	assert.Equal(t, want, fees)
	assert.Equal(t, int32(1), client.calls.Load())

	fees, err = tr.sorobanInclusionPercentiles(ctx, client)
	require.NoError(t, err)
	assert.Equal(t, want, fees)
	assert.Equal(t, int32(1), client.calls.Load(), "second call within TTL must not hit RPC")
}

func TestFeeTracker_ZeroPollIntervalAlwaysFetches(t *testing.T) {
	t.Parallel()
	client := &countingFeeStatsClient{
		resp: protocolrpc.GetFeeStatsResponse{
			SorobanInclusionFee: protocolrpc.FeeDistribution{P50: 1, P90: 2},
		},
	}
	tr := newFeeTracker(0)
	ctx := t.Context()

	_, err := tr.sorobanInclusionPercentiles(ctx, client)
	require.NoError(t, err)
	_, err = tr.sorobanInclusionPercentiles(ctx, client)
	require.NoError(t, err)
	assert.Equal(t, int32(2), client.calls.Load())
}

func TestFeeTracker_StaleWhileRevalidate(t *testing.T) {
	t.Parallel()
	client := &countingFeeStatsClient{
		resp: protocolrpc.GetFeeStatsResponse{
			SorobanInclusionFee: protocolrpc.FeeDistribution{P50: 10, P90: 20, P99: 30},
		},
	}
	tr := newFeeTracker(10 * time.Millisecond)
	ctx := t.Context()

	_, err := tr.sorobanInclusionPercentiles(ctx, client)
	require.NoError(t, err)
	require.Equal(t, int32(1), client.calls.Load())

	time.Sleep(15 * time.Millisecond)
	client.err = assert.AnError
	client.resp = protocolrpc.GetFeeStatsResponse{}

	fees, err := tr.sorobanInclusionPercentiles(ctx, client)
	require.NoError(t, err, "should serve stale data when refresh fails")
	assert.Equal(t, inclusionFeePercentiles{p50: 10, p90: 20, p99: 30}, fees)
	assert.Equal(t, int32(2), client.calls.Load())
}

func TestFeeTracker_FirstFetchErrorNoData(t *testing.T) {
	t.Parallel()
	client := &countingFeeStatsClient{err: assert.AnError}
	tr := newFeeTracker(time.Hour)
	ctx := t.Context()

	_, err := tr.sorobanInclusionPercentiles(ctx, client)
	assert.Error(t, err)
	assert.Equal(t, int32(1), client.calls.Load())
}
