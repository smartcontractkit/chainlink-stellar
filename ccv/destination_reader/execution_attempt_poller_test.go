package destinationreader

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
)

// fakeStellarRPC is a minimal Stellar JSON-RPC server double that serves one
// ExecutionStateChanged event and can be configured to fail getTransaction a
// fixed number of times before succeeding.
type fakeStellarRPC struct {
	event        protocolrpc.EventInfo
	envelopeXDR  string
	latestLedger uint32

	mu                  sync.Mutex
	txFailuresRemaining int
	txCalls             int
}

func (f *fakeStellarRPC) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	writeResult := func(result any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		})
	}

	switch req.Method {
	case protocolrpc.GetLatestLedgerMethodName:
		writeResult(map[string]any{
			"sequence":  f.latestLedger,
			"closeTime": "1700000000",
		})
	case protocolrpc.GetEventsMethodName:
		writeResult(protocolrpc.GetEventsResponse{
			Events:       []protocolrpc.EventInfo{f.event},
			LatestLedger: f.latestLedger,
		})
	case protocolrpc.GetTransactionMethodName:
		f.mu.Lock()
		f.txCalls++
		fail := f.txFailuresRemaining > 0
		if fail {
			f.txFailuresRemaining--
		}
		f.mu.Unlock()

		if fail {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error":   map[string]any{"code": -32603, "message": "simulated transient RPC failure"},
			})
			return
		}
		writeResult(map[string]any{
			"status":                protocolrpc.TransactionStatusSuccess,
			"envelopeXdr":           f.envelopeXDR,
			"latestLedger":          f.latestLedger,
			"latestLedgerCloseTime": "1700000000",
			"oldestLedger":          1,
			"oldestLedgerCloseTime": "1699999900",
			"createdAt":             "1700000000",
		})
	default:
		http.Error(w, "unknown method", http.StatusNotFound)
	}
}

func (f *fakeStellarRPC) transactionCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.txCalls
}

// newFakeStellarRPC builds a fake RPC serving a single ExecutionStateChanged
// event at ledger 15 whose transaction executes the given message with a gas
// limit override of 4242.
func newFakeStellarRPC(t *testing.T, msg protocol.Message) *fakeStellarRPC {
	t.Helper()

	encoded, err := msg.Encode()
	require.NoError(t, err)
	msgID, err := msg.MessageID()
	require.NoError(t, err)

	// Event value: the ScVal map with the message_id entry the parser reads.
	scMap := xdr.ScMap{
		{Key: scval.SymbolToScVal("message_id"), Val: scval.Bytes32ToScVal([32]byte(msgID))},
	}
	scMapPtr := &scMap
	eventVal := xdr.ScVal{
		Type: xdr.ScValTypeScvMap,
		Map:  &scMapPtr,
	}
	eventValB64, err := xdr.MarshalBase64(eventVal)
	require.NoError(t, err)

	// Transaction envelope carrying an execute() InvokeHostFunction op.
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
	envelopeB64, err := xdr.MarshalBase64(envelope)
	require.NoError(t, err)

	return &fakeStellarRPC{
		latestLedger: 20,
		event: protocolrpc.EventInfo{
			EventType:       protocolrpc.EventTypeContract,
			Ledger:          15,
			LedgerClosedAt:  "2026-01-01T00:00:00Z",
			ContractID:      testStellarContractID,
			ID:              "evt-1",
			TransactionHash: "txhash-1",
			ValueXDR:        eventValB64,
		},
		envelopeXDR: envelopeB64,
	}
}

// TestStellarExecutionAttemptPoller_RetriesFailedTransactionFetch reproduces the
// audit scenario: the transaction RPC fails transiently while the polling
// cursor advances past the event. The retry queue must recover the attempt
// once the RPC recovers.
func TestStellarExecutionAttemptPoller_RetriesFailedTransactionFetch(t *testing.T) {
	msg := mustTestMessage(t)
	fake := newFakeStellarRPC(t, msg)
	// The initial fetch in the polling loop and the first retry both fail.
	fake.txFailuresRemaining = 2

	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)

	p, err := NewStellarExecutionAttemptPoller(rpcclient.NewClient(server.URL, nil), testStellarContractID, testLogger(t), time.Minute)
	require.NoError(t, err)
	p.pollInterval = 20 * time.Millisecond
	p.retryInterval = 20 * time.Millisecond

	require.NoError(t, p.Start(context.Background()))
	t.Cleanup(func() { _ = p.Close() })

	require.Eventually(t, func() bool {
		attempts, err := p.GetExecutionAttempts(context.Background(), msg)
		return err == nil && len(attempts) == 1
	}, 5*time.Second, 10*time.Millisecond, "attempt must be recovered via the retry queue")

	attempts, err := p.GetExecutionAttempts(context.Background(), msg)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	assert.Equal(t, big.NewInt(4242), attempts[0].TransactionGasLimit)

	// 1 failed initial fetch + 1 failed retry + 1 successful retry.
	assert.Equal(t, 3, fake.transactionCalls())
	assert.Equal(t, 0, p.retryQueue.Len())
}

