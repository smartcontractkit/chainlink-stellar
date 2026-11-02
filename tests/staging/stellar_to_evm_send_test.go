package staging_tests

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/stretchr/testify/require"

	erc20 "github.com/smartcontractkit/chainlink-ccip/chains/evm/gobindings/generated/v1_5_1/factory_burn_mint_erc20"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	bnmtokenbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/bnm_token"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
)

// Staging tests that send CCIP messages from Stellar testnet to an EVM chain
// (Sepolia by default) through an already-deployed environment. Unlike
// tests/e2e they do not start a devenv; every account and address comes from
// STAGING_* environment variables (see config_test.go and README.md).
//
// Run from the tests/ module:
//
//	set -a; source staging/staging.env; set +a
//	go test -v -count=1 -timeout 45m ./staging/... -run TestStagingStellarToEVM

// errUnsupportedDestinationChain is CCIPError::UnsupportedDestinationChain (63)
// as rendered in Soroban simulation errors.
const errUnsupportedDestinationChain = "Error(Contract, #63)"

// TestStagingStellarToEVMMessage sends a single data-only message via
// Router.ccip_send, checks the OnRamp's CCIPMessageSent event and the exact fee
// pull, and — when STAGING_EVM_RPC_URL/STAGING_EVM_OFFRAMP are set — waits for
// successful execution on the destination OffRamp.
func TestStagingStellarToEVMMessage(t *testing.T) {
	ctx := t.Context()
	env := newStagingEnv(t, ctx)
	cfg := env.cfg

	supported, err := env.router.IsChainSupported(ctx, cfg.DestChainSelector)
	require.NoError(t, err)
	require.True(t, supported, "Router has no OnRamp for dest chain %d", cfg.DestChainSelector)

	var watcher *evmExecWatcher
	if cfg.ExecCheckEnabled() {
		watcher = env.newEVMExecWatcher(t, ctx)
	}

	nativeFee, err := cfg.isNativeFeeToken()
	require.NoError(t, err)
	balanceBefore := env.feeTokenBalance(t, ctx)

	data := []byte(fmt.Sprintf("staging stellar->evm %s", time.Now().UTC().Format(time.RFC3339Nano)))
	sent := env.sendAndConfirm(t, ctx, env.buildMessage(t, data))

	ev := sent.Event
	require.Equal(t, cfg.DestChainSelector, ev.DestChainSelector)
	require.Equal(t, cfg.Sender.Address(), ev.Sender)
	require.Equal(t, cfg.FeeTokenID, ev.FeeToken)
	require.NotEmpty(t, ev.EncodedMessage)
	// One receipt per CCV, plus the executor and network-fee receipts.
	require.Len(t, ev.Receipts, len(cfg.CCVs)+2, "unexpected receipt count")

	// Router pulls exactly the quote (no overpay/refund). With native XLM as the
	// fee token the same balance also pays network fees, so only bound it.
	spent := new(big.Int).Sub(balanceBefore, env.feeTokenBalance(t, ctx))
	if nativeFee {
		require.GreaterOrEqual(t, spent.Cmp(sent.Fee), 0, "spent %s < quoted fee %s", spent, sent.Fee)
	} else {
		require.Zero(t, spent.Cmp(sent.Fee), "spent %s != quoted fee %s", spent, sent.Fee)
	}

	if watcher == nil {
		t.Logf("message %s sent; set %s and %s to also verify execution on the destination",
			hexID(sent.MessageID), envEVMRPCURL, envEVMOffRamp)
		return
	}

	exec := env.waitForExecution(t, ctx, watcher, sent.MessageID)
	env.l.Info().
		Str("messageID", hexID(sent.MessageID)).
		Uint64("messageNumber", exec.MessageNumber).
		Str("txHash", exec.Raw.TxHash.Hex()).
		Uint64("block", exec.Raw.BlockNumber).
		Msg("ExecutionStateChanged observed on destination")
	require.Equalf(t, cciptestinterfaces.ExecutionStateSuccess, cciptestinterfaces.MessageExecutionState(exec.State),
		"message executed with state %d, return data 0x%x", exec.State, exec.ReturnData)
	require.Equal(t, ev.SequenceNumber, exec.MessageNumber, "dest message number should match source sequence number")
}

