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

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
)

func testNodes(urls ...string) []NodeConfig {
	nodes := make([]NodeConfig, len(urls))
	for i, url := range urls {
		nodes[i] = NodeConfig{
			Name:    fmt.Sprintf("node-%d", i),
			URL:     url,
			Timeout: 5 * time.Second,
		}
	}
	return nodes
}

func testCfg() ClientConfig {
	return ClientConfig{
		LedgerCacheTTL: 0,
		PollInterval:   10 * time.Millisecond,
	}
}

// --- Constructor tests ---

func TestNewClientFactory_NoNodes(t *testing.T) {
	t.Parallel()
	_, err := NewClientFactory(nil, "test", testCfg(), logger.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one node")
}

func TestNewClientFactory_Success(t *testing.T) {
	t.Parallel()
	nodes := testNodes("http://node1:8080")
	f, err := NewClientFactory(nodes, "test-chain", testCfg(), logger.Nop())
	require.NoError(t, err)
	assert.Equal(t, 1, f.NodeCount())
}

// --- GetClient tests using mock stubs ---

func TestClientFactory_GetClient_SingleNode(t *testing.T) {
	t.Parallel()

	stub := &stubRPC{
		latestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 100},
	}

	f := &ClientFactory{
		nodes:       testNodes("http://node1:8080"),
		chainID:     "test",
		cfg:         testCfg(),
		lggr:        logger.Nop(),
		clientCache: make(map[string]*Client),
	}

	// Manually seed a cached client with our stub
	f.clientCache["http://node1:8080"] = NewClientFromInterfaceWithConfig(stub, testCfg())

	client, err := f.GetClient()
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestClientFactory_GetClient_EvictsUnhealthyNode(t *testing.T) {
	t.Parallel()

	// Single-node setup: the bad node is the only cached client so it is
	// always tried by the fast path, making the test deterministic.
	// (Two-node setups with rand.Perm are 50/50 — the healthy node may be
	// returned before the bad one is ever checked.)
	failStub := &stubRPC{
		latestLedgerErr: fmt.Errorf("connection refused"),
	}

	f := &ClientFactory{
		nodes:       testNodes("http://bad:8080"),
		chainID:     "test",
		cfg:         testCfg(),
		lggr:        logger.Nop(),
		clientCache: make(map[string]*Client),
	}

	f.clientCache["http://bad:8080"] = NewClientFromInterfaceWithConfig(failStub, testCfg())

	// GetClient will fail (no healthy node), but should still evict the bad entry.
	_, _ = f.GetClient() //nolint:errcheck

	f.clientCacheMu.RLock()
	_, badExists := f.clientCache["http://bad:8080"]
	f.clientCacheMu.RUnlock()
	assert.False(t, badExists, "unhealthy node should be evicted from cache")
}

func TestClientFactory_GetClient_CachesClient(t *testing.T) {
	t.Parallel()

	calls := atomic.Int32{}
	stub := &stubRPC{
		latestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1},
	}

	f := &ClientFactory{
		nodes:       testNodes("http://node1:8080"),
		chainID:     "test",
		cfg:         testCfg(),
		lggr:        logger.Nop(),
		clientCache: make(map[string]*Client),
	}

	// Pre-seed cache
	f.clientCache["http://node1:8080"] = NewClientFromInterfaceWithConfig(stub, testCfg())

	// Override stub to count calls
	countingStub := &countingLatestLedgerStub{
		calls: &calls,
		resp:  protocolrpc.GetLatestLedgerResponse{Sequence: 1},
	}
	f.clientCache["http://node1:8080"] = NewClientFromInterfaceWithConfig(countingStub, testCfg())

	client1, err := f.GetClient()
	require.NoError(t, err)
	client2, err := f.GetClient()
	require.NoError(t, err)

	assert.Same(t, client1, client2, "same cached instance should be returned")
}

type countingLatestLedgerStub struct {
	stubRPC
	calls *atomic.Int32
	resp  protocolrpc.GetLatestLedgerResponse
}

func (s *countingLatestLedgerStub) GetLatestLedger(_ context.Context) (protocolrpc.GetLatestLedgerResponse, error) {
	s.calls.Add(1)
	return s.resp, nil
}

func TestClientFactory_GetClient_AllNodesDown(t *testing.T) {
	t.Parallel()

	failStub := &stubRPC{
		latestLedgerErr: fmt.Errorf("connection refused"),
	}

	f := &ClientFactory{
		nodes:       testNodes("http://bad1:8080", "http://bad2:8080"),
		chainID:     "test",
		cfg:         testCfg(),
		lggr:        logger.Nop(),
		clientCache: make(map[string]*Client),
	}

	f.clientCache["http://bad1:8080"] = NewClientFromInterfaceWithConfig(failStub, testCfg())
	f.clientCache["http://bad2:8080"] = NewClientFromInterfaceWithConfig(failStub, testCfg())

	// All cached clients fail health check, slow path tries to create new
	// clients but they also fail. With stubbed clients we can't go through
	// the rpcclient.NewClient path, so after eviction the cache is empty and
	// the slow path will fail to connect.
	_, err := f.GetClient()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no healthy nodes available")
}

func TestClientFactory_EvictClient(t *testing.T) {
	t.Parallel()

	stub := &stubRPC{
		latestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 1},
	}

	f := &ClientFactory{
		nodes:       testNodes("http://node1:8080"),
		chainID:     "test",
		cfg:         testCfg(),
		lggr:        logger.Nop(),
		clientCache: make(map[string]*Client),
	}

	f.clientCache["http://node1:8080"] = NewClientFromInterfaceWithConfig(stub, testCfg())

	f.EvictClient("http://node1:8080")

	f.clientCacheMu.RLock()
	_, exists := f.clientCache["http://node1:8080"]
	f.clientCacheMu.RUnlock()
	assert.False(t, exists)
}

func TestClientFactory_NodeCount(t *testing.T) {
	t.Parallel()

	f := &ClientFactory{
		nodes:       testNodes("http://a:8080", "http://b:8080", "http://c:8080"),
		chainID:     "test",
		cfg:         testCfg(),
		lggr:        logger.Nop(),
		clientCache: make(map[string]*Client),
	}

	assert.Equal(t, 3, f.NodeCount())
}
