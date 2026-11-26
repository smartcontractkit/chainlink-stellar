package rmn_proxy

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	rmnproxybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_proxy"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels RMN Proxy.
const ContractType = "RmnProxy"

// Deploy uploads rmn_proxy.wasm.
var Deploy = stellarops.NewDeployOperation("rmn-proxy:deploy", "Deploys the RMN Proxy Soroban contract from WASM")

// TransferOwnershipInput starts two-step ownership transfer.
type TransferOwnershipInput struct {
	ContractID string `json:"contract_id"`
	NewOwner   string `json:"new_owner"`
}

// TransferOwnership calls `transfer_ownership` on RMN Proxy.
var TransferOwnership = cldfops.NewOperation(
	"rmn-proxy:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers RMN Proxy ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := rmnproxybindings.NewRmnProxyClient(d.Invoker, in.ContractID)
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

// AcceptOwnership calls `accept_ownership` on RMN Proxy.
var AcceptOwnership = cldfops.NewOperation(
	"rmn-proxy:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts RMN Proxy ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := rmnproxybindings.NewRmnProxyClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// UpgradeInput upgrades the RMN Proxy contract in place to a new Wasm hash.
type UpgradeInput struct {
	ContractID  string   `json:"contract_id"`
	NewWasmHash [32]byte `json:"new_wasm_hash"`
}

// Upgrade calls `upgrade` on RMN Proxy (owner-gated in-place self-upgrade).
var Upgrade = cldfops.NewOperation(
	"rmn-proxy:upgrade",
	stellarops.ContractDeploymentVersion,
	"Upgrades the RMN Proxy Soroban contract in place to a new WASM hash",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UpgradeInput) (stellarops.Void, error) {
		c := rmnproxybindings.NewRmnProxyClient(d.Invoker, in.ContractID)
		if err := c.Upgrade(b.GetContext(), in.NewWasmHash); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
