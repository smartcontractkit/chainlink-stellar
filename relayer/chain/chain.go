package chain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"go.uber.org/multierr"

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
// unchanged and tests can inject mocks.
type RPCClient interface {
	SimulateTransaction(ctx context.Context, req protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error)
	SendTransaction(ctx context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error)
	GetTransaction(ctx context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error)
	GetEvents(ctx context.Context, req protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error)
	GetLedgerEntries(ctx context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error)
	GetLedgers(ctx context.Context, req protocolrpc.GetLedgersRequest) (protocolrpc.GetLedgersResponse, error)
	GetLatestLedger(ctx context.Context) (protocolrpc.GetLatestLedgerResponse, error)
	GetFeeStats(ctx context.Context) (protocolrpc.GetFeeStatsResponse, error)
	Close() error
}

// Chain is the Stellar chain service interface.
type Chain interface {
	types.ChainService

	ID() string
	Config() *config.TOMLConfig
	TxManager() txm.TxManager
	KeyStore() core.Keystore
	GetClient() (RPCClient, error)
}

var _ Chain = (*chain)(nil)

type chain struct {
	types.UnimplementedChainService
	services.StateMachine

	chainInfo chainsel.StellarChain
	cfg       *config.TOMLConfig
	lggr      logger.Logger
	keyStore  core.Keystore

	txm *txm.StellarTxm

	// once ensures the RPC client is created exactly once.
	once   sync.Once
	client RPCClient
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

	ch := &chain{
		chainInfo: chainInfo,
		cfg:       cfg,
		lggr:      lggr,
		keyStore:  opts.KeyStore,
	}

	t, err := txm.New(lggr, opts.KeyStore, cfg.TxManager, func() (txm.RPCClient, error) {
		return ch.GetClient()
	}, chainInfo.ChainID)
	if err != nil {
		return nil, fmt.Errorf("failed to create txm: %w", err)
	}
	ch.txm = t

	return ch, nil
}

func (c *chain) Name() string               { return c.lggr.Name() }
func (c *chain) ID() string                 { return c.chainInfo.ChainID }
func (c *chain) Config() *config.TOMLConfig { return c.cfg }
func (c *chain) TxManager() txm.TxManager { return c.txm }
func (c *chain) KeyStore() core.Keystore    { return c.keyStore }

func (c *chain) Start(ctx context.Context) error {
	return c.StartOnce("StellarChain", func() error {
		c.lggr.Debugw("Starting")
		return c.txm.Start(ctx)
	})
}

func (c *chain) Close() error {
	return c.StopOnce("StellarChain", func() error {
		c.lggr.Debugw("Stopping")
		var errs error
		if c.client != nil {
			if err := c.client.Close(); err != nil {
				c.lggr.Warnw("Error closing RPC client", "err", err)
				errs = multierr.Append(errs, fmt.Errorf("close rpc client: %w", err))
			}
		}
		if err := c.txm.Close(); err != nil {
			c.lggr.Warnw("Error closing txm", "err", err)
			errs = multierr.Append(errs, fmt.Errorf("close txm: %w", err))
		}
		return errs
	})
}

func (c *chain) Ready() error {
	return errors.Join(c.StateMachine.Ready(), c.txm.Ready())
}

func (c *chain) HealthReport() map[string]error {
	report := map[string]error{c.Name(): c.StateMachine.Healthy()}
	services.CopyHealth(report, c.txm.HealthReport())
	return report
}

func (c *chain) GetClient() (RPCClient, error) {
	if len(c.cfg.Nodes) == 0 || c.cfg.Nodes[0].URL == nil {
		return nil, errors.New("no nodes configured")
	}

	// TODO: add multi-node rotation and health-check eviction.
	node := c.cfg.Nodes[0]
	c.once.Do(func() {
		c.client = rpcclient.NewClient(node.URL.String(), &http.Client{Timeout: 30 * time.Second})
		c.lggr.Infow("Created Soroban RPC client", "url", node.URL.String())
	})
	return c.client, nil
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
	client, err := c.GetClient()
	if err != nil {
		return types.Head{}, err
	}

	ledger, err := client.GetLatestLedger(ctx)
	if err != nil {
		return types.Head{}, err
	}

	return types.Head{
		Height:    strconv.Itoa(int(ledger.Sequence)),
		Hash:      []byte(ledger.Hash),
		Timestamp: uint64(ledger.LedgerCloseTime),
	}, nil
}

func (c *chain) FinalizedHead(ctx context.Context) (types.Head, error) {
	return c.LatestHead(ctx)
}
