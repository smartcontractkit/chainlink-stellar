package txm

import (
	"fmt"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// TxStatus represents the lifecycle state of a transaction.
type TxStatus int

const (
	TxStatusPending   TxStatus = iota // Queued, not yet broadcast
	TxStatusBroadcast                 // Submitted to the network, awaiting confirmation
	TxStatusConfirmed                 // Included in a ledger with SUCCESS
	TxStatusFailed                    // Included in a ledger with FAILED, or rejected
	TxStatusExpired                   // Ledger bounds exceeded without inclusion
)

func (s TxStatus) String() string {
	switch s {
	case TxStatusPending:
		return "PENDING"
	case TxStatusBroadcast:
		return "BROADCAST"
	case TxStatusConfirmed:
		return "CONFIRMED"
	case TxStatusFailed:
		return "FAILED"
	case TxStatusExpired:
		return "EXPIRED"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", int(s))
	}
}

// Terminal returns true if the status is a final state.
func (s TxStatus) Terminal() bool {
	return s == TxStatusConfirmed || s == TxStatusFailed || s == TxStatusExpired
}

// TxRequest is submitted to TxManager.Enqueue or EnqueueAndWait.
type TxRequest struct {
	// ID is an idempotency key. Must be unique per transaction.
	ID string
	// ContractID is the Stellar contract address (C… strkey).
	ContractID string
	// FunctionName is the Soroban function to invoke.
	FunctionName string
	// Args are the XDR-encoded arguments.
	Args []xdr.ScVal
	// LedgerBoundsOffset overrides Config.LedgerBoundsOffset for this tx.
	// Zero means use the config default.
	LedgerBoundsOffset uint32
	// SimulateOnly skips broadcast and returns the simulation result.
	SimulateOnly bool
}

// TxResult is the outcome of a completed (terminal) transaction.
type TxResult struct {
	Status     TxStatus
	Hash       string
	ResultMeta *xdr.TransactionMeta
	Error      error
	LedgerNum  uint32
}

// txEntry is the internal bookkeeping record for a transaction in the TxStore.
type txEntry struct {
	Request  TxRequest
	Status   TxStatus
	Hash     string
	Error    error
	Meta     *xdr.TransactionMeta
	Ledger   uint32
	MaxLedger uint32 // LedgerBounds.MaxLedger
	Retries  int
	Created  time.Time
	Updated  time.Time
	Seq      int64 // Stellar sequence number used
	Done     chan struct{}
}
