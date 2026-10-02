package changesets

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// DeployLinkTokenConfig is the input of the LINK token deploy changeset.
// Name/Symbol/Decimals default to "ChainLink Token" / "LINK" / 7 and the Owner
// (the initial token admin) to the deployer. The deploy covers the token only:
// the set_admin mint-authority handoff to the pool and the TokenAdminRegistry
// registration are separate operator steps (see RunDeployBnmToken for the
// onboarding order).
type DeployLinkTokenConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	Owner         string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath      string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
	Name          string `json:"name,omitempty" yaml:"name,omitempty"`
	Symbol        string `json:"symbol,omitempty" yaml:"symbol,omitempty"`
	Decimals      uint32 `json:"decimals,omitempty" yaml:"decimals,omitempty"`
}

// DeployLinkToken deploys and initializes the LINK token on one Stellar chain.
// No dependencies.
type DeployLinkToken struct{}

var _ cldf.ChangeSetV2[DeployLinkTokenConfig] = DeployLinkToken{}

func (DeployLinkToken) VerifyPreconditions(e cldf.Environment, cfg DeployLinkTokenConfig) error {
	return verifyComponent(e, cfg.ChainSelector, nil, nil)
}

func (DeployLinkToken) Apply(e cldf.Environment, cfg DeployLinkTokenConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, nil, cfg.WasmPath, "link_token.wasm", sequences.DeployLinkToken,
		func(_ map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployLinkTokenInput {
			return sequences.DeployLinkTokenInput{
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
