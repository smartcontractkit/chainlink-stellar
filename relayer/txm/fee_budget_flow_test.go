package txm

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	clconfig "github.com/smartcontractkit/chainlink-common/pkg/config"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	"github.com/smartcontractkit/chainlink-stellar/relayer/config"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
)

// enqueueForBudget enqueues a noop invoke with the given per-request resource fee cap
// and returns the tracked tx.
func enqueueForBudget(t *testing.T, txm *StellarTxm, requestMaxResourceFee uint64) *StellarTx {
	t.Helper()
	txID, err := txm.Enqueue(t.Context(), TxRequest{
		FromAddress:    testAddress,
		Operations:     []txnbuild.Operation{testInvokeNoopOp()},
		MaxResourceFee: requestMaxResourceFee,
	})
	require.NoError(t, err)
	txm.transactionsMapLock.RLock()
	defer txm.transactionsMapLock.RUnlock()
	tx, ok := txm.transactions[txID]
	require.True(t, ok)
	return tx
}

func newBudgetTestTxm(t *testing.T, cfg config.TxManagerConfig, mock *mockRPCClient) *StellarTxm {
	t.Helper()
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, txm.Close()) })
	return txm
}

func TestStellarTxm_CumulativeFee_DerivesFromRequestCap(t *testing.T) {
	t.Parallel()

	// SetDefaults is the production path: the config New receives has already been resolved.
	toml := config.TOMLConfig{}
	toml.SetDefaults()
	txm := newBudgetTestTxm(t, toml.TxManager, &mockRPCClient{})

	tx := enqueueForBudget(t, txm, 1_000_000)
	require.NotNil(t, tx.budget)
	assert.Equal(t, int64(7_000_000), tx.budget.max.Int64(), "request cap * (MaxTxRetryAttempts + 2)")
}

func TestStellarTxm_CumulativeFee_DerivesFromNodeCapWithoutRequestCap(t *testing.T) {
	t.Parallel()

	txm := newBudgetTestTxm(t, config.TxManagerConfig{}, &mockRPCClient{})

	tx := enqueueForBudget(t, txm, 0)
	require.NotNil(t, tx.budget)
	assert.Equal(t, int64(2_000_000_000)*(5+2), tx.budget.max.Int64())
}

func TestStellarTxm_CumulativeFee_ExplicitCapWinsOverRequestCap(t *testing.T) {
	t.Parallel()

	txm := newBudgetTestTxm(t, config.TxManagerConfig{MaxCumulativeFee: ptr(int64(50_000))}, &mockRPCClient{})

	tx := enqueueForBudget(t, txm, 1_000_000)
	require.NotNil(t, tx.budget)
	assert.Equal(t, int64(50_000), tx.budget.max.Int64())
}

func TestStellarTxm_CumulativeFee_DisabledWithoutAnyResourceCap(t *testing.T) {
	t.Parallel()

	txm := newBudgetTestTxm(t, config.TxManagerConfig{MaxResourceFee: ptr(int64(0))}, &mockRPCClient{})

	assert.Nil(t, enqueueForBudget(t, txm, 0).budget, "no resource cap leaves nothing to derive from")

	tx := enqueueForBudget(t, txm, 1_000_000)
	require.NotNil(t, tx.budget, "a request cap still derives when the node has none")
	assert.Equal(t, int64(7_000_000), tx.budget.max.Int64())
}

func TestStellarTxm_CumulativeFee_RequestCapAboveNodeCapSends(t *testing.T) {
	t.Parallel()

	var sendCalls atomic.Int32
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: buildAccountEntryXDR(t, testAddress, 100)}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp: protocolrpc.SimulateTransactionResponse{
			MinResourceFee:     80_000_000,
			TransactionDataXDR: buildRestorePreambleTransactionDataXDR(t),
		},
		sendHook: func(protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			sendCalls.Add(1)
			return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"}, nil
		},
	}
	// The node cap derives only 70_000_000, less than this one envelope.
	txm := newBudgetTestTxm(t, config.TxManagerConfig{MaxResourceFee: ptr(int64(10_000_000))}, mock)

	tx := enqueueForBudget(t, txm, 100_000_000)
	require.Eventually(t, func() bool {
		st, err := txm.GetStatus(tx.ID)
		require.NoError(t, err)
		return st == commontypes.Unconfirmed || st == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond)

	st, err := txm.GetStatus(tx.ID)
	require.NoError(t, err)
	assert.Equal(t, commontypes.Unconfirmed, st)
	assert.Equal(t, int32(1), sendCalls.Load(), "an envelope within its own cap must be sent")
}

