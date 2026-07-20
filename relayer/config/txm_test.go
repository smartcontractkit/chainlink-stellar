package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolve_AllDefaults(t *testing.T) {
	t.Parallel()

	d := Defaults().TxManager
	cfg := TxManagerConfig{}
	cfg.Resolve()

	// Shared fields
	assert.Equal(t, d.BroadcastChanSize, cfg.BroadcastChanSize)
	assert.Equal(t, d.ConfirmPollInterval, cfg.ConfirmPollInterval)
	assert.Equal(t, d.MaxSimulateAttempts, cfg.MaxSimulateAttempts)
	assert.Equal(t, d.MaxSubmitRetryAttempts, cfg.MaxSubmitRetryAttempts)
	assert.Equal(t, d.SubmitRetryDelay, cfg.SubmitRetryDelay)
	assert.Equal(t, d.MaxTxRetryAttempts, cfg.MaxTxRetryAttempts)
	assert.Equal(t, d.MaxGetClientRetryAttempts, cfg.MaxGetClientRetryAttempts)
	assert.Equal(t, d.PruneInterval, cfg.PruneInterval)
	assert.Equal(t, d.PruneTxExpiration, cfg.PruneTxExpiration)
	assert.Equal(t, d.FeeStatsPollInterval, cfg.FeeStatsPollInterval)
	assert.Equal(t, d.BaseInclusionFee, cfg.BaseInclusionFee)
	assert.Equal(t, d.MaxInclusionFee, cfg.MaxInclusionFee)
	assert.Equal(t, d.FeeBumpMultiplier, cfg.FeeBumpMultiplier)
	assert.Equal(t, d.ResourceFeeBuffer, cfg.ResourceFeeBuffer)
	assert.Equal(t, d.RestoreFeeBuffer, cfg.RestoreFeeBuffer)

	// Stellar-specific timeout fields
	assert.Equal(t, d.TxTimeoutSecs, cfg.TxTimeoutSecs)
	assert.Equal(t, d.LedgerBoundsOffset, cfg.LedgerBoundsOffset)
	assert.Equal(t, d.MaxRestoreAttempts, cfg.MaxRestoreAttempts)

	assert.Equal(t, builtinSimulationTerminalHints, cfg.SimulationTerminalHints)
	assert.Equal(t, builtinSimulationRetryableHints, cfg.SimulationRetryableHints)
}

func TestResolve_PartialOverride(t *testing.T) {
	t.Parallel()

	d := Defaults().TxManager
	cfg := TxManagerConfig{
		BroadcastChanSize: ptr(uint(50)),
		BaseInclusionFee:  ptr(int64(200)),
	}
	cfg.Resolve()

	assert.Equal(t, uint(50), *cfg.BroadcastChanSize)
	assert.Equal(t, int64(200), *cfg.BaseInclusionFee)

	// Non-overridden fields still get defaults
	assert.Equal(t, d.ConfirmPollInterval, cfg.ConfirmPollInterval)
	assert.Equal(t, d.MaxInclusionFee, cfg.MaxInclusionFee)
	assert.Equal(t, d.FeeBumpMultiplier, cfg.FeeBumpMultiplier)
	assert.Equal(t, d.MaxSimulateAttempts, cfg.MaxSimulateAttempts)
	assert.Equal(t, d.LedgerBoundsOffset, cfg.LedgerBoundsOffset)
	assert.Equal(t, builtinSimulationTerminalHints, cfg.SimulationTerminalHints)
	assert.Equal(t, builtinSimulationRetryableHints, cfg.SimulationRetryableHints)
}

func TestResolve_CustomSimulationHintsAreAdditive(t *testing.T) {
	t.Parallel()

	cfg := TxManagerConfig{
		SimulationTerminalHints:  []string{"custom-terminal"},
		SimulationRetryableHints: []string{"custom-retry"},
	}
	cfg.Resolve()

	assert.Contains(t, cfg.SimulationTerminalHints, "trapped")
	assert.Contains(t, cfg.SimulationTerminalHints, "custom-terminal")
	assert.Equal(t, len(builtinSimulationTerminalHints)+1, len(cfg.SimulationTerminalHints))

	assert.Contains(t, cfg.SimulationRetryableHints, "timeout")
	assert.Contains(t, cfg.SimulationRetryableHints, "custom-retry")
	assert.Equal(t, len(builtinSimulationRetryableHints)+1, len(cfg.SimulationRetryableHints))
}

func TestResolve_SimulationHintsDedupesUserDuplicatesOfBuiltin(t *testing.T) {
	t.Parallel()

	cfg := TxManagerConfig{
		SimulationTerminalHints: []string{"trapped", "only-new-terminal"},
	}
	cfg.Resolve()

	assert.Equal(t, len(builtinSimulationTerminalHints)+1, len(cfg.SimulationTerminalHints))
	assert.Contains(t, cfg.SimulationTerminalHints, "only-new-terminal")
}

func TestResolve_ExplicitZero(t *testing.T) {
	t.Parallel()

	cfg := TxManagerConfig{
		BaseInclusionFee:   ptr(int64(0)),
		MaxRestoreAttempts: ptr(uint(0)),
	}
	cfg.Resolve()

	assert.Equal(t, int64(0), *cfg.BaseInclusionFee,
		"explicit 0 must not be overwritten by default of 100")
	assert.Equal(t, uint(0), *cfg.MaxRestoreAttempts,
		"explicit 0 must not be overwritten by default of 3")

	// Non-overridden fields still get defaults
	assert.Equal(t, Defaults().TxManager.BroadcastChanSize, cfg.BroadcastChanSize)
	assert.Equal(t, Defaults().TxManager.MaxInclusionFee, cfg.MaxInclusionFee)
}

func TestResolve_StellarFeeDefaults(t *testing.T) {
	t.Parallel()

	cfg := TxManagerConfig{}
	cfg.Resolve()

	assert.Equal(t, int64(100), *cfg.BaseInclusionFee, "MinBaseFee = 100 stroops")
	assert.Equal(t, int64(100_000), *cfg.MaxInclusionFee, "cap at 0.01 XLM")
	assert.Equal(t, 1.5, *cfg.FeeBumpMultiplier, "1.5x geometric bump")
	assert.Equal(t, int64(15_000), *cfg.ResourceFeeBuffer, "~15%% buffer over typical MinResourceFee")
	assert.Equal(t, int64(10_000), *cfg.RestoreFeeBuffer, "restore fee buffer")
}

func TestResolve_StellarTimeoutDefaults(t *testing.T) {
	t.Parallel()

	cfg := TxManagerConfig{}
	cfg.Resolve()

	assert.Equal(t, uint32(50), *cfg.LedgerBoundsOffset, "~5 min at 6s/ledger")
	assert.Equal(t, int64(300), *cfg.TxTimeoutSecs, "5 min wall-clock fallback")
	assert.Equal(t, uint(3), *cfg.MaxRestoreAttempts, "max restore attempts")
	assert.Equal(t, uint(3), *cfg.MaxSimulateAttempts, "max simulate attempts")
}
