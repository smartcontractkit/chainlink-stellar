package txm

import (
	"context"
	"fmt"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"

	"github.com/smartcontractkit/chainlink-common/pkg/config"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
)

// --- Mock keystore ---

type mockKeystore struct {
	signFn func(ctx context.Context, id string, data []byte) ([]byte, error)
}

func (m *mockKeystore) Accounts(_ context.Context) ([]string, error) {
	return []string{testAddress}, nil
}

func (m *mockKeystore) Sign(ctx context.Context, id string, data []byte) ([]byte, error) {
	if m.signFn != nil {
		return m.signFn(ctx, id, data)
	}
	return make([]byte, 64), nil
}

func (m *mockKeystore) Decrypt(_ context.Context, _ string, encrypted []byte) ([]byte, error) {
	return encrypted, nil
}

// --- Mock RPC client that satisfies ccvclient.RPCClient ---

type mockRPCClient struct {
	getLatestLedgerResp  protocolrpc.GetLatestLedgerResponse
	getLatestLedgerErr   error
	getLedgerEntriesResp protocolrpc.GetLedgerEntriesResponse
	getLedgerEntriesErr  error
	getTransactionResp   protocolrpc.GetTransactionResponse
	getTransactionErr    error
	sendTransactionResp  protocolrpc.SendTransactionResponse
	sendTransactionErr   error
	simulateResp         protocolrpc.SimulateTransactionResponse
	simulateErr          error
	getEventsResp        protocolrpc.GetEventsResponse
	getEventsErr         error
	getLedgersResp       protocolrpc.GetLedgersResponse
	getLedgersErr        error
	getFeeStatsResp      protocolrpc.GetFeeStatsResponse
	getFeeStatsErr       error

	getTransactionCalls atomic.Int32

	// getLatestLedgerHook, when set, is used instead of getLatestLedgerResp (avoids
	// racy test updates to getLatestLedgerResp after Start).
	getLatestLedgerHook func() (protocolrpc.GetLatestLedgerResponse, error)
	simulateHook        func(protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error)
	sendHook            func(protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error)
	getTransactionHook  func(protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error)
	getEventsHook       func(protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error)
}

func (m *mockRPCClient) SimulateTransaction(_ context.Context, req protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
	if m.simulateHook != nil {
		return m.simulateHook(req)
	}
	return m.simulateResp, m.simulateErr
}
func (m *mockRPCClient) SendTransaction(_ context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
	if m.sendHook != nil {
		return m.sendHook(req)
	}
	return m.sendTransactionResp, m.sendTransactionErr
}
func (m *mockRPCClient) GetTransaction(_ context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
	m.getTransactionCalls.Add(1)
	if m.getTransactionHook != nil {
		return m.getTransactionHook(req)
	}
	return m.getTransactionResp, m.getTransactionErr
}
func (m *mockRPCClient) GetLedgerEntries(_ context.Context, _ protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
	return m.getLedgerEntriesResp, m.getLedgerEntriesErr
}
func (m *mockRPCClient) GetEvents(_ context.Context, req protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error) {
	if m.getEventsHook != nil {
		return m.getEventsHook(req)
	}
	return m.getEventsResp, m.getEventsErr
}
func (m *mockRPCClient) GetLatestLedger(_ context.Context) (protocolrpc.GetLatestLedgerResponse, error) {
	if m.getLatestLedgerHook != nil {
		return m.getLatestLedgerHook()
	}
	return m.getLatestLedgerResp, m.getLatestLedgerErr
}
func (m *mockRPCClient) GetLedgers(_ context.Context, _ protocolrpc.GetLedgersRequest) (protocolrpc.GetLedgersResponse, error) {
	return m.getLedgersResp, m.getLedgersErr
}
func (m *mockRPCClient) GetFeeStats(_ context.Context) (protocolrpc.GetFeeStatsResponse, error) {
	return m.getFeeStatsResp, m.getFeeStatsErr
}

