package txm

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	chain_selectors "github.com/smartcontractkit/chain-selectors"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	"github.com/smartcontractkit/chainlink-common/pkg/services"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	"github.com/smartcontractkit/chainlink-common/pkg/types/core"
	commonutils "github.com/smartcontractkit/chainlink-common/pkg/utils"

	"github.com/smartcontractkit/chainlink-stellar/relayer/config"
)

// RPCClient is the subset of the Stellar Soroban JSON-RPC client used by the TXM.
// Any value satisfying chain.RPCClient (a superset) automatically satisfies this.
type RPCClient interface {
	SimulateTransaction(ctx context.Context, req protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error)
	SendTransaction(ctx context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error)
	GetTransaction(ctx context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error)
	GetEvents(ctx context.Context, req protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error)
	GetLedgerEntries(ctx context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error)
	GetLatestLedger(ctx context.Context) (protocolrpc.GetLatestLedgerResponse, error)
	GetFeeStats(ctx context.Context) (protocolrpc.GetFeeStatsResponse, error)
}

var _ services.Service = &StellarTxm{}

// StellarTxm orchestrates the lifecycle of Stellar/Soroban transactions:
// enqueue → simulate → (restore) → assemble → sign → send → confirm.
type StellarTxm struct {
	baseLogger logger.Logger
	keystore   core.Keystore
	config     config.Config
	chainID    string
	metrics    TxmMetrics
	feeStrat   FeeStrategy

	// transactions + transactionsLock guard both the tx ID → *StellarTx map and
	// many per-tx field updates (status, hash, fee, XDR, attempt). That makes
	// this mutex a universal coarse lock for the TXM.
	//
	// TODO: improve concurrency — e.g. reserve transactionsLock for map
	// membership/prune only, and use per-StellarTx synchronization (or batched
	// updates) for mutable fields so GetStatus / confirm / enqueue contend less.
	transactions     map[string]*StellarTx
	transactionsLock sync.RWMutex

	broadcastChan chan *StellarTx
	accountStore  *AccountStore
	starter       commonutils.StartStopOnce
	done          sync.WaitGroup
	stop          chan struct{}

	feeTracker *feeTracker

	getClient         func(context.Context) (RPCClient, error)
	networkPassphrase string
}

// New creates a StellarTxm. The getClient callback should be obtained from
// chain.Chain.GetClient to enable multi-node rotation; in normal wiring the
// chain package constructs the TXM and passes its own GetClient method.
// The network passphrase is resolved from chainID via NetworkPassphrase.
//
// New calls cfg.Resolve() to guarantee every pointer field is non-nil. In
// production the config arrives already resolved (NewDecodedTOMLConfig ->
// SetDefaults merges TXM defaults via SetFrom), so Resolve is a no-op. Tests
// that construct a config.Config directly (e.g. config.Config{}) rely on
// Resolve to fill defaults before the TXM dereferences them.
func New(
	lgr logger.Logger,
	keystore core.Keystore,
	cfg config.Config,
	getClient func(context.Context) (RPCClient, error),
	chainID string,
) (*StellarTxm, error) {
	cfg.Resolve()

	passphrase, err := chain_selectors.StellarPassphraseFromChainId(chainID)
	if err != nil {
		return nil, fmt.Errorf("resolve network passphrase: %w", err)
	}

	metrics := NewStellarTxmMetrics(lgr, chainID)

	return &StellarTxm{
		baseLogger: logger.Named(lgr, "StellarTxm"),
		keystore:   keystore,
		config:     cfg,
		chainID:    chainID,
		metrics:    metrics,
		feeStrat:   NewFeeStrategyFromConfig(cfg),

		feeTracker: newFeeTracker(cfg.FeeStatsPollInterval.Duration()),

		transactions: make(map[string]*StellarTx),

		broadcastChan:     make(chan *StellarTx, *cfg.BroadcastChanSize),
		accountStore:      NewAccountStore(),
		stop:              make(chan struct{}),
		getClient:         getClient,
		networkPassphrase: passphrase,
	}, nil
}

// --- services.Service ---

func (s *StellarTxm) Name() string {
	return s.baseLogger.Name()
}

func (s *StellarTxm) Ready() error {
	return s.starter.Ready()
}

func (s *StellarTxm) HealthReport() map[string]error {
	return map[string]error{s.Name(): s.starter.Healthy()}
}

func (s *StellarTxm) Start(_ context.Context) error {
	return s.starter.StartOnce(s.Name(), func() error {
		pruneEnabled := s.config.PruneInterval.Duration() > 0
		goroutines := 2
		if pruneEnabled {
			goroutines++
		}
		s.done.Add(goroutines)
		go s.broadcastLoop()
		go s.confirmLoop()
		if pruneEnabled {
			go s.pruneLoop()
		}
		return nil
	})
}

func (s *StellarTxm) Close() error {
	return s.starter.StopOnce(s.Name(), func() error {
		close(s.stop)
		s.done.Wait()
		close(s.broadcastChan)
		return nil
	})
}

// --- Enqueue ---

// Enqueue submits a Soroban transaction request for asynchronous processing.
// Returns the transaction ID (auto-generated if TxRequest.ID is empty).
// If TxRequest.ID is already in flight or tracked, returns that same id with a nil error
// and does not enqueue again (idempotent, aligned with EVM TxMgr idempotency key behavior).
func (s *StellarTxm) Enqueue(ctx context.Context, req TxRequest) (string, error) {
	txID := req.ID
	if txID == "" {
		txID = uuid.New().String()
	}

	if len(req.Operations) != 1 {
		return "", errors.New("currently only single-operation transactions are supported")
	}

	fromAddr := req.FromAddress
	if fromAddr == "" {
		var err error
		fromAddr, err = s.defaultFromAddress(ctx)
		if err != nil {
			return "", err
		}
	}
	// Validate caller-supplied addresses up front so an invalid value can't reach downstream
	if _, err := xdr.AddressToAccountId(fromAddr); err != nil {
		return "", fmt.Errorf("invalid FromAddress %q: %w", fromAddr, err)
	}

	tx := &StellarTx{
		ID:                 txID,
		Timestamp:          time.Now(),
		FromAddress:        fromAddr,
		Operations:         req.Operations,
		LedgerBoundsOffset: req.LedgerBoundsOffset,
		Metadata:           req.Metadata,
		Status:             commontypes.Pending,
		Done:               make(chan struct{}),
	}

	return s.enqueueTransaction(ctx, tx)
}

