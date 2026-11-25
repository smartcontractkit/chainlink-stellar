package destinationreader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-common/pkg/services"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	eventsfetcher "github.com/smartcontractkit/chainlink-stellar/ccv/common/events_fetcher"
)

const (
	defaultStellarPollInterval  = 5 * time.Second
	defaultStellarRetryInterval = 7 * time.Second
	// defaultStartupRetryInterval spaces out attempts to compute the start
	// ledger while the RPC is unreachable at startup.
	defaultStartupRetryInterval = 5 * time.Second
	// defaultRetryQueueLimit for the max number of retriable fetches held in the queue
	defaultRetryQueueLimit        = 64
	defaultAttemptCacheQueueLimit = 256
	// maxEventProcessRetries for the limit of event details fetch retry
	maxEventProcessRetries = 3
	// Stellar ledgers close approximately every 5 seconds. Underestimating
	// the close time only widens the backfill, which is the safe direction.
	estimatedLedgerCloseTime = 5 * time.Second
	minStellarLedger         = uint32(7)
	// retentionMargin keeps a start ledger clear of the RPC's oldest retained
	// ledger, which keeps advancing while requests are in flight.
	retentionMargin          = uint32(10)
	expectedExecuteArgCount  = 4
	stellarPollerServiceName = "stellar.executionattemptpoller.Service"
	// specifies the cap of how many attempts to hold for a given message ID
	defaultLastNAttemptsCache = 10
)

// ExecutionAttemptRPC is the subset of the Stellar RPC client used by the
// execution attempt poller. *rpcclient.Client satisfies it.
type ExecutionAttemptRPC interface {
	eventsfetcher.Client
	GetLatestLedger(ctx context.Context) (protocolrpc.GetLatestLedgerResponse, error)
	GetHealth(ctx context.Context) (protocolrpc.GetHealthResponse, error)
	GetTransaction(ctx context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error)
}

var _ ExecutionAttemptRPC = (*rpcclient.Client)(nil)

// retryTask is a queue entry used for refetching an event's transaction
// details necessary for handling intermittent RPC failures
type retryTask struct {
	event   *ExecutionStateChangedEvent
	retries int
}

// cachedAttempt is an execution attempt together with the transaction that
// made it, so an attempt seen twice (e.g. re-polled or retried) is cached once.
type cachedAttempt struct {
	txHash  string
	attempt protocol.ExecutionAttempt
}

// StellarExecutionAttemptPoller polls for ExecutionStateChanged events on the Stellar
// offramp contract and caches the decoded execution attempts by message ID.
type StellarExecutionAttemptPoller struct {
	services.StateMachine

	lggr              *zerolog.Logger
	rpc               ExecutionAttemptRPC
	offrampContractID string
	filters           []protocolrpc.EventFilter
	// attemptCacheMu is needed to avoid data races between the retry logic
	// and the regular polling loop when accessing `attemptCache`
	attemptCacheMu       sync.Mutex
	attemptCache         *expirable.LRU[protocol.Bytes32, []cachedAttempt]
	stopCh               services.StopChan
	wg                   sync.WaitGroup
	pollInterval         time.Duration
	retryInterval        time.Duration
	startupRetryInterval time.Duration
	retryQueue           *expirable.LRU[string, retryTask]
	lookbackWindow       time.Duration

	// lastPolledLedger is the last ledger whose events have been fetched.
	// Only the polling goroutine reads or writes it.
	lastPolledLedger uint32
	// backfilled is set once the first poll over the lookback window succeeds.
	backfilled atomic.Bool

	healthMu sync.Mutex
	// healthErr reports ledgers skipped because they left RPC retention
	// before being polled; it clears once healthErrUntil passes, i.e. when
	// the skipped ledgers are older than the lookback window anyway.
	healthErr      error
	healthErrUntil time.Time
}

// NewStellarExecutionAttemptPoller creates a new execution attempt poller for the Stellar offramp.
// Once started, the poller backfills the lookback window in the background,
// then continues polling for new events at the configured interval.
func NewStellarExecutionAttemptPoller(
	rpcClient *rpcclient.Client,
	offrampContractID string,
	lggr *zerolog.Logger,
	attemptCacheExpiration time.Duration,
) (*StellarExecutionAttemptPoller, error) {
	if rpcClient == nil {
		return nil, fmt.Errorf("rpc client cannot be nil")
	}
	return newExecutionAttemptPoller(rpcClient, offrampContractID, lggr, attemptCacheExpiration)
}

