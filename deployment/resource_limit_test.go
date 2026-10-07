package deployment

import "testing"

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
