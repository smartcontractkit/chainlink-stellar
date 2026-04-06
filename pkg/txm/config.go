package txm

import "time"

// Config holds TXM configuration parameters.
type Config struct {
	// MaxQueueSize is the max number of pending tx requests.
	MaxQueueSize int
	// ConfirmPollInterval is how often the confirmer checks tx status.
	ConfirmPollInterval time.Duration
	// TxTimeout is the max wall-clock time to wait for confirmation.
	TxTimeout time.Duration
	// LedgerBoundsOffset is the default number of ledgers into the
	// future a tx is valid for. ~50 ledgers ≈ 5 min at 6s/ledger.
	LedgerBoundsOffset uint32
	// MaxRetries for transient failures (TRY_AGAIN_LATER, sequence conflicts).
	MaxRetries int
	// FeeBuffer added to simulation's MinResourceFee (stroops).
	FeeBuffer int64
	// AutoRestore controls automatic RestoreFootprint handling for
	// expired persistent ledger entries.
	AutoRestore bool
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxQueueSize:        256,
		ConfirmPollInterval: 2 * time.Second,
		TxTimeout:           60 * time.Second,
		LedgerBoundsOffset:  50, // ~5 min at 6s/ledger
		MaxRetries:          3,
		FeeBuffer:           10_000,
		AutoRestore:         true,
	}
}
