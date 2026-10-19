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

// feeSample is one GetFeeStats Soroban inclusion fee snapshot (stroops).
type feeSample struct {
	at            time.Time
	p50, p90, p99 uint64
}

// feeTracker caches GetFeeStats Soroban inclusion fee percentiles, refreshing at most
// once per pollInterval, and keeps the samples from the last lookback. A non-positive
// pollInterval fetches on every call; a non-positive lookback keeps only the latest
// sample. On refresh error the samples already held are used (stale-while-revalidate).
type feeTracker struct {
	mu           sync.Mutex
	pollInterval time.Duration
	lookback     time.Duration
	now          func() time.Time

	samples []feeSample // oldest first
}

func newFeeTracker(pollInterval, lookback time.Duration) *feeTracker {
	return &feeTracker{pollInterval: pollInterval, lookback: lookback, now: time.Now}
}

// sorobanInclusionFees returns the highest value of each Soroban inclusion fee
// percentile within lookback (peak) and the latest sample. It errors only when no
// sample has ever been fetched.
func (t *feeTracker) sorobanInclusionFees(ctx context.Context, client feeStatsRPC) (peak, latest feeSample, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	if len(t.samples) == 0 || t.pollInterval <= 0 || now.Sub(t.samples[len(t.samples)-1].at) >= t.pollInterval {
		resp, fetchErr := client.GetFeeStats(ctx)
		if fetchErr != nil && len(t.samples) == 0 {
			return feeSample{}, feeSample{}, fetchErr
		}
		if fetchErr == nil {
			t.samples = append(t.samples, feeSample{
				at:  now,
				p50: resp.SorobanInclusionFee.P50,
				p90: resp.SorobanInclusionFee.P90,
				p99: resp.SorobanInclusionFee.P99,
			})
		}
	}
	t.prune(now)

	for _, s := range t.samples {
		peak.p50 = max(peak.p50, s.p50)
		peak.p90 = max(peak.p90, s.p90)
		peak.p99 = max(peak.p99, s.p99)
	}
	return peak, t.samples[len(t.samples)-1], nil
}

// prune drops samples older than lookback, always keeping the latest one.
func (t *feeTracker) prune(now time.Time) {
	keepFrom := len(t.samples) - 1
	for keepFrom > 0 && now.Sub(t.samples[keepFrom-1].at) <= t.lookback {
		keepFrom--
	}
	t.samples = t.samples[keepFrom:]
}
