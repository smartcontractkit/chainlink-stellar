package sequences

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	bnmops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/bnm_token"
)

// DeployBnmTokenContractInput deploys and initializes the BnM test token — the custom
// (non-SAC) Soroban token implementing the full token::StellarAssetInterface
// that burn-mint token pools mint/burn. Name/Symbol/Decimals default to
// "CCIP BnM" / "BnM" / 7. The deployer is the initial admin (and owner); the
// set_admin mint-authority handoff to the pool and the TokenAdminRegistry
// registration are operator steps (see RunDeployBnmToken for the one-shot
// onboarding). No initial supply is minted.
type DeployBnmTokenContractInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	Name              string                 `json:"name,omitempty"`
	Symbol            string                 `json:"symbol,omitempty"`
	Decimals          uint32                 `json:"decimals,omitempty"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployBnmToken = cldf_ops.NewSequence(
	"stellar-deploy-bnm-token",
	SequenceVersion,
	"Deploys and initializes the BnM Soroban token with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployBnmTokenContractInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		name, symbol, decimals := bnmTokenDefaults(in.Name, in.Symbol, in.Decimals)
		if err := statReleaseWasm(in.WasmPath, "BnM token"); err != nil {
			return ComponentDeployOutput{}, err
		}
		return deployAndInitialize(b.GetContext(), b, deps, bnmops.Deploy,
			stellarccip.BnmTokenDatastoreRef(), in.ChainSelector, "bnm-token", in.WasmPath, owner, in.ExistingAddresses,
			func(contractID string) error {
				_, err := execStellarCCIPOp(b, deps.StellarDeps, bnmops.Initialize, bnmops.InitializeInput{
					ContractID: contractID,
					Admin:      owner,
					Name:       name,
					Symbol:     symbol,
					Decimals:   decimals,
				}, withComponentIdempotencyKey[bnmops.InitializeInput](in.ChainSelector))
				return err
			})
	},
)

// bnmTokenDefaults fills the BnM token metadata defaults (the same values
// RunDeployBnmToken applies).
func bnmTokenDefaults(name, symbol string, decimals uint32) (string, string, uint32) {
	if name == "" {
		name = "CCIP BnM"
	}
	if symbol == "" {
		symbol = "BnM"
	}
	if decimals == 0 {
		decimals = 7
	}
	return name, symbol, decimals
}
