//go:build integration

package integration

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	common "github.com/smartcontractkit/chainlink-stellar/ccv/common"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
	"github.com/stellar/go-stellar-sdk/strkey"
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
			MaxUsdCentsPerMessage: 500_000, // $5000 — Ethereum-spike-safe per-message fee cap (see stellar_ccip_full_deploy.go).
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

// sentinelContractStrkey returns the VersionByteContract strkey of a 32-byte sentinel
// (NO_EXECUTION / USE_DEFAULT), i.e. exactly what the OnRamp places in the executor
// receipt `Issuer` when it leaves a no-execution sentinel in place. Mirrors the helper
// in tests/e2e/stellar_executor_sentinel_test.go so the integration and e2e layers
// assert the same issuer.
func sentinelContractStrkey(t *testing.T, raw []byte) string {
	t.Helper()
	sk, err := strkey.Encode(strkey.VersionByteContract, raw)
	if err != nil {
		t.Fatalf("encode sentinel %x: %v", raw, err)
	}
	return sk
}

// findReceiptByIssuer returns the first receipt whose Issuer matches wantIssuer, or
// fatals listing every issuer seen. Receipts are keyed by (possibly resolved) issuer,
// so this isolates one layer's receipt (executor / CCV / pool / network) regardless of
// the on-wire ordering.
func findReceiptByIssuer(t *testing.T, receipts []onrampbindings.Receipt, wantIssuer string) onrampbindings.Receipt {
	t.Helper()
	for _, r := range receipts {
		if r.Issuer == wantIssuer {
			return r
		}
	}
	for i, r := range receipts {
		t.Errorf("receipt[%d] issuer seen: %s", i, r.Issuer)
	}
	t.Fatalf("no receipt with issuer %q among %d receipts", wantIssuer, len(receipts))
	return onrampbindings.Receipt{}
}

// TestOnRampNoExecutionSentinelZeroExecutorFee is the H-8 / M-7 high-stakes regression
// guard at the integration layer (the e2e layer already covers it in
// tests/e2e/stellar_executor_sentinel_test.go).
//
// PROVES-WORKS:  a Stellar→EVM message whose GenericExtraArgsV3.executor is the
// no-execution sentinel (0xeba517d2…) is ACCEPTED by the OnRamp, which leaves the
// sentinel in place and emits an executor receipt issued by the sentinel strkey.
//
// PROVES-DOESN'T-HAPPEN: that executor receipt's FeeTokenAmount is EXACTLY ZERO — the
// executor flat fee AND the execution-gas cost are both zeroed, so a sender who opts out
// of auto-execution is never charged for execution and the executor is never paid. A
// non-zero value here would mean either an unintended executor payout or execution gas
// being charged to a message that will not be executed — both severe economic bugs.
func TestOnRampNoExecutionSentinelZeroExecutorFee(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, networkPassphrase, friendbotURL := GetSharedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	const localSourceChain = uint64(11111)
	const remoteDestChain = uint64(22222)
	const saltPrefix = "h8-noexec"

	stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localSourceChain, saltPrefix, false)
	feeToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, saltPrefix+"-fee")

	// Data-only wire (no transfer tokens); the default executor is the real Executor
	// deployed inside deployOutboundSendWire. We override the per-message executor with
	// the no-execution sentinel below.
	wire := deployOutboundSendWire(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix, stack,
		localSourceChain, remoteDestChain, feeToken, nil)

	noExecIssuer := sentinelContractStrkey(t, common.NoExecutionAddressRaw)

	buildNoExecExtraArgs := func() []byte {
		extraArgs, err := encodeOnrampExtraArgsV3(onrampbindings.GenericExtraArgsV3{
			Ccvs:               []string{stack.VvrID},
			CcvArgs:            [][]byte{{}},
			Executor:           noExecIssuer, // ← no-execution sentinel
			ExecutorArgs:       []byte{},
			GasLimit:           0,
			BlockConfirmations: 0,
			TokenReceiver:      []byte{},
			TokenArgs:          []byte{},
		})
		if err != nil {
			t.Fatalf("encode no-exec extra args: %v", err)
		}
		return extraArgs
	}

	evmReceiver := make([]byte, 20)
	for i := range evmReceiver {
		evmReceiver[i] = 0x33
	}

	msg := routerbindings.StellarToAnyMessage{
		Receiver:  evmReceiver,
		Data:      []byte("no-exec sentinel integration guard"),
		FeeToken:  feeToken,
		ExtraArgs: buildNoExecExtraArgs(),
	}

	requiredFee, err := stack.RouterClient.GetFee(ctx, remoteDestChain, msg)
	if err != nil {
		t.Fatalf("Router GetFee (no-exec): %v", err)
	}
	// The total fee is still positive (CCV + network), only the executor slice is zero.
	if requiredFee.Sign() <= 0 {
		t.Fatalf("expected positive total fee (CCV+network), got %s", requiredFee.String())
	}

	latest, err := rpcClient.GetLatestLedger(ctx)
	if err != nil {
		t.Fatalf("GetLatestLedger: %v", err)
	}
	startLedger := latest.Sequence

	msgID, err := stack.RouterClient.CcipSend(ctx, deployerAddr, remoteDestChain, msg, requiredFee)
	if err != nil {
		t.Fatalf("Router CcipSend (no-exec sentinel should be accepted): %v", err)
	}
	if msgID == ([32]byte{}) {
		t.Fatal("CcipSend returned empty message_id")
	}

	sentEvt, err := wire.OnRampClient.WaitForCCIPMessageSentEvent(ctx, startLedger, 30*time.Second,
		func(e *onrampbindings.CCIPMessageSentEvent) bool {
			return e.DestChainSelector == remoteDestChain && bytes.Equal(e.MessageId[:], msgID[:])
		})
	if err != nil {
		t.Fatalf("WaitForCCIPMessageSentEvent: %v", err)
	}

	execReceipt := findReceiptByIssuer(t, sentEvt.Receipts, noExecIssuer)
	if execReceipt.FeeTokenAmount == nil {
		t.Fatal("no-exec executor receipt FeeTokenAmount is nil")
	}
	if execReceipt.FeeTokenAmount.Sign() != 0 {
		t.Fatalf("no-exec sentinel must yield a ZERO executor fee (flat + exec-gas), got %s",
			execReceipt.FeeTokenAmount.String())
	}
	t.Logf("✅ no-execution sentinel: executor receipt issued by sentinel %s with fee 0", noExecIssuer)
}

