package txm

import (
	"fmt"
	"math"

	"github.com/smartcontractkit/chainlink-stellar/relayer/config"
)

// FeeStrategy calculates Stellar transaction fees.
//
// Stellar fees have two independent components:
//   - Inclusion fee: market-based bid for validator priority (bumped on retries)
//   - Resource fee: deterministic cost from simulation (not negotiable)
//
// Total fee = inclusionFee(attempt) + minResourceFee + resourceFeeBuffer
type FeeStrategy struct {
	BaseInclusionFee  int64
	MaxInclusionFee   int64
	BumpMultiplier    float64
	ResourceFeeBuffer int64
	MaxResourceFee    int64
}

// NewFeeStrategyFromConfig constructs a FeeStrategy from the resolved Config.
func NewFeeStrategyFromConfig(cfg config.TxManagerConfig) FeeStrategy {
	return FeeStrategy{
		BaseInclusionFee:  *cfg.BaseInclusionFee,
		MaxInclusionFee:   *cfg.MaxInclusionFee,
		BumpMultiplier:    *cfg.FeeBumpMultiplier,
		ResourceFeeBuffer: *cfg.ResourceFeeBuffer,
		MaxResourceFee:    *cfg.MaxResourceFee,
	}
}

// Calculate returns the total fee (in stroops) for a transaction at the given attempt.
// The inclusion fee is geometrically bumped per attempt; the resource fee is passed
// through from simulation with a flat safety buffer.
func (f *FeeStrategy) Calculate(minResourceFee int64, attempt uint64) int64 {
	inclusionFee := f.InclusionFee(attempt)
	resourceFee := minResourceFee + f.ResourceFeeBuffer
	return inclusionFee + resourceFee
}

// ResourceFee returns the resource fee (in stroops) to write into SorobanData: the RPC-reported
// minimum plus a flat buffer, bounded by the tighter of MaxResourceFee and the per-request cap
// (0 = uncapped). minResourceFee is untrusted RPC output, so a non-positive value or one over
// the cap is an error rather than a signed envelope.
func (f *FeeStrategy) ResourceFee(minResourceFee int64, buffer int64, perRequestMaxResourceFee uint64) (int64, error) {
	if minResourceFee <= 0 {
		return 0, fmt.Errorf("rpc reported non-positive MinResourceFee %d", minResourceFee)
	}
	if buffer < 0 {
		return 0, fmt.Errorf("negative resource fee buffer %d", buffer)
	}
	if minResourceFee > math.MaxInt64-buffer {
		return 0, fmt.Errorf("resource fee overflow: MinResourceFee=%d buffer=%d", minResourceFee, buffer)
	}
	fee := minResourceFee + buffer

	capFee := f.MaxResourceFee
	if perRequestMaxResourceFee > 0 && perRequestMaxResourceFee <= math.MaxInt64 {
		if capFee == 0 || int64(perRequestMaxResourceFee) < capFee {
			capFee = int64(perRequestMaxResourceFee)
		}
	}
	if capFee > 0 && fee > capFee {
		return 0, fmt.Errorf("resource fee %d stroops exceeds cap %d (MinResourceFee=%d, buffer=%d)", fee, capFee, minResourceFee, buffer)
	}
	return fee, nil
}

// InclusionFee returns the inclusion fee for the given attempt number.
func (f *FeeStrategy) InclusionFee(attempt uint64) int64 {
	if attempt == 0 {
		return f.BaseInclusionFee
	}

	fee := float64(f.BaseInclusionFee) * math.Pow(f.BumpMultiplier, float64(attempt))
	result := int64(math.Ceil(fee))

	if result > f.MaxInclusionFee {
		return f.MaxInclusionFee
	}
	return result
}

// SeedInclusionFee returns the starting inclusion fee for a transaction broadcast.
// It picks max(geometric baseline for `attempt`, network percentile stroops from
// GetFeeStats — typically P50 for the first attempt and P90 for rebroadcasts,
// refreshed via feeTracker) and caps the result at MaxInclusionFee.

func (f *FeeStrategy) SeedInclusionFee(attempt uint64, networkPercentile uint64) (fee int64, clampedToMax bool) {
	fee = f.InclusionFee(attempt)
	if networkFee := int64(networkPercentile); networkFee > fee { //nolint:gosec // wrap-to-negative is safe; see comment above
		fee = networkFee
	}
	if fee > f.MaxInclusionFee {
		return f.MaxInclusionFee, true
	}
	return fee, false
}

// BumpInclusionFee returns the next inclusion fee after a submit rejection that
// requires a higher bid (e.g. tx_insufficient_fee): multiply the current fee,
// take max with networkPercentile (typically live P90 from GetFeeStats), and
// clamp to MaxInclusionFee.
func (f *FeeStrategy) BumpInclusionFee(currentInclusionFee int64, networkPercentile uint64) (fee int64, clampedToMax bool) {
	bumped := int64(math.Ceil(float64(currentInclusionFee) * f.BumpMultiplier))
	if networkFee := int64(networkPercentile); networkFee > bumped { //nolint:gosec // same as SeedInclusionFee
		bumped = networkFee
	}
	if bumped > f.MaxInclusionFee {
		return f.MaxInclusionFee, true
	}
	return bumped, false
}