// TestStagingStellarToEVMSequentialMessages sends STAGING_SEND_BATCH_SIZE
// messages back-to-back from the same account and checks they get distinct
// message IDs and strictly increasing sequence numbers (other senders on the
// shared lane may interleave, so gaps are allowed). With execution checks
// enabled, every message must execute successfully on the destination.
func TestStagingStellarToEVMSequentialMessages(t *testing.T) {
	ctx := t.Context()
	env := newStagingEnv(t, ctx)
	cfg := env.cfg
	require.Positive(t, cfg.BatchSize, "%s must be > 0", envSendBatchSize)

	var watcher *evmExecWatcher
	if cfg.ExecCheckEnabled() {
		watcher = env.newEVMExecWatcher(t, ctx)
	}

	runID := time.Now().UTC().Format(time.RFC3339Nano)
	sent := make([]sentMessage, 0, cfg.BatchSize)
	seen := make(map[[32]byte]bool, cfg.BatchSize)
	for i := range cfg.BatchSize {
		data := []byte(fmt.Sprintf("staging stellar->evm batch %s #%d", runID, i))
		s := env.sendAndConfirm(t, ctx, env.buildMessage(t, data))

		require.False(t, seen[s.MessageID], "duplicate message id %s", hexID(s.MessageID))
		seen[s.MessageID] = true
		if i > 0 {
			prev := sent[i-1].Event.SequenceNumber
			require.Greater(t, s.Event.SequenceNumber, prev, "sequence number did not increase")
		}
		sent = append(sent, s)
	}

	if watcher == nil {
		return
	}
	for _, s := range sent {
		exec := env.waitForExecution(t, ctx, watcher, s.MessageID)
		require.Equalf(t, cciptestinterfaces.ExecutionStateSuccess, cciptestinterfaces.MessageExecutionState(exec.State),
			"message %s executed with state %d, return data 0x%x", hexID(s.MessageID), exec.State, exec.ReturnData)
	}
}