// buildAccountEntryXDR creates a base64-encoded XDR LedgerEntryData for an account
// with the given sequence number.
func buildAccountEntryXDR(t *testing.T, address string, seqNum int64) string {
	t.Helper()
	aid := xdr.MustAddress(address)
	entry := xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeAccount,
		Account: &xdr.AccountEntry{
			AccountId: aid,
			SeqNum:    xdr.SequenceNumber(seqNum),
			Balance:   xdr.Int64(1_000_000_000),
		},
	}
	b64, err := xdr.MarshalBase64(entry)
	require.NoError(t, err)
	return b64
}

func buildRestorePreambleTransactionDataXDR(t *testing.T) string {
	t.Helper()
	data := xdr.SorobanTransactionData{
		Resources: xdr.SorobanResources{
			Footprint: xdr.LedgerFootprint{},
		},
		ResourceFee: 1,
	}
	b64, err := xdr.MarshalBase64(data)
	require.NoError(t, err)
	return b64
}

func newTestClient(mock *mockRPCClient) *ccvclient.Client {
	return ccvclient.NewClientFromInterface(mock, &ccvclient.ClientConfig{
		LedgerCacheTTL: config.MustNewDuration(0),
		PollInterval:   config.MustNewDuration(10 * time.Millisecond),
	})
}

func newTestGetClient(mock *mockRPCClient) func() (*ccvclient.Client, error) {
	client := newTestClient(mock)
	return func() (*ccvclient.Client, error) { return client, nil }
}

const testAddress = "GAAZI4TCR3TY5OJHCTJC2A4QSY6CJWJH5IAJTGKIN2ER7LBNVKOCCWN7"

// --- Constructor tests ---

func TestNew_Success(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)
	require.NotNil(t, txm)
	assert.Equal(t, "StellarTxm", txm.Name())
}

// --- Lifecycle tests ---

func TestStellarTxm_StartStop(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)

	err = txm.Start(context.Background())
	require.NoError(t, err)

	assert.NoError(t, txm.Ready())
	assert.Contains(t, txm.HealthReport(), txm.Name())

	err = txm.Close()
	require.NoError(t, err)
}

func TestStellarTxm_DoubleStart(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	err = txm.Start(context.Background())
	require.Error(t, err)
}

// --- Enqueue tests ---

