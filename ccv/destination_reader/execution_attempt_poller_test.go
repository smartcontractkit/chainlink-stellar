package destinationreader

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	eventsfetcher "github.com/smartcontractkit/chainlink-stellar/ccv/common/events_fetcher"
	"github.com/smartcontractkit/chainlink-stellar/ccv/common/events_fetcher/eventsfetchertest"
)

// fakePollerRPC is an in-memory Stellar RPC: getEvents, getLatestLedger and
// getHealth come from eventsfetchertest.FakeRPC; getTransaction serves the
// registered execute() envelopes and can fail a number of times first.
type fakePollerRPC struct {
	*eventsfetchertest.FakeRPC

	mu sync.Mutex
	// envelopes maps a transaction hash to its envelope XDR.
	envelopes map[string]string
	// latestErr, when set, fails getLatestLedger.
	latestErr error
	// reportedLatest, when non-zero, is returned by getLatestLedger instead
	// of the events node's latest (a load-balanced node that is ahead).
	reportedLatest      uint32
	txFailuresRemaining int
	txCalls             int
}

func newFakePollerRPC(oldest, latest uint32) *fakePollerRPC {
	return &fakePollerRPC{FakeRPC: eventsfetchertest.New(oldest, latest), envelopes: map[string]string{}}
}

func (f *fakePollerRPC) GetLatestLedger(ctx context.Context) (protocolrpc.GetLatestLedgerResponse, error) {
	f.mu.Lock()
	latestErr, reported := f.latestErr, f.reportedLatest
	f.mu.Unlock()
	if latestErr != nil {
		return protocolrpc.GetLatestLedgerResponse{}, latestErr
	}
	if reported != 0 {
		return protocolrpc.GetLatestLedgerResponse{Sequence: reported}, nil
	}
	return f.FakeRPC.GetLatestLedger(ctx)
}

func (f *fakePollerRPC) GetTransaction(_ context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.txCalls++
	if f.txFailuresRemaining > 0 {
		f.txFailuresRemaining--
		return protocolrpc.GetTransactionResponse{}, errors.New("simulated transient RPC failure")
	}
	env, ok := f.envelopes[req.Hash]
	if !ok {
		return protocolrpc.GetTransactionResponse{}, fmt.Errorf("transaction %s not found", req.Hash)
	}
	return protocolrpc.GetTransactionResponse{
		TransactionDetails: protocolrpc.TransactionDetails{
			Status:      protocolrpc.TransactionStatusSuccess,
			EnvelopeXDR: env,
		},
	}, nil
}

func (f *fakePollerRPC) setTxFailures(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.txFailuresRemaining = n
}

func (f *fakePollerRPC) setLatestErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latestErr = err
}

func (f *fakePollerRPC) setReportedLatest(l uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reportedLatest = l
}

func (f *fakePollerRPC) transactionCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.txCalls
}

// addExecution seeds an ExecutionStateChanged event for msg at the given
// ledger, emitted by transaction txHash whose execute() call carries a gas
// limit override of 4242.
func (f *fakePollerRPC) addExecution(t *testing.T, msg protocol.Message, ledger, txIndex uint32, txHash string) protocolrpc.EventInfo {
	t.Helper()
	f.mu.Lock()
	f.envelopes[txHash] = executeEnvelopeXDR(t, msg)
	f.mu.Unlock()

	e := eventsfetchertest.Event(ledger, txIndex, 0, 0, txHash)
	e.ContractID = testStellarContractID
	e.ValueXDR = execStateChangedValueXDR(t, mustMessageID(t, msg))
	f.Add(e)
	return e
}

func execStateChangedValueXDR(t *testing.T, msgID protocol.Bytes32) string {
	t.Helper()
	scMap := xdr.ScMap{
		{Key: scval.SymbolToScVal("message_id"), Val: scval.Bytes32ToScVal([32]byte(msgID))},
	}
	scMapPtr := &scMap
	v, err := xdr.MarshalBase64(xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &scMapPtr})
	require.NoError(t, err)
	return v
}

