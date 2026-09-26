package rmn_remote

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	rmnremotebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_remote"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels RMN Remote.
const ContractType = "RmnRemote"

// Deploy uploads rmn_remote.wasm.
var Deploy = stellarops.NewDeployOperation("rmn-remote:deploy", "Deploys the RMN Remote Soroban contract from WASM")

// InitializeInput configures RMN Remote owner and optional curse admins.
type InitializeInput struct {
	ContractID  string   `json:"contract_id"`
	Owner       string   `json:"owner"`
	CurseAdmins []string `json:"curse_admins,omitempty"`
}

// Initialize calls RMN Remote `initialize`.
var Initialize = cldfops.NewOperation(
	"rmn-remote:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes RMN Remote with owner and curse admins",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		c := rmnremotebindings.NewRmnRemoteClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.CurseAdmins); err != nil {
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

// TransferOwnership calls `transfer_ownership` on RMN Remote.
var TransferOwnership = cldfops.NewOperation(
	"rmn-remote:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers RMN Remote ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := rmnremotebindings.NewRmnRemoteClient(d.Invoker, in.ContractID)
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

// AcceptOwnership calls `accept_ownership` on RMN Remote.
var AcceptOwnership = cldfops.NewOperation(
	"rmn-remote:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts RMN Remote ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := rmnremotebindings.NewRmnRemoteClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// CurseInput holds the contract ID, curse-admin caller, and subjects to curse.
type CurseInput struct {
	ContractID string     `json:"contract_id"`
	Caller     string     `json:"caller"`
	Subjects   [][16]byte `json:"subjects"`
}

// UncurseInput holds the contract ID and subjects to uncurse (owner-only on chain).
type UncurseInput struct {
	ContractID string     `json:"contract_id"`
	Subjects   [][16]byte `json:"subjects"`
}

// Curse calls `curse` on RMN Remote with the given subjects.
var Curse = cldfops.NewOperation(
	"rmn-remote:curse",
	stellarops.ContractDeploymentVersion,
	"Curses subjects on Stellar RMN Remote",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in CurseInput) (stellarops.Void, error) {
		c := rmnremotebindings.NewRmnRemoteClient(d.Invoker, in.ContractID)
		if err := c.Curse(b.GetContext(), in.Caller, in.Subjects); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// Uncurse calls `uncurse` on RMN Remote with the given subjects.
var Uncurse = cldfops.NewOperation(
	"rmn-remote:uncurse",
	stellarops.ContractDeploymentVersion,
	"Uncurses subjects on Stellar RMN Remote",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UncurseInput) (stellarops.Void, error) {
		c := rmnremotebindings.NewRmnRemoteClient(d.Invoker, in.ContractID)
		if err := c.Uncurse(b.GetContext(), in.Subjects); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyCurseAdminUpdatesInput holds admins to add and remove (owner-only on chain).
type ApplyCurseAdminUpdatesInput struct {
	ContractID    string   `json:"contract_id"`
	AddedAdmins   []string `json:"added_admins,omitempty"`
	RemovedAdmins []string `json:"removed_admins,omitempty"`
}

// ApplyCurseAdminUpdates calls `apply_curse_admin_updates` on RMN Remote.
var ApplyCurseAdminUpdates = cldfops.NewOperation(
	"rmn-remote:apply-curse-admin-updates",
	stellarops.ContractDeploymentVersion,
	"Adds and removes curse admins on Stellar RMN Remote",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyCurseAdminUpdatesInput) (stellarops.Void, error) {
		c := rmnremotebindings.NewRmnRemoteClient(d.Invoker, in.ContractID)
		if err := c.ApplyCurseAdminUpdates(b.GetContext(), in.AddedAdmins, in.RemovedAdmins); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// GetCurseAdminsInput holds the contract ID to read curse admins from.
type GetCurseAdminsInput struct {
	ContractID string `json:"contract_id"`
}

// GetCurseAdminsOutput holds the stored curse-admin list (simulation read).
type GetCurseAdminsOutput struct {
	Admins []string `json:"admins"`
}

// GetCurseAdmins reads `get_curse_admins` on RMN Remote (simulation).
var GetCurseAdmins = cldfops.NewOperation(
	"rmn-remote:get-curse-admins",
	stellarops.ContractDeploymentVersion,
	"Reads the stored curse-admin list on Stellar RMN Remote",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetCurseAdminsInput) (GetCurseAdminsOutput, error) {
		c := rmnremotebindings.NewRmnRemoteClient(d.Invoker, in.ContractID)
		admins, err := c.GetCurseAdmins(b.GetContext())
		if err != nil {
			return GetCurseAdminsOutput{}, err
		}
		return GetCurseAdminsOutput{Admins: admins}, nil
	},
)

// UpgradeInput upgrades the RMN Remote contract in place to a new Wasm hash.
type UpgradeInput struct {
	ContractID  string   `json:"contract_id"`
	NewWasmHash [32]byte `json:"new_wasm_hash"`
}

// Upgrade calls `upgrade` on RMN Remote (owner-gated in-place self-upgrade).
var Upgrade = cldfops.NewOperation(
	"rmn-remote:upgrade",
	stellarops.ContractDeploymentVersion,
	"Upgrades the RMN Remote Soroban contract in place to a new WASM hash",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UpgradeInput) (stellarops.Void, error) {
		c := rmnremotebindings.NewRmnRemoteClient(d.Invoker, in.ContractID)
		if err := c.Upgrade(b.GetContext(), in.NewWasmHash); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
