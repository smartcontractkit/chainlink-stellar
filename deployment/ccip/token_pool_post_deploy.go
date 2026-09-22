package ccip

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	sacops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/sac_token"
	slrpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/siloed_lock_release_pool"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_admin_registry"
	tlbops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_lock_box"
	poolops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_pool"
)

// See [DevenvTestTokenPoolQualifier].

// initialPoolLiquidity is the amount of SAC base units seeded into the token lock box
// so that inbound release_or_mint calls on the siloed pool have liquidity.
// 100 tokens at 7 decimals = 1_000_000_000 base units.
const initialPoolLiquidity int64 = 1_000_000_000

const testTokenPoolDecimals uint32 = 7

// sacApproveLedgerBuffer is added to the current ledger when approving SAC spend for lockbox deposit.
const sacApproveLedgerBuffer uint32 = 1000

// DeployLockReleaseTestTokenPool deploys legacy lock-release and siloed lock-release (+ lock box)
// test pools. E2E token transfers use the siloed pool; the legacy pool remains available for
// lock-release-specific tests. Intended to run from PostDeployContractsForSelector.
func DeployLockReleaseTestTokenPool(ctx context.Context, opBundle cldfops.Bundle, host CCIPDevenvHost) error {
	tarClient := host.TokenAdminRegistryClient()
	if tarClient == nil {
		return fmt.Errorf("token admin registry client is nil before pool token setup")
	}

	if host.FriendbotURL() == "" {
		host.Logger().Warn().Msg("Friendbot URL not available; skipping test token deployment. Token transfer E2E tests will not work.")
		return nil
	}

	tokenContractID, err := host.CreateTestToken(ctx, host.FriendbotURL())
	if err != nil {
		return fmt.Errorf("failed to create test token: %w", err)
	}
	host.SetTestToken(tokenContractID)

	if err := deployLegacyLockReleasePool(ctx, opBundle, host, tokenContractID); err != nil {
		return err
	}
	if err := deploySiloedLockReleaseTestTokenPool(ctx, opBundle, host, tokenContractID, tarClient.ContractID()); err != nil {
		return err
	}
	return nil
}