// executeEnvelopeXDR builds a transaction envelope carrying an execute()
// InvokeHostFunction op for msg with a gas limit override of 4242.
func executeEnvelopeXDR(t *testing.T, msg protocol.Message) string {
	t.Helper()
	encoded, err := msg.Encode()
	require.NoError(t, err)

	contractAddr := scval.ParseAddress(testStellarContractID)
	require.NotNil(t, contractAddr)
	src, err := xdr.AddressToMuxedAccount(keypair.MustRandom().Address())
	require.NoError(t, err)

	envelope := xdr.TransactionEnvelope{
		Type: xdr.EnvelopeTypeEnvelopeTypeTx,
		V1: &xdr.TransactionV1Envelope{
			Tx: xdr.Transaction{
				SourceAccount: src,
				Fee:           100,
				SeqNum:        1,
				Cond:          xdr.Preconditions{Type: xdr.PreconditionTypePrecondNone},
				Memo:          xdr.Memo{Type: xdr.MemoTypeMemoNone},
				Operations: []xdr.Operation{{
					Body: xdr.OperationBody{
						Type: xdr.OperationTypeInvokeHostFunction,
						InvokeHostFunctionOp: &xdr.InvokeHostFunctionOp{
							HostFunction: xdr.HostFunction{
								Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
								InvokeContract: &xdr.InvokeContractArgs{
									ContractAddress: *contractAddr,
									FunctionName:    "execute",
									Args: []xdr.ScVal{
										scval.BytesToScVal(encoded),
										scval.AddressSliceToScVal([]string{testStellarContractID}),
										scval.BytesSliceToScVal([][]byte{{0xab, 0xcd}}),
										scval.Uint32ToScVal(4242),
									},
								},
							},
						},
					},
				}},
				Ext: xdr.TransactionExt{V: 0},
			},
		},
	}
	b64, err := xdr.MarshalBase64(envelope)
	require.NoError(t, err)
	return b64
}

func newTestPoller(t *testing.T, rpc *fakePollerRPC, lookback time.Duration) *StellarExecutionAttemptPoller {
	t.Helper()
	p, err := newExecutionAttemptPoller(rpc, testStellarContractID, testLogger(t), lookback)
	require.NoError(t, err)
	p.pollInterval = 20 * time.Millisecond
	p.retryInterval = 20 * time.Millisecond
	p.startupRetryInterval = 20 * time.Millisecond
	return p
}

func startTestPoller(t *testing.T, p *StellarExecutionAttemptPoller) {
	t.Helper()
	require.NoError(t, p.Start(context.Background()))
	t.Cleanup(func() { _ = p.Close() })
}

func attemptsFor(t *testing.T, p *StellarExecutionAttemptPoller, msg protocol.Message) []protocol.ExecutionAttempt {
	t.Helper()
	attempts, err := p.GetExecutionAttempts(context.Background(), msg)
	require.NoError(t, err)
	return attempts
}

// TestStellarExecutionAttemptPoller_RetriesFailedTransactionFetch reproduces the
// audit scenario: the transaction RPC fails transiently while the polling
// cursor advances past the event. The retry queue must recover the attempt
// once the RPC recovers.
func TestStellarExecutionAttemptPoller_RetriesFailedTransactionFetch(t *testing.T) {
	msg := mustTestMessage(t)
	rpc := newFakePollerRPC(1, 20)
	rpc.addExecution(t, msg, 15, 0, "txhash-1")
	// The initial fetch in the polling loop and the first retry both fail.
	rpc.setTxFailures(2)

	p := newTestPoller(t, rpc, time.Minute)
	startTestPoller(t, p)

	require.Eventually(t, func() bool {
		return len(attemptsFor(t, p, msg)) == 1
	}, 5*time.Second, 10*time.Millisecond, "attempt must be recovered via the retry queue")

	attempts := attemptsFor(t, p, msg)
	require.Len(t, attempts, 1)
	assert.Equal(t, big.NewInt(4242), attempts[0].TransactionGasLimit)

	// 1 failed initial fetch + 1 failed retry + 1 successful retry.
	assert.Equal(t, 3, rpc.transactionCalls())
	assert.Equal(t, 0, p.retryQueue.Len())
}

