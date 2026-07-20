package txm

import (
	"context"
	"fmt"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	clconfig "github.com/smartcontractkit/chainlink-common/pkg/config"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-stellar/relayer/config"
)

func TestStellarTxm_BroadcastPipeline_HappyPath(t *testing.T) {
	t.Parallel()

	accountXDR := buildAccountEntryXDR(t, testAddress, 100)

	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{
				{DataXDR: accountXDR},
			},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp: protocolrpc.SimulateTransactionResponse{
			MinResourceFee: 10000,
		},
		sendTransactionResp: protocolrpc.SendTransactionResponse{
			Status: stellarcore.TXStatusPending,
			Hash:   "test-hash",
		},
	}

	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	txID, err := txm.Enqueue(t.Context(), TxRequest{
		FromAddress: testAddress,
		Operations: []txnbuild.Operation{&txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &xdr.InvokeContractArgs{
					ContractAddress: xdr.ScAddress{
						Type:       xdr.ScAddressTypeScAddressTypeContract,
						ContractId: &xdr.ContractId{},
					},
					FunctionName: xdr.ScSymbol("noop"),
				},
			},
		}},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		status, err := txm.GetStatus(txID)
		require.NoError(t, err)
		return status == commontypes.Unconfirmed
	}, 5*time.Second, 50*time.Millisecond, "tx should move to Unconfirmed")

	txm.transactionsLock.RLock()
	tx := txm.transactions[txID]
	txm.transactionsLock.RUnlock()

	assert.Equal(t, "test-hash", tx.TxHash)
	assert.NotNil(t, tx.Fee)
	assert.True(t, tx.Fee.Cmp(big.NewInt(0)) > 0)
}

func TestStellarTxm_BroadcastPipeline_SimulateError(t *testing.T) {
	t.Parallel()

	accountXDR := buildAccountEntryXDR(t, testAddress, 100)

	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{
				{DataXDR: accountXDR},
			},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateErr:         fmt.Errorf("RPC down"),
	}

	cfg := config.TxManagerConfig{
		MaxSimulateAttempts: ptr(uint(2)),
		SubmitRetryDelay:    clconfig.MustNewDuration(10 * time.Millisecond),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	txID, err := txm.Enqueue(t.Context(), TxRequest{
		FromAddress: testAddress,
		Operations: []txnbuild.Operation{&txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &xdr.InvokeContractArgs{
					ContractAddress: xdr.ScAddress{
						Type:       xdr.ScAddressTypeScAddressTypeContract,
						ContractId: &xdr.ContractId{},
					},
					FunctionName: xdr.ScSymbol("noop"),
				},
			},
		}},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		status, err := txm.GetStatus(txID)
		require.NoError(t, err)
		return status == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond, "tx should fail due to sim error")

	// Ensure sequence was released
	store := txm.accountStore.GetTxStore(testAddress)
	assert.Equal(t, int64(101), store.GetNextSequence())
	assert.Equal(t, 0, store.InflightCount())
}

func TestStellarTxm_BroadcastPipeline_SimulateRPCErrorRetriesThenSucceeds(t *testing.T) {
	t.Parallel()

	accountXDR := buildAccountEntryXDR(t, testAddress, 100)

	var simulateCalls atomic.Int32
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"},
	}
	mock.simulateHook = func(protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
		if simulateCalls.Add(1) == 1 {
			return protocolrpc.SimulateTransactionResponse{}, fmt.Errorf("temporary EOF")
		}
		return protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000}, nil
	}

	cfg := config.TxManagerConfig{
		MaxSimulateAttempts: ptr(uint(2)),
		SubmitRetryDelay:    clconfig.MustNewDuration(10 * time.Millisecond),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		status, err := txm.GetStatus(txID)
		require.NoError(t, err)
		return status == commontypes.Unconfirmed
	}, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, int32(2), simulateCalls.Load())
}

