package bnm_token

import (
	"fmt"
	"math/big"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ContractType labels BnM token contracts.
const ContractType = "BnmToken"

// Deploy uploads bnm_token.wasm. The BnM token is a custom Soroban contract
// implementing the full `token::StellarAssetInterface`, so the existing
// `BurnMintTokenPool` can mint/burn it with no pool-side changes.
var Deploy = stellarops.NewDeployOperation("bnm-token:deploy", "Deploys the BnM Soroban token contract from WASM")

// InitializeInput matches BnM `initialize(admin, name, symbol, decimals)`.
type InitializeInput struct {
	ContractID string `json:"contract_id"`
	Admin      string `json:"admin"`
	Name       string `json:"name"`
	Symbol     string `json:"symbol"`
	Decimals   uint32 `json:"decimals"`
}

// Initialize calls BnM `initialize`. The deployer is set as the initial admin
// and later hands mint authority to the burn-mint pool via `set_admin` (see the
// `sac_token.SetAdmin` op). No initial supply is minted — BnM is remotely
// issued; supply tracks bridge flow (mint on inbound, burn on outbound).
//
// Uses inline `xdr.ScVal` args (no generated binding client) so the op compiles
// without the `stellar` CLI binding-generation step; BnM is registered in the
// binding-gen manifests so macOS can emit a Go binding for future use.
var Initialize = cldfops.NewOperation(
	"bnm-token:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes the BnM token with admin, name, symbol, and decimals",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		if in.Admin == "" {
			return stellarops.Void{}, fmt.Errorf("bnm-token initialize: admin is required")
		}
		args := []xdr.ScVal{
			scval.AddressToScVal(in.Admin),
			scval.StringToScVal(in.Name),
			scval.StringToScVal(in.Symbol),
			scval.Uint32ToScVal(in.Decimals),
		}
		if _, err := d.Invoker.InvokeContract(b.GetContext(), in.ContractID, "initialize", args); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// DripInput matches BnM `drip(to)`.
type DripInput struct {
	ContractID string `json:"contract_id"`
	To         string `json:"to"`
}

// Drip calls BnM `drip(to)` — the permissionless faucet that mints 0.1 token
// (10^6 at 7 decimals) to `to`. No admin/signer requirement (the contract
// performs no `require_auth`), matching EVM `BurnMintERC20WithDrip.drip(to)`.
var Drip = cldfops.NewOperation(
	"bnm-token:drip",
	stellarops.ContractDeploymentVersion,
	"Mints 0.1 BnM (permissionless drip faucet) to the given address",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in DripInput) (stellarops.Void, error) {
		if in.To == "" {
			return stellarops.Void{}, fmt.Errorf("bnm-token drip: to is required")
		}
		args := []xdr.ScVal{
			scval.AddressToScVal(in.To),
		}
		if _, err := d.Invoker.InvokeContract(b.GetContext(), in.ContractID, "drip", args); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// AdminInput is the input for the BnM `admin` read.
type AdminInput struct {
	ContractID string `json:"contract_id"`
}

// AdminOutput is the output of the BnM `admin` read.
type AdminOutput struct {
	Admin string `json:"admin"`
}

// Admin reads the BnM token's current admin (the address holding mint
// authority — typically the burn-mint pool after the `set_admin` handoff).
var Admin = cldfops.NewOperation(
	"bnm-token:admin",
	stellarops.ContractDeploymentVersion,
	"Reads the BnM token admin address",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AdminInput) (AdminOutput, error) {
		res, err := d.Invoker.SimulateContract(b.GetContext(), in.ContractID, "admin", nil)
		if err != nil {
			return AdminOutput{}, err
		}
		if res == nil {
			return AdminOutput{}, fmt.Errorf("bnm-token admin: nil simulation result")
		}
		admin, err := scval.AddressFromScVal(*res)
		if err != nil {
			return AdminOutput{}, fmt.Errorf("bnm-token admin: decode: %w", err)
		}
		return AdminOutput{Admin: admin}, nil
	},
)

// DecimalsInput is the input for the BnM `decimals` read.
type DecimalsInput struct {
	ContractID string `json:"contract_id"`
}

// DecimalsOutput is the output of the BnM `decimals` read.
type DecimalsOutput struct {
	Decimals uint32 `json:"decimals"`
}

// Decimals reads the BnM token decimals (7, the Stellar SAC convention).
var Decimals = cldfops.NewOperation(
	"bnm-token:decimals",
	stellarops.ContractDeploymentVersion,
	"Reads the BnM token decimals",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in DecimalsInput) (DecimalsOutput, error) {
		res, err := d.Invoker.SimulateContract(b.GetContext(), in.ContractID, "decimals", nil)
		if err != nil {
			return DecimalsOutput{}, err
		}
		if res == nil {
			return DecimalsOutput{}, fmt.Errorf("bnm-token decimals: nil simulation result")
		}
		decimals, err := scval.Uint32FromScVal(*res)
		if err != nil {
			return DecimalsOutput{}, fmt.Errorf("bnm-token decimals: decode: %w", err)
		}
		return DecimalsOutput{Decimals: decimals}, nil
	},
)

// BalanceInput is the input for the BnM `balance` read.
type BalanceInput struct {
	ContractID string `json:"contract_id"`
	ID         string `json:"id"`
}

// BalanceOutput is the output of the BnM `balance` read.
type BalanceOutput struct {
	Balance *big.Int `json:"balance"`
}

// Balance reads the BnM balance of `id` as an i128 (returned as *big.Int since
// i128 exceeds int64 range).
var Balance = cldfops.NewOperation(
	"bnm-token:balance",
	stellarops.ContractDeploymentVersion,
	"Reads the BnM token balance of an address",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in BalanceInput) (BalanceOutput, error) {
		args := []xdr.ScVal{
			scval.AddressToScVal(in.ID),
		}
		res, err := d.Invoker.SimulateContract(b.GetContext(), in.ContractID, "balance", args)
		if err != nil {
			return BalanceOutput{}, err
		}
		if res == nil {
			return BalanceOutput{}, fmt.Errorf("bnm-token balance: nil simulation result")
		}
		bal, err := scval.I128FromScVal(*res)
		if err != nil {
			return BalanceOutput{}, fmt.Errorf("bnm-token balance: decode: %w", err)
		}
		return BalanceOutput{Balance: bal}, nil
	},
)