// TestStellarExecutionAttemptPoller_DropsEventAfterMaxRetries verifies that a
// permanently failing fetch is retried exactly maxEventProcessRetries times
// before being dropped.
func TestStellarExecutionAttemptPoller_DropsEventAfterMaxRetries(t *testing.T) {
	msg := mustTestMessage(t)
	rpc := newFakePollerRPC(1, 20)
	rpc.addExecution(t, msg, 15, 0, "txhash-1")
	rpc.setTxFailures(100) // never recovers

	p := newTestPoller(t, rpc, time.Minute)
	startTestPoller(t, p)

	// 1 failed initial fetch + maxEventProcessRetries failed retries.
	require.Eventually(t, func() bool {
		return rpc.transactionCalls() == 1+maxEventProcessRetries
	}, 5*time.Second, 10*time.Millisecond)

	// Once dropped, the queue stays empty and no further fetches happen.
	time.Sleep(10 * p.retryInterval)
	assert.Equal(t, 1+maxEventProcessRetries, rpc.transactionCalls())
	assert.Equal(t, 0, p.retryQueue.Len())
	assert.Nil(t, attemptsFor(t, p, msg))
}

// TestStellarExecutionAttemptPoller_RetryQueueLimit verifies the retry queue is
// bounded at defaultRetryQueueLimit and evicts the oldest entry when full.
func TestStellarExecutionAttemptPoller_RetryQueueLimit(t *testing.T) {
	p, err := NewStellarExecutionAttemptPoller(testRPCClient(t), testStellarContractID, testLogger(t), time.Minute)
	require.NoError(t, err)

	p.enqueueRetry(retryTask{event: &ExecutionStateChangedEvent{TxHash: "tx-oldest"}})
	for i := 0; i < 2*defaultRetryQueueLimit; i++ {
		p.enqueueRetry(retryTask{event: &ExecutionStateChangedEvent{TxHash: fmt.Sprintf("tx-%d", i)}})
	}

	assert.Equal(t, defaultRetryQueueLimit, p.retryQueue.Len())
	assert.False(t, p.retryQueue.Contains("tx-oldest"), "oldest entry must be evicted when the queue is full")
	assert.True(t, p.retryQueue.Contains(fmt.Sprintf("tx-%d", 2*defaultRetryQueueLimit-1)), "newest entry must be kept")
}

// TestStellarExecutionAttemptPoller_RetryQueueDeduplicates verifies that the
// same event enqueued twice (keyed by transaction hash) occupies one slot.
func TestStellarExecutionAttemptPoller_RetryQueueDeduplicates(t *testing.T) {
	p, err := NewStellarExecutionAttemptPoller(testRPCClient(t), testStellarContractID, testLogger(t), time.Minute)
	require.NoError(t, err)

	event := &ExecutionStateChangedEvent{TxHash: "tx-dup"}
	p.enqueueRetry(retryTask{event: event})
	p.enqueueRetry(retryTask{event: event})

	assert.Equal(t, 1, p.retryQueue.Len())
}

// TestStellarExecutionAttemptPoller_ProcessRetriesOncePerInterval verifies that
// a task re-enqueued after a failed retry is not retried again in the same
// tick (it must wait for the next interval).
func TestStellarExecutionAttemptPoller_ProcessRetriesOncePerInterval(t *testing.T) {
	msg := mustTestMessage(t)
	rpc := newFakePollerRPC(1, 20)
	rpc.setTxFailures(100)

	p := newTestPoller(t, rpc, time.Minute)
	p.enqueueRetry(retryTask{event: &ExecutionStateChangedEvent{
		TxHash:    "txhash-1",
		MessageId: [32]byte(mustMessageID(t, msg)),
	}})

	p.processRetries(context.Background())
	assert.Equal(t, 1, rpc.transactionCalls(), "first retry attempt")
	assert.Equal(t, 1, p.retryQueue.Len(), "failed task must be re-enqueued")

	p.processRetries(context.Background())
	assert.Equal(t, 2, rpc.transactionCalls(), "exactly one retry per interval")
}

