//go:build integration

package integration

import (
	"bytes"
	"context"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"

	burnmintpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/burn_mint_pool"
	linkbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/link_token"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	rampregistrybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ramp_registry"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
)

const (
	// linkTokenName/linkTokenSymbol mirror the EVM LinkToken metadata; the
	// decimals are the deliberate Stellar divergence (7, the SAC convention,
	// vs EVM's 18).
	linkTokenName     = "ChainLink Token"
	linkTokenSymbol   = "LINK"
	linkTokenDecimals = uint32(7)
)

// linkLane is the custom (non-SAC) LINK token plus its burn-mint pool,
// onboarded in the canonical order (deploy + initialize → pre-mint while the
// deployer is still admin → set_admin handoff to the pool → pool initialize →
// TAR triple). LINK has NO faucet (unlike the BnM test token): mint authority
// is the pool's alone after the handoff, so any pre-handoff funding must be
// minted before set_admin.
type linkLane struct {
	TokenID     string
	PoolID      string
	TokenClient *linkbindings.LinkTokenClient
	PoolClient  *burnmintpoolbindings.BurnMintPoolClient

	// RemotePool/RemoteToken are the placeholder 20-byte remote pool/token
	// addresses the lane is configured for (EVM-address-shaped, as on main).
	RemotePool  []byte
	RemoteToken []byte
}

// deployLinkToken deploys the custom (non-SAC) LINK token and initializes it
// with the deployer as admin, named "ChainLink Token" / "LINK" / 7 decimals.
// No supply is minted here and no admin handoff happens — LINK is
// remotely-issued, so callers decide whether to mint (only possible while the
// deployer is still admin) and whether to hand mint authority to a burn-mint
// pool via set_admin (deployLinkTokenPool).
func deployLinkToken(
	ctx context.Context,
	t *testing.T,
	projectRoot string,
	deployer *deployment.Deployer,
	deployerAddr string,
	saltPrefix string,
) (*linkbindings.LinkTokenClient, string) {
	t.Helper()

	salt := deployment.GenerateDeterministicSalt(deployerAddr, saltPrefix+"-link-token")
	wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "link_token.wasm")
	tokenID, err := deployer.DeployContract(ctx, wasmPath, salt)
	if err != nil {
		t.Fatalf("deploy link-token: %v", err)
	}
	client := linkbindings.NewLinkTokenClient(deployer, tokenID)
	if err := client.Initialize(ctx, deployerAddr, linkTokenName, linkTokenSymbol, linkTokenDecimals); err != nil {
		t.Fatalf("LinkToken Initialize: %v", err)
	}

	// Sanity: the SAC read entrypoints simulate through the binding (they are
	// the readonly_fns list in scripts/gen_bindings.sh, not the name heuristic).
	name, err := client.Name(ctx)
	if err != nil {
		t.Fatalf("LinkToken Name: %v", err)
	}
	if name != linkTokenName {
		t.Fatalf("LinkToken name mismatch: want %q, got %q", linkTokenName, name)
	}
	symbol, err := client.Symbol(ctx)
	if err != nil {
		t.Fatalf("LinkToken Symbol: %v", err)
	}
	if symbol != linkTokenSymbol {
		t.Fatalf("LinkToken symbol mismatch: want %q, got %q", linkTokenSymbol, symbol)
	}
	decimals, err := client.Decimals(ctx)
	if err != nil {
		t.Fatalf("LinkToken Decimals: %v", err)
	}
	if decimals != linkTokenDecimals {
		t.Fatalf("LinkToken decimals mismatch: want %d, got %d", linkTokenDecimals, decimals)
	}
	if tv, err := client.TypeAndVersion(ctx); err != nil || tv != "LinkToken 1.0.0" {
		t.Fatalf("LinkToken type_and_version: want \"LinkToken 1.0.0\", got %q (err=%v)", tv, err)
	}

	return client, tokenID
}

