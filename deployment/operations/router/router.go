package router

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels Router in datastore-style tooling.
const ContractType = "Router"

// Deploy uploads router.wasm.
var Deploy = stellarops.NewDeployOperation("router:deploy", "Deploys the Router Soroban contract from WASM")

// InitializeInput wires owner and RMN proxy to Router.
type InitializeInput struct {
	ContractID string `json:"contract_id"`
	Owner      string `json:"owner"`
	RmnProxy   string `json:"rmn_proxy"`
}

// Initialize calls Router `initialize`.
var Initialize = cldfops.NewOperation(
	"router:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes Router with owner and RMN proxy",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		c := routerbindings.NewRouterClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.RmnProxy); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyRampUpdatesInput updates on-ramp and off-ramp routing tables.
type ApplyRampUpdatesInput struct {
	ContractID     string                        `json:"contract_id"`
	OnRampUpdates  []routerbindings.OnRampEntry  `json:"on_ramp_updates"`
	OffRampRemoves []routerbindings.OffRampEntry `json:"off_ramp_removes"`
	OffRampAdds    []routerbindings.OffRampEntry `json:"off_ramp_adds"`
}

// ApplyRampUpdates calls Router `apply_ramp_updates`.
var ApplyRampUpdates = cldfops.NewOperation(
	"router:apply-ramp-updates",
	stellarops.ContractDeploymentVersion,
	"Applies Router on-ramp and off-ramp map updates",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyRampUpdatesInput) (stellarops.Void, error) {
		c := routerbindings.NewRouterClient(d.Invoker, in.ContractID)
		if err := c.ApplyRampUpdates(b.GetContext(), in.OnRampUpdates, in.OffRampRemoves, in.OffRampAdds); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// RemoveOnrampInput removes the OnRamp for a destination chain, pausing the lane.
type RemoveOnrampInput struct {
	ContractID        string `json:"contract_id"`
	DestChainSelector uint64 `json:"dest_chain_selector"`
}

// RemoveOnramp calls Router `remove_onramp`. ccip_send and get_fee revert with
// UnsupportedDestinationChain while no OnRamp is configured; re-enable with ApplyRampUpdates.
var RemoveOnramp = cldfops.NewOperation(
	"router:remove-onramp",
	stellarops.ContractDeploymentVersion,
	"Removes the Router OnRamp for a destination chain, pausing the lane",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in RemoveOnrampInput) (stellarops.Void, error) {
		c := routerbindings.NewRouterClient(d.Invoker, in.ContractID)
		if err := c.RemoveOnramp(b.GetContext(), in.DestChainSelector); err != nil {
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

// TransferOwnership calls `transfer_ownership` on Router.
var TransferOwnership = cldfops.NewOperation(
	"router:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers Router ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := routerbindings.NewRouterClient(d.Invoker, in.ContractID)
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

// AcceptOwnership calls `accept_ownership` on Router.
var AcceptOwnership = cldfops.NewOperation(
	"router:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts Router ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := routerbindings.NewRouterClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// UpgradeInput upgrades the Router contract in place to a new Wasm hash.
type UpgradeInput struct {
	ContractID  string   `json:"contract_id"`
	NewWasmHash [32]byte `json:"new_wasm_hash"`
}

// Upgrade calls `upgrade` on Router (owner-gated in-place self-upgrade).
var Upgrade = cldfops.NewOperation(
	"router:upgrade",
	stellarops.ContractDeploymentVersion,
	"Upgrades the Router Soroban contract in place to a new WASM hash",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UpgradeInput) (stellarops.Void, error) {
		c := routerbindings.NewRouterClient(d.Invoker, in.ContractID)
		if err := c.Upgrade(b.GetContext(), in.NewWasmHash); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// GetOnrampInput identifies a Router on-ramp routing read.
type GetOnrampInput struct {
	ContractID        string `json:"contract_id"`
	DestChainSelector uint64 `json:"dest_chain_selector"`
}

// GetOnramp reads Router `get_onramp` for one destination chain. It returns
// (nil, nil) when no OnRamp is routed — the contract errors with
// UnsupportedDestinationChain (#63) — which callers treat as "not routed, add
// the entry" (EVM read-before-write parity).
var GetOnramp = cldfops.NewOperation(
	"router:get-onramp",
	stellarops.ContractDeploymentVersion,
	"Reads the Router OnRamp address routed to one destination chain",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetOnrampInput) (*string, error) {
		c := routerbindings.NewRouterClient(d.Invoker, in.ContractID)
		out, err := c.GetOnramp(b.GetContext(), in.DestChainSelector)
		if err != nil {
			if stellarops.IsContractErrorCode(err, stellarops.UnsupportedDestinationChainCode) {
				return nil, nil
			}
			return nil, err
		}
		return &out, nil
	},
)

// GetOfframpsInput identifies a Router off-ramp table read.
type GetOfframpsInput struct {
	ContractID string `json:"contract_id"`
}

// GetOfframps reads the Router's full off-ramp routing table. An empty table
// (freshly deployed Router) returns an empty slice without error.
var GetOfframps = cldfops.NewOperation(
	"router:get-offramps",
	stellarops.ContractDeploymentVersion,
	"Reads the Router off-ramp routing table",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetOfframpsInput) ([]routerbindings.OffRampEntry, error) {
		c := routerbindings.NewRouterClient(d.Invoker, in.ContractID)
		return c.GetOfframps(b.GetContext())
	},
)
