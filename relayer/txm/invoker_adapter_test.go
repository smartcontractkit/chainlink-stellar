package txm

import (
	"context"
	"fmt"
	"testing"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-stellar/relayer/config"
)

type fakeInvokerTxm struct {
	enqueueReq    TxRequest
	enqueueResult *TxResult
	enqueueErr    error

	simulateReq  TxRequest
	simulateResp protocolrpc.SimulateTransactionResponse
	simulateErr  error
}

func (f *fakeInvokerTxm) EnqueueAndWait(_ context.Context, req TxRequest) (*TxResult, error) {
	f.enqueueReq = req
	return f.enqueueResult, f.enqueueErr
}

func (f *fakeInvokerTxm) Simulate(_ context.Context, req TxRequest) (protocolrpc.SimulateTransactionResponse, error) {
	f.simulateReq = req
	return f.simulateResp, f.simulateErr
}

func TestInvokerAdapter_InvokeContract(t *testing.T) {
	t.Parallel()

	contractID := testContractStrkey(t)
	returnValue := testU32ScVal(42)
	fake := &fakeInvokerTxm{
		enqueueResult: &TxResult{
			ID:            "tx-1",
			Status:        commontypes.Finalized,
			ResultMetaXDR: testTransactionMetaV3B64(t, returnValue),
		},
	}
	adapter, err := NewInvokerAdapter(
		fake,
		func(context.Context) (RPCClient, error) { return newTestClient(&mockRPCClient{}), nil },
		WithInvokerFromAddress(testAddress),
		WithInvokerLedgerBoundsOffset(12),
	)
	require.NoError(t, err)

	result, err := adapter.InvokeContract(t.Context(), contractID, "increment", []xdr.ScVal{returnValue})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, returnValue, *result)

	assert.Equal(t, testAddress, fake.enqueueReq.FromAddress)
	assert.Equal(t, uint32(12), fake.enqueueReq.LedgerBoundsOffset)
	require.Len(t, fake.enqueueReq.Operations, 1)
	assertInvokeOperation(t, fake.enqueueReq.Operations[0], contractID, "increment", []xdr.ScVal{returnValue}, testAddress)
}

