//go:build integration

package integration

import (
	"context"
	"math/big"
	"path/filepath"
	"testing"

	rampregistrybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ramp_registry"
	slrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/siloed_lock_release_pool"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	tlbbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_lock_box"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/xdr"
)

const (
	siloedPoolDecimals     uint32 = 7
	lockBoxSeedLiquidity   int64  = 10_000_000 // 1 INTG at 7 decimals
	sacApproveLedgerBuffer uint32 = 1000
)

// siloedPoolAssets tracks siloed lock-release pool + token lock box wiring for migration tests.
type siloedPoolAssets struct {
	LockBoxID     string
	PoolID        string
	PoolClient    *slrbindings.SiloedLockReleasePoolClient
	LockBoxClient *tlbbindings.TokenLockBoxClient
}

// deploySiloedTokenPool deploys token lock box + siloed pool v1, seeds lock box liquidity,
// registers the pool in TokenAdminRegistry, and wires RampRegistry for the stack.
func (s *fullStack) deploySiloedTokenPool(
	ctx context.Context,
	t *testing.T,
	projectRoot string,
	deployer *deployment.Deployer,
	deployerAddr string,
	rpcClient *rpcclient.Client,
	saltPrefix string,
	tokenID string,
	remoteChainSelector uint64,
	remotePool, remoteToken []byte,
) *siloedPoolAssets {
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

	if s.TokenAdminRegistryID == "" {
		s.TokenAdminRegistryID = deploy("token-admin-registry", "token_admin_registry.wasm")
		s.TokenAdminRegistryClient = tarbindings.NewTokenAdminRegistryClient(deployer, s.TokenAdminRegistryID)
		if err := s.TokenAdminRegistryClient.Initialize(ctx, deployerAddr); err != nil {
			t.Fatalf("TokenAdminRegistry Initialize: %v", err)
		}
	} else if s.TokenAdminRegistryClient == nil {
		s.TokenAdminRegistryClient = tarbindings.NewTokenAdminRegistryClient(deployer, s.TokenAdminRegistryID)
	}

	lockBoxID := deploy("token-lock-box", "pools_token_lock_box.wasm")
	lockBoxClient := tlbbindings.NewTokenLockBoxClient(deployer, lockBoxID)
	if err := lockBoxClient.Initialize(ctx, deployerAddr, tokenID); err != nil {
		t.Fatalf("TokenLockBox Initialize: %v", err)
	}

	poolID := deploy("siloed-lock-release-pool", "pools_siloed_lock_release_pool.wasm")
	poolClient := slrbindings.NewSiloedLockReleasePoolClient(deployer, poolID)

	if s.RampRegistryID == "" {
		s.RampRegistryID = deploy("ramp-registry", "ccip_ramp_registry.wasm")
		s.RampRegistryClient = rampregistrybindings.NewRampRegistryClient(deployer, s.RampRegistryID)
		if err := s.RampRegistryClient.Initialize(ctx, deployerAddr); err != nil {
			t.Fatalf("RampRegistry Initialize: %v", err)
		}
	} else if s.RampRegistryClient == nil {
		s.RampRegistryClient = rampregistrybindings.NewRampRegistryClient(deployer, s.RampRegistryID)
	}

	if err := s.RampRegistryClient.ApplyOfframpUpdates(ctx, []rampregistrybindings.OffRampUpdate{
		{
			SourceChainSelector: remoteSourceChain,
			Offramp:             s.OfframpID,
			Enabled:             true,
		},
	}); err != nil {
		t.Fatalf("RampRegistry ApplyOfframpUpdates: %v", err)
	}

	if s.RouterID == "" {
		t.Fatal("fullStack.RouterID is empty; deployFullStack must run before deploySiloedTokenPool")
	}
	if err := poolClient.Initialize(ctx, deployerAddr, tokenID, siloedPoolDecimals, s.RouterID, s.RampRegistryID); err != nil {
		t.Fatalf("SiloedLockReleasePool Initialize: %v", err)
	}

	if err := lockBoxClient.AddAllowedCallers(ctx, []string{poolID, deployerAddr}); err != nil {
		t.Fatalf("TokenLockBox AddAllowedCallers: %v", err)
	}

	if err := poolClient.ApplyChainUpdates(ctx, []slrbindings.ChainUpdate{{
		RemoteChainSelector:       remoteChainSelector,
		RemotePoolAddresses:       remotePool,
		RemoteTokenAddress:        remoteToken,
		OutboundRateLimiterConfig: slrbindings.RateLimitConfig{},
		InboundRateLimiterConfig:  slrbindings.RateLimitConfig{},
	}}, nil); err != nil {
		t.Fatalf("SiloedLockReleasePool ApplyChainUpdates: %v", err)
	}

	if err := poolClient.ConfigureLockBoxes(ctx, []slrbindings.LockBoxEntry{{
		RemoteChainSelector: remoteChainSelector,
		LockBox:             lockBoxID,
	}}); err != nil {
		t.Fatalf("SiloedLockReleasePool ConfigureLockBoxes: %v", err)
	}

	sacApproveOrFatal(ctx, t, deployer, rpcClient, tokenID, deployerAddr, lockBoxID, lockBoxSeedLiquidity)
	if err := lockBoxClient.Deposit(ctx, deployerAddr, big.NewInt(lockBoxSeedLiquidity)); err != nil {
		t.Fatalf("TokenLockBox Deposit: %v", err)
	}

	if err := s.TokenAdminRegistryClient.ProposeAdministrator(ctx, deployerAddr, tokenID, deployerAddr); err != nil {
		t.Fatalf("TokenAdminRegistry ProposeAdministrator: %v", err)
	}
	if err := s.TokenAdminRegistryClient.AcceptAdminRole(ctx, tokenID); err != nil {
		t.Fatalf("TokenAdminRegistry AcceptAdminRole: %v", err)
	}
	if err := s.TokenAdminRegistryClient.SetPool(ctx, tokenID, &poolID); err != nil {
		t.Fatalf("TokenAdminRegistry SetPool: %v", err)
	}

	s.TokenPoolID = poolID
	s.TokenPoolClient = tokenpoolbindings.NewTokenPoolClient(deployer, poolID)

	return &siloedPoolAssets{
		LockBoxID:     lockBoxID,
		PoolID:        poolID,
		PoolClient:    poolClient,
		LockBoxClient: lockBoxClient,
	}
}

