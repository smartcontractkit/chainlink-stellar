package token_pool

import (
	"fmt"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	tpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType is used for generic token pool deployments (e.g. lock-release WASM behind this client).
const ContractType = "TokenPool"

// Deploy uploads pool WASM (e.g. pools_lock_release_pool.wasm).
var Deploy = stellarops.NewDeployOperation("token-pool:deploy", "Deploys a Soroban token pool contract from WASM")

// InitializeInput configures pool owner, token, decimals, router, ramp registry,
// and the RMN proxy used for remote-chain curse checks.
type InitializeInput struct {
	ContractID    string `json:"contract_id"`
	Owner         string `json:"owner"`
	Token         string `json:"token"`
	TokenDecimals uint32 `json:"token_decimals"`
	Router        string `json:"router"`
	RampRegistry  string `json:"ramp_registry"`
	// RmnProxy is the RMN proxy address stored immutably on the pool at initialize
	// (mirrors EVM TokenPool's `immutable i_rmnProxy` constructor arg — there is NO
	// set_rmn_proxy entrypoint). The pool consults it directly for curse checks in
	// lock_or_burn/release_or_mint (NOT via Router.get_config(), which would re-enter
	// the Router mid-ccip_send). MUST be the same RMN proxy the Router was initialized
	// with, and MUST be non-empty (EVM parity: the constructor reverts on the zero
	// address; Soroban has no zero address, so the op enforces non-empty here).
	RmnProxy string `json:"rmn_proxy"`
}

// Initialize calls token pool `initialize` with owner, token, router, ramp registry,
// and the immutable RMN proxy (EVM `immutable i_rmnProxy` parity — set once, no setter).
var Initialize = cldfops.NewOperation(
	"token-pool:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes token pool with owner, token, router, ramp registry, and RMN proxy",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		if in.RmnProxy == "" {
			return stellarops.Void{}, fmt.Errorf("token pool initialize: rmn_proxy is required (EVM i_rmnProxy parity, no zero address)")
		}
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.Token, in.TokenDecimals, in.Router, in.RampRegistry, in.RmnProxy); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// SetAdvancedPoolHooksInput wires an issuer-owned Advanced Pool Hooks contract
// to a token pool. The pool then relays the hooks' required CCVs
// (get_required_ccvs) to the OnRamp/OffRamp, so the token issuer's chosen CCV
// set is enforced for transfers of their token (CCV-7).
type SetAdvancedPoolHooksInput struct {
	ContractID string `json:"contract_id"` // the token pool contract id
	Hooks      string `json:"hooks"`       // the Advanced Pool Hooks contract id (issuer-owned)
}

// SetAdvancedPoolHooks calls the pool's `set_advanced_pool_hooks`. Pool-owner-gated:
// only the pool owner may wire hooks; for the issuer-driven flow the pool owner
// and the hooks owner are typically the same principal (the token issuer).
var SetAdvancedPoolHooks = cldfops.NewOperation(
	"token-pool:set-advanced-pool-hooks",
	stellarops.ContractDeploymentVersion,
	"Wires an issuer-owned Advanced Pool Hooks contract to a token pool (pool-owner-gated)",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetAdvancedPoolHooksInput) (stellarops.Void, error) {
		if in.Hooks == "" {
			return stellarops.Void{}, fmt.Errorf("token pool set_advanced_pool_hooks: hooks contract id is required")
		}
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		if err := c.SetAdvancedPoolHooks(b.GetContext(), in.Hooks); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// SetRateLimitConfigInput sets outbound and inbound rate limit configs for
// one remote chain and finality bucket. Caller is the invoker identity the
// contract authorizes (owner or rate-limit admin).
type SetRateLimitConfigInput struct {
	ContractID          string                        `json:"contract_id"`
	Caller              string                        `json:"caller"`
	RemoteChainSelector uint64                        `json:"remote_chain_selector"`
	OutboundConfig      tpoolbindings.RateLimitConfig `json:"outbound_config"`
	InboundConfig       tpoolbindings.RateLimitConfig `json:"inbound_config"`
	FastFinality        bool                          `json:"fast_finality"`
}

// SetRateLimitConfig calls pool `set_rate_limit_config` for one remote chain
// and finality bucket.
var SetRateLimitConfig = cldfops.NewOperation(
	"token-pool:set-rate-limit-config",
	stellarops.ContractDeploymentVersion,
	"Sets outbound and inbound rate limit configs for a remote chain on a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetRateLimitConfigInput) (stellarops.Void, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		if err := c.SetRateLimitConfig(b.GetContext(), in.Caller, in.RemoteChainSelector, in.OutboundConfig, in.InboundConfig, in.FastFinality); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// GetRateLimitAdminInput identifies a pool rate-limit admin read.
type GetRateLimitAdminInput struct {
	ContractID string `json:"contract_id"`
}

// GetRateLimitAdmin reads pool `get_rate_limit_admin`.
var GetRateLimitAdmin = cldfops.NewOperation(
	"token-pool:get-rate-limit-admin",
	stellarops.ContractDeploymentVersion,
	"Reads the rate-limit admin of a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetRateLimitAdminInput) (*string, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		return c.GetRateLimitAdmin(b.GetContext())
	},
)

// GetAllowedFinalityConfigInput identifies a pool allowed-finality read.
type GetAllowedFinalityConfigInput struct {
	ContractID string `json:"contract_id"`
}

// GetAllowedFinalityConfig reads pool `get_allowed_finality_config`.
var GetAllowedFinalityConfig = cldfops.NewOperation(
	"token-pool:get-allowed-finality-config",
	stellarops.ContractDeploymentVersion,
	"Reads the allowed finality config of a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetAllowedFinalityConfigInput) (uint32, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		return c.GetAllowedFinalityConfig(b.GetContext())
	},
)

// SetAllowedFinalityConfigInput sets the pool's allowed finality config.
type SetAllowedFinalityConfigInput struct {
	ContractID      string `json:"contract_id"`
	AllowedFinality uint32 `json:"allowed_finality"`
}

// SetAllowedFinalityConfig calls pool `set_allowed_finality_config` (no
// explicit caller: the contract require_auths the invoking owner).
var SetAllowedFinalityConfig = cldfops.NewOperation(
	"token-pool:set-allowed-finality-config",
	stellarops.ContractDeploymentVersion,
	"Sets the allowed finality config of a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetAllowedFinalityConfigInput) (stellarops.Void, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		if err := c.SetAllowedFinalityConfig(b.GetContext(), in.AllowedFinality); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// GetAdvancedPoolHooksInput identifies a pool AdvancedPoolHooks read.
type GetAdvancedPoolHooksInput struct {
	ContractID string `json:"contract_id"`
}

// GetAdvancedPoolHooks reads pool `get_advanced_pool_hooks`; nil means no
// hooks are configured (no zero address exists on Soroban).
var GetAdvancedPoolHooks = cldfops.NewOperation(
	"token-pool:get-advanced-pool-hooks",
	stellarops.ContractDeploymentVersion,
	"Reads the AdvancedPoolHooks wired to a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetAdvancedPoolHooksInput) (*string, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		return c.GetAdvancedPoolHooks(b.GetContext())
	},
)

// GetTokenInput identifies a pool token read.
type GetTokenInput struct {
	ContractID string `json:"contract_id"`
}

// GetToken reads pool `get_token` — the wrapped token's strkey.
var GetToken = cldfops.NewOperation(
	"token-pool:get-token",
	stellarops.ContractDeploymentVersion,
	"Reads the token a token pool wraps",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetTokenInput) (string, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		return c.GetToken(b.GetContext())
	},
)

