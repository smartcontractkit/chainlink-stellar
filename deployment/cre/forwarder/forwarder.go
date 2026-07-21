package forwarder

import (
	"context"
	"fmt"

	crebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/cre"

	"github.com/smartcontractkit/chainlink-stellar/deployment"
)

// DeployForwarder uploads + instantiates the Forwarder.
func DeployForwarder(ctx context.Context, deployer *deployment.Deployer, owner string, wasm []byte, salt [32]byte) (string, error) {
	if deployer == nil {
		return "", fmt.Errorf("deployer is nil")
	}
	if len(wasm) == 0 {
		return "", fmt.Errorf("forwarder wasm is empty")
	}
	contractID, err := deployer.DeployContractBytes(ctx, wasm, salt)
	if err != nil {
		return "", fmt.Errorf("deploy forwarder wasm: %w", err)
	}
	client := crebindings.NewForwarderClient(deployer, contractID)
	if err := client.Initialize(ctx, owner); err != nil {
		return "", fmt.Errorf("initialize forwarder %s (owner %s): %w", contractID, owner, err)
	}
	return contractID, nil
}

// ConfigureForwarder sets the DON signer configuration on an already-deployed forwarder.
func ConfigureForwarder(ctx context.Context, deployer *deployment.Deployer, contractID string, donID, configVersion, f uint32, signers [][32]byte) error {
	if deployer == nil {
		return fmt.Errorf("deployer is nil")
	}
	client := crebindings.NewForwarderClient(deployer, contractID)
	if err := client.SetConfig(ctx, donID, configVersion, f, signers); err != nil {
		return fmt.Errorf("set_config on forwarder %s (don %d cfgVersion %d f %d, %d signers): %w", contractID, donID, configVersion, f, len(signers), err)
	}
	return nil
}

// AddForwarder registers an account  as an authorized transmitter on the forwarder.
func AddForwarder(ctx context.Context, deployer *deployment.Deployer, contractID, forwarder string) error {
	if deployer == nil {
		return fmt.Errorf("deployer is nil")
	}
	client := crebindings.NewForwarderClient(deployer, contractID)
	if err := client.AddForwarder(ctx, forwarder); err != nil {
		return fmt.Errorf("add_forwarder %s on forwarder %s: %w", forwarder, contractID, err)
	}
	return nil
}
