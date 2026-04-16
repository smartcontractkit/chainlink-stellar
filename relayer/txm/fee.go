package txm

import "math"

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
}

// NewFeeStrategyFromConfig constructs a FeeStrategy from the resolved Config.
func NewFeeStrategyFromConfig(cfg Config) FeeStrategy {
	return FeeStrategy{
		BaseInclusionFee:  *cfg.BaseInclusionFee,
		MaxInclusionFee:   *cfg.MaxInclusionFee,
		BumpMultiplier:    *cfg.FeeBumpMultiplier,
		ResourceFeeBuffer: *cfg.ResourceFeeBuffer,
	}
}

// Calculate returns the total fee (in stroops) for a transaction at the given attempt.
// The inclusion fee is geometrically bumped per attempt; the resource fee is passed
// through from simulation with a flat safety buffer.
func (f *FeeStrategy) Calculate(minResourceFee int64, attempt uint64) int64 {
	inclusionFee := f.inclusionFee(attempt)
	resourceFee := minResourceFee + f.ResourceFeeBuffer
	return inclusionFee + resourceFee
}

// CalculateRestoreFee returns the fee for a RestoreFootprint transaction.
// Restore fees are deterministic (no fee competition), so no geometric bumping.
func (f *FeeStrategy) CalculateRestoreFee(preambleMinResourceFee int64, restoreFeeBuffer int64) int64 {
	return preambleMinResourceFee + restoreFeeBuffer
}

// InclusionFee returns the inclusion fee for the given attempt number.
func (f *FeeStrategy) inclusionFee(attempt uint64) int64 {
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
