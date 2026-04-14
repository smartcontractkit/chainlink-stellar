package txm

import "time"

// Config holds TXM configuration parameters.
type Config struct {
	// MaxQueueSize is the max number of pending tx requests.
	MaxQueueSize int
	// ConfirmPollInterval is how often the confirmer checks tx status.
	ConfirmPollInterval time.Duration
	// TxTimeout is a wall-clock safety net for confirmation. It should
	// exceed the ledger-bounds window (LedgerBoundsOffset * ~5s/ledger)
	// so that ledger-based expiry is the primary mechanism.
	TxTimeout time.Duration
	// LedgerBoundsOffset is the default number of ledgers into the
	// future a tx is valid for. ~50 ledgers ≈ 5 min at 6s/ledger.
	LedgerBoundsOffset uint32

	// MaxRetries is the max lifecycle retries (Layer 3: confirm loop re-enqueue).
	MaxRetries int
	// MaxSubmitAttempts is the max HTTP submit retries per broadcast attempt (Layer 1).
	MaxSubmitAttempts int
	// SubmitRetryDelay is the pause between submit retries.
	SubmitRetryDelay time.Duration
	// MaxSimulateAttempts is the max simulation retries for sequence races (Layer 2).
	MaxSimulateAttempts int

	// FeeBuffer is the base inclusion fee added to simulation's MinResourceFee (stroops).
	FeeBuffer int64
	// FeeBumpMultiplier is the geometric multiplier applied per lifecycle retry attempt.
	FeeBumpMultiplier float64
	// MaxInclusionFee is the safety cap on the inclusion fee (stroops).
	MaxInclusionFee int64

	// AutoRestore controls automatic RestoreFootprint handling for
	// expired persistent ledger entries.
	AutoRestore bool

	// PruneThreshold is the age after which terminal txs are reaped.
	PruneThreshold time.Duration
	// PruneInterval is the minimum time between prune runs.
	PruneInterval time.Duration
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxQueueSize:        256,
		ConfirmPollInterval: 2 * time.Second,
		TxTimeout:           5 * time.Minute,
		LedgerBoundsOffset:  50, // ~5 min at 6s/ledger
		MaxRetries:          3,
		MaxSubmitAttempts:   5,
		SubmitRetryDelay:    2 * time.Second,
		MaxSimulateAttempts: 5,
		FeeBuffer:           10_000,
		FeeBumpMultiplier:   1.5,
		MaxInclusionFee:     1_000_000, // 0.1 XLM
		AutoRestore:         true,
		PruneThreshold:      5 * time.Minute,
		PruneInterval:       30 * time.Second,
	}
}