func deployLegacyLockReleasePool(ctx context.Context, opBundle cldfops.Bundle, host CCIPDevenvHost, tokenContractID string) error {
	stellarRoot, err := stellarutil.FindStellarRoot()
	if err != nil {
		return fmt.Errorf("find stellar root: %w", err)
	}

	poolWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "pools_lock_release_pool.wasm")
	if _, statErr := os.Stat(poolWasmPath); os.IsNotExist(statErr) {
		return fmt.Errorf("LockReleasePool WASM not found at %s. Run 'make build'.", poolWasmPath)
	}
	lockBoxWasm := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "pools_token_lock_box.wasm")
	if _, statErr := os.Stat(lockBoxWasm); os.IsNotExist(statErr) {
		return fmt.Errorf("TokenLockBox WASM not found at %s. Run 'make build'.", lockBoxWasm)
	}

	host.Logger().Info().Str("wasmPath", poolWasmPath).Msg("Deploying legacy LockRelease pool (post-deploy, not used for E2E transfers)...")
	deployerAddr := host.DeployerKeypair().Address()
	deps := stellardeps.FromDeployer(host.Deployer())

	// The canonical lock-release pool now escrows in a lockbox (EVM
	// `LockReleaseTokenPool.i_lockBox` parity): `release_or_mint` withdraws from the
	// lockbox and `lock_or_burn` deposits the post-fee amount into it, so the pool's
	// own token balance equals only accrued fees. Seed liquidity into the lockbox,
	// not onto the pool address. The legacy pool is a deployed reference artifact
	// (the siloed pool is the one wired for E2E), so its lockbox is deployed + funded
	// here but not mapped to any remote chain — a test exercising the legacy pool
	// must `configure_lock_boxes` for its remote chains first.
	lockBoxSalt := stellardeployment.GenerateDeterministicSalt(deployerAddr, "legacy-token-lock-box")
	lockBoxRep, err := cldfops.ExecuteOperation(opBundle, tlbops.Deploy, deps, stellarops.DeployInput{WasmPath: lockBoxWasm, Salt: lockBoxSalt})
	if err != nil {
		return fmt.Errorf("deploy legacy pool token lock box: %w", err)
	}
	lockBoxID := lockBoxRep.Output.ContractID
	if _, err := cldfops.ExecuteOperation(opBundle, tlbops.Initialize, deps, tlbops.InitializeInput{
		ContractID: lockBoxID,
		Owner:      deployerAddr,
		Token:      tokenContractID,
	}); err != nil {
		return fmt.Errorf("initialize legacy pool token lock box: %w", err)
	}
	host.Logger().Info().Str("contractID", lockBoxID).Msg("Legacy pool token lock box deployed")

	poolSalt := stellardeployment.GenerateDeterministicSalt(deployerAddr, "lock-release-pool")
	poolRep, err := cldfops.ExecuteOperation(opBundle, poolops.Deploy, deps, stellarops.DeployInput{WasmPath: poolWasmPath, Salt: poolSalt})
	if err != nil {
		return fmt.Errorf("failed to deploy legacy LockRelease pool: %w", err)
	}
	poolContractID := poolRep.Output.ContractID
	host.SetLegacyLockReleasePool(poolContractID)
	host.Logger().Info().Str("contractID", poolContractID).Msg("Legacy LockRelease pool deployed")

	routerContractID := host.RouterContractID()
	if routerContractID == "" {
		return fmt.Errorf("router contract ID is empty; token pool initialize requires router (deploy core CCIP first)")
	}
	rampRegistryContractID := host.RampRegistryContractID()
	if rampRegistryContractID == "" {
		return fmt.Errorf("ramp registry contract ID is empty; token pool initialize requires ramp registry (deploy core CCIP first)")
	}
	rmnProxyContractID := host.RmnProxyContractID()
	if rmnProxyContractID == "" {
		return fmt.Errorf("rmn proxy contract ID is empty; token pool curse checks require it (deploy core CCIP first)")
	}
	if _, err := cldfops.ExecuteOperation(opBundle, poolops.Initialize, deps, poolops.InitializeInput{
		ContractID:    poolContractID,
		Owner:         deployerAddr,
		Token:         tokenContractID,
		TokenDecimals: testTokenPoolDecimals,
		Router:        routerContractID,
		RampRegistry:  rampRegistryContractID,
		RmnProxy:      rmnProxyContractID,
	}); err != nil {
		return fmt.Errorf("failed to initialize legacy pool with token: %w", err)
	}

	if _, err := cldfops.ExecuteOperation(opBundle, tlbops.AddAllowedCallers, deps, tlbops.AddAllowedCallersInput{
		ContractID: lockBoxID,
		Callers:    []string{poolContractID, deployerAddr},
	}); err != nil {
		return fmt.Errorf("add legacy pool and deployer as lock box callers: %w", err)
	}

	expirationLedger, err := host.LatestLedgerSequence(ctx)
	if err != nil {
		host.Logger().Warn().Err(err).Msg("could not read latest ledger; using far-future SAC approve expiration")
		expirationLedger = 9_999_999
	} else {
		expirationLedger += sacApproveLedgerBuffer
	}
	if _, err := cldfops.ExecuteOperation(opBundle, sacops.Approve, deps, sacops.ApproveInput{
		ContractID:       tokenContractID,
		From:             deployerAddr,
		Spender:          lockBoxID,
		Amount:           initialPoolLiquidity,
		ExpirationLedger: expirationLedger,
	}); err != nil {
		return fmt.Errorf("approve legacy lock box to pull SAC for deposit: %w", err)
	}
	if _, err := cldfops.ExecuteOperation(opBundle, tlbops.Deposit, deps, tlbops.DepositInput{
		ContractID: lockBoxID,
		Caller:     deployerAddr,
		Amount:     initialPoolLiquidity,
	}); err != nil {
		return fmt.Errorf("deposit SAC liquidity into legacy lock box: %w", err)
	}
	host.Logger().Info().
		Str("lockBox", lockBoxID).
		Str("pool", poolContractID).
		Int64("amount", initialPoolLiquidity).
		Msg("Funded legacy pool lock box with SAC liquidity (pool balance is fees-only)")
	return nil
}

