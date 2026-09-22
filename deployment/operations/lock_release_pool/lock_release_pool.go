// Package lock_release_pool defines CLDF operations for the Soroban lock-release pool contract.
package lock_release_pool

import (
	"fmt"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	lrpbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/lock_release_pool"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels lock-release pool contracts.
const ContractType = "LockReleasePool"

// Deploy uploads pools_lock_release_pool.wasm.
var Deploy = stellarops.NewDeployOperation("lock-release-pool:deploy", "Deploys the lock-release pool Soroban contract from WASM")

// InitializeInput matches lock-release pool `initialize` (same shape as burn-mint pool).
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

// Initialize calls lock-release pool `initialize` with owner, token, router, ramp registry,
// and the immutable RMN proxy (EVM `immutable i_rmnProxy` parity — set once, no setter).
var Initialize = cldfops.NewOperation(
	"lock-release-pool:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes lock-release pool with owner, token, router, ramp registry, and RMN proxy",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		if in.RmnProxy == "" {
			return stellarops.Void{}, fmt.Errorf("lock-release pool initialize: rmn_proxy is required (EVM i_rmnProxy parity, no zero address)")
		}
		c := lrpbindings.NewLockReleasePoolClient(d.Invoker, in.ContractID)
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

// TransferOwnership calls `transfer_ownership` on lock-release pool.
var TransferOwnership = cldfops.NewOperation(
	"lock-release-pool:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers lock-release pool ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := lrpbindings.NewLockReleasePoolClient(d.Invoker, in.ContractID)
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

// AcceptOwnership calls `accept_ownership` on lock-release pool.
var AcceptOwnership = cldfops.NewOperation(
	"lock-release-pool:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts lock-release pool ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := lrpbindings.NewLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// SetRateLimitConfigInput configures outbound/inbound token bucket rate limits for a remote chain.
type SetRateLimitConfigInput struct {
	ContractID          string                      `json:"contract_id"`
	RemoteChainSelector uint64                      `json:"remote_chain_selector"`
	OutboundConfig      lrpbindings.RateLimitConfig `json:"outbound_config"`
	InboundConfig       lrpbindings.RateLimitConfig `json:"inbound_config"`
	FastFinality        bool                        `json:"fast_finality"`
}

// SetRateLimitConfig calls lock-release pool `set_rate_limit_config`.
var SetRateLimitConfig = cldfops.NewOperation(
	"lock-release-pool:set-rate-limit-config",
	stellarops.ContractDeploymentVersion,
	"Sets outbound and inbound rate limit configs for a remote chain on the lock-release pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetRateLimitConfigInput) (stellarops.Void, error) {
		c := lrpbindings.NewLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.SetRateLimitConfig(b.GetContext(), in.RemoteChainSelector, in.OutboundConfig, in.InboundConfig, in.FastFinality); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// GetCurrentRateLimiterStateInput reads the active bucket state for a remote chain.
type GetCurrentRateLimiterStateInput struct {
	ContractID          string `json:"contract_id"`
	RemoteChainSelector uint64 `json:"remote_chain_selector"`
	FastFinality        bool   `json:"fast_finality"`
}

// GetCurrentRateLimiterStateOutput is the on-chain rate limiter bucket state.
type GetCurrentRateLimiterStateOutput struct {
	State *lrpbindings.RateLimiterState `json:"state"`
}

// GetCurrentRateLimiterState calls lock-release pool `get_current_rate_limiter_state` (simulation).
var GetCurrentRateLimiterState = cldfops.NewOperation(
	"lock-release-pool:get-current-rate-limiter-state",
	stellarops.ContractDeploymentVersion,
	"Reads the current outbound/inbound rate limiter bucket state for a remote chain",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetCurrentRateLimiterStateInput) (GetCurrentRateLimiterStateOutput, error) {
		c := lrpbindings.NewLockReleasePoolClient(d.Invoker, in.ContractID)
		state, err := c.GetCurrentRateLimiterState(b.GetContext(), in.RemoteChainSelector, in.FastFinality)
		if err != nil {
			return GetCurrentRateLimiterStateOutput{}, err
		}
		return GetCurrentRateLimiterStateOutput{State: state}, nil
	},
)

// ApplyChainUpdatesInput adds or removes remote chain configs on the lock-release pool.
type ApplyChainUpdatesInput struct {
	ContractID string                    `json:"contract_id"`
	Adds       []lrpbindings.ChainUpdate `json:"adds"`
	Removes    []uint64                  `json:"removes"`
}

// ApplyChainUpdates calls lock-release pool `apply_chain_updates`.
var ApplyChainUpdates = cldfops.NewOperation(
	"lock-release-pool:apply-chain-updates",
	stellarops.ContractDeploymentVersion,
	"Adds or removes remote chain configs on the lock-release pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyChainUpdatesInput) (stellarops.Void, error) {
		c := lrpbindings.NewLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.ApplyChainUpdates(b.GetContext(), in.Adds, in.Removes); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ConfigureLockBoxesInput maps remote chain selectors to token lock box addresses. The
// canonical lock-release pool now escrows in a lockbox (EVM `LockReleaseTokenPool.i_lockBox`
// parity), so the pool's own token balance equals only accrued fees and `withdraw_fee_tokens`
// can safely sweep the full balance.
type ConfigureLockBoxesInput struct {
	ContractID string                     `json:"contract_id"`
	Configs    []lrpbindings.LockBoxEntry `json:"configs"`
}

// ConfigureLockBoxes calls lock-release pool `configure_lock_boxes`.
var ConfigureLockBoxes = cldfops.NewOperation(
	"lock-release-pool:configure-lock-boxes",
	stellarops.ContractDeploymentVersion,
	"Maps remote chain selectors to token lock box addresses on the lock-release pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ConfigureLockBoxesInput) (stellarops.Void, error) {
		c := lrpbindings.NewLockReleasePoolClient(d.Invoker, in.ContractID)
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
	ContractID string                              `json:"contract_id"`
	Adds       []lrpbindings.TokenTransferFeeConfigArgs `json:"adds"`
	Disables   []uint64                            `json:"disables"`
}

// ApplyTokenFeeConfigUpdates calls lock-release pool `apply_token_fee_config_updates`.
var ApplyTokenFeeConfigUpdates = cldfops.NewOperation(
	"lock-release-pool:apply-token-fee-config-updates",
	stellarops.ContractDeploymentVersion,
	"Applies token-transfer fee config adds/disables on the lock-release pool (EVM applyTokenTransferFeeConfigUpdates parity)",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyTokenFeeConfigUpdatesInput) (stellarops.Void, error) {
		c := lrpbindings.NewLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.ApplyTokenFeeConfigUpdates(b.GetContext(), in.Adds, in.Disables); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// GetTokenTransferFeeConfigInput reads the per-chain token-transfer fee config.
type GetTokenTransferFeeConfigInput struct {
	ContractID          string `json:"contract_id"`
	DestChainSelector   uint64 `json:"dest_chain_selector"`
}

// GetTokenTransferFeeConfigOutput is the on-chain token-transfer fee config (a disabled
// config when none is stored).
type GetTokenTransferFeeConfigOutput struct {
	Config *lrpbindings.TokenTransferFeeConfig `json:"config"`
}

// GetTokenTransferFeeConfig calls lock-release pool `get_token_transfer_fee_config`.
var GetTokenTransferFeeConfig = cldfops.NewOperation(
	"lock-release-pool:get-token-transfer-fee-config",
	stellarops.ContractDeploymentVersion,
	"Reads the token-transfer fee config for a destination chain on the lock-release pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetTokenTransferFeeConfigInput) (GetTokenTransferFeeConfigOutput, error) {
		c := lrpbindings.NewLockReleasePoolClient(d.Invoker, in.ContractID)
		cfg, err := c.GetTokenTransferFeeConfig(b.GetContext(), in.DestChainSelector)
		if err != nil {
			return GetTokenTransferFeeConfigOutput{}, err
		}
		return GetTokenTransferFeeConfigOutput{Config: cfg}, nil
	},
)

// WithdrawFeeTokensInput sweeps accrued fee-token balances to a recipient (EVM
// `TokenPool.withdrawFeeTokens`). Callable on-chain by the owner or fee admin; the op uses the
// deployer/invoker which must be one of those. Safe to sweep the full pool balance because user
// liquidity is escrowed in the lockbox, not held on the pool address.
type WithdrawFeeTokensInput struct {
	ContractID string   `json:"contract_id"`
	FeeTokens  []string `json:"fee_tokens"`
	Recipient  string   `json:"recipient"`
}

// WithdrawFeeTokens calls lock-release pool `withdraw_fee_tokens`.
var WithdrawFeeTokens = cldfops.NewOperation(
	"lock-release-pool:withdraw-fee-tokens",
	stellarops.ContractDeploymentVersion,
	"Withdraws accrued fee-token balances to a recipient on the lock-release pool (EVM withdrawFeeTokens parity)",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in WithdrawFeeTokensInput) (stellarops.Void, error) {
		c := lrpbindings.NewLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.WithdrawFeeTokens(b.GetContext(), in.FeeTokens, in.Recipient); err != nil {
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

// SetFeeAdmin calls lock-release pool `set_fee_admin`.
var SetFeeAdmin = cldfops.NewOperation(
	"lock-release-pool:set-fee-admin",
	stellarops.ContractDeploymentVersion,
	"Sets the fee-admin address on the lock-release pool (EVM setDynamicConfig feeAdmin parity)",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetFeeAdminInput) (stellarops.Void, error) {
		c := lrpbindings.NewLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.SetFeeAdmin(b.GetContext(), in.FeeAdmin); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
