package stellarutil

import (
	"testing"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
)

// TestNoExecutionExecutorStrkey pins the sentinel strkey to the same encoding
// the Rust constant NO_EXECUTION_STRKEY (contracts/common/message/src/lib.rs)
// carries: strkey(contract, 0xEBa517d2 + 28 zero bytes).
func TestNoExecutionExecutorStrkey(t *testing.T) {
	raw := append([]byte{0xeb, 0xa5, 0x17, 0xd2}, make([]byte, 28)...)
	want, err := scval.BytesToContractStrkey(raw)
	if err != nil {
		t.Fatalf("BytesToContractStrkey: %v", err)
	}
	if NoExecutionExecutorStrkey != want {
		t.Fatalf("sentinel strkey drift: const %q, derived %q — update both this constant and the Rust NO_EXECUTION_STRKEY", NoExecutionExecutorStrkey, want)
	}
}
