package changesets

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// DeployRMNRemoteConfig is the input of the RMN Remote deploy changeset.
// CurseAdmins default to none; EnableFastCurse prepends the fast-curse
// timelock resolved from the datastore (qualifier FastCurseQualifier, default
// the Ultra Fast Curse qualifier).
type DeployRMNRemoteConfig struct {
	ChainSelector      uint64   `json:"chainSelector" yaml:"chainSelector"`
	Owner              string   `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath           string   `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
	CurseAdmins        []string `json:"curseAdmins,omitempty" yaml:"curseAdmins,omitempty"`
	EnableFastCurse    bool     `json:"enableFastCurse,omitempty" yaml:"enableFastCurse,omitempty"`
	FastCurseQualifier string   `json:"fastCurseQualifier,omitempty" yaml:"fastCurseQualifier,omitempty"`
}

// DeployRMNRemote deploys and initializes the RMN Remote contract on one
// Stellar chain. No dependencies.
type DeployRMNRemote struct{}

var _ cldf.ChangeSetV2[DeployRMNRemoteConfig] = DeployRMNRemote{}

func (DeployRMNRemote) VerifyPreconditions(e cldf.Environment, cfg DeployRMNRemoteConfig) error {
	return verifyComponent(e, cfg.ChainSelector, nil, nil)
}

func (DeployRMNRemote) Apply(e cldf.Environment, cfg DeployRMNRemoteConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, nil, cfg.WasmPath, "rmn_remote.wasm", sequences.DeployRMNRemote,
		func(_ map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployRMNRemoteInput {
			return sequences.DeployRMNRemoteInput{
				ChainSelector:      cfg.ChainSelector,
				Owner:              cfg.Owner,
				WasmPath:           wasmPath,
				CurseAdmins:        cfg.CurseAdmins,
				EnableFastCurse:    cfg.EnableFastCurse,
				FastCurseQualifier: cfg.FastCurseQualifier,
				ExistingAddresses:  existing,
			}
		})
}
