package txm

import (
	"context"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
)

func TestStellarTxm_classifyErrorResult(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	s, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test", "")
	require.NoError(t, err)

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "unknown_error", s.classifyErrorResult(""))
	})

	t.Run("invalid base64", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "decode_error", s.classifyErrorResult("not-valid-xdr-!!!"))
	})

	t.Run("tx_bad_seq", func(t *testing.T) {
		t.Parallel()
		b64, err := xdr.MarshalBase64(xdr.TransactionResult{
			Result: xdr.TransactionResultResult{
				Code: xdr.TransactionResultCodeTxBadSeq,
			},
		})
		require.NoError(t, err)
		assert.Equal(t, xdr.TransactionResultCodeTxBadSeq.String(), s.classifyErrorResult(b64))
	})
}

func TestStellarTxm_handleSendResult(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	s, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test", "")
	require.NoError(t, err)
	ctx := context.Background()

	badSeqXDR, err := xdr.MarshalBase64(xdr.TransactionResult{
		Result: xdr.TransactionResultResult{
			Code: xdr.TransactionResultCodeTxBadSeq,
		},
	})
	require.NoError(t, err)
	insuffXDR, err := xdr.MarshalBase64(xdr.TransactionResult{
		Result: xdr.TransactionResultResult{
			Code: xdr.TransactionResultCodeTxInsufficientBalance,
		},
	})
	require.NoError(t, err)
	badAuthXDR, err := xdr.MarshalBase64(xdr.TransactionResult{
		Result: xdr.TransactionResultResult{
			Code: xdr.TransactionResultCodeTxBadAuth,
		},
	})
	require.NoError(t, err)
	noAccountXDR, err := xdr.MarshalBase64(xdr.TransactionResult{
		Result: xdr.TransactionResultResult{
			Code: xdr.TransactionResultCodeTxNoAccount,
		},
	})
	require.NoError(t, err)

	tx := &StellarTx{ID: "x", FromAddress: testAddress}

	t.Run(stellarcore.TXStatusPending, func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "a1"}, 1, store, 9)
		require.True(t, acc)
		require.False(t, fatal)
		require.Equal(t, "", reason)
	})

	t.Run("PENDING clears stale previous result fields", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		retriedTx := &StellarTx{
			ID:            "retried",
			FromAddress:   testAddress,
			ResultXDR:     "old-result",
			ResultMetaXDR: "old-meta",
			ResultCode:    "old-code",
		}
		acc, fatal, reason := s.handleSendResult(ctx, retriedTx, protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "a1"}, 1, store, 9)
		require.True(t, acc)
		require.False(t, fatal)
		require.Equal(t, "", reason)
		assert.Empty(t, retriedTx.ResultXDR)
		assert.Empty(t, retriedTx.ResultMetaXDR)
		assert.Empty(t, retriedTx.ResultCode)
	})

	t.Run(stellarcore.TXStatusDuplicate, func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusDuplicate, Hash: "a2"}, 1, store, 9)
		require.True(t, acc)
		require.False(t, fatal)
		require.Equal(t, "", reason)
	})

	t.Run("PENDING without hash is fatal", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending}, 1, store, 9)
		require.False(t, acc)
		require.True(t, fatal)
		assert.Equal(t, ErrorReasonNoHash, reason)
		assert.Equal(t, 0, store.InflightCount())
	})

	t.Run("DUPLICATE without hash is fatal", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusDuplicate}, 1, store, 9)
		require.False(t, acc)
		require.True(t, fatal)
		assert.Equal(t, ErrorReasonNoHash, reason)
		assert.Equal(t, 0, store.InflightCount())
	})

	t.Run(stellarcore.TXStatusTryAgainLater, func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusTryAgainLater}, 1, store, 9)
		require.False(t, acc)
		require.False(t, fatal)
		require.Equal(t, ErrorReasonTryAgainLater, reason)
	})

	t.Run("ERROR bad_seq", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusError, ErrorResultXDR: badSeqXDR}, 1, store, 9)
		require.False(t, acc)
		require.False(t, fatal)
		require.Equal(t, ErrorReasonBadSeq, reason)
	})

	t.Run("ERROR insufficient balance", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusError, ErrorResultXDR: insuffXDR}, 1, store, 9)
		require.False(t, acc)
		require.True(t, fatal)
		assert.Equal(t, xdr.TransactionResultCodeTxInsufficientBalance.String(), reason)
	})

	t.Run("ERROR bad auth", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusError, ErrorResultXDR: badAuthXDR}, 1, store, 9)
		require.False(t, acc)
		require.True(t, fatal)
		assert.Equal(t, xdr.TransactionResultCodeTxBadAuth.String(), reason)
	})

	// Regression: tx_no_account must be FATAL. The pre-fix default branch
	// silently retried any unrecognized code, including this one — see the
	// Stellar transaction error spec for the canonical classification.
	t.Run("ERROR tx_no_account is fatal", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusError, ErrorResultXDR: noAccountXDR}, 1, store, 9)
		require.False(t, acc)
		require.True(t, fatal)
		assert.Equal(t, xdr.TransactionResultCodeTxNoAccount.String(), reason)
	})

	t.Run("unknown status", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: "WEIRD"}, 1, store, 9)
		require.False(t, acc)
		require.True(t, fatal)
		assert.Equal(t, ErrorReasonUnknownSubmit, reason)
	})

	t.Run("AddUnconfirmed conflict is fatal", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		first := &StellarTx{ID: "first"}
		require.NoError(t, store.AddUnconfirmed(1, "h0", 9, first))
		acc, fatal, reason := s.handleSendResult(ctx, &StellarTx{ID: "second"}, protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "h1"}, 1, store, 9)
		require.False(t, acc)
		require.True(t, fatal)
		assert.Equal(t, ErrorReasonStoreAdd, reason)
	})
}

