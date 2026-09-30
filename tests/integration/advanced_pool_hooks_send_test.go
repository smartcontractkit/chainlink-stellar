//go:build integration

// E2E (local Soroban sandbox) coverage for the AdvancedPoolHooks CCV-selection
// flow on the SEND and EXECUTE paths — the Go analogues of the Rust
// `test_special_ccv_augments_defaults_and_coexists_with_default_only_token`
// (onramp) and the siloed-pool `get_required_ccvs_real_advanced_pool_hooks`
// tests. Two gaps from the CCV-7 coverage audit, now closed:
//
//   - Gap 3 (outbound): a token issuer wires AdvancedPoolHooks onto their pool in
//     AUGMENT mode (the issuer's own special CCV + the lane defaults). A ccip_send
//     for that token must route through [special, default]; a ccip_send for a
//     second, hooks-less token on the same lane must route through [default] only.
//     Both coexist on one lane, each against its own CCV set.
//   - Gap 6 (inbound): an inbound token transfer for a pool whose AdvancedPoolHooks
//     INBOUND config requires a special CCV. The OffRamp's pool consult
//     (`get_inbound_pool_required_ccvs` → pool.get_required_ccvs(Inbound) → hooks)
//     must fold that special CCV into the required set, so execute FAILS
//     (RequiredCCVMissing #116) when it is not supplied inline and SUCCEEDS when it
//     is — proving the pool, not the lane, dictates inbound verification.
//
// PREREQUISITE — same as advanced_pool_hooks_test.go: the
// advanced_pool_hooks Go binding must be generated first (`make build` →
// `make generate-interfaces` + add `pub mod advanced_pool_hooks;` to
// contracts/common/interfaces/src/lib.rs → `make generate-bindings`). Gated behind
// the `integration` build tag, so default go build/test/vet are unaffected until then.
package integration

import (
	"bytes"
	"context"
	"encoding/binary"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	advancedpoolhooksbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/advanced_pool_hooks"
	lockreleasepoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/lock_release_pool"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tokenlockboxbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_lock_box"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
)

// receiptIssuers flattens an OnRamp CCIPMessageSent receipt list into its issuer
// addresses (order-preserving) for membership assertions. The merged CCV list may
// order the special CCV and lane default differently than the config, so asserts
// use membership rather than strict index equality.
func receiptIssuers(rcpts []onrampbindings.Receipt) []string {
	out := make([]string, len(rcpts))
	for i, r := range rcpts {
		out[i] = r.Issuer
	}
	return out
}

