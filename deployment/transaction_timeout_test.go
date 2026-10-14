package deployment

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortenTxnTimeBound swaps the transaction validity window for one just over
// a single confirmation-poll tick, restoring it when the test ends. The poll
// ticker is a hardcoded 1s, so this is the smallest window that still lets a
// healthy attempt confirm before the deadline expires.
func shortenTxnTimeBound(t *testing.T, d *Deployer) {
	t.Helper()
	orig := d.txnTimeBound
	d.txnTimeBound = 1100 * time.Millisecond
	t.Cleanup(func() { d.txnTimeBound = orig })
}

// TestBuildAndSubmitTransaction_TimedOutRetries reproduces the devenv
// first-bring-up flake (rmn-proxy initialize): the node accepts the
// transaction (PENDING) but a stalled ledger/queue never includes it before
// its MaxTime. Past the deadline the transaction can no longer be included
// and consumed nothing on-chain, so the round must be retried from a fresh
// simulation rather than failing the whole operation.
func TestBuildAndSubmitTransaction_TimedOutRetries(t *testing.T) {
	sorobanB64 := testSorobanDataB64(t)

	var sends atomic.Int32
	var polls atomic.Int32
	mock := &mockRPC{
		SimulateTransactionFn: func(_ context.Context, _ protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
			return protocolrpc.SimulateTransactionResponse{
				TransactionDataXDR: sorobanB64,
				MinResourceFee:     100000,
			}, nil
		},
		SendTransactionFn: func(_ context.Context, _ protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			sends.Add(1)
			return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "hash-1"}, nil
		},
		GetTransactionFn: func(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
			// The first attempt's transaction is never included; the retry's is.
			if polls.Add(1) == 1 {
				return protocolrpc.GetTransactionResponse{
					TransactionDetails: protocolrpc.TransactionDetails{Status: "NOT_FOUND"},
				}, nil
			}
			return successGetTxResponse(t), nil
		},
	}

	d := newTestDeployer(t, mock)
	shortenTxnTimeBound(t, d)

	op := testInvokeOp(d.signer.Address())
	src, err := d.getSourceAccount(context.Background())
	require.NoError(t, err)

	meta, err := d.buildAndSubmitTransaction(context.Background(), src, op)
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.EqualValues(t, 2, sends.Load(), "the timed-out submission should be followed by a second accepted one")
}

// TestBuildAndSubmitTransaction_TimedOutRetriesExhausted: a ledger that never
// includes the transaction fails after maxAttempts attempts with the original
// timeout message, not a masking wrapper error.
func TestBuildAndSubmitTransaction_TimedOutRetriesExhausted(t *testing.T) {
	sorobanB64 := testSorobanDataB64(t)

	var sends atomic.Int32
	mock := &mockRPC{
		SimulateTransactionFn: func(_ context.Context, _ protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
			return protocolrpc.SimulateTransactionResponse{
				TransactionDataXDR: sorobanB64,
				MinResourceFee:     100000,
			}, nil
		},
		SendTransactionFn: func(_ context.Context, _ protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			sends.Add(1)
			return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "hash-1"}, nil
		},
		GetTransactionFn: func(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
			return protocolrpc.GetTransactionResponse{
				TransactionDetails: protocolrpc.TransactionDetails{Status: "NOT_FOUND"},
			}, nil
		},
	}

	d := newTestDeployer(t, mock)
	shortenTxnTimeBound(t, d)

	op := testInvokeOp(d.signer.Address())
	src, err := d.getSourceAccount(context.Background())
	require.NoError(t, err)

	_, err = d.buildAndSubmitTransaction(context.Background(), src, op)
	require.Error(t, err)
	assert.ErrorContains(t, err, "transaction timed out")
	assert.EqualValues(t, 3, sends.Load(), "the timeout should be retried exactly maxAttempts times before giving up")
}
