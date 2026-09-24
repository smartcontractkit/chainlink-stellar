//go:build integration

// E2E (local Soroban sandbox) coverage for the AdvancedPoolHooks contract — the
// third layer of coverage on top of the Rust unit tests (src/test.rs) and the
// Rust pool-integration tests (burn-mint-pool/src/test.rs). This deploys the
// REAL release WASM to the shared Stellar quickstart container and exercises the
// contract through the generated Go binding, then wires it onto a real pool to
// prove the pool->hooks delegation end-to-end on-network.
//
// PREREQUISITE — the advanced_pool_hooks Go binding does not exist until it is
// generated (it is intentionally NOT committed hand-written). To enable this
// test, in an environment with the `stellar` CLI run:
//
//	make build
//	make generate-interfaces          # writes contracts/common/interfaces/src/advanced_pool_hooks.rs
//	# then add `pub mod advanced_pool_hooks;` to contracts/common/interfaces/src/lib.rs
//	make generate-bindings            # writes bindings/contracts/advanced_pool_hooks/{client.go,types.go}
//
// After that this file compiles under `-tags=integration` and runs via
// `make test-integration`. It is gated behind the `integration` build tag, so it
// does not affect the default `go build`/`go test`/`go vet` flows until the
// binding exists. Method/field names below follow the generator's snake_case ->
// PascalCase convention and must be reconciled against the generated client if
// they ever diverge.
package integration

import (
	"context"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	advancedpoolhooksbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/advanced_pool_hooks"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// remoteChain mirrors the Rust tests' DEFAULT_REMOTE_CHAIN.
const aphRemoteChain = uint64(5009297550715157269)

// deployHooks deploys the AdvancedPoolHooks WASM and returns a client + id.
func deployHooks(ctx context.Context, t *testing.T, projectRoot, deployerAddr string, deployer *deployment.Deployer, saltSuffix string) (*advancedpoolhooksbindings.AdvancedPoolHooksClient, string) {
	t.Helper()
	wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "pools_advanced_pool_hooks.wasm")
	salt := deployment.GenerateDeterministicSalt(deployerAddr, "test-advanced-pool-hooks-"+saltSuffix)
	contractID, err := deployer.DeployContract(ctx, wasmPath, salt)
	if err != nil {
		t.Fatalf("Deploy AdvancedPoolHooks: %v", err)
	}
	return advancedpoolhooksbindings.NewAdvancedPoolHooksClient(deployer, contractID), contractID
}

