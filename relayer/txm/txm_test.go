package txm

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
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

// --- Mock RPC client that satisfies client.RPCClient ---

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

func newTestClient(mock *mockRPCClient) RPCClient {
	return mock
}

func newTestGetClient(mock *mockRPCClient) func(context.Context) (RPCClient, error) {
	return func(context.Context) (RPCClient, error) { return mock, nil }
}

const testAddress = "GAAZI4TCR3TY5OJHCTJC2A4QSY6CJWJH5IAJTGKIN2ER7LBNVKOCCWN7"

// --- Constructor tests ---

func TestNew_Success(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NotNil(t, txm)
	assert.Equal(t, "StellarTxm", txm.Name())
}

// --- Lifecycle tests ---

func TestStellarTxm_StartStop(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	err = txm.Start(t.Context())
	require.NoError(t, err)

	assert.NoError(t, txm.Ready())
	assert.Contains(t, txm.HealthReport(), txm.Name())

	err = txm.Close()
	require.NoError(t, err)
}

func TestStellarTxm_DoubleStart(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	err = txm.Start(t.Context())
	require.Error(t, err)
}

// --- Enqueue tests ---

func TestStellarTxm_Enqueue_Validation(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	t.Run("missing Operations", func(t *testing.T) {
		_, err := txm.Enqueue(t.Context(), TxRequest{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "currently only single-operation transactions are supported")
	})

	t.Run("too many Operations", func(t *testing.T) {
		_, err := txm.Enqueue(t.Context(), TxRequest{
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
		_, err := txm.Enqueue(t.Context(), TxRequest{
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

		id2, err := txm.Enqueue(t.Context(), TxRequest{
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
		assert.Equal(t, id, id2, "duplicate enqueue should return same id (idempotent)")
	})

	t.Run("duplicate ID concurrent", func(t *testing.T) {
		const (
			id       = "concurrent-dup-id"
			nWorkers = 64
		)
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
		req := TxRequest{ID: id, Operations: []txnbuild.Operation{op}}

		var wg sync.WaitGroup
		var mu sync.Mutex
		var results []struct {
			id  string
			err error
		}
		wg.Add(nWorkers)
		for i := 0; i < nWorkers; i++ {
			go func() {
				defer wg.Done()
				gotID, err := txm.Enqueue(t.Context(), req)
				mu.Lock()
				results = append(results, struct {
					id  string
					err error
				}{gotID, err})
				mu.Unlock()
			}()
		}
		wg.Wait()

		for _, r := range results {
			require.NoError(t, r.err)
			assert.Equal(t, id, r.id)
		}

		txm.transactionsMapLock.RLock()
		got, has := txm.transactions[id]
		txm.transactionsMapLock.RUnlock()
		require.True(t, has)
		assert.Equal(t, id, got.ID)
		st, err := txm.GetStatus(id)
		require.NoError(t, err)
		assert.Equal(t, commontypes.Pending, st)
	})

	// Defense regression: an invalid FromAddress must be rejected at the entry
	// point with a clean error rather than panic deep in the broadcast loop
	// (where xdr.MustAddress used to crash the goroutine on untrusted input).
	t.Run("invalid FromAddress rejected at Enqueue", func(t *testing.T) {
		require.NotPanics(t, func() {
			_, err := txm.Enqueue(t.Context(), TxRequest{
				FromAddress: "not-a-valid-strkey",
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
			assert.Contains(t, err.Error(), "invalid FromAddress")
		})
	})

	t.Run("contract strkey is rejected as FromAddress", func(t *testing.T) {
		// Contract addresses (C…) must not be accepted as a transaction source —
		// only ed25519 account ids (G…) are valid sources.
		require.NotPanics(t, func() {
			_, err := txm.Enqueue(t.Context(), TxRequest{
				FromAddress: "CA7QYNF7SOWQ3GLR2BGMZEHXAVIRZA4KVWLTJJFC7MGXUA74P7UJUWDA",
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
			assert.Contains(t, err.Error(), "invalid FromAddress")
		})
	})
}

func TestStellarTxm_Enqueue_AutoID(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	txID, err := txm.Enqueue(t.Context(), TxRequest{
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
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
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

	oldID, err := txm.Enqueue(t.Context(), TxRequest{Operations: []txnbuild.Operation{op}})
	require.NoError(t, err)

	newID, err := txm.Enqueue(t.Context(), TxRequest{Operations: []txnbuild.Operation{op}})
	require.NoError(t, err, "new tx should be accepted after evicting oldest")
	assert.NotEqual(t, oldID, newID)

	// Old tx should be marked Failed with the drop reason; its Done channel closed.
	oldStatus, err := txm.GetStatus(oldID)
	require.NoError(t, err)
	assert.Equal(t, commontypes.Failed, oldStatus, "evicted tx should be Failed")

	txm.transactionsMapLock.RLock()
	oldTx := txm.transactions[oldID]
	newTx := txm.transactions[newID]
	txm.transactionsMapLock.RUnlock()
	require.NotNil(t, oldTx)
	require.NotNil(t, newTx)
	oldTx.mu.RLock()
	assert.Equal(t, string(DropReasonChannelFullOldestEvicted), oldTx.ResultCode)
	oldTx.mu.RUnlock()
	select {
	case <-oldTx.Done:
		// expected — closeDone was called
	default:
		t.Fatal("evicted tx's Done channel should be closed")
	}
	// New tx should still be in the channel, not terminated.
	newTx.mu.RLock()
	assert.Equal(t, commontypes.Pending, newTx.Status)
	newTx.mu.RUnlock()
}

// --- GetStatus tests ---

func TestStellarTxm_GetStatus(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
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
		txID, err := txm.Enqueue(t.Context(), TxRequest{
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
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
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
		txID, err := txm.Enqueue(t.Context(), TxRequest{
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
		txID, err := txm.Enqueue(t.Context(), TxRequest{
			FromAddress: testAddress,
			Operations:  []txnbuild.Operation{testInvokeNoopOp()},
		})
		require.NoError(t, err)

		fee := big.NewInt(12345)
		txm.transactionsMapLock.RLock()
		tx := txm.transactions[txID]
		txm.transactionsMapLock.RUnlock()
		tx.mu.Lock()
		tx.Status = commontypes.Finalized
		tx.TxHash = "hash-finalized"
		tx.Fee = fee
		tx.ResultXDR = "result-xdr"
		tx.ResultMetaXDR = "meta-xdr"
		tx.mu.Unlock()

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
		txID, err := txm.Enqueue(t.Context(), TxRequest{
			FromAddress: testAddress,
			Operations:  []txnbuild.Operation{testInvokeNoopOp()},
		})
		require.NoError(t, err)

		txm.transactionsMapLock.RLock()
		tx := txm.transactions[txID]
		txm.transactionsMapLock.RUnlock()
		tx.mu.Lock()
		tx.Status = commontypes.Failed
		tx.TxHash = "hash-failed"
		tx.ResultXDR = "failed-result-xdr"
		tx.ResultCode = "contract_error"
		tx.mu.Unlock()

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
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	t.Run("empty ID", func(t *testing.T) {
		_, err := txm.GetTransactionFee("")
		require.Error(t, err)
	})

	t.Run("not finalized", func(t *testing.T) {
		txID, err := txm.Enqueue(t.Context(), TxRequest{
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
		txID, err := txm.Enqueue(t.Context(), TxRequest{
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

		txm.transactionsMapLock.RLock()
		tx := txm.transactions[txID]
		txm.transactionsMapLock.RUnlock()
		tx.mu.Lock()
		tx.Status = commontypes.Finalized
		tx.Fee = big.NewInt(12345)
		tx.mu.Unlock()

		fee, err := txm.GetTransactionFee(txID)
		require.NoError(t, err)
		assert.Equal(t, big.NewInt(12345), fee)
	})
}

// --- closeDone tests ---

// closeDone must be safe under concurrent calls. The pre-fix implementation
// guarded a check-then-close pattern with a *shared* RLock, so two goroutines
// could both observe tx.Done as not-yet-closed and both call close(tx.Done),
// panicking on the second close. sync.Once provides a structural exactly-once
// guarantee that this test exercises directly.
func TestStellarTxm_CloseDone_ConcurrentSafe(t *testing.T) {
	t.Parallel()

	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	tx := &StellarTx{ID: "concurrent-close", Done: make(chan struct{})}

	const goroutines = 64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			require.NotPanics(t, func() { txm.closeDone(tx) })
		}()
	}
	close(start)
	wg.Wait()

	select {
	case <-tx.Done:
	default:
		t.Fatal("Done was not closed after closeDone calls")
	}

	// Subsequent calls must remain idempotent and panic-free.
	require.NotPanics(t, func() { txm.closeDone(tx) })
	require.NotPanics(t, func() { txm.closeDone(tx) })
}

// --- InflightCount test ---

func TestStellarTxm_InflightCount(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
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
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"},
	}

	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
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
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"},
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status: protocolrpc.TransactionStatusSuccess,
			},
		},
	}

	cfg := Config{ConfirmPollInterval: config.MustNewDuration(100 * time.Millisecond)}
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
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"},
		getTransactionErr:   fmt.Errorf("not found"),
		getLatestLedgerHook: func() (protocolrpc.GetLatestLedgerResponse, error) {
			return protocolrpc.GetLatestLedgerResponse{Sequence: latestLedgerSeq.Load()}, nil
		},
	}

	cfg := Config{
		ConfirmPollInterval: config.MustNewDuration(100 * time.Millisecond),
		MaxTxRetryAttempts:  ptr(uint64(0)),
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
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"},
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status: protocolrpc.TransactionStatusSuccess,
			},
		},
	}

	cfg := Config{ConfirmPollInterval: config.MustNewDuration(100 * time.Millisecond)}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
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
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"},
		// Never return success — tx stays unconfirmed
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status: protocolrpc.TransactionStatusNotFound,
			},
		},
	}

	cfg := Config{ConfirmPollInterval: config.MustNewDuration(100 * time.Millisecond)}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
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

	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	client := newTestClient(mock)
	seq, err := txm.getSequenceNumber(t.Context(), client, testAddress)
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

	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	client := newTestClient(mock)
	_, err = txm.getSequenceNumber(t.Context(), client, testAddress)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStellarTxm_GetSequenceNumber_EmptyAddress(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	client := newTestClient(mock)
	_, err = txm.getSequenceNumber(t.Context(), client, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "address is required")
}

// Defense regression: getSequenceNumber must NOT panic on a malformed strkey.
// Earlier code used xdr.MustAddress which panics on bad input; this test pins
// the contract that the helper now returns a clean error instead.
func TestStellarTxm_GetSequenceNumber_InvalidAddress(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	client := newTestClient(mock)

	require.NotPanics(t, func() {
		_, err := txm.getSequenceNumber(t.Context(), client, "not-a-stellar-address")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid stellar account address")
	})
}

// Defense regression: getSequenceNumber must NOT panic if the RPC returns a
// ledger entry of an unexpected type (e.g. an offer or trustline entry under
// the account key). The pre-fix entry.MustAccount() would panic; we now error.
func TestStellarTxm_GetSequenceNumber_NonAccountLedgerEntry(t *testing.T) {
	t.Parallel()

	// Build a ledger entry of a different type (Offer) — the SDK populates
	// only the matching arm, so MustAccount() on this would panic.
	nonAccount := xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeOffer,
		Offer: &xdr.OfferEntry{
			SellerId: xdr.MustAddress(testAddress),
			OfferId:  1,
		},
	}
	nonAccountXDR, err := xdr.MarshalBase64(nonAccount)
	require.NoError(t, err)

	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{{DataXDR: nonAccountXDR}},
		},
	}

	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	client := newTestClient(mock)

	require.NotPanics(t, func() {
		_, err := txm.getSequenceNumber(t.Context(), client, testAddress)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not an account entry")
	})
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
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(&mockRPCClient{}), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	t.Run("no operations", func(t *testing.T) {
		t.Parallel()
		_, err := txm.Simulate(t.Context(), TxRequest{FromAddress: testAddress})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least one operation")
	})
}

func TestStellarTxm_Simulate_getClientError(t *testing.T) {
	t.Parallel()
	bad := func(context.Context) (RPCClient, error) { return nil, fmt.Errorf("unreachable") }
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, bad, chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	_, err = txm.Simulate(t.Context(), TxRequest{
		FromAddress: testAddress,
		Operations:  []txnbuild.Operation{testInvokeNoopOp()},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get client")
}

func TestStellarTxm_Simulate_LatestLedgerError(t *testing.T) {
	t.Parallel()
	inner := &mockRPCClient{getLatestLedgerErr: fmt.Errorf("ledger err")}
	c := newTestClient(inner)
	getClient := func(context.Context) (RPCClient, error) { return c, nil }
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, getClient, chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	_, err = txm.Simulate(t.Context(), TxRequest{
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
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	res, err := txm.Simulate(t.Context(), TxRequest{
		FromAddress: testAddress,
		Operations:  []txnbuild.Operation{testInvokeNoopOp()},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(5), res.MinResourceFee)
}

// --- maybeRetry: broadcast channel full ---

// blockingAfterFirstSimulateRPC runs the first Simulate in "started, then block until
// unblock is closed" mode so the broadcast loop can be stuck mid-tx while the channel holds another tx.
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
		sendTransactionResp: protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "h"},
	}
	bmock := &blockingAfterFirstSimulateRPC{
		mockRPCClient: inner,
		started:       make(chan struct{}),
		unblock:       make(chan struct{}),
	}
	getClient := func(context.Context) (RPCClient, error) { return bmock, nil }
	cfg := Config{
		BroadcastChanSize:  ptr(uint(1)),
		MaxTxRetryAttempts: ptr(uint64(3)),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, getClient, chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	defer func() { _ = txm.Close() }()

	op := testInvokeNoopOp()

	_, err = txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{op}})
	require.NoError(t, err)
	// Wait until the first tx is inside Simulate (broadcast loop is blocked there).
	<-bmock.started
	// Buffer size 1: the second tx sits in the channel while the first tx is still in sim.
	_, err = txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{op}})
	require.NoError(t, err)

	retried := txm.maybeRetry(t.Context(), &UnconfirmedTx{
		Tx:   &StellarTx{ID: "retry"},
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
		sendTransactionResp:  protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"},
		getTransactionResp: protocolrpc.GetTransactionResponse{
			LedgerCloseTime: 1_700_000_000,
			TransactionDetails: protocolrpc.TransactionDetails{
				Status:        protocolrpc.TransactionStatusSuccess,
				ResultXDR:     resultB64,
				ResultMetaXDR: metaB64,
			},
		},
	}
	cfg := Config{ConfirmPollInterval: config.MustNewDuration(100 * time.Millisecond)}
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

	txm.transactionsMapLock.RLock()
	tracked := txm.transactions[txID]
	txm.transactionsMapLock.RUnlock()
	require.NotNil(t, tracked)
	tracked.mu.RLock()
	assert.Equal(t, big.NewInt(40_200), tracked.Fee)
	assert.Equal(t, int64(1_700_000_000), tracked.LedgerCloseTime)
	assert.Equal(t, resultB64, tracked.ResultXDR)
	assert.Equal(t, metaB64, tracked.ResultMetaXDR)
	tracked.mu.RUnlock()

	result, err := txm.GetTransactionResult(txID)
	require.NoError(t, err)
	assert.Equal(t, "test-hash", result.Hash)
	assert.Equal(t, commontypes.Finalized, result.Status)
	assert.Equal(t, big.NewInt(40_200), result.Fee)
	assert.Equal(t, int64(1_700_000_000), result.LedgerCloseTime)
	assert.Equal(t, resultB64, result.ResultXDR)
	assert.Equal(t, metaB64, result.ResultMetaXDR)
	assert.NoError(t, result.Error)
}

// --- Prune tests ---

// twoHours is the default PruneTxExpiration, shared across prune tests.
const twoHours = 2 * time.Hour

// TestStellarTxm_PruneTerminal_OnlyEvictsTerminalPastCutoff verifies the core
// pruning invariants without running the full goroutine lifecycle:
//   - in-flight (Pending/Unconfirmed) txs are never pruned regardless of age
//   - terminal txs with zero TerminalTime are never pruned (shouldn't happen in prod, defensive)
//   - terminal txs not yet past PruneTxExpiration are kept
//   - terminal txs past PruneTxExpiration are removed
func TestStellarTxm_PruneTerminal_OnlyEvictsTerminalPastCutoff(t *testing.T) {
	t.Parallel()

	cfg := Config{
		PruneTxExpiration: config.MustNewDuration(twoHours),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(&mockRPCClient{}), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	now := time.Now()

	inject := func(id string, status commontypes.TransactionStatus, terminalTime time.Time) {
		tx := &StellarTx{
			ID:   id,
			Done: make(chan struct{}),
		}
		tx.mu.Lock()
		tx.Status = status
		tx.TerminalTime = terminalTime
		tx.mu.Unlock()
		if isTerminalStatus(status) {
			close(tx.Done)
		}
		txm.transactionsMapLock.Lock()
		txm.transactions[id] = tx
		txm.transactionsMapLock.Unlock()
	}

	inject("inflight-pending", commontypes.Pending, time.Time{})
	inject("inflight-unconfirmed", commontypes.Unconfirmed, time.Time{})
	inject("terminal-no-time", commontypes.Finalized, time.Time{})                      // TerminalTime unset — must not be pruned
	inject("terminal-fresh", commontypes.Finalized, now.Add(-twoHours/2))               // within window
	inject("terminal-expired-finalized", commontypes.Finalized, now.Add(-twoHours-time.Second)) // past window
	inject("terminal-expired-failed", commontypes.Failed, now.Add(-twoHours-time.Second))     // past window

	txm.pruneTerminal()

	txm.transactionsMapLock.RLock()
	_, hasInflightPending := txm.transactions["inflight-pending"]
	_, hasInflightUnconfirmed := txm.transactions["inflight-unconfirmed"]
	_, hasTerminalNoTime := txm.transactions["terminal-no-time"]
	_, hasTerminalFresh := txm.transactions["terminal-fresh"]
	_, hasExpiredFinalized := txm.transactions["terminal-expired-finalized"]
	_, hasExpiredFailed := txm.transactions["terminal-expired-failed"]
	txm.transactionsMapLock.RUnlock()

	assert.True(t, hasInflightPending, "in-flight Pending tx must not be pruned")
	assert.True(t, hasInflightUnconfirmed, "in-flight Unconfirmed tx must not be pruned")
	assert.True(t, hasTerminalNoTime, "terminal tx with zero TerminalTime must not be pruned")
	assert.True(t, hasTerminalFresh, "terminal tx within retention window must not be pruned")
	assert.False(t, hasExpiredFinalized, "expired Finalized tx must be pruned")
	assert.False(t, hasExpiredFailed, "expired Failed tx must be pruned")
}

// TestStellarTxm_TerminalTime_SetOnFirstTerminalWrite verifies that
// updateTransactionStatus stamps TerminalTime exactly once and does not overwrite
// it on a subsequent terminal write (e.g. double-failed).
func TestStellarTxm_TerminalTime_SetOnFirstTerminalWrite(t *testing.T) {
	t.Parallel()

	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(&mockRPCClient{}), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	tx := &StellarTx{ID: "tt-test", Done: make(chan struct{})}
	txm.transactionsMapLock.Lock()
	txm.transactions[tx.ID] = tx
	txm.transactionsMapLock.Unlock()

	before := time.Now()
	txm.updateTransactionStatus(tx, commontypes.Failed)
	after := time.Now()

	tx.mu.RLock()
	first := tx.TerminalTime
	tx.mu.RUnlock()

	assert.False(t, first.Before(before), "TerminalTime should be >= time before call")
	assert.False(t, first.After(after), "TerminalTime should be <= time after call")

	// A second terminal write must not overwrite TerminalTime.
	txm.updateTransactionStatus(tx, commontypes.Finalized)

	tx.mu.RLock()
	second := tx.TerminalTime
	tx.mu.RUnlock()

	assert.Equal(t, first, second, "TerminalTime must not be overwritten on subsequent terminal write")
}

// TestStellarTxm_PruneTerminal_InFlightNeverPruned verifies that a tx which
// was in-flight for a long time (enqueued long ago) is not pruned until
// PruneTxExpiration elapses *after* it reaches a terminal state.
func TestStellarTxm_PruneTerminal_LongInFlightNotPrunedUntilTerminalExpiry(t *testing.T) {
	t.Parallel()

	cfg := Config{
		PruneTxExpiration: config.MustNewDuration(2 * time.Hour),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(&mockRPCClient{}), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	now := time.Now()

	tx := &StellarTx{
		ID:        "long-inflight",
		Timestamp: now.Add(-twoHours + time.Minute), // enqueued nearly 2h ago
		Done:      make(chan struct{}),
	}
	tx.mu.Lock()
	tx.Status = commontypes.Finalized
	tx.TerminalTime = now // just finalized
	tx.mu.Unlock()
	close(tx.Done)
	txm.transactionsMapLock.Lock()
	txm.transactions[tx.ID] = tx
	txm.transactionsMapLock.Unlock()

	txm.pruneTerminal()

	txm.transactionsMapLock.RLock()
	_, stillPresent := txm.transactions["long-inflight"]
	txm.transactionsMapLock.RUnlock()

	assert.True(t, stillPresent, "tx that just finalized must not be pruned even if enqueued long ago")
}

// TestStellarTxm_PruneLoop_RunsWhenIntervalPositive verifies that the prune
// goroutine is started when PruneInterval > 0 and that it actually removes
// expired terminal transactions without manual intervention.
func TestStellarTxm_PruneLoop_RunsWhenIntervalPositive(t *testing.T) {
	t.Parallel()

	cfg := Config{
		PruneInterval:     config.MustNewDuration(50 * time.Millisecond),
		PruneTxExpiration: config.MustNewDuration(0), // expire immediately — any terminal time qualifies
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(&mockRPCClient{}), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	tx := &StellarTx{
		ID:   "prune-loop-tx",
		Done: make(chan struct{}),
	}
	tx.mu.Lock()
	tx.Status = commontypes.Finalized
	tx.TerminalTime = time.Now().Add(-time.Second) // expired: 1s ago, expiration window = 0
	tx.mu.Unlock()
	close(tx.Done)
	txm.transactionsMapLock.Lock()
	txm.transactions[tx.ID] = tx
	txm.transactionsMapLock.Unlock()

	require.Eventually(t, func() bool {
		txm.transactionsMapLock.RLock()
		_, present := txm.transactions["prune-loop-tx"]
		txm.transactionsMapLock.RUnlock()
		return !present
	}, 2*time.Second, 25*time.Millisecond, "pruneLoop should have evicted the expired terminal tx")
}

// TestStellarTxm_PruneImmediateWhenIntervalZero verifies that PruneInterval == 0
// disables the background prune loop but still evicts terminal txs synchronously
// via updateTransactionStatus, so memory does not grow without bound.
func TestStellarTxm_PruneImmediateWhenIntervalZero(t *testing.T) {
	t.Parallel()

	cfg := Config{
		PruneInterval: config.MustNewDuration(0),
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(&mockRPCClient{}), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	tx := &StellarTx{
		ID:   "immediate-prune-tx",
		Done: make(chan struct{}),
	}
	txm.transactionsMapLock.Lock()
	txm.transactions[tx.ID] = tx
	txm.transactionsMapLock.Unlock()

	txm.updateTransactionStatus(tx, commontypes.Finalized)

	txm.transactionsMapLock.RLock()
	_, stillPresent := txm.transactions["immediate-prune-tx"]
	txm.transactionsMapLock.RUnlock()

	assert.False(t, stillPresent, "terminal tx must be evicted immediately when PruneInterval==0")
}

func TestStellarTxm_ConfirmLoop_TerminalContractFailureDoesNotRetry(t *testing.T) {
	t.Parallel()
	accountXDR := buildAccountEntryXDR(t, testAddress, 100)
	resultB64 := buildFailedInvokeHostFunctionResultXDR(t, xdr.InvokeHostFunctionResultCodeInvokeHostFunctionTrapped)
	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{Entries: []protocolrpc.LedgerEntryResult{{DataXDR: accountXDR}}},
		getLatestLedgerResp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		sendTransactionResp:  protocolrpc.SendTransactionResponse{Status: stellarcore.TXStatusPending, Hash: "test-hash"},
		simulateResp:         protocolrpc.SimulateTransactionResponse{MinResourceFee: 10_000},
		getTransactionResp: protocolrpc.GetTransactionResponse{
			LedgerCloseTime: 1_700_000_001,
			TransactionDetails: protocolrpc.TransactionDetails{
				Status:    protocolrpc.TransactionStatusFailed,
				ResultXDR: resultB64,
			},
		},
	}
	cfg := Config{ConfirmPollInterval: config.MustNewDuration(20 * time.Millisecond)}
	txm, err := New(logger.Test(t), &mockKeystore{}, cfg, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)
	require.NoError(t, txm.Start(t.Context()))
	defer txm.Close()

	txID, err := txm.Enqueue(t.Context(), TxRequest{FromAddress: testAddress, Operations: []txnbuild.Operation{testInvokeNoopOp()}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, err := txm.GetStatus(txID)
		return err == nil && st == commontypes.Failed
	}, 5*time.Second, 20*time.Millisecond)

	txm.transactionsMapLock.RLock()
	tracked := txm.transactions[txID]
	txm.transactionsMapLock.RUnlock()
	require.NotNil(t, tracked)
	assert.Equal(t, uint64(0), tracked.Attempt.Load())
	tracked.mu.RLock()
	assert.Equal(t, int64(1_700_000_001), tracked.LedgerCloseTime)
	assert.Equal(t, resultB64, tracked.ResultXDR)
	assert.Equal(t, xdr.InvokeHostFunctionResultCodeInvokeHostFunctionTrapped.String(), tracked.ResultCode)
	tracked.mu.RUnlock()

	result, err := txm.GetTransactionResult(txID)
	require.NoError(t, err)
	assert.Equal(t, "test-hash", result.Hash)
	assert.Equal(t, commontypes.Failed, result.Status)
	assert.Equal(t, int64(1_700_000_001), result.LedgerCloseTime)
	assert.Equal(t, resultB64, result.ResultXDR)
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), xdr.InvokeHostFunctionResultCodeInvokeHostFunctionTrapped.String())

	store := txm.accountStore.GetTxStore(testAddress)
	require.NotNil(t, store)
	assert.Equal(t, int64(102), store.GetNextSequence(), "on-chain FAILED consumed sequence 101, so the next tx should use 102")
}

// --- Concurrency test ---
//
// TestStellarTxm_Concurrency_GetResultAndUpdateOnDifferentTxs runs concurrent
// GetTransactionResult/GetStatus readers and updateTransaction* writers on different
// txs, plus a concurrent pruneTerminal, to verify no deadlock and no data races.
func TestStellarTxm_Concurrency_GetResultAndUpdateOnDifferentTxs(t *testing.T) {
	t.Parallel()

	mock := &mockRPCClient{}
	txm, err := New(logger.Test(t), &mockKeystore{}, Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	const numTxs = 20

	// Inject txs directly into the map (bypass Enqueue/broadcastChan so the
	// broadcast loop never touches them).
	txIDs := make([]string, numTxs)
	txs := make([]*StellarTx, numTxs)
	for i := 0; i < numTxs; i++ {
		id := fmt.Sprintf("concurrency-tx-%d", i)
		tx := &StellarTx{
			ID:   id,
			Done: make(chan struct{}),
		}
		tx.mu.Lock()
		tx.Status = commontypes.Pending
		tx.mu.Unlock()
		txm.transactionsMapLock.Lock()
		txm.transactions[id] = tx
		txm.transactionsMapLock.Unlock()
		txIDs[i] = id
		txs[i] = tx
	}

	// Writers update non-terminal fields so txs stay in the map for the full test
	// (a terminal status would evict via maybeEvictTerminalTx since PruneInterval==0).
	const writerIterations = 200
	const numWriters = numTxs // one writer per tx
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	wg.Add(numWriters)
	for i := 0; i < numWriters; i++ {
		tx := txs[i]
		go func() {
			defer wg.Done()
			fee := big.NewInt(int64(i + 1))
			for j := 0; j < writerIterations; j++ {
				if ctx.Err() != nil {
					return
				}
				txm.updateTransactionFee(tx, fee)
				txm.updateTransactionHash(tx, fmt.Sprintf("hash-%d-%d", i, j))
				txm.updateTransactionResultXDR(tx, fmt.Sprintf("xdr-%d-%d", i, j))
				txm.incrementTransactionAttempt(tx)
			}
		}()
	}

	// Readers target a different tx than any single writer to maximize cross-tx contention.
	const numReaders = 20
	const readerIterations = 200
	readerErrs := make(chan error, numReaders*readerIterations)
	wg.Add(numReaders)
	for r := 0; r < numReaders; r++ {
		go func() {
			defer wg.Done()
			for j := 0; j < readerIterations; j++ {
				if ctx.Err() != nil {
					return
				}
				// Read a tx owned by a different writer (r % numTxs, while writer r
				// writes txs[r]). This guarantees cross-tx lock contention.
				idx := (r + 1) % numTxs
				_, err := txm.GetTransactionResult(txIDs[idx])
				if err != nil {
					readerErrs <- fmt.Errorf("GetTransactionResult(%s): %w", txIDs[idx], err)
					return
				}
				// Also exercise GetStatus (per-tx RLock on Status).
				if _, err := txm.GetStatus(txIDs[idx]); err != nil {
					readerErrs <- fmt.Errorf("GetStatus(%s): %w", txIDs[idx], err)
					return
				}
			}
		}()
	}

	// Concurrent pruneTerminal stresses the map→per-tx nested path.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < readerIterations; j++ {
			if ctx.Err() != nil {
				return
			}
			txm.pruneTerminal()
		}
	}()

	// Timeout detects deadlock (goroutines exit on ctx.Err()).
	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-ctx.Done():
		t.Fatalf("concurrency test timed out — likely deadlock in lock ordering")
	}

	close(readerErrs)
	for err := range readerErrs {
		t.Fatalf("reader error: %v", err)
	}

	// Each writer incremented Attempt exactly writerIterations times.
	for i, tx := range txs {
		assert.Equal(t, uint64(writerIterations), tx.Attempt.Load(),
			"tx %d Attempt should be exactly %d after concurrent increments", i, writerIterations)
	}
}