// deployAdditionalLockReleasePool deploys a second lock-release pool + lockbox for
// tokenID, reusing the stack's existing TokenAdminRegistry and RampRegistry (both
// created by the first deployTokenPool call). This lets two pools share one
// RampRegistry so the OnRamp that deployOutboundSendWire registers into it is
// visible to both pools' require_authorized_onramp → get_onramp(dest) checks.
//
// Call AFTER a first deployTokenPool (which seeds the shared TAR + RampRegistry)
// and capture the first pool's ID/client first — this overwrites
// stack.TokenPoolID/Client/LockBoxID/LockReleasePoolClient with the new pool's.
func (s *fullStack) deployAdditionalLockReleasePool(
	ctx context.Context,
	t *testing.T,
	projectRoot string,
	deployer *deployment.Deployer,
	deployerAddr, saltPrefix string,
	tokenID string,
) {
	t.Helper()
	if s.TokenAdminRegistryID == "" || s.RampRegistryID == "" {
		t.Fatal("deployAdditionalLockReleasePool requires a prior deployTokenPool (shared TAR + RampRegistry)")
	}

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

	s.TokenPoolID = deploy("lock-release-pool", "pools_lock_release_pool.wasm")
	s.TokenPoolClient = tokenpoolbindings.NewTokenPoolClient(deployer, s.TokenPoolID)
	s.LockReleasePoolClient = lockreleasepoolbindings.NewLockReleasePoolClient(deployer, s.TokenPoolID)

	const tokenPoolDecimals uint32 = 7
	// The lockbox is fixed at pool `initialize` (EVM `i_lockBox` constructor
	// parity): stand it up first and pass its address into the pool's
	// `initialize` (8th arg, lock-release-specific entrypoint).
	s.LockBoxID = deploy("token-lock-box", "pools_token_lock_box.wasm")
	s.LockBoxClient = tokenlockboxbindings.NewTokenLockBoxClient(deployer, s.LockBoxID)
	if err := s.LockBoxClient.Initialize(ctx, deployerAddr, tokenID); err != nil {
		t.Fatalf("TokenLockBox Initialize (additional): %v", err)
	}
	if err := s.LockReleasePoolClient.Initialize(ctx, deployerAddr, tokenID, tokenPoolDecimals, s.RouterID, s.RampRegistryID, s.RmnProxyID, s.LockBoxID); err != nil {
		t.Fatalf("LockReleasePool Initialize (additional): %v", err)
	}
	if err := s.LockBoxClient.AddAllowedCallers(ctx, []string{s.TokenPoolID}); err != nil {
		t.Fatalf("TokenLockBox AddAllowedCallers (additional): %v", err)
	}

	// Two-step admin registration in the SHARED registry (already deployed by the
	// first pool): propose deployer, accept, map token → this pool.
	if err := s.TokenAdminRegistryClient.ProposeAdministrator(ctx, deployerAddr, tokenID, deployerAddr); err != nil {
		t.Fatalf("TokenAdminRegistry ProposeAdministrator (additional): %v", err)
	}
	if err := s.TokenAdminRegistryClient.AcceptAdminRole(ctx, tokenID); err != nil {
		t.Fatalf("TokenAdminRegistry AcceptAdminRole (additional): %v", err)
	}
	if err := s.TokenAdminRegistryClient.SetPool(ctx, tokenID, &s.TokenPoolID); err != nil {
		t.Fatalf("TokenAdminRegistry SetPool (additional): %v", err)
	}
}

