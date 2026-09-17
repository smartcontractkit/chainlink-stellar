package token_lock_box

import (
	"math/big"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	tlbbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_lock_box"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels token lock box contracts.
const ContractType = "TokenLockBox"

// Deploy uploads pools_token_lock_box.wasm.
var Deploy = stellarops.NewDeployOperation("token-lock-box:deploy", "Deploys the token lock box Soroban contract from WASM")

// InitializeInput configures lock box owner and token.
type InitializeInput struct {
	ContractID string `json:"contract_id"`
	Owner      string `json:"owner"`
	Token      string `json:"token"`
}

// Initialize calls token lock box `initialize`.
var Initialize = cldfops.NewOperation(
	"token-lock-box:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes token lock box with owner and token",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		c := tlbbindings.NewTokenLockBoxClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.Token); err != nil {
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

// TransferOwnership calls `transfer_ownership` on token lock box.
var TransferOwnership = cldfops.NewOperation(
	"token-lock-box:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers token lock box ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := tlbbindings.NewTokenLockBoxClient(d.Invoker, in.ContractID)
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

// AcceptOwnership calls `accept_ownership` on token lock box.
var AcceptOwnership = cldfops.NewOperation(
	"token-lock-box:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts token lock box ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := tlbbindings.NewTokenLockBoxClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// AddAllowedCallersInput adds pool addresses to the lock box allowlist (owner-only).
type AddAllowedCallersInput struct {
	ContractID string   `json:"contract_id"`
	Callers    []string `json:"callers"`
}

// AddAllowedCallers calls token lock box `add_allowed_callers`.
var AddAllowedCallers = cldfops.NewOperation(
	"token-lock-box:add-allowed-callers",
	stellarops.ContractDeploymentVersion,
	"Adds allowed caller addresses that may deposit or withdraw from the lock box",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AddAllowedCallersInput) (stellarops.Void, error) {
		c := tlbbindings.NewTokenLockBoxClient(d.Invoker, in.ContractID)
		if err := c.AddAllowedCallers(b.GetContext(), in.Callers); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// RemoveAllowedCallersInput removes pool addresses from the lock box allowlist (owner-only).
type RemoveAllowedCallersInput struct {
	ContractID string   `json:"contract_id"`
	Callers    []string `json:"callers"`
}

// RemoveAllowedCallers calls token lock box `remove_allowed_callers`.
var RemoveAllowedCallers = cldfops.NewOperation(
	"token-lock-box:remove-allowed-callers",
	stellarops.ContractDeploymentVersion,
	"Removes allowed caller addresses from the lock box allowlist",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in RemoveAllowedCallersInput) (stellarops.Void, error) {
		c := tlbbindings.NewTokenLockBoxClient(d.Invoker, in.ContractID)
		if err := c.RemoveAllowedCallers(b.GetContext(), in.Callers); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// DepositInput pulls tokens from caller into the lock box (allowed caller only).
type DepositInput struct {
	ContractID string `json:"contract_id"`
	Caller     string `json:"caller"`
	Amount     int64  `json:"amount"`
}

// Deposit calls token lock box `deposit`.
var Deposit = cldfops.NewOperation(
	"token-lock-box:deposit",
	stellarops.ContractDeploymentVersion,
	"Deposits tokens from an allowed caller into the lock box",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in DepositInput) (stellarops.Void, error) {
		c := tlbbindings.NewTokenLockBoxClient(d.Invoker, in.ContractID)
		if err := c.Deposit(b.GetContext(), in.Caller, big.NewInt(in.Amount)); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// WithdrawInput sends tokens from the lock box to a recipient (allowed caller only).
type WithdrawInput struct {
	ContractID string `json:"contract_id"`
	Caller     string `json:"caller"`
	Amount     int64  `json:"amount"`
	Recipient  string `json:"recipient"`
}

// Withdraw calls token lock box `withdraw`.
var Withdraw = cldfops.NewOperation(
	"token-lock-box:withdraw",
	stellarops.ContractDeploymentVersion,
	"Withdraws tokens from the lock box to a recipient on behalf of an allowed caller",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in WithdrawInput) (stellarops.Void, error) {
		c := tlbbindings.NewTokenLockBoxClient(d.Invoker, in.ContractID)
		if err := c.Withdraw(b.GetContext(), in.Caller, big.NewInt(in.Amount), in.Recipient); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
