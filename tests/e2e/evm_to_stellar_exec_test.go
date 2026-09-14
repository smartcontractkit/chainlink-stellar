package e2e_tests

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	chain_selectors "github.com/smartcontractkit/chain-selectors"
	chainsel "github.com/smartcontractkit/chain-selectors"
	offrampoperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/offramp"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/chainreg"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-common/pkg/utils/tests"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	ccvchain "github.com/smartcontractkit/chainlink-stellar/tests/ccv/chain"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// messageV3Version is the CCIP message version we send across the EVM→Stellar
// lane. Per chainlink-ccv changelog/2026-04-27_extra_args_data_provider.md the
// version is now an explicit argument on Chain.SendMessage rather than a field
// inside MessageOptions.
const messageV3Version uint8 = 3

const (
	evmSentTimeout = 30 * time.Second
	execTimeout    = 7 * time.Minute
)

// TestEVMToStellarExecutionHappyPath validates the full EVM-to-Stellar CCIP message flow:
// EVM OnRamp → Verifiers → Indexer → Stellar Executor → Stellar OffRamp.
//
// Contracts must be compiled before running:
//
//	make build
//
// Start the devenv from the chainlink-stellar root:
//
//	CTF_CONFIGS=tests/env/env-stellar-evm.toml go run ./tests/testutils/cmd/devenv
//
// Once the devenv is running, run the test:
//
//	go test -v -timeout 10m ./tests/e2e/... -run TestEVMToStellarExecutionHappyPath
func TestEVMToStellarExecutionHappyPath(t *testing.T) {
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

	t.Run("evm_to_stellar_execution", func(t *testing.T) {
		// Get the Stellar receiver address (deterministic 32-byte ed25519 key).
		stellarReceiver, err := stellarChain.GetEOAReceiverAddress()
		require.NoError(t, err)
		l.Info().Str("stellarReceiver", hex.EncodeToString(stellarReceiver)).Msg("Using Stellar receiver address")

		// Record the expected sequence number before sending.
		seqNo, err := evmChain.GetExpectedNextSequenceNumber(ctx, stellarDetails.ChainSelector)
		require.NoError(t, err)
		l.Info().Uint64("seqNo", seqNo).Msg("Expected next sequence number from EVM OnRamp")

		// Send the CCIP message from EVM to Stellar.
		// For Stellar destinations, rely on the lane defaults configured on the
		// EVM OnRamp. GenericExtraArgsV3 on EVM only supports explicit 20-byte
		// address overrides, while Stellar executor/CCV addresses are 32 bytes.
		sendResult, err := evmChain.SendMessage(ctx, stellarDetails.ChainSelector,
			cciptestinterfaces.MessageFields{
				Receiver: stellarReceiver,
				Data:     []byte("hello from evm"),
			},
			cciptestinterfaces.MessageOptions{
				ExecutionGasLimit: 200_000,
			},
			messageV3Version,
		)
		require.NoError(t, err)
		l.Info().
			Str("messageID", hex.EncodeToString(sendResult.MessageID[:])).
			Int("receiptIssuers", len(sendResult.ReceiptIssuers)).
			Msg("CCIP message sent from EVM")

		// Wait for the sent event on the EVM chain.
		sentEvent, err := evmChain.ConfirmSendOnSource(ctx, stellarDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, evmSentTimeout)
		require.NoError(t, err)
		messageID := sentEvent.MessageID
		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Msg("Sent event confirmed on EVM")

		// Wait for verification and indexing.
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
		require.NotNil(t, result.AggregatedResult)
		require.Len(t, result.IndexedVerifications.Results, 1)
		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Msg("Message verified and aggregated successfully")

		// Get the source chain config from the Stellar OffRamp contract.
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
		l.Info().Str("offrampContractID", offrampContractID).Msg("Found OffRamp in CCV datastore")

		offrampClient := offrampbindings.NewOffRampClient(env.Deployer, offrampContractID)
		sourceChainConfig, err := offrampClient.GetSourceChainConfig(ctx, evmDetails.ChainSelector)
		require.NoError(t, err)

		l.Info().Interface("sourceChainConfig", sourceChainConfig).Msg("Source chain config in OffRamp contract")

		// Wait for execution on the Stellar destination chain.
		execEvent, err := stellarChain.ConfirmExecOnDest(t.Context(), evmDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, execTimeout)
		require.NoError(t, err)
		require.Equalf(
			t,
			cciptestinterfaces.ExecutionStateSuccess,
			execEvent.Event.State,
			"message should have been successfully executed, return data: %x",
			execEvent.Event.ReturnData,
		)

		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Uint64("seqNo", seqNo).
			Msg("Message executed successfully on Stellar")
	})
}