func TestStellarTxm_BroadcastPipeline_TryAgainLater(t *testing.T) {
	t.Parallel()

	accountXDR := buildAccountEntryXDR(t, testAddress, 100)

	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{
				{DataXDR: accountXDR},
			},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp: protocolrpc.SimulateTransactionResponse{
			MinResourceFee: 10000,
		},
		sendTransactionResp: protocolrpc.SendTransactionResponse{
			Status: stellarcore.TXStatusTryAgainLater,
		},
	}

	cfg := config.TxManagerConfig{
		MaxSubmitRetryAttempts: ptr(uint(2)),
		SubmitRetryDelay:       clconfig.MustNewDuration(10 * time.Millisecond),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	txID, err := txm.Enqueue(t.Context(), TxRequest{
		FromAddress: testAddress,
		Operations: []txnbuild.Operation{&txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &xdr.InvokeContractArgs{
					ContractAddress: xdr.ScAddress{
						Type:       xdr.ScAddressTypeScAddressTypeContract,
						ContractId: &xdr.ContractId{},
					},
					FunctionName: xdr.ScSymbol("noop"),
				},
			},
		}},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		status, err := txm.GetStatus(txID)
		require.NoError(t, err)
		return status == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond, "tx should fail after max retries")
}

type mockWrapper struct {
	*mockRPCClient
	sendFn func(ctx context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error)
}

func (m *mockWrapper) SendTransaction(ctx context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
	return m.sendFn(ctx, req)
}

func TestStellarTxm_BroadcastPipeline_BadSeqRetry(t *testing.T) {
	t.Parallel()

	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	accountXDR2 := buildAccountEntryXDR(t, testAddress, 105)

	callCount := 0
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{
				{DataXDR: accountXDR},
			},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp: protocolrpc.SimulateTransactionResponse{
			MinResourceFee: 10000,
		},
	}

	// Override SendTransaction to return bad_seq first time, then success
	mockSend := func(ctx context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		callCount++
		if callCount == 1 {
			// Change ledger entry so resync gets new sequence
			mock.getLedgerEntriesResp = protocolrpc.GetLedgerEntriesResponse{
				Entries: []protocolrpc.LedgerEntryResult{
					{DataXDR: accountXDR2},
				},
			}

			// Build a tx_bad_seq error result
			txResult := xdr.TransactionResult{
				Result: xdr.TransactionResultResult{
					Code: xdr.TransactionResultCodeTxBadSeq,
				},
			}
			b64, _ := xdr.MarshalBase64(txResult)

			return protocolrpc.SendTransactionResponse{
				Status:         stellarcore.TXStatusError,
				ErrorResultXDR: b64,
			}, nil
		}
		return protocolrpc.SendTransactionResponse{
			Status: stellarcore.TXStatusPending,
			Hash:   "test-hash-2",
		}, nil
	}

	wrapper := &mockWrapper{mockRPCClient: mock, sendFn: mockSend}

	cfg := config.TxManagerConfig{
		MaxSubmitRetryAttempts: ptr(uint(3)),
	}

	getClient := func(context.Context) (RPCClient, error) { return wrapper, nil }

	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, getClient, chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	txID, err := txm.Enqueue(t.Context(), TxRequest{
		FromAddress: testAddress,
		Operations: []txnbuild.Operation{&txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &xdr.InvokeContractArgs{
					ContractAddress: xdr.ScAddress{
						Type:       xdr.ScAddressTypeScAddressTypeContract,
						ContractId: &xdr.ContractId{},
					},
					FunctionName: xdr.ScSymbol("noop"),
				},
			},
		}},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		status, err := txm.GetStatus(txID)
		require.NoError(t, err)
		return status == commontypes.Unconfirmed
	}, 5*time.Second, 50*time.Millisecond, "tx should succeed after bad_seq retry")

	store := txm.accountStore.GetTxStore(testAddress)
	assert.Equal(t, int64(107), store.GetNextSequence()) // 105 + 1 + 1 (used)
}

