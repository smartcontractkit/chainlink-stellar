package ccvclient

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"golang.org/x/time/rate"
)

var promStellarRPCLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name: "stellar_rpc_call_latency",
	Help: "Latency of Stellar Soroban RPC calls in milliseconds",
	Buckets: []float64{
		float64(50 * time.Millisecond),
		float64(100 * time.Millisecond),
		float64(200 * time.Millisecond),
		float64(500 * time.Millisecond),
		float64(1 * time.Second),
		float64(2 * time.Second),
		float64(4 * time.Second),
		float64(8 * time.Second),
	},
}, []string{"chainID", "nodeURL", "success", "rpcCallName"})

// RPCClient captures the subset of *rpcclient.Client methods used across all
// Stellar components (TXM, Deployer, SourceReader, DestinationReader). It
// exists solely for test mocking — production code passes the real
// *rpcclient.Client from the Stellar SDK, which already satisfies this
// interface with no additional implementation.
type RPCClient interface {
	SimulateTransaction(ctx context.Context, req protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error)
	SendTransaction(ctx context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error)
	GetTransaction(ctx context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error)
	GetLedgerEntries(ctx context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error)
	GetEvents(ctx context.Context, req protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error)
	GetLatestLedger(ctx context.Context) (protocolrpc.GetLatestLedgerResponse, error)
	GetLedgers(ctx context.Context, req protocolrpc.GetLedgersRequest) (protocolrpc.GetLedgersResponse, error)
	GetFeeStats(ctx context.Context) (protocolrpc.GetFeeStatsResponse, error)
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

	// ChainID is used as a Prometheus label for per-method latency tracking.
	// Leave empty to disable latency metrics.
	ChainID string

	// NodeURL is used as a Prometheus label to identify which RPC node is
	// being called. Set by the ClientFactory for each cached client.
	NodeURL string
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

// Client is an optimization layer over the native *rpcclient.Client. It adds
// a latest-ledger TTL cache, a token-bucket rate limiter, and polling
// utilities. All Stellar components (TXM, SourceReader, DestinationReader,
// Deployer) should share a single Client instance to benefit from coalesced
// caching and unified rate limiting.
//
// Callers may access the underlying RPC client directly via the exported RPC
// field (use WaitRateLimit first if rate limiting is desired), or call the
// forwarded convenience methods which apply rate limiting transparently.
type Client struct {
	RPC     RPCClient
	cfg     ClientConfig
	limiter *rate.Limiter

	// Metrics labels for per-method Prometheus latency tracking.
	chainID string
	nodeURL string

	ledgerMu     sync.Mutex
	cachedLedger *protocolrpc.GetLatestLedgerResponse
	cachedAt     time.Time
}

func (c *Client) recordLatency(rpcCallName string, d time.Duration, err error) {
	if c.chainID == "" && c.nodeURL == "" {
		return
	}
	promStellarRPCLatency.WithLabelValues(
		c.chainID,
		c.nodeURL,
		strconv.FormatBool(err == nil),
		rpcCallName,
	).Observe(float64(d.Milliseconds()))
}

// NewClient creates a Client wrapping a concrete *rpcclient.Client with
// default configuration.
func NewClient(rpcClient *rpcclient.Client) *Client {
	return NewClientWithConfig(rpcClient, DefaultClientConfig())
}

// NewClientFromInterface creates a Client from any RPCClient implementation
// with default configuration. Useful for injecting mocks in tests.
func NewClientFromInterface(rpc RPCClient) *Client {
	return newClient(rpc, DefaultClientConfig())
}

// NewClientWithConfig creates a Client wrapping a concrete *rpcclient.Client
// with the provided configuration.
func NewClientWithConfig(rpcClient *rpcclient.Client, cfg ClientConfig) *Client {
	return newClient(rpcClient, cfg)
}

// NewClientFromInterfaceWithConfig creates a Client from any RPCClient
// implementation with the provided configuration.
func NewClientFromInterfaceWithConfig(rpc RPCClient, cfg ClientConfig) *Client {
	return newClient(rpc, cfg)
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
		chainID: cfg.ChainID,
		nodeURL: cfg.NodeURL,
	}
}

// WaitRateLimit blocks until the rate limiter allows one event, or the
// context is cancelled. Returns nil immediately if rate limiting is disabled.
// Use this before calling c.RPC methods directly to respect the shared rate
// limit. The forwarded convenience methods (SimulateTransaction, etc.) call
// this automatically.
func (c *Client) WaitRateLimit(ctx context.Context) error {
	if c.limiter == nil {
		return nil
	}
	return c.limiter.Wait(ctx)
}

// LatestLedger returns the latest ledger response, using a short-TTL cache
// to coalesce redundant calls from broadcaster, confirmer, pollers, etc.
// Falls through to a direct RPC call if the cache is stale or disabled.
func (c *Client) LatestLedger(ctx context.Context) (protocolrpc.GetLatestLedgerResponse, error) {
	if c.cfg.LedgerCacheTTL <= 0 {
		if err := c.WaitRateLimit(ctx); err != nil {
			return protocolrpc.GetLatestLedgerResponse{}, fmt.Errorf("rate limiter: %w", err)
		}
		return c.RPC.GetLatestLedger(ctx)
	}

	c.ledgerMu.Lock()
	defer c.ledgerMu.Unlock()

	if c.cachedLedger != nil && time.Since(c.cachedAt) < c.cfg.LedgerCacheTTL {
		return *c.cachedLedger, nil
	}

	if err := c.WaitRateLimit(ctx); err != nil {
		return protocolrpc.GetLatestLedgerResponse{}, fmt.Errorf("rate limiter: %w", err)
	}

	resp, err := c.RPC.GetLatestLedger(ctx)
	if err != nil {
		return resp, err
	}

	c.cachedLedger = &resp
	c.cachedAt = time.Now()
	return resp, nil
}