// TestStellarExecutionAttemptPoller_DropsEventAfterMaxRetries verifies that a
// permanently failing fetch is retried exactly maxEventProcessRetries times
// before being dropped.
func TestStellarExecutionAttemptPoller_DropsEventAfterMaxRetries(t *testing.T) {
	msg := mustTestMessage(t)
	fake := newFakeStellarRPC(t, msg)
	fake.txFailuresRemaining = 100 // never recovers

	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)

	p, err := NewStellarExecutionAttemptPoller(rpcclient.NewClient(server.URL, nil), testStellarContractID, testLogger(t), time.Minute)
	require.NoError(t, err)
	p.pollInterval = 20 * time.Millisecond
	p.retryInterval = 20 * time.Millisecond

	require.NoError(t, p.Start(context.Background()))
	t.Cleanup(func() { _ = p.Close() })

	// 1 failed initial fetch + maxEventProcessRetries failed retries.
	require.Eventually(t, func() bool {
		return fake.transactionCalls() == 1+maxEventProcessRetries
	}, 5*time.Second, 10*time.Millisecond)

	// Once dropped, the queue stays empty and no further fetches happen.
	time.Sleep(10 * p.retryInterval)
	assert.Equal(t, 1+maxEventProcessRetries, fake.transactionCalls())
	assert.Equal(t, 0, p.retryQueue.Len())

	attempts, err := p.GetExecutionAttempts(context.Background(), msg)
	require.NoError(t, err)
	assert.Nil(t, attempts)
}

// TestStellarExecutionAttemptPoller_RetryQueueLimit verifies the retry queue is
// bounded at defaultRetryQueueLimit and evicts the oldest entry when full.
func TestStellarExecutionAttemptPoller_RetryQueueLimit(t *testing.T) {
	p, err := NewStellarExecutionAttemptPoller(testRPCClient(t), testStellarContractID, testLogger(t), time.Minute)
	require.NoError(t, err)

	p.enqueueRetry(retryTask{event: &ExecutionStateChangedEvent{TxHash: "tx-oldest"}})
	for i := 0; i < 2*defaultRetryQueueLimit; i++ {
		p.enqueueRetry(retryTask{event: &ExecutionStateChangedEvent{TxHash: string(rune('a'+i%26)) + string(rune('a'+i/26)) + string(rune('0'+i%10))}})
	}

	assert.Equal(t, defaultRetryQueueLimit, p.retryQueue.Len())
	assert.False(t, p.retryQueue.Contains("tx-oldest"), "oldest entry must be evicted when the queue is full")
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
	fake := newFakeStellarRPC(t, msg)
	fake.txFailuresRemaining = 100

	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)

	p, err := NewStellarExecutionAttemptPoller(rpcclient.NewClient(server.URL, nil), testStellarContractID, testLogger(t), time.Minute)
	require.NoError(t, err)
	p.retryQueue = expirable.NewLRU[string, retryTask](defaultRetryQueueLimit, nil, 0)
	p.retryQueue.Add(fake.event.TransactionHash, retryTask{event: &ExecutionStateChangedEvent{
		TxHash:    fake.event.TransactionHash,
		MessageId: [32]byte(mustMessageID(t, msg)),
	}})

	p.processRetries(context.Background())
	assert.Equal(t, 1, fake.transactionCalls(), "first retry attempt")
	assert.Equal(t, 1, p.retryQueue.Len(), "failed task must be re-enqueued")

	p.processRetries(context.Background())
	assert.Equal(t, 2, fake.transactionCalls(), "exactly one retry per interval")
}

// TestStellarExecutionAttemptPoller_ConcurrentAttemptsSameMessage is a
// regression test for the lost-update race on attemptCache: the polling and
// retry loops can process different events for the same message concurrently,
// and the cache read-modify-write must retain every attempt.
func TestStellarExecutionAttemptPoller_ConcurrentAttemptsSameMessage(t *testing.T) {
	msg := mustTestMessage(t)
	fake := newFakeStellarRPC(t, msg)

	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)

	p, err := NewStellarExecutionAttemptPoller(rpcclient.NewClient(server.URL, nil), testStellarContractID, testLogger(t), time.Minute)
	require.NoError(t, err)

	const eventCount = 32
	msgID := mustMessageID(t, msg)

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

	attempts, err := p.GetExecutionAttempts(context.Background(), msg)
	require.NoError(t, err)
	assert.Len(t, attempts, eventCount, "no attempt may be lost to a concurrent cache update")
}

func mustMessageID(t *testing.T, msg protocol.Message) protocol.Bytes32 {
	t.Helper()
	id, err := msg.MessageID()
	require.NoError(t, err)
	return id
}