// deployLinkTokenPool is the LINK analogue of deployBnmTokenPool, with the one
// structural difference forced by LINK having no faucet: any balance must be
// pre-minted (preMintTo, preMintAmount) while the deployer is still the admin,
// BEFORE the set_admin handoff gives mint authority to the pool. After the
// handoff the pool is the only minter (it mints on inbound bridge messages);
// nobody — not even the deployer — can mint out of thin air on Stellar.
// Mirrors the canonical order of the BnM sequence: deploy LINK + initialize →
// pre-mint → deploy burn-mint pool → set_admin(LINK → pool) → initialize pool
// → TAR triple → ApplyChainUpdates. Like the BnM lane there is NO TokenLockBox.
func (s *fullStack) deployLinkTokenPool(
	ctx context.Context,
	t *testing.T,
	projectRoot string,
	deployer *deployment.Deployer,
	deployerAddr string,
	saltPrefix string,
	remoteChainSelector uint64,
	preMintTo string,
	preMintAmount int64,
) *linkLane {
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
		t.Fatal("fullStack.RouterID is empty; deployFullStack must run before deployLinkTokenPool")
	}
	if s.RmnProxyID == "" {
		t.Fatal("fullStack.RmnProxyID is empty; deployFullStack must run before deployLinkTokenPool")
	}

	lane := &linkLane{}

	// 1. LINK token: deploy + initialize with the deployer as admin (one-shot).
	lane.TokenClient, lane.TokenID = deployLinkToken(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix)

	// 2. Pre-handoff funding: mint while the deployer is still admin. This is
	// the ONLY time a non-pool party can mint — after set_admin the pool's
	// require_auth must be satisfied, which only the pool's own release_or_mint
	// (on a verified inbound bridge message) does.
	if preMintAmount > 0 {
		if err := lane.TokenClient.Mint(ctx, preMintTo, big.NewInt(preMintAmount)); err != nil {
			t.Fatalf("LinkToken pre-handoff Mint(%s, %d): %v", preMintTo, preMintAmount, err)
		}
	}

	// 3. Burn-mint pool contract; hand LINK mint authority to the pool BEFORE
	// pool initialize (canonical order). set_admin requires the current admin
	// (the deployer) to authorize.
	lane.PoolID = deploy("burn-mint-pool", "pools_burn_mint_pool.wasm")
	lane.PoolClient = burnmintpoolbindings.NewBurnMintPoolClient(deployer, lane.PoolID)
	if err := lane.TokenClient.SetAdmin(ctx, lane.PoolID); err != nil {
		t.Fatalf("LinkToken SetAdmin(pool): %v", err)
	}
	admin, err := lane.TokenClient.Admin(ctx)
	if err != nil {
		t.Fatalf("LinkToken Admin: %v", err)
	}
	if admin != lane.PoolID {
		t.Fatalf("LinkToken admin after handoff: want pool %s, got %s", lane.PoolID, admin)
	}

	// 4. Ramp registry (mirrors deployBnmTokenPool): the pool's
	// authorized-caller checks resolve the OnRamp/OffRamp through it.
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

	// 5. Initialize the pool against the stack's Router/RampRegistry/RmnProxy.
	if err := lane.PoolClient.Initialize(ctx, deployerAddr, lane.TokenID, linkTokenDecimals, s.RouterID, s.RampRegistryID, s.RmnProxyID); err != nil {
		t.Fatalf("BurnMintPool Initialize: %v", err)
	}

	// 6. TAR triple (reuses the stack's registry). propose_administrator only
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

	// 7. Remote chain support (placeholder 20-byte EVM-address-shaped ids).
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

	t.Logf("LINK lane: token %s, burn-mint pool %s (mint authority handed off to pool)", lane.TokenID, lane.PoolID)
	return lane
}

// linkBalanceOrFatal reads a LINK balance for a holder (G… account or C… contract).
func linkBalanceOrFatal(ctx context.Context, t *testing.T, client *linkbindings.LinkTokenClient, holder string) *big.Int {
	t.Helper()
	bal, err := client.Balance(ctx, holder)
	if err != nil {
		t.Fatalf("LinkToken balance(holder=%s): %v", holder, err)
	}
	return bal
}