func TestStellarTxm_Enqueue_Validation(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	t.Run("missing Operations", func(t *testing.T) {
		_, err := txm.Enqueue(context.Background(), TxRequest{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "currently only single-operation transactions are supported")
	})

	t.Run("too many Operations", func(t *testing.T) {
		_, err := txm.Enqueue(context.Background(), TxRequest{
			Operations: []txnbuild.Operation{
				&txnbuild.InvokeHostFunction{
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
				},
				&txnbuild.InvokeHostFunction{
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
				},
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "currently only single-operation transactions are supported")
	})

	t.Run("duplicate ID", func(t *testing.T) {
		id := "dup-test-id"
		_, err := txm.Enqueue(context.Background(), TxRequest{
			ID: id,
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

		_, err = txm.Enqueue(context.Background(), TxRequest{
			ID: id,
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
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")
	})
}

func TestStellarTxm_Enqueue_AutoID(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	txID, err := txm.Enqueue(context.Background(), TxRequest{
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
	assert.NotEmpty(t, txID)
}

// TestStellarTxm_Enqueue_ChannelFull_EvictsOldest verifies drop-oldest backpressure:
// when the broadcast channel is full, the oldest queued tx is evicted (marked Failed
// with DropReasonChannelFullOldestEvicted) to make room for the newer tx, which is
// accepted. The TXM is not started, so broadcastLoop never drains the channel.
func TestStellarTxm_Enqueue_ChannelFull_EvictsOldest(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	cfg := Config{BroadcastChanSize: ptr(uint(1))}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	op := &txnbuild.InvokeHostFunction{
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
	}

	oldID, err := txm.Enqueue(context.Background(), TxRequest{Operations: []txnbuild.Operation{op}})
	require.NoError(t, err)

	newID, err := txm.Enqueue(context.Background(), TxRequest{Operations: []txnbuild.Operation{op}})
	require.NoError(t, err, "new tx should be accepted after evicting oldest")
	assert.NotEqual(t, oldID, newID)

	// Old tx should be marked Failed with the drop reason; its Done channel closed.
	oldStatus, err := txm.GetStatus(oldID)
	require.NoError(t, err)
	assert.Equal(t, commontypes.Failed, oldStatus, "evicted tx should be Failed")

	txm.transactionsLock.RLock()
	oldTx := txm.transactions[oldID]
	newTx := txm.transactions[newID]
	txm.transactionsLock.RUnlock()
	require.NotNil(t, oldTx)
	require.NotNil(t, newTx)
	assert.Equal(t, DropReasonChannelFullOldestEvicted, oldTx.ResultCode)
	select {
	case <-oldTx.Done:
		// expected — closeDone was called
	default:
		t.Fatal("evicted tx's Done channel should be closed")
	}
	// New tx should still be in the channel, not terminated.
	assert.Equal(t, commontypes.Pending, newTx.Status)
}

// --- GetStatus tests ---

func TestStellarTxm_GetStatus(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	t.Run("empty ID", func(t *testing.T) {
		_, err := txm.GetStatus("")
		require.Error(t, err)
	})

	t.Run("non-existent", func(t *testing.T) {
		_, err := txm.GetStatus("non-existent")
		require.Error(t, err)
	})

	t.Run("existing tx", func(t *testing.T) {
		txID, err := txm.Enqueue(context.Background(), TxRequest{
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

		status, err := txm.GetStatus(txID)
		require.NoError(t, err)
		assert.Equal(t, commontypes.Pending, status)
	})
}

func TestStellarTxm_GetTransactionResult(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	t.Run("empty ID", func(t *testing.T) {
		_, err := txm.GetTransactionResult("")
		require.Error(t, err)
	})

	t.Run("non-existent", func(t *testing.T) {
		_, err := txm.GetTransactionResult("non-existent")
		require.Error(t, err)
	})

	t.Run("pending", func(t *testing.T) {
		txID, err := txm.Enqueue(context.Background(), TxRequest{
			FromAddress: testAddress,
			Operations:  []txnbuild.Operation{testInvokeNoopOp()},
		})
		require.NoError(t, err)

		result, err := txm.GetTransactionResult(txID)
		require.NoError(t, err)
		assert.Equal(t, txID, result.ID)
		assert.Equal(t, commontypes.Pending, result.Status)
		assert.Empty(t, result.Hash)
		assert.Empty(t, result.ResultXDR)
		assert.Empty(t, result.ResultMetaXDR)
		assert.NoError(t, result.Error)
	})

	t.Run("finalized", func(t *testing.T) {
		txID, err := txm.Enqueue(context.Background(), TxRequest{
			FromAddress: testAddress,
			Operations:  []txnbuild.Operation{testInvokeNoopOp()},
		})
		require.NoError(t, err)

		fee := big.NewInt(12345)
		txm.transactionsLock.Lock()
		tx := txm.transactions[txID]
		tx.Status = commontypes.Finalized
		tx.TxHash = "hash-finalized"
		tx.Fee = fee
		tx.ResultXDR = "result-xdr"
		tx.ResultMetaXDR = "meta-xdr"
		txm.transactionsLock.Unlock()

		result, err := txm.GetTransactionResult(txID)
		require.NoError(t, err)
		assert.Equal(t, txID, result.ID)
		assert.Equal(t, "hash-finalized", result.Hash)
		assert.Equal(t, commontypes.Finalized, result.Status)
		assert.Equal(t, fee, result.Fee)
		assert.Equal(t, "result-xdr", result.ResultXDR)
		assert.Equal(t, "meta-xdr", result.ResultMetaXDR)
		assert.NoError(t, result.Error)
	})

	t.Run("failed with result code", func(t *testing.T) {
		txID, err := txm.Enqueue(context.Background(), TxRequest{
			FromAddress: testAddress,
			Operations:  []txnbuild.Operation{testInvokeNoopOp()},
		})
		require.NoError(t, err)

		txm.transactionsLock.Lock()
		tx := txm.transactions[txID]
		tx.Status = commontypes.Failed
		tx.TxHash = "hash-failed"
		tx.ResultXDR = "failed-result-xdr"
		tx.ResultCode = "contract_error"
		txm.transactionsLock.Unlock()

		result, err := txm.GetTransactionResult(txID)
		require.NoError(t, err)
		assert.Equal(t, txID, result.ID)
		assert.Equal(t, "hash-failed", result.Hash)
		assert.Equal(t, commontypes.Failed, result.Status)
		assert.Equal(t, "failed-result-xdr", result.ResultXDR)
		require.Error(t, result.Error)
		assert.Contains(t, result.Error.Error(), "contract_error")
	})
}

// --- GetTransactionFee tests ---

func TestStellarTxm_GetTransactionFee(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	t.Run("empty ID", func(t *testing.T) {
		_, err := txm.GetTransactionFee("")
		require.Error(t, err)
	})

	t.Run("not finalized", func(t *testing.T) {
		txID, err := txm.Enqueue(context.Background(), TxRequest{
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

		_, err = txm.GetTransactionFee(txID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not finalized")
	})

	t.Run("finalized with fee", func(t *testing.T) {
		txID, err := txm.Enqueue(context.Background(), TxRequest{
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

		txm.transactionsLock.Lock()
		tx := txm.transactions[txID]
		tx.Status = commontypes.Finalized
		tx.Fee = big.NewInt(12345)
		txm.transactionsLock.Unlock()

		fee, err := txm.GetTransactionFee(txID)
		require.NoError(t, err)
		assert.Equal(t, big.NewInt(12345), fee)
	})
}

// --- InflightCount test ---

func TestStellarTxm_InflightCount(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	chanLen, storeCount := txm.InflightCount()
	assert.Equal(t, 0, chanLen)
	assert.Equal(t, 0, storeCount)
}

// --- BroadcastLoop integration test ---

func TestStellarTxm_BroadcastLoop_ProcessesTx(t *testing.T) {
	t.Parallel()

	accountXDR := buildAccountEntryXDR(t, testAddress, 100)

	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{
				{DataXDR: accountXDR},
			},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "test-hash"},
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
}

// --- ConfirmLoop integration test ---

func TestStellarTxm_ConfirmLoop_FinalizesSuccess(t *testing.T) {
	t.Parallel()

	accountXDR := buildAccountEntryXDR(t, testAddress, 100)

	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{
				{DataXDR: accountXDR},
			},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "test-hash"},
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status: protocolrpc.TransactionStatusSuccess,
			},
		},
	}

	cfg := Config{ConfirmPollInterval: config.MustNewDuration(100 * time.Millisecond)}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
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
		return err == nil && status == commontypes.Finalized
	}, 5*time.Second, 50*time.Millisecond, "tx should reach Finalized")
}

func TestStellarTxm_ConfirmLoop_ExpiredTxRetries(t *testing.T) {
	t.Parallel()

	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	var latestLedgerSeq atomic.Uint32
	latestLedgerSeq.Store(1000)

	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{
				{DataXDR: accountXDR},
			},
		},
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "test-hash"},
		getTransactionErr:   fmt.Errorf("not found"),
		getLatestLedgerHook: func() (protocolrpc.GetLatestLedgerResponse, error) {
			return protocolrpc.GetLatestLedgerResponse{Sequence: latestLedgerSeq.Load()}, nil
		},
	}

	cfg := Config{
		ConfirmPollInterval: config.MustNewDuration(100 * time.Millisecond),
		MaxTxRetryAttempts:  ptr(uint64(0)),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
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

	// Wait for broadcast loop to pick it up
	require.Eventually(t, func() bool {
		status, _ := txm.GetStatus(txID)
		return status == commontypes.Unconfirmed
	}, 5*time.Second, 50*time.Millisecond)

	// Now simulate the ledger advancing past MaxLedger (which is 1000+50=1050)
	latestLedgerSeq.Store(2000)

	// MaxTxRetryAttempts=0, so after expiry it should go to Failed
	require.Eventually(t, func() bool {
		status, _ := txm.GetStatus(txID)
		return status == commontypes.Failed
	}, 5*time.Second, 50*time.Millisecond, "expired tx with 0 retries should be Failed")
}

// --- EnqueueAndWait test ---

func TestStellarTxm_EnqueueAndWait(t *testing.T) {
	t.Parallel()

	accountXDR := buildAccountEntryXDR(t, testAddress, 100)

	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{
				{DataXDR: accountXDR},
			},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "test-hash"},
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status: protocolrpc.TransactionStatusSuccess,
			},
		},
	}

	cfg := Config{ConfirmPollInterval: config.MustNewDuration(100 * time.Millisecond)}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)

	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := txm.EnqueueAndWait(ctx, TxRequest{
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
	require.NotNil(t, result)
	assert.Equal(t, commontypes.Finalized, result.Status)
	assert.NotEmpty(t, result.Hash)
}

func TestStellarTxm_EnqueueAndWait_ContextCancel(t *testing.T) {
	t.Parallel()

	accountXDR := buildAccountEntryXDR(t, testAddress, 100)

	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{
				{DataXDR: accountXDR},
			},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "test-hash"},
		// Never return success — tx stays unconfirmed
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status: protocolrpc.TransactionStatusNotFound,
			},
		},
	}

	cfg := Config{ConfirmPollInterval: config.MustNewDuration(100 * time.Millisecond)}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)

	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = txm.EnqueueAndWait(ctx, TxRequest{
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
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context")
}

// --- getSequenceNumber tests ---

func TestStellarTxm_GetSequenceNumber(t *testing.T) {
	t.Parallel()

	accountXDR := buildAccountEntryXDR(t, testAddress, 42)

	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{
				{DataXDR: accountXDR},
			},
		},
	}

	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	client := newTestClient(mock)
	seq, err := txm.getSequenceNumber(context.Background(), client, testAddress)
	require.NoError(t, err)
	assert.Equal(t, int64(42), seq)
}

