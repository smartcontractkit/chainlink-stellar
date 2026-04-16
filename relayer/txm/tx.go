package txm

import (
	"crypto/ed25519"
	"math/big"

	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
)

// StellarTx represents a single transaction tracked by the TXM from enqueue to confirmation.
type StellarTx struct {
	ID          string
	Metadata    *commontypes.TxMeta
	Timestamp   uint64
	FromAddress string           // G... strkey
	PublicKey   ed25519.PublicKey

	// Pre-built operation (when the caller provides one directly).
	Operation txnbuild.Operation

	// Contract invocation fields (used when TxRequest specifies contract/function directly).
	ContractID   string      // C… strkey
	FunctionName string
	Args         []xdr.ScVal

	Attempt    uint64
	Status     commontypes.TransactionStatus
	TxHash     string
	Fee        *big.Int // total fee in stroops
	ResultCode string   // result code from GetTransaction (for diagnostics)

	// Done is closed when the transaction reaches a terminal state.
	// Used by EnqueueAndWait to block until completion.
	Done chan struct{}
}

// TxRequest is the input accepted by Enqueue / EnqueueAndWait / Simulate.
type TxRequest struct {
	ID               string // idempotency key (auto-generated if empty)
	FromAddress      string // optional; defaults to TXM's signer address
	ContractID       string // C… strkey of the Soroban contract
	FunctionName     string
	Args             []xdr.ScVal
	LedgerBoundsOffset uint32 // per-tx override (0 = use config default)
	SimulateOnly     bool   // if true, simulate without broadcasting
}

// TxResult is returned by EnqueueAndWait and Simulate with the outcome of a transaction.
type TxResult struct {
	ID            string
	Hash          string
	Status        commontypes.TransactionStatus
	Fee           *big.Int // total fee charged in stroops
	ResultMetaXDR string   // XDR-encoded result meta from GetTransaction
	Error         error
}

// Error reason constants classify broadcast and confirmation failures.
const (
	ErrorReasonSequenceNumber  = "sequence_number"
	ErrorReasonStoreCreate     = "store_create"
	ErrorReasonSimulation      = "simulation"
	ErrorReasonAssembly        = "assembly"
	ErrorReasonSigning         = "signing"
	ErrorReasonNoHash          = "no_hash"
	ErrorReasonStoreAdd        = "store_add"
	ErrorReasonUnknownSubmit   = "unknown_submit"
	ErrorReasonMaxRetries      = "max_retries"
	ErrorReasonRevert          = "revert"
	ErrorReasonTimedOut        = "timed_out"
	ErrorReasonBadSeq          = "bad_seq"
	ErrorReasonInsufficientBal = "insufficient_balance"
	ErrorReasonRestoreFailed   = "restore_failed"
	ErrorReasonBadAuth         = "bad_auth"
	ErrorReasonTryAgainLater   = "try_again_later"
)

// RetryReason classifies why a transaction is being retried (Layer 3 lifecycle retries).
type RetryReason int

const (
	RetryReasonResourceExhaustion RetryReason = iota
	RetryReasonTimedOut
	RetryReasonBadSeq
	RetryReasonTryAgainLater
)
