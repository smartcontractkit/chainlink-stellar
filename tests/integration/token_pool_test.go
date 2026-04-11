//go:build integration

package integration

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func TestTokenPool(t *testing.T) {
	// Many subtests each deploy stacks / use RPC; 5m is too tight on CI (shared ctx expires →
	// "context deadline exceeded" on WASM upload or get_ledger_entries).
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, networkPassphrase, friendbotURL := GetSharedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	t.Run("deploy and initialize lock-release pool", func(t *testing.T) {
		wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "pools_lock_release_pool.wasm")
		salt := deployment.GenerateDeterministicSalt(deployerAddr, "test-lock-release-pool")
		contractID, err := deployer.DeployContract(ctx, wasmPath, salt)
		if err != nil {
			t.Fatalf("Deploy LockRelease pool: %v", err)
		}
		t.Logf("Pool deployed at: %s", contractID)

		mockToken := helpers.GenerateMockContractID(t, deployerAddr, "pool-test-token")
		client := tokenpoolbindings.NewTokenPoolClient(deployer, contractID)

		if err := client.Initialize(ctx, deployerAddr, mockToken, 7); err != nil {
			t.Fatalf("Initialize pool: %v", err)
		}

		gotToken, err := client.GetToken(ctx)
		if err != nil {
			t.Fatalf("GetToken: %v", err)
		}
		if gotToken != mockToken {
			t.Fatalf("token mismatch: want %s, got %s", mockToken, gotToken)
		}

		supported, err := client.IsSupportedToken(ctx, mockToken)
		if err != nil {
			t.Fatalf("IsSupportedToken: %v", err)
		}
		if !supported {
			t.Fatal("expected token to be supported")
		}
		t.Log("Pool deployed, initialized, and token verified")
	})

	t.Run("apply chain updates", func(t *testing.T) {
		wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "pools_lock_release_pool.wasm")
		salt := deployment.GenerateDeterministicSalt(deployerAddr, "test-pool-chain-updates")
		contractID, err := deployer.DeployContract(ctx, wasmPath, salt)
		if err != nil {
			t.Fatalf("Deploy pool: %v", err)
		}

		mockToken := helpers.GenerateMockContractID(t, deployerAddr, "pool-chain-test-token")
		client := tokenpoolbindings.NewTokenPoolClient(deployer, contractID)
		if err := client.Initialize(ctx, deployerAddr, mockToken, 7); err != nil {
			t.Fatalf("Initialize pool: %v", err)
		}

		remoteChain := uint64(54321)
		remotePool := make([]byte, 20)
		remoteToken := make([]byte, 20)
		err = client.ApplyChainUpdates(ctx, []tokenpoolbindings.ChainUpdate{
			{
				RemoteChainSelector:       remoteChain,
				RemotePoolAddresses:       remotePool,
				RemoteTokenAddress:        remoteToken,
				OutboundRateLimiterConfig: tokenpoolbindings.RateLimitConfig{},
				InboundRateLimiterConfig:  tokenpoolbindings.RateLimitConfig{},
			},
		}, nil)
		if err != nil {
			t.Fatalf("ApplyChainUpdates: %v", err)
		}

		supported, err := client.IsSupportedChain(ctx, remoteChain)
		if err != nil {
			t.Fatalf("IsSupportedChain: %v", err)
		}
		if !supported {
			t.Fatal("expected remote chain to be supported after ApplyChainUpdates")
		}
		t.Logf("Chain %d supported after update", remoteChain)
	})

	// Outbound: one full stack + SAC + pool + FeeQuoter/OnRamp wire shared by nested cases (saves repeated WASM deploys).
	t.Run("outbound token pool ccip_send", func(t *testing.T) {
		const localChain = uint64(11111)
		const remoteDestChain = uint64(22222)

		stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localChain, "shared-tp-out", false)
		mockFeeToken := helpers.GenerateMockContractID(t, deployerAddr, "shared-tp-out-fee")
		sacToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, "shared-tp-out")
		stack.deployTokenPool(ctx, t, projectRoot, deployer, deployerAddr, "shared-tp-out", sacToken)

		if stack.TokenAdminRegistryID == "" {
			t.Fatal("TokenAdminRegistryID not set after deployTokenPool")
		}
		if stack.TokenPoolID == "" {
			t.Fatal("TokenPoolID not set after deployTokenPool")
		}
		pool, err := stack.TokenAdminRegistryClient.GetPool(ctx, sacToken)
		if err != nil {
			t.Fatalf("GetPool: %v", err)
		}
		if pool == nil || *pool != stack.TokenPoolID {
			t.Fatalf("pool mismatch: want %s, got %v", stack.TokenPoolID, pool)
		}

		remotePool := make([]byte, 20)
		remoteToken := make([]byte, 20)
		for i := range remotePool {
			remotePool[i] = 0x11
			remoteToken[i] = 0x22
		}
		if err := stack.TokenPoolClient.ApplyChainUpdates(ctx, []tokenpoolbindings.ChainUpdate{{
			RemoteChainSelector:       remoteDestChain,
			RemotePoolAddresses:       remotePool,
			RemoteTokenAddress:        remoteToken,
			OutboundRateLimiterConfig: tokenpoolbindings.RateLimitConfig{},
			InboundRateLimiterConfig:  tokenpoolbindings.RateLimitConfig{},
		}}, nil); err != nil {
			t.Fatalf("TokenPool ApplyChainUpdates: %v", err)
		}

		_ = deployOutboundSendWire(ctx, t, projectRoot, deployer, deployerAddr, "shared-tp-out", stack,
			localChain, remoteDestChain, mockFeeToken, []string{sacToken})

		defaultExecutor := helpers.GenerateMockContractID(t, deployerAddr, "shared-tp-out-exec")
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

		evmReceiver := make([]byte, 20)
		for i := range evmReceiver {
			evmReceiver[i] = 0x33
		}

		t.Run("happy path with lock-release pool token amount", func(t *testing.T) {
			const tokenTransferAmount = int64(1_000_000) // 0.1 at 7 decimals (same scale as ccv/chain E2E test token)

			msg := routerbindings.StellarToAnyMessage{
				Receiver:     evmReceiver,
				Data:         []byte("integration token ccip_send"),
				FeeToken:     mockFeeToken,
				ExtraArgs:    extraArgs,
				TokenAmounts: []routerbindings.TokenAmount{{Token: sacToken, Amount: tokenTransferAmount}},
			}

			requiredFee, err := stack.RouterClient.GetFee(ctx, remoteDestChain, msg)
			if err != nil {
				t.Fatalf("Router GetFee: %v", err)
			}
			if requiredFee <= 0 {
				t.Fatalf("expected positive fee for token message, got %d", requiredFee)
			}
			t.Logf("quoted fee (fee token base units): %d", requiredFee)

			msgNoTokens := msg
			msgNoTokens.TokenAmounts = nil
			msgNoTokens.Data = []byte("integration ccip_send without tokens")

			feeNoTokens, err := stack.RouterClient.GetFee(ctx, remoteDestChain, msgNoTokens)
			if err != nil {
				t.Fatalf("Router GetFee (no tokens): %v", err)
			}
			if feeNoTokens <= 0 {
				t.Fatalf("expected positive fee, got %d", feeNoTokens)
			}

			latest, err := rpcClient.GetLatestLedger(ctx)
			if err != nil {
				t.Fatalf("GetLatestLedger: %v", err)
			}
			startLedger := latest.Sequence

			messageID, err := stack.RouterClient.CcipSend(ctx, deployerAddr, remoteDestChain, msgNoTokens, feeNoTokens)
			if err != nil {
				t.Fatalf("Router CcipSend (no tokens): %v", err)
			}
			if messageID == [32]byte{} {
				t.Fatal("CcipSend returned empty message_id")
			}
			t.Logf("message_id: %x", messageID)

			const eventWait = 30 * time.Second
			sendEvt, err := stack.RouterClient.WaitForCCIPSendRequestedEvent(ctx, startLedger, eventWait,
				func(e *routerbindings.CCIPSendRequestedEvent) bool {
					if e.DestChainSelector != remoteDestChain || e.Sender != deployerAddr {
						return false
					}
					return bytes.Equal(e.MessageId[:], messageID[:])
				})
			if err != nil {
				t.Fatalf("WaitForCCIPSendRequestedEvent: %v", err)
			}
			if !bytes.Equal(sendEvt.MessageId[:], messageID[:]) {
				t.Fatalf("event message_id %x != return value %x", sendEvt.MessageId, messageID)
			}
			t.Logf("CCIPSendRequested at ledger %d tx %s", sendEvt.Ledger, sendEvt.TxHash)

			senderBefore := sacBalanceOrFatal(ctx, t, deployer, sacToken, deployerAddr)
			poolBefore := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.TokenPoolID)
			t.Logf("SAC balances before token send: sender=%d pool=%d", senderBefore, poolBefore)

			latest2, err := rpcClient.GetLatestLedger(ctx)
			if err != nil {
				t.Fatalf("GetLatestLedger (token send): %v", err)
			}
			tokenMsgID, err := stack.RouterClient.CcipSend(ctx, deployerAddr, remoteDestChain, msg, requiredFee)
			if err != nil {
				t.Fatalf("Router CcipSend (with SAC token): %v", err)
			}
			if tokenMsgID == [32]byte{} {
				t.Fatal("CcipSend (token) returned empty message_id")
			}
			t.Logf("token transfer message_id: %x", tokenMsgID)

			tokenEvt, err := stack.RouterClient.WaitForCCIPSendRequestedEvent(ctx, latest2.Sequence, eventWait,
				func(e *routerbindings.CCIPSendRequestedEvent) bool {
					if e.DestChainSelector != remoteDestChain || e.Sender != deployerAddr {
						return false
					}
					return bytes.Equal(e.MessageId[:], tokenMsgID[:])
				})
			if err != nil {
				t.Fatalf("WaitForCCIPSendRequestedEvent (token send): %v", err)
			}
			if !bytes.Equal(tokenEvt.MessageId[:], tokenMsgID[:]) {
				t.Fatalf("event message_id %x != return value %x", tokenEvt.MessageId, tokenMsgID)
			}
			t.Logf("token CCIPSendRequested at ledger %d tx %s", tokenEvt.Ledger, tokenEvt.TxHash)

			senderAfter := sacBalanceOrFatal(ctx, t, deployer, sacToken, deployerAddr)
			poolAfter := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.TokenPoolID)
			t.Logf("SAC balances after token send: sender=%d pool=%d", senderAfter, poolAfter)

			if got := senderBefore - senderAfter; got != tokenTransferAmount {
				t.Fatalf("sender SAC balance should drop by %d; before=%d after=%d (delta=%d)",
					tokenTransferAmount, senderBefore, senderAfter, got)
			}
			if got := poolAfter - poolBefore; got != tokenTransferAmount {
				t.Fatalf("pool SAC balance should increase by %d; before=%d after=%d (delta=%d)",
					tokenTransferAmount, poolBefore, poolAfter, got)
			}
		})

		// Tighten outbound bucket after happy path; amount exceeds capacity → TokenMaxCapacityExceeded.
		t.Run("token transfer fails when outbound pool rate limit exceeded", func(t *testing.T) {
			const outboundCapacity = uint64(500_000)
			const tokenTransferAmount = int64(1_000_000)

			outboundRL := tokenpoolbindings.RateLimitConfig{
				IsEnabled: true,
				Capacity: scval.U128(xdr.UInt128Parts{
					Hi: xdr.Uint64(0),
					Lo: xdr.Uint64(outboundCapacity),
				}),
				Rate: scval.U128(xdr.UInt128Parts{
					Hi: xdr.Uint64(0),
					Lo: xdr.Uint64(outboundCapacity),
				}),
			}
			if err := stack.TokenPoolClient.SetRateLimitConfig(ctx, remoteDestChain, outboundRL, tokenpoolbindings.RateLimitConfig{}, false); err != nil {
				t.Fatalf("TokenPool SetRateLimitConfig: %v", err)
			}

			msg := routerbindings.StellarToAnyMessage{
				Receiver:     evmReceiver,
				Data:         []byte("integration token ccip_send rate limit exceeded"),
				FeeToken:     mockFeeToken,
				ExtraArgs:    extraArgs,
				TokenAmounts: []routerbindings.TokenAmount{{Token: sacToken, Amount: tokenTransferAmount}},
			}

			requiredFee, err := stack.RouterClient.GetFee(ctx, remoteDestChain, msg)
			if err != nil {
				t.Fatalf("Router GetFee: %v", err)
			}
			if requiredFee <= 0 {
				t.Fatalf("expected positive fee for token message, got %d", requiredFee)
			}

			_, err = stack.RouterClient.CcipSend(ctx, deployerAddr, remoteDestChain, msg, requiredFee)
			assertHostContractErrorContainsCode(t, err, tokenpoolbindings.CCIPErrorTokenMaxCapacityExceeded)
			t.Logf("CcipSend rejected as expected (transfer amount > outbound bucket capacity): %v", err)
		})
	})

	// Inbound: one stack + SAC + pool + single ApplyChainUpdates. Subtests use distinct sequence numbers and run
	// low-liq → curse → happy so pool balances compose (curse adds liquidity; happy needs ≥ release in pool).
	t.Run("inbound token pool offramp execute", func(t *testing.T) {
		const localChain = uint64(11111)
		const releaseAmount = int64(2_000_000)
		const poolFunding = int64(500_000)

		stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localChain, "shared-tp-in", true)
		sacToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, "shared-tp-in")
		stack.deployTokenPool(ctx, t, projectRoot, deployer, deployerAddr, "shared-tp-in", sacToken)

		evmPool := bytes.Repeat([]byte{0x51}, 20)
		evmTok := bytes.Repeat([]byte{0x52}, 20)
		if err := stack.TokenPoolClient.ApplyChainUpdates(ctx, []tokenpoolbindings.ChainUpdate{{
			RemoteChainSelector:       remoteSourceChain,
			RemotePoolAddresses:       evmPool,
			RemoteTokenAddress:        evmTok,
			OutboundRateLimiterConfig: tokenpoolbindings.RateLimitConfig{},
			InboundRateLimiterConfig:  tokenpoolbindings.RateLimitConfig{},
		}}, nil); err != nil {
			t.Fatalf("TokenPool ApplyChainUpdates: %v", err)
		}

		evmSender := bytes.Repeat([]byte{0xcd}, 20)

		t.Run("rejects when pool has insufficient SAC balance", func(t *testing.T) {
			sacTransferOrFatal(ctx, t, deployer, sacToken, deployerAddr, stack.TokenPoolID, poolFunding)
			poolBal := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.TokenPoolID)
			if poolBal < releaseAmount {
				t.Logf("pool balance %d < release %d (expected for this test)", poolBal, releaseAmount)
			}

			tokenXfer, err := EncodeCcipTokenTransferV1Inbound(releaseAmount, evmPool, evmTok, sacToken, stack.ReceiverID, nil)
			if err != nil {
				t.Fatalf("EncodeCcipTokenTransferV1Inbound: %v", err)
			}
			var ccvZero [32]byte
			encoded, err := encodeCcipMessageV1(ccipV1Wire{
				SourceChainSelector: remoteSourceChain,
				DestChainSelector:   localChain,
				SequenceNumber:      1,
				ExecutionGasLimit:   0,
				CcipReceiveGasLimit:   0,
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

			err = stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{verifierBlob}, 0)
			assertHostContractErrorContainsCode(t, err, tokenpoolbindings.CCIPErrorInsufficientPoolLiquidity)
			t.Logf("inbound execute rejected for insufficient pool liquidity: %v", err)
		})

		t.Run("rejects when source chain is cursed", func(t *testing.T) {
			sacTransferOrFatal(ctx, t, deployer, sacToken, deployerAddr, stack.TokenPoolID, releaseAmount)

			tokenXfer, err := EncodeCcipTokenTransferV1Inbound(releaseAmount, evmPool, evmTok, sacToken, stack.ReceiverID, nil)
			if err != nil {
				t.Fatalf("EncodeCcipTokenTransferV1Inbound: %v", err)
			}
			var ccvZero [32]byte
			encoded, err := encodeCcipMessageV1(ccipV1Wire{
				SourceChainSelector: remoteSourceChain,
				DestChainSelector:   localChain,
				SequenceNumber:      2,
				ExecutionGasLimit:   0,
				CcipReceiveGasLimit:   0,
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

			subject := rmnSubjectForRouterDestChain(remoteSourceChain)
			if err := stack.RmnRemoteClient.Curse(ctx, [][16]byte{subject}); err != nil {
				t.Fatalf("RmnRemote Curse(source chain subject): %v", err)
			}
			t.Cleanup(func() {
				if err := stack.RmnRemoteClient.Uncurse(ctx, [][16]byte{subject}); err != nil {
					t.Logf("cleanup Uncurse: %v", err)
				}
			})

			err = stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{verifierBlob}, 0)
			assertHostContractErrorContainsCode(t, err, offrampbindings.CCIPErrorCursedByRMN)
			t.Logf("inbound execute rejected when source chain cursed (CCIPErrorCursedByRMN): %v", err)
		})

		t.Run("releases SAC from pool to receiver", func(t *testing.T) {
			sacTransferOrFatal(ctx, t, deployer, sacToken, deployerAddr, stack.TokenPoolID, releaseAmount)

			poolBefore := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.TokenPoolID)
			rcvBefore := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.ReceiverID)
			if poolBefore < releaseAmount {
				t.Fatalf("pool underfunded: %d < %d", poolBefore, releaseAmount)
			}

			tokenXfer, err := EncodeCcipTokenTransferV1Inbound(releaseAmount, evmPool, evmTok, sacToken, stack.ReceiverID, nil)
			if err != nil {
				t.Fatalf("EncodeCcipTokenTransferV1Inbound: %v", err)
			}

			var ccvZero [32]byte
			encoded, err := encodeCcipMessageV1(ccipV1Wire{
				SourceChainSelector: remoteSourceChain,
				DestChainSelector:   localChain,
				SequenceNumber:      3,
				ExecutionGasLimit:   0,
				CcipReceiveGasLimit:   0,
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
				t.Fatalf("OffRamp Execute (inbound release): %v", err)
			}

			state, err := stack.OfframpClient.GetExecutionState(ctx, msgID)
			if err != nil {
				t.Fatalf("GetExecutionState: %v", err)
			}
			if state != offrampbindings.MessageExecutionStateSuccess {
				t.Fatalf("execution state = %d, want Success (%d)", state, offrampbindings.MessageExecutionStateSuccess)
			}

			poolAfter := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.TokenPoolID)
			rcvAfter := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.ReceiverID)

			if got := poolBefore - poolAfter; got != releaseAmount {
				t.Fatalf("pool SAC should drop by %d; before=%d after=%d (delta=%d)",
					releaseAmount, poolBefore, poolAfter, got)
			}
			if got := rcvAfter - rcvBefore; got != releaseAmount {
				t.Fatalf("receiver SAC should increase by %d; before=%d after=%d (delta=%d)",
					releaseAmount, rcvBefore, rcvAfter, got)
			}
			t.Logf("inbound release_or_mint: moved %d SAC base units pool -> receiver %s", releaseAmount, stack.ReceiverID)
		})
	})
}

