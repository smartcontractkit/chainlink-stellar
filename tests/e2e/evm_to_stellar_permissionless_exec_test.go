package e2e_tests

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	offrampoperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/offramp"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-common/pkg/utils/tests"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// TestEVMToStellarPermissionlessManualExecute positively proves that anyone —
// not just the DON's off-chain executor service — can execute a *verified*
// CCIP message on the Stellar OffRamp. This is the "permissionless executor"
// requirement: "Anyone is able to act as an executor for CCIP messages once
// messages are verified, unless there's a specific allowlisting for execution."
//
// The Stellar OffRamp `execute` entrypoint has NO caller-auth gate — its own
// doc-comment states "This is permissionless — anyone can call it. Security
// comes from CCV attestations, not caller identity." (offramp lib.rs). It
// verifies the CCV quorum INLINE from the attestation blobs passed to the call
// (`verify_ccv_quorum` → `verify_ccvs_at` → each CCV's `verify_message`); it
// does NOT require a prior on-chain commit by the executor service. So a manual
// caller can execute the moment valid attestations are available.
//
// This test:
//  1. Sends EVM→Stellar and waits for the message to be verified & indexed
//     (AssertMessage), which makes the per-verifier attestations available.
//  2. Reads the OffRamp's current execution state for the message.
//  3. Assembles the manual-execute arguments (message + CCV addresses +
//     attestation blobs) from the indexed verifications — the same recipe the
//     `manualexec` CLI uses (build/devenv/cli/manualexec/command.go).
//  4. Calls Chain.ManuallyExecuteMessage, whose caller is env.Deployer — the
//     test's deployer account, which is NOT the DON executor service and holds
//     no execute-role. A successful ExecutionStateSuccess from this call is the
//     positive proof that an unregistered third party executed a verified
//     message (permissionless).
//
// Race note: the DON executor service also runs in the devenv and races to
// execute the same message. The OffRamp only accepts Untouched/Failure states,
// so whichever caller is first wins. Because execute verifies attestations
// inline (no commit prerequisite), our synchronous call made the instant
// verification is observed reliably wins. If the executor preempts us (state
// already terminal), the test skips this run rather than flaking — the
// permissionless property is also covered by the Rust `verify_ccv_quorum` /
// `execute` unit tests (offramp/src/test.rs) and the `manualexec` CLI.
//
// Prerequisites:
//
//	make build
//	CTF_CONFIGS=tests/env/env-stellar-evm.toml go run ./tests/testutils/cmd/devenv
//
// Then:
//
//	go test -v -timeout 20m ./tests/e2e/... -run TestEVMToStellarPermissionlessManualExecute
func TestEVMToStellarPermissionlessManualExecute(t *testing.T) {
	configOutputPath := "../env/env-stellar-evm-out.toml"

	stellarChainID := chainsel.STELLAR_LOCALNET.ChainID
	stellarSelector := chainsel.STELLAR_LOCALNET.Selector

	ctx := ccv.Plog.WithContext(t.Context())
	l := zerolog.Ctx(ctx)

	env := helpers.NewE2ETestEnv(t, ctx, l, configOutputPath, stellarChainID, stellarSelector)
	evmDetails := env.DestChainDetails
	stellarDetails := env.SourceChainDetails

	evmChain := env.Chains[evmDetails.ChainSelector]
	require.NotNil(t, evmChain, "EVM chain not found in chains map")

	stellarChain := env.Chains[stellarDetails.ChainSelector]
	require.NotNil(t, stellarChain, "Stellar chain not found in chains map")

	// Resolve the Stellar OffRamp contract from the CCV datastore (destination
	// of the EVM→Stellar message), mirroring TestEVMToStellarExecutionHappyPath.
	offRampKey := datastore.NewAddressRefKey(
		stellarDetails.ChainSelector,
		datastore.ContractType(offrampoperations.ContractType),
		offrampoperations.Version,
		"",
	)
	offrampRef, err := env.DataStore.Addresses().Get(offRampKey)
	require.NoError(t, err)
	require.NotEmpty(t, offrampRef.Address)
	offrampContractID, err := scval.HexToContractStrkey(offrampRef.Address)
	require.NoError(t, err)
	offrampClient := offrampbindings.NewOffRampClient(env.Deployer, offrampContractID)
	l.Info().Str("offrampContractID", offrampContractID).Msg("Resolved Stellar OffRamp for manual execute")

	t.Run("permissionless_manual_execute", func(t *testing.T) {
		stellarReceiver, err := stellarChain.GetEOAReceiverAddress()
		require.NoError(t, err)

		seqNo, err := evmChain.GetExpectedNextSequenceNumber(ctx, stellarDetails.ChainSelector)
		require.NoError(t, err)

		// Send an EVM→Stellar message. A non-zero gas + data makes it a
		// "real" message (not token-only), exercising the full execute path.
		sendResult, err := evmChain.SendMessage(ctx, stellarDetails.ChainSelector,
			cciptestinterfaces.MessageFields{
				Receiver: stellarReceiver,
				Data:     []byte("permissionless manual execute"),
			},
			cciptestinterfaces.MessageOptions{
				ExecutionGasLimit: 200_000,
			},
			messageV3Version,
		)
		require.NoError(t, err, "EVM send must succeed")
		l.Info().
			Str("messageID", hex.EncodeToString(sendResult.MessageID[:])).
			Msg("CCIP message sent from EVM (manual-execute target)")

		sentEvent, err := evmChain.ConfirmSendOnSource(ctx, stellarDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, evmSentTimeout)
		require.NoError(t, err)
		messageID := sentEvent.MessageID
		l.Info().Str("messageID", hex.EncodeToString(messageID[:])).Msg("Sent event confirmed on EVM")

		// Wait for verification + indexing. This makes the per-verifier
		// attestations available — the inputs a manual executor needs.
		defaultAggregatorClient := env.AggregatorClients[devenvcommon.DefaultCommitteeVerifierQualifier]
		require.NotNil(t, defaultAggregatorClient)
		testCtx := e2e.NewTestingContext(t, t.Context(), env.Chains, defaultAggregatorClient, env.IndexerMonitor)
		result, err := testCtx.AssertMessage(protocol.Bytes32(messageID), e2e.AssertMessageOptions{
			TickInterval:            1 * time.Second,
			ExpectedVerifierResults: 1,
			Timeout:                 tests.WaitTimeout(t),
			AssertVerifierLogs:      false,
			AssertExecutorLogs:      false,
		})
		require.NoError(t, err)
		require.NotEmpty(t, result.IndexedVerifications.Results,
			"need ≥1 indexed verification to assemble manual-execute arguments")

		// Assemble the manual-execute arguments from the indexed verifications,
		// exactly as the `manualexec` CLI does: each verifier result contributes
		// its destination CCV address and attestation blob; the message is taken
		// from the first result.
		verifs := result.IndexedVerifications.Results
		ccvs := make([]protocol.UnknownAddress, 0, len(verifs))
		verifierResults := make([][]byte, 0, len(verifs))
		for _, v := range verifs {
			ccvs = append(ccvs, v.VerifierResult.VerifierDestAddress)
			verifierResults = append(verifierResults, v.VerifierResult.CCVData)
		}
		msg := verifs[0].VerifierResult.Message
		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Int("ccvs", len(ccvs)).
			Msg("Assembled manual-execute args from indexed verifications")

		// Read the current execution state to know whether the automated
		// executor service already ran. OffRamp execute only accepts
		// Untouched/Failure; calling it on Success/InProgress reverts.
		state, err := offrampClient.GetExecutionState(ctx, messageID)
		require.NoError(t, err)
		l.Info().Uint32("stateBefore", uint32(state)).Msg("OffRamp execution state before manual execute")

		if state == offrampbindings.MessageExecutionStateSuccess ||
			state == offrampbindings.MessageExecutionStateInProgress {
			t.Skipf("automated executor already reached a terminal/in-progress state (state=%d) before the manual call; re-run to exercise the ManuallyExecuteMessage path",
				uint32(state))
		}

		// Permissionless manual execute. The caller is env.Deployer (the test's
		// deployer account — NOT the DON executor service, holding no execute
		// role). gasLimit 0 ⇒ no override, use the message's own gas limit.
		// A Success here is the positive proof: an unregistered third party
		// executed a verified message.
		execEvent, execErr := stellarChain.ManuallyExecuteMessage(t.Context(), msg, 0, ccvs, verifierResults)
		if execErr != nil {
			// The automated executor may have won the narrow race between our
			// state read and this call (execute only accepts Untouched/Failure).
			// Re-read the state: if it is now Success, the message WAS executed
			// (permissionlessly — the executor service is itself just another
			// unrestricted caller of the gateless `execute`) and we skip the
			// manual-path assertion for this run rather than flake.
			finalState, sErr := offrampClient.GetExecutionState(ctx, messageID)
			require.NoError(t, sErr, "failed to re-read execution state after manual-execute error")
			if finalState == offrampbindings.MessageExecutionStateSuccess {
				t.Skipf("automated executor preempted the manual execute in this narrow race (state now Success); re-run to exercise the ManuallyExecuteMessage path")
			}
			require.NoErrorf(t, execErr,
				"manual execute failed for a non-preemption reason, final state=%d", uint32(finalState))
		}

		require.Equalf(t, cciptestinterfaces.ExecutionStateSuccess, execEvent.State,
			"permissionless manual execute should succeed (caller = deployer, a non-executor-service account; OffRamp execute has no caller-auth gate), return data: %x",
			execEvent.ReturnData)
		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Uint64("seqNo", seqNo).
			Msg("✅ permissionless manual execute succeeded — a non-executor-service account executed a verified message")
	})
}
