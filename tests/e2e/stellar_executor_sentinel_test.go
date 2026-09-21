package e2e_tests

import (
	"encoding/hex"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	ccvchain "github.com/smartcontractkit/chainlink-stellar/tests/ccv/chain"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// Executor sentinel tags — must match common/message/src/lib.rs
// (NO_EXECUTION_TAG / USE_DEFAULT_TAG). Each is a 4-byte tag left-aligned in a
// 32-byte Soroban contract Address; the OnRamp recognises these instead of the
// EVM address(0)/NO_EXECUTION_ADDRESS sentinels (Soroban Address has no zero).
var (
	noExecutionSentinelRaw = append(
		[]byte{0xeb, 0xa5, 0x17, 0xd2}, // NO_EXECUTION_TAG
		make([]byte, 28)...,
	)
	useDefaultSentinelRaw = append(
		[]byte{0x72, 0x06, 0x8b, 0x37}, // USE_DEFAULT_TAG
		make([]byte, 28)...,
	)
)

// sentinelContractStrkey returns the strkey (VersionByteContract) form of a
// 32-byte sentinel, i.e. exactly what the OnRamp places in the executor receipt
// `Issuer` field when it leaves a no-execution sentinel in place. Used to assert
// against the emitted receipt.
func sentinelContractStrkey(t *testing.T, raw []byte) string {
	t.Helper()
	sk, err := strkey.Encode(strkey.VersionByteContract, raw)
	require.NoErrorf(t, err, "encode sentinel %x", raw)
	return sk
}

// findExecutorReceiptByIssuer scans the OnRamp CCIPMessageSent receipts for one
// whose Issuer matches `wantIssuer`. Executor receipts are keyed by the
// (possibly resolved) executor address, so this isolates the executor-layer
// receipt from the network-fee and per-CCV receipts.
func findExecutorReceiptByIssuer(
	t *testing.T,
	l *zerolog.Logger,
	receipts []onrampbindings.Receipt,
	wantIssuer string,
) onrampbindings.Receipt {
	t.Helper()
	for _, r := range receipts {
		if r.Issuer == wantIssuer {
			return r
		}
	}
	// Log all issuers to aid debugging on a mismatch.
	for i, r := range receipts {
		l.Warn().Int("idx", i).Str("issuer", r.Issuer).Msg("receipt issuer seen")
	}
	require.Failf(t, "executor receipt not found", "no receipt with issuer %q among %d receipts", wantIssuer, len(receipts))
	return onrampbindings.Receipt{}
}

// TestStellarToEVMNoExecutionSentinel exercises the M-7 / INV-NOEXEC-1/2 feature:
// a Stellar→EVM message whose GenericExtraArgsV3.executor is the
// no-execution sentinel (0xeba517d2…) is accepted by the OnRamp, which leaves
// the sentinel in place, zeroes the executor flat fee AND the execution-gas
// cost, and still emits an executor receipt with the sentinel as issuer.
//
// This bypasses BuildChainMessage (which hard-codes a default mock executor) by
// building the StellarToAnyMessage directly with a populated Executor field, so
// the OnRamp's sentinel-resolution path is actually driven.
//
// Contracts must be compiled (make build) and the devenv running:
//
//	CTF_CONFIGS=tests/env/env-stellar-evm.toml go run ./tests/testutils/cmd/devenv
//
//	go test -v -timeout 10m ./tests/e2e/... -run TestStellarToEVMNoExecutionSentinel
func TestStellarToEVMNoExecutionSentinel(t *testing.T) {
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
	stellarImpl, ok := stellarChain.(*ccvchain.Chain)
	require.True(t, ok, "Stellar chain is not *ccvchain.Chain")

	evmChain := env.Chains[evmDetails.ChainSelector]
	require.NotNil(t, evmChain, "EVM chain not found in chains map")

	evmReceiver, err := evmChain.GetEOAReceiverAddress()
	require.NoError(t, err)
	l.Info().Str("evmReceiver", hex.EncodeToString(evmReceiver)).Msg("Using EVM receiver address")

	wantIssuer := sentinelContractStrkey(t, noExecutionSentinelRaw)
	l.Info().Str("expectedSentinelIssuer", wantIssuer).Msg("Expected no-execution sentinel issuer")

	// Sequence number for filtering the CCIPMessageSent event.
	seqNo, err := stellarChain.GetExpectedNextSequenceNumber(ctx, evmDetails.ChainSelector)
	require.NoError(t, err)

	// Build the message with the no-execution sentinel in the executor field.
	// OutOfOrderExecution mirrors the devenv policy BuildChainMessage forces.
	msg, err := stellarImpl.BuildStellarMessageWithExecutor(ctx,
		cciptestinterfaces.MessageFields{
			Receiver: evmReceiver,
			Data:     []byte("no-exec sentinel e2e"),
		},
		cciptestinterfaces.MessageOptions{
			OutOfOrderExecution: true,
			Executor:            noExecutionSentinelRaw,
		},
	)
	require.NoError(t, err)

	sendResult, _, err := stellarImpl.SendChainMessage(ctx, evmDetails.ChainSelector, msg, ccvchain.StellarSendOptions{})
	require.NoError(t, err, "ccip_send with no-exec sentinel should succeed")
	l.Info().Str("messageID", hex.EncodeToString(sendResult.MessageID[:])).Msg("Sent no-exec sentinel message")

	sentEvent, err := stellarImpl.WaitForCCIPMessageSentEvent(ctx, evmDetails.ChainSelector,
		cciptestinterfaces.MessageEventKey{SeqNum: seqNo, MessageID: sendResult.MessageID},
		stellarSentTimeout)
	require.NoError(t, err)

	execReceipt := findExecutorReceiptByIssuer(t, l, sentEvent.Receipts, wantIssuer)
	require.NotNil(t, execReceipt.FeeTokenAmount, "executor receipt FeeTokenAmount must be present")
	require.Equalf(t, 0, execReceipt.FeeTokenAmount.Sign(),
		"no-exec sentinel must yield a ZERO executor fee (flat + exec-gas), got %s",
		execReceipt.FeeTokenAmount.String())
	l.Info().
		Str("issuer", execReceipt.Issuer).
		Msg("✅ no-execution sentinel: zero executor fee, sentinel-issued receipt")
}

// TestStellarToEVMUseDefaultSentinel exercises the M-5 / INV-ENC-5 feature: a
// Stellar→EVM message whose GenericExtraArgsV3.executor is the "use default"
// sentinel (0x72068b37…) is resolved by the OnRamp to the lane's
// default_executor BEFORE hashing and BEFORE Executor::get_fee. The emitted
// executor receipt is therefore issued by the concrete default Executor, NOT by
// the sentinel strkey — proving the address(0)→default parity.
//
//	go test -v -timeout 10m ./tests/e2e/... -run TestStellarToEVMUseDefaultSentinel
func TestStellarToEVMUseDefaultSentinel(t *testing.T) {
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
	stellarImpl, ok := stellarChain.(*ccvchain.Chain)
	require.True(t, ok, "Stellar chain is not *ccvchain.Chain")

	evmChain := env.Chains[evmDetails.ChainSelector]
	require.NotNil(t, evmChain, "EVM chain not found in chains map")

	evmReceiver, err := evmChain.GetEOAReceiverAddress()
	require.NoError(t, err)
	l.Info().Str("evmReceiver", hex.EncodeToString(evmReceiver)).Msg("Using EVM receiver address")

	// The concrete executor the sentinel must resolve to.
	defaultExecutor, err := stellarImpl.GetDefaultExecutor(ctx, evmDetails.ChainSelector)
	require.NoError(t, err)
	require.NotEmpty(t, defaultExecutor, "OnRamp default_executor must be configured")
	l.Info().Str("defaultExecutor", defaultExecutor).Msg("Lane default_executor (expected receipt issuer)")

	// Sanity: the resolved default must NOT equal the sentinel strkey — that
	// would mean resolution did not happen.
	sentinelStrkey := sentinelContractStrkey(t, useDefaultSentinelRaw)
	require.NotEqual(t, sentinelStrkey, defaultExecutor,
		"default_executor must be a concrete contract, not the use-default sentinel itself")

	seqNo, err := stellarChain.GetExpectedNextSequenceNumber(ctx, evmDetails.ChainSelector)
	require.NoError(t, err)

	msg, err := stellarImpl.BuildStellarMessageWithExecutor(ctx,
		cciptestinterfaces.MessageFields{
			Receiver: evmReceiver,
			Data:     []byte("use-default sentinel e2e"),
		},
		cciptestinterfaces.MessageOptions{
			OutOfOrderExecution: true,
			Executor:            useDefaultSentinelRaw,
		},
	)
	require.NoError(t, err)

	sendResult, _, err := stellarImpl.SendChainMessage(ctx, evmDetails.ChainSelector, msg, ccvchain.StellarSendOptions{})
	require.NoError(t, err, "ccip_send with use-default sentinel should succeed (resolves to real Executor)")
	l.Info().Str("messageID", hex.EncodeToString(sendResult.MessageID[:])).Msg("Sent use-default sentinel message")

	sentEvent, err := stellarImpl.WaitForCCIPMessageSentEvent(ctx, evmDetails.ChainSelector,
		cciptestinterfaces.MessageEventKey{SeqNum: seqNo, MessageID: sendResult.MessageID},
		stellarSentTimeout)
	require.NoError(t, err)

	// The receipt must be issued by the resolved default_executor, proving the
	// sentinel was resolved before receipt emission (M-5 parity).
	execReceipt := findExecutorReceiptByIssuer(t, l, sentEvent.Receipts, defaultExecutor)
	require.NotNil(t, execReceipt.FeeTokenAmount, "executor receipt FeeTokenAmount must be present")
	require.GreaterOrEqualf(t, execReceipt.FeeTokenAmount.Sign(), 0,
		"use-default executor fee must be non-negative, got %s", execReceipt.FeeTokenAmount.String())
	l.Info().
		Str("issuer", execReceipt.Issuer).
		Str("feeTokenAmount", execReceipt.FeeTokenAmount.String()).
		Msg("✅ use-default sentinel: resolved to concrete default_executor, real executor receipt")
}
