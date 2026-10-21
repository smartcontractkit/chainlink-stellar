package chain

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/network"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	frameworkmetrics "github.com/smartcontractkit/chainlink-framework/metrics"
	"github.com/smartcontractkit/chainlink-framework/multinode"
)

// Head is the multinode head for Stellar. Stellar ledgers are final at close (SCP gives
// deterministic finality, no reorgs), so there is a single head notion: the latest closed
// ledger is also the finalized ledger. Difficulty/total-difficulty are PoW concepts and are
// always nil.
type Head struct {
	Sequence uint32
}

func (h *Head) BlockNumber() int64 {
	if !h.IsValid() {
		return 0
	}
	return int64(h.Sequence)
}

func (h *Head) BlockDifficulty() *big.Int    { return nil }
func (h *Head) GetTotalDifficulty() *big.Int { return nil }

func (h *Head) IsValid() bool { return h != nil && h.Sequence > 0 }

// SorobanClient is the subset of the Stellar SDK rpcclient.Client used by MultiNodeClient: the
// domain RPCClient surface plus the health/network/version probes the multinode node lifecycle
// needs, plus Close. Holding it as an interface keeps the adapter unit-testable;
// *rpcclient.Client satisfies it.
type SorobanClient interface {
	RPCClient
	GetHealth(ctx context.Context) (protocolrpc.GetHealthResponse, error)
	GetNetwork(ctx context.Context) (protocolrpc.GetNetworkResponse, error)
	GetVersionInfo(ctx context.Context) (protocolrpc.GetVersionInfoResponse, error)
	Close() error
}

// MultiNodeClient embeds *multinode.RPCClientBase, which supplies the head/finalized-head
// subscriptions (via polling), subscription bookkeeping.
type MultiNodeClient struct {
	*multinode.RPCClientBase[*Head]

	SorobanClient
	lggr logger.Logger
}

var (
	_ multinode.RPCClient[multinode.StringID, *Head] = (*MultiNodeClient)(nil)
	_ RPCClient                                      = (*MultiNodeClient)(nil)
)

// NewMultiNodeClient builds an adapter around the Soroban RPC at url. cfg supplies the
// head/finalized poll intervals consumed by RPCClientBase.
func NewMultiNodeClient(
	url string,
	cfg multinode.RPCClientBaseConfig,
	requestTimeout time.Duration,
	lggr logger.Logger,
	rpcMetrics frameworkmetrics.RPCClientMetrics,
) *MultiNodeClient {
	c := &MultiNodeClient{
		SorobanClient: rpcclient.NewClient(url, newLimitedHTTPClient(requestTimeout)),
		lggr:          logger.Named(lggr, "MultiNodeClient"),
	}
	c.RPCClientBase = multinode.NewRPCClientBase[*Head](
		cfg, requestTimeout, lggr,
		c.latestBlock,
		c.latestFinalizedBlock,
		url,
		false, // not send-only
		rpcMetrics,
	)
	return c
}

// healthStatusHealthy is the status string the Soroban RPC getHealth method reports for a node that is up and synced.
const healthStatusHealthy = "healthy"

// Dial validates reachability of the endpoint. The SDK client is HTTP and does not hold a
// persistent connection, so a successful health probe stands in for a dial handshake. A node
// that responds but reports a non-healthy status is rejected so it is not added to the pool.
func (c *MultiNodeClient) Dial(ctx context.Context) error {
	resp, err := c.GetHealth(ctx)
	if err != nil {
		return fmt.Errorf("stellar rpc dial/health check failed: %w", err)
	}
	if resp.Status != healthStatusHealthy {
		return fmt.Errorf("stellar rpc dial/health check returned non-healthy status %q", resp.Status)
	}
	return nil
}

// ChainID returns the hex-encoded Stellar network ID (SHA-256 of the network passphrase),
// which matches the chain-selectors ChainID form used in config, so node chain-ID
// verification can be enabled.
func (c *MultiNodeClient) ChainID(ctx context.Context) (multinode.StringID, error) {
	net, err := c.GetNetwork(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get network: %w", err)
	}
	id := network.ID(net.Passphrase)
	return multinode.StringID(hex.EncodeToString(id[:])), nil
}

// ClientVersion doubles as the periodic liveness probe; it returns an error when the RPC is
// unreachable, which the node lifecycle treats as a health failure.
func (c *MultiNodeClient) ClientVersion(ctx context.Context) (string, error) {
	info, err := c.GetVersionInfo(ctx)
	if err != nil {
		return "", err
	}
	return info.Version, nil
}

// IsSyncing is always false: Stellar RPC does not expose a backfill/sync state that blocks
// reads, and lagging nodes are caught by head-based out-of-sync detection instead.
func (c *MultiNodeClient) IsSyncing(_ context.Context) (bool, error) { return false, nil }

// latestBlock / latestFinalizedBlock back the RPCClientBase head subscriptions. They are
// identical because the latest closed ledger is already final on Stellar.
func (c *MultiNodeClient) latestBlock(ctx context.Context) (*Head, error) {
	ledger, err := c.GetLatestLedger(ctx)
	if err != nil {
		return nil, err
	}
	return &Head{Sequence: ledger.Sequence}, nil
}

func (c *MultiNodeClient) latestFinalizedBlock(ctx context.Context) (*Head, error) {
	return c.latestBlock(ctx)
}

// Close tears down the framework subscriptions and the underlying SDK client. It is defined
// explicitly to resolve the ambiguity between RPCClientBase.Close() and SorobanClient.Close().
func (c *MultiNodeClient) Close() {
	c.RPCClientBase.Close()
	if err := c.SorobanClient.Close(); err != nil {
		c.lggr.Warnw("error closing Soroban RPC client", "err", err)
	}
}