func newExecutionAttemptPoller(
	rpc ExecutionAttemptRPC,
	offrampContractID string,
	lggr *zerolog.Logger,
	attemptCacheExpiration time.Duration,
) (*StellarExecutionAttemptPoller, error) {
	if rpc == nil {
		return nil, fmt.Errorf("rpc client cannot be nil")
	}
	if lggr == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}
	if offrampContractID == "" {
		return nil, fmt.Errorf("offramp contract ID cannot be empty")
	}

	return &StellarExecutionAttemptPoller{
		lggr:              lggr,
		rpc:               rpc,
		offrampContractID: offrampContractID,
		filters: []protocolrpc.EventFilter{
			eventsfetcher.ContractTopicFilter(offrampContractID, ExecutionStateChangedEventTopic),
		},
		attemptCache: expirable.NewLRU[protocol.Bytes32, []cachedAttempt](
			defaultAttemptCacheQueueLimit,
			nil,
			attemptCacheExpiration,
		),
		stopCh:               make(services.StopChan),
		pollInterval:         defaultStellarPollInterval,
		retryInterval:        defaultStellarRetryInterval,
		startupRetryInterval: defaultStartupRetryInterval,
		// Keyed by transaction hash (no TTL): doubles as a dedupe guard if the
		// same event fails twice, and bounds pending retries at the queue limit.
		retryQueue:     expirable.NewLRU[string, retryTask](defaultRetryQueueLimit, nil, 0),
		lookbackWindow: attemptCacheExpiration,
	}, nil
}

func (p *StellarExecutionAttemptPoller) Name() string {
	return stellarPollerServiceName
}

// HealthReport reports an error while execution attempts in the lookback
// window are known to be missing because their ledgers left RPC retention
// before they could be polled.
func (p *StellarExecutionAttemptPoller) HealthReport() map[string]error {
	return map[string]error{p.Name(): p.healthError()}
}

// Ready returns nil once the poller is started and its initial backfill of
// the lookback window has completed.
func (p *StellarExecutionAttemptPoller) Ready() error {
	if err := p.StateMachine.Ready(); err != nil {
		return err
	}
	if !p.backfilled.Load() {
		return errors.New("initial backfill not complete")
	}
	return nil
}

// Start launches the polling and retry loops. It does not touch the RPC: the
// start ledger and initial backfill are computed in the background with
// retries, so a transient RPC outage does not fail service startup.
func (p *StellarExecutionAttemptPoller) Start(context.Context) error {
	return p.StartOnce(p.Name(), func() error {
		p.wg.Add(2)
		go func() {
			defer p.wg.Done()
			ctx, cancel := p.stopCh.NewCtx()
			defer cancel()
			p.runPolling(ctx)
		}()
		go func() {
			defer p.wg.Done()
			ctx, cancel := p.stopCh.NewCtx()
			defer cancel()
			p.runRetryLoop(ctx)
		}()
		p.lggr.Info().Msg("Stellar execution attempt poller started")
		return nil
	})
}

// Close stops the poller and waits for the background goroutines to exit.
func (p *StellarExecutionAttemptPoller) Close() error {
	return p.StopOnce(p.Name(), func() error {
		p.lggr.Info().Msg("Stopping stellar execution attempt poller")
		close(p.stopCh)
		p.wg.Wait()
		p.lggr.Info().Msg("Stellar execution attempt poller stopped")
		return nil
	})
}

// GetExecutionAttempts retrieves cached execution attempts for the given message.
func (p *StellarExecutionAttemptPoller) GetExecutionAttempts(_ context.Context, message protocol.Message) ([]protocol.ExecutionAttempt, error) {
	msgID, err := message.MessageID()
	if err != nil {
		return nil, fmt.Errorf("failed to get message ID: %w", err)
	}

	p.attemptCacheMu.Lock()
	defer p.attemptCacheMu.Unlock()

	cached, exists := p.attemptCache.Get(msgID)
	if !exists {
		return nil, nil
	}

	// Retain at most the last N attempts of a given message in cache
	if len(cached) > defaultLastNAttemptsCache {
		cached = cached[len(cached)-defaultLastNAttemptsCache:]
	}

	result := make([]protocol.ExecutionAttempt, len(cached))
	for i, c := range cached {
		result[i] = c.attempt
	}
	return result, nil
}