func TestStellarTxm_GetSequenceNumber_AccountNotFound(t *testing.T) {
	t.Parallel()

	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{},
		},
	}

	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	client := newTestClient(mock)
	_, err = txm.getSequenceNumber(context.Background(), client, testAddress)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStellarTxm_GetSequenceNumber_EmptyAddress(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)
	client := newTestClient(mock)
	_, err = txm.getSequenceNumber(context.Background(), client, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "address is required")
}

// --- Simulate tests ---

func testInvokeNoopOp() *txnbuild.InvokeHostFunction {
	return &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &xdr.InvokeContractArgs{
				ContractAddress: xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &xdr.ContractId{}},
				FunctionName:    xdr.ScSymbol("noop"),
			},
		},
	}
}

func TestStellarTxm_Simulate_validation(t *testing.T) {
	t.Parallel()
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(&mockRPCClient{}), "c", "Test SDF Network ; September 2015")
	require.NoError(t, err)

	t.Run("no operations", func(t *testing.T) {
		t.Parallel()
		_, err := txm.Simulate(context.Background(), TxRequest{FromAddress: testAddress})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least one operation")
	})
}

func TestStellarTxm_Simulate_getClientError(t *testing.T) {
	t.Parallel()
	bad := func() (*ccvclient.Client, error) { return nil, fmt.Errorf("unreachable") }
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, bad, "c", "Test SDF Network ; September 2015")
	require.NoError(t, err)
	_, err = txm.Simulate(context.Background(), TxRequest{
		FromAddress: testAddress,
		Operations:  []txnbuild.Operation{testInvokeNoopOp()},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get client")
}

func TestStellarTxm_Simulate_LatestLedgerError(t *testing.T) {
	t.Parallel()
	inner := &mockRPCClient{getLatestLedgerErr: fmt.Errorf("ledger err")}
	client := newTestClient(inner)
	getClient := func() (*ccvclient.Client, error) { return client, nil }
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, getClient, "c", "Test SDF Network ; September 2015")
	require.NoError(t, err)
	_, err = txm.Simulate(context.Background(), TxRequest{
		FromAddress: testAddress,
		Operations:  []txnbuild.Operation{testInvokeNoopOp()},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "latest ledger")
}

func TestStellarTxm_Simulate_success(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 9},
		simulateResp:        protocolrpc.SimulateTransactionResponse{MinResourceFee: 5},
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), "c", "Test SDF Network ; September 2015")
	require.NoError(t, err)
	res, err := txm.Simulate(context.Background(), TxRequest{
		FromAddress: testAddress,
		Operations:  []txnbuild.Operation{testInvokeNoopOp()},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(5), res.MinResourceFee)
}

