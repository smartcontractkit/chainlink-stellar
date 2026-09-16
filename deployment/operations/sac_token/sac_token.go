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