// TestStellarExecutionAttemptPoller_ConcurrentAttemptsSameMessage is a
// regression test for the lost-update race on attemptCache: the polling and
// retry loops can process different events for the same message concurrently,
// and the cache read-modify-write must retain every attempt.
func TestStellarExecutionAttemptPoller_ConcurrentAttemptsSameMessage(t *testing.T) {
	msg := mustTestMessage(t)
	rpc := newFakePollerRPC(1, 20)
	p := newTestPoller(t, rpc, time.Minute)

	const eventCount = 10
	msgID := mustMessageID(t, msg)
	env := executeEnvelopeXDR(t, msg)
	for i := 0; i < eventCount; i++ {
		rpc.envelopes[fmt.Sprintf("tx-%d", i)] = env
	}

	var wg sync.WaitGroup
	for i := 0; i < eventCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			event := &ExecutionStateChangedEvent{
				TxHash:    fmt.Sprintf("tx-%d", i),
				MessageId: [32]byte(msgID),
			}
			assert.NoError(t, p.processExecutionStateChanged(context.Background(), event))
		}(i)
	}
	wg.Wait()

	assert.Len(t, attemptsFor(t, p, msg), eventCount, "no attempt may be lost to a concurrent cache update")
}

// The same transaction's attempt is cached once even if its event is processed
// twice, and the second time costs no getTransaction call.
func TestStellarExecutionAttemptPoller_DedupesSameTransaction(t *testing.T) {
	msg := mustTestMessage(t)
	rpc := newFakePollerRPC(1, 20)
	p := newTestPoller(t, rpc, time.Minute)
	rpc.envelopes["tx-1"] = executeEnvelopeXDR(t, msg)

	event := &ExecutionStateChangedEvent{TxHash: "tx-1", MessageId: [32]byte(mustMessageID(t, msg))}
	require.NoError(t, p.processExecutionStateChanged(context.Background(), event))
	require.NoError(t, p.processExecutionStateChanged(context.Background(), event))

	assert.Len(t, attemptsFor(t, p, msg), 1)
	assert.Equal(t, 1, rpc.transactionCalls())
}

// Regression: the old poller started each poll at the previous poll's end
// ledger (inclusive), so an event in that ledger could be cached twice.
func TestStellarExecutionAttemptPoller_EventAtPollBoundaryCachedOnce(t *testing.T) {
	msg := mustTestMessage(t)
	rpc := newFakePollerRPC(1, 100)
	rpc.addExecution(t, msg, 100, 0, "tx-boundary")
	p := newTestPoller(t, rpc, time.Minute)
	p.lastPolledLedger = 49

	require.NoError(t, p.pollForEvents(context.Background()))
	assert.Equal(t, uint32(100), p.lastPolledLedger)

	rpc.SetLatest(150)
	require.NoError(t, p.pollForEvents(context.Background()))

	assert.Len(t, attemptsFor(t, p, msg), 1)
	assert.Equal(t, 1, rpc.transactionCalls())

	reqs := rpc.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, uint32(50), reqs[0].StartLedger)
	assert.Equal(t, uint32(101), reqs[0].EndLedger)
	assert.Equal(t, uint32(101), reqs[1].StartLedger, "next window starts after the last polled ledger")
	assert.Equal(t, uint32(151), reqs[1].EndLedger)
}

// A backfill wider than the server's 10000-ledger scan cap must cover the
// whole window; the old poller stopped after the first short page.
func TestStellarExecutionAttemptPoller_BackfillBeyondScanCap(t *testing.T) {
	msgA, msgB := mustTestMessage(t), mustTestMessage(t)
	rpc := newFakePollerRPC(1, 30000)
	rpc.addExecution(t, msgA, 12000, 0, "tx-a")
	rpc.addExecution(t, msgB, 29000, 0, "tx-b")

	// 30000 ledgers of lookback at the 5s estimate.
	p := newTestPoller(t, rpc, 30000*estimatedLedgerCloseTime)
	startTestPoller(t, p)

	require.Eventually(t, func() bool { return p.Ready() == nil }, 5*time.Second, 10*time.Millisecond)
	assert.Len(t, attemptsFor(t, p, msgA), 1)
	assert.Len(t, attemptsFor(t, p, msgB), 1)
}

