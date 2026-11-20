package deployment

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rejectedResultXDR builds a base64 transaction result rejected with the
// given code before execution (no operation results).
func rejectedResultXDR(t *testing.T, code xdr.TransactionResultCode) string {
	t.Helper()
	res := xdr.TransactionResult{
		Result: xdr.TransactionResultResult{Code: code},
	}
	b64, err := xdr.MarshalBase64(res)
	require.NoError(t, err)
	return b64
}

// TestBuildAndSubmitTransaction_BadSeqRetries covers NONEVM-6662's failure
// mode (a): a submission rejected with txBadSeq never executed, so the round
// must re-fetch the on-chain sequence and resubmit instead of failing hard.
// In-process writers are serialized by submitMu, so in practice this path is
// the safety net for an out-of-process writer consuming a sequence.
func TestBuildAndSubmitTransaction_BadSeqRetries(t *testing.T) {
	sorobanB64 := testSorobanDataB64(t)
	badSeqXDR := rejectedResultXDR(t, xdr.TransactionResultCodeTxBadSeq)

	var sends atomic.Int32
	var ledgerReads atomic.Int32
	mock := &mockRPC{
		SimulateTransactionFn: func(_ context.Context, _ protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
			return protocolrpc.SimulateTransactionResponse{
				TransactionDataXDR: sorobanB64,
				MinResourceFee:     100000,
			}, nil
		},
		SendTransactionFn: func(_ context.Context, _ protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			if sends.Add(1) == 1 {
				return protocolrpc.SendTransactionResponse{
					Status:         "ERROR",
					ErrorResultXDR: badSeqXDR,
				}, nil
			}
			return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "hash-1"}, nil
		},
		GetTransactionFn: func(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
			return successGetTxResponse(t), nil
		},
	}

	d := newTestDeployer(t, mock)
	// Wrap the default account-entry mock to count re-fetches: a txBadSeq
	// retry must re-read the sequence from the network before resubmitting.
	base := mock.GetLedgerEntriesFn
	mock.GetLedgerEntriesFn = func(ctx context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
		ledgerReads.Add(1)
		return base(ctx, req)
	}

	op := testInvokeOp(d.signer.Address())
	src, err := d.getSourceAccount(context.Background())
	require.NoError(t, err)

	meta, _, err := d.buildAndSubmitTransaction(context.Background(), src, op)
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.EqualValues(t, 2, sends.Load(), "the txBadSeq rejection should be followed by a second accepted submission")
	assert.GreaterOrEqual(t, ledgerReads.Load(), int32(2), "the retry must re-fetch the on-chain sequence before resubmitting")
}

// TestBuildAndSubmitTransaction_BadSeqRetriesExhausted: a sequence that keeps
// moving under us fails after maxAttempts with the txBadSeq message, not a
// masking wrapper error.
func TestBuildAndSubmitTransaction_BadSeqRetriesExhausted(t *testing.T) {
	sorobanB64 := testSorobanDataB64(t)
	badSeqXDR := rejectedResultXDR(t, xdr.TransactionResultCodeTxBadSeq)

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
			return protocolrpc.SendTransactionResponse{
				Status:         "ERROR",
				ErrorResultXDR: badSeqXDR,
			}, nil
		},
	}

	d := newTestDeployer(t, mock)

	op := testInvokeOp(d.signer.Address())
	src, err := d.getSourceAccount(context.Background())
	require.NoError(t, err)

	_, _, err = d.buildAndSubmitTransaction(context.Background(), src, op)
	require.Error(t, err)
	assert.ErrorContains(t, err, "txBadSeq")
	assert.EqualValues(t, submitMaxAttempts, sends.Load(), "the rejection should be retried exactly maxAttempts times before giving up")
}

// TestBuildAndSubmitTransaction_OtherRejectionsAreTerminal guards the
// boundary the txBadSeq mapping must not cross: deterministic rejections
// (txBadAuth here) are not retryable and must surface immediately with their
// real code, not be reclassified as a recoverable drift.
func TestBuildAndSubmitTransaction_OtherRejectionsAreTerminal(t *testing.T) {
	sorobanB64 := testSorobanDataB64(t)
	badAuthXDR := rejectedResultXDR(t, xdr.TransactionResultCodeTxBadAuth)

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
			return protocolrpc.SendTransactionResponse{
				Status:         "ERROR",
				ErrorResultXDR: badAuthXDR,
			}, nil
		},
	}

	d := newTestDeployer(t, mock)

	op := testInvokeOp(d.signer.Address())
	src, err := d.getSourceAccount(context.Background())
	require.NoError(t, err)

	_, _, err = d.buildAndSubmitTransaction(context.Background(), src, op)
	require.Error(t, err)
	assert.ErrorContains(t, err, "transaction rejected")
	assert.NotContains(t, err.Error(), "txBadSeq")
	assert.NotContains(t, err.Error(), "txInsufficientFee")
	assert.EqualValues(t, 1, sends.Load(), "a non-retryable rejection must not be resubmitted")
}