// TestLinkTokenPoolOutbound validates the outbound leg of a CCIP token send
// through the BurnMintPool with the custom (non-SAC) LINK token — and pins the
// no-faucet property that distinguishes LINK from the BnM test token:
//
//  1. The sender's balance can ONLY come from the pre-handoff admin mint
//     (pinned: sender starts with exactly the pre-mint amount).
//  2. After the set_admin handoff the deployer is no longer the admin, so its
//     direct Mint is rejected — the pool is the sole minter and it only mints
//     on inbound bridge messages. This is the negative proof there is no
//     local faucet.
//  3. Router ccip_send with a LINK TokenAmount → OnRamp → BurnMintPool
//     lock_or_burn burns the post-fee dest_token_amount from the sender and
//     accrues only the in-token fee on the pool's own balance (no lockbox).
func TestLinkTokenPoolOutbound(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, networkPassphrase, friendbotURL := GetIsolatedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	const destChain = uint64(11111)
	const remoteDestChain = uint64(22222)
	const outboundSalt = "link-pool-outbound"

	stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, destChain, outboundSalt, false)

	// Fee token stays a 7-dec SAC (FeeQuoter link_token convention; see
	// deployOutboundSendWire). Only the transferred token is the custom LINK.
	feeToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, outboundSalt+"-fee")

	const preMint = int64(2_000_000) // 0.2 LINK at 7 decimals
	lane := stack.deployLinkTokenPool(ctx, t, projectRoot, deployer, deployerAddr, outboundSalt, remoteDestChain,
		deployerAddr, preMint)

	deployOutboundSendWire(ctx, t, projectRoot, deployer, deployerAddr, outboundSalt, stack,
		destChain, remoteDestChain, feeToken, []string{lane.TokenID})

	const tokenTransferAmount = int64(1_000_000) // 0.1 LINK at 7 decimals

	// The pre-handoff mint is the only supply source: the sender holds exactly
	// the pre-mint, and nothing else (no faucet) exists on the lane.
	senderBefore := linkBalanceOrFatal(ctx, t, lane.TokenClient, deployerAddr)
	if want := big.NewInt(preMint); senderBefore.Cmp(want) != 0 {
		t.Fatalf("sender LINK balance before send: want %s (pre-mint only), got %s", want, senderBefore)
	}
	poolBefore := linkBalanceOrFatal(ctx, t, lane.TokenClient, lane.PoolID)

	// No-faucet negative proof: the deployer is no longer the LINK admin
	// (Admin() == pool, asserted in deployLinkTokenPool), so its direct mint
	// must be rejected — LINK is only ever minted by the pool on inbound
	// bridge messages.
	if err := lane.TokenClient.Mint(ctx, deployerAddr, big.NewInt(1_000_000)); err == nil {
		t.Fatal("post-handoff Mint by the (former-admin) deployer must fail — LINK has no local faucet")
	} else {
		t.Logf("post-handoff Mint by the deployer correctly rejected: %v", err)
	}

	// Fee quote + send, mirroring the BnM outbound test.
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
		Data:         []byte("integration link burn-mint ccip_send"),
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

	tokenMsgID, err := stack.RouterClient.CcipSend(ctx, deployerAddr, remoteDestChain, msg)
	if err != nil {
		t.Fatalf("Router CcipSend (with LINK token): %v", err)
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

	senderAfter := linkBalanceOrFatal(ctx, t, lane.TokenClient, deployerAddr)
	poolAfter := linkBalanceOrFatal(ctx, t, lane.TokenClient, lane.PoolID)

	// Burn semantics: the whole sent amount leaves the sender (burn of
	// dest_token_amount + transfer of the fee), and the pool's own balance only
	// grows by the fee accrual — never the principal, and there is no lockbox.
	if got := new(big.Int).Sub(senderBefore, senderAfter); got.Cmp(big.NewInt(tokenTransferAmount)) != 0 {
		t.Fatalf("sender LINK balance should drop by %d; before=%s after=%s (delta=%s)",
			tokenTransferAmount, senderBefore, senderAfter, got)
	}
	if got := new(big.Int).Sub(poolAfter, poolBefore); got.Cmp(feeAmount) != 0 {
		t.Fatalf("pool LINK balance should change by fees only (%s); before=%s after=%s (delta=%s)",
			feeAmount, poolBefore, poolAfter, got)
	}
	t.Logf("outbound lock_or_burn: burned %s LINK from sender, accrued %s fee on pool (no lockbox)",
		burned.Amount, feeAmount)
}

