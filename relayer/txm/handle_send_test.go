package txm

import (
	"context"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
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
	otherXDR, err := xdr.MarshalBase64(xdr.TransactionResult{
		Result: xdr.TransactionResultResult{
			Code: xdr.TransactionResultCodeTxNoAccount,
		},
	})
	require.NoError(t, err)

	tx := &StellarTx{ID: "x", FromAddress: testAddress}

	t.Run("PENDING", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "a1"}, 1, store, 9)
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
		acc, fatal, reason := s.handleSendResult(ctx, retriedTx, protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "a1"}, 1, store, 9)
		require.True(t, acc)
		require.False(t, fatal)
		require.Equal(t, "", reason)
		assert.Empty(t, retriedTx.ResultXDR)
		assert.Empty(t, retriedTx.ResultMetaXDR)
		assert.Empty(t, retriedTx.ResultCode)
	})

	t.Run("DUPLICATE", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: "DUPLICATE", Hash: "a2"}, 1, store, 9)
		require.True(t, acc)
		require.False(t, fatal)
		require.Equal(t, "", reason)
	})

	t.Run("PENDING without hash is fatal", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: "PENDING"}, 1, store, 9)
		require.False(t, acc)
		require.True(t, fatal)
		assert.Equal(t, ErrorReasonNoHash, reason)
		assert.Equal(t, 0, store.InflightCount())
	})

	t.Run("DUPLICATE without hash is fatal", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: "DUPLICATE"}, 1, store, 9)
		require.False(t, acc)
		require.True(t, fatal)
		assert.Equal(t, ErrorReasonNoHash, reason)
		assert.Equal(t, 0, store.InflightCount())
	})

	t.Run("TRY_AGAIN_LATER", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: "TRY_AGAIN_LATER"}, 1, store, 9)
		require.False(t, acc)
		require.False(t, fatal)
		require.Equal(t, ErrorReasonTryAgainLater, reason)
	})

	t.Run("ERROR bad_seq", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: "ERROR", ErrorResultXDR: badSeqXDR}, 1, store, 9)
		require.False(t, acc)
		require.False(t, fatal)
		require.Equal(t, ErrorReasonBadSeq, reason)
	})

	t.Run("ERROR insufficient balance", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: "ERROR", ErrorResultXDR: insuffXDR}, 1, store, 9)
		require.False(t, acc)
		require.True(t, fatal)
		assert.Equal(t, xdr.TransactionResultCodeTxInsufficientBalance.String(), reason)
	})

	t.Run("ERROR bad auth", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: "ERROR", ErrorResultXDR: badAuthXDR}, 1, store, 9)
		require.False(t, acc)
		require.True(t, fatal)
		assert.Equal(t, xdr.TransactionResultCodeTxBadAuth.String(), reason)
	})

	t.Run("ERROR other retryable", func(t *testing.T) {
		t.Parallel()
		store := NewTxStore(1)
		acc, fatal, reason := s.handleSendResult(ctx, tx, protocolrpc.SendTransactionResponse{Status: "ERROR", ErrorResultXDR: otherXDR}, 1, store, 9)
		require.False(t, acc)
		require.False(t, fatal)
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
		acc, fatal, reason := s.handleSendResult(ctx, &StellarTx{ID: "second"}, protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "h1"}, 1, store, 9)
		require.False(t, acc)
		require.True(t, fatal)
		assert.Equal(t, ErrorReasonStoreAdd, reason)
	})
}