// EnqueueAndWait submits a transaction and blocks until it reaches a terminal
// state (Finalized, Failed) or the context is cancelled.
func (s *StellarTxm) EnqueueAndWait(ctx context.Context, req TxRequest) (*TxResult, error) {
	txID, err := s.Enqueue(ctx, req)
	if err != nil {
		return nil, err
	}

	s.transactionsLock.RLock()
	tx, ok := s.transactions[txID]
	s.transactionsLock.RUnlock()
	if !ok {
		return nil, fmt.Errorf("transaction %s not found after enqueue", txID)
	}

	select {
	case <-tx.Done:
		return s.txResult(tx), nil
	case <-ctx.Done():
		return nil, fmt.Errorf("context cancelled while waiting for tx %s: %w", txID, ctx.Err())
	case <-s.stop:
		return nil, fmt.Errorf("txm stopped while waiting for tx %s", txID)
	}
}

func (s *StellarTxm) txResult(tx *StellarTx) *TxResult {
	s.transactionsLock.RLock()
	defer s.transactionsLock.RUnlock()

	return s.txResultLocked(tx)
}

func (s *StellarTxm) txResultLocked(tx *StellarTx) *TxResult {
	result := &TxResult{
		ID:              tx.ID,
		Hash:            tx.TxHash,
		Status:          tx.Status,
		Fee:             tx.Fee,
		LedgerCloseTime: tx.LedgerCloseTime,
		ResultXDR:       tx.ResultXDR,
		ResultMetaXDR:   tx.ResultMetaXDR,
	}
	if tx.ResultCode != "" {
		result.Error = fmt.Errorf("transaction result: %s", tx.ResultCode)
	}
	return result
}

// enqueueTransaction stores the tx and pushes it to broadcastChan.
// If tx.ID is already present (after prune), returns that id with a nil error and does not
// enqueue again (idempotent, matching EVM TxMgr CreateTransaction with IdempotencyKey).
// On backpressure it drops the oldest queued tx (not the new one): the oldest has
// the stalest simulation data and the nearest LedgerBounds expiry, so the newer tx's
// intent takes priority.
func (s *StellarTxm) enqueueTransaction(ctx context.Context, tx *StellarTx) (string, error) {
	ctxLogger := GetContextedTxLogger(s.baseLogger, tx.ID, nil)

	s.transactionsLock.Lock()
	if _, exists := s.transactions[tx.ID]; exists {
		s.transactionsLock.Unlock()
		ctxLogger.Debugw("enqueue idempotent: tx id already present, not re-enqueueing", "txID", tx.ID)
		s.closeDone(tx)
		return tx.ID, nil
	}
	s.transactions[tx.ID] = tx
	s.transactionsLock.Unlock()

	// Fast path: channel has space.
	select {
	case s.broadcastChan <- tx:
		ctxLogger.Debugw("tx enqueued", "fromAddr", tx.FromAddress, "txID", tx.ID)
		return tx.ID, nil
	default:
	}

	// Slow path: channel full. Drain the oldest queued tx (FIFO head) and
	// mark it dropped, then send the new one into the freed slot.
	var droppedTx *StellarTx
	select {
	case droppedTx = <-s.broadcastChan:
	default:
		// Channel became non-full between the fast-path check and now
		// (broadcastLoop drained it). Proceed directly to the send retry.
	}
	if droppedTx != nil {
		s.dropOldestForBackpressure(ctx, droppedTx)
	}

	select {
	case s.broadcastChan <- tx:
		droppedID := ""
		if droppedTx != nil {
			droppedID = droppedTx.ID
		}
		ctxLogger.Debugw("tx enqueued after evicting oldest", "droppedTxID", droppedID, "txID", tx.ID)
		return tx.ID, nil
	default:
		// Concurrent enqueues refilled the slot. Fall back to dropping the new tx.
		s.transactionsLock.Lock()
		delete(s.transactions, tx.ID)
		s.transactionsLock.Unlock()
		s.metrics.IncrementDroppedTxs(ctx, DropReasonChannelFullNewRejected)
		ctxLogger.Errorw("broadcast channel still full after eviction, dropping new tx", "txID", tx.ID)
		return "", fmt.Errorf("broadcast channel full, tx %s dropped", tx.ID)
	}
}

// dropOldestForBackpressure marks a tx (just drained from broadcastChan) as Failed,
// unblocks any EnqueueAndWait waiter, and emits the drop metric.
// dropped must be the same *StellarTx still registered in s.transactions[dropped.ID];
// if the map entry was replaced or removed, this is a no-op.
func (s *StellarTxm) dropOldestForBackpressure(ctx context.Context, dropped *StellarTx) {
	if dropped == nil {
		return
	}
	s.transactionsLock.Lock()
	ok := false
	if cur, exists := s.transactions[dropped.ID]; exists && cur == dropped {
		dropped.ResultCode = string(DropReasonChannelFullOldestEvicted)
		ok = true
	}
	s.transactionsLock.Unlock()
	if !ok {
		return
	}

	s.updateTransactionStatus(dropped, commontypes.Failed)
	s.metrics.IncrementDroppedTxs(ctx, DropReasonChannelFullOldestEvicted)

	ctxLogger := GetContextedTxLogger(s.baseLogger, dropped.ID, dropped.Metadata)
	ctxLogger.Warnw("oldest queued tx evicted due to channel backpressure",
		"droppedTxID", dropped.ID,
		"age", time.Since(dropped.Timestamp).Round(time.Second))
}

// --- Status queries ---

func (s *StellarTxm) GetStatus(transactionID string) (commontypes.TransactionStatus, error) {
	if transactionID == "" {
		return commontypes.Unknown, errors.New("empty transaction ID")
	}

	s.transactionsLock.RLock()
	defer s.transactionsLock.RUnlock()
	tx, ok := s.transactions[transactionID]
	if !ok {
		return commontypes.Unknown, errors.New("no such transaction")
	}
	return tx.Status, nil
}

