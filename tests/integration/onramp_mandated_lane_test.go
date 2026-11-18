//go:build integration

// CCV-merge scenario coverage at the Go-binding / deployed-Wasm layer, closing
// the "LaneMandatedCcvs is nil in every Go lane config" gap from the CCV
// coverage audit. Four scenarios over one stack + one outbound wire, matching
// the EVM merge semantics (OnRamp.sol `_resolveUserOrDefaultCCVs` L802-810 +
// `_mergeCCVLists` L272) and the Rust unit tests in contracts/onramp/src/test.rs:
//
//   - Scenario 3: data message, EMPTY user CCVs, defaults-only lane (M=nil)
//     ⇒ merged CCVs = [default] — the plain default-UX path (previously only
//     exercised in Go with EXPLICIT user CCVs or on token-only sends).
//   - Scenario 2 (REGRESSION for the validated mandated-suppresses-defaults
//     finding): data message, EMPTY user CCVs, lane with BOTH default_ccvs and
//     lane_mandated_ccvs ⇒ merged = [default, mandated]. Pre-fix the OnRamp
//     committed mandated alone; the destination OffRamp folds defaults into
//     the REQUIRED set for receivers without a CCV policy and reverts
//     RequiredCCVMissing — permanently stuck messages. Asserts the default
//     verifier is both committed (receipt present) and PAID (fee > 0).
//   - Scenario 8: the same D+M lane, but the user SUPPLIES a CCV ⇒ merged =
//     user list verbatim (mandated deduped in, defaults NOT expanded onto a
//     non-empty user list — EVM only substitutes defaults for an EMPTY list).
//     This is also the reachable cross-role dedup case (user CCV == mandated
//     CCV appears once).
//   - Scenario 9 (boundary): mandated overlapping default is NOT a legal lane —
//     config validation rejects D ∩ M ≠ ∅ with InvalidConfig #52 (H-11 /
//     INV-CFG-7, EVM _validateDefaultAndMandatedCCVs parity), so default↔
//     mandated dedup is unreachable from config.
//
// Receipt layout for a data-only message is [CCV_0..CCV_N, Executor, Network],
// issuers in merge order, so index-equality assertions pin the ordering.
package integration

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
)

