package txm

import (
	"sync/atomic"
	"testing"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"

	"github.com/smartcontractkit/chainlink-stellar/relayer/config"
)

const (
	boundarySeq       = int64(101)
	boundaryMaxLedger = uint32(1050)
)

// boundaryMock models stellar-rpc's getTransaction read order. The first poll reads the tx row
// before ledger MaxLedger-1, which contains the tx, is committed, and the ledger range after it,
// so it answers NOT_FOUND with latestLedger = MaxLedger-1. Every later read answers SUCCESS.
func boundaryMock(t *testing.T, reads *atomic.Int32) *mockRPCClient {
	t.Helper()
	return &mockRPCClient{
		// The seeded envelope landed, so the account's last used sequence is boundarySeq.
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: buildAccountEntryXDR(t, testAddress, boundarySeq)}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: boundaryMaxLedger},
		simulateResp:        protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000},
		getTransactionHook: func(req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
			if req.Hash == "seeded-hash" && reads.Add(1) == 1 {
				return protocolrpc.GetTransactionResponse{
					LatestLedger:       boundaryMaxLedger - 1,
					TransactionDetails: protocolrpc.TransactionDetails{Status: protocolrpc.TransactionStatusNotFound},
				}, nil
			}
			return protocolrpc.GetTransactionResponse{
				LatestLedger:       boundaryMaxLedger,
				TransactionDetails: protocolrpc.TransactionDetails{Status: protocolrpc.TransactionStatusSuccess, Ledger: boundaryMaxLedger - 1},
			}, nil
		},
		sendHook: func(req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: sendRequestHash(t, req)}, nil
		},
	}
}

func seedBoundaryTx(t *testing.T, txm *StellarTxm) (*TxStore, *StellarTx) {
	t.Helper()
	store, tx := seedUnconfirmed(t, txm, boundarySeq, boundaryMaxLedger, time.Now().Add(5*time.Minute).Unix(), time.Now())
	tx.Operations = []txnbuild.Operation{testInvokeNoopOp()}
	tx.TxHash = "seeded-hash"
	return store, tx
}

func TestStellarTxm_ConfirmLoop_NotFoundAtLastIncludableLedgerIsReadAgain(t *testing.T) {
	t.Parallel()

	var reads atomic.Int32
	mock := boundaryMock(t, &reads)
	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	store, tx := seedBoundaryTx(t, txm)

	txm.checkUnconfirmed(t.Context())
	assert.Empty(t, txm.broadcastChan, "a NOT_FOUND at MaxLedger-1 may describe a tx included in MaxLedger-1")
	assert.Equal(t, int32(2), reads.Load(), "the boundary reply is read again at once")

	txm.checkUnconfirmed(t.Context())
	res, err := txm.GetTransactionResult(tx.ID)
	require.NoError(t, err)
	assert.Equal(t, commontypes.Finalized, res.Status)
	assert.Equal(t, "seeded-hash", res.Hash)
	assert.Equal(t, boundarySeq+1, store.GetNextSequence())
}

func TestStellarTxm_ConfirmLoop_NotFoundTwiceAtLastIncludableLedgerExpires(t *testing.T) {
	t.Parallel()

	var reads atomic.Int32
	mock := boundaryMock(t, &reads)
	mock.getTransactionHook = func(protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
		reads.Add(1)
		return protocolrpc.GetTransactionResponse{
			LatestLedger:       boundaryMaxLedger - 1,
			TransactionDetails: protocolrpc.TransactionDetails{Status: protocolrpc.TransactionStatusNotFound},
		}, nil
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	seedBoundaryTx(t, txm)

	txm.checkUnconfirmed(t.Context())
	assert.Equal(t, int32(2), reads.Load())
	assert.Len(t, txm.broadcastChan, 1, "two NOT_FOUND reads at MaxLedger-1 prove the envelope expired")
}