func TestStellarTxm_BroadcastPipeline_SendTransactionRPCErrorRetriesThenSucceeds(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	var sendCalls atomic.Int32
	var simulateCalls atomic.Int32
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
	}
	mock.simulateHook = func(protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
		simulateCalls.Add(1)
		return protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000}, nil
	}
	mock.sendHook = func(protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		if sendCalls.Add(1) == 1 {
			return protocolrpc.SendTransactionResponse{}, fmt.Errorf("rpc submit failed")
		}
		return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"}, nil
	}

	cfg := config.TxManagerConfig{
		MaxSubmitRetryAttempts: ptr(uint(2)),
		SubmitRetryDelay:       clconfig.MustNewDuration(10 * time.Millisecond),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, err := txm.GetStatus(txID)
		require.NoError(t, err)
		return st == commontypes.Unconfirmed
	}, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, int32(2), sendCalls.Load())
	assert.Equal(t, int32(2), simulateCalls.Load(), "submit retry should re-simulate before resubmitting")
}

func TestStellarTxm_BroadcastPipeline_SendTransactionRPCErrorExhaustsRetryBudget(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	var sendCalls atomic.Int32
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp:        protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000},
	}
	mock.sendHook = func(protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		sendCalls.Add(1)
		return protocolrpc.SendTransactionResponse{}, fmt.Errorf("rpc submit failed")
	}

	cfg := config.TxManagerConfig{
		MaxSubmitRetryAttempts: ptr(uint(2)),
		SubmitRetryDelay:       clconfig.MustNewDuration(10 * time.Millisecond),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, err := txm.GetStatus(txID)
		require.NoError(t, err)
		return st == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, int32(2), sendCalls.Load())

	store := txm.accountStore.GetTxStore(testAddress)
	require.NotNil(t, store)
	assert.Equal(t, int64(101), store.GetNextSequence())
	assert.Equal(t, 0, store.InflightCount())
}

func TestStellarTxm_BroadcastPipeline_AcceptedWithoutHashFails(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp:        protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000},
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending},
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, err := txm.GetStatus(txID)
		require.NoError(t, err)
		return st == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond)

	store := txm.accountStore.GetTxStore(testAddress)
	require.NotNil(t, store)
	assert.Equal(t, int64(101), store.GetNextSequence())
	assert.Equal(t, 0, store.InflightCount())
}

func TestStellarTxm_BroadcastPipeline_SimulateErrorField(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	var simulateCalls atomic.Int32
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}}},
		getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
	}
	mock.simulateHook = func(protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
		simulateCalls.Add(1)
		return protocolrpc.SimulateTransactionResponse{Error: "soroban simulation failed"}, nil
	}
	cfg := config.TxManagerConfig{
		MaxSimulateAttempts: ptr(uint(3)),
		SubmitRetryDelay:    clconfig.MustNewDuration(10 * time.Millisecond),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()
	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, e := txm.GetStatus(txID)
		require.NoError(t, e)
		return st == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, int32(1), simulateCalls.Load())
}

