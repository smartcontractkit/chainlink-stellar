package burn_mint_pool

import (
	"fmt"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	bmpbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/burn_mint_pool"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels burn-mint pool contracts.
const ContractType = "BurnMintPool"

// Deploy uploads pools_burn_mint_pool.wasm.
var Deploy = stellarops.NewDeployOperation("burn-mint-pool:deploy", "Deploys the burn-mint pool Soroban contract from WASM")

// InitializeInput matches burn-mint pool `initialize` (same shape as lock-release pool).
type InitializeInput struct {
	ContractID    string `json:"contract_id"`
	Owner         string `json:"owner"`
	Token         string `json:"token"`
	TokenDecimals uint32 `json:"token_decimals"`
	Router        string `json:"router"`
	RampRegistry  string `json:"ramp_registry"`
	// RmnProxy is the RMN proxy stored immutably on the pool at initialize (mirrors EVM
	// TokenPool's `immutable i_rmnProxy` constructor arg — there is NO set_rmn_proxy
	// entrypoint). The pool consults it directly for curse checks in
	// lock_or_burn/release_or_mint. Must match the Router's RMN proxy and be non-empty
	// (EVM parity: the constructor reverts on the zero address; Soroban has no zero
	// address, so the op enforces non-empty here).
	RmnProxy string `json:"rmn_proxy"`
}

// Initialize calls burn-mint pool `initialize` with owner, token, router, ramp registry,
// and the immutable RMN proxy (EVM `immutable i_rmnProxy` parity — set once, no setter).
var Initialize = cldfops.NewOperation(
	"burn-mint-pool:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes burn-mint pool with owner, token, router, ramp registry, and RMN proxy",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		if in.RmnProxy == "" {
			return stellarops.Void{}, fmt.Errorf("burn-mint pool initialize: rmn_proxy is required (EVM i_rmnProxy parity, no zero address)")
		}
		c := bmpbindings.NewBurnMintPoolClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.Token, in.TokenDecimals, in.Router, in.RampRegistry, in.RmnProxy); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// TransferOwnershipInput starts two-step ownership transfer.
type TransferOwnershipInput struct {
	ContractID string `json:"contract_id"`
	NewOwner   string `json:"new_owner"`
}

// TransferOwnership calls `transfer_ownership` on burn-mint pool.
var TransferOwnership = cldfops.NewOperation(
	"burn-mint-pool:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers burn-mint pool ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := bmpbindings.NewBurnMintPoolClient(d.Invoker, in.ContractID)
		if err := c.TransferOwnership(b.GetContext(), in.NewOwner); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// AcceptOwnershipInput completes two-step ownership transfer for the caller.
type AcceptOwnershipInput struct {
	ContractID string `json:"contract_id"`
}

// AcceptOwnership calls `accept_ownership` on burn-mint pool.
var AcceptOwnership = cldfops.NewOperation(
	"burn-mint-pool:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts burn-mint pool ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := bmpbindings.NewBurnMintPoolClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyTokenFeeConfigUpdatesInput applies a batch of per-chain token-transfer fee config
// additions and disables (EVM `TokenPool.applyTokenTransferFeeConfigUpdates`). Adds reject
// `is_enabled == false` (use the disable list), bps >= BPS_DIVIDER (10_000), and
// `dest_gas_overhead == 0`; the chain must be supported. Disables delete the stored entry.
type ApplyTokenFeeConfigUpdatesInput struct {
	ContractID string                                   `json:"contract_id"`
	Adds       []bmpbindings.TokenTransferFeeConfigArgs `json:"adds"`
	Disables   []uint64                                 `json:"disables"`
}

// ApplyTokenFeeConfigUpdates calls burn-mint pool `apply_token_fee_config_updates`.
var ApplyTokenFeeConfigUpdates = cldfops.NewOperation(
	"burn-mint-pool:apply-token-fee-config-updates",
	stellarops.ContractDeploymentVersion,
	"Applies token-transfer fee config adds/disables on the burn-mint pool (EVM applyTokenTransferFeeConfigUpdates parity)",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyTokenFeeConfigUpdatesInput) (stellarops.Void, error) {
		c := bmpbindings.NewBurnMintPoolClient(d.Invoker, in.ContractID)
		if err := c.ApplyTokenFeeConfigUpdates(b.GetContext(), in.Adds, in.Disables); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// WithdrawFeeTokensInput sweeps accrued fee-token balances to a recipient (EVM
// `TokenPool.withdrawFeeTokens`). Callable on-chain by the owner or fee admin; the op uses the
// deployer/invoker which must be one of those. Safe to sweep the full pool balance because a
// burn-mint pool holds only accrued fees (user tokens are burned, not held on the pool address).
//
// Caller is the explicit address that invokes and authorizes the call (Soroban has no
// msg.sender, so the contract takes the caller as an argument and require_auths it after checking
// it is the owner or fee admin). Set it to the deployer/invoker signer address (or the governing
// owner address) that will sign the transaction.
type WithdrawFeeTokensInput struct {
	ContractID string   `json:"contract_id"`
	Caller     string   `json:"caller"`
	FeeTokens  []string `json:"fee_tokens"`
	Recipient  string   `json:"recipient"`
}

// WithdrawFeeTokens calls burn-mint pool `withdraw_fee_tokens`.
var WithdrawFeeTokens = cldfops.NewOperation(
	"burn-mint-pool:withdraw-fee-tokens",
	stellarops.ContractDeploymentVersion,
	"Withdraws accrued fee-token balances to a recipient on the burn-mint pool (EVM withdrawFeeTokens parity)",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in WithdrawFeeTokensInput) (stellarops.Void, error) {
		c := bmpbindings.NewBurnMintPoolClient(d.Invoker, in.ContractID)
		if err := c.WithdrawFeeTokens(b.GetContext(), in.Caller, in.FeeTokens, in.Recipient); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// SetFeeAdminInput sets the fee-admin address authorized to call `withdraw_fee_tokens`
// alongside the owner (EVM parity for the `feeAdmin` field of `setDynamicConfig`). Owner-only.
type SetFeeAdminInput struct {
	ContractID string `json:"contract_id"`
	FeeAdmin   string `json:"fee_admin"`
}

// SetFeeAdmin calls burn-mint pool `set_fee_admin`.
var SetFeeAdmin = cldfops.NewOperation(
	"burn-mint-pool:set-fee-admin",
	stellarops.ContractDeploymentVersion,
	"Sets the fee-admin address on the burn-mint pool (EVM setDynamicConfig feeAdmin parity)",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetFeeAdminInput) (stellarops.Void, error) {
		c := bmpbindings.NewBurnMintPoolClient(d.Invoker, in.ContractID)
		if err := c.SetFeeAdmin(b.GetContext(), in.FeeAdmin); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// UpgradeInput upgrades the burn-mint pool contract in place to a new Wasm hash.
type UpgradeInput struct {
	ContractID  string   `json:"contract_id"`
	NewWasmHash [32]byte `json:"new_wasm_hash"`
}

// Upgrade calls `upgrade` on burn-mint pool (owner-gated in-place self-upgrade).
var Upgrade = cldfops.NewOperation(
	"burn-mint-pool:upgrade",
	stellarops.ContractDeploymentVersion,
	"Upgrades the burn-mint pool Soroban contract in place to a new WASM hash",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UpgradeInput) (stellarops.Void, error) {
		c := bmpbindings.NewBurnMintPoolClient(d.Invoker, in.ContractID)
		if err := c.Upgrade(b.GetContext(), in.NewWasmHash); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
