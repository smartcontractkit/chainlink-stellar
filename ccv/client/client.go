package ccvclient

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

// RPCClient is the unified Stellar RPC interface used by TXM, SourceReader,
// DestinationReader, Deployer, and other components. It is the superset of
// all Soroban RPC methods used across the chainlink-stellar repository.
// *rpcclient.Client satisfies this interface.
type RPCClient interface {
	SimulateTransaction(ctx context.Context, req protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error)
	SendTransaction(ctx context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error)
	GetTransaction(ctx context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error)
	GetLedgerEntries(ctx context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error)
	GetEvents(ctx context.Context, req protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error)
	GetLatestLedger(ctx context.Context) (protocolrpc.GetLatestLedgerResponse, error)
	GetLedgers(ctx context.Context, req protocolrpc.GetLedgersRequest) (protocolrpc.GetLedgersResponse, error)
}

var _ RPCClient = (*rpcclient.Client)(nil)

// ClientConfig configures the shared infrastructure on Client.
type ClientConfig struct {
	// LedgerCacheTTL controls how long a GetLatestLedger response is cached.
	// Callers using LatestLedger() benefit from coalesced calls within this
	// window. Zero disables caching.
	LedgerCacheTTL time.Duration

	// RateLimitPerSec is the sustained RPC request rate (requests/second).
	// Zero disables rate limiting.
	RateLimitPerSec float64

	// RateLimitBurst is the maximum burst of RPC requests allowed.
	RateLimitBurst int

	// PollInterval is the default tick interval for PollTransaction.
	PollInterval time.Duration
}

// DefaultClientConfig returns a ClientConfig with sensible defaults.
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		LedgerCacheTTL:  3 * time.Second,
		RateLimitPerSec: 10,
		RateLimitBurst:  20,
		PollInterval:    1 * time.Second,
	}
}

// Client wraps an RPCClient with shared infrastructure: a latest-ledger
// cache, a token-bucket rate limiter, and polling utilities. All components
// (TXM, SourceReader, DestinationReader, Deployer) should share a single
// Client instance to benefit from coalesced caching and unified rate limiting.
//
// RPC is exported intentionally so that components needing low-level access
// (e.g. SendTransaction, SimulateTransaction) can call methods directly.
// Only LatestLedger and PollTransaction apply caching and rate-limiting;
// direct RPC calls bypass those layers. This is by design: high-frequency
// read calls (latest ledger) benefit from caching, while write-path calls
// (send/simulate) should not be blocked by the token bucket.
type Client struct {
	RPC RPCClient
	cfg ClientConfig

	limiter *rate.Limiter

	ledgerMu     sync.RWMutex
	cachedLedger *protocolrpc.GetLatestLedgerResponse
	cachedAt     time.Time
	ledgerGroup  singleflight.Group
}

// NewClient creates a Client wrapping a concrete *rpcclient.Client with
// default configuration.
func NewClient(rpcClient *rpcclient.Client) *Client {
	return NewClientWithConfig(rpcClient, DefaultClientConfig())
}

// NewClientFromInterface creates a Client from any RPCClient implementation
// with default configuration.
func NewClientFromInterface(rpc RPCClient) *Client {
	return newClient(rpc, DefaultClientConfig())
}

// NewClientWithConfig creates a Client wrapping a concrete *rpcclient.Client
// with the provided configuration.
func NewClientWithConfig(rpcClient *rpcclient.Client, cfg ClientConfig) *Client {
	return newClient(rpcClient, cfg)
}

func newClient(rpc RPCClient, cfg ClientConfig) *Client {
	var limiter *rate.Limiter
	if cfg.RateLimitPerSec > 0 {
		limiter = rate.NewLimiter(rate.Limit(cfg.RateLimitPerSec), cfg.RateLimitBurst)
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 1 * time.Second
	}
	return &Client{
		RPC:     rpc,
		cfg:     cfg,
		limiter: limiter,
	}
}

// WaitRateLimit blocks until the rate limiter allows one event, or the
// context is cancelled. Returns nil immediately if rate limiting is disabled.
func (c *Client) WaitRateLimit(ctx context.Context) error {
	if c.limiter == nil {
		return nil
	}
	return c.limiter.Wait(ctx)
}

// LatestLedger returns the latest ledger response, using a short-TTL cache
// to coalesce redundant calls from broadcaster, confirmer, pollers, etc.
// Falls through to a direct RPC call if the cache is stale or disabled.
//
// Concurrent callers share a single in-flight RPC fetch via singleflight
// so that a TTL expiry does not cause a thundering herd.
func (c *Client) LatestLedger(ctx context.Context) (protocolrpc.GetLatestLedgerResponse, error) {
	if c.cfg.LedgerCacheTTL <= 0 {
		return c.RPC.GetLatestLedger(ctx)
	}

	c.ledgerMu.RLock()
	if c.cachedLedger != nil && time.Since(c.cachedAt) < c.cfg.LedgerCacheTTL {
		resp := *c.cachedLedger
		c.ledgerMu.RUnlock()
		return resp, nil
	}
	c.ledgerMu.RUnlock()

	v, err, _ := c.ledgerGroup.Do("latest", func() (any, error) {
		return c.RPC.GetLatestLedger(ctx)
	})
	if err != nil {
		return protocolrpc.GetLatestLedgerResponse{}, err
	}

	resp := v.(protocolrpc.GetLatestLedgerResponse)
	c.ledgerMu.Lock()
	c.cachedLedger = &resp
	c.cachedAt = time.Now()
	c.ledgerMu.Unlock()

	return resp, nil
}

// PollTransaction polls GetTransaction until the transaction reaches a
// terminal status (success/failed) or the timeout elapses. This replaces
// the duplicated poll loops in the TXM confirmer, broadcaster, and test
// helpers. Respects the rate limiter if configured.
func (c *Client) PollTransaction(ctx context.Context, hash string, timeout time.Duration) (protocolrpc.GetTransactionResponse, error) {
	ticker := time.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()

	deadline := time.After(timeout)
	var zero protocolrpc.GetTransactionResponse

	for {
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-deadline:
			return zero, fmt.Errorf("confirmation timed out for %s", hash)
		case <-ticker.C:
			if err := c.WaitRateLimit(ctx); err != nil {
				return zero, err
			}
			resp, err := c.RPC.GetTransaction(ctx, protocolrpc.GetTransactionRequest{Hash: hash})
			if err != nil {
				continue
			}
			switch resp.Status {
			case protocolrpc.TransactionStatusSuccess, protocolrpc.TransactionStatusFailed:
				return resp, nil
			case protocolrpc.TransactionStatusNotFound:
				continue
			}
		}
	}
}