func (s *StellarTxm) GetTransactionResult(transactionID string) (*TxResult, error) {
	if transactionID == "" {
		return nil, errors.New("empty transaction ID")
	}

	s.transactionsLock.RLock()
	defer s.transactionsLock.RUnlock()
	tx, ok := s.transactions[transactionID]
	if !ok {
		return nil, errors.New("no such transaction")
	}

	return s.txResultLocked(tx), nil
}

func (s *StellarTxm) GetTransactionFee(transactionID string) (*big.Int, error) {
	if transactionID == "" {
		return nil, errors.New("empty transaction ID")
	}

	s.transactionsLock.RLock()
	defer s.transactionsLock.RUnlock()
	tx, ok := s.transactions[transactionID]
	if !ok {
		return nil, errors.New("no such transaction")
	}
	if tx.Status != commontypes.Finalized {
		return nil, fmt.Errorf("transaction not finalized, current status: %v", tx.Status)
	}
	if tx.Fee == nil {
		return nil, errors.New("transaction fee not available")
	}
	return tx.Fee, nil
}

// InflightCount returns (queued broadcast work items, total unconfirmed across all accounts).
func (s *StellarTxm) InflightCount() (int, int) {
	return len(s.broadcastChan), s.accountStore.GetTotalInflightCount()
}

// --- Transaction status helpers ---

func isTerminalStatus(status commontypes.TransactionStatus) bool {
	return status == commontypes.Finalized || status == commontypes.Failed
}

// terminalPastRetention reports whether a terminal tx has exceeded the retention
// window. expiration may be 0 for immediate eviction (sync-prune mode).
func terminalPastRetention(tx *StellarTx, expiration time.Duration) bool {
	if !isTerminalStatus(tx.Status) {
		return false
	}
	if tx.TerminalTime.IsZero() {
		return false
	}
	return time.Since(tx.TerminalTime) >= expiration
}

func (s *StellarTxm) updateTransactionStatus(tx *StellarTx, status commontypes.TransactionStatus) {
	s.transactionsLock.Lock()
	tx.Status = status
	terminal := isTerminalStatus(status)
	if terminal && tx.TerminalTime.IsZero() {
		tx.TerminalTime = time.Now()
	}
	if terminal {
		s.maybeEvictTerminalTx(tx)
	}
	s.transactionsLock.Unlock()
	if terminal {
		s.closeDone(tx)
	}
}

// maybeEvictTerminalTx removes a terminal tx when background pruning is disabled.
// Must be called with transactionsLock held. The caller (updateTransactionStatus)
// has already stamped TerminalTime before invoking this.
func (s *StellarTxm) maybeEvictTerminalTx(tx *StellarTx) {
	if s.config.PruneInterval.Duration() > 0 {
		return
	}
	s.baseLogger.Debugw("maybeEvictTerminalTx: evicting terminal tx immediately",
		"txID", tx.ID, "status", tx.Status)
	delete(s.transactions, tx.ID)
}

// markTxFailed sets Failed (and closes Done via updateTransactionStatus) and records
// an error metric. Use when no TxStore sequence was reserved for this attempt.
func (s *StellarTxm) markTxFailed(ctx context.Context, tx *StellarTx, metricReason ErrorReason) {
	s.updateTransactionStatus(tx, commontypes.Failed)
	s.metrics.IncrementErrorTxs(ctx, metricReason)
}

// releaseSeqAndFailTx releases a reserved sequence, marks the tx Failed, and records
// an error metric. Used when simulate/send aborts after GetNextSequence.
func (s *StellarTxm) releaseSeqAndFailTx(ctx context.Context, txStore *TxStore, seq int64, tx *StellarTx, metricReason ErrorReason) {
	txStore.Release(seq)
	s.updateTransactionStatus(tx, commontypes.Failed)
	s.metrics.IncrementErrorTxs(ctx, metricReason)
}

func (s *StellarTxm) markBroadcastAt(tx *StellarTx) {
	s.transactionsLock.Lock()
	defer s.transactionsLock.Unlock()
	tx.BroadcastAt = time.Now()
}

func (s *StellarTxm) recordTimeUntilConfirmed(ctx context.Context, tx *StellarTx) {
	s.transactionsLock.Lock()
	broadcastAt := tx.BroadcastAt
	s.transactionsLock.Unlock()
	if broadcastAt.IsZero() {
		return
	}
	s.metrics.RecordTimeUntilTxConfirmed(ctx, time.Since(broadcastAt).Seconds())
}

func (s *StellarTxm) updateTransactionHash(tx *StellarTx, hash string) {
	s.transactionsLock.Lock()
	defer s.transactionsLock.Unlock()
	tx.TxHash = hash
}

func (s *StellarTxm) updateTransactionFee(tx *StellarTx, fee *big.Int) {
	s.transactionsLock.Lock()
	defer s.transactionsLock.Unlock()
	tx.Fee = fee
}

func (s *StellarTxm) updateTransactionLedgerCloseTime(tx *StellarTx, ledgerCloseTime int64) {
	if ledgerCloseTime <= 0 {
		return
	}
	s.transactionsLock.Lock()
	defer s.transactionsLock.Unlock()
	tx.LedgerCloseTime = ledgerCloseTime
}

func (s *StellarTxm) updateTransactionResultXDR(tx *StellarTx, resultXDR string) {
	s.transactionsLock.Lock()
	defer s.transactionsLock.Unlock()
	tx.ResultXDR = resultXDR
}

func (s *StellarTxm) updateTransactionResultCode(tx *StellarTx, code string) {
	s.transactionsLock.Lock()
	defer s.transactionsLock.Unlock()
	tx.ResultCode = code
}

func (s *StellarTxm) updateTransactionResultMeta(tx *StellarTx, resultMetaXDR string) {
	s.transactionsLock.Lock()
	defer s.transactionsLock.Unlock()
	tx.ResultMetaXDR = resultMetaXDR
}

func (s *StellarTxm) incrementTransactionAttempt(tx *StellarTx) {
	s.transactionsLock.Lock()
	defer s.transactionsLock.Unlock()
	tx.Attempt++
}

