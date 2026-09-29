//go:build integration

package integration

import (
	"bytes"
	"context"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	bnmbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/bnm_token"
	burnmintpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/burn_mint_pool"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	rampregistrybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ramp_registry"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
)

const (
	// bnmTokenName/bnmTokenSymbol/bnmTokenDecimals mirror the EVM
	// BurnMintERC20 test-token conventions (7 decimals = Stellar SAC convention).
	bnmTokenName     = "CCIP BnM"
	bnmTokenSymbol   = "BnM"
	bnmTokenDecimals = uint32(7)

	// bnmDripAmount is what one permissionless drip call mints (0.1 BnM at 7
	// decimals — a deliberate divergence from EVM's 1-token drip).
	bnmDripAmount = int64(1_000_000)
)

// bnmLane is the CCIP BnM test token (the repo's custom, non-SAC Soroban token
// implementing StellarAssetInterface) plus its burn-mint pool, onboarded in the
// canonical order of deployment.RunDeployBnmToken.
type bnmLane struct {
	TokenID     string
	PoolID      string
	TokenClient *bnmbindings.BnmTokenClient
	PoolClient  *burnmintpoolbindings.BurnMintPoolClient

	// RemotePool/RemoteToken are the placeholder 20-byte remote pool/token
	// addresses the lane is configured for (EVM-address-shaped, as on main).
	RemotePool  []byte
	RemoteToken []byte
}

