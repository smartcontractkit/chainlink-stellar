package txm

import (
	"context"
	"errors"
	"fmt"

	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
)

var _ bindings.Invoker = (*InvokerAdapter)(nil)

// InvokerTxManager is the subset of StellarTxm used by InvokerAdapter.
type InvokerTxManager interface {
	EnqueueAndWait(ctx context.Context, req TxRequest) (*TxResult, error)
	Simulate(ctx context.Context, req TxRequest) (protocolrpc.SimulateTransactionResponse, error)
}

// InvokerAdapterOption customizes InvokerAdapter behavior.
type InvokerAdapterOption func(*InvokerAdapter)

// WithInvokerFromAddress sets the account used as the source for contract
// invocations. If unset, StellarTxm falls back to its first keystore account.
func WithInvokerFromAddress(fromAddress string) InvokerAdapterOption {
	return func(a *InvokerAdapter) {
		a.fromAddress = fromAddress
	}
}

// WithInvokerLedgerBoundsOffset overrides the TXM default ledger bounds for
// invocations submitted through this adapter.
func WithInvokerLedgerBoundsOffset(offset uint32) InvokerAdapterOption {
	return func(a *InvokerAdapter) {
		a.ledgerBoundsOffset = offset
	}
}

// InvokerAdapter implements bindings.Invoker by routing contract work through
// the Stellar TXM: InvokeContract → EnqueueAndWait (async pipeline, signing,
// fees, retries), SimulateContract → Simulate, GetEvents → RPC (no sequence).
//
// Generated binding clients (e.g. routerbindings.NewRouterClient) take a
// bindings.Invoker — they do not reference InvokerAdapter directly. This type
// is one Invoker implementation for relayer/Chainlink-style TXM usage; other
// stacks may use a different Invoker (e.g. deployment.Deployer in ccv/devenv,
// which signs and submits via RPC without the TXM).
type InvokerAdapter struct {
	txm                InvokerTxManager
	getClient          func(context.Context) (RPCClient, error)
	fromAddress        string
	ledgerBoundsOffset uint32
}

// NewInvokerAdapter creates a bindings.Invoker backed by the Stellar TXM.
func NewInvokerAdapter(
	txm InvokerTxManager,
	getClient func(context.Context) (RPCClient, error),
	opts ...InvokerAdapterOption,
) (*InvokerAdapter, error) {
	if txm == nil {
		return nil, errors.New("txm is required")
	}
	if getClient == nil {
		return nil, errors.New("getClient is required")
	}

	adapter := &InvokerAdapter{
		txm:       txm,
		getClient: getClient,
	}
	for _, opt := range opts {
		opt(adapter)
	}

	return adapter, nil
}

// InvokeContract submits a Soroban contract invocation through the TXM and
// blocks until the transaction reaches a terminal state (or ctx is cancelled),
// then returns the Soroban return value from the confirmed transaction metadata.
func (a *InvokerAdapter) InvokeContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	op, err := BuildInvokeContractOperation(contractID, functionName, args, a.fromAddress)
	if err != nil {
		return nil, err
	}

	result, err := a.txm.EnqueueAndWait(ctx, TxRequest{
		FromAddress:        a.fromAddress,
		Operations:         []txnbuild.Operation{op},
		LedgerBoundsOffset: a.ledgerBoundsOffset,
	})
	if err != nil {
		return nil, fmt.Errorf("invoke %s.%s: %w", contractID, functionName, err)
	}
	if result == nil {
		return nil, fmt.Errorf("invoke %s.%s: nil transaction result", contractID, functionName)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("invoke %s.%s tx %s failed: %w", contractID, functionName, result.ID, result.Error)
	}
	if result.Status != commontypes.Finalized {
		return nil, fmt.Errorf("invoke %s.%s tx %s ended with status %v", contractID, functionName, result.ID, result.Status)
	}

	return extractReturnValueFromResultMetaXDR(result.ResultMetaXDR)
}