func (s *StellarTxm) getTransactionAttempt(tx *StellarTx) uint64 {
	s.transactionsLock.RLock()
	defer s.transactionsLock.RUnlock()
	return tx.Attempt
}

func (s *StellarTxm) incrementInfraAttempts(tx *StellarTx) {
	s.transactionsLock.Lock()
	defer s.transactionsLock.Unlock()
	tx.InfraAttempts++
}

func (s *StellarTxm) getInfraAttempts(tx *StellarTx) uint64 {
	s.transactionsLock.RLock()
	defer s.transactionsLock.RUnlock()
	return tx.InfraAttempts
}

// closeDone closes the transaction's Done channel to unblock EnqueueAndWait callers.
// Terminal statuses set via updateTransactionStatus also invoke closeDone.
func (s *StellarTxm) closeDone(tx *StellarTx) {
	tx.doneOnce.Do(func() {
		close(tx.Done)
	})
}

// --- Broadcast loop ---

func (s *StellarTxm) broadcastLoop() {
	defer s.done.Done()

	ctx, cancel := commonutils.ContextFromChan(s.stop)
	defer cancel()

	s.baseLogger.Debugw("broadcastLoop: started")
	for {
		select {
		case initialTx := <-s.broadcastChan:
			if initialTx == nil {
				continue
			}
			broadcastTxs := []*StellarTx{initialTx}
		DrainChannel:
			for {
				select {
				case nextTx := <-s.broadcastChan:
					if nextTx == nil {
						continue
					}
					broadcastTxs = append(broadcastTxs, nextTx)
				default:
					break DrainChannel
				}
			}

			sort.Slice(broadcastTxs, func(i, j int) bool {
				return broadcastTxs[i].Timestamp.Before(broadcastTxs[j].Timestamp)
			})

			for _, tx := range broadcastTxs {
				s.simulateAssembleSignAndSend(ctx, tx)
			}
		case <-s.stop:
			s.baseLogger.Debugw("broadcastLoop: stopped")
			return
		}
	}
}

