//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	cciprecv "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ccip_receiver"
	ccvsbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func TestRouter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, _, _, _ := GetSharedTestEnv(ctx, t)

	t.Run("deploy and initialize router", func(t *testing.T) {
		t.Log("Deploying Router contract...")
		salt := deployment.GenerateDeterministicSalt(deployerKP.Address(), "router")
		wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "router.wasm")
		contractID, err := deployer.DeployContract(ctx, wasmPath, salt)
		if err != nil {
			t.Fatalf("Failed to deploy Router: %v", err)
		}
		t.Logf("Router deployed at: %s", contractID)

		mockRmnProxy := helpers.GenerateMockContractID(t, deployerKP.Address(), "rmn-proxy-router")
		client := routerbindings.NewRouterClient(deployer, contractID)

		if err := client.Initialize(ctx, deployerKP.Address(), mockRmnProxy); err != nil {
			t.Fatalf("Failed to initialize Router: %v", err)
		}

		cfg, err := client.GetConfig(ctx)
		if err != nil {
			t.Fatalf("GetConfig: %v", err)
		}
		if cfg == nil {
			t.Fatal("GetConfig returned nil")
		}
		if cfg.RmnProxy != mockRmnProxy {
			t.Fatalf("RmnProxy mismatch: want %q, got %q", mockRmnProxy, cfg.RmnProxy)
		}
		t.Log("Router initialized; config matches")
	})

	// End-to-end: OffRamp execute → internal route_message → Router → example ccip_receiver.
	// CommitteeVerifier.validate_signatures is currently a stub (always OK when signature config exists).
	t.Run("offramp execute routes via router to ccip_receiver", func(t *testing.T) {
		const (
			localDestChain       = uint64(12345)
			remoteSourceChain    = uint64(99999)
			ccipVerifierVersion0 = byte(0x49)
			ccipVerifierVersion1 = byte(0xff)
			ccipVerifierVersion2 = byte(0x34)
			ccipVerifierVersion3 = byte(0xed)
		)

		deploy := func(name, wasmFile string) string {
			t.Helper()
			salt := deployment.GenerateDeterministicSalt(deployerKP.Address(), name)
			p := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", wasmFile)
			id, err := deployer.DeployContract(ctx, p, salt)
			if err != nil {
				t.Fatalf("deploy %s: %v", name, err)
			}
			return id
		}

		rmnRemoteID := deploy("router-int-rmn-remote", "rmn_remote.wasm")
		rmnProxyID := deploy("router-int-rmn-proxy", "rmn_proxy.wasm")
		ccvID := deploy("router-int-ccv", "ccvs_committee_verifier.wasm")
		vvrID := deploy("router-int-vvr", "ccvs_versioned_verifier_resolver.wasm")
		routerID := deploy("router-int-router", "router.wasm")
		offrampID := deploy("router-int-offramp", "offramp.wasm")
		receiverID := deploy("router-int-ccip-recv", "ccip_receiver_example.wasm")

		ccvClient := ccvsbindings.NewCommitteeVerifierClient(deployer, ccvID)
		mockFeeAgg := helpers.GenerateMockContractID(t, deployerKP.Address(), "fee-agg-router-int")
		if err := initialize(ctx, t, deployer, deployerKP, rmnRemoteID, rmnProxyID, ccvClient, mockFeeAgg); err != nil {
			t.Fatal(err)
		}

		signerKey, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("crypto.GenerateKey: %v", err)
		}
		ethAddr := crypto.PubkeyToAddress(signerKey.PublicKey)
		var signerAddrPad [32]byte
		copy(signerAddrPad[12:], ethAddr.Bytes())
		if err := ccvClient.ApplySignatureConfigs(ctx, nil, []ccvsbindings.SignatureQuorumConfig{
			{SourceChainSelector: remoteSourceChain, Threshold: 1, Signers: [][32]byte{signerAddrPad}},
		}); err != nil {
			t.Fatalf("ApplySignatureConfigs: %v", err)
		}

		vvrClient := vvrbindings.NewVersionedVerifierResolverClient(deployer, vvrID)
		if err := vvrClient.Initialize(ctx, deployerKP.Address(), mockFeeAgg); err != nil {
			t.Fatalf("VVR Initialize: %v", err)
		}
		verAddr := ccvID
		if err := vvrClient.ApplyInboundImplUpdates(ctx, []vvrbindings.InboundImplementationUpdate{
			{
				Verifier: &verAddr,
				Version:  [4]byte{ccipVerifierVersion0, ccipVerifierVersion1, ccipVerifierVersion2, ccipVerifierVersion3},
			},
		}); err != nil {
			t.Fatalf("ApplyInboundImplUpdates: %v", err)
		}

		routerClient := routerbindings.NewRouterClient(deployer, routerID)
		if err := routerClient.Initialize(ctx, deployerKP.Address(), rmnProxyID); err != nil {
			t.Fatalf("Router Initialize: %v", err)
		}

		mockTokenAdminReg := helpers.GenerateMockContractID(t, deployerKP.Address(), "token-admin-router-int")
		offrampClient := offrampbindings.NewOffRampClient(deployer, offrampID)
		if err := offrampClient.Initialize(ctx, deployerKP.Address(), offrampbindings.StaticConfig{
			ChainSelector:      localDestChain,
			RmnProxy:           rmnProxyID,
			TokenAdminRegistry: mockTokenAdminReg,
		}); err != nil {
			t.Fatalf("OffRamp Initialize: %v", err)
		}

		recvClient := cciprecv.NewExampleCcipReceiverClient(deployer, receiverID)
		if err := recvClient.Initialize(ctx, deployerKP.Address(), routerID); err != nil {
			t.Fatalf("CcipReceiver Initialize: %v", err)
		}
		// EVM CCIPClientExample.validChain: receiver only accepts `ccip_receive` for allowlisted source selectors.
		if err := recvClient.EnableRemoteChain(ctx, deployerKP.Address(), remoteSourceChain, []byte{0x01}, 0); err != nil {
			t.Fatalf("CcipReceiver EnableRemoteChain (inbound source allowlist): %v", err)
		}

		if err := routerClient.AddOfframp(ctx, remoteSourceChain, offrampID); err != nil {
			t.Fatalf("AddOfframp: %v", err)
		}

		onRampWire := bytes.Repeat([]byte{0xab}, 32)
		offRampSuffix, err := contractAddressScValSuffix32(offrampID)
		if err != nil {
			t.Fatalf("offramp scval suffix: %v", err)
		}
		receiverRaw, err := strkey.Decode(strkey.VersionByteContract, receiverID)
		if err != nil {
			t.Fatalf("decode receiver contract: %v", err)
		}
		if len(receiverRaw) != 32 {
			t.Fatalf("receiver raw len %d, want 32", len(receiverRaw))
		}

		if err := offrampClient.ApplySourceChainCfgUpdates(ctx, []offrampbindings.SourceChainConfigArgs{
			{
				SourceChainSelector: remoteSourceChain,
				Router:              routerID,
				IsEnabled:           true,
				OnRamps:             [][]byte{onRampWire},
				DefaultCcvs:         []string{vvrID},
				LaneMandatedCcvs:    nil,
			},
		}); err != nil {
			t.Fatalf("ApplySourceChainCfgUpdates: %v", err)
		}

		msgData := []byte("ccip-router-integration")
		sender := bytes.Repeat([]byte{0xcd}, 20)
		var ccvHashZero [32]byte
		encoded, err := encodeCcipMessageV1(ccipV1Wire{
			SourceChainSelector: remoteSourceChain,
			DestChainSelector:   localDestChain,
			SequenceNumber:      1,
			ExecutionGasLimit:   500_000,
			CcipReceiveGasLimit: 200_000,
			Finality:            0,
			CcvExecutorHash:     ccvHashZero,
			OnRampAddress:       onRampWire,
			OffRampAddress:      offRampSuffix,
			Sender:              sender,
			Receiver:            receiverRaw,
			DestBlob:            nil,
			TokenTransfer:       nil,
			Data:                msgData,
		})
		if err != nil {
			t.Fatalf("encodeCcipMessageV1: %v", err)
		}

		msgID := keccak256MessageID(encoded)
		versionTag := [4]byte{ccipVerifierVersion0, ccipVerifierVersion1, ccipVerifierVersion2, ccipVerifierVersion3}
		var signedPayload []byte
		signedPayload = append(signedPayload, versionTag[:]...)
		signedPayload = append(signedPayload, msgID[:]...)
		signedHash := crypto.Keccak256(signedPayload)
		sig65, err := crypto.Sign(signedHash, signerKey)
		if err != nil {
			t.Fatalf("crypto.Sign: %v", err)
		}
		// Convert to EIP-2098 compact format (64 bytes)
		var compact [64]byte
		copy(compact[:32], sig65[0:32])
		copy(compact[32:], sig65[32:64])
		v := sig65[64]
		s := new(big.Int).SetBytes(compact[32:])
		halfN := new(big.Int).Rsh(crypto.S256().Params().N, 1)
		if s.Cmp(halfN) > 0 {
			t.Fatalf("crypto.Sign should produce low-S signatures")
		}
		if v == 1 {
			compact[32] |= 0x80
		}
		const perSigBytes = 64
		var verifierBlob []byte
		verifierBlob = append(verifierBlob, versionTag[:]...)
		verifierBlob = binary.BigEndian.AppendUint16(verifierBlob, perSigBytes)
		verifierBlob = append(verifierBlob, compact[:]...)
		if err := offrampClient.Execute(ctx, encoded, []string{vvrID}, [][]byte{verifierBlob}, 0); err != nil {
			t.Fatalf("OffRamp Execute: %v", err)
		}

		state, err := offrampClient.GetExecutionState(ctx, msgID)
		if err != nil {
			t.Fatalf("GetExecutionState: %v", err)
		}
		if state != offrampbindings.MessageExecutionStateSuccess {
			t.Fatalf("execution state = %d, want Success (%d)", state, offrampbindings.MessageExecutionStateSuccess)
		}

		stored, err := recvClient.LastMessageId(ctx)
		if err != nil {
			t.Fatalf("LastMessageId: %v", err)
		}
		if stored != msgID {
			t.Fatalf("receiver last_message_id mismatch: got %x want %x", stored, msgID)
		}
		t.Log("OffRamp → Router → ccip_receive succeeded; receiver persisted message id")
	})
}