// deployBnmTokenPool is the burn-mint analogue of fullStack.deployTokenPool: it
// deploys the custom BnM token and a burn-mint pool for it, and registers the
// lane in the stack's TokenAdminRegistry. Mirrors the canonical onboarding
// order of deployment.RunDeployBnmToken: deploy BnM + initialize → deploy
// burn-mint pool → set_admin(bnm → pool, the mint-authority handoff) →
// initialize pool → TAR triple. Unlike the lock-release lane there is NO
// TokenLockBox: the pool burns on outbound and mints on inbound, so its own
// balance only ever holds accrued fees.
func (s *fullStack) deployBnmTokenPool(
	ctx context.Context,
	t *testing.T,
	projectRoot string,
	deployer *deployment.Deployer,
	deployerAddr string,
	saltPrefix string,
	remoteChainSelector uint64,
) *bnmLane {
	t.Helper()

	deploy := func(name, wasmFile string) string {
		t.Helper()
		salt := deployment.GenerateDeterministicSalt(deployerAddr, saltPrefix+"-"+name)
		p := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", wasmFile)
		id, err := deployer.DeployContract(ctx, p, salt)
		if err != nil {
			t.Fatalf("deploy %s: %v", name, err)
		}
		return id
	}

	if s.RouterID == "" {
		t.Fatal("fullStack.RouterID is empty; deployFullStack must run before deployBnmTokenPool")
	}
	if s.RmnProxyID == "" {
		t.Fatal("fullStack.RmnProxyID is empty; deployFullStack must run before deployBnmTokenPool")
	}

	lane := &bnmLane{}

	// 1. BnM token: deploy + initialize with the deployer as admin (one-shot).
	lane.TokenID = deploy("bnm-token", "bnm_token.wasm")
	lane.TokenClient = bnmbindings.NewBnmTokenClient(deployer, lane.TokenID)
	if err := lane.TokenClient.Initialize(ctx, deployerAddr, bnmTokenName, bnmTokenSymbol, bnmTokenDecimals); err != nil {
		t.Fatalf("BnmToken Initialize: %v", err)
	}

	// Sanity: the SAC read entrypoints simulate through the binding (they are
	// the readonly_fns list in scripts/gen_bindings.sh, not the name heuristic).
	name, err := lane.TokenClient.Name(ctx)
	if err != nil {
		t.Fatalf("BnmToken Name: %v", err)
	}
	if name != bnmTokenName {
		t.Fatalf("BnmToken name mismatch: want %q, got %q", bnmTokenName, name)
	}
	decimals, err := lane.TokenClient.Decimals(ctx)
	if err != nil {
		t.Fatalf("BnmToken Decimals: %v", err)
	}
	if decimals != bnmTokenDecimals {
		t.Fatalf("BnmToken decimals mismatch: want %d, got %d", bnmTokenDecimals, decimals)
	}

	// 2. Burn-mint pool contract; hand BnM mint authority to the pool BEFORE
	// pool initialize (canonical RunDeployBnmToken order). set_admin requires
	// the current admin (the deployer) to authorize.
	lane.PoolID = deploy("burn-mint-pool", "pools_burn_mint_pool.wasm")
	lane.PoolClient = burnmintpoolbindings.NewBurnMintPoolClient(deployer, lane.PoolID)
	if err := lane.TokenClient.SetAdmin(ctx, lane.PoolID); err != nil {
		t.Fatalf("BnmToken SetAdmin(pool): %v", err)
	}
	admin, err := lane.TokenClient.Admin(ctx)
	if err != nil {
		t.Fatalf("BnmToken Admin: %v", err)
	}
	if admin != lane.PoolID {
		t.Fatalf("BnmToken admin after handoff: want pool %s, got %s", lane.PoolID, admin)
	}

	// 3. Ramp registry (mirrors deployTokenPool): the pool's authorized-caller
	// checks resolve the OnRamp/OffRamp through it.
	s.RampRegistryID = deploy("ramp-registry", "ccip_ramp_registry.wasm")
	s.RampRegistryClient = rampregistrybindings.NewRampRegistryClient(deployer, s.RampRegistryID)
	if err := s.RampRegistryClient.Initialize(ctx, deployerAddr); err != nil {
		t.Fatalf("RampRegistry Initialize: %v", err)
	}
	if err := s.RampRegistryClient.ApplyOfframpUpdates(ctx, []rampregistrybindings.OffRampUpdate{
		{
			SourceChainSelector: remoteSourceChain,
			Offramp:             s.OfframpID,
			Enabled:             true,
		},
	}); err != nil {
		t.Fatalf("RampRegistry AddOfframp: %v", err)
	}

	// 4. Initialize the pool against the stack's Router/RampRegistry/RmnProxy.
	// The RMN proxy is set immutably at initialize (EVM `immutable i_rmnProxy`
	// parity) and must match the Router's.
	if err := lane.PoolClient.Initialize(ctx, deployerAddr, lane.TokenID, bnmTokenDecimals, s.RouterID, s.RampRegistryID, s.RmnProxyID); err != nil {
		t.Fatalf("BurnMintPool Initialize: %v", err)
	}

	// 5. TAR triple (reuses the stack's registry; deployFullStack(true) or
	// ensureTokenAdminRegistryForStack created it). propose_administrator only
	// requires the TAR owner (the deployer), not the token's on-chain admin —
	// which is now the pool — so this ordering works live.
	ensureTokenAdminRegistryForStack(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix, s)
	if err := s.TokenAdminRegistryClient.ProposeAdministrator(ctx, deployerAddr, lane.TokenID, deployerAddr); err != nil {
		t.Fatalf("TokenAdminRegistry ProposeAdministrator: %v", err)
	}
	if err := s.TokenAdminRegistryClient.AcceptAdminRole(ctx, lane.TokenID); err != nil {
		t.Fatalf("TokenAdminRegistry AcceptAdminRole: %v", err)
	}
	if err := s.TokenAdminRegistryClient.SetPool(ctx, lane.TokenID, &lane.PoolID); err != nil {
		t.Fatalf("TokenAdminRegistry SetPool: %v", err)
	}

	// 6. Remote chain support (placeholder 20-byte EVM-address-shaped ids).
	lane.RemotePool = bytes.Repeat([]byte{0x51}, 20)
	lane.RemoteToken = bytes.Repeat([]byte{0x52}, 20)
	if err := lane.PoolClient.ApplyChainUpdates(ctx, []burnmintpoolbindings.ChainUpdate{{
		RemoteChainSelector:       remoteChainSelector,
		RemotePoolAddresses:       [][]byte{lane.RemotePool},
		RemoteTokenAddress:        lane.RemoteToken,
		OutboundRateLimiterConfig: burnmintpoolbindings.RateLimitConfig{},
		InboundRateLimiterConfig:  burnmintpoolbindings.RateLimitConfig{},
	}}, nil); err != nil {
		t.Fatalf("BurnMintPool ApplyChainUpdates: %v", err)
	}

	t.Logf("BnM lane: token %s, burn-mint pool %s (admin handed off to pool)", lane.TokenID, lane.PoolID)
	return lane
}