func TestStellarExecutionAttemptPoller_StartLedger(t *testing.T) {
	t.Run("lookback window", func(t *testing.T) {
		rpc := newFakePollerRPC(1, 10000)
		p := newTestPoller(t, rpc, 1000*estimatedLedgerCloseTime)
		start, err := p.getStartLedger(context.Background())
		require.NoError(t, err)
		assert.Equal(t, uint32(9000), start)
	})

	t.Run("clamped to the minimum ledger", func(t *testing.T) {
		rpc := newFakePollerRPC(1, 100)
		p := newTestPoller(t, rpc, 1000*estimatedLedgerCloseTime)
		start, err := p.getStartLedger(context.Background())
		require.NoError(t, err)
		assert.Equal(t, minStellarLedger, start)
	})

	t.Run("within RPC retention is not clamped", func(t *testing.T) {
		rpc := newFakePollerRPC(8500, 10000)
		p := newTestPoller(t, rpc, 1000*estimatedLedgerCloseTime)
		start, err := p.getStartLedger(context.Background())
		require.NoError(t, err)
		assert.Equal(t, uint32(9000), start)
	})

	t.Run("clamped to RPC retention", func(t *testing.T) {
		rpc := newFakePollerRPC(8500, 10000)
		p := newTestPoller(t, rpc, 2000*estimatedLedgerCloseTime)
		start, err := p.getStartLedger(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 8500+retentionMargin, start)
	})

	t.Run("huge lookback does not overflow", func(t *testing.T) {
		rpc := newFakePollerRPC(1, 10000)
		p := newTestPoller(t, rpc, 1<<62)
		start, err := p.getStartLedger(context.Background())
		require.NoError(t, err)
		assert.Equal(t, minStellarLedger, start, "a node retaining from genesis is not clamped")
	})
}

// A polling position that has fallen out of RPC retention (e.g. after a long
// RPC outage) would fail every poll forever; the poller must skip ahead and
// report the gap as unhealthy.
func TestStellarExecutionAttemptPoller_SkipsPastRetention(t *testing.T) {
	msg := mustTestMessage(t)
	rpc := newFakePollerRPC(1, 5000)
	rpc.addExecution(t, msg, 4500, 0, "tx-1")
	p := newTestPoller(t, rpc, time.Hour)
	p.lastPolledLedger = 100
	rpc.SetOldest(4000)

	require.Error(t, p.pollForEvents(context.Background()))
	assert.Equal(t, 4000+retentionMargin-1, p.lastPolledLedger)
	require.Error(t, p.HealthReport()[p.Name()])

	require.NoError(t, p.pollForEvents(context.Background()))
	assert.Len(t, attemptsFor(t, p, msg), 1)
	require.Error(t, p.HealthReport()[p.Name()], "stays unhealthy while the skipped ledgers are inside the lookback window")

	p.setHealthError(p.healthErr, time.Now().Add(-time.Second))
	assert.NoError(t, p.HealthReport()[p.Name()], "clears once the skipped ledgers age out of the lookback window")
}

// A fetch error unrelated to retention must not move the polling position.
func TestStellarExecutionAttemptPoller_FetchErrorDoesNotAdvance(t *testing.T) {
	rpc := newFakePollerRPC(1, 5000)
	p := newTestPoller(t, rpc, time.Hour)
	p.lastPolledLedger = 100
	rpc.Override = func(int, protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error, bool) {
		return protocolrpc.GetEventsResponse{}, errors.New("rpc down"), true
	}

	require.Error(t, p.pollForEvents(context.Background()))
	assert.Equal(t, uint32(100), p.lastPolledLedger)
	assert.NoError(t, p.HealthReport()[p.Name()])
}

// When getLatestLedger is answered by a node ahead of the one serving
// getEvents, the poll must not advance past ledgers it has not read.
func TestStellarExecutionAttemptPoller_LaggingEventsNodeDoesNotAdvance(t *testing.T) {
	msg := mustTestMessage(t)
	rpc := newFakePollerRPC(1, 100)
	p := newTestPoller(t, rpc, time.Hour)
	p.lastPolledLedger = 49
	rpc.setReportedLatest(120)

	require.NoError(t, p.pollForEvents(context.Background()))
	assert.Equal(t, uint32(49), p.lastPolledLedger)

	rpc.addExecution(t, msg, 110, 0, "tx-late")
	rpc.SetLatest(120)
	require.NoError(t, p.pollForEvents(context.Background()))
	assert.Equal(t, uint32(120), p.lastPolledLedger)
	assert.Len(t, attemptsFor(t, p, msg), 1)
}