// migrateSiloedTokenPool deploys siloed pool v2, points it at the same lock box, cuts over TAR,
// and removes v1 from the lock box allowlist. Returns the new pool ID.
func migrateSiloedTokenPool(
	ctx context.Context,
	t *testing.T,
	projectRoot string,
	deployer *deployment.Deployer,
	deployerAddr string,
	stack *fullStack,
	assets *siloedPoolAssets,
	tokenID string,
	remoteChainSelector uint64,
	v2SaltSuffix string,
) string {
	t.Helper()

	oldPoolID := assets.PoolID
	remotePool, err := assets.PoolClient.GetRemotePool(ctx, remoteChainSelector)
	if err != nil {
		t.Fatalf("GetRemotePool from old pool: %v", err)
	}
	remoteToken, err := assets.PoolClient.GetRemoteToken(ctx, remoteChainSelector)
	if err != nil {
		t.Fatalf("GetRemoteToken from old pool: %v", err)
	}

	salt := deployment.GenerateDeterministicSalt(deployerAddr, v2SaltSuffix)
	wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "pools_siloed_lock_release_pool.wasm")
	newPoolID, err := deployer.DeployContract(ctx, wasmPath, salt)
	if err != nil {
		t.Fatalf("deploy siloed pool v2: %v", err)
	}
	newPoolClient := slrbindings.NewSiloedLockReleasePoolClient(deployer, newPoolID)

	if err := newPoolClient.Initialize(ctx, deployerAddr, tokenID, siloedPoolDecimals, stack.RouterID, stack.RampRegistryID); err != nil {
		t.Fatalf("SiloedLockReleasePool v2 Initialize: %v", err)
	}
	if err := newPoolClient.ApplyChainUpdates(ctx, []slrbindings.ChainUpdate{{
		RemoteChainSelector:       remoteChainSelector,
		RemotePoolAddresses:       remotePool,
		RemoteTokenAddress:        remoteToken,
		OutboundRateLimiterConfig: slrbindings.RateLimitConfig{},
		InboundRateLimiterConfig:  slrbindings.RateLimitConfig{},
	}}, nil); err != nil {
		t.Fatalf("SiloedLockReleasePool v2 ApplyChainUpdates: %v", err)
	}
	if err := newPoolClient.ConfigureLockBoxes(ctx, []slrbindings.LockBoxEntry{{
		RemoteChainSelector: remoteChainSelector,
		LockBox:             assets.LockBoxID,
	}}); err != nil {
		t.Fatalf("SiloedLockReleasePool v2 ConfigureLockBoxes: %v", err)
	}

	if err := assets.LockBoxClient.AddAllowedCallers(ctx, []string{newPoolID}); err != nil {
		t.Fatalf("TokenLockBox AddAllowedCallers (v2): %v", err)
	}
	if err := stack.TokenAdminRegistryClient.SetPool(ctx, tokenID, &newPoolID); err != nil {
		t.Fatalf("TokenAdminRegistry SetPool (v2): %v", err)
	}
	if err := assets.LockBoxClient.RemoveAllowedCallers(ctx, []string{oldPoolID}); err != nil {
		t.Fatalf("TokenLockBox RemoveAllowedCallers (v1): %v", err)
	}

	assets.PoolID = newPoolID
	assets.PoolClient = newPoolClient
	stack.TokenPoolID = newPoolID
	stack.TokenPoolClient = tokenpoolbindings.NewTokenPoolClient(deployer, newPoolID)

	t.Logf("migrated siloed pool %s -> %s (lock box %s unchanged)", oldPoolID, newPoolID, assets.LockBoxID)
	return newPoolID
}

func sacApproveOrFatal(
	ctx context.Context,
	t *testing.T,
	deployer *deployment.Deployer,
	rpcClient *rpcclient.Client,
	sacContract, from, spender string,
	amount int64,
) {
	t.Helper()
	ledger, err := rpcClient.GetLatestLedger(ctx)
	if err != nil {
		t.Fatalf("GetLatestLedger for SAC approve: %v", err)
	}
	expiration := ledger.Sequence + sacApproveLedgerBuffer
	args := []xdr.ScVal{
		scval.AddressToScVal(from),
		scval.AddressToScVal(spender),
		scval.I128ToScVal(big.NewInt(amount)),
		scval.Uint32ToScVal(expiration),
	}
	if _, err := deployer.InvokeContract(ctx, sacContract, "approve", args); err != nil {
		t.Fatalf("SAC approve %s -> %s amount=%d: %v", from, spender, amount, err)
	}
}

func setTokenPoolOrFatal(
	ctx context.Context,
	t *testing.T,
	stack *fullStack,
	tokenID, poolID string,
) {
	t.Helper()
	if err := stack.TokenAdminRegistryClient.SetPool(ctx, tokenID, &poolID); err != nil {
		t.Fatalf("TokenAdminRegistry SetPool(%s): %v", poolID, err)
	}
}
