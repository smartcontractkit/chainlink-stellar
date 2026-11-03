package txm

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

	chainsel "github.com/smartcontractkit/chain-selectors"
	clconfig "github.com/smartcontractkit/chainlink-common/pkg/config"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	"github.com/smartcontractkit/chainlink-stellar/relayer/config"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
)

func TestNoopStellarTxmMetrics(t *testing.T) {
	ctx := t.Context()
	m := NewNoopStellarTxmMetrics()

	assert.NotPanics(t, func() {
		m.IncrementBroadcastedTxs(ctx, 0)
		m.IncrementSuccessTxs(ctx, 1)
		m.SetPendingTxs(ctx, 5)
		m.IncrementErrorTxs(ctx, ErrorReasonTimedOut)
		m.IncrementRetryTxs(ctx, RetryReasonTimedOut)
		m.IncrementDroppedTxs(ctx, DropReasonChannelFullNewRejected)
		m.IncrementMaxAttemptsReached(ctx, RetryBudgetLifecycle)
		m.IncrementMaxAttemptsReached(ctx, RetryBudgetInfra)
		m.IncrementSubmitRejected(ctx, ErrorReasonTooLate)
		m.IncrementRestore(ctx, RestoreOutcomeInitiated)
		m.IncrementRestore(ctx, RestoreOutcomeSuccess)
		m.IncrementRestore(ctx, RestoreOutcomeFailed)
		m.ObserveSimulationDuration(ctx, 500)
		m.ObserveInclusionFee(ctx, 1000)
		m.ObserveResourceFee(ctx, 5000)
		m.ObserveRPCLatestLedgerAge(ctx, 2*time.Second)
		m.IncrementRPCCloseTimeUnknown(ctx)
		m.RecordTimeUntilTxConfirmed(ctx, 1)
	})
}

func TestStellarTxm_Metrics_BroadcastIncrementsCounter(t *testing.T) {
	chainID := chainsel.STELLAR_TESTNET.ChainID
	broadcastBefore := testutil.ToFloat64(promStellarTxmBroadcastedTxs.WithLabelValues(chainID))

	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp:        protocolrpc.SimulateTransactionResponse{MinResourceFee: 10000},
		sendTransactionResp: protocolrpc.SendTransactionResponse{
			Status: stellarcore.TXStatusPending,
			Hash:   "test-hash",
		},
	}

	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(mock), chainID)
	require.NoError(t, err)

	require.NoError(t, txm.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, txm.Close()) })

	txID, err := txm.Enqueue(t.Context(), TxRequest{
		FromAddress: testAddress,
		Operations:  []txnbuild.Operation{testInvokeNoopOp()},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		status, err := txm.GetStatus(txID)
		return err == nil && status == commontypes.Unconfirmed
	}, 5*time.Second, 50*time.Millisecond, "tx should reach Unconfirmed after broadcast")

	broadcastAfter := testutil.ToFloat64(promStellarTxmBroadcastedTxs.WithLabelValues(chainID))
	assert.Equal(t, float64(1), broadcastAfter-broadcastBefore, "broadcast counter should increment once")
}

func TestStellarTxm_Metrics_SubmitRejectedIncrementsCounter(t *testing.T) {
	chainID := chainsel.STELLAR_TESTNET.ChainID
	rejectedBefore := testutil.ToFloat64(promStellarTxmSubmitRejected.WithLabelValues(chainID, string(ErrorReasonTooLate)))

	tooLate, err := xdr.MarshalBase64(xdr.TransactionResult{
		Result: xdr.TransactionResultResult{Code: xdr.TransactionResultCodeTxTooLate},
	})
	require.NoError(t, err)

	var sends atomic.Int32
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: buildAccountEntryXDR(t, testAddress, 100)}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp:        protocolrpc.SimulateTransactionResponse{MinResourceFee: 10000},
		sendHook: func(protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			if sends.Add(1) == 1 {
				return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusError, ErrorResultXDR: tooLate}, nil
			}
			return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending}, nil
		},
	}

	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(mock), chainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, txm.Close()) })

	txID, err := txm.Enqueue(t.Context(), TxRequest{
		FromAddress: testAddress,
		Operations:  []txnbuild.Operation{testInvokeNoopOp()},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		status, err := txm.GetStatus(txID)
		return err == nil && status == commontypes.Unconfirmed
	}, 5*time.Second, 50*time.Millisecond, "tx should reach Unconfirmed after the retry")

	rejectedAfter := testutil.ToFloat64(promStellarTxmSubmitRejected.WithLabelValues(chainID, string(ErrorReasonTooLate)))
	assert.Equal(t, float64(1), rejectedAfter-rejectedBefore, "one too_late rejection should be counted")
}

func TestStellarTxm_Metrics_ConfirmIncrementsSuccess(t *testing.T) {
	chainID := chainsel.STELLAR_TESTNET.ChainID
	successBefore := testutil.ToFloat64(promStellarTxmSuccessTxs.WithLabelValues(chainID))

	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp:        protocolrpc.SimulateTransactionResponse{MinResourceFee: 10000},
		sendTransactionResp: protocolrpc.SendTransactionResponse{
			Status: stellarcore.TXStatusPending,
			Hash:   "test-hash",
		},
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status: protocolrpc.TransactionStatusSuccess,
			},
		},
	}

	cfg := config.TxManagerConfig{ConfirmPollInterval: clconfig.MustNewDuration(100 * time.Millisecond)}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainID)
	require.NoError(t, err)

	require.NoError(t, txm.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, txm.Close()) })

	txID, err := txm.Enqueue(t.Context(), TxRequest{
		FromAddress: testAddress,
		Operations:  []txnbuild.Operation{testInvokeNoopOp()},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		status, err := txm.GetStatus(txID)
		return err == nil && status == commontypes.Finalized
	}, 5*time.Second, 50*time.Millisecond, "tx should reach Finalized after confirm loop")

	successAfter := testutil.ToFloat64(promStellarTxmSuccessTxs.WithLabelValues(chainID))
	assert.Equal(t, float64(1), successAfter-successBefore, "success counter should increment once")
}