// Start must not fail when the RPC is down; the poller backfills once it
// recovers and only then reports ready.
func TestStellarExecutionAttemptPoller_StartToleratesRPCOutage(t *testing.T) {
	msg := mustTestMessage(t)
	rpc := newFakePollerRPC(1, 20)
	rpc.addExecution(t, msg, 15, 0, "tx-1")
	rpc.setLatestErr(errors.New("rpc down"))

	p := newTestPoller(t, rpc, time.Minute)
	startTestPoller(t, p)

	time.Sleep(5 * p.startupRetryInterval)
	assert.Error(t, p.Ready(), "not ready before the backfill")
	assert.Nil(t, attemptsFor(t, p, msg))

	rpc.setLatestErr(nil)
	require.Eventually(t, func() bool { return p.Ready() == nil }, 5*time.Second, 10*time.Millisecond)
	assert.Len(t, attemptsFor(t, p, msg), 1)
}

func TestStellarExecutionAttemptPoller_StartCloseOnce(t *testing.T) {
	rpc := newFakePollerRPC(1, 20)
	p := newTestPoller(t, rpc, time.Minute)

	require.NoError(t, p.Start(context.Background()))
	require.Error(t, p.Start(context.Background()), "second Start must fail rather than launch duplicate loops")
	require.NoError(t, p.Close())
	require.Error(t, p.Close())
}

// Readers may query attempts while both loops are writing the cache.
func TestStellarExecutionAttemptPoller_ConcurrentReadsWhileRunning(t *testing.T) {
	rpc := newFakePollerRPC(1, 20)
	msgs := make([]protocol.Message, 5)
	for i := range msgs {
		msgs[i] = mustTestMessage(t)
		rpc.addExecution(t, msgs[i], uint32(10+i), 0, fmt.Sprintf("tx-%d", i))
	}
	rpc.setTxFailures(3)

	p := newTestPoller(t, rpc, time.Minute)
	startTestPoller(t, p)

	require.Eventually(t, func() bool {
		for _, m := range msgs {
			if len(attemptsFor(t, p, m)) != 1 {
				return false
			}
		}
		return true
	}, 5*time.Second, time.Millisecond)
}

func mustMessageID(t *testing.T, msg protocol.Message) protocol.Bytes32 {
	t.Helper()
	id, err := msg.MessageID()
	require.NoError(t, err)
	return id
}

func TestGetEventsInRange_InclusiveRangeAndFilter(t *testing.T) {
	f := eventsfetchertest.New(10, 500)
	f.Add(
		eventsfetchertest.Event(99, 0, 0, 0, "before"),
		eventsfetchertest.Event(100, 0, 0, 0, "from"),
		eventsfetchertest.Event(200, 0, 0, 0, "to"),
		eventsfetchertest.Event(201, 0, 0, 0, "after"),
	)

	events, err := GetEventsInRange(context.Background(), f, testStellarContractID, 100, 200, []string{"topic"})
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, "from", events[0].TransactionHash)
	assert.Equal(t, "to", events[1].TransactionHash)

	reqs := f.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, uint32(100), reqs[0].StartLedger)
	assert.Equal(t, uint32(201), reqs[0].EndLedger, "EndLedger is exclusive")
	require.Len(t, reqs[0].Filters, 1)
	filter := reqs[0].Filters[0]
	assert.Equal(t, []string{testStellarContractID}, filter.ContractIDs)
	require.Len(t, filter.Topics, 1)
	require.Len(t, filter.Topics[0], 2, "topic symbol followed by a zero-or-more wildcard")
	assert.Equal(t, scval.SymbolToScValPtr("topic"), filter.Topics[0][0].ScVal)
	require.NotNil(t, filter.Topics[0][1].Wildcard)
	assert.Equal(t, protocolrpc.WildCardZeroOrMore, *filter.Topics[0][1].Wildcard)
}

func TestGetEventsInRange_LaggingNodeErrors(t *testing.T) {
	f := eventsfetchertest.New(10, 150)

	events, err := GetEventsInRange(context.Background(), f, testStellarContractID, 100, 200, []string{"topic"})
	var inc *eventsfetcher.IncompleteRangeError
	require.ErrorAs(t, err, &inc)
	assert.Nil(t, events)
}