// GetTokenDecimalsInput identifies a pool token decimals read.
type GetTokenDecimalsInput struct {
	ContractID string `json:"contract_id"`
}

// GetTokenDecimals reads pool `get_token_decimals`.
var GetTokenDecimals = cldfops.NewOperation(
	"token-pool:get-token-decimals",
	stellarops.ContractDeploymentVersion,
	"Reads the decimals of the token a token pool wraps",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetTokenDecimalsInput) (uint32, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		return c.GetTokenDecimals(b.GetContext())
	},
)

// IsSupportedChainInput identifies a pool remote-chain support read.
type IsSupportedChainInput struct {
	ContractID          string `json:"contract_id"`
	RemoteChainSelector uint64 `json:"remote_chain_selector"`
}

// IsSupportedChain reads pool `is_supported_chain` for one remote chain.
var IsSupportedChain = cldfops.NewOperation(
	"token-pool:is-supported-chain",
	stellarops.ContractDeploymentVersion,
	"Reads whether a token pool supports one remote chain",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in IsSupportedChainInput) (bool, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		return c.IsSupportedChain(b.GetContext(), in.RemoteChainSelector)
	},
)

// GetRemoteTokenInput identifies a pool remote-token read.
type GetRemoteTokenInput struct {
	ContractID          string `json:"contract_id"`
	RemoteChainSelector uint64 `json:"remote_chain_selector"`
}

