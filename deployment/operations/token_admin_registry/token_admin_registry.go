package token_admin_registry

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels TokenAdminRegistry.
const ContractType = "TokenAdminRegistry"

// Deploy uploads token_admin_registry.wasm.
var Deploy = stellarops.NewDeployOperation("token-admin-registry:deploy", "Deploys the TokenAdminRegistry Soroban contract from WASM")

// InitializeInput sets registry owner.
type InitializeInput struct {
	ContractID string `json:"contract_id"`
	Owner      string `json:"owner"`
}

// Initialize calls TokenAdminRegistry `initialize`.
var Initialize = cldfops.NewOperation(
	"token-admin-registry:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes TokenAdminRegistry with owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		c := tarbindings.NewTokenAdminRegistryClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ProposeAdministratorInput proposes a token administrator.
type ProposeAdministratorInput struct {
	ContractID    string `json:"contract_id"`
	Caller        string `json:"caller"`
	LocalToken    string `json:"local_token"`
	Administrator string `json:"administrator"`
}

// ProposeAdministrator calls `propose_administrator`.
var ProposeAdministrator = cldfops.NewOperation(
	"token-admin-registry:propose-administrator",
	stellarops.ContractDeploymentVersion,
	"Proposes a token administrator in TokenAdminRegistry",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ProposeAdministratorInput) (stellarops.Void, error) {
		c := tarbindings.NewTokenAdminRegistryClient(d.Invoker, in.ContractID)
		if err := c.ProposeAdministrator(b.GetContext(), in.Caller, in.LocalToken, in.Administrator); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// AcceptAdminRoleInput accepts the proposed admin role for a token.
type AcceptAdminRoleInput struct {
	ContractID string `json:"contract_id"`
	LocalToken string `json:"local_token"`
}

// AcceptAdminRole calls `accept_admin_role`.
var AcceptAdminRole = cldfops.NewOperation(
	"token-admin-registry:accept-admin-role",
	stellarops.ContractDeploymentVersion,
	"Accepts administrator role for a token in TokenAdminRegistry",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptAdminRoleInput) (stellarops.Void, error) {
		c := tarbindings.NewTokenAdminRegistryClient(d.Invoker, in.ContractID)
		if err := c.AcceptAdminRole(b.GetContext(), in.LocalToken); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// SetPoolInput registers the default pool for a token.
type SetPoolInput struct {
	ContractID string  `json:"contract_id"`
	LocalToken string  `json:"local_token"`
	Pool       *string `json:"pool,omitempty"`
}

// SetPool calls `set_pool`.
var SetPool = cldfops.NewOperation(
	"token-admin-registry:set-pool",
	stellarops.ContractDeploymentVersion,
	"Sets the default pool for a token in TokenAdminRegistry",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetPoolInput) (stellarops.Void, error) {
		c := tarbindings.NewTokenAdminRegistryClient(d.Invoker, in.ContractID)
		if err := c.SetPool(b.GetContext(), in.LocalToken, in.Pool); err != nil {
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

// TransferOwnership calls `transfer_ownership` on TokenAdminRegistry.
var TransferOwnership = cldfops.NewOperation(
	"token-admin-registry:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers TokenAdminRegistry ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := tarbindings.NewTokenAdminRegistryClient(d.Invoker, in.ContractID)
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

// AcceptOwnership calls `accept_ownership` on TokenAdminRegistry.
var AcceptOwnership = cldfops.NewOperation(
	"token-admin-registry:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts TokenAdminRegistry ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := tarbindings.NewTokenAdminRegistryClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// UpgradeInput upgrades the TokenAdminRegistry contract in place to a new Wasm hash.
type UpgradeInput struct {
	ContractID  string   `json:"contract_id"`
	NewWasmHash [32]byte `json:"new_wasm_hash"`
}

// Upgrade calls `upgrade` on TokenAdminRegistry (owner-gated in-place self-upgrade).
var Upgrade = cldfops.NewOperation(
	"token-admin-registry:upgrade",
	stellarops.ContractDeploymentVersion,
	"Upgrades the TokenAdminRegistry Soroban contract in place to a new WASM hash",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UpgradeInput) (stellarops.Void, error) {
		c := tarbindings.NewTokenAdminRegistryClient(d.Invoker, in.ContractID)
		if err := c.Upgrade(b.GetContext(), in.NewWasmHash); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
