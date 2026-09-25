//go:build integration

package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/keypair"

	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	rmnproxybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_proxy"
	rmnremotebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_remote"
	contracttransmitter "github.com/smartcontractkit/chainlink-stellar/ccv/contract_transmitter"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

const localChainSelector = uint64(12345)

// deployOffRampDependencies deploys and initializes the RMN Remote and RMN Proxy
// contracts that the OffRamp depends on for curse-checking.
func deployOffRampDependencies(
	ctx context.Context,
	t *testing.T,
	projectRoot string,
	deployer *deployment.Deployer,
	deployerAddr string,
) (rmnRemoteID, rmnProxyID, ccvResolverID, ccvVerifierID string) {
	t.Helper()

	salt := deployment.GenerateDeterministicSalt(deployerAddr, "rmn-remote-offramp")
	wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "rmn_remote.wasm")
	rmnRemoteID, err := deployer.DeployContract(ctx, wasmPath, salt)
	if err != nil {
		t.Fatalf("Failed to deploy RMN Remote: %v", err)
	}
	t.Logf("RMN Remote deployed at: %s", rmnRemoteID)

	rmnRemoteClient := rmnremotebindings.NewRmnRemoteClient(deployer, rmnRemoteID)
	if err := rmnRemoteClient.Initialize(ctx, deployerAddr, nil); err != nil {
		t.Fatalf("Failed to initialize RMN Remote: %v", err)
	}

	salt = deployment.GenerateDeterministicSalt(deployerAddr, "rmn-proxy-offramp")
	wasmPath = filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "rmn_proxy.wasm")
	rmnProxyID, err = deployer.DeployContract(ctx, wasmPath, salt)
	if err != nil {
		t.Fatalf("Failed to deploy RMN Proxy: %v", err)
	}
	t.Logf("RMN Proxy deployed at: %s", rmnProxyID)

	rmnProxyClient := rmnproxybindings.NewRmnProxyClient(deployer, rmnProxyID)
	if err := rmnProxyClient.Initialize(ctx, deployerAddr, rmnRemoteID); err != nil {
		t.Fatalf("Failed to initialize RMN Proxy: %v", err)
	}

	ccvResolverID = helpers.GenerateMockContractID(t, deployerAddr, "ccv-resolver")
	ccvVerifierID = helpers.GenerateMockContractID(t, deployerAddr, "ccv-verifier")

	return rmnRemoteID, rmnProxyID, ccvResolverID, ccvVerifierID
}

