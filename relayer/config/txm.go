package config

import (
	clconfig "github.com/smartcontractkit/chainlink-common/pkg/config"
)

func ptr[T any](v T) *T { return &v }

// builtinSimulationTerminalHints and builtinSimulationRetryableHints are the
// defaults for TxManagerConfig.SimulationTerminalHints / SimulationRetryableHints (see
// isRetryableSimulationError in broadcast.go).
var (
	builtinSimulationTerminalHints = []string{
		"error(contract",
		"contract error",
		"trapped",
		"trap",
		"malformed",
		"bad auth",
		"invalid",
		"unknown function",
		"no such contract",
	}
	builtinSimulationRetryableHints = []string{
		"timeout",
		"temporarily unavailable",
		"try_again_later",
		"too many requests",
		"rate limit",
		"connection refused",
		"connection reset",
		"eof",
		"bad_seq",
		"tx_bad_seq",
		"sequence",
		"stale",
		"ledger",
	}
)

// TxManagerConfig defines the Stellar transaction manager configuration.
// Pointer fields are used for TOML deserialization — nil means "not set by user".
// After calling Resolve(), scalar pointer fields are non-nil; simulation hint
// slices are non-empty (built-in defaults when unset).
type TxManagerConfig struct {
	BroadcastChanSize   *uint              `toml:"BroadcastChanSize"`
	ConfirmPollInterval *clconfig.Duration `toml:"ConfirmPollInterval"`

	// Fee strategy: Stellar fees = InclusionFee + ResourceFee.
	// Only the inclusion fee is bumped on retries; the resource fee is deterministic from simulation.
	BaseInclusionFee  *int64   `toml:"BaseInclusionFee"`
	MaxInclusionFee   *int64   `toml:"MaxInclusionFee"`
	FeeBumpMultiplier *float64 `toml:"FeeBumpMultiplier"`
	ResourceFeeBuffer *int64   `toml:"ResourceFeeBuffer"`
	RestoreFeeBuffer  *int64   `toml:"RestoreFeeBuffer"`
	// FeeStatsPollInterval controls how often GetFeeStats is called to refresh
	// Soroban inclusion fee P50/P90 in the feeTracker; back-to-back broadcasts reuse values.
	// Zero disables reuse (every inclusion-fee decision calls GetFeeStats).
	FeeStatsPollInterval *clconfig.Duration `toml:"FeeStatsPollInterval"`

	// Retry & timeout
	MaxSimulateAttempts       *uint              `toml:"MaxSimulateAttempts"`
	MaxSubmitRetryAttempts    *uint              `toml:"MaxSubmitRetryAttempts"`
	SubmitRetryDelay          *clconfig.Duration `toml:"SubmitRetryDelay"`
	TxTimeoutSecs             *int64             `toml:"TxTimeoutSecs"`
	LedgerBoundsOffset        *uint32            `toml:"LedgerBoundsOffset"`
	MaxTxRetryAttempts        *uint64            `toml:"MaxTxRetryAttempts"`
	MaxGetClientRetryAttempts *uint64            `toml:"MaxGetClientRetryAttempts"`
	MaxRestoreAttempts        *uint              `toml:"MaxRestoreAttempts"`

	// SimulationTerminalHints are matched case-insensitively as substrings
	// against failed SimulateTransaction errors; any match means the error is
	// treated as terminal (do not retry). Resolve merges these with built-in
	// defaults (additive); list only extra hints to add on top of defaults.
	SimulationTerminalHints []string `toml:"SimulationTerminalHints"`
	// SimulationRetryableHints: any substring match means retry simulation when
	// attempts remain. Resolve merges with built-in defaults (additive).
	SimulationRetryableHints []string `toml:"SimulationRetryableHints"`

	// Pruning
	// PruneInterval controls how often the background prune loop scans for
	// expired terminal txs. This is independent of PruneTxExpiration (retention).
	// Set to 0 to disable the loop (no goroutine is started); terminal txs are
	// then evicted synchronously when they reach a terminal state instead of
	// being retained until the next periodic prune.
	PruneInterval *clconfig.Duration `toml:"PruneInterval"`
	// PruneTxExpiration is the minimum time a terminal tx (Finalized or Failed)
	// is retained after reaching its terminal state before being eligible for pruning.
	// Measured from TerminalTime. Ignored when PruneInterval is 0 (immediate eviction).
	PruneTxExpiration *clconfig.Duration `toml:"PruneTxExpiration"`
}

