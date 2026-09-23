//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"
	"time"

	cciprecv "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ccip_receiver"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// TestOffRampReceiverCcvConsultation exercises the C-1 receiver CCV consultation
// and the H-7 receiver allowed-finality enforcement through the real OffRamp execute
// path. deployFullStack deploys the example receiver but never configures a CCV
// consultation, so every existing execute test runs the EMPTY-consultation case (the
// lane default CCV folds in). This test configures a non-empty consultation via the
// receiver's apply_ccv_config_updates and asserts the OffRamp enforces the receiver-
// reported required/optional/threshold (C-1) and allowed-finality (H-7).
//
// CRITICAL harness behavior: OffRamp::execute swallows inner rejections (missing
// required CCV, optional-quorum-not-reached, disallowed finality, …) into
// MessageExecutionState::Failure and returns Ok(()) — so Execute returns nil even on
// a recorded failure. Assertions are therefore made via GetExecutionState and the
// ExecutionStateChangedEvent.return_data, whose first 4 bytes are the big-endian
// CCIPError code (116=RequiredCCVMissing, 118=OptionalCCVQuorumNotReached,
// 315=InvalidRequestedFinality). Only OUTER errors (e.g. MessageAlreadyExecuted on
// re-executing a Success) surface as a Go error from Execute.
//
// A second CCV (ccvB) is a second VVR whose inbound implementation is the SAME
// committee verifier (CcvID) the stack already registered a signer on. The verifier
// blob commits to version_tag ‖ message_hash (not the CCV address), so one signed
// blob validates for any CCV that resolves to that verifier — letting a single signer
// attest both VvrID and ccvB without a second committee.
func TestOffRampReceiverCcvConsultation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, _, _ := GetSharedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localChainSelector, "recv-ccv", false)

	// Second VVR (ccvB) resolving to the stack's committee verifier, so the existing
	// signer can attest it. The lane default CCV (VvrID) already resolves there.
	ccvB := deploySecondVvr(ctx, t, projectRoot, deployer, deployerAddr, "recv-ccv", stack.CcvID)

	recvClient := cciprecv.NewExampleCcipReceiverClient(deployer, stack.ReceiverID)

	// applyReceiverCcvConfig sets the receiver's per-source-chain consultation config.
	applyReceiverCcvConfig := func(t *testing.T, required, optional []string, threshold uint32) {
		t.Helper()
		if err := recvClient.ApplyCcvConfigUpdates(ctx, deployerAddr, []cciprecv.CcvConfigUpdate{{
			SourceChainSelector: remoteSourceChain,
			RequiredCcvs:        required,
			OptionalCcvs:        optional,
			OptionalThreshold:   threshold,
		}}); err != nil {
			t.Fatalf("ApplyCcvConfigUpdates: %v", err)
		}
	}

	// buildConsultMessage builds a non-token-only message (non-zero data + receive gas
	// limit ⇒ the consult path, not the token-only branch) with a configurable finality.
	buildConsultMessage := func(t *testing.T, seqNo uint64, finality uint32) (encoded []byte, msgID [32]byte) {
		t.Helper()
		sender := bytes.Repeat([]byte{0xcd}, 20)
		var ccvHashZero [32]byte
		var err error
		encoded, err = encodeCcipMessageV1(ccipV1Wire{
			SourceChainSelector: remoteSourceChain,
			DestChainSelector:   localChainSelector,
			SequenceNumber:      seqNo,
			ExecutionGasLimit:   500_000,
			CcipReceiveGasLimit: 200_000, // non-zero ⇒ non-token-only ⇒ receiver is consulted
			Finality:            finality,
			CcvExecutorHash:     ccvHashZero,
			OnRampAddress:       stack.OnRampWire,
			OffRampAddress:      stack.OffRampSuffix,
			Sender:              sender,
			Receiver:            stack.ReceiverRaw,
			Data:                []byte("receiver-ccv-consultation"),
		})
		if err != nil {
			t.Fatalf("encodeCcipMessageV1: %v", err)
		}
		msgID = keccak256MessageID(encoded)
		return encoded, msgID
	}

	// executeAndAwait runs OffRamp.execute and returns the resulting execution state +
	// error code (0 for Success). One verifier blob per CCV; the blob is CCV-agnostic
	// (see file header) so the same signed blob attests every supplied CCV.
	executeAndAwait := func(t *testing.T, seqNo uint64, finality uint32, ccvs []string) (offrampbindings.MessageExecutionState, uint32) {
		t.Helper()
		encoded, msgID := buildConsultMessage(t, seqNo, finality)

		blobs := make([][]byte, len(ccvs))
		for i := range ccvs {
			blobs[i] = stack.signVerifierBlob(t, msgID)
		}

		latest, err := rpcClient.GetLatestLedger(ctx)
		if err != nil {
			t.Fatalf("GetLatestLedger: %v", err)
		}

		if err := stack.OfframpClient.Execute(ctx, encoded, ccvs, blobs, 0); err != nil {
			// Only outer errors reach here (inner rejections are swallowed into Failure).
			t.Fatalf("Execute (outer): %v", err)
		}

		evt, err := stack.OfframpClient.WaitForExecutionStateChangedEvent(ctx, latest.Sequence, 30*time.Second,
			func(e *offrampbindings.ExecutionStateChangedEvent) bool {
				return bytes.Equal(e.MessageId[:], msgID[:])
			})
		if err != nil {
			t.Fatalf("WaitForExecutionStateChangedEvent: %v", err)
		}

		var code uint32
		if evt.State == offrampbindings.MessageExecutionStateFailure && len(evt.ReturnData) >= 4 {
			code = binary.BigEndian.Uint32(evt.ReturnData[:4])
		}
		return evt.State, code
	}

	// ------------------------------------------------------------------
	// 1. Off-chain view resolves the receiver-configured CCVs (C-1) and
	//    suppresses the lane default when the receiver reports a non-empty
	//    required set.
	// ------------------------------------------------------------------
	t.Run("off-chain get_ccvs_for_message resolves receiver config and suppresses lane default (C-1)", func(t *testing.T) {
		applyReceiverCcvConfig(t, []string{ccvB}, []string{stack.VvrID}, 1)

		// Receiver-side: the stored consultation (pre-merge).
		got, err := recvClient.GetCcvsAndFinalityConfig(ctx, remoteSourceChain, bytes.Repeat([]byte{0xcd}, 20))
		if err != nil {
			t.Fatalf("GetCcvsAndFinalityConfig: %v", err)
		}
		if len(got.RequiredCcvs) != 1 || got.RequiredCcvs[0] != ccvB {
			t.Fatalf("receiver required: want [%s], got %v", ccvB, got.RequiredCcvs)
		}
		if len(got.OptionalCcvs) != 1 || got.OptionalCcvs[0] != stack.VvrID {
			t.Fatalf("receiver optional: want [%s], got %v", stack.VvrID, got.OptionalCcvs)
		}
		if got.OptionalThreshold != 1 {
			t.Fatalf("receiver optional_threshold: want 1, got %d", got.OptionalThreshold)
		}

		// OffRamp view (post-merge): the DON gathers this. The lane default (VvrID)
		// must NOT be in required — a non-empty receiver-required set suppresses the
		// default-CCV fold-in.
		encoded, _ := buildConsultMessage(t, 1, 0)
		req, opt, threshold, err := stack.OfframpClient.GetCcvsForMessage(ctx, encoded)
		if err != nil {
			t.Fatalf("GetCcvsForMessage: %v", err)
		}
		if !contains(req, ccvB) {
			t.Fatalf("view required must contain receiver-required %s, got %v", ccvB, req)
		}
		if contains(req, stack.VvrID) {
			t.Fatalf("view required must NOT contain lane default %s (suppressed), got %v", stack.VvrID, req)
		}
		if !contains(opt, stack.VvrID) {
			t.Fatalf("view optional must contain %s, got %v", stack.VvrID, opt)
		}
		if threshold != 1 {
			t.Fatalf("view optional_threshold: want 1, got %d", threshold)
		}
		t.Logf("C-1 view: required=%v optional=%v threshold=%d (lane default suppressed)", req, opt, threshold)
	})

	// ------------------------------------------------------------------
	// 2. Receiver-required CCV not supplied ⇒ Failure(116). Supplying the
	//    lane default (VvrID) does NOT satisfy a receiver-required ccvB —
	//    proving the receiver, not the lane, dictates required CCVs.
	// ------------------------------------------------------------------
	t.Run("execute fails when receiver-required CCV is not supplied (RequiredCCVMissing=116)", func(t *testing.T) {
		applyReceiverCcvConfig(t, []string{ccvB}, nil, 0)

		state, code := executeAndAwait(t, 2, 0, []string{stack.VvrID}) // supply lane default, NOT ccvB
		if state != offrampbindings.MessageExecutionStateFailure {
			t.Fatalf("state: want Failure, got %d", state)
		}
		if code != offrampbindings.CCIPErrorRequiredCCVMissing {
			t.Fatalf("error code: want RequiredCCVMissing(%d), got %d", offrampbindings.CCIPErrorRequiredCCVMissing, code)
		}
		t.Logf("correctly failed: receiver-required %s missing (lane default did not satisfy it)", ccvB)
	})

	t.Run("execute succeeds when the receiver-required CCV is supplied", func(t *testing.T) {
		// config unchanged from above: required=[ccvB].
		state, code := executeAndAwait(t, 3, 0, []string{ccvB})
		if state != offrampbindings.MessageExecutionStateSuccess {
			t.Fatalf("state: want Success, got %d (code=%d)", state, code)
		}
		t.Log("correctly succeeded: receiver-required CCV supplied")
	})

	// ------------------------------------------------------------------
	// 3. Optional-CCV threshold enforcement (C-1). Required met but the
	//    optional threshold not reached ⇒ Failure(118); meeting it ⇒ Success.
	// ------------------------------------------------------------------
	t.Run("execute fails when optional-CCV threshold not met (OptionalCCVQuorumNotReached=118)", func(t *testing.T) {
		applyReceiverCcvConfig(t, []string{ccvB}, []string{stack.VvrID}, 1)

		state, code := executeAndAwait(t, 4, 0, []string{ccvB}) // required met, optional not supplied
		if state != offrampbindings.MessageExecutionStateFailure {
			t.Fatalf("state: want Failure, got %d", state)
		}
		if code != offrampbindings.CCIPErrorOptionalCCVQuorumNotReached {
			t.Fatalf("error code: want OptionalCCVQuorumNotReached(%d), got %d", offrampbindings.CCIPErrorOptionalCCVQuorumNotReached, code)
		}
		t.Log("correctly failed: optional-CCV threshold (1) not met")
	})

	t.Run("execute succeeds when optional-CCV threshold is met", func(t *testing.T) {
		// config unchanged: required=[ccvB], optional=[VvrID], threshold=1.
		state, code := executeAndAwait(t, 5, 0, []string{ccvB, stack.VvrID})
		if state != offrampbindings.MessageExecutionStateSuccess {
			t.Fatalf("state: want Success, got %d (code=%d)", state, code)
		}
		t.Log("correctly succeeded: optional-CCV threshold met")
	})

	// ------------------------------------------------------------------
	// 4. Receiver allowed-finality enforcement (H-7). The receiver's
	//    allowed_finality_config (0 = WAIT_FOR_FINALITY only, set by
	//    deployFullStack) disallows a WAIT_FOR_SAFE (0x10000) request.
	// ------------------------------------------------------------------
	t.Run("execute fails when requested finality is disallowed by receiver (InvalidRequestedFinality=315, H-7)", func(t *testing.T) {
		// Empty consultation ⇒ lane default (VvrID) folds into required, so quorum
		// is satisfiable — isolating the failure to the finality gate.
		applyReceiverCcvConfig(t, nil, nil, 0)

		const waitForSafe = 1 << 16 // WAIT_FOR_SAFE_FLAG (finality_codec.rs)
		state, code := executeAndAwait(t, 6, waitForSafe, []string{stack.VvrID})
		if state != offrampbindings.MessageExecutionStateFailure {
			t.Fatalf("state: want Failure, got %d", state)
		}
		if code != cciprecv.CCIPErrorInvalidRequestedFinality {
			t.Fatalf("error code: want InvalidRequestedFinality(%d), got %d", cciprecv.CCIPErrorInvalidRequestedFinality, code)
		}
		t.Logf("correctly failed: WAIT_FOR_SAFE (0x%x) disallowed by receiver allowed_finality=0", waitForSafe)
	})
}

