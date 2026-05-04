package stellar

import (
	"context"
	"strconv"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	"github.com/smartcontractkit/chainlink-common/pkg/services"
	relaytypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	stellartypes "github.com/smartcontractkit/chainlink-common/pkg/types/chains/stellar"
)

var _ relaytypes.Relayer = (*Relayer)(nil)
var _ relaytypes.StellarService = (*Relayer)(nil)

// Relayer wraps the Stellar Chain and exposes it as both types.Relayer and
// types.StellarService. All other Relayer methods return codes.Unimplemented
// via the embedded UnimplementedRelayer.
type Relayer struct {
	relaytypes.UnimplementedRelayer
	services.StateMachine

	lggr  logger.Logger
	chain Chain
}

// NewRelayer Note: constructed in core
func NewRelayer(lggr logger.Logger, rpc *rpcclient.Client) *Relayer {
	return &Relayer{
		lggr:  logger.Named(lggr, "StellarRelayer"),
		chain: NewChain(rpc),
	}
}

// Name satisfies services.Service.
func (r *Relayer) Name() string { return r.lggr.Name() }

// Start satisfies services.Service.
func (r *Relayer) Start(ctx context.Context) error {
	return r.StartOnce("StellarRelayer", func() error {
		return r.chain.Start(ctx)
	})
}

// Close satisfies services.Service.
func (r *Relayer) Close() error {
	return r.StopOnce("StellarRelayer", func() error {
		return r.chain.Close()
	})
}

// Ready satisfies services.Service.
func (r *Relayer) Ready() error { return r.Healthy() }

// Healthy satisfies services.Service.
func (r *Relayer) Healthy() error { return nil }

// HealthReport satisfies services.Service.
func (r *Relayer) HealthReport() map[string]error {
	return map[string]error{r.Name(): r.Healthy()}
}

// GetLedgerEntries satisfies types.StellarService.
func (r *Relayer) GetLedgerEntries(ctx context.Context, req stellartypes.GetLedgerEntriesRequest) (stellartypes.GetLedgerEntriesResponse, error) {
	return r.chain.GetLedgerEntries(ctx, req)
}

// GetLatestLedger satisfies types.StellarService.
func (r *Relayer) GetLatestLedger(ctx context.Context) (stellartypes.GetLatestLedgerResponse, error) {
	return r.chain.GetLatestLedger(ctx)
}

// Stellar returns the Relayer itself as a types.StellarService.
// This satisfies the types.Relayer interface and is the hook the LOOP bridge
// calls to obtain a chain-specific service handle.
func (r *Relayer) Stellar() (relaytypes.StellarService, error) {
	return r, nil
}

// GetChainInfo returns a minimal ChainInfo for Stellar.
func (r *Relayer) GetChainInfo(_ context.Context) (relaytypes.ChainInfo, error) {
	return relaytypes.ChainInfo{
		FamilyName: "stellar",
	}, nil
}

// GetChainStatus returns the current chain status by querying the latest ledger.
func (r *Relayer) GetChainStatus(ctx context.Context) (relaytypes.ChainStatus, error) {
	_, err := r.chain.GetLatestLedger(ctx)
	if err != nil {
		return relaytypes.ChainStatus{Enabled: false}, err
	}
	return relaytypes.ChainStatus{Enabled: true}, nil
}

// LatestHead returns the latest Stellar ledger as a Head.
func (r *Relayer) LatestHead(ctx context.Context) (relaytypes.Head, error) {
	latest, err := r.chain.GetLatestLedger(ctx)
	if err != nil {
		return relaytypes.Head{}, err
	}
	return relaytypes.Head{
		Height:    strconv.FormatUint(uint64(latest.Sequence), 10),
		Hash:      []byte(latest.Hash),
		Timestamp: uint64(latest.LedgerCloseTime),
	}, nil
}

// FinalizedHead returns the same as LatestHead since Stellar ledgers are final on close.
func (r *Relayer) FinalizedHead(ctx context.Context) (relaytypes.Head, error) {
	return r.LatestHead(ctx)
}

// ListNodeStatuses returns a minimal status list for the configured RPC node.
func (r *Relayer) ListNodeStatuses(_ context.Context, _ int32, _ string) ([]relaytypes.NodeStatus, string, int, error) {
	return []relaytypes.NodeStatus{}, "", 0, nil
}