// --- maybeRetry: broadcast channel full ---

// blockingAfterFirstSimulateRPC runs the first Simulate in "started, then block until
// unblock is closed" mode so the broadcast loop can be stuck mid-tx while the channel holds another ID.
type blockingAfterFirstSimulateRPC struct {
	*mockRPCClient
	started chan struct{} // closed when the first sim call has entered
	unblock chan struct{} // close to let sim calls finish (tests control lifecycle)
	calls   int32
}

func (b *blockingAfterFirstSimulateRPC) SimulateTransaction(ctx context.Context, req protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
	if atomic.AddInt32(&b.calls, 1) == 1 {
		close(b.started)
		<-b.unblock
	}
	return b.mockRPCClient.SimulateTransaction(ctx, req)
}

func TestStellarTxm_maybeRetry_ReturnsFalseWhenBroadcastChannelIsFull(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	inner := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		simulateResp:        protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000},
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "h"},
	}
	bmock := &blockingAfterFirstSimulateRPC{
		mockRPCClient: inner,
		started:       make(chan struct{}),
		unblock:       make(chan struct{}),
	}
	getClient := func() (*ccvclient.Client, error) {
		return ccvclient.NewClientFromInterface(bmock, &ccvclient.ClientConfig{
			LedgerCacheTTL: config.MustNewDuration(0),
			PollInterval:   config.MustNewDuration(10 * time.Millisecond),
		}), nil
	}
	cfg := Config{
		BroadcastChanSize:  ptr(uint(1)),
		MaxTxRetryAttempts: ptr(uint64(3)),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, getClient, "c", "Test SDF Network ; September 2015")
	require.NoError(t, err)
	require.NoError(t, txm.Start(context.Background()))
	defer func() { _ = txm.Close() }()

	op := testInvokeNoopOp()

	_, err = txm.Enqueue(context.Background(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{op}})
	require.NoError(t, err)
	// Wait until the first tx is inside Simulate (broadcast loop is blocked there).
	<-bmock.started
	// Buffer size 1: the second id sits in the channel while the first tx is still in sim.
	_, err = txm.Enqueue(context.Background(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{op}})
	require.NoError(t, err)

	retried := txm.maybeRetry(context.Background(), &UnconfirmedTx{
		Tx:   &StellarTx{ID: "retry", Attempt: 0},
		Hash: "h",
	}, RetryReasonTimedOut)
	assert.False(t, retried, "with a full broadcast buffer maybeRetry should not block or drop a retry")
	close(bmock.unblock) // Unblock sim so the test can shut down the txm.
}

