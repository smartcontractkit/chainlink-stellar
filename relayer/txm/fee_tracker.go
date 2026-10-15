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

// feeSample is one GetFeeStats Soroban inclusion-fee snapshot (stroops).
type feeSample struct {
	at            time.Time
	p50, p90, p99 uint64
}

// feeTracker polls GetFeeStats at most once per pollInterval and keeps every sample
// taken within lookback. Its bid is the highest Soroban inclusion-fee P99 among them.
//
// The RPC derives the percentiles from the inclusion fees charged over its fee-stats
// retention window (50 ledgers by default). Stellar charges every transaction in a
// lane the lane's clearing price, so through a surge shorter than that window P50 and
// P90 stay at the base fee while P99 moves within a ledger or two; and because surge
// priced ledgers alternate with base priced ones, holding the peak for the lookback
// keeps the bid from falling back while the surge is still on.
//
// A non-positive pollInterval fetches on every call; a non-positive lookback keeps
// only the latest sample. When a refresh fails the samples already held are used.
type feeTracker struct {
	mu           sync.Mutex
	pollInterval time.Duration
	lookback     time.Duration
	now          func() time.Time

	samples []feeSample // oldest first; never emptied once a fetch has succeeded
}

func newFeeTracker(pollInterval, lookback time.Duration) *feeTracker {
	return &feeTracker{pollInterval: pollInterval, lookback: lookback, now: time.Now}
}

// sorobanInclusionFeeBid returns the highest Soroban inclusion-fee P99 reported within
// lookback and the latest sample. It fails only when no sample has ever been fetched.
func (t *feeTracker) sorobanInclusionFeeBid(ctx context.Context, client feeStatsRPC) (bid uint64, latest feeSample, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	if len(t.samples) == 0 || t.pollInterval <= 0 || now.Sub(t.samples[len(t.samples)-1].at) >= t.pollInterval {
		resp, fetchErr := client.GetFeeStats(ctx)
		if fetchErr != nil && len(t.samples) == 0 {
			return 0, feeSample{}, fetchErr
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
		bid = max(bid, s.p99)
	}
	return bid, t.samples[len(t.samples)-1], nil
}

// prune drops samples older than lookback, always keeping the latest one.
func (t *feeTracker) prune(now time.Time) {
	keepFrom := len(t.samples) - 1
	for keepFrom > 0 && now.Sub(t.samples[keepFrom-1].at) <= t.lookback {
		keepFrom--
	}
	t.samples = t.samples[keepFrom:]
}
