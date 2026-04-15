package devenv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Masterminds/semver/v3"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
)

// DevenvTestTokenPoolQualifier is the datastore qualifier for the lock-release test pool.
const DevenvTestTokenPoolQualifier = "TEST"

// DeployLockReleaseTestTokenPool deploys the lock-release pool and, when Friendbot is
// available, creates the test SAC token, initializes the pool, and registers token+pool
// in TokenAdminRegistry. Intended to run from PostDeployContractsForSelector after core CCIP deploy.
func DeployLockReleaseTestTokenPool(ctx context.Context, host Host) error {
	h := host
	stellarRoot, err := stellarutil.FindStellarRoot()
	if err != nil {
		return fmt.Errorf("find stellar root: %w", err)
	}

	poolWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "pools_lock_release_pool.wasm")
	if _, statErr := os.Stat(poolWasmPath); os.IsNotExist(statErr) {
		return fmt.Errorf("LockReleasePool WASM not found at %s. Run 'make build'.", poolWasmPath)
	}
	h.Logger().Info().Str("wasmPath", poolWasmPath).Msg("Deploying LockRelease pool contract (post-deploy)...")
	poolSalt := stellardeployment.GenerateDeterministicSalt(h.DeployerKeypair().Address(), "lock-release-pool")
	poolContractID, err := h.Deployer().DeployContract(ctx, poolWasmPath, poolSalt)
	if err != nil {
		return fmt.Errorf("failed to deploy LockRelease pool: %w", err)
	}
	poolClient := tokenpoolbindings.NewTokenPoolClient(h.Deployer(), poolContractID)
	h.SetTokenPool(poolContractID, poolClient)
	h.Logger().Info().Str("contractID", poolContractID).Msg("LockRelease pool deployed")

	tarClient := h.TokenAdminRegistryClient()
	if tarClient == nil {
		return fmt.Errorf("token admin registry client is nil before pool token setup")
	}

	if h.FriendbotURL() == "" {
		h.Logger().Warn().Msg("Friendbot URL not available; skipping test token deployment. Token transfer E2E tests will not work.")
		return nil
	}

	tokenContractID, tokenErr := h.CreateTestToken(ctx, h.FriendbotURL())
	if tokenErr != nil {
		return fmt.Errorf("failed to create test token: %w", tokenErr)
	}
	h.SetTestToken(tokenContractID)

	// Match typical Stellar SAC / pool configuration (EVM `uint8` token decimals on the pool).
	const testTokenPoolDecimals uint32 = 7
	routerID := h.RouterContractID()
	if routerID == "" {
		return fmt.Errorf("router contract ID is not set on host; cannot initialize token pool")
	}
	if err := poolClient.Initialize(ctx, h.DeployerKeypair().Address(), tokenContractID, testTokenPoolDecimals, routerID); err != nil {
		return fmt.Errorf("failed to initialize pool with token: %w", err)
	}

	deployerAddr := h.DeployerKeypair().Address()
	if err := tarClient.ProposeAdministrator(ctx, deployerAddr, tokenContractID, deployerAddr); err != nil {
		return fmt.Errorf("failed to propose administrator in TokenAdminRegistry: %w", err)
	}
	if err := tarClient.AcceptAdminRole(ctx, tokenContractID); err != nil {
		return fmt.Errorf("failed to accept admin role in TokenAdminRegistry: %w", err)
	}
	if err := tarClient.SetPool(ctx, tokenContractID, &poolContractID); err != nil {
		return fmt.Errorf("failed to register pool in TokenAdminRegistry: %w", err)
	}
	h.Logger().Info().
		Str("token", tokenContractID).
		Str("pool", poolContractID).
		Msg("Token pool registered in TokenAdminRegistry")
	return nil
}

// LockReleasePoolAddressRefDataStore returns a sealed datastore containing only the
// lock-release pool AddressRef for devenv (qualifier DevenvTestTokenPoolQualifier).
func LockReleasePoolAddressRefDataStore(chainSelector uint64, poolContractID string) (datastore.DataStore, error) {
	ds := datastore.NewMemoryDataStore()
	poolHex, err := stellarutil.StrkeyToHex(poolContractID)
	if err != nil {
		return nil, fmt.Errorf("convert pool address: %w", err)
	}
	if err := ds.AddressRefStore.Add(datastore.AddressRef{
		Address:       poolHex,
		ChainSelector: chainSelector,
		Type:          datastore.ContractType(LockReleaseTokenPoolContractType),
		Version:       semver.MustParse("1.0.0"),
		Qualifier:     DevenvTestTokenPoolQualifier,
	}); err != nil {
		return nil, fmt.Errorf("add pool address ref: %w", err)
	}
	return ds.Seal(), nil
}
