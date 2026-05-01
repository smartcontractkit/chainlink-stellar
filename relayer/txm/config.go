package txm

import (
	"time"

	"github.com/smartcontractkit/chainlink-common/pkg/config"
)

func ptr[T any](v T) *T { return &v }

// Config defines the Stellar transaction manager configuration.
// Pointer fields are used for TOML deserialization — nil means "not set by user".
// After calling Resolve(), all fields are guaranteed non-nil.
type Config struct {
	BroadcastChanSize   *uint            `toml:"BroadcastChanSize"`
	ConfirmPollInterval *config.Duration `toml:"ConfirmPollInterval"`

	// Fee strategy: Stellar fees = InclusionFee + ResourceFee.
	// Only the inclusion fee is bumped on retries; the resource fee is deterministic from simulation.
	BaseInclusionFee  *int64   `toml:"BaseInclusionFee"`
	MaxInclusionFee   *int64   `toml:"MaxInclusionFee"`
	FeeBumpMultiplier *float64 `toml:"FeeBumpMultiplier"`
	ResourceFeeBuffer *int64   `toml:"ResourceFeeBuffer"`
	RestoreFeeBuffer  *int64   `toml:"RestoreFeeBuffer"`

	// Retry & timeout
	MaxSimulateAttempts    *uint            `toml:"MaxSimulateAttempts"`
	MaxSubmitRetryAttempts *uint            `toml:"MaxSubmitRetryAttempts"`
	SubmitRetryDelay       *config.Duration `toml:"SubmitRetryDelay"`
	TxTimeoutSecs          *int64           `toml:"TxTimeoutSecs"`
	LedgerBoundsOffset     *uint32          `toml:"LedgerBoundsOffset"`
	MaxTxRetryAttempts     *uint64          `toml:"MaxTxRetryAttempts"`
	MaxRestoreAttempts     *uint            `toml:"MaxRestoreAttempts"`

	// Pruning
	PruneInterval     *config.Duration `toml:"PruneInterval"`
	PruneTxExpiration *config.Duration `toml:"PruneTxExpiration"`
}

// DefaultConfigSet is the default configuration for the Stellar Transaction Manager.
var DefaultConfigSet = Config{
	BroadcastChanSize:   ptr(uint(100)),
	ConfirmPollInterval: config.MustNewDuration(3 * time.Second),

	BaseInclusionFee:  ptr(int64(100)),     // 100 stroops = MinBaseFee
	MaxInclusionFee:   ptr(int64(100_000)), // 0.01 XLM cap
	FeeBumpMultiplier: ptr(1.5),
	ResourceFeeBuffer: ptr(int64(15_000)), // ~15% buffer over MinResourceFee for typical txs
	RestoreFeeBuffer:  ptr(int64(10_000)),

	MaxSimulateAttempts:    ptr(uint(3)),
	MaxSubmitRetryAttempts: ptr(uint(10)),
	SubmitRetryDelay:       config.MustNewDuration(3 * time.Second),
	TxTimeoutSecs:          ptr(int64(300)),  // 5 minutes wall-clock fallback
	LedgerBoundsOffset:     ptr(uint32(50)),  // ~5 min at 6s/ledger
	MaxTxRetryAttempts:     ptr(uint64(5)),
	MaxRestoreAttempts:     ptr(uint(3)),

	PruneInterval:     config.MustNewDuration(2 * time.Hour),
	PruneTxExpiration: config.MustNewDuration(2 * time.Hour),
}

// Resolve fills nil fields with defaults from DefaultConfigSet.
// After calling Resolve, all fields are guaranteed non-nil.
func (c *Config) Resolve() {
	if c.BroadcastChanSize == nil {
		c.BroadcastChanSize = ptr(*DefaultConfigSet.BroadcastChanSize)
	}
	if c.ConfirmPollInterval == nil {
		v := *DefaultConfigSet.ConfirmPollInterval
		c.ConfirmPollInterval = &v
	}
	if c.BaseInclusionFee == nil {
		c.BaseInclusionFee = ptr(*DefaultConfigSet.BaseInclusionFee)
	}
	if c.MaxInclusionFee == nil {
		c.MaxInclusionFee = ptr(*DefaultConfigSet.MaxInclusionFee)
	}
	if c.FeeBumpMultiplier == nil {
		c.FeeBumpMultiplier = ptr(*DefaultConfigSet.FeeBumpMultiplier)
	}
	if c.ResourceFeeBuffer == nil {
		c.ResourceFeeBuffer = ptr(*DefaultConfigSet.ResourceFeeBuffer)
	}
	if c.RestoreFeeBuffer == nil {
		c.RestoreFeeBuffer = ptr(*DefaultConfigSet.RestoreFeeBuffer)
	}
	if c.MaxSimulateAttempts == nil {
		c.MaxSimulateAttempts = ptr(*DefaultConfigSet.MaxSimulateAttempts)
	}
	if c.MaxSubmitRetryAttempts == nil {
		c.MaxSubmitRetryAttempts = ptr(*DefaultConfigSet.MaxSubmitRetryAttempts)
	}
	if c.SubmitRetryDelay == nil {
		v := *DefaultConfigSet.SubmitRetryDelay
		c.SubmitRetryDelay = &v
	}
	if c.TxTimeoutSecs == nil {
		c.TxTimeoutSecs = ptr(*DefaultConfigSet.TxTimeoutSecs)
	}
	if c.LedgerBoundsOffset == nil {
		c.LedgerBoundsOffset = ptr(*DefaultConfigSet.LedgerBoundsOffset)
	}
	if c.MaxTxRetryAttempts == nil {
		c.MaxTxRetryAttempts = ptr(*DefaultConfigSet.MaxTxRetryAttempts)
	}
	if c.MaxRestoreAttempts == nil {
		c.MaxRestoreAttempts = ptr(*DefaultConfigSet.MaxRestoreAttempts)
	}
	if c.PruneInterval == nil {
		v := *DefaultConfigSet.PruneInterval
		c.PruneInterval = &v
	}
	if c.PruneTxExpiration == nil {
		v := *DefaultConfigSet.PruneTxExpiration
		c.PruneTxExpiration = &v
	}
}