// TestAdvancedPoolHooksOutboundSend closes gap 3: a token issuer enforces their
// OWN special CCV (augmented with lane defaults) for their pool, while a second
// hooks-less token on the same lane uses only the lane default — both via the real
// Router → OnRamp → pool → hooks → merge → receipts path on-network.
func TestAdvancedPoolHooksOutboundSend(t *testing.T) {
	// Two full sends (hooked-augment token + default-only token) over one stack +
	// one outbound wire. 10m matches the other full-stack integration tests.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, networkPassphrase, friendbotURL := GetSharedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	const localSourceChain = uint64(11111)
	const remoteDestChain = uint64(22222)
	const saltPrefix = "aph-outbound"

	// offrampUsesDeployedTokenAdminRegistry=false: deployTokenPool deploys its own
	// TAR (mirrors TestOnRampFeeDistribution's outbound stack).
	stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localSourceChain, saltPrefix, false)

	feeToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, saltPrefix+"-fee")
	hookToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, saltPrefix+"-hook")
	defaultToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, saltPrefix+"-default")

	// Pool #1 (hooked token) FIRST — deployTokenPool creates the stack's shared
	// RampRegistry (and sets stack.RampRegistryClient), while deployOutboundSendWire
	// only registers the OnRamp into it when that client is already non-nil. Capture
	// pool #1's ID/client before deploying pool #2 (which overwrites the stack fields).
	stack.deployTokenPool(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix+"-hook-pool", hookToken)
	hookPoolID := stack.TokenPoolID
	hookPoolClient := stack.TokenPoolClient

	// Pool #2 (default-only token) on the SHARED TAR + RampRegistry.
	stack.deployAdditionalLockReleasePool(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix+"-default-pool", defaultToken)
	defaultPoolID := stack.TokenPoolID
	defaultPoolClient := stack.TokenPoolClient

	// --- Wire AdvancedPoolHooks on pool #1 in OUTBOUND AUGMENT mode ---
	hooksClient, hooksID := deployHooks(ctx, t, projectRoot, deployerAddr, deployer, "outbound-hook")
	// Owner = deployer (the issuer/pool-owner). Authorized callers = the pool itself
	// (the pool is the immediate caller of preflight/postflight; EVM `_validateCaller`).
	if err := hooksClient.Initialize(ctx, deployerAddr, nil, big.NewInt(0), []string{hookPoolID}, nil); err != nil {
		t.Fatalf("hooks Initialize: %v", err)
	}
	if err := hookPoolClient.SetAdvancedPoolHooks(ctx, hooksID); err != nil {
		t.Fatalf("pool SetAdvancedPoolHooks: %v", err)
	}

	// Special CCV = a second VVR resolving to the stack's committee verifier, so the
	// existing signer can attest it AND OnRamp get_fee can price it (VVR →
	// get_outbound_implementation → committee verifier's RemoteChainConfig). The lane
	// default (stack.VvrID) already resolves there; ccvB needs its OWN outbound impl
	// mapping (deploySecondVvr only wires the inbound impl).
	ccvB := deploySecondVvr(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix, stack.CcvID)
	ccvBClient := vvrbindings.NewVersionedVerifierResolverClient(deployer, ccvB)
	ccvImpl := stack.CcvID
	if err := ccvBClient.ApplyOutboundImplUpdates(ctx, []vvrbindings.OutboundImplementationUpdate{
		{DestChainSelector: remoteDestChain, Verifier: &ccvImpl},
	}); err != nil {
		t.Fatalf("ccvB ApplyOutboundImplUpdates: %v", err)
	}

	// AUGMENT: issuer's special CCV + lane defaults (include_defaults=true). On the
	// outbound merge, a token-only send with empty user CCVs yields
	// [ccvB, VvrID] (pool ccvs first, then defaults) — exactly the executor's
	// max_ccvs_per_msg=2 cap (deployOutboundSendWire), so no executor re-provisioning
	// is needed (unlike the Rust test, whose lane carries TWO defaults).
	if err := hooksClient.ApplyCcvConfigUpdates(ctx, []advancedpoolhooksbindings.CCVConfigArg{{
		RemoteChainSelector:     remoteDestChain,
		OutboundCcvs:            []string{ccvB},
		OutboundIncludeDefaults: true,
		InboundIncludeDefaults:  true,
	}}); err != nil {
		t.Fatalf("hooks ApplyCcvConfigUpdates: %v", err)
	}

	// One wire priced for fee + both transfer tokens (FeeQuoter prices each + applies
	// its TokenTransferFeeConfig, the pool-fee slice that shows up as a pool receipt).
	wire := deployOutboundSendWire(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix, stack,
		localSourceChain, remoteDestChain, feeToken, []string{hookToken, defaultToken})

	// Remote pool/token bytes for each pool's ApplyChainUpdates (rate limits zeroed).
	remotePool := make([]byte, 20)
	remoteTok := make([]byte, 20)
	for i := range remotePool {
		remotePool[i] = 0x11
		remoteTok[i] = 0x22
	}
	chainUpdate := func() []tokenpoolbindings.ChainUpdate {
		return []tokenpoolbindings.ChainUpdate{{
			RemoteChainSelector:       remoteDestChain,
			RemotePoolAddresses:       [][]byte{remotePool},
			RemoteTokenAddress:        remoteTok,
			OutboundRateLimiterConfig: tokenpoolbindings.RateLimitConfig{},
			InboundRateLimiterConfig:  tokenpoolbindings.RateLimitConfig{},
		}}
	}
	if err := hookPoolClient.ApplyChainUpdates(ctx, chainUpdate(), nil); err != nil {
		t.Fatalf("hook pool ApplyChainUpdates: %v", err)
	}
	if err := defaultPoolClient.ApplyChainUpdates(ctx, chainUpdate(), nil); err != nil {
		t.Fatalf("default pool ApplyChainUpdates: %v", err)
	}

	executorID := stack.ExecutorID
	routerID := stack.RouterID
	vvrID := stack.VvrID

	// Empty user CCVs (token-only ⇒ user-fallback defaults are skipped, so the pool
	// hooks + lane defaults fully drive the merged CCV set). Executor is the lane
	// default; GasLimit 0 ⇒ token-only (H-10: receiver not consulted, not needed
	// outbound).
	buildExtraArgs := func() []byte {
		extraArgs, err := encodeOnrampExtraArgsV3(onrampbindings.GenericExtraArgsV3{
			Ccvs:               nil, // pool hooks + lane defaults drive the merge
			CcvArgs:            nil,
			Executor:           executorID,
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
	const tokenTransferAmount = int64(1_000_000) // 0.1 INTG at 7 decimals

	// sendAndWait runs Router.ccip_send for msg and returns the CCIPMessageSent
	// receipts (the fully merged CCV plan + pool/executor/network receipts).
	sendAndWait := func(t *testing.T, msg routerbindings.StellarToAnyMessage) []onrampbindings.Receipt {
		t.Helper()
		requiredFee, err := stack.RouterClient.GetFee(ctx, remoteDestChain, msg)
		if err != nil {
			t.Fatalf("Router GetFee: %v", err)
		}
		if requiredFee.Sign() <= 0 {
			t.Fatalf("expected positive fee, got %s", requiredFee.String())
		}
		latest, err := rpcClient.GetLatestLedger(ctx)
		if err != nil {
			t.Fatalf("GetLatestLedger: %v", err)
		}
		msgID, err := stack.RouterClient.CcipSend(ctx, deployerAddr, remoteDestChain, msg, requiredFee)
		if err != nil {
			t.Fatalf("Router CcipSend: %v", err)
		}
		if msgID == ([32]byte{}) {
			t.Fatal("CcipSend returned empty message_id")
		}
		sentEvt, err := wire.OnRampClient.WaitForCCIPMessageSentEvent(ctx, latest.Sequence, 30*time.Second,
			func(e *onrampbindings.CCIPMessageSentEvent) bool {
				return e.DestChainSelector == remoteDestChain && bytes.Equal(e.MessageId[:], msgID[:])
			})
		if err != nil {
			t.Fatalf("WaitForCCIPMessageSentEvent: %v", err)
		}
		return sentEvt.Receipts
	}

	// ------------------------------------------------------------------
	// 1. Hooked token (AUGMENT): merged CCVs = [ccvB, VvrID] → 5 receipts
	//    [ccvB, VvrID, pool, executor, network]. The issuer's special CCV is
	//    enforced on top of the lane default.
	// ------------------------------------------------------------------
	t.Run("hooked token send routes through special CCV augmented with lane default", func(t *testing.T) {
		msg := routerbindings.StellarToAnyMessage{
			Receiver:     evmReceiver,
			Data:         nil, // token-only
			FeeToken:     feeToken,
			ExtraArgs:    buildExtraArgs(),
			TokenAmounts: []routerbindings.TokenAmount{{Token: hookToken, Amount: big.NewInt(tokenTransferAmount)}},
		}
		rcpts := sendAndWait(t, msg)

		// [ccvB, VvrID, hookPool, executor, network] = 5.
		const wantReceipts = 5
		if len(rcpts) != wantReceipts {
			t.Fatalf("receipts: want %d ([ccvB, VvrID, pool, executor, network]), got %d: %+v",
				wantReceipts, len(rcpts), receiptIssuers(rcpts))
		}
		issuers := receiptIssuers(rcpts)
		if !contains(issuers, ccvB) {
			t.Errorf("receipts must contain the issuer's special CCV %s, got %v", ccvB, issuers)
		}
		if !contains(issuers, vvrID) {
			t.Errorf("receipts must contain the lane default CCV %s (augment), got %v", vvrID, issuers)
		}
		if !contains(issuers, hookPoolID) {
			t.Errorf("receipts must contain the hook pool %s, got %v", hookPoolID, issuers)
		}
		if !contains(issuers, executorID) {
			t.Errorf("receipts must contain the executor %s, got %v", executorID, issuers)
		}
		if !contains(issuers, routerID) {
			t.Errorf("receipts must contain the router (network) %s, got %v", routerID, issuers)
		}
		for i, r := range rcpts {
			if r.FeeTokenAmount == nil || r.FeeTokenAmount.Sign() <= 0 {
				t.Errorf("receipt[%d] fee_token_amount must be > 0, got %v (issuer=%s)", i, r.FeeTokenAmount, r.Issuer)
			}
		}
		t.Logf("hooked token: merged CCVs [ccvB, VvrID] + pool + executor + network = %d receipts", len(rcpts))
	})

	// ------------------------------------------------------------------
	// 2. Default-only token (NO hooks) on the same lane: merged CCVs = [VvrID]
	//    → 4 receipts [VvrID, pool, executor, network]. The issuer's special
	//    CCV is NOT required for this token (gap 1 coexistence: each token uses
	//    its own CCV set in its own message).
	// ------------------------------------------------------------------
	t.Run("default-only token send routes through lane default only, not the special CCV", func(t *testing.T) {
		msg := routerbindings.StellarToAnyMessage{
			Receiver:     evmReceiver,
			Data:         nil,
			FeeToken:     feeToken,
			ExtraArgs:    buildExtraArgs(),
			TokenAmounts: []routerbindings.TokenAmount{{Token: defaultToken, Amount: big.NewInt(tokenTransferAmount)}},
		}
		rcpts := sendAndWait(t, msg)

		// [VvrID, defaultPool, executor, network] = 4.
		const wantReceipts = 4
		if len(rcpts) != wantReceipts {
			t.Fatalf("receipts: want %d ([VvrID, pool, executor, network]), got %d: %+v",
				wantReceipts, len(rcpts), receiptIssuers(rcpts))
		}
		issuers := receiptIssuers(rcpts)
		if contains(issuers, ccvB) {
			t.Errorf("receipts must NOT contain the special CCV %s for a hooks-less token, got %v", ccvB, issuers)
		}
		if !contains(issuers, vvrID) {
			t.Errorf("receipts must contain the lane default CCV %s, got %v", vvrID, issuers)
		}
		if !contains(issuers, defaultPoolID) {
			t.Errorf("receipts must contain the default pool %s, got %v", defaultPoolID, issuers)
		}
		if !contains(issuers, executorID) {
			t.Errorf("receipts must contain the executor %s, got %v", executorID, issuers)
		}
		if !contains(issuers, routerID) {
			t.Errorf("receipts must contain the router (network) %s, got %v", routerID, issuers)
		}
		t.Logf("default-only token: merged CCVs [VvrID] + pool + executor + network = %d receipts (special CCV absent)", len(rcpts))
	})
}

// TestAdvancedPoolHooksInboundExecute closes gap 6: an inbound token transfer for a
// pool whose AdvancedPoolHooks INBOUND config requires a special CCV. The OffRamp
// consults the pool (get_required_ccvs Inbound → hooks) and folds that special CCV
// into the required set, so execute fails when it is missing and succeeds when it is
// supplied — proving the pool (not the lane) dictates inbound verification.
func TestAdvancedPoolHooksInboundExecute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, networkPassphrase, friendbotURL := GetSharedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	const localChain = uint64(11111)
	const saltPrefix = "aph-inbound"

	// offrampUsesDeployedTokenAdminRegistry=true: OffRamp resolves pools via the real
	// TAR (deployTokenPool registers the pool there), which get_inbound_pool_required_ccvs
	// relies on to find the pool for the inbound dest_token.
	stack := deployFullStack(ctx, t, projectRoot, deployer, deployerAddr, localChain, saltPrefix, true)
	sacToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, saltPrefix)
	stack.deployTokenPool(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix, sacToken)

	evmPool := bytes.Repeat([]byte{0x51}, 20)
	evmTok := bytes.Repeat([]byte{0x52}, 20)
	if err := stack.TokenPoolClient.ApplyChainUpdates(ctx, []tokenpoolbindings.ChainUpdate{{
		RemoteChainSelector:       remoteSourceChain,
		RemotePoolAddresses:       [][]byte{evmPool},
		RemoteTokenAddress:        evmTok,
		OutboundRateLimiterConfig: tokenpoolbindings.RateLimitConfig{},
		InboundRateLimiterConfig:  tokenpoolbindings.RateLimitConfig{},
	}}, nil); err != nil {
		t.Fatalf("TokenPool ApplyChainUpdates (inbound): %v", err)
	}

	// --- Wire AdvancedPoolHooks on the pool in INBOUND AUGMENT mode ---
	hooksClient, hooksID := deployHooks(ctx, t, projectRoot, deployerAddr, deployer, "inbound-hook")
	if err := hooksClient.Initialize(ctx, deployerAddr, nil, big.NewInt(0), []string{stack.TokenPoolID}, nil); err != nil {
		t.Fatalf("hooks Initialize: %v", err)
	}
	if err := stack.TokenPoolClient.SetAdvancedPoolHooks(ctx, hooksID); err != nil {
		t.Fatalf("pool SetAdvancedPoolHooks: %v", err)
	}

	// Special CCV = second VVR resolving to the committee verifier. deploySecondVvr
	// wires its INBOUND impl (what the OffRamp needs); the verifier blob is
	// CCV-agnostic (commits to version_tag ‖ message_hash), so the stack's single
	// signer attests both VvrID and ccvB with the same blob.
	ccvB := deploySecondVvr(ctx, t, projectRoot, deployer, deployerAddr, saltPrefix, stack.CcvID)
	if err := hooksClient.ApplyCcvConfigUpdates(ctx, []advancedpoolhooksbindings.CCVConfigArg{{
		RemoteChainSelector:     remoteSourceChain,
		InboundCcvs:             []string{ccvB},
		InboundIncludeDefaults:  true, // AUGMENT: special CCV + lane default → required = [ccvB, VvrID]
		OutboundIncludeDefaults: true,
	}}); err != nil {
		t.Fatalf("hooks ApplyCcvConfigUpdates: %v", err)
	}

	// buildInboundTokenMsg encodes a token-only inbound message (data=nil, gas=0) ⇒
	// the OffRamp token-only arm consults the POOL (not the receiver) for required
	// CCVs (H-10), isolating the pool-required special CCV.
	const releaseAmount = int64(2_000_000)
	buildInboundTokenMsg := func(t *testing.T, seqNo uint64) (encoded []byte, msgID [32]byte) {
		t.Helper()
		tokenXfer, err := EncodeCcipTokenTransferV1Inbound(releaseAmount, evmPool, evmTok, sacToken, stack.ReceiverID, nil)
		if err != nil {
			t.Fatalf("EncodeCcipTokenTransferV1Inbound: %v", err)
		}
		evmSender := bytes.Repeat([]byte{0xcd}, 20)
		var ccvZero [32]byte
		encoded, err = encodeCcipMessageV1(ccipV1Wire{
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
		msgID = keccak256MessageID(encoded)
		return encoded, msgID
	}

	// executeAndAwait runs OffRamp.execute (one CCV-agnostic blob per supplied CCV)
	// and returns the execution state + big-endian CCIPError code from the
	// ExecutionStateChangedEvent.return_data (0 for Success). Inner rejections
	// (missing required CCV, …) are swallowed into Failure and surface here as
	// (Failure, code), NOT as a Go error from Execute.
	executeAndAwait := func(t *testing.T, seqNo uint64, ccvs []string) (offrampbindings.MessageExecutionState, uint32) {
		t.Helper()
		encoded, msgID := buildInboundTokenMsg(t, seqNo)
		blobs := make([][]byte, len(ccvs))
		for i := range ccvs {
			blobs[i] = stack.signVerifierBlob(t, msgID)
		}
		latest, err := rpcClient.GetLatestLedger(ctx)
		if err != nil {
			t.Fatalf("GetLatestLedger: %v", err)
		}
		if err := stack.OfframpClient.Execute(ctx, encoded, ccvs, blobs, 0); err != nil {
			t.Fatalf("Execute (outer): %v", err)
		}
		evt, err := stack.OfframpClient.WaitForExecutionStateChangedEvent(ctx, latest.Sequence, 30*time.Second,
			func(e *offrampbindings.ExecutionStateChangedEvent) bool {
				return bytes.Equal(e.MessageId[:], msgID[:])
			})
		if err != nil {
			t.Fatalf("WaitForExecutionStateChangedEvent: %v", err)
		}
		var code uint32
		if evt.State == offrampbindings.MessageExecutionStateFailure && len(evt.ReturnData) >= 4 {
			code = binary.BigEndian.Uint32(evt.ReturnData[:4])
		}
		return evt.State, code
	}

	// ------------------------------------------------------------------
	// 1. Off-chain view: the pool consult folds the special CCV into required.
	//    GetCcvsForMessage (token-only arm) → required = [ccvB, VvrID].
	// ------------------------------------------------------------------
	t.Run("off-chain get_ccvs_for_message resolves pool-required special CCV (augment)", func(t *testing.T) {
		encoded, _ := buildInboundTokenMsg(t, 200) // seq unused by the view
		req, _, _, err := stack.OfframpClient.GetCcvsForMessage(ctx, encoded)
		if err != nil {
			t.Fatalf("GetCcvsForMessage: %v", err)
		}
		if !contains(req, ccvB) {
			t.Fatalf("view required must contain pool-required special CCV %s, got %v", ccvB, req)
		}
		if !contains(req, stack.VvrID) {
			t.Fatalf("view required must contain lane default %s (augment), got %v", stack.VvrID, req)
		}
		t.Logf("inbound view: required=%v (pool-required ccvB + lane default folded in)", req)
	})

	// ------------------------------------------------------------------
	// 2. Supply only the lane default VvrID → pool-required ccvB missing ⇒
	//    Failure(RequiredCCVMissing #116). The lane default does NOT satisfy a
	//    pool-required CCV — the pool, not the lane, dictates inbound verification.
	// ------------------------------------------------------------------
	t.Run("inbound execute fails when pool-required special CCV is not supplied (RequiredCCVMissing=116)", func(t *testing.T) {
		state, code := executeAndAwait(t, 1, []string{stack.VvrID}) // supply lane default, NOT ccvB
		if state != offrampbindings.MessageExecutionStateFailure {
			t.Fatalf("state: want Failure, got %d (code=%d)", state, code)
		}
		if code != offrampbindings.CCIPErrorRequiredCCVMissing {
			t.Fatalf("error code: want RequiredCCVMissing(%d), got %d", offrampbindings.CCIPErrorRequiredCCVMissing, code)
		}
		t.Logf("correctly failed: pool-required %s missing (lane default did not satisfy it)", ccvB)
	})

	// ------------------------------------------------------------------
	// 3. Supply [ccvB, VvrID] → quorum met → Success, and release_or_mint
	//    moves `releaseAmount` SAC from the lockbox to the receiver.
	// ------------------------------------------------------------------
	t.Run("inbound execute succeeds when pool-required special CCV is supplied", func(t *testing.T) {
		// L-4: release_or_mint withdraws from the lockbox — top it up to cover the release.
		lockboxBal := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.LockBoxID)
		if lockboxBal < releaseAmount {
			sacTransferOrFatal(ctx, t, deployer, sacToken, deployerAddr, stack.LockBoxID, releaseAmount-lockboxBal)
		}
		rcvBefore := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.ReceiverID)

		state, code := executeAndAwait(t, 2, []string{ccvB, stack.VvrID})
		if state != offrampbindings.MessageExecutionStateSuccess {
			t.Fatalf("state: want Success, got %d (code=%d)", state, code)
		}

		rcvAfter := sacBalanceOrFatal(ctx, t, deployer, sacToken, stack.ReceiverID)
		if got := rcvAfter - rcvBefore; got != releaseAmount {
			t.Fatalf("receiver SAC delta: want %d, got %d (before=%d after=%d)", releaseAmount, got, rcvBefore, rcvAfter)
		}
		t.Logf("inbound release_or_mint succeeded: moved %d SAC base units lockbox -> receiver (pool-required ccvB supplied)", releaseAmount)
	})
}