// getStartLedger estimates the first ledger of the lookback window from the
// latest ledger and the estimated ledger close time, clamped to the RPC's
// retention window.
func (p *StellarExecutionAttemptPoller) getStartLedger(ctx context.Context) (uint32, error) {
	latest, err := p.rpc.GetLatestLedger(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get latest ledger: %w", err)
	}

	lookbackLedgers := int64(p.lookbackWindow / estimatedLedgerCloseTime)
	start := minStellarLedger
	if lookbackLedgers < int64(latest.Sequence) {
		start = max(latest.Sequence-uint32(lookbackLedgers), minStellarLedger)
	}

	health, err := p.rpc.GetHealth(ctx)
	if err != nil {
		p.lggr.Warn().Err(err).Msg("Failed to read RPC retention window; start ledger not clamped to it")
	} else if floor := health.OldestLedger + retentionMargin; health.OldestLedger > minStellarLedger && start < floor {
		// The node has trimmed history (it no longer retains from genesis),
		// so its oldest ledger keeps advancing: start clear of it.
		clamped := min(floor, latest.Sequence)
		p.lggr.Warn().
			Uint32("lookbackStartLedger", start).
			Uint32("oldestLedger", health.OldestLedger).
			Uint32("startLedger", clamped).
			Msg("Lookback window exceeds RPC retention; execution attempts before the start ledger are unavailable")
		start = clamped
	}

	p.lggr.Info().
		Uint32("latestLedger", latest.Sequence).
		Uint32("startLedger", start).
		Int64("lookbackLedgers", lookbackLedgers).
		Msg("Calculated start ledger for backfill")

	return start, nil
}

// runPolling computes the start ledger (retrying until the RPC answers),
// backfills the lookback window, then polls for new events on every tick.
func (p *StellarExecutionAttemptPoller) runPolling(ctx context.Context) {
	if !p.initialize(ctx) {
		return
	}

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.lggr.Debug().Msg("Context cancelled, exiting polling loop")
			return
		case <-ticker.C:
			if err := p.pollForEvents(ctx); err != nil {
				p.lggr.Warn().Err(err).Msg("Failed to poll for execution state changed events")
			}
		}
	}
}

// initialize sets the polling position to just before the start ledger and
// runs the initial backfill. It returns false only if ctx is cancelled first.
func (p *StellarExecutionAttemptPoller) initialize(ctx context.Context) bool {
	for {
		start, err := p.getStartLedger(ctx)
		if err == nil {
			p.lastPolledLedger = start - 1
			p.lggr.Info().Uint32("startLedger", start).Msg("Starting backfill")
			if err := p.pollForEvents(ctx); err != nil {
				// The position did not advance, so the polling loop retries
				// the whole backfill window on its next tick.
				p.lggr.Error().Err(err).Msg("Initial backfill failed; retrying from the polling loop")
			} else {
				p.lggr.Info().Uint32("lastPolledLedger", p.lastPolledLedger).Msg("Backfill complete")
			}
			return true
		}

		p.lggr.Warn().Err(err).Dur("retryIn", p.startupRetryInterval).Msg("Failed to calculate start ledger, retrying")
		select {
		case <-ctx.Done():
			return false
		case <-time.After(p.startupRetryInterval):
		}
	}
}

