package txm

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	clconfig "github.com/smartcontractkit/chainlink-common/pkg/config"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"

	"github.com/smartcontractkit/chainlink-stellar/relayer/config"
)

const testChainID = "stellar:testnet"

func TestNoopStellarTxmMetrics(t *testing.T) {
	ctx := t.Context()
	m := NewNoopStellarTxmMetrics()

	assert.NotPanics(t, func() {
		m.IncrementBroadcastedTxs(ctx)
		m.IncrementSuccessTxs(ctx)
		m.SetPendingTxs(ctx, 5)
		m.IncrementErrorTxs(ctx, ErrorReasonTimedOut)
		m.IncrementRetryTxs(ctx, RetryReasonTimedOut)
		m.IncrementDroppedTxs(ctx, DropReasonChannelFullNewRejected)
		m.IncrementMaxAttemptsReached(ctx, RetryBudgetLifecycle)
		m.IncrementMaxAttemptsReached(ctx, RetryBudgetInfra)
		m.IncrementRestore(ctx, RestoreOutcomeInitiated)
		m.IncrementRestore(ctx, RestoreOutcomeSuccess)
		m.IncrementRestore(ctx, RestoreOutcomeFailed)
		m.ObserveSimulationDuration(ctx, 500)
		m.ObserveInclusionFee(ctx, 1000)
		m.ObserveResourceFee(ctx, 5000)
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
	defer txm.Close()

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
	defer txm.Close()

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
