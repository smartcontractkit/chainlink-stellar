package txm

import (
	"context"
	"fmt"
	"sync"

	"github.com/rs/zerolog"

	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
)

const serviceName = "stellar.txm.Service"

var _ TxManager = (*Txm)(nil)

// Txm is the Stellar Transaction Manager. It provides lifecycle-managed
// transaction submission with simulation, signing, broadcast, confirmation
// polling, retry on transient failures, and ledger-bounds-based expiry.
type Txm struct {
	rpc           ccvclient.RPCClient
	ks            Keystore
	seqMgr        *SequenceManager
	feeEst        *FeeEstimator
	store         *TxStore
	bcast         *broadcaster
	cfm           *confirmer
	cfg           Config
	passphrase    string
	lggr          zerolog.Logger

	enqueueCh     chan *txEntry
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	started       bool
	mu            sync.Mutex
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
	seqMgr := NewSequenceManager(rpc, ks.SignerAddress())
	feeEst := NewFeeEstimator(rpc, cfg.FeeBuffer)

	return &Txm{
		rpc:        rpc,
		ks:         ks,
		seqMgr:     seqMgr,
		feeEst:     feeEst,
		store:      store,
		cfg:        cfg,
		passphrase: networkPassphrase,
		lggr:       lggr.With().Str("service", serviceName).Logger(),
		enqueueCh:  make(chan *txEntry, cfg.MaxQueueSize),
		bcast: newBroadcaster(
			rpc, ks, seqMgr, feeEst, store, cfg, networkPassphrase, lggr,
		),
		cfm: newConfirmer(rpc, store, cfg, lggr),
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

	// Broadcaster goroutine: reads from the enqueue channel.
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.runBroadcaster()
	}()

	// Confirmer goroutine: polls broadcast txs.
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
// Returns immediately. The transaction will be broadcast by the background
// goroutine and confirmed by the confirmer.
func (t *Txm) Enqueue(_ context.Context, req TxRequest) error {
	if err := t.Ready(); err != nil {
		return err
	}

	entry, err := t.store.Add(req)
	if err != nil {
		return err
	}

	select {
	case t.enqueueCh <- entry:
		return nil
	default:
		t.store.SetFailed(req.ID, ErrQueueFull)
		return ErrQueueFull
	}
}

// EnqueueAndWait submits a transaction and blocks until it reaches a
// terminal state (confirmed/failed/expired).
func (t *Txm) EnqueueAndWait(ctx context.Context, req TxRequest) (*TxResult, error) {
	if err := t.Ready(); err != nil {
		return nil, err
	}

	entry, err := t.store.Add(req)
	if err != nil {
		return nil, err
	}

	select {
	case t.enqueueCh <- entry:
	default:
		t.store.SetFailed(req.ID, ErrQueueFull)
		return nil, ErrQueueFull
	}

	// Block until terminal or context cancellation.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-entry.Done:
	}

	return t.buildResult(entry), nil
}

// GetTransactionStatus returns the current status of a transaction by its ID.
func (t *Txm) GetTransactionStatus(_ context.Context, txID string) (TxStatus, error) {
	entry := t.store.Get(txID)
	if entry == nil {
		return 0, ErrTxNotFound
	}
	return entry.Status, nil
}

func (t *Txm) runBroadcaster() {
	for {
		select {
		case <-t.ctx.Done():
			return
		case entry := <-t.enqueueCh:
			t.processTx(entry)
		}
	}
}

func (t *Txm) processTx(entry *txEntry) {
	err := t.bcast.broadcast(t.ctx, entry)
	if err == nil {
		return
	}

	t.lggr.Warn().Err(err).Str("txID", entry.Request.ID).Int("retries", entry.Retries).Msg("Broadcast failed")

	// Handle retryable errors
	if isRetryable(err) && entry.Retries < t.cfg.MaxRetries {
		// Sync sequence on sequence errors
		if isSequenceErr(err) {
			if syncErr := t.seqMgr.Sync(t.ctx); syncErr != nil {
				t.lggr.Error().Err(syncErr).Msg("Failed to sync sequence after conflict")
			}
		}
		retries := t.store.IncrementRetry(entry.Request.ID)
		t.lggr.Info().
			Str("txID", entry.Request.ID).
			Int("retry", retries).
			Msg("Retrying transaction")

		// Re-enqueue
		select {
		case t.enqueueCh <- entry:
		default:
			t.store.SetFailed(entry.Request.ID, fmt.Errorf("retry queue full: %w", err))
		}
		return
	}

	t.store.SetFailed(entry.Request.ID, err)
}

func (t *Txm) buildResult(entry *txEntry) *TxResult {
	return &TxResult{
		Status:     entry.Status,
		Hash:       entry.Hash,
		ResultMeta: entry.Meta,
		Error:      entry.Error,
		LedgerNum:  entry.Ledger,
	}
}

func isRetryable(err error) bool {
	return isSequenceErr(err) || err == ErrOverloaded
}

func isSequenceErr(err error) bool {
	if err == nil {
		return false
	}
	return err == ErrSequence || isSequenceError(err.Error())
}