// simulateAssembleSignAndSend runs the full Stellar broadcast pipeline for a
// single transaction: simulate → (restore archived entries) → assemble →
// sign → send, with retry on transient failures and inclusion-fee bumping
// seeded from feeTracker GetFeeStats Soroban percentiles.
func (s *StellarTxm) simulateAssembleSignAndSend(ctx context.Context, tx *StellarTx) {
	ctxLogger := GetContextedTxLogger(s.baseLogger, tx.ID, tx.Metadata)
	client, err := s.getClient(ctx)
    if err != nil {
        ctxLogger.Errorw("failed to get RPC client", "error", err)
        s.retryGetClientOrFail(ctx, tx, ctxLogger)
        return
    }

	txStore := s.accountStore.GetTxStore(tx.FromAddress)
	if txStore == nil {
		seqNum, err := s.getSequenceNumber(ctx, client, tx.FromAddress)
		if err != nil {
			ctxLogger.Errorw("failed to get sequence number", "fromAddress", tx.FromAddress, "error", err)
			s.markTxFailed(ctx, tx, ErrorReasonSequenceNumber)
			return
		}
		newStore, err := s.accountStore.CreateTxStore(tx.FromAddress, seqNum+1)
		if err != nil {
			ctxLogger.Errorw("failed to create tx store", "fromAddress", tx.FromAddress, "error", err)
			s.markTxFailed(ctx, tx, ErrorReasonStoreCreate)
			return
		}
		txStore = newStore
	}

	currentAttempt := s.getTransactionAttempt(tx)
	if currentAttempt > 0 {
		if err := s.resyncSequence(ctx, client, tx); err != nil {
			ctxLogger.Warnw("best-effort sequence resync before rebroadcast failed, continuing with local tx store",
				"error", err, "attempt", currentAttempt)
		}
	}

	// Seed inclusion fee from live network data (P50 first broadcast, P90 rebroadcasts),
	// via feeTracker to cap GetFeeStats RPC rate.
	// SeedInclusionFee caps the result at MaxInclusionFee
	var networkPercentile uint64
	if p50, p90, fsErr := s.feeTracker.sorobanInclusionPercentiles(ctx, client); fsErr == nil {
		if currentAttempt > 0 {
			networkPercentile = p90
		} else {
			networkPercentile = p50
		}
	} else {
		ctxLogger.Warnw("getFeeStats failed, using geometric baseline", "error", fsErr)
	}
	inclusionFee, clampedToMax := s.feeStrat.SeedInclusionFee(currentAttempt, networkPercentile)
	if clampedToMax {
		ctxLogger.Warnw("seeded inclusion fee clamped to MaxInclusionFee — possible misbehaving RPC",
			"networkPercentile", networkPercentile,
			"maxInclusionFee", s.feeStrat.MaxInclusionFee,
			"attempt", currentAttempt,
		)
	}

	seq := txStore.GetNextSequence()
	restoreHandled := false

	for submitAttempt := uint(0); submitAttempt < *s.config.MaxSubmitRetryAttempts; {
		prelimTx, simResult, maxLedger, err := s.prepareAndSimulateWithRetry(ctx, client, tx, seq)
		if err != nil {
			ctxLogger.Errorw("simulation failed", "error", err)
			s.releaseSeqAndFailTx(ctx, txStore, seq, tx, ErrorReasonSimulation)
			return
		}

		// Soroban simulation may return RestorePreamble when contract state or
		// footprint entries are archived on the ledger. We must submit a separate
		// RestoreFootprint transaction (handleRestore), advance sequence, and
		// re-simulate before we can assemble/sign/send the user's invoke.
		if simResult.RestorePreamble != nil {
			if restoreHandled {
				// Already restored once this broadcast; sim still wants restore — fail
				// instead of looping restore → simulate forever.
				ctxLogger.Errorw("restore still required after RestoreFootprint transaction")
				s.releaseSeqAndFailTx(ctx, txStore, seq, tx, ErrorReasonRestoreFailed)
				return
			}
			if err := s.handleRestore(ctx, client, tx, *simResult.RestorePreamble, seq); err != nil {
				ctxLogger.Errorw("failed to restore archived ledger entries", "error", err)
				s.releaseSeqAndFailTx(ctx, txStore, seq, tx, ErrorReasonRestoreFailed)
				return
			}
			restoreHandled = true
			seq = txStore.GetNextSequence()
			continue // retry simulate → assemble → send with restored entries
		}

		tx.MinResourceFee = simResult.MinResourceFee

		assembledTx, totalFee, err := s.assembleTransaction(prelimTx, simResult, inclusionFee, maxLedger)
		if err != nil {
			ctxLogger.Errorw("failed to assemble transaction", "error", err)
			s.releaseSeqAndFailTx(ctx, txStore, seq, tx, ErrorReasonAssembly)
			return
		}

		resourceFee := totalFee - inclusionFee
		if resourceFee < 0 {
			resourceFee = 0
		}
		s.metrics.ObserveInclusionFee(ctx, inclusionFee)
		s.metrics.ObserveResourceFee(ctx, resourceFee)
		s.updateTransactionFee(tx, big.NewInt(totalFee))

		signedTx, err := s.signTransaction(ctx, assembledTx, tx.FromAddress)
		if err != nil {
			ctxLogger.Errorw("failed to sign transaction", "error", err)
			s.releaseSeqAndFailTx(ctx, txStore, seq, tx, ErrorReasonSigning)
			return
		}

		signedXDR, err := signedTx.Base64()
		if err != nil {
			ctxLogger.Errorw("failed to encode signed transaction", "error", err)
			s.releaseSeqAndFailTx(ctx, txStore, seq, tx, ErrorReasonAssembly)
			return
		}

		submitResult, err := client.SendTransaction(ctx, protocolrpc.SendTransactionRequest{
			Transaction: signedXDR,
		})
		if err != nil {
			ctxLogger.Warnw("failed to submit transaction", "attempt", submitAttempt, "error", err)
			submitAttempt++
			if submitAttempt >= *s.config.MaxSubmitRetryAttempts {
				break
			}
			select {
			case <-time.After(s.config.SubmitRetryDelay.Duration()):
			case <-ctx.Done():
				txStore.Release(seq)
				return
			}
			continue
		}

		accepted, fatalErr, retryReason := s.handleSendResult(ctx, tx, submitResult, seq, txStore, maxLedger)
		if accepted {
			ctxLogger.Debugw("tx broadcast successfully", "attempt", currentAttempt, "seq", seq, "hash", submitResult.Hash)
			s.markBroadcastAt(tx)
			s.metrics.IncrementBroadcastedTxs(ctx)
			s.updateTransactionStatus(tx, commontypes.Unconfirmed)
			return
		}

		if fatalErr {
			ctxLogger.Errorw("fatal error during broadcast", "reason", retryReason)
			s.releaseSeqAndFailTx(ctx, txStore, seq, tx, retryReason)
			return
		}

		if retryReason == ErrorReasonBadSeq {
			ctxLogger.Warnw("tx rejected with bad_seq, resyncing and retrying", "attempt", submitAttempt)
			if err := s.resyncSequence(ctx, client, tx); err != nil {
				ctxLogger.Warnw("sequence resync after bad_seq failed, retry may repeat bad_seq",
					"error", err, "submitAttempt", submitAttempt)
			}
			seq = txStore.GetNextSequence()
			submitAttempt++
			continue
		}

		if retryReason == ErrorReasonInsufficientFee {
			var networkP90 uint64
			if _, p90, fsErr := s.feeTracker.sorobanInclusionPercentiles(ctx, client); fsErr == nil {
				networkP90 = p90
			}
			bumped, clampedToMax := s.feeStrat.BumpInclusionFee(inclusionFee, networkP90)
			if clampedToMax {
				ctxLogger.Warnw("bumped inclusion fee clamped to MaxInclusionFee — possible misbehaving RPC",
					"networkPercentile", networkP90,
					"maxInclusionFee", s.feeStrat.MaxInclusionFee,
				)
			}
			ctxLogger.Warnw("tx rejected, bumping inclusion fee and retrying",
				"reason", retryReason, "attempt", submitAttempt, "prevFee", inclusionFee, "newFee", bumped)
			inclusionFee = bumped
			submitAttempt++
			select {
			case <-time.After(s.config.SubmitRetryDelay.Duration()):
			case <-ctx.Done():
				txStore.Release(seq)
				return
			}
			continue
		}

		// Other retryable errors (e.g. try_again_later, tx_internal_error): retry after
		// backoff without bumping fees. TRY_AGAIN_LATER is transient RPC/mempool
		// backpressure, not an insufficient-fee signal.
		ctxLogger.Warnw("tx rejected with retryable error", "reason", retryReason, "attempt", submitAttempt)
		submitAttempt++
		select {
		case <-time.After(s.config.SubmitRetryDelay.Duration()):
		case <-ctx.Done():
			txStore.Release(seq)
			return
		}
	}

	ctxLogger.Errorw("exhausted all submit attempts")
	s.releaseSeqAndFailTx(ctx, txStore, seq, tx, ErrorReasonMaxRetries)
}

