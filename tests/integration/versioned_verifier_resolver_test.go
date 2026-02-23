//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

func TestVersionedVerifierResolver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, _, _ := helpers.SetupTestEnv(ctx, t)

	// Deploy the VersionedVerifierResolver contract
	t.Log("Deploying VersionedVerifierResolver contract...")
	salt := deployment.GenerateDeterministicSalt(deployerKP.Address(), "versioned-verifier-resolver")
	wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "ccvs_versioned_verifier_resolver.wasm")

	contractID, err := deployer.DeployContract(ctx, wasmPath, salt)
	if err != nil {
		t.Fatalf("Failed to deploy VersionedVerifierResolver: %v", err)
	}
	t.Logf("VersionedVerifierResolver deployed at: %s", contractID)

	// Create client
	client := vvrbindings.NewVersionedVerifierResolverClient(deployer, contractID)

	// Generate mock addresses
	mockFeeAggregator := helpers.GenerateMockContractID(t, deployerKP.Address(), "fee-aggregator")
	mockInboundVerifier := helpers.GenerateMockContractID(t, deployerKP.Address(), "inbound-verifier")
	mockOutboundVerifier := helpers.GenerateMockContractID(t, deployerKP.Address(), "outbound-verifier")

	t.Run("initialize", func(t *testing.T) {
		err := client.Initialize(ctx, deployerKP.Address(), mockFeeAggregator)
		if err != nil {
			t.Fatalf("Failed to initialize: %v", err)
		}
		t.Log("VersionedVerifierResolver initialized successfully")
	})

	t.Run("verify owner", func(t *testing.T) {
		owner, err := client.Owner(ctx)
		if err != nil {
			t.Fatalf("Failed to get owner: %v", err)
		}
		if *owner != deployerKP.Address() {
			t.Errorf("Owner mismatch: expected %s, got %s", deployerKP.Address(), *owner)
		}
		t.Logf("Owner verified: %v", owner)
	})

	t.Run("verify fee aggregator", func(t *testing.T) {
		feeAgg, err := client.GetFeeAggregator(ctx)
		if err != nil {
			t.Fatalf("Failed to get fee aggregator: %v", err)
		}
		if feeAgg != mockFeeAggregator {
			t.Errorf("FeeAggregator mismatch: expected %v, got %v", mockFeeAggregator, feeAgg)
		}
		t.Logf("FeeAggregator verified: %v", feeAgg)
	})

	t.Run("apply inbound implementation updates", func(t *testing.T) {
		version := [4]byte{0x00, 0x01, 0x00, 0x00} // version 1.0

		updates := []vvrbindings.InboundImplementationUpdate{
			{
				Version:  version,
				Verifier: &mockInboundVerifier,
			},
		}

		err := client.ApplyInboundImplUpdates(ctx, updates)
		if err != nil {
			t.Fatalf("Failed to apply inbound impl updates: %v", err)
		}
		t.Log("Inbound implementation update applied")

		// Verify via get_inbound_implementation
		// The first 4 bytes of verifierResults are the version
		verifierResults := version[:]
		addr, err := client.GetInboundImplementation(ctx, verifierResults)
		if err != nil {
			t.Fatalf("Failed to get inbound implementation: %v", err)
		}
		if addr != mockInboundVerifier {
			t.Errorf("Inbound implementation mismatch: expected %v, got %v", mockInboundVerifier, addr)
		}
		t.Logf("Inbound implementation verified: %v", addr)
	})

	t.Run("apply outbound implementation updates", func(t *testing.T) {
		destChainSelector := uint64(12345)

		updates := []vvrbindings.OutboundImplementationUpdate{
			{
				DestChainSelector: destChainSelector,
				Verifier:          &mockOutboundVerifier,
			},
		}

		err := client.ApplyOutboundImplUpdates(ctx, updates)
		if err != nil {
			t.Fatalf("Failed to apply outbound impl updates: %v", err)
		}
		t.Log("Outbound implementation update applied")

		// Verify via get_outbound_implementation
		addr, err := client.GetOutboundImplementation(ctx, destChainSelector, []byte{})
		if err != nil {
			t.Fatalf("Failed to get outbound implementation: %v", err)
		}
		if addr != mockOutboundVerifier {
			t.Errorf("Outbound implementation mismatch: expected %v, got %v", mockOutboundVerifier, addr)
		}
		t.Logf("Outbound implementation verified: %v", addr)
	})

	t.Run("update fee aggregator", func(t *testing.T) {
		newFeeAggregator := helpers.GenerateMockContractID(t, deployerKP.Address(), "new-fee-aggregator")

		err := client.SetFeeAggregator(ctx, newFeeAggregator)
		if err != nil {
			t.Fatalf("Failed to set fee aggregator: %v", err)
		}

		feeAgg, err := client.GetFeeAggregator(ctx)
		if err != nil {
			t.Fatalf("Failed to get fee aggregator: %v", err)
		}
		if feeAgg != newFeeAggregator {
			t.Errorf("FeeAggregator mismatch after update: expected %v, got %v", newFeeAggregator, feeAgg)
		}
		t.Logf("Fee aggregator updated and verified: %v", feeAgg)
	})

	t.Run("remove inbound implementation", func(t *testing.T) {
		version := [4]byte{0x00, 0x01, 0x00, 0x00} // same version as set above

		// Remove by passing nil verifier (maps to Soroban Option::None)
		updates := []vvrbindings.InboundImplementationUpdate{
			{
				Version:  version,
				Verifier: nil,
			},
		}

		err := client.ApplyInboundImplUpdates(ctx, updates)
		if err != nil {
			t.Fatalf("Failed to remove inbound impl: %v", err)
		}
		t.Log("Inbound implementation removed")
	})

	t.Run("remove outbound implementation", func(t *testing.T) {
		destChainSelector := uint64(12345) // same as set above

		// Remove by passing nil verifier (maps to Soroban Option::None)
		updates := []vvrbindings.OutboundImplementationUpdate{
			{
				DestChainSelector: destChainSelector,
				Verifier:          nil,
			},
		}

		err := client.ApplyOutboundImplUpdates(ctx, updates)
		if err != nil {
			t.Fatalf("Failed to remove outbound impl: %v", err)
		}
		t.Log("Outbound implementation removed")
	})

	t.Log("VersionedVerifierResolver test passed!")
}
