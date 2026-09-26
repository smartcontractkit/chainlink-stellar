package executor

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	executorbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/executor"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels the CCIP 2.0 Executor (source-side fee/policy surface).
const ContractType = "Executor"

// Deploy uploads executor.wasm.
var Deploy = stellarops.NewDeployOperation("executor:deploy", "Deploys the Executor Soroban contract from WASM")

// InitializeInput configures the Executor owner, max CCVs per message, and dynamic config.
type InitializeInput struct {
	ContractID    string                         `json:"contract_id"`
	Owner         string                         `json:"owner"`
	MaxCCVsPerMsg uint32                         `json:"max_ccvs_per_msg"`
	DynamicConfig executorbindings.DynamicConfig `json:"dynamic_config"`
}

// Initialize calls Executor `initialize`.
var Initialize = cldfops.NewOperation(
	"executor:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes the Executor with owner, max CCVs per message, and dynamic config",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		c := executorbindings.NewExecutorClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.MaxCCVsPerMsg, in.DynamicConfig); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyDestChainUpdatesInput applies per-destination-chain enable/fee settings on the Executor.
type ApplyDestChainUpdatesInput struct {
	ContractID string                                   `json:"contract_id"`
	ToRemove   []uint64                                 `json:"to_remove"`
	ToAdd      []executorbindings.RemoteChainConfigArgs `json:"to_add"`
}

// ApplyDestChainUpdates calls Executor `apply_dest_chain_updates`.
var ApplyDestChainUpdates = cldfops.NewOperation(
	"executor:apply-dest-chain-updates",
	stellarops.ContractDeploymentVersion,
	"Applies Executor destination chain configuration updates",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyDestChainUpdatesInput) (stellarops.Void, error) {
		c := executorbindings.NewExecutorClient(d.Invoker, in.ContractID)
		if err := c.ApplyDestChainUpdates(b.GetContext(), in.ToRemove, in.ToAdd); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// SetDynamicConfigInput updates the Executor dynamic config (fee aggregator, allowed finality, CCV allowlist).
type SetDynamicConfigInput struct {
	ContractID    string                         `json:"contract_id"`
	DynamicConfig executorbindings.DynamicConfig `json:"dynamic_config"`
}

// SetDynamicConfig calls Executor `set_dynamic_config`.
var SetDynamicConfig = cldfops.NewOperation(
	"executor:set-dynamic-config",
	stellarops.ContractDeploymentVersion,
	"Sets the Executor dynamic config",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetDynamicConfigInput) (stellarops.Void, error) {
		c := executorbindings.NewExecutorClient(d.Invoker, in.ContractID)
		if err := c.SetDynamicConfig(b.GetContext(), in.DynamicConfig); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyAllowedCcvUpdatesInput updates the Executor CCV allowlist.
type ApplyAllowedCcvUpdatesInput struct {
	ContractID          string   `json:"contract_id"`
	ToRemove            []string `json:"to_remove"`
	ToAdd               []string `json:"to_add"`
	CcvAllowlistEnabled bool     `json:"ccv_allowlist_enabled"`
}

// ApplyAllowedCcvUpdates calls Executor `apply_allowed_ccv_updates`.
var ApplyAllowedCcvUpdates = cldfops.NewOperation(
	"executor:apply-allowed-ccv-updates",
	stellarops.ContractDeploymentVersion,
	"Applies Executor allowed CCV updates",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyAllowedCcvUpdatesInput) (stellarops.Void, error) {
		c := executorbindings.NewExecutorClient(d.Invoker, in.ContractID)
		if err := c.ApplyAllowedCcvUpdates(b.GetContext(), in.ToRemove, in.ToAdd, in.CcvAllowlistEnabled); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// WithdrawFeeTokensInput lists fee token contract IDs to withdraw to the configured fee aggregator.
type WithdrawFeeTokensInput struct {
	ContractID string   `json:"contract_id"`
	FeeTokens  []string `json:"fee_tokens"`
}

// WithdrawFeeTokens calls Executor `withdraw_fee_tokens`.
var WithdrawFeeTokens = cldfops.NewOperation(
	"executor:withdraw-fee-tokens",
	stellarops.ContractDeploymentVersion,
	"Withdraws listed fee token balances to the Executor fee aggregator",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in WithdrawFeeTokensInput) (stellarops.Void, error) {
		c := executorbindings.NewExecutorClient(d.Invoker, in.ContractID)
		if err := c.WithdrawFeeTokens(b.GetContext(), in.FeeTokens); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// TransferOwnershipInput starts a two-step ownership transfer.
type TransferOwnershipInput struct {
	ContractID string `json:"contract_id"`
	NewOwner   string `json:"new_owner"`
}

// TransferOwnership calls `transfer_ownership` on the Executor.
var TransferOwnership = cldfops.NewOperation(
	"executor:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers Executor ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := executorbindings.NewExecutorClient(d.Invoker, in.ContractID)
		if err := c.TransferOwnership(b.GetContext(), in.NewOwner); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// AcceptOwnershipInput completes a two-step ownership transfer for the caller.
type AcceptOwnershipInput struct {
	ContractID string `json:"contract_id"`
}

// AcceptOwnership calls `accept_ownership` on the Executor.
var AcceptOwnership = cldfops.NewOperation(
	"executor:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts Executor ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := executorbindings.NewExecutorClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// UpgradeInput upgrades the Executor contract in place to a new Wasm hash.
type UpgradeInput struct {
	ContractID  string   `json:"contract_id"`
	NewWasmHash [32]byte `json:"new_wasm_hash"`
}

// Upgrade calls `upgrade` on Executor (owner-gated in-place self-upgrade).
var Upgrade = cldfops.NewOperation(
	"executor:upgrade",
	stellarops.ContractDeploymentVersion,
	"Upgrades the Executor Soroban contract in place to a new WASM hash",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UpgradeInput) (stellarops.Void, error) {
		c := executorbindings.NewExecutorClient(d.Invoker, in.ContractID)
		if err := c.Upgrade(b.GetContext(), in.NewWasmHash); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