// GetRemoteToken reads pool `get_remote_token` for one remote chain.
var GetRemoteToken = cldfops.NewOperation(
	"token-pool:get-remote-token",
	stellarops.ContractDeploymentVersion,
	"Reads the remote token bytes a token pool stores for one remote chain",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetRemoteTokenInput) ([]byte, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		return c.GetRemoteToken(b.GetContext(), in.RemoteChainSelector)
	},
)

// GetRemotePoolsInput identifies a pool remote-pool read.
type GetRemotePoolsInput struct {
	ContractID          string `json:"contract_id"`
	RemoteChainSelector uint64 `json:"remote_chain_selector"`
}

// GetRemotePools reads pool `get_remote_pools` for one remote chain.
var GetRemotePools = cldfops.NewOperation(
	"token-pool:get-remote-pools",
	stellarops.ContractDeploymentVersion,
	"Reads the remote pool addresses a token pool stores for one remote chain",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetRemotePoolsInput) ([][]byte, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		return c.GetRemotePools(b.GetContext(), in.RemoteChainSelector)
	},
)

// AddRemotePoolInput adds one remote pool entry on a pool. Owner-gated via the
// invoking identity (no explicit caller arg).
type AddRemotePoolInput struct {
	ContractID          string `json:"contract_id"`
	RemoteChainSelector uint64 `json:"remote_chain_selector"`
	RemotePoolAddress   []byte `json:"remote_pool_address"`
}

// AddRemotePool calls pool `add_remote_pool` for one remote chain.
var AddRemotePool = cldfops.NewOperation(
	"token-pool:add-remote-pool",
	stellarops.ContractDeploymentVersion,
	"Adds a remote pool entry for one remote chain on a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AddRemotePoolInput) (stellarops.Void, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		if err := c.AddRemotePool(b.GetContext(), in.RemoteChainSelector, in.RemotePoolAddress); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// RemoveRemotePoolInput removes one remote pool entry on a pool. Owner-gated
// via the invoking identity (no explicit caller arg); the entry is matched by
// its exact stored bytes.
type RemoveRemotePoolInput struct {
	ContractID          string `json:"contract_id"`
	RemoteChainSelector uint64 `json:"remote_chain_selector"`
	RemotePoolAddress   []byte `json:"remote_pool_address"`
}

// RemoveRemotePool calls pool `remove_remote_pool` for one remote chain.
var RemoveRemotePool = cldfops.NewOperation(
	"token-pool:remove-remote-pool",
	stellarops.ContractDeploymentVersion,
	"Removes a remote pool entry for one remote chain on a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in RemoveRemotePoolInput) (stellarops.Void, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		if err := c.RemoveRemotePool(b.GetContext(), in.RemoteChainSelector, in.RemotePoolAddress); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyChainUpdatesInput adds or removes remote chain configs on a pool.
// Owner-gated via the invoking identity (no explicit caller arg).
type ApplyChainUpdatesInput struct {
	ContractID string                      `json:"contract_id"`
	Adds       []tpoolbindings.ChainUpdate `json:"adds"`
	Removes    []uint64                    `json:"removes"`
}

// ApplyChainUpdates calls pool `apply_chain_updates`.
var ApplyChainUpdates = cldfops.NewOperation(
	"token-pool:apply-chain-updates",
	stellarops.ContractDeploymentVersion,
	"Adds or removes remote chain configs on a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyChainUpdatesInput) (stellarops.Void, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		if err := c.ApplyChainUpdates(b.GetContext(), in.Adds, in.Removes); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// GetCurrentRateLimiterStateInput identifies a pool rate-limit state read.
type GetCurrentRateLimiterStateInput struct {
	ContractID          string `json:"contract_id"`
	RemoteChainSelector uint64 `json:"remote_chain_selector"`
	FastFinality        bool   `json:"fast_finality"`
}

// GetCurrentRateLimiterState reads pool `get_current_rate_limiter_state` for
// one remote chain and finality bucket.
var GetCurrentRateLimiterState = cldfops.NewOperation(
	"token-pool:get-current-rate-limiter-state",
	stellarops.ContractDeploymentVersion,
	"Reads the current rate limiter bucket state for one remote chain on a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetCurrentRateLimiterStateInput) (*tpoolbindings.RateLimiterState, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		return c.GetCurrentRateLimiterState(b.GetContext(), in.RemoteChainSelector, in.FastFinality)
	},
)

// GetRouterInput identifies a pool router read.
type GetRouterInput struct {
	ContractID string `json:"contract_id"`
}

// GetRouter reads pool `get_router`; nil means no router is set.
var GetRouter = cldfops.NewOperation(
	"token-pool:get-router",
	stellarops.ContractDeploymentVersion,
	"Reads the router of a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetRouterInput) (*string, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		return c.GetRouter(b.GetContext())
	},
)

// GetFeeAdminInput identifies a pool fee-admin read.
type GetFeeAdminInput struct {
	ContractID string `json:"contract_id"`
}

// GetFeeAdmin reads pool `get_fee_admin`; nil means no fee admin is set.
var GetFeeAdmin = cldfops.NewOperation(
	"token-pool:get-fee-admin",
	stellarops.ContractDeploymentVersion,
	"Reads the fee admin of a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetFeeAdminInput) (*string, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		return c.GetFeeAdmin(b.GetContext())
	},
)