func TestStellarTxm_BroadcastPipeline_RestorePreambleSuccess(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	accountAfterRestoreXDR := buildAccountEntryXDR(t, testAddress, 101)
	preamble := protocolrpc.RestorePreamble{
		MinResourceFee:     1_000,
		TransactionDataXDR: buildRestorePreambleTransactionDataXDR(t),
	}

	var simulateCalls atomic.Int32
	var sendCalls atomic.Int32
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}}},
		getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
	}
	mock.simulateHook = func(protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
		if simulateCalls.Add(1) == 1 {
			return protocolrpc.SimulateTransactionResponse{
				MinResourceFee:  10_000,
				RestorePreamble: &preamble,
			}, nil
		}
		return protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000}, nil
	}
	mock.sendHook = func(protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		if sendCalls.Add(1) == 1 {
			mock.getLedgerEntriesResp = protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountAfterRestoreXDR}}}
			return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "restore-hash"}, nil
		}
		return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "original-hash"}, nil
	}
	mock.getTransactionHook = func(req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
		if req.Hash == "restore-hash" {
			return protocolrpc.GetTransactionResponse{TransactionDetails: protocolrpc.TransactionDetails{Status: protocolrpc.TransactionStatusSuccess}}, nil
		}
		return protocolrpc.GetTransactionResponse{TransactionDetails: protocolrpc.TransactionDetails{Status: protocolrpc.TransactionStatusNotFound}}, nil
	}

	cfg := config.TxManagerConfig{
		MaxSubmitRetryAttempts: ptr(uint(1)),
		SubmitRetryDelay:       clconfig.MustNewDuration(10 * time.Millisecond),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, e := txm.GetStatus(txID)
		require.NoError(t, e)
		return st == commontypes.Unconfirmed
	}, 5*time.Second, 50*time.Millisecond)

	assert.Equal(t, int32(2), simulateCalls.Load(), "should simulate before and after restore")
	assert.Equal(t, int32(2), sendCalls.Load(), "should send restore then original")

	store := txm.accountStore.GetTxStore(testAddress)
	require.NotNil(t, store)
	assert.Equal(t, int64(103), store.GetNextSequence(), "restore uses 101, original uses 102, next available is 103")
}

func TestStellarTxm_BroadcastPipeline_RestorePreambleInvalidXDRFails(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	preamble := protocolrpc.RestorePreamble{MinResourceFee: 1}
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}}},
		getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp: protocolrpc.SimulateTransactionResponse{
			MinResourceFee:  10_000,
			RestorePreamble: &preamble,
		},
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()
	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, e := txm.GetStatus(txID)
		require.NoError(t, e)
		return st == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond)
}

func TestStellarTxm_BroadcastPipeline_RestorePreambleTwiceFails(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	accountAfterRestoreXDR := buildAccountEntryXDR(t, testAddress, 101)
	preamble := protocolrpc.RestorePreamble{
		MinResourceFee:     1_000,
		TransactionDataXDR: buildRestorePreambleTransactionDataXDR(t),
	}

	var sendCalls atomic.Int32
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}}},
		getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp: protocolrpc.SimulateTransactionResponse{
			MinResourceFee:  10_000,
			RestorePreamble: &preamble,
		},
	}
	mock.sendHook = func(protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		sendCalls.Add(1)
		mock.getLedgerEntriesResp = protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountAfterRestoreXDR}}}
		return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "restore-hash"}, nil
	}
	mock.getTransactionHook = func(req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
		if req.Hash == "restore-hash" {
			return protocolrpc.GetTransactionResponse{TransactionDetails: protocolrpc.TransactionDetails{Status: protocolrpc.TransactionStatusSuccess}}, nil
		}
		return protocolrpc.GetTransactionResponse{TransactionDetails: protocolrpc.TransactionDetails{Status: protocolrpc.TransactionStatusNotFound}}, nil
	}

	cfg := config.TxManagerConfig{SubmitRetryDelay: clconfig.MustNewDuration(10 * time.Millisecond)}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, e := txm.GetStatus(txID)
		require.NoError(t, e)
		return st == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, int32(1), sendCalls.Load(), "should not try a second restore")
}

func TestStellarTxm_BroadcastPipeline_SigningError(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}}},
		getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp:         protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000},
	}
	ks := &mockKeystore{signFn: func(_ context.Context, _ string, _ []byte) ([]byte, error) { return nil, fmt.Errorf("sign failed") }}
	txm, err := New(logger.Test(t), ks, config.TxManagerConfig{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()
	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, e := txm.GetStatus(txID)
		require.NoError(t, e)
		return st == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond)
}