// deploySecondVvr deploys a second VersionedVerifierResolver whose inbound
// implementation is the stack's committee verifier (ccvID). This makes the returned
// address a resolvable, verifiable CCV: the OffRamp resolves it to ccvID, whose
// registered signer accepts the same verifier blob as the lane-default VVR.
func deploySecondVvr(
	ctx context.Context,
	t *testing.T,
	projectRoot string,
	deployer *deployment.Deployer,
	deployerAddr, saltPrefix, ccvID string,
) string {
	t.Helper()

	salt := deployment.GenerateDeterministicSalt(deployerAddr, saltPrefix+"-vvr-b")
	p := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "ccvs_versioned_verifier_resolver.wasm")
	id, err := deployer.DeployContract(ctx, p, salt)
	if err != nil {
		t.Fatalf("deploy second vvr: %v", err)
	}

	client := vvrbindings.NewVersionedVerifierResolverClient(deployer, id)
	mockFeeAgg := helpers.GenerateMockContractID(t, deployerAddr, saltPrefix+"-vvr-b-feeagg")
	if err := client.Initialize(ctx, deployerAddr, mockFeeAgg); err != nil {
		t.Fatalf("second vvr Initialize: %v", err)
	}
	ver := ccvID
	if err := client.ApplyInboundImplUpdates(ctx, []vvrbindings.InboundImplementationUpdate{{
		Verifier: &ver,
		Version:  stellarutil.DefaultCommitteeVerifierVersionTag(),
	}}); err != nil {
		t.Fatalf("second vvr ApplyInboundImplUpdates: %v", err)
	}
	t.Logf("second VVR (ccvB) deployed at %s, inbound impl → committee verifier %s", id, ccvID)
	return id
}

// contains is a simple string-slice membership check used for CCV list assertions
// (the contract may order the merged list differently than the config).
func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