// sacTransferOrFatal invokes Soroban token transfer(from, to, amount) on the SAC (deployer must be `from`).
func sacTransferOrFatal(ctx context.Context, t *testing.T, deployer *deployment.Deployer, sacContract, fromStrkey, toStrkey string, amount int64) {
	t.Helper()
	if amount <= 0 {
		t.Fatal("SAC transfer amount must be positive")
	}
	args := []xdr.ScVal{
		scval.AddressToScVal(fromStrkey),
		scval.AddressToScVal(toStrkey),
		scval.I128ToScVal(amount),
	}
	_, err := deployer.InvokeContract(ctx, sacContract, "transfer", args)
	if err != nil {
		t.Fatalf("SAC transfer %s -> %s amount=%d: %v", fromStrkey, toStrkey, amount, err)
	}
}

// sacBalanceOrFatal reads an SPL/SAC balance for a holder (G… account or C… contract) via simulate-only contract call.
func sacBalanceOrFatal(ctx context.Context, t *testing.T, deployer *deployment.Deployer, sacContract, holderStrkey string) int64 {
	t.Helper()
	args := []xdr.ScVal{scval.AddressToScVal(holderStrkey)}
	res, err := deployer.SimulateContract(ctx, sacContract, "balance", args)
	if err != nil {
		t.Fatalf("SAC balance(holder=%s): %v", holderStrkey, err)
	}
	if res == nil {
		t.Fatalf("SAC balance(holder=%s): nil result", holderStrkey)
	}
	bal, err := scval.I128FromScVal(*res)
	if err != nil {
		t.Fatalf("parse SAC balance: %v", err)
	}
	return bal
}
