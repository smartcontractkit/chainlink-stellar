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

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	"github.com/smartcontractkit/chainlink-common/pkg/loop"
	"github.com/smartcontractkit/chainlink-common/pkg/services"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	commonutils "github.com/smartcontractkit/chainlink-common/pkg/utils"

	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
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

	broadcastChan     chan string
	accountStore      *AccountStore
	starter           commonutils.StartStopOnce
	done              sync.WaitGroup
	stop              chan struct{}

	getClient         func() (*ccvclient.Client, error)
	networkPassphrase string
}

// New creates a StellarTxm. The getClient callback should be obtained from
// ClientFactory.GetClient to enable multi-node rotation.
func New(
	lgr logger.Logger,
	keystore loop.Keystore,
	cfg Config,
	getClient func() (*ccvclient.Client, error),
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
func (s *StellarTxm) Enqueue(_ context.Context, req TxRequest) (string, error) {
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

	if req.ContractID == "" {
		return "", errors.New("ContractID is required")
	}
	if req.FunctionName == "" {
		return "", errors.New("FunctionName is required")
	}

	tx := &StellarTx{
		ID:           txID,
		Timestamp:    getTimestampSecs(),
		FromAddress:  req.FromAddress,
		ContractID:   req.ContractID,
		FunctionName: req.FunctionName,
		Args:         req.Args,
		Status:       commontypes.Pending,
		Done:         make(chan struct{}),
	}

	return s.enqueueTransaction(tx)
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

	result := &TxResult{
		ID:     tx.ID,
		Hash:   tx.TxHash,
		Status: tx.Status,
		Fee:    tx.Fee,
	}
	if tx.ResultCode != "" {
		result.Error = fmt.Errorf("transaction result: %s", tx.ResultCode)
	}
	return result
}

// enqueueTransaction handles pruning, stores the tx, and pushes its ID to broadcastChan.
func (s *StellarTxm) enqueueTransaction(tx *StellarTx) (string, error) {
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

	select {
	case s.broadcastChan <- tx.ID:
		ctxLogger.Debugw("tx enqueued", "fromAddr", tx.FromAddress, "txID", tx.ID)
	default:
		s.transactionsLock.Lock()
		delete(s.transactions, tx.ID)
		s.transactionsLock.Unlock()
		ctxLogger.Errorw("broadcast channel full, tx dropped", "txID", tx.ID)
		return "", fmt.Errorf("broadcast channel full, tx %s dropped", tx.ID)
	}

	return tx.ID, nil
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

func (s *StellarTxm) updateTransactionResultCode(tx *StellarTx, code string) {
	s.transactionsLock.Lock()
	defer s.transactionsLock.Unlock()
	tx.ResultCode = code
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
	s.transactionsLock.RLock()
	defer s.transactionsLock.RUnlock()
	select {
	case <-tx.Done:
	default:
		close(tx.Done)
	}
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

	// The full simulate → assemble → sign → send pipeline will be implemented
	// in Phase 7 (broadcast.go). For now, mark the tx as Unconfirmed with a
	// placeholder to validate the lifecycle wiring.
	seq := txStore.GetNextSequence()
	hash := fmt.Sprintf("placeholder_%s_%d", tx.ID, seq)

	s.updateTransactionHash(tx, hash)

	latestLedger, err := client.LatestLedger(ctx)
	if err != nil {
		ctxLogger.Errorw("failed to get latest ledger", "error", err)
		s.updateTransactionStatus(tx, commontypes.Failed)
		s.closeDone(tx)
		s.metrics.IncrementErrorTxs(ctx, ErrorReasonSimulation)
		return
	}

	maxLedger := latestLedger.Sequence + *s.config.LedgerBoundsOffset
	err = txStore.AddUnconfirmed(seq, hash, maxLedger, tx)
	if err != nil {
		ctxLogger.Errorw("failed to add unconfirmed tx", "error", err)
		s.updateTransactionStatus(tx, commontypes.Failed)
		s.closeDone(tx)
		s.metrics.IncrementErrorTxs(ctx, ErrorReasonStoreAdd)
		return
	}

	s.updateTransactionStatus(tx, commontypes.Unconfirmed)
	s.metrics.IncrementBroadcastedTxs(ctx)
	ctxLogger.Debugw("tx broadcast (placeholder)", "attempt", currentAttempt, "seq", seq, "hash", hash)
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

					ctxLogger.Infow("confirmed tx: successful", "hash", hash)
					s.metrics.IncrementSuccessTxs(ctx)
					s.updateTransactionStatus(utx.Tx, commontypes.Finalized)
					s.metrics.IncrementFinalizedTxs(ctx)
					s.closeDone(utx.Tx)
					continue

				case protocolrpc.TransactionStatusFailed:
					if confirmErr := txStore.Confirm(utx.Sequence, hash, true); confirmErr != nil {
						ctxLogger.Errorw("failed to confirm failed tx in TxStore", "hash", hash, "error", confirmErr)
					}
					s.updateTransactionResultCode(utx.Tx, resp.Status)

					ctxLogger.Infow("confirmed tx: failed on-chain", "hash", hash)
					s.metrics.IncrementErrorTxs(ctx, ErrorReasonRevert)

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
			if ledgerErr != nil {
				ctxLogger.Errorw("couldn't fetch latest ledger for expiry check", "error", ledgerErr)
				totalPending++
				continue
			}

			if latestLedger.Sequence <= utx.MaxLedger {
				totalPending++
				ctxLogger.Debugw("tx still pending", "hash", hash, "currentLedger", latestLedger.Sequence, "maxLedger", utx.MaxLedger)
				continue
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

// --- Sequence helpers ---

// getSequenceNumber fetches the on-chain sequence number for a Stellar account.
// Returns the LAST USED sequence (the caller must add +1 for the next expected).
func (s *StellarTxm) getSequenceNumber(ctx context.Context, client *ccvclient.Client, address string) (int64, error) {
	accountKey := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{
			AccountId: xdr.MustAddress(address),
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

	account := entry.MustAccount()
	return int64(account.SeqNum), nil
}

func (s *StellarTxm) resyncSequence(ctx context.Context, client *ccvclient.Client, tx *StellarTx) error {
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
