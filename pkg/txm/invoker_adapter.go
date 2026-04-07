package txm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync/atomic"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

var _ bindings.Invoker = (*InvokerAdapter)(nil)

// InvokerAdapter implements bindings.Invoker by delegating to a TxManager.
// This allows the existing generated contract clients (OffRampClient,
// OnRampClient, etc.) to work unchanged while benefiting from TXM's
// lifecycle management, retry logic, and transaction tracking.
type InvokerAdapter struct {
	txm TxManager
	rpc ccvclient.RPCClient
}

// NewInvokerAdapter creates an InvokerAdapter over a TxManager.
// The rpc client is used for SimulateContract (read-only) and GetEvents.
func NewInvokerAdapter(txm TxManager, rpc ccvclient.RPCClient) *InvokerAdapter {
	return &InvokerAdapter{txm: txm, rpc: rpc}
}

// InvokeContract invokes a Soroban contract function via the TXM.
// It blocks until the transaction is confirmed or fails.
func (a *InvokerAdapter) InvokeContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	txID := generateTxID(contractID, functionName)

	result, err := a.txm.EnqueueAndWait(ctx, TxRequest{
		ID:           txID,
		ContractID:   contractID,
		FunctionName: functionName,
		Args:         args,
	})
	if err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, result.Error
	}

	return extractReturnValue(result.ResultMeta)
}

// SimulateContract performs a read-only simulation without submitting.
// Delegates to TXM with SimulateOnly flag; the simulation runs during the
// broadcast phase but the transaction is not submitted to the network.
func (a *InvokerAdapter) SimulateContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	txID := generateTxID(contractID, functionName)

	result, err := a.txm.EnqueueAndWait(ctx, TxRequest{
		ID:           txID,
		ContractID:   contractID,
		FunctionName: functionName,
		Args:         args,
		SimulateOnly: true,
	})
	if err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, result.Error
	}

	return extractReturnValue(result.ResultMeta)
}

// GetEvents returns contract events. Delegates directly to the RPC client
// since event queries don't involve transactions.
func (a *InvokerAdapter) GetEvents(ctx context.Context, contractID string, startLedger uint32, topics []string) ([]protocolrpc.EventInfo, error) {
	var topicScVals []*xdr.ScVal
	for _, topic := range topics {
		topicScVals = append(topicScVals, scval.SymbolToScValPtr(topic))
	}

	zeroOrMore := protocolrpc.WildCardZeroOrMore
	topicFilter := protocolrpc.TopicFilter{}
	for _, sv := range topicScVals {
		topicFilter = append(topicFilter, protocolrpc.SegmentFilter{ScVal: sv})
	}
	topicFilter = append(topicFilter, protocolrpc.SegmentFilter{Wildcard: &zeroOrMore})

	resp, err := a.rpc.GetEvents(ctx, protocolrpc.GetEventsRequest{
		StartLedger: startLedger,
		Filters: []protocolrpc.EventFilter{
			{
				EventType:   protocolrpc.EventTypeSet{protocolrpc.EventTypeContract: nil},
				ContractIDs: []string{contractID},
				Topics:      []protocolrpc.TopicFilter{topicFilter},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get events: %w", err)
	}

	return resp.Events, nil
}

var txCounter atomic.Uint64

// generateTxID creates a unique idempotency key for each invocation.
func generateTxID(contractID, functionName string) string {
	counter := txCounter.Add(1)
	input := fmt.Sprintf("%s:%s:%d", contractID, functionName, counter)
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:16])
}

// extractReturnValue extracts the Soroban return value from transaction meta.
func extractReturnValue(meta *xdr.TransactionMeta) (*xdr.ScVal, error) {
	if meta == nil {
		return nil, nil
	}

	switch meta.V {
	case 4:
		v := meta.MustV4()
		if v.SorobanMeta == nil {
			return nil, nil
		}
		return v.SorobanMeta.ReturnValue, nil
	case 3:
		v := meta.MustV3()
		if v.SorobanMeta == nil {
			return nil, nil
		}
		return &v.SorobanMeta.ReturnValue, nil
	default:
		return nil, fmt.Errorf("unsupported transaction meta version: %d", meta.V)
	}
}
