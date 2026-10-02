package changesets

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// DeployTokenLockBoxConfig is the input of the TokenLockBox deploy changeset.
// The box takes the token it holds from config; a canonical lock-release pool
// takes the box strkey in its own config (DeployLockReleasePool).
type DeployTokenLockBoxConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	Qualifier     string `json:"qualifier" yaml:"qualifier"`
	Owner         string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath      string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
	Token         string `json:"token" yaml:"token"`
}

// DeployTokenLockBox deploys and initializes a TokenLockBox on one Stellar
// chain. Requires a non-empty Token strkey and a non-empty Qualifier (more
// than one box can live on a chain — one per pool/token; the qualifier keeps
// their datastore refs, salts and op reports distinct, typically the token
// symbol matching the pool it serves).
type DeployTokenLockBox struct{}

var _ cldf.ChangeSetV2[DeployTokenLockBoxConfig] = DeployTokenLockBox{}

func (DeployTokenLockBox) VerifyPreconditions(e cldf.Environment, cfg DeployTokenLockBoxConfig) error {
	return verifyComponent(e, cfg.ChainSelector, nil, func() error {
		if cfg.Token == "" {
			return fmt.Errorf("token is required: pass the token contract strkey")
		}
		if cfg.Qualifier == "" {
			return fmt.Errorf("qualifier is required: one lock box per pool, identify the instance (e.g. the token symbol)")
		}
		return nil
	})
}

func (DeployTokenLockBox) Apply(e cldf.Environment, cfg DeployTokenLockBoxConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, nil, cfg.WasmPath, "pools_token_lock_box.wasm", sequences.DeployTokenLockBox,
		func(_ map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployTokenLockBoxInput {
			return sequences.DeployTokenLockBoxInput{
				ChainSelector:     cfg.ChainSelector,
				Qualifier:         cfg.Qualifier,
				Owner:             cfg.Owner,
				WasmPath:          wasmPath,
				Token:             cfg.Token,
				ExistingAddresses: existing,
			}
		})
}