func TestStellarTxm_CumulativeFee_ExceededFailsWithoutSending(t *testing.T) {
	chainID := chainsel.STELLAR_TESTNET.ChainID
	reason := string(ErrorReasonCumulativeFeeExceeded)
	before := testutil.ToFloat64(promStellarTxmOutcomeTxs.WithLabelValues(chainID, "error", reason))

	var sendCalls atomic.Int32
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: buildAccountEntryXDR(t, testAddress, 100)}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp:        protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000},
		sendHook: func(protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			sendCalls.Add(1)
			return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"}, nil
		},
	}
	// The envelope costs the 100 stroop inclusion fee; a 50 stroop budget rejects it.
	cfg := config.TxManagerConfig{MaxCumulativeFee: ptr(int64(50))}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, txm.Close()) })

	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, err := txm.GetStatus(txID)
		require.NoError(t, err)
		return st == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond)

	assert.Zero(t, sendCalls.Load(), "an envelope the budget rejects must never be sent")
	assert.Equal(t, float64(1), testutil.ToFloat64(promStellarTxmOutcomeTxs.WithLabelValues(chainID, "error", reason))-before)

	store := txm.accountStore.GetTxStore(testAddress)
	require.NotNil(t, store)
	assert.Equal(t, int64(101), store.GetNextSequence(), "the reserved sequence is recycled")
	assert.Zero(t, store.InflightCount())
}

func TestStellarTxm_CumulativeFee_RejectedEnvelopeReleasesReservation(t *testing.T) {
	t.Parallel()

	var sendCalls atomic.Int32
	var simCalls atomic.Int32
	transactionDataXDR := buildRestorePreambleTransactionDataXDR(t)
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: buildAccountEntryXDR(t, testAddress, 100)}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateHook: func(protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
			minResourceFee := int64(10_000)
			if simCalls.Add(1) > 1 {
				minResourceFee = 20_000
			}
			return protocolrpc.SimulateTransactionResponse{
				MinResourceFee:     minResourceFee,
				TransactionDataXDR: transactionDataXDR,
			}, nil
		},
		sendHook: func(protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			if sendCalls.Add(1) == 1 {
				return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusTryAgainLater}, nil
			}
			return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"}, nil
		},
	}
	// The first envelope costs 25_100 stroops and the second 35_100 (inclusion plus
	// buffered resource fee), so the second only fits once the rejected first
	// envelope has released its reservation.
	cfg := config.TxManagerConfig{
		MaxCumulativeFee:       ptr(int64(35_100)),
		MaxSubmitRetryAttempts: ptr(uint(3)),
		SubmitRetryDelay:       clconfig.MustNewDuration(10 * time.Millisecond),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, txm.Close()) })

	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, err := txm.GetStatus(txID)
		require.NoError(t, err)
		return st == commontypes.Unconfirmed
	}, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, int32(2), sendCalls.Load())
}

func TestStellarTxm_CumulativeFee_SendErrorsAtOneSequenceCountOnce(t *testing.T) {
	t.Parallel()

	const rpcErrors = 10
	var sendCalls atomic.Int32
	var simCalls atomic.Int32
	transactionDataXDR := buildRestorePreambleTransactionDataXDR(t)
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: buildAccountEntryXDR(t, testAddress, 100)}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateHook: func(protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
			// 885_000 plus the 15_000 buffer is 0.9 x the request cap. The fee moves by a
			// stroop per simulation so every retry signs a new envelope with a new hash.
			return protocolrpc.SimulateTransactionResponse{
				MinResourceFee:     885_000 + int64(simCalls.Add(1)),
				TransactionDataXDR: transactionDataXDR,
			}, nil
		},
		sendHook: func(protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			if sendCalls.Add(1) <= rpcErrors {
				return protocolrpc.SendTransactionResponse{}, errors.New("rpc unavailable")
			}
			return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"}, nil
		},
	}
	// The request cap of 1_000_000 derives a 7_000_000 budget. Summed, the ten unsent
	// envelopes would pass it, but they share one sequence and at most one can land.
	txm := newBudgetTestTxm(t, config.TxManagerConfig{
		MaxSubmitRetryAttempts: ptr(uint(rpcErrors + 1)),
		SubmitRetryDelay:       clconfig.MustNewDuration(time.Millisecond),
	}, mock)

	tx := enqueueForBudget(t, txm, 1_000_000)
	require.Eventually(t, func() bool {
		st, err := txm.GetStatus(tx.ID)
		require.NoError(t, err)
		return st == commontypes.Unconfirmed || st == commontypes.Failed
	}, 5*time.Second, 10*time.Millisecond)

	st, err := txm.GetStatus(tx.ID)
	require.NoError(t, err)
	assert.Equal(t, commontypes.Unconfirmed, st, "same-sequence envelopes must not fail with %s", ErrorReasonCumulativeFeeExceeded)
	assert.Equal(t, int32(rpcErrors+1), sendCalls.Load())
}

