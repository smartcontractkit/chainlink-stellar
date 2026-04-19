package txm

import (
	"context"
	"fmt"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
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

	getTransactionCalls atomic.Int32
}

func (m *mockRPCClient) SimulateTransaction(_ context.Context, _ protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
	return m.simulateResp, m.simulateErr
}
func (m *mockRPCClient) SendTransaction(_ context.Context, _ protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
	return m.sendTransactionResp, m.sendTransactionErr
}
func (m *mockRPCClient) GetTransaction(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
	m.getTransactionCalls.Add(1)
	return m.getTransactionResp, m.getTransactionErr
}
func (m *mockRPCClient) GetLedgerEntries(_ context.Context, _ protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
	return m.getLedgerEntriesResp, m.getLedgerEntriesErr
}
func (m *mockRPCClient) GetEvents(_ context.Context, _ protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error) {
	return m.getEventsResp, m.getEventsErr
}
func (m *mockRPCClient) GetLatestLedger(_ context.Context) (protocolrpc.GetLatestLedgerResponse, error) {
	return m.getLatestLedgerResp, m.getLatestLedgerErr
}
func (m *mockRPCClient) GetLedgers(_ context.Context, _ protocolrpc.GetLedgersRequest) (protocolrpc.GetLedgersResponse, error) {
	return m.getLedgersResp, m.getLedgersErr
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

func newTestClient(mock *mockRPCClient) *ccvclient.Client {
	return ccvclient.NewClientFromInterfaceWithConfig(mock, ccvclient.ClientConfig{
		LedgerCacheTTL: 0,
		PollInterval:   10 * time.Millisecond,
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
	txm, err := New(logger.Nop(), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)
	require.NotNil(t, txm)
	assert.Equal(t, "StellarTxm", txm.Name())
}

// --- Lifecycle tests ---

func TestStellarTxm_StartStop(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Nop(), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
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
	txm, err := New(logger.Nop(), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
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
	txm, err := New(logger.Nop(), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	t.Run("missing ContractID", func(t *testing.T) {
		_, err := txm.Enqueue(context.Background(), TxRequest{
			FunctionName: "transfer",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ContractID is required")
	})

	t.Run("missing FunctionName", func(t *testing.T) {
		_, err := txm.Enqueue(context.Background(), TxRequest{
			ContractID: "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "FunctionName is required")
	})

	t.Run("duplicate ID", func(t *testing.T) {
		id := "dup-test-id"
		_, err := txm.Enqueue(context.Background(), TxRequest{
			ID:           id,
			ContractID:   "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
			FunctionName: "init",
		})
		require.NoError(t, err)

		_, err = txm.Enqueue(context.Background(), TxRequest{
			ID:           id,
			ContractID:   "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
			FunctionName: "init",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")
	})
}

func TestStellarTxm_Enqueue_AutoID(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Nop(), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	txID, err := txm.Enqueue(context.Background(), TxRequest{
		ContractID:   "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
		FunctionName: "init",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, txID)
}

func TestStellarTxm_Enqueue_ChannelFull(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	cfg := Config{BroadcastChanSize: ptr(uint(1))}
	txm, err := New(logger.Nop(), &mockKeystore{}, cfg, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	_, err = txm.Enqueue(context.Background(), TxRequest{
		ContractID:   "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
		FunctionName: "init",
	})
	require.NoError(t, err)

	_, err = txm.Enqueue(context.Background(), TxRequest{
		ContractID:   "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
		FunctionName: "init",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broadcast channel full")
}

// --- GetStatus tests ---

func TestStellarTxm_GetStatus(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Nop(), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
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
			ContractID:   "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
			FunctionName: "init",
		})
		require.NoError(t, err)

		status, err := txm.GetStatus(txID)
		require.NoError(t, err)
		assert.Equal(t, commontypes.Pending, status)
	})
}

// --- GetTransactionFee tests ---

func TestStellarTxm_GetTransactionFee(t *testing.T) {
	t.Parallel()
	mock := &mockRPCClient{}
	txm, err := New(logger.Nop(), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	t.Run("empty ID", func(t *testing.T) {
		_, err := txm.GetTransactionFee("")
		require.Error(t, err)
	})

	t.Run("not finalized", func(t *testing.T) {
		txID, err := txm.Enqueue(context.Background(), TxRequest{
			ContractID:   "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
			FunctionName: "init",
		})
		require.NoError(t, err)

		_, err = txm.GetTransactionFee(txID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not finalized")
	})

	t.Run("finalized with fee", func(t *testing.T) {
		txID, err := txm.Enqueue(context.Background(), TxRequest{
			ContractID:   "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
			FunctionName: "init",
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
	txm, err := New(logger.Nop(), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
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
	}

	txm, err := New(logger.Nop(), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)

	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	txID, err := txm.Enqueue(context.Background(), TxRequest{
		FromAddress:  testAddress,
		ContractID:   "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
		FunctionName: "init",
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
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status: protocolrpc.TransactionStatusSuccess,
			},
		},
	}

	cfg := Config{ConfirmPollInterval: config.MustNewDuration(100 * time.Millisecond)}
	txm, err := New(logger.Nop(), &mockKeystore{}, cfg, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)

	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	txID, err := txm.Enqueue(context.Background(), TxRequest{
		FromAddress:  testAddress,
		ContractID:   "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
		FunctionName: "init",
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

	mock := &mockRPCClient{
		getLedgerEntriesResp: protocolrpc.GetLedgerEntriesResponse{
			Entries: []protocolrpc.LedgerEntryResult{
				{DataXDR: accountXDR},
			},
		},
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1000},
		getTransactionErr:   fmt.Errorf("not found"),
	}

	cfg := Config{
		ConfirmPollInterval: config.MustNewDuration(100 * time.Millisecond),
		MaxTxRetryAttempts:  ptr(uint64(0)),
	}
	txm, err := New(logger.Nop(), &mockKeystore{}, cfg, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)

	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	txID, err := txm.Enqueue(context.Background(), TxRequest{
		FromAddress:  testAddress,
		ContractID:   "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
		FunctionName: "init",
	})
	require.NoError(t, err)

	// Wait for broadcast loop to pick it up
	require.Eventually(t, func() bool {
		status, _ := txm.GetStatus(txID)
		return status == commontypes.Unconfirmed
	}, 5*time.Second, 50*time.Millisecond)

	// Now simulate the ledger advancing past MaxLedger (which is 1000+50=1050)
	mock.getLatestLedgerResp = protocolrpc.GetLatestLedgerResponse{Sequence: 2000}

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
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status: protocolrpc.TransactionStatusSuccess,
			},
		},
	}

	cfg := Config{ConfirmPollInterval: config.MustNewDuration(100 * time.Millisecond)}
	txm, err := New(logger.Nop(), &mockKeystore{}, cfg, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)

	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := txm.EnqueueAndWait(ctx, TxRequest{
		FromAddress:  testAddress,
		ContractID:   "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
		FunctionName: "init",
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
		// Never return success — tx stays unconfirmed
		getTransactionResp: protocolrpc.GetTransactionResponse{
			TransactionDetails: protocolrpc.TransactionDetails{
				Status: protocolrpc.TransactionStatusNotFound,
			},
		},
	}

	cfg := Config{ConfirmPollInterval: config.MustNewDuration(100 * time.Millisecond)}
	txm, err := New(logger.Nop(), &mockKeystore{}, cfg, newTestGetClient(mock), "test-chain", "Test SDF Network ; September 2015")
	require.NoError(t, err)

	require.NoError(t, txm.Start(context.Background()))
	defer txm.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = txm.EnqueueAndWait(ctx, TxRequest{
		FromAddress:  testAddress,
		ContractID:   "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
		FunctionName: "init",
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

	txm, err := New(logger.Nop(), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
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

	txm, err := New(logger.Nop(), &mockKeystore{}, Config{}, newTestGetClient(mock), "test-chain", "")
	require.NoError(t, err)

	client := newTestClient(mock)
	_, err = txm.getSequenceNumber(context.Background(), client, testAddress)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