// TestInvokeContract_ConcurrentSubmissionsAreSerialized covers NONEVM-6662's
// root cause: concurrent writes from the same signing account must never
// build envelopes with the same sequence. The mock simulates the real race —
// the sequence only advances once a transaction is confirmed — and a slow
// first confirmation widens the window in which an unserialized second caller
// would fetch the same sequence.
func TestInvokeContract_ConcurrentSubmissionsAreSerialized(t *testing.T) {
	sorobanB64 := testSorobanDataB64(t)

	var mu sync.Mutex
	seq := testInitialSeq
	submittedSeqs := map[int64]int{} // seq -> times seen on the wire
	confirmSeq := map[string]int64{} // tx hash -> seq it carries
	var sendCount atomic.Int32

	mock := &mockRPC{
		SimulateTransactionFn: func(_ context.Context, _ protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
			return protocolrpc.SimulateTransactionResponse{
				TransactionDataXDR: sorobanB64,
				MinResourceFee:     100000,
			}, nil
		},
		SendTransactionFn: func(_ context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			n := sendCount.Add(1)
			hash := fmt.Sprintf("hash-%d", n)

			var tx xdr.TransactionEnvelope
			require.NoError(t, xdr.SafeUnmarshalBase64(req.Transaction, &tx))
			sentSeq := int64(tx.SeqNum())

			mu.Lock()
			submittedSeqs[sentSeq]++
			confirmSeq[hash] = sentSeq
			mu.Unlock()

			// A real network rejects a second in-flight envelope carrying an
			// already-submitted sequence; assert instead below that the mutex
			// makes this unreachable.
			return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: hash}, nil
		},
		GetTransactionFn: func(_ context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			// Include the transaction: the on-ledger sequence advances to the
			// submitted value only now.
			seq = confirmSeq[req.Hash]
			return successGetTxResponse(t), nil
		},
	}

	d := newTestDeployer(t, mock)
	// The mock ledger reads the *current* confirmed sequence on every call —
	// exactly what a real ledger read does.
	mock.GetLedgerEntriesFn = func(ctx context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
		mu.Lock()
		s := seq
		mu.Unlock()
		return mockAccountLedgerEntry(t, d.signer.Address(), &s)(ctx, req)
	}

	contractID := randomContractID(t)

	const workers = 4
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.InvokeContract(context.Background(), contractID, "noop", nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}

	mu.Lock()
	defer mu.Unlock()
	assert.EqualValues(t, workers, sendCount.Load(), "every invocation should submit exactly one transaction")
	for s, count := range submittedSeqs {
		assert.Equal(t, 1, count, "sequence %d was submitted %d times — concurrent callers collided", s, count)
	}
	assert.Len(t, submittedSeqs, workers, "each submission must carry a distinct sequence")
}

// TestSimulateContract_AutoRestoreTakesSubmissionLock ensures the read path's
// auto-restore write is serialized with concurrent writes: while a restore
// submission is in flight, a concurrent InvokeContract must wait for the lock
// rather than fetch the same sequence and collide with the restore.
func TestSimulateContract_AutoRestoreTakesSubmissionLock(t *testing.T) {
	sorobanB64 := testSorobanDataB64(t)
	returnValueXDR := mustMarshalScValBase64(t, xdr.ScVal{Type: xdr.ScValTypeScvVoid})

	restoreSubmitted := make(chan struct{})
	finishRestore := make(chan struct{})

	var sims atomic.Int32
	var sends atomic.Int32
	var mu sync.Mutex
	seq := testInitialSeq

	mock := &mockRPC{
		SimulateTransactionFn: func(_ context.Context, _ protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
			// First simulation (from SimulateContract) reports an expired
			// footprint; everything after that simulates cleanly.
			if sims.Add(1) == 1 {
				return protocolrpc.SimulateTransactionResponse{
					RestorePreamble: &protocolrpc.RestorePreamble{
						TransactionDataXDR: sorobanB64,
						MinResourceFee:     50000,
					},
				}, nil
			}
			return protocolrpc.SimulateTransactionResponse{
				TransactionDataXDR: sorobanB64,
				MinResourceFee:     100000,
				Results: []protocolrpc.SimulateHostFunctionResult{
					{ReturnValueXDR: &returnValueXDR},
				},
			}, nil
		},
		SendTransactionFn: func(_ context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			n := sends.Add(1)
			var tx xdr.TransactionEnvelope
			require.NoError(t, xdr.SafeUnmarshalBase64(req.Transaction, &tx))
			opType := tx.Operations()[0].Body.Type
			hash := fmt.Sprintf("hash-%d", n)
			if opType == xdr.OperationTypeRestoreFootprint {
				// Park the restore's confirmation wait, holding submitMu, until
				// the test confirms the concurrent invoke is blocked behind it.
				close(restoreSubmitted)
				<-finishRestore
			}
			mu.Lock()
			seq = int64(tx.SeqNum())
			mu.Unlock()
			return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: hash}, nil
		},
		GetTransactionFn: func(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
			return successGetTxResponse(t), nil
		},
	}

	d := newTestDeployer(t, mock)
	mock.GetLedgerEntriesFn = func(ctx context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
		mu.Lock()
		s := seq
		mu.Unlock()
		return mockAccountLedgerEntry(t, d.signer.Address(), &s)(ctx, req)
	}

	// SimulateContract triggers the auto-restore, which parks inside SendTransaction
	// while holding submitMu.
	simDone := make(chan error, 1)
	go func() {
		_, err := d.SimulateContract(context.Background(), randomContractID(t), "get_value", nil)
		simDone <- err
	}()

	select {
	case <-restoreSubmitted:
	case <-time.After(5 * time.Second):
		t.Fatal("restore was never submitted")
	}

	// The concurrent invoke must block on submitMu for as long as the restore
	// is parked: it must not reach the ledger read, or it would observe the
	// pre-restore sequence and build a colliding envelope.
	invokeDone := make(chan error, 1)
	go func() {
		_, err := d.InvokeContract(context.Background(), randomContractID(t), "noop", nil)
		invokeDone <- err
	}()

	select {
	case err := <-invokeDone:
		t.Fatalf("invoke completed while the restore still held the submission lock (err=%v)", err)
	case <-time.After(200 * time.Millisecond):
		// Still blocked, as required.
	}

	close(finishRestore)
	require.NoError(t, <-simDone)
	require.NoError(t, <-invokeDone)
}