// TestEVMToStellarExecutionCursedSource validates that cursing a source chain
// prevents message execution on Stellar OffRamp. It:
// 1. Curses the source EVM chain from the Stellar chain
// 2. Sends a message from EVM to Stellar
// 3. Expects the OffRamp execute to fail due to curse on source chain
//
// Contracts must be compiled before running:
//
//	make build
//
// Start the devenv from the chainlink-stellar root:
//
//	CTF_CONFIGS=tests/env/env-stellar-evm.toml go run ./tests/testutils/cmd/devenv
//
// Once the devenv is running, run the test:
//
//	go test -v -timeout 10m ./tests/e2e/... -run TestEVMToStellarExecutionCursedSource
func TestEVMToStellarExecutionCursedSource(t *testing.T) {
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

	t.Run("evm_to_stellar_execution_cursed_source", func(t *testing.T) {
		// Get the Stellar receiver address
		stellarReceiver, err := stellarChain.GetEOAReceiverAddress()
		require.NoError(t, err)
		l.Info().Str("stellarReceiver", hex.EncodeToString(stellarReceiver)).Msg("Using Stellar receiver address")

		// Curse the EVM source chain from the Stellar chain using fastcurse changeset
		l.Info().Uint64("chainSelector", evmDetails.ChainSelector).Msg("Cursing EVM source chain")
		helpers.CurseChain(t, env.CLDFEnv, stellarDetails.ChainSelector, evmDetails.ChainSelector)
		l.Info().Msg("✅ EVM source chain cursed successfully")

		t.Cleanup(func() {
			l.Info().Msg("🔓 Cleaning up: uncursing EVM source chain")
			helpers.UncurseChain(t, env.CLDFEnv, stellarDetails.ChainSelector, evmDetails.ChainSelector)
		})

		// Record the expected sequence number before sending
		seqNo, err := evmChain.GetExpectedNextSequenceNumber(ctx, stellarDetails.ChainSelector)
		require.NoError(t, err)
		l.Info().Uint64("seqNo", seqNo).Msg("Expected next sequence number from EVM OnRamp")

		// Send the CCIP message from EVM to Stellar (while source chain is cursed)
		l.Info().Msg("📨 Sending message from cursed EVM source chain to Stellar")
		sendResult, err := evmChain.SendMessage(ctx, stellarDetails.ChainSelector,
			cciptestinterfaces.MessageFields{
				Receiver: stellarReceiver,
				Data:     []byte("message from cursed source"),
			},
			cciptestinterfaces.MessageOptions{
				ExecutionGasLimit: 200_000,
			},
			messageV3Version,
		)
		require.NoError(t, err)
		l.Info().
			Str("messageID", hex.EncodeToString(sendResult.MessageID[:])).
			Msg("✅ CCIP message sent from EVM (message was created while source cursed)")

		// Wait for the sent event on the EVM chain
		sentEvent, err := evmChain.ConfirmSendOnSource(ctx, stellarDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, evmSentTimeout)
		require.NoError(t, err)
		messageID := sentEvent.MessageID
		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Msg("Sent event confirmed on EVM")

		// Wait for verification and indexing
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
		require.NotNil(t, result.AggregatedResult)
		require.Len(t, result.IndexedVerifications.Results, 1)
		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Msg("Message verified and aggregated successfully")

		// Try to execute on Stellar OffRamp - should fail due to curse on source chain
		l.Info().Msg("🔒 Attempting execution on Stellar OffRamp (should fail - source chain is cursed)")

		// The message will not emit an ExecutionStateChanged event because it fails at
		// the require_chain_not_cursed() check before execution. The transaction aborts
		// with CursedByRMN error. Wait a short time (30 seconds) to verify no event appears.
		shortTimeout := 60 * time.Second
		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Msg("🕐 Waiting 30 seconds to verify no execution event appears (should be blocked by curse check)")

		execEvent, err := stellarChain.ConfirmExecOnDest(t.Context(), evmDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, shortTimeout)
		require.Error(t, err, "should timeout waiting for execution event since message is cursed")
		require.Equal(t, cciptestinterfaces.ExecutionStateChangedEvent{}, execEvent, "execution event should be empty/zero value when cursed")

		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Msg("✅ Confirmed: No execution event after 30 seconds (execution blocked by curse check)")

		// Now uncurse the EVM source chain to allow execution
		l.Info().Msg("🔓 Uncursing EVM source chain to allow execution")
		helpers.UncurseChain(t, env.CLDFEnv, stellarDetails.ChainSelector, evmDetails.ChainSelector)
		l.Info().Msg("✅ EVM source chain uncursed successfully")

		// The first message (seqNo) was stuck in the executor's retry heap while
		// cursed. After uncursing, the executor's curse cache will refresh and
		// the stuck message should be retried and executed successfully.
		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Uint64("seqNo", seqNo).
			Msg("🔄 Waiting for stuck message to be retried and executed after uncurse")

		execEventRetried, err := stellarChain.ConfirmExecOnDest(t.Context(), evmDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, execTimeout)
		require.NoError(t, err, "stuck message should have been retried and executed after uncurse")
		require.Equalf(
			t,
			cciptestinterfaces.ExecutionStateSuccess,
			execEventRetried.Event.State,
			"stuck message should have been successfully executed after uncurse, return data: %x",
			execEventRetried.Event.ReturnData,
		)

		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Uint64("seqNo", seqNo).
			Msg("✅ Stuck message executed successfully after uncurse (retry worked)")

		// Send another message after uncursing to verify normal flow is restored
		l.Info().Msg("📨 Sending new message after uncurse")
		seqNo2, err := evmChain.GetExpectedNextSequenceNumber(ctx, stellarDetails.ChainSelector)
		require.NoError(t, err)

		sendResult2, err := evmChain.SendMessage(ctx, stellarDetails.ChainSelector,
			cciptestinterfaces.MessageFields{
				Receiver: stellarReceiver,
				Data:     []byte("hello from evm after uncurse"),
			},
			cciptestinterfaces.MessageOptions{
				ExecutionGasLimit: 200_000,
			},
			messageV3Version,
		)
		require.NoError(t, err)
		l.Info().
			Str("messageID", hex.EncodeToString(sendResult2.MessageID[:])).
			Msg("✅ CCIP message sent from EVM after uncurse")

		// Verify execution succeeds for the new message too
		execEvent2, err := stellarChain.ConfirmExecOnDest(t.Context(), evmDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo2}, execTimeout)
		require.NoError(t, err)
		require.Equalf(
			t,
			cciptestinterfaces.ExecutionStateSuccess,
			execEvent2.Event.State,
			"new message should have been successfully executed after uncurse, return data: %x",
			execEvent2.Event.ReturnData,
		)

		l.Info().
			Str("messageID", hex.EncodeToString(sendResult2.MessageID[:])).
			Uint64("seqNo", seqNo2).
			Msg("✅ New message executed successfully on Stellar after uncurse")
	})
}

