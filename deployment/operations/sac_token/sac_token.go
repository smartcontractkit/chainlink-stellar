package sac_token

import (
	"math/big"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// TransferInput is the input for a Soroban Asset Contract (SAC) `transfer` call.
type TransferInput struct {
	ContractID string `json:"contract_id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Amount     int64  `json:"amount"`
}

// Transfer calls `transfer` on a SAC token contract (e.g. for funding lock-release pools).
var Transfer = cldfops.NewOperation(
	"sac-token:transfer",
	stellarops.ContractDeploymentVersion,
	"Transfers SAC tokens between Stellar accounts/contracts",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferInput) (stellarops.Void, error) {
		args := []xdr.ScVal{
			scval.AddressToScVal(in.From),
			scval.AddressToScVal(in.To),
			scval.I128ToScVal(big.NewInt(in.Amount)),
		}
		_, err := d.Invoker.InvokeContract(b.GetContext(), in.ContractID, "transfer", args)
		if err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApproveInput is the input for a Soroban Asset Contract (SAC) `approve` call.
type ApproveInput struct {
	ContractID       string `json:"contract_id"`
	From             string `json:"from"`
	Spender          string `json:"spender"`
	Amount           int64  `json:"amount"`
	ExpirationLedger uint32 `json:"expiration_ledger"`
}

// Approve calls `approve` on a SAC token contract.
var Approve = cldfops.NewOperation(
	"sac-token:approve",
	stellarops.ContractDeploymentVersion,
	"Approves a SAC token spender up to expiration_ledger",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApproveInput) (stellarops.Void, error) {
		args := []xdr.ScVal{
			scval.AddressToScVal(in.From),
			scval.AddressToScVal(in.Spender),
			scval.I128ToScVal(big.NewInt(in.Amount)),
			scval.Uint32ToScVal(in.ExpirationLedger),
		}
		_, err := d.Invoker.InvokeContract(b.GetContext(), in.ContractID, "approve", args)
		if err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// SetAdminInput is the input for a SAC `set_admin` call. The contract's current
// admin must be the transaction's signer (the framework Deployer), so this op
// has no explicit caller argument — the signer IS the current admin, exactly as
// `Transfer`/`Approve` rely on the Deployer signing as `From`.
type SetAdminInput struct {
	ContractID string `json:"contract_id"`
	NewAdmin   string `json:"new_admin"`
}

// SetAdmin calls `set_admin` on a SAC token contract, handing token admin
// (mint/clawback/set_authorized authority) to `NewAdmin`. It is the burn-mint
// mint-authority handoff: the deployer (initial admin) transfers admin to the
// burn-mint token pool so the pool can mint on inbound bridge messages. The op
// is token-type-agnostic — it works for any contract exposing the SAC
// `set_admin(Address)` entrypoint (SAC assets and custom StellarAssetInterface
// tokens such as BnM).
var SetAdmin = cldfops.NewOperation(
	"sac-token:set-admin",
	stellarops.ContractDeploymentVersion,
	"Transfers SAC token admin (mint authority) to a new address",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetAdminInput) (stellarops.Void, error) {
		args := []xdr.ScVal{
			scval.AddressToScVal(in.NewAdmin),
		}
		_, err := d.Invoker.InvokeContract(b.GetContext(), in.ContractID, "set_admin", args)
		if err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
