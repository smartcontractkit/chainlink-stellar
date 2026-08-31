package txm

import (
	"github.com/stellar/go-stellar-sdk/xdr"
)

type failedTxClassification struct {
	resultCode ErrorReason
	retryable  bool
}

func classifyFailedTransactionResult(resultXDR string) failedTxClassification {
	if resultXDR == "" {
		return failedTxClassification{resultCode: ErrorReasonRevert}
	}

	var txResult xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(resultXDR, &txResult); err != nil {
		return failedTxClassification{resultCode: ErrorReasonRevertDecode}
	}

	resultCode := ErrorReason(txResult.Result.Code.String())
	results, ok := txResult.Result.GetResults()
	if !ok || len(results) == 0 {
		return failedTxClassification{resultCode: resultCode}
	}

	opResult := results[0]
	if opResult.Code != xdr.OperationResultCodeOpInner {
		return failedTxClassification{
			resultCode: ErrorReason(opResult.Code.String()),
			retryable:  opResult.Code == xdr.OperationResultCodeOpExceededWorkLimit,
		}
	}

	tr, ok := opResult.GetTr()
	if !ok {
		return failedTxClassification{resultCode: ErrorReason(opResult.Code.String())}
	}

	if invokeResult, ok := tr.GetInvokeHostFunctionResult(); ok {
		return failedTxClassification{
			resultCode: ErrorReason(invokeResult.Code.String()),
			retryable:  isRetryableInvokeHostFunctionResult(invokeResult.Code),
		}
	}

	return failedTxClassification{resultCode: ErrorReason(tr.Type.String())}
}

func isRetryableInvokeHostFunctionResult(code xdr.InvokeHostFunctionResultCode) bool {
	switch code {
	case xdr.InvokeHostFunctionResultCodeInvokeHostFunctionResourceLimitExceeded,
		xdr.InvokeHostFunctionResultCodeInvokeHostFunctionEntryArchived,
		xdr.InvokeHostFunctionResultCodeInvokeHostFunctionInsufficientRefundableFee:
		return true
	default:
		return false
	}
}