// TestStagingStellarToEVMTokenTransfer sends STAGING_TOKEN_TRANSFER_AMOUNT of the
// Stellar BnM token, with a short payload, via Router.ccip_send. It first checks
// the Stellar pool (and, with execution checks enabled, the EVM pool) is linked
// to its counterpart, then that the pool burns exactly the amount from the
// sender and the fee is pulled exactly. With STAGING_EVM_RPC_URL/STAGING_EVM_OFFRAMP
// set it waits for successful execution and checks the EVM receiver was minted
// the amount scaled to the EVM token's decimals.
func TestStagingStellarToEVMTokenTransfer(t *testing.T) {
	ctx := t.Context()
	env := newStagingEnv(t, ctx)
	cfg := env.cfg
	tt := loadTokenTransferConfig(t)

	// The send invokes the token and pool; keep their entries live like the lane's.
	extendContractTTLs(t, ctx, env.deployer, env.l, tt.StellarToken, tt.StellarPool)
	stellarDecimals := checkStellarTokenPool(t, ctx, env.deployer, tt, cfg.DestChainSelector, env.onRampID, "")

	sender := cfg.Sender.Address()
	token := bnmtokenbindings.NewBnmTokenClient(env.deployer, tt.StellarToken)
	ensureStellarTokenBalance(t, ctx, token, sender, tt.Amount, env.l)

	var (
		watcher        *evmExecWatcher
		evmToken       *erc20.FactoryBurnMintERC20
		wantMinted     *big.Int
		receiverBefore *big.Int
	)
	if cfg.ExecCheckEnabled() {
		watcher = env.newEVMExecWatcher(t, ctx)
		evmDecimals := checkEVMTokenPool(t, ctx, watcher.client, tt, cfg.SourceChainSelector)
		wantMinted = scaleAmount(t, tt.Amount, stellarDecimals, evmDecimals)

		var err error
		evmToken, err = erc20.NewFactoryBurnMintERC20(tt.EVMToken, watcher.client)
		require.NoError(t, err)
		receiverBefore, err = evmToken.BalanceOf(&bind.CallOpts{Context: ctx}, cfg.EVMReceiver)
		require.NoError(t, err, "EVM receiver token balance")
	}

	nativeFee, err := cfg.isNativeFeeToken()
	require.NoError(t, err)
	feeBefore := env.feeTokenBalance(t, ctx)
	tokenBefore, err := token.Balance(ctx, sender)
	require.NoError(t, err)

	data := []byte(fmt.Sprintf("staging stellar->evm token transfer %s", time.Now().UTC().Format(time.RFC3339Nano)))
	msg := env.buildMessage(t, data)
	msg.TokenAmounts = []routerbindings.TokenAmount{{Token: tt.StellarToken, Amount: tt.Amount}}
	sent := env.sendAndConfirm(t, ctx, msg)

	ev := sent.Event
	require.Equal(t, cfg.DestChainSelector, ev.DestChainSelector)
	require.Equal(t, sender, ev.Sender)
	require.Equal(t, cfg.FeeTokenID, ev.FeeToken)
	// One receipt per CCV, plus the pool, executor and network-fee receipts. The
	// pool may require extra CCVs on top of the configured ones.
	require.GreaterOrEqual(t, len(ev.Receipts), len(cfg.CCVs)+3, "unexpected receipt count")

	tokenAfter, err := token.Balance(ctx, sender)
	require.NoError(t, err)
	burned := new(big.Int).Sub(tokenBefore, tokenAfter)
	require.Zerof(t, burned.Cmp(tt.Amount), "sender token balance dropped by %s, want %s", burned, tt.Amount)

	spent := new(big.Int).Sub(feeBefore, env.feeTokenBalance(t, ctx))
	if nativeFee {
		require.GreaterOrEqual(t, spent.Cmp(sent.Fee), 0, "spent %s < quoted fee %s", spent, sent.Fee)
	} else {
		require.Zero(t, spent.Cmp(sent.Fee), "spent %s != quoted fee %s", spent, sent.Fee)
	}

	if watcher == nil {
		t.Logf("token transfer %s sent; set %s and %s to also verify execution and the mint on the destination",
			hexID(sent.MessageID), envEVMRPCURL, envEVMOffRamp)
		return
	}

	exec := env.waitForExecution(t, ctx, watcher, sent.MessageID)
	env.l.Info().
		Str("messageID", hexID(sent.MessageID)).
		Uint64("messageNumber", exec.MessageNumber).
		Str("txHash", exec.Raw.TxHash.Hex()).
		Uint64("block", exec.Raw.BlockNumber).
		Msg("ExecutionStateChanged observed on destination")
	require.Equalf(t, cciptestinterfaces.ExecutionStateSuccess, cciptestinterfaces.MessageExecutionState(exec.State),
		"message executed with state %d, return data 0x%x", exec.State, exec.ReturnData)
	require.Equal(t, ev.SequenceNumber, exec.MessageNumber, "dest message number should match source sequence number")

	// Exact only while nothing else pays this receiver in this token meanwhile.
	receiverAfter, err := evmToken.BalanceOf(&bind.CallOpts{Context: ctx}, cfg.EVMReceiver)
	require.NoError(t, err)
	minted := new(big.Int).Sub(receiverAfter, receiverBefore)
	require.Zerof(t, minted.Cmp(wantMinted), "EVM receiver gained %s, want %s", minted, wantMinted)
}

// TestStagingStellarToEVMUnsupportedDestination checks the Router rejects a
// destination selector with no OnRamp, both when quoting and when sending, and
// that a rejected send does not move fee tokens. It spends no fee tokens.
func TestStagingStellarToEVMUnsupportedDestination(t *testing.T) {
	ctx := t.Context()
	env := newStagingEnv(t, ctx)

	const unknownSelector uint64 = 1 // not a registered chain selector
	supported, err := env.router.IsChainSupported(ctx, unknownSelector)
	require.NoError(t, err)
	require.False(t, supported)

	msg := env.buildMessage(t, []byte("staging: should be rejected"))
	_, err = env.router.GetFee(ctx, unknownSelector, msg)
	require.ErrorContains(t, err, errUnsupportedDestinationChain, "get_fee to an unsupported destination")

	// No quote exists for an unsupported destination; the legacy ABI still needs a fee argument.
	balanceBefore := env.feeTokenBalance(t, ctx)
	_, err = env.ccipSend(ctx, unknownSelector, msg, big.NewInt(0))
	require.ErrorContains(t, err, errUnsupportedDestinationChain, "ccip_send to an unsupported destination")
	env.l.Info().Err(err).Msg("ccip_send rejected as expected")

	nativeFee, err := env.cfg.isNativeFeeToken()
	require.NoError(t, err)
	if !nativeFee {
		require.Zero(t, balanceBefore.Cmp(env.feeTokenBalance(t, ctx)), "fee token balance changed after rejected send")
	}
}
