//go:build integration

package integration

import (
	"bytes"
	"context"
	"math/big"
	"testing"
	"time"

	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
)

// TestOnRampFeeDistribution exercises the H-3 send-time fee distribution and the
// INV-FEE-14 network-only residual end-to-end through the real Router → OnRamp path
// (the unit tests mock the DON; this runs the actual ccip_send + on-ledger transfers).
//
// Core invariant under test (config-independent, so robust to pricing tweaks): the
// fee token debited from the sender equals the sum of the per-recipient fee-token
// deltas plus the OnRamp residual — i.e. every fee cent is accounted for, the CCV and
// executor fees are transferred OUT at send time (H-3), and only the network fee is
// LEFT on the OnRamp for the permissionless withdraw_fee_tokens sweep (INV-FEE-14).
//
// Harness note: deployOutboundSendWire wires the OnRamp's fee_aggregator to a MOCK
// contract ID (no contract instance deployed there). The sweep subtest transfers the
// residual to that address. If that transfer reverts (a mock address cannot hold a
// classic-asset SAC balance), WithdrawFeeTokens errors and the sweep subtest fails —
// which is a real signal that the e2e sweep path needs a real fee-aggregator contract
// wired into the OnRamp DynamicConfig. The distribution/conservation subtests do not
// depend on the mock and are the primary assertion.
func TestOnRampFeeDistribution(t *testing.T) {
	// Two shared sends (data-only + token) over one stack + one outbound wire, plus a
	// sweep. 10m matches the other full-stack integration tests (WASM deploys + RPC).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, networkPassphrase, friendbotURL := GetSharedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	const localSourceChain = uint64(11111)
	const remoteDestChain = uint64(22222)
	const saltPrefix = "fee-dist"

	// offrampUsesDeployedTokenAdminRegistry=false: the token-send subtest deploys its
	// own registry via stack.deployTokenPool (mirrors TestTokenPool's outbound stack).
	stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localSourceChain, saltPrefix, false)

	// Fee token (a real 7-dec SAC) + a bridged token SAC for the token-send subtest.
	feeToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, saltPrefix+"-fee")
	sacToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, saltPrefix+"-bridge")

	// One wire priced for both sends: the fee token is always priced; sacToken is
	// registered as a transferable token so the FeeQuoter prices it + applies its
	// TokenTransferFeeConfig (the pool-fee slice) on the token send.
	wire := deployOutboundSendWire(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix, stack,
		localSourceChain, remoteDestChain, feeToken, []string{sacToken})

	// Pool + lockbox for the bridged token, wired to the remote dest chain. The pool
	// receives the pool-fee slice at send time (H-3) — asserted in the token subtest.
	stack.deployTokenPool(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix+"-bridge-pool", sacToken, remoteDestChain)
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

	executorID := stack.ExecutorID
	vvrID := stack.VvrID
	routerID := stack.RouterID
	onrampID := wire.OnRampID

	defaultExecutor := executorID
	buildExtraArgs := func() []byte {
		extraArgs, err := encodeOnrampExtraArgsV3(onrampbindings.GenericExtraArgsV3{
			Ccvs:               []string{vvrID},
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
		return extraArgs
	}

	evmReceiver := make([]byte, 20)
	for i := range evmReceiver {
		evmReceiver[i] = 0x33
	}

	// feeDelta reads a recipient's fee-token balance delta over a send. Contracts
	// hold classic-asset SAC balances in this harness (the lock-release pool test
	// transfers SAC to the pool/lockbox and reads deltas the same way).
	feeBalance := func(holder string) int64 {
		return sacBalanceOrFatal(ctx, t, deployer, feeToken, holder)
	}

	// ------------------------------------------------------------------
	// 1. Data-only send: CCV + executor fees distributed, network residual
	//    left on the OnRamp. Conservation: debit == CCV + executor + residual.
	// ------------------------------------------------------------------
	t.Run("data-only send distributes CCV and executor fees, leaves network residual on OnRamp", func(t *testing.T) {
		msg := routerbindings.StellarToAnyMessage{
			Receiver:  evmReceiver,
			Data:      []byte("fee-distribution data-only send"),
			FeeToken:  feeToken,
			ExtraArgs: buildExtraArgs(),
			// TokenAmounts nil ⇒ no pool receipt; receipts = [CCV, executor, network].
		}

		requiredFee, err := stack.RouterClient.GetFee(ctx, remoteDestChain, msg)
		if err != nil {
			t.Fatalf("Router GetFee: %v", err)
		}
		if requiredFee.Sign() <= 0 {
			t.Fatalf("expected positive fee, got %s", requiredFee.String())
		}

		senderBefore := feeBalance(deployerAddr)
		vvrBefore := feeBalance(vvrID)
		execBefore := feeBalance(executorID)
		onrampBefore := feeBalance(onrampID)

		latest, err := rpcClient.GetLatestLedger(ctx)
		if err != nil {
			t.Fatalf("GetLatestLedger: %v", err)
		}
		startLedger := latest.Sequence

		msgID, err := stack.RouterClient.CcipSend(ctx, deployerAddr, remoteDestChain, msg, requiredFee)
		if err != nil {
			t.Fatalf("Router CcipSend (data-only): %v", err)
		}
		if msgID == ([32]byte{}) {
			t.Fatal("CcipSend returned empty message_id")
		}

		sentEvt, err := wire.OnRampClient.WaitForCCIPMessageSentEvent(ctx, startLedger, 30*time.Second,
			func(e *onrampbindings.CCIPMessageSentEvent) bool {
				return e.DestChainSelector == remoteDestChain && bytes.Equal(e.MessageId[:], msgID[:])
			})
		if err != nil {
			t.Fatalf("WaitForCCIPMessageSentEvent: %v", err)
		}

		// Receipt layout [CCV, executor, network] for a data-only send (no pool receipt).
		rcpts := sentEvt.Receipts
		const wantReceipts = 3
		if len(rcpts) != wantReceipts {
			t.Fatalf("receipts: want %d (CCV + executor + network), got %d", wantReceipts, len(rcpts))
		}
		if rcpts[0].Issuer != vvrID {
			t.Errorf("receipt[0] issuer want CCV/VVR %s, got %s", vvrID, rcpts[0].Issuer)
		}
		if rcpts[1].Issuer != executorID {
			t.Errorf("receipt[1] issuer want executor %s, got %s", executorID, rcpts[1].Issuer)
		}
		if rcpts[2].Issuer != routerID {
			t.Errorf("receipt[2] issuer want router (network) %s, got %s", routerID, rcpts[2].Issuer)
		}
		for i, r := range rcpts {
			if r.FeeTokenAmount == nil || r.FeeTokenAmount.Sign() <= 0 {
				t.Errorf("receipt[%d] fee_token_amount must be > 0 (USD cents), got %v", i, r.FeeTokenAmount)
			}
		}

		senderAfter := feeBalance(deployerAddr)
		vvrAfter := feeBalance(vvrID)
		execAfter := feeBalance(executorID)
		onrampAfter := feeBalance(onrampID)

		// get_fee quote == actual amount debited from the sender (fee parity).
		senderDebit := new(big.Int).Sub(big.NewInt(senderBefore), big.NewInt(senderAfter))
		if senderDebit.Cmp(requiredFee) != 0 {
			t.Fatalf("sender debit %s != get_fee quote %s", senderDebit.String(), requiredFee.String())
		}

		vvrDelta := int64(0)
		if vvrAfter > vvrBefore {
			vvrDelta = vvrAfter - vvrBefore
		}
		execDelta := int64(0)
		if execAfter > execBefore {
			execDelta = execAfter - execBefore
		}
		onrampResidual := int64(0)
		if onrampAfter > onrampBefore {
			onrampResidual = onrampAfter - onrampBefore
		}

		t.Logf("data-only: debit=%s vvrDelta=%d execDelta=%d onrampResidual=%d",
			senderDebit.String(), vvrDelta, execDelta, onrampResidual)

		// H-3: CCV and executor fees transferred OUT at send time.
		if vvrDelta <= 0 {
			t.Errorf("VVR (CCV issuer) fee-token delta must be > 0 (H-3 distribution), got %d", vvrDelta)
		}
		if execDelta <= 0 {
			t.Errorf("executor fee-token delta must be > 0 (H-3 distribution), got %d", execDelta)
		}
		// INV-FEE-14: the network fee is LEFT on the OnRamp (the residual), not
		// distributed — gas revenue is no longer in this residual.
		if onrampResidual <= 0 {
			t.Errorf("OnRamp must hold the network-only residual > 0 (INV-FEE-14), got %d", onrampResidual)
		}

		// Conservation: debit == distributed (CCV + executor) + OnRamp residual.
		// Exact for a non-LINK fee token (premium_multiplier = 100 ⇒ per-receipt
		// premium-convert is bit-identical to the bare convert, no floor-dust).
		sum := new(big.Int).Add(big.NewInt(vvrDelta), big.NewInt(execDelta))
		sum.Add(sum, big.NewInt(onrampResidual))
		if sum.Cmp(senderDebit) != 0 {
			t.Fatalf("conservation broken: CCV(%d)+executor(%d)+residual(%d)=%s != debit %s",
				vvrDelta, execDelta, onrampResidual, sum.String(), senderDebit.String())
		}
		t.Log("conservation holds: debit == CCV + executor + OnRamp residual")
	})

	// ------------------------------------------------------------------
	// 2. Sweep: withdraw_fee_tokens moves the OnRamp residual to the
	//    fee_aggregator. OnRamp balance must drop to 0.
	// ------------------------------------------------------------------
	t.Run("withdraw_fee_tokens sweeps OnRamp residual to fee aggregator", func(t *testing.T) {
		onrampBefore := feeBalance(onrampID)
		if onrampBefore <= 0 {
			t.Fatalf("OnRamp should hold the network residual from the prior send, got %d", onrampBefore)
		}

		if err := wire.OnRampClient.WithdrawFeeTokens(ctx, []string{feeToken}); err != nil {
			// A revert here means the OnRamp's fee_aggregator (a mock contract ID in
			// deployOutboundSendWire) cannot receive a SAC transfer. Wire a real
			// fee-aggregator contract into the OnRamp DynamicConfig to exercise the
			// sweep end-to-end.
			t.Fatalf("WithdrawFeeTokens: %v (see harness note in file header)", err)
		}

		onrampAfter := feeBalance(onrampID)
		if onrampAfter != 0 {
			t.Fatalf("OnRamp fee-token balance must be 0 after sweep, got %d (residual was %d)", onrampAfter, onrampBefore)
		}
		t.Logf("sweep moved residual %d off the OnRamp; balance now 0", onrampBefore)
	})

	// ------------------------------------------------------------------
	// 3. Token send: the pool-fee slice is distributed to the pool too.
	//    Conservation: debit == CCV + pool + executor + residual.
	// ------------------------------------------------------------------
	t.Run("token send distributes pool fee in addition to CCV and executor", func(t *testing.T) {
		const tokenTransferAmount = int64(1_000_000) // 0.1 INTG at 7 decimals
		msg := routerbindings.StellarToAnyMessage{
			Receiver:     evmReceiver,
			Data:         []byte("fee-distribution token send"),
			FeeToken:     feeToken,
			ExtraArgs:    buildExtraArgs(),
			TokenAmounts: []routerbindings.TokenAmount{{Token: sacToken, Amount: big.NewInt(tokenTransferAmount)}},
		}

		requiredFee, err := stack.RouterClient.GetFee(ctx, remoteDestChain, msg)
		if err != nil {
			t.Fatalf("Router GetFee (token): %v", err)
		}
		if requiredFee.Sign() <= 0 {
			t.Fatalf("expected positive fee, got %s", requiredFee.String())
		}

		senderBefore := feeBalance(deployerAddr)
		vvrBefore := feeBalance(vvrID)
		poolBefore := feeBalance(stack.TokenPoolID)
		execBefore := feeBalance(executorID)
		onrampBefore := feeBalance(onrampID)

		latest, err := rpcClient.GetLatestLedger(ctx)
		if err != nil {
			t.Fatalf("GetLatestLedger: %v", err)
		}
		startLedger := latest.Sequence

		msgID, err := stack.RouterClient.CcipSend(ctx, deployerAddr, remoteDestChain, msg, requiredFee)
		if err != nil {
			t.Fatalf("Router CcipSend (token): %v", err)
		}
		if msgID == ([32]byte{}) {
			t.Fatal("CcipSend (token) returned empty message_id")
		}

		sentEvt, err := wire.OnRampClient.WaitForCCIPMessageSentEvent(ctx, startLedger, 30*time.Second,
			func(e *onrampbindings.CCIPMessageSentEvent) bool {
				return e.DestChainSelector == remoteDestChain && bytes.Equal(e.MessageId[:], msgID[:])
			})
		if err != nil {
			t.Fatalf("WaitForCCIPMessageSentEvent (token): %v", err)
		}

		// Receipt layout [CCV, pool, executor, network] for a token send.
		rcpts := sentEvt.Receipts
		const wantReceipts = 4
		if len(rcpts) != wantReceipts {
			t.Fatalf("receipts: want %d (CCV + pool + executor + network), got %d", wantReceipts, len(rcpts))
		}
		if rcpts[0].Issuer != vvrID {
			t.Errorf("receipt[0] issuer want CCV/VVR %s, got %s", vvrID, rcpts[0].Issuer)
		}
		if rcpts[1].Issuer != stack.TokenPoolID {
			t.Errorf("receipt[1] issuer want pool %s, got %s", stack.TokenPoolID, rcpts[1].Issuer)
		}
		if rcpts[2].Issuer != executorID {
			t.Errorf("receipt[2] issuer want executor %s, got %s", executorID, rcpts[2].Issuer)
		}
		if rcpts[3].Issuer != routerID {
			t.Errorf("receipt[3] issuer want router (network) %s, got %s", routerID, rcpts[3].Issuer)
		}

		senderAfter := feeBalance(deployerAddr)
		vvrAfter := feeBalance(vvrID)
		poolAfter := feeBalance(stack.TokenPoolID)
		execAfter := feeBalance(executorID)
		onrampAfter := feeBalance(onrampID)

		senderDebit := new(big.Int).Sub(big.NewInt(senderBefore), big.NewInt(senderAfter))
		if senderDebit.Cmp(requiredFee) != 0 {
			t.Fatalf("sender debit %s != get_fee quote %s", senderDebit.String(), requiredFee.String())
		}

		delta := func(before, after int64) int64 {
			if after > before {
				return after - before
			}
			return 0
		}
		vvrDelta := delta(vvrBefore, vvrAfter)
		poolDelta := delta(poolBefore, poolAfter)
		execDelta := delta(execBefore, execAfter)
		onrampResidual := delta(onrampBefore, onrampAfter)

		t.Logf("token: debit=%s vvrDelta=%d poolDelta=%d execDelta=%d onrampResidual=%d",
			senderDebit.String(), vvrDelta, poolDelta, execDelta, onrampResidual)

		if vvrDelta <= 0 {
			t.Errorf("VVR (CCV) delta must be > 0, got %d", vvrDelta)
		}
		if poolDelta <= 0 {
			t.Errorf("pool fee-token delta must be > 0 (H-3 pool-fee distribution), got %d", poolDelta)
		}
		if execDelta <= 0 {
			t.Errorf("executor delta must be > 0, got %d", execDelta)
		}
		if onrampResidual <= 0 {
			t.Errorf("OnRamp network residual must be > 0, got %d", onrampResidual)
		}

		sum := new(big.Int).Add(big.NewInt(vvrDelta), big.NewInt(poolDelta))
		sum.Add(sum, big.NewInt(execDelta))
		sum.Add(sum, big.NewInt(onrampResidual))
		if sum.Cmp(senderDebit) != 0 {
			t.Fatalf("conservation broken: CCV(%d)+pool(%d)+executor(%d)+residual(%d)=%s != debit %s",
				vvrDelta, poolDelta, execDelta, onrampResidual, sum.String(), senderDebit.String())
		}
		t.Log("conservation holds: debit == CCV + pool + executor + OnRamp residual")
	})
}
