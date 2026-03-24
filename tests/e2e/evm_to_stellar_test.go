package e2e_tests

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v1_7_0/versioned_verifier_resolver"
	"github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v2_0_0/operations/offramp"
	"github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v2_0_0/operations/proxy"
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

const (
	evmSentTimeout = 30 * time.Second
	execTimeout    = 5 * time.Minute
)

// TestEVMToStellarExecution validates the full EVM-to-Stellar CCIP message flow:
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
//	go test -v -timeout 10m ./tests/e2e/... -run TestEVMToStellarExecution
func TestEVMToStellarExecution(t *testing.T) {
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

	// Look up executor proxy address on the EVM source chain.
	executorKey := datastore.NewAddressRefKey(
		evmDetails.ChainSelector,
		datastore.ContractType(proxy.ContractType),
		semver.MustParse(proxy.Version.String()),
		devenvcommon.DefaultExecutorQualifier,
	)
	executorRef, err := env.DataStore.Addresses().Get(executorKey)
	require.NoError(t, err, "executor proxy address must exist in datastore")
	executorAddr, err := protocol.NewUnknownAddressFromHex(executorRef.Address)
	require.NoError(t, err)
	l.Info().Str("executorAddr", hex.EncodeToString(executorAddr)).Msg("Resolved EVM executor proxy address")

	// Look up CCV (VVR) address on the EVM source chain.
	ccvKey := datastore.NewAddressRefKey(
		evmDetails.ChainSelector,
		datastore.ContractType(versioned_verifier_resolver.ContractType),
		semver.MustParse(versioned_verifier_resolver.Version.String()),
		devenvcommon.DefaultCommitteeVerifierQualifier,
	)
	ccvRef, err := env.DataStore.Addresses().Get(ccvKey)
	require.NoError(t, err, "CCV (VVR) address must exist in datastore")
	ccvAddr, err := protocol.NewUnknownAddressFromHex(ccvRef.Address)
	require.NoError(t, err)
	l.Info().Str("ccvAddr", hex.EncodeToString(ccvAddr)).Msg("Resolved EVM CCV address")

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
		sendResult, err := evmChain.SendMessage(ctx, stellarDetails.ChainSelector,
			cciptestinterfaces.MessageFields{
				Receiver: stellarReceiver,
				Data:     []byte("hello from evm"),
			},
			cciptestinterfaces.MessageOptions{
				Version:           3,
				ExecutionGasLimit: 200_000,
				Executor:          executorAddr,
				CCVs: []protocol.CCV{{
					CCVAddress: ccvAddr,
					Args:       []byte{},
					ArgsLen:    0,
				}},
			},
		)
		require.NoError(t, err)
		l.Info().
			Str("messageID", hex.EncodeToString(sendResult.MessageID[:])).
			Int("receiptIssuers", len(sendResult.ReceiptIssuers)).
			Msg("CCIP message sent from EVM")

		// Wait for the sent event on the EVM chain.
		sentEvent, err := evmChain.WaitOneSentEventBySeqNo(ctx, stellarDetails.ChainSelector, seqNo, evmSentTimeout)
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
			datastore.ContractType(offramp.ContractType),
			semver.MustParse(offramp.Version.String()),
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
		execEvent, err := stellarChain.WaitOneExecEventBySeqNo(t.Context(), evmDetails.ChainSelector, seqNo, execTimeout)
		require.NoError(t, err)
		require.Equalf(
			t,
			cciptestinterfaces.ExecutionStateSuccess,
			execEvent.State,
			"message should have been successfully executed, return data: %x",
			execEvent.ReturnData,
		)

		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Uint64("seqNo", seqNo).
			Msg("Message executed successfully on Stellar")
	})
}