func (s *StellarTxm) prepareAndSimulateWithRetry(
	ctx context.Context,
	client RPCClient,
	tx *StellarTx,
	seq int64,
) (*txnbuild.Transaction, protocolrpc.SimulateTransactionResponse, uint32, error) {
	ctxLogger := GetContextedTxLogger(s.baseLogger, tx.ID, tx.Metadata)

	maxAttempts := *s.config.MaxSimulateAttempts
	if maxAttempts == 0 {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := uint(0); attempt < maxAttempts; attempt++ {
		latestLedger, err := client.GetLatestLedger(ctx)
		if err != nil {
			lastErr = fmt.Errorf("failed to get latest ledger: %w", err)
			if attempt+1 >= maxAttempts {
				return nil, protocolrpc.SimulateTransactionResponse{}, 0, lastErr
			}
			ctxLogger.Warnw("latest ledger fetch failed before simulation, retrying", "attempt", attempt, "error", err)
			if !s.sleepBeforeSimulationRetry(ctx) {
				return nil, protocolrpc.SimulateTransactionResponse{}, 0, ctx.Err()
			}
			continue
		}

		offset := *s.config.LedgerBoundsOffset
		if tx.LedgerBoundsOffset > 0 {
			offset = tx.LedgerBoundsOffset
		}
		tx.MaxLedger = latestLedger.Sequence + offset

		prelimTx, err := s.buildPreliminaryTx(tx, seq, tx.MaxLedger)
		if err != nil {
			return nil, protocolrpc.SimulateTransactionResponse{}, 0, fmt.Errorf("failed to build preliminary tx: %w", err)
		}

		simResult, err := s.simulateTransaction(ctx, client, prelimTx)
		if err == nil {
			return prelimTx, simResult, tx.MaxLedger, nil
		}

		lastErr = err
		if !s.shouldRetrySimulation(ctx, err, attempt, maxAttempts) {
			return nil, protocolrpc.SimulateTransactionResponse{}, 0, err
		}
		ctxLogger.Warnw("simulation failed, retrying", "attempt", attempt, "error", err)
		if !s.sleepBeforeSimulationRetry(ctx) {
			return nil, protocolrpc.SimulateTransactionResponse{}, 0, ctx.Err()
		}
	}

	return nil, protocolrpc.SimulateTransactionResponse{}, 0, fmt.Errorf("simulation attempts exhausted: %w", lastErr)
}

func (s *StellarTxm) shouldRetrySimulation(ctx context.Context, err error, attempt uint, maxAttempts uint) bool {
	return attempt+1 < maxAttempts && s.isRetryableSimulationError(ctx, err)
}

func (s *StellarTxm) sleepBeforeSimulationRetry(ctx context.Context) bool {
	select {
	case <-time.After(s.config.SubmitRetryDelay.Duration()):
		return true
	case <-ctx.Done():
		return false
	}
}

// --- Confirm loop ---

func (s *StellarTxm) confirmLoop() {
	defer s.done.Done()

	ctx, cancel := commonutils.ContextFromChan(s.stop)
	defer cancel()

	pollDuration := s.config.ConfirmPollInterval.Duration()
	tick := time.After(pollDuration)

	s.baseLogger.Debugw("confirmLoop: started")

	for {
		select {
		case <-tick:
			start := time.Now()
			s.checkUnconfirmed(ctx)

			remaining := pollDuration - time.Since(start)
			if remaining > 0 {
				tick = time.After(commonutils.WithJitter(remaining))
			} else {
				tick = time.After(0)
			}
		case <-s.stop:
			s.baseLogger.Debugw("confirmLoop: stopped")
			return
		}
	}
}

// checkUnconfirmed polls GetTransaction for all unconfirmed txs and moves them
// to terminal states. On Stellar there are no reorgs: SUCCESS/FAILED is final.
func (s *StellarTxm) checkUnconfirmed(ctx context.Context) {
	client, err := s.getClient(ctx)
	if err != nil {
		s.baseLogger.Errorw("failed to get client for confirm loop", "error", err)
		return
	}

	allUnconfirmed := s.accountStore.GetAllUnconfirmed()
	totalPending := 0

	for accountAddr, unconfirmedTxs := range allUnconfirmed {
		txStore := s.accountStore.GetTxStore(accountAddr)

		for _, utx := range unconfirmedTxs {
			ctxLogger := GetContextedTxLogger(s.baseLogger, utx.Tx.ID, utx.Tx.Metadata)
			hash := utx.Hash

			resp, err := client.GetTransaction(ctx, protocolrpc.GetTransactionRequest{Hash: hash})

			if err == nil {
				switch resp.Status {
				case protocolrpc.TransactionStatusSuccess:
					if confirmErr := txStore.Confirm(utx.Sequence, hash, false); confirmErr != nil {
						ctxLogger.Errorw("failed to confirm tx in TxStore", "hash", hash, "error", confirmErr)
						continue
					}

					// Replace estimated fee with the actual fee charged by the network.
					s.updateTransactionResultXDR(utx.Tx, resp.ResultXDR)
					s.updateTransactionResultCode(utx.Tx, "")
					if resp.ResultXDR != "" {
						var txResult xdr.TransactionResult
						if decodeErr := xdr.SafeUnmarshalBase64(resp.ResultXDR, &txResult); decodeErr != nil {
							ctxLogger.Warnw("failed to decode ResultXDR for fee extraction", "hash", hash, "error", decodeErr)
						} else {
							s.updateTransactionFee(utx.Tx, big.NewInt(int64(txResult.FeeCharged)))
						}
					}

					if resp.ResultMetaXDR != "" {
						s.updateTransactionResultMeta(utx.Tx, resp.ResultMetaXDR)
					}
					s.updateTransactionLedgerCloseTime(utx.Tx, resp.LedgerCloseTime)

					ctxLogger.Infow("confirmed tx: successful", "hash", hash)
					s.metrics.IncrementSuccessTxs(ctx)
					s.recordTimeUntilConfirmed(ctx, utx.Tx)
					s.updateTransactionStatus(utx.Tx, commontypes.Finalized)
					continue

				case protocolrpc.TransactionStatusFailed:
					if confirmErr := txStore.Confirm(utx.Sequence, hash, false); confirmErr != nil {
						ctxLogger.Errorw("failed to confirm failed tx in TxStore", "hash", hash, "error", confirmErr)
						continue
					}
					s.updateTransactionResultXDR(utx.Tx, resp.ResultXDR)
					classification := classifyFailedTransactionResult(resp.ResultXDR)
					s.updateTransactionResultCode(utx.Tx, string(classification.resultCode))
					s.updateTransactionLedgerCloseTime(utx.Tx, resp.LedgerCloseTime)

					ctxLogger.Infow("confirmed tx: failed on-chain", "hash", hash, "resultCode", classification.resultCode, "retryable", classification.retryable)

					if !classification.retryable {
						// Terminal on-chain failure — count once at the point of no-return.
						s.metrics.IncrementErrorTxs(ctx, classification.resultCode)
						s.updateTransactionStatus(utx.Tx, commontypes.Failed)
						continue
					}
					s.incrementTransactionAttempt(utx.Tx)
					if !s.maybeRetry(ctx, utx, RetryReasonResourceExhaustion) {
						// Retryable failure but max attempts exhausted — count once here.
						s.metrics.IncrementMaxAttemptsReached(ctx, RetryBudgetLifecycle)
						s.metrics.IncrementErrorTxs(ctx, classification.resultCode)
						s.updateTransactionStatus(utx.Tx, commontypes.Failed)
					}
					continue
				}
			}

			// NOT_FOUND or transient RPC error: check ledger expiry.
			latestLedger, ledgerErr := client.GetLatestLedger(ctx)

			txTimeout := time.Duration(*s.config.TxTimeoutSecs) * time.Second
			wallClockExpired := time.Since(utx.Tx.Timestamp) > txTimeout

			if ledgerErr != nil {
				ctxLogger.Errorw("couldn't fetch latest ledger for expiry check", "error", ledgerErr)
				if !wallClockExpired {
					totalPending++
					continue
				}
				// Wall-clock expired while ledger check is unavailable — expire the tx.
				ctxLogger.Warnw("tx wall-clock expired while ledger fetch failed, expiring", "hash", hash)
			} else {
				ledgerExpired := latestLedger.Sequence > utx.MaxLedger
				if !ledgerExpired && !wallClockExpired {
					totalPending++
					ctxLogger.Debugw("tx still pending", "hash", hash, "currentLedger", latestLedger.Sequence, "maxLedger", utx.MaxLedger)
					continue
				}
				if wallClockExpired && !ledgerExpired {
					ctxLogger.Warnw("tx expired via wall-clock fallback", "hash", hash,
						"age", time.Since(utx.Tx.Timestamp).Round(time.Second))
				}
			}

			// Expired: confirm as failed, recycle the sequence.
			if confirmErr := txStore.Confirm(utx.Sequence, hash, true); confirmErr != nil {
				ctxLogger.Errorw("couldn't confirm expired tx", "error", confirmErr)
				s.metrics.IncrementErrorTxs(ctx, ErrorReasonTimedOut)
				s.updateTransactionStatus(utx.Tx, commontypes.Failed)
				continue
			}

			s.incrementTransactionAttempt(utx.Tx)
			if !s.maybeRetry(ctx, utx, RetryReasonTimedOut) {
				// Terminal timeout — count once when we know it won't retry.
				s.metrics.IncrementMaxAttemptsReached(ctx, RetryBudgetLifecycle)
				s.metrics.IncrementErrorTxs(ctx, ErrorReasonTimedOut)
				s.updateTransactionStatus(utx.Tx, commontypes.Failed)
			}
		}
	}

	s.metrics.SetPendingTxs(ctx, totalPending)
}

// --- Prune loop ---

// pruneLoop runs on a time.Ticker and evicts terminal transactions whose
// retention window (measured from TerminalTime) has expired. It is started
// only when PruneInterval > 0; when PruneInterval is zero, terminal txs are
// evicted synchronously in updateTransactionStatus instead. An initial prune
// runs immediately on start so stale txs from a restart are not delayed by a
// full tick.
func (s *StellarTxm) pruneLoop() {
	defer s.done.Done()

	ticker := time.NewTicker(s.config.PruneInterval.Duration())
	defer ticker.Stop()

	s.baseLogger.Debugw("pruneLoop: started")
	s.pruneTerminal()
	for {
		select {
		case <-ticker.C:
			s.pruneTerminal()
		case <-s.stop:
			s.baseLogger.Debugw("pruneLoop: stopped")
			return
		}
	}
}

// pruneTerminal scans the transaction map and deletes entries that are
// terminal (Finalized or Failed) and whose TerminalTime is older than
// PruneTxExpiration. In-flight transactions are never touched.
func (s *StellarTxm) pruneTerminal() {
	expiration := s.config.PruneTxExpiration.Duration()

	s.transactionsLock.Lock()
	defer s.transactionsLock.Unlock()

	for id, tx := range s.transactions {
		if !terminalPastRetention(tx, expiration) {
			continue
		}
		age := time.Since(tx.TerminalTime)
		s.baseLogger.Debugw("pruneTerminal: evicting expired terminal tx",
			"txID", id, "status", tx.Status, "terminalAge", age)
		delete(s.transactions, id)
	}
}

// --- Retry ---

func (s *StellarTxm) maybeRetry(ctx context.Context, utx *UnconfirmedTx, reason RetryReason) bool {
	ctxLogger := GetContextedTxLogger(s.baseLogger, utx.Tx.ID, utx.Tx.Metadata)
	currentAttempt := s.getTransactionAttempt(utx.Tx)
	if currentAttempt >= *s.config.MaxTxRetryAttempts {
		ctxLogger.Errorw("tx reached max retries", "hash", utx.Hash, "retryReason", reason)
		return false
	}

	select {
	case s.broadcastChan <- utx.Tx:
		ctxLogger.Debugw("retrying tx", "attempt", currentAttempt, "hash", utx.Hash, "retryReason", reason)
		s.metrics.IncrementRetryTxs(ctx, reason)
		return true
	default:
		ctxLogger.Errorw("failed to enqueue tx for rebroadcast (channel full)", "attempt", currentAttempt, "hash", utx.Hash, "retryReason", reason)
		s.metrics.IncrementDroppedTxs(ctx, DropReasonChannelFullNewRejected)
		return false
	}
}

// --- Simulate (read-only) ---

// Simulate performs a read-only simulation without consuming a sequence number
// or broadcasting. Callers receive the raw SimulateTransactionResponse so they
// can inspect resource usage, auth entries, and return values. This is the
// entry point for InvokerAdapter.SimulateContract and other read-only queries.
func (s *StellarTxm) Simulate(ctx context.Context, req TxRequest) (protocolrpc.SimulateTransactionResponse, error) {
	if len(req.Operations) == 0 {
		return protocolrpc.SimulateTransactionResponse{}, errors.New("Simulate: at least one operation is required")
	}

	fromAddr := req.FromAddress
	if fromAddr == "" {
		var err error
		fromAddr, err = s.defaultFromAddress(ctx)
		if err != nil {
			return protocolrpc.SimulateTransactionResponse{}, fmt.Errorf("Simulate: %w", err)
		}
	}

	client, err := s.getClient(ctx)
	if err != nil {
		return protocolrpc.SimulateTransactionResponse{}, fmt.Errorf("Simulate: failed to get client: %w", err)
	}

	latestLedger, err := client.GetLatestLedger(ctx)
	if err != nil {
		return protocolrpc.SimulateTransactionResponse{}, fmt.Errorf("Simulate: failed to get latest ledger: %w", err)
	}

	maxLedger := latestLedger.Sequence + *s.config.LedgerBoundsOffset

	// Sequence 0 is valid for simulation — the network never commits it.
	dummyTx := &StellarTx{
		FromAddress: fromAddr,
		Operations:  req.Operations,
	}
	prelimTx, err := s.buildPreliminaryTx(dummyTx, 0, maxLedger)
	if err != nil {
		return protocolrpc.SimulateTransactionResponse{}, fmt.Errorf("Simulate: failed to build tx: %w", err)
	}

	return s.simulateTransaction(ctx, client, prelimTx)
}

func (s *StellarTxm) defaultFromAddress(ctx context.Context) (string, error) {
	accounts, err := s.keystore.Accounts(ctx)
	if err != nil {
		return "", fmt.Errorf("keystore.Accounts: %w", err)
	}
	if len(accounts) == 0 {
		return "", errors.New("keystore has no accounts")
	}
	return accounts[0], nil
}

// --- Sequence helpers ---

// getSequenceNumber fetches the on-chain sequence number for a Stellar account via RPC.
// Returns the LAST USED sequence (the caller must add +1 for the next expected).
func (s *StellarTxm) getSequenceNumber(ctx context.Context, client RPCClient, address string) (int64, error) {
	if address == "" {
		return 0, errors.New("address is required for sequence number lookup")
	}
	accountID, err := xdr.AddressToAccountId(address)
	if err != nil {
		return 0, fmt.Errorf("invalid stellar account address %q: %w", address, err)
	}
	accountKey := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{
			AccountId: accountID,
		},
	}

	keyXDR, err := accountKey.MarshalBinaryBase64()
	if err != nil {
		return 0, fmt.Errorf("failed to marshal account key: %w", err)
	}

	resp, err := client.GetLedgerEntries(ctx, protocolrpc.GetLedgerEntriesRequest{
		Keys: []string{keyXDR},
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get ledger entries: %w", err)
	}

	if len(resp.Entries) == 0 {
		return 0, fmt.Errorf("account %s not found on-chain", address)
	}

	entryXDR := resp.Entries[0].DataXDR
	if entryXDR == "" {
		return 0, fmt.Errorf("empty entry data for account %s", address)
	}

	var entry xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(entryXDR, &entry); err != nil {
		return 0, fmt.Errorf("failed to unmarshal account entry: %w", err)
	}

	account, ok := entry.GetAccount()
	if !ok {
		return 0, fmt.Errorf("ledger entry for %s is not an account entry (type=%v)", address, entry.Type)
	}
	return int64(account.SeqNum), nil
}

func (s *StellarTxm) resyncSequence(ctx context.Context, client RPCClient, tx *StellarTx) error {
	seqNum, err := s.getSequenceNumber(ctx, client, tx.FromAddress)
	if err != nil {
		return fmt.Errorf("failed to resync sequence for %s: %w", tx.FromAddress, err)
	}

	txStore := s.accountStore.GetTxStore(tx.FromAddress)
	if txStore == nil {
		return fmt.Errorf("no tx store for address %s", tx.FromAddress)
	}

	ctxLogger := GetContextedTxLogger(s.baseLogger, tx.ID, tx.Metadata)

	prevNext := txStore.GetNextSequence()
	prevOnchain := txStore.GetLastResyncedNonce()
	txStore.ResyncNonce(seqNum + 1) // +1: Stellar on-chain seq is LAST USED
	updatedNext := txStore.GetNextSequence()
	updatedOnchain := txStore.GetLastResyncedNonce()

	ctxLogger.Infow("resynced sequence",
		"address", tx.FromAddress,
		"onchainSeq", seqNum,
		"prevNext", prevNext, "updatedNext", updatedNext,
		"prevOnchain", prevOnchain, "updatedOnchain", updatedOnchain,
	)
	return nil
}

// retryGetClientOrFail handles a getClient (multinode RPC selection) failure
// separately from the tx lifecycle retry budget (MaxTxRetryAttempts). It
// increments a dedicated InfraAttempts counter and re-enqueues the tx for
// another broadcast attempt after SubmitRetryDelay backoff. Only when
// InfraAttempts reaches MaxGetClientRetryAttempts does the tx fail with
// ErrorReasonClientUnavailable — a genuinely dead RPC pool, not a transient
// blip. This prevents infra outages from stealing retries reserved for
// legitimate post-submit lifecycle failures (RetryReasonTimedOut,
// RetryReasonResourceExhaustion).
func (s *StellarTxm) retryGetClientOrFail(ctx context.Context, tx *StellarTx, ctxLogger logger.Logger) {
    s.incrementInfraAttempts(tx)
    attempts := s.getInfraAttempts(tx)

    maxRetries := uint64(0)
    if s.config.MaxGetClientRetryAttempts != nil {
        maxRetries = *s.config.MaxGetClientRetryAttempts
    }

    if attempts < maxRetries {
        select {
        case <-time.After(s.config.SubmitRetryDelay.Duration()):
        case <-ctx.Done():
            return
        }
        select {
        case s.broadcastChan <- tx:
            ctxLogger.Debugw("re-enqueuing tx after getClient failure",
                "infraAttempts", attempts, "maxGetClientRetries", maxRetries)
        default:
            ctxLogger.Errorw("broadcast channel full, cannot re-enqueue after getClient failure",
                "infraAttempts", attempts)
            s.metrics.IncrementDroppedTxs(ctx, DropReasonChannelFullNewRejected)
        }
        return
    }

    ctxLogger.Errorw("getClient retries exhausted, failing tx",
        "infraAttempts", attempts, "maxGetClientRetries", maxRetries)
    s.metrics.IncrementMaxAttemptsReached(ctx, RetryBudgetInfra)
    s.markTxFailed(ctx, tx, ErrorReasonClientUnavailable)
}
