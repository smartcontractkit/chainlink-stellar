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

	projectRoot, deployerKP, deployer, _, _, _ := GetSharedTestEnv(ctx, t)

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

		t.Run("can apply dest chain config", func(t *testing.T) {
			mockRouter := helpers.GenerateMockContractID(t, deployerKP.Address(), "router")
			mockExecutor := helpers.GenerateMockContractID(t, deployerKP.Address(), "executor")
			mockCcv := helpers.GenerateMockContractID(t, deployerKP.Address(), "ccv")

			destChainArgs := onrampbindings.DestChainConfigArgs{
				DestChainSelector:         99999,
				Router:                    mockRouter,
				AddressBytesLength:        20,
				TokenReceiverAllowed:      true,
				MessageNetworkFeeUsdCents: 50,
				TokenNetworkFeeUsdCents:   100,
				BaseExecutionGasCost:      200000,
				DefaultExecutor:           mockExecutor,
				ExecutionFeeUsdCents:      25,
				LaneMandatedCcvs:          []string{},
				DefaultCcvs:               []string{mockCcv},
				OffRamp:                   make([]byte, 20),
			}

			err := onRampClient.ApplyDestChainConfigUpdates(ctx, []onrampbindings.DestChainConfigArgs{destChainArgs})
			if err != nil {
				t.Fatalf("Failed to apply dest chain config: %v", err)
			}

			stored, err := onRampClient.GetDestChainConfig(ctx, 99999)
			if err != nil {
				t.Fatalf("Failed to get dest chain config: %v", err)
			}
			if stored == nil {
				t.Fatal("GetDestChainConfig returned nil")
			}
			if stored.Router != mockRouter {
				t.Errorf("Router mismatch: expected %s, got %s", mockRouter, stored.Router)
			}
			if stored.AddressBytesLength != 20 {
				t.Errorf("AddressBytesLength mismatch: expected 20, got %d", stored.AddressBytesLength)
			}
			if stored.BaseExecutionGasCost != 200000 {
				t.Errorf("BaseExecutionGasCost mismatch: expected 200000, got %d", stored.BaseExecutionGasCost)
			}
			if stored.ExecutionFeeUsdCents != 25 {
				t.Errorf("ExecutionFeeUsdCents mismatch: expected 25, got %d", stored.ExecutionFeeUsdCents)
			}
			t.Log("Dest chain config applied and verified successfully")
		})

		t.Run("can update dynamic config", func(t *testing.T) {
			newFeeQuoter := helpers.GenerateMockContractID(t, deployerKP.Address(), "new-fee-quoter")
			newFeeAggregator := helpers.GenerateMockContractID(t, deployerKP.Address(), "new-fee-aggregator")

			newDynamicConfig := onrampbindings.DynamicConfig{
				FeeQuoter:     newFeeQuoter,
				FeeAggregator: newFeeAggregator,
			}

			err := onRampClient.SetDynamicConfig(ctx, newDynamicConfig)
			if err != nil {
				t.Fatalf("Failed to set dynamic config: %v", err)
			}

			stored, err := onRampClient.GetDynamicConfig(ctx)
			if err != nil {
				t.Fatalf("Failed to get dynamic config: %v", err)
			}
			if stored == nil {
				t.Fatal("GetDynamicConfig returned nil")
			}
			if stored.FeeQuoter != newFeeQuoter {
				t.Errorf("FeeQuoter mismatch: expected %s, got %s", newFeeQuoter, stored.FeeQuoter)
			}
			if stored.FeeAggregator != newFeeAggregator {
				t.Errorf("FeeAggregator mismatch: expected %s, got %s", newFeeAggregator, stored.FeeAggregator)
			}
			t.Log("Dynamic config updated and verified successfully")
		})

		t.Run("can get all dest chain configs", func(t *testing.T) {
			mockRouter2 := helpers.GenerateMockContractID(t, deployerKP.Address(), "router2")
			mockExecutor2 := helpers.GenerateMockContractID(t, deployerKP.Address(), "executor2")
			mockCcv2 := helpers.GenerateMockContractID(t, deployerKP.Address(), "ccv2")

			destChainArgs2 := onrampbindings.DestChainConfigArgs{
				DestChainSelector:         88888,
				Router:                    mockRouter2,
				AddressBytesLength:        32,
				TokenReceiverAllowed:      false,
				MessageNetworkFeeUsdCents: 75,
				TokenNetworkFeeUsdCents:   150,
				BaseExecutionGasCost:      300000,
				DefaultExecutor:           mockExecutor2,
				ExecutionFeeUsdCents:      25,
				LaneMandatedCcvs:          []string{},
				DefaultCcvs:               []string{mockCcv2},
				OffRamp:                   make([]byte, 32),
			}

			err := onRampClient.ApplyDestChainConfigUpdates(ctx, []onrampbindings.DestChainConfigArgs{destChainArgs2})
			if err != nil {
				t.Fatalf("Failed to apply second dest chain config: %v", err)
			}

			selectors, configs, err := onRampClient.GetAllDestChainConfigs(ctx)
			if err != nil {
				t.Fatalf("Failed to get all dest chain configs: %v", err)
			}
			if len(selectors) < 2 {
				t.Fatalf("Expected at least 2 dest chain configs, got %d", len(selectors))
			}
			if len(configs) != len(selectors) {
				t.Fatalf("Selectors/configs length mismatch: %d vs %d", len(selectors), len(configs))
			}
			t.Logf("Got %d dest chain configs", len(selectors))
		})

		t.Run("get_expected_next_message_number starts at 1", func(t *testing.T) {
			nextNum, err := onRampClient.GetExpectedNextMessageNumber(ctx, 99999)
			if err != nil {
				t.Fatalf("Failed to get expected next message number: %v", err)
			}
			if nextNum != 1 {
				t.Errorf("Expected next message number to be 1, got %d", nextNum)
			}
			t.Logf("Next message number for chain 99999: %d", nextNum)
		})

		t.Run("dest chain not configured returns error", func(t *testing.T) {
			_, err := onRampClient.GetDestChainConfig(ctx, 11111111)
			if err == nil {
				t.Fatal("Expected error for unconfigured chain, got nil")
			}
			t.Logf("Got expected error for unconfigured chain: %v", err)
		})
	})
}
