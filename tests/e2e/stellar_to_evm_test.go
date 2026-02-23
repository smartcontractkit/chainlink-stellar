package e2e_tests

import (
	"encoding/hex"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	onrampoperations "github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v1_7_0/operations/onramp"
	ccv "github.com/smartcontractkit/chainlink-ccv/devenv"
	ccvcommon "github.com/smartcontractkit/chainlink-ccv/devenv/common"
	"github.com/smartcontractkit/chainlink-ccv/devenv/tests/e2e"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-common/pkg/utils/tests"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/onramp"
	ccvsourcereader "github.com/smartcontractkit/chainlink-stellar/ccv/source_reader"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
	"github.com/smartcontractkit/chainlink-testing-framework/framework"
)

const (
	// Test timeouts for Stellar to EVM flow
	stellarSentTimeout = 30 * time.Second
)

// Start the environment required for this test using:
// CTF_CONFIGS=env-stellar-evm.toml go run ./cmd/ccv
// from the build/devenv directory.
//
// Contracts must be compiled before running:
// make build
// from the chainlink-stellar root directory.
func TestStellarToEVMSourceReader(t *testing.T) {
	ccvDevenvDir, err := filepath.Abs("../../../chainlink-ccv/build/devenv")
	require.NoError(t, err)

	// We change the working dir to allow chainlink-ccv command to find the fake
	// services with relative paths
	origDir, err := os.Getwd()
	require.NoError(t, err)
	err = os.Chdir(ccvDevenvDir)
	require.NoError(t, err)

	t.Cleanup(func() {
		os.Chdir(origDir)
		framework.RemoveTestContainers()
	})

	// CTF_CONFIGS must be a relative path because ccv.Load joins it with "."
	// via filepath.Join, which strips leading "/" from absolute paths.
	// This path is relative to ccvDevenvDir (the CWD after Chdir).
	configRelPath, err := filepath.Rel(ccvDevenvDir, filepath.Join(origDir, "../env/env-stellar-evm.toml"))
	require.NoError(t, err)

	configOutputPath, err := filepath.Rel(ccvDevenvDir, filepath.Join(origDir, "../env/env-stellar-evm-out.toml"))
	require.NoError(t, err)

	os.Setenv("CTF_CONFIGS", configRelPath)
	os.Setenv("CTF_CONFIG_OUTPUT", configOutputPath)
	os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "false")

	stellarChainID := "baefd734b8d3e48472cff83912375fedbc7573701912fe308af730180f97d74a"

	ctx := ccv.Plog.WithContext(t.Context())
	l := zerolog.Ctx(ctx)

	env := helpers.NewE2ETestEnv(t, ctx, l, configOutputPath, stellarChainID)
	deployer := env.Deployer
	deployerKP := env.DeployerKP
	rpc := env.RPCClient
	stellarDetails := env.SourceChainDetails
	evmDetails := env.DestChainDetails
	destChain := env.DestChain

	// Look up the OnRamp contract address from the CCV datastore.
	// It was deployed and configured during NewE2ETestEnv → ccv.NewEnvironment()
	// via chain.go DeployContractsForSelector.
	onrampKey := datastore.NewAddressRefKey(
		stellarDetails.ChainSelector,
		datastore.ContractType(onrampoperations.ContractType),
		onrampoperations.Version,
		"",
	)
	onrampRef, err := env.DataStore.Addresses().Get(onrampKey)
	require.NoError(t, err)
	onrampContractID := onrampRef.Address
	require.NotEmpty(t, onrampContractID)
	l.Info().Str("onrampContractID", onrampContractID).Msg("Found OnRamp in CCV datastore")

	onRampClient := onrampbindings.NewOnRampClient(deployer, onrampContractID)

	// Create the Stellar source reader with the DEPLOYED OnRamp contract ID
	stellarSourceReader, err := ccvsourcereader.NewSourceReaderWithClient(
		rpc,
		onrampContractID,
		"onramp_1_7_CCIPMessageSent", // Event topic from OnRamp contract
		l,
	)
	require.NoError(t, err)
	l.Info().Str("onrampContractID", onrampContractID).Msg("Created Stellar source reader")

	t.Run("basic_stellar_to_evm_message", func(t *testing.T) {
		// Get receiver address on EVM
		evmReceiver, err := destChain.GetEOAReceiverAddress()
		require.NoError(t, err)
		l.Info().Str("evmReceiver", hex.EncodeToString(evmReceiver)).Msg("Using EVM receiver address")

		// Record the latest ledger before sending so we know where to scan for events
		latestLedger, err := rpc.GetLatestLedger(ctx)
		require.NoError(t, err)
		startLedger := latestLedger.Sequence
		l.Info().Uint32("startLedger", startLedger).Msg("Recording start ledger before sending")

		// Build the CCIP message
		mockFeeToken := helpers.GenerateMockContractID(t, deployerKP.Address(), "fee-token")
		msg := onrampbindings.StellarToAnyMessage{
			Receiver:     evmReceiver,                    // 20-byte EVM address
			Data:         []byte("hello from stellar"),   // arbitrary payload
			TokenAmounts: []onrampbindings.TokenAmount{}, // no token transfer
			FeeToken:     mockFeeToken,                   // placeholder fee token
			ExtraArgs:    []byte{},                       // no extra args
		}

		// Send the message via the OnRamp's forward_from_router.
		// The deployer is configured as the "router" in the dest chain config,
		// so its transaction signature satisfies the router auth check.
		l.Info().Str("original sender address", deployerKP.Address()).Msg("Sending CCIP message via OnRamp forward_from_router...")
		messageID, err := onRampClient.ForwardFromRouter(
			ctx,
			evmDetails.ChainSelector,
			msg,
			0,                    // fee token amount (fee calculation not yet implemented)
			deployerKP.Address(), // original sender
		)
		require.NoError(t, err)
		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Msg("CCIP message sent successfully via OnRamp")

		// Capture the CCIPMessageSent event via the SourceReader
		l.Info().Msg("Fetching CCIPMessageSent events via SourceReader...")
		var events []protocol.MessageSentEvent
		require.Eventually(t, func() bool {
			events, err = stellarSourceReader.FetchMessageSentEvents(
				ctx,
				big.NewInt(int64(startLedger)),
				nil, // toBlock: nil means latest ledger
			)
			if err != nil {
				l.Debug().Err(err).Msg("Failed to fetch events, retrying...")
				return false
			}
			return len(events) > 0
		}, stellarSentTimeout, 2*time.Second, "expected to find CCIPMessageSent event via SourceReader")

		require.Len(t, events, 1, "expected exactly one CCIPMessageSent event")
		capturedEvent := events[0]

		l.Info().
			Str("capturedMessageID", hex.EncodeToString(capturedEvent.MessageID[:])).
			Uint64("sequenceNumber", uint64(capturedEvent.Message.SequenceNumber)).
			Uint64("blockNumber", capturedEvent.BlockNumber).
			Msg("Message captured via SourceReader")

		// Verify the captured event matches what we sent
		require.Equal(t, protocol.Bytes32(messageID), capturedEvent.MessageID,
			"message ID from OnRamp should match the one captured by SourceReader")

		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Msg("Successfully sent and captured CCIP message from Stellar")

		// =====================================================================
		// Verifier and Execution Assertions
		// =====================================================================

		// NOTE: These assertions require the verifier to be configured with the
		// actual deployed OnRamp contract ID. Currently, the environment setup
		// generates deterministic placeholder addresses in DeployContractsForSelector
		// (chain.go) and the verifier config uses those placeholders.
		//
		// TODO: Update the environment setup to:
		// 1. Deploy real Stellar contracts (router, onramp, etc.)
		// 2. Pass the deployed OnRamp contract ID to the verifier config
		// Once that is done, these assertions should pass end-to-end.

		// Wait for verification through the aggregator
		testCtx := e2e.NewTestingContext(t, t.Context(), env.Chains, env.AggregatorClients[ccvcommon.DefaultCommitteeVerifierQualifier], env.IndexerMonitor)
		result, err := testCtx.AssertMessage(protocol.Bytes32(messageID), e2e.AssertMessageOptions{
			TickInterval:            1 * time.Second,
			ExpectedVerifierResults: 1, // just committee verifier
			Timeout:                 tests.WaitTimeout(t),
			AssertVerifierLogs:      false,
			AssertExecutorLogs:      false,
		})
		require.NoError(t, err)
		require.NotNil(t, result.AggregatedResult)
		require.Len(t, result.IndexedVerifications.Results, 1)

		// // Wait for execution on EVM
		// ev, err := destChain.WaitOneExecEventBySeqNo(t.Context(), stellarDetails.ChainSelector, 1, tests.WaitTimeout(t))
		// require.NoError(t, err)
		// require.Equalf(
		// 	t,
		// 	cciptestinterfaces.ExecutionStateSuccess,
		// 	ev.State,
		// 	"message should have been successfully executed, return data: %x",
		// 	ev.ReturnData,
		// )

		// l.Info().
		// 	Str("messageID", hex.EncodeToString(messageID[:])).
		// 	Msg("Message executed successfully on EVM")
	})
}
