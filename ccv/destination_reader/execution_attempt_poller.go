package destinationreader

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/rs/zerolog"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-ccv/protocol"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
)

const (
	defaultStellarPollInterval = 5 * time.Second
	// Stellar ledgers close approximately every 5-6 seconds.
	estimatedLedgerCloseTime = 6 * time.Second
	minStellarLedger         = uint32(7)
	expectedExecuteArgCount  = 4
	stellarPollerServiceName = "stellar.executionattemptpoller.Service"
)

// StellarExecutionAttemptPoller polls for ExecutionStateChanged events on the Stellar
// offramp contract and caches the decoded execution attempts by message ID.
type StellarExecutionAttemptPoller struct {
	lggr              *zerolog.Logger
	rpcClient         ccvclient.RPCClient
	offrampContractID string
	attemptCache      *expirable.LRU[protocol.Bytes32, []protocol.ExecutionAttempt]
	cancelFunc        context.CancelFunc
	wg                sync.WaitGroup
	pollInterval      time.Duration
	lastPolledLedger  uint32
	startLedger       uint32
	lookbackWindow    time.Duration
}

// NewStellarExecutionAttemptPoller creates a new execution attempt poller for the Stellar offramp.
// On startup the poller performs a backfill over the lookback window, then continues
// polling for new events at the configured interval.
func NewStellarExecutionAttemptPoller(
	rpcClient ccvclient.RPCClient,
	offrampContractID string,
	lggr *zerolog.Logger,
	attemptCacheExpiration time.Duration,
) (*StellarExecutionAttemptPoller, error) {
	if rpcClient == nil {
		return nil, fmt.Errorf("rpc client cannot be nil")
	}
	if lggr == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}
	if offrampContractID == "" {
		return nil, fmt.Errorf("offramp contract ID cannot be empty")
	}

	attemptCache := expirable.NewLRU[protocol.Bytes32, []protocol.ExecutionAttempt](0, nil, attemptCacheExpiration)

	return &StellarExecutionAttemptPoller{
		lggr:              lggr,
		rpcClient:         rpcClient,
		offrampContractID: offrampContractID,
		attemptCache:      attemptCache,
		pollInterval:      defaultStellarPollInterval,
		lookbackWindow:    attemptCacheExpiration,
	}, nil
}

func (p *StellarExecutionAttemptPoller) Name() string {
	return stellarPollerServiceName
}

func (p *StellarExecutionAttemptPoller) HealthReport() map[string]error {
	return map[string]error{p.Name(): nil}
}