func deploySiloedLockReleaseTestTokenPool(
	ctx context.Context,
	opBundle cldfops.Bundle,
	host CCIPDevenvHost,
	tokenContractID, tarID string,
) error {
	stellarRoot, err := stellarutil.FindStellarRoot()
	if err != nil {
		return fmt.Errorf("find stellar root: %w", err)
	}

	lockBoxWasm := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "pools_token_lock_box.wasm")
	if _, statErr := os.Stat(lockBoxWasm); os.IsNotExist(statErr) {
		return fmt.Errorf("TokenLockBox WASM not found at %s. Run 'make build'.", lockBoxWasm)
	}
	siloedWasm := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "pools_siloed_lock_release_pool.wasm")
	if _, statErr := os.Stat(siloedWasm); os.IsNotExist(statErr) {
		return fmt.Errorf("SiloedLockReleasePool WASM not found at %s. Run 'make build'.", siloedWasm)
	}

	deployerAddr := host.DeployerKeypair().Address()
	deps := stellardeps.FromDeployer(host.Deployer())

	lockBoxSalt := stellardeployment.GenerateDeterministicSalt(deployerAddr, "token-lock-box")
	lockBoxRep, err := cldfops.ExecuteOperation(opBundle, tlbops.Deploy, deps, stellarops.DeployInput{WasmPath: lockBoxWasm, Salt: lockBoxSalt})
	if err != nil {
		return fmt.Errorf("deploy token lock box: %w", err)
	}
	lockBoxID := lockBoxRep.Output.ContractID
	if _, err := cldfops.ExecuteOperation(opBundle, tlbops.Initialize, deps, tlbops.InitializeInput{
		ContractID: lockBoxID,
		Owner:      deployerAddr,
		Token:      tokenContractID,
	}); err != nil {
		return fmt.Errorf("initialize token lock box: %w", err)
	}
	host.SetTokenLockBox(lockBoxID)
	host.Logger().Info().Str("contractID", lockBoxID).Msg("Token lock box deployed")

	siloedSalt := stellardeployment.GenerateDeterministicSalt(deployerAddr, "siloed-lock-release-pool")
	siloedRep, err := cldfops.ExecuteOperation(opBundle, slrpops.Deploy, deps, stellarops.DeployInput{WasmPath: siloedWasm, Salt: siloedSalt})
	if err != nil {
		return fmt.Errorf("deploy siloed lock-release pool: %w", err)
	}
	siloedPoolID := siloedRep.Output.ContractID

	routerContractID := host.RouterContractID()
	rampRegistryContractID := host.RampRegistryContractID()
	rmnProxyContractID := host.RmnProxyContractID()
	if rmnProxyContractID == "" {
		return fmt.Errorf("rmn proxy contract ID is empty; token pool curse checks require it (deploy core CCIP first)")
	}
	if _, err := cldfops.ExecuteOperation(opBundle, slrpops.Initialize, deps, slrpops.InitializeInput{
		ContractID:    siloedPoolID,
		Owner:         deployerAddr,
		Token:         tokenContractID,
		TokenDecimals: testTokenPoolDecimals,
		Router:        routerContractID,
		RampRegistry:  rampRegistryContractID,
		RmnProxy:      rmnProxyContractID,
	}); err != nil {
		return fmt.Errorf("initialize siloed pool: %w", err)
	}

	if _, err := cldfops.ExecuteOperation(opBundle, tlbops.AddAllowedCallers, deps, tlbops.AddAllowedCallersInput{
		ContractID: lockBoxID,
		Callers:    []string{siloedPoolID, deployerAddr},
	}); err != nil {
		return fmt.Errorf("add siloed pool and deployer as lock box callers: %w", err)
	}

	expirationLedger, err := host.LatestLedgerSequence(ctx)
	if err != nil {
		host.Logger().Warn().Err(err).Msg("could not read latest ledger; using far-future SAC approve expiration")
		expirationLedger = 9_999_999
	} else {
		expirationLedger += sacApproveLedgerBuffer
	}
	if _, err := cldfops.ExecuteOperation(opBundle, sacops.Approve, deps, sacops.ApproveInput{
		ContractID:       tokenContractID,
		From:             deployerAddr,
		Spender:          lockBoxID,
		Amount:           initialPoolLiquidity,
		ExpirationLedger: expirationLedger,
	}); err != nil {
		return fmt.Errorf("approve lock box to pull SAC for deposit: %w", err)
	}
	if _, err := cldfops.ExecuteOperation(opBundle, tlbops.Deposit, deps, tlbops.DepositInput{
		ContractID: lockBoxID,
		Caller:     deployerAddr,
		Amount:     initialPoolLiquidity,
	}); err != nil {
		return fmt.Errorf("deposit SAC liquidity into lock box: %w", err)
	}
	host.Logger().Info().
		Str("lockBox", lockBoxID).
		Int64("amount", initialPoolLiquidity).
		Msg("Funded token lock box with SAC liquidity for inbound transfers")

	siloedClient := tokenpoolbindings.NewTokenPoolClient(host.Deployer(), siloedPoolID)
	host.SetTokenPool(siloedPoolID, siloedClient)
	host.Logger().Info().Str("contractID", siloedPoolID).Msg("Siloed lock-release pool ready for E2E token transfers")

	if _, err := cldfops.ExecuteOperation(opBundle, tarops.ProposeAdministrator, deps, tarops.ProposeAdministratorInput{
		ContractID:    tarID,
		Caller:        deployerAddr,
		LocalToken:    tokenContractID,
		Administrator: deployerAddr,
	}); err != nil {
		return fmt.Errorf("propose administrator in TokenAdminRegistry: %w", err)
	}
	if _, err := cldfops.ExecuteOperation(opBundle, tarops.AcceptAdminRole, deps, tarops.AcceptAdminRoleInput{
		ContractID: tarID,
		LocalToken: tokenContractID,
	}); err != nil {
		return fmt.Errorf("accept admin role in TokenAdminRegistry: %w", err)
	}
	poolID := siloedPoolID
	if _, err := cldfops.ExecuteOperation(opBundle, tarops.SetPool, deps, tarops.SetPoolInput{
		ContractID: tarID,
		LocalToken: tokenContractID,
		Pool:       &poolID,
	}); err != nil {
		return fmt.Errorf("register siloed pool in TokenAdminRegistry: %w", err)
	}
	host.Logger().Info().
		Str("token", tokenContractID).
		Str("pool", siloedPoolID).
		Msg("Siloed token pool registered in TokenAdminRegistry")
	return nil
}
