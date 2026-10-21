package staging_tests

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/stretchr/testify/require"

	erc20 "github.com/smartcontractkit/chainlink-ccip/chains/evm/gobindings/generated/v1_5_1/factory_burn_mint_erc20"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
)

// Staging tests that send CCIP messages from an EVM chain (Sepolia by default)
// to Stellar testnet through an already-deployed environment, over the same
// lane as stellar_to_evm_send_test.go. Every account and address comes from
// STAGING_* environment variables (see config_test.go and README.md).
//
// Run from the tests/ module:
//
//	set -a; source staging/staging.env; set +a
//	go test -v -count=1 -timeout 45m ./staging/... -run TestStagingEVMToStellar

// TestStagingEVMToStellarMessage sends a single data-only message via the EVM
// Router.ccipSend, checks the OnRamp's CCIPMessageSent event and the exact fee
// paid, then waits for successful execution on the Stellar OffRamp and checks
// the receiver recorded the message.
func TestStagingEVMToStellarMessage(t *testing.T) {
	ctx := t.Context()
	env := newEVMToStellarEnv(t, ctx)
	cfg := env.cfg

	supported, err := env.router.IsChainSupported(&bind.CallOpts{Context: ctx}, cfg.StellarChainSelector)
	require.NoError(t, err)
	require.True(t, supported, "EVM Router has no OnRamp for Stellar chain %d", cfg.StellarChainSelector)

	startLedger := env.latestStellarLedger(t, ctx)
	nativeBefore, tokenBefore := env.balances(t, ctx)

	data := []byte(fmt.Sprintf("staging evm->stellar %s", time.Now().UTC().Format(time.RFC3339Nano)))
	sent := env.sendAndConfirm(t, ctx, env.buildMessage(t, data))

	ev := sent.Event
	require.Equal(t, cfg.StellarChainSelector, ev.DestChainSelector)
	require.Equal(t, env.sender, ev.Sender)
	require.NotEmpty(t, ev.Receipts, "CCIPMessageSent carried no receipts")
	// The Stellar OffRamp matches the message's offRamp field against its own raw
	// 32-byte contract id; any envelope around it fails execution.
	_, offRamp, err := messageV1Addresses(ev.EncodedMessage)
	require.NoError(t, err)
	require.Truef(t, bytes.Equal(env.offRampRaw, offRamp),
		"encoded offRamp 0x%x != Stellar OffRamp %s (0x%x)", offRamp, env.offRampID, env.offRampRaw)

	// Router pulls exactly the quote. Paying natively, the sender's ETH also covers
	// the tx gas, which the receipt prices exactly; an ERC-20 fee comes only out of
	// the token balance (the approval's gas is a separate tx).
	nativeAfter, tokenAfter := env.balances(t, ctx)
	nativeSpent := new(big.Int).Sub(nativeBefore, nativeAfter)
	if env.nativeFee() {
		gasCost := new(big.Int).Mul(new(big.Int).SetUint64(sent.Receipt.GasUsed), sent.Receipt.EffectiveGasPrice)
		want := new(big.Int).Add(sent.Fee, gasCost)
		require.Zerof(t, nativeSpent.Cmp(want), "spent %s != fee %s + gas %s", nativeSpent, sent.Fee, gasCost)
	} else {
		tokenSpent := new(big.Int).Sub(tokenBefore, tokenAfter)
		require.Zerof(t, tokenSpent.Cmp(sent.Fee), "spent %s fee token != quoted fee %s", tokenSpent, sent.Fee)
	}

	exec := env.waitForStellarExecution(t, ctx, startLedger, sent.MessageID)
	env.l.Info().
		Str("messageID", hexID(sent.MessageID)).
		Uint64("sequenceNumber", exec.SequenceNumber).
		Str("txHash", exec.TxHash).
		Uint32("ledger", exec.Ledger).
		Msg("ExecutionStateChanged observed on Stellar")
	require.Equalf(t, offrampbindings.MessageExecutionStateSuccess, exec.State,
		"message executed with state %d, return data 0x%x", exec.State, exec.ReturnData)
	require.Equal(t, cfg.EVMChainSelector, exec.SourceChainSelector)
	require.Equal(t, sent.MessageNumber, exec.SequenceNumber, "Stellar sequence number should match the EVM message number")

	last, err := env.receiver.LastMessageId(ctx)
	require.NoError(t, err, "receiver last_message_id")
	require.Equal(t, sent.MessageID, last, "receiver did not record the message")
}

