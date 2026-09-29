package sequences

import (
	"context"
	"fmt"
	"path/filepath"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	stellarutil "github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	bnmops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/bnm_token"
	bmpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
	sacops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/sac_token"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_admin_registry"
)

// DeployBnmTokenInput configures a BnM token + burn-mint pool onboarding.
// Name/Symbol/Decimals default to "CCIP BnM" / "BnM" / 7 (Stellar SAC
// convention) when left zero. TarContractID is the already-deployed
// TokenAdminRegistry that will link the BnM token to its pool.
type DeployBnmTokenInput struct {
	TarContractID string `json:"tar_contract_id"`
	Name          string `json:"name"`
	Symbol        string `json:"symbol"`
	Decimals      uint32 `json:"decimals"`
}

// DeployBnmTokenOutput is the result of a BnM onboarding: the deployed BnM
// token contract ID and the burn-mint pool wired to it.
type DeployBnmTokenOutput struct {
	BnmTokenID string `json:"bnm_token_id"`
	PoolID     string `json:"pool_id"`
}

// RunDeployBnmToken onboards a CCIP BnM test token as a remotely-issued
// burn-mint token: it deploys the custom BnM Soroban token (implementing the
// full `token::StellarAssetInterface`), hands mint authority to a burn-mint
// token pool via `set_admin`, initializes the pool against the core CCIP stack
// (Router / RampRegistry / RMN proxy already on the host), and registers the
// token↔pool link in the TokenAdminRegistry. No initial supply is minted —
// supply tracks bridge flow (mint on inbound, burn on outbound).
//
// This mirrors the canonical lock-release onboarding
// (`deploySiloedLockReleaseTestTokenPool`) but for the burn-mint model: there
// is no lock-box / approve / deposit (a burn-mint pool holds only accrued
// fees), and the burn-mint-specific step is the `set_admin` mint-authority
// handoff (the deployer, initial BnM admin, transfers admin to the pool so the
// pool can mint on inbound messages).
//
// Prerequisites on the host: Router, RampRegistry, RMN proxy, and the
// TokenAdminRegistry (TarContractID) must already be deployed (run
// RunStellarCCIPFullDeploy first). Per-remote-chain pool config
// (apply_chain_updates) is NOT applied here — it is added at lane-configuration
// time, matching the lock-release onboarding.
//
// deps is the Stellar deploy/invoke surface (as in RunStellarCCIPFullDeploy) so
// the sequence is unit-testable with the operationstest recording harness
// without constructing a real framework Deployer; in production it is
// `stellardeps.FromDeployer(host.Deployer())`.
func RunDeployBnmToken(
	ctx context.Context,
	b cldfops.Bundle,
	deps stellardeps.StellarDeps,
	host stellarccip.CCIPDevenvHost,
	in DeployBnmTokenInput,
) (DeployBnmTokenOutput, error) {
	if host == nil {
		return DeployBnmTokenOutput{}, fmt.Errorf("RunDeployBnmToken: CCIPDevenvHost is nil")
	}
	if deps.Deploy == nil || deps.Invoker == nil {
		return DeployBnmTokenOutput{}, fmt.Errorf("RunDeployBnmToken: incomplete StellarDeps (Deploy/Invoker must be non-nil)")
	}
	if in.TarContractID == "" {
		return DeployBnmTokenOutput{}, fmt.Errorf("RunDeployBnmToken: tar_contract_id is required (deploy core CCIP first)")
	}

	routerContractID := host.RouterContractID()
	rampRegistryContractID := host.RampRegistryContractID()
	rmnProxyContractID := host.RmnProxyContractID()
	if rmnProxyContractID == "" {
		return DeployBnmTokenOutput{}, fmt.Errorf("rmn proxy contract ID is empty; burn-mint pool curse checks require it (deploy core CCIP first)")
	}

	name := in.Name
	if name == "" {
		name = "CCIP BnM"
	}
	symbol := in.Symbol
	if symbol == "" {
		symbol = "BnM"
	}
	decimals := in.Decimals
	if decimals == 0 {
		decimals = 7
	}

	stellarRoot, err := stellarutil.FindStellarRoot()
	if err != nil {
		return DeployBnmTokenOutput{}, fmt.Errorf("locate chainlink-stellar root: %w", err)
	}

	bnmWasm := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "bnm_token.wasm")
	if err := statReleaseWasm(bnmWasm, "BnM token"); err != nil {
		return DeployBnmTokenOutput{}, err
	}
	bmpWasm := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "pools_burn_mint_pool.wasm")
	if err := statReleaseWasm(bmpWasm, "BurnMint pool"); err != nil {
		return DeployBnmTokenOutput{}, err
	}

	deployerAddr := host.DeployerKeypair().Address()

	// 1. Deploy the BnM token contract.
	host.Logger().Info().Str("wasmPath", bnmWasm).Msg("Deploying BnM token contract...")
	bnmSalt := stellardeployment.GenerateDeterministicSalt(deployerAddr, "bnm-token")
	bnmOut, err := execStellarCCIPOp(b, deps, bnmops.Deploy, stellarops.DeployInput{WasmPath: bnmWasm, Salt: bnmSalt})
	if err != nil {
		return DeployBnmTokenOutput{}, fmt.Errorf("deploy BnM token: %w", err)
	}
	bnmID := bnmOut.ContractID
	host.Logger().Info().Str("contractID", bnmID).Msg("BnM token contract deployed")

	// 2. Initialize BnM: deployer is the initial admin (hands off to the pool next).
	if _, err := execStellarCCIPOp(b, deps, bnmops.Initialize, bnmops.InitializeInput{
		ContractID: bnmID,
		Admin:      deployerAddr,
		Name:       name,
		Symbol:     symbol,
		Decimals:   decimals,
	}); err != nil {
		return DeployBnmTokenOutput{}, fmt.Errorf("initialize BnM token: %w", err)
	}
	host.Logger().Info().
		Str("contractID", bnmID).
		Str("name", name).Str("symbol", symbol).Uint32("decimals", decimals).
		Msg("BnM token initialized (deployer=admin, no initial supply)")

	// 3. Deploy the burn-mint pool.
	host.Logger().Info().Str("wasmPath", bmpWasm).Msg("Deploying burn-mint pool contract...")
	bmpSalt := stellardeployment.GenerateDeterministicSalt(deployerAddr, "burn-mint-pool")
	bmpOut, err := execStellarCCIPOp(b, deps, bmpops.Deploy, stellarops.DeployInput{WasmPath: bmpWasm, Salt: bmpSalt})
	if err != nil {
		return DeployBnmTokenOutput{}, fmt.Errorf("deploy burn-mint pool: %w", err)
	}
	poolID := bmpOut.ContractID

	// 4. Burn-mint mint-authority handoff: the deployer (current BnM admin, who
	// signs the transaction) transfers token admin to the pool so the pool can
	// mint on inbound bridge messages (SAC `set_admin` parity). Must happen
	// before the pool mints; order relative to pool Initialize is free, but the
	// pool address must exist (deployed above).
	if _, err := execStellarCCIPOp(b, deps, sacops.SetAdmin, sacops.SetAdminInput{
		ContractID: bnmID,
		NewAdmin:   poolID,
	}); err != nil {
		return DeployBnmTokenOutput{}, fmt.Errorf("set_admin (burn-mint mint authority → pool): %w", err)
	}
	host.Logger().Info().
		Str("token", bnmID).Str("newAdmin(pool)", poolID).
		Msg("BnM mint authority handed to burn-mint pool via set_admin")

	// 5. Initialize the burn-mint pool against the core CCIP stack.
	if _, err := execStellarCCIPOp(b, deps, bmpops.Initialize, bmpops.InitializeInput{
		ContractID:    poolID,
		Owner:         deployerAddr,
		Token:         bnmID,
		TokenDecimals: decimals,
		Router:        routerContractID,
		RampRegistry:  rampRegistryContractID,
		RmnProxy:      rmnProxyContractID,
	}); err != nil {
		return DeployBnmTokenOutput{}, fmt.Errorf("initialize burn-mint pool: %w", err)
	}
	host.Logger().Info().Str("contractID", poolID).Msg("Burn-mint pool initialized")

	// 6. Register the token↔pool link in the TokenAdminRegistry (the canonical
	// ProposeAdministrator → AcceptAdminRole → SetPool triple). TAR only links
	// the token to its pool for ramp `get_pool` lookup; mint authority was
	// already transferred in step 4 via `set_admin`.
	if _, err := execStellarCCIPOp(b, deps, tarops.ProposeAdministrator, tarops.ProposeAdministratorInput{
		ContractID:    in.TarContractID,
		Caller:        deployerAddr,
		LocalToken:    bnmID,
		Administrator: deployerAddr,
	}); err != nil {
		return DeployBnmTokenOutput{}, fmt.Errorf("propose BnM administrator in TokenAdminRegistry: %w", err)
	}
	if _, err := execStellarCCIPOp(b, deps, tarops.AcceptAdminRole, tarops.AcceptAdminRoleInput{
		ContractID: in.TarContractID,
		LocalToken: bnmID,
	}); err != nil {
		return DeployBnmTokenOutput{}, fmt.Errorf("accept BnM admin role in TokenAdminRegistry: %w", err)
	}
	if _, err := execStellarCCIPOp(b, deps, tarops.SetPool, tarops.SetPoolInput{
		ContractID: in.TarContractID,
		LocalToken: bnmID,
		Pool:       &poolID,
	}); err != nil {
		return DeployBnmTokenOutput{}, fmt.Errorf("register BnM pool in TokenAdminRegistry: %w", err)
	}

	// Track the pool on the host (base TokenPool client — burn-mint pool
	// implements BaseTokenPool, same as the siloed onboarding). Use deps.Invoker
	// (the same surface the sequence deployed through) so the client is wired to
	// the recording invoker in tests, not a real Deployer.
	poolClient := tokenpoolbindings.NewTokenPoolClient(deps.Invoker, poolID)
	host.SetTokenPool(poolID, poolClient)

	host.Logger().Info().
		Str("token", bnmID).Str("pool", poolID).
		Msg("BnM burn-mint token onboarded and registered in TokenAdminRegistry")

	_ = ctx // reserved for future ledger-timeline dependencies
	return DeployBnmTokenOutput{BnmTokenID: bnmID, PoolID: poolID}, nil
}
