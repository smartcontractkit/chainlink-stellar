//go:build integration

package integration

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

func TestOnRamp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, _, _ := helpers.SetupTestEnv(ctx, t)

	t.Run("can deploy onramp contract", func(t *testing.T) {
		// Deploy the OnRamp contract
		t.Log("Deploying OnRamp contract...")
		onrampSalt := deployment.GenerateDeterministicSalt(deployerKP.Address(), "onramp")
		onrampWasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "onramp.wasm")

		fmt.Printf("OnRamp WASM path: %s\n", onrampWasmPath)
		contractID, err := deployer.DeployContract(ctx, onrampWasmPath, onrampSalt)
		if err != nil {
			t.Fatalf("Failed to deploy OnRamp: %v", err)
		}

		// Generate mock addresses for the configuration
		mockFeeQuoter := helpers.GenerateMockContractID(t, deployerKP.Address(), "fee-quoter")
		mockFeeAggregator := helpers.GenerateMockContractID(t, deployerKP.Address(), "fee-aggregator")
		mockRMNRemote := helpers.GenerateMockContractID(t, deployerKP.Address(), "rmn-remote")
		mockTokenAdminRegistry := helpers.GenerateMockContractID(t, deployerKP.Address(), "token-admin-registry")

		t.Logf("Mock contracts - FeeQuoter: %s, FeeAggregator: %s, RMNRemote: %s, TokenAdminRegistry: %s",
			mockFeeQuoter, mockFeeAggregator, mockRMNRemote, mockTokenAdminRegistry)

		// Create OnRamp client using the deployer as the Invoker
		onRampClient := onrampbindings.NewOnRampClient(deployer, contractID)

		// Initialize the OnRamp contract using devenv types
		t.Log("Initializing OnRamp contract...")

		staticConfig := onrampbindings.StaticConfig{
			ChainSelector:         12345, // Test chain selector
			TokenAdminRegistry:    mockTokenAdminRegistry,
			RmnProxy:              mockRMNRemote,
			MaxUsdCentsPerMessage: 10000, // $100
		}

		dynamicConfig := onrampbindings.DynamicConfig{
			FeeQuoter:     mockFeeQuoter,
			FeeAggregator: mockFeeAggregator,
		}

		// Call initialize using the OnRampClient
		err = onRampClient.Initialize(ctx, deployerKP.Address(), staticConfig, dynamicConfig)
		if err != nil {
			t.Fatalf("Failed to initialize OnRamp: %v", err)
		}
		t.Log("OnRamp initialized successfully")

		// Verify static config using OnRampClient
		t.Log("Verifying static config...")
		parsedStaticConfig, err := onRampClient.GetStaticConfig(ctx)
		if err != nil {
			t.Fatalf("Failed to call get_static_config: %v", err)
		}
		if parsedStaticConfig == nil {
			t.Fatal("get_static_config() returned nil")
		}

		if parsedStaticConfig.ChainSelector != staticConfig.ChainSelector {
			t.Errorf("ChainSelector mismatch: expected %d, got %d", staticConfig.ChainSelector, parsedStaticConfig.ChainSelector)
		}
		if parsedStaticConfig.MaxUsdCentsPerMessage != staticConfig.MaxUsdCentsPerMessage {
			t.Errorf("MaxUsdCentsPerMessage mismatch: expected %d, got %d", staticConfig.MaxUsdCentsPerMessage, parsedStaticConfig.MaxUsdCentsPerMessage)
		}
		t.Logf("Static config verified: ChainSelector=%d, MaxUsdCentsPerMessage=%d",
			parsedStaticConfig.ChainSelector, parsedStaticConfig.MaxUsdCentsPerMessage)

		// Verify dynamic config using OnRampClient
		t.Log("Verifying dynamic config...")
		parsedDynamicConfig, err := onRampClient.GetDynamicConfig(ctx)
		if err != nil {
			t.Fatalf("Failed to call get_dynamic_config: %v", err)
		}
		if parsedDynamicConfig == nil {
			t.Fatal("get_dynamic_config() returned nil")
		}

		if parsedDynamicConfig.FeeQuoter != dynamicConfig.FeeQuoter {
			t.Errorf("FeeQuoter mismatch: expected %s, got %s", dynamicConfig.FeeQuoter, parsedDynamicConfig.FeeQuoter)
		}
		if parsedDynamicConfig.FeeAggregator != dynamicConfig.FeeAggregator {
			t.Errorf("FeeAggregator mismatch: expected %s, got %s", dynamicConfig.FeeAggregator, parsedDynamicConfig.FeeAggregator)
		}
		t.Logf("Dynamic config verified: FeeQuoter=%s, FeeAggregator=%s",
			parsedDynamicConfig.FeeQuoter, parsedDynamicConfig.FeeAggregator)

		t.Log("OnRamp deployment and initialization test passed!")
	})
}