func TestOffRamp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, networkPassphrase, _ := GetSharedTestEnv(ctx, t)

	// Capture the current ledger before any subtest runs. The "initialize offramp"
	// subtest below emits a StaticConfigSet event; the later "watch for
	// StaticConfigSet event" subtest polls forward from this ledger to find it
	// (the established event-wait convention — see receiver_ccv_consultation_test.go).
	latest, err := rpcClient.GetLatestLedger(ctx)
	if err != nil {
		t.Fatalf("GetLatestLedger: %v", err)
	}
	initStartLedger := latest.Sequence

	rmnRemoteID, rmnProxyID, _, _ := deployOffRampDependencies(ctx, t, projectRoot, deployer, deployerKP.Address())

	t.Log("Deploying OffRamp contract...")
	salt := deployment.GenerateDeterministicSalt(deployerKP.Address(), "offramp")
	wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "offramp.wasm")
	offrampContractID, err := deployer.DeployContract(ctx, wasmPath, salt)
	if err != nil {
		t.Fatalf("Failed to deploy OffRamp: %v", err)
	}
	t.Logf("OffRamp deployed at: %s", offrampContractID)

	offrampClient := offrampbindings.NewOffRampClient(deployer, offrampContractID)

	mockTokenAdminRegistry := helpers.GenerateMockContractID(t, deployerKP.Address(), "token-admin-registry")

	lggr := zerolog.New(os.Stdout).With().Timestamp().Logger()

	_ = networkPassphrase
	_ = rmnProxyID
	_ = &lggr

	// ========================================
	// Initialization
	// ========================================

	t.Run("initialize offramp", func(t *testing.T) {
		staticCfg := offrampbindings.StaticConfig{
			ChainSelector:      localChainSelector,
			RmnProxy:           rmnProxyID,
			TokenAdminRegistry: mockTokenAdminRegistry,
		}
		err := offrampClient.Initialize(ctx, deployerKP.Address(), staticCfg)
		if err != nil {
			t.Fatalf("Failed to initialize OffRamp: %v", err)
		}
		t.Log("OffRamp initialized successfully")
	})

	t.Run("double initialize fails", func(t *testing.T) {
		staticCfg := offrampbindings.StaticConfig{
			ChainSelector:      localChainSelector,
			RmnProxy:           rmnProxyID,
			TokenAdminRegistry: mockTokenAdminRegistry,
		}
		err := offrampClient.Initialize(ctx, deployerKP.Address(), staticCfg)
		if err == nil {
			t.Fatal("Expected error on double initialize, got nil")
		}
		t.Logf("Double initialize correctly rejected: %v", err)
	})

	t.Run("watch for StaticConfigSet event after initialization", func(t *testing.T) {
		// The "initialize offramp" subtest (run above) called Initialize, which
		// emits a StaticConfigSet event. Poll forward from the ledger captured at
		// the top of TestOffRamp (before any subtest) to find it, matching on the
		// full static config so a stray event from another contract can't satisfy.
		evt, err := offrampClient.WaitForStaticConfigSetEvent(ctx, initStartLedger, 30*time.Second,
			func(e *offrampbindings.StaticConfigSetEvent) bool {
				return e.StaticConfig.ChainSelector == localChainSelector &&
					e.StaticConfig.RmnProxy == rmnProxyID &&
					e.StaticConfig.TokenAdminRegistry == mockTokenAdminRegistry
			})
		if err != nil {
			t.Fatalf("WaitForStaticConfigSetEvent: %v", err)
		}
		if evt.StaticConfig.ChainSelector != localChainSelector {
			t.Errorf("StaticConfigSetEvent ChainSelector: want %d, got %d", localChainSelector, evt.StaticConfig.ChainSelector)
		}
		if evt.StaticConfig.RmnProxy != rmnProxyID {
			t.Errorf("StaticConfigSetEvent RmnProxy: want %s, got %s", rmnProxyID, evt.StaticConfig.RmnProxy)
		}
		t.Logf("StaticConfigSet event observed: chain_selector=%d rmn_proxy=%s", evt.StaticConfig.ChainSelector, evt.StaticConfig.RmnProxy)
	})

	// ========================================
	// Query Functions
	// ========================================

	t.Run("verify owner", func(t *testing.T) {
		owner, err := offrampClient.Owner(ctx)
		if err != nil {
			t.Fatalf("Failed to get owner: %v", err)
		}
		if owner == nil || *owner != deployerKP.Address() {
			t.Errorf("Owner mismatch: expected %s, got %v", deployerKP.Address(), owner)
		}
		t.Logf("Owner verified: %s", *owner)
	})

	t.Run("verify static config", func(t *testing.T) {
		cfg, err := offrampClient.GetStaticConfig(ctx)
		if err != nil {
			t.Fatalf("Failed to get static config: %v", err)
		}
		if cfg.ChainSelector != localChainSelector {
			t.Errorf("ChainSelector mismatch: expected %d, got %d", localChainSelector, cfg.ChainSelector)
		}
		if cfg.RmnProxy != rmnProxyID {
			t.Errorf("RmnProxy mismatch: expected %s, got %s", rmnProxyID, cfg.RmnProxy)
		}
		t.Logf("Static config verified: chain_selector=%d", cfg.ChainSelector)
	})

	t.Run("is not cursed initially", func(t *testing.T) {
		cursed, err := offrampClient.IsCursed(ctx)
		if err != nil {
			t.Fatalf("Failed to check IsCursed: %v", err)
		}
		if cursed {
			t.Fatal("OffRamp should not be cursed initially")
		}
		t.Log("OffRamp is not cursed (as expected)")
	})

	// ========================================
	// Source Chain Configuration
	// ========================================

	// "apply source chain config" requires the full contract stack (Router +
	// VVR + CommitteeVerifier); the basic TestOffRamp deploys only OffRamp + RMN.
	// It is covered end-to-end by TestOffRampExecute (deployFullStack applies the
	// source chain config; the execute subtests succeed only because that config
	// is present + enabled), with a dedicated read-back assertion there.

	t.Run("get all source chain configs initially empty", func(t *testing.T) {
		selectors, configs, err := offrampClient.GetAllSourceChainConfigs(ctx)
		if err != nil {
			t.Fatalf("Failed to get all source chain configs: %v", err)
		}
		if len(selectors) != 0 || len(configs) != 0 {
			t.Errorf("Expected empty source chain configs, got %d selectors and %d configs", len(selectors), len(configs))
		}
		t.Log("Source chain configs are empty initially (as expected)")
	})

	// ========================================
	// ContractTransmitter — Execute via ConvertAndWriteMessageToChain
	// ========================================

	t.Run("create contract transmitter", func(t *testing.T) {
		_, err := contracttransmitter.NewContractTransmitterWithClient(
			deployer,
			offrampContractID,
			offrampbindings.ExecutionStateChangedEventTopic,
			rmnRemoteID,
			&lggr,
		)
		if err != nil {
			t.Fatalf("Failed to create ContractTransmitter: %v", err)
		}
		t.Log("ContractTransmitter created successfully")
	})

	// The execute-path scenarios — source chain not enabled, CCV length
	// mismatch, gas-limit-override too low, invalid destination chain, invalid
	// onramp address, invalid offramp address, invalid CCV resolver, empty CCV
	// set, duplicate execute, cursed offramp, and valid execute + watching the
	// ExecutionStateChanged event — all require the full contract stack (Router
	// + VVR + CommitteeVerifier). They are implemented in TestOffRampExecute
	// below. The basic TestOffRamp only deploys OffRamp + RMN, so it cannot
	// exercise execute (which needs a configured + verified source chain).

	// ========================================
	// Execution State Queries
	// ========================================

	t.Run("get execution state for unknown message returns Untouched", func(t *testing.T) {
		unknownID := [32]byte{0xFF, 0xFE, 0xFD}
		state, err := offrampClient.GetExecutionState(ctx, unknownID)
		if err != nil {
			t.Fatalf("Failed to get execution state: %v", err)
		}
		if state != offrampbindings.MessageExecutionStateUntouched {
			t.Errorf("Expected Untouched state, got %d", state)
		}
		t.Log("Unknown message correctly returns Untouched state")
	})

	// "get execution state after successful execute" is covered by
	// TestOffRampExecute (the "get execution state after successful execute"
	// subtest), which runs against the full stack where a real execute succeeds.

	// ========================================
	// Ownership
	// ========================================

	t.Run("transfer ownership", func(t *testing.T) {
		newOwnerKP, err := keypair.Random()
		if err != nil {
			t.Fatalf("Failed to generate new owner keypair: %v", err)
		}

		err = offrampClient.TransferOwnership(ctx, newOwnerKP.Address())
		if err != nil {
			t.Fatalf("TransferOwnership failed: %v", err)
		}

		pending, err := offrampClient.GetPendingOwner(ctx)
		if err != nil {
			t.Fatalf("GetPendingOwner failed: %v", err)
		}
		if pending == nil || *pending != newOwnerKP.Address() {
			t.Fatalf("PendingOwner mismatch: expected %s, got %v", newOwnerKP.Address(), pending)
		}
		t.Logf("Ownership transfer initiated to %s", newOwnerKP.Address())

		err = offrampClient.CancelOwnershipTransfer(ctx)
		if err != nil {
			t.Fatalf("CancelOwnershipTransfer failed: %v", err)
		}

		owner, err := offrampClient.Owner(ctx)
		if err != nil {
			t.Fatalf("Owner check after cancel failed: %v", err)
		}
		if owner == nil || *owner != deployerKP.Address() {
			t.Fatalf("Owner should still be original deployer after cancel, got %v", owner)
		}
		t.Log("Ownership transfer cancelled; original owner retained")
	})
}

