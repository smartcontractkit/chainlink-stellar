package receiver

import (
	"context"
	"fmt"

	"github.com/smartcontractkit/chainlink-stellar/deployment"
)

// DeployReceiver deploys the CRE test receiver from the given compiled WASM bytes.
func DeployReceiver(ctx context.Context, deployer *deployment.Deployer, wasm []byte, salt [32]byte) (string, error) {
	if deployer == nil {
		return "", fmt.Errorf("deployer is nil")
	}
	if len(wasm) == 0 {
		return "", fmt.Errorf("receiver wasm is empty")
	}
	contractID, err := deployer.DeployContractBytes(ctx, wasm, salt)
	if err != nil {
		return "", fmt.Errorf("deploy receiver wasm: %w", err)
	}
	return contractID, nil
}

// ReportCount queries how many reports the receiver has recorded.
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
