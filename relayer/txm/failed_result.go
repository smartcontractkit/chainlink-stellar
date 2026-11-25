package txm

import (
	"github.com/stellar/go-stellar-sdk/xdr"
)

type failedTxClassification struct {
	resultCode ErrorReason
	retryable  bool
	feeCharged int64 // FeeCharged from the result; zero when the result is missing or undecodable
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
	feeCharged := int64(txResult.FeeCharged)
	results, ok := txResult.Result.GetResults()
	if !ok || len(results) == 0 {
		return failedTxClassification{resultCode: resultCode, feeCharged: feeCharged}
	}

	opResult := results[0]
	if opResult.Code != xdr.OperationResultCodeOpInner {
		return failedTxClassification{
			resultCode: ErrorReason(opResult.Code.String()),
			retryable:  opResult.Code == xdr.OperationResultCodeOpExceededWorkLimit,
			feeCharged: feeCharged,
		}
	}

	tr, ok := opResult.GetTr()
	if !ok {
		return failedTxClassification{resultCode: ErrorReason(opResult.Code.String()), feeCharged: feeCharged}
	}

	if invokeResult, ok := tr.GetInvokeHostFunctionResult(); ok {
		return failedTxClassification{
			resultCode: ErrorReason(invokeResult.Code.String()),
			retryable:  isRetryableInvokeHostFunctionResult(invokeResult.Code),
			feeCharged: feeCharged,
		}
	}

	return failedTxClassification{resultCode: ErrorReason(tr.Type.String()), feeCharged: feeCharged}
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
