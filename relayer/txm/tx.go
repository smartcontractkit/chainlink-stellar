package txm

import (
	"crypto/ed25519"
	"math/big"
	"sync"

	"github.com/stellar/go-stellar-sdk/txnbuild"

	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
)

// StellarTx represents a single transaction tracked by the TXM from enqueue to confirmation.
type StellarTx struct {
	ID          string
	Metadata    *commontypes.TxMeta
	Timestamp   uint64
	FromAddress string // G... strkey
	PublicKey   ed25519.PublicKey

	Operations         []txnbuild.Operation
	LedgerBoundsOffset uint32 // per-tx override (0 = use config default)

	Attempt        uint64
	Status         commontypes.TransactionStatus
	TxHash         string
	Fee            *big.Int // total fee in stroops; updated to actual FeeCharged on confirmation
	ResultXDR      string   // XDR-encoded transaction result from GetTransaction
	ResultCode     string   // result code from GetTransaction (for diagnostics)
	ResultMetaXDR  string   // XDR-encoded result meta from GetTransaction SUCCESS
	MaxLedger      uint32   // ledger bounds set during broadcast
	MinResourceFee int64    // from simulation result

	// Done is closed when the transaction reaches a terminal state.
	Done     chan struct{}
	doneOnce sync.Once
}

// TxRequest is the input accepted by Enqueue / EnqueueAndWait.
type TxRequest struct {
	ID                 string               // idempotency key (auto-generated if empty)
	FromAddress        string               // optional; defaults to TXM's signer address
	Operations         []txnbuild.Operation // the Stellar operations to execute
	LedgerBoundsOffset uint32               // per-tx override (0 = use config default)
	Metadata           *commontypes.TxMeta  // optional; carries WorkflowExecutionID and other node-level context
}

// TxResult is returned by EnqueueAndWait and Simulate with the outcome of a transaction.
type TxResult struct {
	ID            string
	Hash          string
	Status        commontypes.TransactionStatus
	Fee           *big.Int // total fee charged in stroops
	ResultXDR     string   // XDR-encoded transaction result from GetTransaction
	ResultMetaXDR string   // XDR-encoded result meta from GetTransaction
	Error         error
}

// Error reason constants classify broadcast and confirmation failures.
const (
	ErrorReasonSequenceNumber    = "sequence_number"
	ErrorReasonStoreCreate       = "store_create"
	ErrorReasonSimulation        = "simulation"
	ErrorReasonAssembly          = "assembly"
	ErrorReasonSigning           = "signing"
	ErrorReasonNoHash            = "no_hash"
	ErrorReasonStoreAdd          = "store_add"
	ErrorReasonUnknownSubmit     = "unknown_submit"
	ErrorReasonMaxRetries        = "max_retries"
	ErrorReasonRevert            = "revert"
	ErrorReasonTimedOut          = "timed_out"
	ErrorReasonBadSeq            = "bad_seq"
	ErrorReasonInsufficientBal   = "insufficient_balance"
	ErrorReasonRestoreFailed     = "restore_failed"
	ErrorReasonBadAuth           = "bad_auth"
	ErrorReasonTryAgainLater     = "try_again_later"
	ErrorReasonClientUnavailable = "client_unavailable"
	ErrorReasonInsufficientFee = "insufficient_fee"
	ErrorReasonInternalError = "internal_error"
)

// Drop reasons classify why a pending transaction was dropped from the broadcast queue.
const (
	// DropReasonChannelFullOldestEvicted: the oldest queued tx was evicted to make
	// room for a newer one. The oldest has the stalest simulation data and the
	// nearest LedgerBounds expiry, so the new tx's intent takes priority.
	DropReasonChannelFullOldestEvicted = "channel_full_oldest_evicted"

	// DropReasonChannelFullNewRejected: the incoming tx was rejected because the
	// channel was still full after an attempted oldest-evict (concurrent enqueue race).
	DropReasonChannelFullNewRejected = "channel_full_new_rejected"
)

// RetryReason classifies why a transaction is being retried (Layer 3 lifecycle retries).
type RetryReason int

const (
	RetryReasonResourceExhaustion RetryReason = iota
	RetryReasonTimedOut
	RetryReasonBadSeq
	RetryReasonTryAgainLater
	RetryReasonClientUnavailable
)
