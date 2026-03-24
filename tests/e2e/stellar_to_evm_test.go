package e2e_tests

import (
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	vvrops "github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v1_7_0/versioned_verifier_resolver"
	onrampoperations "github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v2_0_0/operations/onramp"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-common/pkg/utils/tests"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	ccvchain "github.com/smartcontractkit/chainlink-stellar/ccv/chain"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
	"github.com/stellar/go-stellar-sdk/strkey"
)

const (
	stellarSentTimeout = 30 * time.Second
)

// TestStellarToEVMExecution validates the full Stellar-to-EVM CCIP message flow:
// Stellar Router → OnRamp → Verifiers → Indexer → EVM Executor → EVM OffRamp.
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
//	go test -v -timeout 10m ./tests/e2e/... -run TestStellarToEVMExecution
func TestStellarToEVMExecution(t *testing.T) {
	configOutputPath := "../env/env-stellar-evm-out.toml"

	stellarChainID := chainsel.STELLAR_LOCALNET.ChainID
	stellarSelector := chainsel.STELLAR_LOCALNET.Selector

	ctx := ccv.Plog.WithContext(t.Context())
	l := zerolog.Ctx(ctx)

	env := helpers.NewE2ETestEnv(t, ctx, l, configOutputPath, stellarChainID, stellarSelector)
	stellarDetails := env.SourceChainDetails
	evmDetails := env.DestChainDetails

	stellarChain := env.Chains[stellarDetails.ChainSelector]
	require.NotNil(t, stellarChain, "Stellar chain not found in chains map")

	evmChain := env.Chains[evmDetails.ChainSelector]
	require.NotNil(t, evmChain, "EVM chain not found in chains map")

	t.Run("stellar_to_evm_execution", func(t *testing.T) {
		evmReceiver, err := evmChain.GetEOAReceiverAddress()
		require.NoError(t, err)
		l.Info().Str("evmReceiver", hex.EncodeToString(evmReceiver)).Msg("Using EVM receiver address")

		seqNo, err := stellarChain.GetExpectedNextSequenceNumber(ctx, evmDetails.ChainSelector)
		require.NoError(t, err)
		l.Info().Uint64("seqNo", seqNo).Msg("Expected next sequence number from Stellar OnRamp")

		sendResult, err := stellarChain.SendMessage(ctx, evmDetails.ChainSelector,
			cciptestinterfaces.MessageFields{
				Receiver: evmReceiver,
				Data:     []byte("hello from stellar"),
			},
			cciptestinterfaces.MessageOptions{},
		)
		require.NoError(t, err)
		l.Info().
			Str("messageID", hex.EncodeToString(sendResult.MessageID[:])).
			Msg("CCIP message sent from Stellar")

		sentEvent, err := stellarChain.WaitOneSentEventBySeqNo(ctx, evmDetails.ChainSelector, seqNo, stellarSentTimeout)
		require.NoError(t, err)
		messageID := sentEvent.MessageID
		l.Info().
			Str("messageID", hex.EncodeToString(messageID[:])).
			Msg("Sent event confirmed on Stellar")

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

		// TODO: uncomment once EVM executor is wired up for Stellar-sourced messages.
		// execEvent, err := evmChain.WaitOneExecEventBySeqNo(t.Context(), stellarDetails.ChainSelector, seqNo, execTimeout)
		// require.NoError(t, err)
		// require.Equalf(
		// 	t,
		// 	cciptestinterfaces.ExecutionStateSuccess,
		// 	execEvent.State,
		// 	"message should have been successfully executed, return data: %x",
		// 	execEvent.ReturnData,
		// )
		//
		// l.Info().
		// 	Str("messageID", hex.EncodeToString(messageID[:])).
		// 	Uint64("seqNo", seqNo).
		// 	Msg("Message executed successfully on EVM")
	})
}

// mockStellarContractID returns a deterministic Soroban contract strkey (same scheme as ccv/chain.SendMessage).
func mockStellarContractID(deployerGAddress, name string) string {
	salt := stellardeployment.GenerateDeterministicSalt(deployerGAddress, name)
	encoded, err := strkey.Encode(strkey.VersionByteContract, salt[:])
	if err != nil {
		panic(fmt.Errorf("encode mock contract id: %w", err))
	}
	return encoded
}

func mustStellarOnRampClient(t *testing.T, env *helpers.E2ETestEnv) *onrampbindings.OnRampClient {
	t.Helper()
	key := datastore.NewAddressRefKey(
		env.SourceChainDetails.ChainSelector,
		datastore.ContractType(onrampoperations.ContractType),
		semver.MustParse(onrampoperations.Deploy.Version()),
		"",
	)
	ref, err := env.DataStore.Addresses().Get(key)
	require.NoError(t, err)
	contractID, err := scval.HexToContractStrkey(ref.Address)
	require.NoError(t, err)
	return onrampbindings.NewOnRampClient(env.Deployer, contractID)
}