func TestStellarTxm_BroadcastPipeline_GetClientFailsThenRetries(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}}},
		getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp:         protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000},
		sendTransactionResp:  protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"},
	}
	c := newTestClient(mock)
	var getClientCalls atomic.Int32
	getClient := func(context.Context) (RPCClient, error) {
		if getClientCalls.Add(1) == 1 {
			return nil, fmt.Errorf("no rpc")
		}
		return c, nil
	}
	cfg := config.TxManagerConfig{SubmitRetryDelay: clconfig.MustNewDuration(10 * time.Millisecond)}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, getClient, chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()
	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, e := txm.GetStatus(txID)
		require.NoError(t, e)
		return st == commontypes.Unconfirmed
	}, 5*time.Second, 50*time.Millisecond)
	assert.GreaterOrEqual(t, getClientCalls.Load(), int32(2))

	// getClient failure must not consume the lifecycle Attempt budget.
	txm.transactionsLock.RLock()
	tracked := txm.transactions[txID]
	txm.transactionsLock.RUnlock()
	require.NotNil(t, tracked)
	assert.Equal(t, uint64(0), tracked.Attempt, "lifecycle Attempt must not be incremented by getClient failure")
	assert.Equal(t, uint64(1), tracked.InfraAttempts, "InfraAttempts must be incremented once for the single getClient failure")
}

func TestStellarTxm_BroadcastPipeline_GetClientFailsUntilRetryBudgetExhausted(t *testing.T) {
	t.Parallel()
	getClient := func(context.Context) (RPCClient, error) { return nil, fmt.Errorf("no rpc") }
	cfg := config.TxManagerConfig{
		MaxGetClientRetryAttempts: ptr(uint64(2)),
		SubmitRetryDelay:          clconfig.MustNewDuration(10 * time.Millisecond),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, getClient, chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()
	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, e := txm.GetStatus(txID)
		require.NoError(t, e)
		return st == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond)

	txm.transactionsLock.RLock()
	tracked := txm.transactions[txID]
	txm.transactionsLock.RUnlock()
	require.NotNil(t, tracked)
	assert.Equal(t, uint64(2), tracked.InfraAttempts, "InfraAttempts must reach the MaxGetClientRetryAttempts cap")
	assert.Equal(t, uint64(0), tracked.Attempt, "lifecycle Attempt must remain untouched by getClient failures")
	assert.Equal(t, 0, txm.accountStore.GetTotalInflightCount(), "client failures happen before sequence allocation")
}

func TestStellarTxm_BroadcastPipeline_DUPLICATE(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}}},
		getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp:         protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000},
		sendTransactionResp:  protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusDuplicate, Hash: "dup-h"},
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()
	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, e := txm.GetStatus(txID)
		require.NoError(t, e)
		return st == commontypes.Unconfirmed
	}, 5*time.Second, 50*time.Millisecond)
}