// rmnSubjectForRouterDestChain matches Router::ccip_send / RMN lane subject encoding:
// 16 bytes, last 8 = dest_chain_selector big-endian, high 8 bytes zero.
func rmnSubjectForRouterDestChain(destChainSelector uint64) [16]byte {
	var s [16]byte
	binary.BigEndian.PutUint64(s[8:], destChainSelector)
	return s
}

func assertHostContractErrorContainsCode(t *testing.T, err error, code int) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Error(Contract") {
		t.Fatalf("expected Soroban host contract error, got: %v", err)
	}
	needle := fmt.Sprintf("#%d", code)
	if !strings.Contains(msg, needle) {
		t.Fatalf("expected error to contain %q, got: %v", needle, err)
	}
}

// assertGetFeeRejectsBadExtraArgs accepts either:
//   - Error(Contract, #27) — OnRamp maps failed GenericExtraArgsV3::from_xdr to InvalidExtraArgsData, or
//   - Error(Value, InvalidInput) — host rejects bogus bytes before/during decode (no contract error in string).
func assertGetFeeRejectsBadExtraArgs(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "Error(Contract") && strings.Contains(msg, fmt.Sprintf("#%d", routerbindings.CCIPErrorInvalidExtraArgsData)) {
		return
	}
	if strings.Contains(msg, "Error(Value") && strings.Contains(msg, "InvalidInput") {
		return
	}
	t.Fatalf("expected contract #%d or Value InvalidInput for invalid extra_args, got: %v",
		routerbindings.CCIPErrorInvalidExtraArgsData, err)
}

