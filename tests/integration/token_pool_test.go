//go:build integration

package integration

import (
	"bytes"
	"context"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/smartcontractkit/chainlink-ccv/protocol"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	commonutil "github.com/smartcontractkit/chainlink-stellar/ccv/common"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func TestTokenPool(t *testing.T) {
	// Cap total test wall time (WASM deploys, RPC, event waits). Previously 20m when every subtest
	// called deployFullStack; with two shared stacks, 10m matches headroom above other integration
	// tests (5m) without keeping an oversized ceiling. Raise if CI flakes on slow uploads/RPC.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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
		mockRouter := helpers.GenerateMockContractID(t, deployerAddr, "pool-test-router")
		mockRampRegistry := helpers.GenerateMockContractID(t, deployerAddr, "pool-test-ramp-registry")
		mockRmnProxy := helpers.GenerateMockContractID(t, deployerAddr, "pool-test-rmn-proxy")
		client := tokenpoolbindings.NewTokenPoolClient(deployer, contractID)

		if err := client.Initialize(ctx, deployerAddr, mockToken, 7, mockRouter, mockRampRegistry, mockRmnProxy); err != nil {
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
		mockRouter := helpers.GenerateMockContractID(t, deployerAddr, "pool-chain-test-router")
		mockRampRegistry := helpers.GenerateMockContractID(t, deployerAddr, "pool-chain-test-ramp-registry")
		mockRmnProxy := helpers.GenerateMockContractID(t, deployerAddr, "pool-chain-test-rmn-proxy")
		client := tokenpoolbindings.NewTokenPoolClient(deployer, contractID)
		if err := client.Initialize(ctx, deployerAddr, mockToken, 7, mockRouter, mockRampRegistry, mockRmnProxy); err != nil {
			t.Fatalf("Initialize pool: %v", err)
		}

		remoteChain := uint64(54321)
		remotePool := make([]byte, 20)
		remoteToken := make([]byte, 20)
		err = client.ApplyChainUpdates(ctx, []tokenpoolbindings.ChainUpdate{
			{
				RemoteChainSelector:       remoteChain,
				RemotePoolAddresses:       [][]byte{remotePool},
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

	// Single deployFullStack(false) for outbound: registry smoke test + router ccip_send share the same contracts.
	t.Run("outbound full stack (shared)", func(t *testing.T) {
		const destChain = uint64(11111)
		const remoteDestChain = uint64(22222)
		const outboundSalt = "token-pool-outbound"
		stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, destChain, outboundSalt, false)

		t.Run("registry maps token to pool", func(t *testing.T) {
			mockToken := helpers.GenerateMockContractID(t, deployerAddr, outboundSalt+"-mock-token")
			// Suffix avoids same deployTokenPool WASM salt as SAC pool below (lock-release-pool → ExistingValue).
			stack.deployTokenPool(ctx, t, projectRoot, deployer, deployerAddr, outboundSalt+"-mock-pool", mockToken, remoteDestChain)

			if stack.TokenAdminRegistryID == "" {
				t.Fatal("TokenAdminRegistryID not set after deployTokenPool")
			}
			if stack.TokenPoolID == "" {
				t.Fatal("TokenPoolID not set after deployTokenPool")
			}

			pool, err := stack.TokenAdminRegistryClient.GetPool(ctx, mockToken)
			if err != nil {
				t.Fatalf("GetPool: %v", err)
			}
			if pool == nil || *pool != stack.TokenPoolID {
				t.Fatalf("pool mismatch: want %s, got %v", stack.TokenPoolID, pool)
			}
			t.Log("Full stack with token pool: TokenAdminRegistry correctly maps token to pool")
		})

		t.Run("router ccip_send with lock-release pool token amount", func(t *testing.T) {
			const tokenTransferAmount = int64(1_000_000) // 0.1 INTG at 7 decimals (same scale as ccv/chain E2E test token)

			sacToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, outboundSalt+"-sac")
			feeToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, outboundSalt+"-fee")

			stack.deployTokenPool(ctx, t, projectRoot, deployer, deployerAddr, outboundSalt+"-sac-pool", sacToken, remoteDestChain)

			remotePool := make([]byte, 20)
			remoteToken := make([]byte, 20)
			for i := range remotePool {
				remotePool[i] = 0x11
				remoteToken[i] = 0x22
			}
			if err := stack.TokenPoolClient.ApplyChainUpdates(ctx, []tokenpoolbindings.ChainUpdate{{
				RemoteChainSelector:       remoteDestChain,
				RemotePoolAddresses:       [][]byte{remotePool},
				RemoteTokenAddress:        remoteToken,
				OutboundRateLimiterConfig: tokenpoolbindings.RateLimitConfig{},
				InboundRateLimiterConfig:  tokenpoolbindings.RateLimitConfig{},
			}}, nil); err != nil {
				t.Fatalf("TokenPool ApplyChainUpdates: %v", err)
			}

			wire := deployOutboundSendWire(ctx, t, projectRoot, deployer, deployerAddr, outboundSalt, stack,
				destChain, remoteDestChain, feeToken, []string{sacToken})

			defaultExecutor := stack.ExecutorID
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

			msg := routerbindings.StellarToAnyMessage{
				Receiver:     evmReceiver,
				Data:         []byte("integration token ccip_send"),
				FeeToken:     feeToken,
				ExtraArgs:    extraArgs,
				TokenAmounts: []routerbindings.TokenAmount{{Token: sacToken, Amount: big.NewInt(tokenTransferAmount)}},
			}

			requiredFee, err := stack.RouterClient.GetFee(ctx, remoteDestChain, msg)
			if err != nil {
				t.Fatalf("Router GetFee: %v", err)
			}
			if requiredFee.Sign() <= 0 {
				t.Fatalf("expected positive fee for token message, got %d", requiredFee)
			}
			t.Logf("quoted fee (fee token base units): %d", requiredFee)

			// Successful send (no tokens): Router returns a message ID and emits CCIPSendRequested.
			msgNoTokens := msg
			msgNoTokens.TokenAmounts = nil
			msgNoTokens.Data = []byte("integration ccip_send without tokens")

			feeNoTokens, err := stack.RouterClient.GetFee(ctx, remoteDestChain, msgNoTokens)
			if err != nil {
				t.Fatalf("Router GetFee (no tokens): %v", err)
			}
			if feeNoTokens.Sign() <= 0 {
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
			lockboxBefore := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.LockBoxID)
			t.Logf("SAC balances before token send: sender=%d pool=%d lockbox=%d", senderBefore, poolBefore, lockboxBefore)

			// Token transfer: deployer authorizes SAC transfer into the pool via simulation-derived auth (see deployment.Deployer).
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
			lockboxAfter := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.LockBoxID)
			t.Logf("SAC balances after token send: sender=%d pool=%d lockbox=%d", senderAfter, poolAfter, lockboxAfter)

			if got := senderBefore - senderAfter; got != tokenTransferAmount {
				t.Fatalf("sender SAC balance should drop by %d; before=%d after=%d (delta=%d)",
					tokenTransferAmount, senderBefore, senderAfter, got)
			}
			// L-4 lockbox-escrow parity: lock_or_burn moves the full amount from the
			// sender onto the pool, then deposits dest_token_amount (== amount here,
			// no fee config set) into the lockbox. The pool's own balance is fees
			// only, so its net delta is zero and the lockbox gains the transfer.
			if got := poolAfter - poolBefore; got != 0 {
				t.Fatalf("pool SAC balance should be unchanged (fees only); before=%d after=%d (delta=%d)",
					poolBefore, poolAfter, got)
			}
			if got := lockboxAfter - lockboxBefore; got != tokenTransferAmount {
				t.Fatalf("lockbox SAC balance should increase by %d; before=%d after=%d (delta=%d)",
					tokenTransferAmount, lockboxBefore, lockboxAfter, got)
			}

			// OnRamp CCIPMessageSent receipts: [CCV…, TokenPool, Executor, NetworkFee] (EVM / ccv parity).
			sentEvt, err := wire.OnRampClient.WaitForCCIPMessageSentEvent(ctx, latest2.Sequence, eventWait,
				func(e *onrampbindings.CCIPMessageSentEvent) bool {
					if e.DestChainSelector != remoteDestChain || e.Sender != deployerAddr {
						return false
					}
					return bytes.Equal(e.MessageId[:], tokenMsgID[:])
				})
			if err != nil {
				t.Fatalf("WaitForCCIPMessageSentEvent (token send): %v", err)
			}
			rcpts := sentEvt.Receipts
			const wantReceipts = 4 // 1 default CCV + token pool + executor + network fee
			if len(rcpts) != wantReceipts {
				t.Fatalf("receipts: want len %d (1 CCV + pool + executor + network), got %d", wantReceipts, len(rcpts))
			}
			if rcpts[3].Issuer != stack.RouterID {
				t.Fatalf("receipt[3] issuer want router (network fee) %s, got %s", stack.RouterID, rcpts[3].Issuer)
			}
			// Same layout verifier / chainlink-ccv expect: CCVs, then token pool, executor, network fee.
			numCCVBlobs := len(sentEvt.VerifierBlobs)
			rwbs := onrampReceiptsToReceiptWithBlobs(t, sentEvt.Receipts, sentEvt.VerifierBlobs)
			parsed, err := protocol.ParseReceiptStructure(rwbs, numCCVBlobs, 1)
			if err != nil {
				t.Fatalf("ParseReceiptStructure: %v", err)
			}
			if len(parsed.CCVReceipts) != numCCVBlobs {
				t.Fatalf("ParseReceiptStructure CCV count: want %d, got %d", numCCVBlobs, len(parsed.CCVReceipts))
			}
			vvrRaw, err := commonutil.ToUnknownAddress(stack.VvrID)
			if err != nil {
				t.Fatalf("ToUnknownAddress(VVR): %v", err)
			}
			if !parsed.CCVReceipts[0].Issuer.Equal(vvrRaw) {
				t.Fatalf("ParseReceiptStructure CCV[0] issuer want VVR %s, got %x", stack.VvrID, parsed.CCVReceipts[0].Issuer)
			}
			poolRaw, err := commonutil.ToUnknownAddress(stack.TokenPoolID)
			if err != nil {
				t.Fatalf("ToUnknownAddress(pool): %v", err)
			}
			if len(parsed.TokenReceipts) != 1 || !parsed.TokenReceipts[0].Issuer.Equal(poolRaw) {
				t.Fatalf("ParseReceiptStructure token receipt issuer want pool %s, got %+v", stack.TokenPoolID, parsed.TokenReceipts)
			}
			execRaw, err := commonutil.ToUnknownAddress(defaultExecutor)
			if err != nil {
				t.Fatalf("ToUnknownAddress(executor): %v", err)
			}
			if !parsed.ExecutorReceipt.Issuer.Equal(execRaw) {
				t.Fatalf("ParseReceiptStructure executor issuer want %s, got %x", defaultExecutor, parsed.ExecutorReceipt.Issuer)
			}
			// The token receipt's dest gas/bytes overhead comes from the pool's
			// TokenTransferFeeConfig when enabled, else the OnRamp falls back to the
			// FeeQuoter's per-token TokenTransferFeeConfig (onramp L277-282). No pool
			// fee config is applied here, so the receipt must carry the FeeQuoter
			// values configured in deployOutboundSendWire (DestGasOverhead=90_000,
			// DestBytesOverhead=32) — not zero.
			const wantDestGasLimit uint64 = 90_000
			const wantDestBytesOverhead uint32 = 32
			if parsed.TokenReceipts[0].DestGasLimit != wantDestGasLimit || parsed.TokenReceipts[0].DestBytesOverhead != wantDestBytesOverhead {
				t.Fatalf("ParseReceiptStructure token receipt dest gas/overhead want gas=%d overhead=%d (FeeQuoter fallback), got gas=%d overhead=%d",
					wantDestGasLimit, wantDestBytesOverhead,
					parsed.TokenReceipts[0].DestGasLimit, parsed.TokenReceipts[0].DestBytesOverhead)
			}
		})
	})

	// Single deployFullStack(true) for inbound: subtests share stack + SAC + pool; distinct SequenceNumber per Execute.
	t.Run("inbound full stack (shared)", func(t *testing.T) {
		const localChain = uint64(11111)
		const inboundSalt = "token-pool-inbound-shared"
		stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localChain, inboundSalt, true)
		sacToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, inboundSalt)
		stack.deployTokenPool(ctx, t, projectRoot, deployer, deployerAddr, inboundSalt, sacToken, remoteSourceChain)

		evmPool := bytes.Repeat([]byte{0x51}, 20)
		evmTok := bytes.Repeat([]byte{0x52}, 20)
		if err := stack.TokenPoolClient.ApplyChainUpdates(ctx, []tokenpoolbindings.ChainUpdate{{
			RemoteChainSelector:       remoteSourceChain,
			RemotePoolAddresses:       [][]byte{evmPool},
			RemoteTokenAddress:        evmTok,
			OutboundRateLimiterConfig: tokenpoolbindings.RateLimitConfig{},
			InboundRateLimiterConfig:  tokenpoolbindings.RateLimitConfig{},
		}}, nil); err != nil {
			t.Fatalf("TokenPool ApplyChainUpdates (inbound shared): %v", err)
		}

		// Run low-liquidity first (underfunded lockbox), then curse, then happy-path release, so lockbox balances line up.
		t.Run("offramp inbound execute rejects when pool has insufficient SAC balance", func(t *testing.T) {
			const poolFunding = int64(500_000)
			const releaseAmount = int64(2_000_000)
			const seqNo = uint64(1)

			// L-4: release_or_mint withdraws from the lockbox, so the lockbox
			// (not the pool) must hold the liquidity. Fund it under the release
			// amount so the lockbox withdraw reverts InsufficientPoolLiquidity.
			sacTransferOrFatal(ctx, t, deployer, sacToken, deployerAddr, stack.LockBoxID, poolFunding)
			lockboxBal := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.LockBoxID)
			if lockboxBal < releaseAmount {
				t.Logf("lockbox balance %d < release %d (expected for this test)", lockboxBal, releaseAmount)
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
				SequenceNumber:      seqNo,
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

			err = stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{verifierBlob}, 0)
			assertHostContractErrorContainsCode(t, err, tokenpoolbindings.CCIPErrorInsufficientPoolLiquidity)
			t.Logf("inbound execute rejected for insufficient pool liquidity: %v", err)
		})

		// Inbound unhappy: RMN curse on source chain selector must reject execute before CCV verification.
		t.Run("offramp inbound execute rejects when source chain is cursed", func(t *testing.T) {
			const releaseAmount = int64(2_000_000)
			const seqNo = uint64(2)

			sacTransferOrFatal(ctx, t, deployer, sacToken, deployerAddr, stack.TokenPoolID, releaseAmount)

			tokenXfer, err := EncodeCcipTokenTransferV1Inbound(releaseAmount, evmPool, evmTok, sacToken, stack.ReceiverID, nil)
			if err != nil {
				t.Fatalf("EncodeCcipTokenTransferV1Inbound: %v", err)
			}
			evmSender := bytes.Repeat([]byte{0xcd}, 20)
			var ccvZero [32]byte
			encoded, err := encodeCcipMessageV1(ccipV1Wire{
				SourceChainSelector: remoteSourceChain,
				DestChainSelector:   localChain,
				SequenceNumber:      seqNo,
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

			subject := rmnSubjectForRouterDestChain(remoteSourceChain)
			if err := stack.RmnRemoteClient.Curse(ctx, deployerAddr, [][16]byte{subject}); err != nil {
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

		// Inbound: OffRamp execute decodes token leg, resolves pool via TokenAdminRegistry, calls release_or_mint.
		t.Run("offramp execute releases SAC from pool to receiver (inbound)", func(t *testing.T) {
			const releaseAmount = int64(2_000_000)
			const seqNo = uint64(3)

			// L-4: release_or_mint withdraws from the lockbox, so top up the
			// lockbox (not the pool) to cover the release.
			lockboxBal := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.LockBoxID)
			if lockboxBal < releaseAmount {
				sacTransferOrFatal(ctx, t, deployer, sacToken, deployerAddr, stack.LockBoxID, releaseAmount-lockboxBal)
			}

			lockboxBefore := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.LockBoxID)
			poolBefore := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.TokenPoolID)
			rcvBefore := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.ReceiverID)
			if lockboxBefore < releaseAmount {
				t.Fatalf("lockbox underfunded: %d < %d", lockboxBefore, releaseAmount)
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
				SequenceNumber:      seqNo,
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
			lockboxAfter := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.LockBoxID)
			rcvAfter := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.ReceiverID)

			// L-4: the lockbox holds bridged liquidity; release_or_mint withdraws
			// from it to the receiver. The pool's own balance (fees only) is
			// unchanged.
			if got := poolAfter - poolBefore; got != 0 {
				t.Fatalf("pool SAC balance should be unchanged (fees only); delta=%d", got)
			}
			if got := lockboxBefore - lockboxAfter; got != releaseAmount {
				t.Fatalf("lockbox SAC should drop by %d; before=%d after=%d (delta=%d)",
					releaseAmount, lockboxBefore, lockboxAfter, got)
			}
			if got := rcvAfter - rcvBefore; got != releaseAmount {
				t.Fatalf("receiver SAC should increase by %d; before=%d after=%d (delta=%d)",
					releaseAmount, rcvBefore, rcvAfter, got)
			}
			t.Logf("inbound release_or_mint: moved %d SAC base units lockbox -> receiver %s", releaseAmount, stack.ReceiverID)
		})
	})

	t.Run("siloed pool migration", func(t *testing.T) {
		testTokenPoolSiloedMigration(t, ctx, projectRoot, deployerKP, deployer, rpcClient, networkPassphrase, friendbotURL)
	})
}

