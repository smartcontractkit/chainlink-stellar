package txm

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	clconfig "github.com/smartcontractkit/chainlink-common/pkg/config"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"

	"github.com/smartcontractkit/chainlink-stellar/relayer/config"
)

func lostReplyMock(t *testing.T) *mockRPCClient {
	t.Helper()
	return &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: buildAccountEntryXDR(t, testAddress, 100)}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp:        protocolrpc.SimulateTransactionResponse{MinResourceFee: 10000},
	}
}

func TestStellarTxm_BroadcastPipeline_LostReplyResendsTheSameEnvelope(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var sent []string
	var simulations atomic.Int32
	mock := lostReplyMock(t)
	mock.simulateHook = func(protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
		simulations.Add(1)
		return mock.simulateResp, nil
	}
	mock.sendHook = func(req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, req.Transaction)
		if len(sent) == 1 {
			return protocolrpc.SendTransactionResponse{}, errors.New("connection reset")
		}
		return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending}, nil
	}

	cfg := config.TxManagerConfig{SubmitRetryDelay: clconfig.MustNewDuration(10 * time.Millisecond)}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, txm.Close()) })

	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		status, err := txm.GetStatus(txID)
		return err == nil && status == commontypes.Unconfirmed
	}, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, sent, 2)
	assert.Equal(t, sent[0], sent[1], "after a lost reply the same signed envelope is sent again")
	assert.Equal(t, int32(1), simulations.Load(), "the envelope is not rebuilt")
}

func TestStellarTxm_BroadcastPipeline_LostReplyThenRejectionChecksTheHash(t *testing.T) {
	t.Parallel()

	badSeq, err := xdr.MarshalBase64(xdr.TransactionResult{
		Result: xdr.TransactionResultResult{Code: xdr.TransactionResultCodeTxBadSeq},
	})
	require.NoError(t, err)

	var sends atomic.Int32
	mock := lostReplyMock(t)
	mock.sendHook = func(protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		if sends.Add(1) == 1 {
			return protocolrpc.SendTransactionResponse{}, errors.New("timeout")
		}
		// The first copy landed in the meantime, so the identical resend is rejected.
		return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusError, ErrorResultXDR: badSeq}, nil
	}
	mock.getTransactionHook = func(protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
		return protocolrpc.GetTransactionResponse{
			LatestLedger:       1001,
			TransactionDetails: protocolrpc.TransactionDetails{Status: protocolrpc.TransactionStatusSuccess, Ledger: 1001},
		}, nil
	}

	cfg := config.TxManagerConfig{
		SubmitRetryDelay:    clconfig.MustNewDuration(10 * time.Millisecond),
		ConfirmPollInterval: clconfig.MustNewDuration(20 * time.Millisecond),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, txm.Close()) })

	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		status, err := txm.GetStatus(txID)
		return err == nil && status == commontypes.Finalized
	}, 5*time.Second, 10*time.Millisecond, "the landed envelope is tracked and confirmed")
	assert.Equal(t, int32(2), sends.Load(), "no rebuilt envelope is sent on a new sequence")
}