// TestRouterCcipSendUnhappyPaths covers outbound Router flows that should fail before a successful send.
func TestRouterCcipSendUnhappyPaths(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, _, _, _ := GetSharedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	const (
		localChain      = uint64(12345)
		remoteDestChain = uint64(44444)
		saltPrefix      = "router-ccip-unhappy"
	)

	stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localChain, saltPrefix, false)
	mockFeeToken := helpers.GenerateMockContractID(t, deployerAddr, saltPrefix+"-fee-token")
	_ = deployOutboundSendWire(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix, stack,
		localChain, remoteDestChain, mockFeeToken, nil)

	userExecutor := helpers.GenerateMockContractID(t, deployerAddr, saltPrefix+"-executor")
	validExtra, err := encodeOnrampExtraArgsV3(onrampbindings.GenericExtraArgsV3{
		Ccvs:               []string{stack.VvrID},
		CcvArgs:            [][]byte{{}},
		Executor:           userExecutor,
		ExecutorArgs:       nil,
		GasLimit:           0,
		BlockConfirmations: 0,
		TokenReceiver:      nil,
		TokenArgs:          nil,
	})
	if err != nil {
		t.Fatalf("encodeOnrampExtraArgsV3: %v", err)
	}

	receiver := bytes.Repeat([]byte{0x44}, 20)
	baseMsg := routerbindings.StellarToAnyMessage{
		Receiver:     receiver,
		Data:         []byte("router ccip_send unhappy path"),
		FeeToken:     mockFeeToken,
		ExtraArgs:    validExtra,
		TokenAmounts: nil,
	}

	t.Run("ccip_send rejects when destination chain is cursed", func(t *testing.T) {
		fee, err := stack.RouterClient.GetFee(ctx, remoteDestChain, baseMsg)
		if err != nil {
			t.Fatalf("GetFee before curse: %v", err)
		}
		if fee <= 0 {
			t.Fatalf("expected positive fee before curse, got %d", fee)
		}

		subject := rmnSubjectForRouterDestChain(remoteDestChain)
		if err := stack.RmnRemoteClient.Curse(ctx, [][16]byte{subject}); err != nil {
			t.Fatalf("RmnRemote Curse(dest subject): %v", err)
		}
		t.Cleanup(func() {
			if err := stack.RmnRemoteClient.Uncurse(ctx, [][16]byte{subject}); err != nil {
				t.Logf("cleanup Uncurse: %v", err)
			}
		})

		// Use simulation so the RPC error includes Error(Contract, #62); InvokeContract can
		// surface a generic "transaction failed" after submit without the code in the string.
		args := []xdr.ScVal{
			scval.AddressToScVal(deployerAddr),
			scval.Uint64ToScVal(remoteDestChain),
			scval.MustToScVal(baseMsg.ToScVal()),
			scval.I128ToScVal(fee),
		}
		_, err = deployer.SimulateContract(ctx, stack.RouterID, "ccip_send", args)
		assertHostContractErrorContainsCode(t, err, routerbindings.CCIPErrorBadRMNSignal)
		t.Logf("ccip_send simulation rejected when dest lane cursed (CCIPErrorBadRMNSignal): %v", err)
	})

	t.Run("get_fee rejects invalid extra_args XDR", func(t *testing.T) {
		badMsg := baseMsg
		badMsg.ExtraArgs = []byte{0xde, 0xad, 0xbe, 0xef}

		_, err := stack.RouterClient.GetFee(ctx, remoteDestChain, badMsg)
		assertGetFeeRejectsBadExtraArgs(t, err)
		t.Logf("GetFee rejected invalid extra_args: %v", err)
	})
}
