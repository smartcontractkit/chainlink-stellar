//go:build integration

package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	common "github.com/smartcontractkit/chainlink-stellar/ccv/common"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

const (
	// E2EUpgradeMarker event topic emitted by forward_from_router ONLY when the
	// onramp is built with the `e2e-upgrade-marker` cargo feature. The default
	// (shipped) onramp never emits it, so its presence after a send is the proof
	// that an in-place `upgrade` swapped the executable to the feature-enabled
	// Wasm and that the upgraded code path ran. Not in the generated binding
	// (the binding is generated from the feature-off Wasm), so hardcoded here.
	e2eUpgradeMarkerEventTopic        = "onramp_1_7_E2EUpgradeMarker"
	e2eUpgradeMarkerValue      uint32 = 0xE2E0_0001
	// OnRamp now upgrades via the shared `common_authorization::Upgradeable`
	// trait, whose `Upgraded` event uses the fleet-wide topic `Upgraded`
	// (previously the onramp-local `onramp_1_7_Upgraded`).
	e2eUpgradedEventTopic = "Upgraded"

	// Feature-enabled OnRamp Wasm, built by `make build-onramp-e2e-upgrade` into
	// an isolated target dir so the default onramp.wasm is left untouched.
	e2eUpgradeOnrampWasmRel = "target/e2e-upgrade/wasm32v1-none/release/onramp.wasm"
)

// markerInfo is the result of scanning one send's events for the upgrade marker.
type markerInfo struct {
	count int
	value uint32 // value of the first marker event, valid when count > 0
}

// sendAndScanMarker drives one router→OnRamp send of msg, waits for the
// CCIPMessageSent event (proving the send landed), then scans that send's
// ledger range for E2EUpgradeMarker events. Returns the sent event and the
// marker scan. The OnRamp owner here is the deployer (EOA), set by
// deployOutboundSendWire.
func sendAndScanMarker(
	t *testing.T, ctx context.Context,
	deployer *deployment.Deployer, rpcClient *rpcclient.Client,
	stack *fullStack, wire *outboundSendWire,
	deployerAddr string, remoteDestChain uint64, msg routerbindings.StellarToAnyMessage,
) (*onrampbindings.CCIPMessageSentEvent, markerInfo) {
	t.Helper()

	latest, err := rpcClient.GetLatestLedger(ctx)
	require.NoError(t, err)
	startLedger := latest.Sequence

	requiredFee, err := stack.RouterClient.GetFee(ctx, remoteDestChain, msg)
	require.NoError(t, err)
	require.Positive(t, requiredFee.Sign(), "total fee must be positive (CCV+network)")

	msgID, err := stack.RouterClient.CcipSend(ctx, deployerAddr, remoteDestChain, msg, requiredFee)
	require.NoError(t, err)
	require.NotEqual(t, [32]byte{}, msgID, "CcipSend returned empty message_id")

	sentEvt, err := wire.OnRampClient.WaitForCCIPMessageSentEvent(ctx, startLedger, 30*time.Second,
		func(e *onrampbindings.CCIPMessageSentEvent) bool {
			return e.DestChainSelector == remoteDestChain && bytes.Equal(e.MessageId[:], msgID[:])
		})
	require.NoError(t, err)

	// The marker event (if any) is published in the same invocation as
	// CCIPMessageSent, so it is indexed by the time the wait above returns.
	markerEvents, err := deployer.GetEvents(ctx, wire.OnRampID, startLedger, []string{e2eUpgradeMarkerEventTopic})
	require.NoError(t, err)

	info := markerInfo{count: len(markerEvents)}
	if info.count > 0 {
		info.value = decodeE2EUpgradeMarker(t, markerEvents[0])
	}
	return sentEvt, info
}

// decodeE2EUpgradeMarker reads the `marker` u32 from an E2EUpgradeMarker event
// (ValueXDR is a Map<Symbol,Val> with a single `marker` field).
func decodeE2EUpgradeMarker(t *testing.T, e protocolrpc.EventInfo) uint32 {
	t.Helper()
	var v xdr.ScVal
	require.NoError(t, xdr.SafeUnmarshalBase64(e.ValueXDR, &v))
	m, ok := v.GetMap()
	require.True(t, ok && m != nil, "E2EUpgradeMarker event value is not a map")
	for _, entry := range *m {
		sym, ok := entry.Key.GetSym()
		if !ok || string(sym) != "marker" {
			continue
		}
		val, err := scval.Uint32FromScVal(entry.Val)
		require.NoError(t, err, "E2EUpgradeMarker `marker` is not a u32")
		return val
	}
	t.Fatal("E2EUpgradeMarker event has no `marker` field")
	return 0
}

// requireUpgradedEvent asserts the OnRamp emitted an Upgraded event carrying
// newWasmHash, confirming the `upgrade` call ran and published.
func requireUpgradedEvent(
	t *testing.T, ctx context.Context,
	deployer *deployment.Deployer, rpcClient *rpcclient.Client,
	onrampID string, newWasmHash [32]byte,
) {
	t.Helper()
	latest, err := rpcClient.GetLatestLedger(ctx)
	require.NoError(t, err)

	upgraded, err := deployer.GetEvents(ctx, onrampID, latest.Sequence, []string{e2eUpgradedEventTopic})
	require.NoError(t, err)
	require.NotEmpty(t, upgraded, "expected an Upgraded event from the upgrade call")

	var v xdr.ScVal
	require.NoError(t, xdr.SafeUnmarshalBase64(upgraded[0].ValueXDR, &v))
	m, ok := v.GetMap()
	require.True(t, ok && m != nil, "Upgraded event value is not a map")
	for _, entry := range *m {
		sym, ok := entry.Key.GetSym()
		if !ok || string(sym) != "new_wasm_hash" {
			continue
		}
		hash, err := scval.Bytes32FromScVal(entry.Val)
		require.NoError(t, err)
		require.Equal(t, newWasmHash, hash, "Upgraded event new_wasm_hash must match the installed Wasm hash")
		return
	}
	t.Fatal("Upgraded event has no `new_wasm_hash` field")
}

