package burn_mint_pool

import (
	"fmt"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	bmpbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/burn_mint_pool"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels burn-mint pool contracts.
const ContractType = "BurnMintPool"

// Deploy uploads pools_burn_mint_pool.wasm.
var Deploy = stellarops.NewDeployOperation("burn-mint-pool:deploy", "Deploys the burn-mint pool Soroban contract from WASM")

// InitializeInput matches burn-mint pool `initialize` (same shape as lock-release pool).
type InitializeInput struct {
	ContractID    string `json:"contract_id"`
	Owner         string `json:"owner"`
	Token         string `json:"token"`
	TokenDecimals uint32 `json:"token_decimals"`
	Router        string `json:"router"`
	RampRegistry  string `json:"ramp_registry"`
	// RmnProxy is the RMN proxy stored immutably on the pool at initialize (mirrors EVM
	// TokenPool's `immutable i_rmnProxy` constructor arg — there is NO set_rmn_proxy
	// entrypoint). The pool consults it directly for curse checks in
	// lock_or_burn/release_or_mint. Must match the Router's RMN proxy and be non-empty
	// (EVM parity: the constructor reverts on the zero address; Soroban has no zero
	// address, so the op enforces non-empty here).
	RmnProxy string `json:"rmn_proxy"`
}

// Initialize calls burn-mint pool `initialize` with owner, token, router, ramp registry,
// and the immutable RMN proxy (EVM `immutable i_rmnProxy` parity — set once, no setter).
var Initialize = cldfops.NewOperation(
	"burn-mint-pool:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes burn-mint pool with owner, token, router, ramp registry, and RMN proxy",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		if in.RmnProxy == "" {
			return stellarops.Void{}, fmt.Errorf("burn-mint pool initialize: rmn_proxy is required (EVM i_rmnProxy parity, no zero address)")
		}
		c := bmpbindings.NewBurnMintPoolClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.Token, in.TokenDecimals, in.Router, in.RampRegistry, in.RmnProxy); err != nil {
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

// TransferOwnership calls `transfer_ownership` on burn-mint pool.
var TransferOwnership = cldfops.NewOperation(
	"burn-mint-pool:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers burn-mint pool ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := bmpbindings.NewBurnMintPoolClient(d.Invoker, in.ContractID)
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

// AcceptOwnership calls `accept_ownership` on burn-mint pool.
var AcceptOwnership = cldfops.NewOperation(
	"burn-mint-pool:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts burn-mint pool ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := bmpbindings.NewBurnMintPoolClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