// TestEVMToStellarExecutionInvalidReceiver validates that sending a CCIP message
// from EVM to Stellar with a receiver address that does not correspond to a
// deployed Wasm contract results in an ExecutionStateFailure on the Stellar
// OffRamp. The Stellar OffRamp checks `receiver.executable()` and returns
// CCIPError::ReceiverDoesNotExist (error code 114) when the address has no
// ledger entry.
//
// Contracts must be compiled before running:
//
//	make build
//
// Start the devenv from the chainlink-stellar root:
//
//	CTF_CONFIGS=tests/env/env-stellar-evm.toml go run ./tests/testutils/cmd/devenv
//
// Once the devenv is running, run the test:
//
//	go test -v -timeout 10m ./tests/e2e/... -run TestEVMToStellarExecutionInvalidReceiver
func TestEVMToStellarExecutionInvalidReceiver(t *testing.T) {
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

	t.Run("evm_to_stellar_invalid_receiver", func(t *testing.T) {
		// Build a 32-byte receiver that is syntactically valid but does not
		// correspond to any deployed contract on the Stellar localnet.
		var fakeReceiver [32]byte
		copy(fakeReceiver[:], []byte("INVALID_RECEIVER_NO_CONTRACT____"))
		l.Info().Str("fakeReceiver", hex.EncodeToString(fakeReceiver[:])).Msg("Using fake Stellar receiver address")

		seqNo, err := evmChain.GetExpectedNextSequenceNumber(ctx, stellarDetails.ChainSelector)
		require.NoError(t, err)
		l.Info().Uint64("seqNo", seqNo).Msg("Expected next sequence number from EVM OnRamp")

		sendResult, err := evmChain.SendMessage(ctx, stellarDetails.ChainSelector,
			cciptestinterfaces.MessageFields{
				Receiver: fakeReceiver[:],
				Data:     []byte("hello to nowhere"),
			},
			cciptestinterfaces.MessageOptions{
				ExecutionGasLimit: 200_000,
			},
			messageV3Version,
		)
		require.NoError(t, err, "EVM send should succeed — receiver validation happens on the destination")
		l.Info().
			Str("messageID", hex.EncodeToString(sendResult.MessageID[:])).
			Msg("CCIP message sent from EVM with invalid Stellar receiver")

		sentEvent, err := evmChain.ConfirmSendOnSource(ctx, stellarDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, evmSentTimeout)
		require.NoError(t, err)
		messageID := sentEvent.MessageID
		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Msg("Sent event confirmed on EVM")

		// Wait for verification and indexing — the DON processes the message
		// regardless of receiver validity.
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
		require.NotNil(t, result.AggregatedResult)
		require.Len(t, result.IndexedVerifications.Results, 1)
		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Msg("Message verified and aggregated successfully (receiver validity not checked here)")

		// Wait for execution on the Stellar OffRamp — expect Failure.
		execEvent, err := stellarChain.ConfirmExecOnDest(t.Context(), evmDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, execTimeout)
		require.NoError(t, err, "should receive an execution event even for failed execution")
		require.Equalf(
			t,
			cciptestinterfaces.ExecutionStateFailure,
			execEvent.Event.State,
			"execution should fail because the receiver does not exist on Stellar, return data: %x",
			execEvent.Event.ReturnData,
		)

		// Verify the return data contains the ReceiverDoesNotExist error code (114).
		const ccipErrorReceiverDoesNotExist uint32 = 114
		require.GreaterOrEqual(t, len(execEvent.Event.ReturnData), 4, "return data should contain at least 4 bytes for error code")
		errorCode := binary.BigEndian.Uint32(execEvent.Event.ReturnData[:4])
		require.Equal(t, ccipErrorReceiverDoesNotExist, errorCode,
			"return data should encode CCIPError::ReceiverDoesNotExist (114)")

		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Uint64("seqNo", seqNo).
			Uint32("errorCode", errorCode).
			Msg("Execution failed as expected with ReceiverDoesNotExist error")
	})
}

