package txm

import "errors"

var (
	ErrQueueFull   = errors.New("txm: enqueue queue is full")
	ErrDuplicateTx = errors.New("txm: duplicate transaction ID")
	ErrTxNotFound  = errors.New("txm: transaction not found")
	ErrTxExpired   = errors.New("txm: transaction expired (ledger bounds exceeded)")
	ErrTxFailed    = errors.New("txm: transaction failed on-chain")
	ErrNotStarted  = errors.New("txm: not started")
	ErrAlreadyStop = errors.New("txm: already stopped")
	ErrSimulation  = errors.New("txm: simulation failed")
	ErrSequence    = errors.New("txm: sequence number conflict")
	ErrOverloaded  = errors.New("txm: server overloaded (TRY_AGAIN_LATER)")
)
