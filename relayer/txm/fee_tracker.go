package txm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
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

// feePercentile selects which Soroban inclusion fee percentile seeds a bid.
type feePercentile uint8

const (
	feeP50 feePercentile = iota
	feeP90
	feeP99
)

// Fallbacks for invalid fee seeding config; must match the docs.toml defaults.
const (
	defaultFirstSendFeePercentile   = feeP90
	defaultRebroadcastFeePercentile = feeP99
)

// feePercentileOrDefault parses value, falling back to def with a warning when it is not
// P50, P90 or P99.
func feePercentileOrDefault(lggr logger.Logger, field, value string, def feePercentile) feePercentile {
	p, err := parseFeePercentile(value)
	if err != nil {
		lggr.Warnw("unknown fee percentile, using default", "field", field, "value", value, "default", def)
		return def
	}
	return p
}

// parseFeePercentile accepts "P50", "P90" or "P99", case-insensitively.
func parseFeePercentile(s string) (feePercentile, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "P50":
		return feeP50, nil
	case "P90":
		return feeP90, nil
	case "P99":
		return feeP99, nil
	}
	return 0, fmt.Errorf("unknown fee percentile %q (want P50, P90 or P99)", s)
}

func (p feePercentile) String() string {
	switch p {
	case feeP50:
		return "P50"
	case feeP90:
		return "P90"
	default:
		return "P99"
	}
}

func (s feeSample) percentile(p feePercentile) uint64 {
	switch p {
	case feeP50:
		return s.p50
	case feeP90:
		return s.p90
	default:
		return s.p99
	}
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
