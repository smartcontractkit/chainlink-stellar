package deployment

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

// Real resultXDRs from CI integration-test runs where a WASM upload raced a
// sibling upload of the same wasm hash: the transaction was included but its
// execution exceeded the instruction budget simulated before the sibling
// landed. Both decode to txFailed / invoke-host-function resource-limit-exceeded.
var resourceLimitExceededResultXDRs = []string{
	// TestAdvancedPoolHooksOutboundSend, offramp upload
	"AAAAAAAkSKD/////AAAAAQAAAAAAAAAY/////QAAAAA=",
	// TestRmnRemote, rmn_remote upload
	"AAAAAAAJuNz/////AAAAAQAAAAAAAAAY/////QAAAAA=",
	// TestOnRampFeeDistribution, committee-verifier upload
	"AAAAAAAW9DH/////AAAAAQAAAAAAAAAY/////QAAAAA=",
}

func TestIsInvokeHostFunctionResourceLimitExceeded(t *testing.T) {
	for _, resultXDR := range resourceLimitExceededResultXDRs {
		if !isInvokeHostFunctionResourceLimitExceeded(resultXDR) {
			t.Errorf("isInvokeHostFunctionResourceLimitExceeded(%q) = false, want true", resultXDR)
		}
	}

	for name, resultXDR := range map[string]string{
		"empty":             "",
		"not base64 XDR":    "garbage",
		"truncated padding": "AAAAAAAkSKD/////AAAAAQAAAAAAAAAY/////QAAAA",
	} {
		if isInvokeHostFunctionResourceLimitExceeded(resultXDR) {
			t.Errorf("isInvokeHostFunctionResourceLimitExceeded(%q) = true for %q input, want false", resultXDR, name)
		}
	}

	// A successful invoke-host-function result must not be retryable:
	// txSuccess, one op, invoke-host-function success.
	if isInvokeHostFunctionResourceLimitExceeded("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==") {
		t.Error("isInvokeHostFunctionResourceLimitExceeded(success result) = true, want false")
	}
}

// Real resultXDRs from CI integration-test runs where a WASM upload raced a
// sibling upload of the same wasm hash: the transaction was rejected at
// inclusion (never executed — no diagnostics) with txInsufficientFee, the
// fee-level manifestation of the same drift the resultXDRs above capture at
// the instruction level.
var insufficientFeeRejectedResultXDRs = []string{
	// TestAdvancedPoolHooksOutboundSend, offramp upload
	"AAAAAASjbwr////3AAAAAA==",
}

func TestIsTxInsufficientFee(t *testing.T) {
	for _, resultXDR := range insufficientFeeRejectedResultXDRs {
		if !isTxInsufficientFee(resultXDR) {
			t.Errorf("isTxInsufficientFee(%q) = false, want true", resultXDR)
		}
	}

	for name, resultXDR := range map[string]string{
		"empty":          "",
		"not base64 XDR": "garbage",
		"txSuccess":      "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==",
		"included-but-failed resource-limit (txFAILED/-3, not a tx-level rejection)": resourceLimitExceededResultXDRs[0],
		"truncated padding": "AAAAAASjbwr////3AAAAAA",
	} {
		if isTxInsufficientFee(resultXDR) {
			t.Errorf("isTxInsufficientFee(%q) = true for %q input, want false", resultXDR, name)
		}
	}
}

// declaredInstructions decodes a submitted transaction envelope and returns
// its declared Soroban instruction budget.
func declaredInstructions(t *testing.T, txB64 string) xdr.Uint32 {
	t.Helper()
	var env xdr.TransactionEnvelope
	require.NoError(t, xdr.SafeUnmarshalBase64(txB64, &env))
	return env.MustV1().Tx.Ext.MustSorobanData().Resources.Instructions
}

// TestBuildAndSubmitTransaction_BudgetExceededStickyAcrossOverload is the
// mixed-failure regression: attempt 1 executes and exceeds its declared
// budget; attempt 2 is refused with TRY_AGAIN_LATER (never executed). The
// overload must not clear the known shortfall — attempt 3 must still scale
// and take the final-attempt raise to the network instruction cap.
func TestBuildAndSubmitTransaction_BudgetExceededStickyAcrossOverload(t *testing.T) {
	shortenOverloadBackoff(t)

	sim := func(t *testing.T) protocolrpc.SimulateTransactionResponse {
		t.Helper()
		dataXDR, err := xdr.MarshalBase64(xdr.SorobanTransactionData{
			Resources:   xdr.SorobanResources{Instructions: 22_000_000},
			ResourceFee: 1_000_000,
		})
		require.NoError(t, err)
		return protocolrpc.SimulateTransactionResponse{
			TransactionDataXDR: dataXDR,
			MinResourceFee:     1_000_000,
		}
	}

	var declared []xdr.Uint32
	var sends atomic.Int32
	seq := testInitialSeq
	mock := &mockRPC{
		SimulateTransactionFn: func(_ context.Context, _ protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
			return sim(t), nil
		},
		SendTransactionFn: func(_ context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			n := sends.Add(1)
			declared = append(declared, declaredInstructions(t, req.Transaction))
			if n == 2 {
				return protocolrpc.SendTransactionResponse{Status: "TRY_AGAIN_LATER"}, nil
			}
			return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: fmt.Sprintf("hash-%d", n)}, nil
		},
		GetTransactionFn: func(_ context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
			if req.Hash == "hash-1" {
				return protocolrpc.GetTransactionResponse{
					TransactionDetails: protocolrpc.TransactionDetails{
						Status:    "FAILED",
						ResultXDR: resourceLimitExceededResultXDRs[0],
					},
				}, nil
			}
			return successGetTxResponse(t), nil
		},
		GetLedgerEntriesFn: func(_ context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
			return networkCapsLedgerEntries(t, req, &seq), nil
		},
	}

	d := newTestDeployer(t, mock)
	op := testInvokeOp(d.signer.Address())
	src, err := d.getSourceAccount(context.Background())
	require.NoError(t, err)

	_, err = d.buildAndSubmitTransaction(context.Background(), src, op)
	require.NoError(t, err)
	require.EqualValues(t, 3, sends.Load())
	require.Len(t, declared, 3)
	require.Equal(t, xdr.Uint32(22_000_000), declared[0], "attempt 1: the simulated budget")
	require.Equal(t, xdr.Uint32(44_000_000), declared[1], "attempt 2: x2 after the budget failure")
	require.Equal(t, xdr.Uint32(100_000_000), declared[2], "attempt 3: overload must not clear the shortfall — raise to the cap")
}