// Start calculates the start ledger, runs an initial backfill, and begins
// the background polling loop.
func (p *StellarExecutionAttemptPoller) Start(ctx context.Context) error {
	if err := p.getStartLedger(ctx); err != nil {
		return fmt.Errorf("failed to calculate start ledger: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	p.cancelFunc = cancel

	if err := p.runBackfill(runCtx); err != nil {
		p.lggr.Error().Err(err).Msg("Initial backfill failed, continuing without complete history")
		p.lastPolledLedger = p.startLedger
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.runPolling(runCtx)
	}()

	p.lggr.Info().
		Uint32("startLedger", p.startLedger).
		Uint32("lastPolledLedger", p.lastPolledLedger).
		Msg("Stellar execution attempt poller started")
	return nil
}

// Close stops the poller and waits for the background goroutine to exit.
func (p *StellarExecutionAttemptPoller) Close() error {
	p.lggr.Info().Msg("Stopping stellar execution attempt poller")
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
	p.wg.Wait()
	p.lggr.Info().Msg("Stellar execution attempt poller stopped")
	return nil
}

// GetExecutionAttempts retrieves cached execution attempts for the given message.
func (p *StellarExecutionAttemptPoller) GetExecutionAttempts(_ context.Context, message protocol.Message) ([]protocol.ExecutionAttempt, error) {
	msgID, err := message.MessageID()
	if err != nil {
		return nil, fmt.Errorf("failed to get message ID: %w", err)
	}

	attempts, exists := p.attemptCache.Get(msgID)
	if !exists {
		return nil, nil
	}

	result := make([]protocol.ExecutionAttempt, len(attempts))
	copy(result, attempts)
	return result, nil
}

// getStartLedger estimates the ledger sequence to start polling from based on
// the lookback window and the latest ledger's close time.
func (p *StellarExecutionAttemptPoller) getStartLedger(ctx context.Context) error {
	latest, err := p.rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return fmt.Errorf("failed to get latest ledger: %w", err)
	}

	lookbackLedgers := uint32(p.lookbackWindow / estimatedLedgerCloseTime)
	if lookbackLedgers >= latest.Sequence {
		p.startLedger = minStellarLedger
	} else {
		p.startLedger = max(latest.Sequence-lookbackLedgers, minStellarLedger)
	}

	p.lggr.Info().
		Uint32("latestLedger", latest.Sequence).
		Uint32("startLedger", p.startLedger).
		Uint32("lookbackLedgers", lookbackLedgers).
		Msg("Calculated start ledger for backfill")

	return nil
}

// runBackfill performs a one-shot event query covering the full lookback window.
func (p *StellarExecutionAttemptPoller) runBackfill(ctx context.Context) error {
	p.lggr.Info().Uint32("startLedger", p.startLedger).Msg("Starting backfill")

	p.lastPolledLedger = p.startLedger

	if err := p.pollForEvents(ctx); err != nil {
		return fmt.Errorf("backfill failed: %w", err)
	}

	p.lggr.Info().Uint32("lastPolledLedger", p.lastPolledLedger).Msg("Backfill complete")
	return nil
}

// runPolling runs the ticker-based polling loop.
func (p *StellarExecutionAttemptPoller) runPolling(ctx context.Context) {
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

// pollForEvents queries for ExecutionStateChanged events from lastPolledLedger
// to the latest ledger, processes each event, and advances the cursor.
func (p *StellarExecutionAttemptPoller) pollForEvents(ctx context.Context) error {
	latest, err := p.rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return fmt.Errorf("failed to get latest ledger: %w", err)
	}

	toLedger := latest.Sequence
	if toLedger <= p.lastPolledLedger {
		return nil
	}

	topicScVal := scval.SymbolToScValPtr(offrampbindings.ExecutionStateChangedEventTopic)
	zeroOrMore := protocolrpc.WildCardZeroOrMore

	resp, err := p.rpcClient.GetEvents(ctx, protocolrpc.GetEventsRequest{
		StartLedger: p.lastPolledLedger,
		EndLedger:   toLedger,
		Filters: []protocolrpc.EventFilter{
			{
				EventType:   protocolrpc.EventTypeSet{protocolrpc.EventTypeContract: nil},
				ContractIDs: []string{p.offrampContractID},
				Topics: []protocolrpc.TopicFilter{
					{
						{ScVal: topicScVal},
						{Wildcard: &zeroOrMore},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to get events: %w", err)
	}

	eventCount := 0
	for _, e := range resp.Events {
		parsed, err := offrampbindings.ParseExecutionStateChangedEvent(e)
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
				Msg("Failed to process ExecutionStateChanged event")
			continue
		}
		eventCount++
	}

	p.lastPolledLedger = toLedger

	if eventCount > 0 {
		p.lggr.Debug().
			Uint32("fromLedger", p.lastPolledLedger).
			Uint32("toLedger", toLedger).
			Int("eventCount", eventCount).
			Msg("Polled execution state changed events")
	}

	return nil
}

// processExecutionStateChanged fetches the transaction that triggered the event,
// parses the InvokeHostFunction args, and caches the resulting ExecutionAttempt.
func (p *StellarExecutionAttemptPoller) processExecutionStateChanged(ctx context.Context, event *offrampbindings.ExecutionStateChangedEvent) error {
	txResp, err := p.rpcClient.GetTransaction(ctx, protocolrpc.GetTransactionRequest{
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

	msgID := protocol.Bytes32(event.MessageId)
	attempts, _ := p.attemptCache.Get(msgID)
	attempts = append(attempts, *attempt)
	p.attemptCache.Add(msgID, attempts)

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