// Resolve fills nil scalar fields with defaults from docs.toml (via Defaults),
// and always merges built-in simulation hints (additive with any user hints).
// After calling Resolve, scalar pointer fields are non-nil; simulation hint
// slices are non-empty.
func (c *TxManagerConfig) Resolve() {
	d := Defaults().TxManager
	if c.BroadcastChanSize == nil {
		c.BroadcastChanSize = ptr(*d.BroadcastChanSize)
	}
	if c.ConfirmPollInterval == nil {
		v := *d.ConfirmPollInterval
		c.ConfirmPollInterval = &v
	}
	if c.BaseInclusionFee == nil {
		c.BaseInclusionFee = ptr(*d.BaseInclusionFee)
	}
	if c.MaxInclusionFee == nil {
		c.MaxInclusionFee = ptr(*d.MaxInclusionFee)
	}
	if c.FeeBumpMultiplier == nil {
		c.FeeBumpMultiplier = ptr(*d.FeeBumpMultiplier)
	}
	if c.ResourceFeeBuffer == nil {
		c.ResourceFeeBuffer = ptr(*d.ResourceFeeBuffer)
	}
	if c.RestoreFeeBuffer == nil {
		c.RestoreFeeBuffer = ptr(*d.RestoreFeeBuffer)
	}
	if c.FeeStatsPollInterval == nil {
		v := *d.FeeStatsPollInterval
		c.FeeStatsPollInterval = &v
	}
	if c.MaxSimulateAttempts == nil {
		c.MaxSimulateAttempts = ptr(*d.MaxSimulateAttempts)
	}
	if c.MaxSubmitRetryAttempts == nil {
		c.MaxSubmitRetryAttempts = ptr(*d.MaxSubmitRetryAttempts)
	}
	if c.SubmitRetryDelay == nil {
		v := *d.SubmitRetryDelay
		c.SubmitRetryDelay = &v
	}
	if c.TxTimeoutSecs == nil {
		c.TxTimeoutSecs = ptr(*d.TxTimeoutSecs)
	}
	if c.LedgerBoundsOffset == nil {
		c.LedgerBoundsOffset = ptr(*d.LedgerBoundsOffset)
	}
	if c.MaxTxRetryAttempts == nil {
		c.MaxTxRetryAttempts = ptr(*d.MaxTxRetryAttempts)
	}
	if c.MaxGetClientRetryAttempts == nil {
		c.MaxGetClientRetryAttempts = ptr(*d.MaxGetClientRetryAttempts)
	}
	if c.MaxRestoreAttempts == nil {
		c.MaxRestoreAttempts = ptr(*d.MaxRestoreAttempts)
	}
	if c.PruneInterval == nil {
		v := *d.PruneInterval
		c.PruneInterval = &v
	}
	if c.PruneTxExpiration == nil {
		v := *d.PruneTxExpiration
		c.PruneTxExpiration = &v
	}
	c.SimulationTerminalHints = mergeSimulationHintLists(builtinSimulationTerminalHints, c.SimulationTerminalHints)
	c.SimulationRetryableHints = mergeSimulationHintLists(builtinSimulationRetryableHints, c.SimulationRetryableHints)
}

// mergeSimulationHintLists returns built-in hints followed by any extra hints
// from config not already present (additive, similar to EVM NodePool.Errors).
func mergeSimulationHintLists(builtin, extra []string) []string {
	if len(extra) == 0 {
		return append([]string(nil), builtin...)
	}
	seen := make(map[string]struct{}, len(builtin)+len(extra))
	out := make([]string, 0, len(builtin)+len(extra))
	for _, h := range builtin {
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	for _, h := range extra {
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	return out
}
