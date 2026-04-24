package txm

import (
	"context"
	"fmt"
	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-common/pkg/config"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
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
			Status: "PENDING",
			Hash:   "test-hash",
		},
	}

	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)

	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	txID, err := txm.Enqueue(context.Background(), TxRequest{
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
		return err == nil && status == commontypes.Unconfirmed
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

	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)

	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	txID, err := txm.Enqueue(context.Background(), TxRequest{
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
		return err == nil && status == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond, "tx should fail due to sim error")

	// Ensure sequence was released
	store := txm.accountStore.GetTxStore(testAddress)
	assert.Equal(t, int64(101), store.GetNextSequence())
	assert.Equal(t, 0, store.InflightCount())
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
			Status: "TRY_AGAIN_LATER",
		},
	}

	cfg := Config{
		MaxSubmitRetryAttempts: ptr(uint(2)),
		SubmitRetryDelay:       config.MustNewDuration(10 * time.Millisecond),
	}
	txm, err := New(logger.Nop(), &mockKeystore{}, cfg, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)

	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	txID, err := txm.Enqueue(context.Background(), TxRequest{
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
		return err == nil && status == commontypes.Failed
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
				Status:         "ERROR",
				ErrorResultXDR: b64,
			}, nil
		}
		return protocolrpc.SendTransactionResponse{
			Status: "PENDING",
			Hash:   "test-hash-2",
		}, nil
	}

	wrapper := &mockWrapper{mockRPCClient: mock, sendFn: mockSend}

	cfg := Config{
		MaxSubmitRetryAttempts: ptr(uint(3)),
	}

	// Create a custom client factory for the wrapper
	client := ccvclient.NewClientFromInterfaceWithConfig(wrapper, ccvclient.ClientConfig{
		LedgerCacheTTL: 0,
		PollInterval:   10 * time.Millisecond,
	})
	getClient := func() (*ccvclient.Client, error) { return client, nil }

	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, getClient, "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)

	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	txID, err := txm.Enqueue(context.Background(), TxRequest{
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
		return err == nil && status == commontypes.Unconfirmed
	}, 5*time.Second, 50*time.Millisecond, "tx should succeed after bad_seq retry")

	store := txm.accountStore.GetTxStore(testAddress)
	assert.Equal(t, int64(107), store.GetNextSequence()) // 105 + 1 + 1 (used)
}

func TestStellarTxm_BroadcastPipeline_SendTransactionRPCError(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp:        protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000},
		sendTransactionErr:  fmt.Errorf("rpc submit failed"),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)
	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	txID, err := txm.Enqueue(context.Background(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, err := txm.GetStatus(txID)
		return err == nil && st == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond)
}

func TestStellarTxm_BroadcastPipeline_SimulateErrorField(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}}},
		getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp:         protocolrpc.SimulateTransactionResponse{Error: "soroban simulation failed"},
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "c", "Test SDF Network ; September 2015")
	require.NoError(t, err)
	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()
	txID, err := txm.Enqueue(context.Background(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, e := txm.GetStatus(txID)
		return e == nil && st == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond)
}

func TestStellarTxm_BroadcastPipeline_RestorePreambleFails(t *testing.T) {
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
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "c", "Test SDF Network ; September 2015")
	require.NoError(t, err)
	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()
	txID, err := txm.Enqueue(context.Background(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, e := txm.GetStatus(txID)
		return e == nil && st == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond)
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
	txm, err := New(logger.Test(t), ks, Config{}, newTestGetClient(mock), "c", "Test SDF Network ; September 2015")
	require.NoError(t, err)
	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()
	txID, err := txm.Enqueue(context.Background(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, e := txm.GetStatus(txID)
		return e == nil && st == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond)
}

func TestStellarTxm_BroadcastPipeline_GetClientFails_DoesNotFinalize(t *testing.T) {
	t.Parallel()
	bad := func() (*ccvclient.Client, error) { return nil, fmt.Errorf("no rpc") }
	txm, err := New(logger.Nop(), &mockKeystore{}, Config{}, bad, "c", "Test SDF Network ; September 2015")
	require.NoError(t, err)
	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()
	txID, err := txm.Enqueue(context.Background(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	time.Sleep(80 * time.Millisecond)
	st, err := txm.GetStatus(txID)
	require.NoError(t, err)
	assert.Equal(t, commontypes.Pending, st, "getClient error returns early from broadcast; tx stays pending (current behavior)")
}

func TestStellarTxm_BroadcastPipeline_DUPLICATE(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}}},
		getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp:         protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000},
		sendTransactionResp:  protocolrpc.SendTransactionResponse{Status: "DUPLICATE", Hash: "dup-h"},
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "c", "Test SDF Network ; September 2015")
	require.NoError(t, err)
	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()
	txID, err := txm.Enqueue(context.Background(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, e := txm.GetStatus(txID)
		return e == nil && st == commontypes.Unconfirmed
	}, 5*time.Second, 50*time.Millisecond)
}
