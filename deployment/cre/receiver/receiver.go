// Package receiver deploys the minimal CRE test receiver (on_report recorder) used
// by Local-CRE write tests, and queries its recorded state for assertions.
//
// The compiled receiver WASM is embedded so the chainlink system-tests write test
// can deploy it via a pinned Go module dependency with no soroban toolchain — same
// pattern as the forwarder helper.
package receiver

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/smartcontractkit/chainlink-stellar/deployment"
)

// receiverWASM is the compiled cre-receiver-example Soroban contract
// (contracts/examples/cre_receiver). Rebuild + recommit when that source changes.
//
//go:embed artifacts/cre_receiver_example.wasm
var receiverWASM []byte

// WASM returns the embedded compiled receiver contract bytes.
func WASM() []byte { return receiverWASM }

// DeployReceiver deploys the CRE test receiver and returns its C-address. The
// receiver needs no initialization; the forwarder invokes its on_report(metadata,
// payload) and it records the last payload + a report count.
func DeployReceiver(ctx context.Context, deployer *deployment.Deployer, salt [32]byte) (string, error) {
	if deployer == nil {
		return "", fmt.Errorf("deployer is nil")
	}
	contractID, err := deployer.DeployContractBytes(ctx, receiverWASM, salt)
	if err != nil {
		return "", fmt.Errorf("deploy receiver wasm: %w", err)
	}
	return contractID, nil
}

// ReportCount queries how many reports the receiver has recorded (report_count()),
// via a read-only simulate. Use it to assert a write was delivered on-chain.
func ReportCount(ctx context.Context, deployer *deployment.Deployer, contractID string) (uint32, error) {
	if deployer == nil {
		return 0, fmt.Errorf("deployer is nil")
	}
	sv, err := deployer.SimulateContract(ctx, contractID, "report_count", nil)
	if err != nil {
		return 0, fmt.Errorf("simulate report_count on %s: %w", contractID, err)
	}
	if sv == nil {
		return 0, fmt.Errorf("nil return from report_count on %s", contractID)
	}
	u32, ok := sv.GetU32()
	if !ok {
		return 0, fmt.Errorf("report_count did not return u32 (got scval type %v)", sv.Type)
	}
	return uint32(u32), nil
}