func buildSuccessTransactionResultXDR(t *testing.T, fee int64) string {
	t.Helper()
	inner, err := xdr.NewTransactionResultResult(xdr.TransactionResultCodeTxSuccess, []xdr.OperationResult{})
	require.NoError(t, err)
	res := xdr.TransactionResult{FeeCharged: xdr.Int64(fee), Result: inner}
	b64, err := xdr.MarshalBase64(res)
	require.NoError(t, err)
	return b64
}

func TestStellarTxm_ConfirmLoop_UpdatesFeeAndMetaFromXDR(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	resultB64 := buildSuccessTransactionResultXDR(t, 40_200)
	metaB64 := "QVFMTUFURURfVE1fVEVTVA=="
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}}},
		getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		sendTransactionResp:  protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "test-hash"},
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status:        protocolrpc.TransactionStatusSuccess,
				ResultXDR:     resultB64,
				ResultMetaXDR: metaB64,
			},
		},
	}
	cfg := Config{ConfirmPollInterval: config.MustNewDuration(100 * time.Millisecond)}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), "c", "Test SDF Network ; September 2015")
	require.NoError(t, err)
	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	txID, err := txm.Enqueue(context.Background(), TxRequest{
		FromAddress: testAddress,
		Operations: []txnbuild.Operation{&txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &xdr.InvokeContractArgs{
					ContractAddress: xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &xdr.ContractId{}},
					FunctionName:    xdr.ScSymbol("noop"),
				},
			},
		}},
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, err := txm.GetStatus(txID)
		return err == nil && st == commontypes.Finalized
	}, 5*time.Second, 20*time.Millisecond)

	txm.transactionsLock.RLock()
	tracked := txm.transactions[txID]
	txm.transactionsLock.RUnlock()
	require.NotNil(t, tracked)
	assert.Equal(t, big.NewInt(40_200), tracked.Fee)
	assert.Equal(t, resultB64, tracked.ResultXDR)
	assert.Equal(t, metaB64, tracked.ResultMetaXDR)

	result, err := txm.GetTransactionResult(txID)
	require.NoError(t, err)
	assert.Equal(t, "test-hash", result.Hash)
	assert.Equal(t, commontypes.Finalized, result.Status)
	assert.Equal(t, big.NewInt(40_200), result.Fee)
	assert.Equal(t, resultB64, result.ResultXDR)
	assert.Equal(t, metaB64, result.ResultMetaXDR)
	assert.NoError(t, result.Error)
}