// TestEVMToStellarComposableMessaging exercises the same EVM→Stellar happy
// path as TestEVMToStellarExecutionHappyPath but routed through the
// chainlink-ccv generic V3 messaging scenario, which exercises the
// ChainAsSource / ChainAsDestination interfaces directly.
//
// chainlink-ccv changelog/2026-04-27_extra_args_data_provider.md replaced
// messaging.BasicMessageTestScenario (an importable helper) with
// MessageV3TestScenario inside an *_test.go file (so it cannot be imported
// from outside the messaging package). The V3 helper additionally requires
// the destination to implement cciptestinterfaces.MessageV3Destination
// (GetExecutorArgs / GetTokenArgs). The Stellar chain implementation does
// not implement that interface yet, so we inline the equivalent flow here
// using the EVM-source dispatcher (evm.SerializeEVMExtraArgs) and skip the
// V3 destination type assertion. Once the Stellar Chain grows
// MessageV3Destination support, this test can switch to the upstream
// MessageV3TestScenario by copying the helper into this package.
//
// Prerequisites:
//
//	make build
//	CTF_CONFIGS=tests/env/env-stellar-evm.toml go run ./tests/testutils/cmd/devenv
//
// Then:
//
//	go test -v -timeout 15m ./tests/e2e/... -run TestEVMToStellarComposableMessaging
func TestEVMToStellarComposableMessaging(t *testing.T) {
	ccvchain.RegisterStellarComponents()

	configOutputPath := "../env/env-stellar-evm-out.toml"
	stellarChainID := chain_selectors.STELLAR_LOCALNET.ChainID
	stellarSelector := chain_selectors.STELLAR_LOCALNET.Selector

	ctx := ccv.Plog.WithContext(t.Context())

	env := helpers.NewE2ETestEnv(t, ctx, zerolog.Ctx(ctx), configOutputPath, stellarChainID, stellarSelector)

	stellarDetails := env.SourceChainDetails
	evmDetails := env.DestChainDetails

	stellarImpl := env.Chains[stellarDetails.ChainSelector]
	require.NotNil(t, stellarImpl, "Stellar chain not found in chains map")
	evmImpl := env.Chains[evmDetails.ChainSelector]
	require.NotNil(t, evmImpl, "EVM chain not found in chains map")

	src, ok := evmImpl.(cciptestinterfaces.ChainAsSource)
	require.True(t, ok, "EVM chain must implement cciptestinterfaces.ChainAsSource")
	dest, ok := stellarImpl.(cciptestinterfaces.ChainAsDestination)
	require.True(t, ok, "Stellar chain must implement cciptestinterfaces.ChainAsDestination")

	receiver, err := dest.GetEOAReceiverAddress()
	require.NoError(t, err)

	require.NoError(t, runEVMToStellarV3Scenario(
		ctx,
		src,
		dest,
		cciptestinterfaces.MessageFields{
			Receiver: receiver,
			Data:     []byte("composable evm→stellar"),
		},
		cciptestinterfaces.MessageOptions{ExecutionGasLimit: 200_000},
	))
}