func TestStellarTxm_CumulativeFee_SuccessSettlesToFeeCharged(t *testing.T) {
	t.Parallel()

	mock := &mockRPCClient{
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status:    protocolrpc.TransactionStatusSuccess,
				ResultXDR: buildSuccessTransactionResultXDR(t, 40_200),
			},
		},
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	_, tx := seedUnconfirmed(t, txm, 101, 1050, time.Now().Add(time.Hour).Unix(), time.Now())
	tx.budget = newFeeBudget(1_000_000)
	require.NoError(t, tx.budget.reserve(101, "seeded-hash", 50_000))

	txm.checkUnconfirmed(t.Context())

	assert.Equal(t, int64(40_200), tx.budget.spent.Int64(), "settle uses the fee the ledger charged, not the reservation")
	assert.Empty(t, tx.budget.reservations)
	assert.Equal(t, int64(40_200), tx.Fee.Int64())
}

func TestStellarTxm_CumulativeFee_OnChainFailureSettlesChargedFee(t *testing.T) {
	t.Parallel()

	mock := &mockRPCClient{
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status:    protocolrpc.TransactionStatusFailed,
				ResultXDR: buildFailedInvokeHostFunctionResultXDR(t, xdr.InvokeHostFunctionResultCodeInvokeHostFunctionTrapped),
			},
		},
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	_, tx := seedUnconfirmed(t, txm, 101, 1050, time.Now().Add(time.Hour).Unix(), time.Now())
	tx.budget = newFeeBudget(1_000_000)
	require.NoError(t, tx.budget.reserve(101, "seeded-hash", 50_000))

	txm.checkUnconfirmed(t.Context())

	assert.Equal(t, int64(10_000), tx.budget.spent.Int64(), "a failed envelope still burns its fee")
	assert.Empty(t, tx.budget.reservations)
}

func TestStellarTxm_CumulativeFee_ExpiredEnvelopeReleasesReservation(t *testing.T) {
	t.Parallel()

	mock := &mockRPCClient{
		getTransactionResp: protocolrpc.GetTransactionResponse{
			LatestLedger:       1050,
			TransactionDetails: protocolrpc.TransactionDetails{Status: protocolrpc.TransactionStatusNotFound},
		},
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	const maxLedger = uint32(1050)
	_, tx := seedUnconfirmed(t, txm, 101, maxLedger, time.Now().Add(time.Hour).Unix(), time.Now())
	tx.budget = newFeeBudget(1_000_000)
	require.NoError(t, tx.budget.reserve(101, "seeded-hash", 50_000))

	txm.checkUnconfirmed(t.Context())

	assert.Empty(t, tx.budget.reservations, "an envelope past its ledger bounds can never land")
	assert.Zero(t, tx.budget.spent.Int64())
}

func TestStellarTxm_CumulativeFee_UnknownStatusExpiryKeepsReservation(t *testing.T) {
	t.Parallel()

	mock := &mockRPCClient{getTransactionErr: errors.New("rpc down")}
	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	timeBound := time.Now().Add(-2 * statusUnknownMargin).Unix()
	_, tx := seedUnconfirmed(t, txm, 101, 1050, timeBound, time.Now().Add(-time.Hour))
	tx.budget = newFeeBudget(1_000_000)
	require.NoError(t, tx.budget.reserve(101, "seeded-hash", 50_000))

	txm.checkUnconfirmed(t.Context())

	st, err := txm.GetStatus(tx.ID)
	require.NoError(t, err)
	assert.Equal(t, commontypes.Failed, st)
	assert.Len(t, tx.budget.reservations, 1, "an envelope whose fate was never proven must keep counting against the cap")
	assert.Zero(t, tx.budget.spent.Int64())
}

func TestStellarTxm_CumulativeFee_RestoreSuccessSettles(t *testing.T) {
	t.Parallel()

	preamble := protocolrpc.RestorePreamble{
		MinResourceFee:     1_000,
		TransactionDataXDR: buildRestorePreambleTransactionDataXDR(t),
	}
	mock := &mockRPCClient{
		getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: buildAccountEntryXDR(t, testAddress, 101)}}},
		sendTransactionResp:  protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "restore-h"},
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status:    protocolrpc.TransactionStatusSuccess,
				ResultXDR: buildSuccessTransactionResultXDR(t, 12_000),
			},
		},
	}
	cfg := config.TxManagerConfig{
		MaxRestoreAttempts: ptr(uint(2)),
		TxTimeoutSecs:      ptr(int64(5)),
		SubmitRetryDelay:   clconfig.MustNewDuration(1 * time.Millisecond),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	_, err = txm.accountStore.CreateTxStore(testAddress, 100)
	require.NoError(t, err)

	tx := &StellarTx{ID: "restore-budget-ok", FromAddress: testAddress, Done: make(chan struct{}), budget: newFeeBudget(50_000)}
	require.NoError(t, txm.handleRestore(t.Context(), newTestClient(mock), tx, preamble, 1, 100))

	assert.Equal(t, int64(12_000), tx.budget.spent.Int64())
	assert.Empty(t, tx.budget.reservations)
}

