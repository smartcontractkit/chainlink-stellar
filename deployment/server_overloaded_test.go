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

// shortenOverloadBackoff swaps the overload retry backoff for a no-op wait,
// restoring it when the test ends.
func shortenOverloadBackoff(t *testing.T) {
	t.Helper()
	orig := serverOverloadedBackoff
	serverOverloadedBackoff = time.Nanosecond
	t.Cleanup(func() { serverOverloadedBackoff = orig })
}

// TestBuildAndSubmitTransaction_ServerOverloadedRetries reproduces the CI
// flake shape (TestBnmTokenPoolInbound, offramp WASM upload): the node refuses
// the first submissions outright with TRY_AGAIN_LATER. The refusal is
// transient — nothing about the ledger changed, the transaction was never
// executed — so the round must be retried until the queue drains and the
// submission is accepted.
func TestBuildAndSubmitTransaction_ServerOverloadedRetries(t *testing.T) {
	shortenOverloadBackoff(t)
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
			if sends.Add(1) <= 2 {
				return protocolrpc.SendTransactionResponse{Status: "TRY_AGAIN_LATER"}, nil
			}
			return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "hash-1"}, nil
		},
		GetTransactionFn: func(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
			return successGetTxResponse(t), nil
		},
	}

	d := newTestDeployer(t, mock)

	op := testInvokeOp(d.signer.Address())
	src, err := d.getSourceAccount(context.Background())
	require.NoError(t, err)

	meta, err := d.buildAndSubmitTransaction(context.Background(), src, op)
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.EqualValues(t, 3, sends.Load(), "two TRY_AGAIN_LATER refusals should be followed by a third accepted submission")
}

// TestBuildAndSubmitTransaction_ServerOverloadedRetriesExhausted: a node that
// never drains its queue fails after maxAttempts refusals with the original
// overload message, not a masking wrapper error.
func TestBuildAndSubmitTransaction_ServerOverloadedRetriesExhausted(t *testing.T) {
	shortenOverloadBackoff(t)
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
			return protocolrpc.SendTransactionResponse{Status: "TRY_AGAIN_LATER"}, nil
		},
	}

	d := newTestDeployer(t, mock)

	op := testInvokeOp(d.signer.Address())
	src, err := d.getSourceAccount(context.Background())
	require.NoError(t, err)

	_, err = d.buildAndSubmitTransaction(context.Background(), src, op)
	require.Error(t, err)
	assert.ErrorContains(t, err, "server overloaded, try again later")
	assert.EqualValues(t, 3, sends.Load(), "the refusal should be retried exactly maxAttempts times before giving up")
}