// SimulateContract performs a read-only Soroban simulation through the TXM and
// returns the simulated Soroban return value.
func (a *InvokerAdapter) SimulateContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	op, err := BuildInvokeContractOperation(contractID, functionName, args, a.fromAddress)
	if err != nil {
		return nil, err
	}

	result, err := a.txm.Simulate(ctx, TxRequest{
		FromAddress:        a.fromAddress,
		Operations:         []txnbuild.Operation{op},
		LedgerBoundsOffset: a.ledgerBoundsOffset,
	})
	if err != nil {
		return nil, fmt.Errorf("simulate %s.%s: %w", contractID, functionName, err)
	}
	if result.RestorePreamble != nil {
		return nil, fmt.Errorf("simulate %s.%s: restore required before read-only simulation can return a value", contractID, functionName)
	}

	return extractReturnValueFromSimulationResult(result)
}

// GetEvents reads contract events directly from RPC. TXM is intentionally not
// involved because event reads do not consume sequence numbers or fees.
func (a *InvokerAdapter) GetEvents(ctx context.Context, contractID string, startLedger uint32, topics []string) ([]protocolrpc.EventInfo, error) {
	client, err := a.getClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("get events client: %w", err)
	}

	var topicFilter protocolrpc.TopicFilter
	for _, topic := range topics {
		topicFilter = append(topicFilter, protocolrpc.SegmentFilter{ScVal: scval.SymbolToScValPtr(topic)})
	}
	zeroOrMore := protocolrpc.WildCardZeroOrMore
	topicFilter = append(topicFilter, protocolrpc.SegmentFilter{Wildcard: &zeroOrMore})

	resp, err := client.GetEvents(ctx, protocolrpc.GetEventsRequest{
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

// BuildInvokeContractOperation builds an InvokeHostFunction operation for a Soroban contract
// call. It is shared by the TXM-backed invoker and the relayer's SubmitTransaction path.
func BuildInvokeContractOperation(contractID string, functionName string, args []xdr.ScVal, fromAddress string) (*txnbuild.InvokeHostFunction, error) {
	if contractID == "" {
		return nil, errors.New("contractID is required")
	}
	if functionName == "" {
		return nil, errors.New("functionName is required")
	}

	contractBytes, err := strkey.Decode(strkey.VersionByteContract, contractID)
	if err != nil {
		return nil, fmt.Errorf("decode contract ID %q: %w", contractID, err)
	}

	contractAddr := scval.BuildContractScAddress(contractBytes)
	if contractAddr == nil {
		return nil, fmt.Errorf("build contract address %q", contractID)
	}

	return &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &xdr.InvokeContractArgs{
				ContractAddress: *contractAddr,
				FunctionName:    xdr.ScSymbol(functionName),
				Args:            args,
			},
		},
		SourceAccount: fromAddress,
	}, nil
}

func extractReturnValueFromResultMetaXDR(resultMetaXDR string) (*xdr.ScVal, error) {
	if resultMetaXDR == "" {
		return nil, nil
	}

	var meta xdr.TransactionMeta
	if err := xdr.SafeUnmarshalBase64(resultMetaXDR, &meta); err != nil {
		return nil, fmt.Errorf("decode result meta XDR: %w", err)
	}

	return extractReturnValueFromTransactionMeta(&meta)
}

func extractReturnValueFromTransactionMeta(meta *xdr.TransactionMeta) (*xdr.ScVal, error) {
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

func extractReturnValueFromSimulationResult(result protocolrpc.SimulateTransactionResponse) (*xdr.ScVal, error) {
	if len(result.Results) == 0 || result.Results[0].ReturnValueXDR == nil || *result.Results[0].ReturnValueXDR == "" {
		return nil, nil
	}

	var scVal xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(*result.Results[0].ReturnValueXDR, &scVal); err != nil {
		return nil, fmt.Errorf("decode simulation return value XDR: %w", err)
	}
	return &scVal, nil
}