// TestStellarTxm_classifySubmitErrorCode pins the submit-time retry policy
// against every Stellar TransactionResultCode known to the SDK. The default
// is fatal; only an explicit allowlist (BadSeq / InsufficientFee /
// InternalError) is retryable. See the Stellar transaction error spec:
// https://developers.stellar.org/docs/data/apis/horizon/api-reference/errors/result-codes/transactions
//
// This test guards against the previous bug where the ERROR branch returned
// fatal=false in its default case, silently retrying genuine fatal codes
// (e.g. tx_no_account, tx_too_late, tx_malformed).
func TestStellarTxm_classifySubmitErrorCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		code       xdr.TransactionResultCode
		wantFatal  bool
		wantReason string
	}{
		// Retryable allowlist.
		{"tx_bad_seq is retryable (resync)", xdr.TransactionResultCodeTxBadSeq, false, ErrorReasonBadSeq},
		{"tx_insufficient_fee is retryable (bump fee)", xdr.TransactionResultCodeTxInsufficientFee, false, ErrorReasonInsufficientFee},
		{"tx_internal_error is retryable (transient)", xdr.TransactionResultCodeTxInternalError, false, ErrorReasonInternalError},

		// Fatal — every other documented code.
		{"tx_no_account is fatal", xdr.TransactionResultCodeTxNoAccount, true, xdr.TransactionResultCodeTxNoAccount.String()},
		{"tx_insufficient_balance is fatal", xdr.TransactionResultCodeTxInsufficientBalance, true, xdr.TransactionResultCodeTxInsufficientBalance.String()},
		{"tx_bad_auth is fatal", xdr.TransactionResultCodeTxBadAuth, true, xdr.TransactionResultCodeTxBadAuth.String()},
		{"tx_bad_auth_extra is fatal", xdr.TransactionResultCodeTxBadAuthExtra, true, xdr.TransactionResultCodeTxBadAuthExtra.String()},
		{"tx_too_early is fatal", xdr.TransactionResultCodeTxTooEarly, true, xdr.TransactionResultCodeTxTooEarly.String()},
		{"tx_too_late is fatal", xdr.TransactionResultCodeTxTooLate, true, xdr.TransactionResultCodeTxTooLate.String()},
		{"tx_missing_operation is fatal", xdr.TransactionResultCodeTxMissingOperation, true, xdr.TransactionResultCodeTxMissingOperation.String()},
		{"tx_not_supported is fatal", xdr.TransactionResultCodeTxNotSupported, true, xdr.TransactionResultCodeTxNotSupported.String()},
		{"tx_fee_bump_inner_failed is fatal", xdr.TransactionResultCodeTxFeeBumpInnerFailed, true, xdr.TransactionResultCodeTxFeeBumpInnerFailed.String()},
		{"tx_bad_sponsorship is fatal", xdr.TransactionResultCodeTxBadSponsorship, true, xdr.TransactionResultCodeTxBadSponsorship.String()},
		{"tx_bad_min_seq_age_or_gap is fatal", xdr.TransactionResultCodeTxBadMinSeqAgeOrGap, true, xdr.TransactionResultCodeTxBadMinSeqAgeOrGap.String()},
		{"tx_malformed is fatal", xdr.TransactionResultCodeTxMalformed, true, xdr.TransactionResultCodeTxMalformed.String()},
		{"tx_soroban_invalid is fatal", xdr.TransactionResultCodeTxSorobanInvalid, true, xdr.TransactionResultCodeTxSorobanInvalid.String()},
		{"tx_frozen_key_accessed is fatal", xdr.TransactionResultCodeTxFrozenKeyAccessed, true, xdr.TransactionResultCodeTxFrozenKeyAccessed.String()},
		{"tx_failed (generic op-level container) is fatal at submit", xdr.TransactionResultCodeTxFailed, true, xdr.TransactionResultCodeTxFailed.String()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			accepted, fatal, reason := classifySubmitErrorCode(tt.code, tt.code.String())
			assert.False(t, accepted, "ERROR responses are never accepted")
			assert.Equal(t, tt.wantFatal, fatal, "fatal flag for %s", tt.code.String())
			assert.Equal(t, tt.wantReason, reason, "reason label for %s", tt.code.String())
		})
	}
}

