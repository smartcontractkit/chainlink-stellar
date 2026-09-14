package e2e_tests

import (
	"encoding/hex"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	ccvchain "github.com/smartcontractkit/chainlink-stellar/tests/ccv/chain"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

const (
	tokenTransferSentTimeout = 40 * time.Second
	tokenTransferAmount      = int64(1_000_000) // 0.1 token in SAC base units (7 decimals)
)

// TestStellarToEVMTokenTransfer validates the Stellar-to-EVM token transfer flow:
//
//  1. Fund sender with test SAC tokens
//  2. Send ccip_send via Router with TokenAmounts populated
//  3. OnRamp calls lock_or_burn on the lock-release pool
//  4. Verifiers + Indexer process the message
//  5. EVM Executor triggers OffRamp release/mint on the EVM side
//
// Prerequisites:
//
//	make build
//	CTF_CONFIGS=tests/env/env-stellar-evm.toml go run ./tests/testutils/cmd/devenv
//
// Run:
//
//	go test -v -timeout 10m ./tests/e2e/... -run TestStellarToEVMTokenTransfer
func TestStellarToEVMTokenTransfer(t *testing.T) {
	configOutputPath := "../env/env-stellar-evm-out.toml"

	stellarChainID := chainsel.STELLAR_LOCALNET.ChainID
	stellarSelector := chainsel.STELLAR_LOCALNET.Selector

	ctx := ccv.Plog.WithContext(t.Context())
	l := zerolog.Ctx(ctx)

	env := helpers.NewE2ETestEnv(t, ctx, l, configOutputPath, stellarChainID, stellarSelector)
	stellarDetails := env.SourceChainDetails
	evmDetails := env.DestChainDetails

	stellarChain := env.Chains[stellarDetails.ChainSelector]
	require.NotNil(t, stellarChain, "Stellar chain not found")

	evmChain := env.Chains[evmDetails.ChainSelector]
	require.NotNil(t, evmChain, "EVM chain not found")

	stellarCcvChain, ok := stellarChain.(*ccvchain.Chain)
	require.True(t, ok, "Stellar chain must be *ccvchain.Chain")

	tokenAddr, err := stellarCcvChain.GetTokenAddress()
	require.NoError(t, err, "test token must be deployed")
	l.Info().Str("tokenAddress", tokenAddr).Msg("Using test SAC token")

	tokenRaw, err := strkey.Decode(strkey.VersionByteContract, tokenAddr)
	require.NoError(t, err)

	senderAddr, err := stellarChain.GetSenderAddress()
	require.NoError(t, err)

	t.Run("stellar_to_evm_token_transfer", func(t *testing.T) {
		evmReceiver, err := evmChain.GetEOAReceiverAddress()
		require.NoError(t, err)

		balBefore, err := stellarChain.GetTokenBalance(ctx, senderAddr, protocol.UnknownAddress(tokenRaw))
		require.NoError(t, err)
		l.Info().Str("balance", balBefore.String()).Msg("Sender token balance before transfer")
		require.True(t, balBefore.Int64() >= tokenTransferAmount,
			"sender must have enough tokens; balance=%s, need=%d", balBefore, tokenTransferAmount)

		seqNo, err := stellarChain.GetExpectedNextSequenceNumber(ctx, evmDetails.ChainSelector)
		require.NoError(t, err)

		sendResult, err := stellarChain.SendMessage(ctx, evmDetails.ChainSelector,
			cciptestinterfaces.MessageFields{
				Receiver: evmReceiver,
				Data:     []byte("token-transfer-test"),
				TokenAmount: cciptestinterfaces.TokenAmount{
					Amount:       big.NewInt(tokenTransferAmount),
					TokenAddress: protocol.UnknownAddress(tokenRaw),
				},
			},
			cciptestinterfaces.MessageOptions{},
			messageV3Version,
		)
		require.NoError(t, err)
		l.Info().
			Str("messageID", hex.EncodeToString(sendResult.MessageID[:])).
			Msg("Token transfer message sent from Stellar")

		sentEvent, err := stellarChain.ConfirmSendOnSource(ctx, evmDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, tokenTransferSentTimeout)
		require.NoError(t, err)
		l.Info().
			Str("messageID", hex.EncodeToString(sentEvent.MessageID[:])).
			Msg("Sent event confirmed")

		balAfter, err := stellarChain.GetTokenBalance(ctx, senderAddr, protocol.UnknownAddress(tokenRaw))
		require.NoError(t, err)
		l.Info().Str("balance", balAfter.String()).Msg("Sender token balance after transfer")

		locked := new(big.Int).Sub(balBefore, balAfter)
		require.Equal(t, tokenTransferAmount, locked.Int64(),
			"sender balance should decrease by exactly the transfer amount")

		l.Info().
			Str("messageID", hex.EncodeToString(sentEvent.MessageID[:])).
			Int64("lockedAmount", locked.Int64()).
			Msg("Token transfer source-side flow validated: tokens locked, CCIPMessageSent emitted")

		// TODO(NONEVM-3946): The aggregator currently rejects CCV data for messages
		// that contain token transfers (InvalidArgument: validation failed). Once the
		// aggregator supports the token-transfer message format, uncomment to verify
		// the full pipeline:
		//
		// defaultAggregatorClient := env.AggregatorClients[devenvcommon.DefaultCommitteeVerifierQualifier]
		// require.NotNil(t, defaultAggregatorClient)
		// testCtx := e2e.NewTestingContext(t, t.Context(), env.Chains, defaultAggregatorClient, env.IndexerMonitor)
		// result, err := testCtx.AssertMessage(protocol.Bytes32(sentEvent.MessageID), e2e.AssertMessageOptions{
		// 	TickInterval:            1 * time.Second,
		// 	ExpectedVerifierResults: 1,
		// 	Timeout:                 tests.WaitTimeout(t),
		// 	AssertVerifierLogs:      false,
		// 	AssertExecutorLogs:      false,
		// })
		// require.NoError(t, err)
		// require.NotNil(t, result.AggregatedResult)
		//
		// execEvent, err := evmChain.ConfirmExecOnDest(ctx, stellarDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, 5*time.Minute)
		// require.NoError(t, err)
		// require.Equal(t, cciptestinterfaces.ExecutionStateSuccess, execEvent.State)
	})
}

// TestStellarToEVMTokenTransferFees validates fee collection during a
// Stellar-to-EVM token transfer. Fees are paid in the fee token (a separate
// SAC from the transferred token), so this test tracks sender balances for both:
//
//  1. Transferred token: sender loses exactly tokenTransferAmount
//  2. Fee token: sender's fee-token balance decreases (fee charged by Router/OnRamp)
//
// The fee SAC is FeeQuoter's link_token (from get_static_config). The test asserts
// that address appears in get_fee_tokens, then uses it for balance assertions.
//
// Note: pool and OnRamp contract balances are not checked because
// GetTokenBalance encodes all addresses as classic accounts (G-prefix),
// which fails for contract addresses that hold SAC balances via contract
// storage rather than trustlines.
func TestStellarToEVMTokenTransferFees(t *testing.T) {
	configOutputPath := "../env/env-stellar-evm-out.toml"

	stellarChainID := chainsel.STELLAR_LOCALNET.ChainID
	stellarSelector := chainsel.STELLAR_LOCALNET.Selector

	ctx := ccv.Plog.WithContext(t.Context())
	l := zerolog.Ctx(ctx)

	env := helpers.NewE2ETestEnv(t, ctx, l, configOutputPath, stellarChainID, stellarSelector)
	stellarDetails := env.SourceChainDetails
	evmDetails := env.DestChainDetails

	stellarChain := env.Chains[stellarDetails.ChainSelector]
	require.NotNil(t, stellarChain, "Stellar chain not found")

	evmChain := env.Chains[evmDetails.ChainSelector]
	require.NotNil(t, evmChain, "EVM chain not found")

	stellarCcvChain, ok := stellarChain.(*ccvchain.Chain)
	require.True(t, ok, "Stellar chain must be *ccvchain.Chain")

	tokenAddr, err := stellarCcvChain.GetTokenAddress()
	require.NoError(t, err)
	tokenRaw, err := strkey.Decode(strkey.VersionByteContract, tokenAddr)
	require.NoError(t, err)

	fqClient := stellarCcvChain.FeeQuoterClient()
	require.NotNil(t, fqClient, "FeeQuoter must be initialized")

	staticCfg, err := fqClient.GetStaticConfig(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, staticCfg.LinkToken, "FeeQuoter static config must set link_token (fee SAC)")

	feeTokenStrkeys, err := fqClient.GetFeeTokens(ctx)
	require.NoError(t, err)
	require.True(t, slices.Contains(feeTokenStrkeys, staticCfg.LinkToken),
		"configured fee token (link_token) %q must be listed by get_fee_tokens; got %#v",
		staticCfg.LinkToken, feeTokenStrkeys)

	feeTokenAddr := staticCfg.LinkToken
	feeTokenRaw, err := strkey.Decode(strkey.VersionByteContract, feeTokenAddr)
	require.NoError(t, err)

	senderAddr, err := stellarChain.GetSenderAddress()
	require.NoError(t, err)

	t.Run("fee_collection_on_token_transfer", func(t *testing.T) {
		evmReceiver, err := evmChain.GetEOAReceiverAddress()
		require.NoError(t, err)

		senderTokenBefore, err := stellarChain.GetTokenBalance(ctx, senderAddr, protocol.UnknownAddress(tokenRaw))
		require.NoError(t, err)

		senderFeeBefore, err := stellarChain.GetTokenBalance(ctx, senderAddr, protocol.UnknownAddress(feeTokenRaw))
		require.NoError(t, err)

		l.Info().
			Str("sender_token", senderTokenBefore.String()).
			Str("sender_fee_token", senderFeeBefore.String()).
			Msg("Sender balances before transfer")

		seqNo, err := stellarChain.GetExpectedNextSequenceNumber(ctx, evmDetails.ChainSelector)
		require.NoError(t, err)

		_, err = stellarChain.SendMessage(ctx, evmDetails.ChainSelector,
			cciptestinterfaces.MessageFields{
				Receiver: evmReceiver,
				Data:     []byte("fee-test"),
				TokenAmount: cciptestinterfaces.TokenAmount{
					Amount:       big.NewInt(tokenTransferAmount),
					TokenAddress: protocol.UnknownAddress(tokenRaw),
				},
			},
			cciptestinterfaces.MessageOptions{},
			messageV3Version,
		)
		require.NoError(t, err)

		_, err = stellarChain.ConfirmSendOnSource(ctx, evmDetails.ChainSelector, cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, tokenTransferSentTimeout)
		require.NoError(t, err)

		senderTokenAfter, err := stellarChain.GetTokenBalance(ctx, senderAddr, protocol.UnknownAddress(tokenRaw))
		require.NoError(t, err)

		senderFeeAfter, err := stellarChain.GetTokenBalance(ctx, senderAddr, protocol.UnknownAddress(feeTokenRaw))
		require.NoError(t, err)

		tokenDelta := new(big.Int).Sub(senderTokenBefore, senderTokenAfter)
		feeDelta := new(big.Int).Sub(senderFeeBefore, senderFeeAfter)

		l.Info().
			Str("sender_token_delta", tokenDelta.String()).
			Str("sender_fee_delta", feeDelta.String()).
			Msg("Sender balance deltas after token transfer")

		require.Equal(t, tokenTransferAmount, tokenDelta.Int64(),
			"sender transferred-token balance should decrease by exactly the transfer amount")

		require.True(t, feeDelta.Sign() > 0,
			"sender fee-token balance should decrease (fee charged); got delta=%s", feeDelta)

		l.Info().Str("fee_paid", feeDelta.String()).Msg("Fee collection validated")
	})
}
