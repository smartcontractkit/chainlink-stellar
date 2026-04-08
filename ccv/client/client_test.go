package ccvclient

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	t.Run("stores rpc client reference", func(t *testing.T) {
		rpc := rpcclient.NewClient("http://127.0.0.1:9", &http.Client{Timeout: time.Millisecond})
		c := NewClient(rpc)
		require.NotNil(t, c)
		assert.Same(t, rpc, c.RPC)
	})

	t.Run("allows nil client", func(t *testing.T) {
		c := NewClient(nil)
		require.NotNil(t, c)
		assert.Nil(t, c.RPC)
	})

	t.Run("applies default config", func(t *testing.T) {
		c := NewClient(nil)
		assert.Equal(t, 3*time.Second, c.cfg.LedgerCacheTTL)
		assert.NotNil(t, c.limiter)
	})

	t.Run("custom config disables rate limiting", func(t *testing.T) {
		cfg := ClientConfig{RateLimitPerSec: 0}
		c := NewClientWithConfig(nil, cfg)
		assert.Nil(t, c.limiter)
	})
}

// mockRPC implements RPCClient for testing.
type mockRPC struct {
	getLatestLedgerFn  func(ctx context.Context) (protocolrpc.GetLatestLedgerResponse, error)
	getTransactionFn   func(ctx context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error)
	latestLedgerCalls  int
	mu                 sync.Mutex
}

func (m *mockRPC) SimulateTransaction(_ context.Context, _ protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
	return protocolrpc.SimulateTransactionResponse{}, nil
}
func (m *mockRPC) SendTransaction(_ context.Context, _ protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
	return protocolrpc.SendTransactionResponse{}, nil
}
func (m *mockRPC) GetTransaction(ctx context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
	if m.getTransactionFn != nil {
		return m.getTransactionFn(ctx, req)
	}
	return protocolrpc.GetTransactionResponse{}, nil
}
func (m *mockRPC) GetLedgerEntries(_ context.Context, _ protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
	return protocolrpc.GetLedgerEntriesResponse{}, nil
}
func (m *mockRPC) GetEvents(_ context.Context, _ protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error) {
	return protocolrpc.GetEventsResponse{}, nil
}
func (m *mockRPC) GetLatestLedger(ctx context.Context) (protocolrpc.GetLatestLedgerResponse, error) {
	m.mu.Lock()
	m.latestLedgerCalls++
	m.mu.Unlock()
	if m.getLatestLedgerFn != nil {
		return m.getLatestLedgerFn(ctx)
	}
	return protocolrpc.GetLatestLedgerResponse{Sequence: 100}, nil
}
func (m *mockRPC) GetLedgers(_ context.Context, _ protocolrpc.GetLedgersRequest) (protocolrpc.GetLedgersResponse, error) {
	return protocolrpc.GetLedgersResponse{}, nil
}

func TestLatestLedger_Cache(t *testing.T) {
	mock := &mockRPC{}
	cfg := DefaultClientConfig()
	cfg.LedgerCacheTTL = 100 * time.Millisecond
	cfg.RateLimitPerSec = 0
	c := newClient(mock, cfg)

	ctx := context.Background()

	resp1, err := c.LatestLedger(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint32(100), resp1.Sequence)

	resp2, err := c.LatestLedger(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint32(100), resp2.Sequence)

	mock.mu.Lock()
	calls := mock.latestLedgerCalls
	mock.mu.Unlock()
	assert.Equal(t, 1, calls, "second call should hit cache")

	time.Sleep(150 * time.Millisecond)

	_, err = c.LatestLedger(ctx)
	require.NoError(t, err)

	mock.mu.Lock()
	calls = mock.latestLedgerCalls
	mock.mu.Unlock()
	assert.Equal(t, 2, calls, "should re-fetch after TTL")
}

func TestLatestLedger_CacheDisabled(t *testing.T) {
	mock := &mockRPC{}
	cfg := DefaultClientConfig()
	cfg.LedgerCacheTTL = 0
	cfg.RateLimitPerSec = 0
	c := newClient(mock, cfg)

	ctx := context.Background()
	_, _ = c.LatestLedger(ctx)
	_, _ = c.LatestLedger(ctx)

	mock.mu.Lock()
	calls := mock.latestLedgerCalls
	mock.mu.Unlock()
	assert.Equal(t, 2, calls, "every call should hit RPC when cache is disabled")
}

func txRespWithStatus(status string) protocolrpc.GetTransactionResponse {
	var resp protocolrpc.GetTransactionResponse
	resp.Status = status
	return resp
}

func TestPollTransaction_Success(t *testing.T) {
	callCount := 0
	mock := &mockRPC{
		getTransactionFn: func(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
			callCount++
			if callCount < 3 {
				return txRespWithStatus(protocolrpc.TransactionStatusNotFound), nil
			}
			resp := txRespWithStatus(protocolrpc.TransactionStatusSuccess)
			resp.Ledger = 42
			return resp, nil
		},
	}

	cfg := DefaultClientConfig()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.RateLimitPerSec = 0
	c := newClient(mock, cfg)

	resp, err := c.PollTransaction(context.Background(), "abc123", 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, protocolrpc.TransactionStatusSuccess, resp.Status)
	assert.Equal(t, uint32(42), resp.Ledger)
	assert.GreaterOrEqual(t, callCount, 3)
}

func TestPollTransaction_Timeout(t *testing.T) {
	mock := &mockRPC{
		getTransactionFn: func(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
			return txRespWithStatus(protocolrpc.TransactionStatusNotFound), nil
		},
	}

	cfg := DefaultClientConfig()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.RateLimitPerSec = 0
	c := newClient(mock, cfg)

	_, err := c.PollTransaction(context.Background(), "abc123", 50*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestPollTransaction_Failed(t *testing.T) {
	mock := &mockRPC{
		getTransactionFn: func(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
			return txRespWithStatus(protocolrpc.TransactionStatusFailed), nil
		},
	}

	cfg := DefaultClientConfig()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.RateLimitPerSec = 0
	c := newClient(mock, cfg)

	resp, err := c.PollTransaction(context.Background(), "abc123", 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, protocolrpc.TransactionStatusFailed, resp.Status)
}

func TestWaitRateLimit(t *testing.T) {
	t.Run("nil limiter returns immediately", func(t *testing.T) {
		c := &Client{}
		err := c.WaitRateLimit(context.Background())
		assert.NoError(t, err)
	})

	t.Run("cancelled context returns error", func(t *testing.T) {
		cfg := DefaultClientConfig()
		cfg.RateLimitPerSec = 0.001 // very slow
		cfg.RateLimitBurst = 1
		c := newClient(nil, cfg)

		_ = c.WaitRateLimit(context.Background()) // consume burst

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := c.WaitRateLimit(ctx)
		assert.Error(t, err)
	})
}
