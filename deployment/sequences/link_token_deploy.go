package sequences

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	lnkops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/link_token"
)

// DeployLinkTokenInput deploys and initializes the LINK token — the custom
// (non-SAC) Soroban LINK token used as the CCIP fee token. Name/Symbol/Decimals
// default to "ChainLink Token" / "LINK" / 7 (the deliberate Stellar divergence
// from EVM's 18 decimals). The deployer is the initial admin (and owner); the
// set_admin mint-authority handoff to the pool and the TokenAdminRegistry
// registration are operator steps (see RunDeployBnmToken for the onboarding
// order). No initial supply is minted — mint authority moves to the pool via
// set_admin, and the pre-handoff window is the only operator minting window.
type DeployLinkTokenInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	Name              string                 `json:"name,omitempty"`
	Symbol            string                 `json:"symbol,omitempty"`
	Decimals          uint32                 `json:"decimals,omitempty"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployLinkToken = cldf_ops.NewSequence(
	"stellar-deploy-link-token",
	SequenceVersion,
	"Deploys and initializes the LINK Soroban token with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployLinkTokenInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		name, symbol, decimals := linkTokenDefaults(in.Name, in.Symbol, in.Decimals)
		if err := statReleaseWasm(in.WasmPath, "LINK token"); err != nil {
			return ComponentDeployOutput{}, err
		}
		return deployAndInitialize(b.GetContext(), b, deps, lnkops.Deploy,
			stellarccip.LinkTokenDatastoreRef(), in.ChainSelector, "link-token", in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, lnkops.Initialize, lnkops.InitializeInput{
					ContractID: contractID,
					Admin:      owner,
					Name:       name,
					Symbol:     symbol,
					Decimals:   decimals,
				}, withComponentIdempotencyKey[lnkops.InitializeInput](in.ChainSelector))
				return err
			})
	},
)

// linkTokenDefaults fills the LINK token metadata defaults.
func linkTokenDefaults(name, symbol string, decimals uint32) (string, string, uint32) {
	if name == "" {
		name = "ChainLink Token"
	}
	if symbol == "" {
		symbol = "LINK"
	}
	if decimals == 0 {
		decimals = 7
	}
	return name, symbol, decimals
}
