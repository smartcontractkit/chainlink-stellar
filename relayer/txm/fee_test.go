package txm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func defaultFeeStrategy() FeeStrategy {
	return FeeStrategy{
		BaseInclusionFee:  100,
		MaxInclusionFee:   100_000,
		BumpMultiplier:    1.5,
		ResourceFeeBuffer: 15_000,
	}
}

func TestFeeStrategy_Calculate_FirstAttempt(t *testing.T) {
	t.Parallel()
	fs := defaultFeeStrategy()

	// Total = inclusionFee(0) + minResourceFee + buffer
	//       = 100 + 50_000 + 15_000 = 65_100
	total := fs.Calculate(50_000, 0)
	assert.Equal(t, int64(65_100), total)
}

func TestFeeStrategy_Calculate_GeometricProgression(t *testing.T) {
	t.Parallel()
	fs := defaultFeeStrategy()

	minResourceFee := int64(50_000)

	// Attempt 0: inclusion = 100
	assert.Equal(t, int64(100+50_000+15_000), fs.Calculate(minResourceFee, 0))

	// Attempt 1: inclusion = ceil(100 * 1.5) = 150
	assert.Equal(t, int64(150+50_000+15_000), fs.Calculate(minResourceFee, 1))

	// Attempt 2: inclusion = ceil(100 * 1.5^2) = ceil(225) = 225
	assert.Equal(t, int64(225+50_000+15_000), fs.Calculate(minResourceFee, 2))

	// Attempt 3: inclusion = ceil(100 * 1.5^3) = ceil(337.5) = 338
	assert.Equal(t, int64(338+50_000+15_000), fs.Calculate(minResourceFee, 3))

	// Attempt 4: inclusion = ceil(100 * 1.5^4) = ceil(506.25) = 507
	assert.Equal(t, int64(507+50_000+15_000), fs.Calculate(minResourceFee, 4))
}

func TestFeeStrategy_Calculate_CapsAtMax(t *testing.T) {
	t.Parallel()
	fs := FeeStrategy{
		BaseInclusionFee:  100,
		MaxInclusionFee:   500,
		BumpMultiplier:    2.0,
		ResourceFeeBuffer: 10_000,
	}

	minResourceFee := int64(30_000)

	// Attempt 0: 100
	assert.Equal(t, int64(100+30_000+10_000), fs.Calculate(minResourceFee, 0))
	// Attempt 1: 200
	assert.Equal(t, int64(200+30_000+10_000), fs.Calculate(minResourceFee, 1))
	// Attempt 2: 400
	assert.Equal(t, int64(400+30_000+10_000), fs.Calculate(minResourceFee, 2))
	// Attempt 3: would be 800, capped at 500
	assert.Equal(t, int64(500+30_000+10_000), fs.Calculate(minResourceFee, 3))
	// Attempt 10: still capped at 500
	assert.Equal(t, int64(500+30_000+10_000), fs.Calculate(minResourceFee, 10))
}

func TestFeeStrategy_Calculate_ZeroResourceFee(t *testing.T) {
	t.Parallel()
	fs := defaultFeeStrategy()

	// Even with zero minResourceFee, the buffer is still added
	total := fs.Calculate(0, 0)
	assert.Equal(t, int64(100+15_000), total)
}

func TestFeeStrategy_CalculateRestoreFee(t *testing.T) {
	t.Parallel()
	fs := defaultFeeStrategy()

	// Restore fee = preamble min resource fee + restore buffer (no geometric bumping)
	fee := fs.CalculateRestoreFee(80_000, 10_000)
	assert.Equal(t, int64(90_000), fee)
}

func TestFeeStrategy_CalculateRestoreFee_ZeroBuffer(t *testing.T) {
	t.Parallel()
	fs := defaultFeeStrategy()

	fee := fs.CalculateRestoreFee(80_000, 0)
	assert.Equal(t, int64(80_000), fee)
}

func TestFeeStrategy_NewFromConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfigSet
	cfg.Resolve()
	fs := NewFeeStrategyFromConfig(cfg)

	assert.Equal(t, int64(100), fs.BaseInclusionFee)
	assert.Equal(t, int64(100_000), fs.MaxInclusionFee)
	assert.Equal(t, 1.5, fs.BumpMultiplier)
	assert.Equal(t, int64(15_000), fs.ResourceFeeBuffer)
}

func TestFeeStrategy_Calculate_MultiplierOfOne(t *testing.T) {
	t.Parallel()
	fs := FeeStrategy{
		BaseInclusionFee:  100,
		MaxInclusionFee:   100_000,
		BumpMultiplier:    1.0,
		ResourceFeeBuffer: 5_000,
	}

	// With multiplier 1.0, fee never changes regardless of attempt
	for attempt := uint64(0); attempt < 5; attempt++ {
		assert.Equal(t, int64(100+50_000+5_000), fs.Calculate(50_000, attempt))
	}
}