func TestInvokerAdapter_InvokeContractReturnsTxFailure(t *testing.T) {
	t.Parallel()

	fake := &fakeInvokerTxm{
		enqueueResult: &TxResult{
			ID:     "tx-1",
			Status: commontypes.Failed,
			Error:  fmt.Errorf("transaction result: trapped"),
		},
	}
	adapter, err := NewInvokerAdapter(fake, func(context.Context) (RPCClient, error) { return newTestClient(&mockRPCClient{}), nil })
	require.NoError(t, err)

	_, err = adapter.InvokeContract(t.Context(), testContractStrkey(t), "execute", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tx tx-1 failed")
}

func TestInvokerAdapter_SimulateContract(t *testing.T) {
	t.Parallel()

	contractID := testContractStrkey(t)
	returnValue := testU32ScVal(7)
	returnValueXDR := testScValB64(t, returnValue)
	fake := &fakeInvokerTxm{
		simulateResp: protocolrpc.SimulateTransactionResponse{
			Results: []protocolrpc.SimulateHostFunctionResult{
				{ReturnValueXDR: &returnValueXDR},
			},
		},
	}
	adapter, err := NewInvokerAdapter(
		fake,
		func(context.Context) (RPCClient, error) { return newTestClient(&mockRPCClient{}), nil },
		WithInvokerFromAddress(testAddress),
	)
	require.NoError(t, err)

	result, err := adapter.SimulateContract(t.Context(), contractID, "get_count", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, returnValue, *result)

	require.Len(t, fake.simulateReq.Operations, 1)
	assertInvokeOperation(t, fake.simulateReq.Operations[0], contractID, "get_count", nil, testAddress)
}

func TestInvokerAdapter_SimulateContractRestoreRequired(t *testing.T) {
	t.Parallel()

	fake := &fakeInvokerTxm{
		simulateResp: protocolrpc.SimulateTransactionResponse{
			RestorePreamble: &protocolrpc.RestorePreamble{},
		},
	}
	adapter, err := NewInvokerAdapter(fake, func(context.Context) (RPCClient, error) { return newTestClient(&mockRPCClient{}), nil })
	require.NoError(t, err)

	_, err = adapter.SimulateContract(t.Context(), testContractStrkey(t), "get_count", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restore required")
}

func TestInvokerAdapter_GetEvents(t *testing.T) {
	t.Parallel()

	contractID := testContractStrkey(t)
	expected := []protocolrpc.EventInfo{{Ledger: 123, ContractID: contractID, ID: "evt-1"}}
	var captured protocolrpc.GetEventsRequest
	mock := &mockRPCClient{
		getEventsHook: func(req protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error) {
			captured = req
			return protocolrpc.GetEventsResponse{Events: expected}, nil
		},
	}
	adapter, err := NewInvokerAdapter(&fakeInvokerTxm{}, func(context.Context) (RPCClient, error) { return newTestClient(mock), nil })
	require.NoError(t, err)

	events, err := adapter.GetEvents(t.Context(), contractID, 99, []string{"MessageExecuted"})
	require.NoError(t, err)
	assert.Equal(t, expected, events)
	assert.Equal(t, uint32(99), captured.StartLedger)
	require.Len(t, captured.Filters, 1)
	assert.Equal(t, []string{contractID}, captured.Filters[0].ContractIDs)
	require.Len(t, captured.Filters[0].Topics, 1)
	assert.Len(t, captured.Filters[0].Topics[0], 2)
}

func TestStellarTxm_SimulateDefaultsFromAddress(t *testing.T) {
	t.Parallel()

	mock := &mockRPCClient{
		getLatestLedgerResp: protocolrpc.GetLatestLedgerResponse{Sequence: 9},
		simulateResp:        protocolrpc.SimulateTransactionResponse{MinResourceFee: 5},
	}
	txm, err := New(logger.Test(t), &mockKeystore{}, config.Config{}, newTestGetClient(mock), chainsel.STELLAR_TESTNET.ChainID)
	require.NoError(t, err)

	res, err := txm.Simulate(t.Context(), TxRequest{
		Operations: []txnbuild.Operation{testInvokeNoopOp()},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(5), res.MinResourceFee)
}

func TestInvokerAdapterExtractReturnValueFromMetaV4(t *testing.T) {
	t.Parallel()

	returnValue := testU32ScVal(99)
	meta, err := xdr.NewTransactionMeta(4, xdr.TransactionMetaV4{
		SorobanMeta: &xdr.SorobanTransactionMetaV2{
			ReturnValue: &returnValue,
		},
	})
	require.NoError(t, err)
	metaB64, err := xdr.MarshalBase64(meta)
	require.NoError(t, err)

	got, err := extractReturnValueFromResultMetaXDR(metaB64)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, returnValue, *got)
}

func assertInvokeOperation(t *testing.T, op txnbuild.Operation, contractID string, functionName string, args []xdr.ScVal, sourceAccount string) {
	t.Helper()

	ihf, ok := op.(*txnbuild.InvokeHostFunction)
	require.True(t, ok)
	assert.Equal(t, sourceAccount, ihf.SourceAccount)
	require.NotNil(t, ihf.HostFunction.InvokeContract)
	assert.Equal(t, xdr.HostFunctionTypeHostFunctionTypeInvokeContract, ihf.HostFunction.Type)
	assert.Equal(t, xdr.ScSymbol(functionName), ihf.HostFunction.InvokeContract.FunctionName)
	assert.Equal(t, args, ihf.HostFunction.InvokeContract.Args)

	expectedBytes, err := strkey.Decode(strkey.VersionByteContract, contractID)
	require.NoError(t, err)
	var expectedID xdr.ContractId
	copy(expectedID[:], expectedBytes)
	require.NotNil(t, ihf.HostFunction.InvokeContract.ContractAddress.ContractId)
	assert.Equal(t, expectedID, *ihf.HostFunction.InvokeContract.ContractAddress.ContractId)
}

func testContractStrkey(t *testing.T) string {
	t.Helper()

	contractID := make([]byte, 32)
	for i := range contractID {
		contractID[i] = byte(i + 1)
	}
	encoded, err := strkey.Encode(strkey.VersionByteContract, contractID)
	require.NoError(t, err)
	return encoded
}

func testU32ScVal(v uint32) xdr.ScVal {
	xv := xdr.Uint32(v)
	return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &xv}
}

func testScValB64(t *testing.T, val xdr.ScVal) string {
	t.Helper()

	b64, err := xdr.MarshalBase64(val)
	require.NoError(t, err)
	return b64
}

func testTransactionMetaV3B64(t *testing.T, returnValue xdr.ScVal) string {
	t.Helper()

	meta, err := xdr.NewTransactionMeta(3, xdr.TransactionMetaV3{
		SorobanMeta: &xdr.SorobanTransactionMeta{
			ReturnValue: returnValue,
		},
	})
	require.NoError(t, err)
	b64, err := xdr.MarshalBase64(meta)
	require.NoError(t, err)
	return b64
}