func TestStellarTxmMetrics_AttemptAttributeIsBounded(t *testing.T) {
	m := &stellarTxmMetrics{chainID: "test"}
	for attempt, want := range map[uint64]string{0: "0", 2: "2", 3: "3+", 7: "3+"} {
		attrs := attribute.NewSet(m.attemptAttributes(attempt)...)
		got, ok := attrs.Value("attempt")
		require.True(t, ok)
		assert.Equal(t, want, got.AsString(), "attempt %d", attempt)
	}
}

// recordingTxmMetrics records the calls the instruments for the offset canary depend on.
type recordingTxmMetrics struct {
	noopStellarTxmMetrics
	mu                sync.Mutex
	ages              []time.Duration
	closeTimeUnknown  int
	broadcastAttempts []uint64
	successAttempts   []uint64
}

func (r *recordingTxmMetrics) ObserveRPCLatestLedgerAge(_ context.Context, age time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ages = append(r.ages, age)
}

func (r *recordingTxmMetrics) IncrementRPCCloseTimeUnknown(context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeTimeUnknown++
}

func (r *recordingTxmMetrics) IncrementBroadcastedTxs(_ context.Context, attempt uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.broadcastAttempts = append(r.broadcastAttempts, attempt)
}

func (r *recordingTxmMetrics) IncrementSuccessTxs(_ context.Context, attempt uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.successAttempts = append(r.successAttempts, attempt)
}

func (r *recordingTxmMetrics) snapshot() recordingTxmMetrics {
	r.mu.Lock()
	defer r.mu.Unlock()
	return recordingTxmMetrics{
		ages: append([]time.Duration(nil), r.ages...), closeTimeUnknown: r.closeTimeUnknown,
		broadcastAttempts: append([]uint64(nil), r.broadcastAttempts...),
		successAttempts:   append([]uint64(nil), r.successAttempts...),
	}
}

func TestStellarTxm_EstimateNetworkHead_RecordsLedgerAgeOrUnknownCloseTime(t *testing.T) {
	t.Parallel()

	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(&mockRPCClient{}), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	rec := &recordingTxmMetrics{}
	txm.metrics = rec
	now := time.Unix(1_000_000, 0)
	latest := func(closeTime int64) protocolrpc.GetLatestLedgerResponse {
		return protocolrpc.GetLatestLedgerResponse{Sequence: 100, LedgerCloseTime: closeTime}
	}

	txm.estimateNetworkHead(t.Context(), logger.Test(t), latest(now.Unix()-7), 100, now)
	txm.estimateNetworkHead(t.Context(), logger.Test(t), latest(now.Unix()-60), 100, now)
	got := rec.snapshot()
	assert.Equal(t, []time.Duration{7 * time.Second, 60 * time.Second}, got.ages, "the raw age is recorded, not the capped lag")
	assert.Equal(t, 0, got.closeTimeUnknown)

	txm.estimateNetworkHead(t.Context(), logger.Test(t), latest(0), 100, now)
	txm.estimateNetworkHead(t.Context(), logger.Test(t), latest(now.Unix()+3), 100, now)
	got = rec.snapshot()
	assert.Len(t, got.ages, 2, "a missing or future close time records no age")
	assert.Equal(t, 2, got.closeTimeUnknown)
}

func TestStellarTxm_Metrics_AttemptFollowsTheBroadcastAndItsConfirmation(t *testing.T) {
	t.Parallel()

	h := newFeeSeedingHarness(t, feeStats(100, 1_000_000, 26_000_000))
	var landed atomic.Bool
	h.mock.getTransactionHook = func(protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
		status := protocolrpc.TransactionStatusNotFound
		if landed.Load() {
			status = protocolrpc.TransactionStatusSuccess
		}
		return protocolrpc.GetTransactionResponse{
			LatestLedger:       h.latestLedger.Load(),
			TransactionDetails: protocolrpc.TransactionDetails{Status: status},
		}, nil
	}
	cfg := config.TxManagerConfig{
		FeeStatsPollInterval: clconfig.MustNewDuration(0),
		ConfirmPollInterval:  clconfig.MustNewDuration(50 * time.Millisecond),
		SubmitRetryDelay:     clconfig.MustNewDuration(10 * time.Millisecond),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(h.mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	rec := &recordingTxmMetrics{}
	txm.metrics = rec
	require.NoError(t, txm.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, txm.Close()) })

	h.broadcast(t, txm)
	h.expire(t)
	landed.Store(true)
	require.Eventually(t, func() bool { return len(rec.snapshot().successAttempts) == 1 }, 5*time.Second, 20*time.Millisecond)

	got := rec.snapshot()
	assert.Equal(t, []uint64{0, 1}, got.broadcastAttempts, "each broadcast carries its own attempt")
	assert.Equal(t, []uint64{1}, got.successAttempts, "the confirmation carries the attempt of the envelope that landed")
}