// SetRouterInput sets the pool's router. Owner-only on chain.
type SetRouterInput struct {
	ContractID string `json:"contract_id"`
	Router     string `json:"router"`
}

// SetRouter calls pool `set_router` (no explicit caller: the contract
// require_auths the invoking owner).
var SetRouter = cldfops.NewOperation(
	"token-pool:set-router",
	stellarops.ContractDeploymentVersion,
	"Sets the router of a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetRouterInput) (stellarops.Void, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		if err := c.SetRouter(b.GetContext(), in.Router); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// SetRateLimitAdminInput sets the pool's rate-limit admin. Owner-only on chain.
type SetRateLimitAdminInput struct {
	ContractID string `json:"contract_id"`
	Admin      string `json:"admin"`
}

// SetRateLimitAdmin calls pool `set_rate_limit_admin` (no explicit caller: the
// contract require_auths the invoking owner).
var SetRateLimitAdmin = cldfops.NewOperation(
	"token-pool:set-rate-limit-admin",
	stellarops.ContractDeploymentVersion,
	"Sets the rate-limit admin of a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetRateLimitAdminInput) (stellarops.Void, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		if err := c.SetRateLimitAdmin(b.GetContext(), in.Admin); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// SetFeeAdminInput sets the pool's fee admin. Owner-only on chain.
type SetFeeAdminInput struct {
	ContractID string `json:"contract_id"`
	FeeAdmin   string `json:"fee_admin"`
}

// SetFeeAdmin calls pool `set_fee_admin` (no explicit caller: the contract
// require_auths the invoking owner).
var SetFeeAdmin = cldfops.NewOperation(
	"token-pool:set-fee-admin",
	stellarops.ContractDeploymentVersion,
	"Sets the fee admin of a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetFeeAdminInput) (stellarops.Void, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		if err := c.SetFeeAdmin(b.GetContext(), in.FeeAdmin); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// GetTokenTransferFeeConfigInput identifies a pool per-remote fee config read.
type GetTokenTransferFeeConfigInput struct {
	ContractID        string `json:"contract_id"`
	DestChainSelector uint64 `json:"dest_chain_selector"`
}

// GetTokenTransferFeeConfig reads pool `get_token_transfer_fee_config` for one
// destination chain.
var GetTokenTransferFeeConfig = cldfops.NewOperation(
	"token-pool:get-token-transfer-fee-config",
	stellarops.ContractDeploymentVersion,
	"Reads the token transfer fee config for one destination chain on a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetTokenTransferFeeConfigInput) (*tpoolbindings.TokenTransferFeeConfig, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		return c.GetTokenTransferFeeConfig(b.GetContext(), in.DestChainSelector)
	},
)

// ApplyTokenFeeConfigUpdatesInput applies pool per-remote fee config updates.
// Owner-only on chain.
type ApplyTokenFeeConfigUpdatesInput struct {
	ContractID string                                     `json:"contract_id"`
	Adds       []tpoolbindings.TokenTransferFeeConfigArgs `json:"adds"`
	Disables   []uint64                                   `json:"disables"`
}

// ApplyTokenFeeConfigUpdates calls pool `apply_token_fee_config_updates` (no
// explicit caller: the contract require_auths the invoking owner).
var ApplyTokenFeeConfigUpdates = cldfops.NewOperation(
	"token-pool:apply-token-fee-config-updates",
	stellarops.ContractDeploymentVersion,
	"Applies per-remote token transfer fee config updates on a token pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyTokenFeeConfigUpdatesInput) (stellarops.Void, error) {
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		if err := c.ApplyTokenFeeConfigUpdates(b.GetContext(), in.Adds, in.Disables); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