// TestOnRampMandatedLaneCcvMerge covers the four scenarios above through the
// real Router → OnRamp → merge → receipts path on-network.
func TestOnRampMandatedLaneCcvMerge(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, networkPassphrase, friendbotURL := GetIsolatedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	const localSourceChain = uint64(11111)
	const remoteDestChain = uint64(22222)
	const saltPrefix = "mlane-merge"

	stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localSourceChain, saltPrefix, false)
	feeToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, saltPrefix+"-fee")

	// No transfer tokens — every scenario sends a data-only message (non-empty
	// data ⇒ NOT token-only, so the user-fallback defaults apply when the user
	// list is empty).
	wire := deployOutboundSendWire(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix, stack,
		localSourceChain, remoteDestChain, feeToken, nil)

	vvrID := stack.VvrID
	executorID := stack.ExecutorID
	routerID := stack.RouterID

	// sendDataOnly sends a data message with the given user CCV list (empty args
	// per entry, matching how onramp_test.go supplies explicit CCVs) and returns
	// the receipts from the CCIPMessageSent event. Receipt extraction runs
	// immediately after ccip_send.
	sendDataOnly := func(t *testing.T, ccvs []string) []onrampbindings.Receipt {
		t.Helper()
		ccvArgs := make([][]byte, len(ccvs))
		for i := range ccvArgs {
			ccvArgs[i] = []byte{}
		}
		extraArgs, err := encodeOnrampExtraArgsV3(onrampbindings.GenericExtraArgsV3{
			Ccvs:               ccvs,
			CcvArgs:            ccvArgs,
			Executor:           executorID,
			ExecutorArgs:       []byte{},
			GasLimit:           0,
			BlockConfirmations: 0,
			TokenReceiver:      []byte{},
			TokenArgs:          []byte{},
		})
		if err != nil {
			t.Fatalf("encode extra args: %v", err)
		}

		evmReceiver := make([]byte, 20)
		for i := range evmReceiver {
			evmReceiver[i] = 0x33
		}
		msg := routerbindings.StellarToAnyMessage{
			Receiver:  evmReceiver,
			Data:      []byte("ccv merge scenario"),
			FeeToken:  feeToken,
			ExtraArgs: extraArgs,
		}

		requiredFee, err := stack.RouterClient.GetFee(ctx, remoteDestChain, msg)
		if err != nil {
			t.Fatalf("Router GetFee: %v", err)
		}
		if requiredFee.Sign() <= 0 {
			t.Fatalf("expected positive fee, got %s", requiredFee.String())
		}
		latest, err := rpcClient.GetLatestLedger(ctx)
		if err != nil {
			t.Fatalf("GetLatestLedger: %v", err)
		}
		msgID, err := stack.RouterClient.CcipSend(ctx, deployerAddr, remoteDestChain, msg)
		if err != nil {
			t.Fatalf("Router CcipSend: %v", err)
		}
		if msgID == ([32]byte{}) {
			t.Fatal("CcipSend returned empty message_id")
		}
		sentEvt, err := wire.OnRampClient.WaitForCCIPMessageSentEvent(ctx, latest.Sequence, 30*time.Second,
			func(e *onrampbindings.CCIPMessageSentEvent) bool {
				return e.DestChainSelector == remoteDestChain && bytes.Equal(e.MessageId[:], msgID[:])
			})
		if err != nil {
			t.Fatalf("WaitForCCIPMessageSentEvent: %v", err)
		}
		return sentEvt.Receipts
	}

	// reapplyLane re-applies the OnRamp dest-chain config (same shape
	// deployOutboundSendWire installed) with a caller-supplied mandated list.
	reapplyLane := func(mandated []string) {
		offRampEVM := make([]byte, 20)
		if err := wire.OnRampClient.ApplyDestChainConfigUpdates(ctx, []onrampbindings.DestChainConfigArgs{{
			DestChainSelector:         remoteDestChain,
			Router:                    stack.RouterID,
			AddressBytesLength:        20,
			TokenReceiverAllowed:      true,
			MessageNetworkFeeUsdCents: 50,
			TokenNetworkFeeUsdCents:   100,
			BaseExecutionGasCost:      200_000,
			DefaultExecutor:           executorID,
			ExecutionFeeUsdCents:      25,
			LaneMandatedCcvs:          mandated,
			DefaultCcvs:               []string{vvrID},
			OffRamp:                   offRampEVM,
		}}); err != nil {
			t.Fatalf("OnRamp ApplyDestChainConfigUpdates (mandated=%v): %v", mandated, err)
		}
	}

	// assertCcvs checks the receipt count, the CCV issuers in merge order (the
	// first len(want) receipts), and that every CCV receipt is PAID (> 0).
	assertCcvs := func(t *testing.T, rcpts []onrampbindings.Receipt, want []string) {
		t.Helper()
		// data-only layout: len(want) CCVs + executor + network
		if len(rcpts) != len(want)+2 {
			t.Fatalf("receipts: want %d ([%v CCVs, executor, network]), got %d: %v",
				len(want)+2, want, len(rcpts), receiptIssuers(rcpts))
		}
		for i, wantIssuer := range want {
			if got := rcpts[i].Issuer; got != wantIssuer {
				t.Errorf("receipt[%d].Issuer: want %s (merge order), got %v", i, wantIssuer, receiptIssuers(rcpts))
			}
			if rcpts[i].FeeTokenAmount == nil || rcpts[i].FeeTokenAmount.Sign() <= 0 {
				t.Errorf("CCV receipt[%d] (%s) must be PAID (> 0), got %v",
					i, wantIssuer, rcpts[i].FeeTokenAmount)
			}
		}
	}

	// ------------------------------------------------------------------
	// Scenario 3: pristine lane (D=[vvrID], M=nil), empty user CCVs.
	// ------------------------------------------------------------------
	t.Run("empty user CCVs on a defaults-only lane commit the default", func(t *testing.T) {
		rcpts := sendDataOnly(t, nil)
		assertCcvs(t, rcpts, []string{vvrID})
		if !contains(receiptIssuers(rcpts), routerID) {
			t.Errorf("receipts must contain the router (network), got %v", receiptIssuers(rcpts))
		}
	})

	// ccvB: a second VVR resolving to the stack's committee verifier, with its
	// OWN outbound impl mapping (deploySecondVvr only wires the inbound impl) so
	// OnRamp get_fee can price it.
	ccvB := deploySecondVvr(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix, stack.CcvID)
	ccvBClient := vvrbindings.NewVersionedVerifierResolverClient(deployer, ccvB)
	ccvImpl := stack.CcvID
	if err := ccvBClient.ApplyOutboundImplUpdates(ctx, []vvrbindings.OutboundImplementationUpdate{
		{DestChainSelector: remoteDestChain, Verifier: &ccvImpl},
	}); err != nil {
		t.Fatalf("ccvB ApplyOutboundImplUpdates: %v", err)
	}

	// From here on the lane carries BOTH defaults and a mandate.
	reapplyLane([]string{ccvB})

	// ------------------------------------------------------------------
	// Scenario 2 — REGRESSION for the validated finding: empty user CCVs on a
	// D+M lane must commit [default, mandated] (EVM OnRamp.sol:802-810 + :272),
	// not mandated alone. Pre-fix this send emitted 3 receipts ([ccvB, executor,
	// network]) with the default verifier neither committed nor paid — the
	// destination then reverts RequiredCCVMissing for policy-less receivers.
	// ------------------------------------------------------------------
	t.Run("empty user CCVs on a mandated lane still commit and pay the default", func(t *testing.T) {
		rcpts := sendDataOnly(t, nil)
		assertCcvs(t, rcpts, []string{vvrID, ccvB})
	})

	// ------------------------------------------------------------------
	// Scenario 8: the user SUPPLIES a CCV on the same D+M lane. The merged list
	// is the user list verbatim (mandated deduped against it); defaults are NOT
	// expanded onto a non-empty user list (EVM substitutes defaults only for an
	// EMPTY list). User supplies ccvB (also the mandated CCV): merged = [ccvB]
	// — if defaults were wrongly folded in, this would be [ccvB, vvrID].
	// ------------------------------------------------------------------
	t.Run("user-supplied CCV keeps defaults out and dedups the mandate", func(t *testing.T) {
		rcpts := sendDataOnly(t, []string{ccvB})
		assertCcvs(t, rcpts, []string{ccvB})
		if contains(receiptIssuers(rcpts), vvrID) {
			t.Errorf("defaults must NOT be folded onto a non-empty user list, got %v", receiptIssuers(rcpts))
		}
	})

	// ------------------------------------------------------------------
	// Scenario 9 (corrected): mandated == default is NOT a legal lane —
	// `DestChainConfigArgs::validate` rejects any CCV present in BOTH the
	// default and lane-mandated sets (H-11 / INV-CFG-7, EVM
	// `CCVConfigValidation._validateDefaultAndMandatedCCVs` parity), reverting
	// InvalidConfig #52. The config layer therefore guarantees D ∩ M = ∅, so
	// the merge-level default↔mandated dedup is unreachable from config; the
	// reachable cross-role dedup (user ↔ mandated) is covered by scenario 8.
	// ------------------------------------------------------------------
	t.Run("mandated overlapping default is rejected as invalid config (#52)", func(t *testing.T) {
		offRampEVM := make([]byte, 20)
		err := wire.OnRampClient.ApplyDestChainConfigUpdates(ctx, []onrampbindings.DestChainConfigArgs{{
			DestChainSelector:         remoteDestChain,
			Router:                    stack.RouterID,
			AddressBytesLength:        20,
			TokenReceiverAllowed:      true,
			MessageNetworkFeeUsdCents: 50,
			TokenNetworkFeeUsdCents:   100,
			BaseExecutionGasCost:      200_000,
			DefaultExecutor:           executorID,
			ExecutionFeeUsdCents:      25,
			LaneMandatedCcvs:          []string{vvrID}, // same CCV as DefaultCcvs
			DefaultCcvs:               []string{vvrID},
			OffRamp:                   offRampEVM,
		}})
		if err == nil {
			t.Fatal("ApplyDestChainConfigUpdates with mandated ∩ default ≠ ∅ must revert")
		}
		msg := err.Error()
		if !strings.Contains(msg, "Error(Contract") || !strings.Contains(msg, fmt.Sprintf("#%d", onrampbindings.CCIPErrorInvalidConfig)) {
			t.Fatalf("want InvalidConfig Error(Contract, #%d), got: %v", onrampbindings.CCIPErrorInvalidConfig, err)
		}
	})
}
