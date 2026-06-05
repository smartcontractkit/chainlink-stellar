package relayer

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	"github.com/smartcontractkit/chainlink-common/pkg/loop"
	"github.com/smartcontractkit/chainlink-common/pkg/services"
	relaytypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	"github.com/smartcontractkit/chainlink-stellar/relayer/chain"
	"github.com/smartcontractkit/chainlink-stellar/relayer/chainwriter"
)

var _ loop.Relayer = (*Relayer)(nil)
var _ relaytypes.StellarService = (*Relayer)(nil)

// Relayer wraps the Stellar chain and exposes it as loop.Relayer.
type Relayer struct {
	relaytypes.UnimplementedRelayer

	chainService chain.Chain
	lggr         logger.Logger
	starter      services.StateMachine
	stopCh       services.StopChan

	stellarService
}

// NewRelayer constructs a Relayer from an already-built chain.
func NewRelayer(lggr logger.Logger, chainService chain.Chain) *Relayer {
	lggr = logger.Named(lggr, "StellarRelayer")
	return &Relayer{
		chainService:   chainService,
		lggr:           lggr,
		stopCh:         make(chan struct{}),
		stellarService: newStellarService(chainService),
	}
}

func (r *Relayer) Name() string { return r.lggr.Name() }

func (r *Relayer) Start(ctx context.Context) error {
	return r.starter.StartOnce("StellarRelayer", func() error {
		r.lggr.Debugw("Starting")
		if r.chainService == nil {
			return errors.New("stellar chain unavailable")
		}
		var ms services.MultiStart
		return ms.Start(ctx, r.chainService)
	})
}

func (r *Relayer) Close() error {
	return r.starter.StopOnce("StellarRelayer", func() error {
		r.lggr.Debugw("Stopping")
		close(r.stopCh)
		return services.CloseAll(r.chainService)
	})
}

func (r *Relayer) Ready() error { return r.starter.Ready() }

func (r *Relayer) Healthy() error { return r.starter.Healthy() }

func (r *Relayer) HealthReport() map[string]error {
	report := map[string]error{r.Name(): r.starter.Healthy()}
	services.CopyHealth(report, r.chainService.HealthReport())
	return report
}

func (r *Relayer) GetChainInfo(ctx context.Context) (relaytypes.ChainInfo, error) {
	return r.chainService.GetChainInfo(ctx)
}

func (r *Relayer) GetChainStatus(ctx context.Context) (relaytypes.ChainStatus, error) {
	return r.chainService.GetChainStatus(ctx)
}

func (r *Relayer) LatestHead(ctx context.Context) (relaytypes.Head, error) {
	return r.chainService.LatestHead(ctx)
}

func (r *Relayer) FinalizedHead(ctx context.Context) (relaytypes.Head, error) {
	return r.chainService.FinalizedHead(ctx)
}

func (r *Relayer) ListNodeStatuses(ctx context.Context, pageSize int32, pageToken string) ([]relaytypes.NodeStatus, string, int, error) {
	return r.chainService.ListNodeStatuses(ctx, pageSize, pageToken)
}

func (r *Relayer) Transact(ctx context.Context, from, to string, amount *big.Int, balanceCheck bool) error {
	return r.chainService.Transact(ctx, from, to, amount, balanceCheck)
}

func (r *Relayer) Stellar() (relaytypes.StellarService, error) {
	return &r.stellarService, nil
}

func (r *Relayer) NewContractWriter(_ context.Context, configBytes []byte) (relaytypes.ContractWriter, error) {
	cfg, err := chainwriter.ParseConfig(configBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse chain writer config: %w", err)
	}

	txm := r.chainService.TxManager()
	if txm == nil {
		return nil, errors.New("stellar txm is unavailable")
	}

	ks := r.chainService.KeyStore()
	if ks == nil {
		return nil, errors.New("stellar keystore is unavailable")
	}

	return chainwriter.NewChainWriter(r.lggr, txm, ks, cfg)
}
