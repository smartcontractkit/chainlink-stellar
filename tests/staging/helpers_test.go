package staging_tests

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-ccip/chains/evm/gobindings/generated/latest/offramp"
	evmonramp "github.com/smartcontractkit/chainlink-ccip/chains/evm/gobindings/generated/latest/onramp"
	evmrouter "github.com/smartcontractkit/chainlink-ccip/chains/evm/gobindings/generated/v1_2_0/router"
	erc20 "github.com/smartcontractkit/chainlink-ccip/chains/evm/gobindings/generated/v1_5_1/factory_burn_mint_erc20"
	evmtokenpool "github.com/smartcontractkit/chainlink-ccip/chains/evm/gobindings/generated/v2_0_0/token_pool"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	devenvevm "github.com/smartcontractkit/chainlink-ccv/build/devenv/evm"
	bnmtokenbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/bnm_token"
	burnmintpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/burn_mint_pool"
	ccipreceiverbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ccip_receiver"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	rampregistrybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ramp_registry"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	ccvcommon "github.com/smartcontractkit/chainlink-stellar/ccv/common"
	destinationreader "github.com/smartcontractkit/chainlink-stellar/ccv/destination_reader"
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
	rpc, deployer := newStellarClients(t, cfg.StellarRPCURL, cfg.NetworkPassphrase, cfg.Sender)
	router := routerbindings.NewRouterClient(deployer, cfg.RouterID)

	tv, err := router.TypeAndVersion(ctx)
	require.NoError(t, err, "Router.type_and_version — is %s=%s a Router on this network?", envStellarRouter, cfg.RouterID)

	onRampID := cfg.OnRampID
	if onRampID == "" {
		onRampID, err = router.GetOnramp(ctx, cfg.DestChainSelector)
		require.NoError(t, err, "Router.get_onramp(%d)", cfg.DestChainSelector)
	}

	contractIDs := []string{cfg.RouterID, onRampID}
	contractIDs = append(contractIDs, cfg.CCVs...)
	if cfg.Executor != "" {
		contractIDs = append(contractIDs, cfg.Executor)
	}
	extendContractTTLs(t, ctx, deployer, &l, contractIDs...)

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
		Bool("routerLegacyFeeArg", cfg.RouterLegacyFeeArg).
		Bool("execCheck", cfg.ExecCheckEnabled()).
		Msg("Staging environment")

	// A contract receiver given no callback gas runs out of gas immediately and
	// the message fails on the destination with ReceiverError(0x).
	if cfg.ExecCheckEnabled() && cfg.EVMGasLimit == 0 {
		code, err := dialEVM(t, ctx, cfg).CodeAt(ctx, cfg.EVMReceiver, nil)
		require.NoError(t, err, "receiver code at %s", cfg.EVMReceiver.Hex())
		if len(code) > 0 {
			t.Fatalf("%s=0 but receiver %s is a contract: ccipReceive would get no gas and fail with ReceiverError(0x); set %s (e.g. %d)",
				envEVMGasLimit, cfg.EVMReceiver.Hex(), envEVMGasLimit, defaultEVMGasLimit)
		}
	}

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

	messageID, err := e.ccipSend(ctx, e.cfg.DestChainSelector, msg, fee)
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

// ccipSend calls Router.ccip_send. Routers built before bbc2d2b4 take a trailing
// fee_token_amount argument that the generated binding no longer emits, so with
// STAGING_ROUTER_LEGACY_FEE_ARG set the call is built by hand and passes fee.
func (e *stagingEnv) ccipSend(ctx context.Context, destChainSelector uint64, msg routerbindings.StellarToAnyMessage, fee *big.Int) ([32]byte, error) {
	if !e.cfg.RouterLegacyFeeArg {
		return e.router.CcipSend(ctx, e.cfg.Sender.Address(), destChainSelector, msg)
	}

	msgVal, err := msg.ToScVal()
	if err != nil {
		return [32]byte{}, fmt.Errorf("encode message: %w", err)
	}
	result, err := e.deployer.InvokeContract(ctx, e.cfg.RouterID, "ccip_send", []xdr.ScVal{
		scval.AddressToScVal(e.cfg.Sender.Address()),
		scval.Uint64ToScVal(destChainSelector),
		msgVal,
		scval.I128ToScVal(fee),
	})
	if err != nil {
		return [32]byte{}, fmt.Errorf("failed to call ccip_send (legacy fee arg): %w", err)
	}
	if result == nil {
		return [32]byte{}, fmt.Errorf("no return value from ccip_send")
	}
	return scval.Bytes32FromScVal(*result)
}

