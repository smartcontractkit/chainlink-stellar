package link_token

import (
	"fmt"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	linkbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/link_token"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels LINK token contracts — the custom (non-SAC) Soroban
// LINK token used as the CCIP fee token.
const ContractType = "LinkToken"

// Deploy uploads link_token.wasm.
var Deploy = stellarops.NewDeployOperation("link-token:deploy", "Deploys the LINK Soroban token contract from WASM")

// InitializeInput matches LINK `initialize(admin, name, symbol, decimals)`.
type InitializeInput struct {
	ContractID string `json:"contract_id"`
	Admin      string `json:"admin"`
	Name       string `json:"name"`
	Symbol     string `json:"symbol"`
	Decimals   uint32 `json:"decimals"`
}

// Initialize calls LINK `initialize`. The deployer is set as the initial admin
// and later hands mint authority to the burn-mint pool via `set_admin` — that
// handoff is an operator step (as in the BnM onboarding sequence), not part of
// this op. No initial supply is minted; the pre-handoff mint is the only
// non-pool minting window.
var Initialize = cldfops.NewOperation(
	"link-token:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes the LINK token with admin, name, symbol, and decimals",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		if in.Admin == "" {
			return stellarops.Void{}, fmt.Errorf("link-token initialize: admin is required")
		}
		c := linkbindings.NewLinkTokenClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Admin, in.Name, in.Symbol, in.Decimals); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