// TestLinkTokenPoolInbound validates the inbound leg with the custom LINK
// token: OffRamp Execute → BurnMintPool release_or_mint mints fresh LINK to
// the receiver (the pool is the token admin after the set_admin handoff — the
// only path by which LINK supply ever grows on Stellar). Mirrors the BnM
// inbound test but pins the LINK property: the receiver balance is minted out
// of nothing by the POOL (not by any faucet), and the pool's own balance is
// untouched — there is no lockbox to withdraw escrowed liquidity from.
func TestLinkTokenPoolInbound(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, _, _ := GetIsolatedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	const localChain = uint64(11111)
	const inboundSalt = "link-pool-inbound"

	stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localChain, inboundSalt, true)
	// No pre-mint needed: inbound release_or_mint mints fresh LINK.
	lane := stack.deployLinkTokenPool(ctx, t, projectRoot, deployer, deployerAddr, inboundSalt, remoteSourceChain,
		"", 0)

	const releaseAmount = int64(2_000_000) // 0.2 LINK at 7 decimals
	const seqNo = uint64(1)

	rcvBefore := linkBalanceOrFatal(ctx, t, lane.TokenClient, stack.ReceiverID)
	poolBefore := linkBalanceOrFatal(ctx, t, lane.TokenClient, lane.PoolID)

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
		t.Fatalf("OffRamp Execute (inbound LINK release): %v", err)
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

	rcvAfter := linkBalanceOrFatal(ctx, t, lane.TokenClient, stack.ReceiverID)
	poolAfter := linkBalanceOrFatal(ctx, t, lane.TokenClient, lane.PoolID)

	// Mint semantics: the receiver balance grows by exactly the release amount
	// with no source of funds (freshly minted BY THE POOL — the only LINK
	// mint path), and the pool's own balance is unchanged — unlike
	// lock-release there is no lockbox draining.
	if got := new(big.Int).Sub(rcvAfter, rcvBefore); got.Cmp(big.NewInt(releaseAmount)) != 0 {
		t.Fatalf("receiver LINK balance should increase by %d; before=%s after=%s (delta=%s)",
			releaseAmount, rcvBefore, rcvAfter, got)
	}
	if got := new(big.Int).Sub(poolAfter, poolBefore); got.Sign() != 0 {
		t.Fatalf("pool LINK balance should be unchanged (mint, no custody); before=%s after=%s (delta=%s)",
			poolBefore, poolAfter, got)
	}
	t.Logf("inbound release_or_mint: pool minted %d fresh LINK to receiver %s (pool balance untouched)",
		releaseAmount, stack.ReceiverID)
}