func TestStellarTxm_CumulativeFee_RestoreRejectionReleases(t *testing.T) {
	t.Parallel()

	preamble := protocolrpc.RestorePreamble{
		MinResourceFee:     1_000,
		TransactionDataXDR: buildRestorePreambleTransactionDataXDR(t),
	}
	mock := &mockRPCClient{
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusError},
	}
	cfg := config.TxManagerConfig{
		MaxRestoreAttempts: ptr(uint(2)),
		SubmitRetryDelay:   clconfig.MustNewDuration(1 * time.Millisecond),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	tx := &StellarTx{ID: "restore-budget-rejected", FromAddress: testAddress, Done: make(chan struct{}), budget: newFeeBudget(50_000)}
	require.Error(t, txm.handleRestore(t.Context(), newTestClient(mock), tx, preamble, 1, 100))

	assert.Empty(t, tx.budget.reservations, "a rejected restore envelope can never land")
	assert.Zero(t, tx.budget.spent.Int64())
}

func TestStellarTxm_CumulativeFee_RestoreExceededDoesNotSend(t *testing.T) {
	t.Parallel()

	preamble := protocolrpc.RestorePreamble{
		MinResourceFee:     1_000,
		TransactionDataXDR: buildRestorePreambleTransactionDataXDR(t),
	}
	var sendCalls atomic.Int32
	mock := &mockRPCClient{
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		sendHook: func(protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			sendCalls.Add(1)
			return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending}, nil
		},
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	tx := &StellarTx{ID: "restore-budget-exceeded", FromAddress: testAddress, Done: make(chan struct{}), budget: newFeeBudget(1_000)}
	err = txm.handleRestore(t.Context(), newTestClient(mock), tx, preamble, 1, 100)
	require.ErrorIs(t, err, ErrCumulativeFeeExceeded)
	assert.Zero(t, sendCalls.Load())
}

func TestStellarTxm_CumulativeFee_RestoreExceededFailsTransaction(t *testing.T) {
	chainID := chainsel.STELLAR_TESTNET.ChainID
	reason := string(ErrorReasonCumulativeFeeExceeded)
	before := testutil.ToFloat64(promStellarTxmOutcomeTxs.WithLabelValues(chainID, "error", reason))

	var sendCalls atomic.Int32
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: buildAccountEntryXDR(t, testAddress, 100)}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp: protocolrpc.SimulateTransactionResponse{
			MinResourceFee: 1_000,
			RestorePreamble: &protocolrpc.RestorePreamble{
				MinResourceFee:     1_000,
				TransactionDataXDR: buildRestorePreambleTransactionDataXDR(t),
			},
		},
		sendHook: func(protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			sendCalls.Add(1)
			return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending}, nil
		},
	}
	cfg := config.TxManagerConfig{MaxCumulativeFee: ptr(int64(100))}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, txm.Close()) })

	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, err := txm.GetStatus(txID)
		require.NoError(t, err)
		return st == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond)

	assert.Zero(t, sendCalls.Load())
	assert.Equal(t, float64(1), testutil.ToFloat64(promStellarTxmOutcomeTxs.WithLabelValues(chainID, "error", reason))-before,
		"a restore the budget rejects is reported as cumulative_fee_exceeded, not restore_failed")
}
