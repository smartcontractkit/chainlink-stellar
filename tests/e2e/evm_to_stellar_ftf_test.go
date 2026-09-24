package e2e_tests

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-common/pkg/utils/tests"
	recvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ccip_receiver"
	ccvchain "github.com/smartcontractkit/chainlink-stellar/tests/ccv/chain"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

const (
	// ccipErrorInvalidRequestedFinality mirrors contracts/common/error
	// CCIPError::InvalidRequestedFinality — returned by the Stellar OffRamp's
	// receiver finality gate (finality_codec::ensure_requested_finality_allowed)
	// when a non-default requested finality is not admitted by the receiver's
	// allowed_finality_config.
	ccipErrorInvalidRequestedFinality uint32 = 315
	// ftfQuickThreshold is a sanity bound for send→execute latency on the
	// localnet. Source finality is instant there, so this bounds the DON
	// commit+execute cycle rather than a finality-wait difference; FTF's latency
	// advantage vs full-finality is only realized on chains with slow finality.
	// Sits under execTimeout so it fails only on a pathologically slow cycle.
	ftfQuickThreshold = 6 * time.Minute
)

// FTF (Fast Transfer Finality) end-to-end test for the EVM→Stellar lane.
//
// A sender requests a *non-default* finality (here FinalityWaitForSafe,
// 0x00010000 — "treat the source block as final once it reaches the Ethereum
// `safe` head" instead of waiting for full finality) by setting
// MessageOptions.FinalityConfig. That value flows through
// CCIP17EVM.SendMessage → SerializeMessageV3ExtraArgs → NewV3ExtraArgs into the
// on-wire V3 `finality` field (protocol.Message.Finality).
//
// Two gates touch that field on the way to execution:
//
//  1. EVM OnRamp.getFee validates only the *wire shape* of the requested
//     finality (FinalityCodec._validateRequestedFinality) at send time — it does
//     NOT consult the receiver's policy. The EVM source committee-verifier
//     (BaseVerifier.getFee) does carry an allowed-finality config, and the
//     devenv configures it with {WaitForSafe:true, BlockDepth:1} so a
//     FinalityWaitForSafe request is admitted at fee-quote time and the send
//     succeeds.
//  2. The Stellar OffRamp enforces the *receiver's* allowed-finality policy at
//     execute time (finality_codec::ensure_requested_finality_allowed, the H-7
//     receiver finality gate) for non-token-only messages. The example
//     ccip_receiver is deployed with AllowedFinalityConfig=0 per source chain
//     (WAIT_FOR_FINALITY only), so a FinalityWaitForSafe request is REJECTED
//     with CCIPError::InvalidRequestedFinality (315) until the receiver is
//     reconfigured to admit it.
//
// This test exercises both halves:
//
//   - Negative control: send FTF under the default receiver policy and assert
//     the OffRamp execute fails with ExecutionStateFailure + error code 315.
//     (That 315 failure is also the proof a *non-zero* finality reached the
//     wire: finality=0 is always admitted, so a 315 can only come from a
//     non-default request that the receiver rejected.)
//   - Happy path: reconfigure the receiver (owner = deployer) to admit
//     FinalityWaitForSafe for the EVM source chain via enable_remote_chain,
//     verify the policy took effect with get_ccvs_and_finality_config, re-send
//     the same FTF message, and assert ExecutionStateSuccess plus a bounded
//     send→execute latency. A successful send already means the EVM fee path
//     — which routes requestedFinalityConfig into CCV/executor/pool get_fee —
//     computed and charged a payable fee for the FTF message; the success +
//     315-contrast together demonstrate "Stellar respects the FTF request and
//     executes it quickly while charging proper fees."
//
// Prerequisites:
//
//	make build
//	CTF_CONFIGS=tests/env/env-stellar-evm.toml go run ./tests/testutils/cmd/devenv
//
// Then:
//
//	go test -v -timeout 20m ./tests/e2e/... -run TestEVMToStellarFTFExecution
func TestEVMToStellarFTFExecution(t *testing.T) {
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

	// The example CCIP receiver (a Wasm contract) is the message destination.
	// GetReceiverContractAddress lives on the concrete *ccvchain.Chain, not the
	// CCIP17 interface, so type-assert.
	stellarImpl, ok := stellarChain.(*ccvchain.Chain)
	require.True(t, ok, "stellar chain must be *ccvchain.Chain to access the receiver contract id")
	receiverID, err := stellarImpl.GetReceiverContractAddress()
	require.NoError(t, err)
	l.Info().Str("receiverContractID", receiverID).Msg("Using example CCIP receiver")

	recvClient := recvbindings.NewExampleCcipReceiverClient(env.Deployer, receiverID)
	ownerAddr := env.Deployer.SignerAddress()
	// placeholderExtraArgs mirrors what the deploy sequence seeds via
	// enable_remote_chain ([0x01]); enable_remote_chain rejects empty extra_args.
	placeholderExtra := []byte{0x01}
	// evmSourceSelector is the key the receiver's get_ccvs_and_finality_config
	// reads for inbound-from-EVM policy.
	evmSourceSelector := evmDetails.ChainSelector

	// Always restore the receiver to its deployed default (WAIT_FOR_FINALITY
	// only) so this test does not leak FTF admission into other tests sharing
	// the devenv.
	t.Cleanup(func() {
		l.Info().Msg("🧹 resetting receiver allowed_finality_config to 0 (WAIT_FOR_FINALITY only)")
		if err := recvClient.EnableRemoteChain(context.Background(), ownerAddr, evmSourceSelector, placeholderExtra, 0); err != nil {
			l.Warn().Err(err).Msg("failed to reset receiver allowed_finality_config in cleanup")
		}
	})

	// FTF request: FinalityWaitForSafe (0x00010000). Non-zero ⇒ is_fast_finality.
	ftfOpts := cciptestinterfaces.MessageOptions{
		ExecutionGasLimit: 200_000,
		FinalityConfig:    protocol.FinalityWaitForSafe,
	}

	// === Negative control: default receiver policy rejects FTF (315) ===
	t.Run("ftf_rejected_under_default_receiver_policy", func(t *testing.T) {
		stellarReceiver, err := stellarChain.GetEOAReceiverAddress()
		require.NoError(t, err)
		l.Info().
			Str("stellarReceiver", hex.EncodeToString(stellarReceiver)).
			Msg("Sending FTF message under default (WAIT_FOR_FINALITY-only) receiver policy")

		seqNo, err := evmChain.GetExpectedNextSequenceNumber(ctx, stellarDetails.ChainSelector)
		require.NoError(t, err)

		// Send should succeed: the EVM OnRamp validates only the finality *shape*
		// at getFee, and the EVM source CCV admits WaitForSafe.
		sendResult, err := evmChain.SendMessage(ctx, stellarDetails.ChainSelector,
			cciptestinterfaces.MessageFields{
				Receiver: stellarReceiver,
				Data:     []byte("ftf negative control"),
			},
			ftfOpts,
			messageV3Version,
		)
		require.NoError(t, err, "EVM send must succeed — receiver finality policy is enforced on delivery, not at send")
		l.Info().Str("messageID", hex.EncodeToString(sendResult.MessageID[:])).Msg("FTF message sent from EVM (default receiver policy)")

		sentEvent, err := evmChain.ConfirmSendOnSource(ctx, stellarDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, evmSentTimeout)
		require.NoError(t, err)
		messageID := sentEvent.MessageID
		l.Info().Str("messageID", hex.EncodeToString(messageID[:])).Msg("Sent event confirmed on EVM")

		// The DON verifies/commits the message regardless of receiver policy.
		defaultAggregatorClient := env.AggregatorClients[devenvcommon.DefaultCommitteeVerifierQualifier]
		require.NotNil(t, defaultAggregatorClient)
		testCtx := e2e.NewTestingContext(t, t.Context(), env.Chains, defaultAggregatorClient, env.IndexerMonitor)
		_, err = testCtx.AssertMessage(protocol.Bytes32(messageID), e2e.AssertMessageOptions{
			TickInterval:            1 * time.Second,
			ExpectedVerifierResults: 1,
			Timeout:                 tests.WaitTimeout(t),
			AssertVerifierLogs:      false,
			AssertExecutorLogs:      false,
		})
		require.NoError(t, err)
		l.Info().Str("messageID", hex.EncodeToString(messageID[:])).Msg("FTF message verified and aggregated (receiver policy not checked here)")

		// OffRamp execute must fail: ensure_requested_finality_allowed(WaitForSafe, 0)
		// ⇒ CCIPError::InvalidRequestedFinality (315). A Failure event IS emitted
		// (the InProgress state is set before execute_single_message returns the
		// error), mirroring the InvalidReceiver test pattern.
		execEvent, err := stellarChain.ConfirmExecOnDest(t.Context(), evmDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, execTimeout)
		require.NoError(t, err, "should receive an execution event even for the failed FTF execute")
		require.Equalf(t, cciptestinterfaces.ExecutionStateFailure, execEvent.Event.State,
			"FTF execute should fail under default receiver policy, return data: %x", execEvent.Event.ReturnData)

		require.GreaterOrEqual(t, len(execEvent.Event.ReturnData), 4, "return data should carry a 4-byte CCIP error code")
		errorCode := binary.BigEndian.Uint32(execEvent.Event.ReturnData[:4])
		require.Equal(t, ccipErrorInvalidRequestedFinality, errorCode,
			"return data should encode CCIPError::InvalidRequestedFinality (315) — proves a non-zero finality reached the OffRamp gate")
		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Uint64("seqNo", seqNo).
			Uint32("errorCode", errorCode).
			Msg("✅ FTF rejected as expected under default receiver policy (315 ⇒ FTF was on-wire and reached the gate)")
	})

	// === Enable FTF admission on the receiver for the EVM source chain ===
	l.Info().Msg("⚙️  enabling FinalityWaitForSafe admission on the receiver for the EVM source chain")
	require.NoError(t, recvClient.EnableRemoteChain(ctx, ownerAddr, evmSourceSelector, placeholderExtra, uint32(protocol.FinalityWaitForSafe)))

	// Verify the policy took effect with a cheap on-chain read.
	cfg, err := recvClient.GetCcvsAndFinalityConfig(ctx, evmSourceSelector, []byte{})
	require.NoError(t, err)
	require.Equal(t, uint32(protocol.FinalityWaitForSafe), cfg.AllowedFinalityConfig,
		"receiver must now admit FinalityWaitForSafe for the EVM source chain")
	l.Info().
		Uint32("allowedFinalityConfig", cfg.AllowedFinalityConfig).
		Msg("✅ receiver now admits FinalityWaitForSafe (get_ccvs_and_finality_config confirmed)")

	// === Happy path: FTF admitted and executed quickly, fees charged ===
	t.Run("ftf_admitted_and_executed_after_enabling_policy", func(t *testing.T) {
		stellarReceiver, err := stellarChain.GetEOAReceiverAddress()
		require.NoError(t, err)

		seqNo, err := evmChain.GetExpectedNextSequenceNumber(ctx, stellarDetails.ChainSelector)
		require.NoError(t, err)
		l.Info().Uint64("seqNo", seqNo).Msg("Sending FTF message under FTF-admitting receiver policy")

		sendStart := time.Now()
		sendResult, err := evmChain.SendMessage(ctx, stellarDetails.ChainSelector,
			cciptestinterfaces.MessageFields{
				Receiver: stellarReceiver,
				Data:     []byte("ftf happy path"),
			},
			ftfOpts,
			messageV3Version,
		)
		require.NoError(t, err, "EVM send must succeed (fee path incl. CCV.get_fee with requested finality ran clean and the fee was paid)")
		l.Info().Str("messageID", hex.EncodeToString(sendResult.MessageID[:])).Msg("FTF message sent from EVM (FTF-admitting policy)")

		sentEvent, err := evmChain.ConfirmSendOnSource(ctx, stellarDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, evmSentTimeout)
		require.NoError(t, err)
		messageID := sentEvent.MessageID
		l.Info().Str("messageID", hex.EncodeToString(messageID[:])).Msg("Sent event confirmed on EVM")

		// Best-effort on-wire FTF check. The negative-control 315 failure already
		// proves a non-zero finality reached the gate; this asserts the explicit
		// field when the EVM event parser populates Message.Finality, and falls
		// back to the 315 proof otherwise (no nil-panic).
		if sentEvent.Message != nil {
			require.Equal(t, protocol.FinalityWaitForSafe, sentEvent.Message.Finality,
				"on-wire message must carry the requested FTF (FinalityWaitForSafe)")
			l.Info().Uint32("finality", uint32(sentEvent.Message.Finality)).Msg("on-wire finality confirmed == FinalityWaitForSafe")
		} else {
			l.Warn().Msg("sent event Message is nil; on-wire FTF proven by the negative-control 315 failure")
		}

		defaultAggregatorClient := env.AggregatorClients[devenvcommon.DefaultCommitteeVerifierQualifier]
		require.NotNil(t, defaultAggregatorClient)
		testCtx := e2e.NewTestingContext(t, t.Context(), env.Chains, defaultAggregatorClient, env.IndexerMonitor)
		_, err = testCtx.AssertMessage(protocol.Bytes32(messageID), e2e.AssertMessageOptions{
			TickInterval:            1 * time.Second,
			ExpectedVerifierResults: 1,
			Timeout:                 tests.WaitTimeout(t),
			AssertVerifierLogs:      false,
			AssertExecutorLogs:      false,
		})
		require.NoError(t, err)
		l.Info().Str("messageID", hex.EncodeToString(messageID[:])).Msg("FTF message verified and aggregated")

		execEvent, err := stellarChain.ConfirmExecOnDest(t.Context(), evmDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, execTimeout)
		require.NoError(t, err)
		require.Equalf(t, cciptestinterfaces.ExecutionStateSuccess, execEvent.Event.State,
			"FTF message should execute successfully now that the receiver admits it, return data: %x", execEvent.Event.ReturnData)

		latency := time.Since(sendStart)
		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Uint64("seqNo", seqNo).
			Dur("sendToExecLatency", latency).
			Msg("✅ FTF message executed successfully on Stellar")

		// "Quickly" sanity bound. On the localnet source finality is instant, so
		// this bounds the DON commit+execute cycle rather than a finality-wait
		// difference; FTF's latency advantage vs full-finality is only realized
		// on chains with slow finality. The bound sits under execTimeout so it
		// fails only on a pathologically slow cycle, not normal variance.
		require.Less(t, latency, ftfQuickThreshold,
			"FTF execution should complete well within the timeout (got %s)", latency)
	})
}