// TestOnRampFunctionalExecutorChargesExecutionFee is the FUNCTIONAL complement to
// TestOnRampNoExecutionSentinelZeroExecutorFee — together they pin the two ends of
// the executor fee behavior (H-8 / M-5 / M-7) at the integration layer.
//
// PROVES-WORKS (M-5 resolution): a Stellar→EVM message whose
// GenericExtraArgsV3.executor is the "use default" sentinel (0x72068b37…) is
// resolved by the OnRamp to the lane's concrete default_executor BEFORE receipt
// emission, so the executor receipt is issued by the REAL Executor contract strkey
// (stack.ExecutorID), NOT by the sentinel strkey. This is the address(0)→default
// parity at the Go-binding layer.
//
// PROVES-WORKS (H-8 functional executor): that resolved-default (functional)
// executor receipt's FeeTokenAmount is STRICTLY POSITIVE. The executor flat fee
// (Executor::get_fee = USDCentsFee) is 0 in the devenv, but the execution-gas-cost
// slice (gas_quote.gas_cost_usd_cents) is non-zero for any destination with a set
// gas price — so a functional executor MUST charge a positive fee. This is the
// distinction the no-exec test can't make by itself (it only proves the zero side):
// a functional executor charging exactly 0 would be indistinguishable from the
// no-exec sentinel and would mean the executor layer is economically vacuous.
//
// Safety note: this >0 assertion is sound in this harness because the integration
// devenv sets a non-zero dest gas price (FeeQuoter UpdatePrices) and a non-zero
// base execution gas cost, and fee_distribution_test.go already asserts a non-zero
// executor fee delta on main using the same harness. A zero here would indicate
// either the resolution path collapsed to no-exec behaviour or the gas price was
// mis-seeded (L-12).
func TestOnRampFunctionalExecutorChargesExecutionFee(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, networkPassphrase, friendbotURL := GetSharedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	const localSourceChain = uint64(11111)
	const remoteDestChain = uint64(22222)
	const saltPrefix = "h8-funcexec"

	stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localSourceChain, saltPrefix, false)
	feeToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, saltPrefix+"-fee")

	// deployOutboundSendWire deploys a real Executor and wires it as the OnRamp
	// default_executor (stack.ExecutorID), with UsdCentsFee:0 and a non-zero dest
	// gas price. The use-default sentinel below resolves to it.
	wire := deployOutboundSendWire(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix, stack,
		localSourceChain, remoteDestChain, feeToken, nil)

	if stack.ExecutorID == "" {
		t.Fatal("default_executor must be deployed and wired by deployOutboundSendWire")
	}

	// Sanity: the concrete default_executor must NOT equal the use-default sentinel
	// strkey — that would mean resolution never happens.
	useDefaultIssuer := sentinelContractStrkey(t, common.UseDefaultExecutorAddressRaw)
	if useDefaultIssuer == stack.ExecutorID {
		t.Fatal("default_executor must be a concrete contract, not the use-default sentinel itself")
	}

	buildUseDefaultExtraArgs := func() []byte {
		extraArgs, err := encodeOnrampExtraArgsV3(onrampbindings.GenericExtraArgsV3{
			Ccvs:               []string{stack.VvrID},
			CcvArgs:            [][]byte{{}},
			Executor:           useDefaultIssuer, // ← use-default sentinel, resolves to real Executor
			ExecutorArgs:       []byte{},
			GasLimit:           0,
			BlockConfirmations: 0,
			TokenReceiver:      []byte{},
			TokenArgs:          []byte{},
		})
		if err != nil {
			t.Fatalf("encode use-default extra args: %v", err)
		}
		return extraArgs
	}

	evmReceiver := make([]byte, 20)
	for i := range evmReceiver {
		evmReceiver[i] = 0x33
	}

	msg := routerbindings.StellarToAnyMessage{
		Receiver:  evmReceiver,
		Data:      []byte("functional executor integration guard"),
		FeeToken:  feeToken,
		ExtraArgs: buildUseDefaultExtraArgs(),
	}

	requiredFee, err := stack.RouterClient.GetFee(ctx, remoteDestChain, msg)
	if err != nil {
		t.Fatalf("Router GetFee (use-default): %v", err)
	}
	if requiredFee.Sign() <= 0 {
		t.Fatalf("expected positive total fee, got %s", requiredFee.String())
	}

	latest, err := rpcClient.GetLatestLedger(ctx)
	if err != nil {
		t.Fatalf("GetLatestLedger: %v", err)
	}
	startLedger := latest.Sequence

	msgID, err := stack.RouterClient.CcipSend(ctx, deployerAddr, remoteDestChain, msg, requiredFee)
	if err != nil {
		t.Fatalf("Router CcipSend (use-default sentinel should be accepted): %v", err)
	}
	if msgID == ([32]byte{}) {
		t.Fatal("CcipSend returned empty message_id")
	}

	sentEvt, err := wire.OnRampClient.WaitForCCIPMessageSentEvent(ctx, startLedger, 30*time.Second,
		func(e *onrampbindings.CCIPMessageSentEvent) bool {
			return e.DestChainSelector == remoteDestChain && bytes.Equal(e.MessageId[:], msgID[:])
		})
	if err != nil {
		t.Fatalf("WaitForCCIPMessageSentEvent: %v", err)
	}

	// PROVES-WORKS (M-5): the receipt is issued by the resolved concrete
	// default_executor, not by the sentinel strkey.
	execReceipt := findReceiptByIssuer(t, sentEvt.Receipts, stack.ExecutorID)
	if execReceipt.FeeTokenAmount == nil {
		t.Fatal("functional executor receipt FeeTokenAmount is nil")
	}
	// PROVES-WORKS (H-8): a functional executor charges a strictly positive fee
	// (the execution-gas-cost slice), unlike the no-exec sentinel's exact zero.
	if execReceipt.FeeTokenAmount.Sign() <= 0 {
		t.Fatalf("functional (resolved-default) executor fee must be > 0 — exec-gas cost charged; got %s",
			execReceipt.FeeTokenAmount.String())
	}
	t.Logf("✅ functional executor: receipt issued by concrete %s with fee %s (>0, exec-gas charged)",
		stack.ExecutorID, execReceipt.FeeTokenAmount.String())
}
