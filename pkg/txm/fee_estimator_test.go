package txm

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCalculateInclusionFee_NoRetries(t *testing.T) {
	cfg := DefaultConfig()
	fee := CalculateInclusionFee(0, cfg)
	assert.Equal(t, cfg.FeeBuffer, fee)
}

func TestCalculateInclusionFee_Geometric(t *testing.T) {
	cfg := DefaultConfig()
	fee0 := CalculateInclusionFee(0, cfg)
	fee1 := CalculateInclusionFee(1, cfg)
	fee2 := CalculateInclusionFee(2, cfg)

	expected1 := int64(math.Round(float64(cfg.FeeBuffer) * cfg.FeeBumpMultiplier))
	expected2 := int64(math.Round(float64(cfg.FeeBuffer) * cfg.FeeBumpMultiplier * cfg.FeeBumpMultiplier))

	assert.Equal(t, expected1, fee1)
	assert.Equal(t, expected2, fee2)
	assert.Greater(t, fee1, fee0)
	assert.Greater(t, fee2, fee1)
}

func TestCalculateInclusionFee_Cap(t *testing.T) {
	cfg := Config{
		FeeBuffer:         100_000,
		FeeBumpMultiplier: 10.0,
		MaxInclusionFee:   500_000,
	}

	fee := CalculateInclusionFee(5, cfg)
	assert.Equal(t, cfg.MaxInclusionFee, fee)
}