// runEVMToStellarV3Scenario mirrors messaging.MessageV3TestScenario but uses
// the per-(family, version) extra-args serializer registry instead of the
// MessageV3Source / MessageV3Destination interfaces. The Stellar Chain does
// not yet implement MessageV3Destination (GetExecutorArgs / GetTokenArgs), so
// MessageV3Source.BuildV3ExtraArgs would dereference a nil destination and
// panic. We use the CLI / load-gun pattern documented in
// chainlink-ccv changelog/2026-04-27_extra_args_data_provider.md, looking up
// the registered FamilyStellar V3 serializer (registered by ccv/chain in
// register.go) and feeding the result straight into BuildChainMessage.
//
// Once the Stellar Chain implements MessageV3Destination this can be replaced
// with a verbatim copy of messaging.MessageV3TestScenario from
// chainlink-ccv/build/devenv/tests/composable/messaging.
func runEVMToStellarV3Scenario(
	ctx context.Context,
	src cciptestinterfaces.ChainAsSource,
	dest cciptestinterfaces.ChainAsDestination,
	fields cciptestinterfaces.MessageFields,
	opts cciptestinterfaces.MessageOptions,
) error {
	serializer, ok := chainreg.GetRegistry().GetExtraArgsSerializer(chain_selectors.FamilyStellar, messageV3Version)
	if !ok {
		return fmt.Errorf("no extra args serializer registered for (Stellar, V3) — did ccv/chain.RegisterStellarComponents run?")
	}

	extraArgs, err := serializer(opts)
	if err != nil {
		return fmt.Errorf("serialize V3 extra args for Stellar destination: %w", err)
	}

	srcMessage, err := src.BuildChainMessage(ctx, fields, extraArgs)
	if err != nil {
		return fmt.Errorf("build chain message: %w", err)
	}

	sentEvent, _, err := src.SendChainMessage(ctx, dest.ChainSelector(), srcMessage, nil)
	if err != nil {
		return fmt.Errorf("send chain message: %w", err)
	}

	if _, err := src.ConfirmSendOnSource(ctx, dest.ChainSelector(), cciptestinterfaces.MessageEventKey{MessageID: sentEvent.MessageID}, evmSentTimeout); err != nil {
		return fmt.Errorf("confirm send on source: %w", err)
	}

	execEvent, err := dest.ConfirmExecOnDest(ctx, src.ChainSelector(), cciptestinterfaces.MessageEventKey{MessageID: sentEvent.MessageID}, execTimeout)
	if err != nil {
		return fmt.Errorf("confirm exec on dest: %w", err)
	}
	if execEvent.Event.State != cciptestinterfaces.ExecutionStateSuccess {
		return fmt.Errorf("unexpected execution state %s, return data: %x", execEvent.Event.State, execEvent.Event.ReturnData)
	}
	return nil
}
