package staging_tests

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-ccip/chains/evm/gobindings/generated/latest/offramp"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	ccvcommon "github.com/smartcontractkit/chainlink-stellar/ccv/common"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	ccvchain "github.com/smartcontractkit/chainlink-stellar/tests/ccv/chain"
)

const (
	stellarPollInterval = 3 * time.Second
	evmPollInterval     = 15 * time.Second

	// sentEventLookbackLedgers absorbs a ledger closing between the pre-send
	// GetLatestLedger read and inclusion of the send transaction.
	sentEventLookbackLedgers uint32 = 2
)

// stagingEnv bundles the clients shared by the staging tests.
type stagingEnv struct {
	cfg      *stagingConfig
	rpc      *rpcclient.Client
	deployer *stellardeployment.Deployer
	router   *routerbindings.RouterClient
	onRampID string
	l        *zerolog.Logger
}

func newStagingEnv(t *testing.T, ctx context.Context) *stagingEnv {
	t.Helper()
	cfg := loadStagingConfig(t)

	l := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()
	rpc, deployer := newStellarClients(cfg)
	router := routerbindings.NewRouterClient(deployer, cfg.RouterID)

	tv, err := router.TypeAndVersion(ctx)
	require.NoError(t, err, "Router.type_and_version — is %s=%s a Router on this network?", envStellarRouter, cfg.RouterID)

	onRampID := cfg.OnRampID
	if onRampID == "" {
		onRampID, err = router.GetOnramp(ctx, cfg.DestChainSelector)
		require.NoError(t, err, "Router.get_onramp(%d)", cfg.DestChainSelector)
	}

	l.Info().
		Str("rpc", cfg.StellarRPCURL).
		Str("router", cfg.RouterID).
		Str("routerVersion", tv).
		Str("onRamp", onRampID).
		Str("sender", cfg.Sender.Address()).
		Str("feeToken", cfg.FeeTokenID).
		Strs("ccvs", cfg.CCVs).
		Uint64("destChainSelector", cfg.DestChainSelector).
		Str("evmReceiver", cfg.EVMReceiver.Hex()).
		Bool("execCheck", cfg.ExecCheckEnabled()).
		Msg("Staging environment")

	return &stagingEnv{cfg: cfg, rpc: rpc, deployer: deployer, router: router, onRampID: onRampID, l: &l}
}

// buildMessage builds a data-only StellarToAnyMessage addressed to the configured
// EVM receiver, encoding GenericExtraArgsV3 the way the Stellar OnRamp parses it.
func (e *stagingEnv) buildMessage(t *testing.T, data []byte) routerbindings.StellarToAnyMessage {
	t.Helper()

	executor := e.cfg.Executor
	if executor == "" {
		var err error
		executor, err = ccvcommon.ExecutorSentinelStrkey(ccvcommon.UseDefaultExecutorAddressRaw)
		require.NoError(t, err)
	}

	ccvArgs := make([][]byte, len(e.cfg.CCVs))
	for i := range ccvArgs {
		ccvArgs[i] = []byte{}
	}

	extraArgs, err := ccvcommon.EncodeExtraArgsV3(onrampbindings.GenericExtraArgsV3{
		Ccvs:     e.cfg.CCVs,
		CcvArgs:  ccvArgs,
		Executor: executor,
		GasLimit: e.cfg.EVMGasLimit,
	})
	require.NoError(t, err, "encode GenericExtraArgsV3")

	return routerbindings.StellarToAnyMessage{
		Receiver:  e.cfg.EVMReceiver.Bytes(),
		Data:      data,
		FeeToken:  e.cfg.FeeTokenID,
		ExtraArgs: extraArgs,
	}
}

// sentMessage is the result of a confirmed Stellar-side send.
type sentMessage struct {
	MessageID [32]byte
	Fee       *big.Int
	Event     *ccvchain.CCIPMessageSentEvent
}

// sendAndConfirm quotes the fee, calls Router.ccip_send and waits for the
// matching CCIPMessageSent event on the OnRamp.
func (e *stagingEnv) sendAndConfirm(t *testing.T, ctx context.Context, msg routerbindings.StellarToAnyMessage) sentMessage {
	t.Helper()

	fee, err := e.router.GetFee(ctx, e.cfg.DestChainSelector, msg)
	require.NoError(t, err, "Router.get_fee")
	e.l.Info().Str("fee", fee.String()).Msg("Fee quote")

	latest, err := e.rpc.GetLatestLedger(ctx)
	require.NoError(t, err)
	startLedger := latest.Sequence
	if startLedger > sentEventLookbackLedgers {
		startLedger -= sentEventLookbackLedgers
	}

	messageID, err := e.router.CcipSend(ctx, e.cfg.Sender.Address(), e.cfg.DestChainSelector, msg)
	require.NoError(t, err, "Router.ccip_send")
	e.l.Info().Str("messageID", hexID(messageID)).Msg("ccip_send included")

	event := e.waitForSentEvent(t, ctx, startLedger, messageID)
	e.l.Info().
		Str("messageID", hexID(messageID)).
		Uint64("seqNum", event.SequenceNumber).
		Uint32("ledger", event.Ledger).
		Str("txHash", event.TxHash).
		Int("receipts", len(event.Receipts)).
		Msg("CCIPMessageSent observed on OnRamp")

	return sentMessage{MessageID: messageID, Fee: fee, Event: event}
}

