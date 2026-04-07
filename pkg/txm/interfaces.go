package txm

import (
	"context"
	"math/big"
)

// TxManager is the primary interface for submitting and tracking
// Stellar/Soroban transactions. Consumers (InvokerAdapter, Deployer, tests)
// call Enqueue or EnqueueAndWait; the TXM handles simulation, signing,
// broadcast, confirmation, retry, and expiry internally.
type TxManager interface {
	// Start initialises background goroutines (broadcaster, confirmer).
	Start(ctx context.Context) error
	// Close shuts down gracefully, waiting for in-flight work.
	Close() error
	// Ready returns nil once the TXM is ready to accept work.
	Ready() error
	// HealthReport returns a map of component → error for monitoring.
	HealthReport() map[string]error
	// Name returns a human-readable service name.
	Name() string

	// Enqueue submits a transaction request asynchronously.
	// Returns the transaction ID (auto-generated if TxRequest.ID is empty).
	Enqueue(ctx context.Context, req TxRequest) (string, error)

	// EnqueueAndWait submits a request and blocks until it reaches a
	// terminal state (confirmed/failed/expired).
	EnqueueAndWait(ctx context.Context, req TxRequest) (*TxResult, error)

	// GetTransactionStatus returns the current status of a transaction
	// identified by its idempotency key.
	GetTransactionStatus(ctx context.Context, txID string) (TxStatus, error)

	// GetTransactionResult returns the full result of a terminal transaction
	// including the on-chain hash, status, and result XDR.
	GetTransactionResult(ctx context.Context, txID string) (*TxResult, error)

	// GetTransactionFee returns the fee charged for a confirmed transaction
	// in stroops.
	GetTransactionFee(ctx context.Context, txID string) (*big.Int, error)

	// InflightCount returns the enqueue channel depth and total unconfirmed
	// transaction count.
	InflightCount() (channelDepth int, totalUnconfirmed int)
}
