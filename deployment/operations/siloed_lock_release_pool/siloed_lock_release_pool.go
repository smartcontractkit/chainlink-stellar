package siloed_lock_release_pool

import (
	"fmt"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	slrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/siloed_lock_release_pool"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels siloed lock-release pool contracts.
const ContractType = "SiloedLockReleasePool"

// Deploy uploads pools_siloed_lock_release_pool.wasm.
var Deploy = stellarops.NewDeployOperation("siloed-lock-release-pool:deploy", "Deploys the siloed lock-release pool Soroban contract from WASM")

// InitializeInput matches siloed lock-release pool `initialize`.
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

// Initialize calls siloed lock-release pool `initialize` with owner, token, router, ramp
// registry, and the immutable RMN proxy (EVM `immutable i_rmnProxy` parity — set once, no setter).
var Initialize = cldfops.NewOperation(
	"siloed-lock-release-pool:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes siloed lock-release pool with owner, token, router, ramp registry, and RMN proxy",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		if in.RmnProxy == "" {
			return stellarops.Void{}, fmt.Errorf("siloed lock-release pool initialize: rmn_proxy is required (EVM i_rmnProxy parity, no zero address)")
		}
		c := slrbindings.NewSiloedLockReleasePoolClient(d.Invoker, in.ContractID)
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

// TransferOwnership calls `transfer_ownership` on siloed lock-release pool.
var TransferOwnership = cldfops.NewOperation(
	"siloed-lock-release-pool:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers siloed lock-release pool ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := slrbindings.NewSiloedLockReleasePoolClient(d.Invoker, in.ContractID)
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

// AcceptOwnership calls `accept_ownership` on siloed lock-release pool.
var AcceptOwnership = cldfops.NewOperation(
	"siloed-lock-release-pool:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts siloed lock-release pool ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := slrbindings.NewSiloedLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyChainUpdatesInput adds or removes remote chain configs on the siloed lock-release pool.
type ApplyChainUpdatesInput struct {
	ContractID string                    `json:"contract_id"`
	Adds       []slrbindings.ChainUpdate `json:"adds"`
	Removes    []uint64                  `json:"removes"`
}

// ApplyChainUpdates calls siloed lock-release pool `apply_chain_updates`.
var ApplyChainUpdates = cldfops.NewOperation(
	"siloed-lock-release-pool:apply-chain-updates",
	stellarops.ContractDeploymentVersion,
	"Adds or removes remote chain configs on the siloed lock-release pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyChainUpdatesInput) (stellarops.Void, error) {
		c := slrbindings.NewSiloedLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.ApplyChainUpdates(b.GetContext(), in.Adds, in.Removes); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ConfigureLockBoxesInput maps remote chain selectors to token lock box addresses.
type ConfigureLockBoxesInput struct {
	ContractID string                     `json:"contract_id"`
	Configs    []slrbindings.LockBoxEntry `json:"configs"`
}

// ConfigureLockBoxes calls siloed lock-release pool `configure_lock_boxes`.
var ConfigureLockBoxes = cldfops.NewOperation(
	"siloed-lock-release-pool:configure-lock-boxes",
	stellarops.ContractDeploymentVersion,
	"Maps remote chain selectors to token lock box addresses on the siloed pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ConfigureLockBoxesInput) (stellarops.Void, error) {
		c := slrbindings.NewSiloedLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.ConfigureLockBoxes(b.GetContext(), in.Configs); err != nil {
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
	Adds       []slrbindings.TokenTransferFeeConfigArgs `json:"adds"`
	Disables   []uint64                                 `json:"disables"`
}

// ApplyTokenFeeConfigUpdates calls siloed lock-release pool `apply_token_fee_config_updates`.
var ApplyTokenFeeConfigUpdates = cldfops.NewOperation(
	"siloed-lock-release-pool:apply-token-fee-config-updates",
	stellarops.ContractDeploymentVersion,
	"Applies token-transfer fee config adds/disables on the siloed lock-release pool (EVM applyTokenTransferFeeConfigUpdates parity)",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyTokenFeeConfigUpdatesInput) (stellarops.Void, error) {
		c := slrbindings.NewSiloedLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.ApplyTokenFeeConfigUpdates(b.GetContext(), in.Adds, in.Disables); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// WithdrawFeeTokensInput sweeps accrued fee-token balances to a recipient (EVM
// `TokenPool.withdrawFeeTokens`). Callable on-chain by the owner or fee admin; the op uses the
// deployer/invoker which must be one of those. Safe to sweep the full pool balance because user
// liquidity is escrowed in the lockbox, not held on the pool address.
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

// WithdrawFeeTokens calls siloed lock-release pool `withdraw_fee_tokens`.
var WithdrawFeeTokens = cldfops.NewOperation(
	"siloed-lock-release-pool:withdraw-fee-tokens",
	stellarops.ContractDeploymentVersion,
	"Withdraws accrued fee-token balances to a recipient on the siloed lock-release pool (EVM withdrawFeeTokens parity)",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in WithdrawFeeTokensInput) (stellarops.Void, error) {
		c := slrbindings.NewSiloedLockReleasePoolClient(d.Invoker, in.ContractID)
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

// SetFeeAdmin calls siloed lock-release pool `set_fee_admin`.
var SetFeeAdmin = cldfops.NewOperation(
	"siloed-lock-release-pool:set-fee-admin",
	stellarops.ContractDeploymentVersion,
	"Sets the fee-admin address on the siloed lock-release pool (EVM setDynamicConfig feeAdmin parity)",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetFeeAdminInput) (stellarops.Void, error) {
		c := slrbindings.NewSiloedLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.SetFeeAdmin(b.GetContext(), in.FeeAdmin); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
