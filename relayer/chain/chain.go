package chain

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	chainsel "github.com/smartcontractkit/chain-selectors"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"

	frameworkmetrics "github.com/smartcontractkit/chainlink-framework/metrics"
	"github.com/smartcontractkit/chainlink-framework/multinode"

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	"github.com/smartcontractkit/chainlink-common/pkg/services"
	"github.com/smartcontractkit/chainlink-common/pkg/types"
	"github.com/smartcontractkit/chainlink-common/pkg/types/core"

	"github.com/smartcontractkit/chainlink-stellar/relayer/config"
	"github.com/smartcontractkit/chainlink-stellar/relayer/txm"
)

// RPCClient is the subset of the Stellar Soroban JSON-RPC client used across
// the Stellar relayer (chain + per-component callers). It mirrors the public
// surface of *rpcclient.Client so production wiring passes the SDK type
// unchanged and tests can inject mocks. Connection lifecycle is owned by the
// multinode pool, so this interface does not expose Close.
type RPCClient interface {
	SimulateTransaction(ctx context.Context, req protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error)
	SendTransaction(ctx context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error)
	GetTransaction(ctx context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error)
	GetEvents(ctx context.Context, req protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error)
	GetLedgerEntries(ctx context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error)
	GetLedgers(ctx context.Context, req protocolrpc.GetLedgersRequest) (protocolrpc.GetLedgersResponse, error)
	GetLatestLedger(ctx context.Context) (protocolrpc.GetLatestLedgerResponse, error)
	GetFeeStats(ctx context.Context) (protocolrpc.GetFeeStatsResponse, error)
}

// Chain is the Stellar chain service interface.
type Chain interface {
	types.ChainService

	ID() string
	Config() *config.TOMLConfig
	TxManager() *txm.StellarTxm
	KeyStore() core.Keystore
	GetClient(ctx context.Context) (RPCClient, error)
}

var _ Chain = (*chain)(nil)

type chain struct {
	types.UnimplementedChainService
	services.StateMachine

	chainInfo chainsel.StellarChain
	cfg       *config.TOMLConfig
	lggr      logger.Logger
	keyStore  core.Keystore

	txm       *txm.StellarTxm
	multiNode *multinode.MultiNode[multinode.StringID, *MultiNodeClient]
}

// Opts are the external dependencies required to construct a Chain.
type Opts struct {
	Logger   logger.Logger
	KeyStore core.Keystore
}

func (o *Opts) Validate() error {
	if o.Logger == nil {
		return errors.New("logger is required")
	}
	if o.KeyStore == nil {
		return errors.New("keystore is required")
	}
	return nil
}

func NewChain(cfg *config.TOMLConfig, opts Opts, chainInfo chainsel.StellarChain) (Chain, error) {
	if !cfg.IsEnabled() {
		return nil, fmt.Errorf("cannot create new chain with ID %s: chain is disabled", cfg.ChainID)
	}
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid ChainOpts: %w", err)
	}

	lggr := logger.Named(opts.Logger, "StellarChain")

	// cfg is expected to be already resolved + validated by NewDecodedTOMLConfig.
	if err := cfg.ValidateConfig(); err != nil {
		return nil, fmt.Errorf("invalid Stellar config: %w", err)
	}

	if !cfg.MultiNode.Enabled() {
		lggr.Warnw("MultiNode.Enabled=false is ignored: the Stellar relayer always uses the multinode pool", "chainID", cfg.ChainID)
	}

	ch := &chain{
		chainInfo: chainInfo,
		cfg:       cfg,
		lggr:      lggr,
		keyStore:  opts.KeyStore,
	}

	mn, err := newMultiNode(cfg, lggr)
	if err != nil {
		return nil, fmt.Errorf("failed to create multinode pool: %w", err)
	}
	ch.multiNode = mn

	t, err := txm.New(lggr, opts.KeyStore, cfg.TxManager, func(ctx context.Context) (txm.RPCClient, error) {
		return ch.GetClient(ctx)
	}, chainInfo.ChainID)
	if err != nil {
		return nil, fmt.Errorf("failed to create txm: %w", err)
	}
	ch.txm = t

	return ch, nil
}