// bnmBalanceOrFatal reads a BnM balance for a holder (G… account or C… contract).
func bnmBalanceOrFatal(ctx context.Context, t *testing.T, client *bnmbindings.BnmTokenClient, holder string) *big.Int {
	t.Helper()
	bal, err := client.Balance(ctx, holder)
	if err != nil {
		t.Fatalf("BnmToken balance(holder=%s): %v", holder, err)
	}
	return bal
}

// TestBnmTokenPoolOutbound validates the outbound leg of a CCIP token send
// through the BurnMintPool with the custom (non-SAC) BnM token — the live
// Go-binding-level proof that lock_or_burn works unchanged against a plain
// Soroban contract implementing StellarAssetInterface:
//
//  1. drip funds the sender. drip has no require_auth, and the deployer is no
//     longer the BnM admin at this point (set_admin handed mint authority to
//     the pool), so a successful drip is also the live permissionless proof.
//  2. Router ccip_send with a BnM TokenAmount → OnRamp → BurnMintPool
//     lock_or_burn burns the post-fee dest_token_amount from the sender and
//     accrues only the in-token fee on the pool's own balance.
//
// The assertions pin the burn-mint semantics that distinguish this lane from
// the lock-release lane (token_pool_test.go): there is no lockbox, and the
// pool never holds the principal — its balance only moves by the fee accrual.
func TestBnmTokenPoolOutbound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, networkPassphrase, friendbotURL := GetSharedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	const destChain = uint64(11111)
	const remoteDestChain = uint64(22222)
	const outboundSalt = "bnm-pool-outbound"

	stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, destChain, outboundSalt, false)

	// Fee token stays a 7-dec SAC (FeeQuoter link_token convention; see
	// deployOutboundSendWire). Only the transferred token is the custom BnM.
	feeToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, outboundSalt+"-fee")

	lane := stack.deployBnmTokenPool(ctx, t, projectRoot, deployer, deployerAddr, outboundSalt, remoteDestChain)

	deployOutboundSendWire(ctx, t, projectRoot, deployer, deployerAddr, outboundSalt, stack,
		destChain, remoteDestChain, feeToken, []string{lane.TokenID})

	const tokenTransferAmount = int64(1_000_000) // 0.1 BnM at 7 decimals — exactly one drip

	// Fund the sender with two drips (0.2 BnM). Admin() == pool (asserted in
	// deployBnmTokenPool) proves the deployer is NOT the BnM admin here.
	for i := 0; i < 2; i++ {
		if err := lane.TokenClient.Drip(ctx, deployerAddr); err != nil {
			t.Fatalf("BnmToken Drip %d: %v", i+1, err)
		}
	}
	senderBefore := bnmBalanceOrFatal(ctx, t, lane.TokenClient, deployerAddr)
	if want := big.NewInt(2 * bnmDripAmount); senderBefore.Cmp(want) != 0 {
		t.Fatalf("sender BnM balance after 2 drips: want %s, got %s", want, senderBefore)
	}
	poolBefore := bnmBalanceOrFatal(ctx, t, lane.TokenClient, lane.PoolID)

	// Fee quote + send, mirroring the lock-release outbound test.
	extraArgs, err := encodeOnrampExtraArgsV3(onrampbindings.GenericExtraArgsV3{
		Ccvs:               []string{stack.VvrID},
		CcvArgs:            [][]byte{{}},
		Executor:           stack.ExecutorID,
		ExecutorArgs:       []byte{},
		GasLimit:           0,
		BlockConfirmations: 0,
		TokenReceiver:      []byte{},
		TokenArgs:          []byte{},
	})
	if err != nil {
		t.Fatalf("encode extra args: %v", err)
	}

	evmReceiver := bytes.Repeat([]byte{0x33}, 20)
	msg := routerbindings.StellarToAnyMessage{
		Receiver:     evmReceiver,
		Data:         []byte("integration bnm burn-mint ccip_send"),
		FeeToken:     feeToken,
		ExtraArgs:    extraArgs,
		TokenAmounts: []routerbindings.TokenAmount{{Token: lane.TokenID, Amount: big.NewInt(tokenTransferAmount)}},
	}

	requiredFee, err := stack.RouterClient.GetFee(ctx, remoteDestChain, msg)
	if err != nil {
		t.Fatalf("Router GetFee: %v", err)
	}
	if requiredFee.Sign() <= 0 {
		t.Fatalf("expected positive fee for token message, got %d", requiredFee)
	}

	latest, err := rpcClient.GetLatestLedger(ctx)
	if err != nil {
		t.Fatalf("GetLatestLedger: %v", err)
	}
	startLedger := latest.Sequence

	tokenMsgID, err := stack.RouterClient.CcipSend(ctx, deployerAddr, remoteDestChain, msg, requiredFee)
	if err != nil {
		t.Fatalf("Router CcipSend (with BnM token): %v", err)
	}
	if tokenMsgID == [32]byte{} {
		t.Fatal("CcipSend (token) returned empty message_id")
	}
	t.Logf("token transfer message_id: %x", tokenMsgID)

	// lock_or_burn publishes BurnedEvent with the post-fee dest_token_amount
	// (the pool's TokenTransferFeeConfig is unset/disabled in this stack, so
	// dest == the full amount, but read it from the event rather than assuming).
	const eventWait = 30 * time.Second
	burned, err := lane.PoolClient.WaitForBurnedEvent(ctx, startLedger, eventWait,
		func(e *burnmintpoolbindings.BurnedEvent) bool { return e.Sender == deployerAddr })
	if err != nil {
		t.Fatalf("WaitForBurnedEvent: %v", err)
	}
	feeAmount := new(big.Int).Sub(big.NewInt(tokenTransferAmount), burned.Amount)
	if feeAmount.Sign() < 0 {
		t.Fatalf("burned amount %s exceeds sent amount %d", burned.Amount, tokenTransferAmount)
	}
	t.Logf("BurnedEvent at ledger %d: dest_token_amount=%s, in-token fee=%s",
		burned.Ledger, burned.Amount, feeAmount)

	senderAfter := bnmBalanceOrFatal(ctx, t, lane.TokenClient, deployerAddr)
	poolAfter := bnmBalanceOrFatal(ctx, t, lane.TokenClient, lane.PoolID)

	// Burn semantics: the whole sent amount leaves the sender (burn of
	// dest_token_amount + transfer of the fee), and the pool's own balance only
	// grows by the fee accrual — never the principal, and there is no lockbox.
	if got := new(big.Int).Sub(senderBefore, senderAfter); got.Cmp(big.NewInt(tokenTransferAmount)) != 0 {
		t.Fatalf("sender BnM balance should drop by %d; before=%s after=%s (delta=%s)",
			tokenTransferAmount, senderBefore, senderAfter, got)
	}
	if got := new(big.Int).Sub(poolAfter, poolBefore); got.Cmp(feeAmount) != 0 {
		t.Fatalf("pool BnM balance should change by fees only (%s); before=%s after=%s (delta=%s)",
			feeAmount, poolBefore, poolAfter, got)
	}
	t.Logf("outbound lock_or_burn: burned %s from sender, accrued %s fee on pool (no lockbox)",
		burned.Amount, feeAmount)
}

