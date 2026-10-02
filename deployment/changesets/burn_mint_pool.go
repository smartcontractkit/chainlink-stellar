package changesets

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// DeployBurnMintPoolConfig is the input of the burn-mint pool deploy changeset.
// The pool takes any custom token from config (BnM, LINK, or an issuer token);
// the Router/RampRegistry/RMN Proxy strkeys are resolved from the environment
// datastore, not configured here. The set_admin mint-authority handoff
// (custom token → pool) and the TokenAdminRegistry registration are separate
// operator steps (RunDeployBnmToken bundles the full onboarding), and
// per-remote-chain pool config is applied at lane-configuration time.
type DeployBurnMintPoolConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	Qualifier     string `json:"qualifier" yaml:"qualifier"`
	Owner         string `json:"owner,omitempty" yaml:"owner,omitempty"`
	WasmPath      string `json:"wasmPath,omitempty" yaml:"wasmPath,omitempty"`
	Token         string `json:"token" yaml:"token"`
	TokenDecimals uint32 `json:"tokenDecimals" yaml:"tokenDecimals"`
}

// DeployBurnMintPool deploys and initializes a burn-mint token pool on one
// Stellar chain. Requires Router, RampRegistry and RMN Proxy, a non-empty
// Token strkey with its decimals, and a non-empty Qualifier (more than one
// pool can live on a chain; the qualifier keeps their datastore refs, salts
// and op reports distinct — typically the token symbol).
type DeployBurnMintPool struct{}

var _ cldf.ChangeSetV2[DeployBurnMintPoolConfig] = DeployBurnMintPool{}

func (DeployBurnMintPool) VerifyPreconditions(e cldf.Environment, cfg DeployBurnMintPoolConfig) error {
	return verifyComponent(e, cfg.ChainSelector, poolStackDeps, func() error {
		if cfg.Token == "" {
			return fmt.Errorf("token is required: pass the token contract strkey")
		}
		if cfg.TokenDecimals == 0 {
			return fmt.Errorf("tokenDecimals is required")
		}
		if cfg.Qualifier == "" {
			return fmt.Errorf("qualifier is required: one pool per token, identify the instance (e.g. the token symbol)")
		}
		return nil
	})
}

func (DeployBurnMintPool) Apply(e cldf.Environment, cfg DeployBurnMintPoolConfig) (cldf.ChangesetOutput, error) {
	return applyComponent(e, cfg.ChainSelector, poolStackDeps, cfg.WasmPath, "pools_burn_mint_pool.wasm", sequences.DeployBurnMintPool,
		func(resolved map[string]string, wasmPath string, existing []datastore.AddressRef) sequences.DeployBurnMintPoolInput {
			return sequences.DeployBurnMintPoolInput{
				ChainSelector:     cfg.ChainSelector,
				Qualifier:         cfg.Qualifier,
				Owner:             cfg.Owner,
				WasmPath:          wasmPath,
				Token:             cfg.Token,
				TokenDecimals:     cfg.TokenDecimals,
				Router:            resolved["Router"],
				RampRegistry:      resolved["RampRegistry"],
				RmnProxy:          resolved["RMN Proxy"],
				ExistingAddresses: existing,
			}
		})
}