// --- Forwarded RPCClient methods with transparent rate limiting ---
//
// These convenience methods forward to c.RPC with automatic rate limiting.
// Callers who prefer manual control can use c.WaitRateLimit + c.RPC directly.

func (c *Client) SimulateTransaction(ctx context.Context, req protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
	if err := c.WaitRateLimit(ctx); err != nil {
		return protocolrpc.SimulateTransactionResponse{}, fmt.Errorf("rate limiter: %w", err)
	}
	start := time.Now()
	resp, err := c.RPC.SimulateTransaction(ctx, req)
	c.recordLatency("SimulateTransaction", time.Since(start), err)
	return resp, err
}

func (c *Client) SendTransaction(ctx context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
	if err := c.WaitRateLimit(ctx); err != nil {
		return protocolrpc.SendTransactionResponse{}, fmt.Errorf("rate limiter: %w", err)
	}
	start := time.Now()
	resp, err := c.RPC.SendTransaction(ctx, req)
	c.recordLatency("SendTransaction", time.Since(start), err)
	return resp, err
}

func (c *Client) GetTransaction(ctx context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
	if err := c.WaitRateLimit(ctx); err != nil {
		return protocolrpc.GetTransactionResponse{}, fmt.Errorf("rate limiter: %w", err)
	}
	start := time.Now()
	resp, err := c.RPC.GetTransaction(ctx, req)
	c.recordLatency("GetTransaction", time.Since(start), err)
	return resp, err
}

func (c *Client) GetLedgerEntries(ctx context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
	if err := c.WaitRateLimit(ctx); err != nil {
		return protocolrpc.GetLedgerEntriesResponse{}, fmt.Errorf("rate limiter: %w", err)
	}
	start := time.Now()
	resp, err := c.RPC.GetLedgerEntries(ctx, req)
	c.recordLatency("GetLedgerEntries", time.Since(start), err)
	return resp, err
}

func (c *Client) GetEvents(ctx context.Context, req protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error) {
	if err := c.WaitRateLimit(ctx); err != nil {
		return protocolrpc.GetEventsResponse{}, fmt.Errorf("rate limiter: %w", err)
	}
	start := time.Now()
	resp, err := c.RPC.GetEvents(ctx, req)
	c.recordLatency("GetEvents", time.Since(start), err)
	return resp, err
}

func (c *Client) GetLatestLedger(ctx context.Context) (protocolrpc.GetLatestLedgerResponse, error) {
	if err := c.WaitRateLimit(ctx); err != nil {
		return protocolrpc.GetLatestLedgerResponse{}, fmt.Errorf("rate limiter: %w", err)
	}
	start := time.Now()
	resp, err := c.RPC.GetLatestLedger(ctx)
	c.recordLatency("GetLatestLedger", time.Since(start), err)
	return resp, err
}

func (c *Client) GetLedgers(ctx context.Context, req protocolrpc.GetLedgersRequest) (protocolrpc.GetLedgersResponse, error) {
	if err := c.WaitRateLimit(ctx); err != nil {
		return protocolrpc.GetLedgersResponse{}, fmt.Errorf("rate limiter: %w", err)
	}
	start := time.Now()
	resp, err := c.RPC.GetLedgers(ctx, req)
	c.recordLatency("GetLedgers", time.Since(start), err)
	return resp, err
}

func (c *Client) GetFeeStats(ctx context.Context) (protocolrpc.GetFeeStatsResponse, error) {
	if err := c.WaitRateLimit(ctx); err != nil {
		return protocolrpc.GetFeeStatsResponse{}, fmt.Errorf("rate limiter: %w", err)
	}
	start := time.Now()
	resp, err := c.RPC.GetFeeStats(ctx)
	c.recordLatency("GetFeeStats", time.Since(start), err)
	return resp, err
}

// PollTransaction polls GetTransaction until the transaction reaches a
// terminal status (SUCCESS or FAILED) or the timeout elapses. Transient RPC
// errors are swallowed and retried on the next tick — the timeout is the
// backstop, matching the Aptos confirm-loop pattern.
func (c *Client) PollTransaction(ctx context.Context, hash string, timeout time.Duration) (protocolrpc.GetTransactionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()

	var zero protocolrpc.GetTransactionResponse

	for {
		select {
		case <-ctx.Done():
			return zero, fmt.Errorf("poll timed out for tx %s: %w", hash, ctx.Err())
		case <-ticker.C:
			resp, err := c.GetTransaction(ctx, protocolrpc.GetTransactionRequest{Hash: hash})
			if err != nil {
				continue
			}
			switch resp.Status {
			case protocolrpc.TransactionStatusSuccess, protocolrpc.TransactionStatusFailed:
				return resp, nil
			}
		}
	}
}
