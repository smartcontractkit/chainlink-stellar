package txm

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
)

const serviceName = "stellar.txm.Service"

var _ TxManager = (*Txm)(nil)

// Txm is the Stellar Transaction Manager. It provides lifecycle-managed
// transaction submission with simulation, signing, broadcast, confirmation
// polling, retry on transient failures, and ledger-bounds-based expiry.
type Txm struct {
	rpc        ccvclient.RPCClient
	ks         Keystore
	seqStore   *SequenceStore
	feeEst     *FeeEstimator
	store      *TxStore
	bcast      *broadcaster
	cfm        *confirmer
	metrics    *Metrics
	cfg        Config
	passphrase string
	lggr       zerolog.Logger

	enqueueCh chan *txEntry
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	started   bool
	mu        sync.Mutex

	lastPrune time.Time
}

// NewTxm creates a new Stellar TXM. Call Start() to begin background processing.
func NewTxm(
	rpc ccvclient.RPCClient,
	networkPassphrase string,
	ks Keystore,
	cfg Config,
	lggr zerolog.Logger,
) *Txm {
	store := NewTxStore()
	seqStore := NewSequenceStore(rpc, ks.SignerAddress())
	feeEst := NewFeeEstimator(rpc, cfg)
	metrics := NewMetrics(networkPassphrase, ks.SignerAddress())
	enqueueCh := make(chan *txEntry, cfg.MaxQueueSize)

	return &Txm{
		rpc:        rpc,
		ks:         ks,
		seqStore:   seqStore,
		feeEst:     feeEst,
		store:      store,
		metrics:    metrics,
		cfg:        cfg,
		passphrase: networkPassphrase,
		lggr:       lggr.With().Str("service", serviceName).Logger(),
		enqueueCh:  enqueueCh,
		bcast: newBroadcaster(
			rpc, ks, seqStore, feeEst, store, metrics, cfg, networkPassphrase, lggr,
		),
		cfm: newConfirmer(rpc, store, seqStore, metrics, cfg, lggr, enqueueCh),
	}
}

// Start initialises the broadcaster and confirmer goroutines.
func (t *Txm) Start(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.started {
		return nil
	}

	t.ctx, t.cancel = context.WithCancel(ctx)

	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.runBroadcaster()
	}()

	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.cfm.run(t.ctx)
	}()

	t.started = true
	t.lggr.Info().Msg("TXM started")
	return nil
}

// Close shuts down gracefully, draining in-flight work.
func (t *Txm) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.started {
		return nil
	}

	t.cancel()
	t.wg.Wait()
	t.started = false
	t.lggr.Info().Msg("TXM stopped")
	return nil
}

// Ready returns nil when the TXM is ready to accept transactions.
func (t *Txm) Ready() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		return ErrNotStarted
	}
	return nil
}

// HealthReport returns a map of component → error for monitoring.
func (t *Txm) HealthReport() map[string]error {
	return map[string]error{t.Name(): t.Ready()}
}

// Name returns the service name.
func (t *Txm) Name() string {
	return serviceName
}

// Enqueue submits a transaction request for asynchronous processing.
// Returns the transaction ID (auto-generated if TxRequest.ID is empty).
func (t *Txm) Enqueue(_ context.Context, req TxRequest) (string, error) {
	if err := t.Ready(); err != nil {
		return "", err
	}

	if req.ID == "" {
		req.ID = uuid.New().String()
	}

	t.maybePrune()

	entry, err := t.store.Add(req)
	if err != nil {
		return "", err
	}

	t.metrics.SetPending(int64(len(t.enqueueCh) + 1))

	select {
	case t.enqueueCh <- entry:
		return req.ID, nil
	default:
		t.store.SetFailed(req.ID, ErrQueueFull)
		t.metrics.IncrDrop()
		return "", ErrQueueFull
	}
}

// EnqueueAndWait submits a transaction and blocks until it reaches a
// terminal state (confirmed/failed/expired).
func (t *Txm) EnqueueAndWait(ctx context.Context, req TxRequest) (*TxResult, error) {
	if err := t.Ready(); err != nil {
		return nil, err
	}

	if req.ID == "" {
		req.ID = uuid.New().String()
	}

	t.maybePrune()

	entry, err := t.store.Add(req)
	if err != nil {
		return nil, err
	}

	t.metrics.SetPending(int64(len(t.enqueueCh) + 1))

	select {
	case t.enqueueCh <- entry:
	default:
		t.store.SetFailed(req.ID, ErrQueueFull)
		t.metrics.IncrDrop()
		return nil, ErrQueueFull
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-entry.Done:
	}

	return t.buildResult(entry), nil
}