func (e *stagingEnv) waitForSentEvent(t *testing.T, ctx context.Context, startLedger uint32, messageID [32]byte) *ccvchain.CCIPMessageSentEvent {
	t.Helper()

	ctx, cancel := context.WithTimeout(ctx, e.cfg.SentEventTimeout)
	defer cancel()
	ticker := time.NewTicker(stellarPollInterval)
	defer ticker.Stop()

	var (
		lastErr     error
		scanned     int // CCIPMessageSent events seen in the scanned windows
		parseErrors int
	)
	fromLedger := startLedger
	for {
		// Bounded, advancing window: an unpaginated query from a fixed
		// startLedger only ever returns the server's first page of matches, so
		// an event beyond it would never be seen (see waitForStellarExecution).
		// The window only advances once it has been read: a failed query is
		// retried from the same ledger, or its event would be skipped for good.
		// getEvents' endLedger is exclusive, so [fromLedger, latest) leaves the
		// latest ledger for the next window to start from.
		latest, err := e.rpc.GetLatestLedger(ctx)
		if err != nil {
			lastErr = err
			e.l.Warn().Err(err).Msg("getLatestLedger failed; retrying")
		} else if uint32(latest.Sequence) > fromLedger {
			toLedger := uint32(latest.Sequence)
			events, err := destinationreader.GetEventsInRange(ctx, e.rpc, e.onRampID, fromLedger, toLedger, []string{ccvchain.CCIPMessageSentEventTopic})
			if err != nil {
				lastErr = err
				e.l.Warn().Err(err).Uint32("fromLedger", fromLedger).Uint32("toLedger", toLedger).Msg("getEvents failed; retrying the same window")
			} else {
				for _, raw := range events {
					scanned++
					ev, err := ccvchain.ParseCCIPMessageSentEvent(raw)
					if err != nil {
						parseErrors++
						lastErr = err
						e.l.Warn().Err(err).Str("txHash", raw.TransactionHash).Msg("unparseable CCIPMessageSent event")
						continue
					}
					if ev.MessageId == messageID {
						return ev
					}
				}
				fromLedger = toLedger
			}
		}
		select {
		case <-ctx.Done():
			require.FailNowf(t, "CCIPMessageSent not observed",
				"message %s not seen on OnRamp %s in ledgers [%d, %d) within %s: scanned %d CCIPMessageSent events (%d unparseable); last error: %v. "+
					"Look up the ccip_send transaction hash logged above (\"submitted stellar transaction\") in the explorer.",
				hexID(messageID), e.onRampID, startLedger, fromLedger, e.cfg.SentEventTimeout, scanned, parseErrors, lastErr)
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
	client := dialEVM(t, ctx, e.cfg)

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

// dialEVM connects to the destination RPC; the client is closed at test cleanup.
func dialEVM(t *testing.T, ctx context.Context, cfg *stagingConfig) *ethclient.Client {
	t.Helper()
	client, err := ethclient.DialContext(ctx, cfg.EVMRPCURL)
	require.NoError(t, err, "dial %s", envEVMRPCURL)
	t.Cleanup(client.Close)
	return client
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

// extendContractTTLs extends the instance and WASM code entries of every given
// contract to the network's maximum TTL in one ExtendFootprintTtl transaction
// paid by the deployer. Without it, an expired (or about-to-expire) WASM code
// entry makes the RPC node upload a fresh copy into every simulation footprint
// as it replays the contract, and a send transaction assembled on top of that
// footprint blows the transaction resource/size limit — simulations then fail
// opaquely inside the VM with Error(WasmVm, UnexpectedSize). Extending first
// restores archived entries (auto-restore) and keeps footprints compact.
// Duplicate contract ids are extended once. Extending is permissionless and a
// no-op for entries already at the maximum TTL.
func extendContractTTLs(t *testing.T, ctx context.Context, deployer *stellardeployment.Deployer, l *zerolog.Logger, contractIDs ...string) {
	t.Helper()

	seen := make(map[string]bool, len(contractIDs))
	var keys []xdr.LedgerKey
	for _, id := range contractIDs {
		if seen[id] {
			continue
		}
		seen[id] = true

		wasmHash, err := deployer.ContractWasmHash(ctx, id)
		require.NoError(t, err, "fetch WASM hash of %s", id)
		instanceKey, err := stellardeployment.ContractInstanceLedgerKey(id)
		require.NoError(t, err)
		keys = append(keys, instanceKey, stellardeployment.ContractCodeLedgerKey(wasmHash))
	}

	liveUntil, err := deployer.ExtendTTLToMax(ctx, keys)
	require.NoError(t, err, "extend contract TTLs")
	l.Info().
		Int("entries", len(keys)).
		Uints32("liveUntilLedger", liveUntil).
		Msg("Contract instance and WASM TTLs extended to the network maximum")
}

// ============================================================
// EVM → Stellar
// ============================================================

// evmToStellarEnv bundles the clients shared by the EVM → Stellar staging tests.
type evmToStellarEnv struct {
	cfg *evmToStellarConfig
	l   *zerolog.Logger

	evm      *ethclient.Client
	chainID  *big.Int
	sender   common.Address
	router   *evmrouter.Router
	onRamp   *evmonramp.OnRamp
	onRampAt common.Address

	stellarRPC    *rpcclient.Client
	deployer      *stellardeployment.Deployer
	offRampID     string
	offRampRaw    []byte
	receiver      *ccipreceiverbindings.ExampleCcipReceiverClient
	receiverBytes []byte
}

func newEVMToStellarEnv(t *testing.T, ctx context.Context) *evmToStellarEnv {
	t.Helper()
	cfg := loadEVMToStellarConfig(t)
	l := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()

	client, err := ethclient.DialContext(ctx, cfg.EVMRPCURL)
	require.NoError(t, err, "dial %s", envEVMRPCURL)
	t.Cleanup(client.Close)
	chainID, err := client.ChainID(ctx)
	require.NoError(t, err)

	router, err := evmrouter.NewRouter(cfg.EVMRouter, client)
	require.NoError(t, err)
	onRampAt, err := router.GetOnRamp(&bind.CallOpts{Context: ctx}, cfg.StellarChainSelector)
	require.NoError(t, err, "EVM Router.getOnRamp(%d)", cfg.StellarChainSelector)
	require.NotEqual(t, common.Address{}, onRampAt, "EVM Router %s has no OnRamp for Stellar chain %d", cfg.EVMRouter.Hex(), cfg.StellarChainSelector)
	onRamp, err := evmonramp.NewOnRamp(onRampAt, client)
	require.NoError(t, err)

	stellarRPC, deployer := newStellarClients(t, cfg.StellarRPCURL, cfg.NetworkPassphrase, cfg.StellarAccount)
	offRampID := cfg.OffRampID
	if offRampID == "" {
		entries, err := routerbindings.NewRouterClient(deployer, cfg.StellarRouterID).GetOfframps(ctx)
		require.NoError(t, err, "Stellar Router.get_offramps")
		for _, e := range entries {
			if e.SourceChainSelector == cfg.EVMChainSelector {
				offRampID = e.Offramp
				break
			}
		}
		require.NotEmpty(t, offRampID, "Stellar Router %s has no OffRamp for EVM chain %d", cfg.StellarRouterID, cfg.EVMChainSelector)
	}
	offRampRaw, err := strkey.Decode(strkey.VersionByteContract, offRampID)
	require.NoError(t, err)
	receiverBytes, err := strkey.Decode(strkey.VersionByteContract, cfg.ReceiverID)
	require.NoError(t, err)

	// These tests submit only EVM transactions, but they simulate Stellar calls
	// (Router.get_offramps, receiver views) and the OffRamp/executor read the
	// receiver's WASM on execution — keep those entries live.
	extendContractTTLs(t, ctx, deployer, &l, cfg.StellarRouterID, offRampID, cfg.ReceiverID)

	// The example receiver rejects messages from source chains it hasn't enabled
	// (ccip_receive → InvalidChainForClient), which would surface as an execution
	// Failure long after the send. Check it up front.
	receiver := ccipreceiverbindings.NewExampleCcipReceiverClient(deployer, cfg.ReceiverID)
	recvRouter, err := receiver.GetRouter(ctx)
	require.NoError(t, err, "receiver %s get_router — is it an ExampleCcipReceiver?", cfg.ReceiverID)
	require.Equal(t, cfg.StellarRouterID, recvRouter, "receiver is bound to a different Stellar Router")
	remote, err := receiver.GetRemoteChainConfig(ctx, cfg.EVMChainSelector)
	require.NoError(t, err)
	if len(remote.ExtraArgs) == 0 {
		t.Fatalf("receiver %s has not enabled source chain %d, so ccip_receive would fail with InvalidChainForClient; as its owner run:\n"+
			"  stellar contract invoke --id %s --source-account <owner> --network testnet -- enable_remote_chain --caller <owner> --dest_chain_selector %d --extra_args 01 --allowed_finality_config 0",
			cfg.ReceiverID, cfg.EVMChainSelector, cfg.ReceiverID, cfg.EVMChainSelector)
	}

	env := &evmToStellarEnv{
		cfg: cfg, l: &l,
		evm: client, chainID: chainID, sender: crypto.PubkeyToAddress(cfg.EVMSender.PublicKey),
		router: router, onRamp: onRamp, onRampAt: onRampAt,
		stellarRPC: stellarRPC, deployer: deployer, offRampID: offRampID, offRampRaw: offRampRaw,
		receiver: receiver, receiverBytes: receiverBytes,
	}
	l.Info().
		Str("evmRPC", cfg.EVMRPCURL).
		Str("evmRouter", cfg.EVMRouter.Hex()).
		Str("evmOnRamp", onRampAt.Hex()).
		Str("evmSender", env.sender.Hex()).
		Str("feeToken", env.feeTokenLabel()).
		Str("stellarOffRamp", offRampID).
		Str("stellarReceiver", cfg.ReceiverID).
		Uint64("stellarChainSelector", cfg.StellarChainSelector).
		Uint32("gasLimit", cfg.StellarGasLimit).
		Msg("EVM → Stellar staging environment")
	return env
}

func (e *evmToStellarEnv) nativeFee() bool { return e.cfg.EVMFeeToken == (common.Address{}) }

func (e *evmToStellarEnv) feeTokenLabel() string {
	if e.nativeFee() {
		return "native"
	}
	return e.cfg.EVMFeeToken.Hex()
}

// buildMessage builds a data-only EVM2AnyMessage to the configured Stellar
// receiver. The extra args name no CCVs or executor, so the EVM OnRamp applies
// the lane defaults: GenericExtraArgsV3 on EVM can only carry 20-byte overrides,
// while Stellar CCV/executor addresses are 32 bytes.
func (e *evmToStellarEnv) buildMessage(t *testing.T, data []byte) evmrouter.ClientEVM2AnyMessage {
	t.Helper()
	extraArgs, err := devenvevm.NewV3ExtraArgs(0, e.cfg.StellarGasLimit, "", nil, nil, nil, nil)
	require.NoError(t, err, "encode GenericExtraArgsV3")
	return evmrouter.ClientEVM2AnyMessage{
		Receiver:  e.receiverBytes,
		Data:      data,
		FeeToken:  e.cfg.EVMFeeToken,
		ExtraArgs: extraArgs,
	}
}

// evmSentMessage is the result of a confirmed EVM-side send.
type evmSentMessage struct {
	MessageID     [32]byte
	MessageNumber uint64
	Fee           *big.Int
	Event         *evmonramp.OnRampCCIPMessageSent
	Receipt       *types.Receipt
}

// sendAndConfirm quotes the fee, pays it (msg.value for native, an ERC-20
// approval otherwise), approves the Router for any transferred tokens, calls
// Router.ccipSend and parses the OnRamp's CCIPMessageSent from the receipt.
func (e *evmToStellarEnv) sendAndConfirm(t *testing.T, ctx context.Context, msg evmrouter.ClientEVM2AnyMessage) evmSentMessage {
	t.Helper()

	fee, err := e.router.GetFee(&bind.CallOpts{Context: ctx}, e.cfg.StellarChainSelector, msg)
	require.NoError(t, err, "EVM Router.getFee")
	e.l.Info().Str("fee", fee.String()).Str("feeToken", e.feeTokenLabel()).Msg("Fee quote")

	// approve sets (not adds to) the allowance, so a token that is both
	// transferred and the fee token gets one approval for the sum.
	allowances := make(map[common.Address]*big.Int)
	addAllowance := func(token common.Address, amount *big.Int) {
		if allowances[token] == nil {
			allowances[token] = new(big.Int)
		}
		allowances[token].Add(allowances[token], amount)
	}
	for _, ta := range msg.TokenAmounts {
		addAllowance(ta.Token, ta.Amount)
	}
	if !e.nativeFee() {
		addAllowance(e.cfg.EVMFeeToken, fee)
	}
	for addr, amount := range allowances {
		token, err := erc20.NewFactoryBurnMintERC20(addr, e.evm)
		require.NoError(t, err)
		tx, err := token.Approve(e.transactOpts(t, ctx), e.cfg.EVMRouter, amount)
		require.NoError(t, err, "approve %s", addr.Hex())
		e.waitMined(t, ctx, tx, "approve")
	}

	opts := e.transactOpts(t, ctx)
	if e.nativeFee() {
		opts.Value = fee
	}
	tx, err := e.router.CcipSend(opts, e.cfg.StellarChainSelector, msg)
	require.NoError(t, err, "EVM Router.ccipSend")
	receipt := e.waitMined(t, ctx, tx, "ccipSend")

	var event *evmonramp.OnRampCCIPMessageSent
	for _, lg := range receipt.Logs {
		if lg.Address != e.onRampAt {
			continue
		}
		if ev, err := e.onRamp.ParseCCIPMessageSent(*lg); err == nil {
			event = ev
			break
		}
	}
	require.NotNil(t, event, "no CCIPMessageSent from OnRamp %s in tx %s", e.onRampAt.Hex(), tx.Hash().Hex())
	require.GreaterOrEqual(t, len(event.EncodedMessage), 25, "encoded message too short")
	messageNumber := binary.BigEndian.Uint64(event.EncodedMessage[17:25])

	e.l.Info().
		Str("messageID", hexID(event.MessageId)).
		Uint64("messageNumber", messageNumber).
		Str("txHash", tx.Hash().Hex()).
		Uint64("block", receipt.BlockNumber.Uint64()).
		Int("receipts", len(event.Receipts)).
		Msg("CCIPMessageSent observed on EVM OnRamp")

	return evmSentMessage{MessageID: event.MessageId, MessageNumber: messageNumber, Fee: fee, Event: event, Receipt: receipt}
}

func (e *evmToStellarEnv) transactOpts(t *testing.T, ctx context.Context) *bind.TransactOpts {
	t.Helper()
	opts, err := bind.NewKeyedTransactorWithChainID(e.cfg.EVMSender, e.chainID)
	require.NoError(t, err)
	opts.Context = ctx
	return opts
}

func (e *evmToStellarEnv) waitMined(t *testing.T, ctx context.Context, tx *types.Transaction, what string) *types.Receipt {
	t.Helper()
	e.l.Info().Str("tx", what).Str("txHash", tx.Hash().Hex()).Msg("EVM transaction submitted")
	ctx, cancel := context.WithTimeout(ctx, e.cfg.EVMSentTimeout)
	defer cancel()
	receipt, err := bind.WaitMined(ctx, e.evm, tx)
	require.NoError(t, err, "%s tx %s not mined within %s", what, tx.Hash().Hex(), e.cfg.EVMSentTimeout)
	require.Equal(t, types.ReceiptStatusSuccessful, receipt.Status, "%s tx %s reverted", what, tx.Hash().Hex())
	return receipt
}

// latestStellarLedger is the start ledger for an execution-event scan; read it
// before sending so the scan cannot miss a fast execution.
func (e *evmToStellarEnv) latestStellarLedger(t *testing.T, ctx context.Context) uint32 {
	t.Helper()
	latest, err := e.stellarRPC.GetLatestLedger(ctx)
	require.NoError(t, err)
	return latest.Sequence
}

// waitForStellarExecution polls the Stellar OffRamp for a terminal
// ExecutionStateChanged (Success or Failure) for messageID.
func (e *evmToStellarEnv) waitForStellarExecution(t *testing.T, ctx context.Context, startLedger uint32, messageID [32]byte) *destinationreader.ExecutionStateChangedEvent {
	t.Helper()

	ctx, cancel := context.WithTimeout(ctx, e.cfg.ExecTimeout)
	defer cancel()
	ticker := time.NewTicker(stellarPollInterval)
	defer ticker.Stop()

	e.l.Info().
		Str("messageID", hexID(messageID)).
		Str("offRamp", e.offRampID).
		Uint32("fromLedger", startLedger).
		Dur("timeout", e.cfg.ExecTimeout).
		Msg("Waiting for ExecutionStateChanged on Stellar")

	var lastErr error
	fromLedger := startLedger
	for {
		// Scan a bounded [fromLedger, latest] window that advances every poll.
		// An unbounded unpaginated query (fixed startLedger, no end) always
		// returns the server's first page of matching events; on this shared
		// OffRamp other lanes' executions fill that page and an event beyond it
		// would never be reached — the wait hangs even though the state
		// transition already landed on chain.
		latest, err := e.stellarRPC.GetLatestLedger(ctx)
		if err != nil {
			lastErr = err
		} else if uint32(latest.Sequence) > fromLedger {
			// Advance only past a window that was read, and resume at the
			// excluded end ledger; see waitForSentEvent.
			events, err := destinationreader.GetEventsInRange(ctx, e.stellarRPC, e.offRampID, fromLedger, uint32(latest.Sequence), []string{destinationreader.ExecutionStateChangedEventTopic})
			if err != nil {
				lastErr = err
				e.l.Warn().Err(err).Uint32("fromLedger", fromLedger).Msg("getEvents failed; retrying the same window")
			} else {
				for _, raw := range events {
					ev, err := destinationreader.ParseExecutionStateChangedEvent(raw)
					if err != nil || ev.MessageId != messageID {
						continue
					}
					if ev.State == offrampbindings.MessageExecutionStateSuccess || ev.State == offrampbindings.MessageExecutionStateFailure {
						return ev
					}
				}
				fromLedger = uint32(latest.Sequence)
			}
		}
		select {
		case <-ctx.Done():
			require.FailNowf(t, "message not executed on Stellar",
				"message %s not executed on OffRamp %s within %s (last RPC error: %v)",
				hexID(messageID), e.offRampID, e.cfg.ExecTimeout, lastErr)
		case <-ticker.C:
		}
	}
}

// messageV1Addresses returns the onRamp and offRamp fields of a MessageV1
// encoding (see MessageV1Codec._decodeMessageV1: 69-byte header, then 1-byte
// length-prefixed onRamp and offRamp).
func messageV1Addresses(enc []byte) (onRamp, offRamp []byte, err error) {
	const header = 69
	if len(enc) <= header {
		return nil, nil, fmt.Errorf("encoded message too short: %d bytes", len(enc))
	}
	o := header
	field := func() ([]byte, error) {
		if o >= len(enc) {
			return nil, fmt.Errorf("truncated at offset %d", o)
		}
		n := int(enc[o])
		if o+1+n > len(enc) {
			return nil, fmt.Errorf("field at offset %d overruns message", o)
		}
		b := enc[o+1 : o+1+n]
		o += 1 + n
		return b, nil
	}
	if onRamp, err = field(); err != nil {
		return nil, nil, err
	}
	if offRamp, err = field(); err != nil {
		return nil, nil, err
	}
	return onRamp, offRamp, nil
}

// revertSelector returns the 4-byte selector of a custom Solidity error.
func revertSelector(signature string) string {
	return hex.EncodeToString(crypto.Keccak256([]byte(signature))[:4])
}

// revertData extracts the hex revert data from an eth_call / gas-estimation error.
func revertData(err error) string {
	var de rpc.DataError
	if errors.As(err, &de) {
		if s, ok := de.ErrorData().(string); ok {
			return strings.TrimPrefix(s, "0x")
		}
	}
	return ""
}

// ============================================================
// Token transfers
// ============================================================

// dripABI is the permissionless faucet of BurnMintERC20WithDrip, which the
// chainlink-ccip gobindings do not cover.
const dripABI = `[{"type":"function","name":"drip","inputs":[{"name":"to","type":"address"}],"outputs":[],"stateMutability":"nonpayable"}]`

// maxDrips bounds faucet top-ups so a misconfigured amount fails fast instead
// of looping. One drip is 0.1 BnM on Stellar and 1 token on EVM.
const maxDrips = 10

// checkStellarTokenPool checks the Stellar pool serves the configured token and
// is linked to the EVM token and pool for remoteSelector, that its ramp registry
// accepts the given OnRamp and/or OffRamp (skipped when empty), and returns the
// token's decimals. EVM addresses may be stored raw (20 bytes) or ABI-padded
// (32 bytes); the last 20 bytes are compared.
func checkStellarTokenPool(t *testing.T, ctx context.Context, deployer *stellardeployment.Deployer, tt *tokenTransferConfig, remoteSelector uint64, onRamp, offRamp string) uint32 {
	t.Helper()
	pool := burnmintpoolbindings.NewBurnMintPoolClient(deployer, tt.StellarPool)

	token, err := pool.GetToken(ctx)
	require.NoError(t, err, "Stellar pool %s get_token — is %s a token pool?", tt.StellarPool, envStellarTokenPool)
	require.Equal(t, tt.StellarToken, token, "Stellar pool %s serves a different token", tt.StellarPool)

	supported, err := pool.IsSupportedChain(ctx, remoteSelector)
	require.NoError(t, err)
	require.True(t, supported, "Stellar pool %s does not support chain %d", tt.StellarPool, remoteSelector)

	remoteToken, err := pool.GetRemoteToken(ctx, remoteSelector)
	require.NoError(t, err, "Stellar pool get_remote_token(%d)", remoteSelector)
	require.Equalf(t, tt.EVMToken, common.BytesToAddress(remoteToken),
		"Stellar pool's remote token 0x%x != %s", remoteToken, tt.EVMToken.Hex())

	remotePools, err := pool.GetRemotePools(ctx, remoteSelector)
	require.NoError(t, err, "Stellar pool get_remote_pools(%d)", remoteSelector)
	require.Truef(t, slices.ContainsFunc(remotePools, func(p []byte) bool { return common.BytesToAddress(p) == tt.EVMPool }),
		"Stellar pool's remote pools %x do not include %s", remotePools, tt.EVMPool.Hex())

	// The pool authorizes its caller against its own ramp registry, not the
	// Router: lock_or_burn needs the registry's OnRamp for remoteSelector to be
	// the lane's, release_or_mint needs the lane's OffRamp registered. A miss
	// surfaces as Error(Contract, #63) / CallerNotAuthorized deep in ccip_send.
	registryID, err := pool.GetRampRegistry(ctx)
	require.NoError(t, err, "Stellar pool get_ramp_registry")
	require.NotNil(t, registryID, "Stellar pool %s has no ramp registry", tt.StellarPool)
	registry := rampregistrybindings.NewRampRegistryClient(deployer, *registryID)
	if onRamp != "" {
		got, err := registry.GetOnramp(ctx, remoteSelector)
		require.NoErrorf(t, err, "Stellar pool's ramp registry %s has no OnRamp for chain %d; its owner must apply_onramp_updates with %s",
			*registryID, remoteSelector, onRamp)
		require.Equalf(t, onRamp, got, "Stellar pool's ramp registry %s maps chain %d to a different OnRamp", *registryID, remoteSelector)
	}
	if offRamp != "" {
		ok, err := registry.IsOfframp(ctx, remoteSelector, offRamp)
		require.NoError(t, err, "ramp registry is_offramp")
		require.Truef(t, ok, "Stellar pool's ramp registry %s does not register OffRamp %s for chain %d; its owner must apply_offramp_updates",
			*registryID, offRamp, remoteSelector)
	}

	decimals, err := pool.GetTokenDecimals(ctx)
	require.NoError(t, err)
	return decimals
}

// checkEVMTokenPool checks the EVM pool serves the configured token and is
// linked to the Stellar token and pool (raw 32-byte contract ids) for
// remoteSelector, and returns the token's decimals.
func checkEVMTokenPool(t *testing.T, ctx context.Context, backend bind.ContractBackend, tt *tokenTransferConfig, remoteSelector uint64) uint8 {
	t.Helper()
	pool, err := evmtokenpool.NewTokenPool(tt.EVMPool, backend)
	require.NoError(t, err)
	opts := &bind.CallOpts{Context: ctx}

	token, err := pool.GetToken(opts)
	require.NoError(t, err, "EVM pool %s getToken — is %s a token pool?", tt.EVMPool.Hex(), envEVMTokenPool)
	require.Equal(t, tt.EVMToken, token, "EVM pool %s serves a different token", tt.EVMPool.Hex())

	supported, err := pool.IsSupportedChain(opts, remoteSelector)
	require.NoError(t, err)
	require.True(t, supported, "EVM pool %s does not support chain %d", tt.EVMPool.Hex(), remoteSelector)

	stellarToken, err := strkey.Decode(strkey.VersionByteContract, tt.StellarToken)
	require.NoError(t, err)
	remoteToken, err := pool.GetRemoteToken(opts, remoteSelector)
	require.NoError(t, err, "EVM pool getRemoteToken(%d)", remoteSelector)
	require.Truef(t, bytes.Equal(stellarToken, remoteToken),
		"EVM pool's remote token 0x%x != %s (0x%x)", remoteToken, tt.StellarToken, stellarToken)

	stellarPool, err := strkey.Decode(strkey.VersionByteContract, tt.StellarPool)
	require.NoError(t, err)
	isRemote, err := pool.IsRemotePool(opts, remoteSelector, stellarPool)
	require.NoError(t, err, "EVM pool isRemotePool(%d)", remoteSelector)
	require.True(t, isRemote, "EVM pool %s does not list %s as a remote pool", tt.EVMPool.Hex(), tt.StellarPool)

	decimals, err := pool.GetTokenDecimals(opts)
	require.NoError(t, err)
	return decimals
}

// scaleAmount converts a Stellar-token amount to EVM-token units. The pools
// convert with integer division, so only up-scaling (EVM decimals >= Stellar
// decimals) round-trips exactly.
func scaleAmount(t *testing.T, stellarAmount *big.Int, stellarDecimals uint32, evmDecimals uint8) *big.Int {
	t.Helper()
	require.GreaterOrEqual(t, uint32(evmDecimals), stellarDecimals,
		"EVM token has fewer decimals (%d) than the Stellar token (%d)", evmDecimals, stellarDecimals)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(uint32(evmDecimals)-stellarDecimals)), nil)
	return new(big.Int).Mul(stellarAmount, scale)
}

// ensureStellarTokenBalance tops the account up from the BnM token's
// permissionless drip until it holds at least need.
func ensureStellarTokenBalance(t *testing.T, ctx context.Context, token *bnmtokenbindings.BnmTokenClient, account string, need *big.Int, l *zerolog.Logger) {
	t.Helper()
	for range maxDrips {
		bal, err := token.Balance(ctx, account)
		require.NoError(t, err, "Stellar token balance")
		if bal.Cmp(need) >= 0 {
			return
		}
		l.Info().Str("balance", bal.String()).Str("need", need.String()).Msg("Dripping Stellar BnM token to sender")
		require.NoError(t, token.Drip(ctx, account), "Stellar token drip")
	}
	bal, err := token.Balance(ctx, account)
	require.NoError(t, err)
	require.GreaterOrEqualf(t, bal.Cmp(need), 0, "Stellar sender holds %s < %s after %d drips; lower %s",
		bal, need, maxDrips, envTokenTransferAmount)
}

// ensureEVMTokenBalance tops the sender up from the token's permissionless
// drip until it holds at least need, failing with funding instructions when the
// token has no working drip.
func (e *evmToStellarEnv) ensureEVMTokenBalance(t *testing.T, ctx context.Context, tokenAddr common.Address, need *big.Int) {
	t.Helper()
	token, err := erc20.NewFactoryBurnMintERC20(tokenAddr, e.evm)
	require.NoError(t, err)
	parsed, err := abi.JSON(strings.NewReader(dripABI))
	require.NoError(t, err)
	faucet := bind.NewBoundContract(tokenAddr, parsed, e.evm, e.evm, e.evm)

	for range maxDrips {
		bal, err := token.BalanceOf(&bind.CallOpts{Context: ctx}, e.sender)
		require.NoError(t, err, "EVM token balance")
		if bal.Cmp(need) >= 0 {
			return
		}
		// Dry-run first: a token deployed without the faucet (or a plain
		// BurnMintERC20) reverts here, and the sender must be funded out of band.
		if err := faucet.Call(&bind.CallOpts{Context: ctx, From: e.sender}, nil, "drip", e.sender); err != nil {
			require.FailNowf(t, "EVM sender cannot be funded from drip",
				"sender %s holds %s < %s of %s and drip(address) reverts (%v). The token may not implement drip; "+
					"fund the sender out of band, e.g. have an account with MINTER_ROLE mint to it, or run "+
					"TestStagingStellarToEVMTokenTransfer with %s set to the sender.",
				e.sender.Hex(), bal, need, tokenAddr.Hex(), err, envEVMReceiver)
		}
		e.l.Info().Str("balance", bal.String()).Str("need", need.String()).Msg("Dripping EVM token to sender")
		tx, err := faucet.Transact(e.transactOpts(t, ctx), "drip", e.sender)
		require.NoError(t, err, "EVM token drip")
		e.waitMined(t, ctx, tx, "drip")
	}
	bal, err := token.BalanceOf(&bind.CallOpts{Context: ctx}, e.sender)
	require.NoError(t, err)
	require.GreaterOrEqualf(t, bal.Cmp(need), 0, "EVM sender holds %s < %s after %d drips; lower %s",
		bal, need, maxDrips, envTokenTransferAmount)
}