// pollForEvents fetches ExecutionStateChanged events in
// [lastPolledLedger+1, latest], processes each one, and advances the position
// only once the whole range has been fetched.
func (p *StellarExecutionAttemptPoller) pollForEvents(ctx context.Context) error {
	latest, err := p.rpc.GetLatestLedger(ctx)
	if err != nil {
		return fmt.Errorf("failed to get latest ledger: %w", err)
	}

	fromLedger := p.lastPolledLedger + 1
	toLedger := latest.Sequence
	if toLedger < fromLedger {
		return nil
	}

	res, err := eventsfetcher.FetchRange(ctx, p.rpc, p.filters, fromLedger, toLedger, eventsfetcher.Options{})
	if err != nil {
		var incomplete *eventsfetcher.IncompleteRangeError
		if errors.As(err, &incomplete) {
			// The events node lags the node that reported toLedger; retry the
			// same range next tick.
			p.lggr.Debug().Err(err).Msg("Events node behind latest ledger; will retry range")
			return nil
		}
		p.skipPastRetention(ctx, fromLedger)
		return err
	}

	eventCount := 0
	for _, e := range res.Events {
		parsed, err := ParseExecutionStateChangedEvent(e)
		if err != nil {
			p.lggr.Warn().
				Int("ledger", int(e.Ledger)).
				Str("txHash", e.TransactionHash).
				Err(err).
				Msg("Failed to parse ExecutionStateChanged event, skipping")
			continue
		}

		if err := p.processExecutionStateChanged(ctx, parsed); err != nil {
			p.lggr.Warn().
				Str("txHash", parsed.TxHash).
				Err(err).
				Msg("Failed to process ExecutionStateChanged event, enqueueing for retry")
			p.enqueueRetry(retryTask{event: parsed})
			continue
		}
		eventCount++
	}

	p.lastPolledLedger = toLedger
	p.backfilled.Store(true)

	if eventCount > 0 {
		p.lggr.Debug().
			Uint32("fromLedger", fromLedger).
			Uint32("toLedger", toLedger).
			Int("eventCount", eventCount).
			Msg("Polled execution state changed events")
	}

	return nil
}

// skipPastRetention moves the polling position forward when fromLedger has
// fallen out of the RPC's retention window, which would otherwise make every
// poll fail forever. The skipped ledgers' attempts are lost, so the poller
// reports unhealthy for as long as they would still be in the lookback window.
func (p *StellarExecutionAttemptPoller) skipPastRetention(ctx context.Context, fromLedger uint32) {
	health, err := p.rpc.GetHealth(ctx)
	if err != nil || fromLedger >= health.OldestLedger {
		return
	}

	resume := max(min(health.OldestLedger+retentionMargin, health.LatestLedger), fromLedger)
	skipped := fmt.Errorf("ledgers %d-%d left RPC retention before they were polled; their execution attempts are missing", fromLedger, resume-1)
	p.lggr.Error().
		Err(skipped).
		Uint32("oldestLedger", health.OldestLedger).
		Uint32("resumeLedger", resume).
		Msg("Polling position fell behind RPC retention; skipping ahead")
	p.lastPolledLedger = resume - 1
	p.setHealthError(skipped, time.Now().Add(p.lookbackWindow))
}

func (p *StellarExecutionAttemptPoller) setHealthError(err error, until time.Time) {
	p.healthMu.Lock()
	defer p.healthMu.Unlock()
	p.healthErr = err
	p.healthErrUntil = until
}

func (p *StellarExecutionAttemptPoller) healthError() error {
	p.healthMu.Lock()
	defer p.healthMu.Unlock()
	if p.healthErr != nil && time.Now().After(p.healthErrUntil) {
		p.healthErr = nil
	}
	return p.healthErr
}

// enqueueRetry adds a task to the retry queue. When the queue is full the
// oldest task is evicted; the failed fetch is then unrecoverable until
// process restart.
func (p *StellarExecutionAttemptPoller) enqueueRetry(task retryTask) {
	key := task.event.TxHash
	if !p.retryQueue.Contains(key) && p.retryQueue.Len() >= defaultRetryQueueLimit {
		if evictedKey, _, ok := p.retryQueue.RemoveOldest(); ok {
			p.lggr.Error().
				Str("evictedTxHash", evictedKey).
				Str("txHash", key).
				Int("queueLimit", defaultRetryQueueLimit).
				Msg("Retry queue full, evicted oldest ExecutionStateChanged event")
		}
	}
	p.retryQueue.Add(key, task)
}

// runRetryLoop runs the ticker-based retry loop. It is separate from the
// polling loop so that re-fetching failed transactions never blocks polling.
func (p *StellarExecutionAttemptPoller) runRetryLoop(ctx context.Context) {
	ticker := time.NewTicker(p.retryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.lggr.Debug().Msg("Context cancelled, exiting retry loop")
			return
		case <-ticker.C:
			p.processRetries(ctx)
		}
	}
}

