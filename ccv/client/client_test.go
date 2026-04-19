package ccvclient

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubRPC is a minimal mock of RPCClient for unit tests.
type stubRPC struct {
	simulateResp protocolrpc.SimulateTransactionResponse
	simulateErr  error

	sendResp protocolrpc.SendTransactionResponse
	sendErr  error

	getTransactionResp func() protocolrpc.GetTransactionResponse
	getTransactionErr  error

	getLedgerEntriesResp protocolrpc.GetLedgerEntriesResponse
	getLedgerEntriesErr  error

	getEventsResp protocolrpc.GetEventsResponse
	getEventsErr  error

	latestLedgerResp  protocolrpc.GetLatestLedgerResponse
	latestLedgerErr   error
	latestLedgerCalls atomic.Int32

	getLedgersResp protocolrpc.GetLedgersResponse
	getLedgersErr  error
}

func (s *stubRPC) SimulateTransaction(_ context.Context, _ protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
	return s.simulateResp, s.simulateErr
}

func (s *stubRPC) SendTransaction(_ context.Context, _ protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
	return s.sendResp, s.sendErr
}

func (s *stubRPC) GetTransaction(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
	if s.getTransactionResp != nil {
		return s.getTransactionResp(), s.getTransactionErr
	}
	return protocolrpc.GetTransactionResponse{}, s.getTransactionErr
}

func (s *stubRPC) GetLedgerEntries(_ context.Context, _ protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
	return s.getLedgerEntriesResp, s.getLedgerEntriesErr
}

func (s *stubRPC) GetEvents(_ context.Context, _ protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error) {
	return s.getEventsResp, s.getEventsErr
}

func (s *stubRPC) GetLatestLedger(_ context.Context) (protocolrpc.GetLatestLedgerResponse, error) {
	s.latestLedgerCalls.Add(1)
	return s.latestLedgerResp, s.latestLedgerErr
}

func (s *stubRPC) GetLedgers(_ context.Context, _ protocolrpc.GetLedgersRequest) (protocolrpc.GetLedgersResponse, error) {
	return s.getLedgersResp, s.getLedgersErr
}

func txResp(status string) protocolrpc.GetTransactionResponse {
	return protocolrpc.GetTransactionResponse{
		TransactionDetails: protocolrpc.TransactionDetails{Status: status},
	}
}

// --- LatestLedger cache tests ---

func TestClient_LatestLedger_CacheHit(t *testing.T) {
	t.Parallel()

	stub := &stubRPC{
		latestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 42},
	}
	client := NewClientFromInterfaceWithConfig(stub, ClientConfig{
		LedgerCacheTTL: 5 * time.Second,
	})

	ctx := context.Background()

	resp1, err := client.LatestLedger(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint32(42), resp1.Sequence)

	resp2, err := client.LatestLedger(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint32(42), resp2.Sequence)

	assert.Equal(t, int32(1), stub.latestLedgerCalls.Load(),
		"only one RPC call should have been made")
}

func TestClient_LatestLedger_CacheExpiry(t *testing.T) {
	t.Parallel()

	stub := &stubRPC{
		latestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 42},
	}
	client := NewClientFromInterfaceWithConfig(stub, ClientConfig{
		LedgerCacheTTL: 10 * time.Millisecond,
	})

	ctx := context.Background()

	resp1, err := client.LatestLedger(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint32(42), resp1.Sequence)

	time.Sleep(20 * time.Millisecond)

	stub.latestLedgerResp = protocolrpc.GetLatestLedgerResponse{Sequence: 99}

	resp2, err := client.LatestLedger(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint32(99), resp2.Sequence)

	assert.Equal(t, int32(2), stub.latestLedgerCalls.Load(),
		"two RPC calls should have been made after cache expiry")
}

func TestClient_LatestLedger_CacheDisabled(t *testing.T) {
	t.Parallel()

	stub := &stubRPC{
		latestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1},
	}
	client := NewClientFromInterfaceWithConfig(stub, ClientConfig{
		LedgerCacheTTL: 0,
	})

	ctx := context.Background()

	_, err := client.LatestLedger(ctx)
	require.NoError(t, err)
	_, err = client.LatestLedger(ctx)
	require.NoError(t, err)

	assert.Equal(t, int32(2), stub.latestLedgerCalls.Load(),
		"every call should hit RPC when cache is disabled")
}

func TestClient_LatestLedger_NoCacheOnFirstCall(t *testing.T) {
	t.Parallel()

	stub := &stubRPC{
		latestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1},
	}
	client := NewClientFromInterface(stub)

	resp, err := client.LatestLedger(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint32(1), resp.Sequence)
	assert.Equal(t, int32(1), stub.latestLedgerCalls.Load())
}

// --- PollTransaction tests ---

func TestClient_PollTransaction_ImmediateSuccess(t *testing.T) {
	t.Parallel()

	stub := &stubRPC{
		getTransactionResp: func() protocolrpc.GetTransactionResponse {
			return txResp(protocolrpc.TransactionStatusSuccess)
		},
	}
	client := NewClientFromInterfaceWithConfig(stub, ClientConfig{
		PollInterval: 10 * time.Millisecond,
	})

	resp, err := client.PollTransaction(context.Background(), "abc123", 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, protocolrpc.TransactionStatusSuccess, resp.Status)
}