// GetTransactionStatus returns the current status of a transaction by its ID.
func (t *Txm) GetTransactionStatus(_ context.Context, txID string) (TxStatus, error) {
	status, ok := t.store.Status(txID)
	if !ok {
		return 0, ErrTxNotFound
	}
	return status, nil
}

// GetTransactionResult returns the full result of a terminal transaction.
func (t *Txm) GetTransactionResult(_ context.Context, txID string) (*TxResult, error) {
	entry := t.store.Get(txID)
	if entry == nil {
		return nil, ErrTxNotFound
	}
	if !entry.Status.Terminal() {
		return nil, ErrTxPending
	}
	return t.buildResult(entry), nil
}

// GetTransactionFee returns the fee charged for a confirmed transaction
// in stroops. Returns an error for non-confirmed transactions.
func (t *Txm) GetTransactionFee(_ context.Context, txID string) (*big.Int, error) {
	entry := t.store.Get(txID)
	if entry == nil {
		return nil, ErrTxNotFound
	}
	if entry.Status != TxStatusConfirmed {
		return nil, fmt.Errorf("tx %s is %s, not confirmed", txID, entry.Status)
	}
	return big.NewInt(entry.FeeCharged), nil
}

// InflightCount returns the enqueue channel depth and total unconfirmed
// transaction count.
func (t *Txm) InflightCount() (channelDepth int, totalUnconfirmed int) {
	return len(t.enqueueCh), t.store.UnconfirmedCount()
}

func (t *Txm) runBroadcaster() {
	for {
		select {
		case <-t.ctx.Done():
			return
		case entry := <-t.enqueueCh:
			t.processTx(entry)
			t.metrics.SetPending(int64(len(t.enqueueCh)))
		}
	}
}

func (t *Txm) processTx(entry *txEntry) {
	err := t.bcast.broadcast(t.ctx, entry)
	if err == nil {
		return
	}

	t.lggr.Warn().Err(err).Str("txID", entry.Request.ID).Int("attempt", entry.Attempt).Msg("Broadcast failed")
	t.metrics.IncrError()

	if isRetryable(err) && entry.Attempt < t.cfg.MaxRetries {
		if isSequenceErr(err) {
			if syncErr := t.seqStore.Sync(t.ctx); syncErr != nil {
				t.lggr.Error().Err(syncErr).Msg("Failed to sync sequence after conflict")
			}
		}
		newAttempt := t.store.IncrementRetry(entry.Request.ID)
		t.lggr.Info().
			Str("txID", entry.Request.ID).
			Int("attempt", newAttempt).
			Msg("Retrying transaction from broadcaster")
		t.metrics.IncrRetry()

		select {
		case t.enqueueCh <- entry:
		default:
			t.store.SetFailed(entry.Request.ID, fmt.Errorf("retry queue full: %w", err))
			t.metrics.IncrReject()
		}
		return
	}

	t.store.SetFailed(entry.Request.ID, err)
	t.metrics.IncrReject()
}

func (t *Txm) buildResult(entry *txEntry) *TxResult {
	return &TxResult{
		Status:     entry.Status,
		Hash:       entry.Hash,
		ResultMeta: entry.Meta,
		ResultXDR:  entry.ResultXDR,
		FeeCharged: entry.FeeCharged,
		Error:      entry.Error,
		LedgerNum:  entry.Ledger,
	}
}

// maybePrune removes terminal transactions if enough time has passed since
// the last prune. Called on each Enqueue to avoid a separate goroutine.
func (t *Txm) maybePrune() {
	now := time.Now()
	if now.Sub(t.lastPrune) < t.cfg.PruneInterval {
		return
	}
	t.lastPrune = now
	if reaped := t.store.Reap(t.cfg.PruneThreshold); reaped > 0 {
		t.lggr.Debug().Int("reaped", reaped).Msg("Pruned terminal transactions")
	}
}

func isRetryable(err error) bool {
	return isSequenceErr(err) || errors.Is(err, ErrOverloaded)
}

func isSequenceErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrSequence) || isSequenceError(err.Error())
}