// TestOffRampExecute exercises the full execute path using a complete contract stack
// deployed via deployFullStack. These tests are separated from the basic TestOffRamp
// suite because they require the full contract dependency chain.
func TestOffRampExecute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, _, _, _ := GetSharedTestEnv(ctx, t)

	stack := deployFullStack(ctx, t, projectRoot, deployer, deployerKP.Address(), localChainSelector, "offramp-exec", false)

	t.Run("execute valid message succeeds", func(t *testing.T) {
		encoded, msgID, verifierBlob := stack.buildValidMessage(t, localChainSelector, 1, []byte("offramp-execute-test"))

		err := stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{verifierBlob}, 0)
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}

		state, err := stack.OfframpClient.GetExecutionState(ctx, msgID)
		if err != nil {
			t.Fatalf("GetExecutionState: %v", err)
		}
		if state != offrampbindings.MessageExecutionStateSuccess {
			t.Fatalf("execution state = %d, want Success (%d)", state, offrampbindings.MessageExecutionStateSuccess)
		}
		t.Logf("Execute succeeded; message ID %x has state Success", msgID[:8])
	})

	t.Run("execute already-executed message fails", func(t *testing.T) {
		encoded, _, verifierBlob := stack.buildValidMessage(t, localChainSelector, 1, []byte("offramp-execute-test"))

		err := stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{verifierBlob}, 0)
		if err == nil {
			t.Fatal("Expected error on duplicate execute, got nil")
		}
		if !strings.Contains(err.Error(), "Error(Contract") {
			t.Fatalf("Expected contract error, got: %v", err)
		}
		t.Logf("Duplicate execution correctly rejected: %v", err)
	})

	t.Run("get execution state after successful execute", func(t *testing.T) {
		_, msgID, _ := stack.buildValidMessage(t, localChainSelector, 1, []byte("offramp-execute-test"))
		state, err := stack.OfframpClient.GetExecutionState(ctx, msgID)
		if err != nil {
			t.Fatalf("GetExecutionState: %v", err)
		}
		if state != offrampbindings.MessageExecutionStateSuccess {
			t.Errorf("Expected Success state, got %d", state)
		}
		t.Logf("Execution state confirmed: %d (Success)", state)
	})

	t.Run("execute on cursed offramp fails", func(t *testing.T) {
		var globalCurseSubject [16]byte
		copy(globalCurseSubject[:], []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01})

		err := stack.RmnRemoteClient.Curse(ctx, deployerKP.Address(), [][16]byte{globalCurseSubject})
		if err != nil {
			t.Fatalf("Failed to curse: %v", err)
		}

		encoded, _, verifierBlob := stack.buildValidMessage(t, localChainSelector, 2, []byte("cursed-test"))
		err = stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{verifierBlob}, 0)
		if err == nil {
			t.Fatal("Expected error executing on cursed offramp, got nil")
		}
		if !strings.Contains(err.Error(), "Error(Contract") {
			t.Fatalf("Expected contract error for curse, got: %v", err)
		}
		t.Logf("Execute on cursed offramp correctly rejected: %v", err)

		err = stack.RmnRemoteClient.Uncurse(ctx, [][16]byte{globalCurseSubject})
		if err != nil {
			t.Fatalf("Failed to uncurse: %v", err)
		}
		t.Log("Uncursed successfully; offramp restored")
	})

	t.Run("execute with wrong destination chain fails", func(t *testing.T) {
		wrongDest := uint64(99998)
		encoded, _, verifierBlob := stack.buildValidMessage(t, wrongDest, 3, []byte("wrong-dest"))

		err := stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{verifierBlob}, 0)
		if err == nil {
			t.Fatal("Expected error for wrong destination chain, got nil")
		}
		if !strings.Contains(err.Error(), "Error(Contract") {
			t.Fatalf("Expected contract error, got: %v", err)
		}
		t.Logf("Wrong destination chain correctly rejected: %v", err)
	})

	t.Run("execute with invalid onramp address fails", func(t *testing.T) {
		sender := bytes.Repeat([]byte{0xcd}, 20)
		var ccvHashZero [32]byte
		badOnRamp := bytes.Repeat([]byte{0xFF}, 32)
		encoded, err := encodeCcipMessageV1(ccipV1Wire{
			SourceChainSelector: remoteSourceChain,
			DestChainSelector:   localChainSelector,
			SequenceNumber:      4,
			ExecutionGasLimit:   500_000,
			CcipReceiveGasLimit: 200_000,
			Finality:            0,
			CcvExecutorHash:     ccvHashZero,
			OnRampAddress:       badOnRamp,
			OffRampAddress:      stack.OffRampSuffix,
			Sender:              sender,
			Receiver:            stack.ReceiverRaw,
			DestBlob:            nil,
			TokenTransfer:       nil,
			Data:                []byte("bad-onramp"),
		})
		if err != nil {
			t.Fatalf("encodeCcipMessageV1: %v", err)
		}

		msgID := keccak256MessageID(encoded)
		verifierBlob := stack.signVerifierBlob(t, msgID)
		err = stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{verifierBlob}, 0)
		if err == nil {
			t.Fatal("Expected error for invalid onramp, got nil")
		}
		if !strings.Contains(err.Error(), "Error(Contract") {
			t.Fatalf("Expected contract error, got: %v", err)
		}
		t.Logf("Invalid onramp address correctly rejected: %v", err)
	})

	t.Run("execute with second valid message succeeds", func(t *testing.T) {
		encoded, msgID, verifierBlob := stack.buildValidMessage(t, localChainSelector, 5, []byte("second-message"))

		err := stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{verifierBlob}, 0)
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}

		state, err := stack.OfframpClient.GetExecutionState(ctx, msgID)
		if err != nil {
			t.Fatalf("GetExecutionState: %v", err)
		}
		if state != offrampbindings.MessageExecutionStateSuccess {
			t.Fatalf("execution state = %d, want Success", state)
		}
		t.Logf("Second message executed successfully; ID %x", msgID[:8])
	})

	// ========================================
	// Source Chain Config read-back (fills the "apply source chain config" gap)
	// ========================================

	t.Run("source chain config applied and enabled for remote source chain", func(t *testing.T) {
		// deployFullStack applied the source chain config for remoteSourceChain
		// (99999) with stack.VvrID as the default CCV. Read it back to prove the
		// apply took effect and the chain is enabled — the execute subtests above
		// succeed only because this config is present.
		cfg, err := stack.OfframpClient.GetSourceChainConfig(ctx, remoteSourceChain)
		if err != nil {
			t.Fatalf("GetSourceChainConfig(%d): %v", remoteSourceChain, err)
		}
		if cfg == nil {
			t.Fatal("expected a source chain config, got nil")
		}
		if !cfg.IsEnabled {
			t.Errorf("source chain %d should be enabled", remoteSourceChain)
		}
		found := false
		for _, ccv := range cfg.DefaultCcvs {
			if ccv == stack.VvrID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("default CCVs %v should contain the deployed VVR %s", cfg.DefaultCcvs, stack.VvrID)
		}
		t.Logf("source chain config read back: enabled=%v default_ccvs=%v", cfg.IsEnabled, cfg.DefaultCcvs)
	})

	// ========================================
	// Execute negative paths — hard contract errors (caller sees
	// Error(Contract, #N); execution state stays Untouched; no event emitted)
	// ========================================

	t.Run("execute with source chain not enabled", func(t *testing.T) {
		// Craft a message whose source chain selector (88888) has no
		// SourceChainConfig. The offramp rejects this in the pre-check zone with
		// #100 SourceChainNotEnabled, before any verification/release runs.
		encoded, err := encodeCcipMessageV1(ccipV1Wire{
			SourceChainSelector: 88888, // unconfigured source chain
			DestChainSelector:   localChainSelector,
			SequenceNumber:      10,
			ExecutionGasLimit:   500_000,
			CcipReceiveGasLimit: 200_000,
			Finality:            0,
			CcvExecutorHash:     [32]byte{},
			OnRampAddress:       stack.OnRampWire,
			OffRampAddress:      stack.OffRampSuffix,
			Sender:              bytes.Repeat([]byte{0xcd}, 20),
			Receiver:            stack.ReceiverRaw,
			DestBlob:            nil,
			TokenTransfer:       nil,
			Data:                []byte("src-not-enabled"),
		})
		if err != nil {
			t.Fatalf("encodeCcipMessageV1: %v", err)
		}
		msgID := keccak256MessageID(encoded)
		verifierBlob := stack.signVerifierBlob(t, msgID)

		err = stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{verifierBlob}, 0)
		if err == nil {
			t.Fatal("Expected error for unconfigured source chain, got nil")
		}
		if !strings.Contains(err.Error(), "Error(Contract") {
			t.Fatalf("Expected contract error, got: %v", err)
		}
		t.Logf("Unconfigured source chain correctly rejected: %v", err)

		// Hard pre-check error → execution state stays Untouched.
		state, sErr := stack.OfframpClient.GetExecutionState(ctx, msgID)
		if sErr != nil {
			t.Fatalf("GetExecutionState: %v", sErr)
		}
		if state != offrampbindings.MessageExecutionStateUntouched {
			t.Errorf("state should stay Untouched after a pre-check error, got %d", state)
		}
	})

	t.Run("execute with CCV length mismatch", func(t *testing.T) {
		// Valid message, but len(ccvs) != len(verifierResults) → #107
		// CCVLengthMismatch (pre-check zone, before InProgress).
		encoded, msgID, _ := stack.buildValidMessage(t, localChainSelector, 11, []byte("ccv-len-mismatch"))

		err := stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{}, 0)
		if err == nil {
			t.Fatal("Expected error on CCV length mismatch, got nil")
		}
		if !strings.Contains(err.Error(), "Error(Contract") {
			t.Fatalf("Expected contract error, got: %v", err)
		}
		t.Logf("CCV length mismatch correctly rejected: %v", err)

		state, _ := stack.OfframpClient.GetExecutionState(ctx, msgID)
		if state != offrampbindings.MessageExecutionStateUntouched {
			t.Errorf("state should stay Untouched, got %d", state)
		}
	})

	t.Run("execute with gas limit override too low", func(t *testing.T) {
		// buildValidMessage sets ccip_receive_gas_limit = 200_000. A non-zero
		// override below that → #110 GasLimitOverrideTooLow (pre-check zone).
		// NOTE: on Stellar the override is a validation parameter only — it is
		// NOT forwarded to the receiver call — so this surfaces as a hard #110
		// error, NOT a retryable Failure (diverges from EVM's low-gas→Failure
		// semantics, which have no Stellar equivalent).
		encoded, msgID, _ := stack.buildValidMessage(t, localChainSelector, 12, []byte("gas-too-low"))

		err := stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{stack.signVerifierBlob(t, msgID)}, 100)
		if err == nil {
			t.Fatal("Expected error for too-low gas limit override, got nil")
		}
		if !strings.Contains(err.Error(), "Error(Contract") {
			t.Fatalf("Expected contract error, got: %v", err)
		}
		t.Logf("Too-low gas limit override correctly rejected: %v", err)

		state, _ := stack.OfframpClient.GetExecutionState(ctx, msgID)
		if state != offrampbindings.MessageExecutionStateUntouched {
			t.Errorf("state should stay Untouched, got %d", state)
		}
	})

	t.Run("execute with invalid offramp address in message", func(t *testing.T) {
		// Valid source chain (99999) and destination, but the message's offramp
		// address field does not match this offramp → #103 InvalidOffRampAddress
		// (pre-check zone).
		encoded, err := encodeCcipMessageV1(ccipV1Wire{
			SourceChainSelector: remoteSourceChain,
			DestChainSelector:   localChainSelector,
			SequenceNumber:      13,
			ExecutionGasLimit:   500_000,
			CcipReceiveGasLimit: 200_000,
			Finality:            0,
			CcvExecutorHash:     [32]byte{},
			OnRampAddress:       stack.OnRampWire,
			OffRampAddress:      bytes.Repeat([]byte{0xEE}, 32), // wrong offramp
			Sender:              bytes.Repeat([]byte{0xcd}, 20),
			Receiver:            stack.ReceiverRaw,
			DestBlob:            nil,
			TokenTransfer:       nil,
			Data:                []byte("bad-offramp"),
		})
		if err != nil {
			t.Fatalf("encodeCcipMessageV1: %v", err)
		}
		msgID := keccak256MessageID(encoded)
		verifierBlob := stack.signVerifierBlob(t, msgID)

		err = stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{verifierBlob}, 0)
		if err == nil {
			t.Fatal("Expected error for invalid offramp address, got nil")
		}
		if !strings.Contains(err.Error(), "Error(Contract") {
			t.Fatalf("Expected contract error, got: %v", err)
		}
		t.Logf("Invalid offramp address correctly rejected: %v", err)

		state, _ := stack.OfframpClient.GetExecutionState(ctx, msgID)
		if state != offrampbindings.MessageExecutionStateUntouched {
			t.Errorf("state should stay Untouched, got %d", state)
		}
	})

	// ========================================
	// Execute soft-failure paths — retryable Failure state
	// (Execute returns nil; state == Failure; ExecutionStateChanged emitted).
	// These arise inside execute_single_message, AFTER state is set to
	// InProgress, so the contract records a retryable Failure rather than
	// reverting.
	// ========================================

	t.Run("execute with invalid CCV resolver address", func(t *testing.T) {
		// Supply a contract address that is NOT the configured required CCV (the
		// deployed VVR). The required CCV is then absent from the supplied ccvs,
		// so ensure_quorum_present fails inside execute_single_message → the
		// caller sees Ok with state=Failure (#116 RequiredCCVMissing). The bogus
		// address is never invoked (quorum-presence is checked before any VVR
		// resolution), so this does NOT trap.
		encoded, msgID, _ := stack.buildValidMessage(t, localChainSelector, 14, []byte("bad-ccv-resolver"))
		badCcv := helpers.GenerateMockContractID(t, deployerKP.Address(), "bad-ccv-resolver")

		err := stack.OfframpClient.Execute(ctx, encoded, []string{badCcv}, [][]byte{stack.signVerifierBlob(t, msgID)}, 0)
		if err != nil {
			t.Fatalf("Execute should return Ok (soft failure) for a missing required CCV, got err: %v", err)
		}
		state, sErr := stack.OfframpClient.GetExecutionState(ctx, msgID)
		if sErr != nil {
			t.Fatalf("GetExecutionState: %v", sErr)
		}
		if state != offrampbindings.MessageExecutionStateFailure {
			t.Fatalf("expected Failure state (required CCV missing), got %d", state)
		}
		t.Logf("Invalid CCV resolver correctly produced retryable Failure (state=%d)", state)
	})

	t.Run("execute with empty CCV set requires inline required CCVs not defaults", func(t *testing.T) {
		// ccvs=[] / verifierResults=[]: the default/lane-mandated CCVs form the
		// REQUIRED set and must be supplied inline — they are NOT auto-verified
		// from stored attestations. Empty inline ccvs ⇒ required set absent ⇒
		// Ok-with-Failure (#116 RequiredCCVMissing). This corrects the original
		// placeholder's expectation that execution would SUCCEED via defaults:
		// on Stellar (as on EVM) the caller must supply the required CCV
		// attestations inline to execute; there is no auto-apply of defaults.
		encoded, msgID, _ := stack.buildValidMessage(t, localChainSelector, 15, []byte("empty-ccv"))

		err := stack.OfframpClient.Execute(ctx, encoded, []string{}, [][]byte{}, 0)
		if err != nil {
			t.Fatalf("Execute should return Ok (soft failure) for empty inline CCVs, got err: %v", err)
		}
		state, sErr := stack.OfframpClient.GetExecutionState(ctx, msgID)
		if sErr != nil {
			t.Fatalf("GetExecutionState: %v", sErr)
		}
		if state != offrampbindings.MessageExecutionStateFailure {
			t.Fatalf("expected Failure state (no inline required CCVs), got %d", state)
		}
		t.Logf("Empty CCV set correctly produced retryable Failure (state=%d) — defaults are not auto-applied", state)
	})
}
