package txm

import (
	"context"
	"sync"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

// feeStatsRPC is the subset of RPCClient needed to refresh Soroban fee stats.
type feeStatsRPC interface {
	GetFeeStats(ctx context.Context) (protocolrpc.GetFeeStatsResponse, error)
}

// inclusionFeePercentiles are Soroban inclusion fee percentiles (stroops) from GetFeeStats.
type inclusionFeePercentiles struct {
	p50, p90, p99 uint64
}

// feeTracker holds Soroban inclusion fee percentiles from GetFeeStats and refreshes them
// at most once per pollInterval to reduce RPC load when many txs broadcast back-to-back.
// A non-positive pollInterval disables caching (every call fetches). On refresh error,
// the last successful snapshot is returned if any (stale-while-revalidate).
type feeTracker struct {
	mu           sync.Mutex
	pollInterval time.Duration

	haveData  bool
	fetchedAt time.Time
	fees      inclusionFeePercentiles
}

func newFeeTracker(pollInterval time.Duration) *feeTracker {
	return &feeTracker{pollInterval: pollInterval}
}

// sorobanInclusionPercentiles returns Soroban inclusion fee percentiles (stroops).
// On error it returns zero percentiles and err when there is no usable cached data yet; otherwise
// it returns the last successful snapshot with err == nil (stale-while-revalidate).
func (t *feeTracker) sorobanInclusionPercentiles(ctx context.Context, client feeStatsRPC) (inclusionFeePercentiles, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	if t.pollInterval > 0 && t.haveData && now.Sub(t.fetchedAt) < t.pollInterval {
		return t.fees, nil
	}

	resp, fetchErr := client.GetFeeStats(ctx)
	if fetchErr != nil {
		if t.haveData {
			return t.fees, nil
		}
		return inclusionFeePercentiles{}, fetchErr
	}

	t.fees = inclusionFeePercentiles{
		p50: resp.SorobanInclusionFee.P50,
		p90: resp.SorobanInclusionFee.P90,
		p99: resp.SorobanInclusionFee.P99,
	}
	t.fetchedAt = now
	t.haveData = true
	return t.fees, nil
}
