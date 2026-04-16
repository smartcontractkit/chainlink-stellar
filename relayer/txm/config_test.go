package txm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolve_AllDefaults(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.Resolve()

	// Shared fields
	assert.Equal(t, DefaultConfigSet.BroadcastChanSize, cfg.BroadcastChanSize)
	assert.Equal(t, DefaultConfigSet.ConfirmPollInterval, cfg.ConfirmPollInterval)
	assert.Equal(t, DefaultConfigSet.MaxSimulateAttempts, cfg.MaxSimulateAttempts)
	assert.Equal(t, DefaultConfigSet.MaxSubmitRetryAttempts, cfg.MaxSubmitRetryAttempts)
	assert.Equal(t, DefaultConfigSet.SubmitRetryDelay, cfg.SubmitRetryDelay)
	assert.Equal(t, DefaultConfigSet.MaxTxRetryAttempts, cfg.MaxTxRetryAttempts)
	assert.Equal(t, DefaultConfigSet.PruneInterval, cfg.PruneInterval)
	assert.Equal(t, DefaultConfigSet.PruneTxExpiration, cfg.PruneTxExpiration)

	// Stellar-specific fee fields
	assert.Equal(t, DefaultConfigSet.BaseInclusionFee, cfg.BaseInclusionFee)
	assert.Equal(t, DefaultConfigSet.MaxInclusionFee, cfg.MaxInclusionFee)
	assert.Equal(t, DefaultConfigSet.FeeBumpMultiplier, cfg.FeeBumpMultiplier)
	assert.Equal(t, DefaultConfigSet.ResourceFeeBuffer, cfg.ResourceFeeBuffer)
	assert.Equal(t, DefaultConfigSet.RestoreFeeBuffer, cfg.RestoreFeeBuffer)

	// Stellar-specific timeout fields
	assert.Equal(t, DefaultConfigSet.TxTimeoutSecs, cfg.TxTimeoutSecs)
	assert.Equal(t, DefaultConfigSet.LedgerBoundsOffset, cfg.LedgerBoundsOffset)
	assert.Equal(t, DefaultConfigSet.MaxRestoreAttempts, cfg.MaxRestoreAttempts)
}

func TestResolve_PartialOverride(t *testing.T) {
	t.Parallel()

	cfg := Config{
		BroadcastChanSize: ptr(uint(50)),
		BaseInclusionFee:  ptr(int64(200)),
	}
	cfg.Resolve()

	assert.Equal(t, uint(50), *cfg.BroadcastChanSize)
	assert.Equal(t, int64(200), *cfg.BaseInclusionFee)

	// Non-overridden fields still get defaults
	assert.Equal(t, DefaultConfigSet.ConfirmPollInterval, cfg.ConfirmPollInterval)
	assert.Equal(t, DefaultConfigSet.MaxInclusionFee, cfg.MaxInclusionFee)
	assert.Equal(t, DefaultConfigSet.FeeBumpMultiplier, cfg.FeeBumpMultiplier)
	assert.Equal(t, DefaultConfigSet.MaxSimulateAttempts, cfg.MaxSimulateAttempts)
	assert.Equal(t, DefaultConfigSet.LedgerBoundsOffset, cfg.LedgerBoundsOffset)
}

func TestResolve_ExplicitZero(t *testing.T) {
	t.Parallel()

	cfg := Config{
		BaseInclusionFee:   ptr(int64(0)),
		MaxRestoreAttempts: ptr(uint(0)),
	}
	cfg.Resolve()

	assert.Equal(t, int64(0), *cfg.BaseInclusionFee,
		"explicit 0 must not be overwritten by default of 100")
	assert.Equal(t, uint(0), *cfg.MaxRestoreAttempts,
		"explicit 0 must not be overwritten by default of 3")

	// Non-overridden fields still get defaults
	assert.Equal(t, DefaultConfigSet.BroadcastChanSize, cfg.BroadcastChanSize)
	assert.Equal(t, DefaultConfigSet.MaxInclusionFee, cfg.MaxInclusionFee)
}

func TestResolve_StellarFeeDefaults(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.Resolve()

	assert.Equal(t, int64(100), *cfg.BaseInclusionFee, "MinBaseFee = 100 stroops")
	assert.Equal(t, int64(100_000), *cfg.MaxInclusionFee, "cap at 0.01 XLM")
	assert.Equal(t, 1.5, *cfg.FeeBumpMultiplier, "1.5x geometric bump")
	assert.Equal(t, int64(15_000), *cfg.ResourceFeeBuffer, "~15%% buffer over typical MinResourceFee")
	assert.Equal(t, int64(10_000), *cfg.RestoreFeeBuffer, "restore fee buffer")
}

func TestResolve_StellarTimeoutDefaults(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.Resolve()

	assert.Equal(t, uint32(50), *cfg.LedgerBoundsOffset, "~5 min at 6s/ledger")
	assert.Equal(t, int64(300), *cfg.TxTimeoutSecs, "5 min wall-clock fallback")
	assert.Equal(t, uint(3), *cfg.MaxRestoreAttempts, "max restore attempts")
	assert.Equal(t, uint(3), *cfg.MaxSimulateAttempts, "max simulate attempts")
}