func TestStellarTxm_HandleRestore_RestoreTotalNotInflatedByRetry(t *testing.T) {
	t.Parallel()

	// Mainnet chain ID isolates restore Prometheus labels from the rest of this
	// package's tests, which almost all use STELLAR_TESTNET.
	chainID := chainsel.STELLAR_MAINNET.ChainID

	preamble := protocolrpc.RestorePreamble{
		MinResourceFee:     1_000,
		TransactionDataXDR: buildRestorePreambleTransactionDataXDR(t),
	}

	var sendCalls atomic.Int32
	mock := &mockRPCClient{
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
	}
	mock.sendHook = func(protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		n := sendCalls.Add(1)
		// Stellar core returns PENDING for the first submission and
		// DUPLICATE for the resubmission of the same hash — the exact
		// scenario that double-counted Total under the pre-fix code.
		if n == 1 {
			return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "restore-h"}, nil
		}
		return protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusDuplicate, Hash: "restore-h"}, nil
	}

	cfg := config.TxManagerConfig{
		MaxRestoreAttempts: ptr(uint(2)),
		// 0 → PollTransaction's WithTimeout(ctx, 0) is already past
		// deadline, so it returns "poll timed out" immediately and the
		// loop falls into its `continue` path without wall-clock delay.
		TxTimeoutSecs:    ptr(int64(0)),
		SubmitRetryDelay: clconfig.MustNewDuration(1 * time.Millisecond),
	}

	initiatedBefore := testutil.ToFloat64(promStellarTxmRestore.WithLabelValues(chainID, string(RestoreOutcomeInitiated)))
	failedBefore := testutil.ToFloat64(promStellarTxmRestore.WithLabelValues(chainID, string(RestoreOutcomeFailed)))
	successBefore := testutil.ToFloat64(promStellarTxmRestore.WithLabelValues(chainID, string(RestoreOutcomeSuccess)))

	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainID)
	require.NoError(t, err)

	tx := &StellarTx{ID: "restore-test", FromAddress: testAddress, Done: make(chan struct{})}
	client := newTestClient(mock)

	// handleRestore is expected to fail (loop exhausts), but the metric
	// invariant must hold regardless of outcome.
	err = txm.handleRestore(t.Context(), client, tx, preamble, 1)
	require.Error(t, err)

	assert.GreaterOrEqual(t, sendCalls.Load(), int32(2),
		"loop must iterate at least twice — that's the scenario that exposed the bug")

	initiatedAfter := testutil.ToFloat64(promStellarTxmRestore.WithLabelValues(chainID, string(RestoreOutcomeInitiated)))
	failedAfter := testutil.ToFloat64(promStellarTxmRestore.WithLabelValues(chainID, string(RestoreOutcomeFailed)))
	successAfter := testutil.ToFloat64(promStellarTxmRestore.WithLabelValues(chainID, string(RestoreOutcomeSuccess)))

	assert.Equal(t, float64(1), initiatedAfter-initiatedBefore,
		"restore initiated must increment exactly once per logical restore")
	assert.Equal(t, float64(1), failedAfter-failedBefore,
		"restore failed fires once on attempt-exhaustion")
	assert.Equal(t, float64(0), successAfter-successBefore,
		"no success expected on this path")
}

func TestStellarTxm_HandleRestore_RestoreTotalCountsOnceOnSuccess(t *testing.T) {
	t.Parallel()

	// Localnet shares testnet's passphrase (see NetworkPassphrase) but uses a
	// distinct chain ID for metrics isolation from testnet and mainnet tests.
	chainID := chainsel.STELLAR_LOCALNET.ChainID

	accountAfterRestoreXDR := buildAccountEntryXDR(t, testAddress, 101)
	preamble := protocolrpc.RestorePreamble{
		MinResourceFee:     1_000,
		TransactionDataXDR: buildRestorePreambleTransactionDataXDR(t),
	}

	mock := &mockRPCClient{
		getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountAfterRestoreXDR}}},
		sendTransactionResp:  protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "restore-h"},
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{Status: protocolrpc.TransactionStatusSuccess},
		},
	}

	cfg := config.TxManagerConfig{
		MaxRestoreAttempts: ptr(uint(2)),
		TxTimeoutSecs:      ptr(int64(5)),
		SubmitRetryDelay:   clconfig.MustNewDuration(1 * time.Millisecond),
	}

	initiatedBefore := testutil.ToFloat64(promStellarTxmRestore.WithLabelValues(chainID, string(RestoreOutcomeInitiated)))
	successBefore := testutil.ToFloat64(promStellarTxmRestore.WithLabelValues(chainID, string(RestoreOutcomeSuccess)))
	failedBefore := testutil.ToFloat64(promStellarTxmRestore.WithLabelValues(chainID, string(RestoreOutcomeFailed)))

	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainID)
	require.NoError(t, err)

	// Pre-seed an account store so resyncSequence after restore has somewhere to update.
	_, err = txm.accountStore.CreateTxStore(testAddress, 100)
	require.NoError(t, err)

	tx := &StellarTx{ID: "restore-test-ok", FromAddress: testAddress, Done: make(chan struct{})}
	client := newTestClient(mock)

	require.NoError(t, txm.handleRestore(t.Context(), client, tx, preamble, 1))

	assert.Equal(t, float64(1), testutil.ToFloat64(promStellarTxmRestore.WithLabelValues(chainID, string(RestoreOutcomeInitiated)))-initiatedBefore,
		"restore initiated must increment exactly once per logical restore")
	assert.Equal(t, float64(1), testutil.ToFloat64(promStellarTxmRestore.WithLabelValues(chainID, string(RestoreOutcomeSuccess)))-successBefore,
		"restore success fires once on terminal success")
	assert.Equal(t, float64(0), testutil.ToFloat64(promStellarTxmRestore.WithLabelValues(chainID, string(RestoreOutcomeFailed)))-failedBefore,
		"no failure expected on this path")
}