func mustStellarVVRContractID(t *testing.T, env *helpers.E2ETestEnv) string {
	t.Helper()
	ccvKey := datastore.NewAddressRefKey(
		env.SourceChainDetails.ChainSelector,
		datastore.ContractType(vvrops.CommitteeVerifierResolverType),
		semver.MustParse(vvrops.Deploy.Version()),
		devenvcommon.DefaultCommitteeVerifierQualifier,
	)
	ref, err := env.DataStore.Addresses().Get(ccvKey)
	require.NoError(t, err)
	vvrID, err := scval.HexToContractStrkey(ref.Address)
	require.NoError(t, err)
	return vvrID
}

func mustBuildStellarToEVMOutboundMessage(t *testing.T, env *helpers.E2ETestEnv, evmReceiver []byte) onrampbindings.StellarToAnyMessage {
	t.Helper()
	vvrID := mustStellarVVRContractID(t, env)
	executorID := mockStellarContractID(env.DeployerKP.Address(), "executor")
	extraArgs := onrampbindings.GenericExtraArgsV3{
		Ccvs:               []string{vvrID},
		CcvArgs:            [][]byte{{}},
		Executor:           executorID,
		ExecutorArgs:       []byte{},
		GasLimit:           0,
		BlockConfirmations: 0,
		TokenReceiver:      []byte{},
		TokenArgs:          []byte{},
	}
	encodedExtraArgs, err := ccvchain.EncodeExtraArgsV3(extraArgs)
	require.NoError(t, err)
	feeToken := mockStellarContractID(env.DeployerKP.Address(), "fee-token")
	return onrampbindings.StellarToAnyMessage{
		Receiver:     evmReceiver,
		Data:         []byte("unhappy-path probe"),
		TokenAmounts: nil,
		FeeToken:     feeToken,
		ExtraArgs:    encodedExtraArgs,
	}
}

// TestStellarOnRampUnsupportedDestinationChain exercises OnRamp.get_fee when DEST_CHAINS has no entry
// for the requested selector (CCIPError::DestinationChainNotSupported in onramp get_dest_chain_config_internal).
//
// Devenv and env file requirements match TestStellarToEVMExecution.
//
//	go test -v -timeout 10m ./tests/e2e/... -run TestStellarOnRampUnsupportedDestinationChain
func TestStellarOnRampUnsupportedDestinationChain(t *testing.T) {
	configOutputPath := "../env/env-stellar-evm-out.toml"
	ctx := ccv.Plog.WithContext(t.Context())
	l := zerolog.Ctx(ctx)

	env := helpers.NewE2ETestEnv(t, ctx, l, configOutputPath, chainsel.STELLAR_LOCALNET.ChainID, chainsel.STELLAR_LOCALNET.Selector)
	evmDetails := env.DestChainDetails

	evmReceiver, err := env.Chains[evmDetails.ChainSelector].GetEOAReceiverAddress()
	require.NoError(t, err)

	msg := mustBuildStellarToEVMOutboundMessage(t, env, evmReceiver)
	onramp := mustStellarOnRampClient(t, env)

	// No lane / dest config is registered for this selector in the Stellar OnRamp.
	const bogusDestSelector uint64 = 0xDEADBEEFCAFEBABE
	_, err = onramp.GetFee(ctx, bogusDestSelector, msg)
	require.Error(t, err, "expected OnRamp get_fee to fail with DestinationChainNotSupported")
}

// TestStellarOnRampRejectsMultipleTokensPerMessage exercises StellarToAnyMessage::validate via OnRamp.get_fee
// when token_amounts has more than one entry (CCIPError::CanOnlySendOneTokenPerMessage).
//
//	go test -v -timeout 10m ./tests/e2e/... -run TestStellarOnRampRejectsMultipleTokensPerMessage
func TestStellarOnRampRejectsMultipleTokensPerMessage(t *testing.T) {
	configOutputPath := "../env/env-stellar-evm-out.toml"
	ctx := ccv.Plog.WithContext(t.Context())
	l := zerolog.Ctx(ctx)

	env := helpers.NewE2ETestEnv(t, ctx, l, configOutputPath, chainsel.STELLAR_LOCALNET.ChainID, chainsel.STELLAR_LOCALNET.Selector)
	evmDetails := env.DestChainDetails

	evmReceiver, err := env.Chains[evmDetails.ChainSelector].GetEOAReceiverAddress()
	require.NoError(t, err)

	msg := mustBuildStellarToEVMOutboundMessage(t, env, evmReceiver)
	tokenA := mockStellarContractID(env.DeployerKP.Address(), "token-a")
	tokenB := mockStellarContractID(env.DeployerKP.Address(), "token-b")
	msg.TokenAmounts = []onrampbindings.TokenAmount{
		{Amount: 1, Token: tokenA},
		{Amount: 1, Token: tokenB},
	}

	onramp := mustStellarOnRampClient(t, env)
	_, err = onramp.GetFee(ctx, evmDetails.ChainSelector, msg)
	require.Error(t, err, "expected OnRamp get_fee to fail with CanOnlySendOneTokenPerMessage")
}
