package client

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
)

// NodeConfig holds per-node RPC endpoint settings used by ClientFactory.
type NodeConfig struct {
	Name    string
	URL     string
	Timeout time.Duration
}

// ClientFactory manages a pool of Stellar RPC nodes with random selection and
// per-URL client caching. It mirrors the Aptos chain.GetClient() pattern:
// callers receive a healthy *Client on each call, with transparent failover
// across configured nodes.
//
// The TXM stores a reference to GetClient as its getClient func() callback.
type ClientFactory struct {
	nodes   []NodeConfig
	chainID string
	cfg     ClientConfig
	lggr    logger.Logger

	clientCacheMu sync.RWMutex
	clientCache   map[string]*Client
}

// NewClientFactory creates a factory that rotates across the given nodes.
// The chainID is used for health-check validation and Prometheus labels.
func NewClientFactory(nodes []NodeConfig, chainID string, cfg ClientConfig, lggr logger.Logger) (*ClientFactory, error) {
	if len(nodes) == 0 {
		return nil, errors.New("at least one node is required")
	}
	return &ClientFactory{
		nodes:       nodes,
		chainID:     chainID,
		cfg:         cfg,
		lggr:        logger.Named(lggr, "ClientFactory"),
		clientCache: make(map[string]*Client),
	}, nil
}

// GetClient returns a healthy *Client, randomly selecting from configured
// nodes. Clients are cached per URL. A cached client is validated with a
// GetLatestLedger health check; if the check fails, the cache entry is evicted
// and the next node is tried. If no healthy node is found, a new client is
// created, cached, and returned.
//
// This method is safe for concurrent use and is intended to be stored as the
// TXM's getClient func() (*Client, error) callback.
func (f *ClientFactory) GetClient() (*Client, error) {
	indices := rand.Perm(len(f.nodes))

	// Fast path: try cached clients (read lock only).
	for _, i := range indices {
		node := f.nodes[i]
		f.clientCacheMu.RLock()
		cached := f.clientCache[node.URL]
		f.clientCacheMu.RUnlock()

		if cached == nil {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), node.Timeout)
		_, err := cached.RPC.GetLatestLedger(ctx)
		cancel()
		if err != nil {
			f.lggr.Warnw("Cached client health check failed, evicting",
				"node", node.Name, "url", node.URL, "err", err)
			f.clientCacheMu.Lock()
			delete(f.clientCache, node.URL)
			f.clientCacheMu.Unlock()
			continue
		}
		return cached, nil
	}

	// Slow path: create and cache. Hold write lock for entire creation to
	// prevent multiple goroutines from racing to create the same client.
	f.clientCacheMu.Lock()
	defer f.clientCacheMu.Unlock()

	for _, i := range indices {
		node := f.nodes[i]

		// Double-check: another goroutine may have created it while we waited.
		if c := f.clientCache[node.URL]; c != nil {
			return c, nil
		}

		httpClient := &http.Client{Timeout: node.Timeout}
		rpc := rpcclient.NewClient(node.URL, httpClient)

		ctx, cancel := context.WithTimeout(context.Background(), node.Timeout)
		_, err := rpc.GetLatestLedger(ctx)
		cancel()
		if err != nil {
			f.lggr.Errorw("Failed to connect to node",
				"node", node.Name, "url", node.URL, "err", err)
			continue
		}

		clientCfg := f.cfg
		clientCfg.ChainID = f.chainID
		clientCfg.RPCURL = node.URL

		client := NewClient(rpc, &clientCfg)
		f.clientCache[node.URL] = client
		f.lggr.Debugw("Created and cached client",
			"node", node.Name, "url", node.URL)
		return client, nil
	}

	return nil, fmt.Errorf("no healthy nodes available (tried %d)", len(f.nodes))
}

// EvictClient removes a cached client for the given URL, forcing the next
// GetClient call to create a fresh connection. Useful when a caller detects
// persistent errors on a specific node.
func (f *ClientFactory) EvictClient(nodeURL string) {
	f.clientCacheMu.Lock()
	defer f.clientCacheMu.Unlock()
	delete(f.clientCache, nodeURL)
}

// NodeCount returns the number of configured nodes.
func (f *ClientFactory) NodeCount() int {
	return len(f.nodes)
}
