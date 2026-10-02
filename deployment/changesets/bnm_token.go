package changesets

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// DeployBnmTokenConfig is the input of the BnM token deploy changeset.
// Name/Symbol/Decimals default to "CCIP BnM" / "BnM" / 7 and the Owner (the
// initial token admin) to the deployer. The deploy covers the token only: the
// set_admin mint-authority handoff to the pool and the TokenAdminRegistry
// registration are separate steps (RunDeployBnmToken bundles the full
// onboarding).
type DeployBnmTokenConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	Owner         string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath      string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
	Name          string `json:"name,omitempty" yaml:"name,omitempty"`
	Symbol        string `json:"symbol,omitempty" yaml:"symbol,omitempty"`
	Decimals      uint32 `json:"decimals,omitempty" yaml:"decimals,omitempty"`
}

// DeployBnmToken deploys and initializes the BnM test token on one Stellar
// chain. No dependencies.
type DeployBnmToken struct{}

var _ cldf.ChangeSetV2[DeployBnmTokenConfig] = DeployBnmToken{}

func (DeployBnmToken) VerifyPreconditions(e cldf.Environment, cfg DeployBnmTokenConfig) error {
	return verifyComponent(e, cfg.ChainSelector, nil, nil)
}

func (DeployBnmToken) Apply(e cldf.Environment, cfg DeployBnmTokenConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, nil, cfg.WasmPath, "bnm_token.wasm", sequences.DeployBnmToken,
		func(_ map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployBnmTokenContractInput {
			return sequences.DeployBnmTokenContractInput{
				ChainSelector:     cfg.ChainSelector,
				Owner:             cfg.Owner,
				WasmPath:          wasmPath,
				Name:              cfg.Name,
				Symbol:            cfg.Symbol,
				Decimals:          cfg.Decimals,
				ExistingAddresses: existing,
			}
		})
}