// TestLinkTokenMinterRotation exercises the owner/minters surface of the
// custom LINK token through the Go bindings (docs/token-ownership-and-minters.md
// §5–§6): the owner (the deployer until an optional MCMS transfer) manages the
// minters set; set_admin has reposition semantics (the old primary is demoted
// OUT of the set in the same operation); and the no-faucet property holds at
// every step of the migration overlap.
//
// It deploys ONLY the token — the rotation surface is the token's own. The
// overlap's positive mint legs (mint_as(oldPool/newPool, …)) require the pools'
// own auth inside the call tree, which an external transaction cannot provide,
// so this test asserts the rotation state, the membership rejections, and the
// two-step ownership propose/read/cancel legs; the positive overlap legs are
// covered by the Rust test_two_pool_migration_overlap.
func TestLinkTokenMinterRotation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, _, _, _ := GetIsolatedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	const rotationSalt = "link-minter-rotation"

	client, tokenID := deployLinkToken(ctx, t, projectRoot, deployer, deployerAddr, rotationSalt)
	t.Logf("LINK token %s: owner/primary minter = deployer %s", tokenID, deployerAddr)

	// Pre-handoff: owner = deployer, minters = [deployer] (the deployer holds
	// both roles until the set_admin handoff).
	if owner, err := client.Owner(ctx); err != nil || owner == nil || *owner != deployerAddr {
		t.Fatalf("Owner: want deployer %s, got %v (err=%v)", deployerAddr, owner, err)
	}
	if ok, err := client.IsOwner(ctx, deployerAddr); err != nil || !ok {
		t.Fatalf("IsOwner(deployer) = %v (err=%v); want true", ok, err)
	}
	if minters, err := client.GetMinters(ctx); err != nil || len(minters) != 1 || minters[0] != deployerAddr {
		t.Fatalf("GetMinters: want [deployer], got %v (err=%v)", minters, err)
	}

	// add_minter on the primary is a duplicate (MinterAlreadyExists).
	if err := client.AddMinter(ctx, deployerAddr); err == nil {
		t.Fatal("AddMinter(deployer) must fail — the deployer is already the primary minter")
	} else {
		t.Logf("AddMinter(deployer) correctly rejected: %v", err)
	}

	// The two "pools" and the MCMS stand-in are ordinary accounts — the
	// rotation surface only needs addresses, and none of them ever signs.
	oldPoolKP := keypair.MustRandom()
	newPoolKP := keypair.MustRandom()
	mcmsKP := keypair.MustRandom()
	oldPool, newPool, mcms := oldPoolKP.Address(), newPoolKP.Address(), mcmsKP.Address()

	// Onboarding handoff: set_admin(oldPool) demotes the deployer OUT of the
	// minters set entirely (admin() == MINTERS[0] == the old pool).
	if err := client.SetAdmin(ctx, oldPool); err != nil {
		t.Fatalf("SetAdmin(oldPool): %v", err)
	}
	if minters, err := client.GetMinters(ctx); err != nil || len(minters) != 1 || minters[0] != oldPool {
		t.Fatalf("GetMinters after handoff: want [oldPool %s], got %v (err=%v)", oldPool, minters, err)
	}

	// No-faucet at the new state: the demoted deployer can neither mint (the
	// stored primary is the old pool) nor mint_as itself (membership).
	if err := client.Mint(ctx, deployerAddr, big.NewInt(1)); err == nil {
		t.Fatal("post-handoff Mint by the deployer must fail — LINK has no local faucet")
	} else {
		t.Logf("post-handoff Mint by the deployer correctly rejected: %v", err)
	}
	if err := client.MintAs(ctx, deployerAddr, deployerAddr, big.NewInt(1)); err == nil {
		t.Fatal("post-handoff MintAs(deployer, …) must fail — the deployer is not a minter")
	} else {
		t.Logf("post-handoff MintAs(deployer, …) correctly rejected: %v", err)
	}

	// Migration overlap: the owner adds newPool as a secondary minter — both
	// pools are minters during the window.
	if err := client.AddMinter(ctx, newPool); err != nil {
		t.Fatalf("AddMinter(newPool): %v", err)
	}
	if minters, err := client.GetMinters(ctx); err != nil || len(minters) != 2 || minters[0] != oldPool || minters[1] != newPool {
		t.Fatalf("GetMinters during overlap: want [oldPool, newPool] = [%s, %s], got %v (err=%v)", oldPool, newPool, minters, err)
	}

	// remove_minter refuses the primary (CannotRemovePrimaryMinter) but
	// removes a secondary fine.
	if err := client.RemoveMinter(ctx, oldPool); err == nil {
		t.Fatal("RemoveMinter(oldPool) must fail — the primary must be replaced via set_admin, not removed")
	} else {
		t.Logf("RemoveMinter(oldPool) correctly rejected: %v", err)
	}
	if err := client.RemoveMinter(ctx, newPool); err != nil {
		t.Fatalf("RemoveMinter(newPool): %v", err)
	}
	if err := client.AddMinter(ctx, newPool); err != nil { // re-add for the re-point below
		t.Fatalf("AddMinter(newPool) re-add: %v", err)
	}

	// Re-point: set_admin(newPool) repositions the secondary to primary and
	// removes the old pool from the set in the SAME operation — no separate
	// remove_minter(oldPool) (it would trap MinterNotFound).
	if err := client.SetAdmin(ctx, newPool); err != nil {
		t.Fatalf("SetAdmin(newPool): %v", err)
	}
	if minters, err := client.GetMinters(ctx); err != nil || len(minters) != 1 || minters[0] != newPool {
		t.Fatalf("GetMinters after re-point: want [newPool %s], got %v (err=%v)", newPool, minters, err)
	}
	if admin, err := client.Admin(ctx); err != nil || admin != newPool {
		t.Fatalf("Admin after re-point: want %s, got %s (err=%v)", newPool, admin, err)
	}
	// The old pool lost membership with the re-point: mint_as(oldPool, …) is
	// rejected on membership even though the transaction sender signs.
	if err := client.MintAs(ctx, oldPool, deployerAddr, big.NewInt(1)); err == nil {
		t.Fatal("MintAs(oldPool, …) after re-point must fail — the old pool is no longer a minter")
	} else {
		t.Logf("MintAs(oldPool, …) after re-point correctly rejected: %v", err)
	}

	// Requirement-2 optional leg: the deployer MAY hand token ownership to
	// MCMS via the two-step transfer. Acceptance needs MCMS's own auth (an
	// external transaction cannot provide it), so externally this drives
	// propose + read + cancel; the owner stays the deployer throughout.
	if err := client.TransferOwnership(ctx, mcms); err != nil {
		t.Fatalf("TransferOwnership(mcms): %v", err)
	}
	if pending, err := client.GetPendingOwner(ctx); err != nil || pending == nil || *pending != mcms {
		t.Fatalf("GetPendingOwner: want %s, got %v (err=%v)", mcms, pending, err)
	}
	if err := client.CancelOwnershipTransfer(ctx); err != nil {
		t.Fatalf("CancelOwnershipTransfer: %v", err)
	}
	if pending, err := client.GetPendingOwner(ctx); err != nil || pending != nil {
		t.Fatalf("GetPendingOwner after cancel: want nil, got %v (err=%v)", pending, err)
	}
	if owner, err := client.Owner(ctx); err != nil || owner == nil || *owner != deployerAddr {
		t.Fatalf("Owner after cancel: want deployer %s, got %v (err=%v)", deployerAddr, owner, err)
	}
}