// newMultiNode builds the framework multinode pool from the configured RPC nodes. A single-node
// pool is valid; the pool still provides background health checking and dead-node eviction.
func newMultiNode(cfg *config.TOMLConfig, lggr logger.Logger) (*multinode.MultiNode[multinode.StringID, *MultiNodeClient], error) {
	chainID := multinode.StringID(cfg.ChainID)

	mnMetrics, err := frameworkmetrics.NewGenericMultiNodeMetrics(config.ChainFamilyName, cfg.ChainID)
	if err != nil {
		return nil, fmt.Errorf("failed to create multinode metrics: %w", err)
	}
	rpcMetrics, err := frameworkmetrics.NewRPCClientMetrics(frameworkmetrics.RPCClientMetricsConfig{
		ChainFamily: config.ChainFamilyName,
		ChainID:     cfg.ChainID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create rpc client metrics: %w", err)
	}

	primaries := make([]multinode.Node[multinode.StringID, *MultiNodeClient], 0, len(cfg.Nodes))
	for i, node := range cfg.Nodes {
		rpc := NewMultiNodeClient(node.URL.String(), &cfg.MultiNode, cfg.RequestTimeout.Duration(), lggr, rpcMetrics)
		var order int32
		if node.Order != nil {
			order = *node.Order
		}
		primaries = append(primaries, multinode.NewNode[multinode.StringID, *Head, *MultiNodeClient](
			&cfg.MultiNode, &cfg.MultiNode, lggr, mnMetrics,
			node.URL.URL(), nil, // single HTTP endpoint; no separate ws/http split
			*node.Name, i, chainID, order, rpc, config.ChainFamilyName,
			false, // isLoadBalancedRPC
		))
	}

	return multinode.NewMultiNode[multinode.StringID, *MultiNodeClient](
		lggr, mnMetrics, cfg.MultiNode.SelectionMode(), cfg.MultiNode.LeaseDuration(),
		primaries, nil, // no send-only nodes
		chainID, config.ChainFamilyName, cfg.MultiNode.DeathDeclarationDelay(),
	), nil
}

func (c *chain) Name() string               { return c.lggr.Name() }
func (c *chain) ID() string                 { return c.chainInfo.ChainID }
func (c *chain) Config() *config.TOMLConfig { return c.cfg }
func (c *chain) TxManager() *txm.StellarTxm { return c.txm }
func (c *chain) KeyStore() core.Keystore    { return c.keyStore }

func (c *chain) Start(ctx context.Context) error {
	return c.StartOnce("StellarChain", func() error {
		c.lggr.Debugw("Starting")
		var ms services.MultiStart
		return ms.Start(ctx, c.multiNode, c.txm)
	})
}

func (c *chain) Close() error {
	return c.StopOnce("StellarChain", func() error {
		c.lggr.Debugw("Stopping")
		var errs error
		if err := c.txm.Close(); err != nil {
			c.lggr.Warnw("Error closing txm", "err", err)
			errs = errors.Join(errs, fmt.Errorf("close txm: %w", err))
		}
		if err := c.multiNode.Close(); err != nil {
			c.lggr.Warnw("Error closing multinode", "err", err)
			errs = errors.Join(errs, fmt.Errorf("close multinode: %w", err))
		}
		return errs
	})
}

func (c *chain) Ready() error {
	return errors.Join(c.StateMachine.Ready(), c.multiNode.Ready(), c.txm.Ready())
}

func (c *chain) HealthReport() map[string]error {
	report := map[string]error{c.Name(): c.StateMachine.Healthy()}
	services.CopyHealth(report, c.multiNode.HealthReport())
	services.CopyHealth(report, c.txm.HealthReport())
	return report
}

// GetClient returns a healthy RPC node selected by the multinode pool. It returns
// multinode.ErrNodeError when no live node is available.
func (c *chain) GetClient(ctx context.Context) (RPCClient, error) {
	return c.multiNode.SelectRPC(ctx)
}

func (c *chain) GetChainInfo(_ context.Context) (types.ChainInfo, error) {
	return types.ChainInfo{
		FamilyName:      config.ChainFamilyName,
		ChainID:         c.chainInfo.ChainID,
		NetworkName:     string(c.chainInfo.NetworkType),
		NetworkNameFull: c.chainInfo.Name,
	}, nil
}

func (c *chain) GetChainStatus(_ context.Context) (types.ChainStatus, error) {
	tomlStr, err := c.cfg.TOMLString()
	if err != nil {
		return types.ChainStatus{}, err
	}

	return types.ChainStatus{
		ID:      c.chainInfo.ChainID,
		Enabled: c.cfg.IsEnabled(),
		Config:  tomlStr,
	}, nil
}

func (c *chain) LatestHead(ctx context.Context) (types.Head, error) {
	client, err := c.GetClient(ctx)
	if err != nil {
		return types.Head{}, err
	}

	ledger, err := client.GetLatestLedger(ctx)
	if err != nil {
		return types.Head{}, err
	}

	hash, err := hex.DecodeString(ledger.Hash)
	if err != nil {
		return types.Head{}, fmt.Errorf("failed to decode ledger hash %q: %w", ledger.Hash, err)
	}

	return types.Head{
		Height:    strconv.Itoa(int(ledger.Sequence)),
		Hash:      hash,
		Timestamp: uint64(ledger.LedgerCloseTime),
	}, nil
}

func (c *chain) FinalizedHead(ctx context.Context) (types.Head, error) {
	return c.LatestHead(ctx)
}