func TestAdvancedPoolHooks(t *testing.T) {
	// WASM deploys + RPC against the local quickstart sandbox. Other integration
	// tests use 5-10m; this exercises a single lightweight contract plus one pool
	// wiring, so 10m is comfortable headroom.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, _, _, _ := GetSharedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	t.Run("deploy, initialize, and verify readers", func(t *testing.T) {
		client, _ := deployHooks(ctx, t, projectRoot, deployerAddr, deployer, "readers")
		// No allowlist, no threshold.
		if err := client.Initialize(ctx, deployerAddr, nil, big.NewInt(0)); err != nil {
			t.Fatalf("Initialize: %v", err)
		}

		tv, err := client.TypeAndVersion(ctx)
		if err != nil {
			t.Fatalf("TypeAndVersion: %v", err)
		}
		if want := "AdvancedPoolHooks 2.0.0-dev"; tv != want {
			t.Fatalf("type_and_version: want %q, got %q", want, tv)
		}

		if enabled, err := client.GetAllowlistEnabled(ctx); err != nil || enabled {
			t.Fatalf("allowlist should be disabled when initialized empty, got enabled=%v err=%v", enabled, err)
		}
		threshold, err := client.GetThresholdAmount(ctx)
		if err != nil || threshold.Cmp(big.NewInt(0)) != 0 {
			t.Fatalf("threshold: want 0, got %v err=%v", threshold, err)
		}

		// Unconfigured chain -> no pool CCVs, fall back to lane defaults.
		v, err := client.GetRequiredCcvs(ctx, deployerAddr, aphRemoteChain, big.NewInt(100), 0, nil, advancedpoolhooksbindings.MessageDirectionOutbound)
		if err != nil {
			t.Fatalf("GetRequiredCcvs: %v", err)
		}
		if len(v.Ccvs) != 0 || !v.IncludeDefaults {
			t.Fatalf("unconfigured chain: want {[], include_defaults=true}, got %+v", v)
		}
	})

	t.Run("apply ccv config and read back through get_required_ccvs", func(t *testing.T) {
		client, _ := deployHooks(ctx, t, projectRoot, deployerAddr, deployer, "ccv-config")
		if err := client.Initialize(ctx, deployerAddr, nil, big.NewInt(0)); err != nil {
			t.Fatalf("Initialize: %v", err)
		}

		ccvA := helpers.GenerateMockContractID(t, deployerAddr, "aph-ccv-a")
		ccvB := helpers.GenerateMockContractID(t, deployerAddr, "aph-ccv-b")
		if err := client.ApplyCcvConfigUpdates(ctx, []advancedpoolhooksbindings.CCVConfigArg{
			{
				RemoteChainSelector:     aphRemoteChain,
				OutboundCcvs:            []string{ccvA, ccvB},
				OutboundIncludeDefaults: false,
				InboundIncludeDefaults:  true,
			},
		}); err != nil {
			t.Fatalf("ApplyCcvConfigUpdates: %v", err)
		}

		cfg, err := client.GetCcvConfig(ctx, aphRemoteChain)
		if err != nil {
			t.Fatalf("GetCcvConfig: %v", err)
		}
		if cfg == nil || len(cfg.OutboundCcvs) != 2 {
			t.Fatalf("stored config: want 2 outbound ccvs, got %+v", cfg)
		}

		all, err := client.GetAllCcvConfigs(ctx)
		if err != nil || len(all) != 1 {
			t.Fatalf("GetAllCcvConfigs: want 1, got %d err=%v", len(all), err)
		}

		out, err := client.GetRequiredCcvs(ctx, deployerAddr, aphRemoteChain, big.NewInt(100), 0, nil, advancedpoolhooksbindings.MessageDirectionOutbound)
		if err != nil {
			t.Fatalf("GetRequiredCcvs outbound: %v", err)
		}
		if len(out.Ccvs) != 2 || out.Ccvs[0] != ccvA || out.Ccvs[1] != ccvB || out.IncludeDefaults {
			t.Fatalf("outbound: want [a,b] include_defaults=false, got %+v", out)
		}

		// Inbound direction has no configured list -> empty + relayed include_defaults=true.
		in, err := client.GetRequiredCcvs(ctx, deployerAddr, aphRemoteChain, big.NewInt(100), 0, nil, advancedpoolhooksbindings.MessageDirectionInbound)
		if err != nil {
			t.Fatalf("GetRequiredCcvs inbound: %v", err)
		}
		if len(in.Ccvs) != 0 || !in.IncludeDefaults {
			t.Fatalf("inbound: want {[], include_defaults=true}, got %+v", in)
		}
	})

	t.Run("threshold amount appends additional ccvs at or above threshold", func(t *testing.T) {
		client, _ := deployHooks(ctx, t, projectRoot, deployerAddr, deployer, "threshold")
		// threshold_amount = 1000 configured up front.
		if err := client.Initialize(ctx, deployerAddr, nil, big.NewInt(1000)); err != nil {
			t.Fatalf("Initialize: %v", err)
		}

		base := helpers.GenerateMockContractID(t, deployerAddr, "aph-thr-base")
		extra := helpers.GenerateMockContractID(t, deployerAddr, "aph-thr-extra")
		if err := client.ApplyCcvConfigUpdates(ctx, []advancedpoolhooksbindings.CCVConfigArg{
			{
				RemoteChainSelector:     aphRemoteChain,
				OutboundCcvs:            []string{base},
				ThresholdOutboundCcvs:   []string{extra},
				OutboundIncludeDefaults: false,
				InboundIncludeDefaults:  true,
			},
		}); err != nil {
			t.Fatalf("ApplyCcvConfigUpdates: %v", err)
		}

		below, err := client.GetRequiredCcvs(ctx, deployerAddr, aphRemoteChain, big.NewInt(500), 0, nil, advancedpoolhooksbindings.MessageDirectionOutbound)
		if err != nil {
			t.Fatalf("GetRequiredCcvs below: %v", err)
		}
		if len(below.Ccvs) != 1 || below.Ccvs[0] != base {
			t.Fatalf("below threshold: want [base], got %+v", below)
		}

		at, err := client.GetRequiredCcvs(ctx, deployerAddr, aphRemoteChain, big.NewInt(1000), 0, nil, advancedpoolhooksbindings.MessageDirectionOutbound)
		if err != nil {
			t.Fatalf("GetRequiredCcvs at: %v", err)
		}
		if len(at.Ccvs) != 2 || at.Ccvs[0] != base || at.Ccvs[1] != extra {
			t.Fatalf("at threshold: want [base, extra], got %+v", at)
		}
	})

	t.Run("sender allowlist gates preflight_check", func(t *testing.T) {
		client, _ := deployHooks(ctx, t, projectRoot, deployerAddr, deployer, "allowlist")
		allowed := helpers.GenerateMockContractID(t, deployerAddr, "aph-allowed-sender")
		stranger := helpers.GenerateMockContractID(t, deployerAddr, "aph-stranger-sender")
		// Non-empty allowlist at init -> enabled (immutable thereafter).
		if err := client.Initialize(ctx, deployerAddr, []string{allowed}, big.NewInt(0)); err != nil {
			t.Fatalf("Initialize: %v", err)
		}

		if enabled, err := client.GetAllowlistEnabled(ctx); err != nil || !enabled {
			t.Fatalf("allowlist should be enabled, got enabled=%v err=%v", enabled, err)
		}
		got, err := client.GetAllowlist(ctx)
		if err != nil || len(got) != 1 || got[0] != allowed {
			t.Fatalf("GetAllowlist: want [allowed], got %+v err=%v", got, err)
		}

		// Allowlisted original_sender -> preflight passes.
		if err := client.PreflightCheck(ctx, advancedpoolhooksbindings.LockOrBurnIn{
			OriginalSender:      allowed,
			RemoteChainSelector: aphRemoteChain,
			Amount:              big.NewInt(100),
		}, 0, nil, big.NewInt(100)); err != nil {
			t.Fatalf("PreflightCheck allowlisted: %v", err)
		}

		// Non-allowlisted original_sender -> preflight aborts (#49 SenderNotAllowed).
		if err := client.PreflightCheck(ctx, advancedpoolhooksbindings.LockOrBurnIn{
			OriginalSender:      stranger,
			RemoteChainSelector: aphRemoteChain,
			Amount:              big.NewInt(100),
		}, 0, nil, big.NewInt(100)); err == nil {
			t.Fatal("PreflightCheck non-allowlisted: expected error, got nil")
		}
	})

	t.Run("pool delegates get_required_ccvs to a wired hooks contract", func(t *testing.T) {
		// Deploy a generic token pool (lock-release WASM via the token_pool binding,
		// same pattern as TestTokenPool) and point it at a real hooks contract.
		poolWasm := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "pools_lock_release_pool.wasm")
		poolSalt := deployment.GenerateDeterministicSalt(deployerAddr, "aph-wired-pool")
		poolID, err := deployer.DeployContract(ctx, poolWasm, poolSalt)
		if err != nil {
			t.Fatalf("Deploy pool: %v", err)
		}
		mockToken := helpers.GenerateMockContractID(t, deployerAddr, "aph-pool-token")
		mockRouter := helpers.GenerateMockContractID(t, deployerAddr, "aph-pool-router")
		mockRampRegistry := helpers.GenerateMockContractID(t, deployerAddr, "aph-pool-ramp-registry")
		mockRmnProxy := helpers.GenerateMockContractID(t, deployerAddr, "aph-pool-rmn-proxy")
		pool := tokenpoolbindings.NewTokenPoolClient(deployer, poolID)
		if err := pool.Initialize(ctx, deployerAddr, mockToken, 7, mockRouter, mockRampRegistry, mockRmnProxy); err != nil {
			t.Fatalf("Initialize pool: %v", err)
		}

		hooksClient, hooksID := deployHooks(ctx, t, projectRoot, deployerAddr, deployer, "wired")
		if err := hooksClient.Initialize(ctx, deployerAddr, nil, big.NewInt(0)); err != nil {
			t.Fatalf("Initialize hooks: %v", err)
		}
		if err := pool.SetAdvancedPoolHooks(ctx, hooksID); err != nil {
			t.Fatalf("SetAdvancedPoolHooks: %v", err)
		}

		ccv := helpers.GenerateMockContractID(t, deployerAddr, "aph-wired-ccv")
		if err := hooksClient.ApplyCcvConfigUpdates(ctx, []advancedpoolhooksbindings.CCVConfigArg{
			{
				RemoteChainSelector:     aphRemoteChain,
				OutboundCcvs:            []string{ccv},
				OutboundIncludeDefaults: false,
				InboundIncludeDefaults:  true,
			},
		}); err != nil {
			t.Fatalf("ApplyCcvConfigUpdates: %v", err)
		}

		// The pool's get_required_ccvs must delegate to the hooks and return the
		// issuer-configured CCV (not a mock), proving the wiring end-to-end.
		v, err := pool.GetRequiredCcvs(ctx, mockToken, aphRemoteChain, big.NewInt(100), 0, nil, tokenpoolbindings.MessageDirectionOutbound)
		if err != nil {
			t.Fatalf("pool.GetRequiredCcvs: %v", err)
		}
		if len(v.Ccvs) != 1 || v.Ccvs[0] != ccv || v.IncludeDefaults {
			t.Fatalf("pool did not delegate to hooks: want [ccv] include_defaults=false, got %+v", v)
		}
	})
}
