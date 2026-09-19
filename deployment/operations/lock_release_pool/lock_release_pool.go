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
