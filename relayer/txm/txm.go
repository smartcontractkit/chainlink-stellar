package txm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	"github.com/smartcontractkit/chainlink-common/pkg/loop"
	"github.com/smartcontractkit/chainlink-common/pkg/services"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	commonutils "github.com/smartcontractkit/chainlink-common/pkg/utils"

	"github.com/smartcontractkit/chainlink-stellar/relayer/client"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

var _ services.Service = &StellarTxm{}

// StellarTxm orchestrates the lifecycle of Stellar/Soroban transactions:
// enqueue → simulate → (restore) → assemble → sign → send → confirm.
type StellarTxm struct {
	baseLogger logger.Logger
	keystore   loop.Keystore
	config     Config
	chainID    string
	metrics    *stellarTxmMetrics
	feeStrat   FeeStrategy

	transactions              map[string]*StellarTx
	transactionsLock          sync.RWMutex
	transactionsLastPruneTime uint64

	broadcastChan chan string
	accountStore  *AccountStore
	starter       commonutils.StartStopOnce
	done          sync.WaitGroup
	stop          chan struct{}

	getClient         func() (*client.Client, error)
	networkPassphrase string
}

// New creates a StellarTxm. The getClient callback should be obtained from
// ClientFactory.GetClient to enable multi-node rotation.
func New(
	lgr logger.Logger,
	keystore loop.Keystore,
	cfg Config,
	getClient func() (*client.Client, error),
	chainID string,
	networkPassphrase string,
) (*StellarTxm, error) {
	cfg.Resolve()

	metrics, err := newStellarTxmMetrics(chainID)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize metrics: %w", err)
	}

	return &StellarTxm{
		baseLogger: logger.Named(lgr, "StellarTxm"),
		keystore:   keystore,
		config:     cfg,
		chainID:    chainID,
		metrics:    metrics,
		feeStrat:   NewFeeStrategyFromConfig(cfg),

		transactions:              make(map[string]*StellarTx),
		transactionsLastPruneTime: getTimestampSecs(),

		broadcastChan:     make(chan string, *cfg.BroadcastChanSize),
		accountStore:      NewAccountStore(),
		stop:              make(chan struct{}),
		getClient:         getClient,
		networkPassphrase: networkPassphrase,
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
		s.done.Add(2)
		go s.broadcastLoop()
		go s.confirmLoop()
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
func (s *StellarTxm) Enqueue(ctx context.Context, req TxRequest) (string, error) {
	txID := req.ID
	if txID == "" {
		txID = uuid.New().String()
	} else {
		s.transactionsLock.RLock()
		_, exists := s.transactions[txID]
		s.transactionsLock.RUnlock()
		if exists {
			return "", errors.New("transaction already exists")
		}
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
		Timestamp:          getTimestampSecs(),
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
		ID:            tx.ID,
		Hash:          tx.TxHash,
		Status:        tx.Status,
		Fee:           tx.Fee,
		ResultXDR:     tx.ResultXDR,
		ResultMetaXDR: tx.ResultMetaXDR,
	}
	if tx.ResultCode != "" {
		result.Error = fmt.Errorf("transaction result: %s", tx.ResultCode)
	}
	return result
}

// enqueueTransaction handles pruning, stores the tx, and pushes its ID to broadcastChan.
// On backpressure it drops the oldest queued tx (not the new one): the oldest has
// the stalest simulation data and the nearest LedgerBounds expiry, so the newer tx's
// intent takes priority.
func (s *StellarTxm) enqueueTransaction(ctx context.Context, tx *StellarTx) (string, error) {
	ctxLogger := GetContextedTxLogger(s.baseLogger, tx.ID, nil)

	s.transactionsLock.Lock()
	now := tx.Timestamp
	pruneIntervalSecs := uint64(s.config.PruneInterval.Duration().Seconds())
	if (now - s.transactionsLastPruneTime) > pruneIntervalSecs {
		pruneCutoff := uint64(s.config.PruneTxExpiration.Duration().Seconds())
		for id, existing := range s.transactions {
			if existing.Status != commontypes.Finalized && existing.Status != commontypes.Failed {
				continue
			}
			if (now - existing.Timestamp) < pruneCutoff {
				continue
			}
			ctxLogger.Debugw("Pruning transaction", "prunedTxID", id, "status", existing.Status)
			delete(s.transactions, id)
		}
		s.transactionsLastPruneTime = now
	}
	s.transactions[tx.ID] = tx
	s.transactionsLock.Unlock()

	// Fast path: channel has space.
	select {
	case s.broadcastChan <- tx.ID:
		ctxLogger.Debugw("tx enqueued", "fromAddr", tx.FromAddress, "txID", tx.ID)
		return tx.ID, nil
	default:
	}

	// Slow path: channel full. Drain the oldest queued tx (FIFO head) and
	// mark it dropped, then send the new one into the freed slot.
	var droppedID string
	select {
	case droppedID = <-s.broadcastChan:
	default:
		// Channel became non-full between the fast-path check and now
		// (broadcastLoop drained it). Proceed directly to the send retry.
	}
	if droppedID != "" {
		s.dropOldestForBackpressure(ctx, droppedID)
	}

	select {
	case s.broadcastChan <- tx.ID:
		ctxLogger.Debugw("tx enqueued after evicting oldest", "droppedID", droppedID, "txID", tx.ID)
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
func (s *StellarTxm) dropOldestForBackpressure(ctx context.Context, txID string) {
	s.transactionsLock.Lock()
	tx, ok := s.transactions[txID]
	if ok {
		tx.Status = commontypes.Failed
		tx.ResultCode = DropReasonChannelFullOldestEvicted
	}
	s.transactionsLock.Unlock()
	if !ok {
		return
	}

	s.closeDone(tx)
	s.metrics.IncrementDroppedTxs(ctx, DropReasonChannelFullOldestEvicted)

	ctxLogger := GetContextedTxLogger(s.baseLogger, tx.ID, tx.Metadata)
	ctxLogger.Warnw("oldest queued tx evicted due to channel backpressure",
		"droppedTxID", tx.ID,
		"ageSecs", uint64(time.Now().Unix())-tx.Timestamp)
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

// InflightCount returns (broadcastChan length, total unconfirmed across all accounts).
func (s *StellarTxm) InflightCount() (int, int) {
	return len(s.broadcastChan), s.accountStore.GetTotalInflightCount()
}

// --- Transaction status helpers ---

func (s *StellarTxm) updateTransactionStatus(tx *StellarTx, status commontypes.TransactionStatus) {
	s.transactionsLock.Lock()
	defer s.transactionsLock.Unlock()
	tx.Status = status
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

// closeDone closes the transaction's Done channel to unblock EnqueueAndWait callers.
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
		case initialID := <-s.broadcastChan:
			broadcastIDs := []string{initialID}
		DrainChannel:
			for {
				select {
				case nextID := <-s.broadcastChan:
					broadcastIDs = append(broadcastIDs, nextID)
				default:
					break DrainChannel
				}
			}

			s.transactionsLock.RLock()
			broadcastTxs := make([]*StellarTx, 0, len(broadcastIDs))
			for _, txID := range broadcastIDs {
				tx, ok := s.transactions[txID]
				if !ok {
					s.baseLogger.Errorw("failed to find tx", "txID", txID)
					continue
				}
				broadcastTxs = append(broadcastTxs, tx)
			}
			s.transactionsLock.RUnlock()

			sort.Slice(broadcastTxs, func(i, j int) bool {
				return broadcastTxs[i].Timestamp < broadcastTxs[j].Timestamp
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

// simulateAssembleSignAndSend is the Stellar-specific broadcast pipeline.
// Phase 7 will implement the full pipeline; this is the orchestration skeleton.
func (s *StellarTxm) simulateAssembleSignAndSend(ctx context.Context, tx *StellarTx) {
	ctxLogger := GetContextedTxLogger(s.baseLogger, tx.ID, tx.Metadata)
	client, err := s.getClient()
	if err != nil {
		ctxLogger.Errorw("failed to get RPC client", "error", err)
		s.incrementTransactionAttempt(tx)
		if s.getTransactionAttempt(tx) < *s.config.MaxTxRetryAttempts {
			select {
			case <-time.After(s.config.SubmitRetryDelay.Duration()):
			case <-ctx.Done():
				return
			}
		}
		if !s.maybeRetry(ctx, &UnconfirmedTx{Tx: tx}, RetryReasonClientUnavailable) {
			s.updateTransactionStatus(tx, commontypes.Failed)
			s.closeDone(tx)
			s.metrics.IncrementErrorTxs(ctx, ErrorReasonClientUnavailable)
		}
		return
	}

	txStore := s.accountStore.GetTxStore(tx.FromAddress)
	if txStore == nil {
		seqNum, err := s.getSequenceNumber(ctx, client, tx.FromAddress)
		if err != nil {
			ctxLogger.Errorw("failed to get sequence number", "fromAddress", tx.FromAddress, "error", err)
			s.updateTransactionStatus(tx, commontypes.Failed)
			s.closeDone(tx)
			s.metrics.IncrementErrorTxs(ctx, ErrorReasonSequenceNumber)
			return
		}
		newStore, err := s.accountStore.CreateTxStore(tx.FromAddress, seqNum+1)
		if err != nil {
			ctxLogger.Errorw("failed to create tx store", "fromAddress", tx.FromAddress, "error", err)
			s.updateTransactionStatus(tx, commontypes.Failed)
			s.closeDone(tx)
			s.metrics.IncrementErrorTxs(ctx, ErrorReasonStoreCreate)
			return
		}
		txStore = newStore
	}

	currentAttempt := s.getTransactionAttempt(tx)
	if currentAttempt > 0 {
		_ = s.resyncSequence(ctx, client, tx)
	}

	// Seed inclusion fee from live network data
	// SeedInclusionFee caps the result at MaxInclusionFee
	var networkPercentile uint64
	if feeStats, fsErr := client.GetFeeStats(ctx); fsErr == nil {
		if currentAttempt > 0 {
			networkPercentile = feeStats.SorobanInclusionFee.P99
		} else {
			networkPercentile = feeStats.SorobanInclusionFee.P50
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
			txStore.Release(seq)
			s.updateTransactionStatus(tx, commontypes.Failed)
			s.closeDone(tx)
			s.metrics.IncrementErrorTxs(ctx, ErrorReasonSimulation)
			return
		}

		if simResult.RestorePreamble != nil {
			if restoreHandled {
				ctxLogger.Errorw("restore still required after RestoreFootprint transaction")
				txStore.Release(seq)
				s.updateTransactionStatus(tx, commontypes.Failed)
				s.closeDone(tx)
				s.metrics.IncrementErrorTxs(ctx, ErrorReasonRestoreFailed)
				return
			}
			if err := s.handleRestore(ctx, client, tx, *simResult.RestorePreamble, seq); err != nil {
				ctxLogger.Errorw("failed to restore archived ledger entries", "error", err)
				txStore.Release(seq)
				s.updateTransactionStatus(tx, commontypes.Failed)
				s.closeDone(tx)
				s.metrics.IncrementErrorTxs(ctx, ErrorReasonRestoreFailed)
				return
			}
			restoreHandled = true
			seq = txStore.GetNextSequence()
			continue
		}

		tx.MinResourceFee = simResult.MinResourceFee

		assembledTx, totalFee, err := s.assembleTransaction(prelimTx, simResult, inclusionFee, maxLedger)
		if err != nil {
			ctxLogger.Errorw("failed to assemble transaction", "error", err)
			txStore.Release(seq)
			s.updateTransactionStatus(tx, commontypes.Failed)
			s.closeDone(tx)
			s.metrics.IncrementErrorTxs(ctx, ErrorReasonAssembly)
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
			txStore.Release(seq)
			s.updateTransactionStatus(tx, commontypes.Failed)
			s.closeDone(tx)
			s.metrics.IncrementErrorTxs(ctx, ErrorReasonSigning)
			return
		}

		signedXDR, err := signedTx.Base64()
		if err != nil {
			ctxLogger.Errorw("failed to encode signed transaction", "error", err)
			txStore.Release(seq)
			s.updateTransactionStatus(tx, commontypes.Failed)
			s.closeDone(tx)
			s.metrics.IncrementErrorTxs(ctx, ErrorReasonAssembly)
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
			s.updateTransactionStatus(tx, commontypes.Unconfirmed)
			s.metrics.IncrementBroadcastedTxs(ctx)
			return
		}

		if fatalErr {
			ctxLogger.Errorw("fatal error during broadcast", "reason", retryReason)
			txStore.Release(seq)
			s.updateTransactionStatus(tx, commontypes.Failed)
			s.closeDone(tx)
			s.metrics.IncrementErrorTxs(ctx, retryReason)
			return
		}

		if retryReason == ErrorReasonBadSeq {
			ctxLogger.Warnw("tx rejected with bad_seq, resyncing and retrying", "attempt", submitAttempt)
			_ = s.resyncSequence(ctx, client, tx)
			seq = txStore.GetNextSequence()
			submitAttempt++
			continue
		}

		if retryReason == ErrorReasonTryAgainLater || retryReason == ErrorReasonInsufficientFee {
			// Bump inclusion fee: apply multiplier then take max with live P90.
			// This mirrors Aptos using PrioritizedGasEstimate on retry — we jump
			// to the current clearing price instead of climbing blindly.
			bumped := int64(math.Ceil(float64(inclusionFee) * s.feeStrat.BumpMultiplier))
			if feeStats, fsErr := client.GetFeeStats(ctx); fsErr == nil {
				if networkFee := int64(feeStats.SorobanInclusionFee.P90); networkFee > bumped {
					bumped = networkFee
				}
			}
			if bumped > s.feeStrat.MaxInclusionFee {
				bumped = s.feeStrat.MaxInclusionFee
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

		// Other retryable errors
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
	txStore.Release(seq)
	s.updateTransactionStatus(tx, commontypes.Failed)
	s.closeDone(tx)
	s.metrics.IncrementErrorTxs(ctx, ErrorReasonMaxRetries)
}

func (s *StellarTxm) prepareAndSimulateWithRetry(
	ctx context.Context,
	client *client.Client,
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
		latestLedger, err := client.LatestLedger(ctx)
		if err != nil {
			lastErr = fmt.Errorf("failed to get latest ledger: %w", err)
			if !s.shouldRetrySimulation(ctx, lastErr, attempt, maxAttempts) {
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
		maxLedger := latestLedger.Sequence + offset
		tx.MaxLedger = maxLedger

		prelimTx, err := s.buildPreliminaryTx(tx, seq, maxLedger)
		if err != nil {
			return nil, protocolrpc.SimulateTransactionResponse{}, 0, fmt.Errorf("failed to build preliminary tx: %w", err)
		}

		simResult, err := s.simulateTransaction(ctx, client, prelimTx)
		if err == nil {
			return prelimTx, simResult, maxLedger, nil
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
	return attempt+1 < maxAttempts && isRetryableSimulationError(ctx, err)
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
	client, err := s.getClient()
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

					ctxLogger.Infow("confirmed tx: successful", "hash", hash)
					s.metrics.IncrementSuccessTxs(ctx)
					s.updateTransactionStatus(utx.Tx, commontypes.Finalized)
					s.metrics.IncrementFinalizedTxs(ctx)
					s.closeDone(utx.Tx)
					continue

				case protocolrpc.TransactionStatusFailed:
					if confirmErr := txStore.Confirm(utx.Sequence, hash, false); confirmErr != nil {
						ctxLogger.Errorw("failed to confirm failed tx in TxStore", "hash", hash, "error", confirmErr)
					}
					s.updateTransactionResultXDR(utx.Tx, resp.ResultXDR)
					classification := classifyFailedTransactionResult(resp.ResultXDR)
					s.updateTransactionResultCode(utx.Tx, classification.resultCode)

					ctxLogger.Infow("confirmed tx: failed on-chain", "hash", hash, "resultCode", classification.resultCode, "retryable", classification.retryable)
					s.metrics.IncrementErrorTxs(ctx, classification.resultCode)

					if !classification.retryable {
						s.updateTransactionStatus(utx.Tx, commontypes.Failed)
						s.closeDone(utx.Tx)
						continue
					}
					s.incrementTransactionAttempt(utx.Tx)
					if !s.maybeRetry(ctx, utx, RetryReasonResourceExhaustion) {
						s.updateTransactionStatus(utx.Tx, commontypes.Failed)
						s.closeDone(utx.Tx)
					}
					continue
				}
			}

			// NOT_FOUND or transient RPC error: check ledger expiry.
			latestLedger, ledgerErr := client.LatestLedger(ctx)

			txTimeout := time.Duration(*s.config.TxTimeoutSecs) * time.Second
			wallClockExpired := time.Since(time.Unix(int64(utx.Tx.Timestamp), 0)) > txTimeout

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
						"age", time.Since(time.Unix(int64(utx.Tx.Timestamp), 0)).Round(time.Second))
				}
			}

			// Expired: confirm as failed, recycle the sequence.
			if confirmErr := txStore.Confirm(utx.Sequence, hash, true); confirmErr != nil {
				ctxLogger.Errorw("couldn't confirm expired tx", "error", confirmErr)
				s.updateTransactionStatus(utx.Tx, commontypes.Failed)
				s.closeDone(utx.Tx)
				s.metrics.IncrementErrorTxs(ctx, ErrorReasonTimedOut)
				continue
			}

			s.metrics.IncrementErrorTxs(ctx, ErrorReasonTimedOut)
			s.incrementTransactionAttempt(utx.Tx)
			if !s.maybeRetry(ctx, utx, RetryReasonTimedOut) {
				s.updateTransactionStatus(utx.Tx, commontypes.Failed)
				s.closeDone(utx.Tx)
			}
		}
	}

	s.metrics.SetPendingTxs(ctx, totalPending)
}

// --- Retry ---

func (r RetryReason) String() string {
	switch r {
	case RetryReasonResourceExhaustion:
		return "resource_exhaustion"
	case RetryReasonTimedOut:
		return "timed_out"
	case RetryReasonBadSeq:
		return "bad_seq"
	case RetryReasonTryAgainLater:
		return "try_again_later"
	case RetryReasonClientUnavailable:
		return "client_unavailable"
	default:
		return "unknown"
	}
}

func (s *StellarTxm) maybeRetry(ctx context.Context, utx *UnconfirmedTx, reason RetryReason) bool {
	ctxLogger := GetContextedTxLogger(s.baseLogger, utx.Tx.ID, utx.Tx.Metadata)
	currentAttempt := s.getTransactionAttempt(utx.Tx)
	if currentAttempt >= *s.config.MaxTxRetryAttempts {
		ctxLogger.Errorw("tx reached max retries", "hash", utx.Hash, "retryReason", reason)
		return false
	}

	select {
	case s.broadcastChan <- utx.Tx.ID:
		ctxLogger.Debugw("retrying tx", "attempt", currentAttempt, "hash", utx.Hash, "retryReason", reason)
		s.metrics.IncrementRetryTxs(ctx, reason.String())
		return true
	default:
		ctxLogger.Errorw("failed to enqueue tx for rebroadcast (channel full)", "attempt", currentAttempt, "hash", utx.Hash, "retryReason", reason)
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

	client, err := s.getClient()
	if err != nil {
		return protocolrpc.SimulateTransactionResponse{}, fmt.Errorf("Simulate: failed to get client: %w", err)
	}

	latestLedger, err := client.LatestLedger(ctx)
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

// getSequenceNumber fetches the on-chain sequence number for a Stellar account.
// Returns the LAST USED sequence (the caller must add +1 for the next expected).
func (s *StellarTxm) getSequenceNumber(ctx context.Context, client *client.Client, address string) (int64, error) {
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

func (s *StellarTxm) resyncSequence(ctx context.Context, client *client.Client, tx *StellarTx) error {
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