func TestStellarTxm_BuildPreliminaryTx_SeqZero_DoesNotProduceNegativeSequence(t *testing.T) {
	t.Parallel()

	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, config.TxManagerConfig{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	tx := &StellarTx{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}}

	require.NotPanics(t, func() {
		built, err := txm.buildPreliminaryTx(tx, 0, 1500)
		require.NoError(t, err)
		require.NotNil(t, built)
		assert.GreaterOrEqual(t, built.SequenceNumber(), int64(0),
			"on-wire sequence must never be negative")
	})

	// Sanity: the normal broadcast path (seq>=1) is unaffected by the clamp.
	require.NotPanics(t, func() {
		built, err := txm.buildPreliminaryTx(tx, 50, 1500)
		require.NoError(t, err)
		assert.Equal(t, int64(50), built.SequenceNumber())
	})
}

// TestStellarTxm_BroadcastPipeline_GetClientFailuresDoNotStealLifecycleBudget
// verifies the core invariant of the fix: getClient (infra) failures consume
// InfraAttempts, not the lifecycle Attempt budget. After several getClient
// failures followed by a successful broadcast, the tx must still have the
// full MaxTxRetryAttempts budget available for post-submit lifecycle retries.
func TestStellarTxm_BroadcastPipeline_GetClientFailuresDoNotStealLifecycleBudget(t *testing.T) {
    t.Parallel()
    accountXDR := buildAccountEntryXDR(t, testAddress, 100)
    mock := &mockRPCClient{
        getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}}},
        getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
        simulateResp:         protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000},
        sendTransactionResp:  protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"},
    }
    c := newTestClient(mock)
    var getClientCalls atomic.Int32
    getClient := func(context.Context) (RPCClient, error) {
        // Fail the first 3 getClient calls (infra), then succeed.
        if getClientCalls.Add(1) <= 3 {
            return nil, fmt.Errorf("no rpc")
        }
        return c, nil
    }
    cfg := config.TxManagerConfig{SubmitRetryDelay: clconfig.MustNewDuration(10 * time.Millisecond)}
    txm, err := New(logger.Test(t), &mockKeystore{}, cfg, getClient, chainsel.STELLAR_TESTNET.ChainID)
    require.NoError(t, err)
    require.NoError(t, txm.Start(t.Context()))
    defer txm.Close()
    txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
    require.NoError(t, err)
    require.Eventually(t, func() bool {
        st, e := txm.GetStatus(txID)
        require.NoError(t, e)
        return st == commontypes.Unconfirmed
    }, 5*time.Second, 50*time.Millisecond)

    txm.transactionsLock.RLock()
    tracked := txm.transactions[txID]
    txm.transactionsLock.RUnlock()
    require.NotNil(t, tracked)
    assert.Equal(t, uint64(3), tracked.InfraAttempts, "3 getClient failures must increment InfraAttempts to 3")
    assert.Equal(t, uint64(0), tracked.Attempt, "lifecycle Attempt must be 0 — no post-submit retry happened yet")
}
