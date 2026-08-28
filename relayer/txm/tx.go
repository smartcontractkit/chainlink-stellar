package txm

import (
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stellar/go-stellar-sdk/txnbuild"

	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
)

// StellarTx represents a single transaction tracked by the TXM from enqueue to confirmation.
// ID/Metadata/Timestamp/FromAddress/Operations/LedgerBoundsOffset are immutable after
// enqueue (safe to read without a lock). mu guards the mutable fields below it.
type StellarTx struct {
	ID          string
	Metadata    *commontypes.TxMeta
	Timestamp   time.Time // when the tx was enqueued; zero if unset
	FromAddress string // G... strkey: source account and signer for this TXM

	Operations         []txnbuild.Operation
	LedgerBoundsOffset uint32 // per-tx override (0 = use config default)
	MaxResourceFee uint64

	Attempt       atomic.Uint64
	InfraAttempts atomic.Uint64

	mu              sync.RWMutex
	Status          commontypes.TransactionStatus
	TerminalTime    time.Time // when status first became Finalized or Failed; zero if not yet terminal
	BroadcastAt     time.Time // set when SendTransaction accepts the tx
	TxHash          string
	Fee             *big.Int // total fee in stroops; updated to actual FeeCharged on confirmation
	LedgerCloseTime int64    // unix seconds when tx was included in a ledger; from GetTransaction
	ResultXDR       string   // XDR-encoded transaction result from GetTransaction
	ResultCode      string   // result code from GetTransaction (for diagnostics)
	ResultMetaXDR   string   // XDR-encoded result meta from GetTransaction SUCCESS
	MaxLedger       uint32   // broadcast-loop-owned; ledger bounds set during broadcast
	MinResourceFee  int64    // broadcast-loop-owned; from simulation result

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
	MaxResourceFee uint64 // optional; per-tx override (0 = use config default)
}

// TxResult is returned by EnqueueAndWait and Simulate with the outcome of a transaction.
type TxResult struct {
	ID              string
	Hash            string
	Status          commontypes.TransactionStatus
	Fee             *big.Int // total fee charged in stroops
	LedgerCloseTime int64    // unix seconds when tx was included in a ledger; 0 if unknown
	ResultXDR       string   // XDR-encoded transaction result from GetTransaction
	ResultMetaXDR   string   // XDR-encoded result meta from GetTransaction
	Error           error
}

// ErrorReason is a bounded label classifying broadcast and confirmation failures.
type ErrorReason string

const (
	ErrorReasonSequenceNumber    ErrorReason = "sequence_number"
	ErrorReasonStoreCreate       ErrorReason = "store_create"
	ErrorReasonSimulation        ErrorReason = "simulation"
	ErrorReasonAssembly          ErrorReason = "assembly"
	ErrorReasonSigning           ErrorReason = "signing"
	ErrorReasonNoHash            ErrorReason = "no_hash"
	ErrorReasonStoreAdd          ErrorReason = "store_add"
	ErrorReasonUnknownSubmit     ErrorReason = "unknown_submit"
	ErrorReasonMaxRetries        ErrorReason = "max_retries"
	ErrorReasonRevert            ErrorReason = "revert"
	ErrorReasonRevertDecode      ErrorReason = ErrorReasonRevert + "_decode_error"
	ErrorReasonTimedOut          ErrorReason = "timed_out"
	ErrorReasonBadSeq            ErrorReason = "bad_seq"
	ErrorReasonRestoreFailed     ErrorReason = "restore_failed"
	ErrorReasonTryAgainLater     ErrorReason = "try_again_later"
	ErrorReasonClientUnavailable ErrorReason = "client_unavailable"
	ErrorReasonInsufficientFee   ErrorReason = "insufficient_fee"
	ErrorReasonInternalError     ErrorReason = "internal_error"
	ErrorReasonNilTx             ErrorReason = "nil_tx"
	ErrorReasonNilTxStore        ErrorReason = "nil_tx_store"
	// ErrorReasonSubmitErrorUndecoded means the node returned TXStatusError but
	// ErrorResultXDR was empty or not valid transaction-result XDR.
	ErrorReasonSubmitErrorUndecoded ErrorReason = "submit_error_undecoded"
)

// DropReason is a bounded label classifying why a transaction was dropped from
// the broadcast queue.
type DropReason string

const (
	// DropReasonChannelFullOldestEvicted: the oldest queued tx was evicted to make
	// room for a newer one. The oldest has the stalest simulation data and the
	// nearest LedgerBounds expiry, so the new tx's intent takes priority.
	DropReasonChannelFullOldestEvicted DropReason = "channel_full_oldest_evicted"

	// DropReasonChannelFullNewRejected: the incoming tx was rejected because the
	// channel was still full after an attempted oldest-evict (concurrent enqueue race).
	DropReasonChannelFullNewRejected DropReason = "channel_full_new_rejected"
)

// RetryReason classifies why a transaction is being retried (post-submit lifecycle retries).
type RetryReason string

const (
	RetryReasonResourceExhaustion RetryReason = "resource_exhaustion"
	RetryReasonTimedOut           RetryReason = "timed_out"
)

// RetryBudget identifies which retry budget a tx exhausted before failing.
// This distinguishes retry-exhausted failures from first-attempt non-retryable
// failures in the max_attempts_reached metric.
type RetryBudget string

const (
	// RetryBudgetLifecycle: the tx exhausted MaxTxRetryAttempts after repeated
	// post-submit lifecycle failures (on-chain resource exhaustion or ledger timeout).
	RetryBudgetLifecycle RetryBudget = "lifecycle"
	// RetryBudgetInfra: the tx exhausted MaxGetClientRetryAttempts after repeated
	// getClient (multinode RPC selection) failures before it could be simulated or submitted.
	RetryBudgetInfra RetryBudget = "infra"
)

// RestoreOutcome classifies RestoreFootprint lifecycle events recorded by TXM metrics.
type RestoreOutcome string

const (
	RestoreOutcomeInitiated RestoreOutcome = "initiated"
	RestoreOutcomeSuccess   RestoreOutcome = "success"
	RestoreOutcomeFailed    RestoreOutcome = "failed"
)