func TestStellarTxm_ConfirmLoop_TerminalContractFailureDoesNotRetry(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	resultB64 := buildFailedInvokeHostFunctionResultXDR(t, xdr.InvokeHostFunctionResultCodeInvokeHostFunctionTrapped)
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}}},
		getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		sendTransactionResp:  protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "test-hash"},
		simulateResp:         protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000},
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status:    protocolrpc.TransactionStatusFailed,
				ResultXDR: resultB64,
			},
		},
	}
	cfg := Config{ConfirmPollInterval: config.MustNewDuration(20 * time.Millisecond)}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), "c", "Test SDF Network ; September 2015")
	require.NoError(t, err)
	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	txID, err := txm.Enqueue(context.Background(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, err := txm.GetStatus(txID)
		return err == nil && st == commontypes.Failed
	}, 5*time.Second, 20*time.Millisecond)

	txm.transactionsLock.RLock()
	tracked := txm.transactions[txID]
	txm.transactionsLock.RUnlock()
	require.NotNil(t, tracked)
	assert.Equal(t, uint64(0), tracked.Attempt)
	assert.Equal(t, resultB64, tracked.ResultXDR)
	assert.Equal(t, xdr.InvokeHostFunctionResultCodeInvokeHostFunctionTrapped.String(), tracked.ResultCode)

	result, err := txm.GetTransactionResult(txID)
	require.NoError(t, err)
	assert.Equal(t, "test-hash", result.Hash)
	assert.Equal(t, commontypes.Failed, result.Status)
	assert.Equal(t, resultB64, result.ResultXDR)
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), xdr.InvokeHostFunctionResultCodeInvokeHostFunctionTrapped.String())

	store := txm.accountStore.GetTxStore(testAddress)
	require.NotNil(t, store)
	assert.Equal(t, int64(102), store.GetNextSequence(), "on-chain FAILED consumed sequence 101, so the next tx should use 102")
}
