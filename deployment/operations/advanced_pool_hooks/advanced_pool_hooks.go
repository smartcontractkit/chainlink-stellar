package advanced_pool_hooks

import (
	"math/big"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	aphbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/advanced_pool_hooks"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels Advanced Pool Hooks (the per-token, issuer-owned hook
// contract through which a token issuer / pool owner configures the CCV set
// their token pool requires — CCIP 2.0 "AdvancedPoolHooks").
const ContractType = "AdvancedPoolHooks"

// Deploy uploads pools_advanced_pool_hooks.wasm.
//
// Unlike lane-mandated CCVs (Ramp-owner/Chainlink-gated) and pool-level config
// (pool-owner-gated), an Advanced Pool Hooks contract is owned by the TOKEN
// ISSUER: the issuer deploys it, initializes it with themselves as owner, wires
// it to their token pool, and then uses apply_ccv_config_updates to require the
// CCV(s) they choose for their token's transfers. This is the deployment entry
// point for that issuer-driven flow (CCV-7 deployment tooling).
var Deploy = stellarops.NewDeployOperation("advanced-pool-hooks:deploy", "Deploys the Advanced Pool Hooks Soroban contract from WASM")

// InitializeInput configures an Advanced Pool Hooks contract: its owner (the
// token issuer), the allowlist of senders permitted to use the pool, the
// per-message threshold amount above which the hook applies enhanced checks, and
// the initial set of authorized callers — the pools permitted to invoke
// `preflight_check`/`postflight_check` (EVM `AuthorizedCallers`).
type InitializeInput struct {
	ContractID        string   `json:"contract_id"`
	Owner             string   `json:"owner"`
	Allowlist         []string `json:"allowlist"`
	ThresholdAmount   *big.Int `json:"threshold_amount"`   // i128; defaults to 0 (hook applies to all amounts)
	AuthorizedCallers []string `json:"authorized_callers"` // pool addresses allowed to invoke the hooks (EVM `_validateCaller`)
}

// Initialize calls Advanced Pool Hooks `initialize` with the issuer as owner.
//
// The owner set here is the HOOKS owner — the only principal permitted to call
// apply_ccv_config_updates / apply_allowlist_updates / apply_authorized_callers_updates.
// For the issuer-driven CCV flow this MUST be the token issuer (who is typically
// also the pool owner). `AuthorizedCallers` should include the pool(s) that will
// be wired to these hooks via `token_pool.SetAdvancedPoolHooks`, since only
// authorized callers can pass the hooks' `_validateCaller` gate at invocation.
var Initialize = cldfops.NewOperation(
	"advanced-pool-hooks:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes Advanced Pool Hooks with an issuer owner, allowlist, threshold amount, and authorized callers",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		threshold := in.ThresholdAmount
		if threshold == nil {
			threshold = big.NewInt(0) // scval.I128ToScVal panics on nil; 0 = hook applies to every amount
		}
		c := aphbindings.NewAdvancedPoolHooksClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.Allowlist, threshold, in.AuthorizedCallers); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyCCVConfigUpdatesInput applies per-remote-chain CCV configuration on an
// Advanced Pool Hooks contract. Each CCVConfigArg sets the required inbound and
// outbound CCVs (and whether to additionally include lane defaults) for one
// remote chain selector. This is the operation a token issuer runs to require
// their own chosen CCV(s) on transfers of their token (CCV-7).
type ApplyCCVConfigUpdatesInput struct {
	ContractID string                     `json:"contract_id"`
	Configs    []aphbindings.CCVConfigArg `json:"configs"`
}

// ApplyCCVConfigUpdates calls Advanced Pool Hooks `apply_ccv_config_updates`.
// Hooks-owner-gated (the token issuer); the pool relays the resulting required
// CCVs to the OnRamp/OffRamp via get_required_ccvs.
var ApplyCCVConfigUpdates = cldfops.NewOperation(
	"advanced-pool-hooks:apply-ccv-config-updates",
	stellarops.ContractDeploymentVersion,
	"Applies per-remote-chain CCV configuration (required CCVs + include-defaults) on Advanced Pool Hooks",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyCCVConfigUpdatesInput) (stellarops.Void, error) {
		c := aphbindings.NewAdvancedPoolHooksClient(d.Invoker, in.ContractID)
		if err := c.ApplyCcvConfigUpdates(b.GetContext(), in.Configs); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyAllowlistUpdatesInput updates the sender allowlist on an Advanced Pool
// Hooks contract (hooks-owner-gated).
type ApplyAllowlistUpdatesInput struct {
	ContractID string   `json:"contract_id"`
	Removes    []string `json:"removes"`
	Adds       []string `json:"adds"`
}

// ApplyAllowlistUpdates calls Advanced Pool Hooks `apply_allowlist_updates`.
var ApplyAllowlistUpdates = cldfops.NewOperation(
	"advanced-pool-hooks:apply-allowlist-updates",
	stellarops.ContractDeploymentVersion,
	"Updates the sender allowlist on Advanced Pool Hooks",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyAllowlistUpdatesInput) (stellarops.Void, error) {
		c := aphbindings.NewAdvancedPoolHooksClient(d.Invoker, in.ContractID)
		if err := c.ApplyAllowlistUpdates(b.GetContext(), in.Removes, in.Adds); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyAuthorizedCallersUpdatesInput updates the authorized-callers set on an
// Advanced Pool Hooks contract (hooks-owner-gated) — the pools permitted to
// invoke `preflight_check`/`postflight_check` (EVM `AuthorizedCallers`).
// Removals are applied first, then adds.
type ApplyAuthorizedCallersUpdatesInput struct {
	ContractID string   `json:"contract_id"`
	Removes    []string `json:"removes"`
	Adds       []string `json:"adds"`
}

// ApplyAuthorizedCallersUpdates calls Advanced Pool Hooks
// `apply_authorized_callers_updates`.
var ApplyAuthorizedCallersUpdates = cldfops.NewOperation(
	"advanced-pool-hooks:apply-authorized-callers-updates",
	stellarops.ContractDeploymentVersion,
	"Updates the authorized pool callers (EVM AuthorizedCallers) on Advanced Pool Hooks",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyAuthorizedCallersUpdatesInput) (stellarops.Void, error) {
		c := aphbindings.NewAdvancedPoolHooksClient(d.Invoker, in.ContractID)
		if err := c.ApplyAuthorizedCallersUpdates(b.GetContext(), in.Removes, in.Adds); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// TransferOwnershipInput starts two-step ownership transfer of the hooks
// contract (e.g. issuer → MCMS after initial configuration).
type TransferOwnershipInput struct {
	ContractID string `json:"contract_id"`
	NewOwner   string `json:"new_owner"`
}

// TransferOwnership calls `transfer_ownership` on Advanced Pool Hooks.
var TransferOwnership = cldfops.NewOperation(
	"advanced-pool-hooks:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers Advanced Pool Hooks ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := aphbindings.NewAdvancedPoolHooksClient(d.Invoker, in.ContractID)
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

// AcceptOwnership calls `accept_ownership` on Advanced Pool Hooks.
var AcceptOwnership = cldfops.NewOperation(
	"advanced-pool-hooks:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts Advanced Pool Hooks ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := aphbindings.NewAdvancedPoolHooksClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// UpgradeInput upgrades the Advanced Pool Hooks contract in place to a new Wasm hash.
type UpgradeInput struct {
	ContractID  string   `json:"contract_id"`
	NewWasmHash [32]byte `json:"new_wasm_hash"`
}

// Upgrade calls `upgrade` on Advanced Pool Hooks (owner-gated in-place self-upgrade).
var Upgrade = cldfops.NewOperation(
	"advanced-pool-hooks:upgrade",
	stellarops.ContractDeploymentVersion,
	"Upgrades the Advanced Pool Hooks Soroban contract in place to a new WASM hash",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UpgradeInput) (stellarops.Void, error) {
		c := aphbindings.NewAdvancedPoolHooksClient(d.Invoker, in.ContractID)
		if err := c.Upgrade(b.GetContext(), in.NewWasmHash); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
