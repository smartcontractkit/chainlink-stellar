package timelock

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	tlbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/timelock"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels Timelock.
const ContractType = "Timelock"

// Deploy uploads timelock.wasm.
var Deploy = stellarops.NewDeployOperation("timelock:deploy", "Deploys the Timelock Soroban contract from WASM")

// InitializeInput configures timelock roles and minimum delay.
type InitializeInput struct {
	ContractID string   `json:"contract_id"`
	MinDelay   uint64   `json:"min_delay"`
	Admin      string   `json:"admin"`
	Proposers  []string `json:"proposers"`
	Executors  []string `json:"executors"`
	Cancellers []string `json:"cancellers"`
	Bypassers  []string `json:"bypassers"`
}

// Initialize calls Timelock `initialize`.
var Initialize = cldfops.NewOperation(
	"timelock:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes Timelock with delay, admin, and role holders",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		c := tlbindings.NewTimelockClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.MinDelay, in.Admin, in.Proposers, in.Executors, in.Cancellers, in.Bypassers); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// GrantRoleInput grants a role on Timelock to an account. Used by Deployer to grant ADMIN_ROLE
// to the Timelock itself so role administration goes through scheduled ops.
type GrantRoleInput struct {
	ContractID string `json:"contract_id"`
	Caller     string `json:"caller"`
	Role       string `json:"role"`
	Account    string `json:"account"`
}

// GrantRole calls Timelock `grant_role`.
var GrantRole = cldfops.NewOperation(
	"timelock:grant-role",
	stellarops.ContractDeploymentVersion,
	"Grants a Timelock role (e.g. ADMIN_ROLE) to an account",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GrantRoleInput) (stellarops.Void, error) {
		c := tlbindings.NewTimelockClient(d.Invoker, in.ContractID)
		if err := c.GrantRole(b.GetContext(), in.Caller, in.Role, in.Account); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// RevokeRoleInput revokes a role on Timelock from an account.
type RevokeRoleInput struct {
	ContractID string `json:"contract_id"`
	Caller     string `json:"caller"`
	Role       string `json:"role"`
	Account    string `json:"account"`
}

// RevokeRole calls Timelock `revoke_role`.
var RevokeRole = cldfops.NewOperation(
	"timelock:revoke-role",
	stellarops.ContractDeploymentVersion,
	"Revokes a Timelock role from an account",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in RevokeRoleInput) (stellarops.Void, error) {
		c := tlbindings.NewTimelockClient(d.Invoker, in.ContractID)
		if err := c.RevokeRole(b.GetContext(), in.Caller, in.Role, in.Account); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// UpdateDelayInput changes the Timelock's minimum delay (must be scheduled through itself).
type UpdateDelayInput struct {
	ContractID string `json:"contract_id"`
	Caller     string `json:"caller"`
	NewDelay   uint64 `json:"new_delay"`
}

// UpdateDelay calls Timelock `update_delay`.
var UpdateDelay = cldfops.NewOperation(
	"timelock:update-delay",
	stellarops.ContractDeploymentVersion,
	"Updates Timelock minimum delay",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UpdateDelayInput) (stellarops.Void, error) {
		c := tlbindings.NewTimelockClient(d.Invoker, in.ContractID)
		if err := c.UpdateDelay(b.GetContext(), in.Caller, in.NewDelay); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// GetMinDelayInput identifies the Timelock instance to read the configured min delay from.
type GetMinDelayInput struct {
	ContractID string `json:"contract_id"`
}

// GetMinDelayOutput is the current Timelock min delay (in seconds).
type GetMinDelayOutput struct {
	MinDelay uint64 `json:"min_delay"`
}

// GetMinDelay calls Timelock `get_min_delay` (simulation, read-only).
var GetMinDelay = cldfops.NewOperation(
	"timelock:get-min-delay",
	stellarops.ContractDeploymentVersion,
	"Reads the current Timelock minimum delay via simulation",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetMinDelayInput) (GetMinDelayOutput, error) {
		c := tlbindings.NewTimelockClient(d.Invoker, in.ContractID)
		delay, err := c.GetMinDelay(b.GetContext())
		if err != nil {
			return GetMinDelayOutput{}, err
		}
		return GetMinDelayOutput{MinDelay: delay}, nil
	},
)
