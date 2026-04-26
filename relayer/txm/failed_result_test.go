package txm

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildFailedInvokeHostFunctionResultXDR(t *testing.T, code xdr.InvokeHostFunctionResultCode) string {
	t.Helper()

	invokeResult, err := xdr.NewInvokeHostFunctionResult(code, nil)
	require.NoError(t, err)
	opTr, err := xdr.NewOperationResultTr(xdr.OperationTypeInvokeHostFunction, invokeResult)
	require.NoError(t, err)
	opResult, err := xdr.NewOperationResult(xdr.OperationResultCodeOpInner, opTr)
	require.NoError(t, err)
	txResultInner, err := xdr.NewTransactionResultResult(xdr.TransactionResultCodeTxFailed, []xdr.OperationResult{opResult})
	require.NoError(t, err)
	txResult := xdr.TransactionResult{FeeCharged: 10_000, Result: txResultInner}
	b64, err := xdr.MarshalBase64(txResult)
	require.NoError(t, err)
	return b64
}

func buildFailedOperationResultXDR(t *testing.T, code xdr.OperationResultCode) string {
	t.Helper()

	opResult, err := xdr.NewOperationResult(code, nil)
	require.NoError(t, err)
	txResultInner, err := xdr.NewTransactionResultResult(xdr.TransactionResultCodeTxFailed, []xdr.OperationResult{opResult})
	require.NoError(t, err)
	txResult := xdr.TransactionResult{FeeCharged: 10_000, Result: txResultInner}
	b64, err := xdr.MarshalBase64(txResult)
	require.NoError(t, err)
	return b64
}

func TestClassifyFailedTransactionResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		resultXDR  string
		resultCode string
		retryable  bool
	}{
		{
			name:       "resource limit exceeded is retryable",
			resultXDR:  buildFailedInvokeHostFunctionResultXDR(t, xdr.InvokeHostFunctionResultCodeInvokeHostFunctionResourceLimitExceeded),
			resultCode: xdr.InvokeHostFunctionResultCodeInvokeHostFunctionResourceLimitExceeded.String(),
			retryable:  true,
		},
		{
			name:       "entry archived is retryable",
			resultXDR:  buildFailedInvokeHostFunctionResultXDR(t, xdr.InvokeHostFunctionResultCodeInvokeHostFunctionEntryArchived),
			resultCode: xdr.InvokeHostFunctionResultCodeInvokeHostFunctionEntryArchived.String(),
			retryable:  true,
		},
		{
			name:       "insufficient refundable fee is retryable",
			resultXDR:  buildFailedInvokeHostFunctionResultXDR(t, xdr.InvokeHostFunctionResultCodeInvokeHostFunctionInsufficientRefundableFee),
			resultCode: xdr.InvokeHostFunctionResultCodeInvokeHostFunctionInsufficientRefundableFee.String(),
			retryable:  true,
		},
		{
			name:       "contract trap is terminal",
			resultXDR:  buildFailedInvokeHostFunctionResultXDR(t, xdr.InvokeHostFunctionResultCodeInvokeHostFunctionTrapped),
			resultCode: xdr.InvokeHostFunctionResultCodeInvokeHostFunctionTrapped.String(),
			retryable:  false,
		},
		{
			name:       "malformed invocation is terminal",
			resultXDR:  buildFailedInvokeHostFunctionResultXDR(t, xdr.InvokeHostFunctionResultCodeInvokeHostFunctionMalformed),
			resultCode: xdr.InvokeHostFunctionResultCodeInvokeHostFunctionMalformed.String(),
			retryable:  false,
		},
		{
			name:       "generic exceeded work limit is retryable",
			resultXDR:  buildFailedOperationResultXDR(t, xdr.OperationResultCodeOpExceededWorkLimit),
			resultCode: xdr.OperationResultCodeOpExceededWorkLimit.String(),
			retryable:  true,
		},
		{
			name:       "missing result xdr is terminal revert",
			resultXDR:  "",
			resultCode: ErrorReasonRevert,
			retryable:  false,
		},
		{
			name:       "invalid result xdr is terminal decode error",
			resultXDR:  "not-xdr",
			resultCode: "revert_decode_error",
			retryable:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			classification := classifyFailedTransactionResult(tt.resultXDR)
			assert.Equal(t, tt.resultCode, classification.resultCode)
			assert.Equal(t, tt.retryable, classification.retryable)
		})
	}
}
