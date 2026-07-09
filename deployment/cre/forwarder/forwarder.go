// Package forwarder deploys and configures the Soroban CRE KeystoneForwarder for
// local CRE / integration tests.
//
// The compiled contract WASM is embedded (go:embed) so downstream consumers — e.g.
// the chainlink system-tests Stellar feature — can deploy it via a pinned Go module
// dependency, with no Rust/soroban toolchain at deploy time. This mirrors how
// chainlink-evm ships compiled forwarder bytecode in its gethwrappers and
// chainlink-aptos ships its Move package, adapted to Soroban's prebuilt-WASM model.
package forwarder

import (
	"context"
	_ "embed"
	"fmt"

	crebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/cre"
	"github.com/smartcontractkit/chainlink-stellar/deployment"
)

// forwarderWASM is the compiled keystone-forwarder Soroban contract
// (contracts/cre, built with `stellar contract build` / `cargo build --release
// --target wasm32v1-none -p keystone-forwarder`). Rebuild and recommit whenever
// contracts/cre/src changes so this artifact stays in sync with the source.
//
//go:embed artifacts/keystone_forwarder.wasm
var forwarderWASM []byte

// WASM returns the embedded compiled forwarder contract bytes.
func WASM() []byte { return forwarderWASM }

// DeployForwarder uploads + instantiates the KeystoneForwarder and initializes it
// with the given owner (G… StrKey account). It returns the deployed contract's
// C… address. salt lets callers make deployment deterministic across runs.
func DeployForwarder(ctx context.Context, deployer *deployment.Deployer, owner string, salt [32]byte) (string, error) {
	if deployer == nil {
		return "", fmt.Errorf("deployer is nil")
	}
	contractID, err := deployer.DeployContractBytes(ctx, forwarderWASM, salt)
	if err != nil {
		return "", fmt.Errorf("deploy forwarder wasm: %w", err)
	}
	client := crebindings.NewKeystoneForwarderClient(deployer, contractID)
	if err := client.Initialize(ctx, owner); err != nil {
		return "", fmt.Errorf("initialize forwarder %s (owner %s): %w", contractID, owner, err)
	}
	return contractID, nil
}

// ConfigureForwarder sets the DON signer configuration on an already-deployed
// forwarder. signers are the ed25519 OCR onchain public keys (32 bytes each): the
// forwarder verifies f+1 ed25519 report signatures against this set, so signers
// must be the worker nodes' Stellar (FamilyStellar) OCR2 onchain keys.
func ConfigureForwarder(ctx context.Context, deployer *deployment.Deployer, contractID string, donID, configVersion, f uint32, signers [][32]byte) error {
	if deployer == nil {
		return fmt.Errorf("deployer is nil")
	}
	client := crebindings.NewKeystoneForwarderClient(deployer, contractID)
	if err := client.SetConfig(ctx, donID, configVersion, f, signers); err != nil {
		return fmt.Errorf("set_config on forwarder %s (don %d cfgVersion %d f %d, %d signers): %w", contractID, donID, configVersion, f, len(signers), err)
	}
	return nil
}

// AddForwarder registers an account (G… StrKey) as an authorized transmitter on
// the forwarder. report() calls require_valid_forwarder(transmitter), so the DON
// worker accounts that submit reports (their relayer signing accounts) must each be
// added, or report() reverts with UnauthorizedForwarder. Owner-authenticated:
// deployer must be the forwarder owner. Idempotent add is safe (re-adds are no-ops).
func AddForwarder(ctx context.Context, deployer *deployment.Deployer, contractID, forwarder string) error {
	if deployer == nil {
		return fmt.Errorf("deployer is nil")
	}
	client := crebindings.NewKeystoneForwarderClient(deployer, contractID)
	if err := client.AddForwarder(ctx, forwarder); err != nil {
		return fmt.Errorf("add_forwarder %s on forwarder %s: %w", forwarder, contractID, err)
	}
	return nil
}