// processRetries drains the tasks that were pending at the start of the tick,
// re-enqueueing the ones that still fail until they exhaust their retries.
func (p *StellarExecutionAttemptPoller) processRetries(ctx context.Context) {
	// Keys() snapshots the pending tasks (oldest first): tasks enqueued during
	// this drain are left for the next tick, so a persistently failing task is
	// retried at most once per interval.
	for _, txHash := range p.retryQueue.Keys() {
		task, ok := p.retryQueue.Peek(txHash)
		if !ok {
			continue
		}
		p.retryQueue.Remove(txHash)

		if err := p.processExecutionStateChanged(ctx, task.event); err != nil {
			task.retries++
			if task.retries >= maxEventProcessRetries {
				p.lggr.Error().
					Str("txHash", task.event.TxHash).
					Int("retries", task.retries).
					Err(err).
					Msg("Exhausted retries for ExecutionStateChanged event, dropping")
				continue
			}
			p.lggr.Debug().
				Str("txHash", task.event.TxHash).
				Int("retries", task.retries).
				Err(err).
				Msg("Retry of ExecutionStateChanged event failed, re-enqueueing")
			p.enqueueRetry(task)
			continue
		}

		p.lggr.Debug().
			Str("txHash", task.event.TxHash).
			Int("retries", task.retries).
			Msg("ExecutionStateChanged event processed successfully on retry")
	}
}

// isCached reports whether the attempt made by txHash for msgID is cached.
func (p *StellarExecutionAttemptPoller) isCached(msgID protocol.Bytes32, txHash string) bool {
	p.attemptCacheMu.Lock()
	defer p.attemptCacheMu.Unlock()
	cached, _ := p.attemptCache.Peek(msgID)
	for _, c := range cached {
		if c.txHash == txHash {
			return true
		}
	}
	return false
}

// processExecutionStateChanged fetches the transaction that triggered the event,
// parses the InvokeHostFunction args, and caches the resulting ExecutionAttempt.
// An attempt already cached for the same transaction is not fetched again.
func (p *StellarExecutionAttemptPoller) processExecutionStateChanged(ctx context.Context, event *ExecutionStateChangedEvent) error {
	msgID := protocol.Bytes32(event.MessageId)
	if p.isCached(msgID, event.TxHash) {
		return nil
	}

	txResp, err := p.rpc.GetTransaction(ctx, protocolrpc.GetTransactionRequest{
		Hash: event.TxHash,
	})
	if err != nil {
		return fmt.Errorf("failed to get transaction %s: %w", event.TxHash, err)
	}

	if txResp.Status != protocolrpc.TransactionStatusSuccess {
		return fmt.Errorf("transaction %s has non-success status: %s", event.TxHash, txResp.Status)
	}

	executeArgs, err := extractInvokeContractArgs(txResp.EnvelopeXDR)
	if err != nil {
		return fmt.Errorf("failed to extract execute args from transaction %s: %w", event.TxHash, err)
	}

	attempt, err := decodeExecuteArgsToAttempt(executeArgs)
	if err != nil {
		return fmt.Errorf("failed to decode execute args from transaction %s: %w", event.TxHash, err)
	}

	// Invariant check: assert that the computed messageID matches the on-chain event.
	attemptMsgID := attempt.Report.Message.MustMessageID()
	if !bytes.Equal(event.MessageId[:], attemptMsgID[:]) {
		p.lggr.Error().
			Str("eventMessageID", fmt.Sprintf("%x", event.MessageId)).
			Str("computedMessageID", fmt.Sprintf("%x", attemptMsgID)).
			Msg("MessageID mismatch between event and computed value")
		return fmt.Errorf("computed message id does not match event message id")
	}

	p.attemptCacheMu.Lock()
	cached, _ := p.attemptCache.Get(msgID)
	for _, c := range cached {
		if c.txHash == event.TxHash {
			// Cached concurrently by the other loop while this one fetched.
			p.attemptCacheMu.Unlock()
			return nil
		}
	}
	updated := make([]cachedAttempt, len(cached), len(cached)+1)
	copy(updated, cached)
	updated = append(updated, cachedAttempt{txHash: event.TxHash, attempt: *attempt})
	p.attemptCache.Add(msgID, updated)
	p.attemptCacheMu.Unlock()

	p.lggr.Debug().
		Str("messageID", fmt.Sprintf("%x", msgID)).
		Str("txHash", event.TxHash).
		Msg("Cached execution attempt")

	return nil
}