// TestBnmTokenPoolInbound validates the inbound leg with the custom BnM token:
// OffRamp Execute → BurnMintPool release_or_mint mints fresh BnM to the
// receiver (the pool is the token admin after the set_admin handoff). Mirrors
// the lock-release inbound test (token_pool_test.go) but pins MINT semantics:
// the receiver balance is minted out of nothing and the pool's own balance is
// untouched — there is no lockbox to withdraw escrowed liquidity from.
func TestBnmTokenPoolInbound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, _, _ := GetSharedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	const localChain = uint64(11111)
	const inboundSalt = "bnm-pool-inbound"

	stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localChain, inboundSalt, true)
	lane := stack.deployBnmTokenPool(ctx, t, projectRoot, deployer, deployerAddr, inboundSalt, remoteSourceChain)

	const releaseAmount = int64(2_000_000) // 0.2 BnM at 7 decimals
	const seqNo = uint64(1)

	rcvBefore := bnmBalanceOrFatal(ctx, t, lane.TokenClient, stack.ReceiverID)
	poolBefore := bnmBalanceOrFatal(ctx, t, lane.TokenClient, lane.PoolID)

	tokenXfer, err := EncodeCcipTokenTransferV1Inbound(releaseAmount, lane.RemotePool, lane.RemoteToken, lane.TokenID, stack.ReceiverID, nil)
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

	latest, err := rpcClient.GetLatestLedger(ctx)
	if err != nil {
		t.Fatalf("GetLatestLedger: %v", err)
	}
	startLedger := latest.Sequence

	if err := stack.OfframpClient.Execute(ctx, encoded, []string{stack.VvrID}, [][]byte{verifierBlob}, 0); err != nil {
		t.Fatalf("OffRamp Execute (inbound BnM release): %v", err)
	}

	state, err := stack.OfframpClient.GetExecutionState(ctx, msgID)
	if err != nil {
		t.Fatalf("GetExecutionState: %v", err)
	}
	if state != offrampbindings.MessageExecutionStateSuccess {
		t.Fatalf("execution state = %d, want Success (%d)", state, offrampbindings.MessageExecutionStateSuccess)
	}

	// release_or_mint publishes MintedEvent(sender=pool, recipient, amount).
	const eventWait = 30 * time.Second
	minted, err := lane.PoolClient.WaitForMintedEvent(ctx, startLedger, eventWait,
		func(e *burnmintpoolbindings.MintedEvent) bool { return e.Recipient == stack.ReceiverID })
	if err != nil {
		t.Fatalf("WaitForMintedEvent: %v", err)
	}
	if minted.Amount.Cmp(big.NewInt(releaseAmount)) != 0 {
		t.Fatalf("MintedEvent amount: want %d, got %s", releaseAmount, minted.Amount)
	}

	rcvAfter := bnmBalanceOrFatal(ctx, t, lane.TokenClient, stack.ReceiverID)
	poolAfter := bnmBalanceOrFatal(ctx, t, lane.TokenClient, lane.PoolID)

	// Mint semantics: the receiver balance grows by exactly the release amount
	// with no source of funds (freshly minted), and the pool's own balance is
	// unchanged — unlike lock-release there is no lockbox draining.
	if got := new(big.Int).Sub(rcvAfter, rcvBefore); got.Cmp(big.NewInt(releaseAmount)) != 0 {
		t.Fatalf("receiver BnM balance should increase by %d; before=%s after=%s (delta=%s)",
			releaseAmount, rcvBefore, rcvAfter, got)
	}
	if got := new(big.Int).Sub(poolAfter, poolBefore); got.Sign() != 0 {
		t.Fatalf("pool BnM balance should be unchanged (mint, no custody); before=%s after=%s (delta=%s)",
			poolBefore, poolAfter, got)
	}
	t.Logf("inbound release_or_mint: minted %d fresh BnM to receiver %s (pool balance untouched)",
		releaseAmount, stack.ReceiverID)
}