// TestStagingEVMToStellarSequentialMessages sends STAGING_SEND_BATCH_SIZE
// messages back-to-back from the same account and checks they get distinct
// message IDs and strictly increasing message numbers (other senders on the
// shared lane may interleave, so gaps are allowed). Every message must execute
// successfully on Stellar.
func TestStagingEVMToStellarSequentialMessages(t *testing.T) {
	ctx := t.Context()
	env := newEVMToStellarEnv(t, ctx)
	cfg := env.cfg
	require.Positive(t, cfg.BatchSize, "%s must be > 0", envSendBatchSize)

	startLedger := env.latestStellarLedger(t, ctx)
	runID := time.Now().UTC().Format(time.RFC3339Nano)
	sent := make([]evmSentMessage, 0, cfg.BatchSize)
	seen := make(map[[32]byte]bool, cfg.BatchSize)
	for i := range cfg.BatchSize {
		data := []byte(fmt.Sprintf("staging evm->stellar batch %s #%d", runID, i))
		s := env.sendAndConfirm(t, ctx, env.buildMessage(t, data))

		require.False(t, seen[s.MessageID], "duplicate message id %s", hexID(s.MessageID))
		seen[s.MessageID] = true
		if i > 0 {
			require.Greater(t, s.MessageNumber, sent[i-1].MessageNumber, "message number did not increase")
		}
		sent = append(sent, s)
	}

	for _, s := range sent {
		exec := env.waitForStellarExecution(t, ctx, startLedger, s.MessageID)
		require.Equalf(t, offrampbindings.MessageExecutionStateSuccess, exec.State,
			"message %s executed with state %d, return data 0x%x", hexID(s.MessageID), exec.State, exec.ReturnData)
	}

	// Out-of-order execution is allowed, so the receiver's last message is one of
	// the batch, not necessarily the last one sent.
	last, err := env.receiver.LastMessageId(ctx)
	require.NoError(t, err, "receiver last_message_id")
	require.True(t, seen[last], "receiver's last message %s is not from this batch", hexID(last))
}

// TestStagingEVMToStellarUnsupportedDestination checks the EVM Router rejects
// a destination selector with no OnRamp, both when quoting and when sending.
// The send is only gas-estimated, never broadcast, so it spends nothing.
func TestStagingEVMToStellarUnsupportedDestination(t *testing.T) {
	ctx := t.Context()
	env := newEVMToStellarEnv(t, ctx)

	const unknownSelector uint64 = 1 // not a registered chain selector
	supported, err := env.router.IsChainSupported(&bind.CallOpts{Context: ctx}, unknownSelector)
	require.NoError(t, err)
	require.False(t, supported)

	wantRevert := revertSelector("UnsupportedDestinationChain(uint64)")
	msg := env.buildMessage(t, []byte("staging: should be rejected"))
	_, err = env.router.GetFee(&bind.CallOpts{Context: ctx}, unknownSelector, msg)
	require.Error(t, err, "getFee to an unsupported destination should revert")
	require.Truef(t, len(revertData(err)) >= 8 && revertData(err)[:8] == wantRevert,
		"getFee revert 0x%s, want UnsupportedDestinationChain (0x%s): %v", revertData(err), wantRevert, err)

	nonceBefore, err := env.evm.NonceAt(ctx, env.sender, nil)
	require.NoError(t, err)
	opts := env.transactOpts(t, ctx)
	opts.NoSend = true
	_, err = env.router.CcipSend(opts, unknownSelector, msg)
	require.Error(t, err, "ccipSend to an unsupported destination should fail gas estimation")
	env.l.Info().Err(err).Msg("ccipSend rejected as expected")

	nonceAfter, err := env.evm.NonceAt(ctx, env.sender, nil)
	require.NoError(t, err)
	require.Equal(t, nonceBefore, nonceAfter, "a transaction was sent for the rejected ccipSend")
}

// balances returns the sender's native balance and, when paying in an ERC-20,
// its fee-token balance (nil when paying natively).
func (e *evmToStellarEnv) balances(t *testing.T, ctx context.Context) (native, token *big.Int) {
	t.Helper()
	native, err := e.evm.BalanceAt(ctx, e.sender, nil)
	require.NoError(t, err)
	if e.nativeFee() {
		return native, nil
	}
	fee, err := erc20.NewFactoryBurnMintERC20(e.cfg.EVMFeeToken, e.evm)
	require.NoError(t, err)
	token, err = fee.BalanceOf(&bind.CallOpts{Context: ctx}, e.sender)
	require.NoError(t, err)
	return native, token
}