// extractInvokeContractArgs parses the transaction envelope XDR and returns
// the ScVal arguments passed to the InvokeHostFunction operation.
func extractInvokeContractArgs(envelopeXDR string) ([]xdr.ScVal, error) {
	var envelope xdr.TransactionEnvelope
	if err := xdr.SafeUnmarshalBase64(envelopeXDR, &envelope); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transaction envelope: %w", err)
	}

	var ops []xdr.Operation
	switch envelope.Type {
	case xdr.EnvelopeTypeEnvelopeTypeTxV0:
		ops = envelope.V0.Tx.Operations
	case xdr.EnvelopeTypeEnvelopeTypeTx:
		ops = envelope.V1.Tx.Operations
	case xdr.EnvelopeTypeEnvelopeTypeTxFeeBump:
		ops = envelope.FeeBump.Tx.InnerTx.V1.Tx.Operations
	default:
		return nil, fmt.Errorf("unsupported envelope type: %v", envelope.Type)
	}

	if len(ops) == 0 {
		return nil, fmt.Errorf("transaction has no operations")
	}

	op := ops[0]
	if op.Body.Type != xdr.OperationTypeInvokeHostFunction {
		return nil, fmt.Errorf("first operation is not InvokeHostFunction (type=%v)", op.Body.Type)
	}

	hostFn := op.Body.InvokeHostFunctionOp.HostFunction
	if hostFn.Type != xdr.HostFunctionTypeHostFunctionTypeInvokeContract {
		return nil, fmt.Errorf("host function is not InvokeContract (type=%v)", hostFn.Type)
	}

	invokeArgs := hostFn.InvokeContract
	if invokeArgs == nil {
		return nil, fmt.Errorf("InvokeContract args are nil")
	}

	return invokeArgs.Args, nil
}

// decodeExecuteArgsToAttempt converts the ScVal arguments of the offramp execute()
// call into a protocol.ExecutionAttempt.
//
// execute(encoded_message: Bytes, ccvs: Vec<Address>, verifier_results: Vec<Bytes>, gas_limit_override: u32)
func decodeExecuteArgsToAttempt(args []xdr.ScVal) (*protocol.ExecutionAttempt, error) {
	if len(args) != expectedExecuteArgCount {
		return nil, fmt.Errorf("expected %d args, got %d", expectedExecuteArgCount, len(args))
	}

	encodedMsg, err := scval.BytesFromScVal(args[0])
	if err != nil {
		return nil, fmt.Errorf("failed to decode encoded_message: %w", err)
	}

	ccvAddresses, err := scval.AddressVecFromScVal(args[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode ccvs: %w", err)
	}

	verifierResults, err := scval.BytesVecFromScVal(args[2])
	if err != nil {
		return nil, fmt.Errorf("failed to decode verifier_results: %w", err)
	}

	gasLimitOverride, ok := args[3].GetU32()
	if !ok {
		return nil, fmt.Errorf("failed to decode gas_limit_override: not a u32")
	}

	message, err := protocol.DecodeMessage(encodedMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to decode message: %w", err)
	}

	ccvs := make([]protocol.UnknownAddress, len(ccvAddresses))
	for i, addr := range ccvAddresses {
		ccvs[i] = protocol.UnknownAddress(addr)
	}

	report := protocol.AbstractAggregatedReport{
		CCVS:    ccvs,
		CCVData: verifierResults,
		Message: *message,
	}

	return &protocol.ExecutionAttempt{
		Report:              report,
		TransactionGasLimit: new(big.Int).SetUint64(uint64(gasLimitOverride)),
	}, nil
}

// GetEventsInRange returns every event whose leading topics equal topics that
// the contract emitted in the inclusive ledger range [fromLedger, toLedger].
// It returns the complete range or an error; see eventsfetcher.FetchRange. A
// node that has not yet ingested toLedger yields a
// *eventsfetcher.IncompleteRangeError, so the same range should be retried.
func GetEventsInRange(ctx context.Context, rpcClient eventsfetcher.Client, contractID string, fromLedger, toLedger uint32, topics []string) ([]protocolrpc.EventInfo, error) {
	filters := []protocolrpc.EventFilter{eventsfetcher.ContractTopicFilter(contractID, topics...)}
	res, err := eventsfetcher.FetchRange(ctx, rpcClient, filters, fromLedger, toLedger, eventsfetcher.Options{})
	if err != nil {
		return nil, err
	}
	return res.Events, nil
}