func (e *stagingEnv) waitForSentEvent(t *testing.T, ctx context.Context, startLedger uint32, messageID [32]byte) *ccvchain.CCIPMessageSentEvent {
	t.Helper()

	ctx, cancel := context.WithTimeout(ctx, e.cfg.SentEventTimeout)
	defer cancel()
	ticker := time.NewTicker(stellarPollInterval)
	defer ticker.Stop()

	var lastErr error
	for {
		events, err := e.deployer.GetEvents(ctx, e.onRampID, startLedger, []string{ccvchain.CCIPMessageSentEventTopic})
		if err != nil {
			lastErr = err
		}
		for _, raw := range events {
			ev, err := ccvchain.ParseCCIPMessageSentEvent(raw)
			if err == nil && ev.MessageId == messageID {
				return ev
			}
		}
		select {
		case <-ctx.Done():
			require.FailNowf(t, "CCIPMessageSent not observed",
				"message %s not seen on OnRamp %s from ledger %d within %s (last RPC error: %v)",
				hexID(messageID), e.onRampID, startLedger, e.cfg.SentEventTimeout, lastErr)
		case <-ticker.C:
		}
	}
}

// feeTokenBalance returns the sender's fee token balance via the token's `balance` entrypoint.
func (e *stagingEnv) feeTokenBalance(t *testing.T, ctx context.Context) *big.Int {
	t.Helper()
	res, err := e.deployer.SimulateContract(ctx, e.cfg.FeeTokenID, "balance",
		[]xdr.ScVal{scval.AddressToScVal(e.cfg.Sender.Address())})
	require.NoError(t, err, "fee token balance")
	require.NotNil(t, res, "fee token balance returned no value")
	bal, err := scval.I128FromScVal(*res)
	require.NoError(t, err)
	return bal
}

// evmExecWatcher polls the destination OffRamp for ExecutionStateChanged events.
type evmExecWatcher struct {
	client    *ethclient.Client
	offRamp   *offramp.OffRamp
	fromBlock uint64
}

// newEVMExecWatcher must be created before sending so fromBlock predates execution.
func (e *stagingEnv) newEVMExecWatcher(t *testing.T, ctx context.Context) *evmExecWatcher {
	t.Helper()
	client, err := ethclient.DialContext(ctx, e.cfg.EVMRPCURL)
	require.NoError(t, err, "dial %s", envEVMRPCURL)
	t.Cleanup(client.Close)

	off, err := offramp.NewOffRamp(e.cfg.EVMOffRamp, client)
	require.NoError(t, err)

	head, err := client.BlockNumber(ctx)
	require.NoError(t, err)
	from := uint64(0)
	if head > e.cfg.EVMLookbackBlocks {
		from = head - e.cfg.EVMLookbackBlocks
	}
	return &evmExecWatcher{client: client, offRamp: off, fromBlock: from}
}

// waitForExecution blocks until the message reaches a terminal state on the EVM OffRamp.
func (e *stagingEnv) waitForExecution(t *testing.T, ctx context.Context, w *evmExecWatcher, messageID [32]byte) *offramp.OffRampExecutionStateChanged {
	t.Helper()

	ctx, cancel := context.WithTimeout(ctx, e.cfg.EVMExecTimeout)
	defer cancel()
	ticker := time.NewTicker(evmPollInterval)
	defer ticker.Stop()

	e.l.Info().
		Str("messageID", hexID(messageID)).
		Str("offRamp", e.cfg.EVMOffRamp.Hex()).
		Uint64("fromBlock", w.fromBlock).
		Dur("timeout", e.cfg.EVMExecTimeout).
		Msg("Waiting for ExecutionStateChanged on destination")

	var lastErr error
	for {
		ev, err := w.find(ctx, e.cfg.SourceChainSelector, messageID)
		if err != nil {
			lastErr = err
		} else if ev != nil && isTerminal(ev.State) {
			return ev
		}
		select {
		case <-ctx.Done():
			require.FailNowf(t, "message not executed on destination",
				"message %s not executed on OffRamp %s within %s (last RPC error: %v)",
				hexID(messageID), e.cfg.EVMOffRamp.Hex(), e.cfg.EVMExecTimeout, lastErr)
		case <-ticker.C:
		}
	}
}

func (w *evmExecWatcher) find(ctx context.Context, sourceSelector uint64, messageID [32]byte) (*offramp.OffRampExecutionStateChanged, error) {
	it, err := w.offRamp.FilterExecutionStateChanged(
		&bind.FilterOpts{Context: ctx, Start: w.fromBlock},
		[]uint64{sourceSelector}, nil, [][32]byte{messageID},
	)
	if err != nil {
		return nil, fmt.Errorf("filter ExecutionStateChanged: %w", err)
	}
	defer it.Close()

	var last *offramp.OffRampExecutionStateChanged
	for it.Next() {
		last = it.Event
	}
	return last, it.Error()
}

func isTerminal(state uint8) bool {
	s := cciptestinterfaces.MessageExecutionState(state)
	return s == cciptestinterfaces.ExecutionStateSuccess || s == cciptestinterfaces.ExecutionStateFailure
}

func hexID(id [32]byte) string {
	return "0x" + hex.EncodeToString(id[:])
}