// TestOnRampUpgradeSwapsExecutableObservedViaSend proves the owner-gated
// in-place upgrade end-to-end by observing a REAL behavior change through an
// actual router→OnRamp message send — not a `peek`-style code-existence probe.
//
// The OnRamp is built twice: the default (feature-off) Wasm is deployed and
// wired into a send stack; the `e2e-upgrade-marker` feature-enabled Wasm (built
// by `make build-onramp-e2e-upgrade`) emits an extra E2EUpgradeMarker event in
// forward_from_router. The test:
//  1. sends a message on the default OnRamp → asserts NO marker event
//     (the shipped code never emits it);
//  2. installs the feature-enabled Wasm and calls `upgrade` (owner = deployer
//     EOA) → asserts the Upgraded event carries the new Wasm hash;
//  3. sends the SAME message on the upgraded OnRamp → asserts the
//     CCIPMessageSent event still fires (storage/layout preserved, the upgraded
//     contract still functions) AND the E2EUpgradeMarker event now appears with
//     the sentinel value.
//
// The marker's absence→presence across an identical send is unambiguous evidence
// the executable was swapped and the upgraded forward_from_router ran.
//
// Build prerequisite:
//
//	make build build-onramp-e2e-upgrade
func TestOnRampUpgradeSwapsExecutableObservedViaSend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, networkPassphrase, friendbotURL := GetSharedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	// Feature-enabled upgrade Wasm must be pre-built.
	featureWasmPath := filepath.Join(projectRoot, e2eUpgradeOnrampWasmRel)
	if _, err := os.Stat(featureWasmPath); err != nil {
		t.Fatalf("feature-enabled onramp wasm not found at %s: run `make build-onramp-e2e-upgrade` first: %v",
			featureWasmPath, err)
	}

	const localSourceChain = uint64(11111)
	const remoteDestChain = uint64(22222)
	const saltPrefix = "upg-marker"

	stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localSourceChain, saltPrefix, false)
	feeToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, saltPrefix+"-fee")
	// Data-only wire (no token transfer); the executor is the no-execution sentinel.
	wire := deployOutboundSendWire(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix, stack,
		localSourceChain, remoteDestChain, feeToken, nil)

	noExecIssuer := sentinelContractStrkey(t, common.NoExecutionAddressRaw)

	buildExtraArgs := func() []byte {
		extraArgs, err := encodeOnrampExtraArgsV3(onrampbindings.GenericExtraArgsV3{
			Ccvs:               []string{stack.VvrID},
			CcvArgs:            [][]byte{{}},
			Executor:           noExecIssuer, // no-execution sentinel
			ExecutorArgs:       []byte{},
			GasLimit:           0,
			BlockConfirmations: 0,
			TokenReceiver:      []byte{},
			TokenArgs:          []byte{},
		})
		require.NoError(t, err)
		return extraArgs
	}

	evmReceiver := make([]byte, 20)
	for i := range evmReceiver {
		evmReceiver[i] = 0x33
	}
	msg := routerbindings.StellarToAnyMessage{
		Receiver:  evmReceiver,
		Data:      []byte("upgrade-marker integration send"),
		FeeToken:  feeToken,
		ExtraArgs: buildExtraArgs(),
	}

	// ① Baseline send on the default (feature-off) OnRamp: no marker event.
	sentEvt1, marker1 := sendAndScanMarker(t, ctx, deployer, rpcClient, stack, wire, deployerAddr, remoteDestChain, msg)
	require.NotNil(t, sentEvt1, "baseline send must produce a CCIPMessageSent event")
	require.Equal(t, 0, marker1.count, "default (feature-off) onramp must NOT emit the E2EUpgradeMarker event")

	// ② Install the feature-enabled Wasm and upgrade (owner = deployer EOA).
	newHash, err := deployer.UploadContractWASM(ctx, featureWasmPath)
	require.NoError(t, err)
	require.NoError(t, wire.OnRampClient.Upgrade(ctx, newHash), "EOA-owner upgrade call")
	requireUpgradedEvent(t, ctx, deployer, rpcClient, wire.OnRampID, newHash)

	// ③ Same send on the upgraded OnRamp: the marker event now appears, and the
	//    contract still sends (storage/layout preserved across the swap).
	sentEvt2, marker2 := sendAndScanMarker(t, ctx, deployer, rpcClient, stack, wire, deployerAddr, remoteDestChain, msg)
	require.NotNil(t, sentEvt2, "upgraded onramp must still produce a CCIPMessageSent event (storage preserved)")
	require.GreaterOrEqual(t, marker2.count, 1, "upgraded (feature-on) onramp MUST emit the E2EUpgradeMarker event")
	require.Equal(t, e2eUpgradeMarkerValue, marker2.value, "marker event must carry the sentinel value")

	// The two sends are distinct messages (sequence advances), but both must
	// land — proving the upgraded OnRamp is fully functional, not just emitting
	// a marker.
	require.NotEqual(t, sentEvt1.SequenceNumber, sentEvt2.SequenceNumber,
		"the two sends must be distinct (sequence advances across the upgrade)")
}
