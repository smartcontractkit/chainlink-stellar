package token_pool

import (
	"fmt"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	tpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType is used for generic token pool deployments (e.g. lock-release WASM behind this client).
const ContractType = "TokenPool"

// Deploy uploads pool WASM (e.g. pools_lock_release_pool.wasm).
var Deploy = stellarops.NewDeployOperation("token-pool:deploy", "Deploys a Soroban token pool contract from WASM")

// InitializeInput configures pool owner, token, decimals, router, ramp registry,
// and the RMN proxy used for remote-chain curse checks.
type InitializeInput struct {
	ContractID    string `json:"contract_id"`
	Owner         string `json:"owner"`
	Token         string `json:"token"`
	TokenDecimals uint32 `json:"token_decimals"`
	Router        string `json:"router"`
	RampRegistry  string `json:"ramp_registry"`
	// RmnProxy is the RMN proxy address stored immutably on the pool at initialize
	// (mirrors EVM TokenPool's `immutable i_rmnProxy` constructor arg — there is NO
	// set_rmn_proxy entrypoint). The pool consults it directly for curse checks in
	// lock_or_burn/release_or_mint (NOT via Router.get_config(), which would re-enter
	// the Router mid-ccip_send). MUST be the same RMN proxy the Router was initialized
	// with, and MUST be non-empty (EVM parity: the constructor reverts on the zero
	// address; Soroban has no zero address, so the op enforces non-empty here).
	RmnProxy string `json:"rmn_proxy"`
}

// Initialize calls token pool `initialize` with owner, token, router, ramp registry,
// and the immutable RMN proxy (EVM `immutable i_rmnProxy` parity — set once, no setter).
var Initialize = cldfops.NewOperation(
	"token-pool:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes token pool with owner, token, router, ramp registry, and RMN proxy",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		if in.RmnProxy == "" {
			return stellarops.Void{}, fmt.Errorf("token pool initialize: rmn_proxy is required (EVM i_rmnProxy parity, no zero address)")
		}
		c := tpoolbindings.NewTokenPoolClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.Token, in.TokenDecimals, in.Router, in.RampRegistry, in.RmnProxy); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
