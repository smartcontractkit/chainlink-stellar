//go:build integration

package integration

import (
	"bytes"
	"context"
	"math/big"
	"testing"
	"time"

	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	slrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/siloed_lock_release_pool"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
)

// testTokenPoolSiloedMigration exercises upgrading from siloed pool v1 to v2 while reusing the same
// token lock box (no liquidity migration). Invoked as a TestTokenPool subtest. Each case uses an
// isolated fullStack + salt prefix on the shared integration Stellar network.
func testTokenPoolSiloedMigration(
	t *testing.T,
	ctx context.Context,
	projectRoot string,
	deployerKP *keypair.Full,
	deployer *deployment.Deployer,
	rpcClient *rpcclient.Client,
	networkPassphrase, friendbotURL string,
) {
	t.Helper()
	deployerAddr := deployerKP.Address()

	t.Run("siloed migration outbound ccip_send", func(t *testing.T) {
		const localChain = uint64(31111)
		const remoteDestChain = uint64(32222)
		const saltPrefix = "siloed-migration-outbound"
		const transferAmount = int64(500_000)

		stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localChain, saltPrefix, false)
		sacToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, saltPrefix)
		feeToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, saltPrefix+"-fee")

		remotePool := bytes.Repeat([]byte{0x61}, 20)
		remoteToken := bytes.Repeat([]byte{0x62}, 20)
		assets := stack.deploySiloedTokenPool(ctx, t, projectRoot, deployer, deployerAddr, rpcClient, saltPrefix, sacToken, remoteDestChain, remotePool, remoteToken)

		lockBoxBeforeMigrate := sacBalanceOrFatal(ctx, t, deployer, sacToken, assets.LockBoxID)
		if lockBoxBeforeMigrate < lockBoxSeedLiquidity {
			t.Fatalf("lock box underfunded: %d", lockBoxBeforeMigrate)
		}

		oldPoolID := assets.PoolID
		migrateSiloedTokenPool(ctx, t, projectRoot, deployer, deployerAddr, stack, assets, sacToken, remoteDestChain, saltPrefix+"-pool-v2")

		lockBoxAfterMigrate := sacBalanceOrFatal(ctx, t, deployer, sacToken, assets.LockBoxID)
		if lockBoxAfterMigrate != lockBoxBeforeMigrate {
			t.Fatalf("lock box balance changed during migration: before=%d after=%d", lockBoxBeforeMigrate, lockBoxAfterMigrate)
		}

		pool, err := stack.TokenAdminRegistryClient.GetPool(ctx, sacToken)
		if err != nil {
			t.Fatalf("GetPool after migration: %v", err)
		}
		if pool == nil || *pool != assets.PoolID {
			t.Fatalf("TAR pool after migration: want %s, got %v", assets.PoolID, pool)
		}

		wire := deployOutboundSendWire(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix, stack,
			localChain, remoteDestChain, feeToken, []string{sacToken})

		defaultExecutor := helpers.GenerateMockContractID(t, deployerAddr, saltPrefix+"-executor")
		extraArgs, err := encodeOnrampExtraArgsV3(onrampbindings.GenericExtraArgsV3{
			Ccvs:               []string{stack.VvrID},
			CcvArgs:            [][]byte{{}},
			Executor:           defaultExecutor,
			ExecutorArgs:       []byte{},
			GasLimit:           0,
			BlockConfirmations: 0,
			TokenReceiver:      []byte{},
			TokenArgs:          []byte{},
		})
		if err != nil {
			t.Fatalf("encode extra args: %v", err)
		}

		evmReceiver := bytes.Repeat([]byte{0x63}, 20)
		msg := routerbindings.StellarToAnyMessage{
			Receiver:     evmReceiver,
			Data:         []byte("siloed-migration-outbound"),
			FeeToken:     feeToken,
			ExtraArgs:    extraArgs,
			TokenAmounts: []routerbindings.TokenAmount{{Token: sacToken, Amount: big.NewInt(transferAmount)}},
		}

		requiredFee, err := stack.RouterClient.GetFee(ctx, remoteDestChain, msg)
		if err != nil {
			t.Fatalf("Router GetFee: %v", err)
		}

		senderBefore := sacBalanceOrFatal(ctx, t, deployer, sacToken, deployerAddr)
		lockBoxBeforeSend := sacBalanceOrFatal(ctx, t, deployer, sacToken, assets.LockBoxID)

		latest, err := rpcClient.GetLatestLedger(ctx)
		if err != nil {
			t.Fatalf("GetLatestLedger: %v", err)
		}

		msgID, err := stack.RouterClient.CcipSend(ctx, deployerAddr, remoteDestChain, msg, requiredFee)
		if err != nil {
			t.Fatalf("Router CcipSend after migration (v2 pool): %v", err)
		}
		if msgID == [32]byte{} {
			t.Fatal("CcipSend returned empty message_id")
		}

		const eventWait = 30 * time.Second
		_, err = stack.RouterClient.WaitForCCIPSendRequestedEvent(ctx, latest.Sequence, eventWait,
			func(e *routerbindings.CCIPSendRequestedEvent) bool {
				return e.DestChainSelector == remoteDestChain && e.Sender == deployerAddr &&
					bytes.Equal(e.MessageId[:], msgID[:])
			})
		if err != nil {
			t.Fatalf("WaitForCCIPSendRequestedEvent: %v", err)
		}

		senderAfter := sacBalanceOrFatal(ctx, t, deployer, sacToken, deployerAddr)
		lockBoxAfterSend := sacBalanceOrFatal(ctx, t, deployer, sacToken, assets.LockBoxID)

		if got := senderBefore - senderAfter; got != transferAmount {
			t.Fatalf("sender balance delta: want %d, got %d", transferAmount, got)
		}
		if got := lockBoxAfterSend - lockBoxBeforeSend; got != transferAmount {
			t.Fatalf("lock box balance delta: want %d, got %d (liquidity stays in shared lock box)", transferAmount, got)
		}

		// Old pool is no longer an allowed lock box caller; TAR pointed at v1 should fail ccip_send.
		setTokenPoolOrFatal(ctx, t, stack, sacToken, oldPoolID)
		_, err = stack.RouterClient.CcipSend(ctx, deployerAddr, remoteDestChain, msg, requiredFee)
		assertHostContractErrorContainsCode(t, err, slrbindings.CCIPErrorTokenHandlingError)
		t.Logf("ccip_send correctly rejected for decommissioned pool v1: %v", err)

		_ = wire // wired OnRamp for ccip_send path
	})

	t.Run("siloed migration inbound offramp execute", func(t *testing.T) {
		const localChain = uint64(41111)
		const saltPrefix = "siloed-migration-inbound"
		const releaseAmount = int64(500_000)
		const seqSuccess = uint64(1)
		const seqOldPool = uint64(2)

		stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localChain, saltPrefix, true)
		sacToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, saltPrefix)

		evmPool := bytes.Repeat([]byte{0x71}, 20)
		evmTok := bytes.Repeat([]byte{0x72}, 20)
		assets := stack.deploySiloedTokenPool(ctx, t, projectRoot, deployer, deployerAddr, rpcClient, saltPrefix, sacToken, remoteSourceChain, evmPool, evmTok)

		lockBoxBeforeMigrate := sacBalanceOrFatal(ctx, t, deployer, sacToken, assets.LockBoxID)
		oldPoolID := assets.PoolID

		migrateSiloedTokenPool(ctx, t, projectRoot, deployer, deployerAddr, stack, assets, sacToken, remoteSourceChain, saltPrefix+"-pool-v2")

		lockBoxAfterMigrate := sacBalanceOrFatal(ctx, t, deployer, sacToken, assets.LockBoxID)
		if lockBoxAfterMigrate != lockBoxBeforeMigrate {
			t.Fatalf("lock box balance changed during migration: before=%d after=%d", lockBoxBeforeMigrate, lockBoxAfterMigrate)
		}

		lockBoxBefore := sacBalanceOrFatal(ctx, t, deployer, sacToken, assets.LockBoxID)
		rcvBefore := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.ReceiverID)
		if lockBoxBefore < releaseAmount {
			t.Fatalf("lock box underfunded for release: %d < %d", lockBoxBefore, releaseAmount)
		}

		tokenXfer, err := EncodeCcipTokenTransferV1Inbound(releaseAmount, evmPool, evmTok, sacToken, stack.ReceiverID, nil)
		if err != nil {
			t.Fatalf("EncodeCcipTokenTransferV1Inbound: %v", err)
		}
		evmSender := bytes.Repeat([]byte{0xcd}, 20)
		var ccvZero [32]byte
		encoded, err := encodeCcipMessageV1(ccipV1Wire{
			SourceChainSelector: remoteSourceChain,
			DestChainSelector:   localChain,
			SequenceNumber:      seqSuccess,
			ExecutionGasLimit:   0,
			CcipReceiveGasLimit: 0,
			Finality:            0,
			CcvExecutorHash:     ccvZero,
			OnRampAddress:       stack.OnRampWire,
			OffRampAddress:      stack.OffRampSuffix,
			Sender:              evmSender,
			Receiver:            stack.ReceiverRaw,
			DestBlob:            nil,
			TokenTransfer:       tokenXfer,
			Data:                nil,
		})
		if err != nil {
			t.Fatalf("encodeCcipMessageV1: %v", err)
		}
		msgID := keccak256MessageID(encoded)
		verifierBlob := stack.signVerifierBlob(t, msgID)

		if err := stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{verifierBlob}, 0); err != nil {
			t.Fatalf("OffRamp Execute after migration (v2 pool): %v", err)
		}
		state, err := stack.OfframpClient.GetExecutionState(ctx, msgID)
		if err != nil {
			t.Fatalf("GetExecutionState: %v", err)
		}
		if state != offrampbindings.MessageExecutionStateSuccess {
			t.Fatalf("execution state = %d, want Success", state)
		}

		lockBoxAfter := sacBalanceOrFatal(ctx, t, deployer, sacToken, assets.LockBoxID)
		rcvAfter := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.ReceiverID)
		if got := lockBoxBefore - lockBoxAfter; got != releaseAmount {
			t.Fatalf("lock box should drop by %d; before=%d after=%d", releaseAmount, lockBoxBefore, lockBoxAfter)
		}
		if got := rcvAfter - rcvBefore; got != releaseAmount {
			t.Fatalf("receiver should gain %d; before=%d after=%d", releaseAmount, rcvBefore, rcvAfter)
		}

		// Point TAR back at v1; inbound release should fail because v1 cannot withdraw from lock box.
		setTokenPoolOrFatal(ctx, t, stack, sacToken, oldPoolID)
		tokenXferOld, err := EncodeCcipTokenTransferV1Inbound(releaseAmount, evmPool, evmTok, sacToken, stack.ReceiverID, nil)
		if err != nil {
			t.Fatalf("EncodeCcipTokenTransferV1Inbound (old pool): %v", err)
		}
		encodedOld, err := encodeCcipMessageV1(ccipV1Wire{
			SourceChainSelector: remoteSourceChain,
			DestChainSelector:   localChain,
			SequenceNumber:      seqOldPool,
			ExecutionGasLimit:   0,
			CcipReceiveGasLimit: 0,
			Finality:            0,
			CcvExecutorHash:     ccvZero,
			OnRampAddress:       stack.OnRampWire,
			OffRampAddress:      stack.OffRampSuffix,
			Sender:              evmSender,
			Receiver:            stack.ReceiverRaw,
			DestBlob:            nil,
			TokenTransfer:       tokenXferOld,
			Data:                nil,
		})
		if err != nil {
			t.Fatalf("encodeCcipMessageV1 (old pool): %v", err)
		}
		msgIDOld := keccak256MessageID(encodedOld)
		verifierBlobOld := stack.signVerifierBlob(t, msgIDOld)

		err = stack.OfframpClient.Execute(ctx, encodedOld, []string{stack.VvrID}, [][]byte{verifierBlobOld}, 0)
		assertHostContractErrorContainsCode(t, err, offrampbindings.CCIPErrorCallerNotAuthorized)
		t.Logf("inbound execute correctly rejected for decommissioned pool v1: %v", err)
	})
}
