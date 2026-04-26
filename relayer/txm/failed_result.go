package txm

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/xdr"
)

type failedTxClassification struct {
	resultCode string
	retryable  bool
}

func classifyFailedTransactionResult(resultXDR string) failedTxClassification {
	if resultXDR == "" {
		return failedTxClassification{resultCode: ErrorReasonRevert}
	}

	var txResult xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(resultXDR, &txResult); err != nil {
		return failedTxClassification{resultCode: fmt.Sprintf("%s_decode_error", ErrorReasonRevert)}
	}

	resultCode := txResult.Result.Code.String()
	results, ok := txResult.Result.GetResults()
	if !ok || len(results) == 0 {
		return failedTxClassification{resultCode: resultCode}
	}

	for _, opResult := range results {
		if opResult.Code != xdr.OperationResultCodeOpInner {
			return failedTxClassification{
				resultCode: opResult.Code.String(),
				retryable:  opResult.Code == xdr.OperationResultCodeOpExceededWorkLimit,
			}
		}

		tr, ok := opResult.GetTr()
		if !ok {
			return failedTxClassification{resultCode: opResult.Code.String()}
		}

		if invokeResult, ok := tr.GetInvokeHostFunctionResult(); ok {
			return failedTxClassification{
				resultCode: invokeResult.Code.String(),
				retryable:  isRetryableInvokeHostFunctionResult(invokeResult.Code),
			}
		}

		return failedTxClassification{resultCode: tr.Type.String()}
	}

	return failedTxClassification{resultCode: resultCode}
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
