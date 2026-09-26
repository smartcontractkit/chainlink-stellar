package fee_quoter

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels FeeQuoter.
const ContractType = "FeeQuoter"

// Deploy uploads fee_quoter.wasm.
var Deploy = stellarops.NewDeployOperation("fee-quoter:deploy", "Deploys the FeeQuoter Soroban contract from WASM")

// InitializeInput configures FeeQuoter static config and price updaters.
type InitializeInput struct {
	ContractID        string                  `json:"contract_id"`
	Owner             string                  `json:"owner"`
	StaticConfig      fqbindings.StaticConfig `json:"static_config"`
	AuthorizedCallers []string                `json:"authorized_callers"`
}

// Initialize calls FeeQuoter `initialize`.
var Initialize = cldfops.NewOperation(
	"fee-quoter:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes FeeQuoter with owner, static config, and authorized callers",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		c := fqbindings.NewFeeQuoterClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.StaticConfig, in.AuthorizedCallers); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyDestChainConfigsInput applies destination gas/fee defaults on FeeQuoter.
type ApplyDestChainConfigsInput struct {
	ContractID string                           `json:"contract_id"`
	Configs    []fqbindings.DestChainConfigArgs `json:"configs"`
}

// ApplyDestChainConfigs calls FeeQuoter `apply_dest_chain_configs`.
var ApplyDestChainConfigs = cldfops.NewOperation(
	"fee-quoter:apply-dest-chain-configs",
	stellarops.ContractDeploymentVersion,
	"Applies FeeQuoter destination chain configurations",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyDestChainConfigsInput) (stellarops.Void, error) {
		c := fqbindings.NewFeeQuoterClient(d.Invoker, in.ContractID)
		if err := c.ApplyDestChainConfigs(b.GetContext(), in.Configs); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// UpdatePricesInput sets gas and token price updates on FeeQuoter.
type UpdatePricesInput struct {
	ContractID   string                  `json:"contract_id"`
	Updater      string                  `json:"updater"`
	PriceUpdates fqbindings.PriceUpdates `json:"price_updates"`
}

// UpdatePrices calls FeeQuoter `update_prices`.
var UpdatePrices = cldfops.NewOperation(
	"fee-quoter:update-prices",
	stellarops.ContractDeploymentVersion,
	"Updates gas and token prices on Stellar FeeQuoter",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UpdatePricesInput) (stellarops.Void, error) {
		c := fqbindings.NewFeeQuoterClient(d.Invoker, in.ContractID)
		if err := c.UpdatePrices(b.GetContext(), in.Updater, in.PriceUpdates); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyTokenFeeConfigsInput sets per-token transfer fee overrides on FeeQuoter.
type ApplyTokenFeeConfigsInput struct {
	ContractID string                                `json:"contract_id"`
	AddConfigs []fqbindings.TokenFeeConfigArgs       `json:"add_configs"`
	RemoveArgs []fqbindings.TokenFeeConfigRemoveArgs `json:"remove_args"`
}

// ApplyTokenFeeConfigs calls FeeQuoter `apply_token_fee_configs`.
var ApplyTokenFeeConfigs = cldfops.NewOperation(
	"fee-quoter:apply-token-fee-configs",
	stellarops.ContractDeploymentVersion,
	"Applies per-token transfer fee configurations on Stellar FeeQuoter",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyTokenFeeConfigsInput) (stellarops.Void, error) {
		c := fqbindings.NewFeeQuoterClient(d.Invoker, in.ContractID)
		if err := c.ApplyTokenFeeConfigs(b.GetContext(), in.AddConfigs, in.RemoveArgs); err != nil {
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

// TransferOwnership calls `transfer_ownership` on FeeQuoter.
var TransferOwnership = cldfops.NewOperation(
	"fee-quoter:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers FeeQuoter ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := fqbindings.NewFeeQuoterClient(d.Invoker, in.ContractID)
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

// AcceptOwnership calls `accept_ownership` on FeeQuoter.
var AcceptOwnership = cldfops.NewOperation(
	"fee-quoter:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts FeeQuoter ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := fqbindings.NewFeeQuoterClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// UpgradeInput upgrades the FeeQuoter contract in place to a new Wasm hash.
type UpgradeInput struct {
	ContractID  string   `json:"contract_id"`
	NewWasmHash [32]byte `json:"new_wasm_hash"`
}

// Upgrade calls `upgrade` on FeeQuoter (owner-gated in-place self-upgrade).
var Upgrade = cldfops.NewOperation(
	"fee-quoter:upgrade",
	stellarops.ContractDeploymentVersion,
	"Upgrades the FeeQuoter Soroban contract in place to a new WASM hash",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UpgradeInput) (stellarops.Void, error) {
		c := fqbindings.NewFeeQuoterClient(d.Invoker, in.ContractID)
		if err := c.Upgrade(b.GetContext(), in.NewWasmHash); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