func onrampReceiptsToReceiptWithBlobs(t *testing.T, receipts []onrampbindings.Receipt, verifierBlobs [][]byte) []protocol.ReceiptWithBlob {
	t.Helper()
	out := make([]protocol.ReceiptWithBlob, 0, len(receipts))
	for i, r := range receipts {
		var blob []byte
		if i < len(verifierBlobs) {
			blob = verifierBlobs[i]
		}
		issuer, err := commonutil.ToUnknownAddress(r.Issuer)
		if err != nil {
			t.Fatalf("ToUnknownAddress(issuer %q): %v", r.Issuer, err)
		}
		out = append(out, protocol.ReceiptWithBlob{
			Issuer:            issuer,
			Blob:              protocol.ByteSlice(blob),
			ExtraArgs:         protocol.ByteSlice(r.ExtraArgs),
			DestGasLimit:      uint64(r.DestGasLimit),
			DestBytesOverhead: r.DestBytesOverhead,
			FeeTokenAmount:    new(big.Int).Set(r.FeeTokenAmount),
		})
	}
	return out
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
		scval.I128ToScVal(big.NewInt(amount)),
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
	// SAC balances are 7-decimal and bounded by the devenv mint (≤1000 tokens =
	// 1e10 base units), so they fit int64; the i128-widened binding returns
	// *big.Int. Truncation is impossible for any balance these tests can observe.
	return bal.Int64()
}