// Regression: when ErrorResultXDR is missing or undecodable the classifier
// must fail-closed (return fatal). Without this guard a misbehaving RPC could
// drive infinite retries by emitting garbage in ErrorResultXDR.
func TestStellarTxm_handleSendResult_UndecodableErrorXDRIsFatal(t *testing.T) {
	t.Parallel()

	mock := &mockRPCClient{}
	s, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test", "")
	require.NoError(t, err)
	ctx := context.Background()
	tx := &StellarTx{ID: "x", FromAddress: testAddress}

	t.Run("empty ErrorResultXDR is fatal", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusError, ErrorResultXDR: ""}, 1, store, 9)
		assert.False(t, acc)
		assert.True(t, fatal)
		assert.Equal(t, "unknown_error", reason)
	})

	t.Run("undecodable ErrorResultXDR is fatal", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusError, ErrorResultXDR: "not-valid-base64-xdr-!!!"}, 1, store, 9)
		assert.False(t, acc)
		assert.True(t, fatal)
		assert.Equal(t, "decode_error", reason)
	})
}

// Regression: tx_insufficient_fee must surface ErrorReasonInsufficientFee, the
// distinct reason label that the broadcast loop dispatches through the
// fee-bump path (alongside TRY_AGAIN_LATER). Mislabelling it as a generic
// retry would cause a backoff retry without bumping, which would just hit the
// same minimum-fee rejection again.
func TestStellarTxm_handleSendResult_InsufficientFeeMapsToFeeBumpReason(t *testing.T) {
	t.Parallel()

	mock := &mockRPCClient{}
	s, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test", "")
	require.NoError(t, err)
	ctx := context.Background()
	tx := &StellarTx{ID: "x", FromAddress: testAddress}

	insuffFeeXDR, err := xdr.MarshalBase64(xdr.TransactionResult{
		Result: xdr.TransactionResultResult{
			Code: xdr.TransactionResultCodeTxInsufficientFee,
		},
	})
	require.NoError(t, err)

	store := NewTxStore(1)
	acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusError, ErrorResultXDR: insuffFeeXDR}, 1, store, 9)
	assert.False(t, acc)
	assert.False(t, fatal)
	assert.Equal(t, ErrorReasonInsufficientFee, reason)
}