func TestClient_PollTransaction_EventualSuccess(t *testing.T) {
	t.Parallel()

	calls := atomic.Int32{}
	stub := &stubRPC{
		getTransactionResp: func() protocolrpc.GetTransactionResponse {
			n := calls.Add(1)
			if n < 3 {
				return txResp(protocolrpc.TransactionStatusNotFound)
			}
			return txResp(protocolrpc.TransactionStatusSuccess)
		},
	}
	client := NewClientFromInterfaceWithConfig(stub, ClientConfig{
		PollInterval: 10 * time.Millisecond,
	})

	resp, err := client.PollTransaction(context.Background(), "abc123", 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, protocolrpc.TransactionStatusSuccess, resp.Status)
	assert.GreaterOrEqual(t, calls.Load(), int32(3))
}

func TestClient_PollTransaction_Failed(t *testing.T) {
	t.Parallel()

	stub := &stubRPC{
		getTransactionResp: func() protocolrpc.GetTransactionResponse {
			return txResp(protocolrpc.TransactionStatusFailed)
		},
	}
	client := NewClientFromInterfaceWithConfig(stub, ClientConfig{
		PollInterval: 10 * time.Millisecond,
	})

	resp, err := client.PollTransaction(context.Background(), "abc123", 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, protocolrpc.TransactionStatusFailed, resp.Status)
}

func TestClient_PollTransaction_Timeout(t *testing.T) {
	t.Parallel()

	stub := &stubRPC{
		getTransactionResp: func() protocolrpc.GetTransactionResponse {
			return txResp(protocolrpc.TransactionStatusNotFound)
		},
	}
	client := NewClientFromInterfaceWithConfig(stub, ClientConfig{
		PollInterval: 10 * time.Millisecond,
	})

	_, err := client.PollTransaction(context.Background(), "abc123", 50*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "poll timed out")
}

func TestClient_PollTransaction_SwallowsTransientRPCErrors(t *testing.T) {
	t.Parallel()

	errorStub := &errorThenSuccessStub{errUntil: 2}
	client := NewClientFromInterfaceWithConfig(errorStub, ClientConfig{
		PollInterval: 10 * time.Millisecond,
	})

	resp, err := client.PollTransaction(context.Background(), "abc123", 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, protocolrpc.TransactionStatusSuccess, resp.Status)
	assert.GreaterOrEqual(t, errorStub.calls.Load(), int32(3),
		"should have retried past the transient errors")
}

// errorThenSuccessStub returns errors for the first N GetTransaction calls,
// then returns SUCCESS.
type errorThenSuccessStub struct {
	stubRPC
	calls    atomic.Int32
	errUntil int32
}

func (s *errorThenSuccessStub) GetTransaction(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
	n := s.calls.Add(1)
	if n <= s.errUntil {
		return protocolrpc.GetTransactionResponse{}, fmt.Errorf("transient network error")
	}
	return txResp(protocolrpc.TransactionStatusSuccess), nil
}

// --- Rate limiting tests ---

func TestClient_RateLimitDisabled(t *testing.T) {
	t.Parallel()

	stub := &stubRPC{
		latestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1},
	}
	client := NewClientFromInterfaceWithConfig(stub, ClientConfig{
		RateLimitPerSec: 0,
		LedgerCacheTTL:  0,
	})

	for i := 0; i < 50; i++ {
		_, err := client.GetLatestLedger(context.Background())
		require.NoError(t, err)
	}
	assert.Equal(t, int32(50), stub.latestLedgerCalls.Load())
}

// --- Constructor tests ---

func TestNewClient_DefaultConfig(t *testing.T) {
	t.Parallel()

	stub := &stubRPC{}
	client := NewClientFromInterface(stub)

	assert.Equal(t, 3*time.Second, client.cfg.LedgerCacheTTL)
	assert.Equal(t, 1*time.Second, client.cfg.PollInterval)
	assert.NotNil(t, client.limiter)
}

func TestNewClient_CustomConfig(t *testing.T) {
	t.Parallel()

	stub := &stubRPC{}
	cfg := ClientConfig{
		LedgerCacheTTL:  10 * time.Second,
		RateLimitPerSec: 5,
		RateLimitBurst:  10,
		PollInterval:    2 * time.Second,
	}
	client := NewClientFromInterfaceWithConfig(stub, cfg)

	assert.Equal(t, 10*time.Second, client.cfg.LedgerCacheTTL)
	assert.Equal(t, 2*time.Second, client.cfg.PollInterval)
	assert.NotNil(t, client.limiter)
}

func TestNewClient_ZeroPollIntervalDefaultsToOneSecond(t *testing.T) {
	t.Parallel()

	stub := &stubRPC{}
	client := NewClientFromInterfaceWithConfig(stub, ClientConfig{
		PollInterval: 0,
	})

	assert.Equal(t, 1*time.Second, client.cfg.PollInterval)
}

// --- Interface satisfaction ---

func TestClient_SatisfiesRPCClient(t *testing.T) {
	t.Parallel()

	stub := &stubRPC{}
	client := NewClientFromInterface(stub)
	var _ RPCClient = client
}
